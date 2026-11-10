package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// ---- 通用设置 ----

// settingEntry 缓存条目。found=false 表示「库里没有这个键」也要缓存：
// 剪贴板暂停/忽略列表这类键默认不存在，不缓存 miss 就等于没省。
type settingEntry struct {
	val   string
	found bool
}

// GetSetting 从 app_state 读取配置值。
//
// 结果走进程内缓存：剪贴板捕获是全应用最热的路径（每次 Ctrl+C 查 2-3 个
// 几乎不变的设置键），database/sql 每次调用都是一轮 prepare+exec+close。
// 一致性前提：app_state 的全部写入收敛在 SetSetting（写穿缓存），且本表
// 不参与快照恢复（snapshot.go 已注明），不存在绕过缓存的写入方。
func (d *Database) GetSetting(key string) (string, error) {
	if v, ok := d.settingCache.Load(key); ok {
		e := v.(settingEntry)
		if e.found {
			return e.val, nil
		}
		return "", sql.ErrNoRows
	}
	d.mu.RLock()
	var val string
	err := d.conn.QueryRow("SELECT value FROM app_state WHERE key = ?", key).Scan(&val)
	d.mu.RUnlock()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			d.settingCache.Store(key, settingEntry{found: false})
		}
		// 其他查询错误不缓存，下次重试真库
		return "", err
	}
	d.settingCache.Store(key, settingEntry{val: val, found: true})
	return val, nil
}

// SetSetting 写入或更新 app_state 配置（写穿缓存）。
func (d *Database) SetSetting(key, value string) error {
	d.mu.Lock()
	_, err := d.conn.Exec("INSERT OR REPLACE INTO app_state (key, value) VALUES (?, ?)", key, value)
	d.mu.Unlock()
	if err != nil {
		// 写失败时丢掉可能的旧缓存，宁可下次回库查也别留旧值
		d.settingCache.Delete(key)
		return err
	}
	d.settingCache.Store(key, settingEntry{val: value, found: true})
	return nil
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
