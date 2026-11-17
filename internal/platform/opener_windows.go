//go:build windows

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"quickdock/internal/sysutil"
)

// commandForRun 为 RunCommandCapture 构造命令。
//
// 首段是本地存在的 .bat/.cmd 时不能走默认的 exec.Command：Go 在 Windows 上会
// 自动转成 `cmd /c "<exe>" ...`，但走的是 argv 引用规则——路径无空格时不会被引号
// 包裹，而 cmd 把 `(` `)` `&` 等当特殊字符，导致 `...\激活补丁(Navicat...)\x.bat`
// 被截断成 `...\激活补丁` 并报「不是内部或外部命令」（实测复现）。
// 这里手写完整 CmdLine，用 /s /c "call "路径"" 显式加引号（与 revealFile 同理），
// /s 让 cmd 只剥最外层引号、保留路径自身的引号，括号路径才不会被解释成语法。
// 非批处理（cmd /c、python、git 等）保持默认行为，避免改动既有引用语义。
func commandForRun(exe string, args []string) *exec.Cmd {
	if !isBatchFileOnDisk(exe) {
		return exec.Command(exe, args...)
	}
	sh := os.Getenv("ComSpec")
	if sh == "" {
		sh = os.Getenv("COMSPEC")
	}
	if sh == "" {
		sh = `C:\Windows\System32\cmd.exe`
	}
	// argv[0] 也加引号，避免系统目录含空格时被拆散；/s /c 后整体再包一层，
	// call 的路径引号作为内层引号被保留。参数原样拼在后面（RunCommandCapture
	// 已用 splitArgs 按空格拆好、并剥掉调用方包裹的引号）。
	var b strings.Builder
	b.WriteByte('"')
	b.WriteString(sh)
	b.WriteString(`" /s /c "call "`)
	b.WriteString(exe)
	b.WriteByte('"')
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(a)
	}
	b.WriteByte('"')

	cmd := exec.Command(sh)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: b.String()}
	return cmd
}

// isBatchFileOnDisk 判断首段是不是真实存在的批处理文件。
// 必须是磁盘上存在的文件才算——`cmd` `python` 这类 PATH 命令即使名字里带点
// 也不能误伤；相对名已在 RunCommandCapture 里拼成绝对路径，这里直接 Stat。
func isBatchFileOnDisk(exe string) bool {
	ext := strings.ToLower(filepath.Ext(exe))
	if ext != ".bat" && ext != ".cmd" {
		return false
	}
	info, err := os.Stat(exe)
	return err == nil && !info.IsDir()
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
