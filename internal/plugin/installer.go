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
			if mf.ID == "" || mf.Name == "" || mf.Version == "" || mf.Backend.Runtime == "" || mf.Backend.Entry == "" {
				return "", fmt.Errorf("%w: id/name/version/backend.runtime/backend.entry 为必填字段", ErrInvalidManifest)
			}

			// 校验 ID 格式
			if !isValidPluginID(mf.ID) {
				return "", fmt.Errorf("%w: 插件 ID %q 格式无效，应类似 com.quickdock.xxx", ErrInvalidManifest, mf.ID)
			}

			// 校验 runtime
			switch mf.Backend.Runtime {
			case "native", "node", "python", "powershell", "wasm":
				// 合法
			default:
				return "", fmt.Errorf("%w: 不支持的 runtime %q", ErrInvalidManifest, mf.Backend.Runtime)
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

	// 检查插件是否已安装：先卸载旧插件释放文件句柄，再备份目录
	var backupDir string
	if _, err := os.Stat(targetDir); err == nil {
		// 卸载正在运行的旧实例，释放文件句柄（忽略"未加载"错误）
		m.UnloadPlugin(pluginID)

		backupDir = targetDir + ".bak." + manifest.Version
		os.RemoveAll(backupDir) // 清理旧的备份

		// Windows 上进程退出后文件句柄释放可能有短暂延迟，最多重试 3 次
		var err error
		for retry := 0; retry < 3; retry++ {
			if err = os.Rename(targetDir, backupDir); err == nil {
				break
			}
			if retry < 2 {
				time.Sleep(200 * time.Millisecond)
			}
		}
		if err != nil {
			return "", fmt.Errorf("备份旧版本插件失败（进程可能未完全退出）: %w", err)
		}
	}

	// 创建目标目录
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", fmt.Errorf("创建插件目录失败: %w", err)
	}

	// ---- Zip Slip 防护：检查所有文件名是否包含 .. ----
	for _, f := range zipReader.File {
		// 清理路径，检查是否包含 ..
		sanitized := filepath.Clean(f.Name)
		// 去掉 ./ 前缀
		sanitized = strings.TrimPrefix(sanitized, "./")
		if strings.Contains(sanitized, "..") || strings.HasPrefix(sanitized, "/") || strings.HasPrefix(sanitized, "\\") {
			// Zip Slip 攻击！回滚
			os.RemoveAll(targetDir)
			if backupDir != "" {
				os.Rename(backupDir, targetDir)
			}
			return "", fmt.Errorf("%w: 文件名 %q 包含非法路径", ErrZipSlipDetected, f.Name)
		}
	}

	// ---- 解压所有文件（含 zip bomb 防护）----
	var totalExtracted int64

	for _, f := range zipReader.File {
		// 清理路径并拼接
		cleanName := filepath.Clean(f.Name)
		cleanName = strings.TrimPrefix(cleanName, "./")
		targetPath := filepath.Join(targetDir, cleanName)

		// 确保目标路径在插件目录内（二次防护）
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			os.RemoveAll(targetDir)
			if backupDir != "" {
				os.Rename(backupDir, targetDir)
			}
			return "", fmt.Errorf("%w: 文件 %q 试图跳出插件目录", ErrZipSlipDetected, f.Name)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
			continue
		}

		// 检查单文件解压大小
		if f.UncompressedSize64 > maxSingleFileSize {
			os.RemoveAll(targetDir)
			if backupDir != "" {
				os.Rename(backupDir, targetDir)
			}
			return "", fmt.Errorf("文件 %q 过大（%d bytes）", f.Name, f.UncompressedSize64)
		}

		// 检查总解压大小
		if totalExtracted+int64(f.UncompressedSize64) > maxDecompressedSize {
			os.RemoveAll(targetDir)
			if backupDir != "" {
				os.Rename(backupDir, targetDir)
			}
			return "", fmt.Errorf("解压总大小超出限制（%d bytes）", maxDecompressedSize)
		}

		// 创建父目录
		os.MkdirAll(filepath.Dir(targetPath), 0755)

		// 写入文件
		dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			os.RemoveAll(targetDir)
			if backupDir != "" {
				os.Rename(backupDir, targetDir)
			}
			return "", fmt.Errorf("创建文件 %s 失败: %w", targetPath, err)
		}

		src, err := f.Open()
		if err != nil {
			dst.Close()
			os.RemoveAll(targetDir)
			if backupDir != "" {
				os.Rename(backupDir, targetDir)
			}
			return "", fmt.Errorf("读取 zip 条目 %s 失败: %w", f.Name, err)
		}

		written, err := io.CopyN(dst, src, maxSingleFileSize)
		src.Close()
		dst.Close()
		if err != nil && err != io.EOF {
			os.RemoveAll(targetDir)
			if backupDir != "" {
				os.Rename(backupDir, targetDir)
			}
			return "", fmt.Errorf("写入文件 %s 失败: %w", targetPath, err)
		}
		totalExtracted += written

		// native runtime 的入口文件加可执行权限（仅 Unix，Windows 不适用但无害）
		if manifest.Backend.Runtime == "native" && cleanName == manifest.Backend.Entry {
			os.Chmod(targetPath, 0755)
		}
	}

	// 设置文件权限
	os.Chmod(targetDir, 0755)

	// 确保 python 插件入口有执行权限
	if manifest.Backend.Runtime == "python" {
		entryPath := filepath.Join(targetDir, manifest.Backend.Entry)
		if _, err := os.Stat(entryPath); err == nil {
			os.Chmod(entryPath, 0644)
		}
	}

	// 加载插件
	if err := m.LoadPlugin(*manifest, targetDir); err != nil {
		// 加载失败但安装成功，返回目录路径和错误
		return targetDir, fmt.Errorf("插件安装成功但加载失败（可手动重启）: %w", err)
	}

	return targetDir, nil
}

// isValidPluginID 校验插件 ID 格式：至少要包含一个点号
func isValidPluginID(id string) bool {
	return strings.Count(id, ".") >= 1 && len(id) > 0
}
