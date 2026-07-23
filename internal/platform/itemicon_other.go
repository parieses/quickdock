//go:build !windows

package platform

// ExtractItemIcon 非 Windows 平台返回空字符串。
// 图标提取依赖 Windows Shell API（SHGetFileInfoW 等），其他平台无实现。
func ExtractItemIcon(itemType, value string) string { return "" }
