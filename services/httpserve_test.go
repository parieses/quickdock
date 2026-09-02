//go:build windows

package services

import (
	"net"
	"testing"
)

// freePort 分配一个当前空闲的 TCP 端口（绑定 :0 后读取并立即释放）。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("分配空闲端口失败: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestHTTPServeRunningPersistence 验证 HTTP Serve 的运行态能持久化，
// 并在应用重启（新建 manager 从同一目录加载）时自动恢复（打磨项 #1）。
func TestHTTPServeRunningPersistence(t *testing.T) {
	dir := t.TempDir()
	m := newHTTPServeManagerAt(dir)

	port := freePort(t)
	s, err := m.Create("demo", t.TempDir(), port)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(s.ID); err != nil {
		t.Fatal(err)
	}
	if !m.IsRunning(s.ID) {
		t.Fatal("启动后 IsRunning 应为 true")
	}
	if !m.List()[0].Running {
		t.Fatal("List() 应返回 Running=true")
	}

	// 停止后应持久化为 false
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	if m.IsRunning(s.ID) {
		t.Fatal("停止后 IsRunning 应为 false")
	}
	if m.List()[0].Running {
		t.Fatal("停止后 List() 应返回 Running=false")
	}

	// 模拟重启：running=false 不应自动拉起
	m2 := newHTTPServeManagerAt(dir)
	if m2.IsRunning(s.ID) {
		t.Fatal("running=false 时重启不应自动拉起")
	}

	// 再次启动并重启，应自动恢复
	if err := m2.Start(s.ID); err != nil {
		t.Fatal(err)
	}
	// 模拟「应用退出」：仅释放端口，不把 running 落盘为 false（Stop 会落盘 false）
	m2.mu.Lock()
	if e := m2.servers[s.ID]; e != nil && e.ln != nil {
		_ = e.ln.Close()
		e.ln = nil
		e.srv = nil
	}
	m2.mu.Unlock()

	m3 := newHTTPServeManagerAt(dir)
	if !m3.IsRunning(s.ID) {
		t.Fatal("running=true 重启后应自动恢复运行")
	}
	_ = m3.Stop(s.ID) // 清理端口
}
