package sites

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateHostsEntries 提权子进程是唯一持有管理员权限的代码路径，
// 它接受的参数形态就是最后一道闸门——这里逐个钉死允许与拒绝的边界。
func TestValidateHostsEntries(t *testing.T) {
	ok := []string{"localhost", "a.test", "my-app.test", "a.b.c.internal", "A.TEST"}
	for _, d := range ok {
		if err := validateHostsEntries([]string{d}); err != nil {
			t.Errorf("validateHostsEntries(%q) 应通过，却报 %v", d, err)
		}
	}
	// 空集合必须放行：它的效果只是把标记区块写空，不构成攻击面。
	// 这一层的职责是挡住「可能被当作路径 / 命令行参数 / 换行」的内容，不是限制集合大小。
	if err := validateHostsEntries(nil); err != nil {
		t.Errorf("清空场景应通过，却报 %v", err)
	}

	bad := []string{
		"a b", "a\tb", "a\nb", "\n127.0.0.1 evil.test",
		"../../Windows/System32/drivers/etc/hosts",
		"/etc/passwd", "a/b", "a\\b",
		"a;rm", "$(whoami)", "`id`", "a|b", "a&b",
		"#x", "a,b", "a'b", `a"b`, "a=b",
		"-a", "a-", ".a", "a.", " ",
	}
	for _, d := range bad {
		if err := validateHostsEntries([]string{d}); err == nil {
			t.Errorf("validateHostsEntries(%q) 应拒绝", d)
		}
	}
}

// TestSyncHostsCLI_RejectsInvalidDomainBeforeAnyWrite 校验必须先于任何文件访问：
// 非法参数绝不能走到「打开系统 hosts」那一步。
// 只断言错误来自校验层（含"非法域名"），这样即便本机是管理员也不会真的去写 hosts。
func TestSyncHostsCLI_RejectsInvalidDomainBeforeAnyWrite(t *testing.T) {
	err := SyncHostsCLI([]string{"a.test", "evil; rm -rf /"})
	if err == nil {
		t.Fatal("含注入字符的域名应被拒绝")
	}
	if !strings.Contains(err.Error(), "非法域名") {
		t.Errorf("应报校验错误（而非文件错误），得到: %v", err)
	}
}

// TestWriteHostsTo_NoopWhenNothingToDo 回归：hosts 已是目标状态时不得再写文件。
// 站点每次启动都会调到这里，若这里每次都写，等于每次启动都弹一次 UAC。
func TestWriteHostsTo_NoopWhenNothingToDo(t *testing.T) {
	dir := t.TempDir()

	// 1) 目标集合为空 + 文件不存在 → 无事可做，不该凭空造出 hosts 文件
	missing := filepath.Join(dir, "hosts-not-exist")
	if err := syncHostsTo(missing, nil, true); err != nil {
		t.Fatalf("无事可做时应返回 nil，得到: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("不该创建 hosts 文件")
	}

	// 2) 文件内容已与目标一致 → 不该重写（用 mtime 变化作为"写过盘"的证据）
	path := filepath.Join(dir, "hosts")
	if err := syncHostsFile(path, []string{"a.test", "localhost"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	contentBefore := readFile(t, path)

	if err := syncHostsTo(path, []string{"localhost", "a.test"}, true); err != nil {
		t.Fatalf("已同步时应返回 nil，得到: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("内容已一致却重写了文件（会导致每次启动多弹一次 UAC）")
	}
	if readFile(t, path) != contentBefore {
		t.Error("内容被改动")
	}

	// 3) 目标集合变化 → 必须真的写进去
	if err := syncHostsTo(path, []string{"localhost", "a.test", "b.test"}, true); err != nil {
		t.Fatalf("内容变化时应写入成功，得到: %v", err)
	}
	if !strings.Contains(readFile(t, path), "127.0.0.1 b.test") {
		t.Error("新域名未写入")
	}
}

// TestIsPermissionError 只有权限类错误才值得弹 UAC；别的错误提权也救不了，
// 白弹一次框是纯打扰。这个谓词决定了「弹 or 不弹」，所以单独测。
func TestIsPermissionError(t *testing.T) {
	yes := []error{
		&fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission},
		os.ErrPermission,
		errString("open C:\\WINDOWS\\System32\\drivers\\etc\\hosts: Access is denied."),
	}
	for _, e := range yes {
		if !isPermissionError(e) {
			t.Errorf("%v 应判定为权限错误", e)
		}
	}
	no := []error{
		nil,
		&fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist},
		errString("open D:\\nope\\hosts: The system cannot find the path specified."),
		errString("no space left on device"),
	}
	for _, e := range no {
		if isPermissionError(e) {
			t.Errorf("%v 不该判定为权限错误", e)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
