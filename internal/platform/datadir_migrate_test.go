package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateDataDirSkipsRebuildable 验证迁移的筛选规则。
//
// 现在的规则：**除数据库三件套外全部搬运**（含运行时/模块缓存/依赖树）。
// 早期版本跳过这些「可重建」内容并在迁移后重新下载；改成「打包 → 复制 → 解压」后，
// 跨盘搬运变成一次顺序读写，比逐文件复制快、也比重新下载快，于是不再跳过。
//
// 唯一仍然排除的是数据库三件套：正被 SQLite 独占，普通复制会得到撕裂快照。
func TestMigrateDataDirSkipsRebuildable(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "new")

	// 核心数据：必须被搬走
	mkFile(t, filepath.Join(src, "plugins", "demo", "main.js"), "plugin")
	mkFile(t, filepath.Join(src, "images", "a.png"), "png")
	mkFile(t, filepath.Join(src, "icons", "i.ico"), "ico")
	mkFile(t, filepath.Join(src, "env", "settings.json"), "{}")

	// 以前会被跳过的环境数据：现在**必须**一起搬走
	mkFile(t, filepath.Join(src, "runtime", "go", "1.27.2", "bin", "go.exe"), "goexe")
	mkFile(t, filepath.Join(src, "runtime", "go", "1.27.2", "gopath", "pkg", "mod", "x.go"), "mod")
	mkFile(t, filepath.Join(src, "runtime", "node", "v22.22.2", "node.exe"), "node")
	mkFile(t, filepath.Join(src, "runtime", "git", "cmd", "git.exe"), "git")
	mkFile(t, filepath.Join(src, "gopath", "pkg", "mod", "y.go"), "rootmod")
	mkFile(t, filepath.Join(src, "dsh", "node_modules", "pkg", "index.js"), "nm")

	// 必须搬走、但旧清单曾误判为可跳过的内容：
	//   - dsh_token 是授权令牌，跳过等于强制用户重新登录
	//   - gopath/src 是用户的 Go 工作区代码，丢了不可再生
	//   - runtime/everything 是 104MB 的独立可执行文件，重装链路不覆盖它
	mkFile(t, filepath.Join(src, "dsh_token"), "token")
	mkFile(t, filepath.Join(src, "gopath", "src", "myproj", "main.go"), "usercode")
	mkFile(t, filepath.Join(src, "runtime", "everything", "Everything.exe"), "everything")

	// 数据库三件套：既不复制也不清理（被 SQLite 独占，另行处理）
	mkFile(t, filepath.Join(src, "quickdock.db"), "db-content")
	mkFile(t, filepath.Join(src, "quickdock.db-wal"), "wal")
	mkFile(t, filepath.Join(src, "quickdock.db-shm"), "shm")

	var phases []string
	err := MigrateDataDir(src, dst, func(p MigrateProgress) {
		phases = append(phases, p.Phase)
	})
	if err != nil {
		t.Fatalf("MigrateDataDir 失败: %v", err)
	}

	// 全部内容都到了新目录（核心数据 + 环境数据）
	for _, rel := range []string{
		filepath.Join("plugins", "demo", "main.js"),
		filepath.Join("images", "a.png"),
		filepath.Join("icons", "i.ico"),
		filepath.Join("env", "settings.json"),
		"dsh_token",
		filepath.Join("gopath", "src", "myproj", "main.go"),
		filepath.Join("runtime", "everything", "Everything.exe"),
		filepath.Join("runtime", "go", "1.27.2", "bin", "go.exe"),
		filepath.Join("runtime", "go", "1.27.2", "gopath", "pkg", "mod", "x.go"),
		filepath.Join("runtime", "node", "v22.22.2", "node.exe"),
		filepath.Join("runtime", "git", "cmd", "git.exe"),
		filepath.Join("gopath", "pkg", "mod", "y.go"),
		filepath.Join("dsh", "node_modules", "pkg", "index.js"),
	} {
		if !exists(filepath.Join(dst, rel)) {
			t.Errorf("内容未迁移: %s", rel)
		}
	}

	// 数据库文件既没被复制（快照另走 VACUUM INTO），也没被删除（句柄占用中）
	for _, rel := range []string{"quickdock.db", "quickdock.db-wal", "quickdock.db-shm"} {
		if exists(filepath.Join(dst, rel)) {
			t.Errorf("数据库文件不该被普通复制: %s", rel)
		}
		if !exists(filepath.Join(src, rel)) {
			t.Errorf("数据库文件不该在迁移阶段被删除: %s", rel)
		}
	}

	// 旧目录中已复制的内容被清理（磁盘占用不翻倍）
	for _, rel := range []string{
		filepath.Join("plugins", "demo", "main.js"),
		filepath.Join("images", "a.png"),
		"dsh_token",
		filepath.Join("gopath", "src", "myproj", "main.go"),
		filepath.Join("runtime", "everything", "Everything.exe"),
		filepath.Join("runtime", "go", "1.27.2", "bin", "go.exe"),
		filepath.Join("dsh", "node_modules", "pkg", "index.js"),
	} {
		if exists(filepath.Join(src, rel)) {
			t.Errorf("旧目录已复制内容未清理: %s", rel)
		}
	}

	// 进度事件包含关键阶段
	hasCopying := false
	for _, p := range phases {
		if p == MigratePhaseCopying {
			hasCopying = true
		}
	}
	if !hasCopying {
		t.Errorf("进度回调缺少 copying 阶段: %v", phases)
	}
}

// TestReplaceBytesPath 覆盖数据目录路径改写的边界。
//
// 重点场景：
//   - 内容里写的是**历史遗留**的旧目录（不是本次迁移的 oldDir），也要被替换
//   - `.quickdock-backup` 这类别的目录名不能被误伤
//   - 分隔符混用（/ 与 \）都要认
func TestReplaceBytesPath(t *testing.T) {
	newDir := `D:\.quickdock`
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "历史遗留的旧目录路径",
			in:   "GOPATH=C:\\Users\\Mr.liang\\.quickdock\\runtime\\go\\1.27.2\\gopath\n",
			want: "GOPATH=D:\\.quickdock\\runtime\\go\\1.27.2\\gopath\n",
		},
		{
			name: "正斜杠写法",
			in:   "GOPATH=C:/Users/Mr.liang/.quickdock/runtime/go/1.27.2/gopath\n",
			want: "GOPATH=D:\\.quickdock/runtime/go/1.27.2/gopath\n",
		},
		{
			name: "小写盘符",
			in:   "GOPATH=c:\\users\\mr.liang\\.quickdock\\gopath\n",
			want: "GOPATH=D:\\.quickdock\\gopath\n",
		},
		{
			name: "别的目录名不能误伤",
			in:   "PATH=C:\\Tools\\.quickdock-backup\\bin\n",
			want: "PATH=C:\\Tools\\.quickdock-backup\\bin\n",
		},
		{
			name: "非路径文本不动",
			in:   "GOPROXY=https://goproxy.cn,direct\n",
			want: "GOPROXY=https://goproxy.cn,direct\n",
		},
	}
	for _, c := range cases {
		got := string(replaceBytesPath([]byte(c.in), `C:\Users\Mr.liang\.quickdock`, newDir))
		if got != c.want {
			t.Errorf("%s:\n 输入 %q\n 期望 %q\n 实际 %q", c.name, c.in, c.want, got)
		}
	}
}

// TestCleanupOldDataDirKeepsDatabase 验证旧目录清理：删掉已搬走的内容，
// 但**保留数据库三件套**（正被 SQLite 独占，重启后还要靠它们做 VACUUM INTO 导出）。
func TestCleanupOldDataDirKeepsDatabase(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "plugins", "demo", "main.js"), "x")
	mkFile(t, filepath.Join(dir, "runtime", "go", "1.27.2", "bin", "go.exe"), "x")
	mkFile(t, filepath.Join(dir, "quickdock.db"), "x")
	mkFile(t, filepath.Join(dir, "quickdock.db-wal"), "x")
	mkFile(t, filepath.Join(dir, "quickdock.db-shm"), "x")

	if err := CleanupOldDataDir(dir, true); err != nil {
		t.Fatalf("CleanupOldDataDir 失败: %v", err)
	}

	// 已搬走的内容删掉了
	for _, rel := range []string{
		filepath.Join("plugins", "demo", "main.js"),
		filepath.Join("runtime", "go", "1.27.2", "bin", "go.exe"),
	} {
		if exists(filepath.Join(dir, rel)) {
			t.Errorf("已搬走的内容未清理: %s", rel)
		}
	}
	// 数据库三件套保留
	for _, rel := range []string{"quickdock.db", "quickdock.db-wal", "quickdock.db-shm"} {
		if !exists(filepath.Join(dir, rel)) {
			t.Errorf("数据库文件不该在清理阶段被删除: %s", rel)
		}
	}
}

// TestArchiveDataDirRewritesPaths 验证打包时会改写写死旧目录绝对路径的文本配置。
//
// 背景：runtime/go/<ver>/gopath/env 里写的是 GOPATH=<旧数据目录>\runtime\go\...。
// 整体搬到新盘后若不改写，Go 会把模块缓存继续写回旧目录——等于只搬了一半。
func TestArchiveDataDirRewritesPaths(t *testing.T) {
	oldDir := t.TempDir()
	newDir := filepath.Join(t.TempDir(), "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 写死旧路径的 Go 用户环境文件
	mkFile(t, filepath.Join(oldDir, "runtime", "go", "1.27.2", "gopath", "env"),
		"GOPATH="+filepath.Join(oldDir, "runtime", "go", "1.27.2", "gopath")+"\nGOPROXY=https://goproxy.cn,direct\n")
	// 普通文件（不含路径，不该被改）
	mkFile(t, filepath.Join(oldDir, "plugins", "demo", "main.js"), "console.log(1)")

	zipPath := filepath.Join(newDir, ArchiveName)
	if _, _, err := ArchiveDataDirWithRewrite(oldDir, zipPath, oldDir, newDir, nil); err != nil {
		t.Fatalf("ArchiveDataDirWithRewrite 失败: %v", err)
	}
	if err := ExtractDataDir(zipPath, newDir, nil); err != nil {
		t.Fatalf("ExtractDataDir 失败: %v", err)
	}

	// env 文件里的旧路径应被换成新路径
	b, err := os.ReadFile(filepath.Join(newDir, "runtime", "go", "1.27.2", "gopath", "env"))
	if err != nil {
		t.Fatalf("读取解压后的 env 失败: %v", err)
	}
	got := string(b)
	if strings.Contains(got, oldDir) {
		t.Errorf("env 文件仍含旧目录路径:\n%s", got)
	}
	// 期望值里的分隔符要与写回时一致（Windows 上是反斜杠）。
	want := "GOPATH=" + strings.ReplaceAll(filepath.Join(newDir, "runtime", "go", "1.27.2", "gopath"), "/", `\`)
	if !strings.Contains(got, want) {
		t.Errorf("env 文件未写入新目录路径，期望包含 %q:\n%s", want, got)
	}
	// 无关内容必须保持原样
	if !strings.Contains(got, "GOPROXY=https://goproxy.cn,direct") {
		t.Errorf("改写破坏了 env 文件的其它内容:\n%s", got)
	}
}

// TestArchiveDataDirSkipsArchiveItself 验证打包时跳过压缩包自身。
//
// 旧目录里可能残留着上次迁移的 zip；把它打进包里，解压时会覆盖正在读取的
// 目标文件（Windows 上直接报「另一个程序正在使用此文件」）。
func TestArchiveDataDirSkipsArchiveItself(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "new")
	mkFile(t, filepath.Join(src, "plugins", "demo", "main.js"), "x")
	// 在源目录里放一个同名 zip，模拟残留
	mkFile(t, filepath.Join(src, ArchiveName), "stale")

	zipPath := filepath.Join(dst, ArchiveName)
	n, _, err := ArchiveDataDir(src, zipPath, nil)
	if err != nil {
		t.Fatalf("ArchiveDataDir 失败: %v", err)
	}
	if n != 1 {
		t.Errorf("打包条目数 = %d, 期望 1（压缩包自身应被跳过）", n)
	}
}

// TestNormalizeTargetDir 覆盖用户选路径的规范化规则。
func TestNormalizeTargetDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{"D:", `D:\.quickdock`},
		{`D:\`, `D:\.quickdock`},
		{`D:\data`, `D:\data\.quickdock`},
		{`D:\data\.quickdock`, `D:\data\.quickdock`},
		{`D:\.quickdock`, `D:\.quickdock`},
	}
	for _, c := range cases {
		if got := NormalizeTargetDir(c.in); !strings.EqualFold(got, c.want) {
			t.Errorf("NormalizeTargetDir(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestCleanupRebuildableDirs 验证可重建内容清理：只删可重建的，不碰别的。
func TestCleanupRebuildableDirs(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, filepath.Join(dir, "runtime", "go", "1.27.2", "bin", "go.exe"), "x")
	mkFile(t, filepath.Join(dir, "runtime", "node", "v22", "node.exe"), "x")
	mkFile(t, filepath.Join(dir, "dsh", "node_modules", "p", "i.js"), "x")
	mkFile(t, filepath.Join(dir, "gopath", "pkg", "mod", "m.go"), "x")
	mkFile(t, filepath.Join(dir, "plugins", "demo", "main.js"), "x")

	if err := CleanupRebuildableDirs(dir); err != nil {
		t.Fatalf("CleanupRebuildableDirs 失败: %v", err)
	}

	for _, rel := range []string{
		filepath.Join("runtime", "go"),
		filepath.Join("runtime", "node"),
		filepath.Join("dsh", "node_modules"),
		filepath.Join("gopath", "pkg"),
	} {
		if exists(filepath.Join(dir, rel)) {
			t.Errorf("可重建内容未清理: %s", rel)
		}
	}
	if !exists(filepath.Join(dir, "plugins", "demo", "main.js")) {
		t.Errorf("非可重建内容被误删")
	}
}

// TestIsRebuildable 覆盖「可重建」判定。
//
// 现在这张清单**只用于旧目录清理**（搬完之后把旧目录里的对应内容删掉），
// 不再用于「跳过搬运」——迁移改成整体打包搬运后，什么都不跳。
// 所以判定本身仍需正确：判错会导致旧目录里该删的没删（占用翻倍）
// 或不该删的被删（用户丢数据）。
func TestIsRebuildable(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"runtime/go/1.27.2/bin/go.exe", true},
		{"runtime/go/1.27.2/gopath/pkg/mod/x", true},
		{"runtime/node/v22/node.exe", true},
		{"runtime/git/cmd/git.exe", true},
		{"gopath/pkg/mod/y", true},
		{"dsh/node_modules/p/i.js", true},
		{"quickdock.db", false},
		{"plugins/demo/main.js", false},
		{"images/a.png", false},
		{"runtime", false},   // runtime 目录本身要建，只跳过其下已知子目录
		{"runtime/go", true}, // 目录本身也跳过，避免目标目录留空壳
		{"runtime/go/1.27.2", true},
		{"runtime/other/keep.txt", false}, // runtime 下未知子目录要照常搬
		// 以下三类旧清单判错过，会造成真实的数据丢失：
		{"dsh_token", false},                         // 授权令牌：跳过等于强制重新登录
		{"gopath/src/myproj/main.go", false},         // 用户工作区代码：丢了不可再生
		{"runtime/everything/Everything.exe", false}, // 重装链路不覆盖它
	}
	for _, c := range cases {
		if got := isRebuildable(filepath.ToSlash(c.rel)); got != c.want {
			t.Errorf("isRebuildable(%q) = %v, 期望 %v", c.rel, got, c.want)
		}
	}
}

// TestIsDatabaseFile 覆盖数据库三件套的识别。
func TestIsDatabaseFile(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"quickdock.db", true},
		{"quickdock.db-wal", true},
		{"quickdock.db-shm", true},
		{"quickdock.db.bak", false},
		{"plugins/demo/main.js", false},
		{"db/quickdock.db", false}, // 只认数据目录根下的那一个
	}
	for _, c := range cases {
		if got := isDatabaseFile(filepath.ToSlash(c.rel)); got != c.want {
			t.Errorf("isDatabaseFile(%q) = %v, 期望 %v", c.rel, got, c.want)
		}
	}
}

func mkFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
