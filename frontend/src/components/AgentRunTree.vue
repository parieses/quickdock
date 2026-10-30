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
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, Network } from '@lucide/vue'
import { AIAgentRunTree } from '../../bindings/quickdock/services/ai/aiservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

// 与 internal/db/agent_run.go 的 json tag 对齐（snake_case）。
interface RunNode {
  id: string
  run_id: string
  parent_id: string
  seq: number
  role: string
  task: string
  status: string
  cwd: string
  model: string
  result: string
  prompt_tokens: number
  completion_tokens: number
  started_at: string
  ended_at: string
}
interface RunHead {
  id: string
  status: string
  goal: string
  used_tokens: number
  budget_tokens: number
  error: string
}
interface RunTree {
  run: RunHead | null
  nodes: RunNode[]
  events: unknown[]
  last_seq: number
}

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

function onToggle(e: Event) {
  const el = e.target as HTMLDetailsElement
  if (el.open) void ensureLoaded()
}

// depth 由 parent_id 链算出来，不假设后端已经排好层：
// 节点是并发创建的，seq 只能保证顺序，保证不了「父一定在前」。
const depths = computed<Record<string, number>>(() => {
  const nodes = tree.value?.nodes ?? []
  const byID = new Map(nodes.map((n) => [n.id, n]))
  const out: Record<string, number> = {}
  const walk = (id: string, guard: Set<string>): number => {
    if (out[id] !== undefined) return out[id]
    const n = byID.get(id)
    // 环引用（数据损坏）时立即收敛，别把页面转死。
    if (!n || guard.has(id)) return 0
    guard.add(id)
    const p = n.parent_id ? byID.get(n.parent_id) : undefined
    const d = p ? walk(p.id, guard) + 1 : 0
    out[id] = d
    return d
  }
  for (const n of nodes) walk(n.id, new Set())
  return out
})

const rows = computed(() =>
  (tree.value?.nodes ?? []).map((n) => ({
    ...n,
    indent: depths.value[n.id] ?? 0,
    tokens: n.prompt_tokens + n.completion_tokens,
  })),
)

const nodeCount = computed(() => rows.value.length)

const statusText = computed(() => {
  const s = tree.value?.run?.status
  if (s === 'running') return t('aiRunStatusRunning')
  if (s === 'failed') return t('aiRunStatusFailed')
  if (s === 'canceled') return t('aiRunStatusCanceled')
  return t('aiRunStatusDone')
})

function fmtTokens(n: number): string {
  if (!n) return ''
  return n >= 1000 ? (n / 1000).toFixed(1) + 'k' : String(n)
}
</script>

<template>
  <details class="qd-run" @toggle="onToggle">
    <summary class="qd-run-sum">
      <ChevronDown :size="11" class="qd-run-caret" />
      <Network :size="12" />
      <span>{{ t('aiRunTree', { n: nodeCount || 1 }) }}</span>
      <span v-if="tree?.run" class="qd-run-state" :class="'is-' + tree.run.status">{{ statusText }}</span>
    </summary>
    <div class="qd-run-body">
      <div v-if="loading" class="qd-run-hint">{{ t('aiRunLoading') }}</div>
      <div v-else-if="failed" class="qd-run-hint is-err">{{ failed }}</div>
      <template v-else>
        <div
          v-for="n in rows"
          :key="n.id"
          class="qd-run-node"
          :class="'is-' + n.status"
          :style="{ marginLeft: n.indent * 14 + 'px' }"
        >
          <div class="qd-run-node-head">
            <span class="qd-run-dot" :class="'is-' + n.status"></span>
            <span class="qd-run-role">{{ n.role }}</span>
            <span class="qd-run-task" :title="n.task">{{ n.task }}</span>
            <span v-if="n.tokens" class="qd-run-tokens">{{ fmtTokens(n.tokens) }}</span>
          </div>
          <!-- 结论默认折叠：一排子任务全展开会把回复正文挤出屏幕，
               而用户多数时候只想知道「派了几个、成没成」。 -->
          <details v-if="n.result" class="qd-run-result">
            <summary>{{ t('aiRunResult') }}</summary>
            <pre>{{ n.result }}</pre>
          </details>
        </div>
        <div v-if="!rows.length" class="qd-run-hint">{{ t('aiRunEmpty') }}</div>
      </template>
    </div>
  </details>
</template>
