package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// 国内直连 GitHub Releases 常超时。mirrorProvider 包装 github provider：
// 直连下载失败时自动改用镜像前缀重试（ghfast.top 等），安装包带 Ed25519 签名验证，
// 镜像只改传输 URL、无法篡改内容，安全性不变。
//
// 每次尝试限时 3 分钟：直连卡死时不会无限等待，镜像也失败则返回最后错误。
// 下载先落临时文件、成功后一次性写入 dst —— wails 的 download() 用
// io.MultiWriter(file, hasher) 做流式哈希，若中途失败残留部分字节再重试，
// 哈希会被污染导致签名校验失败；临时文件方案保证 dst 只在完整成功时收到数据。

// updateMirrorDefaults 默认镜像前缀（保持更新，失效可换）。可经 SetUpdateMirrors 覆盖。
var updateMirrorDefaults = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
}

const perURLTimeout = 3 * time.Minute

type mirrorProvider struct {
	inner   updater.Provider
	client  *http.Client
	mirrors []string
}

// NewMirrorUpdaterProvider 包装 github provider，Download 直连失败时自动镜像重试。
// mirrors 为空时使用内置默认镜像列表（updateMirrorDefaults）。
func NewMirrorUpdaterProvider(inner updater.Provider, client *http.Client, mirrors ...string) updater.Provider {
	ms := mirrors
	if len(ms) == 0 {
		ms = updateMirrorDefaults
	}
	return &mirrorProvider{inner: inner, client: client, mirrors: ms}
}

func (p *mirrorProvider) Name() string { return p.inner.Name() }

func (p *mirrorProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	return p.inner.Check(ctx, req)
}

func (p *mirrorProvider) Download(ctx context.Context, rel *updater.Release, dst io.Writer, onProgress func(written, total int64)) error {
	urlStr, _ := rel.Metadata["github.asset.url"].(string)
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
	if total <= 0 {
		total = rel.Artifact.Size
	}
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

	// 完整下载成功：一次性写入 dst（file + hasher），保证哈希只覆盖完整数据
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(dst, tmp); err != nil {
		return err
	}
	return nil
}
