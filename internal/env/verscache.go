package env

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// 上游版本列表缓存：内存 + 落盘 + 后台刷新。
//
// 为什么必须落盘：上游列表原本只有进程内缓存，重启即失效，而环境管理的
// 「更新总览」一次要对二十多个运行时问最新版本。全量同步打上游时，GitHub API
// 的匿名限额（60 次/小时/IP）一次打开就能撞穿，撞穿后各运行时退回内置推荐
// 清单——界面表现为「最新版比已装的还旧」「明明有新版本却显示已是最新」，
// 而且每次进页面都要重新等一遍。
//
// 现在的分工：
//   - 读：命中即返回（过期但有旧值也先给，界面秒出，不阻塞在网络上）；
//   - 写：每次拉取的结果（含失败）都更新内存与磁盘，失败用短 TTL 记，
//     避免「上游不通 → 每次打开总览都重试一遍」；
//   - 刷新：StartVersionRefresher 在后台按 TTL 补拉，完成后经 SetVersionsUpdateHook
//     推事件给前端，界面随之更新。

// versTTL 上游拉取成功后的有效期。
const versTTL = 1 * time.Hour

// versFailTTL 拉取失败（只剩内置兜底清单）的记录时长。
// 失败也要记：不记的话每次打开总览都会对不可达的源再试一遍，这正是
// 「每次都新检测」的由来。
const versFailTTL = 10 * time.Minute

// versRefreshInterval 后台刷新的巡检周期（只刷新已过期的项）。
const versRefreshInterval = 30 * time.Minute

// versRefreshWorkers 后台刷新的并发上限。十几个 GitHub API 同时打会互相拖慢，
// 也更容易一起撞上匿名限额。
const versRefreshWorkers = 3

const versCacheFileRel = "env/verscache.json"

// versCacheEntry 单个缓存项（只存「拉取到的结果」，过期判断靠 expired）。
type versCacheEntry struct {
	vs        []string
	fetchedAt time.Time
	expired   time.Time
	ok        bool // true=上游拉取成功；false=内置兜底清单（短 TTL）
}

var (
	versCacheMu   sync.RWMutex
	versCache     = make(map[string]versCacheEntry)
	versCacheRead bool                    // 磁盘缓存是否已加载（每进程一次）
	versInflight  = make(map[string]bool) // 正在拉取的 key，防后台刷新与页面查询各拉一遍

	// versPersistMu 串行化落盘：后台刷新是并发的（3 个 worker），共用一个 .tmp
	// 路径时两个 goroutine 交错写会写出半截 JSON，rename 也会互相踩。
	versPersistMu sync.Mutex

	versHookMu sync.RWMutex
	versHook   func(rt Runtime, vs []string)
)

// SetVersionsUpdateHook 注册「版本列表已刷新」回调，宿主据此推事件给前端。
func SetVersionsUpdateHook(fn func(rt Runtime, vs []string)) {
	versHookMu.Lock()
	versHook = fn
	versHookMu.Unlock()
}

func emitVersionsUpdate(rt Runtime, vs []string) {
	if len(vs) == 0 {
		return
	}
	versHookMu.RLock()
	fn := versHook
	versHookMu.RUnlock()
	if fn != nil {
		fn(rt, append([]string(nil), vs...))
	}
}

func versCacheKey(rt Runtime, sourceID, custom string) string {
	return string(rt) + "|" + sourceID + "|" + custom
}

// versCacheFilePath 缓存落盘位置。抽成变量是为了测试能指向临时目录，
// 生产代码恒为 <数据目录>/env/verscache.json（与 states.json 同目录）。
var versCacheFilePath = func() string {
	return filepath.Join(platform.DefaultDataDir(), versCacheFileRel)
} // ---- 磁盘读写 ----

type versCacheDisk struct {
	Entries map[string]versCacheDiskEntry `json:"entries"`
}

type versCacheDiskEntry struct {
	Versions  []string `json:"versions"`
	FetchedAt int64    `json:"fetchedAt"` // unix 毫秒
	OK        bool     `json:"ok"`        // 是否来自上游（false=兜底清单）
}

// versCacheEnsureLoaded 首次访问时把磁盘缓存读进内存。
// 文件不存在或损坏都不算错误：等价于「还没有缓存」，下次拉取会重建。
func versCacheEnsureLoaded() {
	versCacheMu.Lock()
	if versCacheRead {
		versCacheMu.Unlock()
		return
	}
	versCacheRead = true
	versCacheMu.Unlock()

	b, err := os.ReadFile(versCacheFilePath())
	if err != nil {
		return
	}
	var disk versCacheDisk
	if json.Unmarshal(b, &disk) != nil || len(disk.Entries) == 0 {
		return
	}
	versCacheMu.Lock()
	for k, e := range disk.Entries {
		if len(e.Versions) == 0 {
			continue
		}
		at := time.UnixMilli(e.FetchedAt)
		ttl := versTTL
		if !e.OK {
			ttl = versFailTTL
		}
		versCache[k] = versCacheEntry{vs: e.Versions, fetchedAt: at, expired: at.Add(ttl), ok: e.OK}
	}
	versCacheMu.Unlock()
	logger.I("[env][vers] 已载入版本列表缓存 %d 项", len(disk.Entries))
}

// versCachePersist 原子落盘（临时文件 + rename）。失败只记日志：
// 缓存写不进去只是下次多一次网络请求，不影响功能。
func versCachePersist() {
	versPersistMu.Lock()
	defer versPersistMu.Unlock()

	versCacheMu.RLock()
	disk := versCacheDisk{Entries: make(map[string]versCacheDiskEntry, len(versCache))}
	for k, e := range versCache {
		disk.Entries[k] = versCacheDiskEntry{Versions: e.vs, FetchedAt: e.fetchedAt.UnixMilli(), OK: e.ok}
	}
	versCacheMu.RUnlock()

	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return
	}
	path := versCacheFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.W("[env][vers] 创建缓存目录失败: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logger.W("[env][vers] 写入版本缓存失败: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.W("[env][vers] 替换版本缓存失败: %v", err)
		_ = os.Remove(tmp)
	}
}

// ---- 缓存访问 ----

// versCacheGet 取一项缓存（返回副本，调用方改不到内部切片）。
func versCacheGet(key string) (versCacheEntry, bool) {
	versCacheEnsureLoaded()
	versCacheMu.RLock()
	e, ok := versCache[key]
	versCacheMu.RUnlock()
	if !ok || len(e.vs) == 0 {
		return versCacheEntry{}, false
	}
	e.vs = append([]string(nil), e.vs...)
	return e, true
}

// versCachePut 写入一项缓存（内存 + 磁盘），成功拉取时顺带推事件。
func versCachePut(key string, rt Runtime, vs []string, upstream bool, now time.Time) {
	ttl := versTTL
	if !upstream {
		ttl = versFailTTL
	}
	versCacheEnsureLoaded()
	versCacheMu.Lock()
	versCache[key] = versCacheEntry{
		vs:        append([]string(nil), vs...),
		fetchedAt: now,
		expired:   now.Add(ttl),
		ok:        upstream,
	}
	versCacheMu.Unlock()
	versCachePersist()
	if upstream {
		emitVersionsUpdate(rt, vs)
	}
}

// ---- 后台刷新 ----

// refreshVersions 立即重新拉取某运行时的版本列表并写缓存。
// 同一 key 已有拉取在飞时直接返回，避免后台巡检与页面查询重复打上游。
func refreshVersions(rt Runtime, sourceID, custom string) {
	def := registry[rt]
	if def.versURL == "" || def.versParse == nil {
		return
	}
	key := versCacheKey(rt, sourceID, custom)
	versCacheEnsureLoaded()
	versCacheMu.Lock()
	if versInflight[key] {
		versCacheMu.Unlock()
		return
	}
	versInflight[key] = true
	versCacheMu.Unlock()
	defer func() {
		versCacheMu.Lock()
		delete(versInflight, key)
		versCacheMu.Unlock()
	}()

	vs, upstream := fetchUpstreamVersions(rt)
	if len(vs) == 0 {
		return
	}
	versCachePut(key, rt, vs, upstream, time.Now())
}

// StartVersionRefresher 启动后台版本列表刷新：启动后先补一遍，之后每
// versRefreshInterval 巡检一次，只刷新已过期的运行时。
// ctx 取消即退出（应用关闭时与看门狗一起收拾）。
func StartVersionRefresher(ctx context.Context) {
	go func() {
		defer logger.RecoverToLog("env:vers-refresh")
		// 稍等再开跑：让主窗口先画出来，别和应用启动时的 PATH 扫描抢网络与磁盘。
		select {
		case <-ctx.Done():
			return
		case <-time.After(4 * time.Second):
		}
		for {
			refreshExpiredVersions(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(versRefreshInterval):
			}
		}
	}()
}

// refreshExpiredVersions 把「没缓存或已过期」的运行时逐个补拉一次。
func refreshExpiredVersions(ctx context.Context) {
	now := time.Now()
	var targets []Runtime
	for _, rt := range runtimeOrder {
		def := registry[rt]
		if def.versURL == "" || def.versParse == nil {
			continue
		}
		if e, ok := versCacheGet(versCacheKey(rt, "", "")); ok && now.Before(e.expired) {
			continue
		}
		targets = append(targets, rt)
	}
	if len(targets) == 0 {
		return
	}
	logger.I("[env][vers] 后台刷新 %d 个运行时的版本列表", len(targets))

	sem := make(chan struct{}, versRefreshWorkers)
	var wg sync.WaitGroup
	for _, rt := range targets {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(rt Runtime) {
			defer wg.Done()
			defer func() { <-sem }()
			refreshVersions(rt, "", "")
		}(rt)
	}
	wg.Wait()
}
