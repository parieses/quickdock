// Package netdisk 网盘访问层：把「本机网盘网关」当成一个可列目录、可下载的
// 远端文件系统。
//
// 设计立场：QuickDock **不实现任何网盘的私有协议**。百度/夸克等网盘由本机的
// OpenList 网关统一转成标准 WebDAV，本包只做 WebDAV 客户端 —— 一份代码通吃
// 所有网盘，网关换了驱动不影响上层。
//
// 与 internal/webdav 的关系：那个包是「备份同步」专用客户端，硬编码了
// quickdock-backup 文件名前缀、只列根目录、不能进子目录，无法复用；
// 本包是通用目录访问客户端，两者并存，不要合并（用途与演进方向都不同）。
//
// 安全边界：
//   - 路径一律以 "/" 开头的**相对**路径表示（相对 endpoint），不做本地文件系统
//     拼接，下载目标路径由 service 层校验；
//   - 凭据（Basic Auth）只存在于本包内存中，绝不写日志、不出现在 URL 里。
package netdisk

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"quickdock/internal/fsview"
)

// Config 网关连接配置。
type Config struct {
	Endpoint    string // WebDAV 基地址，如 http://127.0.0.1:5244/dav
	Username    string
	Password    string
	InsecureTLS bool // 自签证书场景，由用户显式开启
}

// Entry 目录项。Path 是相对 endpoint 的路径（以 "/" 开头，目录不带尾斜杠）。
type Entry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	IsDir    bool   `json:"isDir"`
	Modified string `json:"modified"`
	// Kind 预览形态（fsview.Kind 的字符串值：image/video/audio/pdf/text/code/…）。
	// 由后端按扩展名判定后下发，前端只按它选渲染器，不自己再维护一份扩展名表。
	Kind string `json:"kind"`
}

// Client 一个网关的 WebDAV 客户端。
type Client struct {
	origin   string // scheme://host[:port]，拼完整 URL 用（缺了它请求会变相对路径）
	basePath string // endpoint 的 URL 路径前缀（如 /dav），拼 URL 与剥 href 用
	username string
	password string
	client   *http.Client // 常规操作（列目录/探测），30s 超时
	dlClient *http.Client // 下载专用：无整体超时，依赖 ctx 取消
}

// New 创建客户端。endpoint 合法性在此校验（scheme 必须是 http/https）。
func New(cfg Config) (*Client, error) {
	ep := strings.TrimSpace(cfg.Endpoint)
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("endpoint 必须是 http/https 地址: %q", cfg.Endpoint)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("endpoint 缺少主机地址: %q", cfg.Endpoint)
	}
	return &Client{
		origin:   u.Scheme + "://" + u.Host,
		basePath: strings.TrimRight(u.Path, "/"),
		username: cfg.Username,
		password: cfg.Password,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureTLS},
			},
		},
		dlClient: &http.Client{
			// 下载/上传不设整体超时：几 GB 文件走慢网关会被 30s 误杀；取消靠 ctx。
			// 但「响应头」必须有上限：网关挂起时（驱动卡死、存储未初始化）
			// 否则会永久等下去，用户看到的是一个永远不动的进度条。
			Transport: &http.Transport{
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: cfg.InsecureTLS},
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}, nil
}

// escapePath 把相对路径逐段做 URL 路径编码。
// 必须逐段转义而不是对整条 path 一次性转义：整条转义会把 "/" 也编码掉，
// 而网盘目录名里的空格、中文、"+" 只允许出现在段内。
func escapePath(rel string) string {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return ""
	}
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// fullURL 把相对路径拼成完整 URL。
// 必须带 origin（scheme://host），否则 http.Client 会把 "/dav/xxx" 当成相对地址，
// 直接以 "unsupported protocol scheme" 失败。
func (c *Client) fullURL(rel string) string {
	p := c.basePath + "/" + escapePath(rel)
	if p == "/" {
		return c.origin + "/"
	}
	return c.origin + p
}

// auth 设置 Basic Auth（凭据为空则跳过——部分网关允许匿名只读）。
func (c *Client) auth(req *http.Request) {
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
}

// ---- PROPFIND 响应结构 ----

type multistatus struct {
	XMLName  xml.Name   `xml:"DAV: multistatus"`
	Response []response `xml:"response"`
}

type response struct {
	Href     string   `xml:"href"`
	Propstat propstat `xml:"propstat"`
}

type propstat struct {
	Prop   prop   `xml:"prop"`
	Status string `xml:"status"`
}

type prop struct {
	GetContentLength string        `xml:"getcontentlength"`
	GetLastModified  string        `xml:"getlastmodified"`
	ResourceType     *resourceType `xml:"resourcetype"`
}

// resourceType 里 Collection 必须是**指针**。
//
// 陷阱：WebDAV 的目录标记是空元素 <d:collection/>，解到 string 会得到空串，
// 再拿它判空就等于「目录永远是文件」——表现为列表能出来但双击进不去、
// 目录还被挂上下载按钮。用指针取「元素有没有出现」才是对的。
type resourceType struct {
	Collection *struct{} `xml:"collection"`
}

// propfindBody 标准的 PROPFIND 请求体。比空 body 稳：部分网关对空 body
// 的 Depth:1 请求只返回文件名不带属性。
const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop>
<d:displayname/><d:getcontentlength/><d:getlastmodified/><d:resourcetype/>
</d:prop></d:propfind>`

// ErrUnauthorized 网关拒绝了 Basic Auth（HTTP 401）。
//
// 单独拿出来是因为「401」几乎总是密码漂移（凭据库里的值 ≠ 网关库里的值），
// 上层能据此给出可操作的指引，而不是让用户对着「用户名或密码错误」去猜。
var ErrUnauthorized = errors.New("网关拒绝认证（HTTP 401）")

// Test 探测连通性：PROPFIND Depth:0 根目录。错误信息区分 401（凭据错）与其它。
func (c *Client) Test(ctx context.Context) error {
	status, _, err := c.propfind(ctx, "/", 0)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusUnauthorized:
		return ErrUnauthorized
	case status >= 200 && status < 300:
		return nil
	default:
		return fmt.Errorf("服务器返回 HTTP %d", status)
	}
}

// List 列出目录内容（PROPFIND Depth:1）。目录在前、按名称排序，dir 自身被剔除。
func (c *Client) List(ctx context.Context, dir string) ([]Entry, error) {
	dir = normalizeDir(dir)
	status, body, err := c.propfind(ctx, dir, 1)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, fmt.Errorf("目录不存在: %s", dir)
	case status == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case status < 200 || status >= 300:
		if s := errSnippet(body); s != "" {
			// 网关自己给的原因（如 "require login [guest]"、存储未初始化）比
			// HTTP 状态码有用得多：不带上它，用户只能去翻网关日志。
			return nil, fmt.Errorf("列目录失败（HTTP %d）：%s", status, s)
		}
		return nil, fmt.Errorf("列目录失败（HTTP %d）", status)
	}

	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("解析目录响应失败: %w", err)
	}

	self := strings.TrimSuffix(dir, "/")
	var out []Entry
	for _, r := range ms.Response {
		rel, ok := c.relFromHref(r.Href)
		if !ok || rel == "" || rel == "/" {
			continue
		}
		if strings.TrimSuffix(rel, "/") == self {
			continue
		}
		p := r.Propstat.Prop
		name := path.Base(strings.TrimSuffix(rel, "/"))
		isDir := p.ResourceType != nil && p.ResourceType.Collection != nil
		entry := Entry{
			Name:     name,
			Path:     strings.TrimSuffix(rel, "/"),
			Size:     parseInt64(p.GetContentLength),
			IsDir:    isDir,
			Modified: p.GetLastModified,
		}
		if !isDir {
			entry.Kind = string(fsview.KindByExt(path.Ext(name)))
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Download 把远端文件写入 w。返回文件总大小（服务器未给 Content-Length 时为 -1）。
func (c *Client) Download(ctx context.Context, remotePath string, w io.Writer, onProgress func(done, total int64)) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fullURL(remotePath), nil)
	if err != nil {
		return -1, err
	}
	c.auth(req)
	resp, err := c.dlClient.Do(req)
	if err != nil {
		return -1, fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return -1, fmt.Errorf("文件不存在: %s", remotePath)
	case resp.StatusCode == http.StatusUnauthorized:
		return -1, ErrUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		// 失败响应体很小，读一段给用户看网关怎么说（如"分享已失效"）。
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if s := errSnippet(head); s != "" {
			return -1, fmt.Errorf("下载失败（HTTP %d）：%s", resp.StatusCode, s)
		}
		return -1, fmt.Errorf("下载失败（HTTP %d）", resp.StatusCode)
	}

	total := resp.ContentLength
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return done, fmt.Errorf("写入本地文件失败: %w", werr)
			}
			done += int64(n)
			if onProgress != nil {
				onProgress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, fmt.Errorf("下载中断: %w", rerr)
		}
	}
	return total, nil
}

// ---- 内部 ----

// propfind 发送 PROPFIND 并返回（状态码, 响应体, 错误）。
func (c *Client) propfind(ctx context.Context, dir string, depth int) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.fullURL(dir), strings.NewReader(propfindBody))
	if err != nil {
		return 0, nil, err
	}
	c.auth(req)
	req.Header.Set("Depth", fmt.Sprintf("%d", depth))
	req.Header.Set("Content-Type", "application/xml")

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("无法连接网盘网关: %w", err)
	}
	defer resp.Body.Close()
	const maxResp = 32 << 20 // 32MB：目录列表响应的上限，防异常响应撑爆内存
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResp))
	if rerr != nil {
		return resp.StatusCode, nil, fmt.Errorf("读取响应失败: %w", rerr)
	}
	return resp.StatusCode, body, nil
}

// relFromHref 把 PROPFIND 响应里的 href 还原成相对路径。
//
// 陷阱：href 可能是完整 URL（http://host/dav/%E4%B8%AD/）也可能是路径（/dav/%E4%B8%AD/），
// 且百分号编码由服务器决定。url.Parse 的 Path 字段已解码，剥掉 endpoint 的路径
// 前缀后就是相对路径 —— 不做这一步，中文目录名会带着 %XX 进前端，拼回去就 404。
func (c *Client) relFromHref(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return "", false
	}
	p := u.Path
	if c.basePath != "" && c.basePath != "/" {
		if !strings.HasPrefix(p, c.basePath) {
			return "", false
		}
		p = strings.TrimPrefix(p, c.basePath)
	}
	p = "/" + strings.Trim(p, "/")
	if p == "/" {
		return "/", true
	}
	return p, true
}

// errSnippet 从错误响应体里取一小段可读文本，用于拼进错误信息。
//
// 只保留单行、截断到 160 字节：响应体可能是 HTML 错误页或压缩过的二进制，
// 直接塞进错误信息会把前端提示撑爆。取不到可读内容就返回空串。
func errSnippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	// HTML 错误页没有可读价值，交给状态码表达。
	if strings.HasPrefix(s, "<") && strings.Contains(strings.ToLower(s[:min(len(s), 200)]), "<html") {
		return ""
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// normalizeDir 统一目录表示：空串和 "." 都表示根；保证以 "/" 开头、不带尾斜杠。
func normalizeDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return "/"
	}
	dir = "/" + strings.Trim(dir, "/")
	if dir == "/" {
		return "/"
	}
	return strings.TrimSuffix(dir, "/")
}

// parseInt64 容错解析 Content-Length（某些网关返回空串或带空格）。
func parseInt64(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int64
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
