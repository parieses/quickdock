package services

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPluginDownloadCandidates 锁定镜像候选规则：GitHub URL 直连优先 + 镜像追加，
// 非 GitHub 源保持单一直连。镜像列表要与设置页更新共用（updateMirrorDefaults）。
func TestPluginDownloadCandidates(t *testing.T) {
	gh := "https://github.com/parieses/quickdock-plugins/releases/latest/download/x.zip"
	got := pluginDownloadCandidates(gh)
	want := len(updateMirrorDefaults) + 1
	if len(got) != want {
		t.Fatalf("GitHub URL 候选数: got %d, want %d", len(got), want)
	}
	if got[0] != gh {
		t.Fatalf("首位应为直连: got %s", got[0])
	}
	for i, m := range updateMirrorDefaults {
		if got[i+1] != m+gh {
			t.Fatalf("镜像 %d 拼接错误: got %s, want %s", i, got[i+1], m+gh)
		}
	}

	plain := "https://example.com/plugins/x.zip"
	if g := pluginDownloadCandidates(plain); len(g) != 1 || g[0] != plain {
		t.Fatalf("非 GitHub URL 应保持单一直连: got %v", g)
	}
}

// TestDownloadParallel 本地模拟 Range 服务器验证并发分块下载的完整性
func TestDownloadParallel(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 128*1024) // 2MB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		rng := r.Header.Get("Range")
		if !strings.HasPrefix(rng, "bytes=") {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data)
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if start < 0 || end >= len(data) || start > end {
			http.Error(w, "range out of bounds", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start : end+1])
	}))
	defer srv.Close()

	tmpFile := filepath.Join(t.TempDir(), "chunk-test.bin")
	f, err := os.Create(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	total := int64(len(data))
	tr := &progressTracker{total: total}
	if err := downloadParallelTo(http.DefaultClient, srv.URL, f, total, tr.add); err != nil {
		t.Fatalf("downloadParallelTo: %v", err)
	}

	got, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("内容不一致: got %d bytes, want %d bytes", len(got), len(data))
	}
	if tr.done != total {
		t.Fatalf("进度累计错误: got %d, want %d", tr.done, total)
	}
}
