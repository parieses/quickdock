package aitools

// 项目记忆工具：让内置 AI 主动读写 <cwd>/.quickDock 下的记忆，
// 而不是每次都重读整个项目。详见 services/ai/ai_memory.go 的包注释。
//
// 分工：MEMORY.md（整体记忆）由 AI 通过本工具维护——需要「判断哪些值得长期记住」
// 的事实机器归纳不了；memory/ 每日日志由系统自动追加，本工具只让 AI 读、不让它写，
// 否则会把自动日志的口径搞乱。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mcpsrv "quickdock/internal/mcp"
)

func registerMemory(d Deps) {
	r := registrar{d: d, category: mcpsrv.CategoryCode}
	str := mcpsrv.Str
	r.add(projectMemory, mcpsrv.LevelRead, "project_memory",
		"读写当前工作目录下的 .quickDock 项目记忆，避免反复重读整个项目而浪费 token。"+
			"action=read：返回整体记忆(MEMORY.md)与最近几天的每日活动日志，让你快速了解项目现状与已做的事；"+
			"action=write：用给定 content 整体覆盖 MEMORY.md；action=append：向 MEMORY.md 追加一段。"+
			"每日活动日志由系统自动维护，不要手动写它（会破坏自动日志口径）。只允许工作目录内的 .quickDock。",
		[]string{"action"},
		map[string]any{
			"action":  str("read=读取记忆；write=整体覆盖 MEMORY.md；append=向 MEMORY.md 追加"),
			"content": str("write/append 时提供的文本内容"),
		})
}

func projectMemory(d Deps, args map[string]any) (any, error) {
	cwd := strings.TrimSpace(mcpsrv.Cwd(args))
	if cwd == "" {
		return nil, errors.New("当前对话没有绑定工作目录，无法读写项目记忆；请先把任务建在有效的空间里")
	}
	rootAbs, err := filepath.Abs(filepath.Join(cwd, ".quickDock"))
	if err != nil {
		return nil, errors.New("路径解析失败：" + cwd)
	}
	overall := filepath.Join(rootAbs, "MEMORY.md")

	action := strings.TrimSpace(mcpsrv.Arg(args, "action"))
	switch action {
	case "read", "":
		var b strings.Builder
		if data, rerr := os.ReadFile(overall); rerr == nil {
			b.WriteString("# 整体记忆 (MEMORY.md)\n\n")
			b.WriteString(string(data))
			b.WriteString("\n\n")
		} else {
			b.WriteString("# 整体记忆 (MEMORY.md)\n\n（还没有整体记忆，用 action=write 或 append 创建）\n\n")
		}
		b.WriteString("# 近期每日记忆\n\n")
		for _, p := range recentMemoryDailyPaths(rootAbs, 3) {
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				continue
			}
			nm := strings.TrimSuffix(filepath.Base(p), ".md")
			fmt.Fprintf(&b, "## %s\n%s\n\n", nm, string(data))
		}
		return map[string]any{
			"path":    overall,
			"content": b.String(),
			"tip":     "每日活动日志由系统自动维护，你只需维护 MEMORY.md 的长期事实",
		}, nil

	case "write":
		content := mcpsrv.Arg(args, "content")
		if strings.TrimSpace(content) == "" {
			return nil, errors.New("write 需要提供 content（要写入 MEMORY.md 的内容）")
		}
		if err := writeMemoryFile(overall, content); err != nil {
			return nil, err
		}
		return map[string]any{"path": overall, "changed": true, "bytes": len(content), "note": "已整体覆盖 MEMORY.md"}, nil

	case "append":
		content := strings.TrimSpace(mcpsrv.Arg(args, "content"))
		if content == "" {
			return nil, errors.New("append 需要提供 content（要追加到 MEMORY.md 的内容）")
		}
		if err := appendMemoryFile(overall, "\n"+content); err != nil {
			return nil, err
		}
		return map[string]any{"path": overall, "changed": true, "note": "已追加到 MEMORY.md"}, nil

	default:
		return nil, fmt.Errorf("未知 action=%q（支持 read / write / append）", action)
	}
}

func writeMemoryFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 .quickDock 目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 MEMORY.md 失败：%v", err)
	}
	return nil
}

func appendMemoryFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 .quickDock 目录失败：%v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开 MEMORY.md 失败：%v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("追加 MEMORY.md 失败：%v", err)
	}
	return nil
}

// recentMemoryDailyPaths 返回 .quickDock/memory 下最近的 n 个每日记忆文件（按日期倒序）。
func recentMemoryDailyPaths(rootAbs string, n int) []string {
	dir := filepath.Join(rootAbs, "memory")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > n {
		names = names[:n]
	}
	paths := make([]string, 0, len(names))
	for _, nm := range names {
		paths = append(paths, filepath.Join(dir, nm))
	}
	return paths
}
