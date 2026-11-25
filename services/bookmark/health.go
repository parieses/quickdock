package bookmark

import (
	"context"
	"sync"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/linkcheck"
	"quickdock/internal/logger"
	"quickdock/services"
)

// 死链检查：后台跑、前端轮询。
//
// 为什么不在 IPC 里同步跑完：几百条链接 × 单条最长 10 秒，一屏点下去要等几分钟，
// Wails 调用会一直挂在那里，用户既看不到进度也没法取消。改成「启动 → 立即返回 →
// 轮询状态」，与下载器、知识库建索引同一套交互模型。

// linkWorkers 并发数。取 6：单站点并发过高会被 WAF 拦（全变成 403），
// 太低又跑不完一遍。
const linkWorkers = 6

// LinkCheckStatus 当前/最近一次检查的进度快照。
type LinkCheckStatus struct {
	Running    bool   `json:"running"`
	Total      int    `json:"total"`
	Done       int    `json:"done"`
	OK         int    `json:"ok"`
	Broken     int    `json:"broken"`
	Moved      int    `json:"moved"`
	Blocked    int    `json:"blocked"`
	Failed     int    `json:"failed"`
	TimedOut   int    `json:"timedOut"`
	Deep       bool   `json:"deep"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
}

// linkState 进程内单例。检查是「全库一次」的语义，同时跑两份没有意义。
var linkState = struct {
	sync.Mutex
	status LinkCheckStatus
}{}

// StartLinkCheck 启动一轮检查。ids 为空表示全库网页条目。
//
// deep 为真时顺带抓正文摘要，用来判断「页面内容悄悄变了」；代价是流量与耗时都翻倍，
// 默认关闭。
func (s *BookmarkService) StartLinkCheck(ids []string, deep bool) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}

	linkState.Lock()
	if linkState.status.Running {
		linkState.Unlock()
		return services.FailMsg("检查正在进行中")
	}
	linkState.Unlock()

	var items []db.WebItem
	var err error
	if len(ids) == 0 {
		items, err = s.App.DB.ListWebItems(0)
	} else {
		for _, id := range ids {
			if it, e := s.App.DB.GetWebItem(id); e == nil && it != nil {
				items = append(items, *it)
			}
		}
	}
	if err != nil {
		return services.Fail(err)
	}
	if len(items) == 0 {
		return services.FailMsg("没有可检查的网页条目")
	}

	linkState.Lock()
	linkState.status = LinkCheckStatus{
		Running:   true,
		Total:     len(items),
		Deep:      deep,
		StartedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	linkState.Unlock()

	go s.runLinkCheck(items, deep)
	return services.Ok(linkState.status)
}

func (s *BookmarkService) runLinkCheck(items []db.WebItem, deep bool) {
	defer logger.RecoverToLog("bookmark:runLinkCheck")

	jobs := make(chan db.WebItem)
	var wg sync.WaitGroup

	for i := 0; i < linkWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer logger.RecoverToLog("bookmark:linkWorker")
			for it := range jobs {
				res := linkcheck.Check(context.Background(), it.URL, deep)
				s.persistHealth(it.ID, res, deep)
				s.bumpStatus(res.Status)
			}
		}()
	}

	for _, it := range items {
		jobs <- it
	}
	close(jobs)
	wg.Wait()

	linkState.Lock()
	linkState.status.Running = false
	linkState.status.FinishedAt = time.Now().Format("2006-01-02 15:04:05")
	final := linkState.status
	linkState.Unlock()

	logger.I("QuickDock: 死链检查完成 共=%d 正常=%d 死链=%d 跳转=%d 受限=%d 超时=%d 异常=%d",
		final.Total, final.OK, final.Broken, final.Moved, final.Blocked, final.TimedOut, final.Failed)
}

// persistHealth 落库。写库失败只记日志：单条写失败不该中断整轮检查。
func (s *BookmarkService) persistHealth(itemID string, res linkcheck.Result, deep bool) {
	h := db.ItemHealth{
		ItemID:      itemID,
		Status:      string(res.Status),
		Code:        res.Code,
		FinalURL:    res.FinalURL,
		ErrMsg:      res.Err,
		ContentHash: res.ContentHash,
		CheckedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}
	// 「内容变了」只在深度检查且有上一轮摘要时成立：首次检查没有基线，
	// 把每一条都标成已变化只会制造噪声。
	if deep && res.ContentHash != "" {
		if prev := s.App.DB.PrevContentHash(itemID); prev != "" && prev != res.ContentHash {
			h.Changed = true
		}
	}
	if err := s.App.DB.UpsertItemHealth(h); err != nil {
		logger.W("QuickDock: 写入检查结果失败 id=%s: %v", itemID, err)
	}
}

func (s *BookmarkService) bumpStatus(st linkcheck.Status) {
	linkState.Lock()
	defer linkState.Unlock()
	linkState.status.Done++
	switch st {
	case linkcheck.StatusOK:
		linkState.status.OK++
	case linkcheck.StatusBroken:
		linkState.status.Broken++
	case linkcheck.StatusMoved:
		linkState.status.Moved++
	case linkcheck.StatusBlocked:
		linkState.status.Blocked++
	case linkcheck.StatusTimeout:
		linkState.status.TimedOut++
	default:
		linkState.status.Failed++
	}
}

// LinkCheckStatus 返回进度快照，前端每秒轮询一次。
func (s *BookmarkService) LinkCheckStatus() *services.ApiResult {
	linkState.Lock()
	defer linkState.Unlock()
	return services.Ok(linkState.status)
}

// ListHealth 列出检查结果，status 为空表示全部。
func (s *BookmarkService) ListHealth(status string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	rows, err := s.App.DB.ListItemHealth(status)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(rows)
}

// ClearHealth 清空检查结果（重新扫一遍之前用）。
func (s *BookmarkService) ClearHealth() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.ClearItemHealth(); err != nil {
		return services.Fail(err)
	}
	return services.OkMsg(nil, "已清空检查结果")
}

// ApplyMovedURL 把条目地址换成跳转后的最终地址（「地址已变」时的一键更新）。
func (s *BookmarkService) ApplyMovedURL(itemID, newURL string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if newURL == "" {
		return services.FailMsg("目标地址为空")
	}
	if err := s.App.DB.UpdateItem(itemID, map[string]interface{}{"value": newURL}); err != nil {
		return services.Fail(err)
	}
	return services.OkMsg(nil, "地址已更新")
}
