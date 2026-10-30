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
	"errors"
	"fmt"
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

// startBackgroundCommand 起一个后台进程并立刻返回。
//
// 返回值里带着 job_id 与 log_path：模型要用它们调 job_status / job_output，
// 用户要能自己去看日志。
func startBackgroundCommand(d Deps, command, dir string) (map[string]any, error) {
	if d.DB == nil {
		return nil, errors.New("数据库不可用，无法记录后台任务")
	}
	id := "job_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + strconv.Itoa(os.Getpid())
	logPath := jobLogPath(id)

	// 先落库再起进程：进程如果秒退，它的状态更新会在「还没有这一行」时执行，
	// 影响 0 行，库里就永远留着一个 running 的幽灵任务。
	if err := d.DB.CreateAIJob(db.AIJob{
		ID: id, Command: command, Cwd: dir, Status: db.AIJobRunning, LogPath: logPath,
	}); err != nil {
		return nil, err
	}

	f, ferr := os.Create(logPath)
	if ferr != nil {
		_ = d.DB.FinishAIJob(id, db.AIJobFailed, 0, "无法创建日志文件："+ferr.Error())
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
		_ = d.DB.FinishAIJob(id, db.AIJobFailed, 0, "无法启动命令："+err.Error())
		return nil, fmt.Errorf("无法启动命令：%v", err)
	}
	_ = d.DB.SetAIJobPID(id, cmd.Process.Pid)

	p := &jobProc{cmd: cmd, dir: dir}
	jobsMu.Lock()
	jobsProcs[id] = p
	jobsMu.Unlock()

	go waitBackgroundJob(d, id, p, f)

	logger.I("[ai-job] 后台任务 %s 已启动 pid=%d: %s", id, cmd.Process.Pid, command)
	res := map[string]any{
		"jobId":   id,
		"status":  db.AIJobRunning,
		"cwd":     dir,
		"command": command,
		"logPath": logPath,
		"note": "任务已在后台启动并立刻返回。用 job_status 查它是否还在跑、用 job_output 读最新输出。" +
			"**关闭 QuickDock 会终止这个任务**——它不是守护进程，随宿主一起结束。" +
			"需要一条不会随应用退出的长期服务时，请让用户用系统服务 / 计划任务自己配。",
	}
	return res, nil
}

// waitBackgroundJob 等进程结束，落状态、发通知。
func waitBackgroundJob(d Deps, id string, p *jobProc, logFile *os.File) {
	err := p.cmd.Wait()
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
			_ = d.DB.FinishAIJob(id, db.AIJobFailed, 0, err.Error())
			closeJobLog(logFile, -1, err.Error())
			notifyJobDone(d, id, p.cmd.Args, exitCode, err.Error())
			return
		}
	}

	status := db.AIJobDone
	if killed {
		status = db.AIJobKilled
	} else if exitCode != 0 {
		status = db.AIJobFailed
	}
	_ = d.DB.FinishAIJob(id, status, exitCode, "")
	closeJobLog(logFile, exitCode, "")
	logger.I("[ai-job] 后台任务 %s 结束 status=%s exit=%d", id, status, exitCode)
	notifyJobDone(d, id, p.cmd.Args, exitCode, "")
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
	if d.DB == nil {
		return nil, errors.New("数据库不可用")
	}
	id := strings.TrimSpace(mcpsrv.Arg(args, "job_id"))
	if id == "" {
		return nil, errors.New("缺少参数 job_id")
	}
	j, err := d.DB.GetAIJob(id)
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
	if d.DB == nil {
		return nil, errors.New("数据库不可用")
	}
	id := strings.TrimSpace(mcpsrv.Arg(args, "job_id"))
	if id == "" {
		return nil, errors.New("缺少参数 job_id")
	}
	j, err := d.DB.GetAIJob(id)
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
	text, truncated, rerr := readJobLog(j.LogPath, head)
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

// readJobLog 读日志的头或尾，最多 aiJobOutputMax 字节。
//
// 按字节截断而不是按行：一条 build 可能输出一行几百 KB 的进度条，
// 按行读会把整行读进内存再丢掉。
func readJobLog(path string, head bool) (string, bool, error) {
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
		return string(buf[:n]), truncated, nil
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
	return s, truncated, nil
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
		"查后台任务的状态（running / done / failed / killed）与退出码。只对 run_command(background=true) 起的任务有效；"+
			"同步跑的命令不需要它",
		[]string{"job_id"},
		map[string]any{
			"job_id": str("任务 id，即 run_command 后台模式返回的 jobId"),
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
