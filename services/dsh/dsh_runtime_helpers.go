package dsh

import (
	"fmt"
	"net"
	neturl "net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// dshErrorPage 返回 dsh 启动失败时的错误页（data URL），避免把窗口 SetURL 到一个没在监听的死端口，
// 让用户直面浏览器"无法访问此网站"。完整日志在主界面「环境管理 → DeepSeek Harness」面板。
func dshErrorPage(reason string) string {
	return "data:text/html;charset=utf-8," + neturl.QueryEscape(fmt.Sprintf(`<!DOCTYPE html><html lang="zh"><head><meta charset="utf-8"><style>
html,body{height:100%%;margin:0;background:#17181b;color:#e8eaed;font-family:system-ui,-apple-system,"Segoe UI",sans-serif;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:12px;text-align:center;padding:24px}
.ico{font-size:34px}.t{font-size:15px;font-weight:600}.r{font-size:12px;color:#f28b82;max-width:540px;word-break:break-all}.s{font-size:12px;color:#8b919c}
</style></head><body><div class="ico">⚠️</div><div class="t">dsh 启动失败</div><div class="r">%s</div><div class="s">完整日志见主界面「环境管理 → DeepSeek Harness」，关闭本窗口后重新点击导航可重试</div></body></html>`, neturl.PathEscape(reason)))
}

// FindFreePort 在 127.0.0.1 上找空闲端口（默认端口被占用时兜底）
func FindFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// findPortPID 返回 127.0.0.1:<port> 上 LISTENING 进程的 PID；无占用返回 0。
// netstat 是控制台程序，GUI 主进程（-H windowsgui）直接拉起会弹 cmd，必须隐藏窗口。
func findPortPID(port int) int {
	cmd := exec.Command("netstat", "-ano")
	cmd.SysProcAttr = hideWindowAttr()
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	target := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "LISTENING") || !strings.Contains(line, target) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// isNodeProcess 判断 PID 对应进程是否为 node.exe（dsh 由 node 拉起，残留实例也是 node）。
// tasklist 是控制台程序，同样必须隐藏窗口。
func isNodeProcess(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	cmd.SysProcAttr = hideWindowAttr()
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// CSV 首字段是镜像名（可能带引号），如 "node.exe","8216","Console",...
		name := strings.Trim(line, `"`)
		if idx := strings.Index(name, ","); idx > 0 {
			name = name[:idx]
		}
		if strings.EqualFold(name, "node.exe") {
			return true
		}
	}
	return false
}

// killProcessTree 用 taskkill /T /F 强杀进程树（隐藏窗口），返回是否成功发出。
func killProcessTree(pid int) bool {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = hideWindowAttr()
	return cmd.Run() == nil
}

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".cmd"
	}
	return ""
}

// cleanNodeEnv 返回剔除了 WorkBuddy/CodeBuddy 注入的 NODE_OPTIONS 的环境变量。
// WorkBuddy 会设 NODE_OPTIONS=--require=...genie-safe-delete.cjs 让 node 加载 safe-delete
// shim（把 fs 删除操作劫持成 trash），dsh 启动时 heal profile 需删除旧文件 → 直接抛错崩溃。
// 这里按引号感知分词后滤掉含 genie-safe-delete 的 token，其余保留（如 --use-system-ca）。
func cleanNodeEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || key != "NODE_OPTIONS" || !strings.Contains(val, "genie-safe-delete") {
			out = append(out, kv)
			continue
		}
		toks := splitQuoted(val)
		filtered := make([]string, 0, len(toks))
		for _, t := range toks {
			if strings.Contains(t, "genie-safe-delete") {
				continue
			}
			filtered = append(filtered, t)
		}
		out = append(out, key+"="+quoteJoin(filtered))
	}
	return out
}

// splitQuoted 按空格拆分，尊重双引号包裹（NODE_OPTIONS 的 --require="C:/Program Files/..." 路径带空格）
func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQ = !inQ
		case c == ' ' && !inQ:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// quoteJoin 重拼 NODE_OPTIONS：含空格的 token 重新加引号
func quoteJoin(toks []string) string {
	for i, t := range toks {
		if strings.ContainsAny(t, " \t") {
			toks[i] = `"` + t + `"`
		}
	}
	return strings.Join(toks, " ")
}
