package env

import (
	"bufio"
	"fmt"
	"io"
	"net"
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
type serviceManager struct {
	mu   sync.Mutex
	cmds map[Runtime]*exec.Cmd
}

var svcMgr = &serviceManager{cmds: make(map[Runtime]*exec.Cmd)}

func (s *serviceManager) running(rt Runtime) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.cmds[rt]
	return ok
}

func (s *serviceManager) pid(rt Runtime) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cmds[rt]; ok && c.Process != nil {
		return c.Process.Pid
	}
	return 0
}

func (s *serviceManager) forget(rt Runtime) {
	s.mu.Lock()
	delete(s.cmds, rt)
	s.mu.Unlock()
}

func (s *serviceManager) start(rt Runtime, exe, wd string, onLog func(string)) error {
	s.mu.Lock()
	if _, ok := s.cmds[rt]; ok {
		s.mu.Unlock()
		return fmt.Errorf("服务已在运行")
	}
	cmd := exec.Command(exe)
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
	s.cmds[rt] = cmd
	s.mu.Unlock()

	go consumeLogs(stdout, onLog)
	go consumeLogs(stderr, onLog)
	go func() {
		cmd.Wait()
		s.mu.Lock()
		if s.cmds[rt] == cmd {
			delete(s.cmds, rt)
		}
		s.mu.Unlock()
	}()
	return nil
}

// consumeLogs 逐行消费命令输出并回调（用于服务运行日志）。
func consumeLogs(r io.Reader, onLog func(string)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if onLog != nil {
			onLog(sc.Text())
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
