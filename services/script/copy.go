package script

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

// CopyScript 复制一个脚本（连同目录内容）。
//
// 为什么要这个：改一个现成脚本比从模板重写快得多——「签到脚本再来一个换
// 站点的」是这类面板最高频的第二个动作。复制时保留解释器/参数/超时/并发
// 这些「调试过的设定」，但**不复制**下面三样：
//   - WatchPath：两个脚本监听同一路径会同时触发，等于一次文件变化跑两遍；
//   - MCPAllowed：外部可触发的授权必须逐个脚本重新点头；
//   - Pinned：复制品挤到置顶区只会把用户自己的排序打乱。
func (s *ScriptService) CopyScript(id string) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	src, err := s.App.DB.GetScript(id)
	if err != nil {
		return services.Fail(err)
	}
	created, err := s.App.DB.CreateScript(&db.Script{
		Name:        s.uniqueCopyName(src.Name),
		Description: src.Description,
		Lang:        src.Lang,
		Entry:       src.Entry,
		Interpreter: src.Interpreter,
		DefaultArgs: src.DefaultArgs,
		TimeoutSec:  src.TimeoutSec,
		Concurrency: src.Concurrency,
		AutoInstall: src.AutoInstall,
		Tags:        src.Tags,
	})
	if err != nil {
		return services.Fail(err)
	}

	dst := s.dir(created.ID)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		_ = s.App.DB.DeleteScript(created.ID)
		return services.Fail(fmt.Errorf("创建脚本目录失败: %w", err))
	}
	if err := copyScriptTree(s.dir(src.ID), dst); err != nil {
		// 复制到一半失败：整条记录与半成品目录都清掉，别留下一个「文件缺一半」
		// 的脚本——它点开能看，跑起来才炸，最难排查。
		_ = s.App.DB.DeleteScript(created.ID)
		_ = os.RemoveAll(dst)
		return services.Fail(fmt.Errorf("复制脚本失败: %w", err))
	}
	// 信任状态跟随源脚本：复制一个还没确认过的外部脚本，副本同样不该被信任；
	// 反之确认过的内容复制出来也不必再问一次。
	if s.scriptTrusted(src.ID) {
		_ = s.markTrusted(created.ID)
	} else {
		_ = os.Remove(filepath.Join(dst, trustMarker))
	}

	s.emit(EventChanged, map[string]any{"scriptId": created.ID, "reason": "copy", "from": src.ID})
	return services.Ok(created)
}

// uniqueCopyName 生成不重名的副本名。
func (s *ScriptService) uniqueCopyName(base string) string {
	list, err := s.App.DB.ListScripts()
	if err != nil {
		return base + " 副本"
	}
	used := make(map[string]bool, len(list))
	for _, it := range list {
		used[it.Name] = true
	}
	name := base + " 副本"
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s 副本 %d", base, i)
		if i > 99 {
			break
		}
	}
	return name
}

// copyScriptTree 递归复制目录内容。
func copyScriptTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// 依赖标记不跟着走：副本要按自己的依赖文件指纹重新判断，
		// 复制过去就会「以为装过」而跳过安装。
		if d.Name() == depsMarker {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFilePerm(p, target)
	})
}

// copyFilePerm 复制文件并保留权限位（shell 脚本的可执行位）。
func copyFilePerm(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		logger.W("[script] 关闭复制目标失败 %s: %v", dst, err)
	}
	return nil
}
