package linkcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 状态判定是死链功能唯一会出错的地方：判宽了满屏假死链，判严了真死链看不见。
// 这里把每一档都钉死。
func TestCheckStatusMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html><title>t</title>body</html>"))
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "/boom":
			w.WriteHeader(http.StatusInternalServerError)
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	cases := []struct {
		path string
		want Status
		code int
	}{
		{"/ok", StatusOK, 200},
		{"/gone", StatusBroken, 404},
		{"/forbidden", StatusBlocked, 403},
		{"/boom", StatusError, 500},
		// 跳转到同一主机的不同路径 → 视为地址已变（收藏的地址确实指错了地方）
		{"/moved", StatusMoved, 200},
	}
	for _, c := range cases {
		res := Check(context.Background(), srv.URL+c.path, false)
		if res.Status != c.want {
			t.Errorf("%s: status = %s，期望 %s（code=%d）", c.path, res.Status, c.want, res.Code)
		}
		if res.Code != c.code {
			t.Errorf("%s: code = %d，期望 %d", c.path, res.Code, c.code)
		}
	}
}

// 非法地址不该发请求，直接归到 error。
func TestCheckRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"javascript:void(0)", "file:///c:/x", "", "not a url"} {
		if res := Check(context.Background(), u, false); res.Status != StatusError {
			t.Errorf("%q 应判为 error，实际 %s", u, res.Status)
		}
	}
}

// 深度检查要给出稳定的内容摘要：同内容两次结果必须一致，否则「内容已变」全是误报。
func TestCheckContentHashStable(t *testing.T) {
	body := "<html><body>hello</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	a := Check(context.Background(), srv.URL, true)
	b := Check(context.Background(), srv.URL, true)
	if a.ContentHash == "" {
		t.Fatal("深度检查未产出摘要")
	}
	if a.ContentHash != b.ContentHash {
		t.Errorf("同内容摘要不一致: %s vs %s", a.ContentHash, b.ContentHash)
	}
	// 非深度检查不抓正文
	if c := Check(context.Background(), srv.URL, false); c.ContentHash != "" {
		t.Errorf("非深度检查不该有摘要")
	}
}
