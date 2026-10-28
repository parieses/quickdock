package db

// 角标聚合所需的轻量计数查询（替代前端分别拉 Monitor/Todo 全量再前端数）。

// CountMonitorsDown 统计「启用中且最近一次检查失败」的监控数（角标用）
func (d *Database) CountMonitorsDown() (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM monitors WHERE enabled = 1 AND last_status = 'down'").Scan(&n)
	return n, err
}

// CountTodosDue 统计「未完成且到期日 ≤ today」的待办数（角标用）
func (d *Database) CountTodosDue(today string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM todos WHERE done = 0 AND due_date <> '' AND due_date <= ?", today).Scan(&n)
	return n, err
}
