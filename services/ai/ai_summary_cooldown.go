package ai

// 摘要失败冷却（WP4b 的配套修复）。
//
// 症状：用户看到「上下文已压缩 43k → 37k（摘要失败，已直接截断最旧部分）」
// 反复出现，像是一直在压缩。真实成因有两层：
//
//  1. 摘要每次都失败（上游超窗 / 拒绝 / 端点不认），而我们每轮都会再试一次
//     ——每次都是一次完整计费的模型调用，外带一条吓人的提示；
//  2. 循环内的「折叠较早工具结果」也走同一个 compact 事件，前端把它当成
//     「摘要失败」渲染，于是一轮里发生几次折叠就出现几条失败提示。
//
// 这里只解决第 1 层：失败后冷却一段时间，期间直接硬截断（对话照常继续），
// 冷却结束再给摘要一次机会。第 2 层靠事件里的 kind 由前端区分。

import (
	"sync"
	"time"
)

// aiSummaryFailCooldown 摘要失败后的冷却时长。
//
// 10 分钟：够躲开一轮连续失败（通常是上游在抖动或这个模型就是不支持），
// 又不至于让一次偶发失败永久关掉摘要。
const aiSummaryFailCooldown = 10 * time.Minute

var aiSummaryFails sync.Map // convID -> time.Time（最近一次失败的时刻）

// markSummaryFailure 记一次摘要失败。
func markSummaryFailure(convID string) {
	if convID == "" {
		return
	}
	aiSummaryFails.Store(convID, time.Now())
}

// clearSummaryFailure 摘要成功后清掉失败记录。
func clearSummaryFailure(convID string) {
	if convID == "" {
		return
	}
	aiSummaryFails.Delete(convID)
}

// summaryCoolingDown 这个会话是否还在摘要失败的冷却期内。
//
// 时间倒退（系统时钟被改）时按「不在冷却」处理：宁可多试一次，
// 也不要因为一次时钟异常把摘要永久关掉。
func summaryCoolingDown(convID string) bool {
	if convID == "" {
		return false
	}
	v, ok := aiSummaryFails.Load(convID)
	if !ok {
		return false
	}
	at, ok := v.(time.Time)
	if !ok {
		return false
	}
	age := time.Since(at)
	if age < 0 {
		return false
	}
	return age < aiSummaryFailCooldown
}

// resetSummaryCooldown 清空全部冷却记录（测试用）。
func resetSummaryCooldown() {
	aiSummaryFails.Range(func(k, _ any) bool {
		aiSummaryFails.Delete(k)
		return true
	})
}
