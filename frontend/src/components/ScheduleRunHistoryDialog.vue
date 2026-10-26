<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { GetScheduleRuns } from '../../bindings/quickdock/services/appservice'
import { History, CheckCircle2, XCircle } from '@lucide/vue'

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

const LIMIT = 100

function fmtDuration(ms: number): string {
  if (!ms || ms < 0) return '—'
  if (ms < 1000) return ms + ' ms'
  return (ms / 1000).toFixed(2) + ' s'
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

      <div v-if="error" class="dlg-error">{{ error }}</div>

      <div class="dlg-body">
        <div v-if="loading" class="dlg-empty">{{ t('sched_history_loading') }}</div>
        <div v-else-if="runs.length === 0" class="dlg-empty">{{ t('sched_history_empty') }}</div>

        <div v-else class="run-list">
          <div v-for="r in runs" :key="r.id" class="run-row" :class="r.status">
            <div class="run-status">
              <CheckCircle2 v-if="r.status === 'ok'" :size="14" />
              <XCircle v-else :size="14" />
            </div>
            <div class="run-time">{{ r.runAt }}</div>
            <div class="run-dur">{{ fmtDuration(r.durationMs) }}</div>
            <div class="run-result" :title="r.result">{{ r.result || r.status }}</div>
          </div>
        </div>
      </div>

      <div class="dlg-foot">
        <span class="dlg-retention">{{ t('sched_history_retention') }}</span>
        <button class="btn btn-secondary" @click="close">{{ t('cancel') }}</button>
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
  background: var(--color-surface); border-radius: var(--radius-lg);
  padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.dlg-head {
  display: flex; align-items: center; gap: 10px; position: relative; margin-bottom: var(--space-4);
}
.dlg-title { display: flex; align-items: center; gap: 7px; font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.dlg-sub { font-size: 12px; color: var(--color-text-muted); max-width: 300px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dlg-close {
  position: absolute; right: -6px; top: -4px; width: 28px; height: 28px;
  border: none; background: transparent; color: var(--color-text-disabled);
  font-size: 20px; line-height: 1; cursor: pointer; border-radius: var(--radius-sm);
}
.dlg-close:hover { color: var(--color-text-primary); background: var(--color-bg-active); }

.dlg-error {
  margin-bottom: var(--space-3); padding: 8px 12px; font-size: 12px;
  color: var(--color-danger); background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border); border-radius: var(--radius-md);
}

.dlg-body { flex: 1; overflow-y: auto; min-height: 120px; }
.dlg-empty { text-align: center; padding: var(--space-8) var(--space-4); color: var(--color-text-disabled); font-size: 13px; }

.run-list { display: flex; flex-direction: column; gap: 6px; }
.run-row {
  display: grid; grid-template-columns: 18px 1fr 72px 2fr; align-items: center; gap: 10px;
  padding: 8px 10px; background: var(--color-bg-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
}
.run-status { display: flex; align-items: center; }
.run-row.ok .run-status { color: var(--color-success); }
.run-row.fail .run-status { color: var(--color-danger); }
.run-time { font-size: 12px; color: var(--color-text-secondary); white-space: nowrap; }
.run-dur { font-size: 12px; color: var(--color-text-muted); text-align: right; font-variant-numeric: tabular-nums; }
.run-result {
  font-size: 12px; color: var(--color-text-secondary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}

.dlg-foot {
  display: flex; align-items: center; justify-content: space-between;
  margin-top: var(--space-4); padding-top: var(--space-3); border-top: 1px solid var(--color-border);
}
.dlg-retention { font-size: 11px; color: var(--color-text-disabled); }
.btn-secondary {
  font-size: 12px; padding: 6px 14px;
}
</style>
