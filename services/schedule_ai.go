package services

// 定时任务的 AI 动作。
//
// 宿主侧只做参数整理与结果整形：真正的执行在 services/ai 包（runAgent），
// 通过 AppService.RunAIScheduledFn 注入。拆成函数而不是持有 *ai.AIService，
// 是因为 services/ai 反向 import services，直接引会成环。

import (
	"fmt"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// ScheduledAIRun / ScheduledAIRunResult 是跨包注入的边界类型，只含基本字段。
// 定义在 services 包内，main.go 装配时把值逐项搬到 services/ai 的同构结构里。
type ScheduledAIRun struct {
	Prompt string
	Cwd    string
	Mode   string
	TaskID string
	// TaskName / TaskCreatedAt 供 AI 包按「任务名 · 创建时间」定位（或重建）
	// 专属空间。创建时间只用于显示名，不参与调度。
	TaskName      string
	TaskCreatedAt string
	TimeoutSec    int
}

type ScheduledAIRunResult struct {
	ConvID string
	Text   string
	ErrMsg string
}

// schedAITimeoutSec 一轮 AI 的等待上限。比命令动作的 30 秒宽得多：工具循环
// 可能跑十几轮，但也不能无限等——模型流挂住会一直占着 goroutine，
// 而 schedInflight 按任务 id 持有，会把后续所有调度点全部跳过。
const schedAITimeoutSec = 600

// schedAIConcurrentLimit 同时跑 AI 定时任务的上限。
//
// 无人值守是这里最需要护栏的地方：十个任务撞在同一个整点，不加限制就是十路
// LLM 工具循环同时跑，每路都能跑满 10 分钟。瞬时费用、上游限流、本机 CPU
// 挤在一起，结果往往是谁都跑不完。2 是「正的能并行、又不至于把机器吃满」的档。
//
// 闸门内部不排队：排队会把「到点执行」变成一个不可见的队列，10 分钟一轮叠下去，
// 后面的任务在用户看来全是「怎么还没跑」。超限时直接记一条失败并写进运行历史，
// 用户能看见原因、也能手动再跑一次；任务自己配了失败重试的话，调度器会按
// 既有节奏再试几次（相当于短排队，但次数有上限，不会无限等下去）。
const schedAIConcurrentLimit = 2

// schedAISlots 是进程级的信号量。宿主 AppService 本来就只有一个实例，
// 用包级 channel 省掉一次初始化，也保证手动「立即运行」与定时触发共用同一把闸。
var schedAISlots = make(chan struct{}, schedAIConcurrentLimit)

// schedConvMarker 结果串里的会话 id 标记。运行历史要靠它把「这次执行」和
// 「跑出来的那条 AI 会话」连起来；单独加一列 schedule_runs.conv_id 要动
// snapshot/回收站/前端列表三处，先用可解析的固定前缀承载，语义不丢。
const schedConvMarker = "会话 "

// runAITaskAsync 手动「立即运行」AI 任务的后台路径。
//
// 不能复用 RunScheduledTaskNow 的同步主体：一轮 AI 最长 10 分钟，Wails RPC
// 会一直挂着，前端的转圈按钮也撑不住那么久。这里立即回一条说明，实际执行
// 放 goroutine，收尾与定时触发一致（运行历史 + 通知），但刻意不重排 next_run。
//
// schedInflight 同时挡住「手动跑的同时又到了定时点」——两次执行会各自建会话、
// 各自扣费，用户只会看到两条莫名重复的结果。
func (a *AppService) runAITaskAsync(t *db.ScheduledTask) *ApiResult {
	if _, loaded := a.schedInflight.LoadOrStore(t.ID, struct{}{}); loaded {
		return FailMsg("该任务正在执行中")
	}
	a.taskWG.Add(1)
	go func() {
		defer a.taskWG.Done()
		defer a.schedInflight.Delete(t.ID)
		defer func() {
			if r := recover(); r != nil {
				logger.E("QuickDock: 手动 AI 任务 panic: %v", r)
			}
		}()
		startedAt := time.Now()
		status, result := a.executeAITask(t)
		durationMs := time.Since(startedAt).Milliseconds()
		a.NoteActivity(db.ActivityScheduleRun, t.ID, t.Name)
		// 手动执行只更新结果，不改动 next_run / enabled（与同步路径同一口径）。
		runAt := nowStr()
		_ = a.DB.SetTaskRunResult(t.ID, runAt, status, result, t.NextRun, t.Enabled)
		_ = a.DB.InsertScheduleRun(t.ID, t.Name, status, result, durationMs)
		a.emitScheduleRunUpdated(t, status, result, runAt)
		a.notifyScheduleRun(t, status, result, durationMs)
	}()
	return OkMsg("AI 任务已在后台开始，完成后结果会写入运行历史并通知你",
		"AI 任务已在后台开始，完成后结果会写入运行历史并通知你")
}

// ScheduleRunProgressEvent 一次 AI 执行中途的步骤通知（工具名 + 参数摘要）。
//
// 与 ScheduleRunUpdatedEvent 分工：那条是收尾（结果、状态），这条是过程。
// 定时执行没有 SSE 连接，若不推这一条，用户点完「立即运行」只能看到一个转圈，
// 十分钟里无法判断它是在读文件、在跑命令，还是已经卡死。
const ScheduleRunProgressEvent = "schedule:run-progress"

// emitScheduleRunProgress 广播一条执行步骤。
// 没人监听就丢——它只是「界面正开着时能看见」的增强，不参与任何判定。
func (a *AppService) emitScheduleRunProgress(taskID, tool, args string) {
	if a.app == nil {
		return
	}
	a.app.Event.Emit(ScheduleRunProgressEvent, map[string]any{
		"taskId": taskID,
		"tool":   tool,
		"args":   args,
	})
}

// schedProgressMinGap 两条「不同工具」进度事件之间的最小间隔。
//
// 一轮工具循环可能瞬间并发调十几个工具，不节流的话前端每秒要重渲染几十次，
// 而人眼在那个速度下本来也读不清——400ms 差不多是一次「看清楚」的粒度。
const schedProgressMinGap = 400 * time.Millisecond

// schedProgressSameToolMinGap 同一工具连续调用时的最小间隔。
//
// 「又读了一个文件」本身就是进展，等满 400ms 会让人以为卡住了，所以同一工具
// 只挡病态刷屏（例如一毫秒内推十条），不套上面的完整窗口。
const schedProgressSameToolMinGap = 100 * time.Millisecond

// executeAITask 跑一段 AI 提示词，沿用 executeTask 的 (status, result) 契约：
// 结果会进任务卡、运行历史和机器人通知三处，所以必须压成单行摘要。
func (a *AppService) executeAITask(t *db.ScheduledTask) (string, string) {
	if a.RunAIScheduledFn == nil {
		return "fail", "AI 服务未就绪，无法执行 AI 任务"
	}
	prompt := strings.TrimSpace(t.Target)
	if prompt == "" {
		return "fail", "AI 提示词为空"
	}

	// 并发闸门放在参数校验之后：无效任务不该白占一个名额。defer 保证所有
	// 返回路径（成功 / 失败 / 提前 return）都还槽，避免一次异常永久少一个并发位。
	select {
	case schedAISlots <- struct{}{}:
		defer func() { <-schedAISlots }()
	default:
		return "fail", fmt.Sprintf(
			"同时运行的 AI 任务已达上限（%d），本次未执行；等前面的任务跑完可手动再运行",
			schedAIConcurrentLimit)
	}

	// 进度节流状态只在本 goroutine 里读写（runAgent 同步执行），不需要加锁。
	lastTool, lastAt := "", time.Time{}
	onProgress := func(tool, args string) {
		if strings.TrimSpace(tool) == "" {
			return
		}
		gap := schedProgressMinGap
		if tool == lastTool {
			gap = schedProgressSameToolMinGap
		}
		if !lastAt.IsZero() && time.Since(lastAt) < gap {
			return
		}
		lastTool, lastAt = tool, time.Now()
		a.emitScheduleRunProgress(t.ID, tool, args)
	}

	res := a.RunAIScheduledFn(ScheduledAIRun{
		Prompt:        prompt,
		Cwd:           strings.TrimSpace(t.WorkingDir),
		Mode:          strings.TrimSpace(t.AIMode),
		TaskID:        t.ID,
		TaskName:      t.Name,
		TaskCreatedAt: t.CreatedAt,
		TimeoutSec:    schedAITimeoutSec,
	}, onProgress)

	// 会话 id 无论成败都带上：失败时那条半截回答也落在库里，用户仍该能点进去看。
	prefix := ""
	if res.ConvID != "" {
		prefix = schedConvMarker + res.ConvID + " · "
	}
	body := flattenResult(res.Text)
	if res.ErrMsg != "" {
		if body != "" {
			return "fail", prefix + body + " · " + flattenResult(res.ErrMsg)
		}
		return "fail", prefix + flattenResult(res.ErrMsg)
	}
	if body == "" {
		body = "已完成，无正文输出"
	}
	return "ok", prefix + body
}
