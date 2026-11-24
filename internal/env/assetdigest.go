package env

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// 官方哈希校验。
//
// 背景：环境管理的下载源里有大量第三方镜像（npmmirror、gh-proxy、各高校镜像），
// 这些镜像终结 TLS 后完全可以把二进制换掉，而装完即写 PATH、下次直接执行。
// 只要能从官方渠道拿到 sha256，就应该在落盘后比对一次——镜像能改内容，改不了
// 官方 API 返回的摘要。
//
// GitHub Release 资产自 2024 年起在 API 里带 digest 字段（"sha256:..."）。拿不到
// 时（非 GitHub 源、API 限流/不可达、老 Release 没补 digest）只记日志跳过，不阻断
// 安装：把「无法校验」当成「校验失败」会让离线/内网环境彻底装不了东西。

// ghReleaseAssetRe 从下载地址里认出 GitHub Release 资产路径。
// 下载地址常被镜像加了前缀（https://gh-proxy.com/https://github.com/...），
// 所以不做整串匹配，只在任意位置找 "github.com/<owner>/<repo>/releases/download/<tag>/<asset>"。
var ghReleaseAssetRe = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/releases/download/([^/]+)/([^/?#]+)`)

// ghDigestTimeout GitHub API 查询超时。校验是「尽力而为」的附加步骤，
// 不能因为 API 慢而把一次已经成功的下载拖到超时。
const ghDigestTimeout = 10 * time.Second

type ghReleaseAsset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type ghReleaseResp struct {
	Assets []ghReleaseAsset `json:"assets"`
}

// fileSHA256 计算文件 sha256（十六进制小写）。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// parseGitHubAsset 从候选地址里解析出 owner/repo/tag/asset。
// 找不到返回空串，调用方据此跳过校验。
func parseGitHubAsset(u string) (owner, repo, tag, asset string) {
	m := ghReleaseAssetRe.FindStringSubmatch(u)
	if m == nil {
		return "", "", "", ""
	}
	asset = m[4]
	// 资产名可能被 URL 编码（空格等），比对 API 的 name 前先还原。
	if dec, err := url.PathUnescape(asset); err == nil {
		asset = dec
	}
	return m[1], m[2], m[3], asset
}

// fetchGitHubAssetDigest 查 GitHub Release API 拿某个资产的官方 sha256。
// 返回空串表示「拿不到」（不视为错误）。
func fetchGitHubAssetDigest(ctx context.Context, owner, repo, tag, asset string) (string, error) {
	api := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, url.PathEscape(tag))
	cctx, cancel := context.WithTimeout(ctx, ghDigestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, api, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "QuickDock/1.0")
	resp, err := (&http.Client{Transport: ProxyTransport()}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 404（无该 tag）/ 403（匿名限流）都属常见情形，交给上层记日志跳过。
		return "", fmt.Errorf("GitHub Release API 返回 HTTP %d", resp.StatusCode)
	}
	var rel ghReleaseResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rel); err != nil {
		return "", err
	}
	for _, a := range rel.Assets {
		if a.Name != asset {
			continue
		}
		d := strings.TrimSpace(a.Digest)
		if !strings.HasPrefix(strings.ToLower(d), "sha256:") {
			return "", nil // 老 Release 没有 digest 字段
		}
		return strings.TrimSpace(d[len("sha256:"):]), nil
	}
	return "", nil
}

// VerifyOfficialDigest 尽力校验本地文件与官方发布的 sha256 是否一致。
//
// 返回：
//   - (true, nil)  校验通过
//   - (false, nil) 拿不到官方哈希，跳过（调用方继续，不视为失败）
//   - (false, err) 拿到哈希但不一致，文件不可信
func VerifyOfficialDigest(ctx context.Context, file string, srcURLs ...string) (bool, error) {
	var owner, repo, tag, asset string
	for _, u := range srcURLs {
		if o, r, t, a := parseGitHubAsset(u); a != "" {
			owner, repo, tag, asset = o, r, t, a
			break
		}
	}
	if asset == "" {
		return false, nil
	}
	want, err := fetchGitHubAssetDigest(ctx, owner, repo, tag, asset)
	if err != nil {
		logger.W("[env][digest] %s/%s@%s 取官方哈希失败，跳过校验: %v", owner, repo, tag, err)
		return false, nil
	}
	if want == "" {
		logger.I("[env][digest] %s/%s@%s 无官方 sha256，跳过校验", owner, repo, tag)
		return false, nil
	}
	got, err := fileSHA256(file)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(got, want) {
		return false, fmt.Errorf("文件哈希与官方发布不一致（可能是镜像返回了被替换的内容）: 期望 sha256:%s，实际 %s", want, got)
	}
	logger.I("[env][digest] %s 官方 sha256 校验通过", filepath.Base(file))
	return true, nil
}

// verifyBinaryMagic 校验文件头是否为可执行文件（PE / ELF / Mach-O）。
// 单文件 exe/phar 类下载没有 zip 头可查，源站返回 200 + HTML 错误页时会被当成
// 「安装成功」并落成最终可执行文件，此后 fileExists 短路让它永远修不好。
// 这里按目标平台期望的魔数把关，顺便挡掉明显过小的残包。
func verifyBinaryMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	// 真实运行时二进制远大于 4KB；小于这个量级必然是错误页或被截断的残包。
	const minBinarySize = 4096
	if st.Size() < minBinarySize {
		return fmt.Errorf("%w：文件仅 %d 字节", errContentNotBinary, st.Size())
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return fmt.Errorf("%w：读取文件头失败 %v", errContentNotBinary, err)
	}
	switch {
	case hdr[0] == 'M' && hdr[1] == 'Z': // PE / Windows 可执行
		return nil
	case hdr[0] == 0x7f && hdr[1] == 'E' && hdr[2] == 'L' && hdr[3] == 'F': // ELF
		return nil
	case hdr[0] == 0xfe && hdr[1] == 0xed && hdr[2] == 0xfa: // Mach-O 64 大端
		return nil
	case hdr[0] == 0xcf && hdr[1] == 0xfa && hdr[2] == 0xed && hdr[3] == 0xfe: // Mach-O 64 小端
		return nil
	case hdr[0] == 0xca && hdr[1] == 0xfe && hdr[2] == 0xba && hdr[3] == 0xbe: // Mach-O Fat
		return nil
	}
	return fmt.Errorf("%w：文件头 %x 不是可执行文件", errContentNotBinary, hdr)
}

// errContentNotBinary 表示下载到的内容是错误页而非期望的二进制。
var errContentNotBinary = errors.New("下载内容不是有效的二进制文件（源站可能返回了错误页）")
