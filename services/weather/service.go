// Package weather 天气 / 城市检索的 Wails 绑定门面。
//
// 网络请求放在 internal/weather（WebView2 跟随系统代理，必须由 Go 发，见该包注释）。
// 本包额外持有「唯一一份」天气快照，并按固定周期在后台自行刷新。
//
// 为什么刷新要搬到后台：原先前端每 30 分钟 setInterval 拉一次，页面一切走定时器
// 就没了，多窗口各拉各的，且每次挂载总览页都可能重复请求。现在前端只做三件事：
// 报配置（SetWeatherConfig）、读快照（GetWeather）、手动强制刷新（RefreshWeather）；
// 天气数据什么时候更新、失败多久重试，全由宿主决定，与页面生命周期无关。
package weather

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	internalweather "quickdock/internal/weather"
	"quickdock/services"
)

const (
	// pollInterval 后台刷新周期。Open-Meteo 的 current 约 15 分钟更新一次，
	// 半小时足够新，也不会给免 key 的公共接口造成压力。
	pollInterval = 30 * time.Minute
	// coldRetryGap 冷启动拉取失败后的重试间隔。否则前端每分钟来读一次快照，
	// 缓存一直是空的就会变成每分钟打一次外网，比原来的 30 分钟还频繁。
	coldRetryGap = 5 * time.Minute
)

// location 是后台刷新用的坐标与开关。它由前端报进来，不落库、不重复持久化：
// 配置的真源仍是 app_state 里的 qd_weather_config，这里只是运行态副本。
type location struct {
	enabled   bool
	city      string
	latitude  float64
	longitude float64
	// source 选哪个数据源（auto / open-meteo / wttr / qweather / caiyun）。
	// 存归一后的值：结构体比较要能识别「配置真变了」，"wttr.in" 与 "wttr"
	// 都被认成同一个源，否则每次切页回来都以为变了、把快照清掉。
	source string
	// apiKey 当前源要用的 key（和风的 Key / 彩云的 Token）；免 key 源为空。
	// 只保留当前源的：切到别的源时旧 key 不跟着进内存，少一份泄漏面。
	apiKey string
	// apiHost 和风的专属 API Host；只有和风用得上。
	apiHost string
}

// WeatherService 天气快照服务。
type WeatherService struct {
	mu     sync.RWMutex
	loc    location
	data   *internalweather.Conditions
	lastAt time.Time // 最近一次发起拉取的时间，只用于冷启动失败节流

	inflight  chan struct{} // 非 nil 表示正有一次请求在跑，用于合并并发拉取
	started   chan struct{} // 首次拿到有效配置后关闭，ticker 从那一刻开始计时
	startOnce sync.Once
	quit      chan struct{}
	stopOnce  sync.Once
}

// NewWeatherService 创建服务并启动后台轮询协程。协程会先等首次配置，
// 所以在用户打开总览页之前不会对着空坐标发请求。
func NewWeatherService() *WeatherService {
	s := &WeatherService{
		started: make(chan struct{}),
		quit:    make(chan struct{}),
	}
	go s.loop()
	return s
}

// SetWeatherConfig 由前端在读回 / 保存设置后调用，后端据此决定后台刷不刷、
// 刷哪个城市、用哪个数据源。source 传空或未知值按 auto 处理（见 NormalizeSource）。
//
// apiKey / apiHost 只对需要 key 的源有意义（和风 / 彩云）；缺 key 不在这里拒绝，
// 而是在真正取数时返回「去设置里填」——否则用户先选源、再填 key 这两步
// 中间那一次保存会被打回，界面看着像坏了。
//
// 配置真变了才废弃旧快照：切页回来会重复报同一份配置，那时不该把缓存清掉，
// 也不该把 lastAt 清零（否则冷启动失败节流形同虚设）。
func (s *WeatherService) SetWeatherConfig(enabled bool, latitude, longitude float64, city, source, apiKey, apiHost string) *services.ApiResult {
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return services.FailMsg("无效的经纬度")
	}
	next := location{
		enabled:   enabled,
		city:      city,
		latitude:  latitude,
		longitude: longitude,
		source:    internalweather.NormalizeSource(source),
		apiKey:    strings.TrimSpace(apiKey),
		apiHost:   strings.TrimSpace(apiHost),
	}
	s.mu.Lock()
	changed := s.loc != next
	s.loc = next
	if changed {
		s.data = nil
		s.lastAt = time.Time{}
	}
	s.mu.Unlock()
	if enabled && city != "" {
		s.startOnce.Do(func() { close(s.started) })
	}
	return services.Ok(nil)
}

// GetWeather 只读快照，正常情况不发网络请求。冷启动（快照为空）才同步取一次，
// 否则首次打开总览页要空等后台的第一个 30 分钟。
func (s *WeatherService) GetWeather() *services.ApiResult {
	s.mu.RLock()
	d := s.data
	last := s.lastAt
	s.mu.RUnlock()
	if d != nil {
		return services.Ok(*d)
	}
	if !last.IsZero() && time.Since(last) < coldRetryGap {
		return services.FailMsg("天气暂不可用，稍后自动重试")
	}
	// 不用 services.Wrap：它失败时走 Fail()，那是 [E] 错误级。天气取不到是网络
	// 不可达（api.open-meteo.com 在国内部分网络整域名不通），不是程序出错，
	// 按错误级刷日志会让用户扒出一条红字来问「这是不是坏了」。
	//
	// 变量名不能叫 d：上面那个 d 是 *Conditions 快照，同名 `:=` 会复用它，
	// 编译器报「struct 不能赋给 *struct」。
	cond, err := s.fetchNow()
	if err != nil {
		logger.W("[WRN] weather: %v", err)
		return services.FailMsg(err.Error())
	}
	return services.Ok(cond)
}

// RefreshWeather 手动刷新（天气区的刷新按钮 / 改城市后的重试）：无视冷却，
// 强制取一次最新数据并覆盖快照。
func (s *WeatherService) RefreshWeather() *services.ApiResult {
	d, err := s.fetchNow()
	if err != nil {
		// 同 GetWeather：网络问题降一级，不占错误日志
		logger.W("[WRN] weather refresh: %v", err)
		return services.FailMsg(err.Error())
	}
	return services.Ok(d)
}

// fetchNow 单飞拉取：网络请求期间不持锁，并发调用等同一次结果。
// 失败时保留旧快照，让天气区继续显示上一次成功的数据（带旧时间戳）。
func (s *WeatherService) fetchNow() (internalweather.Conditions, error) {
	s.mu.Lock()
	if s.inflight != nil {
		wait := s.inflight
		s.mu.Unlock()
		<-wait
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.data != nil {
			return *s.data, nil
		}
		return internalweather.Conditions{}, fmt.Errorf("天气暂不可用")
	}
	wait := make(chan struct{})
	s.inflight = wait
	s.lastAt = time.Now()
	loc := s.loc
	s.mu.Unlock()

	var (
		data internalweather.Conditions
		err  error
	)
	// 拉取期间用户可能换了城市：那次结果属于旧坐标，写回就会显示上一个城市的
	// 温度。作废后按新配置重取一次，上限 2 次，避免配置连续变动时无限重试。
	for attempt := 0; attempt < 2; attempt++ {
		if !loc.enabled || loc.city == "" {
			data, err = internalweather.Conditions{}, fmt.Errorf("天气未启用")
		} else {
			data, err = internalweather.FetchConditions(internalweather.Options{
				Lat:     loc.latitude,
				Lon:     loc.longitude,
				City:    loc.city,
				Source:  loc.source,
				APIKey:  loc.apiKey,
				APIHost: loc.apiHost,
			})
		}
		s.mu.Lock()
		if err == nil && s.loc == loc {
			snapshot := data
			s.data = &snapshot
			s.inflight = nil
			close(wait)
			s.mu.Unlock()
			return data, nil
		}
		if err == nil {
			loc = s.loc
			s.mu.Unlock()
			continue
		}
		s.inflight = nil
		close(wait)
		s.mu.Unlock()
		return data, err
	}
	s.mu.Lock()
	s.inflight = nil
	close(wait)
	s.mu.Unlock()
	return internalweather.Conditions{}, fmt.Errorf("天气配置变化过快，请稍后重试")
}

// loop 等首次配置到位后按固定周期刷新。关闭天气时只是跳过本轮，不重建 ticker：
// 再打开时周期仍从原刻度走，前端会自己调 GetWeather 触发一次冷启动拉取。
func (s *WeatherService) loop() {
	select {
	case <-s.quit:
		return
	case <-s.started:
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-ticker.C:
			s.mu.RLock()
			on := s.loc.enabled && s.loc.city != ""
			s.mu.RUnlock()
			if !on {
				continue
			}
			if _, err := s.fetchNow(); err != nil {
				// 后台失败不打扰用户：保留快照，下一轮自动重试。
				logger.W("[WRN] weather poll: %v", err)
			}
		}
	}
}

// ServiceShutdown 退出时停掉后台轮询（强杀进程时协程随进程消失，无需清理）。
func (s *WeatherService) ServiceShutdown() error {
	s.stopOnce.Do(func() { close(s.quit) })
	return nil
}

// SearchCities 按名称检索城市坐标，language 只接受 zh / en，其余按 zh 处理。
// 输入中国省级行政区名时会改查其省会（检索接口不认「省」这一级，只按地名匹配，
// 直接搜「河南」会返回各省恰好叫「河南」的村镇）；详见 internal/weather/regions.go。
// 这是用户主动触发的一次性查询，不进轮询缓存。
func (s *WeatherService) SearchCities(name, language string) *services.ApiResult {
	return services.Wrap(internalweather.SearchCities(name, language))
}
