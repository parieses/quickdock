// Package aria2 把 aria2c 当作独立下载引擎接入：本包只负责 RPC 指令、状态与会话配置，
// 文件字节由 aria2c 自己写入磁盘，QuickDock 全程不读、不写下载内容。
package aria2

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"quickdock/internal/platform"
)

const (
	// PortScanStart 从 16800 起找空闲端口：6800 常被 Motrix 等其它 aria2 前端占用，
	// 复用它会导致 RPC 打到别人的 daemon 上（任务串台、secret 不匹配）。
	PortScanStart = 16800
	portScanRange = 100

	dirRel          = "aria2"
	configFileName  = "rpc.json"
	SessionFileName = "aria2.session"
)

// ScheduleConfig 是限速时段：时段内用时段限速，时段外用全局限速。
// 跨零点也支持（Start > End 表示夜间时段，如 23:00 - 07:00）。
type ScheduleConfig struct {
	Enabled       bool   `json:"enabled"`
	Start         string `json:"start"` // HH:MM
	End           string `json:"end"`   // HH:MM
	DownloadLimit string `json:"downloadLimit"`
	UploadLimit   string `json:"uploadLimit"`
}

// Config 是 QuickDock 与 aria2c 之间唯一的连接契约。
// Secret 只落在本机数据目录、权限 0600，绝不下发到前端（前端只调 DownloadService 绑定方法）。
//
// 布尔开关刻意用「DisableXxx」反向命名：老用户的 rpc.json 里没有这些字段时零值是 false，
// 正好等价于 aria2 推荐的开启状态；若用正向 bool，读旧配置会把 DHT/PEX 悄悄关掉。
type Config struct {
	Port          int    `json:"port"`
	Secret        string `json:"secret"`
	Version       string `json:"version"`
	DownloadDir   string `json:"downloadDir"`
	MaxConcurrent int    `json:"maxConcurrent"`
	Split         int    `json:"split"`
	MaxConnPerSrv int    `json:"maxConnPerServer"`
	DownloadLimit string `json:"downloadLimit"`
	UploadLimit   string `json:"uploadLimit"`
	// 磁盘预分配策略：none / falloc / trunc / prealloc。默认 none（不占空间、零等待）；
	// prealloc 会真实写满整块空间，500GiB 文件能把单线程事件循环卡几十分钟。
	// 本项目安装的 Windows MinGW 构建（见运行时目录 README.mingw）的 falloc 走
	// SetFileValidData，不写零：NTFS + 管理员权限下瞬时，非 NTFS 会退化成慢速分配。
	FileAllocation string `json:"fileAllocation"`

	// BT / DHT
	ListenPort           int    `json:"listenPort"`
	DhtListenPort        int    `json:"dhtListenPort"`
	BtTracker            string `json:"btTracker"`
	BtMaxPeers           int    `json:"btMaxPeers"`
	SeedRatio            string `json:"seedRatio"`
	SeedTime             string `json:"seedTime"`
	MinSplitSize         string `json:"minSplitSize"`
	UserAgent            string `json:"userAgent"`
	Proxy                string `json:"proxy"`
	DisableDht           bool   `json:"disableDht"`
	DisableDht6          bool   `json:"disableDht6"`
	DisablePeerExchange  bool   `json:"disablePeerExchange"`
	DisableSaveMetadata  bool   `json:"disableSaveMetadata"`
	DisableFollowTorrent bool   `json:"disableFollowTorrent"`
	BtForceEncryption    bool   `json:"btForceEncryption"`
	BtRequireCrypto      bool   `json:"btRequireCrypto"`
	BtRemoveUnselected   bool   `json:"btRemoveUnselected"`
	CheckIntegrity       bool   `json:"checkIntegrity"`

	// 自动化
	AutoOpenFolder bool              `json:"autoOpenFolder"`
	AutoCategory   bool              `json:"autoCategory"`
	CategoryRules  map[string]string `json:"categoryRules"` // 扩展名（小写、不含点）→ 子目录名
	// 每个链接下载单独放进「保存目录/<文件名>/」，避免多个任务挤在一个目录。
	// 纯 QuickDock 侧行为，不对应 aria2 选项，因此不下发 changeGlobalOption。
	PerTaskFolder bool           `json:"perTaskFolder"`
	Schedule      ScheduleConfig `json:"schedule"`

	// 远程 aria2：开启后 DownloadService 不再启动本机 daemon，Client 直接连远端 RPC。
	// RemoteSecret 只落本机 0600 配置，optionsView 只回传「是否已设置」，不回传明文。
	RemoteEnabled bool   `json:"remoteEnabled"`
	RemoteHost    string `json:"remoteHost"`
	RemotePort    int    `json:"remotePort"`
	RemoteSecret  string `json:"remoteSecret"`
	RemoteSecure  bool   `json:"remoteSecure"`
	RemotePath    string `json:"remotePath"`

	// 反向命名：老配置缺字段时零值等价「通知开启」。
	NotifyDisabled bool `json:"notifyDisabled"`
}

// DefaultCategoryRules 是「按扩展名自动归类」的内置映射，用户可在设置里整体替换。
func DefaultCategoryRules() map[string]string {
	return map[string]string{
		"mp4": "Videos", "mkv": "Videos", "avi": "Videos", "mov": "Videos",
		"webm": "Videos", "flv": "Videos", "wmv": "Videos", "ts": "Videos",
		"mp3": "Music", "flac": "Music", "wav": "Music", "aac": "Music", "m4a": "Music", "ogg": "Music",
		"jpg": "Pictures", "jpeg": "Pictures", "png": "Pictures", "gif": "Pictures", "webp": "Pictures", "bmp": "Pictures",
		"zip": "Archives", "rar": "Archives", "7z": "Archives", "tar": "Archives", "gz": "Archives", "xz": "Archives",
		"exe": "Programs", "msi": "Programs", "dmg": "Programs", "apk": "Programs", "iso": "Programs",
		"pdf": "Documents", "doc": "Documents", "docx": "Documents", "xls": "Documents",
		"xlsx": "Documents", "ppt": "Documents", "pptx": "Documents", "epub": "Documents", "txt": "Documents",
		"torrent": "Torrents",
	}
}

var cfgMu sync.Mutex

// Dir 返回 aria2 会话目录（与 runtime/aria2 程序目录分开，卸载程序不丢会话与配置）。
func Dir() string { return filepath.Join(platform.DefaultDataDir(), dirRel) }

func configPath() string { return filepath.Join(Dir(), configFileName) }

func SessionPath() string { return filepath.Join(Dir(), SessionFileName) }

// applyDefaults 补齐零值：旧配置文件缺字段时不改变既有行为，同时让 BT 开箱可用。
// NormalizeFileAllocation 校验 aria2 的磁盘预分配策略。空串返回 ok=false，
// 表示「调用方没带这一项，保持原值」，避免旧前端 / 老配置把用户已选的 falloc
// 静默改回 none；非法值同样不改。
func NormalizeFileAllocation(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none":
		return "none", true
	case "falloc":
		return "falloc", true
	case "trunc":
		return "trunc", true
	case "prealloc":
		return "prealloc", true
	default:
		return "none", false
	}
}

// FileAllocationArg 返回可直接拼进 --file-allocation= 的值。老 rpc.json 缺字段
// （零值）与非法值一律回落到 none，保持「预分配永不默认开启」的既有行为。
func (c *Config) FileAllocationArg() string {
	v, ok := NormalizeFileAllocation(c.FileAllocation)
	if !ok {
		return "none"
	}
	return v
}

func (c *Config) applyDefaults() {
	if _, ok := NormalizeFileAllocation(c.FileAllocation); !ok {
		c.FileAllocation = "none"
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 5
	}
	if c.Split <= 0 {
		c.Split = 8
	}
	if c.MaxConnPerSrv <= 0 {
		c.MaxConnPerSrv = 8
	}
	if c.ListenPort <= 0 {
		c.ListenPort = 6881
	}
	if c.DhtListenPort <= 0 {
		c.DhtListenPort = 6881
	}
	if c.BtMaxPeers <= 0 {
		c.BtMaxPeers = 55
	}
	// 0.0 = 与 aria2 默认一致：下完即停，不在后台做种。想做种由用户在设置里改。
	if c.SeedRatio == "" {
		c.SeedRatio = "0.0"
	}
	if c.SeedTime == "" {
		c.SeedTime = "0"
	}
	if len(c.CategoryRules) == 0 {
		c.CategoryRules = DefaultCategoryRules()
	}
	if c.RemotePort <= 0 {
		c.RemotePort = 6800
	}
	if strings.TrimSpace(c.RemotePath) == "" {
		c.RemotePath = "/jsonrpc"
	}
}

func randomSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// DefaultDownloadDir 是保存目录的兜底值：用户主目录下的 Downloads。
// 配置里可能还留着已经拔掉的移动硬盘盘符（E:\Downloads），启动层会用这个
// 函数把 --dir 拉回一个确定存在的位置，避免整个引擎起不来。
func DefaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = platform.DefaultDataDir()
	}
	return filepath.Join(home, "Downloads")
}

// LoadConfig 读取连接配置；文件缺失/损坏时按默认值新建（只写文件，不启动进程）。
func LoadConfig() (*Config, error) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return loadLocked()
}

func loadLocked() (*Config, error) {
	if b, err := os.ReadFile(configPath()); err == nil {
		var c Config
		if json.Unmarshal(b, &c) == nil && c.Port > 0 && c.Secret != "" {
			c.applyDefaults()
			return &c, nil
		}
	}
	port := 0
	for p := PortScanStart; p < PortScanStart+portScanRange; p++ {
		if portFree(p) {
			port = p
			break
		}
	}
	if port == 0 {
		return nil, fmt.Errorf("没有可用的 aria2 RPC 端口（%d 起）", PortScanStart)
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	c := &Config{
		Port:          port,
		Secret:        secret,
		DownloadDir:   DefaultDownloadDir(),
		MaxConcurrent: 5,
		Split:         8,
		MaxConnPerSrv: 8,
	}
	c.applyDefaults()
	if err := saveLocked(c); err != nil {
		return nil, err
	}
	return c, nil
}

// SaveConfig 持久化配置（限速、目录等设置改动后调用）。
func SaveConfig(c *Config) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return saveLocked(c)
}

func saveLocked(c *Config) error {
	c.applyDefaults()
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), b, 0o600)
}

// ===== 远程连接契约 =====

// IsRemote 为真时不再启动本机 daemon，所有 RPC 都发往用户配置的远程 aria2。
func (c *Config) IsRemote() bool {
	return c != nil && c.RemoteEnabled && strings.TrimSpace(c.RemoteHost) != ""
}

// remoteHost 清掉用户可能顺手粘上的 scheme / 路径；host 里自带 :port 时以它为准。
func (c *Config) remoteHost() (string, int) {
	h := strings.TrimSpace(c.RemoteHost)
	for _, p := range []string{"http://", "https://", "ws://", "wss://"} {
		if strings.HasPrefix(h, p) {
			h = strings.TrimPrefix(h, p)
			break
		}
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	port := 0
	if host, p, err := net.SplitHostPort(h); err == nil {
		h = strings.Trim(host, "[]")
		if n, e := strconv.Atoi(p); e == nil {
			port = n
		}
	}
	return h, port
}

// endpoint 返回 RPC 的 host / port / path，本机与远程共用一份拼装逻辑。
func (c *Config) endpoint() (string, int, string) {
	if !c.IsRemote() {
		return "127.0.0.1", c.Port, "/jsonrpc"
	}
	h, hp := c.remoteHost()
	port := c.RemotePort
	if hp > 0 {
		port = hp
	}
	if port <= 0 {
		port = 6800
	}
	p := strings.TrimSpace(c.RemotePath)
	if p == "" {
		p = "/jsonrpc"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return h, port, p
}

func (c *Config) RPCPort() int { _, p, _ := c.endpoint(); return p }

func (c *Config) RPCSecret() string {
	if c.IsRemote() {
		return c.RemoteSecret
	}
	return c.Secret
}

func (c *Config) RPCBase() string { return c.rpcURL("http", "https") }
func (c *Config) WSBase() string  { return c.rpcURL("ws", "wss") }

func (c *Config) rpcURL(plain, secure string) string {
	h, port, p := c.endpoint()
	scheme := plain
	if c.IsRemote() && c.RemoteSecure {
		scheme = secure
	}
	return fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(h, strconv.Itoa(port)), p)
}

// EnsureSessionFile 保证 --input-file 指向的文件存在：aria2 启动时若会话文件缺失会直接报错退出。
func EnsureSessionFile() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	p := SessionPath()
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, nil, 0o600)
}

// SpeedLimitArg 把前端的「KB/s 数字」翻译成 aria2 认识的后缀形式。
// 用户也可以直接填 aria2 原生值（如 2M），此时原样透传；0 / 空表示不限速。
func SpeedLimitArg(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		return "0"
	}
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return v + "K"
	}
	return v
}

func boolArg(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// portRange 生成 aria2 需要的 "start-end" 形式端口段。
func portRange(start int) string {
	return fmt.Sprintf("%d-%d", start, start+99)
}

// DaemonArgs 生成 aria2c 的启动参数。RPC 只听回环、带随机 secret、开启会话保存与断点续传。
// BT 相关参数只在需要时追加，全部为 aria2 1.37 官方选项（已用 --version 实测合法性）。
func DaemonArgs(c *Config) []string {
	c.applyDefaults()
	args := []string{
		"--enable-rpc=true",
		fmt.Sprintf("--rpc-listen-port=%d", c.Port),
		"--rpc-listen-all=false",
		"--rpc-secret=" + c.Secret,
		"--continue=true",
		"--save-session=" + SessionPath(),
		"--save-session-interval=60",
		"--input-file=" + SessionPath(),
		// 默认 NOTICE 会把每个任务每秒的进度刷到 stdout，宿主用 StdoutPipe 逐行
		// 收集再写 aria2.log：实测一个 50GiB 任务几分钟就写到 200KB+ 且持续增长。
		"--console-log-level=warn",
		// 进度摘要不受 console-log-level 控制，默认每 60s 仍会往 stdout 写一段。
		// aria2 是单线程事件循环，stdout 管道一旦写阻塞，RPC 会跟着一起卡住
		// （端口仍 Listen、请求全部超时）。直接关掉，只保留真正的 WARN/ERROR。
		"--summary-interval=0",
		"--dir=" + c.DownloadDir,
		fmt.Sprintf("--max-concurrent-downloads=%d", c.MaxConcurrent),
		fmt.Sprintf("--split=%d", c.Split),
		fmt.Sprintf("--max-connection-per-server=%d", c.MaxConnPerSrv),
		"--file-allocation=" + c.FileAllocationArg(),
		// ===== BT / DHT =====
		"--enable-dht=" + boolArg(!c.DisableDht),
		"--enable-dht6=" + boolArg(!c.DisableDht6),
		"--enable-peer-exchange=" + boolArg(!c.DisablePeerExchange),
		"--listen-port=" + portRange(c.ListenPort),
		"--dht-listen-port=" + portRange(c.DhtListenPort),
		"--dht-file-path=" + filepath.Join(Dir(), "dht.dat"),
		"--dht-file-path6=" + filepath.Join(Dir(), "dht6.dat"),
		fmt.Sprintf("--bt-max-peers=%d", c.BtMaxPeers),
		"--seed-ratio=" + c.SeedRatio,
		"--seed-time=" + c.SeedTime,
		"--bt-save-metadata=" + boolArg(!c.DisableSaveMetadata),
		"--bt-load-saved-metadata=true",
		"--follow-torrent=" + boolArg(!c.DisableFollowTorrent),
		"--bt-remove-unselected-file=" + boolArg(c.BtRemoveUnselected),
		"--bt-force-encryption=" + boolArg(c.BtForceEncryption),
		"--bt-require-crypto=" + boolArg(c.BtRequireCrypto),
		"--check-integrity=" + boolArg(c.CheckIntegrity),
	}
	if c.DownloadLimit != "" && c.DownloadLimit != "0" {
		args = append(args, "--max-overall-download-limit="+SpeedLimitArg(c.DownloadLimit))
	}
	if c.UploadLimit != "" && c.UploadLimit != "0" {
		args = append(args, "--max-overall-upload-limit="+SpeedLimitArg(c.UploadLimit))
	}
	if c.BtTracker = strings.TrimSpace(c.BtTracker); c.BtTracker != "" {
		args = append(args, "--bt-tracker="+c.BtTracker)
	}
	if c.MinSplitSize = strings.TrimSpace(c.MinSplitSize); c.MinSplitSize != "" {
		args = append(args, "--min-split-size="+c.MinSplitSize)
	}
	if c.UserAgent = strings.TrimSpace(c.UserAgent); c.UserAgent != "" {
		args = append(args, "--user-agent="+c.UserAgent)
	}
	if c.Proxy = strings.TrimSpace(c.Proxy); c.Proxy != "" {
		args = append(args, "--all-proxy="+c.Proxy)
	}
	return args
}
