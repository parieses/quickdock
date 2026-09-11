package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---- 清单解析：bool 与 scope 对象两种写法 ----

func TestFilesystemPermParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want FilesystemPerm
	}{
		{"true 只给对话框", `true`, FilesystemPerm{Dialog: true}},
		{"false 什么都不给", `false`, FilesystemPerm{}},
		{"缺省 什么都不给", `null`, FilesystemPerm{}},
		{
			"scope 对象含对话框",
			`{"read":["~/Documents/**"],"write":["~/Downloads"]}`,
			FilesystemPerm{Dialog: true, Read: []string{"~/Documents/**"}, Write: []string{"~/Downloads"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p Permissions
			if err := json.Unmarshal([]byte(`{"filesystem":`+c.in+`}`), &p); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			got := p.Filesystem
			if got.Dialog != c.want.Dialog {
				t.Errorf("Dialog: got %v, want %v", got.Dialog, c.want.Dialog)
			}
			if strings.Join(got.Read, "|") != strings.Join(c.want.Read, "|") {
				t.Errorf("Read: got %v, want %v", got.Read, c.want.Read)
			}
			if strings.Join(got.Write, "|") != strings.Join(c.want.Write, "|") {
				t.Errorf("Write: got %v, want %v", got.Write, c.want.Write)
			}
		})
	}
}

// TestFilesystemGrantedSemantics 锁定向后兼容承诺：
// filesystem:true 只给对话框，**不给读写**。否则升级后 40+ 个老插件
// 会凭空获得整个磁盘的读写能力。
func TestFilesystemGrantedSemantics(t *testing.T) {
	var p Permissions
	if err := json.Unmarshal([]byte(`{"filesystem":true}`), &p); err != nil {
		t.Fatal(err)
	}
	fs := p.Filesystem
	if !fs.Granted() {
		t.Fatal("filesystem:true 应被视为声明了文件能力（可弹框）")
	}
	if !fs.Dialog {
		t.Error("filesystem:true 应允许对话框")
	}
	if len(fs.Read) != 0 || len(fs.Write) != 0 {
		t.Errorf("filesystem:true 不应授予任何读写 scope: read=%v write=%v", fs.Read, fs.Write)
	}
	for _, action := range []string{FSActionRead, FSActionWrite} {
		if fs.Allows(action, filepath.Join(t.TempDir(), "x")) {
			t.Errorf("filesystem:true 不应放行 %s", action)
		}
	}
}

// TestFilesystemPermMarshal 输出形状要与输入对称：仅对话框权限写回 true，
// 老插件写库 / 进市场索引的结果不变。
func TestFilesystemPermMarshal(t *testing.T) {
	cases := []struct {
		in   FilesystemPerm
		want string
	}{
		{FilesystemPerm{}, `false`},
		{FilesystemPerm{Dialog: true}, `true`},
		{FilesystemPerm{Dialog: true, Read: []string{"~/a/**"}}, `{"read":["~/a/**"]}`},
		{FilesystemPerm{Dialog: true, Write: []string{"~/b"}}, `{"write":["~/b"]}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.in)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		if string(got) != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

func TestFilesystemPermValidate(t *testing.T) {
	okCases := [][]string{
		{`~/Documents/**`},
		{`/var/log/**`},
		{`C:/data/**`}, // 跨平台容错：windows 插件在 mac 上校验不应失败
	}
	for _, c := range okCases {
		if err := (FilesystemPerm{Read: c}).Validate(); err != nil {
			t.Errorf("应通过校验 %v: %v", c, err)
		}
	}

	badCases := [][]string{
		{`Documents/**`}, // 相对路径：含义随工作目录变化，必须拒绝
		{`../etc/**`},
		{``},
		{`   `},
	}
	for _, c := range badCases {
		if err := (FilesystemPerm{Read: c}).Validate(); err == nil {
			t.Errorf("应拒绝 %v", c)
		} else if !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("应返回 ErrInvalidManifest，得到 %v", err)
		}
	}
}

// ---- 路径 scope 匹配 ----

func TestMatchScope(t *testing.T) {
	root, err := ResolveRealPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := filepath.Join

	cases := []struct {
		name    string
		pattern string
		target  string
		want    bool
	}{
		{"字面目录授予整个子树", j(root, "docs"), j(root, "docs", "a", "b.txt"), true},
		{"字面目录匹配自身", j(root, "docs"), j(root, "docs"), true},
		{"不许前缀越界（docs vs docs2）", j(root, "docs"), j(root, "docs2", "x"), false},
		{"无关路径", j(root, "docs"), j(root, "other", "x"), false},
		{"** 跨多级", j(root, "**"), j(root, "a", "b", "c"), true},
		{"** 匹配零级", j(root, "docs", "**"), j(root, "docs"), true},
		{"* 不跨段", j(root, "*"), j(root, "a"), true},
		{"* 不跨段（多级失败）", j(root, "*"), j(root, "a", "b"), false},
		{"段内通配", j(root, "*.log"), j(root, "app.log"), true},
		{"段内通配不匹配目录", j(root, "*.log"), j(root, "app.txt"), false},
		{"中间通配", j(root, "*", "c.txt"), j(root, "a", "c.txt"), true},
		{"中间通配不跨段", j(root, "*", "c.txt"), j(root, "a", "b", "c.txt"), false},
		{"pattern 内的 .. 被规整", j(root, "docs", "..", "docs"), j(root, "docs", "x"), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchScope(c.pattern, c.target); got != c.want {
				t.Errorf("matchScope(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
			}
		})
	}
}

func TestMatchScopeHomeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法定位主目录")
	}
	real, err := ResolveRealPath(home)
	if err != nil {
		t.Skipf("主目录无法解析: %v", err)
	}
	if !matchScope("~/Documents/**", filepath.Join(real, "Documents", "x.txt")) {
		t.Error("~ 未展开为用户主目录")
	}
	if matchScope("~/Documents/**", filepath.Join(real, "Desktop", "x.txt")) {
		t.Error("不应命中兄弟目录")
	}
}

// ---- 路径解析：符号链接与 .. ----

func TestResolveRealPathCleansParentRefs(t *testing.T) {
	root, err := ResolveRealPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRealPath(filepath.Join(root, "docs", "..", "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "secret.txt"); got != want {
		t.Errorf(".. 未被规整: got %s, want %s", got, want)
	}
}

// TestResolveRealPathSymlinkEscape 是 scope 校验成立的前提：
// 白名单内的软链接必须被解析到真实位置，否则 ~/Documents/link（→ 别处）
// 能把读写引到任意目录。
func TestResolveRealPathSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "inside")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(inside, "link")
	makeDirLink(t, link, outside)

	realInside, err := ResolveRealPath(inside)
	if err != nil {
		t.Fatal(err)
	}
	realOutside, err := ResolveRealPath(outside)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ResolveRealPath(filepath.Join(link, "evil.txt"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if want := filepath.Join(realOutside, "evil.txt"); got != want {
		t.Fatalf("链接未被解析: got %s, want %s", got, want)
	}

	perm := FilesystemPerm{Dialog: true, Read: []string{filepath.Join(realInside, "**")}}
	if perm.Allows(FSActionRead, got) {
		t.Error("经软链接逃出白名单的路径被放行——scope 校验形同虚设")
	}
	if !perm.Allows(FSActionRead, filepath.Join(realInside, "ok.txt")) {
		t.Error("白名单内的普通路径应放行")
	}
}

// TestResolveRealPathDanglingLink 悬空链接也必须解析到它指向的真实位置。
// 这是写操作的安全前提：若停在链接路径上，scope 校验看到的是白名单内的
// 链接名，而真正的写入会穿透到白名单之外的目标。
func TestResolveRealPathDanglingLink(t *testing.T) {
	dir := root(t)
	link := filepath.Join(dir, "dangling")
	target := filepath.Join(dir, "not-created-yet")
	makeDanglingDirLink(t, link, target)

	got, err := ResolveRealPath(filepath.Join(link, "evil.txt"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if want := filepath.Join(dir, "not-created-yet", "evil.txt"); got != want {
		t.Fatalf("悬空链接未解析到真实目标: got %s, want %s", got, want)
	}

	perm := FilesystemPerm{Dialog: true, Write: []string{filepath.Join(dir, "safe", "**")}}
	if perm.Allows(FSActionWrite, got) {
		t.Error("经悬空链接写入的路径被放行——写操作会穿透到白名单之外")
	}
}

// TestResolveRealPathNonExistent 待创建的新文件：解析父目录后拼回自身，
// 保证 host.fs.write 建新文件时也能落在已校验的目录下。
func TestResolveRealPathNonExistent(t *testing.T) {
	dir, err := ResolveRealPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRealPath(filepath.Join(dir, "new", "deep", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "new", "deep", "a.txt"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// ---- 权限层：host.fs.* 的默认拒绝与 scope 强制 ----

func TestFSHostMethodPermission(t *testing.T) {
	base := root(t)
	inScope := filepath.Join(base, "docs", "a.txt")
	outScope := filepath.Join(base, "..", "outside.txt")

	m := newTestManager()
	addTestPlugin(m, "test.fs.none", Permissions{})
	addTestPlugin(m, "test.fs.dialog", Permissions{Filesystem: FilesystemPerm{Dialog: true}})
	addTestPlugin(m, "test.fs.ro", Permissions{Filesystem: FilesystemPerm{
		Dialog: true,
		Read:   []string{filepath.Join(base, "docs", "**")},
	}})

	m.RegisterHostMethod("host.fs.read", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})
	m.RegisterHostMethod("host.fs.write", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})
	m.RegisterHostMethod("host.dialog.open", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"path": "/picked"}, nil
	})

	readParam := func(p string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"path": p})
		return b
	}

	t.Run("未声明 filesystem 的插件被拒", func(t *testing.T) {
		_, err := m.invokeHostMethod("test.fs.none", "host.fs.read", readParam(inScope))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("filesystem:true（仅对话框）拿不到读文件", func(t *testing.T) {
		if _, err := m.invokeHostMethod("test.fs.dialog", "host.dialog.open", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("对话框应可用: %v", err)
		}
		_, err := m.invokeHostMethod("test.fs.dialog", "host.fs.read", readParam(inScope))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("filesystem:true 不应放行读文件，得到: %v", err)
		}
	})

	t.Run("scope 内放行", func(t *testing.T) {
		got, err := m.invokeHostMethod("test.fs.ro", "host.fs.read", readParam(inScope))
		if err != nil {
			t.Fatalf("scope 内却被拒: %v", err)
		}
		if got.(map[string]interface{})["ok"] != true {
			t.Errorf("handler 未执行: %v", got)
		}
	})

	t.Run("scope 外拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod("test.fs.ro", "host.fs.read", readParam(outScope))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("读权限不蕴含写权限", func(t *testing.T) {
		_, err := m.invokeHostMethod("test.fs.ro", "host.fs.write", readParam(inScope))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("表外的 host.fs.* 一律按未知方法拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod("test.fs.ro", "host.fs.symlink", readParam(inScope))
		if !errors.Is(err, ErrUnknownHostMethod) {
			t.Fatalf("期望 ErrUnknownHostMethod（默认拒绝），得到: %v", err)
		}
	})

	t.Run("缺 path 参数不算权限错误", func(t *testing.T) {
		_, err := m.invokeHostMethod("test.fs.ro", "host.fs.read", json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("应报错")
		}
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrUnknownHostMethod) {
			t.Fatalf("参数错误被误分类，插件侧会拿到错误的 JSON-RPC 错误码: %v", err)
		}
	})
}

// TestFSMoveNeedsBothPathsInScope move 有两条路径，任一侧越界都必须拒绝：
// 只校验 from 等于允许「把东西搬进无权写入的目录」，只校验 to 等于允许「搬走无权移动的文件」。
func TestFSMoveNeedsBothPathsInScope(t *testing.T) {
	base := root(t)
	inDir := filepath.Join(base, "docs")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(base, "..", "outside.txt")

	m := newTestManager()
	addTestPlugin(m, "test.fs.rw", Permissions{Filesystem: FilesystemPerm{
		Dialog: true,
		Read:   []string{filepath.Join(inDir, "**")},
		Write:  []string{filepath.Join(inDir, "**")},
	}})
	m.RegisterHostMethod("host.fs.move", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"moved": true}, nil
	})

	moveParam := func(from, to string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"from": from, "to": to})
		return b
	}
	inFrom := filepath.Join(inDir, "a.txt")
	inTo := filepath.Join(inDir, "b.txt")

	t.Run("双侧都在 scope 内放行", func(t *testing.T) {
		if _, err := m.invokeHostMethod("test.fs.rw", "host.fs.move", moveParam(inFrom, inTo)); err != nil {
			t.Fatalf("双侧在 scope 内却被拒: %v", err)
		}
	})
	t.Run("目标越界拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod("test.fs.rw", "host.fs.move", moveParam(inFrom, outFile))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("目标越界未拒: %v", err)
		}
	})
	t.Run("源越界拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod("test.fs.rw", "host.fs.move", moveParam(outFile, inTo))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("源越界未拒: %v", err)
		}
	})
	t.Run("缺 to 参数不算权限错误", func(t *testing.T) {
		b, _ := json.Marshal(map[string]string{"from": inFrom})
		_, err := m.invokeHostMethod("test.fs.rw", "host.fs.move", b)
		if err == nil {
			t.Fatal("应报错")
		}
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrUnknownHostMethod) {
			t.Fatalf("参数错误被误分类: %v", err)
		}
	})
}

// TestFSHostMethodTablePin 锁定 host.fs.* 的声明表。
// 这张表是「默认拒绝」的依据，改动必须是有意的：
//   - 删条目 → 对应能力失去 scope 校验
//   - 加条目 → 必须有 services 侧实现，否则插件拿到的是执行期错误而非权限拒绝
//   - pathFields 为空 → 权限层根本没校验路径，等于白名单不存在
func TestFSHostMethodTablePin(t *testing.T) {
	want := map[string]string{
		"host.fs.read":   FSActionRead,
		"host.fs.list":   FSActionRead,
		"host.fs.stat":   FSActionRead,
		"host.fs.exists": FSActionRead,
		"host.fs.write":  FSActionWrite,
		"host.fs.mkdir":  FSActionWrite,
		"host.fs.remove": FSActionWrite,
		"host.fs.move":   FSActionWrite,
	}

	if len(fsHostMethods) != len(want) {
		t.Fatalf("声明表条目数变了: got %d, want %d", len(fsHostMethods), len(want))
	}
	for name, action := range want {
		spec, ok := fsHostMethods[name]
		if !ok {
			t.Errorf("声明表缺 %s（插件调用会被当未知方法拒绝）", name)
			continue
		}
		if spec.action != action {
			t.Errorf("%s 动作不符: got %s, want %s", name, spec.action, action)
		}
		if len(spec.pathFields) == 0 {
			t.Errorf("%s 未声明需要校验的路径参数——等于不做 scope 校验", name)
		}
	}
}

// root 返回一个已解析的临时目录，作为 scope 基准。
// 用解析后的路径：Windows 上 t.TempDir() 可能是 8.3 短名，与 EvalSymlinks 结果不一致。
func root(t *testing.T) string {
	t.Helper()
	real, err := ResolveRealPath(t.TempDir())
	if err != nil {
		t.Fatalf("准备临时目录失败: %v", err)
	}
	return real
}

// makeDirLink 在 link 处创建指向 target 的目录链接，环境不支持则跳过测试。
//
// Windows 上 os.Symlink 需要开发者模式或管理员权限，普通环境报
// "A required privilege is not held by the client"；此时退化为 mklink /J
// （目录联接，普通用户可建），它同样会被 filepath.EvalSymlinks 解析——
// 而这些用例验证的正是「解析」这一步，跳过等于核心安全断言没跑。
func makeDirLink(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.Symlink(target, link); err == nil {
			return
		}
		t.Skip("当前环境无法创建符号链接")
	}
	// Windows：直接走 mklink /J（目录联接），这正是 resolveLinks 经 os.Readlink
	// 解析的路径类型。os.Symlink 在本机（Go 1.13 + Windows）会产出「Lstat 打不开」
	// 的坏链接，故不优先使用它——测试用例关注的是联接穿越，而非 symlink 本身。
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("无法创建目录联接（需管理员或开发者模式）: %s", strings.TrimSpace(string(out)))
	}
}

// makeDanglingDirLink 制造指向不存在目标的链接：先建目标、建链接、再删目标。
// 不能直接建悬空链接——Windows 的 mklink /J 要求目标必须已存在。
func makeDanglingDirLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	makeDirLink(t, link, target)
	if err := os.Remove(target); err != nil {
		t.Fatalf("删除链接目标失败: %v", err)
	}
}
