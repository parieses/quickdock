// Package logtail 把 internal/logtail 的同步读取内核包成一个带生命周期的 Wails 服务。
//
// 分工：内核（internal/logtail）只做「给我新增的行」，不调度、不推送；
// 本包负责「多久看一次」「推给谁」「什么时候停」。
package logtail

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/logtail"
	"quickdock/services"
)

// 轮询间隔：有新数据时密轮询，连续空闲后退避一圈怠速轮询。
// 500ms 是「肉眼觉得实时」的下限；1.5s 是静态文件的常规落盘节奏，再快纯属浪费。
const (
	intervalActive = 500 * time.Millisecond
	intervalIdle   = 1500 * time.Millisecond
	idleThreshold  = 6 // 连续 6 次没新数据（约 3s）后怠速
)

// LogTailService 尾随会话的门面服务。
type LogTailService struct {
	App *services.AppService

	mu       sync.Mutex
	sessions map[string]*session
	autoID   atomic.Int64
}

// NewLogTailService 创建尾随门面服务，app 为宿主服务引用（用于事件推送）。
func NewLogTailService(app *services.AppService) *LogTailService {
	return &LogTailService{App: app, sessions: map[string]*session{}}
}

// session 一个被尾随的文件。
type session struct {
	id    string
	path  string
	name  string
	size  int64
	lines int64 // 累计推送行数，供前端显示「已收多少」
	rot   int64 // 累计轮转次数

	tailer *logtail.Tailer
	pause  chan struct{}
	resume chan struct{}
	stop   chan struct{}
	done   chan struct{}
}

// LogTailOpenResult 打开一个尾随会话的返回结构。
type LogTailOpenResult struct {
	ID     string   `json:"id"`
	Path   string   `json:"path"`
	Name   string   `json:"name"`
	Size   int64    `json:"size"`
	Lines  []string `json:"lines"`
	Exists bool     `json:"exists"`
}

// LogTailOpen 打开一个文件并开始尾随，返回会话 ID 与末尾若干行。
//
// 事件流：此后每次有新增都会发 `logtail:line`，载荷 {id, lines, rotated, seq}。
// 前端按 id 分流到对应标签，不做广播也不是全局单流 —— 同时尾随多个文件时
// 单流会互相串台。
//
// tailLines <= 0 时用内核默认值。
func (s *LogTailService) LogTailOpen(path string, tailLines int) *services.ApiResult {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return services.FailMsg("路径为空")
	}
	t, err := logtail.NewTailer(path)
	if err != nil {
		// 文件不存在/是目录：由调用方负责提前说清楚（开源 picking 对话框不会出现这两种）
		return services.Fail(err)
	}
	snap, err := t.Tail(tailLines)
	if err != nil {
		return services.Fail(err)
	}
	s.mu.Lock()
	id := fmt.Sprintf("lt%d", s.autoID.Add(1))
	se := &session{
		id:     id,
		path:   path,
		name:   filepath.Base(path),
		size:   t.Size(),
		tailer: t,
		pause:  make(chan struct{}),
		resume: make(chan struct{}),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	s.sessions[id] = se
	s.mu.Unlock()

	se.lines = int64(len(snap.Lines))
	go s.loop(se)
	return services.Ok(&LogTailOpenResult{
		ID:     id,
		Path:   path,
		Name:   filepath.Base(path),
		Size:   t.Size(),
		Lines:  snap.Lines,
		Exists: true,
	})
}

// LogTailStop 停止并移除一个会话。重复调用是空操作。
//
// 前端切走标签 / 关闭页面必须调它：不调的话 goroutine 会一直 stat 到进程退出。
func (s *LogTailService) LogTailStop(id string) *services.ApiResult {
	s.mu.Lock()
	se := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if se == nil {
		return services.Ok(false)
	}
	close(se.stop)
	<-se.done // 等轮询 goroutine 真的退出，避免它还在写已失效的会话表
	return services.Ok(true)
}

// LogTailPause 暂停 / 恢复某个会话的磁盘读取。
//
// 为什么不是前端丢掉事件就算了：暂停的语义是「别再读了」——用户暂停时通常正在
// 翻历史行，后端继续空转读取既没意义又持续占着一个 stat。
func (s *LogTailService) LogTailPause(id string, paused bool) *services.ApiResult {
	s.mu.Lock()
	se := s.sessions[id]
	s.mu.Unlock()
	if se == nil {
		return services.Ok(false)
	}
	if paused {
		select {
		case se.pause <- struct{}{}:
		default:
		}
	} else {
		select {
		case se.resume <- struct{}{}:
		default:
		}
	}
	return services.Ok(true)
}

// LogTailStats 返回会话的实时读数（已收行数 / 当前文件大小 / 轮转次数）。
// 前端用它显示状态条；会话已关时返回 null。
func (s *LogTailService) LogTailStats(id string) *services.ApiResult {
	s.mu.Lock()
	se := s.sessions[id]
	s.mu.Unlock()
	if se == nil {
		return services.Ok(nil)
	}
	return services.Ok(map[string]int64{
		"lines":  atomic.LoadInt64(&se.lines),
		"size":   se.tailer.Size(),
		"rotate": atomic.LoadInt64(&se.rot),
	})
}

// ServiceShutdown 退出时收拢所有会话（normal power-off path）。
// 强杀进程时这条回调不会执行，但那种情况下 goroutine 随进程一起消失，无需清理。
func (s *LogTailService) ServiceShutdown() error {
	s.mu.Lock()
	all := make([]*session, 0, len(s.sessions))
	for _, se := range s.sessions {
		all = append(all, se)
	}
	s.sessions = map[string]*session{}
	s.mu.Unlock()
	for _, se := range all {
		select {
		case <-se.done:
		default:
			close(se.stop)
			<-se.done
		}
	}
	return nil
}

// loop 单个会话的轮询循环。退出时关闭 done，无论从哪条分支出去。
func (s *LogTailService) loop(se *session) {
	defer close(se.done)
	interval := intervalActive
	idle := 0
	paused := false
	for {
		select {
		case <-se.stop:
			return
		case <-se.pause:
			// 暂停期间保持 1s 心跳：既能及时响应 resume，也不会持续读盘
			paused = true
			interval = time.Second
			continue
		case <-se.resume:
			paused = false
			idle = 0
			interval = intervalActive
			continue
		case <-time.After(interval):
		}
		if paused {
			// 暂停期间不读盘，但仍保持 1s 心跳以便及时响应 resume
			interval = time.Second
			continue
		}
		snap, err := se.tailer.Poll(logtail.MaxBufferedLines)
		if err != nil {
			// 文件被删/权限变化：不退出循环——日志轮转期间临时 stat 失败很常见，
			// 退出会让用户以为功能坏了。慢速重试，等文件回来。
			logger.W("logtail: poll failed (id=%s): %v", se.id, err)
			interval = intervalIdle
			continue
		}
		if snap.Rotated {
			atomic.AddInt64(&se.rot, 1)
			s.emit(se.id, nil, true)
		}
		if len(snap.Lines) == 0 {
			idle++
			if idle >= idleThreshold {
				interval = intervalIdle
			}
			continue
		}
		idle = 0
		interval = intervalActive
		atomic.AddInt64(&se.lines, int64(len(snap.Lines)))
		s.emit(se.id, snap.Lines, false)
	}
}

// emit 把新增行推给前端。拿不到 app（应用尚未就绪/正在退出）时静默丢弃：
// 尾随是锦上添花的功能，不该因为它让主流程报错。
func (s *LogTailService) emit(id string, lines []string, rotated bool) {
	app := s.App.App()
	if app == nil {
		return
	}
	payload := map[string]interface{}{
		"id":      id,
		"lines":   lines,
		"rotated": rotated,
	}
	if lines == nil {
		payload["lines"] = []string{}
	}
	app.Event.Emit("logtail:line", payload)
}

// LogTailStat 报告当前活跃会话数，仅供诊断引用（正常情况下应为 0 或 1，
// 一直是 3 说明有页面忘了 LogTailStop）。
func (s *LogTailService) LogTailStat() *services.ApiResult {
	s.mu.Lock()
	n := len(s.sessions)
	s.mu.Unlock()
	return services.Ok(n)
}
