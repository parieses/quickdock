// Package scriptrun 脚本执行引擎。
//
// 职责单一：把「一份脚本 + 一组变量」变成「一次可观测的运行」——解析解释器、
// 拼参数、落日志文件、把输出逐行转发给上层。不碰数据库、不碰前端事件，
// 于是调度器、HTTP 触发、MCP、手动运行四条路都能复用同一份实现。
package scriptrun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// 支持的语言。custom 必须自带解释器，其余按候选名解析。
//
// go / php 与前三者的差别只在「怎么把入口交给解释器」：Python 与 Node 都是
// `解释器 入口`，而 Go 要 `go run 入口`（源码不是可执行文件），PHP 则要多带
// 几条 -d 让 notice/warning 真的进日志。这些差异都收敛在 Argv 里，其余链路
// （解析、日志、退出码、并发闸门）完全共用。
const (
	LangPython     = "python"
	LangJavaScript = "javascript"
	LangShell      = "shell"
	LangGo         = "go"
	LangPHP        = "php"
	LangCustom     = "custom"
)

// RuntimeLocator 由服务层注入：给定语言返回 QuickDock 托管运行时的 bin 目录。
// 未安装时返回 ""，引擎回退到 PATH。做成函数而不是直接 import internal/env，
// 是为了让这个包保持零依赖（测试里可以塞假目录）。
type RuntimeLocator func(lang string) string

// Spec 一次运行所需的全部输入。
type Spec struct {
	ID   string
	Name string
	Lang string
	// Entry 相对脚本目录的入口文件。
	Entry string
	// Interpreter 覆盖解释器（绝对路径或裸命令名），空 = 按语言解析。
	Interpreter string
	// Dir 脚本工作目录（绝对路径）。
	Dir  string
	Args []string
	// Prefix 插在解释器之后、入口文件之前的子命令。
	//
	// 装依赖就靠它复用同一条链路：`python -m pip install -r requirements.txt`
	// 与 `npm install` 都不是「脚本入口」，但日志、退出码、收尾语义完全一样，
	// 另写一套执行路径只会让日志面板出现两种格式。
	Prefix []string
	// Env 完整环境变量（调用方已在 os.Environ 基础上合并，此处直接给 cmd.Env）。
	Env []string
	// Timeout <= 0 表示不限时。
	Timeout time.Duration
}

// Interpreter 解析结果。
type Interpreter struct {
	Exe string
	// From 说明来源，用于日志与排错：runtime / path / override。
	From string
}

// Result 一次运行的收尾结果。
type Result struct {
	// Status ok | fail | canceled | timeout
	Status   string
	ExitCode int
	Duration time.Duration
	// Summary 末段输出摘要（单行、截断），供列表与通知用；完整输出在 LogPath。
	Summary string
	LogPath string
}

// Sink 逐行输出回调。
type Sink func(stream, line string)

// Resolve 解析解释器：脚本指定 → QuickDock 托管运行时 → PATH。
//
// 三级而不是两级，是 QuickDock 相对青龙的实际优势：用户不必先自己装
// Node/Python，环境管理里点一下装好的便携运行时这里能直接用。
func Resolve(spec Spec, locate RuntimeLocator) (Interpreter, error) {
	if s := strings.TrimSpace(spec.Interpreter); s != "" {
		if filepath.IsAbs(s) {
			if info, err := os.Stat(s); err != nil || info.IsDir() {
				return Interpreter{}, fmt.Errorf("指定的解释器不存在：%s", s)
			}
			return Interpreter{Exe: s, From: "override"}, nil
		}
		if p, err := exec.LookPath(s); err == nil {
			return Interpreter{Exe: p, From: "override"}, nil
		}
		// 裸名也可能是某个已装运行时的 bin 目录里的相对路径（如 "python3.12"），
		// 最后再按语言候选目录兜一次，避免用户在解释器里写了个只有便携目录才有的名字。
		if locate != nil {
			if p := platform.ResolveExeIn(locate(spec.Lang), candidateNames(s)...); p != "" {
				return Interpreter{Exe: p, From: "runtime"}, nil
			}
		}
		return Interpreter{}, fmt.Errorf("找不到解释器：%s", s)
	}

	names := candidateNames(spec.Lang)
	if len(names) == 0 {
		return Interpreter{}, fmt.Errorf("自定义语言必须填写解释器")
	}
	if locate != nil {
		if dir := locate(spec.Lang); dir != "" {
			if p := platform.ResolveExeIn(dir, names...); p != "" {
				return Interpreter{Exe: p, From: "runtime"}, nil
			}
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return Interpreter{Exe: p, From: "path"}, nil
		}
	}
	return Interpreter{}, fmt.Errorf("未找到 %s 解释器，请到「环境管理」安装，或在脚本里指定解释器路径", langLabel(spec.Lang))
}

// candidateNames 按语言给候选可执行名。Windows 要带 .exe 才能被 ResolveExeIn 命中。
func candidateNames(lang string) []string {
	switch lang {
	case LangPython:
		if runtime.GOOS == "windows" {
			return []string{"python.exe", "python3.exe"}
		}
		return []string{"python3", "python"}
	case LangJavaScript:
		// bun 排在 node 后面：脚本生态绝大多数是 node 语义（require / node_modules），
		// bun 虽然更快但兼容面窄，只在用户没装 node 时兜底。
		if runtime.GOOS == "windows" {
			return []string{"node.exe", "bun.exe"}
		}
		return []string{"node", "bun"}
	case LangShell:
		if runtime.GOOS == "windows" {
			// .sh 在 Windows 只能靠 Git Bash / WSL 的 sh；cmd 跑不了。
			// 不给 cmd.exe 兜底——那会让脚本「成功退出 0」但一行都没执行。
			return []string{"bash.exe", "sh.exe"}
		}
		return []string{"bash", "sh"}
	case LangGo:
		if runtime.GOOS == "windows" {
			return []string{"go.exe"}
		}
		return []string{"go"}
	case LangPHP:
		if runtime.GOOS == "windows" {
			return []string{"php.exe"}
		}
		return []string{"php"}
	}
	return nil
}

func langLabel(lang string) string {
	switch lang {
	case LangPython:
		return "Python"
	case LangJavaScript:
		return "Node.js"
	case LangShell:
		return "Shell"
	case LangGo:
		return "Go"
	case LangPHP:
		return "PHP"
	}
	return lang
}

// Argv 组装命令行。entry 为空时只跑解释器（custom 的常见形态：
// 解释器本身带全部参数，如 `powershell -File x.ps1`）。
//
// 两条语言特例都只在「没有 Prefix」时生效：Prefix 非空说明这是一条工具链命令
// （`go mod tidy`、`python -m pip install`、`php composer.phar install`），
// 塞 `run` 或 `-d` 进去会直接把它变成另一条命令。
func Argv(ip Interpreter, spec Spec) []string {
	argv := []string{ip.Exe}
	if len(spec.Prefix) == 0 {
		switch spec.Lang {
		case LangGo:
			// Go 源码不是可执行文件，必须经 `go run`。它的编译错误走 stderr、
			// 退出码非 0，现有链路照样能判 fail，不需要额外处理。
			if strings.TrimSpace(spec.Entry) != "" {
				argv = append(argv, "run")
			}
		case LangPHP:
			// 便携 PHP 通常带 php.ini-production，display_errors=Off ——
			// 脚本报错时页面「退出码 0、什么都没有」，比报错本身更难查。
			// 强制把错误送 stderr（日志里带 [err] 前缀），并全量报错级别。
			argv = append(argv, "-d", "display_errors=stderr", "-d", "error_reporting=E_ALL")
		}
	}
	argv = append(argv, spec.Prefix...)
	if e := strings.TrimSpace(spec.Entry); e != "" {
		argv = append(argv, filepath.Join(spec.Dir, filepath.FromSlash(e)))
	}
	return append(argv, spec.Args...)
}

// LogRoot 脚本日志根目录（在日志目录内，故现有 logtail / 日志页能直接打开）。
func LogRoot() string {
	base := logger.LogDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "scripts")
}

// LogPath 一次运行的日志文件路径。用 runID 做文件名，不给 script_runs 加列
// 也能反查——路径是纯函数推导出来的。
func LogPath(scriptID, runID string) string {
	root := LogRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, scriptID, runID+".log")
}

// summaryKeep 摘要保留的输出长度（字节）。
const summaryKeep = 4096

// Run 执行一次脚本。
//
// 调用方负责：并发闸门、写 script_runs、发通知。本函数只管「跑起来 + 落日志 +
// 给结论」。
func Run(ctx context.Context, spec Spec, ip Interpreter, logPath string, sink Sink) Result {
	started := time.Now()
	argv := Argv(ip, spec)

	var logFile *os.File
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			// 0644：日志里可能有变量值（脱敏在服务层），不需要 0600 那么严，
			// 但也不给组/其他写权限。
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
				logFile = f
				defer logFile.Close()
			}
		}
	}

	writeLine := func(s string) {
		if logFile != nil {
			_, _ = logFile.WriteString(s + "\n")
		}
	}

	// 头部：把「凭什么这么跑」写清楚，事后排查不用猜。
	writeLine("# " + spec.Name + "  " + started.Format("2006-01-02 15:04:05"))
	writeLine("# 命令: " + strings.Join(argv, " "))
	writeLine("# 目录: " + spec.Dir)
	writeLine("# 解释器: " + ip.Exe + " (" + ip.From + ")")
	if spec.Timeout > 0 {
		writeLine(fmt.Sprintf("# 超时: %s", spec.Timeout))
	}
	writeLine("# stderr 输出以 [err] 前缀区分")
	writeLine("")

	var tail strings.Builder // 末段输出，用于摘要
	appendTail := func(line string) {
		tail.WriteString(line)
		tail.WriteByte('\n')
		if tail.Len() > summaryKeep*2 {
			// 只留后半段：摘要要「最后一次报错」，不是「开头那几行 banner」。
			s := tail.String()
			tail.Reset()
			tail.WriteString(s[len(s)-summaryKeep:])
		}
	}

	res := platform.StreamCommand(ctx, argv, platform.StreamOptions{
		Dir:     spec.Dir,
		Env:     spec.Env,
		Timeout: spec.Timeout,
		OnLine: func(stream, line string) {
			if stream == "stderr" {
				writeLine("[err] " + line)
			} else {
				writeLine(line)
			}
			appendTail(line)
			if sink != nil {
				sink(stream, line)
			}
		},
	})

	dur := time.Since(started)
	status := "ok"
	switch {
	case res.TimedOut:
		status = "timeout"
	case res.Canceled:
		status = "canceled"
	case res.Err != nil:
		status = "fail"
	case res.ExitCode != 0:
		status = "fail"
	}

	// 收尾信息同时进日志：日志和 script_runs 的结论必须能对上，
	// 否则「日志显示跑完了、列表显示还在跑」这类问题查不胜查。
	switch status {
	case "timeout":
		writeLine(fmt.Sprintf("# 超时结束（%s）", spec.Timeout))
	case "canceled":
		writeLine("# 已取消")
	}
	if res.Err != nil {
		writeLine("# 启动失败：" + res.Err.Error())
	}
	writeLine(fmt.Sprintf("# 退出码 %d · 耗时 %s", res.ExitCode, dur.Round(time.Millisecond)))
	if logFile != nil {
		_ = logFile.Sync()
	}

	summary := flatten(tail.String())
	if res.Err != nil {
		summary = "启动失败：" + res.Err.Error()
	} else if summary == "" {
		summary = fmt.Sprintf("退出码 %d，无输出", res.ExitCode)
	}
	return Result{
		Status:   status,
		ExitCode: res.ExitCode,
		Duration: dur,
		Summary:  summary,
		LogPath:  logPath,
	}
}

// flatten 把多行输出压成单行摘要（rune 截断，不切坏中文）。
func flatten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 500 {
		return string(r[:500]) + "…"
	}
	return s
}

// ReadTail 读日志尾部 maxBytes 字节（按行对齐），供前端一次性拉取。
//
// 为什么不是直接 ReadFile：一次跑三小时的脚本日志能有几百 MB，
// 而界面只显示末尾几十行。
func ReadTail(path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	if maxBytes <= 0 || maxBytes > size {
		maxBytes = size
	}
	if _, err := f.Seek(size-maxBytes, 0); err != nil {
		return "", err
	}
	buf := make([]byte, maxBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	out := string(buf[:n])
	// 从中间切进来时首行可能是半行，丢掉它避免出现半截中文。
	if maxBytes < size {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out, nil
}
