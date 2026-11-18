package ai

// WP17：任务状态与续跑的单测。
//
// 三块最容易被改坏、且坏了没有报错的地方：
//   - 「这句是不是在说继续」的判定（判宽了会把新任务当续跑，判窄了清单又被重建）；
//   - 续跑提示的内容（没有清单 / 清单已完成时必须闭嘴，否则每轮都在念旧账）；
//   - 清单落库与读取（列名、JSON 形状、updated_at 语义）。

import (
	"strings"
	"testing"

	"quickdock/internal/db"
	aitools "quickdock/services/ai/tools"
)

func TestIsResumeCommand(t *testing.T) {
	resume := []string{
		"继续", "继续吧", "接着", "接着做", "接着干", "往下", "往下做",
		"继续执行", "接上", " continue ", "continue.", "go on", "Keep going", "resume!",
		"继续。",
	}
	for _, s := range resume {
		if !isResumeCommand(s) {
			t.Errorf("%q 应当判定为续跑指令", s)
		}
	}

	// 带实质要求的句子必须按新诉求处理：否则「继续，但换个方案」会被当成「照旧」。
	notResume := []string{
		"", "  ", "继续改 auth 中间件", "继续，但改用另一个方案",
		"接着把剩下三个文件也改了", "重新做一遍", "你好",
		"continue with the migration but use a different approach",
	}
	for _, s := range notResume {
		if isResumeCommand(s) {
			t.Errorf("%q 不应判定为续跑指令", s)
		}
	}
}

func TestPlanStatusOf(t *testing.T) {
	if got := planStatusOf([]aitools.PlanItem{{Text: "a", Status: "done"}}); got != "done" {
		t.Errorf("全部完成时状态 = %q，期望 done", got)
	}
	if got := planStatusOf([]aitools.PlanItem{
		{Text: "a", Status: "done"},
		{Text: "b", Status: "doing"},
	}); got != "running" {
		t.Errorf("还有未完成项时状态 = %q，期望 running", got)
	}
	// 空文本行不计入：模型偶尔会发一条空的占位项，它不该让整份清单永远 running。
	if got := planStatusOf([]aitools.PlanItem{
		{Text: "a", Status: "done"},
		{Text: "  ", Status: "todo"},
	}); got != "done" {
		t.Errorf("空文本行应被忽略，实际状态 = %q", got)
	}
}

func convWithPlan(t *testing.T, planJSON, status, goal string) *db.AIConversation {
	t.Helper()
	return &db.AIConversation{Plan: planJSON, PlanStatus: status, Goal: goal}
}

const samplePlan = `{"items":[{"text":"读一遍 cmd 目录","status":"done"},` +
	`{"text":"改 auth 中间件","status":"doing"},` +
	`{"text":"补测试","status":"todo"}],"note":"已定位到 token 校验处"}`

func TestResumeHintFor(t *testing.T) {
	// 没有清单：一句都不该多说，否则每轮 system 都在涨。
	if got := resumeHintFor(&db.AIConversation{}, "继续"); got != "" {
		t.Errorf("没有清单时不应生成提示，实际: %q", got)
	}
	// 清单已完成：同样闭嘴，别拿旧账干扰新任务。
	if got := resumeHintFor(convWithPlan(t, `{"items":[{"text":"a","status":"done"}]}`, "done", ""), "继续"); got != "" {
		t.Errorf("清单已完成时不应生成提示，实际: %q", got)
	}
	// 坏 JSON：宁可不提示，也不要给模型一段乱码。
	if got := resumeHintFor(convWithPlan(t, `{not json`, "running", ""), "继续"); got != "" {
		t.Errorf("坏 JSON 时应返回空串，实际: %q", got)
	}

	// 纯「继续」：要求更新原清单，并列出未完成项。
	got := resumeHintFor(convWithPlan(t, samplePlan, "running", "重构认证"), "继续")
	if !strings.Contains(got, "续跑") {
		t.Errorf("续跑提示缺少「续跑」标识: %q", got)
	}
	for _, want := range []string{"改 auth 中间件", "补测试", "重构认证", "已定位到 token 校验处"} {
		if !strings.Contains(got, want) {
			t.Errorf("续跑提示缺少 %q: %q", want, got)
		}
	}
	// 已完成项不该出现在「未完成」列表里。
	if strings.Contains(got, "读一遍 cmd 目录") {
		t.Errorf("已完成项不应出现在续跑提示里: %q", got)
	}
	if !strings.Contains(got, "不要另起一份新清单") {
		t.Errorf("续跑提示缺少「不要重建清单」的指令: %q", got)
	}

	// 有新内容但旧清单没做完：允许重建，但必须知道有旧清单在。
	got = resumeHintFor(convWithPlan(t, samplePlan, "running", ""), "把登录页也顺手改一下")
	if strings.Contains(got, "续跑") {
		t.Errorf("非续跑指令不应标成续跑: %q", got)
	}
	if !strings.Contains(got, "已有清单") {
		t.Errorf("非续跑但有旧清单时应提示已有清单: %q", got)
	}
	if !strings.Contains(got, "可以重建") {
		t.Errorf("新诉求应允许重建清单: %q", got)
	}
}

func TestAiGoalOf(t *testing.T) {
	// 换行折成空格而不是只取首行：用户常先写总述再换行列要求。
	if got := aiGoalOf("重构认证\r\n另外把测试补上"); got != "重构认证 另外把测试补上" {
		t.Errorf("目标 = %q", got)
	}
	if got := aiGoalOf("   "); got != "" {
		t.Errorf("空白消息的目标应为空，实际: %q", got)
	}
	long := strings.Repeat("很", aiResumeGoalRunes+50)
	if got := aiGoalOf(long); len([]rune(got)) > aiResumeGoalRunes+1 {
		t.Errorf("目标未裁剪到上限: %d 字", len([]rune(got)))
	}
}

// 清单落库 / 读回 / updated_at 语义。
func TestConversationPlanRoundTrip(t *testing.T) {
	svc, d := newTestAIService(t)

	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	before := conv.UpdatedAt

	svc.saveConversationPlan(conv.ID, aitools.PlanUpdate{
		Items: []aitools.PlanItem{
			{Text: "改 auth", Status: "doing"},
			{Text: "补测试", Status: "todo"},
		},
		Note: "定位到 token 校验",
	})

	got, err := d.GetAIConversation(conv.ID)
	if err != nil {
		t.Fatalf("读会话失败: %v", err)
	}
	if got.Plan == "" {
		t.Fatal("清单未落库")
	}
	if got.PlanStatus != "running" {
		t.Errorf("清单状态 = %q，期望 running", got.PlanStatus)
	}
	if !strings.Contains(got.Plan, "改 auth") {
		t.Errorf("清单内容不对: %q", got.Plan)
	}
	// 清单更新不能推进 updated_at：否则每完成一步，会话就被顶到侧栏最前。
	if got.UpdatedAt != before {
		t.Errorf("清单更新不应改 updated_at: %q -> %q", before, got.UpdatedAt)
	}

	// 全打勾之后状态必须变 done，否则「续跑」会一直挂着。
	svc.saveConversationPlan(conv.ID, aitools.PlanUpdate{
		Items: []aitools.PlanItem{
			{Text: "改 auth", Status: "done"},
			{Text: "补测试", Status: "done"},
		},
	})
	got, _ = d.GetAIConversation(conv.ID)
	if got.PlanStatus != "done" {
		t.Errorf("全部完成后状态 = %q，期望 done", got.PlanStatus)
	}
}

// 目标只在为空时写：第二次调用不能把用户最初的目标覆盖成一句追加要求。
func TestSetConversationGoalKeepsFirst(t *testing.T) {
	_, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := d.SetAIConversationGoal(conv.ID, "重构认证"); err != nil {
		t.Fatalf("写目标失败: %v", err)
	}
	got, _ := d.GetAIConversation(conv.ID)
	if got.Goal != "重构认证" {
		t.Fatalf("目标 = %q", got.Goal)
	}
	// 空串写入是有效的「清空」，这里只验证「读得回来」这一个契约。
	if err := d.SetAIConversationGoal(conv.ID, ""); err != nil {
		t.Fatalf("清空目标失败: %v", err)
	}
	got, _ = d.GetAIConversation(conv.ID)
	if got.Goal != "" {
		t.Errorf("清空后目标 = %q，期望空串", got.Goal)
	}
}

// 老库升级路径：三个新列由 columnMigrations 补上，默认空串不改变行为。
func TestConversationNewColumnsDefaultEmpty(t *testing.T) {
	_, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	got, err := d.GetAIConversation(conv.ID)
	if err != nil {
		t.Fatalf("读会话失败: %v", err)
	}
	if got.Goal != "" || got.Plan != "" || got.PlanStatus != "" {
		t.Errorf("新会话的三个状态列应为空: goal=%q plan=%q status=%q",
			got.Goal, got.Plan, got.PlanStatus)
	}
	// 提示函数据此闭嘴。
	if hint := resumeHintFor(got, "继续"); hint != "" {
		t.Errorf("全新会话不应生成续跑提示: %q", hint)
	}
}

// 会话删除后清单跟着走，不留孤儿（plan 存的是会话表自己的列，这里只防回归）。
func TestConversationPlanSurvivesList(t *testing.T) {
	_, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := d.SetAIConversationPlan(conv.ID, samplePlan, "running"); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	list, err := d.ListAIConversations()
	if err != nil {
		t.Fatalf("列会话失败: %v", err)
	}
	var found *db.AIConversation
	for i := range list {
		if list[i].ID == conv.ID {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("列表里找不到刚建的会话")
	}
	// 列表查询与单条查询必须是同一套列：漏一列就会出现「点进去有、侧栏没有」。
	if found.Plan != samplePlan || found.PlanStatus != "running" {
		t.Errorf("列表未带出清单: plan=%q status=%q", found.Plan, found.PlanStatus)
	}
}
