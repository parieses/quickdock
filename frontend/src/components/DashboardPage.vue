<script setup lang="ts">
/**
 * 总览仪表盘：一屏看清「今天要做什么 / 服务有没有挂 / 环境在跑什么 / 端口被谁占」。
 *
 * 为什么需要这一页：首屏是工作空间启动台，它回答的是「我要打开哪个项目」；
 * 而每天开工的第一分钟真正要回答的是另外几个问题——待办有没有到期、昨晚的
 * 服务是不是还活着、哪个端口被人占了。这些信息散在 4 个页面里，逐个翻一遍才
 * 能拼出来，于是有了这块聚合页。
 *
 * 版式：页头沿用全应用的 .qd-page-header（与监控/笔记同高，切页不跳）；
 * 指标不再做成四张并排的卡片，而是一条通栏刻度带——卡片是「并列的四个模块」，
 * 刻度带是「同一条状态线上的四个读数」，后者才符合这一页「扫一眼」的用途。
 *
 * 数据全部取自已有接口（不新增后端聚合方法）：四块内容各自独立请求、独立降级——
 * 某一块失败只让那块区域显示「加载失败」，不该因为端口枚举失败把整页拖成空白。
 */
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { navigate } from '../composables/bridge'
import { useToast } from '../composables/useToast'
import { ListTodos, ToggleTodo } from '../../bindings/quickdock/services/todo/todoservice'
import { ListMonitors, ListMonitorStats, GetMonitorLogsSince } from '../../bindings/quickdock/services/monitor/monitorservice'
import { EnvList, EnvEverythingStatus } from '../../bindings/quickdock/services/env/environmentservice'
import { DSHStatus } from '../../bindings/quickdock/services/dsh/dshservice'
import { ListListeningPorts } from '../../bindings/quickdock/services/port/portservice'
import { GetMostUsedItems } from '../../bindings/quickdock/services/appservice'
import { OpenItem } from '../../bindings/quickdock/services/collection/collectionservice'
import { GitListRepos, GitRepoStatus } from '../../bindings/quickdock/services/git/gitservice'
import type { Todo, Monitor, CollectionItem } from '../../bindings/quickdock/internal/db/models'
import {
  RefreshCw, ListTodo, Activity, Server, Plug, ChevronRight, FolderOpen, Check,
  FolderGit2,
} from '@lucide/vue'

// 以下两个结构只在返回值里出现，Wails 绑定生成器不会单独生成类型（与 MonitorPage 的
// MonitorLog/MonitorStat 同一处理方式），故本地声明，字段与后端 json tag 一致。
interface RuntimeInfo {
  id: string
  name: string
  group: string
  installed: unknown[] | null
  enabled: boolean
  hasService: boolean
}
interface PortInfo {
  port: number
  protocol: string
  pid: number
  process: string
  path: string
  self: boolean
}
interface GitRepoRef { path: string; name: string; addedAt: string }

/** ListMonitorStats 的行：近 24h 每个监控的在线率汇总（字段与后端 json tag 一致）。 */
interface MonitorStat {
  monitorId: string
  totalChecks: number
  upChecks: number
  uptimeRatio: number // 0..100
  avgLatencyMs: number
  lastDownAt: string
}
/** 热力条只需要时间与结果两个字段，其余不取。 */
interface CheckPoint { checkedTs: number; status: string }

/** 常驻服务卡的一行：不仅 env 运行时，Everything / DSH 也按同一形状并进来。 */
interface ResidentRow {
  id: string
  name: string
  meta: string
  page: string // 点击后去哪一页
}
interface GitRepoRow {
  name: string
  branch: string
  ahead: number
  behind: number
  changes: number
  error: string
}

const { t, locale } = useI18n()
const toast = useToast()

const todos = ref<Todo[]>([])
const monitors = ref<Monitor[]>([])
const runtimes = ref<RuntimeInfo[]>([])
// 两个不归 env 监督器管的常驻进程：只取「跑没跑 / 端口」，其余字段这里用不上
const fileSearch = ref<{ running: boolean; enabled: boolean } | null>(null)
const dsh = ref<{ running: boolean; port: number } | null>(null)
const ports = ref<PortInfo[]>([])
const items = ref<CollectionItem[]>([])
const gitRepos = ref<GitRepoRow[]>([])
// 24h 可用率：stats 是后端聚合好的汇总，logs 用来画逐时段热力条
const monStats = ref<Record<string, MonitorStat>>({})
const monLogs = ref<Record<string, CheckPoint[]>>({})

const loading = ref(false)
const failed = ref<Record<string, boolean>>({})

const todayStr = computed(() => {
  const d = new Date()
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${mm}-${dd}`
})

const dateText = computed(() =>
  new Date().toLocaleDateString(locale.value === 'en-US' ? 'en-US' : 'zh-CN', {
    month: 'long', day: 'numeric', weekday: 'long',
  }))

const greeting = computed(() => {
  const h = new Date().getHours()
  if (h < 6) return t('dash_greet_night')
  if (h < 12) return t('dash_greet_morning')
  if (h < 18) return t('dash_greet_afternoon')
  return t('dash_greet_evening')
})

// ---- 指标 ----
const openTodos = computed(() => todos.value.filter(x => x.status !== 'done'))
const todayDue = computed(() => openTodos.value.filter(x => x.dueDate === todayStr.value).length)
const overdue = computed(() => openTodos.value.filter(x => x.dueDate && x.dueDate < todayStr.value).length)
const enabledMonitors = computed(() => monitors.value.filter(m => m.enabled))
const downMonitors = computed(() => enabledMonitors.value.filter(m => m.lastStatus === 'down'))
const residentRuntimes = computed(() => runtimes.value.filter(r => r.enabled))
const installedRuntimes = computed(() => runtimes.value.filter(r => (r.installed?.length || 0) > 0))

// ---- 24h 可用率图表（纯 CSS 热力条，不引图表库：桌面端无外网，CDN 图表库不可用）----
/** 热力格数：48 格 = 30 分钟/格，与后端 logWindowSec(24h) 对齐；mini 条共用同一份格。 */
const RIBBON_CELLS = 48
const DAY_SEC = 86400

/** 一条监控的 24h 热力格。30 分钟内挂过一次就算 down（宁可红多不漏报）。 */
function bucketsOf(id: string): ('up' | 'down' | 'none')[] {
  const logs = monLogs.value[id]
  const out: ('up' | 'down' | 'none')[] = Array(RIBBON_CELLS).fill('none')
  if (!logs?.length) return out
  const start = Math.floor(Date.now() / 1000) - DAY_SEC
  for (const l of logs) {
    const idx = Math.floor(((l.checkedTs - start) / DAY_SEC) * RIBBON_CELLS)
    if (idx < 0 || idx >= RIBBON_CELLS) continue
    if (l.status === 'down') out[idx] = 'down'
    else if (out[idx] === 'none') out[idx] = 'up'
  }
  return out
}

/** 全局条：启用中的监控一起聚合——一格红 = 这半小时里至少有一个服务挂过。 */
const globalRibbon = computed<( 'up' | 'down' | 'none' )[]>(() => {
  const ids = monitors.value.filter(m => m.enabled).map(m => m.id)
  if (!ids.length) return Array(RIBBON_CELLS).fill('none')
  const per = ids.map(bucketsOf)
  return Array.from({ length: RIBBON_CELLS }, (_, i) =>
    per.some(b => b[i] === 'down') ? 'down' : per.some(b => b[i] === 'up') ? 'up' : 'none')
})

/** 整体可用率 = 启用监控的 up 次数总和 / 检测总次数（后端已按 24h 窗口算好）。 */
const globalUptime = computed(() => {
  const ids = new Set(monitors.value.filter(m => m.enabled).map(m => m.id))
  let total = 0, up = 0
  for (const s of Object.values(monStats.value)) {
    if (!ids.has(s.monitorId)) continue
    total += s.totalChecks
    up += s.upChecks
  }
  return total ? (up / total) * 100 : -1
})

const globalUptimeText = computed(() =>
  globalUptime.value < 0 ? '—' : globalUptime.value.toFixed(1) + '%')

/** 单项可用率文案；没有检测记录显示占位。 */
function upPct(id: string): string {
  const s = monStats.value[id]
  return s && s.totalChecks ? s.uptimeRatio.toFixed(1) + '%' : '—'
}

/** 今日待办的到期分布条：三段各占的数量比例，颜色即语义（红=逾期、琥珀=今天、蓝=以后）。 */
const todoDist = computed(() => {
  const later = Math.max(0, openTodos.value.length - overdue.value - todayDue.value)
  return [
    { k: 'overdue', n: overdue.value, cls: 'danger' },
    { k: 'today', n: todayDue.value, cls: 'warn' },
    { k: 'later', n: later, cls: 'ok' },
  ].filter(x => x.n > 0)
})

/**
 * 常驻服务清单 = 环境的「常驻运行时」+ 两个自带常驻进程（Everything / DSH）。
 *
 * 为什么不只数 EnvList：Everything 与 DSH 不归 env 的监督器管——Everything 的开关是
 * 「文件搜索」总开关 + 自己的进程，DSH 是独立 web 服务（有自己的 running/端口）。
 * 只统计 env 的话，这两样明明在后台吃内存却不出现在这张卡上，排查「谁在跑」时漏人。
 */
const residentServices = computed<ResidentRow[]>(() => {
  const rows: ResidentRow[] = residentRuntimes.value.map(r => ({
    id: 'env:' + r.id,
    name: r.name,
    meta: t('dash_env_versions', { n: r.installed?.length || 0 }),
    page: 'environments',
  }))
  // 进程在跑就算常驻：文件搜索总开关关掉时它仍是常驻进程，只是不参与检索
  if (fileSearch.value?.running) {
    rows.push({ id: 'everything', name: 'Everything', meta: t('dash_svc_filesearch'), page: 'environments' })
  }
  if (dsh.value?.running) {
    rows.push({ id: 'dsh', name: 'DSH', meta: dsh.value.port ? `:${dsh.value.port}` : '', page: 'dsh' })
  }
  return rows.sort((a, b) => a.name.localeCompare(b.name))
})

/** 卡片里最多列 6 条：再多就成了一张要滚动的表，而仪表盘只负责「先看见」。 */
const residentList = computed(() => residentServices.value.slice(0, 6))

// ---- 列表 ----
/** 今日待办：逾期的最靠前，其次今天到期，再按创建顺序兜底。 */
const todoList = computed(() =>
  [...openTodos.value]
    .sort((a, b) => {
      const ad = a.dueDate || '9999-12-31'
      const bd = b.dueDate || '9999-12-31'
      return ad === bd ? 0 : ad < bd ? -1 : 1
    })
    .slice(0, 6))

/** 服务健康：红的在最前，其次是「还没检测过」，最后才是正常的。 */
const monitorList = computed(() => {
  const rank = (m: Monitor) => (m.lastStatus === 'down' ? 0 : m.lastStatus ? 2 : 1)
  return [...monitors.value].sort((a, b) => rank(a) - rank(b)).slice(0, 6)
})

const portList = computed(() =>
  [...ports.value].sort((a, b) => a.port - b.port).slice(0, 6))

/** 需要关注的仓库：有改动 / 领先落后 / 出错的排前面，干净的沉底。 */
const gitList = computed(() =>
  [...gitRepos.value]
    .sort((a, b) => {
      const wa = (a.error ? 4 : 0) + (a.changes ? 2 : 0) + (a.ahead + a.behind ? 1 : 0)
      const wb = (b.error ? 4 : 0) + (b.changes ? 2 : 0) + (b.ahead + b.behind ? 1 : 0)
      return wb - wa
    })
    .slice(0, 6))
const gitBusyCount = computed(() =>
  gitRepos.value.reduce((n, r) => n + (r.changes ? 1 : 0), 0))

function dueText(x: Todo): string {
  if (!x.dueDate) return ''
  if (x.dueDate < todayStr.value) return t('dash_overdue')
  if (x.dueDate === todayStr.value) return t('dash_due_today')
  return x.dueDate.slice(5)
}

/** 逾期或今天到期都算「要紧张一下」，列表里标红提醒。 */
function isUrgent(x: Todo): boolean {
  return !!x.dueDate && x.dueDate <= todayStr.value
}

// ---- 加载 ----
async function loadTodos() {
  try {
    todos.value = (unwrap(await ListTodos()) as Todo[] | null) || []
    delete failed.value.todo
  } catch (e) {
    failed.value.todo = true
    logErr('dashboard.todos', e)
  }
}

async function loadMonitors() {
  try {
    monitors.value = (unwrap(await ListMonitors()) as Monitor[] | null) || []
    delete failed.value.monitor
  } catch (e) {
    failed.value.monitor = true
    logErr('dashboard.monitors', e)
    return
  }
  await loadUptime()
}

/**
 * 24h 可用率：汇总一次（stats），再对启用中的监控逐个取检测日志画热力条。
 * 日志按监控独立请求（都是本地 IPC，监控通常个位数，全并发）；单项失败
 * 只让那一行的条是「无数据」，不拖垮整块。
 */
async function loadUptime() {
  try {
    const st = (unwrap(await ListMonitorStats()) as MonitorStat[] | null) || []
    const map: Record<string, MonitorStat> = {}
    for (const s of st) map[s.monitorId] = s
    monStats.value = map
  } catch (e) {
    logErr('dashboard.monStats', e)
    return
  }
  const enabled = monitors.value.filter(m => m.enabled).slice(0, 8)
  const since = Math.floor(Date.now() / 1000) - DAY_SEC
  const pairs = await Promise.all(enabled.map(async m => {
    try {
      const logs = (unwrap(await GetMonitorLogsSince(m.id, since, 1000)) as CheckPoint[] | null) || []
      return [m.id, logs] as const
    } catch {
      return [m.id, [] as CheckPoint[]] as const
    }
  }))
  monLogs.value = Object.fromEntries(pairs)
}

async function loadRuntimes() {
  try {
    runtimes.value = (unwrap(await EnvList()) as RuntimeInfo[] | null) || []
    delete failed.value.env
  } catch (e) {
    failed.value.env = true
    logErr('dashboard.env', e)
  }
}

/**
 * Everything / DSH 的常驻状态。两者都不在 EnvList 里，各走自己的状态接口。
 * 独立 catch：探测失败只让那一行不出现，不该把「常驻服务」卡整张拖成加载失败。
 */
async function loadTools() {
  const [es, ds] = await Promise.allSettled([EnvEverythingStatus(), DSHStatus()])
  if (es.status === 'fulfilled') {
    const v = unwrap<{ running?: boolean; enabled?: boolean }>(es.value)
    fileSearch.value = v ? { running: !!v.running, enabled: !!v.enabled } : null
  } else {
    logErr('dashboard.everything', es.reason)
  }
  if (ds.status === 'fulfilled') {
    const v = unwrap<{ running?: boolean; port?: number }>(ds.value)
    dsh.value = v ? { running: !!v.running, port: v.port || 0 } : null
  } else {
    logErr('dashboard.dsh', ds.reason)
  }
}

async function loadPorts() {
  try {
    ports.value = (unwrap(await ListListeningPorts()) as PortInfo[] | null) || []
    delete failed.value.port
  } catch (e) {
    failed.value.port = true
    logErr('dashboard.port', e)
  }
}

async function loadItems() {
  try {
    items.value = (unwrap(await GetMostUsedItems(6)) as CollectionItem[] | null) || []
    delete failed.value.item
  } catch (e) {
    failed.value.item = true
    logErr('dashboard.items', e)
  }
}

/** Git 仓库状态：先拿清单，再逐个查 status（仓库少、命令都是本地的，可以全并发）。 */
async function loadGit() {
  try {
    const refs = (unwrap(await GitListRepos()) as GitRepoRef[] | null) || []
    const rows = await Promise.all(refs.map(async r => {
      try {
        const st = unwrap<{ branch: string; ahead: number; behind: number; changes: unknown[] | null }>(
          await GitRepoStatus(r.path))
        return {
          name: r.name, branch: st?.branch || '', ahead: st?.ahead || 0, behind: st?.behind || 0,
          changes: st?.changes?.length || 0, error: '',
        } as GitRepoRow
      } catch (e) {
        logErr('dashboard.gitStatus', e)
        return { name: r.name, branch: '', ahead: 0, behind: 0, changes: 0, error: t('dash_git_err') }
      }
    }))
    gitRepos.value = rows
    delete failed.value.git
  } catch (e) {
    failed.value.git = true
    logErr('dashboard.git', e)
  }
}

async function refreshAll(silent = false) {
  if (!silent) loading.value = true
  await Promise.all([loadTodos(), loadMonitors(), loadRuntimes(), loadTools(), loadPorts(), loadItems(), loadGit()])
  loading.value = false
}

// ---- 交互 ----
function go(page: string) {
  navigate(page)
}

async function toggleTodo(x: Todo) {
  try {
    unwrap(await ToggleTodo(x.id))
    await loadTodos()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('dashboard.toggleTodo', e)
  }
}

async function openItem(it: CollectionItem) {
  try {
    unwrap(await OpenItem(it))
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('dashboard.openItem', e)
  }
}

// ===== PREVIEW-ONLY（截图预览用：浏览器里没有 Wails 桥，加 ?preview=1 注入假数据以便 chrome headless 截图调样式；仅 DEV 生效，保留供后续视觉迭代）=====
const PREVIEW = import.meta.env.DEV && window.location.search.includes('preview=1')
function seedPreview() {
  const d = new Date()
  const day = (n: number) => {
    const x = new Date(d.getTime() + n * 86400000)
    return `${x.getFullYear()}-${String(x.getMonth() + 1).padStart(2, '0')}-${String(x.getDate()).padStart(2, '0')}`
  }
  todos.value = [
    { id: '1', title: '修 composer 在中文输入法下的回车误发送', status: 'open', priority: 'high', dueDate: day(-2) },
    { id: '2', title: '梳理 notes 重命名后遗留的接口字段', status: 'open', priority: 'normal', dueDate: day(-1) },
    { id: '3', title: '给 MCP 补 logs 工具', status: 'open', priority: 'normal', dueDate: day(0) },
    { id: '4', title: '复盘端口扫描插件的内存占用', status: 'open', priority: 'normal', dueDate: day(0) },
    { id: '5', title: '把 Git 面板的 diff 加行号', status: 'open', priority: 'normal', dueDate: day(2) },
    { id: '6', title: '整理数据目录迁移的验收清单', status: 'open', priority: 'normal', dueDate: '' },
  ] as unknown as Todo[]
  monitors.value = [
    { id: '1', name: '博客', kind: 'http', url: 'https://blog.example.dev', target: '', enabled: true, lastStatus: 'down' },
    { id: '2', name: 'MySQL', kind: 'tcp', url: '', target: '127.0.0.1:3306', enabled: true, lastStatus: 'down' },
    { id: '3', name: 'API 网关', kind: 'http', url: 'https://api.example.dev/health', target: '', enabled: true, lastStatus: 'up' },
    { id: '4', name: 'nginx', kind: 'process', url: '', target: 'nginx.exe', enabled: true, lastStatus: 'up' },
    { id: '5', name: 'Redis', kind: 'tcp', url: '', target: '6379', enabled: true, lastStatus: 'up' },
    { id: '6', name: '备份脚本', kind: 'cmd', url: '', target: 'backup.cmd --check', enabled: true, lastStatus: '' },
  ] as unknown as Monitor[]
  runtimes.value = [
    { id: 'nginx', name: 'Nginx', group: 'web', installed: [1, 2], enabled: true, hasService: true },
    { id: 'mysql', name: 'MySQL', group: 'db', installed: [1, 2, 3], enabled: true, hasService: true },
    { id: 'redis', name: 'Redis', group: 'db', installed: [1], enabled: true, hasService: true },
    { id: 'php', name: 'PHP', group: 'lang', installed: [1, 2], enabled: false, hasService: false },
    { id: 'node', name: 'Node.js', group: 'lang', installed: [1, 2, 3, 4], enabled: false, hasService: false },
    { id: 'go', name: 'Go', group: 'lang', installed: [1], enabled: false, hasService: false },
    { id: 'postgresql', name: 'PostgreSQL', group: 'db', installed: [], enabled: false, hasService: true },
    { id: 'mongo', name: 'MongoDB', group: 'db', installed: [1], enabled: false, hasService: true },
    { id: 'traefik', name: 'Traefik', group: 'web', installed: [1], enabled: false, hasService: true },
  ] as unknown as RuntimeInfo[]
  fileSearch.value = { running: true, enabled: true }
  dsh.value = { running: true, port: 8123 }
  // 热力条种子：给每个监控造一份 5 分钟间隔的 24h 检测记录，down 窗口用确定性
  // 区间（不用随机数，保证每次截图布局一致）；id 1/2 各挂两段、其余全绿。
  const nowSec = Math.floor(Date.now() / 1000)
  const badWindows: Record<string, number[][]> = {
    '1': [[nowSec - 3600 * 20, nowSec - 3600 * 19.4], [nowSec - 3600 * 6, nowSec - 3600 * 5.4]],
    '2': [[nowSec - 3600 * 11, nowSec - 3600 * 10.3]],
    '3': [[nowSec - 3600 * 2, nowSec - 3600 * 1.8]],
  }
  const seedLogs = (id: string): CheckPoint[] => {
    const arr: CheckPoint[] = []
    for (let t = nowSec - DAY_SEC; t <= nowSec; t += 300) {
      const bad = (badWindows[id] || []).some(([a, b]) => t >= a && t <= b)
      arr.push({ checkedTs: t, status: bad ? 'down' : 'up' })
    }
    return arr
  }
  // 「备份脚本」(id 6) 是待检测状态，不给它造数据——保持「无检测记录」的真实形态
  const seededIds = ['1', '2', '3', '4', '5']
  monStats.value = Object.fromEntries(seededIds.map(id => {
    const logs = seedLogs(id)
    const up = logs.filter(l => l.status === 'up').length
    return [id, {
      monitorId: id, totalChecks: logs.length, upChecks: up,
      uptimeRatio: (up / logs.length) * 100, avgLatencyMs: 42, lastDownAt: '',
    }]
  }))
  monLogs.value = Object.fromEntries(seededIds.map(id => [id, seedLogs(id)]))
  ports.value = [
    { port: 3306, protocol: 'TCP', pid: 4412, process: 'mysqld.exe', path: '', self: false },
    { port: 6379, protocol: 'TCP', pid: 8104, process: 'redis-server.exe', path: '', self: false },
    { port: 9245, protocol: 'TCP', pid: 22164, process: 'quickdock.exe', path: '', self: true },
    { port: 9230, protocol: 'TCP', pid: 22164, process: 'quickdock.exe', path: '', self: true },
    { port: 5173, protocol: 'TCP', pid: 15092, process: 'node.exe', path: '', self: false },
    { port: 8080, protocol: 'TCP', pid: 7712, process: 'nginx.exe', path: '', self: false },
  ]
  items.value = [
    { id: '1', name: 'quickdock', value: 'E:\\pp\\quickdock', type: 'folder' },
    { id: '2', name: 'hnwf', value: 'E:\\pp\\hnwf', type: 'folder' },
    { id: '3', name: '设计稿', value: 'https://figma.com/x', type: 'url' },
    { id: '4', name: '常用命令', value: 'git status --short', type: 'command' },
    { id: '5', name: 'release.md', value: 'E:\\pp\\quickdock\\docs\\release.md', type: 'file' },
  ] as unknown as CollectionItem[]
  gitRepos.value = [
    { name: 'quickdock', branch: 'dev', ahead: 2, behind: 0, changes: 7, error: '' },
    { name: 'hnwf', branch: 'main', ahead: 0, behind: 3, changes: 0, error: '' },
    { name: 'env-runtime', branch: 'feat/wsl', ahead: 1, behind: 0, changes: 0, error: '' },
    { name: 'docs', branch: 'main', ahead: 0, behind: 0, changes: 0, error: '' },
  ]
}
// ===== PREVIEW-ONLY END =====

// 监控状态是高变动数据，60 秒静默刷一次（静默=不显示 loading，免得用户正在看
// 这一页时整块内容闪一下）。只刷监控：待办变化由用户自己操作触发，
// 而端口与运行时清单要枚举系统进程/扫目录，每分钟跑一遍不值当。
let timer: ReturnType<typeof setInterval> | null = null
onMounted(() => {
  if (PREVIEW) { seedPreview(); return }
  void refreshAll()
  timer = setInterval(() => { void loadMonitors() }, 60000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="qd-page">
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('navDashboard') }}</h2>
        <span class="qd-page-sub">{{ t('dash_subtitle') }}</span>
      </div>
      <div class="qd-page-actions">
        <button class="qd-btn qd-btn-ghost" :disabled="loading" @click="refreshAll()">
          <RefreshCw :size="13" :class="{ spin: loading }" />
          {{ t('dash_refresh') }}
        </button>
      </div>
    </div>

    <div class="qd-page-body">
      <div class="dash">
        <!-- 仪表带：左边问候定调，右边四个读数共用一根线。问候进正文而不进页头：
             页头 15px 的标题行装不下「早上好」的分量，大字问候才是这一页的开场。 -->
        <section class="band">
          <div class="hero">
            <span class="hero-title">{{ greeting }}</span>
            <span class="hero-sub">{{ dateText }}</span>
          </div>

          <button class="readout" @click="go('todo')">
            <span class="r-label"><ListTodo :size="12" />{{ t('dash_stat_todo') }}</span>
            <span class="r-num">{{ openTodos.length }}</span>
            <span class="r-foot" :class="{ danger: overdue > 0 }">
              <template v-if="overdue">{{ t('dash_stat_overdue', { n: overdue }) }}</template>
              <template v-else>{{ t('dash_stat_due_today', { n: todayDue }) }}</template>
            </span>
          </button>

          <button class="readout" :class="{ alert: downMonitors.length > 0 }" @click="go('monitor')">
            <span class="r-label"><Activity :size="12" />{{ t('dash_stat_monitor') }}</span>
            <span class="r-num">{{ downMonitors.length }}</span>
            <span class="r-foot">{{ t('dash_stat_monitor_foot', { n: enabledMonitors.length }) }}</span>
          </button>

          <button class="readout" @click="go('environments')">
            <span class="r-label"><Server :size="12" />{{ t('dash_stat_env') }}</span>
            <span class="r-num">{{ residentServices.length }}</span>
            <span class="r-foot">{{ t('dash_stat_env_foot', { n: installedRuntimes.length }) }}</span>
          </button>

          <div class="readout static">
            <span class="r-label"><Plug :size="12" />{{ t('dash_stat_port') }}</span>
            <span class="r-num">{{ ports.length }}</span>
            <span class="r-foot">{{ t('dash_stat_port_foot') }}</span>
          </div>
        </section>

        <!-- 卡片网格：宽屏三列、中屏两列、窄屏一列。
             原先固定两列 + 1180 上限，窗口一最大化就变成中间一条窄带、两侧大片留白，
             看着像没加载完。三列后卡片填满宽度，同一行的卡片等高，扫读不用来回找。 -->
          <div class="cards">
            <!-- 健康卡占大位（span 4）：这一页真正的图表在这——
                 顶部一条 24h 全局热力条（整段时间里谁挂过一眼可见），
                 每行再给单项的 24h 条与可用率，列表就有了时间维度 -->
            <section class="card span-4">
              <header class="card-head">
                <Activity :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_health') }}</span>
                <span v-if="downMonitors.length" class="card-count bad">{{ t('dash_down_n', { n: downMonitors.length }) }}</span>
                <button class="card-link" @click="go('monitor')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.monitor" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!monitorList.length" class="empty">{{ t('dash_health_empty') }}</p>
              <template v-else>
                <div class="ribbon-wrap" :title="t('dash_uptime_24h')">
                  <div class="ribbon-meta">
                    <span class="ribbon-label">{{ t('dash_uptime_24h') }}</span>
                    <span :class="['ribbon-pct', 'mono', { bad: globalUptime >= 0 && globalUptime < 99 }]">{{ globalUptimeText }}</span>
                  </div>
                  <div class="ribbon">
                    <span v-for="(b, i) in globalRibbon" :key="i" :class="['cell', b]"></span>
                  </div>
                </div>
                <ul class="rows">
                  <li v-for="m in monitorList" :key="m.id" class="row clickable" @click="go('monitor')"
                      :title="(m.kind === 'http' ? m.url : m.target) + (upPct(m.id) === '—' ? '' : ' · ' + upPct(m.id))">
                    <span :class="['dot', m.lastStatus || 'unknown']"></span>
                    <span class="row-title" >{{ m.name }}</span>
                    <span v-if="m.lastStatus === 'down'" class="due warn">{{ t('dash_down') }}</span>
                    <span v-else-if="!m.lastStatus" class="due">{{ t('dash_never_checked') }}</span>
                    <span class="ribbon mini">
                      <span v-for="(b, i) in bucketsOf(m.id)" :key="i" :class="['cell', b]"></span>
                    </span>
                    <span class="up-pct mono">{{ upPct(m.id) }}</span>
                  </li>
                </ul>
              </template>
            </section>

            <!-- 待办：到期分布条用颜色直接回答「紧不紧张」，不必逐行读日期 -->
            <section class="card span-2">
              <header class="card-head">
                <ListTodo :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_today_todo') }}</span>
                <span v-if="todoList.length" class="card-count">{{ todoList.length }}</span>
                <button class="card-link" @click="go('todo')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.todo" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!todoList.length" class="empty">{{ t('dash_todo_empty') }}</p>
              <template v-else>
                <div v-if="todoDist.length" class="todo-dist" :title="t('dash_dist_title')">
                  <span v-for="d in todoDist" :key="d.k"
                        :class="['seg', d.cls]" :style="{ flexGrow: d.n }"
                        :title="d.k === 'overdue' ? t('dash_stat_overdue', { n: d.n }) : d.k === 'today' ? t('dash_stat_due_today', { n: d.n }) : t('dash_dist_later', { n: d.n })"></span>
                </div>
                <ul class="rows">
                  <li v-for="x in todoList" :key="x.id" class="row">
                    <button class="check" :title="t('dash_mark_done')" @click="toggleTodo(x)">
                      <Check :size="10" />
                    </button>
                    <span class="row-title" :title="x.title">{{ x.title }}</span>
                    <span v-if="x.priority === 'high'" class="pri" :title="t('dash_pri_high')"></span>
                    <span v-if="dueText(x)" :class="['due', { warn: isUrgent(x) }]">{{ dueText(x) }}</span>
                  </li>
                </ul>
              </template>
            </section>

          <!-- 侧列：Git 仓库状态排最前——有没有忘提交的、落后没拉的，比端口更常看 -->
            <section class="card span-2">
              <header class="card-head">
                <FolderGit2 :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_git') }}</span>
                <span v-if="gitBusyCount" class="card-count bad">{{ t('dash_git_dirty', { n: gitBusyCount }) }}</span>
                <button class="card-link" @click="go('git')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.git" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!gitList.length" class="empty">{{ t('dash_git_empty') }}</p>
              <ul v-else class="rows">
                <li v-for="r in gitList" :key="r.name" class="row clickable" :title="r.error || r.name" @click="go('git')">
                  <FolderGit2 :size="12" class="row-icon" />
                  <span class="row-title">{{ r.name }}</span>
                  <span v-if="r.changes" class="due warn">{{ t('git_changes', { n: r.changes }) }}</span>
                  <span v-if="r.ahead" class="git-ab">↑{{ r.ahead }}</span>
                  <span v-if="r.behind" class="git-ab down">↓{{ r.behind }}</span>
                  <span v-if="!r.changes && !r.ahead && !r.behind && !r.error" class="git-ok">
                    <Check :size="11" />
                  </span>
                  <span v-if="r.branch" class="row-target">{{ r.branch }}</span>
                </li>
              </ul>
            </section>

            <!-- 常驻服务：仪表带给了数字，这里给「具体是哪几个」——
                 数字只能说明「有几个在跑」，排查时要知道是哪个在跑。
                 除了 env 的常驻运行时，Everything 与 DSH 这两个自带常驻进程也一并列出 -->
            <section class="card span-2">
              <header class="card-head">
                <Server :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_env_resident') }}</span>
                <span v-if="residentList.length" class="card-count">{{ residentList.length }}</span>
                <button class="card-link" @click="go('environments')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="!residentList.length" class="empty">
                {{ failed.env ? t('dash_load_failed') : t('dash_env_empty') }}
              </p>
              <ul v-else class="rows">
                <li v-for="r in residentList" :key="r.id" class="row clickable" @click="go(r.page)">
                  <span class="dot up"></span>
                  <span class="row-title">{{ r.name }}</span>
                  <span class="row-target">{{ r.meta }}</span>
                </li>
              </ul>
            </section>

            <section class="card span-2">
              <header class="card-head">
                <Plug :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_ports') }}</span>
                <button class="card-link" @click="go('environments')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.port" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!portList.length" class="empty">{{ t('dash_ports_empty') }}</p>
              <ul v-else class="rows">
                <li v-for="p in portList" :key="`${p.protocol}-${p.port}`" class="row">
                  <span class="port">{{ p.port }}</span>
                  <span class="row-title mono" :title="p.path">{{ p.process || t('dash_unknown_proc') }}</span>
                  <span v-if="p.self" class="due">{{ t('dash_self') }}</span>
                </li>
              </ul>
            </section>

            <!-- 最近使用：横排磁贴收尾——竖列表在这一页已经出现三次，再用一次
                 就全是同构卡片了；磁贴一行铺满宽度，页面底部不再悬着一块空白 -->
            <section class="card span-6">
              <header class="card-head">
                <FolderOpen :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_recent') }}</span>
                <button class="card-link" @click="go('workspace')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.item" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!items.length" class="empty">{{ t('dash_recent_empty') }}</p>
              <div v-else class="tiles">
                <button v-for="it in items" :key="it.id" class="tile"
                        :title="it.value" @click="openItem(it)">
                  <span class="tile-ic"><FolderOpen :size="14" /></span>
                  <span class="tile-name">{{ it.name }}</span>
                  <span class="tile-type mono">{{ it.type }}</span>
                </button>
              </div>
            </section>
          </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 内容最宽 1680：再宽的话六行清单会被拉成一整条，眼睛要横跨半屏才读完一行；
   1680 在 1920 的屏幕上只留 120 的边距，不会再出现「中间一条窄带、两边全是空白」。 */
.dash {
  max-width: 1680px; margin: 0 auto;
  display: flex; flex-direction: column; gap: var(--space-5);
}

/* ---- 仪表带：问候 + 四读数共用一根线 ---- */
.band {
  display: grid; grid-template-columns: minmax(170px, 218px) repeat(4, minmax(0, 1fr));
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  /* 首页的第一块内容，必须像一块抬起来的板；否则读数和页面底色几乎同层 */
  box-shadow: var(--shadow-card);
  overflow: hidden;
}
.band > * + * { border-left: 1px solid var(--color-border); }

.hero {
  padding: 0 var(--space-7); min-width: 0;
  display: flex; flex-direction: column; justify-content: center; gap: 3px;
}
.hero-title {
  font-size: 19px; font-weight: 600; letter-spacing: -0.01em; line-height: 1.3; color: var(--color-text-primary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.hero-sub { font-size: 11px; color: var(--color-text-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

/* 读数三行全部左对齐：数字是主角，foot 只是注脚，右对齐的对角线留白反而显得散 */
.readout {
  display: flex; flex-direction: column; gap: 2px; min-width: 0;
  padding: 11px var(--space-6) 12px;
  background: none; border: 0; font: inherit; text-align: left; color: inherit;
  cursor: pointer; transition: background-color var(--transition-fast);
}
.readout:hover { background: var(--color-bg-hover); }
.readout.static { cursor: default; }
.readout.static:hover { background: none; }
.r-label {
  display: flex; align-items: center; gap: 5px;
  font-size: 11px; color: var(--color-text-muted); white-space: nowrap;
}
/* 大号读数：mono 保证四位数字不跳宽，字距收一点让它更像"仪表读数"而不是代码 */
.r-num {
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 24px; font-weight: 600; letter-spacing: -0.02em;
  line-height: 1.2; color: var(--color-text-primary);
}
.r-foot {
  font-size: 11px; color: var(--color-text-disabled);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.r-foot.danger { color: var(--color-danger); }
/* 异常读数只染数字——一排四个数字里，颜色比符号更快被看见 */
.readout.alert .r-num { color: var(--color-danger); }

/* ---- 卡片网格 ----
 * 6 列基底 + 跨列：健康卡 4 格、其余 2 格，重要内容拿大位，不再是六张同构卡片平铺。
 * align-items 保持默认 stretch —— 同一行的卡片等高，底边对齐，比参差的下边缘好读。
 */
.cards {
  display: grid; gap: var(--space-5);
  grid-template-columns: repeat(6, minmax(0, 1fr));
}
.span-2 { grid-column: span 2; }
.span-4 { grid-column: span 4; }
.span-6 { grid-column: 1 / -1; }

.card {
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-card);
  overflow: hidden;
}
.card-head {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 9px 14px;
  border-bottom: 1px solid var(--color-border);
}
.t-ic { color: var(--color-text-muted); flex-shrink: 0; }
.card-title { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }
.card-count {
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 10px; line-height: 15px; padding: 0 6px; border-radius: var(--radius-full);
  color: var(--color-text-muted); background: var(--color-bg-tertiary);
}
.card-count.bad { color: var(--color-danger); background: var(--color-danger-bg); }
.card-link {
  display: inline-flex; align-items: center; gap: 1px; margin-left: auto;
  background: none; border: 0; padding: 0; cursor: pointer;
  font: inherit; font-size: 11px; color: var(--color-text-muted);
  transition: color var(--transition-fast);
}
.card-link:hover { color: var(--color-accent); }

/* 卡片内的空态：不居中反而是对的（列表为空时上方还有热力条/分布条），
   但留白要够，否则一行灰字像被漏掉的说明。 */
.empty { margin: 0; padding: 22px 14px; font-size: var(--text-sm); line-height: 1.6; color: var(--color-text-muted); }

/* ---- 24h 可用率热力条 ----
 * 一格 = 30 分钟：绿=全程在线、红=挂过、暗=没有数据。放在卡头之下、列表之上，
 * 「整体一眼 → 逐项细看」的阅读顺序由它领起。 */
.ribbon-wrap {
  padding: var(--space-3) 14px var(--space-4);
  border-bottom: 1px solid var(--color-border);
}
.ribbon-meta {
  display: flex; align-items: baseline; gap: var(--space-3);
  margin-bottom: 6px;
}
.ribbon-label { font-size: 11px; color: var(--color-text-muted); }
.ribbon-pct {
  margin-left: auto; font-size: 13px; font-weight: 600;
  font-variant-numeric: tabular-nums; color: var(--color-success);
}
.ribbon-pct.bad { color: var(--color-danger); }

.ribbon { display: flex; gap: 2px; height: 16px; }
.cell { flex: 1 1 0; min-width: 0; border-radius: 1.5px; background: var(--color-bg-tertiary); }
/* 整条全绿会炸：up 用 55% 透明度压成「在线但不刺眼」，down 保持饱和 */
.cell.up { background: var(--color-success); opacity: .5; }
.cell.down { background: var(--color-danger); opacity: .85; }

/* 行内 mini 条：宽度固定，与名称/徽章同行；格子数同全局条，密度一致才像一张图 */
.ribbon.mini { width: 132px; flex-shrink: 0; height: 12px; }
.up-pct {
  width: 52px; flex-shrink: 0; text-align: right;
  font-size: 11px; font-variant-numeric: tabular-nums;
  color: var(--color-text-secondary);
}

/* ---- 待办到期分布条：三段各按数量占比，颜色即语义 ---- */
.todo-dist { display: flex; gap: 2px; height: 5px; margin: var(--space-3) 14px 0; }
.seg { min-width: 6px; border-radius: 2px; }
.seg.danger { background: var(--color-danger); }
.seg.warn { background: var(--color-warning); }
.seg.ok { background: var(--color-accent); opacity: .55; }

/* ---- 最近使用磁贴 ---- */
.tiles {
  display: flex; flex-wrap: wrap; gap: var(--space-3);
  padding: var(--space-4) 14px;
}
.tile {
  display: inline-flex; align-items: center; gap: var(--space-3);
  padding: 8px 14px 8px 10px; min-width: 0; max-width: 280px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font: inherit; cursor: pointer; text-align: left;
  transition: border-color var(--transition-fast), background-color var(--transition-fast);
}
.tile:hover { border-color: var(--color-accent-border-strong); background: var(--color-bg-hover); }
.tile-ic {
  display: grid; place-items: center; flex-shrink: 0;
  width: 26px; height: 26px; border-radius: var(--radius-xs);
  background: var(--color-accent-bg); color: var(--color-accent);
}
.tile-name {
  min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: 12px; font-weight: 500; color: var(--color-text-primary);
}
.tile-type {
  flex-shrink: 0; font-size: 10px; color: var(--color-text-muted);
  background: var(--color-bg-secondary); padding: 1px 6px; border-radius: var(--radius-full);
}

/* ---- 行 ---- */
.rows { list-style: none; margin: 0; padding: var(--space-2); display: flex; flex-direction: column; }
.row {
  display: flex; align-items: center; gap: var(--space-4);
  height: 32px; padding: 0 var(--space-4); border-radius: var(--radius-xs);
  font-size: 12px; min-width: 0;
}
/* WebView2 不支持 :has()，所以「这行可点」只能靠显式 class，不能靠子元素推断 */
.row.clickable { cursor: pointer; }
.row:hover { background: var(--color-bg-hover); }
.row-title {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-text-primary);
}
.row-icon { color: var(--color-text-muted); flex-shrink: 0; }
.row-target {
  max-width: 46%; flex-shrink: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono); font-size: 11px; color: var(--color-text-muted);
}
.mono { font-family: var(--font-mono); }

/* 勾选框：常态只是一圈细线，悬停才浮出「完成」的绿色，避免六行全是花花绿绿的圆点 */
.check {
  display: grid; place-items: center; flex-shrink: 0;
  width: 16px; height: 16px; padding: 0;
  background: none; border: 1.5px solid var(--color-border-light); border-radius: 50%;
  color: transparent; cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast);
}
.row:hover .check { color: var(--color-text-muted); border-color: var(--color-text-muted); }
.row .check:hover {
  color: var(--color-success); border-color: var(--color-success); background: var(--color-success-bg);
}

.dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; background: var(--color-text-disabled); }
.dot.up { background: var(--color-success); }
.dot.down { background: var(--color-danger); box-shadow: 0 0 0 3px var(--color-danger-bg); }

/* 优先级用一个小三角点，比再写一个「高」字省地方，也不与到期标撞色 */
.pri { width: 0; height: 0; flex-shrink: 0; border-left: 3px solid transparent; border-right: 3px solid transparent; border-bottom: 5px solid var(--color-danger); }

.due {
  flex-shrink: 0; font-size: 10px; line-height: 1.6; padding: 0 5px;
  border-radius: var(--radius-xs); color: var(--color-text-muted); background: var(--color-bg-tertiary);
}
.due.warn { color: var(--color-danger); background: var(--color-danger-bg); }

.port {
  width: 44px; flex-shrink: 0;
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 12px; color: var(--color-text-secondary);
}
.mono { font-family: var(--font-mono); }

/* Git 行的 ↑↓ 与干净标记 */
.git-ab { font-family: var(--font-mono); font-size: 11px; color: var(--color-warning); flex-shrink: 0; }
.git-ab.down { color: var(--color-accent); }
.git-ok { display: flex; color: var(--color-success); flex-shrink: 0; }

.spin { animation: dash-spin 900ms linear infinite; }
@keyframes dash-spin { to { transform: rotate(360deg); } }

/* 1360 以下 6 列会挤：健康卡与最近磁贴退全宽，中三张两两成行 */
@media (max-width: 1360px) {
  .cards { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .span-4, .span-6 { grid-column: 1 / -1; }
  .span-2 { grid-column: auto; }
  .ribbon.mini { width: 96px; }
}

@media (max-width: 1080px) {
  .cards { grid-template-columns: minmax(0, 1fr); }
  .band { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  /* 问候独占一行，读数两两成行：竖线只在左列，横线补行间 */
  .hero { grid-column: 1 / -1; padding: var(--space-5) var(--space-6); border-bottom: 1px solid var(--color-border); }
  .band > * + * { border-left: 0; }
  .band > .readout:nth-child(even) { border-right: 1px solid var(--color-border); }
  .band > .readout:nth-child(n+4) { border-top: 1px solid var(--color-border); }
}
</style>
