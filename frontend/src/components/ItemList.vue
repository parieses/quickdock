<script setup lang="ts">
import { ref, inject, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { Plus, Pencil, Trash2, Play, Link, Search, X } from '@lucide/vue'
import TypeIcon from './TypeIcon.vue'
import ItemEditor from './ItemEditor.vue'
import { getErrorMessage } from '../utils/error'
import { matchesTextOrPinyin } from '../utils/pinyin'
import type { CollectionItem, ToastAPI } from '../types'

const store = useWorkspaceStore()
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 局部过滤 ----
// 与 CollectionList 同构：全局搜索跨集合，这里只在当前集合内按 名称+值 过滤
// （支持拼音），大集合里定位单条不用翻屏。过滤态禁用拖拽排序。
const localQuery = ref('')
const filtering = computed(() => localQuery.value.trim().length > 0)

const visibleItems = computed(() => {
  const q = localQuery.value.trim().toLowerCase()
  const base = store.filteredItems
  if (!q) return base
  return base.filter(it => matchesTextOrPinyin([it.name, it.value], q, 'i:' + it.id))
})

// 换集合时清空：上一个集合的关键词不该接着筛这一集合
watch(() => store.activeCollectionId, () => { localQuery.value = '' })

const showEditor = ref(false)
const editingItem = ref<CollectionItem | null>(null)

async function handleItemClick(item: CollectionItem) {
  try {
    await store.openItem(item)
    toast.success(t('itemLaunched') + ': ' + item.name)
  } catch (e) {
    toast.error(t('itemLaunchFailed') + ': ' + item.name + ' — ' + getErrorMessage(e))
  }
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
  // 没有选中集合时不允许新增（按钮此时为禁用态，这里再兜底一次）
  if (!store.activeCollectionId) return
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
  if (store.hasSearch || filtering.value) {
    dragItemId.value = null
    return
  }
  const ids = store.filteredItems.map(it => it.id)
  const from = ids.indexOf(dragItemId.value!)
  const to = ids.indexOf(targetId)
  if (from < 0 || to < 0 || from === to) {
    dragItemId.value = null
    return
  }
  ids.splice(from, 1)
  ids.splice(to, 0, dragItemId.value!)
  store.reorderItems(ids)
  dragItemId.value = null
}
</script>

<template>
  <section class="item-list qd-panel">
    <div class="qd-panel-header">
      <span class="qd-panel-title">{{ t('items') }}</span>
      <span v-if="store.activeCollection" class="qd-panel-chip">{{ store.activeCollection.name }}</span>
      <div class="qd-panel-actions">
        <button
          class="qd-icon-btn"
          :disabled="!store.activeCollectionId || !visibleItems.length || store.hasSearch || filtering"
          @click="handleOpenAll"
          :title="t('openAll')"
        >
          <Play :size="15" />
        </button>
        <button
          class="qd-icon-btn"
          :disabled="!store.activeCollectionId || store.hasSearch"
          @click="handleAddItem"
          :title="store.activeCollectionId && !store.hasSearch ? t('addItem') : t('selectCollectionFirst')"
        >
          <Plus :size="16" />
        </button>
      </div>
    </div>
    <!-- 局部过滤：只搜当前集合内的条目；ESC 清空。
         与场景/集合栏同位置同高度（无集合时保留输入框但禁用，避免整行消失） -->
    <div class="qd-panel-search" :class="{ 'is-disabled': !store.activeCollectionId || store.hasSearch }">
      <Search :size="13" class="qd-search-icon" />
      <input
        v-model="localQuery"
        type="text"
        class="qd-search-input"
        :placeholder="t('filterItemsPh')"
        autocomplete="off"
        :disabled="!store.activeCollectionId || store.hasSearch"
        @keydown.esc="localQuery = ''"
      />
      <button v-if="filtering" class="qd-search-clear" @click="localQuery = ''" :title="t('clear')">
        <X :size="12" />
      </button>
    </div>
    <div class="qd-panel-body">
      <ul v-if="visibleItems.length">
        <li
          v-for="item in visibleItems"
          :key="item.id"
          class="qd-row"
          :class="{ dragging: dragItemId === item.id }"
          :draggable="!store.hasSearch && !filtering"
          @click="handleItemClick(item)"
          @dragstart="onDragStartItem($event, item.id)"
          @dragend="onDragEndItem"
          @dragover="onDragOverItem"
          @drop="onDropItem($event, item.id)"
        >
          <span class="qd-row-icon">
            <img v-if="item.icon" :src="item.icon" class="item-app-icon-img" alt="" />
            <TypeIcon v-else :type="item.type" :size="17" />
          </span>
          <span class="qd-row-main">
            <span class="qd-row-title">{{ item.name }}</span>
            <span class="qd-row-sub">{{ item.value }}</span>
          </span>
          <span v-if="item.value && item.value.includes('{query}')" class="item-quicklink-badge" :title="t('quicklink')">
            <Link :size="11" />
            <span>{{ t('quicklinkBadge') }}</span>
          </span>
          <span v-if="item.usageCount" class="item-usage" :title="t('openCount') + item.usageCount + t('times')">
            {{ item.usageCount }}
          </span>
          <span class="qd-row-actions" @click.stop>
            <button class="qd-row-btn" @click="handleItemClick(item)" :title="t('open')">
              <Play :size="13" />
            </button>
            <button class="qd-row-btn" @click="handleEditItem(item)" :title="t('edit')">
              <Pencil :size="13" />
            </button>
            <button class="qd-row-btn danger" @click="handleDeleteItem(item.id)" :title="t('delete')">
              <Trash2 :size="13" />
            </button>
          </span>
        </li>
      </ul>
      <div v-else class="qd-empty">
        <template v-if="filtering || store.hasSearch">
          <p class="qd-empty-title">{{ t('noMatchItems') }}</p>
          <p class="qd-empty-hint">{{ t('tryOtherKeywords') }}</p>
        </template>
        <template v-else-if="store.activeCollectionId">
          <p class="qd-empty-title">{{ t('noItems') }}</p>
          <p class="qd-empty-hint">{{ t('createFirstItem') }}</p>
          <button class="qd-empty-cta" @click="handleAddItem">
            <Plus :size="13" />
            <span>{{ t('addItem') }}</span>
          </button>
        </template>
        <template v-else-if="!store.collectionsLoading">
          <p class="qd-empty-title">{{ t('selectCollectionFirst') }}</p>
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
/* 面板头 / 搜索行 / 行 / 空状态的公共外观在 styles/panels.css（qd- 前缀），
   这里只留项目栏特有的部分 */
.item-list {
  flex: 1;
  min-width: 0;
  background: var(--color-bg-primary);
}

/* 应用图标是位图，不能跟着文字色走，尺寸也固定 */
.item-app-icon-img {
  width: 20px;
  height: 20px;
  object-fit: contain;
  border-radius: var(--radius-xs);
}

/* 打开次数徽标 */
.item-usage {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  font-size: 10px;
}

/* 快捷链接徽标（value 里含 {query} 的条目） */
.item-quicklink-badge {
  display: flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-size: 10px;
}
</style>
