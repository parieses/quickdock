package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 站点片段目录名必须与 internal/sites 的 SuggestedConfDir 约定一致，
// 否则「站点」页写的片段和这里 import 的目录会对不上。
func TestSitesDirUnderVersionDir(t *testing.T) {
	c := &CaddyRuntime{baseDir: t.TempDir()}
	got := c.SitesDir("1.2.3")
	want := filepath.Join(c.versionDir("1.2.3"), sitesDirName)
	if got != want {
		t.Errorf("SitesDir = %s, want %s", got, want)
	}
	if filepath.Base(got) != "quickdock-sites" {
		t.Errorf("片段目录名与 internal/sites 约定不一致: %s", filepath.Base(got))
	}
}

func TestIsLegacyDefaultCaddyfile(t *testing.T) {
	if len(legacyDefaultCaddyfiles) == 0 {
		t.Fatal("历史模板列表不能为空，否则老用户永远升级不到带 import 的模板")
	}
	if !isLegacyDefaultCaddyfile(legacyDefaultCaddyfiles[0]) {
		t.Error("历史默认模板应被识别为 QuickDock 生成的")
	}
	// 当前模板不能算「旧」——否则每次 ensureConfig 都会重写一遍。
	if isLegacyDefaultCaddyfile(defaultCaddyfile) {
		t.Error("当前模板被误判为历史模板")
	}
	if isLegacyDefaultCaddyfile("rr.com {\n\trespond \"mine\"\n}\n") {
		t.Error("用户自定义配置绝不能算默认模板")
	}
}

// 核心约束：只改写「不存在」与「内容仍是 QuickDock 历史模板」的两种文件。
func TestEnsureConfigUpgradesOnlyUntouchedDefault(t *testing.T) {
	c := &CaddyRuntime{baseDir: t.TempDir()}
	ver := "9.9.9"
	if err := os.MkdirAll(c.versionDir(ver), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1) 不存在 → 生成当前模板，且必须引入站点片段目录
	if err := c.ensureConfig(ver); err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	got, err := os.ReadFile(c.ConfigPath(ver))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != defaultCaddyfile {
		t.Errorf("首次生成的内容应等于当前默认模板，实际:\n%s", got)
	}
	if !strings.Contains(string(got), sitesDirName) {
		t.Error("默认模板必须引入站点片段目录，否则站点配置永远不会生效")
	}

	// 2) 内容仍是当前模板 → 保持原样（内容不变即未改写）
	if err := c.ensureConfig(ver); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(c.ConfigPath(ver)); string(again) != defaultCaddyfile {
		t.Error("已是当前模板时不该改动内容")
	}

	// 3) 旧模板 → 升级为当前模板
	if err := os.WriteFile(c.ConfigPath(ver), []byte(legacyDefaultCaddyfiles[0]), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureConfig(ver); err != nil {
		t.Fatalf("升级失败: %v", err)
	}
	if got, _ = os.ReadFile(c.ConfigPath(ver)); string(got) != defaultCaddyfile {
		t.Error("历史默认模板应被升级为当前模板")
	}

	// 4) 用户自己写的配置 → 一字不改
	custom := "rr.com {\n\trespond \"mine\"\n}\n"
	if err := os.WriteFile(c.ConfigPath(ver), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureConfig(ver); err != nil {
		t.Fatal(err)
	}
	if got, _ = os.ReadFile(c.ConfigPath(ver)); string(got) != custom {
		t.Errorf("用户配置被改写了（这是最不能出的事），实际:\n%s", got)
	}
}

func TestConfigNeedsSitesImport(t *testing.T) {
	c := &CaddyRuntime{baseDir: t.TempDir()}
	ver := "9.9.9"
	if err := os.MkdirAll(c.versionDir(ver), 0o755); err != nil {
		t.Fatal(err)
	}

	// 文件不存在：ensureConfig 会生成带 import 的新模板，不该报缺失
	if c.ConfigNeedsSitesImport(ver) {
		t.Error("配置不存在时不该报「缺少引入」")
	}

	if err := os.WriteFile(c.ConfigPath(ver), []byte(defaultCaddyfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if c.ConfigNeedsSitesImport(ver) {
		t.Error("含 import 的模板不该报缺失")
	}

	if err := os.WriteFile(c.ConfigPath(ver), []byte(":8080 {\n\trespond \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !c.ConfigNeedsSitesImport(ver) {
		t.Error("主配置缺少 quickdock-sites 引用时应报缺失")
	}
}
