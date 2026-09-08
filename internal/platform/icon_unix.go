//go:build darwin || linux

package platform

// extractIconRaw 非 Windows 平台暂无图标提取实现。
//
// 待移植方案：
//   - macOS：NSWorkspace.shared.icon(forFile:) 取 NSImage → PNG（cgo）
//   - Linux：解析 .desktop 文件的 Icon 字段 + 主题图标查找
//
// 返回空串时 ExtractIconBase64 直接返回空，调用方按「无图标」处理。

func extractIconRaw(filePath string) string { return "" }
