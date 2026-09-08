//go:build windows

package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWritePHPConfigPreservesComments 验证写回 php.ini 时不会删掉
// ;extension= 注释模板行（打磨项 #3 修复的回归点）。
func TestWritePHPConfigPreservesComments(t *testing.T) {
	dir := t.TempDir()
	extDir := filepath.Join(dir, "ext")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	// 可用扩展：foo（禁用）与 bar（启用）
	for _, name := range []string{"php_foo.dll", "php_bar.dll"} {
		if err := os.WriteFile(filepath.Join(extDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ini := strings.Join([]string{
		"; This is a comment line",
		";extension=php_foo.dll",
		"extension=php_bar.dll",
		"disable_functions = \"exec,system\"",
		"error_log = \"C:\\tmp\\php_errors.log\"",
		"memory_limit = 128M",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "php.ini"), []byte(ini), 0644); err != nil {
		t.Fatal(err)
	}

	// 仅启用 bar，保持 disable_functions / error_log 不变
	patch := PHPConfigPatch{
		DisableFunctions: "exec,system",
		ErrorLog:         "C:\\tmp\\php_errors.log",
		Extensions:       []string{"bar"},
	}
	if err := writePHPConfig(dir, patch); err != nil {
		t.Fatal(err)
	}

	got := string(mustRead(t, filepath.Join(dir, "php.ini")))

	if !strings.Contains(got, ";extension=php_foo.dll") {
		t.Errorf("注释模板行 ;extension=php_foo.dll 被丢失：\n%s", got)
	}
	if !strings.Contains(got, "extension=php_bar.dll") {
		t.Errorf("启用的 extension=php_bar.dll 未写出：\n%s", got)
	}
	if !strings.Contains(got, "disable_functions = \"exec,system\"") {
		t.Errorf("disable_functions 未保留：\n%s", got)
	}
	if !strings.Contains(got, "error_log = \"C:\\tmp\\php_errors.log\"") {
		t.Errorf("error_log 未保留：\n%s", got)
	}
	if !strings.Contains(got, "memory_limit = 128M") {
		t.Errorf("其他配置行 memory_limit 被改动：\n%s", got)
	}

	// 回读确认 foo 仍禁用、bar 启用
	cfg, err := readPHPConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	var fooEnabled, barEnabled bool
	for _, e := range cfg.Extensions {
		if e.Name == "foo" {
			fooEnabled = e.Enabled
		}
		if e.Name == "bar" {
			barEnabled = e.Enabled
		}
	}
	if fooEnabled {
		t.Error("foo 应为禁用，但被标记为启用")
	}
	if !barEnabled {
		t.Error("bar 应为启用，但被标记为禁用")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
