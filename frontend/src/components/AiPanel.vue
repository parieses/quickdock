<script setup lang="ts">
/**
 * AI 场景面板的通用外壳。
 *
 * 抽出这一层的原因：SSH 终端与剪贴板都需要「就地提问 + 分段渲染」这套交互，
 * 两者的差别只在**上下文从哪来**与**结果能做什么**。把共用的部分留在这里，
 * 各域只提供差异部分，避免每个新场景域重写一遍面板骨架与渲染逻辑。
 *
 * 通用部分：标题栏 / 消息列表 / 分段渲染（说明走 markdown、代码块走卡片）/
 * 复制按钮 / 风险横幅 / 输入框 / 忙碌与错误态。
 *
 * 差异部分（由调用方通过 slot 提供）：
 *   - `badge`     标题旁的徽章（SSH 是环境标签）
 *   - `context`   上下文引用条（SSH 是终端选区，剪贴板是待处理文本）
 *   - `cmdActions` 代码块卡片上的额外操作（SSH 是「填入终端 / 执行」）
 *
 * **分段判定不在这里**：内容由后端分段返回，前端只在后端未返回分段时
 * 走 utils/aiSegments.ts 兜底。判定规则只有一份实现（见该文件头注释）。
 */
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlignLeft, Check, Copy, Loader2, Send, Sparkles, Trash2, Waves, X } from '@lucide/vue'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { splitAiSegments } from '../utils/aiSegments'
import { useAiStreamPreference } from '../composables/useAiStreamPreference'
import { useToast } from '../composables/useToast'

/** 一条代码块（SSH 场景下是命令）。 */
export interface AiPanelCommand {
  text: string
  risk: string
  note: string
}

/** 后端返回的一段内容。 */
export interface AiPanelSegment {
  type: string
  text: string
  commands: AiPanelCommand[] | null
}

/** 面板展示的完整结果。 */
export interface AiPanelResult {
  answer: string
  commands: AiPanelCommand[] | null
  segments: AiPanelSegment[] | null
  model: string
  hasRisk: boolean
  elapsedMs: number
}

const props = withDefaults(defineProps<{
  /** 面板标题。 */
  title: string
  /** 空状态提示语。 */
  hint: string
  /** 空状态的可点示例问题。 */
  samples?: string[]
  /** 空状态底部的补充说明（用于提示隐藏入口）。 */
  emptyTip?: string
  /** 输入框占位文案。 */
  placeholder: string
  /** 结果对象。null 表示还没有发过问。 */
  result: AiPanelResult | null
  /** 是否正在请求中。 */
  busy: boolean
  /** 错误文案，空串表示无错误。 */
  errMsg: string
  /** 是否显示「清空」按钮。 */
  showClear?: boolean
  /** 是否显示「关闭」按钮。 */
  showClose?: boolean
  /** 附加在标题下方的说明行（CSS 类由调用方作用域决定）。 */
  hintClass?: string
  /**
   * 外部预填的输入框内容。
   *
   * 为什么走 prop 而不是暴露一个 setQuestion 方法给父组件调：
   * 父组件在 watch 里预填时，**首次**触发时子组件可能还没挂载
   * （immediate watch 在 setup 阶段执行），此时调方法会打在 null 上——
   * 表现为"第一次右键问 AI，默认问题没填进去"。
   *
   * 为什么带 seq 而不是光给字符串：
   * 预填内容往往是个固定文案（"这段输出是什么意思"），连续两次右键时
   * **字符串值相同**——只 watch 字符串的话第二次不会触发，预填就失效了。
   * 带 seq 后每次右键都是一个新对象，触发条件恒成立。
   */
  /**
   * 流式增量文本（空串表示当前没有在流式）。
   *
   * 后端逐段推 ai:ask-delta，由**调用方**订阅并按 id 过滤后累加到这里——
   * 面板不直接订阅事件：每个域的 streamId 生命周期不同（重发、清空、关闭），
   * 让面板管会多一份和调用方状态重复的账。
   *
   * 有 result 时以 result 为准（增量只是过程态，避免和终态渲染打架）。
   */
  liveText?: string
  draft?: { text: string; seq: number }
}>(), {
  samples: () => [],
  emptyTip: '',
  showClear: true,
  showClose: true,
  hintClass: '',
  liveText: '',
  draft: undefined,
})

const emit = defineEmits<{
  ask: []
  clear: []
  close: []
  copy: [text: string]
}>()

const { t } = useI18n()
const { error: toastError } = useToast()

/**
 * 回答方式偏好（流式 / 一次性）。
 *
 * 开关放在面板头部而不是设置页：它影响的是「这一次读长回答的体感」，
 * 用户改主意往往就在等答案的时候。两个面板共用同一份偏好（模块级单例），
 * 在这里切换，SSH 与剪贴板同时生效。
 */
const { streamEnabled } = useAiStreamPreference()

/**
 * markdown 渲染器（独立实例，不污染全局 marked 配置）。
 *
 * 代码块**不走** marked 的 renderer：代码块由后端分段后单独渲染成可复制卡片，
 * 若这里也渲染一次，同一条内容会在界面上出现两遍。
 */
const md = new Marked({ breaks: true, gfm: true })

const question = ref('')
const copied = ref('')
const listEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLTextAreaElement | null>(null)

const askDisabled = computed(() => props.busy || !question.value.trim())

/**
 * 实际渲染的结果。
 *
 * 流式期间后端还没有最终文本，分段也无从切起，于是用一个「只有正文」的临时结果占位：
 * 渲染会退回 utils/aiSegments 兜底切分，用户能边生成边看到内容。
 * 一旦最终结果到达，props.result 生效，临时结果自然让位（不需要调用方清 liveText，
 * 清了反而会闪一下空白）。
 */
const shownResult = computed<AiPanelResult | null>(() => {
  if (props.result) return props.result
  if (!props.liveText) return null
  return {
    answer: props.liveText,
    commands: null,
    segments: null,
    model: '',
    hasRisk: false,
    elapsedMs: 0,
  }
})

/** 是否正在流式输出：此时不显示"思考中"，也不显示模型/耗时（都还没确定）。 */
const streaming = computed(() => props.busy && !props.result && !!props.liveText)

/**
 * 内容分段：后端已按模型原始顺序切好「说明文字 / 代码块」，
 * 前端只需把文字段渲染成 markdown，代码段直接用后端给的列表。
 *
 * 文字段保留 markdown 原文交给 marked 渲染，因此 **加粗**、行内代码、
 * 列表都按 markdown 显示，不会再出现 `**星号**` 这样的原始标记。
 */
interface RenderSegment {
  kind: 'text' | 'cmd'
  html: string
  commands: AiPanelCommand[]
  /**
   * 风险判定是否可信。兜底自行切分时为 true —— 那时拿不到后端风险标注，
   * 一律按"未知"处理，不显示任何执行类操作，避免 rm -rf 这类命令被一键跑掉。
   */
  riskUnknown: boolean
}

const renderSegments = computed<RenderSegment[]>(() => {
  const segs = shownResult.value?.segments
  if (!segs || !segs.length) {
    // 兜底：后端没给分段（流式过程中必然没有，或旧版本服务）时，退回按围栏切分正文。
    return fallbackSegments(shownResult.value?.answer ?? '')
  }
  const out: RenderSegment[] = []
  for (const seg of segs) {
    if (seg.type === 'cmd') {
      const cmds = seg.commands ?? []
      if (cmds.length) out.push({ kind: 'cmd', html: '', commands: cmds, riskUnknown: false })
      continue
    }
    const body = (seg.text ?? '').trim()
    if (!body) continue
    out.push({
      kind: 'text',
      html: DOMPurify.sanitize(md.parse(body, { async: false }) as string),
      commands: [],
      riskUnknown: false,
    })
  }
  return out
})

/**
 * 兜底切分：后端未返回分段时，自己按围栏把正文与代码块分开。
 *
 * 与后端同一套判定规则（见 utils/aiSegments），避免两边对"哪段是代码块"
 * 的理解不一致，否则同一条内容会在正文和卡片里各出现一次。
 */
function fallbackSegments(raw: string): RenderSegment[] {
  return splitAiSegments(raw).map((seg) => {
    if (seg.kind === 'cmd') {
      return {
        kind: 'cmd' as const,
        html: '',
        commands: seg.commands.map((c) => ({ text: c, risk: '', note: '' })),
        riskUnknown: true,
      }
    }
    return {
      kind: 'text' as const,
      html: DOMPurify.sanitize(md.parse(seg.text, { async: false }) as string),
      commands: [],
      riskUnknown: false,
    }
  })
}

function scrollToEnd() {
  if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight
}

/** 外部组件可调用它把内容滚到底并聚焦输入框。 */
function focusInput() {
  nextTick(() => inputEl.value?.focus())
}

/**
 * 读取当前输入框内容。
 *
 * 为什么由通用面板持有 question 而不通过 v-model 暴露：
 * 通用面板的输入框是**它自己的**实现（不同域的输入框位置与占位不同），
 * 而"发问"这个动作在通用面板里只能触发一个无参事件——真正的入参
 * （SSH 的屏显、剪贴板的待处理文本）由调用方在处理函数里自行补齐。
 * 所以这里给出显式的读写方法，而不是让调用方双向绑定一个内部状态。
 */
function getQuestion(): string {
  return question.value
}

/** 设置输入框内容（用于预填默认问题、示例点击等）。 */
function setQuestion(v: string) {
  question.value = v
}

/** 标记某条内容已被复制，让调用方把按钮切成对勾态。 */
function markCopied(text: string) {
  copied.value = text
  // 复位以便同一条内容可以再次复制；1.5s 足够用户看清反馈。
  window.setTimeout(() => {
    if (copied.value === text) copied.value = ''
  }, 1500)
}

watch(
  () => props.result,
  async () => {
    await nextTick()
    scrollToEnd()
  }
)

// 流式增量：每合批到达一段就跟着滚到底。
// 不做节流——后端按 ~40 字节合批，频率已经低于一帧一次，再加节流只会让滚动卡顿。
watch(
  () => props.liveText,
  async () => {
    await nextTick()
    scrollToEnd()
  }
)

// 外部预填：draft 变化就填进输入框并聚焦。
// watch 的是整个对象（引用变化即触发），所以连续两次填入相同文案也能生效。
// immediate 保证首次挂载时若父组件已带着预填内容（右键直接打开面板的场景），
// 也能把内容送进去——不等父组件的下一次 watch。
watch(
  () => props.draft,
  (v) => {
    if (!v?.text) return
    question.value = v.text
    focusInput()
  },
  { immediate: true }
)

defineExpose({ focusInput, getQuestion, setQuestion, markCopied })
</script>

<template>
  <div class="ai-panel">
    <header class="ai-panel-head">
      <span class="ai-panel-title">
        <Sparkles :size="14" />
        {{ title }}
        <slot name="badge" />
      </span>
      <span class="ai-panel-head-actions">
        <!--
          回答方式开关：一个图标两种状态，不做成文字标签——300px 侧栏里
          标题 + 环境徽章 + 档案徽章已经占满，再塞两个字会把标题挤没。
          图标含义靠 title 说明，hover 即可读到当前状态与点击后的效果。
        -->
        <button
          class="ai-panel-icon ai-panel-stream"
          :class="{ on: streamEnabled }"
          :title="t(streamEnabled ? 'aiStreamOn' : 'aiStreamOff')"
          @click="streamEnabled = !streamEnabled"
        >
          <Waves v-if="streamEnabled" :size="13" />
          <AlignLeft v-else :size="13" />
        </button>
        <button v-if="showClear" class="ai-panel-icon" :title="t('sshAiClear')" @click="emit('clear')">
          <Trash2 :size="13" />
        </button>
        <button v-if="showClose" class="ai-panel-icon" :title="t('sshAiClose')" @click="emit('close')">
          <X :size="13" />
        </button>
      </span>
    </header>

    <div ref="listEl" class="ai-panel-list">
      <div v-if="!result && !busy && !errMsg" class="ai-panel-empty">
        <p>{{ hint }}</p>
        <!-- 示例做成可点击：看到示例还得自己再打一遍，等于没给。 -->
        <ul v-if="samples.length">
          <li v-for="(s, i) in samples" :key="i">
            <button class="ai-panel-sample" @click="question = s; focusInput()">{{ s }}</button>
          </li>
        </ul>
        <p v-if="emptyTip" class="ai-panel-empty-tip">{{ emptyTip }}</p>
      </div>

      <p v-if="errMsg" class="ai-panel-err">{{ errMsg }}</p>

      <template v-if="shownResult">
        <!--
          按模型原始顺序交错渲染：说明文字（markdown）与代码块卡片。
          说明与代码块在模型输出里是交错的，拆成"文字全在上、代码全在下"会
          丢掉对应关系；markdown 渲染后也不再出现 **星号** 与大片空白。
        -->
        <template v-for="(seg, i) in renderSegments" :key="i">
          <div v-if="seg.kind === 'text'" class="ai-panel-answer" v-html="seg.html" />

          <template v-else>
            <!-- 风险提示紧贴卡片：先看到警告，再决定要不要复制。 -->
            <p v-if="seg.commands.some((c) => c.risk === 'high')" class="ai-panel-risk">
              {{ t('sshAiRiskBanner') }}
            </p>

            <div
              v-for="(c, ci) in seg.commands"
              :key="ci"
              class="ai-panel-cmd"
              :class="{ danger: c.risk === 'high' }"
            >
              <code class="ai-panel-cmd-text">{{ c.text }}</code>
              <button class="ai-panel-copy" :title="t('sshAiCopy')" @click="emit('copy', c.text)">
                <Check v-if="copied === c.text" :size="13" />
                <Copy v-else :size="13" />
              </button>

              <!--
                风险说明必须排在操作按钮**之前**：
                遵循本项目一贯的顺序——先看到警告，再决定要不要动手。
                放在按钮之后就成了"先点了再看"，等于没警告。
              -->
              <p v-if="c.note" class="ai-panel-cmd-note">{{ c.note }}</p>

              <!--
                没有 cmdActions 插槽就不渲染这一行：本地域（日志/端口归因）
                只给复制，不需要任何额外动作；留一个空 div 会多出一条无意义的间距。
              -->
              <div v-if="$slots.cmdActions" class="ai-panel-cmd-actions">
                <slot name="cmdActions" :command="c" :riskUnknown="seg.riskUnknown" />
              </div>
            </div>
          </template>
        </template>

        <!-- 流式光标：让"还在生成"和"已结束但没内容"在视觉上分得开 -->
        <span v-if="streaming" class="ai-panel-caret" />

        <p v-if="!streaming && shownResult.model" class="ai-panel-meta">{{ shownResult.model }} · {{ (shownResult.elapsedMs / 1000).toFixed(1) }}s</p>
      </template>

      <div v-if="busy && !streaming" class="ai-panel-busy">
        <Loader2 :size="14" class="spin" />
        <span>{{ t('sshAiThinking') }}</span>
      </div>
    </div>

    <footer class="ai-panel-foot">
      <!-- 上下文引用条由调用方提供（SSH 是终端选区，剪贴板是待处理文本）。 -->
      <slot name="context" />

      <!-- 快捷动作区（剪贴板的翻译/摘要/格式化）。 -->
      <slot name="actions" />

      <div class="ai-panel-input-row">
        <textarea
          ref="inputEl"
          v-model="question"
          class="ai-panel-input"
          rows="2"
          :placeholder="placeholder"
          :disabled="busy"
          @keydown.enter.exact.prevent="emit('ask')"
        />
        <button class="ai-panel-send" :disabled="askDisabled" @click="emit('ask')">
          <Loader2 v-if="busy" :size="14" class="spin" />
          <Send v-else :size="14" />
        </button>
      </div>
    </footer>
  </div>
</template>

<style scoped>
/*
 * 面板自身不设宽度与边框——由调用方的容器决定形态。
 * SSH 用它做 300px 侧栏（容器加 border-left），剪贴板用它做覆盖弹层。
 * 宽度写死在这里的话，弹层形态就套不上了。
 */
.ai-panel {
  display: flex;
  flex-direction: column;
  width: 100%;
  min-width: 0;
  background: var(--color-bg-primary);
  overflow: hidden;
}

.ai-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.ai-panel-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-primary);
}

.ai-panel-head-actions {
  display: flex;
  gap: 2px;
}

.ai-panel-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ai-panel-icon:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

/* 回答方式开关：流式开启时用 accent 色点出来，不必 hover 读 title 才知道当前状态。
   一次性返回保持默认灰——那是个"降级"状态，不该比流式更显眼。 */
.ai-panel-stream.on,
.ai-panel-stream.on:hover {
  color: var(--color-accent);
}

.ai-panel-list {
  flex: 1;
  overflow-y: auto;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.ai-panel-empty {
  color: var(--color-text-muted);
  font-size: 12px;
  line-height: 1.6;
}

.ai-panel-empty p {
  margin: 0 0 6px;
}

.ai-panel-empty ul {
  margin: 0;
  padding-left: 16px;
  color: var(--color-text-disabled);
}

/* 示例问题：做成链接质感的按钮而不是又一个实心块——
   空状态的这几行是"提示"不是"操作入口"，不该跟发送按钮抢层级。 */
.ai-panel-sample {
  padding: 0;
  font: inherit;
  text-align: left;
  color: inherit;
  background: transparent;
  border: none;
  cursor: pointer;
}

.ai-panel-sample:hover {
  color: var(--color-accent);
}

.ai-panel-empty-tip {
  margin: 8px 0 0;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
  color: var(--color-text-disabled);
}

.ai-panel-err {
  margin: 0;
  font-size: 12px;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-sm);
  padding: 6px 8px;
  line-height: 1.5;
}

/*
 * 说明文字段落。
 *
 * 内容由 markdown 渲染后经 v-html 注入，因此内部元素不受 scoped 影响，
 * 必须用 :deep() 穿透。段落自身的样式仍走 scoped（v-html 容器本身带 scope 属性）。
 */
.ai-panel-answer {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-primary);
  word-break: break-word;
}

/* markdown 渲染出的块级元素自带 margin，这里统一压平，避免段间距过大。 */
.ai-panel-answer :deep(p) {
  margin: 0 0 4px;
}

.ai-panel-answer :deep(p:last-child) {
  margin-bottom: 0;
}

.ai-panel-answer :deep(ul),
.ai-panel-answer :deep(ol) {
  margin: 4px 0;
  padding-left: 18px;
}

.ai-panel-answer :deep(li) {
  margin: 1px 0;
}

.ai-panel-answer :deep(li > p) {
  margin: 0;
}

.ai-panel-answer :deep(strong) {
  font-weight: 600;
  color: var(--color-text-primary);
}

/* 行内代码：命令名/参数常被模型这样标注，给等宽字体更好认。 */
.ai-panel-answer :deep(code) {
  font-family: var(--font-mono);
  font-size: 11px;
  padding: 1px 4px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  color: var(--color-accent);
}

.ai-panel-answer :deep(a) {
  color: var(--color-accent);
  text-decoration: none;
}

.ai-panel-answer :deep(a:hover) {
  text-decoration: underline;
}

.ai-panel-answer :deep(blockquote) {
  margin: 4px 0;
  padding: 2px 0 2px 8px;
  border-left: 2px solid var(--color-border);
  color: var(--color-text-muted);
}

.ai-panel-answer :deep(blockquote p) {
  margin: 0;
}

.ai-panel-risk {
  margin: 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-sm);
  padding: 5px 8px;
}

.ai-panel-cmd {
  position: relative;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  padding: 6px 28px 6px 8px;
}

.ai-panel-cmd.danger {
  border-color: var(--color-danger-border);
}

.ai-panel-cmd-text {
  display: block;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-primary);
  word-break: break-all;
  white-space: pre-wrap;
}

.ai-panel-copy {
  position: absolute;
  top: 4px;
  right: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ai-panel-copy:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

/* 代码块操作条。
 *
 * 刻意做成**轻量工具栏**而不是实心按钮：这个面板的既有语言是扁平的
 * （复制按钮就是无边框图标），一个带底色的大按钮会把注意力从内容本身
 * 抢到按钮上——用户要看的是内容，按钮只是顺手的工具。
 * 因此：无边框 + 弱化文字色，hover 才浮出背景。
 */
.ai-panel-cmd-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  /* 负偏移让按钮左缘对齐内容文本，而不是缩进在卡片内边距之后 */
  margin: 4px 0 0 -5px;
}

.ai-panel-meta {
  margin: 0;
  font-size: 11px;
  color: var(--color-text-disabled);
}

/* 流式光标：一个随呼吸闪烁的窄条，比旋转图标更贴近"文字正在长出来"的观感 */
.ai-panel-caret {
  display: inline-block;
  width: 2px;
  height: 12px;
  margin-left: 1px;
  vertical-align: -2px;
  background: var(--color-accent);
  animation: ai-caret-blink 1s steps(2, start) infinite;
}

@keyframes ai-caret-blink {
  0%,
  50% {
    opacity: 1;
  }
  50.01%,
  100% {
    opacity: 0;
  }
}

.ai-panel-busy {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--color-text-muted);
}

/* 纵向：底部「上下文引用条」与「输入行」是上下关系，
   沿用横向 flex 会把三者挤成一排。 */
.ai-panel-foot {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}

.ai-panel-input-row {
  display: flex;
  gap: 6px;
  align-items: flex-end;
}

.ai-panel-input {
  flex: 1;
  resize: none;
  padding: 6px 8px;
  font-family: inherit;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.ai-panel-input:focus {
  outline: none;
  border-color: var(--color-accent);
  box-shadow: 0 0 0 2px var(--color-accent-border);
}

.ai-panel-send {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: none;
  border-radius: var(--radius-sm);
  background: var(--color-accent);
  color: var(--color-accent-text);
  cursor: pointer;
  flex-shrink: 0;
}

.ai-panel-send:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>
