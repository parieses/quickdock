package sites

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newManagerAt 构造 Manager，并把 hosts 写入重定向到临时文件。
//
// 站点的增删改都会同步 hosts（afterChange），不重定向的话跑一次单测就会改掉开发机
// 真实的 hosts —— 在提权 shell 里尤其明显。
func newManagerAt(t *testing.T, dir string) *Manager {
	t.Helper()
	m := New(dir)
	m.hostsPath = filepath.Join(filepath.Dir(m.file), "hosts")
	return m
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	siteDir := filepath.Join(base, "webroot")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return newManagerAt(t, filepath.Join(base, "cfg")), siteDir
}

func TestValidateDomain(t *testing.T) {
	ok := map[string]string{
		"myapp.test":     "myapp.test",
		"MyApp.Test":     "myapp.test",
		" api.local ":    "api.local",
		"a.b.c.internal": "a.b.c.internal",
		"my-app.test":    "my-app.test",
		"api.test.":      "api.test", // 尾部点归一化
	}
	for in, want := range ok {
		got, err := validateDomain(in)
		if err != nil || got != want {
			t.Errorf("validateDomain(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	bad := []string{"", "localhost", "   ", "my_app.test", "-bad.test", "bad-.test", "a..b", "*.test", "a test"}
	for _, in := range bad {
		if got, err := validateDomain(in); err == nil {
			t.Errorf("validateDomain(%q) 应报错，却得到 %q", in, got)
		}
	}
}

func TestValidateDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := validateDir(dir); err != nil || got != dir {
		t.Errorf("validateDir(%q) = (%q, %v)", dir, got, err)
	}
	if _, err := validateDir(filepath.Join(base, "nope")); err == nil {
		t.Error("不存在的目录应报错")
	}
	file := filepath.Join(base, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateDir(file); err == nil {
		t.Error("文件路径应报错")
	}
	if _, err := validateDir(""); err == nil {
		t.Error("空目录应报错")
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	m, siteDir := newTestManager(t)

	s, err := m.Create("主站", "App.Test", siteDir, "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if s.Domain != "app.test" || !s.Enabled {
		t.Errorf("创建结果不符: %+v", s)
	}

	// 域名唯一性：大小写不敏感
	if _, err := m.Create("重复", "app.test", siteDir, ""); err == nil {
		t.Error("重复域名应被拒绝")
	}

	// 更新：换域名
	if _, err := m.Update(s.ID, "主站", "api.test", siteDir, false, ""); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Domain != "api.test" || list[0].Enabled {
		t.Errorf("更新未生效: %+v", list)
	}

	if _, err := m.Update("no-such-id", "x", "x.test", siteDir, true, ""); err == nil {
		t.Error("未知 id 应报错")
	}
	if err := m.Delete("no-such-id"); err == nil {
		t.Error("删除未知 id 应报错")
	}
	if err := m.Delete(s.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if len(m.List()) != 0 {
		t.Error("删除后列表应为空")
	}
}

func TestPersistAndReload(t *testing.T) {
	base := t.TempDir()
	siteDir := filepath.Join(base, "web")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(base, "cfg")

	m1 := newManagerAt(t, cfgDir)
	if _, err := m1.Create("站点", "s.test", siteDir, ""); err != nil {
		t.Fatal(err)
	}
	// 换一个 Manager 实例读同一份配置（模拟重启）
	m2 := newManagerAt(t, cfgDir)
	list := m2.List()
	if len(list) != 1 || list[0].Domain != "s.test" || list[0].Name != "站点" {
		t.Errorf("站点未持久化: %+v", list)
	}
}

func TestEnabledDomainsIncludesDisabledExcluded(t *testing.T) {
	m, siteDir := newTestManager(t)
	a, err := m.Create("A", "a.test", siteDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("B", "b.test", siteDir, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(a.ID, "A", "a.test", siteDir, false, ""); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	domains := m.enabledDomainsLocked()
	m.mu.Unlock()
	// 禁用站点不参与 --> 只剩 b.test
	if len(domains) != 1 || domains[0] != "b.test" {
		t.Fatalf("禁用站点不应参与证书/hosts: %v", domains)
	}
}

// TestCreateRapidHasUniqueIDs 回归：ID 只用纳秒时间戳时，同纳秒连续创建会让后者覆盖前者，
// 表现为「建了两个站点只剩一个」。
func TestCreateRapidHasUniqueIDs(t *testing.T) {
	m, siteDir := newTestManager(t)
	const n = 50
	ids := map[string]bool{}
	for i := 0; i < n; i++ {
		s, err := m.Create("站点", domainFor(i), siteDir, "")
		if err != nil {
			t.Fatalf("第 %d 次创建失败: %v", i, err)
		}
		if ids[s.ID] {
			t.Fatalf("ID 重复: %s", s.ID)
		}
		ids[s.ID] = true
	}
	if got := len(m.List()); got != n {
		t.Errorf("创建 %d 个站点后列表只有 %d 个（ID 相撞导致覆盖）", n, got)
	}
}

func domainFor(i int) string { return "s" + strconv.Itoa(i) + ".test" }

func TestStatusReturnsHostsInfo(t *testing.T) {
	m, siteDir := newTestManager(t)
	if _, err := m.Create("A", "a.test", siteDir, ""); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.HostsPath == "" {
		t.Error("HostsPath 不应为空")
	}
	// 真机上 hosts 里没有我们的域名，HostsOK 必为 false——这正是「未同步」的如实回显。
	if st.HostsOK {
		t.Log("本机 hosts 恰好已含 a.test（少见），跳过该断言")
	}
	// HostsBlock 是「自动提权被拒时手工粘贴」的兜底内容，必须带上启用域名
	if !strings.Contains(st.HostsBlock, "127.0.0.1 a.test") {
		t.Errorf("HostsBlock 内容不符: %q", st.HostsBlock)
	}
}

// 回归：证书与 hosts 是 nginx/caddy 提供服务的必要条件，站点集合一变就必须跟着收敛。
// 早先 afterChange 在「内置服务未运行」时直接返回，于是「先建站点、再启动 caddy」这条路
// 既拿不到证书、域名也解析不到 —— 站点建好了却打不开。
func TestAfterChangeSyncsCertAndHosts(t *testing.T) {
	m, siteDir := newTestManager(t)
	if _, err := m.Create("A", "a.test", siteDir, ""); err != nil {
		t.Fatalf("创建站点失败: %v", err)
	}
	st := m.Status()
	// hosts 必须已经收敛（写的是重定向后的临时文件）
	if !st.HostsOK {
		t.Errorf("创建站点后应同步 hosts: HostsOK=%v err=%q", st.HostsOK, st.HostsError)
	}
	// 没注入签发器 → 记下明确的失败原因，这正是前端「证书未签发」那一栏的来源
	if st.CertReady || st.CertError == "" {
		t.Errorf("未注入签发器时应留下明确的证书错误: ready=%v err=%q", st.CertReady, st.CertError)
	}

	// 禁用站点后域名要从 hosts 区块里消失（证书/hosts 由站点集合驱动）
	s := m.List()[0]
	if _, err := m.Update(s.ID, s.Name, s.Domain, s.Dir, false, ""); err != nil {
		t.Fatal(err)
	}
	domains, err := hostsBlockDomains(m.hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range domains {
		if d == "a.test" {
			t.Errorf("禁用后 a.test 仍留在 hosts: %v", domains)
		}
	}

	// 一个启用站点都没有 → 清空证书状态（不给空域名集合签一张没用的证书）
	if st := m.Status(); st.CertReady || st.CertError != "" {
		t.Errorf("无启用站点时不应保留证书状态: ready=%v err=%q", st.CertReady, st.CertError)
	}
}

// 旧版本的 sites.json 里带一个 port 字段（内置监听器的端口）。内置监听器已经去掉，
// 老配置文件必须仍能正常读出来，不能因为多了个未知字段就整份丢弃。
func TestLoadToleratesLegacyPortField(t *testing.T) {
	base := t.TempDir()
	cfgDir := filepath.Join(base, "cfg")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"port":443,"sites":[{"id":"1-1","name":"老站","domain":"old.test","dir":"` +
		strings.ReplaceAll(base, `\`, `\\`) + `","enabled":true}]}`
	if err := os.WriteFile(filepath.Join(cfgDir, "sites.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newManagerAt(t, cfgDir)
	list := m.List()
	if len(list) != 1 || list[0].Domain != "old.test" {
		t.Errorf("带历史 port 字段的配置应能正常读取: %+v", list)
	}
}

func TestSetModulesPersistAndNormalize(t *testing.T) {
	m, siteDir := newTestManager(t)
	s, err := m.Create("测试站", "mod.test", siteDir, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.SetModules(s.ID, []string{"cache", "spa", "static", "nope"}, 5173)
	if err != nil {
		t.Fatal(err)
	}
	// 未知模块被丢掉、spa 优先于 static、顺序按固定表排列
	if len(got.Modules) != 2 || got.Modules[0] != "spa" || got.Modules[1] != "cache" {
		t.Errorf("模块归一化不符: %v", got.Modules)
	}
	if got.ProxyPort != 5173 {
		t.Errorf("上游端口未保存: %d", got.ProxyPort)
	}

	// 模块是站点属性：重新加载（模拟重启）后应当还在
	re := newManagerAt(t, filepath.Dir(m.file))
	rs, err := re.Get(s.ID)
	if err != nil {
		t.Fatalf("重载后站点丢失: %v", err)
	}
	if len(rs.Modules) != 2 || rs.Modules[0] != "spa" || rs.ProxyPort != 5173 {
		t.Errorf("重载后模块选择丢失: %v port=%d", rs.Modules, rs.ProxyPort)
	}

	// 允许把勾选全部取消
	cleared, err := m.SetModules(s.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Modules) != 0 || cleared.ProxyPort != 0 {
		t.Errorf("清空模块失败: %v port=%d", cleared.Modules, cleared.ProxyPort)
	}

	// 越界端口归 0（生成时退回默认上游端口），不报错
	bounded, err := m.SetModules(s.ID, []string{"proxy"}, 70000)
	if err != nil {
		t.Fatal(err)
	}
	if bounded.ProxyPort != 0 {
		t.Errorf("越界端口应归 0，实际 %d", bounded.ProxyPort)
	}

	if _, err := m.SetModules("no-such-id", nil, 0); err == nil {
		t.Error("不存在的站点应报错")
	}
}

func TestSetCustomConfigPersistAndClear(t *testing.T) {
	m, siteDir := newTestManager(t)
	s, err := m.Create("测试站", "custom.test", siteDir, "")
	if err != nil {
		t.Fatal(err)
	}

	// 保存 nginx 片段
	const nginxSnip = "server {\n    server_name custom.test;\n}\n"
	got, err := m.SetCustomConfig(s.ID, "nginx", nginxSnip)
	if err != nil {
		t.Fatalf("保存自定义配置失败: %v", err)
	}
	if got.Configs["nginx"] != nginxSnip {
		t.Errorf("自定义内容未保存: %q", got.Configs["nginx"])
	}

	// 两个后端各自一份，互不覆盖
	const caddySnip = "custom.test {\n    respond \"hi\"\n}\n"
	if _, err := m.SetCustomConfig(s.ID, "caddy", caddySnip); err != nil {
		t.Fatal(err)
	}

	// 自定义内容随站点持久化（模拟重启）
	re := newManagerAt(t, filepath.Dir(m.file))
	rs, err := re.Get(s.ID)
	if err != nil {
		t.Fatalf("重载后站点丢失: %v", err)
	}
	if rs.Configs["nginx"] != nginxSnip || rs.Configs["caddy"] != caddySnip {
		t.Errorf("重载后自定义配置丢失: %+v", rs.Configs)
	}

	// 纯空白 = 清除，且只清那一个后端
	cleared, err := m.SetCustomConfig(s.ID, "nginx", "   \n  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cleared.Configs["nginx"]; ok {
		t.Error("纯空白应清除该后端的自定义配置")
	}
	if cleared.Configs["caddy"] != caddySnip {
		t.Error("清除 nginx 不应影响 caddy 的自定义配置")
	}

	// 返回值不与内部 map 共享（List/Get 走的是值拷贝，但 map 是引用类型）
	leak, err := m.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	leak.Configs["caddy"] = "被外部改坏"
	after, err := m.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Configs["caddy"] != caddySnip {
		t.Error("Get 返回的 Configs 与内部共享底层 map")
	}

	// 全部清空后不该落盘一个空的 configs 对象
	if _, err := m.SetCustomConfig(s.ID, "caddy", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "configs") {
		t.Errorf("全部清空后仍写入了 configs 字段: %s", data)
	}

	// 参数校验
	if _, err := m.SetCustomConfig(s.ID, "builtin", "x"); err == nil {
		t.Error("内置监听器不应支持自定义配置")
	}
	if _, err := m.SetCustomConfig(s.ID, "apache", "x"); err == nil {
		t.Error("未知后端应报错")
	}
	if _, err := m.SetCustomConfig("no-such-id", "nginx", "x"); err == nil {
		t.Error("不存在的站点应报错")
	}
}

func TestValidateDocRoot(t *testing.T) {
	// 文档根的值会被写进 nginx/Caddyfile 的 root 指令，一个 .. 就能把站点指向整块磁盘
	for _, bad := range []string{`..`, `../etc`, `public/../../etc`, `D:\www`, `/srv/www`, `pub/..`, `c:/www`} {
		if _, err := validateDocRoot(bad); err == nil {
			t.Errorf("文档根 %q 应被拒绝", bad)
		}
	}
	ok := map[string]string{
		"":             "",
		"public":       "public",
		"public/":      "public",
		`frontend\web`: "frontend/web",
		"./public":     "public",
		"  public  ":   "public",
		"public/sub":   "public/sub",
		"public//sub/": "public/sub",
	}
	for in, want := range ok {
		got, err := validateDocRoot(in)
		if err != nil {
			t.Errorf("文档根 %q 不应报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("文档根 %q = %q，期望 %q", in, got, want)
		}
	}
}

func TestCreateUpdateWithDocRoot(t *testing.T) {
	m, siteDir := newTestManager(t)
	s, err := m.Create("博客", "blog.test", siteDir, "public")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(siteDir, "public")
	if s.DocRoot != "public" || s.EffectiveDir() != want {
		t.Errorf("文档根未生效: docRoot=%q effective=%q 期望 %q", s.DocRoot, s.EffectiveDir(), want)
	}

	// 文档根是持久化的站点属性，不是一次性的界面状态：重载后必须还在
	re := newManagerAt(t, m.dir)
	got, err := re.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DocRoot != "public" {
		t.Errorf("重载后文档根丢失: %q", got.DocRoot)
	}

	// 清空 = 回到项目根目录本身
	if _, err := m.Update(s.ID, "博客", "blog.test", siteDir, true, ""); err != nil {
		t.Fatal(err)
	}
	after, err := m.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DocRoot != "" || after.EffectiveDir() != siteDir {
		t.Errorf("清空文档根后应回到项目目录: %q / %q", after.DocRoot, after.EffectiveDir())
	}

	// 越界的文档根在入口就被挡住，不会写成一份指向项目外的 root
	if _, err := m.Create("非法", "bad.test", siteDir, "../.."); err == nil {
		t.Error("越界的文档根应被拒绝")
	}
	if _, err := m.Update(s.ID, "博客", "blog.test", siteDir, true, "/etc"); err == nil {
		t.Error("绝对路径的文档根应被拒绝")
	}
}

// 回归：装了 PHP 不等于每个站点都要跑 PHP。纯静态站点（前端构建产物目录）不该生成
// FastCGI 段，也就不该依赖 php-fpm 的 9000 —— 这正是「静态站点也得开 9000 吗」的答案。
func TestSiteNeedsPHP(t *testing.T) {
	// 文档根下有 index.php（Laravel / Yii2 / WordPress 的常态）
	phpRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(phpRoot, "index.php"), []byte("<?php ?>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !SiteNeedsPHP(Site{Dir: phpRoot}) {
		t.Error("文档根有 index.php 时应当需要 PHP")
	}
	// 入口在子目录：只看文档根那一层
	withDocRoot := Site{Dir: phpRoot, DocRoot: "."}
	if !SiteNeedsPHP(withDocRoot) {
		t.Error("文档根指向 . 时也应命中 index.php")
	}

	// 纯静态目录（Vite/React 构建产物）
	staticRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticRoot, "index.html"), []byte("<h1>x</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if SiteNeedsPHP(Site{Dir: staticRoot}) {
		t.Error("纯静态目录不该需要 PHP")
	}

	// 用户明确勾了纯静态 / SPA / 反向代理 → 一律不生成 PHP 段，与磁盘上有什么无关
	for _, mod := range []string{"static", "spa", "proxy"} {
		if SiteNeedsPHP(Site{Dir: phpRoot, Modules: []string{mod}}) {
			t.Errorf("勾了 %s 模块的站点不该需要 PHP", mod)
		}
	}

	// 目录不存在（先建站点后拉代码）不该报错，按静态处理
	if SiteNeedsPHP(Site{Dir: filepath.Join(staticRoot, "nope")}) {
		t.Error("目录不存在时应按静态处理")
	}
}
