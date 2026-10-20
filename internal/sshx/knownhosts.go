package sshx

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// 主机指纹校验（TOFU：Trust On First Use）。
//
// 设计要点：
//
//  1. 指纹按「主机 + 端口 + 密钥类型」三元组存储。同一台服务器通常会同时提供
//     ed25519 / ecdsa / rsa 多把主机密钥，具体用哪把取决于双方的算法协商结果。
//     只按「主机 + 端口」存一条会出问题：用户打开「兼容旧设备」后协商到 ssh-rsa，
//     就会被误判成「指纹变了」——这种假警报喊多了，真警报就没人看了。
//
//  2. 指纹只在同类型密钥之间比较才有意义。服务器提供了一个此前没记录过的类型，
//     说明这是一把全新的密钥（无法与旧记录做密码学比较），按「首次连接」处理并要求
//     用户确认，而不是直接判为「变更」。
//
//  3. 任何存储读取失败都直接中断握手（fail closed）。宁可连不上，也不能在无法
//     确认身份的情况下建立加密通道。

// 指纹校验相关错误。
var (
	// ErrUnknownHostKey 首次连接某主机，或该主机提供了一个此前未记录过的密钥类型。
	ErrUnknownHostKey = errors.New("sshx: 该服务器的主机密钥尚未被信任")
	// ErrHostKeyChanged 同类型主机密钥的指纹与本地记录不一致，可能是中间人攻击。
	ErrHostKeyChanged = errors.New("sshx: 主机密钥已变更，连接已中止")
	// ErrNoPendingHostKey 没有待确认的密钥。必须先发起一次连接并收到指纹，才允许信任，
	// 避免前端凭任意字符串就能把一把没见过的密钥写进白名单。
	ErrNoPendingHostKey = errors.New("sshx: 没有待确认的主机密钥，请先发起一次连接")
	// ErrFingerprintMismatch 调用方要求信任的指纹与本次握手观察到的指纹不一致。
	ErrFingerprintMismatch = errors.New("sshx: 要信任的指纹与本次连接观察到的不一致")
	// ErrKnownHostStoreMissing 未配置存储，指纹校验无法进行。
	ErrKnownHostStoreMissing = errors.New("sshx: 未配置主机密钥存储，无法校验服务器身份")
)

// KnownHost 一条已信任的主机密钥记录。
type KnownHost struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	KeyType     string `json:"keyType"`     // 如 ssh-ed25519
	Fingerprint string `json:"fingerprint"` // SHA256:xxx
	AddedAt     int64  `json:"addedAt"`     // Unix 毫秒，与仓库其它表保持一致
}

// ObservedHostKey 本次握手实际观察到的密钥（尚未信任）。
type ObservedHostKey struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	ObservedAt  int64  `json:"observedAt"`
}

// HostKeyStore 主机密钥记录的持久化抽象。
//
// 由 internal/db 实现（见 ssh_host.go）；本包只定义形状，不引数据库依赖，
// 这样引擎层可以脱离存储单独做单元测试。
type HostKeyStore interface {
	// ListKnownHosts 返回该主机已记录的全部密钥（可能多种类型，也可能为空）。
	ListKnownHosts(host string, port int) ([]KnownHost, error)
	// SaveKnownHost 写入一条记录；同 (host, port, keyType) 视为覆盖。
	SaveKnownHost(kh KnownHost) error
	// DeleteKnownHosts 清除该主机的全部记录（UI 的「清除已信任指纹」）。
	DeleteKnownHosts(host string, port int) error
}

// UnknownHostKeyError 首连待确认。Unwrap 到 ErrUnknownHostKey，
// 上层既可用 errors.Is 判类别，也可用 errors.As 取指纹给弹窗展示。
type UnknownHostKeyError struct {
	Host        string
	Port        int
	KeyType     string
	Fingerprint string
	// KnownTypes 该主机已记录过的密钥类型，非空说明这是「新算法」而不是首连，
	// 文案要区别对待（「服务器提供了新的主机密钥类型」）。
	KnownTypes []string
}

func (e *UnknownHostKeyError) Error() string {
	if len(e.KnownTypes) > 0 {
		return fmt.Sprintf("%s（%s:%d 提供了新的密钥类型 %s，已记录: %s）",
			ErrUnknownHostKey, e.Host, e.Port, e.KeyType, strings.Join(e.KnownTypes, ", "))
	}
	return fmt.Sprintf("%s（%s:%d %s %s）", ErrUnknownHostKey, e.Host, e.Port, e.KeyType, e.Fingerprint)
}

func (e *UnknownHostKeyError) Unwrap() error { return ErrUnknownHostKey }

// HostKeyChangedError 同类型密钥指纹不一致，属于高危事件。
type HostKeyChangedError struct {
	Host           string
	Port           int
	KeyType        string
	OldFingerprint string
	NewFingerprint string
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("%s（%s:%d 的 %s 指纹由 %s 变为 %s）",
		ErrHostKeyChanged, e.Host, e.Port, e.KeyType, e.OldFingerprint, e.NewFingerprint)
}

func (e *HostKeyChangedError) Unwrap() error { return ErrHostKeyChanged }

// KnownHosts 指纹校验器。整个进程共用一个实例即可，内部并发安全。
type KnownHosts struct {
	store HostKeyStore

	mu      sync.Mutex
	pending map[string]ObservedHostKey // key: host:port
}

// NewKnownHosts 创建校验器。store 为 nil 时所有连接都会被拒绝（fail closed）。
func NewKnownHosts(store HostKeyStore) *KnownHosts {
	return &KnownHosts{store: store, pending: make(map[string]ObservedHostKey)}
}

// Callback 返回绑定到具体目标的 ssh.HostKeyCallback。
//
// 用配置里的 host/port 做记录键，而不是回调参数里的 hostname：握手时传进来的是
// 拨号地址（"host:port" 字符串），直接拿它当键会把端口混进主机名，且与数据库里的
// 记录对不上。
func (k *KnownHosts) Callback(host string, port int) ssh.HostKeyCallback {
	h, p := normalizeTarget(host, port)
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		return k.verify(h, p, key)
	}
}

// Verify 与 Callback 等价，供不经过 ssh 握手的单元测试与「测试连接」复用。
func (k *KnownHosts) Verify(host string, port int, key ssh.PublicKey) error {
	h, p := normalizeTarget(host, port)
	return k.verify(h, p, key)
}

func (k *KnownHosts) verify(host string, port int, key ssh.PublicKey) error {
	if k == nil || k.store == nil {
		return ErrKnownHostStoreMissing
	}
	if key == nil {
		// 正常握手不会走到这里；返回错误而不是放行，避免任何「无密钥」路径被当成可信。
		return errors.New("sshx: 服务器未提供主机密钥")
	}

	keyType := key.Type()
	fp := ssh.FingerprintSHA256(key)

	known, err := k.store.ListKnownHosts(host, port)
	if err != nil {
		// fail closed：查不到记录就不能证明对方身份，宁可连不上。
		return fmt.Errorf("sshx: 读取主机密钥记录失败: %w", err)
	}

	knownTypes := make([]string, 0, len(known))
	var sameType *KnownHost
	for i := range known {
		knownTypes = append(knownTypes, known[i].KeyType)
		if known[i].KeyType == keyType {
			sameType = &known[i]
		}
	}

	if sameType != nil {
		if sameType.Fingerprint == fp {
			return nil
		}
		k.recordPending(host, port, keyType, fp)
		return &HostKeyChangedError{
			Host:           host,
			Port:           port,
			KeyType:        keyType,
			OldFingerprint: sameType.Fingerprint,
			NewFingerprint: fp,
		}
	}

	// 记录到 pending 再报错：用户确认后由 Trust 落库，避免「先信任一个没见过的指纹」。
	k.recordPending(host, port, keyType, fp)
	return &UnknownHostKeyError{
		Host:        host,
		Port:        port,
		KeyType:     keyType,
		Fingerprint: fp,
		KnownTypes:  knownTypes,
	}
}

func (k *KnownHosts) recordPending(host string, port int, keyType, fp string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pending == nil {
		k.pending = make(map[string]ObservedHostKey)
	}
	k.pending[targetKey(host, port)] = ObservedHostKey{
		Host:        host,
		Port:        port,
		KeyType:     keyType,
		Fingerprint: fp,
		ObservedAt:  time.Now().UnixMilli(),
	}
}

// Pending 返回待确认的密钥（前端弹窗展示用）。
func (k *KnownHosts) Pending(host string, port int) (ObservedHostKey, bool) {
	if k == nil {
		return ObservedHostKey{}, false
	}
	h, p := normalizeTarget(host, port)
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.pending[targetKey(h, p)]
	return v, ok
}

// Forget 丢弃待确认的密钥（用户点了「取消」）。
func (k *KnownHosts) Forget(host string, port int) {
	if k == nil {
		return
	}
	h, p := normalizeTarget(host, port)
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.pending, targetKey(h, p))
}

// Trust 把待确认的密钥写入存储。
//
// fingerprint 必须与本次握手观察到的值一致：用户看到的就是这个指纹，
// 传别的值说明前端状态错乱或有注入，直接拒绝。
// 指纹变更后重新信任也走这里——会覆盖同类型旧记录，所以前端必须先做强告警。
func (k *KnownHosts) Trust(host string, port int, fingerprint string) (KnownHost, error) {
	if k == nil || k.store == nil {
		return KnownHost{}, ErrKnownHostStoreMissing
	}
	h, p := normalizeTarget(host, port)

	obs, ok := k.Pending(h, p)
	if !ok {
		return KnownHost{}, ErrNoPendingHostKey
	}
	if strings.TrimSpace(fingerprint) == "" {
		return KnownHost{}, fmt.Errorf("%w: 指纹不能为空", ErrFingerprintMismatch)
	}
	if strings.TrimSpace(fingerprint) != obs.Fingerprint {
		return KnownHost{}, fmt.Errorf("%w（期望 %s，实际 %s）",
			ErrFingerprintMismatch, obs.Fingerprint, fingerprint)
	}

	rec := KnownHost{
		Host:        h,
		Port:        p,
		KeyType:     obs.KeyType,
		Fingerprint: obs.Fingerprint,
		AddedAt:     time.Now().UnixMilli(),
	}
	if err := k.store.SaveKnownHost(rec); err != nil {
		return KnownHost{}, fmt.Errorf("sshx: 保存主机密钥失败: %w", err)
	}
	k.Forget(h, p)
	return rec, nil
}

// List 返回已信任的密钥记录，供设置页展示与「清除指纹」使用。
func (k *KnownHosts) List(host string, port int) ([]KnownHost, error) {
	if k == nil || k.store == nil {
		return nil, ErrKnownHostStoreMissing
	}
	h, p := normalizeTarget(host, port)
	return k.store.ListKnownHosts(h, p)
}

// RemoveAll 清除该主机的全部已信任记录。
func (k *KnownHosts) RemoveAll(host string, port int) error {
	if k == nil || k.store == nil {
		return ErrKnownHostStoreMissing
	}
	h, p := normalizeTarget(host, port)
	if err := k.store.DeleteKnownHosts(h, p); err != nil {
		return fmt.Errorf("sshx: 清除主机密钥失败: %w", err)
	}
	k.Forget(h, p)
	return nil
}

// normalizeTarget 统一主机名与端口的写法。
//
// 大小写与首尾空格会让同一个主机产生两条记录，后面就会出现「明明信任过又弹窗」。
func normalizeTarget(host string, port int) (string, int) {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]") // IPv6 字面量常写成 [::1]
	host = strings.ToLower(host)
	if port <= 0 || port > 65535 {
		port = defaultPort
	}
	return host, port
}

func targetKey(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}
