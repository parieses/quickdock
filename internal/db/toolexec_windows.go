//go:build windows

package db

// 工具可执行文件解析。
//
// 内置工具表用裸名（chrome/msedge/code…）+ exec.Command 启动，
// 但 Windows 桌面程序（Chrome/Edge/Firefox 等）安装后不写入 PATH，
// exec.LookPath 直接失败："executable file not found in %PATH%"。
//
// 解析顺序：
//  1. 绝对路径或含路径分隔符 → 原样返回（存在性由 exec.Start 报错更准确）
//  2. PATH（保持旧行为，code/cmd 等命令行工具命中）
//  3. 注册表 App Paths（HKCU -> HKLM -> WOW6432Node）：
//     Chrome/Edge/Firefox/Brave/Opera 安装时都会注册，系统级"按名找应用"正道
//  4. 常见安装目录兜底（便携版/精简系统无注册表项）
//  5. 全部未命中 → 返回原名，让 exec 返回与旧版一致的错误信息

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// isTerminalTool 判断某个可执行文件是否为终端类工具
// （cmd/powershell/wt/wsl）。这些工具接收整条命令作为参数，
// 调用方需自行对含空格/括号的命令加引号。
func isTerminalTool(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "cmd", "cmd.exe", "powershell", "powershell.exe",
		"wt", "wt.exe", "wsl", "wsl.exe":
		return true
	}
	return false
}

// isCmdInterpreter 判断工具是否为 cmd.exe。只有它按 cmd 的元字符规则解析命令行；
// powershell / wsl 的转义字符各不相同（反引号 / 反斜杠），一律套 cmd 的 ^ 会打印出
// 多余的字符，所以转义只在 cmd 上做。
func isCmdInterpreter(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "cmd", "cmd.exe":
		return true
	}
	return false
}

// escapeCmdValue 转义 cmd.exe 元字符，让条目值只作为「一条命令」执行：
// & | < > 不再被解释成命令分隔 / 管道 / 重定向，" ^ % 也不再触发二次解析。
//
// 代价（有意为之）：依赖 shell 语法（管道、重定向、&& 串联）的条目会失效。
// 条目值来自用户数据，不能同时被当成「命令」和「脚本」——否则一条
// `echo hi & calc` 就能在「只是打开一个快捷项」时执行任意程序。
func escapeCmdValue(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	for _, r := range s {
		switch r {
		case '"', '%', '^', '&', '|', '<', '>':
			b.WriteByte('^')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tryTerminalTool 以“整条命令”方式启动终端类工具（cmd/powershell/wt/wsl）。
// 返回 (true, err) 表示已处理（含启动结果）；返回 (false, nil) 表示非终端工具，
// 调用方（item.go execOpen）应走通用参数展开路径。
// pathDirs 为条目绑定的运行时 bin 目录，非空时前置到子进程 PATH（项目级版本切换）。
//
// 终端工具会重新解析 /c 之后的整条命令行，路径里的空格、括号会被当成语法
// 分组而静默失败。解决：用 SysProcAttr.CmdLine 直接传递命令行，并对含空格/特殊
// 字符的值做 ""value"" 双重引号包裹（cmd 会剥掉最外层引号，保留内层引号当字面量）。
func tryTerminalTool(tool OpenTool, value, workingDir string, pathDirs []string) (bool, error) {
	if !isTerminalTool(tool.Path) {
		return false, nil
	}
	tmpl := tool.Args
	if tmpl == "" {
		switch strings.ToLower(filepath.Base(tool.Path)) {
		case "wt", "wt.exe":
			tmpl = "new-tab {{command}}"
		case "wsl", "wsl.exe":
			tmpl = "-- {{command}}"
		default:
			tmpl = "/c {{command}}"
		}
	}
	quoted := value
	if isCmdInterpreter(tool.Path) {
		quoted = escapeCmdValue(value)
	}
	if strings.ContainsAny(quoted, " \t()") {
		quoted = `""` + quoted + `""`
	}
	inner := strings.ReplaceAll(strings.ReplaceAll(tmpl, "{{command}}", quoted), "{{path}}", quoted)
	toolPath := resolveExecutable(tool.Path)
	c := exec.Command(toolPath)
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: toolPath + " " + inner}
	if workingDir != "" {
		c.Dir = workingDir
	}
	applyPathDirs(c, pathDirs)
	return true, startDetached(c)
}

// appPathsRoots App Paths 注册表根键探测顺序。
var appPathsRoots = []registry.Key{
	registry.CURRENT_USER,
	registry.LOCAL_MACHINE,
}

// browserRelPaths 裸名 -> 相对安装根目录的候选片段（用 filepath.Join 拼，
// 避免源码里写死平台分隔符）。
var browserRelPaths = map[string][][]string{
	"chrome": {
		{"Google", "Chrome", "Application", "chrome.exe"},
	},
	"msedge": {
		{"Microsoft", "Edge", "Application", "msedge.exe"},
	},
	"firefox": {
		{"Mozilla Firefox", "firefox.exe"},
	},
	"brave": {
		{"BraveSoftware", "Brave-Browser", "Application", "brave.exe"},
	},
	"vivaldi": {
		{"Vivaldi", "Application", "vivaldi.exe"},
	},
	"opera": {
		{"Opera", "opera.exe"},
		{"Opera", "launcher.exe"},
	},
}

// resolveExecutable 把工具名解析为可执行文件绝对路径；解析失败返回原值。
func resolveExecutable(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) || strings.ContainsRune(raw, os.PathSeparator) || strings.Contains(raw, "/") {
		return raw
	}
	if p, err := exec.LookPath(raw); err == nil {
		return p
	}
	name := strings.ToLower(raw)
	if !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	if p := lookupAppPaths(name); p != "" {
		return p
	}
	base := strings.TrimSuffix(name, ".exe")
	for _, segs := range browserRelPaths[base] {
		for _, root := range programRoots() {
			cand := filepath.Join(append([]string{root}, segs...)...)
			if fileExists(cand) {
				return cand
			}
		}
	}
	return raw
}

// lookupAppPaths 读 App Paths 下对应条目的默认值（完整可执行路径）。
func lookupAppPaths(name string) string {
	const leaf = "Software" + string(os.PathSeparator) + "Microsoft" + string(os.PathSeparator) +
		"Windows" + string(os.PathSeparator) + "CurrentVersion" + string(os.PathSeparator) +
		"App Paths" + string(os.PathSeparator)
	for _, root := range appPathsRoots {
		if p := readAppPathsKey(root, leaf+name); p != "" {
			return p
		}
	}
	const wow = "Software" + string(os.PathSeparator) + "WOW6432Node" + string(os.PathSeparator) +
		"Microsoft" + string(os.PathSeparator) + "Windows" + string(os.PathSeparator) +
		"CurrentVersion" + string(os.PathSeparator) + "App Paths" + string(os.PathSeparator)
	return readAppPathsKey(registry.LOCAL_MACHINE, wow+name)
}

func readAppPathsKey(root registry.Key, path string) string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	val, _, err := k.GetStringValue("")
	if err == nil && val != "" && fileExists(val) {
		return val
	}
	return ""
}

// programRoots 展开三类安装根目录，去重保序。
func programRoots() []string {
	var roots []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if v := os.Getenv(env); v != "" {
			dup := false
			for _, r := range roots {
				if strings.EqualFold(r, v) {
					dup = true
					break
				}
			}
			if !dup {
				roots = append(roots, v)
			}
		}
	}
	return roots
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
