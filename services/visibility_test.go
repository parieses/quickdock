package services

import "testing"

// AI 页可见标志是「完成通知要不要发」的第二个判据（见 services/ai/ai_stream.go），
// 它由前端在 KeepAlive 的 activated/deactivated 里写。
//
// 这里只测两件事：置位/清位能读到；Flags 未注入时不 panic —— 后者是真正的风险点，
// 通知链路上任何一次 panic 都会把整个流式请求带崩。
func TestAIPageVisibleFlag(t *testing.T) {
	app := &AppService{Flags: &WindowFlags{}}
	if app.AIPageVisible() {
		t.Fatal("默认应为 false")
	}
	app.SetAIPageVisible(true)
	if !app.AIPageVisible() {
		t.Fatal("置位后应读到 true")
	}
	app.SetAIPageVisible(false)
	if app.AIPageVisible() {
		t.Fatal("清位后应读到 false")
	}
}

func TestSetAIPageVisibleWithoutFlags(t *testing.T) {
	app := &AppService{}
	app.SetAIPageVisible(true) // 不应 panic
	if app.AIPageVisible() {
		t.Fatal("Flags 为 nil 时应恒为 false")
	}
	if res := app.SetAIPageVisible(true); res == nil || res.Code != 0 {
		t.Fatalf("应返回成功结果，得到 %+v", res)
	}
}
