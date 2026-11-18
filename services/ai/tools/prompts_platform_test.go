package aitools

// WP9：平台注记的 golden 断言，按 GOOS 分档。
//
// 为什么必须分档：`prompts_platform_windows.go` 与 `_unix.go` 由 build tag
// 二选一，所以「同一份基准」在两个平台上必然有一个跑不到。这里按
// `runtime.GOOS` 选基准，两个平台各验各的（文档 §WP9 里那条说明指的就是这件事）。
//
// 这条测试真正防的是「搬文件时把内容搬丢」：编译通过、描述变短、模型行为变差，
// 但没有任何报错。

import (
	"runtime"
	"strings"
	"testing"
)

func TestPlatformShellNoteGolden(t *testing.T) {
	note := platformShellNote()

	if note == "" {
		t.Fatal("平台注记为空：embed 没生效或文件被清空")
	}
	// 结尾不能有换行：它要直接拼在上一条描述后面。
	if strings.HasSuffix(note, "\n") || strings.HasSuffix(note, "\r") {
		t.Error("平台注记末尾带了换行，拼进描述会断句")
	}
	// 必须以句号开头：它是接在上一句之后的，不是独立一段。
	if !strings.HasPrefix(note, "。") {
		t.Errorf("平台注记应以「。」开头（承接上一句）: %q", note[:min(len(note), 20)])
	}

	switch runtime.GOOS {
	case "windows":
		for _, want := range []string{"cmd.exe", "head", "PowerShell"} {
			if !strings.Contains(note, want) {
				t.Errorf("Windows 注记缺少 %q", want)
			}
		}
		// 明确不许出现 unix 基准里的措辞：那说明文件接反了。
		if strings.Contains(note, "/bin/sh") {
			t.Error("Windows 构建里出现了 unix 注记，检查 build tag")
		}
	default:
		for _, want := range []string{"/bin/sh", "POSIX"} {
			if !strings.Contains(note, want) {
				t.Errorf("Unix 注记缺少 %q", want)
			}
		}
		if strings.Contains(note, "cmd.exe") {
			t.Error("Unix 构建里出现了 Windows 注记，检查 build tag")
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
