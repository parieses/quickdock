//go:build windows

package plugin

import (
	"os"
	"syscall"
	"unsafe"
)

// kg 对应 Windows 的 GUID（REFKNOWNFOLDERID），用于 SHGetKnownFolderPath。
type kg struct {
	d1 uint32
	d2 uint16
	d3 uint16
	d4 [8]byte
}

// knownFolders 仅覆盖会被重定向的常用用户目录。其余子目录走主目录字面拼接。
var knownFolders = map[string]kg{
	"Downloads": {0x374DE290, 0x123F, 0x4565, [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}},
	"Desktop":   {0xB4BFCC3A, 0xDB2C, 0x424C, [8]byte{0x8F, 0x7A, 0x4A, 0xC6, 0x0E, 0x50, 0x46, 0x39}},
	"Documents": {0xFDD39AD0, 0x238F, 0x46AF, [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7}},
	"Pictures":  {0x33E28130, 0x4E1E, 0x4676, [8]byte{0xA3, 0x5E, 0x8F, 0x07, 0xC9, 0x30, 0xBA, 0x0A}},
}

var (
	modShell32               = syscall.NewLazyDLL("shell32.dll")
	procSHGetKnownFolderPath = modShell32.NewProc("SHGetKnownFolderPath")
	modOle32                 = syscall.NewLazyDLL("ole32.dll")
	procCoTaskMemFree        = modOle32.NewProc("CoTaskMemFree")
)

// knownFolderDir 返回 Windows 已知文件夹重定向后的真实路径。
// 返回 ok=false 表示 sub 不在已知文件夹列表，调用方回退到主目录拼接。
func knownFolderDir(sub string) (string, bool) {
	g, ok := knownFolders[sub]
	if !ok {
		return "", false
	}
	var ptr uintptr
	hr, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&g)), 0, 0, uintptr(unsafe.Pointer(&ptr)))
	if hr != 0 || ptr == 0 {
		return "", false
	}
	defer procCoTaskMemFree.Call(ptr)
	if s := utf16PtrToString((*uint16)(unsafe.Pointer(ptr))); s != "" {
		return s, true
	}
	return "", false
}

// utf16PtrToString 读取 null 结尾的 UTF-16 字符串。
// syscall.UTF16PtrToString 已在 Go 1.23 起废弃、新版移除，故自行实现。
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(n)*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// ensure os import is used even if only on this build tag
var _ = os.UserHomeDir
