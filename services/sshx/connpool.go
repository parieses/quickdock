package sshx

import (
	"context"
	"sync"

	sshcore "quickdock/internal/sshx"
)

// connPool 按主机复用 SSH 连接。
//
// #13 的核心问题：同主机开 3 个标签，如果每次 Connect 都重新握手，就是 3 次
// 完整 TCP + 认证（实测 CentOS 7 上握手约 200-400ms，跨网更慢），而且服务器
// 的 MaxSessions / MaxStartups 会被无谓消耗。SSH 协议本身支持一条连接开多个
// channel（NewSession），这正是当初连接层持 *ssh.Client 而非 *ssh.Session 的原因。
//
// 复用的前提是**引用计数**：连接不能跟着任何一个会话一起死。
//
//	pool.Acquire("host-1")   → refs 0→1
//	pool.Acquire("host-1")   → refs 1→2（复用，不拨号）
//	pool.Release("host-1")   → refs 2→1
//	pool.Release("host-1")   → refs 1→0 → Close + 从池里摘除
//
// 两条容易被忽略的规则：
//
//  1. **并发拨号必须串行化到同一主机**。两个标签同时打开时，若都看到池里没有
//     就各自拨号，会建出两条连接，其中一条立刻多余。同一把锁按 hostID 加，
//     不同主机之间仍然完全并行（不能一把全局锁串行所有拨号）。
//  2. **断开的连接要从池里摘掉，但 refcount 不能动**。连接死了不等于会话结束——
//     会话还留在 Manager 里供回放，它占的那个引用要等用户主动关闭时才归还。
//     若在这里把 refs 清零，后面 Release 会把计数减成负数或误关掉新连接。
type connPool struct {
	mu      sync.Mutex
	entries map[string]*connEntry
	// dialing 按 hostID 串行化同主机的建连过程，见上文规则 1。
	dialing map[string]*sync.Mutex
}

// connEntry 一条被复用的连接及其引用计数。
type connEntry struct {
	client *sshcore.Client

	// mu 保护 refs 与 dead。不用池级的锁：Release 可能来自任意 goroutine
	//（会话状态回调跑在 sess.Wait() 的 goroutine 上），与 Acquire 的拨号路径并发。
	mu    sync.Mutex
	refs  int
	dead  bool // 连接已不可用（网络断开/保活失败），Acquire 时要跳过并重拨
	info  sshcore.HandshakeInfo
	owner string // hostID，仅用于日志与断言
}

func newConnPool() *connPool {
	return &connPool{
		entries: make(map[string]*connEntry),
		dialing: make(map[string]*sync.Mutex),
	}
}

// dialFunc 实际建连的回调，由服务层提供（要解密凭据、装指纹回调）。
type dialFunc func(ctx context.Context) (*sshcore.Client, error)

// Acquire 取得主机的一条可用连接，必要时新建。
//
// 返回的 release 必须被调用恰好一次（无论后续 Start 成功与否），
// 否则这条连接会永远留在池里被泄漏。
func (p *connPool) Acquire(ctx context.Context, hostID string, dial dialFunc) (*sshcore.Client, func(), error) {
	if hostID == "" {
		return nil, nil, errInvalidInput
	}

	// 同一主机的建连串行，不同主机并行。锁的粒度是 hostID。
	unlockDial := p.lockDialing(hostID)
	defer unlockDial()

	p.mu.Lock()
	e := p.entries[hostID]
	p.mu.Unlock()

	if e != nil {
		e.mu.Lock()
		usable := !e.dead && e.client.Alive()
		if usable {
			e.refs++
		}
		e.mu.Unlock()
		if usable {
			return e.client, p.releaserFor(hostID, e), nil
		}
		// 连接已死：把它从池里摘掉再重拨。refs 保持不变——旧会话还占着引用，
		// 它们后续 Release 时会各自减一次（见 connPool 头注释规则 2）。
		p.evict(hostID, e)
	}

	client, err := dial(ctx)
	if err != nil {
		return nil, nil, err
	}

	entry := &connEntry{client: client, refs: 1, info: client.Info(), owner: hostID}
	// 连接一断就要立刻从池里摘掉：留着的话下一个标签会拿到一条死连接，
	// NewSession 报出来的错是语焉不详的 EOF，而不是「连接已断开，请重连」。
	client.AddOnDisconnect(func(error) {
		entry.mu.Lock()
		entry.dead = true
		entry.mu.Unlock()
		p.evict(hostID, entry)
	})

	p.mu.Lock()
	p.entries[hostID] = entry
	p.mu.Unlock()

	return client, p.releaserFor(hostID, entry), nil
}

// releaserFor 生成幂等的归还函数。
//
// 幂等是必需的：一次会话结束可能同时走「状态回调」和「Disconnect 里的 defer」
// 两条路径（测试 TestOwnerMappingOutlivesDisconnect 就抓到过这个泄漏）。
// 非幂等的 releaser 会把 refs 减两次，最后一个标签关掉时把新连接误关。
func (p *connPool) releaserFor(hostID string, e *connEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() { p.release(hostID, e) })
	}
}

func (p *connPool) release(hostID string, e *connEntry) {
	e.mu.Lock()
	e.refs--
	if e.refs > 0 {
		e.mu.Unlock()
		return
	}
	// 归零的这一刻必须就地标记为不可再用，不能等解锁后再 Close。
	//
	// 「refs--」与「Close()」之间只要存在一段无锁区间，并发的 Acquire 就能
	// 看到 refs=0、dead=false、Alive()=true（Close 还没执行）的瞬时状态，
	// 于是 refs 0→1 把这条即将关闭的连接租出去；紧接着 Close 落地，
	// 新会话拿到的是一条死连接，报错是语焉不详的 EOF 而不是「连接已断开」。
	// 把 dead=true 与 refs-- 放进同一临界区，Acquire 的 !e.dead 判定才成立。
	e.dead = true
	e.mu.Unlock()

	p.evict(hostID, e)
	_ = e.client.Close()
}

// evict 把条目从池里摘除（若它还在池中）。
//
// 只摘不关：真正的 Close 由 release 在引用归零时做。这里可能是断线触发的，
// 连接已经自己死了，但会话还留着回放——提前关掉会让正在回放的会话连最后一段
// 输出都读不出来。
func (p *connPool) evict(hostID string, e *connEntry) {
	p.mu.Lock()
	if cur, ok := p.entries[hostID]; ok && cur == e {
		delete(p.entries, hostID)
	}
	p.mu.Unlock()
}

// lockDialing 拿到该主机的建连锁（可重入保护：同一 hostID 串行，不同并行）。
func (p *connPool) lockDialing(hostID string) func() {
	p.mu.Lock()
	mu, ok := p.dialing[hostID]
	if !ok {
		mu = &sync.Mutex{}
		p.dialing[hostID] = mu
	}
	p.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// CloseAll 关闭全部连接（应用退出）。
func (p *connPool) CloseAll() {
	p.mu.Lock()
	entries := p.entries
	p.entries = make(map[string]*connEntry)
	p.mu.Unlock()
	for _, e := range entries {
		_ = e.client.Close()
	}
}

// Stats 返回当前池的连接数与总引用数（测试与排障用）。
func (p *connPool) Stats() (conns, refs int) {
	p.mu.Lock()
	list := make([]*connEntry, 0, len(p.entries))
	for _, e := range p.entries {
		list = append(list, e)
	}
	p.mu.Unlock()
	for _, e := range list {
		e.mu.Lock()
		refs += e.refs
		e.mu.Unlock()
	}
	return len(list), refs
}
