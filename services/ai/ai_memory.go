package ai

// 项目记忆：把「这个工作目录里在做什么、改过什么、踩过什么坑」沉淀到磁盘，
// 让 AI 在后续轮次/会话里不必反复重读整个项目。
//
// 两个文件（都落在 <cwd>/.quickDock/ 下）：
//
//   - MEMORY.md          整体记忆（长期）：项目是什么、技术栈、关键文件、约定、
//     决策与踩过的坑。由 AI 通过 project_memory 工具主动维护——这些是需要
//     「判断哪些值得长期记住」的事实，机器无法自动归纳，交给模型更准。
//   - memory/YYYY-MM-DD.md  每日记忆（当天活动日志）：每轮结束后由系统自动追加，
//     记录「用户要了什么、改了哪些文件、跑了几轮」。纯确定性提取，不调模型，
//     所以一定存在、且不会漏。AI 只读不写它（手动写会破坏自动日志的口径）。
//
// 为什么能省 token：原来「优化一下网页」会让模型把整个 index.html 再读一遍，
// 而且那份内容会留在对话历史里每轮重发。现在每轮开头把记忆块（整体 + 近两日）
// 注入系统提示，模型先看到「我已经建了一个单页站点、结构是这样、昨天改到哪了」，
// 大多数情况直接用 file_read 的 offset/limit 读相关片段或 file_grep 定位就够了，
// 不必整文件重读。跨会话也是同理——下次打开同一个目录，记忆还在。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/logger"
)

const memoryDirName = ".quickDock"

func memoryRoot(cwd string) string { return filepath.Join(cwd, memoryDirName) }
func overallMemoryPath(cwd string) string {
	return filepath.Join(memoryRoot(cwd), "MEMORY.md")
}
func dailyMemoryPath(cwd string, day time.Time) string {
	return filepath.Join(memoryRoot(cwd), "memory", day.Format("2006-01-02")+".md")
}

// memoryCapOverall / memoryCapDaily 注入时各自的上限（字符），防止记忆本身撑爆上下文。
const (
	memoryCapOverall = 6000
	memoryCapDaily   = 2400
)

// readProjectMemory 读取整体记忆 + 今日与昨日每日记忆，拼成一个紧凑的上下文块。
// 没有任何记忆时返回空串，调用方据此决定是否注入（避免无谓地污染系统提示）。
func readProjectMemory(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	if !dirExists(memoryRoot(cwd)) {
		return ""
	}
	var b strings.Builder
	if data, err := os.ReadFile(overallMemoryPath(cwd)); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			b.WriteString("【整体记忆】\n")
			b.WriteString(clipRunes(s, memoryCapOverall))
			b.WriteString("\n\n")
		}
	}
	now := time.Now()
	// 今日 + 昨日：够覆盖「这次会话之前做到哪了」，又不把一整周的日志都塞进来。
	for _, d := range []time.Time{now, now.AddDate(0, 0, -1)} {
		data, err := os.ReadFile(dailyMemoryPath(cwd, d))
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(data)); s != "" {
			b.WriteString(fmt.Sprintf("【%s 每日记忆】\n", d.Format("2006-01-02")))
			b.WriteString(clipRunes(s, memoryCapDaily))
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// appendDailyMemory 在一轮正常结束后追加一条当日活动记录。
//
// 纯确定性提取（用户诉求 + 改了哪些文件 + 工具轮次），不调模型——这是记忆的
// 可靠底座。即使 AI 从没主动写过 MEMORY.md，每日记忆也能让它下一轮知道「刚才做了什么」。
func appendDailyMemory(convID, cwd, userMsg string, files []string, rounds, tools int) {
	if strings.TrimSpace(cwd) == "" {
		return
	}
	root := memoryRoot(cwd)
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		logger.W("[ai] 创建记忆目录失败 cwd=%s: %v", cwd, err)
		return
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %s\n", now.Format("2006-01-02 15:04"))
	if q := strings.TrimSpace(userMsg); q != "" {
		b.WriteString("- 用户：")
		b.WriteString(clipRunes(q, 300))
		b.WriteString("\n")
	}
	if uniq := dedupKeepOrder(files); len(uniq) > 0 {
		b.WriteString("- 改动文件：")
		b.WriteString(strings.Join(uniq, "、"))
		b.WriteString("\n")
	}
	if rounds > 0 || tools > 0 {
		fmt.Fprintf(&b, "- 工具：%d 轮 / %d 次调用\n", rounds, tools)
	}
	f, err := os.OpenFile(dailyMemoryPath(cwd, now), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logger.W("[ai] 写每日记忆失败 cwd=%s: %v", cwd, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		logger.W("[ai] 写每日记忆失败 cwd=%s: %v", cwd, err)
	}
}

// projectMemoryGuidance 写进系统提示的指引：让模型优先用记忆与定向读取，少整文件重读。
func projectMemoryGuidance() string {
	return "\n\n[记忆与读文件指引]\n当前工作目录下有 .quickDock/ 项目记忆：MEMORY.md 是长期整体记忆，" +
		"memory/ 下是按天的活动日志（由系统自动维护，你只读不写）。MEMORY.md 已由系统在首次对话时" +
		"自动生成基线画像，你用 project_memory 工具补充与修正即可。\n" +
		"**记忆只存在 .quickDock/ 里**：用 project_memory 工具读写 MEMORY.md；" +
		"绝不要在项目根目录创建或修改 AGENTS.md / README.md / memory.md——AGENTS.md 是「项目规则文件」，" +
		"由用户在设置里维护、每轮自动注入系统提示，不是你的记忆，你去改它会污染用户的规则。" +
		"遇到值得长期记住的项目事实（技术栈、关键文件路径、约定、踩过的坑、已做的决策），用 project_memory 写入 MEMORY.md。\n" +
		"在改动一个你已经碰过的文件前，先用 file_read 的 offset/limit 只读相关片段，或用 file_grep 定位，" +
		"不要整文件重读——一整页 HTML/JS 重读一次就会吞掉大量 token，而且那份内容还会留在历史里每轮重发。"
}

// ---- 自动生成基线项目画像 ----

// memoryProfileNoiseDirs 扫描顶层时跳过这些目录：要么是依赖/构建产物（node_modules、
// dist），要么是本工具自己的数据目录（.quickDock），要么是以点开头的隐藏目录。
// 写进基线画像只会稀释有效信息，而且每次扫描都把它们数一遍毫无意义。
var memoryProfileNoiseDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".quickDock": true, "vendor": true, ".idea": true, ".vscode": true,
	"target": true, "bin": true, "obj": true, ".next": true, "out": true,
	"coverage": true, ".cache": true, "tmp": true, "venv": true,
}

// memoryProfileManifests 用清单文件名识别技术栈。顺序即识别顺序，命中即记。
var memoryProfileManifests = []struct {
	name  string
	stack string
	exact bool // true=文件名精确匹配；false=后缀匹配（如 *.csproj）
}{
	{"go.mod", "Go", true},
	{"package.json", "Node.js / TypeScript", true},
	{"Cargo.toml", "Rust", true},
	{"requirements.txt", "Python", true},
	{"pyproject.toml", "Python", true},
	{"Pipfile", "Python", true},
	{"pom.xml", "Java (Maven)", true},
	{"build.gradle", "Java (Gradle)", true},
	{"build.gradle.kts", "Java (Gradle)", true},
	{"Gemfile", "Ruby", true},
	{"composer.json", "PHP", true},
	{"pubspec.yaml", "Dart / Flutter", true},
	{".csproj", "C# (.NET)", false},
}

// ensureMemoryProfile 在没有整体记忆时，确定性扫描项目并写一份基线画像到
// <cwd>/.quickDock/MEMORY.md。这样无论用户有没有显式说「建记忆」，首次对话就能
// 把 .quickDock 建起来——而不是让模型自己在项目根目录生成 AGENTS.md 当记忆。
//
// 确定性、不调模型：画像只描述「这是什么项目、长什么样」，判断哪些是值得长期记的
// 事实交给模型（通过 project_memory 工具），避免机器归纳出噪声。
func ensureMemoryProfile(cwd string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return
	}
	if fileExists(overallMemoryPath(cwd)) {
		return
	}
	profile := scanProjectProfile(cwd)
	if strings.TrimSpace(profile) == "" {
		return
	}
	if err := writeOverallMemory(cwd, profile); err != nil {
		logger.W("[ai] 自动生成项目画像失败 cwd=%s: %v", cwd, err)
	}
}

// scanProjectProfile 扫一遍顶层结构 + 一层子目录，拼出一段结构化的基线画像。
func scanProjectProfile(cwd string) string {
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return ""
	}
	var dirs, files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if memoryProfileNoiseDirs[name] || strings.HasPrefix(name, ".") {
				continue
			}
			dirs = append(dirs, name)
		} else {
			files = append(files, name)
		}
	}
	sortStrings(dirs)
	sortStrings(files)

	// 技术栈：命中清单文件即记。
	var stacks []string
	for _, m := range memoryProfileManifests {
		if m.exact {
			for _, f := range files {
				if f == m.name {
					stacks = append(stacks, m.stack)
					break
				}
			}
		} else {
			for _, f := range files {
				if strings.HasSuffix(strings.ToLower(f), m.name) {
					stacks = append(stacks, m.stack)
					break
				}
			}
		}
	}

	// 文件类型计数：顶层 + 一层子目录，限量避免扫进 node_modules 这类巨目录。
	extCount := map[string]int{}
	countExt := func(p string) {
		info, e2 := os.Stat(p)
		if e2 != nil || info.IsDir() {
			return
		}
		if ext := strings.ToLower(filepath.Ext(p)); ext != "" {
			extCount[ext]++
		}
	}
	for _, f := range files {
		countExt(filepath.Join(cwd, f))
	}
	capDirs := dirs
	if len(capDirs) > 12 {
		capDirs = capDirs[:12]
	}
	for _, d := range capDirs {
		sub, e3 := os.ReadDir(filepath.Join(cwd, d))
		if e3 != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() {
				continue
			}
			countExt(filepath.Join(cwd, d, se.Name()))
		}
	}
	exts := make([]string, 0, len(extCount))
	for k := range extCount {
		exts = append(exts, k)
	}
	sortStrings(exts)
	if len(exts) > 10 {
		exts = exts[:10]
	}
	var extParts []string
	for _, e := range exts {
		extParts = append(extParts, e+" ×"+itoa(extCount[e]))
	}

	var b strings.Builder
	b.WriteString("# 项目画像（自动生成的基线）\n\n")
	b.WriteString("> 由 AI 在首次对话时自动扫描生成，可用 `project_memory` 工具补充与修正。\n")
	b.WriteString("> 项目规则（约定、禁令）写在 AGENTS.md / .quickdock/rules 里，由用户在设置里维护、每轮自动注入，不要写在这里。\n\n")
	if len(stacks) > 0 {
		b.WriteString("## 技术栈\n- " + strings.Join(stacks, "\n- ") + "\n\n")
	}
	if len(dirs) > 0 {
		show := dirs
		if len(show) > 16 {
			show = show[:16]
		}
		b.WriteString("## 顶层目录\n- " + strings.Join(show, "\n- ") + "\n\n")
	}
	if len(extParts) > 0 {
		b.WriteString("## 文件概览\n- " + strings.Join(extParts, "、") + "\n\n")
	}
	b.WriteString("## 备注\n- 本文件是长期整体记忆：技术栈、关键文件、约定、踩过的坑、已做的决策都记在这里，让后续轮次不必重新探索整个项目。\n")
	return b.String()
}

// writeOverallMemory 写（覆盖）<cwd>/.quickDock/MEMORY.md。
func writeOverallMemory(cwd, content string) error {
	p := overallMemoryPath(cwd)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func sortStrings(s []string) { sort.Strings(s) }

func itoa(n int) string { return strconv.Itoa(n) }

// ---- 小工具 ----

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// dedupKeepOrder 去重但保留首次出现顺序——每日记忆里同一个文件可能在一轮里被多次触及。
func dedupKeepOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
