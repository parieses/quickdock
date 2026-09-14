package plugin

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	dlutil "quickdock/internal/dl"
	updatesvc "quickdock/services/update"
)

// TestApplyInstalledStatus 锁定市场页「已安装」的判定源为本地安装记录（DB 版本表）：
// 磁盘上有目录但未注册的插件必须显示为「未安装」——否则从 plugins/external
// 同步过去的开发产物会让市场页把所有插件都标成「已安装」。
func TestApplyInstalledStatus(t *testing.T) {
	mk := func(id, ver string) marketPlugin {
		return marketPlugin{ID: id, Version: ver, Platforms: []string{runtime.GOOS}}
	}
	unsupported := marketPlugin{ID: "e.other-platform", Version: "0.1.0", Platforms: []string{"plan9"}}
	plugins := []marketPlugin{
		mk("a.same", "0.1.0"),         // 本地同版本
		mk("b.older", "0.2.0"),        // 本地旧 → 提示更新
		mk("c.newer", "0.1.0"),        // 本地比远程新 → 不提示更新
		mk("d.on-disk-only", "0.1.0"), // 磁盘有目录但未注册 → 未安装
		unsupported,
	}
	installed := map[string]string{
		"a.same":  "0.1.0",
		"b.older": "0.1.0",
		"c.newer": "0.3.0",
	}
	applyInstalledStatus(plugins, installed)

	if p := plugins[3]; p.Installed || p.InstalledVersion != "" || p.HasUpdate {
		t.Fatalf("未注册插件应显示未安装: %+v", p)
	}
	for _, i := range []int{0, 1, 2} {
		if p := plugins[i]; !p.Installed || !p.Supported {
			t.Fatalf("%s 应显示已安装且平台支持: %+v", p.ID, p)
		}
	}
	if plugins[0].HasUpdate {
		t.Fatal("本地与远程同版本不应提示更新")
	}
	if !plugins[1].HasUpdate {
		t.Fatal("远程 0.2.0 > 本地 0.1.0 应提示更新")
	}
	if plugins[2].HasUpdate {
		t.Fatal("本地 0.3.0 > 远程 0.1.0 不应提示更新")
	}
	if plugins[1].InstalledVersion != "0.1.0" {
		t.Fatalf("已安装版本应取自本地记录: got %s", plugins[1].InstalledVersion)
	}
	if plugins[4].Supported {
		t.Fatal("非当前平台应判为不支持")
	}
}

// TestPluginDownloadCandidates 锁定镜像候选规则：GitHub URL 直连优先 + 镜像追加，
// 非 GitHub 源保持单一直连。镜像列表要与设置页更新共用（updatesvc.UpdateMirrorDefaults）。
func TestPluginDownloadCandidates(t *testing.T) {
	gh := "https://github.com/parieses/quickdock-plugins/releases/latest/download/x.zip"
	got := pluginDownloadCandidates(gh)
	want := len(updatesvc.UpdateMirrorDefaults) + 1
	if len(got) != want {
		t.Fatalf("GitHub URL 候选数: got %d, want %d", len(got), want)
	}
	if got[0] != gh {
		t.Fatalf("首位应为直连: got %s", got[0])
	}
	for i, m := range updatesvc.UpdateMirrorDefaults {
		if got[i+1] != m+gh {
			t.Fatalf("镜像 %d 拼接错误: got %s, want %s", i, got[i+1], m+gh)
		}
	}

	plain := "https://example.com/plugins/x.zip"
	if g := pluginDownloadCandidates(plain); len(g) != 1 || g[0] != plain {
		t.Fatalf("非 GitHub URL 应保持单一直连: got %v", g)
	}
}

// TestDownloadParallel 本地模拟 Range 服务器验证并发分块下载的完整性。
// 下载实现已抽到 internal/dl（services 与 services/plugin 共用），这里回归验证。
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
	var sum int64
	// 进度回调并发触发，累加需原子；DownloadParallelTo 每块回调一次
	if err := dlutil.DownloadParallelTo(http.DefaultClient, srv.URL, f, total, func(n int64) {
		atomic.AddInt64(&sum, n)
	}); err != nil {
		t.Fatalf("DownloadParallelTo: %v", err)
	}

	got, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("内容不一致: got %d bytes, want %d bytes", len(got), len(data))
	}
	if atomic.LoadInt64(&sum) != total {
		t.Fatalf("进度累计错误: got %d, want %d", atomic.LoadInt64(&sum), total)
	}
}
