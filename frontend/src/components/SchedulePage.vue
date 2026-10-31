<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { matchesTextOrPinyin } from '../utils/pinyin'
import {
  CreateScheduledTask, ListScheduledTasks, UpdateScheduledTask,
  DeleteScheduledTask, SetScheduledTaskEnabled, RunScheduledTaskNow,
  SetScheduledTaskSort, GetAllScheduleRuns, SetAllScheduledTasksEnabled,
} from '../../bindings/quickdock/services/appservice'
import type { ScheduledTask } from '../../bindings/quickdock/internal/db/models'

import { Events } from '@wailsio/runtime'
import ConfirmDialog from './ConfirmDialog.vue'
import ScheduleRunHistoryDialog from './ScheduleRunHistoryDialog.vue'
// 「打开软件」的目标输入复用应用选择器：支持手输自定义路径 / 浏览文件 / 系统扫描的已装应用
import AppTargetPicker from './AppTargetPicker.vue'
import {
  Plus, Play, Pause, Pencil, Trash2, AlarmClock, AppWindow, FolderOpen,
  Globe, TerminalSquare, Zap, CheckCircle2, XCircle, History,
  Search, LoaderCircle, GripVertical, Bell, BarChart3,
} from '@lucide/vue'

const { t, locale } = useI18n()

const tasks = ref<ScheduledTask[]>([])
const loading = ref(false)
const error = ref('')
// 弹窗内的校验/保存错误：不能复用 error——它渲染在页面里，弹窗盖着完全看不见，
// 用户只看到「点保存没反应」。
const formErr = ref('')

// ---- 动作类型元信息 ----
const ACTIONS = ['app', 'dir', 'url', 'command', 'http'] as const
const actionIcon: Record<string, any> = {
  app: AppWindow, dir: FolderOpen, url: Globe, command: TerminalSquare, http: Zap,
}
function actionLabel(a: string) { return t('sched_action_' + a) }

// ---- 调度类型 ----
// monthly 后端一直支持（validateScheduledTask + computeNextRun 都有分支），
// 前端漏了它，等于「每月几号跑一次」这个能力完全不可达，而派生自待办的月度任务
// 还会在列表里显示成空白描述。
const KINDS = ['once', 'interval', 'daily', 'weekly', 'monthly'] as const
// 周一为首列展示；v 使用 JS 星期编号（0=周日..6=周六）
const WEEKDAYS = computed(() => {
  const zh = ['一', '二', '三', '四', '五', '六', '日']
  const en = ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su']
  const labels = locale.value === 'en-US' ? en : zh
  return [1, 2, 3, 4, 5, 6, 0].map((v, i) => ({ v, l: labels[i] }))
})

// 目标输入框的标签/占位随动作变化
function targetLabel(a: string) { return t('sched_target_' + a) }
function targetPlaceholder(a: string) {
  switch (a) {
    case 'app': return 'C:\\Program Files\\App\\app.exe'
    case 'dir': return 'D:\\project\\quickdock'
    case 'url': return 'https://example.com'
    case 'command': return 'ping -n 1 example.com'
    case 'http': return 'https://api.example.com/webhook'
  }
  return ''
}

// ---- 展示辅助 ----

function scheduleDesc(t0: ScheduledTask): string {
  // 重试次数拼在描述末尾：它改变了「失败了会怎样」的语义，属于该一眼看到的信息，
  // 不该只藏在编辑弹窗里。不重试（0）时不加，避免每条任务都拖一条尾巴。
  const retry = t0.retryCount ? ' · ' + t('sched_retry_short', { n: t0.retryCount }) : ''
  switch (t0.scheduleKind) {
    case 'once': return t('sched_kind_once') + (t0.runAt ? ' · ' + t0.runAt.slice(5, 16) : '')
    case 'interval': return t('sched_kind_interval') + ' · ' + humanInterval(t0.intervalSec) + retry
    case 'daily': return t('sched_kind_daily') + ' · ' + (t0.timeOfDay || '') + retry
    case 'weekly': return t('sched_kind_weekly') + ' · ' + weekdayText(t0.weekdays) + ' ' + (t0.timeOfDay || '') + retry
    case 'monthly': return t('sched_kind_monthly') + ' · ' + (t0.timeOfDay || '') + retry
  }
  return ''
}
function humanInterval(sec: number): string {
  const [v, u] = splitInterval(sec)
  return v + ' ' + t('sched_unit_' + u)
}
function weekdayText(csv: string): string {
  if (!csv) return ''
  return csv.split(',').filter(Boolean).map(n => {
    const w = WEEKDAYS.value.find(x => x.v === Number(n))
    return w ? w.l : ''
  }).join('')
}

// ---- 加载 ----
async function refresh() {
  loading.value = true
  error.value = ''
  try {
    const list = unwrap<ScheduledTask[]>(await ListScheduledTasks()) ?? []
    // action='todo' 是「待办重复」在调度层的内部实现，入口与展示都在待办页。
    // 让它混进本页的代价：图标找不到、文案显示原始 key，而且一编辑保存就被后端
    // 以「未知的动作类型: todo」拒绝——看起来就像本页坏了。
    tasks.value = list.filter(x => x.action !== 'todo')
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
  // 统计与列表解耦：拉历史失败不该让整页报错，也不阻塞列表渲染
  void loadRuns()
}

// ---- 运行统计（近 7 天）----
// 单任务历史只能回答「这条最近怎么样」，看不到「过去一周一共失败了几次」。
// 一次拉全量（上限 1000 条，后端按任务保留 500 条）在前端按天聚合。
interface RunItem {
  id: string; taskId: string; taskName: string; runAt: string
  status: string; result: string; durationMs: number; createdTs: number
}
const runs = ref<RunItem[]>([])
async function loadRuns() {
  try {
    runs.value = unwrap<RunItem[]>(await GetAllScheduleRuns(1000)) ?? []
  } catch { /* 统计失败不影响主列表 */ }
}
const stats = computed(() => {
  const days: { date: string; total: number; fail: number }[] = []
  const today = new Date(); today.setHours(0, 0, 0, 0)
  for (let i = 6; i >= 0; i--) {
    const d = new Date(today); d.setDate(d.getDate() - i)
    days.push({
      date: `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`,
      total: 0, fail: 0,
    })
  }
  const idx = new Map(days.map((x, i) => [x.date, i]))
  let total = 0, fail = 0
  for (const r of runs.value) {
    const i = idx.get((r.runAt || '').slice(0, 10))
    if (i === undefined) continue
    days[i].total++
    total++
    if (r.status !== 'ok') { days[i].fail++; fail++ }
  }
  const max = Math.max(1, ...days.map(d => d.total))
  return { days, total, fail, max, rate: total ? Math.round((fail / total) * 100) : 0 }
})
function barPct(n: number): number {
  return Math.round((n / stats.value.max) * 100)
}
function dayLabel(ds: string): string {
  return ds.slice(5) // MM-DD，tooltip 用
}

// ---- 搜索与筛选 ----
// 任务攒到十几条之后，「找那个每 5 分钟拉的」只能靠肉眼扫列表，
// 而每条又有 name/target 两段可搜文本、四种状态 —— 这两样都补上。
const query = ref('')
const statusFilter = ref<'all' | 'fail' | 'on' | 'off'>('all')

const STATUS_FILTERS = computed(() => [
  { v: 'all' as const, l: t('sched_filter_all') },
  { v: 'fail' as const, l: t('sched_filter_fail') },
  { v: 'on' as const, l: t('sched_filter_on') },
  { v: 'off' as const, l: t('sched_filter_off') },
])

const filteredTasks = computed(() => {
  const q = query.value.trim()
  return tasks.value.filter((x) => {
    if (statusFilter.value === 'fail' && x.lastStatus !== 'fail') return false
    if (statusFilter.value === 'on' && !x.enabled) return false
    if (statusFilter.value === 'off' && x.enabled) return false
    if (!q) return true
    // 与待办/笔记/侧栏同口径：原文包含或拼音命中（名称、目标都参与）
    return matchesTextOrPinyin([x.name, x.target], q, 'sched:' + x.id)
  })
})

/** 有筛选时禁用拖拽：此时列表是任务全集的子集，拖出来的顺序没法映射回全量 sort。 */
const canDrag = computed(() => !query.value.trim() && statusFilter.value === 'all')

// ---- 倒计时 ----
// next_run 是后端算好的绝对时间（'YYYY-MM-DD HH:MM:SS' 本地时区），前端只做差。
// 30 秒一跳：够看清「还有几分钟」，又不至于像秒表那样整页重渲染。
const nowTs = ref(Date.now())
const tickTimer = setInterval(() => { nowTs.value = Date.now() }, 30_000)
onBeforeUnmount(() => clearInterval(tickTimer))

function parseLocal(s: string): number {
  if (!s) return 0
  // 'YYYY-MM-DD HH:MM:SS' → 'YYYY-MM-DDTHH:MM:SS'（WebView2 与各浏览器都按本地时区解）
  const ms = Date.parse(s.replace(' ', 'T'))
  return Number.isFinite(ms) ? ms : 0
}

/** 返回「还有 X」这类相对时间；已到期返回空串（由调用方显示「即将执行」）。 */
function leftText(task: ScheduledTask): string {
  const ts = parseLocal(task.nextRun)
  if (!ts) return ''
  const diff = ts - nowTs.value
  if (diff <= 0) return t('sched_due_now')
  const sec = Math.round(diff / 1000)
  if (sec < 60) return t('sched_left_sec', { n: sec })
  const min = Math.round(sec / 60)
  if (min < 60) return t('sched_left_min', { n: min })
  const hour = Math.floor(min / 60)
  if (hour < 24) return t('sched_left_hr', { n: hour })
  return t('sched_left_day', { n: Math.round(hour / 24) })
}

// ---- 拖拽排序 ----
// db 早有 sort 列，列表也按 sort ASC 排，但一直没有任何 UI 能写它 ——
// 加了任务就只能按创建时间排。拖拽走 SetScheduledTaskSort（只改 sort 列），
// 不经过 UpdateScheduledTask，所以不会重算 next_run。
const dragId = ref('')
const dragOverId = ref('')

function onDragStart(e: DragEvent, task: ScheduledTask) {
  if (!canDrag.value) return
  dragId.value = task.id
  e.dataTransfer?.setData('text/plain', task.id)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}

function onDragOver(task: ScheduledTask) {
  if (!dragId.value || dragId.value === task.id) return
  dragOverId.value = task.id
}

async function onDrop(task: ScheduledTask) {
  const from = dragId.value
  dragId.value = ''
  dragOverId.value = ''
  if (!from || from === task.id) return
  const list = [...filteredTasks.value]
  const fi = list.findIndex((x) => x.id === from)
  const ti = list.findIndex((x) => x.id === task.id)
  if (fi < 0 || ti < 0) return
  const [moved] = list.splice(fi, 1)
  list.splice(ti, 0, moved)
  // 乐观更新：先改本地顺序，失败再拉回来（不然要等 N 次 IPC 才看到变化）
  tasks.value = list
  try {
    // 步长 10：以后要在中间插一条，改一个数就够，不用整段重排。
    for (let i = 0; i < list.length; i++) {
      if ((list[i].sort || 0) === i * 10) continue
      await unwrap(await SetScheduledTaskSort(list[i].id, i * 10))
      list[i].sort = i * 10
    }
  } catch (e) {
    error.value = getErrorMessage(e)
    await refresh()
  }
}

function onDragEnd() {
  dragId.value = ''
  dragOverId.value = ''
}

// ---- 表单模型（新增 / 编辑共用） ----
const showModal = ref(false)
const editingId = ref('')            // '' = 新增
const modalTitle = computed(() => editingId.value ? t('sched_edit') : t('sched_add'))

const fName = ref('')
const fAction = ref<string>('app')
const fTarget = ref('')
const fWorkingDir = ref('')
const fHttpMethod = ref('GET')
const fHttpHeaders = ref('')
const fHttpBody = ref('')
const fKind = ref<string>('once')
const fRunAt = ref('')               // datetime-local
const fIntervalVal = ref(5)           // 数值，配合 fIntervalUnit 换算成秒
const fIntervalUnit = ref<'sec' | 'min' | 'hour' | 'day'>('min')
const fTimeOfDay = ref('09:00')
const fWeekdays = ref<number[]>([1, 2, 3, 4, 5])
const fEnabled = ref(true)
const fNotify = ref(true)
// 通知渠道：与后端 notify_channels 的 system/webhook 一一对应。
// 是「总开关 + 分渠道」不是三选一：fNotify 关掉时两个渠道都不发。
const fNotifySystem = ref(true)
const fNotifyWebhook = ref(true)
// 失败重试次数（0 = 不重试）。只在定时任务上有意义，once 任务后端会归零。
const fRetry = ref(0)

function toInput(s: string): string { return s ? s.replace(' ', 'T') : '' }
function fromInput(s: string): string { return s ? s.replace('T', ' ') : '' }

/** 机器人 Webhook 是全局配置，跳设置页去改（本页不重复实现一份表单）。 */
function openNotifySettings() {
  try { Events.Emit('app:open-settings', 'notify') } catch { /* 事件通道未就绪时静默，不阻塞新建任务 */ }
}

function openCreate() {
  formErr.value = ''
  editingId.value = ''
  fName.value = ''
  fAction.value = 'app'
  fTarget.value = ''
  fWorkingDir.value = ''
  fHttpMethod.value = 'GET'
  fHttpHeaders.value = ''
  fHttpBody.value = ''
  fKind.value = 'once'
  fRunAt.value = ''
  fIntervalVal.value = 5
  fIntervalUnit.value = 'min'
  fTimeOfDay.value = '09:00'
  fWeekdays.value = [1, 2, 3, 4, 5]
  fEnabled.value = true
  fNotify.value = true
  fNotifySystem.value = true
  fNotifyWebhook.value = true
  fRetry.value = 0
  showModal.value = true
}

function openEdit(t0: ScheduledTask) {
  formErr.value = ''
  editingId.value = t0.id
  fName.value = t0.name
  fAction.value = t0.action
  fTarget.value = t0.target
  fWorkingDir.value = t0.workingDir
  fHttpMethod.value = t0.httpMethod || 'GET'
  fHttpHeaders.value = t0.httpHeaders
  fHttpBody.value = t0.httpBody
  fKind.value = t0.scheduleKind
  fRunAt.value = toInput(t0.runAt)
  // 反解间隔：优先落到能整除的最大单位，避免 86400 秒显示成 86400
  const [v, u] = splitInterval(t0.intervalSec || 1800)
  fIntervalVal.value = v
  fIntervalUnit.value = u
  fTimeOfDay.value = (t0.timeOfDay || '09:00').slice(0, 5)
  fWeekdays.value = t0.weekdays ? t0.weekdays.split(',').filter(Boolean).map(Number) : []
  fEnabled.value = t0.enabled
  fNotify.value = t0.notify
  // 空值视为「两个都要」，与 db 层归一化同口径
  const chans = t0.notifyChannels || 'system,webhook'
  fNotifySystem.value = chans.includes('system')
  fNotifyWebhook.value = chans.includes('webhook')
  fRetry.value = t0.retryCount || 0
  showModal.value = true
}

function toggleWeekday(v: number) {
  const i = fWeekdays.value.indexOf(v)
  if (i >= 0) fWeekdays.value.splice(i, 1)
  else fWeekdays.value.push(v)
}

const INTERVAL_UNITS = [
  { v: 'sec', mul: 1, min: 5 },
  { v: 'min', mul: 60, min: 1 },
  { v: 'hour', mul: 3600, min: 1 },
  { v: 'day', mul: 86400, min: 1 },
] as const

// splitInterval 把秒拆成 (数值, 单位)：优先取能整除的最大单位
function splitInterval(sec: number): [number, 'sec' | 'min' | 'hour' | 'day'] {
  const s = Math.max(5, Math.floor(sec || 300))
  for (let i = INTERVAL_UNITS.length - 1; i >= 0; i--) {
    const u = INTERVAL_UNITS[i]
    if (s % u.mul === 0) return [s / u.mul, u.v]
  }
  return [s, 'sec']
}

function unitMeta() {
  return INTERVAL_UNITS.find(u => u.v === fIntervalUnit.value) ?? INTERVAL_UNITS[0]
}

function intervalVal(): number {
  const n = Math.floor(fIntervalVal.value || 1)
  const u = unitMeta()
  return Math.max(5, n * u.mul)
}

function buildChannels(): string {
  if (!fNotify.value) return 'system,webhook' // 总开关关着，渠道值无所谓，保持满值不误导
  const picked = [fNotifySystem.value ? 'system' : '', fNotifyWebhook.value ? 'webhook' : ''].filter(Boolean)
  return picked.length ? picked.join(',') : 'system'
}

function buildPayload(): ScheduledTask {
  return {
    id: editingId.value,
    name: fName.value.trim(),
    action: fAction.value,
    target: fTarget.value.trim(),
    workingDir: fWorkingDir.value.trim(),
    httpMethod: fHttpMethod.value,
    httpHeaders: fHttpHeaders.value,
    httpBody: fHttpBody.value,
    scheduleKind: fKind.value,
    runAt: fromInput(fRunAt.value),
    intervalSec: intervalVal(),
    timeOfDay: fKind.value === 'daily' || fKind.value === 'weekly' || fKind.value === 'monthly' ? fTimeOfDay.value + ':00' : '',
    weekdays: fKind.value === 'weekly' ? [...fWeekdays.value].sort((a, b) => a - b).join(',') : '',
    enabled: fEnabled.value,
    notify: fNotify.value,
    // 开了通知却一个渠道都没勾，等于「静默不通知」——这种组合用户多半是误操作，
    // 兜底成系统通知（本机一定能送达到），而不是让后端归一成"两个都要"往上推机器人。
    notifyChannels: buildChannels(),
    // once 任务没有「下一次调度」，重试次数无意义：后端会归零，这里也置 0，
    // 免得切回其他调度类型时又冒出一个用户没设过的数字。
    retryCount: fKind.value === 'once' ? 0 : fRetry.value,
    nextRun: '', lastRun: '', lastStatus: '', lastResult: '', sort: 0, createdAt: '',
  } as ScheduledTask
}

const saving = ref(false)
async function save() {
  const p = buildPayload()
  formErr.value = ''
  if (!p.name) { formErr.value = t('sched_err_name'); return }
  if (!p.target) { formErr.value = t('sched_err_target'); return }
  if (p.scheduleKind === 'once' && !p.runAt) { formErr.value = t('sched_err_runat'); return }
  if (p.scheduleKind === 'weekly' && !p.weekdays) { formErr.value = t('sched_err_weekday'); return }
  saving.value = true
  try {
    if (editingId.value) {
      await unwrap(await UpdateScheduledTask(p))
    } else {
      await unwrap(await CreateScheduledTask(p))
    }
    showModal.value = false
    await refresh()
  } catch (e) {
    formErr.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(t0: ScheduledTask) {
  try {
    await unwrap(await SetScheduledTaskEnabled(t0.id, !t0.enabled))
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 批量启停 ----
// 临时全停（会议投屏等）不该一条条点。后端只处理普通任务，待办派生任务不受影响。
const bulkBusy = ref(false)
const allOn = computed(() => tasks.value.length > 0 && tasks.value.every(x => x.enabled))
async function toggleAllEnabled() {
  bulkBusy.value = true
  error.value = ''
  try {
    await unwrap(await SetAllScheduledTasksEnabled(!allOn.value))
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    bulkBusy.value = false
  }
}

const runningId = ref('')
async function runNow(t0: ScheduledTask) {
  runningId.value = t0.id
  error.value = ''
  try {
    const r = await RunScheduledTaskNow(t0.id)
    // RunScheduledTaskNow 失败时 code=1，成功时 msg 为结果
    if (r && r.code !== 0) { error.value = r.msg }
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    runningId.value = ''
  }
}

async function remove(t0: ScheduledTask) {
  delTask.value = t0
  showDelConfirm.value = true
}

const delTask = ref<ScheduledTask | null>(null)
const showDelConfirm = ref(false)

// ---- 运行历史 ----
const historyTask = ref<ScheduledTask | null>(null)
const showHistory = ref(false)
function openHistory(t0: ScheduledTask) {
  historyTask.value = t0
  showHistory.value = true
}

async function confirmDel() {
  const t = delTask.value
  if (!t) return
  showDelConfirm.value = false
  delTask.value = null
  try {
    await unwrap(await DeleteScheduledTask(t.id))
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) }
}

onMounted(refresh)
</script>

<template>
  <div class="sched-page">
    <!-- 顶部栏 -->
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('sched_title') }}</h2>
        <span class="qd-page-sub">{{ t('sched_subtitle') }}</span>
      </div>
      <div class="qd-page-actions">
        <!-- 临时全停（会议投屏等）不该一条条点：全启用时显示「暂停所有」，否则「恢复所有」 -->
        <button class="qd-btn" :disabled="bulkBusy || tasks.length === 0" @click="toggleAllEnabled"
                :title="allOn ? t('sched_pause_all') : t('sched_resume_all')">
          <Pause v-if="allOn" :size="14" />
          <Play v-else :size="14" />
          {{ allOn ? t('sched_pause_all') : t('sched_resume_all') }}
        </button>
        <!-- 机器人地址是全局一份，配置入口统一收在设置里；这里只做跳转，避免两个入口改同一份数据 -->
        <button class="qd-btn" :title="t('notifyGoSettings')" @click="openNotifySettings">
          <Bell :size="14" /> {{ t('sched_nf_title') }}
        </button>
        <button class="qd-btn qd-btn-primary" @click="openCreate"><Plus :size="14" /> {{ t('sched_add') }}</button>
      </div>
    </div>

    <div v-if="error" class="sched-error">{{ error }}</div>

    <!-- 搜索 / 状态筛选 -->
    <div v-if="tasks.length" class="sched-toolbar">
      <div class="sched-search">
        <Search :size="13" class="sched-search-icon" />
        <input v-model="query" class="sched-search-input" :placeholder="t('sched_search_ph')" />
      </div>
      <div class="sched-filters">
        <button v-for="f in STATUS_FILTERS" :key="f.v" type="button"
                :class="['qd-btn', 'qd-btn-sm', { 'is-on': statusFilter === f.v }]"
                @click="statusFilter = f.v">{{ f.l }}</button>
      </div>
      <span class="sched-count">{{ t('sched_count', { n: filteredTasks.length, total: tasks.length }) }}</span>
    </div>

    <!-- 运行统计（近 7 天）：失败次数与失败率一眼可见，不用逐条点历史 -->
    <div v-if="tasks.length" class="sched-stats">
      <div class="stat-sum">
        <BarChart3 :size="13" class="stat-ico" />
        <span class="stat-label">{{ t('sched_stats_week') }}</span>
        <span class="stat-num">{{ stats.total }}</span>
        <span class="stat-unit">{{ t('sched_stats_runs') }}</span>
        <span class="stat-sep">·</span>
        <span class="stat-fail" :class="{ zero: stats.fail === 0 }">{{ t('sched_stats_fail', { n: stats.fail }) }}</span>
        <span v-if="stats.total" class="stat-rate" :class="{ bad: stats.rate >= 20 }">{{ stats.rate }}%</span>
      </div>
      <div class="stat-bars">
        <div v-for="d in stats.days" :key="d.date"
             :class="['stat-bar', { 'has-fail': d.fail > 0, empty: d.total === 0 }]"
             :title="dayLabel(d.date) + ' · ' + d.total">
          <div class="bar-fill" :style="{ height: barPct(d.total) + '%' }"></div>
        </div>
      </div>
    </div>

    <!-- 列表 -->
    <div class="sched-list">
      <div v-if="!loading && tasks.length === 0" class="sched-empty">
        <AlarmClock :size="30" class="empty-icon" />
        <p>{{ t('sched_empty') }}</p>
        <p class="empty-hint">{{ t('sched_empty_hint') }}</p>
      </div>

      <div v-else-if="filteredTasks.length === 0" class="sched-empty">
        <Search :size="26" class="empty-icon" />
        <p>{{ t('sched_no_match') }}</p>
      </div>

      <div v-for="task in filteredTasks" :key="task.id"
           :class="['sched-item', { disabled: !task.enabled, dragging: dragId === task.id, 'drag-over': dragOverId === task.id }]"
           :draggable="canDrag"
           @dragstart="onDragStart($event, task)"
           @dragover.prevent="onDragOver(task)"
           @drop.prevent="onDrop(task)"
           @dragend="onDragEnd">
        <!-- 手柄只是提示：整行都可拖，但给个抓取点用户才知道能拖 -->
        <GripVertical v-if="canDrag" :size="13" class="drag-handle" />
        <div class="item-icon" :class="'a-' + task.action">
          <component :is="actionIcon[task.action]" :size="16" />
        </div>

        <div class="item-main">
          <div class="item-line1">
            <span class="item-name">{{ task.name }}</span>
            <span class="item-badge">{{ actionLabel(task.action) }}</span>
          </div>
          <div class="item-target" :title="task.target">{{ task.target }}</div>
          <div class="item-meta">
            <span class="meta-sched">{{ scheduleDesc(task) }}</span>
            <span v-if="task.enabled && task.nextRun" class="meta-next">
              {{ t('sched_next') }} {{ task.nextRun.slice(5, 16) }}
              <span v-if="leftText(task)" class="meta-left">({{ leftText(task) }})</span>
            </span>
            <span v-if="task.lastRun" class="meta-last" :class="task.lastStatus">
              <CheckCircle2 v-if="task.lastStatus === 'ok'" :size="11" />
              <XCircle v-else :size="11" />
              {{ task.lastResult || task.lastStatus }}
            </span>
          </div>
        </div>

        <div class="item-actions">
          <!-- 启用开关 -->
          <button class="toggle-btn" :class="{ active: task.enabled }" @click="toggleEnabled(task)"
                  :title="task.enabled ? t('sched_enabled') : t('sched_disabled')">
            <span class="toggle-knob" />
          </button>
          <!-- 「立即执行」是同步阻塞调用（后端要等命令跑完，最长 30s），
               所以这里只能给转圈 + 说明，不能画进度条或停止按钮 —— 都没有真实通道。 -->
          <button class="act" :disabled="runningId === task.id" @click="runNow(task)"
                  :title="runningId === task.id ? t('sched_running') : t('sched_run_now')">
            <LoaderCircle v-if="runningId === task.id" :size="14" class="spin" />
            <Play v-else :size="14" />
          </button>
          <button class="act" @click="openHistory(task)" :title="t('sched_history')"><History :size="14" /></button>
          <button class="act" @click="openEdit(task)" :title="t('sched_edit')"><Pencil :size="14" /></button>
          <button class="act danger" @click="remove(task)" :title="t('sched_delete')"><Trash2 :size="14" /></button>
        </div>
      </div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showModal" class="modal-mask" @click.self="showModal = false">
      <div class="modal">
        <h3>{{ modalTitle }}</h3>

        <label>{{ t('sched_name') }}</label>
        <input v-model="fName" class="modal-input" :placeholder="t('sched_name_ph')" />

        <!-- 动作类型 -->
        <label>{{ t('sched_action') }}</label>
        <div class="seg">
          <button v-for="a in ACTIONS" :key="a" type="button"
                  :class="['seg-btn', { active: fAction === a }]" @click="fAction = a">
            <component :is="actionIcon[a]" :size="13" /> {{ actionLabel(a) }}
          </button>
        </div>

        <!-- 目标 -->
        <label>{{ targetLabel(fAction) }}</label>
        <AppTargetPicker v-if="fAction === 'app'" v-model="fTarget" :placeholder="targetPlaceholder(fAction)" />
        <input v-else v-model="fTarget" class="modal-input" :placeholder="targetPlaceholder(fAction)" />

        <!-- 工作目录（app/command） -->
        <template v-if="fAction === 'app' || fAction === 'command'">
          <label>{{ t('sched_working_dir') }}</label>
          <input v-model="fWorkingDir" class="modal-input" :placeholder="t('sched_working_dir_ph')" />
        </template>

        <!-- HTTP 选项 -->
        <template v-if="fAction === 'http'">
          <div class="modal-grid">
            <div>
              <label>{{ t('sched_http_method') }}</label>
              <select v-model="fHttpMethod" class="modal-input">
                <option>GET</option><option>POST</option><option>PUT</option>
                <option>DELETE</option><option>PATCH</option><option>HEAD</option>
              </select>
            </div>
          </div>
          <label>{{ t('sched_http_headers') }}</label>
          <textarea v-model="fHttpHeaders" class="modal-input modal-area" rows="2"
                    :placeholder="'Content-Type: application/json'"></textarea>
          <label>{{ t('sched_http_body') }}</label>
          <textarea v-model="fHttpBody" class="modal-input modal-area" rows="2"
                    :placeholder="'{&quot;key&quot;:&quot;value&quot;}'"></textarea>
        </template>

        <!-- 调度类型 -->
        <label>{{ t('sched_kind') }}</label>
        <div class="seg">
          <button v-for="k in KINDS" :key="k" type="button"
                  :class="['seg-btn', { active: fKind === k }]" @click="fKind = k">
            {{ t('sched_kind_' + k) }}
          </button>
        </div>

        <!-- once -->
        <template v-if="fKind === 'once'">
          <label>{{ t('sched_runat') }}</label>
          <input v-model="fRunAt" type="datetime-local" step="1" class="modal-input" />
        </template>

        <!-- interval -->
        <template v-else-if="fKind === 'interval'">
          <label>{{ t('sched_interval') }}</label>
          <div class="interval-row">
            <input v-model.number="fIntervalVal" type="number" :min="unitMeta().min" class="modal-input interval-num" />
            <select v-model="fIntervalUnit" class="modal-input interval-unit">
              <option v-for="u in INTERVAL_UNITS" :key="u.v" :value="u.v">{{ t('sched_unit_' + u.v) }}</option>
            </select>
          </div>
        </template>

        <!-- daily -->
        <template v-else-if="fKind === 'daily'">
          <label>{{ t('sched_time_of_day') }}</label>
          <input v-model="fTimeOfDay" type="time" class="modal-input" />
        </template>

        <!-- weekly -->
        <template v-else-if="fKind === 'weekly'">
          <label>{{ t('sched_weekdays') }}</label>
          <div class="wd-row">
            <button v-for="w in WEEKDAYS" :key="w.v" type="button"
                    :class="['wd-btn', { active: fWeekdays.includes(w.v) }]" @click="toggleWeekday(w.v)">
              {{ w.l }}
            </button>
          </div>
          <label>{{ t('sched_time_of_day') }}</label>
          <input v-model="fTimeOfDay" type="time" class="modal-input" />
        </template>

        <!-- monthly：后端按"保存当天是几号"作为每月日，所以这里只需要时间 -->
        <template v-else-if="fKind === 'monthly'">
          <label>{{ t('sched_time_of_day') }}</label>
          <input v-model="fTimeOfDay" type="time" class="modal-input" />
        </template>

        <!-- 开关 -->
        <div class="toggle-row">
          <label class="toggle-label">
            <input type="checkbox" v-model="fEnabled" /> {{ t('sched_enable_now') }}
          </label>
          <label class="toggle-label">
            <input type="checkbox" v-model="fNotify" /> {{ t('sched_notify') }}
          </label>
          <!-- 渠道只在总开关打开时出现：关着还显示两个灰框，只会让人以为哪里坏了 -->
          <template v-if="fNotify">
            <label class="toggle-label toggle-sub">
              <input type="checkbox" v-model="fNotifySystem" /> {{ t('sched_notify_system') }}
            </label>
            <label class="toggle-label toggle-sub">
              <input type="checkbox" v-model="fNotifyWebhook" /> {{ t('sched_notify_webhook') }}
            </label>
            <!-- 勾了机器人渠道但一个地址都没配，任务不会报错、只是静默发不出去——
                 这里就地给一条去向，省得用户去翻设置菜单。 -->
            <span class="nf-link" @click="openNotifySettings">{{ t('notifyGoSettings') }}</span>
          </template>
        </div>

        <!-- 失败重试：只在非一次性任务上出现——once 没有「下一次调度」，
             重试与「已经跑完了、失败了就结束了」这个语义冲突（后端也会归零）。 -->
        <template v-if="fKind !== 'once'">
          <label>{{ t('sched_retry') }}</label>
          <select v-model.number="fRetry" class="modal-input">
            <option :value="0">{{ t('sched_retry_off') }}</option>
            <option v-for="n in 3" :key="n" :value="n">{{ t('sched_retry_n', { n }) }}</option>
          </select>
          <p class="form-hint">{{ t('sched_retry_hint') }}</p>
        </template>

        <!-- 校验提示必须画在弹窗里：页面级 error 被遮罩盖住，等于没提示 -->
        <p v-if="formErr" class="form-err">{{ formErr }}</p>

        <!-- 用 qd-btn 这套（app.css）而不是设置页的 .btn/.btn-primary：
             .btn-primary 只定义颜色，盒子尺寸靠 .btn 基类，漏了基类就是「浏览器默认
             按钮 + 主题色文字」——正是保存按钮看起来不对的原因。 -->
        <div class="modal-actions">
          <button class="qd-btn" @click="showModal = false">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" :disabled="saving" @click="save">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :visible="showDelConfirm"
      :message="t('sched_delete') + '?'"
      @confirm="confirmDel"
      @cancel="showDelConfirm = false"
    />

    <ScheduleRunHistoryDialog
      :visible="showHistory"
      :task="historyTask"
      @cancel="showHistory = false"
    />
  </div>
</template>

<style scoped>
.sched-page {
  display: flex; flex-direction: column; height: 100%;
  overflow: hidden; background: var(--color-bg-primary);
}
/* 页头已统一为 .qd-page-header（通栏），内边距下放到内容块 */
.interval-row { display: flex; align-items: center; gap: var(--space-2); }
.interval-num { flex: 1 1 auto; min-width: 0; }
.interval-unit { flex: 0 0 96px; width: 96px; }

/* 搜索 / 状态筛选条：跟着页头一起通栏，内边距与列表对齐 */
.sched-toolbar {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 10px 20px 0; flex-shrink: 0; flex-wrap: wrap;
}
.sched-search {
  display: flex; align-items: center; gap: 6px; height: 28px; padding: 0 9px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); flex: 1 1 180px; max-width: 320px;
}
.sched-search:focus-within { border-color: var(--color-accent); }
.sched-search-icon { color: var(--color-text-disabled); flex-shrink: 0; }
.sched-search-input {
  flex: 1; min-width: 0; border: none; outline: none; background: none;
  color: var(--color-text-primary); font-size: 12px; font-family: inherit;
}
.sched-search-input::placeholder { color: var(--color-text-disabled); }
.sched-filters { display: flex; align-items: center; gap: 4px; }
.sched-count { margin-left: auto; font-size: 11px; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }

/* 运行统计条（近 7 天） */
.sched-stats {
  display: flex; align-items: center; gap: var(--space-4);
  padding: 8px 20px 0; flex-shrink: 0;
}
.stat-sum { display: flex; align-items: center; gap: 6px; font-size: 11px; color: var(--color-text-muted); }
.stat-ico { color: var(--color-text-disabled); flex-shrink: 0; }
.stat-label { color: var(--color-text-secondary); }
.stat-num { font-size: 13px; font-weight: 600; color: var(--color-text-primary); font-variant-numeric: tabular-nums; }
.stat-unit { color: var(--color-text-muted); }
.stat-sep { color: var(--color-text-disabled); }
.stat-fail { color: var(--color-danger); }
.stat-fail.zero { color: var(--color-text-disabled); }
.stat-rate {
  font-size: 10px; padding: 0 5px; border-radius: var(--radius-md);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
.stat-rate.bad { background: var(--color-danger-bg); color: var(--color-danger); }
.stat-bars { display: flex; align-items: flex-end; gap: 3px; height: 22px; margin-left: auto; }
.stat-bar {
  width: 9px; height: 100%; display: flex; align-items: flex-end;
  background: var(--color-bg-tertiary); border-radius: 2px; overflow: hidden;
}
.stat-bar .bar-fill {
  width: 100%; background: var(--color-accent); border-radius: 2px;
  min-height: 1px; transition: height var(--transition-fast);
}
.stat-bar.has-fail .bar-fill { background: var(--color-danger); }
.stat-bar.empty .bar-fill { min-height: 0; }

.sched-error {
  margin: 12px 20px 0; padding: 8px 12px; font-size: 12px;
  color: var(--color-danger); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md); flex-shrink: 0;
}

/* 弹窗内校验提示：与页面级 .sched-error 同色系，但去掉外边距（弹窗有自己的内边距） */
.form-err {
  margin: var(--space-3) 0 0; padding: 7px 10px; font-size: 12px;
  color: var(--color-danger); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md);
}

/* 表单里的说明文字（如重试的代价说明）。比标签小一号、不用危险色：
   它是解释而不是错误，染红会让人以为填错了。 */
.form-hint {
  margin: 4px 0 0; font-size: 11px; line-height: 1.5; color: var(--color-text-muted);
}

.sched-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); padding: 14px 20px 16px; }
.sched-empty { text-align: center; padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.empty-icon { opacity: 0.4; margin-bottom: var(--space-2); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); margin-top: var(--space-2); }

.sched-item {
  display: flex; align-items: center; gap: var(--space-3); padding: 12px 14px;
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
.sched-item:hover { background: var(--color-bg-hover); }
.sched-item.disabled { opacity: 0.55; }
/* 拖拽：抓起来的半透明、落点给一条左侧高亮 */
.sched-item.dragging { opacity: 0.45; }
.sched-item.drag-over { border-color: var(--color-accent); box-shadow: inset 0 2px 0 var(--color-accent); }
.drag-handle { flex-shrink: 0; color: var(--color-text-disabled); cursor: grab; margin-right: -6px; }
.sched-item:active .drag-handle { cursor: grabbing; }

.item-icon {
  flex-shrink: 0; width: 34px; height: 34px; display: flex; align-items: center; justify-content: center;
  border-radius: var(--radius-md); color: var(--color-accent-text); background: var(--color-accent);
}
.item-icon.a-app { background: var(--color-accent); }
.item-icon.a-dir { background: var(--color-warning); }
.item-icon.a-url { background: var(--color-success); }
.item-icon.a-command { background: var(--color-cat-command); }
.item-icon.a-http { background: var(--color-cat-http); }

.item-main { flex: 1; min-width: 0; }
.item-line1 { display: flex; align-items: center; gap: 8px; }
.item-name { font-size: 13px; font-weight: 500; color: var(--color-text-primary); }
.item-badge {
  font-size: 10px; padding: 1px 7px; border-radius: var(--radius-md);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
}
.item-target {
  font-size: 12px; color: var(--color-text-secondary); margin-top: 2px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%;
}
.item-meta { font-size: 11px; color: var(--color-text-disabled); margin-top: 4px; display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
.meta-next { color: var(--color-accent); }
.meta-left { color: var(--color-text-muted); }
.meta-last { display: inline-flex; align-items: center; gap: 3px; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meta-last.ok { color: var(--color-text-muted); }
.meta-last.fail { color: var(--color-danger); }

.item-actions { display: flex; align-items: center; gap: 3px; flex-shrink: 0; }
.act {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled);
  border-radius: var(--radius-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.act:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-active); }
.act:disabled { opacity: 0.4; cursor: default; }
.act.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }

/* 同步执行中的转圈 */
.spin { animation: sched-spin 1s linear infinite; }
@keyframes sched-spin { to { transform: rotate(360deg); } }

/* 弹窗 */
.modal-mask {
  position: fixed; inset: 0; background: var(--color-bg-overlay); display: flex;
  align-items: center; justify-content: center; z-index: 100;
}
.modal {
  width: 440px; max-height: 90vh; overflow-y: auto; background: var(--color-surface);
  border-radius: var(--radius-lg); padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.modal-area { height: auto; padding: 8px 10px; resize: vertical; line-height: 1.5; }
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }

/* 分段选择 */
.seg { display: flex; flex-wrap: wrap; gap: 6px; }
.seg-btn {
  display: inline-flex; align-items: center; gap: 4px; padding: 6px 10px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.seg-btn:hover { color: var(--color-text-primary); }
.seg-btn.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }

.wd-row { display: flex; gap: 5px; }
.wd-btn {
  width: 34px; height: 32px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.wd-btn:hover { color: var(--color-text-primary); }
.wd-btn.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }

.toggle-row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3) var(--space-5); margin-top: var(--space-4); }
/* 渠道从属于总开关：左移一点 + 略小，视觉上挂在它下面 */
.toggle-sub { padding-left: var(--space-3); font-size: 11px; color: var(--color-text-muted); }
.toggle-label { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--color-text-secondary); cursor: pointer; margin: 0; }
.toggle-label input { cursor: pointer; }
/* 「机器人地址去设置配」的跳转链接：做成手型+下划线虚线，别用按钮——
   它不是本次操作的动作，只是一条注记。 */
.nf-link { font-size: 11px; color: var(--color-accent); cursor: pointer; text-decoration: underline dotted; }
.nf-link:hover { color: var(--color-accent-hover); }

.modal-actions { display: flex; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-5); }
</style>
