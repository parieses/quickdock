package services

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
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"quickdock/internal/plugin"
)

// 默认插件市场索引地址（QuickDock 插件中心 GitHub Pages）。
// 由 plugins/external 仓库的 gen_site.py 生成 site/index.json 后经 CI 部署。
// MVP 阶段硬编码；后续若需多市场源可改为存 DB 配置。
const defaultPluginMarketURL = "https://parieses.github.io/quickdock-plugins/index.json"

// 下载/索引的流量上限：索引 5MB、zip 包 100MB（与 InstallFromZip 解压上限对齐）
const (
	maxMarketIndexSize = 5 << 20
	maxPluginZipSize   = 100 << 20
)

// marketIndex 机器可读的插件市场索引（对应 gen_site.py 输出的 index.json）
type marketIndex struct {
	Name    string         `json:"name"`
	Updated string         `json:"updated"`
	Plugins []marketPlugin `json:"plugins"`
}

// dlProgressWriter / progressTracker：下载进度节流上报（plugin:download-progress 事件）。
// 至少间隔 minInterval 且（total 已知时）百分比变化才触发，避免事件风暴。
// total<=0（chunked 传输无 Content-Length）时 percent 恒 0，前端回退显示已下载 MB。
type progressTracker struct {
	mu       sync.Mutex
	total    int64
	done     int64
	lastPct  int
	lastEmit time.Time
	emit     func(downloaded, total int64, percent int)
}

const progressMinInterval = 150 * time.Millisecond

// add 并发安全地累计 n 字节并按节流规则决定是否发事件
func (t *progressTracker) add(n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done += n
	pct := 0
	if t.total > 0 {
		pct = int(t.done * 100 / t.total)
		if pct > 100 {
			pct = 100
		}
	}
	if time.Since(t.lastEmit) < progressMinInterval {
		return
	}
	if t.total > 0 && pct == t.lastPct {
		return
	}
	t.lastPct = pct
	t.lastEmit = time.Now()
	if t.emit != nil {
		t.emit(t.done, t.total, pct)
	}
}

// dlProgressWriter 单流下载用：包装目标 writer 并把字节数计入 tracker
type dlProgressWriter struct {
	dst io.Writer
	tr  *progressTracker
}

func (w *dlProgressWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.tr.add(int64(n))
	}
	return n, err
}

// 并发分块下载参数：仅对支持 Accept-Ranges 且体积超阈值的包启用，
// 小包/不支持 Range 的源自动回退单流（避免小包并发握手开销与兼容性问题）
const (
	downloadConcurrency = 4
	parallelMinSize     = 2 << 20 // 2MB
)

// downloadParallelTo 把 url 按 Range 切 downloadConcurrency 段并行下载，
// 各段 WriteAt 写入已按 total 预分配的文件的对应偏移（不同偏移无写竞争）；
// 每段最多重试 3 次，任一段最终失败即整体失败并 cancel 其余段。
// progress 每收到一个数据块回调一次（n 为本次写入字节数），由调用方决定
// 是否节流与如何上报——插件市场走 progressTracker.add（节流 + 事件），
// 设置页"检查更新"走同样逻辑转发 Wails onProgress。共用同一实现避免两处分叉。
func downloadParallelTo(client *http.Client, url string, f *os.File, total int64, progress func(downloaded int64)) error {
	chunk := (total + downloadConcurrency - 1) / downloadConcurrency
	g, ctx := errgroup.WithContext(context.Background())
	for i := 0; i < downloadConcurrency; i++ {
		start := int64(i) * chunk
		end := start + chunk - 1
		if end >= total {
			end = total - 1
		}
		if start > end {
			continue // 尾部空段
		}
		g.Go(func() error {
			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(attempt) * 500 * time.Millisecond): // 退避 0/500ms/1s
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					return err
				}
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
				resp, err := client.Do(req)
				if err != nil {
					lastErr = err
					continue
				}
				if resp.StatusCode != http.StatusPartialContent {
					io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
					resp.Body.Close()
					lastErr = fmt.Errorf("Range 请求返回 HTTP %d", resp.StatusCode)
					continue
				}
				buf := make([]byte, 64*1024)
				offset := start
				var werr error
				for {
					n, rerr := resp.Body.Read(buf)
					if n > 0 {
						if _, e := f.WriteAt(buf[:n], offset); e != nil {
							werr = e
							break
						}
						offset += int64(n)
						if progress != nil {
							progress(int64(n))
						}
					}
					if rerr != nil {
						if rerr != io.EOF {
							werr = rerr
						}
						break
					}
				}
				resp.Body.Close()
				if werr == nil && offset == end+1 {
					return nil
				}
				if werr == nil {
					werr = fmt.Errorf("分段 %d-%d 数据不完整", start, end)
				}
				lastErr = werr
			}
			return lastErr
		})
	}
	return g.Wait()
}

// marketPlugin 市场中单个插件的描述
type marketPlugin struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	NameI18n map[string]string     `json:"name_i18n,omitempty"`
	Version string                 `json:"version"`
	// 展示信息
	Description    string            `json:"description"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"`
	Author         string            `json:"author"`
	Category       string            `json:"category"`
	Icon           string            `json:"icon"`
	// 约束信息（与 plugin.json 对齐）
	Platforms    []string               `json:"platforms"`
	Permissions  map[string]interface{} `json:"permissions"`
	Capabilities []string               `json:"capabilities"`
	Downloads    map[string]string      `json:"downloads"`
	// 以下由后端 GetPluginMarket 填充，前端据此显示"已安装/有新版/不支持"
	Installed       bool   `json:"installed,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	HasUpdate       bool   `json:"has_update,omitempty"`
	Supported       bool   `json:"supported,omitempty"`
}

// GetPluginMarket 拉取插件市场索引并标注每个插件的本地安装状态。
// 前端据此渲染市场列表与"安装/升级/已安装"按钮。
func (a *AppService) GetPluginMarket() *ApiResult {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(defaultPluginMarketURL)
	if err != nil {
		return Fail(fmt.Errorf("拉取插件市场失败: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return FailMsg(fmt.Sprintf("插件市场返回 HTTP %d", resp.StatusCode))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMarketIndexSize))
	if err != nil {
		return Fail(fmt.Errorf("读取市场索引失败: %w", err))
	}

	var idx marketIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return Fail(fmt.Errorf("解析市场索引失败: %w", err))
	}

	// 标注已安装/版本/平台支持
	pluginsDir := ""
	if a.PluginMgr != nil {
		pluginsDir = a.PluginMgr.PluginsDir()
	}
	for i := range idx.Plugins {
		p := &idx.Plugins[i]
		p.Supported = isPlatformSupported(p.Platforms)
		if pluginsDir != "" {
			if mf, err := plugin.LoadManifest(filepath.Join(pluginsDir, p.ID, "plugin.json")); err == nil {
				p.Installed = true
				p.InstalledVersion = mf.Version
				// 版本不一致即视为有更新（不做 semver 比较，简单可靠）
				p.HasUpdate = mf.Version != p.Version
			}
		}
	}

	return Ok(idx)
}

// InstallPluginFromURL 从 HTTPS URL 下载插件 zip 并安装。
// 下载完成后复用 InstallPlugin（InstallFromZip + manifest 读取 + DB 记录），
// 即与"从文件安装"走完全相同的校验与加载链路。
//
// GitHub Releases 直连在国内常超时（与设置页更新遇到的问题一致），故直连失败时
// 自动改用 ghfast.top 等镜像前缀重试（复用 updateMirrorDefaults，与更新链路同一套）。
// 进度事件统一用原始 url 标识，镜像切换不影响前端按 url 匹配卡片。
func (a *AppService) InstallPluginFromURL(url string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// 强制 HTTPS：防止下载链路被中间人篡改（供应链攻击防护）
	if !strings.HasPrefix(url, "https://") {
		return FailMsg("仅支持 HTTPS 下载链接")
	}

	tmpDir := filepath.Join(os.TempDir(), "quickdock-market-install")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return Fail(fmt.Errorf("创建临时目录失败: %w", err))
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
		if err := a.tryDownloadPluginZip(client, u, url, tmpPath); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return Fail(lastErr)
	}

	// 下载完成 → 进入解压安装阶段（无细粒度进度，通知前端切「安装中」态）
	if a.app != nil {
		if f, err := os.Stat(tmpPath); err == nil {
			a.app.Event.Emit("plugin:download-progress", map[string]any{
				"url": url, "downloaded": f.Size(), "total": f.Size(),
				"percent": 100, "stage": "installing",
			})
		}
	}

	// 版本检查：只允许安装"新的"或"更新的"，拒绝同版本重装与降级
	if err := a.checkPluginVersion(tmpPath); err != nil {
		return Fail(err)
	}

	// 复用标准安装链路：InstallFromZip(zip slip/bomb 防护 + 回滚) + manifest + DB
	return a.InstallPlugin(tmpPath)
}

// pluginDownloadCandidates 返回插件 zip 下载尝试顺序：直连优先，
// GitHub 资源（github.com / githubusercontent.com）追加镜像前缀
// （复用 updateMirrorDefaults，与设置页更新同一套镜像）；其他源只直连。
func pluginDownloadCandidates(url string) []string {
	urls := []string{url}
	if strings.Contains(url, "github.com/") || strings.Contains(url, "githubusercontent.com/") {
		for _, m := range updateMirrorDefaults {
			urls = append(urls, m+url)
		}
	}
	return urls
}

// tryDownloadPluginZip 尝试从单个 URL 下载插件 zip 到 tmpPath（成功时 tmpPath 为完整文件，
// 失败时清理临时文件并返回错误）。进度事件统一用 eventURL 标识——镜像重试时保持
// 前端按 url 匹配卡片不变。支持 Range 且 >2MB 时 4 连接分块并行下载，否则单流。
func (a *AppService) tryDownloadPluginZip(client *http.Client, u, eventURL, tmpPath string) error {
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
	tracker := &progressTracker{total: total, lastPct: -1, emit: func(downloaded, total int64, percent int) {
		if a.app == nil {
			return
		}
		a.app.Event.Emit("plugin:download-progress", map[string]any{
			"url": eventURL, "downloaded": downloaded, "total": total, "percent": percent,
		})
	}}
	// 探测成功、开始下载前立即发 0% 事件，让前端进度条/百分比立刻就位——
	// 否则下载很快时（镜像/CDN）用户只看到「安装中」而看不到下载进度。
	if a.app != nil {
		a.app.Event.Emit("plugin:download-progress", map[string]any{
			"url": eventURL, "downloaded": 0, "total": total, "percent": 0,
		})
	}

	var derr error
	if strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes") && total > parallelMinSize && total <= maxPluginZipSize {
		resp.Body.Close() // 关闭探测响应，改用带 Range 的分段请求
		if err := f.Truncate(total); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("预分配临时文件失败: %w", err)
		}
		derr = downloadParallelTo(client, u, f, total, tracker.add)
	} else {
		_, derr = io.Copy(&dlProgressWriter{dst: f, tr: tracker}, io.LimitReader(resp.Body, maxPluginZipSize))
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
func (a *AppService) checkPluginVersion(zipPath string) error {
	dlID, dlVer, err := readZipManifestIDVersion(zipPath)
	if err != nil {
		return fmt.Errorf("读取插件包 manifest 失败: %w", err)
	}
	if a.PluginMgr == nil {
		return nil // 无法比对，放行（兜底）
	}
	localMf, err := plugin.LoadManifest(filepath.Join(a.PluginMgr.PluginsDir(), dlID, "plugin.json"))
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
