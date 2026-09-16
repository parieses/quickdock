package sites

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Backend 站点服务后端：都是外部的服务器软件，QuickDock 只生成它们的配置片段。
type Backend string

const (
	BackendNginx Backend = "nginx"
	BackendCaddy Backend = "caddy"
)

// ParseBackend 校验后端取值。
func ParseBackend(s string) (Backend, error) {
	switch Backend(s) {
	case BackendNginx, BackendCaddy:
		return Backend(s), nil
	}
	return "", fmt.Errorf("未知的站点后端: %s", s)
}

// Module 配置模块：基础模板之外的常用配置块，按需勾选叠加。
// 同名模块在两个后端上语义一致——用户从 nginx 切到 caddy 不该得到不同的行为。
type Module string

const (
	// ModSPA 前端路由回退：找不到的路径交给 index.html（Vue/React 的 history 路由必需）。
	ModSPA Module = "spa"
	// ModStatic 纯静态：找不到就 404，绝不回退（与 ModSPA 互斥，SPA 优先）。
	ModStatic Module = "static"
	// ModGzip 文本类资源压缩。
	ModGzip Module = "gzip"
	// ModCache 静态资源长缓存（文件名带内容哈希的构建产物最受益）。
	ModCache Module = "cache"
	// ModCORS 跨域响应头（前后端分离、前端直连接口时用）。
	ModCORS Module = "cors"
	// ModSecurity 基础安全响应头。
	ModSecurity Module = "security"
	// ModHide 保护 .git / .env / 锁文件等不该被外部读到的路径。
	ModHide Module = "hide"
	// ModUpload 放开上传体积上限（默认 1m，带上传接口的应用常不够）。
	ModUpload Module = "upload"
	// ModProxy 全部请求反向代理到本地上游端口，并透传 WebSocket。
	ModProxy Module = "proxy"
)

// moduleOrder 模块的展示与渲染顺序（固定，保证生成的片段稳定、可 diff）。
var moduleOrder = []Module{
	ModSPA, ModStatic, ModGzip, ModCache, ModCORS, ModSecurity, ModHide, ModUpload, ModProxy,
}

// defaultProxyPort ModProxy 未指定上游端口时的默认值（前端 dev server 的常见端口）。
const defaultProxyPort = 3000

// AllModules 返回全部合法模块 id（固定顺序），供服务层校验与前端对齐。
func AllModules() []string {
	out := make([]string, 0, len(moduleOrder))
	for _, m := range moduleOrder {
		out = append(out, string(m))
	}
	return out
}

// NormalizeModules 过滤未知模块、去重、按固定顺序排列并消解互斥关系，返回字符串形式。
// 服务层与生成器共用同一处规则，避免「界面允许勾选、生成时却被悄悄丢掉」。
func NormalizeModules(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, m := range normalizeModules(raw) {
		out = append(out, string(m))
	}
	return out
}

func normalizeModules(raw []string) []Module {
	has := map[Module]bool{}
	for _, s := range raw {
		m := Module(strings.TrimSpace(s))
		if !isKnownModule(m) {
			continue // 未知 id 直接忽略：前端版本比后端新时不该炸掉整次生成
		}
		has[m] = true
	}
	// 互斥：反向代理接管全部请求，路由回退与静态资源本地缓存都不再有机会生效
	if has[ModProxy] {
		delete(has, ModSPA)
		delete(has, ModStatic)
		delete(has, ModCache)
	}
	// 互斥：SPA 回退与纯静态 404 只能留一个，SPA 优先
	if has[ModSPA] {
		delete(has, ModStatic)
	}
	out := make([]Module, 0, len(has))
	for _, m := range moduleOrder {
		if has[m] {
			out = append(out, m)
		}
	}
	return out
}

func isKnownModule(m Module) bool {
	for _, k := range moduleOrder {
		if k == m {
			return true
		}
	}
	return false
}

func hasModule(mods []Module, want Module) bool {
	for _, m := range mods {
		if m == want {
			return true
		}
	}
	return false
}

// GenInput 生成 nginx / caddy 配置所需的全部输入。
// 这里刻意不碰 env 包：调用方（服务层）负责把证书路径、PHP-FPM 地址、监听端口查出来传进来，
// 生成器只做纯文本拼装 —— 于是它可以被完整单测，不需要真装一个 nginx。
type GenInput struct {
	Site     Site
	Backend  Backend
	CertPath string // mkcert 签发的证书（-cert.pem）
	KeyPath  string // mkcert 签发的私钥（-key.pem）
	// 监听端口不可配：站点恒为 https 443 + http 80（301 跳转到 443）。
	// 本地开发站点只有「输域名就能开」这一种期望形态，给端口加开关只会让用户配出
	// 一个 https://x.test:8443 这种自己都记不住的地址，故不设该字段。
	// PHPFPMAddr 非空时生成 PHP 处理段（nginx fastcgi_pass / caddy php_fastcgi）。
	// 为空表示纯静态站点：调用方（服务层）用 SiteNeedsPHP 判断，装了 PHP 不等于每个站点都要跑 PHP。
	PHPFPMAddr string
	LogDir     string // 访问/错误日志目录；空则不写 log 指令
	// Modules 额外叠加的配置模块（见 Module 常量）。
	// 未知 id 忽略；互斥关系（proxy 接管一切、spa 优先于 static）由 normalizeModules 消解。
	Modules []string
	// ProxyPort ModProxy 的上游端口；<=0 或越界时退回 defaultProxyPort。
	ProxyPort int

	mods []Module // 归一化后的模块，由 Generate 填充，渲染函数只读它
}

// GenResult 生成结果：配置片段 + 建议落盘位置 + 用户需要手动加的一行 include。
type GenResult struct {
	Backend     string   `json:"backend"`
	Snippet     string   `json:"snippet"`
	FileName    string   `json:"fileName"`    // 建议文件名，如 myapp.test.conf
	IncludeLine string   `json:"includeLine"` // 需要用户在 nginx.conf / Caddyfile 里加一次的那行
	NeedsPHPFPM bool     `json:"needsPhpFpm"` // 是否生成了 PHP 处理段
	Modules     []string `json:"modules"`     // 实际生效的模块（互斥项已被剔除）
}

// confSafeRe 配置文件名的安全字符集：域名已过 validateDomain，这里只做落盘前的兜底。
var confSafeRe = regexp.MustCompile(`[^a-zA-Z0-9.-]`)

// Generate 按后端生成配置片段。
func Generate(in GenInput) (*GenResult, error) {
	if _, err := ParseBackend(string(in.Backend)); err != nil {
		return nil, err
	}
	if in.Site.Domain == "" {
		return nil, fmt.Errorf("站点域名不能为空")
	}
	if in.Site.Dir == "" {
		return nil, fmt.Errorf("站点目录不能为空")
	}
	in.mods = normalizeModules(in.Modules)
	if in.ProxyPort <= 0 || in.ProxyPort > 65535 {
		in.ProxyPort = defaultProxyPort
	}
	res := &GenResult{
		Backend: string(in.Backend),
		Modules: NormalizeModules(in.Modules),
	}
	// 反向代理接管全部请求，此时再挂 FastCGI 段没有意义（永不匹配），如实回显。
	res.NeedsPHPFPM = in.PHPFPMAddr != "" && !hasModule(in.mods, ModProxy)
	safe := confSafeRe.ReplaceAllString(in.Site.Domain, "-")
	switch in.Backend {
	case BackendNginx:
		res.FileName = safe + ".conf"
		res.IncludeLine = "include quickdock-sites/*.conf;"
		res.Snippet = nginxServerBlock(in, safe)
	case BackendCaddy:
		res.FileName = safe + ".caddy"
		res.IncludeLine = "import quickdock-sites/*.caddy"
		res.Snippet = caddySiteBlock(in, safe)
	default:
		return nil, fmt.Errorf("未知的站点后端: %s", in.Backend)
	}
	return res, nil
}

// nginxServerBlock 生成 nginx server 块。
// 固定两个 server：443(ssl，mkcert 证书) 是站点本体，80 只做 301 跳转 ——
// 符合本地开发站点「输域名就能开」的预期（浏览器默认走 http）。
// 端口不参与计算：站点对外恒为 443/80，用户不可配（见 GenInput 注释）。
func nginxServerBlock(in GenInput, name string) string {
	var b strings.Builder
	write := func(format string, args ...interface{}) {
		if len(args) == 0 {
			b.WriteString(format + "\n")
			return
		}
		fmt.Fprintf(&b, format+"\n", args...)
	}

	dir := toSlash(in.Site.EffectiveDir())
	proxy := hasModule(in.mods, ModProxy)

	write("# QuickDock site: %s (%s)", in.Site.Name, in.Site.Domain)
	write("# 由 QuickDock 生成；重新生成会覆盖本文件，请勿在此写自己的配置。")
	if in.Site.DocRoot != "" {
		write("# 文档根 = %s/%s", toSlash(in.Site.Dir), in.Site.DocRoot)
	}
	write("")
	write("server {")
	write("    listen      443 ssl;")
	write("    server_name %s;", in.Site.Domain)
	write("")
	write("    ssl_certificate     %s;", toSlash(in.CertPath))
	write("    ssl_certificate_key %s;", toSlash(in.KeyPath))
	write("")
	write("    root  %s;", dir)
	// index 里的 index.php 只对 PHP 站点写：nginx 对没有 FastCGI 段的 .php 请求会当静态文件
	// 原样返回（源码泄漏），纯静态站点把一个 index.php 摆在索引列表首位没有半点好处。
	if in.PHPFPMAddr != "" {
		write("    index index.php index.html index.htm;")
	} else {
		write("    index index.html index.htm;")
	}
	if in.LogDir != "" {
		write("")
		write("    access_log %s/%s.access.log;", toSlash(in.LogDir), name)
		write("    error_log  %s/%s.error.log;", toSlash(in.LogDir), name)
	}

	// ---- 模块：server 级指令 ----
	if hasModule(in.mods, ModUpload) {
		write("")
		write("    # 模块：上传上限（nginx 默认 1m，带上传接口的应用常不够）")
		write("    client_max_body_size 64m;")
	}
	if hasModule(in.mods, ModGzip) {
		write("")
		write("    # 模块：文本资源压缩")
		write("    gzip             on;")
		write("    gzip_vary        on;")
		write("    gzip_min_length  1024;")
		write("    gzip_comp_level  5;")
		write("    gzip_proxied     any;")
		write("    gzip_types       text/plain text/css text/xml application/json application/javascript application/xml+rss image/svg+xml;")
	}
	if hasModule(in.mods, ModCORS) {
		write("")
		write("    # 模块：跨域（开发期前后端分离时放开）")
		write("    add_header Access-Control-Allow-Origin  \"*\" always;")
		write("    add_header Access-Control-Allow-Methods \"GET, POST, PUT, PATCH, DELETE, OPTIONS\" always;")
		write("    add_header Access-Control-Allow-Headers \"Content-Type, Authorization, X-Requested-With\" always;")
		write("    if ($request_method = OPTIONS) {")
		write("        return 204;")
		write("    }")
	}
	if hasModule(in.mods, ModSecurity) {
		write("")
		write("    # 模块：基础安全响应头")
		write("    add_header X-Content-Type-Options      nosniff always;")
		write("    add_header X-Frame-Options             SAMEORIGIN always;")
		write("    add_header Referrer-Policy             strict-origin-when-cross-origin always;")
		write("    add_header Strict-Transport-Security   \"max-age=31536000\" always;")
	}
	if hasModule(in.mods, ModHide) {
		write("")
		write("    # 模块：保护敏感文件（.git / .env / 各类锁文件）")
		write("    location ~* \\.(git|svn|hg|env|ds_store)(/|$) {")
		write("        deny all;")
		write("    }")
		write("    location ~* (composer\\.(json|lock)|package-lock\\.json|yarn\\.lock|pnpm-lock\\.yaml)$ {")
		write("        deny all;")
		write("    }")
	}

	// ---- 根 location：代理 / 回退策略 ----
	write("")
	if proxy {
		write("    # 模块：反向代理（透传 WebSocket，适配前端 HMR）")
		write("    location / {")
		write("        proxy_pass         http://127.0.0.1:%d;", in.ProxyPort)
		write("        proxy_http_version 1.1;")
		write("        proxy_set_header   Host              $host;")
		write("        proxy_set_header   X-Real-IP         $remote_addr;")
		write("        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;")
		write("        proxy_set_header   X-Forwarded-Proto $scheme;")
		write("        proxy_set_header   Upgrade           $http_upgrade;")
		write("        proxy_set_header   Connection        \"upgrade\";")
		write("        proxy_read_timeout 3600s;")
		write("        proxy_buffering    off;")
		write("    }")
	} else {
		write("    location / {")
		write("        try_files %s;", indexTryFiles(in.mods, in.PHPFPMAddr != ""))
		write("    }")
		if in.PHPFPMAddr != "" {
			write("")
			write("    # PHP-FPM：QuickDock 以 php-cgi 的 FastCGI 模式拉起，端口固定")
			write("    location ~ \\.php$ {")
			write("        fastcgi_pass   %s;", in.PHPFPMAddr)
			write("        fastcgi_index  index.php;")
			write("        include        fastcgi_params;")
			write("        fastcgi_param  SCRIPT_FILENAME $document_root$fastcgi_script_name;")
			write("    }")
		}
		if hasModule(in.mods, ModCache) {
			write("")
			write("    # 模块：静态资源长缓存（文件名带内容哈希的构建产物最受益）")
			write("    location ~* \\.(?:js|mjs|css|png|jpe?g|gif|svg|webp|avif|ico|woff2?|ttf|eot|mp4|webm)$ {")
			write("        expires    30d;")
			write("        add_header Cache-Control \"public, max-age=2592000, immutable\";")
			write("        access_log off;")
			write("        try_files  $uri =404;")
			write("    }")
		}
	}
	write("}")
	write("")
	write("# http 固定 301 跳 https：浏览器敲域名默认走 80，没有这段就是「连接被拒绝」")
	write("server {")
	write("    listen      80;")
	write("    server_name %s;", in.Site.Domain)
	write("    return 301 https://$host$request_uri;")
	write("}")
	return b.String()
}

// indexTryFiles 根 location 的回退策略。
// 必须区分「这个站点到底跑不跑 PHP」：默认回退到 /index.php 只对 PHP 框架成立，
// 纯静态站点（前端构建产物、纯 HTML 目录）回退过去只会撞上一个不存在的文件 ——
// 那正是「静态站点配好了、页面却打不开」的来源。
func indexTryFiles(mods []Module, php bool) string {
	switch {
	case hasModule(mods, ModSPA):
		return "$uri $uri/ /index.html"
	case hasModule(mods, ModStatic):
		return "$uri $uri/ =404"
	case php:
		return "$uri $uri/ /index.php?$query_string"
	default:
		// 纯静态且没提要求：与 caddy 侧的默认回退（/index.html）保持一致
		return "$uri $uri/ /index.html"
	}
}

// caddySiteBlock 生成 Caddyfile 站点块。
// 与 nginx 侧同语义：站点本体绑 https（443），另给一个 http 块做 301 跳转。
// Caddy 一个块只能绑一个地址（不像 nginx 能在同一 server 里写两条 listen），
// 所以跳转必须单独成块 —— 但 Caddy 对定义了 tls 的站点本会自动做 80→443 跳转，
// 这里显式写出来的价值是：把跳转目标钉死在用户可见的域名上，而不是依赖自动重定向。
// Caddy 自带自动 HTTPS，但本地域名无法申请公网证书，故仍显式指定 mkcert 的证书文件。
func caddySiteBlock(in GenInput, name string) string {
	var b strings.Builder
	write := func(format string, args ...interface{}) {
		if len(args) == 0 {
			b.WriteString(format + "\n")
			return
		}
		fmt.Fprintf(&b, format+"\n", args...)
	}

	domain := in.Site.Domain
	proxy := hasModule(in.mods, ModProxy)

	write("# QuickDock site: %s (%s)", in.Site.Name, in.Site.Domain)
	write("# 由 QuickDock 生成；重新生成会覆盖本文件，请勿在此写自己的配置。")
	if in.Site.DocRoot != "" {
		write("# 文档根 = %s/%s", toSlash(in.Site.Dir), in.Site.DocRoot)
	}
	write("%s {", domain)
	write("    tls %s %s", toSlash(in.CertPath), toSlash(in.KeyPath))

	// ---- 模块：server 级指令 ----
	if hasModule(in.mods, ModGzip) {
		write("")
		write("    # 模块：文本资源压缩")
		write("    encode gzip zstd")
	}
	if hasModule(in.mods, ModUpload) {
		write("")
		write("    # 模块：上传上限")
		write("    request_body {")
		write("        max_size 64MB")
		write("    }")
	}
	if hasModule(in.mods, ModCORS) {
		write("")
		write("    # 模块：跨域")
		write("    @qdcors method OPTIONS")
		write("    header Access-Control-Allow-Origin  \"*\"")
		write("    header Access-Control-Allow-Methods \"GET, POST, PUT, PATCH, DELETE, OPTIONS\"")
		write("    header Access-Control-Allow-Headers \"Content-Type, Authorization, X-Requested-With\"")
		write("    respond @qdcors 204")
	}
	if hasModule(in.mods, ModSecurity) {
		write("")
		write("    # 模块：基础安全响应头")
		write("    header {")
		write("        X-Content-Type-Options    nosniff")
		write("        X-Frame-Options           SAMEORIGIN")
		write("        Referrer-Policy           strict-origin-when-cross-origin")
		write("        Strict-Transport-Security \"max-age=31536000\"")
		write("    }")
	}
	if hasModule(in.mods, ModHide) {
		write("")
		write("    # 模块：保护敏感文件（Caddy 用 Go 正则，不支持负向断言，故按扩展名枚举）")
		write("    @qdhidden path_regexp (?i)\\.(git|svn|hg|env|ds_store)(/|$)")
		write("    respond @qdhidden \"403 Forbidden\" 403")
	}

	// ---- 站点主体：代理 或 静态文件服务 ----
	write("")
	if proxy {
		write("    # 模块：反向代理（Caddy 的 reverse_proxy 原生支持 WebSocket）")
		write("    reverse_proxy 127.0.0.1:%d", in.ProxyPort)
	} else {
		write("    root * %s", toSlash(in.Site.EffectiveDir()))
		if in.PHPFPMAddr != "" {
			write("    php_fastcgi %s", in.PHPFPMAddr)
			write("    file_server")
		} else {
			// 静态站点：默认回退到 index.html，与 nginx 侧同一套语义；
			// 勾了纯静态模块则去掉回退，找不到就 404。
			if hasModule(in.mods, ModStatic) {
				write("    try_files {path} {path}/")
			} else {
				write("    try_files {path} {path}/ /index.html")
			}
			write("    file_server")
		}
		if hasModule(in.mods, ModCache) {
			write("")
			write("    # 模块：静态资源长缓存")
			write("    @qdstatic path *.js *.mjs *.css *.png *.jpg *.jpeg *.gif *.svg *.webp *.avif *.ico *.woff *.woff2 *.ttf *.eot *.mp4 *.webm")
			write("    header @qdstatic Cache-Control \"public, max-age=2592000, immutable\"")
		}
	}
	if in.LogDir != "" {
		write("")
		write("    log {")
		write("        output file %s/%s.access.log", toSlash(in.LogDir), name)
		write("    }")
	}
	write("}")
	write("")
	// 这段必须自己写，不能指望 Caddy 的自动跳转：
	// Caddy 的 Automatic HTTPS 在「手动加载证书」（即站点本体那条 tls <cert> <key>）时
	// 不会激活（官方文档 Activation 一节的明确条件），而 80→443 的自动跳转正是
	// Automatic HTTPS 的产物 —— 不显式写，浏览器敲域名就是连接被拒绝。
	// 地址必须以 http:// 开头：前缀 http:// 同样阻止自动 HTTPS，于是这个块只监听 80、
	// 只做跳转，不会和本体块抢 443（Caddy 一个块只能绑一个地址，故必须单独成块）。
	write("# http 固定 301 跳 https（Caddy 单块只能绑一个地址，且手动加载证书不会自动跳转）")
	write("http://%s {", domain)
	write("    redir https://{host}{uri} 301")
	write("}")
	return b.String()
}

// SuggestedConfDir 返回片段建议落盘目录：配置文件同级下的 quickdock-sites/。
// 单独放子目录是为了 include/import 一行就能覆盖全部站点，且不与用户自己的配置混在一起。
func SuggestedConfDir(runtimeConfigPath string) string {
	if runtimeConfigPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(runtimeConfigPath), "quickdock-sites")
}

// toSlash 把路径统一成正斜杠：Windows 的反斜杠在 nginx/Caddyfile 里是转义字符，会被当语法。
func toSlash(p string) string {
	return strings.ReplaceAll(p, `\`, "/")
}
