<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Keyboard, RotateCcw, Monitor, Clipboard, Command, FileText, Camera, SquareTerminal } from '@lucide/vue'
import { GetHotkeyConfig, SetHotkeyConfig, GetClipboardHotkeyConfig, SetClipboardHotkeyConfig, GetPaletteHotkeyConfig, SetPaletteHotkeyConfig, GetNoteHotkeyConfig, SetNoteHotkeyConfig, GetScreenshotHotkeyConfig, SetScreenshotHotkeyConfig, GetSshHotkeyConfig, SetSshHotkeyConfig } from '../../bindings/quickdock/services/appservice'
import {
  SuspendHotkeys,
  ResumeHotkeys,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import CheatSheetModal from './CheatSheetModal.vue'
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
const noteLabel = ref('Ctrl+Alt+N')
const noteModifiers = ref(3)
const noteVk = ref(0x4E)
// 截图默认 F1：无修饰键，故 modifiers 为 0（后端 parseHotkeySetting 已支持 mods=0）
const shotLabel = ref('F1')
const shotModifiers = ref(0)
const shotVk = ref(0x70)
// SSH 终端窗口默认 Ctrl+Alt+S：与 tray.go 的 SSH_ACCEL 默认值一致
const sshLabel = ref('Ctrl+Alt+S')
const sshModifiers = ref(3)
const sshVk = ref(0x53)
const capturing = ref<'app' | 'clipboard' | 'palette' | 'note' | 'screenshot' | 'ssh' | null>(null)
const showCheatSheet = ref(false)
const message = ref('')
// 提示色调：冲突/失败是 err（红），保存成功/恢复默认是 ok（绿）。
// 旧版一律绿色，结果"热键冲突"这条报错看起来像成功提示。
const msgTone = ref<'ok' | 'err'>('ok')
const msgTimer = ref<ReturnType<typeof setTimeout> | null>(null)

function clearMsgTimer() {
  if (msgTimer.value !== null) {
    clearTimeout(msgTimer.value)
    msgTimer.value = null
  }
}

function setMsgAndClear(text: string, delay: number, tone: 'ok' | 'err' = 'ok') {
  clearMsgTimer()
  msgTone.value = tone
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
  } else if (capturing.value === 'ssh') {
    sshVk.value = vk; sshModifiers.value = mods; sshLabel.value = toLabel(mods, vk)
  }
  capturing.value = null
  ResumeHotkeys()
}

onMounted(async () => {
  document.addEventListener('keydown', onGlobalKeyDown, true)
  try {
    const c = unwrap<HotkeyConfig>(await GetHotkeyConfig())
    if (c) { currentLabel.value = c.label; currentModifiers.value = c.modifiers; currentVk.value = c.vk }
  } catch (e) { logErr('HotkeySettings', e) }
  try {
    const c = unwrap<HotkeyConfig>(await GetClipboardHotkeyConfig())
    if (c) { clipLabel.value = c.label; clipModifiers.value = c.modifiers; clipVk.value = c.vk }
  } catch (e) { logErr('HotkeySettings', e) }
  try {
    const c = unwrap<HotkeyConfig>(await GetPaletteHotkeyConfig())
    if (c) { paletteLabel.value = c.label; paletteModifiers.value = c.modifiers; paletteVk.value = c.vk }
  } catch (e) { logErr('HotkeySettings', e) }
  try {
    const c = unwrap<HotkeyConfig>(await GetNoteHotkeyConfig())
    if (c) { noteLabel.value = c.label; noteModifiers.value = c.modifiers; noteVk.value = c.vk }
  } catch (e) { logErr('HotkeySettings', e) }
  try {
    const c = unwrap<HotkeyConfig>(await GetScreenshotHotkeyConfig())
    if (c) { shotLabel.value = c.label; shotModifiers.value = c.modifiers; shotVk.value = c.vk }
  } catch (e) { logErr('HotkeySettings', e) }
  try {
    const c = unwrap<HotkeyConfig>(await GetSshHotkeyConfig())
    if (c) { sshLabel.value = c.label; sshModifiers.value = c.modifiers; sshVk.value = c.vk }
  } catch (e) { logErr('HotkeySettings', e) }
})

onUnmounted(() => {
  document.removeEventListener('keydown', onGlobalKeyDown, true)
  clearMsgTimer()
  if (capturing.value) {
    capturing.value = null
    ResumeHotkeys()
  }
})

async function startCapture(type: 'app' | 'clipboard' | 'palette' | 'note' | 'screenshot' | 'ssh') {
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

  // 检查六个热键两两冲突
  const pairs = [
    [currentModifiers.value, currentVk.value],
    [clipModifiers.value, clipVk.value],
    [paletteModifiers.value, paletteVk.value],
    [noteModifiers.value, noteVk.value],
    [shotModifiers.value, shotVk.value],
    [sshModifiers.value, sshVk.value],
  ]
  for (let i = 0; i < pairs.length; i++) {
    for (let j = i + 1; j < pairs.length; j++) {
      if (pairs[i][0] === pairs[j][0] && pairs[i][1] === pairs[j][1]) {
        setMsgAndClear(t('hotkeyConflict'), 3000, 'err')
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
    await SetSshHotkeyConfig(sshModifiers.value, sshVk.value)
    // 重新注册全局快捷键（含调色板/笔记/SSH 窗口），使新配置立即生效。
    await ResumeHotkeys()
    setMsgAndClear(t('hotkeySaved'), 2000)
  } catch (e) {
    msgTone.value = 'err'
    message.value = t('saveFailed2') + ': ' + getErrorMessage(e)
  }
}

async function resetAppDefault() {
  currentModifiers.value = 2; currentVk.value = 32; currentLabel.value = 'Ctrl+Space'
  try { await SetHotkeyConfig(2, 32); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch (e) { logErr('HotkeySettings', e) }
}

async function resetClipDefault() {
  clipModifiers.value = 2; clipVk.value = 0xC0; clipLabel.value = 'Ctrl+`'
  try { await SetClipboardHotkeyConfig(2, 0xC0); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch (e) { logErr('HotkeySettings', e) }
}

async function resetPaletteDefault() {
  paletteModifiers.value = 2; paletteVk.value = 0x4B; paletteLabel.value = 'Ctrl+K'
  try { await SetPaletteHotkeyConfig(2, 0x4B); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch (e) { logErr('HotkeySettings', e) }
}

async function resetNoteDefault() {
  noteModifiers.value = 3; noteVk.value = 0x4E; noteLabel.value = 'Ctrl+Alt+N'
  try { await SetNoteHotkeyConfig(3, 0x4E); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch (e) { logErr('HotkeySettings', e) }
}

async function resetSshDefault() {
  sshModifiers.value = 3; sshVk.value = 0x53; sshLabel.value = 'Ctrl+Alt+S'
  try { await SetSshHotkeyConfig(3, 0x53); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch (e) { logErr('HotkeySettings', e) }
}

async function resetScreenshotDefault() {
  shotModifiers.value = 0; shotVk.value = 0x70; shotLabel.value = 'F1'
  try { await SetScreenshotHotkeyConfig(0, 0x70); await ResumeHotkeys(); setMsgAndClear(t('restoreOk'), 2000) } catch (e) { logErr('HotkeySettings', e) }
}

function isDefault(type: 'app' | 'clipboard' | 'palette' | 'note' | 'screenshot' | 'ssh'): boolean {
  switch (type) {
    case 'app': return currentModifiers.value === 2 && currentVk.value === 32
    case 'clipboard': return clipModifiers.value === 2 && clipVk.value === 0xC0
    case 'palette': return paletteModifiers.value === 2 && paletteVk.value === 0x4B
    case 'note': return noteModifiers.value === 3 && noteVk.value === 0x4E
    case 'screenshot': return shotModifiers.value === 0 && shotVk.value === 0x70
    case 'ssh': return sshModifiers.value === 3 && sshVk.value === 0x53
  }
  return false
}

// 暴露 capturing 状态给父组件（SettingsModal）
defineExpose({ capturing })
</script>

<template>
  <div class="hotkey-page">
    <!-- 页头：标题 + 备忘单入口（备忘单是"看懂这套键"的出口，放在页头而不是页脚，
         因为它不是保存动作的一部分） -->
    <header class="hk-header">
      <h3 class="page-title">{{ t('hotkeySettings') }}</h3>
      <button class="hk-btn hk-btn-ghost" @click="showCheatSheet = true">
        <Keyboard :size="13" />
        <span>{{ t('cheatSheet') }}</span>
      </button>
    </header>

    <!-- 列表而非卡片墙：一行一个热键（图标 / 功能 / 说明 / 键帽 / 重置），
         6 项一屏看完，改绑时不用在卡片之间跳视线 -->
    <section class="hk-panel">
      <div class="hk-panel-head">
        <span class="hk-panel-title">{{ t('cheatSheetGlobal') }}</span>
        <span class="hk-panel-hint">{{ t('clickToModify') }}</span>
      </div>

      <div class="hk-row" :class="{ capturing: capturing === 'app' }">
        <span class="hk-icon"><Monitor :size="14" /></span>
        <span class="hk-name">{{ t('globalActivate') }}</span>
        <span class="hk-desc" :title="t('hotkeyDesc')">{{ t('hotkeyDesc') }}</span>
        <button class="hk-kbd" :class="{ capturing: capturing === 'app' }" :title="t('clickToModify')" @click="startCapture('app')">
          <span v-if="capturing === 'app'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ currentLabel }}</span>
        </button>
        <button class="hk-reset" :class="{ 'is-idle': isDefault('app') }" :title="t('restoreDefault')" @click="resetAppDefault">
          <RotateCcw :size="13" />
        </button>
      </div>

      <div class="hk-row" :class="{ capturing: capturing === 'clipboard' }">
        <span class="hk-icon"><Clipboard :size="14" /></span>
        <span class="hk-name">{{ t('clipboardHotkey') }}</span>
        <span class="hk-desc" :title="t('clipboardHotkeyDesc')">{{ t('clipboardHotkeyDesc') }}</span>
        <button class="hk-kbd" :class="{ capturing: capturing === 'clipboard' }" :title="t('clickToModify')" @click="startCapture('clipboard')">
          <span v-if="capturing === 'clipboard'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ clipLabel }}</span>
        </button>
        <button class="hk-reset" :class="{ 'is-idle': isDefault('clipboard') }" :title="t('restoreDefault')" @click="resetClipDefault">
          <RotateCcw :size="13" />
        </button>
      </div>

      <div class="hk-row" :class="{ capturing: capturing === 'palette' }">
        <span class="hk-icon"><Command :size="14" /></span>
        <span class="hk-name">{{ t('paletteHotkey') }}</span>
        <span class="hk-desc" :title="t('paletteHotkeyDesc')">{{ t('paletteHotkeyDesc') }}</span>
        <button class="hk-kbd" :class="{ capturing: capturing === 'palette' }" :title="t('clickToModify')" @click="startCapture('palette')">
          <span v-if="capturing === 'palette'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ paletteLabel }}</span>
        </button>
        <button class="hk-reset" :class="{ 'is-idle': isDefault('palette') }" :title="t('restoreDefault')" @click="resetPaletteDefault">
          <RotateCcw :size="13" />
        </button>
      </div>

      <div class="hk-row" :class="{ capturing: capturing === 'note' }">
        <span class="hk-icon"><FileText :size="14" /></span>
        <span class="hk-name">{{ t('noteHotkey') }}</span>
        <span class="hk-desc" :title="t('noteHotkeyDesc')">{{ t('noteHotkeyDesc') }}</span>
        <button class="hk-kbd" :class="{ capturing: capturing === 'note' }" :title="t('clickToModify')" @click="startCapture('note')">
          <span v-if="capturing === 'note'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ noteLabel }}</span>
        </button>
        <button class="hk-reset" :class="{ 'is-idle': isDefault('note') }" :title="t('restoreDefault')" @click="resetNoteDefault">
          <RotateCcw :size="13" />
        </button>
      </div>

      <div class="hk-row" :class="{ capturing: capturing === 'screenshot' }">
        <span class="hk-icon"><Camera :size="14" /></span>
        <span class="hk-name">{{ t('screenshotHotkey') }}</span>
        <span class="hk-desc" :title="t('screenshotHotkeyDesc')">{{ t('screenshotHotkeyDesc') }}</span>
        <button class="hk-kbd" :class="{ capturing: capturing === 'screenshot' }" :title="t('clickToModify')" @click="startCapture('screenshot')">
          <span v-if="capturing === 'screenshot'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ shotLabel }}</span>
        </button>
        <button class="hk-reset" :class="{ 'is-idle': isDefault('screenshot') }" :title="t('restoreDefault')" @click="resetScreenshotDefault">
          <RotateCcw :size="13" />
        </button>
      </div>

      <div class="hk-row" :class="{ capturing: capturing === 'ssh' }">
        <span class="hk-icon"><SquareTerminal :size="14" /></span>
        <span class="hk-name">{{ t('sshHotkey') }}</span>
        <span class="hk-desc" :title="t('sshHotkeyDesc')">{{ t('sshHotkeyDesc') }}</span>
        <button class="hk-kbd" :class="{ capturing: capturing === 'ssh' }" :title="t('clickToModify')" @click="startCapture('ssh')">
          <span v-if="capturing === 'ssh'" class="hk-capturing">{{ t('pressKeys') }}</span>
          <span v-else class="hk-keys">{{ sshLabel }}</span>
        </button>
        <button class="hk-reset" :class="{ 'is-idle': isDefault('ssh') }" :title="t('restoreDefault')" @click="resetSshDefault">
          <RotateCcw :size="13" />
        </button>
      </div>
    </section>

    <div class="hk-actions">
      <button class="hk-btn hk-btn-primary" @click="saveAll">{{ t('saveAll') }}</button>
      <p v-if="message" class="hk-msg" :class="{ 'is-error': msgTone === 'err' }">{{ message }}</p>
    </div>

    <p class="hk-tip">
      <Keyboard :size="14" class="tip-icon" />
      {{ t('hotkeyTip') }}
    </p>

    <CheatSheetModal :visible="showCheatSheet" :show-modify="false" @close="showCheatSheet = false" />
  </div>
</template>

<style scoped>
.hotkey-page { width: 100%; max-width: 660px; }

.hk-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  margin-bottom: 14px;
}
.page-title { margin: 0; font-size: 15px; font-weight: 600; color: var(--color-text-primary); }

/* 列表容器：细边框 + 圆角，行间用 1px 分隔线而不再各自成卡，密度更高 */
.hk-panel {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  overflow: hidden;
}
.hk-panel-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-4);
  padding: 8px 12px;
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}
.hk-panel-title { font-size: 11px; font-weight: 600; color: var(--color-text-muted); text-transform: uppercase; letter-spacing: 0.6px; }
.hk-panel-hint { font-size: 11px; color: var(--color-text-disabled); }

/* 单行栅格：图标 / 功能名 / 说明（可伸长可省略） / 键帽 / 重置 */
.hk-row {
  display: grid;
  grid-template-columns: 20px 84px minmax(0, 1fr) auto 26px;
  align-items: center;
  gap: 10px;
  padding: 7px 12px;
  transition: background-color var(--transition-fast);
}
.hk-row + .hk-row { border-top: 1px solid var(--color-border); }
.hk-row:hover { background: var(--color-bg-hover); }
/* 捕获态：整行点亮，让"正在录哪个键"没有歧义 */
.hk-row.capturing { background: var(--color-accent-bg); }

.hk-icon { display: inline-flex; align-items: center; justify-content: center; color: var(--color-text-disabled); }
.hk-row.capturing .hk-icon { color: var(--color-accent); }
.hk-name { font-size: 13px; font-weight: 500; color: var(--color-text-primary); white-space: nowrap; }
.hk-desc {
  font-size: 11.5px;
  color: var(--color-text-disabled);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 键帽：等宽 + 2px 下沿（像真键帽）。宽度按内容走，不再整行铺满 */
.hk-kbd {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 96px;
  height: 26px;
  padding: 0 10px;
  font-family: 'Consolas', ui-monospace, SFMono-Regular, monospace;
  font-size: 12px;
  font-weight: 600;
  line-height: 1;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-bottom-width: 2px;
  border-radius: 6px;
  cursor: pointer;
  transition: border-color var(--transition-fast), background-color var(--transition-fast), color var(--transition-fast), box-shadow var(--transition-fast);
}
.hk-kbd:hover { border-color: var(--color-border-focus); background: var(--color-bg-hover); }
.hk-kbd.capturing {
  border-color: var(--color-accent);
  border-bottom-color: var(--color-accent);
  background: var(--color-surface);
  color: var(--color-accent);
  box-shadow: 0 0 0 2px var(--color-accent-border);
  animation: hk-pulse 1.1s ease-in-out infinite;
}
@keyframes hk-pulse { 0%, 100% { opacity: 1 } 50% { opacity: 0.6 } }
.hk-capturing { font-family: inherit; font-size: 11px; font-weight: 500; }

/* 重置：已是默认值时用 visibility 隐藏而不是 display:none ——
   保留栅格列宽，避免某行"少一个按钮"导致键帽位置左右跳动 */
.hk-reset {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: none;
  background: transparent;
  color: var(--color-text-disabled);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: color var(--transition-fast), background-color var(--transition-fast), opacity var(--transition-fast);
}
.hk-reset:hover { color: var(--color-text-primary); background: var(--color-bg-active); }
.hk-reset.is-idle { visibility: hidden; }

.hk-actions { display: flex; align-items: center; gap: 12px; margin-top: 12px; }
.hk-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 14px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.hk-btn-primary { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }
.hk-btn-primary:hover { background: var(--color-accent-hover); border-color: var(--color-accent-hover); }
.hk-btn-ghost { background: transparent; color: var(--color-text-secondary); border-color: var(--color-border); }
.hk-btn-ghost:hover { color: var(--color-text-primary); background: var(--color-bg-hover); border-color: var(--color-border-focus); }

.hk-msg { margin: 0; font-size: 12px; color: var(--color-success); }
.hk-msg.is-error { color: var(--color-danger); }

.hk-tip {
  margin: 12px 0 0;
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 11px;
  color: var(--color-text-disabled);
  line-height: 1.6;
}
.tip-icon { flex-shrink: 0; margin-top: 1px; }
</style>
