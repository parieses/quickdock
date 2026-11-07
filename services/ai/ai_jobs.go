package ai

// 后台任务面板的后端接口（run_command background=true 起的进程）。
//
// 三份数据各有归属，这里只做拼装，不另实现一遍：
//   - 状态行在 ai_jobs 表（先落行再起进程，见 tools/job.go）；
//   - 进程句柄在 aitools 的内存表里，终止走 aitools.KillJob；
//   - 日志读 aitools.ReadJobLog（与模型 job_output 同一段解码逻辑）。
//
// 分开实现的话，面板显示「运行中」而模型的 job_status 说「已结束」这类
// 自相矛盾的画面迟早会出现。

import (
	"errors"
	"os"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/services"
	aitools "quickdock/services/ai/tools"
)

// AIJobsPanelLimit 面板一次最多列多少条。
//
// 任务表每周清理一次，一次会话通常只有个位数；给 50 条是为了「昨天跑的那个
// 还在不在」也能翻到，再多就只是让用户滚列表。
const aiJobsPanelLimit = 50

// AIJobView 面板的一行。字段名用 camelCase，与其余 AI 绑定返回保持一致。
type AIJobView struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exitCode"`
	Error     string `json:"error"`
	LogPath   string `json:"logPath"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
	// DurationMs 已运行时长：运行中按「到现在」，结束后按起止差。
	DurationMs int64 `json:"durationMs"`
	Running    bool  `json:"running"`
	// LogExists 日志文件还在不在：7 天清理之后行还在、文件已经被删了，
	// 面板上「看日志」就该是灰的，而不是点了才报错。
	LogExists bool `json:"logExists"`
}

// AIJobsList 当前会话最近的若干条后台任务（含运行中与已结束）。
//
// convID 由前端传「界面上正打开的那个会话」：任务是在某次对话里让 AI 起的，
// 切到别的会话还列着一堆不相干的进程既认不出、也容易误点终止。
// 传空串 = 不过滤，保留给宿主诊断；正常 UI 路径不会走到。
func (a *AIService) AIJobsList(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	rows, err := a.App.DB.ListAIJobs(aiJobsPanelLimit, strings.TrimSpace(convID))
	if err != nil {
		return services.Fail(err)
	}
	out := make([]AIJobView, 0, len(rows))
	for _, j := range rows {
		out = append(out, aiJobView(j))
	}
	return services.Ok(out)
}

// aiJobKillWait 终止后最多等多久让状态落库。
//
// 为什么要等：杀掉进程只是第一步，「已终止」这一行要等 waitBackgroundJob 收尾才写。
// 前端点完按钮会立刻重拉列表——不等的话它拿到的还是「运行中」，用户看到的是
// 「点了没反应」，得等到下一次轮询（面板 2.5s / 输入框上方横条 5s）才改过来。
// 上限压在两秒内：再久就是进程没死，那是另一类问题，不该拖住界面。
const aiJobKillWait = 1500 * time.Millisecond

// AIJobKill 终止一个还在跑的后台任务。
//
// 「进程表里没有」和「杀不掉」必须分开：前者是应用重启过、行还挂着 running
// （宿主被强杀时没机会收尾），直接把状态改成已终止；后者才是真的失败。
func (a *AIService) AIJobKill(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return services.Fail(errors.New("缺少任务 id"))
	}
	j, err := a.App.DB.GetAIJob(id)
	if err != nil {
		return services.Fail(err)
	}
	if j == nil {
		return services.Fail(errors.New("没有这个后台任务"))
	}
	if j.Status != db.AIJobRunning {
		return services.Ok(map[string]any{"killed": false, "status": j.Status, "note": "任务已经结束"})
	}
	if aitools.KillJob(id) {
		// 状态由 waitBackgroundJob 统一收尾（它会写成 killed 并补日志结论），
		// 这里只等它落库，不越权改状态。
		settled := a.waitAIJobSettled(id, aiJobKillWait)
		if settled != nil && settled.Status == db.AIJobRunning && !aitools.JobAlive(id) {
			// 等到了上限、进程句柄已经没了、行却还挂着 running：收尾那条路没走完
			// （比如进程组里有孙进程让它一直 Wait 不到）。这种幽灵行会让模型
			// 一直等一个不存在的任务，直接按已终止收掉。
			_ = a.App.DB.FinishAIJob(id, db.AIJobKilled, 0, "进程已退出，状态由界面兜底修正")
			settled, _ = a.App.DB.GetAIJob(id)
		}
		status := ""
		if settled != nil {
			status = settled.Status
		}
		res := map[string]any{"killed": true, "status": status}
		if status == db.AIJobRunning {
			// 杀了但没在等待窗口内退出：可能被系统挡住，也可能进程还在收尾。
			// 状态照样以库里为准，前端继续轮询即可自动追上，不在这里硬改。
			res["note"] = "进程尚未在 1.5 秒内退出，状态会在它退出后自动更新"
		}
		return services.Ok(res)
	}
	_ = a.App.DB.FinishAIJob(id, db.AIJobKilled, 0, "")
	return services.Ok(map[string]any{"killed": false, "status": db.AIJobKilled, "note": "进程已不在，状态已修正为已终止"})
}

// waitAIJobSettled 轮询到这一行不再是 running（或超时），返回最后一次读到的行。
//
// 轮询间隔取 40ms：finishJob 在进程退出后几乎立刻写库，正常情况一两轮就结束，
// 不需要更密；也不做「睡满一千五」再读一次，那样最短路径也要白白等一次超时。
func (a *AIService) waitAIJobSettled(id string, max time.Duration) *db.AIJob {
	deadline := time.Now().Add(max)
	for {
		j, err := a.App.DB.GetAIJob(id)
		if err == nil && j != nil && j.Status != db.AIJobRunning {
			return j
		}
		if time.Now().After(deadline) {
			return j
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// AIJobLog 读一个后台任务的日志尾部。
//
// 只给尾部：面板是「看一眼跑到哪了」，把几十 MB 的构建日志塞进 WebView
// 会把整页卡住；要看开头用户可以直接打开 logPath。
func (a *AIService) AIJobLog(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return services.Fail(errors.New("缺少任务 id"))
	}
	j, err := a.App.DB.GetAIJob(id)
	if err != nil {
		return services.Fail(err)
	}
	if j == nil {
		return services.Fail(errors.New("没有这个后台任务"))
	}
	if j.LogPath == "" {
		return services.Fail(errors.New("这个任务没有日志文件"))
	}
	text, truncated, rerr := aitools.ReadJobLog(j.LogPath, false)
	if rerr != nil {
		return services.Fail(rerr)
	}
	return services.Ok(map[string]any{
		"jobId":     j.ID,
		"output":    text,
		"truncated": truncated,
		"status":    j.Status,
		"exitCode":  j.ExitCode,
		"logPath":   j.LogPath,
	})
}

// aiJobView 表行 → 面板行。
func aiJobView(j db.AIJob) AIJobView {
	v := AIJobView{
		ID:        j.ID,
		Command:   j.Command,
		Cwd:       j.Cwd,
		Status:    j.Status,
		ExitCode:  j.ExitCode,
		Error:     j.Error,
		LogPath:   j.LogPath,
		StartedAt: j.StartedAt,
		EndedAt:   j.EndedAt,
		Running:   j.Status == db.AIJobRunning,
	}
	if t, err := time.Parse(time.RFC3339, j.StartedAt); err == nil {
		end := time.Now()
		if j.EndedAt != "" {
			if e, eerr := time.Parse(time.RFC3339, j.EndedAt); eerr == nil {
				end = e
			}
		}
		if d := end.Sub(t); d > 0 {
			v.DurationMs = d.Milliseconds()
		}
	}
	if j.LogPath != "" {
		if st, serr := os.Stat(j.LogPath); serr == nil && !st.IsDir() {
			v.LogExists = true
		}
	}
	return v
}
