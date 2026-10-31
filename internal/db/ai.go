package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// AISpace AI 空间：绑定一个目录的任务容器。
//
// 与「任务（会话）」的界线：空间是目录的映射，任务是一次对话。
// 用户在一个项目下会开十几条对话，但它们共享同一个目录——把这些对话聚在一个
// 空间下，侧栏才不会变成一长串看不出归属的标题。
type AISpace struct {
	ID   string `json:"id"`
	Name string `json:"name"` // 展示名，默认取目录名
	// Cwd 空间绑定的目录（绝对路径）。空串只在用户手工创建「不带目录的空间」时出现，
	// 目前创建入口都要求选目录，所以实践中恒非空。
	Cwd       string `json:"cwd"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AIConversation AI 对话会话（界面上叫「任务」）
type AIConversation struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Summary          string `json:"summary"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	RequestCount     int    `json:"request_count"`
	Model            string `json:"model"` // 最近一次问答使用的模型名（老数据为空）
	// Cwd 这个对话绑定的工作目录（绝对路径，空 = 未绑定）。
	// 绑在会话而不是全局设置上：同时开两个项目的对话是常态，
	// 而「AI 该在哪个目录里读文件/跑命令」必须跟着对话走，不能是全局状态。
	//
	// 为空且 SpaceID 非空时回退到空间的目录——换目录改空间一处即可，
	// 不必逐条任务改。
	Cwd string `json:"cwd"`
	// SpaceID 所属空间；空 = 普通任务（侧栏的「任务」组）。
	SpaceID string `json:"space_id"`
	// Mode 创建时选定的任务类型：chat / translate / summarize / explain / tutorial。
	// 存库而不是每次从前端带：模式是这个任务的属性（「这条对话是用来翻译的」），
	// 跟着任务走才不会切走再回来就变了。
	Mode string `json:"mode"`
	// Starred 星标：侧栏置顶收藏。单独一列而不是复用 space_id（自建「收藏」空间）：
	// 星标是横切属性 —— 一个任务可以既属于某空间又被星标，塞进空间就二选一了。
	Starred   bool   `json:"starred"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AIMessage AI 对话消息
//
// Images 为 JSON 数组字符串，元素形如 {"kind":"url","url":"https://..."} 或
// {"kind":"file","path":"ai-images/xxx.png","name":"shot.png"}（path 相对数据目录）。
// 只出现在 user 消息上：assistant 回复永远是纯文本。图片字节本身不放 DB——
// file 类落盘到数据目录，发送时按需重读；url 类只存链接。
//
// 后四个字段只在 assistant 消息上有值：这一次调用的模型与用量。
// 存到消息上而不是只放 ai_usage，是因为「回答下面那行小字」要能随消息一起
// 重新加载出来 —— 前端内存里的统计一刷新就没了。
type AIMessage struct {
	ID               string `json:"id"`
	ConvID           string `json:"conv_id"`
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Images           string `json:"images"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	CachedTokens     int    `json:"cached_tokens"`
	// Stopped 这条助手回复是被用户中途打断的（内容是半截的），
	// 前端据此在气泡上标「已中断」，避免刷新后被当成完整回答。
	Stopped bool `json:"stopped"`
	// ToolCalls 这一条回复期间发生的工具调用，JSON 数组（结构由 services/ai 定义，
	// db 层不解析它——工具规格会随 MCP 面板演进，db 不该跟着改）。
	// 空串表示没调过工具；不写 "[]"，省掉绝大多数行的无意义字节。
	ToolCalls string `json:"tool_calls"`
	// ToolRounds 用了几轮工具调用（0 = 纯文本回答）。
	// 与「调用了几个工具」不同：一轮可以并发调多个，界面上「调了几轮」要看这个。
	ToolRounds int    `json:"tool_rounds"`
	CreatedAt  string `json:"created_at"`
}

func mapToAISpace(m map[string]interface{}) AISpace {
	return AISpace{
		ID:        str(m["id"]),
		Name:      str(m["name"]),
		Cwd:       str(m["cwd"]),
		CreatedAt: str(m["created_at"]),
		UpdatedAt: str(m["updated_at"]),
	}
}

func mapToAIConversation(m map[string]interface{}) AIConversation {
	c := AIConversation{
		ID:               str(m["id"]),
		Title:            str(m["title"]),
		Summary:          str(m["summary"]),
		PromptTokens:     integer(m["prompt_tokens"]),
		CompletionTokens: integer(m["completion_tokens"]),
		RequestCount:     integer(m["request_count"]),
		Model:            str(m["model"]),
		Cwd:              str(m["cwd"]),
		SpaceID:          str(m["space_id"]),
		Mode:             str(m["mode"]),
		Starred:          integer(m["starred"]) == 1,
		CreatedAt:        str(m["created_at"]),
		UpdatedAt:        str(m["updated_at"]),
	}
	// 老库没有 mode 列时（升级前的行）这里会是空串。按 chat 处理而不是新增一次
	// 全表回填：空串在后端一律走「默认 chat」，效果等价，少一次迁移风险。
	if c.Mode == "" {
		c.Mode = "chat"
	}
	return c
}

func mapToAIMessage(m map[string]interface{}) AIMessage {
	return AIMessage{
		ID:               str(m["id"]),
		ConvID:           str(m["conv_id"]),
		Role:             str(m["role"]),
		Content:          str(m["content"]),
		ReasoningContent: str(m["reasoning_content"]),
		Images:           str(m["images"]),
		Model:            str(m["model"]),
		PromptTokens:     integer(m["prompt_tokens"]),
		CompletionTokens: integer(m["completion_tokens"]),
		CachedTokens:     integer(m["cached_tokens"]),
		Stopped:          integer(m["stopped"]) != 0,
		ToolCalls:        str(m["tool_calls"]),
		ToolRounds:       integer(m["tool_rounds"]),
		CreatedAt:        str(m["created_at"]),
	}
}

// ListAIConversations 会话列表（按更新时间倒序）
func (d *Database) ListAIConversations() ([]AIConversation, error) {
	rows, err := d.ListTableWhere("ai_conversations", "1=1 ORDER BY updated_at DESC, created_at DESC")
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAIConversation), nil
}

// SetAIConversationStar 设置/取消星标。
//
// 单独一个方法而不是复用 UpdateAIConversationMeta：后者「空串 = 不改」，
// 而星标是布尔量 —— 用空串/非空串表达会是另一种编码，两者混在一个签名里迟早出错。
func (d *Database) SetAIConversationStar(id string, starred bool) error {
	v := 0
	if starred {
		v = 1
	}
	// 不更新 updated_at：星标是「我怎么看这条任务」，不是对话内容变了。
	// 跟着刷新会让侧栏按更新时间排序时，取消星标的一条突然跳到最前面。
	return d.ExecuteParams(
		`UPDATE ai_conversations SET starred = ? WHERE id = ?`,
		[]interface{}{v, strings.TrimSpace(id)},
	)
}

// ---- 空间 ----

// ListAISpaces 全部空间（按更新时间倒序）
func (d *Database) ListAISpaces() ([]AISpace, error) {
	rows, err := d.ListTableWhere("ai_spaces", "1=1 ORDER BY updated_at DESC, created_at DESC")
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAISpace), nil
}

// GetAISpace 读取单个空间
func (d *Database) GetAISpace(id string) (*AISpace, error) {
	row, err := d.QueryOne("SELECT * FROM ai_spaces WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("空间不存在")
	}
	s := mapToAISpace(row)
	return &s, nil
}

// FindAISpaceByCwd 按目录找空间（同一目录不该建出两个空间）。
// 目录比较前统一 Clean + 小写（Windows 不区分大小写），否则「D:\pp\quickdock」
// 与「d:/pp/quickdock」会被判成两个目录，用户每换一种写法就多一个空空间。
func (d *Database) FindAISpaceByCwd(cwd string) (*AISpace, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil, nil
	}
	list, err := d.ListAISpaces()
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(filepath.Clean(cwd))
	for i := range list {
		if strings.ToLower(filepath.Clean(list[i].Cwd)) == want {
			s := list[i]
			return &s, nil
		}
	}
	return nil, nil
}

// CreateAISpace 新建空间（cwd 必填：没有目录的空间只是个文件夹分组，
// 那是 notes 的活，不该在这里重复造）。
func (d *Database) CreateAISpace(name, cwd string) (*AISpace, error) {
	s := &AISpace{
		ID:        newID(),
		Name:      strings.TrimSpace(name),
		Cwd:       strings.TrimSpace(cwd),
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	if s.Name == "" {
		s.Name = filepath.Base(s.Cwd)
	}
	if err := d.BulkInsert("ai_spaces", []map[string]interface{}{structToMap(s)}); err != nil {
		return nil, err
	}
	return s, nil
}

// UpdateAISpaceMeta 改空间名 / 绑定目录（空字段表示不更新）。
//
// 与会话的 UpdateAIConversationMeta 同构：刻意保持一致的「空串 = 不改」约定，
// 两个入口各写一套语义是最容易记反的那种不一致。
func (d *Database) UpdateAISpaceMeta(id, name, cwd string) error {
	sets := []string{}
	args := []interface{}{}
	if name != "" {
		sets = append(sets, "name = ?")
		args = append(args, name)
	}
	if cwd != "" {
		sets = append(sets, "cwd = ?")
		args = append(args, cwd)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now(), id)
	return d.ExecuteParams("UPDATE ai_spaces SET "+strings.Join(sets, ", ")+" WHERE id = ?", args)
}

// ListAIConversationsInSpace 某空间下的全部任务
func (d *Database) ListAIConversationsInSpace(spaceID string) ([]AIConversation, error) {
	rows, err := d.ListTableWhere("ai_conversations", "space_id = ? ORDER BY updated_at DESC", spaceID)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAIConversation), nil
}

// ---- 任务（会话）----

// NewAIConversation 新建任务的参数。
//
// 用结构体而不是继续往 CreateAIConversation 上加位置参数：加上 mode/spaceID/cwd
// 之后已经有四个 string 了，调用点会变成一长串难以核对的实参，而它们的类型
// 全都相同 —— 传错顺序编译器一声不响，出错的表现只是「这条对话的模式不对」。
type NewAIConversation struct {
	Title   string
	Mode    string // 空 = chat
	SpaceID string // 空 = 普通任务
	Cwd     string // 空 = 未绑定（空间任务会回退到空间目录）
}

// CreateAIConversation 新建任务
func (d *Database) CreateAIConversation(in NewAIConversation) (*AIConversation, error) {
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "chat"
	}
	c := &AIConversation{
		ID:        newID(),
		Title:     strings.TrimSpace(in.Title),
		Mode:      mode,
		SpaceID:   strings.TrimSpace(in.SpaceID),
		Cwd:       strings.TrimSpace(in.Cwd),
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	if err := d.BulkInsert("ai_conversations", []map[string]interface{}{structToMap(c)}); err != nil {
		return nil, err
	}
	return c, nil
}

// SetAIConversationMode 改任务的模式（创建后仍允许改：用户可能开错了类型）。
func (d *Database) SetAIConversationMode(id, mode string) error {
	if strings.TrimSpace(mode) == "" {
		mode = "chat"
	}
	return d.ExecuteParams(
		`UPDATE ai_conversations SET mode = ?, updated_at = ? WHERE id = ?`,
		[]interface{}{strings.TrimSpace(mode), now(), strings.TrimSpace(id)},
	)
}

// GetAIConversation 读取单个会话
func (d *Database) GetAIConversation(id string) (*AIConversation, error) {
	row, err := d.QueryOne("SELECT * FROM ai_conversations WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("会话不存在")
	}
	c := mapToAIConversation(row)
	return &c, nil
}

// DeleteAIConversation 硬删除（不进回收站）。
//
// 只给「系统自己发起的清理」用（例如恢复回收站时要先清掉同名残留）。
// 用户主动删除一律走 DeleteAIConversationToRecycle —— 见 tombstone.go 的动机说明。
func (d *Database) DeleteAIConversation(id string) error {
	if err := d.ExecuteParams("DELETE FROM ai_messages WHERE conv_id = ?", []interface{}{id}); err != nil {
		return err
	}
	// 编排运行轨迹随会话一起删（节点与事件由外键 ON DELETE CASCADE 带走）。
	// 不进回收站：run 树是排障用的执行轨迹，恢复一条对话不该连带恢复
	// 半年前那次失败的执行过程——那既不是用户内容，也可能已经和当前代码不符。
	if err := d.DeleteAgentRunsForConv(id); err != nil {
		return err
	}
	return d.ExecuteParams("DELETE FROM ai_conversations WHERE id = ?", []interface{}{id})
}

// DeleteAIConversationToRecycle 用户主动删除任务：先拍墓碑再真删。
//
// 一条任务里攒着的是几十轮对话，误删等于丢掉一整段上下文；而它不像笔记那样
// 能被「再写一遍」替代。走项目/集合/定时任务同一套 tombstone 机制，
// 「设置 → 历史 → 回收站」里一键恢复。
//
// 消息一并入墓碑：只恢复一个空会话等于什么都没恢复——用户要的是那几轮对话，
// 不是一行标题。消息行数可能成百上千，但都是纯文本，拍进 payload 的代价可以接受。
func (d *Database) DeleteAIConversationToRecycle(id string) error {
	rows := map[string][]map[string]interface{}{}
	d.mu.RLock()
	conv, cerr := captureRows(d.conn, "ai_conversations", "id = ?", id)
	if cerr == nil {
		msgs, merr := captureRows(d.conn, "ai_messages", "conv_id = ?", id)
		if merr == nil {
			rows["ai_messages"] = msgs
		}
		rows["ai_conversations"] = conv
	}
	d.mu.RUnlock()
	if cerr != nil {
		return cerr
	}
	if len(conv) == 0 {
		return d.DeleteAIConversation(id)
	}
	label := rowLabel(conv[0])
	if label == "" {
		// ai_conversations 没有 name 列，rowLabel 取不到值；标题是它真正的展示名。
		if t, ok := conv[0]["title"].(string); ok && t != "" {
			label = t
		} else {
			label = id
		}
	}
	if err := d.AddTombstone(RecycleAITask, id, label, rows); err != nil {
		return err
	}
	return d.DeleteAIConversation(id)
}

// DeleteAISpaceToRecycle 用户主动删除空间：连它下面的全部任务与消息一起进回收站。
//
// 只拍容器不拍内容会让「恢复空间」变成「恢复一个空文件夹」——比误删更难看。
// 因此载荷里带上空间行、其下所有会话行，以及这些会话的全部消息行；
// restoreOrder 保证恢复时按 空间 → 会话 → 消息 的顺序写回，不会撞外键。
func (d *Database) DeleteAISpaceToRecycle(id string) error {
	rows := map[string][]map[string]interface{}{}
	d.mu.RLock()
	space, serr := captureRows(d.conn, "ai_spaces", "id = ?", id)
	var convs []map[string]interface{}
	if serr == nil {
		convs, _ = captureRows(d.conn, "ai_conversations", "space_id = ?", id)
	}
	d.mu.RUnlock()
	if serr != nil {
		return serr
	}
	if len(space) == 0 {
		return d.DeleteAISpace(id)
	}
	rows["ai_spaces"] = space
	msgRows := []map[string]interface{}{}
	for _, c := range convs {
		cid, _ := c["id"].(string)
		if cid == "" {
			continue
		}
		d.mu.RLock()
		msgs, err := captureRows(d.conn, "ai_messages", "conv_id = ?", cid)
		d.mu.RUnlock()
		if err != nil {
			return err
		}
		msgRows = append(msgRows, msgs...)
	}
	rows["ai_conversations"] = convs
	rows["ai_messages"] = msgRows

	label := rowLabel(space[0])
	if label == "" {
		label = id
	}
	if err := d.AddTombstone(RecycleAISpace, id, label, rows); err != nil {
		return err
	}
	// 任务与消息要真删：它们已经在墓碑里了，留着的副本会在恢复时被 upsert 覆盖，
	// 而「删了空间却发现侧栏还挂着那些任务」是最让人困惑的一种界面。
	// 这里不能用 DeleteAISpace —— 那条路是把任务降级为普通任务保留下来。
	for _, c := range convs {
		cid, _ := c["id"].(string)
		if cid == "" {
			continue
		}
		if err := d.DeleteAIConversation(cid); err != nil {
			return err
		}
	}
	return d.ExecuteParams("DELETE FROM ai_spaces WHERE id = ?", []interface{}{id})
}

// DeleteAISpace 解散空间：删掉空间本身，其下任务降级为普通任务而不是一起删掉。
//
// 与 DeleteAISpaceToRecycle 的区别是任务去留：用户看到的删除入口一律走回收站
// （连任务一起走），这条留给「只是不想再分组了」的场景——它不进回收站，
// 因为任务都还在，没有可恢复的东西。
func (d *Database) DeleteAISpace(id string) error {
	if err := d.ExecuteParams(
		`UPDATE ai_conversations SET space_id = '', updated_at = ? WHERE space_id = ?`,
		[]interface{}{now(), id}); err != nil {
		return err
	}
	return d.ExecuteParams("DELETE FROM ai_spaces WHERE id = ?", []interface{}{id})
}

// SetAIConversationCwd 设置（或清空）这个会话绑定的工作目录。
//
// 单独一个方法而不是塞进 UpdateAIConversationMeta：那个用空串表示「这个字段不更新」，
// 而这里的空串是一个有意义的取值——解绑。语义冲突，不能共用一个入参。
func (d *Database) SetAIConversationCwd(id, cwd string) error {
	return d.ExecuteParams(
		`UPDATE ai_conversations SET cwd = ?, updated_at = ? WHERE id = ?`,
		[]interface{}{strings.TrimSpace(cwd), now(), strings.TrimSpace(id)},
	)
}

// UpdateAIConversationMeta 更新标题/摘要/更新时间（空字段表示不更新）
func (d *Database) UpdateAIConversationMeta(id, title, summary string) error {
	sets := []string{}
	args := []interface{}{}
	if title != "" {
		sets = append(sets, "title = ?")
		args = append(args, title)
	}
	if summary != "" {
		sets = append(sets, "summary = ?")
		args = append(args, summary)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now())
	args = append(args, id)
	sql := "UPDATE ai_conversations SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	return d.ExecuteParams(sql, args)
}

// RecordAIUsage 记录一次模型调用的用量明细（ai_usage 一行）。
//
// 只写明细、不动会话上的累加列，两者分开是有意为之：
// 生成标题 / 压缩摘要也是模型调用（要花钱、要进统计），但它们不属于
// 「这个对话里的一问一答」。早期把两者混在一起累加，对话列表里的 token
// 就会比「各条回复下面那些数字之和」大出一截 —— 用户一眼就看出不对。
// 累加交给 AddAIConversationUsage，由调用方决定这次算不算一轮问答。
//
// convID 为空表示这次调用不属于任何对话（SSH 面板 / 剪贴板 / 笔记 / 插件的一次性调用）。
func (d *Database) RecordAIUsage(convID, model, kind string, u TokenUsage) error {
	return d.RecordAIUsageScoped(convID, "", "", model, kind, u)
}

// RecordAIUsageScoped 同 RecordAIUsage，额外带上编排维度（runID / nodeID）。
//
// 为什么要独立方法而不是给 RecordAIUsage 加两个参数：后者有十几处调用点，
// 全都要传两个空串——噪音大且容易传错位（都是 string，编译器不拦）。
// 编排外的调用保持原样，只有编排器那条路径需要指明「这笔钱算谁的」。
func (d *Database) RecordAIUsageScoped(convID, runID, nodeID, model, kind string, u TokenUsage) error {
	if u.Empty() {
		return nil
	}
	if strings.TrimSpace(kind) == "" {
		kind = "chat"
	}
	return d.ExecuteParams(
		`INSERT INTO ai_usage (id, conv_id, run_id, node_id, model, kind, prompt_tokens, completion_tokens, cached_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		[]interface{}{newID(), strings.TrimSpace(convID), strings.TrimSpace(runID), strings.TrimSpace(nodeID),
			strings.TrimSpace(model), kind, u.Prompt, u.Completion, u.Cached, now()},
	)
}

// SumAIUsageByRun 按 run 汇总成本（供编排器做预算控制与事后归集）。
//
// 只汇总该 run 的行：编排外的一次性调用（标题生成、SSH 面板问答）run_id 为空，
// 不会串进来，所以「本次编排花了多少」不会被别处的开销污染。
func (d *Database) SumAIUsageByRun(runID string) (TokenUsage, error) {
	var u TokenUsage
	if strings.TrimSpace(runID) == "" {
		return u, nil
	}
	// 必须给聚合列起别名：QueryOne 返回的是 map，无别名的表达式列名不可预测。
	row, err := d.QueryOne(
		`SELECT COALESCE(SUM(prompt_tokens), 0) AS p,
		        COALESCE(SUM(completion_tokens), 0) AS c,
		        COALESCE(SUM(cached_tokens), 0) AS k
		 FROM ai_usage WHERE run_id = ?`, strings.TrimSpace(runID))
	if err != nil {
		if err == sql.ErrNoRows {
			return u, nil
		}
		return u, err
	}
	u.Prompt = integer(row["p"])
	u.Completion = integer(row["c"])
	u.Cached = integer(row["k"])
	return u, nil
}

// AddAIConversationUsage 把一次问答累加到会话行（列表要显示「这个对话花了多少」，
// 逐次 SUM 查一遍不划算）。只有真正的一问一答才该调它。
//
// countRequest 控制「问答次数」是否 +1：内置 agent 一次提问可能连调五轮工具，
// 五轮都算一次问答会让「我问了 3 句」在列表里显示成 15 次。token 每轮照加
// （那确实是五次计费），次数只在用户自己发问的那一轮加。
func (d *Database) AddAIConversationUsage(convID, model string, u TokenUsage, countRequest bool) error {
	if u.Empty() || strings.TrimSpace(convID) == "" {
		return nil
	}
	n := 0
	if countRequest {
		n = 1
	}
	return d.ExecuteParams(
		`UPDATE ai_conversations SET
		   prompt_tokens = prompt_tokens + ?, completion_tokens = completion_tokens + ?,
		   request_count = request_count + ?, model = ?, updated_at = ?
		 WHERE id = ?`,
		[]interface{}{u.Prompt, u.Completion, n, strings.TrimSpace(model), now(), strings.TrimSpace(convID)},
	)
}

// AIUsageGroup 用量明细的分组行（天 × 模型）
type AIUsageGroup struct {
	Day              string `json:"day"`
	Model            string `json:"model"`
	Kind             string `json:"kind"`
	Requests         int    `json:"requests"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CachedTokens     int    `json:"cachedTokens"`
}

// aiUsageFilter 窗口过滤条件；since 为空表示不限时间。
func aiUsageFilter(since string) (string, []interface{}) {
	if strings.TrimSpace(since) == "" {
		return "", nil
	}
	return " WHERE created_at >= ?", []interface{}{since}
}

// ListAIUsageGroups 按「天 × 模型」聚合用量明细。
//
// 一次查询同时喂给按天、按模型、总额三路统计：分三次查各写一遍过滤条件，
// 很容易出现「总额 ≠ 各模型之和」这种自相矛盾的界面。分组交给 SQL，
// 汇总与计价交给调用方，口径只有一处。
func (d *Database) ListAIUsageGroups(since string) ([]AIUsageGroup, error) {
	where, args := aiUsageFilter(since)
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(
		`SELECT substr(created_at, 1, 10) AS day, COALESCE(model, ''), COALESCE(kind, ''),
		        COUNT(*), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0)
		 FROM ai_usage`+where+`
		 GROUP BY day, model, kind ORDER BY day DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIUsageGroup{}
	for rows.Next() {
		var g AIUsageGroup
		if err := rows.Scan(&g.Day, &g.Model, &g.Kind, &g.Requests, &g.PromptTokens, &g.CompletionTokens, &g.CachedTokens); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CountAIUsageConversations 统计窗口内有过模型调用的会话数。
// 窗口口径与 ListAIUsageGroups 一致（按明细时间），否则「会话数」会对不上 token。
func (d *Database) CountAIUsageConversations(since string) (int, error) {
	where, args := aiUsageFilter(since)
	q := `SELECT COUNT(DISTINCT conv_id) FROM ai_usage`
	if where == "" {
		q += ` WHERE conv_id <> ''`
	} else {
		q += where + ` AND conv_id <> ''`
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.conn.QueryRow(q, args...).Scan(&n)
	return n, err
}

// ListAIMessages 某会话的全部消息（按插入顺序正序）
// 注意：同一次问答里 user 与 assistant 的 created_at 往往落在同一秒，再用随机 UUID 作为
// tiebreaker 会导致顺序不确定（偶发 assistant 排在 user 前面）。改用 rowid（插入顺序）可
// 精确保证 user 消息始终先于其 assistant 回复，且对已存的乱序历史记录无需迁移即可正确展示。
func (d *Database) ListAIMessages(convID string) ([]AIMessage, error) {
	rows, err := d.ListTableWhere("ai_messages", "conv_id = ? ORDER BY rowid ASC", convID)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAIMessage), nil
}

// LastAIMessage 取这个会话的最后一条消息（按插入顺序），还没有消息时返回 (nil, nil)。
//
// 单独开这个查询而不是让调用方 ListAIMessages 之后取末位：幂等落库（见 services/ai
// 的 saveUserTurn）每轮发送都要比对「最后一条是不是我」，而整表读会把这个会话的
// 全部历史（含 reasoning 与几十 KB 的工具结果）拉出来，长会话里这一步会随对话
// 长度线性变慢——而它本来只需要一行。
func (d *Database) LastAIMessage(convID string) (*AIMessage, error) {
	row, err := d.QueryOne("SELECT * FROM ai_messages WHERE conv_id = ? ORDER BY rowid DESC LIMIT 1", convID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	m := mapToAIMessage(row)
	return &m, nil
}

// AddAIMessage 追加一条消息（不含 reasoning）
func (d *Database) AddAIMessage(convID, role, content string) (*AIMessage, error) {
	m := &AIMessage{
		ID:        newID(),
		ConvID:    convID,
		Role:      role,
		Content:   content,
		CreatedAt: now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// AddAIMessageFull 追加一条消息（含 reasoning_content）
func (d *Database) AddAIMessageFull(convID, role, content, reasoningContent string) (*AIMessage, error) {
	m := &AIMessage{
		ID:               newID(),
		ConvID:           convID,
		Role:             role,
		Content:          content,
		ReasoningContent: reasoningContent,
		CreatedAt:        now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// AssistantTurn 一次助手回复的落库参数。
//
// 用结构体而不是继续往 SaveAssistantTurn 上加参数：加上工具调用后已经有 8 个位置
// 参数了，调用点会变成一长串难以核对的实参，传错顺序编译器还不一定拦得住
// （model 与 toolCalls 都是 string）。
type AssistantTurn struct {
	Content          string
	ReasoningContent string
	Model            string
	Usage            TokenUsage
	// Stopped 这条回复被用户中途打断：内容照样落库（半截也比没有强），
	// 但要带标记，否则刷新后无法区分「模型只答了这些」与「我自己点了停止」。
	Stopped bool
	// ToolCalls 工具调用记录的 JSON；空串表示没调过工具。ToolRounds 是轮数。
	ToolCalls  string
	ToolRounds int
}

// SaveAssistantTurn 追加一条助手回复，并记下这一次调用自己的用量。
//
// 与 AddAIMessageFull 分开：那个签名已经被「不带用量的写入」用着（例如外部
// 直接补一条消息），把 usage 塞成第 5 个参数会让每个调用点都要判断传什么。
// 用量放消息上是为了「回答下面那行小字」能持久化——前端 statMap 只活在当前会话。
func (d *Database) SaveAssistantTurn(convID string, t AssistantTurn) (*AIMessage, error) {
	m := &AIMessage{
		ID:               newID(),
		ConvID:           convID,
		Role:             "assistant",
		Content:          t.Content,
		ReasoningContent: t.ReasoningContent,
		Model:            strings.TrimSpace(t.Model),
		PromptTokens:     t.Usage.Prompt,
		CompletionTokens: t.Usage.Completion,
		CachedTokens:     t.Usage.Cached,
		Stopped:          t.Stopped,
		ToolCalls:        t.ToolCalls,
		ToolRounds:       t.ToolRounds,
		CreatedAt:        now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// AddAIMessageImages 追加一条带图片引用的 user 消息。
// images 为 JSON 数组字符串（结构见 AIMessage.Images 注释），无图片时传 ""。
// 与 AddAIMessageFull 分开而不是加可选参数：调用点（流式链路）两种形态都要用，
// 分开后每个调用点的意图在签名上就可见，不会出现"这个参数到底传不传"的犹豫。
func (d *Database) AddAIMessageImages(convID, role, content, images string) (*AIMessage, error) {
	m := &AIMessage{
		ID:        newID(),
		ConvID:    convID,
		Role:      role,
		Content:   content,
		Images:    images,
		CreatedAt: now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// DeleteOldAIMessages 仅保留最近 keep 条，删除更早的消息（摘要压缩后清理冗余上下文）
// 用 rowid 代替 created_at 判定"最新"，避免同秒消息按随机 UUID 误删（可能把同一对问答中的
// user 留下而删掉 assistant，或反之）。rowid 即插入顺序，保留的是最近插入的 keep 条。
func (d *Database) DeleteOldAIMessages(convID string, keep int) error {
	if keep <= 0 {
		return nil
	}
	total, err := d.CountWhere("ai_messages", "conv_id = ?", convID)
	if err != nil {
		return err
	}
	if total <= keep {
		return nil
	}
	sql := `DELETE FROM ai_messages WHERE conv_id = ? AND id NOT IN (
		SELECT id FROM ai_messages WHERE conv_id = ? ORDER BY rowid DESC LIMIT ?
	)`
	return d.ExecuteParams(sql, []interface{}{convID, convID, keep})
}
