<script setup lang="ts">
import { ref, inject, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { Plus, Pencil, Trash2, Play, Link, Search, X } from '@lucide/vue'
import TypeIcon from './TypeIcon.vue'
import ItemEditor from './ItemEditor.vue'
import CreateDialog, { type CreateField } from './CreateDialog.vue'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { DeleteItem, UpdateItem } from '../../bindings/quickdock/services/item/itemservice'
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

// ---- 虚拟滚动（仅超大集合窗口化，保留拖拽排序）----
// 普通集合（<=VIRTUAL_THRESHOLD）走原始 v-for，外观 / 拖拽完全不变；
// 超过阈值才窗口化：只渲染可视区 + 上下缓冲的行，避免数千条一次性建 DOM。
// 行高固定 ROW_H（与 .qd-row 实际高度对齐，强制 44px 消除「有无副标题」的高度差）；
// 拖拽落点用 store.filteredItems 全量下标，不依赖 DOM 上哪些行被渲染，故与重排兼容。
const VIRTUAL_THRESHOLD = 100
const ROW_H = 44
const BUFFER = 6
const scrollEl = ref<HTMLElement | null>(null)
const scrollTop = ref(0)
const viewportH = ref(0)
const useVirtual = computed(() => visibleItems.value.length > VIRTUAL_THRESHOLD)
const total = computed(() => visibleItems.value.length)
const startIndex = computed(() => Math.max(0, Math.floor(scrollTop.value / ROW_H) - BUFFER))
const endIndex = computed(() => Math.min(total.value, Math.ceil((scrollTop.value + viewportH.value) / ROW_H) + BUFFER))
const slice = computed(() => visibleItems.value.slice(startIndex.value, endIndex.value))
const offsetY = computed(() => startIndex.value * ROW_H)
const totalH = computed(() => total.value * ROW_H)
const bottomPad = computed(() => Math.max(0, totalH.value - offsetY.value - slice.value.length * ROW_H))

function onScroll(e: Event) {
  const el = e.target as HTMLElement
  scrollTop.value = el.scrollTop
  // 顺手刷新可视高：避免某些布局时机下 onMounted 测量为 0 导致首屏只渲染缓冲行
  viewportH.value = el.clientHeight
}
function measureViewport() {
  if (scrollEl.value) viewportH.value = scrollEl.value.clientHeight
}
onMounted(measureViewport)
window.addEventListener('resize', measureViewport)
onBeforeUnmount(() => window.removeEventListener('resize', measureViewport))

const showEditor = ref(false)
const editingItem = ref<CollectionItem | null>(null)

// ---- 快速链接的 {query} 占位符 ----
//
// 命令面板会就地替换 {query}（useCommandSearch 内联那几行），但从列表直接点
// 走的是 store.openItem → 后端 execOpen → expandItemVars，而它只认
// {date}/{time}/{username}/{clipboard}，{query} 会原样留在 URL 里 → 打开必然 404。
// 这里在点击时先问一句，把值编码后拼进去再交给后端；模板本身不改。
const quickItem = ref<CollectionItem | null>(null)
const showQueryDialog = ref(false)
const queryField = computed<CreateField[]>(() => [
  {
    key: 'query',
    label: t('quicklinkQueryLabel'),
    type: 'text',
    placeholder: t('quicklinkQueryPlaceholder'),
    hint: t('enterQueryHint'),
  },
])

function isQuicklink(item: CollectionItem) {
  return !!item.value && item.value.includes('{query}')
}

async function launch(item: CollectionItem, value?: string) {
  try {
    await store.openItem(value === undefined ? item : { ...item, value })
    toast.success(t('itemLaunched') + ': ' + item.name)
  } catch (e) {
    toast.error(t('itemLaunchFailed') + ': ' + item.name + ' — ' + getErrorMessage(e))
  }
}

async function handleItemClick(item: CollectionItem) {
  if (isQuicklink(item)) {
    quickItem.value = item
    showQueryDialog.value = true
    return
  }
  await launch(item)
}

function onQueryConfirm(values: Record<string, string>) {
  const item = quickItem.value
  showQueryDialog.value = false
  quickItem.value = null
  if (!item) return
  const q = (values.query || '').trim()
  if (!q) return
  // 编码后拼入：模板形如 https://x/search?q={query}，不编码遇到空格或 & 就会截断
  void launch(item, item.value.replace(/\{query\}/g, encodeURIComponent(q)))
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

// ---- 多选批量删除 / 批量移动 ----
// 十几条过期路径想一起清掉，逐条右键删除要点十几轮；移动同理。
// 选择只在当前集合内生效（切集合清空），移动目标也只在同一场景的其它集合里选——
// 跨场景移动会连带 workspace 变化，语义上不是「移动」而是「迁移」，不做。
const selIds = ref<Set<string>>(new Set())
const batchBusy = ref(false)
const allSel = computed(
  () => visibleItems.value.length > 0 && visibleItems.value.every(i => selIds.value.has(i.id)),
)
function isSel(id: string): boolean { return selIds.value.has(id) }
function toggleSel(id: string) {
  const next = new Set(selIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selIds.value = next
}
function toggleSelAll() {
  const on = !allSel.value
  const next = new Set(selIds.value)
  for (const it of visibleItems.value) {
    if (on) next.add(it.id)
    else next.delete(it.id)
  }
  selIds.value = next
}
function clearSel() { if (selIds.value.size) selIds.value = new Set() }
// 换集合清空选择：上一个集合的勾选不该带到下一个集合
watch(() => store.activeCollectionId, () => { selIds.value = new Set() })

/** 除当前集合外的移动目标（同一场景内）。 */
const otherCollections = computed(() => store.collections.filter(c => c.id !== store.activeCollectionId))

async function batchDelete() {
  const ids = [...selIds.value]
  if (!ids.length || batchBusy.value) return
  if (!(await toast.confirm(t('wsBatchDeleteConfirm', { n: ids.length })))) return
  batchBusy.value = true
  let failed = 0
  for (const id of ids) {
    try { unwrap(await DeleteItem(id)) } catch { failed++ }
  }
  batchBusy.value = false
  clearSel()
  if (store.activeCollectionId) await store.fetchItems(store.activeCollectionId)
  if (!failed) toast.success(t('wsBatchDeleted', { n: ids.length }))
  else toast.error(t('wsBatchPartial', { n: failed }))
}

const moveMenuOpen = ref(false)
async function batchMove(targetId: string) {
  const ids = [...selIds.value]
  moveMenuOpen.value = false
  if (!ids.length || batchBusy.value) return
  batchBusy.value = true
  let failed = 0
  for (const id of ids) {
    try { unwrap(await UpdateItem(id, { collection_id: targetId })) } catch { failed++ }
  }
  batchBusy.value = false
  clearSel()
  if (store.activeCollectionId) await store.fetchItems(store.activeCollectionId)
  if (!failed) toast.success(t('wsBatchMoved', { n: ids.length }))
  else toast.error(t('wsBatchPartial', { n: failed }))
}

// ---- 右键菜单 ----
// 三栏此前完全没有右键：想看"这条到底存的是什么路径"、想直接跳到资源管理器，
// 都得先打开编辑框。菜单项按类型给：只有值看起来像本地路径时才出现「在资源管理器显示」。
interface CtxAction {
  id: string
  label: string
  danger?: boolean
  run: () => void
}

const ctx = ref<{ x: number; y: number; item: CollectionItem } | null>(null)

function openContextMenu(e: MouseEvent, item: CollectionItem) {
  ctx.value = { x: e.clientX, y: e.clientY, item }
  // 贴右/贴底时往回收，否则菜单会被窗口裁掉一半
  nextTick(() => {
    const c = ctx.value
    if (!c) return
    c.x = Math.min(c.x, window.innerWidth - 184)
    c.y = Math.min(c.y, window.innerHeight - 196)
  })
}

function closeContextMenu() {
  ctx.value = null
}

/** 本地路径判定：盘符开头或 UNC。网址/命令/应用名不该出现「在资源管理器显示」。 */
function isLocalPath(v: string): boolean {
  return /^[a-zA-Z]:[\\/]/.test(v) || v.startsWith('\\\\')
}

async function copyItemValue(item: CollectionItem) {
  try {
    await navigator.clipboard.writeText(item.value || '')
    toast.success(t('copied'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

async function revealItemPath(item: CollectionItem) {
  try {
    unwrap(await RevealInExplorer(item.value))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 菜单项用 computed 生成：模板里就不必在 v-if 内反复判 ctx.item 非空，
// 也顺手把「哪些项该出现」的判断集中在一处。
const ctxActions = computed<CtxAction[]>(() => {
  const it = ctx.value?.item
  if (!it) return []
  const acts: CtxAction[] = [
    { id: 'open', label: t('open'), run: () => void handleItemClick(it) },
    { id: 'copy', label: t('copyValue'), run: () => void copyItemValue(it) },
  ]
  if (isLocalPath(it.value || '')) {
    acts.push({ id: 'reveal', label: t('actReveal'), run: () => void revealItemPath(it) })
  }
  acts.push({ id: 'edit', label: t('edit'), run: () => handleEditItem(it) })
  acts.push({ id: 'del', label: t('delete'), danger: true, run: () => void handleDeleteItem(it.id) })
  return acts
})

// Esc 关闭：右键菜单是最容易被"忘了关"的浮层
function onCtxKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && ctx.value) {
    e.stopPropagation()
    closeContextMenu()
  }
}
onMounted(() => document.addEventListener('keydown', onCtxKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onCtxKeydown))

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
    <!-- 批量操作条：勾选任意条目后出现。移动目标限同场景其它集合 -->
    <div v-if="selIds.size" class="item-batch">
      <label class="item-selall" :title="t('wsSelAll')">
        <input type="checkbox" :checked="allSel" @change="toggleSelAll" />
      </label>
      <span class="item-batch-count">{{ t('wsSelCount', { n: selIds.size }) }}</span>
      <span class="item-batch-fill" />
      <div class="batch-move-wrap">
        <button class="qd-btn qd-btn-sm" :disabled="batchBusy || !otherCollections.length"
                :title="otherCollections.length ? t('wsBatchMove') : t('wsMoveTargetEmpty')"
                @click="moveMenuOpen = !moveMenuOpen">
          {{ t('wsBatchMove') }}
        </button>
        <div v-if="moveMenuOpen && otherCollections.length" class="batch-move-menu">
          <button v-for="c in otherCollections" :key="c.id" class="batch-move-item" @click="batchMove(c.id)">
            {{ c.name }}
          </button>
        </div>
      </div>
      <button class="qd-btn qd-btn-sm qd-btn-danger" :disabled="batchBusy" @click="batchDelete">{{ t('delete') }}</button>
      <button class="qd-btn qd-btn-ghost qd-btn-sm" @click="clearSel">{{ t('cancel') }}</button>
    </div>
    <div class="qd-panel-body" ref="scrollEl" @scroll.passive="onScroll">
      <div v-if="useVirtual" class="item-vlist">
        <div class="item-vspacer" :style="{ height: offsetY + 'px' }"></div>
        <div
          v-for="item in slice"
          :key="item.id"
          class="qd-row"
          :class="{ dragging: dragItemId === item.id, picked: isSel(item.id) }"
          :style="item.color ? { boxShadow: 'inset 3px 0 0 ' + item.color } : undefined"
          :draggable="!store.hasSearch && !filtering"
          @click="handleItemClick(item)"
          @contextmenu.prevent="openContextMenu($event, item)"
          @dragstart="onDragStartItem($event, item.id)"
          @dragend="onDragEndItem"
          @dragover="onDragOverItem"
          @drop="onDropItem($event, item.id)"
        >
          <input type="checkbox" class="item-check" :checked="isSel(item.id)"
                 :title="t('wsSelHint')" @click.stop @change="toggleSel(item.id)" />
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
        </div>
        <div class="item-vspacer" :style="{ height: bottomPad + 'px' }"></div>
      </div>
      <ul v-else-if="visibleItems.length">
        <li
          v-for="item in visibleItems"
          :key="item.id"
          class="qd-row"
          :class="{ dragging: dragItemId === item.id, picked: isSel(item.id) }"
          :style="item.color ? { boxShadow: 'inset 3px 0 0 ' + item.color } : undefined"
          :draggable="!store.hasSearch && !filtering"
          @click="handleItemClick(item)"
          @contextmenu.prevent="openContextMenu($event, item)"
          @dragstart="onDragStartItem($event, item.id)"
          @dragend="onDragEndItem"
          @dragover="onDragOverItem"
          @drop="onDropItem($event, item.id)"
        >
          <input type="checkbox" class="item-check" :checked="isSel(item.id)"
                 :title="t('wsSelHint')" @click.stop @change="toggleSel(item.id)" />
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

    <!-- 右键菜单：遮罩点哪都关，菜单自身 stop 掉 -->
    <div v-if="ctx" class="ctx-backdrop" @click="closeContextMenu" @contextmenu.prevent="closeContextMenu">
      <div class="ctx-menu" :style="{ left: ctx.x + 'px', top: ctx.y + 'px' }" @click.stop>
        <button
          v-for="a in ctxActions"
          :key="a.id"
          :class="['ctx-item', { danger: a.danger }]"
          @click="a.run(); closeContextMenu()"
        >{{ a.label }}</button>
      </div>
    </div>

    <ItemEditor
      :visible="showEditor"
      :item="editingItem"
      @save="handleSaveItem"
      @cancel="showEditor = false; editingItem = null"
    />

    <CreateDialog
      :visible="showQueryDialog"
      :title="t('quicklinkQueryTitle')"
      :fields="queryField"
      @confirm="onQueryConfirm"
      @cancel="showQueryDialog = false; quickItem = null"
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
/* 右键菜单：fixed 定位 + 透明遮罩，与项目内其它浮层同一套语言 */
.ctx-backdrop {
  position: fixed; inset: 0; z-index: 900;
}
.ctx-menu {
  position: fixed; min-width: 168px; padding: 4px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: var(--radius-md); box-shadow: var(--shadow-lg);
  display: flex; flex-direction: column; gap: 1px;
}
.ctx-item {
  display: flex; align-items: center; height: 28px; padding: 0 10px;
  border: none; border-radius: var(--radius-sm); background: none;
  color: var(--color-text-secondary); font-size: 12px; font-family: inherit;
  text-align: left; cursor: pointer;
}
.ctx-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.ctx-item.danger { color: var(--color-danger); }
.ctx-item.danger:hover { background: var(--color-danger-bg); color: var(--color-danger); }

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

/* 虚拟滚动：窗口化容器与撑高占位块。.qd-row 在虚拟列表里强制固定 44px，
   与 ROW_H 对齐（消除「有无副标题」造成的高度差，否则累计偏移会漂移）。 */
.item-vlist { position: relative; }
.item-vspacer { width: 100%; flex-shrink: 0; }
.item-vlist .qd-row {
  height: 44px;
  margin-bottom: 0;
  box-sizing: border-box;
}

/* ---- 多选批量 ---- */
.item-check { flex-shrink: 0; width: 14px; height: 14px; opacity: 0; cursor: pointer; }
.qd-row:hover .item-check, .item-check:checked { opacity: 1; }
/* 勾选行的高亮（WebView2 不支持 :has()，勾选态用行上的 picked 类表达） */
.qd-row.picked { background: var(--color-bg-hover); }

.item-batch {
  display: flex; align-items: center; gap: var(--space-2);
  padding: 6px 12px; border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.item-selall { display: inline-flex; align-items: center; }
.item-batch-count { font-size: 12px; color: var(--color-text-secondary); }
.item-batch-fill { flex: 1; }
.batch-move-wrap { position: relative; }
.batch-move-menu {
  position: absolute; right: 0; top: calc(100% + 4px); z-index: 30;
  min-width: 140px; max-height: 220px; overflow-y: auto;
  padding: 4px; border-radius: var(--radius-md);
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  box-shadow: var(--shadow-lg, 0 8px 24px rgba(0, 0, 0, 0.15));
  display: flex; flex-direction: column; gap: 1px;
}
.batch-move-item {
  text-align: left; padding: 6px 10px; border: none; background: transparent;
  border-radius: var(--radius-sm); cursor: pointer;
  color: var(--color-text-primary); font-size: 12.5px; white-space: nowrap;
}
.batch-move-item:hover { background: var(--color-bg-hover); }
</style>
