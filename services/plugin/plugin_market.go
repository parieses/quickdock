package plugin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	dlutil "quickdock/internal/dl"
	"quickdock/internal/env"
	"quickdock/internal/logger"
	pluginmgr "quickdock/internal/plugin"
	"quickdock/services"
	updatesvc "quickdock/services/update"
)

// 默认插件市场索引地址（QuickDock 插件中心 GitHub Pages）。
// 由 plugins/external 仓库的 gen_site.py 生成 site/index.json 后经 CI 部署。
// 现在只是「默认源」：用户可在市场页增删/启停更多源，见 loadMarketSources。
const defaultPluginMarketURL = "https://parieses.github.io/quickdock-plugins/index.json"

// 市场配置与缓存都落在 app_state 表（项目已有的 GetValue/SetValue 机制）。
// key 必须以 qd_ 开头才能通过 db.validateAppStateKey 的白名单——前端因而可以直接
// 用现有绑定读写源配置，无需新增任何 Go service 方法、也无需重新生成前端 bindings。
const (
	marketSourcesKey = "qd_plugin_market_sources"
	marketCacheKey   = "qd_plugin_market_cache"
)

// 下载/索引的流量上限：索引 5MB、zip 包 100MB（与 InstallFromZip 解压上限对齐）
const (
	maxMarketIndexSize = 5 << 20
	maxPluginZipSize   = 100 << 20
	// 单个源的索引请求：直连两次 + 系统代理兜底一次，各 4s，总预算 12s。
	// 多个源共享这 12s 总预算，避免用户加了几个源后市场页长时间卡在「加载中」；
	// 预算耗尽的源会快速失败，由「至少一个源成功」的规则决定是否走缓存。
	marketPerCallTimeout = 4 * time.Second
	marketTotalTimeout   = 12 * time.Second
)

// marketSource 一个市场索引源。Builtin 的官方源不可删除（只能停用），
// 保证用户把自定义源全删光后仍能恢复出至少一个可用源。
type marketSource struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	Builtin bool   `json:"builtin,omitempty"`
}

// marketCache 最后一次成功拉取的合并索引。整体缓存而不是按源分别缓存：
// 多源任一失败时用户看到的是一份完整、自洽的列表，而不是半张拼图。
type marketCache struct {
	FetchedAt int64       `json:"fetchedAt"`
	Index     marketIndex `json:"index"`
}

// marketIndex 机器可读的插件市场索引（对应 gen_site.py 输出的 index.json）
type marketIndex struct {
	Name    string         `json:"name"`
	Updated string         `json:"updated"`
	Plugins []marketPlugin `json:"plugins"`
	// 以下三项只在「网络失败、用最后一次成功缓存兜底」时由后端填充。
	// 前端据此明确标注「离线缓存」并给出缓存时间，绝不把旧数据静默冒充成最新。
	Offline     bool  `json:"offline,omitempty"`
	CachedAt    int64 `json:"cachedAt,omitempty"`
	SourceCount int   `json:"sourceCount,omitempty"`
}

// marketPlugin 市场中单个插件的描述
type marketPlugin struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	NameI18n map[string]string `json:"name_i18n,omitempty"`
	Version  string            `json:"version"`
	// 展示信息
	Description     string            `json:"description"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"`
	// 更新日志（纯文本/Markdown），详情页「更新日志」区块展示；
	// changelog_i18n 提供多语言版本，前端按当前 locale 优先取。
	Changelog     string            `json:"changelog,omitempty"`
	ChangelogI18n map[string]string `json:"changelog_i18n,omitempty"`
	Author        string            `json:"author"`
	Category      string            `json:"category"`
	Icon          string            `json:"icon"`
	// 约束信息（与 plugin.json 对齐）
	Platforms    []string               `json:"platforms"`
	Permissions  map[string]interface{} `json:"permissions"`
	Capabilities []string               `json:"capabilities"`
	Downloads    map[string]string      `json:"downloads"`
	// 以下由后端 GetPluginMarket 填充，前端据此显示"已安装/有新版/不支持"
	Installed        bool   `json:"installed,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	HasUpdate        bool   `json:"has_update,omitempty"`
	Supported        bool   `json:"supported,omitempty"`
}

// marketDirectHTTP 显式 Proxy:nil —— 自定义 Transport 的 Proxy 零值就是不走代理，
// 不能复用 http.DefaultTransport（它会读 HTTP_PROXY 环境变量）。
var marketDirectHTTP = &http.Client{
	Timeout: marketPerCallTimeout,
	Transport: &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: marketPerCallTimeout,
	},
}

// marketProxyHTTP 走环境变量 / WinINET 系统代理，只在直连彻底不通时兜底。
var marketProxyHTTP = &http.Client{Timeout: marketPerCallTimeout, Transport: env.ProxyTransport()}

// fetchMarketIndex 直连优先、重试一次、系统代理兜底。
// 为什么不放前端：WebView2 跟随 WinINET 系统代理，用户机器上常见
// 「ProxyEnable=1 但本地代理端口没起」，前端 fetch 会全部 ERR_TIMED_OUT。
// Go 侧显式直连，直连彻底不通才付 env.ProxyTransport() 那一跳。
func fetchMarketIndex(ctx context.Context, rawURL, sourceID string) (marketIndex, error) {
	var lastErr error
	for _, c := range []*http.Client{marketDirectHTTP, marketDirectHTTP, marketProxyHTTP} {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return marketIndex{}, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "QuickDock/market")

		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMarketIndexSize))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		var idx marketIndex
		if err := json.Unmarshal(data, &idx); err != nil {
			lastErr = fmt.Errorf("市场索引不是有效 JSON: %w", err)
			continue
		}
		return idx, nil
	}
	return marketIndex{}, fmt.Errorf("市场源 %s 不可达（直连重试与系统代理均失败）: %v", sourceID, lastErr)
}

// defaultMarketSources 首次运行 / 配置损坏时的兜底：只有官方源。
func defaultMarketSources() []marketSource {
	return []marketSource{{
		ID: "official", Name: "QuickDock 官方市场",
		URL: defaultPluginMarketURL, Enabled: true, Builtin: true,
	}}
}

// loadMarketSources 从 app_state 读用户配置的源列表。
// 解析失败 / 空配置 / 全是非法 URL 时退回默认源，不让一份写坏的配置
// 把市场彻底打不开。只放行 HTTPS：与 InstallPluginFromURL 的供应链防护一致。
func (p *PluginService) loadMarketSources() []marketSource {
	if p.App == nil || p.App.DB == nil {
		return defaultMarketSources()
	}
	raw, err := p.App.DB.GetSetting(marketSourcesKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return defaultMarketSources()
	}
	var list []marketSource
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) == 0 {
		return defaultMarketSources()
	}
	out := make([]marketSource, 0, len(list))
	for _, s := range list {
		u := strings.TrimSpace(s.URL)
		// 内置官方源在前端是不可编辑的占位（url 为空、name 由 i18n 给），
		// 保存配置时会原样写回来。这里补回真实地址，否则用户加了一个自定义源
		// 就会把官方源连带丢掉。
		if u == "" && s.Builtin && s.ID == "official" {
			u = defaultPluginMarketURL
		}
		if u == "" || !strings.HasPrefix(strings.ToLower(u), "https://") {
			continue
		}
		s.URL = u
		out = append(out, s)
	}
	if len(out) == 0 {
		return defaultMarketSources()
	}
	return out
}

func (p *PluginService) saveMarketCache(idx marketIndex) {
	if p.App == nil || p.App.DB == nil {
		return
	}
	b, err := json.Marshal(marketCache{FetchedAt: time.Now().Unix(), Index: idx})
	if err != nil {
		return
	}
	// 缓存写失败不能把一次成功的拉取变成失败：只记日志，下次网络故障时
	// 顶多没有缓存可用、明确报错，也不静默用旧数据冒充最新。
	if err := p.App.DB.SetSetting(marketCacheKey, string(b)); err != nil {
		logger.W("[plugin-market] 写入离线缓存失败: %v", err)
	}
}

func (p *PluginService) loadMarketCache() (marketCache, bool) {
	var zero marketCache
	if p.App == nil || p.App.DB == nil {
		return zero, false
	}
	raw, err := p.App.DB.GetSetting(marketCacheKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return zero, false
	}
	var c marketCache
	if err := json.Unmarshal([]byte(raw), &c); err != nil || c.FetchedAt <= 0 || len(c.Index.Plugins) == 0 {
		return zero, false
	}
	return c, true
}

// mergeMarketIndexes 按插件 id 去重，配置顺序靠前的源优先。
// 同名插件不互相覆盖，避免后加的源悄悄把官方版本换成自己的。
func mergeMarketIndexes(parts []marketIndex) marketIndex {
	merged := marketIndex{Name: "QuickDock Plugin Market", Plugins: make([]marketPlugin, 0)}
	seen := make(map[string]bool)
	for _, part := range parts {
		if part.Updated > merged.Updated {
			merged.Updated = part.Updated
		}
		for _, p := range part.Plugins {
			if p.ID == "" || seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			merged.Plugins = append(merged.Plugins, p)
		}
	}
	return merged
}

// GetPluginMarket 拉取所有启用的市场源、合并索引，并标注每个插件的本地安装状态。
// 全部源失败时用最后一次成功缓存兜底，并在返回结构里明确标记 offline/cachedAt；
// 连缓存都没有就报错，绝不静默用旧数据冒充最新。
func (p *PluginService) GetPluginMarket() *services.ApiResult {
	sources := p.loadMarketSources()
	enabled := make([]marketSource, 0, len(sources))
	for _, s := range sources {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return services.FailMsg("没有启用的插件市场源，请在「市场源」中启用至少一个")
	}

	ctx, cancel := context.WithTimeout(context.Background(), marketTotalTimeout)
	defer cancel()

	parts := make([]marketIndex, 0, len(enabled))
	for _, s := range enabled {
		idx, err := fetchMarketIndex(ctx, s.URL, s.ID)
		if err != nil {
			logger.W("[plugin-market] 源 %s (%s) 拉取失败: %v", s.ID, s.URL, err)
			continue
		}
		parts = append(parts, idx)
	}

	var idx marketIndex
	if len(parts) > 0 {
		idx = mergeMarketIndexes(parts)
		idx.SourceCount = len(parts)
		// 只缓存「至少一个源成功」的结果：全失败时保留上一次完整快照，
		// 不让一次事故把缓存覆盖成半张表。
		p.saveMarketCache(idx)
	} else {
		cache, ok := p.loadMarketCache()
		if !ok {
			return services.FailMsg(fmt.Sprintf("插件市场拉取失败（%d 个源均不可达），且无可用离线缓存", len(enabled)))
		}
		idx = cache.Index
		idx.Offline = true
		idx.CachedAt = cache.FetchedAt
		idx.SourceCount = 0
	}

	// 本地安装状态以实时 DB 为准，与本地插件列表（ListPlugins）保持同一判定源。
	// 不能用磁盘目录存在性判定：pluginsDir 下可能残留「有文件但未注册」的目录
	//（如从 plugins/external 同步的开发产物），只看磁盘会把它们全部标成「已安装」。
	// 离线缓存分支尤其依赖这一步：缓存里的 installed/has_update 是拉取那一刻的，
	// 用户之后装过/卸过插件，照搬就会标错。
	installed := map[string]string{}
	if p.App != nil && p.App.DB != nil {
		vs, err := p.App.DB.ListPluginVersions()
		if err != nil {
			return services.Fail(fmt.Errorf("读取插件安装记录失败: %w", err))
		}
		installed = vs
	}
	applyInstalledStatus(idx.Plugins, installed)

	if idx.Offline {
		return services.OkMsg(idx, fmt.Sprintf("插件市场不可达，展示 %s 的离线缓存",
			time.Unix(idx.CachedAt, 0).Format("2006-01-02 15:04")))
	}
	return services.Ok(idx)
}

// applyInstalledStatus 用本地已安装版本表（id → version）标注市场索引中每个插件的
// 平台支持 / 已安装 / 可更新状态。抽为纯函数以便单测（GetPluginMarket 依赖网络与 DB）。
func applyInstalledStatus(plugins []marketPlugin, installed map[string]string) {
	for i := range plugins {
		p := &plugins[i]
		// 先清零：离线缓存恢复时旧快照带着「已安装/有新版」，必须按实时 DB 重算，
		// 否则卸载过的插件在市场里会一直显示「已安装」。
		p.Installed = false
		p.InstalledVersion = ""
		p.HasUpdate = false
		p.Supported = isPlatformSupported(p.Platforms)
		if v, ok := installed[p.ID]; ok {
			p.Installed = true
			p.InstalledVersion = v
			// 仅当远程版本【高于】本地版本时才提示更新：!= 会把「本地比远程新」
			//（如本地 0.1.7 / 远程索引尚未推送 0.1.6）误判成可升级
			p.HasUpdate = compareVersions(p.Version, v) > 0
		}
	}
}

// InstallPluginFromURL 从 HTTPS URL 下载插件 zip 并安装。
// 下载完成后复用 InstallPlugin（InstallFromZip + manifest 读取 + DB 记录），
// 即与"从文件安装"走完全相同的校验与加载链路。
//
// GitHub Releases 直连在国内常超时（与设置页更新遇到的问题一致），故直连失败时
// 自动改用 ghfast.top 等镜像前缀重试（复用 updateMirrorDefaults，与更新链路同一套）。
// 进度事件统一用原始 url 标识，镜像切换不影响前端按 url 匹配卡片。
func (p *PluginService) InstallPluginFromURL(url string) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	// 强制 HTTPS：防止下载链路被中间人篡改（供应链攻击防护）
	if !strings.HasPrefix(url, "https://") {
		return services.FailMsg("仅支持 HTTPS 下载链接")
	}

	tmpDir := filepath.Join(os.TempDir(), "quickdock-market-install")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return services.Fail(fmt.Errorf("创建临时目录失败: %w", err))
	}

	// 文件名取 URL 末段；缺失/异常则 fallback
	fname := filepath.Base(url)
	if fname == "" || fname == "." || fname == string(filepath.Separator) {
		fname = "market.zip"
	}
	if !strings.HasSuffix(strings.ToLower(fname), ".zip") {
		fname += ".zip"
	}
	tmpPath := filepath.Join(tmpDir, fname)
	defer os.Remove(tmpPath)

	// 下载源列表：直连优先；仅 GitHub 资源（github.com / githubusercontent.com）
	// 追加镜像前缀（ghfast.top 等反代 GitHub 资源，与设置页更新同一套镜像）。
	// 非 GitHub 源保持直连，不加前缀。
	client := &http.Client{Timeout: 5 * time.Minute}
	var lastErr error
	for _, u := range pluginDownloadCandidates(url) {
		if err := p.tryDownloadPluginZip(client, u, url, tmpPath); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return services.Fail(lastErr)
	}

	// 下载完成 → 进入解压安装阶段（无细粒度进度，通知前端切「安装中」态）
	if p.App.App() != nil {
		if f, err := os.Stat(tmpPath); err == nil {
			p.App.App().Event.Emit("plugin:download-progress", map[string]any{
				"url": url, "downloaded": f.Size(), "total": f.Size(),
				"percent": 100, "stage": "installing",
			})
		}
	}

	// 版本检查：只允许安装"新的"或"更新的"，拒绝同版本重装与降级
	if err := p.checkPluginVersion(tmpPath); err != nil {
		return services.Fail(err)
	}

	// 复用标准安装链路：InstallFromZip(zip slip/bomb 防护 + 回滚) + manifest + DB
	return p.InstallPlugin(tmpPath)
}

// pluginDownloadCandidates 返回插件 zip 下载尝试顺序：直连优先，
// GitHub 资源（github.com / githubusercontent.com）追加镜像前缀
// （复用 updateMirrorDefaults，与设置页更新同一套镜像）；其他源只直连。
func pluginDownloadCandidates(url string) []string {
	urls := []string{url}
	if strings.Contains(url, "github.com/") || strings.Contains(url, "githubusercontent.com/") {
		for _, m := range updatesvc.UpdateMirrorDefaults {
			urls = append(urls, m+url)
		}
	}
	return urls
}

// tryDownloadPluginZip 尝试从单个 URL 下载插件 zip 到 tmpPath（成功时 tmpPath 为完整文件，
// 失败时清理临时文件并返回错误）。进度事件统一用 eventURL 标识——镜像重试时保持
// 前端按 url 匹配卡片不变。支持 Range 且 >2MB 时 4 连接分块并行下载，否则单流。
func (p *PluginService) tryDownloadPluginZip(client *http.Client, u, eventURL, tmpPath string) error {
	// 限制下载体积 100MB（与 InstallFromZip 解压上限一致，防恶意大包）
	// 连接级失败（如国内直连 GitHub 的 connectex/wsarecv 超时）多为瞬时抖动，立即重试一次
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(800 * time.Millisecond)
		}
		resp, err = client.Get(u)
		if err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("下载插件包失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}

	total := resp.ContentLength
	tracker := dlutil.NewProgressTracker(total, func(downloaded, total int64, percent int) {
		if p.App.App() == nil {
			return
		}
		p.App.App().Event.Emit("plugin:download-progress", map[string]any{
			"url": eventURL, "downloaded": downloaded, "total": total, "percent": percent,
		})
	})
	// 探测成功、开始下载前立即发 0% 事件，让前端进度条/百分比立刻就位——
	// 否则下载很快时（镜像/CDN）用户只看到「安装中」而看不到下载进度。
	if p.App.App() != nil {
		p.App.App().Event.Emit("plugin:download-progress", map[string]any{
			"url": eventURL, "downloaded": 0, "total": total, "percent": 0,
		})
	}

	var derr error
	if strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes") && total > dlutil.ParallelMinSize && total <= maxPluginZipSize {
		resp.Body.Close() // 关闭探测响应，改用带 Range 的分段请求
		if err := f.Truncate(total); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("预分配临时文件失败: %w", err)
		}
		derr = dlutil.DownloadParallelTo(client, u, f, total, tracker.Add)
	} else {
		_, derr = io.Copy(dlutil.NewProgressWriter(f, tracker), io.LimitReader(resp.Body, maxPluginZipSize))
		resp.Body.Close()
	}
	if derr != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("写入临时文件失败: %w", derr)
	}
	f.Close()
	return nil
}

// isPlatformSupported 判断当前系统是否支持该插件的平台声明。
// 与 internal/plugin.IsPlatformSupported 行为一致，但接受 []string 输入
// （市场索引里的 platforms 不是完整 manifest，无法直接复用）。
func isPlatformSupported(platforms []string) bool {
	if len(platforms) == 0 {
		return true // 未声明 = 全平台
	}
	for _, p := range platforms {
		if strings.ToLower(p) == runtime.GOOS {
			return true
		}
	}
	return false
}

// checkPluginVersion 从 zip 读取 plugin.json 的 id/version，与本地已装版本比较：
// 未安装/同版本/更新 → 放行；仅降级 → 拒绝。
// 同版本放行是为了允许覆盖重装/强制刷新（用户主动点升级即为同意）。
// 在 InstallPluginFromURL 下载完成后、InstallPlugin 解压前调用。
func (p *PluginService) checkPluginVersion(zipPath string) error {
	dlID, dlVer, err := readZipManifestIDVersion(zipPath)
	if err != nil {
		return fmt.Errorf("读取插件包 manifest 失败: %w", err)
	}
	if p.App.PluginMgr == nil {
		return nil // 无法比对，放行（兜底）
	}
	localMf, err := pluginmgr.LoadManifest(filepath.Join(p.App.PluginMgr.PluginsDir(), dlID, "plugin.json"))
	if err != nil {
		return nil // 本地未安装，放行（新安装）
	}
	if compareVersions(dlVer, localMf.Version) < 0 {
		return fmt.Errorf("本地版本 v%s 更新，拒绝降级到 v%s", localMf.Version, dlVer)
	}
	return nil // 同版本放行（覆盖重装）/ 更新放行；仅拒绝降级
}

// readZipManifestIDVersion 从 zip 包读取 plugin.json 的 id 和 version（不解压整个包）。
// 与 internal/plugin/installer.go 的 manifest 解析逻辑一致，但只取 id/version 供版本检查。
func readZipManifestIDVersion(zipPath string) (id, version string, err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", "", fmt.Errorf("打开 zip 包失败: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name == "plugin.json" || f.Name == "./plugin.json" {
			rc, e := f.Open()
			if e != nil {
				return "", "", fmt.Errorf("读取 plugin.json 失败: %w", e)
			}
			defer rc.Close()
			// plugin.json 上限 1MB（与 installer.go 的 maxPluginJSONSize 对齐）
			data, e := io.ReadAll(io.LimitReader(rc, 1<<20))
			if e != nil {
				return "", "", fmt.Errorf("读取 plugin.json 失败: %w", e)
			}
			var mf struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			}
			if e := json.Unmarshal(data, &mf); e != nil {
				return "", "", fmt.Errorf("解析 plugin.json 失败: %w", e)
			}
			return mf.ID, mf.Version, nil
		}
	}
	return "", "", fmt.Errorf("zip 包中未找到 plugin.json")
}

// compareVersions 比较 semver 风格版本字符串 a 与 b：
// 返回 -1 (a<b) / 0 (a==b) / 1 (a>b)。
// 按 . 分段；数字段比数值（保证 0.10.0 > 0.9.0），非数字段按字符串比较。
// semver 规范：prerelease 低于同版本正式版，如 0.1.0-beta < 0.1.0。
// 例：0.1.0-beta < 0.1.0 < 0.2.0 < 0.10.0 < 1.0.0。
func compareVersions(a, b string) int {
	aMain, aPre := splitVersionPrerelease(a)
	bMain, bPre := splitVersionPrerelease(b)

	// 主版本部分比较
	if c := compareVersionSegments(aMain, bMain); c != 0 {
		return c
	}
	// prerelease 部分：无 prerelease 者更新（semver 规范）
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	}
	return compareVersionSegments(aPre, bPre)
}

// splitVersionPrerelease 把 "0.1.0-beta.1" 拆成主版本 "0.1.0" 与 prerelease "beta.1"
// （以第一个 '-' 为界）
func splitVersionPrerelease(v string) (main, pre string) {
	if i := strings.Index(v, "-"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareVersionSegments 按 . 分段比较（数字段比数值，非数字段比字符串）
func compareVersionSegments(a, b string) int {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' })
	}
	sa, sb := split(a), split(b)
	for i := 0; i < len(sa) && i < len(sb); i++ {
		na, ea := strconv.Atoi(sa[i])
		nb, eb := strconv.Atoi(sb[i])
		if ea == nil && eb == nil {
			if na < nb {
				return -1
			}
			if na > nb {
				return 1
			}
		} else {
			if sa[i] < sb[i] {
				return -1
			}
			if sa[i] > sb[i] {
				return 1
			}
		}
	}
	switch {
	case len(sa) < len(sb):
		return -1
	case len(sa) > len(sb):
		return 1
	}
	return 0
}
