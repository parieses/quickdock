package env

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProxyTransport 返回代理感知的 http transport（环境变量 + 系统代理，Windows 下含 WinINET 注册表）。
func ProxyTransport() http.RoundTripper {
	return proxyTransport()
}

// downloadMaxAttempts 单个下载地址的最大重试次数（指数退避）。
// MySQL/MariaDB 等大包网络抖动即整体失败，重试可显著提升成功率。
const downloadMaxAttempts = 4

// Download 依次尝试 urls 下载到 dst；任意一个地址成功即返回。带进度回调与代理穿透。
// 下载先写临时文件，完整成功后才落到 dst，避免中途失败污染目标。
// 每个地址最多重试 downloadMaxAttempts 次（指数退避），全部失败再尝试下一个候选地址。
func Download(ctx context.Context, dst string, urls []string, onProgress func(written, total int64)) error {
	var lastErr error
	for _, u := range urls {
		var attemptErr error
		for attempt := 0; attempt < downloadMaxAttempts; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
			}
			if attemptErr = downloadOne(ctx, u, dst, onProgress); attemptErr == nil {
				return nil
			}
			lastErr = attemptErr
		}
		// 该地址多次重试仍失败，继续尝试下一个候选地址
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
	// 防御：部分源（如 Apache Lounge）对不存在的路径返回 HTTP 200 + text/html 错误页，
	// 若把 HTML 当成 zip 下载，解压阶段才会报 "zip: not a valid zip file"，极难定位。
	// 这里在下载阶段提前拦截（仅对 .zip 目标）。
	if strings.HasSuffix(strings.ToLower(dst), ".zip") {
		if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "text/html") {
			return fmt.Errorf("下载源返回了 HTML 页面而非 ZIP 压缩包（HTTP %d, Content-Type=%s）: %s", resp.StatusCode, ct, urlStr)
		}
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
	// 调用方常把 zip 落到 os.TempDir()/quickdock-runtime/ 之类的子目录，dst 父目录可能不存在，
	// os.Create 会直接报 "The system cannot find the path specified"。提前建好父目录。
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建下载目录失败 %s: %w", dir, err)
		}
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, tmp); err != nil {
		return err
	}
	// 落盘后再做一次魔数校验，拦截「后缀为 .zip 但内容不是 zip」的情况
	// （如源站对不存在路径返回 HTML 错误页但 HTTP 200，Content-Type 未必标记 text/html）。
	if strings.HasSuffix(strings.ToLower(dst), ".zip") {
		if err := verifyZipMagic(dst); err != nil {
			os.Remove(dst)
			return err
		}
	}
	return nil
}

// verifyZipMagic 校验文件是否为合法的 ZIP 压缩包（检查本地文件头魔数）。
// 仅接受 PK\x03\x04（常规）、PK\x05\x06（空归档）、PK\x07\x08（分卷）三种起始签名。
func verifyZipMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, 4)
	n, rerr := io.ReadFull(f, hdr)
	if rerr != nil && rerr != io.EOF {
		return rerr
	}
	if n < 4 {
		return fmt.Errorf("下载文件过小，可能不是有效的 ZIP 压缩包")
	}
	if !bytes.Equal(hdr, []byte("PK\x03\x04")) &&
		!bytes.Equal(hdr, []byte("PK\x05\x06")) &&
		!bytes.Equal(hdr, []byte("PK\x07\x08")) {
		return fmt.Errorf("下载文件不是有效的 ZIP 压缩包（下载源可能返回了错误页）")
	}
	return nil
}
