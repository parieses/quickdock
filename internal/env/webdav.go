package env

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/webdavsrv"
)

// webdavBaseRel WebDAV 配置目录（相对数据目录）：~/.quickdock/runtime/webdav
const webdavBaseRel = "runtime/webdav"

// webdavVersion 与 MCP 同理：内置于 QuickDock 的服务，无外部版本，固定 builtin，
// 环境管理页据此展示为「已安装·内置」，不出现下载/删除入口。
const webdavVersion = "builtin"

// webdavDefaultUser 默认 Basic Auth 用户名（密码首次使用时随机生成并落盘）。
const webdavDefaultUser = "admin"

// WebDAVConfig WebDAV 服务的可编辑配置（JSON，前端配置弹窗直接编辑）。
type WebDAVConfig struct {
	Addr     string `json:"addr"`     // 监听地址，127.0.0.1=仅本机；0.0.0.0=对局域网开放
	Port     int    `json:"port"`     // 监听端口
	Root     string `json:"root"`     // 共享根目录
	Username string `json:"username"` // Basic Auth 用户名
	Password string `json:"password"` // Basic Auth 密码（明文存盘，配置弹窗可见）
	ReadOnly bool   `json:"readOnly"` // 只读模式：拒绝 PUT/DELETE/MKCOL/MOVE 等写操作
}

// WebDAVRuntime 内置 WebDAV 服务端运行时：无安装/无版本，只有启停与配置。
// 实现 ServiceController（环境管理页启停）、ConfigProvider（配置可编辑）、
// ConfigPortsProvider（把实际监听端口回显到状态列）。
type WebDAVRuntime struct {
	baseDir string
}

func NewWebDAVRuntime() *WebDAVRuntime {
	return &WebDAVRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), webdavBaseRel)}
}

func (d *WebDAVRuntime) Kind() Runtime        { return RuntimeWebDAV }
func (d *WebDAVRuntime) DisplayName() string  { return DisplayName(RuntimeWebDAV) }
func (d *WebDAVRuntime) DetectArgs() []string { return nil } // 内置服务，不可从外部导入
func (d *WebDAVRuntime) ParseVersion(string) (string, error) {
	return "", fmt.Errorf("%s 为内置服务，不支持版本探测", DisplayName(RuntimeWebDAV))
}
func (d *WebDAVRuntime) SupportedPlatforms() []string {
	return []string{"windows", "darwin", "linux"}
}
func (d *WebDAVRuntime) Recommended() []string { return nil }
func (d *WebDAVRuntime) ExeFor(string) string  { return "" }

// InstalledVersions 固定返回内置版本，使环境管理页把它当作「已安装」而非「未安装」。
func (d *WebDAVRuntime) InstalledVersions() []Install {
	return []Install{{Version: webdavVersion, Scope: "system", Path: d.baseDir, Active: true}}
}

func (d *WebDAVRuntime) Install(context.Context, string, InstallCallback) error {
	return fmt.Errorf("%s 内置于 QuickDock，无需安装", DisplayName(RuntimeWebDAV))
}

func (d *WebDAVRuntime) DeleteVersion(string) error {
	return fmt.Errorf("%s 内置于 QuickDock，不可删除", DisplayName(RuntimeWebDAV))
}

// -------- 配置 --------

func (d *WebDAVRuntime) configPath() string { return filepath.Join(d.baseDir, "config.json") }

// defaultConfig 默认配置：仅本机可访问 + 随机密码 + 数据目录下的共享根。
func (d *WebDAVRuntime) defaultConfig() WebDAVConfig {
	return WebDAVConfig{
		Addr:     "127.0.0.1",
		Port:     webdavsrv.DefaultPort,
		Root:     filepath.Join(d.baseDir, "root"),
		Username: webdavDefaultUser,
		Password: rand.Text()[:12],
		ReadOnly: false,
	}
}

// ConfigPath 供通用配置读写使用。首次访问时落一份默认配置（密码在此生成并持久化），
// 避免前端配置弹窗「文件不存在」。
func (d *WebDAVRuntime) ConfigPath(string) string {
	_ = os.MkdirAll(d.baseDir, 0o755)
	if _, err := os.Stat(d.configPath()); err != nil {
		raw, _ := json.MarshalIndent(d.defaultConfig(), "", "  ")
		_ = os.WriteFile(d.configPath(), raw, 0o644)
	}
	return d.configPath()
}

// loadConfig 读取配置，缺失/损坏时回退默认值（不因配置写坏就起不来服务）。
func (d *WebDAVRuntime) loadConfig() WebDAVConfig {
	cfg := d.defaultConfig()
	data, err := os.ReadFile(d.configPath())
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return d.defaultConfig()
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1"
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = webdavsrv.DefaultPort
	}
	if cfg.Root == "" {
		cfg.Root = filepath.Join(d.baseDir, "root")
	}
	if cfg.Username == "" {
		cfg.Username = webdavDefaultUser
	}
	return cfg
}

// -------- 服务启停 --------

func (d *WebDAVRuntime) DefaultPort() int { return d.loadConfig().Port }

// ConfiguredPorts 回显真实监听端口（运行中以实际监听为准，未运行时用配置值）。
func (d *WebDAVRuntime) ConfiguredPorts(string) []int {
	if p := webdavsrv.Default.Port(); p > 0 {
		return []int{p}
	}
	return []int{d.loadConfig().Port}
}

func (d *WebDAVRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	if webdavsrv.Default.Running() {
		return fmt.Errorf("WebDAV 服务已在运行")
	}
	cfg := d.loadConfig()
	if cfg.Password == "" {
		return fmt.Errorf("WebDAV 密码为空，请在「编辑配置」中设置后再启动")
	}
	addr, err := webdavsrv.Default.Start(webdavsrv.Config{
		Addr:     cfg.Addr,
		Port:     cfg.Port,
		Root:     cfg.Root,
		Username: cfg.Username,
		Password: cfg.Password,
		ReadOnly: cfg.ReadOnly,
	})
	if err != nil {
		return err
	}
	// 登记到会话内服务表：让启动前的端口冲突检测把「本服务自己在监听」识别为 Ours，
	// 否则重启会被自己的端口拦下。记录不含进程句柄（进程内服务），killTracked 对它是空操作。
	svcMgr.mu.Lock()
	svcMgr.svcs[RuntimeWebDAV] = &runningService{version: webdavVersion}
	svcMgr.mu.Unlock()
	if onLog != nil {
		onLog(fmt.Sprintf("WebDAV 服务已启动: http://%s/（共享目录 %s）", addr, cfg.Root))
		if cfg.ReadOnly {
			onLog("已启用只读模式：客户端无法上传或删除文件")
		}
		if cfg.Addr == "0.0.0.0" || cfg.Addr == "::" {
			onLog("警告：监听地址为 " + cfg.Addr + "，局域网内任何设备都能访问，请确保密码足够复杂")
		}
	}
	logger.I("[env] WebDAV 服务已启动 addr=%s root=%s readOnly=%v", addr, cfg.Root, cfg.ReadOnly)
	return nil
}

func (d *WebDAVRuntime) Stop(string) error {
	if err := webdavsrv.Default.Stop(); err != nil {
		return err
	}
	svcMgr.forget(RuntimeWebDAV)
	return nil
}

func (d *WebDAVRuntime) Status(string) ServiceStatus {
	cfg := d.loadConfig()
	st := ServiceStatus{Running: false, Port: cfg.Port, Ports: []int{cfg.Port}}
	if webdavsrv.Default.Running() {
		st.Running = true
		st.Version = webdavVersion
		if p := webdavsrv.Default.Port(); p > 0 {
			st.Port, st.Ports = p, []int{p}
		}
	}
	return st
}

// 保证 WebDAV 满足环境管理的三项可选能力（启停 / 配置读写 / 端口回显）。
var _ ServiceController = (*WebDAVRuntime)(nil)
var _ ConfigProvider = (*WebDAVRuntime)(nil)
var _ ConfigPortsProvider = (*WebDAVRuntime)(nil)
