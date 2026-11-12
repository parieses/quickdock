// Package credentials 密码库引擎：DEK 生命周期 + 条目加解密 + 匹配排序。
//
// 为什么不把加解密塞进 internal/db：db 层只认「不透明字符串」，
// 而这里的密钥是一条常驻内存的 DEK，有加载/失效/损坏三种状态，
// 还必须在「应用锁定」时被清零。把有状态的东西留在引擎侧，
// db 层就仍然是纯存储，测试与替换都不用关心密钥怎么来的。
//
// 分层（与 internal/lock 同款单例布局）：
//
//	dek.go     密钥的生成、包裹、懒加载、清零
//	crypto.go  AES-256-GCM 单条加解密
//	store.go   业务读写（封装 db 层 + 加解密）
//	match.go   按 scene 匹配与打分排序
//
// 安全边界（必须写清楚，否则会被误当成「绝对安全」）：
//   - DEK 由 Windows DPAPI 包裹后存 app_state，边界是「同一 Windows 用户」。
//     同用户下的其它进程理论上也能解出 DEK —— 这是本机桌面的固有约束，
//     与 ssh_hosts.password 的现状一致，不是本包引入的弱点。
//   - 非 Windows 平台 platform.EncryptSecret 退化为 base64（见 crypto_unix.go），
//     此时密码库只是「混淆」而非加密。调用方（前端）必须明示这一点。
package credentials

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// settingDEK app_state 里存放「被 DPAPI 包裹的 DEK」的键。
//
// 与启动锁的密码哈希同表存放。app_state 不进快照/云备份（snapshot.go 明确排除），
// 所以密码库天然不会随备份离开本机。
const settingDEK = "cred_dek"

// dekLen AES-256 的密钥长度。
const dekLen = 32

var (
	mu   sync.Mutex
	conn *db.Database
	// dek 内存中的数据密钥；nil 表示未加载或已被清零。
	dek []byte
)

// ErrLocked 密钥尚未就绪（DB 未打开，或已被 Wipe 且当前无法重建）。
var ErrLocked = fmt.Errorf("密码库未解锁")

// Init 绑定数据库。只绑引用，不加载 DEK —— 启动时不碰 DPAPI，
// 免得「打开应用」多一次跨进程调用，也免得从没设过密码的用户白付这份成本。
func Init(d *db.Database) {
	mu.Lock()
	conn = d
	dek = nil
	mu.Unlock()
}

// Wipe 清空内存中的 DEK 并返回是否有内容被清除。
//
// 由启动锁的状态变化触发（见 services/credentials 的 initLockBridge）：
// 用户按锁定热键后，即便有人接管了这个进程的内存，也拿不到能解开密文的密钥。
// 解锁后下一次访问会自动用 DPAPI 重新解出 DEK，不需要用户再输一次密码。
func Wipe() bool {
	mu.Lock()
	defer mu.Unlock()
	if dek == nil {
		return false
	}
	for i := range dek {
		dek[i] = 0
	}
	dek = nil
	return true
}

// Loaded 报告 DEK 是否已在内存中（诊断用：区分「没 unlock」与「解不开」）。
func Loaded() bool {
	mu.Lock()
	defer mu.Unlock()
	return dek != nil
}

// ensureDEK 返回可用的 DEK，必要时从 app_state 解出或首次生成。
//
// 并发安全：整段持锁。DPAPI 调用是毫秒级，且只在「首次访问」发生一次，
// 不会成为热点路径；为了它引入 double-check 反而容易出现两份 DEK。
func ensureDEK() ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()

	if dek != nil {
		return dek, nil
	}
	if conn == nil {
		return nil, ErrLocked
	}

	raw, err := conn.GetSetting(settingDEK)
	// GetSetting 在「键不存在」时返回 sql.ErrNoRows，而首次使用本来就没有这条记录。
	// 早期版本把任何 error 都当致命错误，于是永远走不到下面的「首次生成」分支 ——
	// 表现为密码库从第一天起就不可用：读取密钥失败: sql: no rows in result set。
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: 读取密钥失败: %v", ErrLocked, err)
	}

	if raw == "" {
		// 首次使用：生成新 DEK 并用 DPAPI 包裹后落库。
		key := make([]byte, dekLen)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("生成密码库密钥失败: %w", err)
		}
		wrapped, err := platform.EncryptSecret(b64.EncodeToString(key))
		if err != nil {
			return nil, fmt.Errorf("密码库密钥加密失败: %w", err)
		}
		if err := conn.SetSetting(settingDEK, wrapped); err != nil {
			return nil, fmt.Errorf("密码库密钥落库失败: %w", err)
		}
		dek = key
		logger.I("[credentials] 已生成新的密码库密钥")
		return dek, nil
	}

	plain, err := platform.DecryptSecret(raw)
	if err != nil {
		// 换了 Windows 账户/机器，或 DPAPI 数据被破坏。
		// 不能静默重建密钥 —— 那会把已有密文变成永远解不开的垃圾，
		// 用户看到的是「密码全变乱码」。这里明确报错，让人去导出/重录。
		return nil, fmt.Errorf("%w: 密钥解密失败（可能更换了系统账户）: %v", ErrLocked, err)
	}
	key, err := b64.DecodeString(plain)
	if err != nil || len(key) != dekLen {
		return nil, fmt.Errorf("%w: 密钥格式异常", ErrLocked)
	}
	dek = key
	return dek, nil
}
