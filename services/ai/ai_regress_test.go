package ai

// 用户实测反馈里的四条修复的守卫。
//
// 这四条的共同点：都不会报错，只会让人以为「这软件坏了」——
// 预算悄悄断掉、压缩提示音一直响、轨迹永远是空、脚本落到项目根。

import (
	"strings"
	"testing"
	"time"

	"quickdock/internal/db"
)

// 摘要失败后必须进入冷却：否则每轮都白付一次模型调用 + 弹一条「摘要失败」。
func TestSummaryCooldown(t *testing.T) {
	resetSummaryCooldown()
	t.Cleanup(resetSummaryCooldown)

	const conv = "conv-cooldown"
	if summaryCoolingDown(conv) {
		t.Fatal("没失败过就不该在冷却中")
	}
	markSummaryFailure(conv)
	if !summaryCoolingDown(conv) {
		t.Fatal("失败后应进入冷却")
	}
	// 成功一次就清掉：下一次仍然给摘要机会。
	clearSummaryFailure(conv)
	if summaryCoolingDown(conv) {
		t.Error("成功后应退出冷却")
	}
	// 空会话 id 不该命中任何记录（也不该 panic）。
	if summaryCoolingDown("") {
		t.Error("空 convID 不该在冷却中")
	}
	markSummaryFailure("")
	if summaryCoolingDown("") {
		t.Error("空 convID 不该被记录")
	}
}

// 轨迹开关：默认开；关掉后不建 run（一条记录都不留）。
func TestTraceEnabledSwitch(t *testing.T) {
	svc, d := newTestAIService(t)
	if !svc.traceEnabled() {
		t.Fatal("默认应当开启轨迹")
	}
	for _, off := range []string{"0", "false", "off", "no"} {
		if err := d.SetSetting(aiTraceEnabledKey, off); err != nil {
			t.Fatalf("写设置失败: %v", err)
		}
		if svc.traceEnabled() {
			t.Errorf("%q 应关闭轨迹", off)
		}
	}
	// 认不出的值按开启处理（默认值优先于「猜」）。
	if err := d.SetSetting(aiTraceEnabledKey, "maybe"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	if !svc.traceEnabled() {
		t.Error("无法识别的取值应回落到默认（开）")
	}
}

// 关掉轨迹后 ensureRun 必须返回空、且库里不留 run。
func TestEnsureRunSkippedWhenTraceDisabled(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := d.SetSetting(aiTraceEnabledKey, "0"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	sc := &agentScope{svc: svc, convID: conv.ID, goal: "x", cwd: t.TempDir()}
	runID, nodeID, err := sc.ensureRun()
	if err != nil {
		t.Fatalf("关闭轨迹时不该报错: %v", err)
	}
	if runID != "" || nodeID != "" {
		t.Errorf("关闭轨迹时不该建 run: %q %q", runID, nodeID)
	}
	runs, _ := d.ListAgentRunsByConv(conv.ID, 20)
	if len(runs) != 0 {
		t.Errorf("关闭轨迹后库里不该有 run: %d", len(runs))
	}
}

// done 事件必须带 runId：普通对话（不派活）的轨迹入口全靠它。
func TestDoneEventCarriesRunID(t *testing.T) {
	base := map[string]any{"convId": "c1"}
	ev := withRunID(base, "  run-abc  ")
	if got := ev["runId"]; got != "run-abc" {
		t.Errorf("runId 应被去空格后写入: %v", got)
	}
	// 空 runID 不写字段：前端据此不回退到「打开一个空轨迹」。
	ev2 := withRunID(map[string]any{}, "")
	if _, ok := ev2["runId"]; ok {
		t.Error("空 runID 不该写字段")
	}
}

// AILatestRun 给刷新后的页面补 runId。
func TestLatestRunBinding(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if res := svc.AILatestRun(conv.ID); res.Code != 0 || res.Data != nil {
		t.Errorf("没有 run 时应返回 nil 数据: %+v", res)
	}
	run, err := d.CreateAgentRun(db.NewAgentRun{ConvID: conv.ID, Goal: "任务", Trigger: "chat"})
	if err != nil {
		t.Fatalf("建 run 失败: %v", err)
	}
	res := svc.AILatestRun(conv.ID)
	got, ok := res.Data.(*db.AgentRun)
	if !ok || got == nil {
		t.Fatalf("应返回 run: %#v", res.Data)
	}
	if got.ID != run.ID {
		t.Errorf("返回的 run 不对: %s != %s", got.ID, run.ID)
	}
	if res := svc.AILatestRun(""); res.Code == 0 {
		t.Error("空 convID 应当失败")
	}
}

// 辅助文件目录必须是点目录，且清理只碰 tmp/（不能连带删记忆与规则）。
func TestScratchDirIsDottedAndIsolated(t *testing.T) {
	if !strings.HasPrefix(aiScratchDirName, ".") {
		t.Errorf("辅助目录必须是点目录（不能污染 git status）: %q", aiScratchDirName)
	}
	// 与记忆/规则所在的目录同名：两个包之间没有共享常量，靠这条钉住。
	if aiScratchDirName != memoryDirName {
		t.Errorf("辅助目录 %q 与记忆目录 %q 不一致", aiScratchDirName, memoryDirName)
	}
	if aiScratchSubDir == "" || aiScratchSubDir == "." {
		t.Error("辅助文件必须有独立子目录，否则清理会碰到 memory/ 与 rules/")
	}
	// 清理目标必须落在 tmp 之下，而不是 .quickdock 本身。
	target := aiScratchSubDir
	if target == aiScratchDirName {
		t.Error("清理目标不能是 .quickdock 根目录")
	}
	_ = time.Now
}
