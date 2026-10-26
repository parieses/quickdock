package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ClipboardRule 剪贴板自动规则：复制到文本后按优先级做一次转换。
//
// 设计底线（前端与后端都必须守住）：
//  1. 总开关默认关（DB 设置 clipboard_rules_enabled），用户不启用就一行代码都不跑；
//  2. 每条规则单独 enabled；
//  3. write_back 默认 0 —— 默认只改「进历史的那份副本」，不改写系统剪贴板。
//     改写系统剪贴板等于篡改用户刚复制的东西，默认打开属于越界。
//  4. 不做脱敏：不提供手机号/邮箱/身份证/token 的自动替换。
//     误伤率高，且被替换掉的往往正是用户复制它的目的，静默改写比不改写更危险。
type ClipboardRule struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`  // 小者先执行
	MatchKind string `json:"matchKind"` // all | regex | app
	MatchExpr string `json:"matchExpr"` // regex：正则；app：来源应用标题包含该串
	Action    string `json:"action"`    // trim | remove_blank | dedupe_lines | sort_lines | json_format | json_minify | strip_ansi | case | url_decode
	ActionArg string `json:"actionArg"`
	WriteBack bool   `json:"writeBack"` // 是否把转换结果写回系统剪贴板
	CreatedTs int64  `json:"createdTs"`
	UpdatedTs int64  `json:"updatedTs"`
}

const clipRuleCols = `id, name, enabled, priority, match_kind, match_expr, action, action_arg, write_back, created_ts, updated_ts`

func scanClipRule(s rowScanner) (ClipboardRule, error) {
	var r ClipboardRule
	var enabled, writeBack int
	err := s.Scan(&r.ID, &r.Name, &enabled, &r.Priority, &r.MatchKind, &r.MatchExpr,
		&r.Action, &r.ActionArg, &writeBack, &r.CreatedTs, &r.UpdatedTs)
	r.Enabled = enabled != 0
	r.WriteBack = writeBack != 0
	return r, err
}

// CreateClipboardRule 新建规则（名称必填；priority 为空则排到末尾）。
func (d *Database) CreateClipboardRule(r *ClipboardRule) (*ClipboardRule, error) {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return nil, fmt.Errorf("规则名称不能为空")
	}
	if r.MatchKind == "" {
		r.MatchKind = "all"
	}
	now := time.Now().UnixMilli()
	r.ID = newID()
	r.CreatedTs = now
	r.UpdatedTs = now

	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Priority <= 0 {
		var maxPri int
		_ = d.conn.QueryRow(`SELECT COALESCE(MAX(priority), 0) FROM clipboard_rules`).Scan(&maxPri)
		r.Priority = maxPri + 1
	}
	_, err := d.conn.Exec(
		`INSERT INTO clipboard_rules
			(id, name, enabled, priority, match_kind, match_expr, action, action_arg, write_back, created_ts, updated_ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, b2i(r.Enabled), r.Priority, r.MatchKind, r.MatchExpr,
		r.Action, r.ActionArg, b2i(r.WriteBack), r.CreatedTs, r.UpdatedTs,
	)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListClipboardRules 全部规则（按优先级升序，同优先级按创建时间）
func (d *Database) ListClipboardRules() ([]ClipboardRule, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + clipRuleCols + ` FROM clipboard_rules ORDER BY priority ASC, created_ts ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClipboardRule
	for rows.Next() {
		r, err := scanClipRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateClipboardRule 更新可编辑字段（不改动 created_ts）
func (d *Database) UpdateClipboardRule(r *ClipboardRule) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("规则名称不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`UPDATE clipboard_rules SET name=?, enabled=?, priority=?, match_kind=?, match_expr=?, action=?, action_arg=?, write_back=?, updated_ts=?
		 WHERE id=?`,
		r.Name, b2i(r.Enabled), r.Priority, r.MatchKind, r.MatchExpr,
		r.Action, r.ActionArg, b2i(r.WriteBack), time.Now().UnixMilli(), r.ID,
	)
	return err
}

// DeleteClipboardRule 删除规则
func (d *Database) DeleteClipboardRule(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM clipboard_rules WHERE id = ?`, id)
	return err
}

// SetClipboardRuleEnabled 单独开关某条规则
func (d *Database) SetClipboardRuleEnabled(id string, enabled bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE clipboard_rules SET enabled=?, updated_ts=? WHERE id=?`,
		b2i(enabled), time.Now().UnixMilli(), id)
	return err
}

// ensure rowScanner 接口与本包既有实现一致（编译期校验）
var _ rowScanner = (*sql.Row)(nil)
var _ rowScanner = (*sql.Rows)(nil)
