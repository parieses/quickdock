package ai

// 把模型写成正文的工具调用还原成 tool_calls。
//
// DeepSeek 在「system 里说可以调工具、请求里却没有 tools 字段」时，会把调用输出成
// 一段 DSML 标记正文（`<｜｜DSML｜｜ invoke name="file_read">` 加 parameter 节点）。
// 协议层不认识这段文本，会把它当成最终回答，对话就此中断。
//
// 根因已在 ai_stream.go 修掉（system 里不再残留工具说明），这里是第二道闸：
// 换模型、换网关、提示词被长上下文挤掉时都可能复发。
//
//   - dsmlFilter：流式过滤，标记段落不推给前端
//   - parseDSMLToolCalls：把被吞掉的那段文本解析成 aiToolCall

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// dsmlTagRe 匹配一个 DSML 标签。
//
// 前缀竖线数量不固定：规范形式是 `<｜DSML｜invoke>`（单个全角竖线），
// 实测出现的是 `<｜｜DSML｜｜ invoke>`（双竖线 + 空格）。所以按 0~4 个匹配，
// 同时兼容 ASCII 竖线与单双引号，避免为一个渲染细节丢整轮工具调用。
var dsmlTagRe = regexp.MustCompile(`(?is)<\s*(/?)\s*[|｜]{0,4}\s*DSML\s*[|｜]{0,4}\s*([a-z_]+)([^<>]*)>`)

var (
	dsmlAttrNameRe   = regexp.MustCompile(`(?is)\bname\s*=\s*["']([^"']*)["']`)
	dsmlAttrStringRe = regexp.MustCompile(`(?is)\bstring\s*=\s*["']([^"']*)["']`)
)

// dsmlMaxTagBytes 单个标签的长度上限。
// 超过就当普通文本放行：正常标签只有几十字节，一直扣着不开的只会是正文里的 `<`。
const dsmlMaxTagBytes = 200

// dsmlHiddenLimit 被吞内容的累积上限（防一段病态输出把内存吃掉）。
// 一场工具调用的标记通常只有几百字节，256KB 已经奢侈了。
const dsmlHiddenLimit = 256 << 10

const (
	dsmlStatePass    = iota // 正常输出
	dsmlStateTag            // 正在收集一个 <...>，还不知道是不是标记
	dsmlStateSwallow        // 已确认在工具调用段落里，全部吞掉
)

// dsmlFilter 流式过滤器：把 DSML 段落从可见文本里摘出去，同时把原文留在 hidden 里。
//
// 只做「看不出这是标记就先扣着」这一件事：遇到 `<` 先进 tag 缓冲，
// 判定不是标记（`<div>`、`a < b`）就原样吐出去，绝不改写字面内容。
type dsmlFilter struct {
	state   int
	tag     strings.Builder
	tagFrom int    // 进入 tag 之前的状态，决定残片该还给可见文本还是还给 hidden
	endTag  string // "calls" / "invoke"：这一段以哪个闭合标签收尾
	hidden  strings.Builder
}

func newDSMLFilter() *dsmlFilter { return &dsmlFilter{state: dsmlStatePass} }

// Feed 喂入一段增量文本，返回其中应当展示给用户的部分。
// 返回空串是正常情况（正处在标记段落内）。
func (f *dsmlFilter) Feed(s string) string {
	if s == "" {
		return ""
	}
	var vis strings.Builder
	for _, r := range s {
		switch f.state {
		case dsmlStateSwallow:
			// `<` 先不写进 hidden：它可能是下一个标签的开头，等 tag 判定结束再统一落。
			// （swallow 里每遇到一个 `<` 都会开一次 tag，所以这里不会漏字符。）
			if r == '<' {
				f.openTag(dsmlStateSwallow)
			} else {
				f.writeHidden(r)
			}
		case dsmlStateTag:
			f.tag.WriteRune(r)
			if r == '>' {
				f.closeTag(&vis)
			} else if f.tag.Len() > dsmlMaxTagBytes {
				t := f.tag.String()
				f.tag.Reset()
				f.releaseText(t, &vis)
			}
		default:
			if r == '<' {
				f.openTag(dsmlStatePass)
			} else {
				vis.WriteRune(r)
			}
		}
	}
	return vis.String()
}

// Finish 冲刷残留。返回的文本仍应展示给用户（流结束时还没闭合的 `<`）。
func (f *dsmlFilter) Finish() string {
	var vis string
	if f.state == dsmlStateTag {
		t := f.tag.String()
		if f.tagFrom == dsmlStatePass {
			vis = t
		} else {
			// 工具调用段落被流截断：残片留给解析器，别丢。
			f.writeHiddenString(t)
		}
	}
	f.tag.Reset()
	f.state = dsmlStatePass
	f.endTag = ""
	return vis
}

// Hidden 返回被吞掉的工具调用原文。
func (f *dsmlFilter) Hidden() string { return f.hidden.String() }

func (f *dsmlFilter) openTag(from int) {
	f.tag.Reset()
	f.tag.WriteRune('<')
	f.tagFrom = from
	f.state = dsmlStateTag
}

// releaseText 判定「这段不可能是标记」，把缓冲原样还回它来的地方。
//
// 从正文来的（`<div>`、`1 < 2`）还给可见文本；从吞段里来的补进 hidden
// ——swallow 态里 `<` 是交给 tag 缓冲的，还没落进 hidden，不补就丢一个字符。
func (f *dsmlFilter) releaseText(t string, vis *strings.Builder) {
	if f.tagFrom == dsmlStatePass {
		vis.WriteString(t)
		f.state = dsmlStatePass
		return
	}
	f.writeHiddenString(t)
	f.state = dsmlStateSwallow
}

// closeTag 处理一个已完整的 <...>（调用方已把缓冲取走并清空）。
func (f *dsmlFilter) closeTag(vis *strings.Builder) {
	t := f.tag.String()
	f.tag.Reset()
	m := dsmlTagRe.FindStringSubmatch(t)
	if m == nil {
		f.releaseText(t, vis)
		return
	}
	closing := m[1] == "/"
	name := strings.ToLower(m[2])
	if f.tagFrom == dsmlStatePass {
		if closing {
			// 孤立的闭合标记（模型漏了开标签）：丢掉，不展示。
			f.state = dsmlStatePass
			return
		}
		f.writeHiddenString(t)
		// 外层是 calls / tool_calls 就等它的闭合；否则按单条 invoke 收尾。
		if name == "calls" || name == "tool_calls" {
			f.endTag = "calls"
		} else {
			f.endTag = "invoke"
		}
		f.state = dsmlStateSwallow
		return
	}
	// 来自吞段：标签本身还没进 hidden，补上再判断该不该退出。
	f.writeHiddenString(t)
	if closing && ((f.endTag == "calls" && (name == "calls" || name == "tool_calls")) ||
		(f.endTag == "invoke" && name == "invoke")) {
		f.state = dsmlStatePass
		f.endTag = ""
		return
	}
	f.state = dsmlStateSwallow
}

func (f *dsmlFilter) writeHidden(r rune) {
	if f.hidden.Len() < dsmlHiddenLimit {
		f.hidden.WriteRune(r)
	}
}

func (f *dsmlFilter) writeHiddenString(s string) {
	if f.hidden.Len() < dsmlHiddenLimit {
		f.hidden.WriteString(s)
	}
}

// parseDSMLToolCalls 从被吞掉的文本里解析出工具调用。
//
// 解析失败的部分一律丢弃：half 条调用回给上游只会换来一次「参数非法」，
// 而它本来就不是用户想看到的内容。
func parseDSMLToolCalls(text string) []aiToolCall {
	if text == "" || !strings.Contains(strings.ToUpper(text), "DSML") {
		return nil
	}
	hits := dsmlTagRe.FindAllStringSubmatchIndex(text, -1)
	if len(hits) == 0 {
		return nil
	}
	var out []aiToolCall
	var cur *dsmlInvoke
	flush := func(end int) {
		if cur == nil {
			return
		}
		cur = cur.withParam(text, end)
		if cur.name != "" {
			out = append(out, cur.build(len(out)+1))
		}
		cur = nil
	}
	for _, h := range hits {
		closing := h[2] >= 0 && text[h[2]:h[3]] == "/"
		name := strings.ToLower(text[h[4]:h[5]])
		attrs := text[h[6]:h[7]]
		switch name {
		case "invoke":
			if closing {
				flush(h[0])
				continue
			}
			flush(h[0]) // 上一个没闭合就当场收尾，不让它吞掉这一条
			cur = &dsmlInvoke{name: dsmlAttr(attrs, dsmlAttrNameRe)}
		case "parameter":
			if cur == nil {
				continue
			}
			cur = cur.withParam(text, h[0])
			if closing {
				continue
			}
			if pname := dsmlAttr(attrs, dsmlAttrNameRe); pname != "" {
				cur.param = pname
				cur.paramStart = h[1]
				// 缺省按字符串处理：宁可用错类型（模型能从结果里看出来），
				// 也不要把一段本该是文本的内容硬塞进 JSON 数字里丢掉。
				strAttr := dsmlAttr(attrs, dsmlAttrStringRe)
				cur.paramString = !strings.EqualFold(strAttr, "false")
			}
		}
	}
	flush(len(text))
	return out
}

// dsmlInvoke 解析过程中的一条调用。
type dsmlInvoke struct {
	name        string
	args        map[string]any
	param       string
	paramStart  int
	paramString bool
}

// withParam 收取正在进行的参数（end 是参数正文的结束位置）。
func (inv *dsmlInvoke) withParam(text string, end int) *dsmlInvoke {
	if inv.param == "" {
		return inv
	}
	if end > len(text) {
		end = len(text)
	}
	if end < inv.paramStart {
		end = inv.paramStart
	}
	raw := text[inv.paramStart:end]
	if inv.args == nil {
		inv.args = map[string]any{}
	}
	if inv.paramString {
		inv.args[inv.param] = trimOneNewline(raw)
	} else if v, ok := dsmlJSONValue(raw); ok {
		inv.args[inv.param] = v
	} else {
		inv.args[inv.param] = trimOneNewline(raw)
	}
	inv.param = ""
	return inv
}

func (inv *dsmlInvoke) build(idx int) aiToolCall {
	args := inv.args
	if args == nil {
		args = map[string]any{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		b = []byte("{}")
	}
	return aiToolCall{ID: fmt.Sprintf("call_dsml_%d", idx), Name: inv.name, Args: string(b)}
}

// dsmlJSONValue 把 string="false" 的参数正文按 JSON 解析。
func dsmlJSONValue(raw string) (any, bool) {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v); err != nil {
		return nil, false
	}
	return v, true
}

// trimOneNewline 去掉参数值首尾各一个换行。
//
// 只削一个：模型排版时常把参数值单独放一行（`<parameter ...>\n值\n</parameter>`），
// 那一对换行属于标记排版；再往里削就可能吃掉用户真正要的 Markdown 结构。
func trimOneNewline(s string) string {
	s = strings.TrimPrefix(s, "\r\n")
	s = strings.TrimPrefix(s, "\n")
	s = strings.TrimSuffix(s, "\r\n")
	s = strings.TrimSuffix(s, "\n")
	return s
}

// dsmlAttr 从标签属性串里取一个引号包裹的值。
func dsmlAttr(attrs string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}
