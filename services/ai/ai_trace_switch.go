package ai

// 运行轨迹的灰度开关（WP1 §9 的 `ai_trace_enabled`）。
//
// 为什么一个旁路功能要开关：事件表是「每次工具调用都写一两行」的持续写入，
// 万一某个版本在某种环境上把它写成了热点（锁竞争、库膨胀），用户手上唯一的
// 手段是关掉整个 AI 助手。有一个一键关掉的开关，才能在不发版的情况下先止血。
//
// 关掉之后行为必须完全退化成「没有轨迹」：不建 run、不写事件、也不要留下
// 半条记录——半条记录比没有记录更难查。

import (
	"strings"

	"quickdock/internal/logger"
)

// aiTraceEnabledKey 开关的落库键。
const aiTraceEnabledKey = "ai_trace_enabled"

// traceEnabled 轨迹是否开启。**默认开**：默认关会让这个功能永远处于
// 「没人验证过」的状态。
func (a *AIService) traceEnabled() bool {
	if a == nil || a.App == nil || a.App.DB == nil {
		return false
	}
	raw, err := a.App.DB.GetSetting(aiTraceEnabledKey)
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "off", "no":
		return false
	case "", "1", "true", "on", "yes":
		return true
	default:
		// 认不出的取值按默认（开）处理，但要说一声：手改坏了配置却悄悄生效
		// 是最难查的一类问题。
		logger.W("[ai] ai_trace_enabled 取值无法识别 %q，按开启处理", raw)
		return true
	}
}
