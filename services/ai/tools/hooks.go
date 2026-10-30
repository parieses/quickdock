package aitools

// 项目级钩子：<cwd>/.quickdock/hooks.json。
//
// 解决的是「模型改完文件之后该跑的检查」这一类需求：格式化、lint、单元测试。
// 没有它，用户只能每次在提示词里写一遍「改完记得跑 gofmt」。
//
// 为什么**默认关闭、且必须由用户显式批准每一个目录**：
// hook 把「AI 能改文件」升级成「AI 能执行任意命令」——一份 clone 下来的仓库
// 自带 hooks.json，就足以在模型第一次改文件时跑任意脚本，而这一步完全绕过
// 等级门与审批闸门（闸门拦的是工具，不是工具跑起来之后发生的事）。
// 所以批准按**绝对路径**记，换目录、换机器、重新 clone 都要重新批一次。
//
// 三条硬约束：
//  1. 单条 10s 超时，超时整组杀掉（与 run_command 同一套 killCommandTree）。
//  2. 输出上限 2KB：hook 的输出是给模型看的一句话结论，不是构建日志。
//  3. **失败不阻断**：hook 报错只作为结果的一部分告诉模型，工具本身照常成功——
//     「lint 挂了」不该让一次正确的文件修改变成失败，回滚点也不该因此被回退。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/sysutil"
)

const (
	// aiHooksApprovedKey 已批准目录的落库键（JSON 数组，存绝对路径）。
	aiHooksApprovedKey = "ai_hooks_approved"
	aiHookDirName      = ".quickdock"
	aiHookFileName     = "hooks.json"
	aiHookTimeout      = 10 * time.Second
	aiHookOutputMax    = 2 << 10
)

// HooksInfo 一个目录的钩子情况（给设置/前端看）。
type HooksInfo struct {
	Found bool `json:"found"`
	// Path hooks.json 的绝对路径（Found 时才有意义）。
	Path string `json:"path"`
	// AfterEdit 改完文件后要跑的命令。
	AfterEdit []string `json:"afterEdit"`
	// BeforeCommand 执行命令前要跑的命令。
	BeforeCommand []string `json:"beforeCommand"`
	// Approved 用户是否已显式批准这个目录的钩子。
	Approved bool `json:"approved"`
	// Error 配置读不出来时的原因（比如 JSON 写坏了）。有值即视为未生效。
	Error string `json:"error"`
}

// aiHookConfig hooks.json 的结构。
//
// 只认两个键、值必须是字符串数组：hook 是「跑一条命令」，不做条件表达式、
// 不做参数插值。参数多了必然要引入一套脚本语言，而那等于把这里变成第二个
// run_command——它本来就该走 run_command。
type aiHookConfig struct {
	AfterEdit     []string `json:"afterEdit"`
	BeforeCommand []string `json:"beforeCommand"`
}

// hooksPath 一个目录的 hooks.json 路径。
func hooksPath(cwd string) string {
	return filepath.Join(cwd, aiHookDirName, aiHookFileName)
}

// HooksFor 读一个目录的钩子配置与批准状态。
//
// 不经过 fsguard：这里只读 <cwd>/.quickdock/hooks.json 这一个固定路径，
// 而且是否执行另有一道批准闸门；fsguard 的白名单是给「模型指定路径」用的。
func HooksFor(database *db.Database, cwd string) HooksInfo {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return HooksInfo{}
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return HooksInfo{}
	}
	info := HooksInfo{Path: hooksPath(abs)}
	data, rerr := os.ReadFile(info.Path)
	if rerr != nil {
		// 文件不存在是最常见的情况，不算错误。
		return HooksInfo{}
	}
	info.Found = true
	var cfg aiHookConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		info.Error = "hooks.json 解析失败：" + err.Error()
		return info
	}
	info.AfterEdit = cfg.AfterEdit
	info.BeforeCommand = cfg.BeforeCommand
	info.Approved = hooksApproved(database, abs)
	return info
}

// SetHooksApproved 批准/取消批准一个目录的钩子。
func SetHooksApproved(database *db.Database, cwd string, on bool) error {
	if database == nil {
		return errors.New("数据库不可用")
	}
	abs, err := filepath.Abs(strings.TrimSpace(cwd))
	if err != nil || strings.TrimSpace(cwd) == "" {
		return errors.New("工作目录无效")
	}
	set := approvedHooksSet(database)
	if on {
		set[abs] = true
	} else {
		delete(set, abs)
	}
	if len(set) == 0 {
		return database.SetSetting(aiHooksApprovedKey, "")
	}
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return database.SetSetting(aiHooksApprovedKey, string(b))
}

// approvedHooksSet 读已批准目录集合。
//
// 读坏了按「一个都没批」处理：这是唯一安全的失败方向——多批准一个目录等于
// 让某个仓库的脚本能跑起来，少批准只是 hook 不生效（用户会来问）。
func approvedHooksSet(database *db.Database) map[string]bool {
	out := map[string]bool{}
	if database == nil {
		return out
	}
	raw, err := database.GetSetting(aiHooksApprovedKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return out
	}
	var dirs []string
	if err := json.Unmarshal([]byte(raw), &dirs); err != nil {
		logger.W("[ai-hook] 批准列表解析失败，按未批准处理: %v", err)
		return out
	}
	for _, d := range dirs {
		if d = strings.TrimSpace(d); d != "" {
			out[d] = true
		}
	}
	return out
}

// hooksApproved 这个目录的钩子是否获得批准。
func hooksApproved(database *db.Database, cwd string) bool {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	return approvedHooksSet(database)[abs]
}

// runAIHooks 跑某个阶段的所有钩子，返回给模型看的说明（没有可跑的返回 ""）。
//
// phase 取 "afterEdit" / "beforeCommand"，与 hooks.json 的两个键同名。
// vars 是塞进说明里的上下文（改了哪个文件、要跑什么命令），只用于展示，
// 不做参数插值——不做插值是为了不给 hook 引入一套模板语法。
func runAIHooks(d Deps, cwd, phase string, vars map[string]string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || d.DB == nil {
		return ""
	}
	info := HooksFor(d.DB, cwd)
	if !info.Found || info.Error != "" || !info.Approved {
		return ""
	}
	cmds := info.AfterEdit
	if phase == "beforeCommand" {
		cmds = info.BeforeCommand
	}
	if len(cmds) == 0 {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	label := "改完文件后的钩子"
	if phase == "beforeCommand" {
		label = "执行命令前的钩子"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]（来自 %s）", label, filepath.Join(aiHookDirName, aiHookFileName))
	if v := vars["file"]; v != "" {
		fmt.Fprintf(&b, "\n文件: %s", v)
	}
	if v := vars["command"]; v != "" {
		fmt.Fprintf(&b, "\n命令: %s", v)
	}
	for i, cmd := range cmds {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		out, ok, dur := execHook(d, abs, cmd)
		status := "ok"
		if !ok {
			status = "失败"
		}
		fmt.Fprintf(&b, "\n%d) %s → %s（%dms）", i+1, cmd, status, dur.Milliseconds())
		if s := strings.TrimSpace(out); s != "" {
			fmt.Fprintf(&b, "\n%s", logger.Trunc(s, aiHookOutputMax))
		}
	}
	b.WriteString("\n钩子是项目自己配的检查，失败不影响本次操作已生效的结果；按提示修完再继续。")
	return b.String()
}

// attachHooks 跑 afterEdit 阶段的钩子，有产出就挂到工具结果上。
//
// 单独包一层而不让调用方直接用 runAIHooks：写类工具的成功路径已经够长，
// 「有没有钩子」这个判断不该散在每个工具的返回语句里。
func attachHooks(res map[string]any, d Deps, cwd, phase string, vars map[string]string) {
	if note := runAIHooks(d, cwd, phase, vars); note != "" {
		res["hooks"] = note
	}
}

// execHook 跑一条钩子命令，返回输出与是否成功。
func execHook(d Deps, dir, command string) (string, bool, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), aiHookTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
		cmd = sysutil.CommandContext(ctx, shell, "/d", "/s", "/c", command)
	} else {
		cmd = sysutil.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Env = commandEnv(d)
	setProcessGroup(cmd)

	start := time.Now()
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		// 超时：整组杀掉，否则 lint 起的子进程会继续跑。
		killCommandTree(cmd, dir)
		return fmt.Sprintf("超时（%ds）已终止", int(aiHookTimeout.Seconds())), false, time.Since(start)
	}
	ok := err == nil
	if !ok && len(out) == 0 {
		out = []byte(err.Error())
	}
	logger.I("[ai-hook] %s → %v", command, err)
	return string(out), ok, time.Since(start)
}
