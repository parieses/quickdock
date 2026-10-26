//go:build !windows

package platform

// decodeConsoleOutput 非 Windows 平台的子进程输出按 UTF-8 处理，原样返回。
// （Linux/macOS 终端默认 UTF-8；用户显式切了其它 locale 的场景不特殊处理。）
func decodeConsoleOutput(s string) string { return s }
