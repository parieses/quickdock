package services

import (
	"fmt"
	"strconv"
	"strings"

	"quickdock/internal/platform"
)

// hotkeySetting 热键设置定义（用于工厂函数）
// 注意：HotkeyConfig 类型定义在 types.go 中
type hotkeySetting struct {
	key          string // 数据库键名
	defaultMod   int    // 默认修饰键
	defaultVK    int    // 默认虚拟键码
	defaultLabel string // 默认标签
}

// 热键设置定义
var hotkeySettings = []hotkeySetting{
	{"hotkey", 2, 32, "Ctrl+Space"},          // 主热键
	{"clipboard_hotkey", 2, 0xC0, "Ctrl+`"},  // 剪贴板热键
	{"palette_hotkey", 2, 0x4B, "Ctrl+K"},    // 命令面板热键
	{"note_hotkey", 6, 0x4E, "Ctrl+Shift+N"}, // 笔记热键
	{"screenshot_hotkey", 0, 0x70, "F1"},     // 截图热键（无修饰键，故 modifiers 为 0）
}

// ===== 热键配置 =====

// GetHotkeyConfig 获取热键配置（向后兼容，实际调用 getHotkeyConfigByKey）
func (a *AppService) GetHotkeyConfig() *ApiResult {
	return a.getHotkeyConfigByKey("hotkey", 2, 32, "Ctrl+Space")
}

// SetHotkeyConfig 设置热键配置（向后兼容，实际调用 setHotkeyConfigByKey）
func (a *AppService) SetHotkeyConfig(modifiers, vk int) *ApiResult {
	return a.setHotkeyConfigByKey("hotkey", modifiers, vk)
}

// GetClipboardHotkeyConfig 获取剪贴板热键配置
func (a *AppService) GetClipboardHotkeyConfig() *ApiResult {
	return a.getHotkeyConfigByKey("clipboard_hotkey", 2, 0xC0, "Ctrl+`")
}

// SetClipboardHotkeyConfig 设置剪贴板热键配置
func (a *AppService) SetClipboardHotkeyConfig(modifiers, vk int) *ApiResult {
	return a.setHotkeyConfigByKey("clipboard_hotkey", modifiers, vk)
}

// GetPaletteHotkeyConfig 获取命令面板热键配置
func (a *AppService) GetPaletteHotkeyConfig() *ApiResult {
	return a.getHotkeyConfigByKey("palette_hotkey", 2, 0x4B, "Ctrl+K")
}

// SetPaletteHotkeyConfig 设置命令面板热键配置
func (a *AppService) SetPaletteHotkeyConfig(modifiers, vk int) *ApiResult {
	return a.setHotkeyConfigByKey("palette_hotkey", modifiers, vk)
}

// GetNoteHotkeyConfig 获取笔记热键配置
func (a *AppService) GetNoteHotkeyConfig() *ApiResult {
	return a.getHotkeyConfigByKey("note_hotkey", 6, 0x4E, "Ctrl+Shift+N")
}

// SetNoteHotkeyConfig 设置笔记热键配置
func (a *AppService) SetNoteHotkeyConfig(modifiers, vk int) *ApiResult {
	return a.setHotkeyConfigByKey("note_hotkey", modifiers, vk)
}

// GetScreenshotHotkeyConfig 获取截图热键配置（默认 F1，无修饰键）
func (a *AppService) GetScreenshotHotkeyConfig() *ApiResult {
	return a.getHotkeyConfigByKey("screenshot_hotkey", 0, 0x70, "F1")
}

// SetScreenshotHotkeyConfig 设置截图热键配置
func (a *AppService) SetScreenshotHotkeyConfig(modifiers, vk int) *ApiResult {
	return a.setHotkeyConfigByKey("screenshot_hotkey", modifiers, vk)
}

// getHotkeyConfigByKey 通用热键配置获取（工厂函数）
func (a *AppService) getHotkeyConfigByKey(key string, defaultMod, defaultVK int, defaultLabel string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw, err := a.DB.GetSetting(key)
	if err != nil || raw == "" {
		return Ok(&HotkeyConfig{Modifiers: defaultMod, VK: defaultVK, Label: defaultLabel})
	}
	var cfg HotkeyConfig
	_, err = fmt.Sscanf(raw, "%d,%d", &cfg.Modifiers, &cfg.VK)
	if err != nil {
		// 解析失败，返回默认值
		return Ok(&HotkeyConfig{Modifiers: defaultMod, VK: defaultVK, Label: defaultLabel})
	}
	cfg.Label = hotkeyLabel(cfg.Modifiers, cfg.VK)
	return Ok(&cfg)
}

// setHotkeyConfigByKey 通用热键配置设置（工厂函数）
func (a *AppService) setHotkeyConfigByKey(key string, modifiers, vk int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	val := fmt.Sprintf("%d,%d", modifiers, vk)
	if err := a.DB.SetSetting(key, val); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ===== 辅助函数 =====

// hotkeyLabel 根据修饰键和虚拟键码生成标签
func hotkeyLabel(modifiers, vk int) string {
	var parts []string
	if modifiers&1 != 0 {
		parts = append(parts, "Alt")
	}
	if modifiers&2 != 0 {
		parts = append(parts, "Ctrl")
	}
	if modifiers&4 != 0 {
		parts = append(parts, "Shift")
	}
	if modifiers&8 != 0 {
		parts = append(parts, "Win")
	}
	key := platform.VKToKeyName(vk)
	if key == "" {
		key = fmt.Sprintf("VK_%d", vk)
	}
	parts = append(parts, key)
	return strings.Join(parts, "+")
}

// ===== 窗口管理热键配置 =====
//
// 窗口管理有 15 个动作（左/右/上/下/四分/居中/最大化/还原/最小化/置顶/移屏），
// 每个动作在 DB 中各存一条 "modifiers,vk"。为避免过度膨胀，前端按 key 调统一的
// Get/Set 方法（默认值由各动作在前端传入），后端复用 getHotkeyConfigByKey 工厂。

// GetWinmgrHotkeyConfig 读取某个窗口管理动作的热键配置。
// defMod/defVK 为该动作的默认热键；未配置或解析失败时回退默认值。
func (a *AppService) GetWinmgrHotkeyConfig(key string, defMod, defVK int) *ApiResult {
	return a.getHotkeyConfigByKey(key, defMod, defVK, "")
}

// SetWinmgrHotkeyConfig 保存某个窗口管理动作的热键配置。
func (a *AppService) SetWinmgrHotkeyConfig(key string, modifiers, vk int) *ApiResult {
	return a.setHotkeyConfigByKey(key, modifiers, vk)
}

// GetWinmgrRatio 读取分屏比例（左/右分屏时左占百分比，默认 50）。
func (a *AppService) GetWinmgrRatio() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	ratio := 50
	if raw, err := a.DB.GetSetting("winmgr_ratio"); err == nil && raw != "" {
		if v, e := strconv.Atoi(raw); e == nil && v >= 10 && v <= 90 {
			ratio = v
		}
	}
	return Ok(ratio)
}

// SetWinmgrRatio 保存分屏比例（约束在 10~90）。
func (a *AppService) SetWinmgrRatio(ratio int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if ratio < 10 {
		ratio = 10
	}
	if ratio > 90 {
		ratio = 90
	}
	if err := a.DB.SetSetting("winmgr_ratio", strconv.Itoa(ratio)); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// GetWinmgrFloatHotkeyConfig 读取窗口管理浮层热键配置（默认 Ctrl+Alt+W）。
func (a *AppService) GetWinmgrFloatHotkeyConfig() *ApiResult {
	return a.getHotkeyConfigByKey("winmgr_float_hotkey", 3, 0x57, "")
}

// SetWinmgrFloatHotkeyConfig 保存窗口管理浮层热键配置。
func (a *AppService) SetWinmgrFloatHotkeyConfig(modifiers, vk int) *ApiResult {
	return a.setHotkeyConfigByKey("winmgr_float_hotkey", modifiers, vk)
}
