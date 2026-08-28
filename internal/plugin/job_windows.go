//go:build windows

package plugin

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pluginJobHandle windows.Handle
	pluginJobOnce   sync.Once
)

// initPluginJob 创建全局 Job Object，并设 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE：
// 只要主进程退出（含崩溃/被强杀），内核关闭该 Job 句柄时，其中所有进程
// （含其孙进程，默认会一起落入同一 Job）会被一并终止，避免残留孤儿插件进程。
// 注意：KILL_ON_JOB_CLOSE 必须用 JobObjectExtendedLimitInformation（扩展结构），
// 用 JobObjectBasicLimitInformation 在 Win11 上会返回 ERROR_INVALID_PARAMETER(87)。
func initPluginJob() {
	pluginJobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			fmt.Printf("[plugin-job] 创建 Job Object 失败: %v\n", err)
			return
		}
		pluginJobHandle = h

		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(
			pluginJobHandle,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			fmt.Printf("[plugin-job] 设置 Job Object 限制失败: %v\n", err)
			windows.CloseHandle(pluginJobHandle)
			pluginJobHandle = 0
			return
		}
	})
}

// assignProcessToJob 把插件子进程挂入 Job Object（主进程退出时一并回收）。
// 入参为子进程 PID：内部以 PROCESS_ALL_ACCESS 打开句柄（AssignProcessToJobObject
// 需要 PROCESS_SET_QUOTA 权限），挂入后即关闭句柄（Job 已引用该进程）。
// 失败仅告警不致命：例如主进程自身已在某个不支持 breakaway 的 Job 中时，
// 子进程会继承父 Job，无法再分配到我们的 Job，忽略即可（生命周期仍由父 Job 兜底）。
func assignProcessToJob(pid uintptr) {
	initPluginJob()
	if pluginJobHandle == 0 || pid == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(pid))
	if err != nil {
		if err != windows.ERROR_ACCESS_DENIED {
			fmt.Printf("[plugin-job] OpenProcess 失败: %v\n", err)
		}
		return
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(pluginJobHandle, h); err != nil {
		if err != windows.ERROR_ACCESS_DENIED {
			fmt.Printf("[plugin-job] 加入 Job Object 失败: %v\n", err)
		}
	}
}
