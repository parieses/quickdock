<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, inject, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { ListActivity, ClearActivity } from '../../bindings/quickdock/services/appservice'
import { GetTopUsage } from '../../bindings/quickdock/services/frecency/frecencyservice'
import {
  Activity, FolderKanban, Server, Puzzle, AlarmClock, ListChecks, RefreshCw, Trash2,
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
      ListActivity(200),
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

async function clearTimeline() {
  if (toast?.confirm) {
    const ok = await toast.confirm(t('activityClearConfirm'))
    if (!ok) return
  }
  try {
    unwrap(await ClearActivity())
    timeline.value = []
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
    <div class="activity-header">
      <div class="activity-title-wrap">
        <h2 v-if="!props.embedded" class="activity-title">{{ t('navActivity') }}</h2>
        <span class="activity-sub">{{ t('activitySubtitle') }}</span>
      </div>
      <button class="act-btn" :disabled="loading" @click="load">
        <RefreshCw :size="13" /> {{ t('logRefresh') }}
      </button>
      <button class="act-btn" :disabled="loading || !timeline.length" @click="clearTimeline">
        <Trash2 :size="13" /> {{ t('activityClear') }}
      </button>
    </div>

    <div v-if="error" class="activity-error">{{ error }}</div>

    <div class="activity-body">
      <!-- 最常用：来自 usage_frecency，和命令面板排序同一份数据 -->
      <section class="pane">
        <div class="pane-head">
          <span>{{ t('activityTopUsed') }}</span>
          <span v-if="topCount" class="pane-count">{{ topCount }}</span>
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
          <span v-if="timeline.length" class="pane-count">{{ timeline.length }}</span>
        </div>
        <div class="pane-body">
          <div v-if="timeline.length" class="tl-list">
            <div v-for="a in timeline" :key="a.id" class="tl-item">
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
.activity-page.embedded .activity-header { padding: 12px 16px; }
.activity-page.embedded .activity-sub { font-size: 12px; }
.activity-page.embedded .activity-body { padding: 12px 16px 16px; }
.activity-page.embedded .activity-error { margin: 8px 16px 0; }
.activity-header {
  display: flex; align-items: center; gap: 10px;
  padding: 14px 20px; border-bottom: 1px solid var(--color-border); flex-shrink: 0;
}
.activity-title-wrap { display: flex; flex-direction: column; gap: 2px; flex: 1; min-width: 0; }
.activity-title { margin: 0; font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.activity-sub { font-size: 12px; color: var(--color-text-secondary); }
.act-btn {
  display: flex; align-items: center; gap: 4px; font-size: 12px; padding: 4px 9px;
  border-radius: var(--radius-sm); cursor: pointer;
  border: 1px solid var(--color-border); background: var(--color-bg-secondary); color: var(--color-text-primary);
}
.act-btn:hover:not(:disabled) { background: var(--color-bg-hover); }
.act-btn:disabled { opacity: 0.45; cursor: default; }
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
