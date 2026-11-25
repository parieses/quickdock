// 书签条目缺少网站图标时的补齐入口。
//
// 为什么不在导入时一次抓完：1000 条书签背后可能有 400 个站点，每条都走一次
// 「3 秒预算」的 favicon 探测，导入会从 1 秒变成几分钟。改成页面按需小批量补，
// 用户先看到列表，图标在后台一批批出现；已抓过的站点还有磁盘缓存，二次几乎免费。
package bookmark

import (
	"sync"

	"quickdock/internal/db"
	"quickdock/internal/platform"
	"quickdock/services"
)

// iconWorkers 单批并发的抓取数。
//
// 6 是经验值：每个请求最多 3 秒，6 个并发能在 10 秒内跑完 20~30 条，
// 又不会对同一站点同时打出太多请求（并发再高只是更快撞到限流）。
const iconWorkers = 6

// FillIcons 给缺少图标的网页条目补抓 favicon，返回本批实际补上的条数。
//
// limit 建议 20~40：前端在页面打开后循环调用几轮，直到返回 filled = 0。
// 单个条目抓不到（站点拒绝、超时）不算失败——它只是留在缺图标集合里，
// 下次再试，不会阻塞其它条目。
func (s *BookmarkService) FillIcons(limit int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	tasks, err := s.App.DB.ItemsMissingIcon(limit)
	if err != nil {
		return services.Fail(err)
	}
	res := map[string]int{"checked": len(tasks), "filled": 0}
	if len(tasks) == 0 {
		return services.Ok(res)
	}

	jobs := make(chan db.IconTask)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		filled int
	)
	for i := 0; i < iconWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				icon := platform.FetchFavicon(task.URL)
				if icon == "" {
					continue
				}
				if err := s.App.DB.SetItemIcon(task.ID, icon); err != nil {
					continue
				}
				mu.Lock()
				filled++
				mu.Unlock()
			}
		}()
	}
	for _, task := range tasks {
		jobs <- task
	}
	close(jobs)
	wg.Wait()
	res["filled"] = filled
	return services.Ok(res)
}
