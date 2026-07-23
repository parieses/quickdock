//go:build windows

package platform

import "strings"

// ExtractItemIcon 根据 item 类型与值返回图标 base64 data URL。
// 仅当类型为「应用」或值以 .exe/.lnk 结尾时，才尝试从文件提取图标；
// 提取失败（非 exe、无图标资源等）返回空字符串，调用方回退到类型默认图标。
// 非 Windows 平台由 itemicon_other.go 提供空实现（图标提取依赖 Windows Shell API）。
func ExtractItemIcon(itemType, value string) string {
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if itemType == "应用" || strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".lnk") {
		if icon := ExtractIconBase64(value); icon != "" {
			return icon
		}
	}
	return ""
}
