package db

import (
	"time"
)

// Notification 应用内通知（通知中心的一条记录）。
//
// 与系统托盘通知（notifications.SendNotification）刻意分开：
// 托盘那条是「此刻提醒你一下」，弹出即走、系统侧留档不可控；
// 这张表是「回来还能翻到」。两者由同一个业务动作同时产生，
// 但消费场景不同，不能合并成一个通道。
type NotifyItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`  // monitor | schedule | ai | system
	Level     string `json:"level"` // info | success | warn | error
	Title     string `json:"title"`
	Body      string `json:"body"`
	RefType   string `json:"refType"` // 点击跳转的目标类型：monitor | schedule | ai | page
	RefID     string `json:"refId"`
	Read      bool   `json:"read"`
	CreatedTs int64  `json:"createdTs"`
}

const notificationCols = `id, kind, level, title, body, ref_type, ref_id, read, created_ts`

func scanNotification(s rowScanner) (NotifyItem, error) {
	var n NotifyItem
	var read int
	err := s.Scan(&n.ID, &n.Kind, &n.Level, &n.Title, &n.Body, &n.RefType, &n.RefID, &read, &n.CreatedTs)
	n.Read = read != 0
	return n, err
}

// maxNotifications 保留条数上限。
//
// 通知是「回头看一眼」的信息，没有长期价值：留着几千条既翻不动也拖慢列表。
// 插入时顺手裁掉超出部分，不做定时清理任务——这里的数据量极小，
// 每次插入一次 DELETE 的代价远低于多一个后台循环。
const maxNotifications = 500

// InsertNotification 写入一条通知，并把总量裁到上限。
func (d *Database) InsertNotification(n *NotifyItem) (*NotifyItem, error) {
	if n.Kind == "" {
		n.Kind = "system"
	}
	if n.Level == "" {
		n.Level = "info"
	}
	n.ID = newID()
	n.CreatedTs = time.Now().UnixMilli()
	n.Read = false

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, err := d.conn.Exec(
		`INSERT INTO notifications (`+notificationCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Kind, n.Level, n.Title, n.Body, n.RefType, n.RefID, 0, n.CreatedTs,
	); err != nil {
		return nil, err
	}
	// 裁掉最老的那些：按 created_ts 倒序保留 maxNotifications 条。
	_, _ = d.conn.Exec(
		`DELETE FROM notifications WHERE id NOT IN (
			SELECT id FROM notifications ORDER BY created_ts DESC LIMIT ?
		)`, maxNotifications)
	return n, nil
}

// ListNotifications 按时间倒序列出通知。onlyUnread 为 true 时只返回未读。
func (d *Database) ListNotifications(limit int, onlyUnread bool) ([]NotifyItem, error) {
	if limit <= 0 || limit > maxNotifications {
		limit = 200
	}
	q := `SELECT ` + notificationCols + ` FROM notifications`
	if onlyUnread {
		q += ` WHERE read = 0`
	}
	q += ` ORDER BY created_ts DESC LIMIT ?`

	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]NotifyItem, 0, limit)
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadNotificationCount 未读数（前端徽标用，只取一个整数）。
func (d *Database) UnreadNotificationCount() (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM notifications WHERE read = 0`).Scan(&n)
	return n, err
}

// MarkNotificationRead 标记单条已读。id 不存在时不算错误（可能已被清空）。
func (d *Database) MarkNotificationRead(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE notifications SET read = 1 WHERE id = ?`, id)
	return err
}

// MarkAllNotificationsRead 全部标记已读，返回受影响条数。
func (d *Database) MarkAllNotificationsRead() (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`UPDATE notifications SET read = 1 WHERE read = 0`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteNotification 删除单条。
func (d *Database) DeleteNotification(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM notifications WHERE id = ?`, id)
	return err
}

// ClearNotifications 清空通知中心，返回删除条数。
func (d *Database) ClearNotifications() (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`DELETE FROM notifications`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
