package sites

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"path"
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

// Site 一个本地开发站点：域名 → 本地目录。
// QuickDock 自己不监听任何端口：只为 nginx/caddy 生成站点片段、签发证书、写 hosts 解析，
// 真正的对外服务由用户装的那两个服务器软件承担。要挂一个本地 HTTP 目录，走「环境」页的 HTTP 服务。
type Site struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"` // 如 myapp.test
	Dir    string `json:"dir"`
	// DocRoot 是「对外服务的根」相对于 Dir 的子目录，空 = 直接用 Dir。
	// PHP 框架的入口文件几乎都不在项目根：Laravel / ThinkPHP 是 public，
	// Yii2 是 web（advanced 模板为 frontend/web），Symfony 是 public。
	// 没有这一层，为框架生成的配置会把 root 指向项目根 —— .env、storage、vendor
	// 全部暴露在 web 根下，是本地开发最容易踩的安全坑。
	DocRoot string `json:"docRoot,omitempty"`
	Enabled bool   `json:"enabled"` // 禁用则不参与证书与 hosts

	// Modules / ProxyPort 是「生成配置」弹窗里的模块选择，随站点持久化，
	// 下次打开弹窗自动回填（用户不必每次重勾）。只影响片段生成。
	Modules   []string `json:"modules,omitempty"`
	ProxyPort int      `json:"proxyPort,omitempty"`

	// Configs 是用户手工编辑并保存的配置片段：backend → 片段内容。
	// 有值时「生成配置」弹窗用它回填（重新生成不会把它冲掉），空 = 用生成器的结果。
	// 用 map 而非单字段：同一站点可能既看 nginx 片段又看 Caddyfile 片段。
	Configs map[string]string `json:"configs,omitempty"`
}

// EffectiveDir 站点对外服务的实际根目录：文档根是项目目录下的子目录时返回拼接结果，
// 否则就是项目目录本身。生成配置与 PHP 探测都走它，两处的「文档根」语义才不会分叉。
func (s Site) EffectiveDir() string {
	if s.DocRoot == "" {
		return s.Dir
	}
	return filepath.Join(s.Dir, filepath.FromSlash(s.DocRoot))
}

// SiteNeedsPHP 判断站点是否需要生成 PHP 处理段（nginx 的 fastcgi_pass / caddy 的 php_fastcgi）。
//
// 装了 PHP 不等于每个站点都要跑 PHP：给纯静态站点（前端构建产物目录）多挂一段 FastCGI
// 是无用配置，还会让人以为站点依赖 PHP-FPM 的 9000 端口 —— 它根本不依赖。
//
// 两个判据，先看用户的显式选择，再看磁盘：
//  1. 勾了「纯静态 / SPA / 反向代理」模块的站点一律不算 PHP —— 这是用户明确表态；
//  2. 其余看文档根下有没有 .php 文件。只扫一层：框架入口（public/index.php、web/index.php、
//     WordPress 的根 index.php）必在文档根第一层，而递归遍历 node_modules / vendor 代价很高。
func SiteNeedsPHP(s Site) bool {
	for _, mod := range s.Modules {
		switch Module(mod) {
		case ModStatic, ModSPA, ModProxy:
			return false
		}
	}
	entries, err := os.ReadDir(s.EffectiveDir())
	if err != nil {
		return false // 目录不存在或读不了：按静态处理，用户重新生成配置即可
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".php") {
			return true
		}
	}
	return false
}

// cloneConfigs 复制一份 Configs，避免把内部 map 的引用交给调用方（List/Get 返回的是值拷贝，
// 但 map 是引用类型，浅拷贝仍会共享底层数据）。空 map 归一成 nil，免得落盘出现 "configs":{}。
func cloneConfigs(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// CertIssuer 签发本地可信证书的能力（由宿主注入 env.Manager 的实现）。
// 用接口而非直接 import internal/env：sites 与 env 之间不建立编译期依赖，
// 单测可以塞一个假签发器而不需要真实 mkcert。
type CertIssuer interface {
	CertIssue(outDir, name string, hosts []string) (map[string]string, error)
}

// Status 站点功能的整体状态，前端据此解释「为什么打不开」。
// 这里没有运行态：站点由 nginx/caddy 提供服务，跑没跑是那两者的状态，不归本站点管理器表达。
type Status struct {
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

// domainRe 校验站点域名：小写字母/数字/连字符，至少两段。
// 要求两段是为了排除裸 "localhost"（本地站点该有自己的主机名，而不是占掉本机回环名）；
// 不校验 TLD 是否真实存在——本地开发域名本就千奇百怪（.test / .local / .dev / 公司内网域）。
var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// config sites.json 的磁盘格式。用对象而非裸数组，以后加字段不必迁移。
// 旧版本里还带一个 port 字段（内置监听器的端口）——内置监听器已去掉，读到时忽略即可。
type config struct {
	Sites []Site `json:"sites"`
}

// Manager 站点管理器：配置持久化到 <dir>/sites.json，证书放 <dir>/certs。
//
// 只做三件事：维护站点集合、签发「覆盖全部启用域名」的 mkcert 证书（供 nginx/caddy 的
// 站点片段引用）、把启用域名写进系统 hosts。不监听任何端口。
type Manager struct {
	mu     sync.Mutex
	certMu sync.Mutex // 串行化证书签发（mkcert 是外部进程、慢，不能占着 mu）

	dir     string
	file    string
	certDir string
	// hostsPath 系统 hosts 文件路径。生产环境就是平台默认（hostsFilePath()）；
	// 单测把它指向临时文件 —— 站点增删改都会同步 hosts，不重定向的话跑一次单测
	// 就会改掉开发机真实的 hosts（在提权 shell 里尤其明显）。
	hostsPath string

	sites map[string]*Site

	cert     *tls.Certificate
	certSet  string // 当前证书覆盖的域名集合指纹（排序后拼接），用于判断是否需要重签
	certErr  string
	hostsErr string // 最近一次 hosts 同步失败原因（"" = 成功）

	issuer CertIssuer
}

// New 创建站点管理器。
func New(dir string) *Manager {
	_ = os.MkdirAll(dir, 0o755)
	m := &Manager{
		dir:       dir,
		file:      filepath.Join(dir, "sites.json"),
		certDir:   filepath.Join(dir, "certs"),
		hostsPath: hostsFilePath(),
		sites:     map[string]*Site{},
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
	cfg := config{Sites: make([]Site, 0, len(m.sites))}
	for _, s := range m.sites {
		cfg.Sites = append(cfg.Sites, *s)
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

// List 返回全部站点。
func (m *Manager) List() []Site {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

func (m *Manager) listLocked() []Site {
	out := make([]Site, 0, len(m.sites))
	for _, s := range m.sites {
		cp := *s
		cp.Configs = cloneConfigs(s.Configs)
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

// Get 按 id 返回站点。
func (m *Manager) Get(id string) (Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sites[id]
	if !ok {
		return Site{}, fmt.Errorf("站点不存在")
	}
	cp := *s
	cp.Configs = cloneConfigs(s.Configs)
	return cp, nil
}

// CertPaths 返回站点证书/私钥的文件路径。
// 文件名固定（一张证书覆盖全部启用域名），故 nginx/caddy 的配置生成可以直接引用它。
func (m *Manager) CertPaths() (cert, key string) {
	return filepath.Join(m.certDir, "sites-cert.pem"), filepath.Join(m.certDir, "sites-key.pem")
}

// Status 返回站点功能整体状态。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	domains := m.enabledDomainsLocked()
	st := Status{
		CertReady:  m.cert != nil,
		CertError:  m.certErr,
		HostsError: m.hostsErr,
		HostsPath:  m.hostsPath,
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

// enabledDomainsLocked 当前需要证书 / hosts 覆盖的域名集合。
// 不含 localhost：本站点管理器只管用户建的那些站点，回环名本来就由系统 hosts 与 mkcert 根证书覆盖。
func (m *Manager) enabledDomainsLocked() []string {
	out := make([]string, 0, len(m.sites))
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

// validateDocRoot 清洗文档根：统一分隔符、去掉首尾斜杠，空串表示「就用项目根目录」。
//
// 拒绝绝对路径与任何形式的向上逃逸，因为它的值会被写进 nginx/Caddyfile 的 root 指令：
// 一个 "..\.." 就能把本地站点指向 C 盘，配置片段随后被人工放进主配置里长期生效。
// 这里刻意不校验目录是否真实存在——文档根可以先配好、再去建目录，
// 提前报错会让「先建站点后拉代码」这个常见顺序走不通。
func validateDocRoot(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	// 反斜杠在 Windows 上是分隔符，而在 nginx/Caddyfile 里是转义字符，统一成正斜杠
	p = strings.ReplaceAll(p, `\`, "/")
	if p == "" {
		return "", nil
	}
	// 绝对路径必须在去掉前导斜杠之前判：顺序反了的话 /srv/www 会被当成合法的相对路径
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.Contains(p, ":") {
		return "", fmt.Errorf("文档根必须是项目目录下的相对路径: %s", raw)
	}
	p = strings.Trim(p, "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("文档根不能包含 ..（不允许越过项目目录）: %s", raw)
		}
	}
	return path.Clean(p), nil
}

// siteSeq 与时间戳一起构成站点 ID 的进程内唯一性保证。
// 只用 time.Now().UnixNano() 不够：Windows 的时钟粒度下连续两次 Create 可能落在同一纳秒，
// ID 相撞会让后一个站点在 map 里覆盖前一个——静默丢站点，是最难查的那类 bug。
var siteSeq atomic.Uint64

// newSiteID 生成站点 ID：时间戳前缀保证跨重启近似有序，自增序号保证进程内唯一。
func newSiteID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), siteSeq.Add(1))
}

// Create 新增站点。域名唯一、目录必须存在。docRoot 是相对项目目录的文档根子目录（可为空）。
func (m *Manager) Create(name, domain, dir, docRoot string) (*Site, error) {
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
	docRoot, err = validateDocRoot(docRoot)
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
		DocRoot: docRoot,
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
func (m *Manager) Update(id, name, domain, dir string, enabled bool, docRoot string) (*Site, error) {
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
	docRoot, err = validateDocRoot(docRoot)
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
	site.Name, site.Domain, site.Dir, site.Enabled, site.DocRoot = name, d, dir, enabled, docRoot
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
	cp.Configs = cloneConfigs(site.Configs)
	m.mu.Unlock()
	return &cp, nil
}

// SetCustomConfig 保存（或清除）某后端的手工配置片段。content 去掉首尾空白后为空 = 清除，
// 之后该后端回到生成器的结果。
func (m *Manager) SetCustomConfig(id, backend, content string) (*Site, error) {
	be, err := ParseBackend(backend)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" {
		content = "" // 纯空白按清除处理，免得落盘一份肉眼看不见的空片段
	}

	m.mu.Lock()
	site, ok := m.sites[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("站点不存在")
	}
	prev := cloneConfigs(site.Configs)
	next := cloneConfigs(site.Configs)
	if next == nil {
		next = map[string]string{}
	}
	if content == "" {
		delete(next, string(be))
	} else {
		next[string(be)] = content
	}
	site.Configs = cloneConfigs(next) // 删空后归一成 nil，不落盘 "configs": {}
	if err := m.persistLocked(); err != nil {
		site.Configs = prev // 落盘失败回滚内存
		m.mu.Unlock()
		return nil, err
	}
	cp := *site
	cp.Configs = cloneConfigs(site.Configs)
	m.mu.Unlock()
	return &cp, nil
}

// 站点集合一变，两件事必须跟着走：证书覆盖的域名、hosts 里的解析条目。
//
// 刻意不判断内置服务是否在跑 —— 证书文件与 hosts 条目是 nginx/caddy 提供服务的必要条件，
// 而 nginx/caddy 完全不受内置服务开关影响。早先「没跑就跳过」会让
// 「先建站点、再启动 caddy」这条路拿不到证书、域名也解析不到，站点直接打不开。
//
// 失败不返回错误、也不打日志：调用方（Create/Update/Delete）已经改成功了，
// 把「证书没签出来」「hosts 没权限写」当作 CRUD 失败会让用户重试一个已经生效的操作。
// 失败原因一律记进 Status，由前端如实回显并提供重试入口 —— 那条路径已经说得很清楚，
// 这里再刷一行日志只是噪音（非管理员写 hosts 本就是常态，不是异常）。
//
// 这里传 escalate=false：增删改站点常常只是顺手调整（改个文档根、切个启用状态），
// 拿这种动作去弹 UAC 是打扰。需要提权的时机交给用户显式点击（面板上的「重新同步」），
// 那时 Status.HostsOK 为 false，按钮就在那里。
func (m *Manager) afterChange() {
	_, _ = m.ensureCert()
	_ = m.syncHosts(false)
}

// ensureCert 保证磁盘上的证书覆盖当前全部启用域名，域名集合变化时重新签发。
// 签发要跑外部 mkcert，必须在 certMu 下串行、且不持有 mu。
//
// 没有启用站点时直接清空证书状态：证书的唯一用途是被站点片段引用，
// 一个站点都没有时去签一张空证书毫无意义（mkcert 也需要至少一个 host）。
func (m *Manager) ensureCert() (*tls.Certificate, error) {
	m.certMu.Lock()
	defer m.certMu.Unlock()

	m.mu.Lock()
	domains := m.enabledDomainsLocked()
	fp := strings.Join(domains, ",")
	if len(domains) == 0 {
		m.cert, m.certSet, m.certErr = nil, "", ""
		m.mu.Unlock()
		return nil, nil
	}
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

// syncHosts 内部实现，escalate 决定权限不足时是否弹 UAC 重试。
//
// 提权那一路由子进程自己算 hosts 路径（hostsFilePath()），父进程不传路径过去 ——
// 少一个来自父进程的路径参数，就少一个「写任意文件」的入口。
func (m *Manager) syncHosts(escalate bool) error {
	m.mu.Lock()
	domains := m.enabledDomainsLocked()
	path := m.hostsPath
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

// Resume 应用启动时调用：为已启用的站点补一次证书与 hosts 解析（nginx/caddy 要用它们）。
//
// 这是启动时唯一要做的两件事 —— 本站点管理器不监听端口，也无所谓「拉起服务」：
// 服务器软件的开与关归「环境」页管，站点只负责把它需要的证书和解析准备好。
//
// 刻意不提权（escalateHosts=false）：应用一启动就弹 UAC 属于「用户没做任何动作却被要求授权」。
// hosts 同步失败不影响服务本身，用户在面板上点「重新同步」时会正常提权。
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
	if _, err := m.ensureCert(); err != nil {
		logger.W("[sites] 启动时签发证书失败: %v", err)
	}
	if err := m.syncHosts(false); err != nil {
		logger.W("[sites] 启动时同步 hosts 失败: %v", err)
	}
}
