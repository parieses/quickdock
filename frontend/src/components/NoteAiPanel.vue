<script setup lang="ts">
/**
 * 笔记浮窗里的「问 AI」面板。
 *
 * 形态与剪贴板面板一致：**覆盖弹层**，而不是把 AI 页搬进来。
 * 笔记浮窗是个小窗口（约 420×560），塞侧栏会把编辑区挤没；
 * 而且笔记的核心操作是"边看边改"，弹层关掉就回到原文，链路最短。
 *
 * 这里替代的是一条旧链路：点「问 AI」→ 隐藏浮窗 → 唤起主窗口 →
 * 后端广播 note:ask-ai → 主窗口 AI 页注入文本。那条链路横跨三个窗口状态，
 * 任何一环出问题都表现为"点了没反应"，且各窗口 JS 状态互不可见，极难排查。
 * 现在上下文随请求走，一次调用拿回答案。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { AlignLeft, Languages, ListTree, PenLine, CornerDownLeft, Plus, Replace } from '@lucide/vue'
import { AskAI } from '../../bindings/quickdock/services/note/noteservice'
import { unwrap } from '../utils/api'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useAIAvailability } from '../composables/useAIAvailability'
import { useAiStreamPreference } from '../composables/useAiStreamPreference'
import AiPanel, { type AiPanelResult } from './AiPanel.vue'

const props = defineProps<{
  /** 待处理的笔记内容（划选片段，或整篇）。 */
  text: string
  /**
   * 是否提供「写回笔记」操作。
   *
   * 只有笔记管理器页开启：浮窗（NotePanel）里同样能编辑正文，但它是一个
   * 独立小窗，插入位置（光标/选区）与主编辑区不是同一个 DOM，交回主窗口处理
   * 会横跨窗口状态。首页直接写自己的 textarea，链路最短。
   */
  allowWriteBack?: boolean
}>()
const emit = defineEmits<{
  close: []
  writeback: [text: string, mode: 'insert' | 'replace' | 'append']
}>()

const { t } = useI18n()

const busy = ref(false)
const errMsg = ref('')
const result = ref<AiPanelResult | null>(null)
/** 最近几轮的纯文本，用于多轮追问。 */
const history = ref<string[]>([])
/** 上次执行的动作，用于把追问接在同一件事上。 */
const lastAction = ref('')

const panelRef = ref<InstanceType<typeof AiPanel> | null>(null)

/** 当前激活的 AI 档案：多 Key 时回答「这一问用的是哪个」。 */
const { profile: activeProfile, model: activeModel, check: checkAI } = useAIAvailability()
/** 回答方式：关掉流式就传空 streamId，后端退化为一次性返回。 */
const { streamEnabled } = useAiStreamPreference()

const liveText = ref('')
const streamId = ref('')
let offDelta: (() => void) | null = null

onMounted(() => {
  void checkAI()
  offDelta = Events.On('ai:ask-delta', (ev: any) => {
    const d = ev?.data ?? ev
    if (!d || !d.id || d.id !== streamId.value) return
    liveText.value += d.delta ?? ''
  })
})
onUnmounted(() => {
  offDelta?.()
  offDelta = null
})

function newStreamId(): string {
  return `note-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

/** 四个预置动作：覆盖笔记场景的绝大多数诉求。 */
const actions = computed(() => [
  { key: 'summarize', label: t('noteAiSummarize'), icon: ListTree },
  { key: 'polish', label: t('noteAiPolish'), icon: PenLine },
  { key: 'continue', label: t('noteAiContinue'), icon: AlignLeft },
  { key: 'translate', label: t('noteAiTranslate'), icon: Languages },
])

/** 待处理内容的预览：只给前两行，别把整篇铺满弹层。 */
const textPreview = computed(() => {
  const lines = props.text.split('\n')
  return {
    lines: lines.length,
    head: lines.slice(0, 2).map((l) => (l.length > 46 ? l.slice(0, 46) + '…' : l)),
  }
})

async function run(action: string, question: string) {
  if (busy.value) return
  busy.value = true
  errMsg.value = ''
  liveText.value = ''
  streamId.value = newStreamId()
  try {
    const r = unwrap<{
      answer: string
      segments: { type: string; text: string; commands: { text: string; risk: string; note: string }[] | null }[] | null
      model: string
      elapsedMs: number
    }>(
      await AskAI({
        text: props.text,
        action,
        question,
        history: history.value.slice(-6),
        streamId: streamEnabled.value ? streamId.value : '',
      })
    )
    if (!r) throw new Error(t('sshAiFailed'))
    result.value = {
      answer: r.answer,
      commands: null,
      segments: r.segments,
      model: r.model,
      hasRisk: false,
      elapsedMs: r.elapsedMs,
    }
    history.value = [...history.value, question || action, `AI: ${r.answer}`].slice(-12)
    lastAction.value = action
    panelRef.value?.setQuestion('')
  } catch (e) {
    logErr('note.ai.ask', e)
    errMsg.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

function askWithAction(action: string) {
  run(action, '')
}

/** 自由提问；第一次提问不带动作，追问时接在上次的动作上。 */
function askFree() {
  const q = panelRef.value?.getQuestion()?.trim()
  if (!q) return
  run(lastAction.value, q)
}

async function copyResult(text: string) {
  // 统一走 utils/clipboard：WebView2 的 navigator.clipboard 在文档失焦时会被拒绝，
  // 而 AI 面板恰好常在浮窗里被调用，是「点了复制没反应」的高发处。
  if (await copyToClipboard(text)) {
    panelRef.value?.markCopied(text)
    return
  }
  errMsg.value = t('copyFailed')
}

function clearAll() {
  result.value = null
  history.value = []
  errMsg.value = ''
  lastAction.value = ''
  liveText.value = ''
  streamId.value = ''
  panelRef.value?.setQuestion('')
}

/** 把结果交给宿主写回正文；具体落到哪个位置由宿主决定（它才知道光标在哪）。 */
function writeBack(mode: 'insert' | 'replace' | 'append') {
  const answer = result.value?.answer
  if (!answer) return
  emit('writeback', answer, mode)
}
</script>

<template>
  <div class="note-ai">
    <AiPanel
      ref="panelRef"
      :title="t('noteAiTitle')"
      :hint="t('noteAiHint')"
      :placeholder="t('noteAiPlaceholder')"
      :result="result"
      :busy="busy"
      :err-msg="errMsg"
      :live-text="liveText"
      @ask="askFree"
      @clear="clearAll"
      @close="emit('close')"
      @copy="copyResult"
    >
      <template #badge>
        <span
          v-if="activeProfile"
          class="note-ai-profile"
          :title="t('aiUsingProfile', { profile: activeProfile, model: activeModel })"
        >{{ activeProfile }}</span>
      </template>

      <!-- 待处理内容：让用户知道这轮处理的是划选片段还是整篇。 -->
      <template #context>
        <div class="note-ai-quote">
          <div class="note-ai-quote-head">
            <AlignLeft :size="12" />
            <span>{{ t('noteAiQuoteLabel', { n: textPreview.lines }) }}</span>
          </div>
          <div v-for="(l, i) in textPreview.head" :key="i" class="note-ai-quote-line">{{ l }}</div>
        </div>
      </template>

      <template #actions>
        <div class="note-ai-actions">
          <button
            v-for="a in actions"
            :key="a.key"
            class="note-ai-action"
            :disabled="busy"
            @click="askWithAction(a.key)"
          >
            <component :is="a.icon" :size="12" />
            {{ a.label }}
          </button>
        </div>
      </template>

      <!-- 笔记里的代码块是写作内容（示例/提纲），不是要执行的命令。 -->
      <template #cmdActions>
        <span class="note-ai-cmd-hint">{{ t('noteAiCmdHint') }}</span>
      </template>

      <!-- 写回正文：结果只是"看一眼"没有意义，落在文档里才算用上。 -->
      <template v-if="allowWriteBack" #resultActions>
        <button class="note-ai-action" :disabled="busy" @click="writeBack('insert')">
          <CornerDownLeft :size="12" />
          {{ t('noteAiInsertAtCursor') }}
        </button>
        <button class="note-ai-action" :disabled="busy" @click="writeBack('append')">
          <Plus :size="12" />
          {{ t('noteAiAppend') }}
        </button>
        <button class="note-ai-action" :disabled="busy" @click="writeBack('replace')">
          <Replace :size="12" />
          {{ t('noteAiReplaceAll') }}
        </button>
      </template>
    </AiPanel>
  </div>
</template>

<style scoped>
/* 覆盖层：铺满笔记浮窗，关掉就回到原文。 */
.note-ai {
  position: absolute;
  inset: 0;
  z-index: 50;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
}

/* 动作按钮沿用 SSH / 剪贴板的视觉语言：无边框 + hover 浮背景。 */
.note-ai-actions {
  display: flex;
  gap: 2px;
  flex-wrap: wrap;
  margin-left: -5px;
}

.note-ai-action {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 6px;
  font-size: 11px;
  line-height: 1.5;
  font-family: inherit;
  color: var(--color-text-muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.note-ai-action:hover:not(:disabled) {
  color: var(--color-accent);
  background: var(--color-bg-hover);
}

.note-ai-action:disabled {
  opacity: 0.5;
  cursor: default;
}

/* 当前 AI 档案徽章：中性色，不与其它状态色抢注意力。 */
.note-ai-profile {
  max-width: 76px;
  padding: 0 4px;
  font-size: 10px;
  line-height: 15px;
  border-radius: var(--radius-xs);
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 待处理内容引用条，与 SSH 的选区条同一处理。 */
.note-ai-quote {
  padding: 5px 7px;
  border: 1px solid var(--color-accent-border);
  border-left: 2px solid var(--color-accent);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}

.note-ai-quote-head {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--color-accent);
}

.note-ai-quote-line {
  margin-top: 2px;
  font-size: 10.5px;
  line-height: 1.45;
  color: var(--color-text-muted);
  white-space: pre;
  overflow: hidden;
  text-overflow: ellipsis;
}

.note-ai-cmd-hint {
  padding-left: 3px;
  font-size: 11px;
  color: var(--color-text-disabled);
}
</style>
