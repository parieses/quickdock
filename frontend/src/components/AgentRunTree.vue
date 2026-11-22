<script setup lang="ts">
/**
 * 编排 run 树：一次提问里派出去的子 agent。
 *
 * 为什么按需加载（展开时才拉）：一条对话里可能有十几条带编排的回复，
 * 进页面就把每棵树都拉一遍纯属浪费——用户真正会展开的通常只有最后一条。
 *
 * 为什么单独成组件而不是写进 AIPage：消息列表用了 v-memo，父级的整棵子树会
 * 被缓存跳过；数据拉回来时父级不会重渲染。组件自己的响应式状态不受这个影响，
 * 该刷新的时候一定刷新。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight } from '@lucide/vue'
import { AIAgentRunTree } from '../../bindings/quickdock/services/ai/aiservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { fmtElapsed, groupEventsByNode, summarizeEvent, type RunEvent, type RunNode, type RunTree } from '../utils/aiRunTree'
import { agentRoleLabel, toolVerb } from '../utils/aiTools'

// 结构定义在 utils/aiRunTree.ts：轨迹抽屉（AiTraceDrawer.vue）用的是同一份。
// 两处各写一遍接口时，字段一旦对不上就是「一处正常、另一处静默空白」，没有任何报错可看。
const props = defineProps<{ runId: string }>()
const { t } = useI18n()

const tree = ref<RunTree | null>(null)
const loading = ref(false)
const loaded = ref(false)
const failed = ref('')

/** 展开时才拉一次；已经拉过或正在拉就跳过。 */
async function ensureLoaded() {
  if (loaded.value || loading.value || !props.runId) return
  loading.value = true
  failed.value = ''
  try {
    tree.value = unwrap<RunTree>(await AIAgentRunTree(props.runId, 0))
    loaded.value = true
  } catch (e) {
    // 失败不弹 toast：这是个附属面板，用户可能只是想看一眼；
    // 就地显示一行错误，重开一次折叠即可重试（loaded 仍为 false）。
    failed.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

const expanded = ref(false)
function onToggle(e: Event) {
  const el = e.target as HTMLDetailsElement
  expanded.value = el.open
  if (el.open) void ensureLoaded()
}

// 成员卡片：每个节点 = 一次派活 = 一位「团队成员」。
// 保留 parent 关系仅用于标注「由谁派出」，视觉上平铺成卡片，不做树缩进。
const rows = computed(() => {
  const nodes = tree.value?.nodes ?? []
  const byID = new Map(nodes.map((n) => [n.id, n]))
  return nodes.map((n) => ({
    ...n,
    roleLabel: agentRoleLabel(n.role, t),
    tokens: n.prompt_tokens + n.completion_tokens,
    parentRole: n.parent_id ? (byID.get(n.parent_id)?.role ?? '') : '',
  }))
})

const nodeCount = computed(() => rows.value.length)

// 单个成员的显示状态：带 pending（登记未开跑），与 run 整体状态口径分开。
function memberStatus(s: string): { text: string; cls: string } {
  // 旧 run 里 lead 节点可能没被收口：run 已终态，节点还挂着 running/pending。
  // 以 run 为准（后端现在会同步写节点，但历史数据仍要能看）。
  const runStatus = tree.value?.run?.status
  if ((s === 'running' || s === 'pending') &&
      (runStatus === 'done' || runStatus === 'failed' || runStatus === 'canceled')) {
    return memberStatus(runStatus)
  }
  if (s === 'running') return { text: t('aiRunStatusRunning'), cls: 'running' }
  if (s === 'done' || s === 'success') return { text: t('aiRunStatusDone'), cls: 'done' }
  if (s === 'failed' || s === 'error') return { text: t('aiRunStatusFailed'), cls: 'failed' }
  if (s === 'canceled' || s === 'cancelled') return { text: t('aiRunStatusCanceled'), cls: 'canceled' }
  if (s === 'pending') return { text: t('aiRunStatusPending'), cls: 'pending' }
  return { text: s || t('aiRunStatusPending'), cls: 'pending' }
}

// 头像字母：role 的首字符（Explore-1 → E），全大写便于扫读。
function avatarLetter(role: string): string {
  return (role || '?').trim().charAt(0).toUpperCase()
}
// 头像配色按序号循环，只引用现有 token（不新增色值）。
function avatarTone(i: number): string {
  return ['a', 'b', 'c', 'd'][i % 4]
}

// 已处理时长：running 成员用 now 滚动，其余用 ended_at。now 只在展开且有
// running 成员时由定时器推进，收起后立即停，避免常驻每秒重渲染。
const now = ref(Date.now())
let tickTimer: ReturnType<typeof setInterval> | null = null
const hasRunning = computed(() => {
  const s = tree.value?.run?.status
  // run 已终态就别再每秒重渲染了——旧数据里 lead 节点仍写着 running。
  if (s === 'done' || s === 'failed' || s === 'canceled') return false
  return rows.value.some((n) => n.status === 'running' || n.status === 'pending')
})
watch([expanded, hasRunning], ([open, running]) => {
  if (open && running && tickTimer === null) {
    tickTimer = setInterval(() => { now.value = Date.now() }, 1000)
  } else if ((!open || !running) && tickTimer !== null) {
    clearInterval(tickTimer)
    tickTimer = null
  }
})
onUnmounted(() => {
  if (tickTimer !== null) clearInterval(tickTimer)
})

function elapsedOf(n: RunNode): string {
  const start = Date.parse(n.started_at)
  if (!Number.isFinite(start)) return '—'
  const runStatus = tree.value?.run?.status ?? ''
  const terminal = runStatus === 'done' || runStatus === 'failed' || runStatus === 'canceled'
  const open = !terminal && (n.status === 'running' || n.status === 'pending')
  const endMs = open ? now.value : Date.parse(n.ended_at)
  if (!Number.isFinite(endMs) || endMs < start) return '—'
  return fmtElapsed(endMs - start)
}

/** 每个节点自己的事件，展开时按 seq 顺序铺开。 */
const eventsByNode = computed(() => groupEventsByNode(tree.value?.events ?? []))

/**
 * 事件的一行文案（这里空间小，只给「工具名 · 耗时」或一句错误）。
 *
 * 完整的工具参数、diff 行数在左侧轨迹抽屉里看——那里有横向空间。
 * 两处共用 utils/aiRunTree.ts 的 summarizeEvent，字段口径不会分叉。
 */
function eventLine(e: RunEvent): string {
  const s = summarizeEvent(e)
  // 工具名走全局映射：这里空间小，中文动词 + 耗时就够；完整参数去左侧抽屉。
  const verb = () => toolVerb(s.name, (k) => t(k))
  if (e.kind === 'tool') return verb()
  if (e.kind === 'tool_result') {
    const parts = [verb()]
    if (s.ms > 0) parts.push(fmtElapsed(s.ms))
    if (!s.ok) parts.push(t('aiTraceEventFail'))
    return parts.join(' · ')
  }
  // 非工具事件原本直接铺 kind（reasoning_round / approval / plan），
  // 用户看到的是一串英文标识符，和旁边的中文步骤混在一起。
  if (e.kind === 'reasoning_round') return t('aiThought')
  if (e.kind === 'approval') return t('aiRunStepApproval')
  if (e.kind === 'plan') return t('aiRunStepPlan')
  if (e.kind === 'compact') return t('aiCompacted')
  if (e.kind === 'done') return t('aiTraceEventDone')
  if (e.kind === 'notice' || e.kind === 'error') return s.message || t('aiTraceEventFail')
  return verb()
}

const statusText = computed(() => {
  const s = tree.value?.run?.status
  if (s === 'failed') return t('aiRunStatusFailed')
  if (s === 'canceled') return t('aiRunStatusCanceled')
  if (s === 'done') return t('aiRunStatusDone')
  // running / pending / 缺省都归「进行中」——与抽屉的 statusText 口径一致。
  // pending（登记了还没开跑的节点）在这里显示「已完成」是错的。
  return t('aiRunStatusRunning')
})

function fmtTokens(n: number): string {
  if (!n) return ''
  return n >= 1000 ? (n / 1000).toFixed(1) + 'k' : String(n)
}
</script>

<template>
  <details class="qd-run" @toggle="onToggle">
    <summary class="qd-run-sum">
      <ChevronDown :size="13" class="qd-run-caret" />
      <!-- 叠加头像：取前几位成员的首字符，一眼看出「几个人在做」。 -->
      <span class="qd-run-avatars">
        <span
          v-for="(n, i) in rows.slice(0, 4)"
          :key="n.id"
          class="qd-run-avatar qd-run-avatar--stack"
          :class="'tone-' + avatarTone(i)"
        >{{ avatarLetter(n.role) }}</span>
        <span v-if="nodeCount > 4" class="qd-run-avatar qd-run-avatar--more">+{{ nodeCount - 4 }}</span>
      </span>
      <span>{{ t('aiRunTree', { n: nodeCount || 1 }) }}</span>
      <span v-if="tree?.run" class="qd-run-state" :class="'is-' + tree.run.status">{{ statusText }}</span>
    </summary>
    <div class="qd-run-body">
      <div v-if="loading" class="qd-run-hint">{{ t('aiRunLoading') }}</div>
      <div v-else-if="failed" class="qd-run-hint is-err">{{ failed }}</div>
      <template v-else>
        <!-- 一人一张卡片：头像 + 名称 + 状态 + 箭头；点开看任务/耗时/产出。 -->
        <details
          v-for="(n, i) in rows"
          :key="n.id"
          class="qd-run-member"
          :class="'is-' + n.status"
        >
          <summary class="qd-run-member-sum">
            <span class="qd-run-avatar" :class="'tone-' + avatarTone(i)">{{ avatarLetter(n.role) }}</span>
            <span class="qd-run-member-name">{{ n.role }}</span>
            <!-- 与团队卡同口径：名字后面直接挂中文职能，不展开也知道这位负责哪块。 -->
            <span v-if="n.roleLabel" class="qd-team-role-chip">{{ n.roleLabel }}</span>
            <span v-if="n.parentRole" class="qd-run-member-parent">← {{ n.parentRole }}</span>
            <span class="qd-run-member-status" :class="memberStatus(n.status).cls">
              <span class="qd-run-dot" :class="'is-' + memberStatus(n.status).cls"></span>
              {{ memberStatus(n.status).text }}
            </span>
            <ChevronRight :size="13" class="qd-run-member-arrow" />
          </summary>
          <div class="qd-run-member-body">
            <div v-if="n.roleLabel" class="qd-team-role">
              <span class="qd-team-role-tag">{{ t('aiAgentRoleLabel') }}</span>
              <span class="qd-run-role">{{ n.role }}</span>
              <span>· {{ n.roleLabel }}</span>
            </div>
            <p class="qd-run-task-full">{{ n.task }}</p>
            <div class="qd-run-member-meta">
              <span>{{ t('aiRunElapsed') }} {{ elapsedOf(n) }}</span>
              <span v-if="n.tokens">{{ fmtTokens(n.tokens) }} tok</span>
              <span v-if="n.model" :title="n.model">{{ n.model }}</span>
            </div>
            <!-- 结论默认折叠：一排成员全展开会把回复正文挤出屏幕。 -->
            <details v-if="n.result" class="qd-run-result">
              <summary>{{ t('aiRunResult') }}</summary>
              <pre>{{ n.result }}</pre>
            </details>
            <!-- 该成员自己执行的每一步，默认折叠；完整参数去左侧轨迹抽屉。 -->
            <details v-if="(eventsByNode.get(n.id) ?? []).length" class="qd-run-events">
              <summary>{{ t('aiRunEvents', { n: (eventsByNode.get(n.id) ?? []).length }) }}</summary>
              <ul class="qd-run-event-list">
                <li v-for="e in eventsByNode.get(n.id) ?? []" :key="e.id" class="qd-run-event">
                  {{ eventLine(e) }}
                </li>
              </ul>
            </details>
          </div>
        </details>
        <div v-if="!rows.length" class="qd-run-hint">{{ t('aiRunEmpty') }}</div>
      </template>
    </div>
  </details>
</template>
