package ai

// 技能（Skill）：数据目录下 skills/<名字>/SKILL.md 的一份「操作说明」。
//
// 与插件、外部 MCP 的分工，别混在一起：
//   - 插件 = 宿主能执行的动作（命令、iframe 页面）；
//   - 外部 MCP = 别人的工具，进程外、按需连接；
//   - 技能 = 纯文本的操作手册，AI 读完照着做，不新增任何执行能力。
//
// 所以技能的安装就是「把一个目录拷进 skills/」——不需要打包、不需要签名、
// 也不会因此获得任何权限：它只是让模型知道「这类活有现成套路」。
//
// 生效范围是**会话**而不是全局：选择存在 ai_conversations.skills 里
// （「这一步要用翻译技能」是这条任务的事，不该让另一个对话也跟着变）。
// skills.json 留着表示「默认启用哪些」——没选过技能的会话、以及升级前的老会话
// 都按它走，所以这份文件仍然有意义，只是不再是唯一口径。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
)

const (
	skillDirName  = "skills"
	skillFileName = "SKILL.md"
	// skillManifestName 启用清单。只存「哪些开着」，不存技能本体信息——
	// 本体永远以磁盘上的 SKILL.md 为准，否则改了描述还得同步两份。
	skillManifestName = "skills.json"
	// skillDescMax 注入系统提示时的描述上限。描述是给模型判断「要不要用」的，
	// 一句话足够；写成小作文会挤掉真正的内容。
	skillDescMax = 300
	// skillCountMax 单次注入的技能数上限。再多模型也挑不过来，只会稀释注意力。
	skillCountMax = 24
)

// aiSkill 一个技能（前端 + 注入共用）。
type aiSkill struct {
	Name string `json:"name"`
	// Description 一句话说明「什么时候该用它」，来自 front-matter。
	Description string `json:"description"`
	// Path SKILL.md 的绝对路径：AI 要按这个路径去读全文。
	Path string `json:"path"`
	Dir  string `json:"dir"`
	// Enabled 是否在 skills.json 里被标为「默认启用」。
	// 注意它不是「这个技能此刻生效」：生效范围是会话（见文件头），
	// 这个字段只决定「没选过技能的对话默认带哪些」。
	Enabled bool `json:"enabled"`
	// Error 非空表示这个目录认得出来但读不出内容（缺 front-matter 之类）。
	Error string `json:"error,omitempty"`
	// AllowedTools `allowed-tools` 声明（WP10）：这个技能只该用哪些工具。
	//
	// 空表示不限制。**只收不放**：它只能在一轮对话里收窄工具面，永远
	// 不能把用户已经关掉的工具重新打开（见 ToolFilter.AllowNames）。
	AllowedTools []string `json:"allowedTools,omitempty"`
}

// skillsDataDir QuickDock 数据目录（skills/ 与 skills.json 都在它下面）。
func skillsDataDir() string {
	dir, _ := platform.DataDirInfo()
	return dir
}

// skillsRoot 技能根目录（不存在时创建：安装方式就是「往里拷目录」，
// 入口必须能真的打开一个存在的文件夹）。
func skillsRoot() string {
	root := filepath.Join(skillsDataDir(), skillDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		logger.W("[skills] 创建技能目录失败 %s: %v", root, err)
	}
	return root
}

// readSkillManifest 读启用清单。文件不存在/损坏一律当「全关」，
// 不报错也不覆盖——配置读坏了还能修，被程序清空就找不回来了。
func readSkillManifest() map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(skillsDataDir(), skillManifestName))
	if err != nil {
		if !os.IsNotExist(err) {
			logger.W("[skills] 读启用清单失败: %v", err)
		}
		return out
	}
	var doc struct {
		Enabled []string `json:"enabled"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		logger.W("[skills] 启用清单解析失败（按全关处理）: %v", err)
		return out
	}
	for _, n := range doc.Enabled {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out
}

func writeSkillManifest(enabled map[string]bool) error {
	names := make([]string, 0, len(enabled))
	for n, on := range enabled {
		if on {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	doc := struct {
		Enabled []string `json:"enabled"`
	}{Enabled: names}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(skillsDataDir(), skillManifestName)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}

// parseSkillMeta 从 SKILL.md 抽 name / description。
//
// 只认 front-matter 里的单行 `key: value`：技能是给人写给模型看的，
// 不是配置文件；真需要多行描述的用 YAML 块语法反而让人写不对。
// 取不到 name 时回落到目录名——目录名本来就是作者起的那一个。
func parseSkillMeta(content, dirName string) (name, desc string, allowed []string) {
	name, desc = dirName, ""
	body := content
	if strings.HasPrefix(strings.TrimLeft(content, " \t\r\n"), "---") {
		s := strings.TrimLeft(content, " \t\r\n")
		s = s[3:]
		if i := strings.Index(s, "\n---"); i >= 0 {
			for _, line := range strings.Split(s[:i], "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "name":
					if v != "" {
						name = v
					}
				case "description":
					desc = v
				case "allowed-tools", "allowed_tools", "allowedtools":
					allowed = parseAllowedTools(v)
				}
			}
			body = strings.TrimSpace(s[i+4:])
		}
	}
	if desc == "" {
		// 没有 front-matter 的 SKILL.md 也很常见（手写的）：退回正文第一行，
		// 总比在 UI 上显示一个空描述强。
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(strings.TrimLeft(line, "#"))
			if line == "" || strings.HasPrefix(line, "<!--") {
				continue
			}
			desc = line
			break
		}
	}
	if r := []rune(desc); len(r) > skillDescMax {
		desc = string(r[:skillDescMax]) + "…"
	}
	return name, desc, allowed
}

// parseAllowedTools 解析 `allowed-tools:` 的取值。
//
// 支持三种常见写法（都按「分隔符切成名字」处理）：
//
//	allowed-tools: file_read, file_grep
//	allowed-tools: [file_read, file_grep]
//	allowed-tools: file_read file_grep
//
// 只保留形如工具名的 token：出现路径、参数、shell 片段一律丢掉——那是写坏了
// 的声明，猜它的意图不如忽略它（工具面不变，至少不会误收窄到「一个都不剩」）。
func parseAllowedTools(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == ';'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		name := strings.Trim(strings.TrimSpace(f), `"'`)
		if name == "" || seen[name] {
			continue
		}
		// 工具名只允许字母/数字/下划线/连字符：带别的字符说明这一项不是工具名。
		if !validToolName(name) {
			logger.W("[skills] 忽略非法的 allowed-tools 项 %q", f)
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// validToolName 工具名允许的字符集。
//
// 必须以字母开头：这一条挡的是 `--force`、`-rf` 这类参数被当成工具名——
// 它们在对空格切分时和工具名长得一样，而混进白名单会让「按名字收窄」
// 少筛掉一个真工具（更糟的是它永远匹配不上任何工具，白名单就成了摆设）。
func validToolName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// skillAllowNames 决定这次对话要不要按技能声明收窄工具面，返回白名单。
//
// 规则（保守优先）：
//   - 只在**明确选中了恰好一个技能**时收窄。多个技能同时生效时各自声明的是
//     「我需要什么」，取交集会让两个技能互相废掉，取并集等于没有限制——
//     两种都不对，所以这一档干脆不做；
//   - 全局默认（没选过技能）不收窄：那种情况下用户根本没表达「只要这些」；
//   - 该技能没声明 allowed-tools 时也不收窄。
func skillAllowNames(raw string) (map[string]bool, string) {
	picked, global := convSkillList(raw)
	if global || len(picked) != 1 {
		return nil, ""
	}
	tools := picked[0].AllowedTools
	if len(tools) == 0 {
		return nil, ""
	}
	allow := make(map[string]bool, len(tools))
	for _, t := range tools {
		allow[t] = true
	}
	return allow, picked[0].Name
}

// scanSkills 扫一遍技能目录（按名字排序）。单个技能坏掉只标记它自己，
// 不影响其它技能——一个写错的 SKILL.md 不该让整个技能列表消失。
func scanSkills() []aiSkill {
	root := skillsRoot()
	enabled := readSkillManifest()
	entries, err := os.ReadDir(root)
	if err != nil {
		return []aiSkill{}
	}
	out := make([]aiSkill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		path := filepath.Join(dir, skillFileName)
		sk := aiSkill{Name: e.Name(), Dir: dir, Path: path}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			sk.Error = "缺少 SKILL.md"
			out = append(out, sk)
			continue
		}
		sk.Name, sk.Description, sk.AllowedTools = parseSkillMeta(string(data), e.Name())
		sk.Enabled = enabled[sk.Name] || enabled[e.Name()]
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// 技能注入块。
//
// 只注入「名字 + 一句话说明 + 文件路径」，正文按需读取：一次对话可能挂十几个
// 技能，把每份 SKILL.md 全文塞进系统提示，等于用几万字的目录换一次可能用不上
// 的机会。这也让技能可以写得多长都行。
//
// global：这批是从 skills.json 的全局开关来的（老会话 / 没选过技能的情形），
// 提示里要交代清楚「不是本次对话选的」，否则模型会以为用户刚点过它们。
func renderSkillsPrompt(picked []aiSkill, global bool) string {
	if len(picked) == 0 {
		return ""
	}
	if len(picked) > skillCountMax {
		picked = picked[:skillCountMax]
	}
	var b strings.Builder
	b.WriteString("\n\n[技能]")
	if global {
		b.WriteString("\n用户在本机启用了下列技能（全局默认，与本次对话是否相关无关）。")
	} else {
		b.WriteString("\n用户为本次对话选定了下列技能——这表明他期望用这些套路完成当前任务。")
	}
	b.WriteString("它们是操作说明，不是工具：")
	b.WriteString("\n- 先用 file_read 读 <路径> 再照做，不要凭技能名猜内容；")
	b.WriteString("\n- 只有在当前任务确实属于该技能的适用场景时才读，避免无谓的上下文开销；")
	b.WriteString("\n- 读到的步骤与用户的直接要求冲突时，以用户的直接要求为准。")
	for _, sk := range picked {
		b.WriteString("\n- ")
		b.WriteString(sk.Name)
		if sk.Description != "" {
			b.WriteString("：")
			b.WriteString(sk.Description)
		}
		b.WriteString("（")
		b.WriteString(sk.Path)
		b.WriteString("）")
	}
	return b.String()
}

// enabledSkillList skills.json 里全局启用、且磁盘上读得出来的技能。
func enabledSkillList() []aiSkill {
	all := scanSkills()
	picked := make([]aiSkill, 0, len(all))
	for _, sk := range all {
		if sk.Enabled && sk.Error == "" {
			picked = append(picked, sk)
		}
	}
	return picked
}

// encodeConvSkills 把技能名列表编成 ai_conversations.skills 的值。
//
// nil → 空串（「这次没表态」，库里保留原值）；空切片 → "[]"（明确的「不用技能」）。
// 不能一律 Marshal：nil 编出来是 "null"，会和「从未设置」混成同一个语义，
// 于是用户把 /技能名 全删掉后，下一轮又把全局默认挂回来了。
func encodeConvSkills(names []string) string {
	if names == nil {
		return ""
	}
	clean := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n == "" || seen[n] {
			continue
		}
		seen[n] = true
		clean = append(clean, n)
	}
	data, err := json.Marshal(clean)
	if err != nil {
		logger.W("[skills] 编码技能选择失败: %v", err)
		return ""
	}
	return string(data)
}

// convSkillList 按会话选择解析出本次生效的技能。raw 是 ai_conversations.skills 列：
//
//   - 空串：这条对话从未选过技能 → 回落到全局启用（老会话因此保持升级前的行为）；
//   - `[]`：用户明确「这条不用技能」→ 空列表（与「没选过」必须区分开，
//     否则「把 /名字 删掉」这件事根本没有落点）；
//   - 有内容：按名字挑；磁盘上已删掉的技能自动忽略，不报错——
//     技能目录是用户可以随手改的地方，为它拦下整轮对话不值得。
//
// global 表示这批是从全局开关来的，提示词里要交代清楚「不是本次对话选的」。
func convSkillList(raw string) (picked []aiSkill, global bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return enabledSkillList(), true
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		logger.W("[skills] 会话技能列解析失败，按全局默认处理: %v", err)
		return enabledSkillList(), true
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return nil, false
	}
	list := make([]aiSkill, 0, len(want))
	for _, sk := range scanSkills() {
		if want[sk.Name] && sk.Error == "" {
			list = append(list, sk)
		}
	}
	return list, false
}

// convSkillsPrompt 按会话选择组装技能注入块。
func convSkillsPrompt(raw string) string {
	picked, global := convSkillList(raw)
	return renderSkillsPrompt(picked, global)
}

// convSkillNamesJSON 本次真正挂上的技能名（JSON 数组），空 = 没有技能。
//
// 这份值会落到 ai_messages.skills 上，用来在聊天里标出「这条用到了哪个技能」。
// 在此之前技能只存在于提示词里：模型读了 SKILL.md 也只是一条普通的工具行，
// 用户看不出「我选的技能到底生效了没有」。
func convSkillNamesJSON(raw string) string {
	picked, _ := convSkillList(raw)
	if len(picked) == 0 {
		return ""
	}
	if len(picked) > skillCountMax {
		picked = picked[:skillCountMax]
	}
	names := make([]string, 0, len(picked))
	for _, sk := range picked {
		names = append(names, sk.Name)
	}
	data, err := json.Marshal(names)
	if err != nil {
		logger.W("[skills] 编码消息技能名失败: %v", err)
		return ""
	}
	return string(data)
}

// -------- bound 方法 --------

// AIGetSkills 列出技能与启用状态（bound）。
//
// 顺带把根目录一起回给前端：安装入口就是「打开这个文件夹往里拷」，
// 前端拿路径直接调 RevealInExplorer，不必再加一个专门的 bound 方法。
func (a *AIService) AIGetSkills() *services.ApiResult {
	root := skillsRoot()
	return services.Ok(map[string]any{
		"dir":    root,
		"skills": scanSkills(),
	})
}

// AISetSkillEnabled 改「默认启用」清单（bound）。
//
// 注意这不是「这条消息要不要用」——那是输入框里的 /名字（会话选中记在
// ai_conversations.skills）。这里改的是 skills.json，只影响「没表态的会话」。
// 界面上暂时没有入口，留给设置页；口径写清楚免得日后被当成技能总开关。
//
// 按「当前磁盘上的名字」写清单：技能改名后旧名字留在清单里是脏数据，
// 这里用扫描结果做一次归一，只保留确实存在的技能。
func (a *AIService) AISetSkillEnabled(name string, enabled bool) *services.ApiResult {
	want := strings.TrimSpace(name)
	if want == "" {
		return services.Fail(fmt.Errorf("缺少参数 name"))
	}
	all := scanSkills()
	found := false
	for _, sk := range all {
		if sk.Name == want || sk.Dir == want {
			found = true
			break
		}
	}
	if !found {
		return services.FailMsg(fmt.Sprintf("技能 %s 不存在（可能目录已被删除）", want))
	}
	enabledSet := map[string]bool{}
	for _, sk := range all {
		if sk.Error != "" {
			continue
		}
		if sk.Name == want {
			enabledSet[sk.Name] = enabled
			continue
		}
		if sk.Enabled {
			enabledSet[sk.Name] = true
		}
	}
	if err := writeSkillManifest(enabledSet); err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]any{
		"dir":    skillsRoot(),
		"skills": scanSkills(),
	})
}
