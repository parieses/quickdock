<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Keyboard, RotateCcw } from '@lucide/vue'
import { GetHotkeyConfig, SetHotkeyConfig, GetClipboardHotkeyConfig, SetClipboardHotkeyConfig, GetPaletteHotkeyConfig, SetPaletteHotkeyConfig, SuspendHotkeys, ResumeHotkeys } from '../../bindings/quickdock/services/appservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import type { HotkeyConfig } from '../types'

const { t } = useI18n()

const currentLabel = ref('Ctrl+Space')
const currentModifiers = ref(2)
const currentVk = ref(32)
const clipLabel = ref('Ctrl+`')
const clipModifiers = ref(2)
const clipVk = ref(0xC0)
const paletteLabel = ref('Ctrl+K')
const paletteModifiers = ref(2)
const paletteVk = ref(0x4B)
const capturing = ref<'app' | 'clipboard' | 'palette' | null>(null)
const message = ref('')
const msgTimer = ref<ReturnType<typeof setTimeout> | null>(null)

function clearMsgTimer() {
  if (msgTimer.value !== null) {
    clearTimeout(msgTimer.value)
    msgTimer.value = null
  }
}

function setMsgAndClear(text: string, delay: number) {
  clearMsgTimer()
  message.value = text
  msgTimer.value = setTimeout(() => { message.value = ''; msgTimer.value = null }, delay)
}

function toVK(e: KeyboardEvent): number | null {
  const map: Record<string, number> = {
    'Space':0x20,'Enter':0x0D,'Escape':0x1B,'Tab':0x09,
    'Backspace':0x08,'Delete':0x2E,'Insert':0x2D,
    'PageUp':0x21,'PageDown':0x22,'Home':0x24,'End':0x23,
    'ArrowLeft':0x25,'ArrowUp':0x26,'ArrowRight':0x27,'ArrowDown':0x28,
    'Backquote':0xC0,
    'F1':0x70,'F2':0x71,'F3':0x72,'F4':0x73,'F5':0x74,'F6':0x75,
    'F7':0x76,'F8':0x77,'F9':0x78,'F10':0x79,'F11':0x7A,'F12':0x7B,
    'Digit0':0x30,'Digit1':0x31,'Digit2':0x32,'Digit3':0x33,'Digit4':0x34,
    'Digit5':0x35,'Digit6':0x36,'Digit7':0x37,'Digit8':0x38,'Digit9':0x39,
  }
  if (e.code.startsWith('Key') && e.code.length === 4) return e.code.charCodeAt(3)
  return map[e.code] ?? null
}

function toLabel(mods: number, vk: number): string {
  const names: [number, string][] = [[2,'Ctrl'],[1,'Alt'],[4,'Shift'],[8,'Win']]
  const parts = names.filter(([m]) => (mods & m) !== 0).map(([, n]) => n)
  parts.push(vkToName(vk) || `VK_${vk}`)
  return parts.join('+')
}

function vkToName(vk: number): string {
  const n: Record<number, string> = {
    0x20:'Space',0x0D:'Enter',0x1B:'Esc',0x09:'Tab',
    0x08:'Backspace',0x2E:'Del',0x2D:'Ins',
    0x21:'PgUp',0x22:'PgDn',0x24:'Home',0x23:'End',
    0x25:'Left',0x26:'Up',0x27:'Right',0x28:'Down',0xC0:'`',
    0x70:'F1',0x71:'F2',0x72:'F3',0x73:'F4',
    0x74:'F5',0x75:'F6',0x76:'F7',0x77:'F8',
    0x78:'F9',0x79:'F10',0x7A:'F11',0x7B:'F12',
    0x30:'0',0x31:'1',0x32:'2',0x33:'3',0x34:'4',
    0x35:'5',0x36:'6',0x37:'7',0x38:'8',0x39:'9',
    0x41:'A',0x42:'B',0x43:'C',0x44:'D',0x45:'E',
    0x46:'F',0x47:'G',0x48:'H',0x49:'I',0x4A:'J',
    0x4B:'K',0x4C:'L',0x4D:'M',0x4E:'N',0x4F:'O',
    0x50:'P',0x51:'Q',0x52:'R',0x53:'S',0x54:'T',
    0x55:'U',0x56:'V',0x57:'W',0x58:'X',0x59:'Y',0x5A:'Z',
  }
  return n[vk] || ''
}

// 全局 keydown 监听（不依赖元素焦点）
function onGlobalKeyDown(e: KeyboardEvent) {
  if (!capturing.value) return

  // Escape 只取消捕获，不关闭页面（不透传到 SettingsModal）
  if (e.code === 'Escape' || e.key === 'Escape') {
    e.preventDefault()
    e.stopImmediatePropagation()
    capturing.value = null
    ResumeHotkeys()
    return
  }

  e.preventDefault()
  e.stopPropagation()

  const vk = toVK(e)
  if (!vk) return

  const mods = (e.ctrlKey ? 2 : 0) | (e.altKey ? 1 : 0) | (e.shiftKey ? 4 : 0) | (e.metaKey ? 8 : 0)
  if ([0x10, 0x11, 0x12, 0x5B, 0x5C].includes(vk)) return

  if (capturing.value === 'app') {
    currentVk.value = vk; currentModifiers.value = mods; currentLabel.value = toLabel(mods, vk)
  } else if (capturing.value === 'clipboard') {
    clipVk.value = vk; clipModifiers.value = mods; clipLabel.value = toLabel(mods, vk)
  } else if (capturing.value === 'palette') {
    paletteVk.value = vk; paletteModifiers.value = mods; paletteLabel.value = toLabel(mods, vk)
  }
  capturing.value = null
  ResumeHotkeys()
}

onMounted(async () => {
  document.addEventListener('keydown', onGlobalKeyDown, true)
  try {
    const c = unwrap<HotkeyConfig>(await GetHotkeyConfig())
    if (c) { currentLabel.value = c.label; currentModifiers.value = c.modifiers; currentVk.value = c.vk }
  } catch {}
  try {
    const c = unwrap<HotkeyConfig>(await GetClipboardHotkeyConfig())
    if (c) { clipLabel.value = c.label; clipModifiers.value = c.modifiers; clipVk.value = c.vk }
  } catch {}
  try {
    const c = unwrap<HotkeyConfig>(await GetPaletteHotkeyConfig())
    if (c) { paletteLabel.value = c.label; paletteModifiers.value = c.modifiers; paletteVk.value = c.vk }
  } catch {}
})

onUnmounted(() => {
  document.removeEventListener('keydown', onGlobalKeyDown, true)
  clearMsgTimer()
  if (capturing.value) {
    capturing.value = null
    ResumeHotkeys()
  }
})

async function startCapture(type: 'app' | 'clipboard' | 'palette') {
  if (capturing.value === type) {
    capturing.value = null
    await ResumeHotkeys()
    return
  }
  capturing.value = type
  message.value = ''
  await SuspendHotkeys()
}

async function saveAll() {
  message.value = ''

  // 检查三个热键两两冲突
  const pairs = [
    [currentModifiers.value, currentVk.value],
    [clipModifiers.value, clipVk.value],
    [paletteModifiers.value, paletteVk.value],
  ]
  for (let i = 0; i < pairs.length; i++) {
    for (let j = i + 1; j < pairs.length; j++) {
      if (pairs[i][0] === pairs[j][0] && pairs[i][1] === pairs[j][1]) {
        setMsgAndClear(t('hotkeyConflict'), 3000)
        return
      }
    }
  }

  try {
    await SetHotkeyConfig(currentModifiers.value, currentVk.value)
    await SetClipboardHotkeyConfig(clipModifiers.value, clipVk.value)
    await SetPaletteHotkeyConfig(paletteModifiers.value, paletteVk.value)
    setMsgAndClear(t('hotkeySaved'), 2000)
  } catch (e) { message.value = t('saveFailed2') + ': ' + getErrorMessage(e) }
}

async function resetAppDefault() {
  currentModifiers.value = 2; currentVk.value = 32; currentLabel.value = 'Ctrl+Space'
  try { await SetHotkeyConfig(2, 32); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

async function resetClipDefault() {
  clipModifiers.value = 2; clipVk.value = 0xC0; clipLabel.value = 'Ctrl+`'
  try { await SetClipboardHotkeyConfig(2, 0xC0); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

async function resetPaletteDefault() {
  paletteModifiers.value = 2; paletteVk.value = 0x4B; paletteLabel.value = 'Ctrl+K'
  try { await SetPaletteHotkeyConfig(2, 0x4B); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

// 暴露 capturing 状态给父组件（SettingsModal）
defineExpose({ capturing })
</script>

<template>
  <div class="hotkey-page">
    <h3 class="page-title">{{ t('hotkeySettings') }}</h3>

    <div class="hotkey-card">
      <div class="hc-title">{{ t('globalActivate') }}</div>
      <div class="hotkey-row">
        <span class="hotkey-label">{{ t('shortcut') }}</span>
        <div
          :class="['hotkey-display', { capturing: capturing === 'app' }]"
          @click="startCapture('app')"
        >
          <template v-if="capturing === 'app'">
            <span class="capture-hint">{{ t('pressKeys') }}</span>
          </template>
          <template v-else>
            <span class="hotkey-badge">{{ currentLabel }}</span>
            <span class="hotkey-edit-hint">{{ t('clickToModify') }}</span>
          </template>
        </div>
        <button class="reset-sm" @click="resetAppDefault" :title="t('restoreDefault')">
          <RotateCcw :size="12" />
        </button>
      </div>
      <p class="hc-desc">{{ t('hotkeyDesc') }}</p>
    </div>

    <div class="hotkey-card">
      <div class="hc-title">{{ t('clipboardHotkey') }}</div>
      <div class="hotkey-row">
        <span class="hotkey-label">{{ t('shortcut') }}</span>
        <div
          :class="['hotkey-display', { capturing: capturing === 'clipboard' }]"
          @click="startCapture('clipboard')"
        >
          <template v-if="capturing === 'clipboard'">
            <span class="capture-hint">{{ t('pressKeys') }}</span>
          </template>
          <template v-else>
            <span class="hotkey-badge">{{ clipLabel }}</span>
            <span class="hotkey-edit-hint">{{ t('clickToModify') }}</span>
          </template>
        </div>
        <button class="reset-sm" @click="resetClipDefault" :title="t('restoreDefault')">
          <RotateCcw :size="12" />
        </button>
      </div>
      <p class="hc-desc">{{ t('clipboardHotkeyDesc') }}</p>
    </div>

    <div class="hotkey-card">
      <div class="hc-title">{{ t('paletteHotkey') }}</div>
      <div class="hotkey-row">
        <span class="hotkey-label">{{ t('shortcut') }}</span>
        <div
          :class="['hotkey-display', { capturing: capturing === 'palette' }]"
          @click="startCapture('palette')"
        >
          <template v-if="capturing === 'palette'">
            <span class="capture-hint">{{ t('pressKeys') }}</span>
          </template>
          <template v-else>
            <span class="hotkey-badge">{{ paletteLabel }}</span>
            <span class="hotkey-edit-hint">{{ t('clickToModify') }}</span>
          </template>
        </div>
        <button class="reset-sm" @click="resetPaletteDefault" :title="t('restoreDefault')">
          <RotateCcw :size="12" />
        </button>
      </div>
      <p class="hc-desc">{{ t('paletteHotkeyDesc') }}</p>
    </div>

    <button class="save-btn" @click="saveAll">{{ t('saveAll') }}</button>
    <p v-if="message" class="hotkey-msg">{{ message }}</p>

    <p class="hotkey-tip">
      <Keyboard :size="14" class="tip-icon" />
      {{ t('hotkeyTip') }}
    </p>
  </div>
</template>

<style scoped>
.hotkey-page { padding: 24px 32px; }
.page-title { font-size: 16px; font-weight: 600; color: var(--color-text-primary); margin: 0 0 24px; }
.hotkey-card { background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 10px; padding: 16px 20px; margin-bottom: 16px; }
.hc-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); margin-bottom: 12px; }
.hc-desc { font-size: 11px; color: var(--color-text-disabled); margin: 10px 0 0; }
.hotkey-row { display: flex; align-items: center; gap: 12px; }
.hotkey-label { font-size: 12px; color: var(--color-text-muted); flex-shrink: 0; width: 48px; }
.hotkey-display {
  flex: 1; height: 36px; display: flex; align-items: center; gap: 8px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: 6px;
  padding: 0 12px; cursor: pointer; transition: border-color 0.15s;
  min-width: 0;
}
.hotkey-display:hover { border-color: var(--color-border-focus); }
.hotkey-display.capturing {
  border-color: var(--color-warning); box-shadow: 0 0 0 2px rgba(250,173,20,0.12);
  animation: pulse 1.2s ease-in-out infinite;
}
@keyframes pulse { 0%,100%{opacity:1} 50%{opacity:.7} }
.hotkey-badge {
  font-size: 14px; font-weight: 600; color: var(--color-accent);
  background: var(--color-accent-bg); padding: 1px 8px; border-radius: 4px;
  font-family: 'Consolas',monospace; white-space: nowrap;
}
.hotkey-edit-hint { font-size: 11px; color: var(--color-text-disabled); white-space: nowrap; }
.capture-hint { font-size: 13px; color: var(--color-warning); font-family: 'Consolas',monospace; }
.reset-sm {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled); border-radius: 4px; cursor: pointer;
}
.reset-sm:hover { color: var(--color-text-secondary); background: var(--color-bg-hover); }
.save-btn {
  padding: 8px 24px; border: none; background: var(--color-accent); color: var(--color-accent-text);
  font-size: 13px; border-radius: 6px; cursor: pointer; font-family: inherit;
  margin-bottom: 12px; display: inline-flex; align-items: center; gap: 6px;
}
.save-btn:hover { background: var(--color-accent-hover); }
.hotkey-msg { font-size: 12px; color: var(--color-success); margin: 0 0 12px; }
.hotkey-tip {
  font-size: 12px; color: var(--color-text-disabled); line-height: 1.6;
  display: flex; align-items: flex-start; gap: 6px; margin: 0;
}
.tip-icon { flex-shrink: 0; margin-top: 1px; }
</style>
