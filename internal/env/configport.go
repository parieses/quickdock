package env

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ConfigPortsProvider 可选能力：从运行时自身的配置文件解析「实际侦听端口」，
// 替代写死的默认端口常量（用户改了配置后，界面才不会仍显示旧端口）。
//
// Manager.Status 会把解析结果注入 ServiceStatus.Ports（Port 仍为主端口）。
// 未实现本接口、或配置文件不存在/解析失败时，回退 DefaultPort()。
//
// Caddy 特殊：同时返回 admin 端口（默认 2019，恒暴露）与 Caddyfile 里的站点端口，
// 以便前端「两个一块显示」。
type ConfigPortsProvider interface {
	ConfiguredPorts(version string) []int
}

// 各类配置文件的端口解析正则。集中在此，业务文件直接引用，无需各自 import regexp。
var (
	// postgresql.conf: port = 5432（也可能是 port=5432）
	rePgPort = regexp.MustCompile(`(?i)^port\s*=\s*'?(\d+)`)
	// my.ini / my.cnf: port=3306
	reSQLPort = regexp.MustCompile(`(?i)^port\s*=\s*'?(\d+)`)
	// mongod.conf（YAML）: port: 27017
	reMongoPort = regexp.MustCompile(`(?i)^\s*port\s*:\s*(\d+)`)
	// rabbitmq.conf: listeners.tcp.default = 5672
	reRabbitPort = regexp.MustCompile(`(?i)^listeners\.tcp[\w.]*\s*=\s*(\d+)`)
	// rabbitmq.conf 管理后台: management.tcp.port = 15672
	reRabbitMgmt = regexp.MustCompile(`(?i)^management\.tcp\.port\s*=\s*(\d+)`)
	// traefik.yml: address: ":80"
	reTraefikAddr = regexp.MustCompile(`(?i)address\s*[:=]\s*["']?[^\s"']*?:(\d+)`)
	// Caddyfile 站点地址的端口部分（配合行首无缩进判断使用）
	reCaddySitePort = regexp.MustCompile(`(?i):(\d+)$`)
	// Caddyfile: admin localhost:2019 / admin :2019 / admin 2019
	reCaddyAdmin = regexp.MustCompile(`(?i)^admin\s+(?:[\w.]+:|:)?(\d+)\s*$`)
	reCaddyAdminOff = regexp.MustCompile(`(?i)^admin\s+off\s*$`)
)

// readConfRawLines 读取配置文件，返回去注释后的原始行（保留行首缩进，便于
// 调用方判断 Caddyfile 块头/块内）。剔除 # 与 ; 行内注释，跳过纯空行。
// 文件不存在或不可读时返回 nil（调用方据此回退默认端口）。
func readConfRawLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// 行内注释：nginx/redis/apache 用 #，my.ini 还用 ;
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}
		out = append(out, line)
	}
	return out
}

// readConfLines 读取配置文件，逐行去空白并剔除 # / ; 行内注释，返回有效行。
// 文件不存在或不可读时返回 nil（调用方据此回退默认端口）。
func readConfLines(path string) []string {
	raw := readConfRawLines(path)
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// appendPort 去重追加端口（仅收合法端口号）。
func appendPort(dst []int, p int) []int {
	if p <= 0 || p > 65535 {
		return dst
	}
	for _, v := range dst {
		if v == p {
			return dst
		}
	}
	return append(dst, p)
}

// portsInConf 在配置文件中按给定正则收集所有端口（按出现顺序去重）。
func portsInConf(path string, res ...*regexp.Regexp) []int {
	var out []int
	for _, line := range readConfLines(path) {
		for _, re := range res {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				if len(m) < 2 {
					continue
				}
				if p, err := strconv.Atoi(m[1]); err == nil {
					out = appendPort(out, p)
				}
			}
		}
	}
	return out
}

// listenPortFromLine 从一行 listen 指令提取端口，支持：
// listen 80; / listen 127.0.0.1:8080 ssl; / listen [::]:80; / listen *:8080;
//
// 不能直接用正则取首个数字：那样 `listen 127.0.0.1:8080` 会误取 IP 首段的 127，
// 因此改为取地址 token 后再按 IPv6 / host:port / 纯端口三种形态分别处理。
func listenPortFromLine(line string) int {
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "listen") {
		return 0
	}
	addr := strings.TrimSuffix(fields[1], ";")
	if strings.HasPrefix(addr, "[") {
		// [::]:80 / [::1]:8080
		i := strings.Index(addr, "]:")
		if i < 0 {
			return 0
		}
		addr = addr[i+2:]
	} else if i := strings.LastIndex(addr, ":"); i >= 0 {
		// 127.0.0.1:8080 / *:8080 / :8080
		addr = addr[i+1:]
	}
	addr = strings.TrimRight(addr, ";")
	p, err := strconv.Atoi(addr)
	if err != nil {
		return 0
	}
	return p
}

// listenPortsInConf 解析 nginx.conf / httpd.conf 中所有 listen 指令的端口（按出现顺序去重）。
func listenPortsInConf(path string) []int {
	var out []int
	for _, line := range readConfLines(path) {
		if p := listenPortFromLine(line); p > 0 {
			out = appendPort(out, p)
		}
	}
	return out
}

// firstPortOrDefault 解析不到任何端口时回退 fallback。
func firstPortOrDefault(ports []int, fallback int) []int {
	if len(ports) > 0 {
		return ports
	}
	return []int{fallback}
}

// caddySitePorts 解析 Caddyfile 中的站点侦听端口。
// 只认「块头」：行首无缩进的地址行（:8080、127.0.0.1:8080、http://host:8080）。
// 块内指令（reverse_proxy/respond 等）都有缩进，据此排除，避免把上游端口当成侦听端口。
func caddySitePorts(cf string) []int {
	var out []int
	for _, line := range readConfRawLines(cf) {
		// 有缩进 => 块内指令，不是站点地址
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "admin") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if m := reCaddySitePort.FindStringSubmatch(fields[0]); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil {
				out = appendPort(out, p)
			}
		}
	}
	return out
}

// caddyAdminPortFromConf 解析 Caddyfile 的 admin 监听端口。
// 返回 0 表示未显式配置（调用方按 Caddy 默认 localhost:2019 处理）或 admin off。
func caddyAdminPortFromConf(cf string) int {
	for _, line := range readConfLines(cf) {
		if !strings.HasPrefix(strings.ToLower(line), "admin") {
			continue
		}
		if reCaddyAdminOff.MatchString(line) {
			return 0
		}
		if m := reCaddyAdmin.FindStringSubmatch(line); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil {
				return p
			}
		}
	}
	return 0
}
