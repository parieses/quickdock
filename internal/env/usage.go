package env

import (
	"math"
	"time"

	"quickdock/internal/sysutil"
)

// usageSample 某服务上次采样时的累计 CPU 时间与采样时刻。
type usageSample struct {
	cpuSeconds float64
	at         time.Time
}

// procSnapshot 返回全机进程快照，1 秒内的重复调用复用同一份。
// 环境页每 3 秒轮询一次，一轮里会逐个运行时各查一次状态；没有这层缓存就会把整张进程表
// 重复枚举十几遍。1 秒窗口足以覆盖一轮，又不会让资源数字明显陈旧。
func (m *Manager) procSnapshot() []sysutil.ProcStat {
	m.procSnapMu.Lock()
	defer m.procSnapMu.Unlock()
	if m.procSnap != nil && time.Since(m.procSnapAt) < time.Second {
		return m.procSnap
	}
	if snap, err := sysutil.SnapshotProcs(); err == nil {
		m.procSnap, m.procSnapAt = snap, time.Now()
	}
	return m.procSnap
}

// attachUsage 为运行中的服务填充进程树资源占用（内存 / 进程数 / CPU 占用率）。
// 未运行或拿不到进程快照时保持零值，调用方无需区分。
func (m *Manager) attachUsage(st *ServiceStatus) {
	if !st.Running || st.PID <= 0 {
		return
	}
	snap := m.procSnapshot()
	if len(snap) == 0 {
		return
	}

	var mem int64
	var cpu float64
	count := 0
	for _, p := range subtreeOf(snap, st.PID) {
		mem += p.MemBytes
		cpu += p.CPUSeconds
		count++
	}
	st.MemBytes = mem
	st.ProcCount = count

	now := time.Now()
	m.usageMu.Lock()
	prev, ok := m.usageSamples[st.PID]
	m.usageSamples[st.PID] = usageSample{cpuSeconds: cpu, at: now}
	m.pruneUsageLocked(snap)
	m.usageMu.Unlock()

	// 首次采样没有基线：CPUPercent 留 nil，由前端显示为「采样中」，而不是编一个 0。
	if !ok {
		return
	}
	if d := now.Sub(prev.at).Seconds(); d >= 0.2 {
		// 进程退出后 PID 被复用会让累计时间倒退，此时按 0 处理，下一次采样即恢复。
		if pct := (cpu - prev.cpuSeconds) / d * 100; pct > 0 {
			v := math.Round(pct*100) / 100
			st.CPUPercent = &v
		}
	}
}

// pruneUsageLocked 清掉快照里已不存在或久未刷新的采样，避免 PID 长期被复用导致 map 无界增长。
// 阈值取得偏高，正常使用（几十个服务）永远触发不到，仅在极端情况下兜底。
func (m *Manager) pruneUsageLocked(snap []sysutil.ProcStat) {
	if len(m.usageSamples) <= 256 {
		return
	}
	alive := make(map[int]bool, len(snap))
	for _, p := range snap {
		alive[p.PID] = true
	}
	for pid := range m.usageSamples {
		if !alive[pid] {
			delete(m.usageSamples, pid)
		}
	}
}

// subtreeOf 从 root 向下收集整棵进程树（含 root 自身），用于聚合服务及其子进程的占用。
func subtreeOf(snap []sysutil.ProcStat, root int) []sysutil.ProcStat {
	byPID := make(map[int]sysutil.ProcStat, len(snap))
	children := make(map[int][]int, len(snap))
	for _, p := range snap {
		byPID[p.PID] = p
		children[p.ParentPID] = append(children[p.ParentPID], p.PID)
	}
	seen := map[int]bool{root: true}
	queue := []int{root}
	out := make([]sysutil.ProcStat, 0, 4)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if p, ok := byPID[pid]; ok {
			out = append(out, p)
		}
		for _, c := range children[pid] {
			if !seen[c] {
				seen[c] = true
				queue = append(queue, c)
			}
		}
	}
	return out
}
