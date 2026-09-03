package env

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/sysutil"
)

// serviceManager 维护各运行时当前由 QuickDock 拉起的后台服务进程句柄。
// 状态查询优先走端口探测（isPortOpen），因此即使服务由外部启动也能被识别。
// 注意：该句柄仅覆盖“当前 Go 进程会话”内拉起的进程。QuickDock 重启/重编译后
// 原进程会变成孤儿，stop 必须依赖端口/原生信号兜底，不能只靠此句柄。
//
// 多版本运行时（redis/nginx）共用同一默认端口，因此同一时刻只能有一个实例在跑。
// 句柄记录「当前拉起的是哪个版本」，Status 据此精确判定每个版本各自的运行状态，
// 避免出现「装了两个版本、启动一个两个都显示已启动」的误判。
type runningService struct {
	cmd     *exec.Cmd
	version string
	exe     string
}

type serviceManager struct {
	mu   sync.Mutex
	svcs map[Runtime]*runningService
}

var svcMgr = &serviceManager{svcs: make(map[Runtime]*runningService)}

func (s *serviceManager) running(rt Runtime) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.svcs[rt]
	return ok
}

func (s *serviceManager) pid(rt Runtime) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.svcs[rt]; ok && c.cmd.Process != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

// info 返回本会话拉起的服务版本与可执行文件路径（用于按版本精确判定运行状态）。
func (s *serviceManager) info(rt Runtime) (version string, exe string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.svcs[rt]; ok {
		return c.version, c.exe
	}
	return "", ""
}

func (s *serviceManager) forget(rt Runtime) {
	s.mu.Lock()
	delete(s.svcs, rt)
	s.mu.Unlock()
	logger.I("[env] forget %s（清掉会话内进程句柄）", rt)
}

// start 拉起后台服务进程。args 透传给可执行文件（如 redis 的 "redis.conf"、php-fpm 的 "-b host:port"）；
// logPath 非空时把 stdout/stderr 追加写入该文件（用于日志查询），onLog 仍照常回调（通常为 nil）。
func (s *serviceManager) start(rt Runtime, version, exe, wd string, args []string, logPath string, onLog func(string)) error {
	s.mu.Lock()
	if cur, ok := s.svcs[rt]; ok {
		s.mu.Unlock()
		logger.W("[env] start %s 拒绝：同类型服务已在运行 version=%s", rt, cur.version)
		return fmt.Errorf("服务已在运行")
	}
	cmd := sysutil.Command(exe, args...)
	cmd.Dir = wd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.mu.Unlock()
		logger.E("[env] start %s StdoutPipe 失败: %v exe=%s", rt, err, exe)
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.mu.Unlock()
		logger.E("[env] start %s StderrPipe 失败: %v exe=%s", rt, err, exe)
		return err
	}
	logger.I("[env] start %s version=%s exe=%s wd=%s args=%v logPath=%s", rt, version, exe, wd, args, logPath)
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		logger.E("[env] start %s 启动失败: %v (exe=%s wd=%s args=%v)", rt, err, exe, wd, args)
		return err
	}
	pid := cmd.Process.Pid
	s.svcs[rt] = &runningService{cmd: cmd, version: version, exe: exe}
	s.mu.Unlock()
	logger.I("[env] start %s 成功 pid=%d", rt, pid)

	var logF *os.File
	if logPath != "" {
		if f, e := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); e == nil {
			logF = f
		} else {
			logger.W("[env] start %s 打开运行日志失败: %v path=%s（仅回调，不落盘）", rt, e, logPath)
		}
	}

	// 收集 stderr 近期输出，进程异常快速退出时回写主日志辅助排障（"启动不了"但无报错的关键来源）
	collector := &stderrCollector{}
	go consumeLogs(stdout, onLog, logF, nil)
	go consumeLogs(stderr, onLog, logF, collector)
	go func() {
		err := cmd.Wait()
		if logF != nil {
			logF.Close()
		}
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		s.mu.Lock()
		if s.svcs[rt] != nil && s.svcs[rt].cmd == cmd {
			delete(s.svcs, rt)
		}
		s.mu.Unlock()
		if err != nil {
			logger.E("[env] %s 进程异常退出 pid=%d code=%d err=%v", rt, pid, code, err)
			if lines := collector.drain(); len(lines) > 0 {
				logger.E("[env] %s 启动失败 stderr（末 %d 行）:", rt, len(lines))
				for _, l := range lines {
					logger.E("[env]   %s", l)
				}
			}
		} else {
			logger.I("[env] %s 进程正常退出 pid=%d code=%d", rt, pid, code)
		}
	}()
	return nil
}

// processExePath 返回占用 pid 的进程完整可执行文件路径（用于把监听端口反查到具体版本目录）。
func processExePath(pid int) string {
	if pid <= 0 {
		return ""
	}
	if runtime.GOOS != "windows" {
		out, err := exec.Command("readlink", "-f", "/proc/"+strconv.Itoa(pid)+"/exe").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		return ""
	}
	if p, err := processExePathWin(pid); err == nil {
		return strings.TrimSpace(p)
	}
	return ""
}

// consumeLogs 逐行消费命令输出并回调（用于服务运行日志）；logF 非空时同时追加写入文件（日志查询）。
// collect 非空时同步收集到 stderrCollector，供进程异常退出时回写主日志排障。
func consumeLogs(r io.Reader, onLog func(string), logF *os.File, collect *stderrCollector) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024) // 允许较长输出行（如 redis 配置报错）
	for sc.Scan() {
		line := sc.Text()
		if onLog != nil {
			onLog(line)
		}
		if logF != nil {
			logF.WriteString(line + "\n")
		}
		if collect != nil {
			collect.add(line)
		}
	}
}

// stderrCollector 收集服务进程 stderr 的近期输出（上限 200 行），进程异常退出时回写主日志。
type stderrCollector struct {
	mu  sync.Mutex
	buf []string
}

func (c *stderrCollector) add(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buf) < 200 {
		c.buf = append(c.buf, line)
	}
}

func (c *stderrCollector) drain() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.buf
	c.buf = nil
	return out
}

// isPortOpen 探测本机 TCP 端口是否可连接（用于判断 nginx/redis 是否在服务中）。
func isPortOpen(port int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 800*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// findListenPID 返回正在 LISTEN 状态占用 port 的进程 PID（跨平台尽力实现）。
func findListenPID(port int) int {
	if runtime.GOOS == "windows" {
		return findListenPIDWin(port)
	}
	// 非 Windows：使用 lsof/fuser 尽力探测
	if out, err := exec.Command("lsof", "-ti", "tcp:"+strconv.Itoa(port), "-sTCP:LISTEN").Output(); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			return pid
		}
	}
	return 0
}

// processImageMatches 校验 pid 对应的进程镜像名是否包含 expect（不区分大小写）。
// 用于端口兜底停止时，避免误杀其它占用同一端口的程序（如 IIS/Skype 占用 80）。
func processImageMatches(pid int, expect string) bool {
	if runtime.GOOS == "windows" {
		tasklistCmd := sysutil.Command("tasklist", "/fi", "PID eq "+strconv.Itoa(pid), "/fo", "csv", "/nh")
		out, err := tasklistCmd.Output()
		if err != nil {
			return false
		}
		return strings.Contains(strings.ToLower(string(out)), strings.ToLower(expect))
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(expect))
}

// killTree 强制杀掉 pid 及其子进程树（Windows: taskkill /T；非 Windows: kill -9）。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		killCmd := sysutil.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
		_ = killCmd.Run()
		return
	}
	_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
}

// stopByPort 找到占用 port 的监听进程，校验镜像名匹配 expect 后强制杀掉进程树。
// 作为停止服务的兜底手段，覆盖孤儿/残留进程，且只杀真正的目标程序。
func stopByPort(port int, expectImage string) {
	pid := findListenPID(port)
	if pid == 0 {
		return
	}
	if expectImage != "" && !processImageMatches(pid, expectImage) {
		logger.W("[env] stopByPort %d 跳过 pid=%d：镜像名不匹配 %s", port, pid, expectImage)
		return
	}
	logger.W("[env] stopByPort %d 杀掉 pid=%d 进程树（镜像=%s）", port, pid, expectImage)
	killTree(pid)
}

// runNginxStopSignal 对指定目录下的 nginx 发送优雅停止信号（-s stop -p dir）。
// 不依赖 QuickDock 进程句柄，孤儿进程也能停止；失败静默忽略（由端口兜底接管）。
func runNginxStopSignal(exe, dir string) {
	if runtime.GOOS != "windows" {
		return
	}
	stopCmd := sysutil.Command(exe, "-s", "stop", "-p", dir)
	_ = stopCmd.Run()
}

// runRedisStopSignal 通过同目录 redis-cli 优雅关闭 redis（SHUTDOWN NOSAVE）。
func runRedisStopSignal(cli string, port int) {
	if runtime.GOOS != "windows" {
		return
	}
	logger.I("[env] redis 优雅关闭：%s -p %d shutdown nosave", cli, port)
	shutdownCmd := sysutil.Command(cli, "-p", strconv.Itoa(port), "shutdown", "nosave")
	if err := shutdownCmd.Run(); err != nil {
		logger.W("[env] redis 优雅关闭失败（将由端口兜底接管）: %v", err)
	}
}
