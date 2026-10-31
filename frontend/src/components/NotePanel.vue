<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { StickyNote, Check, Loader2, MessageCircleQuestion } from '@lucide/vue'
import { GetNote, SaveNote } from '../../bindings/quickdock/services/note/noteservice'
import { unwrap } from '../utils/api'
import { useAIAvailability } from '../composables/useAIAvailability'
import { createCodeEditor, type CodeEditorHandle } from '../utils/aiCodeEditor'
import { logErr } from '../utils/logger'
import NoteAiPanel from './NoteAiPanel.vue'

const { t } = useI18n()

const note = ref('')
const loaded = ref(false)
const status = ref<'idle' | 'saving' | 'saved'>('idle')
// 编辑器宿主（textarea → CodeMirror：markdown 便签也能看出结构）
const editorHost = ref<HTMLElement | null>(null)
let editor: CodeEditorHandle | null = null
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

watch(note, (v) => {
  scheduleSave()
  // 外部改了内容（首次加载、AI 写回）→ 同步进编辑器。
  // 自己敲字时这里也会被调用一次，但 setValue 内部内容相同会直接 return，不必另设标志位
  // （watch 默认 flush 是 pre，标志位在这一刻早就被重置了，写了也是假的）。
  editor?.setValue(v)
})

async function mountEditor() {
  await nextTick()
  const host = editorHost.value
  if (!host) return
  try {
    editor = await createCodeEditor(host, {
      value: note.value,
      filename: 'note.md',
      fontSize: '14px',
      fontFamily: 'var(--font-family)',
      lineWrapping: true,
      // 便签不需要行号，Tab 也该能移出输入框
      lineNumbers: false,
      tabIndent: false,
      placeholder: t('notePlaceholder'),
      onChange: (v) => {
        note.value = v
      },
      onSave: () => { void SaveNote(note.value).catch(() => {}) },
    })
    editor.focus()
  } catch (e) {
    logErr('NotePanel:editor', e)
  }
}

onMounted(async () => {
  await load()
  void mountEditor()
})
onUnmounted(() => {
  if (timer) {
    clearTimeout(timer)
    timer = null
    // flush 防抖 pending 内容
    SaveNote(note.value).catch(() => {})
  }
  editor?.destroy()
  editor = null
})

// 问 AI：优先取划选文本，否则取整篇笔记，然后在本浮窗内打开面板。
//
// 旧实现是把浮窗隐藏、唤起主窗口、经后端广播 note:ask-ai 让 AI 页接住文本。
// 那条链路横跨三个窗口状态（浮窗显隐、主窗口聚焦、AI 页是否已挂载），
// 每一环失效都只表现为"点了没反应"。现在上下文直接随请求走。
const aiOpen = ref(false)
const aiText = ref('')
const { check: checkAI, goToConfig } = useAIAvailability()

async function askAI() {
  // 划选了就问划选的那段，否则问整篇。选区存在 CM 的 state 里，抢焦点也不会丢。
  const sel = editor?.getSelection()
  let text = sel?.text ?? ''
  if (!text.trim()) text = note.value
  if (!text.trim()) return

  // 入口前置：没配 Key 就带去设置页，而不是打开一个注定失败的面板。
  if (!(await checkAI())) {
    goToConfig()
    return
  }
  aiText.value = text
  aiOpen.value = true
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
    <div ref="editorHost" class="note-editor"></div>

    <!-- AI 面板覆盖在编辑区之上：关掉就回到原文，不打断"边看边改"。 -->
    <NoteAiPanel v-if="aiOpen" :text="aiText" @close="aiOpen = false" />
  </div>
</template>

<style scoped>
.note-standalone {
  height: 100vh; width: 100vw; overflow: hidden;
  display: flex; flex-direction: column;
  background: var(--color-bg-primary);
  /* AI 面板是覆盖层（position:absolute），容器必须建立定位上下文 */
  position: relative;
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
/* 编辑区：CodeMirror 撑满剩余高度。内边距落在 .cm-content 上，
   与原来 textarea 的 14px 观感一致 */
.note-editor { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: hidden; background: var(--color-bg-primary); }
.note-editor :deep(.cm-editor) { flex: 1; min-height: 0; }
.note-editor :deep(.cm-content) { padding: 14px; }
</style>
