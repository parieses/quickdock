package env

import (
	"fmt"
	"os"
)

// ConfigProvider 具备「可编辑配置文件」的 runtime 需实现此接口。
// 实现后即自动获得通用配置读写能力（Manager.ConfigGet/ConfigSet → EnvConfigGet/EnvConfigSet），
// 无需为每个 runtime 单独加一对 API 与前端弹窗——新增支持只需声明配置文件位置。
//
// 注意：PHP 的扩展开关 / 禁用函数等属于结构化能力，不能退化成纯文本编辑，
// 故 PHP 不实现本接口，继续走 Manager.PHPConfigGet/Set 的特化路径。
type ConfigProvider interface {
	// ConfigPath 返回该版本配置文件的绝对路径。
	ConfigPath(version string) string
}

// RuntimeConfig 通用配置快照（配置文件路径 + 全文），供前端直接编辑。
type RuntimeConfig struct {
	Path string `json:"path"` // 配置文件绝对路径（前端展示用）
	Raw  string `json:"raw"`  // 配置文件全文
}

// ReadConfig 读取实现了 ConfigProvider 的 runtime 的配置文件。
func ReadConfig(p ConfigProvider, version string) (*RuntimeConfig, error) {
	path := p.ConfigPath(version)
	if path == "" {
		return nil, fmt.Errorf("该运行时未定义配置文件路径")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	return &RuntimeConfig{Path: path, Raw: string(data)}, nil
}

// WriteConfig 写回配置文件（整体覆盖）。
// 配置改动需重启服务才生效，由前端提示用户。
func WriteConfig(p ConfigProvider, version, raw string) error {
	path := p.ConfigPath(version)
	if path == "" {
		return fmt.Errorf("该运行时未定义配置文件路径")
	}
	return os.WriteFile(path, []byte(raw), 0644)
}
