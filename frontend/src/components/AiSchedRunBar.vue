<!-- 定时任务会话顶部的运行条。

     为什么单独一条而不是塞进消息流：一条会话对应一个任务，消息是「每次运行的结果」，
     而「它现在跑到哪一步 / 一共跑过几次」属于任务级信息，跟某一条消息没有从属关系。
     放在消息区顶上，滚动到任何位置都看得见。

     为什么独立成组件：步骤文本会随事件频繁变化（每个工具调用一次），关在组件里
     只让它自己重渲染，不会带着四千行的 AIPage 一起 diff。 -->
<template>
  <div class="ai-sched-bar" :class="{ 'is-busy': busy }">
    <AlarmClock :size="13" class="ai-sched-ico" />
    <span class="ai-sched-name" :title="taskName">{{ taskName }}</span>

    <!-- 执行中：呼吸点 + 当前工具。没有步骤时仍要给出「正在执行」，
         否则点了运行之后的头几秒界面毫无反应。 -->
    <template v-if="busy">
      <span class="ai-sched-dot" />
      <span class="ai-sched-state">
        {{ step ? t('aiSchedStepRunning', { step: stepText }) : t('sched_running') }}
      </span>
    </template>
    <template v-else-if="lastRun">
      <span class="ai-sched-state" :class="lastStatus === 'fail' ? 'is-fail' : ''">
        <XCircle v-if="lastStatus === 'fail'" :size="11" />
        <CheckCircle2 v-else :size="11" />
        {{ t('aiSchedLastRun', { time: lastRun.slice(5, 16) }) }}
      </span>
    </template>
    <span v-else class="ai-sched-state">{{ t('aiSchedNeverRun') }}</span>

    <span class="ai-sched-spacer" />
    <!-- 任务已被删除时不给「运行一次」：点下去后端只会回「任务不存在」，
         这类空间留着是为了看历史，不是为了再跑 -->
    <button
      v-if="!taskGone"
      class="ai-sched-btn"
      :disabled="busy"
      :title="t('sched_run_now')"
      @click="emit('run-now')"
    >
      <Loader2 v-if="busy" :size="12" class="spin" />
      <Play v-else :size="12" />
      <span>{{ t('sched_run_now') }}</span>
    </button>
    <button class="ai-sched-btn" :title="t('sched_history')" @click="emit('open-history')">
      <History :size="12" />
      <span>{{ t('sched_history') }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlarmClock, CheckCircle2, History, Loader2, Play, XCircle } from '@lucide/vue'
import { toolVerb as aiToolVerb } from '../utils/aiTools'

const props = defineProps<{
  /** 任务名（会话标题就是它，这里再显示一次是为了让「这是什么」不依赖看侧栏） */
  taskName: string
  /** 后台正在跑（由 schedule:run-updated 收尾，见 AIPage.spaceRunBusy） */
  busy: boolean
  /** 当前步骤的工具名（schedule:run-progress 事件带过来的） */
  step: string
  /** 该工具的参数摘要（JSON 串，可能是被截断的，取 path/command 之类显示） */
  stepArgs: string
  /** 最近一次运行的时间与状态（来自任务列表，可能是旧快照） */
  lastRun: string
  lastStatus: string
  /** 归属任务已被删除：还能看历史，但不能「运行一次」 */
  taskGone: boolean
}>()

const emit = defineEmits<{ (e: 'run-now'): void; (e: 'open-history'): void }>()

const { t } = useI18n()

function verbOf(name: string): string {
  return aiToolVerb(name, t)
}

/**
 * 从工具参数里挑一个「在做什么」的落点。
 *
 * 参数串是 Go 侧截断到 200 字的 JSON，截断后可能不是合法 JSON——解析失败就
 * 返回空串，只显示动作（「读取文件」），不为了一个路径把整条状态搞成异常。
 */
const stepTarget = computed(() => {
  const raw = (props.stepArgs || '').trim()
  if (!raw) return ''
  try {
    const o = JSON.parse(raw) as Record<string, unknown>
    const v = o.path ?? o.file_path ?? o.command ?? o.query ?? o.url ?? o.pattern ?? ''
    return String(v || '').replace(/\s+/g, ' ').slice(0, 60)
  } catch {
    return ''
  }
})

/** 「读取文件 · src/foo.ts」：动作在前、落点在后；没有落点时只显示动作 */
const stepText = computed(() => {
  if (!props.step) return ''
  const verb = verbOf(props.step)
  return stepTarget.value ? `${verb} · ${stepTarget.value}` : verb
})
</script>

<style scoped>
/* 与 AiRoundBar 同款：贴在消息区顶部、一条细带，不抢正文的视觉重心 */
.ai-sched-bar {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: 5px var(--space-6);
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.ai-sched-bar.is-busy { color: var(--color-text-primary); }
.ai-sched-ico { flex-shrink: 0; color: var(--color-accent); opacity: 0.9; }

/* 任务名可以截断：它和侧栏是同一串，截断后靠 title 看全名 */
.ai-sched-name {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
  font-weight: 500;
}

.ai-sched-state {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.ai-sched-state.is-fail { color: var(--color-danger); }

.ai-sched-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--color-accent);
  flex-shrink: 0;
  animation: ai-sched-pulse 1.1s ease-in-out infinite;
}
@keyframes ai-sched-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.35; transform: scale(0.65); }
}

.ai-sched-spacer { flex: 1 1 auto; }

.ai-sched-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 22px;
  padding: 0 var(--space-4);
  border: 1px solid transparent;
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast),
    background-color var(--transition-fast);
}
.ai-sched-btn:hover:not(:disabled) {
  color: var(--color-accent);
  border-color: var(--color-border);
  background: var(--color-bg-tertiary);
}
.ai-sched-btn:disabled { opacity: var(--opacity-disabled); cursor: not-allowed; }

.spin { animation: ai-sched-spin 1s linear infinite; }
@keyframes ai-sched-spin { to { transform: rotate(360deg); } }
</style>
