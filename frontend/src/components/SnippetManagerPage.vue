<script setup lang="ts">
import { ref, computed, onMounted, inject, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Clipboard, Plus, Pencil, Trash2, Search, X, CornerDownLeft, CheckSquare, Square, ChevronLeft, ChevronRight } from '@lucide/vue'
import { ListSnippets, CreateSnippet, UpdateSnippet, DeleteSnippet, PasteSnippet } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import CreateDialog from './CreateDialog.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import type { Snippet, ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 数据 ----
const snippets = ref<Snippet[]>([])
const loading = ref(true)
const searchQuery = ref('')

async function loadSnippets() {
  loading.value = true
  try {
    const result = unwrap<Snippet[]>(await ListSnippets())
    snippets.value = result || []
  } catch (e) {
    console.error('[SnippetManager] ListSnippets:', getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

// ---- 搜索过滤 ----
const filteredSnippets = computed(() => {
  if (!searchQuery.value.trim()) return snippets.value
  const q = searchQuery.value.toLowerCase()
  return snippets.value.filter(s =>
    s.keyword.toLowerCase().includes(q) ||
    s.content.toLowerCase().includes(q) ||
    s.category.toLowerCase().includes(q)
  )
})

// ---- 分页 ----
const pageSize = 15
const currentPage = ref(1)

const totalPages = computed(() => Math.max(1, Math.ceil(filteredSnippets.value.length / pageSize)))

const pagedSnippets = computed(() => {
  const start = (currentPage.value - 1) * pageSize
  return filteredSnippets.value.slice(start, start + pageSize)
})

// 搜索或数据变化时回到第一页
watch(filteredSnippets, () => { currentPage.value = 1 })

function goToPage(p: number) {
  if (p >= 1 && p <= totalPages.value) currentPage.value = p
}

// ---- 新建 ----
const showCreateDialog = ref(false)
const createFields = computed(() => [
  { key: 'keyword', label: t('snippetKeyword'), type: 'text' as const, placeholder: t('snippetKeywordPlaceholder') },
  { key: 'content', label: t('snippetContent'), type: 'textarea' as const, placeholder: t('snippetContentPlaceholder') },
  { key: 'category', label: t('snippetCategory'), type: 'select' as const, options: [
    { label: t('snippetCatOther'), value: '' },
    { label: t('snippetCatEmail'), value: '邮箱' },
    { label: t('snippetCatUrl'), value: '链接' },
    { label: t('snippetCatCode'), value: '代码' },
    { label: t('snippetCatPhone'), value: '手机号' },
    { label: t('snippetCatTemplate'), value: '模板' },
  ]},
])

async function handleCreate(values: Record<string, string>) {
  try {
    await CreateSnippet(values.keyword, values.content, values.category || '')
    showCreateDialog.value = false
    toast.success(t('saved'))
    await loadSnippets()
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}

// ---- 编辑 ----
const showEditDialog = ref(false)
const editingSnippet = ref<Snippet | null>(null)

function startEdit(s: Snippet) {
  editingSnippet.value = s
  showEditDialog.value = true
}

async function handleEdit(values: Record<string, string>) {
  if (!editingSnippet.value) return
  try {
    await UpdateSnippet(editingSnippet.value.id, values.keyword, values.content, values.category || '')
    showEditDialog.value = false
    editingSnippet.value = null
    toast.success(t('saved'))
    await loadSnippets()
  } catch (e) {
    toast.error(t('updateFailed') + ': ' + getErrorMessage(e))
  }
}

// ---- 删除 ----
const showDeleteConfirm = ref(false)
const deletingId = ref('')

async function confirmDelete(id: string) {
  deletingId.value = id
  showDeleteConfirm.value = true
}

async function handleDelete() {
  try {
    await DeleteSnippet(deletingId.value)
    showDeleteConfirm.value = false
    toast.success(t('deleted'))
    await loadSnippets()
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}

// ---- 多选删除 ----
const selectedIds = ref(new Set<string>())

const isAllSelected = computed(() =>
  filteredSnippets.value.length > 0 &&
  filteredSnippets.value.every(s => selectedIds.value.has(s.id))
)

function toggleSelect(id: string) {
  const next = new Set(selectedIds.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  selectedIds.value = next
}

function toggleSelectAll() {
  if (isAllSelected.value) {
    selectedIds.value = new Set()
  } else {
    selectedIds.value = new Set(filteredSnippets.value.map(s => s.id))
  }
}

const showBatchDeleteConfirm = ref(false)

function confirmBatchDelete() {
  showBatchDeleteConfirm.value = true
}

async function handleBatchDelete() {
  try {
    const ids = Array.from(selectedIds.value)
    await Promise.all(ids.map(id => DeleteSnippet(id)))
    showBatchDeleteConfirm.value = false
    selectedIds.value = new Set()
    toast.success(t('deleted') + ` (${ids.length})`)
    await loadSnippets()
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}

function clearSelection() {
  selectedIds.value = new Set()
}

// ---- 粘贴 ----
async function handlePaste(s: Snippet) {
  try {
    await PasteSnippet(s.content)
    toast.success(t('copied'))
  } catch (e) {
    toast.error(t('copyFailed') + ': ' + getErrorMessage(e))
  }
}

// ---- 格式化时间 ----
function formatDate(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function contentPreview(text: string, max = 60): string {
  if (!text) return ''
  const single = text.replace(/\s+/g, ' ')
  return single.length > max ? single.slice(0, max) + '...' : single
}

onMounted(loadSnippets)
</script>

<template>
  <div class="snippet-page">
    <!-- 页面头部 -->
    <div class="snippet-header">
      <div class="snippet-title-row">
        <h2 class="snippet-title">{{ t('snippetManager') }}</h2>
        <button class="snippet-add-btn" @click="showCreateDialog = true">
          <Plus :size="15" />
          <span>{{ t('snippetNew') }}</span>
        </button>
      </div>
      <div class="snippet-search">
        <Search :size="14" class="search-icon" />
        <input
          v-model="searchQuery"
          type="text"
          class="search-input"
          :placeholder="t('search')"
        />
        <button v-if="searchQuery" class="clear-btn" @click="searchQuery = ''" :title="t('clear')">
          <X :size="12" />
        </button>
      </div>
    </div>

    <!-- 列表 -->
    <div class="snippet-list">
      <!-- 空状态 -->
      <div v-if="!loading && filteredSnippets.length === 0" class="snippet-empty">
        <Clipboard :size="36" class="empty-icon" />
        <p class="empty-text">{{ t('noSnippets') }}</p>
        <p class="empty-hint">{{ t('createFirstSnippet') }}</p>
      </div>

      <!-- 加载中 -->
      <div v-if="loading" class="snippet-loading">{{ t('loading') }}</div>

      <!-- 表头 -->
      <div v-if="filteredSnippets.length > 0" class="snippet-table-header">
        <span class="col-check">
          <button class="check-btn" @click="toggleSelectAll" :title="t('selectAll')">
            <CheckSquare :size="14" v-if="isAllSelected" />
            <Square :size="14" v-else />
          </button>
        </span>
        <span class="col-kw">{{ t('snippetKeyword') }}</span>
        <span class="col-content">{{ t('snippetContent') }}</span>
        <span class="col-cat">{{ t('snippetCategory') }}</span>
        <span class="col-date">{{ t('snippetCreatedAt') }}</span>
        <span class="col-actions">{{ t('open') }}</span>
      </div>

      <!-- 行 -->
      <div
        v-for="s in pagedSnippets"
        :key="s.id"
        :class="['snippet-row', { 'row-selected': selectedIds.has(s.id) }]"
      >
        <span class="col-check">
          <button class="check-btn" @click="toggleSelect(s.id)">
            <CheckSquare :size="14" v-if="selectedIds.has(s.id)" class="check-on" />
            <Square :size="14" v-else class="check-off" />
          </button>
        </span>
        <span class="col-kw kw-text">{{ s.keyword }}</span>
        <span class="col-content content-preview" :title="s.content">{{ contentPreview(s.content) }}</span>
        <span class="col-cat">
          <span class="cat-badge" v-if="s.category">{{ s.category }}</span>
          <span v-else class="cat-none">-</span>
        </span>
        <span class="col-date date-text">{{ formatDate(s.createdAt) }}</span>
        <span class="col-actions">
          <button class="action-btn paste-btn" :title="t('open')" @click="handlePaste(s)">
            <CornerDownLeft :size="13" />
          </button>
          <button class="action-btn edit-btn" :title="t('edit')" @click="startEdit(s)">
            <Pencil :size="13" />
          </button>
          <button class="action-btn del-btn" :title="t('delete')" @click="confirmDelete(s.id)">
            <Trash2 :size="13" />
          </button>
        </span>
      </div>

      <!-- 批量操作栏 -->
      <div v-if="selectedIds.size > 0" class="batch-bar">
        <span class="batch-count">{{ t('selectedCount', { count: selectedIds.size }) }}</span>
        <div class="batch-actions">
          <button class="batch-btn batch-cancel" @click="clearSelection">{{ t('cancel') }}</button>
          <button class="batch-btn batch-delete" @click="confirmBatchDelete">
            <Trash2 :size="13" />
            <span>{{ t('delete') }} ({{ selectedIds.size }})</span>
          </button>
        </div>
      </div>

      <!-- 分页 -->
      <div v-if="filteredSnippets.length > pageSize" class="pagination">
        <button class="page-btn" :disabled="currentPage <= 1" @click="goToPage(currentPage - 1)">
          <ChevronLeft :size="14" />
        </button>
        <template v-for="p in totalPages" :key="p">
          <button
            v-if="p === 1 || p === totalPages || Math.abs(p - currentPage) <= 1"
            :class="['page-btn page-num', { active: p === currentPage }]"
            @click="goToPage(p)"
          >{{ p }}</button>
          <span v-else-if="p === totalPages - 1 || p === 2" class="page-ellipsis">…</span>
        </template>
        <button class="page-btn" :disabled="currentPage >= totalPages" @click="goToPage(currentPage + 1)">
          <ChevronRight :size="14" />
        </button>
        <span class="page-total">{{ t('paginationTotal', { total: filteredSnippets.length }) }}</span>
      </div>
    </div>
    <CreateDialog
      :visible="showCreateDialog"
      :title="t('snippetNew')"
      :fields="createFields"
      @confirm="handleCreate"
      @cancel="showCreateDialog = false"
    />

    <!-- 编辑对话框 -->
    <CreateDialog
      v-if="editingSnippet"
      :visible="showEditDialog"
      :title="t('snippetEdit')"
      :fields="createFields"
      :editValues="{ keyword: editingSnippet.keyword, content: editingSnippet.content, category: editingSnippet.category }"
      @confirm="handleEdit"
      @cancel="showEditDialog = false; editingSnippet = null"
    />

    <!-- 删除确认 -->
    <ConfirmDialog
      :visible="showDeleteConfirm"
      :message="t('confirmDelete')"
      @confirm="handleDelete"
      @cancel="showDeleteConfirm = false"
    />

    <!-- 批量删除确认 -->
    <ConfirmDialog
      :visible="showBatchDeleteConfirm"
      :message="t('confirmDeleteBatch', { count: selectedIds.size })"
      @confirm="handleBatchDelete"
      @cancel="showBatchDeleteConfirm = false"
    />
  </div>
</template>

<style scoped>
.snippet-page {
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  padding: 20px 24px;
  background: var(--color-bg-primary);
}

/* 头部 */
.snippet-header {
  flex-shrink: 0;
  margin-bottom: 16px;
}
.snippet-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.snippet-title {
  font-size: 16px;
  font-weight: 500;
  color: var(--color-text-primary);
  margin: 0;
}
.snippet-add-btn {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 6px 12px;
  border: none;
  border-radius: 6px;
  background: var(--color-accent);
  color: #fff;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition: opacity var(--transition-fast);
}
.snippet-add-btn:hover { opacity: 0.85; }

/* 搜索 */
.snippet-search {
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  padding: 0 10px;
  height: 34px;
}
.snippet-search:focus-within {
  border-color: var(--color-border-focus);
  box-shadow: 0 0 0 2px var(--color-accent-bg);
}
.snippet-search .search-input {
  flex: 1;
  background: none;
  border: none;
  outline: none;
  color: var(--color-text-primary);
  font-size: 13px;
  font-family: inherit;
}
.snippet-search .clear-btn {
  background: none;
  border: none;
  color: var(--color-text-disabled);
  cursor: pointer;
  display: flex;
  padding: 2px;
  border-radius: 4px;
}
.snippet-search .clear-btn:hover { color: var(--color-text-muted); background: var(--color-bg-active); }

/* 列表 */
.snippet-list {
  flex: 1;
  overflow-y: auto;
}

/* 表头 */
.snippet-table-header {
  display: flex;
  align-items: center;
  padding: 8px 12px;
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  border-bottom: 1px solid var(--color-border);
  user-select: none;
}

/* 行 */
.snippet-row {
  display: flex;
  align-items: center;
  padding: 10px 12px;
  border-bottom: 1px solid var(--color-border);
  transition: background var(--transition-fast);
}
.snippet-row:hover { background: var(--color-bg-hover); }
.snippet-row.row-selected { background: var(--color-accent-bg); }

.col-check { width: 32px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; }
.col-kw { width: 130px; flex-shrink: 0; }
.col-content { flex: 1; min-width: 0; }
.col-cat { width: 80px; flex-shrink: 0; text-align: center; }
.col-date { width: 100px; flex-shrink: 0; text-align: center; }
.col-actions { width: 100px; flex-shrink: 0; text-align: right; }

.kw-text {
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.content-preview {
  font-size: 12px;
  color: var(--color-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono, monospace);
}
.date-text {
  font-size: 12px;
  color: var(--color-text-muted);
}
.cat-badge {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 4px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  font-size: 11px;
}
.cat-none { color: var(--color-text-disabled); font-size: 12px; }

/* 复选框 */
.check-btn {
  background: none;
  border: none;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2px;
  border-radius: 3px;
  transition: color var(--transition-fast);
}
.check-btn .check-on { color: var(--color-accent); }
.check-btn .check-off { color: var(--color-text-disabled); }
.check-btn:hover .check-off { color: var(--color-text-muted); }

/* 批量操作栏 */
.batch-bar {
  position: sticky;
  bottom: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  background: var(--color-bg-tertiary);
  border-top: 1px solid var(--color-border);
  border-radius: 8px 8px 0 0;
  margin-top: 4px;
}
.batch-count {
  font-size: 12px;
  color: var(--color-text-secondary);
  font-weight: 500;
}
.batch-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.batch-btn {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 14px;
  border: none;
  border-radius: 6px;
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
  transition: all var(--transition-fast);
}
.batch-cancel {
  background: var(--color-bg-active);
  color: var(--color-text-muted);
}
.batch-cancel:hover { color: var(--color-text-primary); }
.batch-delete {
  background: var(--color-danger);
  color: #fff;
}
.batch-delete:hover { opacity: 0.85; }

/* 分页 */
.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 12px 0 4px;
  flex-shrink: 0;
}
.page-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 28px;
  height: 28px;
  border: 1px solid var(--color-border);
  border-radius: 5px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
  transition: all var(--transition-fast);
}
.page-btn:hover:not(:disabled) { background: var(--color-bg-active); color: var(--color-text-primary); }
.page-btn:disabled { opacity: 0.35; cursor: default; }
.page-btn.active { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
.page-num { font-weight: 500; }
.page-ellipsis { color: var(--color-text-disabled); font-size: 12px; width: 20px; text-align: center; }
.page-total { margin-left: 8px; font-size: 11px; color: var(--color-text-muted); }

/* 操作按钮 */
.action-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: none;
  border-radius: 5px;
  background: transparent;
  cursor: pointer;
  transition: all var(--transition-fast);
  margin-left: 2px;
}
.paste-btn { color: var(--color-accent); }
.edit-btn { color: var(--color-text-muted); }
.del-btn { color: var(--color-text-disabled); }
.paste-btn:hover { background: var(--color-accent-bg); }
.edit-btn:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.del-btn:hover { background: rgba(232, 76, 76, 0.1); color: var(--color-danger); }

/* 空状态 */
.snippet-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 24px;
  gap: 8px;
}
.empty-icon { color: var(--color-text-disabled); }
.empty-text { font-size: 14px; color: var(--color-text-secondary); margin: 0; }
.empty-hint { font-size: 12px; color: var(--color-text-muted); margin: 0; }
.snippet-loading {
  text-align: center;
  padding: 40px;
  color: var(--color-text-muted);
  font-size: 13px;
}

/* 滚动条 */
.snippet-list::-webkit-scrollbar { width: 5px; }
.snippet-list::-webkit-scrollbar-track { background: transparent; }
.snippet-list::-webkit-scrollbar-thumb { background: var(--color-scrollbar-thumb); border-radius: 3px; }
</style>
