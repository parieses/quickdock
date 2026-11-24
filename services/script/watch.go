package script

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

// 事件触发：监听一个文件或目录，变化后跑脚本。
//
// 为什么 5 秒轮询而不是 fsnotify：脚本目录就在本机磁盘上，一次 stat 是微秒级；
// 而 fsnotify 在 Windows 上有句柄数上限、网络盘静默失效、重命名要自己拼成
// 「删除+新建」三件事。为一个「低频监听 + 秒级容忍」的需求引入原生依赖和
// 一堆平台差异，不划算。
const (
	watchInterval = 5 * time.Second
	// watchDebounce 变化后的静默期：保存文件通常连着触发几次写事件
	// （编辑器先写临时文件再重命名），不防抖就是连跑三次。
	watchDebounce = 3 * time.Second
)

// watchState 单个脚本的监听状态。
type watchState struct {
	// sig 变化指纹；空串表示上一轮路径不可用。
	sig string
	// changed 最后一次观察到变化的时间（零值 = 已处理完）。
	changed time.Time
}

// StartWatcher 启动监听循环（幂等）。由 Startup 调用。
func (s *ScriptService) StartWatcher() {
	s.watchMu.Lock()
	if s.watchOn {
		s.watchMu.Unlock()
		return
	}
	s.watchOn = true
	if s.watchStates == nil {
		s.watchStates = map[string]*watchState{}
	}
	// 每次启动一个新 stop 通道：StopWatcher 关掉它，循环随之退出。
	// 原来没有停止路径，watchOn 一旦置位就再也没有机会置回，Shutdown 之后
	// 循环仍在跑（并且继续读库、跑脚本）。
	stop := make(chan struct{})
	s.watchStop = stop
	s.watchMu.Unlock()
	go s.watchLoop(stop)
}

// StopWatcher 停止监听循环（幂等）。应用退出时调用。
func (s *ScriptService) StopWatcher() {
	s.watchMu.Lock()
	if !s.watchOn {
		s.watchMu.Unlock()
		return
	}
	s.watchOn = false
	stop := s.watchStop
	s.watchStop = nil
	s.watchMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func (s *ScriptService) watchLoop(stop <-chan struct{}) {
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.tickWatchers()
		case <-stop:
			return
		}
	}
}

// tickWatchers 一轮扫描。
//
// 首次见到某脚本的监听路径时只记基线、不触发：否则应用一启动就会把所有
// 「监听着某个目录」的脚本全部跑一遍，而它们并没有收到任何新输入。
func (s *ScriptService) tickWatchers() {
	if s.App.DB == nil {
		return
	}
	list, err := s.App.DB.ListScripts()
	if err != nil {
		return
	}
	now := time.Now()
	seen := make(map[string]bool, len(list))

	for i := range list {
		sc := &list[i]
		if sc.WatchPath == "" {
			continue
		}
		seen[sc.ID] = true
		path := s.resolveWatchPath(sc)
		sig, err := watchSignature(path)
		if err != nil {
			s.watchMu.Lock()
			st, ok := s.watchStates[sc.ID]
			if !ok || st.sig != "" {
				logger.W("[script] 监听路径不可用 %s: %v", path, err)
				s.watchStates[sc.ID] = &watchState{}
			}
			s.watchMu.Unlock()
			continue
		}

		s.watchMu.Lock()
		st, ok := s.watchStates[sc.ID]
		if !ok {
			s.watchStates[sc.ID] = &watchState{sig: sig}
			s.watchMu.Unlock()
			continue
		}
		if sig != st.sig {
			// 签名变了：更新并进入静默期。
			oldSig := st.sig
			st.sig = sig
			// 「从不可用恢复」不算变化：外接盘插回来的那一刻跑一次脚本，
			// 用户完全预期不到，而且往往发生在什么都没做的时候。
			if oldSig != "" && st.changed.IsZero() {
				st.changed = now
			}
			s.watchMu.Unlock()
			continue
		}
		due := !st.changed.IsZero() && now.Sub(st.changed) >= watchDebounce
		if due {
			st.changed = time.Time{}
		}
		s.watchMu.Unlock()

		if due {
			// 正在跑就跳过这一轮：脚本还在处理上一批文件，再塞一次只会被
			// 并发闸门拒掉，白白在通知里留一条「正在运行中」。
			if !s.gate.active(sc.ID) {
				s.RunScriptWithTrigger(sc.ID, "", TriggerEvent)
			}
		}
	}

	// 清理已删除脚本或已关闭监听的条目，防止 map 随使用无限增长。
	s.watchMu.Lock()
	for id := range s.watchStates {
		if !seen[id] {
			delete(s.watchStates, id)
		}
	}
	s.watchMu.Unlock()
}

// watchSignature 生成路径的「变化指纹」。
//
// 文件取 mtime+size；目录取 mtime+条目数（目录 mtime 只在增删项时变，
// 内容修改不变——这正是「外部程序往目录里丢文件」这个主场景需要的语义）。
func watchSignature(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return fmt.Sprintf("f:%d:%d", info.ModTime().UnixNano(), info.Size()), nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("d:%d:%d", info.ModTime().UnixNano(), len(entries)), nil
}

// resolveWatchPath 把用户填的路径展开成绝对路径（允许相对脚本目录写）。
func (s *ScriptService) resolveWatchPath(sc *db.Script) string {
	if sc.WatchPath == "" {
		return ""
	}
	if filepath.IsAbs(sc.WatchPath) {
		return filepath.Clean(sc.WatchPath)
	}
	return filepath.Join(s.dir(sc.ID), filepath.FromSlash(sc.WatchPath))
}

// TestWatchPath 校验监听路径是否可读（保存前给即时反馈）。
func (s *ScriptService) TestWatchPath(path string) *services.ApiResult {
	if path == "" {
		return services.Ok(map[string]any{"ok": false})
	}
	info, err := os.Stat(path)
	if err != nil {
		return services.FailMsg("路径不存在或不可访问：" + err.Error())
	}
	// 只读检查：监听一个自己都读不了的目录，脚本跑起来也是必然失败。
	if _, err := os.ReadDir(path); err != nil && info.IsDir() {
		return services.FailMsg("目录不可读：" + err.Error())
	}
	return services.Ok(map[string]any{"ok": true, "isDir": info.IsDir()})
}
