// Package cliprule 实现「剪贴板自动规则」的文本转换引擎。
//
// 刻意做成纯函数、不碰数据库、不碰剪贴板：规则判定与文本变换是最容易出错、
// 也最需要被单测覆盖的部分，把它和 IO 分开才能放心改。
//
// 不做什么（产品决定，不是遗漏）：
//   - 不做脱敏：不识别/替换手机号、邮箱、身份证、token。误伤率高，且被替换掉的
//     往往正是用户复制它的目的，静默改写比不改写更危险。
//   - 不改写系统剪贴板，除非规则显式 write_back（默认关）。
package cliprule

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"quickdock/internal/db"
)

// 动作标识：与前端下拉框的取值一一对应，改动需两边同步。
const (
	ActionTrim        = "trim"         // 去首尾空白 + 每行行尾空白
	ActionRemoveBlank = "remove_blank" // 删除空行
	ActionDedupeLines = "dedupe_lines" // 重复行去重（保留首次出现）
	ActionSortLines   = "sort_lines"   // 行排序
	ActionJSONFormat  = "json_format"  // JSON 缩进格式化（action_arg 为缩进宽度，默认 2）
	ActionJSONMinify  = "json_minify"  // JSON 压缩成一行
	ActionStripANSI   = "strip_ansi"   // 去掉 ANSI 颜色/控制序列
	ActionCase        = "case"         // 大小写：action_arg = lower | upper | title
	ActionURLDecode   = "url_decode"   // URL 解码
)

// MatchKind 取值
const (
	MatchAll   = "all"
	MatchRegex = "regex"
	MatchApp   = "app"
)

// ansiRe 匹配 CSI / OSC 等常见 ANSI 转义序列。
// 日志里最常见的两类是 ESC[31m（颜色）与 ESC]0;title BEL（窗口标题），都要清掉。
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")

// Matches 判断一条规则是否命中本次复制。
// sourceApp 是复制发生时的前台窗口标题（可能为空），app 规则据此过滤。
func Matches(r db.ClipboardRule, text, sourceApp string) bool {
	switch r.MatchKind {
	case "", MatchAll:
		return true
	case MatchRegex:
		if strings.TrimSpace(r.MatchExpr) == "" {
			return false
		}
		re, err := regexp.Compile(r.MatchExpr)
		if err != nil {
			return false // 正则写坏就当作不命中，不让一条坏规则把整条链路拖垮
		}
		return re.MatchString(text)
	case MatchApp:
		expr := strings.TrimSpace(r.MatchExpr)
		if expr == "" || sourceApp == "" {
			return false
		}
		return strings.Contains(strings.ToLower(sourceApp), strings.ToLower(expr))
	default:
		return false
	}
}

// ApplyOne 对单条规则做一次转换。第二条返回值为是否实际改变。
func ApplyOne(r db.ClipboardRule, text string) (string, bool) {
	out, ok := transform(r.Action, r.ActionArg, text)
	if !ok || out == text {
		return text, false
	}
	return out, true
}

// Apply 按优先级顺序把命中的规则串起来跑（前一条的输出是后一条的输入）。
//
// 返回值：
//
//	out       —— 最终文本
//	changed   —— 是否与原文不同
//	writeBack —— 是否应改写系统剪贴板（只有"命中且显式要求回写"的规则会置位）
func Apply(rules []db.ClipboardRule, text, sourceApp string) (out string, changed, writeBack bool) {
	out = text
	// 调用方已按 priority 排序，这里不重复排；但先复制一份再排，避免意外改到调用方切片。
	ordered := make([]db.ClipboardRule, len(rules))
	copy(ordered, rules)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })

	for _, r := range ordered {
		if !r.Enabled || !Matches(r, out, sourceApp) {
			continue
		}
		next, ok := ApplyOne(r, out)
		if !ok {
			continue
		}
		out = next
		changed = true
		if r.WriteBack {
			writeBack = true
		}
	}
	if !changed {
		return text, false, false
	}
	return out, true, writeBack
}

// transform 执行单个动作。ok=false 表示"这条规则在当前输入上无意义"（如非法 JSON）。
func transform(action, arg, text string) (string, bool) {
	switch action {
	case ActionTrim:
		lines := strings.Split(text, "\n")
		for i, ln := range lines {
			lines[i] = strings.TrimRight(ln, " \t\r")
		}
		return strings.TrimSpace(strings.Join(lines, "\n")), true

	case ActionRemoveBlank:
		lines := strings.Split(text, "\n")
		kept := make([]string, 0, len(lines))
		for _, ln := range lines {
			if strings.TrimSpace(ln) != "" {
				kept = append(kept, ln)
			}
		}
		return strings.Join(kept, "\n"), true

	case ActionDedupeLines:
		lines := strings.Split(text, "\n")
		seen := make(map[string]struct{}, len(lines))
		kept := make([]string, 0, len(lines))
		for _, ln := range lines {
			if _, dup := seen[ln]; dup {
				continue
			}
			seen[ln] = struct{}{}
			kept = append(kept, ln)
		}
		return strings.Join(kept, "\n"), true

	case ActionSortLines:
		lines := strings.Split(text, "\n")
		sort.SliceStable(lines, func(i, j int) bool { return lines[i] < lines[j] })
		return strings.Join(lines, "\n"), true

	case ActionJSONFormat, ActionJSONMinify:
		// 用 json.Indent / json.Compact 直接处理原始字节，而不是 Unmarshal 成 map 再 Marshal：
		//   1) map 会把键按字母重排，用户看到的顺序变了；
		//   2) float64 会把 12345678901234567890 变成 1.2345678901234567e+19。
		// 这两个副作用对一个"复制即用"的剪贴板工具都不可接受。
		// 两个函数本身会校验输入，尾部有多余内容会返回 error —— 这时原样不动。
		var buf bytes.Buffer
		var err error
		if action == ActionJSONMinify {
			err = json.Compact(&buf, []byte(text))
		} else {
			indent := 2
			if n, e := strconv.Atoi(strings.TrimSpace(arg)); e == nil && n >= 0 && n <= 8 {
				indent = n
			}
			err = json.Indent(&buf, []byte(text), "", strings.Repeat(" ", indent))
		}
		if err != nil {
			return text, false
		}
		return buf.String(), true

	case ActionStripANSI:
		return ansiRe.ReplaceAllString(text, ""), true

	case ActionCase:
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "lower":
			return strings.ToLower(text), true
		case "upper":
			return strings.ToUpper(text), true
		case "title":
			return titleCase(text), true
		default:
			return text, false
		}

	case ActionURLDecode:
		// 先 PathUnescape：QueryUnescape 会把 '+' 变成空格，普通文本里的加号会被悄悄改掉。
		// 只有 %XX 形式解不开时才退回 QueryUnescape（表单编码场景）。
		if s, err := url.PathUnescape(text); err == nil {
			return s, true
		}
		if s, err := url.QueryUnescape(text); err == nil {
			return s, true
		}
		return text, false

	default:
		return text, false
	}
}

// titleCase 每个单词首字母大写（按空白与常见分隔符切分，其余保持原样转小写）。
func titleCase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	upNext := true
	for _, r := range s {
		if upNext {
			b.WriteRune(toUpperRune(r))
			upNext = false
		} else {
			b.WriteRune(toLowerRune(r))
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '-' || r == '_' || r == '/' {
			upNext = true
		}
	}
	return b.String()
}

func toUpperRune(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	}
	return r
}

func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

// Test 供界面「试运行」用：返回转换结果与是否命中，不落库、不影响剪贴板。
func Test(r db.ClipboardRule, sample, sourceApp string) (out string, matched, changed bool) {
	if !Matches(r, sample, sourceApp) {
		return sample, false, false
	}
	out, ok := ApplyOne(r, sample)
	return out, true, ok
}

// ValidRegex 供门面层做"保存前校验"：正则写坏要当场报错，
// 而不是等用户复制了东西才发现规则默默不生效。
func ValidRegex(expr string) bool {
	if strings.TrimSpace(expr) == "" {
		return false
	}
	_, err := regexp.Compile(expr)
	return err == nil
}
