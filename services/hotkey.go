package services

import (
	"fmt"
	"strings"
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
		return dberr(err)
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
		return dberr(err)
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
		return dberr(err)
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
	key := vkToName(vk)
	if key == "" {
		key = fmt.Sprintf("VK_%d", vk)
	}
	parts = append(parts, key)
	return strings.Join(parts, "+")
}

func vkToName(vk int) string {
	names := map[int]string{
		0x20: "Space", 0x0D: "Enter", 0x1B: "Escape", 0x09: "Tab",
		0x08: "Backspace", 0x2E: "Delete", 0x2D: "Insert",
		0x21: "PageUp", 0x22: "PageDown", 0x24: "Home", 0x23: "End",
		0x25: "Left", 0x26: "Up", 0x27: "Right", 0x28: "Down",
		0x70: "F1", 0x71: "F2", 0x72: "F3", 0x73: "F4",
		0x74: "F5", 0x75: "F6", 0x76: "F7", 0x77: "F8",
		0x78: "F9", 0x79: "F10", 0x7A: "F11", 0x7B: "F12",
		0x30: "0", 0x31: "1", 0x32: "2", 0x33: "3", 0x34: "4",
		0x35: "5", 0x36: "6", 0x37: "7", 0x38: "8", 0x39: "9",
		0x41: "A", 0x42: "B", 0x43: "C", 0x44: "D", 0x45: "E",
		0x46: "F", 0x47: "G", 0x48: "H", 0x49: "I", 0x4A: "J",
		0x4B: "K", 0x4C: "L", 0x4D: "M", 0x4E: "N", 0x4F: "O",
		0x50: "P", 0x51: "Q", 0x52: "R", 0x53: "S", 0x54: "T",
		0x55: "U", 0x56: "V", 0x57: "W", 0x58: "X", 0x59: "Y", 0x5A: "Z",
		0xC0: "`",
		0x6A: "Num*", 0x6B: "Num+", 0x6D: "Num-", 0x6E: "Num.", 0x6F: "Num/",
	}
	if n, ok := names[vk]; ok {
		return n
	}
	return ""
}
