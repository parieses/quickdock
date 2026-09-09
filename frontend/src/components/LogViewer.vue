<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ScrollText, FolderOpen, RefreshCw, Search, Copy, Check } from '@lucide/vue'
import { ListLogFiles, ReadLogFile } from '../../bindings/quickdock/services/diag/diagservice'
import { OpenLogsDir } from '../../bindings/quickdock/services/system/systemservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

const { t } = useI18n()

interface LogFile { name: string; size: number; time: string }

const files = ref<LogFile[]>([])
const selected = ref('')
const rawLines = ref<string[]>([])
const keyword = ref('')
const levels = ref<Record<string, boolean>>({ I: true, W: true, E: true })
const follow = ref(false)
const loading = ref(false)
let followTimer: ReturnType<typeof setInterval> | null = null
let lastCount = 0

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
    if (!selected.value && files.value.length) selected.value = files.value[0].name
  } catch (_) { /* ignore */ }
}

async function loadContent() {
  if (!selected.value) return
  loading.value = true
  try {
    const res = unwrap<string[]>(await ReadLogFile(selected.value, 3000))
    rawLines.value = res || []
    lastCount = rawLines.value.length
    await nextTick()
    scrollToBottom()
  } catch (e: any) {
    rawLines.value = ['[error] ' + getErrorMessage(e)]
  } finally {
    loading.value = false
  }
}

function scrollToBottom() {
  const el = document.querySelector('.log-body') as HTMLElement | null
  if (el) el.scrollTop = el.scrollHeight
}

function toggleFollow() {
  follow.value = !follow.value
  if (follow.value) {
    followTimer = setInterval(async () => {
      if (!selected.value) return
      try {
        const res = unwrap<string[]>(await ReadLogFile(selected.value, 3000))
        const lines = res || []
        // 仅当新增行时更新，避免无谓重绘
        if (lines.length >= lastCount && lines.slice(0, lastCount).join('\n') === rawLines.value.join('\n')) {
          rawLines.value = lines
          lastCount = lines.length
          await nextTick()
          scrollToBottom()
        } else {
          rawLines.value = lines
          lastCount = lines.length
        }
      } catch (_) { /* ignore */ }
    }, 2000)
  } else if (followTimer) {
    clearInterval(followTimer)
    followTimer = null
  }
}

watch(selected, () => { loadContent() })
watch([levels, keyword], () => { /* 纯前端过滤，computed 自动更新 */ })

const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null
async function copyText(text: string) {
  if (!text) return
  let ok = false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      ok = true
    }
  } catch { /* 落到兜底方案 */ }
  if (!ok) {
    try {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.left = '-9999px'
      document.body.appendChild(ta)
      ta.focus(); ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
      ok = true
    } catch { /* ignore */ }
  }
  if (ok) {
    copied.value = true
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => { copied.value = false }, 1500)
  }
}
function copyAll() { copyText(filteredLines.value.join('\n')) }
function copyLine(l: string) { copyText(l) }

onMounted(async () => {
  await loadFiles()
  await loadContent()
})
onUnmounted(() => { if (followTimer) clearInterval(followTimer) })
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
          v-for="f in files"
          :key="f.name"
          :class="['log-file', { active: f.name === selected }]"
          @click="selected = f.name"
        >
          <span class="log-file-name">{{ f.name }}</span>
          <span class="log-file-meta">{{ (f.size / 1024).toFixed(1) }} KB</span>
        </button>
        <p v-if="!files.length" class="log-empty">{{ t('logEmpty') }}</p>
      </div>
    </aside>

    <section class="log-main">
      <div class="log-toolbar">
        <div class="log-levels">
          <button :class="['lv', 'I', { off: !levels.I }]" @click="levels.I = !levels.I">I</button>
          <button :class="['lv', 'W', { off: !levels.W }]" @click="levels.W = !levels.W">W</button>
          <button :class="['lv', 'E', { off: !levels.E }]" @click="levels.E = !levels.E">E</button>
        </div>
        <div class="log-search">
          <Search :size="13" />
          <input v-model="keyword" :placeholder="t('logSearch')" class="log-search-input" />
        </div>
        <label class="log-follow">
          <input type="checkbox" :checked="follow" @change="toggleFollow" />
          {{ t('logFollow') }}
        </label>
        <button class="log-btn" :disabled="loading" @click="loadContent">
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

      <div class="log-body">
        <div v-for="(l, i) in filteredLines" :key="i" :class="['log-line-row', 'lv-' + levelOf(l)]">
          <pre class="log-line"><span v-html="highlight(l)"></span></pre>
          <button class="line-copy" :title="t('logCopyLine')" @click="copyLine(l)"><Copy :size="12" /></button>
        </div>
        <p v-if="!filteredLines.length" class="log-empty">{{ t('logNoMatch') }}</p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.log-viewer { display: flex; height: 100%; overflow: hidden; background: var(--color-bg-primary); }
.log-side {
  width: 200px; flex-shrink: 0; border-right: 1px solid var(--color-border);
  display: flex; flex-direction: column; background: var(--color-bg-secondary);
}
.log-side-head { display: flex; align-items: center; gap: 6px; padding: 10px 12px; font-size: 13px; font-weight: 600; color: var(--color-text-primary); border-bottom: 1px solid var(--color-border); }
.log-file-list { flex: 1; overflow-y: auto; padding: 6px; }
.log-file { display: flex; flex-direction: column; gap: 2px; width: 100%; text-align: left; padding: 7px 9px; border-radius: 7px; border: none; cursor: pointer; background: transparent; color: var(--color-text-primary); }
.log-file:hover { background: var(--color-hover); }
.log-file.active { background: var(--color-accent); color: var(--color-accent-text); }
.log-file-name { font-size: 12px; font-family: var(--font-family); }
.log-file-meta { font-size: 10px; opacity: 0.6; }
.log-empty { color: var(--color-text-disabled); font-size: 12px; padding: 12px; }

.log-main { flex: 1; display: flex; flex-direction: column; overflow: hidden; }
.log-toolbar { display: flex; align-items: center; gap: 10px; padding: 8px 12px; border-bottom: 1px solid var(--color-border); flex-shrink: 0; }
.log-levels { display: flex; gap: 4px; }
.lv { width: 24px; height: 24px; border-radius: 5px; border: 1px solid var(--color-border); cursor: pointer; font-size: 12px; font-weight: 700; background: var(--color-bg-secondary); color: var(--color-text-primary); }
.lv.off { opacity: 0.3; }
.lv.I { color: #3b9eff; }
.lv.W { color: #e0a92b; }
.lv.E { color: #ff5c5c; }
.log-search { display: flex; align-items: center; gap: 5px; flex: 1; max-width: 280px; padding: 4px 8px; border-radius: 7px; background: var(--color-bg-secondary); color: var(--color-text-disabled); }
.log-search-input { flex: 1; border: none; outline: none; background: transparent; color: var(--color-text-primary); font-size: 13px; }
.log-follow { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--color-text-primary); cursor: pointer; }
.log-btn { display: flex; align-items: center; gap: 4px; font-size: 12px; padding: 4px 9px; border-radius: 7px; cursor: pointer; border: 1px solid var(--color-border); background: var(--color-bg-secondary); color: var(--color-text-primary); }
.log-btn:disabled { opacity: 0.5; cursor: default; }

.log-body { flex: 1; overflow-y: auto; padding: 8px 0; font-family: var(--font-family); font-size: 12px; line-height: 1.55; }
.log-line-row { position: relative; display: flex; align-items: flex-start; }
.log-line { margin: 0; padding: 1px 28px 1px 12px; white-space: pre-wrap; word-break: break-all; color: var(--color-text-primary); flex: 1; }
.line-copy {
  position: absolute; top: 2px; right: 6px; width: 20px; height: 20px;
  display: flex; align-items: center; justify-content: center; cursor: pointer;
  border: 1px solid var(--color-border); border-radius: 5px; background: var(--color-bg-secondary);
  color: var(--color-text-disabled); opacity: 0; transition: opacity var(--transition-fast), color var(--transition-fast), background var(--transition-fast);
}
.log-line-row:hover .line-copy { opacity: 1; }
.line-copy:hover { color: var(--color-accent); border-color: var(--color-accent-border); }
.log-line.lv-W { color: #e0a92b; }
.log-line.lv-E { color: #ff7b7b; }
.log-line :deep(mark) { background: #ffd54a; color: #1a1a1a; border-radius: 2px; padding: 0 1px; }
</style>
