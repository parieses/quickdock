package note

// 笔记模块的扩展门面：修订历史、标签聚合、批量导入导出、剪藏、精简检索。
// 都是「校验参数 → 调 internal/db → 包 ApiResult」的薄层，业务规则留在 db 包。

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/services"
)

// NoteBrief 搜索/最近列表用的精简项。
//
// 为什么不带 content：MCP 的 note_search 早先把整篇正文回给模型，
// 一篇几千字的笔记就能吃掉大半上下文。列表只给 snippet，模型真要全文
// 再用 note_read 按 id 取， token 花在刀刃上。
type NoteBrief struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ParentID  string `json:"parentId"`
	IsFolder  bool   `json:"isFolder"`
	Tags      string `json:"tags"`
	UpdatedAt string `json:"updatedAt"`
	Snippet   string `json:"snippet"`
}

// GetNoteDoc 读单篇笔记全文（含正文）。MCP note_read 与前端深链都用它。
func (s *NoteService) GetNoteDoc(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.FailMsg("笔记 id 不能为空")
	}
	n, err := s.App.DB.GetNoteNode(id)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(n)
}

// ListNoteRevisions 列出某篇笔记的历史版本（新在前）。
func (s *NoteService) ListNoteRevisions(id string, limit int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.Ok([]db.NoteRevision{})
	}
	list, err := s.App.DB.ListNoteRevisions(id, limit)
	return services.Wrap(list, err)
}

// GetNoteRevision 读单条历史版本全文，供前端做预览/对比。
func (s *NoteService) GetNoteRevision(revId string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	rev, err := s.App.DB.GetNoteRevision(revId)
	return services.Wrap(rev, err)
}

// RestoreNoteRevision 把某条历史版本写回笔记。
// 回滚后正文变了，wiki 出边必须跟着重建，否则图谱会停在回滚前的状态。
func (s *NoteService) RestoreNoteRevision(revId string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	n, err := s.App.DB.ApplyNoteRevision(revId)
	if err != nil {
		return services.Fail(err)
	}
	s.syncWikiLinks(n.ID, n.Content)
	return services.Ok(n)
}

// ListNoteTags 后端聚合全部标签，前端不必再拉整棵树逐条解析 JSON。
func (s *NoteService) ListNoteTags() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListNoteTags()
	return services.Wrap(list, err)
}

// RecentNotes 按修改时间倒序列出最近编辑的文档。
func (s *NoteService) RecentNotes(limit int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	list, err := s.App.DB.ListNotesTree()
	if err != nil {
		return services.Fail(err)
	}
	docs := make([]db.Note, 0, len(list))
	for _, n := range list {
		if !n.IsFolder {
			docs = append(docs, n)
		}
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].UpdatedAt > docs[j].UpdatedAt })
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return services.Ok(docs)
}

// ExportNotes 导出全树为带相对路径的扁平列表，前端序列化成 JSON 下载。
func (s *NoteService) ExportNotes() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ExportNoteBatch()
	return services.Wrap(list, err)
}

// ImportNotes 批量导入。payload 为本工具导出的 JSON 数组或单篇 Markdown 文本；
// parentId 为空表示导到根目录，fileName 只在单篇文本时用作标题。
func (s *NoteService) ImportNotes(payload, parentId, fileName string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	n, err := s.App.DB.ImportNoteBatch(payload, parentId, fileName)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]interface{}{"created": n})
}

// ClipNote 网页剪藏入口：浏览器扩展 POST /vault/clip-note 经宿主调到这里。
// 正文里带上来源 URL，方便日后回溯；标题为空时给一个可辨识的默认值。
func (s *NoteService) ClipNote(title, content, url, tags string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "网页剪藏 " + time.Now().Format("2006-01-02 15:04")
	}
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	body := strings.TrimSpace(content)
	if url != "" {
		body = fmt.Sprintf("> 来源：<%s>\n\n%s", url, body)
	}
	n, err := s.App.DB.CreateNoteDoc("", title, body, "markdown")
	if err != nil {
		return services.Fail(err)
	}
	if strings.TrimSpace(tags) != "" {
		if err := s.App.DB.UpdateNoteTagsByName(n.ParentID, n.Name, tags); err != nil {
			return services.Fail(err)
		}
	}
	s.syncWikiLinks(n.ID, body)
	return services.Ok(n)
}

// SearchNotesBrief 精简检索：MCP 与命令面板用，不含整篇正文。
func (s *NoteService) SearchNotesBrief(q string, limit int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var list []db.Note
	var err error
	if strings.TrimSpace(q) == "" {
		list, err = s.App.DB.ListNotesTree()
	} else {
		list, err = s.App.DB.SearchNotes(q)
	}
	if err != nil {
		return services.Fail(err)
	}
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]NoteBrief, 0, len(list))
	for _, n := range list {
		name := n.Name
		if name == "" {
			name = n.Keyword
		}
		out = append(out, NoteBrief{
			ID:        n.ID,
			Name:      name,
			ParentID:  n.ParentID,
			IsFolder:  n.IsFolder,
			Tags:      n.Tags,
			UpdatedAt: n.UpdatedAt,
			Snippet:   db.NoteSnippet(n.Content, q, 160),
		})
	}
	return services.Ok(out)
}
