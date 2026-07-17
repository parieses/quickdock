<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { StickyNote, Check, Loader2 } from '@lucide/vue'
import { GetNote, SaveNote } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'

const { t } = useI18n()

const note = ref('')
const loaded = ref(false)
const status = ref<'idle' | 'saving' | 'saved'>('idle')
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
onUnmounted(() => { if (timer) clearTimeout(timer) })
</script>

<template>
  <div class="note-standalone">
    <div class="note-head">
      <span class="note-title">
        <StickyNote :size="14" />
        {{ t('noteTitle') }}
      </span>
      <span :class="['note-status', status]">
        <Loader2 v-if="status === 'saving'" :size="12" class="spin" />
        <Check v-else-if="status === 'saved'" :size="12" />
        <template v-if="status === 'saving'">{{ t('noteSaving') }}</template>
        <template v-else-if="status === 'saved'">{{ t('noteSaved') }}</template>
      </span>
    </div>
    <textarea
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
