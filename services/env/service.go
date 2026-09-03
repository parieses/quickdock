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
}

// start 拉起后台服务进程。args 透传给可执行文件（如 redis 的 "redis.conf"、php-fpm 的 "-b host:port"）；
// logPath 非空时把 stdout/stderr 追加写入该文件（用于日志查询），onLog 仍照常回调（通常为 nil）。
func (s *serviceManager) start(rt Runtime, version, exe, wd string, args []string, logPath string, onLog func(string)) error {
	s.mu.Lock()
	if _, ok := s.svcs[rt]; ok {
		s.mu.Unlock()
		return fmt.Errorf("服务已在运行")
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = wd
	cmd.SysProcAttr = hideWindowAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.svcs[rt] = &runningService{cmd: cmd, version: version, exe: exe}
	s.mu.Unlock()

	var logF *os.File
	if logPath != "" {
		if f, e := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); e == nil {
			logF = f
		}
	}

	go consumeLogs(stdout, onLog, logF)
	go consumeLogs(stderr, onLog, logF)
	go func() {
		cmd.Wait()
		if logF != nil {
			logF.Close()
		}
		s.mu.Lock()
		if s.svcs[rt] != nil && s.svcs[rt].cmd == cmd {
			delete(s.svcs, rt)
		}
		s.mu.Unlock()
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
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-Process -Id "+strconv.Itoa(pid)+" -ErrorAction SilentlyContinue).Path").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// consumeLogs 逐行消费命令输出并回调（用于服务运行日志）；logF 非空时同时追加写入文件（日志查询）。
func consumeLogs(r io.Reader, onLog func(string), logF *os.File) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if onLog != nil {
			onLog(line)
		}
		if logF != nil {
			logF.WriteString(line + "\n")
		}
	}
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
		out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
		if err != nil {
			return 0
		}
		want := ":" + strconv.Itoa(port) + " "
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			line := sc.Text()
			if !strings.Contains(line, want) {
				continue
			}
			if !strings.Contains(line, "LISTENING") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			pidStr := fields[len(fields)-1]
			if pid, err := strconv.Atoi(pidStr); err == nil {
				return pid
			}
		}
		return 0
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
		out, err := exec.Command("tasklist", "/fi", "PID eq "+strconv.Itoa(pid), "/fo", "csv", "/nh").Output()
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
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
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
		return
	}
	killTree(pid)
}

// runNginxStopSignal 对指定目录下的 nginx 发送优雅停止信号（-s stop -p dir）。
// 不依赖 QuickDock 进程句柄，孤儿进程也能停止；失败静默忽略（由端口兜底接管）。
func runNginxStopSignal(exe, dir string) {
	if runtime.GOOS != "windows" {
		return
	}
	_ = exec.Command(exe, "-s", "stop", "-p", dir).Run()
}

// runRedisStopSignal 通过同目录 redis-cli 优雅关闭 redis（SHUTDOWN NOSAVE）。
func runRedisStopSignal(cli string, port int) {
	if runtime.GOOS != "windows" {
		return
	}
	_ = exec.Command(cli, "-p", strconv.Itoa(port), "shutdown", "nosave").Run()
}
