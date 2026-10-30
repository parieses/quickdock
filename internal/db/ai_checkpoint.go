package db

// AI 编辑快照（ai_checkpoints，方案 A3 的存储层）。
//
// 表里只存「哪一次改动动了哪些文件」，文件本体落在
// <dataDir>/ai_checkpoints/<convID>/<seq>/。分开存而不是把内容塞进 payload：
// 一个文件动辄几百 KB，进库会让这一张表比整个库还大，而 SQLite 膨胀到那个量级后
// VACUUM 与备份都会变成用户能感知的卡顿。
//
// 与回收站墓碑的区别：墓碑存「被删掉的整行数据」，这里存「被改掉的旧文件内容」。
// 两者都是「用户可能会后悔的操作」的撤销依据，所以都放在启动清理那一趟里按时间淘汰。

import (
	"fmt"
	"strings"
	"time"
)

const (
	// AICheckpointMaxAgeDays 快照的最长保留天数。
	AICheckpointMaxAgeDays = 7
	// AICheckpointKeepPerConv 每个会话保留的最近回滚点个数。
	//
	// 20 个大约覆盖「一次任务里改了十几处」的完整过程，再往前的快照用户已经
	// 记不清是第几步了，留着只占磁盘。
	AICheckpointKeepPerConv = 20
)

// AICheckpoint 一个回滚点。
type AICheckpoint struct {
	ID         string `json:"id"`
	ConvID     string `json:"conv_id"`
	Seq        int    `json:"seq"`
	ToolCallID string `json:"tool_call_id"`
	Tool       string `json:"tool"`
	// Files 本次改动涉及的文件清单（JSON 数组，见 AICheckpointFile）。
	//
	// 存 JSON 而不是单独一列一个文件：现在一次替换只动一个文件，但同一条调用
	// 将来可能批量改多个，那时不用动表结构。
	Files     string `json:"files"`
	CreatedAt string `json:"created_at"`
}

// AICheckpointFile 快照里的一个文件。
type AICheckpointFile struct {
	Path string `json:"path"`
	// Existed 改动前文件是否存在。
	//
	// 新建的文件回滚时要「删除」而不是「还原」——只记内容不记这一点，回滚后
	// 会给用户留下一个空文件，而那个文件本来不该存在。
	Existed bool `json:"existed"`
	// Mode 原文件权限，还原时一并复原（可执行位丢了会让人一头雾水）。
	Mode uint32 `json:"mode"`
	// Backup 快照文件名（原路径 base64url 编码）。Existed=false 时为空。
	Backup string `json:"backup"`
	// Bytes 原文件大小，用于展示与配额统计。
	Bytes int64 `json:"bytes"`
}

// isUniqueViolation 判断是不是唯一索引冲突。
//
// 不用 sqlite3.Error 类型断言：本项目的连接驱动被封装在 dbConn 里（见 db.go），
// 引入驱动包的具体错误类型会把「以后换驱动」变成一次全局改动。字符串匹配
// 在 SQLite 的三个主流 Go 驱动里都稳定（错误文本都来自 SQLite 本身）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT")
}

// CreateAICheckpoint 写入一个回滚点，返回分配到的 seq。
//
// seq 在会话内单调递增，先 MAX+1 再插入，撞唯一索引就重算重试。
// 并发来源是 spawn 出来的子 agent：它们各自改文件、各自建快照，两个 goroutine
// 同时 MAX+1 必然拿到同一个值。重试 3 次足够——竞争窗口是两次 SQL 之间的微秒级。
func (d *Database) CreateAICheckpoint(c AICheckpoint) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	const maxTries = 3
	for try := 0; try < maxTries; try++ {
		var maxSeq int
		if err := d.conn.QueryRow(
			`SELECT COALESCE(MAX(seq),0) FROM ai_checkpoints WHERE conv_id=?`, c.ConvID).Scan(&maxSeq); err != nil {
			return 0, err
		}
		c.Seq = maxSeq + 1
		_, err := d.conn.Exec(
			`INSERT INTO ai_checkpoints (id, conv_id, seq, tool_call_id, tool, files, created_at)
			 VALUES (?,?,?,?,?,?,?)`,
			c.ID, c.ConvID, c.Seq, c.ToolCallID, c.Tool, c.Files, c.CreatedAt)
		if err == nil {
			return c.Seq, nil
		}
		if !isUniqueViolation(err) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("回滚点编号冲突，重试 %d 次仍失败", maxTries)
}

// ListAICheckpoints 会话的回滚点，最近的在前（seq 倒序）。
//
// 倒序而不是正序：界面上「回滚到上一步」用到的永远是最近那几个，
// 正序会让每次都要滚到底。
func (d *Database) ListAICheckpoints(convID string) ([]AICheckpoint, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(
		`SELECT id, conv_id, seq, tool_call_id, tool, files, created_at
		 FROM ai_checkpoints WHERE conv_id=? ORDER BY seq DESC`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AICheckpoint
	for rows.Next() {
		var c AICheckpoint
		if err := rows.Scan(&c.ID, &c.ConvID, &c.Seq, &c.ToolCallID, &c.Tool, &c.Files, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAICheckpoint 取指定回滚点。不存在时返回 (nil, nil)：
// 前端可能拿着一张过期的列表来点，报错不如让它自己刷新。
func (d *Database) GetAICheckpoint(convID string, seq int) (*AICheckpoint, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var c AICheckpoint
	err := d.conn.QueryRow(
		`SELECT id, conv_id, seq, tool_call_id, tool, files, created_at
		 FROM ai_checkpoints WHERE conv_id=? AND seq=?`, convID, seq).
		Scan(&c.ID, &c.ConvID, &c.Seq, &c.ToolCallID, &c.Tool, &c.Files, &c.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// PurgeAICheckpoints 清理过老的与超量的回滚点，返回被删掉的记录。
//
// 返回记录而不是只返回条数：快照文件在磁盘上，删了行却不删目录就是纯泄漏，
// 而上层（services/ai）不知道要删哪些目录，只能由这里告诉它。
//
// 两条条件取并集：
//   - 太老（超过 maxAgeDays）：即使只有 2 个也清掉，否则「一个月没动的会话」
//     会永远占着快照；
//   - 超出最近 keepPerConv 个：一次改五十处的任务，前三十个快照没人会点。
//
// 时间比较用 julianday：created_at 是带时区偏移的 RFC3339，跨时区/改过系统时间之后
// 字典序不再等于时间序（墓碑那次的教训）。代价是用不上 created_at 索引，全表扫可接受。
func (d *Database) PurgeAICheckpoints(maxAgeDays, keepPerConv int) ([]AICheckpoint, error) {
	if maxAgeDays <= 0 {
		maxAgeDays = AICheckpointMaxAgeDays
	}
	if keepPerConv <= 0 {
		keepPerConv = AICheckpointKeepPerConv
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays).Format(time.RFC3339)

	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT id, conv_id, seq, tool_call_id, tool, files, created_at FROM ai_checkpoints
		 WHERE julianday(created_at) < julianday(?)
		    OR seq < (SELECT COALESCE(MAX(seq),0) FROM ai_checkpoints c2
		              WHERE c2.conv_id = ai_checkpoints.conv_id) - ?`,
		cutoff, keepPerConv)
	if err != nil {
		return nil, err
	}
	var doomed []AICheckpoint
	for rows.Next() {
		var c AICheckpoint
		if err := rows.Scan(&c.ID, &c.ConvID, &c.Seq, &c.ToolCallID, &c.Tool, &c.Files, &c.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		doomed = append(doomed, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range doomed {
		if _, err := d.conn.Exec(`DELETE FROM ai_checkpoints WHERE id=?`, c.ID); err != nil {
			return doomed, err
		}
	}
	return doomed, nil
}

// DeleteAIcheckpointsOfConv 删掉一个会话的全部回滚点，返回被删记录。
//
// 会话被删时不自动调用：与墓碑同一个判断——用户删会话之后很可能正想「把改坏的
// 文件退回去」，此时把快照一起清掉等于把最后一根救命稻草也拿走。靠 TTL 淘汰。
func (d *Database) DeleteAIcheckpointsOfConv(convID string) ([]AICheckpoint, error) {
	list, err := d.ListAICheckpoints(convID)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(`DELETE FROM ai_checkpoints WHERE conv_id=?`, convID); err != nil {
		return nil, err
	}
	return list, nil
}
