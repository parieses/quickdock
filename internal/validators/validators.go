package validators

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidationErr 配置验证错误
type ValidationErr struct {
	Field string
	Msg   string
}

func (e *ValidationErr) Error() string {
	return fmt.Sprintf("配置验证失败 [%s]: %s", e.Field, e.Msg)
}

// Validator 验证器接口
type Validator interface {
	Validate() error
}

// ---- 热键配置验证 ----

// HotkeyConfig 热键配置
type HotkeyConfig struct {
	Modifiers int    `json:"modifiers"`
	VK        int    `json:"vk"`
	Label     string `json:"label"`
}

// Validate 验证热键配置
func (c *HotkeyConfig) Validate() error {
	if c == nil {
		return &ValidationErr{Field: "hotkey", Msg: "配置为空"}
	}

	// 修饰键范围检查 (0-15)
	if c.Modifiers < 0 || c.Modifiers > 15 {
		return &ValidationErr{Field: "modifiers", Msg: fmt.Sprintf("值 %d 超出范围 (0-15)", c.Modifiers)}
	}

	// 虚拟键码范围检查 (0-255)
	if c.VK < 0 || c.VK > 255 {
		return &ValidationErr{Field: "vk", Msg: fmt.Sprintf("值 %d 超出范围 (0-255)", c.VK)}
	}

	// 标签非空检查
	if c.Label == "" {
		return &ValidationErr{Field: "label", Msg: "标签不能为空"}
	}

	return nil
}

// ---- 时间解析验证 ----

// ParseTimeResult 时间解析结果
type ParseTimeResult struct {
	Hour   int
	Minute int
	Second int
	Valid  bool
}

// ParseTimeOfDay 解析 HH:MM 或 HH:MM:SS 格式
func ParseTimeOfDay(s string) (*ParseTimeResult, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, &ValidationErr{Field: "time", Msg: "时间不能为空"}
	}

	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, &ValidationErr{Field: "time", Msg: "格式应为 HH:MM 或 HH:MM:SS"}
	}

	h, e1 := parseIntParts(parts[0], 0, 23)
	if e1 != nil {
		return nil, e1
	}

	m, e2 := parseIntParts(parts[1], 0, 59)
	if e2 != nil {
		return nil, e2
	}

	sec := 0
	if len(parts) == 3 {
		var e3 error
		sec, e3 = parseIntParts(parts[2], 0, 59)
		if e3 != nil {
			return nil, e3
		}
	}

	return &ParseTimeResult{Hour: h, Minute: m, Second: sec, Valid: true}, nil
}

func parseIntParts(s string, min, max int) (int, *ValidationErr) {
	var result int
	_, err := fmt.Sscanf(s, "%d", &result)
	if err != nil {
		return 0, &ValidationErr{Field: "time", Msg: fmt.Sprintf("无效的数字: %s", s)}
	}
	if result < min || result > max {
		return 0, &ValidationErr{Field: "time", Msg: fmt.Sprintf("值 %d 超出范围 (%d-%d)", result, min, max)}
	}
	return result, nil
}

// ---- 路径验证 ----

// ValidatePath 验证路径安全性
func ValidatePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return &ValidationErr{Field: "path", Msg: "路径不能为空"}
	}

	// 检查是否是绝对路径
	if !regexp.MustCompile(`^[A-Za-z]:`).MatchString(path) && !strings.HasPrefix(path, "/") {
		return &ValidationErr{Field: "path", Msg: "必须是绝对路径"}
	}

	// 检查路径穿越
	cleanPath := strings.ReplaceAll(path, "\\", "/")
	parts := strings.Split(cleanPath, "/")
	for _, part := range parts {
		if part == ".." {
			return &ValidationErr{Field: "path", Msg: "路径包含非法字符 '..'"}
		}
	}

	return nil
}

// ---- 名称验证 ----

// ValidateName 验证名称合法性
func ValidateName(name string, minLen, maxLen int) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return &ValidationErr{Field: "name", Msg: "名称不能为空"}
	}

	if len(name) < minLen {
		return &ValidationErr{Field: "name", Msg: fmt.Sprintf("名称长度不能少于 %d 个字符", minLen)}
	}

	if len(name) > maxLen {
		return &ValidationErr{Field: "name", Msg: fmt.Sprintf("名称长度不能超过 %d 个字符", maxLen)}
	}

	// 检查非法字符
	if regexp.MustCompile(`[<>\:\"|?*]`).MatchString(name) {
		return &ValidationErr{Field: "name", Msg: "名称包含非法字符"}
	}

	return nil
}

// ---- URL 验证 ----

// ValidateURL 验证 URL 格式
func ValidateURL(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return &ValidationErr{Field: "url", Msg: "URL 不能为空"}
	}

	// 简单的 URL 格式验证
	pattern := `^https?://[a-zA-Z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+$`
	if !regexp.MustCompile(pattern).MatchString(url) {
		return &ValidationErr{Field: "url", Msg: "URL 格式无效"}
	}

	return nil
}
