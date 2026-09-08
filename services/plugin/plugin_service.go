package plugin

import (
	"sync"
	"time"

	"quickdock/services"
)

// PluginService 承载原 AppService 的插件领域方法与私有状态。
// 通过 App 回指宿主 AppService，以访问跨领域共享依赖（DB / 应用实例 / 通知 / 窗口等）。
// 这样 services 包只持有插件相关「字段」（供 lifecycle/theme 使用），方法逻辑下沉到本包，
// 且 services 不反向 import 本包，避免循环依赖。
type PluginService struct {
	App *services.AppService

	frontendCache   map[string]*frontendCacheEntry
	frontendCacheMu sync.RWMutex

	// 跨窗口传递：命令面板→插件窗口的初始计算文本 + 命中的子命令
	pendingInitPlugin  string
	pendingInitText    string
	pendingInitCommand string
	pendingInitTextMu  sync.Mutex
}

// NewPluginService 创建插件服务实例，App 为宿主服务引用（用于回指共享依赖）。
func NewPluginService(app *services.AppService) *PluginService {
	return &PluginService{
		App:           app,
		frontendCache: make(map[string]*frontendCacheEntry),
	}
}

// frontendCacheEntry 插件前端页面 HTML 缓存条目
type frontendCacheEntry struct {
	html        string
	htmlMtime   time.Time
	commonMtime time.Time // common.css 修改时间，变化时全部失效
}
