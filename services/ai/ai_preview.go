package ai

// 文件预览（bound）：回答产物条点开后的内容读取。
//
// 为什么独立成一个方法而不是复用工具的 file_read：那条路走的是「给模型看」的
// 上下文预算与截断策略，前端预览要的是「给人看」的完整性与 data URL——
// 两者的截断上限、二进制处理、错误话术都不一样，硬合用一个入口只会互相迁就。
//
// 安全面与工具侧一致：读白名单（快捷项目录 / 会话工作目录）由 fsguard 把关，
// 凭据类文件一律拒绝——产物条上的文件本来都是 AI 刚写过的，正常永远过得了闸；
// 这道闸拦的是「把路径改成 ../../ 之外的文件再让前端读出来」这条路。

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/fsguard"
	"quickdock/internal/logger"
	"quickdock/services"
)

const (
	// aiPreviewSizeMax 预览的文件体积上限。再大的文件预览也没有意义
	//（几十 MB 的日志拖垮 WebView2），直接让用户去系统里打开。
	aiPreviewSizeMax = 20 << 20
	// aiPreviewTextMax 文本预览回传的内容上限。超出截断并标记，
	// 前端显示「已截断」而不是无声吞掉后半截。
	aiPreviewTextMax = 512 << 10
	// aiPreviewBinProbe 二进制嗅探的采样字节数：开头 8KB 出现 NUL 即判二进制。
	aiPreviewBinProbe = 8 << 10
)

// aiPreviewImageMime 按扩展名认图片。列表刻意短：只收浏览器 img 能直接渲染的，
// psd 之类收进来也显示不了，不如走二进制分支让用户在系统里打开。
var aiPreviewImageMime = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
	".ico": "image/x-icon", ".svg": "image/svg+xml",
}

// aiPreviewHTMLExts 按扩展名认 HTML：这类「产物」用户要的是看渲染后的页面，
// 不是读源码（让 AI 生成 HTML 报告/页面是高频用法）。前端拿到 kind=html
// 后放进沙箱 iframe 渲染，源码视图只是可切换的备选。
var aiPreviewHTMLExts = map[string]bool{".html": true, ".htm": true}

// AIReadPreviewFile 读一个文件供前端预览（bound）。
//
// 返回值按 kind 分三路：
//   - image：dataUrl（base64，img 标签直接渲染）
//   - text：content（已按上限截断，truncated 标记）
//   - binary：两边都不是（含 NUL 的非图片），前端引导去系统里打开
func (a *AIService) AIReadPreviewFile(path string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw := strings.TrimSpace(path)
	if raw == "" {
		return services.Fail(errors.New("缺少参数 path"))
	}
	// 实测反馈里「打开产物要好几秒」最难判断的是慢在谁身上：Go 侧读文件、
	// 传输排队、还是前端首帧渲染。这里只给 Go 侧的耗时——超过 300ms 打一条，
	// 日志里没有这条就说明慢在前端（或文件系统之外）。
	start := time.Now()
	abs, err := fsguard.ResolveRead(a.App.DB, raw, "", true)
	defer func() {
		if d := time.Since(start); d > 300*time.Millisecond {
			logger.W("[ai-preview] 读取 %s 耗时 %s", abs, d.Round(time.Millisecond))
		}
	}()
	if err != nil {
		return services.Fail(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return services.Fail(fmt.Errorf("无法访问 %s：%v", abs, err))
	}
	if info.Size() > aiPreviewSizeMax {
		return services.Fail(fmt.Errorf("%s 有 %dMB，超过预览上限 %dMB；请在系统里打开查看",
			filepath.Base(abs), info.Size()>>20, aiPreviewSizeMax>>20))
	}

	out := map[string]any{
		"path":    abs,
		"name":    filepath.Base(abs),
		"size":    info.Size(),
		"kind":    "binary",
		"modTime": info.ModTime().Format(time.RFC3339),
	}

	ext := strings.ToLower(filepath.Ext(abs))
	if mime, ok := aiPreviewImageMime[ext]; ok {
		data, err := os.ReadFile(abs)
		if err != nil {
			return services.Fail(fmt.Errorf("读取失败：%v", err))
		}
		out["kind"] = "image"
		out["mime"] = mime
		out["dataUrl"] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
		return services.Ok(out)
	}

	// HTML：当文本读，但 kind 单独标出——前端对它的处理是 iframe 渲染而不是 pre 源码。
	if aiPreviewHTMLExts[ext] {
		data, err := os.ReadFile(abs)
		if err != nil {
			return services.Fail(fmt.Errorf("读取失败：%v", err))
		}
		content := string(data)
		truncated := false
		if len(content) > aiPreviewTextMax {
			content = content[:aiPreviewTextMax]
			truncated = true
		}
		out["kind"] = "html"
		out["content"] = content
		out["truncated"] = truncated
		return services.Ok(out)
	}

	f, err := os.Open(abs)
	if err != nil {
		return services.Fail(fmt.Errorf("读取失败：%v", err))
	}
	defer f.Close()
	probe := make([]byte, min(int(info.Size()), aiPreviewBinProbe))
	n, _ := f.Read(probe)
	if bytes.IndexByte(probe[:n], 0) >= 0 {
		// 含 NUL 的非图片（exe、zip、sqlite …）：前端引导去系统里打开，
		// 硬转文本只会得到一屏乱码。
		return services.Ok(out)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return services.Fail(fmt.Errorf("读取失败：%v", err))
	}
	content := string(data)
	truncated := false
	if len(content) > aiPreviewTextMax {
		content = content[:aiPreviewTextMax]
		truncated = true
	}
	out["kind"] = "text"
	out["content"] = content
	out["truncated"] = truncated
	return services.Ok(out)
}

// AISavePreviewFile 把右侧面板里编辑后的文本写回磁盘（bound）。
//
// 与 AIReadPreviewFile 同一套闸：fsguard.ResolveWrite 限定白名单目录，
// 凭据文件与 .git 内部文件拒写——面板的编辑入口是给人用的，但路径终归
// 来自前端，闸照过不误。截断文件前端不提供编辑（保存会丢掉没读到的后半截），
// 这里再兜一道：超过预览回传上限的内容一律拒绝，宁可让用户用编辑器开。
func (a *AIService) AISavePreviewFile(path, content string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw := strings.TrimSpace(path)
	if raw == "" {
		return services.Fail(errors.New("缺少参数 path"))
	}
	if len(content) > aiPreviewTextMax {
		return services.Fail(fmt.Errorf("内容有 %dKB，超过编辑保存上限 %dKB；大文件请在系统编辑器里修改",
			len(content)>>10, aiPreviewTextMax>>10))
	}
	abs, err := fsguard.ResolveWrite(a.App.DB, raw, "")
	if err != nil {
		return services.Fail(err)
	}
	// 与工具侧 guardWritableFile 同一底线（它在 tools 包里未导出，这里不能复用）：
	// 私钥不写是防「密钥被换掉」；.git 内部不写是防「手改 index/HEAD 把仓库搞坏」。
	if fsguard.SecretFile(abs) {
		return services.Fail(fmt.Errorf("%s 属于凭据/密钥文件，不允许写入", filepath.Base(abs)))
	}
	if strings.Contains(strings.ToLower(abs), string(os.PathSeparator)+".git"+string(os.PathSeparator)) {
		return services.Fail(errors.New("不允许直接修改 .git 目录内的文件"))
	}
	mode := os.FileMode(0o644)
	if fi, serr := os.Stat(abs); serr == nil {
		if fi.IsDir() {
			return services.Fail(fmt.Errorf("%s 是目录，不是文件", abs))
		}
		mode = fi.Mode()
	}
	// 先写临时文件再改名：写到一半失败不留半截文件（与工具侧 writeFileAtomic 同款）。
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".qd-preview-*")
	if err != nil {
		return services.Fail(fmt.Errorf("保存失败：%v", err))
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return services.Fail(fmt.Errorf("保存失败：%v", err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return services.Fail(fmt.Errorf("保存失败：%v", err))
	}
	if err := os.Rename(tmpName, abs); err != nil {
		_ = os.Remove(tmpName)
		return services.Fail(fmt.Errorf("保存失败：%v", err))
	}
	_ = os.Chmod(abs, mode)
	info, err := os.Stat(abs)
	if err != nil {
		// 文件已经写成功了，只是回读失败——照常报成功，给个估算值。
		return services.Ok(map[string]any{"path": abs, "size": len(content)})
	}
	return services.Ok(map[string]any{
		"path":    abs,
		"size":    info.Size(),
		"modTime": info.ModTime().Format(time.RFC3339),
	})
}
