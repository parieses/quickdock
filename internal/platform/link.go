//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"quickdock/internal/sysutil"
)

// 本文件解决「Ctrl+K 启动的已安装应用，主程序重启后随之退出」的问题：
// 原 LaunchApp 直接走 windows.ShellExecute，子进程未脱离 QuickDock 进程组，
// 主程序退出时被一并带走（与 services/app 下 item.go 的 startDetached 注释描述一致）。
// 这里把 .lnk 解析为真实目标 exe + 参数 + 工作目录，改用 CreateProcess 并套用
// sysutil.Detach（DETACHED_PROCESS），使第三方软件在主程序退出后继续存活。

// ResolveLink 解析 .lnk 快捷方式，返回目标 exe 路径、启动参数、工作目录。
// 失败返回 err（调用方应回退到 ShellExecute）。
func ResolveLink(lnkPath string) (target, args, workingDir string, err error) {
	clsidShellLink := windows.GUID{
		Data1: 0x00021401, Data2: 0, Data3: 0,
		Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46},
	}
	iidIShellLinkW := windows.GUID{
		Data1: 0x000214F9, Data2: 0, Data3: 0,
		Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46},
	}
	iidIPersistFile := windows.GUID{
		Data1: 0x0000010B, Data2: 0, Data3: 0,
		Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46},
	}

	didInit := false
	if hr, _, _ := procCoInitializeEx.Call(0, uintptr(COINIT_APARTMENTTHREADED|COINIT_DISABLE_OLE1DDE)); hr == 0 {
		didInit = true
	}
	defer func() {
		if didInit {
			procCoUninitialize.Call()
		}
	}()

	var ppvSL unsafe.Pointer
	r, _, _ := syscall.Syscall6(
		procCoCreateInstance.Addr(), 5,
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0,
		CLSCTX_INPROC_SERVER,
		uintptr(unsafe.Pointer(&iidIShellLinkW)),
		uintptr(unsafe.Pointer(&ppvSL)),
		0,
	)
	if r != 0 || ppvSL == nil {
		return "", "", "", fmt.Errorf("CoCreateInstance(IShellLink) 失败 hr=0x%x", uint32(r))
	}
	defer func() {
		comCall(*(*unsafe.Pointer)(ppvSL), 2, uintptr(ppvSL)) // Release
	}()

	// 从 IShellLink 取 IPersistFile 以加载 .lnk 文件
	var ppvPF unsafe.Pointer
	slVtbl := *(*unsafe.Pointer)(ppvSL)
	r, _, _ = comCall(slVtbl, 0, uintptr(ppvSL),
		uintptr(unsafe.Pointer(&iidIPersistFile)),
		uintptr(unsafe.Pointer(&ppvPF)))
	if r != 0 || ppvPF == nil {
		return "", "", "", fmt.Errorf("QueryInterface(IPersistFile) 失败 hr=0x%x", uint32(r))
	}
	defer func() {
		comCall(*(*unsafe.Pointer)(ppvPF), 2, uintptr(ppvPF)) // Release
	}()

	lnkUTF16, err := windows.UTF16PtrFromString(lnkPath)
	if err != nil {
		return "", "", "", err
	}
	pfVtbl := *(*unsafe.Pointer)(ppvPF)
	r, _, _ = comCall(pfVtbl, 5, uintptr(ppvPF), uintptr(unsafe.Pointer(lnkUTF16)), STGM_READ)
	if r != 0 {
		return "", "", "", fmt.Errorf("IPersistFile.Load 失败 hr=0x%x", uint32(r))
	}

	var (
		targetBuf [windows.MAX_PATH]uint16
		argsBuf   [windows.MAX_PATH]uint16
		wdBuf     [windows.MAX_PATH]uint16
		fd        findDataW
	)

	r, _, _ = comCall(slVtbl, 3, uintptr(ppvSL),
		uintptr(unsafe.Pointer(&targetBuf[0])),
		windows.MAX_PATH,
		uintptr(unsafe.Pointer(&fd)),
		SLGP_UNCPRIORITY)
	if r != 0 {
		return "", "", "", fmt.Errorf("IShellLink.GetPath 失败 hr=0x%x", uint32(r))
	}
	target = windows.UTF16ToString(targetBuf[:])
	if target == "" {
		return "", "", "", fmt.Errorf("解析到的目标路径为空")
	}

	comCall(slVtbl, 10, uintptr(ppvSL), uintptr(unsafe.Pointer(&argsBuf[0])), windows.MAX_PATH)
	args = windows.UTF16ToString(argsBuf[:])

	comCall(slVtbl, 8, uintptr(ppvSL), uintptr(unsafe.Pointer(&wdBuf[0])), windows.MAX_PATH)
	workingDir = windows.UTF16ToString(wdBuf[:])

	return target, args, workingDir, nil
}

// startResolved 用 CreateProcess 启动已解析的目标，并脱离父进程进程组。
func startResolved(target, argsStr, wd string) error {
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("解析目标不存在: %s", target)
	}
	cmd := exec.Command(target)
	if argsStr != "" {
		// 直传 CmdLine：lnk 参数可能自带引号，避免二次拆分再拼接导致的双重引号。
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: quoteIfSpace(target) + " " + argsStr}
	}
	if wd != "" {
		if fi, e := os.Stat(wd); e == nil && fi.IsDir() {
			cmd.Dir = wd
		}
	}
	return sysutil.StartDetached(cmd)
}

func quoteIfSpace(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// ---- COM 辅助 ----

const (
	CLSCTX_INPROC_SERVER = 0x1
	STGM_READ            = 0x0
	SLGP_UNCPRIORITY     = 0x0004
)

var procCoCreateInstance = modOle32.NewProc("CoCreateInstance")

// findDataW 对应 WIN32_FIND_DATAW，GetPath 的 pfd 参数需要一个可写缓冲区。
type findDataW struct {
	dwFileAttributes uint32
	ftCreationTime   syscall.Filetime
	ftLastAccessTime syscall.Filetime
	ftLastWriteTime  syscall.Filetime
	nFileSizeHigh    uint32
	nFileSizeLow     uint32
	dwReserved0      uint32
	dwReserved1      uint32
	cFileName        [windows.MAX_PATH]uint16
	cAlternate       [14]uint16
}

// comCall 调用 COM 接口方法。vtable 为虚表指针（即 *ppv 解出的 unsafe.Pointer），
// index 为虚表偏移（IUnknown: 0=QueryInterface,1=AddRef,2=Release，其后为接口方法）。
// 第一个变参必须是接口指针 this。最多支持 6 个参数（含 this）。
func comCall(vtable unsafe.Pointer, index uintptr, a ...uintptr) (uintptr, uintptr, error) {
	vtableArr := (*[1 << 30]uintptr)(vtable)
	m := vtableArr[index]
	switch len(a) {
	case 1:
		return syscall.Syscall(m, 1, a[0], 0, 0)
	case 2:
		return syscall.Syscall(m, 2, a[0], a[1], 0)
	case 3:
		return syscall.Syscall6(m, 3, a[0], a[1], a[2], 0, 0, 0)
	case 4:
		return syscall.Syscall6(m, 4, a[0], a[1], a[2], a[3], 0, 0)
	case 5:
		return syscall.Syscall6(m, 5, a[0], a[1], a[2], a[3], a[4], 0)
	case 6:
		return syscall.Syscall6(m, 6, a[0], a[1], a[2], a[3], a[4], a[5])
	}
	return 0, 0, fmt.Errorf("comCall 不支持的参数数量: %d", len(a))
}
