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

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Note.SearchNotesTree(mcpsrv.Arg(args, "query")))
	}, mcpsrv.LevelRead, "note_search", "搜索笔记（标题与正文）", []string{"query"},
		map[string]any{"query": str("搜索关键词")})

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
}
