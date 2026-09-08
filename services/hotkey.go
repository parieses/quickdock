package services

import (
	"fmt"
	"strings"

	"quickdock/internal/platform"
)

// ===== 热键配置 =====

func (a *AppService) GetHotkeyConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw, err := a.DB.GetSetting("hotkey")
	if err != nil || raw == "" {
		return Ok(&HotkeyConfig{Modifiers: 2, VK: 32, Label: "Ctrl+Space"})
	}
	var cfg HotkeyConfig
	fmt.Sscanf(raw, "%d,%d", &cfg.Modifiers, &cfg.VK)
	cfg.Label = hotkeyLabel(cfg.Modifiers, cfg.VK)
	return Ok(&cfg)
}

func (a *AppService) SetHotkeyConfig(modifiers, vk int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	val := fmt.Sprintf("%d,%d", modifiers, vk)
	if err := a.DB.SetSetting("hotkey", val); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) GetClipboardHotkeyConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw, err := a.DB.GetSetting("clipboard_hotkey")
	if err != nil || raw == "" {
		return Ok(&HotkeyConfig{Modifiers: 2, VK: 0xC0, Label: "Ctrl+`"})
	}
	var cfg HotkeyConfig
	fmt.Sscanf(raw, "%d,%d", &cfg.Modifiers, &cfg.VK)
	cfg.Label = hotkeyLabel(cfg.Modifiers, cfg.VK)
	return Ok(&cfg)
}

func (a *AppService) SetClipboardHotkeyConfig(modifiers, vk int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	val := fmt.Sprintf("%d,%d", modifiers, vk)
	if err := a.DB.SetSetting("clipboard_hotkey", val); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) GetPaletteHotkeyConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw, err := a.DB.GetSetting("palette_hotkey")
	if err != nil || raw == "" {
		return Ok(&HotkeyConfig{Modifiers: 2, VK: 0x4B, Label: "Ctrl+K"})
	}
	var cfg HotkeyConfig
	fmt.Sscanf(raw, "%d,%d", &cfg.Modifiers, &cfg.VK)
	cfg.Label = hotkeyLabel(cfg.Modifiers, cfg.VK)
	return Ok(&cfg)
}

func (a *AppService) SetPaletteHotkeyConfig(modifiers, vk int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	val := fmt.Sprintf("%d,%d", modifiers, vk)
	if err := a.DB.SetSetting("palette_hotkey", val); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) GetNoteHotkeyConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw, err := a.DB.GetSetting("note_hotkey")
	if err != nil || raw == "" {
		return Ok(&HotkeyConfig{Modifiers: 6, VK: 0x4E, Label: "Ctrl+Shift+N"})
	}
	var cfg HotkeyConfig
	fmt.Sscanf(raw, "%d,%d", &cfg.Modifiers, &cfg.VK)
	cfg.Label = hotkeyLabel(cfg.Modifiers, cfg.VK)
	return Ok(&cfg)
}

func (a *AppService) SetNoteHotkeyConfig(modifiers, vk int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	val := fmt.Sprintf("%d,%d", modifiers, vk)
	if err := a.DB.SetSetting("note_hotkey", val); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ===== 辅助函数 =====

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
