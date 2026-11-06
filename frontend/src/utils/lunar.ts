/**
 * 农历、二十四节气与传统节日：纯计算，零依赖，离线可用。
 *
 * 为什么不用数据表 / 第三方库：
 * - 农历（含闰月、干支纪年）直接交给 WebView2 自带的 ICU 日历（Intl 的 ca-chinese），
 *   覆盖 1900–2100，比手抄一份 201 项 lunarInfo 常量可靠，也不必新增 npm 依赖；
 * - 节气用太阳视黄经天文近似（Meeus《天文算法》简化式）实时求解太阳到达每 15°
 *   的时刻，不受「某年 C 值表」的年份修正影响，跨年也不用维护数据表；
 * - 本文件只产出**结构化数据**（数字与索引），所有展示文案由组件走 i18n，
 *   不在这里写死中文（AGENTS.md R8）。
 *
 * 时区口径：节气是中国的太阳黄经时刻，一律按北京时间（UTC+8）取日期；
 * 传入的 Date 用本地时区的年月日。非东八区环境下两者可能差一天，属既定口径。
 */

const DEG = Math.PI / 180
const DAY_MS = 86400000
const BEIJING_OFFSET_MS = 8 * 3600 * 1000
const TERM_COUNT = 24

/** 传统顺序 0=立春 的节气对应的太阳视黄经（度）。 */
function termLongitude(index: number): number {
  return (315 + index * 15) % 360
}

function pad2(n: number): string {
  return n < 10 ? '0' + n : String(n)
}

/** 本地时区的 yyyy-MM-dd。 */
export function localISO(d: Date): string {
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
}

/** 北京时间下的 yyyy-MM-dd（用于节气日期）。 */
function beijingISO(utcMs: number): string {
  const d = new Date(utcMs + BEIJING_OFFSET_MS)
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`
}

function julianDay(utcMs: number): number {
  return utcMs / DAY_MS + 2440587.5
}

/**
 * 给定儒略日求太阳视黄经（0..360 度）。
 * 取 Meeus 第 25 章的低精度太阳坐标 + 章动/光行差主项，1900–2100 内
 * 与精密历的误差在角分量级，换算成时刻约几分钟，足够判定节气落在哪天。
 */
function sunLongitude(jd: number): number {
  const T = (jd - 2451545.0) / 36525
  const L0 = 280.46646 + 36000.76983 * T + 0.0003032 * T * T
  const M = 357.52911 + 35999.05029 * T - 0.0001537 * T * T
  const Mr = M * DEG
  const C =
    (1.914602 - 0.004817 * T - 0.000014 * T * T) * Math.sin(Mr) +
    (0.019993 - 0.000101 * T) * Math.sin(2 * Mr) +
    0.000289 * Math.sin(3 * Mr)
  const omega = (125.04 - 1934.136 * T) * DEG
  const L = L0 + C - 0.00569 - 0.00478 * Math.sin(omega)
  return ((L % 360) + 360) % 360
}

interface TermPoint {
  /** 传统顺序索引，0=立春 */
  index: number
  /** 北京时间日期 yyyy-MM-dd */
  date: string
}

const termCache = new Map<number, TermPoint[]>()

/**
 * 某一年里 24 个节气的日期：从北京时间 1/1 起每 6 小时采样一次太阳黄经，
 * 检测它跨过哪个 15° 边界，再线性插值出交节时刻。
 * 一年扫描约 1460 次，按年缓存后每次进页面只算一次。
 */
function termsOfYear(year: number): TermPoint[] {
  const cached = termCache.get(year)
  if (cached) return cached
  const step = 6 * 3600 * 1000
  // 北京时间 year-01-01 00:00 == UTC 前一年 12-31 16:00
  const t0 = Date.UTC(year - 1, 11, 31, 16)
  const end = Date.UTC(year, 11, 31, 16)
  const found: (TermPoint | undefined)[] = new Array(TERM_COUNT)
  let prevT = t0
  let prevLon = sunLongitude(julianDay(prevT))
  for (let t = t0 + step; t <= end + step; t += step) {
    const lon = sunLongitude(julianDay(t))
    // 黄经在 360→0 处回绕；把本步的推进量统一成正值，角度比较全部按「从 prevLon 顺时针走多远」
    let delta = lon - prevLon
    if (delta < 0) delta += 360
    for (let i = 0; i < TERM_COUNT; i++) {
      if (found[i]) continue
      let off = termLongitude(i) - prevLon
      if (off < 0) off += 360
      if (off <= 0 || off > delta) continue
      found[i] = { index: i, date: beijingISO(prevT + (off / delta) * step) }
    }
    prevT = t
    prevLon = lon
  }
  const out = found.filter((x): x is TermPoint => x !== undefined)
  termCache.set(year, out)
  return out
}

/** 以 year 为中心，取前后三年的节气并按时间排成一条轴，避开「小寒属于哪一年」的归属问题。 */
const timelineCache = new Map<number, TermPoint[]>()
function termTimeline(year: number): TermPoint[] {
  const cached = timelineCache.get(year)
  if (cached) return cached
  const out = [
    ...termsOfYear(year - 1),
    ...termsOfYear(year),
    ...termsOfYear(year + 1),
  ].sort((a, b) => (a.date < b.date ? -1 : a.date > b.date ? 1 : a.index - b.index))
  timelineCache.set(year, out)
  return out
}

/** ICU 中国农历解析结果；不支持时全部为零值，UI 会跳过农历行。 */
interface LunarParts {
  month: number
  day: number
  leap: boolean
  ganzhiYear: string
  zodiac: number
}

const MONTH_WORDS = [
  '正月', '二月', '三月', '四月', '五月', '六月',
  '七月', '八月', '九月', '十月', '十一月', '十二月',
]
const EARTHLY_BRANCHES = ['子', '丑', '寅', '卯', '辰', '巳', '午', '未', '申', '酉', '戌', '亥']

function lunarParts(d: Date): LunarParts {
  const empty: LunarParts = { month: 0, day: 0, leap: false, ganzhiYear: '', zodiac: -1 }
  try {
    const fmt = new Intl.DateTimeFormat('zh-CN-u-ca-chinese', {
      year: 'numeric', month: 'long', day: 'numeric',
    })
    // 断言成宽松形状：TS 的 DateTimeFormatPartTypes 未必列出 yearName/relatedYear
    const parts = fmt.formatToParts(d) as Array<{ type: string; value: string }>
    const pick = (type: string) => parts.find(p => p.type === type)?.value ?? ''
    const monthText = pick('month')
    const leap = monthText.startsWith('闰')
    const base = leap ? monthText.slice(1) : monthText
    const ganzhiYear = pick('yearName')
    return {
      month: MONTH_WORDS.indexOf(base) + 1,
      day: Number.parseInt(pick('day'), 10) || 0,
      leap,
      ganzhiYear,
      zodiac: EARTHLY_BRANCHES.indexOf(ganzhiYear.slice(-1)),
    }
  } catch {
    // 极老的 WebView2 若缺 chinese 历，日期卡仍可用，只是不显示农历
    return empty
  }
}

/** 节日 key 走 i18n 的 `festival<Key>` 键，PascalCase 后缀。 */
function festivalsOf(
  month: number, day: number, leap: boolean,
  solarMD: string, nextDayIsLunarNewYear: boolean,
): string[] {
  const out: string[] = []
  if (!leap && month > 0 && day > 0) {
    const lunarFestivals: Record<string, string> = {
      '1-1': 'Spring', '1-15': 'Lantern', '5-5': 'DragonBoat', '7-7': 'Qixi',
      '7-15': 'Ghost', '8-15': 'MidAutumn', '9-9': 'Chongyang', '12-8': 'Laba',
    }
    const hit = lunarFestivals[`${month}-${day}`]
    if (hit) out.push(hit)
    // 腊月最后一天靠「明天是正月初一」判定，不需要知道当月是大月还是小月
    if (month === 12 && nextDayIsLunarNewYear) out.push('NewYearEve')
  }
  const solarFestivals: Record<string, string> = {
    '01-01': 'NewYear', '05-01': 'Labor', '10-01': 'National', '12-25': 'Christmas',
  }
  const solarHit = solarFestivals[solarMD]
  if (solarHit) out.push(solarHit)
  return out
}

export interface LunarDay {
  /** 本地日期 yyyy-MM-dd */
  date: string
  /** 农历月 1..12，解析失败为 0 */
  month: number
  /** 农历日 1..30，解析失败为 0 */
  day: number
  /** 是否闰月 */
  leap: boolean
  /** 干支纪年（如「丙午」），ICU 不支持时为空 */
  ganzhiYear: string
  /** 生肖索引 0=鼠 … 11=猪，无法判定为 -1 */
  zodiac: number
  /** 今日节气索引（0=立春），非节气为 -1 */
  term: number
  /** 下一个节气索引 */
  nextTerm: number
  /** 下一个节气的北京时间日期 */
  nextTermDate: string
  /** 距下一个节气的天数 */
  nextTermIn: number
  /** 今日节日 key 列表 */
  festivals: string[]
}

/** 描述某一天：农历 + 节气 + 节日。传本地 Date，返回纯数据供 i18n 渲染。 */
export function describeDay(d: Date = new Date()): LunarDay {
  const date = localISO(d)
  const lunar = lunarParts(d)
  const tomorrow = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1)
  const nextLunar = lunarParts(tomorrow)

  const timeline = termTimeline(d.getFullYear())
  const todayPoint = timeline.find(p => p.date === date)
  const nextPoint = timeline.find(p => p.date > date)
  const nextTermIn = nextPoint
    ? Math.round(
        (Date.parse(`${nextPoint.date}T00:00:00Z`) - Date.parse(`${date}T00:00:00Z`)) / DAY_MS,
      )
    : 0

  return {
    date,
    month: lunar.month,
    day: lunar.day,
    leap: lunar.leap,
    ganzhiYear: lunar.ganzhiYear,
    zodiac: lunar.zodiac,
    term: todayPoint ? todayPoint.index : -1,
    nextTerm: nextPoint ? nextPoint.index : -1,
    nextTermDate: nextPoint ? nextPoint.date : '',
    nextTermIn,
    festivals: festivalsOf(
      lunar.month, lunar.day, lunar.leap,
      `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`,
      nextLunar.month === 1 && nextLunar.day === 1 && !nextLunar.leap,
    ),
  }
}
