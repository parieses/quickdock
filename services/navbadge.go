package services

import (
	"os"
	"time"

	"quickdock/internal/logger"
)

// NavBadges 聚合角标所需的后端计数，一次调用替代 Monitor/Todo/Diag 三次独立 IPC，
// 减少轮询往返（每次 tick 从 3 次降到 1 次）。
type NavBadges struct {
	MonitorDown int `json:"monitorDown"`
	TodoDue     int `json:"todoDue"`
	CrashFiles  int `json:"crashFiles"`
}

// GetNavBadges 返回当前各数据源的角标计数。插件可更新角标是前端派生值（市场索引拉取后写入），
// 不走后端，故不在此聚合。
func (a *AppService) GetNavBadges() *ApiResult {
	down, _ := a.DB.CountMonitorsDown()
	due, _ := a.DB.CountTodosDue(todayStr())
	return Ok(NavBadges{
		MonitorDown: down,
		TodoDue:     due,
		CrashFiles:  countCrashFiles(),
	})
}

// countCrashFiles 统计崩溃日志目录下的文件数（非目录条目），与 DiagService.ListCrashFiles 口径一致。
func countCrashFiles() int {
	dir := logger.CrashDir()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

func todayStr() string {
	return time.Now().Format("2006-01-02")
}
