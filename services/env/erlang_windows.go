//go:build windows

package env

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// killEpmdBeforeDelete 在删除某个 Erlang 版本目录前，先停掉属于该版本的 epmd.exe。
//
// 背景：epmd 是 Erlang 的端口映射守护进程（Erlang Port Mapper Daemon）。只要跑过
// 任何 Erlang/OTP 节点（含 RabbitMQ），epmd 就会常驻后台并锁住自身 exe，导致
// os.RemoveAll 报 "Access is denied"（即用户看到的“没权限”）。
//
// 仅匹配镜像路径位于 dir 下的 epmd：dir 即被删版本的 runtime/erlang/<version>，
// 这样不会误杀服务其他 Erlang 版本或正在运行 RabbitMQ 的 epmd。
func killEpmdBeforeDelete(dir string) {
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
	prefix := strings.ToLower(dir) + `\`
	for {
		name := windows.UTF16ToString(pe.szExeFile[:])
		if strings.EqualFold(name, "epmd.exe") {
			if p, err := processExePathWin(int(pe.th32ProcessID)); err == nil {
				if strings.HasPrefix(strings.ToLower(p), prefix) {
					killPID(int(pe.th32ProcessID))
				}
			}
		}
		if r, _, _ := procProcess32Next.Call(h, uintptr(unsafe.Pointer(&pe))); r == 0 {
			break
		}
	}
}
