package plugin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// plugincrypto.go —— 宿主侧加密能力，供前端插件替代不可用的 window.crypto.subtle。
//
// 背景：插件前端跑在 iframe 里，sandbox 刻意不含 allow-same-origin
// （见 frontend/src/components/PluginFrame.vue 的安全注释），于是拿到的是
// opaque origin，window.crypto.subtle 在该上下文里**不可用**。
// 结果就是 vault 这类需要加密的插件只能降级成异或混淆——那只能挡住
// 「顺手一瞥」，挡不住拿到数据文件的人。
//
// 这里的做法是把加密放到宿主持有（主进程，正常 origin）完成，插件通过
// host.crypto.encrypt / host.crypto.decrypt 桥接调用。宿主持有密钥材料
// 的时间仅限于单次调用内部，不做任何持久化。
//
// 参数一律走 base64 文本：iframe 与宿主之间只有 JSON-RPC 一条通道，
// 传二进制要额外约定编码，而 base64 是两端都无需依赖的选择。

const (
	// pluginCryptoMaxPlain = 1 MiB。密码库这类场景远超足够；
	// 设上限是防止插件误把整个大文件塞进来，让宿主内存与 CPU 被单次调用占满。
	pluginCryptoMaxPlain = 1 << 20
	// pluginCryptoIterations PBKDF2 迭代次数。与 vault 前端的 100000 对齐，
	// 保证同一份密码库在新旧两种路径下派生出的密钥一致（可互相解密）。
	pluginCryptoIterations = 100000
	// pluginCryptoKeyLen AES-256
	pluginCryptoKeyLen = 32
	// pluginCryptoSaltLen 128-bit salt
	pluginCryptoSaltLen = 16
)

var (
	// ErrCryptoPlainTooLarge 明文超过上限
	ErrCryptoPlainTooLarge = errors.New("明文超过 1 MiB 上限")
	// ErrCryptoBadBase64 入参不是合法 base64
	ErrCryptoBadBase64 = errors.New("base64 解码失败")
	// ErrCryptoDecryptFailed 解密失败（密码错误 / 数据被篡改 / 参数不合法）
	ErrCryptoDecryptFailed = errors.New("解密失败：密码错误或数据已损坏")
)

// B64 解码一个 base64 字符串，空白与 URL-safe 变体都接受。
// 插件可能手写标准 base64，也可能用 URL-safe 变体，两端都要能解。
func B64(s string) ([]byte, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, nil
	}
	// 先按标准 base64 试，失败再试 URL-safe（Go 的标准库对 padding 要求严格）
	if b, err := base64.StdEncoding.DecodeString(t); err == nil {
		return b, nil
	}
	b, err := base64.URLEncoding.DecodeString(t)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCryptoBadBase64, err)
	}
	return b, nil
}

// B64Enc 编码为**标准** base64（带 padding），便于插件直接落进 JSON 存储。
func B64Enc(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// deriveKey 用 PBKDF2-SHA256 从口令派生 AES 密钥。
// 与 vault 前端的 derive() 参数完全一致：PBKDF2 / SHA-256 / 100000 次 / 256 位。
func deriveKey(password string, salt []byte) []byte {
	return pbkdf2.Key([]byte(password), salt, pluginCryptoIterations, pluginCryptoKeyLen, sha256.New)
}

// EncryptForPlugin AES-256-GCM 加密，返回 {iv, data}（均为标准 base64）。
//
// salt 由调用方传入（插件负责生成并随密文一起存储）——宿主不生成、不记住 salt，
// 这样「盐」与「密文」始终成对出现在插件自己的存储里，不会出现宿主换了会话
// 就解不开旧数据的情况。
func EncryptForPlugin(password string, salt, plaintext []byte) (ivB64, dataB64 string, err error) {
	if len(plaintext) > pluginCryptoMaxPlain {
		return "", "", fmt.Errorf("%w（当前 %d 字节）", ErrCryptoPlainTooLarge, len(plaintext))
	}
	if len(salt) == 0 {
		return "", "", fmt.Errorf("salt 不能为空")
	}
	key := deriveKey(password, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	iv := make([]byte, gcm.NonceSize())
	// crypto/rand 读取失败时必须中止：用可预测的 IV 会让 GCM 的认证强度大打折扣
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", "", fmt.Errorf("读取随机 IV 失败: %w", err)
	}
	// GCM 的 Seal 会把认证标签附在密文尾部，无需单独存 tag
	sealed := gcm.Seal(nil, iv, plaintext, nil)
	return B64Enc(iv), B64Enc(sealed), nil
}

// DecryptForPlugin AES-256-GCM 解密。
// 失败时统一返回 ErrCryptoDecryptFailed，不区分「密码错」与「数据损坏」——
// 区分开只会给攻击者提供更多信息，对合法用户也没有额外帮助。
func DecryptForPlugin(password string, salt []byte, ivB64, dataB64 string) ([]byte, error) {
	if len(salt) == 0 {
		return nil, fmt.Errorf("salt 不能为空")
	}
	iv, err := B64(ivB64)
	if err != nil {
		return nil, err
	}
	data, err := B64(dataB64)
	if err != nil {
		return nil, err
	}
	key := deriveKey(password, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(iv) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w：IV 长度应为 %d 字节", ErrCryptoDecryptFailed, gcm.NonceSize())
	}
	plain, err := gcm.Open(nil, iv, data, nil)
	if err != nil {
		// 密码错 / 密文被改 / tag 不符，都在这里收敛为同一错误
		return nil, fmt.Errorf("%w（%v）", ErrCryptoDecryptFailed, err)
	}
	return plain, nil
}

// RandomForPlugin 生成 n 字节密码学随机数，返回标准 base64。
// 供插件生成 salt / IV / 随机密码用——iframe 里 crypto.getRandomValues 其实可用
// （非 subtle），但走宿主能少一个「在某些 WebView 版本上表现不一致」的变量。
func RandomForPlugin(n int) (string, error) {
	if n <= 0 || n > 4096 {
		return "", fmt.Errorf("长度须在 1..4096 之间（当前 %d）", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("读取随机数失败: %w", err)
	}
	return B64Enc(buf), nil
}
