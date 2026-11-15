package weather

// 彩云天气数据源：需要用户在开放平台申请的 Token。
//
// 选它的理由和和风不同：彩云主打分钟级降水（雷达外推），实时温度更新频率更高，
// 适合「刚出门看一眼」的场景；但它免费版同样有每日额度上限，且必须注册。
//
// 接口是 v2.6：token 放在 URL 路径里（/{token}/{经度},{纬度}/realtime），这是
// 该版本唯一的认证位置，没法像和风那样塞进请求头——所以 token 会出现在 URL 中，
// 不要把这个 URL 打进日志。
//
// 坐标系：彩云要求中国大陆用 GCJ-02、境外用 WGS84。我们手里的坐标来自
// Open-Meteo 的检索接口（WGS84），与 GCJ-02 差几百米；天气数据本身是 1km 网格，
// 这点偏移不会改变结果，因此不做坐标转换（转错的代价比不转更大）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const caiyunBase = "https://api.caiyunapp.com"

type caiyunRealtimeResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Result *struct {
		Realtime *struct {
			Temperature float64 `json:"temperature"`
			Apparent    float64 `json:"apparent_temperature"`
			// Humidity 是 0~1 的比值，不是百分数——前端要的是 0~100。
			Humidity float64 `json:"humidity"`
			Skycon   string  `json:"skycon"`
			Wind     struct {
				Speed float64 `json:"speed"`
			} `json:"wind"`
		} `json:"realtime"`
	} `json:"result"`
}

type caiyunDailyResponse struct {
	Status string `json:"status"`
	Result *struct {
		Daily *struct {
			Temperature []struct {
				Max float64 `json:"max"`
				Min float64 `json:"min"`
			} `json:"temperature"`
		} `json:"daily"`
	} `json:"result"`
}

// fetchCaiyun 当前天气 + 今日最高/最低温。
// 与和风同策略：3d 预报失败只让 max/min 回落到当前温度，不拖垮整张卡。
func fetchCaiyun(lat, lon float64, city, token string) (Conditions, error) {
	var zero Conditions
	tk := strings.TrimSpace(token)
	// 用户可能把整串 "token" 连引号复制进来
	tk = strings.Trim(tk, "\"'")
	coord := strconv.FormatFloat(lon, 'f', 4, 64) + "," + strconv.FormatFloat(lat, 'f', 4, 64)
	// token 在路径里，特殊字符（某些平台发的 token 带 _ 或 -）按路径段转义，
	// 不会把 / 变成新的路径层级。
	tk = url.PathEscape(tk)

	var r caiyunRealtimeResponse
	u := fmt.Sprintf("%s/v2.6/%s/%s/realtime", caiyunBase, tk, coord)
	if err := getJSON(u, &r); err != nil {
		return zero, caiyunHTTPError(err)
	}
	if r.Status != "" && r.Status != "ok" {
		return zero, caiyunStatusError(r.Error)
	}
	if r.Result == nil || r.Result.Realtime == nil {
		return zero, fmt.Errorf("彩云天气响应缺少 realtime")
	}
	rt := r.Result.Realtime

	max, min := rt.Temperature, rt.Temperature
	var daily caiyunDailyResponse
	if err := getJSON(fmt.Sprintf("%s/v2.6/%s/%s/daily?dailysteps=1", caiyunBase, tk, coord), &daily); err == nil &&
		daily.Result != nil && daily.Result.Daily != nil && len(daily.Result.Daily.Temperature) > 0 {
		max = daily.Result.Daily.Temperature[0].Max
		min = daily.Result.Daily.Temperature[0].Min
	}

	return Conditions{
		Temp:      rt.Temperature,
		Apparent:  rt.Apparent,
		Humidity:  rt.Humidity * 100, // 0~1 → 0~100
		Wind:      rt.Wind.Speed,
		Code:      caiyunToWMO(rt.Skycon),
		IsDay:     !strings.HasSuffix(rt.Skycon, "_NIGHT"),
		Max:       max,
		Min:       min,
		UpdatedAt: time.Now().UnixMilli(),
		City:      city,
		Source:    SourceCaiyun,
	}, nil
}

// caiyunStatusError 把彩云的业务错误说清楚。
// 最常见的就两种：token 抄错 / 额度用完，用户看到原文 "token is invalid"
// 并不知道该去改哪里。
func caiyunStatusError(msg string) error {
	m := strings.TrimSpace(msg)
	switch {
	case m == "":
		return fmt.Errorf("彩云天气请求失败（检查 Token 是否有效）")
	case strings.Contains(strings.ToLower(m), "token"):
		return fmt.Errorf("彩云天气认证失败：请检查 Token 是否正确（%s）", m)
	case strings.Contains(strings.ToLower(m), "exceed"), strings.Contains(strings.ToLower(m), "limit"):
		return fmt.Errorf("彩云天气调用额度已用完（%s）", m)
	}
	return fmt.Errorf("彩云天气请求失败：%s", m)
}

// caiyunHTTPError 把 HTTP 层的失败翻成可操作的提示。
//
// 彩云的 token 无效是 **HTTP 400**（不是 401），body 里才写着 "token is invalid"，
// 所以必须读响应体，不能只看状态码。
func caiyunHTTPError(err error) error {
	var he *HTTPStatusError
	if !errors.As(err, &he) {
		return fmt.Errorf("彩云天气请求失败：%w", err)
	}
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal([]byte(he.Body), &body) == nil && strings.TrimSpace(body.Error) != "" {
		return caiyunStatusError(body.Error)
	}
	switch he.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("彩云天气认证失败：请检查 Token 是否正确")
	}
	return fmt.Errorf("彩云天气返回 HTTP %d", he.Status)
}

// caiyunToWMO 把彩云的 skycon 枚举翻成 WMO 码。
// skycon 是稳定的字符串枚举（含 _DAY/_NIGHT 后缀），不需要按数字区间猜。
func caiyunToWMO(skycon string) int {
	s := strings.ToUpper(strings.TrimSpace(skycon))
	switch s {
	case "CLEAR_DAY", "CLEAR_NIGHT":
		return 0
	case "PARTLY_CLOUDY_DAY", "PARTLY_CLOUDY_NIGHT":
		return 1
	case "CLOUDY":
		return 3
	case "FOG":
		return 45
	case "LIGHT_HAZE", "MODERATE_HAZE", "HEAVY_HAZE":
		return 45
	case "LIGHT_RAIN":
		return 61
	case "MODERATE_RAIN":
		return 63
	case "HEAVY_RAIN":
		return 65
	case "STORM_RAIN":
		return 82
	case "LIGHT_SNOW":
		return 71
	case "MODERATE_SNOW":
		return 73
	case "HEAVY_SNOW", "STORM_SNOW":
		return 75
	case "DUST", "SAND":
		// WMO 有独立的沙尘码（30~35），但前端 weatherGroup 没这一组，
		// 归到「雾」比归到「未知」更接近用户看到的景象。
		return 45
	case "WIND":
		return 1
	}
	return -1
}
