package sshx

import (
	"encoding/base64"
	"sync"
	"testing"
	"time"
)

// ---- 连接池：#13 的地基 ----
//
// 验收标准是「同主机开 3 个标签只建立 1 条 TCP」。这件事一旦做错，症状很隐蔽：
// 功能全都正常，只是每次开标签都慢 200-400ms（跨网更久），服务器 MaxSessions
// 被白耗。所以下面每个用例都用服务端计数器直接断言 TCP 条数，不靠推测。

func TestConnPoolReusesOneTCPForSameHost(t *testing.T) {
	svc := newTestService(t)
	host, port, rec, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	// 基准线：saveTrustedHost 里的 TestHost 建过一条（连完就关），
	// 从这里往后新开的标签只应该复用池里那条，或者建一条新的。
	base, _, _ := rec.counts()

	var sids []string
	for i := 0; i < 3; i++ {
		r := svc.Connect(h.ID, 80, 24)
		if r.Code != 0 {
			t.Fatalf("第 %d 个标签建立失败: %+v", i, r)
		}
		sids = append(sids, r.Data.(SshConnectResult).SessionID)
	}

	tcp, channels, _ := rec.counts()
	if got := tcp - base; got != 1 {
		t.Fatalf("3 个标签应只新建 1 条 TCP，实际新建 %d 条", got)
	}
	if channels != 3 {
		t.Fatalf("服务端应看到 3 个会话（channel），实际 %d", channels)
	}

	conns, refs := svc.pool.Stats()
	if conns != 1 || refs != 3 {
		t.Fatalf("池状态不对：连接 %d 条、引用 %d 个，期望 1/3", conns, refs)
	}

	// 逐个关闭，最后一个才关连接。
	for i, sid := range sids {
		if r := svc.Disconnect(sid); r.Code != 0 {
			t.Fatalf("关闭第 %d 个标签失败: %+v", i, r)
		}
		wantConns, wantRefs := 1, 3-(i+1)
		if wantRefs == 0 {
			wantConns = 0
		}
		if c, rf := svc.pool.Stats(); c != wantConns || rf != wantRefs {
			t.Fatalf("关闭第 %d 个后池状态 = %d 连接/%d 引用，期望 %d/%d", i, c, rf, wantConns, wantRefs)
		}
	}
	if conns, _ := svc.pool.Stats(); conns != 0 {
		t.Fatalf("全部关闭后池里仍有 %d 条连接（泄漏）", conns)
	}
}

// TestConnPoolConcurrentAcquireDialsOnce 并发开标签时也只应有一条连接。
//
// 这条比顺序开更容易踩：两个 goroutine 同时看到池里没有连接，就各自拨号，
// 结果建出两条，第二条立刻变多余（而且 refcount 会算错，归还时把新连接误关）。
func TestConnPoolConcurrentAcquireDialsOnce(t *testing.T) {
	svc := newTestService(t)
	host, port, rec, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	base, _, _ := rec.counts()

	const n = 5
	var wg sync.WaitGroup
	sids := make([]string, n)
	fails := make([]string, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 尽量让所有 goroutine 同时冲进 Acquire
			r := svc.Connect(h.ID, 80, 24)
			if r.Code != 0 {
				fails[i] = r.Msg
				return
			}
			sids[i] = r.Data.(SshConnectResult).SessionID
		}(i)
	}
	close(start)
	wg.Wait()

	for i, msg := range fails {
		if msg != "" {
			t.Fatalf("并发建立第 %d 个标签失败: %s", i, msg)
		}
	}

	tcp, channels, _ := rec.counts()
	if got := tcp - base; got != 1 {
		t.Fatalf("并发开 %d 个标签应只新建 1 条 TCP，实际 %d 条", n, got)
	}
	if channels != n {
		t.Fatalf("服务端应看到 %d 个会话，实际 %d", n, channels)
	}

	for _, sid := range sids {
		svc.Disconnect(sid)
	}
	if conns, refs := svc.pool.Stats(); conns != 0 || refs != 0 {
		t.Fatalf("全部关闭后池状态 = %d 连接/%d 引用，应为 0/0", conns, refs)
	}
}

// TestConnPoolDistinctHostsDoNotShare 不同主机必须各拿各的连接，
// 否则会把 A 服务器的凭据用到 B 上——这是安全问题，不是效率问题。
func TestConnPoolDistinctHostsDoNotShare(t *testing.T) {
	svc := newTestService(t)
	hostA, portA, recA, _ := startPtySvcServer(t)
	hostB, portB, recB, _ := startPtySvcServer(t)
	hA := saveTrustedHost(t, svc, hostA, portA)
	hB := saveTrustedHost(t, svc, hostB, portB)

	rA := svc.Connect(hA.ID, 80, 24)
	rB := svc.Connect(hB.ID, 80, 24)
	if rA.Code != 0 || rB.Code != 0 {
		t.Fatalf("建立失败: A=%+v B=%+v", rA, rB)
	}
	sidA := rA.Data.(SshConnectResult).SessionID
	sidB := rB.Data.(SshConnectResult).SessionID

	if _, channels, _ := recA.counts(); channels != 1 {
		t.Fatalf("A 服务端应只看到 1 个会话，实际 %d（连接被错误共享）", channels)
	}
	if _, channels, _ := recB.counts(); channels != 1 {
		t.Fatalf("B 服务端应只看到 1 个会话，实际 %d", channels)
	}
	if conns, refs := svc.pool.Stats(); conns != 2 || refs != 2 {
		t.Fatalf("池状态 = %d 连接/%d 引用，期望 2/2", conns, refs)
	}

	// 关掉 A 的标签不能影响 B：这是 refcount 按主机隔离的直接验证。
	svc.Disconnect(sidA)
	if conns, refs := svc.pool.Stats(); conns != 1 || refs != 1 {
		t.Fatalf("关掉 A 后池状态 = %d 连接/%d 引用，期望 1/1", conns, refs)
	}
	if _, _, open := recB.counts(); open != 1 {
		t.Fatalf("关掉 A 标签时把 B 的会话也关了（残留 %d）", open)
	}
	svc.Disconnect(sidB)
}

// TestConnPoolReleaseIdempotent 归还函数必须幂等。
//
// 一次会话结束可能走两条路径：状态回调（sess.Wait() 的 goroutine）与
// Disconnect 里的清理。非幂等的 releaser 会把计数减两次，最后一个标签关掉时
// 把仍然在用的新连接误关 —— 表现为「明明还有标签在跑，输出却突然全断」。
func TestConnPoolReleaseIdempotent(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	r := svc.Connect(h.ID, 80, 24)
	if r.Code != 0 {
		t.Fatalf("建立失败: %+v", r)
	}
	sid := r.Data.(SshConnectResult).SessionID

	// 再开一个，让引用数变成 2。
	r2 := svc.Connect(h.ID, 80, 24)
	if r2.Code != 0 {
		t.Fatalf("建立第二个失败: %+v", r2)
	}
	sid2 := r2.Data.(SshConnectResult).SessionID

	// 同一个会话重复归还 5 次。
	for i := 0; i < 5; i++ {
		svc.dropRelease(sid)
	}
	if conns, refs := svc.pool.Stats(); conns != 1 || refs != 1 {
		t.Fatalf("重复归还不应影响计数，实际 = %d 连接/%d 引用，期望 1/1", conns, refs)
	}

	// 第二个标签还能正常用，就证明连接没被误关。
	if _, ok := svc.manager.Get(sid2); !ok {
		t.Fatal("第二个会话被误关了")
	}
	svc.Disconnect(sid2)
}

// TestConnPoolDropsDeadConnection 断开的连接要从池里摘掉，但引用数不能动。
//
// 两件事必须同时成立：
//   - 摘掉：否则下一个标签会拿到死连接，NewSession 报 EOF 而不是「请重连」。
//   - 不动 refs：断开的会话还留在 Manager 里供回放，它占的引用要等用户
//     主动关闭时才归还。若这里清零，后面 Release 会把新连接的计数减成负数。
func TestConnPoolDropsDeadConnection(t *testing.T) {
	svc := newTestService(t)
	host, port, rec, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	r := svc.Connect(h.ID, 80, 24)
	if r.Code != 0 {
		t.Fatalf("建立失败: %+v", r)
	}
	sid := r.Data.(SshConnectResult).SessionID
	if conns, refs := svc.pool.Stats(); conns != 1 || refs != 1 {
		t.Fatalf("初始池状态 = %d 连接/%d 引用，期望 1/1", conns, refs)
	}

	// 从服务端掐断连接，走真实断连路径（client 的 watch goroutine → 断连回调 → 摘表）。
	rec.killConns()
	waitSvcUntil(t, "死连接被摘出池", func() bool {
		conns, _ := svc.pool.Stats()
		return conns == 0
	})

	// 再开一个标签：必须重拨，而不是复用死连接。
	r2 := svc.Connect(h.ID, 80, 24)
	if r2.Code != 0 {
		t.Fatalf("死连接后重连失败: %+v", r2)
	}
	sid2 := r2.Data.(SshConnectResult).SessionID
	if sid2 == sid {
		t.Fatal("不该复用同一个会话 ID")
	}
	if conns, refs := svc.pool.Stats(); conns != 1 || refs != 1 {
		t.Fatalf("重拨后池状态 = %d 连接/%d 引用，期望 1/1", conns, refs)
	}

	// 旧会话还留在 Manager 里（供回放），归还会走到已失效的条目上——
	// 必须安全无副作用，不能误关掉刚建的新连接。
	if _, ok := svc.manager.Get(sid); !ok {
		t.Fatal("断开的会话应仍留在管理器里供回放")
	}
	svc.Disconnect(sid)
	if conns, refs := svc.pool.Stats(); conns != 1 || refs != 1 {
		t.Fatalf("清理旧会话后新连接被误关了：池状态 = %d 连接/%d 引用，期望 1/1", conns, refs)
	}

	svc.Disconnect(sid2)
	if conns, _ := svc.pool.Stats(); conns != 0 {
		t.Fatalf("收尾后池里仍有 %d 条连接", conns)
	}
}

// TestConnPoolCloseAllShutdown 应用退出要一次关掉全部连接。
func TestConnPoolCloseAllShutdown(t *testing.T) {
	svc := newTestService(t)
	hostA, portA, _, _ := startPtySvcServer(t)
	hostB, portB, _, _ := startPtySvcServer(t)
	hA := saveTrustedHost(t, svc, hostA, portA)
	hB := saveTrustedHost(t, svc, hostB, portB)

	if r := svc.Connect(hA.ID, 80, 24); r.Code != 0 {
		t.Fatalf("A 失败: %+v", r)
	}
	if r := svc.Connect(hB.ID, 80, 24); r.Code != 0 {
		t.Fatalf("B 失败: %+v", r)
	}
	if conns, _ := svc.pool.Stats(); conns != 2 {
		t.Fatalf("应有 2 条连接，实际 %d", conns)
	}

	svc.Shutdown()

	if conns, refs := svc.pool.Stats(); conns != 0 || refs != 0 {
		t.Fatalf("Shutdown 后池状态 = %d 连接/%d 引用，应为 0/0", conns, refs)
	}
}

// TestPoolDialingLockSerializesSameHost 拨号锁必须按 hostID 生效：
// 同主机串行（避免并发建出两条），不同主机并行（不能全局串行）。
func TestPoolDialingLockSerializesSameHost(t *testing.T) {
	p := newConnPool()

	// 同主机：两个 goroutine 不应同时进入临界区。
	var mu sync.Mutex
	inside, maxInside := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := p.lockDialing("same")
			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("同主机的拨号临界区并发度应为 1，实际 %d", maxInside)
	}

	// 不同主机：应能真正并行，否则开 5 台主机会被串成 5 倍等待。
	enter := make(chan struct{}, 2)
	releaseCh := make(chan struct{})
	go func() {
		unlock := p.lockDialing("h1")
		defer unlock()
		enter <- struct{}{}
		<-releaseCh
	}()
	go func() {
		unlock := p.lockDialing("h2")
		defer unlock()
		enter <- struct{}{}
		<-releaseCh
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-enter:
		case <-time.After(2 * time.Second):
			close(releaseCh)
			t.Fatal("不同主机的拨号锁互相阻塞了，应当并行")
		}
	}
	close(releaseCh)
}

// TestMultiTabShareConnectionEndToEnd 走完整服务路径的验收用例：
// 同主机开 3 个标签 → 服务端只看到 1 条 TCP、3 个独立会话 → 关掉一个标签
// 不影响另外两个的输入输出。
//
// 前面的用例测的是池本身，这个测的是「池 + Manager + 事件」合起来对不对。
// 只测池会漏掉一类问题：连接共享了，但三个会话的数据流互相串了
// （事件按 sessionId 分发，串了就是 A 终端看到 B 的输出）。
func TestMultiTabShareConnectionEndToEnd(t *testing.T) {
	svc := newTestService(t)
	host, port, rec, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	base, _, _ := rec.counts()

	var sids []string
	for i := 0; i < 3; i++ {
		r := svc.Connect(h.ID, 80, 24)
		if r.Code != 0 {
			t.Fatalf("第 %d 个标签建立失败: %s", i, r.Msg)
		}
		sids = append(sids, r.Data.(SshConnectResult).SessionID)
	}
	if len(sids) != 3 {
		t.Fatalf("应有 3 个会话，实际 %d", len(sids))
	}
	// 会话 ID 必须互不相同，否则事件分发会串。
	seen := map[string]bool{}
	for _, id := range sids {
		if seen[id] {
			t.Fatalf("会话 ID 重复: %s", id)
		}
		seen[id] = true
	}
	if tcp, _, _ := rec.counts(); tcp-base != 1 {
		t.Fatalf("3 个标签应只建 1 条 TCP，实际 %d", tcp-base)
	}

	// 每个标签的尺寸独立：三个终端可以不一样大。
	sizes := [][2]int{{100, 30}, {120, 40}, {80, 24}}
	for i, sid := range sids {
		if r := svc.Resize(sid, sizes[i][0], sizes[i][1]); r.Code != 0 {
			t.Fatalf("第 %d 个标签 Resize 失败: %s", i, r.Msg)
		}
	}
	waitSvcUntil(t, "三个窗口尺寸请求都到达", func() bool {
		_, _, open := rec.counts()
		_ = open
		n, _, _ := rec.win()
		return n >= 3
	})
	if _, w, hgt := rec.win(); w != 80 || hgt != 24 {
		t.Fatalf("最后改尺寸的应是第 3 个标签 80x24，实际 %dx%d", w, hgt)
	}

	// 关掉中间那个，另外两个必须还活着。
	if r := svc.Disconnect(sids[1]); r.Code != 0 {
		t.Fatalf("关闭中间标签失败: %s", r.Msg)
	}
	for i, sid := range []string{sids[0], sids[2]} {
		if _, ok := svc.manager.Get(sid); !ok {
			t.Fatalf("关闭中间标签时误关了标签 %d", i)
		}
	}
	if conns, refs := svc.pool.Stats(); conns != 1 || refs != 2 {
		t.Fatalf("关一个后池状态 = %d 连接/%d 引用，期望 1/2", conns, refs)
	}
	// 剩下两个标签仍能写入（连接没被误关）。
	if r := svc.Write(sids[0], base64.StdEncoding.EncodeToString([]byte("echo hi"))); r.Code != 0 {
		t.Fatalf("关闭中间标签后其余标签无法写入: %s", r.Msg)
	}

	svc.Disconnect(sids[0])
	svc.Disconnect(sids[2])
	if conns, _ := svc.pool.Stats(); conns != 0 {
		t.Fatalf("全部关闭后池里仍有 %d 条连接", conns)
	}
}
