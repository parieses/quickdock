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
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Sparkles } from '@lucide/vue'
import { Terminal, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { Clipboard, Events } from '@wailsio/runtime'
import '@xterm/xterm/css/xterm.css'

import { Replay, Resize, Write } from '../../bindings/quickdock/services/sshx/sshservice'
import { base64ToBytes, bytesToBase64, textToBytes } from '../utils/base64'
import { createWriteQueue } from '../utils/termWriteQueue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useThemeName } from '../composables/useThemeName'
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

// ---- 右键菜单（复制 / 粘贴 / 全选 / 清空） ----
//
// xterm.js 只负责「选中文字」，但默认**不会把选区写进系统剪贴板**，也没有任何
// 右键菜单——所以在 WebView 里选中一段输出后，用户既没法 Ctrl+C（那是发给远端的
// SIGINT），也没法右键复制，等于「看得见拿不走」。这里补一个轻量右键菜单 +
// 快捷键（Ctrl+Shift+C 复制 / Ctrl+Shift+V、Shift+Insert 粘贴 / Ctrl+Shift+A 问 AI），
// 与主流终端一致。
const menu = ref<{ x: number; y: number } | null>(null)

function hasSelection(): boolean {
  return !!term && term.hasSelection()
}

function openMenu(e: MouseEvent) {
  // 超出视口时往回收，避免菜单被屏幕边缘裁掉点不到。
  // 底部预留 190px：菜单每加一项约增高 30px，预留不足会正好压住最后一项。
  const x = Math.min(e.clientX, window.innerWidth - 180)
  const y = Math.min(e.clientY, window.innerHeight - 190)
  menu.value = { x, y }
}

function closeMenu() {
  menu.value = null
}

/**
 * 写系统剪贴板。
 *
 * 走 Wails 原生桥（Clipboard.SetText）——它直接调 Go 侧的 app.Clipboard.SetText，
 * 经系统 API 写剪贴板，**不受 WebView2 安全上下文限制**。这是项目里早验证过的
 * 正确路径（见 services/clipboard 的 CopyText 注释）：在 WebView 里 navigator.clipboard
 * 经常因非安全上下文被静默拦截，execCommand('copy') 在 WebView2 也极不可靠，
 * 表现为「选中了却复制不走」。非 Wails 环境（纯浏览器/单测）才退回 navigator.clipboard。
 */
async function copyText(text: string): Promise<boolean> {
  try {
    await Clipboard.SetText(text)
    return true
  } catch {
    // 退回 Web 标准方案（仅非 Wails 环境有意义）
  }
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* ignore */
  }
  return false
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
  if (hasSelection()) await copyText(term!.getSelection())
  closeMenu()
}

async function onPaste() {
  const text = await readClipboardText()
  // 显式粘贴：不走 isMultiLinePaste 确认（用户主动操作），
  // 直接经与按键相同的发送路径发往远端。
  if (text) sendInput(text)
  closeMenu()
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
  if (!hasSelection()) {
    closeMenu()
    return
  }
  const lines = term!.getSelection().split('\n').map((l) => l.trimEnd())
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

/** 快捷键：
 *  - Ctrl/Cmd+Shift+C 复制、Ctrl/Cmd+Insert 复制；
 *  - Ctrl/Cmd+V、Ctrl/Cmd+Shift+V、Shift+Insert 粘贴（都走 Wails 原生桥读取剪贴板）。
 *  普通 Ctrl+C 不介入（那是 SIGINT，交给 xterm / 远端）。
 *  普通 Ctrl+V 也要拦截：WebView2 下浏览器原生粘贴事件常拿不到剪贴板数据，
 *  直接交给原生会导致「粘贴没反应」，故统一改走原生桥读取。 */
function onKeydown(e: KeyboardEvent) {
  const mod = e.ctrlKey || e.metaKey
  const k = e.key
  if (mod && k === 'C' && e.shiftKey) {
    e.preventDefault()
    void onCopy()
    return
  }
  // Ctrl/Cmd+Shift+A：对选中内容提问 AI。无选区时不拦截，
  // 把这个组合留给宿主/浏览器的其它用途，避免按了没反应又拦了键。
  if (mod && k === 'A' && e.shiftKey) {
    if (!hasSelection()) return
    e.preventDefault()
    onAskAI()
    return
  }
  if (mod && k === 'V' && e.shiftKey) {
    e.preventDefault()
    void onPaste()
    return
  }
  if (mod && k === 'V') {
    e.preventDefault()
    void onPaste()
    return
  }
  if (mod && k === 'Insert') {
    e.preventDefault()
    void onCopy()
    return
  }
  if (e.shiftKey && k === 'Insert') {
    e.preventDefault()
    void onPaste()
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
    fontSize: props.fontSize,
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

  term.open(hostEl.value)
  await nextTick()
  fitAddon.fit()

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
    if (!term || !size) return
    term.options.fontSize = size
    fitAddon?.fit()
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

    <Teleport to="body">
      <div
        v-if="menu"
        class="term-menu"
        :style="{ left: menu.x + 'px', top: menu.y + 'px' }"
        @click.stop
        @contextmenu.prevent
      >
        <button class="term-menu-item" :disabled="!hasSelection()" @click="onCopy">
          <span>{{ t('sshCtxCopy') }}</span>
          <span class="term-menu-kbd">Ctrl+Shift+C</span>
        </button>
        <button class="term-menu-item ask-ai" :disabled="!hasSelection()" @click="onAskAI">
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
