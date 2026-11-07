<script setup lang="ts">
/**
 * 专注统计面板。数据全部来自 SaveFocusSession 落库后的两个查询接口
 * （FocusStats 汇总 / ListFocusSessions 明细），不读 localStorage、不接收业务数据 props。
 * 由 TodoPage 的「统计」入口按需 v-if 挂载：首次加载与切档都走下面的 watch(immediate)，
 * refreshToken 变化用于宿主在「完成一次专注后」让已打开的面板重新拉数。
 */
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import {
  FocusStats, ListFocusSessions,
} from '../../bindings/quickdock/services/appservice'

const props = withDefaults(defineProps<{ refreshToken?: number }>(), { refreshToken: 0 })

const { t } = useI18n()
const toast = useToast()

interface TodoStat { todoId: string; title: string; minutes: number; sessions: number }
interface DayStat { day: string; minutes: number; sessions: number }
interface StatsData {
  sessions: number; minutes: number; byTodo: TodoStat[]; daily: DayStat[]
}
interface FocusRecord {
  id: string; todoId: string; title: string; minutes: number
  startedAt: string; endedAt: string; done: boolean; createdAt: string
}

type RangeKey = 'week' | 'month' | 'all'
// days 是传给 FocusStats 的天数；「全部」用校验上限 3650（约 10 年）近似，
// 明细则传空区间让后端不过滤，务必覆盖到比 10 年更早的记录。
const RANGES: { key: RangeKey; days: number; label: string }[] = [
  { key: 'week', days: 7, label: 'focusStatsWeek' },
  { key: 'month', days: 30, label: 'focusStatsMonth' },
  { key: 'all', days: 3650, label: 'focusStatsAll' },
]

const range = ref<RangeKey>('week')
const loading = ref(false)
const stats = ref<StatsData>({ sessions: 0, minutes: 0, byTodo: [], daily: [] })
const records = ref<FocusRecord[]>([])

const hasData = computed(() => stats.value.sessions > 0 || records.value.length > 0)

// SQLite 对 ±HH:MM 时区后缀的支持早于 'Z'，且后端 now() 也是这个格式；
// 不用 toISOString()（Z 结尾在老版本 SQLite 可能解析成 NULL，julianday 区间比较会失效）。
function toRFC3339(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  const off = -d.getTimezoneOffset()
  const sign = off >= 0 ? '+' : '-'
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}${sign}${p(Math.floor(Math.abs(off) / 60))}:${p(Math.abs(off) % 60)}`
}

// startedAt 是带时区偏移的 RFC3339，new Date 能直接解析；只显示月日 + 时分。
function fmtRecord(s: string): string {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function emptyStats(): StatsData {
  return { sessions: 0, minutes: 0, byTodo: [], daily: [] }
}

// 快速连切档位时，只认最后一次请求的结果（照 TodoPage.refresh 的 generation 写法）。
let loadSeq = 0
async function load() {
  const seq = ++loadSeq
  loading.value = true
  const r = RANGES.find(x => x.key === range.value)!
  // 「全部」档传空区间让后端不过滤；其余按档位天数给 [from, to]。
  let from = '', to = ''
  if (r.key !== 'all') {
    from = toRFC3339(Date.now() - r.days * 86400000)
    to = toRFC3339(Date.now() + 60000)
  }
  try {
    const [statsRes, listRes] = await Promise.all([
      FocusStats(r.days),
      ListFocusSessions(from, to),
    ])
    if (seq !== loadSeq) return
    stats.value = unwrap<StatsData>(statsRes) || emptyStats()
    records.value = unwrap<FocusRecord[]>(listRes) || []
  } catch (e) {
    if (seq !== loadSeq) return
    logErr('FocusStatsPanel', e)
    toast.error(getErrorMessage(e) || t('focusLoadFailed'))
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

watch([range, () => props.refreshToken], () => { void load() }, { immediate: true })
</script>

<template>
  <div class="fsp">
    <div class="fsp-head">{{ t('focusStatsTitle') }}</div>
    <div class="fsp-tabs">
      <button v-for="r in RANGES" :key="r.key"
        :class="['fsp-tab', { active: range === r.key }]"
        @click="range = r.key">
        {{ t(r.label) }}
      </button>
    </div>

    <div v-if="loading" class="fsp-state">{{ t('loading') }}</div>
    <div v-else-if="!hasData" class="fsp-state">{{ t('focusNoRecords') }}</div>
    <template v-else>
      <div class="fsp-cards">
        <div class="fsp-card">
          <span class="fsp-card-label">{{ t('focusStatsSessions') }}</span>
          <span class="fsp-card-value">{{ stats.sessions }}</span>
        </div>
        <div class="fsp-card">
          <span class="fsp-card-label">{{ t('focusStatsMinutes') }}</span>
          <span class="fsp-card-value">{{ stats.minutes }}<em class="fsp-unit">{{ t('focusStatsMinutesUnit') }}</em></span>
        </div>
      </div>

      <div class="fsp-sec">{{ t('focusByTodo') }}</div>
      <div v-if="!stats.byTodo.length" class="fsp-sub-empty">—</div>
      <div v-for="b in stats.byTodo" :key="b.todoId" class="fsp-row">
        <span class="fsp-row-title">{{ b.title || t('focusTimerTitle') }}</span>
        <span class="fsp-row-num">{{ b.minutes }}{{ t('focusStatsMinutesUnit') }}</span>
        <span class="fsp-row-sub">×{{ b.sessions }}</span>
      </div>

      <div class="fsp-sec">{{ t('focusRecords') }}</div>
      <div v-if="!records.length" class="fsp-sub-empty">—</div>
      <div v-for="rec in records.slice(0, 20)" :key="rec.id" class="fsp-row">
        <span class="fsp-row-title">{{ rec.title || t('focusTimerTitle') }}</span>
        <span class="fsp-row-num">{{ rec.minutes }}{{ t('focusStatsMinutesUnit') }}</span>
        <span class="fsp-row-sub">{{ fmtRecord(rec.startedAt) }}</span>
      </div>
    </template>

    <div class="fsp-hint">{{ t('focusSavedHint') }}</div>
  </div>
</template>

<style scoped>
.fsp {
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  padding: var(--space-3);
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
}
.fsp-head {
  font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary);
  padding-bottom: var(--space-2); margin-bottom: var(--space-2);
  border-bottom: 1px solid var(--color-border);
}
.fsp-tabs { display: flex; gap: var(--space-1); margin-bottom: var(--space-3); }
.fsp-tab {
  flex: 1; padding: var(--space-1) var(--space-2);
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-muted); border-radius: var(--radius-sm);
  font-size: var(--text-xs); font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.fsp-tab:hover { color: var(--color-text-primary); }
.fsp-tab.active {
  background: var(--color-accent-bg); color: var(--color-accent);
  border-color: var(--color-accent-border); font-weight: 600;
}
.fsp-state { padding: var(--space-5) 0; text-align: center; color: var(--color-text-disabled); }
.fsp-cards { display: flex; gap: var(--space-2); margin-bottom: var(--space-3); }
.fsp-card {
  flex: 1; display: flex; flex-direction: column; gap: var(--space-1);
  padding: var(--space-2) var(--space-3);
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.fsp-card-label { color: var(--color-text-muted); }
.fsp-card-value {
  font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary);
  font-variant-numeric: tabular-nums;
}
.fsp-unit {
  margin-left: var(--space-1); font-size: var(--text-xs); font-weight: 400;
  font-style: normal; color: var(--color-text-muted);
}
.fsp-sec { font-weight: 600; color: var(--color-text-primary); margin: var(--space-3) 0 var(--space-1); }
.fsp-sub-empty { color: var(--color-text-disabled); padding: var(--space-1) 0; }
.fsp-row { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-1) 0; }
.fsp-row-title {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis;
  white-space: nowrap; color: var(--color-text-primary);
}
.fsp-row-num { flex-shrink: 0; color: var(--color-accent); font-variant-numeric: tabular-nums; }
.fsp-row-sub { flex-shrink: 0; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }
.fsp-hint {
  margin-top: var(--space-3); padding-top: var(--space-2);
  border-top: 1px solid var(--color-border); color: var(--color-text-disabled);
}
</style>
