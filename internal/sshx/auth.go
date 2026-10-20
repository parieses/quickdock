package sshx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// AuthType 认证类型。
type AuthType string

const (
	// AuthPassword 密码登录。实际会同时提供 keyboard-interactive 与 password 两种方法。
	AuthPassword AuthType = "password"
	// AuthKey 私钥登录。
	AuthKey AuthType = "key"
)

// AuthSpec 认证参数。密码与私钥口令由调用方从数据库解密后传入，本包不碰存储。
type AuthSpec struct {
	Type          AuthType
	Password      string
	KeyPath       string
	KeyPassphrase string
}

// buildAuthMethods 构造认证方法链。
//
// 密码登录刻意返回两种方法，顺序是 keyboard-interactive 在前：
// 阿里云/腾讯云等大量主机关闭了 password 只开 keyboard-interactive，
// 只做 password 会得到「密码明明正确却连不上」这种最难排查的失败。
func buildAuthMethods(spec AuthSpec) ([]ssh.AuthMethod, error) {
	switch spec.Type {
	case AuthKey:
		if strings.TrimSpace(spec.KeyPath) == "" {
			return nil, errors.New("sshx: 私钥认证缺少私钥路径")
		}
		signer, err := loadSigner(spec.KeyPath, spec.KeyPassphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil

	case AuthPassword, "":
		if spec.Password == "" {
			return nil, ErrNoAuthMethod
		}
		return []ssh.AuthMethod{
			keyboardInteractivePassword(spec.Password),
			ssh.Password(spec.Password),
		}, nil

	default:
		return nil, fmt.Errorf("sshx: 未知的认证类型 %q", spec.Type)
	}
}

// keyboardInteractivePassword 对所有提问都回同一个密码。
// 常见服务器只问一条 "Password:"，但少数会二次确认，所以按 questions 长度填充而不是固定返回一条。
func keyboardInteractivePassword(password string) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range answers {
			answers[i] = password
		}
		return answers, nil
	})
}

// loadSigner 读取并解析私钥文件。
func loadSigner(path, passphrase string) (ssh.Signer, error) {
	resolved, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("sshx: 读取私钥失败: %w", err)
	}

	var signer ssh.Signer
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(raw)
	}
	if err != nil {
		// 加密私钥在没给口令时返回的就是 PassphraseMissingError，
		// 直接透出去会让前端显示一串英文内部错误，这里换成可操作的中文提示。
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, errors.New("sshx: 该私钥有口令保护，请提供私钥口令")
		}
		return nil, fmt.Errorf("sshx: 解析私钥失败（口令错误或格式不支持）: %w", err)
	}
	return signer, nil
}

// expandHome 展开路径开头的 ~（Windows 与 Unix 一致处理）。
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sshx: 无法解析用户主目录: %w", err)
	}
	rest := strings.TrimPrefix(path, "~")
	rest = strings.TrimLeft(rest, `/\`)
	if rest == "" {
		return home, nil
	}
	return filepath.Join(home, rest), nil
}
