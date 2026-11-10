package download

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quickdock/internal/aria2"
)

func TestTakePosition(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]string
		want  int
	}{
		{"absent", map[string]string{"dir": "D:/dl"}, 0},
		{"valid", map[string]string{"dir": "D:/dl", "__position": "3"}, 3},
		{"zero", map[string]string{"__position": "0"}, 0},
		{"negative", map[string]string{"__position": "-1"}, 0},
		{"not a number", map[string]string{"__position": "abc"}, 0},
		{"empty", map[string]string{"__position": ""}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := c.input
			if got := takePosition(opts); got != c.want {
				t.Errorf("takePosition() = %d, want %d", got, c.want)
			}
			if _, stillThere := opts["__position"]; stillThere {
				t.Error("__position 必须从 options 里摘掉：它不是 aria2 option，下发会被当成非法键")
			}
		})
	}
}

func TestBaseOptions(t *testing.T) {
	cfg := &aria2.Config{DownloadDir: "D:/dl", Split: 8, MaxConnPerSrv: 8}

	opts, dir, err := baseOptions(cfg, "", "")
	if err != nil {
		t.Fatalf("空 optsJSON 不该报错: %v", err)
	}
	if dir != "D:/dl" || opts["dir"] != "D:/dl" {
		t.Errorf("dir 未落回配置默认: dir=%q opts.dir=%q", dir, opts["dir"])
	}
	if opts["split"] != "8" || opts["max-connection-per-server"] != "8" || opts["continue"] != "true" {
		t.Errorf("基础选项不对: %v", opts)
	}

	// addOne 的顺序是 baseOptions → categoryDir → takePosition，
	// 所以 __position 此刻还在表里，由 takePosition 摘出当 addUri 的第 4 参数。
	opts, dir, err = baseOptions(cfg, "E:/custom", `{"out":"a.bin","__position":"3","header":"X-A: one"}`)
	if err != nil {
		t.Fatalf("合法 optsJSON 不该报错: %v", err)
	}
	if dir != "E:/custom" || opts["dir"] != "E:/custom" {
		t.Errorf("显式 dir 未生效: dir=%q opts.dir=%q", dir, opts["dir"])
	}
	if opts["out"] != "a.bin" || opts["header"] != "X-A: one" {
		t.Errorf("高级选项未合并: %v", opts)
	}
	if got := takePosition(opts); got != 3 {
		t.Errorf("takePosition = %d, want 3", got)
	}
	if _, ok := opts["__position"]; ok {
		t.Error("__position 仍在 options 里")
	}

	if _, _, err := baseOptions(cfg, "", `{bad json`); err == nil {
		t.Error("非法 optsJSON 应返回错误")
	}
}

func TestInTimeWindow(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 11, 10, h, m, 0, 0, time.Local)
	}
	cases := []struct {
		start, end string
		now        time.Time
		want       bool
	}{
		// 白天时段：左闭右开。
		{"09:00", "18:00", at(8, 59), false},
		{"09:00", "18:00", at(9, 0), true},
		{"09:00", "18:00", at(17, 59), true},
		{"09:00", "18:00", at(18, 0), false},
		// 跨零点时段（Start > End）：夜里 + 凌晨都算在内。
		{"23:00", "07:00", at(22, 59), false},
		{"23:00", "07:00", at(23, 0), true},
		{"23:00", "07:00", at(0, 30), true},
		{"23:00", "07:00", at(6, 59), true},
		{"23:00", "07:00", at(7, 0), false},
		{"23:00", "07:00", at(12, 0), false},
		// 格式非法时宁可不进入时段（保持全局限速），不能误判成全天。
		{"bad", "07:00", at(12, 0), false},
		{"23:00", "", at(0, 30), false},
	}
	for _, c := range cases {
		if got := inTimeWindow(c.start, c.end, c.now); got != c.want {
			t.Errorf("inTimeWindow(%q,%q,%s) = %v, want %v",
				c.start, c.end, c.now.Format("15:04"), got, c.want)
		}
	}
}

func TestParseHHMM(t *testing.T) {
	valid := map[string]int{
		"00:00": 0, "07:05": 425, "23:59": 1439, " 9:30 ": 570,
	}
	for in, want := range valid {
		got, ok := parseHHMM(in)
		if !ok || got != want {
			t.Errorf("parseHHMM(%q) = (%d,%v), want (%d,true)", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "24:00", "12:60", "-1:00", "1:2:3", "abc", "12"} {
		if _, ok := parseHHMM(in); ok {
			t.Errorf("parseHHMM(%q) 不该通过", in)
		}
	}
}

func TestValidateURI(t *testing.T) {
	valid := []string{
		"https://example.com/a.bin",
		"http://example.com:8080/a?b=1",
		"ftp://ftp.example.com/pub/a.iso",
		"sftp://host/path/a.zip",
		"magnet:?xt=urn:btih:ABCDEF&dn=ubuntu.iso",
		"MAGNET:?xt=urn:btmh:ABCDEF",
	}
	for _, in := range valid {
		if err := validateURI(in); err != nil {
			t.Errorf("validateURI(%q) 应通过，实际: %v", in, err)
		}
	}
	invalid := []string{
		"", "not-a-url", "file:///etc/passwd", "javascript:alert(1)",
		"magnet:?dn=missing-xt", "//example.com/a.bin",
	}
	for _, in := range invalid {
		if err := validateURI(in); err == nil {
			t.Errorf("validateURI(%q) 应被拒绝", in)
		}
	}
}

func TestNameFromURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://example.com/dir/a.bin?x=1", "a.bin"},
		{"https://example.com/", "https://example.com/"},
		{"magnet:?xt=urn:btih:ABCDEF&dn=ubuntu.iso", "ubuntu.iso"},
		{"magnet:?xt=urn:btih:ABCDEF", "magnet-abcdef"},
	}
	for _, c := range cases {
		if got := nameFromURL(c.in); got != c.want {
			t.Errorf("nameFromURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseInt64(t *testing.T) {
	if got := parseInt64(""); got != 0 {
		t.Errorf("空串应为 0，实际 %d", got)
	}
	if got := parseInt64("garbage"); got != 0 {
		t.Errorf("非法串应为 0，实际 %d", got)
	}
	if got := parseInt64("-42"); got != -42 {
		t.Errorf("负数应保留，实际 %d", got)
	}
}

func TestApplyKnownSize(t *testing.T) {
	s := &DownloadService{knownSize: map[string]taskSize{}}

	active := aria2.TaskView{GID: "a", Status: "active", Total: 500, Completed: 100, Progress: 20}
	s.applyKnownSize(&active)
	if active.Total != 500 || active.Completed != 100 {
		t.Fatalf("活跃任务不应被改写: %+v", active)
	}

	// aria2 暂停后把两个长度都报成 0，这里必须用最后一次已知值回填。
	paused := aria2.TaskView{GID: "a", Status: "paused"}
	s.applyKnownSize(&paused)
	if paused.Total != 500 || paused.Completed != 100 {
		t.Fatalf("暂停后应回填最后已知大小: %+v", paused)
	}
	if paused.Progress != 20 {
		t.Errorf("回填后进度要按回填值重算，实际 %v", paused.Progress)
	}

	other := aria2.TaskView{GID: "b", Status: "paused"}
	s.applyKnownSize(&other)
	if other.Total != 0 || other.Completed != 0 {
		t.Errorf("没有历史记录的任务不能凭空补大小: %+v", other)
	}

	resumed := aria2.TaskView{GID: "a", Status: "active", Total: 500, Completed: 120, Progress: 24}
	s.applyKnownSize(&resumed)
	if resumed.Completed != 120 {
		t.Errorf("aria2 重新报告的真实进度不能被旧缓存覆盖: %+v", resumed)
	}
}

func TestProbeFillsKnownTotal(t *testing.T) {
	s := &DownloadService{knownSize: map[string]taskSize{}}
	// 从未下载过 / 会话恢复的任务：缓存里原本也没有任何大小。
	s.applyKnownSize(&aria2.TaskView{GID: "cold", Status: "waiting"})
	s.setKnownTotal("cold", 536870912000)

	paused := aria2.TaskView{GID: "cold", Status: "paused"}
	s.applyKnownSize(&paused)
	if paused.Total != 536870912000 {
		t.Fatalf("URL 探测得到的总大小应回填到 0/0 任务，实际 %d", paused.Total)
	}
	if paused.Completed != 0 {
		t.Errorf("探测只能补总大小，已下载量不能凭空生成: %d", paused.Completed)
	}
}

// 设置表单里 fileAllocation 为空（旧前端）或传入非法值时，不能把用户已选的
// falloc 静默改回 none；合法值要规范化后再落盘。
func TestApplyOptionsInputFileAllocation(t *testing.T) {
	cfg := &aria2.Config{FileAllocation: "falloc"}
	applyOptionsInput(cfg, DlOptionsView{})
	if cfg.FileAllocation != "falloc" {
		t.Fatalf("空串不应覆盖已有策略: %q", cfg.FileAllocation)
	}
	applyOptionsInput(cfg, DlOptionsView{FileAllocation: "not-a-method"})
	if cfg.FileAllocation != "falloc" {
		t.Fatalf("非法值不应覆盖已有策略: %q", cfg.FileAllocation)
	}
	applyOptionsInput(cfg, DlOptionsView{FileAllocation: " PREALLOC "})
	if cfg.FileAllocation != "prealloc" {
		t.Fatalf("合法值应规范化后落盘: %q", cfg.FileAllocation)
	}
	if got := optionsView(cfg).FileAllocation; got != "prealloc" {
		t.Errorf("optionsView 回传的应是可直接填进下拉框的值: %q", got)
	}
}

// 目录名来自不可信的 URL：必须 URL 解码、去扩展名、清掉路径分隔符与控制字符，
// 拿不到可靠名字时宁可不分组，也不能让任务写进目录外的位置。
func TestTaskFolderName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://oss.example.com/dl/ubuntu-24.04.iso?Expires=123", "ubuntu-24.04"},
		{"https://x.com/dl/m-8vbe08r7rq8lx1c1u9wy_data_xvdc.raw", "m-8vbe08r7rq8lx1c1u9wy_data_xvdc"},
		{"https://x.com/dl/%E4%B8%AD%E6%96%87%20%E5%90%8D.zip", "中文 名"},
		{"https://x.com/dl/a%3Ab%3Fc.iso", "a_b_c"},
		{"https://x.com/dl/..%2Fetc.iso", "_etc"},
		{"https://x.com/dl/CON.exe", "_CON"},
		{"https://x.com/dl/...iso", ""},
		{"https://x.com/", ""},
		{"magnet:?xt=urn:btih:abc&dn=ubuntu.iso", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		if got := taskFolderName(c.in); got != c.want {
			t.Errorf("taskFolderName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := "https://x.com/dl/" + strings.Repeat("a", 120) + ".iso"
	if got := taskFolderName(long); len([]rune(got)) != 80 {
		t.Errorf("超长目录名应截断到 80 字符，实际 %d", len([]rune(got)))
	}
}

func TestTaskFolderDir(t *testing.T) {
	const uri = "https://x.com/dl/ubuntu.iso"
	cfg := &aria2.Config{}
	if got := taskFolderDir(cfg, "D:/dl", uri); got != "D:/dl" {
		t.Fatalf("未开启时不应改目录: %q", got)
	}
	cfg.PerTaskFolder = true
	want := filepath.Join(filepath.Clean("D:/dl"), "ubuntu")
	if got := taskFolderDir(cfg, "D:/dl", uri); got != want {
		t.Fatalf("子目录不对: got %q want %q", got, want)
	}
	if got := taskFolderDir(cfg, "D:/dl", "magnet:?xt=urn:btih:abc"); got != filepath.Clean("D:/dl") {
		t.Errorf("磁力链接不该建子目录: %q", got)
	}
	if got := taskFolderDir(cfg, "", uri); got != "" {
		t.Errorf("保存目录为空时不该拼路径: %q", got)
	}
	cfg.RemoteEnabled, cfg.RemoteHost = true, "192.168.1.5"
	if got := taskFolderDir(cfg, "/mnt/dl", uri); got != "/mnt/dl/ubuntu" {
		t.Errorf("远程 Linux 路径应保持斜杠: %q", got)
	}
	if got := taskFolderDir(cfg, `F:\Downloads`, uri); got != `F:\Downloads\ubuntu` {
		t.Errorf("远程 Windows 路径应保持反斜杠: %q", got)
	}
}
