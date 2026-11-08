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
import { computed, nextTick, ref, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAiCostPreference } from '../composables/useAiCostPreference'
import {
  ArrowDown, Bot, ChevronDown, ChevronRight, Copy, FileText, Pencil, Play, RotateCcw, StickyNote, Undo2, Wrench,
} from '@lucide/vue'
import AgentRunTree from './AgentRunTree.vue'
import AiDiffView from './AiDiffView.vue'
import type { LiveBlock } from '../composables/useAiMarkdownBlocks'
import type { AIRun } from '../utils/aiRuns'
import { pickActiveRun } from '../utils/aiRuns'
import type {
  AIArtifact, AICheckpointView, AIJobRow, AIMessage, AIMsgImage, AIRuleFile, AIStreamStat, AIToolCall,
} from '../types/ai'
import {
  artifactsFromCalls, artifactsOf, countToolRounds, jobIdOf, rulesOf, rulesTitle, runIDOf, toolIcon, toolList,
  toolOutput, toolResultLang, toolTarget, toolVerb as aiToolVerb,
} from '../utils/aiTools'

const props = defineProps<{
  hasKey: boolean
  messages: AIMessage[]
  /** 每条回复的用量统计（父组件的 reactive Map，同一份对象，读 .get 仍会追踪） */
  statMap: Map<string, AIStreamStat>
  /** 语言标识：进 v-memo 依赖，切语言要重渲染出新的 title 文案 */
  locale: string
  mode: string
  /**
   * 所有正在跑的流（父组件持有的原数组，读元素属性仍会追踪）。
   *
   * 这一层是「流式读取点下沉」的关键：父组件只把数组本身传下来，模板里不读
   * 任何 run 的内部字段，于是 token 到达时失效的只有本组件这棵子树。
   * 以前把 reasoning / tools / blocks / rules 一条条算好传进来，其中 blocks
   * 每帧都换引用，等于让 4200 行的 AIPage 每帧重跑一遍模板 diff。
   */
  runs: AIRun[]
  /** 当前打开的会话 id：决定这一页该画哪条流（规则见 utils/aiRuns） */
  activeId: string
  /** 落地页那张草稿对应的 run id（会话还没建出来时用） */
  draftRunId: string
  errorMsg: string
  canRetry: () => boolean
  /**
   * 是否给「重新生成」入口：最后一条是助手回复且没有 run 在跑。
   *
   * 与 canRetry 分开两个 prop 而不是共用一个判据：失败时最后一条是**用户**
   * 消息（助手回复没落库），那时要的是「重试」；而重新生成的前提是已经有一条
   * 回答可以被重答。合成一个判据必然让其中一种情况消失。
   */
  canRegenerate: boolean
  renderBlock: (md: string) => string
  renderMessageBlocks: (m: AIMessage) => string[]
  msgStat: (m: AIMessage) => AIStreamStat | null
  formatStat: (s: AIStreamStat) => string
  cpForCall: (tc: AIToolCall) => AICheckpointView | undefined
  /**
   * 后台任务状态：job id → 一行（由父组件轮询 ai_jobs 得到，含已结束的）。
   *
   * 卡片上原来直接把 job id 印出来（`后台任务 job_mvzabgak_27620：…`），那是
   * 「毫秒时间戳 base36 + 进程 pid」拼的主键，对用户零信息量。这里用它换成
   * 「运行中 / 已结束 · 退出码 1」，id 只留在面板与日志文件名里。
   */
  jobRows: Map<string, AIJobRow>
  /** jobRows 的版本号：进 v-memo 依赖，否则任务结束后历史消息里那行不会翻状态 */
  jobSeq: number
}>()

// —— 流式状态从 runs 里自己取 ——
//
// 空值用稳定常量而不是字面量 `[]`：每次渲染都换新引用会让模板里的 .length / v-for
// 白 diff 一遍，也不能进 v-memo 依赖。
const NO_TOOLS: AIToolCall[] = []
const NO_BLOCKS: LiveBlock[] = []
const NO_RULES: AIRuleFile[] = []

/** 这一页该画的那条流（多条任务同时跑时只取属于当前会话的那条） */
const activeRun = computed<AIRun | null>(
  () => pickActiveRun(props.runs, props.activeId, props.draftRunId) ?? null,
)
const streaming = computed(() => !!activeRun.value)
const reasoningText = computed(() => activeRun.value?.reasoning ?? '')
const streamTools = computed<AIToolCall[]>(() => activeRun.value?.tools ?? NO_TOOLS)
const streamBlocks = computed<LiveBlock[]>(() => activeRun.value?.blocks ?? NO_BLOCKS)
const runRules = computed<AIRuleFile[]>(() => activeRun.value?.rules ?? NO_RULES)
// 流式期的产物：file_write 返回的当下就挂出来，等 done 再显示会让
// 「文件已经写完了」和「看得见」之间隔一整轮回答。
const streamArtifacts = computed<AIArtifact[]>(() => artifactsFromCalls(streamTools.value))

// ---- 编辑重发 ----
// 编辑态放在消息气泡里而不是回填输入框：改的是「那条历史提问」，就地改才不会
// 让人以为要重新发一条新消息。截断与重发交给父组件，二次确认也在它那边。
const editingId = ref('')
const editDraft = ref('')
const editInputRef = ref<HTMLTextAreaElement | null>(null)
// v-for 里的字符串 ref 会被收集成数组，用函数 ref 才拿得到那个唯一的 textarea。
function setEditInput(el: unknown) {
  if (el) editInputRef.value = el as HTMLTextAreaElement
}

function beginEdit(m: AIMessage) {
  editingId.value = m.id
  editDraft.value = m.content
  void nextTick(() => { editInputRef.value?.focus() })
}
function cancelEdit() {
  editingId.value = ''
  editDraft.value = ''
}
function saveEdit(m: AIMessage) {
  const text = editDraft.value.trim()
  if (!text) return
  if (text === m.content.trim()) { cancelEdit(); return }
  emit('edit', m, editDraft.value)
  cancelEdit()
}
// Esc 必须自己吃掉：AIPage 的全局 Esc 是「停止生成」，编辑到一半按 Esc
// 不该把别的会话里正在跑的任务停掉。输入法拿 Esc 取消候选时一律不处理。
function onEditKeydown(e: KeyboardEvent, m: AIMessage) {
  if (e.isComposing || e.keyCode === 229) return
  if (e.key === 'Escape') {
    e.preventDefault(); e.stopPropagation(); cancelEdit(); return
  }
  if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey) {
    e.preventDefault(); e.stopPropagation(); saveEdit(m)
  }
}

// 命令在跑的时候输出在卡片里往下长：不跟着滚，用户看到的永远是开头几行，
// 而这恰恰是最没信息量的部分（pip 真正在动的进度在最后一行）。
// 只跟「正在跑」的卡片——已经跑完的卡片用户可能正在往上翻，不该被拽回底部。
const toolResEls = new Map<string, HTMLElement>()

function setToolResEl(id: string, el: unknown) {
  if (el instanceof HTMLElement) toolResEls.set(id, el)
  else toolResEls.delete(id)
}

watch(
  // 依赖只有「正在跑的几条的长度」：内容一变就滚，跑完的卡片不再触发。
  () => streamTools.value.map((t) => (t.running ? (t.live?.length || 0) + (t.liveTail?.length || 0) : -1)).join(','),
  () => {
    nextTick(() => {
      for (const t of streamTools.value) {
        if (!t.running) continue
        const el = toolResEls.get(t.id)
        if (el) el.scrollTop = el.scrollHeight
      }
    })
  },
)

// 有没有正文，只看块数组就够了。
//
// 原来还额外收一个 `streamingText` 字符串，作用是「有内容吗」。那是父组件把一个
// 每帧都在变的全文字符串传进来，只为一个布尔判断 —— 它让 AIPage 的模板多了一条
// 每帧失效的依赖，代价是整页重渲染。块数组本来就要传（渲染靠它），零额外成本。
const hasStreamText = computed(() => streamBlocks.value.length > 0)

const emit = defineEmits<{
  (e: 'open-settings'): void
  (e: 'open-artifact', a: AIArtifact): void
  (e: 'open-img', img: AIMsgImage): void
  (e: 'copy', text: string): void
  (e: 'save-note', text: string): void
  (e: 'restore-checkpoint', cp: AICheckpointView): void
  /** 点「查看」：打开后台任务面板并定位到这条任务 */
  (e: 'open-jobs', id: string): void
  (e: 'run-plan', m: AIMessage): void
  (e: 'retry'): void
  /** 重新生成上一条回答 */
  (e: 'regenerate'): void
  /** 编辑某条历史提问并重发：父组件负责二次确认与截断重发 */
  (e: 'edit', m: AIMessage, content: string): void
  (e: 'msg-click', ev: MouseEvent): void
  (e: 'msg-mouseup', ev: MouseEvent | KeyboardEvent): void
  (e: 'msg-contextmenu', ev: MouseEvent): void
}>()

const { t } = useI18n()

// —— 单条费用（气泡尾部）——
//
// 数据全部来自后端已经算好的那一份，前端不复制价目表、也不重算钱：
//   · 金额 msgStat(m).cost —— 本次会话刚定格时来自 done 事件的 estimateCost
//     （ai_stream.go），刷新后回落到 AIGetMessages 的 AIMessageView.Cost
//     （ai_conversation.go）。两条路用的是同一份 ai_pricing.go 价目表，
//     前端再算一遍只会让「气泡 ¥0.03 / 设置页账单 ¥0.05」这种漂移发生。
//   · token 取同一条 stat 的 prompt + completion。缓存命中（cached）已经含在
//     prompt 里，再加一遍会把「省了钱的调用」当成「花得更多」（见 tokensOf）。
//   · 模型名是 m.model，done 落地与刷新后都在消息行上。
//
// 只遍历已落库的 props.messages：流式那一轮还挂在下面的 streaming 卡片里、没进
// messages，所以流式期间这个 computed 的依赖（messages / statMap / 开关）都不变
// —— 不会每来一个 chunk 重算一次；done 落库那一刻才重算一次，正好是「消息定格」。
// 结果按消息 id 存成 Map，模板里只做一次 .get，不在模板里拼字符串或调格式化。
const { showMsgCost } = useAiCostPreference()

// 与 SettingsAI.vue 的 fmtTokens / fmtCost 逐行一致：设置页总量、每日柱、模型表
// 都用这套口径，气泡上的单条金额必须一眼看得出是同一种钱。
function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 1 : 2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + 'k'
  return String(n)
}

function fmtCost(n: number): string {
  if (!n) return '¥0'
  if (n >= 1) return '¥' + n.toFixed(2)
  if (n >= 0.01) return '¥' + n.toFixed(3)
  return '¥' + n.toFixed(4)
}

// 关掉开关时给一个稳定空 Map：每次渲染换新引用会让 v-memo 依赖白 diff 一遍。
const NO_COST_LINES: ReadonlyMap<string, string> = new Map<string, string>()

const costLines = computed<ReadonlyMap<string, string>>(() => {
  if (!showMsgCost.value) return NO_COST_LINES
  const out = new Map<string, string>()
  for (const m of props.messages) {
    if (m.role !== 'assistant') continue
    const st = props.msgStat(m)
    const cost = st?.cost
    // 未收录模型 / 上游没回 usage：cost 缺失或 0 就整行不显示。
    // 宁可少一个数字，也不要为了「有这一行」编一个 ¥0 —— 与设置页「未知模型
    // 不计价」、统计行「0 即隐藏」是同一个口径。
    if (!cost || cost <= 0) continue
    const tokens = (st?.promptTokens || 0) + (st?.completionTokens || 0)
    const parts: string[] = []
    // 「能拿到多少显示多少」：模型名是数据不是文案，缺了就省这一段。
    if (m.model) parts.push(m.model)
    // 复用已有的 aiStatCost（「费用 {c}」）文案，不另造一个同义 key。
    parts.push(t('aiStatCost', { c: fmtCost(cost) }))
    if (tokens > 0) parts.push(t('aiMsgCostTokens', { n: fmtTokens(tokens) }))
    out.set(m.id, parts.join(' · '))
  }
  return out
})

// 父组件 formatStat（AIPage.vue）拼出的统计行里本来就带一段「费用 ¥x」。
// 单条费用现在统一由气泡尾部的 .ai-msg-cost 展示、并受设置开关控制：这里把
// cost 从传给 formatStat 的副本里抹掉，避免同一笔钱在两行各写一遍，也避免
// 关掉开关后统计行里仍留着「费用」那一截 —— 那样等于没关干净。
// 只浅拷贝，不碰 statMap 里的原对象（会话底部汇总还要读它的 cost / token）。
function statWithoutCost(s: AIStreamStat): AIStreamStat {
  return { ...s, cost: 0 }
}

// 工具 / 产物的解析缓存。
//
// 模板里同一条消息会调 toolList(m) 四次（摘要、轮次、列表、key），artifactsOf 内部
// 又再 parse 一次；而每次都是 JSON.parse(tool_calls)。一屏十几条消息就是几十次解析，
// 滚动/重渲染时白白烧掉主线程。这里按「消息 id + 原始 JSON 长度」做记忆化：
// 内容一变签名就变，不会拿到旧结果。
const toolCache = new Map<string, { sig: string; list: AIToolCall[] }>()
function toolsOf(m: AIMessage): AIToolCall[] {
  const sig = `${m.id}:${(m.tool_calls || '').length}`
  const hit = toolCache.get(m.id)
  if (hit && hit.sig === sig) return hit.list
  const list = toolList(m)
  toolCache.set(m.id, { sig, list })
  return list
}

const artCache = new Map<string, { sig: string; list: AIArtifact[] }>()
function artsOf(m: AIMessage): AIArtifact[] {
  const sig = `${m.id}:${m.role}:${(m.tool_calls || '').length}`
  const hit = artCache.get(m.id)
  if (hit && hit.sig === sig) return hit.list
  const list = artifactsOf(m)
  artCache.set(m.id, { sig, list })
  return list
}


/** i18n 的 t 由本组件提供，工具动词的翻译在这里接上 */
function toolVerb(name: string): string {
  return aiToolVerb(name, t)
}

// —— 工具输出的折叠 ——
//
// 一个 `file_read` 的结果可能是几千行，整段塞进 <pre> 意味着一条消息就排出一个
// 几万字符的文本节点。滚动容器里这种节点不贵在渲染、贵在每次 reflow 都要为它
// 算一遍行盒 —— 三百条消息的会话打开就是整页卡死。
//
// 默认只给前 aiToolResClamp 字，「展开」时再放全文：用户绝大多数时候只需要看
// 开头几行确认「读对了没有」，而真要复制全文时点一下就行，不牺牲能力。
const aiToolResClamp = 3000

const expandedTools = ref<Set<string>>(new Set())

function toolResExpanded(id: string): boolean {
  return expandedTools.value.has(id)
}

/**
 * 这条消息里有几个工具结果被展开过。
 *
 * 进 `.ai-msg` 的 v-memo 依赖用。不能直接放一个全局版本号：那样点开任意一条的
 * 输出，整个会话的 v-memo 全部失效，等于把「长会话滚动」重新做一遍。按消息算，
 * 只有点开的那一条会重渲染。
 */
function expandedInMessage(m: AIMessage): number {
  if (expandedTools.value.size === 0) return 0
  let n = 0
  for (const tc of toolsOf(m)) {
    if (expandedTools.value.has(tc.id)) n++
  }
  return n
}

function toolResText(tc: AIToolCall): string {
  const full = toolOutput(tc)
  if (full.length <= aiToolResClamp || toolResExpanded(tc.id)) return full
  return full.slice(0, aiToolResClamp)
}

function toolResClamped(tc: AIToolCall): boolean {
  return toolOutput(tc).length > aiToolResClamp && !toolResExpanded(tc.id)
}

function expandToolRes(id: string) {
  const next = new Set(expandedTools.value)
  next.add(id)
  expandedTools.value = next
}

// —— 长会话分批渲染 ——
//
// content-visibility 省的是「离屏消息的排版」，创建 vnode、挂 DOM 仍然全量发生：
// 一个两千条消息的会话打开时要建几万个节点，那才是「点开会话先卡两秒」的主因。
//
// 默认只挂最近 aiMsgRenderTail 条（够覆盖正常阅读与「继续追问」的上下文），
// 往上翻时按同样批量往前放。不做真虚拟滚动：消息高度不固定（代码块、图片、
// 折叠卡片），虚拟化要自己维护高度缓存与滚动锚定，出错时表现为「滚动位置乱跳」，
// 比慢一点糟得多。分批渲染没有动态高度问题，代价只是「翻到很早的历史要多点一次」。
const aiMsgRenderTail = 200

const renderLimit = ref(aiMsgRenderTail)
const olderHidden = computed(() => Math.max(0, props.messages.length - renderLimit.value))
const shownMessages = computed(() =>
  olderHidden.value > 0 ? props.messages.slice(olderHidden.value) : props.messages,
)

async function showOlder() {
  // 往前插入内容会让当前阅读位置往下滑，用户点「更早」正是为了看上面的东西，
  // 滑走等于白点。按高度差补偿 scrollTop，视线落点保持不动。
  const el = msgArea.value
  const beforeH = el ? el.scrollHeight : 0
  const beforeTop = el ? el.scrollTop : 0
  renderLimit.value = Math.min(props.messages.length, renderLimit.value + aiMsgRenderTail)
  await nextTick()
  if (el) el.scrollTop = beforeTop + (el.scrollHeight - beforeH)
}

// 切会话（或清空）时回到默认批量：否则上一条会话把上限顶到几千，
// 切到新会话又变成全量渲染，等于没做。
watch(
  () => props.messages[0]?.id,
  () => { renderLimit.value = aiMsgRenderTail },
)

// —— 后台任务行 ——
// 只在「这条调用带 jobId」时出现，用来替代早先那句把主键印进正文的提示。
//
// strict=false 用于流式区：任务刚起、父组件的轮询还没拿到这一行（最多 3s），
// 此时仍应显示「后台运行中」——它一定在跑。历史消息则必须严格查表，
// 查不到（更早的任务已不在最近 50 条里）就整行不显示，而不是瞎猜成运行中。
function jobLine(tc: AIToolCall, strict = true): string {
  const id = jobIdOf(tc)
  if (!id) return ''
  const row = props.jobRows.get(id)
  if (!row) return strict ? '' : t('aiJobBgRunning')
  if (row.running) return t('aiJobBgRunning')
  if (row.status === 'killed') return t('aiJobBgStopped')
  return t('aiJobBgEnded', { code: row.exitCode })
}

function jobRunning(tc: AIToolCall): boolean {
  const id = jobIdOf(tc)
  return !!id && !!props.jobRows.get(id)?.running
}

// v-memo 的依赖项：只有「这条回复里带后台任务」时才跟着 jobSeq 重渲染，
// 其余历史消息继续走缓存——否则每 3s 一次轮询会把整屏消息全部重建。
function jobSeqOf(m: AIMessage): number {
  return toolsOf(m).some((tc) => jobIdOf(tc)) ? props.jobSeq : 0
}

// —— 工具卡片的展开状态 ——
//
// 早先直接 `:open="!!tc.running"`：跑完 running 变 false，卡片当场折回去。
// 而命令类工具最该看的是**末尾几行**（构建失败的栈、测试的汇总），等它跑完
// 才折起来等于恰好在最有用的那一刻把内容收走。
//
// 所以展开变成「粘性」状态：一旦开过就一直开着，只有用户自己点了摘要行才收。
// 两个集合分工：
//   - autoOpen 由「开始跑 / 等待审批」驱动，程序性打开
//   - userClosed 记住用户手动收起的，之后不再自动打开
// 判定必须与模板里的 :open 表达式完全一致（见 toolShouldOpen），否则程序性
// 改 open 触发的 toggle 会被误当成用户操作，两个集合互相推着打架。
const autoOpenTools = ref(new Set<string>())
const userClosedTools = ref(new Set<string>())

function toolShouldOpen(tc: AIToolCall): boolean {
  if (userClosedTools.value.has(tc.id)) return false
  // 等待审批时必须开着：按钮虽然在下面的审批条里，卡片上写着「为什么停下来」。
  if (tc.approval === 'pending') return true
  return autoOpenTools.value.has(tc.id)
}

watch(
  () => streamTools.value.map((t) => `${t.id}:${t.running ? 1 : 0}${t.approval === 'pending' ? 'a' : ''}`).join(','),
  () => {
    // 一轮结束（streamTools 被清空）时把两个集合一起清掉：里面的 id 是这条流的
    // 调用 id，下一轮不会复用，留着只会让 Set 随提问次数一直长。
    if (streamTools.value.length === 0) {
      if (autoOpenTools.value.size) autoOpenTools.value.clear()
      if (userClosedTools.value.size) userClosedTools.value.clear()
      return
    }
    for (const tc of streamTools.value) {
      if (userClosedTools.value.has(tc.id)) continue
      if (tc.running || tc.approval === 'pending') autoOpenTools.value.add(tc.id)
    }
  },
  { immediate: true },
)

function onToolToggle(tc: AIToolCall, e: Event) {
  const el = e.target as HTMLDetailsElement
  // 与期望状态一致 = 我们自己改的，不是用户点的
  if (el.open === toolShouldOpen(tc)) return
  if (el.open) {
    autoOpenTools.value.add(tc.id)
    userClosedTools.value.delete(tc.id)
  } else {
    autoOpenTools.value.delete(tc.id)
    userClosedTools.value.add(tc.id)
  }
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

/**
 * 确保某条消息已经挂上 DOM 后再跳过去（搜索命中跳转用）。
 *
 * 默认只挂最近 200 条（见 aiMsgRenderTail），命中更早的消息时 jumpToMsg 找不到
 * 节点、静默返回 false——用户点了搜索命中却停在底部，只会以为这个功能坏了。
 * 这里先把上限抬到「包含目标那条」，等一帧再跳。
 *
 * 抬高后不回退：这是用户主动要求往前翻，一压缩就又把它藏回去。
 */
async function ensureRendered(id: string): Promise<boolean> {
  const idx = props.messages.findIndex((m) => m.id === id)
  if (idx < 0) return false
  const need = props.messages.length - idx
  if (need > renderLimit.value) {
    renderLimit.value = Math.min(
      props.messages.length,
      Math.ceil(need / aiMsgRenderTail) * aiMsgRenderTail,
    )
    await nextTick()
  }
  const ok = jumpToMsg(id)
  if (ok) flashMsg(id)
  return ok
}

/**
 * 目标消息闪一下底。
 *
 * 直接改 DOM class 而不是走响应式：消息列表在 v-memo 里，加一个「高亮某条」的
 * 响应式字段会让那次重渲染扩散到整份缓存。class 只是临时的视觉提示，1700ms
 * 后自己消失，没有状态需要回读。
 */
function flashMsg(id: string) {
  const el = msgArea.value?.querySelector<HTMLElement>(`[data-msg-id="${id}"]`)
  if (!el) return
  el.classList.add('is-hit-flash')
  window.setTimeout(() => el.classList.remove('is-hit-flash'), 1700)
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

defineExpose({ scrollToBottom, followBottom, jumpToMsg, ensureRendered, areaEl, contentEl })
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
        <!-- 更早的历史按需放出来（默认只挂最近 200 条，理由见 aiMsgRenderTail） -->
        <button v-if="olderHidden > 0" class="ai-load-older" @click="showOlder">
          {{ t('aiLoadOlder', { n: Math.min(olderHidden, aiMsgRenderTail) }) }}
        </button>
        <!-- v-memo：历史消息在流式期间内容不变，命中缓存即可整棵子树跳过重建。
             依赖数组必须覆盖子树内所有响应式来源，缺一个就会出现「数据变了但界面不变」：
               · m.content / m.reasoning_content —— 正文与思考过程
               · statMap.get(m.id)               —— 该条回复的耗时/token 统计
               · costLines.get(m.id)             —— 气泡尾部的单条费用文案。开关切换、
                                                   done 落库填入 cost 时这一项才变，
                                                   让缓存里的旧气泡重画；流式期间它恒为
                                                   同一串，历史消息继续命中缓存
               · locale                         —— 按钮 title 等文案随语言切换刷新
               · mode                           —— 「开始执行」按钮只看当前模式
               · jobSeqOf(m)                    —— 后台任务行的「运行中 / 已结束」；
                                                   只在含任务的回复上取值，其余恒为 0
                                                   继续命中缓存（否则每 3s 轮询会把整屏
                                                   消息全部重建）
               · 是否最后一条                    —— 按钮只给最后一条回答；新消息到达时
                                                   上一条要在同一份缓存里把按钮收掉
             编译器生成的代码含 `_cached.key === m.id` 校验，
             切换会话（消息 id 变化）不会误命中旧缓存。 -->
        <div
          v-for="m in shownMessages"
          :key="m.id"
          :data-msg-id="m.id"
          v-memo="[m.content, m.reasoning_content, statMap.get(m.id), costLines.get(m.id), locale, (m.imgList?.length ?? 0), m.tool_calls, m.tool_rounds, mode, jobSeqOf(m), m.id === messages[messages.length - 1]?.id, expandedInMessage(m), editingId === m.id]"
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
          <details v-if="m.role === 'assistant' && toolsOf(m).length" class="ai-tools">
            <summary class="ai-tools-sum">
              <Wrench :size="12" class="ai-tools-ico" />
              <span>{{ t('aiToolSummary', { n: toolsOf(m).length, rounds: m.tool_rounds || countToolRounds(toolsOf(m)) }) }}</span>
              <ChevronDown :size="13" class="ai-tools-chev" />
            </summary>
            <div class="ai-tools-body">
              <!-- 每条调用自带折叠：一次回答调了五六个工具时，用户只想展开
                   其中一条看细节，而不是要么全看要么全不看。 -->
              <details v-for="tc in toolsOf(m)" :key="tc.id" class="ai-tool" :class="{ 'is-err': !!tc.error }">
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
                  <ChevronRight :size="12" class="ai-tool-chev" />
                </summary>
                <!-- 行级 diff：结果串只说「改成功了」，这里说「改成了什么」。 -->
                <AiDiffView v-if="tc.diff" :diff="tc.diff" />
                <div v-if="tc.error" class="ai-tool-err">{{ tc.error }}</div>
                <!-- 有 diff 时不再重放一句「已编辑 xxx（替换 1 处）」：
                     路径在摘要行上已经有了，改动在 diff 里已经有了。 -->
                <pre v-else-if="toolOutput(tc) && !tc.diff" class="ai-tool-res" :class="{ 'is-code': !!toolResultLang(tc) }">{{ toolResText(tc) }}</pre>
                <button
                  v-if="toolResClamped(tc)"
                  class="ai-tool-more"
                  @click.stop.prevent="expandToolRes(tc.id)"
                >{{ t('aiToolShowAll', { n: toolOutput(tc).length }) }}</button>
                <!-- 后台任务行：与流式区同款，历史消息里也要在——不然刷新一次就只剩
                     「跑过了」的代码块，看不出那个进程现在还在不在跑。 -->
                <div v-if="jobLine(tc)" class="ai-job-note" :class="{ 'is-run': jobRunning(tc) }">
                  <span class="ai-job-note-dot"></span>
                  <span class="ai-job-note-text">{{ jobLine(tc) }}</span>
                  <button class="ai-job-note-open" @click.stop.prevent="emit('open-jobs', jobIdOf(tc))">{{ t('aiJobBgView') }}</button>
                </div>
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
          <div v-if="m.role === 'assistant'" class="ai-msg-content ai-md">
            <div v-for="(b, bi) in renderMessageBlocks(m)" :key="bi" class="ai-md-block" v-html="b"></div>
          </div>
          <div v-else-if="editingId === m.id" class="ai-msg-edit">
            <textarea
              :ref="setEditInput"
              v-model="editDraft"
              class="ai-msg-edit-input"
              rows="3"
              @keydown="onEditKeydown($event, m)"
            ></textarea>
            <div class="ai-msg-edit-ops">
              <button class="qd-btn qd-btn-primary qd-btn-sm" :disabled="!editDraft.trim()" @click="saveEdit(m)">
                {{ t('aiEditResend') }}
              </button>
              <button class="qd-btn qd-btn-sm" @click="cancelEdit">{{ t('cancel') }}</button>
              <span class="ai-msg-edit-hint">{{ t('aiEditResendHint') }}</span>
            </div>
          </div>
          <div v-else class="ai-msg-content ai-plain">{{ m.content }}</div>
          <!-- 回答产物：靠右贴边的可折叠列表（同常见 AI IDE 的产物区样式）。
               数据就在 tool_calls 里，不用后端另发事件；点击预览（读磁盘现状）。 -->
          <details v-if="m.role === 'assistant' && artsOf(m).length" class="ai-artifacts" open>
            <summary class="ai-artifacts-sum">
              {{ t('aiArtifactsCount', { n: artsOf(m).length }) }}
              <ChevronDown :size="12" class="ai-artifacts-chev" />
            </summary>
            <div class="ai-artifacts-list">
              <button v-for="a in artsOf(m)" :key="a.path" class="ai-artifact" :title="a.path" @click="emit('open-artifact', a)">
                <FileText :size="13" />
                <span class="ai-artifact-name">{{ a.name }}</span>
                <span class="ai-artifact-op">{{ t(a.op === 'edit' ? 'aiArtifactEdited' : 'aiArtifactWritten') }}</span>
              </button>
            </div>
          </details>
          <!-- 操作按钮同一行：.ai-msg 是纵向 flex，不包一层的话每个按钮都会各占一行 -->
          <div class="ai-msg-actions">
            <!-- 中断标记：内容照样保留（后端也把半截内容连同 stopped 一起落库），
                 但要让人一眼看出「这是我按了停止」，而不是模型只答了这么多。
                 断流（不是用户停的）用另一句提示，否则用户会以为自己误触过。 -->
            <span
              v-if="m.stopped"
              class="ai-msg-stopped"
              :title="m.interrupted ? t('aiInterruptedHint') : t('aiStoppedHint')"
            >{{ t('aiStopped') }}</span>
            <!-- 计划模式下的执行入口。原来挂在「计划」卡片头上，卡片去掉后
                 没有它，计划模式就只剩只读，永远迈不出执行那一步。
                 只给最后一条：历史回答早就执行过了，每条都挂一个只会误导。 -->
            <button
              v-if="mode === 'plan' && !streaming && m.role === 'assistant' && m.content && m.id === messages[messages.length - 1]?.id"
              class="ai-copy ai-run-plan"
              :title="t('aiPlanRunHint')"
              @click="emit('run-plan', m)"
            >
              <Play :size="12" /> {{ t('aiPlanRun') }}
            </button>
            <button v-if="m.content" class="ai-copy" @click="emit('copy', m.content)" :title="t('copy')">
              <Copy :size="12" /> {{ t('copy') }}
            </button>
            <!-- 编辑历史提问：只在没有流在跑时给，编辑期间后端会截断这条之后的所有消息 -->
            <button
              v-if="!streaming && m.role === 'user' && m.content && editingId !== m.id"
              class="ai-copy"
              :title="t('aiEditResendHint')"
              @click="beginEdit(m)"
            >
              <Pencil :size="12" /> {{ t('aiEdit') }}
            </button>
            <button v-if="m.role === 'assistant' && m.content" class="ai-copy" @click="emit('save-note', m.content)" :title="t('aiSaveToNote')">
              <StickyNote :size="12" /> {{ t('aiSaveToNote') }}
            </button>
            <!-- 单条回复统计：耗时 / 首字延迟 / token 用量 -->
            <span v-if="msgStat(m)" class="ai-msg-stat">
              {{ formatStat(statWithoutCost(msgStat(m)!)) }}
            </span>
          </div>
          <!-- 气泡尾部：这一轮的费用（模型名 + 金额 + token）。文案由 costLines
               预生成，这里只查一次表；关掉设置里的开关后整行不渲染。 -->
          <div v-if="costLines.get(m.id)" class="ai-msg-cost">{{ costLines.get(m.id) }}</div>
        </div>

        <!-- 任务清单卡片由父组件通过 #plan 提供。数据（planCards，按会话存）归
             AIPage，但它必须渲染在这个滚动容器里才「跟着聊天记录」——挂在输入区
             上方就是一块永远挡着、还挤掉消息区高度的固定面板。
             位置放在历史消息之后、流式气泡之前：语义上就是「AI 先列了清单，
             然后开始动手」，往上翻历史时它会跟着一起滚走。 -->
        <slot name="plan" />

        <div v-if="reasoningText" class="ai-reasoning">
          <details open>
            <summary>{{ t('aiThinking') }}</summary>
            <div class="ai-reasoning-content">{{ reasoningText }}</div>
          </details>
        </div>
        <!-- 流式区：模型先调工具时还没有正文，这里的 v-if 必须带上 streamTools，
             否则「正在查环境」的那几秒界面上什么都没有，看起来像卡死。 -->
        <div v-if="hasStreamText || streamTools.length" class="ai-msg assistant streaming">
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
              <!-- 正在跑的那条自动展开：等待期间它是唯一的进度信息。
                   跑完之后**不自动收起**（理由见 toolShouldOpen），用户点摘要行才收。 -->
              <details v-for="tc in streamTools" :key="tc.id" class="ai-tool" :class="{ 'is-err': !!tc.error, 'is-run': tc.running, 'is-ask': tc.approval === 'pending' }" :open="toolShouldOpen(tc)" @toggle="onToolToggle(tc, $event)">
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
                  <ChevronRight :size="12" class="ai-tool-chev" />
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
                <pre v-else-if="toolOutput(tc) && !tc.diff" :ref="(el) => setToolResEl(tc.id, el)" class="ai-tool-res" :class="{ 'is-code': !!toolResultLang(tc) }">{{ toolOutput(tc) }}</pre>
                <!-- 后台任务：卡片上明写「关闭应用会终止」。工具描述里也写了同一句，
                     但用户看的是这张卡；不在这里再说一次，关窗口时他一定以为
                     dev server 还在后台跑着。job id 是主键（时间戳+pid），只放面板里。 -->
                <div v-if="jobLine(tc, false)" class="ai-job-note" :class="{ 'is-run': jobRunning(tc) }">
                  <span class="ai-job-note-dot"></span>
                  <span class="ai-job-note-text">{{ jobLine(tc, false) }}</span>
                  <button class="ai-job-note-open" @click.stop.prevent="emit('open-jobs', jobIdOf(tc))">{{ t('aiJobBgView') }}</button>
                </div>
              </details>
            </div>
          </details>
          <div v-if="hasStreamText" class="ai-msg-content ai-md">
            <!-- key 只用序号：内容变了由 v-html 的字符串比较兜住（Vue 会就地更新）。
                 早先把「长度 + 前缀」拼进 key，末尾那块每来一个字就换一次 key，
                 于是每帧销毁重建整块 DOM——用户刚划中的选区被抹掉，解析结果也白丢。 -->
            <div v-for="(b, bi) in streamBlocks" :key="bi" class="ai-md-block" v-html="b.html"></div>
          </div>
          <span v-if="hasStreamText" class="ai-cursor">▋</span>
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

        <!-- 重新生成上一条回答。放在消息流末尾而不是每条回复的按钮组里：
             只有最后一条重答才有意义（前面的重答会与后面已有的回答矛盾），
             而每条都挂一个按钮等于给长会话再添 200 个可点区域。
             后端按 targetId 截断旧回答并重算会话 token（ai_usage 明细保留：
             那是一次真实花过钱的调用），所以刷新后不会冒出重复的提问气泡。 -->
        <div v-if="canRegenerate && !streaming" class="ai-regen">
          <button class="ai-regen-btn" :title="t('aiRegenerateHint')" @click="emit('regenerate')">
            <RotateCcw :size="12" /> {{ t('aiRegenerate') }}
          </button>
        </div>

        <div v-if="errorMsg" class="ai-error">
          <span class="ai-error-text">{{ errorMsg }}</span>
          <!-- 失败时用户消息已落库，重试就是把它再发一次（图片一起带回去） -->
          <button v-if="canRetry()" class="ai-retry" @click="emit('retry')">{{ t('aiRetry') }}</button>
        </div>

        <div v-if="messages.length === 0 && !hasStreamText && !errorMsg" class="ai-empty">
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

/* ── 思考过程 ─────────────────────────────────────────────
   无框：一行灰字，展开后是浅竖线引出的灰正文。
   原来 details 带 --color-bg-active 灰底，和下面的工具卡、正文叠在一起
   会形成一块块深浅不一的方块——同一段回答里三种「容器」叠着，风格就散了。 */
.ai-reasoning {
  font-size: 12px; margin: 2px 0; max-width: 100%;
}

.ai-reasoning details { background: none; border-radius: 0; padding: 0; }

.ai-reasoning summary {
  width: fit-content; max-width: 100%;
  /* 负 margin 抵消内边距：hover 底色向外扩，文字仍与正文同一条左边缘 */
  padding: 3px 6px; margin-left: -6px;
  border-radius: var(--radius-sm);
  cursor: pointer; color: var(--color-text-muted); font-size: 12px;
  /* 同 .ai-tools > summary：fit-content 下不 nowrap 会被挤到第二行 */
  white-space: nowrap;
  user-select: none; outline: none;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-reasoning summary:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.ai-reasoning-content {
  margin-top: 2px; padding: 2px 0 2px 10px; border-left: 2px solid var(--color-border);
  color: var(--color-text-muted); font-size: 13px; line-height: 1.7;
  white-space: pre-wrap; word-break: break-word;
}

/* ── 工具调用 ───────────────────────────────────────────────
   无框的一列行：图标 + 动词 + 目标，展开才看结果。
   这里原来是一张带 border / 圆角 / 二级底色的卡片，和上面的思考块、
   下面的正文各是一种容器——三种方块叠在一起，正文反而是最没存在感的那个。
   现在统一成「正文之外的辅助信息一律无框」：只靠缩进、字号、颜色分层。
   每行是「图标 + 动词 + 目标 + 增删 + 箭头」一句连贯的话：动词与正文同亮度，
   目标（文件名 / 命令）用主色跳出来，状态由图标着色表达。 */
.ai-tools {
  margin: 4px 0 2px;
  max-width: 100%;
}

.ai-tools > summary {
  display: flex;
  align-items: center;
  gap: 7px;
  width: fit-content;
  max-width: 100%;
  /* 负 margin 抵消内边距：hover 底色向外扩，文字仍与正文同一条左边缘 */
  padding: 3px 6px;
  margin-left: -6px;
  border-radius: var(--radius-sm);
  list-style: none;
  cursor: pointer;
  user-select: none;
  outline: none;
  font-size: 12px;
  color: var(--color-text-muted);
  /* 必须 nowrap：summary 是 fit-content，默认的 normal 会让「调用 N 个工具 ·
     M 轮」按最长不可断片段算 min-content，容器一紧「轮」就被挤到第二行；
     nowrap 之后 fit-content 的 max/min-content 都按整行算，宽度够放下整句。 */
  white-space: nowrap;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-tools > summary::-webkit-details-marker { display: none; }

.ai-tools > summary:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-tools-ico { flex: 0 0 auto; color: var(--color-text-muted); }

/* 箭头靠右、展开时翻转：这张卡现在是开是合，不必自己看内容判断 */
.ai-tools-chev {
  margin-left: auto;
  flex: 0 0 auto;
  color: var(--color-text-disabled);
  transition: transform var(--transition-fast);
}

.ai-tools[open] > summary .ai-tools-chev { transform: rotate(180deg); }

.ai-tools-body { display: flex; flex-direction: column; gap: 0; padding: 1px 0 0; }

.ai-tool { transition: background-color var(--transition-fast); }

/* hover 高亮放在 summary 上而不是 details 上：summary 用负 margin 伸到
   details 左边 6px（为了文字与正文对齐），高亮跟着 summary 走才不缺一角；
   放在 details 上则展开结果时整块都会被点亮。 */
.ai-tool > summary:hover { background: var(--color-bg-hover); }

.ai-tool > summary {
  display: flex;
  align-items: center;
  gap: 6px;
  /* 宽度跟着内容走。铺满整行时，hover 底色会从动词一直拉到右边，中间一大段
     空白，眼睛要横穿过去才发现行尾有个箭头；fit-content 之后这行读起来就是
     一句连续的「读了哪个文件、改了几行」。max-width 卡住 100% 是防止长路径
     把消息区顶出横向滚动条（负 margin 已让盒子左移 6px）。 */
  width: fit-content;
  max-width: 100%;
  /* 左内边距 6px 由负 margin 抵消：hover 底色向外扩，文字仍与正文同一条左边缘 */
  padding: 4px 6px;
  margin-left: -6px;
  border-radius: var(--radius-sm);
  list-style: none;
  cursor: pointer;
  user-select: none;
  outline: none;
}

.ai-tool > summary::-webkit-details-marker { display: none; }

.ai-tool > summary:hover .ai-tool-target { color: var(--color-accent-light); }

/* 状态全部由图标着色表达：运行中=主色、出错=红、等审批=橙。
   原来在行首画一条 2px 竖条，但 summary 有圆角、又伸出正文 6px，画出来是一段
   弯的短线，看着像个没闭合的括号，还和图标断开——一行辅助信息不该有第二个焦点。 */
.ai-tool-icon { flex: 0 0 auto; color: var(--color-text-muted); }

.ai-tool.is-err .ai-tool-icon { color: var(--color-danger); opacity: 1; }

.ai-tool.is-run .ai-tool-icon { color: var(--color-accent); opacity: 1; }

.ai-tool.is-ask .ai-tool-icon { color: var(--color-warning); opacity: 1; }

.ai-tool-verb { flex: 0 0 auto; font-size: 12px; color: var(--color-text-primary); }

/* 这行里唯一需要「一眼确认是谁」的就是目标（文件名 / 命令），用主色把它
   从一串灰字里挑出来；等宽体去掉了——路径不是要对齐的代码，界面字体读得更快，
   而下面展开的命令输出仍然是等宽。 */
.ai-tool-target {
  flex: 0 1 auto;
  min-width: 0;
  max-width: 320px;
  font-size: 12px;
  font-weight: 500;
  color: var(--color-accent);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-tool-diff { display: flex; gap: 4px; flex: 0 0 auto; font-family: var(--font-mono); font-size: 11px; }

.ai-diff-add { color: var(--color-success, #3fb950); font-style: normal; }

.ai-diff-del { color: var(--color-danger); font-style: normal; }

/* 时长/状态跟在增删数后面，用最弱的一档颜色——它只是参考信息，
   用户真正关心的是「动的是哪个文件、改了几行」。不再用胶囊底色：
   一列行里只有它带底，看起来像漏了一个没去掉的卡片。 */
.ai-tool-ms {
  flex: 0 0 auto;
  padding: 0;
  background: none;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--color-text-disabled);
}

.ai-tool-ms.is-ask { background: none; color: var(--color-warning); }

.ai-tool-ms.is-denied { color: var(--color-text-muted); }

/* 行尾箭头：说明「这行点得开」。此前只有整组一个箭头，单条调用能不能展开
   全靠猜——那行文字看着就是个标签，不像按钮。 */
.ai-tool-chev {
  flex: 0 0 auto;
  margin-left: 1px;
  /* 比 muted 亮一档再压 0.75 透明度：muted/disabled 在深色下几乎看不见，
     而这枚箭头是「这行点得开」的唯一提示，不能靠运气发现。 */
  color: var(--color-text-secondary);
  opacity: 0.75;
  transition: transform var(--transition-fast), color var(--transition-fast);
}

.ai-tool > summary:hover .ai-tool-chev { opacity: 1; }

.ai-tool[open] > summary .ai-tool-chev { transform: rotate(90deg); }

/* 展开的内容缩进到文本列：summary 的 -6px 外边距 + 6px 内边距 + 图标 12 + gap 6，
   文字起点落在 18px 处，展开内容跟动词对齐。改 gap / 图标尺寸时要同步改这里。 */
.ai-tool-err,
.ai-tool-res,
.ai-approval { margin: 0 6px 6px 18px; }

.ai-approval { display: flex; flex-direction: column; gap: 6px; }

.ai-approval-risk {
  font-size: 11px;
  line-height: 1.55;
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  padding: 5px 8px;
  border-left: 2px solid var(--color-warning);
}

.ai-approval-hint { font-size: 11px; color: var(--color-warning); }

.ai-approval-done { font-size: 11px; color: var(--color-text-muted); }

/* diff 由子组件渲染，.qd-diff 自带边框，这里只补对齐用的外边距 */
.ai-tool :deep(.qd-diff) { margin: 0 6px 6px 18px; }

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


.ai-tool-err {
  font-size: 11px; color: var(--color-danger);
  word-break: break-word;
}

.ai-tool-res {
  font-size: 11px; color: var(--color-text-secondary);
  font-family: var(--font-mono); line-height: 1.55;
  /* 340px ≈ 二十来行。原来 220px 只放得下十三四行，读文件的卡片看起来像
     「AI 只读了十几行」，而它其实一次读的是几百行——内容被可视区藏起来了。 */
  max-height: 340px; overflow: auto;
  white-space: pre-wrap; word-break: break-word;
}

/* 「展开全文」：贴在被截断的结果下方，视觉上是那条结果的附注而不是新按钮。
   截断发生在渲染层（见 toolResText），不是 CSS —— 折叠时 DOM 里真的没有
   后面那几万字，长会话才快得起来。 */
.ai-tool-more {
  align-self: flex-start;
  margin: 0 6px 6px 18px;
  padding: 2px 6px;
  font-size: 10px;
  color: var(--color-accent);
  background: none;
  border: none;
  cursor: pointer;
  border-radius: var(--radius-sm);
}
.ai-tool-more:hover { background: var(--color-bg-tertiary); }

/* 命令/代码类结果给一层淡底就够，不加描边：消息区不再有第二种容器 */
.ai-tool-res.is-code {
  background: var(--color-bg-tertiary);
  border: none;
  border-radius: var(--radius-sm);
  padding: 7px 9px;
}

/* 后台任务行：一行放得下「圆点 + 状态 + 查看」，不换行也不撑高卡片。
   底色/描边一概不要——它和上面的日志块同属「这一步的附注」，
   画成小卡片会把无框正文重新切碎。 */
.ai-job-note {
  display: flex; align-items: center; gap: 8px;
  margin-top: 4px; padding: 2px 0;
  font-size: 10px; line-height: 1.5; color: var(--color-text-muted);
}

.ai-job-note-dot {
  flex: 0 0 auto; width: 5px; height: 5px; border-radius: 50%;
  background: var(--color-text-muted);
}

.ai-job-note.is-run .ai-job-note-dot {
  background: var(--color-accent);
  animation: ai-job-note-pulse 1.8s ease-in-out infinite;
}

@keyframes ai-job-note-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: .3; }
}

.ai-job-note-text {
  flex: 0 1 auto; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.ai-job-note.is-run .ai-job-note-text { color: var(--color-text-secondary); }

.ai-job-note-open {
  flex: 0 0 auto; padding: 0 2px;
  border: none; background: none;
  font-family: inherit; font-size: 10px; color: var(--color-text-muted);
  cursor: pointer;
}

/* 悬停才给下划线：平时它是「附注里的一句」，别做得像按钮抢走注意力。 */
.ai-job-note-open:hover {
  color: var(--color-accent);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.ai-messages {
  flex: 1;
  overflow-y: auto;
  /* 与输入框同一条左右边线。输入框的宽度是 --ai-column-w（min(90vw,1200px,100%)），
     居中在 .ai-dock 的 16px 内边距里，所以它的左边缘恒等于
         max(16px, (主区宽 - --ai-column-max) / 2)
     这里用同一个公式算左右内边距，正文边线就跟输入框的边线重合。
     原来写的是 max(20px, 10%)——那是配 width:80% 的输入框的，输入框换了口径之后
     消息区比它窄一截，看起来就是「正文和输入框左右错开」。
     百分号在 padding 上相对包含块（.ai-main）的宽度，与上面的推导一致。 */
  --ai-msg-pad: max(var(--space-6), calc((100% - var(--ai-column-max)) / 2));
  padding: var(--space-6) var(--ai-msg-pad);
  /* 右边刻意不补滚动条那 -6px：overflow-y 的滚动条占的是 content box 内的位置，
     理论上减掉能对齐，但 scrollbar-gutter: stable 在自定义 ::-webkit-scrollbar 下
     并不预留槽位（实测 offsetWidth == clientWidth），补了反而在「内容不足一屏」时
     向右溢出 6px；macOS 的 overlay 滚动条也根本不占宽。两种偏差里选「始终窄 6px」，
     左边线（阅读起点）永远精确对齐，右边差半个字宽，看不出。
     另外不要改成 overflow-y: scroll 去强制占位——那会把滚动条轨道常驻，跨平台不一致。 */
  display: flex;
  flex-direction: column;
  overflow-anchor: none; /* 关掉浏览器自动锚定，避免与我们的贴底逻辑打架 */
  /* 整个消息区允许划词。body 上有全局 user-select:none，此前只给
     .ai-msg-content 开了 text——从气泡留白处起手拖选就一个字符都选不中，
     跨消息复制（正文 + 代码块 + 工具结果）更是无从谈起，用户感受就是
     「聊天区不能复制」。这里把整区打开，纯装饰/可点击部件各自关掉。 */
  user-select: text;
  -webkit-user-select: text;
}

/* 只关掉「按钮 / 折叠标题」这类点一下就变的东西：划中它们只会得到噪音。
   正文、工具结果、表格、清单都不在此列——那些正是用户想复制的。 */
.ai-msg-actions,
.ai-tools > summary,
.ai-tool > summary,
.ai-reasoning summary,
.ai-empty,
.ai-jump-bottom {
  user-select: none;
  -webkit-user-select: none;
}

.ai-msg-content-wrap {
  display: flex;
  flex-direction: column;
  gap: 14px;
  flex: 0 0 auto;
  min-height: 100%;
}

/* 离屏消息跳过后面的排版：三百条消息的会话里，打开时只有最后四五条可见，
   其余几百条的块布局/行盒计算全是白工（配合上面的工具结果截断，才是
   「长会话打开不再卡死」的完整解法）。
   contain-intrinsic-size 用 auto：浏览器会记住上次渲染出来的真实高度，
   滚动回来时不会因为「先按 96px 占位」而整页跳动。
   代价是离屏消息里的文字不参与 Ctrl+F 查找——对话页以滚动浏览为主，可以接受。 */
.ai-msg {
  display: flex; flex-direction: column; gap: 4px; max-width: 88%;
  content-visibility: auto; contain-intrinsic-size: auto 96px;
}

/* 「显示更早的消息」：长会话默认只挂最近 200 条，这条按钮是往上翻的入口。
   做成一行安静的次要按钮，不做成卡片——它只是分页，不是内容。 */
.ai-load-older {
  align-self: center;
  margin: 2px 0 6px;
  padding: 4px 12px;
  font-size: 11px;
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  cursor: pointer;
}
.ai-load-older:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }

.ai-msg.user { align-self: flex-end; align-items: flex-end; }

/* 助手消息允许铺满内容区：表格/代码块才能和输入框同宽。
   用户消息仍收 88%，气泡太宽就失去「我说的话」那种块感。 */
.ai-msg.assistant { align-self: flex-start; max-width: 100%; }

.ai-msg-role { font-size: 11px; color: var(--color-text-disabled); margin-bottom: 1px; display: flex; align-items: center; gap: 6px; }

.ai-msg-model {
  font-size: 10px; color: var(--color-text-disabled);
  background: var(--color-bg-tertiary); padding: 0 5px;
  border-radius: var(--radius-xs); opacity: 0.85;
}

.ai-msg-content {
  padding: 0;
  /* 与 app.css 的 .ai-md 保持同一个值：scoped 选择器带 [data-v] 优先级更高，
     这里写 13px 会把那边的 13.5px 盖掉，两边不同步就白改了。 */
  font-size: 14px;
  line-height: 1.75;
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

/* 计划模式的「开始执行」：和复制按钮同一行，但用强调色区分——
   它是这条回复上唯一的动作型按钮，跟「复制 / 存笔记」这类附属操作不是一类。 */
/* 用双类名提特异性：本条写在 .ai-copy 之前，同特异性下会被它的
   color: text-disabled 覆盖，靠书写顺序取胜太脆弱。 */
.ai-copy.ai-run-plan { color: var(--color-accent); font-weight: 500; }

.ai-copy.ai-run-plan:hover { color: var(--color-accent); background: var(--color-accent-bg); }

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

/* 重新生成：整行居中的软按钮，不抢正文的注意力（它是一次可选的重复操作） */
.ai-regen {
  align-self: center;
  display: flex;
  justify-content: center;
  margin: 2px 0 8px;
}

.ai-regen-btn {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 4px 12px;
  font-size: 11px; font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast);
}

.ai-regen-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border-color: var(--color-border-light);
}

/* 搜索命中跳转后的落点高亮：只闪一次，不做常驻状态。
   同时闪底色与一圈光晕——用户消息的底色画在气泡上，单靠容器的 background
   会被气泡盖住，看不到「跳到了哪」。 */
.ai-msg.is-hit-flash {
  border-radius: var(--radius-sm);
  animation: ai-hit-flash 1.7s ease-out;
}
@keyframes ai-hit-flash {
  0%   { background: var(--color-accent-bg); box-shadow: 0 0 0 5px var(--color-accent-bg); }
  100% { background: transparent; box-shadow: 0 0 0 0 transparent; }
}

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

/* 标题 / 段落 / 列表 / 任务清单 / 表格 / 引用 / 分隔线的样式都在
   styles/app.css 的「AI 回复正文：markdown 排版」一节。放全局而非 scoped：正文
   由 v-html 渲染，scoped 的 :deep 依赖 data-v 属性，该组件模块一旦没随 HMR 更新
   就整套失效；全局 CSS 走独立 style 热更新，不受组件模块刷新与否影响。
   这里只留「由组件自己拼出来」的部件：代码块外壳、表格滚动容器、排序表头。 */

.ai-md :deep(br) { display: inline; }

.ai-md :deep(a) { color: var(--color-accent); text-decoration: none; }

.ai-md :deep(a:hover) { text-decoration: underline; }

/* 代码块靠底色分块，不再画描边：消息区里已经有「正文无框」这条规则，
   代码块是内容、不是容器，描边会让它看起来又变成一张卡片。 */
.ai-md :deep(.md-code) {
  margin: 8px 0;
  border: none;
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
  border-bottom: none;
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

/* 表格容器只负责横向滚动，不画框：表格本身已经是「表头底色 + 行下边线」，
   外面再套一圈 border 就等于给数据加了个相框，比内容还抢眼。 */
.ai-md :deep(.md-table-wrap) {
  margin: 8px 0;
  overflow-x: auto;
  width: 100%;
}

/* 「导出 CSV」压到表格右上角，默认只是几个灰字：它是个偶尔才用的附属操作，
   不该占一条带底色的工具条。hover 时才浮出边框和强调色。 */
.ai-md :deep(.md-table-bar) {
  display: flex; justify-content: flex-end;
  padding: 0 2px 3px;
}

.ai-md :deep(.md-table-export) {
  display: inline-flex; align-items: center; gap: 3px;
  font-size: 11px; padding: 1px 7px;
  border: 1px solid transparent; border-radius: var(--radius-xs);
  background: transparent; color: var(--color-text-disabled);
  cursor: pointer;
  transition: color var(--transition-fast), background-color var(--transition-fast),
              border-color var(--transition-fast);
}

.ai-md :deep(.md-table-export:hover) {
  color: var(--color-accent);
  border-color: var(--color-border);
  background: var(--color-bg-secondary);
}

.ai-md :deep(.md-th-sortable) { cursor: pointer; user-select: none; white-space: nowrap; }

.ai-md :deep(.md-th-sortable:hover) { color: var(--color-accent); }

.ai-md :deep(.md-th-sortable[data-sort-dir="asc"]::after) { content: " ▲"; font-size: 10px; }

.ai-md :deep(.md-th-sortable[data-sort-dir="desc"]::after) { content: " ▼"; font-size: 10px; }

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

/* ---- 就地编辑历史提问 ---- */
.ai-msg-edit { display: flex; flex-direction: column; gap: var(--space-3); }
.ai-msg-edit-input {
  width: 100%;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: 13px;
  line-height: 1.6;
  resize: vertical;
  outline: none;
}
.ai-msg-edit-input:focus { border-color: var(--color-accent); }
.ai-msg-edit-ops { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-3); }
.ai-msg-edit-hint { font-size: 11px; color: var(--color-text-muted); }

/* 气泡尾部的单条费用：与 .ai-msg-stat 同属「一行安静的辅助信息」——
   无框、最弱一档颜色、允许换行（模型名 + 金额 + token 在窄窗口会折行）。
   只写在本 SFC 的 scoped 块里，不改全局 styles/*.css；
   WebView2 不支持 :has()/:is()/:container，这里也不依赖它们。 */
.ai-msg-cost {
  font-size: 10px;
  color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
  line-height: 1.6;
  white-space: normal;
  word-break: break-word;
  cursor: default;
}
</style>
