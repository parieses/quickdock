package ai

import (
	"strings"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/services"
)

// aiMsgSearchMinRunes 正文搜索的最短词长。
//
// 单字搜索在长会话上是全表扫 + 几乎全命中：侧栏会瞬间被几十条无关片段填满，
// 而用户此刻通常还在打字。标题搜索仍然一字就搜——那是内存里的一次 includes。
const aiMsgSearchMinRunes = 2

// AISearchMessages 跨会话搜消息正文（bound）。
//
// 与 AIListConversations 并存而不是给它加 query 参数：列表是「我在哪」，
// 搜索是「那句话在哪」，前者每次打开侧栏都要，后者只在打字时；合成一个方法
// 会让每次列会话都为一条可能用不上的 LIKE 付出代价。
func (a *AIService) AISearchMessages(query string, limit int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	q := strings.TrimSpace(query)
	// 太短就不查，返回空列表而不是错误：这是「打字过程中的中间状态」，
	// 弹一个「关键词太短」只会让人以为搜索坏了。
	if utf8.RuneCountInString(q) < aiMsgSearchMinRunes {
		return services.Ok([]db.AIMessageHit{})
	}
	hits, err := a.App.DB.SearchAIMessages(q, limit)
	if err != nil {
		return services.Fail(err)
	}
	// 同一段话里可能命中好几条（用户追问、模型复述），侧栏按对话聚合才有意义：
	// 一屏十几条全是同一条任务的片段，反而看不出「还有别的对话提过这件事」。
	return services.Ok(aggregateMessageHits(hits))
}

// aiConvHit 一条对话的消息命中聚合。
type aiConvHit struct {
	ConvID    string `json:"conv_id"`
	ConvTitle string `json:"conv_title"`
	// Count 这条对话里命中了几条消息（聚合前的条数，用于「还有 N 条」）。
	Count int `json:"count"`
	// Latest 最新那条命中，作为列表里展示的那一行。
	Latest db.AIMessageHit `json:"latest"`
}

// aggregateMessageHits 按对话聚合，保持库返回的顺序（新→旧）。
//
// 顺序按「最近一次命中」而不是按命中条数：用户找的是「上次说的那件事」，
// 时间比热度更接近他的记忆方式。
func aggregateMessageHits(hits []db.AIMessageHit) []aiConvHit {
	if len(hits) == 0 {
		return []aiConvHit{}
	}
	idx := make(map[string]int, len(hits))
	out := make([]aiConvHit, 0, len(hits))
	for _, h := range hits {
		if i, ok := idx[h.ConvID]; ok {
			out[i].Count++
			continue
		}
		idx[h.ConvID] = len(out)
		out = append(out, aiConvHit{
			ConvID:    h.ConvID,
			ConvTitle: h.ConvTitle,
			Count:     1,
			Latest:    h,
		})
	}
	return out
}
