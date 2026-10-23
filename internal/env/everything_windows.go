//go:build windows

package env

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readExeVersion 从 PE 文件版本资源读取 Everything.exe 的产品版本（"1.4.1.1024"）。
// 读不到（无版本资源 / API 失败）时返回空串，调用方据此标记版本为 unknown，不影响「已安装」判定。
//
// 踩坑记录（2026-10-22）：\VarFileInfo\Translation 是 DWORD 数组，每个 DWORD 的
// 低 16 位是语言 ID（wLanguage）、高 16 位是代码页（wCodePage）。曾经把两者写反，
// 拼出的块名（如 \StringFileInfo\04B00409\FileVersion）永远查不到，版本恒为 unknown，
// 「环境管理版本列 / 端口全景」全部显示 unknown。现已修正并遍历全部翻译项兜底。
func readExeVersion(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return ""
	}
	// 先取翻译（语言-代码页）数组，据此逐一拼出 StringFileInfo 子块名。
	var trBuf *uint16
	var trLen uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&trBuf), &trLen); err != nil || trLen < 4 {
		return ""
	}
	// Translation 元素为 DWORD（4 字节）：低位=语言 ID，高位=代码页。
	entries := int(trLen) / 4
	tr := unsafe.Slice((*uint32)(unsafe.Pointer(trBuf)), entries)
	for _, raw := range tr {
		langID := raw & 0xFFFF // 低 16 位：语言 ID
		cp := raw >> 16        // 高 16 位：代码页
		for _, field := range []string{"FileVersion", "ProductVersion"} {
			block := fmt.Sprintf(`\StringFileInfo\%04X%04X\%s`, langID, cp, field)
			var verBuf *uint16
			var verLen uint32
			if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), block, unsafe.Pointer(&verBuf), &verLen); err != nil || verBuf == nil {
				continue
			}
			if v := strings.TrimSpace(windows.UTF16PtrToString(verBuf)); v != "" {
				return v
			}
		}
	}
	return ""
}
