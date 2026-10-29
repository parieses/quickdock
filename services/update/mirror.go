package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	dlutil "quickdock/internal/dl"
	"quickdock/internal/logger"
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
// 下载先落临时文件、成功后一次性写入 dst —— wails 的 download() 用
// io.MultiWriter(file, hasher) 做流式哈希，若中途失败残留部分字节再重试，
// 哈希会被污染导致签名校验失败；临时文件方案保证 dst 只在完整成功时收到数据。

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
}

// NewMirrorUpdaterProvider 包装 endpoint provider，Check/Download 直连 GitHub
// 失败时自动镜像重试。manifestURL 为直连 manifest 地址（空则 Check 不做镜像
// 重试，仅保留 Download 行为）；mirrors 为空时使用内置默认镜像列表
// （UpdateMirrorDefaults）。
func NewMirrorUpdaterProvider(inner updater.Provider, client *http.Client, manifestURL string, mirrors ...string) updater.Provider {
	ms := mirrors
	if len(ms) == 0 {
		ms = UpdateMirrorDefaults
	}
	return &mirrorProvider{inner: inner, client: client, mirrors: ms, manifestURL: manifestURL}
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

// downloadURL 下载单个 URL 到临时文件，成功后拷贝进 dst
func (p *mirrorProvider) downloadURL(ctx context.Context, urlStr string, rel *updater.Release, dst io.Writer, onProgress func(written, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "QuickDock-Updater/1.0")

	tmp, err := os.CreateTemp("", "quickdock-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	total := resp.ContentLength
	// 单流路径的进度显示需要总量：Content-Length 缺失时回退 manifest 里记录的大小
	if total <= 0 {
		total = rel.Artifact.Size
	}

	// 服务器明确返回 Content-Length 且支持 Range、包体超阈值时，4 连接分块并行下载
	// （与插件市场 dlutil.DownloadParallelTo 同一实现）。GitHub Releases 单连接常被限速到
	// 几十 KB/s，分块可显著提速；ghfast.top 等镜像 CDN 通常也支持 Range，天然兼容。
	// 不满足条件（chunked 传输/小包/不支持 Range）自动回退下方单流。
	if resp.ContentLength > dlutil.ParallelMinSize && strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes") {
		resp.Body.Close()
		if err := tmp.Truncate(total); err != nil {
			return err
		}
		tracker := dlutil.NewProgressTracker(total, func(done, t int64, _ int) {
			if onProgress != nil {
				onProgress(done, t)
			}
		})
		if err := dlutil.DownloadParallelTo(p.client, urlStr, tmp, total, tracker.Add); err != nil {
			return err
		}
	} else {
		written := int64(0)
		buf := make([]byte, 64*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := tmp.Write(buf[:n]); werr != nil {
					return werr
				}
				written += int64(n)
				if onProgress != nil {
					onProgress(written, total)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return rerr
			}
		}
	}

	// 完整下载成功：一次性写入 dst（file + hasher），保证哈希只覆盖完整数据
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(dst, tmp); err != nil {
		return err
	}
	return nil
}
