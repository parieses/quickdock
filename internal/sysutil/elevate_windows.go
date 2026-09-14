//go:build windows

package sysutil

import (
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsElevated 当前进程是否以「提权（完整管理员）令牌」运行。
// UAC 过滤令牌（非提权管理员 / 标准用户）返回 false。
// 与 internal/env 里那份判定的区别：此处是通用工具，供需要「判断该不该提权」的场景复用。
func IsElevated() bool {
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

const (
	// seeMaskNoCloseProcess = SEE_MASK_NOCLOSEPROCESS：请求返回进程句柄，
	// 否则 ShellExecuteExW 不给 hProcess，就无法等待子进程结束、拿不到退出码。
	seeMaskNoCloseProcess = 0x00000040
)

// shellExecuteExW 取 shell32 的提权入口。用懒加载而非静态链接：
// 少了它也不会让整个程序起不来，只在真正需要提权时报错。
var shellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellExecuteInfoW 对应 Win32 的 SHELLEXECUTEINFOW。
// 字段顺序/宽度必须与系统头文件一致——布局错位会写出内存垃圾，
// 表现为 ShellExecuteEx 直接失败或系统弹一个莫名其妙的框。
type shellExecuteInfoW struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle // 与 hMonitor 共用同一块存储
	hProcess     windows.Handle
}

// RunElevated 以管理员身份启动 exe（触发一次 UAC），并等待其结束。
//
// 返回 ErrElevationCancelled 表示用户在 UAC 上点了「否」。
// timeout 只约束「子进程运行时长」，不约束用户在 UAC 上的思考时间——
// ShellExecuteExW 是在用户作出选择、提权进程创建出来之后才返回的。
func RunElevated(exe string, args []string, timeout time.Duration) error {
	if err := shellExecuteExW.Find(); err != nil {
		return fmt.Errorf("%w: %v", ErrElevationUnsupported, err)
	}
	sei := shellExecuteInfoW{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       windows.StringToUTF16Ptr("runas"),
		lpFile:       windows.StringToUTF16Ptr(exe),
		lpParameters: windows.StringToUTF16Ptr(joinWindowsArgs(args)),
		nShow:        windows.SW_HIDE, // 子进程是纯后台任务，别闪任何窗口
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))

	if r1, _, err := shellExecuteExW.Call(uintptr(unsafe.Pointer(&sei))); r1 == 0 {
		if errorsIsCancelled(err) {
			return ErrElevationCancelled
		}
		return fmt.Errorf("提权启动失败: %v", err)
	}
	if sei.hProcess == 0 {
		// 理论上不该发生（已请求 NOCLOSEPROCESS），保守返回成功以免误判，
		// 真实结果由调用方核对文件内容确认。
		return nil
	}
	defer windows.CloseHandle(sei.hProcess)

	ev, err := windows.WaitForSingleObject(sei.hProcess, uint32(timeout/time.Millisecond))
	if err != nil {
		return fmt.Errorf("等待提权进程失败: %v", err)
	}
	if ev == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("提权进程在 %s 内未结束", timeout)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return fmt.Errorf("读取提权进程退出码失败: %v", err)
	}
	if code != 0 {
		return fmt.Errorf("提权子进程执行失败（退出码 %d）", code)
	}
	return nil
}

// errorsIsCancelled 判断 ShellExecuteExW 的失败是否为「用户拒绝授权」。
// 不引 errors 包单独比较：这里只需要等值判断一个 errno。
func errorsIsCancelled(err error) bool {
	return err == windows.ERROR_CANCELLED
}

// joinWindowsArgs 把参数拼成一条 Windows 命令行字符串。
func joinWindowsArgs(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteWindowsArg(a))
	}
	return b.String()
}

// quoteWindowsArg 按 CommandLineToArgvW 的规则转义单个参数：
// 无空白/引号则原样；否则整体加引号，并把「引号前的连续反斜杠」翻倍（引号前的反斜杠
// 若为奇数个会被当作转义符吃掉一个），末尾的连续反斜杠同样翻倍（否则会转义收尾引号）。
func quoteWindowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			backslashes = 0
			b.WriteByte('"')
		default:
			b.WriteString(strings.Repeat("\\", backslashes))
			backslashes = 0
			b.WriteRune(r)
		}
	}
	b.WriteString(strings.Repeat("\\", backslashes*2))
	b.WriteByte('"')
	return b.String()
}
