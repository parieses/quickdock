package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/wailsapp/wails/v3/pkg/application"

	"quickdock/internal/sysutil"
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
//
// 路由策略（优先框架 Browser，跨平台；失败或不适配时回退 ShellExecute 直连）：
//   - http/https 网址、真实存在的本地路径 → app.Browser.OpenURL / OpenFile
//   - workingDir 非空（quicklink 需要工作目录，Browser 不支持）→ 直连
//   - 自定义协议（vscode:// 等）、不存在的裸目标 → 直连，维持旧的系统关联解析行为
func ShellOpen(target, workingDir string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("打开目标为空")
	}
	if err := rejectDangerous(target); err != nil {
		return err
	}

	if strings.TrimSpace(workingDir) == "" {
		lower := strings.ToLower(target)
		switch {
		case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
			// 网址：无空格歧义，直接走框架
			return browserOpen(target, true)
		case !strings.Contains(lower, "://"):
			// 仅真实存在且不含空白的本地路径走框架；
			// 含空白路径 rundll32 的参数解析不可靠，交 ShellExecute（对空格/引号最稳）
			if _, err := os.Stat(target); err == nil && !strings.ContainsAny(target, " \t") {
				return browserOpen(target, false)
			}
		}
	}
	return shellOpenDirect(target, workingDir)
}

// browserOpen 经框架 Browser 打开（Windows= rundll32 FileProtocolHandler，
// darwin=open，linux=xdg-open）；框架未就绪或调用失败时回退直连保持可用性。
func browserOpen(target string, isURL bool) error {
	if app := application.Get(); app != nil && app.Browser != nil {
		var err error
		if isURL {
			err = app.Browser.OpenURL(target)
		} else {
			err = app.Browser.OpenFile(target)
		}
		if err == nil {
			return nil
		}
	}
	return shellOpenDirect(target, "")
}

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

// RevealInExplorer 在文件资源管理器中定位并选中目标路径。
// 目录 → 直接打开该目录；文件 → explorer /select 高亮选中；路径不存在 → 退回打开父目录。
func RevealInExplorer(target string) error {
	target = strings.TrimSpace(strings.Trim(strings.TrimSpace(target), `"`))
	if target == "" {
		return fmt.Errorf("路径为空")
	}
	if err := rejectDangerous(target); err != nil {
		return err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		abs = target
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		parent := filepath.Dir(abs)
		if _, e := os.Stat(parent); e != nil {
			return fmt.Errorf("路径不存在: %s", target)
		}
		return ShellOpen(parent, "")
	}
	if info.IsDir() {
		return ShellOpen(abs, "")
	}
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
	sysutil.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// 异步 Wait 回收 Windows 进程句柄（与 explorer 分支一致），避免每次执行泄漏句柄
	go func() { _ = cmd.Wait() }()
	return nil
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
