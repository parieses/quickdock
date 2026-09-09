// Package terminal 提供内嵌终端的会话管理：在伪控制台（Windows ConPTY）中
// 拉起 shell，宿主侧读写合并输出，并支持窗口尺寸同步与环境变量注入。
//
// 非 Windows 平台 sysutil 的 ConPTY 为占位实现（Start 返回错误），
// 本包据此直接返回「仅支持 Windows」，由前端提示用户。
package terminal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"quickdock/internal/sysutil"
)

// flushInterval 输出合并窗口。终端高频输出（npm install 之类）若逐块回传，
// 每条都要跨进程序列化一次事件，反而拖慢自身；合并成 16ms 一批即可。
const flushInterval = 16 * time.Millisecond

// readBufSize 单次从伪控制台读取的字节数。
const readBufSize = 8192

// Manager 管理多个终端会话。OnOutput / OnExit 由宿主注入，用于把数据推给前端。
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*session

	OnOutput func(id, data string)
	OnExit   func(id string, code int)
}

type session struct {
	pty  *sysutil.ConPty
	once sync.Once
}

// New 创建终端管理器。
func New() *Manager {
	return &Manager{sessions: map[string]*session{}}
}

// Start 创建一个终端会话。
//   - shell: "powershell"（默认）或 "cmd"
//   - dir: 工作目录，为空则继承宿主
//   - extraPath: 需要前置到 PATH 的目录（用于注入某个运行时的 bin），可为 nil
//   - cols/rows: 初始尺寸，<=0 时使用伪控制台默认 120x50
func (m *Manager) Start(id, shell, dir string, extraPath []string, cols, rows int) error {
	if id == "" {
		return errors.New("terminal: 会话 ID 为空")
	}
	m.mu.Lock()
	if _, exists := m.sessions[id]; exists {
		m.mu.Unlock()
		return errors.New("terminal: 会话已存在")
	}
	m.mu.Unlock()

	exe, args := resolveShell(shell)
	pty, err := sysutil.StartConPtyWithEnv(exe, args, dir, buildEnv(extraPath))
	if err != nil {
		return err
	}
	if cols > 0 && rows > 0 {
		// 尺寸仅影响子进程换行宽度，失败不致命（旧系统无 ResizePseudoConsole）。
		_ = pty.Resize(cols, rows)
	}

	s := &session{pty: pty}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go m.readLoop(id, s)
	return nil
}

// Write 向终端标准输入写入（用户键盘输入）。
func (m *Manager) Write(id, data string) error {
	s, ok := m.get(id)
	if !ok {
		return errors.New("terminal: 会话不存在")
	}
	if data == "" {
		return nil
	}
	_, err := s.pty.Write([]byte(data))
	return err
}

// Resize 同步终端尺寸变化。
func (m *Manager) Resize(id string, cols, rows int) error {
	s, ok := m.get(id)
	if !ok {
		return errors.New("terminal: 会话不存在")
	}
	return s.pty.Resize(cols, rows)
}

// Kill 结束会话（终止子进程并释放伪控制台）。
func (m *Manager) Kill(id string) error {
	s, ok := m.get(id)
	if !ok {
		return errors.New("terminal: 会话不存在")
	}
	m.remove(id)
	return s.pty.Kill()
}

// KillAll 结束所有会话，用于应用退出时清理。
func (m *Manager) KillAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Kill(id)
	}
}

// Has 判断会话是否仍存活。
func (m *Manager) Has(id string) bool {
	_, ok := m.get(id)
	return ok
}

func (m *Manager) get(id string) (*session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// readLoop 读取伪控制台输出并按时间窗口合并后回传，进程退出后清理会话。
//
// 分两个协程：读协程只管把字节塞进缓冲（阻塞在 Read 上），主协程按 ticker 冲刷。
// 这样高频输出不会每条都触发一次跨进程序列化。
func (m *Manager) readLoop(id string, s *session) {
	var (
		buf     bytes.Buffer
		bufMu   sync.Mutex
		readEnd = make(chan struct{})
	)

	go func() {
		defer close(readEnd)
		b := make([]byte, readBufSize)
		for {
			n, err := s.pty.Read(b)
			if n > 0 {
				bufMu.Lock()
				buf.Write(b[:n])
				bufMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	flush := func() {
		bufMu.Lock()
		if buf.Len() == 0 {
			bufMu.Unlock()
			return
		}
		data := make([]byte, buf.Len())
		_, _ = buf.Read(data)
		bufMu.Unlock()
		if m.OnOutput != nil {
			m.OnOutput(id, string(data))
		}
	}

	tick := time.NewTicker(flushInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			flush()
		case <-readEnd:
			flush()
			code, _ := s.pty.Wait()
			m.remove(id)
			// 读取已结束，此刻关闭句柄是安全的（见 ConPty.Close 的前置条件说明）。
			s.once.Do(s.pty.Close)
			if m.OnExit != nil {
				m.OnExit(id, code)
			}
			return
		}
	}
}

// resolveShell 决定终端拉起的 shell 与启动参数。
//
// Windows 上必须返回绝对路径：CreateProcessW 在 lpApplicationName 只给裸文件名
// （无目录）时不会搜索 PATH，直接当相对路径解析 → 报 "The system cannot find the file
// specified"。因此这里仿照 VSCode 的做法，先探测系统自带 shell 的绝对路径，再交给 ConPTY。
// 两个 shell 都显式把控制台编码设为 UTF-8：Windows 控制台默认代码页是 GBK，
// 中文输出若不经此设置会在前端显示成乱码。
func resolveShell(shell string) (string, []string) {
	if strings.EqualFold(shell, "cmd") {
		return cmdPath(), []string{"/K", "chcp 65001 >nul"}
	}
	return pwshPath(), []string{
		"-NoLogo", "-NoExit", "-Command",
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; [Console]::InputEncoding=[System.Text.Encoding]::UTF8",
	}
}

// cmdPath 返回 cmd.exe 的绝对路径：优先 %COMSPEC%，缺失回退 System32（VSCode 同款逻辑）。
func cmdPath() string {
	if runtime.GOOS == "windows" {
		if p := os.Getenv("COMSPEC"); p != "" && fileExists(p) {
			return p
		}
		return filepath.Join(winRoot(), "System32", "cmd.exe")
	}
	return "cmd"
}

// pwshPath 返回 PowerShell 的绝对路径：优先 Windows PowerShell（系统内置、必然存在），
// 可选探测 PowerShell 7 (pwsh.exe)。非 Windows 平台回退命令名（由 PATH 解析，且终端实际不启动）。
func pwshPath() string {
	if runtime.GOOS != "windows" {
		return "powershell"
	}
	builtin := filepath.Join(winRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if fileExists(builtin) {
		return builtin
	}
	if p := pwsh7Path(); p != "" {
		return p
	}
	return builtin
}

// pwsh7Path 探测 PowerShell 7 安装路径（可选，未安装返回空）。
func pwsh7Path() string {
	prog := os.Getenv("ProgramFiles")
	if prog == "" {
		prog = `C:\Program Files`
	}
	p := filepath.Join(prog, "PowerShell", "7", "pwsh.exe")
	if fileExists(p) {
		return p
	}
	return ""
}

// winRoot 返回 Windows 系统根目录，兜底 C:\Windows。
func winRoot() string {
	if r := os.Getenv("SystemRoot"); r != "" {
		return r
	}
	return `C:\Windows`
}

// fileExists 判断路径存在且为常规文件。
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// buildEnv 在原环境基础上把 extraPath 前置到 PATH，让终端带上指定运行时的命令。
// 注意返回值会完全替换子进程环境，因此必须以 os.Environ() 为基底。
func buildEnv(extraPath []string) []string {
	env := os.Environ()
	if len(extraPath) == 0 {
		return env
	}
	sep := string(os.PathListSeparator)
	prefix := strings.Join(extraPath, sep)

	// Windows 上环境变量名大小写不敏感（常见 "Path"），故逐项比对而不能只查 "PATH"。
	out := make([]string, 0, len(env)+2)
	replaced := false
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:i]
		if strings.EqualFold(key, "path") {
			old := kv[i+1:]
			merged := prefix
			if old != "" {
				merged = prefix + sep + old
			}
			out = append(out, key+"="+merged)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, "Path="+prefix)
	}
	return out
}
