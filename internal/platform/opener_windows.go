//go:build windows

package platform

import (
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"quickdock/internal/sysutil"
)

// shellOpenDirect 直接经 ShellExecute 打开（支持 workingDir）。
func shellOpenDirect(target, workingDir string) error {
	var dirPtr *uint16
	if strings.TrimSpace(workingDir) != "" {
		dirPtr = windows.StringToUTF16Ptr(workingDir)
	}
	return windows.ShellExecute(0,
		windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(target),
		nil, dirPtr, windows.SW_SHOWNORMAL)
}

// revealFile 在资源管理器中选中指定文件（explorer /select）。
func revealFile(abs string) error {
	// explorer 对 /select 参数的解析不遵循标准 argv 规则，必须手写完整命令行，
	// 否则含空格的路径会被 Go 的自动引用规则拆散。
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `explorer.exe /select,"` + abs + `"`,
	}
	// sysutil.Hide 用 |= 合并，保留上面手写的 CmdLine（整体覆盖 SysProcAttr 会静默丢掉它）
	sysutil.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// explorer.exe 即使成功也常返回退出码 1，不能据此判定失败，直接释放句柄。
	go func() { _ = cmd.Wait() }()
	return nil
}
