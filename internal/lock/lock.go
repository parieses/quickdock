// Package lock 维护 QuickDock 的「启动锁」：密码哈希、失败退避、一次性恢复码。
//
// 状态只有一个真相源（本包内存变量）：前端的锁层、托盘菜单、MCP 工具分发
// 都只是它的投影。之所以不放在 services 子包里——internal/mcp 需要判断锁定态，
// 而 services 子包 import 根包会与 notify.go 等根包代码成环。
//
// 存储走 app_state（GetSetting/SetSetting）。该表被 snapshot.go 明确排除在
// 同步与备份之外，因此密码不会随 WebDAV 备份外泄；代价是换机恢复后等于没设过
// 密码，这一点在设置页文案里已说明。
package lock

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const (
	// settingPasswordHash 密码的 bcrypt 哈希（再经 DPAPI 加密后落库）。
	settingPasswordHash = "startup_password_hash"
	// settingRecoveryHash 一次性恢复码的 bcrypt 哈希。
	settingRecoveryHash = "startup_password_recovery"
	// settingPasswordLen 密码的字符数（明文整数）。
	//
	// 只为「输满即自动解锁」服务：前端拿到长度后不必再按一次回车。它和哈希同表
	// 存放（app_state 不进快照/备份），刻意不加密 —— 长度本身不是秘密，肩窥与
	// 键盘记录最先拿到的就是它；加密只会多一层无谓判断。旧版本设过密码的用户
	// 没有这个键，读出来是 0，前端据此退回「回车提交」的老行为。
	settingPasswordLen = "startup_password_len"

	// freeAttempts 免退避的连续失败次数。
	freeAttempts = 5
	// baseBackoff 第 freeAttempts 次失败后的首次退避时长，之后每次翻倍。
	baseBackoff = 30 * time.Second
	// maxBackoff 退避上限。
	maxBackoff = 5 * time.Minute
	// minPasswordLen 密码最短长度（按字符计，不按字节）。
	minPasswordLen = 6
	// recoveryLen 恢复码长度（Crockford Base32 字符数，约 80 bit 熵）。
	recoveryLen = 16
)

// 对外错误。前端按 Code!=0 展示 Msg，同时读 LockStatus 的 retryAfterSec 做倒计时。
var (
	ErrNotInitialized = errors.New("数据库尚未就绪")
	ErrNoPassword     = errors.New("尚未设置启动密码")
	ErrAlreadySet     = errors.New("启动密码已设置")
	ErrPasswordShort  = fmt.Errorf("密码至少 %d 个字符", minPasswordLen)
	ErrBadPassword    = errors.New("密码错误")
	ErrTooManyTries   = errors.New("尝试次数过多，请稍后再试")
	ErrBadRecovery    = errors.New("恢复码无效或已使用")
)

// manager 锁状态机。所有字段由 mu 保护；bcrypt 比对（约 50~100ms）在锁外做，
// 避免热键回调里的 IsLocked 被一次校验卡住。
type manager struct {
	mu  sync.Mutex
	db  *db.Database
	pwd []byte // bcrypt 哈希；空表示未设密码
	rec []byte // 恢复码的 bcrypt 哈希
	// pwdLen 密码字符数，供前端「输满自动解锁」；0 表示未知（旧数据 / 未设密码）。
	pwdLen int
	lock   bool // 是否处于锁定态
	// fails/until 连续失败计数与退避截止时间。只存内存：重启后清零，
	// 否则一次遗忘的密码会把用户锁到下一次开机。
	fails int
	until time.Time
}

var std = &manager{}

var (
	subMu     sync.Mutex
	listeners []func(locked bool)
)

// Subscribe 注册锁状态变化监听（锁定与解锁都会回调）。
//
// 回调在状态更新、内部锁释放之后执行，因此回调里可以安全调用 IsLocked。
// 启动时 Init 触发的「进入锁定态」也会通知一次，托盘菜单靠它从解锁版切到锁定版。
func Subscribe(fn func(locked bool)) {
	if fn == nil {
		return
	}
	subMu.Lock()
	listeners = append(listeners, fn)
	subMu.Unlock()
}

func notify(locked bool) {
	subMu.Lock()
	fns := make([]func(bool), len(listeners))
	copy(fns, listeners)
	subMu.Unlock()
	for _, fn := range fns {
		fn(locked)
	}
}

// Init 绑定数据库并载入已有凭据；已设密码时直接进入锁定态。
//
// 由 AppService.ServiceStartup 在 DB 打开后调用一次。此时主窗口可能已经创建，
// 所以前端不能靠「建窗时就知道锁没锁」，而要靠首次 LockStatus 查询与启动遮罩。
func Init(d *db.Database) {
	std.mu.Lock()
	std.db = d
	std.mu.Unlock()
	if d == nil {
		return
	}
	pwd := readHash(settingPasswordHash)
	rec := readHash(settingRecoveryHash)
	var pwdLen int
	if len(pwd) > 0 {
		pwdLen = readPasswordLen()
	}
	std.mu.Lock()
	std.pwd, std.rec, std.pwdLen = pwd, rec, pwdLen
	std.lock = len(pwd) > 0
	locked := std.lock
	std.mu.Unlock()
	if locked {
		logger.I("[lock] 已设启动密码，进入锁定态")
		notify(true)
	}
}

func readHash(key string) []byte {
	std.mu.Lock()
	d := std.db
	std.mu.Unlock()
	if d == nil {
		return nil
	}
	raw, err := d.GetSetting(key)
	if err != nil || raw == "" {
		return nil // ErrNoRows 是常态：没设过密码
	}
	plain, err := platform.DecryptSecret(raw)
	if err != nil {
		logger.W("[lock] %s 解密失败，按未设置处理: %v", key, err)
		return nil
	}
	return []byte(plain)
}

// readPasswordLen 读取密码字符数；未设置或旧数据缺失时返回 0（前端据此退回回车提交）。
func readPasswordLen() int {
	std.mu.Lock()
	d := std.db
	std.mu.Unlock()
	if d == nil {
		return 0
	}
	raw, err := d.GetSetting(settingPasswordLen)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// writePasswordLen 落库密码字符数；0 表示清除（移除密码 / 恢复码重置）。
func writePasswordLen(n int) error {
	std.mu.Lock()
	d := std.db
	std.mu.Unlock()
	if d == nil {
		return ErrNotInitialized
	}
	return d.SetSetting(settingPasswordLen, strconv.Itoa(n))
}

// rememberPasswordLen 把新密码长度写进内存与库，并同步给前端。
//
// 写库失败不阻断设密流程：密码已经设好了，只是少了「输满自动解锁」这一层便利，
// 用户仍可按回车。为此只记一条警告，不把错误抛给调用方（否则用户会看到「设置失败」
// 却已经设上了，反而更慌）。
func rememberPasswordLen(n int) {
	std.mu.Lock()
	std.pwdLen = n
	std.mu.Unlock()
	if err := writePasswordLen(n); err != nil {
		logger.W("[lock] 密码长度落库失败（不影响解锁，回车仍可用）: %v", err)
	}
}

func writeHash(key string, hash []byte) error {
	std.mu.Lock()
	d := std.db
	std.mu.Unlock()
	if d == nil {
		return ErrNotInitialized
	}
	if len(hash) == 0 {
		return d.SetSetting(key, "")
	}
	enc, err := platform.EncryptSecret(string(hash))
	if err != nil {
		return fmt.Errorf("凭据加密失败: %w", err)
	}
	return d.SetSetting(key, enc)
}

// IsLocked 报告当前是否处于锁定态。
func IsLocked() bool {
	std.mu.Lock()
	defer std.mu.Unlock()
	return std.lock
}

// HasPassword 报告是否设置过启动密码。
func HasPassword() bool {
	std.mu.Lock()
	defer std.mu.Unlock()
	return len(std.pwd) > 0
}

// PasswordLength 返回密码字符数，供锁层「输满即自动解锁」；0 表示未知。
func PasswordLength() int {
	std.mu.Lock()
	defer std.mu.Unlock()
	return std.pwdLen
}

// RetryAfter 返回距离可以再次尝试还差多久；0 表示可以立刻尝试。
func RetryAfter() time.Duration {
	std.mu.Lock()
	defer std.mu.Unlock()
	return std.retryAfterLocked()
}

func (m *manager) retryAfterLocked() time.Duration {
	if m.until.IsZero() {
		return 0
	}
	if d := time.Until(m.until); d > 0 {
		return d
	}
	return 0
}

// Lock 进入锁定态（主动锁定 / 锁定热键）。已解锁状态下幂等。
func Lock() {
	std.mu.Lock()
	already := std.lock
	std.mu.Unlock()
	if already {
		return
	}
	std.mu.Lock()
	std.lock = true
	std.mu.Unlock()
	logger.I("[lock] 已锁定")
	notify(true)
}

// checkPassword 只做校验与退避计数，不改变锁定态。
//
// 改密 / 移除密码在「已解锁」状态下也会调用它——若复用 Verify，解锁态下
// Verify 直接返回 nil，旧密码校验就被绕过了。
func (m *manager) checkPassword(pwd string) error {
	m.mu.Lock()
	if wait := m.retryAfterLocked(); wait > 0 {
		m.mu.Unlock()
		return fmt.Errorf("%w（还需 %d 秒）", ErrTooManyTries, int(wait.Seconds()+0.999))
	}
	hash := m.pwd
	m.mu.Unlock()

	if len(hash) == 0 {
		return ErrNoPassword
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(pwd)); err != nil {
		m.mu.Lock()
		m.recordFailLocked()
		m.mu.Unlock()
		return ErrBadPassword
	}
	m.mu.Lock()
	m.fails, m.until = 0, time.Time{}
	m.mu.Unlock()
	return nil
}

// recordFailLocked 记一次失败并按次数设置退避窗口。
func (m *manager) recordFailLocked() {
	m.fails++
	if d := backoffFor(m.fails); d > 0 {
		m.until = time.Now().Add(d)
		logger.W("[lock] 密码连续失败 %d 次，退避 %v", m.fails, d)
	}
}

func backoffFor(fails int) time.Duration {
	if fails < freeAttempts {
		return 0
	}
	n := fails - freeAttempts
	if n > 6 { // 30s<<6 = 32min，早已超过上限，直接封顶避免移位溢出
		return maxBackoff
	}
	d := baseBackoff << n
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// Verify 校验密码，成功则解锁。
func Verify(pwd string) error {
	std.mu.Lock()
	locked := std.lock
	std.mu.Unlock()
	if !locked {
		return nil // 幂等：已解锁时重复校验按成功处理
	}
	if err := std.checkPassword(pwd); err != nil {
		return err
	}
	std.mu.Lock()
	std.lock = false
	lenUnknown := std.pwdLen <= 0
	std.mu.Unlock()
	if lenUnknown {
		// 旧数据自愈：这个键是「输满自动解锁」加进来之后才有的，老用户库里只有哈希。
		// 既然这次密码已经被验证通过，长度就是确定信息，顺手补上——不必逼用户
		// 特意去改一次密码才能用上新行为。下次锁定起就生效。
		rememberPasswordLen(len([]rune(pwd)))
	}
	logger.I("[lock] 密码校验通过，已解锁")
	notify(false)
	return nil
}

// SetPassword 首次设置密码，返回只展示一次的恢复码明文。
// 设置后不立即锁定：用户刚设完密码就被挡在门外是最差的体验。
func SetPassword(pwd string) (string, error) {
	if err := validatePassword(pwd); err != nil {
		return "", err
	}
	std.mu.Lock()
	exists := len(std.pwd) > 0
	std.mu.Unlock()
	if exists {
		return "", ErrAlreadySet
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	code, codeHash, err := newRecovery()
	if err != nil {
		return "", err
	}
	if err := writeHash(settingPasswordHash, hash); err != nil {
		return "", err
	}
	if err := writeHash(settingRecoveryHash, codeHash); err != nil {
		return "", err
	}
	std.mu.Lock()
	std.pwd, std.rec = hash, codeHash
	std.mu.Unlock()
	// 长度按「字符」计（rune），前端也用 code point 计，两边才对得上（emoji 是 2 个 UTF-16 单元）。
	rememberPasswordLen(len([]rune(pwd)))
	logger.I("[lock] 启动密码已设置（恢复码已生成）")
	return code, nil
}

// ChangePassword 校验旧密码后更换密码，同时作废旧恢复码并生成新的。
func ChangePassword(oldPwd, newPwd string) (string, error) {
	if err := validatePassword(newPwd); err != nil {
		return "", err
	}
	if err := std.checkPassword(oldPwd); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	code, codeHash, err := newRecovery()
	if err != nil {
		return "", err
	}
	if err := writeHash(settingPasswordHash, hash); err != nil {
		return "", err
	}
	if err := writeHash(settingRecoveryHash, codeHash); err != nil {
		return "", err
	}
	std.mu.Lock()
	std.pwd, std.rec = hash, codeHash
	std.mu.Unlock()
	rememberPasswordLen(len([]rune(newPwd)))
	logger.I("[lock] 启动密码已修改（旧恢复码作废）")
	return code, nil
}

// RemovePassword 校验当前密码后清除密码与恢复码，并解除锁定。
func RemovePassword(pwd string) error {
	if err := std.checkPassword(pwd); err != nil {
		return err
	}
	if err := writeHash(settingPasswordHash, nil); err != nil {
		return err
	}
	if err := writeHash(settingRecoveryHash, nil); err != nil {
		return err
	}
	std.mu.Lock()
	wasLocked := std.lock
	std.pwd, std.rec = nil, nil
	std.lock = false
	std.mu.Unlock()
	rememberPasswordLen(0)
	logger.I("[lock] 启动密码已移除")
	if wasLocked {
		notify(false)
	}
	return nil
}

// Recover 校验一次性恢复码。通过后清除密码并解锁，恢复码同时作废。
//
// 语义刻意做成「恢复 = 重置」而不是「恢复 = 临时解锁」：恢复码只在设置密码时
// 展示一次，用户多半是彻底忘了密码才用它，此时留着旧密码只会让人再忘一次。
func Recover(code string) error {
	norm := normalizeCode(code)
	if len(norm) != recoveryLen {
		return ErrBadRecovery
	}
	std.mu.Lock()
	if wait := std.retryAfterLocked(); wait > 0 {
		std.mu.Unlock()
		return fmt.Errorf("%w（还需 %d 秒）", ErrTooManyTries, int(wait.Seconds()+0.999))
	}
	rec := std.rec
	std.mu.Unlock()

	if len(rec) == 0 {
		return ErrBadRecovery
	}
	if err := bcrypt.CompareHashAndPassword(rec, []byte(norm)); err != nil {
		std.mu.Lock()
		std.recordFailLocked()
		std.mu.Unlock()
		return ErrBadRecovery
	}
	if err := writeHash(settingPasswordHash, nil); err != nil {
		return err
	}
	if err := writeHash(settingRecoveryHash, nil); err != nil {
		return err
	}
	std.mu.Lock()
	std.pwd, std.rec = nil, nil
	std.lock = false
	std.mu.Unlock()
	rememberPasswordLen(0)
	logger.I("[lock] 已用恢复码清除启动密码并解锁")
	notify(false)
	return nil
}

func validatePassword(pwd string) error {
	if len([]rune(pwd)) < minPasswordLen {
		return ErrPasswordShort
	}
	return nil
}

// newRecovery 生成恢复码明文与它的 bcrypt 哈希。
//
// 明文形如 QD7K-3M2F-9XAB-4TYE，只在这里产生一次，之后库里只留哈希——
// 丢了就再也没法反推，这是「一次性」的技术兑现方式。
func newRecovery() (code string, hash []byte, err error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford Base32：去掉 I L O U
	raw := make([]byte, recoveryLen)
	if _, err = rand.Read(raw); err != nil {
		return "", nil, err
	}
	buf := make([]byte, recoveryLen)
	for i, b := range raw {
		// 256 % 32 == 0，取模不引入偏置
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	s := string(buf)
	code = fmt.Sprintf("%s-%s-%s-%s", s[0:4], s[4:8], s[8:12], s[12:16])
	hash, err = bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, err
	}
	return code, hash, nil
}

// normalizeCode 归一化用户输入：忽略连字符/空格，按 Crockford 规则把易混字符折回数字。
func normalizeCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", " ", "", "_", "", "\t", "").Replace(s)
	s = strings.NewReplacer("I", "1", "L", "1", "O", "0").Replace(s)
	return s
}
