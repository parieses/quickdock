<script setup lang="ts">
/**
 * 运行轨迹里单步的详情分段：把一条步骤的 args/output/result/diff/error/text
 * 从「六段裸 <pre> 从上到下堆一屏」改成带标签的分段，点标签切一段看。
 *
 * 抽屉里三处时间线（子任务步骤 / 会话回放 / run 节点步骤）原本各写一遍同样的
 * <pre> 瀑布，改一处漏两处。这里收成一份，三处共用。
 *
 * 失败的步骤默认展开「错误」段（出错原因是最该先看到的），其余默认「结论」
 * （没有结论就退到有内容的第一段）。段内保留等宽排版，但不再一次全糊出来。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RunStep } from '../utils/aiRunTree'
import { compactPastedBlocks } from '../utils/aiPaste'

const props = defineProps<{ step: RunStep }>()
const { t } = useI18n()

interface Segment {
  key: string
  label: string
  text: string
}

/** 有内容的段才给标签；顺序固定，失败时把 error 顶到最前。 */
const segments = computed<Segment[]>(() => {
  const s = props.step
  const all: Segment[] = [
    { key: 'error', label: t('aiTraceDetailError'), text: s.error },
    { key: 'result', label: t('aiTraceDetailResult'), text: s.result },
    { key: 'args', label: t('aiTraceDetailArgs'), text: s.args },
    { key: 'output', label: t('aiTraceDetailOutput'), text: s.output },
    { key: 'diff', label: t('aiTraceDetailDiff'), text: s.diff },
    { key: 'text', label: t('aiTraceDetailText'), text: s.text },
  ]
  return all.filter((seg) => !!seg.text)
})

/** 当前选中的段 key；step 切换（列表复用 DOM）时重置到默认段。 */
const activeKey = ref('')
watch(
  () => props.step.key,
  () => { activeKey.value = defaultKey() },
  { immediate: true },
)

function defaultKey(): string {
  const segs = segments.value
  if (!segs.length) return ''
  // 失败优先看错误；否则优先结论；都没有就用第一段。
  const preferred = !props.step.ok ? 'error' : 'result'
  return segs.find((s) => s.key === preferred)?.key ?? segs[0].key
}

const active = computed(() => segments.value.find((s) => s.key === activeKey.value) ?? segments.value[0])

// 轨迹抽屉本身是 dialog（有自己的 Esc / 焦点陷阱），这里不再嵌一层弹窗；
// 粘贴长文压成一行标记即可，全文仍在消息气泡里可点开看。
function detailText(text: string): string {
  return compactPastedBlocks(text, (n) => t('aiTracePastedBlock', { n }))
}
</script>

<template>
  <div v-if="segments.length" class="step-detail">
    <div class="step-detail-tabs">
      <button
        v-for="seg in segments"
        :key="seg.key"
        class="step-detail-tab"
        :class="{ 'is-on': seg.key === active?.key, 'is-bad': seg.key === 'error' }"
        @click.stop="activeKey = seg.key"
      >
        {{ seg.label }}
      </button>
    </div>
    <pre class="step-detail-body">{{ detailText(active?.text || '') }}</pre>
  </div>
</template>

<style scoped>
.step-detail {
  margin-top: var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  overflow: hidden;
}

.step-detail-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 2px;
  padding: var(--space-1) var(--space-1) 0;
  border-bottom: 1px solid var(--color-border-light);
}

.step-detail-tab {
  border: none;
  border-bottom: 2px solid transparent;
  background: transparent;
  padding: var(--space-1) var(--space-2);
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
  cursor: pointer;
}

.step-detail-tab:hover { color: var(--color-text-primary); }
.step-detail-tab.is-on { color: var(--color-accent-text); border-bottom-color: var(--color-accent); }
.step-detail-tab.is-bad { color: var(--color-danger-ink); }
.step-detail-tab.is-bad.is-on { border-bottom-color: var(--color-danger); }

.step-detail-body {
  margin: 0;
  padding: var(--space-2);
  max-height: 240px;
  overflow: auto;
  font-size: var(--text-2xs);
  line-height: 1.5;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono);
}
</style>
