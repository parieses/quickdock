<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { ScrollText, FolderOpen, RefreshCw, Search, Copy, Check, AlertTriangle, Trash2, HardDrive, Radio, Plus, X, Pause, Play } from '@lucide/vue'
import { ListLogFiles, ReadLogFile, ListCrashFiles, ClearCrashFiles, GetLogStorage, ClearLogs } from '../../bindings/quickdock/services/diag/diagservice'
import { OpenLogsDir } from '../../bindings/quickdock/services/system/systemservice'
import { SelectLocalFile } from '../../bindings/quickdock/services/appservice'
import { LogTailOpen, LogTailStop, LogTailPause } from '../../bindings/quickdock/services/logtail/logtailservice'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { copyToClipboard } from '../utils/clipboard'
import { formatBytes } from '../utils/ssh'

const { t } = useI18n()

interface LogFile { name: string; size: number; time: string }
interface CrashFile { name: string; size: number; time: string; kind: string }

const files = ref<LogFile[]>([])
// 崩溃记录单独取一份：ListLogFiles 里虽然也带 "crash/" 前缀的条目，
// 但只有 ListCrashFiles 给出 kind（panic / js）—— 那是后端按落盘规则判定的，
// 前端照文件名前缀猜会在命名规则变动时静默错位。
const crashFiles = ref<CrashFile[]>([])
// 三种来源互斥：应用日志 / 崩溃记录 / 实时尾随。
// 原来是 isCrash 布尔，加第三种来源后布尔不够用了（会变成两个人改同一个标志位）。
type Source = 'log' | 'crash' | 'tail'
const source = ref<Source>('log')
const isCrash = computed(() => source.value === 'crash')
const isTail = computed(() => source.value === 'tail')
const selected = ref('')
// 静态来源（日志/崩溃）读出来的行；展示统一走下面的 rawLines 计算属性
const staticLines = ref<string[]>([])
// 展示用的原始行：静态来源读全文、尾随来源读当前标签的环形缓冲。
// 后面的过滤/级别人色/复制全部建立在它之上，三种来源共用一条渲染管线。
const rawLines = computed<string[]>(() =>
  isTail.value ? (activeTab.value?.lines ?? []) : staticLines.value,
)
const keyword = ref('')
const levels = ref<Record<string, boolean>>({ I: true, W: true, E: true })
const follow = ref(false)
const loading = ref(false)
let followTimer: ReturnType<typeof setInterval> | null = null
let lastCount = 0
let lastFollowContent = '' // 跟随轮询用的「上次内容」快照，避免每 2s 整段 join 比对
const LOG_FOLLOW_INTERVAL_MS = 2000
const LOG_TAIL_LINES = 3000

const logFiles = computed(() => files.value.filter((f) => !f.name.startsWith('crash/')))

const levelOf = (line: string): string => {
  const m = line.match(/\]\s*\[([IWE])\]/)
  if (m) return m[1]
  if (/\[W\]/.test(line)) return 'W'
  if (/\[E\]/.test(line)) return 'E'
  return 'I'
}

const filteredLines = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return rawLines.value.filter((l) => {
    const lv = levelOf(l)
    if (!levels.value[lv]) return false
    if (kw && !l.toLowerCase().includes(kw)) return false
    return true
  })
})

// 把一行拆成「时间戳 / 级别 / 正文」三段分别排版。
//
// 不拆就只能整行一个颜色，警告和错误埋在正文里看不出来。拆开后时间戳可以弱化，
// 级别独占一列上色，正文才是阅读主体。
//
// 匹配不上的行（Go panic 栈、第三方程序输出、插件透传的多行文本）整体进正文，
// 级别走 levelOf 的兜底判定——这类行本来就该原样呈现，不该被排版逻辑改写。
const LINE_RE = /^(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d{1,3})?)\s+\[([IWE])\]\s?([\s\S]*)$/

interface LogRow {
  /** 原始整行，复制时用这个（带上时间戳才有上下文） */
  raw: string
  ts: string
  level: string
  msg: string
}

function parseLine(raw: string): LogRow {
  const m = raw.match(LINE_RE)
  if (m) return { raw, ts: m[1], level: m[2], msg: m[3] }
  return { raw, ts: '', level: levelOf(raw), msg: raw }
}

// 崩溃文件不是「带时间戳的日志流」，而是「一次现场」：首部 time/kind/label/message
// 头块 + 其后一堆堆栈/详情行。这些行本身没有时间戳，直接套用日志的三段栅格会整列空着、
// 级别也落不到（stack 里没有 [E]/[W]），看起来就像坏了。
//
// 处置：把头块抽成 meta，整段详情共用「现场时间」与「按 kind 定的级别」
// （js→W、panic→E），这样崩溃记录也按日志的格式对齐，时间一眼可见。
interface CrashMeta { time: string; kind: string; label: string; message: string }

const crashMeta = computed<CrashMeta | null>(() => {
  if (!isCrash.value) return null
  const m: CrashMeta = { time: '', kind: '', label: '', message: '' }
  for (const l of rawLines.value) {
    let x: RegExpMatchArray | null
    if ((x = l.match(/^time:\s*(.+)$/))) m.time = x[1].trim()
    else if ((x = l.match(/^kind:\s*(.+)$/))) m.kind = x[1].trim()
    else if ((x = l.match(/^label:\s*(.+)$/))) m.label = x[1].trim()
    else if ((x = l.match(/^message:\s*(.+)$/))) m.message = x[1].trim()
    else if (l.trim() === '') break
  }
  return m
})

function crashLevel(kind: string): string {
  return kind.toLowerCase() === 'js' ? 'W' : 'E'
}

const crashDisplayLines = computed<LogRow[]>(() => {
  const meta = crashMeta.value
  if (!meta) return []
  const lvl = crashLevel(meta.kind)
  if (!levels.value[lvl]) return []
  const kw = keyword.value.trim().toLowerCase()
  // 头块（time/kind/label/message + 空行）之后才是堆栈/详情
  const blank = rawLines.value.findIndex((l) => l.trim() === '')
  const detail = blank >= 0 ? rawLines.value.slice(blank + 1) : rawLines.value
  return detail
    .filter((l) => !kw || l.toLowerCase().includes(kw))
    .map((l) => ({ raw: l, ts: meta.time, level: lvl, msg: l }))
})

const displayLines = computed<LogRow[]>(() =>
  isCrash.value ? crashDisplayLines.value : filteredLines.value.map(parseLine),
)

function escapeHtml(s: string): string {
  return s.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c] as string))
}

function highlight(line: string): string {
  const kw = keyword.value.trim()
  const html = escapeHtml(line)
  if (!kw) return html
  const re = new RegExp(`(${kw.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi')
  return html.replace(re, '<mark>$1</mark>')
}

async function loadFiles() {
  try {
    const res = unwrap<LogFile[]>(await ListLogFiles())
    files.value = res || []
    if (!selected.value && logFiles.value.length) {
      selected.value = logFiles.value[0].name
      source.value = 'log'
    }
  } catch (_) { /* ignore */ }
  try {
    crashFiles.value = unwrap<CrashFile[]>(await ListCrashFiles()) || []
  } catch (_) { crashFiles.value = [] }
}

// 崩溃文件复用 ReadLogFile 读（它接受 "crash/x.log" 相对路径并做穿越校验，
// 还带 tail 截断）；ReadCrashFile 返回的是 []byte，过 JSON 会变成 base64，
// 前端还得再解一次，只在 MCP 那条通道上更合适。
function currentPath(): string {
  return isCrash.value ? 'crash/' + selected.value : selected.value
}

function selectFile(name: string, crash: boolean) {
  if (selected.value === name && source.value === (crash ? 'crash' : 'log')) return
  selected.value = name
  source.value = crash ? 'crash' : 'log'
  loadContent()
}

async function loadContent() {
  if (!selected.value) return
  loading.value = true
  try {
    const res = unwrap<string[]>(await ReadLogFile(currentPath(), LOG_TAIL_LINES))
    staticLines.value = res || []
    lastCount = rawLines.value.length
    lastFollowContent = rawLines.value.join('\n')
    await nextTick()
    scrollToBottom()
  } catch (e: any) {
    staticLines.value = ['[error] ' + getErrorMessage(e)]
  } finally {
    loading.value = false
  }
}

// 清空崩溃记录：二次点击确认（与 SSH 生产机执行同一套「再点一次」模式），
// 不额外引入确认弹窗——这个动作不可逆但也不珍贵，两次点击够了。
const clearArmed = ref(false)
let clearTimer: ReturnType<typeof setTimeout> | null = null

async function clearCrash() {
  if (!clearArmed.value) {
    clearArmed.value = true
    if (clearTimer) clearTimeout(clearTimer)
    clearTimer = setTimeout(() => { clearArmed.value = false }, 3000)
    return
  }
  if (clearTimer) clearTimeout(clearTimer)
  clearArmed.value = false
  try {
    unwrap(await ClearCrashFiles())
    crashFiles.value = []
    if (isCrash.value) {
      source.value = 'log'
      selected.value = logFiles.value[0]?.name || ''
      staticLines.value = []
      if (selected.value) await loadContent()
    }
    // 崩溃记录也算在占用里，清完必须重算，否则卡片会一直显示已删掉的那部分
    await loadStorage()
  } catch (e: any) {
    staticLines.value = ['[error] ' + getErrorMessage(e)]
  }
}

// ---- 日志占用与清理 ----
//
// 日志目录会一直长：宿主本身只在启动时清一次 30 天前的，插件日志、崩溃记录
// 各自还有别的落盘路径。没有一个「现在占了多少」的读数，用户要么永远不会
// 注意到，要么发现时目录已经几百 MB 却不知道从哪下手。
interface LogStorageInfo {
  dir: string
  totalSize: number
  logSize: number
  crashSize: number
  logCount: number
  crashCount: number
  oldest: string
  newest: string
  removableSize: number
}
const storage = ref<LogStorageInfo | null>(null)

// 占比条按「日志 / 崩溃」两段画：两者清理入口不同（崩溃另有「清空崩溃记录」），
// 只给一个总数的话，用户看到 300MB 也不知道该点哪个按钮。
const logPercent = computed(() => {
  const s = storage.value
  if (!s || s.totalSize <= 0) return 0
  return Math.max(0, Math.min(100, (s.logSize / s.totalSize) * 100))
})

async function loadStorage() {
  try {
    storage.value = unwrap<LogStorageInfo>(await GetLogStorage())
  } catch (_) { storage.value = null }
}

// 清理一律「点两次」：删文件不可逆，而这里的收益只是省空间，
// 不值得为它弹一个模态框打断正在看的日志——与清空崩溃记录保持同一套手感。
const cleanArmed = ref<'old' | 'all' | null>(null)
let cleanTimer: ReturnType<typeof setTimeout> | null = null

async function runCleanup(scope: 'old' | 'all') {
  if (cleanArmed.value !== scope) {
    cleanArmed.value = scope
    if (cleanTimer) clearTimeout(cleanTimer)
    cleanTimer = setTimeout(() => { cleanArmed.value = null }, 3000)
    return
  }
  if (cleanTimer) clearTimeout(cleanTimer)
  cleanArmed.value = null
  try {
    // keepDays=30 对齐宿主的自动清理线；scope=all 时传 0，删到只剩今天。
    // 今天的永远不删（后端保证）——logger 正握着它的写句柄，删了也不真释放。
    const res = unwrap<{ removed: number; freed: number }>(await ClearLogs(scope === 'old' ? 30 : 0, true))
    if (res && res.removed > 0) {
      toastMsg.value = t('logCleanupDone', { n: res.removed, size: formatBytes(res.freed) })
    } else {
      toastMsg.value = t('logCleanupNone')
    }
    msgTimer()
    await loadFiles()
    await loadStorage()
  } catch (e: any) {
    toastMsg.value = t('logCleanupFailed') + ': ' + getErrorMessage(e)
    msgTimer()
  }
}

// 清理结果的提示：本组件不 inject toast（设置页与主页面都挂它，注入点不统一），
// 用一行自带的横条反馈，位置固定在占用卡片下方，不会跑到别的实例身上。
const toastMsg = ref('')
let toastTimer: ReturnType<typeof setTimeout> | null = null
function msgTimer() {
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toastMsg.value = '' }, 4000)
}

// 用 ref 而不是 document.querySelector('.log-body')：设置面板里将来若同时挂
// 两个日志视图（比如崩溃详情侧栏），全局查询会抓到另一个实例，滚动就跑到别人身上了。
const bodyEl = ref<HTMLElement | null>(null)

function scrollToBottom() {
  const el = bodyEl.value
  if (el) el.scrollTop = el.scrollHeight
}

function toggleFollow() {
  follow.value = !follow.value
  if (follow.value) {
    followTimer = setInterval(async () => {
      if (!selected.value) return
      try {
        const res = unwrap<string[]>(await ReadLogFile(currentPath(), LOG_TAIL_LINES))
        const lines = res || []
        const joined = lines.join('\n')
        // 内容没变就什么都不做：不重绘、不抢滚动条。后端 ReadLogFile 已对未变更文件做
        // 整文件读缓存，这里只在内容真正变化时才 join 一次并决定要不要滚到底部。
        if (joined === lastFollowContent) return
        staticLines.value = lines
        lastCount = lines.length
        lastFollowContent = joined
        await nextTick()
        scrollToBottom()
      } catch (_) { /* ignore */ }
    }, LOG_FOLLOW_INTERVAL_MS)
  } else if (followTimer) {
    clearInterval(followTimer)
    followTimer = null
  }
}

// 文件切换由 selectFile 显式触发加载（它同时设置 source，watch(selected)
// 会在「同名文件、不同来源」时不触发，容易读到上一个文件的内容）。
// 纯前端过滤（levels/keyword）由 computed 自动完成，无需 watch 触发。

// ==================== 实时尾随 ====================
//
// 与上面两种来源的根本区别：静态日志是「读一次」，尾随是「订阅一段增长」。
// 后端每 500ms 看一次文件，只推新增的行；这里要做的是收、攒、不超过内存预算。
//
// 不用 setInterval 在前端轮询：每次把整块文件读出来再通过 JSON 传一遍既浪费 IO 也卡主线程，
// 而后端已经握着偏移——让它来决策「有没有新东西」最省。
interface TailTab {
  id: string
  path: string
  name: string
  lines: string[]
  received: number
  paused: boolean
}

// 环形缓冲上限：一条高速滚动的日志跑一晚上能到几百万行，
// 全部留在数组里内存会失控，而用户真正会回看的永远只有最近那一段。
const TAIL_MAX_LINES = 5000

const tailTabs = ref<TailTab[]>([])
const tailId = ref('')
const activeTab = computed(() => tailTabs.value.find((x) => x.id === tailId.value) || null)

async function pickTailFile() {
  let path = ''
  try {
    path = (unwrap<string>(await SelectLocalFile(t('ltPick'))) || '').trim()
  } catch (e: any) {
    setTailError(t('ltOpenFailed') + ': ' + getErrorMessage(e))
    return
  }
  if (!path) return
  if (tailTabs.value.some((x) => x.path === path)) {
    tailId.value = tailTabs.value.find((x) => x.path === path)!.id
    source.value = 'tail'
    return
  }
  try {
    const res = unwrap<any>(await LogTailOpen(path, TAIL_MAX_LINES))
    if (!res || !res.id) {
      setTailError(t('ltOpenFailed'))
      return
    }
    tailTabs.value.push({
      id: res.id,
      path: res.path,
      name: res.name,
      lines: res.lines || [],
      received: (res.lines || []).length,
      paused: false,
    })
    tailId.value = res.id
    source.value = 'tail'
    await nextTick()
    scrollToBottom()
  } catch (e: any) {
    setTailError(t('ltOpenFailed') + ': ' + getErrorMessage(e))
  }
}

async function closeTail(tab: TailTab) {
  const idx = tailTabs.value.findIndex((x) => x.id === tab.id)
  if (idx >= 0) tailTabs.value.splice(idx, 1)
  // 必须通知后端停掉轮询 goroutine：前端删掉标签只是不显示了，
  // 后端还会继续 stat 到进程退出（ServiceShutdown 才能收，那是几分钟以后的事）。
  try { unwrap(await LogTailStop(tab.id)) } catch (_) { /* ignore */ }
  if (tailId.value === tab.id) {
    tailId.value = tailTabs.value[0]?.id || ''
    if (!tailTabs.value.length) source.value = 'log'
  }
}

async function toggleTabPause(tab: TailTab) {
  tab.paused = !tab.paused
  try { unwrap(await LogTailPause(tab.id, tab.paused)) } catch (_) { /* ignore */ }
}

// 清空同样「点两次」：与本组件其它破坏性动作保持一致的手感。
const clearTailArmed = ref(false)
let clearTailTimer: ReturnType<typeof setTimeout> | null = null
function clearTail() {
  if (!clearTailArmed.value) {
    clearTailArmed.value = true
    if (clearTailTimer) clearTimeout(clearTailTimer)
    clearTailTimer = setTimeout(() => { clearTailArmed.value = false }, 3000)
    return
  }
  if (clearTailTimer) clearTimeout(clearTailTimer)
  clearTailArmed.value = false
  const tab = activeTab.value
  if (tab) tab.lines = []
}

const tailError = ref('')
function setTailError(msg: string) {
  tailError.value = msg
  setTimeout(() => { if (tailError.value === msg) tailError.value = '' }, 5000)
}

// 自动滚动只在「用户本来就贴着底部」时执行。
// 尾随时每 500ms 就可能来一批新行，无条件滚到底会让正在往上翻的用户永远翻不上去。
const stickBottom = ref(true)
function onBodyScroll() {
  const el = bodyEl.value
  if (!el) return
  stickBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

let offTail: (() => void) | null = null

function onTailLine(ev: any) {
  const p = ev?.data ?? ev
  if (!p || !p.id) return
  const tab = tailTabs.value.find((x) => x.id === p.id)
  if (!tab) return
  if (p.rotated) {
    // 轮转：后端已经重新对齐到新文件末尾，这里跟着换掉整屏，
    // 否则用户会看到「旧文件的尾巴 + 新文件的开头」拼在一起的错误上下文。
    tab.lines = p.lines || []
    setTailError(t('ltRotated'))
    if (isTail.value) scrollToBottom()
    return
  }
  const incoming = p.lines || []
  if (!incoming.length) return
  tab.lines = tab.lines.concat(incoming)
  tab.received += incoming.length
  if (tab.lines.length > TAIL_MAX_LINES) {
    tab.lines = tab.lines.slice(tab.lines.length - TAIL_MAX_LINES)
  }
  if (isTail.value && stickBottom.value) {
    nextTick(() => scrollToBottom())
  }
}

// 卸载必须逐个 LogTailStop：设置面板反复开关，每留一个会话就多一个常驻轮询，
// 而会话只有 ServiceShutdown 才统一收——那是几小时以后的事。
async function stopAllTails() {
  if (offTail) { offTail(); offTail = null }
  for (const tab of tailTabs.value) {
    try { unwrap(await LogTailStop(tab.id)) } catch (_) { /* ignore */ }
  }
  tailTabs.value = []
  tailId.value = ''
  if (source.value === 'tail') source.value = 'log'
}

const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null
// 复制统一走 utils/clipboard：后端 Win32 写入，不依赖 WebView2 的焦点/激活状态。
async function copyText(text: string) {
  if (!text) return
  const ok = await copyToClipboard(text)
  if (ok) {
    copied.value = true
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => { copied.value = false }, 1500)
  }
}
function copyAll() { copyText(displayLines.value.map((r) => r.raw).join('\n')) }
function copyLine(l: string) { copyText(l) }

onMounted(async () => {
  offTail = Events.On('logtail:line', onTailLine)
  await loadFiles()
  await loadStorage()
  await loadContent()
})
onUnmounted(() => {
  // stopAllTails 是异步的，卸载时不 await：停止请求已经发出，
  // 后端按 id 幂等处理，页面先消失也不影响结果。
  void stopAllTails()
  if (followTimer) clearInterval(followTimer)
  if (cleanTimer) clearTimeout(cleanTimer)
  if (toastTimer) clearTimeout(toastTimer)
  if (clearTailTimer) clearTimeout(clearTailTimer)
})
</script>

<template>
  <div class="log-viewer">
    <aside class="log-side">
      <div class="log-side-head">
        <ScrollText :size="14" />
        <span>{{ t('logFiles') }}</span>
      </div>
      <div class="log-file-list">
        <button
          v-for="f in logFiles"
          :key="f.name"
          :class="['log-file', { active: source === 'log' && f.name === selected }]"
          @click="selectFile(f.name, false)"
        >
          <span class="log-file-name" :title="f.name">{{ f.name }}</span>
          <span class="log-file-meta">{{ (f.size / 1024).toFixed(1) }} KB</span>
        </button>
        <p v-if="!logFiles.length" class="log-empty">{{ t('logEmpty') }}</p>
      </div>

      <div class="log-side-head crash-head">
        <AlertTriangle :size="14" />
        <span>{{ t('crashRecords') }}</span>
        <span v-if="crashFiles.length" class="crash-num">{{ crashFiles.length }}</span>
      </div>
      <div class="crash-list">
        <button
          v-for="c in crashFiles"
          :key="c.name"
          :class="['log-file', { active: source === 'crash' && c.name === selected }]"
          @click="selectFile(c.name, true)"
        >
          <span class="log-file-name" :title="c.name">
            <span class="crash-badge" :data-kind="c.kind">{{ c.kind }}</span>{{ c.name }}
          </span>
          <span class="log-file-meta">{{ c.time }} · {{ (c.size / 1024).toFixed(1) }} KB</span>
        </button>
        <p v-if="!crashFiles.length" class="log-empty">{{ t('crashEmpty') }}</p>
      </div>
      <div v-if="crashFiles.length" class="crash-actions">
        <button class="log-btn" :class="{ armed: clearArmed }" @click="clearCrash">
          <Trash2 :size="13" />
          {{ clearArmed ? t('crashClearConfirm') : t('crashClear') }}
        </button>
      </div>

      <!-- 实时尾随：与前两种来源并列的第三种来源。
           应用日志/崩溃是「读一次」，尾随是「订阅增长」，所以它有独立的
           打开入口（选任意本机文件）与会话生命周期（关闭标签要通知后端停轮询）。 -->
      <div class="log-side-head tail-head">
        <Radio :size="14" />
        <span>{{ t('ltSection') }}</span>
        <button class="tail-add" :title="t('ltPick')" @click="pickTailFile">
          <Plus :size="13" />
        </button>
      </div>
      <div class="crash-list">
        <div
          v-for="tab in tailTabs"
          :key="tab.id"
          :class="['log-file', 'tail-tab', { active: source === 'tail' && tab.id === tailId }]"
          @click="tailId = tab.id; source = 'tail'"
        >
          <span class="log-file-name" :title="tab.path">
            <span class="tail-dot" :class="{ paused: tab.paused }" /><span class="tail-name">{{ tab.name }}</span>
          </span>
          <span class="log-file-meta">
            {{ tab.paused ? t('ltPause') : t('ltWatching') }} · {{ t('ltReceived', { n: tab.received }) }}
          </span>
          <button
            class="tail-x"
            :title="t('ltClose')"
            @click.stop="closeTail(tab)"
          >
            <X :size="12" />
          </button>
        </div>
        <p v-if="!tailTabs.length" class="log-empty">{{ t('ltEmpty') }}</p>
      </div>

      <!-- 日志占用：读数 + 占比条 + 清理入口 -->
      <div class="storage-card">
        <div class="storage-head">
          <HardDrive :size="13" />
          <span class="storage-title">{{ t('logStorage') }}</span>
          <span v-if="storage" class="storage-total">{{ formatBytes(storage.totalSize) }}</span>
        </div>

        <template v-if="storage && storage.totalSize > 0">
          <div class="storage-bar" role="img" :aria-label="t('logStorage')">
            <div class="storage-seg storage-seg--log" :style="{ width: logPercent + '%' }" />
            <div class="storage-seg storage-seg--crash" :style="{ width: (100 - logPercent) + '%' }" />
          </div>
          <div class="storage-meta">
            <span>{{ t('logStorageFiles', { logs: storage.logCount, crashes: storage.crashCount }) }}</span>
            <span v-if="storage.oldest" class="storage-range">
              {{ storage.oldest === storage.newest ? storage.newest : storage.oldest + ' ~ ' + storage.newest }}
            </span>
          </div>
          <div class="storage-legend">
            <span class="lg lg--log">{{ t('logFiles') }} {{ formatBytes(storage.logSize) }}</span>
            <span class="lg lg--crash">{{ t('crashRecords') }} {{ formatBytes(storage.crashSize) }}</span>
          </div>
        </template>
        <div v-else class="storage-meta">{{ t('logStorageEmpty') }}</div>

        <div class="storage-actions">
          <button
            class="log-btn"
            :class="{ armed: cleanArmed === 'old' }"
            :title="t('logCleanupOldTitle')"
            @click="runCleanup('old')"
          >
            <Trash2 :size="12" />
            {{ cleanArmed === 'old' ? t('logCleanupConfirm') : t('logCleanupOld') }}
          </button>
          <button
            class="log-btn"
            :class="{ armed: cleanArmed === 'all' }"
            :title="t('logCleanupAllTitle')"
            @click="runCleanup('all')"
          >
            <Trash2 :size="12" />
            {{ cleanArmed === 'all' ? t('logCleanupConfirm') : t('logCleanupAll') }}
          </button>
        </div>
        <div v-if="toastMsg" class="storage-msg">{{ toastMsg }}</div>
      </div>
    </aside>

    <section class="log-main">
      <div class="log-toolbar">
        <div class="log-toolbar-left">
          <div class="log-levels" role="group" :aria-label="t('logFiles')">
            <button
              v-for="lv in ['I', 'W', 'E']"
              :key="lv"
              :class="['lv', lv, { off: !levels[lv] }]"
              :aria-pressed="levels[lv]"
              @click="levels[lv] = !levels[lv]"
            >{{ lv }}</button>
          </div>
          <div class="log-search">
            <Search :size="13" class="log-search-icon" />
            <input v-model="keyword" :placeholder="t('logSearch')" class="log-search-input" />
          </div>
          <label v-if="!isTail" class="log-follow">
            <input type="checkbox" :checked="follow" @change="toggleFollow" />
            {{ t('logFollow') }}
          </label>
          <!-- 尾随模式：读取的是订阅流而不是静态文件，所以不需要「刷新/跟随」，
               换成暂停（真的停止后端读取）与清空（丢弃已攒的行） -->
          <template v-else-if="activeTab">
            <button class="log-btn" @click="toggleTabPause(activeTab)">
              <Pause v-if="!activeTab.paused" :size="13" />
              <Play v-else :size="13" />
              {{ activeTab.paused ? t('ltResume') : t('ltPause') }}
            </button>
            <button class="log-btn" :class="{ armed: clearTailArmed }" @click="clearTail">
              <Trash2 :size="13" />
              {{ clearTailArmed ? t('ltClearConfirm') : t('ltClear') }}
            </button>
          </template>
        </div>
        <div class="log-toolbar-right">
          <span v-if="isTail && activeTab" class="log-count">
            {{ t('ltReceived', { n: activeTab.received }) }}
          </span>
          <span v-else-if="rawLines.length" class="log-count">
            {{ t('logLines', { shown: displayLines.length, total: rawLines.length }) }}
          </span>
          <button v-if="!isTail" class="log-btn" :disabled="loading" @click="loadContent">
            <RefreshCw :size="13" /> {{ t('logRefresh') }}
          </button>
          <button class="log-btn" @click="OpenLogsDir()">
            <FolderOpen :size="13" /> {{ t('logOpenDir') }}
          </button>
          <button class="log-btn" @click="copyAll" :title="t('logCopyLine')">
            <Copy v-if="!copied" :size="13" />
            <Check v-else :size="13" />
            {{ copied ? t('logCopied') : t('logCopy') }}
          </button>
        </div>
      </div>

      <div ref="bodyEl" class="log-body" @scroll.passive="onBodyScroll">
        <p v-if="tailError" class="log-empty tail-error">{{ tailError }}</p>
        <div v-if="isCrash && crashMeta" class="crash-meta" :data-kind="crashMeta.kind">
          <div class="crash-meta-head">
            <span class="crash-badge" :data-kind="crashMeta.kind">{{ crashMeta.kind }}</span>
            <span class="crash-meta-time">{{ crashMeta.time }}</span>
            <span class="crash-meta-label">{{ crashMeta.label }}</span>
          </div>
          <div class="crash-meta-msg">{{ crashMeta.message }}</div>
        </div>
        <div
          v-for="(row, i) in displayLines"
          :key="i"
          :class="['log-line-row', 'lv-' + row.level]"
        >
          <!-- 时间戳不做高亮：一列里出现一排小黄块会破坏对齐，
               而按时间搜（"21:06"）远少于按错误内容搜，收益不值这个代价 -->
          <span class="log-ts">{{ row.ts }}</span>
          <span class="log-lv">{{ row.level }}</span>
          <pre class="log-line"><span v-html="highlight(row.msg)"></span></pre>
          <button class="line-copy" :title="t('logCopyLine')" @click="copyLine(row.raw)">
            <Copy :size="12" />
          </button>
        </div>
        <p v-if="!displayLines.length" class="log-empty log-empty-body">
          {{ rawLines.length ? t('logNoMatch') : t('logEmpty') }}
        </p>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* 日志查看器
 *
 * 排版原则：一行三段（时间戳 / 级别 / 正文），信息密度靠对齐而不是靠颜色。
 * 时间戳弱化成参照物，级别独占一列承担信号，正文才是阅读主体——
 * 原来是整行同一个颜色，警告和错误埋在几千行里根本挑不出来。
 */
.log-viewer {
  display: flex; height: 100%; overflow: hidden;
  background: var(--color-bg-primary);
}

/* === 左侧文件列表 === */
.log-side {
  width: 216px; flex-shrink: 0;
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--color-bg-secondary);
  border-right: 1px solid var(--color-border);
}
/* 分组标题做成小节标签而非整条 bar：侧栏里有两个分组，双 bar 会显得像两个工具栏 */
.log-side-head {
  display: flex; align-items: center; gap: 6px; flex-shrink: 0;
  padding: 14px 14px 6px;
  font-size: 11px; font-weight: 600; letter-spacing: 0.06em;
  color: var(--color-text-muted);
  user-select: none;
}
.log-side-head svg { opacity: 0.75; flex-shrink: 0; }
.crash-head { border-top: 1px solid var(--color-border); margin-top: 8px; padding-top: 13px; }
.crash-num {
  margin-left: auto; min-width: 16px; padding: 0 5px;
  border-radius: var(--radius-full); text-align: center;
  font-size: 10px; font-weight: 700; font-variant-numeric: tabular-nums;
  background: var(--color-danger-bg); color: var(--color-danger-text);
}

.log-file-list {
  flex: 1 1 auto; min-height: 56px;
  overflow-y: auto; padding: 0 8px 8px;
}
/* 崩溃分组不抢主列表的空间：给上限，多出来的自己滚 */
.crash-list {
  flex: 0 1 auto; min-height: 0; max-height: 40%;
  overflow-y: auto; padding: 0 8px 8px;
}

.log-file {
  display: flex; flex-direction: column; gap: 3px;
  width: 100%; padding: 7px 9px; text-align: left;
  background: transparent; color: var(--color-text-primary);
  /* 透明边框占位：选中时才加边框，避免整行高度跳动 */
  border: 1px solid transparent; border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast),
              border-color var(--transition-fast);
}
.log-file:hover { background: var(--color-bg-hover); }
/* 选中用淡底 + 主色文字，不用实心色块：实心在暗色主题下过于抢眼，
   而且会让崩溃徽章失去对比（它只能反色成 inherit）。 */
.log-file.active {
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border);
  color: var(--color-accent);
}
/* 不能用 flex：徽章是行内元素，一旦容器变 flex，后面的文件名会变成匿名 flex item，
   容器的 text-overflow: ellipsis 就不再作用于它，长文件名会直接溢出。 */
.log-file-name {
  display: block;
  font-family: var(--font-mono); font-size: 11.5px; line-height: 1.5;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.log-file-meta {
  font-size: 10px; line-height: 1.4;
  color: var(--color-text-disabled); font-variant-numeric: tabular-nums;
}
.log-file.active .log-file-meta { color: var(--color-accent); opacity: 0.7; }

.crash-badge {
  display: inline-block; flex-shrink: 0;
  margin-right: 5px; padding: 0 4px; border-radius: var(--radius-xs);
  font-family: var(--font-family); font-size: 9.5px; font-weight: 700;
  line-height: 15px; letter-spacing: 0.02em; text-transform: uppercase;
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
}
.crash-badge[data-kind='panic'] { background: var(--color-danger-bg); color: var(--color-danger-text); }
.crash-badge[data-kind='js'] { background: var(--color-warning-bg); color: var(--color-warning); }
.log-file.active .crash-badge { background: transparent; color: inherit; }

.crash-actions { padding: 8px; border-top: 1px solid var(--color-border); flex-shrink: 0; }
.crash-actions .log-btn { width: 100%; justify-content: center; }
.crash-actions .log-btn.armed {
  border-color: var(--color-danger-border); color: var(--color-danger-text);
  background: var(--color-danger-bg);
}
.log-empty { padding: 12px 6px; font-size: 11.5px; color: var(--color-text-disabled); }

/* === 实时尾随 ===
   状态点刻意做成「呼吸」而不是静态圆点：尾随是一个持续进行的动作，
   静止的小圆点和「已停止」分不出来。暂停时彻底停掉动画——
   还在微微呼吸的话，用户会以为它仍在读。 */
.tail-head { border-top: 1px solid var(--color-border); margin-top: 8px; padding-top: 13px; }
.tail-add {
  margin-left: auto; display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; padding: 0;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-muted); cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}
.tail-add:hover { color: var(--color-accent); border-color: var(--color-accent); }

.tail-tab { position: relative; }
/* 名称行让出右侧 20px：关闭按钮是绝对定位的，不占栅格宽度，
   但文字必须提前避让，否则「hover 时按钮盖住文件名尾部」 */
.tail-tab .log-file-name { display: flex; align-items: center; gap: 5px; padding-right: 20px; }
/* ellipsis 必须落在自己的盒子上：flex 容器里的匿名文本不吃 text-overflow */
.tail-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tail-dot {
  flex-shrink: 0; width: 6px; height: 6px; border-radius: 50%;
  background: var(--color-success); animation: tail-pulse 1.6s ease-in-out infinite;
}
.tail-dot.paused { background: var(--color-text-disabled); animation: none; }
@keyframes tail-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.25; } }

.tail-x {
  position: absolute; top: 5px; right: 4px;
  display: inline-flex; align-items: center; justify-content: center;
  width: 18px; height: 18px; padding: 0; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-disabled); border: none; cursor: pointer;
  opacity: 0; transition: opacity var(--transition-fast), color var(--transition-fast);
}
.tail-tab:hover .tail-x { opacity: 1; }
.tail-x:hover { color: var(--color-danger-text); }
/* 键盘可达：只用 :hover 显示关闭按钮的话，键盘用户永远摸不到它 */
.tail-x:focus-visible { opacity: 1; }

.tail-error {
  margin: 0 0 8px; padding: 6px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-warning-bg); color: var(--color-warning);
}

/* === 日志占用卡片 ===
   钉在侧栏底部：它管的是「整个目录」而不是某一个文件，跟上面的文件列表是
   两件事，放一起会被误读成「当前选中文件占了多少」。 */
.storage-card {
  margin-top: auto; flex-shrink: 0;
  padding: 10px 12px 12px;
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.storage-head {
  display: flex; align-items: center; gap: 6px;
  font-size: 11px; color: var(--color-text-muted);
  margin-bottom: 8px;
}
.storage-title { font-weight: 600; letter-spacing: 0.04em; }
.storage-total {
  margin-left: auto;
  font-family: var(--font-mono, monospace); font-size: 11.5px;
  color: var(--color-text-primary);
}
.storage-bar {
  display: flex; height: 6px; width: 100%;
  border-radius: var(--radius-full, 999px);
  overflow: hidden; background: var(--color-bg-tertiary);
}
.storage-seg { height: 100%; }
.storage-seg--log { background: var(--color-accent); }
.storage-seg--crash { background: var(--color-danger); }
.storage-meta {
  display: flex; flex-direction: column; gap: 2px;
  margin-top: 6px; font-size: 10.5px; color: var(--color-text-muted);
}
.storage-range { color: var(--color-text-disabled); }
.storage-legend { display: flex; gap: 10px; margin-top: 6px; font-size: 10px; }
.lg { display: inline-flex; align-items: center; gap: 4px; color: var(--color-text-muted); }
/* 小色块用 ::before 而不是再画一个元素：这里只需要一个 6px 的点 */
.lg::before {
  content: ''; width: 6px; height: 6px; border-radius: 50%;
}
.lg--log::before { background: var(--color-accent); }
.lg--crash::before { background: var(--color-danger); }
.storage-actions { display: flex; gap: 6px; margin-top: 10px; }
.storage-actions .log-btn { flex: 1; justify-content: center; padding: 4px 6px; font-size: 10.5px; }
.storage-actions .log-btn.armed {
  border-color: var(--color-danger-border); color: var(--color-danger-text);
  background: var(--color-danger-bg);
}
.storage-msg {
  margin-top: 8px; padding: 5px 7px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
  font-size: 10.5px; color: var(--color-text-secondary);
  word-break: break-all;
}

/* === 右侧日志区 === */
.log-main { flex: 1; display: flex; flex-direction: column; overflow: hidden; min-width: 0; }

.log-toolbar {
  display: flex; align-items: center; gap: 10px 12px; flex-wrap: wrap;
  padding: 8px 14px; flex-shrink: 0;
  border-bottom: 1px solid var(--color-border);
}
/* 左侧「过滤条件」与右侧「动作」分组：中间留白把它们分开，
   否则七个控件排成一排，找按钮时得逐个读文字。 */
.log-toolbar-left { display: flex; align-items: center; gap: 10px; flex: 1 1 auto; min-width: 0; }
.log-toolbar-right { display: flex; align-items: center; gap: 8px; flex-shrink: 0; margin-left: auto; }
.log-count {
  font-size: 11px; color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums; white-space: nowrap; padding-right: 2px;
}

/* 级别开关：开着的按钮有淡底，关掉的透明且半透明——状态一眼可辨，
   比原来「都一样、只差 0.3 透明度」清楚 */
.log-levels {
  display: flex; gap: 2px; flex-shrink: 0; padding: 2px;
  border-radius: var(--radius-sm); background: var(--color-bg-secondary);
}
.lv {
  width: 22px; height: 20px; border: none; border-radius: var(--radius-xs);
  background: transparent; cursor: pointer;
  font-family: var(--font-mono); font-size: 11px; font-weight: 700;
  line-height: 1;
  transition: background var(--transition-fast), opacity var(--transition-fast);
}
.lv:not(.off) { background: var(--color-bg-tertiary); }
.lv.off { opacity: 0.32; }
.lv.I { color: var(--color-log-info); }
.lv.W { color: var(--color-warning); }
.lv.E { color: var(--color-log-error); }
.lv:hover { opacity: 1; }

.log-search {
  display: flex; align-items: center; gap: 6px;
  flex: 1 1 auto; min-width: 120px; max-width: 340px; height: 26px;
  padding: 0 9px;
  color: var(--color-text-disabled);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  transition: border-color var(--transition-fast);
}
.log-search:focus-within { border-color: var(--color-border-focus); }
.log-search-icon { flex-shrink: 0; }
.log-search-input {
  flex: 1; min-width: 0;
  border: none; outline: none; background: transparent;
  font-family: var(--font-family); font-size: 12px;
  color: var(--color-text-primary);
}

.log-follow {
  display: flex; align-items: center; gap: 5px; flex-shrink: 0;
  font-size: 12px; color: var(--color-text-muted);
  cursor: pointer; user-select: none;
  transition: color var(--transition-fast);
}
.log-follow:hover { color: var(--color-text-primary); }
/* 自绘复选框：项目没有全局 checkbox 样式，WebView2 在暗色主题下仍渲染成白底方块，
   与周围按钮明显割裂。这里不引 color-scheme（会影响整个页面），只就地画一个。 */
.log-follow input {
  appearance: none; -webkit-appearance: none;
  position: relative; width: 13px; height: 13px; margin: 0;
  border: 1px solid var(--color-border); border-radius: 3px;
  background: var(--color-bg-secondary); cursor: pointer;
  transition: background var(--transition-fast), border-color var(--transition-fast);
}
.log-follow input:checked {
  background: var(--color-accent); border-color: var(--color-accent);
}
.log-follow input:checked::after {
  content: ''; position: absolute; left: 3.5px; top: 0.5px;
  width: 4px; height: 7.5px;
  border: solid var(--color-accent-text);
  border-width: 0 1.5px 1.5px 0;
  transform: rotate(45deg);
}

.log-btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 5px;
  height: 26px; padding: 0 10px;
  font-family: var(--font-family); font-size: 12px; white-space: nowrap;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-muted);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast),
              border-color var(--transition-fast);
}
.log-btn:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.log-btn:disabled { opacity: 0.5; cursor: default; }

/* === 日志正文 === */
.log-body {
  flex: 1; overflow-y: auto; overflow-x: hidden;
  padding: 6px 0 12px;
  background: var(--color-bg-primary);
  font-family: var(--font-mono); font-size: 12px; line-height: 1.6;
}

.log-line-row {
  position: relative;
  display: grid;
  /* 时间戳列宽按 "2026-10-23 21:06:29.648" 在 11px 等宽下量出，再留一点余量；
     三列共用同一套栅格，栈追踪这类没有前缀的行也自动对齐到正文列。 */
  grid-template-columns: 154px 14px minmax(0, 1fr);
  align-items: start; column-gap: 8px;
  padding: 0 32px 0 12px;   /* 右侧 32px 给悬浮复制按钮留位，不压正文 */
  border-left: 2px solid transparent;
  transition: background var(--transition-fast);
}
.log-line-row:hover { background: var(--color-bg-hover); }
/* 警告/错误在左边缘留色条：滚动时余光就能看到异常位置，
   不用逐行读级别字母 */
.log-line-row.lv-W { border-left-color: var(--color-warning); }
.log-line-row.lv-E { border-left-color: var(--color-log-error); }

.log-ts {
  font-size: 11px; color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  font-variant-numeric: tabular-nums;
  user-select: text;
}
/* info 是基线，不上色：一旦整屏每行都带颜色，警告和错误就失去信号意义 */
.log-lv { font-size: 11px; font-weight: 700; text-align: center; color: var(--color-text-disabled); }
.lv-W .log-lv { color: var(--color-warning); }
.lv-E .log-lv { color: var(--color-log-error); }

.log-line {
  margin: 0;
  /* anywhere 而不是 break-all：日志里的路径、URL、堆栈符号需要在可断点处换行，
     break-all 会把整个标识符从中间劈开，反而更难读 */
  white-space: pre-wrap; overflow-wrap: anywhere;
  color: var(--color-text-primary);
}
/* 警告只染级别列和左色条，正文保持主色：
   整段黄字读起来比真正的错误还刺眼，反而抢走了「错误」的信号。
   错误行整段染红是刻意的——它就是要打断阅读。 */
.lv-E .log-line { color: var(--color-log-error); }

.line-copy {
  position: absolute; top: 1px; right: 8px;
  width: 20px; height: 20px;
  display: flex; align-items: center; justify-content: center; cursor: pointer;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-secondary); color: var(--color-text-disabled);
  opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast);
}
.log-line-row:hover .line-copy { opacity: 1; }
.line-copy:hover { color: var(--color-accent); border-color: var(--color-accent-border); }

.log-empty-body { padding: 48px 0; text-align: center; font-size: 12.5px; }

/* 崩溃现场头块：把 time/kind/label/message 从「正文堆」里拎出来做成卡片，
   下方堆栈每行才有统一的现场时间与级别上色。否则这些头字段会和堆栈混在一块，
   时间列要么空着要么在「time: ...」这种伪行上重复。 */
.crash-meta {
  margin: 6px 12px 10px; padding: 9px 12px;
  border: 1px solid var(--color-border);
  border-left: 3px solid var(--color-log-error);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}
.crash-meta[data-kind='js'] { border-left-color: var(--color-warning); }
.crash-meta-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.crash-meta-time {
  font-family: var(--font-mono); font-size: 11px; color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
.crash-meta-label {
  font-size: 11.5px; color: var(--color-text-secondary); font-weight: 600;
}
.crash-meta-msg {
  margin-top: 5px;
  font-family: var(--font-mono); font-size: 12px; line-height: 1.5;
  color: var(--color-text-primary); white-space: pre-wrap; overflow-wrap: anywhere;
}

.log-line :deep(mark) {
  background: var(--color-mark-bg); color: var(--color-mark-text);
  border-radius: var(--radius-xs); padding: 0 1px;
}
</style>
