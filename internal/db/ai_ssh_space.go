package db

import (
	"database/sql"
	"errors"
)

// SSH 助手专属空间。
//
// SSH 终端的每次连接都会产出一条 AI 任务（见 services/sshx/ai_session.go）。
// 这些任务如果散在侧栏「任务」组里，一台机器连几次就是一排「SSH · root@…」，
// 用户自己的对话会被淹掉。所以统一收进一个 kind='ssh' 的空间：
// 侧栏多一组「SSH 助手」，点开是连接史，收起时完全不占地方。
//
// 按 kind 查而不是按名字查：用户可能把空间改名（「SSH 助手」→「生产机排查」），
// 按名字查会在改名后再建一个重复的空间，历史被劈成两半。
// kind 是程序维护的标记，改名字段动不到它。
const defaultSSHSpaceName = "SSH 助手"

// sshSpaceKind 与 schedule 空间并列的第二种专属空间。
const sshSpaceKind = "ssh"

// FindSSHSpace 取 SSH 专属空间；没有返回 (nil, nil)。
//
// QueryOne 没命中时返回 sql.ErrNoRows，这里必须当「没有」：
// 当成错误往上抛会让 EnsureSSHSpace 在「第一次建空间」这条唯一的主路径上直接失败。
func (d *Database) FindSSHSpace() (*AISpace, error) {
	row, err := d.QueryOne(
		`SELECT * FROM ai_spaces WHERE kind = ? ORDER BY created_at ASC LIMIT 1`,
		sshSpaceKind,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	s := mapToAISpace(row)
	return &s, nil
}

// EnsureSSHSpace 幂等地拿到 SSH 专属空间，没有就建。
//
// 为什么每次提问都走一遍而不是在 SshService 里缓存 id：用户可以在侧栏把这个空间
// 删掉（删空间会把里面的会话快照进回收站，会话本身移出该空间）。缓存下来的 id
// 指向一个不存在的空间，之后新写的会话就是「悬挂」的 —— 侧栏按 space_id 归组，
// 它不会出现在任何一组里，用户看到的是「问答成功但找不到任务」。
// 一次 SELECT 的代价远小于这个排查成本。
//
// Cwd 留空：这个空间不是工作目录容器，SSH 场景的 AI 也没有本地文件可读写。
// 前端据此不给它「新建任务 / 在资源管理器打开」这类入口（见 AiConversationSidebar）。
func (d *Database) EnsureSSHSpace() (*AISpace, error) {
	if sp, err := d.FindSSHSpace(); err != nil || sp != nil {
		return sp, err
	}
	s := &AISpace{
		ID:        newID(),
		Name:      defaultSSHSpaceName,
		Kind:      sshSpaceKind,
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	if err := d.BulkInsert("ai_spaces", []map[string]interface{}{structToMap(s)}); err != nil {
		return nil, err
	}
	return s, nil
}
