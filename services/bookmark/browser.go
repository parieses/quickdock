// 直接读本机浏览器书签（不必先导出 HTML）的绑定入口。
//
// 与 HTML 导入共用同一条落库路径：解析 → Entry → db.ImportBookmarks，
// 因此两种入口得到的条目完全一样（同样判重、同样映射文件夹、同样进「书签」工作空间）。
package bookmark

import (
	"strings"

	ibk "quickdock/internal/bookmark"
	"quickdock/services"
)

// BrowserSourceInfo 前端展示用的来源信息。
//
// 不回传 Kind：前端只需要「名字 + 配置 + 路径」三段文字和一个 Key 回传，
// 解析器选择留在后端，避免前端也维护一份 chromium / firefox 的判断。
type BrowserSourceInfo struct {
	Key     string `json:"key"`
	Browser string `json:"browser"`
	Profile string `json:"profile"`
	Path    string `json:"path"`
	// Count 这个来源里能读出的书签条数，-1 表示读取失败。
	//
	// 弹窗一打开就把条数摆出来，用户不必点进去才知道「这台机器上到底有没有我要的浏览器」。
	// 之前只列路径，扫不到 Chrome 时用户无法判断是没扫到、还是扫到了但内容为空。
	Count int `json:"count"`
}

// ListBrowserSources 扫描本机可直读的书签来源。
//
// 空列表不是错误：多数用户可能一个浏览器都没装，或装在非标准位置。
// 前端据此提示「改用 HTML 文件导入」，而不是弹一个红字错误。
func (s *BookmarkService) ListBrowserSources() *services.ApiResult {
	srcs := ibk.DiscoverSources()
	out := make([]BrowserSourceInfo, 0, len(srcs))
	for _, src := range srcs {
		// 顺手数一遍：几百 KB 的 JSON 解析十几毫秒，换来「点开前就知道有多少条」，
		// 比让用户逐个点进去试要省事。读取失败只标记 -1，不影响其他来源。
		count := -1
		if entries, err := ibk.ReadSource(src); err == nil {
			count = len(entries)
		}
		out = append(out, BrowserSourceInfo{
			Key:     src.Key,
			Browser: src.Browser,
			Profile: src.Profile,
			Path:    src.Path,
			Count:   count,
		})
	}
	return services.Ok(out)
}

// PreviewBrowserImport 解析某个来源但不写库。
func (s *BookmarkService) PreviewBrowserImport(key string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	src, entries, err := loadSource(key)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(buildPreview(src.Path, sourceLabel(src), src.Browser, entries, s.existingURLs()))
}

// ImportBrowserBookmarks 直接导入某个来源。sceneName 为空时用浏览器名。
func (s *BookmarkService) ImportBrowserBookmarks(key, sceneName string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	src, entries, err := loadSource(key)
	if err != nil {
		return services.Fail(err)
	}
	if strings.TrimSpace(sceneName) == "" {
		sceneName = src.Browser
	}
	return s.write(entries, sceneName, sourceLabel(src))
}

// loadSource 按 Key 重新定位来源并读出条目。
func loadSource(key string) (ibk.BrowserSource, []ibk.Entry, error) {
	src, ok := ibk.FindSource(strings.TrimSpace(key))
	if !ok {
		return ibk.BrowserSource{}, nil, errSourceGone
	}
	entries, err := ibk.ReadSource(src)
	if err != nil {
		return src, nil, err
	}
	return src, entries, nil
}

// sourceLabel 展示用来源名：浏览器 + 配置名（只有一个配置时 Chrome · Default 也很必要，
// 因为它同时出现在预览标题里，用户靠它确认「我导的是哪个配置」）。
func sourceLabel(src ibk.BrowserSource) string {
	if strings.TrimSpace(src.Profile) == "" {
		return src.Browser
	}
	return src.Browser + " · " + src.Profile
}

const errSourceGone = plainErr("这个浏览器书签来源已失效（浏览器被卸载或配置被删除），请关闭后重新打开导入窗口")
