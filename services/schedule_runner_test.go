package services

import (
	"runtime"
	"strings"
	"testing"

	"quickdock/internal/db"
)

// shellCmd 包一层平台 shell（splitArgs 只认双引号，故用双引号包裹）。
func shellCmd(body string) string {
	if runtime.GOOS == "windows" {
		return `cmd /c "` + body + `"`
	}
	return `sh -c "` + body + `"`
}

func TestFlattenResult(t *testing.T) {
	long := strings.Repeat("x", 400)
	cases := []struct{ name, in, want string }{
		{"多行折叠为单行", "a\r\nb\n\n  c", "a b c"},
		{"纯空白视为空", " \n\t ", ""},
		{"超限按 rune 截断", long, strings.Repeat("x", schedResultMaxRunes) + "…（已截断）"},
		{"中文不切坏", strings.Repeat("中", 400), strings.Repeat("中", schedResultMaxRunes) + "…（已截断）"},
		{"恰好等于上限不追加标记", strings.Repeat("y", schedResultMaxRunes), strings.Repeat("y", schedResultMaxRunes)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := flattenResult(c.in); got != c.want {
				t.Errorf("flattenResult 结果不符:\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

func TestExecuteCommandTask(t *testing.T) {
	a := &AppService{}

	t.Run("带输出：退出码与输出摘要都进结果", func(t *testing.T) {
		status, result := a.executeCommandTask(&db.ScheduledTask{Target: shellCmd("echo hello-qd")})
		if status != "ok" {
			t.Errorf("status 应为 ok: %q", result)
		}
		if !strings.Contains(result, "退出码 0") {
			t.Errorf("结果应含退出码: %q", result)
		}
		if !strings.Contains(result, "hello-qd") {
			t.Errorf("结果应含命令输出: %q", result)
		}
	})

	t.Run("失败：非零退出码与 stderr 都进结果", func(t *testing.T) {
		status, result := a.executeCommandTask(&db.ScheduledTask{Target: shellCmd("echo boom-qd 1>&2 && exit 2")})
		if status != "fail" {
			t.Errorf("status 应为 fail: %q", result)
		}
		if !strings.Contains(result, "退出码 2") {
			t.Errorf("结果应含退出码: %q", result)
		}
		if !strings.Contains(result, "boom-qd") {
			t.Errorf("结果应含 stderr 输出: %q", result)
		}
	})

	t.Run("静默命令回退到不带输出的文案", func(t *testing.T) {
		target := shellCmd("exit 0")
		status, result := a.executeCommandTask(&db.ScheduledTask{Target: target})
		if status != "ok" {
			t.Errorf("status 应为 ok: %q", result)
		}
		if want := "命令已执行：" + target; result != want {
			t.Errorf("无输出时应回退旧文案:\n got: %q\nwant: %q", result, want)
		}
	})

	t.Run("启动失败", func(t *testing.T) {
		status, result := a.executeCommandTask(&db.ScheduledTask{Target: "qd-no-such-binary-xyz"})
		if status != "fail" {
			t.Errorf("status 应为 fail: %q", result)
		}
		if !strings.Contains(result, "命令执行失败") {
			t.Errorf("结果应说明执行失败: %q", result)
		}
	})
}
