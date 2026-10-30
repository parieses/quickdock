package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"quickdock/internal/platform"
)

// SSH 主机与会话树的存储层。
//
// 安全约定：
//   - 密码与私钥口令用 platform.EncryptSecret（Windows 上为 DPAPI）加密后落库；
//   - 所有读取接口（列表/详情）都不查密文字段，只回 HasPassword / HasPassphrase，
//     明文只有 GetSshHostSecret 一处在「即将建立连接」时解密，绝不进前端。
//
// 与本包其它表一致：文件夹与主机同表，用 is_folder 区分（参照 notes）。
type SshHost struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	AuthType string `json:"authType"` // password | key

	KeyPath          string `json:"keyPath"`
	LegacyAlgorithms bool   `json:"legacyAlgorithms"`

	// Env 标记生产/预发/测试，用于标签与边框着色，避免误操作生产机。
	Env string `json:"env"`

	ParentID string `json:"parentId"`
	IsFolder bool   `json:"isFolder"`
	Sort     int    `json:"sort"`
	Remark   string `json:"remark"`

	LastConnectedAt string `json:"lastConnectedAt"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`

	// 以下两个字段只写不读：保存时传入明文，任何读取路径都不会带回。
	Password      string `json:"-"`
	KeyPassphrase string `json:"-"`

	// ClearPassword 保存时显式清除已存密码。
	// 密码留空的默认语义是「不修改」，没有这个开关就永远删不掉已存的密码。
	ClearPassword bool `json:"clearPassword"`

	HasPassword   bool `json:"hasPassword"`
	HasPassphrase bool `json:"hasPassphrase"`
}

// SshHostSecret 连接瞬间使用的解密凭据，只在内存中短暂存在。
type SshHostSecret struct {
	Host             string
	Port             int
	User             string
	AuthType         string
	Password         string
	KeyPath          string
	KeyPassphrase    string
	LegacyAlgorithms bool
}

// SSH 认证类型与环境取值。
const (
	SshAuthPassword = "password"
	SshAuthKey      = "key"

	SshEnvProd    = "prod"
	SshEnvStaging = "staging"
	SshEnvTest    = "test"
)

// ErrSshValidation 表示入参不合法——用户填错了表单，不是程序缺陷。
//
// 这个哨兵存在的唯一目的是让上层能把「用户填错」和「真的出错了」分开：
// 前者应该做表单内联提示，后者才需要报内部错误让人去翻日志。
// 不包装的话，上层 classify 只能落到兜底分支，用户看到「内部错误」却不知道
// 自己只是漏填了主机地址。
var ErrSshValidation = errors.New("SSH 参数无效")

// sshInvalid 构造一条带哨兵的可读校验错误。
func sshInvalid(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrSshValidation, fmt.Sprintf(format, args...))
}

// sshHostCols 列表/详情统一投影。
// 密文字段不出现，改为输出「是否已设置」的标志位，从查询层面杜绝明文外泄。
const sshHostCols = `id, name, host, port, user, auth_type, key_path, legacy_algorithms,
	env, parent_id, is_folder, sort, remark, last_connected_at, created_at, updated_at,
	CASE WHEN password <> '' THEN 1 ELSE 0 END,
	CASE WHEN key_passphrase <> '' THEN 1 ELSE 0 END`

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanSshHost(row rowScanner) (*SshHost, error) {
	var h SshHost
	var legacy, isFolder, hasPw, hasPass int
	err := row.Scan(
		&h.ID, &h.Name, &h.Host, &h.Port, &h.User, &h.AuthType, &h.KeyPath, &legacy,
		&h.Env, &h.ParentID, &isFolder, &h.Sort, &h.Remark, &h.LastConnectedAt,
		&h.CreatedAt, &h.UpdatedAt, &hasPw, &hasPass,
	)
	if err != nil {
		return nil, err
	}
	h.LegacyAlgorithms = legacy != 0
	h.IsFolder = isFolder != 0
	h.HasPassword = hasPw != 0
	h.HasPassphrase = hasPass != 0
	return &h, nil
}

// ListSshHosts 返回全部节点（含文件夹），按父节点 + sort 排序供前端组树。
func (d *Database) ListSshHosts() ([]SshHost, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.conn.Query("SELECT " + sshHostCols + ` FROM ssh_hosts
		ORDER BY parent_id ASC, is_folder DESC, sort ASC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询 SSH 主机失败: %w", err)
	}
	defer rows.Close()

	out := make([]SshHost, 0, 16)
	for rows.Next() {
		h, err := scanSshHost(rows)
		if err != nil {
			return nil, fmt.Errorf("读取 SSH 主机失败: %w", err)
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// GetSshHost 读取单个节点（不含明文凭据）。
func (d *Database) GetSshHost(id string) (*SshHost, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.getSshHostLocked(id)
}

func (d *Database) getSshHostLocked(id string) (*SshHost, error) {
	h, err := scanSshHost(d.conn.QueryRow("SELECT "+sshHostCols+" FROM ssh_hosts WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("SSH 主机不存在: %w", sql.ErrNoRows)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 主机失败: %w", err)
	}
	return h, nil
}

// GetSshHostSecret 在连接前解密凭据。
//
// 这是全项目唯一返回 SSH 明文密码的入口：调用方必须只把它交给 sshx.Dial，
// 不得写日志、不得回传前端。
func (d *Database) GetSshHostSecret(id string) (*SshHostSecret, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var s SshHostSecret
	var encPw, encPass string
	err := d.conn.QueryRow(`SELECT host, port, user, auth_type, key_path,
		legacy_algorithms, password, key_passphrase FROM ssh_hosts WHERE id = ?`, id).
		Scan(&s.Host, &s.Port, &s.User, &s.AuthType, &s.KeyPath, &s.LegacyAlgorithms, &encPw, &encPass)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("SSH 主机不存在: %w", sql.ErrNoRows)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 凭据失败: %w", err)
	}

	if encPw != "" {
		pw, err := platform.DecryptSecret(encPw)
		if err != nil {
			return nil, fmt.Errorf("解密 SSH 密码失败（可能是在另一台机器/账户下保存的）: %w", err)
		}
		s.Password = pw
	}
	if encPass != "" {
		pass, err := platform.DecryptSecret(encPass)
		if err != nil {
			return nil, fmt.Errorf("解密私钥口令失败（可能是在另一台机器/账户下保存的）: %w", err)
		}
		s.KeyPassphrase = pass
	}
	return &s, nil
}

// SaveSshHost 新建或更新节点。
//
// 新建时 ID 为空；更新时 Password / KeyPassphrase 留空表示保持原值，
// 需要真正清除时置 ClearPassword。
func (d *Database) SaveSshHost(in SshHost) (*SshHost, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	normalizeSshHost(&in)
	if in.IsFolder && in.Name == "" {
		return nil, sshInvalid("文件夹名称不能为空")
	}
	if !in.IsFolder && in.Host == "" {
		return nil, sshInvalid("主机地址不能为空")
	}
	if in.Name == "" {
		// 名字留空就用「主机地址:端口」兜底，避免列表里出现空白行。
		in.Name = in.Host
	}
	if in.ID != "" && in.ParentID == in.ID {
		return nil, sshInvalid("不能把节点挂到自身上")
	}
	if err := d.checkSshParentLocked(in.ID, in.ParentID); err != nil {
		return nil, err
	}

	nowStr := now()
	var err error

	if in.ID == "" {
		in.ID = newID()
		if in.Sort == 0 {
			in.Sort = nextSortLocked(d, "ssh_hosts", in.ParentID)
		}
		in.CreatedAt = nowStr
		in.UpdatedAt = nowStr

		encPw, err := encryptOptional(in.Password)
		if err != nil {
			return nil, err
		}
		encPass, err := encryptOptional(in.KeyPassphrase)
		if err != nil {
			return nil, err
		}

		_, err = d.conn.Exec(`INSERT INTO ssh_hosts
			(id, name, host, port, user, auth_type, password, key_path, key_passphrase,
			 legacy_algorithms, env, parent_id, is_folder, sort, remark, last_connected_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.ID, in.Name, in.Host, in.Port, in.User, in.AuthType, encPw, in.KeyPath, encPass,
			b2i(in.LegacyAlgorithms), in.Env, in.ParentID, b2i(in.IsFolder), in.Sort, in.Remark,
			"", in.CreatedAt, in.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("保存 SSH 主机失败: %w", err)
		}
		in.HasPassword = encPw != ""
		in.HasPassphrase = encPass != ""
		return &in, nil
	}

	// 更新：先取出既有密文与创建时间。
	var encPw, encPass, createdAt string
	err = d.conn.QueryRow(
		`SELECT password, key_passphrase, created_at FROM ssh_hosts WHERE id = ?`, in.ID).
		Scan(&encPw, &encPass, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("SSH 主机不存在: %w", sql.ErrNoRows)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 主机失败: %w", err)
	}
	if in.ClearPassword {
		encPw = ""
	}
	if in.Password != "" {
		encPw, err = encryptOptional(in.Password)
		if err != nil {
			return nil, err
		}
	}
	if in.ClearPassword {
		encPass = ""
	}
	if in.KeyPassphrase != "" {
		encPass, err = encryptOptional(in.KeyPassphrase)
		if err != nil {
			return nil, err
		}
	}

	in.CreatedAt = createdAt
	in.UpdatedAt = nowStr
	in.HasPassword = encPw != ""
	in.HasPassphrase = encPass != ""

	_, err = d.conn.Exec(`UPDATE ssh_hosts SET
		name = ?, host = ?, port = ?, user = ?, auth_type = ?, password = ?, key_path = ?,
		key_passphrase = ?, legacy_algorithms = ?, env = ?, parent_id = ?, is_folder = ?,
		sort = ?, remark = ?, updated_at = ?
		WHERE id = ?`,
		in.Name, in.Host, in.Port, in.User, in.AuthType, encPw, in.KeyPath, encPass,
		b2i(in.LegacyAlgorithms), in.Env, in.ParentID, b2i(in.IsFolder), in.Sort, in.Remark,
		in.UpdatedAt, in.ID)
	if err != nil {
		return nil, fmt.Errorf("更新 SSH 主机失败: %w", err)
	}
	return &in, nil
}

// MoveSshHost 把节点移到新父节点（会话树拖拽）。
func (d *Database) MoveSshHost(id, newParentID string) error {
	if id == newParentID {
		return sshInvalid("不能把节点移到自身下")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.checkSshParentLocked(id, newParentID); err != nil {
		return err
	}
	sort := nextSortLocked(d, "ssh_hosts", newParentID)
	res, err := d.conn.Exec(`UPDATE ssh_hosts SET parent_id = ?, sort = ?, updated_at = ? WHERE id = ?`,
		newParentID, sort, now(), id)
	if err != nil {
		return fmt.Errorf("移动 SSH 节点失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("SSH 节点不存在: %w", sql.ErrNoRows)
	}
	return nil
}

// DeleteSshHost 删除节点及其整棵子树（删文件夹时子节点一起走，避免孤儿）。
func (d *Database) DeleteSshHost(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.conn.Exec(`WITH RECURSIVE sub(id) AS (
			SELECT id FROM ssh_hosts WHERE id = ?
			UNION
			SELECT h.id FROM ssh_hosts h JOIN sub ON h.parent_id = sub.id
		)
		DELETE FROM ssh_hosts WHERE id IN (SELECT id FROM sub)`, id)
	if err != nil {
		return fmt.Errorf("删除 SSH 主机失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("SSH 主机不存在: %w", sql.ErrNoRows)
	}
	return nil
}

// TouchSshHostConnected 记录最近连接时间（连接成功后调用）。
func (d *Database) TouchSshHostConnected(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE ssh_hosts SET last_connected_at = ? WHERE id = ?`, now(), id)
	return err
}

// checkSshParentLocked 校验父节点合法：必须存在、必须是文件夹、不能是自己的后代。
//
// 与 notes 同样的坑：parent_id 指向不存在或非文件夹的节点时，前端组树会把子节点
// 挂到不渲染的父节点下，表现为「建了但看不见」，比直接报错难查得多。
func (d *Database) checkSshParentLocked(id, parentID string) error {
	if parentID == "" {
		return nil
	}
	var isFolder int
	err := d.conn.QueryRow(`SELECT is_folder FROM ssh_hosts WHERE id = ?`, parentID).Scan(&isFolder)
	if errors.Is(err, sql.ErrNoRows) {
		return sshInvalid("父文件夹不存在")
	}
	if err != nil {
		return fmt.Errorf("校验父文件夹失败: %w", err)
	}
	if isFolder == 0 {
		return sshInvalid("父节点不是文件夹")
	}
	if id == "" {
		return nil
	}
	// 把自己的父节点设成自己的后代，会形成环，树就永远展不开了。
	var n int
	err = d.conn.QueryRow(`WITH RECURSIVE sub(id) AS (
			SELECT id FROM ssh_hosts WHERE parent_id = ?
			UNION
			SELECT h.id FROM ssh_hosts h JOIN sub ON h.parent_id = sub.id
		)
		SELECT COUNT(*) FROM sub WHERE id = ?`, id, parentID).Scan(&n)
	if err != nil {
		return fmt.Errorf("校验父文件夹失败: %w", err)
	}
	if n > 0 {
		return sshInvalid("不能把文件夹移到自己的子文件夹下")
	}
	return nil
}

// normalizeSshHost 归一化取值，保证库里的数据可预期。
func normalizeSshHost(h *SshHost) {
	h.Name = validateName(h.Name)
	h.Host = strings.TrimSpace(h.Host)
	h.User = strings.TrimSpace(h.User)
	h.KeyPath = strings.TrimSpace(h.KeyPath)
	h.Remark = strings.TrimSpace(h.Remark)
	h.ParentID = strings.TrimSpace(h.ParentID)

	if h.Port <= 0 || h.Port > 65535 {
		h.Port = 22
	}
	switch h.AuthType {
	case SshAuthKey, SshAuthPassword:
	default:
		h.AuthType = SshAuthPassword
	}
	switch h.Env {
	case SshEnvProd, SshEnvStaging, SshEnvTest:
	default:
		h.Env = SshEnvTest
	}
	// 文件夹不承载连接信息，避免残留凭据跟着一个文件夹混在库里。
	if h.IsFolder {
		h.Host = ""
		h.User = ""
		h.KeyPath = ""
		h.Password = ""
		h.KeyPassphrase = ""
		h.ClearPassword = true
		h.LegacyAlgorithms = false
	}
}

func encryptOptional(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	enc, err := platform.EncryptSecret(plain)
	if err != nil {
		return "", fmt.Errorf("加密 SSH 凭据失败: %w", err)
	}
	return enc, nil
}

// ---- 已信任主机密钥 ----

// SshKnownHost 一条已信任的主机密钥。
// 与 sshx.KnownHost 字段一致，但刻意不引用 sshx 包：db 是最底层，
// 不该反向依赖 SSH 引擎；两者之间的适配放在 services/sshx。
type SshKnownHost struct {
	Host        string
	Port        int
	KeyType     string
	Fingerprint string
	AddedAt     int64
}

// ListSshKnownHosts 返回某主机已记录的全部密钥（可能多种类型）。
func (d *Database) ListSshKnownHosts(host string, port int) ([]SshKnownHost, error) {
	host, port = normalizeKnownHost(host, port)

	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.conn.Query(`SELECT host, port, key_type, fingerprint, added_at
		FROM ssh_known_hosts WHERE host = ? AND port = ? ORDER BY key_type`, host, port)
	if err != nil {
		return nil, fmt.Errorf("查询主机密钥失败: %w", err)
	}
	defer rows.Close()

	out := make([]SshKnownHost, 0, 2)
	for rows.Next() {
		var k SshKnownHost
		if err := rows.Scan(&k.Host, &k.Port, &k.KeyType, &k.Fingerprint, &k.AddedAt); err != nil {
			return nil, fmt.Errorf("读取主机密钥失败: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ListAllSshKnownHosts 返回全部已信任的主机密钥，按 host + port + key_type 排序。
//
// 与 ListSshKnownHosts 的区别是不带 WHERE：管理页要一次列出所有服务器的
// 已信任记录，让用户能看到「我到底信任过什么、能撤销哪些」。
// 用带 WHERE 的版本去做这件事需要前端按主机逐台查，主机多了就是 N 次 IPC。
func (d *Database) ListAllSshKnownHosts() ([]SshKnownHost, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.conn.Query(`SELECT host, port, key_type, fingerprint, added_at
		FROM ssh_known_hosts ORDER BY host ASC, port ASC, key_type ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询主机密钥失败: %w", err)
	}
	defer rows.Close()

	out := make([]SshKnownHost, 0, 8)
	for rows.Next() {
		var k SshKnownHost
		if err := rows.Scan(&k.Host, &k.Port, &k.KeyType, &k.Fingerprint, &k.AddedAt); err != nil {
			return nil, fmt.Errorf("读取主机密钥失败: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SaveSshKnownHost 写入或更新一条密钥记录（同 host+port+keyType 覆盖）。
func (d *Database) SaveSshKnownHost(k SshKnownHost) error {
	if strings.TrimSpace(k.KeyType) == "" || strings.TrimSpace(k.Fingerprint) == "" {
		return errors.New("主机密钥类型与指纹不能为空")
	}
	k.Host, k.Port = normalizeKnownHost(k.Host, k.Port)
	if k.AddedAt == 0 {
		k.AddedAt = time.Now().UnixMilli()
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(`INSERT INTO ssh_known_hosts (host, port, key_type, fingerprint, added_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(host, port, key_type) DO UPDATE SET
			fingerprint = excluded.fingerprint,
			added_at = excluded.added_at`,
		k.Host, k.Port, k.KeyType, k.Fingerprint, k.AddedAt)
	if err != nil {
		return fmt.Errorf("保存主机密钥失败: %w", err)
	}
	return nil
}

// DeleteSshKnownHosts 清除某主机的全部已信任密钥。
func (d *Database) DeleteSshKnownHosts(host string, port int) error {
	host, port = normalizeKnownHost(host, port)

	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(`DELETE FROM ssh_known_hosts WHERE host = ? AND port = ?`, host, port)
	if err != nil {
		return fmt.Errorf("清除主机密钥失败: %w", err)
	}
	return nil
}

// normalizeKnownHost 与 sshx 侧的规范化保持一致（大小写/空格/默认端口）。
// 两边规则不一致会让同一个主机产生两份记录，表现为「信任过又弹窗」。
func normalizeKnownHost(host string, port int) (string, int) {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	host = strings.ToLower(host)
	if port <= 0 || port > 65535 {
		port = 22
	}
	return host, port
}
