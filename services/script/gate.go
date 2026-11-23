package script

import "sync"

// runGate 并发闸门：脚本 id → 当前在跑的实例数。
//
// 为什么不是 sync.Map 存「是否在跑」：脚本的 Concurrency 允许大于 1（抓取类
// 脚本并行更快），那就不是布尔量而是计数。用 Mutex + map 而不是 sync.Map +
// 原子计数：条目数只有「同时跑着的脚本数」，量级是个位数，锁竞争可以忽略，
// 而计数递减、归零删除这两件事用一把锁写出来不会错。
type runGate struct {
	mu   sync.Mutex
	live map[string]int
}

// acquire 尝试占一个实例位。limit < 1 按 1 处理（互斥）。
func (g *runGate) acquire(id string, limit int) bool {
	if limit < 1 {
		limit = 1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.live == nil {
		g.live = make(map[string]int, 8)
	}
	if g.live[id] >= limit {
		return false
	}
	g.live[id]++
	return true
}

// release 归还一个实例位，归零时删键（否则 map 会随脚本数无限长）。
func (g *runGate) release(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := g.live[id]; n > 1 {
		g.live[id] = n - 1
	} else {
		delete(g.live, id)
	}
}

// active 该脚本是否有实例在跑（事件触发、界面状态用）。
func (g *runGate) active(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.live[id] > 0
}

// count 当前实例数（并发上限提示用）。
func (g *runGate) count(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.live[id]
}
