package mcp

// 启动台域工具：快捷项（项目/命令/目录/网址）、最近使用、工作空间。
//
// 三者同属「用户要去哪儿干活」这一个心智模型，读侧（搜索/最近/列表）与
// 写侧（打开）放在一起，改「怎么定位一个快捷项」时只需看这一个文件。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

func registerItemTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	// ---- 只读 ----

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Workspace.ListWorkspaces())
	}, mcpsrv.LevelRead, "workspace_list", "列出所有工作空间", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		q := mcpsrv.Arg(args, "query")
		if q == "" {
			return unwrap(s.App.ListAllItems())
		}
		return unwrap(s.App.SearchAll(q))
	}, mcpsrv.LevelRead, "item_search", "按名称/路径关键词搜索快捷项（项目、命令、目录、网址等），留空返回全部", nil,
		map[string]any{"query": str("关键词，留空返回全部")})

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Frecency.GetRecentUsage(mcpsrv.ArgInt(args, "limit", 10)))
	}, mcpsrv.LevelRead, "recent_items", "列出最近使用过的快捷项", nil,
		map[string]any{"limit": intp("返回条数，默认 10")})

	// ---- 低危写 ----

	r.add(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 item_search 获取）")
		}
		items, err := s.allItems()
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.ID == id {
				if r := s.Collection.OpenItem(it); r != nil && r.Code != 0 {
					return nil, errors.New(r.Msg)
				}
				return "已打开 " + it.Name, nil
			}
		}
		return nil, errors.New("未找到该快捷项: " + id)
	}, mcpsrv.LevelWrite, "item_open", "打开某个快捷项（项目/命令/目录），先用 item_search 找到 id", []string{"id"},
		map[string]any{"id": str("快捷项 id")})
}
