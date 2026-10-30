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
	// 期望值是**解引用后**的目录：Windows 上 t.TempDir() 给的是 8.3 短名
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
