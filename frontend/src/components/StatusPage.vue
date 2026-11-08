<script setup lang="ts">
/**
 * 「网站监控」里的第三个视图（状态页）：本机只读的监控汇总，对标 Uptime Kuma 的展示形态。
 * 不单独占侧栏导航 —— 配置监控与查看整体状态属于同一件事。
 *
 * 为什么不做公开 HTTP 端点：这是本机单窗口应用，开端口就要引入鉴权、进程/窗口
 * 生命周期、端口占用与防火墙问题，远超「偶尔发给别人看」的需求。改用
 * 「只读汇总页 + 单文件静态 HTML 快照」：快照内联样式、无脚本、无外链，
 * 双击即可在任意浏览器打开，天然不含令牌/密钥，也不新开端口。
 *
 * 数据源与监控页同源，按并行契约不新增任何后端方法：
 *   ListMonitors() / ListMonitorStats() / GetMonitorLogsSince(id, sinceTs, limit)
 */
import { ref, computed, onMounted, onActivated, onDeactivated, onUnmounted, watch } from 'vue'

// embedded 时父页已提供标题与汇总条，这里只保留内容区的一行工具。
const props = withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false })
// 状态页是 status 视图里唯一的数据源；把本轮结果交给父页，
// 汇总条就不必为了四个数字再单开一条 IPC（本机轮询有成本）。
const emit = defineEmits<{
  (e: 'data', monitors: Monitor[], stats: Record<string, MonitorStat>): void
}>()
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import {
  ListMonitors, ListMonitorStats, GetMonitorLogsSince,
} from '../../bindings/quickdock/services/monitor/monitorservice'
import type { Monitor } from '../../bindings/quickdock/internal/db/models'

const { t } = useI18n()

// 这两个结构只在 ApiResult.data 里出现，Wails 绑定生成器不会单独出类型，故本地声明
//（与 MonitorPage.vue 同形；p95/p99 是后端本轮新增的两个字段）。
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

// 刷新固定 60 秒：一轮要发 1（监控）+1（统计）+N（每条日志）次 IPC，
// 这是本机进程内调用，不是浏览器轮询 CDN；20s/10s 的 web 节奏在这里纯属浪费。
// 默认开、可整体关掉；页面不可见 / 被 KeepAlive 挂起时暂停。
const REFRESH_MS = 60_000
const LOG_LIMIT = 30
const LOG_WINDOW_SEC = 86_400 // 与 24h 在线率同口径：只画最近一天里的检测

const monitors = ref<Monitor[]>([])
const stats = ref<Record<string, MonitorStat>>({})
const logsMap = ref<Record<string, MonitorLog[]>>({})
const loading = ref(false)
const error = ref('')
const loadedOnce = ref(false) // 首屏是否成功加载过（用于区分加载态与「刷新中保留上一屏」）
const lastUpdatedAt = ref<number>(0)
const autoRefresh = ref(true)
const pageActive = ref(false)

let timer: ReturnType<typeof setInterval> | null = null
let loadSeq = 0 // 并发保护：慢的旧请求回来不能盖掉新请求的结果

// ── 展示辅助 ───────────────────────────────────────────────────────
// 一律给「无数据」一个 '—'：0 与「没统计」在延迟/分位数上完全是两回事，
// 显示成 0ms 会让人误以为服务快到没有延迟。

/** 目标显示：http 读 url，其余（含 dns/ping）读 target —— 与 MonitorPage 同口径。 */
function targetText(m: Monitor): string {
  return m.kind === 'http' ? (m.url || '') : (m.target || '')
}
/** 探测类型是后端枚举：dns/ping 用 camelCase 新键，其余沿用历史 mon_kind_<k>。 */
function kindText(m: Monitor): string {
  const k = m.kind || 'http'
  if (k === 'dns') return t('monitorKindDns')
  if (k === 'ping') return t('monitorKindPing')
  const key = 'mon_kind_' + k
  const label = t(key)
  return label === key ? k : label
}
function uptimeText(s?: MonitorStat): string {
  if (!s || !s.totalChecks) return '—'
  return s.uptimeRatio.toFixed(1) + '%'
}
function msText(v?: number): string {
  return v && v > 0 ? Math.round(v) + 'ms' : '—'
}
function downText(v?: string): string {
  return v ? v.slice(5, 16) : '—' // YYYY-MM-DD HH:MM:SS → MM-DD HH:MM
}
function statusClass(m: Monitor): string {
  if (!m.enabled) return 'disabled'
  if (m.lastStatus === 'up') return 'up'
  if (m.lastStatus === 'down') return 'down'
  return 'unknown'
}
// 状态词复用监控页已有的键，不重复造一份同义文案。
function statusLabel(m: Monitor): string {
  const c = statusClass(m)
  if (c === 'up') return t('mon_up')
  if (c === 'down') return t('mon_down')
  if (c === 'disabled') return t('mon_disabled')
  return t('mon_never')
}
function cellTitle(l: MonitorLog): string {
  const st = l.status === 'up' ? t('mon_up') : (l.status === 'down' ? t('mon_down') : l.status)
  return `${l.checkedAt} · ${st} · ${l.latencyMs}ms`
}
function fmtUpdated(ts: number): string {
  if (!ts) return '—'
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
function stamp(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
}

// ── 加载 ───────────────────────────────────────────────────────────
/** 单条日志独立请求、独立失败：某条监控刚被删或库忙，不该让整页变错误态。 */
async function loadOneLogs(id: string, since: number): Promise<MonitorLog[]> {
  try {
    const raw = unwrap<MonitorLog[]>(await GetMonitorLogsSince(id, since, LOG_LIMIT))
    const list: MonitorLog[] = Array.isArray(raw) ? [...raw] : []
    // 后端返回顺序不保证，统一按时间升序后取最近 N 条，迷你格左旧右新。
    return list.sort((a, b) => (a.checkedTs || 0) - (b.checkedTs || 0)).slice(-LOG_LIMIT)
  } catch {
    return []
  }
}

async function load() {
  // 60s 定时器与「切回本页」可能撞在一起；已在跑就别再叠一轮 IPC。
  if (loading.value) return
  const seq = ++loadSeq
  loading.value = true
  try {
    // 监控与统计无依赖，并行；日志按每条单独发，各自静默降级。
    const [mons, st] = await Promise.all([ListMonitors(), ListMonitorStats()])
    const monRaw = unwrap<Monitor[]>(mons)
    const monList = Array.isArray(monRaw) ? monRaw : []
    const stRaw = unwrap<MonitorStat[]>(st)
    const stArr = Array.isArray(stRaw) ? stRaw : []
    if (seq !== loadSeq) return
    const statMap: Record<string, MonitorStat> = {}
    for (const s of stArr) if (s && s.monitorId) statMap[s.monitorId] = s
    const since = Math.floor(Date.now() / 1000) - LOG_WINDOW_SEC
    const entries = await Promise.all(
      monList.map(async (m) => [m.id, await loadOneLogs(m.id, since)] as const),
    )
    if (seq !== loadSeq) return
    const lm: Record<string, MonitorLog[]> = {}
    for (const [id, rows] of entries) lm[id] = rows
    monitors.value = monList
    stats.value = statMap
    logsMap.value = lm
    lastUpdatedAt.value = Date.now()
    loadedOnce.value = true
    error.value = ''
    emit('data', monList, statMap)
  } catch (e) {
    // 只在首屏把它当错误；刷新失败保留上一屏数据，不把已看到的汇总抹掉。
    if (!loadedOnce.value) error.value = getErrorMessage(e)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

// ── 派生数据 ───────────────────────────────────────────────────────
interface Overall {
  labelKey: string
  total: number
  online: number
  uptime: number
  uptimeSample: boolean
}
const overall = computed<Overall>(() => {
  const list = monitors.value
  const enabled = list.filter((m) => m.enabled)
  const online = enabled.filter((m) => m.lastStatus === 'up').length
  const down = enabled.filter((m) => m.lastStatus === 'down').length
  let labelKey = 'statusAllDown'
  if (online > 0 && down === 0) labelKey = 'statusAllUp'
  else if (online > 0 && down > 0) labelKey = 'statusPartial'
  // 整体 24h 在线率用「成功次数 / 总次数」聚合，而不是把各监控的百分比再平均：
  // 10 次里挂 1 次与 10000 次里挂 10 次不该得到一样的权重。
  let totalChecks = 0, upChecks = 0
  for (const s of Object.values(stats.value)) {
    totalChecks += s.totalChecks || 0
    upChecks += s.upChecks || 0
  }
  const uptimeSample = totalChecks > 0
  return {
    labelKey,
    total: list.length,
    online,
    uptime: uptimeSample ? (upChecks / totalChecks) * 100 : 0,
    uptimeSample,
  }
})

interface StatusCard { monitor: Monitor; stat?: MonitorStat; logs: MonitorLog[] }
interface StatusGroup { name: string; isNone: boolean; cards: StatusCard[] }
function toCard(m: Monitor): StatusCard {
  return { monitor: m, stat: stats.value[m.id], logs: logsMap.value[m.id] ?? [] }
}
const groups = computed<StatusGroup[]>(() => {
  const byName = new Map<string, Monitor[]>()
  const none: Monitor[] = []
  const list = [...monitors.value].sort((a, b) => (a.sort || 0) - (b.sort || 0))
  for (const m of list) {
    const g = (m.groupName || '').trim()
    if (!g) { none.push(m); continue }
    if (!byName.has(g)) byName.set(g, [])
    byName.get(g)!.push(m)
  }
  const out: StatusGroup[] = []
  if (none.length) out.push({ name: t('statusGroupNone'), isNone: true, cards: none.map(toCard) })
  for (const name of Array.from(byName.keys()).sort((a, b) => a.localeCompare(b))) {
    out.push({ name, isNone: false, cards: (byName.get(name) ?? []).map(toCard) })
  }
  return out
})

// 四个状态互斥且覆盖全部情况，模板用独立 v-if 而不是 v-else 链：
// 不靠「else 分支一定非空」的隐式推断，避免联合状态下渲染出两套内容。
const showInitialLoading = computed(() => !loadedOnce.value && !error.value)
const showError = computed(() => !loadedOnce.value && !!error.value)
const showEmpty = computed(() => loadedOnce.value && monitors.value.length === 0)
const showContent = computed(() => monitors.value.length > 0)

// ── 刷新策略 ───────────────────────────────────────────────────────
function shouldRun(): boolean {
  return autoRefresh.value && pageActive.value && document.visibilityState === 'visible'
}
function syncTimer() {
  if (shouldRun() && timer === null) {
    timer = setInterval(() => { void load() }, REFRESH_MS)
  } else if (!shouldRun() && timer !== null) {
    clearInterval(timer)
    timer = null
  }
}
function onVisibilityChange() { syncTimer() }

watch(autoRefresh, syncTimer)

onMounted(() => {
  pageActive.value = true
  document.addEventListener('visibilitychange', onVisibilityChange)
  void load()
  syncTimer()
})
// KeepAlive：切走停轮询（组件没卸载，定时器还会跑），切回立即补一次数据 ——
// 页面可能已挂起很久，等到下一个 60s 刻度才刷新会显示陈旧状态。
onActivated(() => {
  // 首次挂载时 onMounted 也会调 load()，上面的 guard 会挡掉重复请求（不依赖两个钩子
  // 的先后）；这里真正的用途是「从别的页切回来」立刻补一次数据，不等下一个 60s 刻度。
  pageActive.value = true
  void load()
  syncTimer()
})
onDeactivated(() => {
  pageActive.value = false
  syncTimer()
})
onUnmounted(() => {
  document.removeEventListener('visibilitychange', onVisibilityChange)
  if (timer !== null) { clearInterval(timer); timer = null }
})

// ── 导出 HTML 快照 ─────────────────────────────────────────────────
/** 名称/目标都是用户可控文本，不转义会撑破标签甚至注入内容。 */
function esc(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}
function metricHtml(label: string, value: string): string {
  return `<div class="m"><span>${esc(label)}</span><b>${esc(value)}</b></div>`
}

/**
 * 单文件静态 HTML：内联样式、无外链、无 <script>。
 * 脱敏边界：只渲染 monitors / stats / logsMap（监控目标与运行指标），
 * 从不读取设置页的 webhook、令牌或扩展渠道配置 —— 那些数据本页压根没有，
 * 快照里也就不会出现任何凭证。所有可见文字都经 t() 取当前语言。
 */
function buildSnapshotHtml(): string {
  const o = overall.value
  const banner = `<div class="banner ${esc(o.labelKey)}">
    <div class="bhead"><span class="bdot"></span>${esc(t(o.labelKey))}</div>
    <div class="bmeta">
      <span><b>${o.total}</b> ${esc(t('statusTotal'))}</span>
      <span><b>${o.online}</b> ${esc(t('statusOnline'))}</span>
      <span><b>${esc(o.uptimeSample ? o.uptime.toFixed(1) + '%' : '—')}</b> ${esc(t('statusOverallUptime'))}</span>
      <span>${esc(t('statusLastUpdated'))} ${esc(fmtUpdated(lastUpdatedAt.value))}</span>
    </div>
  </div>`
  const sections = groups.value.map((g) => {
    const cards = g.cards.map((c) => {
      const m = c.monitor
      const s = c.stat
      const cells = c.logs.length
        ? c.logs.map((l) => {
            const cls = l.status === 'up' ? 'up' : (l.status === 'down' ? 'down' : 'na')
            return `<div class="cell ${cls}" title="${esc(cellTitle(l))}"></div>`
          }).join('')
        : `<span class="empty">${esc(t('statusNoLogs'))}</span>`
      return `<article class="card">
        <div class="chead"><span class="dot ${esc(statusClass(m))}"></span><span class="nm">${esc(m.name)}</span><span class="st">${esc(statusLabel(m))}</span></div>
        <div class="ctarget">${esc(kindText(m))} · ${esc(targetText(m) || '—')}</div>
        <div class="metrics">
          ${metricHtml(t('statusUptime24h'), uptimeText(s))}
          ${metricHtml(t('statusAvgLatency'), msText(s?.avgLatencyMs))}
          ${metricHtml(t('statusP95'), msText(s?.p95LatencyMs))}
          ${metricHtml(t('statusP99'), msText(s?.p99LatencyMs))}
          ${metricHtml(t('statusLastDown'), downText(s?.lastDownAt))}
        </div>
        <div class="cells">${cells}</div>
      </article>`
    }).join('')
    return `<section><h2>${esc(g.name)}</h2><div class="grid">${cards}</div></section>`
  }).join('')

  // 快照是独立文档，离开应用就没有 CSS 变量了，故色值、圆角一律写具体值。
  // 别在这里用 var(--radius-*)：独立文档里变量未定义，border-radius 会静默
  // 退化成 0，导出的卡片和横幅全是直角，和界面里看到的完全不是一回事。
  return `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>${esc(t('statusTitle'))}</title>
<style>
  body{margin:0;padding:24px;background:#f6f7f9;color:#1a1f27;font-family:-apple-system,Segoe UI,Arial,sans-serif}
  h1{font-size:18px;margin:0 0 4px}
  .sub{font-size:12px;color:#6e7683;margin:0 0 20px}
  .banner{border-radius:12px;padding:18px 20px;margin-bottom:24px;background:#e8f7ee;border:1px solid #bfe6cd}
  .banner.statusPartial{background:#fff6e8;border-color:#f3d9a8}
  .banner.statusAllDown{background:#fdeaea;border-color:#f3c0c0}
  .bhead{display:flex;align-items:center;gap:10px;font-size:18px;font-weight:600}
  .bdot{width:12px;height:12px;border-radius:50%;background:#22c55e}
  .banner.statusPartial .bdot{background:#f59e0b}
  .banner.statusAllDown .bdot{background:#ef4444}
  .bmeta{display:flex;flex-wrap:wrap;gap:20px;margin-top:14px;font-size:13px;color:#525b68}
  .bmeta b{font-size: 15px;color:#1a1f27;margin-right:4px}
  h2{font-size:14px;margin:22px 0 10px;color:#525b68}
  .grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px}
  .card{background:#fff;border:1px solid #dde1e8;border-radius:8px;padding:14px}
  .chead{display:flex;align-items:center;gap:8px}
  .nm{font-weight:600;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .st{font-size:12px;color:#6e7683}
  .dot{width:9px;height:9px;border-radius:50%;background:#9aa2ae;flex-shrink:0}
  .dot.up{background:#22c55e}.dot.down{background:#ef4444}.dot.unknown{background:#f59e0b}
  .ctarget{font-size:12px;color:#6e7683;margin:6px 0 12px;word-break:break-all}
  .metrics{display:grid;grid-template-columns:repeat(2,1fr);gap:8px 12px}
  .m{display:flex;justify-content:space-between;font-size:12px}
  .m span{color:#6e7683}.m b{font-weight:600}
  .cells{display:flex;gap:3px;margin-top:14px;flex-wrap:wrap}
  .cell{width:10px;height:16px;border-radius:2px;background:#c9cfd9}
  .cell.up{background:#22c55e}.cell.down{background:#ef4444}
  .empty{font-size:12px;color:#9aa2ae}
  footer{margin-top:28px;font-size:11px;color:#9aa2ae}
</style></head>
<body>
<h1>${esc(t('statusTitle'))}</h1>
<p class="sub">${esc(t('statusSubtitle'))}</p>
${banner}
${sections}
<footer>${esc(t('statusExportNote'))}</footer>
</body></html>`
}

function exportSnapshot() {
  const blob = new Blob([buildSnapshotHtml()], { type: 'text/html;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'quickdock-status-' + stamp(new Date()) + '.html'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // WebView2 里 a.click() 的下载是异步取流的，立即 revoke 会提前释放 Blob；留一拍。
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
</script>

<template>
  <div :class="['st-page', { embedded: props.embedded }]">
    <!-- 嵌入时父页已有标题与汇总条，这里降级成内容区的一行工具；
         再摆一层 qd-page-header 看着像第二个页头，会把上方统计顶掉。 -->
    <div :class="props.embedded ? 'st-toolbar' : 'qd-page-header'">
      <div v-if="!props.embedded" class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('statusTitle') }}</h2>
        <span class="qd-page-sub">{{ t('statusSubtitle') }}</span>
      </div>
      <div :class="props.embedded ? 'st-toolbar-actions' : 'qd-page-actions'">
        <label class="st-refresh">
          <input v-model="autoRefresh" type="checkbox" />
          <span>{{ t('statusAutoRefresh') }}</span>
        </label>
        <button class="qd-btn" :disabled="!monitors.length" @click="exportSnapshot">
          {{ t('statusExportHtml') }}
        </button>
      </div>
    </div>

    <p class="st-note">{{ t('statusExportNote') }}</p>

    <div v-if="showInitialLoading" class="st-state">{{ t('statusLoading') }}</div>

    <div v-if="showError" class="st-state st-state-error">
      <span>{{ t('statusError') }} · {{ error }}</span>
      <button class="qd-btn" @click="load()">{{ t('statusRetry') }}</button>
    </div>

    <div v-if="showEmpty" class="st-state">
      <span>{{ t('statusEmpty') }}</span>
      <p class="st-state-hint">{{ t('statusEmptyHint') }}</p>
    </div>

    <!-- 只读：整页没有任何新建 / 编辑 / 删除 / 启停入口。 -->
    <template v-if="showContent">
      <div :class="['st-banner', overall.labelKey]">
        <div class="st-banner-head">
          <span class="st-banner-dot"></span>
          <span class="st-banner-text">{{ t(overall.labelKey) }}</span>
        </div>
        <div class="st-banner-metrics">
          <span><b>{{ overall.total }}</b> {{ t('statusTotal') }}</span>
          <span><b>{{ overall.online }}</b> {{ t('statusOnline') }}</span>
          <span><b>{{ overall.uptimeSample ? overall.uptime.toFixed(1) + '%' : '—' }}</b> {{ t('statusOverallUptime') }}</span>
          <span class="st-updated">{{ t('statusLastUpdated') }} {{ fmtUpdated(lastUpdatedAt) }}</span>
        </div>
      </div>

      <div v-for="g in groups" :key="g.isNone ? '__none__' : g.name" class="st-group">
        <h3 class="st-group-title">{{ g.name }}</h3>
        <div class="st-grid">
          <div v-for="c in g.cards" :key="c.monitor.id" class="st-card">
            <div class="st-card-head">
              <span :class="['st-dot', statusClass(c.monitor)]"></span>
              <span class="st-card-name" :title="c.monitor.name">{{ c.monitor.name }}</span>
              <span class="st-card-status">{{ statusLabel(c.monitor) }}</span>
            </div>
            <div class="st-card-target" :title="targetText(c.monitor)">
              <span class="st-kind">{{ kindText(c.monitor) }}</span> {{ targetText(c.monitor) || '—' }}
            </div>
            <div class="st-metrics">
              <div class="st-metric"><span class="st-metric-lbl">{{ t('statusUptime24h') }}</span><span class="st-metric-val">{{ uptimeText(c.stat) }}</span></div>
              <div class="st-metric"><span class="st-metric-lbl">{{ t('statusAvgLatency') }}</span><span class="st-metric-val">{{ msText(c.stat?.avgLatencyMs) }}</span></div>
              <div class="st-metric"><span class="st-metric-lbl" :title="t('monitorPercentileTip')">{{ t('statusP95') }}</span><span class="st-metric-val">{{ msText(c.stat?.p95LatencyMs) }}</span></div>
              <div class="st-metric"><span class="st-metric-lbl" :title="t('monitorPercentileTip')">{{ t('statusP99') }}</span><span class="st-metric-val">{{ msText(c.stat?.p99LatencyMs) }}</span></div>
              <div class="st-metric"><span class="st-metric-lbl">{{ t('statusLastDown') }}</span><span class="st-metric-val">{{ downText(c.stat?.lastDownAt) }}</span></div>
            </div>
            <div class="st-cells">
              <div
                v-for="(l, i) in c.logs"
                :key="l.id || i"
                :class="['st-cell', l.status === 'up' ? 'up' : (l.status === 'down' ? 'down' : 'na')]"
                :title="cellTitle(l)"
              ></div>
              <span v-if="!c.logs.length" class="st-cells-empty">{{ t('statusNoLogs') }}</span>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.st-page { display: flex; flex-direction: column; gap: var(--space-5); padding: var(--space-6); }
/* 嵌进监控页后由父页提供标题与外边距，这里只留工具条，避免两层页头叠在一起。 */
.st-page.embedded { padding: 0; gap: var(--space-4); }
.st-toolbar { display: flex; align-items: center; justify-content: flex-end; flex-shrink: 0; }
.st-toolbar-actions { display: inline-flex; align-items: center; gap: var(--space-3); }
.st-refresh { display: inline-flex; align-items: center; gap: var(--space-2); font-size: 12px; color: var(--color-text-secondary); cursor: pointer; }
.st-note { margin: 0; font-size: 11px; line-height: 1.6; color: var(--color-text-disabled); }

.st-state {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: var(--space-3); min-height: 160px; font-size: 13px; color: var(--color-text-muted);
}
.st-state-error { color: var(--color-danger); }
.st-state-hint { margin: 0; font-size: 12px; color: var(--color-text-disabled); }

.st-banner {
  padding: var(--space-6); border-radius: var(--radius-lg);
  border: 1px solid var(--color-success-border); background: var(--color-success-bg);
}
.st-banner.statusPartial { border-color: var(--color-warning-border); background: var(--color-warning-bg); }
.st-banner.statusAllDown { border-color: var(--color-danger-border); background: var(--color-danger-bg); }
.st-banner-head { display: flex; align-items: center; gap: var(--space-3); font-size: 18px; font-weight: 600; color: var(--color-text-primary); }
.st-banner-dot {
  width: 12px; height: 12px; border-radius: var(--radius-full);
  background: var(--color-success); flex-shrink: 0;
}
.st-banner.statusPartial .st-banner-dot { background: var(--color-warning); }
.st-banner.statusAllDown .st-banner-dot { background: var(--color-danger); }
.st-banner-metrics {
  display: flex; flex-wrap: wrap; gap: var(--space-7); margin-top: var(--space-4);
  font-size: 12px; color: var(--color-text-secondary);
}
.st-banner-metrics b { font-size: 15px; color: var(--color-text-primary); margin-right: var(--space-2); }
.st-updated { color: var(--color-text-muted); }

.st-group-title { margin: var(--space-2) 0 var(--space-3); font-size: 13px; font-weight: 600; color: var(--color-text-secondary); }
.st-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: var(--space-4); }
.st-card {
  padding: var(--space-4); border-radius: var(--radius-md);
  border: 1px solid var(--card-border-color); background: var(--color-card-bg);
}
.st-card-head { display: flex; align-items: center; gap: var(--space-2); }
.st-dot { width: 9px; height: 9px; border-radius: var(--radius-full); background: var(--color-text-disabled); flex-shrink: 0; }
.st-dot.up { background: var(--color-success); }
.st-dot.down { background: var(--color-danger); }
.st-dot.unknown { background: var(--color-warning); }
.st-card-name {
  flex: 1; min-width: 0; font-size: 13px; font-weight: 600; color: var(--color-text-primary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.st-card-status { font-size: 11px; color: var(--color-text-muted); flex-shrink: 0; }
.st-card-target {
  margin-top: var(--space-2); font-size: 11px; color: var(--color-text-secondary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.st-kind {
  display: inline-block; padding: 0 var(--space-2); border-radius: var(--radius-xs);
  background: var(--color-accent-bg); color: var(--color-accent); font-size: 10px;
}
.st-metrics {
  display: grid; grid-template-columns: repeat(2, 1fr); gap: var(--space-2) var(--space-4);
  margin-top: var(--space-4); padding-top: var(--space-3); border-top: 1px solid var(--color-border);
}
.st-metric { display: flex; justify-content: space-between; gap: var(--space-2); font-size: 11px; }
.st-metric-lbl { color: var(--color-text-muted); }
.st-metric-val { color: var(--color-text-primary); font-weight: 600; }
.st-cells { display: flex; flex-wrap: wrap; gap: 3px; margin-top: var(--space-4); }
.st-cell { width: 10px; height: 16px; border-radius: var(--radius-xs); background: var(--color-border-light); }
.st-cell.up { background: var(--color-success); }
.st-cell.down { background: var(--color-danger); }
.st-cells-empty { font-size: 11px; color: var(--color-text-disabled); }
</style>
