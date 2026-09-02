package env

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// ProxyTransport 返回代理感知的 http transport（环境变量 + 系统代理，Windows 下含 WinINET 注册表）。
func ProxyTransport() http.RoundTripper {
	return proxyTransport()
}

// Download 依次尝试 urls 下载到 dst；任意一个成功即返回。带进度回调与代理穿透。
// 下载先写临时文件，完整成功后才落到 dst，避免中途失败污染目标。
func Download(ctx context.Context, dst string, urls []string, onProgress func(written, total int64)) error {
	var lastErr error
	for _, u := range urls {
		if err := downloadOne(ctx, u, dst, onProgress); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无可用下载地址")
	}
	return lastErr
}

func downloadOne(ctx context.Context, urlStr, dst string, onProgress func(written, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	client := &http.Client{Transport: ProxyTransport()}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("下载失败 HTTP %d: %s", resp.StatusCode, urlStr)
	}

	tmp, err := os.CreateTemp("", "quickdock-dl-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpPath) }()

	total := resp.ContentLength
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
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, tmp); err != nil {
		return err
	}
	return nil
}
