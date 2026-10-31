// useNavBadges.ts — 侧边栏导航项的通用角标机制
//
// 在此之前只有「插件可更新」一个角标，逻辑写死在 Sidebar 的 plugins 分支里。
// 想给第二个、第三个数据源加角标就得复制粘贴，且每条源的已读语义各不相同 ——
// 所以抽成一个小型注册表：
//
//   SOURCES(id) → raw 数量 → 减去「已读水位」→ 显示值
//
// 两条关键约定：
// 1. 「已读」而不是「已消除」：水位做出之差。打开监控页看过 3 个宕机 → 角标消失，
//    但宕机还在；再来第 4 个时角标是 1 而不是 4 —— 只提示新增部分。
//    相反 sticky 源（今日到期）是「持续存在的事实」，打开页面也不该假装它不存在。
// 2. 角标不能比业务先崩：fetch 抛错（DB 未就绪、网络未起）一律吞掉按 0 处理。
//    异步 numerate 一个红色小点，不值得让用户看到错误 toast。
//
// 新增一个角标源：只在 SOURCES 加一条（后端驱动的再在 NavBadgesResp 加一个字段）。
// badgeOf / markSeen / refreshAggregated 全部按这张表遍历，不存在「改了三处漏一处」。
//
// 生命周期：轮询与监听跟随**使用者**（useNavBadges 的调用方）——引用计数归零时停止，
// 并摘掉 focus / visibilitychange 监听。见下方 acquirePolling / stopPolling。
//
// 自动清已读：Sidebar 监听 currentPage 变化，进入哪一页就 markSeen 哪一页。

import { reactive, computed, watch, getCurrentScope, onScopeDispose } from 'vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { writeStorageDebounced } from '../utils/storageQueue'
import { GetNavBadges } from '../../bindings/quickdock/services/appservice'
import { pluginUpdateBadge } from './usePluginUpdateBadge'

export type BadgeTone = 'danger' | 'warning' | 'info'

export interface NavBadge {
  count: number
  tone: BadgeTone
  titleKey: string
}

// 后端聚合返回的角标计数（一次性拿 monitor/todo/crash，替代三路独立 IPC）
interface NavBadgesResp {
  monitorDown: number
  todoDue: number
  crashFiles: number
}

// 一条角标源 = 展示元数据 + 计数取值方式。raw 计数由轮询统一写入 raw 表。
interface BadgeSource {
  tone: BadgeTone
  titleKey: string
  /** 持续存在的事实（如今日到期）：打开页面也不该假装它不存在，不参与已读水位 */
  sticky?: boolean
  /** 从后端聚合响应里取原始计数；省略则固定为 0 */
  read?: (r: NavBadgesResp) => number
  /** 计数由前端派生、不来自后端聚合：后端请求失败时不能把它清零 */
  client?: boolean
}

const SOURCES: Record<string, BadgeSource> = {
  monitor: { tone: 'danger', titleKey: 'badgeMonitorDown', read: (r) => r.monitorDown },
  todo: { tone: 'warning', titleKey: 'badgeTodoDue', sticky: true, read: (r) => r.todoDue },
  settings: { tone: 'danger', titleKey: 'badgeNewCrash', read: (r) => r.crashFiles },
  // 插件可更新是前端派生值（市场索引拉取后写入），不走后端聚合
  plugins: { tone: 'danger', titleKey: 'pluginUpdateBadge', client: true, read: () => pluginUpdateBadge.count },
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

// 写入合并交给 utils/storageQueue（同 key 只落最后一份值）：轮询下调水位、markSeen 都会
// 连续触发写，没必要每次 tick 都 setItem。存进去的必须是**完整快照**而不是增量 ——
// 合并只保留最后一次调用的值，传增量的话前几次的水位会被直接覆盖掉，
// 下次启动角标又会全部冒出来。
function writeSeen(map: Record<string, number>) {
  writeStorageDebounced(SEEN_KEY, JSON.stringify(map))
}

const raw = reactive<Record<string, number>>({})
const seen = reactive<Record<string, number>>(readSeen())

/** 某个 source 当前应显示的角标；无需显示返回 null。 */
function badgeOf(id: string): NavBadge | null {
  const s = SOURCES[id]
  if (!s) return null
  const r = raw[id] ?? 0
  const n = s.sticky ? r : Math.max(0, r - (seen[id] ?? 0))
  if (n <= 0) return null
  return { count: n, tone: s.tone, titleKey: s.titleKey }
}

/** 「设置」按钮上的汇总角标：直接复用 badgeOf('settings')，避免和上面那套「已读水位」逻辑重复 */
const settingsBadge = computed<NavBadge | null>(() => badgeOf('settings'))

/** 进入某页 → 记为已读（sticky 源不受影响）。 */
function markSeen(id: string) {
  const s = SOURCES[id]
  if (!s || s.sticky) return
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
      for (const [id, s] of Object.entries(SOURCES)) {
        raw[id] = Math.max(0, s.read?.(r) ?? 0)
        pullWaterLevel(id)
      }
      return
    }
  } catch {
    // 角标取不到就当没有：它不是业务闭环的一环，静默降级比报错合适
  }
  // 后端没回：只清后端驱动的源。前端派生源（plugins）与这次请求无关，
  // 清零会让「有新版本可更新」凭空消失。
  for (const [id, s] of Object.entries(SOURCES)) {
    if (!s.client) raw[id] = 0
  }
}

/** 立即刷新（业务操作后调用，例如改完待办希望角标马上跟上） */
function refreshNow() {
  void refreshAggregated()
}

// ---- 轮询：统一 60s tick（取原最快相关源 crash=60s 的节奏），每次只打 1 次后端聚合 ----
const TICK_SEC = 60
// 单次调用迟迟不返回时的兜底时长：见 tick() 里的说明
const CALL_STUCK_MS = 15_000
let timer: ReturnType<typeof setInterval> | null = null
let running = false
// 状态翻转事件订阅的退订句柄：随轮询一起挂载/摘除，避免重复订阅。
let monitorOff: (() => void) | null = null
// 使用者计数：只有最后一个 useNavBadges 调用方销毁时才真正停轮询。
// 不这么做的话，HMR 或未来出现第二个宿主时会互相踩停。
let clients = 0

function tick() {
  if (running) return // 慢请求叠加保护：上一轮没跑完就不发新的
  running = true
  // 兜底：一次卡住不回的应用调用不能把轮询永久锁死。running 一旦永久停在 true，
  // 之后每次 tick 都会被这行挡回去，角标就永远停在旧数字上——表现为「待办删了，
  // 角标还在」，而且再也不会自己恢复。所以到点直接放开，让下一轮重新发起。
  const guard = setTimeout(() => { running = false }, CALL_STUCK_MS)
  void refreshAggregated().finally(() => {
    clearTimeout(guard)
    running = false
  })
}

// 窗口从后台回到前台：立即补一次。
// WebView2 在窗口隐藏/最小化时会把定时器节流到每分钟甚至挂起，用户切回来看到的
// 仍是离开时的旧角标；仅靠 60s tick 会有一段"明明已经删了还在显示"的空窗。
function onForeground() {
  tick()
}
function onVisibility() {
  if (!document.hidden) tick()
}

function startPolling() {
  if (timer) return
  void refreshAggregated()
  timer = setInterval(tick, TICK_SEC * 1000)
  window.addEventListener('focus', onForeground)
  document.addEventListener('visibilitychange', onVisibility)
  // 监控宕机/恢复实时刷新角标：checker 检测到状态翻转会广播 monitor:changed，
  // 收到即重拉 GetNavBadges，角标不再等 60s tick。与 timer 同生命期、只挂一份。
  if (!monitorOff) {
    monitorOff = Events.On('monitor:changed', () => { void refreshAggregated() })
  }
}

/**
 * 停止轮询并摘掉监听。
 * 不动 `running`：在途请求的 finally 与兜底 guard（15s）都会把它放回 false，
 * 这里强改反而可能与在途请求的收尾打架。
 */
function stopPolling() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  window.removeEventListener('focus', onForeground)
  document.removeEventListener('visibilitychange', onVisibility)
  if (monitorOff) {
    monitorOff()
    monitorOff = null
  }
}

/** 取用轮询：计引用，调用方的 effect scope 销毁时归还 */
function acquirePolling() {
  clients++
  startPolling()
  // 不在组件 setup 里调用时（理论上只有 Sidebar）不注册生命周期，避免 dev 告警
  if (!getCurrentScope()) return
  onScopeDispose(() => {
    clients--
    if (clients <= 0) stopPolling()
  })
}

/**
 * 不启动轮询、也不注册生命周期的访问点：给独立小组件用
 * （NavBadge 渲染角标、设置弹窗标记已读、业务操作后手动刷新）。
 * API 与 useNavBadges 一致，只是不会拉起 60s tick —— 所以可以随便调。
 */
export function navBadges() {
  return { badgeOf, settingsBadge, markSeen, refreshNow }
}

/**
 * 在组件里挂载：启动轮询 + 跟随 currentPage 自动打已读。
 * 只应由 Sidebar 调用（它是角标的唯一宿主）；轮询跟随调用方生命周期，
 * 最后一个调用方销毁时自动停止并摘除 focus / visibilitychange 监听。
 */
export function useNavBadges(currentPage?: () => string) {
  if (currentPage) {
    watch(currentPage, (p) => { if (p) markSeen(p) }, { immediate: true })
  }
  acquirePolling()
  return navBadges()
}
