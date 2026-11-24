package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// Script 脚本元数据（青龙式脚本任务）。
//
// 正文与依赖文件放磁盘 dataDir/scripts/<id>/ 下，本表只存索引与运行策略。
// 刻意不存 content：脚本天然需要工作目录（requirements.txt / data.json / 读同目录
// 配置），并且用户会用外部编辑器改它——双真相源只会在「外部改完」之后被库里的
// 旧内容反向覆盖，而那种丢失没有任何报错。
type Script struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Lang 决定默认解释器与编辑器语言：python | javascript | shell | custom。
	// custom 必须自带 Interpreter，否则运行时无处可查。
	Lang string `json:"lang"`
	// Entry 入口文件（相对脚本目录，如 main.py）。允许子目录，防止路径逃逸由服务层校验。
	Entry string `json:"entry"`
	// Interpreter 覆盖解释器（绝对路径或裸命令名）。空 = 走三级解析：
	// 已装便携运行时 → PATH → 报错并引导去环境管理。
	Interpreter string `json:"interpreter"`
	// DefaultArgs 默认参数（JSON 数组字符串，如 ["--days","7"]）。
	// 用 JSON 而不是空格分隔：路径带空格时后者必然出错，而这正是 Windows 的日常。
	DefaultArgs string `json:"defaultArgs"`
	// TimeoutSec 单次运行上限，0 = 不限。签到类脚本跑四十分钟很正常，
	// 默认超时会把它砍在半路，故必须允许 0。
	TimeoutSec int `json:"timeoutSec"`
	// Concurrency 同一脚本允许同时运行几个实例。
	//
	// 默认 1（互斥）：签到类脚本并发跑两次就是两次重复提交，还可能同时写
	// 同一个 state.json。抓取/转码类脚本则相反——并行反而更快，所以开放到 8。
	// 上限 8 而不是无限：每个实例都是真实进程，开太多会把这台机器拖死。
	Concurrency int `json:"concurrency"`
	// AutoInstall 运行前若存在 requirements.txt / package.json 且依赖未装，先装一次。
	AutoInstall bool `json:"autoInstall"`
	Pinned      bool `json:"pinned"`
	// MCPAllowed 允许外部 AI 客户端（MCP）触发本脚本。默认关：
	// 跑脚本等于任意代码执行，必须由用户逐个脚本明确点头。
	MCPAllowed bool   `json:"mcpAllowed"`
	Tags       string `json:"tags"` // CSV
	// 以下为运行聚合，由 FinishScriptRun 维护，前端列表直接用，不必二次查询。
	RunCount       int    `json:"runCount"`
	FailCount      int    `json:"failCount"`
	LastRun        string `json:"lastRun"`
	LastStatus     string `json:"lastStatus"`
	LastDurationMs int64  `json:"lastDurationMs"`
	Sort           int    `json:"sort"`
	// WatchPath 事件触发监听的路径（文件或目录，空 = 不监听）。
	// 与本机自动化中枢的定位配套：外部程序往某个目录丢文件，脚本自动处理。
	WatchPath string `json:"watchPath"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

const scriptCols = `id, name, description, lang, entry, interpreter, default_args,
	timeout_sec, concurrency, auto_install, pinned, mcp_allowed, tags,
	run_count, fail_count, last_run, last_status, last_duration_ms,
	sort, watch_path, created_at, updated_at`

func scanScript(row interface{ Scan(...interface{}) error }) (Script, error) {
	var s Script
	var autoInstall, pinned, mcpAllowed int
	err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Lang, &s.Entry, &s.Interpreter,
		&s.DefaultArgs, &s.TimeoutSec, &s.Concurrency, &autoInstall, &pinned, &mcpAllowed,
		&s.Tags, &s.RunCount, &s.FailCount, &s.LastRun, &s.LastStatus, &s.LastDurationMs,
		&s.Sort, &s.WatchPath, &s.CreatedAt, &s.UpdatedAt)
	s.AutoInstall = autoInstall != 0
	s.Pinned = pinned != 0
	s.MCPAllowed = mcpAllowed != 0
	return s, err
}

// normalizeScript 归一化必填项与越界值。
//
// 越界只可能是前端漏传或手改库，为此让整条保存失败不值得；但也不能放任
// （timeout 存成负数会让 time.After 立刻触发，脚本永远「超时」）。
func normalizeScript(s *Script) {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		s.Name = "未命名脚本"
	}
	s.Description = strings.TrimSpace(s.Description)
	s.Lang = strings.ToLower(strings.TrimSpace(s.Lang))
	switch s.Lang {
	case "python", "javascript", "shell", "go", "php", "custom":
	default:
		s.Lang = "python"
	}
	if strings.TrimSpace(s.Entry) == "" {
		s.Entry = defaultEntryForLang(s.Lang)
	}
	if s.TimeoutSec < 0 {
		s.TimeoutSec = 0
	}
	// 上限 24 小时：再长就不是「脚本任务」而是常驻服务，该走站点/服务那条线。
	if s.TimeoutSec > 86400 {
		s.TimeoutSec = 86400
	}
	if s.Concurrency < 1 {
		s.Concurrency = 1
	}
	if s.Concurrency > 8 {
		s.Concurrency = 8
	}
	if s.DefaultArgs == "" {
		s.DefaultArgs = "[]"
	}
}

func defaultEntryForLang(lang string) string {
	switch lang {
	case "javascript":
		return "main.js"
	case "shell":
		return "main.sh"
	case "go":
		return "main.go"
	case "php":
		return "main.php"
	case "custom":
		return "main"
	}
	return "main.py"
}

// DefaultScriptEntry 暴露给服务层：新建脚本时按语言给默认入口文件名。
func DefaultScriptEntry(lang string) string { return defaultEntryForLang(lang) }

// CreateScript 新建脚本
func (d *Database) CreateScript(s *Script) (*Script, error) {
	normalizeScript(s)
	if s.ID == "" {
		s.ID = newID()
	}
	s.CreatedAt = now()
	s.UpdatedAt = s.CreatedAt
	_, err := d.conn.Exec(`INSERT INTO scripts (`+scriptCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.Description, s.Lang, s.Entry, s.Interpreter, s.DefaultArgs,
		s.TimeoutSec, s.Concurrency, b2i(s.AutoInstall), b2i(s.Pinned), b2i(s.MCPAllowed),
		s.Tags, s.RunCount, s.FailCount, s.LastRun, s.LastStatus, s.LastDurationMs,
		s.Sort, s.WatchPath, s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListScripts 列出全部脚本：置顶优先、其次手动排序、最后按创建时间。
func (d *Database) ListScripts() ([]Script, error) {
	rows, err := d.conn.Query(`SELECT ` + scriptCols + ` FROM scripts
		ORDER BY pinned DESC, sort ASC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Script, 0, 16)
	for rows.Next() {
		s, err := scanScript(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetScript 按 id 取脚本。
func (d *Database) GetScript(id string) (*Script, error) {
	s, err := scanScript(d.conn.QueryRow(`SELECT `+scriptCols+` FROM scripts WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("脚本不存在")
		}
		return nil, err
	}
	return &s, nil
}

// UpdateScript 保存脚本元数据（不动运行聚合列，那些只由 FinishScriptRun 写）。
func (d *Database) UpdateScript(s *Script) error {
	normalizeScript(s)
	s.UpdatedAt = now()
	res, err := d.conn.Exec(`UPDATE scripts SET name=?, description=?, lang=?, entry=?,
		interpreter=?, default_args=?, timeout_sec=?, concurrency=?, auto_install=?,
		pinned=?, mcp_allowed=?, tags=?, sort=?, watch_path=?, updated_at=? WHERE id=?`,
		s.Name, s.Description, s.Lang, s.Entry, s.Interpreter, s.DefaultArgs,
		s.TimeoutSec, s.Concurrency, b2i(s.AutoInstall), b2i(s.Pinned), b2i(s.MCPAllowed),
		s.Tags, s.Sort, s.WatchPath, s.UpdatedAt, s.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("脚本不存在")
	}
	return nil
}

// DeleteScript 删除脚本及其变量与运行记录（正文目录由服务层负责移除）。
func (d *Database) DeleteScript(id string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM script_envs WHERE script_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM script_runs WHERE script_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM scripts WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("脚本不存在")
		}
		return nil
	})
}

// SetScriptSort 拖拽排序。
func (d *Database) SetScriptSort(id string, sort int) error {
	return d.ExecuteParams(`UPDATE scripts SET sort = ? WHERE id = ?`, []interface{}{sort, id})
}

// SetScriptPin 置顶开关。
func (d *Database) SetScriptPin(id string, pinned bool) error {
	return d.ExecuteParams(`UPDATE scripts SET pinned = ? WHERE id = ?`, []interface{}{b2i(pinned), id})
}

// ---------- 环境变量 ----------

// ScriptEnv 脚本环境变量。ScriptID 为空表示全局变量（所有脚本可见）。
type ScriptEnv struct {
	ID       string `json:"id"`
	ScriptID string `json:"scriptId"`
	Key      string `json:"key"`
	// Value 明文只在「未加密」时是真实值；secret=1 时这里存 cred:// 占位，
	// 真实值在密码库，运行前由服务层解密。
	Value     string `json:"value"`
	Secret    bool   `json:"secret"`
	Remark    string `json:"remark"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
}

const scriptEnvCols = `id, script_id, env_key, env_value, secret, remark, sort, created_at`

func scanScriptEnv(row interface{ Scan(...interface{}) error }) (ScriptEnv, error) {
	var e ScriptEnv
	var secret int
	err := row.Scan(&e.ID, &e.ScriptID, &e.Key, &e.Value, &secret, &e.Remark, &e.Sort, &e.CreatedAt)
	e.Secret = secret != 0
	return e, err
}

// ListScriptEnvs 取某脚本可见的全部变量：全局在前，脚本级在后（脚本级覆盖全局）。
// scriptID 传空时只返回全局。
func (d *Database) ListScriptEnvs(scriptID string) ([]ScriptEnv, error) {
	rows, err := d.conn.Query(`SELECT `+scriptEnvCols+` FROM script_envs
		WHERE script_id = '' OR script_id = ?
		ORDER BY CASE WHEN script_id = '' THEN 0 ELSE 1 END, sort ASC, env_key ASC`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ScriptEnv, 0, 16)
	for rows.Next() {
		e, err := scanScriptEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertScriptEnv 按 (script_id, env_key) 唯一键写入。
//
// 用 ON CONFLICT 而不是「先删后插」：后者会换掉 id，前端正拿着旧 id 编辑时
// 再保存一次就会报「记录不存在」。唯一索引见 schema.go 的 postMigrations。
func (d *Database) UpsertScriptEnv(e *ScriptEnv) (*ScriptEnv, error) {
	e.Key = strings.TrimSpace(e.Key)
	if e.Key == "" {
		return nil, fmt.Errorf("变量名不能为空")
	}
	// 变量名限制为 shell 友好字符：拼进子进程环境时，含 '=' 或空白的键
	// 会被 exec 直接拒绝（Windows 更严格），而那种报错在运行日志里极难定位。
	for _, r := range e.Key {
		ok := r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return nil, fmt.Errorf("变量名只能包含字母、数字和下划线")
		}
	}
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = now()
	_, err := d.conn.Exec(`INSERT INTO script_envs (`+scriptEnvCols+`)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(script_id, env_key) DO UPDATE SET
			env_value = excluded.env_value,
			secret = excluded.secret,
			remark = excluded.remark,
			sort = excluded.sort`,
		e.ID, e.ScriptID, e.Key, e.Value, b2i(e.Secret), e.Remark, e.Sort, e.CreatedAt)
	if err != nil {
		return nil, err
	}
	// 冲突路径下 id 仍是旧行的，回读一次让调用方拿到真实 id。
	return d.getScriptEnv(e.ScriptID, e.Key)
}

func (d *Database) getScriptEnv(scriptID, key string) (*ScriptEnv, error) {
	e, err := scanScriptEnv(d.conn.QueryRow(`SELECT `+scriptEnvCols+`
		FROM script_envs WHERE script_id = ? AND env_key = ?`, scriptID, key))
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// GetScriptEnv 按 id 取（服务层解密 secret 时用）。
func (d *Database) GetScriptEnv(id string) (*ScriptEnv, error) {
	e, err := scanScriptEnv(d.conn.QueryRow(`SELECT `+scriptEnvCols+` FROM script_envs WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("变量不存在")
		}
		return nil, err
	}
	return &e, nil
}

// DeleteScriptEnv 删除单条变量。
func (d *Database) DeleteScriptEnv(id string) error {
	return d.ExecuteParams(`DELETE FROM script_envs WHERE id = ?`, []interface{}{id})
}

// ---------- 运行记录 ----------

// ScriptRun 一次脚本运行。与 schedule_runs 并存：定时触发两边都写，
// 靠同一个 runID 关联，任务视角和脚本视角都能查到同一次运行。
type ScriptRun struct {
	ID         string `json:"id"`
	ScriptID   string `json:"scriptId"`
	ScriptName string `json:"scriptName"`
	// Trigger manual | cron | http | mcp | ai | event
	Trigger    string `json:"trigger"`
	Status     string `json:"status"` // running | ok | fail | canceled | timeout
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	LogPath    string `json:"logPath"`
	// Summary 末 500 字摘要（通知与列表用）。完整输出在 LogPath。
	Summary string `json:"summary"`
	// AIPatch AI 自愈产出的补丁建议（JSON），空 = 没有建议。绝不自动应用。
	AIPatch    string `json:"aiPatch"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
}

// trigger_kind 而不是 trigger：TRIGGER 是 SQLite 关键字，裸列名会直接语法错误。
const scriptRunCols = `id, script_id, script_name, trigger_kind, status, exit_code,
	duration_ms, log_path, summary, ai_patch, started_at, finished_at`

func scanScriptRun(row interface{ Scan(...interface{}) error }) (ScriptRun, error) {
	var r ScriptRun
	err := row.Scan(&r.ID, &r.ScriptID, &r.ScriptName, &r.Trigger, &r.Status, &r.ExitCode,
		&r.DurationMs, &r.LogPath, &r.Summary, &r.AIPatch, &r.StartedAt, &r.FinishedAt)
	return r, err
}

// InsertScriptRun 落一条 running 记录。
func (d *Database) InsertScriptRun(r *ScriptRun) error {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.StartedAt == "" {
		r.StartedAt = now()
	}
	if r.Status == "" {
		r.Status = "running"
	}
	_, err := d.conn.Exec(`INSERT INTO script_runs (`+scriptRunCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ScriptID, r.ScriptName, r.Trigger, r.Status, r.ExitCode,
		r.DurationMs, r.LogPath, r.Summary, r.AIPatch, r.StartedAt, r.FinishedAt)
	return err
}

// FinishScriptRun 收尾：写终态，并在同一事务里维护 scripts 的运行聚合。
//
// 聚合放在这里而不是由服务层各算一遍：手动跑、定时跑、HTTP 触发、MCP 触发
// 四条路径最终都汇到这一个函数，漏一条就会出现「列表上的成功率和历史对不上」。
// 取消/超时算失败（fail_count +1）：它们同样是「这次没成功」，用户关心的是
// 成功率而不是失败原因的细分——细分在 status 列里。
func (d *Database) FinishScriptRun(runID, status string, exitCode int, durationMs int64, summary string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE script_runs SET status=?, exit_code=?, duration_ms=?,
			summary=?, finished_at=? WHERE id=?`,
			status, exitCode, durationMs, summary, now(), runID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("运行记录不存在")
		}
		var scriptID string
		var startedAt string
		if err := tx.QueryRow(`SELECT script_id, started_at FROM script_runs WHERE id = ?`, runID).
			Scan(&scriptID, &startedAt); err != nil {
			return err
		}
		failInc := 0
		if status != "ok" && status != "running" {
			failInc = 1
		}
		_, err = tx.Exec(`UPDATE scripts SET run_count = run_count + 1,
			fail_count = fail_count + ?, last_run = ?, last_status = ?,
			last_duration_ms = ?, updated_at = ? WHERE id = ?`,
			failInc, startedAt, status, durationMs, now(), scriptID)
		return err
	})
}

// SetScriptRunAIPatch 保存 AI 自愈建议。
func (d *Database) SetScriptRunAIPatch(runID, patch string) error {
	return d.ExecuteParams(`UPDATE script_runs SET ai_patch = ? WHERE id = ?`, []interface{}{patch, runID})
}

// ListScriptRuns 按脚本取运行记录（scriptID 为空 = 全部脚本）。
func (d *Database) ListScriptRuns(scriptID string, limit int) ([]ScriptRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if scriptID == "" {
		rows, err = d.conn.Query(`SELECT `+scriptRunCols+` FROM script_runs
			ORDER BY started_at DESC LIMIT ?`, limit)
	} else {
		rows, err = d.conn.Query(`SELECT `+scriptRunCols+` FROM script_runs
			WHERE script_id = ? ORDER BY started_at DESC LIMIT ?`, scriptID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ScriptRun, 0, limit)
	for rows.Next() {
		r, err := scanScriptRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetScriptRun 按 id 取一次运行。
func (d *Database) GetScriptRun(id string) (*ScriptRun, error) {
	r, err := scanScriptRun(d.conn.QueryRow(`SELECT `+scriptRunCols+` FROM script_runs WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("运行记录不存在")
		}
		return nil, err
	}
	return &r, nil
}

// PruneScriptRuns 每脚本只留最近 keep 条，其余删掉。
//
// 保留上限而不是「永久保留」：脚本可能每分钟跑一次，一年就是 50 万行，
// 而且日志文件也要跟着删——留个指向已删文件的记录只会让用户点开一片空白。
// 返回被删记录（调用方据此清理日志文件）。
func (d *Database) PruneScriptRuns(scriptID string, keep int) ([]ScriptRun, error) {
	if keep < 1 {
		keep = 100
	}
	rows, err := d.conn.Query(`SELECT `+scriptRunCols+` FROM script_runs
		WHERE script_id = ? AND id NOT IN (
			SELECT id FROM script_runs WHERE script_id = ? ORDER BY started_at DESC LIMIT ?
		)`, scriptID, scriptID, keep)
	if err != nil {
		return nil, err
	}
	var stale []ScriptRun
	for rows.Next() {
		r, err := scanScriptRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		stale = append(stale, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range stale {
		if err := d.ExecuteParams(`DELETE FROM script_runs WHERE id = ?`, []interface{}{r.ID}); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

// DeleteAllScriptRuns 清空某脚本的运行历史，返回被删记录（调用方据此删日志文件）。
func (d *Database) DeleteAllScriptRuns(scriptID string) ([]ScriptRun, error) {
	runs, err := d.ListScriptRuns(scriptID, 500)
	if err != nil {
		return nil, err
	}
	if err := d.ExecuteParams(`DELETE FROM script_runs WHERE script_id = ?`, []interface{}{scriptID}); err != nil {
		return nil, err
	}
	return runs, nil
}

// ScriptStats 单脚本的运行统计（前端趋势图与成功率用）。
type ScriptStats struct {
	Total      int     `json:"total"`
	OK         int     `json:"ok"`
	Fail       int     `json:"fail"`
	AvgMs      float64 `json:"avgMs"`
	Last7OK    int     `json:"last7ok"`
	Last7Total int     `json:"last7total"`
}

// GetScriptStats 聚合统计。status='running' 不计入（还没结果）。
func (d *Database) GetScriptStats(scriptID string) (ScriptStats, error) {
	var st ScriptStats
	err := d.conn.QueryRow(`SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('fail','canceled','timeout') THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(CASE WHEN duration_ms > 0 THEN duration_ms END), 0)
		FROM script_runs WHERE script_id = ? AND status <> 'running'`, scriptID).
		Scan(&st.Total, &st.OK, &st.Fail, &st.AvgMs)
	if err != nil {
		return st, err
	}
	// 「最近 7 次」比「最近 7 天」稳定：低频脚本按天算永远是 0，看不出趋势。
	err = d.conn.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM (SELECT status FROM script_runs
			WHERE script_id = ? AND status <> 'running'
			ORDER BY started_at DESC LIMIT 7)`, scriptID).Scan(&st.Last7OK, &st.Last7Total)
	return st, err
}

// FailStaleScriptRuns 进程崩溃后遗留的 running 记录在新一次启动时收尾成 fail。
//
// 不做的话「并发闸门」会永久卡住：上一进程被强杀，running 记录留着，
// 用户点运行永远得到「该脚本正在运行中」，且没有任何界面能清掉它。
func (d *Database) FailStaleScriptRuns() (int, error) {
	res, err := d.conn.Exec(`UPDATE script_runs SET status='fail',
		summary='应用退出导致运行中断', finished_at=? WHERE status='running'`, now())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
