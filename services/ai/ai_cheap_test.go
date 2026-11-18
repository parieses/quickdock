package ai

// WP11：双模型路由。
//
// 这里验的三件事都是「出错时不会有报错」的类型：
// 没配时必须完全不变（默认路径不能被影响）；
// 配了时必须只换模型名（其它参数跟着主模型走）；
// 必须关掉 thinking（廉价模型多半不支持，报错信息还很难懂）。

import "testing"

func TestCheapProfileDefaultUnchanged(t *testing.T) {
	cfg := AIProfile{Model: "main-model", Temperature: 0.7, ThinkingEnabled: true, MaxTokens: 8000}
	got := aiCheapProfile(cfg)
	if got != cfg {
		t.Errorf("没配廉价模型时必须原样返回: %+v", got)
	}
	// 填成和主模型一样也算没配：那种配置只会让人以为省了钱。
	cfg.CheapModel = "main-model"
	if got := aiCheapProfile(cfg); got != cfg {
		t.Errorf("廉价模型与主模型相同时应原样返回: %+v", got)
	}
}

func TestCheapProfileSwapsModelOnly(t *testing.T) {
	cfg := AIProfile{
		Model: "main-model", CheapModel: "cheap-model",
		Temperature: 0.7, TopP: 0.9, MaxTokens: 8000, ThinkingEnabled: true,
		BaseURL: "https://example/v1", APIKey: "k",
	}
	got := aiCheapProfile(cfg)
	if got.Model != "cheap-model" {
		t.Errorf("模型名没换: %q", got.Model)
	}
	// 端点、密钥、采样参数必须跟着主模型：廉价档不是另一套账号。
	if got.BaseURL != cfg.BaseURL || got.APIKey != cfg.APIKey {
		t.Error("端点或密钥被改了")
	}
	if got.Temperature != cfg.Temperature || got.TopP != cfg.TopP || got.MaxTokens != cfg.MaxTokens {
		t.Error("采样参数被改了")
	}
	// thinking 必须关：廉价模型多半不支持，开着会 400 或白付思考 token。
	if got.ThinkingEnabled {
		t.Error("廉价档应关闭 thinking")
	}
	// 原结构体不能被就地改动（调用方还要用主模型继续跑对话）。
	if !cfg.ThinkingEnabled || cfg.Model != "main-model" {
		t.Error("aiCheapProfile 修改了入参")
	}
}

func TestCheapModelOf(t *testing.T) {
	if got := aiCheapModelOf(AIProfile{Model: "m"}); got != "m" {
		t.Errorf("没配时应回主模型: %q", got)
	}
	if got := aiCheapModelOf(AIProfile{Model: "m", CheapModel: " c "}); got != "c" {
		t.Errorf("配了应回廉价模型（并去空格）: %q", got)
	}
}
