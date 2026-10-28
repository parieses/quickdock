// useNavBadges.ts — 侧边栏导航项的通用角标机制
//
// 在此之前只有「插件可更新」一个角标，逻辑写死在 Sidebar 的 plugins 分支里。
// 想给第二个、第三个数据源加角标就得复制粘贴，且每条源的已读语义各不相同 ——
// 所以抽成一个小型注册表：
//
//   source(id) → raw 数量 → 减去「已读水位」→ 显示值
//
// 两条关键约定：
// 1. **「已读」而不是「已消除」**：水位做出之差。打开监控页看过 3 个宕机 → 角标消失，
//    但宕机还在；再来第 4 个时角标是 1 而不是 4 —— 只提示新增部分。
//    相反 sticky 源（今日到期）是「持续存在的事实」，打开页面也不该假装它不存在。
// 2. **角标不能比业务先崩**：fetch 抛错（DB 未就绪、网络未起）一律吞掉按 0 处理。
//    异步 numerate 一个红色小点，不值得让用户看到错误 toast。
//
// 自动清已读：Sidebar 监听 currentPage 变化，进入哪一页就 markSeen 哪一页。

import { reactive, computed, watch } from 'vue'
import { unwrap } from '../utils/api'
import { GetNavBadges } from '../../bindings/quickdock/services/appservice'
import { pluginUpdateBadge } from './usePluginUpdateBadge'

export type BadgeTone = 'danger' | 'warning' | 'info'

export interface NavBadge {
  count: number
  tone: BadgeTone
  titleKey: string
}

// 角标元数据：tone / 文案 / sticky。raw 计数由下方轮询统一写入。
interface BadgeMeta {
  tone: BadgeTone
  titleKey: string
  sticky?: boolean
}

const META: Record<string, BadgeMeta> = {
  monitor: { tone: 'danger', titleKey: 'badgeMonitorDown' },
  todo: { tone: 'warning', titleKey: 'badgeTodoDue', sticky: true },
  settings: { tone: 'danger', titleKey: 'badgeNewCrash' },
  plugins: { tone: 'danger', titleKey: 'pluginUpdateBadge' },
}

// 后端聚合返回的角标计数（一次性拿 monitor/todo/crash，替代三路独立 IPC）
interface NavBadgesResp {
  monitorDown: number
  todoDue: number
  crashFiles: number
}

// 今日 0 点的比较基准只用日期字符串（YYYY-MM-DD 字典序 == 时间序），不引 date 库
function todayStr(): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

const SEEN_KEY = 'qd_badge_seen'

function readSeen(): Record<string, number> {
  try {
    const raw = localStorage.getItem(SEEN_KEY)
    if (!raw) return {}
    const obj = JSON.parse(raw)
    if (!obj || typeof obj !== 'object') return {}
    const out: Record<string, number> = {}
    for (const [k, v] of Object.entries(obj)) {
      if (typeof v === 'number' && Number.isFinite(v)) out[k] = v
    }
    return out
  } catch {
    return {}
  }
}

// 写入合并：轮询下调水位、markSeen 都会触发写，用 150ms 防抖合并高频写，避免每次 tick 都 setItem。
// pendingSeenJson 存的必须是**最后一次**调用传入的值：防抖窗口内的多次调用只保留最新一份，
// 否则窗口结束时落盘的是第一次的旧水位，下次启动角标又会全部冒出来。
let pendingSeenJson: string | null = null
let seenWriteTimer: ReturnType<typeof setTimeout> | null = null
function scheduleWriteSeen(map: Record<string, number>) {
  pendingSeenJson = JSON.stringify(map)
  if (seenWriteTimer) return
  seenWriteTimer = setTimeout(() => {
    seenWriteTimer = null
    if (pendingSeenJson === null) return
    try {
      localStorage.setItem(SEEN_KEY, pendingSeenJson)
    } catch {
      /* 隐私模式下写不了，本次会话内仍有效 */
    }
    pendingSeenJson = null
  }, 150)
}

function writeSeen(map: Record<string, number>) {
  scheduleWriteSeen(map)
}

const raw = reactive<Record<string, number>>({})
const seen = reactive<Record<string, number>>(readSeen())

/** 某个 source 当前应显示的角标；无需显示返回 null。 */
function badgeOf(id: string): NavBadge | null {
  const m = META[id]
  if (!m) return null
  const r = raw[id] ?? 0
  const n = m.sticky ? r : Math.max(0, r - (seen[id] ?? 0))
  if (n <= 0) return null
  return { count: n, tone: m.tone, titleKey: m.titleKey }
}

/** 「设置」按钮上的汇总角标：直接复用 badgeOf('settings')，避免和上面那套「已读水位」逻辑重复 */
const settingsBadge = computed<NavBadge | null>(() => badgeOf('settings'))

/** 进入某页 → 记为已读（sticky 源不受影响）。单独导出：设置弹窗打开时也要清崩溃角标 */
export function markSeen(id: string) {
  const m = META[id]
  if (!m || m.sticky) return
  seen[id] = raw[id] ?? 0
  writeSeen({ ...seen })
}

function pullWaterLevel(id: string) {
  // 数据源下降时把水位一起拉下来。否则「清空了 3 条 → seen 仍记 3」，
  // 之后新来 1 条会被算成 (1-3) = 0，新事件就永远不提示了。
  if ((seen[id] ?? 0) > (raw[id] ?? 0)) {
    seen[id] = raw[id]
    writeSeen({ ...seen })
  }
}

async function refreshAggregated() {
  try {
    const r = unwrap<NavBadgesResp>(await GetNavBadges())
    if (r) {
      raw['monitor'] = Math.max(0, r.monitorDown)
      raw['todo'] = Math.max(0, r.todoDue)
      raw['settings'] = Math.max(0, r.crashFiles)
      // 插件可更新是前端派生值（市场索引拉取后写入），不走后端聚合
      raw['plugins'] = Math.max(0, pluginUpdateBadge.count)
      for (const id of ['monitor', 'todo', 'settings', 'plugins']) pullWaterLevel(id)
      return
    }
  } catch {
    // 角标取不到就当没有：它不是业务闭环的一环，静默降级比报错合适
  }
  for (const id of ['monitor', 'todo', 'settings']) raw[id] = 0
}

/** 立即刷新（业务操作后调用，例如改完待办希望角标马上跟上） */
function refreshNow() {
  void refreshAggregated()
}

/** 全量刷新（首次挂载用） */
function refreshAll() {
  void refreshAggregated()
}

// ---- 轮询：统一 60s tick（取原最快相关源 crash=60s 的节奏），每次只打 1 次后端聚合 ----
const TICK_SEC = 60
let timer: ReturnType<typeof setInterval> | null = null
let running = false

function tick() {
  if (running) return // 慢请求叠加保护：上一轮没跑完就不发新的
  running = true
  void refreshAggregated().finally(() => { running = false })
}

function startPolling() {
  if (timer) return
  void refreshAggregated()
  timer = setInterval(tick, TICK_SEC * 1000)
}

/**
 * 在组件里挂载：启动轮询 + 跟随 currentPage 自动打已读。
 * 只应由 Sidebar 调用一次。角标轮询是全局常驻的：宿主（侧栏）几乎不卸载，
 * 且即便卸载也不该让角标冻结，所以不随组件卸载而停——它跟随应用生命周期，
 * startPolling 自带幂等保护（timer 已存在则跳过）。
 */
export function useNavBadges(currentPage?: () => string) {
  if (currentPage) {
    watch(currentPage, (p) => { if (p) markSeen(p) }, { immediate: true })
  }
  startPolling()
  return { badgeOf, settingsBadge, markSeen, refreshNow, refreshAll }
}

/** 不启动轮询、也不注册生命周期的只读取用（给独立小组件渲染用） */
export function navBadges() {
  return { badgeOf, settingsBadge }
}
