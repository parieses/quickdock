package aitools

// WP18：辅助文件目录（`<cwd>/.quickdock/tmp/`）的两个尾巴。
//
// 这两条都在验「边界」而不是「功能」：提示只该在**真的**写进那个目录、
// 且项目**真的**有 .gitignore 又没忽略它时出现。判宽了会变成每写一个文件
// 都念一遍（模型会开始复述这句提示，用户看到的就是噪音）。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScratchGitignoreHint(t *testing.T) {
	dir := t.TempDir()
	cwd := dir

	// 没有 .gitignore：不提示（还没打算用 git 管这些文件）。
	if got := ScratchGitignoreHint(cwd, filepath.Join(cwd, quickdockDirName, scratchSubDir, "a.js")); got != "" {
		t.Errorf("没有 .gitignore 时不该提示: %q", got)
	}

	gi := filepath.Join(cwd, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules/\ndist\n"), 0o644); err != nil {
		t.Fatalf("写 .gitignore 失败: %v", err)
	}
	// 有 .gitignore 但没忽略 quickdock/：提示。
	if got := ScratchGitignoreHint(cwd, filepath.Join(cwd, quickdockDirName, scratchSubDir, "a.js")); got == "" {
		t.Error("应提示 quickdock/ 未进 .gitignore")
	}
	// 写的是项目里的正常文件：不提示。
	if got := ScratchGitignoreHint(cwd, filepath.Join(cwd, "src", "a.go")); got != "" {
		t.Errorf("写项目文件时不该提示: %q", got)
	}
	// 目录前缀相同但不是那个目录：不提示。
	if got := ScratchGitignoreHint(cwd, filepath.Join(cwd, quickdockDirName+"-other", "a.js")); got != "" {
		t.Errorf("%s-other 不是那个目录：%q", quickdockDirName, got)
	}

	// 已经忽略：不提示。三种常见写法都要认。
	for _, entry := range []string{quickdockDirName + "/", "/" + quickdockDirName, quickdockDirName, quickdockDirName + "/*"} {
		if err := os.WriteFile(gi, []byte("node_modules/\n"+entry+"\n"), 0o644); err != nil {
			t.Fatalf("写 .gitignore 失败: %v", err)
		}
		if got := ScratchGitignoreHint(cwd, filepath.Join(cwd, quickdockDirName, scratchSubDir, "a.js")); got != "" {
			t.Errorf("写成 %q 时不该再提示: %q", entry, got)
		}
	}

	// 注释掉的条目不算忽略。
	if err := os.WriteFile(gi, []byte("# "+quickdockDirName+"/\n"), 0o644); err != nil {
		t.Fatalf("写 .gitignore 失败: %v", err)
	}
	if got := ScratchGitignoreHint(cwd, filepath.Join(cwd, quickdockDirName, scratchSubDir, "a.js")); got == "" {
		t.Error("注释掉的条目不算忽略，应当提示")
	}
}

// 相对路径也要能判断（file_write 允许相对 cwd 的路径）。
func TestScratchGitignoreHintRelative(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".gitignore"), []byte("dist\n"), 0o644); err != nil {
		t.Fatalf("写 .gitignore 失败: %v", err)
	}
	if got := ScratchGitignoreHint(cwd, filepath.Join(quickdockDirName, scratchSubDir, "x.py")); got == "" {
		t.Error("相对路径落在 quickdock/ 下时应提示")
	}
	if got := ScratchGitignoreHint("", filepath.Join(quickdockDirName, scratchSubDir, "x.py")); got != "" {
		t.Errorf("没有 cwd 时无法判断，应返回空: %q", got)
	}
}
