package weather

// 和风天气（QWeather）数据源：需要用户自己的 API Key。
//
// 为什么值得让用户多填一个 key：Open-Meteo 在某些国内网络下整域名不可达、
// wttr.in 是境外服务随时可能变慢。和风是国内服务，直连稳定，且免费订阅的
// 额度（每天 1000 次）对「每 30 分钟拉一次」这种用法完全够用。
//
// 两个坑：
//  1. 和风自 2026 年起逐步停用公共地址（devapi.qweather.com），新注册的开发者
//     每人有一个专属 API Host（形如 abc123.xy.qweatherapi.com，控制台-设置里
//     查看）。所以设置里 API Host 与 Key 是两个独立字段，Host 留空才退回公共地址。
//  2. 这里只按经纬度查询（location=经度,纬度），不查 LocationID——那需要先调
//     GeoAPI 换 ID，多一次请求、多一份缓存，而天气图标并不需要那么精细的定位。

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// qweatherDefaultHost 旧公共地址。只为兼容 2025 年前注册、还没迁移的老账号；
// 新账号不填 Host 会得到 401/403，错误提示里会让用户去控制台拿。
const qweatherDefaultHost = "devapi.qweather.com"

type qweatherNowResponse struct {
	// Code 是字符串（"200" 正常，401 认证失败，402 超额…），HTTP 状态码照样是 200，
	// 所以必须读它，不能只看 HTTP。
	Code string `json:"code"`
	Now  *struct {
		Temp      string `json:"temp"`
		FeelsLike string `json:"feelsLike"`
		Humidity  string `json:"humidity"`
		WindSpeed string `json:"windSpeed"`
		Icon      string `json:"icon"`
	} `json:"now"`
}

type qweatherDailyResponse struct {
	Code  string `json:"code"`
	Daily []struct {
		TempMax string `json:"tempMax"`
		TempMin string `json:"tempMin"`
	} `json:"daily"`
}

// normalizeQWeatherHost 容忍用户从浏览器地址栏整段粘过来：
// 去掉协议头、路径、末尾斜杠与空白。
func normalizeQWeatherHost(raw string) string {
	h := strings.TrimSpace(raw)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return qweatherDefaultHost
	}
	return h
}

// qweatherCodeError 把和风的业务错误码翻成人话。
// 只说「HTTP 200 但 code=401」对用户没有任何帮助——他需要知道去改哪个字段。
func qweatherCodeError(code string) error {
	switch strings.TrimSpace(code) {
	case "", "200":
		return nil
	case "401":
		return fmt.Errorf("和风天气认证失败：请检查 API Key 与 API Host（新账号必须填自己的 Host）")
	case "402":
		return fmt.Errorf("和风天气今日调用额度已用完，明天恢复或升级订阅")
	case "403":
		return fmt.Errorf("和风天气拒绝访问：请确认订阅有效、且已在控制台开启对应接口")
	case "404":
		return fmt.Errorf("和风天气查不到这个位置")
	case "429":
		return fmt.Errorf("和风天气请求过于频繁，请稍后再试")
	}
	return fmt.Errorf("和风天气返回错误码 %s", code)
}

// fetchQWeather 当前天气 + 今日最高/最低温。
//
// 第二次请求（3d 预报）失败不影响整体：温度已经有了，max/min 用当前温度兜底，
// 比整张卡报错更合理。
func fetchQWeather(lat, lon float64, city, key, apiHost string) (Conditions, error) {
	var zero Conditions
	host := normalizeQWeatherHost(apiHost)
	// 和风的经纬度参数是「经度,纬度」，且只要两位小数（约 1km，天气用足够）。
	loc := strconv.FormatFloat(lon, 'f', 2, 64) + "," + strconv.FormatFloat(lat, 'f', 2, 64)
	hdr := map[string]string{"X-QW-Api-Key": key}

	var now qweatherNowResponse
	if err := getJSONWithHeaders("https://"+host+"/v7/weather/now?location="+url.QueryEscape(loc), &now, hdr); err != nil {
		return zero, qweatherHTTPError(err)
	}
	if err := qweatherCodeError(now.Code); err != nil {
		return zero, err
	}
	if now.Now == nil {
		return zero, fmt.Errorf("和风天气响应缺少 now")
	}
	n := now.Now
	temp, ok := atof(n.Temp)
	if !ok {
		return zero, fmt.Errorf("和风天气温度字段不是数字: %q", n.Temp)
	}

	max, min := temp, temp
	var daily qweatherDailyResponse
	if err := getJSONWithHeaders("https://"+host+"/v7/weather/3d?location="+url.QueryEscape(loc), &daily, hdr); err == nil && len(daily.Daily) > 0 {
		max = atofOr(daily.Daily[0].TempMax, temp)
		min = atofOr(daily.Daily[0].TempMin, temp)
	}

	return Conditions{
		Temp:      temp,
		Apparent:  atofOr(n.FeelsLike, temp),
		Humidity:  atofOr(n.Humidity, 0),
		Wind:      atofOr(n.WindSpeed, 0),
		Code:      qweatherToWMO(n.Icon),
		IsDay:     qweatherIsDay(n.Icon),
		Max:       max,
		Min:       min,
		UpdatedAt: time.Now().UnixMilli(),
		City:      city,
		Source:    SourceQWeather,
	}, nil
}

// qweatherHTTPError 把 HTTP 层的失败也说得具体一点：
// 401/403（以及个别 WAF 前置的 400）大多是 key 或 host 填错，而不是「天气服务坏了」。
func qweatherHTTPError(err error) error {
	var he *HTTPStatusError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest:
			return fmt.Errorf("和风天气认证失败：请检查 API Key 与 API Host（新账号必须填自己的 Host）")
		}
	}
	// 网络层失败（超时 / 域名不通）也要说清是哪个源没通，否则日志里只是一句
	// 「天气服务不可达」，分不出是和多风还是 Open-Meteo 的问题。
	return fmt.Errorf("和风天气请求失败：%w", err)
}

// qweatherIsDay 用图标码判断昼夜。
//
// 和风只有晴/多云这几个才有夜间版（150-153），雨雪雾不分昼夜、也没必要分：
// 前端的夜间图标只对晴天有区别，其它天气一律用白天图标。
func qweatherIsDay(icon string) bool {
	switch strings.TrimSpace(icon) {
	case "150", "151", "152", "153":
		return false
	}
	return true
}

// qweatherToWMO 把和风图标码翻成 WMO 码（前端 weatherGroup 只认后者）。
//
// 和风的码是分段编号：1xx 晴/云、3xx 雨、4xx 雪、5xx 雾霾、9xx 冷热/未知。
// 只需归到前端的 8 个组，不需要逐个精确对应（见 wwo.go 的同样说明）。
func qweatherToWMO(icon string) int {
	code, err := strconv.Atoi(strings.TrimSpace(icon))
	if err != nil {
		return -1
	}
	switch {
	case code == 100, code == 150: // 晴
		return 0
	case code == 101, code == 102, code == 103, // 多云 / 少云 / 晴间多云
		code == 151, code == 152, code == 153:
		return 1
	case code == 104: // 阴
		return 3
	}

	switch code {
	// 雨
	case 300, 301, 350, 351: // 阵雨
		return 80
	case 302, 303, 304: // 雷阵雨 / 强雷阵雨 / 雷阵雨伴有冰雹
		return 95
	case 305, 309: // 小雨 / 毛毛雨
		return 61
	case 306, 314, 399: // 中雨 / 小到中雨
		return 63
	case 307, 308, 310, 311, 312, 315, 316, 317, 318: // 大雨及各种暴雨
		return 65
	case 313: // 冻雨
		return 66

	// 雪
	case 400, 407, 408: // 小雪 / 阵雪
		return 71
	case 401, 409, 499: // 中雪 / 中到大雪
		return 73
	case 402, 403, 410, 456, 457: // 大雪 / 暴雪
		return 75
	case 404, 405, 406: // 雨夹雪
		return 79

	// 雾 / 霾 / 沙尘（WMO 无沙尘细类，前端也只归到「雾」这一组）
	case 500, 501, 509, 510, 514, 515, 502, 503, 504:
		return 45
	case 511, 512, 513: // 沙尘暴 / 强沙尘暴 / 浮尘
		return 45
	}
	return -1
}
