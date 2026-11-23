package dsh

// DSH 诊断日志的统一出口：一边落盘（关窗后仍可回溯，日志页 / GetLogsInfo
// 自动可见），一边推前端「环境管理 → DeepSeek Harness」实时日志面板。
//
// 以前这些日志只走 quickdock:dsh:log 事件，面板数组存在浏览器内存里（上限
// 500 行），关掉窗口或重启应用就全没了——安装 / 更新失败时用户拿不到证据。
// 落盘复用主 logger（<dataDir>/logs/quickdock-YYYYMMDD.log，按日 + 分片轮转，
// ListLogFiles / GetLogsInfo / 存储统计自动覆盖），不新增 binding、不新开文件。
import (
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"quickdock/internal/logger"
)

// emitDSHLog 落盘 + 推送一条 DSH 日志。
//
// app 为 nil（无界面调用，例如后台自愈）时仍落盘，只跳过事件推送。
// 单条截断到 500 rune：npm 输出可能带整段 stack，撑爆日志读数没有价值。
func emitDSHLog(app *application.App, level, msg string) {
	trunc := logger.Trunc(msg, 500)
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error", "err":
		logger.E("[dsh] %s", trunc)
	case "warn", "warning":
		logger.W("[dsh] %s", trunc)
	default:
		logger.I("[dsh] %s", trunc)
	}
	if app != nil {
		app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
	}
}
