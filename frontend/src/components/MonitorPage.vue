<script setup lang="ts">
defineOptions({ name: 'MonitorPage' })
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { matchesTextOrPinyin } from '../utils/pinyin'
import { useToast } from '../composables/useToast'
import {
  CreateMonitor, ListMonitors, UpdateMonitor,
  DeleteMonitor, SoftDeleteMonitor, RestoreMonitor, ListDeletedMonitors,
  SetMonitorEnabled, SetMonitorSort, SetMonitorMaintenance, CheckMonitorNow,
  GetMonitorLogs, GetMonitorLogsSince, ClearMonitorLogs, ListMonitorStats,
} from '../../bindings/quickdock/services/monitor/monitorservice'
import { useFloatMenu } from '../composables/useFloatMenu'
import type { Monitor } from '../../bindings/quickdock/internal/db/models'
import { Events } from '@wailsio/runtime'
import ConfirmDialog from './ConfirmDialog.vue'
// D9：canvas 图表用色集中到 palette（主题扩展表），canvas 无法吃 CSS var，运行时取此表
import { CHART_COLORS } from '../utils/palette'

// 以下两个结构仅作为返回值中的元素出现，Wails 绑定生成器不会为其单独生成类型，
// 故在前端本地声明（运行时为普通 JSON，字段与后端 json tag 一致）。
interface MonitorLog {
  id: string
  monitorId: string
  checkedAt: string
  checkedTs: number
  status: string
  statusCode: number
  latencyMs: number
  error: string
}
interface MonitorStat {
  monitorId: string
  totalChecks: number
  upChecks: number
  uptimeRatio: number
  avgLatencyMs: number
  lastDownAt: string
}
import {
  Plus, Play, Pencil, Trash2, Globe, Activity, RefreshCw,
  ChevronDown, ChevronUp, CheckCircle2, XCircle, Radio, Download, Search, Power, X, Wrench,
  Bell as BellIcon,
} from '@lucide/vue'

const { t } = useI18n()
const toast = useToast()

const monitors = ref<Monitor[]>([])
const stats = ref<Record<string, MonitorStat>>({})
const logsMap = ref<Record<string, MonitorLog[]>>({})
const expanded = ref<Set<string>>(new Set())
const loading = ref(false)
const error = ref('')

// ---- 分组 ----
// 分组名由用户在建/改时填写，为空即「未分组」。展示顺序：未分组在前、
// 命名分组按名称排——组是用户自己划的，字母序比「谁先建」更易找。
const groupFilter = ref('') // ''=全部；'__none__'=仅看未分组

/** 搜索词：匹配名称/URL/分组名，支持拼音（与命令面板/工作空间同一套 utils）。 */
const query = ref('')

// ---- 多选批量启停 ----
//
// 十几个监控点里想「把 staging 那几条都停掉」，逐个点开关要来回找；
// 停用后开关状态又很容易看漏。多选 + 批量是最短的路径。
const selectedIds = ref<Set<string>>(new Set())
const allVisibleSelected = computed(
  () => visibleMonitors.value.length > 0 && visibleMonitors.value.every(m => selectedIds.value.has(m.id)),
)
const batchBusy = ref(false)

function isSelected(id: string): boolean { return selectedIds.value.has(id) }
function toggleSelect(id: string, force?: boolean) {
  const want = force ?? !selectedIds.value.has(id)
  const next = new Set(selectedIds.value)
  if (want) next.add(id)
  else next.delete(id)
  selectedIds.value = next
}
function toggleSelectAll() {
  const on = !allVisibleSelected.value
  const next = new Set(selectedIds.value)
  for (const m of visibleMonitors.value) {
    if (on) next.add(m.id)
    else next.delete(m.id)
  }
  selectedIds.value = next
}
function clearSelection() { if (selectedIds.value.size) selectedIds.value = new Set() }

/** 批量启用/停用。逐个发请求而不是造一个新的批量接口：
 *  后端 SetMonitorEnabled 是幂等的单条写，N 条最多也就十几毫秒，
 *  为它加一条批量接口反而要额外维护「部分失败怎么回滚」的语义。 */
async function batchSetEnabled(enabled: boolean) {
  const ids = [...selectedIds.value]
  if (!ids.length || batchBusy.value) return
  batchBusy.value = true
  error.value = ''
  const failed: string[] = []
  for (const id of ids) {
    const m = monitors.value.find(x => x.id === id)
    if (!m || m.enabled === enabled) continue
    try {
      unwrap(await SetMonitorEnabled(id, enabled))
    } catch (e) {
      failed.push(m.name)
      error.value = getErrorMessage(e)
    }
  }
  batchBusy.value = false
  clearSelection()
  await refresh()
  if (!failed.length) toast.success(t(enabled ? 'mon_batch_enabled' : 'mon_batch_disabled', { n: ids.length }))
  else toast.error(t('mon_batch_partial', { n: failed.length, names: failed.slice(0, 3).join('、') }))
}

const monitorGroups = computed(() => {
  const names = new Set<string>()
  for (const m of monitors.value) if (m.groupName) names.add(m.groupName)
  return Array.from(names).sort((a, b) => a.localeCompare(b))
})

const hasUngrouped = computed(() => monitors.value.some(m => !m.groupName))

interface MonitorGroup { name: string; items: Monitor[] }
const monitorGroupList = computed<MonitorGroup[]>(() => {
  const byName = new Map<string, Monitor[]>()
  const ungrouped: Monitor[] = []
  for (const m of monitors.value) {
    if (m.groupName) {
      const arr = byName.get(m.groupName) || []
      arr.push(m)
      byName.set(m.groupName, arr)
    } else {
      ungrouped.push(m)
    }
  }
  const groups: MonitorGroup[] = []
  if (ungrouped.length) groups.push({ name: '', items: ungrouped })
  for (const name of Array.from(byName.keys()).sort((a, b) => a.localeCompare(b))) {
    groups.push({ name, items: byName.get(name)! })
  }
  // 分组筛选：只看某一组（含「未分组」）
  let out = groups
  if (groupFilter.value === '__none__') out = groups.filter(g => g.name === '')
  else if (groupFilter.value) out = groups.filter(g => g.name === groupFilter.value)
  // 搜索最后做：作用在已筛选的组内，命中为空的分组整个不显示——
  // 否则搜一个词会留下五六个空标题，看着像没筛选成功。
  const q = query.value.trim()
  if (!q) return out
  const hit = out
    .map(g => ({ name: g.name, items: g.items.filter(m => matchesTextOrPinyin([m.name, m.url, m.groupName], q, 'monitor')) }))
    .filter(g => g.items.length > 0)
  return hit
})

/** 当前列表里可见的监控点（搜索/分组筛选之后），批量操作的「全选」以它为准。 */
const visibleMonitors = computed<Monitor[]>(() => monitorGroupList.value.flatMap(g => g.items))

// ---- 汇总指标 ----
const summary = computed(() => {
  const list = monitors.value
  const up = list.filter(m => m.lastStatus === 'up').length
  const down = list.filter(m => m.lastStatus === 'down').length
  let lat = 0, n = 0
  for (const m of list) {
    if (m.lastStatus === 'up' && m.lastLatencyMs > 0) { lat += m.lastLatencyMs; n++ }
  }
  return {
    total: list.length,
    up, down,
    avgLatency: n ? Math.round(lat / n) : 0,
  }
})

// ---- 加载 ----
async function refresh() {
  loading.value = true
  error.value = ''
  try {
    // 两条查询无依赖，并行发起，减少一次 Wails 往返（每次 binding 调用都是序列化 round-trip）
    const [mons, st] = await Promise.all([ListMonitors(), ListMonitorStats()])
    monitors.value = unwrap<Monitor[]>(mons) ?? []
    const statsArr = unwrap<MonitorStat[]>(st) ?? []
    const map: Record<string, MonitorStat> = {}
    for (const s of statsArr) map[s.monitorId] = s
    stats.value = map
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function statOf(id: string): MonitorStat | undefined { return stats.value[id] }

// ---- 相对时间 ----
function relTime(ts: number): string {
  if (!ts) return t('mon_never')
  const diff = Math.max(0, Math.floor(Date.now() / 1000 - ts))
  if (diff < 60) return diff + t('mon_sec_ago')
  if (diff < 3600) return Math.floor(diff / 60) + t('mon_min_ago')
  if (diff < 86400) return Math.floor(diff / 3600) + t('mon_hour_ago')
  return Math.floor(diff / 86400) + t('mon_day_ago')
}

function uptimeText(id: string): string {
  const s = statOf(id)
  if (!s || s.totalChecks === 0) return '—'
  return s.uptimeRatio.toFixed(1) + '%'
}

// 列表里补上后端早就算好、前端却一直没显示的三个统计量。
// 只有 uptimeRatio 在用的时候，「在线率 99.9%」到底是 10 次里挂 1 次还是
// 10000 次里挂 10 次完全看不出来 —— totalChecks 就是这个分母。
function checksOf(id: string): number { return statOf(id)?.totalChecks ?? 0 }
function avgLatOf(id: string): number { return statOf(id)?.avgLatencyMs ?? 0 }
function lastDownText(id: string): string {
  const v = statOf(id)?.lastDownAt || ''
  return v ? v.slice(5, 16) : '' // YYYY-MM-DD HH:MM:SS → MM-DD HH:MM
}

function expectedLabel(e: string): string {
  if (e === '') return 'any'
  return e
}

// ---- 表单（新增 / 编辑共用） ----
const showModal = ref(false)
const editingId = ref('')
const modalTitle = computed(() => editingId.value ? t('mon_edit') : t('mon_add'))

const fName = ref('')
const fUrl = ref('')
const fMethod = ref('GET')
const fIntervalVal = ref(300) // 默认 300 秒
const fTimeout = ref(10)
const fExpected = ref('2xx')
const fFollow = ref(true)
const fEnabled = ref(true)
const fNotifyDown = ref(true)
const fNotifyUp = ref(true)
const fSkipTLS = ref(false) // 忽略证书错误（默认关闭）
const fDownAlertThreshold = ref(1) // 误报抑制：连续 N 次失败才告警
const fGroup = ref('') // 分组名（空=未分组）
const fLatencyAlert = ref(0) // 延迟告警阈值 ms（0=关闭）

// 6.1 SSL 证书告警提前天数；6.2 内容匹配
const fCertWarnDays = ref(14)
const fContentMatchType = ref('none') // none | contains | not_contains | regex
const fContentMatchPattern = ref('')
// 请求头 / 请求体：这两列是随「POST/PUT/PATCH 探针不可用」的修复一起加的，
// bindings 要重新生成才会带上字段，故用交叉类型补齐，避免整页类型检查被卡住
//（不手改 frontend/bindings，那是生成物）。
type MonitorWithHTTP = Monitor & { httpHeaders?: string; httpBody?: string }
const fHeaders = ref('')
const fBody = ref('')
const NEEDS_BODY = ['POST', 'PUT', 'PATCH'] as const
const showBodyFields = computed(() => (NEEDS_BODY as readonly string[]).includes(fMethod.value))

const MATCH_TYPES = ['none', 'contains', 'not_contains', 'regex']

const METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'PATCH']
const EXPECTED = ['2xx', '3xx', '4xx', '5xx', '200', 'any']

function intervalVal(): number {
  return Math.max(5, Math.floor(fIntervalVal.value || 300))
}

/** 机器人 Webhook 是全局配置，跳设置页去改（本页不重复实现一份表单）。 */
function openNotifySettings() {
  try { Events.Emit('app:open-settings', 'notify') } catch { /* 事件通道未就绪时静默，不阻塞新建监控 */ }
}

function openCreate() {
  formErr.value = ''
  editingId.value = ''
  fName.value = ''
  fUrl.value = ''
  fMethod.value = 'GET'
  fIntervalVal.value = 300
  fTimeout.value = 10
  fExpected.value = '2xx'
  fFollow.value = true
  fEnabled.value = true
  fNotifyDown.value = true
  fNotifyUp.value = true
  fSkipTLS.value = false
  fCertWarnDays.value = 14
  fDownAlertThreshold.value = 1
  fGroup.value = ''
  fLatencyAlert.value = 0
  fContentMatchType.value = 'none'
  fContentMatchPattern.value = ''
  fHeaders.value = ''
  fBody.value = ''
  showModal.value = true
}

function openEdit(m: Monitor) {
  formErr.value = ''
  editingId.value = m.id
  fName.value = m.name
  fUrl.value = m.url
  fMethod.value = m.method || 'GET'
  fIntervalVal.value = Math.max(5, m.intervalSec || 60)
  fTimeout.value = m.timeoutSec || 10
  fExpected.value = m.expectedStatus === '' ? 'any' : m.expectedStatus
  fFollow.value = m.followRedirects
  fEnabled.value = m.enabled
  fNotifyDown.value = m.notifyDown
  fNotifyUp.value = m.notifyUp
  fSkipTLS.value = m.skipTLSVerify
  fCertWarnDays.value = m.certWarnDays > 0 ? m.certWarnDays : 14
  fContentMatchType.value = m.contentMatchType || 'none'
  fContentMatchPattern.value = m.contentMatchPattern || ''
  const mh = m as MonitorWithHTTP
  fHeaders.value = mh.httpHeaders || ''
  fBody.value = mh.httpBody || ''
  fDownAlertThreshold.value = m.downAlertThreshold > 0 ? m.downAlertThreshold : 1
  fGroup.value = m.groupName || ''
  fLatencyAlert.value = m.latencyAlertMs > 0 ? m.latencyAlertMs : 0
  showModal.value = true
}

function buildPayload(): MonitorWithHTTP {
  return {
    id: editingId.value,
    name: fName.value.trim(),
    url: fUrl.value.trim(),
    method: fMethod.value,
    intervalSec: intervalVal(),
    timeoutSec: Math.max(1, Math.floor(fTimeout.value || 10)),
    expectedStatus: fExpected.value === 'any' ? '' : fExpected.value,
    followRedirects: fFollow.value,
    enabled: fEnabled.value,
    notifyDown: fNotifyDown.value,
    notifyUp: fNotifyUp.value,
    skipTLSVerify: fSkipTLS.value,
    certWarnDays: Math.max(1, Math.floor(fCertWarnDays.value || 14)),
    certExpiresAt: 0,
    lastCertWarned: 0,
    contentMatchType: fContentMatchType.value,
    contentMatchPattern: fContentMatchPattern.value.trim(),
    downAlertThreshold: Math.max(1, Math.floor(fDownAlertThreshold.value || 1)),
    groupName: fGroup.value.trim(),
    latencyAlertMs: Math.max(0, Math.floor(fLatencyAlert.value || 0)),
    consecutiveDown: 0,
    // 只有带 body 的方法才发送这两个值：GET/HEAD 上带 body 请求体会被部分服务端直接拒，
    // 用户从 POST 切回 GET 后残留的 body 不该偷偷生效。
    httpHeaders: fHeaders.value.trim(),
    httpBody: showBodyFields.value ? fBody.value : '',
    lastStatus: '', lastCheckedAt: '', lastCheckedTs: 0, lastLatencyMs: 0,
    lastStatusCode: 0, lastError: '', sort: 0, createdAt: '',
  } as MonitorWithHTTP
}

const saving = ref(false)
// 弹窗内的校验/保存错误：页面级 error 被遮罩盖着看不见，等于「点保存没反应」
const formErr = ref('')
async function save() {
  const p = buildPayload()
  formErr.value = ''
  if (!p.name) { formErr.value = t('mon_err_name'); return }
  if (!p.url) { formErr.value = t('mon_err_url'); return }
  saving.value = true
  try {
    if (editingId.value) await unwrap(await UpdateMonitor(p))
    else await unwrap(await CreateMonitor(p))
    showModal.value = false
    await refresh()
  } catch (e) {
    formErr.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(m: Monitor) {
  try { await unwrap(await SetMonitorEnabled(m.id, !m.enabled)); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}

const checkingId = ref('')
async function checkNow(m: Monitor) {
  checkingId.value = m.id
  error.value = ''
  try {
    const r = await CheckMonitorNow(m.id)
    if (r && r.code !== 0) error.value = r.msg
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) } finally { checkingId.value = '' }
}

async function remove(m: Monitor) {
  delTarget.value = m
  showDelConfirm.value = true
}

const delTarget = ref<Monitor | null>(null)
const showDelConfirm = ref(false)

// 删除改为软删进回收站（与定时任务一致），避免「手滑删一个监控、几年日志全没」。
async function confirmDel() {
  const m = delTarget.value
  if (!m) return
  showDelConfirm.value = false
  delTarget.value = null
  try { await unwrap(await SoftDeleteMonitor(m.id)); expanded.value.delete(m.id); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 回收站 ----
// 与硬删不同，软删只打标，日志与运行数据都留着。误删可一键恢复，
// 彻底删除（真要抹掉）留在回收站里做，平时不会误触。
const viewMode = ref<'active' | 'recycle'>('active')
const deletedMonitors = ref<Monitor[]>([])
const loadingDeleted = ref(false)

async function loadDeleted() {
  loadingDeleted.value = true
  try {
    deletedMonitors.value = unwrap<Monitor[]>(await ListDeletedMonitors()) ?? []
  } catch (e) { error.value = getErrorMessage(e) } finally { loadingDeleted.value = false }
}

async function switchView(v: 'active' | 'recycle') {
  viewMode.value = v
  clearSelection()
  if (v === 'recycle') await loadDeleted()
  else await refresh()
}

async function restoreMonitor(m: Monitor) {
  try { await unwrap(await RestoreMonitor(m.id)); await loadDeleted() } catch (e) { error.value = getErrorMessage(e) }
}

const purgeTarget = ref<Monitor | null>(null)
const showPurgeConfirm = ref(false)
function askPurge(m: Monitor) { purgeTarget.value = m; showPurgeConfirm.value = true }
async function confirmPurge() {
  const m = purgeTarget.value
  if (!m) return
  showPurgeConfirm.value = false
  purgeTarget.value = null
  try { await unwrap(await DeleteMonitor(m.id)); await loadDeleted() } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 维护窗口 ----
// 维护后端时不想被一连串误报告警刷屏：设个 1 小时窗口，期间检查器直接跳过本监控，
// 不写日志也不告警。到点自动恢复，不用回头改配置。
function inMaintenance(m: Monitor): boolean {
  return (m.maintenanceUntil || 0) > Math.floor(Date.now() / 1000)
}
function maintRemaining(m: Monitor): string {
  const left = (m.maintenanceUntil || 0) - Math.floor(Date.now() / 1000)
  if (left <= 0) return ''
  if (left < 3600) return Math.ceil(left / 60) + t('mon_min_short')
  return (left / 3600).toFixed(1) + t('mon_hour_short')
}
async function toggleMaintenance(m: Monitor) {
  try {
    const until = inMaintenance(m) ? 0 : Math.floor(Date.now() / 1000) + 3600
    await unwrap(await SetMonitorMaintenance(m.id, until))
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 拖拽排序 ----
// 复用定时任务/笔记同一套「按可见顺序重写 sort=i*10」：拖一下只动 sort 一列，
// 不碰任何编辑字段，也绝不重算运行数据。分组只是展示层——拖动会反映到全局 sort 顺序，
// 跨组拖动保留原分组归属（组是用户自己划的，拖排序不该偷偷改它）。
const dragId = ref('')
function onDragStart(e: DragEvent, m: Monitor) {
  dragId.value = m.id
  e.dataTransfer?.setData('text/plain', m.id)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}
function onDragOver(e: DragEvent, m: Monitor) {
  if (!dragId.value || dragId.value === m.id) return
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
}
async function onDrop(target: Monitor) {
  const from = dragId.value
  dragId.value = ''
  if (!from || from === target.id) return
  const list = visibleMonitors.value
  const fromIdx = list.findIndex(x => x.id === from)
  if (fromIdx < 0) return
  const next = list.slice()
  const [moved] = next.splice(fromIdx, 1)
  const toIdx = next.findIndex(x => x.id === target.id)
  if (toIdx < 0) return
  next.splice(toIdx, 0, moved)
  const busy = new Set<string>()
  try {
    for (let i = 0; i < next.length; i++) {
      if (next[i].sort !== i * 10) {
        busy.add(next[i].id)
        await SetMonitorSort(next[i].id, i * 10)
      }
    }
    // 乐观更新：本地立刻重排，省一次往返闪烁
    monitors.value = next.slice()
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 展开日志 ----
function isExpanded(id: string) { return expanded.value.has(id) }

// 6.3 趋势图时间范围切换
const LOG_RANGES = [
  { value: '24h', sec: 86400, label: '24h' },
  { value: '7d', sec: 86400 * 7, label: '7d' },
  { value: 'all', sec: 0, label: '全部' },
]
// 范围按监控各存一份。用单个全局值时，同页展开多条会出现
// 「切了 A 的范围，B 的图也跟着变了，但 B 的数据没重载」——图和数据对不上。
const logRanges = ref<Record<string, string>>({})
function logRangeOf(id: string): string { return logRanges.value[id] || '24h' }
function setLogRange(id: string, v: string) { logRanges.value[id] = v }

function rangeSec(id: string): number {
  const r = LOG_RANGES.find(x => x.value === logRangeOf(id))
  return r ? r.sec : 86400
}

async function loadLogs(id: string) {
  try {
    const rs = rangeSec(id)
    const since = rs > 0 ? Math.floor(Date.now() / 1000) - rs : 0
    // 绘图点上限 2000：再多 canvas 也画不出区别，但序列化与解析会明显变慢。
    // 后端保留上限已提到 20000 条，所以「7d / 全部」拿到的确实是可用区间内的数据。
    logsMap.value[id] = unwrap<MonitorLog[]>(await GetMonitorLogsSince(id, since, 2000)) ?? []
  } catch { /* ignore */ }
}

// 切换时间范围后重新加载并重绘
async function onRangeChange(id: string) {
  await loadLogs(id)
  await nextTick()
  drawChart(id)
}

// 6.1 证书到期显示
function certText(m: Monitor): string | null {
  if (!m.certExpiresAt || m.certExpiresAt <= 0) return null
  // 括号不能省：/ 的优先级高于 -，原式 Math.floor(a - b/1000)/86400 会让
  // Math.floor 作用在「秒」上而不是「天」上，天数取整形同虚设。
  const days = (m.certExpiresAt - Date.now() / 1000) / 86400
  if (days < 0) return t('mon_cert_expired', { d: Math.ceil(-days) })
  if (days < 1) return t('mon_cert_expire_soon')
  return t('mon_cert_expires', { d: Math.ceil(days) })
}
function certWarn(m: Monitor): boolean {
  if (!m.certExpiresAt || m.certExpiresAt <= 0) return false
  return m.certExpiresAt - Date.now() / 1000 < m.certWarnDays * 86400
}

// ---- 曲线图 tooltip ----
// 存储每个图的数据点像素位置，用于鼠标悬停检测
const chartPoints = new Map<string, Array<{ x: number; y: number; data: MonitorLog }>>()
const tooltip = ref<{ show: boolean; data: MonitorLog | null }>({
  show: false, data: null,
})
// floating-ui：跟随鼠标位置（偏移到右下方），靠边自动内收
const tipCtx = useFloatMenu({ placement: 'right-start', offset: { mainAxis: 14, crossAxis: -8 } })

function onChartMove(e: MouseEvent, id: string) {
  const pts = chartPoints.get(id)
  if (!pts || pts.length === 0) { tooltip.value.show = false; tipCtx.hide(); return }
  const rect = (e.currentTarget as HTMLCanvasElement).getBoundingClientRect()
  const mx = e.clientX - rect.left
  const my = e.clientY - rect.top
  const threshold = 28
  let minDist = Infinity
  let nearest: typeof pts[0] | null = null
  for (const p of pts) {
    const d = Math.sqrt((p.x - mx) ** 2 + (p.y - my) ** 2)
    if (d < minDist && d < threshold) { minDist = d; nearest = p }
  }
  if (nearest) {
    tooltip.value = { show: true, data: nearest.data }
    if (tipCtx.visible.value) tipCtx.updateAt(e.clientX, e.clientY)
    else tipCtx.showAt(e.clientX, e.clientY)
  } else {
    tooltip.value.show = false
    tipCtx.hide()
  }
}
function onChartLeave() {
  tooltip.value.show = false
  tipCtx.hide()
}

function drawChart(id: string) {
  const logs = logsMap.value[id]
  if (!logs || logs.length === 0) return
  const canvas = document.getElementById('chart-' + id) as HTMLCanvasElement | null
  if (!canvas) return

  const dpr = window.devicePixelRatio || 1
  const rect = canvas.getBoundingClientRect()
  canvas.width = rect.width * dpr
  canvas.height = rect.height * dpr
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  ctx.scale(dpr, dpr)
  const W = rect.width, H = rect.height

  // 边距
  const PL = 48, PR = 16, PT = 12, PB = 28
  const CX = W - PL - PR  // chart area width
  const CY = H - PT - PB  // chart area height

  // 背景
  ctx.fillStyle = CHART_COLORS.bg
  ctx.fillRect(0, 0, W, H)

  // 数据：取最近 50 条，时间正序（后端返回倒序）
  const data = [...logs].reverse()
  const n = data.length
  if (n < 2) {
    ctx.fillStyle = CHART_COLORS.textMuted
    ctx.font = '12px sans-serif'
    ctx.textAlign = 'center'
    ctx.fillText(t('monitorNeedMoreData'), W / 2, H / 2)
    return
  }

  // 找出延迟最大值 + 超时
  let maxLat = 0
  for (const d of data) {
    if (d.latencyMs > maxLat) maxLat = d.latencyMs
  }
  maxLat = Math.max(maxLat, 100) // 至少 100ms
  // Y 轴刻度：取整
  const yStep = maxLat <= 200 ? 50 : maxLat <= 500 ? 100 : maxLat <= 2000 ? 500 : 1000
  const yMax = Math.ceil(maxLat / yStep) * yStep
  const timeout = monitors.value.find(m => m.id === id)?.timeoutSec || 10

  function xPos(i: number): number {
    if (n <= 1) return PL + CX / 2
    return PL + (i / (n - 1)) * CX
  }
  function yPos(ms: number): number {
    if (yMax <= 0) return PT
    return PT + CY - (ms / yMax) * CY
  }

  // ---- 网格 ----
  ctx.strokeStyle = CHART_COLORS.grid
  ctx.lineWidth = 1
  for (let y = 0; y <= yMax; y += yStep) {
    const yy = yPos(y)
    ctx.beginPath(); ctx.moveTo(PL, yy); ctx.lineTo(W - PR, yy); ctx.stroke()
    ctx.fillStyle = CHART_COLORS.textMuted
    ctx.font = '10px sans-serif'
    ctx.textAlign = 'right'
    ctx.fillText(y + t('monitorMsSuffix'), PL - 6, yy + 3)
  }

  // 超时线
  const timeoutY = yPos(timeout * 1000)
  if (timeoutY > PT) {
    ctx.strokeStyle = CHART_COLORS.timeoutLine
    ctx.setLineDash([4, 4])
    ctx.beginPath(); ctx.moveTo(PL, timeoutY); ctx.lineTo(W - PR, timeoutY); ctx.stroke()
    ctx.setLineDash([])
    ctx.fillStyle = CHART_COLORS.timeoutText
    ctx.font = '9px sans-serif'
    ctx.textAlign = 'left'
    ctx.fillText(t('monitorTimeoutLabel', { seconds: timeout }), PL + 4, timeoutY - 3)
  }

  // ---- X 轴标签（最多显示 6 个） ----
  const xLabelCount = Math.min(n, 6)
  const xLabelStep = Math.max(1, Math.floor((n - 1) / (xLabelCount - 1)))
  ctx.fillStyle = CHART_COLORS.textMuted2
  ctx.font = '9px sans-serif'
  ctx.textAlign = 'center'
  for (let i = 0; i < n; i += xLabelStep) {
    const label = data[i].checkedAt.slice(11, 19) || ''
    if (label) ctx.fillText(label, xPos(i), H - 6)
  }
  // 最后一个
  const lastLabel = data[n - 1].checkedAt.slice(11, 19) || ''
  if (lastLabel && (n - 1) % xLabelStep !== 0) ctx.fillText(lastLabel, xPos(n - 1), H - 6)

  // ---- 折线 ----
  // 先画线
  ctx.beginPath()
  for (let i = 0; i < n; i++) {
    const x = xPos(i), y = yPos(data[i].latencyMs)
    if (i === 0) ctx.moveTo(x, y)
    else ctx.lineTo(x, y)
  }
  ctx.strokeStyle = CHART_COLORS.accent
  ctx.lineWidth = 2
  ctx.stroke()

  // 再画点 + 上下箭头（根据 status）
  for (let i = 0; i < n; i++) {
    const d = data[i]
    const x = xPos(i), y = yPos(d.latencyMs)
    const r = 3.5
    if (d.status === 'up') {
      ctx.fillStyle = CHART_COLORS.ok
    } else {
      ctx.fillStyle = CHART_COLORS.danger
    }
    ctx.beginPath()
    ctx.arc(x, y, r, 0, Math.PI * 2)
    ctx.fill()
    // 失败的显示小箭头或 x
    if (d.status !== 'up') {
      ctx.strokeStyle = CHART_COLORS.danger
      ctx.lineWidth = 1.5
      const s = 4
      ctx.beginPath()
      ctx.moveTo(x - s, y - s); ctx.lineTo(x + s, y + s)
      ctx.moveTo(x + s, y - s); ctx.lineTo(x - s, y + s)
      ctx.stroke()
    }
  }

  // 右上角图例
  const total = n, upCnt = data.filter(d => d.status === 'up').length
  ctx.fillStyle = CHART_COLORS.legendBg
  ctx.fillRect(W - PR - 110, PT + 2, 108, 38)
  ctx.font = '10px sans-serif'
  ctx.textAlign = 'left'
  ctx.fillStyle = CHART_COLORS.textMuted3
  ctx.fillText(t('monitorChecks', { count: total }), W - PR - 102, PT + 14)
  ctx.fillStyle = CHART_COLORS.ok
  ctx.fillText(t('monitorHealthy', { count: upCnt }), W - PR - 102, PT + 27)
  ctx.fillStyle = CHART_COLORS.danger
  ctx.fillText(t('monitorFaults', { count: total - upCnt }), W - PR - 50, PT + 27)
  // 存储像素坐标供 tooltip 悬停检测
  const pts: Array<{ x: number; y: number; data: MonitorLog }> = []
  for (let i = 0; i < n; i++) {
    pts.push({ x: xPos(i), y: yPos(data[i].latencyMs), data: data[i] })
  }
  chartPoints.set(id, pts)
}

// 展开日志时等待 DOM 渲染完再画图
async function toggleExpand(m: Monitor) {
  if (expanded.value.has(m.id)) { expanded.value.delete(m.id); return }
  expanded.value.add(m.id)
  await loadLogs(m.id)
  await nextTick()
  drawChart(m.id)
}

function chartId(id: string) { return 'chart-' + id }

async function clearLogs(m: Monitor) {
  try { await unwrap(await ClearMonitorLogs(m.id)); logsMap.value[m.id] = [] } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 导出 CSV ----
// 导出所选时间范围的全量日志，而不是画图用的那份 logsMap：
// 那条链路为了 canvas 渲染把上限压到 2000 条，拿它导出等于「选 7 天只导出一段」。
// 两个细节：① 带 UTF-8 BOM——Windows 用户默认双击用 Excel 打开，无 BOM 中文必乱码；
// ② 后端返回倒序（新→旧），导出转正序，符合「从早读到晚」的习惯。
const CSV_MAX_ROWS = 20000 // 与后端 monitor_logs 的保留上限一致

async function exportCsv(m: Monitor) {
  try {
    const rs = rangeSec(m.id)
    const since = rs > 0 ? Math.floor(Date.now() / 1000) - rs : 0
    const logs = unwrap<MonitorLog[]>(await GetMonitorLogsSince(m.id, since, CSV_MAX_ROWS)) ?? []
    if (logs.length === 0) return
    const esc = (v: string | number) => '"' + String(v ?? '').replace(/"/g, '""') + '"'
    const lines = [['checked_at', 'status', 'status_code', 'latency_ms', 'error'].map(esc).join(',')]
    for (const l of [...logs].reverse()) {
      lines.push([l.checkedAt, l.status, l.statusCode, l.latencyMs, l.error].map(esc).join(','))
    }
    const blob = new Blob(['\uFEFF' + lines.join('\r\n')], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    // 文件名去掉 Windows 非法字符，否则下载静默失败
    a.download = 'monitor-' + m.name.replace(/[\\/:*?"<>|]/g, '_') + '-' + new Date().toISOString().slice(0, 10) + '.csv'
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// ---- 停机日历 ----
// 按日聚合当前范围日志的失败次数，格子色深表示当日失败强度。
// 数据源就是已加载的 logsMap：切换时间范围日历跟着变，不额外请求。
// 跨度取日志覆盖的日期（对齐到周一、最多 12 周）——24h 范围只亮一两天，
// 全部范围则是一幅停机热力图。
interface CalDay { key: string; down: number; total: number }
const CAL_MAX_DAYS = 84

function fmtDay(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function calendarDaysFor(id: string): CalDay[] {
  const logs = logsMap.value[id] || []
  const byDay = new Map<string, { down: number; total: number }>()
  let minTs = Infinity, maxTs = -Infinity
  for (const l of logs) {
    const ts = l.checkedTs || Date.parse(l.checkedAt)
    if (!ts || isNaN(ts)) continue
    if (ts < minTs) minTs = ts
    if (ts > maxTs) maxTs = ts
    const day = l.checkedAt ? l.checkedAt.slice(0, 10) : fmtDay(new Date(ts))
    let e = byDay.get(day)
    if (!e) { e = { down: 0, total: 0 }; byDay.set(day, e) }
    e.total++
    if (l.status !== 'up') e.down++
  }
  if (minTs === Infinity) return []
  // 对齐到周一，末尾至少铺到今天
  const start = new Date(Math.min(minTs, Date.now()))
  start.setHours(0, 0, 0, 0)
  start.setDate(start.getDate() - ((start.getDay() + 6) % 7))
  const end = new Date(Math.max(maxTs, Date.now()))
  end.setHours(0, 0, 0, 0)
  const days: CalDay[] = []
  for (let d = new Date(start); d <= end && days.length < CAL_MAX_DAYS; d.setDate(d.getDate() + 1)) {
    const key = fmtDay(d)
    const e = byDay.get(key)
    days.push({ key, down: e?.down || 0, total: e?.total || 0 })
  }
  return days
}

// 0=无失败；1-3 按当日失败占比升档。无数据的格子单独一档（lv-x），
 // 免得「没探测」和「全天在线」看起来一样。
function calLevel(d: CalDay): string {
  if (d.total === 0) return 'x'
  if (d.down === 0) return '0'
  const r = d.down / d.total
  return r <= 0.25 ? '1' : r <= 0.5 ? '2' : '3'
}
function calTip(d: CalDay): string {
  if (d.total === 0) return d.key + ' · ' + t('mon_cal_nodata')
  return t('mon_cal_tip', { date: d.key, down: d.down, total: d.total })
}

// ---- 自动刷新设置（页面级偏好，存 localStorage） ----
// 0 = 关闭；其余为间隔秒数
const REFRESH_OPTIONS = [0, 5, 10, 15, 30, 60]
const refreshSec = ref(5)
const LS_REFRESH_KEY = 'quickdock.monitor.refreshSec'

if (typeof localStorage !== 'undefined') {
  const saved = localStorage.getItem(LS_REFRESH_KEY)
  if (saved !== null) {
    const n = parseInt(saved, 10)
    if (REFRESH_OPTIONS.includes(n)) refreshSec.value = n
  }
}

let pollTimer: ReturnType<typeof setTimeout> | undefined
let polling = false

// setTimeout 链式轮询：上一次 refresh 完成后才排下一次，
// 避免 setInterval 在慢请求时回调叠加（且 polling 标志兜底防重入）
async function pollTick() {
  if (polling) { scheduleNext(); return }
  polling = true
  try {
    await refresh()
    // 重新加载展开的监控日志并重绘图表
    for (const id of expanded.value) {
      await loadLogs(id)
      drawChart(id)
    }
  } catch (_) { /* refresh/loadLogs 内部已处理错误，此处仅兜底 */ }
  polling = false
  scheduleNext()
}

function scheduleNext() {
  if (refreshSec.value <= 0) return
  pollTimer = setTimeout(pollTick, refreshSec.value * 1000)
}

function applyAutoRefresh() {
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = undefined }
  if (refreshSec.value > 0) scheduleNext()
}

watch(refreshSec, (v) => {
  if (typeof localStorage !== 'undefined') localStorage.setItem(LS_REFRESH_KEY, String(v))
  applyAutoRefresh()
})

onMounted(() => {
  refresh()
  applyAutoRefresh()
})

onUnmounted(() => {
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = undefined }
})
</script>

<template>
  <div class="mon-page">
    <!-- 顶部栏 -->
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('mon_title') }}</h2>
        <span class="qd-page-sub">{{ t('mon_subtitle') }}</span>
      </div>
      <div class="qd-page-actions">
        <div class="refresh-control">
          <RefreshCw :size="14" />
          <select v-model.number="refreshSec" class="refresh-select" :title="t('mon_auto_refresh')">
            <option v-for="o in REFRESH_OPTIONS" :key="o" :value="o">
              {{ o === 0 ? t('mon_refresh_off') : o + t('mon_refresh_sec') }}
            </option>
          </select>
        </div>
        <!-- 机器人地址是全局配置（设置 → 机器人通知），这里只跳转，不再重复弹一份表单 -->
        <button class="qd-btn" :title="t('notifyGoSettings')" @click="openNotifySettings">
          <BellIcon :size="14" /> {{ t('mon_nf_title') }}
        </button>
        <button class="qd-btn qd-btn-primary" @click="openCreate"><Plus :size="14" /> {{ t('mon_add') }}</button>
      </div>
    </div>

    <div v-if="error" class="mon-error">{{ error }}</div>

    <!-- 汇总条 -->
    <div v-if="monitors.length || (viewMode === 'recycle' && deletedMonitors.length)" class="mon-summary">
      <div class="sum-item"><span class="sum-num">{{ summary.total }}</span><span class="sum-lbl">{{ t('mon_sum_total') }}</span></div>
      <div class="sum-item up"><span class="sum-num">{{ summary.up }}</span><span class="sum-lbl">{{ t('mon_sum_up') }}</span></div>
      <div class="sum-item down"><span class="sum-num">{{ summary.down }}</span><span class="sum-lbl">{{ t('mon_sum_down') }}</span></div>
      <div class="sum-item"><span class="sum-num">{{ summary.avgLatency }}<i>ms</i></span><span class="sum-lbl">{{ t('mon_sum_latency') }}</span></div>
    </div>

    <!-- 视图切换：活动列表 / 回收站 -->
    <div class="mon-viewtabs">
      <button :class="['vt-tab', { active: viewMode === 'active' }]" @click="switchView('active')">
        {{ t('mon_view_active') }}<span v-if="summary.total" class="vt-count">{{ summary.total }}</span>
      </button>
      <button :class="['vt-tab', { active: viewMode === 'recycle' }]" @click="switchView('recycle')">
        {{ t('mon_view_recycle') }}<span v-if="deletedMonitors.length" class="vt-count warn">{{ deletedMonitors.length }}</span>
      </button>
    </div>

    <!-- 分组筛选（有命名分组时才出现，否则纯属噪音） -->
    <div v-if="monitorGroups.length" class="group-filter">
      <button :class="['group-chip', { active: groupFilter === '' }]" @click="groupFilter = ''">
        {{ t('mon_group_all') }}
      </button>
      <button
        v-for="g in monitorGroups"
        :key="g"
        :class="['group-chip', { active: groupFilter === g }]"
        @click="groupFilter = g"
      >{{ g }}</button>
      <button v-if="hasUngrouped" :class="['group-chip', { active: groupFilter === '__none__' }]" @click="groupFilter = '__none__'">
        {{ t('mon_group_none') }}
      </button>
    </div>

    <!-- 搜索 + 批量操作条 -->
    <div v-if="monitors.length >= 4" class="mon-tools">
      <label v-if="visibleMonitors.length > 1" class="mon-selall" :title="t('mon_sel_all')">
        <input type="checkbox" :checked="allVisibleSelected" @change="toggleSelectAll" />
      </label>
      <div class="mon-search">
        <Search :size="13" />
        <input v-model="query" class="mon-search-input" :placeholder="t('mon_search_ph')" />
        <button v-if="query" class="mon-search-clear" @click="query = ''"><X :size="12" /></button>
      </div>
      <span v-if="query" class="mon-search-count">{{ t('mon_search_count', { n: visibleMonitors.length }) }}</span>
      <span class="mon-tools-fill" />
      <template v-if="selectedIds.size">
        <span class="mon-sel-count">{{ t('mon_sel_count', { n: selectedIds.size }) }}</span>
        <button class="mon-selbtn" :disabled="batchBusy" @click="batchSetEnabled(true)">
          <Power :size="12" />{{ t('mon_batch_on') }}
        </button>
        <button class="mon-selbtn" :disabled="batchBusy" @click="batchSetEnabled(false)">
          <Power :size="12" />{{ t('mon_batch_off') }}
        </button>
        <button class="mon-selbtn" @click="clearSelection">{{ t('mon_sel_clear') }}</button>
      </template>
    </div>

    <!-- 列表（活动监控） -->
    <div v-if="viewMode === 'active'" class="mon-list">
      <div v-if="!loading && monitors.length === 0" class="mon-empty">
        <Radio :size="30" class="empty-icon" />
        <p>{{ t('mon_empty') }}</p>
        <p class="empty-hint">{{ t('mon_empty_hint') }}</p>
      </div>

      <template v-for="g in monitorGroupList" :key="g.name || '__none__'">
        <!-- 分组头：多个组并列时才需要，单独一组时是冗余信息 -->
        <div v-if="monitorGroupList.length > 1" class="group-head">
          <span class="group-head-name">{{ g.name || t('mon_group_none') }}</span>
          <span class="group-head-count">{{ g.items.length }}</span>
          <!-- 组头右侧的「全选本组」：搜索/筛选之后，只想批量动眼前这一组 -->
          <button v-if="g.items.length > 1" class="group-sel" @click="g.items.forEach(m => toggleSelect(m.id, !g.items.every(x => isSelected(x.id))))">
            {{ g.items.every(x => isSelected(x.id)) ? t('mon_sel_none') : t('mon_sel_group') }}
          </button>
        </div>
        <div
          v-for="m in g.items"
          :key="m.id"
          :class="['mon-item', { disabled: !m.enabled, picked: isSelected(m.id), dragging: dragId === m.id }]"
          draggable="true"
          @dragstart="onDragStart($event, m)"
          @dragover="onDragOver($event, m)"
          @drop="onDrop(m)"
          @dragend="dragId = ''"
        >
        <div class="item-head" @click="toggleExpand(m)">
          <input
            type="checkbox"
            class="mon-check"
            :checked="isSelected(m.id)"
            :title="t('mon_sel_hint')"
            @click.stop
            @change="toggleSelect(m.id)"
          />
          <span :class="['status-dot', m.lastStatus || 'unknown']"></span>
          <div class="item-main">
            <div class="item-line1">
              <span class="item-name">{{ m.name }}</span>
              <span v-if="m.lastStatus" :class="['status-badge', m.lastStatus]">
                {{ m.lastStatus === 'up' ? t('mon_up') : t('mon_down') }}
              </span>
            </div>
            <div class="item-url" :title="m.url"><Globe :size="11" /> {{ m.url }}</div>
            <div class="item-meta">
              <span class="meta-up" :title="t('mon_checks_ttl')">{{ t('mon_uptime') }} {{ uptimeText(m.id) }}<template v-if="checksOf(m.id)"> · {{ t('mon_checks_n', { n: checksOf(m.id) }) }}</template></span>
              <span class="meta-lat" v-if="m.lastLatencyMs > 0">{{ m.lastLatencyMs }}ms</span>
              <span class="meta-avg" v-if="avgLatOf(m.id)" :title="t('mon_avg_ttl')">⌀ {{ avgLatOf(m.id) }}ms</span>
              <span class="meta-checked">{{ t('mon_checked') }} {{ relTime(m.lastCheckedTs) }}</span>
              <span v-if="lastDownText(m.id)" class="meta-down">{{ t('mon_last_down') }} {{ lastDownText(m.id) }}</span>
              <span v-if="m.lastStatusCode" class="meta-code" :title="t('mon_status_code')">HTTP {{ m.lastStatusCode }}</span>
              <span v-if="m.lastError" class="meta-err" :title="m.lastError">{{ m.lastError }}</span>
              <span v-if="certText(m)" :class="['meta-cert', { warn: certWarn(m) }]">
                <component :is="certWarn(m) ? XCircle : CheckCircle2" :size="11" /> {{ certText(m) }}
              </span>
              <span v-if="inMaintenance(m)" class="meta-maint" :title="t('mon_maint_ttl')">
                <Wrench :size="11" /> {{ t('mon_maint_on', { t: maintRemaining(m) }) }}
              </span>
            </div>
          </div>
          <div class="item-actions" @click.stop>
            <button class="toggle-btn" :class="{ active: m.enabled }" @click="toggleEnabled(m)"
                    :title="m.enabled ? t('mon_enabled') : t('mon_disabled')">
              <span class="toggle-knob" />
            </button>
            <button class="act" :disabled="checkingId === m.id" @click="checkNow(m)" :title="t('mon_check_now')">
              <RefreshCw :size="14" :class="{ spin: checkingId === m.id }" />
            </button>
            <button class="act" @click="openEdit(m)" :title="t('mon_edit')"><Pencil :size="14" /></button>
            <button class="act" :class="{ active: inMaintenance(m) }" @click="toggleMaintenance(m)"
                    :title="inMaintenance(m) ? t('mon_maint_cancel') : t('mon_maint_1h')">
              <Wrench :size="14" />
            </button>
            <button class="act danger" @click="remove(m)" :title="t('mon_delete')"><Trash2 :size="14" /></button>
          </div>
          <button class="expand-btn" @click.stop="toggleExpand(m)" :title="t('mon_logs')">
            <ChevronDown v-if="!isExpanded(m.id)" :size="16" />
            <ChevronUp v-else :size="16" />
          </button>
        </div>

        <!-- 检测日志 -->
        <div v-if="isExpanded(m.id)" class="logs-panel">
          <div class="logs-head">
            <span>{{ t('mon_logs') }} ({{ logsMap[m.id]?.length || 0 }})</span>
            <div class="logs-head-right">
              <!-- 6.3 时间范围切换 -->
              <div class="range-tabs">
                <button
                  v-for="r in LOG_RANGES"
                  :key="r.value"
                  :class="['range-tab', { active: logRangeOf(m.id) === r.value }]"
                  @click="setLogRange(m.id, r.value); onRangeChange(m.id)"
                >{{ r.label }}</button>
              </div>
              <button class="clear-logs" :disabled="!logsMap[m.id]?.length" @click="exportCsv(m)" :title="t('mon_export_csv')">
                <Download :size="12" /> {{ t('mon_export_csv') }}
              </button>
              <button class="clear-logs" @click="clearLogs(m)">{{ t('mon_clear_logs') }}</button>
            </div>
          </div>
          <div v-if="!logsMap[m.id] || logsMap[m.id].length === 0" class="logs-empty">{{ t('mon_no_logs') }}</div>
          <template v-else>
            <canvas :id="chartId(m.id)" class="chart-canvas"
              @mousemove="onChartMove($event, m.id)" @mouseleave="onChartLeave"></canvas>
            <!-- 停机日历：按日看失败强度，一眼定位「哪天挂过」 -->
            <div v-if="calendarDaysFor(m.id).length" class="down-cal">
              <div class="cal-head">
                <span>{{ t('mon_down_calendar') }}</span>
                <span class="cal-legend">
                  {{ t('mon_cal_less') }}
                  <i class="cal-cell lv-x"></i><i class="cal-cell lv0"></i><i class="cal-cell lv1"></i>
                  <i class="cal-cell lv2"></i><i class="cal-cell lv3"></i>
                  {{ t('mon_cal_more') }}
                </span>
              </div>
              <div class="cal-grid">
                <i v-for="d in calendarDaysFor(m.id)" :key="d.key"
                  :class="['cal-cell', 'lv' + calLevel(d)]" :title="calTip(d)"></i>
              </div>
            </div>
          </template>
        </div>
        <!-- /logs-panel -->
        </div>
        <!-- /mon-item：必须收在 <template v-for> 之内，否则分组循环外会多出一个闭合标签 -->
      </template>
    </div>

    <!-- 回收站 -->
    <div v-else-if="viewMode === 'recycle'" class="mon-list recycle-list">
      <div v-if="loadingDeleted" class="mon-state"><Loader2 :size="16" class="spin" /><span>{{ t('loading') }}</span></div>
      <div v-else-if="!deletedMonitors.length" class="mon-empty">
        <Trash2 :size="30" class="empty-icon" />
        <p>{{ t('mon_recycle_empty') }}</p>
      </div>
      <div v-for="m in deletedMonitors" :key="m.id" class="mon-item recycled">
        <div class="item-main">
          <div class="item-line1"><span class="item-name">{{ m.name }}</span></div>
          <div class="item-url" :title="m.url"><Globe :size="11" /> {{ m.url }}</div>
          <div class="item-meta">
            <span class="meta-checked">{{ t('mon_deleted_at') }} {{ m.deletedAt?.slice(5, 16) || '' }}</span>
          </div>
        </div>
        <div class="item-actions" @click.stop>
          <button class="act" @click="restoreMonitor(m)" :title="t('mon_restore')"><RefreshCw :size="14" /></button>
          <button class="act danger" @click="askPurge(m)" :title="t('mon_purge')"><Trash2 :size="14" /></button>
        </div>
      </div>
    </div>

    <!-- 曲线图 tooltip -->
    <div v-if="tooltip.show && tooltip.data" class="chart-tooltip"
      :ref="el => (tipCtx.floatingRef.value = el as HTMLElement | null)" :style="tipCtx.floatingStyles.value">
      <div class="tip-line">
        <span :class="['tip-dot', tooltip.data.status]"></span>
        <span class="tip-status">{{ tooltip.data.status === 'up' ? t('mon_up') : t('mon_down') }}</span>
        <span class="tip-code">{{ tooltip.data.statusCode || 'ERR' }}</span>
      </div>
      <div class="tip-line tip-time">{{ tooltip.data.checkedAt }}</div>
      <div class="tip-line tip-lat">{{ tooltip.data.latencyMs }}ms</div>
      <div v-if="tooltip.data.error" class="tip-line tip-err">{{ tooltip.data.error }}</div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showModal" class="modal-mask" @click.self="showModal = false">
      <div class="modal">
        <h3>{{ modalTitle }}</h3>

        <label>{{ t('mon_name') }}</label>
        <input v-model="fName" class="modal-input" :placeholder="t('mon_name_ph')" />

        <label>{{ t('mon_url') }}</label>
        <input v-model="fUrl" class="modal-input" placeholder="https://example.com" />

        <div class="modal-grid">
          <div>
            <label>{{ t('mon_method') }}</label>
            <select v-model="fMethod" class="modal-input">
              <option v-for="mt in METHODS" :key="mt">{{ mt }}</option>
            </select>
          </div>
          <div>
            <label>{{ t('mon_expected') }}</label>
            <select v-model="fExpected" class="modal-input">
              <option v-for="e in EXPECTED" :key="e" :value="e">{{ expectedLabel(e) }}</option>
            </select>
          </div>
        </div>

        <!-- 请求头 / 请求体：POST/PUT/PATCH 类探针要能带鉴权头与 payload，
             否则这些方法只能发出一个空 body，和 GET 没区别（接口一律 401/400）。 -->
        <template v-if="showBodyFields">
          <label>{{ t('mon_http_headers') }}</label>
          <textarea v-model="fHeaders" class="modal-input mon-textarea" rows="3"
            :placeholder="t('mon_http_headers_ph')"></textarea>
          <label>{{ t('mon_http_body') }}</label>
          <textarea v-model="fBody" class="modal-input mon-textarea" rows="3"
            :placeholder="t('mon_http_body_ph')"></textarea>
        </template>

        <div class="modal-grid">
          <div>
            <label>{{ t('mon_interval') }}{{ t('sched_unit_sec') }}</label>
            <input v-model.number="fIntervalVal" type="number" min="5" class="modal-input" />
          </div>          <div>
            <label>{{ t('mon_timeout') }}</label>
            <div class="interval-row">
              <input v-model.number="fTimeout" type="number" min="1" class="modal-input interval-num" />
              <span class="unit-suffix">{{ t('sched_unit_sec') }}</span>
            </div>
          </div>
        </div>

        <label class="toggle-label">
          <input type="checkbox" v-model="fFollow" /> {{ t('mon_follow_redirects') }}
        </label>

        <label class="toggle-label">
          <input type="checkbox" v-model="fSkipTLS" /> {{ t('mon_ignore_tls') }}
        </label>

        <!-- 6.1 SSL 证书到期提前告警 -->
        <div class="modal-grid">
          <div>
            <label>{{ t('mon_cert_warn_days') }}</label>
            <div class="interval-row">
              <input v-model.number="fCertWarnDays" type="number" min="1" max="365" class="modal-input interval-num" />
              <span class="unit-suffix">{{ t('mon_cert_warn_unit') }}</span>
            </div>
          </div>
        </div>

        <!-- 6.2 内容关键字 / 正则匹配 -->
        <label>{{ t('mon_content_match') }}</label>
        <select v-model="fContentMatchType" class="modal-input">
          <option v-for="mt in MATCH_TYPES" :key="mt" :value="mt">{{ t('mon_match_' + mt) }}</option>
        </select>
        <input v-if="fContentMatchType !== 'none'" v-model="fContentMatchPattern" class="modal-input"
          :placeholder="t('mon_match_pattern_ph')" style="margin-top: var(--space-2);" />

        <div class="toggle-row">
          <label class="toggle-label"><input type="checkbox" v-model="fNotifyDown" /> {{ t('mon_notify_down') }}</label>
          <label class="toggle-label"><input type="checkbox" v-model="fNotifyUp" /> {{ t('mon_notify_up') }}</label>
          <label class="toggle-label"><input type="checkbox" v-model="fEnabled" /> {{ t('mon_enable_now') }}</label>
        </div>

        <!-- 误报抑制：连续 N 次失败才告警 -->
        <div class="interval-row" style="margin-top: var(--space-2);">
          <label>{{ t('mon_suppress_threshold') }}</label>
          <input v-model.number="fDownAlertThreshold" type="number" min="1" max="20" class="modal-input interval-num" style="flex:0 0 72px;" />
          <span class="unit-suffix">{{ t('mon_suppress_unit') }}</span>
        </div>

        <!-- 多条件告警：延迟阈值（0=关闭）。达到「连续 N 次失败」时告警，延迟超阈值
             只在消息里附加说明——它不参与是否告警的判定（否则第 N 次延迟正常就永远不告警） -->
        <div class="interval-row" style="margin-top: var(--space-2);">
          <label>{{ t('mon_latency_alert') }}</label>
          <input v-model.number="fLatencyAlert" type="number" min="0" step="100" class="modal-input interval-num" style="flex:0 0 72px;" placeholder="0" />
          <span class="unit-suffix">{{ t('mon_latency_alert_unit') }}</span>
        </div>
        <p class="field-hint">{{ t('mon_latency_alert_hint') }}</p>

        <!-- 分组：列表按组分区，监控多了之后按业务线/环境一眼定位 -->
        <label style="margin-top: var(--space-2);">{{ t('mon_group') }}</label>
        <input v-model="fGroup" class="modal-input" :placeholder="t('mon_group_ph')" list="mon-group-options" />
        <datalist id="mon-group-options">
          <option v-for="g in monitorGroups" :key="g" :value="g" />
        </datalist>

        <!-- 校验提示画在弹窗里：页面级 error 被遮罩盖住，等于没提示 -->
        <p v-if="formErr" class="form-err">{{ formErr }}</p>

        <div class="modal-actions">
          <button class="qd-btn" @click="showModal = false">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" :disabled="saving" @click="save">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :visible="showDelConfirm"
      :message="t('mon_delete_confirm')"
      @confirm="confirmDel"
      @cancel="showDelConfirm = false"
    />
    <ConfirmDialog
      :visible="showPurgeConfirm"
      :message="t('mon_purge_confirm')"
      @confirm="confirmPurge"
      @cancel="showPurgeConfirm = false"
    />
  </div>
</template>

<style scoped>
/* 请求头 / 请求体输入框：多行、可手动拉高。字体用等宽更利于核对
   "Name: Value" 的冒号对齐，但行高跟着主题走，不写死像素。 */
.mon-textarea {
  resize: vertical;
  font-family: var(--font-mono);
  line-height: 1.5;
}
/* 页头已统一为 .qd-page-header（通栏），页容器不再留 padding，
   内边距下放给各内容块 —— 否则页头会缩在 padding 里，与其它页错位。 */
.mon-page {
  display: flex; flex-direction: column; height: 100%;
  overflow: hidden; background: var(--color-bg-primary);
}
.refresh-control {
  display: inline-flex; align-items: center; gap: 5px; padding: 0 10px;
  height: 28px; background: transparent; color: var(--color-text-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  font-size: 12px; cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.refresh-control:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.refresh-select {
  border: none; background: transparent; color: inherit; font-size: 12px;
  font-family: inherit; outline: none; cursor: pointer; padding: 0;
  max-width: 64px;
}
.refresh-select option { background: var(--color-surface); color: var(--color-text-primary); }

.mon-error {
  margin: 12px 20px 0; padding: 8px 12px; font-size: 12px;
  color: var(--color-danger); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md); flex-shrink: 0;
}

.mon-summary {
  display: flex; gap: var(--space-3); margin: 14px 20px 12px; flex-shrink: 0;
}
.sum-item {
  flex: 1; display: flex; flex-direction: column; gap: 2px; padding: 10px 14px;
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.sum-num { font-size: 18px; font-weight: 600; color: var(--color-text-primary); }
.sum-num i { font-size: 11px; font-style: normal; color: var(--color-text-disabled); margin-left: 2px; }
.sum-lbl { font-size: 11px; color: var(--color-text-disabled); }
.sum-item.up .sum-num { color: var(--color-success); }
.sum-item.down .sum-num { color: var(--color-danger); }

.mon-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); padding: 0 20px 16px; }

/* ---- 分组筛选与分组头 ---- */
.group-filter { display: flex; gap: 4px; flex-wrap: wrap; margin: 0 20px 10px; flex-shrink: 0; }
.group-chip {
  padding: 3px 10px; border-radius: var(--radius-md);
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); font-size: 11px; font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.group-chip:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.group-chip.active {
  background: var(--color-accent-bg); border-color: var(--color-accent); color: var(--color-accent);
}
.group-head {
  display: flex; align-items: center; gap: 8px;
  margin: var(--space-2) 0 0; flex-shrink: 0;
  font-size: 11px; font-weight: 600; color: var(--color-text-muted);
}
.group-head-name { text-transform: uppercase; letter-spacing: 0.06em; }
.group-head-count {
  font-weight: 400; padding: 0 6px; border-radius: 8px;
  background: var(--color-bg-tertiary); color: var(--color-text-disabled);
}
/* 组头右侧的全选本组：只在组内多于一条时出现 */
.group-sel {
  margin-left: auto; padding: 1px 6px; font-size: 10px; font-family: inherit;
  color: var(--color-text-muted); background: none; border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); cursor: pointer;
}
.group-sel:hover { color: var(--color-accent); border-color: var(--color-accent); }

/* ---- 搜索 + 批量条 ---- */
.mon-tools {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 0 20px var(--space-2);
}
.mon-selall { display: inline-flex; align-items: center; flex-shrink: 0; cursor: pointer; }
.mon-search {
  display: flex; align-items: center; gap: 6px;
  width: 220px; padding: 4px 8px; flex-shrink: 0;
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); color: var(--color-text-muted);
}
.mon-search:focus-within { border-color: var(--color-border-focus); }
.mon-search-input {
  flex: 1; min-width: 0; border: none; outline: none; background: none;
  color: var(--color-text-primary); font-size: 12px; font-family: inherit;
}
.mon-search-clear {
  display: inline-flex; align-items: center; border: none; background: none;
  color: var(--color-text-muted); cursor: pointer; padding: 0;
}
.mon-search-count { font-size: 11px; color: var(--color-text-muted); flex-shrink: 0; }
.mon-tools-fill { flex: 1; }
.mon-sel-count { font-size: 11px; font-weight: 500; color: var(--color-accent); }
.mon-selbtn {
  padding: 3px 9px; font-size: 11px; font-family: inherit; cursor: pointer;
  color: var(--color-text-secondary); background: var(--color-bg-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  display: inline-flex; align-items: center; gap: 4px; white-space: nowrap;
}
.mon-selbtn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent); }
.mon-selbtn:disabled { opacity: 0.5; cursor: not-allowed; }
.mon-check { flex-shrink: 0; width: 12px; height: 12px; accent-color: var(--color-accent); cursor: pointer; }
.field-hint { margin: 4px 0 0; font-size: 11px; color: var(--color-text-disabled); }
.mon-empty { text-align: center; padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.empty-icon { opacity: 0.4; margin-bottom: var(--space-2); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); margin-top: var(--space-2); }

.mon-item {
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
.mon-item:hover { background: var(--color-bg-hover); }
.mon-item.disabled { opacity: 0.55; }
/* 勾选行的高亮不能只靠 checkbox：停用行本身半透明，只多一个小勾很难看出来 */
.mon-item.picked { border-color: var(--color-accent); }
/* 拖拽中：半透明 + 虚线框，告诉用户「这条正在被拖」而不是页面卡了 */
.mon-item.dragging { opacity: 0.45; border-style: dashed; }
.mon-item[draggable="true"] { cursor: grab; }
.mon-item[draggable="true"]:active { cursor: grabbing; }
/* 回收站里的条目：只读展示，没有展开/勾选那套交互 */
.mon-item.recycled { cursor: default; }

/* 维护窗口徽标：与状态徽标区分，用中性的强调色 */
.meta-maint { color: var(--color-warning, var(--color-text-muted)); }
.mon-item .act.active { color: var(--color-warning, var(--color-accent)); }

/* 视图切换 tab：活动列表 / 回收站，像标签页一样一眼分清当前在哪 */
.mon-viewtabs { display: flex; gap: var(--space-2); padding: 0 20px 10px; }
.vt-tab {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 5px 12px; border-radius: 999px;
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  color: var(--color-text-muted); font-size: 12.5px; cursor: pointer;
}
.vt-tab.active { background: var(--color-bg-tertiary); border-color: var(--color-accent); color: var(--color-text-primary); }
.vt-count {
  min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px;
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--color-bg-tertiary); color: var(--color-text-muted); font-size: 11px;
}
.vt-count.warn { background: var(--color-danger-bg, var(--color-bg-tertiary)); color: var(--color-danger, var(--color-text-muted)); }
.mon-state { display: flex; align-items: center; gap: 8px; justify-content: center; padding: 24px; color: var(--color-text-muted); }

.item-head { display: flex; align-items: center; gap: var(--space-3); padding: 12px 14px; cursor: pointer; }

.status-dot { flex-shrink: 0; width: 10px; height: 10px; border-radius: 50%; background: var(--color-text-disabled); }
.status-dot.up { background: var(--color-success); box-shadow: 0 0 6px var(--color-success-border); }
.status-dot.down { background: var(--color-danger); box-shadow: 0 0 6px var(--color-danger-border); }
.status-dot.unknown { background: var(--color-text-disabled); }

.item-main { flex: 1; min-width: 0; }
.item-line1 { display: flex; align-items: center; gap: 8px; }
.item-name { font-size: 13px; font-weight: 500; color: var(--color-text-primary); }
.status-badge { font-size: 10px; padding: 1px 7px; border-radius: var(--radius-md); }
.status-badge.up { background: var(--color-success-bg); color: var(--color-success); }
.status-badge.down { background: var(--color-danger-bg); color: var(--color-danger); }
.item-url { font-size: 12px; color: var(--color-text-secondary); margin-top: 2px; display: flex; align-items: center; gap: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }
.item-meta { font-size: 11px; color: var(--color-text-disabled); margin-top: 4px; display: flex; gap: 10px; flex-wrap: wrap; }
.meta-up { color: var(--color-accent); }
.meta-lat { color: var(--color-text-muted); }
.meta-avg { color: var(--color-text-muted); }
.meta-down { color: var(--color-warning); }
.meta-code { color: var(--color-text-muted); }
/* 最近一次错误：单行截断，完整内容在 title 里（错误串可能很长，撑开会把行挤爆） */
.meta-err {
  max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-danger);
}
.meta-cert { display: inline-flex; align-items: center; gap: 3px; color: var(--color-text-muted); }
.meta-cert.warn { color: var(--color-warning); font-weight: 500; }

.item-actions { display: flex; align-items: center; gap: 3px; flex-shrink: 0; }
.act {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled);
  border-radius: var(--radius-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.act:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-active); }
.act:disabled { opacity: 0.4; cursor: default; }
.act.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.spin { animation: mon-spin 0.8s linear infinite; }
@keyframes mon-spin { to { transform: rotate(360deg); } }
.expand-btn {
  width: 26px; height: 26px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled);
  border-radius: var(--radius-sm); cursor: pointer; flex-shrink: 0; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.expand-btn:hover { color: var(--color-text-primary); background: var(--color-bg-active); }

.logs-panel { border-top: 1px solid var(--color-border); padding: 8px 14px 12px; }
.logs-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px; gap: 8px; }
.logs-head > span { font-size: 12px; color: var(--color-text-secondary); font-weight: 500; }
.logs-head-right { display: flex; align-items: center; gap: 10px; }
.range-tabs { display: inline-flex; gap: 2px; padding: 2px; background: var(--color-bg-tertiary); border-radius: var(--radius-sm); }
.range-tab {
  font-size: 11px; padding: 2px 9px; border: none; background: transparent;
  color: var(--color-text-muted); border-radius: var(--radius-xs); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.range-tab:hover { color: var(--color-text-primary); }
.range-tab.active { background: var(--color-accent); color: var(--color-accent-text); }
.clear-logs { font-size: 11px; color: var(--color-text-disabled); background: transparent; border: none; cursor: pointer; font-family: inherit; }
.clear-logs:hover { color: var(--color-danger); }
.logs-empty { font-size: 11px; color: var(--color-text-disabled); padding: 6px 0; }

/* 曲线图 canvas */
.chart-canvas { display: block; width: 100%; height: 200px; border-radius: var(--radius-md); }

/* ---- 停机日历 ---- */
.down-cal { margin-top: 10px; }
.cal-head {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  font-size: 11px; color: var(--color-text-muted); margin-bottom: 6px;
}
.cal-legend { display: inline-flex; align-items: center; gap: 2px; font-size: 10px; color: var(--color-text-disabled); }
.cal-legend .cal-cell { margin-left: 2px; }
/* 一周一列（7 行），列自动流；格子 11px + 3px 间距，12 周约 190px 宽 */
.cal-grid {
  display: grid; grid-auto-flow: column;
  grid-template-rows: repeat(7, 11px);
  grid-auto-columns: 11px; gap: 3px;
  overflow-x: auto; padding-bottom: 2px;
}
.cal-cell {
  display: block; width: 11px; height: 11px; border-radius: 2px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
}
.cal-cell.lv0 { background: var(--color-success-bg); border-color: var(--color-success-border); }
.cal-cell.lv1 { background: var(--color-warning-bg); border-color: var(--color-warning-border); }
.cal-cell.lv2 { background: var(--color-warning); border-color: var(--color-warning); }
.cal-cell.lv3 { background: var(--color-danger); border-color: var(--color-danger); }

/* 曲线图 tooltip */
.chart-tooltip {
  position: fixed; z-index: 999; pointer-events: none;
  background: rgba(20, 20, 35, 0.92); border: 1px solid var(--color-accent-border-strong);
  border-radius: var(--radius-md); padding: 6px 10px; font-size: 11px;
  box-shadow: 0 4px 16px var(--color-bg-overlay); backdrop-filter: blur(4px);
  min-width: 100px;
}
.tip-line { display: flex; align-items: center; gap: 6px; margin: 1px 0; }
.tip-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.tip-dot.up { background: var(--color-success); }
.tip-dot.down { background: #e84c4c; }
.tip-status { color: var(--color-text-primary); font-weight: 500; }
.tip-code { color: var(--color-text-muted); font-weight: 600; }
.tip-time { color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }
.tip-lat { color: var(--color-accent); }
.tip-err { color: var(--color-danger); white-space: nowrap; max-width: 220px; overflow: hidden; text-overflow: ellipsis; }


/* 弹窗 */
.modal-mask {
  position: fixed; inset: 0; background: var(--color-bg-overlay); display: flex;
  align-items: center; justify-content: center; z-index: 100;
}
.modal {
  width: 440px; max-height: 90vh; overflow-y: auto; background: var(--color-surface);
  border-radius: var(--radius-lg); padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }

.interval-row { display: flex; gap: var(--space-2); align-items: center; }
.interval-num { flex: 1; min-width: 0; }
.unit-suffix { font-size: 12px; color: var(--color-text-disabled); width: 90px; flex-shrink: 0; text-align: center; }

.toggle-label { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--color-text-secondary); cursor: pointer; margin: var(--space-2) 0 0; }
.toggle-label input { cursor: pointer; }
.toggle-row { display: flex; gap: var(--space-5); margin-top: var(--space-3); flex-wrap: wrap; }

.modal-actions { display: flex; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-5); }
/* 弹窗内校验提示：弹窗自己有内边距，这里不再加外边距 */
.form-err {
  margin: var(--space-3) 0 0; padding: 7px 10px; font-size: 12px;
  color: var(--color-danger); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md);
}
</style>
