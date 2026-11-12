// Package credentials 密码库门面服务：密码条目的增删改查、取用、匹配与浏览器扩展配对。
//
// 引擎在 internal/credentials（DEK + AES-GCM + 匹配打分），存储在本包的 db 层，
// 本包只做参数校验、错误归一与两个消费入口的串联：
//
//	前端 Wails 绑定  ← CredentialsPage / CredentialPrompt / SSH 等内部场景
//	本地 HTTP API    ← Chrome / Edge 扩展（见 api.go）
//
// 两个入口共用同一份引擎，所以「在网页里存的密码」和「在应用里看到的密码」
// 天然是同一条数据，不存在同步问题。
package credentials

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"strings"
	"sync"
	"time"

	"quickdock/internal/credentials"
	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
)

// 错误码：与 sshx 同款做法，前端可按码分叉（此处只有两类就够）。
const (
	CodeDBUnavailable  = 1
	CodeVaultLocked    = 2
	CodeValidation     = 3
	CodeNotFound       = 4
	CodeExtensionError = 5
)

// CredentialInput 前端提交的条目表单。
//
// 与 db.Credential 分开的唯一理由：Secret 在这边是**明文输入**，
// 在 db 层是**密文**，同一个字段名承担两种语义容易让人写错。
// 分开后「加密在哪一步发生」是显而易见的。
type CredentialInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Username    string `json:"username"`
	Secret      string `json:"secret"` // 明文；留空 = 保持原值
	ClearSecret bool   `json:"clearSecret"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	URL         string `json:"url"`
	Notes       string `json:"notes"`
	Tags        string `json:"tags"`
	Source      string `json:"source"`
	SourceID    string `json:"sourceId"`
	Favorite    bool   `json:"favorite"`
}

// VaultStatus 密码库整体状态（密码库页头部 / 扩展配对页共用）。
type VaultStatus struct {
	// Available 密钥是否可用。false 时前端要显式提示「密码库不可用」，
	// 而不是让每条密码都显示成一串错误。
	Available bool `json:"available"`
	Count     int  `json:"count"`
	// PlatformProtected Windows 下为 true（DPAPI）；其它平台 false，
	// 前端据此提示「当前平台仅混淆，不是真加密」。
	PlatformProtected bool `json:"platformProtected"`
	// LockBound 是否已跟随启动锁：锁定后内存密钥被清零。
	LockBound bool `json:"lockBound"`
	// ExtensionEnabled 浏览器集成是否开启（有长期密钥且本地 API 在跑）。
	ExtensionEnabled bool `json:"extensionEnabled"`
	// ExtensionPaired 兼容旧字段：与 ExtensionEnabled 同义。
	ExtensionPaired bool `json:"extensionPaired"`
	// ExtensionPort 本地 API 端口（0 表示没起来，前端据此隐藏「连接密钥」）。
	ExtensionPort int `json:"extensionPort"`
}

// CredentialsService 密码库绑定服务。
type CredentialsService struct {
	App *services.AppService

	// api 本地 HTTP 服务（供浏览器扩展调用）。nil 表示未启动。
	api *localAPI

	mu sync.Mutex
}

// NewCredentialsService 创建门面服务。
func NewCredentialsService(app *services.AppService) *CredentialsService {
	return &CredentialsService{App: app}
}

// failCode 带业务码的失败结果（错误仍进日志，运维侧可查）。
func failCode(code int, msg string) *services.ApiResult {
	return &services.ApiResult{Code: code, Msg: msg}
}

// classify 把引擎/存储层的错误映射成前端可分别处理的码。
func classify(err error) *services.ApiResult {
	switch {
	case err == nil:
		return services.Ok(nil)
	case errors.Is(err, credentials.ErrLocked):
		logger.W("[credentials] 密码库不可用: %v", err)
		return failCode(CodeVaultLocked, err.Error())
	case errors.Is(err, db.ErrCredentialNotFound):
		return failCode(CodeNotFound, "密码条目不存在（可能已在别处删除）")
	default:
		return services.Fail(err)
	}
}

// Status 查询密码库状态。
func (s *CredentialsService) Status() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	st := VaultStatus{
		PlatformProtected: runtime.GOOS == "windows",
		LockBound:         true,
		ExtensionPort:     s.apiPort(),
	}
	// 用一次 Count 探活：它会先取 DB 引用，再走到表上。
	// 不主动 ensureDEK —— 只看「有没有密码」不该触发密钥解密。
	if n, err := credentials.Count(); err == nil {
		st.Available = true
		st.Count = n
	} else {
		logger.W("[credentials] 状态查询失败: %v", err)
	}
	st.ExtensionPaired = s.extensionPaired()
	st.ExtensionEnabled = st.ExtensionPaired
	return services.Ok(st)
}

// ListCredentials 列出全部条目（不含密码明文）。
func (s *CredentialsService) ListCredentials() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	list, err := credentials.List()
	if err != nil {
		return classify(err)
	}
	return services.Ok(list)
}

// GetCredential 取单条元信息（不含密码明文）。
func (s *CredentialsService) GetCredential(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	c, err := credentials.Get(strings.TrimSpace(id))
	if err != nil {
		return classify(err)
	}
	return services.Ok(c)
}

// RevealCredential 显示/复制时的明文读取。前端必须显式调用它，
// 列表接口永远不会带密码回来。
func (s *CredentialsService) RevealCredential(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	sec, err := credentials.Reveal(strings.TrimSpace(id))
	if err != nil {
		return classify(err)
	}
	return services.Ok(map[string]any{
		"id":       sec.ID,
		"username": sec.Username,
		"secret":   sec.Secret,
	})
}

// SaveCredential 新建或更新条目。
func (s *CredentialsService) SaveCredential(in CredentialInput) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	name := strings.TrimSpace(in.Name)
	host := strings.TrimSpace(in.Host)
	if name == "" && host == "" && strings.TrimSpace(in.Username) == "" {
		return failCode(CodeValidation, "名称、主机、用户名至少填一项")
	}
	if in.Port < 0 || in.Port > 65535 {
		return failCode(CodeValidation, "端口不合法")
	}
	if in.Secret != "" && len([]rune(in.Secret)) > 4096 {
		return failCode(CodeValidation, "密码过长")
	}

	c, err := credentials.Save(db.Credential{
		ID:          strings.TrimSpace(in.ID),
		Name:        name,
		Kind:        normalizeKind(in.Kind),
		Username:    strings.TrimSpace(in.Username),
		Secret:      in.Secret,
		ClearSecret: in.ClearSecret,
		Host:        host,
		Port:        in.Port,
		URL:         strings.TrimSpace(in.URL),
		Notes:       in.Notes,
		Tags:        strings.TrimSpace(in.Tags),
		Source:      in.Source,
		SourceID:    in.SourceID,
		Favorite:    in.Favorite,
	})
	if err != nil {
		return classify(err)
	}
	return services.Ok(c)
}

// DeleteCredential 删除条目。
func (s *CredentialsService) DeleteCredential(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if err := credentials.Delete(strings.TrimSpace(id)); err != nil {
		return classify(err)
	}
	return services.Ok(nil)
}

// MatchCredentials 按场景推荐条目（内部弹层与扩展共用同一套打分）。
//
// 只做元信息匹配，绝不在这里解密 —— 用户还没选定哪一条。
func (s *CredentialsService) MatchCredentials(kind, host string, port int, username string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	list, err := credentials.Match(credentials.MatchQuery{
		Kind:     normalizeKind(kind),
		Host:     host,
		Port:     port,
		Username: username,
	})
	if err != nil {
		return classify(err)
	}
	return services.Ok(list)
}

// TouchCredential 记一次使用（扩展比对到密码一致时调用，无需取明文）。
func (s *CredentialsService) TouchCredential(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if err := credentials.Touch(strings.TrimSpace(id)); err != nil {
		return classify(err)
	}
	return services.Ok(nil)
}

// CopyCredentialSecret 后端写剪贴板 + 定时清除。
//
// 为什么由后端做：① 30 秒后要「比对了再清」，只有这里能读剪贴板；
// ② 前端关掉/切页时定时器会跟着丢，后端不会；
// ③ 必须走 services.SetClipboardText（登记回环令牌），
// 否则密码会被剪贴板捕获协程记进 clipboard_entries 历史表。
func (s *CredentialsService) CopyCredentialSecret(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	sec, err := credentials.Reveal(strings.TrimSpace(id))
	if err != nil {
		return classify(err)
	}
	if sec.Secret == "" {
		return failCode(CodeValidation, "这条没有保存密码")
	}
	if !services.SetClipboardText(sec.Secret) {
		return services.FailMsg("写剪贴板失败")
	}
	clearClipboardAfter(sec.Secret, clipboardClearDelay)
	return services.Ok(map[string]any{"clearedInSec": int(clipboardClearDelay.Seconds())})
}

// clipboardClearDelay 密码在剪贴板上的存活时间。
// 30 秒够切到目标窗口粘贴，又不至于让密码整天躺在剪贴板里被误粘。
const clipboardClearDelay = 30 * time.Second

// clearClipboardAfter 到点后「内容仍是这条密码」才清空。
//
// 只比对再清，是为了不误删用户这期间复制的新内容——
// 直接把剪贴板写空会让用户以为复制功能坏了。
func clearClipboardAfter(secret string, delay time.Duration) {
	go func() {
		time.Sleep(delay)
		if strings.TrimSpace(platform.GetClipboardText()) != strings.TrimSpace(secret) {
			return // 用户已经复制了别的东西
		}
		if services.SetClipboardText("") {
			logger.I("[credentials] 剪贴板中的密码已按计划清除")
		}
	}()
}

// GeneratePassword 生成随机密码（不落库，前端拿到后自己决定存不存）。
func (s *CredentialsService) GeneratePassword(length int, digits, symbols, upper bool) *services.ApiResult {
	if length < 8 {
		length = 16
	}
	if length > 128 {
		length = 128
	}
	pw, err := generatePassword(length, digits, symbols, upper)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]any{"password": pw})
}

// generatePassword 从字符池里取 length 个随机字符。
//
// 用 crypto/rand 而不是 math/rand：后者是可预测的 PRNG，
// 用它生成的「密码」等于把强度交给种子。
func generatePassword(length int, digits, symbols, upper bool) (string, error) {
	pool := "abcdefghijklmnopqrstuvwxyz"
	if upper {
		pool += "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	}
	if digits {
		pool += "0123456789"
	}
	if symbols {
		// 刻意排除 ` " ' \ 这几个：粘进 shell / 配置文件 / SQL 时最容易出事。
		pool += "!#$%&()*+,-./:;<=>?@[]^_{|}~"
	}
	max := big.NewInt(int64(len(pool)))
	var b strings.Builder
	b.Grow(length)
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("生成随机密码失败: %w", err)
		}
		b.WriteByte(pool[n.Int64()])
	}
	return b.String(), nil
}

// normalizeKind 归一化类型；未知值落到 other 而不是报错。
//
// 为什么容错：扩展会带着自己的枚举来（未来可能加 credit_card 之类），
// 因为一个不认识的值就拒绝保存，用户会丢掉刚输的密码。
func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", db.CredKindLogin:
		return db.CredKindLogin
	case db.CredKindSSH:
		return db.CredKindSSH
	case db.CredKindDatabase:
		return db.CredKindDatabase
	case db.CredKindWebDAV:
		return db.CredKindWebDAV
	case db.CredKindGit:
		return db.CredKindGit
	case db.CredKindAPIKey:
		return db.CredKindAPIKey
	default:
		return db.CredKindOther
	}
}
