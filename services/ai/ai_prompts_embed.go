package ai

// 提示词装配（WP9）。
//
// 正文都在 `prompts/modes/*.md`，这里只负责读出来并做最低限度的校验。
// 为什么一到运行期就检查：`//go:embed` 保证文件存在，但保证不了「文件还在
// 里面还留着字」——一次误删把某个 .md 清空，模型会收到一个空 system prompt，
// 表现为「它突然什么都不遵守了」，而没有任何报错。宁可启动就崩。

import (
	"embed"
	"strings"
)

//go:embed prompts/modes/*.md prompts/actions/*.md
var aiModePromptFS embed.FS

// loadModePrompt 读一个模式提示词。
func loadModePrompt(name string) string {
	b, err := aiModePromptFS.ReadFile("prompts/modes/" + name + ".md")
	if err != nil {
		panic("缺少提示词文件 prompts/modes/" + name + ".md：" + err.Error())
	}
	// 去掉末尾换行：原来这些正文是 Go 原始字符串字面量，本身不带尾换行。
	// 留着会让 system prompt 末尾多一个空行，与迁移前不是逐字节一致。
	s := strings.TrimRight(string(b), "\r\n")
	if strings.TrimSpace(s) == "" {
		panic("提示词文件为空 prompts/modes/" + name + ".md")
	}
	return s
}

// loadActionPrompt 读一个动作提示词（prompts/actions/<name>.md）。
//
// 缺失或为空一律 panic：动作提示词是行为本身，静默退化成「没有约束」会让
// 「翻译」变成普通聊天，而界面上看不出任何异常——这种失败必须在启动时炸掉。
func loadActionPrompt(name string) string {
	data, err := aiModePromptFS.ReadFile("prompts/actions/" + name + ".md")
	if err != nil {
		panic("缺少动作提示词 prompts/actions/" + name + ".md: " + err.Error())
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		panic("动作提示词为空: prompts/actions/" + name + ".md")
	}
	return s
}
