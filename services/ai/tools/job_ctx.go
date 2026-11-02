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

// JobDone 一条「后台任务已结束」的通知。
//
// 存在的理由：后台任务的结束与模型的一轮输出在时间上完全无关。没有这条通知，
// 模型想知道结果就只能反复调 job_status（每条查询都是一次完整的模型往返），
// 而用户在界面上看到的是一串「正在查询任务状态」——这正是同类工具明确禁止的做法。
// 有了它，任务结束时把事实攒在收集器上，下一轮模型调用前随消息一起递过去。
type JobDone struct {
	ID       string
	Command  string
	Status   string // db.AIJob* 之一
	ExitCode int
	// DurationMs 从启动到结束的毫秒数。
	DurationMs int64
	// ErrMsg 启动失败或异常退出原因；正常结束为空。
	ErrMsg string
}

// JobCollector 收集一次请求里起的后台任务 id。
type JobCollector struct {
	mu  sync.Mutex
	ids []string
	// done 已经结束、但还没交给模型的任务。
	done []JobDone
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

// addDone 记一条「任务已结束」的通知。
func (c *JobCollector) addDone(ev JobDone) {
	if c == nil || ev.ID == "" {
		return
	}
	c.mu.Lock()
	c.done = append(c.done, ev)
	c.mu.Unlock()
}

// DrainDone 取走尚未交付的通知（取完即清空）。
//
// 必须「取走」而不是「读一遍」：agent 循环每轮都会调它，
// 留着重读会让同一条任务结束事件被反复塞进上下文，慢慢把窗口吃掉。
func (c *JobCollector) DrainDone() []JobDone {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	evs := c.done
	c.done = nil
	c.mu.Unlock()
	return evs
}

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

// jobCollectorFrom 取出 ctx 上的收集器；没挂返回 nil。
//
// 起后台任务时就要把它抓在手里，不能等到任务结束再从 ctx 取：任务往往在整轮
// 结束之后才退出，那时的 ctx 早就被 cancel 了，值虽然还在、语义上已经不该用。
func jobCollectorFrom(ctx context.Context) *JobCollector {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(jobCollectorKey{}).(*JobCollector)
	return c
}
