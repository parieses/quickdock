package env

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// 环境管理自身的设置项（区别于某个运行时的安装配置），持久化在 env/settings.json。

// envSettings 落盘结构。字段只增不改，保证旧版本读到新文件时能忽略未知字段。
type envSettings struct {
	// GitHubAccelerate 是否把 GitHub 下载地址自动改写成 gh-proxy 国内加速地址。
	// 国内直连 github.com 的 Release 资产常失败（连接级重置），开启后候选地址里
	// gh-proxy 版本排在直连之前，下载器优先走镜像。
	GitHubAccelerate bool `json:"githubAccelerate"`
}

const ghProxyPrefix = "https://gh-proxy.com/"

var (
	envSetMu   sync.RWMutex
	envSet     = envSettings{}
	envSetOnce sync.Once
)

func envSettingsFile() string {
	return filepath.Join(platform.DefaultDataDir(), "env", "settings.json")
}

// LoadEnvSettings 从磁盘载入设置（幂等，可重复调用；只在首次真正读盘）。
func LoadEnvSettings() {
	envSetOnce.Do(func() {
		data, err := os.ReadFile(envSettingsFile())
		if err != nil {
			return
		}
		var s envSettings
		if json.Unmarshal(data, &s) == nil {
			envSetMu.Lock()
			envSet = s
			envSetMu.Unlock()
		}
	})
}

// saveEnvSettingsLocked 序列化落盘。调用方须持有 envSetMu。
func saveEnvSettingsLocked() error {
	data, err := json.MarshalIndent(envSet, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(envSettingsFile()), 0755); err != nil {
		return err
	}
	return os.WriteFile(envSettingsFile(), data, 0644)
}

// GitHubAccelerateEnabled 报告是否开启 GitHub 下载加速。
func GitHubAccelerateEnabled() bool {
	LoadEnvSettings()
	envSetMu.RLock()
	defer envSetMu.RUnlock()
	return envSet.GitHubAccelerate
}

// SetGitHubAccelerate 开启/关闭 GitHub 下载加速并持久化。
func SetGitHubAccelerate(on bool) error {
	LoadEnvSettings()
	envSetMu.Lock()
	defer envSetMu.Unlock()
	envSet.GitHubAccelerate = on
	if err := saveEnvSettingsLocked(); err != nil {
		logger.E("[env][settings] 保存 GitHub 加速开关失败: %v", err)
		return err
	}
	logger.I("[env][settings] GitHub 下载加速 = %v", on)
	return nil
}

// ghProxyURL 把 github.com 的下载地址改写成 gh-proxy 前缀地址。
// 非 github.com 地址、以及已经是 gh-proxy 的地址返回空串（表示「无需改写」）。
func ghProxyURL(u string) string {
	if !strings.Contains(u, "github.com/") || strings.Contains(u, ghProxyPrefix) {
		return ""
	}
	return ghProxyPrefix + u
}
