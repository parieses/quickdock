<script setup lang="ts">
/**
 * 剪贴板条目的「问 AI」面板。
 *
 * 场景：用户在剪贴板历史里看到一段文本（英文段落、一堆日志、一段 JSON），
 * 想问 AI 怎么处理。三个预置动作覆盖绝大多数诉求：
 * 翻译 / 摘要 / 格式化；也能自由提问。
 *
 * 形态是覆盖弹层而不是侧栏：剪贴板面板本身就可能是独立小浮窗
 * （480×540），塞一个 300px 侧栏会把列表挤没。弹层用满整个面板区，
 * 关闭后回到列表。
 *
 * 面板骨架与分段渲染复用通用组件 AiPanel.vue（与 SSH 场景共用），
 * 本文件只负责剪贴板特有的部分：动作按钮、上下文引用条、请求调用。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { FileText, Sparkles } from '@lucide/vue'
import { AskAI } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { unwrap } from '../utils/api'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useAIAvailability } from '../composables/useAIAvailability'
import { useAiStreamPreference } from '../composables/useAiStreamPreference'
import AiPanel, { type AiPanelResult } from './AiPanel.vue'

const props = defineProps<{
  /** 待处理的剪贴板文本。 */
  text: string
}>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()

const busy = ref(false)
const errMsg = ref('')
const result = ref<AiPanelResult | null>(null)
/** 最近几轮的纯文本，用于多轮追问。 */
const history = ref<string[]>([])
/** 上次执行的动作，用于判断"这是追问还是新任务"。 */
const lastAction = ref('')

const panelRef = ref<InstanceType<typeof AiPanel> | null>(null)

/** 当前激活的 AI 档案：多 Key 时回答「这一问用的是哪个」。 */
const { profile: activeProfile, model: activeModel, check: checkAI } = useAIAvailability()

/** 回答方式：关掉流式就传空 streamId，后端退化为一次性返回（请求/返回结构不变）。 */
const { streamEnabled } = useAiStreamPreference()

/**
 * 流式增量与它所属的请求。
 *
 * 按 id 过滤是必须的：ai:ask-delta 全局广播，SSH 面板或另一个剪贴板条目
 * 可能同时在流式输出，不过滤就会把别人的内容拼进这条回答里。
 */
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

/** 每次提问一个独立 id：复用同一个 id 会让上一轮迟到的增量混进新一轮。 */
function newStreamId(): string {
  return `clip-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

/** 三个预置动作。顺序按使用频率排：翻译最常用。 */
const actions = computed(() => [
  { key: 'translate', label: t('clipboardAiTranslate'), icon: Sparkles },
  { key: 'summarize', label: t('clipboardAiSummarize'), icon: FileText },
  { key: 'format', label: t('clipboardAiFormat'), icon: FileText },
])

/** 待处理文本的预览：只给看前两行，别把整段再铺一遍占满弹层。 */
const textPreview = computed(() => {
  const lines = props.text.split('\n')
  return {
    lines: lines.length,
    head: lines.slice(0, 2).map((l) => (l.length > 46 ? l.slice(0, 46) + '…' : l)),
  }
})

/**
 * 执行一次问答。
 *
 * action 为空表示纯自由提问（question 来自输入框）；
 * 否则带上 action（translate / summarize / format）。
 *
 * 追问时会同时带上 action 与 question：后端会把两者组合成
 * 「动作指令 + 在此基础上再满足这一点」，模型才知道当前在做什么。
 */
async function run(action: string, question: string) {
  if (busy.value) return
  busy.value = true
  errMsg.value = ''
  // 新一轮开始：清掉上一轮的增量，并换一个 streamId（旧 id 迟到的增量不再匹配）。
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
        // 关掉流式就传空：后端据此不推增量，等于一次性返回。
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
    logErr('clipboard.ai.ask', e)
    errMsg.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

function askWithAction(action: string) {
  run(action, '')
}

/** 自由提问：输入框里的文字作为问题。 */
function askFree() {
  const q = panelRef.value?.getQuestion()?.trim()
  if (!q) return
  // 追问时带上次的动作：用户说"再短一点"时，模型得知道在处理什么。
  // 第一次提问（还没执行过任何动作）则不带 action，是纯粹的自由提问。
  run(lastAction.value, q)
}

async function copyResult(text: string) {
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
</script>

<template>
  <!--
    覆盖弹层：铺满整个剪贴板面板区。
    不用侧栏是因为这个面板可能只有 480px 宽，侧栏会把列表挤没。
  -->
  <div class="clip-ai">
    <AiPanel
      ref="panelRef"
      :title="t('clipboardAiTitle')"
      :hint="t('clipboardAiHint')"
      :placeholder="t('clipboardAiPlaceholder')"
      :result="result"
      :busy="busy"
      :err-msg="errMsg"
      :live-text="liveText"
      @ask="askFree"
      @clear="clearAll"
      @close="emit('close')"
      @copy="copyResult"
    >
      <!-- 当前使用的 AI 档案：配置了多个 Key 时，用户能看出这一问走的是哪个。 -->
      <template #badge>
        <span
          v-if="activeProfile"
          class="clip-ai-profile"
          :title="t('aiUsingProfile', { profile: activeProfile, model: activeModel })"
        >{{ activeProfile }}</span>
      </template>
      <!-- 待处理文本的引用条：让用户知道这轮处理的是哪段内容。 -->
      <template #context>
        <div class="clip-ai-quote">
          <div class="clip-ai-quote-head">
            <FileText :size="12" />
            <span>{{ t('clipboardAiQuoteLabel', { n: textPreview.lines }) }}</span>
          </div>
          <div v-for="(l, i) in textPreview.head" :key="i" class="clip-ai-quote-line">{{ l }}</div>
        </div>
      </template>

      <!-- 三个预置动作：点一下就走完，不用先想好该怎么说。 -->
      <template #actions>
        <div class="clip-ai-actions">
          <button
            v-for="a in actions"
            :key="a.key"
            class="clip-ai-action"
            :disabled="busy"
            @click="askWithAction(a.key)"
          >
            <component :is="a.icon" :size="12" />
            {{ a.label }}
          </button>
        </div>
      </template>

      <!--
        剪贴板场景的代码块是「给人看的代码」，不是「要执行的命令」：
        所以只有复制按钮，没有填入/执行。
      -->
      <template #cmdActions>
        <span class="clip-ai-cmd-hint">{{ t('clipboardAiCmdHint') }}</span>
      </template>
    </AiPanel>
  </div>
</template>

<style scoped>
/*
 * 覆盖层：铺满剪贴板面板区。
 * position: absolute 是必须的——面板自身是 flex 列布局，
 * 直接插一个子元素会把列表挤到下面，而不是盖在上面。
 */
.clip-ai {
  position: absolute;
  inset: 0;
  z-index: 50;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
}

/* 三个动作按钮：排成一行，窄面板里也能放下。 */
.clip-ai-actions {
  display: flex;
  gap: 2px;
  flex-wrap: wrap;
  /* 负偏移让按钮左缘对齐内容文本，与 SSH 面板的命令操作条同一处理 */
  margin-left: -5px;
}

/*
 * 动作按钮沿用 SSH 面板的视觉语言：无边框 + 弱化文字色，hover 才浮出背景。
 *
 * 之前是描边小胶囊，在 480px 的弹层里三个连排像一排"必须点的主按钮"，
 * 和面板整体（扁平、图标按钮）不搭。这些只是快捷入口，主操作是输入框那行。
 */
.clip-ai-action {
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

.clip-ai-action:hover:not(:disabled) {
  color: var(--color-accent);
  background: var(--color-bg-hover);
}

.clip-ai-action:disabled {
  opacity: 0.5;
  cursor: default;
}

/* 当前 AI 档案徽章：中性色，不与风险/状态色抢注意力。 */
.clip-ai-profile {
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

/* 待处理文本的引用条：让用户看见这轮处理挂了什么内容。 */
.clip-ai-quote {
  padding: 5px 7px;
  border: 1px solid var(--color-accent-border);
  border-left: 2px solid var(--color-accent);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}

.clip-ai-quote-head {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--color-accent);
}

.clip-ai-quote-line {
  margin-top: 2px;
  font-family: var(--font-mono);
  font-size: 10.5px;
  line-height: 1.45;
  color: var(--color-text-muted);
  white-space: pre;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 代码块下方的说明。用 disabled 灰而不是警示色：
   这里不是出错，只是"这段代码不会自动执行"。 */
.clip-ai-cmd-hint {
  padding-left: 3px;
  font-size: 11px;
  color: var(--color-text-disabled);
}
</style>
