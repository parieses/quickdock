package aitools

// 记忆工具：让内置 AI 主动读写自己那份长期记忆，而不是反复重读整个项目。
// 目录由宿主决定（project_memory 见 services/ai/ai_memory.go 的包注释）：
// 项目任务在 <cwd>/.quickdock，无空间任务在数据目录下跨任务共享的 ai_memory/。
//
// 三层结构（与 services/ai/ai_memory_doc.go 一致）：
//   - MEMORY.md              常青记忆（项目基线/硬规则/踩过的坑/项目事实/决策/分册索引）
//   - memory/YYYY-MM-DD.md   按天的工作记录（系统自动写，本工具只让 AI 读）
//   - memory/_distill/*.md   专题分册（本工具可写；MEMORY.md 的索引会自动跟上）
//
// 为什么要分册：MEMORY.md 会整份注入系统提示（上限 7000 字），塞不下「插件系统
// 三年踩坑全史」这种长文。分册不参与注入，模型需要细节时按索引去 file_read。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	mcpsrv "quickdock/internal/mcp"
)

const (
	// memoryTopicDirName 分册目录（在 memory/ 下）。
	memoryTopicDirName = "_distill"
	// memoryTopicNameRune 分册名长度上限。
	memoryTopicNameRune = 40
)

func registerMemory(d Deps) {
	r := registrar{d: d, domain: mcpsrv.CategoryCode}
	str := mcpsrv.Str
	r.add(projectMemory, mcpsrv.LevelRead, "project_memory",
		"读写当前任务所在工作区的长期记忆，避免反复重读整个项目或让用户重复交代背景而浪费 token。"+
			"action=read：返回 MEMORY.md（常青记忆）+ 最近几天的按天记录 + 分册清单；"+
			"action=write：用给定 content 整体覆盖 MEMORY.md（或覆盖某个分册）；"+
			"action=append：向 MEMORY.md（或某个分册）追加一段；"+
			"action=topics：列出 memory/_distill/ 下的分册。"+
			"memory/ 下的按天记录由系统自动维护，不要手动写它（会破坏自动日志口径）。"+
			"某个主题的细节超过十来条时，用 action=write + topic 写进 memory/_distill/NN-主题.md，"+
			"MEMORY.md 只留一条规则加一句指路——那样才塞得进每轮注入的 7000 字。"+
			"旧事实不成立时要改掉或删掉它，不要只在旁边追加一条新的。",
		[]string{"action"},
		map[string]any{
			"action":  str("read=读取记忆；write=整体覆盖；append=追加；topics=列出分册"),
			"content": str("write/append 时提供的文本内容"),
			"topic":   str("可选：分册名（如「插件系统」）；给了就写 memory/_distill/，不给就写 MEMORY.md"),
		})
}

func projectMemory(d Deps, args map[string]any) (any, error) {
	cwd := strings.TrimSpace(mcpsrv.Cwd(args))
	if cwd == "" {
		return nil, errors.New("当前对话没有绑定工作目录，无法读写记忆；请先把任务建在有效的空间里")
	}
	rootAbs := memoryRootAbs(d, cwd)
	overall := filepath.Join(rootAbs, "MEMORY.md")

	action := strings.TrimSpace(mcpsrv.Arg(args, "action"))
	topic := strings.TrimSpace(mcpsrv.Arg(args, "topic"))

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
		if topics := listMemoryTopics(rootAbs); len(topics) > 0 {
			b.WriteString("# 分册（细节长文，需要时用 file_read 打开）\n\n")
			for _, t := range topics {
				fmt.Fprintf(&b, "- %s\n", t)
			}
		}
		return map[string]any{
			"path":    overall,
			"content": b.String(),
			"tip":     "按天记录由系统自动维护；你只维护 MEMORY.md 与 memory/_distill/ 分册",
		}, nil

	case "topics":
		topics := listMemoryTopics(rootAbs)
		return map[string]any{
			"dir":    filepath.Join(rootAbs, "memory", memoryTopicDirName),
			"topics": topics,
			"count":  len(topics),
		}, nil

	case "write":
		content := mcpsrv.Arg(args, "content")
		if strings.TrimSpace(content) == "" {
			return nil, errors.New("write 需要提供 content（要写入的内容）")
		}
		target := overall
		note := "已整体覆盖 MEMORY.md"
		if topic != "" {
			p, perr := memoryTopicPath(rootAbs, topic)
			if perr != nil {
				return nil, perr
			}
			target = p
			note = "已整体覆盖分册 " + filepath.Base(p)
		}
		if err := writeMemoryFile(target, content); err != nil {
			return nil, err
		}
		if topic != "" {
			d.refreshMemoryIndex(cwd)
		}
		return map[string]any{"path": target, "changed": true, "bytes": len(content), "note": note}, nil

	case "append":
		content := strings.TrimSpace(mcpsrv.Arg(args, "content"))
		if content == "" {
			return nil, errors.New("append 需要提供 content（要追加的内容）")
		}
		target := overall
		note := "已追加到 MEMORY.md"
		if topic != "" {
			p, perr := memoryTopicPath(rootAbs, topic)
			if perr != nil {
				return nil, perr
			}
			target = p
			note = "已追加到分册 " + filepath.Base(p)
		}
		if err := appendMemoryFile(target, "\n"+content); err != nil {
			return nil, err
		}
		if topic != "" {
			d.refreshMemoryIndex(cwd)
		}
		return map[string]any{"path": target, "changed": true, "note": note}, nil

	default:
		return nil, fmt.Errorf("未知 action=%q（支持 read / write / append / topics）", action)
	}
}

// memoryRootAbs 记忆目录的绝对路径（宿主注入的判定优先）。
func memoryRootAbs(d Deps, cwd string) string {
	rootAbs := filepath.Join(cwd, quickdockDirName)
	if d.MemoryRoot != nil {
		if r := strings.TrimSpace(d.MemoryRoot(cwd)); r != "" {
			rootAbs = r
		}
	}
	if abs, aerr := filepath.Abs(rootAbs); aerr == nil {
		rootAbs = abs
	}
	return rootAbs
}

// memoryTopicMu 串行化「查重名 → 算编号 → 抢占文件」这三步。
//
// 为什么需要：spawn_agent 让多个子 agent 并行写记忆成为常态，而这三步之间
// 没有任何互斥。两个子 agent 同时写同一主题时都判定「没有旧文件」，于是各自
// 算号、写同一个路径（os.WriteFile 是 truncate，后写者覆盖前写者）；写两个
// 不同主题时又都读到 max=1，各自生成 02-A.md / 02-B.md，编号重复。
// 锁只罩这几步（目录 IO，毫秒级），不罩模型调用。
var memoryTopicMu sync.Mutex

// memoryTopicPath 算出分册文件的路径：同名复用已有的编号，新主题自动续号。
//
// 自动续号是为了 `ls memory/_distill/` 时能按创建顺序读；复用的是「去掉 NN- 前缀
// 后同名」的那份——否则模型每次 append 都会新建一个 03-插件.md、04-插件.md。
func memoryTopicPath(rootAbs, topic string) (string, error) {
	name := strings.TrimSuffix(strings.TrimSpace(topic), ".md")
	name = sanitizeTopicName(name)
	if name == "" {
		return "", fmt.Errorf("topic=%q 里没有可用字符（用中文、字母、数字、-、_）", topic)
	}
	dir := filepath.Join(rootAbs, "memory", memoryTopicDirName)

	memoryTopicMu.Lock()
	defer memoryTopicMu.Unlock()

	if existing := findTopicFile(dir, name); existing != "" {
		return filepath.Join(dir, existing), nil
	}
	if !hasTopicNumber(name) {
		name = fmt.Sprintf("%02d-%s", nextTopicNumber(dir), name)
	}
	p := filepath.Join(dir, name+".md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建记忆目录失败：%v", err)
	}
	// 编号的落点是原子抢占，不是「算完就假定没人抢」：O_EXCL 让并发的那一方
	// 立刻失败，回头再查一次就能拿到对方刚建好的那个文件。进程内已有锁，
	// 这一层防的是多实例 / 锁失效时的静默覆盖。
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		_ = f.Close()
		return p, nil
	}
	if !os.IsExist(err) {
		return "", fmt.Errorf("创建分册 %s 失败：%v", filepath.Base(p), err)
	}
	if existing := findTopicFile(dir, name); existing != "" {
		return filepath.Join(dir, existing), nil
	}
	return p, nil
}

// sanitizeTopicName 把主题名洗成安全文件名。
func sanitizeTopicName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 127: // 中日韩等非 ASCII：保留（它是可读的主题名）
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	// 连字符折叠，避免 a---b 这种名字。
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if utf8.RuneCountInString(out) > memoryTopicNameRune {
		out = string([]rune(out)[:memoryTopicNameRune])
	}
	return out
}

// 分册编号允许到 6 位：只认 1-3 位时，第 1000 册（如 "1000-go基础"）会被
// 当成「无编号主题」，findTopicFile 找不到旧分册、nextTopicNumber 也算不到它，
// 于是同一主题反复新建、编号撞车。放宽后旧的 1-3 位分册仍照常匹配。
var topicNumberRe = regexp.MustCompile(`^(\d{1,6})-(.+)$`)

func hasTopicNumber(name string) bool { return topicNumberRe.MatchString(name) }

func nextTopicNumber(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	max := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := topicNumberRe.FindStringSubmatch(strings.TrimSuffix(e.Name(), ".md"))
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

// findTopicFile 在目录里找「去掉编号前缀后同名」的分册。
func findTopicFile(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	want := strings.ToLower(name)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".md")
		if m := topicNumberRe.FindStringSubmatch(base); m != nil {
			base = m[2]
		}
		if strings.ToLower(base) == want {
			return e.Name()
		}
	}
	return ""
}

// listMemoryTopics 列出分册：路径 + 第一行标题。
func listMemoryTopics(rootAbs string) []string {
	dir := filepath.Join(rootAbs, "memory", memoryTopicDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		line := filepath.Join("memory", memoryTopicDirName, name)
		if data, rerr := os.ReadFile(filepath.Join(dir, name)); rerr == nil {
			if t := firstHeadingLine(string(data)); t != "" {
				line += " · " + t
			}
		}
		out = append(out, line)
	}
	return out
}

// firstHeadingLine 取第一行 `# 标题`。
func firstHeadingLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// refreshMemoryIndex 让 MEMORY.md 的分册索引立刻跟上（宿主注入；未注入时跳过）。
func (d Deps) refreshMemoryIndex(cwd string) {
	if d.MemoryRefresh != nil {
		d.MemoryRefresh(cwd)
	}
}

func writeMemoryFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建记忆目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败：%v", filepath.Base(path), err)
	}
	return nil
}

func appendMemoryFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建记忆目录失败：%v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开 %s 失败：%v", filepath.Base(path), err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("追加 %s 失败：%v", filepath.Base(path), err)
	}
	return nil
}

// recentMemoryDailyPaths 返回 .quickdock/memory 下最近的 n 个每日记忆文件（按日期倒序）。
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
