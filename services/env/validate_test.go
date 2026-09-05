package env

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateVersion(t *testing.T) {
	valid := []string{
		"8.3.0", "27.3.4", "1.20.0-nts", "0.1.1-rc.2",
		"php8.2", "1.20_nts", "v3", "go1.23.2",
	}
	for _, v := range valid {
		if err := validateVersion(v); err != nil {
			t.Errorf("validateVersion(%q) 不应报错，实际: %v", v, err)
		}
	}

	// 路径穿越 / 注入类：必须全部拒绝（P0-3 修复点）
	invalid := []string{
		"",                                    // 空
		"..",                                  // 向上穿越
		"../..",                               // 向上穿越
		`..\..\Users\xxx`,                     // Windows 反斜杠穿越
		"8.3.0/../../etc",                     // 含斜杠
		"../../etc/passwd",                    // 经典穿越
		"1.2.3;rm -rf /",                      // 命令注入风格
		".hidden",                             // 以 . 开头
		"a..b",                                // 连续点
		"a/b",                                 // 含路径分隔
		"a*b",                                 // 非法字符
		"../../windows/system32",             // 穿越
	}
	for _, v := range invalid {
		if err := validateVersion(v); err == nil {
			t.Errorf("validateVersion(%q) 应报错（路径穿越/非法字符）却通过了", v)
		}
	}

	// 超长
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if err := validateVersion(string(long)); err == nil {
		t.Error("validateVersion(65字符) 应报「过长」却通过了")
	}
}

func TestValidateImportDir(t *testing.T) {
	if err := validateImportDir(""); err == nil {
		t.Error("空目录应报错")
	}
	if err := validateImportDir("relative/path"); err == nil {
		t.Error("相对路径应报错")
	}

	// 真实存在的目录应通过
	tmp := t.TempDir()
	if err := validateImportDir(tmp); err != nil {
		t.Errorf("存在的绝对目录应通过，实际: %v", err)
	}

	// 存在的文件不应通过（必须目录）
	f, err := os.CreateTemp(tmp, "file-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := validateImportDir(f.Name()); err == nil {
		t.Error("普通文件路径应报错（要求目录）")
	}

	// 不存在的路径应报错
	if err := validateImportDir(filepath.Join(tmp, "nope", "x")); err == nil {
		t.Error("不存在的路径应报错")
	}
}
