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

import { reactive, computed, ref, watch } from 'vue'
import { unwrap } from '../utils/api'
import { ListMonitors } from '../../bindings/quickdock/services/monitor/monitorservice'
import { ListTodos } from '../../bindings/quickdock/services/todo/todoservice'
import { ListCrashFiles } from '../../bindings/quickdock/services/diag/diagservice'
import { pluginUpdateBadge } from './usePluginUpdateBadge'
import type { Monitor, Todo } from '../../bindings/quickdock/internal/db/models'

export type BadgeTone = 'danger' | 'warning' | 'info'

export interface NavBadge {
  count: number
  tone: BadgeTone
  titleKey: string
}

interface NavBadgeSource {
  id: string
  tone: BadgeTone
  /** 轮询间隔：崩溃要第一时间看到，待办到期一天之内变不了几次 */
  intervalSec: number
  /** sticky = 不在乎「看过没有」，数量本身就是要持续提醒的事实 */
  sticky?: boolean
  fetch: () => Promise<number>
  titleKey: string
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

function writeSeen(map: Record<string, number>) {
  try {
    localStorage.setItem(SEEN_KEY, JSON.stringify(map))
  } catch {
    /* 隐私模式下写不了，本次会话内仍有效 */
  }
}

// ---- 数据源注册表：新增一个角标就在这里加一条 ----
const sources: NavBadgeSource[] = [
  {
    id: 'monitor',
    tone: 'danger',
    intervalSec: 90,
    titleKey: 'badgeMonitorDown',
    async fetch() {
      const list = unwrap<Monitor[]>(await ListMonitors()) || []
      // 只数「启用中且最近一次检查失败」的；停用的监控停 while 不该打扰用户
      return list.filter((m) => m.enabled && m.lastStatus === 'down').length
    },
  },
  {
    id: 'todo',
    tone: 'warning',
    intervalSec: 300,
    sticky: true,
    titleKey: 'badgeTodoDue',
    async fetch() {
      const list = unwrap<Todo[]>(await ListTodos()) || []
      const today = todayStr()
      return list.filter((x) => !x.done && x.dueDate && x.dueDate <= today).length
    },
  },
  {
    id: 'settings',
    tone: 'danger',
    intervalSec: 60,
    titleKey: 'badgeNewCrash',
    async fetch() {
      const files = unwrap<string[]>(await ListCrashFiles()) || []
      return files.length
    },
  },
  {
    id: 'plugins',
    tone: 'danger',
    intervalSec: 300,
    titleKey: 'pluginUpdateBadge',
    // 市场索引拉取后写入 pluginUpdateBadge，这里只读它，不重复发请求
    async fetch() {
      return pluginUpdateBadge.count
    },
  },
]

const raw = reactive<Record<string, number>>({})
const seen = reactive<Record<string, number>>(readSeen())

/** 某个 source 当前应显示的角标；无需显示返回 null。
 * 用普通函数而非 `computed(() => (id) => ...)`：后者只在「返回一个函数」上 memo，
 * 真正的响应式读取发生在调用处的 render effect 里，computed 几乎没收益还误导读者。 */
function badgeOf(id: string): NavBadge | null {
  const s = sources.find((x) => x.id === id)
  if (!s) return null
  const r = raw[id] ?? 0
  const n = s.sticky ? r : Math.max(0, r - (seen[id] ?? 0))
  if (n <= 0) return null
  return { count: n, tone: s.tone, titleKey: s.titleKey }
}

/** 「设置」按钮上的汇总角标：直接复用 badgeOf('settings')，避免和上面那套「已读水位」逻辑重复 */
const settingsBadge = computed<NavBadge | null>(() => badgeOf('settings'))

/** 进入某页 → 记为已读（sticky 源不受影响）。单独导出：设置弹窗打开时也要清崩溃角标 */
export function markSeen(id: string) {
  const s = sources.find((x) => x.id === id)
  if (!s || s.sticky) return
  seen[id] = raw[id] ?? 0
  writeSeen({ ...seen })
}

async function refreshSource(s: NavBadgeSource) {
  try {
    raw[s.id] = Math.max(0, await s.fetch())
  } catch {
    // 角标取不到就当没有：它不是业务闭环的一环，静默降级比报错合适
    raw[s.id] = 0
  }
  // 数据源下降时把水位一起拉下来。否则「清空了 3 条 → seen 仍记 3」，
  // 之后新来 1 条会被算成 (1-3) = 0，新事件就永远不提示了。
  if (!s.sticky && (seen[s.id] ?? 0) > raw[s.id]) {
    seen[s.id] = raw[s.id]
    writeSeen({ ...seen })
  }
}

/** 立即刷新单个源（业务操作后调用，例如改完待办希望角标马上跟上） */
function refreshNow(id: string) {
  const s = sources.find((x) => x.id === id)
  if (s) void refreshSource(s)
}

function refreshAll() {
  void Promise.all(sources.map(refreshSource))
}

// ---- 轮询：统一 15s tick，每个源按自己的 intervalSec 到期才真正请求 ----
const TICK_SEC = 15
const lastRun = ref<Record<string, number>>({})
let timer: ReturnType<typeof setInterval> | null = null
let running = false

function tick() {
  if (running) return // 慢请求叠加保护：上一轮没跑完就不发新的
  running = true
  const now = Date.now()
  void Promise.all(
    sources
      .filter((s) => now - (lastRun.value[s.id] ?? 0) >= s.intervalSec * 1000)
      .map(async (s) => {
        lastRun.value[s.id] = now
        await refreshSource(s)
      }),
  ).finally(() => { running = false })
}

function startPolling() {
  if (timer) return
  void Promise.all(sources.map(async (s) => {
    lastRun.value[s.id] = Date.now()
    await refreshSource(s)
  }))
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
