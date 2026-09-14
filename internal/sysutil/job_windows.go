//go:build windows

package sysutil

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	jobMu     sync.Mutex
	jobHandle windows.Handle
	// jobReady 标记 Job 已成功建立。用显式互斥量 + 就绪位而非 sync.Once：
	// CreateJobObject / SetInformationJobObject 万一失败（罕见），sync.Once 会让
	// Job 在整个进程生命周期内永久失效；这里失败则保持 false，下次调用重新尝试。
	jobReady bool
)

// initJobLocked 惰性创建全局 Job Object 并设 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE。
// 调用方必须已持有 jobMu。
//
// KILL_ON_JOB_CLOSE 必须用 JobObjectExtendedLimitInformation（扩展结构），
// 用 BasicLimitInformation 在 Win11 上会返回 ERROR_INVALID_PARAMETER(87)。
func initJobLocked() {
	if jobReady {
		return
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(h)
		return
	}
	jobHandle = h
	jobReady = true
}

// AssignProcessToJob 把子进程挂入本进程的全局 Job Object，令其随 QuickDock 退出
// 而被内核一并回收。
//
// 为什么需要它：仅靠 Go 侧的关停回调（ServiceShutdown → Stop）只能覆盖「正常退出」。
// 进程被强杀 / 崩溃 / `wails3 dev` 重编译替换时，那条回调根本不会执行，子进程就会
// 变成孤儿：继续占端口、吃内存，且下次启动无法复用（token 已随旧进程丢失）。
// 挂入 Job 后由内核在句柄关闭时终止，与关停回调是否执行无关。
//
// 子进程自身创建的孙进程默认也落在同一 Job 内，一并回收。
//
// 失败静默忽略（不致命）：典型场景是 QuickDock 自身已处于某个不允许 breakaway 的
// Job 中，此时 AssignProcessToJobObject 返回 ERROR_ACCESS_DENIED；此时子进程仍由
// 外层 Job 兜底，行为与不调用本函数一致，故不视为错误。
func AssignProcessToJob(pid int) {
	if pid <= 0 {
		return
	}
	jobMu.Lock()
	initJobLocked()
	h, ready := jobHandle, jobReady
	jobMu.Unlock()
	if !ready || h == 0 {
		return
	}
	// AssignProcessToJobObject 需要 PROCESS_SET_QUOTA 权限，故用 ALL_ACCESS 打开。
	ph, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(ph)
	_ = windows.AssignProcessToJobObject(h, ph)
}
