package ai

// 上游重试的提示通道（WP15 的尾巴）。
//
// 背景：`streamAIChat` 里已经有 429/5xx 重试（最多 3 次、优先 Retry-After），
// 但重试期间界面上什么都不动——用户看到的是「一直在转圈」，很容易以为卡死
// 然后点停止，正好把一次马上要成功的请求掐掉。
//
// 为什么走 ctx 而不是加参数：`streamAIChat` 有 5 个调用点（对话、问答、测试），
// 只有对话这一条有 sink。为一个可选提示去改所有签名，等于让每个调用方都多背
// 一个 nil 参数；而 ctx 钩子在本项目已有先例（plan 回调、命令进度都是这么传的）。

import (
	"context"
	"time"
)

// retryNoticeFn 一次重试发生时的回调。
//
// 参数：第几次重试（从 1 开始）、等多久、上游状态码（网络层错误为 0）。
type retryNoticeFn func(attempt int, wait time.Duration, status int)

type retryNoticeKey struct{}

// withRetryNotice 把重试提示挂到 ctx 上。
func withRetryNotice(ctx context.Context, fn retryNoticeFn) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, retryNoticeKey{}, fn)
}

// retryNoticeFrom 取出重试提示；没有则返回 nil。
//
// 调用方必须判 nil：问答、测试等路径不会挂这个钩子，而 nil 调用会让
// 一次本该安静重试的请求直接 panic。
func retryNoticeFrom(ctx context.Context) retryNoticeFn {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(retryNoticeKey{}).(retryNoticeFn)
	return fn
}

// notifyRetry 安全地发一次重试提示。
func notifyRetry(ctx context.Context, attempt int, wait time.Duration, status int) {
	if fn := retryNoticeFrom(ctx); fn != nil {
		fn(attempt, wait, status)
	}
}
