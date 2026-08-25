package services

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// TestMirrorProviderDownloadParallel 验证设置页"检查更新"下载路径：
// mirrorProvider 对支持 Range 的大包（>parallelMinSize）应走 4 连接分块下载，
// 内容逐字节一致、进度回调有上报、Range 请求确实发出 4 段。
func TestMirrorProviderDownloadParallel(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 192*1024) // 3MB
	var rangeReqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		rng := r.Header.Get("Range")
		if !strings.HasPrefix(rng, "bytes=") {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data)
			return
		}
		rangeReqs.Add(1)
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

	rel := &updater.Release{
		Metadata: map[string]any{"endpoint.artifact.url": srv.URL},
		Artifact: updater.Artifact{Size: int64(len(data))},
	}
	p := NewMirrorUpdaterProvider(nil, &http.Client{})

	var buf bytes.Buffer
	progressCalls := 0
	var maxWritten int64
	err := p.Download(context.Background(), rel, &buf, func(written, total int64) {
		progressCalls++
		maxWritten = written
		if total != int64(len(data)) {
			t.Fatalf("进度总量错误: got %d, want %d", total, len(data))
		}
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("内容不一致: got %d bytes, want %d bytes", buf.Len(), len(data))
	}
	if n := rangeReqs.Load(); n != downloadConcurrency {
		t.Fatalf("Range 请求数错误: got %d, want %d（未走并行分块?）", n, downloadConcurrency)
	}
	if progressCalls == 0 || maxWritten <= 0 || maxWritten > int64(len(data)) {
		t.Fatalf("进度回调异常: calls=%d max=%d", progressCalls, maxWritten)
	}
}
