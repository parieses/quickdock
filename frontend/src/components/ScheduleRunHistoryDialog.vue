<script setup lang="ts">
import { computed, ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { GetScheduleRuns } from '../../bindings/quickdock/services/appservice'
import { downloadCsv } from '../utils/csv'
import { History, CheckCircle2, XCircle, Download, Ban } from '@lucide/vue'

const props = defineProps<{
  visible: boolean
  task: { id: string; name: string } | null
}>()
const emit = defineEmits<{ (e: 'cancel'): void }>()

const { t } = useI18n()

interface RunItem {
  id: string
  taskId: string
  taskName: string
  runAt: string
  status: string
  result: string
  durationMs: number
  createdTs: number
}

const runs = ref<RunItem[]>([])
const loading = ref(false)
const error = ref('')

// 后端按时间倒序返回，limit 是上限。100 条对「每分钟跑一次」的任务只够一小时半，
// 排查一次偶发失败常常要翻更早的记录 —— 提到 500，真正的收敛仍由后端的
// 保留策略负责（不会因为前端要得多就无限增长）。
const LIMIT = 500

/** 只看失败：排查时最常用的视角，省得在几百条 ok 里找红点。
 *  skipped（关机错过 / 未到生效期 / 已过有效期）不是执行失败，不计入。 */
const onlyFail = ref(false)

const failed = computed(() => runs.value.filter((r) => r.status === 'fail'))

const shown = computed(() => (onlyFail.value ? failed.value : runs.value))

/** 导出状态列：skipped 原文对看表的人不友好，翻成与列表一致的文案。 */
function statusLabel(s: string): string {
  return s === 'skipped' ? t('sched_history_status_skipped') : s
}

function exportCsv() {
  const head = [t('sched_history_col_time'), t('sched_history_col_status'), t('sched_history_col_duration'), t('sched_history_col_result')]
  const rows = shown.value.map((r) => [r.runAt, statusLabel(r.status), r.durationMs || 0, r.result || ''])
  const name = (props.task?.name || 'schedule').replace(/[\\/:*?"<>|]/g, '_')
  downloadCsv(`schedule-${name}-${new Date().toISOString().slice(0, 10)}.csv`, [head, ...rows])
}

function fmtDuration(ms: number): string {
  if (!ms || ms < 0) return '—'
  if (ms < 1000) return ms + ' ms'
  return (ms / 1000).toFixed(2) + ' s'
}

/** AI 动作的执行结果串形如 '会话 <convID> · <摘要>'，能提取出 convID 就给个跳转入口。 */
function convIdOf(result: string): string {
  const m = /会话 ([a-zA-Z0-9_-]+)/.exec(result || '')
  return m ? m[1] : ''
}

function openConv(id: string) {
  // 只发事件，页面跳转由 AIPage.vue 监听；事件通道未就绪时静默，不阻塞查看历史
  try { Events.Emit('ai:open-conversation', { id }) } catch { /* 事件通道未就绪时静默 */ }
}

/** skipped 行的结果串是跳过原因，前面补一个状态文案才看得出是「被跳过」而非失败。 */
function resultText(r: RunItem): string {
  if (r.status === 'skipped') return t('sched_history_status_skipped') + (r.result ? ' · ' + r.result : '')
  return r.result || r.status
}

async function load() {
  if (!props.task) return
  loading.value = true
  error.value = ''
  runs.value = []
  try {
    const r = unwrap<RunItem[]>(await GetScheduleRuns(props.task.id, LIMIT)) ?? []
    runs.value = r
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

// 打开弹窗且任务就绪时加载；切换任务也重新拉取
watch(
  () => [props.visible, props.task?.id],
  ([v]) => {
    if (v) load()
  },
  { immediate: true },
)

// 弹窗开着时后台跑完一轮也要立刻冒出来。异步 AI 任务最长十分钟，
// 用户往往是「点完运行 → 打开历史等着看」，不监听就永远停在打开那一刻的快照。
// 只在本弹窗对应任务有更新时重载，别的任务跑完不打扰。
let offRunUpdated: (() => void) | null = null
onMounted(() => {
  offRunUpdated = Events.On('schedule:run-updated', (ev: any) => {
    const p = (ev?.data ?? ev) as { taskId?: string }
    if (!props.visible || !props.task || p?.taskId !== props.task.id) return
    void load()
  })
})
onBeforeUnmount(() => { offRunUpdated?.(); offRunUpdated = null })

function close() {
  emit('cancel')
}
</script>

<template>
  <div v-if="visible" class="mask" @click.self="close">
    <div class="dialog">
      <div class="dlg-head">
        <div class="dlg-title">
          <History :size="16" />
          <span>{{ t('sched_history_title') }}</span>
        </div>
        <div v-if="task" class="dlg-sub">{{ task.name }}</div>
        <button class="dlg-close" @click="close" title="×">×</button>
      </div>

      <!-- 筛选 / 导出：历史原本只有一坨按时间的流水，既挑不出失败也带不走 -->
      <div class="dlg-tools">
        <button :class="['qd-btn', 'qd-btn-sm', { 'is-on': !onlyFail }]" @click="onlyFail = false">
          {{ t('sched_filter_all') }} ({{ runs.length }})
        </button>
        <button :class="['qd-btn', 'qd-btn-sm', { 'is-on': onlyFail }]" @click="onlyFail = true">
          {{ t('sched_filter_fail') }} ({{ failed.length }})
        </button>
        <button class="qd-btn qd-btn-sm dlg-export" :disabled="!shown.length" @click="exportCsv">
          <Download :size="12" /> {{ t('sched_history_export') }}
        </button>
      </div>

      <div v-if="error" class="dlg-error">{{ error }}</div>

      <div class="dlg-body">
        <div v-if="loading" class="dlg-empty">{{ t('sched_history_loading') }}</div>
        <div v-else-if="runs.length === 0" class="dlg-empty">{{ t('sched_history_empty') }}</div>
        <div v-else-if="shown.length === 0" class="dlg-empty">{{ t('sched_history_no_fail') }}</div>

        <div v-else class="run-list">
          <div v-for="r in shown" :key="r.id" class="run-row" :class="r.status">
            <div class="run-status">
              <CheckCircle2 v-if="r.status === 'ok'" :size="14" />
              <Ban v-else-if="r.status === 'skipped'" :size="14" />
              <XCircle v-else :size="14" />
            </div>
            <div class="run-time">{{ r.runAt }}</div>
            <div class="run-dur">{{ fmtDuration(r.durationMs) }}</div>
            <div class="run-result">
              <span class="run-result-text" :title="r.result">{{ resultText(r) }}</span>
              <button v-if="convIdOf(r.result)" class="qd-btn qd-btn-sm run-open-conv"
                      @click="openConv(convIdOf(r.result))">
                {{ t('sched_ai_open_conv') }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="dlg-foot">
        <span class="dlg-retention">{{ t('sched_history_retention') }}</span>
        <button class="qd-btn" @click="close">{{ t('cancel') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask {
  position: fixed; inset: 0; background: var(--color-bg-overlay);
  display: flex; align-items: center; justify-content: center; z-index: 200;
}
.dialog {
  width: 460px; max-width: 92vw; max-height: 80vh; display: flex; flex-direction: column;
  background: var(--color-popover-bg); border-radius: var(--radius-lg);
  padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.dlg-head {
  display: flex; align-items: center; gap: 10px; position: relative; margin-bottom: var(--space-4);
}
.dlg-title { display: flex; align-items: center; gap: 7px; font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary); }
.dlg-sub { font-size: var(--text-sm); color: var(--color-text-muted); max-width: 300px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dlg-close {
  position: absolute; right: -6px; top: -4px; width: 28px; height: 28px;
  border: none; background: transparent; color: var(--color-text-muted);
  font-size: var(--text-xl); line-height: 1; cursor: pointer; border-radius: var(--radius-sm);
}
.dlg-close:hover { color: var(--color-text-primary); background: var(--color-bg-active); }

.dlg-error {
  margin-bottom: var(--space-3); padding: var(--space-4) var(--space-5); font-size: var(--text-sm);
  color: var(--color-danger); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md);
}

.dlg-body { flex: 1; overflow-y: auto; min-height: 120px; }
.dlg-empty { text-align: center; padding: var(--space-8) var(--space-4); color: var(--color-text-muted); font-size: var(--text-md); }

.run-list { display: flex; flex-direction: column; gap: var(--space-3); }
.run-row {
  display: grid; grid-template-columns: 18px 1fr 72px 2fr; align-items: center; gap: 10px;
  padding: var(--space-4) 10px; background: var(--color-card-bg);
  border: 1px solid var(--card-border-color); border-radius: var(--radius-md);
}
.run-status { display: flex; align-items: center; }
.run-row.ok .run-status { color: var(--color-success); }
.run-row.fail .run-status { color: var(--color-danger); }
.run-row.skipped .run-status { color: var(--color-text-muted); }
.run-time { font-size: var(--text-sm); color: var(--color-text-secondary); white-space: nowrap; }
.run-dur { font-size: var(--text-sm); color: var(--color-text-muted); text-align: right; font-variant-numeric: tabular-nums; }
.run-result {
  font-size: var(--text-sm); color: var(--color-text-secondary);
  display: flex; align-items: center; gap: var(--space-2); min-width: 0;
}
.run-result-text {
  flex: 1 1 auto; min-width: 0;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.run-open-conv { flex-shrink: 0; }

.dlg-foot {
  display: flex; align-items: center; justify-content: space-between;
  margin-top: var(--space-4); padding-top: var(--space-3); border-top: 1px solid var(--color-border);
}
.dlg-retention { font-size: var(--text-xs); color: var(--color-text-muted); }
/* 工具条与头部之间留白；导出按钮靠右 */
.dlg-tools { display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-3); }
.dlg-export { margin-left: auto; }
</style>
