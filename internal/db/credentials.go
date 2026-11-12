package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// 密码库存储层。
//
// 与 ssh_host.go 的分工差异：本文件**不碰加解密**。
// ssh_hosts 的密码直接调 platform.EncryptSecret（DPAPI）逐条加密，够用且简单；
// 密码库要多一条「用一条 DEK 加密 N 条密文」的路径（见 internal/credentials），
// 那把加解密留在引擎侧，本层只把 secret 当成不透明字符串存取。
//
// 投影约定与 ssh_host 一致：列表/详情都不查 secret 列，只回 HasSecret，
// 从查询层面杜绝明文/密文外泄到前端。

// Credential 一条密码库条目。
//
// Secret 是密文（含算法前缀，见 internal/credentials/crypto.go），
// json:"-" 保证它即便被误塞进响应也不会序列化出去。
type Credential struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // ssh | database | webdav | git | api_key | login | other
	Username string `json:"username"`
	Secret   string `json:"-"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	URL      string `json:"url"`
	Notes    string `json:"notes"`
	Tags     string `json:"tags"`

	// Source / SourceID 标记条目来源，用于「从某处填充到密码库」的单向溯源。
	// 空串表示用户手工创建。密码库从不反写来源，避免双向同步的脏写。
	Source   string `json:"source"`
	SourceID string `json:"sourceId"`

	Favorite   bool   `json:"favorite"`
	UseCount   int    `json:"useCount"`
	LastUsedAt string `json:"lastUsedAt"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`

	// HasSecret 由查询计算得出；Secret 为空字符串表示「只有账号没有密码」。
	HasSecret bool `json:"hasSecret"`

	// ClearSecret 保存时显式清除已存密码。
	// 与 ssh_hosts 同款语义：Secret 留空的默认含义是「不修改」，
	// 没有这个开关就永远删不掉已存的密码。
	ClearSecret bool `json:"clearSecret"`
}

// Kind 取值。放常量而不是裸字符串：扩展与内部弹层都要按它匹配，
// 拼错一处就会表现为「明明存了却推荐不出来」。
const (
	CredKindSSH      = "ssh"
	CredKindDatabase = "database"
	CredKindWebDAV   = "webdav"
	CredKindGit      = "git"
	CredKindAPIKey   = "api_key"
	CredKindLogin    = "login"
	CredKindOther    = "other"
)

// ErrCredentialNotFound 条目不存在（可能刚被另一个窗口删掉）。
var ErrCredentialNotFound = errors.New("密码条目不存在")

// credentialCols 列表/详情统一投影：secret 只输出「有没有」。
const credentialCols = `id, name, kind, username, host, port, url, notes, tags,
	source, source_id, favorite, use_count, last_used_at, created_at, updated_at,
	CASE WHEN secret <> '' THEN 1 ELSE 0 END`

func scanCredential(row rowScanner) (*Credential, error) {
	var c Credential
	var fav, hasSecret int
	err := row.Scan(
		&c.ID, &c.Name, &c.Kind, &c.Username, &c.Host, &c.Port, &c.URL, &c.Notes, &c.Tags,
		&c.Source, &c.SourceID, &fav, &c.UseCount, &c.LastUsedAt, &c.CreatedAt, &c.UpdatedAt,
		&hasSecret,
	)
	if err != nil {
		return nil, err
	}
	c.Favorite = fav != 0
	c.HasSecret = hasSecret != 0
	return &c, nil
}

// ListCredentials 返回全部条目，收藏优先、最近使用次之。
//
// 不做 SQL 侧过滤：条目量级是「几十到几百」，全量取出后由引擎层按
// host/kind/username 打分排序（match.go），比在 SQL 里堆条件更可控。
func (d *Database) ListCredentials() ([]Credential, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.conn.Query("SELECT " + credentialCols + ` FROM credentials
		ORDER BY favorite DESC, use_count DESC, updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("读取密码库失败: %w", err)
	}
	defer rows.Close()

	out := make([]Credential, 0, 32)
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("读取密码条目失败: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetCredential 取单条元信息（不含密文）。
func (d *Database) GetCredential(id string) (*Credential, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	c, err := scanCredential(d.conn.QueryRow(
		"SELECT "+credentialCols+" FROM credentials WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取密码条目失败: %w", err)
	}
	return c, nil
}

// GetCredentialSecret 取单条密文。仅供引擎在「即将使用」时解密，任何列表/详情路径都不得调用。
func (d *Database) GetCredentialSecret(id string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var cipher string
	err := d.conn.QueryRow(`SELECT secret FROM credentials WHERE id = ?`, id).Scan(&cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCredentialNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取密码密文失败: %w", err)
	}
	return cipher, nil
}

// FindCredentialBySource 按来源溯源查找（如 source='ssh_host', sourceID=<主机 id>）。
//
// 扩展「保存密码」时用它判重：同一站点同一用户名不该每访问一次就多一条。
func (d *Database) FindCredentialBySource(source, sourceID string) (*Credential, error) {
	if source == "" || sourceID == "" {
		return nil, ErrCredentialNotFound
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	c, err := scanCredential(d.conn.QueryRow(
		"SELECT "+credentialCols+` FROM credentials
		 WHERE source = ? AND source_id = ? LIMIT 1`, source, sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("按来源查找密码条目失败: %w", err)
	}
	return c, nil
}

// SaveCredential 新建或更新。
//
// 新建时 ID 为空；更新时 Secret 留空表示保持原值，需要清除时置 ClearSecret。
// 加密由调用方（internal/credentials）完成后以密文形式传入。
func (d *Database) SaveCredential(in Credential) (*Credential, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if in.Name == "" {
		// 名称留空时按「host 或用户名」兜底，避免列表里出现空白行（与 ssh_host 同款处理）。
		switch {
		case in.Host != "":
			in.Name = in.Host
		case in.Username != "":
			in.Name = in.Username
		default:
			return nil, fmt.Errorf("密码条目名称不能为空")
		}
	}
	if in.Kind == "" {
		in.Kind = CredKindLogin
	}

	nowStr := now()
	if in.ID == "" {
		in.ID = newID()
		in.CreatedAt = nowStr
		in.UpdatedAt = nowStr
		_, err := d.conn.Exec(`INSERT INTO credentials
			(id, name, kind, username, secret, host, port, url, notes, tags,
			 source, source_id, favorite, use_count, last_used_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.ID, in.Name, in.Kind, in.Username, in.Secret, in.Host, in.Port, in.URL,
			in.Notes, in.Tags, in.Source, in.SourceID, b2i(in.Favorite), in.UseCount,
			in.LastUsedAt, in.CreatedAt, in.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("保存密码条目失败: %w", err)
		}
		in.HasSecret = in.Secret != ""
		return &in, nil
	}

	// 更新：先取既有密文与创建时间，按「留空不修改」语义合并。
	var cipher, createdAt string
	err := d.conn.QueryRow(
		`SELECT secret, created_at FROM credentials WHERE id = ?`, in.ID).
		Scan(&cipher, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取密码条目失败: %w", err)
	}
	if in.ClearSecret {
		cipher = ""
	}
	if in.Secret != "" {
		cipher = in.Secret
	}

	in.CreatedAt = createdAt
	in.UpdatedAt = nowStr
	in.HasSecret = cipher != ""
	_, err = d.conn.Exec(`UPDATE credentials SET
		name = ?, kind = ?, username = ?, secret = ?, host = ?, port = ?, url = ?,
		notes = ?, tags = ?, source = ?, source_id = ?, favorite = ?, updated_at = ?
		WHERE id = ?`,
		in.Name, in.Kind, in.Username, cipher, in.Host, in.Port, in.URL,
		in.Notes, in.Tags, in.Source, in.SourceID, b2i(in.Favorite), in.UpdatedAt, in.ID)
	if err != nil {
		return nil, fmt.Errorf("更新密码条目失败: %w", err)
	}
	return &in, nil
}

// DeleteCredential 物理删除。密码库不做回收站：留一份密文在库里与「我已经删了」的预期相悖。
func (d *Database) DeleteCredential(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.conn.Exec(`DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除密码条目失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// TouchCredential 记录一次使用（次数 + 最近使用时间），用于列表排序与推荐权重。
//
// 失败不返回错误给用户：这是纯统计副作用，写不进去不该让「填充密码」这件事失败。
func (d *Database) TouchCredential(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(
		`UPDATE credentials SET use_count = use_count + 1, last_used_at = ? WHERE id = ?`,
		now(), id)
	return err
}

// CountCredentials 统计条目数（密码库首页徽标、扩展首次配对提示用）。
func (d *Database) CountCredentials() (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var n int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM credentials`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计密码条目失败: %w", err)
	}
	return n, nil
}
