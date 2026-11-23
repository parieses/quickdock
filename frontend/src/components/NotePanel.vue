<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { StickyNote, Check, Loader2, MessageCircleQuestion, ListTodo, Save } from '@lucide/vue'
import { GetNote, SaveNote } from '../../bindings/quickdock/services/note/noteservice'
import { unwrap } from '../utils/api'
import { useAIAvailability } from '../composables/useAIAvailability'
import { useSaveAs } from '../composables/useSaveAs'
import { createCodeEditor, type CodeEditorHandle } from '../utils/aiCodeEditor'
import { logErr } from '../utils/logger'
import NoteAiPanel from './NoteAiPanel.vue'

const { t } = useI18n()
const sa = useSaveAs()

const note = ref('')
const loaded = ref(false)
// 读取失败必须与「笔记本来是空的」区分：以前失败就 note='' + loaded=true，
// 随后输入触发的自动保存会把后端原有内容整篇覆盖掉。
const loadFailed = ref(false)
const status = ref<'idle' | 'saving' | 'saved' | 'error'>('idle')
// 卸载 flush 失败时的兜底草稿：内容还没落库就关窗，下次打开优先恢复，别静默丢内容。
const NOTE_DRAFT_KEY = 'qd_note_unsaved_draft'
// 编辑器宿主（textarea → CodeMirror：markdown 便签也能看出结构）
const editorHost = ref<HTMLElement | null>(null)
let editor: CodeEditorHandle | null = null
let timer: ReturnType<typeof setTimeout> | null = null
let pending = ''

async function load() {
  try {
    const s = unwrap<{ content: string }>(await GetNote())
    if (s) note.value = s.content || ''
    loadFailed.value = false
  } catch (e) {
    // 不动 note、不置 loaded：让自动保存保持关闭，等用户重试。
    logErr('NotePanel.load', e)
    loadFailed.value = true
    return
  }
  // 有上次退出时没存成的草稿就恢复，避免 flush 失败的内容被静默吞掉。
  try {
    const draft = localStorage.getItem(NOTE_DRAFT_KEY)
    if (draft) note.value = draft
  } catch (e) {
    logErr('NotePanel.restoreDraft', e)
  }
  loaded.value = true
}

/** 加载失败后的重试：成功后把内容灌进已挂载的编辑器并放开自动保存。 */
async function retryLoad() {
  await load()
  if (!loadFailed.value) {
    editor?.setValue(note.value)
    editor?.focus()
  }
}

/** 统一的落盘出口：unwrap 让后端 code!=0 抛错，失败落 error 态并记日志。 */
async function persist(content: string) {
  try {
    unwrap(await SaveNote(content))
    // 存成功就清掉兜底草稿，免得下次打开又把旧内容盖回来。
    try { localStorage.removeItem(NOTE_DRAFT_KEY) } catch (e) { logErr('NotePanel.clearDraft', e) }
    status.value = 'saved'
    setTimeout(() => { if (status.value === 'saved') status.value = 'idle' }, 1500)
  } catch (e) {
    status.value = 'error'
    logErr('NotePanel.save', e)
  }
}

function scheduleSave() {
  if (!loaded.value || loadFailed.value) return
  status.value = 'saving'
  pending = note.value
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => { void persist(pending) }, 500)
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
      onSave: () => { void persist(note.value) },
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
    // flush 防抖 pending 内容；失败时留草稿兜底，别把没存成的内容静默丢掉。
    void SaveNote(note.value).then((r) => unwrap(r)).catch((e) => {
      try { localStorage.setItem(NOTE_DRAFT_KEY, note.value) } catch (se) { logErr('NotePanel.draft', se) }
      logErr('NotePanel.flush', e)
    })
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

// 选中段落 → 待办 / 另存为。与问 AI 同一套取选区逻辑（CM 选区不随失焦丢失）。
function selectedText(): string {
  return (editor?.getSelection()?.text || '').trim()
}
function selToTodo() {
  const text = selectedText()
  if (!text) return
  void sa.saveToTodo({ content: text })
}
function selSaveAs(e: MouseEvent) {
  const text = selectedText()
  if (!text) return
  sa.open(
    { text, source: 'note', sourceMeta: { label: text.split('\n')[0]?.slice(0, 40) } },
    { x: e.clientX, y: e.clientY },
  )
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
        <button class="note-ask" :title="t('noteSelToTodo')" @click="selToTodo">
          <ListTodo :size="13" />
          {{ t('noteSelToTodo') }}
        </button>
        <button class="note-ask" :title="t('saveAsTitle')" @click="selSaveAs">
          <Save :size="13" />
          {{ t('saveAsTitle') }}
        </button>
        <button class="note-ask" :disabled="!note.trim()" :title="t('noteAskAI')" @click="askAI">
          <MessageCircleQuestion :size="13" />
          {{ t('noteAskAI') }}
        </button>
        <span :class="['note-status', status]">
          <Loader2 v-if="status === 'saving'" :size="12" class="spin" />
          <Check v-else-if="status === 'saved'" :size="12" />
          <template v-if="status === 'saving'">{{ t('noteSaving') }}</template>
          <template v-else-if="status === 'saved'">{{ t('noteSaved') }}</template>
          <template v-else-if="status === 'error'">{{ t('noteSaveFailed') }}</template>
        </span>
      </div>
    </div>
    <div class="note-editor-wrap">
      <div ref="editorHost" class="note-editor"></div>

      <!-- 读不到内容时盖住编辑区：否则用户对着空稿输入，自动保存会把后端原文覆盖掉 -->
      <div v-if="loadFailed" class="note-load-error">
        <span>{{ t('statusError') }}</span>
        <button class="note-retry" @click="retryLoad">{{ t('statusRetry') }}</button>
      </div>
    </div>

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
  padding: var(--space-5) var(--space-6); border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.note-title { display: flex; align-items: center; gap: var(--space-3); font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.note-actions { display: flex; align-items: center; gap: var(--space-4); }
.note-ask {
  display: flex; align-items: center; gap: var(--space-2);
  font-size: var(--text-sm); padding: 3px var(--space-4); border-radius: var(--radius-sm); cursor: pointer;
  background: var(--color-accent); color: var(--color-accent-text); border: none;
}
.note-ask:disabled { opacity: 0.4; cursor: not-allowed; }
.note-status { display: flex; align-items: center; gap: var(--space-2); font-size: var(--text-xs); color: var(--color-text-muted); }
.note-status.saved { color: var(--color-success); }
.note-status.error { color: var(--color-danger); }
.spin { animation: note-spin 1s linear infinite; }
@keyframes note-spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
/* 编辑区：CodeMirror 撑满剩余高度。内边距落在 .cm-content 上，
   与原 textarea 的留白观感一致 */
.note-editor { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: hidden; background: var(--color-bg-primary); }
/* 加载失败蒙层的定位上下文：不用写死页头高度，蒙层正好盖住编辑区 */
.note-editor-wrap { position: relative; flex: 1; min-height: 0; display: flex; }
.note-editor :deep(.cm-editor) { flex: 1; min-height: 0; }
.note-editor :deep(.cm-content) { padding: var(--space-6); }
.note-load-error {
  position: absolute; inset: 0; display: flex; align-items: center; justify-content: center;
  gap: var(--space-4); font-size: var(--text-sm); color: var(--color-text-secondary);
  background: var(--color-bg-primary);
}
.note-retry {
  padding: 3px var(--space-5); border-radius: var(--radius-sm); cursor: pointer;
  font-size: var(--text-sm); color: var(--color-accent-text); background: var(--color-accent); border: none;
}
</style>
