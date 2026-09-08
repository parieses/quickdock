//go:build darwin || linux

package platform

import (
	"fmt"
	"runtime"
	"strings"

	"quickdock/internal/sysutil"
)

// SimulatePaste 模拟粘贴快捷键（darwin=Cmd+V）。
// 其余平台无实现，静默返回。
func SimulatePaste() {
	if runtime.GOOS != "darwin" {
		return
	}
	_ = sysutil.Command("osascript", "-e",
		`tell application "System Events" to keystroke "v" using {command down}`).Start()
}

// GetClipboardText 读取系统剪贴板纯文本（darwin=pbpaste，linux=xclip/xsel）。
// 失败返回空串（与 Windows 版一致，永不返回错误）。
func GetClipboardText() string {
	name := "xclip"
	args := []string{"-selection", "clipboard", "-o"}
	if runtime.GOOS == "darwin" {
		name, args = "pbpaste", nil
	}
	out, err := sysutil.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}

// GetActiveWindowTitle 返回前台窗口标题。
// darwin 通过 osascript 取最前台进程名；失败返回空串。
func GetActiveWindowTitle() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := sysutil.Command("osascript", "-e",
		`tell application "System Events" to get name of first application process whose frontmost is true`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SetClipboardFiles 将文件路径列表写入剪贴板。
// 非 Windows 暂不支持 CF_HDROP 等价物，退化为写入以换行分隔的路径文本。
func SetClipboardFiles(hwnd uintptr, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return setClipboardText(strings.Join(paths, "\n"))
}

// SetClipboardImage 将图片写入剪贴板。非 Windows 暂不支持，返回明确错误。
func SetClipboardImage(hwnd uintptr, imagePath string) error {
	return fmt.Errorf("当前平台暂不支持写入图片到剪贴板")
}

// setClipboardText 写入纯文本到剪贴板（darwin=pbcopy）。
func setClipboardText(text string) error {
	name := "xclip"
	args := []string{"-selection", "clipboard"}
	if runtime.GOOS == "darwin" {
		name, args = "pbcopy", nil
	}
	cmd := sysutil.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
