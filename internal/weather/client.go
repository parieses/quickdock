// Package weather 是 Open-Meteo 的只读客户端：当前天气 + 城市名检索。
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
	"time"
	"unicode/utf8"

	"quickdock/internal/env"
)

const (
	forecastURL  = "https://api.open-meteo.com/v1/forecast"
	geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
	maxBodyBytes = 256 << 10
	// 3.5s × 3 次（直连、直连重试、代理兜底）≈ 10.5s，留 0.5s 给整体 ctx 收尾。
	// 单次给太长会把「代理兜底」那一次的时间预算吃光：直连两次超时后 ctx 就到期了。
	perCallTimeout = 3500 * time.Millisecond
	totalTimeout   = 11 * time.Second
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
}

// directHTTP 显式 Proxy:nil —— 自定义 Transport 的 Proxy 零值就是不走代理，
// 不能复用 http.DefaultTransport（它会读 HTTP_PROXY 环境变量）。
var directHTTP = &http.Client{
	Timeout: perCallTimeout,
	Transport: &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
	},
}

// proxyHTTP 走环境变量 / WinINET 系统代理，只在直连彻底不通时兜底。
var proxyHTTP = &http.Client{Timeout: perCallTimeout, Transport: env.ProxyTransport()}

// getJSON 直连优先、重试一次、系统代理兜底。
//
// 顺序不是随便排的：实测国内直连 geocoding 稳定 ~1s，而 forecast 偶发 TLS 抖动
// （三次里一次超时），所以先补一次直连重试比立刻切代理更快；只有直连彻底不通
// （用户开了全局代理，或运营商阻断）才值得付代理那一跳的代价。
func getJSON(rawURL string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), totalTimeout)
	defer cancel()

	var lastErr error
	for _, c := range []*http.Client{directHTTP, directHTTP, proxyHTTP} {
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
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			lastErr = fmt.Errorf("响应不是有效 JSON: %w", err)
			continue
		}
		return nil
	}
	return fmt.Errorf("天气服务不可达（直连重试与系统代理均失败）: %v", lastErr)
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

// FetchConditions 取指定坐标的当前天气与今日最高/最低温。
func FetchConditions(lat, lon float64, city string) (Conditions, error) {
	var zero Conditions
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return zero, fmt.Errorf("无效的经纬度: %.4f, %.4f", lat, lon)
	}
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
	}, nil
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
