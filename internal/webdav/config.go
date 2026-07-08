package webdav

import (
	"bytes"
	"strings"
)

// MarshalConfig 将 Config 序列化为 JSON 字符串（手动构建，无外部依赖）
func MarshalConfig(cfg *Config) (string, error) {
	if cfg == nil {
		return `{}`, nil
	}
	var buf bytes.Buffer
	buf.WriteString(`{"url":`)
	buf.WriteString(jsonEscape(cfg.URL))
	buf.WriteString(`,"username":`)
	buf.WriteString(jsonEscape(cfg.Username))
	buf.WriteString(`,"password":`)
	buf.WriteString(jsonEscape(cfg.Password))
	buf.WriteString(`}`)
	return buf.String(), nil
}

// UnmarshalConfig 从 JSON 字符串解析 Config
func UnmarshalConfig(s string) *Config {
	cfg := &Config{}
	if s == "" || s == "{}" {
		return cfg
	}
	cfg.URL = extractJSONField(s, "url")
	cfg.Username = extractJSONField(s, "username")
	cfg.Password = extractJSONField(s, "password")
	return cfg
}

func jsonEscape(s string) string {
	var buf bytes.Buffer
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			buf.WriteString(`\\`)
		case '"':
			buf.WriteString(`\"`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
	return buf.String()
}

func extractJSONField(jsonStr, field string) string {
	prefix := `"` + field + `":`
	idx := strings.Index(jsonStr, prefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(prefix)
	// 跳过空白
	for start < len(jsonStr) && jsonStr[start] == ' ' {
		start++
	}
	if start >= len(jsonStr) || jsonStr[start] != '"' {
		return ""
	}
	start++ // 跳过开头的引号
	end := start
	for end < len(jsonStr) {
		if jsonStr[end] == '\\' {
			end += 2
			continue
		}
		if jsonStr[end] == '"' {
			break
		}
		end++
	}
	if end >= len(jsonStr) {
		return ""
	}
	return unescapeJSON(jsonStr[start:end])
}

func unescapeJSON(s string) string {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '"':
				buf.WriteByte('"')
			case '\\':
				buf.WriteByte('\\')
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			default:
				buf.WriteByte(s[i+1])
			}
			i++
		} else {
			buf.WriteByte(s[i])
		}
	}
	return buf.String()
}
