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
	// EverythingInstallPath 用户自定义的 Everything 安装目录（覆盖默认 Program Files 探测路径）。
	EverythingInstallPath string `json:"everythingInstallPath"`
	// EsExePath 用户自定义的 es.exe 路径（es.exe 与 Everything.exe 分开发行时兜底定位）。
	EsExePath string `json:"esExePath"`
	// FileSearchEnabled 文件搜索总开关：命令面板检索与 MCP file_search 工具是否启用。
	FileSearchEnabled bool `json:"fileSearchEnabled"`
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
			// 无设置文件：采用安全默认——文件搜索总开关默认开启，
			// 这样 Everything 就绪后命令面板 / MCP 立即可用；用户可在设置里关闭。
			envSetMu.Lock()
			envSet.FileSearchEnabled = true
			envSetMu.Unlock()
			return
		}
		s, ok := parseEnvSettings(data)
		if !ok {
			// 文件损坏：同样给安全默认，避免功能被零值关死。
			envSetMu.Lock()
			envSet.FileSearchEnabled = true
			envSetMu.Unlock()
			return
		}
		envSetMu.Lock()
		envSet = s
		envSetMu.Unlock()
	})
}

// parseEnvSettings 解析 settings.json 原始字节并补齐缺省字段。
// 第二个返回值为 false 表示 JSON 不可解析（调用方应落安全默认）。
//
// 老版本写出的文件可能不含 fileSearchEnabled 字段（零值为 false）。为保持「装上即可用」，
// 仅当该字段确实缺省时才默认开启；已显式写入 false 的（用户主动关闭）则予以尊重——
// 这正是 jsonHasKey 存在的原因。
func parseEnvSettings(data []byte) (envSettings, bool) {
	var s envSettings
	if json.Unmarshal(data, &s) != nil {
		return envSettings{}, false
	}
	if !jsonHasKey(data, "fileSearchEnabled") {
		s.FileSearchEnabled = true
	}
	return s, true
}

// jsonHasKey 判断原始 JSON 文本是否包含某顶层键（用于在零值无法区分「缺省」与「显式 false」时做判断）。
func jsonHasKey(data []byte, key string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, present := raw[key]
	return present
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

// ---- Everything 文件搜索设置 ----

// GetEverythingInstallPath 返回用户自定义的 Everything 安装目录（空串=使用默认探测）。
func GetEverythingInstallPath() string {
	LoadEnvSettings()
	envSetMu.RLock()
	defer envSetMu.RUnlock()
	return envSet.EverythingInstallPath
}

// SetEverythingInstallPath 设置/清除自定义安装目录（空串=恢复默认）。
func SetEverythingInstallPath(p string) error {
	LoadEnvSettings()
	envSetMu.Lock()
	defer envSetMu.Unlock()
	envSet.EverythingInstallPath = p
	return saveEnvSettingsLocked()
}

// GetEsExePath 返回用户自定义的 es.exe 路径（空串=自动探测）。
func GetEsExePath() string {
	LoadEnvSettings()
	envSetMu.RLock()
	defer envSetMu.RUnlock()
	return envSet.EsExePath
}

// SetEsExePath 设置/清除自定义 es.exe 路径（空串=恢复自动探测）。
func SetEsExePath(p string) error {
	LoadEnvSettings()
	envSetMu.Lock()
	defer envSetMu.Unlock()
	envSet.EsExePath = p
	return saveEnvSettingsLocked()
}

// FileSearchEnabled 返回文件搜索总开关（默认开启）。
func FileSearchEnabled() bool {
	LoadEnvSettings()
	envSetMu.RLock()
	defer envSetMu.RUnlock()
	return envSet.FileSearchEnabled
}

// SetFileSearchEnabled 开启/关闭文件搜索并持久化。
func SetFileSearchEnabled(on bool) error {
	LoadEnvSettings()
	envSetMu.Lock()
	defer envSetMu.Unlock()
	envSet.FileSearchEnabled = on
	return saveEnvSettingsLocked()
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
