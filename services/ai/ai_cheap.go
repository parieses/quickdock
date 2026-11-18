package ai

// 双模型路由（WP11）：给「不需要聪明」的调用换一个便宜模型。
//
// 哪些调用算「不需要聪明」：起标题、摘要历史、压缩上下文。它们的特点是
// **输出很短、失败也不致命、但输入往往很长**——真正的成本在输入侧，所以
// 换模型省的是单价，不是次数。
//
// 为什么不做成自动挑最便宜的模型：价目表只覆盖一部分模型，而「自动挑」
// 一旦挑错（比如挑了个上下文很小的廉价模型），表现是摘要一直失败、
// 对话频繁降级硬截断。让用户显式填一个型号，出错时因果关系是清楚的。

import "strings"

// aiCheapProfile 返回廉价档的档案副本：只换模型名。
//
// 空串表示没配，原样返回——这是默认状态，绝大多数用户不该被迫做选择。
func aiCheapProfile(cfg AIProfile) AIProfile {
	cheap := strings.TrimSpace(cfg.CheapModel)
	if cheap == "" || cheap == strings.TrimSpace(cfg.Model) {
		return cfg
	}
	out := cfg
	out.Model = cheap
	// 廉价模型通常不支持 extended thinking：不关掉的话，请求可能直接被拒
	// （400 未知参数），或者白付一轮思考 token——而这两件事都不会有明确提示。
	out.ThinkingEnabled = false
	return out
}

// aiCheapModelOf 返回当前生效的廉价模型名（没配时就是主模型）。
// 给统计与日志用：账单要能说清「这一次花了哪个模型的价」。
func aiCheapModelOf(cfg AIProfile) string {
	if cheap := strings.TrimSpace(cfg.CheapModel); cheap != "" {
		return cheap
	}
	return strings.TrimSpace(cfg.Model)
}
