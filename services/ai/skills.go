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
func parseSkillMeta(content, dirName string) (name, desc string) {
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
	return name, desc
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
		sk.Name, sk.Description = parseSkillMeta(string(data), e.Name())
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

// convSkillsPrompt 按会话选择组装技能注入块。raw 是 ai_conversations.skills 列：
//
//   - 空串：这条对话从未选过技能 → 回落到全局启用（老会话因此保持升级前的行为）；
//   - `[]`：用户明确「这条不用技能」→ 不注入（与「没选过」必须区分开，
//     否则「把 /名字 删掉」这件事根本没有落点）；
//   - 有内容：按名字挑；磁盘上已删掉的技能自动忽略，不报错——
//     技能目录是用户可以随手改的地方，为它拦下整轮对话不值得。
func convSkillsPrompt(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return renderSkillsPrompt(enabledSkillList(), true)
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		logger.W("[skills] 会话技能列解析失败，按全局默认处理: %v", err)
		return renderSkillsPrompt(enabledSkillList(), true)
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return ""
	}
	picked := make([]aiSkill, 0, len(want))
	for _, sk := range scanSkills() {
		if want[sk.Name] && sk.Error == "" {
			picked = append(picked, sk)
		}
	}
	return renderSkillsPrompt(picked, false)
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
