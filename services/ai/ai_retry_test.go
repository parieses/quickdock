package ai

// WP15：上游 429/5xx 重试，以及 AI 请求的 User-Agent 注入。
//
// 这两条都属于「本地毫无症状、只有服务商后台看得出来」的问题：重试缺失只在
// 长任务被限流时暴露，UA 只在网关日志/风控里暴露。所以用 httptest 把行为钉死。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"quickdock/services"
)

// sseOK 是一段最小的成功流：一条 content 增量 + [DONE]。
const sseOK = `data: {"choices":[{"delta":{"content":"ok"}}]}

data: [DONE]

`

// newStreamTestService 构造一个只连 httptest 的 AIService。
func newStreamTestService(srv *httptest.Server) *AIService {
	return &AIService{
		App:          &services.AppService{},
		aiHTTPClient: srv.Client(),
	}
}

func drainStream(t *testing.T, svc *AIService, baseURL string) (chatTurn, error) {
	t.Helper()
	return svc.streamAIChat(
		context.Background(),
		AIProfile{Model: "test-model", Provider: "openai", BaseURL: baseURL},
		[]map[string]interface{}{{"role": "user", "content": "hi"}},
		"conv-test",
		nil,
		func(string) {},
		func(string) {},
	)
}

// 第一次 429（带 Retry-After: 0）、第二次 200：应当自动重试并拿到完整回复。
func TestStreamRetriesOn429(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseOK))
	}))
	defer srv.Close()

	turn, err := drainStream(t, newStreamTestService(srv), srv.URL)
	if err != nil {
		t.Fatalf("429 后应重试成功，实际失败: %v", err)
	}
	if turn.Content != "ok" {
		t.Errorf("回复内容 = %q，期望 %q", turn.Content, "ok")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("请求次数 = %d，期望 2（首发 + 一次重试）", got)
	}
}

// 401 是鉴权失败：重试一万次也一样，必须只打一次。
func TestStreamNoRetryOn401(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	_, err := drainStream(t, newStreamTestService(srv), srv.URL)
	if err == nil {
		t.Fatal("401 应当返回错误")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误信息应含状态码 401，实际: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("401 不应重试，实际请求 %d 次", got)
	}
}

// 连续 429：重试次数用尽后给出可读错误，且总请求数是 1 + aiRetryMax。
func TestStreamGivesUpAfterRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer srv.Close()

	_, err := drainStream(t, newStreamTestService(srv), srv.URL)
	if err == nil {
		t.Fatal("持续 503 最终应返回错误")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("错误信息应含状态码 503，实际: %v", err)
	}
	if want := int32(1 + aiRetryMax); atomic.LoadInt32(&hits) != want {
		t.Errorf("请求次数 = %d，期望 %d", hits, want)
	}
}

// 可重试状态码与不可重试状态码的边界。
func TestRetryableStatus(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504}
	for _, code := range retryable {
		if !aiRetryableStatus(code) {
			t.Errorf("%d 应当可重试", code)
		}
	}
	// 400/401/403/404 都是「改了也没用」的错，重试只会拖长等待。
	for _, code := range []int{400, 401, 403, 404, 422} {
		if aiRetryableStatus(code) {
			t.Errorf("%d 不应重试", code)
		}
	}
}

// User-Agent 必须被注入：Go 默认的 "Go-http-client/1.1" 会被网关归成脚本流量。
func TestUserAgentInjected(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &userAgentTransport{
		base: http.DefaultTransport,
		ua:   func() string { return "QuickDock/9.9.9" },
	}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	_ = resp.Body.Close()
	if gotUA != "QuickDock/9.9.9" {
		t.Errorf("User-Agent = %q，期望 %q", gotUA, "QuickDock/9.9.9")
	}
}

// 调用方自己设了 UA 时不能被覆盖（测试与将来某家要求特定 UA 的场景都靠它）。
func TestUserAgentRespectsExplicit(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &userAgentTransport{
		base: http.DefaultTransport,
		ua:   func() string { return "QuickDock/9.9.9" },
	}}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("建请求失败: %v", err)
	}
	req.Header.Set("User-Agent", "custom-agent/1.0")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	_ = resp.Body.Close()
	if gotUA != "custom-agent/1.0" {
		t.Errorf("显式 UA 被覆盖为 %q", gotUA)
	}
}

// 版本号为空时（单测 / 早期初始化）仍要产出一个可用的 UA，不能变成 "QuickDock/"。
func TestUserAgentFallbackVersion(t *testing.T) {
	if got := aiUserAgentFor(nil); got != "QuickDock/0.0.0" {
		t.Errorf("nil App 时 UA = %q，期望 QuickDock/0.0.0", got)
	}
	if got := aiUserAgentFor(&services.AppService{AppVersion: "1.2.3"}); got != "QuickDock/1.2.3" {
		t.Errorf("带版本时 UA = %q，期望 QuickDock/1.2.3", got)
	}
}
