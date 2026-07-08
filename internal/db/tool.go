package db

import "fmt"

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

func (d *Database) EnsureDefaultTools() error {
	count, err := d.CountWhere("tools", "1=1")
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	defaults := []OpenTool{
		{ID: newID(), Name: "系统默认", Type: "系统", Path: "", Args: "", IsDefault: 1},
		{ID: newID(), Name: "VS Code", Type: "编辑器", Path: "code", Args: "{{path}}", IsDefault: 0},
		{ID: newID(), Name: "Chrome", Type: "浏览器", Path: "chrome", Args: "{{url}}", IsDefault: 0},
		{ID: newID(), Name: "Edge", Type: "浏览器", Path: "msedge", Args: "{{url}}", IsDefault: 0},
		{ID: newID(), Name: "CMD", Type: "终端", Path: "cmd", Args: "/c {{command}}", IsDefault: 0},
		{ID: newID(), Name: "PowerShell", Type: "终端", Path: "powershell", Args: "-Command {{command}}", IsDefault: 0},
		{ID: newID(), Name: "Office", Type: "Office", Path: "", Args: "", IsDefault: 0},
	}
	for _, t := range defaults {
		err := d.BulkInsert("tools", []map[string]interface{}{structToMap(&t)})
		if err != nil {
			return err
		}
	}
	return nil
}
