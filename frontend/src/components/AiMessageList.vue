<script setup lang="ts">
/**
 * 消息区：历史消息列表 + 流式回答 + 错误/空态，以及「回到底部」按钮。
 *
 * 这个组件同时接管了消息容器的滚动子系统——原因不是「顺手」，而是滚动状态
 * (贴底判定 / 是否显示回到按钮 / ResizeObserver) 与容器 DOM 是一件事：
 * 拆成父子两半，就必然有一半要靠事件回传才能读到另一半，反而更容易漏。
 *
 * 根元素 display:contents：它不生成盒子，`.ai-messages` 与「回到底部」按钮
 * 仍然直接参与 .ai-main 的 flex 布局 / 绝对定位，与拆分前的排版一致。
 */
import { nextTick, ref, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ArrowDown, Bot, ChevronDown, Copy, FileText, ListChecks, Play, StickyNote, Undo2, Wrench,
} from '@lucide/vue'
import AgentRunTree from './AgentRunTree.vue'
import AiDiffView from './AiDiffView.vue'
import type { LiveBlock } from '../composables/useAiMarkdownBlocks'
import type {
  AIArtifact, AICheckpointView, AIMessage, AIMsgImage, AIRuleFile, AIStreamStat, AIToolCall,
} from '../types/ai'
import {
  artifactsOf, countToolRounds, jobIdOf, rulesOf, rulesTitle, runIDOf, toolIcon, toolList,
  toolResultLang, toolTarget, toolVerb as aiToolVerb,
} from '../utils/aiTools'
import { planSecs, planStepIndex } from '../utils/aiPlan'

defineProps<{
  hasKey: boolean
  messages: AIMessage[]
  /** 每条回复的用量统计（父组件的 reactive Map，同一份对象，读 .get 仍会追踪） */
  statMap: Map<string, AIStreamStat>
  /** 语言标识：进 v-memo 依赖，切语言要重渲染出新的 title 文案 */
  locale: string
  mode: string
  streaming: boolean
  reasoningText: string
  streamTools: AIToolCall[]
  streamingText: string
  streamBlocks: LiveBlock[]
  runRules: AIRuleFile[]
  streamArtifacts: AIArtifact[]
  errorMsg: string
  canRetry: () => boolean
  renderBlock: (md: string) => string
  renderMessageBlocks: (m: AIMessage) => string[]
  msgStat: (m: AIMessage) => AIStreamStat | null
  formatStat: (s: AIStreamStat) => string
  cpForCall: (tc: AIToolCall) => AICheckpointView | undefined
}>()

const emit = defineEmits<{
  (e: 'open-settings'): void
  (e: 'open-artifact', a: AIArtifact): void
  (e: 'open-img', img: AIMsgImage): void
  (e: 'copy', text: string): void
  (e: 'save-note', text: string): void
  (e: 'restore-checkpoint', cp: AICheckpointView): void
  (e: 'run-plan', m: AIMessage): void
  (e: 'retry'): void
  (e: 'msg-click', ev: MouseEvent): void
  (e: 'msg-mouseup', ev: MouseEvent | KeyboardEvent): void
  (e: 'msg-contextmenu', ev: MouseEvent): void
}>()

const { t } = useI18n()

/** i18n 的 t 由本组件提供，工具动词的翻译在这里接上 */
function toolVerb(name: string): string {
  return aiToolVerb(name, t)
}

// —— 滚动跟随 ——
// 根因：原先用 watch(streamingText) 驱动滚动，但真正改变 DOM 高度的是
// markdown 渲染结果，两者时间点错位，scrollHeight 读到的是旧值。
// 现在改为：ResizeObserver 观察消息容器内容高度变化 → 变化即滚到底。
// 同时记录用户是否主动上滑（stickToBottom），上滑后不再强制抢滚动，改为显示「回到底部」按钮。
const msgArea = ref<HTMLElement | null>(null)
const msgContent = ref<HTMLElement | null>(null)
const stickToBottom = ref(true)
const showJumpBtn = ref(false)
let resizeObs: ResizeObserver | null = null
const BOTTOM_THRESHOLD = 48 // 距底部小于该像素即视为「贴底」

function onMsgScroll() {
  const el = msgArea.value
  if (!el) return
  const dist = el.scrollHeight - el.scrollTop - el.clientHeight
  stickToBottom.value = dist <= BOTTOM_THRESHOLD
  showJumpBtn.value = !stickToBottom.value
}

/**
 * 滚到底部。默认同时把「贴底」状态复位——调用方（发送、切会话、重新生成）
 * 都是「主动把视线带回最新一条」的语义，不复位的话下一次内容增长就不会再跟随。
 */
function scrollToBottom(smooth = false) {
  stickToBottom.value = true
  showJumpBtn.value = false
  if (!msgArea.value) return
  nextTick(() => {
    const e = msgArea.value
    if (!e) return
    if (smooth) {
      e.scrollTo({ top: e.scrollHeight, behavior: 'smooth' })
    } else {
      e.scrollTop = e.scrollHeight
    }
  })
}

// 流式过程中跟随底部：内容追加后把视口拉到底。
// 用户主动上滑查看历史时（stickToBottom=false）不抢滚动，只显示「回到底部」按钮。
//
// 必须走 nextTick：调用时 Vue 还没把新的 markdown 渲染进 DOM，
// 此时读 scrollHeight 拿到的是上一帧的旧高度，滚不到位。
function followBottom() {
  if (!stickToBottom.value) return
  nextTick(() => {
    // 下一帧用户可能已主动上滑，这时不该再抢滚动
    if (!stickToBottom.value) return
    const el = msgArea.value
    if (!el) return
    // 直接写 scrollTop 会触发 onMsgScroll，把贴底状态重新判定为 true —— 正是期望行为
    el.scrollTop = el.scrollHeight
  })
}

function jumpToBottom() {
  stickToBottom.value = true
  showJumpBtn.value = false
  scrollToBottom(true)
}

/**
 * 跳到某条消息。返回是否真的跳了——父组件只在跳成功时才收起大纲面板，
 * 找不到该条（比如消息已被截断）就停在原地，面板也该留着。
 */
function jumpToMsg(id: string): boolean {
  const box = msgArea.value
  if (!box) return false
  const el = box.querySelector<HTMLElement>(`[data-msg-id="${id}"]`)
  if (!el) return false
  // 用 getBoundingClientRect 而不是 offsetTop：消息区的定位祖先不一定就是它自己，
  // offsetTop 是相对最近定位祖先算的，直接拿来当 scrollTop 会偏。
  const top = el.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop
  stickToBottom.value = false
  box.scrollTo({ top: Math.max(0, top - 12), behavior: 'smooth' })
  return true
}

// ResizeObserver 观察消息内容容器：内容变高（流式输出、图片加载、代码块撑开）
// 时把视口拉到底部。这是「AI 一直在回复但视口不跟随」的正确修法——
// 滚动应由 DOM 实际高度变化驱动，而不是由某个响应式变量间接推断。
//
// 绑定必须跟着容器走：首屏是落地页（父组件 isNewTask 时本组件根本不挂载），
// 但子组件重新挂载（切会话、新建任务后再进来）时 msgContent 也可能先后变化，
// 所以用 watch 而不是只在 onMounted 里 observe 一次。
if (typeof ResizeObserver !== 'undefined') {
  resizeObs = new ResizeObserver(() => {
    if (!stickToBottom.value) return
    const el = msgArea.value
    if (!el) return
    el.scrollTop = el.scrollHeight
  })
}

watch(msgContent, (el, prev) => {
  if (!resizeObs) return
  if (prev) resizeObs.unobserve(prev)
  if (el) resizeObs.observe(el)
})

onUnmounted(() => {
  if (resizeObs) { resizeObs.disconnect(); resizeObs = null }
})

// 父组件要用这两个容器做外部扫描（Mermaid / 表格增强），以及把视口带回最新
function areaEl(): HTMLElement | null { return msgArea.value }
function contentEl(): HTMLElement | null { return msgContent.value }

defineExpose({ scrollToBottom, followBottom, jumpToMsg, areaEl, contentEl })
</script>

<template>
  <div ref="msgArea" class="ai-messages" @scroll="onMsgScroll" @mouseup="emit('msg-mouseup', $event)" @keyup="emit('msg-mouseup', $event)" @click="emit('msg-click', $event)" @contextmenu="emit('msg-contextmenu', $event)">
    <div ref="msgContent" class="ai-msg-content-wrap">
      <div v-if="!hasKey" class="ai-need-key">
        <Bot :size="40" />
        <p>{{ t('aiNeedKey') }}</p>
        <button class="ai-go-settings" @click="emit('open-settings')">{{ t('aiGoSettings') }}</button>
      </div>

      <template v-else>
        <!-- v-memo：历史消息在流式期间内容不变，命中缓存即可整棵子树跳过重建。
             依赖数组必须覆盖子树内所有响应式来源，缺一个就会出现「数据变了但界面不变」：
               · m.content / m.reasoning_content —— 正文与思考过程
               · statMap.get(m.id)               —— 该条回复的耗时/token 统计
               · locale                         —— 按钮 title 等文案随语言切换刷新
               · mode                           —— 计划卡片上的「执行」按钮只看当前模式；
                                                   不放进依赖，点完执行它还会留在原地
             编译器生成的代码含 `_cached.key === m.id` 校验，
             切换会话（消息 id 变化）不会误命中旧缓存。 -->
        <div
          v-for="m in messages"
          :key="m.id"
          :data-msg-id="m.id"
          v-memo="[m.content, m.reasoning_content, statMap.get(m.id), locale, (m.imgList?.length ?? 0), m.tool_calls, m.tool_rounds, mode]"
          :class="['ai-msg', m.role]"
        >
          <div class="ai-msg-role">
            {{ m.role === 'user' ? t('aiYou') : 'AI' }}
            <!-- 多档案（不同模型/档案）混着聊时，不写模型名就分不清这句是谁答的 -->
            <span v-if="m.role === 'assistant' && m.model" class="ai-msg-model">{{ m.model }}</span>
          </div>
          <!-- 图片缩略图：url 类直发链接，file 类用已加载的 dataUrl；历史 file 图由 loadMessages 懒加载填回 -->
          <div v-if="m.imgList && m.imgList.length" class="ai-msg-imgs">
            <div v-for="(img, bi) in m.imgList" :key="bi" class="ai-msg-img">
              <img v-if="img.url" :src="img.url" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="emit('open-img', img)" />
              <img v-else-if="img.dataUrl" :src="img.dataUrl" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="emit('open-img', img)" />
              <div v-else class="ai-msg-img-el ai-img-broken">{{ t('aiImageBroken') }}</div>
            </div>
          </div>
          <details v-if="m.reasoning_content" class="ai-reasoning">
            <summary>{{ t('aiThought') }} · {{ t('aiThoughtChars', { n: m.reasoning_content.length }) }}</summary>
            <div class="ai-reasoning-content">{{ m.reasoning_content }}</div>
          </details>
          <!-- 工具调用卡片：放在正文之前，因为调用发生在作答之前。
               默认折叠——一段回答调了五个工具时，全展开会把正文挤出屏幕；
               摘要行自己就说明了「调了几个、几轮」。 -->
          <details v-if="m.role === 'assistant' && toolList(m).length" class="ai-tools">
            <summary class="ai-tools-sum">
              <Wrench :size="12" class="ai-tools-ico" />
              <span>{{ t('aiToolSummary', { n: toolList(m).length, rounds: m.tool_rounds || countToolRounds(toolList(m)) }) }}</span>
              <ChevronDown :size="13" class="ai-tools-chev" />
            </summary>
            <div class="ai-tools-body">
              <!-- 每条调用自带折叠：一次回答调了五六个工具时，用户只想展开
                   其中一条看细节，而不是要么全看要么全不看。 -->
              <details v-for="tc in toolList(m)" :key="tc.id" class="ai-tool" :class="{ 'is-err': !!tc.error }">
                <summary class="ai-tool-line">
                  <component :is="toolIcon(tc.name)" :size="12" class="ai-tool-icon" />
                  <span class="ai-tool-verb">{{ toolVerb(tc.name) }}</span>
                  <span v-if="toolTarget(tc)" class="ai-tool-target" :title="toolTarget(tc)">{{ toolTarget(tc) }}</span>
                  <!-- 增删行数放摘要行：改文件这一件事上，「改了几行」比「成功了」
                       更值得先看到。 -->
                  <span v-if="tc.added || tc.removed" class="ai-tool-diff">
                    <em v-if="tc.added" class="ai-diff-add">+{{ tc.added }}</em>
                    <em v-if="tc.removed" class="ai-diff-del">-{{ tc.removed }}</em>
                  </span>
                  <span v-if="tc.denied" class="ai-tool-ms is-denied">{{ t('aiToolDenied') }}</span>
                  <span v-else-if="tc.dedup" class="ai-tool-ms">{{ t('aiToolDedup') }}</span>
                  <span v-else-if="tc.ms" class="ai-tool-ms">{{ tc.ms }}ms</span>
                  <!-- 回滚到这一步之前：只有真的建过快照的写类调用才有这个按钮。 -->
                  <button v-if="cpForCall(tc)" class="ai-tool-undo" :title="t('aiCheckpointUndo')" @click.stop.prevent="emit('restore-checkpoint', cpForCall(tc)!)">
                    <Undo2 :size="11" />
                  </button>
                </summary>
                <!-- 行级 diff：结果串只说「改成功了」，这里说「改成了什么」。 -->
                <AiDiffView v-if="tc.diff" :diff="tc.diff" />
                <div v-if="tc.error" class="ai-tool-err">{{ tc.error }}</div>
                <!-- 有 diff 时不再重放一句「已编辑 xxx（替换 1 处）」：
                     路径在摘要行上已经有了，改动在 diff 里已经有了。 -->
                <pre v-else-if="tc.result && !tc.diff" class="ai-tool-res" :class="{ 'is-code': !!toolResultLang(tc) }">{{ tc.result }}</pre>
              </details>
            </div>
          </details>
          <!-- 编排 run 树：这条回复派出去的子 agent。只有带 run_id 的回复才渲染，
               否则每一句普通回答下面都会多一个空面板。展开时才拉数据。 -->
          <AgentRunTree v-if="runIDOf(m)" :run-id="runIDOf(m)" />
          <!-- 本次生效的规则文件：一行小字，悬停看文件名与字节数。
               没有这一行的话，「规则改了不生效」在界面上没有任何线索——
               写错位置、被 8KB 截断、cwd 不是想的那一层，表现完全一样。 -->
          <div v-if="rulesOf(m).length" class="ai-rules" :title="rulesTitle(rulesOf(m))">
            <FileText :size="11" />
            <span>{{ t('aiRulesActive', { n: rulesOf(m).length }) }}</span>
            <span class="ai-rules-name">{{ rulesOf(m).map((r) => r.name).join('、') }}</span>
          </div>
          <div v-if="m.role === 'assistant' && planSecs(m).length" class="ai-plan">
            <div class="ai-plan-head">
              <ListChecks :size="13" class="ai-plan-icon" />
              <span class="ai-plan-name">{{ t('aiPlanTitle') }}</span>
              <!-- 「N 步」只数步骤那一节：把涉及文件、风险也算进去，
                   这个数就没有意义了 -->
              <span v-if="planSecs(m)[planStepIndex(planSecs(m))]?.items.length" class="ai-plan-count">
                {{ t('aiPlanSteps', { n: planSecs(m)[planStepIndex(planSecs(m))].items.length }) }}
              </span>
              <!-- 只有计划模式下才给「执行」：切回对话之后这份计划已经执行过了，
                   再点一次等于把同一份要求重发一遍。
                   勾选框有意做成非受控（不绑 :checked）：消息区带 v-memo，
                   绑了状态就得把每个勾选位塞进依赖数组，等于每勾一下重渲染整个列表。 -->
              <button
                v-if="mode === 'plan' && !streaming"
                class="ai-plan-run"
                :title="t('aiPlanRunHint')"
                @click="emit('run-plan', m)"
              >
                <Play :size="11" /> {{ t('aiPlanRun') }}
              </button>
            </div>
            <div v-for="(sec, si) in planSecs(m)" :key="si" class="ai-plan-sec">
              <div class="ai-plan-sec-title">{{ sec.title }}</div>
              <ul v-if="sec.items.length" class="ai-plan-items" :class="{ 'is-step': si === planStepIndex(planSecs(m)) }">
                <li v-for="(it, ii) in sec.items" :key="ii">
                  <input v-if="si === planStepIndex(planSecs(m))" type="checkbox" class="ai-plan-check" />
                  <span class="ai-plan-item-md" v-html="renderBlock(it)"></span>
                </li>
              </ul>
              <div v-if="sec.text" class="ai-plan-text ai-md" v-html="renderBlock(sec.text)"></div>
            </div>
          </div>
          <div v-else-if="m.role === 'assistant'" class="ai-msg-content ai-md">
            <div v-for="(b, bi) in renderMessageBlocks(m)" :key="bi" class="ai-md-block" v-html="b"></div>
          </div>
          <div v-else class="ai-msg-content ai-plain">{{ m.content }}</div>
          <!-- 回答产物：靠右贴边的可折叠列表（同常见 AI IDE 的产物区样式）。
               数据就在 tool_calls 里，不用后端另发事件；点击预览（读磁盘现状）。 -->
          <details v-if="m.role === 'assistant' && artifactsOf(m).length" class="ai-artifacts" open>
            <summary class="ai-artifacts-sum">
              {{ t('aiArtifactsCount', { n: artifactsOf(m).length }) }}
              <ChevronDown :size="12" class="ai-artifacts-chev" />
            </summary>
            <div class="ai-artifacts-list">
              <button v-for="a in artifactsOf(m)" :key="a.path" class="ai-artifact" :title="a.path" @click="emit('open-artifact', a)">
                <FileText :size="13" />
                <span class="ai-artifact-name">{{ a.name }}</span>
                <span class="ai-artifact-op">{{ t(a.op === 'edit' ? 'aiArtifactEdited' : 'aiArtifactWritten') }}</span>
              </button>
            </div>
          </details>
          <!-- 操作按钮同一行：.ai-msg 是纵向 flex，不包一层的话每个按钮都会各占一行 -->
          <div class="ai-msg-actions">
            <!-- 中断标记：内容照样保留（后端也把半截内容连同 stopped 一起落库），
                 但要让人一眼看出「这是我按了停止」，而不是模型只答了这么多 -->
            <span v-if="m.stopped" class="ai-msg-stopped" :title="t('aiStoppedHint')">{{ t('aiStopped') }}</span>
            <button v-if="m.content" class="ai-copy" @click="emit('copy', m.content)" :title="t('copy')">
              <Copy :size="12" /> {{ t('copy') }}
            </button>
            <button v-if="m.role === 'assistant' && m.content" class="ai-copy" @click="emit('save-note', m.content)" :title="t('aiSaveToNote')">
              <StickyNote :size="12" /> {{ t('aiSaveToNote') }}
            </button>
            <!-- 单条回复统计：耗时 / 首字延迟 / token 用量 -->
            <span v-if="msgStat(m)" class="ai-msg-stat">
              {{ formatStat(msgStat(m)!) }}
            </span>
          </div>
        </div>

        <div v-if="reasoningText" class="ai-reasoning">
          <details open>
            <summary>{{ t('aiThinking') }}</summary>
            <div class="ai-reasoning-content">{{ reasoningText }}</div>
          </details>
        </div>
        <!-- 流式区：模型先调工具时还没有正文，这里的 v-if 必须带上 streamTools，
             否则「正在查环境」的那几秒界面上什么都没有，看起来像卡死。 -->
        <div v-if="streamingText || streamTools.length" class="ai-msg assistant streaming">
          <div class="ai-msg-role">AI</div>
          <!-- 与历史卡片同款提示：流式期就能看到「这次带上了哪几个规则文件」，
               不然要等回答完才知道，而那时已经照错的约定做完了。 -->
          <div v-if="runRules.length" class="ai-rules" :title="rulesTitle(runRules)">
            <FileText :size="11" />
            <span>{{ t('aiRulesActive', { n: runRules.length }) }}</span>
            <span class="ai-rules-name">{{ runRules.map((r) => r.name).join('、') }}</span>
          </div>
          <!-- 流式期默认展开：用户需要看到「正在用哪个工具」，这是等待期间唯一的进度信息 -->
          <details v-if="streamTools.length" class="ai-tools" open>
            <summary class="ai-tools-sum">
              <Wrench :size="12" class="ai-tools-ico" />
              <span>{{ t('aiToolSummary', { n: streamTools.length, rounds: countToolRounds(streamTools) }) }}</span>
              <ChevronDown :size="13" class="ai-tools-chev" />
            </summary>
            <div class="ai-tools-body">
              <!-- 正在跑的那条自动展开：等待期间它是唯一的进度信息。 -->
              <details v-for="tc in streamTools" :key="tc.id" class="ai-tool" :class="{ 'is-err': !!tc.error, 'is-run': tc.running, 'is-ask': tc.approval === 'pending' }" :open="!!tc.running || tc.approval === 'pending'">
                <summary class="ai-tool-line">
                  <component :is="toolIcon(tc.name)" :size="12" class="ai-tool-icon" />
                  <span class="ai-tool-verb">{{ toolVerb(tc.name) }}</span>
                  <span v-if="toolTarget(tc)" class="ai-tool-target" :title="toolTarget(tc)">{{ toolTarget(tc) }}</span>
                  <span v-if="tc.added || tc.removed" class="ai-tool-diff">
                    <em v-if="tc.added" class="ai-diff-add">+{{ tc.added }}</em>
                    <em v-if="tc.removed" class="ai-diff-del">-{{ tc.removed }}</em>
                  </span>
                  <span v-if="tc.approval === 'pending'" class="ai-tool-ms is-ask">{{ t('aiApprovalWaiting') }}</span>
                  <span v-else-if="tc.approval === 'denied'" class="ai-tool-ms is-denied">{{ t('aiToolDenied') }}</span>
                  <span v-else-if="tc.running" class="ai-tool-ms">{{ t('aiToolRunning') }}</span>
                  <span v-else-if="tc.dedup" class="ai-tool-ms">{{ t('aiToolDedup') }}</span>
                  <span v-else-if="tc.ms" class="ai-tool-ms">{{ tc.ms }}ms</span>
                  <!-- 流式期也能回滚：改错了最好在那一步刚结束时退回去，而不是等整轮跑完。 -->
                  <button v-if="cpForCall(tc)" class="ai-tool-undo" :title="t('aiCheckpointUndo')" @click.stop.prevent="emit('restore-checkpoint', cpForCall(tc)!)">
                    <Undo2 :size="11" />
                  </button>
                </summary>
                <!-- 审批状态：写类与高危工具会在这里停下来（见后端 ai_approval.go）。
                     卡片只说明「为什么停下来」，按钮统一在输入区上方的审批条里。 -->
                <div v-if="tc.approval === 'pending'" class="ai-approval">
                  <div v-if="tc.riskNote" class="ai-approval-risk">{{ tc.riskNote }}</div>
                  <!-- 按钮不在这里：审批条钉在输入区上方（AiApprovalBar）。
                       两处各有一套按钮时，点完一处的状态同步会慢半拍，
                       反而让人以为没点上又点第二次。 -->
                  <div class="ai-approval-hint">{{ t('aiApprovalPickBelow') }}</div>
                </div>
                <div v-else-if="tc.approval === 'allowed'" class="ai-approval-done">{{ t('aiApprovalAllowed') }}</div>
                <AiDiffView v-if="tc.diff" :diff="tc.diff" />
                <div v-if="tc.error" class="ai-tool-err">{{ tc.error }}</div>
                <pre v-else-if="tc.result && !tc.diff" class="ai-tool-res" :class="{ 'is-code': !!toolResultLang(tc) }">{{ tc.result }}</pre>
                <!-- 后台任务：卡片上明写「关闭应用会终止」。工具描述里也写了同一句，
                     但用户看的是这张卡；不在这里再说一次，关窗口时他一定以为
                     dev server 还在后台跑着。 -->
                <div v-if="jobIdOf(tc)" class="ai-job-note">{{ t('aiJobBgNote', { id: jobIdOf(tc) }) }}</div>
              </details>
            </div>
          </details>
          <div v-if="streamingText" class="ai-msg-content ai-md">
            <!-- key 用「块原文 + 序号」：原文可能重复（两段相同的话），
                 仅用原文会触发 Vue 的重复 key 警告。仅末尾块会变，
                 前面的块 key 稳定 → 不会被重建。 -->
            <div v-for="(b, bi) in streamBlocks" :key="bi + ':' + b.raw.length + ':' + b.raw.slice(0, 24)" class="ai-md-block" v-html="b.html"></div>
          </div>
          <span v-if="streamingText" class="ai-cursor">▋</span>
          <!-- 流式期的产物列表：file_write 一返回就出现，不等回答收尾 -->
          <details v-if="streamArtifacts.length" class="ai-artifacts" open>
            <summary class="ai-artifacts-sum">
              {{ t('aiArtifactsCount', { n: streamArtifacts.length }) }}
              <ChevronDown :size="12" class="ai-artifacts-chev" />
            </summary>
            <div class="ai-artifacts-list">
              <button v-for="a in streamArtifacts" :key="a.path" class="ai-artifact" :title="a.path" @click="emit('open-artifact', a)">
                <FileText :size="13" />
                <span class="ai-artifact-name">{{ a.name }}</span>
                <span class="ai-artifact-op">{{ t(a.op === 'edit' ? 'aiArtifactEdited' : 'aiArtifactWritten') }}</span>
              </button>
            </div>
          </details>
        </div>

        <div v-if="errorMsg" class="ai-error">
          <span class="ai-error-text">{{ errorMsg }}</span>
          <!-- 失败时用户消息已落库，重试就是把它再发一次（图片一起带回去） -->
          <button v-if="canRetry()" class="ai-retry" @click="emit('retry')">{{ t('aiRetry') }}</button>
        </div>

        <div v-if="messages.length === 0 && !streamingText && !errorMsg" class="ai-empty">
          <Bot :size="40" />
          <p>{{ t('aiEmptyHint') }}</p>
        </div>
      </template>
    </div>
  </div>
  <!-- 「回到底部」按钮：用户主动上滑查看历史时出现 -->
  <button v-if="showJumpBtn" class="ai-jump-bottom" @click="jumpToBottom" :title="t('aiBackToBottom')">
    <ArrowDown :size="14" />
  </button>
</template>

<style scoped>
/* 本组件是多根节点（.ai-messages + 「回到底部」按钮），没有包装元素，
   所以两者像拆分前一样直接参与 .ai-main 的 column flex；
   .ai-jump-bottom 的绝对定位锚点仍是 .ai-main。 */

.ai-reasoning {
  font-size: 12px; margin: 2px 0; max-width: 80%;
}

.ai-reasoning details { background: var(--color-bg-active); border-radius: var(--radius-sm); padding: 4px 8px; }

.ai-reasoning summary {
  cursor: pointer; color: var(--color-text-muted); font-size: 11px;
  user-select: none; outline: none;
}

.ai-reasoning summary:hover { color: var(--color-text-primary); }

.ai-reasoning-content {
  margin-top: 4px; padding-top: 4px; border-top: 1px solid var(--color-border);
  color: var(--color-text-secondary); font-size: 12px; line-height: 1.5;
  white-space: pre-wrap; word-break: break-word;
}

/* ── 工具调用卡片 ─────────────────────────────────────────────
   一张卡片：头部是摘要行，展开后是一列可点的行。
   原来是「灰块套灰块」——外层 summary 和每条 tool 都用 --color-bg-active，
   11px 字号下两层边界完全糊成一片，看着又脏又没层次。
   现在边界交给一条细边框，行之间靠 hover 区分，状态用左侧内阴影条表达。 */
.ai-tools {
  margin: 6px 0 2px;
  max-width: 100%;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  overflow: hidden;
}

.ai-tools > summary {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 7px 10px;
  list-style: none;
  cursor: pointer;
  user-select: none;
  outline: none;
  font-size: 11.5px;
  color: var(--color-text-secondary);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-tools > summary::-webkit-details-marker { display: none; }

.ai-tools > summary:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-tools[open] > summary { border-bottom: 1px solid var(--color-border); }

.ai-tools-ico { flex: 0 0 auto; color: var(--color-accent); opacity: 0.85; }

/* 箭头靠右、展开时翻转：这张卡现在是开是合，不必自己看内容判断 */
.ai-tools-chev {
  margin-left: auto;
  flex: 0 0 auto;
  color: var(--color-text-disabled);
  transition: transform var(--transition-fast);
}

.ai-tools[open] > summary .ai-tools-chev { transform: rotate(180deg); }

.ai-tools-body { display: flex; flex-direction: column; gap: 1px; padding: 4px; }

.ai-tool { border-radius: var(--radius-sm); transition: background-color var(--transition-fast); }

.ai-tool:hover { background: var(--color-bg-hover); }

/* 状态条用 inset box-shadow：border-left 会让图标与文字在有无状态之间横向跳动 */
.ai-tool.is-run { box-shadow: inset 2px 0 0 var(--color-accent); }
.ai-tool.is-err { box-shadow: inset 2px 0 0 var(--color-danger); }
.ai-tool.is-ask { box-shadow: inset 2px 0 0 var(--color-warning); }

.ai-tool > summary {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 5px 8px;
  list-style: none;
  cursor: pointer;
  user-select: none;
  outline: none;
}

.ai-tool > summary::-webkit-details-marker { display: none; }

.ai-tool > summary:hover .ai-tool-target { color: var(--color-text-primary); }

.ai-tool-icon { flex: 0 0 auto; color: var(--color-accent); opacity: 0.8; }

.ai-tool.is-err .ai-tool-icon { color: var(--color-danger); opacity: 1; }

.ai-tool-verb { flex: 0 0 auto; font-size: 12px; color: var(--color-text-primary); }

.ai-tool-target {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 320px;
}

.ai-tool-diff { display: flex; gap: 4px; flex: 0 0 auto; font-family: var(--font-mono); font-size: 11px; }

.ai-diff-add { color: var(--color-success, #3fb950); font-style: normal; }

.ai-diff-del { color: var(--color-danger); font-style: normal; }

/* 时长/状态做成小胶囊：margin-left:auto 推到最右，和左边的动词分层 */
.ai-tool-ms {
  flex: 0 0 auto;
  margin-left: auto;
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--color-bg-tertiary);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  color: var(--color-text-muted);
}

.ai-tool-ms.is-ask { background: var(--color-warning-bg); color: var(--color-warning); }

.ai-tool-ms.is-denied { color: var(--color-text-muted); }

/* 展开的内容统一缩进到文本列（图标 12 + gap 7 + padding 8），与摘要行的动词对齐 */
.ai-tool-err,
.ai-tool-res,
.ai-approval { margin: 0 8px 6px 27px; }

.ai-approval { display: flex; flex-direction: column; gap: 6px; }

.ai-approval-risk {
  font-size: 11px;
  line-height: 1.55;
  color: var(--color-text-secondary);
  background: var(--color-bg-primary);
  border-radius: var(--radius-sm);
  padding: 5px 8px;
  border-left: 2px solid var(--color-warning);
}

.ai-approval-hint { font-size: 11px; color: var(--color-warning); }

.ai-approval-done { font-size: 11px; color: var(--color-text-muted); }

/* diff 由子组件渲染，.qd-diff 自带边框，这里只补对齐用的外边距 */
.ai-tool :deep(.qd-diff) { margin: 0 8px 6px 27px; }

.ai-tool-undo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  padding: 2px 4px;
  border: none;
  border-radius: 3px;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ai-tool-undo:hover { color: var(--color-accent); background: var(--color-bg-hover); }

.ai-rules {
  display: flex; align-items: center; gap: 4px;
  margin: 2px 0 4px; font-size: 11px; color: var(--color-text-muted);
}

.ai-rules-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}

.ai-plan {
  align-self: stretch; max-width: 100%;
  margin: 4px 0 6px; padding: 10px 12px;
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  background: var(--color-bg-primary);
}

.ai-plan-head { display: flex; align-items: center; gap: 6px; }

.ai-plan-icon { color: var(--color-accent); flex: none; }

.ai-plan-name { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }

.ai-plan-count { font-size: 11px; color: var(--color-text-disabled); }

.ai-plan-run {
  display: inline-flex; align-items: center; gap: 4px;
  margin-left: auto; padding: 3px 9px; flex-shrink: 0;
  border: 1px solid var(--color-accent-border); border-radius: var(--radius-sm);
  background: var(--color-accent-bg); color: var(--color-accent);
  font-size: 11px; cursor: pointer;
}

.ai-plan-run:hover { border-color: var(--color-accent); }

.ai-plan-sec { margin-top: 8px; }

.ai-plan-sec-title {
  font-size: 11px; font-weight: 600; color: var(--color-text-secondary);
  margin-bottom: 3px;
}

.ai-plan-items { margin: 0; padding: 0; list-style: none; }

.ai-plan-items li {
  display: flex; align-items: flex-start; gap: 6px;
  font-size: 12.5px; line-height: 1.55; color: var(--color-text-secondary);
  padding: 1px 0;
}

.ai-plan-items:not(.is-step) li::before {
  content: '·'; color: var(--color-text-disabled); flex: none;
}

.ai-plan-check { margin: 3px 0 0; flex: none; accent-color: var(--color-accent); }

.ai-plan-item-md { flex: 1; min-width: 0; }

.ai-plan-item-md :deep(p) { margin: 0; }

.ai-plan-text { font-size: 12.5px; color: var(--color-text-secondary); }

.ai-tool-err {
  font-size: 11px; color: var(--color-danger);
  word-break: break-word;
}

.ai-tool-res {
  font-size: 11px; color: var(--color-text-secondary);
  font-family: var(--font-mono); line-height: 1.55;
  max-height: 220px; overflow: auto;
  white-space: pre-wrap; word-break: break-word;
}

.ai-tool-res.is-code {
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  padding: 7px 9px;
}

.ai-job-note {
  margin-top: 4px; padding: 2px 0 2px 6px;
  border-left: 2px solid var(--color-accent);
  font-size: 10px; line-height: 1.5; color: var(--color-text-muted);
}

.ai-messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  overflow-anchor: none; /* 关掉浏览器自动锚定，避免与我们的贴底逻辑打架 */
}

.ai-msg-content-wrap {
  display: flex;
  flex-direction: column;
  gap: 14px;
  flex: 0 0 auto;
  min-height: 100%;
}

.ai-msg { display: flex; flex-direction: column; gap: 4px; max-width: 88%; }

.ai-msg.user { align-self: flex-end; align-items: flex-end; }

.ai-msg.assistant { align-self: flex-start; }

.ai-msg-role { font-size: 11px; color: var(--color-text-disabled); margin-bottom: 1px; display: flex; align-items: center; gap: 6px; }

.ai-msg-model {
  font-size: 10px; color: var(--color-text-disabled);
  background: var(--color-bg-tertiary); padding: 0 5px;
  border-radius: var(--radius-xs); opacity: 0.85;
}

.ai-msg-content {
  padding: 0;
  font-size: 13px;
  line-height: 1.65;
  word-break: break-word;
  color: var(--color-text-primary);
  user-select: text;
  -webkit-user-select: text;
  cursor: text;
  min-width: 0;
}

.ai-msg.user .ai-msg-content {
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  color: var(--color-text-primary);
  padding: 8px 12px;
  border-radius: var(--radius-md);
  white-space: pre-wrap;
}

.ai-msg-actions {
  display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap;
}

.ai-msg.user .ai-msg-actions { justify-content: flex-end; }

.ai-copy {
  display: flex; align-items: center; gap: 3px;
  background: none; border: none; color: var(--color-text-disabled);
  font-size: 11px; cursor: pointer; padding: 2px 4px; border-radius: var(--radius-xs);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-copy:hover { color: var(--color-text-muted); background: var(--color-bg-active); }

.ai-msg.user .ai-copy { color: var(--color-text-muted); }

.ai-msg.user .ai-copy:hover { color: var(--color-accent); background: var(--color-accent-bg); }

.ai-msg-stopped {
  font-size: 10px; padding: 1px 6px; border-radius: var(--radius-xs);
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border: 1px solid var(--color-warning-border);
  cursor: default;
}

.ai-msg-stat {
  font-size: 10px;
  color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
  /* 允许换行：这行可能同时有「耗时 / 首字 / 输入输出 / 缓存命中」四段，
     窄窗口下 nowrap 会把后面的内容顶出可视区（数字被裁比换行更难接受） */
  white-space: normal;
  line-height: 1.6;
  cursor: default;
}

.ai-cursor { animation: ai-blink 1s steps(1) infinite; color: var(--color-accent); }

.ai-error {
  align-self: flex-start;
  padding: 10px 14px; border-radius: var(--radius-lg);
  background: var(--color-danger-bg); color: var(--color-danger);
  font-size: 13px; max-width: 80%;
  /* 错误文案 + 重试按钮同一行；长错误换行，按钮不跟着被挤压 */
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}

.ai-error-text { flex: 1 1 auto; min-width: 0; word-break: break-word; }

.ai-retry {
  flex: 0 0 auto;
  padding: 3px 12px; border-radius: var(--radius-sm);
  border: 1px solid var(--color-danger-border);
  background: transparent; color: var(--color-danger);
  font-size: 12px; font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast);
}

.ai-retry:hover { background: var(--color-danger-bg); }

.ai-need-key, .ai-empty {
  margin: auto;
  display: flex; flex-direction: column; align-items: center; gap: 12px;
  color: var(--color-text-disabled); text-align: center;
}

.ai-need-key p, .ai-empty p { font-size: 13px; margin: 0; }

.ai-jump-bottom {
  position: absolute;
  bottom: 96px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 20;
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px;
  border-radius: var(--radius-full);
  border: 1px solid var(--color-border);
  background: var(--color-surface-elevated);
  color: var(--color-text-secondary);
  cursor: pointer;
  box-shadow: var(--shadow-md);
  transition: background-color var(--transition-fast), color var(--transition-fast),
    border-color var(--transition-fast), opacity var(--transition-fast);
}

.ai-jump-bottom:hover { color: var(--color-accent); border-color: var(--color-accent); }

.ai-md-block { display: contents; }

.ai-md :deep(h1), .ai-md :deep(h2), .ai-md :deep(h3), .ai-md :deep(h4) {
  font-size: 14px; margin: 10px 0 4px; font-weight: 600; line-height: 1.4;
}

.ai-md :deep(h1:first-child), .ai-md :deep(h2:first-child), .ai-md :deep(h3:first-child) { margin-top: 0; }

.ai-md :deep(p) { margin: 5px 0; }

.ai-md :deep(ul), .ai-md :deep(ol) { padding-left: 20px; margin: 5px 0; }

.ai-md :deep(li) { margin: 2px 0; line-height: 1.6; }

.ai-md :deep(li > p) { margin: 0; }

.ai-md :deep(br) { display: inline; }

.ai-md :deep(a) { color: var(--color-accent); text-decoration: none; }

.ai-md :deep(a:hover) { text-decoration: underline; }

.ai-md :deep(blockquote) {
  border-left: 3px solid var(--color-accent-muted);
  padding: 6px 12px;
  margin: 8px 0;
  background: var(--color-bg-secondary);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  color: var(--color-text-secondary);
}

.ai-md :deep(blockquote p) { margin: 0; }

.ai-md :deep(.md-code) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  overflow: hidden;
  background: var(--color-code-bg);
}

.ai-md :deep(.md-code-bar) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px 4px 10px;
  background: var(--color-code-bar-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: 11px;
  color: var(--color-text-muted);
}

.ai-md :deep(.md-code-lang) {
  text-transform: lowercase;
  letter-spacing: 0.02em;
}

.ai-md :deep(.md-code-copy) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-md :deep(.md-code-actions) { display: flex; align-items: center; gap: 2px; }

.ai-md :deep(.md-code-run) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-md :deep(.md-code-run:hover) { background: var(--color-bg-hover); color: var(--color-accent); }

.ai-md :deep(.md-code-run svg) { width: 13px; height: 13px; }

.ai-md :deep(.md-code-copy:hover) { background: var(--color-bg-hover); color: var(--color-accent); }

.ai-md :deep(.md-code-copy.copied) { color: var(--color-success); }

.ai-md :deep(.md-code-copy svg) { width: 13px; height: 13px; }

.ai-md :deep(pre) {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  background: transparent;
  font-size: 12px;
  line-height: 1.6;
}

.ai-md :deep(code) {
  font-family: var(--font-mono, monospace); font-size: 12px;
  background: var(--color-code-inline-bg); padding: 1px 5px; border-radius: var(--radius-xs);
}

.ai-md :deep(pre code) { background: none; padding: 0; font-size: 12px; }

.ai-md :deep(table) { border-collapse: collapse; }

.ai-md :deep(th), .ai-md :deep(td) { border: 1px solid var(--color-border); padding: 5px 9px; }

.ai-md :deep(th) { background: var(--color-bg-secondary); font-weight: 600; }

.ai-md :deep(.md-table-wrap) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  overflow-x: auto;
}

.ai-md :deep(.md-table-bar) {
  display: flex; justify-content: flex-end;
  padding: 3px 6px;
  border: 1px solid var(--color-border);
  border-bottom: none;
  border-radius: var(--radius-md) var(--radius-md) 0 0;
  background: var(--color-code-bar-bg);
}

.ai-md :deep(.md-table-export) {
  display: inline-flex; align-items: center; gap: 3px;
  font-size: 11px; padding: 2px 8px;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-primary); color: var(--color-text-muted); cursor: pointer;
}

.ai-md :deep(.md-table-export:hover) { color: var(--color-accent); border-color: var(--color-accent); }

.ai-md :deep(.md-th-sortable) { cursor: pointer; user-select: none; white-space: nowrap; }

.ai-md :deep(.md-th-sortable:hover) { color: var(--color-accent); }

.ai-md :deep(.md-th-sortable[data-sort-dir="asc"]::after) { content: " ▲"; font-size: 9px; }

.ai-md :deep(.md-th-sortable[data-sort-dir="desc"]::after) { content: " ▼"; font-size: 9px; }

.ai-md :deep(hr) { border: none; border-top: 1px solid var(--color-border); margin: 10px 0; }

.ai-md :deep(img) { max-width: 100%; border-radius: var(--radius-sm); }

.ai-md :deep(.md-math-inline) { display: inline; }

.ai-md :deep(.md-math-inline .katex) { font-size: 1.05em; }

.ai-md :deep(.md-math-block) {
  display: block;
  margin: 8px 0;
  padding: 2px 0;
  overflow-x: auto;
  overflow-y: hidden;
}

.ai-md :deep(.md-math-block .katex-display) { margin: 0; }

.ai-md :deep(.md-math-raw) {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--color-text-secondary);
}

.ai-md :deep(.md-mermaid) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-code-bg);
  /* 圆角要裁得住里面的工具栏；横向滚动交给内容区，见 .md-mermaid-body */
  overflow: hidden;
}

.ai-md :deep(.md-mermaid-bar) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px 4px 10px;
  background: var(--color-code-bar-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: 11px;
  color: var(--color-text-muted);
  /* 工具栏只是外壳，别让它参与划词复制 */
  user-select: none;
}

.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-bar) {
  border-bottom-color: transparent;
}

.ai-md :deep(.md-mermaid-lang) {
  text-transform: lowercase;
  letter-spacing: 0.02em;
}

.ai-md :deep(.md-mermaid-acts) { display: flex; align-items: center; gap: 2px; }

.ai-md :deep(.md-mermaid-act) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-md :deep(.md-mermaid-act:hover) { background: var(--color-bg-hover); color: var(--color-accent); }

.ai-md :deep(.md-mermaid-act svg) { width: 13px; height: 13px; display: block; }

.ai-md :deep(.md-mermaid-act .md-mermaid-i-eye) { display: none; }

.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-act .md-mermaid-i-code) { display: none; }

.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-act .md-mermaid-i-eye) { display: block; }

.ai-md :deep(.md-mermaid-act .md-mermaid-i-down) { display: none; }

.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-act .md-mermaid-i-up) { display: none; }

.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-act .md-mermaid-i-down) { display: block; }

.ai-md :deep(.md-mermaid-body) { overflow-x: auto; }

.ai-md :deep(.md-mermaid-svg) { display: none; }

.ai-md :deep(.md-mermaid[data-mermaid-done="1"] .md-mermaid-svg) {
  display: block;
  padding: 10px;
  background: var(--color-bg-primary);
}

.ai-md :deep(.md-mermaid[data-mermaid-done="1"] .md-mermaid-src) { display: none; }

.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-svg) { display: none; }

.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-src) {
  display: block;
  max-height: 320px;
  overflow: auto;
}

.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-body) { display: none; }

.ai-md :deep(.md-mermaid svg) {
  display: block;
  margin: 0 auto;
  max-width: 100%;
  height: auto;
}

.ai-md :deep(.md-mermaid-err) {
  padding: 4px 10px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-code-bar-bg);
  font-size: 11px;
  color: var(--color-text-muted);
}

.ai-msg.streaming :deep(.ai-md-block:last-child .md-mermaid-bar) { display: none; }

.ai-msg-imgs { display: flex; flex-wrap: wrap; gap: 8px; }

.ai-msg.user .ai-msg-imgs { justify-content: flex-end; }

.ai-msg-img { width: 180px; max-width: 55%; border-radius: var(--radius-sm); overflow: hidden; border: 1px solid var(--color-border); }

.ai-msg-img-el { width: 100%; display: block; max-height: 220px; object-fit: cover; background: var(--color-bg-tertiary); }

.ai-msg-img-el.is-clickable { cursor: zoom-in; }

.ai-img-broken { height: 80px; display: flex; align-items: center; justify-content: center; font-size: 20px; color: var(--color-text-disabled); }

@keyframes ai-blink { 50% { opacity: 0; } }
</style>
