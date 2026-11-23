package script

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/scriptrun"
	"quickdock/services"
)

// 触发来源。写进 script_runs.trigger_kind，也是「要不要发通知」的判据。
const (
	TriggerManual = "manual"
	TriggerCron   = "cron"
	TriggerHTTP   = "http"
	TriggerMCP    = "mcp"
	TriggerAI     = "ai"
	TriggerEvent  = "event"
	// TriggerDeps 装依赖：它不是脚本运行，但走同一套 run 记录与日志，
	// 于是前端一套历史/日志组件就够，不必再画一个「安装输出」弹窗。
	TriggerDeps = "deps"
)

// runLogBatcher 把逐行输出合成 100ms 一批推给前端。
//
// 逐行 emit 是错的：一个每秒打一千行的抓取脚本会让 Wails 事件通道彻底堵死，
// 界面卡到连「停止」按钮都点不动。合批后前端一次拿几十行，渲染也更省。
type runLogBatcher struct {
	svc      *ScriptService
	runID    string
	scriptID string

	mu  sync.Mutex
	buf []map[string]string
}

// logBatchMax 单批最大行数：超过就立刻 flush，别等下一个 tick。
const logBatchMax = 300

func (b *runLogBatcher) add(stream, text string) {
	b.mu.Lock()
	b.buf = append(b.buf, map[string]string{"stream": stream, "text": text})
	need := len(b.buf) >= logBatchMax
	b.mu.Unlock()
	if need {
		b.flush()
	}
}

func (b *runLogBatcher) flush() {
	b.mu.Lock()
	if len(b.buf) == 0 {
		b.mu.Unlock()
		return
	}
	lines := b.buf
	b.buf = nil
	b.mu.Unlock()
	b.svc.emit(EventLog, map[string]any{
		"runId":    b.runID,
		"scriptId": b.scriptID,
		"lines":    lines,
	})
}

// RunScript 手动运行（前端按钮 / 命令面板）。
func (s *ScriptService) RunScript(id, argsJSON string) *services.ApiResult {
	return s.RunScriptWithTrigger(id, argsJSON, TriggerManual)
}

// launch 建立一条运行记录并占住一个并发实例位。
func (s *ScriptService) launch(sc *db.Script, trigger string) (*db.ScriptRun, context.Context, context.CancelFunc, error) {
	if trigger == "" {
		trigger = TriggerManual
	}
	// 外部导入的脚本在第一次手动运行前要用户确认（脚本 = 任意代码执行）。
	// 只拦手动：定时/HTTP/MCP 都是用户为这个脚本显式配过的入口，说明他知道
	// 自己在跑什么；在无人值守的凌晨弹一个「请确认」只会变成一条失败记录。
	if trigger == TriggerManual && !s.scriptTrusted(sc.ID) {
		return nil, nil, nil, errUntrustedScript
	}
	// 并发闸门。计数在锁内做「检查 + 自增」，两个点击几乎同时到达时不会都放行。
	if !s.gate.acquire(sc.ID, sc.Concurrency) {
		if sc.Concurrency > 1 {
			return nil, nil, nil, fmt.Errorf("该脚本已有 %d 个实例在跑（上限 %d）", s.gate.count(sc.ID), sc.Concurrency)
		}
		return nil, nil, nil, errAlreadyRunning
	}
	run := &db.ScriptRun{
		ID:         newRunID(),
		ScriptID:   sc.ID,
		ScriptName: sc.Name,
		Trigger:    trigger,
		Status:     "running",
	}
	run.LogPath = scriptrun.LogPath(sc.ID, run.ID)
	// 日志路径算不出来（日志目录未初始化）不该让运行失败：那只是少了归档，
	// 实时输出仍然可用。
	if err := s.App.DB.InsertScriptRun(run); err != nil {
		s.gate.release(sc.ID)
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancels.Store(run.ID, cancel)
	return run, ctx, cancel, nil
}

// RunScriptWithTrigger 指定触发来源运行，返回 runId（不等待结束）。
//
// 异步而不是同步等待：脚本可能跑几十分钟，Wails binding 挂在那里既会让界面
// 转圈，也无法在中途取消。前端拿 runId 订阅事件即可。
func (s *ScriptService) RunScriptWithTrigger(id, argsJSON, trigger string) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	sc, err := s.App.DB.GetScript(id)
	if err != nil {
		return services.Fail(err)
	}
	args := defaultArgsSlice(sc.DefaultArgs)
	if strings.TrimSpace(argsJSON) != "" {
		var override []string
		if err := json.Unmarshal([]byte(argsJSON), &override); err != nil {
			return services.FailMsg("参数必须是一个 JSON 字符串数组，如 [\"--days\",\"7\"]")
		}
		if len(override) > 0 {
			args = override
		}
	}

	run, ctx, cancel, err := s.launch(sc, trigger)
	if err != nil {
		return services.Fail(err)
	}
	s.emit(EventRun, map[string]any{
		"runId": run.ID, "scriptId": sc.ID, "status": "running", "trigger": run.Trigger,
	})

	go func() {
		defer func() {
			cancel()
			s.cancels.Delete(run.ID)
			s.gate.release(sc.ID)
		}()
		started := time.Now()
		status, exitCode, dur, summary := s.execute(ctx, sc, run, args, nil, nil, started)
		s.finishRun(sc, run, status, exitCode, dur, summary, started)
	}()

	return services.Ok(map[string]any{"runId": run.ID, "logPath": run.LogPath})
}

// RunScriptSyncCtx 同步运行，供定时任务使用（它要拿结果写 schedule_runs 并决定重试）。
//
// 父 ctx 取消时必须杀掉脚本进程：定时任务的「立即执行 + 取消」按钮走的是
// 调度器那条 ctx，不跟脚本自己的 cancels 表打通，取消就成了摆设。
func (s *ScriptService) RunScriptSyncCtx(ctx context.Context, id, trigger string) (string, string, error) {
	if s.App.DB == nil {
		return "fail", "数据库未就绪", fmt.Errorf("数据库未就绪")
	}
	sc, err := s.App.DB.GetScript(id)
	if err != nil {
		return "fail", "脚本不存在：" + err.Error(), err
	}
	args := defaultArgsSlice(sc.DefaultArgs)

	run, runCtx, cancel, err := s.launch(sc, trigger)
	if err != nil {
		// 并发冲突对定时任务来说不是「失败」而更像「跳过」：上一轮还没跑完，
		// 这一轮直接让位。返回 ok 会让「上次结果」显示成功，误导性更强，
		// 故仍按 fail 记，但摘要写清是被跳过。
		return "fail", err.Error(), err
	}
	// 父 ctx 取消 → 子 ctx 取消 → StreamCommand 杀进程。
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-stopWatch:
		}
	}()
	defer func() {
		close(stopWatch)
		cancel()
		s.cancels.Delete(run.ID)
		s.gate.release(sc.ID)
	}()

	started := time.Now()
	status, exitCode, dur, summary := s.execute(runCtx, sc, run, args, nil, nil, started)
	s.finishRun(sc, run, status, exitCode, dur, summary, started)
	return status, summary, nil
}

// newRunID 生成运行 id。用时间前缀 + 随机后缀：日志文件按它命名，
// 按文件名排序天然就是时间序，排查时不用先查库。
func newRunID() string {
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), randSuffix())
}

// randSuffix 6 位随机后缀（同一秒内多次触发不撞名）。
func randSuffix() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
}

// execute 一次完整运行（含可选的自动装依赖），返回 (status, exitCode, 耗时, 摘要)。
//
// 不做 finishRun：异步与同步两条路的收尾时机不同（同步那条要先把结果交回
// 调度器），但「怎么跑」必须只有一份实现，否则两边迟早跑出不一样的语义。
func (s *ScriptService) execute(ctx context.Context, sc *db.Script, run *db.ScriptRun, args []string,
	prefix []string, sink scriptrun.Sink, started time.Time) (string, int, time.Duration, string) {

	batcher := &runLogBatcher{svc: s, runID: run.ID, scriptID: sc.ID}
	combined := func(stream, line string) {
		batcher.add(stream, line)
		if sink != nil {
			sink(stream, line)
		}
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				batcher.flush()
			case <-stop:
				batcher.flush()
				return
			}
		}
	}()
	defer close(stop)

	interp, err := scriptrun.Resolve(scriptrun.Spec{
		Lang:        sc.Lang,
		Interpreter: s.interpreterFor(sc),
	}, s.locateRuntime)
	if err != nil {
		batcher.add("stderr", "解释器解析失败："+err.Error())
		return "fail", -1, time.Since(started), "解释器解析失败：" + err.Error()
	}

	envList, envErr := s.buildEnv(sc, run.ID)
	if envErr != nil {
		// 变量组装失败不该静默丢掉——那会让脚本用一个「看起来配了但没生效」
		// 的环境跑，结果对不上还查不出原因。
		batcher.add("stderr", "环境变量组装失败："+envErr.Error())
	}

	// 自动装依赖：只在有依赖文件、且没有成功标记时做一次。
	// 标记文件放在脚本目录里（用户看得见），而不是藏进应用数据目录。
	if sc.AutoInstall && prefix == nil {
		if deps := s.detectDeps(sc); deps.FileName != "" {
			if deps.Stale {
				batcher.add("stdout", "# 依赖文件有变化，先安装："+deps.FileName)
				if ok := s.runDeps(ctx, sc, run, batcher); !ok {
					return "fail", -1, time.Since(started), "依赖安装失败，已中止运行"
				}
				s.markDepsInstalled(sc)
			}
		}
	}

	spec := scriptrun.Spec{
		ID:          sc.ID,
		Name:        sc.Name,
		Lang:        sc.Lang,
		Entry:       sc.Entry,
		Interpreter: sc.Interpreter,
		Dir:         s.dir(sc.ID),
		Args:        args,
		Prefix:      prefix,
		Env:         envList,
		Timeout:     time.Duration(sc.TimeoutSec) * time.Second,
	}
	res := scriptrun.Run(ctx, spec, interp, run.LogPath, combined)
	return res.Status, res.ExitCode, res.Duration, res.Summary
}

// interpreterFor 解析脚本里填的自定义解释器。
//
// 用户写相对路径（.venv/Scripts/python.exe、./py311/python.exe）是最自然的——
// 尤其是想给单个脚本隔离依赖时。但 exec.LookPath 是按**应用进程**的工作目录
// 解析的，跟脚本目录无关，直接交给 Resolve 只会得到「找不到解释器」。
// 这里先按脚本目录试一次，命中就换成绝对路径；否则原样返回（裸命令名照旧走 PATH）。
func (s *ScriptService) interpreterFor(sc *db.Script) string {
	v := strings.TrimSpace(sc.Interpreter)
	if v == "" || filepath.IsAbs(v) {
		return v
	}
	p := filepath.Join(s.dir(sc.ID), filepath.FromSlash(v))
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		return p
	}
	return v
}

// runDeps 装依赖，输出并入同一次运行的日志。返回是否成功。
//
// 四种语言四种安装器，但「怎么执行、日志往哪写、失败怎么判」完全一致——
// 都走 scriptrun.Run，所以这里只需要把「命令长什么样」拼出来。
func (s *ScriptService) runDeps(ctx context.Context, sc *db.Script, run *db.ScriptRun, batcher *runLogBatcher) bool {
	deps := s.detectDeps(sc)
	if deps.FileName == "" {
		return true
	}
	dir := s.dir(sc.ID)
	spec := scriptrun.Spec{Dir: dir, Name: "install deps"}
	switch {
	case deps.Python:
		spec.Lang = scriptrun.LangPython
		// 必须沿用脚本自己指定的解释器：用户在「解释器」里填了 venv 的 python
		// 就是为了让依赖装进那个环境。这里不传的话会装到便携运行时/系统 Python，
		// 脚本一跑还是 ModuleNotFoundError —— 而且日志显示「安装完成」，极难排查。
		spec.Interpreter = s.interpreterFor(sc)
		spec.Prefix = []string{"-m", "pip", "install", "-r", "requirements.txt"}
	case deps.Node:
		npm, err := s.resolveNpm()
		if err != nil {
			batcher.add("stderr", "找不到 npm："+err.Error())
			return false
		}
		spec.Lang = scriptrun.LangCustom
		spec.Interpreter = npm
		spec.Prefix = []string{"install"}
	case deps.Go:
		// go mod tidy 而不是 go get：它按源码里实际的 import 收敛依赖，
		// 顺手删掉不再用的、把缺的补进 go.mod/go.sum。副作用是会改写 go.mod，
		// 但这本来就是「安装依赖」应当做的事。
		spec.Lang = scriptrun.LangGo
		spec.Interpreter = s.interpreterFor(sc)
		spec.Prefix = []string{"mod", "tidy"}
	case deps.PHP:
		phpExe, composerPrefix, err := s.composerInvocation(sc)
		if err != nil {
			batcher.add("stderr", "找不到 composer："+err.Error())
			return false
		}
		// composer 走 custom：exe 可能是 php（后跟 composer.phar），也可能是
		// composer.bat 本身，两者都只是「解释器 + 前置参数」的差别。
		spec.Lang = scriptrun.LangCustom
		spec.Interpreter = phpExe
		// --no-interaction：没有 TTY，composer 一提问就永远挂着等输入，
		// 表现为「安装依赖」跑到超时才结束。--no-progress 免把进度条
		// 刷进日志（一次几百行 \r）。
		spec.Prefix = append(composerPrefix, "install", "--no-interaction", "--no-progress")
	default:
		return true
	}
	interp, err := scriptrun.Resolve(spec, s.locateRuntime)
	if err != nil {
		batcher.add("stderr", "依赖安装器解析失败："+err.Error())
		return false
	}
	spec.Env, _ = s.buildEnv(sc, run.ID)
	// 装依赖给 15 分钟上限：pip 在慢网下装 torch 能跑十分钟以上，但也不能
	// 无限——它和「运行脚本」抢同一个并发闸门。
	spec.Timeout = 15 * time.Minute
	res := scriptrun.Run(ctx, spec, interp, run.LogPath, batcher.add)
	return res.Status == "ok"
}

// composerInvocation 决定怎么调 composer：返回可执行文件与它需要的前置参数。
//
// 托管 Composer 只是 composer.phar（不是可执行文件），必须让 php 去解释它，
// 所以第一种形态是 (php, [composer.phar])；系统 PATH 上的 composer 通常是个
// 批处理/脚本包装（Windows 是 composer.bat），直接当可执行文件跑即可。
func (s *ScriptService) composerInvocation(sc *db.Script) (string, []string, error) {
	if dir := s.composerBinDir(); dir != "" {
		if phar := platform.ResolveExeIn(dir, "composer.phar"); phar != "" {
			php, err := scriptrun.Resolve(scriptrun.Spec{
				Lang:        scriptrun.LangPHP,
				Interpreter: s.interpreterFor(sc),
			}, s.locateRuntime)
			if err != nil {
				return "", nil, fmt.Errorf("已装 Composer 但没有可用的 PHP：%w", err)
			}
			return php.Exe, []string{phar}, nil
		}
	}
	for _, n := range []string{"composer.bat", "composer"} {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil, nil
		}
	}
	return "", nil, fmt.Errorf("请在「环境管理」安装 Composer（需要 PHP），或系统 PATH 中装有 composer")
}

// resolveNpm npm 是 .cmd（Windows），不能按普通 exe 探测——拿到脚本文件
// 直接 Start 会报「不是有效的 Win32 应用程序」。
func (s *ScriptService) resolveNpm() (string, error) {
	if dir := s.locateRuntime(scriptrun.LangJavaScript); dir != "" {
		if p := platform.ResolveExeIn(dir, "npm.cmd", "npm"); p != "" {
			return p, nil
		}
	}
	for _, n := range []string{"npm.cmd", "npm"} {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 npm，请到「环境管理」安装 Node.js")
}

// finishRun 收尾：写库、推事件、发通知、（可选）触发 AI 自愈。
func (s *ScriptService) finishRun(sc *db.Script, run *db.ScriptRun, status string, exitCode int,
	dur time.Duration, summary string, started time.Time) {
	ms := dur.Milliseconds()
	if err := s.App.DB.FinishScriptRun(run.ID, status, exitCode, ms, summary); err != nil {
		logger.W("[script] 写运行结果失败 %s: %v", run.ID, err)
	}

	s.emit(EventRun, map[string]any{
		"runId": run.ID, "scriptId": sc.ID, "status": status,
		"exitCode": exitCode, "durationMs": ms, "summary": summary,
		"trigger": run.Trigger, "logPath": run.LogPath,
	})

	s.notify(sc, run, status, summary, dur)

	// 历史裁剪：每脚本留最近 200 条，日志文件同步删掉。
	// 不做的话每分钟跑的任务一年就是 50 万行 + 50 万个日志文件。
	if stale, err := s.App.DB.PruneScriptRuns(sc.ID, 200); err == nil {
		for _, r := range stale {
			if r.LogPath != "" {
				_ = os.Remove(r.LogPath)
			}
		}
	}

	// AI 自愈：只有失败、且不是手动运行时才自动分析。手动运行失败时用户
	// 就在屏幕前，直接看日志比等 AI 三十秒更有用。
	if status != "ok" && status != "canceled" && run.Trigger != TriggerManual && run.Trigger != TriggerDeps {
		go s.healRun(run.ID)
	}
}

// notify 通知策略。
//
// 刻意不是「成功也推」：每分钟跑一次的脚本会把通知中心刷成一堵墙，
// 真正出事那条反而被埋掉。规则：
//   - manual / deps：不发（用户就在屏幕前，结果就在眼前）
//   - 其它触发成功：不发
//   - 任何触发失败：通知中心 + 桌面气泡
func (s *ScriptService) notify(sc *db.Script, run *db.ScriptRun, status, summary string, dur time.Duration) {
	if status == "ok" || run.Trigger == TriggerManual || run.Trigger == TriggerDeps {
		return
	}
	// 连续失败要收敛，不能每轮都发：网断了的凌晨，每分钟一次的任务会在一夜
	// 之间堆四百条一模一样的通知，把真正需要人看的那条埋掉。规则是「第一条
	// 必发（说明开始坏了），之后 3 / 5 / 10 次提醒一次，20 次起每 10 次」。
	if streak := s.failStreak(sc.ID); streak > 1 && streak < 20 &&
		streak != 3 && streak != 5 && streak != 10 {
		return
	}
	title := sc.Name + " 运行失败"
	switch status {
	case "timeout":
		title = sc.Name + " 运行超时"
	case "canceled":
		title = sc.Name + " 运行已取消"
	}
	streak := s.failStreak(sc.ID)
	body := fmt.Sprintf("%s\n耗时 %s", summary, dur.Round(time.Millisecond))
	if streak > 1 {
		body += fmt.Sprintf("\n已连续失败 %d 次", streak)
	}
	s.App.PushNotification("script", "warn", title, body, "script", sc.ID)
	s.App.NotifyDesktop("script-"+run.ID, title, body)
}

// failStreak 最近连续失败次数（含本次）。
//
// 只看最近 30 条：够判断「是不是一直挂」，又不会为一条通知扫全表。
// 取消（canceled）不算失败——那多半是用户自己点的停止，把它算进连续失败
// 会让「停一下再重跑」的调试过程立刻触发出一个「连续失败」告警。
func (s *ScriptService) failStreak(scriptID string) int {
	runs, err := s.App.DB.ListScriptRuns(scriptID, 30)
	if err != nil {
		return 1
	}
	n := 0
	for _, r := range runs {
		if r.Status == "ok" || r.Status == "canceled" || r.Status == "running" {
			break
		}
		n++
	}
	if n == 0 {
		n = 1
	}
	return n
}

// CancelScriptRun 停止正在跑的脚本。
func (s *ScriptService) CancelScriptRun(runID string) *services.ApiResult {
	if v, ok := s.cancels.Load(runID); ok {
		v.(context.CancelFunc)()
		return services.Ok(nil)
	}
	return services.FailMsg("该运行已结束或不存在")
}

// ListScriptRuns 运行历史（scriptID 空 = 全部脚本）。
func (s *ScriptService) ListScriptRuns(scriptID string, limit int) *services.ApiResult {
	list, err := s.App.DB.ListScriptRuns(scriptID, limit)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(list)
}

// GetScriptRunLog 读某次运行的日志尾部。
func (s *ScriptService) GetScriptRunLog(runID string, maxBytes int64) *services.ApiResult {
	run, err := s.App.DB.GetScriptRun(runID)
	if err != nil {
		return services.Fail(err)
	}
	if run.LogPath == "" {
		return services.Ok(map[string]any{"content": "", "path": ""})
	}
	if maxBytes <= 0 {
		maxBytes = 256 << 10
	}
	content, err := scriptrun.ReadTail(run.LogPath, maxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return services.Ok(map[string]any{"content": "", "path": run.LogPath, "missing": true})
		}
		return services.Fail(err)
	}
	return services.Ok(map[string]any{"content": content, "path": run.LogPath, "run": run})
}

// GetScriptStats 成功率与耗时统计。
func (s *ScriptService) GetScriptStats(id string) *services.ApiResult {
	st, err := s.App.DB.GetScriptStats(id)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(st)
}

// InstallDeps 手动装依赖（按钮）。和自动装共用 runDeps，单独占一条 run 记录。
func (s *ScriptService) InstallDeps(id string) *services.ApiResult {
	sc, err := s.App.DB.GetScript(id)
	if err != nil {
		return services.Fail(err)
	}
	if s.detectDeps(sc).FileName == "" {
		return services.FailMsg("脚本目录里没有依赖文件（requirements.txt / package.json / go.mod / composer.json）")
	}
	run, ctx, cancel, err := s.launch(sc, TriggerDeps)
	if err != nil {
		return services.Fail(err)
	}
	s.emit(EventRun, map[string]any{"runId": run.ID, "scriptId": sc.ID, "status": "running", "trigger": TriggerDeps})
	go func() {
		defer func() {
			cancel()
			s.cancels.Delete(run.ID)
			s.gate.release(sc.ID)
		}()
		started := time.Now()
		batcher := &runLogBatcher{svc: s, runID: run.ID, scriptID: sc.ID}
		ok := s.runDeps(ctx, sc, run, batcher)
		batcher.flush()
		status, summary := "ok", "依赖安装完成"
		if !ok {
			status, summary = "fail", "依赖安装失败，详情见日志"
		} else {
			// 手动装完也要打标记：否则下次运行发现指纹对不上又要重装一遍。
			s.markDepsInstalled(sc)
		}
		dur := time.Since(started)
		s.finishRun(sc, run, status, 0, dur, summary, started)
	}()
	return services.Ok(map[string]any{"runId": run.ID, "logPath": run.LogPath})
}
