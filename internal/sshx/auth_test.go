package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// writeTestKey 生成一个临时私钥文件，用于验证私钥解析路径。
// passphrase 非空时写出加密私钥。
func writeTestKey(t *testing.T, passphrase string) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}

	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "quickdock-test", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "quickdock-test")
	}
	if err != nil {
		t.Fatalf("序列化测试密钥失败: %v", err)
	}

	path := filepath.Join(t.TempDir(), "id_ed25519")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建测试密钥文件失败: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, block); err != nil {
		t.Fatalf("写入测试密钥失败: %v", err)
	}
	return path
}

func TestBuildAuthMethodsPassword(t *testing.T) {
	// 密码登录必须同时给出 keyboard-interactive 与 password 两种方法：
	// 国内云主机常见只开前者，只做 password 会「密码对但连不上」。
	methods, err := buildAuthMethods(AuthSpec{Type: AuthPassword, Password: "secret"})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(methods) != 2 {
		t.Fatalf("期望 2 种认证方法（keyboard-interactive + password），实际 %d", len(methods))
	}
}

func TestBuildAuthMethodsPasswordMissing(t *testing.T) {
	_, err := buildAuthMethods(AuthSpec{Type: AuthPassword})
	if !errors.Is(err, ErrNoAuthMethod) {
		t.Fatalf("空密码应返回 ErrNoAuthMethod，实际: %v", err)
	}
}

func TestBuildAuthMethodsKey(t *testing.T) {
	path := writeTestKey(t, "")
	methods, err := buildAuthMethods(AuthSpec{Type: AuthKey, KeyPath: path})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("期望 1 种认证方法，实际 %d", len(methods))
	}
}

func TestBuildAuthMethodsKeyEncrypted(t *testing.T) {
	path := writeTestKey(t, "pp")

	// 不给口令：必须给出「有口令保护」这种可操作的提示，而不是内部错误。
	_, err := buildAuthMethods(AuthSpec{Type: AuthKey, KeyPath: path})
	if err == nil {
		t.Fatal("加密私钥未提供口令时应报错")
	}
	if !strings.Contains(err.Error(), "口令") {
		t.Fatalf("错误信息应提示口令问题，实际: %v", err)
	}

	// 给对口令：应成功。
	if _, err := buildAuthMethods(AuthSpec{Type: AuthKey, KeyPath: path, KeyPassphrase: "pp"}); err != nil {
		t.Fatalf("提供正确口令后应成功，实际: %v", err)
	}

	// 给错口令：应失败。
	if _, err := buildAuthMethods(AuthSpec{Type: AuthKey, KeyPath: path, KeyPassphrase: "wrong"}); err == nil {
		t.Fatal("错误口令应报错")
	}
}

func TestBuildAuthMethodsKeyPathMissing(t *testing.T) {
	_, err := buildAuthMethods(AuthSpec{Type: AuthKey})
	if err == nil || !strings.Contains(err.Error(), "私钥路径") {
		t.Fatalf("缺少私钥路径应报错，实际: %v", err)
	}
}

func TestBuildAuthMethodsUnknownType(t *testing.T) {
	_, err := buildAuthMethods(AuthSpec{Type: AuthType("gssapi")})
	if err == nil {
		t.Fatal("未知认证类型应报错")
	}
}

func TestLoadSignerNotFound(t *testing.T) {
	_, err := loadSigner(filepath.Join(t.TempDir(), "not-exist"), "")
	if err == nil {
		t.Fatal("文件不存在时应报错")
	}
}

func TestExpandHome(t *testing.T) {
	got, err := expandHome("~/.ssh/id_ed25519")
	if err != nil {
		t.Fatalf("展开 ~ 失败: %v", err)
	}
	if strings.HasPrefix(got, "~") {
		t.Fatalf("~ 未被展开: %s", got)
	}
	if !strings.HasSuffix(got, filepath.Join(".ssh", "id_ed25519")) {
		t.Fatalf("展开结果异常: %s", got)
	}

	// 非 ~ 开头的路径必须原样返回
	const abs = "/tmp/whatever"
	if out, _ := expandHome(abs); out != abs {
		t.Fatalf("普通路径不应被改写，得到: %s", out)
	}
}
