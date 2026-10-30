package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 后台任务的取值。写成常量而不是裸字符串：面板、清理、退出钩子三处都要判它，
// 一处写成 "done" 另一处写成 "finished"，就会出现「状态是失败但退出码是 0」
// 这种谁也解释不了的组合。
const (
	AIJobRunning = "running"
	AIJobDone    = "done"
	AIJobFailed  = "failed"
	AIJobKilled  = "killed"
)

// AIJob 模型用 run_command(background=true) 起的一次临时进程。
//
// 输出不进库（见 schema.go 里 log_path 的说明）：一条 build 的输出可以几十 MB，
// 塞进库里会让备份、VACUUM、回收站快照全部变慢，而它本来只是「让人能翻一下」。
type AIJob struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	// Status 见上面 AIJob* 常量。
	Status string `json:"status"`
	// ExitCode 只有进程真的退出过才有意义；被宿主杀掉时保持 0。
	ExitCode int    `json:"exit_code"`
	LogPath  string `json:"log_path"`
	// PID 进程组里主进程的 pid：宿主退出时要靠它整组杀掉。
	PID int `json:"pid"`
	// Error 启动失败或异常退出的原因；正常结束为空。
	Error     string `json:"error"`
	StartedAt string `json:"started_at"`
	// EndedAt 空表示还在跑。
	EndedAt string `json:"ended_at"`
}

// CreateAIJob 先落一行 running 再启动进程。
//
// 顺序不能反：进程起得比这行快的话，一个秒退的命令会在「还没有记录」的瞬间
// 就去更新状态，更新影响 0 行，于是库里永远留着一个 running 的幽灵任务。
func (d *Database) CreateAIJob(j AIJob) error {
	if j.StartedAt == "" {
		j.StartedAt = time.Now().Format(time.RFC3339)
	}
	if j.Status == "" {
		j.Status = AIJobRunning
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`INSERT INTO ai_jobs (id, command, cwd, status, exit_code, log_path, pid, error, started_at, ended_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Command, j.Cwd, j.Status, j.ExitCode, j.LogPath, j.PID, j.Error, j.StartedAt, j.EndedAt)
	if err != nil {
		return fmt.Errorf("创建后台任务失败: %w", err)
	}
	return nil
}

// SetAIJobPID 进程起来之后回填 pid。
//
// 与 CreateAIJob 分两步：pid 只有 Start() 之后才有，而记录必须先插（见上）。
func (d *Database) SetAIJobPID(id string, pid int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE ai_jobs SET pid=? WHERE id=?`, pid, id)
	return err
}

// FinishAIJob 结束一个任务。重复调用是幂等的（只改还在 running 的那一行）。
func (d *Database) FinishAIJob(id, status string, exitCode int, errMsg string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`UPDATE ai_jobs SET status=?, exit_code=?, error=?, ended_at=?
		 WHERE id=? AND status=?`,
		status, exitCode, errMsg, time.Now().Format(time.RFC3339), id, AIJobRunning)
	return err
}

// GetAIJob 取一条任务；不存在返回 (nil, nil)。
func (d *Database) GetAIJob(id string) (*AIJob, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	row := d.conn.QueryRow(
		`SELECT id, command, cwd, status, exit_code, log_path, pid, error, started_at, ended_at
		 FROM ai_jobs WHERE id=?`, id)
	var j AIJob
	if err := row.Scan(&j.ID, &j.Command, &j.Cwd, &j.Status, &j.ExitCode,
		&j.LogPath, &j.PID, &j.Error, &j.StartedAt, &j.EndedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &j, nil
}

// ListRunningAIJobs 还在跑的任务。
//
// 启动时用它把「上次退出没来得及收尾」的行改掉：宿主被强杀时没有任何机会
// 更新状态，库里会留下一堆永远 running 的行，而模型查到 running 就会一直等。
func (d *Database) ListRunningAIJobs() ([]AIJob, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(
		`SELECT id, command, cwd, status, exit_code, log_path, pid, error, started_at, ended_at
		 FROM ai_jobs WHERE status=? ORDER BY started_at DESC`, AIJobRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIJob{}
	for rows.Next() {
		var j AIJob
		if err := rows.Scan(&j.ID, &j.Command, &j.Cwd, &j.Status, &j.ExitCode,
			&j.LogPath, &j.PID, &j.Error, &j.StartedAt, &j.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// PurgeAIJobs 清掉结束时间早于 before 的任务，返回被清掉的日志路径。
//
// 返回路径而不是自己删文件：db 层不碰数据目录（目录布局由 services/ai 决定），
// 两处各自负责自己那一半。
func (d *Database) PurgeAIJobs(before time.Time) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cutoff := before.Format(time.RFC3339)
	rows, err := d.conn.Query(
		`SELECT log_path FROM ai_jobs
		 WHERE ended_at <> '' AND julianday(ended_at) < julianday(?)`, cutoff)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 时间比较用 julianday：ended_at 是带时区偏移的 RFC3339，跨时区时字典序
	// 不再等于时间序（墓碑那次的教训）。
	if _, err := d.conn.Exec(
		`DELETE FROM ai_jobs WHERE ended_at <> '' AND julianday(ended_at) < julianday(?)`,
		cutoff); err != nil {
		return nil, err
	}
	return paths, nil
}
