//go:build windows

package env

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// isElevated 返回当前进程是否以「提权（完整管理员）令牌」运行。
// UAC 过滤令牌（非提权管理员 / 标准用户）返回 false。
// PostgreSQL 在 Windows 上拒绝以管理员身份直接启动，因此是否走 Windows 服务
// 取决于此值：提权 → 服务（LocalSystem），否则 → 直接拉起（deny-only 已绕过检测）。
func isElevated() bool {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()
	// TokenElevation 在本版 x/sys/windows 中是常量（class=20），对应结构体仅含 TokenIsElevated 标志位。
	var e struct{ TokenIsElevated uint32 }
	var out uint32
	if err := windows.GetTokenInformation(tok, uint32(windows.TokenElevation),
		(*byte)(unsafe.Pointer(&e)), uint32(unsafe.Sizeof(e)), &out); err != nil {
		return false
	}
	return e.TokenIsElevated != 0
}
