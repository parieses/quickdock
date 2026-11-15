package weather

// 备用源的天气码转换。
//
// 为什么需要这张表：主源 Open-Meteo 返回 WMO 4677 码（0=晴、45=雾、61=小雨…），
// 前端的图标与文案（weatherGroup）就是按它归类的；备用源 wttr.in 返回的是
// WorldWeatherOnline 那一套（113=晴、116=多云、296=小雨…）。不转换的话，
// 一旦回退到备用源，界面上每种天气都会变成「未知」。
//
// 只保证「归到同一类」而不是逐值精确对应：前端的 weatherGroup 只分 8 组
// （晴/多云/阴/雾/雨/雪/雷/未知），多一个少一个中间档看不出来。

// wwoToWMO 把 WWO 天气码翻成最接近的 WMO 码；未知码返回 -1（前端归到「未知」）。
func wwoToWMO(code int) int {
	switch code {
	case 113: // Sunny / Clear
		return 0
	case 116: // Partly cloudy
		return 2
	case 119, 122: // Cloudy / Overcast
		return 3
	case 143, 149, 248: // Mist / Smoke / Fog
		return 45
	case 260: // Freezing fog
		return 48

		// 毛毛雨
	case 263, 266, 281, 284:
		return 51

		// 雨
	case 293, 296, 353:
		return 61
	case 299, 302:
		return 63
	case 305, 308, 356:
		return 65
	case 359: // Torrential rain shower
		return 82

		// 冻雨
	case 311, 314:
		return 66

		// 雨夹雪 / 冰粒
	case 317, 320, 350, 362, 365, 374, 377:
		return 79

		// 雪
	case 323, 326, 368:
		return 71
	case 329, 332:
		return 73
	case 335, 338, 371:
		return 75
	case 227: // Blowing snow
		return 77
	case 230: // Blizzard
		return 75

		// 雷
	case 200, 386, 392:
		return 95
	case 389, 395:
		return 96
	}
	return -1
}
