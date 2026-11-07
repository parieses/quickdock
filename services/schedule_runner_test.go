package services

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"quickdock/internal/db"
)

// shellCmd 包一层平台 shell（splitArgs 只认双引号，故用双引号包裹）。
func shellCmd(body string) string {
	if runtime.GOOS == "windows" {
		return `cmd /c "` + body + `"`
	}
	return `sh -c "` + body + `"`
}

func TestParseCronExpr(t *testing.T) {
	valid := []string{
		"* * * * *", "*/5 * * * *", "0 9 * * 1-5", "0 0 1,15 * *",
		"30 2 1 JAN MON", "@daily", "@weekly", "0 0 * * 7",
		"0 0-23/2 * * *", "5/10 * * * *", "0 0 29 2 *",
	}
	for _, expr := range valid {
		if _, err := parseCronExpr(expr); err != nil {
			t.Errorf("合法表达式被拒 %q: %v", expr, err)
		}
	}
	invalid := []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *",
		"* * * * FOO", "@nope", "1-5-9 * * * *", "0 0 31 2 *",
	}
	for _, expr := range invalid {
		if err := validateCronExpr(expr); err == nil {
			t.Errorf("非法/永不匹配的表达式不该通过校验: %q", expr)
		}
	}
}

// 固定 2026-11-06 09:00:30（周五）作为起点，期望值全部按日历手算，
// 不依赖 time.Now：跑测试的机器在哪天、哪个时区都不影响结论。
func TestComputeNextRunCron(t *testing.T) {
	const from = "2026-11-06 09:00:30"
	cases := []struct{ name, expr, want string }{
		{"每天 9 点，今天已过", "0 9 * * *", "2026-11-07 09:00:00"},
		{"每 5 分钟", "*/5 * * * *", "2026-11-06 09:05:00"},
		{"工作日 9 点，周五已过跳下周一", "0 9 * * 1-5", "2026-11-09 09:00:00"},
		{"每月 1 号", "0 0 1 * *", "2026-12-01 00:00:00"},
		{"宏", "@daily", "2026-11-07 00:00:00"},
		{"日与周都受限时取或", "0 0 1 * 1", "2026-11-09 00:00:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeNextRun(&db.ScheduledTask{ScheduleKind: "cron", CronExpr: c.expr}, from)
			if got != c.want {
				t.Errorf("computeNextRun(%q) = %q, want %q", c.expr, got, c.want)
			}
		})
	}
	if got := computeNextRun(&db.ScheduledTask{ScheduleKind: "cron", CronExpr: "bad expr"}, from); got != "" {
		t.Errorf("非法表达式应返回空而不是 %q", got)
	}
}

func TestComputeNextRunMonthly(t *testing.T) {
	// 旧的「用 from.Day()」实现：1/31 跑过 2/28 后会永久漂到 28 号。
	// 显式 DayOfMonth 必须让下个月回到 31 号。
	task := &db.ScheduledTask{ScheduleKind: "monthly", TimeOfDay: "09:00", DayOfMonth: 31}
	if got, want := computeNextRun(task, "2026-01-31 10:00:00"), "2026-02-28 09:00:00"; got != want {
		t.Errorf("1/31 后应钳到 2/28: got %q want %q", got, want)
	}
	if got, want := computeNextRun(task, "2026-02-28 09:00:00"), "2026-03-31 09:00:00"; got != want {
		t.Errorf("月末钳位不该改变原始日号: got %q want %q", got, want)
	}
	last := &db.ScheduledTask{ScheduleKind: "monthly", TimeOfDay: "09:00", DayOfMonth: -1}
	if got, want := computeNextRun(last, "2026-02-10 09:00:00"), "2026-02-28 09:00:00"; got != want {
		t.Errorf("-1 应落在当月最后一天: got %q want %q", got, want)
	}
	legacy := &db.ScheduledTask{ScheduleKind: "monthly", TimeOfDay: "09:00"}
	if got, want := computeNextRun(legacy, "2026-01-31 10:00:00"), "2026-02-28 09:00:00"; got != want {
		t.Errorf("老数据（0）沿用创建日口径: got %q want %q", got, want)
	}
}

func TestCronNextStrictlyAfter(t *testing.T) {
	from := time.Date(2026, 11, 6, 9, 0, 0, 0, time.Local)
	at, ok := cronNext("*/5 * * * *", from)
	if !ok || !at.After(from) {
		t.Errorf("cronNext 必须返回严格晚于起点的时刻: %v, %v", at, ok)
	}
	if at.Minute()%5 != 0 {
		t.Errorf("步长 5 的分钟应为 5 的倍数: %v", at)
	}
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
