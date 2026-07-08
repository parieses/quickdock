package db

import (
	"database/sql"
	"fmt"
	"os/exec"
	goruntime "runtime"
	"strings"

	"golang.org/x/sys/windows"
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
	specials := []string{"\"", "*", "+", "-", "(", ")", "~", "^", "<", ">", ",", "AND", "OR", "NOT", "NEAR"}
	result := q
	for _, s := range specials {
		result = strings.ReplaceAll(result, s, "")
	}
	return strings.TrimSpace(result)
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

	rows, err := d.conn.Query(`SELECT `+itemCols+`
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
	err = d.BulkInsert("items", []map[string]interface{}{structToMap(item)})
	return item, err
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
	updates["updated_at"] = now()
	return d.updateByID("items", id, updates)
}

func (d *Database) DeleteItem(id string) error {
	return d.DeleteWhere("items", "id = ?", id)
}

func (d *Database) ReorderItems(orderedIDs []string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		for i, id := range orderedIDs {
			if _, err := tx.Exec("UPDATE items SET sort = ? WHERE id = ?", i*10, id); err != nil {
				return err
			}
		}
		return nil
	})
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

func openWithSystemDefault(value, itemType string, workingDir string) error {
	goos := goruntime.GOOS

	if itemType == "网页" || itemType == "快速链接" {
		if goos == "windows" {
			return windows.ShellExecute(0,
				windows.StringToUTF16Ptr("open"),
				windows.StringToUTF16Ptr(value),
				nil, nil, windows.SW_SHOWNORMAL)
		} else if goos == "darwin" {
			return exec.Command("open", value).Start()
		} else {
			return exec.Command("xdg-open", value).Start()
		}
	} else if itemType == "目录" || itemType == "文件" {
		if goos == "windows" {
			return windows.ShellExecute(0,
				windows.StringToUTF16Ptr("open"),
				windows.StringToUTF16Ptr(value),
				nil, nil, windows.SW_SHOWNORMAL)
		} else if goos == "darwin" {
			return exec.Command("open", value).Start()
		} else {
			return exec.Command("xdg-open", value).Start()
		}
	} else if itemType == "命令" {
		if goos == "windows" {
			argList := splitArgs(value)
			if len(argList) == 0 {
				return fmt.Errorf("命令内容为空")
			}
			cmd := exec.Command(argList[0], argList[1:]...)
			if workingDir != "" {
				cmd.Dir = workingDir
			}
			return cmd.Start()
		} else {
			cmd := exec.Command("sh", "-c", value)
			if workingDir != "" {
				cmd.Dir = workingDir
			}
			return cmd.Start()
		}
	} else {
		if goos == "windows" {
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
