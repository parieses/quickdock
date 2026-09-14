//go:build darwin || linux

package sysutil

// AssignProcessToJob 非 Windows 平台无 Job Object 概念，为空操作。
//
// unix 的孤儿进程由 init/systemd 收养，父进程退出不会连带终止子进程，
// 也没有与 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE 等价的单调用机制
// （PR_SET_PDEATHSIG 需由子进程自行设置，且仅覆盖直接子进程）。
// 故此处保持原有行为，由调用方自身的关停逻辑负责清理。
func AssignProcessToJob(pid int) {}
