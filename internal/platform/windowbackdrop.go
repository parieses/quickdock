package platform

import (
	"fmt"
	"sync"
)

// 窗口材质（Windows 11 的 Mica / Acrylic）配置与能力判定。
//
// 与 uiZoom 同源：材质由 Wails 的 WebviewWindowOptions 在「窗口创建时」固定，
// 运行期改不了，所以配置必须存在数据目录之外的 quickdock.json —— 数据库要等
// Wails 服务启动（ServiceStartup）才打开，那时窗口早已建好。改完同样要重启才生效。
//
// 关键约束：只有 SupportsWindowBackdrop() 为真（Win11 22621+）时配置才会被应用。
// Win10 / 非 Windows 一律按「关」处理：窗口保持 BackgroundTypeSolid + 实色，
// 前端也拿不到这个设置项，行为与加这个功能之前完全一致。

const (
	BackdropOff     = "off"
	BackdropMica    = "mica"
	BackdropAcrylic = "acrylic"
	BackdropTabbed  = "tabbed" // Mica Alt
)

// windowBackdropModes 允许的档位，顺序即设置页的展示顺序。
var windowBackdropModes = []string{BackdropOff, BackdropMica, BackdropAcrylic, BackdropTabbed}

// WindowBackdropModes 返回允许的档位（副本，避免外部改写内部切片）。
func WindowBackdropModes() []string {
	out := make([]string, len(windowBackdropModes))
	copy(out, windowBackdropModes)
	return out
}

// NormalizeWindowBackdrop 把任意输入收敛到合法档位；空串/非法值一律回落「关」。
func NormalizeWindowBackdrop(mode string) string {
	for _, m := range windowBackdropModes {
		if m == mode {
			return m
		}
	}
	return BackdropOff
}

// LoadWindowBackdrop 读取持久化档位（未配置或非法 → 关）。
func LoadWindowBackdrop() string {
	return NormalizeWindowBackdrop(readDataDirConfig().WindowBackdrop)
}

// SetWindowBackdrop 持久化档位。非白名单值直接报错，不写脏值进配置文件。
func SetWindowBackdrop(mode string) error {
	if NormalizeWindowBackdrop(mode) != mode {
		return fmt.Errorf("不支持的窗口材质: %q", mode)
	}
	cfg := readDataDirConfig()
	cfg.WindowBackdrop = mode
	return writeDataDirConfig(cfg)
}

var (
	backdropMu        sync.RWMutex
	effectiveBackdrop = BackdropOff
)

// ResolveWindowBackdrop 在启动期（创建任何窗口之前）判定本次进程实际生效的材质并缓存。
//
// 为什么需要缓存一份「生效值」而不是各处直接读配置：用户改了设置但还没重启时，
// 配置文件里已经是新值，窗口却还是旧材质。前端要靠这个值决定要不要把 CSS 背景
// 变透明 —— 若直接读配置，就会出现「背景透明了、系统材质却还是旧的」的怪异画面。
func ResolveWindowBackdrop() string {
	mode := BackdropOff
	if SupportsWindowBackdrop() {
		mode = LoadWindowBackdrop()
	}
	backdropMu.Lock()
	effectiveBackdrop = mode
	backdropMu.Unlock()
	return mode
}

// EffectiveWindowBackdrop 返回本次启动实际生效的材质，运行期固定不变，直到下次重启。
func EffectiveWindowBackdrop() string {
	backdropMu.RLock()
	defer backdropMu.RUnlock()
	return effectiveBackdrop
}
