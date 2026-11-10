package db

// DlTask 只是下载任务的**元数据**，不是状态：进度、速度、状态一律实时问 aria2 要。
// 单独建表而非塞进 app_state：任务会有多条、要按时间倒序、要能按 gid 反查。
//
// 刻意不进快照（syncExtraTables）：换机恢复后 gid 与磁盘上的 .aria2 控制文件对不上，
// 留着的旧记录只会变成脏数据；任务本身靠 aria2.session 恢复。
type DlTask struct {
	GID       string `json:"gid"`
	URL       string `json:"url"`
	Name      string `json:"name"`
	SaveDir   string `json:"saveDir"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"createdAt"`
}

// InsertDlTask 登记一条任务元数据。用 INSERT OR REPLACE：会话恢复后同一个 gid 再被
// addUri 提交时不该因主键冲突失败。
func (d *Database) InsertDlTask(t DlTask) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		"INSERT OR REPLACE INTO dl_tasks (gid, url, name, save_dir, source, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		t.GID, t.URL, t.Name, t.SaveDir, t.Source, t.CreatedAt,
	)
	return err
}

func (d *Database) ListDlTasks() ([]DlTask, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query("SELECT gid, url, name, save_dir, source, created_at FROM dl_tasks ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DlTask{}
	for rows.Next() {
		var t DlTask
		if err := rows.Scan(&t.GID, &t.URL, &t.Name, &t.SaveDir, &t.Source, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *Database) DeleteDlTask(gid string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM dl_tasks WHERE gid = ?", gid)
	return err
}
