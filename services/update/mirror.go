package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	dlutil "quickdock/internal/dl"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// 国内直连 GitHub Releases 常超时。mirrorProvider 包装 endpoint provider：
// Check（拉 manifest）与 Download（下载安装包）直连失败时都自动改用镜像前缀重试
// （ghfast.top 等），安装包带 Ed25519 签名验证，镜像只改传输 URL、无法篡改内容，
// 安全性不变。
//
// manifest 侧为何安全：CI 生成 manifest 时用 -url-prefix 写入【绝对】artifact URL，
// 镜像命中的 Release 里 endpoint.artifact.url 仍是 GitHub 原始地址，下载加速由
// Download 的镜像重试链负责，两层互不干扰。
// 已知取舍：镜像对 releases/latest 的重定向有几分钟缓存，极端情况下可能取到
// 旧 manifest 而被判"已是最新"——好过整体失败，且下次周期检查会自愈。
//
// 每次尝试限时 3 分钟：直连卡死时不会无限等待，镜像也失败则返回最后错误。
// 下载先落一份持久的缓存文件（<数据目录>/update/cache/pending.part），完整成功
// 后才一次性写入 dst —— wails 的 download() 用 io.MultiWriter(file, hasher) 做
// 流式哈希，半份数据进去就会污染摘要导致验签失败。缓存 + 落盘的续传状态让
// 「3 分钟超时」「换镜像重试」「重启应用后再点下载」都能从断点继续，而不是
// 每次从 0 重来（网络差时那样永远下不完）。

// UpdateMirrorDefaults 默认镜像前缀（保持更新，失效可换）。可经 SetUpdateMirrors 覆盖。
// 导出供 services/plugin 的市场下载镜像候选复用（与更新链路同一套镜像）。
var UpdateMirrorDefaults = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
}

const perURLTimeout = 3 * time.Minute

// ManifestURL 更新 manifest 的 GitHub 稳定地址。
// initUpdater 的 endpoint provider 与镜像 Check 重试共用同一常量，
// 避免两处字面量漂移。
const ManifestURL = "https://github.com/parieses/quickdock/releases/latest/download/manifest.json"

type mirrorProvider struct {
	inner       updater.Provider
	client      *http.Client
	mirrors     []string
	manifestURL string
	// cacheDir 断点续传缓存目录；空则用 <数据目录>/update/cache
	cacheDir string
}

// MirrorConfig 构造镜像 provider 的参数。
type MirrorConfig struct {
	Inner       updater.Provider
	Client      *http.Client
	ManifestURL string
	Mirrors     []string
	// CacheDir 断点续传缓存目录（留空 = <数据目录>/update/cache）。测试注入临时目录用。
	CacheDir string
}

// NewMirrorProvider 按配置构造镜像 provider。
func NewMirrorProvider(cfg MirrorConfig) updater.Provider {
	ms := cfg.Mirrors
	if len(ms) == 0 {
		ms = UpdateMirrorDefaults
	}
	return &mirrorProvider{
		inner:       cfg.Inner,
		client:      cfg.Client,
		mirrors:     ms,
		manifestURL: cfg.ManifestURL,
		cacheDir:    cfg.CacheDir,
	}
}

// NewMirrorUpdaterProvider 包装 endpoint provider，Check/Download 直连 GitHub
// 失败时自动镜像重试。manifestURL 为直连 manifest 地址（空则 Check 不做镜像
// 重试，仅保留 Download 行为）；mirrors 为空时使用内置默认镜像列表
// （UpdateMirrorDefaults）。
func NewMirrorUpdaterProvider(inner updater.Provider, client *http.Client, manifestURL string, mirrors ...string) updater.Provider {
	return NewMirrorProvider(MirrorConfig{
		Inner:       inner,
		Client:      client,
		ManifestURL: manifestURL,
		Mirrors:     mirrors,
	})
}

func (p *mirrorProvider) Name() string { return p.inner.Name() }

func (p *mirrorProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	rel, err := p.inner.Check(ctx, req)
	if err == nil || p.manifestURL == "" || len(p.mirrors) == 0 {
		return rel, err
	}
	// 直连拉 manifest 失败（国内对 github.com 常间歇性超时）→ 逐个镜像重试。
	// 镜像 URL 形如 https://ghfast.top/https://github.com/.../manifest.json。
	for _, m := range p.mirrors {
		ep, cerr := endpoint.New(endpoint.Config{URL: m + p.manifestURL, HTTPClient: p.client})
		if cerr != nil {
			continue
		}
		if mRel, mErr := ep.Check(ctx, req); mErr == nil {
			logger.I("[update] 直连拉取 manifest 失败，镜像 %s 命中", m)
			return mRel, nil
		}
	}
	return rel, err
}

func (p *mirrorProvider) Download(ctx context.Context, rel *updater.Release, dst io.Writer, onProgress func(written, total int64)) error {
	// endpoint provider 把下载地址放在 endpoint.artifact.url；github provider 用 github.asset.url。
	// 两边都要认，切换 provider 后镜像重试才不会失效。
	urlStr, _ := rel.Metadata["endpoint.artifact.url"].(string)
	if urlStr == "" {
		urlStr, _ = rel.Metadata["github.asset.url"].(string)
	}
	if urlStr == "" {
		return p.inner.Download(ctx, rel, dst, onProgress)
	}

	urls := []string{urlStr}
	for _, m := range p.mirrors {
		urls = append(urls, m+urlStr)
	}

	var lastErr error
	for _, u := range urls {
		// 每个 URL 独立子超时，避免直连长时间卡死拖慢整个下载流程
		uCtx, cancel := context.WithTimeout(ctx, perURLTimeout)
		err := p.downloadURL(uCtx, u, rel, dst, onProgress)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

// cachePaths 断点续传用的缓存文件与状态文件。固定一对文件名（不带版本号）：
// 同一时刻只会有一个待安装版本，按版本命名反而会留下一堆永远用不上的半成品。
func (p *mirrorProvider) cachePaths() (dir, part, state string) {
	dir = p.cacheDir
	if dir == "" {
		dir = filepath.Join(platform.DefaultDataDir(), "update", "cache")
	}
	return dir, filepath.Join(dir, "pending.part"), filepath.Join(dir, "pending.state")
}

// downloadURL 下载单个 URL 到缓存文件，完整成功后一次性拷进 dst。
func (p *mirrorProvider) downloadURL(ctx context.Context, urlStr string, rel *updater.Release, dst io.Writer, onProgress func(written, total int64)) error {
	dir, partPath, statePath := p.cachePaths()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建更新缓存目录失败: %w", err)
	}

	// 有可续传的半成品时直接用它的总长与分块方案，省掉探测请求：
	// 探测会从 0 再拉一条连接，白传几 MB 才发现本该续传。
	total, parts := int64(0), 1
	if st, ok := dlutil.ReadResume(statePath); ok && st.Source == urlStr {
		total, parts = st.Total, len(st.Parts)
	}
	if total <= 0 {
		t, ps, err := p.probe(ctx, urlStr, rel)
		if err != nil {
			return err
		}
		total, parts = t, ps
	}

	f, err := os.OpenFile(partPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	tracker := dlutil.NewProgressTracker(total, func(done, t int64, _ int) {
		if onProgress != nil {
			onProgress(done, t)
		}
	})

	run := func(blockParts int) error {
		return dlutil.DownloadResumable(dlutil.ResumeOptions{
			Ctx:       ctx,
			Client:    p.client,
			URL:       urlStr,
			File:      f,
			Total:     total,
			Parts:     blockParts,
			StatePath: statePath,
			OnResume: func(done int64) {
				if done > 0 {
					logger.I("[update] 命中未完成的下载，从 %d%% 续传（%d/%d 字节）", done*100/total, done, total)
				}
				// 基线抬到断点处，否则界面上的进度会从 0 重新爬
				tracker.Seed(done)
			},
			Progress: tracker.Add,
		})
	}
	err = run(parts)
	if errors.Is(err, dlutil.ErrRangeUnsupported) && parts > 1 {
		// 探测声称支持 Range、真正发 Range 却回整份（中间层 CDN 会这么干）：
		// 并行方案不成立，退回单流再来一次，而不是把这次更新直接判死。
		logger.W("[update] 源实际不支持 Range（探测误报），退回单流下载")
		_ = os.Remove(statePath)
		parts = 1
		err = run(1)
	}
	if err != nil {
		// 保留 part/state：网络抖动或 3 分钟超时后重试可从断点继续
		return err
	}

	// 完整数据才交给 dst（wails 侧是 file+hasher，半份数据会污染摘要导致验签失败）
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(dst, f); err != nil {
		return err
	}

	// Windows 上文件句柄没关是删不掉的（Go 的 OpenFile 不带 FILE_SHARE_DELETE），
	// 这里先显式关闭；defer 里那次 Close 会返回 ErrClosed，忽略即可。
	_ = f.Close()
	_ = os.Remove(statePath)
	_ = os.Remove(partPath)
	return nil
}

// probe 探测包体大小与是否支持 Range，随即断开连接
// （真正的下载由 DownloadResumable 重新发 Range 请求）。
func (p *mirrorProvider) probe(ctx context.Context, urlStr string, rel *updater.Release) (int64, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return 0, 1, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "QuickDock-Updater/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, 1, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, 1, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	// 单流路径的进度显示需要总量：Content-Length 缺失时回退 manifest 里记录的大小
	total := resp.ContentLength
	if total <= 0 {
		total = rel.Artifact.Size
	}
	if total <= 0 {
		return 0, 1, errors.New("下载失败: 无法确定安装包大小")
	}

	parts := 1
	// 服务器明确返回 Content-Length 且支持 Range、包体超阈值时 4 连接分块并行
	// （GitHub Releases 单连接常被限速到几十 KB/s，分块可显著提速；ghfast.top
	// 等镜像 CDN 通常也支持 Range，天然兼容）。不满足条件自动回退单流。
	if resp.ContentLength > dlutil.ParallelMinSize && strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes") {
		parts = dlutil.DownloadConcurrency
	}
	return total, parts, nil
}
