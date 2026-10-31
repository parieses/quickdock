<script setup lang="ts">
import { ref, computed, inject, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import TypeIcon from './TypeIcon.vue'
import CreateDialog from './CreateDialog.vue'
import { getErrorMessage } from '../utils/error'
import { pluginUpdateBadge } from '../composables/usePluginUpdateBadge'
import { useSceneFields } from '../composables/useSceneFields'
import { useDragSort } from '../composables/useDragSort'
import type { Scene, ToastAPI } from '../types'
import {
  Search, X, Plus, Pencil, Trash2, ChevronRight,
} from '@lucide/vue'

const store = useWorkspaceStore()
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 搜索时过滤场景列表
const displayScenes = computed(() => {
  if (store.hasSearch && store.searchResults) {
    return store.searchResults.scenes
  }
  return store.sortedScenes
})

// ---- 按 groupName 分组折叠渲染 ----
// 分组名相同的场景收进同一组；空 groupName 视为「未分组」并沉底。
// hasGroups 为 false 时（全部场景都没有分组）不渲染组头，列表退化为旧的单层结构，
// 拖拽排序照常工作；一旦有任意场景分了组，组头出现，跨组拖拽无意义 → 禁用拖拽。
const UNGROUPED = ''
const groupedScenes = computed(() => {
  const buckets = new Map<string, Scene[]>()
  for (const s of displayScenes.value) {
    const g = (s.groupName ?? '').trim()
    if (!buckets.has(g)) buckets.set(g, [])
    buckets.get(g)!.push(s)
  }
  const out = [...buckets.entries()].map(([name, scenes]) => ({
    name,
    label: name || t('mon_group_none'),
    scenes,
  }))
  out.sort((a, b) => {
    if (a.name === UNGROUPED && b.name !== UNGROUPED) return 1
    if (a.name !== UNGROUPED && b.name === UNGROUPED) return -1
    return a.label.localeCompare(b.label, 'zh-Hans-CN')
  })
  return out
})
const hasGroups = computed(() => groupedScenes.value.some(g => g.name !== UNGROUPED))
const displaySceneIds = computed(() => groupedScenes.value.flatMap(g => g.scenes.map(s => s.id)))

const collapsedGroups = reactive(new Set<string>())
function toggleGroup(name: string) {
  if (collapsedGroups.has(name)) collapsedGroups.delete(name)
  else collapsedGroups.add(name)
}

// ---- 拖拽排序（管道在 useDragSort，与设置页的导航排序共用一份）----
// 顺序按过滤后的列表算：搜索中本来就不允许拖（模板 :draggable="!store.hasSearch"），
// 但写在这里也不会错 —— 拿到的就是用户看到的那一份顺序。
const {
  dragId: dragSceneId,
  onDragStart: onDragStartScene,
  onDragOver: onDragOverScene,
  onDrop: onDropScene,
  onDragEnd: onDragEndScene,
} = useDragSort({
  ids: () => displaySceneIds.value,
  onReorder: (ids) => { store.reorderScenes(ids) },
})

// 搜索
const searchText = ref('')

function handleInput() {
  store.setSearch(searchText.value)
}

function clearSearch() {
  searchText.value = ''
  store.clearSearch()
}

// 场景对话框
const showSceneDialog = ref(false)
// 字段定义与引导页共用（composables/useSceneFields），不在此处重复声明
const sceneFields = useSceneFields()

const editingScene = ref<Scene | null>(null)
const showEditDialog = ref(false)

function handleSceneClick(sceneId: string) {
  store.selectScene(sceneId)
}

async function handleCreateScene(values: Record<string, string>) {
  try {
    await store.addScene(values.name, values.type)
    showSceneDialog.value = false
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleEditScene(values: Record<string, string>) {
  if (!editingScene.value) return
  try {
    await store.updateSceneAction(editingScene.value.id, { name: values.name, type: values.type, groupName: values.groupName })
    showEditDialog.value = false
    editingScene.value = null
  } catch (e) {
    toast.error(t('updateFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleDeleteScene(sceneId: string) {
  if (toast && !(await toast.confirm(t('confirmDeleteScene')))) return
  try { await store.removeScene(sceneId) } catch (e) { toast?.error(t('deleteFailed') + ': ' + getErrorMessage(e)) }
}

// 新建场景的唯一入口（头部 + 与空状态按钮都走这里）
function openCreate() {
  showSceneDialog.value = true
}

// 场景导入入口已移除（产品决定不做导入，只保留导出）：面板头只剩「新建」。
</script>

<template>
  <aside class="scene-rail qd-panel">
    <!-- 面板头：标题 + 新建（三栏同高同内边距，样式见 styles/panels.css） -->
    <div class="qd-panel-header">
      <span class="qd-panel-title">{{ t('scenes') }}</span>
      <div class="qd-panel-actions">
        <button class="qd-icon-btn" @click="openCreate" :title="t('addScene')">
          <Plus :size="16" />
        </button>
      </div>
    </div>

    <!-- 搜索栏：与集合/项目同位置同高度（此前三栏各写一套，位置还不一样） -->
    <div class="qd-panel-search">
      <Search :size="13" class="qd-search-icon" />
      <input
        v-model="searchText"
        type="text"
        class="qd-search-input"
        :placeholder="t('search')"
        @input="handleInput"
      />
      <button v-if="searchText" class="qd-search-clear" @click="clearSearch" :title="t('clear')">
        <X :size="12" />
      </button>
    </div>

    <!-- 搜索结果提示 -->
    <div v-if="store.hasSearch && store.searchResults" class="qd-search-hint">
      {{ t('searchResults') }} {{ store.searchResults.scenes.length }} {{ t('count') }}
    </div>

    <!-- 场景列表：按 groupName 分组折叠；无分组时退化为单层列表（仍可拖拽排序） -->
    <nav class="qd-panel-body">
      <template v-if="displayScenes.length">
        <template v-for="grp in groupedScenes" :key="grp.name || 'ungrouped'">
          <div
            v-if="hasGroups"
            class="qd-group-header"
            @click="toggleGroup(grp.name)"
          >
            <ChevronRight :size="13" class="qd-group-caret" :class="{ open: !collapsedGroups.has(grp.name) }" />
            <span class="qd-group-title">{{ grp.label }}</span>
            <span class="qd-group-count">{{ grp.scenes.length }}</span>
          </div>
          <ul v-show="!hasGroups || !collapsedGroups.has(grp.name)" class="qd-group-list">
            <li
              v-for="(scene, i) in grp.scenes"
              :key="scene.id"
              class="qd-row"
              :class="{ active: scene.id === store.activeSceneId, dragging: dragSceneId === scene.id }"
              :draggable="!store.hasSearch && !hasGroups"
              @click="handleSceneClick(scene.id)"
              @dragstart="onDragStartScene(scene.id, $event, i)"
              @dragend="onDragEndScene"
              @dragover="onDragOverScene(i, $event)"
              @drop="onDropScene(scene.id, $event)"
            >
              <span class="qd-row-icon">
                <TypeIcon :type="scene.type ?? '通用'" :size="17" />
              </span>
              <span class="qd-row-main">
                <span class="qd-row-title">{{ scene.name }}</span>
                <span class="qd-row-sub">{{ scene.type || t('sceneType') }}</span>
              </span>
              <span class="qd-row-actions" @click.stop>
                <button class="qd-row-btn" @click="editingScene = scene; showEditDialog = true" :title="t('edit')">
                  <Pencil :size="13" />
                </button>
                <button class="qd-row-btn danger" @click="handleDeleteScene(scene.id)" :title="t('delete')">
                  <Trash2 :size="13" />
                </button>
              </span>
            </li>
          </ul>
        </template>
      </template>
      <div v-else-if="store.hasSearch" class="qd-empty">
        <p class="qd-empty-title">{{ t('noMatchScenes') }}</p>
        <p class="qd-empty-hint">{{ t('tryOtherKeywords') }}</p>
      </div>
      <!-- 加载中不渲染空状态：切换工作空间时 scenes 会先被清空，
           否则会闪一下「暂无场景 + 添加场景」再变成真实列表 -->
      <div v-else-if="!store.scenesLoading" class="qd-empty">
        <span class="qd-empty-icon"><Plus :size="18" /></span>
        <p class="qd-empty-title">{{ t('noScenes') }}</p>
        <p class="qd-empty-hint">{{ t('createFirstScene') }}</p>
        <button class="qd-empty-cta" @click="openCreate">
          <Plus :size="13" />
          <span>{{ t('addScene') }}</span>
        </button>
      </div>
    </nav>

    <CreateDialog :visible="showSceneDialog" :title="t('addScene')" :fields="sceneFields"
      @confirm="handleCreateScene" @cancel="showSceneDialog = false" />
    <CreateDialog :visible="showEditDialog" :title="t('editScene')" :fields="sceneFields"
      :editValues="editingScene ? { name: editingScene.name, type: editingScene.type ?? '通用', groupName: editingScene.groupName ?? '' } : undefined"
      @confirm="handleEditScene" @cancel="showEditDialog = false; editingScene = null" />
  </aside>
</template>

<style scoped>
/* 面板头 / 搜索行 / 行 / 空状态的公共外观全部在 styles/panels.css（qd- 前缀），
   三栏共用一份，这里只留场景栏特有的部分 —— 免得又出现"改一处漏两处"。 */
.scene-rail {
  /* 行尾只剩「编辑 / 删除」两个按钮，200px 足够显示 5~6 个汉字的场景名。
     背景与集合/项目栏统一为 bg-primary：三栏是同一张工作台切出来的三列，
     靠 1px 分隔线区分即可；场景栏单独用 bg-secondary 会让人以为它是"另一个导航区"。 */
  width: 200px;
  min-width: 200px;
  flex-shrink: 0;
  background: var(--color-bg-primary);
  border-right: 1px solid var(--color-border);
}

/* 分组头：可点击折叠；箭头旋转给出展开/收起信号 */
.qd-group-header {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 5px 10px 3px;
  cursor: pointer;
  user-select: none;
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.03em;
}
.qd-group-header:hover { color: var(--color-text-secondary); }
.qd-group-caret {
  flex-shrink: 0;
  transition: transform var(--transition-fast);
}
.qd-group-caret.open { transform: rotate(90deg); }
.qd-group-title {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qd-group-count { font-size: 10px; opacity: 0.65; }
.qd-group-list { list-style: none; margin: 0; padding: 0; }

</style>
