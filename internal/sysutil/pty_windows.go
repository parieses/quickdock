//go:build windows

package sysutil

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 本文件提供 Windows 伪控制台（ConPTY）拉起子进程的能力。
//
// 背景：少数 Windows 控制台程序会检测自己的 stdout/stderr 是否为「真实控制台句柄」，
// 一旦被重定向到管道或文件就立刻自行退出（exit code 0、无任何输出）。
// 典型是 adamyg 版 memcached_service.exe 的 "-d run" 非服务模式：
// 走 exec.Cmd 的 StdoutPipe/StderrPipe 时 48ms 内就退出，导致服务「启动即关闭」。
// 而按住不放地给它一个真实控制台（哪怕不可见），它就正常常驻并监听端口。
//
// ConPTY 正好满足两点：子进程视角 stdout/stderr 是控制台（检测通过），
// 宿主侧则从管道另一端读取合并输出（日志照常采集）。

const (
	// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE：把 HPCON 绑到子进程的启动属性上。
	procThreadAttrPseudoConsole = 0x00020016
	// EXTENDED_STARTUPINFO_PRESENT：CreateProcess 需配合 STARTUPINFOEX 使用。
	extendedStartupInfoPresent = 0x00080000
	infinite                   = 0xFFFFFFFF
)

var (
	kernel32DLL = syscall.NewLazyDLL("kernel32.dll")

	pCreatePseudoConsole               = kernel32DLL.NewProc("CreatePseudoConsole")
	pClosePseudoConsole                = kernel32DLL.NewProc("ClosePseudoConsole")
	pInitializeProcThreadAttributeList = kernel32DLL.NewProc("InitializeProcThreadAttributeList")
	pUpdateProcThreadAttribute         = kernel32DLL.NewProc("UpdateProcThreadAttribute")
	pDeleteProcThreadAttributeList     = kernel32DLL.NewProc("DeleteProcThreadAttributeList")
	pCreateProcessW                    = kernel32DLL.NewProc("CreateProcessW")
)

// callErr 归一 LazyProc 的错误：调用成功时 err 也可能是 Errno(0)，不能据此判错。
func callErr(err error) error {
	if err != nil && err != syscall.Errno(0) {
		return err
	}
	return nil
}

// startupInfoW 对应 Win32 STARTUPINFOW（x/sys 的 StartupInfo 无法扩展成 EX 版，故自带一份）。
type startupInfoW struct {
	cb              uint32
	lpReserved      uintptr
	lpDesktop       uintptr
	lpTitle         uintptr
	dwX             uint32
	dwY             uint32
	dwXSize         uint32
	dwYSize         uint32
	dwXCountChars   uint32
	dwYCountChars   uint32
	dwFillAttribute uint32
	dwFlags         uint32
	wShowWindow     uint16
	cbReserved2     uint16
	_               uint32 // 补齐到 8 字节对齐，使 lpReserved2 落在偏移 72
	lpReserved2     uintptr
	hStdInput       uintptr
	hStdOutput      uintptr
	hStdError       uintptr
}

// startupInfoExW 对应 STARTUPINFOEXW = STARTUPINFOW + 属性表指针。
type startupInfoExW struct {
	startupInfoW
	lpAttributeList uintptr
}

// processInformation 对应 Win32 PROCESS_INFORMATION。
type processInformation struct {
	hProcess    uintptr
	hThread     uintptr
	dwProcessId uint32
	dwThreadId  uint32
}

// ConPty 是一个 ConPTY 会话句柄：既持有伪控制台与子进程，也提供宿主侧的读写/等待/终止。
type ConPty struct {
	pc       uintptr // HPCON
	inW      windows.Handle
	outR     windows.Handle
	proc     windows.Handle
	pid      int
	attrList []byte
	closed   atomic.Bool
	// ptyOnce 保证伪控制台只关闭一次：重复 ClosePseudoConsole 会损坏堆（实测 0xC0000374）。
	ptyOnce   sync.Once
	closeOnce sync.Once
}

// StartConPty 以伪控制台拉起 exe。
// 子进程的标准输入/输出/错误全部接到伪控制台（对它而言就是控制台），
// 宿主通过返回的 *ConPty.Read 读取合并输出。dir 为空时继承当前工作目录。
func StartConPty(exe string, args []string, dir string) (*ConPty, error) {
	if exe == "" {
		return nil, errors.New("StartConPty: exe 为空")
	}

	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("CreatePipe(in) 失败: %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, fmt.Errorf("CreatePipe(out) 失败: %w", err)
	}

	// 伪控制台：COORD 是两个 int16，打包进一个 32 位值传入。
	var pc uintptr
	size := uint32(uint16(120)) | uint32(uint16(50))<<16
	_, _, err := pCreatePseudoConsole.Call(uintptr(size), uintptr(inR), uintptr(outW), 0, uintptr(unsafe.Pointer(&pc)))
	if err := callErr(err); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		windows.CloseHandle(outW)
		return nil, fmt.Errorf("CreatePseudoConsole 失败（需 Windows 10 1809+）: %w", err)
	}
	// 伪控制台已接管这两端，宿主侧不再持有。
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)

	// 准备 STARTUPINFOEX + 属性表（两次调用：先取所需字节数，再真正初始化）。
	var attrSize uintptr
	_, _, err = pInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&attrSize)))
	if err := callErr(err); err != nil && attrSize == 0 {
		pClosePseudoConsole.Call(pc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("InitializeProcThreadAttributeList(取长度) 失败: %w", err)
	}
	attrList := make([]byte, attrSize)
	attrPtr := uintptr(unsafe.Pointer(&attrList[0]))
	_, _, err = pInitializeProcThreadAttributeList.Call(attrPtr, 1, 0, uintptr(unsafe.Pointer(&attrSize)))
	if err := callErr(err); err != nil {
		pClosePseudoConsole.Call(pc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("InitializeProcThreadAttributeList 失败: %w", err)
	}
	hpcon := pc
	_, _, err = pUpdateProcThreadAttribute.Call(attrPtr, 0, procThreadAttrPseudoConsole,
		hpcon, unsafe.Sizeof(hpcon), 0, 0)
	if err := callErr(err); err != nil {
		pDeleteProcThreadAttributeList.Call(attrPtr)
		pClosePseudoConsole.Call(pc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("UpdateProcThreadAttribute 失败: %w", err)
	}

	si := startupInfoExW{}
	si.cb = uint32(unsafe.Sizeof(si))
	si.lpAttributeList = attrPtr

	// 命令行需按 Windows argv 规则转义（路径含空格时尤其重要）。
	cmdLine := syscall.EscapeArg(exe)
	for _, a := range args {
		cmdLine += " " + syscall.EscapeArg(a)
	}
	pExe, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		pDeleteProcThreadAttributeList.Call(attrPtr)
		pClosePseudoConsole.Call(pc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("转换 exe 路径失败: %w", err)
	}
	pCmd, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		pDeleteProcThreadAttributeList.Call(attrPtr)
		pClosePseudoConsole.Call(pc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("转换命令行失败: %w", err)
	}
	var pDir *uint16
	if dir != "" {
		if pDir, err = windows.UTF16PtrFromString(dir); err != nil {
			pDeleteProcThreadAttributeList.Call(attrPtr)
			pClosePseudoConsole.Call(pc)
			windows.CloseHandle(inW)
			windows.CloseHandle(outR)
			return nil, fmt.Errorf("转换工作目录失败: %w", err)
		}
	}

	var pi processInformation
	// 注意：句柄继承传 0——子进程通过属性表连上伪控制台，不靠句柄继承。
	_, _, err = pCreateProcessW.Call(
		uintptr(unsafe.Pointer(pExe)),
		uintptr(unsafe.Pointer(pCmd)),
		0, 0, 0,
		extendedStartupInfoPresent,
		0,
		uintptr(unsafe.Pointer(pDir)),
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if err := callErr(err); err != nil {
		pDeleteProcThreadAttributeList.Call(attrPtr)
		pClosePseudoConsole.Call(pc)
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("CreateProcessW 失败: %w", err)
	}

	// 主线程句柄用不到，创建后立即释放（不保存，避免 Close 时重复关闭）。
	windows.CloseHandle(windows.Handle(pi.hThread))

	return &ConPty{
		pc:       pc,
		inW:      inW,
		outR:     outR,
		proc:     windows.Handle(pi.hProcess),
		pid:      int(pi.dwProcessId),
		attrList: attrList,
	}, nil
}

// Pid 返回子进程 PID。
func (c *ConPty) Pid() int { return c.pid }

// Read 读取伪控制台的合并输出（stdout+stderr）。伪控制台关闭后返回 io.EOF。
func (c *ConPty) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, io.EOF
	}
	var done uint32
	err := windows.ReadFile(c.outR, p, &done, nil)
	if err != nil {
		if err == windows.ERROR_BROKEN_PIPE {
			return int(done), io.EOF
		}
		return int(done), err
	}
	if done == 0 {
		return 0, io.EOF
	}
	return int(done), nil
}

// Write 向子进程的标准输入写入（伪控制台通道）。
func (c *ConPty) Write(p []byte) (int, error) {
	var done uint32
	err := windows.WriteFile(c.inW, p, &done, nil)
	return int(done), err
}

// Wait 阻塞等待子进程退出并返回退出码。
func (c *ConPty) Wait() (int, error) {
	_, err := windows.WaitForSingleObject(c.proc, infinite)
	if err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(c.proc, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}

// Kill 强制结束子进程。
// 顺序：先 TerminateProcess，再关伪控制台——反过来的话进程会在控制台被拆除时
// 异常终止（实测退出码 0xC0000374），日志里会留下误导性的「进程异常退出」。
//
// 这里只关伪控制台、不关管道句柄：可能有 goroutine 正阻塞在 outR 的 Read 上，
// 而「关闭带未完成 I/O 的句柄」在 Windows 上是未定义行为（同样会触发堆损坏）。
// 关掉伪控制台后，写端随之关闭，阻塞中的 Read 会收到 broken pipe 并返回 EOF。
func (c *ConPty) Kill() error {
	var code uint32
	if err := windows.GetExitCodeProcess(c.proc, &code); err == nil && code == 259 { // STILL_ACTIVE
		if terr := windows.TerminateProcess(c.proc, 1); terr != nil {
			c.ClosePty()
			return terr
		}
	}
	c.ClosePty()
	return nil
}

// ClosePty 幂等地关闭伪控制台（不触碰管道句柄），用于让阻塞中的 Read 尽快收到 EOF。
func (c *ConPty) ClosePty() {
	c.ptyOnce.Do(func() {
		pClosePseudoConsole.Call(c.pc)
	})
}

// Close 释放伪控制台、管道与进程句柄。
// 前置条件：读取端已结束（Read 返回 EOF），否则不得关闭仍阻塞在 Read 上的句柄。
func (c *ConPty) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.ClosePty()
		windows.CloseHandle(c.inW)
		windows.CloseHandle(c.outR)
		if c.attrList != nil {
			pDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&c.attrList[0])))
		}
		windows.CloseHandle(c.proc)
	})
}
