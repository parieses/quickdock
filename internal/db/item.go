package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os/exec"
	goruntime "runtime"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"quickdock/internal/platform"
)

const itemCols = "id, workspace_id, collection_id, name, type, value, working_directory, tool_id, tool, args, icon, color, remark, plugin_data, usage_count, sort, created_at, updated_at"

// ---- 项目 ----

func (d *Database) ListItems(collectionID string) ([]CollectionItem, error) {
	rows, err := d.ListTableWhere("items", "collection_id = ?", collectionID)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToItem), nil
}

// fts5Escape 清理 FTS5 查询中的特殊字符（移除会导致 FTS5 解析错误的运算符和关键字）
func fts5Escape(q string) string {
	// FTS5 保留关键字（AND/OR/NOT/NEAR）仅当以独立 token 出现时才剥离，
	// 绝不能按子串替换——否则会把 COMMAND、SANDWICH、NOTE 等正常词里的子串误删。
	reserved := map[string]bool{"AND": true, "OR": true, "NOT": true, "NEAR": true}
	// 单字符运算符直接移除（unicode61 分词器本就会按这些符号切词；"-" 等若残留会破坏查询）。
	specials := []string{"\"", "*", "+", "-", "(", ")", "~", "^", "<", ">", ","}
	result := q
	for _, s := range specials {
		result = strings.ReplaceAll(result, s, " ")
	}
	var out []string
	for _, tok := range strings.Fields(result) {
		if reserved[strings.ToUpper(tok)] {
			continue
		}
		out = append(out, tok)
	}
	return strings.Join(out, " ")
}

// SearchAllItems 跨全部工作空间搜索项目（使用 FTS5 全文索引）
// query 为空时返回空结果（前端请使用 GetMostUsedItems 获取热数据）
func (d *Database) SearchAllItems(query string) ([]CollectionItem, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if query == "" {
		return nil, nil
	}

	// FTS5 前缀匹配：每个词 + *
	safe := fts5Escape(query)
	if safe == "" {
		return nil, nil
	}
	words := strings.Fields(safe)
	var parts []string
	for _, w := range words {
		parts = append(parts, w+"*")
	}
	ftsQuery := strings.Join(parts, " ")

	// items_fts 虚拟表自身含 id/name/value 列，与 items 表同名，SELECT 必须加 items. 前缀限定
	rows, err := d.conn.Query(`SELECT `+("items."+strings.ReplaceAll(itemCols, ", ", ", items."))+`
		FROM items_fts JOIN items ON items.rowid = items_fts.rowid
		WHERE items_fts MATCH ?
		ORDER BY rank
		LIMIT 200`, ftsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// GetMostUsedItems 返回最常使用的项目（按 usage_count 降序，用于命令面板「最近使用」）
func (d *Database) GetMostUsedItems(limit int) ([]CollectionItem, error) {
	if limit <= 0 {
		limit = 30
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query("SELECT "+itemCols+" FROM items ORDER BY usage_count DESC, updated_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// ListAllItems 返回全部工作空间的项目（不分页）。
// 命令面板改用前端对全量池做拼音/子串权威匹配（见 useCommandSearch），
// 避免后端 FTS5 前缀匹配导致拼音与子串搜索完全失效。
func (d *Database) ListAllItems() ([]CollectionItem, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query("SELECT " + itemCols + " FROM items ORDER BY usage_count DESC, updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// scanItems 通用 items 行扫描器
func scanItems(rows *sql.Rows) ([]CollectionItem, error) {
	var items []CollectionItem
	for rows.Next() {
		var item CollectionItem
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.CollectionID, &item.Name, &item.Type, &item.Value, &item.WorkingDirectory, &item.ToolID, &item.Tool, &item.Args, &item.Icon, &item.Color, &item.Remark, &item.PluginData, &item.UsageCount, &item.Sort, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (d *Database) CreateItem(workspaceID, collectionID, name, itemType, value string) (*CollectionItem, error) {
	name = validateName(name)
	if name == "" {
		return nil, fmt.Errorf("项目名称不能为空")
	}
	if collectionID == "" {
		return nil, fmt.Errorf("集合 ID 不能为空")
	}
	exists, err := d.nameExists("items", "collection_id = ? AND name = ?", collectionID, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("名称已存在")
	}

	item := &CollectionItem{
		ID:           newID(),
		WorkspaceID:  workspaceID,
		CollectionID: collectionID,
		Name:         name,
		Type:         itemType,
		Value:        value,
		CreatedAt:    now(),
		UpdatedAt:    now(),
	}
	// 应用/exe/lnk 类型：自动从文件提取图标（无图标资源则留空，渲染回退到默认图标）
	if icon := platform.ExtractItemIcon(itemType, value); icon != "" {
		item.Icon = icon
	}
	err = d.BulkInsert("items", []map[string]interface{}{structToMap(item)})
	return item, err
}

// SaveUrlAsItem 将 URL 保存为「网页」项目，落入专用「剪贴板收藏」工作空间/
// 场景/集合（find-or-create）。命令面板为独立窗口，无法感知主窗口当前选中的
// 工作空间，故使用固定落点，避免跨窗口状态传递。各辅助方法内部已加锁，此处不持锁。
func (d *Database) SaveUrlAsItem(rawURL string) (*CollectionItem, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("不是有效的 http/https URL")
	}

	wsID, err := d.ensureWorkspace("剪贴板收藏")
	if err != nil {
		return nil, err
	}
	sceneID, err := d.ensureScene(wsID, "默认")
	if err != nil {
		return nil, err
	}
	colID, err := d.ensureCollection(wsID, sceneID, "网页")
	if err != nil {
		return nil, err
	}
	return d.CreateItem(wsID, colID, u.Host, "网页", rawURL)
}

// 以下 ensure* 均为幂等 find-or-create：并发下若创建因重名失败，回退为查询已有记录。
func (d *Database) ensureWorkspace(name string) (string, error) {
	row, err := d.QueryOne("SELECT * FROM workspaces WHERE name = ?", name)
	if err != nil {
		return "", err
	}
	if row != nil {
		return str(row["id"]), nil
	}
	ws, err := d.CreateWorkspace(name)
	if err != nil {
		if row2, e2 := d.QueryOne("SELECT * FROM workspaces WHERE name = ?", name); e2 == nil && row2 != nil {
			return str(row2["id"]), nil
		}
		return "", err
	}
	return ws.ID, nil
}

func (d *Database) ensureScene(workspaceID, name string) (string, error) {
	scenes, err := d.ListScenes(workspaceID)
	if err != nil {
		return "", err
	}
	if len(scenes) > 0 {
		return scenes[0].ID, nil
	}
	sc, err := d.CreateScene(workspaceID, name, "通用")
	if err != nil {
		if scenes2, e2 := d.ListScenes(workspaceID); e2 == nil && len(scenes2) > 0 {
			return scenes2[0].ID, nil
		}
		return "", err
	}
	return sc.ID, nil
}

func (d *Database) ensureCollection(workspaceID, sceneID, name string) (string, error) {
	cols, err := d.ListCollections(sceneID)
	if err != nil {
		return "", err
	}
	if len(cols) > 0 {
		return cols[0].ID, nil
	}
	col, err := d.CreateCollection(workspaceID, sceneID, name, "目录集合", "")
	if err != nil {
		if cols2, e2 := d.ListCollections(sceneID); e2 == nil && len(cols2) > 0 {
			return cols2[0].ID, nil
		}
		return "", err
	}
	return col.ID, nil
}

func (d *Database) UpdateItem(id string, updates map[string]interface{}) error {
	if id == "" {
		return fmt.Errorf("id 不能为空")
	}
	if name, ok := updates["name"]; ok {
		if s, ok2 := name.(string); ok2 && validateName(s) == "" {
			return fmt.Errorf("项目名称不能为空")
		}
	}
	// 应用/exe/lnk 类型：未显式设置图标且 value/type 变化时，尝试自动提取图标
	if _, iconSet := updates["icon"]; !iconSet {
		d.autoFillItemIcon(id, updates)
	}
	updates["updated_at"] = now()
	return d.updateByID("items", id, updates)
}

// autoFillItemIcon 在编辑 item 时，按更新后的类型/值自动处理图标：
//   - 路径(value)发生变化：按新路径重新提取；若不再是 exe/lnk 则清空图标回退默认图标
//   - 路径未变且当前无图标：尝试补齐（历史/早期创建的 item）
//   - 路径未变且已有图标：保留，不覆盖
func (d *Database) autoFillItemIcon(id string, updates map[string]interface{}) {
	row, err := d.QueryOne("SELECT type, value, icon FROM items WHERE id = ?", id)
	if err != nil || row == nil {
		return
	}
	itemType := str(row["type"])
	oldValue := str(row["value"])
	value := oldValue
	if t, ok := updates["type"].(string); ok && t != "" {
		itemType = t
	}
	newVal, valChanged := updates["value"].(string)
	if valChanged && newVal != "" {
		value = newVal
	}
	// 路径变化：以新路径为准重新提取图标（非 exe/lnk 则清空）
	if valChanged && newVal != oldValue {
		if icon := platform.ExtractItemIcon(itemType, value); icon != "" {
			updates["icon"] = icon
		} else {
			updates["icon"] = ""
		}
		return
	}
	// 路径未变且无图标：尝试补齐
	if str(row["icon"]) == "" {
		if icon := platform.ExtractItemIcon(itemType, value); icon != "" {
			updates["icon"] = icon
		}
	}
}

func (d *Database) DeleteItem(id string) error {
	return d.DeleteWhere("items", "id = ?", id)
}

// ---- 打开项目 ----

func (d *Database) OpenItem(item *CollectionItem) error {
	var tool OpenTool
	if item.ToolID != "" {
		row, err := d.QueryOne("SELECT * FROM tools WHERE id = ?", item.ToolID)
		if err == nil {
			tool = mapToOpenTool(row)
		}
	}
	if tool.ID == "" {
		row, err := d.QueryOne("SELECT * FROM tools WHERE is_default = 1 LIMIT 1")
		if err == nil {
			tool = mapToOpenTool(row)
		}
	}

	d.ExecuteParams("UPDATE items SET usage_count = usage_count + 1 WHERE id = ?", []interface{}{item.ID})

	return execOpen(item, tool)
}

func (d *Database) OpenAllInCollection(collectionID string) error {
	rows, err := d.Query("SELECT * FROM items WHERE collection_id = ? ORDER BY sort, created_at", collectionID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		item := mapToItem(row)
		_ = d.OpenItem(&item)
	}
	return nil
}

func execOpen(item *CollectionItem, tool OpenTool) error {
	value := item.Value
	itemType := item.Type

	if tool.Path == "" || tool.Name == "系统默认" {
		return openWithSystemDefault(value, itemType, item.WorkingDirectory)
	}

	args := tool.Args
	if args == "" {
		args = "{{path}}"
	}
	if itemType == "网页" || itemType == "快速链接" {
		args = strings.ReplaceAll(args, "{{url}}", value)
		args = strings.ReplaceAll(args, "{{path}}", value)
	} else if itemType == "命令" {
		// 终端工具（cmd/powershell/wt/wsl）会重新解析 /c 之后的整条命令行，
		// 路径里的空格、括号会被当成语法分组而静默失败。
		// 解决：用 SysProcAttr.CmdLine 直接传递命令行，并对含空格/特殊字符的值
		// 做 ""value"" 双重引号包裹（cmd 会剥掉最外层引号，保留内层引号当字面量）。
		if isTerminalTool(tool.Path) {
			tmpl := tool.Args
			if tmpl == "" {
				tmpl = "/c {{command}}"
			}
			quoted := value
			if strings.ContainsAny(value, " \t()&|<>^") {
				quoted = `""` + value + `""`
			}
			inner := strings.ReplaceAll(strings.ReplaceAll(tmpl, "{{command}}", quoted), "{{path}}", quoted)
			c := exec.Command(tool.Path)
			c.SysProcAttr = &syscall.SysProcAttr{CmdLine: tool.Path + " " + inner}
			if item.WorkingDirectory != "" {
				c.Dir = item.WorkingDirectory
			}
			return c.Start()
		}
		args = strings.ReplaceAll(args, "{{command}}", value)
		args = strings.ReplaceAll(args, "{{path}}", value)
	} else {
		args = strings.ReplaceAll(args, "{{path}}", value)
		args = strings.ReplaceAll(args, "{{command}}", value)
		args = strings.ReplaceAll(args, "{{url}}", value)
	}

	argList := splitArgs(args)
	cmd := exec.Command(tool.Path, argList...)
	if item.WorkingDirectory != "" {
		cmd.Dir = item.WorkingDirectory
	}
	return cmd.Start()
}

// validOpenSchemes 允许通过 ShellExecute 打开的 URL 协议白名单。
var validOpenSchemes = map[string]bool{
	"http": true, "https": true, "mailto": true, "ftp": true, "file": true,
}

// validateOpenTarget 校验用户存储的打开目标，防止 javascript:/ms-powershell: 等
// 危险协议被 ShellExecute 直接触发（存储型协议注入）。
func validateOpenTarget(itemType, value string) error {
	if value == "" {
		return fmt.Errorf("打开目标为空")
	}
	lower := strings.ToLower(value)
	// 危险协议黑名单（直接拒绝）
	dangerous := []string{
		"javascript:", "vbscript:", "ms-powershell:", "powershell:",
		"cmd:", "ms-msdt:", "msdt:", "wscript:", "cscript:",
	}
	for _, p := range dangerous {
		if strings.HasPrefix(lower, p) {
			return fmt.Errorf("拒绝危险协议: %s", p)
		}
	}
	// 网页/快速链接只允许 http/https/mailto/ftp 协议；无协议的裸域名按 https 处理，
	// 避免误拒用户已有的条目（如 github.com）。
	if itemType == "网页" || itemType == "快速链接" {
		u, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("无效的链接: %s", value)
		}
		if u.Scheme == "" {
			if u, err = url.Parse("https://" + value); err != nil {
				return fmt.Errorf("无效的链接: %s", value)
			}
		}
		if !validOpenSchemes[u.Scheme] {
			return fmt.Errorf("不支持的链接协议: %s", u.Scheme)
		}
	}
	return nil
}

func openWithOSDefault(value string) error {
	switch goruntime.GOOS {
	case "windows":
		return windows.ShellExecute(0,
			windows.StringToUTF16Ptr("open"),
			windows.StringToUTF16Ptr(value),
			nil, nil, windows.SW_SHOWNORMAL)
	case "darwin":
		return exec.Command("open", value).Start()
	default:
		return exec.Command("xdg-open", value).Start()
	}
}

func openCommand(value, workingDir string) error {
	argList := splitArgs(value)
	if len(argList) == 0 {
		return fmt.Errorf("命令内容为空")
	}
	cmd := exec.Command(argList[0], argList[1:]...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	return cmd.Start()
}

func openWithSystemDefault(value, itemType string, workingDir string) error {
	if err := validateOpenTarget(itemType, value); err != nil {
		return err
	}

	switch itemType {
	case "网页", "快速链接", "目录", "文件":
		return openWithOSDefault(value)
	case "命令":
		return openCommand(value, workingDir)
	default:
		if goruntime.GOOS == "windows" {
			return windows.ShellExecute(0,
				windows.StringToUTF16Ptr("open"),
				windows.StringToUTF16Ptr(value),
				nil, nil, windows.SW_SHOWNORMAL)
		}
		cmd := exec.Command(value)
		return cmd.Start()
	}
}

func splitArgs(args string) []string {
	var result []string
	var current []byte
	inQuotes := false

	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case c == ' ' && !inQuotes:
			if len(current) > 0 {
				result = append(result, string(current))
				current = current[:0]
			}
		default:
			current = append(current, c)
		}
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}
	return result
}

// isTerminalTool 判断某个可执行文件是否为终端类工具
// （cmd/powershell/wt/wsl）。这些工具接收整条命令作为参数，
// 调用方需自行对含空格/括号的命令加引号。
func isTerminalTool(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "cmd", "cmd.exe", "powershell", "powershell.exe",
		"wt", "wt.exe", "wsl", "wsl.exe":
		return true
	}
	return false
}
