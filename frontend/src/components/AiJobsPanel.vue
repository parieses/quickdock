<script setup lang="ts">
/**
 * 后台任务面板：列出 run_command(background=true) 起的进程。
 *
 * 和 AiDigestPanel 一样挂在父级 .ai-main 上（绝对定位），不做模态框——
 * 「刚才那个脚本跑完了吗」是要一眼看到的事，弹窗的遮罩与关闭动作用户没耐心走。
 *
 * 数据只读后端 ai_jobs 表（见 services/ai/ai_jobs.go）：面板不自己记状态，
 * 否则模型 job_status 查到的和面板显示的迟早会对不上。
 */
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { AIJobKill, AIJobLog, AIJobsList } from '../../bindings/quickdock/services/ai/aiservice'
import { unwrap } from '../utils/api'

/** 后端返回的一行（services/ai/ai_jobs.go 的 AIJobView）。 */
interface AIJobView {
  id: string
  command: string
  cwd: string
  status: string
  exitCode: number
  error: string
  logPath: string
  startedAt: string
  endedAt: string
  durationMs: number
  running: boolean
  logExists: boolean
}

const props = defineProps<{
  /** 面板是否打开；关闭时停轮询 */
  open: boolean
  /** 要定位的任务 id（从消息卡片的「查看」进来时带上）：滚动到该行并高亮 */
  focus?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  /** 任务状态变了（终止成功）：页面上的徽标与输入框上方横条要立刻跟着更新 */
  (e: 'changed'): void
}>()

const { t } = useI18n()

const jobs = ref<AIJobView[]>([])
const loading = ref(false)
const errMsg = ref('')
/** 正在终止的任务 id（禁用按钮防连点） */
const killingId = ref('')
/** 当前展开日志的任务 id，一次只看一个——同时展开几份日志会把列表挤没 */
const openLog = ref('')
const logText = ref('')
const logTruncated = ref(false)
const logBusy = ref(false)

const runningCount = computed(() => jobs.value.filter((j) => j.running).length)

/** 状态文案走静态映射而不是拼 key：i18n 检查脚本只认字面量 key。 */
const STATUS_LABEL = computed<Record<string, string>>(() => ({
  running: t('aiJobRunning'),
  done: t('aiJobDone'),
  failed: t('aiJobFailed'),
  killed: t('aiJobKilled'),
}))

let timer: ReturnType<typeof setInterval> | undefined

function stopPoll() {
  if (timer) {
    clearInterval(timer)
    timer = undefined
  }
}

/**
 * 只有还存在运行中的任务才持续轮询。
 * 全部结束后立刻停：面板开着不用也不该每 2.5 秒发一次请求。
 */
function syncPoll() {
  if (!props.open) {
    stopPoll()
    return
  }
  if (runningCount.value > 0) {
    if (!timer) timer = setInterval(() => void load(), 2500)
  } else {
    stopPoll()
  }
}

async function load() {
  loading.value = true
  try {
    const list = unwrap<AIJobView[]>(await AIJobsList()) || []
    // 运行中的排最前：列表按开始时间倒序，昨天起的那个长跑任务会被今天跑完的
    // 几条挤到下面，而用户打开面板第一眼要找的恰恰是「还在跑的那个」。
    jobs.value = list.slice().sort((a, b) => Number(b.running) - Number(a.running))
    errMsg.value = ''
  } catch (e: any) {
    errMsg.value = e?.message || String(e)
  } finally {
    loading.value = false
    syncPoll()
    void scrollToFocus()
  }
}

async function kill(j: AIJobView) {
  if (killingId.value) return
  killingId.value = j.id
  try {
    const res = unwrap<{ killed: boolean; status?: string }>(await AIJobKill(j.id))
    // 本地先翻一格再重查：后端 AIJobKill 已经等到「已终止」落库才返回，
    // 这一手只是让按钮的反馈不依赖下面那次重新查询的往返——不然点完要愣一下。
    const i = jobs.value.findIndex((x) => x.id === j.id)
    if (i >= 0) {
      jobs.value[i] = { ...jobs.value[i], running: false, status: res?.status || 'killed' }
    }
    emit('changed')
    await load()
  } catch (e: any) {
    errMsg.value = e?.message || String(e)
  } finally {
    killingId.value = ''
  }
}

async function toggleLog(j: AIJobView) {
  if (openLog.value === j.id) {
    openLog.value = ''
    return
  }
  openLog.value = j.id
  logBusy.value = true
  logText.value = ''
  logTruncated.value = false
  try {
    const res = unwrap<{ output: string; truncated: boolean }>(await AIJobLog(j.id))
    logText.value = res?.output || ''
    logTruncated.value = !!res?.truncated
  } catch (e: any) {
    logText.value = e?.message || String(e)
  } finally {
    logBusy.value = false
  }
}

// —— 定位到指定任务 ——
// 从消息卡片的「查看」进来时，列表里可能有几十条，用户还得自己找那一条。
const rowEls = new Map<string, HTMLElement>()
// 每次 focus 变化只自动滚一次：轮询每 2.5s 重画一次列表，次次都滚会把用户
// 手动滚到的位置拽回去。
let focusScrolled = false

function setRowEl(id: string, el: unknown) {
  if (el instanceof HTMLElement) rowEls.set(id, el)
  else rowEls.delete(id)
}

async function scrollToFocus() {
  if (!props.focus || focusScrolled) return
  await nextTick()
  const el = rowEls.get(props.focus)
  if (!el) return
  focusScrolled = true
  el.scrollIntoView({ block: 'nearest' })
}

watch(
  () => props.focus,
  () => {
    focusScrolled = false
    void scrollToFocus()
  },
)

/** 毫秒 → 8s / 3m12s / 1h04m。运行中的时长靠轮询刷新，不需要秒级跳动。 */
function fmtDur(ms: number): string {  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

/** 目录只留尾部两段：面板窄，完整路径靠 title 悬停看。 */
function shortCwd(p: string): string {
  if (!p) return ''
  const parts = p.split(/[\\/]+/).filter(Boolean)
  return parts.length <= 2 ? p : '…/' + parts.slice(-2).join('/')
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      void load()
    } else {
      stopPoll()
      openLog.value = ''
    }
  },
  { immediate: true },
)

onUnmounted(stopPoll)
</script>

<template>
  <div class="ai-jobs">
    <div class="ai-jobs-head">
      <span class="ai-jobs-title">
        {{ t('aiJobsTitle') }}
        <span v-if="runningCount" class="ai-jobs-count">{{ runningCount }}</span>
      </span>
      <button class="ai-jobs-x" :title="t('close')" @click="emit('close')"><X :size="12" /></button>
    </div>

    <div class="ai-jobs-list">
      <div v-for="j in jobs" :key="j.id" class="ai-job" :class="[`is-${j.status}`, { 'is-focus': j.id === focus }]" :ref="(el) => setRowEl(j.id, el)">
        <div class="ai-job-main">
          <span class="ai-job-dot" />
          <div class="ai-job-info">
            <div class="ai-job-cmd" :title="j.command">{{ j.command }}</div>
            <div class="ai-job-meta">
              <span class="ai-job-status">{{ STATUS_LABEL[j.status] || j.status }}</span>
              <span class="ai-job-time">{{ fmtDur(j.durationMs) }}</span>
              <span class="ai-job-cwd" :title="j.cwd">{{ shortCwd(j.cwd) }}</span>
            </div>
          </div>
          <div class="ai-job-acts">
            <button
              v-if="j.logExists"
              class="ai-job-act"
              :disabled="logBusy && openLog === j.id"
              @click="toggleLog(j)"
            >
              {{ openLog === j.id ? t('aiJobHideLog') : t('aiJobViewLog') }}
            </button>
            <button v-if="j.running" class="ai-job-act is-danger" :disabled="killingId === j.id" @click="kill(j)">
              {{ killingId === j.id ? t('aiJobKilling') : t('aiJobKill') }}
            </button>
          </div>
        </div>
        <div v-if="j.error" class="ai-job-err">{{ j.error }}</div>
        <pre v-if="openLog === j.id" class="ai-job-log">{{ logBusy ? t('aiJobLogLoading') : (logText || t('aiJobLogEmpty')) }}</pre>
        <div v-if="openLog === j.id && logTruncated" class="ai-job-trunc">{{ t('aiJobLogTail') }}</div>
      </div>

      <div v-if="!jobs.length && !loading" class="ai-jobs-empty">{{ t('aiJobsEmpty') }}</div>
      <div v-if="errMsg" class="ai-jobs-empty is-err">{{ errMsg }}</div>
    </div>

    <p class="ai-jobs-hint">{{ t('aiJobsHint') }}</p>
  </div>
</template>

<style scoped>
.ai-jobs {
  position: absolute;
  top: 52px;
  right: 16px;
  z-index: 20;
  width: 400px;
  max-width: calc(100% - 32px);
  max-height: 70vh;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg, 0 12px 32px rgba(0, 0, 0, 0.28));
  overflow: hidden;
}

.ai-jobs-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-bottom: 1px solid var(--color-border);
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text-secondary);
}

.ai-jobs-title {
  display: flex;
  align-items: center;
  gap: 6px;
}

/* 运行中数量做成小圆点而不是「(N)」：一眼看到「还有东西在跑」就够，不用读数字 */
.ai-jobs-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 15px;
  height: 15px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}

.ai-jobs-x {
  background: none;
  border: none;
  color: var(--color-text-muted);
  cursor: pointer;
  display: flex;
  padding: 2px;
  border-radius: var(--radius-sm);
}

.ai-jobs-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.ai-jobs-list {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 4px;
}

.ai-job {
  padding: 6px 6px 7px;
  border-radius: var(--radius-sm);
}

.ai-job:hover {
  background: var(--color-bg-hover);
}

/* 从消息卡片的「查看」点进来的那一行：只加底色，不画描边——
   面板里每一行本来就靠状态点区分，再加一圈框会和「运行中」的语义打架。 */
.ai-job.is-focus {
  background: var(--color-bg-hover);
}

.ai-job-main {
  display: flex;
  align-items: flex-start;
  gap: 7px;
}

/* 状态点：与消息列表的运行点同一套语义（呼吸 / 绿 / 红 / 灰） */
.ai-job-dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  margin-top: 5px;
  border-radius: 50%;
  background: var(--color-text-disabled);
}

.ai-job.is-running .ai-job-dot {
  background: var(--color-accent);
  animation: ai-job-pulse 1.6s ease-in-out infinite;
}

.ai-job.is-done .ai-job-dot {
  background: var(--color-success, #3fb950);
}

.ai-job.is-failed .ai-job-dot {
  background: var(--color-danger);
}

.ai-job.is-killed .ai-job-dot {
  background: var(--color-text-disabled);
}

@keyframes ai-job-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.3;
  }
}

.ai-job-info {
  flex: 1 1 auto;
  min-width: 0;
}

.ai-job-cmd {
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-job-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
  font-size: 10.5px;
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.ai-job.is-failed .ai-job-status {
  color: var(--color-danger);
}

.ai-job-cwd {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  opacity: 0.85;
}

.ai-job-acts {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 2px;
  margin-top: -1px;
}

.ai-job-act {
  padding: 1px 6px;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  white-space: nowrap;
}

.ai-job-act:hover {
  color: var(--color-accent);
  background: var(--color-bg-active);
}

.ai-job-act:disabled {
  opacity: 0.5;
  cursor: default;
}

.ai-job-act.is-danger:hover {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.ai-job-err {
  margin: 4px 0 0 13px;
  font-size: 11px;
  color: var(--color-danger);
  word-break: break-word;
}

/* 日志贴在行下、与命令文字对齐（缩进 = 点 6 + gap 7） */
.ai-job-log {
  margin: 5px 0 0 13px;
  padding: 6px 8px;
  max-height: 220px;
  overflow: auto;
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}

.ai-job-trunc {
  margin: 3px 0 0 13px;
  font-size: 10.5px;
  color: var(--color-text-disabled);
}

.ai-jobs-empty {
  padding: 14px 8px;
  text-align: center;
  font-size: 11.5px;
  color: var(--color-text-disabled);
}

.ai-jobs-empty.is-err {
  color: var(--color-danger);
}

.ai-jobs-hint {
  margin: 0;
  padding: 7px 10px;
  border-top: 1px solid var(--color-border);
  font-size: 10.5px;
  line-height: 1.45;
  color: var(--color-text-disabled);
}
</style>
