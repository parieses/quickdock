package db

// 笔记批量导入 / 导出。
//
// 为什么用「带相对路径的扁平列表」而不是嵌套 JSON 树：
// 开发者最常见的迁移来源是 Obsidian / 一堆 .md 文件，目录层级就是 path。
// 扁平列表既能无损表达树，也能直接被别的工具消费；导入时按 / 逐级
// find-or-create 文件夹，同一份格式导出再导回来即可复原结构。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// NoteExport 一条导出项。Path 是用 / 分隔的相对路径，文档带 .md 后缀。
type NoteExport struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Tags    string `json:"tags"`
	Format  string `json:"format"`
}

// ExportNoteBatch 导出全部笔记文档（不含文件夹节点本身，层级体现在 Path）。
func (d *Database) ExportNoteBatch() ([]NoteExport, error) {
	nodes, err := d.ListNotesTree()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Note, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	var pathOf func(n Note) string
	pathOf = func(n Note) string {
		// 层级不深，但历史脏数据可能把 parent_id 指成一个环，
		// 用深度上限兜住，别让一次导出把 UI 线程卡死。
		name := n.Name
		if name == "" {
			name = n.Keyword
		}
		cur, depth := n, 0
		parts := []string{name}
		for cur.ParentID != "" && depth < 32 {
			p, ok := byID[cur.ParentID]
			if !ok || p.ID == cur.ID {
				break
			}
			pn := p.Name
			if pn == "" {
				pn = p.Keyword
			}
			parts = append([]string{pn}, parts...)
			cur, depth = p, depth+1
		}
		return strings.Join(parts, "/")
	}

	out := make([]NoteExport, 0, len(nodes))
	for _, n := range nodes {
		if n.IsFolder || n.Keyword == NoteQuickKeyword {
			continue
		}
		path := pathOf(n)
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			path += ".md"
		}
		out = append(out, NoteExport{
			Path:    path,
			Content: n.Content,
			Tags:    n.Tags,
			Format:  n.Format,
		})
	}
	return out, nil
}

// ImportNoteBatch 导入一批笔记，返回新建的文档数。
//
// payload 有两种形态：
//  1. NoteExport 数组的 JSON（本工具导出的备份）；
//  2. 纯 Markdown 文本（前端选中一个 .md 文件时直接把内容传进来，
//     用 fallbackName 当标题）。
//
// 靠首个非空白字符是否是 '[' 来分流，比先 Unmarshal 再猜更省一次大字符串拷贝。
// ImportNoteBatch 的返回值用命名参数，是为了让 defer 里的索引重建能把错误带出去。
// importNoteTitle 从单篇导入的 fallbackName 里取出笔记标题。
//
// 调用方可能直接传完整路径（拖一个 .md 进来时前端常带上打开对话框的路径），
// 直接 TrimSuffix(".md") 会把目录层级留在标题里，而标题同时是 notes.name
// 与 FTS 索引列，标题带路径会让搜索与显示一起变脏。所以先取最后一段，
// 再大小写不敏感地剥掉 .md / .markdown（Windows 上常见 .MD 大写）。
func importNoteTitle(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	lower := strings.ToLower(name)
	for _, ext := range []string{".markdown", ".md"} {
		if strings.HasSuffix(lower, ext) {
			return strings.TrimSpace(name[:len(name)-len(ext)])
		}
	}
	return name
}

func (d *Database) ImportNoteBatch(payload, parentID, fallbackName string) (created int, err error) {
	if strings.TrimSpace(payload) == "" {
		return 0, fmt.Errorf("导入内容为空")
	}
	var items []NoteExport
	if strings.HasPrefix(strings.TrimSpace(payload), "[") {
		if err := json.Unmarshal([]byte(payload), &items); err != nil {
			return 0, fmt.Errorf("导入文件格式不是有效的笔记 JSON: %w", err)
		}
	} else {
		name := importNoteTitle(fallbackName)
		if name == "" {
			name = "导入的笔记"
		}
		items = []NoteExport{{Path: name + ".md", Content: payload, Format: "markdown", Tags: "[]"}}
	}
	if len(items) == 0 {
		return 0, fmt.Errorf("导入文件里没有笔记")
	}
	if len(items) > 2000 {
		return 0, fmt.Errorf("一次最多导入 2000 篇，当前 %d 篇", len(items))
	}

	// 正文里的 [[...]] 是派生数据的真源，但 createNoteDocLocked 走 *Locked 变体
	// 刻意不碰 wiki 出边（service 层的 syncWikiLinks 也进不来）。攒下来整批结束
	// 再统一重建：那时所有笔记都已落库，跨文档的 [[标题]] 也能解析到 id。
	// 声明在 defer 之前，让中途失败的部分导入也能把已写入笔记的出边补齐。
	imported := make([]struct{ id, content string }, 0, len(items))

	// 整批只持一次写锁：内部全走 *Locked 变体（不自行加锁、不逐篇重建索引），
	// 末尾统一重建一次。否则 2000 篇导入会触发上千次全表重建（O(N²)），而且
	// 旧写法里 findOrCreateNoteFolder 带 RLock、CreateNoteDoc 带 Lock，
	// 与这里的外层写锁会 RWMutex 重入死锁。
	// 用 defer 而非在末尾直接重建：中途 return 时可能已经写进去一部分行，
	// 索引也必须跟着重建，保持与正文一致。重建放在 Unlock 之前，仍持锁。
	d.mu.Lock()
	defer func() {
		if rerr := d.rebuildNoteIndex(); rerr != nil && err == nil {
			err = rerr
		}
		// 中途 return 时可能已经写进去一部分行，这些笔记的 wiki 出边也要补齐；
		// 仍在同一把写锁内、直接走 d.conn，不调带锁的 SyncNoteWikiLinks（会重入死锁）。
		// 派生数据失败只记日志，不覆盖真正的导入错误。
		for _, n := range imported {
			if e := rebuildNoteWikiLinks(d.conn, n.id, n.content); e != nil {
				logger.W("[note] 批量导入重建 wiki 出边失败 id=%s: %v", n.id, e)
			}
		}
		d.mu.Unlock()
	}()

	// 同一次导入里复用已建的文件夹，避免 10 篇同目录笔记建出 10 个同名文件夹。
	folders := map[string]string{"": parentID}
	for _, item := range items {
		path := strings.TrimSpace(strings.ReplaceAll(item.Path, "\\", "/"))
		if path == "" {
			path = "未命名笔记.md"
		}
		segs := strings.Split(path, "/")
		for i := range segs {
			segs[i] = strings.TrimSpace(segs[i])
		}
		parent := parentID
		key := ""
		for _, dir := range segs[:len(segs)-1] {
			if dir == "" || dir == "." || dir == ".." {
				continue // 挡掉 ../ 之类的路径穿越，笔记路径不该能逃出树
			}
			key += dir + "/"
			if id, ok := folders[key]; ok {
				parent = id
				continue
			}
			id, e := d.findOrCreateNoteFolderLocked(parent, dir)
			if e != nil {
				return created, e
			}
			folders[key] = id
			parent = id
		}
		name := strings.TrimSuffix(segs[len(segs)-1], ".md")
		if name == "" {
			name = "未命名笔记"
		}
		format := item.Format
		if format != "text" {
			format = "markdown"
		}
		tags := item.Tags
		if tags == "" {
			tags = "[]"
		}
		n, e := d.createNoteDocLocked(parent, name, item.Content, format)
		if e != nil {
			return created, e
		}
		imported = append(imported, struct{ id, content string }{n.ID, item.Content})
		// createNoteDocLocked 把 tags 固定成 []，这里补写导入文件里带的标签。
		// 用 UpdateNoteTags 会额外留一条修订，导入本身就是一次明确的离散动作，
		// 留痕合理；但为了不让每篇导入都产生「空标签」垃圾修订，只在真有标签时写。
		if tags != "[]" {
			if e := d.updateNoteTagsByNameLocked(parent, name, tags); e != nil {
				return created, e
			}
		}
		created++
	}
	return created, nil
}

// findOrCreateNoteFolderLocked 按 (parent, name) 复用已有文件夹，没有才建
// （调用方需持 d.mu；不自行加锁、不重建索引，专供 ImportNoteBatch 批量写）。
func (d *Database) findOrCreateNoteFolderLocked(parentID, name string) (string, error) {
	var id string
	err := d.conn.QueryRow(`SELECT id FROM notes
		WHERE parent_id = ? AND name = ? AND is_folder = 1 ORDER BY sort ASC LIMIT 1`, parentID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err // 真故障别靠「再建一个文件夹」掩盖
	}
	n, err := d.createNoteFolderLocked(parentID, name)
	if err != nil {
		return "", err
	}
	return n.ID, nil
}

// updateNoteTagsByNameLocked 补标签（调用方需持 d.mu；不自行加锁、不重建索引）。
func (d *Database) updateNoteTagsByNameLocked(parentID, name, tags string) error {
	_, err := d.conn.Exec(`UPDATE notes SET tags = ?, updated_at = ?
		WHERE id = (SELECT id FROM notes WHERE parent_id = ? AND name = ? AND is_folder = 0
		            ORDER BY rowid DESC LIMIT 1)`,
		tags, time.Now().Format(time.RFC3339), parentID, name)
	return err
}

// UpdateNoteTagsByName 给刚导入/剪藏的文档补标签（按 parent+name 取最后一条）。
// 网页剪藏 ClipNote（services/note）会直接调它，所以保留公开的加锁版 + 同步索引；
// 批量导入走上面的 *Locked 变体，末尾统一刷一次。
func (d *Database) UpdateNoteTagsByName(parentID, name, tags string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.updateNoteTagsByNameLocked(parentID, name, tags); err != nil {
		return err
	}
	// 只重刷这一行：整表重建在这个入口上纯粹是浪费（剪藏一篇就 O(N) 重 tokenize）。
	return d.syncNoteIndexWhere(`id = (SELECT id FROM notes WHERE parent_id = ? AND name = ? AND is_folder = 0
		ORDER BY rowid DESC LIMIT 1)`, parentID, name)
}
