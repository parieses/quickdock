<script setup lang="ts">
import { ref, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { HardDrive, RotateCcw, Trash2, Plus, Eye, X } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { CreateSnapshot, ListSnapshots, DeleteSnapshot, RestoreSnapshot, GetSnapshot } from '../../bindings/quickdock/services/snapshot/snapshotservice'
import { GetValue, SetValue } from '../../bindings/quickdock/services/appservice'
import type { Snapshot, ToastAPI } from '../types'
import { useWorkspaceStore } from '../stores/workspace'

type AutoMode = 'off' | 'daily' | 'weekly'

// 与 Go 侧 snapshotConfig 同形：一个 JSON 串存进 app_state 的 qd_snapshot_config。
// 前端用现成的 GetValue/SetValue 读写，后端 ticker 用 GetSetting 读同一份，
// 不新造配置系统，也不需要为设置项新增 RPC 方法。
interface SnapshotConfig {
  autoMode: AutoMode
  autoTime: string
  autoWeekday: number
  maxCount: number
}

// GetSnapshot 返回的 payload 是 internal/db.SnapshotPayload 的 JSON：
// 5 个核心数组 + tables（扩展表，key 见 syncExtraTables）。预览只统计条数，
// 不需要把整库实体结构都声明出来。
interface SnapshotPayload {
  workspaces?: unknown[]
  scenes?: unknown[]
  collections?: unknown[]
  items?: unknown[]
  tools?: unknown[]
  tables?: Record<string, unknown[]>
}

interface ModuleRow { name: string; count: number }

const CONFIG_KEY = 'qd_snapshot_config'
// 展示顺序与 Go 的 SnapshotPayload 字段 + syncExtraTables 对齐；名称统一从
// i18n 的逗号分隔表按 index 取，模板里不写死任何用户可见的中文模块名。
const MODULE_KEYS = [
  'workspaces', 'scenes', 'collections', 'items', 'tools',
  'notes', 'todos', 'scheduled_tasks', 'monitors', 'plugins',
  'plugin_data', 'usage_frecency', 'ai_prompts', 'file_shelf',
]
const MODE_KEYS: AutoMode[] = ['off', 'daily', 'weekly']

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const store = useWorkspaceStore()
const emit = defineEmits<{ close: [] }>()
const props = defineProps<{ visible: boolean }>()

const snapshots = ref<Snapshot[]>([])
const snapshotLabel = ref('')
const snapshotNote = ref('')
const showCreateSnapshotForm = ref(false)
const snapshotMsg = ref('')
const snapshotMsgTimer = ref<ReturnType<typeof setTimeout> | null>(null)

const cfg = ref<SnapshotConfig>({ autoMode: 'off', autoTime: '03:00', autoWeekday: 1, maxCount: 20 })

const previewId = ref<string | null>(null)
const previewRows = ref<ModuleRow[]>([])
const previewTotal = ref(0)
const previewLoading = ref(false)

/** 逗号分隔的 i18n 名称表按 index 取；越界退回空串而不是渲染出 undefined。 */
function nameAt(key: string, index: number): string {
  return String(t(key)).split(',')[index] ?? ''
}

function showSnapshotMsg(msg: string, duration = 3000) {
  if (snapshotMsgTimer.value) clearTimeout(snapshotMsgTimer.value)
  snapshotMsg.value = msg
  snapshotMsgTimer.value = setTimeout(() => { snapshotMsg.value = ''; snapshotMsgTimer.value = null }, duration)
}

async function loadSnapshots() {
  try {
    const result = unwrap<Snapshot[]>(await ListSnapshots())
    snapshots.value = result ?? []
  } catch (e) {
    showSnapshotMsg(t('snapshotRestoreFailed') + ': ' + getErrorMessage(e))
  }
}

async function loadConfig() {
  try {
    const raw = unwrap<string>(await GetValue(CONFIG_KEY))
    if (!raw) return
    const p = JSON.parse(raw) as Partial<SnapshotConfig>
    cfg.value = {
      autoMode: p.autoMode === 'daily' || p.autoMode === 'weekly' ? p.autoMode : 'off',
      autoTime: typeof p.autoTime === 'string' && p.autoTime ? p.autoTime : '03:00',
      autoWeekday: typeof p.autoWeekday === 'number' && p.autoWeekday >= 0 && p.autoWeekday <= 6 ? p.autoWeekday : 1,
      maxCount: typeof p.maxCount === 'number' && p.maxCount >= 0 ? Math.floor(p.maxCount) : 20,
    }
  } catch {
    // 坏配置不让设置页报错：保留上面的默认值，用户重新保存一次即可覆盖。
  }
}

async function saveConfig() {
  // 规范化后再落库：number 输入被清空时会变成空串，直接序列化会让后端
  // 的 int 字段反序列化失败；0 是合法的「不限制」，不能被当成缺省回退。
  const normalized: SnapshotConfig = {
    autoMode: cfg.value.autoMode,
    autoTime: cfg.value.autoTime || '03:00',
    autoWeekday: Number(cfg.value.autoWeekday),
    maxCount: Math.max(0, Math.floor(Number(cfg.value.maxCount) || 0)),
  }
  try {
    unwrap(await SetValue(CONFIG_KEY, JSON.stringify(normalized)))
    showSnapshotMsg(t('saved'))
  } catch (e) {
    showSnapshotMsg(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}

async function togglePreview(s: Snapshot) {
  if (previewId.value === s.id) { previewId.value = null; return }
  if (previewLoading.value) return
  previewLoading.value = true
  previewId.value = s.id
  previewRows.value = []
  previewTotal.value = 0
  try {
    // 只在点开时按 id 取单条：ListSnapshots 刻意不带 payload（整库导出可能
    // 几 MB），预览才走 GetSnapshot 把这一份载荷读出来解析各模块条数。
    const detail = unwrap<Snapshot>(await GetSnapshot(s.id))
    let payload: SnapshotPayload = {}
    if (detail?.payload) payload = JSON.parse(detail.payload) as SnapshotPayload
    const tables = payload.tables ?? {}
    const top: Record<string, unknown[]> = {
      workspaces: payload.workspaces ?? [],
      scenes: payload.scenes ?? [],
      collections: payload.collections ?? [],
      items: payload.items ?? [],
      tools: payload.tools ?? [],
    }
    const rows = MODULE_KEYS.map((key, i) => {
      const arr = top[key] ?? tables[key]
      return { name: nameAt('snapshotModuleNames', i), count: Array.isArray(arr) ? arr.length : 0 }
    })
    previewRows.value = rows
    previewTotal.value = rows.reduce((n, r) => n + r.count, 0)
  } catch (e) {
    previewId.value = null
    showSnapshotMsg(t('snapshotPreviewFailed') + ': ' + getErrorMessage(e))
  } finally {
    previewLoading.value = false
  }
}

watch(() => props.visible, (v) => { if (v) { loadSnapshots(); loadConfig() } }, { immediate: true })

async function handleCreateSnapshot() {
  try {
    unwrap(await CreateSnapshot(snapshotLabel.value, snapshotNote.value))
    snapshotLabel.value = ''
    snapshotNote.value = ''
    showCreateSnapshotForm.value = false
    previewId.value = null
    showSnapshotMsg(t('snapshotCreated'))
    await loadSnapshots()
  } catch (e) {
    showSnapshotMsg(t('snapshotCreateFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleRestoreSnapshot(id: string) {
  if (!(await toast.confirm(t('confirmRestore')))) return
  try {
    unwrap(await RestoreSnapshot(id))
    showSnapshotMsg(t('restoreSuccess'))
    setTimeout(async () => { emit('close'); await store.initialize() }, 800)
  } catch (e) {
    showSnapshotMsg(t('snapshotRestoreFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleDeleteSnapshot(id: string) {
  if (!(await toast.confirm(t('confirmDeleteSnapshot')))) return
  try {
    unwrap(await DeleteSnapshot(id))
    if (previewId.value === id) previewId.value = null
    showSnapshotMsg(t('snapshotDeleted'))
    await loadSnapshots()
  } catch (e) {
    showSnapshotMsg(t('snapshotRestoreFailed') + ': ' + getErrorMessage(e))
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}

function formatDate(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  const pad = (n: number) => n.toString().padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
</script>

<template>
  <div class="section">
    <!-- 标题由设置页的分段栏提供，这里不再重复写一遍 -->
    <p class="section-desc">{{ t('snapshotDesc') }}</p>

    <div class="snapshot-create-area">
      <button v-if="!showCreateSnapshotForm" class="btn btn-primary" @click="showCreateSnapshotForm = true">
        <Plus :size="14" /> {{ t('createSnapshot') }}
      </button>
      <div v-else class="snapshot-create-form">
        <input v-model="snapshotLabel" class="snapshot-input" :placeholder="t('snapshotLabelPlaceholder')" />
        <input v-model="snapshotNote" class="snapshot-input" :placeholder="t('snapshotNotePlaceholder')" />
        <div class="snapshot-create-actions">
          <button class="btn btn-primary" @click="handleCreateSnapshot">{{ t('create') }}</button>
          <button class="btn btn-secondary" @click="showCreateSnapshotForm = false; snapshotLabel = ''; snapshotNote = ''">{{ t('cancel') }}</button>
        </div>
      </div>
    </div>

    <!-- 自动快照与条数上限：配置落 app_state(qd_snapshot_config)，后端 ticker 读同一份 -->
    <div class="snapshot-settings">
      <div class="snapshot-settings-title">{{ t('snapshotAutoLabel') }}</div>
      <div class="snapshot-settings-grid">
        <label class="snapshot-field">
          <span class="snapshot-field-label">{{ t('snapshotAutoMode') }}</span>
          <select v-model="cfg.autoMode" class="snapshot-input">
            <option v-for="(mode, i) in MODE_KEYS" :key="mode" :value="mode">{{ nameAt('snapshotModeNames', i) }}</option>
          </select>
        </label>
        <label class="snapshot-field">
          <span class="snapshot-field-label">{{ t('snapshotAutoTime') }}</span>
          <input v-model="cfg.autoTime" type="time" class="snapshot-input" />
        </label>
        <label v-if="cfg.autoMode === 'weekly'" class="snapshot-field">
          <span class="snapshot-field-label">{{ t('snapshotAutoWeekday') }}</span>
          <select v-model.number="cfg.autoWeekday" class="snapshot-input">
            <option v-for="wd in 7" :key="wd" :value="wd - 1">{{ nameAt('snapshotWeekdayNames', wd - 1) }}</option>
          </select>
        </label>
        <label class="snapshot-field">
          <span class="snapshot-field-label">{{ t('snapshotMaxCount') }}</span>
          <input v-model.number="cfg.maxCount" type="number" min="0" class="snapshot-input" />
        </label>
      </div>
      <p class="snapshot-settings-hint">{{ t('snapshotMaxCountHint') }}</p>
      <button class="btn btn-secondary snapshot-settings-save" @click="saveConfig">{{ t('save') }}</button>
    </div>

    <p v-if="snapshotMsg" class="result-hint">{{ snapshotMsg }}</p>

    <div v-if="snapshots.length === 0" class="snapshot-empty">
      <HardDrive :size="36" class="empty-icon" />
      <p class="empty-text">{{ t('emptySnapshots') }}</p>
      <p class="empty-hint">{{ t('createFirstSnapshot') }}</p>
    </div>
    <div v-else class="snapshot-list">
      <template v-for="s in snapshots" :key="s.id">
        <div class="snapshot-item">
          <div class="snapshot-item-info">
            <!-- 自动快照的 label 是后端写的哨兵串，后台拿不到 t()，在这里翻译 -->
            <span class="snapshot-item-label">{{ s.label === 'auto' ? t('snapshotAutoLabel') : (s.label || t('snapshot')) }}</span>
            <span v-if="s.note" class="snapshot-item-note">{{ s.note }}</span>
            <span class="snapshot-item-meta">{{ formatDate(s.created_at) }} · {{ formatSize(s.size) }}</span>
          </div>
          <div class="snapshot-item-actions">
            <button class="action-btn" :class="{ 'enabled-btn': previewId === s.id }" @click="togglePreview(s)" :title="t('snapshotPreviewAction')">
              <Eye :size="14" />
            </button>
            <button class="action-btn restore-btn" @click="handleRestoreSnapshot(s.id)" :title="t('restoreSnapshot')">
              <RotateCcw :size="14" />
            </button>
            <button class="action-btn danger" @click="handleDeleteSnapshot(s.id)" :title="t('deleteSnapshot')">
              <Trash2 :size="14" />
            </button>
          </div>
        </div>
        <div v-if="previewId === s.id" class="snapshot-preview">
          <div class="snapshot-preview-head">
            <span class="snapshot-preview-title">{{ t('snapshotPreviewTitle') }}</span>
            <button class="snapshot-preview-close" @click="previewId = null" :title="t('cancel')"><X :size="13" /></button>
          </div>
          <p v-if="previewTotal === 0" class="snapshot-preview-empty">{{ t('snapshotPreviewEmpty') }}</p>
          <div v-else class="snapshot-module-grid">
            <div v-for="row in previewRows" :key="row.name" class="snapshot-module-row">
              <span class="snapshot-module-name">{{ row.name }}</span>
              <span class="snapshot-module-count">{{ row.count }}</span>
            </div>
            <div class="snapshot-module-row snapshot-module-total">
              <span class="snapshot-module-name">{{ t('snapshotTotalCount') }}</span>
              <span class="snapshot-module-count">{{ previewTotal }}</span>
            </div>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
/* 新样式只写在本 SFC 的 scoped 块里，不动全局 settings.css / style.css。
   颜色、圆角、字体一律复用项目已有的 CSS token。 */
.snapshot-settings {
  margin-bottom: 16px; padding: 12px 14px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.snapshot-settings-title { font-size: 13px; font-weight: 500; color: var(--color-text-primary); margin-bottom: 10px; }
.snapshot-settings-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 12px; }
.snapshot-field { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.snapshot-field-label { font-size: 12px; color: var(--color-text-secondary); }
.snapshot-settings-hint { font-size: 11px; color: var(--color-text-disabled); margin: 8px 0 0; line-height: 1.5; }
.snapshot-settings-save { margin-top: 10px; }

.snapshot-preview {
  margin: -2px 0 4px; padding: 10px 14px;
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.snapshot-preview-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.snapshot-preview-title { font-size: 12px; font-weight: 500; color: var(--color-text-primary); }
.snapshot-preview-close {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; border: none; background: transparent;
  color: var(--color-text-muted); border-radius: var(--radius-sm); cursor: pointer;
}
.snapshot-preview-close:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.snapshot-preview-empty { font-size: 12px; color: var(--color-text-disabled); margin: 0; }
.snapshot-module-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 4px 16px; }
.snapshot-module-row {
  display: flex; align-items: center; justify-content: space-between;
  font-size: 12px; padding: 2px 0; border-bottom: 1px solid var(--color-border);
}
.snapshot-module-name { color: var(--color-text-secondary); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.snapshot-module-count { color: var(--color-text-primary); font-variant-numeric: tabular-nums; font-family: var(--font-mono, monospace); }
.snapshot-module-total { border-bottom: none; font-weight: 500; }
.snapshot-module-total .snapshot-module-name { color: var(--color-text-primary); }
</style>
