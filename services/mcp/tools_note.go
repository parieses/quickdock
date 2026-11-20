package mcp

// 笔记域工具：搜索、建文档/建文件夹、改正文/标签、写快捷笔记。
//
// 「让 AI 把产出塞回 QuickDock」是这组工具的核心用途，故写侧比读侧更全；
// note_create 与 note_folder_create 的描述必须互相点明边界——模型分不清
// 「文档」和「文件夹」时会反复建错，把提示写进描述比事后补救有效。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

func registerNoteTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str

	// ---- 只读 ----

	// 只回精简项：旧实现把整篇 content 塞进结果，一篇几千字笔记就能吃掉
	// 大半上下文。模型要全文时再用 note_read 按 id 精确取。
	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Note.SearchNotesBrief(
			mcpsrv.Arg(args, "query"), mcpsrv.ArgInt(args, "limit", 30)))
	}, mcpsrv.LevelRead, "note_search", "搜索笔记（标题/正文/标签）。返回精简列表，每条只带 160 字命中片段，不含整篇正文；需要全文请用 note_read", []string{},
		map[string]any{
			"query": str("搜索关键词；留空返回全部笔记的前 N 条"),
			"limit": mcpsrv.Int("最多返回条数，默认 30，上限 100"),
		})

	r.add(func(args map[string]any) (any, error) {
		id, err := requireNoteID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Note.GetNoteDoc(id))
	}, mcpsrv.LevelRead, "note_read", "按 id 读取一篇笔记的完整内容（含正文、标签、修改时间）", []string{"id"},
		map[string]any{"id": str("笔记 id（用 note_search 获取）")})

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Note.ListNoteTags())
	}, mcpsrv.LevelRead, "note_tags", "列出笔记库里已用过的全部标签", []string{}, map[string]any{})

	r.add(func(args map[string]any) (any, error) {
		id, err := requireNoteID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Note.ListNoteRevisions(id, mcpsrv.ArgInt(args, "limit", 20)))
	}, mcpsrv.LevelRead, "note_revisions", "列出某篇笔记的历史版本（自动保存点/导入/回滚），新的在前", []string{"id"},
		map[string]any{
			"id":    str("笔记 id"),
			"limit": mcpsrv.Int("最多返回条数，默认 20"),
		})

	// note_quick 早先只能写不能读：AI 塞进去一份草稿后想核对，只能去搜笔记列表碰运气。
	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Note.GetNote())
	}, mcpsrv.LevelRead, "note_quick_read", "读取「快捷笔记」当前内容（note_quick 写入的那篇）。核对上次写进去的草稿时用", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		id, err := requireNoteID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Note.GetNoteRevision(id))
	}, mcpsrv.LevelRead, "note_revision_read", "读取某个历史版本的正文（note_revisions 只列不读）。回滚前先看清楚那一版写了什么", []string{"id"},
		map[string]any{"id": str("历史版本 id，来自 note_revisions")})

	// ---- 低危写 ----

	r.add(func(args map[string]any) (any, error) {
		name := mcpsrv.Arg(args, "name")
		if name == "" {
			// 兼容模型常用 title：不兼容的话会白跑一次工具调用（note_create 必填 name）
			name = mcpsrv.Arg(args, "title")
		}
		if name == "" {
			return nil, errors.New("缺少参数 name（笔记标题，也可用 title）")
		}
		return unwrap(s.Note.CreateNoteDoc(
			mcpsrv.Arg(args, "parentId"),
			name,
			mcpsrv.Arg(args, "content"),
			mcpsrv.Arg(args, "format"),
		))
	}, mcpsrv.LevelWrite, "note_create", "新建一篇笔记文档（markdown/text）。parentId 留空则创建到根目录。注意：要新建的是「文件夹」请用 note_folder_create，不要用本工具", []string{"name"},
		map[string]any{
			"name":     str("笔记标题"),
			"title":    str("笔记标题的别名（name 为空时使用）"),
			"content":  str("正文内容，可留空"),
			"format":   str("渲染格式：markdown 或 text，默认 markdown"),
			"parentId": str("父目录 id，留空则放到根目录"),
		})

	r.add(func(args map[string]any) (any, error) {
		name := mcpsrv.Arg(args, "name")
		if name == "" {
			name = mcpsrv.Arg(args, "title")
		}
		if name == "" {
			return nil, errors.New("缺少参数 name（文件夹名称，也可用 title）")
		}
		return unwrap(s.Note.CreateNoteFolder(mcpsrv.Arg(args, "parentId"), name))
	}, mcpsrv.LevelWrite, "note_folder_create", "新建笔记文件夹（可嵌套）。parentId 留空则建到根目录；返回的 id 可作为 note_create 的 parentId 继续建子项", []string{"name"},
		map[string]any{
			"name":     str("文件夹名称"),
			"title":    str("文件夹名称的别名（name 为空时使用）"),
			"parentId": str("父文件夹 id，留空则建到根目录"),
		})

	r.add(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 note_search 获取）")
		}
		content, tags := mcpsrv.Arg(args, "content"), mcpsrv.Arg(args, "tags")
		// 分三路：只传一个字段时不要覆盖另一个。此前空 tags 会清空标签、空 content 会清空正文，
		// 模型「只改一半」就会造成静默数据丢失。
		switch {
		case content == "" && tags == "":
			return nil, errors.New("content 与 tags 至少提供一个（需要清空正文请显式传 content 为空串的场景请改用 note_create 覆盖）")
		case content == "":
			return unwrap(s.Note.UpdateNoteDocTags(id, tags))
		case tags == "":
			return unwrap(s.Note.UpdateNoteDocContent(id, content))
		default:
			return unwrap(s.Note.UpdateNoteDoc(id, content, tags))
		}
	}, mcpsrv.LevelWrite, "note_update", "更新笔记正文与标签：只传 content 改正文（保留标签），只传 tags 改标签（保留正文），两个都传则一起覆盖。tags 为 JSON 数组字符串，如 [\"a\",\"b\"]；传 [] 清空标签", []string{"id"},
		map[string]any{
			"id":      str("笔记 id（用 note_search 获取）"),
			"content": str("新的正文内容；不传则保留原正文"),
			"tags":    str("标签 JSON 数组字符串；不传则保留原标签，传 [] 清空"),
		})

	r.add(func(args map[string]any) (any, error) {
		content := mcpsrv.Arg(args, "content")
		if content == "" {
			return nil, errors.New("缺少参数 content")
		}
		return unwrap(s.Note.SaveNote(content))
	}, mcpsrv.LevelWrite, "note_quick", "把一段文本写入固定「快捷笔记」（覆盖式 upsert）。适合让 AI 把产出直接塞回 QuickDock 当草稿", []string{"content"},
		map[string]any{"content": str("要保存的文本内容")})

	r.add(func(args map[string]any) (any, error) {
		id, err := requireNoteID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Note.MoveNoteNode(id, mcpsrv.Arg(args, "parentId")))
	}, mcpsrv.LevelWrite, "note_move", "把笔记或文件夹移到另一个文件夹下；parentId 留空表示移到根目录", []string{"id"},
		map[string]any{
			"id":       str("要移动的节点 id"),
			"parentId": str("目标文件夹 id，留空则移到根目录"),
		})

	r.add(func(args map[string]any) (any, error) {
		revID := mcpsrv.Arg(args, "revisionId")
		if revID == "" {
			return nil, errors.New("缺少参数 revisionId（用 note_revisions 获取）")
		}
		return unwrap(s.Note.RestoreNoteRevision(revID))
		// 等级说明：环境页只有「只读 / 只读+低危写」两档，LevelRisk 恒不注册，
		// 标成高危等于这个工具不存在。回滚虽覆盖当前正文，但 RestoreNoteRevision
		// 会先把当前内容另存一个 rollback 修订点，随时能再滚回来 —— 可逆操作
		// 与「建待办 / 启停服务」同档，放 LevelWrite 才真的可用。
	}, mcpsrv.LevelWrite, "note_restore", "把某条历史版本回滚成笔记当前内容。会覆盖现有正文，但旧内容会自动另存一个回滚点，可再滚回来", []string{"revisionId"},
		map[string]any{"revisionId": str("历史版本 id（用 note_revisions 获取）")})

	r.add(func(args map[string]any) (any, error) {
		id, err := requireNoteID(args)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Note.DeleteNoteNode(id))
		// 同 note_restore：不标 LevelRisk（默认恒不注册）。删除会写墓碑、子树
		// 整份进回收站可一键恢复，属于可逆操作；描述里把连带删除讲清楚，
		// 免得模型以为只是删一行。
	}, mcpsrv.LevelWrite, "note_delete", "删除笔记或文件夹（文件夹会连带整棵子树）。删除会写墓碑，可在 QuickDock 设置→历史→回收站恢复", []string{"id"},
		map[string]any{"id": str("要删除的节点 id")})
}

// requireNoteID 统一取 id 并给出可执行提示：模型拿到「缺少 id」时
// 至少知道下一步该调哪个工具，而不是再盲试一次。
func requireNoteID(args map[string]any) (string, error) {
	id := mcpsrv.Arg(args, "id")
	if id == "" {
		return "", errors.New("缺少参数 id（用 note_search 获取）")
	}
	return id, nil
}
