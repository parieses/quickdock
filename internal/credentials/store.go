package credentials

import (
	"fmt"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// database 取已绑定的库引用（可能为 nil：DB 尚未打开）。
func database() *db.Database {
	mu.Lock()
	defer mu.Unlock()
	return conn
}

// List 返回全部条目（元信息，不含明文）。
func List() ([]db.Credential, error) {
	d := database()
	if d == nil {
		return nil, ErrLocked
	}
	return d.ListCredentials()
}

// Get 取单条元信息。
func Get(id string) (*db.Credential, error) {
	d := database()
	if d == nil {
		return nil, ErrLocked
	}
	return d.GetCredential(id)
}

// Save 新建或更新条目。
//
// 入参 Secret 是**明文**（来自前端表单 / 扩展 / 内部「顺手保存」），
// 本函数负责加密后交给 db 层；其余字段原样透传。
// Secret 留空表示保持原值（与 ssh_hosts 一致的约定），清除请置 ClearSecret。
func Save(in db.Credential) (*db.Credential, error) {
	d := database()
	if d == nil {
		return nil, ErrLocked
	}

	if in.Secret != "" {
		key, err := ensureDEK()
		if err != nil {
			return nil, err
		}
		enc, err := seal(key, in.Secret)
		if err != nil {
			return nil, err
		}
		in.Secret = enc
	}
	return d.SaveCredential(in)
}

// Secret 一次「取用」的结果：账号与明文密码。
type Secret struct {
	ID       string
	Username string
	Secret   string
}

// Reveal 解出单条明文，并在库里记一次使用。
//
// 这是全库唯一把明文交出去的入口。调用方只有三处：前端「显示/复制」、
// 本地 HTTP 的 /vault/reveal、内部弹层填充表单。审计日志记在这里，
// 保证「谁看过密码」有迹可循。
func Reveal(id string) (*Secret, error) {
	d := database()
	if d == nil {
		return nil, ErrLocked
	}
	meta, err := d.GetCredential(id)
	if err != nil {
		return nil, err
	}
	out := &Secret{ID: meta.ID, Username: meta.Username}
	if !meta.HasSecret {
		return out, nil // 只存了账号，没有密码：返回空串而不是报错
	}

	cipherText, err := d.GetCredentialSecret(id)
	if err != nil {
		return nil, err
	}
	key, err := ensureDEK()
	if err != nil {
		return nil, err
	}
	plain, err := open(key, cipherText)
	if err != nil {
		return nil, fmt.Errorf("解开「%s」的密码失败: %w", meta.Name, err)
	}
	out.Secret = plain
	logger.I("[credentials] reveal id=%s kind=%s", meta.ID, meta.Kind)

	// 使用统计失败不影响取用（见 db.TouchCredential 的注释）。
	if err := d.TouchCredential(id); err != nil {
		logger.W("[credentials] 记录使用次数失败: %v", err)
	}
	return out, nil
}

// Delete 删除条目。
func Delete(id string) error {
	d := database()
	if d == nil {
		return ErrLocked
	}
	return d.DeleteCredential(id)
}

// Touch 只记一次使用（扩展比对到「密码一致」时调用，不需要明文）。
func Touch(id string) error {
	d := database()
	if d == nil {
		return ErrLocked
	}
	return d.TouchCredential(id)
}

// Count 返回条目总数。
func Count() (int, error) {
	d := database()
	if d == nil {
		return 0, ErrLocked
	}
	return d.CountCredentials()
}
