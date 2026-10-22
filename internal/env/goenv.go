package env

import (
	"fmt"
)

// EnvKeyDef 描述一个可经「子命令式」机制维护的键值环境变量（如 Go 的 `go env -w`）。
// 与 ConfigProvider（编辑配置文件全文）不同，本结构面向「键 = 值」形式的配置写回。
type EnvKeyDef struct {
	Key         string   `json:"key"`         // 环境变量名，如 GOPROXY
	Label       string   `json:"label"`       // 中文标签
	Desc        string   `json:"desc"`        // 说明（前端表单下方提示）
	Placeholder string   `json:"placeholder"` // 输入框占位
	Choices     []string `json:"choices"`     // 固定候选值（如 GO111MODULE: on/off/auto），空 = 自由输入
	Secret      bool     `json:"secret"`      // 敏感值（如 token），前端用密码框
}

// GoEnvProvider 可选能力：运行时可经自身的「env 子命令」维护一组键值环境变量。
// 实现后即通过 Manager.GoEnvGet/Set 暴露，前端可在操作菜单里打开「配置环境变量」弹窗，
// 无需为该运行时单独加一对 API 与前端弹窗。首个实现是 Go（go env -w / -u / -json）。
type GoEnvProvider interface {
	// GoEnvKeys 返回可编辑的环境变量清单（含中文标签/说明/候选值）。
	GoEnvKeys() []EnvKeyDef
	// GoEnvGet 读取当前生效的环境变量（key→value），用于表单回填。
	GoEnvGet(version string) (map[string]string, error)
	// GoEnvSet 写回：patch 中 value 非空则设置，空则清除（unset）。
	GoEnvSet(version string, patch map[string]string) error
}

// GoEnvSupport 判断某 runtime 是否支持「配置环境变量」（即实现了 GoEnvProvider）。
func (m *Manager) GoEnvSupport(rt Runtime) bool {
	a, err := m.adapter(rt)
	if err != nil {
		return false
	}
	_, ok := a.(GoEnvProvider)
	return ok
}

// GoEnvKeys 返回某 runtime 可编辑的环境变量清单。
func (m *Manager) GoEnvKeys(rt Runtime) ([]EnvKeyDef, error) {
	a, err := m.adapter(rt)
	if err != nil {
		return nil, err
	}
	p, ok := a.(GoEnvProvider)
	if !ok {
		return nil, fmt.Errorf("%s 不支持环境变量配置", DisplayName(rt))
	}
	return p.GoEnvKeys(), nil
}

// GoEnvGet 读取某 runtime 某版本的环境变量（go env 等）。
// version 为空表示「系统全局」作用域（用户级配置文件，对所有未做版本隔离的实例生效），
// 此时不做版本号校验——它不涉及任何版本目录，没有路径穿越风险。
func (m *Manager) GoEnvGet(rt Runtime, version string) (map[string]string, error) {
	if version != "" {
		if err := validateVersion(version); err != nil {
			return nil, err
		}
	}
	a, err := m.adapter(rt)
	if err != nil {
		return nil, err
	}
	p, ok := a.(GoEnvProvider)
	if !ok {
		return nil, fmt.Errorf("%s 不支持环境变量配置", DisplayName(rt))
	}
	return p.GoEnvGet(version)
}

// GoEnvSet 写回某 runtime 某版本的环境变量。version 为空 = 系统全局作用域。
func (m *Manager) GoEnvSet(rt Runtime, version string, patch map[string]string) error {
	if version != "" {
		if err := validateVersion(version); err != nil {
			return err
		}
	}
	a, err := m.adapter(rt)
	if err != nil {
		return err
	}
	p, ok := a.(GoEnvProvider)
	if !ok {
		return fmt.Errorf("%s 不支持环境变量配置", DisplayName(rt))
	}
	return p.GoEnvSet(version, patch)
}
