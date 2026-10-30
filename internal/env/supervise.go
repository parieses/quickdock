package env

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// 服务常驻：期望状态（enabled）与崩溃自愈看门狗。
//
// 设计：每个 runtime 有一个「期望状态」开关（默认关闭，opt-in）。开启即表示“我希望它常驻运行”，
// 宿主在启动时对账拉起、运行期间由看门狗在进程掉线后自动重启。关闭即停止。
// 这与原来的「手动 start/stop 单次动作」不同：用户只表达意图，启停与自愈由宿主保证。

const (
	// maxRestartAttempts 看门狗在 restartWindow 内对单一 runtime 的最大重启次数，
	// 超过则暂停自愈一段时间，避免“启动即崩”造成无限重启循环。
	maxRestartAttempts = 5
	restartWindow      = 60 * time.Second
	watchdogInterval   = 15 * time.Second

	// exitStopTimeout 退出清理的整体超时。单项 Stop 内部可能阻塞在子进程调用上
	// （caddy stop 走 admin API、redis-cli shutdown 等握手），不能让任一环节把进程退出卡死。
	exitStopTimeout = 5 * time.Second
)

// statesFile 返回期望状态持久化路径（env/states.json）。
func (m *Manager) statesFile() string {
	return filepath.Join(platform.DefaultDataDir(), "env", "states.json")
}

func (m *Manager) loadStates() {
	m.enabledMu.Lock()
	defer m.enabledMu.Unlock()
	m.enabled = map[Runtime]bool{}
	b, err := os.ReadFile(m.statesFile())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &m.enabled)
}

func (m *Manager) saveStates() {
	// 深拷贝后再释放锁：MarshalIndent 遍历期间若另一 goroutine 拿写锁改 m.enabled，
	// 直接序列化 m.enabled 引用会触发并发读写 map panic。
	m.enabledMu.RLock()
	data := make(map[Runtime]bool, len(m.enabled))
	for k, v := range m.enabled {
		data[k] = v
	}
	m.enabledMu.RUnlock()
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.statesFile()), 0755); err != nil {
		return
	}
	_ = os.WriteFile(m.statesFile(), b, 0644)
}

// Enabled 返回某运行时的期望状态（是否已开启常驻）。
func (m *Manager) Enabled(rt Runtime) bool {
	m.enabledMu.RLock()
	defer m.enabledMu.RUnlock()
	return m.enabled[rt]
}

// resolveStartVersion 返回开启/对账时应启动的版本：优先激活版本，否则第一个已装版本。
// 没有任何已装版本时返回错误（调用方应提示先安装/激活）。
func (m *Manager) resolveStartVersion(rt Runtime) (string, error) {
	return m.ResolveVersion(rt, activeVersion(rt))
}

// ResolveVersion 返回应使用（启动/停止）的版本：want 非空且确实已安装则优先用 want，
// 否则回退到激活版本，仍无则取首个已装版本。
// 供外部（如场景绑定）在持有「期望版本」时复用与常驻对账完全一致的版本决策，
// 避免绑定里写死的版本被卸载后启停失败。
func (m *Manager) ResolveVersion(rt Runtime, want string) (string, error) {
	installed, err := m.InstalledVersions(rt)
	if err != nil || len(installed) == 0 {
		return "", fmt.Errorf("该运行时尚未安装任何版本，请先安装并激活")
	}
	if want != "" {
		for _, i := range installed {
			if i.Version == want {
				return want, nil
			}
		}
	}
	if active := activeVersion(rt); active != "" {
		for _, i := range installed {
			if i.Version == active {
				return i.Version, nil
			}
		}
	}
	return installed[0].Version, nil
}

// SetEnabled 设定某运行时的期望状态（开/关）。
// 开启：立即拉起（用激活版本，无激活则用首个已装版本）；关闭：立即停止。
// 非服务类运行时返回错误（前端仅对 hasService 的运行时展示开关）。
func (m *Manager) SetEnabled(rt Runtime, on bool) error {
	a := m.lookupAdapter(rt)
	if a == nil {
		return fmt.Errorf("未知运行时: %s", rt)
	}
	if _, ok := a.(ServiceController); !ok {
		return fmt.Errorf("该运行时不支持服务管理，无法设为常驻")
	}
	m.enabledMu.Lock()
	m.enabled[rt] = on
	m.enabledMu.Unlock()
	m.saveStates()

	if on {
		ver, err := m.resolveStartVersion(rt)
		if err != nil {
			return err
		}
		// 服务可能已由场景应用等路径启动（运行中但非常驻）：此时只需登记期望态。
		// 若照常调 Start，svcMgr 会以「服务已在运行」拒绝，开关打开失败却被回滚成关闭，
		// 而后端期望态已落盘 —— 前端显示关、看门狗却按开自愈，彻底停不掉。
		if st, e := m.Status(rt, ver); e == nil && st.Running {
			return nil
		}
		return m.Start(rt, ver, nil)
	}
	if ver, err := m.resolveStartVersion(rt); err == nil && ver != "" {
		_ = m.Stop(rt, ver) // 停止失败（如本就未运行）不阻断关开关
	}
	return nil
}

// ReclaimOrphans 在应用启动初期（ReconcileEnabled 之前）调用：回收上次会话
// 异常退出遗留的孤儿服务进程，避免它们脱离管理、占着端口。
//
// StopAllOnExit 只清理本会话经 svcMgr 拉起的进程。宿主被强杀（kill -9、断电、
// 退出清理超时）时，上次会话的子进程会以孤儿身份继续监听端口。本次启动 svcMgr
// 初始恒为空，所以任何 Status() 判为 Running 的实例必然是上次会话的孤儿。
// 不回收的话，ReconcileEnabled 见到 Running 会直接跳过，孤儿从此脱管，用户手动
// 启动也会被「已在运行 / 端口冲突」拦下。
//
// 回收是对每个已装版本执行 Stop（优雅关闭 + 端口兜底强杀，按镜像名匹配，
// 不会误伤其它程序）。Stop 幂等，对已停止的版本调用也安全；回收后由
// ReconcileEnabled 按 states.json 的期望态重新拉起。
//
// 不论是否设为常驻都回收——手动启动的服务同样属于上次会话，异常退出后同样是孤儿。
func (m *Manager) ReclaimOrphans(ctx context.Context) {
	for _, rt := range runtimeOrder {
		// 仅处理支持服务管理的运行时（与 SetEnabled 同款能力判定）
		a := m.lookupAdapter(rt)
		if a == nil {
			continue
		}
		if _, ok := a.(ServiceController); !ok {
			continue
		}
		// Everything 不参与孤儿回收：它是用户桌面工具（托盘 GUI），不监听任何端口，
		// 留着运行不会与任何服务冲突；而回收它意味着每次启动都去结束用户可能手动
		// （甚至以管理员身份）拉起的实例——既违背预期，非管理员进程也根本杀不掉
		//（拒绝访问），只会刷一堆无用的失败日志。残留实例由用户自行决定去留。
		if rt == RuntimeEverything {
			continue
		}
		installs, err := m.InstalledVersions(rt)
		if err != nil || len(installs) == 0 {
			continue
		}
		for _, ins := range installs {
			if ctx.Err() != nil {
				return
			}
			st, e := m.Status(rt, ins.Version)
			if e != nil || !st.Running {
				continue
			}
			logger.I("[env] 检测到 %s(%s) 上次会话遗留的孤儿进程，开始回收", rt, ins.Version)
			if err := m.Stop(rt, ins.Version); err != nil {
				logger.W("[env] 回收孤儿 %s(%s) 失败: %v", rt, ins.Version, err)
				continue
			}
			logger.I("[env] 已回收孤儿 %s(%s)，稍后由对账逻辑按需重新拉起", rt, ins.Version)
		}
	}
}

// ReconcileEnabled 应用启动时调用：拉起所有「已开启但未在运行」的服务。
func (m *Manager) ReconcileEnabled(ctx context.Context) {
	for _, rt := range runtimeOrder {
		if !m.Enabled(rt) {
			continue
		}
		ver, err := m.resolveStartVersion(rt)
		if err != nil {
			logger.W("[env] reconcile %s 跳过: %v", rt, err)
			continue
		}
		if st, e := m.Status(rt, ver); e == nil && st.Running {
			continue
		}
		if e := m.Start(rt, ver, nil); e != nil {
			logger.W("[env] reconcile 启动 %s 失败: %v", rt, e)
		}
	}
}

// StartWatchdog 周期巡检已开启但掉线的服务并自动重启（带重启预算防崩溃循环）。
// 随应用生命周期运行；ctx 取消即退出。
func (m *Manager) StartWatchdog(ctx context.Context) {
	go func() {
		// 常驻循环：panic 落盘后不重抛，避免一次异常让服务自愈整体停摆
		defer logger.RecoverToLog("env:watchdog")
		failCount := map[Runtime]int{}
		lastAttempt := map[Runtime]time.Time{}
		ticker := time.NewTicker(watchdogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				for _, rt := range runtimeOrder {
					if !m.Enabled(rt) {
						delete(failCount, rt)
						delete(lastAttempt, rt)
						continue
					}
					ver, err := m.resolveStartVersion(rt)
					if err != nil {
						continue
					}
					if st, e := m.Status(rt, ver); e == nil && st.Running {
						failCount[rt] = 0 // 正常运行，重置预算
						continue
					}
					// 已连续失败达上限：距上次失败未超过窗口则暂停，避免“启动即崩”无限重启。
					if failCount[rt] >= maxRestartAttempts {
						if now.Sub(lastAttempt[rt]) > restartWindow {
							failCount[rt] = 0 // 窗口已过，给一次重试机会
						} else {
							logger.W("[env] %s 连续重启 %d 次仍失败，暂停自愈避免崩溃循环", rt, maxRestartAttempts)
							continue
						}
					}
					if e := m.Start(rt, ver, nil); e != nil {
						failCount[rt]++
						lastAttempt[rt] = now
						logger.W("[env] watchdog 重启 %s 失败(%d/%d): %v", rt, failCount[rt], maxRestartAttempts, e)
					} else {
						failCount[rt] = 0
					}
				}
			}
		}
	}()
}

// StopAllOnExit 宿主退出时调用：停止本会话拉起的全部 env 服务，避免留下孤儿进程。
//
// 为什么需要：env 服务（redis / caddy / nginx / php-cgi / postgres ...）是宿主拉起的
// 独立子进程，既不随宿主退出而结束，也不在 job object 内。不清理的话，上次会话的进程会以
// 孤儿身份一直活着——占内存、脱离隐藏控制台，且下次启动对账时因 Status().Running 为真
// 被判定「已在运行」而跳过，从此不受任何会话管理（实测遗留 caddy+php-cgi+redis 共 54.5 MB）。
//
// 只停 svcMgr 记录的本会话句柄，不做端口全量扫描，两个原因：
//  1. 单实例降级：框架让第二个进程以 ExitCode 退出时也会走到这里，此时 svcMgr 为空，
//     不会误停首实例正在跑的服务；
//  2. stopByPort 杀的是「镜像名匹配该端口的任意进程」，退出路径上不该有这种波及面。
//
// 上次会话遗留的孤儿不在 svcMgr 中，本函数不处理（属「上次没清干净」，非本次退出职责）。
// 不看 Enabled：手动启动（未开常驻）的服务同样只属于本次会话，一并不留；
// 常驻服务由下次启动的 ReconcileEnabled 按 states.json 重新拉起。
//
// 幂等：停完即从 svcMgr 移除，重复调用为空操作（main.go 与 ServiceShutdown 双路径各调一次）。
// 单项失败只记日志、不中断，且整体受 exitStopTimeout 保护。
func (m *Manager) StopAllOnExit() {
	tracked := svcMgr.tracked()
	if len(tracked) == 0 {
		return
	}
	logger.I("[env] 退出清理：本会话拉起了 %d 个服务，开始停止", len(tracked))

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer logger.RecoverToLog("env:stop-on-exit")
		stopped, failed := 0, 0
		// 按 runtimeOrder 顺序停止（web server 在 php 之前），避免停 php 后 caddy 仍在上游报错刷日志
		for _, rt := range runtimeOrder {
			ver, ok := tracked[rt]
			if !ok {
				continue
			}
			if err := m.Stop(rt, ver); err != nil {
				logger.W("[env] 退出停止 %s(%s) 失败: %v", rt, ver, err)
				failed++
				continue
			}
			stopped++
		}
		logger.I("[env] 退出清理：已停止 %d 个本地服务（失败 %d）", stopped, failed)
	}()

	select {
	case <-done:
	case <-time.After(exitStopTimeout):
		logger.W("[env] 退出清理超过 %s 未完成，剩余停止操作随进程退出中断", exitStopTimeout)
	}
}
