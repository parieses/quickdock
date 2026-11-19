package ai

// 按模型自动给输出上限（max_tokens）。
//
// 这不是「猜」——上游 API 不提供这个数（见 ai_model_caps.go 开头的说明），
// 所以只能靠能力表 + 运行期修正。这里钉住两头：常见模型给对数、认不出来给兜底。

import "testing"

func TestAutoMaxTokensByModel(t *testing.T) {
	cases := []struct {
		model  string
		window int
		want   int
	}{
		// 思考型模型要留足：截断后重试的成本比多要一点高得多。
		{"deepseek-reasoner", 65536, 8192},
		// v4 系列：1M 窗口 + thinking，输出给 32768（用户实测口径）。
		{"deepseek-v4.1-flash", 1000000, 32768},
		{"deepseek-v4.1-flash", 65536, 16384},
		{"deepseek-chat", 65536, 8192},
		{"gpt-5-chat", 200000, 32768},
		{"claude-3-7-sonnet", 200000, 32768},
		{"gpt-4o-mini", 128000, 16384},
		{"gpt-4o", 128000, 16384},
		{"gpt-4-turbo", 128000, 4096},
		{"qwen2.5-coder-32b", 32768, 8192},
		{"gemini-2.5-pro", 1000000, 65536},
		// 认不出来的模型：兜底值与旧行为一致。
		{"some-local-model", 0, aiDefaultMax},
		// 窗口很小的模型：夹到 w/4，且不低于 256。
		{"gpt-4o", 8192, 2048},
		{"gpt-4o", 512, 256},
	}
	for _, c := range cases {
		if got := autoMaxTokensOf(c.model, c.window); got != c.want {
			t.Errorf("autoMaxTokensOf(%q, %d) = %d，期望 %d", c.model, c.window, got, c.want)
		}
	}
}

// 空模型名不能 panik 或返回 0。
func TestAutoMaxTokensEmpty(t *testing.T) {
	if got := autoMaxTokensOf("", 0); got != aiDefaultMax {
		t.Errorf("空模型名应回落 %d，实际 %d", aiDefaultMax, got)
	}
	if got := autoMaxTokensOf("  ", 0); got <= 0 {
		t.Errorf("空白模型名给出了非法值 %d", got)
	}
}
