package platform

import "github.com/wailsapp/wails/v3/pkg/application"

// ApplyWindowBackdrop 按本次启动的生效档位给窗口选项套上系统材质，返回是否套上。
//
// 为什么放在 platform 而不是 main：套材质的建窗点分散在 main（主窗口 / SSH /
// 四个浮窗）与 internal/*（插件独立窗口），而「档位 → Wails BackdropType」的映射
// 必须只有一份 —— 三处各写一遍，迟早出现「Mica Alt 只在主窗口生效」这类不一致。
//
// 返回 false 表示保持实色（系统不支持 / 用户选「关」/ 非 Windows），调用方不用再判断。
// 注意：Translucent 下 Wails 会把 chromium 底色强制设为 (0,0,0,0)，建窗选项里的
// BackgroundColour 只在不套材质时才有意义。
func ApplyWindowBackdrop(opts *application.WebviewWindowOptions) bool {
	bt, ok := windowBackdropType()
	if !ok {
		return false
	}
	opts.BackgroundType = application.BackgroundTypeTranslucent
	opts.Windows.BackdropType = bt
	return true
}

// windowBackdropType 返回本次启动生效的 Wails 材质档位；未启用时 ok=false。
//
// 建窗（ApplyWindowBackdrop）与运行期重写（ReapplyWindowBackdrop）必须共用这一份
// 映射：两处各写一遍 switch，迟早出现「选 Mica Alt 只有主窗口对」这类错位。
func windowBackdropType() (application.BackdropType, bool) {
	switch EffectiveWindowBackdrop() {
	case BackdropMica:
		return application.Mica, true
	case BackdropAcrylic:
		return application.Acrylic, true
	case BackdropTabbed:
		return application.Tabbed, true
	}
	return 0, false
}
