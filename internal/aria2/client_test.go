package aria2

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// RPC 层用一个假的 RoundTripper 拦截，不起真实 aria2，也不碰本机端口。
// 这样 KeepAlive / 重试 / token 拼接都能按调用序号精确断言。
type stubRoundTripper struct {
	mu     sync.Mutex
	calls  []rpcRequest
	handle func(call rpcRequest, seq int) (int, string, error)
}

func (s *stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(req.Body)
	var call rpcRequest
	_ = json.Unmarshal(b, &call)
	s.mu.Lock()
	seq := len(s.calls)
	s.calls = append(s.calls, call)
	s.mu.Unlock()
	code, body, err := s.handle(call, seq)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (s *stubRoundTripper) at(i int) rpcRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

func (s *stubRoundTripper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func newStubClient(secret string, handle func(rpcRequest, int) (int, string, error)) (*Client, *stubRoundTripper) {
	st := &stubRoundTripper{handle: handle}
	return &Client{
		Base:   "http://stub.invalid/jsonrpc",
		Secret: secret,
		HTTP:   &http.Client{Transport: st},
	}, st
}

func okResult(raw string) string {
	return `{"jsonrpc":"2.0","id":"quickdock","result":` + raw + `}`
}

func TestCallInjectsTokenOnlyWhenSecretSet(t *testing.T) {
	c, st := newStubClient("sec", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusOK, okResult(`"1.37.0"`), nil
	})
	if _, err := c.Call("aria2.getVersion"); err != nil {
		t.Fatalf("Call 失败: %v", err)
	}
	params := st.at(0).Params
	if len(params) != 1 || params[0] != "token:sec" {
		t.Fatalf("带 secret 时参数应为 [token:sec]，实际 %v", params)
	}

	// 远程 aria2 常不设 secret：多带一个 token 前缀会让第一个位置参数被误读。
	c2, st2 := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusOK, okResult(`"1.37.0"`), nil
	})
	if _, err := c2.Call("aria2.getVersion"); err != nil {
		t.Fatalf("无 secret Call 失败: %v", err)
	}
	if len(st2.at(0).Params) != 0 {
		t.Fatalf("无 secret 时不应带 token，实际 %v", st2.at(0).Params)
	}
}

// Remove 必须按状态分派到两套 API：已结束的结果用 removeDownloadResult，
// 活跃/等待/暂停用 remove / forceRemove。混用会报 "Active Download not found"。
func TestRemoveDispatchesByStatus(t *testing.T) {
	cases := []struct {
		status string
		force  bool
		want   string
	}{
		{"active", false, "aria2.remove"},
		{"active", true, "aria2.forceRemove"},
		{"waiting", false, "aria2.remove"},
		{"paused", false, "aria2.remove"},
		{"complete", false, "aria2.removeDownloadResult"},
		{"error", false, "aria2.removeDownloadResult"},
		{"error", true, "aria2.removeDownloadResult"},
		{"removed", false, "aria2.removeDownloadResult"},
	}
	for _, c := range cases {
		t.Run(c.status+fmt.Sprintf("/force=%v", c.force), func(t *testing.T) {
			status := c.status
			cl, st := newStubClient("sec", func(call rpcRequest, _ int) (int, string, error) {
				if call.Method == "aria2.tellStatus" {
					return http.StatusOK, okResult(fmt.Sprintf(`{"status":%q}`, status)), nil
				}
				return http.StatusOK, okResult(`"OK"`), nil
			})
			if err := cl.Remove("GID#1", c.force); err != nil {
				t.Fatalf("Remove 失败: %v", err)
			}
			if got := st.count(); got != 2 {
				t.Fatalf("应有 tellStatus + 删除两次调用，实际 %d", got)
			}
			if got := st.at(1).Method; got != c.want {
				t.Errorf("status=%q force=%v 走 %q，应为 %q", c.status, c.force, got, c.want)
			}
			if got := st.at(1).Params; len(got) < 2 || got[0] != "token:sec" || got[1] != "GID#1" {
				t.Errorf("删除参数不对: %v", got)
			}
		})
	}
}

func TestRemoveTreatsMissingGIDAsSuccess(t *testing.T) {
	cl, st := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusOK,
			`{"jsonrpc":"2.0","id":"quickdock","error":{"code":1,"message":"Active Download not found for GID#gone"}}`, nil
	})
	if err := cl.Remove("GID#gone", false); err != nil {
		t.Fatalf("任务已不存在应视为删除成功，实际: %v", err)
	}
	if got := st.count(); got != 1 {
		t.Fatalf("查不到 GID 时不应再发删除指令，实际调用 %d 次", got)
	}
}

// 只有「只读且幂等」的方法允许重试一次；写方法重试会重复建任务/重复删除。
func TestReadMethodRetriesTransportErrorOnce(t *testing.T) {
	cl, st := newStubClient("", func(_ rpcRequest, seq int) (int, string, error) {
		if seq == 0 {
			return 0, "", errors.New("connection reset")
		}
		return http.StatusOK, okResult(`"1.37.0"`), nil
	})
	if _, err := cl.Call("aria2.getVersion"); err != nil {
		t.Fatalf("只读方法应在网络错误后重试成功，实际: %v", err)
	}
	if got := st.count(); got != 2 {
		t.Errorf("只读方法应恰好调用 2 次，实际 %d", got)
	}
}

func TestWriteMethodDoesNotRetry(t *testing.T) {
	cl, st := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return 0, "", errors.New("connection reset")
	})
	if err := cl.Pause("GID#1"); err == nil {
		t.Fatal("网络失败应返回错误")
	}
	if got := st.count(); got != 1 {
		t.Errorf("写方法绝不能重试，实际调用 %d 次", got)
	}
}

func TestBusinessErrorIsNotRetried(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":"quickdock","error":{"code":-32602,"message":"Invalid params"}}`
	cl, st := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusOK, body, nil
	})
	if _, err := cl.Call("aria2.getVersion"); err == nil {
		t.Fatal("aria2 业务错误应返回 error")
	}
	if got := st.count(); got != 1 {
		t.Errorf("业务错误不该重试，实际调用 %d 次", got)
	}
}

func TestNon200ResponseIsNotRetried(t *testing.T) {
	cl, st := newStubClient("", func(_ rpcRequest, _ int) (int, string, error) {
		return http.StatusInternalServerError, "boom", nil
	})
	if _, err := cl.Call("aria2.getVersion"); err == nil {
		t.Fatal("HTTP 500 应返回 error")
	}
	if got := st.count(); got != 1 {
		t.Errorf("HTTP 错误不该重试，实际调用 %d 次", got)
	}
}

func TestStatusKeysCoverProgressFields(t *testing.T) {
	// 1s 快路径只问这些 key：漏掉一个就会让列表里的进度/速度静默归零。
	keys := map[string]bool{}
	for _, k := range statusKeys {
		keys[k] = true
	}
	for _, k := range []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "uploadSpeed", "errorMessage", "files", "bittorrent"} {
		if !keys[k] {
			t.Errorf("statusKeys 缺 %q", k)
		}
	}
}
