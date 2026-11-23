package env

import (
	"path/filepath"
	"testing"
	"time"
)

// resetVersCacheForTest 清空进程内缓存，模拟「应用重启」。
func resetVersCacheForTest() {
	versCacheMu.Lock()
	versCache = make(map[string]versCacheEntry)
	versCacheRead = false
	versInflight = make(map[string]bool)
	versCacheMu.Unlock()
}

// useTempVersCache 把缓存目录指到临时目录，避免测试写到真实数据目录。
func useTempVersCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := versCacheFilePath
	versCacheFilePath = func() string { return filepath.Join(dir, "verscache.json") }
	resetVersCacheForTest()
	t.Cleanup(func() {
		versCacheFilePath = old
		resetVersCacheForTest()
	})
}

// TestVersCachePersistRoundTrip 落盘后再读回来：这就是「重启应用后总览秒出」的路径。
func TestVersCachePersistRoundTrip(t *testing.T) {
	useTempVersCache(t)
	now := time.Now()
	key := versCacheKey(RuntimeNode, "", "")

	versCachePut(key, RuntimeNode, []string{"v24.22.1", "v22.23.4"}, true, now)

	resetVersCacheForTest() // 丢掉内存，只靠磁盘
	e, ok := versCacheGet(key)
	if !ok {
		t.Fatal("重新加载后取不到缓存")
	}
	if len(e.vs) != 2 || e.vs[0] != "v24.22.1" {
		t.Fatalf("版本列表不对: %v", e.vs)
	}
	if !e.ok {
		t.Error("上游成功的条目读回来变成了兜底条目")
	}
	// 时间戳按毫秒落盘，读回来会有亚毫秒级误差。
	if got := e.expired.Sub(now); got < versTTL-time.Millisecond || got > versTTL {
		t.Errorf("成功条目的有效期应约为 %v，实际 %v", versTTL, got)
	}
}

// TestVersCacheFailureShortTTL 失败也要缓存，但只记 10 分钟，避免「上游不通 →
// 每次打开总览都重试一遍」。
func TestVersCacheFailureShortTTL(t *testing.T) {
	useTempVersCache(t)
	now := time.Now()
	key := versCacheKey(RuntimeGo, "", "")

	versCachePut(key, RuntimeGo, []string{"1.27.3"}, false, now)
	e, ok := versCacheGet(key)
	if !ok {
		t.Fatal("兜底清单也应缓存")
	}
	if e.ok {
		t.Error("上游失败被记成了成功")
	}
	if got := e.expired.Sub(now); got != versFailTTL {
		t.Errorf("失败条目的有效期应为 %v，实际 %v", versFailTTL, got)
	}
}

// TestVersCacheGetReturnsCopy 调用方改返回值不能污染缓存。
func TestVersCacheGetReturnsCopy(t *testing.T) {
	useTempVersCache(t)
	key := versCacheKey(RuntimePHP, "", "")
	versCachePut(key, RuntimePHP, []string{"8.5.12", "8.4.27"}, true, time.Now())

	e, _ := versCacheGet(key)
	e.vs[0] = "0.0.0"

	again, _ := versCacheGet(key)
	if again.vs[0] != "8.5.12" {
		t.Fatalf("缓存被调用方改坏了: %v", again.vs)
	}
}
