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
	"os"
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
	//
	// Available 不能只看 Count：表读得通但 DEK 坏了（换过 Windows 账户、
	// DPAPI 数据损坏）时，界面会显示「可用」，直到用户保存或显示密码才报错。
	// KeyReadable 只读 app_state 解一次，不把 DEK 载入内存 —— 否则锁定后
	// 打开这个页面就会把刚 Wipe 掉的密钥自动重建回来。
	if n, err := credentials.Count(); err == nil {
		st.Count = n
		st.Available = credentials.KeyReadable()
		if !st.Available {
			logger.W("[credentials] 密码库密钥不可读（DPAPI 解不开或格式异常）")
		}
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

// 字符池。刻意排除 ` " ' \ 这几个：粘进 shell / 配置文件 / SQL 时最容易出事。
const (
	pwLower   = "abcdefghijklmnopqrstuvwxyz"
	pwUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	pwDigits  = "0123456789"
	pwSymbols = "!#$%&()*+,-./:;<=>?@[]^_{|}~"
)

// generatePassword 生成随机密码，且保证勾选的每一类字符至少出现一次。
//
// 早期版本只是把勾选的字符并进一个大池子再随机取，结果「勾了数字」仍可能
// 生成纯字母（20 位时约 10% 概率），要求「必须含数字/符号」的站点会直接拒绝。
// 现在先按类各取一个、再填满剩余长度，最后洗牌打散位置。
//
// 用 crypto/rand 而不是 math/rand：后者是可预测的 PRNG，
// 用它生成的「密码」等于把强度交给种子。
func generatePassword(length int, digits, symbols, upper bool) (string, error) {
	groups := []string{pwLower}
	if upper {
		groups = append(groups, pwUpper)
	}
	if digits {
		groups = append(groups, pwDigits)
	}
	if symbols {
		groups = append(groups, pwSymbols)
	}
	// 上层已把长度夹在 8~128，这里只是兜住「类别比长度还多」的极端调用。
	if length < len(groups) {
		length = len(groups)
	}

	pick := func(pool string) (byte, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
		if err != nil {
			return 0, fmt.Errorf("生成随机密码失败: %w", err)
		}
		return pool[n.Int64()], nil
	}

	out := make([]byte, 0, length)
	for _, g := range groups {
		c, err := pick(g)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	all := strings.Join(groups, "")
	for len(out) < length {
		c, err := pick(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	// Fisher-Yates：否则「每类保底的那一个」永远排在最前几位，等于给猜解省事。
	for i := len(out) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", fmt.Errorf("生成随机密码失败: %w", err)
		}
		j := int(n.Int64())
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

// ExportCredentials 把整库导出成口令加密的可移植备份。
//
// 文件对话框由前端调 AppService.SelectSavePath，这里只负责写文件：
// 宿主对话框能力不该在每个业务域里各实现一份（见 services/dialog.go）。
// 口令不落日志、不进数据库，只在本次调用里参与派生密钥。
func (s *CredentialsService) ExportCredentials(path, password string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return failCode(CodeValidation, "没有选择保存位置")
	}
	if len([]rune(password)) < 8 {
		return failCode(CodeValidation, "导出口令至少 8 位")
	}
	data, err := credentials.ExportPortable(password)
	if err != nil {
		return classify(err)
	}
	// 0600：备份里虽然是密文，也不该让同机其它账户随手读走做离线爆破。
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return services.Fail(fmt.Errorf("写入备份文件失败: %w", err))
	}
	count, _ := credentials.Count() // 只用于结果文案，统计失败不该让导出「失败」
	return services.Ok(map[string]any{"bytes": len(data), "count": count, "path": path})
}

// ImportCredentials 从加密备份或浏览器导出的 CSV 导入。
//
// 格式识别与解密全在 internal/credentials，门面只做路径与错误归一。
// 导入逐条走 Save，用**本机当前 DEK** 重新加密，因此换机后照样能解开。
func (s *CredentialsService) ImportCredentials(path, password string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return failCode(CodeValidation, "没有选择要导入的文件")
	}
	// 先按文件大小挡一道：误选了一个几 GB 的文件时不该把它整个读进内存。
	if st, err := os.Stat(path); err == nil && st.Size() > credentials.MaxImportBytes {
		return failCode(CodeValidation, fmt.Sprintf("文件超过 %d MB，不像是密码库备份", credentials.MaxImportBytes>>20))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return services.Fail(fmt.Errorf("读取备份文件失败: %w", err))
	}
	rep, err := credentials.ImportAuto(data, password)
	if err != nil {
		return failCode(CodeValidation, err.Error())
	}
	return services.Ok(rep)
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
