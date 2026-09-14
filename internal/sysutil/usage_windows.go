//go:build windows

package sysutil

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// procK32GetProcessMemoryInfo 取进程工作集大小。
// x/sys/windows 未导出该入口，直接绑 kernel32 的 K32 版本（Win7+，psapi 的转发别名）。
var procK32GetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").
	NewProc("K32GetProcessMemoryInfo")

// processMemoryCounters 对应 PROCESS_MEMORY_COUNTERS。
// SIZE_T 一律用 uintptr，32/64 位下字段偏移自动对齐，无需按架构分文件。
type processMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// SnapshotProcs 经 CreateToolhelp32Snapshot 枚举进程表，再逐进程取资源用量。
// 一次快照即拿到全部 PID 与父子关系；用量查询对系统/其他用户进程会因权限失败，
// 此时该进程的内存与 CPU 保持 0（不影响整表枚举与服务子树聚合）。
func SnapshotProcs() ([]ProcStat, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var out []ProcStat
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	err = windows.Process32First(snap, &e)
	for err == nil {
		if pid := int(e.ProcessID); pid > 0 {
			mem, cpu := procUsageWin(pid)
			out = append(out, ProcStat{
				PID:        pid,
				ParentPID:  int(e.ParentProcessID),
				MemBytes:   mem,
				CPUSeconds: cpu,
			})
		}
		err = windows.Process32Next(snap, &e)
	}
	return out, nil
}

// procUsageWin 取单进程的工作集与累计 CPU 时间；无权限时返回 0,0。
func procUsageWin(pid int) (memBytes int64, cpuSeconds float64) {
	h, err := windows.OpenProcess(
		windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return 0, 0
	}
	defer windows.CloseHandle(h)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err == nil {
		// FILETIME 以 100ns 为单位
		cpuSeconds = float64(filetimeUint64(kernel)+filetimeUint64(user)) / 1e7
	}

	var pmc processMemoryCounters
	pmc.Size = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(h),
		uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.Size)); r != 0 {
		memBytes = int64(pmc.WorkingSetSize)
	}
	return memBytes, cpuSeconds
}

// filetimeUint64 把 FILETIME 的高低 32 位合成 64 位计数。
func filetimeUint64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}
