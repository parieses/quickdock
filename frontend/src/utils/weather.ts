/**
 * 天气数据：5 个数据源，3 个免 key（Open-Meteo / wttr.in / 自动切换），
 * 2 个需要用户自己的 key（和风天气 / 彩云天气）。
 *
 * 数据源在设置面板里可选（默认 auto：主源不通时自动换备用源）。之所以要能手动
 * 指定，是因为不同网络下可达的那个源不一样——auto 只会让用户看到「能用」，
 * 不知道到底走的是谁；出问题时也没法自己钉住一个源来验证。
 *
 * 需要 key 的两个源：key 与和风的 API Host 都存在同一个配置对象里（localStorage
 * 的 qd_weather_config），只发给本机 Go 后端，不经过任何第三方中转；上报时也只
 * 发当前选中源的那一份，切回免 key 源后后端内存里不会留着旧 key。
 *
 * 网络请求与刷新节奏都由 Go 后端管（services/weather 持唯一快照 + 30 分钟后台
 * ticker），前端只做类型适配、配置上报和 WMO 天气码归类。原因有两个：WebView2
 * 跟随 WinINET 系统代理，代理端口没起时前端 fetch 全超时；而前端 setInterval
 * 会随页面卸载消失、多窗口还会各拉各的。别把 fetch 或定时器搬回这里。
 *
 * localStorage 还兼「降级兜底」：后端刚重启、快照还没热起来时，先显示上一次
 * 成功结果（带旧时间戳），绝不把整张卡片清空。它不再承担节流职责。
 */

import { unwrap } from './api'
import {
  GetWeather, RefreshWeather, SetWeatherConfig, SearchCities,
} from '../../bindings/quickdock/services/weather/weatherservice'

import { writeStored } from './storage'
const CACHE_KEY = 'qd_weather_cache_v1'

export interface WeatherConfig {
  enabled: boolean
  city: string
  latitude: number
  longitude: number
  /** 数据源；auto = 主源失败自动换备用源。 */
  source: WeatherSource
  /** 和风天气的 API Key（只有 source='qweather' 才发给后端） */
  qweatherKey: string
  /** 和风的专属 API Host（新账号必填，老账号可留空走公共地址） */
  qweatherHost: string
  /** 彩云天气的 Token（只有 source='caiyun' 才发给后端） */
  caiyunToken: string
}

/**
 * 可选数据源。
 *
 * 前三个免 key：天气卡是开机第一眼的信息，默认不该要求用户先去注册。
 * 后两个需要 key：它们存在的意义是「公共接口在国内网络下时通时不通」，
 * 愿意花两分钟注册的人可以钉住一个稳定的国内源。
 */
export type WeatherSource = 'auto' | 'open-meteo' | 'wttr' | 'qweather' | 'caiyun'

export const WEATHER_SOURCES: WeatherSource[] = ['auto', 'open-meteo', 'wttr', 'qweather', 'caiyun']

/** 源的静态属性：是否要 key、注册/控制台地址（UI 里给「去注册」按钮用）。 */
export const WEATHER_SOURCE_META: Record<WeatherSource, { needsKey: boolean; consoleUrl: string }> = {
  auto: { needsKey: false, consoleUrl: '' },
  'open-meteo': { needsKey: false, consoleUrl: '' },
  wttr: { needsKey: false, consoleUrl: '' },
  // 控制台能同时注册、建项目、看 API Host 与 Key（2026 年起新账号必须填 Host）
  qweather: { needsKey: true, consoleUrl: 'https://console.qweather.com/' },
  // 开放平台 → 天气令牌里拿 Token
  caiyun: { needsKey: true, consoleUrl: 'https://dashboard.caiyunapp.com/' },
}

/** 老配置（无 source 字段）或手改过的非法值 → auto。 */
export function normalizeSource(s: unknown): WeatherSource {
  return s === 'open-meteo' || s === 'wttr' || s === 'qweather' || s === 'caiyun' ? s : 'auto'
}

/**
 * 后端标注的「实际用了哪个源」→ 展示用的源 id。
 *
 * 与 normalizeSource 的区别：那个用于「用户选了什么」，未知值合理回落 auto；
 * 这个是「这次数据来自哪里」，auto 模式下后端会给出具体源，不能再变成 auto，
 * 否则界面上永远显示「自动」，看不出到底走的哪个。旧缓存的 "wttr.in" 也在这里兼容。
 */
export function actualSource(raw: unknown): WeatherSource {
  const s = String(raw || '').trim().toLowerCase()
  if (s === 'wttr' || s === 'wttr.in') return 'wttr'
  if (s === 'open-meteo') return 'open-meteo'
  if (s === 'qweather') return 'qweather'
  if (s === 'caiyun') return 'caiyun'
  return 'auto'
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
  /** 这次数据实际来自哪个源（后端标注，auto 模式下用来告诉用户当前走的是哪个） */
  source?: string
}

export interface CityHit {
  name: string
  /** 上级行政区，形如「河南 · 郑州市」；同名地名靠它区分。港澳台由后端统一成中国省级行政区。 */
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
  source: 'auto',
  qweatherKey: '',
  qweatherHost: '',
  caiyunToken: '',
}

interface CachePayload {
  latitude: number
  longitude: number
  data: WeatherData
}

/**
 * 把配置同步给后台：决定它刷不刷、刷哪个城市、用哪个源。配置没变时后端不会清快照。
 *
 * 只发当前源要用的那份 key：切到免 key 源时后端内存里就不该再留着上次的 key，
 * 免得「已经不用和风了，进程里还揣着一个和风 key」。
 */
export async function setWeatherConfig(cfg: WeatherConfig): Promise<void> {
  const src = normalizeSource(cfg.source)
  const key = src === 'qweather' ? cfg.qweatherKey : src === 'caiyun' ? cfg.caiyunToken : ''
  const host = src === 'qweather' ? cfg.qweatherHost : ''
  unwrap(await SetWeatherConfig(
    cfg.enabled, cfg.latitude, cfg.longitude, cfg.city, src, key || '', host || '',
  ))
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
  writeStored(CACHE_KEY, JSON.stringify({ latitude, longitude, data } satisfies CachePayload))
}
