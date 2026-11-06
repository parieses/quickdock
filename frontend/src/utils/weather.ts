/**
 * 天气数据：Open-Meteo（免 key、无需注册）。
 *
 * 网络请求与刷新节奏都由 Go 后端管（services/weather 持唯一快照 + 30 分钟后台
 * ticker），前端只做类型适配、配置上报和 WMO 天气码归类。原因有两个：WebView2
 * 跟随 WinINET 系统代理，代理端口没起时前端 fetch 全超时；而前端 setInterval
 * 会随页面卸载消失、多窗口还会各拉各的。别把 fetch 或定时器搬回这里。
 *
 * localStorage 只剩「降级兜底」一个用途：后端刚重启、快照还没热起来时，
 * 先显示上一次成功结果（带旧时间戳），绝不把整张卡片清空。它不再承担节流职责。
 */

import { unwrap } from './api'
import {
  GetWeather, RefreshWeather, SetWeatherConfig, SearchCities,
} from '../../bindings/quickdock/services/weather/weatherservice'

const CACHE_KEY = 'qd_weather_cache_v1'

export interface WeatherConfig {
  enabled: boolean
  city: string
  latitude: number
  longitude: number
}

export interface WeatherData {
  temp: number
  apparent: number
  humidity: number
  wind: number
  /** WMO weather code */
  code: number
  isDay: boolean
  max: number
  min: number
  /** 本机时间戳（毫秒） */
  updatedAt: number
  city: string
}

export interface CityHit {
  name: string
  region: string
  country: string
  latitude: number
  longitude: number
}

/** 默认北京：首次开启时给一个能立刻出结果的坐标，用户再改成自己的城市。 */
export const DEFAULT_WEATHER: WeatherConfig = {
  enabled: false,
  city: '',
  latitude: 39.9075,
  longitude: 116.39723,
}

interface CachePayload {
  latitude: number
  longitude: number
  data: WeatherData
}

/** 把配置同步给后台：决定它刷不刷、刷哪个城市。配置没变时后端不会清快照。 */
export async function setWeatherConfig(cfg: WeatherConfig): Promise<void> {
  unwrap(await SetWeatherConfig(cfg.enabled, cfg.latitude, cfg.longitude, cfg.city))
}

/** 读后端快照。只有冷启动（后端还没有数据）时才会同步发一次请求。 */
export async function fetchWeather(): Promise<WeatherData> {
  const data = unwrap<WeatherData>(await GetWeather())
  if (!data || typeof data.temp !== 'number') {
    throw new Error('weather: empty current conditions')
  }
  return data
}

/** 手动刷新 / 改城市后强制取一次，绕过冷启动失败冷却。 */
export async function refreshWeather(): Promise<WeatherData> {
  const data = unwrap<WeatherData>(await RefreshWeather())
  if (!data || typeof data.temp !== 'number') {
    throw new Error('weather: empty current conditions')
  }
  return data
}

export async function searchCities(name: string, lang: 'zh' | 'en'): Promise<CityHit[]> {
  return unwrap<CityHit[]>(await SearchCities(name, lang)) ?? []
}

/** WMO code 归成 8 类，对应 i18n 的 `weatherKinds` 与一组图标。 */
export const WEATHER_GROUP_COUNT = 8
export function weatherGroup(code: number): number {
  if (code === 0) return 0
  if (code === 1 || code === 2) return 1
  if (code === 3) return 2
  if (code === 45 || code === 48) return 3
  if ([51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82].includes(code)) return 4
  if ([71, 73, 75, 77, 85, 86].includes(code)) return 5
  if ([95, 96, 99].includes(code)) return 6
  return 7
}

/** 读取降级缓存：坐标变了即失效。 */
export function readWeatherCache(latitude: number, longitude: number): WeatherData | null {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    if (!raw) return null
    const p = JSON.parse(raw) as CachePayload
    if (p.latitude !== latitude || p.longitude !== longitude || !p.data) return null
    return p.data
  } catch {
    return null
  }
}

export function writeWeatherCache(latitude: number, longitude: number, data: WeatherData): void {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify({ latitude, longitude, data } satisfies CachePayload))
  } catch {
    // 隐私模式 / 配额满：缓存不是必需，下一次仍会正常拉取
  }
}
