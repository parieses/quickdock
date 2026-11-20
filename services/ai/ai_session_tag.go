package ai

// 请求侧的会话标识：网关（magpie 一类）靠它把同一条会话的请求归到一起。
//
// 为什么拆成「名字 + ID」两样：
// 看板把这两样显示在**两个位置**——列表里是人看的名字，点开详情才显示机器 ID。
// 其他 agent 就是这个形态（列表写「查看截图」，悬停才是 UUID）。混成一个
// （比如名字后面缀随机码）虽然唯一，但列表上一排「缓存命中原因为-3F9A」
// 既不像人话，也没有能给用户看的会话 id。
//
// 名字取**会话首条提问**的前几个字，会话内固定：
//   - 取「每次提问」的话，同一会话里两次提问会让名字来回变，看板上像两个人；
//   - 会话名本来就该回答「这条会话在聊什么」，那是第一条消息决定的。
//
// ID 用 ai_conversations.id（UUID），与点开详情看到的完全一致。
// 两样都不落库：名字由首条消息确定性推出，重启后算出同一个值。

import (
	"context"
	"strings"
	"sync"
	"unicode"

	"quickdock/internal/logger"
)

// sessionInfo 一次请求要带的会话标识。
type sessionInfo struct {
	// Name 可读名：会话首条提问的前 8 个字（已滤掉标点）。
	Name string
	// ID 会话 id，与 ai_conversations.id 一致。
	ID string
}

// usable 两样都空时说明调用方没提供（测试连接等），请求里就不带。
func (s sessionInfo) usable() bool { return s.Name != "" || s.ID != "" }

type sessionInfoKey struct{}

// withSessionInfo 把会话标识挂到 ctx 上。
func withSessionInfo(ctx context.Context, si sessionInfo) context.Context {
	if ctx == nil || !si.usable() {
		return ctx
	}
	return context.WithValue(ctx, sessionInfoKey{}, si)
}

// sessionInfoOf 取 ctx 上的会话标识（没有返回零值）。
func sessionInfoOf(ctx context.Context) sessionInfo {
	if ctx == nil {
		return sessionInfo{}
	}
	si, _ := ctx.Value(sessionInfoKey{}).(sessionInfo)
	return si
}

// sessionNames 会话 id → 可读名（进程内缓存）。
//
// 名字由「首条用户消息」确定，而那条消息在第一轮之后就永远不变，所以缓存是
// 安全的；重启后重查一次会算出同一个值。不缓存的话每轮请求都要拉一次历史。
var sessionNames sync.Map

// sessionNameFor 取/算某条会话的可读名（首条用户消息的前 8 个字）。
//
// fallback 是当前这次提问的正文：会话还没有落库消息时（第一发的极早期）
// 直接用它，省掉一次空查询。
func (a *AIService) sessionNameFor(convID, fallback string) string {
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return tagPrefix(fallback)
	}
	if v, ok := sessionNames.Load(convID); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	name := ""
	// 只取尾部若干条：首条提问在 50 条以内几乎必然会覆盖到。查不到就退回
	// fallback——宁可名字是这次问的话，也不要空着变成「未命名」。
	if a != nil && a.App != nil && a.App.DB != nil {
		if msgs, err := a.App.DB.ListAIMessagesTail(convID, 50); err == nil {
			for _, m := range msgs {
				if m.Role != "user" || strings.TrimSpace(m.Content) == "" {
					continue
				}
				name = tagPrefix(m.Content)
				break
			}
		} else {
			logger.W("[ai] 读取会话首条消息失败 conv=%s: %v", convID, err)
		}
	}
	if name == "" {
		name = tagPrefix(fallback)
	}
	if name == "" {
		return "chat"
	}
	sessionNames.Store(convID, name)
	return name
}

// sessionInfoForConversation 只有会话 id、没有 run ctx 时的标识。
//
// 标题生成、会话摘要这类调用是从按钮/设置页触发的，不经过 runAgent，因此拿不到
// ctx 上的标识。但它们在网关上同样属于某条会话，不该掉进「未提供会话标识」。
// 名字仍然取会话首条提问——与流式路径算出来的是同一个值。
func (a *AIService) sessionInfoForConversation(convID string) sessionInfo {
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return sessionInfo{}
	}
	return sessionInfo{Name: a.sessionNameFor(convID, ""), ID: convID}
}

// tagPrefix 取文本的可读前缀：只留字母 / 数字 / 汉字，最多 8 个字。
//
// 标点、空白、emoji 一律跳过：它们要么在看板上难读，要么让名字变成一串问号。
// 整句都是符号时返回空串，由调用方决定兜底成什么。
func tagPrefix(q string) string {
	const maxRune = 8
	out := make([]rune, 0, maxRune)
	for _, r := range strings.TrimSpace(q) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		out = append(out, r)
		if len(out) >= maxRune {
			break
		}
	}
	return string(out)
}
