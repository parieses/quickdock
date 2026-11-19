<script setup lang="ts">
/**
 * 「上下文已用」详情（WP2）。
 *
 * 为什么要一个弹框而不是在气泡下面堆数字：用户想知道的是三件不同的事——
 * 「还有多少余量」「都花在哪了」「这个上限是哪来的」。前两件事需要条形与分项，
 * 第三件事决定他要不要去改配置（「128k 是模型真认的，还是我们猜的」）。
 *
 * 数据来自 `AIContextStats(convID)`：窗口上限要经过三层检测、工具 schema 开销
 * 要装配完才知道、上一发实测在进程内——这些前端都算不出来。
 * 打开时才拉一次，失败按 R9 走 toast + logErr。
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { AICompactConversation, AIContextStats } from '../../bindings/quickdock/services/ai/aiservice'
import { useDialogFocus } from '../composables/useDialogFocus'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'

// 返回体嵌在 ApiResult.Data 里，不在绑定模型里（方法签名是 *services.ApiResult），
// 所以这里手写一份字段说明，与 services/ai/ai_context_stats.go 的 json tag 对齐。
interface ContextStats {
  conversation_id: string
  model: string
  profile_ok: boolean
  window: number
  output: number
  ceiling: number
  history_budget: number
  loop_budget: number
  window_source: string
  measured_tokens: number
  estimated_tokens: number
  tool_count: number
  tool_tokens: number
  message_count: number
  history_chars: number
  session_prompt: number
  session_completion: number
  request_count: number
  // 上次压缩（WP2 compacted 状态）：at 为空表示这次进程里还没压过。
  last_compact_at: string
  last_compact_before: number
  last_compact_after: number
  last_compact_folded: number
  last_compact_summarized: number
  last_compact_by_model: boolean
}

const props = defineProps<{
  open: boolean
  convId: string
}>()

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'open-trace'): void
}>()

const { t, n } = useI18n()
const toast = useToast()

const dialogEl = ref<HTMLElement | null>(null)
useDialogFocus(() => props.open, dialogEl)

const stats = ref<ContextStats | null>(null)
const loading = ref(false)

async function load() {
  if (!props.convId) return
  loading.value = true
  try {
    stats.value = unwrap<ContextStats>(await AIContextStats(props.convId))
  } catch (e) {
    stats.value = null
    logErr('AiContextDialog.load', e)
    // 弹框里的错误就地显示就够，不再弹 toast：用户此刻就盯着这个框。
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.open, props.convId] as const,
  ([open]) => {
    if (open) void load()
  },
  { immediate: true },
)

/** 已用：实测优先。实测是上游报的原始用量，比任何估算都准。 */
const used = computed(() => {
  const s = stats.value
  if (!s) return 0
  return s.measured_tokens > 0 ? s.measured_tokens : s.estimated_tokens
})

/** 分母用出站上限而不是物理窗口：超过它的那次请求本来就会 400。 */
const total = computed(() => stats.value?.ceiling || stats.value?.window || 0)

const percent = computed(() => {
  if (!total.value) return 0
  return Math.min(100, Math.round((used.value / total.value) * 100))
})

const tone = computed(() => (percent.value >= 85 ? 'danger' : percent.value >= 60 ? 'warn' : 'ok'))

const isMeasured = computed(() => (stats.value?.measured_tokens ?? 0) > 0)

/** 上次压缩的一句话说明（没有就返回空串，整行不渲染）。 */
const compactSummary = computed(() => {
  const s = stats.value
  if (!s?.last_compact_at) return ''
  if (s.last_compact_by_model) {
    return t('aiCtxCompactByModel', {
      before: fmt(s.last_compact_before),
      after: fmt(s.last_compact_after),
      dropped: s.last_compact_summarized,
    })
  }
  return t('aiCtxCompactByFold', {
    folded: s.last_compact_folded,
    summarized: s.last_compact_summarized,
    before: fmt(s.last_compact_before),
    after: fmt(s.last_compact_after),
  })
})

const sourceText = computed(() => {
  switch (stats.value?.window_source) {
    case 'profile': return t('aiCtxSourceProfile')
    case 'learned': return t('aiCtxSourceLearned')
    case 'table': return t('aiCtxSourceTable')
    default: return t('aiCtxSourceDefault')
  }
})

function close() {
  emit('update:open', false)
}

// 「立即压缩」（WP4c）：手动触发一次历史摘要。
//
// 它会真的调一次模型（摘要），所以只在用户点了才做，并且按钮要能看见
// 「正在压缩」。压完重新拉一次统计——用户点这个按钮就是想看到数字变小。
const compacting = ref(false)

async function compactNow() {
  if (compacting.value || !props.convId) return
  compacting.value = true
  try {
    unwrap(await AICompactConversation(props.convId))
    await load()
  } catch (e) {
    // 压缩失败 ≠ 统计读不出来：把 stats 清掉会让弹框从「有数字」变成
    // 「读取失败，稍后再试」，用户根本分不清是哪一步坏了。
    logErr('AiContextDialog.compact', e)
    toast.error(getErrorMessage(e))
  } finally {
    compacting.value = false
  }
}

function openTrace() {
  close()
  emit('open-trace')
}

/** Esc 关闭；监听只在打开期间挂。 */
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

onBeforeUnmount(() => window.document.removeEventListener('keydown', onKeydown, true))

function fmt(n2: number): string {
  if (!n2) return '0'
  return n2 >= 1000 ? (n2 / 1000).toFixed(1) + 'k' : String(n2)
}

void n // 保留 i18n 的数字格式化入口，后续要按语言显示千分位时用
</script>

<template>
  <Teleport to="body">
    <template v-if="props.open">
      <div class="ctx-backdrop" @click="close" />
      <section ref="dialogEl" class="ctx-dialog" role="dialog" aria-modal="true" tabindex="-1" :aria-label="t('aiCtxTitle')">
        <header class="ctx-head">
          <span class="ctx-title">{{ t('aiCtxTitle') }}</span>
          <button class="ctx-close" :title="t('aiCtxClose')" @click="close"><X :size="14" /></button>
        </header>

        <div class="ctx-body">
          <p v-if="loading" class="ctx-hint">{{ t('aiCtxLoading') }}</p>
          <p v-else-if="!stats" class="ctx-hint is-bad">{{ t('aiCtxLoadFail') }}</p>

          <template v-else>
            <!-- 进度条：分母是「出站上限」，不是物理窗口 —— 后者超一点不会立刻死，
                 前者超了就是 400，用户该盯的是这条线。 -->
            <div class="ctx-meter">
              <div class="ctx-meter-top">
                <span class="ctx-meter-used" :class="`is-${tone}`">
                  {{ t('aiCtxUsedOf', { used: fmt(used), total: fmt(total) }) }}
                </span>
                <span class="ctx-meter-pct" :class="`is-${tone}`">{{ percent }}%</span>
              </div>
              <div class="ctx-bar">
                <div class="ctx-bar-fill" :class="`is-${tone}`" :style="{ width: percent + '%' }" />
              </div>
            </div>

            <dl class="ctx-rows">
              <div class="ctx-row">
                <dt>{{ t('aiCtxTools', { n: stats.tool_count }) }}</dt>
                <dd>{{ fmt(stats.tool_tokens) }}</dd>
              </div>
              <div class="ctx-row">
                <dt>{{ t('aiCtxHistory', { n: stats.message_count }) }}</dt>
                <dd>{{ fmt(stats.estimated_tokens - stats.tool_tokens) }}</dd>
              </div>
              <div class="ctx-row">
                <dt>{{ t('aiCtxOutputReserve') }}</dt>
                <dd>{{ fmt(stats.output) }}</dd>
              </div>
              <div class="ctx-row">
                <dt>{{ t('aiCtxCeiling') }}</dt>
                <dd>{{ fmt(stats.ceiling) }}</dd>
              </div>
              <div class="ctx-row">
                <dt>{{ t('aiCtxHistoryBudget') }}</dt>
                <dd>{{ fmt(stats.history_budget) }}</dd>
              </div>
            </dl>

            <div class="ctx-meta">
              <div class="ctx-meta-line">
                <span>{{ t('aiCtxModel') }}</span>
                <strong>{{ stats.model || '—' }}</strong>
              </div>
              <div class="ctx-meta-line">
                <span>{{ t('aiCtxWindow') }}</span>
                <strong>{{ fmt(stats.window) }} · {{ sourceText }}</strong>
              </div>
              <div class="ctx-meta-line">
                <span>{{ isMeasured ? t('aiCtxMeasured') : t('aiCtxEstimated') }}</span>
                <strong>{{ fmt(used) }}</strong>
              </div>
              <div class="ctx-meta-line">
                <span>{{ t('aiCtxSession') }}</span>
                <strong>{{ fmt(stats.session_prompt) }} / {{ fmt(stats.session_completion) }} / {{ stats.request_count }}</strong>
              </div>
              <p v-if="!stats.profile_ok" class="ctx-warn">{{ t('aiCtxNoProfile') }}</p>
              <!-- 上次压缩（WP2）：用户第一次遇到「AI 忽然不记得前文」时，
                   这行是唯一能解释它的事。 -->
              <p v-if="compactSummary" class="ctx-compact">
                <span>{{ t('aiCtxLastCompact') }}</span>
                <strong>{{ compactSummary }}</strong>
              </p>
            </div>

            <p class="ctx-note">{{ t('aiCtxHint') }}</p>
          </template>
        </div>

        <footer class="ctx-foot">
          <button class="btn btn-secondary" :disabled="compacting || !props.convId" @click="compactNow">
            {{ compacting ? t('aiCtxCompacting') : t('aiCtxCompactNow') }}
          </button>
          <button class="btn btn-secondary" @click="openTrace">{{ t('aiCtxTrace') }}</button>
          <button class="btn btn-primary" @click="close">{{ t('aiCtxOK') }}</button>
        </footer>
      </section>
    </template>
  </Teleport>
</template>

<style scoped>
.ctx-backdrop {
  position: fixed;
  inset: 0;
  background: var(--color-bg-overlay);
  z-index: 210;
}

.ctx-dialog {
  position: fixed;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  width: 460px;
  max-width: 92vw;
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  z-index: 211;
}

.ctx-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-4) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.ctx-title {
  flex: 1;
  font-size: var(--text-md);
  color: var(--color-text-primary);
}

.ctx-close {
  display: flex;
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

.ctx-close:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.ctx-body {
  flex: 1;
  overflow-y: auto;
  padding: var(--space-4);
}

.ctx-hint {
  margin: 0;
  padding: var(--space-4) 0;
  font-size: var(--text-sm);
  color: var(--color-text-muted);
}

.ctx-hint.is-bad { color: var(--color-danger); }

.ctx-meter-top {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  margin-bottom: var(--space-2);
}

.ctx-meter-used {
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

.ctx-meter-pct {
  font-size: var(--text-md);
  font-variant-numeric: tabular-nums;
}

.ctx-meter-used.is-warn, .ctx-meter-pct.is-warn { color: var(--color-warning); }
.ctx-meter-used.is-danger, .ctx-meter-pct.is-danger { color: var(--color-danger); }
.ctx-meter-pct.is-ok { color: var(--color-text-primary); }

.ctx-bar {
  height: 8px;
  border-radius: var(--radius-full);
  background: var(--color-bg-secondary);
  overflow: hidden;
}

.ctx-bar-fill {
  height: 100%;
  border-radius: var(--radius-full);
  background: var(--color-accent);
  transition: width var(--transition-base);
}

.ctx-bar-fill.is-warn { background: var(--color-warning); }
.ctx-bar-fill.is-danger { background: var(--color-danger); }

.ctx-rows {
  margin: var(--space-4) 0 0;
  padding: 0;
}

.ctx-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--color-border-light);
  font-size: var(--text-sm);
}

.ctx-row dt { color: var(--color-text-muted); min-width: 0; }
.ctx-row dd {
  margin: 0;
  color: var(--color-text-primary);
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}

.ctx-meta { margin-top: var(--space-4); }

.ctx-meta-line {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-1) 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.ctx-meta-line strong {
  font-weight: 500;
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
  text-align: right;
  word-break: break-word;
}

.ctx-warn {
  margin: var(--space-2) 0 0;
  font-size: var(--text-xs);
  color: var(--color-warning);
}

.ctx-compact {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin: var(--space-2) 0 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.ctx-compact strong {
  font-weight: 500;
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

.ctx-note {
  margin: var(--space-3) 0 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-disabled);
}

.ctx-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}
</style>
