package ai

import (
	"context"
	"strings"
	"time"

	"quickdock/services"
)

// 一次性问答（非流式）的公共能力边界。
//
// 为什么单独开一组常量而不在调用点写魔法数字：这些上限是成本与稳定性的第一道闸门。
// 插件和 SSH 面板都会走到这里，两者都可能被第三方内容驱动（插件参数 / 终端屏显文本）。
// 上限散落在各自文件里迟早会被某一处"忘了检查"。
const (
	// askMaxSystemRune 系统提示词长度上限（rune 而非 byte：中文一个字 3 字节，
	// 按字节算会让中文提示词在远未到语义上限时就被截断，报错还很难懂）。
	// 放宽到 24000 以容纳插件携带的完整 API 参考等长系统提示词；
	// SSH/剪贴板/笔记等场景使用各自固定的短系统提示词，不受此值影响。
	askMaxSystemRune = 24000

	// askMaxUserRune 用户输入 + 上下文总长度上限。
	// 4000 rune ≈ 2000 个中文字符，够放一段典型报错 + 一次提问，
	// 再大就该让调用方主动裁剪上下文，而不是由后端默默截断成半句话。
	//
	// 注意：截断是从头保留，所以调用方必须自己保证最重要内容排在前、
	// 且总长不超预算——否则被砍掉的正是末尾（往往是用户本次的问题）。
	// 这个上限已经导出（见下方 AskMaxUserRune），调用方应引用它做预算，
	// 而不是各自复制一个数字。
	askMaxUserRune = 4000

	// AskMaxUserRune 暴露给用户侧开支做预算裁剪的上限。
	//
	// 为什么必须导出：AIAsk 的截断没有协商余地，调用方只能自己提前裁。
	// 若各调用方各自维护一份数字，一旦这里调整，那些地方就会静默超预算，
	// 而超预算的后果是"用户的问题被悄悄砍掉"，很难复现也很难排查。
	AskMaxUserRune = askMaxUserRune

	// askMaxTokens 兜底上限。注意这是下限保护：档案里配的 MaxTokens 更小时
	// 沿用档案值（用户可能故意调小省钱），只有未配置或异常时才用这个值。
	askMaxTokens = 2048

	// askTimeout 一次性调用超时。非流式没有理由跑几分钟——超过 30s 基本是
	// 端点配错了或网络黑洞，再等下去只会让 UI 卡住。
	askTimeout = 30 * time.Second

	// askMaxReturnRune 模型返回文本的裁剪上限，防止异常端点返回超长内容把内存打满。
	askMaxReturnRune = 32000
)

// AIAskRequest 一次性问答的入参。
//
// System 与 Question 分开而不是拼成一个大字符串：两者长度上限不同，
// 且分开能保证用户的终端屏显永远进不了 system 位置（见 AIAsk 的实现）。
type AIAskRequest struct {
	// System 可选的系统提示词。留空则用档案里的 SystemPrompt。
	System string `json:"system"`
	// Question 必填，用户实际问的内容。
	Question string `json:"question"`
	// TimeoutSec 可选超时秒数，0 用默认 30s，上限 120s。
	TimeoutSec int `json:"timeoutSec"`
	// MaxTokens 可选上限，0 用档案配置或默认值。
	MaxTokens int `json:"maxTokens"`
}

// AIAskResult 一次性问答的返回。
type AIAskResult struct {
	// Text 模型返回的正文（已裁剪到上限）。
	Text string `json:"text"`
	// Model 实际使用的模型名，供前端展示与排障。
	Model string `json:"model"`
	// Profile 实际使用的档案名。
	Profile string `json:"profile"`
	// Truncated 文本是否因超长被尾部截断。
	Truncated bool `json:"truncated"`
	// ElapsedMs 耗时毫秒数。
	ElapsedMs int64 `json:"elapsedMs"`
}

// AIAvailable 报告当前是否具备可用的 AI 档案。
//
// 单独开这个接口而不是让调用方从 AIAsk 的报错里猜：SSH 面板需要在渲染前
// 就决定 AI 按钮是否置灰，用户没配 API Key 时不该先看到一个输入框，
// 点了才告诉他"没配"。
func (a *AIService) AIAvailable() *services.ApiResult {
	cfg, ok := a.getActiveAIProfile()
	if !ok {
		return services.Ok(map[string]interface{}{"available": false, "reason": "no_profile"})
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return services.Ok(map[string]interface{}{"available": false, "reason": "no_endpoint"})
	}
	return services.Ok(map[string]interface{}{
		"available": true,
		"profile":   cfg.Name,
		"model":     cfg.Model,
	})
}

// AIAsk 一次性（非流式）调用当前激活档案。
//
// 与流式链路的区别，也是它存在的理由：
//   - 不落库。不建会话、不写消息表。SSH 面板和插件问的都是一次性问题，
//     存进 AI 对话列表只会污染用户的聊天历史。
//   - 不占用 convId。不会打断前端正在进行的流式对话。
//   - 有超时。流式靠 ctx 控制生命周期，这里必须自带兜底。
//
// 这条路径同时服务三个调用方：SSH 面板（services/sshx）、插件宿主 API
// （services/plugin 的 host.ai.ask）、以及任何未来的内部调用方。
func (a *AIService) AIAsk(req AIAskRequest) *services.ApiResult {
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return services.FailMsg("问题不能为空")
	}
	cfg, ok := a.getActiveAIProfile()
	if !ok {
		return services.FailMsg("未配置可用的 AI 档案")
	}

	system := strings.TrimSpace(req.System)
	if system == "" {
		system = cfg.SystemPrompt
	}
	if n := len([]rune(system)); n > askMaxSystemRune {
		system = string([]rune(system)[:askMaxSystemRune])
	}
	if n := len([]rune(question)); n > askMaxUserRune {
		question = string([]rune(question)[:askMaxUserRune])
	}

	timeout := askTimeout
	if req.TimeoutSec > 0 {
		if req.TimeoutSec > 120 {
			req.TimeoutSec = 120
		}
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	maxTok := cfg.MaxTokens
	if maxTok <= 0 {
		maxTok = aiDefaultMax
	}
	if req.MaxTokens > 0 && req.MaxTokens < maxTok {
		maxTok = req.MaxTokens
	}
	if maxTok > askMaxTokens {
		maxTok = askMaxTokens
	}
	// 必须把结果挂到这次调用真正读的那份配置上：callAIOnce 只认 cfg.MaxTokens，
	// 算出来却不用等于这两个上限（req.MaxTokens 的收紧、askMaxTokens 的兜底）
	// 形同虚设——用户把上限调到 200 想省钱，请求里发的仍是档案里的 8192。
	// 用局部副本而不是改 a 的缓存：一次性问答不该污染对话流用的档案。
	askCfg := cfg
	askCfg.MaxTokens = maxTok

	start := time.Now()
	text, usage, err := a.callAIOnce(ctx, askCfg, []map[string]string{
		{"role": "system", "content": system},
		{"role": "user", "content": question},
	})
	if err != nil {
		return services.Fail(err)
	}
	// 一次性调用同样花钱，必须记进统计：这条路径服务 SSH 面板、剪贴板 AI、
	// 笔记 AI 与插件 host.ai.ask。早期只记对话流，这几处消耗在统计里完全是空白。
	a.recordAIUsage("", usageKindAsk, askCfg, usage)

	truncated := false
	if n := len([]rune(text)); n > askMaxReturnRune {
		text = string([]rune(text)[:askMaxReturnRune])
		truncated = true
	}

	return services.Ok(AIAskResult{
		Text:      text,
		Model:     cfg.Model,
		Profile:   cfg.Name,
		Truncated: truncated,
		ElapsedMs: time.Since(start).Milliseconds(),
	})
}

// AIAskStream 与 AIAsk 同源，但每收到一段增量文本就通过 onToken 回调，
// 供前端逐字流式渲染——这正是「问 AI」面板从"等半天出整段"变成"边想边出"的关键。
//
// 约束与 AIAsk 完全一致（见其注释）：不落库、不占 convId、自带超时、
// 返回裁剪后的完整文本。区别只在传输方式：这里把增量交给调用方，
// 最终的完整文本仍按 AIAsk 的口径裁剪后返回，方便调用方做一次性的分段解析。
//
// 为何不导出到前端：onToken 是 func(string)，跨不过 JSON 边界——生成器会为它
// 吐一条 "function types are not supported by encoding/json" 告警，并产出
// 一个参数填不进去的假方法。前端要流式请走 StartAIStreamServer 的 SSE 通道；
// 这里的调用方只有 Go 侧（main.go 的 AskAIFnStream，供 goja 插件的宿主 API）。
//
//wails:ignore
func (a *AIService) AIAskStream(req AIAskRequest, onToken func(text string)) *services.ApiResult {
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return services.FailMsg("问题不能为空")
	}
	cfg, ok := a.getActiveAIProfile()
	if !ok {
		return services.FailMsg("未配置可用的 AI 档案")
	}

	system := strings.TrimSpace(req.System)
	if system == "" {
		system = cfg.SystemPrompt
	}
	if n := len([]rune(system)); n > askMaxSystemRune {
		system = string([]rune(system)[:askMaxSystemRune])
	}
	if n := len([]rune(question)); n > askMaxUserRune {
		question = string([]rune(question)[:askMaxUserRune])
	}

	timeout := askTimeout
	if req.TimeoutSec > 0 {
		if req.TimeoutSec > 120 {
			req.TimeoutSec = 120
		}
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	maxTok := cfg.MaxTokens
	if maxTok <= 0 {
		maxTok = aiDefaultMax
	}
	if req.MaxTokens > 0 && req.MaxTokens < maxTok {
		maxTok = req.MaxTokens
	}
	if maxTok > askMaxTokens {
		maxTok = askMaxTokens
	}
	// 同上：streamAIChat 也只读 cfg.MaxTokens，不挂上去这两个上限就白算了。
	askCfg := cfg
	askCfg.MaxTokens = maxTok

	// streamAIChat 收 []map[string]interface{}（与 buildAIMessages 同构），
	// 这里手搓两条消息即可，不必再走一次会话/摘要逻辑。
	messages := []map[string]interface{}{
		{"role": "system", "content": system},
		{"role": "user", "content": question},
	}

	start := time.Now()
	// 一次性问答不带工具：它的调用方（SSH 面板 / 剪贴板 AI / 笔记 AI / 插件
	// host.ai.ask）都在等一句短回答，没有展示工具调用卡片的位置，
	// 也没有让它连调几轮的预算。工具只对 AI 页的对话流开放。
	turn, err := a.streamAIChat(ctx, askCfg, messages, "ai-ask", nil, onToken, nil)
	if err != nil {
		return services.Fail(err)
	}
	a.recordAIUsage("", usageKindAsk, askCfg, turn.Usage)

	text := turn.Content
	truncated := false
	if n := len([]rune(text)); n > askMaxReturnRune {
		text = string([]rune(text)[:askMaxReturnRune])
		truncated = true
	}

	return services.Ok(AIAskResult{
		Text:      text,
		Model:     cfg.Model,
		Profile:   cfg.Name,
		Truncated: truncated,
		ElapsedMs: time.Since(start).Milliseconds(),
	})
}
