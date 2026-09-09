<template>
  <div class="term-page">
    <div class="term-header">
      <div class="term-title-wrap">
        <h2 class="term-title">{{ t('terminal_title') }}</h2>
        <span class="term-sub">{{ t('terminal_sub') }}</span>
      </div>
      <div class="term-actions">
        <select v-model="shell" class="term-select" :title="t('terminal_shell')">
          <option value="powershell">PowerShell</option>
          <option value="cmd">CMD</option>
        </select>
        <select v-model="runtimeID" class="term-select term-select-wide" :title="t('terminal_runtime_tip')">
          <option value="">{{ t('terminal_runtime_none') }}</option>
          <option v-for="r in runtimes" :key="r.id" :value="r.id">
            {{ r.id }}{{ r.version ? ' ' + r.version : '' }}
          </option>
        </select>
        <button class="term-btn primary" @click="newSession">+ {{ t('terminal_new') }}</button>
        <button class="term-btn" :disabled="!activeId" @click="closeActive">{{ t('terminal_close') }}</button>
      </div>
    </div>

    <div v-if="tabs.length" class="term-tabs">
      <button
        v-for="tab in tabs" :key="tab.id"
        :class="['term-tab', { active: tab.id === activeId, dead: tab.exited }]"
        @click="activate(tab.id)"
      >
        <span>{{ tab.label }}</span>
        <span v-if="tab.exited" class="term-tab-dot" :title="t('terminal_exited')">•</span>
      </button>
    </div>

    <div class="term-body">
      <div
        v-for="tab in tabs" :key="tab.id"
        v-show="tab.id === activeId"
        :ref="el => setHost(tab.id, el)"
        class="term-host"
      ></div>
      <div v-if="!tabs.length" class="term-empty">
        <p>{{ t('terminal_empty') }}</p>
        <button class="term-btn primary" @click="newSession">+ {{ t('terminal_new') }}</button>
      </div>
      <p v-if="errorMsg" class="term-error">{{ errorMsg }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import {
  TerminalStart, TerminalWrite, TerminalResize, TerminalKill, TerminalKillAll, TerminalRuntimeOptions,
} from '../../bindings/quickdock/services/appservice'

interface ToastAPI { error(msg: string): void; success(msg: string): void }
interface Tab { id: string; label: string; exited: boolean }

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')

const shell = ref('powershell')
const runtimeID = ref('')
const runtimes = ref<{ id: string; version: string; binDir: string; inPath: string }[]>([])
const tabs = ref<Tab[]>([])
const activeId = ref('')
const errorMsg = ref('')

// 每个会话一个 xterm 实例，切换标签时只切 DOM 显示，不销毁终端（保留滚动历史）
const terms = new Map<string, Terminal>()
const fits = new Map<string, FitAddon>()
const hosts = new Map<string, HTMLElement>()
const termRO = new Map<string, ResizeObserver>()
let seq = 0
let resizeObserver: ResizeObserver | null = null

function unwrap<T>(r: any): T | null {
  if (!r) return null
  if (r.code === 0) return r.data as T
  throw new Error(r.msg || 'unknown error')
}

// focusTerm 把键盘焦点交给指定会话的 xterm 文本框。
// 这是「能看不能输」的核心修复：xterm 只在文本框获得焦点时才触发 onData。
// 内嵌 WebView 里焦点极易被布局/窗口抢走，故做多重兜底：
//   - 立即 focus
//   - 下一帧再 focus（对抗 open 时容器尚未布局完成）
//   - 60ms / 200ms 各补一次（对抗 WebView / 宿主窗口异步抢焦）
function focusTerm(id: string) {
  const term = terms.get(id)
  if (!term) return
  term.focus()
  requestAnimationFrame(() => term.focus())
  setTimeout(() => term.focus(), 60)
  setTimeout(() => term.focus(), 200)
}

// writePty 把一段输入写入指定会话的伪控制台，失败必须可见（否则表现为「能看不能输」的静默失效）。
function writePty(id: string, data: string) {
  if (!id || !data) return
  TerminalWrite(id, data).catch((err: unknown) => {
    console.warn('[terminal] 输入写入失败', err)
  })
}

// handleKeydown 终端输入的最终兜底：在 document 捕获阶段接管键盘，自行转译成终端字节流写入 PTY。
// 背景：内嵌 WebView(WebView2) 里 xterm 的隐藏 textarea 常常收不到键盘输入，
// 此前「多次 focus / ResizeObserver / 窗口获焦补焦」全部无效，说明焦点链路本身不可靠，必须绕开 textarea。
//
// 防双写（关键）：凡本函数处理的按键，一律 preventDefault + stopPropagation。
//   - preventDefault：阻止字符进入 textarea（textarea 值不变 → 不触发 input → xterm 不会自行 fire data）
//   - stopPropagation：捕获阶段即掐断，事件到不了 textarea 上 xterm 自己的 keydown 监听
//     （xterm 对方向键等会直接 fire data，不拦住就会「按一次输入两遍」）
// 由此，写入 PTY 的只有本函数这一条路径。
function handleKeydown(e: KeyboardEvent) {
  const id = activeId.value
  if (!id) return
  // 下拉框（shell / 运行时）与输入框交给浏览器原生，否则无法用方向键选选项
  const tag = (e.target as HTMLElement | null)?.tagName
  if (tag === 'SELECT' || tag === 'INPUT') return
  // 输入法组字过程（含中文）原样放行给 textarea，避免吃掉组字
  if (e.isComposing || e.key === 'Process') return

  let data: string | null = null
  if (e.ctrlKey && !e.altKey) {
    // Ctrl+V / Ctrl+Insert 是粘贴，交给 paste 事件处理，不能写成控制字符
    if (e.key === 'v' || e.key === 'V' || e.key === 'Insert') return
    if (e.key.length === 1) {
      const c = e.key.toUpperCase().charCodeAt(0)
      if (c >= 65 && c <= 90) data = String.fromCharCode(c - 64) // Ctrl+A..Z → \x01..\x1A（Ctrl+C 中断 / Ctrl+D EOF 等）
      else if (e.key === '[') data = '\x1b'                      // Ctrl+[ == ESC
      else if (e.key === '\\') data = '\x1c'
      else if (e.key === ']') data = '\x1d'
      else if (e.key === '^') data = '\x1e'
      else if (e.key === '_') data = '\x1f'
    } else if (e.key === 'ArrowLeft') data = '\x1b[1;5D'  // 按词左移
    else if (e.key === 'ArrowRight') data = '\x1b[1;5C'   // 按词右移
  } else if (e.altKey) {
    return // Alt 组合在部分键盘布局用于输入字符，保守放行
  } else {
    switch (e.key) {
      case 'Enter': data = '\r'; break
      case 'Backspace': data = '\x7f'; break
      case 'Tab': data = '\t'; break
      case 'Escape': data = '\x1b'; break
      case 'ArrowUp': data = '\x1b[A'; break
      case 'ArrowDown': data = '\x1b[B'; break
      case 'ArrowRight': data = '\x1b[C'; break
      case 'ArrowLeft': data = '\x1b[D'; break
      case 'Home': data = '\x1b[H'; break
      case 'End': data = '\x1b[F'; break
      case 'Delete': data = '\x1b[3~'; break
      case 'Insert': data = '\x1b[2~'; break
      case 'PageUp': data = '\x1b[5~'; break
      case 'PageDown': data = '\x1b[6~'; break
      case 'F1': data = '\x1bOP'; break
      case 'F2': data = '\x1bOQ'; break
      case 'F3': data = '\x1bOR'; break
      case 'F4': data = '\x1bOS'; break
      default:
        if (e.key.length === 1) data = e.key // 可打印字符（含空格）
    }
  }
  if (data === null) return

  e.preventDefault()
  e.stopPropagation()
  writePty(id, data)
}

// handlePaste 粘贴：直接把剪贴板文本写入 PTY（Ctrl+V 已在 keydown 中放行到此处）。
function handlePaste(e: ClipboardEvent) {
  const id = activeId.value
  if (!id) return
  const tag = (e.target as HTMLElement | null)?.tagName
  if (tag === 'SELECT' || tag === 'INPUT') return
  const text = e.clipboardData?.getData('text/plain') || e.clipboardData?.getData('text')
  if (!text) return
  e.preventDefault()
  writePty(id, text)
}

function setHost(id: string, el: any) {
  if (el) hosts.set(id, el as HTMLElement)
  else hosts.delete(id)
}

async function loadRuntimes() {
  try {
    const list = unwrap<any[]>(await TerminalRuntimeOptions()) ?? []
    runtimes.value = list.filter(r => r && r.id)
  } catch {
    runtimes.value = []
  }
}

async function newSession() {
  errorMsg.value = ''
  seq++
  const id = 't' + Date.now() + '-' + seq
  const label = (runtimeID.value ? runtimeID.value + ' · ' : '') + (shell.value === 'cmd' ? 'CMD' : 'PS')

  tabs.value.push({ id, label, exited: false })
  activeId.value = id
  await nextTick()

  const host = hosts.get(id)
  if (!host) return

  const term = new Terminal({
    convertEol: true,
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Cascadia Mono, Consolas, Menlo, monospace',
    theme: {
      background: '#12141a',
      foreground: '#d6dae3',
      cursor: '#d6dae3',
      selectionBackground: '#3a4a66',
    },
    // 单次写入上限：后端已按 16ms 合并，这里再留足余量避免长输出被截断
    scrollback: 5000,
  })
  const fit = new FitAddon()
  term.loadAddon(fit)
  term.loadAddon(new WebLinksAddon())
  term.open(host)
  terms.set(id, term)
  fits.set(id, fit)

  term.onData(data => {
    // 原生路径（textarea 能用时）：输入经后端写入伪控制台。
    // 兜底路径见 handleKeydown；两条路径都收敛到 writePty，且不会同时触发（详见该函数注释）。
    writePty(id, data)
  })
  term.onResize(({ cols, rows }) => { void TerminalResize(id, cols, rows) })

  // 点击终端区域时强制把键盘焦点交给 xterm 文本框：这是「能看不能输」最常见的根因
  // （焦点在别处时 xterm 的 onData 不会触发）。mousedown 即聚焦，避免点击后还需再点一次。
  host.addEventListener('mousedown', () => focusTerm(id))
  host.addEventListener('pointerdown', () => focusTerm(id))

  // 容器尺寸首次就绪（或后续变化）时，重新 fit 并把焦点抢回来——
  // open 时容器可能尚未布局完成（0 高），fit 会失败；布局一就绪立刻补上，
  // 否则文本框 0 尺寸时焦点不粘，表现为「终端能显示、但打不了字」。
  const ro = new ResizeObserver(() => {
    try { fit.fit() } catch { /* 容器尺寸为 0 时 fit 会抛错，下一帧再试 */ }
    term.focus()
  })
  ro.observe(host)
  termRO.set(id, ro)

  try {
    fit.fit()
  } catch { /* 容器尚未布局完成，后续 resize 观察器会补上 */ }

  try {
    unwrap(await TerminalStart(id, shell.value, '', runtimeID.value, term.cols, term.rows))
    // open 时容器可能尚未完成布局，focus 不粘；多重兜底聚焦确保拿到键盘焦点。
    focusTerm(id)
  } catch (e: any) {
    errorMsg.value = e?.message ?? String(e)
    term.writeln('\r\n[!] ' + errorMsg.value)
  }
}

function activate(id: string) {
  activeId.value = id
  nextTick(() => {
    const term = terms.get(id)
    const fit = fits.get(id)
    if (!term) return
    try { fit?.fit() } catch { /* 忽略：容器尺寸为 0 时 fit 会抛错 */ }
    focusTerm(id)
  })
}

async function closeActive() {
  const id = activeId.value
  if (!id) return
  await closeTab(id)
}

async function closeTab(id: string) {
  const term = terms.get(id)
  term?.dispose()
  terms.delete(id)
  fits.delete(id)
  hosts.delete(id)
  if (termRO.has(id)) {
    termRO.get(id)?.disconnect()
    termRO.delete(id)
  }
  tabs.value = tabs.value.filter(x => x.id !== id)
  if (activeId.value === id) {
    activeId.value = tabs.value.length ? tabs.value[tabs.value.length - 1].id : ''
  }
  try { await TerminalKill(id) } catch { /* 进程可能已退出 */ }
}

function onOutput(payload: any) {
  const id = payload?.data?.id
  const data = payload?.data?.data
  if (!id || data == null) return
  terms.get(id)?.write(data)
}

function onExit(payload: any) {
  const id = payload?.data?.id
  if (!id) return
  const tab = tabs.value.find(x => x.id === id)
  if (tab) tab.exited = true
  const code = payload?.data?.code ?? 0
  terms.get(id)?.writeln(`\r\n[${t('terminal_exited')} · ${code}]`)
}

let offOutput: (() => void) | null = null
let offExit: (() => void) | null = null
let offWinFocus: (() => void) | null = null
let offKeyHandlers: (() => void) | null = null

onMounted(() => {
  void loadRuntimes()
  offOutput = Events.On('quickdock:terminal:output', onOutput)
  offExit = Events.On('quickdock:terminal:exit', onExit)
  resizeObserver = new ResizeObserver(() => {
    const id = activeId.value
    if (!id) return
    try { fits.get(id)?.fit() } catch { /* 忽略 */ }
    focusTerm(id)
  })
  nextTick(() => {
    const el = document.querySelector('.term-body')
    if (el) resizeObserver?.observe(el)
  })
  // 窗口/WebView 重新获得焦点时，把光标交还当前终端——避免焦点被宿主窗口抢走后「打不了字」。
  const onWinFocus = () => { if (activeId.value) focusTerm(activeId.value) }
  window.addEventListener('focus', onWinFocus)
  offWinFocus = () => window.removeEventListener('focus', onWinFocus)
  // 终端键盘输入兜底：capture 阶段接管，确保先于 xterm 自身的监听执行（配合 stopPropagation 防双写）
  document.addEventListener('keydown', handleKeydown, true)
  document.addEventListener('paste', handlePaste, true)
  offKeyHandlers = () => {
    document.removeEventListener('keydown', handleKeydown, true)
    document.removeEventListener('paste', handlePaste, true)
  }
})

onUnmounted(() => {
  offOutput?.()
  offExit?.()
  offWinFocus?.()
  offKeyHandlers?.()
  resizeObserver?.disconnect()
  resizeObserver = null
  terms.forEach(tm => tm.dispose())
  terms.clear()
  fits.clear()
  termRO.forEach(ro => ro.disconnect())
  termRO.clear()
  void TerminalKillAll()
  void toast
})
</script>

<style scoped>
.term-page {
  display: flex; flex-direction: column; height: 100%;
  padding: var(--space-6) var(--space-8); overflow: hidden;
}
.term-header {
  display: flex; align-items: flex-end; justify-content: space-between;
  margin-bottom: var(--space-4); flex-shrink: 0; gap: var(--space-3);
}
.term-title-wrap { display: flex; align-items: baseline; gap: var(--space-3); }
.term-title { font-size: 18px; font-weight: 600; color: var(--color-text-primary); margin: 0; }
.term-sub { font-size: 12px; color: var(--color-text-disabled); }
.term-actions { display: flex; align-items: center; gap: var(--space-2); }

.term-select, .term-btn {
  height: 28px; padding: 0 var(--space-2);
  background: var(--color-bg-elevated);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  color: var(--color-text-primary);
  font-size: 12px; cursor: pointer;
}
.term-select-wide { min-width: 130px; }
.term-btn:hover { border-color: var(--color-primary); }
.term-btn:disabled { opacity: .5; cursor: not-allowed; }
.term-btn.primary { background: var(--color-primary); border-color: var(--color-primary); color: #fff; }

.term-tabs { display: flex; gap: var(--space-1); margin-bottom: var(--space-2); flex-shrink: 0; flex-wrap: wrap; }
.term-tab {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 4px 10px; font-size: 12px; cursor: pointer;
  background: var(--color-bg-elevated);
  border: 1px solid var(--color-border);
  border-radius: var(--radius) var(--radius) 0 0;
  color: var(--color-text-secondary);
}
.term-tab.active { color: var(--color-text-primary); border-bottom-color: transparent; background: var(--color-bg-card); }
.term-tab.dead { opacity: .6; }
.term-tab-dot { color: #e5a23c; font-size: 14px; line-height: 1; }

.term-body { position: relative; flex: 1; min-height: 0; background: #12141a; border-radius: var(--radius); overflow: hidden; }
.term-host { width: 100%; height: 100%; }
.term-empty {
  height: 100%; display: flex; flex-direction: column;
  align-items: center; justify-content: center; gap: var(--space-3);
  color: var(--color-text-disabled); font-size: 13px;
}
.term-error {
  position: absolute; left: var(--space-3); bottom: var(--space-3);
  margin: 0; padding: 6px 10px; font-size: 12px;
  background: rgba(220, 90, 90, .12); color: #e58a8a;
  border: 1px solid rgba(220, 90, 90, .3); border-radius: var(--radius);
}
</style>
