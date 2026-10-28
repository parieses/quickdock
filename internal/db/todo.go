package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Todo 待办任务
type Todo struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Done         bool   `json:"done"`
	Priority     string `json:"priority"` // none | low | medium | high
	DueDate      string `json:"dueDate"`  // '' 或 YYYY-MM-DD
	Note         string `json:"note"`
	StartTime    string `json:"startTime"`    // '' 或 YYYY-MM-DD HH:MM:SS
	EndTime      string `json:"endTime"`      // '' 或 YYYY-MM-DD HH:MM:SS
	ReminderTime string `json:"reminderTime"` // '' 或 YYYY-MM-DD HH:MM:SS
	ReminderSent bool   `json:"reminderSent"`
	Tags         string `json:"tags"`       // JSON 数组字符串，如 ["工作","紧急"]
	Recurrence   string `json:"recurrence"` // JSON：{"kind":"daily|weekly|monthly","timeOfDay":"09:00","weekdays":"1,2,3"}；none/空=不重复
	ParentID     string `json:"parentId"`   // 子任务所属父待办 ID；空=顶层待办
	Status       string `json:"status"`     // todo | doing | done（权威字段），done 由其派生
	Sort         int    `json:"sort"`
	CreatedAt    string `json:"createdAt"`
	CompletedAt  string `json:"completedAt"`
}

// CreateTodo 新建待办（置为未完成，排在未完成列表末尾）
func (d *Database) CreateTodo(title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags string) (*Todo, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("标题不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var maxSort int
	// 排序基准用 status（权威字段）而非 done 列，避免两字段不一致时新待办插到错误位置
	_ = d.conn.QueryRow("SELECT COALESCE(MAX(sort), 0) FROM todos WHERE status <> 'done'").Scan(&maxSort)

	t := &Todo{
		ID:           newID(),
		Title:        title,
		Done:         false,
		Priority:     priority,
		DueDate:      dueDate,
		Note:         note,
		StartTime:    startTime,
		EndTime:      endTime,
		ReminderTime: reminderTime,
		ReminderSent: false,
		Tags:         tags,
		Recurrence:   recurrence,
		ParentID:     "",
		Status:       "todo",
		Sort:         maxSort + 1,
		CreatedAt:    time.Now().Format(time.RFC3339),
	}
	_, err := d.conn.Exec(
		`INSERT INTO todos
			(id, title, done, priority, due_date, note, start_time, end_time, reminder_time, reminder_sent, tags, recurrence, parent_id, status, sort, created_at, completed_at)
		 VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 'todo', ?, ?, '')`,
		t.ID, t.Title, t.Priority, t.DueDate, t.Note, t.StartTime, t.EndTime, t.ReminderTime, t.Tags, t.Recurrence, t.ParentID, t.Sort, t.CreatedAt,
	)
	return t, err
}

// CreateSubtask 新建子任务（单层级 checklist 项，归属指定父待办）
func (d *Database) CreateSubtask(parentID, title string) (*Todo, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("子任务标题不能为空")
	}
	if parentID == "" {
		return nil, fmt.Errorf("父待办不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var maxSort int
	_ = d.conn.QueryRow("SELECT COALESCE(MAX(sort), 0) FROM todos").Scan(&maxSort)

	t := &Todo{
		ID:        newID(),
		Title:     title,
		Done:      false,
		Priority:  "none",
		ParentID:  parentID,
		Status:    "todo",
		Sort:      maxSort + 1,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	_, err := d.conn.Exec(
		`INSERT INTO todos
			(id, title, done, priority, due_date, note, start_time, end_time, reminder_time, reminder_sent, tags, recurrence, parent_id, status, sort, created_at, completed_at)
		 VALUES (?, ?, 0, 'none', '', '', '', '', '', 0, '', '', ?, 'todo', ?, ?, '')`,
		t.ID, t.Title, t.ParentID, t.Sort, t.CreatedAt,
	)
	return t, err
}

// ListTodos 按 未完成在前、已完成在后、再按 sort 排序返回
func (d *Database) ListTodos() ([]Todo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT id, title, done, priority, due_date, note, start_time, end_time, reminder_time, reminder_sent, tags, recurrence, parent_id, status, sort, created_at, completed_at
		 FROM todos ORDER BY CASE WHEN status = 'done' THEN 1 ELSE 0 END ASC, sort ASC, created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var todos []Todo
	for rows.Next() {
		var t Todo
		var done, reminderSent int
		if err := rows.Scan(&t.ID, &t.Title, &done, &t.Priority, &t.DueDate, &t.Note,
			&t.StartTime, &t.EndTime, &t.ReminderTime, &reminderSent, &t.Tags, &t.Recurrence, &t.ParentID, &t.Status, &t.Sort, &t.CreatedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		t.Done = t.Status == "done" // 由 status 派生，避免与 done 列口径不一致
		t.ReminderSent = reminderSent != 0
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

// GetTodo 按 ID 查询
func (d *Database) GetTodo(id string) (*Todo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(
		`SELECT id, title, done, priority, due_date, note, start_time, end_time, reminder_time, reminder_sent, tags, recurrence, parent_id, status, sort, created_at, completed_at
		 FROM todos WHERE id = ?`, id)
	var t Todo
	var done, reminderSent int
	if err := row.Scan(&t.ID, &t.Title, &done, &t.Priority, &t.DueDate, &t.Note,
		&t.StartTime, &t.EndTime, &t.ReminderTime, &reminderSent, &t.Tags, &t.Recurrence, &t.ParentID, &t.Status, &t.Sort, &t.CreatedAt, &t.CompletedAt); err != nil {
		return nil, err
	}
	t.Done = t.Status == "done" // 由 status 派生，避免与 done 列口径不一致
	t.ReminderSent = reminderSent != 0
	return &t, nil
}

// UpdateTodo 更新待办内容（含起止时间与提醒时间、标签、重复配置）
func (d *Database) UpdateTodo(id, title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags, status string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("标题不能为空")
	}
	if status == "" {
		status = "todo"
	}
	done := 0
	if status == "done" {
		done = 1
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	// 先读旧值：SQLite 的 UPDATE ... SET 里引用同名列时语义不直观（取决于赋值顺序），
	// 这两件事又都必须基于「改之前」的值判断，索性读出来在 Go 里算。
	var oldReminder, oldCompletedAt string
	var oldSent int
	if err := d.conn.QueryRow(
		"SELECT reminder_time, reminder_sent, completed_at FROM todos WHERE id = ?", id,
	).Scan(&oldReminder, &oldSent, &oldCompletedAt); err != nil {
		return err
	}
	// 提醒时间被改过就复位 reminder_sent：提醒器只扫 reminder_sent=0 的条目，
	// 不复位等于「这条提醒响过一次，用户改了时间想让它再响一次，却永远不会响」。
	sent := oldSent
	if oldReminder != reminderTime {
		sent = 0
	}
	// 完成时间与 status 严格绑定（与 SetTodoStatus 同口径）：
	//   - 改成 done：沿用已有的完成时间，没有才写当前时间（"完成于"应是首次完成时刻，
	//     而不是每次点保存都刷新）；
	//   - 改成非 done：清空，避免「未完成却有完成时间」。
	completedAt := ""
	if status == "done" {
		completedAt = oldCompletedAt
		if completedAt == "" {
			completedAt = time.Now().Format(time.RFC3339)
		}
	}

	_, err := d.conn.Exec(
		`UPDATE todos
		 SET title = ?, priority = ?, due_date = ?, note = ?, start_time = ?, end_time = ?, reminder_time = ?, recurrence = ?, tags = ?, status = ?, done = ?, reminder_sent = ?, completed_at = ?
		 WHERE id = ?`,
		title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags, status, done, sent, completedAt, id,
	)
	return err
}

// SetTodoStatus 设置待办状态（kanban 拖拽用），done 由 status 派生
func (d *Database) SetTodoStatus(id, status string) error {
	if status != "todo" && status != "doing" && status != "done" {
		return fmt.Errorf("非法状态: %s", status)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	done := 0
	if status == "done" {
		done = 1
	}
	// completed_at 与 status 严格绑定：改为非完成态时清空，避免出现「未完成但有完成时间」
	_, err := d.conn.Exec(
		`UPDATE todos SET status = ?, done = ?, completed_at = CASE WHEN ? = 'done' THEN ? ELSE '' END WHERE id = ?`,
		status, done, status, time.Now().Format(time.RFC3339), id,
	)
	return err
}

// SetTodoSort 只更新排序权重（看板拖拽落位 / 子任务上下移）。
// 不复用 UpdateTodo：那条语句要传十几个字段、还会复位 reminder_sent，
// 为了挪一个位置把提醒重置掉，副作用太远。
func (d *Database) SetTodoSort(id string, sort int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE todos SET sort = ? WHERE id = ?`, sort, id)
	return err
}

// ToggleTodo 切换完成状态（status 权威：done↔todo），并记录完成时间
func (d *Database) ToggleTodo(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var status string
	if err := d.conn.QueryRow("SELECT status FROM todos WHERE id = ?", id).Scan(&status); err != nil {
		return err
	}
	newDone := 0
	newStatus := "todo"
	completedAt := ""
	if status != "done" {
		newDone = 1
		newStatus = "done"
		completedAt = time.Now().Format(time.RFC3339)
	}
	_, err := d.conn.Exec("UPDATE todos SET done = ?, status = ?, completed_at = ? WHERE id = ?", newDone, newStatus, completedAt, id)
	return err
}

// DeleteTodo 删除待办（同时级联删除其子任务）
func (d *Database) DeleteTodo(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM todos WHERE id = ? OR parent_id = ?", id, id)
	return err
}

// SearchTodos 按标题 / 备注 / 标签模糊检索待办（命令面板用）。
//
// 只回未完成的顶层任务：面板里的「待办」是"接下来要做什么"的入口，
// 已完成的条目和子任务混进来只会把真要找的那条挤下去。
// 排序列故意不含未完成的重排逻辑——沿用 ListTodos 的 sort/created_at 顺序即可，
// 面板另有 frecency 加权。
func (d *Database) SearchTodos(q string, limit int) ([]Todo, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	pattern := "%" + escapeLike(q) + "%"
	rows, err := d.conn.Query(
		`SELECT id, title, done, priority, due_date, note, start_time, end_time, reminder_time, reminder_sent, tags, recurrence, parent_id, status, sort, created_at, completed_at
		 FROM todos
		 WHERE parent_id = '' AND status <> 'done'
		   AND (title LIKE ? ESCAPE '\' OR note LIKE ? ESCAPE '\' OR tags LIKE ? ESCAPE '\')
		 ORDER BY sort ASC, created_at DESC
		 LIMIT ?`,
		pattern, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTodos(rows)
}

// scanTodos 把查询结果扫成 []Todo（ListTodos 之后新增的查询共用）。
func scanTodos(rows *sql.Rows) ([]Todo, error) {
	var todos []Todo
	for rows.Next() {
		var t Todo
		var done, reminderSent int
		if err := rows.Scan(&t.ID, &t.Title, &done, &t.Priority, &t.DueDate, &t.Note,
			&t.StartTime, &t.EndTime, &t.ReminderTime, &reminderSent, &t.Tags, &t.Recurrence, &t.ParentID, &t.Status, &t.Sort, &t.CreatedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		t.Done = t.Status == "done" // 由 status 派生，避免与 done 列口径不一致
		t.ReminderSent = reminderSent != 0
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

// escapeLike 转义 LIKE 通配符：用户搜字面下划线/百分号时不应退化成"匹配任意"。
// SQLite 无默认转义符，必须在每个 LIKE 后写 ESCAPE '\' 才生效。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// ClearCompletedTodos 清除所有已完成项（连同它们的子任务）
//
// 子任务几乎总是 "todo" 状态（父任务做完了，勾选项未必逐条勾过），只删
// status='done' 会把它们留成**孤儿行**：列表按父行分组渲染、搜索也只返回顶层任务，
// 于是这些行既看不见也搜不到，只会在数据库里一直躺着。
func (d *Database) ClearCompletedTodos() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(
		"DELETE FROM todos WHERE parent_id <> '' AND parent_id IN (SELECT id FROM todos WHERE status = 'done')",
	); err != nil {
		return err
	}
	_, err := d.conn.Exec("DELETE FROM todos WHERE status = 'done'")
	return err
}

// ListDueReminders 返回「已到提醒时间、未发送、未完成」的待办（now 格式 YYYY-MM-DD HH:MM:SS）
func (d *Database) ListDueReminders(now string) ([]Todo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT id, title, done, priority, due_date, note, start_time, end_time, reminder_time, reminder_sent, tags, recurrence, parent_id, status, sort, created_at, completed_at
		 FROM todos
		 WHERE reminder_time <> '' AND reminder_sent = 0 AND status <> 'done' AND reminder_time <= ?
		 ORDER BY reminder_time ASC`,
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var todos []Todo
	for rows.Next() {
		var t Todo
		var done, reminderSent int
		if err := rows.Scan(&t.ID, &t.Title, &done, &t.Priority, &t.DueDate, &t.Note,
			&t.StartTime, &t.EndTime, &t.ReminderTime, &reminderSent, &t.Tags, &t.Recurrence, &t.ParentID, &t.Status, &t.Sort, &t.CreatedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		t.Done = t.Status == "done" // 由 status 派生，避免与 done 列口径不一致
		t.ReminderSent = reminderSent != 0
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

// MarkReminderSent 标记提醒已发送（避免重复提醒）
func (d *Database) MarkReminderSent(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE todos SET reminder_sent = 1 WHERE id = ?", id)
	return err
}
