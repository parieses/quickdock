package env

import (
	"encoding/json"
	"os"
	"path/filepath"

	"quickdock/internal/platform"
)

// 检测结果持久化缓存：真实检测（扫描便携目录 + 系统 PATH，spawn exe 探测版本）很慢，
// 是「打开环境管理页闪一下」的根因——前端先渲染空列表，等探测完成才填充。
// 因此 List/InstalledVersions 一律读缓存（env/detected.json），真实检测仅在触发点执行：
// 启动后台扫描 PATH、导入已有安装、安装完成、删除版本，每次检测后重新保存。

// detectCachePath 缓存文件路径（env/detected.json，与 links.json 同目录）
func (m *Manager) detectCachePath() string {
	return filepath.Join(platform.DefaultDataDir(), "env", "detected.json")
}

// loadDetected 启动时从磁盘加载上次检测结果
func (m *Manager) loadDetected() {
	data, err := os.ReadFile(m.detectCachePath())
	if err != nil {
		return
	}
	var tmp map[Runtime][]Install
	if json.Unmarshal(data, &tmp) == nil {
		m.detectMu.Lock()
		m.detectCache = tmp
		m.detectMu.Unlock()
	}
}

// snapshotDetectedLocked 在调用方已持有 detectMu（读或写）时返回缓存的深拷贝：
// map 与每个版本的切片都不与 live 数据共享底层数组，使解锁后序列化不再触碰 live map，
// 避免与 RefreshDetected/RefreshAllAsync 的写入并发触发 concurrent map read and map write
// （该 panic 是 runtime fatal error，不可 recover）。
func (m *Manager) snapshotDetectedLocked() map[Runtime][]Install {
	out := make(map[Runtime][]Install, len(m.detectCache))
	for rt, ins := range m.detectCache {
		out[rt] = append([]Install(nil), ins...)
	}
	return out
}

// saveDetected 将检测结果缓存持久化到磁盘
func (m *Manager) saveDetected() {
	m.detectMu.RLock()
	tmp := m.snapshotDetectedLocked()
	m.detectMu.RUnlock()
	data, err := json.MarshalIndent(tmp, "", "  ")
	if err != nil {
		return
	}
	p := m.detectCachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

// cachedInstalls 返回某运行时缓存的检测结果（副本；无缓存返回 nil）
func (m *Manager) cachedInstalls(rt Runtime) []Install {
	m.detectMu.RLock()
	defer m.detectMu.RUnlock()
	src := m.detectCache[rt]
	if len(src) == 0 {
		return nil
	}
	out := make([]Install, len(src))
	copy(out, src)
	return out
}

// RefreshDetected 真正执行一次检测并重新保存缓存，返回新结果。
// 触发点：启动后台扫描 / 安装完成 / 删除版本（导入走 links.json 自身的持久化，无需重扫）。
func (m *Manager) RefreshDetected(rt Runtime) []Install {
	a, err := m.adapter(rt)
	if err != nil {
		return nil
	}
	ins := a.InstalledVersions()
	m.detectMu.Lock()
	if m.detectCache == nil {
		m.detectCache = map[Runtime][]Install{}
	}
	m.detectCache[rt] = ins
	m.detectMu.Unlock()
	m.saveDetected()
	return ins
}

// RefreshAllAsync 后台逐个刷新所有运行时的检测结果（避免阻塞启动与 IPC），完成后回调 done（可空）。
func (m *Manager) RefreshAllAsync(done func()) {
	go func() {
		for _, rt := range runtimeOrder {
			_ = m.RefreshDetected(rt)
		}
		if done != nil {
			done()
		}
	}()
}
