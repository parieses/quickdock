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
		text := mcpsrv.Arg(args, "text")
		if text == "" {
			return nil, errors.New("缺少参数 text")
		}
		s.Clip.CopyText(text)
		return "已写入剪贴板", nil
	}, mcpsrv.LevelWrite, "clipboard_copy", "把文本写入系统剪贴板", []string{"text"},
		map[string]any{"text": str("要写入的文本")})
}
