package mcp

import (
	"strings"
	"testing"
)

// pluginInput 是 MCP 与插件之间唯一的参数形状转换点，四个分支各自对应一种真实调用：
// AI 只传文本、AI 传结构化参数、AI 什么都没传、AI 传了个数组。
func TestPluginInputShapes(t *testing.T) {
	// 缺省 → 空 map（非 nil：下游 recordPluginExecLog 会直接读 key）
	got, err := pluginInput(nil)
	if err != nil {
		t.Fatalf("nil input 不应报错: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("nil input 应得到空 map，实际 %#v", got)
	}

	// 字符串 → text（命令面板一直这么传）
	got, err = pluginInput("192.168.1.0/24")
	if err != nil {
		t.Fatalf("字符串 input 不应报错: %v", err)
	}
	if got["text"] != "192.168.1.0/24" {
		t.Fatalf("字符串应转成 text，实际 %#v", got)
	}

	// 空白字符串 → 空 map。若传成 {"text":""}，会把插件里已存的输入覆盖成空值
	got, err = pluginInput("   ")
	if err != nil || len(got) != 0 {
		t.Fatalf("空白字符串应得到空 map，实际 %#v err=%v", got, err)
	}

	// JSON 对象 → 原样透传（多字段结构化参数，这是本次放开的重点）
	in := map[string]interface{}{"text": "x", "mode": "fast", "port": float64(8080)}
	got, err = pluginInput(in)
	if err != nil {
		t.Fatalf("对象 input 不应报错: %v", err)
	}
	if got["mode"] != "fast" || got["port"] != float64(8080) {
		t.Fatalf("对象应原样透传，实际 %#v", got)
	}

	// 其他类型必须报错：静默转成字符串会让模型以为参数生效了
	for _, bad := range []interface{}{float64(1), []interface{}{"a"}, true} {
		if _, err := pluginInput(bad); err == nil {
			t.Fatalf("input=%#v（%T）应报错而不是静默接受", bad, bad)
		}
	}
}

// checkRequiredParams 只判 required：未知键与类型都放行，由插件自己决定宽严。
func TestCheckRequiredParams(t *testing.T) {
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{"type": "string", "description": "目标网段"},
			"ports":  map[string]interface{}{"type": "string", "description": "端口范围"},
		},
		"required": []interface{}{"target"},
	}

	// 没有 required 的声明直接放行
	if err := checkRequiredParams(map[string]interface{}{"type": "object"}, nil); err != nil {
		t.Fatalf("无 required 时不应报错: %v", err)
	}

	// 给齐 → 通过；顺手多给一个未声明的键也要通过（声明可能不全）
	if err := checkRequiredParams(params, map[string]interface{}{"target": "10.0.0.1", "extra": 1}); err != nil {
		t.Fatalf("必填参数齐全时不应报错: %v", err)
	}

	// 缺键 / null / 空白串都算没给，并且错误里要带出类型与说明，模型一次就能补齐
	for _, input := range []map[string]interface{}{
		nil,
		{"ports": "80"},
		{"target": nil},
		{"target": "   "},
	} {
		err := checkRequiredParams(params, input)
		if err == nil {
			t.Fatalf("input=%#v 缺 target 应报错", input)
		}
		if !strings.Contains(err.Error(), "target") || !strings.Contains(err.Error(), "目标网段") {
			t.Fatalf("错误应指出缺哪个键并带出说明，实际: %v", err)
		}
	}
}
