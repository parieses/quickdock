// Package download 下载器门面：前端只调本服务，aria2 RPC 的端口与 secret 一律不暴露。
//
// 职责边界：QuickDock 是「遥控器 + 仪表盘」——发指令、记元数据、读状态；
// 文件字节由 aria2c 自己从源站取回并写入磁盘，本包不碰下载内容。
package download

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/aria2"
	"quickdock/internal/db"
	envengine "quickdock/internal/env"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
)

// StatusView 是下载器整体状态（不是任务状态）。
type StatusView struct {
	Installed     bool   `json:"installed"`
	Running       bool   `json:"running"`
	Port          int    `json:"port"`
	Version       string `json:"version"`
	DownloadDir   string `json:"downloadDir"`
	MaxConcurrent int    `json:"maxConcurrent"`
	Split         int    `json:"split"`
	DownloadLimit string `json:"downloadLimit"`
	UploadLimit   string `json:"uploadLimit"`
	Remote        bool   `json:"remote"`      // 连接的是远程 aria2，本地磁盘预检不适用
	Host          string `json:"host"`        // 不含凭据的连接地址
	WsConnected   bool   `json:"wsConnected"` // 事件通道在线时轮询会自动降频
}

// DlOptionsView 是下发给前端的可编辑设置（完整表单，提交时整体回写）。
// ⚠️ 刻意不含 secret / port：RPC 凭据只留在 Go 侧，绝不能出现在 ApiResult.data 里。
type DlOptionsView struct {
	DownloadDir    string `json:"downloadDir"`
	MaxConcurrent  int    `json:"maxConcurrent"`
	Split          int    `json:"split"`
	DownloadLimit  string `json:"downloadLimit"`
	UploadLimit    string `json:"uploadLimit"`
	FileAllocation string `json:"fileAllocation"`

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

	AutoOpenFolder bool                 `json:"autoOpenFolder"`
	AutoCategory   bool                 `json:"autoCategory"`
	CategoryRules  map[string]string    `json:"categoryRules"`
	PerTaskFolder  bool                 `json:"perTaskFolder"`
	Schedule       aria2.ScheduleConfig `json:"schedule"`

	// 远程连接：RemoteSecret 是「只写」字段，optionsView 永远返回空串；
	// RemoteSecretSet 告诉前端「已经配过，留空即保持不变」。
	RemoteEnabled   bool   `json:"remoteEnabled"`
	RemoteHost      string `json:"remoteHost"`
	RemotePort      int    `json:"remotePort"`
	RemoteSecret    string `json:"remoteSecret"`
	RemoteSecretSet bool   `json:"remoteSecretSet"`
	RemoteSecure    bool   `json:"remoteSecure"`
	RemotePath      string `json:"remotePath"`
	NotifyDisabled  bool   `json:"notifyDisabled"`
}

func optionsView(c *aria2.Config) DlOptionsView {
	return DlOptionsView{
		DownloadDir:    c.DownloadDir,
		MaxConcurrent:  c.MaxConcurrent,
		Split:          c.Split,
		DownloadLimit:  c.DownloadLimit,
		UploadLimit:    c.UploadLimit,
		FileAllocation: c.FileAllocationArg(),

		ListenPort:           c.ListenPort,
		DhtListenPort:        c.DhtListenPort,
		BtTracker:            c.BtTracker,
		BtMaxPeers:           c.BtMaxPeers,
		SeedRatio:            c.SeedRatio,
		SeedTime:             c.SeedTime,
		MinSplitSize:         c.MinSplitSize,
		UserAgent:            c.UserAgent,
		Proxy:                c.Proxy,
		DisableDht:           c.DisableDht,
		DisableDht6:          c.DisableDht6,
		DisablePeerExchange:  c.DisablePeerExchange,
		DisableSaveMetadata:  c.DisableSaveMetadata,
		DisableFollowTorrent: c.DisableFollowTorrent,
		BtForceEncryption:    c.BtForceEncryption,
		BtRequireCrypto:      c.BtRequireCrypto,
		BtRemoveUnselected:   c.BtRemoveUnselected,
		CheckIntegrity:       c.CheckIntegrity,

		AutoOpenFolder: c.AutoOpenFolder,
		AutoCategory:   c.AutoCategory,
		CategoryRules:  c.CategoryRules,
		PerTaskFolder:  c.PerTaskFolder,
		Schedule:       c.Schedule,

		RemoteEnabled:   c.RemoteEnabled,
		RemoteHost:      c.RemoteHost,
		RemotePort:      c.RemotePort,
		RemoteSecretSet: c.RemoteSecret != "",
		RemoteSecure:    c.RemoteSecure,
		RemotePath:      c.RemotePath,
		NotifyDisabled:  c.NotifyDisabled,
	}
}

func applyOptionsInput(cfg *aria2.Config, in DlOptionsView) {
	if v := strings.TrimSpace(in.DownloadDir); v != "" {
		cfg.DownloadDir = v
	}
	cfg.DownloadLimit = strings.TrimSpace(in.DownloadLimit)
	cfg.UploadLimit = strings.TrimSpace(in.UploadLimit)
	// 空串 / 非法值保持原样，避免旧前端把用户已选的策略改回 none。
	if v, ok := aria2.NormalizeFileAllocation(in.FileAllocation); ok {
		cfg.FileAllocation = v
	}
	if in.MaxConcurrent > 0 && in.MaxConcurrent <= 64 {
		cfg.MaxConcurrent = in.MaxConcurrent
	}
	if in.Split > 0 && in.Split <= 64 {
		cfg.Split = in.Split
	}
	if in.ListenPort > 0 && in.ListenPort <= 65500 {
		cfg.ListenPort = in.ListenPort
	}
	if in.DhtListenPort > 0 && in.DhtListenPort <= 65500 {
		cfg.DhtListenPort = in.DhtListenPort
	}
	if in.BtMaxPeers > 0 && in.BtMaxPeers <= 1024 {
		cfg.BtMaxPeers = in.BtMaxPeers
	}
	cfg.BtTracker = strings.TrimSpace(in.BtTracker)
	cfg.SeedRatio = strings.TrimSpace(in.SeedRatio)
	cfg.SeedTime = strings.TrimSpace(in.SeedTime)
	cfg.MinSplitSize = strings.TrimSpace(in.MinSplitSize)
	cfg.UserAgent = strings.TrimSpace(in.UserAgent)
	cfg.Proxy = strings.TrimSpace(in.Proxy)
	cfg.DisableDht = in.DisableDht
	cfg.DisableDht6 = in.DisableDht6
	cfg.DisablePeerExchange = in.DisablePeerExchange
	cfg.DisableSaveMetadata = in.DisableSaveMetadata
	cfg.DisableFollowTorrent = in.DisableFollowTorrent
	cfg.BtForceEncryption = in.BtForceEncryption
	cfg.BtRequireCrypto = in.BtRequireCrypto
	cfg.BtRemoveUnselected = in.BtRemoveUnselected
	cfg.CheckIntegrity = in.CheckIntegrity
	cfg.AutoOpenFolder = in.AutoOpenFolder
	cfg.AutoCategory = in.AutoCategory
	cfg.PerTaskFolder = in.PerTaskFolder
	if in.CategoryRules != nil {
		cfg.CategoryRules = in.CategoryRules
	}
	cfg.Schedule = in.Schedule

	cfg.RemoteEnabled = in.RemoteEnabled
	if h := strings.TrimSpace(in.RemoteHost); h != "" {
		cfg.RemoteHost = h
	} else if in.RemoteEnabled {
		// 没填主机就不切远程，避免把所有 RPC 打到空地址上。
		cfg.RemoteEnabled = false
	}
	if in.RemotePort > 0 && in.RemotePort <= 65535 {
		cfg.RemotePort = in.RemotePort
	}
	// 留空表示「不修改已有 secret」；填了才覆盖，避免表单每次打开把凭据清空。
	if v := strings.TrimSpace(in.RemoteSecret); v != "" {
		cfg.RemoteSecret = v
	}
	cfg.RemoteSecure = in.RemoteSecure
	if p := strings.TrimSpace(in.RemotePath); p != "" {
		cfg.RemotePath = p
	}
	cfg.NotifyDisabled = in.NotifyDisabled
}

// ===== 详情 / 全局统计 DTO =====

type DlURIView struct {
	URI    string `json:"uri"`
	Status string `json:"status"`
}

type DlFileView struct {
	Index     int64       `json:"index"`
	Path      string      `json:"path"`
	Length    int64       `json:"length"`
	Completed int64       `json:"completed"`
	Selected  bool        `json:"selected"`
	URIs      []DlURIView `json:"uris"`
}

type DlPeerView struct {
	PeerID        string `json:"peerId"`
	IP            string `json:"ip"`
	Port          int    `json:"port"`
	DownloadSpeed int64  `json:"downloadSpeed"`
	UploadSpeed   int64  `json:"uploadSpeed"`
	Seeder        bool   `json:"seeder"`
	AmChoking     bool   `json:"amChoking"`
	PeerChoking   bool   `json:"peerChoking"`
}

type DlServerView struct {
	Index      int    `json:"index"`
	URI        string `json:"uri"`
	CurrentURI string `json:"currentUri"`
	Status     string `json:"status"`
	Speed      int64  `json:"speed"`
	Error      string `json:"error"`
}

type DlDetailView struct {
	GID                 string            `json:"gid"`
	Options             map[string]string `json:"options"`
	Files               []DlFileView      `json:"files"`
	URIs                []DlURIView       `json:"uris"`
	Peers               []DlPeerView      `json:"peers"`
	Servers             []DlServerView    `json:"servers"`
	AnnounceList        [][]string        `json:"announceList"`
	TorrentComment      string            `json:"torrentComment"`
	TorrentMode         string            `json:"torrentMode"`
	TorrentCreationDate int64             `json:"torrentCreationDate"`
}

type GlobalStatView struct {
	DownloadSpeed int64 `json:"downloadSpeed"`
	UploadSpeed   int64 `json:"uploadSpeed"`
	Active        int   `json:"active"`
	Waiting       int   `json:"waiting"`
	Stopped       int   `json:"stopped"`
	StoppedTotal  int   `json:"stoppedTotal"`
}

type EngineInfoView struct {
	Version   string            `json:"version"`
	Features  []string          `json:"features"`
	SessionID string            `json:"sessionId"`
	Methods   []string          `json:"methods"`
	Options   map[string]string `json:"options"`
}

// DlNotice 是「完成 / 失败」通知的事件载荷。以前只传任务名，失败会被前端
// 误报成「下载完成」；改成结构化载荷，前端按 ok 分 success/error toast。
type DlNotice struct {
	OK    bool   `json:"ok"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// aria2Version 给环境框架的 Install/Start 传一个合法的逻辑版本号。
func aria2Version() string {
	if vs := envengine.Versions(envengine.RuntimeAria2); len(vs) > 0 {
		return vs[0]
	}
	return "1.37.0"
}

// DownloadService 面向前端的下载器门面。
//
// poller 由 DlEnable / DlStatus / 新建任务懒启动：1s 一轮，活跃任务走单 RPC 快路径，
// 等待/已结束每 5 轮补一次；任何写操作后 forceFull 让下一轮立刻全量。
type DownloadService struct {
	App *services.AppService

	mu     sync.Mutex
	ticker *time.Ticker
	stopCh chan struct{}
	prev   map[string]string
	// notified 记录「已经推过终态通知」的 gid:status，轮询与 WS 事件共用，避免重复打扰。
	notified     map[string]bool
	failStreak   int
	lastGlobal   time.Time
	eventsCancel context.CancelFunc
	wsAlive      atomic.Bool
	// disconnected 只在用户主动点「停止」时置位：远程模式不能靠探进程判断，
	// 否则 DlStatus 一握手就会把刚断开的轮询又拉起来。
	disconnected atomic.Bool
	// eventPending 合并高频事件：一次快照没跑完时不重复起 goroutine。
	eventPending atomic.Bool
	snapMu       sync.Mutex

	// restCache 缓存不常变的 waiting/stopped；只在 emitSnapshot 的 ticker goroutine 里读写。
	restCache []aria2.Status
	tickCount int
	forceFull atomic.Bool
	// knownSize 记住每个任务最后一次报出的真实大小。aria2 暂停后会把它清空，
	// 详见 taskSize 的注释。用独立锁：snapshotLocked 持 snapMu 时也要访问。
	sizeMu    sync.RWMutex
	knownSize map[string]taskSize
	// aria2 只有在真正连上源站后才知道 Content-Length。排队中 / 从会话恢复 /
	// QuickDock 刚重启的任务会一直是 0/0，此时主动探测 URL 补回总大小。
	// tried 受 sizeMu 保护，失败也冷却，避免签名过期时每轮轮询刷网络。
	sizeProbeTried map[string]time.Time
	sizeProbing    atomic.Bool
	// scheduleOn 记录当前是否处在限速时段内，避免每分钟重复下发同样的选项。
	scheduleOn atomic.Bool

	// 大文件把目标盘写满时，aria2 会卡在 write cache flush（日志实锤
	// errNum=112 / errorCode=9 / There is not enough space on the disk），
	// 主线程被磁盘 I/O 堵住后端口仍 Listen、RPC 却全部超时。看门狗只按端口
	// 判断「运行中」，识别不出这种假活，所以这里自己做守卫与自愈。
	diskCheckedAt time.Time
	diskPaused    atomic.Bool
	lastRecover   time.Time
	recovering    atomic.Bool
}

func NewDownloadService(app *services.AppService) *DownloadService {
	s := &DownloadService{
		App: app, prev: map[string]string{}, notified: map[string]bool{},
		knownSize: map[string]taskSize{}, sizeProbeTried: map[string]time.Time{},
	}
	s.forceFull.Store(true)
	return s
}

func (s *DownloadService) dbOK() *services.ApiResult {
	if s.App == nil {
		return services.FailMsg("服务未初始化")
	}
	return services.CheckDB(s.App.DB)
}

func (s *DownloadService) invalidate() { s.forceFull.Store(true) }

// ===== 引擎启停 =====

func (s *DownloadService) DlStatus() *services.ApiResult {
	if s.App == nil || s.App.Env == nil {
		return services.FailMsg("环境管理未初始化")
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	v := StatusView{
		Port:          cfg.Port,
		DownloadDir:   cfg.DownloadDir,
		MaxConcurrent: cfg.MaxConcurrent,
		Split:         cfg.Split,
		DownloadLimit: cfg.DownloadLimit,
		UploadLimit:   cfg.UploadLimit,
		Version:       cfg.Version,
		Remote:        cfg.IsRemote(),
		Host:          cfg.RPCBase(),
		WsConnected:   s.wsAlive.Load(),
	}
	if s.disconnected.Load() {
		v.Installed = true
		return services.Ok(v)
	}
	if cfg.IsRemote() {
		// 远程模式不碰环境管理：有没有本地 aria2c 与能不能连上都无关。
		v.Installed = true
		ver, e := aria2.NewClient(cfg).GetVersion()
		if e == nil {
			v.Running = true
			if ver != "" {
				v.Version = ver
			}
			s.startPoller()
		}
		return services.Ok(v)
	}
	if ins := envengine.NewAria2Runtime().InstalledVersions(); len(ins) > 0 {
		v.Installed = true
		if v.Version == "" {
			v.Version = ins[0].Version
		}
	}
	ver, rpcErr := aria2.NewClient(cfg).GetVersion()
	if rpcErr == nil {
		v.Running = true
		if ver != "" {
			v.Version = ver
		}
		s.startPoller()
	} else if envengine.NewAria2Runtime().Status(aria2Version()).Running {
		v.Running = true
	}
	return services.Ok(v)
}

func (s *DownloadService) DlEnable() *services.ApiResult {
	if s.App == nil || s.App.Env == nil {
		return services.FailMsg("环境管理未初始化")
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	if cfg.IsRemote() {
		if _, err := aria2.NewClient(cfg).GetVersion(); err != nil {
			return services.Fail(fmt.Errorf("远程 aria2 连接失败: %w", err))
		}
		s.startPoller()
		return services.Ok(nil)
	}
	rt := envengine.NewAria2Runtime()
	if len(rt.InstalledVersions()) == 0 {
		cb := envengine.InstallCallback{
			OnStage: func(stage, msg string) { logger.I("[download] aria2 安装 %s: %s", stage, msg) },
			OnLog:   func(msg string) { logger.I("[download] %s", msg) },
		}
		if err := rt.Install(context.Background(), aria2Version(), cb); err != nil {
			return services.Fail(err)
		}
		s.App.Env.RefreshDetected(envengine.RuntimeAria2)
	}
	if _, err := aria2.NewClient(cfg).GetVersion(); err == nil {
		s.startPoller()
		return services.Ok(nil)
	}
	if err := s.App.Env.Start(envengine.RuntimeAria2, aria2Version(), nil); err != nil {
		return services.Fail(err)
	}
	s.startPoller()
	return services.Ok(nil)
}

func (s *DownloadService) DlDisable() *services.ApiResult {
	if s.App == nil || s.App.Env == nil {
		return services.FailMsg("环境管理未初始化")
	}
	s.stopPoller()
	// 远程模式只断开自己的连接，不去停用户机器上的本地引擎（可能根本不是我们的）。
	// 本地模式仍靠进程探测判断状态，不置 disconnected，否则用户在环境管理里
	// 手动重启引擎后，下载页会一直显示「未运行」。
	if cfg, err := aria2.LoadConfig(); err == nil && cfg.IsRemote() {
		s.disconnected.Store(true)
		return services.Ok(nil)
	}
	if err := s.App.Env.Stop(envengine.RuntimeAria2, aria2Version()); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DlDiskSpace 返回目标目录所在卷的容量，供新建任务提交前做空间预检。
// 远程模式下文件写在远端，这个本机容量只能看看，前端会跳过拦截。
func (s *DownloadService) DlDiskSpace(dir string) *services.ApiResult {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	target := strings.TrimSpace(dir)
	if target == "" {
		target = cfg.DownloadDir
	}
	if target == "" {
		home, e := os.UserHomeDir()
		if e != nil || home == "" {
			home = platform.DefaultDataDir()
		}
		target = filepath.Join(home, "Downloads")
	}
	d, err := aria2.CheckDiskSpace(target)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(d)
}

// DlProbeUrl 在添加任务前探测远端文件大小，供前端比对目标盘剩余空间能不能装下。
// urlsJSON 是字符串数组；只读响应头、不下载文件字节。HEAD 拿不到长度时内部会
// 自动回退到 GET + Range: bytes=0-0（OSS 预签名链接按 GET 签发，HEAD 会 403）。
// 探测走独立 HTTP 客户端，与 aria2 引擎是否在本机、RPC 是否繁忙都无关。
func (s *DownloadService) DlProbeUrl(urlsJSON string) *services.ApiResult {
	var raw []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(urlsJSON)), &raw); err != nil {
		return services.FailMsg("下载链接格式无效")
	}
	seen := make(map[string]struct{}, len(raw))
	urls := make([]string, 0, len(raw))
	for _, u := range raw {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
		if len(urls) >= aria2.ProbeMaxURLs {
			break
		}
	}
	if len(urls) == 0 {
		return services.FailMsg("请先填写下载链接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return services.Ok(aria2.ProbeURLs(ctx, urls))
}

// DlEngineInfo 给设置页看引擎特性 / 会话 / 实际生效的全局选项（诊断用途）。
func (s *DownloadService) DlEngineInfo() *services.ApiResult {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	c := aria2.NewClient(cfg)
	vi, err := c.GetVersionInfo()
	if err != nil {
		return services.Fail(err)
	}
	out := EngineInfoView{Version: vi.Version, Features: vi.EnabledFeatures}
	if si, err := c.GetSessionInfo(); err == nil {
		out.SessionID = si.SessionID
	}
	if ms, err := c.ListMethods(); err == nil {
		out.Methods = ms
	}
	if opts, err := c.GetGlobalOption(); err == nil {
		out.Options = opts
	}
	return services.Ok(out)
}

// DlShutdown 走 aria2 自己的 RPC 关闭（优雅 / 强制），与 DlDisable 的环境层 Stop 二选一。
func (s *DownloadService) DlShutdown(force bool) *services.ApiResult {
	if err := s.simpleCall(func(c *aria2.Client) error { return c.Shutdown(force) }); err != nil {
		return services.Fail(err)
	}
	s.stopPoller()
	return services.Ok(nil)
}

// ===== 任务新建 =====

func (s *DownloadService) DlAdd(uri, dir, optsJSON string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	gid, err := s.addOne(uri, dir, optsJSON, "manual")
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(gid)
}

func (s *DownloadService) DlBatchAdd(urisJSON, dir string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	var uris []string
	if err := json.Unmarshal([]byte(urisJSON), &uris); err != nil {
		return services.Fail(fmt.Errorf("链接列表格式不正确: %w", err))
	}
	added := 0
	var lastErr error
	for _, u := range uris {
		if _, err := s.addOne(u, dir, "", "batch"); err != nil {
			lastErr = err
			continue
		}
		added++
	}
	if added == 0 && lastErr != nil {
		return services.Fail(lastErr)
	}
	return services.Ok(added)
}

// DlAddTorrent 从本地 .torrent 文件建任务：Go 读文件转 base64，前端拿不到文件内容以外的一切。
func (s *DownloadService) DlAddTorrent(filePath, dir, optsJSON string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if !strings.EqualFold(filepath.Ext(filePath), ".torrent") {
		return services.FailMsg("请选择 .torrent 种子文件")
	}
	gids, err := s.addBinary("torrent", filePath, dir, optsJSON)
	if err != nil {
		return services.Fail(err)
	}
	if len(gids) == 0 {
		return services.FailMsg("aria2 未返回任务 GID")
	}
	return services.Ok(gids[0])
}

// DlAddMetalink 支持 .metalink / .meta4，一次可能返回多个 GID。
func (s *DownloadService) DlAddMetalink(filePath, dir, optsJSON string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".metalink" && ext != ".meta4" {
		return services.FailMsg("请选择 .metalink / .meta4 文件")
	}
	gids, err := s.addBinary("metalink", filePath, dir, optsJSON)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(gids)
}

func (s *DownloadService) addBinary(kind, filePath, dir, optsJSON string) ([]string, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取种子文件失败: %w", err)
	}
	if len(raw) == 0 || len(raw) > 32<<20 {
		return nil, fmt.Errorf("种子文件为空或超过 32MB")
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return nil, err
	}
	c := aria2.NewClient(cfg)
	if _, err := c.GetVersion(); err != nil {
		return nil, fmt.Errorf("aria2 未运行，请先启用下载器")
	}
	opts, dir, err := baseOptions(cfg, dir, optsJSON)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(filePath)
	b64 := base64.StdEncoding.EncodeToString(raw)
	var gids []string
	pos := takePosition(opts)
	if kind == "torrent" {
		gid, err := c.AddTorrent(b64, []string{}, opts, pos)
		if err != nil {
			return nil, err
		}
		gids = []string{gid}
	} else {
		gids, err = c.AddMetalink(b64, opts, pos)
		if err != nil {
			return nil, err
		}
	}
	for _, gid := range gids {
		s.insertMeta(gid, filePath, name, dir, kind)
	}
	s.invalidate()
	s.startPoller()
	return gids, nil
}

func (s *DownloadService) addOne(uri, dir, optsJSON, source string) (string, error) {
	uri = strings.TrimSpace(uri)
	if err := validateURI(uri); err != nil {
		return "", err
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return "", err
	}
	c := aria2.NewClient(cfg)
	if _, err := c.GetVersion(); err != nil {
		return "", fmt.Errorf("aria2 未运行，请先启用下载器")
	}
	opts, dir, err := baseOptions(cfg, dir, optsJSON)
	if err != nil {
		return "", err
	}
	if source == "manual" || source == "batch" {
		if d := categoryDir(cfg, dir, uri); d != "" {
			dir = d
			opts["dir"] = d
		}
		if d := taskFolderDir(cfg, dir, uri); d != dir {
			// 本机引擎先建出子目录：aria2 对不存在的 dir 行为不一致，且前端磁盘预检
			// 与断点续传都依赖这个真实路径。远程引擎的路径属于远端机器，不能在本机建。
			if !cfg.IsRemote() {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return "", fmt.Errorf("创建下载子目录失败: %w", err)
				}
			}
			dir = d
			opts["dir"] = d
		}
	}
	pos := takePosition(opts)
	gid, err := c.AddURIAt([]string{uri}, opts, pos)
	if err != nil {
		return "", err
	}
	s.insertMeta(gid, uri, nameFromURL(uri), dir, source)
	s.invalidate()
	s.startPoller()
	return gid, nil
}

// takePosition 从 optsJSON 里取出前端的 __position（队列插入位置）并从选项表中删掉：
// position 是 addUri 的第 4 个参数，不是 aria2 选项，混进 options 会被当作非法键。
func takePosition(opts map[string]string) int {
	v, ok := opts["__position"]
	delete(opts, "__position")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func baseOptions(cfg *aria2.Config, dir, optsJSON string) (map[string]string, string, error) {
	if dir == "" {
		dir = cfg.DownloadDir
	}
	opts := map[string]string{
		"dir":                       dir,
		"split":                     strconv.Itoa(cfg.Split),
		"max-connection-per-server": strconv.Itoa(cfg.MaxConnPerSrv),
		"continue":                  "true",
	}
	if optsJSON != "" {
		var extra map[string]string
		if err := json.Unmarshal([]byte(optsJSON), &extra); err != nil {
			return nil, "", fmt.Errorf("下载选项格式不正确: %w", err)
		}
		for k, v := range extra {
			opts[k] = v
		}
	}
	return opts, dir, nil
}

func (s *DownloadService) insertMeta(gid, uri, name, dir, source string) {
	if err := s.App.DB.InsertDlTask(db.DlTask{
		GID:       gid,
		URL:       uri,
		Name:      name,
		SaveDir:   dir,
		Source:    source,
		CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		logger.W("[download] 登记任务元数据失败 gid=%s: %v", gid, err)
	}
}

// categoryDir 按扩展名把默认下载目录细分成子目录；用户显式指定目录时不接管。
func categoryDir(cfg *aria2.Config, dir, uri string) string {
	if !cfg.AutoCategory || cfg.DownloadDir == "" || dir != cfg.DownloadDir {
		return ""
	}
	name := nameFromURL(uri)
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if ext == "" {
		return ""
	}
	sub, ok := cfg.CategoryRules[ext]
	if !ok || sub == "" {
		return ""
	}
	return filepath.Join(dir, sub)
}

// taskFolderName 从链接解析出一个可安全用作目录名的名字（不含扩展名）。
// 拿不到可靠文件名时返回空串，调用方就不建子目录——宁可不分组也别造出怪目录。
func taskFolderName(uri string) string {
	if strings.HasPrefix(strings.ToLower(uri), "magnet:") {
		// 磁力 / BT 的真实名字要等元数据，dn 也未必是文件名，交给 aria2 自己决定。
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return ""
	}
	name := path.Base(u.Path)
	if decoded, e := url.PathUnescape(name); e == nil {
		name = decoded
	}
	// nameFromURL 解析失败时会把整条 URL 原样返回，不能拿它当目录名。
	if name == "" || name == "." || name == "/" || strings.Contains(name, "://") {
		return ""
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" {
		stem = name
	}
	stem = strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	).Replace(stem)
	stem = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, stem)
	stem = strings.Trim(stem, " .")
	if stem == "" || stem == "." || stem == ".." {
		return ""
	}
	// 80 个 rune 足够区分，也避免叠上分类目录后撞 Windows MAX_PATH。
	if rs := []rune(stem); len(rs) > 80 {
		stem = strings.Trim(string(rs[:80]), " .")
	}
	if stem == "" {
		return ""
	}
	// Windows 设备保留名不能直接做目录名。
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5",
		"COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4",
		"LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		stem = "_" + stem
	}
	return stem
}

// taskFolderDir 开启「每个下载单独文件夹」后返回 <dir>/<文件名>/。
// 自动分类在前（<默认目录>/<分类>/），文件名子目录接在它下面；同名文件复用
// 同一个目录，重新添加同一 URL 才能命中已有的 .aria2 续传控制文件。
func taskFolderDir(cfg *aria2.Config, dir, uri string) string {
	if !cfg.PerTaskFolder || strings.TrimSpace(dir) == "" {
		return dir
	}
	folder := taskFolderName(uri)
	if folder == "" {
		return dir
	}
	if cfg.IsRemote() {
		// 远端可能是 Linux：不能拿本机的分隔符去拼它的路径。
		// 按原路径风格保留盘符/斜杠，避免把 /mnt/dl 拼成 \mnt\dl。
		out := path.Join(strings.ReplaceAll(dir, "\\", "/"), folder)
		if strings.Contains(dir, "\\") {
			out = strings.ReplaceAll(out, "/", "\\")
		}
		return out
	}
	clean := filepath.Clean(dir)
	out := filepath.Join(clean, folder)
	// 双保险：清洗后的名字绝不能逃出目标目录。
	if !strings.HasPrefix(out, clean+string(filepath.Separator)) {
		return dir
	}
	return out
}

// ===== 任务控制 =====

func (s *DownloadService) DlList() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	tasks, err := s.snapshot()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(tasks)
}

func (s *DownloadService) DlPause(gid string) *services.ApiResult {
	if err := s.mutating(func(c *aria2.Client) error { return c.Pause(gid) }); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

func (s *DownloadService) DlForcePause(gid string) *services.ApiResult {
	if err := s.mutating(func(c *aria2.Client) error { return c.ForcePause(gid) }); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

func (s *DownloadService) DlResume(gid string) *services.ApiResult {
	if err := s.mutating(func(c *aria2.Client) error { return c.Unpause(gid) }); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DlPauseAll / DlResumeAll 用 aria2 原生批量方法，不再串行 N 次单 gid 调用。
func (s *DownloadService) DlPauseAll(force bool) *services.ApiResult {
	if err := s.mutating(func(c *aria2.Client) error { return c.PauseAll(force) }); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

func (s *DownloadService) DlResumeAll() *services.ApiResult {
	if err := s.mutating(func(c *aria2.Client) error { return c.UnpauseAll() }); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DlPurgeResults 一次性清掉全部已结束结果，并同步会话与 DB 元数据。
func (s *DownloadService) DlPurgeResults() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	c := aria2.NewClient(cfg)
	if err := c.PurgeDownloadResult(); err != nil {
		return services.Fail(err)
	}
	if err := c.SaveSession(); err != nil {
		logger.W("[download] 清除结果后同步会话失败: %v", err)
	}
	// 只有成功拿到存活列表才清元数据：RPC 失败时 alive 为空会把 DB 全清掉。
	statuses, listErr := c.ListStatuses()
	if listErr == nil {
		alive := map[string]bool{}
		for _, st := range statuses {
			alive[st.GID] = true
		}
		if metas, err := s.App.DB.ListDlTasks(); err == nil {
			for _, m := range metas {
				if !alive[m.GID] {
					if err := s.App.DB.DeleteDlTask(m.GID); err != nil {
						logger.W("[download] 清理孤儿元数据失败 gid=%s: %v", m.GID, err)
					}
				}
			}
		}
	} else {
		logger.W("[download] 清除结果后拉取存活任务失败，跳过元数据清理: %v", listErr)
	}
	s.invalidate()
	return services.Ok(nil)
}

func (s *DownloadService) DlRemove(gid string, force bool) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.mutating(func(c *aria2.Client) error { return c.Remove(gid, force) }); err != nil {
		return services.Fail(err)
	}
	if err := s.App.DB.DeleteDlTask(gid); err != nil {
		logger.W("[download] 删除任务元数据失败 gid=%s: %v", gid, err)
	}
	if err := s.simpleCall(func(c *aria2.Client) error { return c.SaveSession() }); err != nil {
		logger.W("[download] 同步 aria2 会话失败（任务已移除）: %v", err)
	}
	return services.Ok(nil)
}

// DlBatchAction 按传入 gid 的顺序逐条回报批量结果：
// 部分成功是常态（有的任务已经结束、有的 gid 已失效），不能整批一起判成败。
type DlBatchAction struct {
	GID   string `json:"gid"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

type DlBatchResult struct {
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
	Actions   []DlBatchAction `json:"actions"`
}

// DlBatch 一次批量执行 pause / resume / remove。
// 原先前端选中 N 个任务要串行 N 次 RPC（remove 是 2N 次），中途一项失败还会
// 拖慢后面全部；现在 pause/resume 固定 1 次、remove 固定 2 次 system.multicall。
func (s *DownloadService) DlBatch(action, gidsJSON string, force bool) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	var gids []string
	if err := json.Unmarshal([]byte(gidsJSON), &gids); err != nil {
		return services.FailMsg("任务列表格式不正确")
	}
	// 去重去空，避免同一 gid 收到两条指令；上限防止一次 multicall 把 RPC 撑爆。
	seen := make(map[string]bool, len(gids))
	clean := make([]string, 0, len(gids))
	for _, g := range gids {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		clean = append(clean, g)
	}
	if len(clean) == 0 {
		return services.Ok(DlBatchResult{Actions: []DlBatchAction{}})
	}
	if len(clean) > 200 {
		return services.FailMsg("一次最多批量操作 200 个任务")
	}

	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	c := aria2.NewClient(cfg)
	var results []aria2.MultiCallResult
	switch action {
	case "pause":
		results, err = c.PauseMany(clean)
	case "resume":
		results, err = c.UnpauseMany(clean)
	case "remove":
		results, err = c.RemoveMany(clean, force)
	default:
		return services.FailMsg("不支持的批量操作: " + action)
	}
	if err != nil {
		return services.Fail(err)
	}

	out := DlBatchResult{Actions: make([]DlBatchAction, 0, len(clean))}
	for i, gid := range clean {
		a := DlBatchAction{GID: gid, OK: true}
		if i < len(results) {
			a.OK = results[i].OK
			a.Error = results[i].Error
		} else {
			a.OK = false
			a.Error = "aria2 未返回该任务的结果"
		}
		if a.OK {
			out.Succeeded++
		} else {
			out.Failed++
		}
		out.Actions = append(out.Actions, a)
	}
	if action == "remove" {
		for _, a := range out.Actions {
			if !a.OK {
				continue
			}
			if err := s.App.DB.DeleteDlTask(a.GID); err != nil {
				logger.W("[download] 批量删除任务元数据失败 gid=%s: %v", a.GID, err)
			}
		}
		// 与单条 DlRemove 一致：不同步 session，aria2 重启后会用 aria2.session 复活 GID。
		if err := s.simpleCall(func(cl *aria2.Client) error { return cl.SaveSession() }); err != nil {
			logger.W("[download] 批量删除后同步 aria2 会话失败: %v", err)
		}
	}
	s.invalidate()
	return services.Ok(out)
}

// DlChangePosition 调整等待队列位置，how 取 POS_SET / POS_CUR / POS_END。
func (s *DownloadService) DlChangePosition(gid string, pos int, how string) *services.ApiResult {
	switch how {
	case "POS_SET", "POS_CUR", "POS_END":
	default:
		return services.FailMsg("队列位置参数不合法")
	}
	var out int
	err := s.mutating(func(c *aria2.Client) error {
		v, e := c.ChangePosition(gid, pos, how)
		out = v
		return e
	})
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(out)
}

func (s *DownloadService) DlChangeUri(gid string, fileIndex int, delUrisJSON, addUrisJSON string) *services.ApiResult {
	var delUris, addUris []string
	if err := json.Unmarshal([]byte(delUrisJSON), &delUris); err != nil {
		return services.Fail(fmt.Errorf("待移除链接格式不正确: %w", err))
	}
	if err := json.Unmarshal([]byte(addUrisJSON), &addUris); err != nil {
		return services.Fail(fmt.Errorf("待添加链接格式不正确: %w", err))
	}
	var counts []int
	err := s.mutating(func(c *aria2.Client) error {
		v, e := c.ChangeURI(gid, fileIndex, delUris, addUris)
		counts = v
		return e
	})
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(counts)
}

// ===== 详情 / 选项 =====

func (s *DownloadService) DlDetail(gid string) *services.ApiResult {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	c := aria2.NewClient(cfg)
	out := DlDetailView{
		GID:          gid,
		Options:      map[string]string{},
		Files:        []DlFileView{},
		URIs:         []DlURIView{},
		Peers:        []DlPeerView{},
		Servers:      []DlServerView{},
		AnnounceList: [][]string{},
	}
	st, err := c.TellStatus(gid)
	if err != nil {
		return services.Fail(err)
	}
	if st.Bittorrent != nil {
		out.AnnounceList = st.Bittorrent.AnnounceList
		out.TorrentComment = st.Bittorrent.Comment
		out.TorrentMode = st.Bittorrent.Mode
		out.TorrentCreationDate = st.Bittorrent.CreationDate
	}
	if opts, e := c.GetOption(gid); e == nil {
		out.Options = opts
	} else {
		logger.W("[download] 读取任务选项失败 gid=%s: %v", gid, e)
	}
	if files, e := c.GetFiles(gid); e == nil {
		out.Files = toFileViews(files)
		// 暂停后 aria2 连 files[].length / completedLength 也报 0，
		// 详情弹框的「文件」页同样用最后一次已知值补齐（仅单文件任务）。
		if k, ok := s.sizeOf(gid); ok && len(out.Files) == 1 {
			if out.Files[0].Length == 0 {
				out.Files[0].Length = k.total
			}
			if out.Files[0].Completed == 0 {
				out.Files[0].Completed = k.completed
			}
		}
	} else {
		logger.W("[download] 读取文件列表失败 gid=%s: %v", gid, e)
	}
	if uris, e := c.GetUris(gid); e == nil {
		out.URIs = toURIViews(uris)
	} else {
		logger.W("[download] 读取 URI 列表失败 gid=%s: %v", gid, e)
	}
	// getPeers / getServers 只对部分协议有效，失败不算详情失败，留空表即可。
	if peers, e := c.GetPeers(gid); e == nil {
		out.Peers = toPeerViews(peers)
	}
	if servers, e := c.GetServers(gid); e == nil {
		out.Servers = toServerViews(servers)
	}
	return services.Ok(out)
}

func toFileViews(in []aria2.RawFile) []DlFileView {
	out := make([]DlFileView, 0, len(in))
	for _, f := range in {
		v := DlFileView{
			Index:     parseInt64(f.Index),
			Path:      f.Path,
			Length:    parseInt64(f.Length),
			Completed: parseInt64(f.CompletedLength),
			Selected:  strings.EqualFold(f.Selected, "true"),
			URIs:      toURIViews(f.URIs),
		}
		out = append(out, v)
	}
	return out
}

func toURIViews(in []aria2.RawURI) []DlURIView {
	out := make([]DlURIView, 0, len(in))
	for _, u := range in {
		out = append(out, DlURIView{URI: u.URI, Status: u.Status})
	}
	return out
}

func toPeerViews(in []aria2.RawPeer) []DlPeerView {
	out := make([]DlPeerView, 0, len(in))
	for _, p := range in {
		out = append(out, DlPeerView{
			PeerID:        p.PeerID,
			IP:            p.IP,
			Port:          int(parseInt64(p.Port)),
			DownloadSpeed: parseInt64(p.DownloadSpeed),
			UploadSpeed:   parseInt64(p.UploadSpeed),
			Seeder:        strings.EqualFold(p.Seeder, "true"),
			AmChoking:     strings.EqualFold(p.AmChoking, "true"),
			PeerChoking:   strings.EqualFold(p.PeerChoking, "true"),
		})
	}
	return out
}

func toServerViews(in []aria2.RawServer) []DlServerView {
	out := []DlServerView{}
	for _, group := range in {
		for _, srv := range group.Servers {
			out = append(out, DlServerView{
				Index:      int(parseInt64(group.Index)),
				URI:        srv.URI,
				CurrentURI: srv.CurrentURI,
				Status:     srv.Status,
				Speed:      parseInt64(srv.DownloadSpeed),
				Error:      srv.ErrorMessage,
			})
		}
	}
	return out
}

// DlSetTaskOption 修改单任务选项（限速、做种、加密 …），optsJSON 是 key→string。
func (s *DownloadService) DlSetTaskOption(gid, optsJSON string) *services.ApiResult {
	opts, err := decodeOpts(optsJSON)
	if err != nil {
		return services.Fail(err)
	}
	if len(opts) == 0 {
		return services.FailMsg("没有需要修改的选项")
	}
	if err := s.mutating(func(c *aria2.Client) error { return c.ChangeOption(gid, opts) }); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DlSetFileSelection 把前端勾选的索引转成 aria2 的 select-file（1 基、逗号分隔）。
func (s *DownloadService) DlSetFileSelection(gid, indexesJSON string) *services.ApiResult {
	var idx []int
	if err := json.Unmarshal([]byte(indexesJSON), &idx); err != nil {
		return services.Fail(fmt.Errorf("文件索引格式不正确: %w", err))
	}
	parts := []string{}
	for _, i := range idx {
		if i > 0 {
			parts = append(parts, strconv.Itoa(i))
		}
	}
	if len(parts) == 0 {
		return services.FailMsg("至少要选择一个文件")
	}
	err := s.mutating(func(c *aria2.Client) error {
		return c.ChangeOption(gid, map[string]string{"select-file": strings.Join(parts, ",")})
	})
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

func decodeOpts(optsJSON string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(optsJSON) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(optsJSON), &out); err != nil {
		return nil, fmt.Errorf("选项格式不正确: %w", err)
	}
	return out, nil
}

func (s *DownloadService) DlGlobalStat() *services.ApiResult {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	st, err := aria2.NewClient(cfg).GetGlobalStat()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(GlobalStatView{
		DownloadSpeed: parseInt64(st.DownloadSpeed),
		UploadSpeed:   parseInt64(st.UploadSpeed),
		Active:        int(parseInt64(st.NumActive)),
		Waiting:       int(parseInt64(st.NumWaiting)),
		Stopped:       int(parseInt64(st.NumStopped)),
		StoppedTotal:  int(parseInt64(st.NumStoppedTotal)),
	})
}

func (s *DownloadService) DlGlobalOptions() *services.ApiResult {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	opts, err := aria2.NewClient(cfg).GetGlobalOption()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(opts)
}

// DlSetGlobalOption 高级入口：把 aria2 原生全局选项原样下发给运行中的 daemon（不落盘）。
func (s *DownloadService) DlSetGlobalOption(optsJSON string) *services.ApiResult {
	opts, err := decodeOpts(optsJSON)
	if err != nil {
		return services.Fail(err)
	}
	if len(opts) == 0 {
		return services.FailMsg("没有需要修改的选项")
	}
	if err := s.mutating(func(c *aria2.Client) error { return c.ChangeGlobalOption(opts) }); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ===== 系统动作 =====

// DlReveal 在系统文件管理器里定位文件/目录。用户主动点击才触发，不自动扫盘。
func (s *DownloadService) DlReveal(target string) *services.ApiResult {
	if strings.TrimSpace(target) == "" {
		return services.FailMsg("路径为空")
	}
	if err := platform.RevealInExplorer(target); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ===== 设置 =====

func (s *DownloadService) DlOptions() *services.ApiResult {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(optionsView(cfg))
}

func (s *DownloadService) DlSetOptions(optsJSON string) *services.ApiResult {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return services.Fail(err)
	}
	before := cfg.RPCBase() + "|" + cfg.RPCSecret()
	var in DlOptionsView
	if err := json.Unmarshal([]byte(optsJSON), &in); err != nil {
		return services.Fail(fmt.Errorf("设置格式不正确: %w", err))
	}
	applyOptionsInput(cfg, in)
	if err := aria2.SaveConfig(cfg); err != nil {
		return services.Fail(err)
	}
	s.applyRuntimeOptions(cfg, "设置保存")
	if before != cfg.RPCBase()+"|"+cfg.RPCSecret() {
		// 连接目标变了：旧 WS / 轮询全停，按新配置重新握手后再起。
		s.stopPoller()
		if _, e := aria2.NewClient(cfg).GetVersion(); e == nil {
			s.startPoller()
		}
	}
	// DHT / 监听端口这类只能在启动时决定的选项无法热更新，交给用户重启引擎。
	return services.Ok(optionsView(cfg))
}

func dlBoolArg(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// applyRuntimeOptions 只下发 aria2 允许 changeGlobalOption 热改的选项，分两批避免
// 某一项被拒绝时把整批都带崩；不能热改的（DHT / 端口 / 会话文件）只落盘等重启。
func (s *DownloadService) applyRuntimeOptions(cfg *aria2.Config, reason string) {
	c := aria2.NewClient(cfg)
	// 若当前正处在限速时段，下发时段限速而不是全局限速，否则保存设置会把时段规则顶掉。
	dl, ul := cfg.DownloadLimit, cfg.UploadLimit
	if cfg.Schedule.Enabled && inTimeWindow(cfg.Schedule.Start, cfg.Schedule.End, time.Now()) {
		if cfg.Schedule.DownloadLimit != "" {
			dl = cfg.Schedule.DownloadLimit
		}
		if cfg.Schedule.UploadLimit != "" {
			ul = cfg.Schedule.UploadLimit
		}
	}
	core := map[string]string{
		"max-concurrent-downloads":   strconv.Itoa(cfg.MaxConcurrent),
		"split":                      strconv.Itoa(cfg.Split),
		"max-connection-per-server":  strconv.Itoa(cfg.MaxConnPerSrv),
		"max-overall-download-limit": aria2.SpeedLimitArg(dl),
		"max-overall-upload-limit":   aria2.SpeedLimitArg(ul),
	}
	if cfg.MinSplitSize != "" {
		core["min-split-size"] = cfg.MinSplitSize
	}
	if cfg.UserAgent != "" {
		core["user-agent"] = cfg.UserAgent
	}
	if cfg.Proxy != "" {
		core["all-proxy"] = cfg.Proxy
	}
	if err := c.ChangeGlobalOption(core); err != nil {
		logger.W("[download] %s：下发全局选项失败（已保存，重启后生效）: %v", reason, err)
	}
	bt := map[string]string{
		"bt-max-peers":              strconv.Itoa(cfg.BtMaxPeers),
		"bt-force-encryption":       dlBoolArg(cfg.BtForceEncryption),
		"bt-require-crypto":         dlBoolArg(cfg.BtRequireCrypto),
		"bt-remove-unselected-file": dlBoolArg(cfg.BtRemoveUnselected),
		"check-integrity":           dlBoolArg(cfg.CheckIntegrity),
	}
	if cfg.SeedRatio != "" {
		bt["seed-ratio"] = cfg.SeedRatio
	}
	if cfg.SeedTime != "" {
		bt["seed-time"] = cfg.SeedTime
	}
	if cfg.BtTracker != "" {
		bt["bt-tracker"] = cfg.BtTracker
	}
	if err := c.ChangeGlobalOption(bt); err != nil {
		logger.W("[download] %s：下发 BT 选项失败（已保存，重启后生效）: %v", reason, err)
	}
	// file-allocation 是启动参数类选项，单独一批下发：部分 aria2 构建不接受热改，
	// 失败也不影响前面的 core / bt，落盘后重启引擎即可生效。
	if err := c.ChangeGlobalOption(map[string]string{"file-allocation": cfg.FileAllocationArg()}); err != nil {
		logger.W("[download] %s：预分配策略不支持热改（已保存，重启后生效）: %v", reason, err)
	}
}

// ===== 轮询与事件 =====

// taskSize 记住 aria2 最后一次报出的真实大小。pause / forcePause 会把任务移进
// waiting 队列并释放下载上下文，此时 tellStatus / tellWaiting 把 totalLength、
// completedLength 乃至 files[].length 全报成 "0"（实测 500GiB 任务暂停后返回
// 0/0/空 path、URI 全是 waiting），要等 unpause 重新连上服务器才恢复。
// 直接把 0 渲染出去，用户会以为进度丢了。
type taskSize struct {
	total     int64
	completed int64
}

// applyKnownSize 每轮快照用真实值刷新缓存；只在两个长度都为 0 时回填，
// 免得拿旧值盖住 aria2 重新报告的真实进度。
func (s *DownloadService) applyKnownSize(v *aria2.TaskView) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	if s.knownSize == nil {
		s.knownSize = map[string]taskSize{}
	}
	k := s.knownSize[v.GID]
	if v.Total > 0 {
		k.total = v.Total
	}
	if v.Completed > 0 {
		k.completed = v.Completed
	}
	if v.Total == 0 && v.Completed == 0 && (k.total > 0 || k.completed > 0) {
		v.Total, v.Completed = k.total, k.completed
		if v.Total > 0 {
			v.Progress = float64(v.Completed) * 100 / float64(v.Total)
			if v.Progress > 100 {
				v.Progress = 100
			}
		}
	}
	s.knownSize[v.GID] = k
}

func (s *DownloadService) sizeOf(gid string) (taskSize, bool) {
	s.sizeMu.RLock()
	defer s.sizeMu.RUnlock()
	k, ok := s.knownSize[gid]
	return k, ok
}

func (s *DownloadService) setKnownTotal(gid string, total int64) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	if s.knownSize == nil {
		s.knownSize = map[string]taskSize{}
	}
	k := s.knownSize[gid]
	if total > k.total {
		k.total = total
	}
	s.knownSize[gid] = k
}

// probeUnknownSizes 给「暂停 / 排队且长度还是 0/0」的任务异步补一次总大小。
// 只取响应头，不下载任何字节；单飞 + 每个 gid 冷却，失败不再重试到冷却结束。
func (s *DownloadService) probeUnknownSizes(tasks []aria2.TaskView) {
	if len(tasks) == 0 || !s.sizeProbing.CompareAndSwap(false, true) {
		return
	}
	type candidate struct{ gid, url string }
	s.sizeMu.Lock()
	if s.sizeProbeTried == nil {
		s.sizeProbeTried = map[string]time.Time{}
	}
	now := time.Now()
	list := make([]candidate, 0, len(tasks))
	for _, v := range tasks {
		if v.Total != 0 || v.Completed != 0 || v.GID == "" {
			continue
		}
		if v.Status != "paused" && v.Status != "waiting" {
			continue
		}
		lower := strings.ToLower(v.URL)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			continue
		}
		if last, ok := s.sizeProbeTried[v.GID]; ok && now.Sub(last) < sizeProbeCooldown {
			continue
		}
		s.sizeProbeTried[v.GID] = now
		list = append(list, candidate{v.GID, v.URL})
	}
	s.sizeMu.Unlock()
	if len(list) == 0 {
		s.sizeProbing.Store(false)
		return
	}
	go func() {
		defer s.sizeProbing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		urls := make([]string, len(list))
		for i, c := range list {
			urls[i] = c.url
		}
		r := aria2.ProbeURLs(ctx, urls)
		hit := false
		for i, it := range r.Items {
			if it.Size > 0 {
				s.setKnownTotal(list[i].gid, it.Size)
				hit = true
			}
		}
		if hit {
			s.invalidate()
		}
	}()
}

func (s *DownloadService) snapshot() ([]aria2.TaskView, error) {
	s.snapMu.Lock()
	defer s.snapMu.Unlock()
	return s.snapshotLocked(true)
}

// snapshotLocked 调用方必须已持有 snapMu。
// full=true 拉全量并刷新缓存；full=false 只问一次 tellActive，其余复用缓存。
func (s *DownloadService) snapshotLocked(full bool) ([]aria2.TaskView, error) {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return nil, err
	}
	c := aria2.NewClient(cfg)
	var statuses []aria2.Status
	if full {
		statuses, err = c.ListStatuses()
		if err != nil {
			return nil, err
		}
		rest := make([]aria2.Status, 0, len(statuses))
		for _, st := range statuses {
			if st.Status != "active" {
				rest = append(rest, st)
			}
		}
		s.restCache = rest
	} else {
		active, e := c.ListActive()
		if e != nil {
			return nil, e
		}
		statuses = append([]aria2.Status{}, active...)
		// 缓存里可能还留着刚从 waiting 转 active 的旧快照，直接拼接会让同一 GID
		// 出现两行（active + waiting），还会把完成检测的前一状态覆盖掉。
		activeGIDs := make(map[string]bool, len(active))
		for _, st := range active {
			activeGIDs[st.GID] = true
		}
		for _, st := range s.restCache {
			if !activeGIDs[st.GID] {
				statuses = append(statuses, st)
			}
		}
	}
	metas, err := s.App.DB.ListDlTasks()
	if err != nil {
		logger.W("[download] 读取任务元数据失败: %v", err)
		metas = []db.DlTask{}
	}
	byGID := make(map[string]db.DlTask, len(metas))
	for _, m := range metas {
		byGID[m.GID] = m
	}
	out := make([]aria2.TaskView, 0, len(statuses))
	for _, st := range statuses {
		v := st.ToView()
		s.applyKnownSize(&v)
		if m, ok := byGID[v.GID]; ok {
			v.Source = m.Source
			v.CreatedAt = m.CreatedAt
			if m.Name != "" && !v.IsTorrent {
				v.Name = m.Name
			}
			if v.URL == "" {
				v.URL = m.URL
			}
		}
		out = append(out, v)
	}
	s.probeUnknownSizes(out)
	return out, nil
}

func (s *DownloadService) startPoller() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 任何「启用 / 新建任务 / 握手成功」走到这里都视为重新连上。
	s.disconnected.Store(false)
	if s.ticker != nil {
		return
	}
	// 1s ticker：WS 在线时事件负责即时刷新，轮询降为 3s 兜底；断线回到 1s。
	tk := time.NewTicker(time.Second)
	stop := make(chan struct{})
	s.ticker, s.stopCh = tk, stop
	s.startEventsLocked()
	go func() {
		n := 0
		for {
			select {
			case <-tk.C:
				n++
				if s.wsAlive.Load() && n%3 != 0 {
					continue
				}
				s.emitSnapshot()
			case <-stop:
				return
			}
		}
	}()
	go s.scheduleLoop(stop)
}

// startEventsLocked 调用方必须已持有 s.mu。
func (s *DownloadService) startEventsLocked() {
	if s.eventsCancel != nil {
		return
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.eventsCancel = cancel
	aria2.Subscribe(ctx, cfg, s.onEventConnect, s.onAria2Event)
}

func (s *DownloadService) onEventConnect(ok bool) {
	s.wsAlive.Store(ok)
	if app := s.App.App(); app != nil {
		app.Event.Emit("quickdock:dl:ws", ok)
	}
}

func (s *DownloadService) onAria2Event(n aria2.Notification) {
	// 事件到达就立刻要一次全量快照（异步，别卡住 WS 读循环）；
	// 终态通知由 emitSnapshot 统一发，notified 保证 WS 与轮询不会重复打扰。
	_ = n
	s.forceFull.Store(true)
	if !s.eventPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.eventPending.Store(false)
		s.emitSnapshot()
	}()
}

func (s *DownloadService) stopPoller() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ticker != nil {
		s.ticker.Stop()
		s.ticker = nil
	}
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	if s.eventsCancel != nil {
		s.eventsCancel()
		s.eventsCancel = nil
	}
	s.wsAlive.Store(false)
}

func (s *DownloadService) emitSnapshot() {
	if !s.snapMu.TryLock() {
		return
	}
	defer s.snapMu.Unlock()

	s.tickCount++
	full := s.forceFull.Swap(false) || s.tickCount%5 == 0
	tasks, err := s.snapshotLocked(full)
	s.mu.Lock()
	if err != nil {
		// 全量刷新失败要把标记放回去，否则下一轮会带着旧缓存走快路径。
		if full {
			s.forceFull.Store(true)
		}
		s.failStreak++
		streak := s.failStreak
		if streak == 3 || streak%30 == 0 {
			logger.W("[download] 轮询 aria2 连续失败 %d 次: %v", streak, err)
		}
		s.mu.Unlock()
		// 连续失败到阈值才自愈：单次超时可能只是 daemon 正在保存会话 / 合并分片。
		// 异步执行，别把 Stop+Start 阻塞在快照推送链路上。
		if streak >= 6 {
			go s.recoverEngine(err)
		}
		return
	}
	if s.failStreak >= 3 {
		logger.I("[download] aria2 状态轮询已恢复")
	}
	s.failStreak = 0
	newly := []aria2.TaskView{}
	failed := []aria2.TaskView{}
	for _, t := range tasks {
		old := s.prev[t.GID]
		if old != "" && old != t.Status {
			if t.Status == "complete" {
				newly = append(newly, t)
			}
			if t.Status == "error" {
				failed = append(failed, t)
			}
		}
		s.prev[t.GID] = t.Status
	}
	// 任务被移除后清掉去重标记，否则重试生成的新状态可能被旧记录挡住。
	alive := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		alive[t.GID] = true
	}
	for k := range s.notified {
		if i := strings.LastIndexByte(k, ':'); i > 0 && !alive[k[:i]] {
			delete(s.notified, k)
		}
	}
	for gid := range s.prev {
		if !alive[gid] {
			delete(s.prev, gid)
		}
	}
	s.sizeMu.Lock()
	for gid := range s.knownSize {
		if !alive[gid] {
			delete(s.knownSize, gid)
		}
	}
	for gid := range s.sizeProbeTried {
		if !alive[gid] {
			delete(s.sizeProbeTried, gid)
		}
	}
	s.sizeMu.Unlock()
	s.mu.Unlock()

	if app := s.App.App(); app != nil {
		app.Event.Emit("quickdock:dl:tasks", tasks)
		// 全局统计按真实时间 5s 一次：WS 在线时轮询会降频，按 tick 计数会越推越慢。
		if time.Since(s.lastGlobal) >= 5*time.Second {
			s.lastGlobal = time.Now()
			if cfg, e := aria2.LoadConfig(); e == nil {
				if g, e := aria2.NewClient(cfg).GetGlobalStat(); e == nil {
					app.Event.Emit("quickdock:dl:global", GlobalStatView{
						DownloadSpeed: parseInt64(g.DownloadSpeed),
						UploadSpeed:   parseInt64(g.UploadSpeed),
						Active:        int(parseInt64(g.NumActive)),
						Waiting:       int(parseInt64(g.NumWaiting)),
						Stopped:       int(parseInt64(g.NumStopped)),
						StoppedTotal:  int(parseInt64(g.NumStoppedTotal)),
					})
				}
			}
		}
	}
	for _, t := range newly {
		s.notifyTerminal(t, true)
	}
	for _, t := range failed {
		s.notifyTerminal(t, false)
	}
	// 同时完成多个任务时只打开第一个所在目录，避免一次弹出一堆资源管理器窗口。
	if len(newly) > 0 {
		s.onCompleted(newly[0])
	}
	s.maybeGuardDisk(tasks)
}

// ===== 磁盘守卫 / 引擎自愈 =====

const (
	// 0/0 任务的 URL 大小探测：失败（预签名过期等）也冷却，别每轮发请求。
	sizeProbeCooldown = 5 * time.Minute
	// 磁盘检查是本地系统调用，但别跟 1s 轮询一起跑；30s 足够在写满前拦住。
	diskGuardInterval = 30 * time.Second
	// 低于 1GiB 就暂停：aria2 的 write cache 刷盘失败会让整个 daemon 卡死，
	// 等真的写到 0 字节再处理已经来不及了。
	diskGuardMinFree = uint64(1) << 30
	// 自愈冷却：避免卡死实例反复 Stop+Start 形成重启风暴。
	engineRecoverCooldown = 60 * time.Second
)

// maybeGuardDisk 只在间隔到了时才起一次检查，避免每轮快照都创建 goroutine。
func (s *DownloadService) maybeGuardDisk(tasks []aria2.TaskView) {
	s.mu.Lock()
	if time.Since(s.diskCheckedAt) < diskGuardInterval {
		s.mu.Unlock()
		return
	}
	s.diskCheckedAt = time.Now()
	s.mu.Unlock()
	go s.guardDiskSpace(tasks)
}

// guardDiskSpace 在活跃任务把目标盘写满之前强制暂停全部任务。
// 这是「下载过大文件 → aria2 daemon 无响应」的治本兜底：新建弹框的大小预检
// 只能覆盖添加瞬间，多任务并行/其它程序占盘仍会把剩余空间吃到 0。
func (s *DownloadService) guardDiskSpace(tasks []aria2.TaskView) {
	if s.App == nil {
		return
	}
	cfg, err := aria2.LoadConfig()
	if err != nil || cfg.IsRemote() {
		return // 文件写在远端，本机剩余空间不适用
	}
	dirs := make(map[string]bool)
	active := false
	for _, t := range tasks {
		if t.Status != "active" {
			continue
		}
		active = true
		d := strings.TrimSpace(t.Dir)
		if d == "" {
			d = cfg.DownloadDir
		}
		if d != "" {
			// Windows 路径大小写不敏感，按盘去重免得同一卷查 N 次。
			dirs[strings.ToLower(filepath.Clean(d))] = true
		}
	}
	if !active {
		// 任务都停了，允许下次空间再次吃紧时重新触发。
		s.diskPaused.Store(false)
		return
	}
	if s.diskPaused.Load() {
		return
	}
	var minFree uint64
	found := false
	for d := range dirs {
		ds, e := aria2.CheckDiskSpace(d)
		if e != nil || ds.Free == 0 {
			continue
		}
		if !found || ds.Free < minFree {
			minFree, found = ds.Free, true
		}
	}
	if !found || minFree >= diskGuardMinFree {
		return
	}
	if err := s.simpleCall(func(c *aria2.Client) error { return c.PauseAll(true) }); err != nil {
		logger.W("[download] 磁盘空间不足，自动暂停全部任务失败: %v", err)
		return
	}
	s.diskPaused.Store(true)
	s.invalidate()
	logger.W("[download] 目标盘仅剩 %d 字节，已自动暂停全部任务，避免 aria2 写盘卡死", minFree)
	title := "磁盘空间不足"
	body := "目标盘剩余空间不足 1 GiB，已自动暂停全部下载任务。清理磁盘或更换保存目录后可继续。"
	s.App.PushNotification("system", "error", title, body, "page:download", "disk-guard")
	s.App.NotifyDesktop("dl-disk-guard", title, body)
}

// recoverEngine 处理「进程还在但 RPC 全超时 / 进程已退出」两种掉线。
// 环境层看门狗只看端口，识别不出卡死的 daemon，所以由轮询链路自己兜底。
func (s *DownloadService) recoverEngine(lastErr error) {
	if s.App == nil || s.App.Env == nil || s.disconnected.Load() {
		return
	}
	cfg, err := aria2.LoadConfig()
	if err != nil || cfg.IsRemote() {
		return // 远程引擎不归本机管，别去动用户机器上的进程
	}
	if !s.recovering.CompareAndSwap(false, true) {
		return
	}
	defer s.recovering.Store(false)
	s.mu.Lock()
	if time.Since(s.lastRecover) < engineRecoverCooldown {
		s.mu.Unlock()
		return
	}
	s.lastRecover = time.Now()
	s.mu.Unlock()

	logger.W("[download] aria2 连续无响应，尝试自愈重启: %v", lastErr)
	s.emitEnginePhase("recovering")
	ver := aria2Version()
	// Stop 内部按端口杀进程树（带镜像校验），能清掉卡死但还占着端口的实例。
	_ = s.App.Env.Stop(envengine.RuntimeAria2, ver)
	if err := s.App.Env.Start(envengine.RuntimeAria2, ver, nil); err != nil {
		logger.E("[download] aria2 自愈重启失败: %v", err)
		s.emitEnginePhase("failed")
		s.App.PushNotification("system", "error", "下载引擎异常",
			"引擎无响应且自动重启失败，请在环境管理中手动启动 aria2", "page:download", "engine")
		return
	}
	logger.I("[download] aria2 自愈重启完成，任务将从会话恢复")
	s.emitEnginePhase("recovered")
	s.App.PushNotification("system", "warning", "下载引擎已重启",
		"引擎曾无响应，已自动恢复；未完成任务从会话继续下载。", "page:download", "engine")
	s.invalidate()
}

// emitEnginePhase 把自愈进度推给下载页：卡死时界面原本会一直停在旧快照，
// 用户只看到日志里刷「daemon 未响应」，不知道引擎正在自动恢复。
func (s *DownloadService) emitEnginePhase(phase string) {
	if app := s.App.App(); app != nil {
		app.Event.Emit("quickdock:dl:engine", map[string]string{"phase": phase})
	}
}

// notifyTerminal 把一个终态推到 toast / 应用内通知中心 / 桌面气泡。
// 轮询与 WS 事件都会走到这里，notified 以 gid:status 去重，不会重复打扰。
func (s *DownloadService) notifyTerminal(t aria2.TaskView, ok bool) {
	if s.App == nil {
		return
	}
	key := t.GID + ":" + t.Status
	s.mu.Lock()
	if s.notified[key] {
		s.mu.Unlock()
		return
	}
	s.notified[key] = true
	s.mu.Unlock()

	cfg, err := aria2.LoadConfig()
	if err != nil || cfg.NotifyDisabled {
		return
	}
	level, title := "error", "下载失败"
	if ok {
		level, title = "success", "下载完成"
	}
	body := t.Name
	if !ok && t.Error != "" {
		body = t.Name + "：" + t.Error
	}
	notice := DlNotice{OK: ok, Name: t.Name, Error: t.Error}
	s.App.PushNotification("system", level, title, body, "page:download", t.GID)
	s.App.NotifyDesktop("dl-"+t.GID, title, body)
	if app := s.App.App(); app != nil {
		app.Event.Emit("quickdock:dl:notice", notice)
	}
}

func (s *DownloadService) onCompleted(t aria2.TaskView) {
	cfg, err := aria2.LoadConfig()
	if err != nil || !cfg.AutoOpenFolder {
		return
	}
	target := t.Path
	if target == "" {
		target = t.Dir
	}
	if target == "" {
		return
	}
	if err := platform.RevealInExplorer(target); err != nil {
		logger.W("[download] 打开下载目录失败: %v", err)
	}
}

// ===== 限速时段 =====

func (s *DownloadService) scheduleLoop(stop <-chan struct{}) {
	tk := time.NewTicker(time.Minute)
	defer tk.Stop()
	s.checkSchedule()
	for {
		select {
		case <-tk.C:
			s.checkSchedule()
		case <-stop:
			return
		}
	}
}

func (s *DownloadService) checkSchedule() {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return
	}
	in := false
	if cfg.Schedule.Enabled {
		in = inTimeWindow(cfg.Schedule.Start, cfg.Schedule.End, time.Now())
	}
	if s.scheduleOn.Swap(in) == in {
		return
	}
	dl, ul := cfg.DownloadLimit, cfg.UploadLimit
	if in {
		if cfg.Schedule.DownloadLimit != "" {
			dl = cfg.Schedule.DownloadLimit
		}
		if cfg.Schedule.UploadLimit != "" {
			ul = cfg.Schedule.UploadLimit
		}
	}
	opts := map[string]string{
		"max-overall-download-limit": aria2.SpeedLimitArg(dl),
		"max-overall-upload-limit":   aria2.SpeedLimitArg(ul),
	}
	if err := aria2.NewClient(cfg).ChangeGlobalOption(opts); err != nil {
		logger.W("[download] 限速时段切换失败（in=%v）: %v", in, err)
	} else {
		logger.I("[download] 限速时段切换 in=%v down=%s up=%s", in, dl, ul)
	}
}

func parseHHMM(v string) (int, bool) {
	p := strings.Split(strings.TrimSpace(v), ":")
	if len(p) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(p[0])
	if err != nil {
		return 0, false
	}
	m, err := strconv.Atoi(p[1])
	if err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// inTimeWindow 支持跨零点（Start > End，例如 23:00 - 07:00）。
func inTimeWindow(start, end string, now time.Time) bool {
	a, ok1 := parseHHMM(start)
	b, ok2 := parseHHMM(end)
	if !ok1 || !ok2 {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	if a <= b {
		return cur >= a && cur < b
	}
	return cur >= a || cur < b
}

// ===== 辅助 =====

func (s *DownloadService) simpleCall(fn func(*aria2.Client) error) error {
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return err
	}
	return fn(aria2.NewClient(cfg))
}

// mutating 在写操作成功后让下一轮轮询立刻全量，避免界面停在 5s 前的缓存状态。
func (s *DownloadService) mutating(fn func(*aria2.Client) error) error {
	if err := s.simpleCall(fn); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func validateURI(raw string) error {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "magnet:") {
		// btih(v1) / btmh(v2) 都算合法磁力；大小写不敏感。直接看整串而不依赖
		// url.Parse 把查询落到 RawQuery 还是 Opaque，与前端 isSupportedUri 保持一致。
		if !strings.Contains(lower, "xt=urn:bt") {
			return fmt.Errorf("磁力链接格式不正确")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("下载地址不合法")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ftp", "sftp":
		return nil
	default:
		return fmt.Errorf("暂不支持 %s 协议", u.Scheme)
	}
}

func nameFromURL(raw string) string {
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		u, err := url.Parse(raw)
		if err == nil {
			q := u.Query()
			if dn := q.Get("dn"); dn != "" {
				return dn
			}
			if dn := q.Get("DN"); dn != "" {
				return dn
			}
			xt := q.Get("xt")
			if xt == "" {
				xt = q.Get("XT")
			}
			if xt != "" {
				v := strings.TrimPrefix(strings.ToLower(xt), "urn:btih:")
				v = strings.TrimPrefix(v, "urn:btmh:")
				return "magnet-" + v
			}
		}
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	n := path.Base(u.Path)
	if n == "" || n == "." || n == "/" {
		return raw
	}
	return n
}
