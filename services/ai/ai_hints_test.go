package ai

// WP18 / WP19：两条行为约束提示的守卫测试。
//
// 这两条都是纯提示词改动，失效方式同样安静：提示段写好了但没接进去、
// 或者接进去却在不该出现的时候出现（只读对话里教它写 quickdock/）。
// 所以这里验的不是文案好不好，而是「该出现时出现、不该出现时不出现」。

import (
	"strings"
	"testing"
)

// 只读工具集：没有任何会写盘的工具。
func readOnlyTools() []map[string]any {
	return []map[string]any{
		{"type": "function", "function": map[string]any{"name": "file_read"}},
		{"type": "function", "function": map[string]any{"name": "dir_list"}},
	}
}

func withTool(names ...string) []map[string]any {
	out := readOnlyTools()
	for _, n := range names {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{"name": n}})
	}
	return out
}

// WP18：有目录 + 有写权限才提「辅助文件放 quickdock/」。
func TestWorkdirHintConditions(t *testing.T) {
	// 没有工作目录：模型没有可写的根，讲「写到 quickdock/」是空话。
	if got := workdirHintFor("", withTool("file_write")); got != "" {
		t.Errorf("没有 cwd 时不该提工作文件约定: %q", got)
	}
	// 只读档：讲怎么写文件会诱导它去调一个挂不上的工具。
	if got := workdirHintFor("/proj", readOnlyTools()); got != "" {
		t.Errorf("只读对话不该提工作文件约定: %q", got)
	}
	// 正常情况：有目录 + 有 file_write。
	got := workdirHintFor("/proj", withTool("file_write"))
	if got == "" {
		t.Fatal("有工作目录与写权限时应追加工作文件约定")
	}
	for _, want := range []string{"quickdock/", "临时脚本", "文件性质"} {
		if !strings.Contains(got, want) {
			t.Errorf("提示里缺少关键约束 %q: %q", want, got)
		}
	}
	// run_command 也算写权限：脚本最终是它跑起来的。
	if got := workdirHintFor("/proj", withTool("run_command")); got == "" {
		t.Error("只有 run_command 时也应提工作文件约定（脚本就是它跑的）")
	}
}

// WP19：没挂 spawn_agent 就别讲派活。
func TestScaleHintConditions(t *testing.T) {
	if got := scaleHintFor(readOnlyTools()); got != "" {
		t.Errorf("没挂 spawn_agent 时不该讲派活: %q", got)
	}
	got := scaleHintFor(withTool(spawnAgentToolName))
	if got == "" {
		t.Fatal("挂了 spawn_agent 应追加任务规模提示")
	}
	// 这几句各自防一个已经踩过的坑：
	//   「串行分治也算」——U2 的直接成因（原描述把派活框成"并行"）；
	//   「默认把能独立完成的条目派出去」——模型几乎不派，是因为 planHint 说
	//     「直接继续做下一步」而 scaleHint 只软性说「再决定」，两边打架；
	//   「按条目性质选角色」——旧措辞把 code 角色框成「只有用户明确要才行」，
	//     等于把「改代码的独立条目」整类排除在派活之外；
	//   「一次性全派出去」——一次只派一个，比一个主任务还慢。
	for _, want := range []string{"串行分治", "默认把能独立完成的条目派出去", "按条目性质选角色", "一次性全派出去", "三步"} {
		if !strings.Contains(got, want) {
			t.Errorf("任务规模提示缺少 %q: %q", want, got)
		}
	}
	// 旧的压制措辞不能回来：它会让模型把改代码的活全留给自己。
	if strings.Contains(got, "只有用户明确要") || strings.Contains(got, "默认派只读角色") {
		t.Errorf("任务规模提示里仍有劝退派活的旧措辞: %q", got)
	}
}

// spawn_agent 的描述必须讲「规模」，且不能再出现会把模型劝退的旧措辞。
//
// 这是 U2 的回归测试：旧描述「适合可以拆开并行做的活」被读成
// 「不能同时跑就别派」，于是大任务全在主循环里硬跑。
func TestSpawnDescriptionMentionsScale(t *testing.T) {
	svc, _ := newTestAIService(t)
	// 传 nil 而不是 &ToolFilter{}：零值的 MaxLevel 是 0，含义是「只留只读工具」
	// （见 ToolFilter.MaxLevel 的注释），会把 spawn_agent 这类写类工具全过滤掉。
	sf := svc.aiSurface("chat", aiPermissionFull, "", nil)

	tool, ok := sf.Tool(spawnAgentToolName)
	if !ok {
		t.Fatalf("%s 不在工具面上（现有：%v）", spawnAgentToolName, sf.Names())
	}
	desc := tool.Description

	if !strings.Contains(desc, "三步") {
		t.Errorf("描述没有讲任务规模，模型仍然不知道该不该派: %q", desc)
	}
	if !strings.Contains(desc, "串行分治") {
		t.Errorf("描述必须明说「串行分治也算」: %q", desc)
	}
	if strings.Contains(desc, "适合可以拆开并行做的活") {
		t.Errorf("旧的『并行』措辞仍在，它正是 U2 的成因: %q", desc)
	}
	// 「不适合」那段必须保留：它不是劝退，是防止模型把需要人判断的事也派出去。
	if !strings.Contains(desc, "不适合") {
		t.Errorf("描述丢失了「不适合」的边界: %q", desc)
	}
}

// 两条提示不能出现在只读 + 无目录的极端组合里：那是最容易被加入噪音的场景。
func TestHintsQuietForReadOnlyNoCwd(t *testing.T) {
	tools := readOnlyTools()
	if got := workdirHintFor("", tools) + scaleHintFor(tools); got != "" {
		t.Errorf("只读且无目录时不该有任何行为约束提示: %q", got)
	}
}
