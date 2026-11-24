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
import { useSaveAs } from '../composables/useSaveAs'
import {
  ArrowDown, Bot, Check, ChevronDown, ChevronRight, Copy, ExternalLink, FileText, Globe, Loader2, Play, Sparkles, StickyNote, Undo2, Wrench, Save,
} from '@lucide/vue'
import AgentRunTree from './AgentRunTree.vue'
import AgentTeamCard from './AgentTeamCard.vue'
import AiDiffView from './AiDiffView.vue'
import AiPastedContent from './AiPastedContent.vue'
import AiPlanCard from './AiPlanCard.vue'
import type { LiveBlock } from '../composables/useAiMarkdownBlocks'
import type { AICompact, AIRun } from '../utils/aiRuns'
import { pickActiveRun } from '../utils/aiRuns'
import { fmtElapsed } from '../utils/aiRunTree'
import type {
  AIArtifact, AICheckpointView, AIJobRow, AIMessage, AIMsgImage, AIRuleFile, AISource, AIStreamStat, AIToolCall,
} from '../types/ai'
import {
  artifactsFromCalls, artifactsOf, hostOf, jobIdOf, rulesOf, rulesTitle, runIDOf, sourcesFromCalls, sourcesOf,
  toolIcon, toolList, toolOutput, toolResultLang, toolTarget, toolVerb as aiToolVerb,
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
  renderBlock: (md: string) => string
  renderMessageBlocks: (m: AIMessage) => string[]
  msgStat: (m: AIMessage) => AIStreamStat | null
  formatStat: (s: AIStreamStat) => string
  cpForCall: (tc: AIToolCall) => AICheckpointView | undefined
  /**
   * 回滚点的版本号（父组件传 checkpoints.length 即可）。
   *
   * v-memo 的依赖里必须有它：历史消息渲染时 checkpoints 还在异步加载，
   * 只靠 cpForCall 这个稳定引用的函数，checkpoints 到货后依赖值全不变 →
   * 命中缓存 → 撤销按钮不出现，得手动展开一次工具卡才冒出来。
   */
  cpVersion: number
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
  /**
   * 定时任务的会话：在每条用户消息旁标出执行时刻。
   *
   * 定时任务是「一个任务一条会话、每次执行往后追加一轮」，一轮的提问文字通常
   * 一模一样；不标时间的话，两次运行在界面上就是两段看不出区别的问答。
   */
  schedTimes?: boolean
  /** 消息分页：后端还有更早的消息 / 正在拉取 / 拉取动作（状态在 AIPage）。
   *  首屏只取最新 50 条，所以需要一条「从服务器加载更早」的入口。 */
  canLoadEarlier: boolean
  loadingEarlier: boolean
  loadEarlier: () => Promise<void>
  /**
   * 会话切换中：整块消息区淡出（见 .ai-messages.is-switching）。
   * 父组件在 activeId 变化时先置 true，数据 + 视口都就绪后再置 false，
   * 让「加载 + 200 条 markdown 渲染」这段卡顿发生在不可见期。
   * 淡出期间顺带屏蔽点击——那时 activeId 已经是新会话、messages 还是旧的，
   * 点中一条旧消息会触发基于错误上下文的操作。
   */
  switching?: boolean
}>()

// —— 流式状态从 runs 里自己取 ——
//
// 空值用稳定常量而不是字面量 `[]`：每次渲染都换新引用会让模板里的 .length / v-for
// 白 diff 一遍，也不能进 v-memo 依赖。
const NO_TOOLS: AIToolCall[] = []
const NO_BLOCKS: LiveBlock[] = []
const NO_RULES: AIRuleFile[] = []
const NO_COMPACTS: AICompact[] = []

/** 这一页该画的那条流（多条任务同时跑时只取属于当前会话的那条） */
const activeRun = computed<AIRun | null>(
  () => pickActiveRun(props.runs, props.activeId, props.draftRunId) ?? null,
)
const streaming = computed(() => !!activeRun.value)
const reasoningText = computed(() => activeRun.value?.reasoning ?? '')
// 已归档的各轮思考（后端在轮次边界发 reasoning_round）。
const streamReasonRounds = computed(() => activeRun.value?.reasonRounds ?? NO_REASON_ROUNDS)
const streamTools = computed<AIToolCall[]>(() => activeRun.value?.tools ?? NO_TOOLS)
/** 流式普通工具：派活交给 AgentTeamCard 单独成卡，不混进工具列表。 */
const plainStreamTools = computed(() => streamTools.value.filter((tc) => tc.name !== 'spawn_agent'))
const streamBlocks = computed<LiveBlock[]>(() => activeRun.value?.blocks ?? NO_BLOCKS)
const runRules = computed<AIRuleFile[]>(() => activeRun.value?.rules ?? NO_RULES)
// 这一轮的上下文压缩记录：通常一行都没有，长会话里才会有 1～2 行。
// 数字一律走下面那个 fmtTokens（与设置页、气泡费用同一套口径）——
// 界面上看到的两处 token 数必须是同一种写法，否则「48k」和「48.2K」会让人
// 以为是两个不同的量。
const runCompacts = computed<AICompact[]>(() => activeRun.value?.compacts ?? NO_COMPACTS)
// 流式期的产物：file_write 返回的当下就挂出来，等 done 再显示会让
// 「文件已经写完了」和「看得见」之间隔一整轮回答。
const streamArtifacts = computed<AIArtifact[]>(() => artifactsFromCalls(streamTools.value))
const streamSources = computed<AISource[]>(() => sourcesFromCalls(streamTools.value))

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
  /** 点某条回复末尾的「本轮用量」：打开这条消息自己的 token 明细弹框 */
  (e: 'open-usage', m: AIMessage): void
  (e: 'retry'): void
  /** 被中断/停止的回复旁「继续生成」：让父组件把「继续」作为新 user 消息发出 */
  (e: 'continue'): void
  (e: 'msg-click', ev: MouseEvent): void
  (e: 'msg-mouseup', ev: MouseEvent | KeyboardEvent): void
  (e: 'msg-contextmenu', ev: MouseEvent): void
}>()

const { t } = useI18n()
const sa = useSaveAs()

/** 逐条回复的「另存为」：共享菜单一次给出笔记 / 待办 / 知识库三个目标。 */
function saveAsMsg(e: MouseEvent, m: AIMessage) {
  if (!m.content) return
  sa.open(
    {
      text: m.content,
      source: 'ai',
      sourceMeta: { convId: props.activeId, messageId: m.id },
    },
    { x: e.clientX, y: e.clientY },
  )
}

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

// 这条用户消息发出时挂上的技能名。落库的是 JSON 数组字符串，坏数据一律当空，
// 不让一条脏记录把整条消息渲染打断。
const skillCache = new Map<string, { sig: string; list: string[] }>()
function skillsOf(m: AIMessage): string[] {
  const raw = m.skills || ''
  if (!raw) return []
  const hit = skillCache.get(m.id)
  if (hit && hit.sig === raw) return hit.list
  let list: string[] = []
  try {
    const v = JSON.parse(raw)
    if (Array.isArray(v)) list = v.filter((x): x is string => typeof x === 'string' && !!x.trim())
  } catch {
    list = []
  }
  skillCache.set(m.id, { sig: raw, list })
  return list
}

// 用户消息插入过的文件：本地刚发的消息只有 attached_files JSON，
// 历史消息由 AIPage 解析成 attachedList。两种都吃，坏 JSON 当空。
const attachedCache = new Map<string, { sig: string; list: { name: string; path?: string }[] }>()
function attachedOf(m: AIMessage): { name: string; path?: string }[] {
  if (m.attachedList?.length) return m.attachedList
  const raw = m.attached_files || ''
  if (!raw) return []
  const hit = attachedCache.get(m.id)
  if (hit && hit.sig === raw) return hit.list
  let list: { name: string; path?: string }[] = []
  try {
    const v = JSON.parse(raw)
    if (Array.isArray(v)) {
      list = v
        .filter((x: any) => x && typeof x.name === 'string' && !!x.name)
        .map((x: any) => ({ name: x.name, path: typeof x.path === 'string' ? x.path : '' }))
    }
  } catch { list = [] }
  attachedCache.set(m.id, { sig: raw, list })
  return list
}

// 分轮思考的解析缓存（与 toolsOf 同一个理由：模板里一条消息会取好几次）。
interface ReasonRound {
  round: number
  text: string
}
const reasonRoundCache = new Map<string, { sig: string; list: ReasonRound[] }>()
function reasonRoundsOf(m: AIMessage): ReasonRound[] {
  const raw = m.reasoning_rounds || ''
  if (!raw) return NO_REASON_ROUNDS
  const sig = `${m.id}:${raw.length}`
  const hit = reasonRoundCache.get(m.id)
  if (hit && hit.sig === sig) return hit.list
  let list: ReasonRound[] = []
  try {
    const v = JSON.parse(raw)
    if (Array.isArray(v)) {
      list = v
        .filter((x) => x && typeof x.text === 'string' && x.text.trim() !== '')
        .map((x) => ({ round: Number(x.round) || 0, text: String(x.text) }))
    }
  } catch {
    list = []
  }
  reasonRoundCache.set(m.id, { sig, list })
  return list
}

/**
 * 这张工具卡片之前要不要显示「本轮思考」，返回空串表示不显示。
 *
 * 只在「这一轮的第一个调用」前面插一次：一轮里连调五个工具时，每个卡片上方都
 * 重复同一段思考只是噪音。轮次号来自工具记录（`round`），与 thinking 记录一一对应。
 */
function reasoningBefore(m: AIMessage, i: number): string {
  const rounds = reasonRoundsOf(m)
  if (!rounds.length) return ''
  const list = toolsOf(m)
  const cur = Number(list[i]?.round) || 0
  const prev = i > 0 ? Number(list[i - 1]?.round) || 0 : -1
  if (i > 0 && prev === cur) return ''
  const hit = rounds.find((x) => x.round === cur)
  return hit ? hit.text : ''
}

/**
 * 没有被任何工具卡片消费掉的思考（通常是最后一轮作答的推理）。
 *
 * 为什么要单独算：分轮思考是按 `round` 往工具卡片上方挂的，而作答轮往往没有
 * 工具调用，它的 round 落不到任何卡片上——不补这一块，那一段思考就永远不显示，
 * 而顶部整段又因为「有分轮信息」被隐藏了（用户看到的就是「思考丢了」）。
 */
function trailingReasoning(m: AIMessage): string {
  const rounds = reasonRoundsOf(m)
  if (!rounds.length) return ''
  const used = new Set<number>()
  for (const tc of toolsOf(m)) used.add(Number(tc.round) || 0)
  return rounds
    .filter((r) => !used.has(r.round))
    .map((r) => r.text)
    .join('\n\n')
}

// 空数组常量：没有分轮信息时返回同一个引用，避免每次渲染都造新数组
// （v-if 会因此认为依赖变了，把整块重算一遍）。
const NO_REASON_ROUNDS: ReasonRound[] = []

const artCache = new Map<string, { sig: string; list: AIArtifact[] }>()

function artsOf(m: AIMessage): AIArtifact[] {
  const sig = `${m.id}:${m.role}:${(m.tool_calls || '').length}`
  const hit = artCache.get(m.id)
  if (hit && hit.sig === sig) return hit.list
  const list = artifactsOf(m)
  artCache.set(m.id, { sig, list })
  return list
}

const sourceCache = new Map<string, { sig: string; list: AISource[] }>()
function sourcesOfMsg(m: AIMessage): AISource[] {
  const sig = `${m.id}:${m.role}:${(m.tool_calls || '').length}`
  const hit = sourceCache.get(m.id)
  if (hit && hit.sig === sig) return hit.list
  const list = sourcesOf(m)
  sourceCache.set(m.id, { sig, list })
  return list
}


/** i18n 的 t 由本组件提供，工具动词的翻译在这里接上 */
function toolVerb(name: string): string {
  return aiToolVerb(name, t)
}

// —— 派活 = 团队成员卡片 ——
// 编排树（AgentRunTree.vue）是消息底部按 run_id 拉的完整树，流式期看不到；
// 消息里的派活走 AgentTeamCard.vue：一人一卡的成员视图，流式/历史都渲染。
// 这里只负责把 tool_calls 拆成「普通工具」和「派活」两拨，分别交给两处模板。

/** 派活调用子集，交给 AgentTeamCard 渲染成团队成员卡。 */
function spawnsOf(list: AIToolCall[]): AIToolCall[] {
  return list.filter((tc) => tc.name === 'spawn_agent')
}

/** 普通工具子集：工具列表只显示这些，派活不混在里面。 */
function plainToolsOf(m: AIMessage): AIToolCall[] {
  return toolsOf(m).filter((tc) => tc.name !== 'spawn_agent')
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

/**
 * 最后一条带清单的 assistant 消息 = 当前清单，更早的是历史归档。
 *
 * 续跑（用户发「继续」）时模型可能另建一份新清单：旧清单跟着旧消息留在历史里，
 * 冻结在 0/5 看着像没做完。把非最新的清单标记成归档，默认折叠并注明「历史」，
 * 避免同一屏里两张清单并排时误以为任务又回到起点。
 */
const latestPlanMsgId = computed(() => {
  for (let i = shownMessages.value.length - 1; i >= 0; i--) {
    const m = shownMessages.value[i]
    if (m.role === 'assistant' && m.plan) return m.id
  }
  return ''
})

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

// 「从服务器加载更早」：与 showOlder（只放开已加载的窗口）不同，这条真的去后端
// 拉更旧的消息并 prepend 到 messages。同样做 scrollTop 补偿，别让正在看的位置滑走。
async function onLoadEarlier() {
  const el = msgArea.value
  const beforeH = el ? el.scrollHeight : 0
  const beforeTop = el ? el.scrollTop : 0
  await props.loadEarlier()
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

// msgClock 从 created_at 截出「MM-DD HH:mm」，给定时任务的每轮执行标时间。
//
// 用切片而不是 Date 解析：库里存的是带 +08:00 的字面量，交给 Date 再按本地时区
// 格式化会多一次换算，跨时区或夏令时会凭空偏几小时；这里要的就是「当时看到的钟点」。
function msgClock(s: string | undefined): string {
  const v = (s || '').trim()
  if (v.length < 16 || v[7] !== '-' || v[13] !== ':') return ''
  return v.slice(5, 10) + ' ' + v.slice(11, 16)
}

/**
 * 回复完成时刻：给消息脚注的统计行补上「这条什么时候跑完的」。
 * 不走 msgClock 的切片——切会话刚定格的消息 created_at 是 new Date().toISOString()
 * （UTC 的 Z 结尾），库里读回的却是带 +08:00 的字面量；直接切片会把新消息
 * 显示成 UTC，差 8 小时。交给 Date 解析，两种格式都落到本地 HH:mm。
 */
function finishClock(s: string | undefined): string {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return ''
  return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0')
}

// —— 工具卡片的展开状态 ——
//
// 工具详情一律默认收起，历史消息和流式期同一口径，是否展开完全交给用户点
// <summary> 决定（原生 <details> 自己记状态）。
//
// 早先这里有一套「程序性自动展开」：running / 待审批的调用由 :open 强制打开，
// 并用 autoOpen / userClosed 两个 Set 做粘性。实测一轮调五六个工具时，整屏全是
// 命令输出，正文被挤出视野；回复结束后又留下一排展开的卡片。现在改为默认收起：
// 摘要行给出工具数，点开后每条调用自带状态（运行中 / 待审批 / 耗时）与输出。
// 待审批的风险说明在输入区上方的审批条里另有一份，不依赖卡片展开。
//
// 注意别再加回 `:open` 绑定：流式期 streamTools 每次更新都会 patch，绑定会把
// 用户手动点开的状态覆盖回去。

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
  <div v-key-click ref="msgArea" class="ai-messages" :class="{ 'is-switching': switching }" @scroll="onMsgScroll" @mouseup="emit('msg-mouseup', $event)" @keyup="emit('msg-mouseup', $event)" @click="emit('msg-click', $event)" @contextmenu="emit('msg-contextmenu', $event)">
    <div ref="msgContent" class="ai-msg-content-wrap">
      <div v-if="!hasKey" class="ai-need-key">
        <Bot :size="40" />
        <p>{{ t('aiNeedKey') }}</p>
        <button class="ai-go-settings" @click="emit('open-settings')">{{ t('aiGoSettings') }}</button>
      </div>

      <template v-else>
        <!-- 从后端加载更早的历史：首屏只取最新 50 条（见 AIPage.loadMessages），
             这里把窗口往上扩。样式与「放开已加载窗口」的按钮共用。 -->
        <button v-if="canLoadEarlier" class="ai-load-older" :disabled="loadingEarlier" @click="onLoadEarlier">
          {{ loadingEarlier ? t('aiLoadEarlierDoing') : t('aiLoadEarlierFromServer') }}
        </button>
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
          v-memo="[m.content, m.reasoning_content, statMap.get(m.id), costLines.get(m.id), locale, (m.imgList?.length ?? 0), m.attached_files, m.attachedList?.length, m.tool_calls, m.skills, m.plan, m.id !== latestPlanMsgId, mode, jobSeqOf(m), m.id === messages[messages.length - 1]?.id, expandedInMessage(m), schedTimes, m.created_at, cpVersion]"
          :class="['ai-msg', m.role]"
        >
          <div class="ai-msg-role">
            {{ m.role === 'user' ? t('aiYou') : 'AI' }}
            <!-- 定时任务每轮执行的时刻：同一个任务只有一条会话，靠它把各次运行分开 -->
            <span v-if="schedTimes && m.role === 'user' && msgClock(m.created_at)" class="ai-msg-clock">{{ msgClock(m.created_at) }}</span>
            <!-- 多档案（不同模型/档案）混着聊时，不写模型名就分不清这句是谁答的 -->
            <span v-if="m.role === 'assistant' && m.model" class="ai-msg-model">{{ m.model }}</span>
          </div>
          <!-- 这条消息挂上的技能。技能只活在提示词里（名字 + SKILL.md 路径），
               模型读了也只留一条工具行，不标出来用户就看不出「我选的技能生效了没有」。 -->
          <div v-if="m.role === 'user' && skillsOf(m).length" class="ai-msg-skills">
            <span v-for="n in skillsOf(m)" :key="n" class="ai-msg-skill">
              <Sparkles :size="10" />
              {{ n }}
            </span>
          </div>
          <!-- 插入的文件：选中时在输入框上方有 chip，发出去后同样要在气泡里留痕，
               否则刷新后只剩一句问题，看不出当时递给 AI 的是哪份文档。 -->
          <div v-if="m.role === 'user' && attachedOf(m).length" class="ai-msg-attached">
            <span v-for="(f, fi) in attachedOf(m)" :key="fi" class="ai-msg-attached-chip" :title="f.path || t('aiAttachPastedHint')">
              <FileText :size="11" />
              <span class="ai-msg-attached-name">{{ f.name }}</span>
            </span>
          </div>
          <!-- 图片缩略图：url 类直发链接，file 类用已加载的 dataUrl；历史 file 图由 loadMessages 懒加载填回 -->
          <div v-if="m.imgList && m.imgList.length" class="ai-msg-imgs">
            <div v-for="(img, bi) in m.imgList" :key="bi" class="ai-msg-img">
              <img v-key-click v-if="img.url" :src="img.url" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="emit('open-img', img)" />
              <img v-key-click v-else-if="img.dataUrl" :src="img.dataUrl" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="emit('open-img', img)" />
              <div v-else class="ai-msg-img-el ai-img-broken">{{ t('aiImageBroken') }}</div>
            </div>
          </div>
          <!-- 整段思考：只在**没有**分轮信息时显示（旧数据、或模型只在一轮里思考）。
               有分轮信息时，思考已经贴到对应那一轮的工具卡片上方了。 -->
          <details v-if="m.reasoning_content && !reasonRoundsOf(m).length" class="ai-reasoning">
            <summary>{{ t('aiThought') }} · {{ t('aiThoughtChars', { n: m.reasoning_content.length }) }}</summary>
            <div class="ai-reasoning-content">{{ m.reasoning_content }}</div>
          </details>
          <!-- 工具调用卡片：放在正文之前，因为调用发生在作答之前。
               默认折叠——一段回答调了五个工具时，全展开会把正文挤出屏幕；
               摘要行只说「调了几个」，要看细节自己点开。 -->
          <details v-if="m.role === 'assistant' && plainToolsOf(m).length" class="ai-tools">
            <summary class="ai-tools-sum">
              <Wrench :size="12" class="ai-tools-ico" />
              <span>{{ t('aiToolSummary', { n: plainToolsOf(m).length }) }}</span>
              <ChevronDown :size="13" class="ai-tools-chev" />
            </summary>
            <div class="ai-tools-body">
              <!-- 每条调用自带折叠：一次回答调了五六个工具时，用户只想展开
                   其中一条看细节，而不是要么全看要么全不看。
                   有分轮思考时，在「这一轮的第一个调用」上方插一段思考——这样
                   「它当时在想什么」就贴在那一步旁边，而不是全堆在回复顶部。
                   派活不在这里：它们交给下方的 AgentTeamCard 单独成卡。 -->
              <template v-for="(tc, ti) in toolsOf(m)" :key="tc.id">
              <details v-if="reasoningBefore(m, ti)" class="ai-reasoning is-round">
                <summary>
                  {{ t('aiThought') }} ·
                  {{ t('aiThoughtChars', { n: reasoningBefore(m, ti).length }) }}
                </summary>
                <div class="ai-reasoning-content">{{ reasoningBefore(m, ti) }}</div>
              </details>
              <details v-if="tc.name !== 'spawn_agent'" class="ai-tool" :class="{ 'is-err': !!tc.error }">
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
                  <span v-else-if="tc.ms" class="ai-tool-ms">{{ fmtElapsed(tc.ms) }}</span>
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
              </template>
            </div>
          </details>
          <!-- 团队卡独立于工具列表：普通工具（如「列出」）留在上面的工具列表里，
               派活单独成卡放下面，不把普通工具卷进团队分组。 -->
          <AgentTeamCard v-if="spawnsOf(toolsOf(m)).length" :calls="spawnsOf(toolsOf(m))" />
          <!-- 没被任何工具卡片消费掉的思考：通常是最后一轮（作答轮）的推理，
               它没有对应的工具调用，挂在工具列表之后、正文之前。 -->
          <details v-if="trailingReasoning(m)" class="ai-reasoning">
            <summary>{{ t('aiThought') }} · {{ t('aiThoughtChars', { n: trailingReasoning(m).length }) }}</summary>
            <div class="ai-reasoning-content">{{ trailingReasoning(m) }}</div>
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
          <!-- 用户消息：粘贴的大段文字在发送时按约定展开成 <pasted_text> 发给模型，
               这里按同一标记折回 chip，点开看全文；正文原文不动（复制/重试照旧）。 -->
          <div v-else class="ai-msg-content ai-plain"><AiPastedContent :content="m.content" /></div>
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
          <!-- 联网来源：AI 调用 web_fetch / web_links / 外部搜索工具时结果里带 URL。
               和产物一样从 tool_calls 就地解析，不另开表、不发事件。默认折叠成一行
               「🔥 N 个来源」，点开看链接列表。 -->
          <details v-if="m.role === 'assistant' && sourcesOfMsg(m).length" class="ai-sources">
            <summary class="ai-sources-sum">
              <span class="ai-sources-flame">🔥</span>
              {{ t('aiSourcesCount', { n: sourcesOfMsg(m).length }) }}
              <ChevronDown :size="12" class="ai-sources-chev" />
            </summary>
            <div class="ai-sources-list">
              <a
                v-for="s in sourcesOfMsg(m)"
                :key="s.url"
                class="ai-source"
                :href="s.url"
                target="_blank"
                rel="noopener noreferrer"
                :title="s.url"
              >
                <Globe :size="13" />
                <span class="ai-source-title">{{ s.title || hostOf(s.url) }}</span>
                <ExternalLink :size="11" class="ai-source-ext" />
              </a>
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
            <!-- 继续生成：把「继续」作为新 user 消息发出，后端续写这段半截回答。
                 只给最后一条 assistant 消息挂（更早的中断回答已经翻篇，再续会接错上下文）。 -->
            <button
              v-if="m.stopped && m.id === messages[messages.length - 1]?.id"
              class="ai-copy ai-continue"
              :title="t('aiContinueHint')"
              @click="emit('continue')"
            >
              <Play :size="12" /> {{ t('aiContinue') }}
            </button>
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
            <button v-if="m.role === 'assistant' && m.content" class="ai-copy" @click="saveAsMsg($event, m)" :title="t('saveAsTitle')">
              <Save :size="12" /> {{ t('saveAsTitle') }}
            </button>
            <!-- 单条回复统计：耗时 / 首字延迟 / token 用量。末尾补完成时刻：
                 同屏里日期都一样，只给 HH:mm，回答「这条几点跑完的」。 -->
            <span v-if="msgStat(m)" class="ai-msg-stat">
              {{ formatStat(statWithoutCost(msgStat(m)!)) }}<template v-if="finishClock(m.created_at)"> · {{ t('aiStatFinishedAt', { t: finishClock(m.created_at) }) }}</template>
            </span>
            <!-- 本轮用量：这条回复自己的 token 明细（提供方/模型、命中率、缓存读写）。
                 点开弹框，数据由父组件从这条消息的 usage 现取，不走会话级聚合。 -->
            <button
              v-if="m.role === 'assistant' && msgStat(m)"
              class="ai-copy ai-msg-usage"
              :title="t('aiMsgUsageTip')"
              @click="emit('open-usage', m)"
            >{{ t('aiMsgUsageTitle') }}</button>
          </div>
          <!-- 气泡尾部：这一轮的费用（模型名 + 金额 + token）。文案由 costLines
               预生成，这里只查一次表；关掉设置里的开关后整行不渲染。 -->
          <div v-if="costLines.get(m.id)" class="ai-msg-cost">{{ costLines.get(m.id) }}</div>
          <!-- 任务清单挂在**生成它的这条回复**里，跟着对话一起滚。
               早先它是消息流末尾一个固定插槽，上一轮的清单会一直挂在最新回复下面。
               本会话内由 SSE 固化时写到 m.plan；刷新后由父组件回填到最后一条。 -->
          <AiPlanCard v-if="m.role === 'assistant' && m.plan" :card="m.plan" :archived="m.id !== latestPlanMsgId" class="ai-msg-plan" />
        </div>

        <!-- 历史消息之后、流式气泡之前：上下文压缩分隔行按自己的时间线落位。
             任务清单不再放这里——它属于某一条具体回复，见上面的 .ai-msg-plan。 -->
        <!-- 上下文压缩：一条分隔行插在流式气泡之前。
             位置和语义都对：压缩发生在「这一问的上下文刚组装完、还没调模型」那一刻，
             所以它在时间线上就是回答之前的一件事。
             为什么不留到 done 再显示：摘要是一次几秒的模型调用，这几秒里界面
             原本一片空白，用户会以为卡死去点停止；而点完恰好吃掉一条正在写库的摘要。
             第二步（done）只把同一行改文案，不新增一行——一次压缩只该出现一次。 -->
        <div
          v-for="c in runCompacts"
          :key="c.id"
          class="ai-compact"
          :class="{ 'is-running': c.phase === 'start' }"
          :title="t('aiCompactTitle', { window: fmtTokens(c.window), limit: fmtTokens(c.limit), floor: fmtTokens(c.floor) })"
        >
          <Loader2 v-if="c.phase === 'start'" :size="12" class="ai-compact-ico is-spin" />
          <Check v-else :size="12" class="ai-compact-ico" />
          <span class="ai-compact-text">
            {{
              c.phase === 'start'
                ? t('aiCompacting')
                : c.kind === 'loop'
                  ? t('aiCompactFolded')
                  : t('aiCompacted')
            }}
          </span>
          <span v-if="c.phase === 'done'" class="ai-compact-num">
            {{ fmtTokens(c.before) }} → {{ fmtTokens(c.after) }}
          </span>
          <!-- 折叠条数：折叠与摘要是两种不同的动作，用户得知道「是丢细节还是真总结过」。 -->
          <span v-if="c.phase === 'done' && c.kind === 'loop'" class="ai-compact-num">
            {{ t('aiCompactFoldedCount', { n: c.dropped }) }}
          </span>
          <!-- 摘要失败时降级为硬截断：必须说，否则用户看到「压缩了但 AI 还是忘了
               中间那段」，会以为是模型的问题而不是这里退化了。
               只在「真发起过摘要且失败」时出现——冷却期与循环内折叠都不属于失败。 -->
          <span
            v-if="c.phase === 'done' && c.kind === 'history' && c.attempted && !c.summary"
            class="ai-compact-warn"
          >{{ t('aiCompactTruncated') }}</span>
        </div>

        <!-- 流式思考：与历史消息、工具卡片同一口径，默认收起。
             长推理动辄几千字，常驻展开会把正文挤到屏幕外；摘要行给出实时字数，
             首字之前那十几秒仍有「它还在动」的进度可看，想看过程自己点开。
             已完成的轮次单独成块（后端在轮次边界发 reasoning_round），
             当前这一轮还在长的部分挂在最后——不然十几轮的思考会实时糊成一个。
             正文开始输出后文案从「思考中」切成历史消息用的「已思考」。 -->
        <div v-if="streamReasonRounds.length || reasoningText" class="ai-reasoning">
          <details v-for="r in streamReasonRounds" :key="'rr' + r.round" class="is-round">
            <summary>{{ t('aiThought') }} · {{ t('aiThoughtChars', { n: r.text.length }) }}</summary>
            <div class="ai-reasoning-content">{{ r.text }}</div>
          </details>
          <details v-if="reasoningText">
            <summary>{{ hasStreamText ? t('aiThought') : t('aiThinking') }} · {{ t('aiThoughtChars', { n: reasoningText.length }) }}</summary>
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
          <!-- 流式期与历史消息同一口径：默认收起。摘要行给出工具数，
               想看正在跑哪个、看输出，点开一层即可。派活不在这里，见下方团队卡。 -->
          <details v-if="plainStreamTools.length" class="ai-tools">
            <summary class="ai-tools-sum">
              <Wrench :size="12" class="ai-tools-ico" />
              <span>{{ t('aiToolSummary', { n: plainStreamTools.length }) }}</span>
              <ChevronDown :size="13" class="ai-tools-chev" />
            </summary>
            <div class="ai-tools-body">
              <!-- 每条也默认收起，交给原生 details 自己记展开状态。
                   is-run / is-ask 只改图标与状态文字，不再驱动自动展开。 -->
              <details v-for="tc in plainStreamTools" :key="tc.id" class="ai-tool" :class="{ 'is-err': !!tc.error, 'is-run': tc.running, 'is-ask': tc.approval === 'pending' }">
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
                  <span v-else-if="tc.ms" class="ai-tool-ms">{{ fmtElapsed(tc.ms) }}</span>
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
          <!-- 团队卡独立于工具列表：派活一人一卡，普通工具留在上面的工具列表里。 -->
          <AgentTeamCard v-if="spawnsOf(streamTools).length" :calls="spawnsOf(streamTools)" />
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
          <!-- 流式期来源：web 工具一返回就出现，不等回答收尾 -->
          <details v-if="streamSources.length" class="ai-sources" open>
            <summary class="ai-sources-sum">
              <span class="ai-sources-flame">🔥</span>
              {{ t('aiSourcesCount', { n: streamSources.length }) }}
              <ChevronDown :size="12" class="ai-sources-chev" />
            </summary>
            <div class="ai-sources-list">
              <a
                v-for="s in streamSources"
                :key="s.url"
                class="ai-source"
                :href="s.url"
                target="_blank"
                rel="noopener noreferrer"
                :title="s.url"
              >
                <Globe :size="13" />
                <span class="ai-source-title">{{ s.title || hostOf(s.url) }}</span>
                <ExternalLink :size="11" class="ai-source-ext" />
              </a>
            </div>
          </details>
          <!-- 流式清单：这一轮 plan 事件一到就显示在这条流式气泡里，收尾固化后
               转成对应历史消息上的卡片，位置不会跳到消息流末尾。 -->
          <AiPlanCard v-if="activeRun?.plan" :card="activeRun.plan" :archived="false" class="ai-msg-plan" />
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
  font-size: var(--text-sm); margin: var(--space-1) 0; max-width: 100%;
}

.ai-reasoning details { background: none; border-radius: 0; padding: 0; }

.ai-reasoning summary {
  width: fit-content; max-width: 100%;
  /* 负 margin 抵消内边距：hover 底色向外扩，文字仍与正文同一条左边缘 */
  padding: 3px var(--space-3); margin-left: -6px;
  border-radius: var(--radius-sm);
  cursor: pointer; color: var(--color-text-muted); font-size: var(--text-sm);
  /* 同 .ai-tools > summary：fit-content 下不 nowrap 会被挤到第二行 */
  white-space: nowrap;
  user-select: none; outline: none;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-reasoning summary:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.ai-reasoning-content {
  margin-top: var(--space-1); padding: var(--space-1) 0 var(--space-1) 10px; border-left: 2px solid var(--color-border);
  color: var(--color-text-muted); font-size: var(--text-md); line-height: 1.7;
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
  margin: var(--space-2) 0 var(--space-1);
  max-width: 100%;
}

.ai-tools > summary {
  display: flex;
  align-items: center;
  gap: 7px;
  width: fit-content;
  max-width: 100%;
  /* 负 margin 抵消内边距：hover 底色向外扩，文字仍与正文同一条左边缘 */
  padding: 3px var(--space-3);
  margin-left: -6px;
  border-radius: var(--radius-sm);
  list-style: none;
  cursor: pointer;
  user-select: none;
  outline: none;
  font-size: var(--text-sm);
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
  gap: var(--space-3);
  /* 宽度跟着内容走。铺满整行时，hover 底色会从动词一直拉到右边，中间一大段
     空白，眼睛要横穿过去才发现行尾有个箭头；fit-content 之后这行读起来就是
     一句连续的「读了哪个文件、改了几行」。max-width 卡住 100% 是防止长路径
     把消息区顶出横向滚动条（负 margin 已让盒子左移 6px）。 */
  width: fit-content;
  max-width: 100%;
  /* 左内边距 6px 由负 margin 抵消：hover 底色向外扩，文字仍与正文同一条左边缘 */
  padding: var(--space-2) var(--space-3);
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

.ai-tool-verb { flex: 0 0 auto; font-size: var(--text-sm); color: var(--color-text-primary); }

/* 这行里唯一需要「一眼确认是谁」的就是目标（文件名 / 命令），用主色把它
   从一串灰字里挑出来；等宽体去掉了——路径不是要对齐的代码，界面字体读得更快，
   而下面展开的命令输出仍然是等宽。 */
.ai-tool-target {
  flex: 0 1 auto;
  min-width: 0;
  max-width: 320px;
  font-size: var(--text-sm);
  font-weight: 500;
  color: var(--color-accent);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-tool-diff { display: flex; gap: var(--space-2); flex: 0 0 auto; font-family: var(--font-mono); font-size: var(--text-xs); }

.ai-diff-add { color: var(--color-success); font-style: normal; }

.ai-diff-del { color: var(--color-danger); font-style: normal; }

/* 时长/状态跟在增删数后面，用最弱的一档颜色——它只是参考信息，
   用户真正关心的是「动的是哪个文件、改了几行」。不再用胶囊底色：
   一列行里只有它带底，看起来像漏了一个没去掉的卡片。 */
.ai-tool-ms {
  flex: 0 0 auto;
  padding: 0;
  background: none;
  font-size: var(--text-xs);
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
.ai-approval { margin: 0 var(--space-3) var(--space-3) 18px; }

.ai-approval { display: flex; flex-direction: column; gap: var(--space-3); }

.ai-approval-risk {
  font-size: var(--text-xs);
  line-height: 1.55;
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  padding: 5px var(--space-4);
  border-left: 2px solid var(--color-warning);
}

.ai-approval-hint { font-size: var(--text-xs); color: var(--color-warning); }

.ai-approval-done { font-size: var(--text-xs); color: var(--color-text-muted); }

/* diff 由子组件渲染，.qd-diff 自带边框，这里只补对齐用的外边距 */
.ai-tool :deep(.qd-diff) { margin: 0 var(--space-3) var(--space-3) 18px; }

.ai-tool-undo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  padding: var(--space-1) var(--space-2);
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ai-tool-undo:hover { color: var(--color-accent); background: var(--color-bg-hover); }

.ai-rules {
  display: flex; align-items: center; gap: var(--space-2);
  margin: var(--space-1) 0 var(--space-2); font-size: var(--text-xs); color: var(--color-text-muted);
}

.ai-rules-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}

/* 上下文压缩的分隔行。
   两侧的细线不是装饰：这一行说的是「时间线上的一件事」，不是某个人说的话，
   没有线就会跟下面那条 AI 回答混成一体。文字固定居中，所以压缩前后只换字不换位。 */
.ai-compact {
  display: flex; align-items: center; gap: var(--space-2);
  margin: var(--space-3) 0;
  font-size: var(--text-xs); color: var(--color-text-muted);
}

.ai-compact::before,
.ai-compact::after {
  content: '';
  flex: 1 1 auto;
  height: 1px;
  background: var(--color-border);
}

.ai-compact-ico { flex: none; }
.ai-compact-ico.is-spin { animation: ai-compact-spin 900ms linear infinite; }
@keyframes ai-compact-spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }

.ai-compact-text { flex: none; }

/* 「48k → 12k」：等宽字体让两个数位数对齐，压缩前后的量级差一眼看得出来 */
.ai-compact-num {
  flex: none;
  font-family: var(--font-mono);
  color: var(--color-text-secondary);
}

.ai-compact-warn { flex: none; color: var(--color-warning); }

/* 压缩进行中给一点强调色：那是唯一一个「正在花你的钱等几秒」的状态 */
.ai-compact.is-running { color: var(--color-text-secondary); }
.ai-compact.is-running .ai-compact-ico { color: var(--color-accent); }


.ai-tool-err {
  font-size: var(--text-xs); color: var(--color-danger);
  word-break: break-word;
}

.ai-tool-res {
  font-size: var(--text-xs); color: var(--color-text-secondary);
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
  margin: 0 var(--space-3) var(--space-3) 18px;
  padding: var(--space-1) var(--space-3);
  font-size: var(--text-2xs);
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
  display: flex; align-items: center; gap: var(--space-4);
  margin-top: var(--space-2); padding: var(--space-1) 0;
  font-size: var(--text-2xs); line-height: 1.5; color: var(--color-text-muted);
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
  flex: 0 0 auto; padding: 0 var(--space-1);
  border: none; background: none;
  font-family: inherit; font-size: var(--text-2xs); color: var(--color-text-muted);
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
  /* 会话切换过渡：淡入走 base、淡出走 fast（和导航切页的手感一致）。
     只动 opacity、不碰 transform——这是滚动容器，transform 会造出 containing
     block 影响内部定位；而淡入淡出已经足够消掉「啪一下换内容」的突兀感。 */
  transition: opacity var(--transition-base);
}

/* 切会话途中先淡出并吃掉点击。父组件在数据加载 + 视口就绪后才摘掉这个 class，
   于是「拉消息 + 渲染 200 条 markdown」这段最卡的活发生在看不见的时候；
   淡出期间 activeId 已是新会话、messages 还是旧的，屏蔽点击免得点中旧气泡。
   transition 必须放在基线上：写在 .is-switching 里的话，class 一摘动画属性
   也跟着消失，淡入就变成硬切——上一版正是这样，切回来时内容突然蹦出来。 */
.ai-messages {
  transition: opacity var(--transition-base);
}
.ai-messages.is-switching {
  opacity: 0;
  pointer-events: none;
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
  display: flex; flex-direction: column; gap: var(--space-2); max-width: 88%;
  content-visibility: auto; contain-intrinsic-size: auto 96px;
}

/* 「显示更早的消息」：长会话默认只挂最近 200 条，这条按钮是往上翻的入口。
   做成一行安静的次要按钮，不做成卡片——它只是分页，不是内容。 */
.ai-load-older {
  align-self: center;
  margin: var(--space-1) 0 var(--space-3);
  padding: var(--space-2) var(--space-5);
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  cursor: pointer;
}
.ai-load-older:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }
.ai-load-older:disabled { opacity: 0.6; cursor: default; }

.ai-msg.user { align-self: flex-end; align-items: flex-end; }

/* 助手消息允许铺满内容区：表格/代码块才能和输入框同宽。
   用户消息仍收 88%，气泡太宽就失去「我说的话」那种块感。 */
.ai-msg.assistant { align-self: flex-start; max-width: 100%; }

.ai-msg-role { font-size: var(--text-xs); color: var(--color-text-disabled); margin-bottom: 1px; display: flex; align-items: center; gap: var(--space-3); }

.ai-msg-model {
  font-size: var(--text-2xs); color: var(--color-text-disabled);
  background: var(--color-bg-tertiary); padding: 0 5px;
  border-radius: var(--radius-xs); opacity: 0.85;
}

/* 定时任务每轮执行的时刻。等宽数字，纵向看下来时间列是齐的。 */
.ai-msg-clock {
  font-size: var(--text-2xs); color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
}

/* 这条消息挂上的技能：一枚小胶囊，跟角色行一起读作「用户 + 用了什么」。
   用 accent 的淡底而不是灰底——这是「技能生效了」的正面信息，不该和
   「几点发的」这种元数据长得一样。 */
.ai-msg-skills { display: flex; flex-wrap: wrap; gap: var(--space-2); margin: 2px 0 var(--space-2); }

.ai-msg.user .ai-msg-skills { justify-content: flex-end; }

/* 插入文件 chip 与输入框上方那排同款：消息区里只留名字，路径放 title。 */
.ai-msg-attached { display: flex; flex-wrap: wrap; gap: var(--space-2); margin: 2px 0 var(--space-2); }
.ai-msg.user .ai-msg-attached { justify-content: flex-end; }
.ai-msg-attached-chip {
  display: inline-flex; align-items: center; gap: 4px; max-width: 260px;
  padding: 0 6px; height: 20px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
  font-size: var(--text-2xs);
}
.ai-msg-attached-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--font-mono); }

.ai-msg-skill {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0 6px;
  height: 18px;
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-full);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-size: var(--text-2xs);
  line-height: 1;
}

.ai-msg-content {
  padding: 0;
  /* 与 app.css 的 .ai-md 保持同一个值：scoped 选择器带 [data-v] 优先级更高，
     这里写 13px 会把那边的 13.5px 盖掉，两边不同步就白改了。 */
  font-size: var(--text-base);
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
  padding: var(--space-4) var(--space-5);
  border-radius: var(--radius-md);
  white-space: pre-wrap;
}

.ai-msg-actions {
  display: flex; align-items: center; gap: var(--space-3);
  flex-wrap: wrap;
}

.ai-msg.user .ai-msg-actions { justify-content: flex-end; }

/* 计划模式的「开始执行」：和复制按钮同一行，但用强调色区分——
   它是这条回复上唯一的动作型按钮，跟「复制 / 存笔记」这类附属操作不是一类。 */
/* 用双类名提特异性：本条写在 .ai-copy 之前，同特异性下会被它的
   color: text-disabled 覆盖，靠书写顺序取胜太脆弱。 */
.ai-copy.ai-run-plan { color: var(--color-accent); font-weight: 500; }

.ai-copy.ai-run-plan:hover { color: var(--color-accent); background: var(--color-accent-bg); }

/* 继续生成：跟「开始执行」同款强调色，让被中断的回复旁这个操作看得见 */
.ai-copy.ai-continue { color: var(--color-accent); }
.ai-copy.ai-continue:hover { color: var(--color-accent); background: var(--color-accent-bg); }

.ai-copy {
  display: flex; align-items: center; gap: 3px;
  background: none; border: none; color: var(--color-text-disabled);
  font-size: var(--text-xs); cursor: pointer; padding: var(--space-1) var(--space-2); border-radius: var(--radius-xs);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-copy:hover { color: var(--color-text-muted); background: var(--color-bg-active); }

.ai-msg.user .ai-copy { color: var(--color-text-muted); }

.ai-msg.user .ai-copy:hover { color: var(--color-accent); background: var(--color-accent-bg); }

.ai-msg-stopped {
  font-size: var(--text-2xs); padding: 1px var(--space-3); border-radius: var(--radius-xs);
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border: 1px solid var(--color-warning-border);
  cursor: default;
}

.ai-msg-stat {
  font-size: var(--text-2xs);
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
  font-size: var(--text-md); max-width: 80%;
  /* 错误文案 + 重试按钮同一行；长错误换行，按钮不跟着被挤压 */
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}

.ai-error-text { flex: 1 1 auto; min-width: 0; word-break: break-word; }

.ai-retry {
  flex: 0 0 auto;
  padding: 3px var(--space-5); border-radius: var(--radius-sm);
  border: 1px solid var(--color-danger-border);
  background: transparent; color: var(--color-danger);
  font-size: var(--text-sm); font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast);
}

.ai-retry:hover { background: var(--color-danger-bg); }

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
  display: flex; flex-direction: column; align-items: center; gap: var(--space-5);
  color: var(--color-text-disabled); text-align: center;
}

.ai-need-key p, .ai-empty p { font-size: var(--text-md); margin: 0; }

.ai-jump-bottom {
  position: absolute;
  bottom: 96px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 20;
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px;
  border-radius: var(--radius-full);
  border: 1px solid var(--popover-border-color);
  background: var(--color-popover-bg);
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
  margin: var(--space-4) 0;
  border: none;
  border-radius: var(--radius-md);
  overflow: hidden;
  background: var(--color-code-bg);
}

.ai-md :deep(.md-code-bar) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-2) var(--space-3) var(--space-2) 10px;
  background: var(--color-code-bar-bg);
  border-bottom: none;
  font-size: var(--text-xs);
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

.ai-md :deep(.md-code-actions) { display: flex; align-items: center; gap: var(--space-1); }

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
  padding: 10px var(--space-5);
  overflow-x: auto;
  background: transparent;
  font-size: var(--text-sm);
  line-height: 1.6;
}

.ai-md :deep(code) {
  font-family: var(--font-mono); font-size: var(--text-sm);
  background: var(--color-code-inline-bg); padding: 1px 5px; border-radius: var(--radius-xs);
}

.ai-md :deep(pre code) { background: none; padding: 0; font-size: var(--text-sm); }

/* 表格容器只负责横向滚动，不画框：表格本身已经是「表头底色 + 行下边线」，
   外面再套一圈 border 就等于给数据加了个相框，比内容还抢眼。 */
.ai-md :deep(.md-table-wrap) {
  margin: var(--space-4) 0;
  overflow-x: auto;
  width: 100%;
}

/* 「导出 CSV」压到表格右上角，默认只是几个灰字：它是个偶尔才用的附属操作，
   不该占一条带底色的工具条。hover 时才浮出边框和强调色。 */
.ai-md :deep(.md-table-bar) {
  display: flex; justify-content: flex-end;
  padding: 0 var(--space-1) 3px;
}

.ai-md :deep(.md-table-export) {
  display: inline-flex; align-items: center; gap: 3px;
  font-size: var(--text-xs); padding: 1px 7px;
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

.ai-md :deep(.md-th-sortable[data-sort-dir="asc"]::after) { content: " ▲"; font-size: var(--text-2xs); }

.ai-md :deep(.md-th-sortable[data-sort-dir="desc"]::after) { content: " ▼"; font-size: var(--text-2xs); }

.ai-md :deep(hr) { border: none; border-top: 1px solid var(--color-border); margin: 10px 0; }

.ai-md :deep(img) { max-width: 100%; border-radius: var(--radius-sm); }

.ai-md :deep(.md-math-inline) { display: inline; }

.ai-md :deep(.md-math-inline .katex) { font-size: 1.05em; }

.ai-md :deep(.md-math-block) {
  display: block;
  margin: var(--space-4) 0;
  padding: var(--space-1) 0;
  overflow-x: auto;
  overflow-y: hidden;
}

.ai-md :deep(.md-math-block .katex-display) { margin: 0; }

.ai-md :deep(.md-math-raw) {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--color-text-secondary);
}

.ai-md :deep(.md-mermaid) {
  margin: var(--space-4) 0;
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
  padding: var(--space-2) var(--space-3) var(--space-2) 10px;
  background: var(--color-code-bar-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: var(--text-xs);
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

.ai-md :deep(.md-mermaid-acts) { display: flex; align-items: center; gap: var(--space-1); }

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
  padding: var(--space-2) 10px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-code-bar-bg);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.ai-msg.streaming :deep(.ai-md-block:last-child .md-mermaid-bar) { display: none; }

.ai-msg-imgs { display: flex; flex-wrap: wrap; gap: var(--space-4); }

.ai-msg.user .ai-msg-imgs { justify-content: flex-end; }

.ai-msg-img { width: 180px; max-width: 55%; border-radius: var(--radius-sm); overflow: hidden; border: 1px solid var(--color-border); }

.ai-msg-img-el { width: 100%; display: block; max-height: 220px; object-fit: cover; background: var(--color-bg-tertiary); }

.ai-msg-img-el.is-clickable { cursor: zoom-in; }

.ai-img-broken { height: 80px; display: flex; align-items: center; justify-content: center; font-size: var(--text-xl); color: var(--color-text-disabled); }

@keyframes ai-blink { 50% { opacity: 0; } }

/* 气泡尾部的单条费用：与 .ai-msg-stat 同属「一行安静的辅助信息」——
   无框、最弱一档颜色、允许换行（模型名 + 金额 + token 在窄窗口会折行）。
   只写在本 SFC 的 scoped 块里，不改全局 styles/*.css；
   WebView2 不支持 :has()/:is()/:container，这里也不依赖它们。 */
.ai-msg-cost {
  font-size: var(--text-2xs);
  color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
  line-height: 1.6;
  white-space: normal;
  word-break: break-word;
  cursor: default;
}
</style>
