package platform

// VKToKeyName 将 Windows 虚拟键码 (VK) 转为可读的键名
func VKToKeyName(vk int) string {
	switch vk {
	case 0x20:
		return "Space"
	case 0x0D:
		return "Enter"
	case 0x1B:
		return "Escape"
	case 0x09:
		return "Tab"
	case 0x08:
		return "Backspace"
	case 0x2E:
		return "Delete"
	case 0x2D:
		return "Insert"
	case 0x21:
		return "PageUp"
	case 0x22:
		return "PageDown"
	case 0x24:
		return "Home"
	case 0x23:
		return "End"
	case 0x25:
		return "Left"
	case 0x26:
		return "Up"
	case 0x27:
		return "Right"
	case 0x28:
		return "Down"
	case 0xC0:
		return "`"
	case 0x70:
		return "F1"
	case 0x71:
		return "F2"
	case 0x72:
		return "F3"
	case 0x73:
		return "F4"
	case 0x74:
		return "F5"
	case 0x75:
		return "F6"
	case 0x76:
		return "F7"
	case 0x77:
		return "F8"
	case 0x78:
		return "F9"
	case 0x79:
		return "F10"
	case 0x7A:
		return "F11"
	case 0x7B:
		return "F12"
	case 0x6A:
		return "Num*"
	case 0x6B:
		return "Num+"
	case 0x6D:
		return "Num-"
	case 0x6E:
		return "Num."
	case 0x6F:
		return "Num/"
	}
	if vk >= 0x30 && vk <= 0x39 {
		return string(rune('0' + vk - 0x30))
	}
	if vk >= 0x41 && vk <= 0x5A {
		return string(rune('A' + vk - 0x41))
	}
	return ""
}
