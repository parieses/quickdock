package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixPthFileUncommentsImportSite 验证 _pth 修复的核心行为：
// embeddable 包默认把 `import site` 注释掉，导致 site-packages 不进 sys.path，
// 装好 pip 也 import 不到。修复后必须取消注释。
func TestFixPthFileUncommentsImportSite(t *testing.T) {
	dir := t.TempDir()
	pth := filepath.Join(dir, "python315._pth")
	original := "python315.zip\n.\n\n# Uncomment to run site.main() automatically\n#import site\n"
	if err := os.WriteFile(pth, []byte(original), 0644); err != nil {
		t.Fatalf("写入 _pth 失败: %v", err)
	}

	changed, err := fixPthFile(pth)
	if err != nil {
		t.Fatalf("fixPthFile 返回错误: %v", err)
	}
	if !changed {
		t.Fatal("期望 changed=true（首次修复必然有改动）")
	}

	data, err := os.ReadFile(pth)
	if err != nil {
		t.Fatalf("读取 _pth 失败: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "\nimport site\n") && !strings.HasPrefix(content, "import site\n") {
		t.Fatalf("未找到未注释的 import site，实际内容:\n%s", content)
	}
	if strings.Contains(content, "#import site") {
		t.Fatalf("仍残留注释形式的 import site，实际内容:\n%s", content)
	}
	// 不应破坏原有的 zip / 当前目录路径行
	if !strings.Contains(content, "python315.zip") || !strings.Contains(content, ".") {
		t.Fatalf("原有路径行被破坏，实际内容:\n%s", content)
	}
}

// TestFixPthFileIdempotent 验证幂等：重复执行不应反复追加内容。
func TestFixPthFileIdempotent(t *testing.T) {
	dir := t.TempDir()
	pth := filepath.Join(dir, "python313._pth")
	if err := os.WriteFile(pth, []byte("python313.zip\n.\n\n#import site\n"), 0644); err != nil {
		t.Fatalf("写入 _pth 失败: %v", err)
	}
	if _, err := fixPthFile(pth); err != nil {
		t.Fatalf("首次 fixPthFile 失败: %v", err)
	}
	first, _ := os.ReadFile(pth)

	changed, err := fixPthFile(pth)
	if err != nil {
		t.Fatalf("二次 fixPthFile 失败: %v", err)
	}
	if changed {
		t.Error("期望 changed=false（已修复过的文件不应再被改写）")
	}
	second, _ := os.ReadFile(pth)
	if string(first) != string(second) {
		t.Errorf("二次执行后内容发生变化:\n第一次:\n%s\n第二次:\n%s", first, second)
	}
}

// TestFixPthFileKeepsExistingSitePath 验证已有 site-packages 路径行时不再追加重复行。
func TestFixPthFileKeepsExistingSitePath(t *testing.T) {
	dir := t.TempDir()
	pth := filepath.Join(dir, "python312._pth")
	content := "python312.zip\n.\nLib\\site-packages\nimport site\n"
	if err := os.WriteFile(pth, []byte(content), 0644); err != nil {
		t.Fatalf("写入 _pth 失败: %v", err)
	}
	changed, err := fixPthFile(pth)
	if err != nil {
		t.Fatalf("fixPthFile 失败: %v", err)
	}
	if changed {
		t.Error("期望 changed=false（已含 site-packages 路径且 site 已启用）")
	}
	data, _ := os.ReadFile(pth)
	if n := strings.Count(string(data), "site-packages"); n != 1 {
		t.Errorf("site-packages 路径行应恰好 1 条，实际 %d 条:\n%s", n, data)
	}
}

// TestFixPthFileMissingFile 验证文件不存在时返回错误而非 panic。
func TestFixPthFileMissingFile(t *testing.T) {
	if _, err := fixPthFile(filepath.Join(t.TempDir(), "nope._pth")); err == nil {
		t.Error("期望文件不存在时返回错误")
	}
}

// TestFixPthFileEmptyPath 验证空路径被安全忽略。
func TestFixPthFileEmptyPath(t *testing.T) {
	changed, err := fixPthFile("")
	if err != nil {
		t.Errorf("空路径不应返回错误，实际: %v", err)
	}
	if changed {
		t.Error("空路径不应报告有改动")
	}
}

// TestDetectPythonEmbedLayoutNoExe 验证没有解释器时返回 nil（不是 embeddable）。
func TestDetectPythonEmbedLayoutNoExe(t *testing.T) {
	if lay := detectPythonEmbedLayout(t.TempDir()); lay != nil {
		t.Error("空目录不应被识别为 embeddable 布局")
	}
}

// TestDetectPythonEmbedLayoutFindsPth 验证能正确找到 _pth 文件。
func TestDetectPythonEmbedLayoutFindsPth(t *testing.T) {
	dir := t.TempDir()
	// 造一个假的解释器文件
	exe := filepath.Join(dir, "python.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0755); err != nil {
		t.Fatalf("写入假 exe 失败: %v", err)
	}
	pth := filepath.Join(dir, "python315._pth")
	if err := os.WriteFile(pth, []byte("python315.zip\n.\n#import site\n"), 0644); err != nil {
		t.Fatalf("写入 _pth 失败: %v", err)
	}

	lay := detectPythonEmbedLayout(dir)
	if lay == nil {
		t.Fatal("期望识别出 embeddable 布局")
	}
	if lay.PthFile != pth {
		t.Errorf("_pth 路径不对: 期望 %s，实际 %s", pth, lay.PthFile)
	}
	wantSite := filepath.Join(dir, "Lib", "site-packages")
	if lay.SitePkgs != wantSite {
		t.Errorf("site-packages 路径不对: 期望 %s，实际 %s", wantSite, lay.SitePkgs)
	}
}
