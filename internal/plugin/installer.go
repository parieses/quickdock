package plugin

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 安全限制
const (
	maxPluginJSONSize     = 1 << 20  // plugin.json 最大 1MB
	maxDecompressedSize   = 100 << 20 // 解压总大小上限 100MB
	maxSingleFileSize     = 50 << 20  // 单文件解压大小上限 50MB
)

// InstallFromZip 从 zip 包安装插件
// zipPath: zip 文件路径
// 返回安装目录路径
func (m *Manager) InstallFromZip(zipPath string) (string, error) {
	// 打开 zip 文件
	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("打开 zip 包失败: %w", err)
	}
	defer zipReader.Close()

	// 查找 plugin.json 并提取插件 ID
	var pluginID string
	var manifest *PluginManifest

		for _, f := range zipReader.File {
		if f.Name == "plugin.json" || f.Name == "./plugin.json" {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("读取 plugin.json 失败: %w", err)
			}
			data, err := io.ReadAll(io.LimitReader(rc, maxPluginJSONSize))
			rc.Close()
			if err != nil {
				return "", fmt.Errorf("读取 plugin.json 失败: %w", err)
			}

			var mf PluginManifest
			if err := json.Unmarshal(data, &mf); err != nil {
				return "", fmt.Errorf("%w: plugin.json 解析失败: %v", ErrInvalidManifest, err)
			}

			// 校验必填字段
			if mf.ID == "" || mf.Name == "" || mf.Version == "" || mf.Backend.Runtime == "" {
				return "", fmt.Errorf("%w: id/name/version/backend.runtime 为必填字段", ErrInvalidManifest)
			}
			// none runtime 不需要 entry
			if mf.Backend.Runtime != "none" && mf.Backend.Entry == "" {
				return "", fmt.Errorf("%w: backend.entry 为必填字段（none runtime 除外）", ErrInvalidManifest)
			}

			// 校验 ID 格式：与 LoadManifest/validateManifest 同一白名单（pluginIDRe），
			// 不允许 / \ 与以字母开头的约束天然阻止 "../" 等路径穿越
			if !pluginIDRe.MatchString(mf.ID) {
				return "", fmt.Errorf("%w: 插件 ID %q 格式无效（应匹配 com.quickdock.xxx 或 hosts-manager 形式）", ErrInvalidManifest, mf.ID)
			}

			// 校验 runtime
			switch mf.Backend.Runtime {
			case "native", "goja", "none":
				// 合法
			default:
				return "", fmt.Errorf("%w: 不支持的 runtime %q（支持: native, goja, none）", ErrInvalidManifest, mf.Backend.Runtime)
			}

			manifest = &mf
			pluginID = mf.ID
			break
		}
	}

	if manifest == nil {
		return "", fmt.Errorf("%w: zip 包中未找到 plugin.json", ErrInvalidManifest)
	}

	targetDir := filepath.Join(m.pluginsDir, pluginID)

	// 检查插件是否已安装：先停止旧实例释放文件句柄，再备份目录
	var backupDir string
	if _, err := os.Stat(targetDir); err == nil {
		// 停止正在运行的旧实例，并记录 PID（用于兜底强杀进程树）
		var oldPID int
		m.mu.Lock()
		if inst, ok := m.plugins[pluginID]; ok {
			if inst.Cmd != nil && inst.Cmd.Process != nil {
				oldPID = inst.Cmd.Process.Pid
			}
			m.stopPlugin(inst)
			delete(m.plugins, pluginID)
		}
		m.mu.Unlock()

		// 立即兜底杀：① 已知 PID 的整棵进程树（stopPlugin 的 Kill 只杀主进程，
		// 子进程如 pdfcpu.exe 会变孤儿并以其 CWD 锁住整个目录）② 可执行路径位于
		// 目标目录内的孤儿进程（manager 未跟踪，如崩溃重启飞行中的实例/残留）。
		// 顺序：先杀树再按路径，避免路径扫描漏掉读取不到 Path 的受限进程。
		if oldPID > 0 {
			killProcessTree(oldPID)
		}
		killProcessesLockingDir(targetDir)

		backupDir = targetDir + ".bak." + manifest.Version
		os.RemoveAll(backupDir) // 清理旧的备份

		// Windows 上 TerminateProcess 后文件句柄/CWD 锁异步释放（且可能被 Defender
		// 实时扫描短暂独占）。用更短间隔（150ms）高频重试，并在每次重试时兜底强杀
		// 锁定进程，以更快地抢到目录释放窗口——把"安装更新"的等待从十秒级压到通常
		// 1~3 秒；总上限约 9s，足以覆盖绝大多数场景，失败时仍给出明确操作指引。
		waits := 60 // 60 × 150ms ≈ 9s
		var err error
		for i := 0; i < waits; i++ {
			if err = os.Rename(targetDir, backupDir); err == nil {
				break
			}
			// 每次重试都兜底强杀：清掉可能残留的锁定进程（含 Defender 之外的真实锁）
			if oldPID > 0 {
				killProcessTree(oldPID)
			}
			killProcessesLockingDir(targetDir)
			time.Sleep(150 * time.Millisecond)
		}
		if err != nil {
			return "", fmt.Errorf("备份旧版本插件失败（进程可能未完全退出，请在插件管理页点击「停止进程」后重试）: %w", err)
		}
	}

	// 标记文件：记录备份路径，解压完成后删除。若存在此文件说明安装中断，下次启动时自动回滚
	rollbackMark := targetDir + ".rollback"
	if backupDir != "" {
		os.WriteFile(rollbackMark, []byte(backupDir), 0644)
	}

	// 创建目标目录
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		_ = rollbackInstall(targetDir, backupDir, rollbackMark)
		return "", fmt.Errorf("创建插件目录失败: %w", err)
	}

	// 统一延迟回滚：如果未调用 commit，任何 return 都会触发清理+还原
	committed := false
	defer func() {
		if !committed {
			_ = rollbackInstall(targetDir, backupDir, rollbackMark)
		}
	}()

	// ---- Zip Slip 防护：检查所有文件名是否包含 .. ----
	for _, f := range zipReader.File {
		sanitized := filepath.Clean(f.Name)
		sanitized = strings.TrimPrefix(sanitized, "./")
		if strings.Contains(sanitized, "..") || strings.HasPrefix(sanitized, "/") || strings.HasPrefix(sanitized, "\\") {
			return "", fmt.Errorf("%w: 文件名 %q 包含非法路径", ErrZipSlipDetected, f.Name)
		}
	}

	// ---- 解压所有文件（含 zip bomb 防护）----
	var totalExtracted int64

	for _, f := range zipReader.File {
		cleanName := filepath.Clean(f.Name)
		cleanName = strings.TrimPrefix(cleanName, "./")
		targetPath := filepath.Join(targetDir, cleanName)

		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			return "", fmt.Errorf("%w: 文件 %q 试图跳出插件目录", ErrZipSlipDetected, f.Name)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
			continue
		}

		if f.UncompressedSize64 > maxSingleFileSize {
			return "", fmt.Errorf("文件 %q 过大（%d bytes）", f.Name, f.UncompressedSize64)
		}

		if totalExtracted+int64(f.UncompressedSize64) > maxDecompressedSize {
			return "", fmt.Errorf("解压总大小超出限制（%d bytes）", maxDecompressedSize)
		}

		os.MkdirAll(filepath.Dir(targetPath), 0755)

		dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			return "", fmt.Errorf("创建文件 %s 失败: %w", targetPath, err)
		}

		src, err := f.Open()
		if err != nil {
			dst.Close()
			return "", fmt.Errorf("读取 zip 条目 %s 失败: %w", f.Name, err)
		}

		written, err := io.CopyN(dst, src, maxSingleFileSize)
		src.Close()
		dst.Close()
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("写入文件 %s 失败: %w", targetPath, err)
		}
		totalExtracted += written

		if manifest.Backend.Runtime == "native" && cleanName == manifest.Backend.Entry {
			os.Chmod(targetPath, 0755)
		}
	}

	os.Chmod(targetDir, 0755)

	// 安装成功，提交（defer 不再回滚）
	committed = true
	os.Remove(rollbackMark)

	// 加载插件
	if err := m.LoadPlugin(*manifest, targetDir); err != nil {
		return targetDir, fmt.Errorf("插件安装成功但加载失败（可手动重启）: %w", err)
	}

	return targetDir, nil
}

// rollbackInstall 清理新安装目录并恢复备份（失败时仅日志）
func rollbackInstall(targetDir, backupDir, markFile string) error {
	// 清理 rollback 标记
	os.Remove(markFile)
	// 删除残缺的新安装
	os.RemoveAll(targetDir)
	// 恢复备份
	if backupDir != "" {
		// 最多重试 3 次，避免 Windows 文件句柄延迟
		var err error
		for retry := 0; retry < 3; retry++ {
			if err = os.Rename(backupDir, targetDir); err == nil {
				return nil
			}
			if retry < 2 {
				time.Sleep(200 * time.Millisecond)
			}
		}
		return fmt.Errorf("恢复备份目录失败（残留目录: %s, 原目录: %s）: %w", backupDir, targetDir, err)
	}
	return nil
}

// isValidPluginID 已废弃：统一使用 manifest.go 的 pluginIDRe 白名单校验，
// 避免两套规则不一致（旧规则仅要求含点号，曾导致 ".." 这类 ID 通过校验）。
// 如发现仍引用 isValidPluginID 请改用 pluginIDRe.MatchString。
