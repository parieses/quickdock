package platform

import (
	"fmt"
	"math"
)

// 全局界面缩放（WebView ZoomFactor）的持久化与档位校验。
//
// 为什么放在 platform 的配置文件而不是数据库：窗口的 Zoom 与尺寸在「创建时」就固定了，
// 主窗口在 main() 里创建，而数据库要等 Wails 服务启动（ServiceStartup）才打开 —— 那时
// 窗口早已建好，改不动了。因此缩放必须存在一个进程启动即可读到的地方，这里复用
// datadir_override.go 的 quickdock.json（位于数据目录之外的系统配置目录）。
//
// 同理，改完不能即时生效，必须重启应用（前端随后调用 AppService.RestartApp）。

// uiZoomLevels 允许的缩放档位：与设置页刻度一一对应。
// 窗口尺寸按 uiZoom 等比放大（见 main.go 的 scaledSize），因此档位本身不会裁内容；
// 只收口到这几档，是为了不让用户填出超出屏幕的任意数值。
var uiZoomLevels = []float64{1.0, 1.1, 1.15, 1.2, 1.25, 1.5}

// DefaultUiZoom 未配置（或配置值非法）时的档位：1.0 即「标准」，不放大。
const DefaultUiZoom = 1.0

// uiZoomEpsilon 浮点比较容差：JSON 往返、手写配置都应判定为同一档位。
const uiZoomEpsilon = 1e-6

// LoadUiZoom 读取持久化的界面缩放系数；未配置或值不在允许档位内时回落 DefaultUiZoom。
// 必须在创建任何窗口之前调用。
func LoadUiZoom() float64 {
	z := readDataDirConfig().UiZoom
	for _, lv := range uiZoomLevels {
		if math.Abs(z-lv) < uiZoomEpsilon {
			return lv
		}
	}
	return DefaultUiZoom
}

// UiZoomLevels 返回允许的缩放档位（供上层做校验/展示，返回副本避免外部修改）。
func UiZoomLevels() []float64 {
	out := make([]float64, len(uiZoomLevels))
	copy(out, uiZoomLevels)
	return out
}

// SetUiZoom 持久化界面缩放系数。值必须命中允许档位之一，否则返回错误
// —— 拒绝畸形值好过写进去得到一个内容被裁的窗口。
func SetUiZoom(z float64) error {
	matched := -1.0
	for _, lv := range uiZoomLevels {
		if math.Abs(z-lv) < uiZoomEpsilon {
			matched = lv
			break
		}
	}
	if matched < 0 {
		return fmt.Errorf("不支持的缩放档位: %g", z)
	}
	cfg := readDataDirConfig()
	cfg.UiZoom = matched
	return writeDataDirConfig(cfg)
}
