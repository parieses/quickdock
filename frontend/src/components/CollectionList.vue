<script setup lang="ts">
import { ref, inject, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { Plus, Pencil, Trash2, Play, Search, X } from '@lucide/vue'
import TypeIcon from './TypeIcon.vue'
import CreateDialog from './CreateDialog.vue'
import { getErrorMessage } from '../utils/error'
import { matchesTextOrPinyin } from '../utils/pinyin'
import { useCollectionFields } from '../composables/useCollectionFields'
import type { Collection, ToastAPI } from '../types'

const store = useWorkspaceStore()
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 局部过滤 ----
// 全局搜索（SceneRail）跨全部已打开场景，集合多了以后「在这一屏里定位一个集合」
// 仍要逐行看。局部过滤只作用于当前场景下的集合，与全局搜索叠加生效：
// 全局搜索结果集 → 本框再过滤。拖拽排序在过滤态禁用（重排一个被裁过的列表会错位）。
const localQuery = ref('')
const filtering = computed(() => localQuery.value.trim().length > 0)

const visibleCollections = computed(() => {
  const q = localQuery.value.trim().toLowerCase()
  const base = store.filteredCollections
  if (!q) return base
  return base.filter(c => matchesTextOrPinyin([c.name, c.type], q, 'c:' + c.id))
})

// 换场景时清空局部过滤：留着上个场景的关键词，新场景列表会「莫名其妙少一半」
watch(() => store.activeSceneId, () => { localQuery.value = '' })

const showCollectionDialog = ref(false)
// 字段定义与「无集合」整页引导共用（composables/useCollectionFields），不在此处重复声明
const collectionFields = useCollectionFields()

const editingCollection = ref<Collection | null>(null)
const showEditDialog = ref(false)

function handleCollectionClick(collectionId: string) {
  store.selectCollection(collectionId)
}

// 「打开」按集合自己的 open_strategy 执行（single / batch / all）。
// 之前这个字段只在表单里存在，选了没有任何行为差异。
async function handleOpenByStrategy(col: Collection) {
  try {
    const n = await store.openCollectionByStrategy(col)
    if (n === 0) toast.success(t('collectionHasNoItems'))
    else if (n > 0) toast.success(t('collectionOpenedN', { n }))
  } catch (e) {
    toast.error(t('openAllFailed') + ': ' + getErrorMessage(e))
  }
}

function openStrategyTitle(col: Collection) {
  const s = (col.openStrategy || 'single') as 'single' | 'batch' | 'all'
  return t('openStrategy') + ': ' + t(`openStrategies.${s}`)
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
  if (store.hasSearch || filtering.value) {
    dragColId.value = null
    return
  }
  const ids = store.filteredCollections.map(c => c.id)
  const from = ids.indexOf(dragColId.value!)
  const to = ids.indexOf(targetId)
  if (from < 0 || to < 0 || from === to) {
    dragColId.value = null
    return
  }
  ids.splice(from, 1)
  ids.splice(to, 0, dragColId.value!)
  store.reorderCollections(ids)
  dragColId.value = null
}
</script>

<template>
  <section class="collection-list qd-panel">
    <div class="qd-panel-header">
      <span class="qd-panel-title">{{ t('collections') }}</span>
      <div class="qd-panel-actions">
        <button
          class="qd-icon-btn"
          :disabled="!store.activeSceneId || store.hasSearch"
          :title="store.activeSceneId && !store.hasSearch ? t('addCollection') : t('selectSceneFirst')"
          @click="showCollectionDialog = true"
        >
          <Plus :size="16" />
        </button>
      </div>
    </div>
    <!-- 局部过滤：只搜当前场景下的集合；ESC 清空。
         与场景/项目栏同位置同高度（无场景时保留输入框但禁用，避免整行消失） -->
    <div class="qd-panel-search" :class="{ 'is-disabled': !store.activeSceneId || store.hasSearch }">
      <Search :size="13" class="qd-search-icon" />
      <input
        v-model="localQuery"
        type="text"
        class="qd-search-input"
        :placeholder="t('filterCollectionsPh')"
        autocomplete="off"
        :disabled="!store.activeSceneId || store.hasSearch"
        @keydown.esc="localQuery = ''"
      />
      <button v-if="filtering" class="qd-search-clear" @click="localQuery = ''" :title="t('clear')">
        <X :size="12" />
      </button>
    </div>
    <div class="qd-panel-body">
      <ul v-if="visibleCollections.length">
        <li
          v-for="col in visibleCollections"
          :key="col.id"
          class="qd-row"
          :class="{ active: col.id === store.activeCollectionId, dragging: dragColId === col.id }"
          :draggable="!store.hasSearch && !filtering"
          @click="handleCollectionClick(col.id)"
          @dragstart="onDragStartCol($event, col.id)"
          @dragend="onDragEndCol"
          @dragover="onDragOverCol"
          @drop="onDropCol($event, col.id)"
        >
          <span class="qd-row-icon">
            <TypeIcon :type="col.type ?? '目录集合'" :size="17" />
          </span>
          <span class="qd-row-main">
            <span class="qd-row-title">{{ col.name }}</span>
            <span class="qd-row-sub">{{ col.type }}</span>
          </span>
          <span class="qd-row-actions" @click.stop>
            <button class="qd-row-btn" @click="handleOpenByStrategy(col)" :title="openStrategyTitle(col)">
              <Play :size="13" />
            </button>
            <button class="qd-row-btn" @click="editingCollection = col; showEditDialog = true" :title="t('edit')">
              <Pencil :size="13" />
            </button>
            <button class="qd-row-btn danger" @click="handleDeleteCollection(col.id)" :title="t('delete')">
              <Trash2 :size="13" />
            </button>
          </span>
        </li>
      </ul>
      <div v-else class="qd-empty">
        <template v-if="filtering || store.hasSearch">
          <p class="qd-empty-title">{{ t('noMatchCollections') }}</p>
          <p class="qd-empty-hint">{{ t('tryOtherKeywords') }}</p>
        </template>
        <template v-else-if="store.activeSceneId">
          <span class="qd-empty-icon"><Plus :size="18" /></span>
          <p class="qd-empty-title">{{ t('noCollections') }}</p>
          <p class="qd-empty-hint">{{ t('createFirstCollection') }}</p>
          <button class="qd-empty-cta" @click="showCollectionDialog = true">
            <Plus :size="13" />
            <span>{{ t('addCollection') }}</span>
          </button>
        </template>
        <template v-else-if="!store.scenesLoading">
          <p class="qd-empty-title">{{ t('selectSceneFirst') }}</p>
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
/* 面板头 / 搜索行 / 行 / 空状态的公共外观在 styles/panels.css（qd- 前缀），
   这里只留集合栏特有的部分 */
.collection-list {
  /* 集合行的信息只有「名称 + 类型」两行短文字，300px 太奢侈；收窄到 240px。
     再窄就放不下 3 个 22px 行尾按钮 + 名称了。 */
  width: 240px;
  min-width: 240px;
  flex-shrink: 0;
  background: var(--color-bg-primary);
  border-right: 1px solid var(--color-border);
}
</style>
