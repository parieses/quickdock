<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Keyboard, X, Copy, Check } from '@lucide/vue'
import {
  GetHotkeyConfig, GetClipboardHotkeyConfig, GetPaletteHotkeyConfig,
  GetNoteHotkeyConfig, GetScreenshotHotkeyConfig, GetSshHotkeyConfig,
} from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { logErr } from '../utils/logger'
import type { HotkeyConfig } from '../types'

const props = defineProps<{
  visible: boolean
  /** 是否显示「在设置里修改」入口。从设置 → 快捷键页打开时传 false（人已经在那里了） */
  showModify?: boolean
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'open-settings', page: string): void
}>()
const { t } = useI18n()

// Esc 关闭：命令面板自己也有 Esc 处理（那边遇备忘单会让路），这里再兜一层，
// 保证从设置页等非面板场景打开时同样能 Esc 退出。
function onEsc(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  e.stopPropagation()
  emit('close')
}
watch(() => props.visible, (v) => {
  if (v) document.addEventListener('keydown', onEsc, true)
  else document.removeEventListener('keydown', onEsc, true)
})
onUnmounted(() => document.removeEventListener('keydown', onEsc, true))

// 全局热键一律实时读后端，不在前端留一份硬编码副本：
// 用户在「设置 → 快捷键」改绑后，备忘单必须显示新的键；留副本就是留一份会说谎的文档。
type Row = { labelKey: string; keys: string }
const globalRows = ref<Row[]>([])

async function loadHotkeys() {
  const defs: { labelKey: string; get: () => Promise<unknown> }[] = [
    { labelKey: 'globalActivate', get: GetHotkeyConfig },
    { labelKey: 'clipboardHotkey', get: GetClipboardHotkeyConfig },
    { labelKey: 'paletteHotkey', get: GetPaletteHotkeyConfig },
    { labelKey: 'noteHotkey', get: GetNoteHotkeyConfig },
    { labelKey: 'screenshotHotkey', get: GetScreenshotHotkeyConfig },
    { labelKey: 'sshHotkey', get: GetSshHotkeyConfig },
  ]
  const rows: Row[] = []
  for (const d of defs) {
    let keys = ''
    try {
      const cfg = unwrap<HotkeyConfig>(await d.get() as never)
      if (cfg?.label) keys = cfg.label
    } catch (e) { logErr('CheatSheet', e) }
    rows.push({ labelKey: d.labelKey, keys })
  }
  globalRows.value = rows
}

watch(() => props.visible, (v) => { if (v) loadHotkeys() }, { immediate: true })

// 命令面板内的键位：这些是交互定义而非用户可配项，故以常量呈现（键帽本身不是文案，走数据不走 i18n）。
const paletteRows: Row[] = [
  { labelKey: 'cheatSheetNavUD', keys: '↑ ↓' },
  { labelKey: 'cheatSheetNavLR', keys: '← →' },
  { labelKey: 'cheatSheetExec', keys: 'Enter' },
  { labelKey: 'cheatSheetActionMenu', keys: 'Ctrl + Enter' },
  { labelKey: 'cheatSheetClose', keys: 'Esc' },
  { labelKey: 'cheatSheetActPick', keys: '1 – 9' },
  { labelKey: 'cheatSheetActEdge', keys: 'Home / End' },
]

// 命令面板语法：给的是可直接照抄的样例，同样不是文案。
const syntaxRows: Row[] = [
  { labelKey: 'cheatSheetCalc', keys: '= 12*8+5' },
  // 单位换算必须带目标单位：utils/calc.ts 的正则要求 to/in/=/->/→ 之一，只输 "1km" 不会命中
  { labelKey: 'cheatSheetUnit', keys: '1km to m' },
  { labelKey: 'cheatSheetQuicklink', keys: '{query}' },
]

const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null

function buildMarkdown(): string {
  const line = (name: string, keys: string) => `| ${name} | \`${keys}\` |`
  const out: string[] = [`## ${t('cheatSheet')}`, '']
  out.push(`### ${t('cheatSheetGlobal')}`, '', line(t('shortcut'), t('cheatSheetAction')), '')
  for (const r of globalRows.value) out.push(line(t(r.labelKey), r.keys || '—'))
  out.push('', `### ${t('cheatSheetInPalette')}`, '', line(t('cheatSheetAction'), t('shortcut')), '')
  for (const r of paletteRows) out.push(line(t(r.labelKey), r.keys))
  out.push('', `### ${t('cheatSheetSyntax')}`, '', line(t('cheatSheetUsage'), t('cheatSheetExample')), '')
  for (const r of syntaxRows) out.push(line(t(r.labelKey), r.keys))
  return out.join('\n')
}

async function copyMarkdown() {
  try {
    await navigator.clipboard.writeText(buildMarkdown())
    copied.value = true
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => { copied.value = false; copyTimer = null }, 1800)
  } catch (e) { logErr('CheatSheet:copy', e) }
}

function goHotkeySettings() {
  emit('close')
  emit('open-settings', 'hotkeys')
}
</script>

<template>
  <div v-if="visible" class="cs-overlay" @click.self="emit('close')">
    <div class="cs-modal" role="dialog" :aria-label="t('cheatSheet')">
      <div class="cs-head">
        <Keyboard :size="16" class="cs-head-icon" />
        <div class="cs-head-text">
          <h3 class="cs-title">{{ t('cheatSheet') }}</h3>
          <p class="cs-desc">{{ t('cheatSheetDesc') }}</p>
        </div>
        <button class="cs-icon-btn" :title="t('cancel')" @click="emit('close')">
          <X :size="16" />
        </button>
      </div>

      <div class="cs-body">
        <section class="cs-group">
          <h4 class="cs-group-title">{{ t('cheatSheetGlobal') }}</h4>
          <div v-for="r in globalRows" :key="r.labelKey" class="cs-row">
            <span class="cs-row-label">{{ t(r.labelKey) }}</span>
            <kbd class="cs-kbd">{{ r.keys || t('cheatSheetUnset') }}</kbd>
          </div>
          <button v-if="showModify !== false" class="cs-link" @click="goHotkeySettings">{{ t('cheatSheetModify') }}</button>
        </section>

        <section class="cs-group">
          <h4 class="cs-group-title">{{ t('cheatSheetInPalette') }}</h4>
          <div v-for="r in paletteRows" :key="r.labelKey" class="cs-row">
            <span class="cs-row-label">{{ t(r.labelKey) }}</span>
            <kbd class="cs-kbd">{{ r.keys }}</kbd>
          </div>
        </section>

        <section class="cs-group">
          <h4 class="cs-group-title">{{ t('cheatSheetSyntax') }}</h4>
          <div v-for="r in syntaxRows" :key="r.labelKey" class="cs-row">
            <span class="cs-row-label">{{ t(r.labelKey) }}</span>
            <kbd class="cs-kbd cs-kbd-sample">{{ r.keys }}</kbd>
          </div>
        </section>
      </div>

      <div class="cs-foot">
        <button class="cs-btn" @click="copyMarkdown">
          <Check v-if="copied" :size="13" />
          <Copy v-else :size="13" />
          <span>{{ copied ? t('cheatSheetCopied') : t('cheatSheetCopy') }}</span>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cs-overlay {
  position: fixed;
  inset: 0;
  background: var(--color-bg-overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 120;
}
.cs-modal {
  display: flex;
  flex-direction: column;
  width: 520px;
  max-width: calc(100vw - 48px);
  max-height: calc(100vh - 64px);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
}
.cs-head {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
  padding: 14px 16px 12px;
  border-bottom: 1px solid var(--color-border);
}
.cs-head-icon { color: var(--color-accent); flex-shrink: 0; margin-top: 2px; }
.cs-head-text { flex: 1; min-width: 0; }
.cs-title { margin: 0; font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.cs-desc { margin: 3px 0 0; font-size: 11px; color: var(--color-text-disabled); line-height: 1.5; }
.cs-icon-btn {
  flex-shrink: 0;
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
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.cs-icon-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.cs-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 4px 16px 12px;
}
.cs-group { padding: 10px 0 4px; }
.cs-group + .cs-group { border-top: 1px solid var(--color-border); }
.cs-group-title {
  margin: 0 0 8px;
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.cs-row {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  min-height: 30px;
  padding: 3px 0;
}
.cs-row-label {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  color: var(--color-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 键帽：与快捷键设置页 .hk-kbd 同一套语言（等宽 + 2px 下沿），保证"看起来是一个软件" */
.cs-kbd {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  /* 统一最小宽度：键帽排成一条竖列，扫读时不需要左右跳（短键如 F1 也占同宽） */
  min-width: 92px;
  height: 24px;
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
  white-space: nowrap;
}
/* 语法样例不是键位，去掉"键帽"的下沿厚度，避免被误读成要按的键 */
.cs-kbd-sample { border-bottom-width: 1px; background: var(--color-bg-hover); font-weight: 500; }
.cs-link {
  margin-top: 6px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--color-accent);
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
}
.cs-link:hover { text-decoration: underline; }

.cs-foot {
  flex-shrink: 0;
  display: flex;
  justify-content: flex-end;
  padding: 10px 16px;
  border-top: 1px solid var(--color-border);
}
.cs-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 12px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px;
  font-family: inherit;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.cs-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); border-color: var(--color-border-focus); }
</style>
