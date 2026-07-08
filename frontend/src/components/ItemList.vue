<script setup lang="ts">
import { ref, inject, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { Plus, Pencil, Trash2, Play } from '@lucide/vue'
import TypeIcon from './TypeIcon.vue'
import ItemEditor from './ItemEditor.vue'
import { getErrorMessage } from '../utils/error'
import type { CollectionItem, ToastAPI } from '../types'

const store = useWorkspaceStore()
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const showEditor = ref(false)
const editingItem = ref<CollectionItem | null>(null)

function handleItemClick(item: CollectionItem) {
  store.openItem(item)
}

async function handleOpenAll() {
  if (!store.activeCollectionId) return
  try {
    await store.openAllInCollection(store.activeCollectionId)
  } catch (e) {
    toast.error(t('openAllFailed') + ': ' + getErrorMessage(e))
  }
}

function handleAddItem() {
  editingItem.value = null
  showEditor.value = true
}

function handleEditItem(item: CollectionItem) {
  editingItem.value = item
  showEditor.value = true
}

async function handleSaveItem(data: Partial<CollectionItem>) {
  try {
    if (editingItem.value) {
      await store.updateItemAction(editingItem.value.id, data)
    } else {
      await store.addItem(data.name || '', data.type || '目录', data.value || '')
    }
    showEditor.value = false
    editingItem.value = null
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleDeleteItem(itemId: string) {
  if (!(await toast.confirm(t('confirmDeleteItem')))) return
  try { await store.removeItem(itemId) } catch (e) { toast.error(t('deleteFailed') + ': ' + getErrorMessage(e)) }
}

// 拖拽排序
const dragItemId = ref<string | null>(null)

function onDragStartItem(e: DragEvent, itemId: string) {
  dragItemId.value = itemId
  e.dataTransfer?.setData('text/plain', itemId)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}
function onDragEndItem() {
  dragItemId.value = null
}
function onDragOverItem(e: DragEvent) {
  e.preventDefault()
}
function onDropItem(e: DragEvent, targetId: string) {
  e.preventDefault()
  const ids = store.filteredItems.map(it => it.id)
  const from = ids.indexOf(dragItemId.value!)
  const to = ids.indexOf(targetId)
  if (from < 0 || to < 0 || from === to) return
  ids.splice(from, 1)
  ids.splice(to, 0, dragItemId.value!)
  if (store.hasSearch) return
  store.reorderItems(ids)
  dragItemId.value = null
}
</script>

<template>
  <section class="item-list">
    <div class="item-header">
      <div class="header-left">
        <span class="item-title">{{ store.hasSearch ? t('searchResults') : (store.activeCollection?.name || t('items')) }}</span>
        <span v-if="store.filteredItems.length" class="item-count">{{ store.filteredItems.length }} {{ t('count') }}</span>
      </div>
      <div class="header-actions">
        <button v-if="store.activeCollectionId && store.filteredItems.length && !store.hasSearch" class="action-btn" @click="handleOpenAll" :title="t('openAll')">
          <Play :size="13" />
        </button>
        <button v-if="store.activeCollectionId && !store.hasSearch" class="action-btn" @click="handleAddItem" :title="t('addItem')">
          <Plus :size="16" />
        </button>
      </div>
    </div>
    <div class="item-body">
      <ul v-if="store.filteredItems.length">
        <li
          v-for="item in store.filteredItems"
          :key="item.id"
          :class="{ dragging: dragItemId === item.id }"
          :draggable="!store.hasSearch"
          @click="handleItemClick(item)"
          @dragstart="onDragStartItem($event, item.id)"
          @dragend="onDragEndItem"
          @dragover="onDragOverItem"
          @drop="onDropItem($event, item.id)"
        >
          <span class="item-icon">
            <TypeIcon :type="item.type" :size="18" />
          </span>
          <div class="item-info">
            <span class="item-name">{{ item.name }}</span>
            <span class="item-meta">{{ item.value }}</span>
          </div>
          <span v-if="item.usageCount" class="item-usage" :title="t('openCount') + item.usageCount + t('times')">
            {{ item.usageCount }}
          </span>
          <span class="item-actions" @click.stop>
            <button class="action-btn" @click="handleItemClick(item)" :title="t('open')">
              <Play :size="13" />
            </button>
            <button class="action-btn" @click="handleEditItem(item)" :title="t('edit')">
              <Pencil :size="13" />
            </button>
            <button class="action-btn danger" @click="handleDeleteItem(item.id)" :title="t('delete')">
              <Trash2 :size="13" />
            </button>
          </span>
        </li>
      </ul>
      <div v-else class="item-empty">
        <template v-if="store.hasSearch">
          <p class="empty-text">{{ t('noMatchItems') }}</p>
          <p class="empty-hint">{{ t('tryOtherKeywords') }}</p>
        </template>
        <template v-else-if="store.activeCollectionId">
          <p class="empty-text">{{ t('noItems') }}</p>
          <p class="empty-hint">{{ t('createFirstItem') }}</p>
        </template>
        <template v-else>
          <p class="empty-text muted">{{ t('selectCollectionFirst') }}</p>
        </template>
      </div>
    </div>

    <ItemEditor
      :visible="showEditor"
      :item="editingItem"
      @save="handleSaveItem"
      @cancel="showEditor = false; editingItem = null"
    />
  </section>
</template>

<style scoped>
.item-list {
  flex: 1; min-width: 0; height: 100%;
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--color-bg-primary);
}
.item-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 18px; border-bottom: 1px solid var(--color-border);
}
.header-left { display: flex; align-items: center; gap: 10px; }
.item-title { font-size: 14px; font-weight: 600; color: var(--color-text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.item-count { font-size: 11px; color: var(--color-text-disabled); }
.header-actions { display: flex; gap: 4px; }
.item-body { flex: 1; overflow-y: auto; padding: 8px 0; }
.item-body ul { list-style: none; }
.item-body li {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 18px; cursor: pointer; color: var(--color-text-secondary);
  font-size: 13px; transition: all 0.12s;
}
.item-body li:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.item-icon {
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; flex-shrink: 0; color: var(--color-text-muted);
}
.item-info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.item-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.item-meta { font-size: 11px; color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.item-usage {
  font-size: 10px; color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 1px 6px; border-radius: 8px;
}
.item-actions { display: flex; gap: 2px; opacity: 0; transition: opacity 0.12s; }
.item-body li:hover .item-actions { opacity: 1; }
.action-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  width: 26px; height: 26px; border-radius: 5px; transition: all 0.12s;
  display: flex; align-items: center; justify-content: center;
}
.action-btn:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.action-btn.danger:hover { color: var(--color-danger); background: rgba(255,77,79,0.1); }

.item-body li.dragging { opacity: 0.4; }
.item-body li[draggable="true"] { cursor: grab; }
.item-body li[draggable="true"]:active { cursor: grabbing; }

.item-empty { padding: 48px 16px; text-align: center; }
.empty-text { font-size: 13px; color: var(--color-text-disabled); margin-bottom: 4px; }
.empty-text.muted { color: var(--color-text-disabled); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); }
</style>
