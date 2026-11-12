// Package sshx 是 SSH 远程连接的门面服务：主机管理、指纹信任、连通性测试与命令执行。
//
// 引擎在 internal/sshx（连接/认证/指纹/执行），存储在本包的 db 层，
// 本包只做参数校验、错误码归一与凭据串联。
package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"quickdock/internal/credentials"
	"quickdock/internal/db"
	"quickdock/internal/logger"
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
	// 生命周期比连接池故意更长：连接断了会话还要留着供回放，
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

	// forwards 端口转发规则与运行时实例。刻意不进 connPool：转发是长生命周期的
	// 监听，池的引用计数归零即 Close，随便关一个终端标签就会把它掐死。
	forwards *forwardManager

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
	s.forwards = newForwardManager(app)
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

	// JumpHostID 跳板机主机 id，空串 = 直连。跳板机本身就是另一条 ssh_hosts 记录，
	// 所以它也能再挂一级跳板，形成链式；环在 SaveHost / 建链时挡住。
	JumpHostID string `json:"jumpHostId"`

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
	if err := s.validateJumpChain(strings.TrimSpace(in.ID), strings.TrimSpace(in.JumpHostID)); err != nil {
		return failSsh(err)
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
		JumpHostID:       strings.TrimSpace(in.JumpHostID),
		ParentID:         in.ParentID,
		IsFolder:         in.IsFolder,
		Sort:             in.Sort,
		Remark:           in.Remark,
	})
	if err != nil {
		return failSsh(err)
	}
	// 主机配置变了（尤其是跳板/端口/凭据），池里那条旧链路不能再给新标签复用。
	// 只从池里摘掉、不主动 Close：已有会话还持着 release，会自己关掉旧连接。
	s.pool.Invalidate(h.ID)
	// 密码顺手进密码库：用户在主机表单里填的密码本来只想「连得上」，
	// 但下次换台机器、或者要在密码库页里找它时，那条记录不该是空的。
	// 失败只记日志——密码库不可用不该拦住「改 SSH 配置」这件正事。
	if !h.IsFolder && h.AuthType == db.SshAuthPassword {
		switch {
		case strings.TrimSpace(in.Password) != "":
			s.syncVaultPassword(h, in.Password)
		case in.ClearPassword:
			s.clearVaultPassword(h.ID)
		}
	}
	return services.Ok(h)
}

// sourceSSHHost 密码库条目的来源标记。
//
// 一条 SSH 主机对应至多一条凭据，用 source+source_id 判重：
// 每次改主机都新建一条的话，密码库会被同一台机器的历史密码塞满。
const sourceSSHHost = "ssh_host"

// syncVaultPassword 把主机密码写进密码库（新建或更新同源条目）。
func (s *SshService) syncVaultPassword(h *db.SshHost, password string) {
	name := h.Name
	if name == "" {
		name = h.Host
	}
	in := db.Credential{
		Name:     name,
		Kind:     db.CredKindSSH,
		Username: h.User,
		Secret:   password,
		Host:     h.Host,
		Port:     h.Port,
		Source:   sourceSSHHost,
		SourceID: h.ID,
	}
	// 已有条目时保留用户自己改过的展示字段：密码库页里那条可能被重命名、
	// 加了备注/标签，仅仅因为改了主机端口就把它覆盖回默认值太粗暴。
	if old, err := s.App.DB.FindCredentialBySource(sourceSSHHost, h.ID); err == nil && old != nil {
		in.ID = old.ID
		in.Name = old.Name
		in.URL = old.URL
		in.Notes = old.Notes
		in.Tags = old.Tags
		in.Favorite = old.Favorite
	}
	if _, err := credentials.Save(in); err != nil {
		logger.W("[ssh] 同步主机密码到密码库失败: %v", err)
	}
}

// clearVaultPassword 主机清空密码时同步清掉密码库里的那份密文（保留条目本身）。
func (s *SshService) clearVaultPassword(hostID string) {
	old, err := s.App.DB.FindCredentialBySource(sourceSSHHost, hostID)
	if err != nil || old == nil {
		return
	}
	if _, err := credentials.Save(db.Credential{
		ID: old.ID, Name: old.Name, Kind: old.Kind, Username: old.Username,
		Host: old.Host, Port: old.Port, URL: old.URL, Notes: old.Notes, Tags: old.Tags,
		Source: old.Source, SourceID: old.SourceID, Favorite: old.Favorite,
		ClearSecret: true,
	}); err != nil {
		logger.W("[ssh] 清除密码库中的主机密码失败: %v", err)
	}
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

// TestHost 测试已落库主机的连通性：建连 + 认证 + 取主机指纹，成功即断开。
func (s *SshService) TestHost(id string) *services.ApiResult {
	host, secret, err := s.loadHost(id)
	if err != nil {
		return failSsh(err)
	}
	return s.probe(host, secret)
}

// TestHostInput 用表单里还没落库的配置测试连通性。
//
// 为什么需要它：新建主机时用户没有任何办法先验证一遍，只能「先存一台连不上的
// 机器，再回头改」。而 TestHost 只认 id、内部读库拿凭据，对「库里还没有的主机」
// 无能为力；先 SaveHost 再 TestHost 又会留下脏数据（测试失败但主机已经写进列表）。
//
// 编辑态下密码/口令留空是「不修改」的约定（见 db.SaveSshHost），所以这里要把
// 库里已存的凭据补回来，否则用户改个端口再去测试会被判成「空密码」。
func (s *SshService) TestHostInput(in SshHostInput) *services.ApiResult {
	host, secret, err := s.buildTestTarget(in)
	if err != nil {
		return failSsh(err)
	}
	return s.probe(host, secret)
}

// probe 真正的探测段：两条入口（已落库 / 未落库）共用，确保指纹确认逻辑只有一份。
func (s *SshService) probe(host *db.SshHost, secret *db.SshHostSecret) *services.ApiResult {
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
	// 未落库的表单值没有 id，跳过即可（UPDATE ... WHERE id='' 只是空跑）。
	if host.ID != "" {
		_ = s.App.DB.TouchSshHostConnected(host.ID)
	}
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
		// 主机自己没存密码时回落到密码库：用户可能是在密码库页里维护这台机器的
		// 账号，而不是在主机表单里。两者指向同一台机，没理由要他再填一遍。
		if pw := s.vaultPassword(host); pw != "" {
			secret.Password = pw
		} else {
			return nil, nil, errNoCredential
		}
	}
	if secret.AuthType == db.SshAuthKey && secret.KeyPath == "" {
		return nil, nil, errNoCredential
	}
	return host, secret, nil
}

// vaultPassword 按 host/port/user 从密码库找一条 SSH 密码。
//
// 取分数最高的一条并解出明文；任何一步失败都返回空串——
// 密码库被锁（内存密钥已清零）不该让「主机自己存了密码」的连接也连不上。
func (s *SshService) vaultPassword(host *db.SshHost) string {
	list, err := credentials.Match(credentials.MatchQuery{
		Kind:     db.CredKindSSH,
		Host:     host.Host,
		Port:     host.Port,
		Username: host.User,
	})
	if err != nil {
		logger.W("[ssh] 从密码库匹配主机密码失败: %v", err)
		return ""
	}
	for _, c := range list {
		if !c.HasSecret {
			continue
		}
		sec, err := credentials.Reveal(c.ID)
		if err != nil {
			logger.W("[ssh] 解开密码库条目失败: %v", err)
			continue
		}
		if sec.Secret != "" {
			return sec.Secret
		}
	}
	return ""
}

// buildTestTarget 把表单入参整理成「可以直接拿去 dial」的 host + secret。
//
// 归一化规则对齐 db.normalizeSshHost，但不落库：端口非法回落 22、用户留空回落 root、
// 认证类型只认 password/key。校验口径也保持一致——缺主机地址、缺密码、缺私钥路径
// 都按 errInvalidInput / errNoCredential 报错，让前端拿到的错误和保存时一模一样。
func (s *SshService) buildTestTarget(in SshHostInput) (*db.SshHost, *db.SshHostSecret, error) {
	if s.App.DB == nil {
		return nil, nil, errDBUnavailable
	}
	if in.IsFolder {
		return nil, nil, errFolderNotConnectable
	}

	hostname := strings.TrimSpace(in.Host)
	if hostname == "" {
		return nil, nil, errInvalidInput
	}
	port := in.Port
	if port <= 0 || port > 65535 {
		port = 22
	}
	user := strings.TrimSpace(in.User)
	if user == "" {
		user = "root"
	}

	authType := in.AuthType
	if authType != db.SshAuthKey {
		authType = db.SshAuthPassword
	}
	secret := &db.SshHostSecret{
		Host:             hostname,
		Port:             port,
		User:             user,
		AuthType:         authType,
		Password:         in.Password,
		KeyPath:          strings.TrimSpace(in.KeyPath),
		KeyPassphrase:    in.KeyPassphrase,
		LegacyAlgorithms: in.LegacyAlgorithms,
	}

	// 编辑态：库里的值兜底（留空 = 不修改），显式清除优先。
	if id := strings.TrimSpace(in.ID); id != "" {
		if stored, err := s.App.DB.GetSshHostSecret(id); err == nil {
			if secret.Password == "" {
				secret.Password = stored.Password
			}
			if secret.KeyPassphrase == "" {
				secret.KeyPassphrase = stored.KeyPassphrase
			}
		}
	}
	if in.ClearPassword {
		secret.Password = ""
	}

	if authType == db.SshAuthPassword && secret.Password == "" {
		return nil, nil, errNoCredential
	}
	if authType == db.SshAuthKey && secret.KeyPath == "" {
		return nil, nil, errNoCredential
	}
	// 表单里还没落库的跳板选择同样要挡环：否则「测试连接」能把用户带进
	// 一次无限递归建链，而保存时才会报错，两个入口口径不一致。
	jumpID := strings.TrimSpace(in.JumpHostID)
	if err := s.validateJumpChain(strings.TrimSpace(in.ID), jumpID); err != nil {
		return nil, nil, err
	}

	return &db.SshHost{
		ID:               strings.TrimSpace(in.ID),
		Name:             in.Name,
		Host:             hostname,
		Port:             port,
		User:             user,
		LegacyAlgorithms: in.LegacyAlgorithms,
		JumpHostID:       jumpID,
	}, secret, nil
}

// maxJumpDepth 允许的跳板级数。每多一跳就多一次完整握手与一次指纹校验，
// 断链时也更难说清是哪一跳；3 级覆盖「本机 → 堡垒机 → 区域跳板 → 目标」。
const maxJumpDepth = 3

// jumpHop 链上的一跳：主机配置 + 解密后的凭据。
type jumpHop struct {
	host   *db.SshHost
	secret *db.SshHostSecret
}

// validateJumpChain 保存 / 表单测试前的链路校验：只沿 jump_host_id 往上走，
// 不解密凭据也不建连接，所以「还没落库」的表单也能用。
//
// 环必须在保存前挡住：真到连接时才发现，用户看到的会是一次超时而不是配置错误。
func (s *SshService) validateJumpChain(hostID, jumpHostID string) error {
	jumpHostID = strings.TrimSpace(jumpHostID)
	if jumpHostID == "" {
		return nil
	}
	if hostID != "" && jumpHostID == hostID {
		return fmt.Errorf("%w: 不能把主机自己设为跳板机", errInvalidInput)
	}
	seen := map[string]bool{}
	if hostID != "" {
		seen[hostID] = true
	}
	cur := jumpHostID
	for i := 0; cur != ""; i++ {
		if i >= maxJumpDepth {
			return fmt.Errorf("%w: 跳板机最多 %d 级", errInvalidInput, maxJumpDepth)
		}
		if seen[cur] {
			return fmt.Errorf("%w: 跳板机链路成环（%s）", errInvalidInput, cur)
		}
		seen[cur] = true
		h, err := s.App.DB.GetSshHost(cur)
		if err != nil {
			return fmt.Errorf("读取跳板机失败: %w", err)
		}
		if h == nil {
			return fmt.Errorf("%w: 跳板机不存在或已被删除", errInvalidInput)
		}
		if h.IsFolder {
			return fmt.Errorf("%w: 文件夹不能作为跳板机", errInvalidInput)
		}
		cur = strings.TrimSpace(h.JumpHostID)
	}
	return nil
}

// resolveJumpChain 把「目标 → 直接跳板 → 跳板的跳板」解析成从最外层开始的拨号顺序。
//
// 跳板机就是另一条 ssh_hosts 记录，凭据同样走 loadHost 解密，不新增任何存储。
func (s *SshService) resolveJumpChain(host *db.SshHost) ([]jumpHop, error) {
	chain := make([]jumpHop, 0, maxJumpDepth)
	seen := map[string]bool{}
	cur := ""
	if host != nil {
		if host.ID != "" {
			seen[host.ID] = true
		}
		cur = strings.TrimSpace(host.JumpHostID)
	}
	for cur != "" {
		if len(chain) >= maxJumpDepth {
			return nil, fmt.Errorf("%w: 跳板机最多 %d 级", errInvalidInput, maxJumpDepth)
		}
		if seen[cur] {
			return nil, fmt.Errorf("%w: 跳板机链路成环（%s）", errInvalidInput, cur)
		}
		seen[cur] = true
		jh, sec, err := s.loadHost(cur)
		if err != nil {
			return nil, fmt.Errorf("跳板机不可用: %w", err)
		}
		chain = append(chain, jumpHop{host: jh, secret: sec})
		cur = strings.TrimSpace(jh.JumpHostID)
	}
	// 反转：chain 收集时是「离目标最近的先」，拨号必须从最外层开始。
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// dial 统一装配连接参数：解密后的凭据 + 每跳独立的指纹校验 + 跳板链。
//
// 方案 A：整条链当作一个 sshcore.Client 交给连接池。中间跳挂在 final 的依赖上，
// final.Close() 会级联关掉，connPool（key 仍是 hostID）完全不需要改。
// 代价是 N 个目标各有一条到跳板的 TCP，但换来了「连接生命周期只有一个所有者」。
func (s *SshService) dial(ctx context.Context, host *db.SshHost, secret *db.SshHostSecret) (*sshcore.Client, error) {
	hops, err := s.resolveJumpChain(host)
	if err != nil {
		return nil, err
	}
	if len(hops) == 0 {
		return s.dialOne(ctx, host, secret, nil)
	}

	built := make([]*sshcore.Client, 0, len(hops))
	// 任何一跳失败都要把前面已经建好的跳板关掉，否则一次失败的连接尝试
	// 会在堡垒机上留一条连接到应用退出。
	closeBuilt := func() {
		for i := len(built) - 1; i >= 0; i-- {
			_ = built[i].Close()
		}
	}

	prev, err := s.dialOne(ctx, hops[0].host, hops[0].secret, nil)
	if err != nil {
		return nil, err
	}
	built = append(built, prev)
	for _, hop := range hops[1:] {
		c, e := s.dialOne(ctx, hop.host, hop.secret, prev.SSH().DialContext)
		if e != nil {
			closeBuilt()
			return nil, e
		}
		built = append(built, c)
		prev = c
	}
	final, err := s.dialOne(ctx, host, secret, prev.SSH().DialContext)
	if err != nil {
		closeBuilt()
		return nil, err
	}
	for _, c := range built {
		final.AddDependency(c)
	}
	return final, nil
}

// dialOne 拨一跳。connector 为 nil 时直连，非 nil 时由上一跳开出的
// direct-tcpip channel 提供底层 net.Conn（x/crypto 的 Client.DialContext 正好同签名）。
func (s *SshService) dialOne(ctx context.Context, host *db.SshHost, secret *db.SshHostSecret,
	connector func(context.Context, string, string) (net.Conn, error)) (*sshcore.Client, error) {
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
		// 每一跳各自校验指纹。known.Callback 是按 host/port 造的闭包，
		// 拿最终目标的回调去校验中间跳的话，跳板换了密钥也发现不了。
		HostKeyCallback: s.known.Callback(host.Host, host.Port),
		Connector:       connector,
	})
}
