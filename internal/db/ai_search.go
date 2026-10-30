package db

import "strings"

// AIMessageHit 一条消息搜索命中。
//
// 只带片段不带全文：侧栏渲染的是「哪条对话里提过这件事」，
// 点进去才是读原文的地方。
type AIMessageHit struct {
	MsgID  string `json:"msg_id"`
	ConvID string `json:"conv_id"`
	// ConvTitle 所属对话的标题。命中列表要能一眼看出「这是哪个任务里的」，
	// 只给 conv_id 等于让前端再查一次。
	ConvTitle string `json:"conv_title"`
	Role      string `json:"role"`
	// Snippet 命中位置前后各留一点上下文，两端用 … 标出被截断。
	Snippet   string `json:"snippet"`
	CreatedAt string `json:"created_at"`
}

const (
	aiSearchDefaultLimit = 20
	// 上限 50：超过这个量级，用户要的已经不是「翻列表」而是「换个更准的词」。
	aiSearchMaxLimit = 50
	// 片段口径：命中点前留 24 个字符（够回看出主语），整段 140。
	// 再长就不是列表条目了——一条工具输出动辄几十 KB，
	// 几十条命中全量传回前端，侧栏会被撑成阅读器。
	aiSearchBefore = 24
	aiSearchSpan   = 140
)

// SearchAIMessages 跨会话搜消息正文。
//
// 侧栏原来只搜标题，而标题是模型起的十几个字——「上次那个连接池报错」这种记忆
// 落在正文里，标题往往只写「修复配置」。搜不到就等于这段历史不存在。
//
// 截断在 SQL 里做（substr + instr）：命中位置只能由库算（Go 侧算要先把整段
// content 读出来，而正是一条工具结果就能占几十 KB），顺带把传输量也压到一个片段。
//
// LIKE 的转义不能省：用户搜「100%」时未转义的 % 会匹配全部消息，
// 表现出来就是「搜索坏了，搜什么都一样」。
func (d *Database) SearchAIMessages(query string, limit int) ([]AIMessageHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = aiSearchDefaultLimit
	}
	if limit > aiSearchMaxLimit {
		limit = aiSearchMaxLimit
	}
	// lower() 两侧都套：SQLite 的 LIKE 对 ASCII 本身不区分大小写，但 instr 区分，
	// 不一致会出现「LIKE 命中了、截取却从开头起」的怪片段。
	const searchMsgSQL = `
		SELECT m.id AS msg_id,
		       m.conv_id AS conv_id,
		       m.role AS role,
		       COALESCE(c.title, '') AS conv_title,
		       m.created_at AS created_at,
		       CASE WHEN instr(lower(m.content), lower(?)) > 0
		            THEN substr(m.content,
		                        max(1, instr(lower(m.content), lower(?)) - ?),
		                        ?)
		            ELSE substr(m.content, 1, ?)
		       END AS snippet
		FROM ai_messages m
		JOIN ai_conversations c ON c.id = m.conv_id
		WHERE m.content LIKE ? ESCAPE '\'
		ORDER BY m.rowid DESC
		LIMIT ?`
	rows, err := d.Query(searchMsgSQL, q, q, aiSearchBefore, aiSearchSpan, aiSearchSpan,
		"%"+likeEscape(q)+"%", limit)
	if err != nil {
		return nil, err
	}
	out := make([]AIMessageHit, 0, len(rows))
	for _, r := range rows {
		out = append(out, AIMessageHit{
			MsgID:     str(r["msg_id"]),
			ConvID:    str(r["conv_id"]),
			ConvTitle: str(r["conv_title"]),
			Role:      str(r["role"]),
			Snippet:   str(r["snippet"]),
			CreatedAt: str(r["created_at"]),
		})
	}
	return out, nil
}

// likeEscape 转义 LIKE 的通配符，配合 ESCAPE '\' 使用。
//
// 顺序由 NewReplacer 保证（一次扫描、不回溯），所以反斜杠写在最前也不会被
// 后续规则二次转义——用四次 strings.Replace 就会，那是这个函数唯一需要注意的地方。
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
