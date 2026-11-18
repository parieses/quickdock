package ai

// 引擎可替换（WP14）：把「跑一轮 agent」这件事收成一个接口。
//
// 本批只做**接口缝**，不实现 DSH 适配器——那条路要走 stdio JSON-RPC 与会话
// 映射，是几天的量，而且「要不要用 DSH 引擎」本身还没拍板。先把缝留出来，
// 目的是让「换引擎」不再意味着改 runAgent 的调用点。
//
// 为什么接口这么小：它只描述**一次运行**（入参、事件出口、结果），不认识
// 内部的轮次预算、审批闸门、压缩策略。接口一旦把这些也写进去，任何新引擎
// 都得先实现一遍我们的内部机制——那就不叫可替换了。

import (
	"context"
	"fmt"
	"strings"

	"quickdock/internal/logger"
)

// AIAgentEngine 一次 agent 运行的可替换实现。
type AIAgentEngine interface {
	// Name 引擎标识（用于日志与「这次用的是什么引擎」的展示）。
	Name() string
	// Run 执行一次运行。事件通过 sink 流出，返回值与内置实现同构。
	Run(ctx context.Context, req AgentRunReq, sink EventSink) AgentRunResult
}

// aiAgentEngineBuiltin 内置引擎的标识。
const aiAgentEngineBuiltin = "builtin"

// builtinEngine 现有实现（就是原来的 runAgent）。
type builtinEngine struct{ svc *AIService }

func (e builtinEngine) Name() string { return aiAgentEngineBuiltin }

func (e builtinEngine) Run(ctx context.Context, req AgentRunReq, sink EventSink) AgentRunResult {
	return e.svc.runAgentBuiltin(ctx, req, sink)
}

// aiAgentEngineFactories 可用的引擎工厂。
//
// 目前只有内置一个。加引擎的方式是在这里注册一个新工厂，而不是在调用点
// 写 if——三个调用点（主对话、子 agent、定时任务）都得跟着改，那种改法
// 一定会漏掉一处。
var aiAgentEngineFactories = map[string]func(*AIService) AIAgentEngine{
	aiAgentEngineBuiltin: func(s *AIService) AIAgentEngine { return builtinEngine{svc: s} },
}

// aiAgentEngine 取当前配置的引擎；配置不认识时回落到内置并记一条警告。
//
// 回落而不是报错：引擎名是存在设置里的字符串，用户升级/回滚版本后它可能
// 指向一个已经不存在的引擎。那种时候「对话还能用」比「明确告诉你引擎丢了」
// 重要得多——后者可以在日志里说。
func (a *AIService) aiAgentEngine() AIAgentEngine {
	name := strings.TrimSpace(a.loadAgentEngineName())
	if f, ok := aiAgentEngineFactories[name]; ok {
		return f(a)
	}
	if name != "" && name != aiAgentEngineBuiltin {
		logger.W("[ai] 未知的 agent 引擎 %q，回落到内置", name)
	}
	if f, ok := aiAgentEngineFactories[aiAgentEngineBuiltin]; ok {
		return f(a)
	}
	// 注册表里连内置都没有 = 构建期错误（有人把注册删了）。
	panic(fmt.Sprintf("没有可用的 agent 引擎（注册表里缺少 %q）", aiAgentEngineBuiltin))
}

// aiAgentEngineSettingKey 引擎名的落库键。
const aiAgentEngineSettingKey = "ai_agent_engine"

// loadAgentEngineName 读引擎名；读不到时返回内置。
func (a *AIService) loadAgentEngineName() string {
	if a == nil || a.App == nil || a.App.DB == nil {
		return aiAgentEngineBuiltin
	}
	raw, err := a.App.DB.GetSetting(aiAgentEngineSettingKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return aiAgentEngineBuiltin
	}
	return strings.TrimSpace(raw)
}

// runAgent 现在的入口：交给当前引擎。
//
// 保留这个名字与签名，是因为三个调用点都只用这一层——换引擎不该改它们。
func (a *AIService) runAgent(ctx context.Context, req AgentRunReq, sink EventSink) AgentRunResult {
	return a.aiAgentEngine().Run(ctx, req, sink)
}
