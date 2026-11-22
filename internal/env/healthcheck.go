package env

import (
	"strconv"
	"sync"
	"time"
)

// 服务健康探活（E-4）：给状态表加一个轻量诊断，区分「进程在但服务没真正起来」。
//
// 与「进程是否存在 / 端口是否冲突」是两回事：nginx 进程在但配置写错不会监听，
// MySQL 刚启动时进程已在、但还没接受连接。这里只用 TCP 拨端口做判定（不依赖
// 各运行时特有协议），保持对所有服务一致、低成本。
//
// 状态表每 3s 轮询，不能每次都拨号：结论按 (runtime/version/port) 缓存 2s 复用，
// 与 aria2 的健康缓存同一口径。探活本身 800ms 超时（复用 isPortOpen），
// 缓存未过期时零开销。

const serviceHealthTTL = 2 * time.Second

type serviceHealthKey struct {
	runtime string
	version string
	port    int // 首个侦听端口；0 表示无端口配置
}

type serviceHealthCached struct {
	at     time.Time
	health string
	detail string
}

var (
	serviceHealthMu    sync.Mutex
	serviceHealthCache = map[serviceHealthKey]serviceHealthCached{}
)

// probeServiceHealth 返回 (health, detail)：
//
//	""      = 非服务 / 无端口配置，不做判定（避免对 git / 无端口运行时误报）
//	"ok"    = 至少一个侦听端口可连，服务正常提供服务
//	"pending" = 进程在但所有端口都连不上，通常是启动中或配置有误
//
// detail 是一句中文诊断，前端放进状态徽标 title。调用方负责只在 Running=true
// 时验证结果；本函数只负责探活与缓存，不改任何既有 running 语义。
func probeServiceHealth(runtime, version string, running bool, ports []int) (string, string) {
	if !running || len(ports) == 0 {
		return "", ""
	}
	// 首个有效端口作为缓存键与拨号对象：多端口运行时（caddy admin + 站点）
	// 任一端口可连即视为健康时会漏缓存，这里固定探首端口，语义简单可预期。
	port := 0
	for _, p := range ports {
		if p > 0 {
			port = p
			break
		}
	}
	if port == 0 {
		return "", ""
	}

	key := serviceHealthKey{runtime: runtime, version: version, port: port}
	serviceHealthMu.Lock()
	if hit, ok := serviceHealthCache[key]; ok && time.Since(hit.at) < serviceHealthTTL {
		serviceHealthMu.Unlock()
		return hit.health, hit.detail
	}
	serviceHealthMu.Unlock()

	// 探活放在锁外：避免 800ms 拨号把状态接口的其他查询串行堵住。
	health, detail := "pending", "端口 "+strconv.Itoa(port)+" 未监听，服务可能仍在启动或配置有误"
	if isPortOpen(port) {
		health, detail = "ok", ""
	}

	serviceHealthMu.Lock()
	serviceHealthCache[key] = serviceHealthCached{at: time.Now(), health: health, detail: detail}
	// 顺手清掉过期项，防止长期运行下缓存无界增长（运行时/版本组合有限，但换版本会累积）。
	for k, v := range serviceHealthCache {
		if time.Since(v.at) > serviceHealthTTL*4 {
			delete(serviceHealthCache, k)
		}
	}
	serviceHealthMu.Unlock()
	return health, detail
}
