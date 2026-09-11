package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"quickdock/internal/platform"
	pluginmgr "quickdock/internal/plugin"
)

// ===== 插件文件系统能力（host.fs.*）=====
//
// 权限与路径 scope 的判定在 internal/plugin（invokeHostMethod → checkFSPermission），
// 本文件只负责「拿到已被校验的真实路径后怎么读写」。两者共用
// pluginmgr.ResolveRealPath，保证判定用的路径与真正 I/O 的路径是同一个。
//
// 设计约束：
//   - 所有方法都必须经 resolveFSParam 取路径——它同时做二次 scope 校验，
//     绕过它就等于绕过白名单。
//   - write 走「同目录临时文件 + rename」，避免写一半被中断留下半截文件。
//   - write **不自动创建父目录**：路径创建是独立能力（host.fs.mkdir），
//     少一个隐式的目录创建路径就少一处越权可能。
//   - remove 走 platform.MoveToTrash（系统回收站），不使用 os.Remove。

const (
	pluginFSMaxRead  = 8 << 20 // 单次读取上限 8 MiB，超限直接报错而非截断（截断后写回会毁数据）
	pluginFSMaxWrite = 8 << 20 // 单次写入上限 8 MiB
	pluginFSMaxList  = 2000    // 单目录返回条目上限，防超大目录把响应撑爆
)

// registerFSHostMethods 注入 host.fs.* 实现。
// 方法名与 internal/plugin 的 fsHostMethods 表一一对应——那边是权限声明，
// 这里是实现，两边都要加，漏了会被权限层按「未知方法」拒绝。
func (svc *PluginService) registerFSHostMethods() {
	mgr := svc.App.PluginMgr

	// ---- 读 ----
	mgr.InjectHostMethod("host.fs.read", func(pluginID string, params json.RawMessage) (interface{}, error) {
		real, err := svc.resolveFSParam(pluginID, params, "path", pluginmgr.FSActionRead)
		if err != nil {
			return nil, err
		}
		return fsReadFile(real)
	})

	mgr.InjectHostMethod("host.fs.list", func(pluginID string, params json.RawMessage) (interface{}, error) {
		real, err := svc.resolveFSParam(pluginID, params, "path", pluginmgr.FSActionRead)
		if err != nil {
			return nil, err
		}
		entries, truncated, err := fsListDir(real)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"path": real, "entries": entries, "truncated": truncated}, nil
	})

	mgr.InjectHostMethod("host.fs.stat", func(pluginID string, params json.RawMessage) (interface{}, error) {
		real, err := svc.resolveFSParam(pluginID, params, "path", pluginmgr.FSActionRead)
		if err != nil {
			return nil, err
		}
		return fsStat(real)
	})

	mgr.InjectHostMethod("host.fs.exists", func(pluginID string, params json.RawMessage) (interface{}, error) {
		real, err := svc.resolveFSParam(pluginID, params, "path", pluginmgr.FSActionRead)
		if err != nil {
			return nil, err
		}
		st, err := fsStat(real)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"path": real, "exists": st["exists"]}, nil
	})

	// ---- 写 ----
	mgr.InjectHostMethod("host.fs.write", func(pluginID string, params json.RawMessage) (interface{}, error) {
		real, err := svc.resolveFSParam(pluginID, params, "path", pluginmgr.FSActionWrite)
		if err != nil {
			return nil, err
		}
		var arg struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		data, err := decodeFSContent(arg.Content, arg.Encoding)
		if err != nil {
			return nil, err
		}
		if err := fsWriteFile(real, data); err != nil {
			return nil, err
		}
		return map[string]interface{}{"path": real, "size": len(data)}, nil
	})

	mgr.InjectHostMethod("host.fs.mkdir", func(pluginID string, params json.RawMessage) (interface{}, error) {
		real, err := svc.resolveFSParam(pluginID, params, "path", pluginmgr.FSActionWrite)
		if err != nil {
			return nil, err
		}
		var arg struct {
			Parents bool `json:"parents"`
		}
		_ = json.Unmarshal(params, &arg)

		if arg.Parents {
			// 沿途各级都落在同一已校验前缀之下，不会越出 scope
			if err := os.MkdirAll(real, 0o755); err != nil {
				return nil, fmt.Errorf("创建目录失败: %w", err)
			}
		} else if err := os.Mkdir(real, 0o755); err != nil {
			return nil, fmt.Errorf("创建目录失败: %w", err)
		}
		return map[string]interface{}{"path": real}, nil
	})

	// ---- 删除 / 移动 ----
	//
	// remove 走回收站而不是 os.Remove：插件能删的东西不该比宿主删得更彻底。
	// 与 write 一样归 write scope——把文件从目录里挪走和往目录里写，破坏力同级。
	mgr.InjectHostMethod("host.fs.remove", func(pluginID string, params json.RawMessage) (interface{}, error) {
		real, err := svc.resolveFSParam(pluginID, params, "path", pluginmgr.FSActionWrite)
		if err != nil {
			return nil, err
		}
		if err := platform.MoveToTrash(real); err != nil {
			return nil, err
		}
		return map[string]interface{}{"path": real, "removed": true, "trash": true}, nil
	})

	mgr.InjectHostMethod("host.fs.move", func(pluginID string, params json.RawMessage) (interface{}, error) {
		from, err := svc.resolveFSParam(pluginID, params, "from", pluginmgr.FSActionWrite)
		if err != nil {
			return nil, err
		}
		to, err := svc.resolveFSParam(pluginID, params, "to", pluginmgr.FSActionWrite)
		if err != nil {
			return nil, err
		}
		if err := fsMove(from, to); err != nil {
			return nil, err
		}
		return map[string]interface{}{"from": from, "to": to, "moved": true}, nil
	})
}

// resolveFSParam 取 params 中的路径字段，解析为绝对真实路径并做二次 scope 校验。
//
// 权限层（invokeHostMethod → checkFSPermission）已经校验过一次，这里再校验一次
// 并非冗余保险，而是因为两者必须共用同一个解析函数：判定用的路径与真正 I/O 的
// 路径若各解析一次、结果不一致，白名单就形同虚设。调用同一函数的代价是多一次
// EvalSymlinks，远低于路径被解析成两个不同结果的风险。
func (svc *PluginService) resolveFSParam(pluginID string, params json.RawMessage, field, action string) (string, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(params, &args); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	var raw string
	if v, ok := args[field]; ok {
		_ = json.Unmarshal(v, &raw)
	}
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("缺少参数 %s", field)
	}
	return svc.resolvePluginFSPath(pluginID, raw, action)
}

// resolvePluginFSPath 解析路径并按 manifest 的 filesystem scope 校验。
// 返回的路径可直接用于 I/O。
func (svc *PluginService) resolvePluginFSPath(pluginID, raw, action string) (string, error) {
	if svc.App == nil || svc.App.PluginMgr == nil {
		return "", fmt.Errorf("插件管理器未初始化")
	}
	inst := svc.App.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return "", pluginmgr.ErrPluginNotFound
	}
	real, err := pluginmgr.ResolveRealPath(raw)
	if err != nil {
		return "", err
	}
	if !inst.Manifest.Permissions.Filesystem.Allows(action, real) {
		return "", fmt.Errorf("%w: 插件 %q 无权 %s 路径 %s（不在 permissions.filesystem.%s 白名单内）",
			pluginmgr.ErrPermissionDenied, pluginID, action, real, action)
	}
	return real, nil
}

// ---- 纯 I/O 实现（不含权限判断，便于单测）----

// decodeFSContent 解析写入内容：默认 utf8，base64 用于二进制
func decodeFSContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf8", "utf-8", "text":
		return []byte(content), nil
	case "base64":
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
		if err != nil {
			return nil, fmt.Errorf("base64 解码失败: %w", err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("不支持的 encoding %q（可用 utf8 / base64）", encoding)
	}
}

// fsReadFile 读取文件：合法 UTF-8 且无 NUL 视为文本直出，否则转 base64。
// 超过上限直接报错——截断返回会让调用方把残缺内容写回去，毁数据。
func fsReadFile(realPath string) (map[string]interface{}, error) {
	f, err := os.Open(realPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录，无法按文件读取", realPath)
	}
	if fi.Size() > pluginFSMaxRead {
		return nil, fmt.Errorf("文件 %d 字节，超过 %d 字节读取上限", fi.Size(), pluginFSMaxRead)
	}

	data, err := io.ReadAll(io.LimitReader(f, pluginFSMaxRead+1))
	if err != nil {
		return nil, err
	}
	// Stat 与读之间文件可能增长，再兜一次
	if len(data) > pluginFSMaxRead {
		return nil, fmt.Errorf("文件超过 %d 字节读取上限", pluginFSMaxRead)
	}

	encoding := "utf8"
	content := string(data)
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		encoding = "base64"
		content = base64.StdEncoding.EncodeToString(data)
	}
	return map[string]interface{}{
		"path":     realPath,
		"size":     len(data),
		"encoding": encoding,
		"content":  content,
	}, nil
}

// fsWriteFile 原子写入：同目录临时文件 + rename，保留原有权限位，失败不留半截文件。
func fsWriteFile(realPath string, data []byte) error {
	if len(data) > pluginFSMaxWrite {
		return fmt.Errorf("内容 %d 字节，超过 %d 字节写入上限", len(data), pluginFSMaxWrite)
	}

	mode := os.FileMode(0o644)
	if fi, err := os.Stat(realPath); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%s 是目录，无法写入", realPath)
		}
		mode = fi.Mode().Perm()
	}

	dir := filepath.Dir(realPath)
	tmp, err := os.CreateTemp(dir, ".qd-fs-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败（父目录不存在？先用 host.fs.mkdir）: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("设置权限失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, realPath); err != nil {
		return fmt.Errorf("替换目标文件失败: %w", err)
	}
	tmpName = "" // rename 成功，别在 defer 里把它删掉
	return nil
}

// fsMove 改名 / 移动。
//
// 两条刻意保留的严格限制：
//   - **目标已存在即报错**，不覆盖。批量重命名里一个算错的目标名不该毁掉已有文件；
//     插件要覆盖就自己先 remove。
//   - **不支持跨卷 / 跨文件系统**（os.Rename 的限制），报清晰错误让插件改用
//     read + write + remove 组合。宿主不代为「复制后删源」——那在中途失败时会
//     留下「源已删、目标半截」的破坏性结果，比直接报错糟得多。
//
// 不自动创建目标的父目录（与 write 一致）。
func fsMove(from, to string) error {
	if _, err := os.Lstat(from); err != nil {
		return fmt.Errorf("源路径不存在或无法访问: %w", err)
	}
	if from == to {
		return nil
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("目标 %s 已存在（host.fs.move 不覆盖）", to)
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("移动失败（跨盘符/跨文件系统不支持）: %w", err)
	}
	return nil
}

// fsListDir 列目录（不递归）。条目超过上限时截断并置 truncated。
func fsListDir(realPath string) ([]map[string]interface{}, bool, error) {
	entries, err := os.ReadDir(realPath)
	if err != nil {
		return nil, false, err
	}

	list := make([]map[string]interface{}, 0, len(entries))
	truncated := false
	for _, e := range entries {
		if len(list) >= pluginFSMaxList {
			truncated = true
			break
		}
		item := map[string]interface{}{
			"name":  e.Name(),
			"path":  filepath.Join(realPath, e.Name()),
			"isDir": e.IsDir(),
		}
		if info, ierr := e.Info(); ierr == nil {
			item["size"] = info.Size()
			item["mtime"] = info.ModTime().UnixMilli()
		}
		list = append(list, item)
	}
	return list, truncated, nil
}

// fsStat 返回元信息；路径不存在时返回 exists:false 而非报错，便于插件做存在性判断。
func fsStat(realPath string) (map[string]interface{}, error) {
	fi, err := os.Stat(realPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{"path": realPath, "exists": false}, nil
		}
		return nil, err
	}
	return map[string]interface{}{
		"path":   realPath,
		"exists": true,
		"isDir":  fi.IsDir(),
		"size":   fi.Size(),
		"mtime":  fi.ModTime().UnixMilli(),
		"mode":   fi.Mode().Perm().String(),
	}, nil
}
