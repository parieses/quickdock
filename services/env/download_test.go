package env

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestDownloadSingleURL 验证基本下载：内容正确、进度回调收到写入字节数。
func TestDownloadSingleURL(t *testing.T) {
	payload := []byte("hello-quickdock-download")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.bin")
	var gotWritten int64
	if err := Download(context.Background(), dst, []string{srv.URL}, func(w, _ int64) {
		gotWritten = w
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Fatalf("content mismatch: got %q", data)
	}
	if gotWritten != int64(len(payload)) {
		t.Fatalf("progress written=%d want %d", gotWritten, len(payload))
	}
}

// TestDownloadFallbackOnFailure 验证 urls 顺序回退：首个 404，第二个成功。
func TestDownloadFallbackOnFailure(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer good.Close()

	dst := filepath.Join(t.TempDir(), "out.bin")
	if err := Download(context.Background(), dst, []string{bad.URL, good.URL}, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "ok" {
		t.Fatalf("expected fallback content, got %q", data)
	}
}

// TestDownloadAllFail 验证全部失败时返回错误且不落盘（临时文件机制，不污染目标）。
func TestDownloadAllFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	dst := filepath.Join(t.TempDir(), "out.bin")
	if err := Download(context.Background(), dst, []string{bad.URL}, nil); err == nil {
		t.Fatal("expected error when all URLs fail")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("dst should not exist after failed download")
	}
}
