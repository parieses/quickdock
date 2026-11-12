package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// b64 统一用标准编码：密文要能安全地存进 TEXT 列、也能贴进导出文件。
var b64 = base64.StdEncoding

// cipherPrefix 密文版本前缀。
//
// 带前缀是必需的：将来换算法（比如加个 Argon2id 派生层）时，
// 靠前缀就能同时读新旧两种密文，不必写一次全库重加密的迁移。
const cipherPrefix = "v1:"

// aad 附加认证数据。固定串即可：它的作用是让「别处生成的密文」
// 拿到这里解会直接失败，而不是解出一串乱码当密码用。
var aad = []byte("quickdock.credentials.v1")

// ErrCipherFormat 密文格式不对（前缀缺失/段数不对/base64 损坏）。
var ErrCipherFormat = errors.New("密文格式无效")

// seal 用 DEK 加密明文，返回 "v1:<nonce_b64>:<ciphertext_b64>"。
//
// 每次调用都生成新的随机 nonce：GCM 的 nonce 复用会直接泄漏明文异或值，
// 这是少数「绝对不能省随机数」的地方。
func seal(key []byte, plain string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("初始化加密器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	// 把 nonce 放在密文前面一起返回，省一个字段；解析时按 NonceSize 切开。
	out := gcm.Seal(nonce, nonce, []byte(plain), aad)
	return cipherPrefix + b64.EncodeToString(out), nil
}

// open 解密 seal 的产物。
func open(key []byte, cipherText string) (string, error) {
	if !strings.HasPrefix(cipherText, cipherPrefix) {
		return "", ErrCipherFormat
	}
	raw, err := b64.DecodeString(strings.TrimPrefix(cipherText, cipherPrefix))
	if err != nil {
		return "", ErrCipherFormat
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("初始化解密器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrCipherFormat
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, aad)
	if err != nil {
		// 认证失败只有两种可能：密钥不是这条密文的密钥，或密文被改过。
		// 不把底层错误原样抛给前端（它只说明「校验失败」，对用户无意义）。
		return "", fmt.Errorf("解密失败（密钥不匹配或数据已损坏）")
	}
	return string(plain), nil
}
