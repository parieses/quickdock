<!-- 本轮实时用时条。
     独立成组件不是为了复用，而是为了把「每 200ms 变一次的数字」关在自己的
     render 里：此前它读的是 AIPage 上的 computed，一次计时跳动就要把那个
     四千行的页面整棵树 diff 一遍，连带侧栏的会话列表一起。工具调用期间正文
     不动、只有计时在跳，那几秒里全是在为这一个数字重渲染。 -->
<template>
  <div class="ai-round-bar" :title="t('aiRoundElapsedHint')">
    <span class="ai-round-dot" />
    <span class="ai-round-text">{{ t('aiRoundElapsedStatus', { ms: formatDuration(elapsed) }) }}</span>
  </div>
</template>

<script setup lang="ts">
import { onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  /**
   * 本轮发起时刻，performance.now() 基准。
   *
   * 传时间点而不是传「已过多少毫秒」：后者要求父组件持续更新一个 prop，
   * 正是要避免的那件事。用同一个时钟基准才不会受系统时间调整影响。
   */
  startedAt: number
}>()

const { t } = useI18n()

const elapsed = ref(0)
function tick() {
  elapsed.value = Math.max(0, performance.now() - props.startedAt)
}
tick()
// 200ms 一跳：肉眼够顺滑，又不会把主线程占满（数字本身只有两位变化）。
const timer = setInterval(tick, 200)
onUnmounted(() => clearInterval(timer))

function formatDuration(ms: number): string {
  const totalSec = Math.max(0, Math.floor(ms / 1000))
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`
  if (totalSec < 60) return `${totalSec}s`
  const min = Math.floor(totalSec / 60)
  const sec = totalSec % 60
  if (min < 60) return `${min}m${sec.toString().padStart(2, '0')}s`
  const hr = Math.floor(min / 60)
  const mLeft = min % 60
  return `${hr}h${mLeft.toString().padStart(2, '0')}m`
}
</script>

<style scoped>
/* 本轮实时用时状态条：贴在消息区顶部，复刻「已处理 54s」那种实时计时感。
   左侧一个呼吸圆点表示「进行中」，右侧等宽数字跳动，避免宽度乱晃。 */
.ai-round-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  font-size: 12px;
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.ai-round-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--color-accent);
  flex-shrink: 0;
  animation: ai-round-pulse 1.1s ease-in-out infinite;
}
@keyframes ai-round-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.35; transform: scale(0.65); }
}
.ai-round-text {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
</style>
