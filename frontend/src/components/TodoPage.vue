<script setup lang="ts">
import PageHeader from './PageHeader.vue'
import QdModal from './QdModal.vue'
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, onActivated, onDeactivated, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { KEEP_ALIVE } from '../pages/keepAlive'
defineOptions({ name: KEEP_ALIVE.todo })
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { writeStored, removeStored } from '../utils/storage'
import ConfirmDialog from './ConfirmDialog.vue'
import EntityLinkPicker from './EntityLinkPicker.vue'
import QdSearchBox from './QdSearchBox.vue'
import FocusStatsPanel from './FocusStatsPanel.vue'
import {
  CreateTodo, ListTodos, UpdateTodo, ToggleTodo, DeleteTodo, ClearCompletedTodos,
  CreateSubtask, SetTodoStatus, SetTodoSort,
} from '../../bindings/quickdock/services/todo/todoservice'
import {
  SendTestNotification, NotifyFocusComplete, NotifyFocusStart, SaveFocusSession,
} from '../../bindings/quickdock/services/appservice'
import { pendingOpenTodoId, pendingCreateTodo, pendingTarget } from '../composables/bridge'
// 待办的增删改都会汇流到 refresh()，在那里顺手把侧边栏角标拉齐（不用等 60s 轮询）
import { navBadges } from '../composables/useNavBadges'
import { matchesTextOrPinyin } from '../utils/pinyin'
import { invalidateLinkTargets } from '../constants/linkEvents'
import {
  Plus, Check, Trash2, Pencil, ChevronLeft, ChevronRight, CalendarDays, Bell, Clock, BellRing,
  RefreshCw, ListTree, Timer, X, ChevronUp, ChevronDown, History, BarChart3, MoreHorizontal,
} from '@lucide/vue'

import { reportError } from '../utils/reportError'
const { t } = useI18n()

interface Todo {
  id: string
  title: string
  done: boolean
  priority: string // none | low | medium | high
  dueDate: string  // '' 或 YYYY-MM-DD
  note: string
  startTime: string  // '' 或 YYYY-MM-DD HH:MM:SS
  endTime: string    // '' 或 YYYY-MM-DD HH:MM:SS
  reminderTime: string // '' 或 YYYY-MM-DD HH:MM:SS
  reminderSent: boolean
  tags: string      // 标签 JSON 数组字符串，如 '["工作","紧急"]'
  recurrence: string // 重复配置 JSON：{"kind":"daily|weekly|monthly","timeOfDay":"09:00","weekdays":"1,2,3"}
  parentId: string   // 子任务所属父待办 ID；空=顶层
  status: string     // todo | doing | done（权威字段）
  sort: number
  createdAt: string
  completedAt: string
}

// 4.4 重复配置结构（前端编辑态）
interface RecurrenceForm {
  kind: string      // none | daily | weekly | monthly
  timeOfDay: string // HH:MM
  weekdays: string  // 逗号分隔 0-6（0=周日）
}

const todos = ref<Todo[]>([])
const loading = ref(false)
const error = ref('')

// ---- 番茄钟 / 专注计时 ----
//
// 计时用截止时间戳而不是每秒递减：
//   - 窗口失焦 / 系统休眠 / 后台节流会让 setInterval 的“一秒”被拉长，递减式计时
//     越走越慢，用户回来一看还剩 18 分钟，而实际已经过了 25 分钟；
//   - 切到别的页面时本组件会被卸载（不在 KeepAlive 白名单），状态必须落到
//     localStorage 才能在回来时接上，否则"专注中切走就丢了"。
const FOCUS_DEFAULTS = [25, 45, 60]
const FOCUS_KEY = 'qd_focus_session'
const focusMinutes = ref(25)          // 本次专注时长（分钟）
const showFocusDur = ref(false)       // 专注时长浮层（把 25/45/60/自定义 收进一个下拉）
const focusTodoId = ref('')           // 正在专注的待办 id（''=无）
const focusRemaining = ref(0)         // 剩余秒数
const focusRunning = ref(false)       // 是否在倒计时
const focusTotalSec = ref(0)          // 本轮目标秒数：区分「到点完成」与「中途放弃」
let focusTimer: number | null = null  // setInterval id（只负责刷新显示，不负责计时）
let focusEndAt = 0                    // 运行中的截止时间戳（ms）；0 = 未运行
let focusStartedAt = 0                // 本轮开始时间戳（ms）：落库 startedAt，暂停不重置

// 专注历史：此前 completeFocus 只发通知、不留痕，「今天专注了几次」无从回顾。
// 落在 localStorage：本页不在 KeepAlive 白名单、切页即卸载，内存里的记录会丢。
const FOCUS_HISTORY_KEY = 'qd_focus_history'
const MAX_FOCUS_HISTORY = 100
interface FocusRecord { ts: number; minutes: number; done: boolean; title: string }
function loadFocusHistory(): FocusRecord[] {
  try {
    const a = JSON.parse(localStorage.getItem(FOCUS_HISTORY_KEY) || '[]')
    if (!Array.isArray(a)) return []
    return a
      .filter((x: any) => x && typeof x.ts === 'number' && typeof x.minutes === 'number')
      .slice(0, MAX_FOCUS_HISTORY)
  } catch { return [] }
}
const focusHistory = ref<FocusRecord[]>(loadFocusHistory())
const showFocusHist = ref(false)
// 统计面板入口：与历史浮层互斥。refreshToken 每完成一次专注 +1，
// 让已打开的面板重新拉数（面板内部 watch 它）。
const showFocusStats = ref(false)
// 面板右上角「更多」：清除已完成 / 发送测试提醒这两个低频动作从底栏收进来，
// 让列表底部保持干净（底栏那行提示文案在深色下也读不清）。
const showMore = ref(false)
const focusStatsRefresh = ref(0)
function toggleFocusHist() {
  showFocusHist.value = !showFocusHist.value
  if (showFocusHist.value) showFocusStats.value = false
}
function toggleFocusStats() {
  showFocusStats.value = !showFocusStats.value
  if (showFocusStats.value) showFocusHist.value = false
}

// 命令面板的「专注统计」动作经 pendingTarget 深链过来（面板是独立窗口，不共享 Pinia）。
// 与 pendingOpenTodoId 同一口径：消费即清空，避免下次进本页又自动弹一次。
watch(pendingTarget, (t) => {
  if (!t || t.page !== 'todo' || t.target !== 'focus-stats') return
  pendingTarget.value = null
  showFocusStats.value = true
  showFocusHist.value = false
})

// SQLite 对 ±HH:MM 时区后缀的支持早于 'Z'，后端 now() 也是这个格式；
// 不用 toISOString()（Z 结尾在老版本 SQLite 可能解析成 NULL，julianday 比较会失效）。
function toRFC3339(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  const off = -d.getTimezoneOffset()
  const sign = off >= 0 ? '+' : '-'
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}${sign}${p(Math.floor(Math.abs(off) / 60))}:${p(Math.abs(off) % 60)}`
}

/**
 * 一次专注结束的记录：
 *  1) localStorage 保底：切页恢复、历史浮层即时读数都靠它，保留不删；
 *  2) 异步落库：只有 done=true 的完成会话才写 focus_sessions。中途放弃不落库、
 *     也不写 localStorage 历史（见 endFocus），前后端口径一致：后端统计只认 done=1。
 */
async function recordFocus(minutes: number, done: boolean, title: string,
  todoId: string, startedAt: number, endedAt: number) {
  const m = Math.round(minutes)
  if (!m) return
  focusHistory.value = [{ ts: endedAt, minutes: m, done, title }, ...focusHistory.value].slice(0, MAX_FOCUS_HISTORY)
  writeStored(FOCUS_HISTORY_KEY, JSON.stringify(focusHistory.value))
  if (!done) return
  try {
    unwrap(await SaveFocusSession(todoId, title, toRFC3339(startedAt), toRFC3339(endedAt), m, true))
    focusStatsRefresh.value++
  } catch (e) {
    reportError(e, 'TodoPage', { fallback: t('focusSyncFailed') })
  }
}
// 今日统计只算真正走完的会话（done=true），中途放弃不计入「今天专注 N 次」
const todayFocusStats = computed(() => {
  const t0 = new Date(); t0.setHours(0, 0, 0, 0)
  const start = t0.getTime()
  let count = 0, minutes = 0
  for (const r of focusHistory.value) {
    if (r.ts >= start && r.done) { count++; minutes += r.minutes }
  }
  return { count, minutes }
})
function fmtHistTime(ts: number): string {
  const d = new Date(ts)
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  const dayKey = (x: Date) => `${x.getFullYear()}-${x.getMonth()}-${x.getDate()}`
  const now = new Date()
  if (dayKey(d) === dayKey(now)) return hm
  const y = new Date(); y.setDate(y.getDate() - 1)
  if (dayKey(d) === dayKey(y)) return t('todoFocusYesterday') + ' ' + hm
  return `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${hm}`
}
// 自定义时长：输入框只在「非预设值」时显示数字，否则留空并显示占位符
const isFocusPreset = computed(() => FOCUS_DEFAULTS.includes(focusMinutes.value))
function applyFocusCustom(e: Event) {
  const el = e.target as HTMLInputElement
  const v = Math.floor(Number(el.value))
  if (!Number.isFinite(v) || v < 1) { el.value = ''; return }
  focusMinutes.value = Math.min(180, v)
  el.value = String(focusMinutes.value)
}
function pickFocusMinutes(m: number) {
  focusMinutes.value = m
  showFocusDur.value = false
}

function focusTitle(id: string): string {
  return todos.value.find(t => t.id === id)?.title || ''
}

function saveFocus() {
  if (!focusTodoId.value) {
    removeStored(FOCUS_KEY)
    return
  }
  writeStored(FOCUS_KEY, JSON.stringify({
    todoId: focusTodoId.value,
    endAt: focusEndAt,
    remaining: focusRemaining.value,
    running: focusRunning.value,
    total: focusTotalSec.value,
    minutes: focusMinutes.value,
    startedAt: focusStartedAt,
  }))
}

function tickFocus() {
  if (!focusRunning.value || !focusEndAt) return
  const left = Math.ceil((focusEndAt - Date.now()) / 1000)
  focusRemaining.value = Math.max(0, left)
  if (left <= 0) completeFocus()
}

function startTicker() {
  stopFocusTimer()
  focusTimer = window.setInterval(tickFocus, 500)
}

function completeFocus() {
  const todoId = focusTodoId.value
  const title = focusTitle(todoId) || t('focusTimerTitle')
  const mins = Math.round(focusTotalSec.value / 60) || 1
  const endedAt = Date.now()
  const startedAt = focusStartedAt || endedAt
  try { NotifyFocusComplete(title, mins) } catch (e) { logErr('TodoPage', e) }
  void recordFocus(mins, true, title, todoId, startedAt, endedAt)
  stopFocusTimer()
  focusEndAt = 0
  focusStartedAt = 0
  focusTodoId.value = ''
  focusRunning.value = false
  focusRemaining.value = 0
  focusTotalSec.value = 0
  saveFocus()
}

function resumeFocus() {
  // 兜底：从旧存档恢复 / 异常路径没设过开始时刻时补上，保证落库 startedAt 不为空。
  // 正常新一轮由 startFocus 设定；暂停→继续不重置，本轮起点始终是第一次开始。
  if (!focusStartedAt) focusStartedAt = Date.now()
  focusEndAt = Date.now() + focusRemaining.value * 1000
  focusRunning.value = true
  startTicker()
  saveFocus()
}

function startFocus(todo: Todo) {
  if (todo.done) return
  // 若在专注别条，先清掉旧的
  stopFocusTimer()
  focusTodoId.value = todo.id
  focusTotalSec.value = Math.max(1, Math.floor(focusMinutes.value)) * 60
  focusRemaining.value = focusTotalSec.value
  focusStartedAt = Date.now()   // 本轮开始时刻（落库 startedAt）
  resumeFocus()
  try { NotifyFocusStart(focusTitle(todo.id), focusMinutes.value) } catch (e) { logErr('TodoPage', e) }
}
function stopFocusTimer() {
  if (focusTimer !== null) { clearInterval(focusTimer); focusTimer = null }
}
function endFocus() {
  // 只有真的走到 0 才算「专注完成」。过去这里一律调 NotifyFocusComplete，
  // 点了两分钟就放弃也被记成"专注 25 分钟完成"，统计与通知全是假的。
  const total = focusTotalSec.value
  const left = Math.max(0, focusRemaining.value)
  const finished = total > 0 && left <= 0
  const todoId = focusTodoId.value
  const title = focusTitle(todoId)
  const endedAt = Date.now()
  const startedAt = focusStartedAt || endedAt
  stopFocusTimer()
  focusEndAt = 0
  if (finished && todoId) {
    try { NotifyFocusComplete(title || t('focusTimerTitle'), Math.round(total / 60) || 1) } catch (e) { logErr('TodoPage', e) }
    // 只有走完的完成会话才记录（同时写 localStorage 保底 + 异步落库）。
    // 中途放弃不写任何记录：后端统计只认 done=1，前后端口径一致。
    void recordFocus(total / 60, true, title || t('focusTimerTitle'), todoId, startedAt, endedAt)
  }
  focusStartedAt = 0
  focusTodoId.value = ''
  focusRunning.value = false
  focusRemaining.value = 0
  focusTotalSec.value = 0
  saveFocus()
}
function pauseFocus() {
  if (focusRunning.value) {
    // 暂停不动 focusStartedAt：本轮起点是第一次开始，不是每次继续。
    // 先结算一次再停，避免暂停时漏掉刚刚过去的那一秒
    tickFocus()
    focusRunning.value = false
    focusEndAt = 0
    stopFocusTimer()
    saveFocus()
    return true
  }
  resumeFocus()
  return false
}

/** 回到本页时接上未结束的专注（含"离开期间已到点"的补结算）。 */
function restoreFocus() {
  let raw = ''
  try { raw = localStorage.getItem(FOCUS_KEY) || '' } catch { return }
  if (!raw) return
  try {
    const s = JSON.parse(raw) as {
      todoId?: string; endAt?: number; remaining?: number
      running?: boolean; total?: number; minutes?: number; startedAt?: number
    }
    if (!s?.todoId) return
    focusTodoId.value = s.todoId
    if (s.minutes) focusMinutes.value = s.minutes
    focusTotalSec.value = s.total || 0
    focusRemaining.value = s.remaining || 0
    // 旧存档没有 startedAt（新增字段）：按已消耗时长反推起点，保证补结算能落库。
    focusStartedAt = s.startedAt || (Date.now() - (focusTotalSec.value - focusRemaining.value) * 1000)
    if (s.running && s.endAt) {
      const left = Math.ceil((s.endAt - Date.now()) / 1000)
      if (left <= 0) {
        focusRemaining.value = 0
        completeFocus() // 离开期间到点：补发一次完成通知并清状态
        return
      }
      focusRemaining.value = left
      focusEndAt = s.endAt
      focusRunning.value = true
      startTicker()
    }
  } catch { /* 数据损坏就当没存过 */ }
}
function fmtFocus(sec: number): string {
  const m = Math.floor(sec / 60); const s = sec % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
}
onUnmounted(stopFocusTimer)


// ---- 日历状态 ----
const viewYear = ref(new Date().getFullYear())
const viewMonth = ref(new Date().getMonth()) // 0-based
const selectedDate = ref('')                  // '' = 未排期；否则 YYYY-MM-DD
const todayStr = computed(() => {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
})

/** Date → datetime-local 值（YYYY-MM-DDTHH:MM:SS，与输入框 step=1 对齐）。 */
function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

// 「今天」按钮只在点了真的会改变视图时才显示：已经选中今天、且正看着今天所在月份时，
// 点它毫无效果、只剩占位感。翻到别的月份仍保留，方便一键跳回。
const atToday = computed(() => {
  if (selectedDate.value !== todayStr.value) return false
  const d = new Date()
  return viewYear.value === d.getFullYear() && viewMonth.value === d.getMonth()
})

const WEEKDAY_NAMES = computed(() => ['日', '一', '二', '三', '四', '五', '六'].map(w => t('toDoWeekday_' + w)))

const monthLabel = computed(() => t('toDoMonthYear', { y: viewYear.value, m: viewMonth.value + 1 }))

// 时间显示：YYYY-MM-DD HH:MM:SS -> HH:MM（或带日期）
function shortTime(s: string): string {
  return s ? s.slice(11, 19) : ''
}
function hasTime(todo: Todo): boolean {
  return !!(todo.startTime || todo.endTime || todo.reminderTime)
}

// ---- 4.2 标签 ----
// 脏 JSON 属历史数据，解析失败退回空标签即可；弹错会把每次打开编辑框都变成报错。
function parseTags(jsonStr: string): string[] {
  if (!jsonStr) return []
  try {
    const arr = JSON.parse(jsonStr)
    if (Array.isArray(arr)) return arr.filter((x: unknown) => typeof x === 'string')
  } catch { /* ignore */ }
  return []
}
// 逗号/空格/分号分隔的文本 -> JSON 数组字符串
function tagsTextToJSON(text: string): string {
  const arr = text.split(/[,，;；\s]+/).map(s => s.trim()).filter(Boolean)
  return JSON.stringify(arr)
}
function tagsJSONToText(jsonStr: string): string {
  return parseTags(jsonStr).join(', ')
}

// ---- 4.4 重复 ----
const RECUR_KINDS = ['none', 'daily', 'weekly', 'monthly']
// 同上：重复规则的脏 JSON 退回默认值，静默降级。
function parseRecurrence(jsonStr: string): RecurrenceForm {
  const def: RecurrenceForm = { kind: 'none', timeOfDay: '09:00', weekdays: '' }
  if (!jsonStr) return def
  try {
    const o = JSON.parse(jsonStr)
    if (o && typeof o === 'object') {
      return {
        kind: o.kind || 'none',
        timeOfDay: o.timeOfDay || '09:00',
        weekdays: o.weekdays || '',
      }
    }
  } catch { /* ignore */ }
  return def
}
function recurrenceToJSON(f: RecurrenceForm): string {
  if (!f.kind || f.kind === 'none') return ''
  return JSON.stringify({ kind: f.kind, timeOfDay: f.timeOfDay || '09:00', weekdays: f.weekdays || '' })
}
const WEEKDAY_LABELS = ['日', '一', '二', '三', '四', '五', '六']
function recurrenceLabel(todo: Todo): string {
  const f = parseRecurrence(todo.recurrence)
  if (!f.kind || f.kind === 'none') return ''
  const time = f.timeOfDay || '09:00'
  if (f.kind === 'daily') return t('todoRecDaily', { time })
  if (f.kind === 'monthly') return t('todoRecMonthly', { time })
  if (f.kind === 'weekly') {
    const days = f.weekdays.split(',').map(s => s.trim()).filter(Boolean)
      .map(d => WEEKDAY_LABELS[Number(d)] || '').filter(Boolean)
    const wk = days.join('')
    return t('todoRecWeekly', { days: wk || '—', time })
  }
  return ''
}

// ---- 重复待办：occurrence 级编辑 / snooze 的日期工具 ----
// 项目里没有现成的 nextDue/addDays，这里只做这些功能需要的最小实现。
// 一律用本地日期构造器（new Date(y, m, d)）避免 'YYYY-MM-DD' 被当成 UTC 解析导致跨日。
function parseDateOnly(s: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s)
  if (!m) return null
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]))
  return Number.isNaN(d.getTime()) ? null : d
}
function formatDateOnly(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
/** 按重复规则把某个截止日推进到下一周期（仅取日期部分，不含时分秒）。 */
function nextDueDate(due: string, f: RecurrenceForm): string {
  const base = parseDateOnly(due) || new Date()
  base.setHours(0, 0, 0, 0)
  const next = new Date(base.getTime())
  if (f.kind === 'weekly') {
    const days = f.weekdays.split(',').map(s => Number(s.trim()))
      .filter(n => Number.isInteger(n) && n >= 0 && n <= 6)
    if (days.length) {
      let step = 1
      while (step <= 7 && !days.includes((base.getDay() + step) % 7)) step++
      next.setDate(base.getDate() + Math.min(step, 7))
    } else {
      next.setDate(base.getDate() + 7) // 没选星期就按整周推进
    }
  } else if (f.kind === 'monthly') {
    const day = base.getDate()
    next.setDate(1) // 先落到月初，规避 setMonth 的溢出（1/31 -> 3/3）
    next.setMonth(next.getMonth() + 1)
    const last = new Date(next.getFullYear(), next.getMonth() + 1, 0).getDate()
    next.setDate(Math.min(day, last)) // 月末钳到当月最后一天
  } else {
    next.setDate(base.getDate() + 1) // daily / none 兜底：往后一天
  }
  return formatDateOnly(next)
}

// ---- 4.2 标签筛选 ----
const activeTag = ref('')
const allTags = computed(() => {
  const set = new Set<string>()
  for (const x of todos.value) for (const tg of parseTags(x.tags as unknown as string)) set.add(tg)
  return Array.from(set).sort()
})

// ---- 4.3 状态 + 看板视图 ----
const viewMode = ref<'list' | 'kanban' | 'quadrant'>('list')
const KANBAN_STATUSES = ['todo', 'doing', 'done'] as const
const STATUS_ORDER: Record<string, string[]> = {
  todo: ['doing', 'done', 'todo'],
  doing: ['done', 'todo', 'doing'],
  done: ['todo', 'doing', 'done'],
}
function statusLabel(s: string | undefined): string {
  if (s === 'doing') return t('todoStatusDoing')
  if (s === 'done') return t('todoStatusDone')
  return t('todoStatusTodo')
}
function cycleStatus(todo: Todo) {
  const next = (STATUS_ORDER[todo.status] || STATUS_ORDER.todo)[0]
  setStatus(todo, next)
}
async function setStatus(todo: Todo, status: string) {
  try {
    await unwrap(await SetTodoStatus(todo.id, status))
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// ---- 4.1 子任务（单层级 checklist）----
// 索引：parentId → 子任务（已排序）；避免模板每行多次全量 filter
const subtasksByParent = computed(() => {
  const m = new Map<string, Todo[]>()
  for (const x of todos.value) {
    if (!x.parentId) continue
    if (!m.has(x.parentId)) m.set(x.parentId, [])
    m.get(x.parentId)!.push(x)
  }
  for (const arr of m.values()) arr.sort((a, b) => Number(a.done) - Number(b.done) || a.sort - b.sort)
  return m
})

function subtasksOf(parentId: string): Todo[] {
  return subtasksByParent.value.get(parentId) || []
}
function subProgress(parentId: string): { done: number; total: number; pct: number } {
  const subs = subtasksOf(parentId)
  const total = subs.length
  const done = subs.filter(s => s.done).length
  const pct = total === 0 ? 0 : Math.round((done / total) * 100)
  return { done, total, pct }
}
const addingSubFor = ref('')
const newSubTitle = ref('')
function toggleSubInput(todo: Todo) {
  addingSubFor.value = addingSubFor.value === todo.id ? '' : todo.id
  newSubTitle.value = ''
}
async function addSubtask(parent: Todo) {
  // 先同步取标题并清空：Enter 提交后紧接的 blur 不应该拿旧文本再建一条。
  const title = newSubTitle.value.trim()
  newSubTitle.value = ''
  if (!title) return
  try {
    await unwrap(await CreateSubtask(parent.id, title))
    await refresh()
  } catch (e) {
    // 提交失败把标题放回去，别让用户白打一遍（清空是为了防 Enter+blur 双提交）
    newSubTitle.value = title
    error.value = getErrorMessage(e)
  }
}
// 输入框失焦：有内容就当 Enter 提交，别让打了半天的标题悄悄消失。
function onSubInputBlur(parent: Todo) {
  if (newSubTitle.value.trim()) void addSubtask(parent)
  else toggleSubInput(parent)
}

// ---- 子任务：行内改名 / 上下移 ----
// 原来子任务只能「加」和「删」：改一个字要删了重建，顺序永远按创建先后。
// 改名复用 UpdateTodo（其它字段原样带回），排序走 SetTodoSort。
const renamingSubId = ref('')
const renamingSubTitle = ref('')
const subRenameInputRef = ref<HTMLInputElement[] | HTMLInputElement | null>(null)

function startRenameSub(st: Todo) {
  renamingSubId.value = st.id
  renamingSubTitle.value = st.title
  nextTick(() => {
    const el = Array.isArray(subRenameInputRef.value) ? subRenameInputRef.value[0] : subRenameInputRef.value
    el?.focus()
    el?.select()
  })
}

async function commitRenameSub(st: Todo) {
  // 先清标记再判等：Enter 与 blur 会连续触发两次，第二次拿到的是空 id 直接返回
  if (renamingSubId.value !== st.id) return
  renamingSubId.value = ''
  const title = renamingSubTitle.value.trim()
  if (!title || title === st.title) return
  try {
    await unwrap(await UpdateTodo(st.id, title, st.priority, st.dueDate, st.note,
      st.startTime, st.endTime, st.reminderTime, st.recurrence,
      st.tags as unknown as string, st.status))
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

/** 与相邻兄弟交换位置，然后把整组按 index*10 重写 sort（步长 10 留插入余地）。 */
async function moveSub(st: Todo, dir: -1 | 1) {
  const siblings = subtasksOf(st.parentId)
  const i = siblings.findIndex(x => x.id === st.id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= siblings.length) return
  const order = [...siblings]
  order.splice(j, 0, order.splice(i, 1)[0])
  try {
    for (let k = 0; k < order.length; k++) {
      if ((order[k].sort || 0) === k * 10) continue
      await unwrap(await SetTodoSort(order[k].id, k * 10))
    }
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// ---- 子任务：单独设截止日 ----
// 子任务原本只能跟着父待办的日期走：父没排期，子也永远"未排期"；想给某条子任务
// 单独定个 deadline 只能去改父任务（改了又影响其它子任务）。父/子各有独立 dueDate，
// 这里只改子任务这一条，不动父。
const subDueFor = ref('')
function toggleSubDue(st: Todo) { subDueFor.value = subDueFor.value === st.id ? '' : st.id }
async function commitSubDue(st: Todo, val: string) {
  subDueFor.value = ''
  if (val === st.dueDate) return
  try {
    await unwrap(await UpdateTodo(st.id, st.title, st.priority, val, st.note,
      st.startTime, st.endTime, st.reminderTime, st.recurrence,
      st.tags as unknown as string, st.status))
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) }
}

// 看板：顶层待办按状态分组。
// 数据源必须是 selectedTodos，不能是 todos —— 否则日期、搜索、标签这三个筛选
// 在列表视图生效、切到看板全部失效（选着"今天"，看板却给出全部待办）。
// 分组只是在这个已筛好的集合上再按 status 切一刀。
// 索引：status → 待办，模板每列多次调用只做 O(1) 查找
const topTodosByStatus = computed(() => {
  const m = new Map<string, Todo[]>()
  for (const x of selectedTodos.value) {
    if (!m.has(x.status)) m.set(x.status, [])
    m.get(x.status)!.push(x)
  }
  // 列内按用户手排的 sort：selectedTodos 已按「未完成 → 有时间 → sort」排过，
  // 那份顺序是给列表视图的，看板里同列应该体现拖拽后的相对位置。
  for (const arr of m.values()) arr.sort((a, b) => a.sort - b.sort)
  return m
})

function statusTodos(status: string): Todo[] {
  return topTodosByStatus.value.get(status) || []
}

// 四象限视图：用「优先级 + 截止日期」近似「重要 + 紧急」。
// 重要 = priority high；紧急 = 有 dueDate 且已到期（今天或逾期）。
// 数据源与看板一致取 selectedTodos，保证日期/搜索/标签筛选在三套视图间一致。
const QUADRANTS = ['Q1', 'Q2', 'Q3', 'Q4'] as const
function quadrantOf(todo: Todo): typeof QUADRANTS[number] {
  const important = todo.priority === 'high'
  const urgent = todo.dueDate !== '' && todo.dueDate <= todayStr.value
  if (important && urgent) return 'Q1'
  if (important) return 'Q2'
  if (urgent) return 'Q3'
  return 'Q4'
}
const quadrantTodos = computed(() => {
  const m = new Map<string, Todo[]>()
  for (const q of QUADRANTS) m.set(q, [])
  for (const x of selectedTodos.value) m.get(quadrantOf(x))!.push(x)
  return m
})
function todosInQuadrant(q: string): Todo[] {
  return quadrantTodos.value.get(q) || []
}
function quadrantLabel(q: string): string {
  return t('todoQuadrant' + q)
}

const dragId = ref('')
const dragOverStatus = ref('')

function onDragStart(id: string, e?: DragEvent) {
  dragId.value = id
  dragOverStatus.value = ''
  // Chromium 不设 dataTransfer 也能拖，但 WebKit/Firefox 必须 setData 才真正启动；
  // effectAllowed 决定光标是「移动」而不是「复制」。
  try {
    if (e?.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move'
      e.dataTransfer.setData('text/plain', id)
    }
  } catch { /* 只读 dataTransfer 时忽略 */ }
}
function onDragEnd() {
  dragId.value = ''
  dragOverStatus.value = ''
  calDropDate.value = ''
}

/** 拖进某一列：先换状态，再落到该列末尾。 */
async function moveToStatus(status: string) {
  const id = dragId.value
  dragId.value = ''
  dragOverStatus.value = ''
  if (!id) return
  const todo = todos.value.find(x => x.id === id)
  if (!todo) return
  // 同列内落位也要重排（换到末尾），否则拖了半天顺序纹丝不动
  const siblings = statusTodos(status).filter(x => x.id !== id)
  const nextSort = siblings.length ? Math.max(...siblings.map(x => x.sort || 0)) + 10 : 0
  try {
    if (todo.status !== status) await unwrap(await SetTodoStatus(id, status))
    // sort 单独走 SetTodoSort：SetTodoStatus 不管顺序，混在一起会让人
    // 以为「只改状态」也动了排序。
    if ((todo.sort || 0) !== nextSort) await unwrap(await SetTodoSort(id, nextSort))
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// ---- 拖拽改日期：从列表/看板把待办拖到日历格子上 ----
const calDropDate = ref('')
function onCalDragLeave(ds: string) {
  if (calDropDate.value === ds) calDropDate.value = ''
}
async function setTodoDueDate(id: string, dueDate: string) {
  const todo = todos.value.find(x => x.id === id)
  if (!todo || todo.dueDate === dueDate) return
  try {
    // 只改 dueDate 也得走全量 UpdateTodo：后端没有单字段更新接口
    await unwrap(await UpdateTodo(
      todo.id, todo.title, todo.priority, dueDate, todo.note,
      todo.startTime, todo.endTime, todo.reminderTime,
      todo.recurrence, todo.tags, todo.status || 'todo',
    ))
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}
async function dropOnDate(ds: string) {
  const id = dragId.value
  dragId.value = ''
  calDropDate.value = ''
  if (!id) return
  await setTodoDueDate(id, ds)
  // 落到目标日期后切过去，用户立刻能看到条目出现在那天（否则停在原日期像没反应）
  selectedDate.value = ds
}

// 构建 6×7 共 42 个单元格（周一为起始列）
// 索引：dueDate → 待办，避免 42 格逐格全量 filter
const todosByDate = computed(() => {
  const m = new Map<string, Todo[]>()
  for (const x of todos.value) {
    const key = x.dueDate || ''
    if (!m.has(key)) m.set(key, [])
    m.get(key)!.push(x)
  }
  return m
})

const cells = computed(() => {
  const first = new Date(viewYear.value, viewMonth.value, 1)
  let lead = first.getDay() - 1
  if (lead < 0) lead = 6
  const start = new Date(viewYear.value, viewMonth.value, 1 - lead)
  const out: {
    date: string; day: number; inMonth: boolean; isToday: boolean
    isSelected: boolean; items: Todo[]; hasReminder: boolean
  }[] = []
  for (let i = 0; i < 42; i++) {
    const d = new Date(start.getFullYear(), start.getMonth(), start.getDate() + i)
    const ds = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    const items = todosByDate.value.get(ds) || []
    out.push({
      date: ds,
      day: d.getDate(),
      inMonth: d.getMonth() === viewMonth.value,
      isToday: ds === todayStr.value,
      isSelected: ds === selectedDate.value,
      items,
      hasReminder: items.some(x => x.reminderTime !== '' && !x.reminderSent),
    })
  }
  return out
})

const selectedLabel = computed(() => {
  if (searching.value) return t('todoSearchResults', { count: selectedTodos.value.length })
  if (selectedDate.value === '') return t('todoUnscheduled')
  const [y, m, d] = selectedDate.value.split('-').map(Number)
  const wk = ['日', '一', '二', '三', '四', '五', '六'][new Date(y, m - 1, d).getDay()]
  return `${m}月${d}日 周${wk}`
})

const unscheduledCount = computed(() => todos.value.filter(x => x.dueDate === '').length)

// 页面内搜索：ListTodos 已经一次把全部待办取到内存，直接前端过滤即可 ——
// 支持拼音、零延迟，也不必再为页面单独开一条 SearchTodos 的请求链路
//（那个接口的价值在命令面板：那边是独立窗口，没有这份内存数据）。
// 搜索时跨日期：只搜当天等于没搜，用户找的是"那条待办在哪天"。
const searchQuery = ref('')
const searching = computed(() => searchQuery.value.trim().length > 0)

// 「今天完成」的条目：即使它挂在过去日期（每日重复待办的典型形态：模板建在旧日期、
// 今天在「今天」视图里被 `!done` 带出来完成），完成后也要留在视野里，
// 否则用户只看到它凭空消失，不知道「今天的已经做完了」。
function isDoneToday(x: Todo): boolean {
  return x.done && (x.completedAt || '').slice(0, 10) === todayStr.value
}
const selectedTodos = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  let list: Todo[]
  if (q) {
    list = todos.value.filter(x =>
      x.parentId === '' && matchesTextOrPinyin([x.title, x.note, x.tags], q, 't:' + x.id))
  } else if (selectedDate.value === '') {
    list = todos.value.filter(x => x.dueDate === '' && x.parentId === '')
  } else {
    const d = selectedDate.value
    // 看「今天」时把逾期未完成的一并带出来：导航角标统计的正是「今天到期 + 逾期」，
    // 而页面此前只按 dueDate 精确匹配，于是角标写 8、点进去只看到 2。
    // 逾期条目挂在过去某天，不并进来就得逐个翻日历才找得到。
    list = todos.value.filter(x => x.parentId === '' && (
      x.dueDate === d || (d === todayStr.value && x.dueDate !== '' && x.dueDate < d && (!x.done || isDoneToday(x)))
    ))
  }
  if (activeTag.value) {
    list = list.filter(x => parseTags(x.tags).includes(activeTag.value))
  }
  // 排序：未完成 → 有时间安排的 → 用户手排的 sort。
  // hasTime 此前是个定义了却没人调的死函数；用在这里是因为"几点要做"比
  // "哪天要做但没说时间"更该排在前面，同一天里这个差别对用户是可见的。
  return [...list].sort((a, b) =>
    Number(a.done) - Number(b.done) ||
    Number(hasTime(b)) - Number(hasTime(a)) ||
    a.sort - b.sort)
})

const remainingCount = computed(() => selectedTodos.value.filter(x => !x.done).length)

// ---- 空状态 ----
// 空态不再放第二颗「添加」按钮（上方快捷输入框已经是最显眼的 CTA），
// 改为温和文案 + 两块有价值的信息：刚做完的（成就感）和接下来 3 天的（提前规划）。
const isTodaySelected = computed(() => !searching.value && selectedDate.value === todayStr.value)
const emptyTitle = computed(() => {
  if (activeTag.value) return t('todoEmptyTagTitle')
  if (selectedDate.value === '') return t('todoEmptyUnscheduled')
  return isTodaySelected.value ? t('todoEmptyCalmToday') : t('todoEmptyCalmDay')
})
const emptyHint = computed(() => activeTag.value ? t('todoEmptyTagHint') : t('todoEmptyQuickHint'))

const recentDone = computed(() => todos.value
  .filter(x => x.done && x.parentId === '' && x.completedAt)
  .sort((a, b) => (b.completedAt || '').localeCompare(a.completedAt || ''))
  .slice(0, 3))

const upcomingPreview = computed(() => {
  if (searching.value) return []
  const base = new Date(); base.setHours(0, 0, 0, 0)
  const days = [1, 2, 3].map(n => {
    const d = new Date(base.getTime() + n * 86400000)
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  })
  const labels: Record<string, string> = { [days[0]]: t('todoTomorrow'), [days[1]]: t('todoDayAfter') }
  return todos.value
    .filter(x => !x.done && x.parentId === '' && x.dueDate && days.includes(x.dueDate))
    .sort((a, b) => a.dueDate.localeCompare(b.dueDate) || a.sort - b.sort)
    .slice(0, 4)
    .map(x => ({ ...x, dayLabel: labels[x.dueDate] || x.dueDate.slice(5) }))
})

// ---- 操作 ----
let refreshGen = 0
async function refresh() {
  const gen = ++refreshGen
  loading.value = true
  error.value = ''
  try {
    const r = unwrap<Todo[]>(await ListTodos()) ?? []
    if (gen !== refreshGen) return
    todos.value = r
  } catch (e) {
    if (gen !== refreshGen) return
    error.value = getErrorMessage(e)
  } finally {
    if (gen === refreshGen) {
      loading.value = false
      // 所有增删改都走 refresh()（新建/编辑/勾选/删除/清空/拖拽/子任务），
      // 在这里统一补一次角标刷新：后端 CountTodosDue 是即时的，但角标原本只靠
      // 60s 轮询，删掉一条到期待办后侧边栏数字还会杵着，看起来像没删掉。
      navBadges().refreshNow()
    }
  }
}

function prevMonth() {
  if (viewMonth.value === 0) { viewMonth.value = 11; viewYear.value-- } else { viewMonth.value-- }
}
function nextMonth() {
  if (viewMonth.value === 11) { viewMonth.value = 0; viewYear.value++ } else { viewMonth.value++ }
}
function goToday() {
  const d = new Date()
  viewYear.value = d.getFullYear(); viewMonth.value = d.getMonth()
  selectedDate.value = todayStr.value
}
function selectDate(ds: string) {
  selectedDate.value = ds
  const [y, m] = ds.split('-').map(Number)
  viewYear.value = y; viewMonth.value = m - 1
}
function selectUnscheduled() {
  selectedDate.value = ''
}

// ---- 新增（点击 + 按钮弹出弹窗）----
const showCreate = ref(false)
// 弹窗内的即时反馈：页面级 error 在遮罩下面，弹窗打开时用户看不见，
// 标题空 / 保存失败都会表现成「点了没反应」。与 SchedulePage / MonitorPage 的 formErr 同口径。
const formErr = ref('')
const cTitle = ref('')
const cPriority = ref('none')
const cDueDate = ref('')
const cNote = ref('')
const cStart = ref('')
const cEnd = ref('')
const cReminder = ref('')
const cTags = ref('')
const cRecKind = ref('none')
const cRecTime = ref('09:00')
const cRecWeekdays = ref('')

function openCreate() {
  formErr.value = ''
  cTitle.value = ''
  cPriority.value = 'none'
  // 未指定日期时默认落在今天，避免新建的待办静默掉进「未排期」
  cDueDate.value = selectedDate.value || todayStr.value
  cNote.value = ''
  // 时间字段预填当前日期（跟随截止日，未选日期则今天）：开始=当前时刻、结束=+1h、提醒与开始同刻。
  // 原生 datetime-local 空值时日期段是 yyyy/mm/日，用户得从年份一格格拨到今天，太费事。
  const baseDay = cDueDate.value
  const startD = new Date(`${baseDay}T${toLocalInput(new Date()).slice(11)}`)
  const endD = new Date(startD.getTime() + 60 * 60 * 1000)
  cStart.value = toLocalInput(startD)
  cEnd.value = toLocalInput(endD)
  cReminder.value = cStart.value
  cTags.value = ''
  cRecKind.value = 'none'
  cRecTime.value = '09:00'
  cRecWeekdays.value = ''
  showCreate.value = true
}
async function createTodo() {
  const title = cTitle.value.trim()
  if (!title) { formErr.value = t('todoErrTitle'); return }
  formErr.value = ''
  try {
    await unwrap(await CreateTodo(
      title, cPriority.value, cDueDate.value, cNote.value,
      fromInput(cStart.value), fromInput(cEnd.value), fromInput(cReminder.value),
      recurrenceToJSON({ kind: cRecKind.value, timeOfDay: cRecTime.value, weekdays: cRecWeekdays.value }),
      tagsTextToJSON(cTags.value),
    ))
    showCreate.value = false
    invalidateLinkTargets() // 新待办立刻可当互链目标，不等候选缓存 TTL
    await refresh()
  } catch (e) {
    formErr.value = getErrorMessage(e)
  }
}

// ---- 行内快速添加 ----
//
// 「+」弹窗有九个字段，但日常新增待办九成只要一个标题。多出来的那一步是纯摩擦。
// 这里只带走标题与当前选中的日期（今天 / 某个分组），提醒、重复、优先级仍回弹窗——
// 那些是「这条和别的不一样」的地方，塞进一行输入会让回车变成一次不确定的提交。
const quickTitle = ref('')
const quickBusy = ref(false)

async function quickCreate() {
  const title = quickTitle.value.trim()
  if (!title || quickBusy.value) return
  quickBusy.value = true
  try {
    await unwrap(await CreateTodo(
      title, 'none', selectedDate.value || todayStr.value, '',
      '', '', '',
      recurrenceToJSON({ kind: 'none', timeOfDay: '09:00', weekdays: '' }),
      tagsTextToJSON(''),
    ))
    quickTitle.value = ''
    invalidateLinkTargets() // 新待办立刻可当互链目标
    await refresh()
  } catch (e) {
    reportError(e, 'TodoPage.quickCreate')
  } finally {
    quickBusy.value = false
  }
}

// ---- 编辑 ----
const showEdit = ref(false)
const editing = ref<Todo | null>(null)
const editTitle = ref('')
const editPriority = ref('none')
const editDueDate = ref('')
const editNote = ref('')
const editStart = ref('')   // datetime-local 格式 YYYY-MM-DDTHH:MM:SS
const editEnd = ref('')
const editReminder = ref('')
const editTags = ref('')
const editRecKind = ref('none')
const editRecTime = ref('09:00')
const editRecWeekdays = ref('')
const editStatus = ref('todo')
// 重复待办的「本次编辑应用范围」：默认 all 保持原有行为不变。
type EditScope = 'all' | 'thisOnly' | 'fromNext'
const editScope = ref<EditScope>('all')
// 只有「原本就是重复待办」才显示范围选择：普通待办 UI 保持原样，
// 且推进 due_date 应以模板自身的重复规则为准，而不是用户在弹窗里临时改的 kind。
const editIsRecurring = computed(() =>
  !!editing.value && parseRecurrence(editing.value.recurrence).kind !== 'none')

function toInput(s: string): string { return s ? s.replace(' ', 'T') : '' }
function fromInput(s: string): string { return s ? s.replace('T', ' ') : '' }

function startEdit(todo: Todo) {
  formErr.value = ''
  editing.value = todo
  editTitle.value = todo.title
  editPriority.value = todo.priority
  editDueDate.value = todo.dueDate
  editNote.value = todo.note
  editStart.value = toInput(todo.startTime)
  editEnd.value = toInput(todo.endTime)
  editReminder.value = toInput(todo.reminderTime)
  editTags.value = tagsJSONToText(todo.tags)
  const rf = parseRecurrence(todo.recurrence)
  editRecKind.value = rf.kind
  editRecTime.value = rf.timeOfDay
  editRecWeekdays.value = rf.weekdays
  editStatus.value = todo.status || 'todo'
  editScope.value = 'all' // 每次打开都回到默认，避免上次的选择串到这条
  showEdit.value = true
}
async function saveEdit() {
  if (!editing.value) return
  const title = editTitle.value.trim()
  if (!title) { formErr.value = t('todoErrTitle'); return }
  formErr.value = ''
  const orig = editing.value
  const editedRec = recurrenceToJSON({ kind: editRecKind.value, timeOfDay: editRecTime.value, weekdays: editRecWeekdays.value })
  const editedTags = tagsTextToJSON(editTags.value)
  const editedStart = fromInput(editStart.value)
  const editedEnd = fromInput(editEnd.value)
  const editedReminder = fromInput(editReminder.value)
  try {
    // 重复待办 + 非「全部」：单模板模型下用「推进模板 due_date + 必要时新建一条 occurrence」实现。
    if (editScope.value !== 'all' && editIsRecurring.value) {
      const originalDue = orig.dueDate
      const advanced = nextDueDate(originalDue, parseRecurrence(orig.recurrence))
      if (editScope.value === 'thisOnly') {
        // 仅本次：先建本次的独立待办（不带 recurrence、截止日=原日期），成功后再推进模板。
        // 顺序反了会在「模板已推进、本次未建成」时把本周期整条弄丢。
        let created: Todo | null = null
        try {
          created = unwrap<Todo>(await CreateTodo(
            title, editPriority.value, originalDue, editNote.value,
            editedStart, editedEnd, editedReminder, '', editedTags,
          ))
          await unwrap(await UpdateTodo(
            orig.id, orig.title, orig.priority, advanced, orig.note,
            orig.startTime, orig.endTime, orig.reminderTime, orig.recurrence,
            orig.tags, orig.status,
          ))
        } catch (e) {
          if (created?.id) {
            try { unwrap(await DeleteTodo(created.id)) } catch (e2) { logErr('TodoPage.rollbackOccurrence', e2) }
          }
          throw e
        }
        invalidateLinkTargets() // 新 occurrence 立即可当互链目标
      } else {
        // 从下次起：改模板本身，但把截止日推到下一周期——编辑从下一次开始生效，本次保持原样。
        await unwrap(await UpdateTodo(
          orig.id, title, editPriority.value, advanced, editNote.value,
          editedStart, editedEnd, editedReminder,
          editedRec, editedTags, editStatus.value,
        ))
      }
    } else {
      // 全部：保持原有行为——照旧改这一条模板。
      await unwrap(await UpdateTodo(
        orig.id, title, editPriority.value, editDueDate.value, editNote.value,
        editedStart, editedEnd, editedReminder, editedRec, editedTags, editStatus.value,
      ))
    }
    showEdit.value = false
    editing.value = null
    await refresh()
  } catch (e) {
    formErr.value = getErrorMessage(e)
  }
}

// 把带时间的日期字符串按「天数差」整体平移（保留原来的时刻）。
// snooze 的「推一天」和「自定义日期」共用这套算法：自定义只是 days 由目标日算出。
function diffDays(from: string, to: string): number {
  const a = parseDateOnly(from.slice(0, 10))
  const b = parseDateOnly(to.slice(0, 10))
  if (!a || !b) return 1
  return Math.round((b.getTime() - a.getTime()) / 86400000)
}
function shiftDays(s: string, days: number): string {
  if (!s) return ''
  const d = parseDateOnly(s.slice(0, 10))
  if (!d) return s
  d.setDate(d.getDate() + days)
  return formatDateOnly(d) + s.slice(10)
}

// 稍后提醒：把 due_date（连同起止/提醒时刻）整体推后，保留未完成状态、
// 不动 recurrence。传 targetDate 就是「自定义日期」入口；不传则沿用原来的推一天。
// UpdateTodo 会复位 reminder_sent，正好让提醒重新响一次。
async function snooze(todo: Todo, targetDate?: string) {
  const base = todo.dueDate || todayStr.value
  const days = targetDate ? diffDays(base, targetDate) : 1
  const due = shiftDays(base, days)
  try {
    await unwrap(await UpdateTodo(
      todo.id, todo.title, todo.priority, due, todo.note,
      shiftDays(todo.startTime, days), shiftDays(todo.endTime, days), shiftDays(todo.reminderTime, days),
      todo.recurrence,
      todo.tags as unknown as string, 'todo',
    ))
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// 自定义改期：点行内日历图标展开一个 date 输入框，选完直接调 snooze(todo, 日期)。
const snoozeDateFor = ref('')
const snoozeDateValue = ref('')
function openSnoozeDate(todo: Todo) {
  if (snoozeDateFor.value === todo.id) { snoozeDateFor.value = ''; return }
  snoozeDateFor.value = todo.id
  // 输入框预填「原截止日 + 一天」，与默认 snooze 行为一致；没有截止日就用今天。
  const base = todo.dueDate || todayStr.value
  snoozeDateValue.value = shiftDays(base, 1).slice(0, 10)
}
async function applySnoozeDate(todo: Todo) {
  const date = snoozeDateValue.value
  snoozeDateFor.value = ''
  if (!date) return
  await snooze(todo, date)
}

async function toggle(todo: Todo) {
  try { await unwrap(await ToggleTodo(todo.id)); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}

// 重复待办「完成」的范围选择：点勾选框时先弹菜单，由用户决定这次完成应用到哪些实例。
// 普通待办与已完成的重复待办（取消完成）保持原来的 ToggleTodo 行为，不多一层点击。
const completeScopeFor = ref('')
function onCompleteClick(todo: Todo) {
  const recurring = parseRecurrence(todo.recurrence).kind !== 'none'
  if (!todo.done && recurring) {
    completeScopeFor.value = completeScopeFor.value === todo.id ? '' : todo.id
    snoozeDateFor.value = ''
    return
  }
  completeScopeFor.value = ''
  void toggle(todo)
}

// 复用 saveEdit 那一套「推进模板 due_date + 必要时 CreateTodo」的单模板模型实现：
//   all      —— 整条模板标记完成，系列到此结束（等同原 ToggleTodo）。
//   thisOnly —— 模板推进到下一周期、保持未完成（继续生成未来实例），
//               另建一条不带 recurrence 的独立待办，截止日为原日期、标记完成，代表「本次」。
//   fromNext —— 模板推进到下一周期并标记完成（本次及以后都不再做），
//               同样拆一条本次的完成记录。单模板模型无法真正生成并完成无限多个未来实例，
//               这是前端能做到的最接近语义；完整序列需要后端按范围批量生成，见报告。
async function completeRecurring(todo: Todo, scope: EditScope) {
  completeScopeFor.value = ''
  if (scope === 'all') { await toggle(todo); return }
  // 顺序很重要：先落「本次」的完成记录，再推进模板。
  // 反过来的话，模板已推进而本次没建成 → 本周期既无完成记录、列表里也再看不到这条（数据丢失）。
  let created: Todo | null = null
  try {
    created = unwrap<Todo>(await CreateTodo(
      todo.title, todo.priority, todo.dueDate || todayStr.value, todo.note,
      todo.startTime, todo.endTime, todo.reminderTime, '', todo.tags as unknown as string,
    ))
    if (created?.id) {
      await unwrap(await UpdateTodo(
        created.id, created.title, created.priority, created.dueDate, created.note,
        created.startTime, created.endTime, created.reminderTime, '', created.tags as unknown as string, 'done',
      ))
    }
  } catch (e) {
    error.value = getErrorMessage(e)
    await refresh()
    return
  }
  try {
    const advanced = nextDueDate(todo.dueDate || todayStr.value, parseRecurrence(todo.recurrence))
    await unwrap(await UpdateTodo(
      todo.id, todo.title, todo.priority, advanced, todo.note,
      todo.startTime, todo.endTime, todo.reminderTime, todo.recurrence,
      todo.tags as unknown as string, scope === 'thisOnly' ? (todo.status || 'todo') : 'done',
    ))
    invalidateLinkTargets() // 新拆出的 occurrence 立即可当互链目标
  } catch (e) {
    error.value = getErrorMessage(e)
    // 模板没推进成功就把刚建的本次记录撤掉，别留下一条重复的完成项
    if (created?.id) {
      try { unwrap(await DeleteTodo(created.id)) } catch (e2) { logErr('TodoPage.rollbackOccurrence', e2) }
    }
  }
  await refresh()
}
async function remove(todo: Todo) {
  delTodo.value = todo
  showDelConfirm.value = true
}
async function clearCompleted() {
  showClearConfirm.value = true
}

const delTodo = ref<Todo | null>(null)
const showDelConfirm = ref(false)
const showClearConfirm = ref(false)

async function confirmDel() {
  const todo = delTodo.value
  if (!todo) return
  showDelConfirm.value = false
  delTodo.value = null
  try { await unwrap(await DeleteTodo(todo.id)); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}
async function confirmClear() {
  showClearConfirm.value = false
  try { await unwrap(await ClearCompletedTodos()); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}

// 测试提醒
const testing = ref(false)
async function testReminder() {
  testing.value = true
  try {
    await unwrap(await SendTestNotification('', ''))
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    testing.value = false
  }
}

function isOverdue(todo: Todo) {
  return todo.dueDate !== '' && todo.dueDate < todayStr.value && !todo.done
}

// 完成时间文案：completedAt 是 RFC3339（带时区偏移），slice 取日期/时分即可
function doneLabel(todo: Todo): string {
  const s = todo.completedAt || ''
  if (!s) return t('todoDone')
  const day = s.slice(0, 10)
  const hm = s.slice(11, 16)
  if (day === todayStr.value) return t('todoDoneToday', { time: hm })
  const y = new Date(); y.setDate(y.getDate() - 1)
  const ys = `${y.getFullYear()}-${String(y.getMonth() + 1).padStart(2, '0')}-${String(y.getDate()).padStart(2, '0')}`
  if (day === ys) return t('todoDoneYesterday', { time: hm })
  return `${day.slice(5)} ${hm}`
}

// ---- 命令面板深链定位 ----
// 面板选中一条待办后由后端广播 id，App.vue 写入 pendingOpenTodoId 并切到本页。
// 关键点是「先把 gaze 挪过去」：列表只显示 selectedDate 那一天（或未排期），
// 日期不对时目标压根不在 DOM 里，谈滚动/高亮都是空的。
const highlightTodoId = ref('')
let highlightTimer: number | null = null

async function focusPendingTodo() {
  const id = pendingOpenTodoId.value
  if (!id) return
  pendingOpenTodoId.value = '' // 先清空： navigateTo 与本页 mount 的先后不确定，可能重复触发
  // id 可能先于数据到位（切页时列表还没 refresh 完）
  let target = todos.value.find(x => x.id === id)
  if (!target) {
    await refresh()
    target = todos.value.find(x => x.id === id)
  }
  if (!target) return
  selectedDate.value = target.dueDate // '' = 未排期，与 selectedTodos 的取值口径一致
  if (activeTag.value && !parseTags(target.tags).includes(activeTag.value)) activeTag.value = ''
  if (viewMode.value !== 'list') viewMode.value = 'list' // 看板视图下没有可滚动的单行
  await nextTick()
  const el = document.querySelector<HTMLElement>(`[data-todo-id="${id.replace(/"/g, '\\"')}"]`)
  if (!el) return
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  highlightTodoId.value = id
  if (highlightTimer) clearTimeout(highlightTimer)
  highlightTimer = window.setTimeout(() => { highlightTodoId.value = '' }, 1800)
}

watch(pendingOpenTodoId, (v) => { if (v) void focusPendingTodo() })

// 总览「新建待办」快捷动作：标志在切页前写好，这里消费即清。
// watch + onMounted 双保险：TodoPage 在 KeepAlive 白名单里，已挂载时走 watch，
// 首次从总览跳过来时组件尚未创建，靠 onMounted 补一次。
function consumeCreateRequest() {
  if (!pendingCreateTodo.value) return
  pendingCreateTodo.value = false
  openCreate()
}
watch(pendingCreateTodo, consumeCreateRequest)

function onDocClick(e: MouseEvent) {
  const el = e.target as HTMLElement
  if (!el.closest?.('.focus-hist-wrap')) {
    showFocusHist.value = false
    showFocusStats.value = false
  }
  if (!el.closest?.('.focus-dur-wrap')) showFocusDur.value = false
  if (!el.closest?.('.panel-more-wrap')) showMore.value = false
}
// 全局 click 监听要跟着激活态走：本页保活后切走不卸载，若一直挂着，
// 用户在别的页面点一下也会触发本页的 onDocClick（收起浮层），属于串台。
function attachGlobal() { document.addEventListener('click', onDocClick) }
function detachGlobal() { document.removeEventListener('click', onDocClick) }

// stale-while-revalidate：切回来直接显示上次列表，再静默补一次。
// refresh() 只覆盖 todos.value、不清空，加载期间旧数据照常可见；
// 首次挂载时 onMounted 已刷过，紧随的 onActivated 由 pageActive 挡掉，不重复请求。
let pageActive = false
let needsRefresh = false
function startPage() {
  if (pageActive) return
  pageActive = true
  attachGlobal()
  if (needsRefresh) { needsRefresh = false; void refresh() }
}
function stopPage() {
  pageActive = false
  needsRefresh = true
  detachGlobal()
}

onMounted(() => {
  selectedDate.value = todayStr.value
  void refresh()
  // 专注计时可能在上次离开本页时仍在跑（状态在 localStorage 里），先接上再渲染
  restoreFocus()
  // id 可能在组件创建之前就已写入（先切页、后 mount），watch 不会回溯触发
  if (pendingOpenTodoId.value) void focusPendingTodo()
  consumeCreateRequest()
  startPage()
})
onActivated(startPage)
onDeactivated(stopPage)
onUnmounted(() => {
  if (highlightTimer) clearTimeout(highlightTimer)
  detachGlobal()
})
// 注意：focusTimer 刻意不停。它只负责刷新显示，但计时完成通知也在 tickFocus 里
// 发出；切到别的页面时专注仍在继续，停掉会让用户错过完成提醒。LRU 淘汰真正
// 卸载时由上方已有的 onUnmounted(stopFocusTimer) 收尾。
</script>

<template>
  <div class="todo-page">
    <!-- 顶部栏 -->
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title">{{ t('todoTitle') }}</h2>
        <span class="qd-page-sub">{{ t('todoSubtitle') }}</span>
      </template>
      <div class="qd-page-fill">
        <QdSearchBox v-model="searchQuery" class="todo-search" :icon-size="14" :placeholder="t('todoSearchPh')" />
        <div class="cal-nav">
          <button class="qd-icon-btn" @click="prevMonth" :title="t('prevMonth')"><ChevronLeft :size="16" /></button>
          <span class="cal-month">{{ monthLabel }}</span>
          <button class="qd-icon-btn" @click="nextMonth" :title="t('nextMonth')"><ChevronRight :size="16" /></button>
          <button v-if="!atToday" class="qd-btn qd-btn-ghost" @click="goToday">{{ t('todoToday') }}</button>
        </div>
      </div>
      <div class="qd-page-actions">
        <!-- 专注时长：一排光秃秃的数字（25/45/60）猜不出含义，收成一个带秒表图标的下拉 -->
        <div class="focus-dur-wrap">
          <button class="focus-dur-trigger" :class="{ open: showFocusDur }"
            :title="t('todoFocusDur')" @click.stop="showFocusDur = !showFocusDur">
            <Timer :size="14" />
            <span class="fd-label">{{ t('todoFocusMode') }}</span>
            <span class="fd-value">{{ focusMinutes }} {{ t('todoFocusMinUnit') }}</span>
            <ChevronDown :size="12" class="fd-caret" />
          </button>
          <div v-key-click v-if="showFocusDur" class="focus-dur-pop" @click.stop>
            <button v-for="d in FOCUS_DEFAULTS" :key="d"
              :class="['fd-opt', { active: focusMinutes === d }]"
              @click="pickFocusMinutes(d)">
              {{ d }} {{ t('todoFocusMinUnit') }}
            </button>
            <div class="fd-custom">
              <span>{{ t('todoFocusCustom') }}</span>
              <input
                class="focus-dur-custom"
                type="number" min="1" max="180"
                :value="isFocusPreset ? '' : focusMinutes"
                :placeholder="String(FOCUS_DEFAULTS[0])"
                @change="applyFocusCustom"
              />
            </div>
          </div>
        </div>
        <div class="focus-hist-wrap">
          <button class="qd-icon-btn focus-hist-btn" :class="{ active: showFocusHist }"
            :title="t('todoFocusHistory')" @click.stop="toggleFocusHist">
            <History :size="15" />
            <span v-if="todayFocusStats.count" class="focus-hist-badge">{{ todayFocusStats.count }}</span>
          </button>
          <!-- 统计入口：与历史浮层互斥，v-if 挂载时 FocusStatsPanel 自行拉数 -->
          <button class="qd-icon-btn focus-stats-btn" :class="{ active: showFocusStats }"
            :title="t('focusStatsTitle')" @click.stop="toggleFocusStats">
            <BarChart3 :size="15" />
          </button>
          <div v-key-click v-if="showFocusHist" class="focus-hist-pop" @click.stop>
            <div class="fh-head">{{ t('todoFocusTodayStat', { count: todayFocusStats.count, min: todayFocusStats.minutes }) }}</div>
            <div v-if="!focusHistory.length" class="fh-empty">{{ t('todoFocusNoHistory') }}</div>
            <div v-else class="fh-list">
              <div v-for="r in focusHistory.slice(0, 20)" :key="r.ts" class="fh-row">
                <span class="fh-dot" :class="{ ok: r.done }"></span>
                <span class="fh-title">{{ r.title || t('focusTimerTitle') }}</span>
                <span class="fh-min">{{ r.minutes }}{{ t('todoFocusMinUnit') }}</span>
                <span class="fh-time">{{ fmtHistTime(r.ts) }}</span>
              </div>
            </div>
          </div>
          <div v-key-click v-if="showFocusStats" class="focus-stats-pop" @click.stop>
            <FocusStatsPanel :refresh-token="focusStatsRefresh" />
          </div>
        </div>
      </div>
    </PageHeader>

    <div v-if="error" class="todo-error">{{ error }}</div>

    <div class="todo-body">
      <!-- 左侧：月历 -->
      <div class="cal-card">
        <div class="cal-weekdays">
          <span v-for="w in WEEKDAY_NAMES" :key="w" class="cal-weekday">{{ w }}</span>
        </div>
        <div class="cal-grid">
          <button
            v-for="c in cells"
            :key="c.date"
            :class="['cal-cell', { 'out-month': !c.inMonth, 'is-today': c.isToday, 'is-selected': c.isSelected, 'drop-over': calDropDate === c.date }]"
            @click="selectDate(c.date)"
            @dragover.prevent="calDropDate = c.date"
            @dragleave="onCalDragLeave(c.date)"
            @drop.prevent="dropOnDate(c.date)"
          >
            <span class="cal-day">{{ c.day }}</span>
            <span class="cal-marks">
              <i v-for="it in c.items.slice(0, 3)" :key="it.id"
                 :class="['dot', 'p-' + it.priority, { done: it.done }]"></i>
              <Bell v-if="c.hasReminder" :size="9" class="cal-bell" />
              <em v-if="c.items.length > 3" class="cal-more">+{{ c.items.length - 3 }}</em>
            </span>
          </button>
        </div>
        <button class="unscheduled-chip" :class="{ active: selectedDate === '' }" @click="selectUnscheduled">
          <CalendarDays :size="14" />
          <span>{{ t('todoUnscheduled') }}</span>
          <em v-if="unscheduledCount" class="chip-count">{{ unscheduledCount }}</em>
        </button>
      </div>

      <!-- 右侧：选中日任务 -->
      <div class="todo-panel">
        <div class="panel-head">
          <div class="panel-head-left">
            <span class="panel-date">{{ selectedLabel }}</span>
            <span v-if="isTodaySelected" class="panel-today-tag">{{ t('todoToday') }}</span>
          </div>
          <div class="panel-head-right">
            <div class="view-toggle">
              <button :class="['vt-btn', { active: viewMode === 'list' }]" @click="viewMode = 'list'">{{ t('todoViewList') }}</button>
              <button :class="['vt-btn', { active: viewMode === 'kanban' }]" @click="viewMode = 'kanban'">{{ t('todoViewKanban') }}</button>
              <button :class="['vt-btn', { active: viewMode === 'quadrant' }]" @click="viewMode = 'quadrant'">{{ t('todoViewQuadrant') }}</button>
            </div>
            <span class="panel-remaining">{{ t('todoRemaining', { count: remainingCount }) }}</span>
            <button class="add-fab" @click="openCreate" :title="t('todoAdd')"><Plus :size="16" /></button>
            <!-- 低频操作从底栏收进这里，列表底部不再挂一排灰字 -->
            <div class="panel-more-wrap">
              <button class="qd-icon-btn" :class="{ active: showMore }"
                :title="t('todoMore')" @click.stop="showMore = !showMore">
                <MoreHorizontal :size="16" />
              </button>
              <div v-key-click v-if="showMore" class="panel-more-pop" @click.stop>
                <button class="pm-item danger" @click="clearCompleted(); showMore = false">
                  <Trash2 :size="13" /> {{ t('todoClearCompleted') }}
                </button>
                <button class="pm-item" :disabled="testing" @click="testReminder(); showMore = false">
                  <Bell :size="13" /> {{ t('todoTestReminder') }}
                </button>
                <p class="pm-hint">{{ t('todoReminderHint') }}</p>
              </div>
            </div>
          </div>
        </div>

        <!-- 列表视图 -->
        <template v-if="viewMode === 'list'">
          <!-- 行内快速添加：回车即建，落在当前选中的日期上。它是本页最常用的入口，
               所以做成实心输入控件（描边 + 卡片底），而不是一行虚线占位符。 -->
          <div class="quick-add">
            <Plus :size="14" class="quick-add-icon" />
            <input
              v-model="quickTitle"
              class="quick-add-input"
              :placeholder="t('todoQuickAddPlaceholder')"
              :disabled="quickBusy"
              @keydown.enter.prevent="quickCreate"
            />
            <kbd class="quick-add-kbd" aria-hidden="true">↵</kbd>
          </div>
          <!-- 4.2 标签筛选 -->
          <div v-if="allTags.length" class="tag-filter">
            <button :class="['tag-fbtn', { active: activeTag === '' }]" @click="activeTag = ''">{{ t('todoAllTags') }}</button>
            <button
              v-for="tg in allTags" :key="tg"
              :class="['tag-fbtn', { active: activeTag === tg }]"
              @click="activeTag = tg"
            >{{ tg }}</button>
          </div>

          <!-- 列表 -->
          <div class="todo-list">
            <div v-if="selectedTodos.length === 0" class="todo-empty">
              <template v-if="searching">
                <div class="empty-hero">
                  <CalendarDays :size="20" class="empty-icon" />
                  <p>{{ t('noMatchItems') }}</p>
                  <p class="empty-hint">{{ t('tryOtherKeywords') }}</p>
                </div>
              </template>
              <template v-else>
                <!-- 空态不放第二个「添加」按钮：上方输入框就是唯一的行动号召 -->
                <div class="empty-hero">
                  <svg class="empty-art" viewBox="0 0 120 100" fill="none" aria-hidden="true">
                    <circle cx="60" cy="50" r="36" fill="var(--color-accent)" opacity="0.07" />
                    <g class="art-sway">
                      <path d="M60 60C60 48 57 40 52 34" stroke="var(--color-text-muted)" stroke-width="2" stroke-linecap="round" />
                      <path d="M60 60C60 50 64 42 69 36" stroke="var(--color-text-muted)" stroke-width="2" stroke-linecap="round" />
                      <ellipse cx="49" cy="35" rx="9" ry="5.5" transform="rotate(-28 49 35)" fill="var(--color-accent)" opacity="0.55" />
                      <ellipse cx="72" cy="37" rx="9" ry="5.5" transform="rotate(26 72 37)" fill="var(--color-accent)" opacity="0.35" />
                      <ellipse cx="60" cy="27" rx="7" ry="4.5" fill="var(--color-accent)" opacity="0.7" />
                    </g>
                    <rect x="43" y="59" width="34" height="7" rx="2.5" fill="var(--color-bg-tertiary)" stroke="var(--color-border)" />
                    <path d="M46 66h28l-3.2 17.5c-.5 2.6-2.5 4.5-5 4.5H54.2c-2.5 0-4.5-1.9-5-4.5L46 66z"
                      fill="var(--color-bg-tertiary)" stroke="var(--color-border)" />
                  </svg>
                  <p class="empty-title">{{ emptyTitle }}</p>
                  <p class="empty-hint">{{ emptyHint }}</p>
                </div>

                <!-- 有价值的补充信息，比一大片纯黑更有用：刚做完的给成就感，接下来的便于规划 -->
                <div v-if="recentDone.length || upcomingPreview.length" class="empty-insights">
                  <div v-if="recentDone.length" class="empty-card">
                    <div class="empty-card-title"><Check :size="12" /> {{ t('todoEmptyRecentDone') }}</div>
                    <div v-for="x in recentDone" :key="x.id" class="empty-card-row">
                      <span class="ecr-title">{{ x.title }}</span>
                      <span class="ecr-date">{{ (x.completedAt || '').slice(5, 10) }}</span>
                    </div>
                  </div>
                  <div v-if="upcomingPreview.length" class="empty-card">
                    <div class="empty-card-title"><CalendarDays :size="12" /> {{ t('todoEmptyUpcoming') }}</div>
                    <div v-for="x in upcomingPreview" :key="x.id" class="empty-card-row">
                      <span class="ecr-title">{{ x.title }}</span>
                      <span class="ecr-date">{{ x.dayLabel }}</span>
                    </div>
                  </div>
                </div>
              </template>
            </div>
            <div
              v-for="todo in selectedTodos"
              :key="todo.id"
              :data-todo-id="todo.id"
              draggable="true"
              :class="['todo-item', { done: todo.done, overdue: isOverdue(todo), 'todo-flash': highlightTodoId === todo.id }]"
              @dragstart="onDragStart(todo.id, $event)"
              @dragend="onDragEnd"
            >
              <button class="check" :class="{ checked: todo.done }" @click="onCompleteClick(todo)">
                <Check v-if="todo.done" :size="13" />
              </button>
              <!-- 重复待办完成范围：点勾选框后弹的小菜单，复用编辑弹窗那三个 scope key。
                   绝对定位挂在行上，点选项即执行并关闭。 -->
              <div v-if="completeScopeFor === todo.id" class="complete-scope" @click.stop>
                <div class="complete-scope-title">{{ t('todoOccScope') }}</div>
                <button @click="completeRecurring(todo, 'thisOnly')">{{ t('todoOccThisOnly') }}</button>
                <button @click="completeRecurring(todo, 'fromNext')">{{ t('todoOccFromNext') }}</button>
                <button @click="completeRecurring(todo, 'all')">{{ t('todoOccAll') }}</button>
              </div>
              <i :class="['pri-dot', 'p-' + todo.priority]"></i>
              <div class="todo-main">
                <span class="todo-name">{{ todo.title }}</span>
                <div class="todo-meta">
                  <!-- 搜索是跨日期的，列表必须标出这一条属于哪天，否则只有标题看不出落点 -->
                  <span v-if="searching && todo.dueDate" class="meta-pill">{{ todo.dueDate }}</span>
                  <span v-if="isOverdue(todo)" class="overdue-text">{{ t('todoOverdue') }}</span>
                  <span v-if="todo.done" class="meta-pill done-pill">
                    <Check :size="11" /> {{ doneLabel(todo) }}
                  </span>
                  <span v-if="todo.reminderTime" class="meta-pill" :class="{ sent: todo.reminderSent }">
                    <Bell v-if="!todo.reminderSent" :size="11" />
                    <BellRing v-else :size="11" />
                    {{ todo.reminderTime.slice(5, 16) }}{{ todo.reminderSent ? ' · ' + t('todoReminderSent') : '' }}
                  </span>
                  <span v-if="todo.startTime" class="meta-pill">
                    <Clock :size="11" />
                    {{ shortTime(todo.startTime) }}<template v-if="todo.endTime">–{{ shortTime(todo.endTime) }}</template>
                  </span>
                  <span v-if="recurrenceLabel(todo)" class="meta-pill rec-pill">
                    <RefreshCw :size="11" /> {{ recurrenceLabel(todo) }}
                  </span>
                  <template v-for="tg in parseTags(todo.tags)" :key="tg">
                    <span v-key-click class="tag-chip" @click.stop="activeTag = tg">{{ tg }}</span>
                  </template>
                  <button class="status-pill" :class="'st-' + (todo.status || 'todo')" @click.stop="cycleStatus(todo)">
                    {{ statusLabel(todo.status) }}
                  </button>
                  <span v-if="todo.note" class="todo-note">· {{ todo.note }}</span>
                </div>
              </div>
              <div class="todo-actions">
                <template v-if="focusTodoId === todo.id">
                  <button :class="['act focus-act', { running: focusRunning }]" @click="pauseFocus()"
                    :title="focusRunning ? t('todoFocusPause') : t('todoFocusResume')">
                    {{ fmtFocus(focusRemaining) }}
                  </button>
                  <button class="act danger" @click="endFocus()" :title="t('todoFocusEnd')"><X :size="13" /></button>
                </template>
                <template v-else>
                  <button class="act" @click="startFocus(todo)" :title="t('todoFocusStart')"><Timer :size="13" /></button>
                </template>
                <button class="act" @click="toggleSubInput(todo)" :title="t('todoSubtaskAdd')"><ListTree :size="13" /></button>
                <button class="act" @click="snooze(todo)" :title="t('todoSnooze')"><Clock :size="13" /></button>
                <button class="act" @click="openSnoozeDate(todo)" :title="t('todoSnoozeDate')"><CalendarDays :size="13" /></button>
                <button class="act" @click="startEdit(todo)" :title="t('todoEdit')"><Pencil :size="13" /></button>
                <button class="act danger" @click="remove(todo)" :title="t('todoDelete')"><Trash2 :size="13" /></button>
              </div>

              <!-- 自定义改期：行内日期输入框，选完即改；Esc / 再点日历图标收起 -->
              <div v-if="snoozeDateFor === todo.id" class="snooze-date" @click.stop>
                <input v-model="snoozeDateValue" type="date" @change="applySnoozeDate(todo)" @keyup.esc="snoozeDateFor = ''" />
              </div>

              <!-- 4.1 子任务（单层级 checklist）-->
              <div v-if="subtasksOf(todo.id).length || addingSubFor === todo.id" class="subtasks" @dragstart.stop>
                <div v-if="subtasksOf(todo.id).length" class="sub-progress">
                  <div class="sub-progress-bar"><div class="sub-progress-fill" :style="{ width: subProgress(todo.id).pct + '%' }"></div></div>
                  <span class="sub-progress-text">{{ subProgress(todo.id).done }}/{{ subProgress(todo.id).total }} ({{ subProgress(todo.id).pct }}%)</span>
                </div>
                <div v-for="(st, si) in subtasksOf(todo.id)" :key="st.id" :class="['sub-item', { done: st.done }]">
                  <button class="check sub-check" :class="{ checked: st.done }" @click="toggle(st)">
                    <Check v-if="st.done" :size="11" />
                  </button>
                  <!-- 行内改名：双击进入，Enter/blur 提交。子任务原本只能删了重建，
                       改一个字都要重来一遍 -->
                  <input
                    v-if="renamingSubId === st.id"
                    ref="subRenameInputRef"
                    v-model="renamingSubTitle"
                    class="sub-rename"
                    @keyup.enter="commitRenameSub(st)"
                    @keyup.esc="renamingSubId = ''"
                    @blur="commitRenameSub(st)"
                  />
                  <span v-else v-key-click class="sub-name" :title="t('todoSubRenameHint')" @dblclick="startRenameSub(st)" @keydown.f2.stop.prevent="startRenameSub(st)">{{ st.title }}</span>
                  <!-- 单独设截止日：子任务默认跟着父任务的日期，这里可单独覆盖 -->
                  <button class="act sub-act sub-due" :class="{ active: !!st.dueDate }"
                          :title="st.dueDate ? t('todoSubDueSet', { d: st.dueDate }) : t('todoSubDue')"
                          @click="toggleSubDue(st)">
                    <CalendarDays :size="11" />
                  </button>
                  <input v-if="subDueFor === st.id" type="date" class="sub-date"
                         :value="st.dueDate" @change="commitSubDue(st, ($event.target as HTMLInputElement).value)" @blur="subDueFor = ''" />
                  <!-- 上下移：单层 checklist 的排序只能靠拖，但子任务行太小拖不准 -->
                  <div class="sub-move">
                    <button class="act sub-act" :disabled="si === 0" @click="moveSub(st, -1)" :title="t('todoSubMoveUp')"><ChevronUp :size="11" /></button>
                    <button class="act sub-act" :disabled="si === subtasksOf(todo.id).length - 1" @click="moveSub(st, 1)" :title="t('todoSubMoveDown')"><ChevronDown :size="11" /></button>
                  </div>
                  <button class="act danger sub-del" @click="remove(st)" :title="t('todoDelete')"><Trash2 :size="11" /></button>
                </div>
                <div v-if="addingSubFor === todo.id" class="sub-add">
                  <input v-model="newSubTitle" class="modal-input sub-input" :placeholder="t('todoSubtaskPh')"
                         @keyup.enter="addSubtask(todo)" @blur="onSubInputBlur(todo)" />
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- 看板视图（4.3）-->
        <div v-else-if="viewMode === 'kanban'" class="kanban">
          <div
            v-for="st in KANBAN_STATUSES"
            :key="st"
            :class="['kanban-col', { 'drag-over': dragOverStatus === st }]"
            @dragover.prevent
            @dragenter="dragOverStatus = st"
            @dragleave="dragOverStatus = ''"
            @drop="moveToStatus(st)"
          >
            <div class="kanban-col-head">
              <span :class="['kanban-dot', 'st-' + st]"></span>
              <span class="kanban-col-title">{{ statusLabel(st) }}</span>
              <span class="kanban-count">{{ statusTodos(st).length }}</span>
            </div>
            <div class="kanban-cards">
              <div
                v-for="card in statusTodos(st)"
                :key="card.id"
                class="kanban-card"
                draggable="true"
                :title="t('todoKanbanEditHint')"
                @dblclick.stop="startEdit(card)"
                @dragstart="onDragStart(card.id, $event)"
                @dragend="onDragEnd"
              >
                <div class="kanban-card-top">
                  <span class="kanban-card-name">{{ card.title }}</span>
                  <span class="kanban-card-acts">
                    <button class="act" @click.stop="startEdit(card)" :title="t('todoEdit')"><Pencil :size="12" /></button>
                    <button class="act danger" @click.stop="remove(card)" :title="t('todoDelete')"><Trash2 :size="12" /></button>
                  </span>
                </div>
                <div class="kanban-card-meta">
                  <!-- 看板按状态看，卡片不写日期的话「今天哪些没做完」在列里完全看不出来 -->
                  <span v-if="card.dueDate" :class="['kanban-due', { overdue: isOverdue(card) }]">
                    {{ card.dueDate.slice(5) }}
                  </span>
                  <button class="status-pill" :class="'st-' + (card.status || 'todo')" @click.stop="cycleStatus(card)">{{ statusLabel(card.status) }}</button>
                  <i :class="['pri-dot', 'p-' + card.priority]"></i>
                  <template v-for="tg in parseTags(card.tags)" :key="tg">
                    <span v-key-click class="tag-chip" @click.stop="activeTag = tg">{{ tg }}</span>
                  </template>
                  <span v-if="subtasksOf(card.id).length"><ChevronRight :size="12" /> {{ subProgress(card.id).done }}/{{ subProgress(card.id).total }}</span>
                </div>
              </div>
              <div v-if="statusTodos(st).length === 0" class="kanban-empty">{{ t('todoKanbanEmpty') }}</div>
            </div>
          </div>
        </div>

        <!-- 四象限视图：重要×紧急，按优先级与截止日期自动归类；不做拖拽 -->
        <div v-else-if="viewMode === 'quadrant'" class="quadrant-view">
          <p class="quadrant-hint">{{ t('todoQuadrantHint') }}</p>
          <div class="quadrant">
            <div v-for="q in QUADRANTS" :key="q" class="quadrant-cell">
              <div class="quadrant-head">
                <span :class="['quadrant-dot', q.toLowerCase()]"></span>
                <span class="quadrant-title">{{ quadrantLabel(q) }}</span>
                <span class="quadrant-count">{{ todosInQuadrant(q).length }}</span>
              </div>
              <div class="quadrant-cards">
                <div
                  v-for="card in todosInQuadrant(q)"
                  :key="card.id"
                  class="kanban-card"
                >
                  <div class="kanban-card-top">
                    <span class="kanban-card-name">{{ card.title }}</span>
                    <span class="kanban-card-acts">
                      <button class="act" @click.stop="startEdit(card)" :title="t('todoEdit')"><Pencil :size="12" /></button>
                      <button class="act danger" @click.stop="remove(card)" :title="t('todoDelete')"><Trash2 :size="12" /></button>
                    </span>
                  </div>
                  <div class="kanban-card-meta">
                    <span v-if="card.dueDate" :class="['kanban-due', { overdue: isOverdue(card) }]">
                      {{ card.dueDate.slice(5) }}
                    </span>
                    <button class="status-pill" :class="'st-' + (card.status || 'todo')" @click.stop="cycleStatus(card)">{{ statusLabel(card.status) }}</button>
                    <i :class="['pri-dot', 'p-' + card.priority]"></i>
                    <template v-for="tg in parseTags(card.tags)" :key="tg">
                      <span v-key-click class="tag-chip" @click.stop="activeTag = tg">{{ tg }}</span>
                    </template>
                    <span v-if="subtasksOf(card.id).length"><ChevronRight :size="12" /> {{ subProgress(card.id).done }}/{{ subProgress(card.id).total }}</span>
                  </div>
                </div>
                <div v-if="todosInQuadrant(q).length === 0" class="quadrant-empty">{{ t('todoQuadrantEmpty') }}</div>
              </div>
            </div>
          </div>
        </div>

      </div>
    </div>

    <!-- 编辑弹窗 -->
    <QdModal v-model="showEdit" :title="t('todoEdit')" width="380px">
        <p v-if="formErr" class="form-err" role="alert">{{ formErr }}</p>
        <label>{{ t('todoTitle') }}</label>
        <input v-model="editTitle" class="modal-input" placeholder="..." @input="formErr = ''" />

        <label>{{ t('todoPriority') }}</label>
        <select v-model="editPriority" class="modal-input">
          <option value="none">{{ t('todoPriorityNone') }}</option>
          <option value="low">{{ t('todoPriorityLow') }}</option>
          <option value="medium">{{ t('todoPriorityMedium') }}</option>
          <option value="high">{{ t('todoPriorityHigh') }}</option>
        </select>

        <label>{{ t('todoStatus') }}</label>
        <select v-model="editStatus" class="modal-input">
          <option value="todo">{{ t('todoStatusTodo') }}</option>
          <option value="doing">{{ t('todoStatusDoing') }}</option>
          <option value="done">{{ t('todoStatusDone') }}</option>
        </select>

        <div class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoStartTime') }}</label>
            <input v-model="editStart" type="datetime-local" step="1" class="modal-input" />
          </div>
          <div>
            <label><Clock :size="12" /> {{ t('todoEndTime') }}</label>
            <input v-model="editEnd" type="datetime-local" step="1" class="modal-input" />
          </div>
        </div>

        <label><Bell :size="12" /> {{ t('todoReminderTime') }}</label>
        <input v-model="editReminder" type="datetime-local" step="1" class="modal-input" />

        <!-- 4.4 重复 -->
        <label>{{ t('todoRecurrence') }}</label>
        <select v-model="editRecKind" class="modal-input">
          <option v-for="rk in RECUR_KINDS" :key="rk" :value="rk">{{ t('todoRec_' + rk) }}</option>
        </select>
        <div v-if="editRecKind !== 'none'" class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoRecTime') }}</label>
            <input v-model="editRecTime" type="time" step="1" class="modal-input" />
          </div>
          <div v-if="editRecKind === 'weekly'">
            <label>{{ t('todoRecWeekdays') }}</label>
            <input v-model="editRecWeekdays" class="modal-input" :placeholder="t('todoRecWeekdaysPh')" />
          </div>
        </div>

        <!-- 重复待办：本次编辑的应用范围。普通待办不显示，保持原 UI。 -->
        <template v-if="editIsRecurring">
          <label>{{ t('todoOccScope') }}</label>
          <select v-model="editScope" class="modal-input">
            <option value="all">{{ t('todoOccAll') }}</option>
            <option value="thisOnly">{{ t('todoOccThisOnly') }}</option>
            <option value="fromNext">{{ t('todoOccFromNext') }}</option>
          </select>
          <p class="occ-hint">{{ t('todoOccHint') }}</p>
        </template>

        <!-- 4.2 标签 -->
        <label>{{ t('todoTags') }}</label>
        <input v-model="editTags" class="modal-input" :placeholder="t('todoTagsPh')" list="todo-tag-options" />

        <label>{{ t('todoDue') }}</label>
        <input v-model="editDueDate" type="date" class="modal-input" />

        <label>{{ t('todoNote') }}</label>
        <input v-model="editNote" class="modal-input" />

        <!-- 只在编辑已有待办时挂关联面板：新建弹窗没有 id，组件会自己显示禁用态。
             关联走独立 service，不进 UpdateTodo 的字段。 -->
        <EntityLinkPicker v-if="editing" src-type="todo" :src-id="editing.id" :src-label="editTitle" />

      <template #footer>
        <button class="qd-btn" @click="showEdit = false">{{ t('cancel') }}</button>
        <button class="qd-btn qd-btn-primary" @click="saveEdit">{{ t('save') }}</button>
      </template>
    </QdModal>

    <!-- 新增弹窗 -->
    <QdModal v-model="showCreate" :title="t('todoAdd')" width="380px">
        <p v-if="formErr" class="form-err" role="alert">{{ formErr }}</p>
        <label>{{ t('todoTitle') }}</label>
        <input v-model="cTitle" class="modal-input" :placeholder="t('todoAddPlaceholder')" @input="formErr = ''" />

        <label>{{ t('todoPriority') }}</label>
        <select v-model="cPriority" class="modal-input">
          <option value="none">{{ t('todoPriorityNone') }}</option>
          <option value="low">{{ t('todoPriorityLow') }}</option>
          <option value="medium">{{ t('todoPriorityMedium') }}</option>
          <option value="high">{{ t('todoPriorityHigh') }}</option>
        </select>

        <div class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoStartTime') }}</label>
            <input v-model="cStart" type="datetime-local" step="1" class="modal-input" />
          </div>
          <div>
            <label><Clock :size="12" /> {{ t('todoEndTime') }}</label>
            <input v-model="cEnd" type="datetime-local" step="1" class="modal-input" />
          </div>
        </div>

        <label><Bell :size="12" /> {{ t('todoReminderTime') }}</label>
        <input v-model="cReminder" type="datetime-local" step="1" class="modal-input" />

        <!-- 4.4 重复 -->
        <label>{{ t('todoRecurrence') }}</label>
        <select v-model="cRecKind" class="modal-input">
          <option v-for="rk in RECUR_KINDS" :key="rk" :value="rk">{{ t('todoRec_' + rk) }}</option>
        </select>
        <div v-if="cRecKind !== 'none'" class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoRecTime') }}</label>
            <input v-model="cRecTime" type="time" step="1" class="modal-input" />
          </div>
          <div v-if="cRecKind === 'weekly'">
            <label>{{ t('todoRecWeekdays') }}</label>
            <input v-model="cRecWeekdays" class="modal-input" :placeholder="t('todoRecWeekdaysPh')" />
          </div>
        </div>

        <!-- 4.2 标签 -->
        <label>{{ t('todoTags') }}</label>
        <input v-model="cTags" class="modal-input" :placeholder="t('todoTagsPh')" list="todo-tag-options" />

        <label>{{ t('todoDue') }}</label>
        <input v-model="cDueDate" type="date" class="modal-input" />

        <label>{{ t('todoNote') }}</label>
        <input v-model="cNote" class="modal-input" />

      <template #footer>
        <button class="qd-btn" @click="showCreate = false">{{ t('cancel') }}</button>
        <button class="qd-btn qd-btn-primary" @click="createTodo">{{ t('save') }}</button>
      </template>
    </QdModal>

    <!-- 标签联想：allTags 早就统计好了（标签筛选栏在用），输入时却全靠手打，
         拼错一个字就分裂成两个标签。原生 datalist 零依赖，够用。 -->
    <datalist id="todo-tag-options">
      <option v-for="tg in allTags" :key="tg" :value="tg" />
    </datalist>

    <ConfirmDialog
      :visible="showDelConfirm"
      :message="t('todoDelete') + '?'"
      @confirm="confirmDel"
      @cancel="showDelConfirm = false"
    />
    <ConfirmDialog
      :visible="showClearConfirm"
      :message="t('todoClearCompleted') + '?'"
      @confirm="confirmClear"
      @cancel="showClearConfirm = false"
    />
  </div>
</template>

<style scoped>
/* 页头已统一为 .qd-page-header（通栏），页容器不再留 padding */
.todo-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
  background: var(--color-bg-primary);
}
/* flex-shrink:0：右栏加了自定义时长输入与历史按钮后，页头中间变挤，
   若让 cal-nav 被压缩，其内容会溢出压到「今天」按钮与右侧 25 档按钮上（重叠） */
.cal-nav { display: flex; align-items: center; gap: var(--space-2); flex-shrink: 0; }
/* 页内搜索：只搜标题/备注/标签，跨日期；宽度与笔记页搜索一致。
   皮肤由 QdSearchBox 统一提供，这里只定宽——空间不足时优先压缩它（可窄到 120px），
   保住日历导航不被挤压。 */
.todo-search { width: 220px; flex-shrink: 1; min-width: 120px; margin-right: var(--space-3); }
/* 专注时长：一个带秒表图标的触发器 + 浮层，替代原来一排 25/45/60 裸数字 */
.focus-dur-wrap { position: relative; }
.focus-dur-trigger {
  display: inline-flex; align-items: center; gap: var(--space-2);
  height: 26px; padding: 0 var(--space-3);
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-secondary); border-radius: var(--radius-sm);
  font-size: var(--text-xs); font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.focus-dur-trigger:hover { color: var(--color-text-primary); border-color: var(--color-border-focus); }
.focus-dur-trigger.open { border-color: var(--color-accent-border); background: var(--color-accent-bg); color: var(--color-accent-ink); }
.fd-label { color: var(--color-text-muted); }
.focus-dur-trigger.open .fd-label { color: var(--color-accent-ink); }
.fd-value { color: var(--color-text-primary); font-weight: 600; font-variant-numeric: tabular-nums; }
.focus-dur-trigger.open .fd-value { color: var(--color-accent-ink); }
.fd-caret { opacity: 0.7; }
.focus-dur-pop {
  position: absolute; top: calc(100% + 6px); right: 0; z-index: var(--z-dropdown);
  display: flex; flex-direction: column; gap: 2px; min-width: 132px; padding: var(--space-2);
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md); box-shadow: var(--shadow-pop);
}
.fd-opt {
  padding: var(--space-2) var(--space-3); text-align: left;
  border: none; background: none; border-radius: var(--radius-xs); cursor: pointer;
  color: var(--color-text-secondary); font-size: var(--text-sm); font-family: inherit;
  font-variant-numeric: tabular-nums;
}
.fd-opt:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.fd-opt.active { color: var(--color-accent-ink); background: var(--color-accent-bg); font-weight: 600; }
.fd-custom {
  display: flex; align-items: center; gap: var(--space-2);
  margin-top: var(--space-1); padding-top: var(--space-2);
  border-top: 1px solid var(--color-border);
  font-size: var(--text-2xs); color: var(--color-text-muted);
}
.focus-dur-custom {
  width: 56px; height: 24px; padding: 0 var(--space-2); text-align: center;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); border-radius: var(--radius-sm);
  font-size: var(--text-xs); font-family: inherit; outline: none; -moz-appearance: textfield;
  transition: border-color var(--transition-fast);
}
.focus-dur-custom::-webkit-outer-spin-button,
.focus-dur-custom::-webkit-inner-spin-button { -webkit-appearance: none; margin: 0; }
.focus-dur-custom:focus { border-color: var(--color-border-focus-soft); }
.focus-hist-wrap { position: relative; display: flex; align-items: center; margin-left: var(--space-2); }
.focus-hist-btn { position: relative; }
.focus-hist-btn.active { color: var(--color-accent-ink); }
.focus-stats-btn { margin-left: var(--space-1); }
.focus-stats-btn.active { color: var(--color-accent-ink); }
/* 统计浮层只负责定位/滚动/投影，卡片外观由 FocusStatsPanel 自带的 .fsp 提供 */
.focus-stats-pop {
  position: absolute; top: calc(100% + 6px); right: 0; z-index: var(--z-dropdown); width: 320px;
  max-height: 420px; overflow: auto;
  border-radius: var(--radius-md); box-shadow: var(--shadow-pop);
}
.focus-hist-badge {
  position: absolute; top: -4px; right: -4px; min-width: 14px; height: 14px;
  padding: 0 3px; border-radius: var(--radius-full); background: var(--color-accent);
  color: var(--color-accent-text); font-size: var(--text-2xs); line-height: 14px; text-align: center; font-weight: 600;
}
.focus-hist-pop {
  position: absolute; top: calc(100% + 6px); right: 0; z-index: var(--z-dropdown); width: 264px;
  max-height: 320px; overflow: auto; padding: var(--space-3);
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md); box-shadow: var(--shadow-pop);
}
.fh-head {
  font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary);
  padding-bottom: var(--space-2); margin-bottom: var(--space-2);
  border-bottom: 1px solid var(--color-border);
}
.fh-empty { font-size: var(--text-xs); color: var(--color-text-muted); text-align: center; padding: var(--space-3) 0; }
.fh-row { display: flex; align-items: center; gap: var(--space-3); padding: 3px 0; font-size: var(--text-xs); }
.fh-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--color-text-disabled); flex-shrink: 0; }
.fh-dot.ok { background: var(--color-accent); }
.fh-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-text-primary); }
.fh-min { color: var(--color-text-muted); flex-shrink: 0; }
.fh-time { color: var(--color-text-muted); flex-shrink: 0; font-variant-numeric: tabular-nums; }
.cal-month { font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); min-width: 84px; text-align: center; }

.todo-error {
  margin: var(--space-5) var(--space-7) 0; padding: var(--space-4) var(--space-5); font-size: var(--text-sm);
  color: var(--color-danger-ink); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md); flex-shrink: 0;
}

.todo-body { display: flex; gap: var(--space-6); flex: 1; min-height: 0; padding: 14px var(--space-7) var(--space-6); }

/* 日历卡片 */
/* 日历是页面里唯一的大卡片：圆角与全站统一到 md，并给它一道内高光——
   不给阴影是因为它下面还有整列待办，卡片"浮起"会抢走列表的注意力。 */
.cal-card {
  width: 332px; flex-shrink: 0; display: flex; flex-direction: column;
  background: var(--color-card-bg);
  border-radius: var(--radius-md);
  border: 1px solid var(--card-border-color);
  box-shadow: var(--shadow-card);
  padding: var(--space-4);
  align-self: flex-start;
}
.cal-weekdays { display: grid; grid-template-columns: repeat(7, 1fr); margin-bottom: var(--space-1); }
.cal-weekday { text-align: center; font-size: var(--text-xs); color: var(--color-text-muted); font-weight: 500; padding-bottom: var(--space-1); }
/* 日期格高度钉死在 46：原来用 1fr 且卡片被 flex 拉伸，空月份时日历会撑到
   500px 高，六行数字之间各空出一大段，整块看起来像没加载完。
   日历高度跟着内容走（日历卡自身 align-self: flex-start），右侧列表该占多高占多高。 */
.cal-grid { display: grid; grid-template-columns: repeat(7, 1fr); grid-auto-rows: minmax(46px, auto); gap: var(--space-1); }
.cal-cell {
  position: relative;
  display: flex; flex-direction: column; align-items: center; justify-content: flex-start; gap: 3px;
  padding: 5px 0; border: none; outline: none; background: transparent;
  cursor: pointer; font-family: inherit; border-radius: var(--radius-md);
  transition: background var(--transition-fast);
}
.cal-cell:hover { background: var(--color-bg-hover); }
.cal-cell.drop-over {
  background: var(--color-accent-bg);
  outline: 1.5px dashed var(--color-accent-border);
  outline-offset: -2px;
}
.cal-cell.out-month { opacity: 0.45; }
.cal-day {
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 50%;
  font-size: var(--text-sm); font-weight: 500; color: var(--color-text-primary);
  transition: color var(--transition-fast), background var(--transition-fast), box-shadow var(--transition-fast);
}
.cal-cell.out-month .cal-day { color: var(--color-text-muted); }
.cal-cell.is-today .cal-day { color: var(--color-accent-ink); border: 1px solid var(--color-accent); }
/* 选中态用半透明主题色 + 一圈细描边：深色下实心亮蓝对比过强，压得整块日历发刺 */
.cal-cell.is-selected .cal-day {
  background: var(--color-select-bg);
  color: var(--color-accent-ink);
  font-weight: 600;
  box-shadow: inset 0 0 0 1px var(--color-accent-border);
}
.cal-cell.is-selected.is-today .cal-day { box-shadow: inset 0 0 0 1px var(--color-accent-muted); }
.cal-marks { display: flex; align-items: center; gap: 3px; height: 7px; flex-wrap: wrap; justify-content: center; }
/* 圆点带优先级配色（none 灰 / low 蓝 / medium 橙 / high 红），4px 太小几乎只看得出"有个点"，
   高优再放大 1px，颜色之外多一个尺寸信号 */
.dot { width: 5px; height: 5px; border-radius: 50%; display: inline-block; }
.dot.p-high { width: 6px; height: 6px; }
.dot.done { opacity: 0.3; }
.cal-bell { color: var(--color-accent-ink); }
.cal-more { font-size: var(--text-2xs); color: var(--color-text-muted); font-style: normal; }

.unscheduled-chip {
  display: flex; align-items: center; gap: var(--space-3); margin-top: var(--space-3);
  padding: var(--space-4) 10px; border: 1px solid var(--color-border); background: var(--color-bg-primary);
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: var(--text-sm);
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.unscheduled-chip:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.unscheduled-chip.active { background: var(--color-accent-bg); color: var(--color-accent-ink); border-color: var(--color-accent-border); }
.chip-count { margin-left: auto; font-style: normal; background: var(--color-bg-tertiary); border-radius: var(--radius-md); padding: 0 var(--space-3); font-size: var(--text-xs); }

/* 右侧面板 */
.todo-panel { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.panel-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-4); }
/* 日期是右栏的主标题：加粗一档 + 今日标签，与下方 12px 的视图切换拉开层级。
   字号维持 15px（与页头标题同级），不越级到 18px 的「区块标题」档。 */
.panel-head-left { display: flex; align-items: center; gap: var(--space-3); min-width: 0; }
.panel-date {
  font-size: var(--text-lg); font-weight: 700; letter-spacing: -0.01em;
  color: var(--color-text-primary); white-space: nowrap;
}
.panel-today-tag {
  flex-shrink: 0; padding: 1px 7px; border-radius: var(--radius-full);
  background: var(--color-accent-bg); border: 1px solid var(--color-accent-border);
  color: var(--color-accent-ink); font-size: var(--text-2xs); font-weight: 600; line-height: 15px;
}
.panel-remaining { font-size: var(--text-sm); color: var(--color-text-muted); }

.panel-head-right { display: flex; align-items: center; gap: var(--space-3); }

/* 面板右上角「更多」：低频操作（清除已完成 / 测试提醒）收在这里，底部不再挂灰字 */
.panel-more-wrap { position: relative; display: flex; align-items: center; }
.panel-more-wrap .qd-icon-btn.active { color: var(--color-accent-ink); }
.panel-more-pop {
  position: absolute; top: calc(100% + 6px); right: 0; z-index: var(--z-dropdown);
  display: flex; flex-direction: column; gap: 2px; width: 208px; padding: var(--space-2);
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md); box-shadow: var(--shadow-pop);
}
.pm-item {
  display: flex; align-items: center; gap: var(--space-3); width: 100%;
  padding: var(--space-3); text-align: left;
  border: none; background: none; border-radius: var(--radius-xs); cursor: pointer;
  color: var(--color-text-secondary); font-size: var(--text-sm); font-family: inherit;
}
.pm-item:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.pm-item.danger:hover:not(:disabled) { color: var(--color-danger-ink); background: var(--color-danger-bg); }
.pm-item:disabled { color: var(--color-text-disabled); cursor: default; }
.pm-hint {
  margin: var(--space-1) 0 0; padding: var(--space-2) var(--space-3) 0;
  border-top: 1px solid var(--color-border);
  font-size: var(--text-2xs); line-height: 1.6; color: var(--color-text-muted);
}

/* 视图切换（列表 / 看板）*/
.view-toggle { display: inline-flex; border: 1px solid var(--color-border); border-radius: var(--radius-md); overflow: hidden; }
.vt-btn {display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); 
  height: 22px; padding: 0 9px; border: none; background: var(--color-bg-tertiary);
  color: var(--color-text-muted); font-size: var(--text-xs); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.vt-btn + .vt-btn { border-left: 1px solid var(--color-border); }
/* 视图切换是"同一组里的两档"，选中档用淡底 + 强调色文字即可；
   刷成实心蓝会和页面里真正的主操作（新建待办）抢同一个视觉权重。 */
.vt-btn.active {
  background: var(--color-accent-bg);
  color: var(--color-accent-ink);
  font-weight: 600;
}

/* 状态徽章（4.3）*/
.status-pill {
  font-size: var(--text-2xs); padding: 0 7px; line-height: 16px; border-radius: var(--radius-md); cursor: pointer;
  border: 1px solid transparent; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.status-pill.st-todo { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
.status-pill.st-doing { background: var(--color-warning-bg); color: var(--color-warning-ink); border-color: var(--color-warning-border); }
.status-pill.st-done { background: var(--color-accent-bg); color: var(--color-accent-ink); border-color: var(--color-accent-border); }

/* 子任务（4.1）*/
.todo-item { flex-wrap: wrap; }
.sub-progress { display: flex; align-items: center; gap: var(--space-4); margin-bottom: var(--space-2); }
.sub-progress-bar { flex: 1; height: 4px; border-radius: var(--radius-xs); background: var(--color-bg-tertiary); overflow: hidden; }
.sub-progress-fill { height: 100%; background: var(--color-accent); border-radius: var(--radius-xs); transition: width var(--transition-fast); }
.sub-progress-text { font-size: var(--text-xs); color: var(--color-text-muted); font-variant-numeric: tabular-nums; white-space: nowrap; }
.subtasks { flex-basis: 100%; margin: var(--space-3) 0 var(--space-1) 26px; display: flex; flex-direction: column; gap: 3px; }
.sub-item { display: flex; align-items: center; gap: 7px; padding: 3px 0; }
.sub-item.done .sub-name { text-decoration: line-through; color: var(--color-text-disabled); }
.sub-check { width: 15px; height: 15px; border-radius: var(--radius-xs); }
.sub-name { flex: 1; font-size: var(--text-sm); color: var(--color-text-secondary); min-width: 0; }
.sub-del { opacity: 0; width: 22px; height: 22px; }
.sub-item:hover .sub-del { opacity: 1; }
/* 行内改名：与 .sub-name 同字号，去掉输入框边框只留底色，避免行高跳动 */
.sub-rename {
  flex: 1; min-width: 0; height: 20px; padding: 0 5px;
  border: 1px solid var(--color-accent); border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: var(--text-sm); font-family: inherit; outline: none;
}
/* 上下移：hover 才出现，和删除按钮同一套作息 */
.sub-move { display: flex; align-items: center; gap: 1px; opacity: 0; }
.sub-item:hover .sub-move { opacity: 1; }
.sub-act { width: 18px; height: 18px; }
.sub-act:disabled { color: var(--color-text-disabled); cursor: default; }
.sub-add { margin-top: var(--space-1); }
.sub-input { height: 28px; font-size: var(--text-sm); }

/* 单独设截止日：日期按钮与上下移同一套 hover 作息；已设日期的常显一个小色点提示 */
.sub-due { opacity: 0; }
.sub-item:hover .sub-due { opacity: 1; }
.sub-due.active { opacity: 1; color: var(--color-accent-ink); }
.sub-date {
  height: 20px; padding: 0 var(--space-2); border: 1px solid var(--color-accent);
  border-radius: var(--radius-xs); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: var(--text-xs); font-family: inherit; outline: none;
}

/* 看板视图（4.3）*/
.kanban { flex: 1; display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--space-3); min-height: 0; overflow: hidden; }
.kanban-col {
  display: flex; flex-direction: column; min-height: 0; height: 100%;
  background: var(--color-card-bg); border-radius: var(--radius-lg);
  border: 1px solid var(--card-border-color); padding: var(--space-3);
}
.kanban-col-head { display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-2); flex-shrink: 0; }
.kanban-dot { width: 8px; height: 8px; border-radius: 50%; }
.kanban-dot.st-todo { background: var(--color-text-muted); }
.kanban-dot.st-doing { background: var(--color-warning); }
.kanban-dot.st-done { background: var(--color-accent); }
.kanban-col-title { font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.kanban-count { margin-left: auto; font-size: var(--text-xs); color: var(--color-text-muted); background: var(--color-bg-tertiary); border-radius: var(--radius-md); padding: 0 7px; }
.kanban-cards { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.kanban-card {
  padding: 9px 11px; background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md); cursor: grab; transition: background var(--transition-fast);
  min-height: 46px;
}
.kanban-card:hover { background: var(--color-bg-hover); }
.kanban-card:active { cursor: grabbing; }
.kanban-card-name { font-size: var(--text-md); color: var(--color-text-primary); }
.kanban-card-meta { margin-top: 5px; display: flex; gap: var(--space-3); flex-wrap: wrap; align-items: center; }
/* 卡片上的截止日：逾期改用警示色，扫一眼就知道哪张拖后腿了 */
.kanban-due { font-size: var(--text-2xs); color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
.kanban-due.overdue { color: var(--color-danger-ink); font-weight: 500; }
/* 拖拽落点：整列描边提示，比只高亮卡片更容易判断"会落到哪一列" */
.kanban-col.drag-over { border-color: var(--color-accent); background: var(--color-accent-bg); }
.kanban-empty {
  flex: 1; display: flex; align-items: center; justify-content: center;
  text-align: center; font-size: var(--text-xs); color: var(--color-text-muted); padding: var(--space-5) 0;
}

/* 四象限视图：2×2 网格，样式沿用看板 token，不引入硬编码色值 */
.quadrant-view { flex: 1; display: flex; flex-direction: column; min-height: 0; overflow: hidden; }
.quadrant-hint { margin: 0 0 var(--space-2); font-size: var(--text-xs); color: var(--color-text-muted); flex-shrink: 0; }
.quadrant { flex: 1; display: grid; grid-template-columns: repeat(2, 1fr); grid-template-rows: repeat(2, 1fr); gap: var(--space-3); min-height: 0; }
.quadrant-cell {
  display: flex; flex-direction: column; min-height: 0;
  background: var(--color-card-bg); border: 1px solid var(--card-border-color);
  border-radius: var(--radius-lg); padding: var(--space-3);
}
.quadrant-head { display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2); flex-shrink: 0; }
.quadrant-dot { width: 8px; height: 8px; border-radius: 50%; }
.quadrant-dot.q1 { background: var(--color-danger); }
.quadrant-dot.q2 { background: var(--color-warning); }
.quadrant-dot.q3 { background: var(--color-accent); }
.quadrant-dot.q4 { background: var(--color-text-muted); }
.quadrant-title { font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.quadrant-count { margin-left: auto; font-size: var(--text-xs); color: var(--color-text-muted); background: var(--color-bg-tertiary); border-radius: var(--radius-md); padding: 0 7px; }
.quadrant-cards { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.quadrant-empty { flex: 1; display: flex; align-items: center; justify-content: center; text-align: center; font-size: var(--text-xs); color: var(--color-text-muted); padding: var(--space-5) 0; }
/* 象限卡片复用看板卡片，但不拖拽，光标不提示可抓取 */
.quadrant .kanban-card, .quadrant .kanban-card:active { cursor: default; }

.tag-filter { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-bottom: var(--space-3); }
.tag-fbtn {display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); 
  font-size: var(--text-xs); height: 22px; padding: 0 9px; border-radius: var(--radius-full); cursor: pointer;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-muted); font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.tag-fbtn:hover { color: var(--color-text-primary); border-color: var(--color-border-focus); }
.tag-fbtn.active { background: var(--color-accent-bg); color: var(--color-accent-ink); border-color: var(--color-accent-border); }
.add-fab {
  width: 30px; height: 30px; flex-shrink: 0; display: flex; align-items: center; justify-content: center;
  background: var(--color-accent); color: var(--color-accent-text); border: none; border-radius: var(--radius-md);
  cursor: pointer; transition: background var(--transition-fast);
}
.add-fab:hover { background: var(--color-accent-hover); }

/* 行内快速添加：本页最高频的入口，做成"真实的输入控件"——
   实心卡片底 + 1px 描边 + 聚焦时的淡色外环；回车提示贴在右侧。 */
.quick-add {
  display: flex; align-items: center; gap: var(--space-3);
  margin: 0 0 var(--space-3);
  padding: 0 var(--space-4);
  height: 38px;
  background: var(--color-card-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  transition: border-color var(--transition-fast), background-color var(--transition-fast), box-shadow var(--transition-fast);
}
.quick-add:hover { border-color: var(--color-border-focus); }
.quick-add:focus-within { border-color: var(--color-accent); box-shadow: 0 0 0 3px var(--color-accent-bg); }
.quick-add-icon { color: var(--color-text-muted); flex-shrink: 0; }
.quick-add-input {
  flex: 1; min-width: 0;
  border: 0; outline: 0; background: transparent;
  font: inherit; font-size: var(--text-sm); color: var(--color-text-primary);
}
.quick-add-input::placeholder { color: var(--color-text-muted); }
.quick-add-kbd {
  flex-shrink: 0; min-width: 18px; padding: 0 4px; line-height: 16px; text-align: center;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  font-family: inherit; font-size: var(--text-2xs); color: var(--color-text-muted);
  transition: color var(--transition-fast), border-color var(--transition-fast);
}
.quick-add:focus-within .quick-add-kbd { color: var(--color-text-muted); border-color: var(--color-accent-border); }

.todo-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
/* 空态占满可用高度并居中：比顶部一小撮灰字更像"状态"，而不是加载残骸 */
.todo-empty {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: var(--space-5); padding: var(--space-6) var(--space-4); text-align: center;
  color: var(--color-text-muted);
}
.empty-hero { display: flex; flex-direction: column; align-items: center; }
/* 搜索无结果时的空态图标：同 .qd-empty-icon 的 44 方底。
   原先只有 opacity .4——把细线图标洗成一片灰雾，看不出是日历还是加载中。 */
.empty-icon {
  width: 44px;
  height: 44px;
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: inset 0 1px 0 var(--color-hairline);
  margin-bottom: var(--space-2);
}
.empty-art { width: 118px; height: 96px; }
/* 微动效：盆栽轻轻摆动（幅度很小，只是让空态"活着"），减弱动效偏好下停掉 */
.art-sway { transform-origin: 60px 60px; animation: art-sway 6.5s ease-in-out infinite; }
@keyframes art-sway {
  0%, 100% { transform: rotate(-1.8deg); }
  50% { transform: rotate(1.8deg); }
}
.empty-title { margin: var(--space-2) 0 0; font-size: var(--text-md); color: var(--color-text-secondary); }
.empty-hint { margin: var(--space-2) 0 0; font-size: var(--text-xs); color: var(--color-text-muted); }
.empty-insights {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(176px, 1fr));
  gap: var(--space-3); width: 100%; max-width: 460px;
}
.empty-card {
  text-align: left; padding: var(--space-3) var(--space-4);
  background: var(--color-card-bg); border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
}
.empty-card-title {
  display: flex; align-items: center; gap: var(--space-2);
  margin-bottom: var(--space-2); font-size: var(--text-xs); color: var(--color-text-muted);
}
.empty-card-row { display: flex; align-items: center; gap: var(--space-3); padding: 3px 0; }
.ecr-title {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-sm); color: var(--color-text-secondary);
}
.ecr-date {
  flex-shrink: 0; font-size: var(--text-2xs); color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
@media (prefers-reduced-motion: reduce) {
  .art-sway { animation: none; }
}

.todo-item {
  position: relative;
  display: flex; align-items: center; gap: var(--space-3); padding: 10px var(--space-5);
  background: var(--color-card-bg); border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
/* 重复待办完成范围小菜单：贴着勾选框弹出来，纯列表不做遮罩/浮层卡片堆叠 */
.complete-scope {
  position: absolute; left: var(--space-4); top: calc(100% - var(--space-1)); z-index: var(--z-dropdown);
  display: flex; flex-direction: column; min-width: 12em; padding: var(--space-1);
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); box-shadow: var(--shadow-md);
}
.complete-scope-title { padding: var(--space-1) var(--space-2); font-size: var(--text-2xs); color: var(--color-text-muted); }
.complete-scope button {
  padding: var(--space-2); text-align: left; font-size: var(--text-xs); font-family: inherit;
  color: var(--color-text-secondary); background: none; border: none; border-radius: var(--radius-xs); cursor: pointer;
}
.complete-scope button:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
/* 自定义改期的行内日期框：放在操作行下方，不额外占一行高度 */
.snooze-date { position: absolute; right: var(--space-5); top: calc(100% - var(--space-1)); z-index: var(--z-dropdown); }
.snooze-date input { font-size: var(--text-xs); font-family: inherit; padding: var(--space-1) var(--space-2); color: var(--color-text-primary); background: var(--color-bg-primary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); }
.todo-item:hover { background: var(--color-bg-hover); }
/* 深链定位后的短暂高亮：一天可能有十几条，不给个落点眼睛找不到滚到哪了 */
.todo-item.todo-flash {
  border-color: var(--color-accent);
  animation: todo-flash 1.8s ease-out 1;
}
@keyframes todo-flash {
  0%, 55% { background: var(--color-bg-hover); }
  100% { background: var(--color-bg-secondary); }
}
.todo-item.done .todo-name { text-decoration: line-through; color: var(--color-text-disabled); }
.todo-item.overdue { border: 1px solid var(--color-danger-border); }
.check {
  flex-shrink: 0; width: 18px; height: 18px; border: 1.5px solid var(--color-border);
  background: transparent; border-radius: var(--radius-sm); cursor: pointer; display: flex;
  align-items: center; justify-content: center; color: var(--color-accent-text); transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.check:hover { border-color: var(--color-accent); }
.check.checked { background: var(--color-accent); border-color: var(--color-accent); }
.pri-dot { flex-shrink: 0; width: 8px; height: 8px; border-radius: 50%; }
.todo-main { flex: 1; min-width: 0; }
.todo-name { font-size: var(--text-md); color: var(--color-text-primary); }
.todo-meta { font-size: var(--text-sm); color: var(--color-text-muted); margin-top: 3px; display: flex; gap: var(--space-3); flex-wrap: wrap; align-items: center; }
.overdue-text { color: var(--color-danger-ink); }
.meta-pill { display: inline-flex; align-items: center; gap: 3px; color: var(--color-text-muted); }
.meta-pill.sent { color: var(--color-text-muted); }
.done-pill { color: var(--color-accent-ink); }
.todo-note { color: var(--color-text-muted); }
.tag-chip {
  font-size: var(--text-2xs); padding: 0 7px; line-height: 16px; border-radius: var(--radius-md);
  background: var(--color-accent-bg); color: var(--color-accent-ink);
  cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.tag-chip:hover { filter: brightness(1.15); }
.rec-pill { color: var(--color-text-secondary); }
.todo-actions { display: flex; gap: var(--space-1); opacity: 0; transition: opacity var(--transition-fast); }
.todo-item:hover .todo-actions { opacity: 1; }
.act {
  width: 26px; height: 26px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-muted);
  border-radius: var(--radius-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.act:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.act.danger:hover { color: var(--color-danger-ink); background: var(--color-danger-bg); }
.focus-act { font-size: var(--text-xs); font-weight: 600; color: var(--color-accent-ink); min-width: 34px; }
.focus-act.running { background: var(--color-accent-bg); color: var(--color-accent-ink); }

/* 优先级配色 */
.p-none { background: var(--color-text-muted); }
.p-low { background: var(--color-accent); }
.p-medium { background: var(--color-warning); }
.p-high { background: var(--color-danger); }

/* 弹窗 */
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }
.modal-grid input[type="datetime-local"] { width: 100%; box-sizing: border-box; }
.occ-hint { margin: var(--space-1) 0 0; font-size: var(--text-xs); color: var(--color-text-muted); line-height: 1.5; }

/* 弹窗内错误条：页面级 .todo-error 在遮罩下面，弹窗必须自带一处反馈。
   与 SchedulePage / MonitorPage 的 .form-err 同口径。 */
.form-err { margin: 0 0 var(--space-3); padding: var(--space-2) var(--space-3); font-size: var(--text-sm); color: var(--color-danger-ink); background: var(--color-danger-bg); border: 1px solid var(--color-danger-border); border-radius: var(--radius-sm); }

/* 看板卡片：标题与最小操作集同一行。操作按钮常驻但弱化，聚焦时补回对比度，
   避免 hover 才显形导致键盘用户 Tab 到透明按钮上（横向共性里的可达性问题）。 */
.kanban-card-top { display: flex; align-items: flex-start; gap: var(--space-2); }
.kanban-card-name { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.kanban-card-acts { display: inline-flex; gap: 2px; flex-shrink: 0; opacity: 0.5; }
.kanban-card:hover .kanban-card-acts, .kanban-card:focus-within .kanban-card-acts { opacity: 1; }
.kanban-card-acts .act { width: 20px; height: 20px; }

/* 窄窗口 / 高 DPI 缩放适配：800px 窗口主区仅约 590 CSS px，332px 日历 +
   右栏工具条会把新建按钮挤出 overflow:hidden。与全局页头 900px 断点对齐。 */
@media (max-width: 900px) {
  .todo-body { gap: var(--space-4); padding: var(--space-3) var(--space-5); }
  .cal-card { width: 240px; padding: var(--space-3); }
  .panel-head { flex-wrap: wrap; row-gap: var(--space-2); }
  .panel-head-right { flex-wrap: wrap; }
  .panel-remaining { display: none; } /* 剩余数信息密度最低，窄屏先让位于操作按钮 */
}
@media (max-width: 720px) {
  .todo-body { flex-direction: column; }
  .cal-card { width: 100%; align-self: stretch; }
}
</style>
