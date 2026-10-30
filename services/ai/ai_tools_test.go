package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsrv "quickdock/internal/mcp"
)

// parseToolArgs：无参工具模型常给 "" 而不是 {}，必须按空对象处理；
// 坏 JSON 要返回错误（调用方会把原文回给模型让它自己改）。
func TestParseToolArgs(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantLen int
		wantErr bool
	}{
		{"空串按无参数", "", 0, false},
		{"null 按无参数", "null", 0, false},
		{"空对象", "{}", 0, false},
		{"正常参数", `{"path":"a.txt","limit":10}`, 2, false},
		{"坏 JSON 报错", `{bad`, 0, true},
	}
	for _, c := range cases {
		args, err := parseToolArgs(c.in)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v, want err=%v", c.name, err, c.wantErr)
		}
		if len(args) != c.wantLen {
			t.Fatalf("%s: args=%v, want len %d", c.name, args, c.wantLen)
		}
	}
}

// flushToolCalls：必须按 index 升序输出（tool 结果消息的顺序要与之对齐），
// 丢掉没有 name 的半条残片（流被掐断的产物），并给缺 id 的调用补一个稳定 id。
func TestFlushToolCallsOrderAndCleanup(t *testing.T) {
	pending := map[int]*aiToolCall{
		2: {ID: "c2", Name: "env_list"},
		0: {ID: "", Name: "app_info", Args: "{}"},
		1: {ID: "c1", Name: "", Args: "半截"},
	}
	out := flushToolCalls(pending)
	if len(out) != 2 {
		t.Fatalf("应丢弃无名残片，实际 %d 条: %+v", len(out), out)
	}
	if out[0].Name != "app_info" || out[1].Name != "env_list" {
		t.Fatalf("顺序错误: %+v", out)
	}
	if out[0].ID == "" {
		t.Fatal("缺 id 时应自动补一个，否则 tool 结果无法对回去")
	}
}

// aiToolVisibleInMode：宿主工具只在教程模式挂给模型，agent 工具在所有模式都挂。
// 这条规则弄错的后果不是「少个功能」，而是「普通对话顺手翻用户的笔记」——
// 所以用测试钉住，而不是靠读代码时记得。
func TestAIToolVisibleInMode(t *testing.T) {
	cases := []struct {
		mode, category string
		agentOnly      bool
		want           bool
	}{
		{"chat", mcpsrv.CategoryNote, false, false},
		{"chat", mcpsrv.CategoryEnv, false, false},
		{"chat", mcpsrv.CategorySchedule, false, false},
		// 文件只读在普通对话也挂：没有 file_read，file_edit 就成了闭眼改代码。
		// 写能力本身另有闸（fsguard 的写白名单要求用户先绑定工作目录），不靠模式挡。
		{"chat", mcpsrv.CategoryFile, false, true},
		{"tutorial", mcpsrv.CategoryNote, false, true},
		{"tutorial", mcpsrv.CategoryFile, false, true},
		// agent 工具（web/code）不看模式：它们不读写宿主状态，
		// 「这个模式该不该有」这个问题的答案永远是「该」。
		{"chat", mcpsrv.CategoryWeb, true, true},
		{"tutorial", mcpsrv.CategoryWeb, true, true},
		{"chat", mcpsrv.CategoryCode, true, true},
		// 未知模式按「普通对话」处理：将来加新模式却忘了在这里登记，只会少工具，
		// 不会意外拿到全部系统权限。
		{"some-new-mode", mcpsrv.CategoryNote, false, false},
		{"some-new-mode", mcpsrv.CategoryWeb, true, true},
	}
	for _, c := range cases {
		if got := aiToolVisibleInMode(c.mode, c.category, c.agentOnly); got != c.want {
			t.Fatalf("mode=%s category=%s agentOnly=%v: got %v want %v", c.mode, c.category, c.agentOnly, got, c.want)
		}
	}
}

// toolCallKey：等价参数必须归一成同一个键。
// 模型两次给出的 JSON 往往只差空格或键的顺序，按原文比对会漏判成「两次不同调用」，
// 而漏判的代价是副作用工具真的跑第二遍（实测 robot_notify 给手机发了两条）。
func TestToolCallKey(t *testing.T) {
	same := [][2]string{
		{`{"title":"标题","body":"内容"}`, `{"body":"内容", "title":"标题"}`},
		{"", `{}`},
		{`null`, `{}`},
	}
	for _, p := range same {
		a1, _ := parseToolArgs(p[0])
		a2, _ := parseToolArgs(p[1])
		if toolCallKey("robot_notify", a1, p[0]) != toolCallKey("robot_notify", a2, p[1]) {
			t.Fatalf("等价参数未归一：%q 与 %q", p[0], p[1])
		}
	}
	// 反向：不同参数、不同工具绝不能撞键，否则会把正常调用挡掉——那是比重复执行更糟的错。
	a1, _ := parseToolArgs(`{"path":"a"}`)
	a2, _ := parseToolArgs(`{"path":"b"}`)
	if toolCallKey("file_read", a1, "") == toolCallKey("file_read", a2, "") {
		t.Fatal("不同参数撞键了")
	}
	if toolCallKey("file_read", a1, "") == toolCallKey("file_grep", a1, "") {
		t.Fatal("不同工具撞键了")
	}
}

// runToolCalls 的重复调用兜底：命中 seen 就不再真执行，把上次结果递回模型。
//
// 这里的 AIService 故意是 nil：命中分支必须在碰任何服务之前就返回。
// 哪天有人在函数开头加了 `a.something`，这个测试会直接 panic 出来提醒他。
func TestRunToolCallsSkipsDuplicate(t *testing.T) {
	var a *AIService
	args := `{"title":"标题","body":"内容"}`
	parsed, _ := parseToolArgs(args)
	seen := map[string]aiToolRecord{
		toolCallKey("robot_notify", parsed, args): {
			ID: "c0", Name: "robot_notify", Round: 1, Result: `{"sent":1,"ok":true}`,
		},
	}
	calls := []aiToolCall{
		{ID: "c1", Name: "robot_notify", Args: args},
		{ID: "c2", Name: "robot_notify", Args: `{"body":"内容","title":"标题"}`}, // 键序不同，语义相同
	}
	out, recs := a.runToolCalls(context.Background(), calls, 2, "", seen, "")

	if len(recs) != 2 {
		t.Fatalf("每个调用都要有记录，实际 %d 条", len(recs))
	}
	for i, r := range recs {
		if !r.Dedup {
			t.Fatalf("第 %d 条应被标记为跳过", i)
		}
		if r.Error != "" {
			t.Fatalf("跳过不是失败，不该有 error：%q", r.Error)
		}
		if !strings.Contains(r.Result, `{"sent":1`) {
			t.Fatalf("第 %d 条没有把上次结果递回去：%q", i, r.Result)
		}
	}
	// 协议要求：assistant(tool_calls) + 每个调用一条 tool 消息，顺序一致。
	if len(out) != 3 || out[0]["role"] != "assistant" {
		t.Fatalf("消息序列不合法: %+v", out)
	}
	if out[1]["tool_call_id"] != "c1" || out[2]["tool_call_id"] != "c2" {
		t.Fatalf("tool 消息顺序与调用不一致: %+v", out)
	}
}

// compactLoopMessages：超预算时折叠**最早**的工具结果，最后一轮必须原样保留——
// 模型正要用它，折了等于逼它重查一次（多花一轮的钱）。
func TestCompactLoopMessagesFoldsOldestOnly(t *testing.T) {
	big := strings.Repeat("x", 20000) // 单条约 12500 token，两条即超 24000 预算
	msgs := []map[string]interface{}{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "q"},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "a"}}},
		{"role": "tool", "tool_call_id": "a", "content": big},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "b"}}},
		{"role": "tool", "tool_call_id": "b", "content": big},
	}
	if n := compactLoopMessages(msgs, 24000); n != 1 {
		t.Fatalf("应只折叠 1 条最早的，实际 %d", n)
	}
	if got := len([]rune(msgs[3]["content"].(string))); got > aiToolFoldRune+200 {
		t.Fatalf("最早那条未被折叠，长度 %d", got)
	}
	if got := len([]rune(msgs[5]["content"].(string))); got != 20000 {
		t.Fatalf("最后一轮不该被折叠，长度 %d", got)
	}
	// 折叠后必须仍带提示，否则模型会以为自己当时只拿到了 600 字。
	if !strings.Contains(msgs[3]["content"].(string), "已折叠") {
		t.Fatal("折叠后缺少说明文字")
	}
}

// encodeToolRecords：没有调用时存空串（不用 "[]"），有调用时落库前截断结果，
// 否则五条 12000 字的结果就是 60KB，而每条回复都要读一遍。
func TestEncodeToolRecords(t *testing.T) {
	if s := encodeToolRecords(nil); s != "" {
		t.Fatalf("无记录应为空串，实际 %q", s)
	}
	long := strings.Repeat("y", 5000)
	s := encodeToolRecords([]aiToolRecord{{ID: "1", Name: "file_read", Result: long, Round: 1}})
	if s == "" {
		t.Fatal("有记录时不该返回空串")
	}
	var back []aiToolRecord
	if err := json.Unmarshal([]byte(s), &back); err != nil {
		t.Fatalf("落库 JSON 无法解析: %v", err)
	}
	if len(back) != 1 || back[0].Name != "file_read" || back[0].Round != 1 {
		t.Fatalf("字段丢失: %+v", back)
	}
	if n := len([]rune(back[0].Result)); n > aiRecordResultMaxRune+50 {
		t.Fatalf("结果未按落库上限截断，长度 %d", n)
	}
	// 落库时不能改动调用方的原始记录：同一份 records 还要用来回传模型
	// （模型需要完整的工具结果，落库那份是给人回看的摘要）。
	in := []aiToolRecord{{ID: "1", Name: "file_read", Result: long, Round: 1}}
	_ = encodeToolRecords(in)
	if in[0].Result != long {
		t.Fatal("encodeToolRecords 不应修改入参")
	}
}
