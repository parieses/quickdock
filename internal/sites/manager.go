package sites

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/sysutil"
)

// Site 一个本地开发站点：域名 → 本地目录，经内置 HTTPS 服务以 https://<domain> 访问。
type Site struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Domain  string `json:"domain"` // 如 myapp.test
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"` // 禁用则不参与证书、hosts 与请求分发
	Running bool   `json:"running"` // 派生字段：HTTPS 服务是否在跑（不持久化）

	// Modules / ProxyPort 是「生成配置」弹窗里的模块选择，随站点持久化，
	// 下次打开弹窗自动回填（用户不必每次重勾）。只影响片段生成，不影响内置监听器。
	Modules   []string `json:"modules,omitempty"`
	ProxyPort int      `json:"proxyPort,omitempty"`
}

// CertIssuer 签发本地可信证书的能力（由宿主注入 env.Manager 的实现）。
// 用接口而非直接 import internal/env：sites 与 env 之间不建立编译期依赖，
// 单测可以塞一个假签发器而不需要真实 mkcert。
type CertIssuer interface {
	CertIssue(outDir, name string, hosts []string) (map[string]string, error)
}

// Status 站点服务的整体状态，前端据此解释「为什么打不开」。
type Status struct {
	Running    bool   `json:"running"`
	Port       int    `json:"port"`
	CertReady  bool   `json:"certReady"`
	CertError  string `json:"certError"`
	HostsOK    bool   `json:"hostsOk"`
	HostsError string `json:"hostsError"`
	HostsPath  string `json:"hostsPath"`
	// HostsBlock 是当前应写入 hosts 的区块文本：自动提权被拒（无 UAC 权限、
	// 组策略禁止提权、标准用户账号）时，用户还能照着它手工粘贴，不至于卡死。
	HostsBlock string `json:"hostsBlock"`
	// Elevated 宿主自身是否已是管理员。为 true 却写不进去，说明是安全软件锁定
	// 或文件被独占，提权也救不了——前端据此给出不同的提示，而不是引导用户点一次白弹的 UAC。
	Elevated bool `json:"elevated"`
}

// defaultPort 默认监听端口。443 是 https 标准端口，URL 里可省略端口号。
const defaultPort = 443

// domainRe 校验站点域名：小写字母/数字/连字符，至少两段。
// 要求两段是为了排除裸 "localhost"（与内置服务冲突）；
// 不校验 TLD 是否真实存在——本地开发域名本就千奇百怪（.test / .local / .dev / 公司内网域）。
var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// config sites.json 的磁盘格式。用对象而非裸数组，port 才有地方放。
type config struct {
	Port  int    `json:"port"`
	Sites []Site `json:"sites"`
}

// Manager 站点管理器：配置持久化到 <dir>/sites.json，证书放 <dir>/certs。
// 单一 HTTPS 监听器 + SNI 共用一张「覆盖全部启用域名」的证书——
// 每站点一张证书要多机器多个 GetCertificate 分支，收益为零。
type Manager struct {
	mu     sync.Mutex
	certMu sync.Mutex // 串行化证书签发（mkcert 是外部进程、慢，不能占着 mu）

	dir     string
	file    string
	certDir string
	port    int

	sites map[string]*Site

	ln       net.Listener
	srv      *http.Server
	cert     *tls.Certificate
	certSet  string // 当前证书覆盖的域名集合指纹（排序后拼接），用于判断是否需要重签
	certErr  string
	hostsErr string // 最近一次 hosts 同步失败原因（"" = 成功）

	issuer CertIssuer
}

// New 创建站点管理器。port<=0 或越界时退回 443。
func New(dir string, port int) *Manager {
	if port <= 0 || port > 65535 {
		port = defaultPort
	}
	_ = os.MkdirAll(dir, 0o755)
	m := &Manager{
		dir:     dir,
		file:    filepath.Join(dir, "sites.json"),
		certDir: filepath.Join(dir, "certs"),
		port:    port,
		sites:   map[string]*Site{},
	}
	m.load()
	return m
}

// SetCertIssuer 注入证书签发能力（应用启动时调用一次）。
func (m *Manager) SetCertIssuer(issuer CertIssuer) {
	m.mu.Lock()
	m.issuer = issuer
	m.mu.Unlock()
}

func (m *Manager) load() {
	data, err := os.ReadFile(m.file)
	if err != nil {
		return
	}
	var cfg config
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if cfg.Port > 0 && cfg.Port <= 65535 {
		m.port = cfg.Port
	}
	for i := range cfg.Sites {
		s := cfg.Sites[i]
		// 磁盘上的模块集合同样过一遍归一化：旧版本、手改 json 都可能留下已废弃/互斥的 id。
		s.Modules = NormalizeModules(s.Modules)
		m.sites[s.ID] = &s
	}
}

// persistLocked 落盘。调用方必须已持有 m.mu（对比 httpserve 的 persist 自带加锁——
// 那里造成了调用方持锁时死锁的坑，这里统一改成 *Locked 版本，边界更清楚）。
func (m *Manager) persistLocked() error {
	cfg := config{Port: m.port, Sites: make([]Site, 0, len(m.sites))}
	for _, s := range m.sites {
		cp := *s
		cp.Running = false // 运行态不持久化，重启后由 Resume 重新判定
		cfg.Sites = append(cfg.Sites, cp)
	}
	sort.Slice(cfg.Sites, func(i, j int) bool {
		if cfg.Sites[i].Domain != cfg.Sites[j].Domain {
			return cfg.Sites[i].Domain < cfg.Sites[j].Domain
		}
		return cfg.Sites[i].ID < cfg.Sites[j].ID
	})
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.file, data, 0o644)
}

// List 返回全部站点（Running 反映服务当前是否在跑）。
func (m *Manager) List() []Site {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

func (m *Manager) listLocked() []Site {
	out := make([]Site, 0, len(m.sites))
	for _, s := range m.sites {
		cp := *s
		cp.Running = m.srv != nil
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get 按 id 返回站点（Running 反映服务当前是否在跑）。
func (m *Manager) Get(id string) (Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sites[id]
	if !ok {
		return Site{}, fmt.Errorf("站点不存在")
	}
	cp := *s
	cp.Running = m.srv != nil
	return cp, nil
}

// CertPaths 返回内置监听器用的证书/私钥路径。
// 文件名固定（一张证书覆盖全部启用域名），故 nginx/caddy 配置生成可以引用同一份证书，
// 不必依赖内置服务是否在运行。
func (m *Manager) CertPaths() (cert, key string) {
	return filepath.Join(m.certDir, "sites-cert.pem"), filepath.Join(m.certDir, "sites-key.pem")
}

// Status 返回服务整体状态。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	domains := m.enabledDomainsLocked()
	st := Status{
		Running:    m.srv != nil,
		Port:       m.port,
		CertReady:  m.cert != nil,
		CertError:  m.certErr,
		HostsError: m.hostsErr,
		HostsPath:  hostsFilePath(),
		HostsBlock: renderHostsBlock(domains),
		Elevated:   sysutil.IsElevated(),
	}
	// HostsOK 以「文件里实际登记的域名集合 == 期望集合」为准，而不是「上次写入没报错」——
	// 用户手动改过 hosts、或文件被别的程序覆盖时，回显必须如实。
	if cur, err := hostsBlockDomains(st.HostsPath); err == nil {
		st.HostsOK = sameDomainSet(cur, domains)
	} else if st.HostsError == "" {
		st.HostsError = err.Error()
	}
	return st
}

// enabledDomainsLocked 当前需要证书 / hosts 覆盖的域名集合（含 localhost）。
func (m *Manager) enabledDomainsLocked() []string {
	out := make([]string, 0, len(m.sites)+1)
	out = append(out, "localhost")
	for _, s := range m.sites {
		if s.Enabled && s.Domain != "" {
			out = append(out, s.Domain)
		}
	}
	return normalizeDomains(out)
}

// validateDomain 校验域名并归一化（小写、去空白、去尾部点）。
func validateDomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", fmt.Errorf("域名不能为空")
	}
	if !domainRe.MatchString(d) {
		return "", fmt.Errorf("域名不合法: %s（示例：myapp.test）", raw)
	}
	return d, nil
}

// validateDir 校验目录存在且确实是目录，返回清理后的路径。
func validateDir(raw string) (string, error) {
	dir := filepath.Clean(strings.TrimSpace(raw))
	if dir == "" || dir == "." {
		return "", fmt.Errorf("目录不能为空")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("目录路径解析失败: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("目录不存在或不是有效目录: %s", raw)
	}
	return dir, nil
}

// siteSeq 与时间戳一起构成站点 ID 的进程内唯一性保证。
// 只用 time.Now().UnixNano() 不够：Windows 的时钟粒度下连续两次 Create 可能落在同一纳秒，
// ID 相撞会让后一个站点在 map 里覆盖前一个——静默丢站点，是最难查的那类 bug。
var siteSeq atomic.Uint64

// newSiteID 生成站点 ID：时间戳前缀保证跨重启近似有序，自增序号保证进程内唯一。
func newSiteID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), siteSeq.Add(1))
}

// Create 新增站点。域名唯一、目录必须存在。
func (m *Manager) Create(name, domain, dir string) (*Site, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("站点名称不能为空")
	}
	d, err := validateDomain(domain)
	if err != nil {
		return nil, err
	}
	dir, err = validateDir(dir)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	for _, s := range m.sites {
		if strings.EqualFold(s.Domain, d) {
			m.mu.Unlock()
			return nil, fmt.Errorf("域名 %s 已被站点「%s」占用", d, s.Name)
		}
	}
	site := &Site{
		ID:      newSiteID(),
		Name:    name,
		Domain:  d,
		Dir:     dir,
		Enabled: true,
	}
	m.sites[site.ID] = site
	err = m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		m.mu.Lock()
		delete(m.sites, site.ID)
		m.mu.Unlock()
		return nil, err
	}

	m.afterChange()
	cp := *site
	return &cp, nil
}

// Update 更新站点。domain 变化时需重新校验唯一性。
func (m *Manager) Update(id, name, domain, dir string, enabled bool) (*Site, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("站点名称不能为空")
	}
	d, err := validateDomain(domain)
	if err != nil {
		return nil, err
	}
	dir, err = validateDir(dir)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	site, ok := m.sites[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("站点不存在")
	}
	for oid, s := range m.sites {
		if oid != id && strings.EqualFold(s.Domain, d) {
			m.mu.Unlock()
			return nil, fmt.Errorf("域名 %s 已被站点「%s」占用", d, s.Name)
		}
	}
	prev := *site
	site.Name, site.Domain, site.Dir, site.Enabled = name, d, dir, enabled
	if err := m.persistLocked(); err != nil {
		*site = prev // 落盘失败回滚内存，避免内存与磁盘不一致
		m.mu.Unlock()
		return nil, err
	}
	cp := *site
	m.mu.Unlock()

	m.afterChange()
	return &cp, nil
}

// Delete 删除站点。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	if _, ok := m.sites[id]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("站点不存在")
	}
	delete(m.sites, id)
	err := m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.afterChange()
	return nil
}

// SetPort 修改监听端口（443 被占用/无权限时的降级出口）。服务运行中不允许改端口。
func (m *Manager) SetPort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("端口无效（1-65535）")
	}
	m.mu.Lock()
	if m.srv != nil {
		m.mu.Unlock()
		return fmt.Errorf("请先停止站点服务再修改端口")
	}
	prev := m.port
	m.port = port
	if err := m.persistLocked(); err != nil {
		m.port = prev
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	return nil
}

// SetModules 保存站点的配置模块选择（生成 nginx/caddy 片段时叠加）。
// 落盘前先归一化：未知模块被过滤、互斥关系被消解，
// 于是磁盘上存的一定是能真正生效的集合，回显与生成结果不会互相打架。
func (m *Manager) SetModules(id string, modules []string, proxyPort int) (*Site, error) {
	if proxyPort < 0 || proxyPort > 65535 {
		proxyPort = 0 // 0 = 用默认上游端口
	}
	mods := NormalizeModules(modules)

	m.mu.Lock()
	site, ok := m.sites[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("站点不存在")
	}
	prevMods, prevPort := site.Modules, site.ProxyPort
	site.Modules, site.ProxyPort = mods, proxyPort
	if err := m.persistLocked(); err != nil {
		site.Modules, site.ProxyPort = prevMods, prevPort // 落盘失败回滚内存
		m.mu.Unlock()
		return nil, err
	}
	cp := *site
	m.mu.Unlock()
	return &cp, nil
}
// 两个动作都只在服务运行时有意义——没跑就不需要证书和解析。
// 失败不返回错误：调用方（Create/Update/Delete）已经改成功了，
// 把「证书没签出来」「hosts 没权限写」当作 CRUD 失败会让用户重试一个已经生效的操作。
// 失败原因记进 Status，由前端如实回显并提供重试入口。
func (m *Manager) afterChange() {
	m.mu.Lock()
	running := m.srv != nil
	m.mu.Unlock()
	if !running {
		return
	}
	if _, err := m.ensureCert(); err != nil {
		logger.W("[sites] 证书签发失败: %v", err)
	}
	if err := m.SyncHosts(); err != nil {
		logger.W("[sites] hosts 同步失败: %v", err)
	}
}

// ensureCert 保证内存中的证书覆盖当前全部启用域名，域名集合变化时重新签发。
// 签发要跑外部 mkcert，必须在 certMu 下串行、且不持有 mu。
func (m *Manager) ensureCert() (*tls.Certificate, error) {
	m.certMu.Lock()
	defer m.certMu.Unlock()

	m.mu.Lock()
	domains := m.enabledDomainsLocked()
	fp := strings.Join(domains, ",")
	if m.cert != nil && m.certSet == fp {
		c := m.cert
		m.mu.Unlock()
		return c, nil
	}
	issuer, certDir := m.issuer, m.certDir
	m.mu.Unlock()

	if issuer == nil {
		m.setCertErr("证书签发能力未初始化")
		return nil, fmt.Errorf("证书签发能力未初始化（mkcert 运行时未就绪）")
	}
	paths, err := issuer.CertIssue(certDir, "sites", domains)
	if err != nil {
		m.setCertErr(err.Error())
		return nil, err
	}
	certPath, keyPath := paths["cert"], paths["key"]
	if certPath == "" || keyPath == "" {
		err := fmt.Errorf("证书签发未返回有效路径")
		m.setCertErr(err.Error())
		return nil, err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		err = fmt.Errorf("加载证书失败: %w", err)
		m.setCertErr(err.Error())
		return nil, err
	}

	m.mu.Lock()
	m.cert, m.certSet, m.certErr = &pair, fp, ""
	m.mu.Unlock()
	logger.I("[sites] 证书已就绪，覆盖域名: %s", fp)
	return &pair, nil
}

func (m *Manager) setCertErr(msg string) {
	m.mu.Lock()
	m.certErr = msg
	m.mu.Unlock()
}

// SyncHosts 把当前启用域名写入系统 hosts 的标记区块。
// 由用户显式触发（点「重新同步」），故被拒绝时会自动提权重试一次；仍失败则返回错误，
// 由前端提示并提供「复制区块手工粘贴」的兜底。
func (m *Manager) SyncHosts() error { return m.syncHosts(true) }

// ClearHosts 清除 hosts 里的 QuickDock 标记区块（停止服务时调用，避免留下
// 「域名能解析但没有服务在听」的悬空状态）。
func (m *Manager) ClearHosts() error {
	err := syncHostsTo(hostsFilePath(), nil, true)
	m.recordHostsErr(err)
	return err
}

// syncHosts 内部实现，escalate 决定权限不足时是否弹 UAC 重试。
func (m *Manager) syncHosts(escalate bool) error {
	m.mu.Lock()
	domains := m.enabledDomainsLocked()
	path := hostsFilePath()
	m.mu.Unlock()

	err := syncHostsTo(path, domains, escalate)
	m.recordHostsErr(err)
	return err
}

// recordHostsErr 记录最近一次 hosts 同步结果（"" 表示成功），供 Status 回显。
func (m *Manager) recordHostsErr(err error) {
	m.mu.Lock()
	if err != nil {
		m.hostsErr = err.Error()
	} else {
		m.hostsErr = ""
	}
	m.mu.Unlock()
}

// Start 启动 HTTPS 站点服务（先备好证书，再监听端口，最后同步 hosts）。
// 由用户显式点击触发，故 hosts 写不进去时会自动提权重试（弹一次 UAC）。
func (m *Manager) Start() error { return m.start(true) }

func (m *Manager) start(escalateHosts bool) error {
	m.mu.Lock()
	if m.srv != nil {
		m.mu.Unlock()
		return fmt.Errorf("站点服务已在运行")
	}
	port := m.port
	m.mu.Unlock()

	// 先备证书再监听：没证书就绑端口，只会让每个请求都停在握手里，前端也拿不到可读原因。
	if _, err := m.ensureCert(); err != nil {
		return err
	}

	// hosts 失败不阻断启动：证书与监听都是本机能力，hosts 只是「把域名解析过来」，
	// 用户也可能用自己的 DNS/代理。失败原因由 Status 回显。
	if err := m.syncHosts(escalateHosts); err != nil {
		logger.W("[sites] hosts 同步失败（站点仍启动）: %v", err)
	}

	// 只监听回环：本地开发站点没有对外暴露的理由，需要局域网访问时再显式放开。
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("监听端口 %d 失败：%w（可能被占用或权限不足，可在站点设置里改用 8443）", port, err)
	}
	tlsLn := tls.NewListener(ln, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			m.mu.Lock()
			c := m.cert
			m.mu.Unlock()
			if c == nil {
				return nil, fmt.Errorf("证书未就绪")
			}
			return c, nil
		},
	})
	srv := &http.Server{Handler: http.HandlerFunc(m.handle)}
	m.mu.Lock()
	m.ln, m.srv = tlsLn, srv
	m.mu.Unlock()

	go func() { _ = srv.Serve(tlsLn) }()
	logger.I("[sites] 站点服务已启动 port=%d 站点数=%d", port, len(m.List()))
	return nil
}

// Stop 停止服务并清除 hosts 标记区块。
func (m *Manager) Stop() error {
	m.mu.Lock()
	srv, ln := m.srv, m.ln
	m.srv, m.ln = nil, nil
	m.mu.Unlock()
	if srv == nil {
		return nil
	}
	// 关闭在锁外做，避免阻塞其它调用；字段已清空，List/Status 读到的是一致的「已停止」。
	_ = srv.Close()
	if ln != nil {
		_ = ln.Close()
	}
	if err := m.ClearHosts(); err != nil {
		logger.W("[sites] 停止后清理 hosts 失败: %v", err)
	}
	logger.I("[sites] 站点服务已停止")
	return nil
}

// Resume 应用启动时调用：存在已启用站点则自动拉起服务（用户此前显式启用过，
// 属于明确的持续意图）。失败只记日志——开机就弹一个绑定端口失败的错误没有意义，
// 状态在 Status 里可见，用户点「启动」即可得到完整报错。
//
// 这里刻意传 escalateHosts=false：应用一启动就弹 UAC 属于「用户没做任何动作却被要求授权」。
// hosts 服务本身不受影响，用户在面板上点「重新同步」时会正常提权。
func (m *Manager) Resume() {
	m.mu.Lock()
	anyEnabled := false
	for _, s := range m.sites {
		if s.Enabled {
			anyEnabled = true
			break
		}
	}
	m.mu.Unlock()
	if !anyEnabled {
		return
	}
	if err := m.start(false); err != nil {
		logger.W("[sites] 启动时自动拉起站点服务失败: %v", err)
	}
}

// handle 按请求域名分发到对应站点根目录。
func (m *Manager) handle(w http.ResponseWriter, r *http.Request) {
	domain := hostOnly(r.Host)
	m.mu.Lock()
	var dir string
	for _, s := range m.sites {
		if s.Enabled && strings.EqualFold(s.Domain, domain) {
			dir = s.Dir
			break
		}
	}
	m.mu.Unlock()
	if dir == "" {
		http.Error(w, "no site configured for host: "+domain, http.StatusNotFound)
		return
	}
	serveDir(w, r, dir)
}

// serveDir 提供静态文件，并禁用目录列举：
// 请求指向目录且无 index.html 时返回 403，避免任意程序遍历站点目录结构。
func serveDir(w http.ResponseWriter, r *http.Request, dir string) {
	if strings.HasSuffix(r.URL.Path, "/") {
		idx := filepath.Join(dir, filepath.Clean(r.URL.Path), "index.html")
		if _, err := os.Stat(idx); err != nil {
			http.Error(w, "directory listing disabled", http.StatusForbidden)
			return
		}
	}
	http.FileServer(http.Dir(dir)).ServeHTTP(w, r)
}

// hostOnly 从 Host 头里取出主机名：去掉端口，并剥掉 IPv6 的方括号。
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}
