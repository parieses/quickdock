<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, watch, onMounted, onUnmounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { StickyNote, Check, Loader2, MessageCircleQuestion } from '@lucide/vue'
import { GetNote, SaveNote, AskAI } from '../../bindings/quickdock/services/note/noteservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const note = ref('')
const loaded = ref(false)
const status = ref<'idle' | 'saving' | 'saved'>('idle')
const areaRef = ref<HTMLTextAreaElement | null>(null)
let timer: ReturnType<typeof setTimeout> | null = null
let pending = ''

async function load() {
  try {
    const s = unwrap<{ content: string }>(await GetNote())
    if (s) note.value = s.content || ''
  } catch (_) {
    note.value = ''
  }
  loaded.value = true
}

function scheduleSave() {
  if (!loaded.value) return
  status.value = 'saving'
  pending = note.value
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => {
    SaveNote(pending).then(() => {
      status.value = 'saved'
      setTimeout(() => { if (status.value === 'saved') status.value = 'idle' }, 1500)
    }).catch(() => {
      status.value = 'idle'
    })
  }, 500)
}

watch(note, scheduleSave)

onMounted(load)
onUnmounted(() => {
  if (timer) {
    clearTimeout(timer)
    timer = null
    // flush 防抖 pending 内容
    SaveNote(note.value).catch(() => {})
  }
})

// 问 AI：优先取划选文本，否则取整篇笔记。
// 本组件只运行在独立笔记浮窗里，与主窗口是两个 WebView、JS 状态互不可见，
// 所以交给后端处理：隐藏本浮窗 + 唤起主窗口 + 广播文本（App.vue 监听后注入 AI 页）。
// 浮窗内直接 navigateTo('ai') 会把完整主界面渲染进小浮窗，导致样式错乱。
async function askAI() {
  const ta = areaRef.value
  let text = ''
  if (ta && ta.selectionStart !== ta.selectionEnd) {
    text = ta.value.slice(ta.selectionStart, ta.selectionEnd)
  }
  if (!text.trim()) text = note.value
  if (!text.trim()) return
  try {
    unwrap(await AskAI(text))
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('NotePanel', e)
  }
}
</script>

<template>
  <div class="note-standalone">
    <div class="note-head">
      <span class="note-title">
        <StickyNote :size="14" />
        {{ t('noteTitle') }}
      </span>
      <div class="note-actions">
        <button class="note-ask" :disabled="!note.trim()" :title="t('noteAskAI')" @click="askAI">
          <MessageCircleQuestion :size="13" />
          {{ t('noteAskAI') }}
        </button>
        <span :class="['note-status', status]">
          <Loader2 v-if="status === 'saving'" :size="12" class="spin" />
          <Check v-else-if="status === 'saved'" :size="12" />
          <template v-if="status === 'saving'">{{ t('noteSaving') }}</template>
          <template v-else-if="status === 'saved'">{{ t('noteSaved') }}</template>
        </span>
      </div>
    </div>
    <textarea
      ref="areaRef"
      v-model="note"
      class="note-area"
      :placeholder="t('notePlaceholder')"
      spellcheck="false"
    ></textarea>
  </div>
</template>

<style scoped>
.note-standalone {
  height: 100vh; width: 100vw; overflow: hidden;
  display: flex; flex-direction: column;
  background: var(--color-bg-primary);
}
.note-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 10px 14px; border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.note-title { display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.note-actions { display: flex; align-items: center; gap: 10px; }
.note-ask {
  display: flex; align-items: center; gap: 4px;
  font-size: 12px; padding: 3px 8px; border-radius: var(--radius-sm); cursor: pointer;
  background: var(--color-accent); color: var(--color-accent-text); border: none;
}
.note-ask:disabled { opacity: 0.4; cursor: not-allowed; }
.note-status { display: flex; align-items: center; gap: 4px; font-size: 11px; color: var(--color-text-disabled); }
.note-status.saved { color: var(--color-success); }
.spin { animation: note-spin 1s linear infinite; }
@keyframes note-spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
.note-area {
  flex: 1; width: 100%;
  border: none; outline: none; resize: none;
  background: var(--color-bg-primary); color: var(--color-text-primary);
  font-family: var(--font-family);
  font-size: 14px; line-height: 1.6;
  padding: 14px;
  box-sizing: border-box;
}
.note-area::placeholder { color: var(--color-text-disabled); }
</style>
