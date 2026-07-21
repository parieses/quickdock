//go:build darwin

package platform

import "encoding/base64"

// EncryptSecret macOS 占位实现：DPAPI 不可用，退化为 base64（非加密，仅避免明文落库）。
// 上层调用方应理解此明文兜底的安全性边界；mac 分支后续可接入钥匙串（Keychain）等效加密。
func EncryptSecret(plain string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(plain)), nil
}

// DecryptSecret 解密 base64 字符串
func DecryptSecret(cipher string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(cipher)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
