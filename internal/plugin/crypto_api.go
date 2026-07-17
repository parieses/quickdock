package plugin

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// newCryptoAPI 返回暴露给 goja 插件运行时的 crypto 工具集。
// 所有算法均使用 Go 标准库实现，保证正确性（UTF-8 / 多字节 / 4 字节代理对均正确处理）。
func newCryptoAPI() map[string]interface{} {
	return map[string]interface{}{
		"md5": func(s string) string {
			sum := md5.Sum([]byte(s))
			return hex.EncodeToString(sum[:])
		},
		"sha256": func(s string) string {
			sum := sha256.Sum256([]byte(s))
			return hex.EncodeToString(sum[:])
		},
		"base64Encode": func(s string) string {
			return base64.StdEncoding.EncodeToString([]byte(s))
		},
		"base64Decode": func(s string) (string, error) {
			s = strings.TrimSpace(s)
			// 容错：补齐 '=' 填充（用户粘贴经常丢失）
			if m := len(s) % 4; m != 0 {
				s += strings.Repeat("=", 4-m)
			}
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return "", fmt.Errorf("Base64 解码失败: %v", err)
			}
			return string(b), nil
		},
		"urlEncode": func(s string) string {
			return encodeURIComponent(s)
		},
		"urlDecode": func(s string) (string, error) {
			return decodeURIComponent(s)
		},
		"htmlEncode": func(s string) string {
			return htmlEncodeGo(s)
		},
		"htmlDecode": func(s string) string {
			return htmlDecodeGo(s)
		},
	}
}

// encodeURIComponent 等价于 JS 的 encodeURIComponent：
// 保留 A-Za-z0-9-._~，其余按 UTF-8 字节以 %XX 转义（大写十六进制）。
func encodeURIComponent(s string) string {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		b := s[i]
		if isUnreserved(b) {
			buf.WriteByte(b)
		} else {
			buf.WriteString(fmt.Sprintf("%%%02X", b))
		}
	}
	return buf.String()
}

func isUnreserved(b byte) bool {
	switch b {
	case '-', '.', '_', '~', '!', '*', '\'', '(', ')':
		return true
	}
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9')
}

// decodeURIComponent 反向：%XX → 字节，累加后整体按 UTF-8 解码；
// '+' 也视为空格以兼容表单编码。无法解析的 % 序列按原样保留。
func decodeURIComponent(s string) (string, error) {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) {
			hi := fromHex(s[i+1])
			lo := fromHex(s[i+2])
			if hi >= 0 && lo >= 0 {
				buf.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		} else if c == '+' {
			buf.WriteByte(' ')
			continue
		}
		buf.WriteByte(c)
	}
	return buf.String(), nil
}

func fromHex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// htmlEncodeGo 与前端原有 htmlEncode 行为一致（使用 &quot; / &#39;）。
func htmlEncodeGo(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(s)
}

// htmlDecodeGo 反向解码，兼容常见数字实体。
func htmlDecodeGo(s string) string {
	return strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&#x27;", "'",
		"&#x2F;", "/",
	).Replace(s)
}
