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
import { Activity, ChevronDown, ChevronRight, X } from '@lucide/vue'
import { AIAgentRunTree, AISessionTimeline } from '../../bindings/quickdock/services/ai/aiservice'
import { useDialogFocus } from '../composables/useDialogFocus'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { toolTarget, toolVerb } from '../utils/aiTools'
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
  try {
    const res = unwrap<SessionTimeline>(await AISessionTimeline(props.convId, 0))
    timeline.value = res?.runs ?? []
    // 默认展开最近一次；用户手动选过 run 之后就不再自动切换。
    const last = timeline.value[timeline.value.length - 1]
    if (last && !runToggleTouched.value) openRuns.value[last.run.id] = true
  } catch (e) {
    sessionFailed.value = getErrorMessage(e)
    logErr('ai:traceTimeline', e)
  } finally {
    if (!silent) sessionLoading.value = false
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

/** 步骤标题：工具用人话动词，思考用固定文案，结论用事件名。 */
function stepTitle(step: RunStep): string {
  if (step.kind === 'think') return t('aiThought')
  if (step.kind === 'tool') return toolVerb(step.name, t)
  if (step.kind === 'compact') return step.compactKind === 'loop' ? t('aiCompactFolded') : t('aiCompacted')
  return step.name
}

/** 步骤的操作目标：复用气泡卡片的 shortPath 口径，绝对路径压成末两段。 */
function stepTarget(step: RunStep): string {
  if (step.kind !== 'tool') return ''
  return toolTarget({ args: step.args } as AIToolCall)
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
  try {
    tree.value = unwrap<RunTree>(await AIAgentRunTree(props.runId, 0))
  } catch (e) {
    failed.value = getErrorMessage(e)
    logErr('AiTraceDrawer.load', e)
    tree.value = null
  } finally {
    loading.value = false
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
          <div class="trace-top-children-label">{{ t('aiTraceSessionChildren', { n: topChildModel.tasks.length }) }}</div>
          <ul class="trace-child-tasks">
            <li v-for="c in topChildModel.tasks" :key="c.id" class="trace-child-task">
              <div v-key-click class="trace-child-task-head" @click="toggleChild(c.id)">
                <span class="trace-child-task-task" :title="c.task">{{ c.task }}</span>
                <span class="trace-child-task-role">{{ c.role }}</span>
                <span class="trace-child-task-metrics">
                  <span v-if="c.tokens">{{ fmtTokens(c.tokens) }} tok</span>
                  <span v-if="c.elapsedMs">{{ fmtElapsed(c.elapsedMs) }}</span>
                  <span class="trace-node-status" :class="`is-${c.status}`">{{ c.status }}</span>
                </span>
                <!-- 右箭头沿用 DSH 子任务面板的读法：整行可点，箭头只提示「还能展开」。 -->
                <ChevronRight :size="12" class="trace-child-task-arrow" :class="{ 'is-open': childOpen[c.id] }" />
              </div>
              <div v-if="childOpen[c.id]" class="trace-child-task-detail">
                <div class="trace-child-task-full">{{ c.task }}</div>
                <div class="trace-child-task-meta">
                  <span v-if="c.node.cwd">{{ t('aiTraceChildCwd') }}: {{ c.node.cwd }}</span>
                  <span v-if="c.node.model">{{ t('aiTraceChildModel') }}: {{ c.node.model }}</span>
                  <span>{{ t('aiTraceChildTokens', { input: fmtTokens(c.node.prompt_tokens), output: fmtTokens(c.node.completion_tokens) }) }}</span>
                  <span v-if="c.elapsedMs">{{ fmtElapsed(c.elapsedMs) }}</span>
                </div>
                <template v-if="c.node.result">
                  <div class="trace-child-task-result-label">{{ t('aiTraceChildResult') }}</div>
                  <pre class="trace-child-task-result">{{ c.node.result }}</pre>
                </template>
                <ol class="trace-timeline trace-child-task-steps">
                  <li
                    v-for="(step, si) in stepsForChild(c.id).steps"
                    :key="step.key"
                    class="trace-step"
                    :class="`is-${step.kind}`"
                  >
                    <span class="trace-step-idx">{{ si + 1 }}</span>
                    <div v-key-click class="trace-step-main" @click="stepHasDetail(step) && toggleStep(step.key)">
                      <div class="trace-step-line">
                        <span class="trace-step-title">{{ stepTitle(step) }}</span>
                        <span v-if="step.kind === 'tool' && stepTarget(step)" class="trace-step-target" :title="step.path || step.args">
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
                      <div v-if="stepOpen[step.key] && stepHasDetail(step)" class="trace-step-detail">
                        <pre v-if="step.text">{{ step.text }}</pre>
                        <pre v-if="step.args">{{ step.args }}</pre>
                        <pre v-if="step.output">{{ step.output }}</pre>
                        <pre v-else-if="step.result">{{ step.result }}</pre>
                        <pre v-if="step.diff">{{ step.diff }}</pre>
                      </div>
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
                <span class="trace-node-status" :class="`is-${r.run.status}`">{{ r.run.status }}</span>
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
                  :class="`is-${step.kind}`"
                >
                  <span class="trace-step-idx">{{ si + 1 }}</span>
                  <div v-key-click class="trace-step-main" @click="stepHasDetail(step) && toggleStep(step.key)">
                    <div class="trace-step-line">
                      <span class="trace-step-title">{{ stepTitle(step) }}</span>
                      <span v-if="step.kind === 'tool' && stepTarget(step)" class="trace-step-target" :title="step.path || step.args">
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
                    <div v-if="stepOpen[step.key] && stepHasDetail(step)" class="trace-step-detail">
                      <pre v-if="step.text">{{ step.text }}</pre>
                      <pre v-if="step.args">{{ step.args }}</pre>
                      <pre v-if="step.output">{{ step.output }}</pre>
                      <pre v-else-if="step.result">{{ step.result }}</pre>
                      <pre v-if="step.diff">{{ step.diff }}</pre>
                    </div>
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
                <span>{{ tree.run.status }}</span>
                <span class="trace-sep">·</span>
                <span>{{ t('aiTraceSteps', { n: tree.nodes.length }) }}</span>
                <span class="trace-sep">·</span>
                <span>{{ fmtTokens(tree.run.used_tokens) }} / {{ fmtTokens(tree.run.budget_tokens) }} tok</span>
              </div>
            </div>

            <ul class="trace-nodes">
              <li v-for="n in nodes" :key="n.id" class="trace-node" :style="{ paddingLeft: n.depth * 12 + 'px' }">
                <div v-key-click class="trace-node-head" @click="toggleNode(n.id)">
                  <component :is="expanded[n.id] ? ChevronDown : ChevronRight" :size="11" class="trace-node-caret" />
                  <span class="trace-node-role">{{ n.role }}</span>
                  <span class="trace-node-task">{{ n.task }}</span>
                  <span class="trace-node-status" :class="`is-${n.status}`">{{ n.status }}</span>
                </div>
                <ol v-if="expanded[n.id]" class="trace-timeline">
                  <li
                    v-for="(step, si) in stepsByNode.get(n.id)?.steps ?? []"
                    :key="step.key"
                    class="trace-step"
                    :class="`is-${step.kind}`"
                  >
                    <span class="trace-step-idx">{{ si + 1 }}</span>
                    <div v-key-click class="trace-step-main" @click="stepHasDetail(step) && toggleStep(step.key)">
                      <div class="trace-step-line">
                        <span class="trace-step-title">{{ stepTitle(step) }}</span>
                        <span v-if="step.kind === 'tool' && stepTarget(step)" class="trace-step-target" :title="step.path || step.args">
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
                      <div v-if="stepOpen[step.key] && stepHasDetail(step)" class="trace-step-detail">
                        <pre v-if="step.text">{{ step.text }}</pre>
                        <pre v-if="step.args">{{ step.args }}</pre>
                        <pre v-if="step.output">{{ step.output }}</pre>
                        <pre v-else-if="step.result">{{ step.result }}</pre>
                        <pre v-if="step.diff">{{ step.diff }}</pre>
                      </div>
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
  z-index: 200;
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
  z-index: 201;
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

.trace-head-ico { color: var(--color-accent); }

.trace-title {
  font-size: var(--text-md);
  color: var(--color-text-primary);
}

/* 范围切换（WP12）：贴在标题右侧，不占一整行——抽屉很窄，多一行会把
   下面的事件列表挤掉大半屏。 */
.trace-modes {
  display: flex;
  gap: var(--space-1);
  margin-left: auto;
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

.trace-close {  display: flex;
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

.trace-hint.is-bad { color: var(--color-danger); }

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

.trace-node { margin-bottom: var(--space-2); }

.trace-node-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2);
  border-radius: var(--radius-sm);
  cursor: pointer;
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

.trace-node-status.is-done { color: var(--color-success); }
.trace-node-status.is-failed { color: var(--color-danger); }

/* 抽屉顶部的子任务区：固定在头部下方，不随下面的 run 列表滚动。 */
.trace-top-children {
  padding: var(--space-2) var(--space-4) 0;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
/* 「子任务」入口下这是唯一的内容区：整段会话的子任务可能很多，
   让它自己吃掉剩余高度并滚动，不能像轨迹模式那样只占一小条。 */
.trace-top-children.is-children-view {
  flex: 1;
  min-height: 0;
  border-bottom: 0;
  overflow-y: auto;
}
.trace-top-children-label {
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
}

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
  padding: var(--space-2);
  font-size: var(--text-xs);
  cursor: pointer;
}
.trace-child-task-head:hover { background: var(--color-bg-hover); }
.trace-child-task-task {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
}
.trace-child-task-role {
  padding: 0 var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-accent-bg);
  color: var(--color-accent-text);
  font-size: var(--text-2xs);
  flex-shrink: 0;
}
.trace-child-task-metrics {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  flex-shrink: 0;
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
}
.trace-child-task-arrow {
  flex-shrink: 0;
  color: var(--color-text-disabled);
  transition: transform var(--transition-fast);
}
.trace-child-task-arrow.is-open { transform: rotate(90deg); }

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

.trace-event-title.is-bad { color: var(--color-danger); }

.trace-event-detail {
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trace-event.is-empty { color: var(--color-text-disabled); }

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

.trace-step.is-done .trace-step-idx { background: var(--color-success-bg); color: var(--color-success); }
.trace-step.is-think .trace-step-idx { background: var(--color-accent-bg); color: var(--color-accent-text); }

.trace-step-main {
  flex: 1;
  min-width: 0;
  padding: var(--space-1) var(--space-2);
  border-radius: var(--radius-sm);
  cursor: default;
}

.trace-step-main:has(.trace-step-detail),
.trace-step-main:has(.trace-node-caret) { cursor: pointer; }
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
  color: var(--color-danger);
}

.trace-step-detail {
  margin-top: var(--space-2);
  padding: var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  max-height: 240px;
  overflow: auto;
}

.trace-step-detail pre {
  margin: 0 0 var(--space-2);
  font-size: var(--text-2xs);
  line-height: 1.5;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono);
}
.trace-step-detail pre:last-child { margin-bottom: 0; }

.trace-step-hidden {
  padding: var(--space-1) 0 var(--space-1) 28px;
  font-size: var(--text-2xs);
  color: var(--color-text-disabled);
}
</style>
