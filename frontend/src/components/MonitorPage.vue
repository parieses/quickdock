<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import {
  CreateMonitor, ListMonitors, UpdateMonitor,
  DeleteMonitor, SetMonitorEnabled, CheckMonitorNow,
  GetMonitorLogs, GetMonitorLogsSince, ClearMonitorLogs, ListMonitorStats,
} from '../../bindings/quickdock/services/appservice'
import type { Monitor } from '../../bindings/quickdock/internal/db/models'
import WebhookSettingsModal from './WebhookSettingsModal.vue'
import ConfirmDialog from './ConfirmDialog.vue'

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
  ChevronDown, ChevronUp, CheckCircle2, XCircle, Radio,
} from '@lucide/vue'

const { t } = useI18n()

const monitors = ref<Monitor[]>([])
const stats = ref<Record<string, MonitorStat>>({})
const logsMap = ref<Record<string, MonitorLog[]>>({})
const expanded = ref<Set<string>>(new Set())
const loading = ref(false)
const error = ref('')

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
    monitors.value = unwrap<Monitor[]>(await ListMonitors()) ?? []
    const st = unwrap<MonitorStat[]>(await ListMonitorStats()) ?? []
    const map: Record<string, MonitorStat> = {}
    for (const s of st) map[s.monitorId] = s
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

// 6.1 SSL 证书告警提前天数；6.2 内容匹配
const fCertWarnDays = ref(14)
const fContentMatchType = ref('none') // none | contains | not_contains | regex
const fContentMatchPattern = ref('')

const MATCH_TYPES = ['none', 'contains', 'not_contains', 'regex']

const METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'PATCH']
const EXPECTED = ['2xx', '3xx', '4xx', '5xx', '200', 'any']

function intervalVal(): number {
  return Math.max(5, Math.floor(fIntervalVal.value || 300))
}

function openCreate() {
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
  fContentMatchType.value = 'none'
  fContentMatchPattern.value = ''
  showModal.value = true
}

function openEdit(m: Monitor) {
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
  showModal.value = true
}

function buildPayload(): Monitor {
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
    lastStatus: '', lastCheckedAt: '', lastCheckedTs: 0, lastLatencyMs: 0,
    lastStatusCode: 0, lastError: '', sort: 0, createdAt: '',
  } as Monitor
}

const saving = ref(false)
async function save() {
  const p = buildPayload()
  if (!p.name) { error.value = t('mon_err_name'); return }
  if (!p.url) { error.value = t('mon_err_url'); return }
  saving.value = true
  error.value = ''
  try {
    if (editingId.value) await unwrap(await UpdateMonitor(p))
    else await unwrap(await CreateMonitor(p))
    showModal.value = false
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
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

async function confirmDel() {
  const m = delTarget.value
  if (!m) return
  showDelConfirm.value = false
  delTarget.value = null
  try { await unwrap(await DeleteMonitor(m.id)); expanded.value.delete(m.id); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}

// ---- 展开日志 ----
function isExpanded(id: string) { return expanded.value.has(id) }

// 6.3 趋势图时间范围切换
const LOG_RANGES = [
  { value: '24h', sec: 86400, label: '24h' },
  { value: '7d', sec: 86400 * 7, label: '7d' },
  { value: 'all', sec: 0, label: '全部' },
]
const logRange = ref('24h')

function rangeSec(): number {
  const r = LOG_RANGES.find(x => x.value === logRange.value)
  return r ? r.sec : 86400
}

async function loadLogs(id: string) {
  try {
    const since = rangeSec() > 0 ? Math.floor(Date.now() / 1000) - rangeSec() : 0
    logsMap.value[id] = unwrap<MonitorLog[]>(await GetMonitorLogsSince(id, since, 500)) ?? []
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
  const days = Math.floor(m.certExpiresAt - Date.now() / 1000) / 86400
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
const tooltip = ref<{ show: boolean; x: number; y: number; data: MonitorLog | null }>({
  show: false, x: 0, y: 0, data: null,
})

function onChartMove(e: MouseEvent, id: string) {
  const pts = chartPoints.get(id)
  if (!pts || pts.length === 0) { tooltip.value.show = false; return }
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
    tooltip.value = { show: true, x: e.clientX, y: e.clientY, data: nearest.data }
  } else {
    tooltip.value.show = false
  }
}
function onChartLeave() {
  tooltip.value.show = false
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
  ctx.fillStyle = '#1a1a2e'
  ctx.fillRect(0, 0, W, H)

  // 数据：取最近 50 条，时间正序（后端返回倒序）
  const data = [...logs].reverse()
  const n = data.length
  if (n < 2) {
    ctx.fillStyle = '#666'
    ctx.font = '12px sans-serif'
    ctx.textAlign = 'center'
    ctx.fillText('需要至少 2 次检测数据', W / 2, H / 2)
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
  ctx.strokeStyle = '#2a2a3e'
  ctx.lineWidth = 1
  for (let y = 0; y <= yMax; y += yStep) {
    const yy = yPos(y)
    ctx.beginPath(); ctx.moveTo(PL, yy); ctx.lineTo(W - PR, yy); ctx.stroke()
    ctx.fillStyle = '#666'
    ctx.font = '10px sans-serif'
    ctx.textAlign = 'right'
    ctx.fillText(y + 'ms', PL - 6, yy + 3)
  }

  // 超时线
  const timeoutY = yPos(timeout * 1000)
  if (timeoutY > PT) {
    ctx.strokeStyle = 'rgba(232,76,76,0.4)'
    ctx.setLineDash([4, 4])
    ctx.beginPath(); ctx.moveTo(PL, timeoutY); ctx.lineTo(W - PR, timeoutY); ctx.stroke()
    ctx.setLineDash([])
    ctx.fillStyle = 'rgba(232,76,76,0.6)'
    ctx.font = '9px sans-serif'
    ctx.textAlign = 'left'
    ctx.fillText('timeout ' + timeout + 's', PL + 4, timeoutY - 3)
  }

  // ---- X 轴标签（最多显示 6 个） ----
  const xLabelCount = Math.min(n, 6)
  const xLabelStep = Math.max(1, Math.floor((n - 1) / (xLabelCount - 1)))
  ctx.fillStyle = '#888'
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
  ctx.strokeStyle = '#4a9eff'
  ctx.lineWidth = 2
  ctx.stroke()

  // 再画点 + 上下箭头（根据 status）
  for (let i = 0; i < n; i++) {
    const d = data[i]
    const x = xPos(i), y = yPos(d.latencyMs)
    const r = 3.5
    if (d.status === 'up') {
      ctx.fillStyle = '#46b17b'
    } else {
      ctx.fillStyle = '#e84c4c'
    }
    ctx.beginPath()
    ctx.arc(x, y, r, 0, Math.PI * 2)
    ctx.fill()
    // 失败的显示小箭头或 x
    if (d.status !== 'up') {
      ctx.strokeStyle = '#e84c4c'
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
  ctx.fillStyle = 'rgba(0,0,0,0.6)'
  ctx.fillRect(W - PR - 110, PT + 2, 108, 38)
  ctx.font = '10px sans-serif'
  ctx.textAlign = 'left'
  ctx.fillStyle = '#aaa'
  ctx.fillText('检测 ' + total + ' 次', W - PR - 102, PT + 14)
  ctx.fillStyle = '#46b17b'
  ctx.fillText('正常 ' + upCnt, W - PR - 102, PT + 27)
  ctx.fillStyle = '#e84c4c'
  ctx.fillText('故障 ' + (total - upCnt), W - PR - 50, PT + 27)
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

let pollTimer: ReturnType<typeof setInterval> | undefined

function applyAutoRefresh() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = undefined }
  if (refreshSec.value > 0) {
    pollTimer = setInterval(async () => {
      await refresh()
      // 重新加载展开的监控日志并重绘图表
      for (const id of expanded.value) {
        await loadLogs(id)
        drawChart(id)
      }
    }, refreshSec.value * 1000)
  }
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
  if (pollTimer) { clearInterval(pollTimer); pollTimer = undefined }
})
</script>

<template>
  <div class="mon-page">
    <!-- 顶部栏 -->
    <div class="mon-header">
      <div class="mon-title-wrap">
        <h2 class="mon-title">{{ t('mon_title') }}</h2>
        <span class="mon-sub">{{ t('mon_subtitle') }}</span>
      </div>
      <div class="mon-header-actions">
        <div class="refresh-control">
          <RefreshCw :size="14" />
          <select v-model.number="refreshSec" class="refresh-select" :title="t('mon_auto_refresh')">
            <option v-for="o in REFRESH_OPTIONS" :key="o" :value="o">
              {{ o === 0 ? t('mon_refresh_off') : o + t('mon_refresh_sec') }}
            </option>
          </select>
        </div>
        <WebhookSettingsModal :label="t('mon_nf_title')" />
        <button class="add-btn" @click="openCreate"><Plus :size="15" /> {{ t('mon_add') }}</button>
      </div>
    </div>

    <div v-if="error" class="mon-error">{{ error }}</div>

    <!-- 汇总条 -->
    <div v-if="monitors.length" class="mon-summary">
      <div class="sum-item"><span class="sum-num">{{ summary.total }}</span><span class="sum-lbl">{{ t('mon_sum_total') }}</span></div>
      <div class="sum-item up"><span class="sum-num">{{ summary.up }}</span><span class="sum-lbl">{{ t('mon_sum_up') }}</span></div>
      <div class="sum-item down"><span class="sum-num">{{ summary.down }}</span><span class="sum-lbl">{{ t('mon_sum_down') }}</span></div>
      <div class="sum-item"><span class="sum-num">{{ summary.avgLatency }}<i>ms</i></span><span class="sum-lbl">{{ t('mon_sum_latency') }}</span></div>
    </div>

    <!-- 列表 -->
    <div class="mon-list">
      <div v-if="!loading && monitors.length === 0" class="mon-empty">
        <Radio :size="30" class="empty-icon" />
        <p>{{ t('mon_empty') }}</p>
        <p class="empty-hint">{{ t('mon_empty_hint') }}</p>
      </div>

      <div v-for="m in monitors" :key="m.id" :class="['mon-item', { disabled: !m.enabled }]">
        <div class="item-head" @click="toggleExpand(m)">
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
              <span class="meta-up">{{ t('mon_uptime') }} {{ uptimeText(m.id) }}</span>
              <span class="meta-lat" v-if="m.lastLatencyMs > 0">{{ m.lastLatencyMs }}ms</span>
              <span class="meta-checked">{{ t('mon_checked') }} {{ relTime(m.lastCheckedTs) }}</span>
              <span v-if="certText(m)" :class="['meta-cert', { warn: certWarn(m) }]">
                <component :is="certWarn(m) ? XCircle : CheckCircle2" :size="11" /> {{ certText(m) }}
              </span>
            </div>
          </div>
          <div class="item-actions" @click.stop>
            <button class="switch" :class="{ on: m.enabled }" @click="toggleEnabled(m)"
                    :title="m.enabled ? t('mon_enabled') : t('mon_disabled')">
              <span class="knob"></span>
            </button>
            <button class="act" :disabled="checkingId === m.id" @click="checkNow(m)" :title="t('mon_check_now')">
              <RefreshCw :size="14" :class="{ spin: checkingId === m.id }" />
            </button>
            <button class="act" @click="openEdit(m)" :title="t('mon_edit')"><Pencil :size="14" /></button>
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
                  :class="['range-tab', { active: logRange === r.value }]"
                  @click="logRange = r.value; onRangeChange(m.id)"
                >{{ r.label }}</button>
              </div>
              <button class="clear-logs" @click="clearLogs(m)">{{ t('mon_clear_logs') }}</button>
            </div>
          </div>
          <div v-if="!logsMap[m.id] || logsMap[m.id].length === 0" class="logs-empty">{{ t('mon_no_logs') }}</div>
          <canvas v-else :id="chartId(m.id)" class="chart-canvas"
            @mousemove="onChartMove($event, m.id)" @mouseleave="onChartLeave"></canvas>
        </div>
      </div>
    </div>

    <!-- 曲线图 tooltip -->
    <div v-if="tooltip.show && tooltip.data" class="chart-tooltip" :style="{ left: tooltip.x + 12 + 'px', top: tooltip.y - 10 + 'px' }">
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

        <div class="modal-grid">
          <div>
            <label>{{ t('mon_interval') }}{{ t('sched_unit_sec') }}</label>
            <input v-model.number="fIntervalVal" type="number" min="5" class="modal-input" />
          </div>
          <div>
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

        <div class="modal-actions">
          <button class="btn-ghost" @click="showModal = false">{{ t('cancel') }}</button>
          <button class="btn-primary" :disabled="saving" @click="save">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :visible="showDelConfirm"
      :message="t('mon_delete') + '?'"
      @confirm="confirmDel"
      @cancel="showDelConfirm = false"
    />
  </div>
</template>

<style scoped>
.mon-page {
  display: flex; flex-direction: column; height: 100%;
  padding: var(--space-6) var(--space-8); overflow: hidden;
}
.mon-header {
  display: flex; align-items: flex-end; justify-content: space-between;
  margin-bottom: var(--space-5); flex-shrink: 0;
}
.mon-title-wrap { display: flex; align-items: baseline; gap: var(--space-3); }
.mon-title { font-size: 18px; font-weight: 600; color: var(--color-text-primary); margin: 0; }
.mon-sub { font-size: 12px; color: var(--color-text-disabled); }
.add-btn {
  display: inline-flex; align-items: center; gap: 5px; padding: 7px 14px;
  background: var(--color-accent); color: #fff; border: none; border-radius: var(--radius-md);
  font-size: 13px; cursor: pointer; font-family: inherit; transition: background var(--transition-fast);
}
.add-btn:hover { background: var(--color-accent-hover); }
.mon-header-actions { display: flex; align-items: center; gap: var(--space-2); }
.refresh-control {
  display: inline-flex; align-items: center; gap: 5px; padding: 0 10px;
  height: 32px; background: transparent; color: var(--color-text-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  font-size: 13px; cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.refresh-control:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.refresh-select {
  border: none; background: transparent; color: inherit; font-size: 12px;
  font-family: inherit; outline: none; cursor: pointer; padding: 0;
  max-width: 64px;
}
.refresh-select option { background: var(--color-surface); color: var(--color-text-primary); }

.mon-error {
  margin-bottom: var(--space-4); padding: 8px 12px; font-size: 12px;
  color: var(--color-danger); background: rgba(232, 76, 76, 0.1);
  border: 1px solid rgba(232, 76, 76, 0.3); border-radius: var(--radius-md); flex-shrink: 0;
}

.mon-summary {
  display: flex; gap: var(--space-3); margin-bottom: var(--space-4); flex-shrink: 0;
}
.sum-item {
  flex: 1; display: flex; flex-direction: column; gap: 2px; padding: 10px 14px;
  background: var(--color-bg-secondary); box-shadow: inset 0 0 0 1px var(--color-border);
  border-radius: var(--radius-md);
}
.sum-num { font-size: 18px; font-weight: 600; color: var(--color-text-primary); }
.sum-num i { font-size: 11px; font-style: normal; color: var(--color-text-disabled); margin-left: 2px; }
.sum-lbl { font-size: 11px; color: var(--color-text-disabled); }
.sum-item.up .sum-num { color: #46b17b; }
.sum-item.down .sum-num { color: var(--color-danger); }

.mon-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.mon-empty { text-align: center; padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.empty-icon { opacity: 0.4; margin-bottom: var(--space-2); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); margin-top: var(--space-2); }

.mon-item {
  background: var(--color-bg-secondary); box-shadow: inset 0 0 0 1px var(--color-border);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
.mon-item:hover { background: var(--color-bg-hover); }
.mon-item.disabled { opacity: 0.55; }

.item-head { display: flex; align-items: center; gap: var(--space-3); padding: 12px 14px; cursor: pointer; }

.status-dot { flex-shrink: 0; width: 10px; height: 10px; border-radius: 50%; background: var(--color-text-disabled); }
.status-dot.up { background: #46b17b; box-shadow: 0 0 6px rgba(70, 177, 123, 0.6); }
.status-dot.down { background: var(--color-danger); box-shadow: 0 0 6px rgba(232, 76, 76, 0.6); }
.status-dot.unknown { background: var(--color-text-disabled); }

.item-main { flex: 1; min-width: 0; }
.item-line1 { display: flex; align-items: center; gap: 8px; }
.item-name { font-size: 13px; font-weight: 500; color: var(--color-text-primary); }
.status-badge { font-size: 10px; padding: 1px 7px; border-radius: 8px; }
.status-badge.up { background: rgba(70, 177, 123, 0.15); color: #46b17b; }
.status-badge.down { background: rgba(232, 76, 76, 0.15); color: var(--color-danger); }
.item-url { font-size: 12px; color: var(--color-text-secondary); margin-top: 2px; display: flex; align-items: center; gap: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }
.item-meta { font-size: 11px; color: var(--color-text-disabled); margin-top: 4px; display: flex; gap: 10px; flex-wrap: wrap; }
.meta-up { color: var(--color-accent); }
.meta-lat { color: var(--color-text-muted); }
.meta-cert { display: inline-flex; align-items: center; gap: 3px; color: var(--color-text-muted); }
.meta-cert.warn { color: #E2A04A; font-weight: 500; }

.item-actions { display: flex; align-items: center; gap: 3px; flex-shrink: 0; }
.switch {
  width: 34px; height: 19px; border-radius: 10px; border: none; cursor: pointer;
  background: var(--color-bg-tertiary); box-shadow: inset 0 0 0 1px var(--color-border);
  position: relative; padding: 0; margin-right: 4px; transition: background var(--transition-fast);
}
.switch.on { background: var(--color-accent); box-shadow: none; }
.knob { position: absolute; top: 2px; left: 2px; width: 15px; height: 15px; border-radius: 50%; background: #fff; transition: transform var(--transition-fast); }
.switch.on .knob { transform: translateX(15px); }
.act {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled);
  border-radius: var(--radius-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.act:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-active); }
.act:disabled { opacity: 0.4; cursor: default; }
.act.danger:hover { color: var(--color-danger); background: rgba(232, 76, 76, 0.1); }
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
  color: var(--color-text-muted); border-radius: 4px; cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.range-tab:hover { color: var(--color-text-primary); }
.range-tab.active { background: var(--color-accent); color: #fff; }
.clear-logs { font-size: 11px; color: var(--color-text-disabled); background: transparent; border: none; cursor: pointer; font-family: inherit; }
.clear-logs:hover { color: var(--color-danger); }
.logs-empty { font-size: 11px; color: var(--color-text-disabled); padding: 6px 0; }

/* 曲线图 canvas */
.chart-canvas { display: block; width: 100%; height: 200px; border-radius: var(--radius-md); }

/* 曲线图 tooltip */
.chart-tooltip {
  position: fixed; z-index: 999; pointer-events: none;
  background: rgba(20, 20, 35, 0.92); border: 1px solid rgba(74, 158, 255, 0.3);
  border-radius: var(--radius-md); padding: 6px 10px; font-size: 11px;
  box-shadow: 0 4px 16px rgba(0,0,0,0.4); backdrop-filter: blur(4px);
  min-width: 100px;
}
.tip-line { display: flex; align-items: center; gap: 6px; margin: 1px 0; }
.tip-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.tip-dot.up { background: #46b17b; }
.tip-dot.down { background: #e84c4c; }
.tip-status { color: var(--color-text-primary); font-weight: 500; }
.tip-code { color: var(--color-text-muted); font-weight: 600; }
.tip-time { color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }
.tip-lat { color: #4a9eff; }
.tip-err { color: var(--color-danger); white-space: nowrap; max-width: 220px; overflow: hidden; text-overflow: ellipsis; }


/* 弹窗 */
.modal-mask {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5); display: flex;
  align-items: center; justify-content: center; z-index: 100;
}
.modal {
  width: 440px; max-height: 90vh; overflow-y: auto; background: var(--color-surface);
  border-radius: var(--radius-lg); padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.modal h3 { margin: 0 0 var(--space-3); font-size: 15px; color: var(--color-text-primary); }
.modal label { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--color-text-muted); margin: var(--space-3) 0 var(--space-1); }
.modal-input {
  width: 100%; box-sizing: border-box; height: 34px; padding: 0 10px; background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  color: var(--color-text-primary); font-size: 13px; font-family: inherit; outline: none;
}
.modal-input:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }

.interval-row { display: flex; gap: var(--space-2); align-items: center; }
.interval-num { flex: 1; min-width: 0; }
.unit-suffix { font-size: 12px; color: var(--color-text-disabled); width: 90px; flex-shrink: 0; text-align: center; }

.toggle-label { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--color-text-secondary); cursor: pointer; margin: var(--space-2) 0 0; }
.toggle-label input { cursor: pointer; }
.toggle-row { display: flex; gap: var(--space-5); margin-top: var(--space-3); flex-wrap: wrap; }

.modal-actions { display: flex; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-5); }
.btn-ghost { padding: 6px 14px; border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-ghost:hover { background: var(--color-bg-hover); }
.btn-primary { padding: 6px 14px; border: none; background: var(--color-accent); color: #fff; border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-primary:hover:not(:disabled) { background: var(--color-accent-hover); }
.btn-primary:disabled { opacity: 0.5; cursor: default; }
</style>
