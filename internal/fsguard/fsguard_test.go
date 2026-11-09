package fsguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 相对路径按会话工作目录补全，且该目录本身就是合法写入点。
// 这两件事必须同时成立：只补全不许写，等于「AI 能定位却写不进去」；
// 只允许不许补全，等于每个工具调用都得自己拼绝对路径。
func TestResolveWriteJoinsCwd(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveWrite(nil, "sub/a.txt", dir)
	if err != nil {
		t.Fatalf("绑定工作目录后应可写: %v", err)
	}
	// 期望值是解引用后的目录：Windows 上 t.TempDir() 给的是 8.3 短名
	// （C:\Users\MR12C5~1.LIA\…），而白名单里的根都会展开成长名。这条不写明，
	// 换台机器跑就会以为「路径补全错了」——实际是比对对象没对齐。
	realDir, rerr := filepath.EvalSymlinks(dir)
	if rerr != nil {
		realDir = dir
	}
	want := filepath.Join(realDir, "sub", "a.txt")
	if !strings.EqualFold(got, want) {
		t.Fatalf("相对路径补全错误:\n got=%s\nwant=%s", got, want)
	}
}

// 没绑定工作目录、库里也没有快捷项时，写操作必须给一条「去让用户选目录」的话，
// 而不是「路径不在范围内」——后者模型只会换个路径再试一次。
func TestResolveWriteWithoutCwd(t *testing.T) {
	_, err := ResolveWrite(nil, filepath.Join(t.TempDir(), "x.txt"), "")
	if err == nil {
		t.Skip("本机配置了快捷项目录，该路径可能确实可写")
	}
	if !strings.Contains(err.Error(), "工作目录") && !strings.Contains(err.Error(), "可写范围") {
		t.Fatalf("错误信息应指引用户授权: %v", err)
	}
}

// 写白名单里不能有家目录：用户允许 AI 读文件，不等于允许它改 ~/Documents。
// 这条不对称是整个写能力的底线，用测试钉住。
func TestWriteRootsExcludeHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("取不到家目录")
	}
	for _, r := range WriteRoots(nil, "") {
		if strings.EqualFold(filepath.Clean(r), filepath.Clean(home)) {
			t.Fatalf("家目录不该出现在写白名单里: %s", r)
		}
	}
}

// SkipDirs 里混着 Pods 这种首字母大写的键：只按小写查会永远匹配不到，
// 于是 iOS 工程一列目录就被 Pods 淹没。
func TestSkipDirCaseInsensitive(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"node_modules", true},
		{"NODE_MODULES", true},
		{"Pods", true},
		{"pods", true},
		{"src", false},
	}
	for _, c := range cases {
		if got := SkipDir(c.name); got != c.want {
			t.Fatalf("SkipDir(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// 绑定工作目录后，读必须限定在该目录内。
//
// 这条钉的是一个真实越界：用户贴了本地项目的报错，报错文本里带另一个项目的绝对路径，
// 模型顺着它把源码读了——从旧白名单看不算犯规（都在家目录里），但用户的心智是
// 「这次对话是这个项目」。所以这里同时断言「同一个越界文件在宽松路径下是能读的」，
// 否则测试在临时目录不在家目录的机器上会静默变成空断言。
func TestResolveReadScopedRejectsOutside(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "b.go")
	inside := filepath.Join(root, "a.go")

	if _, err := ResolveReadScoped(nil, inside, root, true); err != nil {
		t.Fatalf("工作目录内的文件应可读: %v", err)
	}
	if _, err := ResolveRead(nil, outside, "", true); err != nil {
		t.Skipf("临时目录不在宽松白名单内，本用例无法证明 scoped 更严: %v", err)
	}
	if _, err := ResolveReadScoped(nil, outside, root, true); err == nil {
		t.Fatalf("工作目录外的文件不该被读: %s", outside)
	}
	// 没绑定工作目录时要退回宽松白名单，否则没有目录的对话连随手提到的文件都读不了。
	if _, err := ResolveReadScoped(nil, outside, "", true); err != nil {
		t.Fatalf("未绑定工作目录时应退回宽松白名单: %v", err)
	}
}

// 用户当场同意的那一次读取必须能过闸，但只对那一次有效。
//
// 这是「默认只能操作当前目录」的配套通道：没有它，越界读取就只有拒绝一条路，
// 用户想让 AI 看一眼别处的文件只能先切工作目录。
func TestResolveReadScopedExtra(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "b.go")

	if _, err := ResolveReadScopedExtra(nil, outside, root, nil, true); err == nil {
		t.Fatal("没有授权时越界读取应被拒")
	}
	if _, err := ResolveReadScopedExtra(nil, outside, root, []string{outsideDir}, true); err != nil {
		t.Fatalf("用户同意后应放行这一次: %v", err)
	}
	// 授权的是那个目录，不是整个磁盘。
	if _, err := ResolveReadScopedExtra(nil, filepath.Join(t.TempDir(), "c.go"), root, []string{outsideDir}, true); err == nil {
		t.Fatal("授权目录之外的路径不该被放行")
	}
}

// Outside 只做判断不报错：宿主据此决定要不要弹确认卡。
func TestOutside(t *testing.T) {
	root := t.TempDir()
	if _, out := Outside(filepath.Join(root, "a.go"), root); out {
		t.Fatal("目录内的文件不该判为越界")
	}
	if _, out := Outside(filepath.Join(t.TempDir(), "b.go"), root); !out {
		t.Fatal("目录外的文件应判为越界")
	}
	if _, out := Outside(filepath.Join(t.TempDir(), "b.go"), ""); out {
		t.Fatal("没有声明范围时不该判越界")
	}
}

// 凭据类文件按名字判：内容判据要读完才知道，而读完就已经进了日志。
func TestSecretFile(t *testing.T) {
	yes := []string{"id_rsa", "/home/u/.ssh/id_ed25519", "a/b/server.pem", "cert.P12", ".git-credentials", "credentials.json"}
	for _, p := range yes {
		if !SecretFile(p) {
			t.Fatalf("%s 应判为凭据文件", p)
		}
	}
	no := []string{"main.go", "config.json", "keyboard.txt", "pem.md"}
	for _, p := range no {
		if SecretFile(p) {
			t.Fatalf("%s 不该判为凭据文件", p)
		}
	}
}
