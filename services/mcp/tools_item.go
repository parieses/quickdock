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

	// 以下四条补齐「工作空间里到底有什么」的结构化列举：此前只有模糊搜索
	// item_search，AI 想按 场景→集合→项目 的层级浏览时只能靠猜 id。
	r.add(func(args map[string]any) (any, error) {
		if s.Workspace == nil {
			return nil, errors.New("工作空间服务未就绪")
		}
		return unwrap(s.Workspace.ListWorkspaces())
	}, mcpsrv.LevelRead, "workspace_list_all", "列出所有工作空间（含 id、名称）。scene_list / collection_list 要用这里的 workspaceId 作过滤", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if s.Scene == nil {
			return nil, errors.New("场景服务未就绪")
		}
		return unwrap(s.Scene.ListScenes(mcpsrv.Arg(args, "workspaceId")))
	}, mcpsrv.LevelRead, "scene_list", "列出场景（可按 workspaceId 过滤）。用户问「我有哪些场景」时用", nil,
		map[string]any{"workspaceId": str("工作空间 id，来自 workspace_list_all；留空返回全部")})

	r.add(func(args map[string]any) (any, error) {
		if s.Collection == nil {
			return nil, errors.New("集合服务未就绪")
		}
		return unwrap(s.Collection.ListCollections(mcpsrv.Arg(args, "sceneId")))
	}, mcpsrv.LevelRead, "collection_list", "列出集合（可按 sceneId 过滤）。用户问「某个场景下有哪些集合」时用", nil,
		map[string]any{"sceneId": str("场景 id，来自 scene_list；留空返回全部")})

	r.add(func(args map[string]any) (any, error) {
		if s.Item == nil {
			return nil, errors.New("快捷项服务未就绪")
		}
		return unwrap(s.Item.ListItems(mcpsrv.Arg(args, "collectionId")))
	}, mcpsrv.LevelRead, "item_list", "列出某集合下的全部快捷项（项目/命令/目录/网址）。要打开某一项先用这个拿到 id", nil,
		map[string]any{"collectionId": str("集合 id，来自 collection_list；留空返回全部")})

	r.add(func(args map[string]any) (any, error) {
		if s.Shelf == nil {
			return nil, errors.New("书架服务未就绪")
		}
		return unwrap(s.Shelf.List())
	}, mcpsrv.LevelRead, "shelf_list", "列出书架里的常用路径（总览页的那组分块）。用户问「我常去的目录有哪些」时用", nil, nil)

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
				if r := s.Collection.OpenItem(it.ID); r != nil && r.Code != 0 {
					return nil, errors.New(r.Msg)
				}
				return "已打开 " + it.Name, nil
			}
		}
		return nil, errors.New("未找到该快捷项: " + id)
	}, mcpsrv.LevelWrite, "item_open", "打开某个快捷项（项目/命令/目录），先用 item_search 找到 id", []string{"id"},
		map[string]any{"id": str("快捷项 id")})
}
