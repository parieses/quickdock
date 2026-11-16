package netdisk

// 写操作与搜索：MKCOL（建目录）、MOVE（重命名/移动）、DELETE（删除）、
// PUT（上传）、BFS 递归搜索。
//
// 立场与 client.go 一致：只讲标准 WebDAV，不讲任何网盘私有协议。网关驱动
// 若支持写（OpenList 的多数驱动都支持），这些方法就直接可用。
//
// 三条容易踩的协议细节，都在这里统一处理：
//   - MOVE 的 Destination 必须是**绝对 URL**，相对路径会被网关拒（400）；
//   - MKCOL 只能建一级目录，父目录必须已存在（父不存在返回 409）；
//   - 上传用 PUT 时要显式设 Content-Length，否则部分网关走 chunked 会 411。

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Mkdir 创建目录。dir 是完整的目标路径（含新目录名，如 "/夸克/电影"）。
func (c *Client) Mkdir(ctx context.Context, dir string) error {
	dir = normalizeDir(dir)
	if dir == "/" {
		return fmt.Errorf("目录名不能为空")
	}
	resp, err := c.do(ctx, "MKCOL", dir, nil, 0, false, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusMethodNotAllowed:
		// 405 在 MKCOL 上的含义是「这个路径已经有东西了」，转成人话。
		return fmt.Errorf("同名文件或目录已存在")
	case http.StatusConflict:
		return fmt.Errorf("上级目录不存在")
	}
	return statusErr("新建目录", resp)
}

// Move 把 from 改名/移动到 to（同源路径）。目标已存在时拒绝，不静默覆盖。
func (c *Client) Move(ctx context.Context, from, to string) error {
	from, to = normalizeDir(from), normalizeDir(to)
	if from == "/" || to == "/" {
		return fmt.Errorf("路径不能为空")
	}
	if from == to {
		return nil
	}
	resp, err := c.do(ctx, "MOVE", from, nil, 0, false, map[string]string{
		// 绝对 URL 由 fullURL 拼：网关拿它和自己的 host 对齐，相对路径会被拒。
		"Destination": c.fullURL(to),
		"Overwrite":   "F",
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusPreconditionFailed:
		return fmt.Errorf("目标名称已存在")
	case http.StatusConflict:
		return fmt.Errorf("目标目录不存在")
	case http.StatusNotFound:
		return fmt.Errorf("源文件或目录不存在")
	}
	return statusErr("重命名", resp)
}

// Delete 删除文件或目录（目录递归删除由网关负责）。
func (c *Client) Delete(ctx context.Context, remotePath string) error {
	remotePath = normalizeDir(remotePath)
	if remotePath == "/" {
		return fmt.Errorf("不能删除根目录")
	}
	resp, err := c.do(ctx, http.MethodDelete, remotePath, nil, 0, false, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("文件或目录不存在")
	}
	return statusErr("删除", resp)
}

// Upload 把本地 reader 上传到 remotePath（PUT）。
//
// 用 dlClient（无整体超时）：几 GB 的文件在慢网盘上要传很久，30s 的常规客户端
// 会从中间掐断。取消走 ctx。
func (c *Client) Upload(ctx context.Context, remotePath string, r io.Reader, size int64, onProgress func(done, total int64)) error {
	remotePath = normalizeDir(remotePath)
	if remotePath == "/" {
		return fmt.Errorf("上传路径不能为空")
	}
	body := io.Reader(r)
	if onProgress != nil && size > 0 {
		body = &progressReader{r: r, total: size, on: onProgress, last: -1}
	}
	resp, err := c.do(ctx, http.MethodPut, remotePath, body, size, true, map[string]string{
		"Content-Type": contentTypeFor(remotePath),
	})
	if err != nil {
		return fmt.Errorf("上传失败: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("上级目录不存在")
	case http.StatusInsufficientStorage:
		return fmt.Errorf("网盘空间不足")
	}
	return statusErr("上传", resp)
}

// SearchOptions 递归搜索的护栏。网盘目录动辄上万个文件，不设上限的 BFS 会把
// 网关打到超时、也让用户对着转圈等半分钟。
type SearchOptions struct {
	MaxResults int // 命中上限，默认 200
	MaxDirs    int // 最多访问多少个目录，默认 150
	MaxDepth   int // 相对 root 的最大深度，默认 6
}

func (o SearchOptions) normalized() SearchOptions {
	if o.MaxResults <= 0 {
		o.MaxResults = 200
	}
	if o.MaxDirs <= 0 {
		o.MaxDirs = 150
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = 6
	}
	return o
}

// Search 在 root 下按名称递归搜索（大小写不敏感，包含匹配）。
//
// 串行 BFS 而不是并发：网关多半是单机单进程，并发 PROPFIND 只会互相抢带宽，
// 还会让某些驱动触发风控。够快的前提是「目录少、层级浅」，超出护栏就返回
// 已找到的部分——宁可少给几条，也不要卡住整个页面。
func (c *Client) Search(ctx context.Context, root, keyword string, opt SearchOptions) ([]Entry, error) {
	root = normalizeDir(root)
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return nil, nil
	}
	opt = opt.normalized()

	type node struct {
		path  string
		depth int
	}
	queue := []node{{path: root}}
	visited := 0
	var out []Entry

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		cur := queue[0]
		queue = queue[1:]
		visited++
		if visited > opt.MaxDirs {
			break
		}
		items, err := c.List(ctx, cur.path)
		if err != nil {
			// 单个子目录读不了（权限/驱动异常）不该让整次搜索失败，
			// 跳过它继续找——搜索结果本来就是「尽力而为」。
			if cur.path == root {
				return nil, err
			}
			continue
		}
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.Name), keyword) {
				out = append(out, it)
				if len(out) >= opt.MaxResults {
					return out, nil
				}
			}
			if it.IsDir && cur.depth < opt.MaxDepth {
				queue = append(queue, node{path: it.Path, depth: cur.depth + 1})
			}
		}
	}
	return out, nil
}

// ---- 内部 ----

// do 发起一个请求。big=true 用无整体超时的客户端（上传），否则用常规客户端。
func (c *Client) do(ctx context.Context, method, rel string, body io.Reader, size int64, big bool, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.fullURL(rel), body)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPut {
		// 显式长度（0 也要显式）：不定长会让网关走 chunked，部分驱动直接 411。
		req.ContentLength = size
	}
	c.auth(req)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	client := c.client
	if big {
		client = c.dlClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接网盘网关: %w", err)
	}
	return resp, nil
}

// statusErr 把非 2xx 响应翻成带网关原话的错误。
func statusErr(action string, resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if s := errSnippet(head); s != "" {
		return fmt.Errorf("%s失败（HTTP %d）：%s", action, resp.StatusCode, s)
	}
	return fmt.Errorf("%s失败（HTTP %d）", action, resp.StatusCode)
}

// contentTypeFor 按扩展名给上传类型；认不出就用二进制流。
func contentTypeFor(remotePath string) string {
	if ct := mime.TypeByExtension(strings.ToLower(path.Ext(remotePath))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// progressReader 在读取的同时回报进度。
//
// 只按整数百分比回调：上传一个 2GB 的文件会读几万次，每次回调都发事件会把
// 前端淹没在 IPC 消息里。
type progressReader struct {
	r     io.Reader
	done  int64
	total int64
	last  int
	on    func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		total := p.total
		if total <= 0 {
			total = 1
		}
		if pct := int(p.done * 100 / total); pct != p.last {
			p.last = pct
			p.on(p.done, p.total)
		}
	}
	return n, err
}
