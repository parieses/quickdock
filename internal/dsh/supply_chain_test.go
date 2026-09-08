package dsh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestMergeAllowBuilds 验证：能新增 git 插件白名单、保留其它键、且幂等。
func TestMergeAllowBuilds(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "pnpm-workspace.yaml")
	orig := `packages:
  - .
nodeLinker: hoisted
autoInstallPeers: false
minimumReleaseAgeExclude:
  - dshmarket@1.10.1
allowBuilds:
  cloudflared: true
  node-pty: true
`
	if err := os.WriteFile(f, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}

	// 第一次 merge：应补入名单内 git 插件
	if err := mergeAllowBuilds(f, []string{"some-other-pkg"}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	b, _ := os.ReadFile(f)
	var root map[string]any
	if err := yaml.Unmarshal(b, &root); err != nil {
		t.Fatalf("回读 yaml 失败: %v\n%s", err, b)
	}
	// 保留既有键
	if root["nodeLinker"] != "hoisted" {
		t.Errorf("nodeLinker 被破坏: %v", root["nodeLinker"])
	}
	allow, ok := root["allowBuilds"].(map[string]any)
	if !ok {
		t.Fatalf("allowBuilds 非 map: %T", root["allowBuilds"])
	}
	if allow["cloudflared"] != true {
		t.Errorf("cloudflared 丢失/被改: %v", allow["cloudflared"])
	}
	if allow["@changfenhuang/dsh-genui"] != true {
		t.Errorf("git 插件白名单未写入: %v", allow)
	}
	if allow["some-other-pkg"] != true {
		t.Errorf("额外包未写入: %v", allow)
	}

	// 幂等：再 merge 一次，内容不应变化
	b1, _ := os.ReadFile(f)
	if err := mergeAllowBuilds(f, []string{"some-other-pkg"}); err != nil {
		t.Fatalf("二次 merge: %v", err)
	}
	b2, _ := os.ReadFile(f)
	if string(b1) != string(b2) {
		t.Errorf("非幂等：二次 merge 改了文件\n---\n%s\n---\n%s", b1, b2)
	}
}

// TestMergeAllowBuildsAbsentSection 验证 allowBuilds 段不存在时能新建（不报错）。
func TestMergeAllowBuildsAbsentSection(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "pnpm-workspace.yaml")
	orig := "packages:\n  - .\nnodeLinker: hoisted\n"
	if err := os.WriteFile(f, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}
	if err := mergeAllowBuilds(f, []string{"@x/y"}); err != nil {
		t.Fatalf("merge(无 allowBuilds 段): %v", err)
	}
	b, _ := os.ReadFile(f)
	if !strings.Contains(string(b), "allowBuilds:") {
		t.Errorf("未创建 allowBuilds 段:\n%s", b)
	}
}

// TestParseBuildDepsFromPnpmErr 验证从 pnpm 供应链报错提取包名。
func TestParseBuildDepsFromPnpmErr(t *testing.T) {
	msg := `The git-hosted package "@changfenhuang/dsh-genui@0.9.8" needs to execute
build scripts but is not in the "allowBuilds" allowlist.`
	got := parseBuildDepsFromPnpmErr(msg)
	if len(got) != 1 || got[0] != "@changfenhuang/dsh-genui" {
		t.Errorf("提取结果不符: %#v", got)
	}
	// 无匹配返回空
	if v := parseBuildDepsFromPnpmErr("progress: reused 298"); len(v) != 0 {
		t.Errorf("误报匹配: %#v", v)
	}
}
