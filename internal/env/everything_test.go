package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quickdock/internal/platform"
)

// parseEsOutput 是纯字符串解析，跨平台可确定性测试：剔除空行与 \r，按行归一化出文件名/目录。
func TestParseEsOutput(t *testing.T) {
	// 混合 \r\n、空行、纯 \n、以及正斜杠路径（es.exe 在部分配置下输出正斜杠）。
	raw := "C:\\Users\\me\\file1.txt\r\n" +
		"\r\n" +
		"D:\\data\\sub\\image.png\n" +
		"C:/tmp/note.md\r\n"

	results := parseEsOutput(raw)
	if len(results) != 3 {
		t.Fatalf("期望解析出 3 条非空路径，实际 %d 条", len(results))
	}

	// 第 1 条：Windows 反斜杠
	if results[0].Path != `C:\Users\me\file1.txt` {
		t.Errorf("path0 = %q", results[0].Path)
	}
	if results[0].Name != "file1.txt" {
		t.Errorf("name0 = %q", results[0].Name)
	}
	if results[0].Dir != `C:\Users\me` {
		t.Errorf("dir0 = %q", results[0].Dir)
	}

	// 第 2 条：含子目录的图片
	if results[1].Path != `D:\data\sub\image.png` {
		t.Errorf("path1 = %q", results[1].Path)
	}
	if results[1].Name != "image.png" {
		t.Errorf("name1 = %q", results[1].Name)
	}
	if results[1].Dir != `D:\data\sub` {
		t.Errorf("dir1 = %q", results[1].Dir)
	}

	// 第 3 条：正斜杠路径也能被 filepath 正确切分（Windows 下 filepath.Base/Dir 兼容）
	if results[2].Name != "note.md" {
		t.Errorf("name2 = %q", results[2].Name)
	}
	if results[2].Dir != `C:\tmp` {
		t.Errorf("dir2 = %q（filepath.Dir 应把 C:/tmp 规范为 C:\\tmp）", results[2].Dir)
	}

	// 测试环境的路径不存在，Modified 应保留默认 0（不报错）。
	for i, r := range results {
		if r.Modified != 0 {
			t.Errorf("第 %d 条路径不存在，Modified 应为 0，实际 %d", i, r.Modified)
		}
	}
}

func TestParseEsOutputEmpty(t *testing.T) {
	results := parseEsOutput("   \r\n\t\n")
	if len(results) != 0 {
		t.Errorf("纯空白输出应解析为空切片，实际 %d 条", len(results))
	}
}

// normalizeSort 把未知排序列回退到 name，其余原样小写透传。
func TestNormalizeSort(t *testing.T) {
	cases := map[string]string{
		"":                      "name",
		"name":                  "name",
		"NAME":                  "name",
		"  size  ":              "size",
		"path":                  "path",
		"Date_Modified":         "date_modified",
		"date_recently_changed": "date_recently_changed",
		"bogus_column":          "name",
	}
	for in, want := range cases {
		if got := normalizeSort(in); got != want {
			t.Errorf("normalizeSort(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// Search 仅在检索词为空（或纯空白）时返回 error，用于提示调用方；其余一律静默降级。
func TestSearchEmptyQueryReturnsError(t *testing.T) {
	rt := &EverythingRuntime{}
	if _, err := rt.Search(context.Background(), "   ", 30, "name"); err == nil {
		t.Error("空白检索词必须返回 error 引导调用方")
	}
	if _, err := rt.Search(context.Background(), "", 30, "name"); err == nil {
		t.Error("空检索词必须返回 error")
	}
}

// Search 在 Everything 未就绪（es 缺失 / 未运行）时绝不返回 error，保证不阻塞 QuickDock 自身检索。
// 逻辑保证：本机未安装 Everything 时（测试环境清空 PATH、baseDir 为空），按需拉起无从发生，
// 必然走降级分支返回 nil,nil。若本机装有 Everything，Search 会自动拉起后再检索——
// 此时 IsAvailable() 在调用返回后已为真，断言同样成立。
func TestSearchDegradesWithoutError(t *testing.T) {
	t.Setenv("PATH", "")
	rt := &EverythingRuntime{}

	results, err := rt.Search(context.Background(), "report", 30, "name")
	if err != nil {
		t.Fatalf("未就绪时不应返回 error（静默降级），但得到 err=%v", err)
	}
	if !rt.IsAvailable() && results != nil {
		t.Errorf("未就绪时结果应为 nil，实际 %d 条", len(results))
	}
}

// Search 透传 limit / sortBy 参数：空 limit 回落默认 30，非法 sort 回落 name，不引发异常。
func TestSearchHonorsLimitAndSortFallback(t *testing.T) {
	t.Setenv("PATH", "")
	rt := &EverythingRuntime{}
	if _, err := rt.Search(context.Background(), "x", 0, "bogus"); err != nil {
		t.Errorf("limit=0 不应报错，得到 %v", err)
	}
	if _, err := rt.Search(context.Background(), "x", -5, ""); err != nil {
		t.Errorf("limit 为负不应报错，得到 %v", err)
	}
}

// EverythingStatus 的四态必须是合法值，且 Enabled 与全局开关一致；可用性与运行态自洽。
func TestEverythingStatusStateValidAndConsistent(t *testing.T) {
	rt := &EverythingRuntime{}
	st := rt.EverythingStatus()

	valid := map[string]bool{
		EverythingStateNotInstalled: true,
		EverythingStateMissingES:    true,
		EverythingStateStopped:      true,
		EverythingStateRunning:      true,
	}
	if !valid[st.State] {
		t.Errorf("非法状态 %q", st.State)
	}
	if st.Enabled != FileSearchEnabled() {
		t.Errorf("Enabled 应=%v（FileSearchEnabled），实际 %v", FileSearchEnabled(), st.Enabled)
	}

	if st.State == EverythingStateRunning {
		if !rt.IsAvailable() {
			t.Error("running 态必须 IsAvailable()=true")
		}
	} else if rt.IsAvailable() {
		t.Error("非 running 态必须 IsAvailable()=false")
	}
}

// TestEverythingArch 仅断言返回值在合法集合内（x64/x86），不依赖具体架构。
func TestEverythingArch(t *testing.T) {
	a := everythingArch()
	if a != "x64" && a != "x86" {
		t.Fatalf("everythingArch() 返回非法值 %q", a)
	}
}

// TestFindExeRecursive 验证有界递归查找：命中多层目录内的 exe、深度超限剪枝、错误文件名不命中。
func TestFindExeRecursive(t *testing.T) {
	base := t.TempDir()
	// 嵌套两层放入 Everything.exe
	nested := filepath.Join(base, "a", "b")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(nested, "Everything.exe")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// 深度 5 层的干扰项（应被剪枝，不应命中）
	deep := filepath.Join(base, "x1", "x2", "x3", "x4", "x5")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	deepFile := filepath.Join(deep, "Everything.exe")
	if err := os.WriteFile(deepFile, []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	// 错误文件名的干扰项
	if err := os.WriteFile(filepath.Join(base, "everything.txt"), []byte("z"), 0644); err != nil {
		t.Fatal(err)
	}

	got := findExeRecursive(base, everythingExeNames)
	if got != target {
		t.Fatalf("findExeRecursive 应命中 %q，实际 %q", target, got)
	}

	// 不存在时返回空
	if p := findExeRecursive(t.TempDir(), everythingExeNames); p != "" {
		t.Fatalf("空目录应返回空，实际 %q", p)
	}
}

// splitVersion 解析点分数字版本；含非法字段（非数字/预发布后缀）时返回 nil，表示不可比较。
func TestSplitVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"1.4.1.1032", []int{1, 4, 1, 1032}},
		{"1.4.1.1024beta", []int{1, 4, 1, 1024}}, // 预发布后缀被截断
		{"  1.5.0  ", []int{1, 5, 0}},            // 空白被清理
		{"2", []int{2}},
		{"", nil},
		{"1.4.1.x", nil}, // 含非数字字段 → 不可比较
	}
	for _, c := range cases {
		got := splitVersion(c.in)
		if c.want == nil {
			if got != nil {
				t.Errorf("splitVersion(%q) 应返回 nil，实际 %v", c.in, got)
			}
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("splitVersion(%q) = %v，期望 %v", c.in, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("splitVersion(%q)[%d] = %d，期望 %d", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// versionLess 严格小于比较：位数不同补 0、相等返回 false、含不可比较字段返回 false。
func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.4.1.1024", "1.4.1.1032", true},
		{"1.4.1.1032", "1.4.1.1032", false},
		{"1.4.1.1032", "1.4.1.1024", false},
		{"1.4.0.0", "1.4.1.0", true},
		{"1.3.9.9", "1.4.0.0", true},
		{"1.5.0.0", "1.4.1.1032", false},
		{"1.4.1.1032", "1.4.1.1032.1", true}, // 位数不同，b 多一位视作更大
		{"1.4.1.x", "1.4.1.1032", false},     // 不可比较 → false（不误判更新）
		{"1.4.1.1032", "1.4.1.beta", false},  // 不可比较 → false
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q,%q) = %v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// UpdateAvailable 仅在「已安装且版本严格落后于 everythingStableVer」时为真；
// 未检测到 exe 或版本不可解析时一律 false，避免误触发更新。
func TestUpdateAvailable(t *testing.T) {
	rt := &EverythingRuntime{}
	// 无任何 Everything 安装时，更新不可用（不会误更新）。
	rt.baseDir = t.TempDir()
	if rt.UpdateAvailable() {
		t.Error("未安装 Everything 时 UpdateAvailable 必须为 false")
	}
}

// readExeVersion 直读真实 PE 版本资源（回归测试）：曾经把 Translation 的语言 ID 与
// 代码页高低位写反，拼出的 StringFileInfo 块名永远查不到，版本恒显示 unknown。
// 本机装有托管版 Everything 时必须能读出非空版本号。
func TestReadExeVersionRealExe(t *testing.T) {
	exe := filepath.Join(platform.DefaultDataDir(), "runtime", "everything", "Everything.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Skip("本机未安装托管版 Everything，跳过真实 exe 版本读取")
	}
	v := readExeVersion(exe)
	if v == "" {
		t.Fatal("readExeVersion 读托管版 Everything.exe 返回空（语言/代码页块名回归？）")
	}
	if v != everythingStableVer {
		t.Logf("本机版本 %s 与目标稳定版 %s 不同（用户自行更新过属正常）", v, everythingStableVer)
	}
}

// setSearchDiag / SearchDiag  round-trip：诊断码可记录、可读回、可清除。
// SearchDiag 经 EverythingStatusInfo 暴露给前端，是「静默降级」唯一可自愈场景的指引来源。
func TestSearchDiagRoundTrip(t *testing.T) {
	rt := &EverythingRuntime{}
	if d := rt.SearchDiag(); d != "" {
		t.Fatalf("初始诊断应为空，实际 %q", d)
	}
	rt.setSearchDiag(EverythingDiagIpcElevated)
	if d := rt.SearchDiag(); d != EverythingDiagIpcElevated {
		t.Fatalf("诊断应可读回，实际 %q", d)
	}
	// EverythingStatus 必须带上诊断码（前端面板据此显示指引）
	st := rt.EverythingStatus()
	if st.SearchDiag != EverythingDiagIpcElevated {
		t.Fatalf("EverythingStatus.SearchDiag = %q，期望 %q", st.SearchDiag, EverythingDiagIpcElevated)
	}
	rt.setSearchDiag("")
	if d := rt.SearchDiag(); d != "" {
		t.Fatalf("诊断应可清除，实际 %q", d)
	}
}

// exitCodeOf 只认 *exec.ExitError（含 fmt.Errorf %w 包装）；普通 error 返回 -1。
func TestExitCodeOf(t *testing.T) {
	if got := exitCodeOf(errors.New("boom")); got != -1 {
		t.Errorf("普通 error 应返回 -1，实际 %d", got)
	}
	if got := exitCodeOf(nil); got != -1 {
		t.Errorf("nil error 应返回 -1，实际 %d", got)
	}
}

// 诊断码取值稳定：前端按码分流引导文案（ipc_elevated → 退出提权的 Everything），
// 改码必须同步改前端 EnvironmentPage.vue 的判断。
func TestEverythingDiagCodes(t *testing.T) {
	if EverythingDiagIpcElevated == "" {
		t.Fatal("诊断码不能为空串")
	}
}

// isManaged 判断检测到的 exe 是否落在托管目录（baseDir）内，区分托管安装与系统目录安装。
func TestIsManaged(t *testing.T) {
	base := t.TempDir()
	rt := &EverythingRuntime{baseDir: base}

	managed := filepath.Join(base, "Everything.exe")
	if !rt.isManaged(managed) {
		t.Errorf("%q 应判定为托管安装", managed)
	}
	nested := filepath.Join(base, "sub", "Everything64.exe")
	if !rt.isManaged(nested) {
		t.Errorf("%q 应判定为托管安装（子目录也算）", nested)
	}
	system := filepath.Join(os.TempDir(), "Everything.exe")
	if rt.isManaged(system) {
		t.Errorf("%q 不应判定为托管安装（落在 baseDir 之外）", system)
	}
}

// parseEnvSettings 的缺省补齐语义（「装上即可用」与「尊重用户显式关闭」并存）：
//   - 字段整体缺省（老版本设置文件）→ 文件搜索默认开启；
//   - 显式写 false → 尊重，不得被默认值覆盖；
//   - JSON 损坏 → 第二个返回值为 false，调用方落安全默认。
func TestParseEnvSettingsDefaults(t *testing.T) {
	// 老文件：字段没有
	s, ok := parseEnvSettings([]byte(`{"everythingInstallPath":"D:\\Everything"}`))
	if !ok {
		t.Fatal("合法 JSON 应解析成功")
	}
	if !s.FileSearchEnabled {
		t.Error("缺省 fileSearchEnabled 应默认开启")
	}
	if s.EverythingInstallPath != `D:\Everything` {
		t.Errorf("其余字段应原样保留，实际 %q", s.EverythingInstallPath)
	}

	// 用户显式关闭文件搜索：必须尊重
	s, ok = parseEnvSettings([]byte(`{"fileSearchEnabled":false}`))
	if !ok {
		t.Fatal("合法 JSON 应解析成功")
	}
	if s.FileSearchEnabled {
		t.Error("显式 false 必须被尊重，不得默认开启")
	}

	// 损坏 JSON：ok=false
	if _, ok := parseEnvSettings([]byte(`{not json`)); ok {
		t.Error("损坏 JSON 应返回 ok=false")
	}
}

// truncateLog 截断超长文本（es.exe 失败时可能带上全量结果，不截断会刷爆日志），
// 短文本原样返回（仅去首尾空白）。
func TestTruncateLog(t *testing.T) {
	if got := truncateLog("  short  "); got != "short" {
		t.Errorf("短文本应原样返回（去空白），实际 %q", got)
	}
	long := ""
	for i := 0; i < 600; i++ {
		long += "x"
	}
	got := truncateLog(long)
	if len(got) >= len(long) {
		t.Errorf("超长文本应被截断，实际长度 %d", len(got))
	}
	if !strings.HasSuffix(got, "…(已截断)") {
		t.Errorf("截断结果应带省略标记，实际结尾 %q", got[len(got)-20:])
	}
}

// recoverInCooldown 冷却判定：首次（零值时间）不冷却；刚自愈过处于冷却期；
// 超过冷却时长后解冻。这是防止「等索引」恶化成「反复重启」的核心不变量。
func TestRecoverInCooldown(t *testing.T) {
	rt := &EverythingRuntime{}
	if rt.recoverInCooldown() {
		t.Error("首次自愈前不应处于冷却期")
	}
	rt.recoverMu.Lock()
	rt.lastRecoverAt = time.Now()
	rt.recoverMu.Unlock()
	if !rt.recoverInCooldown() {
		t.Error("刚自愈过必须处于冷却期，否则会把等索引变成反复重启")
	}
	rt.recoverMu.Lock()
	rt.lastRecoverAt = time.Now().Add(-everythingRecoverCooldown - time.Second)
	rt.recoverMu.Unlock()
	if rt.recoverInCooldown() {
		t.Error("超过冷却时长后应解冻")
	}
}

// waitIndexed 在 es.exe 定位不到时必须立刻返回 0（不能靠超时干等）：
// 冷启动路径会调它等待索引就绪，定位不到 CLI 时早退才不会拖慢检索。
func TestWaitIndexedNoEsReturnsZeroFast(t *testing.T) {
	t.Setenv("PATH", "")
	rt := &EverythingRuntime{}
	start := time.Now()
	if n := rt.waitIndexed(300 * time.Millisecond); n != 0 {
		t.Errorf("es 缺失时应返回 0，实际 %d", n)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("es 缺失时应早退（<250ms），实际耗时 %v", elapsed)
	}
}
