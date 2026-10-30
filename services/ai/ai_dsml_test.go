package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// dsmlSample 是一段**线上真实抓到**的输出（DeepSeek 在 tools 被撤下后把调用写成正文）。
// 竖线是全角 U+FF5C 且是双个——这正是不能只按规范形式匹配的原因。
const dsmlSample = "骨架文件已经建好，我开始逐段填入内容。\n\n" +
	"<｜｜DSML｜｜ calls>\n" +
	"<｜｜DSML｜｜ invoke name=\"file_read\">\n" +
	"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">quickdock-guide.md</｜｜DSML｜｜ parameter>\n" +
	"<｜｜DSML｜｜ parameter name=\"limit\" string=\"false\">40</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"</｜｜DSML｜｜ calls>"

func TestDSMLFilterHidesToolCallBlock(t *testing.T) {
	f := newDSMLFilter()
	vis := f.Feed(dsmlSample) + f.Finish()

	// 用户能看到的只有那句正文，标记一个字符都不该漏出去。
	if strings.Contains(vis, "DSML") || strings.Contains(vis, "invoke") {
		t.Fatalf("DSML 标记漏到了可见文本：%q", vis)
	}
	if strings.TrimSpace(vis) != "骨架文件已经建好，我开始逐段填入内容。" {
		t.Fatalf("可见文本不对：%q", vis)
	}

	calls := parseDSMLToolCalls(f.Hidden())
	if len(calls) != 1 {
		t.Fatalf("期望 1 个调用，实际 %d 个（hidden=%q）", len(calls), f.Hidden())
	}
	if calls[0].Name != "file_read" {
		t.Fatalf("工具名不对：%q", calls[0].Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Args), &args); err != nil {
		t.Fatalf("参数不是合法 JSON：%v (%s)", err, calls[0].Args)
	}
	if args["path"] != "quickdock-guide.md" {
		t.Fatalf("path 不对：%v", args["path"])
	}
	if n, ok := args["limit"].(float64); !ok || n != 40 {
		// string="false" 必须还原成数字：工具要的是 int，拿到 "40" 会报类型错。
		t.Fatalf("limit 应为数字 40，实际 %#v", args["limit"])
	}
}

// 流式分片：标记会被切在任何位置，包括竖线与标签中间。
func TestDSMLFilterAcrossChunks(t *testing.T) {
	f := newDSMLFilter()
	var vis strings.Builder
	const chunk = 3
	runes := []rune(dsmlSample)
	for i := 0; i < len(runes); i += chunk {
		end := i + chunk
		if end > len(runes) {
			end = len(runes)
		}
		vis.WriteString(f.Feed(string(runes[i:end])))
	}
	vis.WriteString(f.Finish())

	if strings.Contains(vis.String(), "DSML") {
		t.Fatalf("分片喂入时标记漏出：%q", vis.String())
	}
	calls := parseDSMLToolCalls(f.Hidden())
	if len(calls) != 1 || calls[0].Name != "file_read" {
		t.Fatalf("分片解析失败：%+v", calls)
	}
}

// 正常内容里的尖括号必须原样保留：不能因为「看起来像标签」就把正文吃掉。
func TestDSMLFilterKeepsPlainHTML(t *testing.T) {
	f := newDSMLFilter()
	in := "普通 HTML：<div class=\"a\">文本</div>，以及比较符号 1 < 2。"
	vis := f.Feed(in) + f.Finish()
	if vis != in {
		t.Fatalf("正文被改写：\n期望 %q\n实际 %q", in, vis)
	}
	if f.Hidden() != "" {
		t.Fatalf("普通内容被吞：%q", f.Hidden())
	}
}

// 参数正文里带尖括号（写文件内容时极常见）不能被当成标签边界。
func TestDSMLParseContentWithAngleBrackets(t *testing.T) {
	hidden := "<｜｜DSML｜｜ calls>" +
		"<｜｜DSML｜｜ invoke name=\"file_write\">" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">a.md</｜｜DSML｜｜ parameter>" +
		"<｜｜DSML｜｜ parameter name=\"content\" string=\"true\"># 标题\n<div>正文</div>\n</｜｜DSML｜｜ parameter>" +
		"</｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"

	calls := parseDSMLToolCalls(hidden)
	if len(calls) != 1 || calls[0].Name != "file_write" {
		t.Fatalf("解析失败：%+v", calls)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(calls[0].Args), &args)
	if args["content"] != "# 标题\n<div>正文</div>" {
		t.Fatalf("内容被破坏：%q", args["content"])
	}
}

// 多个 invoke 并行、且没写外层 calls：也得一条条解出来。
func TestDSMLParseMultipleInvokes(t *testing.T) {
	hidden := "<｜DSML｜invoke name=\"app_info\"></｜DSML｜invoke>" +
		"<｜DSML｜invoke name=\"file_read\"><｜DSML｜parameter name=\"path\">x.txt</｜DSML｜parameter></｜DSML｜invoke>"

	calls := parseDSMLToolCalls(hidden)
	if len(calls) != 2 {
		t.Fatalf("期望 2 个调用，实际 %d：%+v", len(calls), calls)
	}
	if calls[0].Name != "app_info" || calls[1].Name != "file_read" {
		t.Fatalf("顺序或名称不对：%+v", calls)
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("ID 重复：%q", calls[0].ID)
	}
}

// 流被截断（没有闭合标签）时，已经完整的部分仍要能用。
func TestDSMLFilterTruncatedStream(t *testing.T) {
	f := newDSMLFilter()
	cut := "<｜｜DSML｜｜ calls>\n" +
		"<｜｜DSML｜｜ invoke name=\"file_read\">\n" +
		"<｜｜DSML｜｜ parameter name=\"path\" string=\"true\">quickdock-guide.md</｜｜DSML｜｜ parameter>\n" +
		"<｜｜DSML｜｜ parameter name=\"limit\" string=\"false\">4"
	vis := f.Feed(cut) + f.Finish()
	if strings.Contains(vis, "DSML") {
		t.Fatalf("截断时标记漏出：%q", vis)
	}
	calls := parseDSMLToolCalls(f.Hidden())
	if len(calls) != 1 || calls[0].Name != "file_read" {
		t.Fatalf("截断时解析失败：%+v (hidden=%q)", calls, f.Hidden())
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(calls[0].Args), &args)
	// limit 只剩 "4"：仍然按数字收下，模型拿到会自己纠正，总比丢掉整条调用好。
	if args["path"] != "quickdock-guide.md" {
		t.Fatalf("path 丢失：%v", args)
	}
}

// 不带 DSML 的普通文本必须快速返回，不能误伤。
func TestDSMLParseIgnoresPlainText(t *testing.T) {
	if calls := parseDSMLToolCalls("就是一段普通回答，含 <b>粗体</b>。"); calls != nil {
		t.Fatalf("普通文本被解析成调用：%+v", calls)
	}
}
