<script setup lang="ts">
import { downloadBlob } from '../utils/download'
import PageHeader from './PageHeader.vue'
import QdTabs from './QdTabs.vue'
import QdSwitch from './QdSwitch.vue'
import QdSearchBox from './QdSearchBox.vue'
import QdModal from './QdModal.vue'
defineOptions({ name: KEEP_ALIVE.monitor })
// name 的唯一来源：pages/keepAlive.ts（与 KeepAlive 白名单共用同一常量）
import { KEEP_ALIVE } from '../pages/keepAlive'
import { ref, computed, onMounted, onUnmounted, onActivated, onDeactivated, nextTick, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { escapeHtml } from '../utils/echarts'
import { matchesTextOrPinyin } from '../utils/pinyin'
import { useToast } from '../composables/useToast'
import { useSaveAs } from '../composables/useSaveAs'
import {
  CreateMonitor, ListMonitors, UpdateMonitor,
  SoftDeleteMonitor,
  SetMonitorEnabled, SetMonitorSort, SetMonitorMaintenance, CheckMonitorNow,
  GetMonitorLogs, GetMonitorLogsSince, ClearMonitorLogs, ListMonitorStats,
  SetMonitorMute,
} from '../../bindings/quickdock/services/monitor/monitorservice'
import type { Monitor } from '../../bindings/quickdock/internal/db/models'
import { Events } from '@wailsio/runtime'
import ConfirmDialog from './ConfirmDialog.vue'
// 状态页与监控列表同源，做成第三个视图而不是独立导航：用户配置完监控就地看整体状态。
import StatusPage from './StatusPage.vue'
import QdLineChart from './charts/QdLineChart.vue'
// 通知中心「点通知 → 落到具体监控项」：深链目标由 bridge 的 pendingTarget 带过来
import { pendingTarget } from '../composables/bridge'

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
  p95LatencyMs: number
  p99LatencyMs: number
  lastDownAt: string
}
import {
  Plus, Play, Pencil, Trash2, Globe, Activity, RefreshCw,
  ChevronDown, ChevronUp, CheckCircle2, XCircle, Radio, Download, Power, Wrench,
  Bell as BellIcon, BellOff, Network, Cpu, TerminalSquare, Save,
  // 汇总卡的延迟趋势箭头：向上=变慢（告警色）、向下=变快、持平用一条短横线
  TrendingUp, TrendingDown, Minus, Ellipsis, AlertTriangle, History,
} from '@lucide/vue'

import { writeStored } from '../utils/storage'
const { t } = useI18n()
const toast = useToast()
const sa = useSaveAs()

const monitors = ref<Monitor[]>([])
const stats = ref<Record<string, MonitorStat>>({})
const logsMap = ref<Record<string, MonitorLog[]>>({})
const logsFailed = ref<Set<string>>(new Set()) // 每条监控的日志拉取是否失败：空数组要区分「真没记录」与「失败」
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

// 批量删除：同样是逐条软删（进回收站，可恢复）。勾了七八条再一条条点垃圾桶，
// 是这页最长的一条操作路径，收进批量条里才合理。
const showBatchDel = ref(false)
function askBatchDelete() {
  if (selectedIds.value.size) showBatchDel.value = true
}
async function batchDelete() {
  showBatchDel.value = false
  const ids = [...selectedIds.value]
  if (!ids.length || batchBusy.value) return
  batchBusy.value = true
  error.value = ''
  const failed: string[] = []
  for (const id of ids) {
    const m = monitors.value.find(x => x.id === id)
    try {
      unwrap(await SoftDeleteMonitor(id))
      expanded.value.delete(id)
    } catch (e) {
      failed.push(m?.name || id)
      error.value = getErrorMessage(e)
    }
  }
  batchBusy.value = false
  clearSelection()
  await refresh()
  if (!failed.length) toast.success(t('mon_batch_deleted', { n: ids.length }))
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
    .map(g => ({ name: g.name, items: g.items.filter(m => matchesTextOrPinyin([m.name, m.url, m.target, m.groupName], q, 'monitor')) }))
    .filter(g => g.items.length > 0)
  return hit
})

/** 当前列表里可见的监控点（搜索/分组筛选之后），批量操作的「全选」以它为准。 */
const visibleMonitors = computed<Monitor[]>(() => monitorGroupList.value.flatMap(g => g.items))

// ---- 汇总指标 ----
// 「平均延迟 268ms」单独摆着看不出好坏，所以顺手算一个趋势基准：
// 用 ListMonitorStats 的 24h 均值做参照，当前值与之相比才有方向。
// 只统计当前在线的监控：故障项的 lastLatency 是超时/错误链路的残留值，混进来会把均值抬高。
const summary = computed(() => {
  const list = monitors.value
  const up = list.filter(m => m.lastStatus === 'up').length
  const down = list.filter(m => m.lastStatus === 'down').length
  let lat = 0, n = 0
  let lat24 = 0, n24 = 0
  for (const m of list) {
    const now = m.lastStatus === 'up' ? m.lastLatencyMs : 0
    if (now > 0) { lat += now; n++ }
    const avg = stats.value[m.id]?.avgLatencyMs || 0
    if (m.lastStatus === 'up' && avg > 0) { lat24 += avg; n24++ }
  }
  const avgLatency = n ? Math.round(lat / n) : 0
  const avg24 = n24 ? Math.round(lat24 / n24) : 0
  const trendPct = avg24 > 0 && avgLatency > 0 ? Math.round(((avgLatency - avg24) / avg24) * 100) : 0
  return {
    total: list.length,
    up, down,
    avgLatency, avg24, trendPct,
    // 升高=更慢（告警色），降低=更快（成功色），持平用中性灰
    trendDir: trendPct === 0 ? 'flat' : trendPct > 0 ? 'up' : 'down',
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
// 分位数是近 24h 成功探测的响应时间统计；0 表示没有样本，下面模板用 >0 判断，
// 免得把「没数据」显示成「0ms 超快」。
function p95Of(id: string): number { return statOf(id)?.p95LatencyMs ?? 0 }
function p99Of(id: string): number { return statOf(id)?.p99LatencyMs ?? 0 }
function lastDownText(id: string): string {
  const v = statOf(id)?.lastDownAt || ''
  return v ? v.slice(5, 16) : '' // YYYY-MM-DD HH:MM:SS → MM-DD HH:MM
}

// 当次延迟占超时阈值的比例：纯数字肉眼分不出「131ms 算快还是慢」，
// 给一条相对阈值的细进度条当刻度（越满越接近超时）。
// 下限 4% 只是为了让极快响应也留下一小截可见的长度。
function latPct(m: Monitor): number {
  const limit = (m.timeoutSec || 10) * 1000
  if (!limit || m.lastLatencyMs <= 0) return 0
  return Math.max(4, Math.min(100, Math.round((m.lastLatencyMs / limit) * 100)))
}
function latLevel(m: Monitor): string {
  const limit = (m.timeoutSec || 10) * 1000
  if (!limit || m.lastLatencyMs <= 0) return ''
  const r = m.lastLatencyMs / limit
  return r >= 0.8 ? 'bad' : r >= 0.5 ? 'warn' : ''
}

// ---- 表单（新增 / 编辑共用） ----
const showModal = ref(false)
const editingId = ref('')
const modalTitle = computed(() => editingId.value ? t('mon_edit') : t('mon_add'))

const fName = ref('')
// 探测类型：http / tcp / process / cmd。http 用 fUrl，其余用 fTarget——
// 与后端 db.Monitor.Kind/Target 一一对应，切换类型时不清空另一侧的值，
// 用户误点类型再切回来不会丢掉刚填的地址。
const fKind = ref('http')
const fTarget = ref('')
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
// muteUntil 同理：新增列要 build 后才进 bindings，这里用交叉类型补齐，
// 免得整页类型检查被一个生成物字段卡住。
type MonitorWithMute = Monitor & { muteUntil?: number }
const fHeaders = ref('')
const fBody = ref('')
const NEEDS_BODY = ['POST', 'PUT', 'PATCH'] as const
const showBodyFields = computed(() => (NEEDS_BODY as readonly string[]).includes(fMethod.value))

const MATCH_TYPES = ['none', 'contains', 'not_contains', 'regex']

/** 探测类型清单，值与后端 db.MonitorKind* 一一对应。 */
const KINDS = ['http', 'tcp', 'process', 'cmd', 'dns', 'ping'] as const

/** 探测类型按钮文案。dns/ping 是后加的类型，用 camelCase 新键而不是继续往
 *  mon_kind_<k> 这套历史下划线键里补 —— 新增键按规范一律 camelCase。 */
function kindLabel(k: string): string {
  if (k === 'dns') return t('monitorKindDns')
  if (k === 'ping') return t('monitorKindPing')
  return t('mon_kind_' + k)
}
/** 探测类型分段控件数据源（QdTabs variant=segmented），图标走 #tab 插槽。 */
const kindTabs = computed(() => KINDS.map(k => ({ id: k, label: kindLabel(k) })))

/** 归一化探测类型：空值/未知值按 http（与后端 normalizeMonitorKind 同口径）。 */
function kindOf(m: Monitor): string {
  return m.kind && (KINDS as readonly string[]).includes(m.kind) ? m.kind : 'http'
}

/** 列表里显示的目标：http 读 url，其余读 target。 */
function targetText(m: Monitor): string {
  return kindOf(m) === 'http' ? m.url : (m.target || '')
}

/** 每类探针一个图标：一眼区分「网站」「端口」「进程」「脚本」，比统一用地球图标有用。 */
const KIND_ICONS: Record<string, Component> = {
  http: Globe, tcp: Network, process: Cpu, cmd: TerminalSquare,
  // 复用本文件已引入的图标，不新增依赖：域名解析用地球，ICMP 探活用信号。
  dns: Globe, ping: Radio,
}
function kindIcon(m: Monitor): Component {
  return KIND_ICONS[kindOf(m)] || Globe
}

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
  fKind.value = 'http'
  fTarget.value = ''
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
  fKind.value = kindOf(m)
  fTarget.value = m.target || ''
  fUrl.value = m.url
  fMethod.value = m.method || 'GET'
  fIntervalVal.value = Math.max(5, m.intervalSec || 60)
  fTimeout.value = m.timeoutSec || 10
  // any 以字面量 'any' 落库（monitor_checker.matchesExpected 只认字面量）。
  // 空串只是「未填」的缺眢值，DB 归一化后也是 2xx，不再当作 any 的别名。
  fExpected.value = m.expectedStatus || '2xx'
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
  const kind = fKind.value
  return {
    id: editingId.value,
    name: fName.value.trim(),
    kind,
    // 非 http 类型不写 url、http 不写 target：两列都留着对方的旧值会让「改了类型
    // 但忘了改目标」的监控看起来仍指向旧地址，排查时白费一轮。
    target: kind === 'http' ? '' : fTarget.value.trim(),
    url: fUrl.value.trim(),
    method: fMethod.value,
    intervalSec: intervalVal(),
    timeoutSec: Math.max(1, Math.floor(fTimeout.value || 10)),
    // 不能再把 any 转成空串：db/monitor.go 会把空串归一成 '2xx'，
    // 于是后端 matchesExpected 的 any 分支永远走不到（选 any 实际仍按 2xx 判）。
    expectedStatus: fExpected.value,
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
  // 目标校验也分类型：非 http 判 url 会让「填了 TCP 目标却提示要填网址」，
  // 用户只能去猜哪个字段才是当前类型真正要的。
  if (p.kind === 'http') {
    if (!p.url) { formErr.value = t('mon_err_url'); return }
  } else if (!p.target) {
    formErr.value = t('mon_err_target')
    return
  }
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
    // refresh() 开头会清 error，故错误必须在它之后写回，否则被吞掉。
    await refresh()
    if (r && r.code !== 0) error.value = r.msg
  } catch (e) { error.value = getErrorMessage(e) } finally { checkingId.value = '' }
}

async function remove(m: Monitor) {
  delTarget.value = m
  showDelConfirm.value = true
}

// ---- 行内「更多」菜单 ----
// 静默 / 维护窗口 / 删除都是低频动作，和「立即检测 / 编辑」挤在一排会把常用项
// 稀释成一片灰点；收进 ⋯ 之后，hover 时露出的三个图标才是真正每天要点的。
// 同一时刻只允许开一个：几十行里留一堆展开的浮层，比藏着更难看清。
const menuFor = ref('')
function toggleRowMenu(id: string) { menuFor.value = menuFor.value === id ? '' : id }
function closeRowMenu(e: MouseEvent) {
  if (!(e.target as HTMLElement | null)?.closest?.('.mon-more-wrap')) menuFor.value = ''
}

// ---- 最近故障 ----
// 列表只回答「现在好不好」，回答不了「刚刚有没有抖过」。页面下方本来就空着，
// 用最近 24h 的 lastDownAt 拼一条时间线，点一下还能跳回那条监控。
interface RecentDown { id: string; name: string; ts: number; rel: string }
const recentDowns = computed<RecentDown[]>(() => {
  const nowSec = Date.now() / 1000
  const out: RecentDown[] = []
  for (const m of monitors.value) {
    const at = stats.value[m.id]?.lastDownAt || ''
    if (!at) continue
    // 后端给的是 "YYYY-MM-DD HH:MM:SS"（本地时间）：换成 T 分隔，Safari/旧 WebView 才认
    const ms = new Date(at.replace(' ', 'T')).getTime()
    if (!Number.isFinite(ms)) continue
    const ts = Math.floor(ms / 1000)
    if (nowSec - ts > 86400) continue
    out.push({ id: m.id, name: m.name, ts, rel: relTime(ts) })
  }
  return out.sort((a, b) => b.ts - a.ts).slice(0, 6)
})

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

// ---- 视图切换 ----
// 回收站不再占一个 tab：全站只有一份，在「设置 → 历史 → 回收站」。
// 监控软删仍保留日志与运行数据，只是找回/彻底删除都交给全局回收站做。
const viewMode = ref<'active' | 'status'>('active')

/** 回收站的唯一入口，落到设置的历史页并直接切到回收站分段。 */
function openRecycleBin() {
  try { Events.Emit('app:open-settings', 'history:recycle') } catch { /* 事件通道未就绪时静默 */ }
}

// 两项视图的标签；计数不放进 tabs 数据，而是在 #tab 插槽里渲染——
// .vt-count 带告警色变体，QdTabs 的中性 badge 会把告警弱化成普通小字。
const viewTabs = computed(() => [
  { id: 'active', label: t('mon_view_active') },
  { id: 'status', label: t('monitorViewStatus') },
])

async function switchView(v: 'active' | 'status') {
  viewMode.value = v
  clearSelection()
  if (v === 'active') await refresh()
  // status 视图自带 60 秒轮询与首屏加载，父页不再重复发一轮 IPC。
}

// 状态页每轮 load 拿到的就是父页汇总条所需的同一份数据。直接回灌，
// 上方四个数字才能跟着状态页一起刷新，又不必在 status 视图再开一条父页定时器。
function onStatusData(mons: Monitor[], st: Record<string, MonitorStat>) {
  monitors.value = mons
  stats.value = st
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

// ---- 告警静默 ----
// 与维护窗口不是一回事：维护窗口连探测都停了（图上是一段空白），
// 静默只是「别再弹我」。凌晨备份把站点抖宕几分钟的场景要的是后者，
// 用维护窗口会让这几分钟直接没数据，事后分不清「没探测」和「没问题」。
function inMute(m: Monitor): boolean {
  return ((m as MonitorWithMute).muteUntil || 0) > Math.floor(Date.now() / 1000)
}
function muteRemaining(m: Monitor): string {
  const left = ((m as MonitorWithMute).muteUntil || 0) - Math.floor(Date.now() / 1000)
  if (left <= 0) return ''
  if (left < 3600) return Math.ceil(left / 60) + t('mon_min_short')
  return (left / 3600).toFixed(1) + t('mon_hour_short')
}
async function toggleMute(m: Monitor) {
  try {
    const until = inMute(m) ? 0 : Math.floor(Date.now() / 1000) + 3600
    await unwrap(await SetMonitorMute(m.id, until))
    await refresh()
    toast.success(t(until ? 'mon_mute_on_toast' : 'mon_mute_off_toast', { name: m.name }))
  } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 拖拽排序 ----
// 复用定时任务/笔记同一套「按可见顺序重写 sort=i*10」：拖一下只动 sort 一列，
// 不碰任何编辑字段，也绝不重算运行数据。分组只是展示层——拖动会反映到全局 sort 顺序，
// 跨组拖动保留原分组归属（组是用户自己划的，拖排序不该偷偷改它）。
/** 有搜索/分组筛选时禁拖：visibleMonitors 只是子集，按可见下标写 sort=i*10 会打乱隐藏项。 */
const canDrag = computed(() => !query.value.trim() && !groupFilter.value)
const dragId = ref('')
function onDragStart(e: DragEvent, m: Monitor) {
  if (!canDrag.value) return
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
  // 乐观更新：先改本地顺序，写库失败再拉回服务端顺序（对照定时任务拖拽）。
  const serverOrder = monitors.value.slice()
  monitors.value = next.slice()
  try {
    for (let i = 0; i < next.length; i++) {
      if (next[i].sort === i * 10) continue
      // unwrap 会把 code!==0 的失败抛成 ApiError，逐条检查，不再静默忽略。
      unwrap(await SetMonitorSort(next[i].id, i * 10))
      next[i].sort = i * 10
    }
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
    monitors.value = serverOrder // 恢复拖拽前的本地顺序，别把假象留着
    await refresh() // 再拉一次，确保与库里实际顺序一致
  }
}

// ---- 展开日志 ----
function isExpanded(id: string) { return expanded.value.has(id) }

// 6.3 趋势图时间范围切换
// 1h 是后补的：24h 在「刚重启/刚改配置」时满屏都是旧数据，看不出这半小时发生了什么。
const LOG_RANGES = [
  { value: '1h', sec: 3600, label: '1h' },
  { value: '24h', sec: 86400, label: '24h' },
  { value: '7d', sec: 86400 * 7, label: '7d' },
  { value: 'all', sec: 0, label: '' },
]
function rangeLabel(r: { value: string; label: string }): string {
  return r.value === 'all' ? t('mon_range_all') : r.label
}
/** 日志时间范围分段控件数据源：每个展开的监控各用一个 QdTabs 实例。 */
const rangeTabs = computed(() => LOG_RANGES.map(r => ({ id: r.value, label: rangeLabel(r) })))
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
    // 绘图点上限 2000：再多画布也画不出区别，但序列化与解析会明显变慢。
    // 后端保留上限已提到 20000 条，所以「7d / 全部」拿到的确实是可用区间内的数据。
    logsMap.value[id] = unwrap<MonitorLog[]>(await GetMonitorLogsSince(id, since, 2000)) ?? []
    logsFailed.value.delete(id) // 拉取成功，清掉失败态
  } catch (e) {
    // 失败时标记该条：让面板显示「加载失败 + 重试」，不伪装成「暂无检测记录」，也不拖垮整页。
    logErr('monitor.loadLogs', e)
    logsFailed.value.add(id)
  }
}

// 切换时间范围后重新加载：QdLineChart watch 数据变化自动重绘
async function onRangeChange(id: string) {
  await loadLogs(id)
}

// ---- 正常率环形图 ----
// 折线图回答「什么时候慢/挂了」，回答不了「这段时间到底稳不稳」——
// 而后者才是运维第一眼看的东西。同一个 logsMap 再算一遍 up/total，
// 口径与图、日历完全一致（切范围一起变），不额外请求。
interface UptimeCell { total: number; up: number; down: number; pct: number }
const uptimeMap = computed<Record<string, UptimeCell>>(() => {
  const out: Record<string, UptimeCell> = {}
  for (const [id, logs] of Object.entries(logsMap.value)) {
    let up = 0
    for (const l of logs) if (l.status === 'up') up++
    const total = logs.length
    out[id] = { total, up, down: total - up, pct: total ? (up / total) * 100 : 0 }
  }
  return out
})
function uptimeOf(id: string): UptimeCell {
  return uptimeMap.value[id] || { total: 0, up: 0, down: 0, pct: 0 }
}
// SVG 用 stroke-dasharray 画环：周长 2πr，r=26（viewBox 64×64）。
const RING_C = 2 * Math.PI * 26
function ringDash(pct: number): string {
  const on = Math.max(0, Math.min(100, pct)) / 100 * RING_C
  return on.toFixed(2) + ' ' + (RING_C - on).toFixed(2)
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

// 后端 monitor_logs.checked_ts 是 Unix 秒（services/monitor_checker.go 用 time.Now().Unix()），
// 而 Date / ECharts time 轴一律要毫秒。凡是拿它去 new Date() 的地方都走这里，
// 否则折线图与可用日历会整片落到 1970。relTime() 本来就是按秒算的，不走这里。
function logTsMs(l: MonitorLog): number {
  if (l.checkedTs) return l.checkedTs * 1000
  const parsed = Date.parse(l.checkedAt)
  return Number.isFinite(parsed) ? parsed : 0
}

// ---- 延迟折线图数据 ----
// 图由 QdLineChart 渲染：x 轴是 time（毫秒时间戳），上千点靠 lttb 降采样，
// 超时阈值走 markLine，探测失败的点单独标红。数据只在这里组装，组件不拉数据。
interface ChartView {
  series: { name: string; points: [number, number][]; color: 'accent' }[]
  anomalies: { x: number; y: number }[]
  threshold: number
  tooltipExtra: (x: number, y: number) => string
}
const chartViewMap = computed<Record<string, ChartView>>(() => {
  const out: Record<string, ChartView> = {}
  for (const [id, logs] of Object.entries(logsMap.value)) {
    const data = [...logs].reverse() // 后端倒序，转正序
    const points: [number, number][] = []
    const anomalies: { x: number; y: number }[] = []
    for (const l of data) {
      const ts = logTsMs(l)
      if (!ts || ts <= 0) continue
      points.push([ts, l.latencyMs])
      if (l.status !== 'up') anomalies.push({ x: ts, y: l.latencyMs })
    }
    const timeout = monitors.value.find((m) => m.id === id)?.timeoutSec || 10
    out[id] = {
      series: [{ name: t('chartLegendLatency'), points, color: 'accent' as const }],
      anomalies,
      threshold: timeout * 1000,
      tooltipExtra: (x: number): string => {
        const l = data.find((v) => logTsMs(v) === x)
        if (!l) return ''
        const status = l.status === 'up' ? t('mon_up') : t('mon_down')
        let html = `<div>${t('chartTooltipStatus')}: ${escapeHtml(status)} ${escapeHtml(String(l.statusCode || 'ERR'))}</div>`
        if (l.error) html += `<div>${escapeHtml(l.error)}</div>`
        return html
      },
    }
  }
  return out
})
function chartView(id: string): ChartView {
  return chartViewMap.value[id] ?? {
    series: [], anomalies: [], threshold: 0, tooltipExtra: () => '',
  }
}

// 展开日志时才挂载 QdLineChart（v-if），组件 onMounted 自行 init；收起即销毁。
async function toggleExpand(m: Monitor) {
  if (expanded.value.has(m.id)) { expanded.value.delete(m.id); return }
  expanded.value.add(m.id)
  await loadLogs(m.id)
}

async function clearLogs(m: Monitor) {
  try { await unwrap(await ClearMonitorLogs(m.id)); logsMap.value[m.id] = []; logsFailed.value.delete(m.id) } catch (e) { error.value = getErrorMessage(e) }
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
    // 文件名去掉 Windows 非法字符，否则下载静默失败
    downloadBlob('monitor-' + m.name.replace(/[\\/:*?"<>|]/g, '_') + '-' + new Date().toISOString().slice(0, 10) + '.csv', blob)
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// 转存日志：把当前展开面板已加载的日志拼成纯文本，交给共享沉淀层另存为笔记/待办/知识库。
// 数据源与展示同源（logsMap），不另发请求；时间/状态/错误是排障时最需要的三段。
function saveLogsAs(e: MouseEvent, m: Monitor) {
  const logs = logsMap.value[m.id] || []
  if (!logs.length) return
  const text = logs.map(l => [l.checkedAt, l.status, l.error].filter(Boolean).join(' ')).join('\n')
  sa.open(
    { text, source: 'monitor', sourceMeta: { monitorId: m.id, label: m.name } },
    { x: e.clientX, y: e.clientY },
  )
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
    const ts = logTsMs(l)
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
  // 隐私模式 / 企业策略下 getItem 本身会抛 SecurityError，typeof 挡不住；
  // 读偏好失败就沿用默认的 5 秒，别让整页脚本崩在这里。
  try {
    const saved = localStorage.getItem(LS_REFRESH_KEY)
    if (saved !== null) {
      const n = parseInt(saved, 10)
      if (REFRESH_OPTIONS.includes(n)) refreshSec.value = n
    }
  } catch { /* 存储不可用：沿用默认刷新间隔 */ }
}

let pollTimer: ReturnType<typeof setTimeout> | undefined
let polling = false
// 本页在 KeepAlive 白名单里：切走只触发 onDeactivated 不卸载，
// document.hidden 只挡得住最小化，挡不住切页。没有这个标志，
// 后台会一直每 refreshSec 秒跑一遍 refresh + loadLogs。
let pageActive = false
let needsRefresh = false

// setTimeout 链式轮询：上一次 refresh 完成后才排下一次，
// 避免 setInterval 在慢请求时回调叠加（且 polling 标志兜底防重入）
async function pollTick() {
  // 本轮已经被触发，标记随之失效；配合 scheduleNext 的去重，
  // 防止「切走又切回时旧 tick 收尾」与新一轮各排一个定时器。
  pollTimer = undefined
  if (!pageActive) return
  // 窗口在后台时不打接口也不重绘：这里的轮询是「看着它才刷新」，
  // 最小化 / 切到别的应用还每 5 秒发一次请求纯属白烧后端与电量。
  // 回到前台后由 visibilitychange 立刻补一次（见下方监听）。
  if (typeof document !== 'undefined' && document.hidden) { scheduleNext(); return }
  if (polling) { scheduleNext(); return }
  polling = true
  try {
    await refresh()
    // 重新加载展开的监控日志：图表组件 watch 数据变化自动重绘
    for (const id of expanded.value) {
      await loadLogs(id)
    }
  } catch (e) {
    // refresh/loadLogs 内部已处理错误，此处仅兜底记录
    logErr('monitor.pollTick', e)
  }
  polling = false
  scheduleNext()
}

function scheduleNext() {
  // 状态页有自己的 60 秒刷新；这里继续排 timer 只会每 5 秒空转一次。
  if (!pageActive || refreshSec.value <= 0 || pollTimer || viewMode.value === 'status') return
  pollTimer = setTimeout(pollTick, refreshSec.value * 1000)
}

function applyAutoRefresh() {
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = undefined }
  if (pageActive && refreshSec.value > 0) scheduleNext()
}

watch(refreshSec, (v) => {
  writeStored(LS_REFRESH_KEY, String(v))
  applyAutoRefresh()
})

// 切回活动/回收站时恢复父页轮询；切到状态页时 applyAutoRefresh 会清掉 timer。
watch(viewMode, () => applyAutoRefresh())

// 回到前台立刻补一次：后台期间轮询是跳过的，不补一次会看到一份过期数据。
function onVisibility() {
  if (!pageActive || document.hidden) return
  if (refreshSec.value <= 0) return
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = undefined }
  void pollTick()
}

// 启停做成幂等：首次挂载时 onMounted 先起，随后触发的 onActivated 直接返回，
// 不会重复 refresh / 重复注册监听；页面将来若不在 KeepAlive 里渲染也仍能轮询。
function startPolling() {
  if (pageActive) return
  pageActive = true
  if (needsRefresh) {
    needsRefresh = false
    void refresh() // 后台期间的数据是过期的，切回来立刻补一次
  }
  document.addEventListener('visibilitychange', onVisibility)
  // 行内 ⋯ 菜单的点击外部关闭：跟着页面激活态挂/卸，
  // 保活页常驻时不会在别的页面点一下就把本页浮层关掉（串台）。
  document.addEventListener('click', closeRowMenu)
  applyAutoRefresh()
}

function stopPolling() {
  pageActive = false
  needsRefresh = true
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = undefined }
  document.removeEventListener('visibilitychange', onVisibility)
  document.removeEventListener('click', closeRowMenu)
  menuFor.value = ''
}

// 设置里的全局回收站恢复/彻底删除监控后发这个事件。监控页是 KeepAlive
// 常驻页，自动刷新还可能被关掉，光靠轮询会一直看到旧的软删状态。
let offMonitorChanged: (() => void) | null = null

onMounted(() => {
  refresh()
  startPolling()
  offMonitorChanged = Events.On('monitor:changed', () => { void refresh() })
})
onActivated(startPolling)
onDeactivated(stopPolling)
onUnmounted(stopPolling)

// ---- 通知中心深链定位 ----
// 点一条监控通知后 App 把 refId 挂到 pendingTarget 再切到本页；只切页不够，
// 得停在那一条上。与 TodoPage 的 focusPendingTodo 同一套做法。
const flashMonitorId = ref('')
let flashTimer: number | null = null

async function focusMonitor(id: string) {
  // 目标可能先于数据到位：切页时 refresh() 还在路上，列表是空的
  let m = monitors.value.find(x => x.id === id)
  if (!m) { await refresh(); m = monitors.value.find(x => x.id === id) }
  // 条目已被删除 / 是回收站里的软删项：静默忽略，不弹 toast、不落到空白详情
  if (!m) return
  // 筛选会把目标藏在列表之外，DOM 里根本没这一行，先把它放回可见集合
  if (viewMode.value !== 'active') await switchView('active')
  if (groupFilter.value) groupFilter.value = ''
  if (query.value) query.value = ''
  await nextTick()
  const el = document.querySelector<HTMLElement>(`[data-monitor-id="${id.replace(/"/g, '\\"')}"]`)
  if (!el) return
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  flashMonitorId.value = id
  if (flashTimer) clearTimeout(flashTimer)
  flashTimer = window.setTimeout(() => { flashMonitorId.value = '' }, 1800)
}

function consumePendingTarget() {
  const p = pendingTarget.value
  if (!p || p.page !== 'monitor' || !p.target) return
  const id = p.target
  // 取走即清空：留着的话下次从侧栏正常进来（或切走再回来）会重复定位到旧条目
  pendingTarget.value = null
  // 命令面板「新建监控」带 target='new'：直接打开新建弹窗，不再走定位。
  if (id === 'new') {
    openCreate()
    return
  }
  void focusMonitor(id)
}
// immediate 吃「navigateTo 先写好、本页还没挂载」的目标；置 null 会让 watch 再触发一次（守卫挡掉）
watch(pendingTarget, consumePendingTarget, { immediate: true })
// 双重兜底：异步组件竞态下若 immediate 那次被跳过，挂载后再补一次（已消费则是 no-op）
onMounted(consumePendingTarget)
onUnmounted(() => {
  if (flashTimer) clearTimeout(flashTimer)
  offMonitorChanged?.()
  offMonitorChanged = null
})
</script>

<template>
  <div class="mon-page">
    <!-- 顶部栏 -->
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title">{{ t('mon_title') }}</h2>
        <span class="qd-page-sub">{{ t('mon_subtitle') }}</span>
      </template>
      <div class="qd-page-actions">
        <div v-if="viewMode !== 'status'" class="refresh-control">
          <RefreshCw :size="14" />
          <select v-model.number="refreshSec" class="refresh-select" :title="t('mon_auto_refresh')">
            <option v-for="o in REFRESH_OPTIONS" :key="o" :value="o">
              {{ o === 0 ? t('mon_refresh_off') : o + t('mon_refresh_sec') }}
            </option>
          </select>
        </div>
        <div class="qd-action-group">
          <!-- 回收站全站只有一份（设置 → 历史 → 回收站），这里只留一个跳转入口 -->
          <button class="qd-btn" :title="t('recycleBin')" @click="openRecycleBin">
            <Trash2 :size="14" /> {{ t('recycleBin') }}
          </button>
          <!-- 机器人地址是全局配置（设置 → 机器人通知），这里只跳转，不再重复弹一份表单 -->
          <button class="qd-btn" :title="t('notifyGoSettings')" @click="openNotifySettings">
            <BellIcon :size="14" /> {{ t('mon_nf_title') }}
          </button>
        </div>
        <span class="qd-action-sep"></span>
        <button class="qd-btn qd-btn-primary" @click="openCreate"><Plus :size="14" /> {{ t('mon_add') }}</button>
      </div>
    </PageHeader>

    <div v-if="error" class="mon-error">{{ error }}</div>

    <!-- 汇总条：三个视图共用，状态页不许把统计顶掉，否则切 tab 时上方数字会整块消失。
         排版统一为「小标签在上、读数在下」：数字与单位贴同一基线，
         标签也不再比数字深一级灰——两行各自成组，视线不会在卡片里来回跳。 -->
    <div v-if="monitors.length" class="mon-summary">
      <div class="sum-item">
        <span class="sum-lbl">{{ t('mon_sum_total') }}</span>
        <span class="sum-val"><span class="sum-num">{{ summary.total }}</span></span>
      </div>
      <div class="sum-item up">
        <span class="sum-lbl">{{ t('mon_sum_up') }}</span>
        <span class="sum-val"><span class="sum-num">{{ summary.up }}</span></span>
      </div>
      <div class="sum-item down">
        <span class="sum-lbl">{{ t('mon_sum_down') }}</span>
        <span class="sum-val"><span class="sum-num">{{ summary.down }}</span></span>
      </div>
      <!-- 平均延迟带趋势：单独一个 268ms 看不出好坏，跟自己的 24h 均值比才有方向 -->
      <div class="sum-item" :title="t('monLatTrendTtl')">
        <span class="sum-lbl">{{ t('mon_sum_latency') }}</span>
        <span class="sum-val">
          <span class="sum-num">{{ summary.avgLatency }}<i>ms</i></span>
          <span v-if="summary.avgLatency > 0 && summary.avg24 > 0" :class="['sum-trend', summary.trendDir]">
            <TrendingUp v-if="summary.trendDir === 'up'" :size="11" />
            <TrendingDown v-else-if="summary.trendDir === 'down'" :size="11" />
            <Minus v-else :size="11" />
            <template v-if="summary.trendDir === 'flat'">{{ t('monLatFlat') }}</template>
            <template v-else-if="summary.trendDir === 'up'">{{ t('monLatSlower', { p: Math.abs(summary.trendPct) }) }}</template>
            <template v-else>{{ t('monLatFaster', { p: Math.abs(summary.trendPct) }) }}</template>
          </span>
        </span>
      </div>
    </div>

    <!-- 视图切换：活动列表 / 状态页（回收站在设置 → 历史） -->
    <div class="mon-viewtabs">
      <QdTabs
        :model-value="viewMode"
        :tabs="viewTabs"
        variant="chip"
        @update:model-value="(v: string) => switchView(v as 'active' | 'status')"
      >
        <template #tab="{ tab }">
          {{ tab.label }}
          <span v-if="tab.id === 'active' && summary.total" class="vt-count">{{ summary.total }}</span>
        </template>
      </QdTabs>
    </div>

    <!-- 分组筛选（有命名分组时才出现，否则纯属噪音） -->
    <div v-if="viewMode !== 'status' && monitorGroups.length" class="group-filter">
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

    <!-- 搜索 + 批量操作条：只要勾了东西就必须出现，不再被「至少 4 条」挡住；
         勾选态下整条换成强调色底，否则批量动作藏在灰色工具条里等于没提示。 -->
    <div v-if="viewMode !== 'status' && (monitors.length >= 4 || selectedIds.size)" :class="['mon-tools', { 'mon-tools-batch': selectedIds.size > 0 }]">
      <label v-if="visibleMonitors.length > 1" class="mon-selall" :title="t('mon_sel_all')">
        <input type="checkbox" :checked="allVisibleSelected" @change="toggleSelectAll" />
      </label>
      <QdSearchBox v-model="query" class="mon-search" :placeholder="t('mon_search_ph')" />
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
        <button class="mon-selbtn danger" :disabled="batchBusy" @click="askBatchDelete">
          <Trash2 :size="12" />{{ t('mon_batch_delete') }}
        </button>
        <button class="mon-selbtn" @click="clearSelection">{{ t('mon_sel_clear') }}</button>
      </template>
    </div>

    <!-- 状态页：只读汇总 + 可导出的静态 HTML 快照，与列表互斥 -->
    <div v-if="viewMode === 'status'" class="mon-status-wrap">
      <StatusPage embedded @data="onStatusData" />
    </div>

    <!-- 列表（活动监控） -->
    <div v-else-if="viewMode === 'active'" class="mon-list">
      <div v-if="!loading && monitors.length === 0" class="mon-empty">
        <Radio :size="20" class="empty-icon" />
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
          :data-monitor-id="m.id"
          :class="['mon-item', { disabled: !m.enabled, picked: isSelected(m.id), dragging: dragId === m.id, 'mon-flash': flashMonitorId === m.id }]"
          :draggable="canDrag"
          @dragstart="onDragStart($event, m)"
          @dragover="onDragOver($event, m)"
          @drop="onDrop(m)"
          @dragend="dragId = ''"
        >
        <div v-key-click class="item-head" :aria-expanded="isExpanded(m.id)" @click="toggleExpand(m)">
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
            <!-- 第一行：名称 + 状态 + 目标地址（单行省略）。
                 地址从独立一行挪上来，第二行整行让给指标——原来是名称/地址/数字三层堆叠，
                 每一层都要从头扫一遍，视线负担比信息本身还重。 -->
            <div class="item-line1">
              <span class="item-name">{{ m.name }}</span>
              <span v-if="m.lastStatus" :class="['status-badge', m.lastStatus]">
                {{ m.lastStatus === 'up' ? t('mon_up') : t('mon_down') }}
              </span>
              <!-- 维护/静默原本混在指标行里，和延迟、状态码抢位置；它们改的是「行为」不是「读数」，
                   跟状态徽标排在一起更合理 -->
              <span v-if="inMaintenance(m)" class="item-tag" :title="t('mon_maint_ttl')">
                <Wrench :size="10" />{{ t('mon_maint_on', { t: maintRemaining(m) }) }}
              </span>
              <span v-if="inMute(m)" class="item-tag mute" :title="t('mon_mute_ttl')">
                <BellOff :size="10" />{{ t('mon_mute_on', { t: muteRemaining(m) }) }}
              </span>
              <span class="item-url" :title="targetText(m)">
                <component :is="kindIcon(m)" :size="11" class="item-url-icon" />
                <span class="item-url-text">{{ targetText(m) }}</span>
              </span>
            </div>
            <!-- 第二行：指标详情。统一 11px 灰字 + 竖线分隔，数值前用文字给出口径，
                 比一串并列的裸数字好扫。延迟后面那条细进度条是「占超时阈值的比例」，
                 纯数字看不出 131ms 到底算快还是接近超时。 -->
            <div class="item-meta">
              <span class="meta-cell meta-up" :title="t('mon_checks_ttl')">{{ t('mon_uptime') }} {{ uptimeText(m.id) }}</span>
              <span v-if="checksOf(m.id)" class="meta-cell">{{ t('mon_checks_n', { n: checksOf(m.id) }) }}</span>
              <span v-if="m.lastLatencyMs > 0" class="meta-cell meta-lat" :title="t('monLatBarTtl')">
                <span class="meta-lat-num">{{ m.lastLatencyMs }}ms</span>
                <span :class="['lat-bar', latLevel(m)]"><i :style="{ width: latPct(m) + '%' }"></i></span>
                <span v-if="avgLatOf(m.id)" class="meta-sub" :title="t('mon_avg_ttl')">{{ t('monMetaAvg', { ms: avgLatOf(m.id) }) }}</span>
              </span>
              <!-- p95/p99 从 ListMonitorStats 读：只在有样本（>0）时显示，0 会被当成「没统计」而不是「0ms」 -->
              <span v-if="p95Of(m.id) > 0" class="meta-cell" :title="t('monitorPercentileTip')">p95 {{ p95Of(m.id) }}ms</span>
              <span v-if="p99Of(m.id) > 0" class="meta-cell" :title="t('monitorPercentileTip')">p99 {{ p99Of(m.id) }}ms</span>
              <!-- 状态码列复用给非 http 类型：tcp 没有码（不显示），process 的 code 是进程数，
                   cmd 的 code 是退出码——同一格换成对应含义，比硬写 HTTP 更不容易误读 -->
              <span v-if="m.lastStatusCode && kindOf(m) === 'http'" class="meta-cell" :title="t('mon_status_code')">HTTP {{ m.lastStatusCode }}</span>
              <span v-else-if="m.lastStatusCode && kindOf(m) === 'process'" class="meta-cell" :title="t('mon_proc_count_ttl')">{{ m.lastStatusCode }}{{ t('mon_proc_count_unit') }}</span>
              <span v-else-if="m.lastStatusCode && kindOf(m) === 'cmd'" class="meta-cell" :title="t('mon_exit_code_ttl')">{{ t('mon_exit_code') }} {{ m.lastStatusCode }}</span>
              <span v-if="certText(m)" :class="['meta-cell', 'meta-cert', { warn: certWarn(m) }]">
                <component :is="certWarn(m) ? XCircle : CheckCircle2" :size="11" /> {{ certText(m) }}
              </span>
              <span class="meta-cell">{{ t('mon_checked') }} {{ relTime(m.lastCheckedTs) }}</span>
              <span v-if="lastDownText(m.id)" class="meta-cell meta-down">{{ t('mon_last_down') }} {{ lastDownText(m.id) }}</span>
            </div>
          </div>
          <div v-key-click class="item-actions" @click.stop>
            <QdSwitch :model-value="m.enabled" @update:model-value="() => toggleEnabled(m)"
                      :label="m.enabled ? t('mon_enabled') : t('mon_disabled')"
                      :title="m.enabled ? t('mon_enabled') : t('mon_disabled')" />
            <button class="act" :disabled="checkingId === m.id" @click="checkNow(m)" :title="t('mon_check_now')">
              <RefreshCw :size="14" :class="{ spin: checkingId === m.id }" />
            </button>
            <button class="act" @click="openEdit(m)" :title="t('mon_edit')"><Pencil :size="14" /></button>
            <!-- 静默 / 维护 / 删除收进 ⋯：这三个是低频动作，常驻只会把
                 「立即检测」「编辑」稀释成一片同样大小的灰图标 -->
            <div class="mon-more-wrap">
              <button class="act" :class="{ active: menuFor === m.id || inMute(m) || inMaintenance(m) }"
                      :title="t('monMore')" :aria-expanded="menuFor === m.id" @click="toggleRowMenu(m.id)">
                <Ellipsis :size="14" />
              </button>
              <div v-if="menuFor === m.id" class="mon-more-pop" @click.stop>
                <button class="mon-menu-item" @click="menuFor = ''; toggleMute(m)">
                  <BellOff :size="13" />{{ inMute(m) ? t('mon_mute_cancel') : t('mon_mute_1h') }}
                  <span v-if="inMute(m)" class="mm-hint">{{ muteRemaining(m) }}</span>
                </button>
                <button class="mon-menu-item" @click="menuFor = ''; toggleMaintenance(m)">
                  <Wrench :size="13" />{{ inMaintenance(m) ? t('mon_maint_cancel') : t('mon_maint_1h') }}
                  <span v-if="inMaintenance(m)" class="mm-hint">{{ maintRemaining(m) }}</span>
                </button>
                <button class="mon-menu-item danger" @click="menuFor = ''; remove(m)">
                  <Trash2 :size="13" />{{ t('mon_delete') }}
                </button>
              </div>
            </div>
          </div>
          <button class="expand-btn" :aria-expanded="isExpanded(m.id)" @click.stop="toggleExpand(m)" :title="t('mon_logs')">
            <ChevronDown v-if="!isExpanded(m.id)" :size="16" />
            <ChevronUp v-else :size="16" />
          </button>
        </div>

        <!-- 故障行下方独立告警区：错误串原来接在网址后面，长错误会把地址挤没，
             而且和正常指标同号同色，读起来像一行脏数据。这里给淡红底 + 可换行的完整错误。 -->
        <div v-if="m.lastError" class="err-banner">
          <AlertTriangle :size="14" class="eb-icon" />
          <div class="eb-main">
            <div class="eb-title">
              {{ t('monErrBanner') }}
              <span class="eb-time">{{ relTime(m.lastCheckedTs) }}</span>
            </div>
            <p class="eb-msg">{{ m.lastError }}</p>
          </div>
          <button class="eb-retry" :disabled="checkingId === m.id" @click.stop="checkNow(m)">
            <RefreshCw :size="12" :class="{ spin: checkingId === m.id }" />{{ t('mon_check_now') }}
          </button>
        </div>

        <!-- 检测日志 -->
        <div v-if="isExpanded(m.id)" class="logs-panel">
          <div class="logs-head">
            <span>{{ t('mon_logs') }} ({{ logsMap[m.id]?.length || 0 }})</span>
            <div class="logs-head-right">
              <!-- 6.3 时间范围切换：收编到 QdTabs（每个监控独立一份状态） -->
              <QdTabs
                :model-value="logRangeOf(m.id)"
                :tabs="rangeTabs"
                variant="segmented"
                :aria-label="t('mon_logs')"
                @update:model-value="(v: string) => { setLogRange(m.id, v); onRangeChange(m.id) }"
              />
              <button class="logs-btn" :disabled="!logsMap[m.id]?.length" @click="exportCsv(m)" :title="t('mon_export_csv')">
                <Download :size="12" /> {{ t('mon_export_csv') }}
              </button>
              <button class="logs-btn" :disabled="!logsMap[m.id]?.length" @click="saveLogsAs($event, m)" :title="t('monSaveLogs')">
                <Save :size="12" /> {{ t('monSaveLogs') }}
              </button>
              <button class="logs-btn danger" @click="clearLogs(m)">{{ t('mon_clear_logs') }}</button>
            </div>
          </div>
          <div v-if="logsFailed.has(m.id)" class="logs-load-error">
            <span>{{ t('monLogLoadFailed') }}</span>
            <button class="logs-btn" @click="onRangeChange(m.id)">{{ t('statusRetry') }}</button>
          </div>
          <div v-else-if="!logsMap[m.id] || logsMap[m.id].length === 0" class="logs-empty">{{ t('mon_no_logs') }}</div>
          <template v-else>
            <div class="logs-viz">
              <!-- 正常率环形图：与折线图共用同一份 logsMap，切范围一起变 -->
              <div class="uptime-ring" :title="t('mon_uptime_ring_ttl')">
                <div class="ring-wrap">
                  <svg viewBox="0 0 64 64" class="ring-svg" aria-hidden="true">
                    <circle class="ring-track" cx="32" cy="32" r="26" />
                    <circle
                      class="ring-value"
                      :class="{ warn: uptimeOf(m.id).pct < 99, bad: uptimeOf(m.id).pct < 90 }"
                      cx="32" cy="32" r="26"
                      :stroke-dasharray="ringDash(uptimeOf(m.id).pct)"
                      transform="rotate(-90 32 32)"
                    />
                  </svg>
                  <div class="ring-center">
                    <span class="ring-pct">{{ uptimeOf(m.id).pct.toFixed(1) }}<i>%</i></span>
                    <span class="ring-lbl">{{ t('mon_uptime') }}</span>
                  </div>
                </div>
                <div class="ring-legend">
                  <span class="ok">{{ t('mon_up') }} {{ uptimeOf(m.id).up }}</span>
                  <span class="bad">{{ t('mon_down') }} {{ uptimeOf(m.id).down }}</span>
                </div>
              </div>
              <QdLineChart
                class="chart-canvas"
                :series="chartView(m.id).series"
                :height="200"
                :threshold="chartView(m.id).threshold"
                :threshold-name="t('chartLegendTimeout')"
                :anomalies="chartView(m.id).anomalies"
                :anomalies-name="t('mon_down')"
                :axis-name-x="t('chartAxisTime')"
                :axis-name-y="t('chartAxisLatency')"
                :tooltip-extra="chartView(m.id).tooltipExtra"
              />
            </div>
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

      <!-- 最近故障时间线：列表只回答「现在好不好」，这块补上「刚刚抖过没有」。
           放在列表末尾 + margin-top:auto——监控少时它落到页面底部填掉那片留白，
           监控多时自然跟着滚动，不额外占一屏。 -->
      <div v-if="recentDowns.length" class="mon-recent">
        <div class="mr-head">
          <History :size="12" />
          <span>{{ t('mon_last_down') }}</span>
          <span class="mr-hint">{{ t('monRecentHint') }}</span>
        </div>
        <button v-for="d in recentDowns" :key="d.id + '-' + d.ts" class="mr-row" @click="focusMonitor(d.id)">
          <span class="mr-dot"></span>
          <span class="mr-name">{{ d.name }}</span>
          <span class="mr-time">{{ d.rel }}</span>
        </button>
      </div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <QdModal v-model="showModal" :title="modalTitle" width="440px">
        <label>{{ t('mon_name') }}</label>
        <input v-model="fName" class="modal-input" :placeholder="t('mon_name_ph')" />

        <!-- 探测类型：同一张表承载六类探针，切换只影响「目标」与 http 专属参数。
             用分段按钮而不是下拉：能点到的选项比需要展开的列表快。 -->
        <label>{{ t('mon_kind') }}</label>
        <div class="kind-tab-wrap">
          <QdTabs
            v-model="fKind"
            :tabs="kindTabs"
            variant="segmented"
            :aria-label="t('mon_kind')"
          >
            <template #tab="{ tab }">
              <component :is="KIND_ICONS[tab.id]" :size="13" /> {{ tab.label }}
            </template>
          </QdTabs>
        </div>

        <template v-if="fKind === 'http'">
          <label>{{ t('mon_url') }}</label>
          <input v-model="fUrl" class="modal-input" placeholder="https://example.com" />
        </template>
        <!-- dns/ping 单独分支：它们的目标格式与旧四类不同，且新增 i18n 键按规范用 camelCase，
             不复用 mon_target_<kind> 的历史下划线动态键（照 cmd 的 v-else 风格加 v-else-if）。 -->
        <template v-else-if="fKind === 'dns'">
          <label>{{ t('monitorTargetDns') }}</label>
          <input v-model="fTarget" class="modal-input" :placeholder="t('monitorTargetDnsPh')" />
          <p class="field-hint">{{ t('monitorTargetDnsHint') }}</p>
        </template>
        <template v-else-if="fKind === 'ping'">
          <label>{{ t('monitorTargetPing') }}</label>
          <input v-model="fTarget" class="modal-input" :placeholder="t('monitorTargetPingPh')" />
          <p class="field-hint">{{ t('monitorTargetPingHint') }}</p>
        </template>
        <template v-else>
          <label>{{ t('mon_target_' + fKind) }}</label>
          <textarea v-if="fKind === 'cmd'" v-model="fTarget" class="modal-input mon-textarea" rows="2"
            :placeholder="t('mon_target_' + fKind + '_ph')"></textarea>
          <input v-else v-model="fTarget" class="modal-input" :placeholder="t('mon_target_' + fKind + '_ph')" />
          <p class="field-hint">{{ t('mon_target_' + fKind + '_hint') }}</p>
        </template>

        <template v-if="fKind === 'http'">
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
                <option v-for="e in EXPECTED" :key="e" :value="e">{{ e }}</option>
              </select>
            </div>
          </div>
        </template>

        <!-- 请求头对所有 HTTP 方法都可用：GET/DELETE 调鉴权接口同样要带
             Authorization / Token，不能因为没 body 就让它们只能发裸请求。
             请求体仍只给 POST/PUT/PATCH（showBodyFields）。 -->
        <template v-if="fKind === 'http'">
          <label>{{ t('mon_http_headers') }}</label>
          <textarea v-model="fHeaders" class="modal-input mon-textarea" rows="3"
            :placeholder="t('mon_http_headers_ph')"></textarea>
        </template>
        <template v-if="showBodyFields && fKind === 'http'">
          <label>{{ t('mon_http_body') }}</label>
          <textarea v-model="fBody" class="modal-input mon-textarea" rows="3"
            :placeholder="t('mon_http_body_ph')"></textarea>
        </template>

        <div class="modal-grid">
          <div>
            <label>{{ t('mon_interval') }}{{ t('sched_unit_sec') }}</label>
            <input v-model.number="fIntervalVal" type="number" min="5" class="modal-input" />
            <p class="field-hint">{{ t('monitorIntervalHint') }}</p>
          </div>          <div>
            <label>{{ t('mon_timeout') }}</label>
            <div class="interval-row">
              <input v-model.number="fTimeout" type="number" min="1" class="modal-input interval-num" />
              <span class="unit-suffix">{{ t('sched_unit_sec') }}</span>
            </div>
          </div>
        </div>

        <template v-if="fKind === 'http'">
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
        </template>

        <!-- 6.2 内容关键字 / 正则匹配：http 匹配响应体，cmd 匹配命令输出 -->
        <template v-if="fKind === 'http' || fKind === 'cmd'">
          <label>{{ t('mon_content_match') }}</label>
          <select v-model="fContentMatchType" class="modal-input">
            <option v-for="mt in MATCH_TYPES" :key="mt" :value="mt">{{ t('mon_match_' + mt) }}</option>
          </select>
          <input v-if="fContentMatchType !== 'none'" v-model="fContentMatchPattern" class="modal-input mt-2"
            :placeholder="t('mon_match_pattern_ph')" />
        </template>

        <div class="toggle-row">
          <label class="toggle-label"><input type="checkbox" v-model="fNotifyDown" /> {{ t('mon_notify_down') }}</label>
          <label class="toggle-label"><input type="checkbox" v-model="fNotifyUp" /> {{ t('mon_notify_up') }}</label>
          <label class="toggle-label"><input type="checkbox" v-model="fEnabled" /> {{ t('mon_enable_now') }}</label>
        </div>

        <!-- 误报抑制：连续 N 次失败才告警 -->
        <div class="interval-row mt-2">
          <label>{{ t('mon_suppress_threshold') }}</label>
          <input v-model.number="fDownAlertThreshold" type="number" min="1" max="20" class="modal-input interval-num interval-num--fixed" />
          <span class="unit-suffix">{{ t('mon_suppress_unit') }}</span>
        </div>

        <!-- 多条件告警：延迟阈值（0=关闭）。达到「连续 N 次失败」时告警，延迟超阈值
             只在消息里附加说明——它不参与是否告警的判定（否则第 N 次延迟正常就永远不告警） -->
        <div class="interval-row mt-2">
          <label>{{ t('mon_latency_alert') }}</label>
          <input v-model.number="fLatencyAlert" type="number" min="0" step="100" class="modal-input interval-num interval-num--fixed" placeholder="0" />
          <span class="unit-suffix">{{ t('mon_latency_alert_unit') }}</span>
        </div>
        <p class="field-hint">{{ t('mon_latency_alert_hint') }}</p>

        <!-- 分组：列表按组分区，监控多了之后按业务线/环境一眼定位 -->
        <label class="mt-2">{{ t('mon_group') }}</label>
        <input v-model="fGroup" class="modal-input" :placeholder="t('mon_group_ph')" list="mon-group-options" />
        <datalist id="mon-group-options">
          <option v-for="g in monitorGroups" :key="g" :value="g" />
        </datalist>

        <!-- 校验提示画在弹窗里：页面级 error 被遮罩盖住，等于没提示 -->
        <p v-if="formErr" class="form-err">{{ formErr }}</p>

      <template #footer>
        <button class="qd-btn" @click="showModal = false">{{ t('cancel') }}</button>
        <button class="qd-btn qd-btn-primary" :disabled="saving" @click="save">{{ t('save') }}</button>
      </template>
    </QdModal>

    <ConfirmDialog
      :visible="showDelConfirm"
      :message="t('mon_delete_confirm')"
      @confirm="confirmDel"
      @cancel="showDelConfirm = false"
    />

    <!-- 批量删除单独一个确认框：文案要带数量，用户才知道自己到底要删几条 -->
    <ConfirmDialog
      :visible="showBatchDel"
      :message="t('mon_batch_delete_confirm', { n: selectedIds.size })"
      @confirm="batchDelete"
      @cancel="showBatchDel = false"
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
  font-size: var(--text-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.refresh-control:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.refresh-select {
  border: none; background: transparent; color: inherit; font-size: var(--text-sm);
  font-family: inherit; outline: none; cursor: pointer; padding: 0;
  max-width: 64px;
}
.refresh-select option { background: var(--color-popover-bg); color: var(--color-text-primary); }

.mon-error {
  margin: var(--space-5) var(--space-7) 0; padding: var(--space-4) var(--space-5); font-size: var(--text-sm);
  color: var(--color-danger-ink); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md); flex-shrink: 0;
}

.mon-summary {
  display: flex; gap: var(--space-3); margin: 14px var(--space-7) var(--space-5); flex-shrink: 0;
}
.sum-item {
  flex: 1; display: flex; flex-direction: column; gap: 2px; padding: 9px 14px 10px;
  background: var(--color-card-bg); border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
}
/* 标签提到数字上方并用 muted（而不是 disabled）：disabled 那档在深底上低于 4.5:1，
   这一行本来就小，再压灰等于读不出来。 */
.sum-lbl { font-size: var(--text-xs); color: var(--color-text-muted); letter-spacing: 0.02em; }
/* 数字与单位同一基线、只留 1px 间隙：单位和数字属于同一个读数，不该被拉开成两段 */
.sum-val { display: flex; align-items: baseline; gap: var(--space-3); min-width: 0; }
.sum-num {
  font-size: var(--text-xl); font-weight: 700; line-height: 1.15;
  color: var(--color-text-primary);
  /* 等宽数字：4 张卡并排时，3 和 268 的字形宽度不跳，读数才对齐 */
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.01em;
}
.sum-num i { font-size: var(--text-xs); font-weight: 500; font-style: normal; color: var(--color-text-muted); margin-left: 1px; }
/* 延迟趋势：向上=变慢（danger）、向下=变快（success）、持平中性色。
   箭头比纯文字快，百分比补上幅度。 */
.sum-trend {
  display: inline-flex; align-items: center; gap: 2px; min-width: 0;
  font-size: var(--text-2xs); color: var(--color-text-muted); white-space: nowrap;
}
.sum-trend.up { color: var(--color-danger-ink); }
.sum-trend.down { color: var(--color-success-ink); }
.sum-item.up .sum-num { color: var(--color-success-ink); }
.sum-item.down .sum-num { color: var(--color-danger-ink); }

.mon-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); padding: 0 var(--space-7) var(--space-6); }

/* ---- 分组筛选与分组头 ---- */
.group-filter { display: flex; gap: var(--space-2); flex-wrap: wrap; margin: 0 var(--space-7) 10px; flex-shrink: 0; }
.group-chip {
  padding: 3px 10px; border-radius: var(--radius-md);
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); font-size: var(--text-xs); font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.group-chip:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.group-chip.active {
  background: var(--color-accent-bg); border-color: var(--color-accent); color: var(--color-accent-ink);
}
.group-head {
  display: flex; align-items: center; gap: var(--space-4);
  margin: var(--space-2) 0 0; flex-shrink: 0;
  font-size: var(--text-xs); font-weight: 600; color: var(--color-text-muted);
}
.group-head-name { letter-spacing: 0.06em; }
.group-head-count {
  font-weight: 400; padding: 0 var(--space-3); border-radius: var(--radius-md);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
}
/* 组头右侧的全选本组：只在组内多于一条时出现 */
.group-sel {
  margin-left: auto; padding: 1px var(--space-3); font-size: var(--text-2xs); font-family: inherit;
  color: var(--color-text-muted); background: none; border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); cursor: pointer;
}
.group-sel:hover { color: var(--color-accent-ink); border-color: var(--color-accent); }

/* ---- 搜索 + 批量条 ---- */
.mon-tools {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 0 var(--space-7) var(--space-2);
}
.mon-selall { display: inline-flex; align-items: center; flex-shrink: 0; cursor: pointer; }
/* 皮肤由 QdSearchBox 统一提供，这里只定宽。 */
.mon-search {
  width: 220px; flex-shrink: 0;
}
.mon-search-count { font-size: var(--text-xs); color: var(--color-text-muted); flex-shrink: 0; }
.mon-tools-fill { flex: 1; }
.mon-sel-count { font-size: var(--text-xs); font-weight: 500; color: var(--color-accent-ink); }
.mon-selbtn {
  height: 24px; padding: 0 var(--space-4); font-size: var(--text-xs); font-family: inherit; cursor: pointer;
  color: var(--color-text-secondary); background: var(--color-bg-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  display: inline-flex; align-items: center; gap: var(--space-2); white-space: nowrap;
}
.mon-selbtn:hover:not(:disabled) { color: var(--color-accent-ink); border-color: var(--color-accent); }
.mon-selbtn:disabled { color: var(--color-text-disabled); cursor: not-allowed; }
.mon-selbtn.danger:hover:not(:disabled) { color: var(--color-danger-ink); border-color: var(--color-danger); }
/* 勾选态把整条工具条换成强调底：批量动作只在选中后出现，
   如果还跟搜索框混在同一条灰底里，等于没有提示。 */
.mon-tools-batch {
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-sm);
  margin: 0 var(--space-7) var(--space-2);
  padding: 4px var(--space-4);
  flex-wrap: wrap; row-gap: var(--space-3);
}
/* 批量条自带左右内缩后，搜索框不再需要外层的 20px padding */
.mon-tools-batch .mon-search { background: var(--color-bg-secondary); }
.mon-check { flex-shrink: 0; cursor: pointer; }
.field-hint { margin: var(--space-2) 0 0; font-size: var(--text-xs); color: var(--color-text-muted); }
.mon-empty { text-align: center; padding: 72px var(--space-4); color: var(--color-text-muted); }
/* 空状态图标：一根细线图标直接浮在深底上，容易被读成「没加载出来」。
   给一层方底 + 细边，它才是一个完整的标识。
   盒子固定 44（= .qd-empty-icon），图标一律 20：早先靠 padding + content-box
   "适配任意尺寸"，结果 28/30/36/48 各自撑成 48~68，同屏不同页的图标底托大小不一。 */
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
  margin-bottom: var(--space-5);
}
.empty-hint { font-size: var(--text-sm); color: var(--color-text-muted); margin-top: var(--space-3); }

.mon-item {
  background: var(--color-card-bg); border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
.mon-item:hover { background: var(--color-bg-hover); }
.mon-item.disabled { color: var(--color-text-disabled); }
/* 勾选行的高亮不能只靠 checkbox：停用行本身半透明，只多一个小勾很难看出来 */
.mon-item.picked { border-color: var(--color-accent); }
/* 拖拽中：半透明 + 虚线框，告诉用户「这条正在被拖」而不是页面卡了 */
.mon-item.dragging { opacity: 0.45; border-style: dashed; }
/* 通知深链定位后的短暂高亮：监控点可能几十条，不给个落点找不到滚到了哪 */
.mon-item.mon-flash {
  border-color: var(--color-accent);
  animation: mon-flash 1.8s ease-out 1;
}
@keyframes mon-flash {
  0%, 55% { background: var(--color-bg-hover); }
  100% { background: var(--color-bg-secondary); }
}
.mon-item[draggable="true"] { cursor: grab; }
.mon-item[draggable="true"]:active { cursor: grabbing; }
/* 回收站里的条目：只读展示，没有展开/勾选那套交互 */

/* 维护 / 静默已收进第一行的 .item-tag，原来的 .meta-maint / .meta-mute 随之删除 */
.mon-item .act.active { color: var(--color-warning-ink); }

/* 视图切换 tab：活动列表 / 状态页，像标签页一样一眼分清当前在哪 */
.mon-viewtabs { display: flex; gap: var(--space-2); padding: 0 var(--space-7) 10px; }
/* 状态页与列表互斥，继承同一块可滚动内容区，避免多出一截空白。 */
.mon-status-wrap { flex: 1; min-height: 0; overflow-y: auto; padding: 0 var(--space-7) var(--space-6); }
.vt-count {
  min-width: 18px; height: 18px; padding: 0 5px; border-radius: var(--radius-md);
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--color-bg-tertiary); color: var(--color-text-muted); font-size: var(--text-xs);
}
.vt-count.warn { background: var(--color-danger-bg); color: var(--color-danger-ink); }
.mon-state { display: flex; align-items: center; gap: var(--space-4); justify-content: center; padding: var(--space-8); color: var(--color-text-muted); }

.item-head { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-5) 14px; cursor: pointer; }

.status-dot { flex-shrink: 0; width: 10px; height: 10px; border-radius: 50%; background: var(--color-text-disabled); }
/* 与 DashboardPage .dot 同一克制画法：实色点，down 额外一圈实底环，不用发光 */
.status-dot.up { background: var(--color-success); }
.status-dot.down { background: var(--color-danger); box-shadow: 0 0 0 3px var(--color-danger-bg); }
.status-dot.unknown { background: var(--color-text-muted); }

.item-main { flex: 1; min-width: 0; }
.item-line1 { display: flex; align-items: center; gap: var(--space-3); min-width: 0; }
.item-name {
  font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary);
  max-width: 38%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* 状态徽标：实心绿/红底在深色下过曝，改成 12% 透明底 + 亮字 + 一圈淡描边。
   描边用语义色的 40% 透明度（--color-*-border），比纯色块克制，又比无边框清晰。 */
.status-badge {
  display: inline-flex; align-items: center; flex-shrink: 0;
  height: 17px; padding: 0 6px;
  font-size: var(--text-2xs); font-weight: 500; line-height: 1;
  border-radius: var(--radius-full); border: 1px solid transparent;
  background: transparent;
}
.status-badge.up { background: var(--color-success-bg); color: var(--color-success-ink); border-color: var(--color-success-border); }
.status-badge.down { background: var(--color-danger-bg); color: var(--color-danger-ink); border-color: var(--color-danger-border); }
/* 维护 / 静默标记：与状态徽标同高但更轻，不抢「正常/故障」的判断 */
.item-tag {
  display: inline-flex; align-items: center; gap: 3px; flex-shrink: 0;
  height: 17px; padding: 0 6px; border-radius: var(--radius-full);
  font-size: var(--text-2xs); color: var(--color-warning-ink);
  background: var(--color-warning-bg); border: 1px solid var(--color-warning-border);
  white-space: nowrap;
}
/* 静默：探测照常、日志照常，只是不吵人——比「维护中」更轻，用中性灰而不是告警黄 */
.item-tag.mute {
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}

/* 地址与名称同一行：flex:1 吃掉剩余宽度，超出部分靠内部 span 省略。
   直接给 flex 容器写 text-overflow 对匿名文本节点无效，必须套一层。 */
.item-url {
  display: flex; align-items: center; gap: 4px; flex: 1 1 auto; min-width: 24px;
  font-size: var(--text-xs); color: var(--color-text-muted); overflow: hidden;
}
.item-url-icon { flex-shrink: 0; opacity: 0.75; }
.item-url-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 指标行：11px 灰字 + 竖线分隔。原来 12px 且和地址同色，整块看起来像一行散落的数字。 */
.item-meta {
  display: flex; flex-wrap: wrap; align-items: center; row-gap: 2px;
  margin-top: 3px; font-size: var(--text-xs); color: var(--color-text-muted);
}
.meta-cell { display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
.meta-cell + .meta-cell::before {
  content: '|'; margin: 0 var(--space-3); color: var(--color-border); font-weight: 400;
}
.meta-up { color: var(--color-accent-ink); }
.meta-lat { color: var(--color-text-secondary); font-variant-numeric: tabular-nums; }
.meta-lat-num { font-weight: 500; }
.meta-sub { color: var(--color-text-muted); }
.meta-down { color: var(--color-warning-ink); }
/* 延迟刻度：占超时阈值的比例。细到 3px，只做「离超时还有多远」的暗示，不喧宾夺主。 */
.lat-bar {
  display: inline-block; width: 34px; height: 3px; flex-shrink: 0;
  border-radius: var(--radius-full); background: var(--color-border-light); overflow: hidden;
}
.lat-bar > i { display: block; height: 100%; border-radius: inherit; background: var(--color-success); }
.lat-bar.warn > i { background: var(--color-warning); }
.lat-bar.bad > i { background: var(--color-danger); }
.meta-cert { color: var(--color-text-muted); }
.meta-cert.warn { color: var(--color-warning-ink); font-weight: 500; }

.item-actions { display: flex; align-items: center; gap: 3px; flex-shrink: 0; }
.act {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-muted);
  border-radius: var(--radius-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.act:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-active); }
.act:disabled { color: var(--color-text-disabled); cursor: default; }
.act.danger:hover { color: var(--color-danger-ink); background: var(--color-danger-bg); }
/* 行操作默认收起，只留启用开关（那是状态信号，必须常驻）。
   与知识库 .kb-doc-actions 同一套「常驻占位 + 透明度切换」，不用
   display:none —— 那会让 hover 时行凭空变宽、整行跳一下。
   disabled 的按钮（正在检测）保持可见，转圈才有落点。 */
.item-actions .act { opacity: 0; transition: opacity var(--transition-fast); }
.mon-item:hover .item-actions .act,
.mon-item.picked .item-actions .act,
.item-actions:focus-within .act,
.item-actions .act:disabled { opacity: 1; }
.spin { animation: mon-spin 0.8s linear infinite; }
@keyframes mon-spin { to { transform: rotate(360deg); } }
.expand-btn {
  width: 26px; height: 26px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-muted);
  border-radius: var(--radius-sm); cursor: pointer; flex-shrink: 0; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.expand-btn:hover { color: var(--color-text-primary); background: var(--color-bg-active); }
/* 开关的强调色饱和度收到 0.7：它只是「开/关」的状态指示，
   和旁边的「正常/故障」徽标抢同一块视觉注意力没有必要。 */
.item-actions :deep(.qd-switch[aria-checked='true']) { filter: saturate(0.7); }
/* ⋯ 菜单：静默 / 维护 / 删除。z-index 用 dropdown 层，避免被下一条监控盖住 */
.mon-more-wrap { position: relative; display: flex; align-items: center; }
.mon-more-pop {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: var(--z-dropdown);
  display: flex; flex-direction: column; gap: 2px; width: 186px; padding: var(--space-2);
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md); box-shadow: var(--shadow-pop);
}
.mon-menu-item {
  display: flex; align-items: center; gap: var(--space-3); width: 100%;
  padding: var(--space-3); border: none; background: none; border-radius: var(--radius-xs);
  color: var(--color-text-secondary); font-size: var(--text-sm); font-family: inherit;
  text-align: left; cursor: pointer; white-space: nowrap;
}
.mon-menu-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.mon-menu-item.danger:hover { color: var(--color-danger-ink); background: var(--color-danger-bg); }
.mm-hint { margin-left: auto; font-size: var(--text-2xs); color: var(--color-text-muted); }

/* 故障告警条：淡红底 + 红字，与正常指标行彻底分开。
   错误串可能是整段 Get "https://…" 的报错，必须允许换行，不能截断成省略号。 */
.err-banner {
  display: flex; align-items: flex-start; gap: var(--space-4);
  margin: 0 14px var(--space-5); padding: var(--space-4) var(--space-5);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-sm);
  color: var(--color-danger-ink);
}
.eb-icon { flex-shrink: 0; margin-top: 1px; }
.eb-main { flex: 1; min-width: 0; }
.eb-title { display: flex; align-items: center; gap: var(--space-3); font-size: var(--text-xs); font-weight: 600; }
.eb-time { font-weight: 400; opacity: 0.8; }
.eb-msg {
  margin: 3px 0 0; font-size: var(--text-xs); line-height: 1.55;
  opacity: 0.92; word-break: break-all;
}
.eb-retry {
  display: inline-flex; align-items: center; gap: var(--space-2); flex-shrink: 0;
  height: 22px; padding: 0 var(--space-4); font-family: inherit; font-size: var(--text-xs);
  color: var(--color-danger-ink); background: transparent; cursor: pointer;
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-xs);
}
.eb-retry:hover:not(:disabled) { background: var(--color-danger-bg); }
.eb-retry:disabled { color: var(--color-text-disabled); border-color: var(--color-border); cursor: default; }

/* 最近故障时间线：margin-top:auto 让它在监控少时落到页面底部填白，
   监控多时自然跟在列表末尾一起滚。 */
.mon-recent {
  margin-top: auto; padding: var(--space-4) var(--space-5) var(--space-4);
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.mr-head {
  display: flex; align-items: center; gap: var(--space-3);
  font-size: var(--text-xs); font-weight: 600; color: var(--color-text-secondary);
  margin-bottom: var(--space-3);
}
.mr-hint { font-weight: 400; color: var(--color-text-muted); }
.mr-row {
  display: flex; align-items: center; gap: var(--space-4); width: 100%;
  padding: var(--space-3) var(--space-2); border: none; background: none;
  border-radius: var(--radius-xs); cursor: pointer; font-family: inherit;
  font-size: var(--text-xs); color: var(--color-text-secondary); text-align: left;
}
.mr-row:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.mr-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; background: var(--color-danger); }
.mr-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mr-time { flex-shrink: 0; color: var(--color-text-muted); font-variant-numeric: tabular-nums; }

.logs-panel { border-top: 1px solid var(--color-border); padding: var(--space-4) 14px var(--space-5); }
/* 面板头部允许换行：范围切换 + 导出 + 清空三个控件加起来比一行还宽时，
   原来 justify-content 会把右侧那组挤到溢出，导出按钮直接看不见。 */
.logs-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-3); gap: var(--space-4); flex-wrap: wrap; }
.logs-head > span { font-size: var(--text-sm); color: var(--color-text-secondary); font-weight: 500; }
.logs-head-right { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; row-gap: var(--space-3); }
/* 导出/清空是一组并列的次要操作：做成同样大小的按钮，导出用强调色描边，
   否则和「清空」一样灰，用户会以为那只是条说明文字。 */
.logs-btn {
  display: inline-flex; align-items: center; gap: var(--space-2);
  height: 24px; padding: 0 var(--space-4); font-size: var(--text-xs); font-family: inherit; cursor: pointer;
  color: var(--color-text-secondary); background: var(--color-bg-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  white-space: nowrap;
}
.logs-btn:hover:not(:disabled) { color: var(--color-accent-ink); border-color: var(--color-accent); }
.logs-btn:disabled { color: var(--color-text-disabled); cursor: not-allowed; }
.logs-btn.danger:hover:not(:disabled) { color: var(--color-danger-ink); border-color: var(--color-danger); }
.logs-empty { font-size: var(--text-xs); color: var(--color-text-muted); padding: var(--space-3) 0; }
.logs-load-error { display: inline-flex; align-items: center; gap: var(--space-3); font-size: var(--text-xs); color: var(--color-danger-ink); padding: var(--space-3) 0; }

/* 日志可视化：正常率环（原生 SVG）+ 延迟折线（QdLineChart） */
.logs-viz { display: flex; align-items: stretch; gap: var(--space-5); }
.chart-canvas { flex: 1; min-width: 0; }

/* ---- 正常率环形图 ---- */
/* 108px 固定宽：再窄环和百分比会互相挤压，再宽就抢折线图的空间。 */
.uptime-ring {
  flex: 0 0 108px; width: 108px;
  display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--space-3);
  background: var(--color-card-bg); border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md); padding: var(--space-4) var(--space-3);
}
.ring-wrap { position: relative; width: 72px; height: 72px; }
.ring-svg { width: 72px; height: 72px; display: block; }
.ring-track {
  fill: none; stroke: var(--color-bg-tertiary); stroke-width: 7;
}
.ring-value {
  fill: none; stroke: var(--color-success); stroke-width: 7; stroke-linecap: round;
  transition: stroke-dasharray 0.3s ease, stroke 0.2s;
}
.ring-value.warn { stroke: var(--color-warning); }
.ring-value.bad { stroke: var(--color-danger); }
/* 居中文字压在环上：绝对定位而不是 flex，避免百分比位数变化时环被顶动 */
.ring-center {
  position: absolute; inset: 0; display: flex; flex-direction: column;
  align-items: center; justify-content: center; gap: 0;
}
.ring-pct { font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary); line-height: 1.1; font-variant-numeric: tabular-nums; }
.ring-pct i { font-size: var(--text-2xs); font-style: normal; color: var(--color-text-muted); margin-left: 1px; }
.ring-lbl { font-size: var(--text-xs); color: var(--color-text-muted); }
.ring-legend { display: flex; gap: var(--space-4); font-size: var(--text-xs); font-variant-numeric: tabular-nums; }
.ring-legend .ok { color: var(--color-success-ink); }
.ring-legend .bad { color: var(--color-danger-ink); }

/* ---- 停机日历 ---- */
.down-cal { margin-top: 10px; }
.cal-head {
  display: flex; align-items: center; justify-content: space-between; gap: var(--space-4);
  font-size: var(--text-xs); color: var(--color-text-muted); margin-bottom: var(--space-3);
}
.cal-legend { display: inline-flex; align-items: center; gap: var(--space-1); font-size: var(--text-xs); color: var(--color-text-muted); }
.cal-legend .cal-cell { margin-left: var(--space-1); }
/* 一周一列（7 行），列自动流；格子 11px + 3px 间距，12 周约 190px 宽 */
.cal-grid {
  display: grid; grid-auto-flow: column;
  grid-template-rows: repeat(7, 11px);
  grid-auto-columns: 11px; gap: 3px;
  overflow-x: auto; padding-bottom: var(--space-1);
}
.cal-cell {
  display: block; width: 11px; height: 11px; border-radius: var(--radius-2xs);
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
}
.cal-cell.lv0 { background: var(--color-success-bg); border-color: var(--color-success-border); }
.cal-cell.lv1 { background: var(--color-warning-bg); border-color: var(--color-warning-border); }
.cal-cell.lv2 { background: var(--color-warning); border-color: var(--color-warning); }
.cal-cell.lv3 { background: var(--color-danger); border-color: var(--color-danger); }


/* 弹窗 */
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }

/* 探测类型切换：收编到 QdTabs（variant=segmented）。弹窗较窄，允许折行避免溢出。 */
.kind-tab-wrap { margin-bottom: var(--space-2); }
.kind-tab-wrap :deep(.qd-tabs) { flex-wrap: wrap; }

.interval-row { display: flex; gap: var(--space-2); align-items: center; }
.interval-num { flex: 1; min-width: 0; }
.interval-num--fixed { flex: 0 0 72px; }
.mt-2 { margin-top: var(--space-2); }
.unit-suffix { font-size: var(--text-sm); color: var(--color-text-muted); width: 90px; flex-shrink: 0; text-align: center; }

.toggle-label { display: flex; align-items: center; gap: var(--space-3); font-size: var(--text-sm); color: var(--color-text-secondary); cursor: pointer; margin: var(--space-2) 0 0; }
.toggle-label input { cursor: pointer; }
.toggle-row { display: flex; gap: var(--space-5); margin-top: var(--space-3); flex-wrap: wrap; }

/* 弹窗内校验提示：弹窗自己有内边距，这里不再加外边距 */
.form-err {
  margin: var(--space-3) 0 0; padding: 7px 10px; font-size: var(--text-sm);
  color: var(--color-danger-ink); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md);
}
</style>
