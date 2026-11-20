<script setup lang="ts">
/**
 * 会话统计 / Token 用量弹框（对齐 DSH 的下方胶囊详情）。
 *
 * 两个页签共用一次数据拉取：AISessionStats 一条 SQL 聚合给出轮次、用时、
 * TTFT、TPS 与 token 明细。打开时才拉，失败就地显示，不弹 toast 打断。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { AISessionStats } from '../../bindings/quickdock/services/ai/aiservice'
import { unwrap } from '../utils/api'
import { logErr } from '../utils/logger'

type Tab = 'session' | 'tokens'

/** 单条回复的用量（「本轮用量」从消息末尾点开时传入）。不传则按 convId 拉整段会话。 */
export interface UsageSnapshot {
  model: string
  total: number
  uncached: number
  cacheRead: number
  output: number
}

const props = defineProps<{
  open: boolean
  convId: string
  tab: Tab
  usage?: UsageSnapshot | null
}>()
const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'update:tab', v: Tab): void
}>()

const { t } = useI18n()

interface Stats {
  rounds: number
  steps: number
  model_ms: number
  tool_ms: number
  avg_first_ms: number
  tps: number
  request_count: number
  total_tokens: number
  uncached_input: number
  cache_read: number
  output: number
  cache_hit_rate: number
  model: string
  model_details?: { model: string; tokens: number; requests: number }[]
}

const stats = ref<Stats | null>(null)
const loading = ref(false)

// 单条消息用量直接拼成 Stats（只填 token 相关字段，时间口径本弹框 token 页用不到）。
function statsFromUsage(u: UsageSnapshot): Stats {
  return {
    rounds: 0, steps: 0, model_ms: 0, tool_ms: 0, avg_first_ms: 0, tps: 0,
    request_count: 1, total_tokens: u.total, uncached_input: u.uncached,
    cache_read: u.cacheRead, output: u.output,
    cache_hit_rate: u.uncached + u.cacheRead > 0 ? Math.round(u.cacheRead / (u.uncached + u.cacheRead) * 100) : 0,
    model: u.model,
    model_details: [], // 单条回复只有一个模型，明细列表无意义
  }
}

async function load() {
  // 单条用量走 prop，不请求后端（点开即刻显示，也没有会话级聚合可对应）。
  if (props.usage) {
    stats.value = statsFromUsage(props.usage)
    loading.value = false
    return
  }
  if (!props.convId) return
  loading.value = true
  try {
    stats.value = unwrap<Stats>(await AISessionStats(props.convId))
  } catch (e) {
    stats.value = null
    logErr('AiSessionStatsDialog.load', e)
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.open, props.convId, props.usage] as const,
  ([open]) => { if (open) void load() },
  { immediate: true },
)

function close() { emit('update:open', false) }
/** 千分位整数（本轮用量弹框：2,862,497 tok 这种全量写法，不缩写）。 */
function fmtFull(n: number): string {
  return Math.round(n || 0).toLocaleString('en-US')
}
/** 命中率保留一位小数（截图口径 99.7%，不是取整的 99%）。分母用未缓存+缓存读取=prompt。
 *  上限夹到 100：cache_read 偶尔会大于 prompt（上游计数口径异常），不夹会出现 137.0%。 */
function hitRate1(st: Stats): string {
  const prompt = st.uncached_input + st.cache_read
  return prompt > 0 ? Math.min(100, (st.cache_read / prompt) * 100).toFixed(1) : '0.0'
}
/** 毫秒转「22 分 55 秒」/「3.6 秒」/「820 毫秒」。单位按语言走 i18n，不写死中文。 */
function fmtDur(ms: number): string {
  if (ms <= 0) return t('aiSStatDurSec', { n: 0 })
  if (ms < 1000) return t('aiSStatDurMs', { n: Math.round(ms) })
  const sec = ms / 1000
  if (sec < 60) return t('aiSStatDurSec', { n: sec.toFixed(1) })
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  return t('aiSStatDurMinSec', { m, s })
}

// 标题：单条回复→「本轮用量」；会话级→「会话统计 / Token 用量」。
//
// 直接返回**翻译后的字符串**而不是 key：`t(变量)` 里的 key 静态检查看不见，
// 曾经 aiSStatTabTokens 就是这么漏掉的（中英字典都没有，界面上直接显示这串
// 英文标识符）。每个分支写死字面量 key，缺了就一眼能查出来。
const dialogTitle = computed(() => {
  if (props.tab === 'tokens') return props.usage ? t('aiMsgUsageTitle') : t('aiSStatTabTokens')
  return t('aiSStatTitle')
})

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && props.open) { e.stopPropagation(); close() }
}
watch(() => props.open, (v) => {
  if (v) window.document.addEventListener('keydown', onKeydown, true)
  else window.document.removeEventListener('keydown', onKeydown, true)
}, { immediate: true })
</script>

<template>
  <Teleport to="body">
    <template v-if="props.open">
      <div class="sstat-backdrop" @click="close" />
      <section class="sstat-dialog" role="dialog" aria-modal="true" :aria-label="dialogTitle">
        <header class="sstat-head">
          <span class="sstat-title">{{ dialogTitle }}</span>
          <strong v-if="props.tab === 'tokens' && stats" class="sstat-head-total">{{ fmtFull(stats.total_tokens) }} tok</strong>
          <button class="sstat-close" :title="t('aiCtxClose')" @click="close"><X :size="14" /></button>
        </header>

        <div class="sstat-body">
          <p v-if="loading" class="sstat-hint">{{ t('aiCtxLoading') }}</p>
          <p v-else-if="!stats" class="sstat-hint is-bad">{{ t('aiCtxLoadFail') }}</p>

          <template v-else-if="props.tab === 'session'">
            <div class="sstat-hero">
              <strong>{{ stats.rounds }}</strong><span>{{ t('aiSStatRounds') }}</span>
              <strong>{{ stats.steps }}</strong><span>{{ t('aiSStatSteps') }}</span>
            </div>
            <dl class="sstat-rows">
              <div class="sstat-row">
                <dt>{{ t('aiSStatModelTime') }}</dt>
                <dd>{{ fmtDur(stats.model_ms) }}</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatToolTime') }}</dt>
                <dd>{{ fmtDur(stats.tool_ms) }}</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatTTFT') }}</dt>
                <dd>{{ fmtDur(stats.avg_first_ms) }}</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatTPS') }}</dt>
                <dd>{{ stats.tps }} tok/s</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatRequests') }}</dt>
                <dd>{{ stats.request_count }}</dd>
              </div>
            </dl>
            <p class="sstat-note">{{ t('aiSStatModelTimeHint') }}</p>
          </template>

          <template v-else>
            <dl class="sstat-rows">
              <div class="sstat-row">
                <dt>{{ t('aiMsgUsageProvider') }}</dt>
                <dd class="sstat-provider">{{ stats.model || '—' }}</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatHitRate') }}</dt>
                <dd>{{ hitRate1(stats) }}%</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatUncached') }}</dt>
                <dd>{{ fmtFull(stats.uncached_input) }} tok</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatCacheRead') }}</dt>
                <dd>{{ fmtFull(stats.cache_read) }} tok</dd>
              </div>
              <div class="sstat-row">
                <dt>{{ t('aiSStatOutput') }}</dt>
                <dd>{{ fmtFull(stats.output) }} tok</dd>
              </div>
            </dl>
            <!-- 会话中途换过模型时列全部模型；只有一种时不重复占地方（上面首行已显示）。 -->
            <div v-if="stats.model_details && stats.model_details.length > 1" class="sstat-models">
              <div class="sstat-models-title">{{ t('aiMsgUsageModels') }}</div>
              <div v-for="m in stats.model_details" :key="m.model" class="sstat-model-row">
                <span class="sstat-model-name">{{ m.model }}</span>
                <span class="sstat-model-num">{{ fmtFull(m.tokens) }} tok <em>· {{ m.requests }} {{ t('aiMsgUsageReqShort') }}</em></span>
              </div>
            </div>
          </template>
        </div>
      </section>
    </template>
  </Teleport>
</template>

<style scoped>
.sstat-backdrop {
  position: fixed;
  inset: 0;
  background: var(--color-bg-overlay);
  z-index: 220;
}
.sstat-dialog {
  position: fixed;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  width: 400px;
  max-width: 92vw;
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  z-index: 221;
  overflow: hidden;
}
.sstat-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-4) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.sstat-title { flex: 1; font-size: var(--text-md); color: var(--color-text-primary); }
/* 「本轮用量」标题右侧的总量：截图里 2,862,497 tok 与标题同一行、右对齐。 */
.sstat-head-total {
  font-size: var(--text-md);
  color: var(--color-text-primary);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.sstat-close {
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
.sstat-close:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.sstat-body { flex: 1; overflow-y: auto; padding: var(--space-4); }
.sstat-hint { margin: 0; padding: var(--space-4) 0; font-size: var(--text-sm); color: var(--color-text-muted); }
.sstat-hint.is-bad { color: var(--color-danger); }
.sstat-hero {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  padding-bottom: var(--space-3);
  border-bottom: 1px solid var(--color-border-light);
}
.sstat-hero strong { font-size: var(--text-xl); color: var(--color-text-primary); font-variant-numeric: tabular-nums; }
.sstat-hero span { font-size: var(--text-xs); color: var(--color-text-muted); }
.sstat-rows { margin: 0; padding: 0; }
.sstat-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--color-border-light);
  font-size: var(--text-sm);
}
.sstat-row dt { color: var(--color-text-muted); flex-shrink: 0; }
.sstat-row dd {
  margin: 0;
  color: var(--color-text-primary);
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
  text-align: right;
}
/* 提供方/模型名长且含斜杠，允许折行右对齐，别把弹框撑出横向滚动。 */
.sstat-row dd.sstat-provider {
  flex-shrink: 1;
  min-width: 0;
  word-break: break-all;
  font-size: var(--text-xs);
}
/* 多模型明细：会话中途换模型时列全部，模型名可折行、数值右对齐。 */
.sstat-models { margin-top: var(--space-3); }
.sstat-models-title {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  padding-bottom: var(--space-1);
}
.sstat-model-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--color-border-light);
  font-size: var(--text-sm);
}
.sstat-model-row:last-child { border-bottom: none; }
.sstat-model-name {
  color: var(--color-text-secondary);
  min-width: 0;
  word-break: break-all;
  font-size: var(--text-xs);
}
.sstat-model-num {
  color: var(--color-text-primary);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.sstat-model-num em { font-style: normal; color: var(--color-text-muted); }
.sstat-note {
  margin: var(--space-3) 0 0;
  font-size: var(--text-2xs);
  line-height: 1.6;
  color: var(--color-text-disabled);
}
</style>
