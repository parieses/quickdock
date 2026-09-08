package db

import (
	"database/sql"
	"fmt"
)

// ---- 打开工具 ----

func (d *Database) ListTools() ([]OpenTool, error) {
	rows, err := d.ListTable("tools")
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToOpenTool), nil
}

func (d *Database) CreateTool(name, toolType, path, args string) (*OpenTool, error) {
	name = validateName(name)
	if name == "" {
		return nil, fmt.Errorf("工具名称不能为空")
	}

	t := &OpenTool{
		ID:        newID(),
		Name:      name,
		Type:      toolType,
		Path:      path,
		Args:      args,
		IsDefault: 0,
	}
	err := d.BulkInsert("tools", []map[string]interface{}{structToMap(t)})
	return t, err
}

// UpdateTool 更新已存在打开工具的字段；isDefault=1 时清除其它默认工具。
// 清默认与更新在同一事务内完成，避免并发设置默认时出现多个默认工具（data race）。
func (d *Database) UpdateTool(id, name, toolType, path, args string, isDefault int) error {
	name = validateName(name)
	if name == "" {
		return fmt.Errorf("工具名称不能为空")
	}
	return d.Transaction(func(tx *sql.Tx) error {
		if isDefault == 1 {
			if _, err := tx.Exec(`UPDATE tools SET is_default = 0 WHERE id != ?`, id); err != nil {
				return err
			}
		}
		setClause := "name = ?, type = ?, path = ?, args = ?, is_default = ?"
		_, err := tx.Exec(`UPDATE tools SET `+setClause+` WHERE id = ?`,
			name, toolType, path, args, isDefault, id)
		return err
	})
}

// DeleteTool 删除打开工具
func (d *Database) DeleteTool(id string) error {
	return d.DeleteWhere("tools", "id = ?", id)
}

func (d *Database) EnsureDefaultTools() error {
	// 按名称幂等补齐：已存在的默认工具不重复创建（兼容老库，新增的编辑器工具也能补上）
	defaults := []OpenTool{
		{Name: "系统默认", Type: "系统", Path: "", Args: "", IsDefault: 1},
		{Name: "VS Code", Type: "编辑器", Path: "code", Args: "{{path}}", IsDefault: 0},
		{Name: "Trae", Type: "编辑器", Path: "trae", Args: "{{path}}", IsDefault: 0},
		{Name: "Cursor", Type: "编辑器", Path: "cursor", Args: "{{path}}", IsDefault: 0},
		{Name: "Chrome", Type: "浏览器", Path: "chrome", Args: "{{url}}", IsDefault: 0},
		{Name: "Edge", Type: "浏览器", Path: "msedge", Args: "{{url}}", IsDefault: 0},
		{Name: "CMD", Type: "终端", Path: "cmd", Args: "/k {{command}}", IsDefault: 0},
		{Name: "PowerShell", Type: "终端", Path: "powershell", Args: "-Command {{command}}", IsDefault: 0},
		{Name: "Office", Type: "Office", Path: "", Args: "", IsDefault: 0},
	}
	for _, t := range defaults {
		n, err := d.CountWhere("tools", "name = ?", t.Name)
		if err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		t.ID = newID()
		if err := d.BulkInsert("tools", []map[string]interface{}{structToMap(&t)}); err != nil {
			return err
		}
	}
	return nil
}
