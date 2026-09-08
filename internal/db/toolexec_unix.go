//go:build darwin || linux

package db

// 非 Windows 占位：macOS 经 NSWorkspace/open -a、Linux 桌面程序走 desktop 文件
// 索引（gtk-launch），裸名通常可直接命中 /usr/bin；接入时在此补平台实现。

func resolveExecutable(raw string) string { return raw }

// tryTerminalTool 非 Windows 无 cmd/powershell/wt/wsl 终端类工具，
// 恒返回 done=false，让调用方走通用参数展开路径。
func tryTerminalTool(tool OpenTool, value, workingDir string) (bool, error) {
	return false, nil
}
