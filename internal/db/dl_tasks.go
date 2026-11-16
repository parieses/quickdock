package db

// DlTask 是下载任务的元数据 + 终态快照。
//
// 进度与速度只认 aria2 的实时值，不在这里双写；但 **状态/大小/错误/结束时间必须落库**：
// aria2 的 tellStopped 是进程内存，daemon 一退，已完成与失败的任务就再也查不到了。
// 不落这一份的话，QuickDock 每次重启，下载页的历史记录都会凭空清空。
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
	// Status 是最后一次观察到的 aria2 状态（active/waiting/paused/complete/error）。
	// 空串 = 升级前的老记录，从没写过状态。
	Status     string `json:"status"`
	Total      int64  `json:"total"`
	Completed  int64  `json:"completed"`
	Error      string `json:"error"`
	UpdatedAt  int64  `json:"updatedAt"`
	FinishedAt int64  `json:"finishedAt"`
}

// 终态常量。字符串与 aria2 的 status 保持一致，前端不必再翻译一套。
const (
	DlStatusComplete = "complete"
	DlStatusError    = "error"
)

// UpsertDlTaskMeta 建任务时登记元数据。
//
// 用 UPSERT 而不是 INSERT OR REPLACE：会话恢复后同一个 gid 会被重新提交，
// REPLACE 是先删后插，会把已经写好的终态、created_at 一起抹掉——历史记录会退化成
// 「刚创建、状态未知」。冲突时只刷新会变的那几列。
func (d *Database) UpsertDlTaskMeta(t DlTask) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`INSERT INTO dl_tasks (gid, url, name, save_dir, source, created_at, status, total, completed, error, updated_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(gid) DO UPDATE SET
		   url        = excluded.url,
		   name       = CASE WHEN excluded.name <> '' THEN excluded.name ELSE dl_tasks.name END,
		   save_dir   = CASE WHEN excluded.save_dir <> '' THEN excluded.save_dir ELSE dl_tasks.save_dir END,
		   source     = excluded.source,
		   updated_at = excluded.updated_at`,
		t.GID, t.URL, t.Name, t.SaveDir, t.Source, t.CreatedAt,
		t.Status, t.Total, t.Completed, t.Error, t.UpdatedAt, t.FinishedAt,
	)
	return err
}

// UpdateDlTaskState 记录一次状态观察。finishedAt 只在终态时传非零，其余传 0 保留原值。
func (d *Database) UpdateDlTaskState(gid, status string, total, completed int64, errMsg string, updatedAt, finishedAt int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`UPDATE dl_tasks SET status = ?, total = ?, completed = ?, error = ?, updated_at = ?,
		        finished_at = CASE WHEN ? > 0 THEN ? ELSE finished_at END
		 WHERE gid = ?`,
		status, total, completed, errMsg, updatedAt, finishedAt, finishedAt, gid,
	)
	return err
}

func (d *Database) ListDlTasks() ([]DlTask, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT gid, url, name, save_dir, source, created_at, status, total, completed, error, updated_at, finished_at
		 FROM dl_tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DlTask{}
	for rows.Next() {
		var t DlTask
		if err := rows.Scan(&t.GID, &t.URL, &t.Name, &t.SaveDir, &t.Source, &t.CreatedAt,
			&t.Status, &t.Total, &t.Completed, &t.Error, &t.UpdatedAt, &t.FinishedAt); err != nil {
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

// DeleteDlFinished 删除全部终态记录：引擎没起来时「清除已结束」也得好使，
// 否则重启后那一整屏历史记录就成了删不掉的东西。
func (d *Database) DeleteDlFinished() (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`DELETE FROM dl_tasks WHERE status IN ('complete', 'error', 'removed', '')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneDlTasks 只保留最近 keep 条历史，防止任务表无限增长。
// 保留集合按 created_at 倒序取；正在进行的任务可能很老（挂了几天的种子），
// 所以删除范围只限终态/未知状态，别把还在跑的任务的记录铲掉。
func (d *Database) PruneDlTasks(keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(
		`DELETE FROM dl_tasks WHERE status IN ('complete', 'error', 'removed', '') AND gid NOT IN (
		   SELECT gid FROM dl_tasks ORDER BY created_at DESC LIMIT ?
		 )`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
