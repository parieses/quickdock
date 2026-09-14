package db

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"quickdock/internal/logger"
)

// ItemEnvEntry 条目绑定的单个运行时版本（持久化在 items.env 的 JSON 数组里，与 scenes.env 同形）。
type ItemEnvEntry struct {
	Runtime string `json:"runtime"` // 运行时 id，如 node / php
	Version string `json:"version"` // 期望版本；空=跟随当前激活版本
}

// PathEnvResolver 由宿主注入的回调：给定运行时 id 与期望版本，返回该版本应前置到 PATH 的
// bin 目录；无法解析（该版本未安装、运行时未知）返回 ""。
// db 包不 import internal/env —— 保持叶子依赖，避免 db↔env 互相引用成环。
type PathEnvResolver func(runtime, version string) string

// pathEnvResolver 见 PathEnvResolver，由 SetPathEnvResolver 在应用启动时注入。
// 未注入时（单元测试、无环境管理器的构建）打开条目不做任何 PATH 注入，行为与旧版一致。
var pathEnvResolver PathEnvResolver

// SetPathEnvResolver 注入运行时 bin 目录解析器，应用启动时调用一次。
func SetPathEnvResolver(fn PathEnvResolver) { pathEnvResolver = fn }

// parseItemEnv 解析 items.env；空串或非法 JSON 一律视为未绑定，不报错、不阻断打开。
func parseItemEnv(raw string) []ItemEnvEntry {
	if raw == "" {
		return nil
	}
	var out []ItemEnvEntry
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		logger.W("QuickDock: 解析条目环境绑定失败: %v", err)
		return nil
	}
	return out
}

// itemPathDirs 返回条目绑定版本应前置到 PATH 的 bin 目录（按绑定顺序，未安装的跳过）。
func itemPathDirs(item *CollectionItem) []string {
	if pathEnvResolver == nil || item.Env == "" {
		return nil
	}
	entries := parseItemEnv(item.Env)
	if len(entries) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Runtime == "" {
			continue
		}
		if d := pathEnvResolver(e.Runtime, e.Version); d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// applyPathDirs 把 dirs 前置到 cmd 的 PATH（用于"项目级版本切换"：只影响本次打开的子进程，
// 不写系统环境变量）。
//
// 注意 cmd.Env 为 nil 时表示"继承父进程环境"，一旦我们显式设置 Env，子进程就只拿到我们给的
// 变量——所以必须先填空成 os.Environ()，否则子进程连 SystemRoot 都没有，Windows 上会直接启动失败。
func applyPathDirs(cmd *exec.Cmd, dirs []string) {
	if len(dirs) == 0 {
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	sep := string(os.PathListSeparator)
	prefix := strings.Join(dirs, sep)
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		// Windows 环境变量名不区分大小写，不能只比 "PATH"
		if ok && strings.EqualFold(name, "PATH") {
			out = append(out, name+"="+prefix+sep+val)
			found = true
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+prefix)
	}
	cmd.Env = out
}
