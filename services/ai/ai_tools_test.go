package ai

import (
	"context"
	"encoding/json"
	"path/filepath"
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

// 场景与角色：装配结果决定模型「知不知道」有这个能力。
//
// 以前这里是 aiToolVisibleInMode（模式门 + 硬编码白名单），后果是每来一个新需求
// 就要回来改一次常量。现在场景写在域声明里（services/mcp/catalog.go），
// 这里只验证装配真的按声明收窄，而不是漏掉某一层。
func TestAISurfaceSceneAndRole(t *testing.T) {
	mcpsrv.DefineDomain(mcpsrv.Domain{
		ID: "testscene",
		Scenes: map[string]mcpsrv.Access{
			mcpsrv.SceneAssistant: mcpsrv.AccessFull,
			mcpsrv.SceneReadOnly:  mcpsrv.AccessRead,
		},
	})
	mcpsrv.Register(mcpsrv.Tool{
		Name: "testscene_read", Level: mcpsrv.LevelRead, Domain: "testscene",
		Handler: func(map[string]any) (any, error) { return "ok", nil },
	})
	mcpsrv.Register(mcpsrv.Tool{
		Name: "testscene_write", Level: mcpsrv.LevelWrite, Domain: "testscene",
		Handler: func(map[string]any) (any, error) { return "ok", nil },
	})

	pol := mcpsrv.NewPolicy(mcpsrv.LevelWrite, nil)
	assistant := mcpsrv.Resolve(mcpsrv.ToolScope{
		Caller: mcpsrv.CallerAI, Policy: pol, Scene: mcpsrv.SceneAssistant,
	})
	if !assistant.Has("testscene_read") || !assistant.Has("testscene_write") {
		t.Fatalf("助手档应同时挂读与写，实际 %v", assistant.Names())
	}

	readonly := mcpsrv.Resolve(mcpsrv.ToolScope{
		Caller: mcpsrv.CallerAI, Policy: pol, Scene: mcpsrv.SceneReadOnly,
	})
	if readonly.Has("testscene_write") {
		t.Fatalf("只读档不该挂写工具，实际 %v", readonly.Names())
	}
	if !readonly.Has("testscene_read") {
		t.Fatal("只读档至少要挂读工具，否则 AI 什么也干不了")
	}

	// 角色收窄：子 agent 的 MaxLevel=0 必须把写工具摘掉。
	roleFilter := &ToolFilter{MaxLevel: mcpsrv.LevelRead}
	explore := mcpsrv.Resolve(mcpsrv.ToolScope{
		Caller: mcpsrv.CallerAI, Policy: pol, Scene: mcpsrv.SceneAssistant, Keep: roleFilter.keep,
	})
	if explore.Has("testscene_write") {
		t.Fatal("explore 角色不该拿得到写工具")
	}

	// 提示词纪律：hint 里提到的工具必须真的挂在这一面上。
	// 这条是本轮改造的防漂移闸——以前三份手写常量，门放开了文案还写着「去教程模式」。
	for _, sf := range []mcpsrv.Surface{assistant, readonly} {
		hint := buildToolHint(sf)
		for _, name := range toolNamesInText(hint) {
			if !sf.Has(name) {
				t.Fatalf("提示词提到了没挂上的工具 %q（场景 %s）", name, sf.Scene)
			}
		}
	}
}

// toolNamesInText 从提示词里挑出形如 file_write / env_stop 的工具名。
//
// 只认「全小写 + 下划线」且带一个下划线的形态：中文正文里不会出现这种串，
// 而 _cwd / _scope 这类宿主注入键在提示词里也不会出现。
func toolNamesInText(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && r != '_'
	}) {
		if !strings.Contains(w, "_") || strings.HasPrefix(w, "_") || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
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

// compactLoopMessages：超预算时折叠最早的工具结果，最后一轮必须原样保留——
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

// madeProgress：轮次到顶后要不要再开一批，全看这一批有没有真往前走。
//
// 判反的两种代价都很实：判成「有进展」→ 一个原地打转的模型被续 12 轮，
// 每轮全额计费；判成「没进展」→ 长活儿干到一半被硬停，用户只能再打一次「继续」。
func TestMadeProgress(t *testing.T) {
	cases := []struct {
		name string
		recs []aiToolRecord
		want bool
	}{
		{"空批次", nil, false},
		{"全都去重跳过", []aiToolRecord{{Dedup: true}, {Dedup: true}}, false},
		{"全都用户拒绝", []aiToolRecord{{Denied: true}, {Denied: true}}, false},
		{"去重里夹一个新调用", []aiToolRecord{{Dedup: true}, {}}, true},
		// 执行失败也算进展：模型会换参数再试，那正是它在想办法。
		{"失败也是进展", []aiToolRecord{{Error: "no such file"}}, true},
	}
	for _, c := range cases {
		if got := madeProgress(c.recs); got != c.want {
			t.Errorf("%s: madeProgress = %v, want %v", c.name, got, c.want)
		}
	}
}

// toolOutputText：卡片上该显示的是「命令输出了什么」，不是那段给模型的 JSON。
//
// 之前卡片直接铺 Result，run_command 的结果是 `{"stdout":"line1\nline2"...}`：
// 换行成了 \n、引号成了 \"，400 字里装不下几行，截断后连 JSON 都不成立——
// 用户看到的就是一屏转义字符加一句「已截断」。
func TestToolOutputText(t *testing.T) {
	cmd := func(m map[string]any) any { return m }
	cases := []struct {
		name string
		tool string
		val  any
		want string
	}{
		{
			"stdout 优先，单独出现时不加标签",
			"run_command",
			cmd(map[string]any{"stdout": "ok\n", "stderr": "", "exitCode": 0}),
			"ok",
		},
		{
			"两路都有内容才给 stderr 分隔",
			"run_command",
			cmd(map[string]any{"stdout": "out", "stderr": "bad", "exitCode": 1}),
			"exit 1\nout\n--- stderr ---\nbad",
		},
		{
			"只有 stderr 时直接给内容",
			"run_command",
			cmd(map[string]any{"stdout": "", "stderr": "'head' 不是内部或外部命令", "exitCode": 255}),
			"exit 255\n'head' 不是内部或外部命令",
		},
		{
			"超时要写进正文",
			"run_command",
			cmd(map[string]any{"stdout": "", "stderr": "", "exitCode": 0, "timeout": true}),
			"超时，已终止进程",
		},
		{
			"成功且无输出要有明确的一句话",
			"run_command",
			cmd(map[string]any{"stdout": "", "stderr": "", "exitCode": 0}),
			"（无输出）",
		},
		{
			"后台任务没有 stdout 键，交回原始结果",
			"run_command",
			cmd(map[string]any{"jobId": "j1", "logPath": "/tmp/a.log"}),
			"",
		},
		{
			"被停止要写成一句话，而不是当成命令失败",
			"run_command",
			cmd(map[string]any{"stdout": "", "stderr": "", "exitCode": -1, "canceled": true}),
			"exit -1 · 已停止（进程已终止）",
		},
		{
			// 读文件的卡片：先给「读了哪一段、读全了没有」，再给正文。
			// 没有这一行时，用户看到的是一段被截断的 JSON，只会以为 AI 只读了十几行。
			"file_read 给正文与读到的区间",
			"file_read",
			cmd(map[string]any{
				"from": 12, "to": 41, "lines": 30, "truncated": true,
				"content": "   12→package main\n   13→func main() {}",
			}),
			"第 12–41 行 · 共 30 行 · 文件还有更多内容（需要时用 offset 继续读）\n" +
				"   12→package main\n   13→func main() {}",
		},
		{
			"file_read 读到末尾",
			"file_read",
			cmd(map[string]any{"from": 1, "to": 2, "lines": 2, "truncated": false, "content": "     1→a\n     2→b"}),
			"第 1–2 行 · 共 2 行 · 已到文件末尾\n     1→a\n     2→b",
		},
		// 空文件 / offset 越界：正文为空时交回原始结果，原因由结果自己的
		// truncatedNote 说明。
		{"file_read 空内容", "file_read", cmd(map[string]any{"content": ""}), ""},
		// 其它工具的结果有自己的展示分支，这里不插手。
		{"非命令工具", "dir_list", cmd(map[string]any{"entries": []any{}}), ""},
	}
	for _, c := range cases {
		if got := toolOutputText(c.tool, c.val); got != c.want {
			t.Errorf("%s:\n got: %q\nwant: %q", c.name, got, c.want)
		}
	}
}

// skillReadPath：skill 目录下的读取要判为「可信、不越界」，这样宿主既不弹确认卡、
// 又会注入 _allow 让执行层放行。相对路径和 skills 根之外的路径不能被误判——
// 后者一旦误判，等于给模型开了「读任意路径都不问」的口子。
func TestSkillReadPath(t *testing.T) {
	root := skillsRoot()
	inside := filepath.Join(root, "labor-law-advisor-skill", "SKILL.md")
	outside := filepath.Join(filepath.Dir(root), "some-other-project", "secret.txt")

	cases := []struct {
		name string
		path string
		want bool // 是否判为 skill 读取
	}{
		{"skill 目录下的 SKILL.md", inside, true},
		{"skills 根之外", outside, false},
		{"相对路径不认", filepath.Join("skills", "x", "SKILL.md"), false},
		{"空路径不认", "", false},
	}
	for _, c := range cases {
		args := map[string]any{"path": c.path}
		got := skillReadPath(args) != ""
		if got != c.want {
			t.Errorf("%s: skillReadPath=%q, 判为 skill=%v, want %v", c.name, skillReadPath(args), got, c.want)
		}
	}
}
