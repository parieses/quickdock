<script setup lang="ts">
import { ref, inject, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { Plus, Pencil, Trash2, Play } from '@lucide/vue'
import TypeIcon from './TypeIcon.vue'
import CreateDialog from './CreateDialog.vue'
import { getErrorMessage } from '../utils/error'
import type { Collection, ToastAPI } from '../types'

const store = useWorkspaceStore()
const { t, tm } = useI18n()
const toast = inject<ToastAPI>('toast')!

const showCollectionDialog = ref(false)
const collectionFields = computed(() => {
  const ct = tm('collectionTypes') as Record<string, string>
  const os = tm('openStrategies') as Record<string, string>
  return [
  { key: 'name', label: t('collectionName'), type: 'text' as const, placeholder: '例如：项目源码' },
  { key: 'type', label: t('collectionType'), type: 'select' as const, options: [
    { label: ct['目录集合'] || '目录集合', value: '目录集合' },
    { label: ct['网页集合'] || '网页集合', value: '网页集合' },
    { label: ct['命令集合'] || '命令集合', value: '命令集合' },
    { label: ct['文件集合'] || '文件集合', value: '文件集合' },
    { label: ct['应用集合'] || '应用集合', value: '应用集合' },
  ]},
  { key: 'openStrategy', label: t('openStrategy'), type: 'select' as const, options: [
    { label: os['single'] || '单次打开', value: 'single' },
    { label: os['batch'] || '批量打开', value: 'batch' },
    { label: os['all'] || '全部打开', value: 'all' },
  ]},
]})

const editingCollection = ref<Collection | null>(null)
const showEditDialog = ref(false)

function handleCollectionClick(collectionId: string) {
  store.selectCollection(collectionId)
}

async function handleOpenAll(collectionId: string) {
  try {
    await store.openAllInCollection(collectionId)
  } catch (e) {
    toast.error(t('openAllFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleCreateCollection(values: Record<string, string>) {
  try {
    await store.addCollection(values.name, values.type, values.openStrategy || 'single')
    showCollectionDialog.value = false
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleEditCollection(values: Record<string, string>) {
  if (!editingCollection.value) return
  try {
    await store.updateCollectionAction(editingCollection.value.id, { name: values.name, type: values.type, openStrategy: values.openStrategy || 'single' })
    showEditDialog.value = false
    editingCollection.value = null
  } catch (e) {
    toast.error(t('updateFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleDeleteCollection(collectionId: string) {
  if (toast && !(await toast.confirm(t('confirmDeleteCollection')))) return
  try { await store.removeCollection(collectionId) } catch (e) { toast?.error(t('deleteFailed') + ': ' + getErrorMessage(e)) }
}

// 拖拽排序
const dragColId = ref<string | null>(null)

function onDragStartCol(e: DragEvent, colId: string) {
  dragColId.value = colId
  e.dataTransfer?.setData('text/plain', colId)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}
function onDragEndCol() {
  dragColId.value = null
}
function onDragOverCol(e: DragEvent) {
  e.preventDefault()
}
function onDropCol(e: DragEvent, targetId: string) {
  e.preventDefault()
  const ids = store.filteredCollections.map(c => c.id)
  const from = ids.indexOf(dragColId.value!)
  const to = ids.indexOf(targetId)
  if (from < 0 || to < 0 || from === to) return
  ids.splice(from, 1)
  ids.splice(to, 0, dragColId.value!)
  if (store.hasSearch) return
  store.reorderCollections(ids)
  dragColId.value = null
}
</script>

<template>
  <section class="collection-list">
    <div class="collection-header">
      <span class="collection-title">{{ store.hasSearch ? t('searchResults') : t('collections') }}</span>
      <button v-if="store.activeSceneId && !store.hasSearch" class="icon-btn" @click="showCollectionDialog = true" :title="t('addCollection')">
        <Plus :size="16" />
      </button>
    </div>
    <div class="collection-body">
      <ul v-if="store.filteredCollections.length">
        <li
          v-for="col in store.filteredCollections"
          :key="col.id"
          :class="{ active: col.id === store.activeCollectionId, dragging: dragColId === col.id }"
          :draggable="!store.hasSearch"
          @click="handleCollectionClick(col.id)"
          @dragstart="onDragStartCol($event, col.id)"
          @dragend="onDragEndCol"
          @dragover="onDragOverCol"
          @drop="onDropCol($event, col.id)"
        >
          <span class="col-icon">
            <TypeIcon :type="col.type ?? '目录集合'" :size="22" />
          </span>
          <div class="col-info">
            <span class="col-name">{{ col.name }}</span>
            <span class="col-meta">{{ col.type }}</span>
          </div>
          <span class="col-actions" @click.stop>
            <button class="action-btn" @click="handleOpenAll(col.id)" :title="t('openAll')">
              <Play :size="13" />
            </button>
            <button class="action-btn" @click="editingCollection = col; showEditDialog = true" :title="t('edit')">
              <Pencil :size="13" />
            </button>
            <button class="action-btn danger" @click="handleDeleteCollection(col.id)" :title="t('delete')">
              <Trash2 :size="13" />
            </button>
          </span>
        </li>
      </ul>
      <div v-else class="collection-empty">
        <template v-if="store.hasSearch">
          <p class="empty-text">{{ t('noMatchCollections') }}</p>
          <p class="empty-hint">{{ t('tryOtherKeywords') }}</p>
        </template>
        <template v-else-if="store.activeSceneId">
          <p class="empty-icon">+</p>
          <p class="empty-text">{{ t('noCollections') }}</p>
          <p class="empty-hint">{{ t('createFirstCollection') }}</p>
        </template>
        <template v-else>
          <p class="empty-text muted">{{ t('selectSceneFirst') }}</p>
        </template>
      </div>
    </div>

    <CreateDialog :visible="showCollectionDialog" :title="t('addCollection')" :fields="collectionFields"
      @confirm="handleCreateCollection" @cancel="showCollectionDialog = false" />
    <CreateDialog :visible="showEditDialog" :title="t('editCollection')" :fields="collectionFields"
      :editValues="editingCollection ? { name: editingCollection.name, type: editingCollection.type ?? '目录集合', openStrategy: editingCollection.openStrategy ?? 'single' } : undefined"
      @confirm="handleEditCollection" @cancel="showEditDialog = false; editingCollection = null" />
  </section>
</template>

<style scoped>
.collection-list {
  width: 300px; min-width: 300px; height: 100%;
  background: var(--color-bg-secondary); border-right: 1px solid var(--color-border);
  display: flex; flex-direction: column; overflow: hidden;
}
.collection-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 12px 14px 12px 18px; box-shadow: var(--shadow-border);
}
.collection-title {
  font-size: 11px; font-weight: 600; color: var(--color-text-muted);
  text-transform: uppercase; letter-spacing: 1px;
}
.icon-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 6px; transition: all 0.15s;
}
.icon-btn:hover { color: var(--color-accent); background: var(--color-bg-hover); }
.collection-body { flex: 1; overflow-y: auto; padding: 6px 8px; }
.collection-body ul { list-style: none; padding: 0; margin: 0; }
.collection-body li {
  display: flex; align-items: center; gap: 12px;
  padding: 10px 14px; cursor: pointer; color: var(--color-text-secondary);
  font-size: 13px; transition: all var(--transition-fast); border-radius: var(--radius-md);
  margin-bottom: var(--space-1);
}
.collection-body li:hover { background: var(--color-bg-tertiary); color: var(--color-text-primary); }
.collection-body li.active {
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  box-shadow: inset 0 0 0 1px var(--color-accent-border);
}
.col-icon {
  display: flex; align-items: center; justify-content: center;
  width: 36px; height: 36px; flex-shrink: 0;
  background: var(--color-bg-tertiary); border-radius: 8px; color: var(--color-text-muted);
}
.collection-body li.active .col-icon {
  background: var(--color-accent-bg); color: var(--color-accent);
}
.col-info { display: flex; flex-direction: column; overflow: hidden; flex: 1; gap: 2px; }
.col-name {
  font-size: 13px; font-weight: 500;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.col-meta { font-size: 11px; color: var(--color-text-disabled); }
.collection-body li.active .col-meta { color: var(--color-text-muted); }
.col-actions {
  display: flex; gap: 2px; opacity: 0; transition: opacity 0.12s;
}
.collection-body li:hover .col-actions { opacity: 1; }
.action-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  width: 26px; height: 26px; border-radius: 5px; transition: all 0.12s;
  display: flex; align-items: center; justify-content: center;
}
.action-btn:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.action-btn.danger:hover { color: var(--color-danger); background: rgba(255,77,79,0.1); }

.collection-body li.dragging { opacity: 0.4; }
.collection-body li[draggable="true"] { cursor: grab; }
.collection-body li[draggable="true"]:active { cursor: grabbing; }

.collection-empty {
  padding: 40px 16px; text-align: center;
}
.empty-icon {
  width: 44px; height: 44px; border-radius: 50%; border: 2px dashed var(--color-border);
  color: var(--color-text-disabled); font-size: 20px; display: flex; align-items: center;
  justify-content: center; margin: 0 auto 14px;
}
.empty-text { font-size: 13px; color: var(--color-text-disabled); margin-bottom: 4px; }
.empty-text.muted { color: var(--color-text-disabled); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); }
</style>
