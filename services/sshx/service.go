// Package sshx 是 SSH 远程连接的门面服务：主机管理、指纹信任、连通性测试与命令执行。
//
// 引擎在 internal/sshx（连接/认证/指纹/执行），存储在本包的 db 层，
// 本包只做参数校验、错误码归一与凭据串联。
package sshx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"quickdock/internal/db"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// 默认超时。
const (
	dialTimeout    = 15 * time.Second
	defaultExecSec = 30
	maxExecSec     = 600
)

// SshService SSH 绑定服务。
type SshService struct {
	App *services.AppService

	// known 全进程唯一实例：它内部持有「待确认密钥」的内存表，
	// 每次连接都新建一个会丢掉 pending，用户点「信任」时就找不到本次观察到的指纹。
	known *sshcore.KnownHosts

	// batcher 与 manager 也必须全进程唯一：
	// 一个 Batcher 服务所有会话（只有一个 ticker），一个 Manager 管所有终端，
	// 窗口关掉重开要靠它把会话和回放找回来。
	batcher *sshcore.Batcher
	manager *sshcore.Manager

	// pool 按主机复用连接（#13）：同主机开多个标签只握手一次。
	// 引用计数在池内维护，这里只负责在 Connect/断开时调用 Acquire/Release。
	pool *connPool

	// releases 记录「会话 → 连接归还函数」，断开时归还引用。
	// 最后一条会话归还时连接才会真正关闭（见 connPool）。
	releaseMu sync.Mutex
	releases  map[string]func()

	// owners 记录「会话 → 主机 ID」。
	//
	// 生命周期比连接池**故意更长**：连接断了会话还要留着供回放，
	// 所以主人映射必须活到用户主动关闭会话为止。
	// 否则窗口重开后 ListSessions 会给出一堆认不出主人的孤儿 sessionId。
	ownerMu sync.Mutex
	owners  map[string]string

	// transferMu / transferCancels 记录「进行中的传输 → 取消函数」，键是前端生成的
	// transferID。为什么按 ID 而不是按主机：一个用户可以在两个标签里同时往同一台机器
	// 传文件，按主机只能一把取消全部，那不是用户按下「取消」时想要的结果。
	transferMu      sync.Mutex
	transferCancels map[string]context.CancelFunc

	// batchMu / batchCancels 记录「进行中的批量执行 → 取消函数」，键是批次 id。
	// 与 transferCancels 同形但不是同一张表：两者生命周期与语义都不同
	// （传输是单文件搬运，批次是一组主机的命令），混在一起会让取消互相误伤。
	batchMu      sync.Mutex
	batchCancels map[string]context.CancelFunc

	shutdownOnce sync.Once
}

// NewSshService 创建 SshService。
func NewSshService(app *services.AppService) *SshService {
	s := &SshService{
		App:             app,
		known:           sshcore.NewKnownHosts(knownHostStore{app: app}),
		pool:            newConnPool(),
		releases:        make(map[string]func()),
		owners:          make(map[string]string),
		transferCancels: make(map[string]context.CancelFunc),
		batchCancels:    make(map[string]context.CancelFunc),
	}
	s.batcher = sshcore.NewBatcher(eventSink{app: app}, sshcore.DefaultFlushInterval)
	s.manager = sshcore.NewManager(s.batcher, sshcore.DefaultScrollback)
	return s
}

// SshHostInput 新建/编辑主机的入参。
//
// 不直接用 db.SshHost 是因为密码字段在 db 层标了 json:"-"，序列化时会被丢掉，
// 前端根本传不进来；这里显式列出可写字段，也顺带把「哪些字段允许前端改」定死。
type SshHostInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`

	AuthType      string `json:"authType"` // password | key
	Password      string `json:"password"` // 留空 = 不修改
	ClearPassword bool   `json:"clearPassword"`
	KeyPath       string `json:"keyPath"`
	KeyPassphrase string `json:"keyPassphrase"` // 留空 = 不修改

	LegacyAlgorithms bool   `json:"legacyAlgorithms"`
	Env              string `json:"env"` // prod | staging | test

	ParentID string `json:"parentId"`
	IsFolder bool   `json:"isFolder"`
	Sort     int    `json:"sort"`
	Remark   string `json:"remark"`
}

// TestHostResult 「测试连接」结果。
//
// 未信任的主机密钥不算测试失败：首次连接本来就要先看到指纹再决定是否信任，
// 所以这种情况下仍返回 Ok，用 needsTrust 告诉前端弹「确认指纹」对话框。
type TestHostResult struct {
	Trusted          bool     `json:"trusted"`
	NeedsTrust       bool     `json:"needsTrust"`
	ServerVersion    string   `json:"serverVersion"`
	HostKeyAlgorithm string   `json:"hostKeyAlgorithm"`
	Fingerprint      string   `json:"fingerprint"`
	KnownTypes       []string `json:"knownTypes"`
	LatencyMs        int64    `json:"latencyMs"`
}

// ExecResult 一次性命令执行结果。
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Truncated  bool   `json:"truncated"`
}

// ---- 主机管理 ----

// ListHosts 列出全部节点（主机 + 文件夹），不含任何凭据。
func (s *SshService) ListHosts() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	list, err := s.App.DB.ListSshHosts()
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(list)
}

// GetHost 读取单个节点（不含凭据）。
func (s *SshService) GetHost(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if strings.TrimSpace(id) == "" {
		return failSsh(errInvalidInput)
	}
	h, err := s.App.DB.GetSshHost(id)
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(h)
}

// SaveHost 新建或更新主机/文件夹。
func (s *SshService) SaveHost(in SshHostInput) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	h, err := s.App.DB.SaveSshHost(db.SshHost{
		ID:               strings.TrimSpace(in.ID),
		Name:             in.Name,
		Host:             in.Host,
		Port:             in.Port,
		User:             in.User,
		AuthType:         in.AuthType,
		Password:         in.Password,
		KeyPath:          in.KeyPath,
		KeyPassphrase:    in.KeyPassphrase,
		ClearPassword:    in.ClearPassword,
		LegacyAlgorithms: in.LegacyAlgorithms,
		Env:              in.Env,
		ParentID:         in.ParentID,
		IsFolder:         in.IsFolder,
		Sort:             in.Sort,
		Remark:           in.Remark,
	})
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(h)
}

// DeleteHost 删除主机或文件夹（文件夹会连同子树一起删除）。
func (s *SshService) DeleteHost(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if strings.TrimSpace(id) == "" {
		return failSsh(errInvalidInput)
	}
	if err := s.App.DB.DeleteSshHost(id); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}

// MoveHost 移动节点到新父节点（会话树拖拽）。
func (s *SshService) MoveHost(id, parentID string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if err := s.App.DB.MoveSshHost(strings.TrimSpace(id), strings.TrimSpace(parentID)); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}

// ---- 连通性与指纹 ----

// TestHost 测试连通性：建连 + 认证 + 取主机指纹，成功即断开。
func (s *SshService) TestHost(id string) *services.ApiResult {
	host, secret, err := s.loadHost(id)
	if err != nil {
		return failSsh(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	start := time.Now()
	client, err := s.dial(ctx, host, secret)
	if err != nil {
		// 首次连接：把指纹交回前端去确认，这不算失败。
		var unknown *sshcore.UnknownHostKeyError
		if errors.As(err, &unknown) {
			return services.Ok(TestHostResult{
				NeedsTrust:       true,
				HostKeyAlgorithm: unknown.KeyType,
				Fingerprint:      unknown.Fingerprint,
				KnownTypes:       unknown.KnownTypes,
				LatencyMs:        time.Since(start).Milliseconds(),
			})
		}
		return failSsh(err)
	}
	defer client.Close()

	info := client.Info()
	// 记录时间失败不影响「连接可用」的结论，不打断流程。
	_ = s.App.DB.TouchSshHostConnected(host.ID)
	return services.Ok(TestHostResult{
		Trusted:          true,
		ServerVersion:    info.ServerVersion,
		HostKeyAlgorithm: info.HostKeyAlgorithm,
		Fingerprint:      info.Fingerprint,
		LatencyMs:        time.Since(start).Milliseconds(),
	})
}

// ListTrustedKeys 查看某主机已信任的密钥（设置页展示）。
func (s *SshService) ListTrustedKeys(host string, port int) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	keys, err := s.known.List(host, port)
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(keys)
}

// TrustFingerprint 信任本次连接观察到的主机密钥（首次确认 / 变更后接受）。
//
// fingerprint 必须是弹窗里给用户看过的那个值，服务层会与本次握手观察到的值比对，
// 不一致直接拒绝——避免「前端随便传一个指纹就把未知密钥写进白名单」。
func (s *SshService) TrustFingerprint(host string, port int, fingerprint string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	rec, err := s.known.Trust(host, port, fingerprint)
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(rec)
}

// TrustedHost 一台服务器的已信任密钥（ListAllTrustedKeys 的返回项）。
//
// Host 用「主机名:端口」拼好的展示串另给 Display 字段：前端要列表展示，
// 但撤销时必须回传原始 host + port，混用会因端口归一化对不上而删不掉。
type TrustedHost struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Display     string `json:"display"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	AddedAt     int64  `json:"addedAt"`
	/** 该记录对应的主机 ID；主机已删除时为空串 */
	HostID   string `json:"hostId"`
	HostName string `json:"hostName"`
}

// ListAllTrustedKeys 列出全部已信任的主机密钥（指纹管理页）。
//
// 为什么需要「全部」而不是复用 ListTrustedKeys(host, port)：后者按主机查，
// 管理页要展示全部记录就得前端逐台调，主机多了那是 N 次 IPC。
//
// 刻意把 HostID 一起带出来：用户最想删的往往正是「已经不连了但记录还在」
// 的那些，光有指纹字符串他认不出对应哪台机器。
func (s *SshService) ListAllTrustedKeys() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	recs, err := s.App.DB.ListAllSshKnownHosts()
	if err != nil {
		return failSsh(err)
	}
	// 建 host:port → 主机 的映射。同一台机器可能有多个端口配置，
	// 这里只挂第一个匹配到的，够用户辨认归属即可。
	// 拿不到主机列表不算失败 —— 指纹记录的存在不依赖主机配置，
	// 主机被删了记录也该能被看到和清掉。
	byTarget := make(map[string]*db.SshHost)
	if list, err := s.App.DB.ListSshHosts(); err == nil {
		for i := range list {
			h := list[i]
			key := fmt.Sprintf("%s:%d", h.Host, h.Port)
			if _, ok := byTarget[key]; !ok {
				byTarget[key] = &h
			}
		}
	}

	out := make([]TrustedHost, 0, len(recs))
	for _, r := range recs {
		item := TrustedHost{
			Host:        r.Host,
			Port:        r.Port,
			Display:     fmt.Sprintf("%s:%d", r.Host, r.Port),
			KeyType:     r.KeyType,
			Fingerprint: r.Fingerprint,
			AddedAt:     r.AddedAt,
		}
		if h, ok := byTarget[item.Display]; ok && h != nil {
			item.HostID = h.ID
			item.HostName = h.Name
		}
		out = append(out, item)
	}
	return services.Ok(out)
}

// RemoveTrustedKeys 清除某主机的全部已信任密钥（「清除指纹」）。
func (s *SshService) RemoveTrustedKeys(host string, port int) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if err := s.known.RemoveAll(host, port); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}

// ---- 命令执行 ----

// Exec 在目标主机上执行一条命令（非交互，一次性连接）。
//
// 退出码放在 data.exitCode 里：非零退出码是命令的正常结果（grep 没匹配返回 1），
// 只有「命令没跑完」才返回失败。
func (s *SshService) Exec(hostID, cmd string, timeoutSec int) *services.ApiResult {
	host, secret, err := s.loadHost(hostID)
	if err != nil {
		return failSsh(err)
	}
	if strings.TrimSpace(cmd) == "" {
		return failSsh(sshcore.ErrEmptyCommand)
	}
	if timeoutSec <= 0 {
		timeoutSec = defaultExecSec
	}
	if timeoutSec > maxExecSec {
		timeoutSec = maxExecSec
	}

	dialCtx, cancelDial := context.WithTimeout(context.Background(), dialTimeout)
	defer cancelDial()
	client, err := s.dial(dialCtx, host, secret)
	if err != nil {
		return failSsh(err)
	}
	defer client.Close()

	execCtx, cancelExec := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancelExec()

	start := time.Now()
	res, err := client.Exec(execCtx, cmd)
	out := ExecResult{
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitCode:   res.ExitCode,
		DurationMs: time.Since(start).Milliseconds(),
		Truncated:  res.Truncated,
	}
	if err != nil {
		// 失败也把已收到的输出带回，用户能看清它卡在哪一步。
		return failSshWith(err, map[string]interface{}{"result": out})
	}
	return services.Ok(out)
}

// ---- 内部 ----

// loadHost 取主机配置与解密凭据。
func (s *SshService) loadHost(id string) (*db.SshHost, *db.SshHostSecret, error) {
	if s.App.DB == nil {
		return nil, nil, errDBUnavailable
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil, errInvalidInput
	}
	host, err := s.App.DB.GetSshHost(id)
	if err != nil {
		return nil, nil, err
	}
	if host.IsFolder {
		return nil, nil, errFolderNotConnectable
	}
	secret, err := s.App.DB.GetSshHostSecret(id)
	if err != nil {
		return nil, nil, err
	}
	if secret.AuthType == db.SshAuthPassword && secret.Password == "" {
		return nil, nil, errNoCredential
	}
	if secret.AuthType == db.SshAuthKey && secret.KeyPath == "" {
		return nil, nil, errNoCredential
	}
	return host, secret, nil
}

// dial 统一装配连接参数：解密后的凭据 + 指纹校验回调。
func (s *SshService) dial(ctx context.Context, host *db.SshHost, secret *db.SshHostSecret) (*sshcore.Client, error) {
	auth := sshcore.AuthSpec{Type: sshcore.AuthPassword, Password: secret.Password}
	if secret.AuthType == db.SshAuthKey {
		auth = sshcore.AuthSpec{
			Type:          sshcore.AuthKey,
			KeyPath:       secret.KeyPath,
			KeyPassphrase: secret.KeyPassphrase,
		}
	}
	return sshcore.Dial(ctx, sshcore.DialConfig{
		Host:             host.Host,
		Port:             host.Port,
		User:             host.User,
		Auth:             auth,
		Timeout:          dialTimeout,
		LegacyAlgorithms: host.LegacyAlgorithms,
		HostKeyCallback:  s.known.Callback(host.Host, host.Port),
	})
}
