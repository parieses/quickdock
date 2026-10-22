package env

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"quickdock/internal/logger"
)

// 源体检（2026-10 引入）：把「下载链接是否可达」从人工排查变成一键可复用能力。
//
// 背景：环境管理里 28 个运行时、多则 4 个下载源，上游改版（gyan.dev 换路径、
// dl.min.io 全站 410、php.net 只保留 vs16 包……）不会通知任何人，用户只会看到
// 「下载失败」，且失败原因被重试与多源兜底掩盖。体检把每个源的真实状态码暴露出来。

const (
	// probeTimeout 单个地址的探测超时。取 12s：够慢网络握手，又不至于让一次
	// 全量体检（约 50 个地址 / 6 并发）拖到分钟级。
	probeTimeout = 12 * time.Second
	// headProbeTimeout HEAD 兜底探测的超时。HEAD 只是拿状态码，不需要等 TLS 之后的数据。
	headProbeTimeout = 6 * time.Second
	// sourceCheckWorkers 体检并发度（按运行时并行，单个运行时内部串行探测各源）。
	sourceCheckWorkers = 6
)

// SourceCheck 单个下载源的体检结果。
type SourceCheck struct {
	Runtime   string `json:"runtime"`
	Name      string `json:"name"`      // 运行时展示名
	SourceID  string `json:"sourceId"`  // 源 ID
	Source    string `json:"source"`    // 源展示名
	Version   string `json:"version"`   // 本次探测用的版本
	URL       string `json:"url"`       // 实际探测地址
	OK        bool   `json:"ok"`        // 2xx/3xx（重定向已跟随）
	Code      int    `json:"code"`      // HTTP 状态码，0=连接层失败
	Error     string `json:"error"`     // 失败原因
	LatencyMs int64  `json:"latencyMs"` // 耗时
	// MirrorOK 直连失败、但经 gh-proxy 镜像可达（仅 github.com 地址会置位）。
	// 这种情况不是「源坏了」，而是「需要开启镜像加速」——面板会区分提示。
	MirrorOK bool  `json:"mirrorOk"`
	MirrorMs int64 `json:"mirrorMs"`
}

// VersionSourceCheck 上游「版本列表」的体检结果与新鲜度。
type VersionSourceCheck struct {
	Runtime     string `json:"runtime"`
	Name        string `json:"name"`
	URL         string `json:"url"` // 空=该运行时没有上游列表，只有推荐清单
	OK          bool   `json:"ok"`
	Code        int    `json:"code"`
	Error       string `json:"error"`
	LatencyMs   int64  `json:"latencyMs"`
	Count       int    `json:"count"`       // 上游解析出的版本数
	Latest      string `json:"latest"`      // 上游最新版本
	Recommended string `json:"recommended"` // 本地推荐清单首项（兜底用）
	Stale       bool   `json:"stale"`       // 推荐清单首项落后于上游最新
}

// SourceCheckReport 一次完整体检的汇总。
type SourceCheckReport struct {
	DurationMs int64                `json:"durationMs"`
	Sources    []SourceCheck        `json:"sources"`
	Versions   []VersionSourceCheck `json:"versions"`
	OK         int                  `json:"ok"`         // 直连可达的下载源数
	MirrorOnly int                  `json:"mirrorOnly"` // 直连不通但镜像可达（建议开加速）
	Failed     int                  `json:"failed"`     // 直连与镜像都不可达
}

// maxProbeHops 手工跟随重定向的层数上限。
//
// 为什么不让 http.Client 自动跟：需要知道「最终连到哪个主机」。JDK 的下载源是
// 重定向 API（api.adoptium.net/v3/binary/latest/... → 307 → github.com Release 资产），
// 候选地址本身不含 github.com，镜像改写（ghProxyURL）无从下手，结果把
// 「国内直连 github 被阻断」误判成「源失效」。跟到最终地址才能正确复探镜像。
const maxProbeHops = 4

// noFollowRedirect 关闭自动重定向，改由 probeURLStatus 逐跳处理。
func noFollowRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// isRedirectStatus 301/302/303/307/308 视为需要继续跳转。
func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// resolveRedirect 把 Location 解析成绝对地址（支持相对跳转）。
func resolveRedirect(base, loc string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	l, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	u := b.ResolveReference(l)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("不支持的重定向协议: %s", u.Scheme)
	}
	return u.String(), nil
}

// probeURLStatus 探测地址可达性，返回「最终状态码」与「实际连到的地址」。
//
// 三个设计要点：
//  1. 用 1 字节 GET（Range: bytes=0-0）而不是 HEAD：HEAD 对部分 CDN/对象存储不可靠——
//     实测 github.com 的 Release 资产直连时 HEAD 与 GET 都会卡住；PHP 的 archives 地址
//     HEAD 要 12s 而 GET 0.9s 就回；个别 CDN 对 HEAD 直接 403/405。GET 才是「用户真能
//     下到」的那个动作，代价只有 1 字节。
//  2. 服务器明确不支持 Range（405/501）或连接失败时退化为 HEAD，只求一个状态码。
//  3. 重定向手工逐跳跟随，返回的第二个值是最末一跳地址——镜像复探要以它为准。
func probeURLStatus(rawURL string) (int, string, error) {
	cur := rawURL
	for hop := 0; hop <= maxProbeHops; hop++ {
		code, loc, err := probeOnce(cur, http.MethodGet, probeTimeout)
		if err != nil || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented {
			if c2, _, e2 := probeOnce(cur, http.MethodHead, headProbeTimeout); e2 == nil {
				return c2, cur, nil // HEAD 拿到了状态码，以它为准
			}
			if err != nil {
				return 0, cur, err
			}
		}
		if isRedirectStatus(code) && loc != "" {
			next, rerr := resolveRedirect(cur, loc)
			if rerr != nil {
				return code, cur, nil // 跳不动就按当前状态码收尾，避免误报
			}
			cur = next
			continue
		}
		return code, cur, nil
	}
	return 0, cur, fmt.Errorf("重定向层数超过 %d 层: %s", maxProbeHops, rawURL)
}

// probeOnce 发一次探测请求，返回状态码与重定向目标（不自动跟随）。
// 代理失败时退化直连，与 fetchURL 的容错口径一致。
func probeOnce(rawURL, method string, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	newReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "QuickDock/1.0")
		req.Header.Set("Range", "bytes=0-0")
		return req, nil
	}
	req, err := newReq()
	if err != nil {
		return 0, "", err
	}
	resp, err := (&http.Client{Transport: ProxyTransport(), CheckRedirect: noFollowRedirect}).Do(req)
	if err != nil {
		req2, _ := newReq()
		resp, err = (&http.Client{Transport: &http.Transport{}, CheckRedirect: noFollowRedirect}).Do(req2)
		if err != nil {
			return 0, "", err
		}
	}
	defer resp.Body.Close()
	loc := ""
	if isRedirectStatus(resp.StatusCode) {
		loc = resp.Header.Get("Location")
	}
	return resp.StatusCode, loc, nil
}

// checkableRuntimes 返回有下载源或版本列表的运行时（按固定展示顺序）。
// MCP / WebDAV 这类内置服务没有外部依赖，体检无意义，直接跳过。
func checkableRuntimes() []Runtime {
	out := make([]Runtime, 0, len(runtimeOrder))
	for _, rt := range runtimeOrder {
		def := registry[rt]
		if len(def.sources) == 0 && def.versURL == "" {
			continue
		}
		out = append(out, rt)
	}
	return out
}

// CheckAllSources 全量体检：逐个探测每个运行时的每个下载源，以及上游版本列表。
//
// 结果通过 onSource / onVersion 实时回调（可为 nil），前端据此边跑边渲染；
// 返回的汇总报告用于一次性渲染/统计。ctx 取消时尽快返回已完成的部分。
func CheckAllSources(ctx context.Context, onSource func(SourceCheck), onVersion func(VersionSourceCheck)) SourceCheckReport {
	start := time.Now()
	rts := checkableRuntimes()

	var (
		mu     sync.Mutex
		report = SourceCheckReport{Sources: []SourceCheck{}, Versions: []VersionSourceCheck{}}
		wg     sync.WaitGroup
		sem    = make(chan struct{}, sourceCheckWorkers)
	)

	for _, rt := range rts {
		rt := rt
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			// 1) 下载源逐个体检
			def := registry[rt]
			version := ""
			if len(def.versions) > 0 {
				version = def.versions[0]
			}
			for _, src := range ListSources(rt) {
				if ctx.Err() != nil {
					return
				}
				url := ""
				if version != "" {
					url = src.Build(version, runtime.GOOS, runtime.GOARCH)
				}
				item := SourceCheck{
					Runtime:  string(rt),
					Name:     def.display,
					SourceID: src.ID,
					Source:   src.Name,
					Version:  version,
					URL:      url,
				}
				if url == "" {
					item.Error = "该源不支持当前平台或无法构造地址"
				} else {
					t0 := time.Now()
					code, finalURL, err := probeURLStatus(url)
					item.Code = code
					item.LatencyMs = time.Since(t0).Milliseconds()
					item.OK = err == nil && code >= 200 && code < 400
					if err != nil {
						item.Error = err.Error()
					} else if !item.OK {
						item.Error = http.StatusText(code)
						if item.Error == "" {
							item.Error = "HTTP " + strconv.Itoa(code)
						}
					}
					// 直连失败时，对「最终地址」再探一次 gh-proxy：
					// 国内直连 github Release 资产常被阻断（连接层超时），而镜像可用——
					// 这跟「源本身坏了」是两回事，要在面板里分开表达。
					// 用 finalURL 而不是原始 url：JDK 这类重定向 API 原始地址里没有
					// github.com，只有跟到最后一跳才看得出需要加速。
					if !item.OK {
						if mirrorURL := ghProxyURL(finalURL); mirrorURL != "" {
							t1 := time.Now()
							if c2, _, e2 := probeURLStatus(mirrorURL); e2 == nil && c2 >= 200 && c2 < 400 {
								item.MirrorOK = true
								item.MirrorMs = time.Since(t1).Milliseconds()
							}
						}
					}
				}
				if onSource != nil {
					onSource(item)
				}
				mu.Lock()
				report.Sources = append(report.Sources, item)
				switch {
				case item.OK:
					report.OK++
				case item.MirrorOK:
					report.MirrorOnly++
				default:
					report.Failed++
				}
				mu.Unlock()
			}

			// 2) 上游版本列表体检 + 新鲜度
			vc := checkVersionSource(rt)
			if onVersion != nil {
				onVersion(vc)
			}
			mu.Lock()
			report.Versions = append(report.Versions, vc)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 固定排序，便于前端稳定渲染与 diff（并发 append 的顺序是不确定的）
	sort.Slice(report.Sources, func(i, j int) bool {
		if report.Sources[i].Runtime != report.Sources[j].Runtime {
			return report.Sources[i].Runtime < report.Sources[j].Runtime
		}
		return report.Sources[i].SourceID < report.Sources[j].SourceID
	})
	sort.Slice(report.Versions, func(i, j int) bool {
		return report.Versions[i].Runtime < report.Versions[j].Runtime
	})
	report.DurationMs = time.Since(start).Milliseconds()
	logger.I("[env][check] 源体检完成 sources=%d ok=%d mirrorOnly=%d failed=%d versions=%d 耗时=%dms",
		len(report.Sources), report.OK, report.MirrorOnly, report.Failed, len(report.Versions), report.DurationMs)
	// 失败明细单独打：汇总数字看不出「哪个源坏了」，排查时还得再点一遍体检。
	for _, it := range report.Sources {
		if it.OK || it.MirrorOK {
			continue
		}
		logger.W("[env][check] 不可达 %s/%s code=%d url=%s err=%s",
			it.Runtime, it.SourceID, it.Code, it.URL, it.Error)
	}
	return report
}

// checkVersionSource 拉取上游版本列表，判断可用性与「推荐清单是否落后」。
func checkVersionSource(rt Runtime) VersionSourceCheck {
	def := registry[rt]
	vc := VersionSourceCheck{Runtime: string(rt), Name: def.display, URL: def.versURL}
	if len(def.versions) > 0 {
		vc.Recommended = def.versions[0]
	}
	if def.versURL == "" || def.versParse == nil {
		// 没有上游列表：不算故障（MySQL/MariaDB 等本就只维护推荐清单）
		vc.OK = true
		return vc
	}
	t0 := time.Now()
	body, code, err := fetchURLCode(def.versURL)
	vc.Code = code
	vc.LatencyMs = time.Since(t0).Milliseconds()
	if err == nil {
		vs := sortVersionsDesc(def.versParse(body))
		vs = validateTopVersions(rt, vs)
		vc.Count = len(vs)
		if len(vs) == 0 {
			vc.Error = "上游列表解析为空"
			return vc
		}
		vc.OK = true
		vc.Latest = vs[0]
		vc.Stale = vc.Recommended != "" && vc.Latest != vc.Recommended
		return vc
	}

	// 失败：GitHub API 的 403 基本都是匿名限流（1 小时 60 次），而实际安装时
	// AvailableVersions 会自动退到 Releases 页面 HTML 解析。这里复现同一条兜底，
	// 并把「为什么 403」写清楚，避免面板把限流误报成「上游挂了」。
	if def.versHTMLFallbackParse != nil && def.fallbackHTMLURL != "" {
		if body2, _, err2 := fetchURLCode(def.fallbackHTMLURL); err2 == nil {
			if vs := sortVersionsDesc(def.versHTMLFallbackParse(body2)); len(vs) > 0 {
				vc.OK = true
				vc.Count = len(vs)
				vc.Latest = vs[0]
				vc.Stale = vc.Recommended != "" && vc.Latest != vc.Recommended
				if code == http.StatusForbidden {
					vc.Error = "API 匿名限流（每小时 60 次），已改用 Releases 页面解析"
				} else {
					vc.Error = "API 不可用，已改用 Releases 页面解析"
				}
				return vc
			}
		}
	}
	vc.Error = err.Error()
	return vc
}

// validateTopVersions 对上游版本列表头部做「真实可下载性」复核（见 runtimeDef.versValidate）。
// AvailableVersions 与源体检共用，保证两边对「最新版」的判断一致。
func validateTopVersions(rt Runtime, vs []string) []string {
	def := registry[rt]
	if def.versValidate == nil || def.versValidateTopN <= 0 || len(vs) == 0 {
		return vs
	}
	n := def.versValidateTopN
	if n > len(vs) {
		n = len(vs)
	}
	kept := make([]string, 0, len(vs))
	for i, v := range vs {
		if i < n && !def.versValidate(v) {
			logger.W("[env][source] %s 版本 %s 在上游实际不可下载，已从列表剔除", rt, v)
			continue
		}
		kept = append(kept, v)
	}
	if len(kept) == 0 {
		return vs
	}
	return kept
}
