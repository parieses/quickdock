<script setup lang="ts">
/**
 * 运行轨迹抽屉：从左侧滑出，展示当前会话这次执行的 run 树与每一步事件。
 *
 * 数据来自 `AIAgentRunTree(runId, afterSeq)`——它本来就存在（services/ai/agent_view.go），
 * 不需要新增绑定。runId 由父组件从最近一条工具记录的 run_id 取。
 *
 * 与消息下方那条 run 树（AgentRunTree.vue）的区别只在入口与信息密度：
 * 那个挂在「哪条消息派了活」下面，按需展开；这个常驻工具栏，随时能看到
 * 「现在跑到第几步」。两者共用 utils/aiRunTree.ts 的类型与分组逻辑。
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Activity, ChevronDown, ChevronRight, CircleStop, X } from '@lucide/vue'
import { AIAgentRunTree, AICancelAgentNode, AISessionTimeline } from '../../bindings/quickdock/services/ai/aiservice'
import { useDialogFocus } from '../composables/useDialogFocus'
import TraceStepDetail from './TraceStepDetail.vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { agentRoleLabel, toolTarget, toolVerb } from '../utils/aiTools'
import { compactPastedBlocks } from '../utils/aiPaste'
import type { AIToolCall } from '../types/ai'
import {
  childTasksFromNodes,
  childTasksOf,
  eventsToSteps,
  fmtElapsed,
  groupEventsByNode,
  type ChildTask,
  type RunEvent,
  type RunStep,
  type RunNode,
  type RunTree,
  type SessionRun,
  type SessionTimeline,
} from '../utils/aiRunTree'

const props = defineProps<{
  open: boolean
  /** 当前会话最近一次执行的 run id；空串表示还没有记录 */
  runId: string
  /** 当前会话 id：整段回放（WP12）要用它拉全部 run */
  convId: string
  /** 入口视图：trace=运行轨迹（默认）；children=整段会话的子任务列表 */
  view?: 'trace' | 'children'
}>()

const emit = defineEmits<{ (e: 'update:open', v: boolean): void }>()

const { t } = useI18n()

const dialogEl = ref<HTMLElement | null>(null)
useDialogFocus(() => props.open, dialogEl)

const tree = ref<RunTree | null>(null)
const loading = ref(false)
const failed = ref('')
const expanded = ref<Record<string, boolean>>({})

// —— 整段会话回放（WP12）——
//
// 与「本次运行」共用同一个抽屉：用户想看的都是「这一路发生了什么」，
// 差别只在范围。做成两个抽屉会让人不知道该点哪个。
const mode = ref<'run' | 'session'>('run')
const timeline = ref<SessionRun[]>([])
const sessionLoading = ref(false)
const sessionFailed = ref('')
const openRuns = ref<Record<string, boolean>>({})
// 用户一旦手动点过某条 run，就尊重他的选择；否则顶部子任务默认跟最新的 run 走。
const runToggleTouched = ref(false)

async function loadTimeline(silent = false) {
  if (!props.convId) {
    timeline.value = []
    return
  }
  // 轮询刷新必须「静默」：一旦把 sessionLoading 切成 true，模板会从列表
  // 换成加载提示、列表 DOM 被销毁重建，用户下拉到一半就被弹回顶部。
  if (!silent) sessionLoading.value = true
  sessionFailed.value = ''
  const want = props.convId
  try {
    const res = unwrap<SessionTimeline>(await AISessionTimeline(want, 0))
    // 期间换了会话：这次结果属于上一条，回填会把时间线闪回旧内容
    if (props.convId !== want) return
    timeline.value = res?.runs ?? []
    // 默认展开最近一次；用户手动选过 run 之后就不再自动切换。
    const last = timeline.value[timeline.value.length - 1]
    if (last && !runToggleTouched.value) openRuns.value[last.run.id] = true
  } catch (e) {
    if (props.convId !== want) return
    sessionFailed.value = getErrorMessage(e)
    logErr('ai:traceTimeline', e)
  } finally {
    if (!silent && props.convId === want) sessionLoading.value = false
  }
}

function switchMode(next: 'run' | 'session') {
  mode.value = next
  if (next === 'session' && !timeline.value.length) void loadTimeline()
}

function toggleRun(id: string) {
  runToggleTouched.value = true
  openRuns.value = { ...openRuns.value, [id]: !openRuns.value[id] }
}

// 没有 runId（比如刚聊完还没取到、或这条会话只有较早的记录）时，直接落到
// 「整段会话」：那个视图只需要 convId，能看到的东西永远不会比空抽屉少。
watch(
  () => [props.open, props.runId] as const,
  ([open, runId]) => {
    if (open && !runId) {
      mode.value = 'session'
      if (!timeline.value.length) void loadTimeline()
    }
  },
  { immediate: true },
)

// 切会话时清空折叠状态：否则上一条对话展开的 run / 子任务 id 会被带到
// 新对话里（id 不会撞，但列表一直保持收起，看起来像「新对话没有轨迹」）。
watch(
  () => props.convId,
  () => {
    timeline.value = []
    openRuns.value = {}
    childOpen.value = {}
    runToggleTouched.value = false
    sessionFailed.value = ''
    if (props.open && props.view === 'children') void loadTimeline()
  },
)

// 同一个抽屉兼作「轨迹」与「子任务」两个入口：切换入口时按新视图重拉数据。
watch(
  () => props.view,
  (view) => {
    if (!props.open) return
    if (view === 'children') void loadTimeline()
    else void load()
  },
)

/** 回放里每条 run 的事件按顺序排（库已按 seq 返回，这里只做防御性排序）。 */
function runEvents(r: SessionRun): RunEvent[] {
  return [...(r.events ?? [])].sort((a, b) => a.seq - b.seq)
}

/** 事件按节点分组，供每个节点展开时取用。 */
const eventsByNode = computed(() => groupEventsByNode(tree.value?.events ?? []))

/** 每个节点的事件聚合成时间线步骤；顺便拿到被过滤的元数据条数。 */
const stepsByNode = computed(() => {
  const out = new Map<string, { steps: RunStep[]; hidden: number }>()
  for (const [nodeId, events] of eventsByNode.value) {
    out.set(nodeId, eventsToSteps(events))
  }
  return out
})

/** 整段会话里每条 run 的步骤（事件不按节点分，直接全量聚合）。 */
function stepsOfRun(r: SessionRun) {
  return eventsToSteps(runEvents(r))
}

/** 顶部子任务列表：点开后看这个子 agent 自己的步骤（只取它的 node_id）。 */
const childOpen = ref<Record<string, boolean>>({})
function toggleChild(id: string) {
  childOpen.value = { ...childOpen.value, [id]: !childOpen.value[id] }
}

/**
 * 抽屉顶部那一份子任务列表，两种模式共用：
 *   - 本次运行：这棵 run 树里的子节点；
 *   - 整段会话：当前展开的那条 run（没展开就用最近一次）——
 *     派活发生在哪条 run，顶部就列哪条的子任务，不会混进别的任务。
 */
const topChildModel = computed(() => {
  // 弹框顶部那一份永远是「当前对话派出去的所有子任务」，不跟着某条 run 走：
  // 用户刚才抱怨「派了 3 个只显示 1 个」——那 3 个可能落在 3 条 run 上。
  // 事件合并后仍能按 node_id 筛出每个子 agent 自己的步骤时间线。
  const tasks: ChildTask[] = []
  const events: RunEvent[] = []
  for (const r of timeline.value) {
    tasks.push(...childTasksOf(r))
    events.push(...(r.events ?? []))
  }
  if (tasks.length) return { tasks: tasks.sort((a, b) => b.tokens - a.tokens), events }
  // timeline 还没回来（首次打开的一两帧）时用当前 run 的树兜底，避免空白闪一下。
  if (tree.value) {
    return { tasks: childTasksFromNodes(tree.value.nodes), events: tree.value.events }
  }
  return null
})

/**
 * 顶部汇总：一眼看清「几个成了、几个失败、几个还在跑」，不用逐条数徽标。
 * 口径与 childRunning/statusText 一致：pending / 缺省归「进行中」。
 * 数据全来自现有 ChildTask.status，不需要后端改动。
 */
const childSummary = computed(() => {
  const tasks = topChildModel.value?.tasks ?? []
  let done = 0, failed = 0, canceled = 0, running = 0
  let tokens = 0
  let elapsed = 0
  for (const c of tasks) {
    if (c.status === 'done') done++
    else if (c.status === 'failed') failed++
    else if (c.status === 'canceled') canceled++
    else running++
    tokens += c.tokens
    elapsed = Math.max(elapsed, c.elapsedMs) // 并发任务：用时取最长那条，不是求和
  }
  return { total: tasks.length, done, failed, canceled, running, tokens, elapsed }
})

/**
 * 每个子 agent 的步骤预先算好，避免 v-for 里重复 filter + eventsToSteps。
 *
 * 原来三处模板（步骤列表 / 空态 / 隐藏计数）各对每条事件全量 filter 一次，
 * 会话回放几十 run、上百事件时是 O(节点数 × 事件数)。这里一次扫描按 node_id
 * 分桶，复杂度降到 O(事件数)，topChildModel 变化时重算。
 */
const childStepsByNode = computed<Map<string, ReturnType<typeof eventsToSteps>>>(() => {
  const buckets = new Map<string, RunEvent[]>()
  for (const e of topChildModel.value?.events ?? []) {
    if (!e.node_id) continue
    let list = buckets.get(e.node_id)
    if (!list) buckets.set(e.node_id, list = [])
    list.push(e)
  }
  const out = new Map<string, ReturnType<typeof eventsToSteps>>()
  for (const [id, events] of buckets) out.set(id, eventsToSteps(events))
  return out
})

function stepsForChild(id: string) {
  return childStepsByNode.value.get(id) ?? { steps: [], hidden: 0 }
}

/** 步骤详情是否展开（key 用 step.key，跨 run 不冲突）。 */
const stepOpen = ref<Record<string, boolean>>({})
function toggleStep(key: string) {
  stepOpen.value = { ...stepOpen.value, [key]: !stepOpen.value[key] }
}

// —— 单个子任务的停止（见后端 AICancelAgentNode）——
//
// 一次编排常常同时跑五六个，跑偏的通常只有一两个。此前用户只能「等它跑完」或
// 「整条对话停掉」——后者会把已经跑了大半的其它子任务一起杀掉，白花钱。
const stopping = ref<Record<string, boolean>>({})

/** 还在跑（缺省也算 running：刚建出来的节点还没写状态）。 */
function childRunning(status?: string): boolean {
  return !status || status === 'running'
}

async function stopChild(id: string) {
  if (stopping.value[id]) return
  stopping.value = { ...stopping.value, [id]: true }
  try {
    unwrap(await AICancelAgentNode(id))
  } catch (e) {
    // 「这个子任务已经结束了」是最常见的失败：用户点得比它收尾慢半拍。
    // 那不是错误，把状态刷新出来就够了。
    logErr('ai:traceStopChild', e)
  } finally {
    stopping.value = { ...stopping.value, [id]: false }
    // 取消是异步生效的（子 agent 在下一轮边界才会看到 ctx 已取消），
    // 立刻拉一次能看到「已取消」，不必等下一个 1 秒轮询。
    void loadTimeline(true)
    void load(true)
  }
}

/** 状态徽标：库里存的是英文 run/node 状态，界面一律走 i18n；pending / 缺省按「进行中」显示。 */
function statusText(s?: string): string {
  if (s === 'done') return t('aiRunStatusDone')
  if (s === 'failed') return t('aiRunStatusFailed')
  if (s === 'canceled') return t('aiRunStatusCanceled')
  return t('aiRunStatusRunning')
}

/** 步骤标题：工具用人话动词，思考用固定文案，结论用事件名。 */
function stepTitle(step: RunStep): string {
  if (step.kind === 'think') return t('aiThought')
  if (step.kind === 'tool') return toolVerb(step.name, t)
  if (step.kind === 'compact') return step.compactKind === 'loop' ? t('aiCompactFolded') : t('aiCompacted')
  return step.name
}

/** 单行摘要里的粘贴块压成标记（全文仍在任务里，展开详情可点 chip 看）。 */
function traceText(task: string): string {
  return compactPastedBlocks(task || '', (n) => t('aiTracePastedBlock', { n }))
}

/** 步骤的操作目标：复用气泡卡片的 shortPath 口径，绝对路径压成末两段。 */
function stepTarget(step: RunStep): string {
  if (step.kind !== 'tool') return ''
  // name 必须一起传：toolTarget 对 spawn_agent 有专门的摘要口径（role · 任务首行），
  // 少了 name 就退化成「挑第一个字符串值」，派活那一步又变回一串参数原文。
  return toolTarget({ name: step.name, args: step.args } as AIToolCall)
}

/** 步骤右侧的短摘要：耗时 / 增删行 / 字数，一眼看出这一步的「分量」。 */
function stepBadge(step: RunStep): string {
  const bits: string[] = []
  if (step.kind === 'think') return t('aiThoughtChars', { n: step.reasoningChars })
  if (step.kind === 'compact') return `${fmtTokens(step.compactBefore)} → ${fmtTokens(step.compactAfter)}`
  if (step.ms > 0) bits.push(fmtElapsed(step.ms))
  if (step.added || step.removed) bits.push(t('aiTraceEventLines', { added: step.added, removed: step.removed }))
  if (step.kind === 'done') bits.push(t('aiTraceEventDone'))
  return bits.join(' · ')
}

/** 步骤能否展开看详情。 */
function stepHasDetail(step: RunStep): boolean {
  return !!(step.args || step.output || step.result || step.diff || step.error || step.text)
}

/** 节点按 parent_id 算缩进层级（并发创建时 seq 不保证父在前）。 */
const nodes = computed<Array<RunNode & { depth: number }>>(() => {
  const list = tree.value?.nodes ?? []
  const byId = new Map(list.map((n) => [n.id, n]))
  const depths = new Map<string, number>()
  const walk = (id: string, guard: Set<string>): number => {
    const cached = depths.get(id)
    if (cached !== undefined) return cached
    const n = byId.get(id)
    // 环引用（数据损坏）时立即收敛，别把页面转死。
    if (!n || guard.has(id)) return 0
    guard.add(id)
    const parent = n.parent_id ? byId.get(n.parent_id) : undefined
    const d = parent ? walk(parent.id, guard) + 1 : 0
    depths.set(id, d)
    return d
  }
  return list.map((n) => ({ ...n, depth: walk(n.id, new Set()) }))
})

async function load(silent = false) {
  if (!props.runId) {
    tree.value = null
    failed.value = ''
    return
  }
  // 轮询时不要亮「加载中」：抽屉一直闪一下就没人看得清步骤在长。
  if (!silent) loading.value = true
  failed.value = ''
  const want = props.runId
  try {
    const next = unwrap<RunTree>(await AIAgentRunTree(want, 0))
    // 快速切换 run 或轮询与新请求并发时，旧响应不能覆盖当前树
    if (props.runId !== want) return
    tree.value = next
  } catch (e) {
    if (props.runId !== want) return
    failed.value = getErrorMessage(e)
    logErr('AiTraceDrawer.load', e)
    tree.value = null
  } finally {
    if (!silent && props.runId === want) loading.value = false
  }
}

// 轮询：抽屉开着且这次 run 还在跑时，每秒补拉一次。
//
// 为什么必须有：原来只在打开瞬间拉一次，而 run 的 status 在跑完之前一直是
// running——用户运行中打开抽屉，看到的步骤数永远停在打开那一刻，必须关掉重开。
// `AIAgentRunTree` 早就留了 afterSeq 增量参数（agent_view.go 的注释也写着
// 「前端 500ms 轮询用」），只是前端一直没接。
let pollTimer: ReturnType<typeof setInterval> | null = null

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

/**
 * 还要继续轮询吗。
 *
 * 「数据还没到」也算要继续：timer 刚启动时 tree / timeline 都为空，
 * 若把空当成终态会立刻停表，run 视图就永远停在加载态。
 * 只有拿到了明确的终态、且当前视图的数据源已经加载，才停。
 */
function shouldKeepPolling(): boolean {
  if (props.view === 'children' || mode.value === 'session') {
    // 这个视图只看 timeline：没加载或最新一条还在跑就继续。
    const last = timeline.value[timeline.value.length - 1]
    if (!last) return true
    return last.run.status === 'running'
  }
  // run 视图看本次 run 树：没加载到、或树里还没有 run 就继续等。
  if (!tree.value?.run) return true
  return tree.value.run.status === 'running'
}

function startPolling() {
  stopPolling()
  pollTimer = setInterval(() => {
    if (!props.open) {
      stopPolling()
      return
    }
    // 每个 tick 只拉一次 timeline：session 子和顶部子任务共用这一份数据，
    // 原来 session 模式一秒拉两次（顶部一次 + 分支一次），pure IO 浪费。
    void loadTimeline(true)
    // 当前视图所需的数据：children 视图只用 timeline，run 视图还要拉 run 树。
    if (props.view !== 'children' && mode.value === 'run') void load(true)
    // 当前视图的数据源拿到终态就停表：跑完的 run 不会再变，继续每秒拉只是白花 IO。
    // run 终态现在由后端统一写库（finalRunStatus），这里能真正收到 done。
    if (!shouldKeepPolling()) stopPolling()
  }, 1000)
}

// 打开时拉一次；运行中 runId 可能变化（新的一轮换了 run），跟着重拉。
watch(
  () => [props.open, props.runId] as const,
  ([open]) => {
    if (open) {
      // 顶部子任务统计需要全量 timeline：即使从「轨迹」入口进来也要拉。
      void loadTimeline()
      if (props.view !== 'children') void load()
      startPolling()
    } else {
      stopPolling()
    }
  },
  { immediate: true },
)

function close() {
  emit('update:open', false)
}

function toggleNode(id: string) {
  expanded.value = { ...expanded.value, [id]: !expanded.value[id] }
}

function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  return n >= 1000 ? (n / 1000).toFixed(1) + 'k' : String(n)
}

/** Esc 关闭；监听只在打开期间挂，避免常驻一个文档级监听。 */
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && props.open) {
    e.stopPropagation()
    close()
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) window.document.addEventListener('keydown', onKeydown, true)
    else window.document.removeEventListener('keydown', onKeydown, true)
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  window.document.removeEventListener('keydown', onKeydown, true)
  stopPolling()
})
</script>

<template>
  <Teleport to="body">
    <template v-if="props.open">
      <div class="trace-backdrop" @click="close" />
      <aside ref="dialogEl" class="trace-drawer" role="dialog" aria-modal="true" tabindex="-1" :aria-label="t(props.view === 'children' ? 'aiSubtasksTitle' : 'aiTraceTitle')">
        <header class="trace-head">
          <Activity :size="14" class="trace-head-ico" />
          <span class="trace-title">{{ t(props.view === 'children' ? 'aiSubtasksTitle' : 'aiTraceTitle') }}</span>
          <!-- 范围切换（WP12）：一次运行 vs 整段会话。子任务入口只看整段会话，不需要切。 -->
          <div v-if="props.view !== 'children'" class="trace-modes">
            <button
              class="trace-mode"
              :class="{ 'is-on': mode === 'run' }"
              @click="switchMode('run')"
            >
              {{ t('aiTraceModeRun') }}
            </button>
            <button
              class="trace-mode"
              :class="{ 'is-on': mode === 'session' }"
              @click="switchMode('session')"
            >
              {{ t('aiTraceModeSession') }}
            </button>
          </div>
          <button class="trace-close" :title="t('aiTraceClose')" @click="close">
            <X :size="14" />
          </button>
        </header>

        <!-- 顶部子任务（对标 DSH）：两种模式共用一块，列的是「当前这次执行」
             派出去的子 agent。点开看完整任务、目录/模型、产出和它自己的步骤。
             「子任务」入口下列的是整段会话的全部子 agent。 -->
        <section v-if="topChildModel && topChildModel.tasks.length" class="trace-top-children" :class="{ 'is-children-view': props.view === 'children' }">
          <!-- 汇总条：一眼看清成/败/跑，不逐条数徽标；进度条按状态分段。 -->
          <div class="trace-summary">
            <div class="trace-summary-top">
              <span class="trace-summary-title">{{ t('aiTraceSessionChildren', { n: childSummary.total }) }}</span>
              <span class="trace-summary-stats">
                <span class="is-done">{{ childSummary.done }} {{ t('aiRunStatusDone') }}</span>
                <span v-if="childSummary.failed" class="is-failed">{{ childSummary.failed }} {{ t('aiRunStatusFailed') }}</span>
                <span v-if="childSummary.canceled" class="is-canceled">{{ childSummary.canceled }} {{ t('aiRunStatusCanceled') }}</span>
                <span v-if="childSummary.running" class="is-running">{{ childSummary.running }} {{ t('aiRunStatusRunning') }}</span>
              </span>
            </div>
            <div class="trace-summary-bar" :title="`${fmtTokens(childSummary.tokens)} tok · ${fmtElapsed(childSummary.elapsed)}`">
              <span v-if="childSummary.done" class="seg is-done" :style="{ width: (childSummary.done / childSummary.total * 100) + '%' }" />
              <span v-if="childSummary.failed" class="seg is-failed" :style="{ width: (childSummary.failed / childSummary.total * 100) + '%' }" />
              <span v-if="childSummary.canceled" class="seg is-canceled" :style="{ width: (childSummary.canceled / childSummary.total * 100) + '%' }" />
              <span v-if="childSummary.running" class="seg is-running" :style="{ width: (childSummary.running / childSummary.total * 100) + '%' }" />
            </div>
          </div>
          <ul class="trace-child-tasks">
            <li v-for="c in topChildModel.tasks" :key="c.id" class="trace-child-task">
              <div v-key-click class="trace-child-task-head" @click="toggleChild(c.id)">
                <component :is="childOpen[c.id] ? ChevronDown : ChevronRight" :size="12" class="trace-child-task-arrow" />
                <!-- 状态单一信源：一枚色点表达状态，徽标补文字，不再左侧色条 + 圆点 + 文字三处重复。 -->
                <span class="trace-child-dot" :class="`is-${c.status || 'running'}`" />
                <span class="trace-child-task-task" :title="traceText(c.task)">{{ traceText(c.task) }}</span>
                <span class="trace-child-badge" :class="`is-${c.status || 'running'}`">{{ statusText(c.status) }}</span>
                <!-- 只停这一个：整行可点展开，所以必须 stop 掉冒泡。 -->
                <button
                  v-if="childRunning(c.status)"
                  class="trace-child-stop"
                  :title="t('aiTraceChildStop')"
                  :disabled="!!stopping[c.id]"
                  @click.stop.prevent="stopChild(c.id)"
                >
                  <CircleStop :size="12" />
                </button>
              </div>
              <!-- 副行放次要信息：role · token · 耗时。主行不再塞 7 个元素互相挤压。 -->
              <div class="trace-child-task-sub">
                <span class="trace-child-task-role">{{ c.role }}</span>
                <span v-if="agentRoleLabel(c.role, t)" class="trace-role-label">{{ agentRoleLabel(c.role, t) }}</span>
                <span v-if="c.tokens" class="trace-sub-metric">{{ fmtTokens(c.tokens) }} tok</span>
                <span v-if="c.elapsedMs" class="trace-sub-metric">{{ fmtElapsed(c.elapsedMs) }}</span>
              </div>
              <div v-if="childOpen[c.id]" class="trace-child-task-detail">
                <!-- 完整任务里可能夹着粘贴的长文：压成一行标记（抽屉本身是 dialog，
                     不再嵌弹窗；全文在消息气泡里可点开看）。 -->
                <div class="trace-child-task-full">{{ traceText(c.task) }}</div>
                <div class="trace-child-task-meta">
                  <span v-if="c.node.cwd">{{ t('aiTraceChildCwd') }}: {{ c.node.cwd }}</span>
                  <span v-if="c.node.model">{{ t('aiTraceChildModel') }}: {{ c.node.model }}</span>
                  <span>{{ t('aiTraceChildTokens', { input: fmtTokens(c.node.prompt_tokens), output: fmtTokens(c.node.completion_tokens) }) }}</span>
                  <span v-if="c.elapsedMs">{{ fmtElapsed(c.elapsedMs) }}</span>
                </div>
                <template v-if="c.node.result">
                  <div class="trace-child-task-result-label">{{ t('aiTraceChildResult') }}</div>
                  <pre class="trace-child-task-result">{{ traceText(c.node.result) }}</pre>
                </template>
                <ol class="trace-timeline trace-child-task-steps">
                  <li
                    v-for="(step, si) in stepsForChild(c.id).steps"
                    :key="step.key"
                    class="trace-step"
                    :class="[`is-${step.kind}`, { 'is-err': !step.ok }]"
                  >
                    <span class="trace-step-idx">{{ si + 1 }}</span>
                    <div v-key-click class="trace-step-main" :class="{ 'is-clickable': stepHasDetail(step) }" @click="stepHasDetail(step) && toggleStep(step.key)">
                      <div class="trace-step-line">
                        <span class="trace-step-title">{{ stepTitle(step) }}</span>
                        <span v-if="step.kind === 'tool' && stepTarget(step)" class="trace-step-target" :title="step.path || traceText(step.args)">
                          {{ stepTarget(step) }}
                        </span>
                        <span v-if="stepBadge(step)" class="trace-step-badge">{{ stepBadge(step) }}</span>
                        <component
                          :is="stepOpen[step.key] ? ChevronDown : ChevronRight"
                          v-if="stepHasDetail(step)"
                          :size="11"
                          class="trace-node-caret"
                        />
                      </div>
                      <div v-if="!step.ok" class="trace-step-err">{{ step.error || t('aiTraceEventFail') }}</div>
                      <TraceStepDetail v-if="stepOpen[step.key] && stepHasDetail(step)" :step="step" />
                    </div>
                  </li>
                  <li v-if="!stepsForChild(c.id).steps.length" class="trace-event is-empty">
                    {{ t('aiTraceNodeNoEvents') }}
                  </li>
                  <li v-if="stepsForChild(c.id).hidden > 0" class="trace-step-hidden">
                    {{ t('aiTraceHiddenMeta', { n: stepsForChild(c.id).hidden }) }}
                  </li>
                </ol>
              </div>
            </li>
          </ul>
        </section>

        <!-- 子任务入口：没有派过活 / 还在拉数据时给个明确说法，别只剩空白抽屉。 -->
        <div v-if="props.view === 'children' && (!topChildModel || !topChildModel.tasks.length)" class="trace-body">
          <p v-if="sessionLoading" class="trace-hint">{{ t('aiTraceLoading') }}</p>
          <p v-else-if="sessionFailed" class="trace-hint is-bad">{{ sessionFailed }}</p>
          <p v-else-if="!topChildModel || !topChildModel.tasks.length" class="trace-hint">{{ t('aiSubtasksEmpty') }}</p>
        </div>

        <div v-if="props.view !== 'children' && mode === 'session'" class="trace-body">
          <p v-if="sessionLoading" class="trace-hint">{{ t('aiTraceLoading') }}</p>
          <p v-else-if="sessionFailed" class="trace-hint is-bad">{{ sessionFailed }}</p>
          <p v-else-if="!timeline.length" class="trace-hint">{{ t('aiTraceSessionEmpty') }}</p>
          <ul v-else class="trace-runs">
            <li v-for="r in timeline" :key="r.run.id" class="trace-run-item">
              <div v-key-click class="trace-run-head" @click="toggleRun(r.run.id)">
                <component :is="openRuns[r.run.id] ? ChevronDown : ChevronRight" :size="11" class="trace-node-caret" />
                <span class="trace-run-goal2">{{ r.run.goal || t('aiTraceNoGoal') }}</span>
                <span class="trace-node-status" :class="`is-${r.run.status}`">{{ statusText(r.run.status) }}</span>
              </div>
              <div class="trace-run-meta trace-run-meta2">
                <span>{{ t('aiTraceSteps', { n: r.nodes.length }) }}</span>
                <span class="trace-sep">·</span>
                <span>{{ fmtTokens(r.run.used_tokens) }} tok</span>
                <template v-if="r.children">
                  <span class="trace-sep">·</span>
                  <span>{{ t('aiTraceSessionChildren', { n: r.children }) }}</span>
                </template>
              </div>
              <template v-if="openRuns[r.run.id]">
              <ol class="trace-timeline">
                <li
                  v-for="(step, si) in stepsOfRun(r).steps"
                  :key="step.key"
                  class="trace-step"
                  :class="[`is-${step.kind}`, { 'is-err': !step.ok }]"
                >
                  <span class="trace-step-idx">{{ si + 1 }}</span>
                  <div v-key-click class="trace-step-main" :class="{ 'is-clickable': stepHasDetail(step) }" @click="stepHasDetail(step) && toggleStep(step.key)">
                    <div class="trace-step-line">
                      <span class="trace-step-title">{{ stepTitle(step) }}</span>
                      <span v-if="step.kind === 'tool' && stepTarget(step)" class="trace-step-target" :title="step.path || traceText(step.args)">
                        {{ stepTarget(step) }}
                      </span>
                      <span v-if="stepBadge(step)" class="trace-step-badge">{{ stepBadge(step) }}</span>
                      <component
                        :is="stepOpen[step.key] ? ChevronDown : ChevronRight"
                        v-if="stepHasDetail(step)"
                        :size="11"
                        class="trace-node-caret"
                      />
                    </div>
                    <div v-if="!step.ok" class="trace-step-err">{{ step.error || t('aiTraceEventFail') }}</div>
                    <!-- 与另外两条时间线共用分段详情；此前这里五段裸 <pre>，
                         粘贴长文会把整个抽屉糊成一片。 -->
                    <TraceStepDetail v-if="stepOpen[step.key] && stepHasDetail(step)" :step="step" />
                  </div>
                </li>
                <li v-if="!stepsOfRun(r).steps.length" class="trace-event is-empty">
                  {{ t('aiTraceNodeNoEvents') }}
                </li>
              </ol>
              </template>
            </li>
          </ul>
        </div>

        <div v-else-if="props.view !== 'children'" class="trace-body">
          <p v-if="loading" class="trace-hint">{{ t('aiTraceLoading') }}</p>
          <p v-else-if="failed" class="trace-hint is-bad">{{ failed }}</p>
          <p v-else-if="!props.runId" class="trace-hint">{{ t('aiTraceNoRun') }}</p>
          <p v-else-if="!tree || (!tree.nodes.length && !tree.events.length)" class="trace-hint">
            {{ t('aiTraceEmpty') }}
          </p>

          <template v-else>
            <div v-if="tree.run" class="trace-run">
              <div class="trace-run-goal">{{ tree.run.goal || t('aiTraceNoGoal') }}</div>
              <div class="trace-run-meta">
                <span>{{ statusText(tree.run.status) }}</span>
                <span class="trace-sep">·</span>
                <span>{{ t('aiTraceSteps', { n: tree.nodes.length }) }}</span>
                <span class="trace-sep">·</span>
                <span>{{ fmtTokens(tree.run.used_tokens) }} / {{ fmtTokens(tree.run.budget_tokens) }} tok</span>
              </div>
            </div>

            <ul class="trace-nodes">
              <li
                v-for="n in nodes"
                :key="n.id"
                class="trace-node"
                :class="{ 'is-child': n.depth > 0 }"
                :style="{ paddingLeft: n.depth * 12 + 'px' }"
              >
                <div v-key-click class="trace-node-head" @click="toggleNode(n.id)">
                  <component :is="expanded[n.id] ? ChevronDown : ChevronRight" :size="11" class="trace-node-caret" />
                  <span class="trace-node-role">{{ n.role }}</span>
                  <span v-if="agentRoleLabel(n.role, t)" class="trace-role-label">{{ agentRoleLabel(n.role, t) }}</span>
                  <span class="trace-node-task">{{ traceText(n.task) }}</span>
                  <span class="trace-node-status" :class="`is-${n.status}`">{{ statusText(n.status) }}</span>
                </div>
                <ol v-if="expanded[n.id]" class="trace-timeline">
                  <li
                    v-for="(step, si) in stepsByNode.get(n.id)?.steps ?? []"
                    :key="step.key"
                    class="trace-step"
                    :class="[`is-${step.kind}`, { 'is-err': !step.ok }]"
                  >
                    <span class="trace-step-idx">{{ si + 1 }}</span>
                    <div v-key-click class="trace-step-main" :class="{ 'is-clickable': stepHasDetail(step) }" @click="stepHasDetail(step) && toggleStep(step.key)">
                      <div class="trace-step-line">
                        <span class="trace-step-title">{{ stepTitle(step) }}</span>
                        <span v-if="step.kind === 'tool' && stepTarget(step)" class="trace-step-target" :title="step.path || traceText(step.args)">
                          {{ stepTarget(step) }}
                        </span>
                        <span v-if="stepBadge(step)" class="trace-step-badge">{{ stepBadge(step) }}</span>
                        <component
                          :is="stepOpen[step.key] ? ChevronDown : ChevronRight"
                          v-if="stepHasDetail(step)"
                          :size="11"
                          class="trace-node-caret"
                        />
                      </div>
                      <div v-if="!step.ok" class="trace-step-err">{{ step.error || t('aiTraceEventFail') }}</div>
                      <TraceStepDetail v-if="stepOpen[step.key] && stepHasDetail(step)" :step="step" />
                    </div>
                  </li>
                  <li v-if="!(stepsByNode.get(n.id)?.steps ?? []).length" class="trace-event is-empty">
                    {{ t('aiTraceNodeNoEvents') }}
                  </li>
                  <li v-if="(stepsByNode.get(n.id)?.hidden ?? 0) > 0" class="trace-step-hidden">
                    {{ t('aiTraceHiddenMeta', { n: stepsByNode.get(n.id)?.hidden }) }}
                  </li>
                </ol>
              </li>
            </ul>
          </template>
        </div>
      </aside>
    </template>
  </Teleport>
</template>

<style scoped>
.trace-backdrop {
  position: fixed;
  inset: 0;
  background: var(--color-bg-overlay);
  z-index: var(--z-modal);
}

/* 居中弹框（对齐 DSH 的子任务面板）：不再从左侧滑出。列表项右侧那列
   token / 耗时本来就是为窄卡片排的，放在中间和放在左边一样好读，
   但居中后视觉上明确是「一串独立任务的清单」，而不是被某个侧栏拥有。 */
.trace-drawer {
  position: fixed;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  width: 480px;
  max-width: 92vw;
  max-height: min(720px, 86vh);
  display: flex;
  flex-direction: column;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  z-index: calc(var(--z-modal) + 1);
  overflow: hidden;
}

.trace-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-4) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.trace-head-ico { color: var(--color-accent-ink); flex-shrink: 0; }

/* 标题吃掉全部剩余宽度，把右边的范围切换和关闭按钮顶到边上。
   别用 .trace-modes 的 margin-left:auto 干这事：子任务视图不渲染范围切换，
   那样关闭按钮会紧跟在标题后面，看起来跑到左边去了。 */
.trace-title {
  font-size: var(--text-md);
  color: var(--color-text-primary);
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 范围切换（WP12）：贴在标题右侧，不占一整行——抽屉很窄，多一行会把
   下面的事件列表挤掉大半屏。 */
.trace-modes {
  display: flex;
  flex-shrink: 0;
  gap: var(--space-1);
  padding: 2px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-hover);
}

.trace-mode {
  border: none;
  background: transparent;
  padding: 2px var(--space-2);
  border-radius: var(--radius-sm);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  cursor: pointer;
}

.trace-mode.is-on {
  background: var(--color-popover-bg);
  color: var(--color-text-primary);
}

.trace-runs {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.trace-run-item {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  padding: var(--space-2);
}

.trace-run-head {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  cursor: pointer;
}

.trace-run-goal2 {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--text-sm);
  color: var(--color-text-primary);
}

.trace-run-meta2 {
  margin: var(--space-1) 0 0 calc(11px + var(--space-1));
}

.trace-close {
  display: flex;
  flex-shrink: 0;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.trace-close:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.trace-body {
  flex: 1;
  overflow-y: auto;
  padding: var(--space-3) var(--space-4) var(--space-4);
}

.trace-hint {
  margin: 0;
  padding: var(--space-4) 0;
  font-size: var(--text-sm);
  color: var(--color-text-muted);
  line-height: 1.6;
}

.trace-hint.is-bad { color: var(--color-danger-ink); }

.trace-run {
  padding: var(--space-3);
  margin-bottom: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
}

.trace-run-goal {
  font-size: var(--text-sm);
  color: var(--color-text-primary);
  line-height: 1.5;
  word-break: break-word;
}

.trace-run-meta {
  margin-top: var(--space-2);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.trace-sep { margin: 0 var(--space-1); }

.trace-nodes { list-style: none; margin: 0; padding: 0; }

.trace-node { margin-bottom: var(--space-2); position: relative; }
/* 子节点左侧画一段「└」引导线，配合 depth 缩进表达父子归属。
   纯 padding 缩进只能看出「更深」，看不出「是谁的孩子」。 */
.trace-node.is-child::before {
  content: '';
  position: absolute;
  left: 4px;
  top: -4px;
  bottom: 50%;
  width: 6px;
  border-left: 1px solid var(--color-border-light);
  border-bottom: 1px solid var(--color-border-light);
  border-bottom-left-radius: var(--radius-sm);
  pointer-events: none;
}

.trace-node-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2);
  border-radius: var(--radius-sm);
  cursor: pointer;
  position: relative;
}

.trace-node-head:hover { background: var(--color-bg-hover); }

.trace-node-caret { color: var(--color-text-muted); flex-shrink: 0; }

.trace-node-role {
  padding: 0 var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-accent-bg);
  color: var(--color-accent-text);
  font-size: var(--text-2xs);
  flex-shrink: 0;
}

.trace-node-task {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
}

.trace-node-status {
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
  flex-shrink: 0;
}

/* 与气泡内 run 树（panels.css .qd-run-state）同一套口径：
   pending 中性、running 蓝、canceled 橙，别让运行中/已停止都落灰。 */
.trace-node-status.is-running { color: var(--color-accent-ink); }
.trace-node-status.is-done { color: var(--color-success-ink); }
.trace-node-status.is-failed { color: var(--color-danger-ink); }
.trace-node-status.is-canceled { color: var(--color-warning-ink); }

/* 抽屉顶部的子任务区：固定在头部下方，不随下面的 run 列表滚动。
   这里要限高！一轮派十几个子 agent 时，整列铺开会把下面的「运行轨迹」挤出
   弹窗——用户点开轨迹是想逐条看步骤的，结果只能看到子任务名单。限高后自己
   内部滚，两种视图都保住下方的轨迹区。 */
.trace-top-children {
  padding: var(--space-2) var(--space-4) 0;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  max-height: min(240px, 34vh);
  overflow-y: auto;
}
/* 「子任务」入口下这是唯一的内容区：整段会话的子任务可能很多，
   让它自己吃掉剩余高度并滚动，不能像轨迹模式那样只占一小条。 */
.trace-top-children.is-children-view {
  flex: 1;
  min-height: 0;
  max-height: none;
  border-bottom: 0;
  overflow-y: auto;
}
/* 汇总条：标题 + 状态计数在同一行，下面一条分段进度条。
   并发子任务「用时」取最长那条，与 childSummary.elapsed 一致。 */
.trace-summary { padding-top: var(--space-1); }
.trace-summary-top {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-2);
}
.trace-summary-title { font-size: var(--text-2xs); color: var(--color-text-muted); }
.trace-summary-stats {
  display: inline-flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  font-size: var(--text-2xs);
  font-variant-numeric: tabular-nums;
  color: var(--color-text-muted);
}
.trace-summary-stats .is-done { color: var(--color-success-ink); }
.trace-summary-stats .is-failed { color: var(--color-danger-ink); }
.trace-summary-stats .is-canceled { color: var(--color-warning-ink); }
.trace-summary-stats .is-running { color: var(--color-accent-ink); }
.trace-summary-bar {
  display: flex;
  height: 4px;
  margin-top: var(--space-2);
  border-radius: var(--radius-full);
  background: var(--color-bg-hover);
  overflow: hidden;
}
.trace-summary-bar .seg { height: 100%; }
.trace-summary-bar .seg.is-done { background: var(--color-success); }
.trace-summary-bar .seg.is-failed { background: var(--color-danger); }
.trace-summary-bar .seg.is-canceled { background: var(--color-warning); }
.trace-summary-bar .seg.is-running { background: var(--color-accent); }

/* 子任务详情（对标 DSH 轨迹顶部那块，一个子 agent 一行）。 */
.trace-child-tasks {
  list-style: none;
  margin: var(--space-2) 0;
  padding: 0;
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-sm);
  overflow: hidden;
}
.trace-child-task + .trace-child-task { border-top: 1px solid var(--color-border-light); }
.trace-child-task-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-2) 0;
  font-size: var(--text-xs);
  cursor: pointer;
}
.trace-child-task-head:hover { background: var(--color-bg-hover); }
.trace-child-task-arrow {
  flex-shrink: 0;
  color: var(--color-text-muted);
  transition: transform var(--transition-fast);
}
/* 状态色点：单一信源。徽标只补文字，不再重复上色。 */
.trace-child-dot {
  flex-shrink: 0;
  width: 7px;
  height: 7px;
  border-radius: var(--radius-full);
  background: var(--color-text-disabled);
}
.trace-child-dot.is-done { background: var(--color-success); }
.trace-child-dot.is-failed { background: var(--color-danger); }
.trace-child-dot.is-canceled { background: var(--color-warning); }
.trace-child-dot.is-running {
  background: var(--color-accent);
  animation: trace-pulse 1.6s ease-in-out infinite;
}
@keyframes trace-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: .35; }
}
.trace-child-task-task {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
}
.trace-child-badge {
  flex-shrink: 0;
  padding: 0 var(--space-2);
  border-radius: var(--radius-full);
  font-size: var(--text-2xs);
  line-height: 1.6;
  background: var(--color-bg-hover);
  color: var(--color-text-muted);
}
.trace-child-badge.is-done { background: var(--color-success-bg); color: var(--color-success-ink); }
.trace-child-badge.is-failed { background: var(--color-danger-bg); color: var(--color-danger-ink); }
.trace-child-badge.is-canceled { background: var(--color-warning-bg); color: var(--color-warning-ink); }
.trace-child-badge.is-running { background: var(--color-accent-bg); color: var(--color-accent-text); }
/* 副行：role 胶囊 + token/耗时，等宽数值对齐，不挤主行。 */
.trace-child-task-sub {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 2px var(--space-2) var(--space-2) calc(var(--space-2) + 12px + var(--space-2));
}
.trace-child-task-role {
  padding: 0 var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-accent-bg);
  color: var(--color-accent-text);
  font-size: var(--text-2xs);
  flex-shrink: 0;
}
.trace-sub-metric {
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
/* 角色后的中文职能（排查/测试/开发…）：role 是英文标识，补一个中文让
   一眼能看出这个子任务是干什么的两个字。两处列表共用。 */
.trace-role-label {
  font-size: var(--text-2xs);
  color: var(--color-text-secondary);
  flex-shrink: 0;
}

/* 单个子任务的停止：默认淡出，悬停整行时才显形——子任务一多，一排红图标
   比任务标题还抢眼。::after 不用，直接靠 opacity + pointer-events 控制命中。 */
.trace-child-stop {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px; height: 20px;
  flex-shrink: 0;
  padding: 0;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast);
}
.trace-child-task-head:hover .trace-child-stop,
.trace-child-stop:focus-visible { opacity: 1; }
.trace-child-stop:hover { color: var(--color-danger-ink); background: var(--color-bg-hover); }
/* 键盘用户不触发 hover：聚焦时也显形，且给个明确的焦点环。 */
.trace-child-stop:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 1px; }
.trace-child-stop:disabled { color: var(--color-text-disabled); cursor: default; }

.trace-child-task-detail {
  padding: var(--space-2);
  border-top: 1px solid var(--color-border-light);
  background: var(--color-bg-secondary);
}
.trace-child-task-full {
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-primary);
  white-space: pre-wrap;
  word-break: break-word;
}
.trace-child-task-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1) var(--space-3);
  margin-top: var(--space-2);
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
  word-break: break-all;
}
.trace-child-task-result-label {
  margin-top: var(--space-2);
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
}
.trace-child-task-result {
  margin: var(--space-1) 0 0;
  padding: var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-bg-hover);
  max-height: 200px;
  overflow: auto;
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  line-height: 1.5;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}
.trace-child-task-steps { margin-top: var(--space-2); }

.trace-events {
  list-style: none;
  margin: var(--space-1) 0 0;
  padding: 0 0 0 var(--space-4);
  border-left: 1px solid var(--color-border-light);
}

.trace-event {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  padding: var(--space-1) 0 var(--space-1) var(--space-3);
  font-size: var(--text-xs);
  line-height: 1.5;
}

.trace-event-title {
  color: var(--color-text-primary);
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}

.trace-event-title.is-bad { color: var(--color-danger-ink); }

.trace-event-detail {
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trace-event.is-empty { color: var(--color-text-muted); }

/* 时间线：一步一张卡片，左侧序号 + 竖线串成执行顺序。
   与旧 trace-events 的区别是「一条事件一行」变成「一个动作一张卡」——
   tool/result 已由 eventsToSteps 合成一步。 */
.trace-timeline {
  list-style: none;
  margin: var(--space-1) 0 0;
  padding: 0 0 0 var(--space-2);
}

.trace-step {
  display: flex;
  gap: var(--space-2);
  position: relative;
  padding-bottom: var(--space-1);
}

/* 竖线：从序号圆点往下连到下一步。最后一步用 :last-child 掐掉。 */
.trace-step:not(:last-child)::before {
  content: '';
  position: absolute;
  left: 10px;
  top: 20px;
  bottom: -2px;
  width: 1px;
  background: var(--color-border-light);
}

.trace-step-idx {
  flex-shrink: 0;
  width: 20px;
  height: 20px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-full);
  background: var(--color-bg-secondary);
  color: var(--color-text-muted);
  font-size: var(--text-2xs);
  font-variant-numeric: tabular-nums;
  position: relative;
  z-index: 1;
}

.trace-step.is-done .trace-step-idx { background: var(--color-success-bg); color: var(--color-success-ink); }
.trace-step.is-think .trace-step-idx { background: var(--color-accent-bg); color: var(--color-accent-text); }

.trace-step-main {
  flex: 1;
  min-width: 0;
  padding: var(--space-1) var(--space-2);
  border-radius: var(--radius-sm);
  cursor: default;
}

.trace-step-main.is-clickable { cursor: pointer; }
.trace-step-main:hover { background: var(--color-bg-hover); }

.trace-step-line {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}

.trace-step-title {
  font-size: var(--text-xs);
  color: var(--color-text-primary);
  flex-shrink: 0;
}

.trace-step.is-note .trace-step-title { color: var(--color-text-muted); }
/* 失败的工具/错误事件归在 note 类里，仅靠下方红字不够醒目：
   把序号圆点和标题一起标红，长串时间线里也能立刻定位到出错那一步。
   is-err 由 !step.ok 显式加，不用 :has()（旧 WebView2 整条规则会静默失效）。 */
.trace-step.is-err .trace-step-idx {
  background: var(--color-danger-bg);
  color: var(--color-danger-ink);
}
.trace-step.is-err .trace-step-title { color: var(--color-danger-ink); }

.trace-step-target {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
  font-family: var(--font-mono);
}

.trace-step-badge {
  flex-shrink: 0;
  margin-left: auto;
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.trace-step-err {
  margin-top: 2px;
  font-size: var(--text-2xs);
  color: var(--color-danger-ink);
}

.trace-step-hidden {
  padding: var(--space-1) 0 var(--space-1) 28px;
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
}

/* 尊重系统的「减少动态效果」：箭头旋转与透明度过渡一并关掉。
   项目里只有 AiToolbar 做了这层降级，抽屉此前遗漏。 */
@media (prefers-reduced-motion: reduce) {
  .trace-child-task-arrow,
  .trace-child-stop,
  .trace-mode { transition: none; }
  .trace-child-task-arrow.is-open { transform: none; }
  .trace-child-dot.is-running { animation: none; }
}
</style>
