package sshx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/db"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// 批量执行：把同一条命令并发地发到多台主机，逐台回传结果。
//
// 为什么不「同步等全部跑完再返回」：一批 20 台、每台 30 秒超时，最坏要几分钟，
// 前端只能干转圈——哪台连上了、哪台卡在哪一步，全是空白。所以这里是
// 「立即返回 + 逐台事件」：每台跑完推一条结果，前端边收边填表。
//
// 取消也只能靠 batchID 定位：单条 Exec 的取消靠调用方的 ctx，而批量一旦
// 立刻返回，发起者的调用栈就结束了，没有任何对象可以挂取消函数。
const (
	// EventSshBatchHost 单台主机执行完成，payload 是 SshBatchHostResult。
	EventSshBatchHost = "quickdock:ssh:batch-host"
	// EventSshBatchDone 整批结束（跑完或被取消），payload 是 SshBatchSummary。
	EventSshBatchDone = "quickdock:ssh:batch-done"
)

// 批量执行的边界。
const (
	// defaultBatchConcurrency 默认并发。
	//
	// 不能放开：每台主机是一条独立 TCP + 一次完整的密钥交换，20 台同时拨号
	// 在慢网络下会互相拖成全部超时，比串行还慢。6 是「明显快于串行、
	// 又不至于把自己的出口带宽打满」的经验值。
	defaultBatchConcurrency = 6
	maxBatchConcurrency     = 16

	// maxBatchHosts 单批主机数上限。
	//
	// 这不是性能上限而是防误操作：一口气选 200 台跑一条写操作，
	// 出事的面积远超用户点下去时的预期。真需要更大的规模，分批更安全。
	maxBatchHosts = 100

	// batchHostCap 单台输出保留上限（stdout 与 stderr 各自）。
	//
	// 比单条 Exec 的 1 MiB 小一个量级：批量场景下 20 台 × 2 MiB 的
	// 结果同时摆在界面上既读不完也吃不消，而「命令卡住时前几十行」
	// 已经足够定位问题。
	batchHostCap = 128 << 10
)

// SshBatchPlanRequest 批量执行的预演入参。
type SshBatchPlanRequest struct {
	HostIDs []string `json:"hostIds"`
	Command string   `json:"command"`
}

// SshBatchTarget 一台确定会参与执行的主机。
type SshBatchTarget struct {
	HostID string `json:"hostId"`
	Name   string `json:"name"`
	Addr   string `json:"addr"` // user@host:port，结果表里直接认人
	Env    string `json:"env"`  // prod | staging | test，原始值
}

// SshBatchSkipped 被跳过的主机及原因。
//
// 跳过而不是让整批失败：一批 10 台里有一台的凭据丢了，另外 9 台该跑还得跑。
// 但绝不能静默丢——用户以为命令发给了 10 台，实际只有 9 台收到，
// 这种「少了一台」的偏差在现场排查时极难发现。
type SshBatchSkipped struct {
	HostID string `json:"hostId"`
	Name   string `json:"name"`
	Reason string `json:"reason"` // 稳定错误码，前端映射成文案
	Detail string `json:"detail"`
}

// SshBatchPlan 一次批量执行的预演结果。
//
// 单独一个方法而不是塞进 Run 的返回值：前端必须在用户按下执行**之前**
// 就把风险说明和生产机数量摆出来（风险说明排在按钮之前是硬要求），
// 而那时 Run 还没被调用。
type SshBatchPlan struct {
	Hosts     []SshBatchTarget  `json:"hosts"`
	Skipped   []SshBatchSkipped `json:"skipped"`
	RiskLevel string            `json:"riskLevel"` // high | medium | ""
	RiskNote  string            `json:"riskNote"`
	ProdCount int               `json:"prodCount"`
}

// SshBatchRequest 执行入参。
//
// BatchID 由**前端生成并先登记**：事件可能在后端返回之前就发出来
// （第一台很快跑完时），前端只能靠一个自己已经知道的 id 过滤。
// 后端不会改写非空的 BatchID，只在为空时兜底生成。
type SshBatchRequest struct {
	BatchID     string   `json:"batchId"`
	HostIDs     []string `json:"hostIds"`
	Command     string   `json:"command"`
	TimeoutSec  int      `json:"timeoutSec"`
	Concurrency int      `json:"concurrency"`
}

// SshBatchStart 执行受理结果。
type SshBatchStart struct {
	BatchID string `json:"batchId"`
	Total   int    `json:"total"`
	Skipped int    `json:"skipped"`
}

// SshBatchHostResult 单台主机的执行结果。
//
// ErrorCode 与 ExitCode 的分工与单条 Exec 一致，但批量里更强调：
// 退出码非 0 是「命令跑了、结果是否定的」（grep 没匹配返回 1），
// ErrorCode 非空是「命令根本没跑完」（连接失败/超时/取消）。
// 结果表里这两类必须分开汇总，否则「3 台失败」到底是网络不通还是
// 命令返回非零，用户完全无从判断——而这两种情况的处置方式正相反。
type SshBatchHostResult struct {
	BatchID    string `json:"batchId"`
	HostID     string `json:"hostId"`
	Name       string `json:"name"`
	Addr       string `json:"addr"`
	Env        string `json:"env"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Truncated  bool   `json:"truncated"`
	ErrorCode  string `json:"errorCode"`
	Error      string `json:"error"`
}

// SshBatchSummary 整批汇总。
type SshBatchSummary struct {
	BatchID    string `json:"batchId"`
	Total      int    `json:"total"`     // 实际参与执行的主机数（不含跳过）
	Succeeded  int    `json:"succeeded"` // 跑完且退出码 0
	Failed     int    `json:"failed"`    // 跑完但退出码非 0
	Errored    int    `json:"errored"`   // 没跑完（连接失败/超时）
	Canceled   int    `json:"canceled"`  // 因取消而没执行
	Skipped    int    `json:"skipped"`
	Aborted    bool   `json:"aborted"` // 用户按了取消
	DurationMs int64  `json:"durationMs"`
}

// SshBatchPlan 预演：解析出真正可执行的主机、跳过项、命令风险等级。
//
// 前端可以在用户勾选主机/改命令时随时调用（纯只读，不建立任何连接）。
func (s *SshService) SshBatchPlan(req SshBatchPlanRequest) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return r
	}
	plan := s.resolveBatch(req.HostIDs)
	// 风险等级必须由后端给：规则表是唯一判定实现（见 sshDangerRule），
	// 前端自己匹配关键词迟早与后端漂移，届时界面说"低风险"而实际命中了 high。
	plan.RiskLevel, plan.RiskNote = sshDangerRule.Eval(req.Command)
	return services.Ok(plan)
}

// SshBatchRun 受理一批执行，立即返回 batchId，结果通过事件逐台推送。
func (s *SshService) SshBatchRun(req SshBatchRequest) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return r
	}
	if strings.TrimSpace(req.Command) == "" {
		return failSsh(sshcore.ErrEmptyCommand)
	}

	// 主机列表重新解析，**不信任前端传来的展示数据**：
	// 前端手里的是打开对话框那一刻的快照，期间主机可能被删、环境被改成生产。
	if len(req.HostIDs) > maxBatchHosts {
		return failCode(CodeInvalidInput, "单批主机数超过上限")
	}
	plan := s.resolveBatch(req.HostIDs)
	if len(plan.Hosts) == 0 {
		return failCode(CodeInvalidInput, "没有可执行的主机")
	}

	timeout := req.TimeoutSec
	if timeout <= 0 {
		timeout = defaultExecSec
	}
	if timeout > maxExecSec {
		timeout = maxExecSec
	}
	conc := req.Concurrency
	if conc <= 0 {
		conc = defaultBatchConcurrency
	}
	if conc > maxBatchConcurrency {
		conc = maxBatchConcurrency
	}
	if conc > len(plan.Hosts) {
		conc = len(plan.Hosts)
	}

	batchID := strings.TrimSpace(req.BatchID)
	if batchID == "" {
		batchID = newBatchID()
	}

	// 同 id 不允许并发：前端重复点击（双击、按钮没及时禁用）会开两批，
	// 事件流混在一起，界面上的行会来回跳。
	ctx, cancel := context.WithCancel(context.Background())
	s.batchMu.Lock()
	if s.batchCancels == nil {
		s.batchCancels = make(map[string]context.CancelFunc)
	}
	if _, busy := s.batchCancels[batchID]; busy {
		s.batchMu.Unlock()
		cancel()
		return failCode(CodeInvalidInput, "该批次已在执行")
	}
	s.batchCancels[batchID] = cancel
	s.batchMu.Unlock()

	s.noteBatchActivity(plan, req.Command)

	go s.runBatch(ctx, batchID, plan, req.Command, time.Duration(timeout)*time.Second, conc)

	return services.Ok(SshBatchStart{
		BatchID: batchID,
		Total:   len(plan.Hosts),
		Skipped: len(plan.Skipped),
	})
}

// SshBatchCancel 取消一批正在执行的命令（对已结束的批次是空操作）。
func (s *SshService) SshBatchCancel(batchID string) *services.ApiResult {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return failCode(CodeInvalidInput, "批次 id 不能为空")
	}
	s.batchMu.Lock()
	cancel, ok := s.batchCancels[batchID]
	s.batchMu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
	// 找不到也返回成功：批次可能刚好在这一刻自然结束了，
	// 此时报错只会让用户看到一个没有意义的红字。
	return services.Ok(nil)
}

// ---- 内部 ----

// resolveBatch 把前端传来的 id 列表解析成可执行目标 + 跳过项。
func (s *SshService) resolveBatch(hostIDs []string) SshBatchPlan {
	plan := SshBatchPlan{
		Hosts:   make([]SshBatchTarget, 0, len(hostIDs)),
		Skipped: make([]SshBatchSkipped, 0),
	}
	seen := make(map[string]bool, len(hostIDs))
	for _, raw := range hostIDs {
		id := strings.TrimSpace(raw)
		// 去重：树上的多选、全选分组都可能把同一台带进来两次，
		// 不去重就是同一台机器跑两遍——写操作跑两遍的后果不必细说。
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		host, _, err := s.loadHost(id)
		if err != nil {
			code, _ := classifySshError(err)
			item := SshBatchSkipped{HostID: id, Reason: code, Detail: err.Error()}
			// loadHost 失败时拿不到名称，单独补一次（只在错误路径上，
			// 不影响正常流程；缺名字的话跳过列表就只剩一串 id）。
			if h, gerr := s.App.DB.GetSshHost(id); gerr == nil && h != nil {
				item.Name = h.Name
			}
			plan.Skipped = append(plan.Skipped, item)
			continue
		}

		if host.Env == db.SshEnvProd {
			plan.ProdCount++
		}
		plan.Hosts = append(plan.Hosts, SshBatchTarget{
			HostID: host.ID,
			Name:   host.Name,
			Addr:   host.User + "@" + host.Host + ":" + strconv.Itoa(host.Port),
			Env:    host.Env,
		})
	}
	return plan
}

// runBatch 并发跑完整批，每台完成即推事件，最后推汇总。
func (s *SshService) runBatch(ctx context.Context, batchID string, plan SshBatchPlan, cmd string, timeout time.Duration, conc int) {
	start := time.Now()
	var mu sync.Mutex
	var succeeded, failed, errored, canceled int

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	defer func() {
		wg.Wait()
		s.batchMu.Lock()
		delete(s.batchCancels, batchID)
		s.batchMu.Unlock()
		s.emitBatchDone(SshBatchSummary{
			BatchID:    batchID,
			Total:      len(plan.Hosts),
			Succeeded:  succeeded,
			Failed:     failed,
			Errored:    errored,
			Canceled:   canceled,
			Skipped:    len(plan.Skipped),
			Aborted:    ctx.Err() != nil,
			DurationMs: time.Since(start).Milliseconds(),
		})
	}()

	for _, t := range plan.Hosts {
		// 每台之前先看一次是否已取消。没有这一步，取消之后仍在拨号的主机
		// 会继续占着并发槽，用户看到的是「按了取消还在连」。
		if ctx.Err() != nil {
			mu.Lock()
			canceled++
			mu.Unlock()
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			canceled++
			mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(t SshBatchTarget) {
			defer wg.Done()
			defer func() { <-sem }()

			res := s.execOnBatchHost(ctx, batchID, t, cmd, timeout)
			s.emitBatchHost(res)

			mu.Lock()
			switch {
			case res.ErrorCode != "":
				errored++
			case res.ExitCode == 0:
				succeeded++
			default:
				failed++
			}
			mu.Unlock()
		}(t)
	}
}

// execOnBatchHost 在一台主机上跑一次命令。返回值的 ErrorCode 非空表示命令没跑完。
func (s *SshService) execOnBatchHost(ctx context.Context, batchID string, t SshBatchTarget, cmd string, timeout time.Duration) (res SshBatchHostResult) {
	res = SshBatchHostResult{
		BatchID: batchID,
		HostID:  t.HostID,
		Name:    t.Name,
		Addr:    t.Addr,
		Env:     t.Env,
	}
	start := time.Now()
	// 具名返回值 + defer：每条 return 分支都要带耗时，写四遍不如写一遍。
	defer func() { res.DurationMs = time.Since(start).Milliseconds() }()

	if ctx.Err() != nil {
		res.ErrorCode = CodeBatchCanceled
		res.Error = "已取消"
		return
	}

	host, secret, err := s.loadHost(t.HostID)
	if err != nil {
		res.ErrorCode, res.Error = batchErrCode(ctx, err)
		return
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
	client, err := s.dial(dialCtx, host, secret)
	cancelDial()
	if err != nil {
		res.ErrorCode, res.Error = batchErrCode(ctx, err)
		return
	}
	defer client.Close()

	execCtx, cancelExec := context.WithTimeout(ctx, timeout)
	defer cancelExec()

	out, err := client.Exec(execCtx, cmd)
	res.Stdout = clipBatchOutput(out.Stdout)
	res.Stderr = clipBatchOutput(out.Stderr)
	res.ExitCode = out.ExitCode
	res.Truncated = out.Truncated || len(out.Stdout) > batchHostCap || len(out.Stderr) > batchHostCap
	if err != nil {
		res.ErrorCode, res.Error = batchErrCode(ctx, err)
	}
	return
}

// batchErrCode 收敛批量场景的错误码。
//
// 先看批次的 ctx：取消会让 Exec/dial 返回 context.Canceled，
// 而 classifySshError 把 context.Canceled 归给了 CodeTransferCanceled
// （原本是给 SFTP 传输取消用的）。直接沿用会让结果表把「我按了取消」
// 标成「传输被取消」，文案完全对不上，所以这里先拦一道。
func batchErrCode(ctx context.Context, err error) (string, string) {
	if ctx.Err() != nil {
		return CodeBatchCanceled, "已取消"
	}
	code, _ := classifySshError(err)
	return code, err.Error()
}

// clipBatchOutput 把单台输出裁到 batchHostCap，保留头部。
//
// 保头部而不是尾部：批量场景看的是「命令一上来就报了什么错」，
// 与终端日志相反（那边关心最后几行）。裁剪处必须留可见标记，
// 否则用户会以为命令真的只输出了这么多。
func clipBatchOutput(s string) string {
	if len(s) <= batchHostCap {
		return s
	}
	// 按字节切可能切坏 UTF-8 多字节字符，回退到合法边界。
	cut := batchHostCap
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n… (输出超过上限，已截断)"
}

// noteBatchActivity 记一条「批量执行」动态。
//
// 只记命令不记输出：流水是「做过什么」，不是命令回执。
// 命令本身有截断——超长的一行脚本塞进时间线没有可读性。
func (s *SshService) noteBatchActivity(plan SshBatchPlan, cmd string) {
	if s.App == nil {
		return
	}
	label := strings.TrimSpace(cmd)
	if r := []rune(label); len(r) > 60 {
		label = string(r[:60]) + "…"
	}
	s.App.NoteActivity(db.ActivitySshBatch, "", label)
}

func (s *SshService) emitBatchHost(res SshBatchHostResult) {
	if s.App == nil {
		return
	}
	if a := s.App.App(); a != nil {
		a.Event.Emit(EventSshBatchHost, res)
	}
}

func (s *SshService) emitBatchDone(sum SshBatchSummary) {
	if s.App == nil {
		return
	}
	if a := s.App.App(); a != nil {
		a.Event.Emit(EventSshBatchDone, sum)
	}
}

// cancelBatches 取消全部在跑的批次（应用退出时调用）。
func (s *SshService) cancelBatches() {
	s.batchMu.Lock()
	cancels := s.batchCancels
	s.batchCancels = make(map[string]context.CancelFunc)
	s.batchMu.Unlock()
	for _, c := range cancels {
		if c != nil {
			c()
		}
	}
}

// newBatchID 后端兜底生成的批次 id（正常情况下由前端提供）。
func newBatchID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用极罕见，用时间戳退化即可——它只需要在本进程内唯一。
		return "batch-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "batch-" + hex.EncodeToString(b[:])
}
