package db

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPathDirs_PrependsExistingPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	cmd := &exec.Cmd{Env: []string{"FOO=1", "PATH=/usr/bin" + sep + "/bin"}}
	applyPathDirs(cmd, []string{"/opt/node18/bin"})
	want := "PATH=/opt/node18/bin" + sep + "/usr/bin" + sep + "/bin"
	if len(cmd.Env) != 2 || cmd.Env[1] != want {
		t.Errorf("PATH 未正确前置: %v", cmd.Env)
	}
	if cmd.Env[0] != "FOO=1" {
		t.Errorf("其它环境变量被改动: %v", cmd.Env)
	}
}

func TestApplyPathDirs_CaseInsensitiveKey(t *testing.T) {
	// Windows 上环境变量名不区分大小写，os.Environ() 里实际是 "Path="
	sep := string(os.PathListSeparator)
	cmd := &exec.Cmd{Env: []string{"Path=C:" + sep + "Windows"}}
	applyPathDirs(cmd, []string{"D:" + sep + "node"})
	if len(cmd.Env) != 1 {
		t.Fatalf("应就地替换而非追加新变量: %v", cmd.Env)
	}
	if !strings.HasPrefix(cmd.Env[0], "Path=D:"+sep+"node"+sep) {
		t.Errorf("Path 变量未被识别: %v", cmd.Env)
	}
}

func TestApplyPathDirs_NilEnvInheritsParent(t *testing.T) {
	// cmd.Env 为 nil 表示继承父进程环境；显式设置 Env 后必须自带完整环境，
	// 否则 Windows 上子进程连 SystemRoot 都没有，直接启动失败。
	cmd := &exec.Cmd{}
	applyPathDirs(cmd, []string{"/opt/php82/bin"})
	if len(cmd.Env) < 2 {
		t.Fatalf("应从 os.Environ() 补齐环境，得到 %d 项", len(cmd.Env))
	}
	foundPath := false
	for _, kv := range cmd.Env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			foundPath = true
		}
	}
	if !foundPath {
		t.Error("未写入 PATH")
	}
}

func TestApplyPathDirs_EmptyIsNoop(t *testing.T) {
	cmd := &exec.Cmd{Env: []string{"PATH=/usr/bin"}}
	applyPathDirs(cmd, nil)
	if cmd.Env != nil && len(cmd.Env) != 1 {
		t.Errorf("空 dirs 不应改动 cmd: %v", cmd.Env)
	}
}

func TestParseItemEnv(t *testing.T) {
	got := parseItemEnv(`[{"runtime":"node","version":"18.20.0"}]`)
	if len(got) != 1 || got[0].Runtime != "node" || got[0].Version != "18.20.0" {
		t.Errorf("解析失败: %+v", got)
	}
	if parseItemEnv("") != nil || parseItemEnv("{bad json") != nil {
		t.Error("空串/非法 JSON 应返回 nil")
	}
}

// withResolver 临时替换包级解析器，测试结束恢复（避免污染同包其它用例）。
func withResolver(t *testing.T, fn PathEnvResolver) {
	t.Helper()
	prev := pathEnvResolver
	pathEnvResolver = fn
	t.Cleanup(func() { pathEnvResolver = prev })
}

func TestItemPathDirs_ResolvesAndSkips(t *testing.T) {
	var asked [][2]string
	withResolver(t, func(rt, version string) string {
		asked = append(asked, [2]string{rt, version})
		if rt == "node" {
			return filepath.Join("opt", "node18", "bin")
		}
		return "" // 例如 php 未装该版本
	})
	item := &CollectionItem{Env: `[{"runtime":"node","version":"18.20.0"},{"runtime":"php","version":"8.2"}]`}
	dirs := itemPathDirs(item)
	if len(dirs) != 1 || dirs[0] != filepath.Join("opt", "node18", "bin") {
		t.Errorf("应只保留解析成功的目录: %v", dirs)
	}
	if len(asked) != 2 {
		t.Errorf("绑定项应逐个询问解析器: %v", asked)
	}
}

func TestItemPathDirs_NoResolverOrNoBinding(t *testing.T) {
	withResolver(t, nil)
	if got := itemPathDirs(&CollectionItem{Env: `[{"runtime":"node","version":"18"}]`}); got != nil {
		t.Errorf("未注入解析器时不应注入: %v", got)
	}
	withResolver(t, func(rt, version string) string { return "/x" })
	if got := itemPathDirs(&CollectionItem{}); got != nil {
		t.Errorf("无绑定时不应注入: %v", got)
	}
	if got := itemPathDirs(&CollectionItem{Env: `[{"runtime":"","version":"1"}]`}); len(got) != 0 {
		t.Errorf("空 runtime 应跳过: %v", got)
	}
}
