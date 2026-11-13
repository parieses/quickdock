package credentials

// 密码库的可移植备份格式（导出 / 导入）。
//
// 为什么必须有这一层：DEK 由 platform.EncryptSecret（Windows 上是 DPAPI）
// 包裹后存在 app_state，绑死「当前 Windows 用户 + 当前机器」。重装系统、换账户、
// 换机器都解不开，而 credentials 表又刻意不进快照/WebDAV 备份（见 schema.go）。
// 没有导出，用户一旦换环境，全库密文就变成永远解不开的垃圾数据。
//
// 格式（二进制，避免 JSON 里再套一层 base64）：
//
//	"QDV1" (4B) | salt (16B) | nonce (12B) | AES-256-GCM(明文 JSON + tag)
//
// 口令只在导出/导入时临时输入一次，不做成「主密码」：引入常驻主密码要改
// DEK 的包裹方式、处理忘记口令与锁定联动，还会跟现有启动锁叠成两套概念。
// 这里让备份文件自带口令保护，导入时用**本机新的 DEK** 逐条重新加密，
// 因此换机后库是完整可用的，不依赖原来那把 DPAPI 密钥。
//
// 明文 JSON 只在内存中存在片刻；导出文件里永远是密文。
// 同时支持导入 Chrome / Edge / Bitwarden / 1Password 导出的明文 CSV，
// 让用户不必从零重录已有的浏览器密码。

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

const (
	// PortableMagic 文件头。导入时靠它区分「加密备份」和「明文 CSV」。
	PortableMagic = "QDV1"

	// MaxImportBytes 导入文件大小上限：正常密码库远小于此，挡住误选大文件。
	MaxImportBytes = 16 << 20

	portableVersion = 1
	// kdfIterations PBKDF2-HMAC-SHA256 迭代数。20 万次在桌面端约几十毫秒，
	// 对「只输一次口令」的场景完全够用，又不至于让离线爆破过于便宜。
	kdfIterations   = 200000
	portableSaltLen = 16
	portableKeyLen  = 32
	portableNonce   = 12
	maxImportItems  = 10000
)

// portableAAD 把密文绑死在「本格式 v1」上：别处生成的同口令密文拿过来解不开。
var portableAAD = []byte("quickdock.credentials.portable.v1")

// ErrBadPortable 口令错误 / 文件损坏 / 被篡改，统一成一个文案。
//
// 不区分具体原因是有意的：分开报「magic 不对」「tag 校验失败」等于给离线
// 爆破者一个免费的判别器。
var ErrBadPortable = errors.New("导出口令不对，或文件已损坏")

// portableItem 导出文件里的单条明文。
//
// 不复用 db.Credential：那边 Secret 字段是密文，这里必须是明文，
// 同名混用是最容易写出「导出了一堆打不开的密文」的地方。
type portableItem struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	URL      string `json:"url"`
	Notes    string `json:"notes"`
	Tags     string `json:"tags"`
	Favorite bool   `json:"favorite"`
	UseCount int    `json:"useCount,omitempty"`
}

type portableFile struct {
	Version    int            `json:"version"`
	ExportedAt string         `json:"exportedAt"`
	App        string         `json:"app"`
	Items      []portableItem `json:"items"`
}

// ImportReport 导入结果。失败逐条记，不让一行脏数据挡住整批。
type ImportReport struct {
	Total   int      `json:"total"`
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// ExportPortable 把整库导出成口令加密的字节流。
//
// 走 decrypt 而不是 Reveal：导出是「搬家」，不是「使用」，不该把每条的
// use_count 刷一遍，也不该在审计日志里留下 N 条假的「查看密码」记录。
func ExportPortable(password string) ([]byte, error) {
	if len([]rune(password)) < 8 {
		return nil, errors.New("导出口令至少 8 位")
	}
	list, err := List()
	if err != nil {
		return nil, err
	}
	pf := portableFile{
		Version:    portableVersion,
		ExportedAt: time.Now().Format(time.RFC3339),
		App:        "quickdock",
		Items:      make([]portableItem, 0, len(list)),
	}
	for _, c := range list {
		sec, err := decrypt(c.ID)
		if err != nil {
			return nil, fmt.Errorf("读取「%s」失败: %w", c.Name, err)
		}
		pf.Items = append(pf.Items, portableItem{
			Name: c.Name, Kind: c.Kind, Username: c.Username,
			Secret: sec.Secret, Host: c.Host, Port: c.Port, URL: c.URL,
			Notes: c.Notes, Tags: c.Tags, Favorite: c.Favorite, UseCount: c.UseCount,
		})
	}
	plain, err := json.Marshal(pf)
	if err != nil {
		return nil, fmt.Errorf("序列化密码库失败: %w", err)
	}
	out, err := sealPortable(plain, password)
	if err != nil {
		return nil, err
	}
	logger.I("[credentials] 已导出 %d 条密码到加密备份（%d 字节）", len(pf.Items), len(out))
	return out, nil
}

// ImportAuto 自动识别格式并导入。
//
// 带 QDV1 头 = 加密备份，必须有口令；其余一律按明文 CSV 解析
// （Chrome/Edge/Bitwarden/1Password 导出的那几种表头）。
func ImportAuto(data []byte, password string) (*ImportReport, error) {
	if len(data) == 0 {
		return nil, errors.New("文件是空的")
	}
	if len(data) > MaxImportBytes {
		return nil, fmt.Errorf("文件超过 %d MB，不像是密码库备份", MaxImportBytes>>20)
	}
	if bytes.HasPrefix(data, []byte(PortableMagic)) {
		if strings.TrimSpace(password) == "" {
			return nil, errors.New("这是加密备份，需要当时的导出口令")
		}
		plain, err := openPortable(data, password)
		if err != nil {
			return nil, err
		}
		var pf portableFile
		if err := json.Unmarshal(plain, &pf); err != nil {
			return nil, ErrBadPortable
		}
		if pf.Version != portableVersion {
			return nil, fmt.Errorf("备份版本 %d 不受支持", pf.Version)
		}
		return importItems(pf.Items)
	}
	items, err := parsePortableCSV(data)
	if err != nil {
		return nil, err
	}
	return importItems(items)
}

// sealPortable 用 PBKDF2 派生密钥后 AES-256-GCM 加密。
func sealPortable(plain []byte, password string) ([]byte, error) {
	salt := make([]byte, portableSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("生成导出盐失败: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, kdfIterations, portableKeyLen)
	if err != nil {
		return nil, fmt.Errorf("派生导出密钥失败: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化导出加密器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化导出 GCM 失败: %w", err)
	}
	// openPortable 按固定 12 字节切 nonce，两边必须永远一致。
	if gcm.NonceSize() != portableNonce {
		return nil, fmt.Errorf("导出格式的 nonce 长度已变化，需要升版本")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成导出随机数失败: %w", err)
	}
	out := make([]byte, 0, len(PortableMagic)+len(salt)+len(nonce)+len(plain)+gcm.Overhead())
	out = append(out, []byte(PortableMagic)...)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, gcm.Seal(nil, nonce, plain, portableAAD)...)
	return out, nil
}

// openPortable 解密导出文件；任何结构/校验失败都归到 ErrBadPortable。
func openPortable(data []byte, password string) ([]byte, error) {
	head := len(PortableMagic) + portableSaltLen + portableNonce + 16 // 16 = GCM tag
	if len(data) < head || !bytes.HasPrefix(data, []byte(PortableMagic)) {
		return nil, ErrBadPortable
	}
	salt := data[len(PortableMagic) : len(PortableMagic)+portableSaltLen]
	nonce := data[len(PortableMagic)+portableSaltLen : len(PortableMagic)+portableSaltLen+portableNonce]
	body := data[len(PortableMagic)+portableSaltLen+portableNonce:]
	key, err := pbkdf2.Key(sha256.New, password, salt, kdfIterations, portableKeyLen)
	if err != nil {
		return nil, ErrBadPortable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrBadPortable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrBadPortable
	}
	plain, err := gcm.Open(nil, nonce, body, portableAAD)
	if err != nil {
		return nil, ErrBadPortable
	}
	return plain, nil
}

// importKey 判重键：同一站点 + 同一账号算同一条。
//
// 域名走 normalizeHost（去 www / 去端口），用户名忽略大小写与首尾空白。
// host 为空时退回用名称，避免一堆手动建的条目被压成一条。
func importKey(host, username, name string) string {
	h := normalizeHost(host)
	if h == "" {
		h = strings.ToLower(strings.TrimSpace(name))
	}
	return h + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

// importItems 逐条写库，用本机当前 DEK 重新加密。
//
// 判到同站点同账号就更新（以备份文件为准），否则新建；导入文件里
// 自己有重复项时，后一条会命中刚写入的那条，不会插出两条。
func importItems(items []portableItem) (*ImportReport, error) {
	if len(items) > maxImportItems {
		return nil, fmt.Errorf("一次最多导入 %d 条", maxImportItems)
	}
	rep := &ImportReport{Total: len(items)}
	existing, err := List()
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]db.Credential, len(existing))
	for _, c := range existing {
		k := importKey(c.Host, c.Username, c.Name)
		if _, ok := byKey[k]; !ok {
			byKey[k] = c
		}
	}

	for _, it := range items {
		name := strings.TrimSpace(it.Name)
		host := strings.TrimSpace(it.Host)
		user := strings.TrimSpace(it.Username)
		if name == "" && host == "" && user == "" {
			rep.Skipped++
			continue
		}
		k := importKey(host, user, name)
		cur, found := byKey[k]
		in := db.Credential{
			Name:     name,
			Kind:     it.Kind,
			Username: user,
			Secret:   it.Secret,
			Host:     host,
			Port:     it.Port,
			URL:      strings.TrimSpace(it.URL),
			Notes:    it.Notes,
			Tags:     strings.TrimSpace(it.Tags),
			Favorite: it.Favorite,
		}
		if in.Kind == "" {
			in.Kind = db.CredKindLogin
		}
		if found {
			in.ID = cur.ID
			// 来源是本地溯源信息，导入不该把它改写成 import，
			// 否则 SSH 主机下次改密码会多建一条。
			in.Source = cur.Source
			in.SourceID = cur.SourceID
		} else {
			in.Source = "import"
		}
		if in.Port < 0 || in.Port > 65535 {
			in.Port = 0
		}
		saved, err := Save(in)
		if err != nil {
			rep.Failed++
			if len(rep.Errors) < 5 {
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", in.Name, err))
			}
			continue
		}
		if found {
			rep.Updated++
		} else {
			rep.Created++
		}
		// 用 Save 返回的真实 ID 回填：导入文件里自己有重复项时，
		// 后一条要更新刚写入的这条，而不是因为 ID 为空又插一条新的。
		byKey[k] = *saved
	}
	logger.I("[credentials] 导入完成: 共 %d 条（新建 %d / 更新 %d / 跳过 %d / 失败 %d）",
		rep.Total, rep.Created, rep.Updated, rep.Skipped, rep.Failed)
	return rep, nil
}

// parsePortableCSV 解析各家密码管理器导出的 CSV。
//
// 表头按「包含关键词」认列，不认固定列序：Chrome/Edge 是
// name,url,username,password；Bitwarden 是 login_uri/login_username/
// login_password；1Password 是 Title/Website/Username/Password。
// 找不到密码列就明确报错，而不是导入一堆空密码条目。
func parsePortableCSV(data []byte) ([]portableItem, error) {
	raw := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}) // UTF-8 BOM
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1 // 各家列数不同，甚至同一文件行尾空格数也不同
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV 解析失败: %w", err)
	}
	if len(rows) < 2 {
		return nil, errors.New("CSV 里没有数据行")
	}
	norm := func(s string) string {
		return strings.ToLower(strings.Join(strings.FieldsFunc(strings.TrimSpace(s), func(r rune) bool {
			return r == '_' || r == '-' || r == ' '
		}), ""))
	}
	header := make([]string, len(rows[0]))
	for i, h := range rows[0] {
		header[i] = norm(h)
	}
	// 精确相等优先于「包含」：Bitwarden 的 login_username 归一化后是
	// loginusername，里面含 "name"，若按列顺序先扫到它，name 列就会错指到
	// 账号列。分两轮（先全表精确、再全表包含）才能稳定认对列。
	findCol := func(keys ...string) int {
		for _, k := range keys {
			for i, h := range header {
				if h == k {
					return i
				}
			}
		}
		for _, k := range keys {
			for i, h := range header {
				if strings.Contains(h, k) {
					return i
				}
			}
		}
		return -1
	}
	pwCol := findCol("password", "passwd", "密码")
	userCol := findCol("username", "user", "账号", "用户名")
	urlCol := findCol("url", "uri", "website", "网址", "网站")
	nameCol := findCol("name", "title", "名称", "标题")
	if pwCol < 0 {
		return nil, errors.New("CSV 里找不到密码列（表头需含 password / 密码）")
	}
	cell := func(row []string, idx int) string {
		if idx < 0 || idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}
	out := make([]portableItem, 0, len(rows)-1)
	for _, row := range rows[1:] {
		it := portableItem{
			Kind:     db.CredKindLogin,
			Secret:   cell(row, pwCol),
			Username: cell(row, userCol),
			URL:      cell(row, urlCol),
			Name:     cell(row, nameCol),
		}
		if it.URL != "" {
			if u, err := url.Parse(it.URL); err == nil && u.Hostname() != "" {
				it.Host = u.Hostname()
			} else {
				it.Host = normalizeHost(it.URL)
			}
		}
		if it.Name == "" {
			it.Name = it.Host
		}
		if it.Name == "" && it.Username == "" {
			continue
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil, errors.New("CSV 里没有可导入的条目")
	}
	return out, nil
}
