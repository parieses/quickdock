package aitools

// 后台任务：run_command(background=true) 起的临时进程。
//
// 为什么要有它：跑 dev server、跑一次长构建、起个本地服务再去看页面——这些
// 命令的特点都是「不会自己结束」。同步等待只能等超时（60s 起步、上限 600s），
// 等到了也只是一句「超时已被杀」，而用户本来只是想让它一直在那儿跑。
//
// 设计上的三条底线：
//  1. **关闭 QuickDock 会终止任务**。它是随宿主活的临时进程，不是守护进程。
//     这一点必须写在工具描述和结果里——不写，用户一定以为关掉窗口它还在跑。
//  2. 输出进文件不进库（<dataDir>/ai_jobs/<id>.log）：一条构建的输出可以几十 MB。
//  3. 进程独立成组，退出/超时都整组杀：`npm run dev` 退出后留下的 node
//     会一直占着端口，而那是最难排查的一类故障。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

const (
	aiJobDirName = "ai_jobs"
	// 单次 job_output 最多回多少字节：模型看的是「跑到哪一步了」，
	// 不是把整份构建日志读进上下文——一份几十 MB 的日志会直接把窗口打满。
	aiJobOutputMax = 16 << 10
	// 任务与日志的保留天数（与回收站同一个量级）。
	aiJobMaxAgeDays = 7
)

// jobProc 一个仍在运行的进程。
type jobProc struct {
	cmd *exec.Cmd
	dir string
	// command 原始命令文本：写通知、写日志头都要用；只留在 argv 里的话
	// 拿到的是 cmd.exe 那层包装（"/d /s /c ..."），对用户没有意义。
	command string
	// startedAt 启动时刻，用来算时长。
	startedAt time.Time
	// col 起任务时就抓住的收集器（见 jobCollectorFrom）：任务结束时它早已被
	// cancel 过，只有提前抓住才能在结束时把通知递回本轮。
	col *JobCollector
	// killedByUs 由宿主主动终止（退出、清理）时置位：用来把「被杀」和
	// 「命令自己失败」区分开，两者的结论完全不同。
	killedByUs bool
}

var (
	jobsMu    sync.Mutex
	jobsProcs = map[string]*jobProc{}
)

// jobDir 后台任务日志目录（不存在则创建）。
func jobDir() string {
	dir := filepath.Join(platform.DefaultDataDir(), aiJobDirName)
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// jobLogPath 一个任务的日志路径。
func jobLogPath(id string) string {
	return filepath.Join(jobDir(), id+".log")
}

// newJobID 生成任务 id。时间戳在前，列表按 id 倒序就是按时间倒序。
func newJobID() string {
	return "job_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + strconv.Itoa(os.Getpid())
}

// startBackgroundCommand 起一个后台进程并立刻返回。
//
// 返回值里带着 job_id 与 log_path：模型要用它们调 job_status / job_output，
// 用户要能自己去看日志。
func startBackgroundCommand(ctx context.Context, d Deps, command, dir string) (map[string]any, error) {
	database := d.db()
	if database == nil {
		// 拿不到库就没法落行，也就没法让用户看见/停掉这个任务。此时不退回
		// 「照样起进程」：那样会留下一个谁都不知道它存在的常驻进程。
		// 也明确挡住「自己写 detached 启动脚本绕过去」这条路——绕过去的任务
		// 不进面板、不受退出清理，用户以为关掉就没了（2026-11-02 就发生过）。
		return nil, errors.New("数据库不可用，无法记录后台任务；不要改用 detached 进程或 nohup 绕过，" +
			"那样起的进程用户看不到也停不掉，请把情况说明给用户")
	}
	id := newJobID()
	logPath := jobLogPath(id)

	// 先落库再起进程：进程如果秒退，它的状态更新会在「还没有这一行」时执行，
	// 影响 0 行，库里就永远留着一个 running 的幽灵任务。
	if err := database.CreateAIJob(db.AIJob{
		ID: id, Command: command, Cwd: dir, Status: db.AIJobRunning, LogPath: logPath, ConvID: ConvIDFrom(ctx),
	}); err != nil {
		return nil, err
	}

	f, ferr := os.Create(logPath)
	if ferr != nil {
		_ = database.FinishAIJob(id, db.AIJobFailed, 0, "无法创建日志文件："+ferr.Error())
		return nil, fmt.Errorf("无法创建日志文件：%v", ferr)
	}
	// 头部写清楚这条日志属于谁：日志文件会被用户直接打开，没有这一行就分不清
	// 「这是哪条命令的输出」。
	fmt.Fprintf(f, "# %s\n# cwd: %s\n# started: %s\n# 关闭 QuickDock 会终止这个任务\n\n",
		command, dir, time.Now().Format(time.RFC3339))

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
		cmd = sysutil.Command(shell, "/d", "/s", "/c", command)
	} else {
		cmd = sysutil.Command("/bin/sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Env = commandEnv(d)
	// 独立进程组：退出与清理时能整组杀掉，而不是留下 npm→node 这样的孙子进程。
	setProcessGroup(cmd)
	cmd.Stdout, cmd.Stderr = f, f

	if err := cmd.Start(); err != nil {
		f.Close()
		_ = database.FinishAIJob(id, db.AIJobFailed, 0, "无法启动命令："+err.Error())
		return nil, fmt.Errorf("无法启动命令：%v", err)
	}
	_ = database.SetAIJobPID(id, cmd.Process.Pid)

	p := &jobProc{cmd: cmd, dir: dir, command: command, startedAt: time.Now(), col: jobCollectorFrom(ctx)}
	jobsMu.Lock()
	jobsProcs[id] = p
	jobsMu.Unlock()

	go waitBackgroundJob(d, id, p, f)
	// 登记到本轮：用户点停止时，runAgent 会按这份名单把任务一起收掉。
	// 不登记的话，一个 background=true 的全盘检索在停止之后还会跑到天荒地老，
	// 而界面上没有任何地方能停它。
	recordJob(ctx, id)

	logger.I("[ai-job] 后台任务 %s 已启动 pid=%d: %s", id, cmd.Process.Pid, command)
	res := map[string]any{
		"jobId":      id,
		"status":     db.AIJobRunning,
		"cwd":        dir,
		"command":    command,
		"logPath":    logPath,
		"background": true,
		"note": "任务已在后台启动并立刻返回。**接着干别的事**：它结束时会有通知送到你这里，" +
			"不要 sleep 或反复 job_status 轮询（每查一次都是一次完整的模型往返）；" +
			"用户问起进度时用 job_output 看最新输出。" +
			"**关闭 QuickDock 会终止这个任务**——它不是守护进程，随宿主一起结束。" +
			"需要一条不会随应用退出的长期服务时，请让用户用系统服务 / 计划任务自己配。",
	}
	return res, nil
}

// -------- 前台超时 → 转入后台 --------

// switchWriter 可以在命令运行中换掉输出目标。
//
// 为什么需要它：前台命令的输出端在 Start 那一刻就固定了（os/exec 内部起了
// 复制管道的 goroutine，改字段没用）。超时转入后台时，进程还在往原来的
// 缓冲里写，而我们希望此后的输出进日志文件——只能换掉写入端本身。
//
// 拿不到这把锁就没法安全地「先把已有内容交给日志、再切目标」：
// 两步之间若有一次并发的 Write，那一行会两边都没有。
type switchWriter struct {
	mu sync.Mutex
	w  io.Writer
	// tag 只用在日志里：stdout / stderr 两路会各抄一段进来，
	// 不标清楚就分不出「前半截是哪一路的」。
	tag string
	// snap 切换的一瞬间取一次「已写入的内容」，交给日志做开头。
	// 在锁内调用，所以能安全读到底层缓冲。
	snap func() string
}

func newSwitchWriter(w io.Writer, tag string, snap func() string) *switchWriter {
	return &switchWriter{w: w, tag: tag, snap: snap}
}

func (s *switchWriter) Write(p []byte) (int, error) {
	if s == nil || s.w == nil {
		return len(p), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Take 取走「切换前已捕获的内容」，之后 Redirect 不再补抄一遍。
func (s *switchWriter) Take() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap == nil {
		return ""
	}
	head := s.snap()
	s.snap = nil
	return head
}

// Redirect 把输出改到 w，并（可选）先把已捕获的内容抄进 w。
func (s *switchWriter) Redirect(w io.Writer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil {
		if head := s.snap(); head != "" {
			_, _ = io.WriteString(w, "# ---- 切换前已捕获的 "+s.tag+" ----\n"+head+
				"\n# ---- "+s.tag+" 切换到本文件 ----\n")
		}
		s.snap = nil
	}
	s.w = w
}

// handoffToBackground 把「前台跑超时、但进程还活着」的命令交给后台任务管理器。
//
// 为什么不再杀掉：超时只说明「这次等不到它」，不说明它失败了。一条 npm install
// 在第 61 秒被连根杀掉，下次还得从头再装；一个跑了十分钟的数据脚本被杀掉，
// 前面十分钟全白费——用户看到的是「界面卡住，然后什么都没有」。
// 同类工具（Claude Code / WorkBuddy）的做法都是：超时不杀，转后台继续跑，
// 把 task_id 交回去，结束时主动通知。
//
// 返回 (结果, true) 表示已经接管；返回 false 表示转不了（没库、日志建不出来），
// 调用方按老路子杀掉即可——宁可杀掉，也不能让进程既跑着又没人管。
func handoffToBackground(ctx context.Context, d Deps, p *jobProc, out, errw *switchWriter,
	waitCh <-chan error, elapsed time.Duration) (map[string]any, bool) {
	if d.db() == nil || p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil, false
	}
	id := newJobID()
	logPath := jobLogPath(id)
	if err := d.db().CreateAIJob(db.AIJob{
		ID: id, Command: p.command, Cwd: p.dir, Status: db.AIJobRunning, LogPath: logPath, ConvID: ConvIDFrom(ctx),
	}); err != nil {
		logger.W("[ai-job] 超时转入后台失败（落库）：%v", err)
		return nil, false
	}
	f, ferr := os.Create(logPath)
	if ferr != nil {
		_ = d.db().FinishAIJob(id, db.AIJobFailed, 0, "无法创建日志文件："+ferr.Error())
		logger.W("[ai-job] 超时转入后台失败（建日志）：%v", ferr)
		return nil, false
	}
	// 头部必须写清楚「它是前台跑的、后来才转的」：用户翻日志时看到的第一行
	// 就是「我什么时候开的这条命令」，而时间和原因只有这里能记。
	fmt.Fprintf(f, "# %s\n# cwd: %s\n# started: %s（前台执行 %s 后仍未结束，已自动转入后台）\n# 关闭 QuickDock 会终止这个任务\n\n",
		p.command, p.dir, p.startedAt.Format(time.RFC3339), elapsed.Round(time.Second))

	// 把前台阶段已经产生的输出抄进日志：只从切换那一刻开始记的话，
	// 用户打开日志看到的是半截，前面那段「跑到哪了」全在对话里，翻起来很别扭。
	stdoutHead := out.Take()
	stderrHead := errw.Take()
	for _, seg := range []struct{ tag, body string }{{"stdout", stdoutHead}, {"stderr", stderrHead}} {
		if strings.TrimSpace(seg.body) != "" {
			fmt.Fprintf(f, "# ---- 切换前已捕获的 %s ----\n%s\n", seg.tag, strings.TrimRight(seg.body, "\n"))
		}
	}

	// 先换输出端再登记进程：反过来的话，进程恰在此时退出，finishJob 会先删掉
	// 一条还不存在的登记，留下一个永远 running 的幽灵任务。
	out.Redirect(f)
	errw.Redirect(f)
	_ = d.db().SetAIJobPID(id, p.cmd.Process.Pid)

	jobsMu.Lock()
	jobsProcs[id] = p
	jobsMu.Unlock()

	// Wait 已经在 run_command 里跑着了，这里只等它的结果——不能再 Wait 一次。
	go func() { finishJob(d, id, p, f, <-waitCh) }()
	// 登记到本轮：用户点停止时一并收掉，否则「对话停了、任务还在跑」。
	recordJob(ctx, id)

	logger.I("[ai-job] 前台命令超时，转入后台 %s pid=%d: %s", id, p.cmd.Process.Pid, p.command)
	return map[string]any{
		"jobId":        id,
		"status":       db.AIJobRunning,
		"cwd":          p.dir,
		"command":      p.command,
		"logPath":      logPath,
		"background":   true,
		"backgrounded": true,
		"timeout":      true,
		"stdout":       stdoutHead,
		"stderr":       stderrHead,
		"note": fmt.Sprintf("前台等待超过 %s 仍未结束，**已自动转入后台继续运行**（不是失败，也不是被终止）。"+
			"接下来不用干等、也不用 sleep 轮询：它结束时系统会把结果通知给你；"+
			"想确认进度可以查 job_status / job_output，用户也可以在顶部终端图标里看到它并手动终止。", elapsed.Round(time.Second)),
	}, true
}

// waitBackgroundJob 等进程结束，落状态、发通知。
func waitBackgroundJob(d Deps, id string, p *jobProc, logFile *os.File) {
	finishJob(d, id, p, logFile, p.cmd.Wait())
}

// finishJob 任务收尾：落状态、补日志尾、推通知。
//
// 单独抽出来是因为「前台超时转后台」那条路不能自己再 Wait 一次——进程是前台
// 起的，Wait 已经在另一个 goroutine 里跑着了，它只把等到的结果交过来。
func finishJob(d Deps, id string, p *jobProc, logFile *os.File, err error) {
	jobsMu.Lock()
	killed := p.killedByUs
	delete(jobsProcs, id)
	jobsMu.Unlock()

	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else if !killed {
			// 不是退出码问题（比如 IO 错）：按失败记，原因写进 error 列。
			_ = d.db().FinishAIJob(id, db.AIJobFailed, 0, err.Error())
			closeJobLog(logFile, -1, err.Error())
			notifyJobDone(d, id, p.cmd.Args, exitCode, err.Error())
			p.col.addDone(JobDone{
				ID: id, Command: p.command, Status: db.AIJobFailed,
				ExitCode: 0, DurationMs: time.Since(p.startedAt).Milliseconds(), ErrMsg: err.Error(),
			})
			return
		}
	}

	status := db.AIJobDone
	if killed {
		status = db.AIJobKilled
	} else if exitCode != 0 {
		status = db.AIJobFailed
	}
	_ = d.db().FinishAIJob(id, status, exitCode, "")
	closeJobLog(logFile, exitCode, "")
	logger.I("[ai-job] 后台任务 %s 结束 status=%s exit=%d", id, status, exitCode)
	notifyJobDone(d, id, p.cmd.Args, exitCode, "")
	p.col.addDone(JobDone{
		ID: id, Command: p.command, Status: status,
		ExitCode: exitCode, DurationMs: time.Since(p.startedAt).Milliseconds(),
	})
}

// closeJobLog 在日志末尾补一行结论再关文件。
//
// 没有这一行，日志的结尾看起来和「还在跑」一模一样；而用户翻日志时最想先知道的
// 就是「它到底结束了没有、退出码是多少」。
func closeJobLog(f *os.File, exitCode int, errMsg string) {
	if f == nil {
		return
	}
	line := fmt.Sprintf("\n# ended: %s  exit=%d\n", time.Now().Format(time.RFC3339), exitCode)
	if errMsg != "" {
		line = fmt.Sprintf("\n# ended: %s  失败: %s\n", time.Now().Format(time.RFC3339), errMsg)
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// notifyJobDone 后台任务结束时推一条通知。
//
// 不判窗口焦点：这个任务的全部意义就是「用户去干别的了」，
// 结果回来时不提醒，模型下次被问起才说「早就跑完了」。
func notifyJobDone(d Deps, id string, argv []string, exitCode int, errMsg string) {
	if d.Notify == nil {
		return
	}
	cmd := ""
	if len(argv) > 0 {
		cmd = logger.Trunc(argv[len(argv)-1], 60)
	}
	title := "后台任务已结束"
	body := fmt.Sprintf("%s（exit=%d）", cmd, exitCode)
	if errMsg != "" {
		title = "后台任务失败"
		body = fmt.Sprintf("%s：%s", cmd, logger.Trunc(errMsg, 120))
	} else if exitCode != 0 {
		title = "后台任务已退出（非零）"
	}
	d.Notify(title, body+"\n任务 id："+id)
}

// jobStatus 查一个后台任务的状态。
func jobStatus(d Deps, args map[string]any) (any, error) {
	if d.db() == nil {
		return nil, errors.New("数据库不可用")
	}
	id := strings.TrimSpace(mcpsrv.Arg(args, "job_id"))
	if id == "" {
		return nil, errors.New("缺少参数 job_id")
	}
	j, err := d.db().GetAIJob(id)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, fmt.Errorf("没有这个后台任务：%s（任务只在本次数据库里有效，应用重启后历史任务会被清理）", id)
	}
	res := map[string]any{
		"jobId":     j.ID,
		"status":    j.Status,
		"command":   j.Command,
		"cwd":       j.Cwd,
		"exitCode":  j.ExitCode,
		"logPath":   j.LogPath,
		"startedAt": j.StartedAt,
	}
	if j.EndedAt != "" {
		res["endedAt"] = j.EndedAt
	}
	if j.Error != "" {
		res["error"] = j.Error
	}
	switch j.Status {
	case db.AIJobRunning:
		res["note"] = "仍在运行。用 job_output 读最新输出；它会随 QuickDock 退出而终止"
	case db.AIJobKilled:
		res["note"] = "被宿主终止（QuickDock 退出或用户停止），不是命令自己失败"
	}
	return res, nil
}

// jobOutput 读一个后台任务的最新输出。
//
// 默认只给尾部：模型要的是「跑到哪一步 / 有没有报错」，把整份日志灌进上下文
// 会直接吃掉窗口预算。要看开头用 head=true（比如确认它到底有没有启动成功）。
func jobOutput(d Deps, args map[string]any) (any, error) {
	if d.db() == nil {
		return nil, errors.New("数据库不可用")
	}
	id := strings.TrimSpace(mcpsrv.Arg(args, "job_id"))
	if id == "" {
		return nil, errors.New("缺少参数 job_id")
	}
	j, err := d.db().GetAIJob(id)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, fmt.Errorf("没有这个后台任务：%s", id)
	}
	if j.LogPath == "" {
		return nil, errors.New("这个任务没有日志文件")
	}
	head := false
	if v, ok := args["head"]; ok {
		if b, isb := v.(bool); isb {
			head = b
		}
	}
	text, truncated, rerr := ReadJobLog(j.LogPath, head)
	if rerr != nil {
		return nil, rerr
	}
	res := map[string]any{
		"jobId":  j.ID,
		"status": j.Status,
		"output": text,
	}
	if truncated {
		res["truncated"] = true
		if head {
			res["note"] = fmt.Sprintf("日志超过 %dKB，这里只给了开头；要结尾请不传 head", aiJobOutputMax>>10)
		} else {
			res["note"] = fmt.Sprintf("日志超过 %dKB，这里只给了结尾；要看开头传 head=true", aiJobOutputMax>>10)
		}
	}
	return res, nil
}

// ReadJobLog 读日志的头或尾，最多 aiJobOutputMax 字节。
//
// 导出是因为后台任务面板（services/ai/ai_jobs.go）也要读同一份日志：
// 面板里给用户看的和给模型看的内容必须是同一段，两边各写一份解码逻辑
// 迟早出现「模型说没有报错、用户看到一片乱码」。
//
// 按字节截断而不是按行：一条 build 可能输出一行几百 KB 的进度条，
// 按行读会把整行读进内存再丢掉。
//
// 读出后按控制台代码页补一次解码：日志里存的是子进程写出的原始字节，
// 中文 Windows 下 cmd / 部分 CLI 是 GBK，直接当 UTF-8 给模型就是一片 U+FFFD。
func ReadJobLog(path string, head bool) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("打不开日志文件：%v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	size := st.Size()
	truncated := size > aiJobOutputMax
	if head || size <= aiJobOutputMax {
		buf := make([]byte, aiJobOutputMax)
		n, _ := f.Read(buf)
		return platform.DecodeConsoleText(string(buf[:n])), truncated, nil
	}
	if _, err := f.Seek(size-aiJobOutputMax, 0); err != nil {
		return "", false, err
	}
	buf := make([]byte, aiJobOutputMax)
	n, _ := f.Read(buf)
	// 从中间切进去的第一行多半是半截：丢掉它，避免把一行错误日志读成乱码。
	s := string(buf[:n])
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return platform.DecodeConsoleText(s), truncated, nil
}

// KillJob 终止一个还在跑的后台任务，返回是否真的杀到了东西。
//
// 与 StopAllJobs 的分工：那个是「应用要退了，全部收掉」，这个是「这一轮别干了」。
// 用户点停止时按本轮登记过的 id 逐个停，不会误伤别的会话里正在跑的任务。
func KillJob(id string) bool {
	jobsMu.Lock()
	p, ok := jobsProcs[id]
	if ok {
		p.killedByUs = true
	}
	jobsMu.Unlock()
	if !ok {
		return false
	}
	killCommandTree(p.cmd, p.dir)
	logger.I("[ai-job] 后台任务 %s 已被主动终止", id)
	return true
}

// JobAlive 报告某个后台任务的进程句柄是否还挂在内存表里。
//
// 与 job_status 的区别：那个读库里的状态行，这个读进程表。两者会短暂不一致
// （刚杀掉、还没收尾写库），调用方要判「是不是幽灵行」就得看这一份——
// 库里 running 而进程表里已经没有，说明收尾那条路没走完，可以安全地改状态。
func JobAlive(id string) bool {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	_, ok := jobsProcs[id]
	return ok
}

// StopAllJobs 终止所有还在跑的后台任务（应用退出时调用）。
//
// 必须主动杀：它们是独立进程组，宿主退出不会带走。留下的 node / dotnet
// 会一直占着端口，而用户下次启动只看到「端口被占用」，完全联想不到是 AI。
func StopAllJobs() {
	jobsMu.Lock()
	list := make([]*jobProc, 0, len(jobsProcs))
	for _, p := range jobsProcs {
		p.killedByUs = true
		list = append(list, p)
	}
	jobsMu.Unlock()
	for _, p := range list {
		killCommandTree(p.cmd, p.dir)
	}
}

// ResetStaleJobs 把库里遗留的 running 行改成 killed（启动时调用）。
//
// 宿主被强杀时没有任何机会更新状态，库里会留一堆永远 running 的行；
// 而模型查到 running 就会一直等一个早就没了的进程。
func ResetStaleJobs(database *db.Database) {
	if database == nil {
		return
	}
	jobs, err := database.ListRunningAIJobs()
	if err != nil {
		logger.W("[ai-job] 清理遗留任务失败: %v", err)
		return
	}
	for _, j := range jobs {
		_ = database.FinishAIJob(j.ID, db.AIJobKilled, 0, "QuickDock 退出时未能收尾")
	}
	if len(jobs) > 0 {
		logger.I("[ai-job] 已把 %d 条遗留的后台任务标记为终止", len(jobs))
	}
}

// PurgeOldJobs 清掉过期任务及其日志文件。
//
// 日志在磁盘上，行在库里，两边都要清；目录布局只有这个包知道，
// 所以 db 只负责把路径交出来。
func PurgeOldJobs(database *db.Database) {
	if database == nil {
		return
	}
	paths, err := database.PurgeAIJobs(time.Now().AddDate(0, 0, -aiJobMaxAgeDays))
	if err != nil {
		logger.W("[ai-job] 清理过期任务失败: %v", err)
		return
	}
	for _, p := range paths {
		// 只删自己目录下的文件：库里的路径是当初自己写的，但硬盘上的东西
		// 不归我们保证（用户可能把数据目录指到别处、或手动改过库）。
		if strings.HasPrefix(filepath.Clean(p), filepath.Clean(jobDir())) {
			_ = os.Remove(p)
		}
	}
	if len(paths) > 0 {
		logger.I("[ai-job] 已清理 %d 个过期后台任务", len(paths))
	}
}

// registerJob 后台任务的查询工具。
//
// 都是 LevelRead：查状态与读日志不改任何东西，纳入审批只会让「刚起完就查一下」
// 也弹一次确认，而那个频率高得离谱。
func registerJob(d Deps) {
	r := registrar{d: d, category: mcpsrv.CategoryCode}
	str := mcpsrv.Str
	boolp := mcpsrv.Bool

	r.add(jobStatus, mcpsrv.LevelRead, "job_status",
		"查后台任务的状态（running / done / failed / killed）与退出码。只对后台任务有效（background=true 起的，"+
			"以及前台超时后自动转入后台的）。**不要拿它轮询**：任务结束时会主动通知你，"+
			"这里只用于「用户问起进度」或「不确定它是否还活着」",
		[]string{"job_id"},
		map[string]any{
			"job_id": str("任务 id，即 run_command 返回的 jobId"),
		})

	r.add(jobOutput, mcpsrv.LevelRead, "job_output",
		"读后台任务的最新输出。默认给结尾（看进度与报错），head=true 给开头（确认是否启动成功）；"+
			"日志很长时只回一段，完整的让用户自己看 logPath",
		[]string{"job_id"},
		map[string]any{
			"job_id": str("任务 id"),
			"head":   boolp("可选。true 读开头，默认 false 读结尾"),
		})
}
