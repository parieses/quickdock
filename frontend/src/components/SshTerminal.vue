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
import { Terminal, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { Events } from '@wailsio/runtime'
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
}>()

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
watch(themeName, () => {
  // 热更新，不重建终端：重建会丢掉当前画面与回滚内容。
  if (term) term.options.theme = readTermTheme()
})

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
  writeQueue.dispose()
  pending = []
  duringReplay = []
  // 只销毁渲染器，不关会话：会话常驻 Go 侧，关窗后重开要能接回来。
  term?.dispose()
  term = null
})

defineExpose({
  focus: () => term?.focus(),
  fit: () => fitAddon?.fit(),
  getSize: () => ({ cols: term?.cols ?? 80, rows: term?.rows ?? 24 }),
  clear: () => term?.clear(),
  /** 用户确认多行粘贴后由父组件调用，走与按键完全相同的发送路径。 */
  send: sendInput,
})
</script>

<template>
  <div class="ssh-term">
    <div ref="hostEl" class="ssh-term-host" />
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
</style>
