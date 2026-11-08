package clipboard

import (
	"encoding/json"
	"strings"

	"quickdock/internal/logger"
	"quickdock/services"
)

// pasteQueueKey 粘贴队列在 app_state 里的键。
//
// 为什么不留在前端组件里：剪贴板面板同时存在两份运行实例——主窗口导航页与
// Ctrl+~ 唤出的独立浮窗（两个 WebView）。队列若只存组件内 ref，在一边入队、
// 到另一边就看不见，而连续粘贴的真实用法恰恰是「主窗口挑好 → 浮窗里逐条贴」。
// 放后端是这两个窗口唯一共用的地方，顺带也让窗口重开时不丢。
const pasteQueueKey = "clipboard_paste_queue"

// PasteQueueEvent 队列变更广播给**所有**窗口用的事件名。
const PasteQueueEvent = "clipboard:queue"

// PasteQueueItem 队列里的一条。label 一起存：另一端窗口不查库也能渲染出
// 「1. 某段文本…」，省一次列表查询，也避免那条记录已被删除时显示空标签。
type PasteQueueItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// GetPasteQueue 读取跨窗口共享的粘贴队列。未设置或数据损坏时返回空数组。
func (a *ClipboardService) GetPasteQueue() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw, err := a.App.DB.GetSetting(pasteQueueKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return services.Ok([]PasteQueueItem{})
	}
	out := []PasteQueueItem{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		// 存坏了当空队列：这里报错只会让面板挂一个永远修不好的红条，
		// 而队列本来就是可随时重建的临时状态。
		return services.Ok([]PasteQueueItem{})
	}
	return services.Ok(out)
}

// SavePasteQueue 覆盖保存队列并广播给其它窗口。
//
// 由「改动队列的那一侧」调用；另一侧只接收事件、不再回写，否则两个窗口会
// 互相触发形成写放大（且谁最后写谁赢，队列会莫名其妙缺条）。
func (a *ClipboardService) SavePasteQueue(items []PasteQueueItem) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if items == nil {
		items = []PasteQueueItem{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return services.Fail(err)
	}
	if err := a.App.DB.SetSetting(pasteQueueKey, string(raw)); err != nil {
		return services.Fail(err)
	}
	a.emitPasteQueue(items)
	return services.Ok(nil)
}

// emitPasteQueue 广播队列快照。
//
// 带完整数组而不只是「变了」的信号：接收端拿到就能直接渲染，不必回查库；
// 事件本身也可能与本地写入交错，直接替换比「再读一次」更不容易落到旧值。
func (a *ClipboardService) emitPasteQueue(items []PasteQueueItem) {
	app := a.App.App()
	if app == nil {
		return
	}
	defer func() {
		// 事件系统在窗口已销毁/正在销毁时可能 panic，队列同步失败不该拖垮粘贴。
		if r := recover(); r != nil {
			logger.W("QuickDock: 粘贴队列广播失败: %v", r)
		}
	}()
	app.Event.Emit(PasteQueueEvent, items)
}
