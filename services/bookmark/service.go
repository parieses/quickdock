// Package bookmark 浏览器书签导入门面服务。
//
// 只做编排：读文件 → 交给 internal/bookmark 解析 → 交给 internal/db 落库。
// 解析规则（标签容错、文件夹栈、实体解码）与落库映射（文件夹 → 集合、
// 书签 → 网页类型项目）各自留在自己的包里，本层不重复判断。
//
// 书签不另开表：导入结果就是普通的「网页」项目，因此导入完立刻能被命令面板、
// 资源树、AI、MCP 用到；死链检查与查重也只需扫 items 里的网页条目。
package bookmark

import (
	"os"
	"path/filepath"
	"strings"

	ibk "quickdock/internal/bookmark"
	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

// BookmarkService 书签导入与维护的绑定服务。
type BookmarkService struct {
	App *services.AppService
}

// NewBookmarkService 创建 BookmarkService。
func NewBookmarkService(app *services.AppService) *BookmarkService {
	return &BookmarkService{App: app}
}

func (s *BookmarkService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// previewSampleSize 预览时回给前端的示例条数。够让用户确认「选对文件了」就行，
// 整份列表没必要走 IPC。
const previewSampleSize = 5

// previewMaxBytes 预览阶段就读完整文件（解析要全文），但挡住明显选错的超大文件。
const previewMaxBytes = 64 << 20

// ImportPreview 解析书签文件但不写库，用于导入前的确认。
type ImportPreview struct {
	Path      string              `json:"path"`
	Source    string              `json:"source"` // 展示用来源名：浏览器名 · 配置名，HTML 文件为空
	SceneName string              `json:"sceneName"`
	Total     int                 `json:"total"`
	Valid     int                 `json:"valid"`
	Invalid   int                 `json:"invalid"`
	Existing  int                 `json:"existing"` // 库里已经有这个网址（多半来自别的浏览器）
	Fresh     int                 `json:"fresh"`    // 这次真正会新增的条数 = Valid - Existing
	Folders   int                 `json:"folders"`
	Sample    []ImportPreviewItem `json:"sample"`
}

// ImportPreviewItem 预览里的一条。
type ImportPreviewItem struct {
	Title  string `json:"title"`
	URL    string `json:"url"`
	Folder string `json:"folder"`
}

// PreviewImport 解析并统计，不写库。
func (s *BookmarkService) PreviewImport(path string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	entries, scene, err := readBookmarkFile(path)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(buildPreview(path, "", scene, entries, s.existingURLs()))
}

// existingURLs 库里已有的网页地址集合。查失败返回空表：预览只是提示，
// 不能因为一次查询失败就让整个导入入口打不开（真正判重发生在落库时）。
func (s *BookmarkService) existingURLs() map[string]bool {
	if s.App == nil || s.App.DB == nil {
		return nil
	}
	set, err := s.App.DB.WebURLSet()
	if err != nil {
		logger.W("QuickDock: 读取已有书签地址失败: %v", err)
		return nil
	}
	return set
}

// buildPreview 统计条目并取样，预览与导入共用一套口径。
//
// 判重放在这里而不是只在落库时做：预览要让用户看到「可导入多少条」，
// 如果按 1000 条算，导完却只多 600 条，用户会以为丢数据了。
// 再加一层「已存在」：同一批书签往往分散在多个浏览器里，第二个浏览器导入时
// 大部分会被判重跳过，不提前说明的话用户只会看到「导了没反应」。
func buildPreview(path, source, scene string, entries []ibk.Entry, existing map[string]bool) ImportPreview {
	out := ImportPreview{Path: path, Source: source, SceneName: scene, Total: len(entries)}
	folders := map[string]bool{}
	seen := map[string]bool{}
	for _, e := range entries {
		if !ibk.IsWebURL(e.URL) {
			out.Invalid++
			continue
		}
		key := ibk.NormalizeURL(e.URL)
		if key == "" || seen[key] {
			out.Invalid++
			continue
		}
		seen[key] = true
		if existing[key] {
			out.Existing++
			continue
		}
		out.Valid++
		if len(e.Folder) > 0 {
			folders[strings.Join(e.Folder, " / ")] = true
		}
		if len(out.Sample) < previewSampleSize {
			out.Sample = append(out.Sample, ImportPreviewItem{
				Title:  displayTitle(e),
				URL:    e.URL,
				Folder: strings.Join(e.Folder, " / "),
			})
		}
	}
	out.Fresh = out.Valid
	out.Folders = len(folders)
	return out
}

// ImportBookmarks 解析并写入。sceneName 为空时按文件名推断浏览器名。
func (s *BookmarkService) ImportBookmarks(path, sceneName string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	entries, guessed, err := readBookmarkFile(path)
	if err != nil {
		return services.Fail(err)
	}
	if strings.TrimSpace(sceneName) == "" {
		sceneName = guessed
	}
	return s.write(entries, sceneName, path)
}

// write 把条目落库并统一日志口径。
func (s *BookmarkService) write(entries []ibk.Entry, sceneName, origin string) *services.ApiResult {
	items := make([]db.BookmarkItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, db.BookmarkItem{
			Title:  e.Title,
			URL:    e.URL,
			Remark: e.Desc,
			Folder: e.Folder,
		})
	}

	res, err := s.App.DB.ImportBookmarks(sceneName, items)
	if err != nil {
		logger.E("QuickDock: 书签导入失败 来源=%s: %v", origin, err)
		return services.Fail(err)
	}
	logger.I("QuickDock: 书签导入完成 场景=%s 来源=%s 写入=%d 已存在=%d 文件内重复=%d 无效=%d",
		res.SceneName, origin, res.Created, res.Skipped, res.Duplicated, res.Invalid)
	return services.Ok(res)
}

// ListBookmarks 列出全部书签条目，keyword 为空表示不筛。
func (s *BookmarkService) ListBookmarks(keyword string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	rows, err := s.App.DB.ListBookmarks(keyword, 0)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(rows)
}

// readBookmarkFile 读文件 → 解析 → 推断场景名。
func readBookmarkFile(path string) ([]ibk.Entry, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if info.IsDir() {
		return nil, "", errIsDir
	}
	if info.Size() > previewMaxBytes {
		return nil, "", errTooLarge
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	content := string(raw)
	entries, err := ibk.Parse(content)
	if err != nil {
		return nil, "", err
	}
	return entries, guessBrowser(path, content), nil
}

type plainErr string

func (e plainErr) Error() string { return string(e) }

const (
	errIsDir    = plainErr("选中的是文件夹，请选择浏览器导出的 HTML 书签文件")
	errTooLarge = plainErr("文件超过 64 MB，不像是浏览器书签导出")
)

// guessBrowser 按文件名 + 内容推断浏览器，作为场景默认名。
//
// Chrome 与 Edge 的导出文件名格式完全一样（bookmarks_MM_DD_YY.html），只能靠内容区分：
// Edge 的书签里常见 edge:// 链接，Chrome 的是 chrome://。都认不出时叫「浏览器」，
// 用户可以在导入对话框里改名。
func guessBrowser(path, content string) string {
	base := strings.ToLower(filepath.Base(path))
	lower := strings.ToLower(content)

	switch {
	case strings.Contains(base, "safari"):
		return "Safari"
	case strings.Contains(base, "firefox"):
		return "Firefox"
	}
	if strings.Contains(lower, "edge://") {
		return "Edge"
	}
	if strings.Contains(lower, "chrome://") {
		return "Chrome"
	}
	if strings.HasPrefix(base, "bookmarks_") || base == "bookmarks.html" {
		return "Chrome"
	}
	return "浏览器"
}

// displayTitle 标题为空时用域名兜底，与落库口径保持一致（预览要能看出实际效果）。
func displayTitle(e ibk.Entry) string {
	if t := strings.TrimSpace(e.Title); t != "" {
		return t
	}
	return ibk.HostOf(e.URL)
}
