package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"quickdock/internal/bookmark"
	"quickdock/internal/logger"
)

// 浏览器书签导入的落库实现。
//
// 映射关系（不新开表，书签就是「网页」类型项目）：
//
//	工作空间  默认（也是唯一）工作空间，不再单独开一个「书签」空间
//	场景      一次导入一个浏览器 → 一个场景（同名的复用，重复导入是合并而不是新建），
//	          场景类型固定为「书签」，侧栏一眼能和用户自己的项目场景区分开
//	集合      书签文件夹的末级名字；上级路径写进集合的 group_name（侧栏按组折叠）
//	项目      type = "网页"，value = 原始 URL，remark = <DD> 描述
//
// 这样导入完立刻能享受现成能力：资源树、命令面板搜索、AI 读取、MCP 工具、
// 死链检查与查重也只需扫 items 里的网页条目，不必再写一套查询。

// BookmarkItem 一条待导入书签（service 层已做完格式清洗）。
type BookmarkItem struct {
	Title  string
	URL    string
	Remark string
	Folder []string
}

// BookmarkImportResult 导入结果，直接回给前端展示。
type BookmarkImportResult struct {
	WorkspaceID string `json:"workspaceId"`
	SceneID     string `json:"sceneId"`
	SceneName   string `json:"sceneName"`
	Created     int    `json:"created"`     // 新写入的条目数
	Skipped     int    `json:"skipped"`     // 库里已有（归一化 URL 命中）
	Duplicated  int    `json:"duplicated"`  // 文件内部重复
	Invalid     int    `json:"invalid"`     // 非 http/https 或 URL 为空
	Collections int    `json:"collections"` // 新建的集合数（不含复用）
}

// bookmarkWorkspaceName 旧版给书签单独开的工作空间名。
//
// 应用里已经只保留一个工作空间（默认空间），新的导入直接落进默认空间；
// 这个常量只为升级时把老库的「书签」空间搬回来（见 migrateBookmarkWorkspace）。
const bookmarkWorkspaceName = "书签"

// bookmarkSceneType 书签场景的场景类型（scenes.type）。
//
// 场景类型只有展示与图标用途，加一个「书签」是为了在侧栏把「导入来的浏览器
// 场景」和用户自己建的「项目 / 办公」场景分开，不参与任何业务判断。
const bookmarkSceneType = "书签"

// maxBookmarkNameLen 条目名上限。
//
// items.name 没有长度约束，但网页标题动辄一百多字（尤其是带站点后缀的标题），
// 列表里会把整行撑开。200 字符足够放下正常标题，超出部分截断并补省略号。
const maxBookmarkNameLen = 200

// ImportBookmarks 把一批书签写入数据库，返回统计结果。
//
// 判重口径是归一化 URL 全局比对（跨集合、跨场景），不是只在同一个文件夹内比对——
// 同一条链接被用户收进两个文件夹是常见情况，导入后应只留一条。
func (d *Database) ImportBookmarks(sceneName string, items []BookmarkItem) (*BookmarkImportResult, error) {
	res := &BookmarkImportResult{SceneName: sceneName}

	wsID, err := d.defaultWorkspaceID()
	if err != nil {
		return nil, fmt.Errorf("读取默认工作空间失败: %w", err)
	}
	sceneID, err := d.ensureSceneByName(wsID, sceneName, bookmarkSceneType)
	if err != nil {
		return nil, fmt.Errorf("准备场景失败: %w", err)
	}
	res.WorkspaceID = wsID
	res.SceneID = sceneID

	existing, err := d.existingWebURLs()
	if err != nil {
		return nil, fmt.Errorf("读取已有条目失败: %w", err)
	}

	// 集合按「组 + 名字」缓存，避免每个条目都查一次库
	type colKey struct{ group, name string }
	cols := map[colKey]string{}
	if rows, e := d.Query("SELECT id, name, group_name FROM collections WHERE scene_id = ?", sceneID); e == nil {
		for _, r := range rows {
			cols[colKey{str(r["group_name"]), str(r["name"])}] = str(r["id"])
		}
	}

	now := now()
	rows := make([]map[string]interface{}, 0, len(items))
	sortByCol := map[string]int{}
	namesInCol := map[string]map[string]int{}
	seen := map[string]bool{}

	for _, it := range items {
		if !bookmark.IsWebURL(it.URL) {
			res.Invalid++
			continue
		}
		key := bookmark.NormalizeURL(it.URL)
		if key == "" {
			res.Invalid++
			continue
		}
		if seen[key] {
			res.Duplicated++
			continue
		}
		if existing[key] {
			res.Skipped++
			continue
		}
		seen[key] = true

		group, name := splitFolder(it.Folder)
		colID, ok := cols[colKey{group, name}]
		if !ok {
			// 书签集合用「网页集合」而不是「目录集合」：它装的是网页链接而非本机目录，
			// 侧栏图标（Globe）与集合类型标签一眼能和普通目录集合区分。
			created, cerr := d.CreateCollection(wsID, sceneID, name, "网页集合", "single", group)
			if cerr != nil {
				// 并发下同名集合可能刚被建出来，回查一次；仍失败就跳过这条
				if r, e := d.QueryOne("SELECT id FROM collections WHERE scene_id = ? AND name = ? AND group_name = ?", sceneID, name, group); e == nil && r != nil {
					colID = str(r["id"])
				} else {
					res.Invalid++
					continue
				}
			} else {
				colID = created.ID
				res.Collections++
			}
			cols[colKey{group, name}] = colID
		}

		title := truncateName(it.Title)
		if title == "" {
			title = bookmark.HostOf(it.URL)
		}
		if title == "" {
			title = it.URL
		}
		// 同集合内允许重名——BulkInsert 绕过了 CreateItem 的重名校验，
		// 但界面上两条同名条目没法区分，故这里补后缀：foo / foo · 2 / foo · 3
		if namesInCol[colID] == nil {
			namesInCol[colID] = map[string]int{}
		}
		namesInCol[colID][title]++
		if n := namesInCol[colID][title]; n > 1 {
			title = truncateName(fmt.Sprintf("%s · %d", title, n))
		}

		sortByCol[colID]++
		rows = append(rows, map[string]interface{}{
			"id":            newID(),
			"workspace_id":  wsID,
			"collection_id": colID,
			"name":          title,
			"type":          "网页",
			"value":         it.URL,
			"remark":        it.Remark,
			"sort":          sortByCol[colID],
			"created_at":    now,
			"updated_at":    now,
		})
	}

	if len(rows) == 0 {
		return res, nil
	}
	// 一次性批量写入：逐条 CreateItem 每条都要过一次重名查询 + 单独事务，
	// 上千条书签会跑出几十秒。这里结构已建好，剩下的只是插入。
	if err := d.BulkInsert("items", rows); err != nil {
		return nil, fmt.Errorf("写入条目失败: %w", err)
	}
	res.Created = len(rows)
	return res, nil
}

// existingWebURLs 取库里所有网页条目的归一化 URL 集合。
func (d *Database) existingWebURLs() (map[string]bool, error) {
	rows, err := d.Query("SELECT value FROM items WHERE type = ?", "网页")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		if k := bookmark.NormalizeURL(str(r["value"])); k != "" {
			out[k] = true
		}
	}
	return out, nil
}

// WebURLSet 库里已有的网页地址集合（归一化后），键是 NormalizeURL 的结果。
//
// 预览要用它算出「导入后实际新增多少条」：用户看到「共 565 条、将新增 146 条」，
// 才不会在导入后以为丢了几百条——那些其实早就从另一个浏览器进来过了。
func (d *Database) WebURLSet() (map[string]bool, error) {
	return d.existingWebURLs()
}

// defaultWorkspaceID 返回全应用唯一（默认）工作空间的 id。
//
// 书签导入落在它里面，而不是像旧版那样另开一个「书签」空间：应用里已经去掉
// 新建/切换工作空间，多出来的空间用户根本切不过去，数据等于失联。
// 顺序按 sort / created_at，与侧栏展示的第一个空间一致；一个都没有时补建一个
// （正常启动 services 会先建好，这里只兜住测试与冷启动的极端顺序）。
func (d *Database) defaultWorkspaceID() (string, error) {
	if r, err := d.QueryOne("SELECT id FROM workspaces ORDER BY sort ASC, created_at ASC LIMIT 1"); err == nil && r != nil {
		return str(r["id"]), nil
	}
	return d.ensureWorkspace(defaultWorkspaceName)
}

// mergeWorkspacesIntoDefaultLocked 把所有非默认工作空间的数据并入默认工作空间。
//
// 应用现在只有一个工作空间。老库里用户可能建过多个空间，或者旧版导入书签时建过
// 单独的「书签」空间——这些空间在侧栏已无切换入口，里面的数据等于失联。升级时
// 一次性把它们的场景 / 集合 / 条目改挂到默认空间，再删掉空空间。
//
// 默认空间的选择：优先名字等于 defaultWorkspaceName 的；没有则取 sort/created_at
// 第一个（与侧栏展示的第一个一致）。库里只有一个空间时把它改名成默认名。
//
// 同名场景合并：默认空间已有同名场景时把集合改挂过去、删掉旧场景，避免侧栏出现
// 两个看不出区别的同名场景。items 没有 scene_id（只挂集合），靠末尾整体 UPDATE
// workspace_id 带走。来自旧「书签」空间的场景类型补成「书签」。
//
// 幂等：合并完空间就没了，下次启动直接跳过。调用方需持 d.mu（migrate 已持锁），
// 因此全部直接走 d.conn，不能用 d.Query 系列。
func (d *Database) mergeWorkspacesIntoDefaultLocked() error {
	rows, err := d.conn.Query(`SELECT id, name FROM workspaces ORDER BY sort ASC, created_at ASC`)
	if err != nil {
		return err
	}
	type wsRow struct{ id, name string }
	var all []wsRow
	for rows.Next() {
		var w wsRow
		if err := rows.Scan(&w.id, &w.name); err != nil {
			rows.Close()
			return err
		}
		all = append(all, w)
	}
	rows.Close()
	if len(all) == 0 {
		return nil // 空库，services 启动时会补建默认空间
	}

	// 默认空间：优先名字匹配的；否则取 sort 最前的
	defID := ""
	for _, w := range all {
		if w.name == defaultWorkspaceName {
			defID = w.id
			break
		}
	}
	if defID == "" {
		defID = all[0].id
	}
	if len(all) == 1 {
		// 只有一个空间：改名成默认名即可，数据原地不动
		if all[0].name != defaultWorkspaceName {
			if _, e := d.conn.Exec(`UPDATE workspaces SET name = ? WHERE id = ?`, defaultWorkspaceName, defID); e != nil {
				return e
			}
		}
		return nil
	}

	for _, w := range all {
		if w.id == defID {
			continue
		}
		if err := d.mergeOneWorkspaceLocked(w.id, w.name, defID); err != nil {
			return err
		}
	}

	// 已迁移过的库（书签场景已在默认空间、类型已是「书签」）里，集合类型还是旧的
	// 「目录集合」。按场景类型幂等修正一次，不依赖「旧空间还在」。
	if _, err := d.conn.Exec(
		`UPDATE collections SET type = ? WHERE scene_id IN (SELECT id FROM scenes WHERE type = ?)`,
		"网页集合", bookmarkSceneType); err != nil {
		return err
	}
	return nil
}

// mergeOneWorkspaceLocked 把单个工作空间(oldID)的内容并入 defID 并删除该空间。
func (d *Database) mergeOneWorkspaceLocked(oldID, oldName, defID string) error {
	sceneRows, err := d.conn.Query(`SELECT id, name FROM scenes WHERE workspace_id = ?`, oldID)
	if err != nil {
		return err
	}
	type oldScene struct{ id, name string }
	var olds []oldScene
	for sceneRows.Next() {
		var s oldScene
		if err := sceneRows.Scan(&s.id, &s.name); err != nil {
			sceneRows.Close()
			return err
		}
		olds = append(olds, s)
	}
	sceneRows.Close()

	for _, s := range olds {
		var dupID string
		dupErr := d.conn.QueryRow(
			`SELECT id FROM scenes WHERE workspace_id = ? AND name = ?`, defID, s.name).Scan(&dupID)
		if dupErr == nil {
			// 默认空间里已有同名场景：集合改挂过去，旧场景随之删掉。
			// 书签空间来的集合顺带把类型改成「网页集合」——必须在改挂前改，
			// 否则会误伤默认空间同名场景下原有的集合。
			if oldName == bookmarkWorkspaceName {
				if _, e := d.conn.Exec(
					`UPDATE collections SET type = ? WHERE scene_id = ?`, "网页集合", s.id); e != nil {
					return e
				}
			}
			if _, e := d.conn.Exec(
				`UPDATE collections SET scene_id = ?, workspace_id = ? WHERE scene_id = ?`, dupID, defID, s.id); e != nil {
				return e
			}
			if _, e := d.conn.Exec(`DELETE FROM scenes WHERE id = ?`, s.id); e != nil {
				return e
			}
			continue
		}
		if !errors.Is(dupErr, sql.ErrNoRows) {
			return dupErr
		}
		// 来自旧「书签」空间的场景类型补成「书签」，其下集合类型补成「网页集合」；
		// 其他空间保留原 type。
		newType := ""
		if oldName == bookmarkWorkspaceName {
			newType = bookmarkSceneType
			if _, e := d.conn.Exec(
				`UPDATE collections SET type = ? WHERE scene_id = ?`, "网页集合", s.id); e != nil {
				return e
			}
		}
		if newType != "" {
			if _, e := d.conn.Exec(
				`UPDATE scenes SET workspace_id = ?, type = ? WHERE id = ?`, defID, newType, s.id); e != nil {
				return e
			}
		} else if _, e := d.conn.Exec(`UPDATE scenes SET workspace_id = ? WHERE id = ?`, defID, s.id); e != nil {
			return e
		}
	}

	// 场景以外的直接子项（理论上没有，兜住手工改库留下的孤儿行）
	for _, stmt := range []string{
		`UPDATE collections SET workspace_id = ? WHERE workspace_id = ?`,
		`UPDATE items SET workspace_id = ? WHERE workspace_id = ?`,
	} {
		if _, e := d.conn.Exec(stmt, defID, oldID); e != nil {
			return e
		}
	}
	if _, e := d.conn.Exec(`DELETE FROM workspaces WHERE id = ?`, oldID); e != nil {
		return e
	}
	logger.I("[db] 工作空间已并入默认空间：%s(%s) → %s，场景 %d 个", oldID, oldName, defID, len(olds))
	return nil
}

// ensureSceneByName 按名字 find-or-create 场景（ensureScene 只认第一个场景，
// 书签导入需要「一个浏览器一个场景」，同名复用）。
//
// sceneType 只在新建时生效：已有同名场景可能是用户自己建的（类型是他选的），
// 复用时不该被改写。
func (d *Database) ensureSceneByName(workspaceID, name, sceneType string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "书签"
	}
	if sceneType == "" {
		sceneType = "通用"
	}
	if r, err := d.QueryOne("SELECT id FROM scenes WHERE workspace_id = ? AND name = ?", workspaceID, name); err == nil && r != nil {
		return str(r["id"]), nil
	}
	sc, err := d.CreateScene(workspaceID, name, sceneType, "")
	if err != nil {
		if r, e := d.QueryOne("SELECT id FROM scenes WHERE workspace_id = ? AND name = ?", workspaceID, name); e == nil && r != nil {
			return str(r["id"]), nil
		}
		return "", err
	}
	return sc.ID, nil
}

// splitFolder 把文件夹路径拆成（组名, 集合名）。
//
// 两层以上时，上级路径用 " / " 连成组名：书签里的「前端 / 框架 / Vue」因此在侧栏
// 表现为「前端 / 框架」组下的「Vue」集合，折叠后仍能看出原层级。根上的书签
// 落进「未分类」集合——留空会得到一个没有名字的集合，用户找不到条目跑哪去了。
func splitFolder(folder []string) (group, name string) {
	cleaned := make([]string, 0, len(folder))
	for _, f := range folder {
		if f = strings.TrimSpace(f); f != "" {
			cleaned = append(cleaned, f)
		}
	}
	if len(cleaned) == 0 {
		return "", "未分类"
	}
	if len(cleaned) == 1 {
		return "", truncateName(cleaned[0])
	}
	return truncateName(strings.Join(cleaned[:len(cleaned)-1], " / ")), truncateName(cleaned[len(cleaned)-1])
}

func truncateName(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxBookmarkNameLen {
		return s
	}
	return string(r[:maxBookmarkNameLen-1]) + "…"
}

// ListBookmarks 列出书签（网页类型条目）及其所在集合 / 场景，供书签页渲染。
// keyword 走 LIKE 前缀匹配，与书签页的即时筛选同口径；空串表示不筛。
// 上限 2000：书签页是浏览用的，超过这个量应当用搜索而不是一路滚动。
func (d *Database) ListBookmarks(keyword string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	q := `SELECT i.id, i.name, i.value, i.remark, i.color, i.icon, i.usage_count,
	             i.collection_id, c.name AS collection_name, c.group_name,
	             c.scene_id, s.name AS scene_name
	      FROM items i
	      LEFT JOIN collections c ON c.id = i.collection_id
	      LEFT JOIN scenes s ON s.id = c.scene_id
	      WHERE i.type = '网页'`
	args := []interface{}{}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q += " AND (i.name LIKE ? OR i.value LIKE ?)"
		like := "%" + kw + "%"
		args = append(args, like, like)
	}
	q += " ORDER BY s.name, c.group_name, c.name, i.sort LIMIT ?"
	args = append(args, limit)
	return d.Query(q, args...)
}

// IconTask 一条待补图标的条目。
//
// 只带 id 与 url：补图标只需要这两个字段，回传整行会让 service 层多一次映射。
type IconTask struct {
	ID  string
	URL string
}

// ItemsMissingIcon 取缺少图标的网页条目，按使用频率优先。
//
// 导入走的是 BulkInsert，绕过了 CreateItem 那条「保存时自动抓 favicon」的路径，
// 因此导入的几百条书签 icon 全为空。这里给页面一个「按需补齐」的入口：
// 每次只取一小批，抓到就写回，抓不到的条目下次还会被选中（Limit 保证不会饿死）。
//
// 排序用 usage_count DESC + created_at DESC，末位用 RANDOM() 打散：
// 用户最常打开的、最近收藏的先出图标；抓不到的条目（站点拒绝、已下线）会留在
// 缺图标集合里，靠随机末位让它下一轮换一批，否则每次都被同一批失败条目占满名额，
// 后面的条目永远轮不到。
func (d *Database) ItemsMissingIcon(limit int) ([]IconTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 24
	}
	rows, err := d.Query(
		`SELECT id, value FROM items
		 WHERE type = '网页' AND (icon IS NULL OR icon = '')
		 ORDER BY usage_count DESC, created_at DESC, RANDOM()
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	out := make([]IconTask, 0, len(rows))
	for _, r := range rows {
		if id, u := str(r["id"]), str(r["value"]); id != "" && u != "" {
			out = append(out, IconTask{ID: id, URL: u})
		}
	}
	return out, nil
}

// SetItemIcon 写入图标，仅当该条目当前仍无图标。
//
// 带 icon = ” 条件是为了不和用户手动在编辑器里设置的图标抢：
// 抓取是异步的，期间用户可能已经手动改过。
func (d *Database) SetItemIcon(id, icon string) error {
	if id == "" || icon == "" {
		return nil
	}
	return d.ExecuteParams(`UPDATE items SET icon = ? WHERE id = ? AND (icon IS NULL OR icon = '')`, []interface{}{icon, id})
}
