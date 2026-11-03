package ai

// 停止信号：区分「用户点了停止」与「页面自己断了」。
//
// 为什么需要一次额外的 HTTP 往返：前端点停止能做的最直接的事是 abort fetch，
// 从服务端看就是 r.Context() 被取消 —— 这与用户刷新页面、关闭窗口、网线被拔
// 完全一致，不附带任何信息。后端因此无法判断「要不要告诉他回答被停了」，
// 只能一律当作用户中断：刷一下页面，通知中心就多一条「AI 回答已停止」。
//
// 这个端点就是那条缺失的信号：先告诉后端「是我按的」，再 abort。
// 顺序不能反 —— 连接一断，handler 就开始收尾，那时再查标记已经晚了。
// 前端为此 await 这个小请求（本地 HTTP 正常 <5ms），并带 200ms 上限兜底，
// 不会把停止按钮卡住。

import (
	"context"
	"errors"
	"strings"

	"quickdock/internal/logger"
)

var (
	// errStopByUser 用户点了停止按钮。只有这一种情况才该说「回答已停止」。
	errStopByUser = errors.New("用户停止")
	// errClientGone 页面自己的连接没了（刷新 / 关窗 / 网络断 / 前端崩溃）。
	errClientGone = errors.New("客户端断开")
)

// stopSlot 一次运行的停止入口。
//
// 为什么要包一层而不是直接存 context.CancelCauseFunc：函数值在 Go 里除了与
// nil 比较之外不可比较，而注销时必须能判断「表里这条还是不是我的」——否则
// 停止后几百毫秒内重发，旧 run 的 defer 会把新 run 的取消函数删掉，新的一轮
// 就再也停不下来（点停止毫无反应，日志里却一切正常）。存指针就比得动。
type stopSlot struct {
	cancel context.CancelCauseFunc
}

// registerStop 把这一轮的停止入口挂到会话上，供 /ai/stop 找到。
func (a *AIService) registerStop(convID string, cancel context.CancelCauseFunc) *stopSlot {
	convID = strings.TrimSpace(convID)
	if a == nil || convID == "" || cancel == nil {
		return nil
	}
	slot := &stopSlot{cancel: cancel}
	a.stopsMu.Lock()
	defer a.stopsMu.Unlock()
	if a.stops == nil {
		a.stops = make(map[string]*stopSlot)
	}
	a.stops[convID] = slot
	return slot
}

// unregisterStop 比对指针再删，理由见 stopSlot 的说明。
func (a *AIService) unregisterStop(convID string, slot *stopSlot) {
	convID = strings.TrimSpace(convID)
	if a == nil || convID == "" {
		return
	}
	a.stopsMu.Lock()
	defer a.stopsMu.Unlock()
	if cur, ok := a.stops[convID]; ok && (slot == nil || cur == slot) {
		delete(a.stops, convID)
	}
}

// requestStop 用户点了停止：取消这条会话正在跑的那一轮，并把原因标成
// errStopByUser。返回 false = 这一刻没有在跑的轮次（前端多半是刚好结束，无害）。
func (a *AIService) requestStop(convID string) bool {
	convID = strings.TrimSpace(convID)
	if a == nil || convID == "" {
		return false
	}
	a.stopsMu.Lock()
	slot := a.stops[convID]
	a.stopsMu.Unlock()
	if slot == nil {
		return false
	}
	slot.cancel(errStopByUser)
	logger.I("[ai] 用户请求停止 conv=%s", convID)
	return true
}
