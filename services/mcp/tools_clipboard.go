package mcp

// 剪贴板域工具：读最近历史、写文本。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

func registerClipboardTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.add(func(args map[string]any) (any, error) {
		if s.Clip == nil {
			return nil, errors.New("剪贴板服务未就绪")
		}
		return s.Clip.ListClipboardEntries(mcpsrv.ArgInt(args, "limit", 20)), nil
	}, mcpsrv.LevelRead, "clipboard_recent", "列出最近的剪贴板历史（文本条目）", nil,
		map[string]any{"limit": intp("返回条数，默认 20")})

	r.add(func(args map[string]any) (any, error) {
		if s.Clip == nil {
			return nil, errors.New("剪贴板服务未就绪")
		}
		return unwrap(s.Clip.SearchClipboardEntries(
			mcpsrv.ArgInt(args, "limit", 50),
			mcpsrv.Arg(args, "query"),
			mcpsrv.Arg(args, "contentType"),
			mcpsrv.Arg(args, "smartType")))
	}, mcpsrv.LevelRead, "clipboard_search", "全库搜索剪贴板历史（关键词 + 可选类型）。clipboard_recent 只看最近 N 条，找旧内容要用这个", nil,
		map[string]any{
			"query":       str("搜索关键词，留空则只按类型过滤"),
			"limit":       intp("最多返回条数，默认 50"),
			"contentType": str("内容类型过滤（text/image/file 等），留空不过滤"),
			"smartType":   str("智能分类过滤（如 url/code），留空不过滤"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Clip == nil {
			return nil, errors.New("剪贴板服务未就绪")
		}
		text := mcpsrv.Arg(args, "text")
		if text == "" {
			return nil, errors.New("缺少参数 text")
		}
		s.Clip.CopyText(text)
		return "已写入剪贴板", nil
	}, mcpsrv.LevelWrite, "clipboard_copy", "把文本写入系统剪贴板", []string{"text"},
		map[string]any{"text": str("要写入的文本")})
}
