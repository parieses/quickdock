package ai

// WP8：事件载荷落库前必须脱敏。
//
// 为什么这条值得单独测：事件表是 WP1 新开的持久化面，而工具参数里天然会带
// `--token xxx`、Bearer、内嵌凭据的 URL。它们一旦进库，就会被轨迹抽屉原样
// 显示、也会随备份出到别处——而这些东西本来只该在那一次对话里存在。

import (
	"strings"
	"testing"

	"quickdock/internal/db"
)

func TestDbSinkRedactsSecrets(t *testing.T) {
	_, d := newTestAIService(t)
	run, err := d.CreateAgentRun(db.NewAgentRun{Goal: "脱敏测试", Trigger: "chat"})
	if err != nil {
		t.Fatalf("建 run 失败: %v", err)
	}

	sink := &dbSink{db: d, runID: run.ID}
	sink.Event("tool", map[string]any{
		"name": "run_command",
		"args": "--token sk-abcdefghijklmnop1234 --api-key AKIAIOSFODNN7EXAMPLE",
		"url":  "https://user:hunter2@example.com/x",
		"auth": "Bearer abcdefghijklmnop",
	})

	evs, err := d.ListAgentEvents(run.ID, 0, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("读事件失败: %v %d", err, len(evs))
	}
	p := evs[0].Payload

	// 三类凭据都不能以原文出现。
	for _, secret := range []string{"sk-abcdefghijklmnop1234", "AKIAIOSFODNN7EXAMPLE", "hunter2", "abcdefghijklmnop"} {
		if strings.Contains(p, secret) {
			t.Errorf("载荷里仍有明文凭据 %q: %s", secret, p)
		}
	}
	// 必须留下脱敏标记，而不是整条抹掉：轨迹要能看出「这里原本有东西」。
	if !strings.Contains(p, "REDACTED") {
		t.Errorf("缺少脱敏标记: %s", p)
	}
	// 非敏感字段一个不能少——脱敏是替换，不是删事件。
	for _, keep := range []string{"run_command", "name", "args"} {
		if !strings.Contains(p, keep) {
			t.Errorf("非敏感字段 %q 被误删: %s", keep, p)
		}
	}
}

// 正常内容不该被改动（否则轨迹会被一堆误伤的重写痕迹污染）。
func TestDbSinkKeepsPlainPayload(t *testing.T) {
	_, d := newTestAIService(t)
	run, err := d.CreateAgentRun(db.NewAgentRun{Goal: "正常载荷", Trigger: "chat"})
	if err != nil {
		t.Fatalf("建 run 失败: %v", err)
	}
	sink := &dbSink{db: d, runID: run.ID}
	sink.Event("tool", map[string]any{"name": "file_read", "args": `{"path":"src/main.go"}`})

	evs, _ := d.ListAgentEvents(run.ID, 0, 10)
	if len(evs) != 1 {
		t.Fatalf("事件条数不对: %d", len(evs))
	}
	if strings.Contains(evs[0].Payload, "REDACTED") {
		t.Errorf("普通载荷不该被改动: %s", evs[0].Payload)
	}
	if !strings.Contains(evs[0].Payload, "src/main.go") {
		t.Errorf("路径丢失: %s", evs[0].Payload)
	}
}
