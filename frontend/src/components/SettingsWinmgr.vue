<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, reactive, onMounted, onUnmounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { RotateCcw } from '@lucide/vue'
import {
  GetWinmgrHotkeyConfig,
  SetWinmgrHotkeyConfig,
  GetWinmgrRatio,
  SetWinmgrRatio,
  GetWinmgrFloatHotkeyConfig,
  SetWinmgrFloatHotkeyConfig,
} from '../../bindings/quickdock/services/appservice'
import { SuspendHotkeys, ResumeHotkeys } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import { isAppLocked } from '../utils/appLock'
import type { HotkeyConfig, ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface WinmgrItem {
  key: string
  labelKey: string
  defMod: number
  defVK: number
  mods: number
  vk: number
}

// 15 个窗口管理动作，默认热键统一 Ctrl+Alt+*，避开系统 Win+方向 的 Snap 冲突。
const actions: Omit<WinmgrItem, 'mods' | 'vk'>[] = [
  { key: 'winmgr_left', labelKey: 'winmgrLeft', defMod: 3, defVK: 0x25 },
  { key: 'winmgr_right', labelKey: 'winmgrRight', defMod: 3, defVK: 0x27 },
  { key: 'winmgr_top', labelKey: 'winmgrTop', defMod: 3, defVK: 0x26 },
  { key: 'winmgr_bottom', labelKey: 'winmgrBottom', defMod: 3, defVK: 0x28 },
  { key: 'winmgr_tl', labelKey: 'winmgrTl', defMod: 3, defVK: 0x31 },
  { key: 'winmgr_tr', labelKey: 'winmgrTr', defMod: 3, defVK: 0x32 },
  { key: 'winmgr_bl', labelKey: 'winmgrBl', defMod: 3, defVK: 0x33 },
  { key: 'winmgr_br', labelKey: 'winmgrBr', defMod: 3, defVK: 0x34 },
  { key: 'winmgr_center', labelKey: 'winmgrCenter', defMod: 3, defVK: 0x43 },
  { key: 'winmgr_maximize', labelKey: 'winmgrMaximize', defMod: 3, defVK: 0x0D },
  { key: 'winmgr_restore', labelKey: 'winmgrRestore', defMod: 3, defVK: 0x52 },
  { key: 'winmgr_minimize', labelKey: 'winmgrMinimize', defMod: 3, defVK: 0x4D },
  { key: 'winmgr_topmost', labelKey: 'winmgrTopmost', defMod: 3, defVK: 0x54 },
  { key: 'winmgr_monitor_prev', labelKey: 'winmgrMonitorPrev', defMod: 3, defVK: 0xDB },
  { key: 'winmgr_monitor_next', labelKey: 'winmgrMonitorNext', defMod: 3, defVK: 0xDD },
]

const items = reactive<WinmgrItem[]>(actions.map((a) => ({ ...a, mods: a.defMod, vk: a.defVK })))
const ratio = ref(50)
const message = ref('')
const msgTimer = ref<ReturnType<typeof setTimeout> | null>(null)

// 批量排版浮层热键（默认 Ctrl+Alt+W）。与 15 个动作共用捕获/保存逻辑，单独存一个目标。
const floatMods = ref(3)
const floatVk = ref(0x57)

// 捕获目标：区分「动作项」与「浮层热键」，避免各自维护捕获状态。
type CaptureTarget = { kind: 'action'; i: number } | { kind: 'float' } | null
const captureTarget = ref<CaptureTarget>(null)

function clearMsgTimer() {
  if (msgTimer.value !== null) {
    clearTimeout(msgTimer.value)
    msgTimer.value = null
  }
}
function setMsg(text: string, delay: number) {
  clearMsgTimer()
  message.value = text
  msgTimer.value = setTimeout(() => {
    message.value = ''
    msgTimer.value = null
  }, delay)
}

// 保存/恢复默认失败的统一收尾：页内提示 + toast + 日志。
// 只记日志的话，打包版用户看不到 DevTools，界面上仍是一片「成功」的错觉。
function onSaveError(e: unknown) {
  const msg = getErrorMessage(e)
  message.value = t('saveFailed2') + ': ' + msg
  toast.error(msg)
  logErr('SettingsWinmgr', e)
}

function toVK(e: KeyboardEvent): number | null {
  const map: Record<string, number> = {
    'Space': 0x20, 'Enter': 0x0d, 'Escape': 0x1b, 'Tab': 0x09,
    'Backspace': 0x08, 'Delete': 0x2e, 'Insert': 0x2d,
    'PageUp': 0x21, 'PageDown': 0x22, 'Home': 0x24, 'End': 0x23,
    'ArrowLeft': 0x25, 'ArrowUp': 0x26, 'ArrowRight': 0x27, 'ArrowDown': 0x28,
    'Backquote': 0xc0,
    'F1': 0x70, 'F2': 0x71, 'F3': 0x72, 'F4': 0x73, 'F5': 0x74, 'F6': 0x75,
    'F7': 0x76, 'F8': 0x77, 'F9': 0x78, 'F10': 0x79, 'F11': 0x7a, 'F12': 0x7b,
    'Digit0': 0x30, 'Digit1': 0x31, 'Digit2': 0x32, 'Digit3': 0x33, 'Digit4': 0x34,
    'Digit5': 0x35, 'Digit6': 0x36, 'Digit7': 0x37, 'Digit8': 0x38, 'Digit9': 0x39,
  }
  if (e.code.startsWith('Key') && e.code.length === 4) return e.code.charCodeAt(3)
  return map[e.code] ?? null
}
function vkToName(vk: number): string {
  const n: Record<number, string> = {
    0x20: 'Space', 0x0d: 'Enter', 0x1b: 'Esc', 0x09: 'Tab', 0x08: 'Backspace', 0x2e: 'Del', 0x2d: 'Ins',
    0x21: 'PgUp', 0x22: 'PgDn', 0x24: 'Home', 0x23: 'End',
    0x25: 'Left', 0x26: 'Up', 0x27: 'Right', 0x28: 'Down', 0xc0: '`',
    0x70: 'F1', 0x71: 'F2', 0x72: 'F3', 0x73: 'F4', 0x74: 'F5', 0x75: 'F6',
    0x76: 'F7', 0x77: 'F8', 0x78: 'F9', 0x79: 'F10', 0x7a: 'F11', 0x7b: 'F12',
    0x30: '0', 0x31: '1', 0x32: '2', 0x33: '3', 0x34: '4',
    0x35: '5', 0x36: '6', 0x37: '7', 0x38: '8', 0x39: '9',
    0xdb: '[', 0xdd: ']',
    0x41: 'A', 0x42: 'B', 0x43: 'C', 0x44: 'D', 0x45: 'E', 0x46: 'F', 0x47: 'G',
    0x48: 'H', 0x49: 'I', 0x4a: 'J', 0x4b: 'K', 0x4c: 'L', 0x4d: 'M', 0x4e: 'N',
    0x4f: 'O', 0x50: 'P', 0x51: 'Q', 0x52: 'R', 0x53: 'S', 0x54: 'T',
    0x55: 'U', 0x56: 'V', 0x57: 'W', 0x58: 'X', 0x59: 'Y', 0x5a: 'Z',
  }
  return n[vk] || ''
}
function toLabel(mods: number, vk: number): string {
  const names: [number, string][] = [[2, 'Ctrl'], [1, 'Alt'], [4, 'Shift'], [8, 'Win']]
  const parts = names.filter(([m]) => (mods & m) !== 0).map(([, n]) => n)
  parts.push(vkToName(vk) || `VK_${vk}`)
  return parts.join('+')
}

function onGlobalKeyDown(e: KeyboardEvent) {
  // 同 HotkeySettings：捕获阶段的录制监听会在锁定时把密码框的按键记成快捷键。
  if (isAppLocked()) return
  const tgt = captureTarget.value
  if (!tgt) return
  if (e.code === 'Escape' || e.key === 'Escape') {
    e.preventDefault()
    e.stopImmediatePropagation()
    captureTarget.value = null
    ResumeHotkeys()
    return
  }
  e.preventDefault()
  e.stopPropagation()
  const vk = toVK(e)
  if (!vk) return
  const mods = (e.ctrlKey ? 2 : 0) | (e.altKey ? 1 : 0) | (e.shiftKey ? 4 : 0) | (e.metaKey ? 8 : 0)
  if ([0x10, 0x11, 0x12, 0x5b, 0x5c].includes(vk)) return
  if (tgt.kind === 'action') {
    items[tgt.i].vk = vk
    items[tgt.i].mods = mods
  } else {
    floatMods.value = mods
    floatVk.value = vk
  }
  captureTarget.value = null
  ResumeHotkeys()
}

onMounted(async () => {
  document.addEventListener('keydown', onGlobalKeyDown, true)
  try {
    const r = unwrap<number>(await GetWinmgrRatio())
    if (typeof r === 'number') ratio.value = r
  } catch (e) { logErr('SettingsWinmgr', e) }
  for (const it of items) {
    try {
      const c = unwrap<HotkeyConfig>(await GetWinmgrHotkeyConfig(it.key, it.defMod, it.defVK))
      if (c) {
        it.mods = c.modifiers
        it.vk = c.vk
      }
    } catch (e) { logErr('SettingsWinmgr', e) }
  }
  try {
    const fc = unwrap<HotkeyConfig>(await GetWinmgrFloatHotkeyConfig())
    if (fc) {
      floatMods.value = fc.modifiers
      floatVk.value = fc.vk
    }
  } catch (e) { logErr('SettingsWinmgr', e) }
})

onUnmounted(() => {
  document.removeEventListener('keydown', onGlobalKeyDown, true)
  clearMsgTimer()
  if (captureTarget.value !== null) {
    captureTarget.value = null
    ResumeHotkeys()
  }
})

async function startCapture(i: number) {
  if (captureTarget.value && captureTarget.value.kind === 'action' && captureTarget.value.i === i) {
    captureTarget.value = null
    await ResumeHotkeys()
    return
  }
  captureTarget.value = { kind: 'action', i }
  await SuspendHotkeys()
}

async function startCaptureFloat() {
  if (captureTarget.value?.kind === 'float') {
    captureTarget.value = null
    await ResumeHotkeys()
    return
  }
  captureTarget.value = { kind: 'float' }
  await SuspendHotkeys()
}

async function resetOne(i: number) {
  items[i].mods = items[i].defMod
  items[i].vk = items[i].defVK
}

function resetFloat() {
  floatMods.value = 3
  floatVk.value = 0x57
}

function isCapturing(i: number): boolean {
  const tgt = captureTarget.value
  return tgt?.kind === 'action' && tgt.i === i
}

function isDefault(i: number): boolean {
  return items[i].mods === items[i].defMod && items[i].vk === items[i].defVK
}

async function resetAll() {
  for (const it of items) {
    it.mods = it.defMod
    it.vk = it.defVK
  }
  ratio.value = 50
  floatMods.value = 3
  floatVk.value = 0x57
  try {
    // 逐个写库：以前这里只改了本地 ref，界面上显示「已恢复默认」，
    // 实际库里还是旧键位，重启后原样回来。
    for (const it of items) unwrap(await SetWinmgrHotkeyConfig(it.key, it.mods, it.vk))
    unwrap(await SetWinmgrRatio(50))
    unwrap(await SetWinmgrFloatHotkeyConfig(3, 0x57))
    await ResumeHotkeys()
    setMsg(t('restoreOk'), 2000)
  } catch (e) { onSaveError(e) }
}

async function saveAll() {
  message.value = ''
  const pairs: [number, number][] = items.map((it) => [it.mods, it.vk])
  pairs.push([floatMods.value, floatVk.value])
  for (let i = 0; i < pairs.length; i++) {
    for (let j = i + 1; j < pairs.length; j++) {
      if (pairs[i][0] === pairs[j][0] && pairs[i][1] === pairs[j][1]) {
        setMsg(t('hotkeyConflict'), 3000)
        return
      }
    }
  }
  try {
    // unwind：后端返回 code!=0 时不会 reject，不 unwrap 就会提示「已保存」。
    for (const it of items) unwrap(await SetWinmgrHotkeyConfig(it.key, it.mods, it.vk))
    unwrap(await SetWinmgrRatio(ratio.value))
    unwrap(await SetWinmgrFloatHotkeyConfig(floatMods.value, floatVk.value))
    await ResumeHotkeys()
    setMsg(t('hotkeySaved'), 2000)
  } catch (e) {
    onSaveError(e)
  }
}
</script>

<template>
  <div class="wm-page">
    <header class="wm-head">
      <h3 class="wm-title">{{ t('winmgr') }}</h3>
      <p class="wm-desc">{{ t('winmgrDesc') }}</p>
    </header>

    <section class="wm-card">
      <div class="wm-card-head">
        <span class="wm-card-title">{{ t('winmgrLayoutTitle') }}</span>
      </div>
      <div class="wm-grid">
        <div v-for="(it, i) in items" :key="it.key" class="wm-item" :class="{ capturing: isCapturing(i) }">
          <span class="wm-item-label">{{ t(it.labelKey) }}</span>
          <button class="wm-kbd" :title="t('clickToModify')" @click="startCapture(i)">
            <span v-if="isCapturing(i)" class="wm-capturing">{{ t('pressKeys') }}</span>
            <span v-else class="wm-keys">{{ toLabel(it.mods, it.vk) }}</span>
          </button>
          <button v-show="!isDefault(i)" class="wm-reset" :title="t('restoreDefault')" @click="resetOne(i)">
            <RotateCcw :size="12" />
          </button>
        </div>
      </div>
    </section>

    <section class="wm-card">
      <div class="wm-card-head">
        <span class="wm-card-title">{{ t('winmgrFloatHotkey') }}</span>
        <span class="wm-card-sub">{{ t('winmgrFloatHotkeyDesc') }}</span>
      </div>
      <div class="wm-row">
        <span class="wm-row-label">{{ t('winmgrFloatOpen') }}</span>
        <button class="wm-kbd" :class="{ capturing: captureTarget?.kind === 'float' }" :title="t('clickToModify')" @click="startCaptureFloat()">
          <span v-if="captureTarget?.kind === 'float'" class="wm-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="wm-keys">{{ toLabel(floatMods, floatVk) }}</span>
        </button>
        <button v-show="!(floatMods === 3 && floatVk === 0x57)" class="wm-reset" :title="t('restoreDefault')" @click="resetFloat">
          <RotateCcw :size="12" />
        </button>
      </div>
    </section>

    <section class="wm-card">
      <div class="wm-card-head">
        <span class="wm-card-title">{{ t('winmgrRatio') }}</span>
      </div>
      <div class="wm-ratio">
        <input v-model.number="ratio" type="range" min="10" max="90" step="1" class="wm-slider" />
        <span class="wm-ratio-val">{{ ratio }}%</span>
      </div>
      <p class="wm-hint">{{ t('winmgrRatioDesc') }}</p>
    </section>

    <div class="wm-actions">
      <button class="qd-btn qd-btn-lg qd-btn-primary" @click="saveAll">{{ t('saveAll') }}</button>
      <button class="qd-btn qd-btn-lg qd-btn-ghost" @click="resetAll">{{ t('restoreDefault') }}</button>
    </div>
    <p v-if="message" class="wm-msg">{{ message }}</p>
    <p class="wm-tip">{{ t('winmgrTip') }}</p>
  </div>
</template>

<style scoped>
.wm-page { width: 100%; max-width: var(--settings-max); }
.wm-head { margin-bottom: var(--space-5); }
.wm-title { font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary); margin: 0 0 var(--space-2); }
.wm-desc { font-size: var(--text-sm); color: var(--color-text-muted); margin: 0; line-height: 1.5; }

.wm-card {
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  padding: var(--space-5);
  margin-bottom: var(--space-5);
}
.wm-card-head { display: flex; align-items: baseline; gap: var(--space-4); margin-bottom: var(--space-4); }
.wm-card-title { font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.wm-card-sub { font-size: var(--text-xs); color: var(--color-text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 2 列紧凑网格 */
.wm-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-2) var(--space-5);
}
.wm-item {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  min-width: 0;
  padding: var(--space-2) 0;
}
.wm-item-label {
  flex: 1;
  min-width: 0;
  font-size: var(--text-sm);
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wm-item.capturing .wm-item-label { color: var(--color-accent-ink); }

/* 真实键帽样式 */
.wm-kbd {
  flex-shrink: 0;
  min-width: 60px;
  height: 26px;
  padding: 0 var(--space-3);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  font-weight: 600;
  line-height: 1;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-bottom-width: 2px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: border-color var(--transition-fast), background-color var(--transition-fast), color var(--transition-fast), box-shadow var(--transition-fast);
}
.wm-kbd:hover { border-color: var(--color-border-focus); background: var(--color-bg-hover); }
.wm-kbd.capturing {
  border-color: var(--color-accent);
  border-bottom-color: var(--color-accent);
  background: var(--color-accent-bg);
  color: var(--color-accent-ink);
  box-shadow: 0 0 0 2px var(--color-accent-border);
  animation: wm-pulse 1.1s ease-in-out infinite;
}
@keyframes wm-pulse { 0%, 100% { opacity: 1 } 50% { opacity: 0.65 } }
.wm-capturing { font-family: inherit; font-size: var(--text-xs); font-weight: 500; }

.wm-reset {
  flex-shrink: 0;
  width: 22px;
  height: 22px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  background: transparent;
  color: var(--color-text-muted);
  border-radius: var(--radius-xs);
  cursor: pointer;
  opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast), background var(--transition-fast);
}
.wm-item:hover .wm-reset,
.wm-row:hover .wm-reset { opacity: 1; }
.wm-reset:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.wm-row { display: flex; align-items: center; gap: var(--space-4); }
.wm-row-label { flex: 1; font-size: var(--text-sm); color: var(--color-text-muted); }

.wm-ratio { display: flex; align-items: center; gap: var(--space-5); }
.wm-slider { flex: 1; accent-color: var(--color-accent); }
.wm-ratio-val { font-size: var(--text-md); font-weight: 600; color: var(--color-accent-ink); min-width: 44px; text-align: right; }
.wm-hint { font-size: var(--text-xs); color: var(--color-text-muted); margin: var(--space-4) 0 0; line-height: 1.5; }

.wm-actions { display: flex; gap: var(--space-4); margin-top: var(--space-1); }
.wm-msg { font-size: var(--text-sm); color: var(--color-success-ink); margin: var(--space-4) 0 0; }
.wm-tip { font-size: var(--text-sm); color: var(--color-text-muted); line-height: 1.6; margin: var(--space-4) 0 0; }
</style>
