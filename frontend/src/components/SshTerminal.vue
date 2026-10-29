<script setup lang="ts">
/**
 * SSH 终端视图（xterm 封装）。
 *
 * 职责边界：**只负责渲染与输入输出**，不负责建连、不负责会话生命周期。
 * 父组件先挂载本组件拿到尺寸，再调 Connect，最后把 sessionId 传进来。
 *
 * 这个顺序是刻意的：Connect 一返回，远端 shell 就会吐出 banner 和提示符，
 * 如果等 Connect 回来才挂监听，第一批输出会全部丢掉（表现为黑屏，直到用户敲键）。
 * 所以本组件挂载即订阅事件，把「还没 attach」期间收到的帧先缓存，
 * 等 sessionId 到位再按到达顺序补写。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronUp, Search, Sparkles, X, ZoomIn, ZoomOut } from '@lucide/vue'
import { Terminal, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { SearchAddon } from '@xterm/addon-search'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { Clipboard, Events } from '@wailsio/runtime'
import '@xterm/xterm/css/xterm.css'

import { Replay, Resize, Write } from '../../bindings/quickdock/services/sshx/sshservice'
import { base64ToBytes, bytesToBase64, textToBytes } from '../utils/base64'
import { copyToClipboard } from '../utils/clipboard'
import { createWriteQueue } from '../utils/termWriteQueue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useThemeName } from '../composables/useThemeName'
import { useToast } from '../composables/useToast'
import { debounce } from '../utils/debounce'
import { isMultiLinePaste } from '../utils/ssh'

/** 与 services/sshx/session.go 里的常量一一对应。 */
const EVENT_DATA = 'quickdock:ssh:data'
const EVENT_STATE = 'quickdock:ssh:state'

interface SshDataEvent {
  sessionId: string
  /** base64 编码的原始字节 */
  data: string
}

interface SshStateEvent {
  sessionId: string
  state: string
  error?: string
  errorCode?: string
}

interface ReplayResult {
  /** base64 编码的原始字节 */
  data: string
  truncated: boolean
  state: string
}

const props = withDefaults(
  defineProps<{
    /** 会话 ID。由父组件 Connect 之后传入；空串表示「终端已就绪但还没连上」。 */
    sessionId?: string
    /**
     * attach 时先拉取历史输出。
     *
     * 只用于「窗口重开、接回已有会话」的场景。首次连接必须保持 false：
     * 此时事件通道已经在推送，再回放一次会把同一段输出写两遍（提示符重复）。
     */
    replayOnAttach?: boolean
    fontSize?: number
    autoFocus?: boolean
  }>(),
  { sessionId: '', replayOnAttach: false, fontSize: 13, autoFocus: true },
)

const emit = defineEmits<{
  ready: [size: { cols: number; rows: number }]
  state: [event: SshStateEvent]
  error: [message: string]
  /** 多行粘贴待确认：数据原样带出，父组件弹窗确认后再决定是否发送 */
  paste: [data: string]
  /** 对选中内容提问 AI：带出选区文本，由父组件决定是否打开面板 */
  askAI: [selection: string]
}>()

const { t } = useI18n()
// 复制结果要当场看得见：成功/失败都给一条轻提示，否则「点了复制没反应」时
// 分不清是没选中、还是剪贴板被占用——这正是这个问题反复排查不下去的原因。
const { success: toastSuccess, error: toastError } = useToast()

// ---- 右键菜单（复制 / 粘贴 / 全选 / 清空） ----
//
// xterm.js 只负责「选中文字」，但默认**不会把选区写进系统剪贴板**，也没有任何
// 右键菜单——所以在 WebView 里选中一段输出后，用户既没法 Ctrl+C（那是发给远端的
// SIGINT），也没法右键复制，等于「看得见拿不走」。这里补一个轻量右键菜单 +
// 快捷键（Ctrl+Shift+C 复制 / Ctrl+Shift+V、Shift+Insert 粘贴 / Ctrl+Shift+A 问 AI），
// 与主流终端一致。
const menu = ref<{ x: number; y: number } | null>(null)

// ---- 字号（本地偏好） ----
//
// 声明了 `fontSize` prop 但父组件从不传，等于把这项能力焊死在 13px。xterm 要求改
// `options.fontSize` 后重新 fit（列数会变，必须同步 resized 给远端），所以调字号
// 不是纯 CSS 改动，只能走这里的统一入口。存 localStorage 是因为它属于"这台机器
// 这台屏幕"的偏好，不该进数据库跟着账号走。
const FONT_SIZE_KEY = 'qd_ssh_fontsize'
const FONT_SIZE_MIN = 10
const FONT_SIZE_MAX = 24

function readFontSize(): number {
  try {
    const v = Number(localStorage.getItem(FONT_SIZE_KEY))
    if (Number.isFinite(v) && v >= FONT_SIZE_MIN && v <= FONT_SIZE_MAX) return v
  } catch { /* localStorage 不可用时用默认值 */ }
  return props.fontSize || 13
}

const fontSizePx = ref(readFontSize())

function applyFontSize(next: number) {
  const size = Math.min(FONT_SIZE_MAX, Math.max(FONT_SIZE_MIN, Math.round(next)))
  if (size === fontSizePx.value && term?.options.fontSize === size) return
  fontSizePx.value = size
  try { localStorage.setItem(FONT_SIZE_KEY, String(size)) } catch { /* ignore */ }
  if (!term) return
  term.options.fontSize = size
  // fit 之后再触发一次 resize 同步：只改 options 不改窗口时，远端仍按旧列数换行，
  // 表现为"字变大了但 vim 还是按窄屏排版"。
  fitAddon?.fit()
  closeMenu()
}

function openMenu(e: MouseEvent) {
  // 右键时兜底再快照一次：正常路径已在 onSelectionChange 捕获，这里防的是个别
  // WebView2 版本 selectionchange 时机差异、而此刻选区其实还在的情况。
  if (term && term.hasSelection()) lastSelection.value = term.getSelection()
  // 超出视口时往回收，避免菜单被屏幕边缘裁掉点不到。
  // 底部预留 260px：菜单每加一项约增高 30px，预留不足会正好压住最后一项。
  const x = Math.min(e.clientX, window.innerWidth - 180)
  const y = Math.min(e.clientY, window.innerHeight - 260)
  menu.value = { x, y }
}

function closeMenu() {
  if (!menu.value) return
  menu.value = null
  // 菜单项是 <button>，点过之后焦点会留在按钮上（按钮随后被移除，焦点掉到 body）。
  // 那样接下来的 Ctrl+V / 直接敲字都不会进到终端，表现为「关掉菜单后还得先点一下终端」。
  // 菜单一关就把焦点还给 xterm。
  term?.focus()
}

/**
 * 写系统剪贴板。
 *
 * 统一走 utils/clipboard（→ 后端 CopyText → platform 的 Win32 写入）：不依赖
 * WebView2 的安全上下文 / 文档焦点 / 用户激活，且会如实返回成败。
 * 旧实现在这里调 @wailsio/runtime 的 Clipboard.SetText，它内部把写入同步派发到
 * 主线程、失败还只能靠 navigator.clipboard 兜底，而那条兜底在 WebView2 里
 * 「选中了却复制不走」正是本函数要解决的原始问题。
 */
async function copyText(text: string): Promise<boolean> {
  return copyToClipboard(text)
}

/**
 * 读系统剪贴板。
 *
 * 同样走 Wails 原生桥（Clipboard.Text），避免 WebView2 对 readText 的授权/安全上下文
 * 拦截——那正是终端「粘贴没反应」的根因。调用方（onPaste）拿到空串会静默处理。
 */
async function readClipboardText(): Promise<string> {
  try {
    return (await Clipboard.Text()) || ''
  } catch {
    // 退回 Web 标准方案
  }
  try {
    if (navigator.clipboard && window.isSecureContext) {
      return await navigator.clipboard.readText()
    }
  } catch {
    /* ignore */
  }
  return ''
}

async function onCopy() {
  // 取值口径：快照优先（右键/点击会清掉实时选区），再兜底实时选区。
  const text = currentSelection()
  closeMenu()
  if (!text) {
    // 走到这里说明快照和实时选区都空了。给失败反馈，别让「点了复制」静默无反应。
    toastError(t('copyFailed'))
    return
  }
  const ok = await copyText(text)
  if (ok) toastSuccess(t('copied'))
  else toastError(t('copyFailed'))
}

async function onPaste() {
  const text = await readClipboardText()
  closeMenu()
  if (!text) {
    // 读不到不能静默：用户按了 Ctrl+V 却什么都没发生，和「功能坏了」无法区分。
    toastError(t('sshPasteEmpty'))
    return
  }
  // 显式粘贴：不走 isMultiLinePaste 确认（用户主动操作），
  // 直接经与按键相同的发送路径发往远端。
  sendInput(text)
}

function onSelectAll() {
  term?.selectAll()
  closeMenu()
}

/**
 * 对选中内容提问 AI。
 *
 * 选区文本要做规整：xterm 的 getSelection 对跨行选取会保留每行的**尾随空格**
 * （补齐到终端宽度），并且整块包裹的长行在换行处不会补回车——直接喂给模型的话，
 * 既浪费 token，也会让日志/堆栈的行边界错位，模型因此更容易读错报错位置。
 */
function onAskAI() {
  // 优先用上次选区成立的快照（onSelectionChange 捕获，不依赖点击此刻的实时选区，
  // 否则右键/菜单交互可能已把选区清掉，拿到空串）。若快照为空但此刻确有选区，
  // 再退回读一次实时选区兜底。
  const raw = currentSelection()
  if (!raw.trim()) {
    closeMenu()
    return
  }
  const lines = raw.split('\n').map((l) => l.trimEnd())
  // 去掉首尾空行：中间的细节才是现场，边缘空白对模型没有信息量。
  while (lines.length && lines[0].trim() === '') lines.shift()
  while (lines.length && lines[lines.length - 1].trim() === '') lines.pop()
  const text = lines.join('\n')
  if (!text) {
    closeMenu()
    return
  }
  emit('askAI', text)
  closeMenu()
}

function onClear() {
  term?.clear()
  closeMenu()
}

/**
 * 宿主层快捷键 —— **只放「不影响发往远端字节流」的键**。
 *
 * 复制 / 粘贴 / 问 AI 一律不在这里：它们必须赶在 xterm 之前拦，见下面
 * `term.attachCustomKeyEventHandler`（这里的 keydown 是冒泡阶段，
 * 等它跑起来 xterm 早已把 \x03、\x16 等控制码发给远端了）。
 */
function onKeydown(e: KeyboardEvent) {
  const mod = e.ctrlKey || e.metaKey
  const k = e.key
  // Ctrl/Cmd+F：终端内搜索。必须拦，否则 Ctrl+F 会发给远端
  // （readline 里它是「光标前移一格」，用户按了只会看到光标走一步，更迷惑）。
  if (mod && !e.shiftKey && (k === 'f' || k === 'F')) {
    e.preventDefault()
    openSearch()
    return
  }
  // Esc 只在搜索条开着时才归我们处理，其余情况原样发给远端（vim / less 都要它）。
  if (k === 'Escape' && searchOpen.value) {
    e.preventDefault()
    closeSearch()
    return
  }
}

// ---- 文档级监听：点菜单外 / 滚动 / Esc 关闭 ----
function onDocMouseDown(e: MouseEvent) {
  if (!menu.value) return
  const el = e.target as HTMLElement | null
  if (!el || !el.closest('.term-menu')) closeMenu()
}

function onDocKeydown(e: KeyboardEvent) {
  // 捕获阶段拦截：菜单开着时 Esc 只关菜单，别漏给终端（否则会把 Esc 发到远端）。
  if (menu.value && e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    closeMenu()
  }
}

function onDocScroll() {
  if (menu.value) closeMenu()
}

const hostEl = ref<HTMLElement | null>(null)
/** 用普通变量而非 ref 持有实例：xterm 对象一旦被 Vue 代理，内部大量
 *  getter / 私有字段会变慢，也容易在响应式依赖里被误追踪。 */
let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let searchAddon: SearchAddon | null = null

// ---- 终端内搜索（Ctrl+F）----
const searchOpen = ref(false)
const searchText = ref('')
/** 第几个 / 共几个。index 从 0 起；命中数超过插件的 highlightLimit 时给 -1 */
const searchScore = ref<{ index: number; count: number }>({ index: -1, count: 0 })
const searchInputEl = ref<HTMLInputElement | null>(null)

const searchCountText = computed(() => {
  if (!searchText.value) return ''
  const { index, count } = searchScore.value
  if (count <= 0) return t('sshTermSearchNone')
  // index < 0 表示「命中了但多到不再单独计数」，此时只报总数，不编一个假序号。
  return index < 0 ? String(count) : `${index + 1}/${count}`
})

/** 高亮色从 token 读：写死色值会让浅色主题下出现一坨看不清的深色块。 */
function searchDecorations() {
  const match = cssVar('--color-terminal-search-match')
  const active = cssVar('--color-terminal-search-active')
  return {
    matchBackground: match,
    matchOverviewRuler: match,
    activeMatchBackground: active,
    activeMatchColorOverviewRuler: active,
  }
}

function runSearch(dir: 'next' | 'prev', incremental = false) {
  if (!searchAddon) return
  const kw = searchText.value
  if (!kw) {
    searchAddon.clearDecorations()
    searchScore.value = { index: -1, count: 0 }
    return
  }
  // 大小写不敏感：终端里出现宏名/路径/报错，用户不会记得当初打的是哪种大小写。
  const opts = { caseSensitive: false, incremental, decorations: searchDecorations() }
  if (dir === 'next') searchAddon.findNext(kw, opts)
  else searchAddon.findPrevious(kw, opts)
}

function openSearch() {
  searchOpen.value = true
  // 打开即全选上次的词：连着搜第二个词时，直接打字就覆盖了，不用先清空。
  void nextTick(() => {
    searchInputEl.value?.focus()
    searchInputEl.value?.select()
  })
}

function closeSearch() {
  if (!searchOpen.value) return
  searchOpen.value = false
  // 必须清装饰：否则关掉搜索条后满屏高亮还留着，看起来像选中了一大片。
  searchAddon?.clearDecorations()
  searchScore.value = { index: -1, count: 0 }
  term?.focus()
}

/**
 * 最近一次「非空选区」的快照。
 *
 * 为什么需要它：xterm 的选区是**实时**的，`term.getSelection()` 在右键菜单被点击
 * 的那一刻经常已经是空串——xterm 会在用户点击、输入、或右键时清掉选区
 * （见 xterm 源码 `onUserInput(() => { this.hasSelection && this.clearSelection() })`），
 * 而 WebView2 里右键 contextmenu 的时机更不可控，选区说没就没。
 *
 * 所以不能「点击复制/问 AI 时再读一次 getSelection()」，而要在选区**成立的瞬间**
 * （onSelectionChange 回调）就把它存下来，并且**选区被清除时也不清空**——
 * 这样右键菜单弹出、用户点「复制」的那一刻，按钮仍能用这份快照。
 *
 * 做成 ref 是必须的：「复制 / 问 AI」按钮的 disabled 直接绑它。若按钮可用态只认
 * 实时选区（term.hasSelection()），右键一清选区按钮立刻变灰 → 用户点「复制」
 * 毫无反应，这正是「右键里复制不生效」的根因。
 */
const lastSelection = ref('')

/**
 * 当前可用的选区文本 —— 复制与问 AI 的**唯一取值口径**。
 *
 * 必须优先用快照：现读 `getSelection()` 在「右键菜单点下去」「Ctrl+Shift+C 按下」
 * 这些时机经常已经是空串（xterm 会在用户输入/点击终端时清选区，WebView2 的
 * contextmenu 时机更不可控）。没有快照时再退回实时选区兜底。
 */
function currentSelection(): string {
  if (lastSelection.value) return lastSelection.value
  return term && term.hasSelection() ? term.getSelection() : ''
}

/**
 * 是否存在可复制/可提问的内容。
 *
 * 读快照 ref（不是实时选区）：右键/点击会清掉实时选区，若这里读 `hasSelection()`
 * 按钮会在用户点它的瞬间变灰，表现为「复制点了没反应」。快照一经选中就保留，
 * 按钮可用态因此稳定。
 */
function canCopy(): boolean {
  return lastSelection.value !== ''
}

// ---- 写入队列（rAF 分片 + 背压） ----
//
// 具体策略在 utils/termWriteQueue.ts（可单测），这里只负责把 xterm 接上去。
const writeQueue = createWriteQueue((chunk) => term?.write(chunk))

// ---- attach 之前的帧缓存 ----
const PENDING_LIMIT = 2 << 20
let pending: Array<{ sid: string; bytes: Uint8Array }> = []
let pendingBytes = 0
let pendingOverflow = false

// ---- 回放期间到达的实时帧（必须先写回放，再写它们） ----
let replaying = false
let duringReplay: Uint8Array[] = []

let errorNotified = false

// ---- 尺寸同步 ----
let resizeChain: Promise<void> = Promise.resolve()
let lastSentSize = ''

// ---- 输入串行化（保证按键顺序） ----
let inputChain: Promise<void> = Promise.resolve()

function enqueue(bytes: Uint8Array) {
  writeQueue.enqueue(bytes)
}

function reportError(err: unknown) {
  if (errorNotified) return
  errorNotified = true
  emit('error', getErrorMessage(err))
}

// ---- 主题：从 CSS token 读实际值 ----
//
// xterm 的 theme 是 JS 对象，只认具体色值。直接写死 `#1b1b1b` 会绕开主题体系
// （浅色主题下终端仍是黑的），所以必须每次从 getComputedStyle 读 token 实际值。
function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function readTermTheme(): ITheme {
  return {
    background: cssVar('--color-terminal-bg'),
    foreground: cssVar('--color-terminal-fg'),
    cursor: cssVar('--color-terminal-cursor'),
    cursorAccent: cssVar('--color-terminal-bg'),
    selectionBackground: cssVar('--color-terminal-selection'),
    black: cssVar('--color-terminal-black'),
    red: cssVar('--color-terminal-red'),
    green: cssVar('--color-terminal-green'),
    yellow: cssVar('--color-terminal-yellow'),
    blue: cssVar('--color-terminal-blue'),
    magenta: cssVar('--color-terminal-magenta'),
    cyan: cssVar('--color-terminal-cyan'),
    white: cssVar('--color-terminal-white'),
    brightBlack: cssVar('--color-terminal-bright-black'),
    brightRed: cssVar('--color-terminal-bright-red'),
    brightGreen: cssVar('--color-terminal-bright-green'),
    brightYellow: cssVar('--color-terminal-bright-yellow'),
    brightBlue: cssVar('--color-terminal-bright-blue'),
    brightMagenta: cssVar('--color-terminal-bright-magenta'),
    brightCyan: cssVar('--color-terminal-bright-cyan'),
    brightWhite: cssVar('--color-terminal-bright-white'),
  }
}

const themeName = useThemeName()
watch(
  themeName,
  async () => {
    // 热更新，不重建终端：重建会丢掉当前画面与回滚内容。
    if (!term) return
    // 等一拍再读 token，避免切主题瞬间读到中间态的旧值
    await nextTick()
    term.options.theme = readTermTheme()
    // 强制重绘已渲染的画面，否则背景/文字会残留旧主题色（表现为「切了还是深色」）
    term.refresh(0, term.rows - 1)
  },
)

// ---- attach（拿到 sessionId 之后） ----

async function attach(sid: string) {
  if (!term) return
  errorNotified = false
  lastSentSize = ''

  // 先补写「挂载 → Connect 返回」这段窗口内到达的帧。
  // 这里是同步执行的，事件回调不可能插进来，所以顺序天然正确。
  for (const frame of pending) {
    if (frame.sid === sid) enqueue(frame.bytes)
  }
  pending = []
  pendingBytes = 0

  if (props.replayOnAttach) {
    replaying = true
    try {
      const r = unwrap<ReplayResult>(await Replay(sid))
      if (r?.data && term) {
        term.reset() // 重开窗口 / 切会话：先清屏，避免旧画面与新回放叠加
        enqueue(base64ToBytes(r.data))
      }
    } catch (e) {
      reportError(e)
    } finally {
      replaying = false
    }
    const buffered = duringReplay
    duringReplay = []
    for (const bytes of buffered) enqueue(bytes)
  }

  if (pendingOverflow) {
    // 缓冲被丢过一部分：说明这段空窗期输出太多，画面必然不完整，
    // 提示用户重新连一次比留一个残缺的屏幕更好。
    pendingOverflow = false
    emit('state', { sessionId: sid, state: 'incomplete' })
  }

  await syncResize()
  if (props.autoFocus) term.focus()
}

function syncResize(cols?: number, rows?: number) {
  const sid = props.sessionId
  if (!sid || !term) return Promise.resolve()

  const c = cols ?? term.cols
  const r = rows ?? term.rows
  if (c <= 0 || r <= 0) return Promise.resolve()
  // 尺寸没变就不发：FitAddon 会在每次 fit 时触发 onResize，
  // 无脑透传会变成每拖一下窗口都往远端发一次 window-change。
  const sig = `${c}x${r}`
  if (sig === lastSentSize) return Promise.resolve()
  lastSentSize = sig

  resizeChain = resizeChain.then(async () => {
    try {
      unwrap(await Resize(sid, c, r))
    } catch {
      // 会话已断开时必定失败，这不是异常路径（状态事件会告诉 UI），
      // 静默即可，避免每次拖窗口都弹一次错误。
      lastSentSize = ''
    }
  })
  return resizeChain
}

/** 把按键发给远端。串行链保证「快敲」和粘贴的字节顺序不会被打乱。 */
function sendInput(data: string) {
  const sid = props.sessionId
  if (!sid || !data) return
  const b64 = bytesToBase64(textToBytes(data))
  inputChain = inputChain.then(async () => {
    try {
      unwrap(await Write(sid, b64))
    } catch (e) {
      reportError(e)
    }
  })
}

/**
 * 输入分流：多行内容交回父组件确认，单行直接发。
 *
 * 必须在**发送之前**判断。判据见 utils/ssh.ts 的 isMultiLinePaste——
 * 终端里回车键发出的是 \r，不能简单按「含不含换行」判断，
 * 否则用户每敲一次回车都会弹确认框。
 */
function handleInput(data: string) {
  if (!data) return
  if (isMultiLinePaste(data)) {
    emit('paste', data)
    return
  }
  sendInput(data)
}

// ---- 生命周期 ----

let offData: (() => void) | null = null
let offState: (() => void) | null = null
let observer: ResizeObserver | null = null

onMounted(async () => {
  if (!hostEl.value) return

  term = new Terminal({
    // 字体名必须写真实的字体族，不能用 var(--font-mono)：
    // xterm 要在 canvas 上量字符宽度，CSS 变量在这个上下文里无效，
    // 拿不到度量就会退回默认字体，导致光标位置与渲染错位。
    fontFamily: "Consolas, 'Cascadia Mono', 'Microsoft YaHei Mono', 'Noto Sans Mono CJK SC', monospace",
    fontSize: fontSizePx.value,
    lineHeight: 1.2,
    cursorBlink: true,
    scrollback: 5000,
    // Unicode11Addon 依赖 term.unicode，那是 proposed API：不开这个开关，
    // loadAddon 内部访问 unicode 会直接抛 _checkProposedApi 错误。
    // 异常发生在 mounted 里，会中断整个挂载流程——终端不渲染、事件不订阅，
    // 表现为「点了主机右侧一片空白」，而不是某个功能缺失，很难定位。
    allowProposedApi: true,
    theme: readTermTheme(),
  })

  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  // 中文字符宽度判定：不开这个，中文会按 1 列算，整行右移错位。
  //
  // 包 try/catch 而不是让它裸奔：这是纯增强项，最坏情况只是宽字符算窄，
  // 不该因为它让整个终端起不来。加载失败时 term.open 仍要继续执行。
  try {
    term.loadAddon(new Unicode11Addon())
    term.unicode.activeVersion = '11'
  } catch (e) {
    console.warn('[SshTerminal] Unicode11 addon 不可用，中文宽字符可能错位:', e)
  }

  // 搜索插件：只在终端内找、不碰远端（Ctrl+F 在 readline 里是「前移一个字符」，
  // 用户按它几乎只是想找屏幕上的东西）。
  //
  // 用 onDidChangeResults 拿「第几个 / 共几个」的前提是**开启 decorations**，
  // 关掉高亮时该事件不触发，计数会永远停在 0。所以 findNext 每次都带 decorations。
  searchAddon = new SearchAddon()
  term.loadAddon(searchAddon)
  searchAddon.onDidChangeResults(({ resultIndex, resultCount }) => {
    searchScore.value = { index: resultIndex, count: resultCount }
  })

  term.open(hostEl.value)
  await nextTick()
  fitAddon.fit()

  // 选区成立的瞬间就快照下来（见 lastSelection 注释）。只在「非空」时更新，
  // 选区被清除时不动它——这样右键问 AI 仍能拿到上一段被选中的内容，
  // 而不会拿到一个空值。
  term.onSelectionChange(() => {
    if (term && term.hasSelection()) {
      lastSelection.value = term.getSelection()
    }
    // 注意：这里**不要**在选区被清空时把 lastSelection 置空。右键菜单弹出、
    // 点击终端其它位置都会瞬间清掉实时选区，此时若把快照也清掉，「复制 / 问 AI」
    // 按钮会在用户点它的那一刻变灰——正是「右键里复制不生效」的根因。快照保留，
    // 复制随时可用，直到下一次选中覆盖它。
  })

  // 终端里的复制/粘贴/问 AI 快捷键全部在这里拦：
  // host 上的 keydown 是冒泡阶段，跑在 xterm 自己的 keydown **之后**——那时 xterm 已经把
  //   Ctrl+C 当 \x03(SIGINT)、Ctrl+V 当 \x16(quoted-insert)、Ctrl+Insert 当其它控制码
  // 发给远端了，再 preventDefault 也收不回来。用户看到的正是「Ctrl+V 粘不出来，终端里
  // 反而冒出一个 ^V」。attachCustomKeyEventHandler 在 xterm 判定按键**之前**执行，
  // 返回 false 才能真正把键吞掉。
  //
  // 同时 stopPropagation：本键已由这里处理，别再让 host 的 onKeydown 处理第二遍
  // （否则一次 Ctrl+V 会粘贴两次）。
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== 'keydown') return true
    const mod = e.ctrlKey || e.metaKey
    const k = e.key
    const keyOf = (code: string, ...aliases: string[]) => e.code === code || aliases.includes(k)
    const swallow = () => {
      e.preventDefault()
      e.stopPropagation()
      return false
    }
    const sel = !!term && term.hasSelection()

    // 普通 Ctrl+C：**有可见选区才复制**，没有选区必须放行 → 照常发 SIGINT，
    // 否则「中断正在跑的命令」这个最常用的操作就没了。
    if (mod && !e.shiftKey && !e.altKey && keyOf('KeyC', 'c', 'C')) {
      if (sel) {
        void onCopy()
        return swallow()
      }
      return true
    }
    // Ctrl+Shift+C / Ctrl+Insert：复制（无选区时 onCopy 会给出失败提示）
    if ((mod && e.shiftKey && keyOf('KeyC', 'c', 'C')) || (mod && keyOf('Insert'))) {
      void onCopy()
      return swallow()
    }
    // Ctrl+V / Ctrl+Shift+V / Shift+Insert：粘贴
    if ((mod && !e.altKey && keyOf('KeyV', 'v', 'V')) || (!mod && e.shiftKey && keyOf('Insert'))) {
      void onPaste()
      return swallow()
    }
    // Ctrl+Shift+A：对选中内容提问 AI；无选区不拦，留给宿主/浏览器。
    if (mod && e.shiftKey && keyOf('KeyA', 'a', 'A') && sel) {
      onAskAI()
      return swallow()
    }
    return true
  })

  term.onData(handleInput)
  term.onResize(({ cols, rows }) => {
    void syncResize(cols, rows)
  })

  // 订阅必须在 Connect 之前完成，否则第一批输出（banner / 提示符）会丢。
  // 注意：Wails 的回调参数是事件信封 { name, data }，事件负载在 .data 里。
  offData = Events.On(EVENT_DATA, (ev: any) => {
    const frame = ev?.data as SshDataEvent | undefined
    if (!frame?.sessionId || !frame.data) return

    // 先确认这帧是本会话的，再解码。
    //
    // 每个标签一个终端实例、各订阅一次全局事件，N 个标签就是 N 个回调；
    // 若把 base64 解码放在归属判断之前，每一帧都会被所有实例各解一次
    // （每次都分配一个 Uint8Array）——实际需要量的 N 倍，GC 压力同样 N 倍。
    // 唯一例外是尚未拿到 sessionId 时（连接还没返回），那才需要入 pending。
    if (props.sessionId && frame.sessionId !== props.sessionId) return
    const bytes = base64ToBytes(frame.data)

    if (!props.sessionId) {
      if (pendingBytes + bytes.byteLength > PENDING_LIMIT) {
        pendingOverflow = true
        return
      }
      pending.push({ sid: frame.sessionId, bytes })
      pendingBytes += bytes.byteLength
      return
    }
    if (replaying) {
      duringReplay.push(bytes)
      return
    }
    enqueue(bytes)
  })

  // 状态事件全部转发，由父组件按 sessionId 过滤：
  // 会话有可能在 Connect 返回前就断开，这里若也按 props 过滤，那条通知会被丢掉。
  offState = Events.On(EVENT_STATE, (ev: any) => {
    const state = ev?.data as SshStateEvent | undefined
    if (state?.sessionId) emit('state', state)
  })

  observer = new ResizeObserver(
    debounce(() => {
      if (!term || !fitAddon) return
      fitAddon.fit()
    }, 120),
  )
  observer.observe(hostEl.value)

  // 右键菜单的文档级关闭监听：点菜单外 / 滚动 / Esc。
  // 用捕获阶段挂 Esc，确保菜单开时 Esc 不会漏给终端。
  document.addEventListener('mousedown', onDocMouseDown)
  document.addEventListener('keydown', onDocKeydown, true)
  document.addEventListener('scroll', onDocScroll, true)

  emit('ready', { cols: term.cols, rows: term.rows })
  if (props.sessionId) await attach(props.sessionId)
  else if (props.autoFocus) term.focus()
})

watch(
  () => props.sessionId,
  async (sid, old) => {
    if (!sid || sid === old) return
    await attach(sid)
  },
)

watch(
  () => props.fontSize,
  (size) => {
    // 父组件显式传值时才跟随；内部调过的字号优先（用户刚点过"放大"，不该被默认值覆盖）。
    if (size && size !== fontSizePx.value) applyFontSize(size)
  },
)

onBeforeUnmount(() => {
  offData?.()
  offState?.()
  observer?.disconnect()
  observer = null
  document.removeEventListener('mousedown', onDocMouseDown)
  document.removeEventListener('keydown', onDocKeydown, true)
  document.removeEventListener('scroll', onDocScroll, true)
  writeQueue.dispose()
  pending = []
  duringReplay = []
  // 只销毁渲染器，不关会话：会话常驻 Go 侧，关窗后重开要能接回来。
  term?.dispose()
  term = null
  searchAddon = null
})

// ---- 屏显取文本（供 AI 面板当上下文）----
//
// 从 xterm 的 buffer 读而不是让后端去取 Replay：Replay 是原始字节，
// 里面全是 ANSI 转义序列，直接喂模型只会得到乱码；buffer 里是已渲染的纯文本。
//
// 只取「当前视口 + 往上多留一屏」：再往上是滚动历史，对"刚才那条命令报了什么错"
// 这类问题没有帮助，却会让 token 费用与无关噪声一起翻倍。
function readScreenText(): string {
  if (!term) return ''
  const buf = term.buffer.active
  // baseY 是当前视口最上方的行号；多往上取一屏，覆盖"命令 + 输出 + 报错"完整一段。
  const top = Math.max(0, buf.baseY - term.rows)
  const lines: string[] = []
  for (let y = top; y < buf.baseY + term.rows; y++) {
    const line = buf.getLine(y)
    lines.push(line ? line.translateToString(true) : '')
  }
  // 去掉首尾空行：模型看到几十行空白会以为上下文被截断了，反而多问一轮。
  while (lines.length && lines[0].trim() === '') lines.shift()
  while (lines.length && lines[lines.length - 1].trim() === '') lines.pop()
  return lines.join('\n')
}

defineExpose({
  focus: () => term?.focus(),
  fit: () => fitAddon?.fit(),
  getSize: () => ({ cols: term?.cols ?? 80, rows: term?.rows ?? 24 }),
  clear: () => term?.clear(),
  /** 用户确认多行粘贴后由父组件调用，走与按键完全相同的发送路径。 */
  send: sendInput,
  /** 取当前屏幕的纯文本，供 AI 面板当作上下文。 */
  readScreen: readScreenText,
})
</script>

<template>
  <div class="ssh-term">
    <div
      ref="hostEl"
      class="ssh-term-host"
      @contextmenu.prevent="openMenu"
      @keydown="onKeydown"
    />

    <!-- 搜索条绝对定位于右上角：不参与 flex 布局，免得把终端渲染宽度挤窄
         （挤窄会触发 fit → cols 变化 → 远端 PS1 重排，画面来回跳）。 -->
    <div v-if="searchOpen" class="term-search" @mousedown.stop>
      <Search :size="12" class="term-search-icon" />
      <input
        ref="searchInputEl"
        v-model="searchText"
        class="term-search-input"
        :placeholder="t('sshTermSearch')"
        spellcheck="false"
        @input="runSearch('next', true)"
        @keydown.enter.exact.prevent="runSearch('next')"
        @keydown.shift.enter.prevent="runSearch('prev')"
        @keydown.esc.prevent.stop="closeSearch"
      />
      <span class="term-search-count">{{ searchCountText }}</span>
      <button
        class="term-search-btn"
        :title="t('sshTermSearchPrev')"
        @click="runSearch('prev')"
      >
        <ChevronUp :size="12" />
      </button>
      <button
        class="term-search-btn"
        :title="t('sshTermSearchNext')"
        @click="runSearch('next')"
      >
        <ChevronDown :size="12" />
      </button>
      <button class="term-search-btn" :title="t('cancel')" @click="closeSearch">
        <X :size="12" />
      </button>
    </div>

    <Teleport to="body">
      <div
        v-if="menu"
        class="term-menu"
        :style="{ left: menu.x + 'px', top: menu.y + 'px' }"
        @click.stop
        @contextmenu.prevent
      >
        <button class="term-menu-item" :disabled="!canCopy()" @click="onCopy">
          <span>{{ t('sshCtxCopy') }}</span>
          <span class="term-menu-kbd">Ctrl+Shift+C</span>
        </button>
        <button class="term-menu-item ask-ai" :disabled="!canCopy()" @click="onAskAI">
          <span class="mi-lead">
            <Sparkles :size="13" />
            <span>{{ t('sshCtxAskAI') }}</span>
          </span>
          <span class="term-menu-kbd">Ctrl+Shift+A</span>
        </button>
        <button class="term-menu-item" @click="onPaste">
          <span>{{ t('sshCtxPaste') }}</span>
          <span class="term-menu-kbd">Ctrl+Shift+V</span>
        </button>
        <button class="term-menu-item" @click="onSelectAll">
          <span>{{ t('sshCtxSelectAll') }}</span>
        </button>
        <div class="term-menu-sep" />
        <button class="term-menu-item" :disabled="fontSizePx >= FONT_SIZE_MAX" @click="applyFontSize(fontSizePx + 1)">
          <span class="mi-lead">
            <ZoomIn :size="13" />
            <span>{{ t('sshCtxFontLarger') }}</span>
          </span>
          <span class="term-menu-kbd">{{ fontSizePx }}</span>
        </button>
        <button class="term-menu-item" :disabled="fontSizePx <= FONT_SIZE_MIN" @click="applyFontSize(fontSizePx - 1)">
          <span class="mi-lead">
            <ZoomOut :size="13" />
            <span>{{ t('sshCtxFontSmaller') }}</span>
          </span>
        </button>
        <div class="term-menu-sep" />
        <button class="term-menu-item" @click="onClear">
          <span>{{ t('sshCtxClear') }}</span>
        </button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.ssh-term {
  display: flex;
  position: relative;
  box-sizing: border-box;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  padding: var(--space-3);
  background: var(--color-terminal-bg);
}

/* 内边距必须加在外层、由这一层承接实际尺寸：
   FitAddon 读的是「父元素的 computed height」再减去 .xterm 自己的 padding，
   它不会扣父元素的 padding。所以如果把 padding 直接放在 xterm 的父节点上，
   可用高度会被算多 1~2 行，结果就是内容溢出、右侧/底部冒出多余的滚动条。
   这里用 flex 让父节点拿到「已扣掉 padding」的确切像素高度。 */
.ssh-term-host {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

/* ---- 终端内搜索条 ---- */
.term-search {
  position: absolute;
  top: var(--space-4);
  right: var(--space-5);
  z-index: 20;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  max-width: calc(100% - var(--space-8));
  padding: var(--space-2) var(--space-3);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-md);
}

.term-search-icon {
  flex: 0 0 auto;
  color: var(--color-text-muted);
}

.term-search-input {
  flex: 1 1 auto;
  min-width: 120px;
  padding: 1px 0;
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: transparent;
  border: none;
  outline: none;
}

.term-search-count {
  flex: 0 0 auto;
  min-width: 34px;
  font-family: var(--font-mono);
  font-size: 10.5px;
  text-align: right;
  color: var(--color-text-muted);
}

.term-search-btn {
  display: flex;
  flex: 0 0 auto;
  padding: 2px;
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.term-search-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.ssh-term :deep(.xterm-viewport) {
  background: transparent !important;
  scrollbar-width: thin;
}

/* 终端右键菜单：轻量浮层，不引入第三方菜单库。
   用 position: fixed + Teleport 到 body，避免被 .ssh-term 的 overflow:hidden 裁掉，
   也避开任意祖先的 stacking context。 */
.term-menu {
  position: fixed;
  z-index: 2000;
  min-width: 160px;
  padding: var(--space-1);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: 0 8px 28px var(--color-bg-overlay);
}

.term-menu-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-6);
  width: 100%;
  padding: var(--space-3) var(--space-4);
  font-size: 12px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  text-align: left;
}

.term-menu-item:hover:not(:disabled) {
  background: var(--color-bg-active);
}

.term-menu-item:disabled {
  color: var(--color-text-disabled);
  cursor: default;
}

/* 「问 AI」项：图标与文字成组靠左，快捷键靠右，
   与复制/粘贴项的 [内容 ... 快捷键] 结构保持一致。
   用显式 class 而不是 :has(.mi-lead) —— 后者要 Chrome 105+，
   WebView2 若停在旧版会整条样式失效。 */
.term-menu-item.ask-ai {
  color: var(--color-accent);
}

.term-menu-item.ask-ai:disabled {
  color: var(--color-text-disabled);
}

.term-menu-item .mi-lead {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
}

.term-menu-kbd {
  font-size: 10.5px;
  color: var(--color-text-muted);
  font-family: var(--font-mono);
}

.term-menu-sep {
  height: 1px;
  margin: var(--space-1) 0;
  background: var(--color-border);
}
</style>
