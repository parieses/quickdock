<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Keyboard, RotateCcw } from '@lucide/vue'
import { GetHotkeyConfig, SetHotkeyConfig, GetClipboardHotkeyConfig, SetClipboardHotkeyConfig, GetPaletteHotkeyConfig, SetPaletteHotkeyConfig, GetNoteHotkeyConfig, SetNoteHotkeyConfig, GetScreenshotHotkeyConfig, SetScreenshotHotkeyConfig } from '../../bindings/quickdock/services/appservice'
import {
  SuspendHotkeys,
  ResumeHotkeys,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
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
const noteLabel = ref('Ctrl+Shift+N')
const noteModifiers = ref(6)
const noteVk = ref(0x4E)
// 截图默认 F1：无修饰键，故 modifiers 为 0（后端 parseHotkeySetting 已支持 mods=0）
const shotLabel = ref('F1')
const shotModifiers = ref(0)
const shotVk = ref(0x70)
const capturing = ref<'app' | 'clipboard' | 'palette' | 'note' | 'screenshot' | null>(null)
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
  } else if (capturing.value === 'note') {
    noteVk.value = vk; noteModifiers.value = mods; noteLabel.value = toLabel(mods, vk)
  } else if (capturing.value === 'screenshot') {
    shotVk.value = vk; shotModifiers.value = mods; shotLabel.value = toLabel(mods, vk)
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
  try {
    const c = unwrap<HotkeyConfig>(await GetNoteHotkeyConfig())
    if (c) { noteLabel.value = c.label; noteModifiers.value = c.modifiers; noteVk.value = c.vk }
  } catch {}
  try {
    const c = unwrap<HotkeyConfig>(await GetScreenshotHotkeyConfig())
    if (c) { shotLabel.value = c.label; shotModifiers.value = c.modifiers; shotVk.value = c.vk }
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

async function startCapture(type: 'app' | 'clipboard' | 'palette' | 'note' | 'screenshot') {
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

  // 检查五个热键两两冲突
  const pairs = [
    [currentModifiers.value, currentVk.value],
    [clipModifiers.value, clipVk.value],
    [paletteModifiers.value, paletteVk.value],
    [noteModifiers.value, noteVk.value],
    [shotModifiers.value, shotVk.value],
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
    await SetNoteHotkeyConfig(noteModifiers.value, noteVk.value)
    await SetScreenshotHotkeyConfig(shotModifiers.value, shotVk.value)
    // 重新注册全局快捷键（含调色板/笔记），使新配置立即生效。
    await ResumeHotkeys()
    setMsgAndClear(t('hotkeySaved'), 2000)
  } catch (e) { message.value = t('saveFailed2') + ': ' + getErrorMessage(e) }
}

async function resetAppDefault() {
  currentModifiers.value = 2; currentVk.value = 32; currentLabel.value = 'Ctrl+Space'
  try { await SetHotkeyConfig(2, 32); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

async function resetClipDefault() {
  clipModifiers.value = 2; clipVk.value = 0xC0; clipLabel.value = 'Ctrl+`'
  try { await SetClipboardHotkeyConfig(2, 0xC0); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

async function resetPaletteDefault() {
  paletteModifiers.value = 2; paletteVk.value = 0x4B; paletteLabel.value = 'Ctrl+K'
  try { await SetPaletteHotkeyConfig(2, 0x4B); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

async function resetNoteDefault() {
  noteModifiers.value = 6; noteVk.value = 0x4E; noteLabel.value = 'Ctrl+Shift+N'
  try { await SetNoteHotkeyConfig(6, 0x4E); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

async function resetScreenshotDefault() {
  shotModifiers.value = 0; shotVk.value = 0x70; shotLabel.value = 'F1'
  try { await SetScreenshotHotkeyConfig(0, 0x70); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch {}
}

function isDefault(type: 'app' | 'clipboard' | 'palette' | 'note' | 'screenshot'): boolean {
  switch (type) {
    case 'app': return currentModifiers.value === 2 && currentVk.value === 32
    case 'clipboard': return clipModifiers.value === 2 && clipVk.value === 0xC0
    case 'palette': return paletteModifiers.value === 2 && paletteVk.value === 0x4B
    case 'note': return noteModifiers.value === 6 && noteVk.value === 0x4E
    case 'screenshot': return shotModifiers.value === 0 && shotVk.value === 0x70
  }
  return false
}

// 暴露 capturing 状态给父组件（SettingsModal）
defineExpose({ capturing })
</script>

<template>
  <div class="hotkey-page">
    <h3 class="page-title">{{ t('hotkeySettings') }}</h3>

    <section class="hk-card">
      <div class="hk-card-head">
        <span class="hk-card-title">{{ t('globalActivate') }}</span>
        <span class="hk-card-sub">{{ t('hotkeyDesc') }}</span>
      </div>
      <div class="hk-row">
        <button class="hk-kbd" :class="{ capturing: capturing === 'app' }" :title="t('clickToModify')" @click="startCapture('app')">
          <span v-if="capturing === 'app'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ currentLabel }}</span>
        </button>
        <button v-show="!isDefault('app')" class="hk-reset" :title="t('restoreDefault')" @click="resetAppDefault">
          <RotateCcw :size="12" />
        </button>
      </div>
    </section>

    <section class="hk-card">
      <div class="hk-card-head">
        <span class="hk-card-title">{{ t('clipboardHotkey') }}</span>
        <span class="hk-card-sub">{{ t('clipboardHotkeyDesc') }}</span>
      </div>
      <div class="hk-row">
        <button class="hk-kbd" :class="{ capturing: capturing === 'clipboard' }" :title="t('clickToModify')" @click="startCapture('clipboard')">
          <span v-if="capturing === 'clipboard'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ clipLabel }}</span>
        </button>
        <button v-show="!isDefault('clipboard')" class="hk-reset" :title="t('restoreDefault')" @click="resetClipDefault">
          <RotateCcw :size="12" />
        </button>
      </div>
    </section>

    <section class="hk-card">
      <div class="hk-card-head">
        <span class="hk-card-title">{{ t('paletteHotkey') }}</span>
        <span class="hk-card-sub">{{ t('paletteHotkeyDesc') }}</span>
      </div>
      <div class="hk-row">
        <button class="hk-kbd" :class="{ capturing: capturing === 'palette' }" :title="t('clickToModify')" @click="startCapture('palette')">
          <span v-if="capturing === 'palette'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ paletteLabel }}</span>
        </button>
        <button v-show="!isDefault('palette')" class="hk-reset" :title="t('restoreDefault')" @click="resetPaletteDefault">
          <RotateCcw :size="12" />
        </button>
      </div>
    </section>

    <section class="hk-card">
      <div class="hk-card-head">
        <span class="hk-card-title">{{ t('noteHotkey') }}</span>
        <span class="hk-card-sub">{{ t('noteHotkeyDesc') }}</span>
      </div>
      <div class="hk-row">
        <button class="hk-kbd" :class="{ capturing: capturing === 'note' }" :title="t('clickToModify')" @click="startCapture('note')">
          <span v-if="capturing === 'note'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ noteLabel }}</span>
        </button>
        <button v-show="!isDefault('note')" class="hk-reset" :title="t('restoreDefault')" @click="resetNoteDefault">
          <RotateCcw :size="12" />
        </button>
      </div>
    </section>

    <section class="hk-card">
      <div class="hk-card-head">
        <span class="hk-card-title">{{ t('screenshotHotkey') }}</span>
        <span class="hk-card-sub">{{ t('screenshotHotkeyDesc') }}</span>
      </div>
      <div class="hk-row">
        <button class="hk-kbd" :class="{ capturing: capturing === 'screenshot' }" :title="t('clickToModify')" @click="startCapture('screenshot')">
          <span v-if="capturing === 'screenshot'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ shotLabel }}</span>
        </button>
        <button v-show="!isDefault('screenshot')" class="hk-reset" :title="t('restoreDefault')" @click="resetScreenshotDefault">
          <RotateCcw :size="12" />
        </button>
      </div>
    </section>

    <div class="hk-actions">
      <button class="hk-btn hk-btn-primary" @click="saveAll">{{ t('saveAll') }}</button>
    </div>
    <p v-if="message" class="hk-msg">{{ message }}</p>

    <p class="hk-tip">
      <Keyboard :size="14" class="tip-icon" />
      {{ t('hotkeyTip') }}
    </p>
  </div>
</template>

<style scoped>
.hotkey-page { width: 100%; max-width: 600px; }
.page-title { font-size: 16px; font-weight: 600; color: var(--color-text-primary); margin: 0 0 16px; }

.hk-card {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  padding: 12px 14px;
  margin-bottom: 12px;
}
.hk-card-head { display: flex; align-items: baseline; gap: 8px; margin-bottom: 10px; }
.hk-card-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.hk-card-sub { font-size: 11px; color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.hk-row { display: flex; align-items: center; gap: 10px; }

/* 真实键帽样式（与窗口管理设置页一致） */
.hk-kbd {
  flex: 1;
  min-width: 0;
  height: 28px;
  padding: 0 12px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-family: 'Consolas', ui-monospace, SFMono-Regular, monospace;
  font-size: 13px;
  font-weight: 600;
  line-height: 1;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-bottom-width: 2px;
  border-radius: 6px;
  cursor: pointer;
  transition: border-color 0.12s, background-color 0.12s, color 0.12s, box-shadow 0.12s;
}
.hk-kbd:hover { border-color: var(--color-border-focus); background: var(--color-bg-hover); }
.hk-kbd.capturing {
  border-color: var(--color-accent);
  border-bottom-color: var(--color-accent);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  box-shadow: 0 0 0 2px var(--color-accent-border);
  animation: hk-pulse 1.1s ease-in-out infinite;
}
@keyframes hk-pulse { 0%, 100% { opacity: 1 } 50% { opacity: 0.65 } }
.hk-capturing { font-family: inherit; font-size: 11px; font-weight: 500; }

.hk-reset {
  flex-shrink: 0;
  width: 24px;
  height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  background: transparent;
  color: var(--color-text-disabled);
  border-radius: var(--radius-xs);
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.12s, color 0.12s, background 0.12s;
}
.hk-card:hover .hk-reset { opacity: 1; }
.hk-reset:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.hk-actions { display: flex; gap: 8px; margin-top: 2px; }
.hk-btn {
  padding: 7px 18px;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition: background 0.12s, border-color 0.12s, color 0.12s;
}
.hk-btn-primary { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }
.hk-btn-primary:hover { background: var(--color-accent-hover); border-color: var(--color-accent-hover); }
.hk-msg { font-size: 12px; color: var(--color-success); margin: 10px 0 0; }
.hk-tip {
  font-size: 11px;
  color: var(--color-text-disabled);
  line-height: 1.6;
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 10px 0 0;
}
.tip-icon { flex-shrink: 0; margin-top: 1px; }
</style>
