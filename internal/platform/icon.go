package platform

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
)

// IconMIME 根据扩展名返回图标 data URI 的 MIME 类型。
// 支持 svg/png/ico/jpg/jpeg，未知扩展名默认 image/svg+xml（内置插件图标均为 SVG）。
func IconMIME(ext string) string {
	switch strings.ToLower(ext) {
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return "image/svg+xml"
	}
}

// iconCacheDir 返回图标缓存目录
func iconCacheDir() string {
	dir := filepath.Join(DefaultDataDir(), "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.W("QuickDock: 图标缓存目录创建失败: %v", err)
	}
	return dir
}

// sanitizeIconName 将路径转为安全的缓存文件名。
// 同时纳入目录归一化哈希，避免不同目录下同名 exe（如两个 notepad.exe）共用同一缓存而串图。
func sanitizeIconName(path string) string {
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return dirHash(dir) + "_" + safeBaseName(base)
}

// safeBaseName 仅保留文件名词中的安全字符
func safeBaseName(base string) string {
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, base)
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

// dirHash 对目录做 FNV-1a 哈希，得到稳定的短标识，用于区分同名文件的不同来源目录
func dirHash(dir string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(filepath.Clean(dir))))
	return fmt.Sprintf("%08x", h.Sum32())
}

// ExtractIconBase64 从文件提取图标，返回 base64 data URL。
// 优先读取磁盘缓存；若源文件比缓存更新则重新提取（避免图标永不刷新）。
// 失败时返回空字符串。
// 实际提取由平台相关实现 extractIconRaw 完成（Windows=Shell API，其他平台暂无实现）。
func ExtractIconBase64(filePath string) string {
	cacheKey := sanitizeIconName(filePath)
	cachePath := filepath.Join(iconCacheDir(), cacheKey+".png")

	// 源文件比缓存新 → 跳过缓存，重新提取
	if srcInfo, err := os.Stat(filePath); err == nil {
		if cacheInfo, err := os.Stat(cachePath); err == nil {
			if srcInfo.ModTime().After(cacheInfo.ModTime()) {
				goto extract
			}
		}
	}

	// 1. 尝试读缓存
	if data, err := os.ReadFile(cachePath); err == nil {
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	}

extract:
	// 2. 提取图标
	dataURL := extractIconRaw(filePath)
	if dataURL == "" {
		return ""
	}

	// 3. 写入缓存
	if idx := bytes.IndexByte([]byte(dataURL), ','); idx >= 0 {
		if pngData, err := base64.StdEncoding.DecodeString(dataURL[idx+1:]); err == nil {
			_ = os.WriteFile(cachePath, pngData, 0o644)
		}
	}

	return dataURL
}
