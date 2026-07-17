package platform

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// dangerousSchemes 拒绝直接通过 ShellExecute 触发的危险协议（存储型协议注入防护）。
var dangerousSchemes = []string{
	"javascript:", "vbscript:", "ms-powershell:", "powershell:",
	"cmd:", "ms-msdt:", "msdt:", "wscript:", "cscript:",
}

func rejectDangerous(target string) error {
	lower := strings.ToLower(strings.TrimSpace(target))
	for _, p := range dangerousSchemes {
		if strings.HasPrefix(lower, p) {
			return fmt.Errorf("拒绝危险协议: %s", p)
		}
	}
	return nil
}

// ShellOpen 使用系统默认关联程序打开软件/文件/目录/网址（等价于双击/在浏览器打开）。
// workingDir 为空时使用进程默认目录。
func ShellOpen(target, workingDir string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("打开目标为空")
	}
	if err := rejectDangerous(target); err != nil {
		return err
	}
	var dirPtr *uint16
	if strings.TrimSpace(workingDir) != "" {
		dirPtr = windows.StringToUTF16Ptr(workingDir)
	}
	return windows.ShellExecute(0,
		windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(target),
		nil, dirPtr, windows.SW_SHOWNORMAL)
}

// RunCommand 执行一条命令行（按 argv 拆词，避免直接交给 shell 解释导致注入）。
// 若需要 shell 特性（管道/重定向），用户应显式写 cmd /c "..."。
func RunCommand(command, workingDir string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("命令内容为空")
	}
	argList := splitArgs(command)
	if len(argList) == 0 {
		return fmt.Errorf("命令内容为空")
	}
	cmd := exec.Command(argList[0], argList[1:]...)
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}
	// 隐藏子进程控制台窗口，避免定时命令弹黑框。
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

// splitArgs 按空格拆词，支持双引号包裹保留空格。
func splitArgs(args string) []string {
	var result []string
	var current []byte
	inQuotes := false
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case c == ' ' && !inQuotes:
			if len(current) > 0 {
				result = append(result, string(current))
				current = current[:0]
			}
		default:
			current = append(current, c)
		}
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}
	return result
}
