package db

import "fmt"

// ---- 通用设置 ----

// GetSetting 从 app_state 读取配置值
func (d *Database) GetSetting(key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var val string
	err := d.conn.QueryRow("SELECT value FROM app_state WHERE key = ?", key).Scan(&val)
	if err != nil {
		return "", err
	}
	return val, nil
}

// SetSetting 写入或更新 app_state 配置
func (d *Database) SetSetting(key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("INSERT OR REPLACE INTO app_state (key, value) VALUES (?, ?)", key, value)
	return err
}

// updateByID 通过主键安全更新表记录（列名经白名单校验）
func (d *Database) updateByID(table, id string, updates map[string]interface{}) error {
	if err := validateTable(table); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	setClause := ""
	values := make([]interface{}, 0, len(updates)+1)
	i := 0
	for col, val := range updates {
		if err := validateColumn(col); err != nil {
			return err
		}
		if i > 0 {
			setClause += ", "
		}
		setClause += fmt.Sprintf("%s = ?", col)
		values = append(values, val)
		i++
	}
	values = append(values, id)

	_, err := d.conn.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", table, setClause), values...)
	return err
}
