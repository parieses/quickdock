package ai

// WP9：提示词迁移后不许「悄悄变短」。
//
// 这类回归最难发现：把一段正文搬进 .md 时少粘一段、或者正则截断在某个 ``` 上，
// 编译通过、对话照跑，只是模型少了一条约束。所以这里钉的是**内容契约**：
// 每个模式必须都在、都必须带几条关键约束。

import (
	"strings"
	"testing"
)

func TestModePromptsLoaded(t *testing.T) {
	for _, mode := range []string{"chat", "tutorial", "plan"} {
		p, ok := aiModePrompts[mode]
		if !ok {
			t.Fatalf("缺少模式提示词: %s", mode)
		}
		if len([]rune(p)) < 30 {
			t.Errorf("%s 的提示词过短（%d 字），像是搬丢了", mode, len([]rune(p)))
		}
		// md 来源的两条末尾不该带多余换行（迁移前是 Go 原始字符串，不带尾换行）。
		// chat 例外：它是 md 正文 + aiStyleHint，而 hint 自己以换行结尾。
		if mode != "chat" && strings.HasSuffix(p, "\n") {
			t.Errorf("%s 的提示词末尾多了换行", mode)
		}
	}
}

// chat 必须拼上 aiStyleHint：那是全模式共用的行为约束，
// 少了它模型会回到「先讲一大段背景」的老毛病。
func TestChatPromptIncludesStyleHint(t *testing.T) {
	if !strings.Contains(aiModePrompts["chat"], aiStyleHint) {
		t.Error("chat 提示词没有拼上 aiStyleHint")
	}
}

// 关键约束逐条钉住：这些句子一旦消失，行为会变但不会有任何报错。
func TestModePromptKeyConstraints(t *testing.T) {
	cases := []struct{ mode, want, why string }{
		{"chat", "QuickDock", "要自报身份，否则用户不知道这是哪个软件里的助手"},
		{"tutorial", "使用教程", "教程模式的定位"},
		{"tutorial", "不要罗列工具名", "自我介绍时不能把内部工具名念给用户"},
		{"plan", "只读", "计划模式不能写文件"},
	}
	for _, c := range cases {
		p := aiModePrompts[c.mode]
		if !strings.Contains(p, c.want) {
			t.Errorf("%s 提示词缺少「%s」（%s）", c.mode, c.want, c.why)
		}
	}
}

// 读不到的提示词必须 panic 而不是返回空串：空 system prompt 会让模型
// 失去全部约束，而现象是「它突然什么都不遵守了」，排查起来毫无线索。
func TestLoadModePromptPanicsOnMissing(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("缺失的提示词文件应当 panic")
		}
	}()
	loadModePrompt("no-such-mode")
}
