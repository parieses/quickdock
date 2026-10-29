package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// paramMap 构造测试用 params：JSON 解出来的形状是 map[string]interface{}。
func paramMap(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("测试数据不是合法 JSON: %v", err)
	}
	return m
}

func manifestWith(params map[string]interface{}) *PluginManifest {
	return &PluginManifest{
		ID:      "test-plugin",
		Name:    "测试插件",
		Version: "1.0.0",
		Backend: BackendConfig{Runtime: "none"},
		Commands: []Command{
			{ID: "scan", Title: "扫描", AcceptsInput: true, Params: params},
			{ID: "ping", Title: "探活"},
		},
	}
}

func TestSanitizeCommandParamsAcceptsValidSchema(t *testing.T) {
	params := paramMap(t, `{
		"type": "object",
		"properties": {
			"target": {"type": "string", "description": "目标网段", "default": "127.0.0.1"},
			"ports":  {"type": "string", "description": "端口范围"},
			"fast":   {"type": "boolean", "description": "只探活", "default": false},
			"count":  {"type": "integer", "description": "次数", "enum": [1, 2, 3]}
		},
		"required": ["target", "target"],
		"oneOf": [{"required": ["ports"]}]
	}`)
	m := manifestWith(params)
	warns := sanitizeCommandParams(m)
	if len(warns) != 0 {
		t.Fatalf("合规声明不应产生警告，实际: %v", warns)
	}
	got := m.Commands[0].Params
	if got == nil {
		t.Fatal("合规声明被丢掉了")
	}
	if _, ok := got["oneOf"]; ok {
		t.Fatal("宿主不支持的 oneOf 应被剔除，否则模型会以为分支约束生效")
	}
	req, _ := got["required"].([]interface{})
	if len(req) != 1 || req[0] != "target" {
		t.Fatalf("required 应去重成 [target]，实际 %v", req)
	}
	props, _ := got["properties"].(map[string]interface{})
	fast, _ := props["fast"].(map[string]interface{})
	if _, ok := fast["default"]; !ok {
		t.Fatal("default 是受支持的键，不该被剔除")
	}
	// 其它命令不受影响
	if len(m.Commands) != 2 || m.Commands[1].Params != nil {
		t.Fatal("未声明 params 的命令不应被改动")
	}
}

func TestSanitizeCommandParamsDropsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		reason string // 期望原因里包含的关键词
	}{
		{
			name:   "顶层不是 object",
			raw:    `{"type":"array","properties":{"a":{"type":"string","description":"x"}}}`,
			reason: "object",
		},
		{
			name:   "properties 为空",
			raw:    `{"type":"object","properties":{}}`,
			reason: "properties",
		},
		{
			name:   "属性缺 type",
			raw:    `{"type":"object","properties":{"a":{"description":"x"}}}`,
			reason: "type",
		},
		{
			name:   "属性 type 是 object",
			raw:    `{"type":"object","properties":{"a":{"type":"object","description":"x"}}}`,
			reason: "不被支持",
		},
		{
			name:   "属性缺 description",
			raw:    `{"type":"object","properties":{"a":{"type":"string"}}}`,
			reason: "description",
		},
		{
			name:   "default 类型不匹配",
			raw:    `{"type":"object","properties":{"a":{"type":"integer","description":"x","default":"5"}}}`,
			reason: "default",
		},
		{
			name:   "enum 元素类型不匹配",
			raw:    `{"type":"object","properties":{"a":{"type":"boolean","description":"x","enum":["yes"]}}}`,
			reason: "enum",
		},
		{
			name:   "required 引用不存在的键",
			raw:    `{"type":"object","properties":{"a":{"type":"string","description":"x"}},"required":["b"]}`,
			reason: "required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := manifestWith(paramMap(t, tc.raw))
			warns := sanitizeCommandParams(m)
			if len(warns) != 1 {
				t.Fatalf("应产生 1 条警告，实际 %d 条: %v", len(warns), warns)
			}
			if m.Commands[0].Params != nil {
				t.Fatal("不合规的 params 必须整条丢弃（半份 schema 会让模型以为参数更少）")
			}
			if !strings.Contains(warns[0], tc.reason) {
				t.Fatalf("警告文案应包含 %q，实际: %s", tc.reason, warns[0])
			}
		})
	}
}

func TestSanitizeCommandParamsEmptyObjectIsNoop(t *testing.T) {
	m := manifestWith(map[string]interface{}{})
	if warns := sanitizeCommandParams(m); len(warns) != 0 {
		t.Fatalf("空对象不该报警告: %v", warns)
	}
	if m.Commands[0].Params != nil {
		t.Fatal("空对象应归一成 nil")
	}
}

// TestLoadManifestKeepsPluginWhenParamsInvalid 是本功能的核心承诺：
// 一份写歪的 schema 只能丢字段，不能让插件加载失败。
func TestLoadManifestKeepsPluginWhenParamsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.json")
	body := `{
		"id": "test-plugin",
		"name": "测试插件",
		"version": "1.0.0",
		"backend": {"runtime": "none"},
		"commands": [
			{"id": "scan", "title": "扫描", "params": {"type": "array", "properties": {}}},
			{"id": "ping", "title": "探活", "description": "发 ICMP", "params": {
				"type": "object",
				"properties": {"host": {"type": "string", "description": "目标主机"}},
				"required": ["host"]
			}}
		]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}

	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("不合规 params 不应让插件加载失败: %v", err)
	}
	if m.Commands[0].Params != nil {
		t.Fatal("不合规声明应被丢弃")
	}
	if m.Commands[1].Params == nil {
		t.Fatal("同文件里合规的声明不该受牵连")
	}
	if m.Commands[1].Description != "发 ICMP" {
		t.Fatal("命令 description 应原样保留")
	}
}
