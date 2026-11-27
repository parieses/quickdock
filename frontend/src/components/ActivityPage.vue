<script setup lang="ts">
import PageHeader from './PageHeader.vue'
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
  // source：这条动作是谁触发的。ui=用户自己点（含快捷键），mcp=AI 通过 MCP 工具调用。
  // 老库升级上来的历史记录统一是 ui（当时没有来源信息）。
  source: string
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

// 来源标签：两种都标出来，不靠「有没有标签」暗示。
// AI 的动作与自己的动作写在同一张表、同一种措辞里，只给 AI 打标时，
// 用户看到没标的行会默认是自己点的——恰恰可能是刚才让 AI 干的那件。
function sourceLabel(source: string): string {
  return source === 'mcp' ? t('activitySourceMcp') : t('activitySourceUi')
}

function sourceTip(source: string): string {
  return source === 'mcp' ? t('activitySourceMcpTip') : t('activitySourceUiTip')
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
    <PageHeader>
      <!-- titles 插槽：嵌进设置的「历史」页时，由外层把「最近动态 / 回收站」分段控件
           放进标题位，避免再加一层工具条（两层 40px 会白吃 80px 高度） -->
      <template #titles>
        <slot name="titles">
          <h2 v-if="!props.embedded" class="qd-page-title">{{ t('navActivity') }}</h2>
        </slot>
      </template>
      <!-- 副标题作为页头 flex 子项与标题同行（Knowledge/Netdisk 同款）；
           嵌进设置时外层已替换 titles 插槽，这里也不该重复副标题。 -->
      <span v-if="!props.embedded" class="qd-page-sub qd-page-sub-inline">{{ t('activitySubtitle') }}</span>
      <div class="qd-page-actions">
        <button class="qd-btn" :disabled="loading" @click="load">
          <RefreshCw :size="13" /> {{ t('logRefresh') }}
        </button>
        <button class="qd-btn" :disabled="loading || !visibleTimeline.length" @click="clearTimeline">
          <Trash2 :size="13" /> {{ filterActive ? t('activityClearFilteredBtn') : t('activityClear') }}
        </button>
      </div>
    </PageHeader>

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
                  <!-- 操作来源：AI 的动作用强调色，自己的用弱色小字 -->
                  <span
                    :class="['tl-src', { 'is-mcp': a.source === 'mcp' }]"
                    :title="sourceTip(a.source)"
                  >{{ sourceLabel(a.source) }}</span>
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
.activity-page.embedded .activity-body { padding: var(--space-5) var(--space-6) var(--space-6); }
.activity-page.embedded .activity-error { margin: var(--space-4) var(--space-6) 0; }
.activity-error {
  margin: 10px var(--space-7) 0; padding: var(--space-4) var(--space-5); border-radius: var(--radius-sm); font-size: var(--text-sm);
  color: var(--color-danger-text); background: var(--color-danger-bg); border: 1px solid var(--color-danger-border);
}

.activity-body {
  flex: 1; display: grid; grid-template-columns: minmax(240px, 340px) 1fr;
  gap: 14px; padding: 14px var(--space-7) var(--space-7); overflow: hidden;
}
.pane {
  display: flex; flex-direction: column; overflow: hidden;
  border: 1px solid var(--card-border-color); border-radius: var(--radius-md);
  background: var(--color-card-bg);
}
.pane-head {
  display: flex; align-items: center; gap: var(--space-3); padding: 9px var(--space-5); flex-shrink: 0;
  font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary);
  border-bottom: 1px solid var(--color-border);
}
.pane-count {
  margin-left: auto; font-weight: 400; font-size: var(--text-xs); padding: 0 var(--space-3);
  border-radius: var(--radius-md); background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.pane-head-btn {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; margin-left: var(--space-1);
  border: none; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-muted); cursor: pointer;
}
.pane-head-btn:hover { color: var(--color-accent-ink); background: var(--color-bg-active); }
/* ---- 时间线筛选（来源 + 时间）---- */
.tl-filters {
  display: flex; flex-direction: column; gap: var(--space-2);
  padding: var(--space-4) 10px; flex-shrink: 0;
  border-bottom: 1px solid var(--color-border);
}
.tl-filter-row { display: flex; gap: var(--space-2); flex-wrap: wrap; }
.tl-chip {
  padding: var(--space-1) 9px; border-radius: var(--radius-md);
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); font-size: var(--text-xs); font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.tl-chip:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.tl-chip.active {
  background: var(--color-accent-bg); border-color: var(--color-accent); color: var(--color-accent-ink);
}
.pane-body { flex: 1; overflow-y: auto; padding: var(--space-3); }
.pane-empty { margin: 0; padding: var(--space-8) var(--space-5); font-size: var(--text-sm); line-height: 1.6; color: var(--color-text-muted); }

.used-list { list-style: none; margin: 0; padding: 0; }
.used-row {
  display: flex; align-items: center; gap: var(--space-4); padding: var(--space-3) var(--space-4);
  border-radius: var(--radius-sm); font-size: var(--text-sm); color: var(--color-text-primary);
}
.used-row:hover { background: var(--color-bg-hover); }
.used-rank {
  width: 18px; flex-shrink: 0; text-align: center; font-size: var(--text-xs);
  color: var(--color-text-muted); font-variant-numeric: tabular-nums;
}
.used-rank.top { color: var(--color-accent-ink); font-weight: 600; }
.used-label {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.used-count {
  flex-shrink: 0; font-size: var(--text-xs); color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

.tl-list { display: flex; flex-direction: column; }
.tl-item { display: flex; gap: 10px; padding: var(--space-3) var(--space-4); border-radius: var(--radius-sm); }
.tl-item:hover { background: var(--color-bg-hover); }
.tl-dot {
  width: 26px; height: 26px; flex-shrink: 0; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-secondary);
}
.tl-dot[data-kind='item'] { color: var(--color-accent-ink); border-color: var(--color-accent-border); }
.tl-dot[data-kind='ssh'] { color: var(--color-success-ink); border-color: var(--color-success-border); }
.tl-dot[data-kind='plugin'] { color: var(--color-warning-ink); }
.tl-dot[data-kind='schedule'] { color: var(--color-log-info); }
.tl-dot[data-kind='ssh_batch'] { color: var(--color-danger-ink); border-color: var(--color-danger-border); }
.tl-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.tl-line { font-size: var(--text-sm); color: var(--color-text-primary); display: flex; gap: var(--space-2); align-items: baseline; }
.tl-verb { color: var(--color-text-secondary); flex-shrink: 0; }
.tl-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 来源标签：靠右固定，不参与名字的收缩（名字再长也只截名字） */
.tl-src {
  flex-shrink: 0;
  margin-left: auto;
  padding: 0 var(--space-2);
  border-radius: var(--radius-xs);
  font-size: var(--text-2xs);
  line-height: 1.5;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
}
/* AI 触发的动作用强调色：一眼能扫出「哪些不是我干的」 */
.tl-src.is-mcp { color: var(--color-accent-ink); background: var(--color-accent-bg); }
.tl-time { font-size: var(--text-xs); color: var(--color-text-muted); }
</style>
