//go:build windows

package env

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateToolhelp32Snapshot = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateToolhelp32Snapshot")
	procProcess32First           = windows.NewLazySystemDLL("kernel32.dll").NewProc("Process32FirstW")
	procProcess32Next            = windows.NewLazySystemDLL("kernel32.dll").NewProc("Process32NextW")
)

const th32csSnapProcess = 0x00000002

type processEntry32 struct {
	Size                uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [260]uint16
}

// killSQLHolders 杀掉 exe 名匹配 serverBin 且镜像路径位于 versionDir 下的所有进程。
// 用于 initDataDir 清空脏数据目录前解除文件占用：首次初始化中途崩溃、或仍有
// mysqld --initialize 残留进程锁住 #ib_16384_0.dblwr 等文件时，RemoveAll 会失败。
// 仅匹配该版本目录下的镜像，不会影响其他版本或其他 MySQL/MariaDB 实例。
func killSQLHolders(versionDir, serverBin string) {
	h, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if h == 0 || h == ^uintptr(0) {
		return
	}
	defer windows.CloseHandle(windows.Handle(h))
	var pe processEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if r, _, _ := procProcess32First.Call(h, uintptr(unsafe.Pointer(&pe))); r == 0 {
		return
	}
	for {
		name := windows.UTF16ToString(pe.szExeFile[:])
		if strings.EqualFold(name, serverBin) {
			if p, err := processExePathWin(int(pe.th32ProcessID)); err == nil {
				if strings.HasPrefix(strings.ToLower(p), strings.ToLower(versionDir)+`\`) {
					killPID(int(pe.th32ProcessID))
				}
			}
		}
		if r, _, _ := procProcess32Next.Call(h, uintptr(unsafe.Pointer(&pe))); r == 0 {
			break
		}
	}
}

func killPID(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.TerminateProcess(h, 1)
}
