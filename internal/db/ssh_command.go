package db

import (
	"fmt"
	"strings"
)

// SshCommand 一条 SSH 命令片段。
//
// 与 AI 提示词库（prompt.go）不是一回事：那边的正文要交给模型读，这边的正文
// 会被 SshService.Exec / BatchRun 直接送进远程 shell。所以本表只存原文，
// 不做任何变量展开——把 ${host} 之类的占位符留给前端替换会带来注入面，
// 而片段的实际价值（少打一遍长命令）不需要变量也能成立。
//
// 表名刻意不叫 snippets：schema.go 的迁移会把 snippets 合并/重命名成 notes。
// 也不往 notes 加 category：那会污染笔记列表和 AI 上下文。
type SshCommand struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Command     string `json:"command"`
	Description string `json:"description"`
	Category    string `json:"category"`
	UsageCount  int    `json:"usageCount"`
	Sort        int    `json:"sort"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

const sshCommandCols = `id, name, command, description, category, usage_count, sort, created_at, updated_at`

// 片段字段上限。比提示词更严：命令最终会进 shell，超长串既无实用价值也会
// 让预演 / 结果面板卡住。
const (
	maxSshCommandName = 120
	maxSshCommandText = 4096
	maxSshCommandDesc = 500
	maxSshCommandCate = 60
)

func mapToSshCommand(m map[string]interface{}) SshCommand {
	return SshCommand{
		ID:          str(m["id"]),
		Name:        str(m["name"]),
		Command:     str(m["command"]),
		Description: str(m["description"]),
		Category:    str(m["category"]),
		UsageCount:  integer(m["usage_count"]),
		Sort:        integer(m["sort"]),
		CreatedAt:   str(m["created_at"]),
		UpdatedAt:   str(m["updated_at"]),
	}
}

// ListSshCommands 列出全部片段（分类 → 手工排序 → 使用次数 → 名称）。
// 不分页：这类数据是「自己攒的几十条」，翻页只会多一层点击。
func (d *Database) ListSshCommands() ([]SshCommand, error) {
	rows, err := d.Query(`SELECT ` + sshCommandCols +
		` FROM ssh_commands ORDER BY category ASC, sort ASC, usage_count DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToSshCommand), nil
}

// GetSshCommand 按 id 取一条；不存在返回 (nil, nil)。
func (d *Database) GetSshCommand(id string) (*SshCommand, error) {
	row, err := d.QueryOne(`SELECT `+sshCommandCols+` FROM ssh_commands WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	c := mapToSshCommand(row)
	return &c, nil
}

func validateSshCommand(c *SshCommand) error {
	c.Name = strings.TrimSpace(c.Name)
	c.Category = strings.TrimSpace(c.Category)
	c.Description = strings.TrimSpace(c.Description)
	// 命令正文不 TrimSpace 掉行首缩进：多行脚本的缩进可能有语义，
	// 只允许去掉首尾空行。
	c.Command = strings.Trim(c.Command, "\r\n")
	if c.Name == "" {
		return fmt.Errorf("片段名称不能为空: %w", ErrSshValidation)
	}
	if strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("命令内容不能为空: %w", ErrSshValidation)
	}
	if len(c.Name) > maxSshCommandName || len(c.Command) > maxSshCommandText ||
		len(c.Description) > maxSshCommandDesc || len(c.Category) > maxSshCommandCate {
		return fmt.Errorf("片段内容过长: %w", ErrSshValidation)
	}
	return nil
}

// SaveSshCommand 新增或更新一条片段（id 为空即新增）。
// UsageCount 不允许由调用方写入：它由 TouchSshCommand 累加，避免表单往返把
// 计数重置成 0。
func (d *Database) SaveSshCommand(c *SshCommand) (*SshCommand, error) {
	if err := validateSshCommand(c); err != nil {
		return nil, err
	}
	ts := now()
	if c.ID == "" {
		c.ID = newID()
		c.CreatedAt = ts
		c.UpdatedAt = ts
		c.UsageCount = 0
		if err := d.ExecuteParams(
			`INSERT INTO ssh_commands (`+sshCommandCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]interface{}{c.ID, c.Name, c.Command, c.Description, c.Category, 0, c.Sort, ts, ts},
		); err != nil {
			return nil, err
		}
		return c, nil
	}
	cur, err := d.GetSshCommand(c.ID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("命令片段不存在: %w", ErrSshValidation)
	}
	c.UsageCount = cur.UsageCount
	c.CreatedAt = cur.CreatedAt
	c.UpdatedAt = ts
	if err := d.ExecuteParams(
		`UPDATE ssh_commands SET name = ?, command = ?, description = ?, category = ?,
		 sort = ?, updated_at = ? WHERE id = ?`,
		[]interface{}{c.Name, c.Command, c.Description, c.Category, c.Sort, ts, c.ID},
	); err != nil {
		return nil, err
	}
	return c, nil
}

// DeleteSshCommand 删除一条片段。片段是纯用户数据，不走回收站——它只有几行文本，
// 重建成本远低于给「删一条命令」加一套墓碑机制。
func (d *Database) DeleteSshCommand(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("片段 id 不能为空: %w", ErrSshValidation)
	}
	return d.ExecuteParams(`DELETE FROM ssh_commands WHERE id = ?`, []interface{}{id})
}

// TouchSshCommand 选中并执行一次后累加使用次数，让常用的片段自己浮上来。
// 不存在时静默忽略：片段可能刚被另一个窗口删掉，不该让执行命令失败。
func (d *Database) TouchSshCommand(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return d.ExecuteParams(
		`UPDATE ssh_commands SET usage_count = usage_count + 1 WHERE id = ?`,
		[]interface{}{id},
	)
}
