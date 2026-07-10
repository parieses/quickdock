package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 支持的 runtime 类型
var supportedRuntimes = map[string]bool{
	"none":   true,
	"goja":   true,
	"native": true,
}

// 插件 ID 格式：com.xxx.xxx
var pluginIDRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]*(\.[a-zA-Z][a-zA-Z0-9-]*)+$`)

// LoadManifest 读取并校验 plugin.json
func LoadManifest(path string) (*PluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	if err := validateManifest(&m); err != nil {
		return nil, fmt.Errorf("校验 %s 失败: %w", path, err)
	}

	return &m, nil
}

// LoadManifestFromDir 从插件目录加载 plugin.json
func LoadManifestFromDir(dir string) (*PluginManifest, error) {
	path := filepath.Join(dir, "plugin.json")
	return LoadManifest(path)
}

// validateManifest 校验插件清单字段完整性
func validateManifest(m *PluginManifest) error {
	// 必填字段
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("%w: id 不能为空", ErrInvalidManifest)
	}
	if !pluginIDRe.MatchString(m.ID) {
		return fmt.Errorf("%w: id 格式无效 (期望 com.xxx.xxx，得到 %s)", ErrInvalidManifest, m.ID)
	}

	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: name 不能为空", ErrInvalidManifest)
	}
	if m.Version == "" {
		return fmt.Errorf("%w: version 不能为空", ErrInvalidManifest)
	}

	// 校验 backend
	if m.Backend.Runtime == "" {
		return fmt.Errorf("%w: backend.runtime 不能为空", ErrInvalidManifest)
	}
	if !supportedRuntimes[m.Backend.Runtime] {
		return fmt.Errorf("%w: 不支持的 runtime %q，仅支持: none/goja/native", ErrUnsupportedRuntime, m.Backend.Runtime)
	}
	// none runtime 不需要 entry
	if m.Backend.Runtime != "none" && strings.TrimSpace(m.Backend.Entry) == "" {
		return fmt.Errorf("%w: backend.entry 不能为空（none runtime 除外）", ErrInvalidManifest)
	}

	return nil
}
