<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import ConfirmDialog from './ConfirmDialog.vue'
import {
  CreateTodo, ListTodos, UpdateTodo, ToggleTodo, DeleteTodo, ClearCompletedTodos,
  CreateSubtask, SetTodoStatus, SetTodoSort,
} from '../../bindings/quickdock/services/todo/todoservice'
import {
  SendTestNotification, NotifyFocusComplete, NotifyFocusStart,
} from '../../bindings/quickdock/services/appservice'
import { pendingOpenTodoId } from '../composables/bridge'
// 待办的增删改都会汇流到 refresh()，在那里顺手把侧边栏角标拉齐（不用等 60s 轮询）
import { refreshNavBadges } from '../composables/useNavBadges'
import { matchesTextOrPinyin } from '../utils/pinyin'
import {
  Plus, Check, Trash2, Pencil, ChevronLeft, ChevronRight, CalendarDays, Bell, Clock, BellRing,
  RefreshCw, ListTree, Timer, X, Search, ChevronUp, ChevronDown,
} from '@lucide/vue'

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
const focusTodoId = ref('')           // 正在专注的待办 id（''=无）
const focusRemaining = ref(0)         // 剩余秒数
const focusRunning = ref(false)       // 是否在倒计时
const focusTotalSec = ref(0)          // 本轮目标秒数：区分「到点完成」与「中途放弃」
let focusTimer: number | null = null  // setInterval id（只负责刷新显示，不负责计时）
let focusEndAt = 0                    // 运行中的截止时间戳（ms）；0 = 未运行

function focusTitle(id: string): string {
  return todos.value.find(t => t.id === id)?.title || ''
}

function saveFocus() {
  try {
    if (!focusTodoId.value) {
      localStorage.removeItem(FOCUS_KEY)
      return
    }
    localStorage.setItem(FOCUS_KEY, JSON.stringify({
      todoId: focusTodoId.value,
      endAt: focusEndAt,
      remaining: focusRemaining.value,
      running: focusRunning.value,
      total: focusTotalSec.value,
      minutes: focusMinutes.value,
    }))
  } catch { /* localStorage 不可用（隐私模式等）时只影响跨页恢复 */ }
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
  const title = focusTitle(focusTodoId.value) || t('focusTimerTitle')
  try { NotifyFocusComplete(title, Math.round(focusTotalSec.value / 60) || 1) } catch (e) { logErr('TodoPage', e) }
  stopFocusTimer()
  focusEndAt = 0
  focusTodoId.value = ''
  focusRunning.value = false
  focusRemaining.value = 0
  focusTotalSec.value = 0
  saveFocus()
}

function resumeFocus() {
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
  const title = focusTitle(focusTodoId.value)
  stopFocusTimer()
  focusEndAt = 0
  if (finished && focusTodoId.value) {
    try { NotifyFocusComplete(title || t('focusTimerTitle'), Math.round(total / 60) || 1) } catch (e) { logErr('TodoPage', e) }
  }
  focusTodoId.value = ''
  focusRunning.value = false
  focusRemaining.value = 0
  focusTotalSec.value = 0
  saveFocus()
}
function pauseFocus() {
  if (focusRunning.value) {
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
      running?: boolean; total?: number; minutes?: number
    }
    if (!s?.todoId) return
    focusTodoId.value = s.todoId
    if (s.minutes) focusMinutes.value = s.minutes
    focusTotalSec.value = s.total || 0
    focusRemaining.value = s.remaining || 0
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

// ---- 4.2 标签筛选 ----
const activeTag = ref('')
const allTags = computed(() => {
  const set = new Set<string>()
  for (const x of todos.value) for (const tg of parseTags(x.tags as unknown as string)) set.add(tg)
  return Array.from(set).sort()
})

// ---- 4.3 状态 + 看板视图 ----
const viewMode = ref<'list' | 'kanban'>('list')
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
  const title = newSubTitle.value.trim()
  if (!title) return
  try {
    await unwrap(await CreateSubtask(parent.id, title))
    newSubTitle.value = ''
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
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
const dragId = ref('')
const dragOverStatus = ref('')

function onDragStart(id: string) {
  dragId.value = id
  dragOverStatus.value = ''
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
      x.dueDate === d || (d === todayStr.value && x.dueDate !== '' && x.dueDate < d && !x.done)
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
      refreshNavBadges()
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
  cTitle.value = ''
  cPriority.value = 'none'
  // 未指定日期时默认落在今天，避免新建的待办静默掉进「未排期」
  cDueDate.value = selectedDate.value || todayStr.value
  cNote.value = ''
  cStart.value = ''
  cEnd.value = ''
  cReminder.value = ''
  cTags.value = ''
  cRecKind.value = 'none'
  cRecTime.value = '09:00'
  cRecWeekdays.value = ''
  showCreate.value = true
}
async function createTodo() {
  const title = cTitle.value.trim()
  if (!title) return
  try {
    await unwrap(await CreateTodo(
      title, cPriority.value, cDueDate.value, cNote.value,
      fromInput(cStart.value), fromInput(cEnd.value), fromInput(cReminder.value),
      recurrenceToJSON({ kind: cRecKind.value, timeOfDay: cRecTime.value, weekdays: cRecWeekdays.value }),
      tagsTextToJSON(cTags.value),
    ))
    showCreate.value = false
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
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

function toInput(s: string): string { return s ? s.replace(' ', 'T') : '' }
function fromInput(s: string): string { return s ? s.replace('T', ' ') : '' }

function startEdit(todo: Todo) {
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
  showEdit.value = true
}
async function saveEdit() {
  if (!editing.value) return
  const title = editTitle.value.trim()
  if (!title) return
  try {
    await unwrap(await UpdateTodo(
      editing.value.id, title, editPriority.value, editDueDate.value, editNote.value,
      fromInput(editStart.value), fromInput(editEnd.value), fromInput(editReminder.value),
      recurrenceToJSON({ kind: editRecKind.value, timeOfDay: editRecTime.value, weekdays: editRecWeekdays.value }),
      tagsTextToJSON(editTags.value),
      editStatus.value,
    ))
    showEdit.value = false
    editing.value = null
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

async function toggle(todo: Todo) {
  try { await unwrap(await ToggleTodo(todo.id)); await refresh() } catch (e) { error.value = getErrorMessage(e) }
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

onMounted(() => {
  selectedDate.value = todayStr.value
  refresh()
  // 专注计时可能在上次离开本页时仍在跑（状态在 localStorage 里），先接上再渲染
  restoreFocus()
  // id 可能在组件创建之前就已写入（先切页、后 mount），watch 不会回溯触发
  if (pendingOpenTodoId.value) void focusPendingTodo()
})
onUnmounted(() => { if (highlightTimer) clearTimeout(highlightTimer) })
</script>

<template>
  <div class="todo-page">
    <!-- 顶部栏 -->
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('todoTitle') }}</h2>
        <span class="qd-page-sub">{{ t('todoSubtitle') }}</span>
      </div>
      <div class="qd-page-fill">
        <div class="qd-input-wrap todo-search">
          <Search :size="14" />
          <input v-model="searchQuery" class="qd-input ts-input" :placeholder="t('todoSearchPh')" />
          <button v-if="searchQuery" class="ts-clear" @click="searchQuery = ''"><X :size="13" /></button>
        </div>
        <div class="cal-nav">
          <button class="qd-icon-btn" @click="prevMonth" :title="t('prevMonth')"><ChevronLeft :size="16" /></button>
          <span class="cal-month">{{ monthLabel }}</span>
          <button class="qd-icon-btn" @click="nextMonth" :title="t('nextMonth')"><ChevronRight :size="16" /></button>
          <button class="qd-btn qd-btn-ghost" @click="goToday">{{ t('todoToday') }}</button>
        </div>
      </div>
      <div class="qd-page-actions">
        <div class="focus-dur" :title="t('todoFocusDur')">
          <button v-for="d in FOCUS_DEFAULTS" :key="d"
            :class="['focus-dur-btn', { active: focusMinutes === d }]"
            @click="focusMinutes = d">
            {{ d }}
          </button>
        </div>
      </div>
    </div>

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
            :class="['cal-cell', { 'out-month': !c.inMonth, 'is-today': c.isToday, 'is-selected': c.isSelected }]"
            @click="selectDate(c.date)"
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
          <span class="panel-date">{{ selectedLabel }}</span>
          <div class="panel-head-right">
            <div class="view-toggle">
              <button :class="['vt-btn', { active: viewMode === 'list' }]" @click="viewMode = 'list'">{{ t('todoViewList') }}</button>
              <button :class="['vt-btn', { active: viewMode === 'kanban' }]" @click="viewMode = 'kanban'">{{ t('todoViewKanban') }}</button>
            </div>
            <span class="panel-remaining">{{ t('todoRemaining', { count: remainingCount }) }}</span>
            <button class="add-fab" @click="openCreate" :title="t('todoAdd')"><Plus :size="16" /></button>
          </div>
        </div>

        <!-- 列表视图 -->
        <template v-if="viewMode === 'list'">
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
              <CalendarDays :size="28" class="empty-icon" />
              <template v-if="searching">
                <p>{{ t('noMatchItems') }}</p>
                <p class="empty-hint">{{ t('tryOtherKeywords') }}</p>
              </template>
              <template v-else>
                <p>{{ t('todoNoTodosThisDay') }}</p>
                <p class="empty-hint">{{ t('todoClickDayHint') }}</p>
              </template>
            </div>
            <div
              v-for="todo in selectedTodos"
              :key="todo.id"
              :data-todo-id="todo.id"
              :class="['todo-item', { done: todo.done, overdue: isOverdue(todo), 'todo-flash': highlightTodoId === todo.id }]"
            >
              <button class="check" :class="{ checked: todo.done }" @click="toggle(todo)">
                <Check v-if="todo.done" :size="13" />
              </button>
              <i :class="['pri-dot', 'p-' + todo.priority]"></i>
              <div class="todo-main">
                <span class="todo-name">{{ todo.title }}</span>
                <div class="todo-meta">
                  <!-- 搜索是跨日期的，列表必须标出这一条属于哪天，否则只有标题看不出落点 -->
                  <span v-if="searching && todo.dueDate" class="meta-pill">{{ todo.dueDate }}</span>
                  <span v-if="isOverdue(todo)" class="overdue-text">{{ t('todoOverdue') }}</span>
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
                    <span class="tag-chip" @click.stop="activeTag = tg">{{ tg }}</span>
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
                <button class="act" @click="startEdit(todo)" :title="t('todoEdit')"><Pencil :size="13" /></button>
                <button class="act danger" @click="remove(todo)" :title="t('todoDelete')"><Trash2 :size="13" /></button>
              </div>

              <!-- 4.1 子任务（单层级 checklist）-->
              <div v-if="subtasksOf(todo.id).length || addingSubFor === todo.id" class="subtasks">
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
                  <span v-else class="sub-name" :title="t('todoSubRenameHint')" @dblclick="startRenameSub(st)">{{ st.title }}</span>
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
                         @keyup.enter="addSubtask(todo)" @blur="toggleSubInput(todo)" />
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- 看板视图（4.3）-->
        <div v-else class="kanban">
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
                @dragstart="onDragStart(card.id)"
              >
                <span class="kanban-card-name">{{ card.title }}</span>
                <div class="kanban-card-meta">
                  <!-- 看板按状态看，卡片不写日期的话「今天哪些没做完」在列里完全看不出来 -->
                  <span v-if="card.dueDate" :class="['kanban-due', { overdue: isOverdue(card) }]">
                    {{ card.dueDate.slice(5) }}
                  </span>
                  <button class="status-pill" :class="'st-' + (card.status || 'todo')" @click.stop="cycleStatus(card)">{{ statusLabel(card.status) }}</button>
                  <i :class="['pri-dot', 'p-' + card.priority]"></i>
                  <template v-for="tg in parseTags(card.tags)" :key="tg">
                    <span class="tag-chip" @click.stop="activeTag = tg">{{ tg }}</span>
                  </template>
                  <span v-if="subtasksOf(card.id).length" class="sub-badge">▸{{ subProgress(card.id).done }}/{{ subProgress(card.id).total }}</span>
                </div>
              </div>
              <div v-if="statusTodos(st).length === 0" class="kanban-empty">{{ t('todoKanbanEmpty') }}</div>
            </div>
          </div>
        </div>

        <div class="panel-foot">
          <button class="clear-btn" @click="clearCompleted">{{ t('todoClearCompleted') }}</button>
          <button class="test-btn" :disabled="testing" @click="testReminder">
            <Bell :size="12" /> {{ t('todoTestReminder') }}
          </button>
        </div>
        <p class="reminder-hint">{{ t('todoReminderHint') }}</p>
      </div>
    </div>

    <!-- 编辑弹窗 -->
    <div v-if="showEdit" class="modal-mask" @click.self="showEdit = false">
      <div class="modal">
        <h3>{{ t('todoEdit') }}</h3>

        <label>{{ t('todoTitle') }}</label>
        <input v-model="editTitle" class="modal-input" placeholder="..." />

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

        <!-- 4.2 标签 -->
        <label>{{ t('todoTags') }}</label>
        <input v-model="editTags" class="modal-input" :placeholder="t('todoTagsPh')" list="todo-tag-options" />

        <label>{{ t('todoDue') }}</label>
        <input v-model="editDueDate" type="date" class="modal-input" />

        <label>{{ t('todoNote') }}</label>
        <input v-model="editNote" class="modal-input" />

        <div class="modal-actions">
          <button class="qd-btn" @click="showEdit = false">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" @click="saveEdit">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <!-- 新增弹窗 -->
    <div v-if="showCreate" class="modal-mask" @click.self="showCreate = false">
      <div class="modal">
        <h3>{{ t('todoAdd') }}</h3>

        <label>{{ t('todoTitle') }}</label>
        <input v-model="cTitle" class="modal-input" :placeholder="t('todoAddPlaceholder')" />

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

        <div class="modal-actions">
          <button class="qd-btn" @click="showCreate = false">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" @click="createTodo">{{ t('save') }}</button>
        </div>
      </div>
    </div>

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
.cal-nav { display: flex; align-items: center; gap: var(--space-2); }
/* 页内搜索：只搜标题/备注/标签，跨日期；宽度与笔记页搜索一致 */
.todo-search { width: 220px; flex-shrink: 0; margin-right: var(--space-3); }
.ts-input { padding-left: 28px; padding-right: 26px; }
.ts-clear { position: absolute; right: 6px; border: none; background: none; color: var(--color-text-muted); cursor: pointer; display: flex; align-items: center; }
.focus-dur { display: flex; align-items: center; gap: 2px; }
.focus-dur-btn {
  min-width: 28px; height: 24px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-muted); border-radius: var(--radius-sm); font-size: 11px; cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.focus-dur-btn:hover { color: var(--color-text-primary); }
.focus-dur-btn.active { color: var(--color-accent); background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.cal-month { font-size: 13px; font-weight: 600; color: var(--color-text-primary); min-width: 84px; text-align: center; }

.todo-error {
  margin: 12px 20px 0; padding: 8px 12px; font-size: 12px;
  color: var(--color-danger); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md); flex-shrink: 0;
}

.todo-body { display: flex; gap: var(--space-6); flex: 1; min-height: 0; padding: 14px 20px 16px; }

/* 日历卡片 */
.cal-card {
  width: 332px; flex-shrink: 0; display: flex; flex-direction: column;
  background: var(--color-bg-secondary);
  border-radius: var(--radius-lg);
  border: 1px solid var(--color-border);
  padding: var(--space-4);
}
.cal-weekdays { display: grid; grid-template-columns: repeat(7, 1fr); margin-bottom: var(--space-1); }
.cal-weekday { text-align: center; font-size: 11px; color: var(--color-text-disabled); font-weight: 500; padding-bottom: var(--space-1); }
.cal-grid { display: grid; grid-template-columns: repeat(7, 1fr); grid-auto-rows: 1fr; gap: 2px; flex: 1; }
.cal-cell {
  position: relative;
  display: flex; flex-direction: column; align-items: center; justify-content: flex-start; gap: 3px;
  padding: 5px 0; border: none; outline: none; background: transparent;
  cursor: pointer; font-family: inherit; border-radius: var(--radius-md);
  transition: background var(--transition-fast);
}
.cal-cell:hover { background: var(--color-bg-hover); }
.cal-cell.out-month { opacity: 0.45; }
.cal-day {
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 50%;
  font-size: 12px; font-weight: 500; color: var(--color-text-primary);
  transition: color var(--transition-fast), background var(--transition-fast), box-shadow var(--transition-fast);
}
.cal-cell.out-month .cal-day { color: var(--color-text-disabled); }
.cal-cell.is-today .cal-day { color: var(--color-accent); border: 1px solid var(--color-accent); }
.cal-cell.is-selected .cal-day { background: var(--color-accent); color: var(--color-accent-text); box-shadow: none; }
.cal-cell.is-selected.is-today .cal-day { background: var(--color-accent); color: var(--color-accent-text); }
.cal-marks { display: flex; align-items: center; gap: 3px; height: 7px; flex-wrap: wrap; justify-content: center; }
.dot { width: 4px; height: 4px; border-radius: 50%; display: inline-block; }
.dot.done { opacity: 0.3; }
.cal-bell { color: var(--color-accent); }
.cal-more { font-size: 9px; color: var(--color-text-disabled); font-style: normal; }

.unscheduled-chip {
  display: flex; align-items: center; gap: 6px; margin-top: var(--space-3);
  padding: 8px 10px; border: 1px solid var(--color-border); background: var(--color-bg-primary);
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.unscheduled-chip:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.unscheduled-chip.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }
.chip-count { margin-left: auto; font-style: normal; background: var(--color-bg-tertiary); border-radius: var(--radius-md); padding: 0 6px; font-size: 11px; }

/* 右侧面板 */
.todo-panel { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.panel-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-4); }
.panel-date { font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.panel-remaining { font-size: 12px; color: var(--color-text-disabled); }

.panel-head-right { display: flex; align-items: center; gap: var(--space-3); }

/* 视图切换（列表 / 看板）*/
.view-toggle { display: inline-flex; border: 1px solid var(--color-border); border-radius: var(--radius-md); overflow: hidden; }
.vt-btn {
  padding: 4px 10px; border: none; background: var(--color-bg-tertiary);
  color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.vt-btn + .vt-btn { border-left: 1px solid var(--color-border); }
.vt-btn.active { background: var(--color-accent); color: var(--color-accent-text); }

/* 状态徽章（4.3）*/
.status-pill {
  font-size: 10px; padding: 0 7px; line-height: 16px; border-radius: var(--radius-md); cursor: pointer;
  border: 1px solid transparent; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.status-pill.st-todo { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
.status-pill.st-doing { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning-border); }
.status-pill.st-done { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }

/* 子任务（4.1）*/
.todo-item { flex-wrap: wrap; }
.sub-progress { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.sub-progress-bar { flex: 1; height: 4px; border-radius: var(--radius-xs); background: var(--color-bg-tertiary); overflow: hidden; }
.sub-progress-fill { height: 100%; background: var(--color-accent); border-radius: var(--radius-xs); transition: width var(--transition-fast); }
.sub-progress-text { font-size: 11px; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; white-space: nowrap; }
.subtasks { flex-basis: 100%; margin: 6px 0 2px 26px; display: flex; flex-direction: column; gap: 3px; }
.sub-item { display: flex; align-items: center; gap: 7px; padding: 3px 0; }
.sub-item.done .sub-name { text-decoration: line-through; color: var(--color-text-disabled); }
.sub-check { width: 15px; height: 15px; border-radius: var(--radius-xs); }
.sub-name { flex: 1; font-size: 12px; color: var(--color-text-secondary); min-width: 0; }
.sub-del { opacity: 0; width: 22px; height: 22px; }
.sub-item:hover .sub-del { opacity: 1; }
/* 行内改名：与 .sub-name 同字号，去掉输入框边框只留底色，避免行高跳动 */
.sub-rename {
  flex: 1; min-width: 0; height: 20px; padding: 0 5px;
  border: 1px solid var(--color-accent); border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 12px; font-family: inherit; outline: none;
}
/* 上下移：hover 才出现，和删除按钮同一套作息 */
.sub-move { display: flex; align-items: center; gap: 1px; opacity: 0; }
.sub-item:hover .sub-move { opacity: 1; }
.sub-act { width: 18px; height: 18px; }
.sub-act:disabled { opacity: 0.25; cursor: default; }
.sub-add { margin-top: 2px; }
.sub-input { height: 28px; font-size: 12px; }

/* 单独设截止日：日期按钮与上下移同一套 hover 作息；已设日期的常显一个小色点提示 */
.sub-due { opacity: 0; }
.sub-item:hover .sub-due { opacity: 1; }
.sub-due.active { opacity: 1; color: var(--color-accent); }
.sub-date {
  height: 20px; padding: 0 4px; border: 1px solid var(--color-accent);
  border-radius: var(--radius-xs); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 11px; font-family: inherit; outline: none;
}

/* 看板视图（4.3）*/
.kanban { flex: 1; display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--space-3); min-height: 0; overflow: hidden; }
.kanban-col {
  display: flex; flex-direction: column; min-height: 0;
  background: var(--color-bg-secondary); border-radius: var(--radius-lg);
  border: 1px solid var(--color-border); padding: var(--space-3);
}
.kanban-col-head { display: flex; align-items: center; gap: 6px; margin-bottom: var(--space-2); flex-shrink: 0; }
.kanban-dot { width: 8px; height: 8px; border-radius: 50%; }
.kanban-dot.st-todo { background: var(--color-text-disabled); }
.kanban-dot.st-doing { background: var(--color-warning); }
.kanban-dot.st-done { background: var(--color-accent); }
.kanban-col-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.kanban-count { margin-left: auto; font-size: 11px; color: var(--color-text-disabled); background: var(--color-bg-tertiary); border-radius: var(--radius-md); padding: 0 7px; }
.kanban-cards { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.kanban-card {
  padding: 9px 11px; background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md); cursor: grab; transition: background var(--transition-fast);
}
.kanban-card:hover { background: var(--color-bg-hover); }
.kanban-card:active { cursor: grabbing; }
.kanban-card-name { font-size: 13px; color: var(--color-text-primary); }
.kanban-card-meta { margin-top: 5px; display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
/* 卡片上的截止日：逾期改用警示色，扫一眼就知道哪张拖后腿了 */
.kanban-due { font-size: 10px; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }
.kanban-due.overdue { color: var(--color-danger); font-weight: 500; }
/* 拖拽落点：整列描边提示，比只高亮卡片更容易判断"会落到哪一列" */
.kanban-col.drag-over { border-color: var(--color-accent); background: var(--color-accent-bg); }
.kanban-empty { text-align: center; font-size: 11px; color: var(--color-text-disabled); padding: var(--space-5) 0; }

.tag-filter { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: var(--space-3); }
.tag-fbtn {
  font-size: 11px; padding: 2px 10px; border-radius: var(--radius-md); cursor: pointer;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-muted); font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.tag-fbtn:hover { color: var(--color-text-primary); border-color: var(--color-border-focus); }
.tag-fbtn.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }
.add-fab {
  width: 30px; height: 30px; flex-shrink: 0; display: flex; align-items: center; justify-content: center;
  background: var(--color-accent); color: var(--color-accent-text); border: none; border-radius: var(--radius-md);
  cursor: pointer; transition: background var(--transition-fast);
}
.add-fab:hover { background: var(--color-accent-hover); }

.todo-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.todo-empty { text-align: center; padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.empty-icon { opacity: 0.4; margin-bottom: var(--space-2); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); margin-top: var(--space-2); }

.todo-item {
  display: flex; align-items: center; gap: var(--space-3); padding: 10px 12px;
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
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
.todo-name { font-size: 13px; color: var(--color-text-primary); }
.todo-meta { font-size: 11px; color: var(--color-text-disabled); margin-top: 3px; display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
.overdue-text { color: var(--color-danger); }
.meta-pill { display: inline-flex; align-items: center; gap: 3px; color: var(--color-text-muted); }
.meta-pill.sent { color: var(--color-text-disabled); }
.todo-note { color: var(--color-text-muted); }
.tag-chip {
  font-size: 10px; padding: 0 7px; line-height: 16px; border-radius: var(--radius-md);
  background: var(--color-accent-bg); color: var(--color-accent);
  cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.tag-chip:hover { filter: brightness(1.15); }
.rec-pill { color: var(--color-text-secondary); }
.todo-actions { display: flex; gap: 2px; opacity: 0; transition: opacity var(--transition-fast); }
.todo-item:hover .todo-actions { opacity: 1; }
.act {
  width: 26px; height: 26px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled);
  border-radius: var(--radius-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.act:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.act.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.focus-act { font-size: 11px; font-weight: 600; color: var(--color-accent); min-width: 34px; }
.focus-act.running { background: var(--color-accent-bg); color: var(--color-accent); }

.panel-foot { margin-top: var(--space-3); flex-shrink: 0; display: flex; align-items: center; gap: var(--space-3); }
.clear-btn {
  padding: 6px 12px; border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-disabled); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.clear-btn:hover { color: var(--color-danger); border-color: var(--color-danger-border); }
.test-btn {
  display: inline-flex; align-items: center; gap: 5px; margin-left: auto;
  padding: 6px 12px; border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.test-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent-border); }
.test-btn:disabled { opacity: 0.5; cursor: default; }
.reminder-hint { margin: var(--space-2) 0 0; font-size: 11px; color: var(--color-text-disabled); }

/* 优先级配色 */
.p-none { background: var(--color-text-disabled); }
.p-low { background: var(--color-accent); }
.p-medium { background: var(--color-warning); }
.p-high { background: var(--color-danger); }

/* 弹窗 */
.modal-mask {
  position: fixed; inset: 0; background: var(--color-bg-overlay); display: flex;
  align-items: center; justify-content: center; z-index: 100;
}
.modal {
  width: 380px; max-height: 94vh; overflow: visible; background: var(--color-surface);
  border-radius: var(--radius-lg); padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }
.modal-grid input[type="datetime-local"] { width: 100%; box-sizing: border-box; }
.modal-actions { display: flex; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-4); }
</style>
