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
	installed, err := m.InstalledVersions(rt)
	if err != nil || len(installed) == 0 {
		return "", fmt.Errorf("该运行时尚未安装任何版本，请先安装并激活")
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
	if _, ok := m.adapters[rt]; !ok {
		return fmt.Errorf("未知运行时: %s", rt)
	}
	if _, ok := m.adapters[rt].(ServiceController); !ok {
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
		return m.Start(rt, ver, nil)
	}
	if ver, err := m.resolveStartVersion(rt); err == nil && ver != "" {
		_ = m.Stop(rt, ver) // 停止失败（如本就未运行）不阻断关开关
	}
	return nil
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
