<!-- 后台任务横条：贴在输入框正上方。
     从 AIPage 拆出来不是为复用，而是把 1Hz 的计时关在自己的 render 里：
     此前横条内容是 AIPage 上的 computed，依赖一个每秒写一次的 nowTick，
     只要后台有任务在跑，那个四千行的页面（连会话侧栏、消息列表的 props）
     每秒都要整棵 diff 一遍，只为更新这一行「已运行 3m12s」。
     父组件只传运行中的任务数组，计时、格式化、启停全在这里。 -->
<template>
  <button v-if="first" class="ai-jobs-bar" :title="t('aiJobsBarTip')" @click="emit('toggle')">
    <span class="ai-jobs-bar-dot" />
    <span class="ai-jobs-bar-label">{{ t('aiJobsBarRunning') }}</span>
    <code class="ai-jobs-bar-cmd" :title="first.command">{{ first.command }}</code>
    <span class="ai-jobs-bar-meta">
      {{ elapsed }}<template v-if="props.jobs.length > 1"> · {{ t('aiJobsBarMore', { n: props.jobs.length - 1 }) }}</template>
    </span>
  </button>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AIJobRow } from '../types/ai'

const props = defineProps<{
  /** 运行中的后台任务，由父页每次徽标轮询整份替换 */
  jobs: AIJobRow[]
  /** 父页是否在 KeepAlive 中处于激活态；切走时把计时一起停掉 */
  active: boolean
}>()
const emit = defineEmits<{
  (e: 'toggle'): void
}>()
const { t } = useI18n()

const now = ref(Date.now())
const first = computed(() => props.jobs[0])
const elapsed = computed(() => {
  const started = Date.parse(first.value?.startedAt ?? '') || now.value
  return formatDuration(Math.max(0, now.value - started))
})

let timer: ReturnType<typeof setInterval> | undefined
function startTimer() {
  if (timer) return
  now.value = Date.now()
  timer = setInterval(() => { now.value = Date.now() }, 1000)
}
function stopTimer() {
  if (timer) { clearInterval(timer); timer = undefined }
}

// 只看长度不看数组引用：父页每 3s 会整份替换 jobs，引用变了但任务还在跑，
// 不该因此反复重启计时。第一个任务换了 startedAt 时 elapsed 会自己重算。
// active 直接取父页的 pageActive，不依赖 KeepAlive 的 activated 钩子往后代传播。
watch([() => props.jobs.length, () => props.active], ([n, act]) => {
  if (n > 0 && act) startTimer()
  else stopTimer()
}, { immediate: true })

onUnmounted(stopTimer)

// 与 AiRoundBar / AIPage 的 formatDuration 同一口径：
// <1s 精确毫秒，<1min 秒，<1h 分:秒，否则 时:分。
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
/* 与输入框同宽同左边（--ai-column-w + margin auto），
   一条浅底的行内按钮，不加边框——它是状态提示，不是一张卡片。
   高度刻意压到与等待插入那条一致，两条同时出现时不会一厚一薄。 */
.ai-jobs-bar {
  display: flex;
  align-items: center;
  gap: 7px;
  width: var(--ai-column-w);
  min-width: 0;
  margin: 0 auto 6px;
  padding: 5px 10px;
  border: none;
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  font-family: inherit;
  font-size: 12px;
  color: var(--color-text-secondary);
  text-align: left;
  cursor: pointer;
}

.ai-jobs-bar:hover {
  background: var(--color-bg-hover);
}

/* 呼吸点：静态圆点会被读成「一个图标」，这一条要说的是「此刻还在跑」 */
.ai-jobs-bar-dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-accent);
  animation: ai-jobs-bar-pulse 1.2s ease-in-out infinite;
}

@keyframes ai-jobs-bar-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.25; }
}

.ai-jobs-bar-label {
  flex: 0 0 auto;
  color: var(--color-text-secondary);
}

/* 命令是这一条里唯一要认的东西：等宽、提亮、可截断（完整命令看 title） */
.ai-jobs-bar-cmd {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-primary);
}

.ai-jobs-bar-meta {
  flex: 0 0 auto;
  margin-left: auto;
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
</style>
