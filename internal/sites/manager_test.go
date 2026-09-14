package sites

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	siteDir := filepath.Join(base, "webroot")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(base, "cfg"), 0), siteDir
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

	s, err := m.Create("主站", "App.Test", siteDir)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if s.Domain != "app.test" || !s.Enabled {
		t.Errorf("创建结果不符: %+v", s)
	}

	// 域名唯一性：大小写不敏感
	if _, err := m.Create("重复", "app.test", siteDir); err == nil {
		t.Error("重复域名应被拒绝")
	}

	// 更新：换域名
	if _, err := m.Update(s.ID, "主站", "api.test", siteDir, false); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Domain != "api.test" || list[0].Enabled {
		t.Errorf("更新未生效: %+v", list)
	}

	if _, err := m.Update("no-such-id", "x", "x.test", siteDir, true); err == nil {
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

	m1 := New(cfgDir, 8443)
	if _, err := m1.Create("站点", "s.test", siteDir); err != nil {
		t.Fatal(err)
	}
	// 端口随配置一起持久化
	m2 := New(cfgDir, 0)
	if m2.Status().Port != 8443 {
		t.Errorf("端口未持久化: %d", m2.Status().Port)
	}
	list := m2.List()
	if len(list) != 1 || list[0].Domain != "s.test" || list[0].Name != "站点" {
		t.Errorf("站点未持久化: %+v", list)
	}
}

func TestEnabledDomainsIncludesDisabledExcluded(t *testing.T) {
	m, siteDir := newTestManager(t)
	a, err := m.Create("A", "a.test", siteDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("B", "b.test", siteDir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(a.ID, "A", "a.test", siteDir, false); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	domains := m.enabledDomainsLocked()
	m.mu.Unlock()
	// 禁用站点不参与 --> 只剩 localhost + b.test（注意 normalizeDomains 会排序，断言用集合而非下标）
	want := map[string]bool{"localhost": true, "b.test": true}
	if len(domains) != len(want) {
		t.Fatalf("禁用站点不应参与证书/hosts: %v", domains)
	}
	for _, d := range domains {
		if !want[d] {
			t.Errorf("出现了不该有的域名: %v", domains)
		}
	}
}

// TestCreateRapidHasUniqueIDs 回归：ID 只用纳秒时间戳时，同纳秒连续创建会让后者覆盖前者，
// 表现为「建了两个站点只剩一个」。
func TestCreateRapidHasUniqueIDs(t *testing.T) {
	m, siteDir := newTestManager(t)
	const n = 50
	ids := map[string]bool{}
	for i := 0; i < n; i++ {
		s, err := m.Create("站点", domainFor(i), siteDir)
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

func TestSetPort(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.SetPort(8443); err != nil {
		t.Fatalf("设置端口失败: %v", err)
	}
	if m.Status().Port != 8443 {
		t.Errorf("端口未生效: %d", m.Status().Port)
	}
	if err := m.SetPort(0); err == nil {
		t.Error("非法端口应报错")
	}
	if err := m.SetPort(70000); err == nil {
		t.Error("越界端口应报错")
	}
}

func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"app.test":      "app.test",
		"app.test:443":  "app.test",
		"App.Test:8443": "app.test",
		"[::1]:443":     "::1",
		"[::1]":         "::1",
		"":              "",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusReturnsPortAndHostsPath(t *testing.T) {
	m, siteDir := newTestManager(t)
	if _, err := m.Create("A", "a.test", siteDir); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.Running {
		t.Error("未 Start 时 Running 应为 false")
	}
	if st.Port != defaultPort {
		t.Errorf("默认端口应为 %d，得到 %d", defaultPort, st.Port)
	}
	if st.HostsPath == "" {
		t.Error("HostsPath 不应为空")
	}
	// 真机上 hosts 里没有我们的域名，HostsOK 必为 false——这正是「未同步」的如实回显。
	if st.HostsOK {
		t.Log("本机 hosts 恰好已含 a.test（少见），跳过该断言")
	}
	// HostsBlock 是「自动提权被拒时手工粘贴」的兜底内容，必须带上启用域名 + localhost。
	if !strings.Contains(st.HostsBlock, "127.0.0.1 a.test") || !strings.Contains(st.HostsBlock, "127.0.0.1 localhost") {
		t.Errorf("HostsBlock 内容不符: %q", st.HostsBlock)
	}
}

func TestAfterChangeNoopWhenStopped(t *testing.T) {
	// 服务未启动时改配置不应触发证书签发（没注入 issuer 也不会报错）
	m, siteDir := newTestManager(t)
	if _, err := m.Create("A", "a.test", siteDir); err != nil {
		t.Fatalf("服务未启动时创建站点不应失败: %v", err)
	}
}

func TestSetModulesPersistAndNormalize(t *testing.T) {
	m, siteDir := newTestManager(t)
	s, err := m.Create("测试站", "mod.test", siteDir)
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
	re := New(filepath.Dir(m.file), 0)
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
