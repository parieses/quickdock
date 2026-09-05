// Package env 管理本地开发环境的部署与版本切换（参考 FlyEnv）。
// 首期覆盖 Node/PHP/Go/Redis/Nginx：纯用户态、不写注册表、不申请管理员权限；
// 下载源可切换（官方 / 镜像 / 自定义），并支持拉取上游全量版本列表（不止硬编码推荐）。
package env

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
)

// Runtime 受管运行时类型
type Runtime string

const (
	RuntimeNode  Runtime = "node"
	RuntimePHP   Runtime = "php"
	RuntimeGo    Runtime = "go"
	RuntimeRedis Runtime = "redis"
	RuntimeNginx Runtime = "nginx"
	RuntimeGit   Runtime = "git"
	RuntimeCaddy    Runtime = "caddy"
	RuntimeComposer Runtime = "composer"

	// 第二批扩展运行时（2026-09-04）：语言/工具/Web服务器/缓存/数据库
	RuntimeFFmpeg     Runtime = "ffmpeg"
	RuntimePython     Runtime = "python"
	RuntimeApache     Runtime = "apache"
	RuntimeMemcached  Runtime = "memcached"
	RuntimeMariaDB    Runtime = "mariadb"
	RuntimeMySQL      Runtime = "mysql"
	RuntimePostgreSQL Runtime = "postgresql"
	RuntimeMongoDB    Runtime = "mongodb"
	RuntimeMailpit    Runtime = "mailpit"
	RuntimeMinIO      Runtime = "minio"
	RuntimeFrpc       Runtime = "frpc"
	RuntimeFTP        Runtime = "ftp"

	// 第三批扩展运行时（2026-09-05）：GitHub CLI / Bun / Traefik / mkcert / RabbitMQ
	RuntimeGh       Runtime = "gh"
	RuntimeBun      Runtime = "bun"
	RuntimeTraefik  Runtime = "traefik"
	RuntimeMkcert   Runtime = "mkcert"
	RuntimeRabbitMQ Runtime = "rabbitmq"

	// Erlang：底层语言运行时（被 RabbitMQ 等依赖），作为「语言」分组里的独立可管理运行时。
	RuntimeErlang Runtime = "erlang"
)

// 环境管理分组（侧边栏按组归类：语言 / Web 服务器 / 缓存与存储 / 工具 / 数据库）
// 与前端 EnvironmentPage.vue 的 GROUP_ORDER / GROUP_LABEL 保持一致。
// 后端 List() 会把这些值回填到 RuntimeInfo.Group，前端合并时以后端为准，避免两侧分组发散。
const (
	GroupLanguage  = "language"
	GroupWebServer = "webserver"
	GroupStorage   = "storage"
	GroupTool      = "tool"
	GroupDatabase  = "database"
)

// Source 一个可切换的下载源
type Source struct {
	ID   string // 唯一标识，如 "npmmirror" / "official" / "custom"
	Name string // 展示名
	// Build 根据版本/平台构造下载地址；该平台不支持时返回空串（如 PHP 仅在 Windows 提供官方包）
	Build func(version, goos, arch string) string
}

type runtimeDef struct {
	display   string   // 展示名，如 "Node.js"
	group     string   // 分组：GroupLanguage / GroupWebServer / GroupCache / GroupTool
	versions  []string // 推荐可下载版本清单（拉取失败时的兜底）
	sources   []Source
	versURL   string // 上游全量版本列表地址（空=只用推荐列表）
	versParse func(body []byte) []string // 解析版本列表（返回形如 "1.25.0" / "v22.22.2" 的版本号）
	// versHTMLFallbackParse 当 API 失败时的 HTML 解析兜底；fallbackHTMLURL 返回对应 HTML 页面地址
	versHTMLFallbackParse func(body []byte) []string
	// fallbackHTMLURL 该运行时 GitHub Releases 页面的 HTML 地址（API 不可用时兜底解析版本）。
	// 大多数 GitHub Releases 可共用默认 HTML tag 解析器，故直接作为自描述字段，新增运行时无需改中心 switch。
	fallbackHTMLURL string
}

var (
	regMu    sync.RWMutex
	registry = map[Runtime]runtimeDef{
		RuntimeNode: {display: "Node.js", group: GroupLanguage, versions: []string{"v22.22.2", "v20.19.0", "v18.20.4"}, versURL: "https://nodejs.org/dist/index.json", versParse: parseNodeVersions, sources: []Source{
			{ID: "npmmirror", Name: "npmmirror 镜像", Build: nodeURL("https://registry.npmmirror.com/-/binary/node/{v}/node-{v}-{os}-{arch}.{ext}")},
			{ID: "official", Name: "Node.js 官方", Build: nodeURL("https://nodejs.org/dist/{v}/node-{v}-{os}-{arch}.{ext}")},
		}},
		RuntimeGo: {display: "Go", group: GroupLanguage, versions: []string{"1.23.4", "1.22.10", "1.21.13"}, versURL: "https://go.dev/dl/", versParse: parseGoVersionsFromHTML, sources: []Source{
			{ID: "official", Name: "Go 官方", Build: goURL("https://go.dev/dl/go{version}.{os}-{arch}.{ext}")},
			{ID: "golangcn", Name: "golang.google.cn (国内)", Build: goURL("https://golang.google.cn/dl/go{version}.{os}-{arch}.{ext}")},
		}},
		RuntimePHP: {display: "PHP", group: GroupLanguage, versions: []string{"8.3.20", "8.2.27", "8.1.31"}, versURL: "https://downloads.php.net/~windows/releases/archives/", versParse: parsePHPWinVersions, sources: []Source{
			{ID: "windowsphpnet-archive-vs17", Name: "windows.php.net (archives VS17)", Build: phpURL("https://downloads.php.net/~windows/releases/archives/php-{version}-Win32-vs17-x64.zip")},
			{ID: "windowsphpnet-archive", Name: "windows.php.net (archives VS16)", Build: phpURL("https://downloads.php.net/~windows/releases/archives/php-{version}-Win32-vs16-x64.zip")},
			{ID: "windowsphpnet", Name: "windows.php.net (releases VS17)", Build: phpURL("https://windows.php.net/downloads/releases/php-{version}-Win32-vs17-x64.zip")},
			{ID: "windowsphpnet-rel", Name: "windows.php.net (releases VS16)", Build: phpURL("https://windows.php.net/downloads/releases/php-{version}-Win32-vs16-x64.zip")},
		}},
		RuntimeRedis: {display: "Redis", group: GroupStorage, versions: []string{"7.4.0", "7.2.5", "7.0.15"}, versURL: "https://api.github.com/repos/redis-windows/redis-windows/releases?per_page=100", versParse: parseRedisVersions, versHTMLFallbackParse: parseRedisVersionsHTML, fallbackHTMLURL: "https://github.com/redis-windows/redis-windows/releases", sources: []Source{
			{ID: "rediswindows", Name: "redis-windows/redis-windows (GitHub)", Build: redisURL("https://github.com/redis-windows/redis-windows/releases/download/{version}/Redis-{version}-Windows-x64-msys2.zip")},
		}},
		RuntimeNginx: {display: "Nginx", group: GroupWebServer, versions: []string{"1.27.5", "1.26.3", "1.25.5"}, versURL: "https://nginx.org/download/", versParse: parseNginxVersions, sources: []Source{
			{ID: "nginxorg", Name: "nginx.org 官方", Build: nginxURL("https://nginx.org/download/nginx-{version}.zip")},
		}},
		RuntimeGit: {display: "Git", group: GroupTool, versions: []string{"2.45.0", "2.44.0", "2.43.0"}, versURL: "https://api.github.com/repos/git-for-windows/git/releases?per_page=100", versParse: parseGitVersions, versHTMLFallbackParse: parseGitVersionsHTML, fallbackHTMLURL: "https://github.com/git-for-windows/git/releases", sources: []Source{
			{ID: "gfw", Name: "git-for-windows (GitHub)", Build: gitURL("https://github.com/git-for-windows/git/releases/download/v{version}.windows.1/MinGit-{version}.windows.1-64-bit.zip")},
		}},
		RuntimeCaddy: {display: "Caddy", group: GroupWebServer, versions: []string{"2.8.4", "2.7.6", "2.6.4"}, versURL: "https://api.github.com/repos/caddyserver/caddy/releases?per_page=100", versParse: parseCaddyVersions, versHTMLFallbackParse: parseCaddyVersionsHTML, fallbackHTMLURL: "https://github.com/caddyserver/caddy/releases", sources: []Source{
			{ID: "caddyserver", Name: "caddyserver/caddy (GitHub)", Build: caddyURL("https://github.com/caddyserver/caddy/releases/download/v{version}/caddy_{version}_windows_amd64.zip")},
		}},
		RuntimeComposer: {display: "Composer", group: GroupTool, versions: []string{"2.7.7", "2.6.6", "2.5.8"}, versURL: "https://getcomposer.org/versions", versParse: parseComposerVersions, sources: []Source{
			{ID: "getcomposer", Name: "getcomposer.org 官方", Build: composerURL("https://getcomposer.org/download/{version}/composer.phar")},
		}},
		RuntimeFFmpeg: {display: "FFmpeg", group: GroupTool, versions: []string{"7.1", "6.1", "5.1"}, sources: []Source{
			{ID: "gyandev", Name: "gyan.dev 官方构建", Build: ffmpegURL("https://www.gyan.dev/ffmpeg/builds/ffmpeg-{version}-essentials_build.zip")},
		}},
		RuntimePython: {display: "Python", group: GroupLanguage, versions: []string{"3.12.7", "3.11.9", "3.10.14"}, versURL: "https://www.python.org/ftp/python/", versParse: parsePythonVersions, sources: []Source{
			{ID: "pythonorg", Name: "python.org 官方", Build: pythonURL("https://www.python.org/ftp/python/{version}/python-{version}-embed-amd64.zip")},
		}},
		// ⚠️ Apache Lounge 文件名含构建日期且 VS 版本会升级（httpd-2.4.66-251206-Win64-VS17.zip / httpd-2.4.68-260827-Win64-VS18.zip），
		// 构建日期无法由版本号推导，静态模板必然 404（且返回 HTTP 200 的 HTML 错误页，伪装成有效下载）。
		// URL 与版本列表均由 apacheFetchIndex 抓取官方目录页动态解析（VS17/VS18 两页，正则大小写不敏感），
		// 见下方 apacheURL / apacheVersParse。当前 2.4.66 在 VS17、2.4.68 在 VS18，均已实测可解析为有效 zip。
		RuntimeApache: {display: "Apache", group: GroupWebServer, versions: []string{"2.4.68", "2.4.66"}, versURL: apacheLoungeBase + "/download/VS17/", versParse: apacheVersParse, sources: []Source{
			{ID: "apachelounge", Name: "Apache Lounge (官方)", Build: apacheURL()},
		}},
		// Memcached：nono303 已停止发布 GitHub Releases，改用 adamyg/memcached-win32（含
		// package-vc2022-x64.zip，内部可执行文件为 memcached_service.exe，需 -d run 以控制台模式运行）。
		RuntimeMemcached: {display: "Memcached", group: GroupStorage, versions: []string{"1.6.34.11"}, versURL: "https://api.github.com/repos/adamyg/memcached-win32/releases?per_page=100", versParse: parseGitVersions, sources: []Source{
			{ID: "adamyg", Name: "adamyg/memcached-win32 (GitHub)", Build: memcachedURL("https://github.com/adamyg/memcached-win32/releases/download/{version}/package-vc2022-x64.zip")},
		}},
		RuntimeMariaDB: {display: "MariaDB", group: GroupDatabase, versions: []string{"11.6.2", "11.5.2", "10.11.10"}, sources: []Source{
			{ID: "mariadborg", Name: "archive.mariadb.org 官方", Build: mariadbURL("https://archive.mariadb.org/mariadb-{version}/winx64-packages/mariadb-{version}-winx64.zip")},
		}},
	RuntimeMySQL: {display: "MySQL", group: GroupDatabase, versions: []string{"8.4.3", "8.0.40", "5.7.44"}, sources: []Source{
		// downloads.mysql.com/archives/get/p/23/file/... 是归档页下载中转，直接拉会被 403（需 cookie）。
		// 改用 CDN 直链 cdn.mysql.com/archives/mysql-{mm}/，8.x/5.7 均 200（已验证）。
		{ID: "mysqlarchive", Name: "MySQL 官方归档", Build: mysqlURL("https://cdn.mysql.com/archives/mysql-{mm}/mysql-{version}-winx64.zip")},
	}},
		RuntimePostgreSQL: {display: "PostgreSQL", group: GroupDatabase, versions: []string{"16.4", "15.6", "14.13"}, sources: []Source{
			// EDB 二进制包文件名含 build 号（固定 -1），缺则 404。
			{ID: "edb", Name: "EnterpriseDB 二进制包", Build: postgresURL("https://get.enterprisedb.com/postgresql/postgresql-{version}-1-windows-x64-binaries.zip")},
		}},
		RuntimeMongoDB: {display: "MongoDB", group: GroupDatabase, versions: []string{"7.0.14", "6.0.16", "5.0.30"}, versURL: "https://api.github.com/repos/mongodb/mongo/releases?per_page=100", versParse: parseMongoVersions, sources: []Source{
			{ID: "mongodb", Name: "fastdl.mongodb.org 官方", Build: mongoURL("https://fastdl.mongodb.org/windows/mongodb-windows-x86_64-{version}.zip")},
		}},
		// Mailpit：本地 SMTP + Web 收件箱（SMTP 1025 / Web UI 8025）。GitHub releases 单文件 zip。
		RuntimeMailpit: {display: "Mailpit", group: GroupTool, versions: []string{"1.31.0", "1.30.7", "1.30.6"}, versURL: "https://api.github.com/repos/axllent/mailpit/releases?per_page=100", versParse: parseMailpitVersions, sources: []Source{
			{ID: "mailpit", Name: "axllent/mailpit (GitHub)", Build: ffmpegURL("https://github.com/axllent/mailpit/releases/download/v{version}/mailpit-windows-amd64.zip")},
		}},
		// MinIO：S3 兼容对象存储（API 9000 / Console 9001）。滚动发布（RELEASE 日期版本），单文件 minio.exe。
		RuntimeMinIO: {display: "MinIO", group: GroupStorage, versions: []string{"latest"}, sources: []Source{
			{ID: "minio", Name: "dl.min.io 官方", Build: minioURL("https://dl.min.io/server/minio/release/windows-amd64/minio.exe")},
		}},
		// frpc：frp 内网穿透客户端（fatedier/frp）。本地以 `frpc -c frpc.toml` 运行，
		// 连接远端 frps 服务器；本身无固定监听端口（出站连接），故作为「工具型」运行时：
		// 可安装 + 可编辑 frpc.toml（通用 ConfigProvider），不接入 ServiceController 启停。
		RuntimeFrpc: {display: "frpc", group: GroupTool, versions: []string{"0.71.0", "0.70.1", "0.69.0"}, versURL: "https://api.github.com/repos/fatedier/frp/releases?per_page=100", versParse: parseFrpcVersions, versHTMLFallbackParse: parseFrpcVersionsHTML, fallbackHTMLURL: "https://github.com/fatedier/frp/releases", sources: []Source{
			{ID: "fatedier", Name: "fatedier/frp (GitHub)", Build: frpcURL("https://github.com/fatedier/frp/releases/download/v{version}/frp_{version}_windows_amd64.zip")},
		}},
	// FTP：轻量开源控制台 FTP 服务器（FTPDMIN，Matthias Wandel，public domain）。
	// 单文件 ftpdmin.exe（~65KB），无安装、无配置文件、匿名登录（设计如此），适合临时文件传输。
	// 作为服务型运行时：端口 21、支持 -p 指定端口、-g 只读、位置参数指定根目录（即本版本 data 目录）。
	// 下载源为作者个人站点（单文件 exe，非 zip），Install 直下 exe 到 ExeFor 不调用 Extract。
	RuntimeFTP: {display: "FTP", group: GroupWebServer, versions: []string{"0.96"}, sources: []Source{
		{ID: "ftpdmin", Name: "FTPDMIN (Sentex)", Build: ftpURL("https://www.sentex.net/~mwandel/ftpdmin/ftpdmin.exe")},
	}},
	// GitHub CLI (gh)：官方命令行工具，单文件 zip 内 gh.exe。无服务、无配置、无导入（DetectArgs 返回空 → 不可导入系统 exe）。
	RuntimeGh: {display: "GitHub CLI", group: GroupTool, versions: []string{"2.100.0", "2.99.0", "2.98.0"}, versURL: "https://api.github.com/repos/cli/cli/releases?per_page=100", versParse: parseGhVersions, fallbackHTMLURL: "https://github.com/cli/cli/releases", sources: []Source{
		{ID: "cli", Name: "cli/cli (GitHub)", Build: ghURL("https://github.com/cli/cli/releases/download/v{version}/gh_{version}_windows_amd64.zip")},
	}},
	// Bun：新兴 JS/TS 运行时（oven-sh/bun）。多版本并存，bun-windows-x64.zip 内含 bun.exe。无服务。
	RuntimeBun: {display: "Bun", group: GroupLanguage, versions: []string{"1.4.1", "1.3.0", "1.2.0"}, versURL: "https://api.github.com/repos/oven-sh/bun/releases?per_page=100", versParse: parseBunVersions, fallbackHTMLURL: "https://github.com/oven-sh/bun/releases", sources: []Source{
		{ID: "oven", Name: "oven-sh/bun (GitHub)", Build: bunURL("https://github.com/oven-sh/bun/releases/download/bun-v{version}/bun-windows-x64.zip")},
	}},
	// Traefik：Go 编写的边缘路由器，单文件 traefik.exe。服务型（serve），支持配置校验（validate --configFile）。
	RuntimeTraefik: {display: "Traefik", group: GroupWebServer, versions: []string{"3.7.13", "3.6.4", "3.5.2"}, versURL: "https://api.github.com/repos/traefik/traefik/releases?per_page=100", versParse: parseTraefikVersions, fallbackHTMLURL: "https://github.com/traefik/traefik/releases", sources: []Source{
		{ID: "traefik", Name: "traefik/traefik (GitHub)", Build: traefikURL("https://github.com/traefik/traefik/releases/download/v{version}/traefik_v{version}_windows_amd64.zip")},
	}},
	// mkcert：本地 HTTPS 自签名证书工具（FiloSottile/mkcert），单文件 exe（非 zip）。无服务、无配置。
	RuntimeMkcert: {display: "mkcert", group: GroupTool, versions: []string{"1.4.4", "1.4.3"}, versURL: "https://api.github.com/repos/FiloSottile/mkcert/releases?per_page=100", versParse: parseMkcertVersions, fallbackHTMLURL: "https://github.com/FiloSottile/mkcert/releases", sources: []Source{
		{ID: "mkcert", Name: "FiloSottile/mkcert (GitHub)", Build: mkcertURL("https://github.com/FiloSottile/mkcert/releases/download/v{version}/mkcert-v{version}-windows-amd64.exe")},
	}},
	// RabbitMQ：Erlang 消息代理（rabbitmq-server-windows 自带 Erlang 运行时，无需单独安装）。服务型（sbin/rabbitmq-server.bat）。
	RuntimeRabbitMQ: {display: "RabbitMQ", group: GroupStorage, versions: []string{"4.3.5", "4.2.0", "3.13.7"}, versURL: "https://api.github.com/repos/rabbitmq/rabbitmq-server/releases?per_page=100", versParse: parseRabbitVersions, fallbackHTMLURL: "https://github.com/rabbitmq/rabbitmq-server/releases", sources: []Source{
		{ID: "rabbitmq", Name: "rabbitmq/rabbitmq-server (GitHub)", Build: rabbitURL("https://github.com/rabbitmq/rabbitmq-server/releases/download/v{version}/rabbitmq-server-windows-{version}.zip")},
	}},
	// Erlang：底层语言运行时，多版本并存（erlang/otp 官方 GitHub 发布的 Windows 便携 zip）。
	// 被 RabbitMQ 安装/启动时复用：RabbitMQ 优先复用本运行时已安装的 Erlang（或系统 ERLANG_HOME/PATH）。
	RuntimeErlang: {display: "Erlang", group: GroupLanguage, versions: []string{"27.3.4", "26.2.5", "25.3.2.9"}, versURL: "https://api.github.com/repos/erlang/otp/releases?per_page=100", versParse: parseErlangVersions, fallbackHTMLURL: "https://github.com/erlang/otp/releases", sources: []Source{
		{ID: "erlang", Name: "erlang/otp (GitHub)", Build: erlangURL("https://github.com/erlang/otp/releases/download/OTP-{version}/otp_win64_{version}.zip")},
	}},
}

	activeMu     sync.RWMutex
	activeSource = map[Runtime]string{} // 用户选定的活跃源（优先尝试）

	// customTemplate 用户自定义源模板，支持 {version}/{v}/{os}/{arch} 占位符。
	// 网络不佳时用户可粘贴自己的镜像地址，作为最后兜底。
	customTemplate = map[Runtime]string{}
)

// ---- 各运行时 URL 构造器 ----

func nodeURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" && goos != "linux" && goos != "darwin" {
			return ""
		}
		os := mapNodeOS(goos)
		a := mapNodeArch(arch)
		ext := "tar.gz"
		if goos == "windows" {
			ext = "zip"
		}
		return strings.NewReplacer(
			"{v}", version, "{os}", os, "{arch}", a, "{ext}", ext,
		).Replace(tmpl)
	}
}

func goURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" && goos != "linux" && goos != "darwin" {
			return ""
		}
		ext := "tar.gz"
		if goos == "windows" {
			ext = "zip"
		}
		return strings.NewReplacer(
			"{version}", version, "{os}", goos, "{arch}", arch, "{ext}", ext,
		).Replace(tmpl)
	}
}

func phpURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

func redisURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

func nginxURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// gitURL 构造 git-for-windows 的 MinGit 便携包地址。版本号内部存 "2.45.0"，
// 真实发布标签为 v2.45.0.windows.1，故在模板外再补 .windows.1 后缀。
func gitURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// caddyURL 构造 caddyserver/caddy 的 Windows 发行 zip 地址（单文件 caddy.exe）。
// Caddy 跨平台均有构建，但本环境管理仅在 Windows 桌面提供，故非 Windows 返回空。
func caddyURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// composerURL 构造 composer.phar 地址（跨平台单文件，依赖 PHP 运行）。
func composerURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// ffmpegURL / pythonURL / apacheURL / memcachedURL / mariadbURL / postgresURL / mongoURL
// 均为 Windows 桌面发行包（便携 zip），非 Windows 返回空。
func ffmpegURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

func pythonURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// ---- Apache Lounge 动态索引 ----
//
// Apache Lounge 的下载文件名含构建日期，且 VS 运行时版本会升级：
//
//	/download/VS17/binaries/httpd-2.4.66-251206-Win64-VS17.zip
//	/download/VS18/binaries/httpd-2.4.68-260827-Win64-VS18.zip
//	                                     ^^^^^^ 构建日期，不可由版本号推导
//
// 因此无法再用 "版本 → URL" 的静态模板拼装。更糟的是该站对不存在的路径返回
// **HTTP 200 + text/html 的 404 页面**，下载器会以为成功、把 HTML 存成 .zip，
// 最终在解压阶段才报 "zip: not a valid zip file"，掩盖了真实原因。
// 改为抓取官方目录页，正则提取 Win64 包真实链接，建立 version → URL 映射。

const apacheLoungeBase = "https://www.apachelounge.com"

// apacheDirPages 需要扫描的下载目录页。VS18 在前，靠前的 VS 版本通常更新。
var apacheDirPages = []string{"/download/VS18/", "/download/VS17/"}

// apacheLinkRe 提取 Win64 包的链接与版本号；win32 包与 .zip.asc/.zip.txt 校验文件均不匹配。
var apacheLinkRe = regexp.MustCompile(`(?i)href="(/download/vs\d+/binaries/httpd-(\d+\.\d+\.\d+)-\d+-win64-vs\d+\.zip)"`)

var (
	apacheIdxMu   sync.Mutex
	apacheIdx     = map[string]string{} // version → 绝对下载 URL
	apacheIdxTime time.Time
	apacheIdxTTL  = 1 * time.Hour
)

// apacheFetchIndex 抓取目录页构建 version → URL 映射（带 TTL 缓存）。
// force=true 时忽略缓存强制刷新。全部页面拉取失败时返回上一次的成功结果兜底（可能为空）。
func apacheFetchIndex(force bool) map[string]string {
	apacheIdxMu.Lock()
	defer apacheIdxMu.Unlock()
	if !force && len(apacheIdx) > 0 && time.Since(apacheIdxTime) < apacheIdxTTL {
		return copyApacheIdx(apacheIdx)
	}
	merged := map[string]string{}
	for _, page := range apacheDirPages {
		body, err := fetchURL(apacheLoungeBase + page)
		if err != nil {
			logger.W("[env][source] apache 目录页拉取失败 %s: %v", page, err)
			continue
		}
		for _, m := range apacheLinkRe.FindAllStringSubmatch(string(body), -1) {
			if len(m) < 3 {
				continue
			}
			merged[m[2]] = apacheLoungeBase + m[1]
		}
	}
	if len(merged) == 0 {
		logger.W("[env][source] apache 目录页未解析到任何 Win64 包")
		return copyApacheIdx(apacheIdx) // 保留旧结果兜底
	}
	apacheIdx = merged
	apacheIdxTime = time.Now()
	logger.I("[env][source] apache 索引已更新 versions=%d", len(merged))
	return copyApacheIdx(merged)
}

func copyApacheIdx(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// apacheVersParse 返回 Apache Lounge 上可下载的 httpd 版本列表。
// 版本来自目录索引而非入参 body（需同时扫描 VS17/VS18 两个目录页）。
func apacheVersParse(body []byte) []string {
	idx := apacheFetchIndex(false)
	vs := make([]string, 0, len(idx))
	for v := range idx {
		vs = append(vs, v)
	}
	return vs
}

// apacheURL 返回按版本解析真实下载地址的函数（Windows 专用）。
// 索引未命中时强制刷新一次再试；仍无结果返回空串（交给上层报"无可用下载源"，
// 好过拼一个必然 404 的 URL 把 HTML 存成 zip）。
func apacheURL() func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		if u, ok := apacheFetchIndex(false)[version]; ok && u != "" {
			return u
		}
		if u, ok := apacheFetchIndex(true)[version]; ok && u != "" {
			return u
		}
		logger.W("[env][source] apache 索引中无版本 %s（可用: %d 个）", version, len(apacheIdx))
		return ""
	}
}

func memcachedURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

func mariadbURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// mysqlURL 构造 MySQL 官方 CDN 地址：下载目录按 主.次 版本（{mm}）划分，
// 例如 8.4.3 → https://cdn.mysql.com/Downloads/MySQL-8.4/mysql-8.4.3-winx64.zip
func mysqlURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		mm := version
		if i := strings.LastIndexByte(version, '.'); i > 0 {
			mm = version[:i]
		}
		return strings.NewReplacer("{version}", version, "{mm}", mm).Replace(tmpl)
	}
}

func postgresURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

func mongoURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// minioURL 构造 MinIO 官方地址。MinIO 为单文件 exe（无版本路径），模板忽略 {version}。
func minioURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// frpcURL 构造 fatedier/frp 的 Windows 发行 zip 地址（含 frpc.exe / frps.exe / frpc.toml 等）。
func frpcURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// ftpURL 构造 FTPDMIN 的下载地址。FTPDMIN 为单文件 exe（无版本路径），模板忽略 {version}；
// 仅 Windows 提供（GUI 桌面发行）。非 Windows 返回空。
func ftpURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// ghURL / bunURL / traefikURL / mkcertURL / rabbitURL 均为 Windows 桌面发行包（便携 zip 或单 exe），非 Windows 返回空。
func ghURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}
func bunURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}
func traefikURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}
func mkcertURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}
func rabbitURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

// erlangURL 构造 Erlang/OTP 官方 Windows 便携 zip 地址（otp_win64_X.Y.Z.zip），仅 Windows。
func erlangURL(tmpl string) func(version, goos, arch string) string {
	return func(version, goos, arch string) string {
		if goos != "windows" {
			return ""
		}
		return strings.NewReplacer("{version}", version).Replace(tmpl)
	}
}

func mapNodeOS(goos string) string {
	switch goos {
	case "windows":
		return "win"
	case "linux":
		return "linux"
	case "darwin":
		return "darwin"
	}
	return goos
}

func mapNodeArch(arch string) string {
	if arch == "amd64" {
		return "x64"
	}
	return arch // arm64
}

func buildCustom(tmpl, version, goos, arch string) string {
	return strings.NewReplacer(
		"{version}", version, "{v}", version,
		"{os}", goos, "{arch}", arch,
	).Replace(tmpl)
}

// CandidateURLs 返回某运行时某版本的候选下载地址（有序）：活跃源优先，其余源兜底，自定义源最后。
// sourceID/custom 为可选覆盖（前端在发起安装时一并传入，先切源再下载）。
func CandidateURLs(rt Runtime, version string) []string {
	def := registry[rt]
	goos := runtime.GOOS
	arch := runtime.GOARCH

	var urls []string
	add := func(s Source) {
		if u := s.Build(version, goos, arch); u != "" {
			urls = append(urls, u)
		}
	}

	activeMu.RLock()
	active := activeSource[rt]
	activeMu.RUnlock()

	if active != "" {
		for _, s := range def.sources {
			if s.ID == active {
				add(s)
				break
			}
		}
	}
	for _, s := range def.sources {
		if s.ID == active {
			continue
		}
		add(s)
	}

	regMu.RLock()
	tmpl := customTemplate[rt]
	regMu.RUnlock()
	if tmpl != "" {
		if u := buildCustom(tmpl, version, goos, arch); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// ListSources 列出某运行时所有可用下载源（含自定义源），供前端渲染切换下拉框。
func ListSources(rt Runtime) []Source {
	def := registry[rt]
	out := append([]Source{}, def.sources...)
	regMu.RLock()
	tmpl := customTemplate[rt]
	regMu.RUnlock()
	if tmpl != "" {
		out = append(out, Source{
			ID:   "custom",
			Name: "自定义源",
			Build: func(v, o, a string) string {
				return buildCustom(tmpl, v, o, a)
			},
		})
	}
	return out
}

// ActiveSource 返回当前活跃源 ID（未显式设置时取第一个预设源）。
func ActiveSource(rt Runtime) string {
	activeMu.RLock()
	defer activeMu.RUnlock()
	if a, ok := activeSource[rt]; ok {
		return a
	}
	if len(registry[rt].sources) > 0 {
		return registry[rt].sources[0].ID
	}
	return ""
}

// SetActiveSource 切换活跃下载源。
func SetActiveSource(rt Runtime, id string) {
	activeMu.Lock()
	defer activeMu.Unlock()
	activeSource[rt] = id
}

// SetCustomSource 设置/清除用户自定义源模板（template=="" 表示清除）。
func SetCustomSource(rt Runtime, template string) {
	regMu.Lock()
	defer regMu.Unlock()
	if template == "" {
		delete(customTemplate, rt)
		return
	}
	customTemplate[rt] = template
}

// DisplayName 返回运行时的展示名（如 "Node.js"）。
func DisplayName(rt Runtime) string {
	return registry[rt].display
}

// Versions 返回某运行时的推荐可下载版本清单（拉取失败时的兜底）。返回副本，调用方不可修改内部状态。
func Versions(rt Runtime) []string {
	vs := registry[rt].versions
	out := make([]string, len(vs))
	copy(out, vs)
	return out
}

// ---- 版本列表缓存 ----
//
// AvailableVersions 每次调用都会请求上游，频繁切换运行时时会重复拉取。
// 加一层内存缓存：TTL=1h，key=runtime+sourceID+custom，命中直接返回，
// 同时 sourceID/custom 变化时自动失效（换源意味着要重新拉取对应列表）。
type versCacheEntry struct {
	vs      []string
	expired time.Time
	fetched bool // true=上游拉取成功, false=兜底列表(不应缓存,允许立即重试)
}

var (
	versCacheMu sync.RWMutex
	versCache   = make(map[string]versCacheEntry)
	versTTLSec  = 1 * time.Hour
)

// AvailableVersions 拉取某运行时的全量可下载版本（上游列表）；任何失败均兜底返回推荐列表。
// 带 1h 内存缓存，sourceID/custom 变化时自动失效。
// 返回的版本号格式与 CandidateURLs 的 version 参数一致（如 Go "1.25.0" / Node "v22.22.2"）。
func AvailableVersions(rt Runtime, sourceID, custom string) []string {
	cacheKey := string(rt) + "|" + sourceID + "|" + custom
	now := time.Now()

	versCacheMu.RLock()
	entry, hit := versCache[cacheKey]
	versCacheMu.RUnlock()

	if hit && now.Before(entry.expired) && len(entry.vs) > 0 {
		return entry.vs
	}

	def := registry[rt]
	var vs []string
	fetched := false
	if def.versURL != "" && def.versParse != nil {
		if body, err := fetchURL(def.versURL); err == nil {
		if parsed := def.versParse(body); len(parsed) > 0 {
			vs = sortVersionsDesc(parsed)
			fetched = true
			logger.I("[env][source] %s 上游版本拉取成功 fetched=%d len(vs)=%d first=%s", rt, len(parsed), len(vs), vs[0])
		} else {
			logger.W("[env][source] %s 上游版本解析返回 0 条", rt)
		}
	} else {
		logger.E("[env][source] %s 上游版本拉取失败: %v", rt, err)
	}
	}
	// HTML 兜底：GitHub API 限流或网络不通时，尝试解析 Releases 页面 HTML
	if len(vs) == 0 && def.versHTMLFallbackParse != nil && def.fallbackHTMLURL != "" {
		if body, err := fetchURL(def.fallbackHTMLURL); err == nil {
			if parsed := def.versHTMLFallbackParse(body); len(parsed) > 0 {
				vs = sortVersionsDesc(parsed)
				fetched = true
			}
		}
	}
	if len(vs) == 0 {
		vs = Versions(rt)
	}
	// Node.js: parseNodeVersions 内部已做智能筛选（LTS + 近 3 年，上限 60 条）

	// 写缓存：仅上游拉取成功时缓存（TTL 1h）。
	// 失败时不缓存，下次调用立即重试——避免网络抖动时用户长时间看到兜底列表。
	if fetched {
		versCacheMu.Lock()
		versCache[cacheKey] = versCacheEntry{vs: vs, expired: now.Add(versTTLSec), fetched: true}
		versCacheMu.Unlock()
	}

	return vs
}

// fetchURL 拉取 URL 内容（带代理与 30s 超时），最多 4MB。
// 若代理模式下失败，自动重试无代理直连（兼容部分场景下代理配置异常的情况）。
func fetchURL(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")

	// 先尝试带代理
	resp, err := (&http.Client{Transport: ProxyTransport()}).Do(req)
	if err != nil {
		// 代理失败，尝试直连
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req2.Header.Set("User-Agent", "QuickDock/1.0")
		resp, err = (&http.Client{Transport: &http.Transport{}}).Do(req2)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// ---- 上游版本列表解析 ----

// parseTagVersions 解析 GitHub Releases API 返回的 JSON（{ "tag_name": "..." } 列表），
// 按 prefixes 顺序去除版本前缀（如 "v" / "r"），并兼容 git-for-windows 的 ".windows.N" 后缀。
// 大多数运行时的版本列表解析都可复用本函数，避免每个运行时写一份近乎相同的实现。
func parseTagVersions(body []byte, prefixes ...string) []string {
	var arr []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		v := it.TagName
		for _, p := range prefixes {
			v = strings.TrimPrefix(v, p)
		}
		// git-for-windows 发布标签形如 "v2.45.0.windows.1"，去掉 .windows.N 后缀取纯版本号
		if i := strings.LastIndex(v, ".windows."); i >= 0 {
			v = v[:i]
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseReleasesTagVersionsHTML 从 GitHub Releases 页面 HTML 提取形如 v?1.2.3 的版本号（API 兜底）。
func parseReleasesTagVersionsHTML(body []byte) []string {
	re := regexp.MustCompile(`/releases/tag/v?(\d+\.\d+\.\d+)`)
	matches := re.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		v := string(m[1])
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// parseGitReleasesTagVersionsHTML git-for-windows 专用：Releases 页面 HTML 的标签含 .windows.N 后缀。
func parseGitReleasesTagVersionsHTML(body []byte) []string {
	re := regexp.MustCompile(`/releases/tag/v?(\d+\.\d+\.\d+)\.windows\.\d+`)
	matches := re.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		v := string(m[1])
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func parseNodeVersions(body []byte) []string {
	var arr []struct {
		Version string `json:"version"`
		Date    string `json:"date"`
		Lts     interface{} `json:"lts"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		logger.E("[env][source] parseNodeVersions unmarshal error: %v", err)
		return nil
	}
	logger.I("[env][source] parseNodeVersions unmarshaled %d entries", len(arr))
	// 智能筛选：LTS + 近 3 年发布版本，去重后按语义降序，上限 60 条
	type item struct { ver string; date time.Time; lts bool }
	var items []item
	seen := map[string]bool{}
	for _, it := range arr {
		if it.Version == "" || seen[it.Version] {
			continue
		}
		seen[it.Version] = true
		var d time.Time
		if it.Date != "" {
			d, _ = time.Parse("2006-01-02", it.Date)
		}
		// lts 可能是 string("Jod") 或 number 或 bool
		isLts := false
		switch v := it.Lts.(type) {
		case string:
			isLts = v != ""
		case bool:
			isLts = v
		case float64:
			isLts = v != 0
		}
		items = append(items, item{ver: it.Version, date: d, lts: isLts})
	}
	logger.I("[env][source] parseNodeVersions unique versions=%d items=%d first3=%v", len(seen), len(items), func() []string{ r := make([]string, 0, min(3, len(items))); for i := 0; i < len(r) && i < len(items); i++ { r = append(r, items[i].ver) }; return r }())
	now := time.Now()
	cutoff := now.AddDate(-3, 0, 0)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].lts != items[j].lts {
			return items[i].lts
		}
		iR := !items[i].date.IsZero() && items[i].date.After(cutoff)
		jR := !items[j].date.IsZero() && items[j].date.After(cutoff)
		if iR != jR {
			return iR
		}
		pi, pj := splitSemver(items[i].ver), splitSemver(items[j].ver)
		for k := 0; k < 3; k++ {
			if pi[k] != pj[k] {
				return pi[k] > pj[k]
			}
		}
		return false
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ver)
		if len(out) >= 60 {
			break
		}
	}
	return out
}

// parseGoVersionsFromHTML 从 go.dev/dl/ 页面 HTML 提取所有稳定版版本号。
// 该页面列出了所有已发布版本，无 API 配额限制，不受网络环境影响。
func parseGoVersionsFromHTML(body []byte) []string {
	html := string(body)
	// go.dev/dl/ 中版本以 "go1.23.4" 形式出现在 <a> 标签和 <td> 文本中
	re := regexp.MustCompile(`go(\d+\.\d+\.\d+)`)
	matches := re.FindAllSubmatch([]byte(html), -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		v := string(m[1])
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// parsePHPWinVersions 从 windows.php.net 发布目录（含 302 跳转到 downloads.php.net/~windows）
// 解析形如 php-8.3.20-Win32-vs16-x64.zip 或 php-7.4.33-Win32-vc15-x64.zip 的文件名，
// 提取完整版本号（如 8.3.20 / 7.4.33），可直接拼成 windows.php.net 的下载地址。
// 使用 vc?\d+ 兼容 VS（vs16）和 VC（vc14/vc15）编译器的所有二进制包。
// php.net 官方 releases JSON 仅返回大版本（"8"），无法用于拼下载 URL，故改用目录页。
var phpWinRe = regexp.MustCompile(`php-(\d+\.\d+\.\d+)-Win32-(?:vc|vs)\d+-x64\.zip`)

func parsePHPWinVersions(body []byte) []string {
	matches := phpWinRe.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, string(m[1]))
	}
	return out
}

func parseRedisVersions(body []byte) []string { return parseTagVersions(body, "v") }

var nginxRe = regexp.MustCompile(`nginx-(\d+\.\d+\.\d+)\.zip`)

func parseNginxVersions(body []byte) []string {
	matches := nginxRe.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, string(m[1]))
	}
	return out
}

// parseRedisVersionsHTML 从 GitHub Releases 页面 HTML 提取版本号（API 不可用时兜底）。
func parseRedisVersionsHTML(body []byte) []string { return parseReleasesTagVersionsHTML(body) }

func parseGitVersions(body []byte) []string { return parseTagVersions(body, "v") }

// parseGitVersionsHTML 从 GitHub Releases 页面 HTML 提取 MinGit 版本号（API 不可用时兜底）。
func parseGitVersionsHTML(body []byte) []string { return parseGitReleasesTagVersionsHTML(body) }

// parseCaddyVersions 从 GitHub Releases API 解析 caddyserver/caddy 的 tag_name（如 "v2.8.4"）。
func parseCaddyVersions(body []byte) []string { return parseTagVersions(body, "v") }

// parseCaddyVersionsHTML 从 GitHub Releases 页面 HTML 提取版本号（API 不可用时兜底）。
func parseCaddyVersionsHTML(body []byte) []string { return parseReleasesTagVersionsHTML(body) }

// parseComposerVersions 从 getcomposer.org/versions 解析 stable 数组的 version 字段（如 "2.7.7"）。
func parseComposerVersions(body []byte) []string {
	var obj struct {
		Stable []struct {
			Version string `json:"version"`
		} `json:"stable"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil
	}
	out := make([]string, 0, len(obj.Stable))
	for _, it := range obj.Stable {
		v := strings.TrimSpace(it.Version)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parsePythonVersions 从 python.org ftp 目录页解析形如 href="3.12.7/" 的子目录名（完整版本号）。
var pythonRe = regexp.MustCompile(`href="(\d+\.\d+\.\d+)/"`)

func parsePythonVersions(body []byte) []string {
	matches := pythonRe.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, string(m[1]))
	}
	return out
}

// parseMongoVersions 从 mongodb/mongo GitHub Releases 解析 tag_name（如 "r7.0.14"），去掉前缀 r。
func parseMongoVersions(body []byte) []string { return parseTagVersions(body, "r") }

// parseMailpitVersions 从 axllent/mailpit GitHub Releases 解析 tag_name（如 "v1.31.0"），去掉前缀 v。
func parseMailpitVersions(body []byte) []string { return parseTagVersions(body, "v") }

// parseFrpcVersions 从 fatedier/frp GitHub Releases 解析 tag_name（如 "v0.61.0"），去掉前缀 v。
func parseFrpcVersions(body []byte) []string { return parseTagVersions(body, "v") }

// parseFrpcVersionsHTML 从 GitHub Releases 页面 HTML 提取版本号（API 限流/不可用时兜底）。
func parseFrpcVersionsHTML(body []byte) []string { return parseReleasesTagVersionsHTML(body) }

// parseBunVersions 从 oven-sh/bun GitHub Releases 解析 tag_name（如 "bun-v1.4.1"），去掉前缀 "bun-v"。
func parseBunVersions(body []byte) []string { return parseTagVersions(body, "bun-v") }

// GitHub Releases 标签形如 "vX.Y.Z" 的运行时，统一委托（前缀 "v"）。
func parseGhVersions(body []byte) []string       { return parseTagVersions(body, "v") }
func parseTraefikVersions(body []byte) []string { return parseTagVersions(body, "v") }
func parseMkcertVersions(body []byte) []string  { return parseTagVersions(body, "v") }
func parseRabbitVersions(body []byte) []string  { return parseTagVersions(body, "v") }

// parseErlangVersions 从 erlang/otp GitHub Releases 解析 tag_name（如 "OTP-27.3.4"），去掉前缀 "OTP-"。
func parseErlangVersions(body []byte) []string { return parseTagVersions(body, "OTP-") }

// sortVersionsDesc 按语义版本号降序排序（仅比较 major.minor.patch，忽略预发布）。
func sortVersionsDesc(vs []string) []string {
	uniq := make([]string, 0, len(vs))
	seen := map[string]bool{}
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			uniq = append(uniq, v)
		}
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		return semverLess(uniq[j], uniq[i]) // 降序
	})
	return uniq
}

// semverLess 比较 a<b（按 major.minor.patch 数值；无法解析时按字符串）。
func semverLess(a, b string) bool {
	pa := splitSemver(a)
	pb := splitSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func splitSemver(v string) [3]int {
	var r [3]int
	// 去掉前缀 v 与后缀预发布
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	for i, p := range strings.Split(v, ".") {
		if i >= 3 {
			break
		}
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		r[i] = n
	}
	return r
}
