package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AIPrompt 提示词库里的一条模板。
//
// 与技能（services/ai/skills.go 的 SKILL.md）不是一回事，别合并：
//   - 技能 = 给模型读的操作手册，跟着磁盘目录走，装 = 拷目录；
//   - 提示词 = 用户自己的话术，跟着库走（受备份/恢复保护），要能在界面上增删改、
//     要能带变量（{{date}} / {{selection}}），点一下把正文填进输入框。
type AIPrompt struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Category  string `json:"category"`
	Sort      int    `json:"sort"`
	Builtin   int    `json:"builtin"`
	SourceKey string `json:"sourceKey"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

const promptCols = `id, name, content, category, sort, builtin, source_key, created_at, updated_at`

func mapToPrompt(m map[string]interface{}) AIPrompt {
	return AIPrompt{
		ID:        str(m["id"]),
		Name:      str(m["name"]),
		Content:   str(m["content"]),
		Category:  str(m["category"]),
		Sort:      integer(m["sort"]),
		Builtin:   integer(m["builtin"]),
		SourceKey: str(m["source_key"]),
		CreatedAt: str(m["created_at"]),
		UpdatedAt: str(m["updated_at"]),
	}
}

// ListAIPrompts 列出全部提示词（分类 → 排序 → 名称）。
//
// 不做分页：这类数据是「自己攒的十几条」，翻页只会多一层点击。
func (d *Database) ListAIPrompts() ([]AIPrompt, error) {
	rows, err := d.Query(`SELECT ` + promptCols + ` FROM ai_prompts ORDER BY sort ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToPrompt), nil
}

// GetAIPrompt 按 id 取一条；不存在返回 (nil, nil)。
func (d *Database) GetAIPrompt(id string) (*AIPrompt, error) {
	row, err := d.QueryOne(`SELECT `+promptCols+` FROM ai_prompts WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	p := mapToPrompt(row)
	return &p, nil
}

// SaveAIPrompt 新增或更新一条提示词（id 为空即新增）。
//
// builtin / source_key 不在这里改：内置条目的身份由 source_key 决定，
// 用户能改的只有名称、正文、分类、排序（「内置」只影响能不能删）。
func (d *Database) SaveAIPrompt(p *AIPrompt) (*AIPrompt, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Content = strings.TrimSpace(p.Content)
	p.Category = strings.TrimSpace(p.Category)
	if p.Name == "" {
		return nil, fmt.Errorf("提示词名称不能为空")
	}
	if p.Content == "" {
		return nil, fmt.Errorf("提示词内容不能为空")
	}
	ts := now()

	if p.ID == "" {
		p.ID = newID()
		p.CreatedAt = ts
		p.UpdatedAt = ts
		if err := d.ExecuteParams(
			`INSERT INTO ai_prompts (`+promptCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{p.ID, p.Name, p.Content, p.Category, p.Sort, p.Builtin, p.SourceKey, p.CreatedAt, p.UpdatedAt},
		); err != nil {
			return nil, err
		}
		return p, nil
	}

	cur, err := d.GetAIPrompt(p.ID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("提示词不存在")
	}
	p.Builtin = cur.Builtin
	p.SourceKey = cur.SourceKey
	p.CreatedAt = cur.CreatedAt
	p.UpdatedAt = ts
	if err := d.ExecuteParams(
		`UPDATE ai_prompts SET name = ?, content = ?, category = ?, sort = ?, updated_at = ? WHERE id = ?`,
		[]interface{}{p.Name, p.Content, p.Category, p.Sort, ts, p.ID},
	); err != nil {
		return nil, err
	}
	return p, nil
}

// DeleteAIPrompt 删除一条提示词。内置条目不允许删——它们的文案会随版本更新，
// 删了下次启动又会回来，用户会以为删除没生效；想少看几条就用「隐藏」，不要用删。
func (d *Database) DeleteAIPrompt(id string) error {
	cur, err := d.GetAIPrompt(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	if cur.Builtin != 0 {
		return fmt.Errorf("内置提示词不能删除，可以复制一份再改")
	}
	return d.ExecuteParams(`DELETE FROM ai_prompts WHERE id = ?`, []interface{}{id})
}

// builtinAIPrompts 随版本发布的内置提示词。
//
// source_key 一旦发布就不能改：升级时按它判断「这条是不是已经有了」，
// 改名会让老用户多出一份内容相同的条目。
// 正文里的 {{selection}} / {{date}} / {{time}} 由前端在插入时展开，
// 这里存原文——提前展开会把用户上次复制的东西固化进模板。
var builtinAIPrompts = []struct {
	Key      string
	Name     string
	Category string
	Content  string
}{
	{
		"code-review", "代码审查", "开发",
		"请审查下面这段代码，按严重程度排序指出：潜在 bug、边界条件、错误处理、性能与可读性问题。每处给出「为什么是问题」和最小改法，不要重写整段。\n\n```\n{{selection}}\n```",
	},
	{
		"explain-error", "解释报错", "开发",
		"下面是一段报错。请说明：1) 直接原因；2) 根本原因；3) 最小改法；4) 以后怎么避免。不要贴整段修复代码，只给要改的部分。\n\n```\n{{selection}}\n```",
	},
	{
		"write-tests", "补单元测试", "开发",
		"为下面的代码补单元测试：覆盖正常路径、边界值和错误分支，用表驱动写法，只输出测试代码。\n\n{{selection}}",
	},
	{
		"refactor", "重构建议", "开发",
		"下面这段代码能怎么改得更清楚？请给 2-3 个具体方案，说明各自代价，并指出哪一个改动最小、收益最高。\n\n{{selection}}",
	},
	{
		"commit-msg", "写提交信息", "开发",
		"根据当前改动生成一条 Conventional Commits 提交信息：首行「类型(范围): 摘要」，不超过 50 字；改动涉及多个不相干的点时补 1-3 条要点。只输出提交信息本身。",
	},
	{
		"sql-explain", "解释这段 SQL", "开发",
		"解释下面这段 SQL 做了什么，按执行顺序说明每一段的作用；再指出可能的性能问题（索引、隐式转换、全表扫描）与改写建议。\n\n```sql\n{{selection}}\n```",
	},
	{
		"translate-en", "翻译成英文", "写作",
		"把下面的中文翻译成自然、简洁的英文。代码、变量名、专有名词保持原样，只输出译文。\n\n{{selection}}",
	},
	{
		"polish", "润色", "写作",
		"把下面这段文字改得更清楚、更简洁，保留原意与语气，只输出改写后的版本，不要解释改了什么。\n\n{{selection}}",
	},
	{
		"summarize", "摘要", "写作",
		"用不超过 5 条要点概括下面的内容，每条一句话，不加评论、不加结论。\n\n{{selection}}",
	},
	{
		"shorten", "精简到一半", "写作",
		"把下面的内容压缩到原来一半左右的长度，保留全部关键信息与数据，删掉重复和铺垫。\n\n{{selection}}",
	},
	{
		"plan-task", "拆任务", "日常",
		"把下面这件事拆成可执行的步骤清单：每步一个动作、标明产出物和验收标准，按依赖顺序排。今天是 {{date}}。\n\n",
	},
	{
		"meeting-notes", "整理会议记录", "日常",
		"把下面的会议记录整理成：结论 / 待办（谁、做什么、什么时候）/ 悬而未决的问题。原文没有提到的不要编。\n\n{{selection}}",
	},
	{
		"explain-code", "讲清这段代码", "日常",
		"用中文讲清下面这段代码在做什么：先一句话说目的，再按调用顺序讲关键步骤，最后指出读的时候容易卡住的地方。假设读者会这门语言，但没看过这个项目。\n\n{{selection}}",
	},
}

// seedAIPrompts 幂等写入内置提示词。
//
// 只补缺、不覆盖：用户可能把内置条目改成了自己的版本（比如换了语气），
// 每次启动刷回原文等于把用户的修改静默吃掉。所以「已经存在就跳过」，
// 代价是改文案后老用户看不到更新——这个代价比删改用户内容小。
//
// ⚠️ 调用方（migrate）已经持有写锁，这里必须直接用 d.conn：
// 再走 d.QueryOne / d.ExecuteParams 会二次加锁，RWMutex 不可重入，启动直接死锁。
func (d *Database) seedAIPrompts() error {
	for i, p := range builtinAIPrompts {
		var exist string
		err := d.conn.QueryRow(`SELECT id FROM ai_prompts WHERE source_key = ?`, p.Key).Scan(&exist)
		switch {
		case err == nil:
			continue // 已有：保留用户可能改过的内容
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		ts := now()
		if _, err := d.conn.Exec(
			`INSERT INTO ai_prompts (`+promptCols+`) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			newID(), p.Name, p.Content, p.Category, (i+1)*10, p.Key, ts, ts,
		); err != nil {
			return err
		}
	}
	return nil
}
