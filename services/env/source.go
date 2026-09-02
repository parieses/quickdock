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
)

// 环境管理分组（侧边栏按组归类：语言 / Web 服务器 / 缓存 / 工具）
const (
	GroupLanguage  = "language"
	GroupWebServer = "webserver"
	GroupCache     = "cache"
	GroupTool      = "tool"
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
}

var (
	regMu    sync.RWMutex
	registry = map[Runtime]runtimeDef{
		RuntimeNode: {display: "Node.js", group: GroupLanguage, versions: []string{"v22.22.2", "v20.19.0", "v18.20.4"}, versURL: "https://nodejs.org/dist/index.json", versParse: parseNodeVersions, sources: []Source{
			{ID: "npmmirror", Name: "npmmirror 镜像", Build: nodeURL("https://registry.npmmirror.com/-/binary/node/{v}/node-{v}-{os}-{arch}.{ext}")},
			{ID: "official", Name: "Node.js 官方", Build: nodeURL("https://nodejs.org/dist/{v}/node-{v}-{os}-{arch}.{ext}")},
		}},
		RuntimeGo: {display: "Go", group: GroupLanguage, versions: []string{"1.23.4", "1.22.10", "1.21.13"}, versURL: "https://go.dev/dl/?mode=json", versParse: parseGoVersions, sources: []Source{
			{ID: "official", Name: "Go 官方", Build: goURL("https://go.dev/dl/go{version}.{os}-{arch}.{ext}")},
			{ID: "golangcn", Name: "golang.google.cn (国内)", Build: goURL("https://golang.google.cn/dl/go{version}.{os}-{arch}.{ext}")},
		}},
		RuntimePHP: {display: "PHP", group: GroupLanguage, versions: []string{"8.3.20", "8.2.27", "8.1.31"}, versURL: "https://downloads.php.net/~windows/releases/archives/", versParse: parsePHPWinVersions, sources: []Source{
			{ID: "windowsphpnet-archive", Name: "windows.php.net (archives)", Build: phpURL("https://downloads.php.net/~windows/releases/archives/php-{version}-Win32-vs16-x64.zip")},
			{ID: "windowsphpnet", Name: "windows.php.net (releases)", Build: phpURL("https://windows.php.net/downloads/releases/php-{version}-Win32-vs16-x64.zip")},
		}},
		RuntimeRedis: {display: "Redis", group: GroupCache, versions: []string{"7.4.0", "7.2.5", "7.0.15"}, versURL: "https://api.github.com/repos/redis-windows/redis-windows/releases?per_page=100", versParse: parseRedisVersions, sources: []Source{
			{ID: "rediswindows", Name: "redis-windows/redis-windows (GitHub)", Build: redisURL("https://github.com/redis-windows/redis-windows/releases/download/{version}/Redis-{version}-Windows-x64-msys2.zip")},
		}},
		RuntimeNginx: {display: "Nginx", group: GroupWebServer, versions: []string{"1.27.5", "1.26.3", "1.25.5"}, versURL: "https://nginx.org/download/", versParse: parseNginxVersions, sources: []Source{
			{ID: "nginxorg", Name: "nginx.org 官方", Build: nginxURL("https://nginx.org/download/nginx-{version}.zip")},
		}},
		RuntimeGit: {display: "Git", group: GroupTool, versions: []string{"2.45.0", "2.44.0", "2.43.0"}, versURL: "https://api.github.com/repos/git-for-windows/git/releases?per_page=100", versParse: parseGitVersions, sources: []Source{
			{ID: "gfw", Name: "git-for-windows (GitHub)", Build: gitURL("https://github.com/git-for-windows/git/releases/download/v{version}.windows.1/MinGit-{version}.windows.1-64-bit.zip")},
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

// AvailableVersions 拉取某运行时的全量可下载版本（上游列表）；任何失败均兜底返回推荐列表。
// 返回的版本号格式与 CandidateURLs 的 version 参数一致（如 Go "1.25.0" / Node "v22.22.2"）。
func AvailableVersions(rt Runtime, sourceID, custom string) []string {
	def := registry[rt]
	if def.versURL != "" && def.versParse != nil {
		if body, err := fetchURL(def.versURL); err == nil {
			if vs := def.versParse(body); len(vs) > 0 {
				return sortVersionsDesc(vs)
			}
		}
	}
	return Versions(rt)
}

// fetchURL 拉取 URL 内容（带代理与 10s 超时），最多 4MB。
func fetchURL(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	resp, err := (&http.Client{Transport: ProxyTransport()}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// ---- 上游版本列表解析 ----

func parseNodeVersions(body []byte) []string {
	var arr []struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if it.Version != "" {
			out = append(out, it.Version)
		}
	}
	return out
}

func parseGoVersions(body []byte) []string {
	var arr []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		v := strings.TrimPrefix(it.Version, "go")
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parsePHPWinVersions 从 windows.php.net 发布目录（含 302 跳转到 downloads.php.net/~windows）
// 解析形如 php-8.3.20-Win32-vs16-x64.zip 的文件名，提取完整版本号（如 8.3.20），
// 可直接拼成 windows.php.net 的下载地址。php.net 官方 releases JSON 仅返回大版本（"8"），
// 无法用于拼下载 URL，故改用 windows.php.net 目录页。
var phpWinRe = regexp.MustCompile(`php-(\d+\.\d+\.\d+)-Win32-vs16-x64\.zip`)

func parsePHPWinVersions(body []byte) []string {
	matches := phpWinRe.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, string(m[1]))
	}
	return out
}

func parseRedisVersions(body []byte) []string {
	var arr []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		v := strings.TrimPrefix(it.TagName, "v")
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

var nginxRe = regexp.MustCompile(`nginx-(\d+\.\d+\.\d+)\.zip`)

func parseNginxVersions(body []byte) []string {
	matches := nginxRe.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, string(m[1]))
	}
	return out
}

func parseGitVersions(body []byte) []string {
	var arr []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		v := strings.TrimPrefix(it.TagName, "v")
		v = strings.TrimSuffix(v, ".windows.1")
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

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
