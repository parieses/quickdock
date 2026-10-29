package update

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	dlutil "quickdock/internal/dl"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// TestMirrorProviderDownloadResume 验证断点续传：第一条分块请求下到一半被掐断
// （模拟网络中断 / 3 分钟超时）后，重试应从断点继续而不是整块重下。
//
// 断言的落点是「请求的 Range 起点不在块边界上」——起点落在块中间只可能是续传，
// 从头重下的话每条 Range 都会对齐到 chunk 边界。
func TestMirrorProviderDownloadResume(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 192*1024) // 3MB
	chunk := int64((len(data) + dlutil.DownloadConcurrency - 1) / dlutil.DownloadConcurrency)

	dir := t.TempDir()
	var rangeReqs atomic.Int32
	var resumed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		rng := r.Header.Get("Range")
		if !strings.HasPrefix(rng, "bytes=") {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data)
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if int64(start)%chunk != 0 {
			resumed.Add(1)
		}
		if rangeReqs.Add(1) == 1 {
			// 第一条分块请求只给一半就硬断连接：客户端应读到意外的 EOF
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.WriteHeader(http.StatusPartialContent)
			half := start + (end-start+1)/2
			_, _ = w.Write(data[start:half])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
	defer srv.Close()

	rel := &updater.Release{
		Metadata: map[string]any{"endpoint.artifact.url": srv.URL},
		Artifact: updater.Artifact{Size: int64(len(data))},
	}
	p := NewMirrorProvider(MirrorConfig{Client: &http.Client{}, CacheDir: dir})

	var buf bytes.Buffer
	if err := p.Download(context.Background(), rel, &buf, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("内容不一致: got %d bytes, want %d bytes", buf.Len(), len(data))
	}
	if resumed.Load() == 0 {
		t.Fatal("没有观察到续传请求：中断后仍从块起点重下")
	}
	// 成功收尾时要清掉缓存，否则下次更新会读到过期的半成品
	if _, ok := dlutil.ReadResume(filepath.Join(dir, "pending.state")); ok {
		t.Fatal("下载完成后续传状态未清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "pending.part")); err == nil {
		t.Fatal("下载完成后缓存文件未清理")
	}
}

// TestMirrorProviderDownloadKeepsPartialOnFailure 验证下载失败时半成品被保留：
// 这是「重试可续」的前提。探测成功、分块请求只给了半截就 500，此时缓存文件
// 与续传状态都要留着，且绝不向 dst 写入（半份数据会让 wails 的哈希摘要失效）。
func TestMirrorProviderDownloadKeepsPartialOnFailure(t *testing.T) {
	dir := t.TempDir()
	data := bytes.Repeat([]byte("y"), 4096)
	var rangeReqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		if !strings.HasPrefix(r.Header.Get("Range"), "bytes=") {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data)
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if rangeReqs.Add(1) == 1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start : start+(end-start+1)/2])
			w.(http.Flusher).Flush()
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	rel := &updater.Release{
		Metadata: map[string]any{"endpoint.artifact.url": srv.URL},
		Artifact: updater.Artifact{Size: int64(len(data))},
	}
	p := NewMirrorProvider(MirrorConfig{Client: &http.Client{}, CacheDir: dir})

	var buf bytes.Buffer
	if err := p.Download(context.Background(), rel, &buf, nil); err == nil {
		t.Fatal("分块请求一直 500，下载不该成功")
	}
	if buf.Len() != 0 {
		t.Fatalf("失败时不该向 dst 写入数据，实际写了 %d 字节", buf.Len())
	}
	st, ok := dlutil.ReadResume(filepath.Join(dir, "pending.state"))
	if !ok || st.Download() <= 0 {
		t.Fatalf("失败后应保留续传状态与已下字节: ok=%v state=%+v", ok, st)
	}
}
