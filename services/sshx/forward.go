package sshx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// EventSshForward 转发状态变化（start / running / retrying / error / stop）。
// payload: sshcore.ForwardView
const EventSshForward = "quickdock:ssh:forward"

// forwardSettingKey 转发规则存在 app_state 而不是新建表，理由有两条：
//  1. 规则强绑定本机（监听端口、autoStart 会开洞），跟着快照搬到另一台机器
//     轻则端口冲突，重则在没预期的机器上自动开放代理——app_state 刻意不参与快照。
//  2. 它是「一组配置」，不是需要排序 / 回收站 / 全文检索的用户内容；
//     与 notify_extra_channels、git 仓库清单同一类，没必要为它再动 5 处 schema。
const forwardSettingKey = "ssh_forward_rules"

// SshForwardInput 前端表单入参。字段与 sshcore.ForwardRule 一一对应，
// 故意不收 HostName / 状态 / 流量：那些是运行时或展示数据。
type SshForwardInput struct {
	ID         string `json:"id"`
	HostID     string `json:"hostId"`
	Kind       string `json:"kind"`
	BindHost   string `json:"bindHost"`
	BindPort   int    `json:"bindPort"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	AutoStart  bool   `json:"autoStart"`
}

func (in SshForwardInput) rule() sshcore.ForwardRule {
	return sshcore.ForwardRule{
		ID: strings.TrimSpace(in.ID), HostID: strings.TrimSpace(in.HostID),
		Kind: strings.TrimSpace(in.Kind), BindHost: strings.TrimSpace(in.BindHost),
		BindPort: in.BindPort, TargetHost: strings.TrimSpace(in.TargetHost),
		TargetPort: in.TargetPort, AutoStart: in.AutoStart,
	}
}

// forwardManager 管所有规则的运行时实例。
//
// 生命周期与 SshService 一致：一个实例一份，不进 connPool。
type forwardManager struct {
	app *services.AppService

	mu      sync.Mutex
	running map[string]*sshcore.Forward
	// autoStarted 用 bool 而不是 sync.Once：Once 在闭包第一次被调用时就「消耗」
	// 掉了，哪怕那次 load 因 DB 尚未就绪而失败，之后再也不会重试 autoStart。
	autoStarted bool
}

func newForwardManager(app *services.AppService) *forwardManager {
	return &forwardManager{app: app, running: map[string]*sshcore.Forward{}}
}

func (m *forwardManager) dbOK() error {
	if m.app == nil || m.app.DB == nil {
		return errDBUnavailable
	}
	return nil
}

func (m *forwardManager) load() ([]sshcore.ForwardRule, error) {
	if err := m.dbOK(); err != nil {
		return nil, err
	}
	raw, err := m.app.DB.GetSetting(forwardSettingKey)
	if err != nil {
		// key 不存在是首次使用，不是故障。
		return []sshcore.ForwardRule{}, nil
	}
	if strings.TrimSpace(raw) == "" {
		return []sshcore.ForwardRule{}, nil
	}
	var rules []sshcore.ForwardRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("读取转发规则失败: %w", err)
	}
	return rules, nil
}

func (m *forwardManager) save(rules []sshcore.ForwardRule) error {
	if err := m.dbOK(); err != nil {
		return err
	}
	buf, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	return m.app.DB.SetSetting(forwardSettingKey, string(buf))
}

// rulesCopy 返回规则副本 + 运行时快照，按创建顺序（切片顺序）稳定输出。
func (m *forwardManager) views() ([]sshcore.ForwardView, error) {
	rules, err := m.load()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	out := make([]sshcore.ForwardView, 0, len(rules))
	for _, r := range rules {
		if f := m.running[r.ID]; f != nil {
			out = append(out, f.Snapshot())
		} else {
			out = append(out, r.View())
		}
	}
	m.mu.Unlock()
	// 补主机显示名放在锁外：这里要走 DB 查询，没必要让其它规则的启停排队。
	for i := range out {
		out[i].HostName = ""
		m.fillHostName(&out[i])
	}
	return out, nil
}

func (m *forwardManager) fillHostName(v *sshcore.ForwardView) {
	if v.HostName != "" || v.HostID == "" {
		return
	}
	if err := m.dbOK(); err != nil {
		return
	}
	h, err := m.app.DB.GetSshHost(v.HostID)
	if err == nil && h != nil {
		v.HostName = h.Name
		if v.HostName == "" {
			v.HostName = h.Host
		}
	}
}

func (m *forwardManager) emit(v sshcore.ForwardView) {
	m.fillHostName(&v)
	if m.app == nil {
		return
	}
	a := m.app.App()
	if a == nil {
		return
	}
	a.Event.Emit(EventSshForward, v)
}

// dial 独立拨号：走 SshService.dial（跳板链、每跳指纹校验都自动生效），
// 完全绕过 connPool —— 池的 refs 归零即 Close，转发不能挂在别人引用计数上。
func (m *forwardManager) dial(svc *SshService) sshcore.ForwardDialer {
	return func(ctx context.Context, hostID string) (*sshcore.Client, error) {
		host, secret, err := svc.loadHost(hostID)
		if err != nil {
			return nil, err
		}
		return svc.dial(ctx, host, secret)
	}
}

func (m *forwardManager) newForward(svc *SshService, r sshcore.ForwardRule) *sshcore.Forward {
	return sshcore.NewForward(r, m.dial(svc), m.emit)
}

func (m *forwardManager) put(rule sshcore.ForwardRule, f *sshcore.Forward) {
	m.mu.Lock()
	m.running[rule.ID] = f
	m.mu.Unlock()
}

func (m *forwardManager) get(id string) *sshcore.Forward {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[id]
}

func (m *forwardManager) remove(id string) {
	m.mu.Lock()
	delete(m.running, id)
	m.mu.Unlock()
}

// startAuto 把勾了 autoStart 的规则拉起来，只跑一次。
//
// 时机放在「SSH 页首次拉取转发列表」而不是进程启动：DB 在 ServiceStartup
// 才注入，进程启动时读不到配置；更关键的是开机就在后台静默开一个 SOCKS
// 代理、用户完全没感知，不是好默认。SSH 页打开即恢复，语义可解释。
func (m *forwardManager) startAuto(svc *SshService) {
	m.mu.Lock()
	if m.autoStarted || m.dbOK() != nil {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	rules, err := m.load()
	if err != nil {
		return // 下次拉列表再试，不把「启动失败」固化下来
	}
	for _, r := range rules {
		if !r.AutoStart || sshcore.ValidateForwardRule(r) != nil {
			continue
		}
		f := m.newForward(svc, r)
		m.put(r, f)
		if err := f.Start(); err != nil {
			m.remove(r.ID)
		}
	}
	m.mu.Lock()
	m.autoStarted = true
	m.mu.Unlock()
}

// stopAll 退出时关掉全部转发，避免监听端口随宿主残留。
func (m *forwardManager) stopAll() {
	m.mu.Lock()
	list := make([]*sshcore.Forward, 0, len(m.running))
	for _, f := range m.running {
		list = append(list, f)
	}
	m.running = map[string]*sshcore.Forward{}
	m.mu.Unlock()
	for _, f := range list {
		f.Stop()
	}
}

// ---- Wails 门面 ----

// ListSshForwards 列出全部规则与运行状态。data: []sshcore.ForwardView
func (s *SshService) ListSshForwards() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	s.forwards.startAuto(s)
	views, err := s.forwards.views()
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(views)
}

// SaveSshForward 新增或更新一条规则（id 为空即新增）。data: sshcore.ForwardView
//
// 已在运行的规则被编辑后要先停再按新配置起：不然界面显示的是新端口，
// 实际监听的还是旧端口，这种「改了没生效」最难排查。
func (s *SshService) SaveSshForward(in SshForwardInput) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	rule := in.rule()
	if err := sshcore.ValidateForwardRule(rule); err != nil {
		return failSsh(err)
	}
	if _, _, err := s.loadHost(rule.HostID); err != nil {
		return failSsh(fmt.Errorf("转发目标主机不可用: %w", err))
	}

	rules, err := s.forwards.load()
	if err != nil {
		return failSsh(err)
	}
	wasRunning := false
	if rule.ID == "" {
		rule.ID = uuid.New().String()
		rules = append(rules, rule)
	} else {
		found := false
		for i := range rules {
			if rules[i].ID == rule.ID {
				rules[i] = rule
				found = true
				break
			}
		}
		if !found {
			return failSsh(fmt.Errorf("%w: 转发规则不存在", errInvalidInput))
		}
		if old := s.forwards.get(rule.ID); old != nil {
			wasRunning = true
			old.Stop()
			s.forwards.remove(rule.ID)
		}
	}
	if err := s.forwards.save(rules); err != nil {
		return failSsh(err)
	}

	if wasRunning {
		f := s.forwards.newForward(s, rule)
		s.forwards.put(rule, f)
		if err := f.Start(); err != nil {
			s.forwards.remove(rule.ID)
			return failSsh(err)
		}
		v := f.Snapshot()
		s.forwards.fillHostName(&v)
		return services.Ok(v)
	}
	v := rule.View()
	s.forwards.fillHostName(&v)
	return services.Ok(v)
}

// DeleteSshForward 删除一条规则（先停再删）。data: nil
func (s *SshService) DeleteSshForward(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return failSsh(errInvalidInput)
	}
	if f := s.forwards.get(id); f != nil {
		f.Stop()
		s.forwards.remove(id)
	}
	rules, err := s.forwards.load()
	if err != nil {
		return failSsh(err)
	}
	out := rules[:0]
	for _, r := range rules {
		if r.ID != id {
			out = append(out, r)
		}
	}
	if err := s.forwards.save(out); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}

// StartSshForward 启动一条规则。data: sshcore.ForwardView
func (s *SshService) StartSshForward(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	rules, err := s.forwards.load()
	if err != nil {
		return failSsh(err)
	}
	var target *sshcore.ForwardRule
	for i := range rules {
		if rules[i].ID == id {
			target = &rules[i]
			break
		}
	}
	if target == nil {
		return failSsh(fmt.Errorf("%w: 转发规则不存在", errInvalidInput))
	}
	if err := sshcore.ValidateForwardRule(*target); err != nil {
		return failSsh(err)
	}
	f := s.forwards.get(id)
	if f == nil {
		f = s.forwards.newForward(s, *target)
		s.forwards.put(*target, f)
	}
	// 已经在跑就当成功返回：前端的启停按钮可能因事件延迟连点两次，
	// 报「已在运行」只会让人以为操作失败。
	if st := f.Snapshot().Status; st == sshcore.ForwardStateRunning ||
		st == sshcore.ForwardStateStarting || st == sshcore.ForwardStateRetrying {
		v := f.Snapshot()
		s.forwards.fillHostName(&v)
		return services.Ok(v)
	}
	if err := f.Start(); err != nil {
		return failSsh(err)
	}
	v := f.Snapshot()
	s.forwards.fillHostName(&v)
	return services.Ok(v)
}

// StopSshForward 停止一条规则。data: sshcore.ForwardView
func (s *SshService) StopSshForward(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	f := s.forwards.get(id)
	if f == nil {
		return failSsh(fmt.Errorf("%w: 转发规则未在运行", errInvalidInput))
	}
	f.Stop()
	v := f.Snapshot()
	s.forwards.fillHostName(&v)
	return services.Ok(v)
}
