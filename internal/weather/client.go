// Package weather 是天气数据的只读客户端：当前天气 + 城市名检索。
//
// 两个数据源都免 key：Open-Meteo（WMO 码，字段最全）与 wttr.in（WWO 码，
// 需转 WMO，见 wwo.go）。用哪个由前端配置决定（source：auto / open-meteo / wttr），
// 默认 auto = 主源失败换备用源——不同网络下可达的源不一样，写死一个就会
// 「有的人永远看不到天气」。
//
// 为什么请求由 Go 发、不走 WebView2 fetch：WebView2 默认跟随 WinINET 系统代理，
// 用户机器上常见「ProxyEnable=1 但本地代理端口没起」，这时前端所有外部请求都会
// ERR_TIMED_OUT，连城市都搜不出来。Go 侧可以显式直连，并在直连被网络干扰时
// 回退到环境变量 / 系统代理（internal/env.ProxyTransport 已实现这套探测）。
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"quickdock/internal/env"
)

const (
	forecastURL  = "https://api.open-meteo.com/v1/forecast"
	geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
	// wttrBase 备用源（按坐标取 j1 JSON，免 key）。
	//
	// 为什么需要备用源：api.open-meteo.com 在国内网络下是**域名级**不可达——
	// 实测同机器的 geocoding-api.open-meteo.com 直连 1s 正常，而 forecast 那个
	// 域名的 TLS 握手完全不返回（12s 超时），换代理也一样。这不是「偶发抖动」，
	// 重试多少次都没用；有一个能直连的源顶上来，天气区才不会一直空着。
	wttrBase     = "https://wttr.in"
	maxBodyBytes = 256 << 10
	// 3 次尝试（直连、直连重试、代理兜底）共用 totalTimeout。
	//
	// 收紧过一轮：原来 3.5s×3≈10.5s，而主源在这台机器上是直接黑洞——
	// 等满 10.5s 才轮到备用源，用户开机时天气区要空白十几秒。现在主源最多花 6s，
	// 之后立刻切 wttr（实测 1.7s 出结果）。
	perCallTimeout = 2500 * time.Millisecond
	totalTimeout   = 6 * time.Second
)

// City 是城市检索的一条结果。坐标交给天气接口，字符串只用于展示。
type City struct {
	Name      string  `json:"name"`
	Region    string  `json:"region"`
	Country   string  `json:"country"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Conditions 是信息条要显示的全部字段；UpdatedAt 用本机毫秒时间戳，
// 前端据此算「更新于 HH:mm」和 30 分钟缓存。
type Conditions struct {
	Temp      float64 `json:"temp"`
	Apparent  float64 `json:"apparent"`
	Humidity  float64 `json:"humidity"`
	Wind      float64 `json:"wind"`
	Code      int     `json:"code"`
	IsDay     bool    `json:"isDay"`
	Max       float64 `json:"max"`
	Min       float64 `json:"min"`
	UpdatedAt int64   `json:"updatedAt"`
	City      string  `json:"city"`
	// Source 这次数据来自哪个源，取值是 Source* 常量（open-meteo / wttr /
	// qweather / caiyun）。前端把它映射成可读名字，所以这里不放中文——
	// 英文界面下「来自 和风天气」会很跳。
	Source string `json:"source,omitempty"`
}

// directHTTP 显式 Proxy:nil —— 自定义 Transport 的 Proxy 零值就是不走代理，
// 不能复用 http.DefaultTransport（它会读 HTTP_PROXY 环境变量）。
var directHTTP = &http.Client{
	Timeout: perCallTimeout,
	Transport: &http.Transport{
		Proxy: nil,
		// 必须小于 client Timeout：比它大等于没设，超时一律按 client 那个报
		// 「awaiting headers」，日志里就看不出卡在握手还是卡在等响应。
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
	},
}

// proxyHTTP 走环境变量 / WinINET 系统代理，只在直连彻底不通时兜底。
var proxyHTTP = &http.Client{Timeout: perCallTimeout, Transport: env.ProxyTransport()}

// HTTPStatusError 上游返回了非 200。带上响应体（截断到 maxBodyBytes 以内）：
// 彩云在 token 无效时返回 HTTP 400 + {"status":"failed","error":"token is invalid"}，
// 只把「HTTP 400」抛给用户等于什么都没说——他需要知道是 key 错了还是网络断了。
type HTTPStatusError struct {
	Status int
	Body   string
}

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

// getJSON 直连优先，机器上确实配了代理时才补一次代理兜底。
//
// 顺序不是随便排的：实测国内直连 geocoding 稳定 ~1s，而 forecast 偶发 TLS 抖动，
// 所以先直连；只有直连彻底不通（用户开了全局代理，或运营商阻断）才值得付代理
// 那一跳的代价。
//
// 这里原来固定试 direct, direct, proxy 三次。两次直连是多余的（抖动由备用源
// 1.2s 就兜住了，比重跑一次直连更快），而没有代理时的第三次「代理兜底」其实
// 就是第三次直连——白白多等一个超时。现在由 env.HasProxy() 决定要不要那一次。
func getJSON(rawURL string, out any) error {
	return getJSONWithHeaders(rawURL, out, nil)
}

// getJSONWithHeaders 同 getJSON，额外带请求头；和风天气的 key 走 X-QW-Api-Key。
//
// key 放头而不放 query：和风两种都支持，但头不会进 URL，也就不会被错误日志、
// 代理记录或抓包工具带出去。
func getJSONWithHeaders(rawURL string, out any, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), totalTimeout)
	defer cancel()

	clients := []*http.Client{directHTTP}
	if env.HasProxy() {
		clients = append(clients, proxyHTTP)
	}

	var lastErr error
	for _, c := range clients {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "QuickDock/weather")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = &HTTPStatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			lastErr = fmt.Errorf("响应不是有效 JSON: %w", err)
			continue
		}
		return nil
	}
	if lastErr == nil {
		// clients 非空时循环理论上必会赋值，但 ctx 恰好在第一次进循环前就超时、
		// 或以后把 clients 改成动态列表时都可能漏掉；%w 接到 nil 会打印出
		// %!w(<nil>)，错误链也就断了（下游的 errors.As 再也认不出 HTTPStatusError）。
		lastErr = fmt.Errorf("没有可用的请求通道")
	}
	// %w 保留错误链：调用方要用 errors.As 认 HTTPStatusError，把它翻成
	// 「token 无效」这种可操作的提示（见 caiyun.go / qweather.go）。
	return fmt.Errorf("天气服务不可达（直连与代理均未成功）: %w", lastErr)
}

type forecastResponse struct {
	Current *struct {
		Temperature *float64 `json:"temperature_2m"`
		Apparent    *float64 `json:"apparent_temperature"`
		Humidity    *float64 `json:"relative_humidity_2m"`
		Wind        *float64 `json:"wind_speed_10m"`
		Code        *int     `json:"weather_code"`
		IsDay       *int     `json:"is_day"`
	} `json:"current"`
	Daily *struct {
		Max []float64 `json:"temperature_2m_max"`
		Min []float64 `json:"temperature_2m_min"`
	} `json:"daily"`
}

func derefFloat(p *float64, fallback float64) float64 {
	if p != nil {
		return *p
	}
	return fallback
}

func derefInt(p *int, fallback int) int {
	if p != nil {
		return *p
	}
	return fallback
}

// 可选数据源 id。
//
// 前三个免 key——天气卡是开机第一眼的信息，默认不该要求用户先去注册；
// 后两个需要用户自己去开放平台申请 key。它们存在的意义是：公共接口在国内
// 网络下时通时不通，愿意花两分钟注册的人可以钉住一个稳定的源。
const (
	// SourceAuto 自动：主源优先，失败换备用源（默认）。
	SourceAuto = "auto"
	// SourceOpenMeteo 只用 Open-Meteo（免 key）。
	SourceOpenMeteo = "open-meteo"
	// SourceWttr 只用 wttr.in（免 key）。
	SourceWttr = "wttr"
	// SourceQWeather 和风天气（需 key，可选专属 API Host）。
	SourceQWeather = "qweather"
	// SourceCaiyun 彩云天气（需 token）。
	SourceCaiyun = "caiyun"
)

// Options 一次天气请求的全部输入。
//
// 用结构体而不是继续加位置参数：坐标 / 城市 / 源已是三项，再塞进 key 与 host
// 就是六个参数，调用点会变成一串谁也读不懂的字符串字面量。
type Options struct {
	Lat, Lon float64
	City     string
	// Source 见上面 Source* 常量；空或未知值按 auto。
	Source string
	// APIKey 和风天气的 Key / 彩云天气的 Token；免 key 源忽略。
	APIKey string
	// APIHost 和风天气的专属 API Host（形如 abc123.xy.qweatherapi.com）。
	// 留空时退回旧公共地址——和风自 2026 年起逐步停用公共地址，新账号必须填。
	APIHost string
}

// NormalizeSource 把外部传来的源 id 收敛到已知值。
//
// 配置是从 localStorage 读回来的：老版本没有这个字段、用户手改过 JSON、
// 或将来某个源被移除，都可能给出不认识的串。一律回落到 auto 而不是报错——
// 为一个字段拼错就把整张天气卡打成失败态，代价远大于收益。
func NormalizeSource(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SourceOpenMeteo:
		return SourceOpenMeteo
	case SourceWttr:
		return SourceWttr
	case SourceQWeather:
		return SourceQWeather
	case SourceCaiyun:
		return SourceCaiyun
	default:
		return SourceAuto
	}
}

// NeedsKey 该源是否需要用户自己填 key。
//
// 前端用 WEATHER_SOURCE_META 判断同一件事；这里是给调用方在服务端也有一份
// 权威判断（例如日志或未来的命令行入口）。
func NeedsKey(source string) bool {
	switch NormalizeSource(source) {
	case SourceQWeather, SourceCaiyun:
		return true
	}
	return false
}

// FetchConditions 取指定坐标的当前天气与今日最高/最低温。
//
// Source 为空或 auto 时：先试主源（Open-Meteo），失败再试备用源（wttr.in）。
// 主源在国内网络下可能整域名不可达，且失败方式是「黑洞」——不返回 RST，只是
// 永远等不到响应，所以只能靠超时切源。各源的天气码都要先转成 WMO（见 wwo.go），
// 否则前端图标全变「未知」。
//
// Source 指定具体源时只打那一个：用户既然在设置里选了，就不该悄悄换成别的源
// ——「我选的 A，界面上却是 B 的数据」比「A 不通，明确报错」更难解释。
func FetchConditions(o Options) (Conditions, error) {
	var zero Conditions
	if o.Lat < -90 || o.Lat > 90 || o.Lon < -180 || o.Lon > 180 {
		return zero, fmt.Errorf("无效的经纬度: %.4f, %.4f", o.Lat, o.Lon)
	}

	switch NormalizeSource(o.Source) {
	case SourceOpenMeteo:
		// 显式选择不看熔断窗口（熔断是给 auto 省时间的），但成败照样记进去：
		// 用户在这一源上试通/试不通，正好是 auto 下次该不该跳过它的最新依据。
		return recordPrimary(fetchOpenMeteo(o.Lat, o.Lon, o.City))
	case SourceWttr:
		return fetchWttr(o.Lat, o.Lon, o.City)
	case SourceQWeather:
		key := strings.TrimSpace(o.APIKey)
		if key == "" {
			return zero, fmt.Errorf("和风天气需要 API Key，请在天气设置的「数据源」下面填写")
		}
		return fetchQWeather(o.Lat, o.Lon, o.City, key, o.APIHost)
	case SourceCaiyun:
		token := strings.TrimSpace(o.APIKey)
		if token == "" {
			return zero, fmt.Errorf("彩云天气需要 Token，请在天气设置的「数据源」下面填写")
		}
		return fetchCaiyun(o.Lat, o.Lon, o.City, token)
	}

	primary, pErr := fetchPrimary(o.Lat, o.Lon, o.City)
	if pErr == nil {
		return primary, nil
	}
	// 主源失败直接换源，不再重试主源：「域名级不可达」重试只是让用户多等一个
	// 超时周期；真正会「再试一次就好」的抖动已经被 getJSON 内部的直连重试覆盖。
	backup, bErr := fetchWttr(o.Lat, o.Lon, o.City)
	if bErr == nil {
		return backup, nil
	}
	return zero, fmt.Errorf("天气服务不可达（主源: %v；备用源: %v）", pErr, bErr)
}

// primaryCooldown 主源连续失败后的熔断时长。
//
// 为什么要熔断：主源不通时每一次拉取都要先在它身上耗满 6s 才轮到备用源，而
// 后台每 30 分钟拉一轮、冷启动时前端还同步等着。熔断之后这段时间里直接走备用源
// （1~2s 出结果）；到期再试一次主源，恢复了就自动切回去。
//
// 15 分钟：短于轮询间隔的一半，网络恢复后最多多等一轮就回主源；又长到不会
// 在「每轮都试一次、每轮多 6s」里白耗。
const primaryCooldown = 15 * time.Minute

var (
	primaryMu     sync.Mutex
	primaryFailed time.Time // 主源最近一次失败的时刻；零值表示「没失败过」
)

// fetchPrimary 在主源熔断窗口外才真的去请求，并把成败记进熔断状态。
func fetchPrimary(lat, lon float64, city string) (Conditions, error) {
	primaryMu.Lock()
	cooling := !primaryFailed.IsZero() && time.Since(primaryFailed) < primaryCooldown
	primaryMu.Unlock()
	if cooling {
		return Conditions{}, fmt.Errorf("主源熔断中（上次失败后未满 %s）", primaryCooldown)
	}
	return recordPrimary(fetchOpenMeteo(lat, lon, city))
}

// recordPrimary 记录主源这次请求的成败。
// 成功清掉熔断标记——下次 auto 时就能立刻回到数据更全的主源。
func recordPrimary(c Conditions, err error) (Conditions, error) {
	primaryMu.Lock()
	if err != nil {
		primaryFailed = time.Now()
	} else {
		primaryFailed = time.Time{}
	}
	primaryMu.Unlock()
	return c, err
}

// fetchOpenMeteo 主源：返回 WMO 天气码，字段与前端一一对应。
func fetchOpenMeteo(lat, lon float64, city string) (Conditions, error) {
	var zero Conditions
	u, err := url.Parse(forecastURL)
	if err != nil {
		return zero, err
	}
	q := u.Query()
	q.Set("latitude", strconv.FormatFloat(lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', 4, 64))
	q.Set("current", "temperature_2m,apparent_temperature,relative_humidity_2m,wind_speed_10m,weather_code,is_day")
	q.Set("daily", "temperature_2m_max,temperature_2m_min")
	q.Set("timezone", "auto")
	q.Set("forecast_days", "1")
	u.RawQuery = q.Encode()

	var r forecastResponse
	if err := getJSON(u.String(), &r); err != nil {
		return zero, err
	}
	if r.Current == nil || r.Current.Temperature == nil {
		return zero, fmt.Errorf("天气响应缺少 current.temperature_2m")
	}
	c := r.Current
	max, min := *c.Temperature, *c.Temperature
	if r.Daily != nil {
		if len(r.Daily.Max) > 0 {
			max = r.Daily.Max[0]
		}
		if len(r.Daily.Min) > 0 {
			min = r.Daily.Min[0]
		}
	}
	return Conditions{
		Temp:      *c.Temperature,
		Apparent:  derefFloat(c.Apparent, *c.Temperature),
		Humidity:  derefFloat(c.Humidity, 0),
		Wind:      derefFloat(c.Wind, 0),
		Code:      derefInt(c.Code, -1),
		IsDay:     c.IsDay == nil || *c.IsDay != 0,
		Max:       max,
		Min:       min,
		UpdatedAt: time.Now().UnixMilli(),
		City:      city,
		Source:    SourceOpenMeteo,
	}, nil
}

// wttrResponse 备用源 j1 响应。所有数值都是字符串（该接口的老习惯），
// 一律走 atof 容错，缺字段时回落到合理默认而不是整次失败。
type wttrResponse struct {
	Current []struct {
		TempC    string `json:"temp_C"`
		FeelsC   string `json:"FeelsLikeC"`
		Humidity string `json:"humidity"`
		WindKmph string `json:"windspeedKmph"`
		Code     string `json:"weatherCode"`
		// observation_time 是 UTC 观测时刻（不是城市本地时间），这里不解析：
		// 它是上一整点的观测、可能滞后一两小时，拿来判断「现在」并不准。
	} `json:"current_condition"`
	Weather []struct {
		MaxC      string `json:"maxtempC"`
		MinC      string `json:"mintempC"`
		Astronomy []struct {
			Sunrise string `json:"sunrise"`
			Sunset  string `json:"sunset"`
		} `json:"astronomy"`
	} `json:"weather"`
}

// fetchWttr 备用源：按坐标取，返回 WWO 码，转成 WMO 后与主源同构。
func fetchWttr(lat, lon float64, city string) (Conditions, error) {
	var zero Conditions
	u := fmt.Sprintf("%s/%.4f,%.4f?format=j1", wttrBase, lat, lon)
	var r wttrResponse
	if err := getJSON(u, &r); err != nil {
		return zero, err
	}
	if len(r.Current) == 0 {
		return zero, fmt.Errorf("备用源响应缺少 current_condition")
	}
	c := r.Current[0]
	temp, ok := atof(c.TempC)
	if !ok {
		return zero, fmt.Errorf("备用源温度字段不是数字: %q", c.TempC)
	}
	out := Conditions{
		Temp:      temp,
		Apparent:  atofOr(c.FeelsC, temp),
		Humidity:  atofOr(c.Humidity, 0),
		Wind:      atofOr(c.WindKmph, 0),
		Code:      wwoToWMO(int(atofOr(c.Code, -1))),
		IsDay:     true,
		Max:       temp,
		Min:       temp,
		UpdatedAt: time.Now().UnixMilli(),
		City:      city,
		Source:    SourceWttr,
	}
	if len(r.Weather) > 0 {
		w := r.Weather[0]
		out.Max = atofOr(w.MaxC, temp)
		out.Min = atofOr(w.MinC, temp)
		if len(w.Astronomy) > 0 {
			out.IsDay = isDaytime(w.Astronomy[0].Sunrise, w.Astronomy[0].Sunset, lon, time.Now())
		}
	}
	return out, nil
}

// atof 解析字符串数字；备用源把数值全塞在字符串里，空串 / "-" 也常见。
func atof(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func atofOr(s string, fallback float64) float64 {
	if v, ok := atof(s); ok {
		return v
	}
	return fallback
}

// isDaytime 判断该城市此刻是白天还是夜晚。
//
// sunrise/sunset 是**城市当地钟点**，原来拿 time.Now() 的本机钟点直接比，
// 看远端城市时两边根本不是同一把尺（北京晚上看纽约会被判成白天，反之亦然）。
//
// j1 响应没有城市本地时间：current_condition.observation_time 是 UTC 的上一整点
// 观测时刻，滞后一两小时，拿来当「现在」照样不准。但日出日落本身就是本地钟点，
// 它们的中点是当地太阳正午，与 12:00 的偏差恰好是「时区中央经线与城市经度之差」：
//
//	mid(分钟) = 720 + 60*时区小时 - 4*经度(度)
//	⇒ 时区偏移(分钟) = mid - 720 + 4*经度
//
// 本地钟点 = UTC 钟点 + 这个偏移。夏令时、半小时时区都已经体现在 mid 里，
// 不需要维护时区表，也不用给配置加字段。均时差 / 大气折射带来约 ±20 分钟
// 误差，对昼夜图标可以接受（只在日出日落前后那十几分钟可能差一档）。
//
// 解析失败一律当白天：夜里的图标是月亮，误判成夜里比误判成白天更刺眼。
// 跨零点的日落值也归到白天。
func isDaytime(sunrise, sunset string, lon float64, now time.Time) bool {
	rise, ok1 := parseClock(sunrise)
	set, ok2 := parseClock(sunset)
	if !ok1 || !ok2 || set < rise {
		return true
	}
	tzMinutes := float64((rise+set)/2-720) + 4*lon
	// 真实时区落在 UTC-12..UTC+14；上游偶发的脏钟点不该把本地时间推到另一天。
	if tzMinutes < -720 {
		tzMinutes = -720
	} else if tzMinutes > 840 {
		tzMinutes = 840
	}
	utcMinutes := now.UTC().Hour()*60 + now.UTC().Minute()
	cur := ((utcMinutes+int(tzMinutes))%1440 + 1440) % 1440
	return cur >= rise && cur < set
}

// parseClock 解析备用源的 "06:58 AM" 形式时刻，返回当日分钟数。
func parseClock(s string) (int, bool) {
	t, err := time.Parse("03:04 PM", strings.ToUpper(strings.TrimSpace(s)))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

type geocodingResponse struct {
	Results []struct {
		Name        string   `json:"name"`
		Admin1      string   `json:"admin1"`
		Admin2      string   `json:"admin2"`
		Country     string   `json:"country"`
		CountryCode string   `json:"country_code"`
		FeatureCode string   `json:"feature_code"`
		Population  int      `json:"population"`
		Latitude    *float64 `json:"latitude"`
		Longitude   *float64 `json:"longitude"`
	} `json:"results"`
}

// 一次最多给前端 6 条；省名解析后通常只剩 1 条省会，这里主要兜住
// 「西安」「Springfield」这类重名地名。
const maxCityHits = 6

type cityCandidate struct {
	City
	population  int
	featureCode string
	countryCode string
}

// isPopulatedPlace 挡掉机场 / 车站 / 山峰这类 POI：搜 Hohhot 时接口会混进
// 「呼和浩特白塔国际机场」(AIRP)，它们不是能代表一座城市的天气坐标点。
func isPopulatedPlace(code string) bool {
	return strings.HasPrefix(code, "PPL") || strings.HasPrefix(code, "PCL") || strings.HasPrefix(code, "ADM")
}

// isAdminCenter 是省会 / 首都 / 特区那一档地名。省名查询只认这一档：
// 搜 Haikou 还会带出云南的「海口」(PPLA4, 11 万人)，它有人口但不是行政中心，
// 留着就会和海南海口并排出现。34 个代表城市的实测首条全是 PPLA / PPLC / PCLS。
func isAdminCenter(code string) bool {
	switch code {
	case "PPLA", "PPLC", "PCLS", "PCLI":
		return true
	}
	return false
}

// candidateScore 把「有人口、行政级别高、名字对得上」的排前面。
// 同一批结果里村镇与大城混着时靠它把大城顶上来；不直接删无人口的结果，
// 因为不少真实小县城在 GeoNames 里就是没有 population 字段。
func candidateScore(c cityCandidate, query string) int {
	s := 0
	switch {
	case c.population >= 1_000_000:
		s += 100
	case c.population >= 100_000:
		s += 70
	case c.population > 0:
		s += 40
	}
	switch c.featureCode {
	case "PPLC":
		s += 30
	case "PPLA":
		s += 25
	case "PPLA2", "PPLA3", "PPLA4":
		s += 10
	}
	if strings.EqualFold(c.Name, query) {
		s += 15
	}
	return s
}

// displayRegion 拼出「省 · 市」并把港澳台的归属写全。
// 接口对 TW 给的是 country="台湾"，HK / MO 的 country、admin1 干脆为空；
// 直接铺到界面上既不好辨认也不对，统一按中国省级行政区展示。
func displayRegion(admin1, admin2, country, code, lang string) (region, ctry string) {
	switch strings.ToUpper(code) {
	case "TW":
		if lang == "en" {
			return "Taiwan, China", "China"
		}
		return "中国台湾", "中国"
	case "HK":
		if lang == "en" {
			return "Hong Kong, China", "China"
		}
		return "中国香港", "中国"
	case "MO":
		if lang == "en" {
			return "Macao, China", "China"
		}
		return "中国澳门", "中国"
	}
	parts := make([]string, 0, 2)
	if admin1 != "" {
		parts = append(parts, admin1)
	}
	if admin2 != "" && admin2 != admin1 {
		parts = append(parts, admin2)
	}
	return strings.Join(parts, " · "), country
}

// fetchCityCandidates 发一次检索并转成可排序的候选。
func fetchCityCandidates(query, lang string) ([]cityCandidate, error) {
	u, err := url.Parse(geocodingURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("name", query)
	// 取多一点再按质量筛：接口默认顺序把同名的无人口村镇排在前面，
	// 只要前 6 条的话，真正的城市可能根本进不了候选。
	q.Set("count", "10")
	q.Set("language", lang)
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	var r geocodingResponse
	if err := getJSON(u.String(), &r); err != nil {
		return nil, err
	}
	out := make([]cityCandidate, 0, len(r.Results))
	for _, x := range r.Results {
		if x.Latitude == nil || x.Longitude == nil {
			continue
		}
		if !isPopulatedPlace(x.FeatureCode) {
			continue
		}
		region, ctry := displayRegion(x.Admin1, x.Admin2, x.Country, x.CountryCode, lang)
		out = append(out, cityCandidate{
			City: City{
				Name: x.Name, Region: region, Country: ctry,
				Latitude: *x.Latitude, Longitude: *x.Longitude,
			},
			population:  x.Population,
			featureCode: x.FeatureCode,
			countryCode: x.CountryCode,
		})
	}
	return out, nil
}

// SearchCities 按名称检索城市，最多 6 条；无坐标 / 非居民点的结果直接丢掉。
//
// 输入省名时改查省会、输入省会中文名时改查罗马化词（见 regions.go），这两类
// 查询只留有人口的城市：罗马化词偶尔也会带出一条同名村镇，不该再混进备选。
// 解析不命中时不回退原词，因为原词正是那堆同名村镇。
func SearchCities(name, lang string) ([]City, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return nil, fmt.Errorf("城市名无效")
	}
	if lang != "en" {
		lang = "zh"
	}

	query, strict := resolveCityQuery(name)
	cand, err := fetchCityCandidates(query, lang)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(cand, func(i, j int) bool {
		return candidateScore(cand[i], query) > candidateScore(cand[j], query)
	})

	if strict {
		// 省名 / 省会名的检索词是确定的，只留「有人口的行政中心」。
		// 万一上游把某个省会的 feature_code 改了，白名单会误伤——那时退回
		// 「只要有人口」，宁可多一条也别把唯一正确答案砍成 0 条。
		centers, withPop := make([]cityCandidate, 0, len(cand)), make([]cityCandidate, 0, len(cand))
		for _, c := range cand {
			if c.population == 0 {
				continue
			}
			withPop = append(withPop, c)
			if isAdminCenter(c.featureCode) {
				centers = append(centers, c)
			}
		}
		if len(centers) > 0 {
			cand = centers
		} else {
			cand = withPop
		}
	}

	hits := make([]City, 0, maxCityHits)
	seen := make(map[string]bool, len(cand))
	limit := maxCityHits
	if strict {
		limit = 3
	}
	for _, c := range cand {
		// 同坐标的多语言别名（如「郑州」与「Zhengzhou」）只留分高的那条。
		key := fmt.Sprintf("%s|%.2f|%.2f", c.countryCode, c.City.Latitude, c.City.Longitude)
		if seen[key] {
			continue
		}
		seen[key] = true
		hits = append(hits, c.City)
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}
