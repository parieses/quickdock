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
		Name      string   `json:"name"`
		Admin1    string   `json:"admin1"`
		Country   string   `json:"country"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	} `json:"results"`
}

// SearchCities 按名称检索城市，最多 6 条；无坐标的结果直接丢掉。
func SearchCities(name, lang string) ([]City, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return nil, fmt.Errorf("城市名无效")
	}
	if lang != "en" {
		lang = "zh"
	}
	u, err := url.Parse(geocodingURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("name", name)
	q.Set("count", "6")
	q.Set("language", lang)
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	var r geocodingResponse
	if err := getJSON(u.String(), &r); err != nil {
		return nil, err
	}
	hits := make([]City, 0, len(r.Results))
	for _, x := range r.Results {
		if x.Latitude == nil || x.Longitude == nil {
			continue
		}
		hits = append(hits, City{
			Name:      x.Name,
			Region:    x.Admin1,
			Country:   x.Country,
			Latitude:  *x.Latitude,
			Longitude: *x.Longitude,
		})
	}
	return hits, nil
}
