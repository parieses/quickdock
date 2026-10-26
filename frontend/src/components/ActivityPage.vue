<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, inject, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { ListActivity, ClearActivity, ClearActivityWhere } from '../../bindings/quickdock/services/appservice'
import { GetTopUsage, ClearUsage } from '../../bindings/quickdock/services/frecency/frecencyservice'
import {
  Activity, FolderKanban, Server, Puzzle, AlarmClock, ListChecks, RefreshCw, Trash2, RotateCcw,
} from '@lucide/vue'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')

// embedded：嵌在设置面板里（唯一用法）。
//
// 这个页面原本是侧边栏的一级导航，后来判断「不常用」移入设置 —— 页内大标题
// 与设置左侧菜单项重复，整页背景与设置内容区也不一致。embedded 只做两件事：
// 收掉重复标题、把内边距与背景让给外层容器，数据逻辑一行不动。
// 留这个 prop 而不是把样式写死在设置里：组件样式是 scoped 的，
// 让外层用 :deep 覆盖比在这里声明一次意图更难维护。
const props = withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false })

// FrecencyEntry 与后端 db.FrecencyEntry 同形（小写 tag，别按 PascalCase 写，
// JS 大小写敏感，字段名错一个就是整列空白）。
interface FrecencyEntry {
  key: string
  type: string
  label: string
  description: string
  input: string
  count: number
  lastUsed: number // Unix 毫秒
}

// ActivityRow 与后端 db.Activity 同形。
interface ActivityRow {
  id: string
  kind: string
  refId: string
  text: string
  createdAt: string
  createdTs: number // Unix 毫秒
}

const topUsed = ref<FrecencyEntry[]>([])
const timeline = ref<ActivityRow[]>([])
const loading = ref(false)
const error = ref('')

// ---- 筛选：来源（kind）+ 时间范围 ----
// 两条都只作用于「已加载的最近 500 条」：activity 有自动裁剪上限，
// 全量拉取没有意义，500 条足够覆盖任何人工筛选场景。
const ACTIVITY_LOAD_LIMIT = 500

const KIND_FILTERS: { value: string; labelKey: string }[] = [
  { value: '', labelKey: 'activityFilterAll' },
  { value: 'item', labelKey: 'activityFilterItem' },
  { value: 'ssh', labelKey: 'activityFilterSsh' },
  { value: 'ssh_batch', labelKey: 'activityFilterSshBatch' },
  { value: 'plugin', labelKey: 'activityFilterPlugin' },
  { value: 'schedule', labelKey: 'activityFilterSchedule' },
]
const kindFilter = ref('')

const TIME_FILTERS = [
  { value: 'today', labelKey: 'activityTimeToday', ms: 0 },
  { value: '7d', labelKey: 'activityTime7d', ms: 7 * 86400_000 },
  { value: 'all', labelKey: 'activityTimeAll', ms: 0 },
]
const timeFilter = ref('all')

// 「今天」按本地天零点起算：Unix 毫秒直接减 86400000 会得到「24 小时前」，
// 下午打开页面时把昨天下午的记录也算成「今天」。
function todayStartMs(): number {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  return d.getTime()
}

const timeSinceMs = computed(() => {
  const f = TIME_FILTERS.find(x => x.value === timeFilter.value)
  if (!f || f.value === 'all') return 0
  if (f.value === 'today') return todayStartMs()
  return Date.now() - f.ms
})

const filterActive = computed(() => kindFilter.value !== '' || timeSinceMs.value > 0)

const visibleTimeline = computed(() => {
  const since = timeSinceMs.value
  return timeline.value.filter(a =>
    (!kindFilter.value || a.kind === kindFilter.value) &&
    (!since || a.createdTs >= since))
})

// now 每分钟推进一次：相对时间（「3 分钟前」）不靠定时器刷新的话，
// 页面开着不动，显示就会一直停在打开那一刻。
const now = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | null = null

const KIND_ICON: Record<string, Component> = {
  item: FolderKanban,
  ssh: Server,
  plugin: Puzzle,
  schedule: AlarmClock,
  // 批量执行与单机连接用不同图标：同一台机器「连上去」和「往里推命令」
  // 在时间线上是两件事，图标一样会让人以为重复记了两条。
  ssh_batch: ListChecks,
}

const KIND_VERB: Record<string, string> = {
  item: 'activityKindItem',
  ssh: 'activityKindSsh',
  plugin: 'activityKindPlugin',
  schedule: 'activityKindSchedule',
  ssh_batch: 'activityKindSshBatch',
}

function ago(ts: number): string {
  if (!ts) return ''
  const diff = now.value - ts
  if (diff < 60_000) return t('activityJustNow')
  if (diff < 3_600_000) return t('activityMinAgo', { n: Math.floor(diff / 60_000) })
  if (diff < 86_400_000) return t('activityHourAgo', { n: Math.floor(diff / 3_600_000) })
  return t('activityDayAgo', { n: Math.floor(diff / 86_400_000) })
}

function verb(kind: string): string {
  const key = KIND_VERB[kind]
  return key ? t(key) : kind
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [top, acts] = await Promise.all([
      GetTopUsage(20),
      ListActivity(ACTIVITY_LOAD_LIMIT),
    ])
    topUsed.value = (unwrap<FrecencyEntry[]>(top) || []) as FrecencyEntry[]
    timeline.value = (unwrap<ActivityRow[]>(acts) || []) as ActivityRow[]
    now.value = Date.now()
  } catch (e: any) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

// 批量清除 = 清除「当前筛选结果」：筛选是用户自己划的范围，
// 清这个范围比只有「全清」一个档位有用，也比逐条勾选轻。
async function clearTimeline() {
  const scope = filterActive.value ? t('activityClearFiltered') : t('activityClearAll')
  if (toast?.confirm) {
    const ok = await toast.confirm(t('activityClearConfirm', { scope }))
    if (!ok) return
  }
  try {
    if (filterActive.value) {
      unwrap(await ClearActivityWhere(kindFilter.value, timeSinceMs.value))
    } else {
      unwrap(await ClearActivity())
    }
    // 本地同步移除被清掉的行，不必整页重拉（重闪一下也没意义）。
    // 删除条件与后端 ClearActivityWhere 一致：kind 与时间是 AND 关系。
    const since = timeSinceMs.value
    const matchesFilter = (a: ActivityRow) =>
      (!kindFilter.value || a.kind === kindFilter.value) &&
      (!since || a.createdTs >= since)
    timeline.value = timeline.value.filter(a => !matchesFilter(a))
  } catch (e: any) {
    toast?.error?.(getErrorMessage(e))
  }
}

// 重置最常用排行：清的是使用频率统计，不是任何业务数据
async function resetTopUsed() {
  if (toast?.confirm) {
    const ok = await toast.confirm(t('activityResetTopConfirm'))
    if (!ok) return
  }
  try {
    unwrap(await ClearUsage())
    topUsed.value = []
  } catch (e: any) {
    toast?.error?.(getErrorMessage(e))
  }
}

// 只读的排行榜不做到「点一下重新执行」：条目有 8 种打开策略、插件命令还要
// 额外参数（input），从一条历史记录里重建执行上下文并不可靠 —— 那属于
// 命令面板的活，这里专心做「看得见的回顾」。
const topCount = computed(() => topUsed.value.length)

onMounted(() => {
  load()
  ticker = setInterval(() => { now.value = Date.now() }, 60_000)
})
onUnmounted(() => {
  if (ticker) clearInterval(ticker)
})
</script>

<template>
  <div class="activity-page" :class="{ embedded: props.embedded }">
    <div class="qd-page-header">
      <!-- titles 插槽：嵌进设置的「历史」页时，由外层把「最近动态 / 回收站」分段控件
           放进标题位，避免再加一层工具条（两层 40px 会白吃 80px 高度） -->
      <div class="qd-page-titles">
        <slot name="titles">
          <h2 v-if="!props.embedded" class="qd-page-title">{{ t('navActivity') }}</h2>
          <span class="qd-page-sub">{{ t('activitySubtitle') }}</span>
        </slot>
      </div>
      <div class="qd-page-actions">
        <button class="qd-btn" :disabled="loading" @click="load">
          <RefreshCw :size="13" /> {{ t('logRefresh') }}
        </button>
        <button class="qd-btn" :disabled="loading || !visibleTimeline.length" @click="clearTimeline">
          <Trash2 :size="13" /> {{ filterActive ? t('activityClearFilteredBtn') : t('activityClear') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="activity-error">{{ error }}</div>

    <div class="activity-body">
      <!-- 最常用：来自 usage_frecency，和命令面板排序同一份数据 -->
      <section class="pane">
        <div class="pane-head">
          <span>{{ t('activityTopUsed') }}</span>
          <span v-if="topCount" class="pane-count">{{ topCount }}</span>
          <button v-if="topCount" class="pane-head-btn" :title="t('activityResetTop')" @click="resetTopUsed">
            <RotateCcw :size="12" />
          </button>
        </div>
        <div class="pane-body">
          <ol v-if="topUsed.length" class="used-list">
            <li v-for="(e, i) in topUsed" :key="e.key" class="used-row" :title="e.key + ' · ' + ago(e.lastUsed)">
              <span class="used-rank" :class="{ top: i < 3 }">{{ i + 1 }}</span>
              <span class="used-label">{{ e.label || e.description || e.key }}</span>
              <span class="used-count">{{ t('activityTimes', { n: e.count }) }}</span>
            </li>
          </ol>
          <p v-else class="pane-empty">{{ t('activityTopEmpty') }}</p>
        </div>
      </section>

      <!-- 操作时间线：activity 表，由打开项目 / 连主机 / 跑插件命令 / 触发任务写入 -->
      <section class="pane">
        <div class="pane-head">
          <span>{{ t('activityTimeline') }}</span>
          <span v-if="visibleTimeline.length" class="pane-count">{{ visibleTimeline.length }}</span>
        </div>
        <!-- 筛选：来源 + 时间范围。都作用于本地已加载行，即时无请求 -->
        <div class="tl-filters">
          <div class="tl-filter-row">
            <button
              v-for="f in KIND_FILTERS"
              :key="f.value"
              :class="['tl-chip', { active: kindFilter === f.value }]"
              @click="kindFilter = f.value"
            >{{ t(f.labelKey) }}</button>
          </div>
          <div class="tl-filter-row">
            <button
              v-for="f in TIME_FILTERS"
              :key="f.value"
              :class="['tl-chip', { active: timeFilter === f.value }]"
              @click="timeFilter = f.value"
            >{{ t(f.labelKey) }}</button>
          </div>
        </div>
        <div class="pane-body">
          <div v-if="visibleTimeline.length" class="tl-list">
            <div v-for="a in visibleTimeline" :key="a.id" class="tl-item">
              <span class="tl-dot" :data-kind="a.kind">
                <component :is="KIND_ICON[a.kind] || Activity" :size="13" />
              </span>
              <div class="tl-body">
                <div class="tl-line">
                  <span class="tl-verb">{{ verb(a.kind) }}</span>
                  <span class="tl-name">{{ a.text || t('activityUnknown') }}</span>
                </div>
                <div class="tl-time">{{ ago(a.createdTs) }}</div>
              </div>
            </div>
          </div>
          <p v-else-if="timeline.length" class="pane-empty">{{ t('activityFilterEmpty') }}</p>
          <p v-else class="pane-empty">{{ t('activityEmpty') }}</p>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.activity-page {
  display: flex; flex-direction: column; height: 100%; overflow: hidden;
  background: var(--color-bg-primary);
}
/* 嵌入设置面板：背景交给外层，内边距收紧一档 */
.activity-page.embedded { background: transparent; }
.activity-page.embedded .qd-page-header { padding: 0 14px; min-height: 40px; box-shadow: none; }
.activity-page.embedded .activity-body { padding: 12px 16px 16px; }
.activity-page.embedded .activity-error { margin: 8px 16px 0; }
.activity-error {
  margin: 10px 20px 0; padding: 8px 12px; border-radius: var(--radius-sm); font-size: 12px;
  color: var(--color-danger-text); background: var(--color-danger-bg); border: 1px solid var(--color-danger-border);
}

.activity-body {
  flex: 1; display: grid; grid-template-columns: minmax(240px, 340px) 1fr;
  gap: 14px; padding: 14px 20px 20px; overflow: hidden;
}
.pane {
  display: flex; flex-direction: column; overflow: hidden;
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
}
.pane-head {
  display: flex; align-items: center; gap: 6px; padding: 9px 12px; flex-shrink: 0;
  font-size: 12px; font-weight: 600; color: var(--color-text-primary);
  border-bottom: 1px solid var(--color-border);
}
.pane-count {
  margin-left: auto; font-weight: 400; font-size: 11px; padding: 0 6px;
  border-radius: 8px; background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.pane-head-btn {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; margin-left: 2px;
  border: none; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-disabled); cursor: pointer;
}
.pane-head-btn:hover { color: var(--color-accent); background: var(--color-bg-active); }
/* ---- 时间线筛选（来源 + 时间）---- */
.tl-filters {
  display: flex; flex-direction: column; gap: 4px;
  padding: 8px 10px; flex-shrink: 0;
  border-bottom: 1px solid var(--color-border);
}
.tl-filter-row { display: flex; gap: 4px; flex-wrap: wrap; }
.tl-chip {
  padding: 2px 9px; border-radius: var(--radius-md);
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); font-size: 11px; font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.tl-chip:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.tl-chip.active {
  background: var(--color-accent-bg); border-color: var(--color-accent); color: var(--color-accent);
}
.pane-body { flex: 1; overflow-y: auto; padding: 6px; }
.pane-empty { margin: 0; padding: 24px 12px; font-size: 12px; line-height: 1.6; color: var(--color-text-disabled); }

.used-list { list-style: none; margin: 0; padding: 0; }
.used-row {
  display: flex; align-items: center; gap: 8px; padding: 6px 8px;
  border-radius: var(--radius-sm); font-size: 12px; color: var(--color-text-primary);
}
.used-row:hover { background: var(--color-bg-hover); }
.used-rank {
  width: 18px; flex-shrink: 0; text-align: center; font-size: 11px;
  color: var(--color-text-disabled); font-variant-numeric: tabular-nums;
}
.used-rank.top { color: var(--color-accent); font-weight: 600; }
.used-label {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.used-count {
  flex-shrink: 0; font-size: 11px; color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

.tl-list { display: flex; flex-direction: column; }
.tl-item { display: flex; gap: 10px; padding: 6px 8px; border-radius: var(--radius-sm); }
.tl-item:hover { background: var(--color-bg-hover); }
.tl-dot {
  width: 26px; height: 26px; flex-shrink: 0; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-secondary);
}
.tl-dot[data-kind='item'] { color: var(--color-accent); border-color: var(--color-accent-border); }
.tl-dot[data-kind='ssh'] { color: var(--color-success); border-color: var(--color-success-border); }
.tl-dot[data-kind='plugin'] { color: var(--color-warning); }
.tl-dot[data-kind='schedule'] { color: var(--color-log-info); }
.tl-dot[data-kind='ssh_batch'] { color: var(--color-danger); border-color: var(--color-danger-border); }
.tl-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.tl-line { font-size: 12px; color: var(--color-text-primary); display: flex; gap: 4px; align-items: baseline; }
.tl-verb { color: var(--color-text-secondary); flex-shrink: 0; }
.tl-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tl-time { font-size: 11px; color: var(--color-text-disabled); }
</style>
