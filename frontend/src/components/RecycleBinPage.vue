<script setup lang="ts">
import { ref, computed, onMounted, inject, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useWorkspaceStore } from '../stores/workspace'
import {
  ListRecycle, RestoreRecycle, PurgeRecycle, ClearRecycle,
} from '../../bindings/quickdock/services/recycle/recycleservice'
import { Boxes, LayoutGrid, Layers, Bookmark, FileText, RefreshCw, Trash2, Undo2, Timer, MessageSquare, Folder } from '@lucide/vue'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')
const store = useWorkspaceStore()

// embedded：嵌在设置的「历史」页里（与 ActivityPage 同款契约）。
// 页头标题与副标题由外层分段控件承担，这里只保留操作按钮（刷新 / 清空）。
const props = withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false })

// 与后端 db.Tombstone 同形（小写 tag —— 字段名错一个是整列空白）。
interface RecycleEntry {
  collection: string
  id: string
  label: string
  deletedAt: string
}

const entries = ref<RecycleEntry[]>([])
const loading = ref(false)
const error = ref('')
const busyKey = ref('') // 正在恢复/删除的那一条，避免整列表按钮同时禁用

// 类型筛选：删除的东西一多，最想先找的是「我刚才删的那个」，
// 按类型切一刀比在几十条里翻快。
const TYPE_FILTERS: { value: string; labelKey: string }[] = [
  { value: '', labelKey: 'recycleTypeAll' },
  { value: 'workspaces', labelKey: 'recycleTypeWorkspace' },
  { value: 'scenes', labelKey: 'recycleTypeScene' },
  { value: 'collections', labelKey: 'recycleTypeCollection' },
  { value: 'items', labelKey: 'recycleTypeItem' },
  { value: 'notes', labelKey: 'recycleTypeNote' },
  { value: 'schedules', labelKey: 'recycleTypeSchedule' },
  { value: 'ai_tasks', labelKey: 'recycleTypeAITask' },
  { value: 'ai_spaces', labelKey: 'recycleTypeAISpace' },
]
const typeFilter = ref('')

const TYPE_ICON: Record<string, Component> = {
  workspaces: Boxes,
  scenes: LayoutGrid,
  collections: Layers,
  items: Bookmark,
  notes: FileText,
  schedules: Timer,
  // AI 任务与空间：空间带目录（文件夹图标），任务是单条对话。
  ai_tasks: MessageSquare,
  ai_spaces: Folder,
}

const TYPE_LABEL: Record<string, string> = {
  workspaces: 'recycleTypeWorkspace',
  scenes: 'recycleTypeScene',
  collections: 'recycleTypeCollection',
  items: 'recycleTypeItem',
  notes: 'recycleTypeNote',
  schedules: 'recycleTypeSchedule',
  ai_tasks: 'recycleTypeAITask',
  ai_spaces: 'recycleTypeAISpace',
}

// 只显示出现在筛选里的类型，避免假选项（某类型一条都没有时点进去是空页）
const visibleFilters = computed(() => {
  const present = new Set(entries.value.map(e => e.collection))
  return TYPE_FILTERS.filter(f => f.value === '' || present.has(f.value))
})

const visibleEntries = computed(() =>
  typeFilter.value ? entries.value.filter(e => e.collection === typeFilter.value) : entries.value)

function typeLabel(c: string): string {
  const key = TYPE_LABEL[c]
  return key ? t(key) : c
}

function keyOf(e: RecycleEntry): string {
  return e.collection + '/' + e.id
}

function fmtTime(iso: string): string {
  if (!iso) return ''
  // 后端存的是 RFC3339（本地时区偏移已带上），直接交给本地化格式
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    entries.value = (unwrap<RecycleEntry[]>(await ListRecycle()) || []) as RecycleEntry[]
  } catch (e: any) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

// 恢复后要让主界面立刻能看到回来的数据：只刷新「当前打开的那条链路」，
// 不动工作空间选择状态（恢复不该把用户当前视野踢走）。
async function refreshWorkspace() {
  try {
    if (store.activeWorkspaceId) await store.fetchScenes(store.activeWorkspaceId)
    if (store.activeSceneId) await store.fetchCollections(store.activeSceneId)
    if (store.activeCollectionId) await store.fetchItems(store.activeCollectionId)
    // 恢复的可能是一个被整个删掉的工作空间 → 列表本身也要重取
    await store.fetchWorkspaces()
  } catch { /* 刷新失败不影响恢复结果，下次进入自动重载 */ }
}

async function restore(e: RecycleEntry) {
  const k = keyOf(e)
  if (busyKey.value) return
  busyKey.value = k
  try {
    unwrap(await RestoreRecycle(e.collection, e.id))
    entries.value = entries.value.filter(x => keyOf(x) !== k)
    toast?.success?.(t('recycleRestored', { name: e.label || typeLabel(e.collection) }))
    await refreshWorkspace()
  } catch (err: any) {
    toast?.error?.(getErrorMessage(err))
    await load()
  } finally {
    busyKey.value = ''
  }
}

async function purge(e: RecycleEntry) {
  const k = keyOf(e)
  if (busyKey.value) return
  if (toast?.confirm) {
    const ok = await toast.confirm(t('recyclePurgeConfirm', { name: e.label || typeLabel(e.collection) }))
    if (!ok) return
  }
  busyKey.value = k
  try {
    unwrap(await PurgeRecycle(e.collection, e.id))
    entries.value = entries.value.filter(x => keyOf(x) !== k)
  } catch (err: any) {
    toast?.error?.(getErrorMessage(err))
  } finally {
    busyKey.value = ''
  }
}

async function clearAll() {
  if (toast?.confirm) {
    const ok = await toast.confirm(t('recycleClearConfirm', { n: entries.value.length }))
    if (!ok) return
  }
  try {
    unwrap(await ClearRecycle())
    entries.value = []
  } catch (e: any) {
    toast?.error?.(getErrorMessage(e))
  }
}

onMounted(load)
</script>

<template>
  <div class="recycle-page" :class="{ embedded: props.embedded }">
    <div class="qd-page-header">
      <!-- titles 插槽：见 ActivityPage 同名插槽（同一套嵌入契约） -->
      <div class="qd-page-titles">
        <slot name="titles">
          <h2 v-if="!props.embedded" class="qd-page-title">{{ t('recycleBin') }}</h2>
          <span v-if="!props.embedded" class="qd-page-sub">{{ t('recycleSubtitle') }}</span>
        </slot>
      </div>
      <div class="qd-page-actions">
        <button class="qd-btn" :disabled="loading" @click="load">
          <RefreshCw :size="13" /> {{ t('logRefresh') }}
        </button>
        <button class="qd-btn" :disabled="loading || !entries.length" @click="clearAll">
          <Trash2 :size="13" /> {{ t('recycleClear') }}
        </button>
      </div>
    </div>

    <div class="recycle-filters">
      <button
        v-for="f in visibleFilters"
        :key="f.value"
        :class="['rc-chip', { active: typeFilter === f.value }]"
        @click="typeFilter = f.value"
      >{{ t(f.labelKey) }}</button>
    </div>

    <div v-if="error" class="recycle-error">{{ error }}</div>

    <div class="recycle-body">
      <div v-if="visibleEntries.length" class="rc-list">
        <div v-for="e in visibleEntries" :key="keyOf(e)" class="rc-row">
          <span class="rc-icon" :data-type="e.collection">
            <component :is="TYPE_ICON[e.collection] || Bookmark" :size="15" />
          </span>
          <div class="rc-main">
            <div class="rc-line">
              <span class="rc-name">{{ e.label || t('activityUnknown') }}</span>
              <span class="rc-badge">{{ typeLabel(e.collection) }}</span>
            </div>
            <div class="rc-time">{{ fmtTime(e.deletedAt) }}</div>
          </div>
          <div class="rc-actions">
            <button class="rc-btn restore" :disabled="!!busyKey" @click="restore(e)">
              <Undo2 :size="12" /> {{ t('recycleRestore') }}
            </button>
            <button class="rc-btn danger" :disabled="!!busyKey" :title="t('recyclePurge')" @click="purge(e)">
              <Trash2 :size="12" />
            </button>
          </div>
        </div>
      </div>
      <div v-else-if="loading" class="rc-empty">{{ t('loading') }}</div>
      <p v-else-if="entries.length" class="rc-empty">{{ t('recycleFilterEmpty') }}</p>
      <div v-else class="rc-empty">
        <Trash2 :size="26" class="rc-empty-icon" />
        <p class="rc-empty-title">{{ t('recycleEmpty') }}</p>
        <p class="rc-empty-hint">{{ t('recycleEmptyHint') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.recycle-page {
  display: flex; flex-direction: column; height: 100%; overflow: hidden;
  background: transparent;
}
/* 嵌入设置时：页头降级（标题由外层分段控件给），与 ActivityPage.embedded 保持一致 */
.recycle-page.embedded .qd-page-header { padding: 0 14px; min-height: 40px; box-shadow: none; }
/* 页头已统一为 .qd-page-header（见 styles/app.css） */

.recycle-filters {
  display: flex; gap: 4px; flex-wrap: wrap; flex-shrink: 0;
  padding: 8px 16px; border-bottom: 1px solid var(--color-border);
}
.rc-chip {
  padding: 2px 9px; border-radius: var(--radius-md);
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); font-size: 11px; font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.rc-chip:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.rc-chip.active {
  background: var(--color-accent-bg); border-color: var(--color-accent); color: var(--color-accent);
}

.recycle-error {
  margin: 10px 16px 0; padding: 8px 12px; border-radius: var(--radius-sm); font-size: 12px;
  color: var(--color-danger-text); background: var(--color-danger-bg); border: 1px solid var(--color-danger-border);
}

.recycle-body { flex: 1; overflow-y: auto; padding: 8px 12px 16px; }
.rc-list { display: flex; flex-direction: column; gap: 4px; }
.rc-row {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 10px; border-radius: var(--radius-sm);
  border: 1px solid var(--color-border); background: var(--color-bg-secondary);
}
.rc-row:hover { background: var(--color-bg-hover); }
.rc-icon {
  width: 28px; height: 28px; flex-shrink: 0; border-radius: var(--radius-sm);
  display: flex; align-items: center; justify-content: center;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-secondary);
}
.rc-icon[data-type='workspaces'] { color: var(--color-accent); border-color: var(--color-accent-border); }
.rc-icon[data-type='scenes'] { color: var(--color-log-info); }
.rc-icon[data-type='collections'] { color: var(--color-success); border-color: var(--color-success-border); }
.rc-icon[data-type='notes'] { color: var(--color-warning); }
.rc-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.rc-line { display: flex; align-items: center; gap: 6px; min-width: 0; }
.rc-name {
  font-size: 12px; color: var(--color-text-primary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.rc-badge {
  flex-shrink: 0; font-size: 10px; padding: 0 5px; border-radius: 7px;
  color: var(--color-text-secondary); background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
}
.rc-time { font-size: 11px; color: var(--color-text-disabled); }
.rc-actions { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.rc-btn {
  display: flex; align-items: center; gap: 4px; font-size: 11px;
  padding: 3px 8px; border-radius: var(--radius-sm); cursor: pointer;
  border: 1px solid var(--color-border); background: var(--color-bg-primary);
  color: var(--color-text-primary); font-family: inherit;
}
.rc-btn:hover:not(:disabled) { background: var(--color-bg-active); }
.rc-btn:disabled { opacity: 0.4; cursor: default; }
.rc-btn.restore:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent); }
.rc-btn.danger { padding: 3px 6px; color: var(--color-text-disabled); }
.rc-btn.danger:hover:not(:disabled) { color: var(--color-danger); border-color: var(--color-danger-border); }

.rc-empty {
  display: flex; flex-direction: column; align-items: center; gap: 4px;
  padding: 32px 16px; margin: 0; text-align: center;
  font-size: 12px; color: var(--color-text-disabled);
}
.rc-empty-icon { color: var(--color-text-disabled); opacity: 0.6; margin-bottom: 4px; }
.rc-empty-title { margin: 0; font-size: 13px; color: var(--color-text-secondary); }
.rc-empty-hint { margin: 0; line-height: 1.6; }
</style>
