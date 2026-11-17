//go:build !windows

package platform

import (
	"os/exec"
	"runtime"

	"quickdock/internal/sysutil"
)

// commandForRun 非 Windows 平台没有 cmd 的括号解析问题，按原样走 argv。
func commandForRun(exe string, args []string) *exec.Cmd {
	return exec.Command(exe, args...)
}

// shellOpenDirect 用系统默认程序打开目标：
// darwin=open，linux=xdg-open。workingDir 仅作子进程工作目录。
func shellOpenDirect(target, workingDir string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := sysutil.Command(name, target)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	return cmd.Start()
}

// revealFile 在文件管理器中选中指定文件（darwin=Finder 显示，linux=xdg-open 父目录）。
func revealFile(abs string) error {
	if runtime.GOOS == "darwin" {
		return sysutil.Command("open", "-R", abs).Start()
	}
	return sysutil.Command("xdg-open", abs).Start()
}
