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

// ErrChallengeRequired 表示交互式问答回调没能给出与问题一一对应的答案。
// 调用方用 errors.Is 判断「前端与服务端问答协议对不上」：这不等于密码错误，
// 不该当成认证失败去重试，否则会被服务端记成暴力尝试。
var ErrChallengeRequired = errors.New("sshx: 需要交互式问答答案")

// AuthSpec 认证参数。密码与私钥口令由调用方从数据库解密后传入，本包不碰存储。
type AuthSpec struct {
	Type          AuthType
	Password      string
	KeyPath       string
	KeyPassphrase string

	// Challenge 交互式问答回调。nil 时退回「所有问题回同一个密码」的旧行为。
	Challenge func(name, instruction string, questions []string, echos []bool) ([]string, error)
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
			keyboardInteractive(spec),
			ssh.Password(spec.Password),
		}, nil

	default:
		return nil, fmt.Errorf("sshx: 未知的认证类型 %q", spec.Type)
	}
}

// keyboardInteractive 构造 keyboard-interactive 认证方法。
//
// 为什么 Challenge == nil 必须保留「所有问题回同一个密码」的旧行为：
// Exec、SFTP、批量执行、端口转发、MCP 这些非交互路径都没有 UI 能弹窗问用户，
// 一旦在这里等回调就会把整条连接卡死。回调只由确实有交互能力的调用方
// （例如终端登录）显式注入；没注入就代表当前路径不支持多轮问答，只能退回旧行为。
//
// 另外 OTP / PAM 是多轮问答：服务端会先问 "Password:" 再问 "Verification code:"，
// 甚至分多轮下发。绝不能把同一个密码重复填进第二问——那会被判为认证失败，
// 连续几次还会触发账号锁定或暴力尝试告警。因此有回调时答案必须由回调按
// questions 逐条给出，且条数必须严格相等，缺一不能擅自补齐（长度不符说明
// 前端与服务端的问答协议不一致，自行补答案只会把错答案喂给服务端）。
func keyboardInteractive(spec AuthSpec) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		// 没有问题就不必打扰回调，直接回一个空 slice。
		if len(questions) == 0 {
			return []string{}, nil
		}

		if spec.Challenge == nil {
			// 兼容旧行为：未注入回调时，所有问题都回同一个密码。
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = spec.Password
			}
			return answers, nil
		}

		answers, err := spec.Challenge(name, instruction, questions, echos)
		if err != nil {
			return nil, err
		}
		if len(answers) != len(questions) {
			return nil, ErrChallengeRequired
		}
		return answers, nil
	})
}

// keyboardInteractivePassword 保留旧函数名，等价于 Challenge 为 nil 的
// keyboardInteractive；给只想用纯密码、不注入回调的调用点使用，也避免破坏
// 既有测试对该名字的引用。
func keyboardInteractivePassword(password string) ssh.AuthMethod {
	return keyboardInteractive(AuthSpec{Password: password})
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
