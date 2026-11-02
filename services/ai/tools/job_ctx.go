package aitools

// 「这一轮请求起了哪些后台任务」的收集器。
//
// 解决的问题：用户点停止时，同步命令会随 ctx 被杀掉，但 background=true 起的那条
// 是独立进程，只受「应用退出」管。于是出现最难受的状态——对话停了，一个 find /
// 全盘检索 / 构建还在后台吃 CPU，而界面上找不到任何能停它的入口。
//
// 做法是把「起了哪些 job」挂到 ctx 上：工具侧起任务时登记，runAgent 在 ctx 被取消
// （= 用户点了停止，或连接断了）时统一收尾。ctx 正常结束时整轮代码已经跑完，
// 不会误杀——收尾只在 ctx.Err() != nil 时触发。

import (
	"context"
	"sync"
)

// JobCollector 收集一次请求里起的后台任务 id。
type JobCollector struct {
	mu  sync.Mutex
	ids []string
}

// add 登记一个 job id。
func (c *JobCollector) add(id string) {
	if c == nil || id == "" {
		return
	}
	c.mu.Lock()
	c.ids = append(c.ids, id)
	c.mu.Unlock()
}

// KillAll 终止登记过的、还在跑的任务，返回真正被杀掉的数量。
//
// 已经不在了的（自然结束、用户自己停过）不算：KillJob 返回 false。
func (c *JobCollector) KillAll() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	ids := append([]string(nil), c.ids...)
	c.ids = nil
	c.mu.Unlock()
	n := 0
	for _, id := range ids {
		if KillJob(id) {
			n++
		}
	}
	return n
}

type jobCollectorKey struct{}

// WithJobCollector 把收集器挂到 ctx 上。
func WithJobCollector(ctx context.Context, c *JobCollector) context.Context {
	if ctx == nil || c == nil {
		return ctx
	}
	return context.WithValue(ctx, jobCollectorKey{}, c)
}

// recordJob 从 ctx 里取出收集器并登记（没有收集器时静默跳过）。
func recordJob(ctx context.Context, id string) {
	if ctx == nil {
		return
	}
	if c, ok := ctx.Value(jobCollectorKey{}).(*JobCollector); ok {
		c.add(id)
	}
}
