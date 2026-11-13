package aiutil

// 终端/剪贴板文本送进模型之前的凭据脱敏。
//
// 为什么默认开启、且不允许调用方跳过：AI 上下文是一条绕过本地安全边界的
// 数据通道。密码库做了锁定、审计、剪贴板 30 秒清除，但只要屏显里出现了
// `cat ~/.ssh/id_ed25519` 或 `export AWS_SECRET_ACCESS_KEY=...`，用户点一次
// 「问 AI」就把这些内容发给了第三方模型，前面所有防护都白做。
//
// 设计取舍：
//   - 只做「模式匹配 + 替换」，不做语义理解。目标是挡住最常见、最致命的
//     一批格式，不可能穷举所有密钥；注释里也不该假装做到了。
//   - 保持行数不变：终端屏显是按行排版喂给模型的，吞掉换行会让模型把
//     PEM 私钥前后两行误读成同一条命令。所以逐行处理，PEM 块按行替换。
//   - 幂等：已含 [REDACTED] 的文本再跑一次结果不变，避免流式上下文被
//     反复追加标记。
import (
	"regexp"
	"strings"
)

// 占位符。刻意带上类型，模型看到 [REDACTED:private-key] 会知道这里原本
// 有密钥、不该继续追问「把刚才那段私钥贴出来」，比泛泛的 *** 有用。
const (
	redactGeneric = "[REDACTED]"
	redactKey     = "[REDACTED:private-key]"
	redactToken   = "[REDACTED:token]"
)

var (
	// PEM / OpenSSH 私钥块的起止行。只认行首尾的标记，避免把一行日志里
	// 提到 "BEGIN PRIVATE KEY" 的普通文本整段吃掉。
	pemBeginRe = regexp.MustCompile(`(?i)^\s*-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	pemEndRe   = regexp.MustCompile(`(?i)^\s*-----END [A-Z0-9 ]*PRIVATE KEY-----`)

	// 赋值型密钥：`KEY=value`、`KEY: value`、`KEY := "value"`。
	// 值至少 4 个字符才脱敏——`password: yes` 这种提示词不该被抹掉，
	// 否则模型会以为上下文被篡改过。
	// 键名前缀 `[A-Za-z0-9_]*` 不能省：屏显里最常见的是 `DB_PASSWORD=` /
	// `MYSQL_ROOT_PASSWORD=` 这类带业务前缀的环境变量，只写 \b(password)\b
	// 会因为前缀与 password 同为 word 字符而完全匹配不到。
	// RE2 不支持 lookahead，所以键名用穷举的词表而不是「任意大写下划线串」。
	assignRe = regexp.MustCompile(`(?i)\b([A-Za-z0-9_]*(?:aws_secret_access_key|aws_access_key_id|aws_session_token|api[_-]?key|secret[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|private[_-]?key|auth[_-]?token|password|passwd|pwd|token|authorization))\b(\s*(?::=|=|:)\s*)(["']?)([^\s"',;]{4,})`)
	// 命令行参数型：`--password xxx` / `--token=xxx`。
	cliFlagRe = regexp.MustCompile(`(?i)(--(?:password|passwd|token|api[_-]?key|secret|access[_-]?token|client[_-]?secret))(=|\s+)([^\s]{3,})`)
	// URL 内嵌凭据：scheme://user:pass@host。
	urlCredRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/:@]+:[^\s/@]+@`)
	// Bearer 头保留前缀，让模型仍能看出这是一次带鉴权的请求。
	bearerRe = regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/\-]{8,}=*`)
)

// 常见厂商令牌。分开写而不是一条大正则：某个前缀将来改格式时，
// 只动对应一行，不会误伤其它规则；也便于补测试时对着清单核对。
var tokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{16,}\b`),                                // GitHub
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{16,}\b`),                               // GitHub fine-grained
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),                               // Slack
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),                                      // OpenAI 及兼容端点
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}\b`),                                   // GitLab
	regexp.MustCompile(`\bAKIA[0-9A-Z]{12,}\b`),                                          // AWS access key id
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), // JWT
	regexp.MustCompile(`\b(?:npm|pypi|dop_v1|shpat_|shpca_)[A-Za-z0-9_-]{20,}\b`),        // 常见包管理器 / SaaS
}

// RedactSecrets 抹掉文本中的凭据，保持行数与行尾换行符不变。
//
// 逐行处理而不是整段正则：屏显可能同时有 CRLF 与 LF（远端程序输出混着
// 本地拼接），SplitAfter 保留原始分隔符，拼回去后排版与原来完全一致。
func RedactSecrets(input string) string {
	if input == "" {
		return input
	}
	// 不做「看一眼没有冒号就跳过」的快路径：AKIA...、JWT、PEM 起止行
	// 都未必包含常见分隔符，快路径一旦判断错就是漏脱敏，而 AI 上下文
	// 本来就有 rune 预算上限，逐行跑正则的开销可以忽略。
	lines := strings.SplitAfter(input, "\n")
	inKeyBlock := false
	for i, line := range lines {
		body, eol := splitEOL(line)
		if pemBeginRe.MatchString(body) {
			inKeyBlock = true
			lines[i] = redactKey + eol
			continue
		}
		if inKeyBlock {
			// 块内每一行都占位，行数不变；END 行结束状态。
			lines[i] = redactKey + eol
			if pemEndRe.MatchString(body) {
				inKeyBlock = false
			}
			continue
		}
		lines[i] = redactLine(body) + eol
	}
	return strings.Join(lines, "")
}

// RedactSecretsIn 批量脱敏，返回新 slice，不改动入参。
func RedactSecretsIn(sections []string) []string {
	out := make([]string, len(sections))
	for i, s := range sections {
		out[i] = RedactSecrets(s)
	}
	return out
}

// redactLine 处理单行。
//
// 顺序有讲究：先处理 PEM 之外的 token 与 URL，最后处理赋值型。
// 如果先跑赋值规则，`curl -H "Authorization: Bearer eyJ..."` 会被
// 替换成 `Authorization: [REDACTED]`，后面的 Bearer 规则就再也识别不到，
// 但那也已经安全了；反过来则会留下明文前缀。两种顺序都安全，
// 这里选「先长后短」让更具体的标记优先出现，便于用户看出命中了哪类。
func redactLine(line string) string {
	for _, re := range tokenPatterns {
		line = re.ReplaceAllString(line, redactToken)
	}
	line = urlCredRe.ReplaceAllString(line, "${1}"+redactGeneric+"@")
	line = bearerRe.ReplaceAllString(line, "${1}"+redactGeneric)
	// `Authorization: Bearer abc...` 在上一行已经变成 `Authorization: Bearer
	// [REDACTED]`；若再让赋值规则把 Bearer 当成值吃掉，就只剩两个孤零零的
	// [REDACTED]，模型看不出这是带鉴权的请求。真正的凭据此时已处理完，
	// 所以遇到值恰为 Bearer 时原样放回，只保留鉴权方式这个上下文。
	line = assignRe.ReplaceAllStringFunc(line, func(m string) string {
		parts := assignRe.FindStringSubmatch(m)
		if len(parts) > 4 && strings.EqualFold(parts[4], "bearer") {
			return m
		}
		return assignRe.ReplaceAllString(m, "${1}${2}${3}"+redactGeneric)
	})
	line = cliFlagRe.ReplaceAllString(line, "${1}${2}"+redactGeneric)
	return line
}

// splitEOL 拆出行尾的 \r\n 或 \n。
func splitEOL(line string) (body, eol string) {
	if strings.HasSuffix(line, "\r\n") {
		return line[:len(line)-2], "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return line[:len(line)-1], "\n"
	}
	return line, ""
}
