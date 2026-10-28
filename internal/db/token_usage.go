package db

// TokenUsage 一次模型调用的 token 用量。
//
// Cached 是「输入里命中前缀缓存的那部分」（DeepSeek 上下文缓存 / OpenAI prompt caching）：
// 它已经包含在 Prompt 里，不是独立的一份。必须单独记的原因是价格差一个数量级
// （缓存读取通常只有正常输入的 1/4 ~ 1/10），按全价算会让估算花费远高于账单——
// 长会话尤其明显，同一段系统提示词每轮都命中，命中率能到 99%。
type TokenUsage struct {
	Prompt     int `json:"promptTokens"`
	Completion int `json:"completionTokens"`
	Cached     int `json:"cachedTokens"`
}

// Empty 上游没回 usage（部分兼容接口如此）时为真，此时不记录也不展示。
func (u TokenUsage) Empty() bool { return u.Prompt <= 0 && u.Completion <= 0 }
