package db

import (
	"sort"
	"strconv"
	"strings"
)

// focus_sessions 记录一轮专注会话：结束（到点完成）时落一行。
//
// 与互链边表同样的取舍：title 是结束那一刻抓下来的标题快照，不 join todos 表。
// 待办会被改标题、会被硬删，若统计时回查主体，历史要么跟着改名（读不出当时专注的是
// 什么），要么因目标不存在而缺行。留一份快照才能让一条历史独立读懂。
//
// done=1 才是「真正走完」的会话，统计口径（FocusStats / 前端今日次数）只认它；
// 中途放弃由前端决定不落库（连 localStorage 历史也不写），库里因此只有完成会话。

// FocusSession 一条专注会话。json tag 用 camelCase：Wails 序列化 ApiResult.Data
// 时直接按 tag 输出，前端拿到的就是 todoId/startedAt/... 而非 snake_case。
type FocusSession struct {
	ID        string `json:"id"`
	TodoID    string `json:"todoId"`
	Title     string `json:"title"`
	Minutes   int    `json:"minutes"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
	Done      bool   `json:"done"`
	CreatedAt string `json:"createdAt"`
}

// FocusTodoStat / FocusDayStat / FocusStatsData 是 FocusStats 的汇总结果。
// 拆成具名结构而不是直接吐 map：db 层给出确定的字段与类型，service 层再按前端
// 约定组装成 {sessions, minutes, byTodo, daily} 的 map（见 services/focus.go）。
type FocusTodoStat struct {
	TodoID   string `json:"todoId"`
	Title    string `json:"title"`
	Minutes  int    `json:"minutes"`
	Sessions int    `json:"sessions"`
}

type FocusDayStat struct {
	Day      string `json:"day"`
	Minutes  int    `json:"minutes"`
	Sessions int    `json:"sessions"`
}

type FocusStatsData struct {
	Sessions int             `json:"sessions"`
	Minutes  int             `json:"minutes"`
	ByTodo   []FocusTodoStat `json:"byTodo"`
	Daily    []FocusDayStat  `json:"daily"`
}

const focusCols = `id, todo_id, title, minutes, started_at, ended_at, done, created_at`

func focusSessionFromMap(m map[string]interface{}) FocusSession {
	return FocusSession{
		ID:        str(m["id"]),
		TodoID:    str(m["todo_id"]),
		Title:     str(m["title"]),
		Minutes:   integer(m["minutes"]),
		StartedAt: str(m["started_at"]),
		EndedAt:   str(m["ended_at"]),
		Done:      integer(m["done"]) != 0,
		CreatedAt: str(m["created_at"]),
	}
}

// SaveFocusSession 落一行会话。id 与 created_at 在 db 层生成（照 AddEntityLink
// 的写法），其余字段由 service 层校验后原样带入；返回落库行。
func (d *Database) SaveFocusSession(todoID, title, startedAt, endedAt string, minutes int, done bool) (FocusSession, error) {
	doneInt := 0
	if done {
		doneInt = 1
	}
	s := FocusSession{
		ID:        newID(),
		TodoID:    todoID,
		Title:     title,
		Minutes:   minutes,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Done:      done,
		CreatedAt: now(),
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(
		`INSERT INTO focus_sessions (id, todo_id, title, minutes, started_at, ended_at, done, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.TodoID, s.Title, s.Minutes, s.StartedAt, s.EndedAt, doneInt, s.CreatedAt,
	); err != nil {
		return FocusSession{}, err
	}
	return s, nil
}

// ListFocusSessions 列出 [from, to] 内开始的全部会话（含 done=0，明细不筛完成态，
// 便于将来回看放弃记录），按开始时间倒序。
//
// 时间串可能是 RFC3339 带 ±HH:MM 偏移（与 now() 一致）或带 Z 的 ISO 串，表示不同
// 时裸字符串比较会失序；统一用 julianday() 归一到绝对时间再比较、排序。
// from/to 为空串表示该侧不设边界——「全部」档据此一次取回所有记录。
func (d *Database) ListFocusSessions(from, to string) ([]FocusSession, error) {
	var clauses []string
	var args []interface{}
	if from != "" {
		clauses = append(clauses, `julianday(started_at) >= julianday(?)`)
		args = append(args, from)
	}
	if to != "" {
		clauses = append(clauses, `julianday(started_at) <= julianday(?)`)
		args = append(args, to)
	}
	q := `SELECT ` + focusCols + ` FROM focus_sessions`
	if len(clauses) > 0 {
		q += ` WHERE ` + strings.Join(clauses, ` AND `)
	}
	q += ` ORDER BY julianday(started_at) DESC, id DESC`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]FocusSession, 0, len(rows))
	for _, r := range rows {
		out = append(out, focusSessionFromMap(r))
	}
	return out, nil
}

// FocusStats 汇总最近 days 天内「完成」的会话：总次数、总分钟、按待办、按天。
//
// 时间边界用 julianday('now', '-N days') 而不是拼本地时间字符串：库里时间串带
// 时区偏移时，字符串比较同样会失序，交给 SQLite 统一算绝对时间最稳。
// 按天分组用 date(started_at,'localtime')，让东八区凌晨开始的会话仍归到当天。
func (d *Database) FocusStats(days int) (FocusStatsData, error) {
	out := FocusStatsData{
		ByTodo: []FocusTodoStat{},
		Daily:  []FocusDayStat{},
	}
	rows, err := d.Query(
		`SELECT todo_id, title, minutes, date(started_at, 'localtime') AS day
		 FROM focus_sessions
		 WHERE done = 1 AND julianday(started_at) >= julianday('now', ?)
		 ORDER BY julianday(started_at) DESC, id DESC`,
		`-`+strconv.Itoa(days)+` days`,
	)
	if err != nil {
		return out, err
	}
	byTodo := make(map[string]*FocusTodoStat)
	daily := make(map[string]*FocusDayStat)
	for _, r := range rows {
		mins := integer(r["minutes"])
		todoID := str(r["todo_id"])
		day := str(r["day"])
		out.Sessions++
		out.Minutes += mins
		// 行按开始时间倒序，同一 todo 第一次出现即其最近一次会话，标题快照取它，
		// 避免同一待办改过标题后被拆成多行、或显示成更早的旧名。
		if ts, ok := byTodo[todoID]; ok {
			ts.Minutes += mins
			ts.Sessions++
		} else {
			byTodo[todoID] = &FocusTodoStat{
				TodoID: todoID, Title: str(r["title"]), Minutes: mins, Sessions: 1,
			}
		}
		if ds, ok := daily[day]; ok {
			ds.Minutes += mins
			ds.Sessions++
		} else {
			daily[day] = &FocusDayStat{Day: day, Minutes: mins, Sessions: 1}
		}
	}
	for _, v := range byTodo {
		out.ByTodo = append(out.ByTodo, *v)
	}
	for _, v := range daily {
		out.Daily = append(out.Daily, *v)
	}
	// 按任务累计分钟多的在前；按天日期新的在前。map 遍历无序，必须显式排序，
	// 否则同一份数据每次刷新顺序都可能变。
	sort.Slice(out.ByTodo, func(i, j int) bool { return out.ByTodo[i].Minutes > out.ByTodo[j].Minutes })
	sort.Slice(out.Daily, func(i, j int) bool { return out.Daily[i].Day > out.Daily[j].Day })
	return out, nil
}
