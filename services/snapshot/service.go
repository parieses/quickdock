// Package snapshot 快照备份门面服务。
//
// 自动快照的常驻循环留在本包：它持有进程级单例状态（创建锁、退出通道），
// 且要在 app 生命周期内后台建快照——与 mailbox 的轮询循环同一类情形。
// 建快照/删除的 DB 逻辑全在 internal/db，本包只做配置读取、定时判定与并发收口。
package snapshot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"quickdock/internal/logger"
	"quickdock/services"
)

// snapshotConfigKey 与前端 GetValue/SetValue 共用的 app_state key。
// 走 qd_ 前缀：db.validateAppStateKey 只放行这个前缀给前端 KV 入口；后端内部
// 读用 GetSetting（不经过白名单），但沿用同一个 key，两边看到的才是同一份配置。
const snapshotConfigKey = "qd_snapshot_config"

const (
	defaultMaxSnapshots = 20 // 默认保留份数；0 表示不限制
	defaultAutoTime     = "03:00"
	defaultAutoWeekday  = 1 // time.Weekday 口径：1=周一
)

// snapshotConfig 是落库形态（一个 JSON 串存进 app_state.value）。
// 字段命名与前端 SnapshotConfig 保持一致，避免两边各写一份解析后漂移。
// autoWeekday 用 Go 的 time.Weekday 数值（0=周日..6=周六），前端直接下发，
// 这样比对时不用做「周一从 0 还是 1 开始」的换算。
type snapshotConfig struct {
	AutoMode    string `json:"autoMode"`    // "off" | "daily" | "weekly"
	AutoTime    string `json:"autoTime"`    // "HH:MM"，24 小时制
	AutoWeekday int    `json:"autoWeekday"` // 0=周日 .. 6=周六
	MaxCount    int    `json:"maxCount"`    // 0 = 不限制
}

func defaultConfig() snapshotConfig {
	return snapshotConfig{
		AutoMode:    "off",
		AutoTime:    defaultAutoTime,
		AutoWeekday: defaultAutoWeekday,
		MaxCount:    defaultMaxSnapshots,
	}
}

// SnapshotService 快照备份绑定服务。
type SnapshotService struct {
	App *services.AppService

	// createMu 把「建快照 + 按上限清理」整体串行化：手动点击和定时 ticker
	// 撞在同一刻时只会有一个在写，避免两次清理互相删掉对方刚建的记录。
	createMu sync.Mutex

	quit     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewSnapshotService 创建 SnapshotService。
func NewSnapshotService(app *services.AppService) *SnapshotService {
	return &SnapshotService{App: app, quit: make(chan struct{})}
}

// ServiceStartup 由 Wails 框架按注册顺序回调（排在 AppService 之后，DB 已就绪）。
// 在这里起常驻 goroutine，而不是改 main.go 注入 ticker：生命周期钩子本就是
// 框架给 service 的扩展点，main.go 一行都不用动就能跟随 app 启停。
func (s *SnapshotService) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.autoLoop()
	}()
	return nil
}

// ServiceShutdown 先关 quit 再等 goroutine 收尾：否则退出时 DB 可能已经开始
// 关闭，autoLoop 恰好在那一刻写快照会撞上已关闭的连接（报错日志 + 半截写入）。
func (s *SnapshotService) ServiceShutdown() error {
	s.stopOnce.Do(func() { close(s.quit) })
	s.wg.Wait()
	return nil
}

func (s *SnapshotService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// loadConfig 每轮重新读 app_state：用户在设置页改了配置不用重启就生效。
// 读失败/为空/坏 JSON 一律回退默认值，不让一份坏配置让后台循环空转或刷屏报错。
func (s *SnapshotService) loadConfig() snapshotConfig {
	c := defaultConfig()
	if s.App.DB == nil {
		return c
	}
	raw, err := s.App.DB.GetSetting(snapshotConfigKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return c
	}
	// 先铺默认值再反序列化：缺字段保持默认，显式写 0 才会覆盖成「不限制」。
	// 若直接 Unmarshal 到零值结构，老配置没有 maxCount 时会被当成 0=不限制。
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		logger.W("QuickDock: 快照配置解析失败，回退默认: %v", err)
		return defaultConfig()
	}
	if c.AutoMode != "daily" && c.AutoMode != "weekly" {
		c.AutoMode = "off"
	}
	if _, _, ok := parseHHMM(c.AutoTime); !ok {
		c.AutoTime = defaultAutoTime
	}
	if c.AutoWeekday < 0 || c.AutoWeekday > 6 {
		c.AutoWeekday = defaultAutoWeekday
	}
	if c.MaxCount < 0 {
		c.MaxCount = defaultMaxSnapshots
	}
	return c
}

// parseHHMM 解析 "HH:MM"（允许带秒），返回 时/分。
func parseHHMM(s string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 {
		return 0, 0, false
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// autoLoop 分钟级轮询：每天/每周只需要在固定 HH:MM 触发一次，用 1 分钟
// ticker 对齐即可，不必引入完整 cron。schedule_runner 的 cron/monthly 属于
// 用户自定义任务域（且是共享文件、刚实现过），本需求不该去动它。
// lastRun 记住「日期+时刻」，防止同一分钟内重复触发或系统时钟回拨造成补跑。
func (s *SnapshotService) autoLoop() {
	// 启动后缓一轮：ServiceStartup 阶段 DB 刚打开，别和首屏初始化抢 IO。
	startTimer := time.NewTimer(10 * time.Second)
	defer startTimer.Stop()
	select {
	case <-s.quit:
		return
	case <-startTimer.C:
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastRun := ""
	for {
		select {
		case <-s.quit:
			return
		case <-ticker.C:
		}
		cfg := s.loadConfig()
		if cfg.AutoMode == "off" {
			continue
		}
		h, m, ok := parseHHMM(cfg.AutoTime)
		if !ok {
			continue
		}
		now := time.Now()
		if now.Hour() != h || now.Minute() != m {
			continue
		}
		if cfg.AutoMode == "weekly" && int(now.Weekday()) != cfg.AutoWeekday {
			continue
		}
		key := now.Format("2006-01-02 15:04")
		if key == lastRun {
			continue
		}
		lastRun = key
		s.runAuto(cfg)
	}
}

// runAuto 用 TryLock 拿创建锁：拿不到说明手动创建（或上一轮自动）还在跑，
// 直接跳过本轮而不是排队——定时快照宁可少一份，也不能和手动操作叠在一起。
// 这里不复用 CreateSnapshot RPC：那条路径要包装 ApiResult 给前端，后台路径
// 只需要 DB 副作用，硬套 RPC 会把错误处理与日志口径搅在一起。
func (s *SnapshotService) runAuto(cfg snapshotConfig) {
	if s.App.DB == nil {
		return
	}
	if !s.createMu.TryLock() {
		logger.I("QuickDock: 已有快照创建在进行，跳过本次自动快照")
		return
	}
	defer s.createMu.Unlock()
	select {
	case <-s.quit:
		return
	default:
	}
	// label 用哨兵串 "auto"：前端据此显示本地化的「自动快照」，
	// 后台 goroutine 拿不到 t()，不在这里写用户可见的中文。
	if _, err := s.App.DB.CreateFullSnapshot("auto", time.Now().Format("2006-01-02 15:04:05")); err != nil {
		logger.W("QuickDock: 自动快照创建失败: %v", err)
		return
	}
	s.enforceLimit(cfg.MaxCount)
	logger.I("QuickDock: 自动快照已创建（上限=%d）", cfg.MaxCount)
}

// enforceLimit 创建后按上限清理最旧的快照。
// max<=0 表示不限制——0 是用户显式选的「不限制」，不能当成「没配过」回退默认。
// ListSnapshots 已按 created_at DESC 排序，保留前 max 条、删掉其余即可。
func (s *SnapshotService) enforceLimit(max int) {
	if max <= 0 || s.App.DB == nil {
		return
	}
	list, err := s.App.DB.ListSnapshots()
	if err != nil {
		logger.W("QuickDock: 快照上限清理读取列表失败: %v", err)
		return
	}
	if len(list) <= max {
		return
	}
	for _, old := range list[max:] {
		if err := s.App.DB.DeleteSnapshot(old.ID); err != nil {
			logger.W("QuickDock: 清理旧快照 %s 失败: %v", old.ID, err)
		}
	}
}

// CreateSnapshot 创建全量快照（手动入口）。创建成功后按配置的上限清理最旧的，
// 让手动与自动两条路径受同一份 maxCount 约束。
func (s *SnapshotService) CreateSnapshot(label, note string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	data, err := s.App.DB.CreateFullSnapshot(label, note)
	if err != nil {
		return services.Fail(err)
	}
	s.enforceLimit(s.loadConfig().MaxCount)
	return services.Ok(data)
}

// ListSnapshots 列出全部快照。
func (s *SnapshotService) ListSnapshots() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.ListSnapshots()
	return services.Wrap(data, err)
}

// GetSnapshot 获取单个快照详情（含 payload），供设置页预览各模块内容。
func (s *SnapshotService) GetSnapshot(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.GetSnapshot(id)
	return services.Wrap(data, err)
}

// DeleteSnapshot 删除快照。
func (s *SnapshotService) DeleteSnapshot(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteSnapshot(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// RestoreSnapshot 恢复快照。
func (s *SnapshotService) RestoreSnapshot(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.RestoreSnapshot(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
