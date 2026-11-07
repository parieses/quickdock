package env

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDepsDir 建一个非空目录，用来模拟已安装的 vendor / node_modules。
func writeDepsDir(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, ".keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findDeps(list []ProjectDeps, kind string) (ProjectDeps, bool) {
	for _, d := range list {
		if d.Kind == kind {
			return d, true
		}
	}
	return ProjectDeps{}, false
}

func TestDetectProjectDeps_PHP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{
  "require": {"php": "^8.2", "ext-json": "*", "monolog/monolog": "^3", "psr/log": "^3"},
  "require-dev": {"phpunit/phpunit": "^11", "psr/log": "^3"}
}`)
	writeFile(t, dir, "composer.lock", `{"packages":[{"name":"a"},{"name":"b"}],"packages-dev":[{"name":"c"}]}`)
	writeDepsDir(t, dir, "vendor")

	d, ok := findDeps(DetectProjectDeps(dir), "php")
	if !ok {
		t.Fatal("未识别 PHP 生态")
	}
	if d.Manager != "composer" || d.Manifest != "composer.json" || d.DepsDir != "vendor" {
		t.Errorf("生态字段错误: %+v", d)
	}
	if d.ManifestDeps != 3 {
		t.Errorf("直接依赖应为 3（排除 platform 包与重复项），得到 %d", d.ManifestDeps)
	}
	if !d.LockPresent || d.LockFile != "composer.lock" || d.LockDeps != 3 {
		t.Errorf("锁文件解析错误: %+v", d)
	}
	if !d.DepsInstalled {
		t.Error("vendor 非空应判为已安装")
	}
}

func TestDetectProjectDeps_NodePackageManager(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "packageManager": "pnpm@9.1.0+sha512.abcdef",
  "dependencies": {"vue": "^3", "lodash": "^4"},
  "devDependencies": {"vue": "^3", "typescript": "^5"},
  "peerDependencies": {"vue": "^3"}
}`)
	writeFile(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	// 迁移残留：packageManager 已声明 pnpm，不应被 npm 锁文件反推
	writeFile(t, dir, "package-lock.json", `{"packages":{"":{},"vue":{},"lodash":{}}}`)
	writeDepsDir(t, dir, "node_modules")

	d, ok := findDeps(DetectProjectDeps(dir), "node")
	if !ok {
		t.Fatal("未识别 Node 生态")
	}
	if d.Manager != "pnpm" || d.ManagerSpec != "9.1.0" {
		t.Errorf("packageManager 应优先于锁文件: %+v", d)
	}
	if d.ManifestDeps != 3 {
		t.Errorf("直接依赖应去重为 3，得到 %d", d.ManifestDeps)
	}
	if !d.LockPresent || d.LockFile != "pnpm-lock.yaml" || d.LockDeps != -1 {
		t.Errorf("pnpm 锁文件只认存在、不解析内容: %+v", d)
	}
	if !d.DepsInstalled {
		t.Error("node_modules 非空应判为已安装")
	}
}

func TestDetectProjectDeps_NodeNpmLockfileV1(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"x"}`)
	writeFile(t, dir, "package-lock.json", `{"lockfileVersion":1,"dependencies":{"vue":{},"lodash":{}}}`)

	d, _ := findDeps(DetectProjectDeps(dir), "node")
	if d.Manager != "npm" || d.LockDeps != 2 {
		t.Errorf("v1 锁文件应数 dependencies: %+v", d)
	}
	if d.DepsInstalled {
		t.Error("没有 node_modules 不应判为已安装")
	}
}

func TestDetectProjectDeps_MixedAndAncestor(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "composer.json", `{"require":{"php":"^8.1"}}`)
	child := filepath.Join(root, "frontend")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, child, "package.json", `{"packageManager":"yarn@4.1.0"}`)
	writeFile(t, child, "yarn.lock", "# yarn lockfile v1\n")

	list := DetectProjectDeps(child)
	if len(list) != 2 {
		t.Fatalf("应同时识别 PHP/Node 两个生态，得到 %d: %+v", len(list), list)
	}
	php, _ := findDeps(list, "php")
	if php.ManifestDir != root {
		t.Errorf("composer.json 应向上找到根目录: %+v", php)
	}
	node, _ := findDeps(list, "node")
	if node.ManifestDir != child || node.Manager != "yarn" || node.ManagerSpec != "4.1.0" {
		t.Errorf("就近清单解析错误: %+v", node)
	}
}

func TestDetectProjectDeps_EmptyAndBrokenManifest(t *testing.T) {
	if got := DetectProjectDeps(""); len(got) != 0 {
		t.Errorf("空目录应返回空: %+v", got)
	}
	if got := DetectProjectDeps(t.TempDir()); len(got) != 0 {
		t.Errorf("无清单目录应返回空: %+v", got)
	}
	// 清单损坏仍要算「识别到了项目」，只是字段取不到
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", "not json")
	list := DetectProjectDeps(dir)
	if len(list) != 1 || list[0].ManifestDeps != 0 {
		t.Errorf("损坏清单不应让整个项目消失: %+v", list)
	}
}

func TestSplitPackageManager(t *testing.T) {
	cases := []struct{ in, name, ver string }{
		{"pnpm@9.1.0+sha512.abc", "pnpm", "9.1.0"},
		{"yarn@4.1.0", "yarn", "4.1.0"},
		{"npm@10.2.0", "npm", "10.2.0"},
		{"bun@1.1.0", "bun", "1.1.0"},
		{"@scope/tool@1.2.3", "", ""},
		{"unknown@1.0", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		n, v := splitPackageManager(c.in)
		if n != c.name || v != c.ver {
			t.Errorf("splitPackageManager(%q) = (%q,%q), want (%q,%q)", c.in, n, v, c.name, c.ver)
		}
	}
}
