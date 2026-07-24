<script setup lang="ts">
import { ref, computed, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Pen, Trash2, Check, X } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { ListTools, CreateTool, UpdateTool, DeleteTool } from '../../bindings/quickdock/services/appservice'
import type { OpenTool } from '../types'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const props = defineProps<{ visible: boolean }>()

// 可选的工具类型（与后端 EnsureDefaultTools 及 TYPE_TOOL_MAP 保持一致）
const TOOL_TYPES = [
  { value: '系统', label: '系统（跟随系统打开）' },
  { value: '编辑器', label: '编辑器（如 VS Code / Trae）' },
  { value: '浏览器', label: '浏览器（如 Chrome / Edge）' },
  { value: '终端', label: '终端（CMD / PowerShell）' },
  { value: 'Office', label: 'Office' },
]

const tools = ref<OpenTool[]>([])
const loading = ref(false)
const error = ref('')

// 编辑草稿：null = 关闭；对象 = 新增/编辑中
const editing = ref<OpenTool | null>(null)
const isNew = ref(false)
const saving = ref(false)
const savingError = ref('')

function isSentinel(t: OpenTool): boolean {
  return t.name === '系统默认' && t.isDefault === 1
}

function emptyDraft(): OpenTool {
  return { id: '', name: '', type: '系统', path: '', args: '', isDefault: 0 }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = unwrap<OpenTool[]>(await ListTools())
    // 排除内置“系统默认”哨兵工具（编辑器已有空选项代表系统默认）
    tools.value = (result || []).filter((x) => !isSentinel(x)) as OpenTool[]
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

// 按类型分组（保持 TOOL_TYPES 顺序）；仅展示有工具的分组
const groupedTools = computed(() => {
  return TOOL_TYPES
    .map((opt) => ({
      type: opt.value,
      label: opt.value,
      items: tools.value.filter((x) => x.type === opt.value),
    }))
    .filter((g) => g.items.length > 0)
})

// 打开设置页时加载一次
watch(() => props.visible, (v) => { if (v) load() }, { immediate: true })

function openCreate() {
  isNew.value = true
  savingError.value = ''
  editing.value = emptyDraft()
}

function openEdit(tool: OpenTool) {
  isNew.value = false
  savingError.value = ''
  editing.value = { ...tool }
}

function closeEditor() {
  editing.value = null
}

async function save() {
  if (!editing.value) return
  const draft = editing.value
  const name = (draft.name || '').trim()
  if (!name) {
    savingError.value = t('emptyName')
    return
  }
  saving.value = true
  savingError.value = ''
  try {
    const type = draft.type || '系统'
    const path = draft.path || ''
    const args = draft.args || ''
    if (isNew.value) {
      const created = unwrap<OpenTool>(await CreateTool(name, type, path, args))
      // CreateTool 无 isDefault 参数，若需设为默认则再更新一次
      if (draft.isDefault === 1 && created && created.id) {
        await UpdateTool(created.id, name, type, path, args, 1)
      }
    } else {
      await UpdateTool(draft.id, name, type, path, args, draft.isDefault === 1 ? 1 : 0)
    }
    toast.success(t('saveSuccess'))
    closeEditor()
    await load()
  } catch (e) {
    savingError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function remove(tool: OpenTool) {
  if (!(await toast.confirm(t('confirmDeleteTool')))) return
  try {
    unwrap(await DeleteTool(tool.id))
    toast.success(t('toolDeleted'))
    await load()
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}

async function setDefault(tool: OpenTool) {
  try {
    await UpdateTool(tool.id, tool.name, tool.type, tool.path || '', tool.args || '', 1)
    toast.success(t('toolDefaultSet'))
    await load()
  } catch (e) {
    toast.error(t('updateFailed') + ': ' + getErrorMessage(e))
  }
}
</script>

<template>
  <div class="tool-manage">
    <div class="tool-header">
      <div>
        <h3 class="tool-title">{{ t('openTool') }}</h3>
        <p class="tool-desc">{{ t('toolManageDesc') }}</p>
      </div>
      <button class="btn btn-primary" :disabled="saving" @click="openCreate">
        <Plus :size="14" /> {{ t('newTool') }}
      </button>
    </div>

    <p v-if="error" class="tool-error">{{ error }}</p>

    <div v-if="loading" class="tool-loading">{{ t('loadFailed') }}…</div>

    <div v-else-if="groupedTools.length === 0" class="tool-empty">
      {{ t('noTools') }}
    </div>

    <div v-else class="tool-groups">
      <div v-for="group in groupedTools" :key="group.type" class="tool-group">
        <div class="tool-group-header">
          <span class="tool-group-title">{{ group.label }}</span>
          <span class="tool-group-count">{{ group.items.length }}</span>
        </div>
        <div class="tool-list">
          <div v-for="tool in group.items" :key="tool.id" class="tool-item" :class="{ 'is-default': tool.isDefault === 1 }">
            <div class="tool-item-main">
              <span class="tool-name">{{ tool.name }}</span>
              <span v-if="tool.isDefault === 1" class="tool-default-badge">{{ t('defaultBadge') }}</span>
            </div>
            <div class="tool-item-meta">
              <span v-if="tool.path" class="tool-path">{{ tool.path }}</span>
              <span v-if="tool.args" class="tool-args">{{ tool.args }}</span>
            </div>
            <div class="tool-item-actions">
              <button
                v-if="tool.isDefault !== 1"
                class="tool-action"
                :title="t('toolSetDefault')"
                @click="setDefault(tool)"
              >
                <Check :size="15" />
              </button>
              <button class="tool-action" :title="t('editTool')" @click="openEdit(tool)">
                <Pen :size="15" />
              </button>
              <button class="tool-action danger" :title="t('delete')" @click="remove(tool)">
                <Trash2 :size="15" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 编辑 / 新增模态框 -->
    <Teleport to="body">
      <div v-if="editing" class="tool-modal-overlay" @mousedown.self="closeEditor">
        <div class="tool-modal">
          <div class="tool-modal-header">
            <h3>{{ isNew ? t('newTool') : t('editTool') }}</h3>
            <button class="tool-modal-close" @click="closeEditor">
              <X :size="18" />
            </button>
          </div>

          <div class="tool-modal-body">
            <div class="field">
              <label class="field-label">{{ t('name') }}</label>
              <input
                v-model="editing.name"
                class="field-input"
                :placeholder="t('toolNamePlaceholder')"
                @keyup.enter="save"
              />
            </div>

            <div class="field">
              <label class="field-label">{{ t('toolType') }}</label>
              <select v-model="editing.type" class="field-input">
                <option v-for="opt in TOOL_TYPES" :key="opt.value" :value="opt.value">
                  {{ opt.label }}
                </option>
              </select>
            </div>

            <div class="field">
              <label class="field-label">{{ t('toolPath') }}</label>
              <input
                v-model="editing.path"
                class="field-input"
                :placeholder="t('toolPath')"
              />
            </div>

            <div class="field">
              <label class="field-label">{{ t('toolArgs') }}</label>
              <input
                v-model="editing.args"
                class="field-input"
                :placeholder="t('toolArgs')"
              />
              <p class="field-hint">{{ t('toolArgsHint') }}</p>
            </div>

            <div class="field tool-default-field">
              <label class="tool-default-toggle">
                <input type="checkbox" v-model="editing.isDefault" :true-value="1" :false-value="0" />
                <span>{{ t('toolSetDefault') }}</span>
              </label>
            </div>

            <p v-if="savingError" class="tool-error">{{ savingError }}</p>
          </div>

          <div class="tool-modal-footer">
            <button class="btn btn-secondary" :disabled="saving" @click="closeEditor">
              {{ t('cancel') }}
            </button>
            <button class="btn btn-primary" :disabled="saving" @click="save">
              {{ t('save') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.tool-manage {
  width: 100%;
  max-width: 640px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.tool-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.tool-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--color-text-primary);
  margin: 0 0 4px;
}
.tool-desc {
  font-size: 12px;
  color: var(--color-text-disabled);
  margin: 0;
}
.tool-loading,
.tool-empty {
  font-size: 13px;
  color: var(--color-text-disabled);
  padding: 24px 0;
  text-align: center;
}
.tool-error {
  font-size: 12px;
  color: var(--color-danger);
  margin: 0;
}

/* 分组 */
.tool-groups {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.tool-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.tool-group-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 2px;
}
.tool-group-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text-secondary);
  letter-spacing: 0.02em;
}
.tool-group-count {
  font-size: 11px;
  min-width: 18px;
  height: 18px;
  padding: 0 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 9px;
  background: var(--color-bg-active);
  color: var(--color-text-muted);
}

/* 列表 */
.tool-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.tool-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  transition: border-color 0.12s;
}
.tool-item:hover {
  border-color: var(--color-border-light);
}
.tool-item.is-default {
  border-color: var(--color-accent);
  background: var(--color-accent-bg);
}
.tool-item-main {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex: 1;
}
.tool-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tool-type-badge {
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 10px;
  background: var(--color-bg-active);
  color: var(--color-text-secondary);
  flex-shrink: 0;
}
.tool-default-badge {
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 10px;
  background: var(--color-accent);
  color: var(--color-accent-text);
  flex-shrink: 0;
}
.tool-item-meta {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  flex: 1;
  text-align: right;
}
.tool-path {
  font-size: 11px;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tool-args {
  font-size: 11px;
  color: var(--color-text-disabled);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tool-item-actions {
  display: flex;
  gap: 2px;
  align-items: center;
  flex-shrink: 0;
}
.tool-action {
  width: 28px;
  height: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: none;
  background: transparent;
  color: var(--color-text-muted);
  border-radius: 6px;
  cursor: pointer;
  transition: background-color 0.12s, color 0.12s;
}
.tool-action:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.tool-action.danger:hover {
  color: var(--color-danger);
  background: rgba(232, 76, 76, 0.1);
}

/* 编辑模态框 */
.tool-modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 20000;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
}
.tool-modal {
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: 12px;
  width: 520px;
  max-width: 90vw;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.25);
}
.tool-modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 18px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.tool-modal-header h3 {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
  color: var(--color-text-primary);
}
.tool-modal-close {
  background: none;
  border: none;
  color: var(--color-text-muted);
  font-size: 20px;
  cursor: pointer;
  width: 28px;
  height: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
}
.tool-modal-close:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}
.tool-modal-body {
  padding: 14px 18px;
  overflow-y: auto;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.tool-modal-footer {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  padding: 12px 18px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.field-label {
  font-size: 12px;
  color: var(--color-text-muted);
  font-weight: 500;
}
.field-input {
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  padding: 9px 12px;
  color: var(--color-text-primary);
  font-size: 13px;
  outline: none;
  transition: border-color 0.15s;
  font-family: inherit;
  width: 100%;
  box-sizing: border-box;
}
.field-input:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 2px var(--color-accent-border);
}
.field-hint {
  font-size: 11px;
  color: var(--color-text-disabled);
  margin: 0;
  line-height: 1.5;
}
.tool-default-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--color-text-muted);
  cursor: pointer;
}
.tool-default-toggle input {
  width: 16px;
  height: 16px;
  accent-color: var(--color-accent);
  cursor: pointer;
}

/* 复用设置页通用按钮样式 */
.btn {
  padding: 6px 14px;
  border: none;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
  font-family: inherit;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.btn-primary {
  background: var(--color-accent);
  color: var(--color-accent-text);
}
.btn-primary:hover {
  background: var(--color-accent-hover);
}
.btn-secondary {
  background: var(--color-bg-active);
  color: var(--color-text-secondary);
}
.btn-secondary:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}
.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
