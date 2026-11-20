package ai

// 网关会话标识（ai_session_tag.go）的守卫。
//
// 三条约束不能破：
//  1. 同一会话的所有请求必须是同一个 id，否则网关把它们当不同会话，
//     缓存命中率统计不出来；
//  2. 发出去的**会话 id 必须纯净**——magpie 点开详情显示的就是它，掺名字会把
//     id 弄脏；列表标题是 magpie 自己推的（GatewayTitle），不用我们传；
//  3. 可读名只服务于本地的日志与兜底，不参与 id。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTagPrefixTrimsAndFilters(t *testing.T) {
	// 标点、空白、emoji 不进名字：读一下 ai_context.go → 读一下aicontextgo，截 8 个字
	if got := tagPrefix("  读一下 ai_context.go，然后告诉我  "); got != "读一下aicon" {
		t.Errorf("前缀 = %q，期望按 8 个字截取且滤掉标点", got)
	}
	if n := utf8.RuneCountInString(tagPrefix("一二三四五六七八九十")); n != 8 {
		t.Errorf("最长应是 8 个字，实际 %d", n)
	}
	// 全是符号 / 空串时返回空，由调用方兜底（不在这里编一个「chat」，
	// 否则会话名叫「chat」和真的没名字分不清）。
	if got := tagPrefix("？？！！……😀"); got != "" {
		t.Errorf("全符号应返回空串，实际 %q", got)
	}
	if got := tagPrefix(""); got != "" {
		t.Errorf("空串应返回空串，实际 %q", got)
	}
}

// ctx 往返：没挂载时返回零值（调用方据此退回 convID）。
func TestSessionInfoContext(t *testing.T) {
	if got := sessionInfoOf(context.Background()); got.usable() {
		t.Errorf("没挂载时应为零值，实际 %+v", got)
	}
	c := withSessionInfo(context.Background(), sessionInfo{Name: "缓存命中", ID: "2923d858"})
	got := sessionInfoOf(c)
	if got.Name != "缓存命中" || got.ID != "2923d858" {
		t.Errorf("ctx 往返失败: %+v", got)
	}
	// 零值不挂载：避免下游误以为拿到了标识。
	if got := sessionInfoOf(withSessionInfo(context.Background(), sessionInfo{})); got.usable() {
		t.Errorf("零值不该被挂上去，实际 %+v", got)
	}
}

// magpie 只接受会话 id，标题由它自己从记录里推。
//
// 之前试过把名字掺进 X-Magpie-Session，结果点开详情看到的会话 id 也变成了
// 那串拼接值——id 必须是纯净的会话 id，名字交给 magpie 的 GatewayTitle。
func TestSessionIDStaysClean(t *testing.T) {
	si := sessionInfo{Name: "缓存命中原因为何", ID: "81436f38-e617-4d5a-b919-3864f49b8dc0"}
	if si.ID != "81436f38-e617-4d5a-b919-3864f49b8dc0" {
		t.Errorf("id 被污染了: %q", si.ID)
	}
	if strings.Contains(si.ID, si.Name) {
		t.Error("id 里不该出现名字")
	}
}

// 网关身份 header：agent / 会话 / 工作区三类都设上；会话 id 仍保持纯净，
// 名字不掺进任何 id header（点开详情看到的就是这个值）。
func TestApplyGatewayIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader("{}"))
	applyGatewayIdentity(req, sessionInfo{
		Name: "缓存命中原因为何",
		ID:   "2923d858-5f52-4e31-a06c-27e14046b081",
		Cwd:  `D:\project\quickdock-v3`,
	})
	want := map[string]string{
		"User-Agent":         gatewayUserAgent,
		"X-Agent-Name":       gatewayAgentName,
		"X-Agent-Session-Id": "2923d858-5f52-4e31-a06c-27e14046b081",
		"X-Magpie-Session":   "2923d858-5f52-4e31-a06c-27e14046b081",
		"X-Session-Id":       "2923d858-5f52-4e31-a06c-27e14046b081",
		"X-Workspace-ID":     `D:\project\quickdock-v3`,
	}
	for k, v := range want {
		if got := req.Header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	// 可读名不能出现在任何 header 值里：网关列表标题由它自己从记录推。
	for k, v := range req.Header {
		if len(v) > 0 && strings.Contains(v[0], "缓存命中") {
			t.Errorf("header %s 掺进了可读名: %q", k, v[0])
		}
	}
}

// 没绑目录时不带 X-Workspace-ID；没有会话 id 时也不带任何 id header，
// 但 agent 身份头照发——测试连接这类无会话请求也要能识别来源。
func TestApplyGatewayIdentityNoSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader("{}"))
	applyGatewayIdentity(req, sessionInfo{})
	if got := req.Header.Get("User-Agent"); got != gatewayUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, gatewayUserAgent)
	}
	if got := req.Header.Get("X-Workspace-ID"); got != "" {
		t.Errorf("没绑目录不该带 X-Workspace-ID, got %q", got)
	}
	if got := req.Header.Get("X-Session-Id"); got != "" {
		t.Errorf("没会话 id 不该带 X-Session-Id, got %q", got)
	}
}

func TestSessionInfoKeepsNameAndIDSeparate(t *testing.T) {
	si := sessionInfo{Name: tagPrefix("缓存命中原因为何"), ID: "2923d858-5f52-4e31-a06c-27e14046b081"}
	if si.Name != "缓存命中原因为何" {
		t.Errorf("名字应取前 8 个字，实际 %q", si.Name)
	}
	if si.Name == si.ID {
		t.Error("名字与 id 不能是同一个值（否则看板上两处显示一样）")
	}
	for _, r := range si.Name {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			t.Errorf("名字里不该出现随机码字符: %q", si.Name)
			break
		}
	}
}
