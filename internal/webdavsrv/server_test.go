package webdavsrv

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/webdav"
)

// newTestHandler 构造一个指向临时目录的 guard 处理器（不真实监听端口）。
func newTestHandler(t *testing.T, readOnly bool) http.Handler {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	dav := &webdav.Handler{
		Prefix:     "/",
		FileSystem: webdav.Dir(root),
		LockSystem: webdav.NewMemLS(),
	}
	return guard(Config{Root: root, Username: "u", Password: "p", ReadOnly: readOnly}, dav)
}

func do(h http.Handler, method, target string, auth bool, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if auth {
		req.SetBasicAuth("u", "p")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestGuardRequiresAuth WebDAV 会把目录暴露到网络，无凭据必须一律 401。
func TestGuardRequiresAuth(t *testing.T) {
	h := newTestHandler(t, false)
	if code := do(h, "PROPFIND", "/", false, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("无凭据应 401，实际 %d", code)
	}
	if code := do(h, "PROPFIND", "/", true, "").Code; code == http.StatusUnauthorized {
		t.Fatal("凭据正确时不应再 401")
	}
}

// TestGuardRejectsBackslashPath 反斜杠是 Windows 的路径分隔符，而 path.Clean 不认它，
// 放行即可绕过规范化逃出共享根目录。
func TestGuardRejectsBackslashPath(t *testing.T) {
	h := newTestHandler(t, false)
	// %5C 解码后即反斜杠
	if code := do(h, "GET", "/..%5C..%5Cwindows%5Cwin.ini", true, "").Code; code != http.StatusBadRequest {
		t.Fatalf("含反斜杠的路径应 400，实际 %d", code)
	}
}

// TestGuardNormalizesDotDot 正斜杠的 .. 会被 Clean 归一化到根内，不会逃逸。
func TestGuardNormalizesDotDot(t *testing.T) {
	h := newTestHandler(t, false)
	if code := do(h, "GET", "/../../etc/passwd", true, "").Code; code == http.StatusOK {
		t.Fatal("路径穿越不应返回 200")
	}
}

// TestGuardReadOnly 只读模式拦截写方法，但读方法照常。
func TestGuardReadOnly(t *testing.T) {
	h := newTestHandler(t, true)
	if code := do(h, "PUT", "/new.txt", true, "x").Code; code != http.StatusForbidden {
		t.Fatalf("只读模式下 PUT 应 403，实际 %d", code)
	}
	if code := do(h, "DELETE", "/a.txt", true, "").Code; code != http.StatusForbidden {
		t.Fatalf("只读模式下 DELETE 应 403，实际 %d", code)
	}
	if code := do(h, "GET", "/a.txt", true, "").Code; code != http.StatusOK {
		t.Fatalf("只读模式下 GET 应 200，实际 %d", code)
	}
}

// TestGuardServesDirIndex x/net/webdav 不带 HTML 目录页，guard 需补一个，否则浏览器访问打不开。
func TestGuardServesDirIndex(t *testing.T) {
	h := newTestHandler(t, false)
	rec := do(h, "GET", "/sub/", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("目录页应 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("目录页 Content-Type 应为 text/html，实际 %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "a.txt") && !strings.Contains(rec.Body.String(), "..") {
		t.Fatalf("目录页内容异常: %s", rec.Body.String())
	}
}

// TestGuardAllowsAuthenticatedRead 认证 + 正常路径应能读到内容。
func TestGuardAllowsAuthenticatedRead(t *testing.T) {
	h := newTestHandler(t, false)
	rec := do(h, "GET", "/a.txt", true, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("读取文件失败: code=%d body=%q", rec.Code, rec.Body.String())
	}
}
