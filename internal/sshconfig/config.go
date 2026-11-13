// Package sshconfig 把 OpenSSH ~/.ssh/config 解析成可直接映射为 QuickDock 主机的结构。
//
// 这是 QuickDock「从 SSH config 单向导入」的纯解析引擎：只读用户文件、绝不写回。
// 它刻意不感知 QuickDock 的存储模型，也不感知 UI——映射成 db.SshHost 的活儿留给上层门面。
// 因此只用标准库，且处于 internal/** 最底层，不反向依赖门面层与存储层（遵守 R10 依赖方向），
// 也不执行任何来自配置文件的命令。
package sshconfig

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Host 是一条 Host 块的解析结果。字段用零值表示「该指令没出现」，由调用方决定是否回填。
//
// 安全约定（导入外部文件时必须守住）：
//   - ProxyCommand 只当作字符串记录，绝不执行、绝不拼接进 shell 命令。OpenSSH 允许它是任意
//     本地命令，照搬执行等于把用户配置变成任意代码执行；QuickDock 连接走自己的 sshx 引擎，
//     这里保留字段仅供展示/人工核对。ProxyJump 同理，只是记录，不解析成跳板机 id。
//   - Alias / HostName 以 '-' 开头的条目整条丢弃：映射到 ssh 命令行时可能被当成选项
//     （如 -oProxyCommand=...）造成选项注入，参考 Motty 单向导入的拒绝对策。
type Host struct {
	Alias               string // Host 指令的第一个别名（用作显示名）
	HostName            string // HostName，未写则等于 Alias
	User                string
	Port                int    // 未写时为 22
	IdentityFile        string // ~ 已展开为绝对路径；%d/%h/%r/%u 等占位符原样保留
	ProxyJump           string
	ProxyCommand        string // 只记录，绝不执行、绝不拼 shell（见结构体上方安全约定）
	ServerAliveInterval int    // 0 = 未配置；显式 0 与未配置用 HasAliveInterval 区分
	HasAliveInterval    bool
	ServerAliveCountMax int
	IdentitiesOnly      bool
	RequestTTY          bool
}

const (
	defaultPort       = 22
	maxIncludeDepth   = 5 // 根文件为第 0 层，被包含文件最多到第 5 层
	maxScannerBufSize = 1024 * 1024
)

// knownDirectives 是要采集的指令集合。其余指令（Match、Include、HostKeyAlgorithms、
// ControlMaster 等）一律忽略——解析不了/不认识的指令跳过而不是让整份 config 失败。
var knownDirectives = map[string]bool{
	"hostname":            true,
	"user":                true,
	"port":                true,
	"identityfile":        true,
	"proxyjump":           true,
	"proxycommand":        true,
	"serveraliveinterval": true,
	"serveralivecountmax": true,
	"identitiesonly":      true,
	"requesttty":          true,
}

// unescapeReplacer 还原被双引号包住的值里的 \" 与 \\；包级复用避免每行新建。
var unescapeReplacer = strings.NewReplacer(`\"`, `"`, `\\`, `\`)

type directive struct {
	order int    // 全文件行序，用于 first-obtained-value：先出现的值胜出
	key   string // 小写关键字
	val   string // 已剥引号、去行内注释的值
}

type block struct {
	isDefaults bool     // 通配块（Host * 等）：不产出 Host，其指令作为全局默认值
	aliases    []string // 具体别名（通配别名已跳过）
	directives []directive
}

// Parse 解析 OpenSSH config 文本。第一个可用块优先；解析不了的指令跳过而不是整份失败。
//
// Parse 只有 io.Reader、没有基准目录，无法解析 Include 的相对路径，因此按契约忽略全部
// Include；需要递归展开 Include 请用 ParseFile。
func Parse(r io.Reader) ([]Host, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("读取 SSH config 失败: %w", err)
	}
	return parseLines(readLines(data)), nil
}

// ParseFile 解析文件；仅「文件不存在 / 读不了」才返回 error。
// Include 相对于该 config 所在目录递归展开，找不到/读不了/成环/超限一律静默跳过。
func ParseFile(path string) ([]Host, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 SSH config 文件失败: %w", err)
	}
	visited := map[string]bool{canonical(path): true}
	lines := expandIncludes(readLines(data), filepath.Dir(path), 0, visited)
	return parseLines(lines), nil
}

// readLines 按行切分：bufio.ScanLines 会吃掉 CRLF 的 \r；开头的 UTF-8 BOM 在这里一次性剥掉，
// 否则首行关键字会带上 \ufeff 而识别不了。
func readLines(data []byte) []string {
	s := bufio.NewScanner(bytes.NewReader(stripBOM(data)))
	s.Buffer(make([]byte, 4096), maxScannerBufSize)
	var lines []string
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	return lines
}

func stripBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

// stripComment 去掉行内 # 注释：双引号内的 # 不算注释，引号内的 \" 也不闭合引号。
// 只做「找边界、截断」，引号的剥离交给 unquoteValue / splitFields。
func stripComment(line string) string {
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && inQuote && i+1 < len(line) {
			i++ // 跳过被转义的字符，别把 \" 当成引号边界
			continue
		}
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if c == '#' && !inQuote {
			return line[:i]
		}
	}
	return line
}

// splitKeyValue 拆出关键字与未经处理的值，兼容 Key=Value / Key Value / Key = Value。
// 值里的空格（如 ProxyCommand 的参数）保留，交给 unquoteValue / splitFields 再处理。
func splitKeyValue(line string) (string, string) {
	s := strings.TrimSpace(line)
	i := 0
	for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '=' {
		i++
	}
	key := s[:i]
	rest := strings.TrimSpace(s[i:])
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimSpace(rest[1:])
	}
	return key, rest
}

// splitFields 按空白切分，但双引号内的空白不切分、引号被剥掉。反斜杠一律按字面量保留
// （Include 可能是 Windows 绝对路径 C:\...，不能当转义符吃掉），规格也不要求转义语法。
// 用于 Host 的多别名与 Include 的多路径——别名/路径含空格时可用引号包成一个 token。
func splitFields(s string) []string {
	var out []string
	var buf strings.Builder
	inQuote, started := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			started = true
		case (c == ' ' || c == '\t' || c == '\r' || c == '\n') && !inQuote:
			if started {
				out = append(out, buf.String())
				buf.Reset()
				started = false
			}
		default:
			buf.WriteByte(c)
			started = true
		}
	}
	if started {
		out = append(out, buf.String())
	}
	return out
}

// unquoteValue 去掉包住整个值的双引号并还原转义；值本身含空格但没被引号包住时原样返回
// （ProxyCommand 的参数就是这种形态）。
func unquoteValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return unescapeReplacer.Replace(v[1 : len(v)-1])
	}
	return v
}

// hasWildcard：别名出现 * ? ! 即视为通配模式，不作为一台机器、也不产出 Host。
// OpenSSH 里这些是模式/取反；本引擎把整块当作全局默认，不按模式去匹配具体别名。
func hasWildcard(alias string) bool {
	return strings.ContainsAny(alias, "*?!")
}

// expandTilde 把 ~/... 或 ~ 展开成绝对路径。拿不到主目录时原样返回，绝不编造路径。
// 含 %d/%h/%r/%u 的值（如 %d/.ssh/id_rsa）不以 ~ 开头，会原样保留占位符。
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// canonical 归一化成绝对干净路径，作为已访问集合的键：相对写法不同也指向同一文件时同样防成环。
func canonical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// readFileLines 读一个被包含的文件；不存在/读不了返回 ok=false，由调用方静默跳过。
func readFileLines(path string) ([]string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return readLines(data), true
}

// resolveIncludePath 解析单个 Include 目标：剥引号、展开 ~、相对路径挂到当前 config 目录。
func resolveIncludePath(tok, baseDir string) string {
	p := expandTilde(unquoteValue(tok))
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	return p
}

// expandIncludes 把 Include 行就地替换成被包含文件的行，得到一条扁平、有序的行序列，
// 再交给 parseLines——这样 Include 进来的指令按其在文件中的真实位置参与 first-value-wins。
// 深度上限 + 已访问集合双保险防成环；找不到/读不了/超限的目标静默跳过，不报错。
func expandIncludes(lines []string, baseDir string, depth int, visited map[string]bool) []string {
	var out []string
	ignoring := false
	for _, line := range lines {
		body := strings.TrimSpace(stripComment(line))
		key, raw := "", ""
		if body != "" {
			key, raw = splitKeyValue(body)
		}
		lk := strings.ToLower(key)

		// 与 parseLines 保持一致：Match 块内的 Include 不展开，也不污染后续 Host。
		if lk == "match" {
			ignoring = true
			out = append(out, line)
			continue
		}
		if lk == "host" {
			ignoring = false
		}

		if !ignoring && lk == "include" {
			if depth+1 > maxIncludeDepth {
				continue // 超过深度上限：整条 Include 丢弃，内容不展开
			}
			for _, tok := range splitFields(raw) {
				p := resolveIncludePath(tok, baseDir)
				if p == "" {
					continue
				}
				abs := canonical(p)
				if visited[abs] {
					continue // 已经访问过：防成环，也顺带避免同一文件被重复导入
				}
				child, ok := readFileLines(p)
				if !ok {
					continue
				}
				visited[abs] = true
				out = append(out, expandIncludes(child, filepath.Dir(p), depth+1, visited)...)
			}
			continue // Include 行本身被展开内容替换
		}
		out = append(out, line)
	}
	return out
}

// parseLines 是核心：先切成 Host/默认块，再按「全文件行序 + first-obtained-value」给每条
// 具体 Host 定值。通配块的指令当作全局默认值参与竞争，先出现的值胜出，因此放在文件顶部的
// Host * 会覆盖后面具体块的同名字段（与 OpenSSH 一致，想让具体块优先需把 Host * 放底部）。
func parseLines(lines []string) []Host {
	blocks := []*block{{isDefaults: true}} // 隐含全局块：收纳首个 Host/Match 之前的指令
	cur := blocks[0]
	ignoring := false
	order := 0

	for _, line := range lines {
		body := strings.TrimSpace(stripComment(line))
		if body == "" {
			continue
		}
		key, raw := splitKeyValue(body)
		lk := strings.ToLower(key)
		order++

		switch {
		case lk == "host":
			ignoring = false
			b := &block{}
			for _, a := range splitFields(raw) {
				a = strings.TrimSpace(a)
				if a == "" || hasWildcard(a) {
					continue // 通配别名不是一台机器，跳过
				}
				b.aliases = append(b.aliases, a)
			}
			b.isDefaults = len(b.aliases) == 0
			cur = b
			blocks = append(blocks, b)
		case lk == "match":
			// Match 后面的指令不应污染后续 Host：进入忽略态直到下一个 Host 行。
			ignoring = true
			cur = nil
		case ignoring || cur == nil:
			continue
		case knownDirectives[lk]:
			cur.directives = append(cur.directives, directive{order: order, key: lk, val: unquoteValue(raw)})
		default:
			// 其它指令（含 Include、HostKeyAlgorithms、ControlMaster 等）忽略
		}
	}

	// 汇总所有默认块的指令，作为每条具体 Host 的候选值来源。
	var defaults []directive
	for _, b := range blocks {
		if b.isDefaults {
			defaults = append(defaults, b.directives...)
		}
	}

	// 按别名归并具体块：同一别名出现在多个 Host 块时合并取值，第一个可用块优先。
	byAlias := make(map[string][]*block)
	var aliasOrder []string
	for _, b := range blocks {
		if b.isDefaults {
			continue
		}
		for _, a := range b.aliases {
			if _, ok := byAlias[a]; !ok {
				aliasOrder = append(aliasOrder, a)
			}
			byAlias[a] = append(byAlias[a], b)
		}
	}

	hosts := make([]Host, 0, len(aliasOrder))
	for _, alias := range aliasOrder {
		dirs := make([]directive, 0, len(defaults)+4)
		dirs = append(dirs, defaults...)
		for _, b := range byAlias[alias] {
			dirs = append(dirs, b.directives...)
		}
		sort.SliceStable(dirs, func(i, j int) bool { return dirs[i].order < dirs[j].order })

		h := Host{Alias: alias}
		obtained := make(map[string]bool, len(dirs))
		for _, d := range dirs {
			if obtained[d.key] {
				continue
			}
			if applyDirective(&h, d) {
				obtained[d.key] = true
			}
		}
		if h.HostName == "" {
			h.HostName = alias
		}
		if h.Port == 0 {
			h.Port = defaultPort
		}
		// - 开头的别名/主机名会被当成命令行选项注入，整条丢弃。
		if strings.HasPrefix(h.Alias, "-") || strings.HasPrefix(h.HostName, "-") {
			continue
		}
		hosts = append(hosts, h)
	}
	return hosts
}

// applyDirective 把一条指令写进 h，返回该指令是否「有效取得」（决定 first-value-wins 的占用）。
// 取值非法/不认识的指令返回 false：不占用字段，后面的同名指令仍可生效，也不会让整份解析失败。
func applyDirective(h *Host, d directive) bool {
	val := strings.TrimSpace(d.val)
	switch d.key {
	case "hostname":
		if val == "" {
			return false
		}
		h.HostName = val
		return true
	case "user":
		if val == "" {
			return false
		}
		h.User = val
		return true
	case "port":
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 || n > 65535 {
			return false // 非法/越界：跳过该指令，收尾时回退 22
		}
		h.Port = n
		return true
	case "identityfile":
		if val == "" {
			return false
		}
		h.IdentityFile = expandTilde(val)
		return true
	case "proxyjump":
		if val == "" {
			return false
		}
		h.ProxyJump = val
		return true
	case "proxycommand":
		// 只记录字符串，绝不执行、绝不拼 shell（见 Host 的安全约定）。
		if val == "" {
			return false
		}
		h.ProxyCommand = val
		return true
	case "serveraliveinterval":
		n, err := strconv.Atoi(val)
		if err != nil {
			return false
		}
		h.ServerAliveInterval = n
		h.HasAliveInterval = true // 显式写 0 也要与「未配置」区分开
		return true
	case "serveralivecountmax":
		n, err := strconv.Atoi(val)
		if err != nil {
			return false
		}
		h.ServerAliveCountMax = n
		return true
	case "identitiesonly":
		switch strings.ToLower(val) {
		case "yes":
			h.IdentitiesOnly = true
			return true
		case "no":
			h.IdentitiesOnly = false
			return true
		default:
			return false
		}
	case "requesttty":
		switch strings.ToLower(val) {
		case "yes", "force":
			h.RequestTTY = true
			return true
		case "no", "auto":
			// auto 是「由 ssh 自行决定」，映射成 bool 时保守记 false（不强制申请 TTY）。
			h.RequestTTY = false
			return true
		default:
			return false
		}
	}
	return false
}
