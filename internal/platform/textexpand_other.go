//go:build !windows

package platform

// 非 Windows 平台的空实现（macOS 分支未启用 Text Expansion 钩子）。

func TextExpansionSetResolver(fn func(string) string) {}

func TextExpansionSetSnippets(snippets map[string]string) {}

func TextExpansionStart(snippets map[string]string) {}

func TextExpansionStop() {}

func TextExpansionEnabled() bool { return false }
