//go:build windows

package env

import (
	"strings"
	"testing"

	winreg "golang.org/x/sys/windows/registry"
)

// TestSystemPathEchoVariables 实证：读取系统 PATH 时，%变量% 占位符原样保留、不被展开为绝对路径，
// 且 REG_EXPAND_SZ 类型会被正确标记为 expand=true。测试结束还原原始 PATH。
func TestSystemPathEchoVariables(t *testing.T) {
	orig, origExpand := sysReadPath()

	// 最终还原原始 PATH
	defer func() {
		k, e := winreg.OpenKey(winreg.CURRENT_USER, `Environment`, winreg.READ|winreg.WRITE|winreg.SET_VALUE)
		if e != nil {
			return
		}
		defer k.Close()
		if orig == "" {
			_ = k.DeleteValue("PATH")
		} else if origExpand {
			_ = k.SetExpandStringValue("PATH", orig)
		} else {
			_ = k.SetStringValue("PATH", orig)
		}
	}()

	setPath := func(v string, expand bool) {
		k, e := winreg.OpenKey(winreg.CURRENT_USER, `Environment`, winreg.READ|winreg.WRITE|winreg.SET_VALUE)
		if e != nil {
			t.Fatalf("open Environment: %v", e)
		}
		defer k.Close()
		if expand {
			if e := k.SetExpandStringValue("PATH", v); e != nil {
				t.Fatalf("SetExpandStringValue: %v", e)
			}
		} else {
			if e := k.SetStringValue("PATH", v); e != nil {
				t.Fatalf("SetStringValue: %v", e)
			}
		}
	}

	// 场景1：REG_SZ + 含 %变量%
	sz := `%USERPROFILE%\qd-test;C:\Windows\system32`
	setPath(sz, false)
	val, expand := sysReadPath()
	if val != sz {
		t.Fatalf("SZ 回显不一致:\n got:  %q\n want: %q", val, sz)
	}
	if expand {
		t.Fatalf("REG_SZ 不应标记为 expand=true")
	}
	if !strings.Contains(val, "%USERPROFILE%") {
		t.Fatalf("变量占位符被展开或丢失: %q", val)
	}
	if strings.Contains(val, `C:\Users`) {
		t.Fatalf("REG_SZ 变量被错误展开为绝对路径: %q", val)
	}

	// 场景2：REG_EXPAND_SZ + 含 %变量%
	ex := `%APPDATA%\qd-test;%LOCALAPPDATA%\bin`
	setPath(ex, true)
	val2, expand2 := sysReadPath()
	if val2 != ex {
		t.Fatalf("EXPAND_SZ 回显不一致:\n got:  %q\n want: %q", val2, ex)
	}
	if !expand2 {
		t.Fatalf("REG_EXPAND_SZ 应标记为 expand=true")
	}
	if strings.Contains(val2, `C:\Users`) {
		t.Fatalf("EXPAND_SZ 变量被错误展开为绝对路径: %q", val2)
	}

	t.Logf("PASS: 变量占位符均原样保留（未展开）。SZ=%q EXPAND_SZ=%q", sz, ex)
}
