// Package aria2 的会话文件解析。
//
// --save-session 写出的文件不是 JSON，也不是 aria2.addUri 的命令序列，而是
// 「输入文件格式」：每个下载一个块，块首行是一个或多个用 TAB 分隔的 URI
// （行尾还有一个 TAB），其后每行是一个以单个空格缩进的 key=value 选项：
//
//	https://example.com/a.iso\t
//	 gid=766322f4c7a2a3db
//	 pause=true
//	 dir=F:\Downloads
//	 continue=true
//
// 换电脑续传要的就是这里面的 URI 与「原文件名」，目录本身属于旧机器，
// 导入时会按新机器上实际找到的 .aria2 / 数据文件位置重写。
package aria2

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

const (
	// MaxSessionBytes 防止误选到几个 GB 的文件把内存吃光。
	MaxSessionBytes = 16 << 20
	// maxSessionLineBytes 预签名 URL 可能很长，bufio.Scanner 默认 64K 不够。
	maxSessionLineBytes = 1 << 20
)

// SessionTask 是会话文件里的一个未完成下载。
type SessionTask struct {
	URIs    []string          `json:"uris"`
	Options map[string]string `json:"options"`
}

// GID 是旧机器上 aria2 给的任务 ID；只用于展示，重新 addUri 会生成新 GID。
func (t SessionTask) GID() string { return t.Options["gid"] }

// Paused 反映会话里的 pause=true；导入时默认不沿用（用户想继续下载）。
func (t SessionTask) Paused() bool {
	return strings.EqualFold(strings.TrimSpace(t.Options["pause"]), "true")
}

// ParseSession 容错解析 aria2.session 的原始内容。
//
// 判定规则刻意做得很松：首字符是空格/TAB 且含 '=' 的行算选项，否则算新的 URI 行；
// 空行、注释行、写坏的选项行都跳过。这样即使旧版 aria2 / 第三方前端（Motrix 等）
// 在块之间多插了空行或注释，也不会让后面的任务整个错位。
func ParseSession(data []byte) []SessionTask {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), maxSessionLineBytes)
	var out []SessionTask
	var cur *SessionTask
	flush := func() {
		if cur != nil && len(cur.URIs) > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
		// 选项行的形态是 " key=value"：等号左边是一个不带空格、不带 :// 的键。
		// 预签名 URL 的查询串里也有 '='，只判「缩进行 + 含等号」会把它误吃成选项，
		// 整条任务因此消失，所以这里多一道键形态校验。
		keyPart, _, hasEq := strings.Cut(trimmed, "=")
		isOption := hasEq && indented && keyPart != "" &&
			!strings.Contains(keyPart, "://") && !strings.ContainsAny(keyPart, " \t")
		if isOption {
			if cur == nil {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(keyPart))
			cur.Options[k] = trimmed[len(keyPart)+1:]
			continue
		}
		if indented && !strings.Contains(trimmed, "://") &&
			!strings.HasPrefix(strings.ToLower(trimmed), "magnet:") {
			// 缩进、没有等号、又不像 URI —— 写坏的选项行，丢掉即可。
			// 若当成新 URI 行，会凭空多出一个任务并让后面的选项串位。
			continue
		}
		// 非缩进行 = 新任务的 URI 行。URI 用 TAB 分隔，末尾常带一个空段。
		flush()
		uris := make([]string, 0, 2)
		for _, part := range strings.Split(line, "\t") {
			if u := strings.TrimSpace(part); u != "" {
				uris = append(uris, u)
			}
		}
		if len(uris) == 0 {
			continue
		}
		cur = &SessionTask{URIs: uris, Options: map[string]string{}}
	}
	flush()
	return out
}

// ReadSession 读取并解析会话文件；空文件不算错误，交给调用方决定提示什么。
func ReadSession(path string) ([]SessionTask, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("读取会话文件失败: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("会话文件路径是目录，请选择 aria2.session 文件")
	}
	if st.Size() > MaxSessionBytes {
		return nil, fmt.Errorf("会话文件超过 16MB，可能选错了文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取会话文件失败: %w", err)
	}
	return ParseSession(data), nil
}
