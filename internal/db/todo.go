package db

import (
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
	Tags         string `json:"tags"`        // JSON 数组字符串，如 ["工作","紧急"]
	Recurrence   string `json:"recurrence"`  // JSON：{"kind":"daily|weekly|monthly","timeOfDay":"09:00","weekdays":"1,2,3"}；none/空=不重复
	ParentID     string `json:"parentId"`    // 子任务所属父待办 ID；空=顶层待办
	Status       string `json:"status"`      // todo | doing | done（权威字段），done 由其派生
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
	_ = d.conn.QueryRow("SELECT COALESCE(MAX(sort), 0) FROM todos WHERE done = 0").Scan(&maxSort)

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
		 FROM todos ORDER BY done ASC, sort ASC, created_at DESC`,
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
		t.Done = done != 0
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
	t.Done = done != 0
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
	_, err := d.conn.Exec(
		`UPDATE todos
		 SET title = ?, priority = ?, due_date = ?, note = ?, start_time = ?, end_time = ?, reminder_time = ?, recurrence = ?, tags = ?, status = ?, done = ?
		 WHERE id = ?`,
		title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags, status, done, id,
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
	_, err := d.conn.Exec(
		`UPDATE todos SET status = ?, done = ?, completed_at = CASE WHEN ? = 'done' THEN ? ELSE completed_at END WHERE id = ?`,
		status, done, status, time.Now().Format(time.RFC3339), id,
	)
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

// ClearCompletedTodos 清除所有已完成项
func (d *Database) ClearCompletedTodos() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM todos WHERE done = 1")
	return err
}

// ListDueReminders 返回「已到提醒时间、未发送、未完成」的待办（now 格式 YYYY-MM-DD HH:MM:SS）
func (d *Database) ListDueReminders(now string) ([]Todo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT id, title, done, priority, due_date, note, start_time, end_time, reminder_time, reminder_sent, tags, recurrence, parent_id, status, sort, created_at, completed_at
		 FROM todos
		 WHERE reminder_time <> '' AND reminder_sent = 0 AND done = 0 AND reminder_time <= ?
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
		t.Done = done != 0
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
