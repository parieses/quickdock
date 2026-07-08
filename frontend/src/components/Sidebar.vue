<script setup lang="ts">
import { ref, inject, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { Plus, Pencil, Trash2, Search, X, Settings, ChevronDown, FolderKanban } from '@lucide/vue'
import TypeIcon from './TypeIcon.vue'
import CreateDialog from './CreateDialog.vue'
import { getErrorMessage } from '../utils/error'
import type { Scene, ToastAPI } from '../types'

const store = useWorkspaceStore()
const { t, tm } = useI18n()

// 搜索时过滤场景列表
const displayScenes = computed(() => {
  if (store.hasSearch && store.searchResults) {
    return store.searchResults.scenes
  }
  return store.sortedScenes
})
const emit = defineEmits<{ (e: 'open-settings'): void }>()
const toast = inject<ToastAPI>('toast')!

// ---- 工作空间 ----
const showWorkspaceMenu = ref(false)
const showCreateWorkspaceDialog = ref(false)
const showEditWorkspaceDialog = ref(false)
const editingWorkspaceId = ref('')
const editingWorkspaceName = ref('')

function toggleWorkspaceMenu() {
  showWorkspaceMenu.value = !showWorkspaceMenu.value
}

function selectWorkspace(id: string) {
  showWorkspaceMenu.value = false
  store.selectWorkspace(id)
}

async function handleCreateWorkspace(values: Record<string, string>) {
  try {
    await store.addWorkspace(values.name)
    showCreateWorkspaceDialog.value = false
    showWorkspaceMenu.value = false
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}

function startEditWorkspace(ws: { id: string; name: string }) {
  editingWorkspaceId.value = ws.id
  editingWorkspaceName.value = ws.name
  showWorkspaceMenu.value = false
  showEditWorkspaceDialog.value = true
}

async function handleEditWorkspace(values: Record<string, string>) {
  try {
    await store.updateWorkspaceAction(editingWorkspaceId.value, values.name)
    showEditWorkspaceDialog.value = false
  } catch (e) {
    toast.error(t('updateFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleDeleteWorkspace(wsId: string, wsName: string) {
  showWorkspaceMenu.value = false
  if (!(await toast.confirm(t('confirmDeleteWorkspace')))) return
  try {
    await store.removeWorkspace(wsId)
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}

// 点击外部关闭工作空间菜单
function onDocumentClick() {
  showWorkspaceMenu.value = false
}

onMounted(() => document.addEventListener('click', onDocumentClick))
onUnmounted(() => document.removeEventListener('click', onDocumentClick))

// 拖拽排序
const dragSceneId = ref<string | null>(null)

function onDragStartScene(e: DragEvent, sceneId: string) {
  dragSceneId.value = sceneId
  e.dataTransfer?.setData('text/plain', sceneId)
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}
function onDragOverScene(e: DragEvent) {
  e.preventDefault()
}
function onDragEndScene() {
  dragSceneId.value = null
}
function onDropScene(e: DragEvent, targetId: string) {
  e.preventDefault()
  const ids = displayScenes.value.map(s => s.id)
  const from = ids.indexOf(dragSceneId.value!)
  const to = ids.indexOf(targetId)
  if (from < 0 || to < 0 || from === to) return
  ids.splice(from, 1)
  ids.splice(to, 0, dragSceneId.value!)
  store.reorderScenes(ids)
  dragSceneId.value = null
}

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
const sceneFields = computed(() => {
  const st = tm('sceneTypes') as Record<string, string>
  return [
  { key: 'name', label: t('sceneName'), type: 'text' as const, placeholder: '例如：前端开发' },
  { key: 'type', label: t('sceneType'), type: 'select' as const, options: [
    { label: st['项目'] || '项目', value: '项目' },
    { label: st['办公'] || '办公', value: '办公' },
    { label: st['工程'] || '工程', value: '工程' },
    { label: st['设计'] || '设计', value: '设计' },
    { label: st['通用'] || '通用', value: '通用' },
    { label: st['自定义'] || '自定义', value: '自定义' },
  ]},
]})

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
    await store.updateSceneAction(editingScene.value.id, { name: values.name, type: values.type })
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
</script>

<template>
  <aside class="sidebar">
    <!-- 工作空间切换器 -->
    <div class="workspace-selector" @click.stop="toggleWorkspaceMenu">
      <FolderKanban :size="16" class="ws-icon" />
      <span class="ws-name">{{ store.activeWorkspace?.name || t('selectWorkspace') }}</span>
      <ChevronDown :size="14" class="ws-arrow" />
      <Transition name="dropdown">
        <div v-if="showWorkspaceMenu" class="ws-dropdown" @click.stop>
          <div class="ws-dropdown-header">{{ t('workspaces') }}</div>
          <div v-for="ws in store.workspaces" :key="ws.id" class="ws-item-row">
            <button
              :class="['ws-option', { active: ws.id === store.activeWorkspaceId }]"
              @click="selectWorkspace(ws.id)"
            >
              <FolderKanban :size="14" />
              <span>{{ ws.name }}</span>
            </button>
            <button class="ws-item-action" :title="t('edit')" @click="startEditWorkspace(ws)">
              <Pencil :size="11" />
            </button>
            <button class="ws-item-action danger" :title="t('delete')" @click="handleDeleteWorkspace(ws.id, ws.name)">
              <Trash2 :size="11" />
            </button>
          </div>
          <div class="ws-dropdown-divider" />
          <button class="ws-option ws-create" @click="showCreateWorkspaceDialog = true">
            <Plus :size="14" />
            <span>{{ t('addWorkspace') }}</span>
          </button>
        </div>
      </Transition>
    </div>

    <!-- 搜索栏（置于场景列表上方） -->
    <div class="sidebar-search">
      <div class="search-wrapper">
        <Search :size="14" class="search-icon" />
        <input
          v-model="searchText"
          type="text"
          class="search-input"
          :placeholder="t('search')"
          @input="handleInput"
        />
        <button v-if="searchText" class="clear-btn" @click="clearSearch" :title="t('clear')">
          <X :size="12" />
        </button>
      </div>
    </div>

    <!-- 场景标题 -->
    <div class="sidebar-header">
      <span class="sidebar-title">{{ t('scenes') }}</span>
      <button class="icon-btn" @click="showSceneDialog = true" :title="t('addScene')">
        <Plus :size="16" />
      </button>
    </div>

    <!-- 搜索结果提示 -->
    <div v-if="store.hasSearch && store.searchResults" class="search-hint">
      {{ t('searchResults') }} {{ store.searchResults.scenes.length }} {{ t('count') }}
    </div>

    <!-- 场景列表 -->
    <nav class="sidebar-nav">
      <ul v-if="displayScenes.length">
        <li
          v-for="scene in displayScenes"
          :key="scene.id"
          :class="{ active: scene.id === store.activeSceneId, dragging: dragSceneId === scene.id }"
          :draggable="!store.hasSearch"
          @click="handleSceneClick(scene.id)"
          @dragstart="onDragStartScene($event, scene.id)"
          @dragend="onDragEndScene"
          @dragover="onDragOverScene"
          @drop="onDropScene($event, scene.id)"
        >
          <span class="scene-icon">
            <TypeIcon :type="scene.type ?? '通用'" :size="18" />
          </span>
          <span class="scene-name">{{ scene.name }}</span>
          <span class="scene-actions" @click.stop>
            <button class="action-btn" @click="editingScene = scene; showEditDialog = true" :title="t('edit')">
              <Pencil :size="13" />
            </button>
            <button class="action-btn danger" @click="handleDeleteScene(scene.id)" :title="t('delete')">
              <Trash2 :size="13" />
            </button>
          </span>
        </li>
      </ul>
      <div v-else-if="store.hasSearch" class="sidebar-empty">
        <p class="empty-text">{{ t('noMatchScenes') }}</p>
        <p class="empty-hint">{{ t('tryOtherKeywords') }}</p>
      </div>
      <div v-else class="sidebar-empty">
        <p class="empty-icon">+</p>
        <p class="empty-text">{{ t('noScenes') }}</p>
        <p class="empty-hint">{{ t('createFirstScene') }}</p>
      </div>
    </nav>

    <CreateDialog :visible="showSceneDialog" :title="t('addScene')" :fields="sceneFields"
      @confirm="handleCreateScene" @cancel="showSceneDialog = false" />
    <CreateDialog :visible="showEditDialog" :title="t('editScene')" :fields="sceneFields"
      :editValues="editingScene ? { name: editingScene.name, type: editingScene.type ?? '通用' } : undefined"
      @confirm="handleEditScene" @cancel="showEditDialog = false; editingScene = null" />
    <CreateDialog :visible="showCreateWorkspaceDialog" :title="t('addWorkspace')"
      :fields="[{ key: 'name', label: t('workspace'), type: 'text', placeholder: '例如：日常工作' }]"
      @confirm="handleCreateWorkspace" @cancel="showCreateWorkspaceDialog = false" />
    <CreateDialog :visible="showEditWorkspaceDialog" :title="t('edit')"
      :fields="[{ key: 'name', label: t('workspace'), type: 'text', placeholder: '例如：日常工作' }]"
      :editValues="{ name: editingWorkspaceName }"
      @confirm="handleEditWorkspace" @cancel="showEditWorkspaceDialog = false" />

    <!-- 底部设置入口 -->
    <div class="sidebar-footer">
      <button class="settings-btn" @click="emit('open-settings')" :title="t('settings')">
        <Settings :size="16" />
        <span>{{ t('settings') }}</span>
      </button>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  width: 210px; min-width: 210px; height: 100%;
  background: var(--color-bg-secondary); border-right: 1px solid var(--color-border);
  display: flex; flex-direction: column; overflow: hidden;
}

/* 工作空间切换器 */
.workspace-selector {
  position: relative; flex-shrink: 0;
  display: flex; align-items: center; gap: 8px;
  padding: 12px 14px; cursor: pointer; user-select: none;
  border-bottom: 1px solid var(--color-border);
  transition: background 0.12s;
}
.workspace-selector:hover { background: var(--color-bg-hover); }
.ws-icon { color: var(--color-accent); flex-shrink: 0; }
.ws-name { flex: 1; font-size: 13px; font-weight: 600; color: var(--color-text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-arrow { color: var(--color-text-disabled); flex-shrink: 0; transition: transform 0.15s; }
.ws-dropdown {
  position: absolute; top: 100%; left: 8px; right: 8px; z-index: 100;
  background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 8px;
  box-shadow: 0 8px 24px var(--color-bg-overlay); overflow: hidden;
}
.ws-dropdown-header {
  padding: 8px 12px; font-size: 11px; color: var(--color-text-disabled);
  text-transform: uppercase; letter-spacing: 0.5px;
}
.ws-option {
  width: 100%; display: flex; align-items: center; gap: 10px;
  padding: 9px 12px; border: none; background: transparent;
  color: var(--color-text-secondary); font-size: 13px; cursor: pointer; font-family: inherit;
  transition: all 0.1s; text-align: left;
}
.ws-option:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.ws-option.active { color: var(--color-accent); }
.ws-option.active svg { color: var(--color-accent); }

/* 工作空间条目行（含编辑/删除按钮） */
.ws-item-row {
  display: flex; align-items: center;
}
.ws-item-row .ws-option {
  flex: 1; min-width: 0;
}
.ws-item-action {
  flex-shrink: 0;
  display: none; align-items: center; justify-content: center;
  width: 24px; height: 24px; border: none; background: transparent;
  color: var(--color-text-disabled); cursor: pointer;
  border-radius: 4px; margin-right: 4px; transition: all 0.1s;
}
.ws-item-row:hover .ws-item-action {
  display: flex;
}
.ws-item-action:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.ws-item-action.danger:hover { color: var(--color-danger); background: rgba(255,77,79,0.1); }
.ws-create { border-top: 1px solid var(--color-border); color: var(--color-text-disabled); }
.ws-create:hover { color: var(--color-accent); }
.ws-dropdown-divider { height: 1px; background: var(--color-border); }

/* 搜索栏 */
.sidebar-search {
  flex-shrink: 0;
  padding: 8px 10px;
  border-bottom: 1px solid var(--color-border);
  -webkit-app-region: drag;
}
.search-wrapper {
  display: flex; align-items: center; background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border); border-radius: 6px;
  padding: 0 6px 0 10px; width: 100%;
  height: 30px; gap: 6px; transition: border-color 0.15s, box-shadow 0.15s;
  -webkit-app-region: no-drag;
}
.search-wrapper:focus-within {
  border-color: var(--color-border-focus);
  box-shadow: 0 0 0 2px var(--color-accent-bg);
}
.search-icon { color: var(--color-text-disabled); flex-shrink: 0; }
.search-input {
  flex: 1; background: none; border: none; outline: none;
  color: var(--color-text-primary); font-size: 12px; font-family: inherit; min-width: 0;
}
.search-input::placeholder { color: var(--color-text-disabled); }
.clear-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  width: 18px; height: 18px; border-radius: 4px; transition: all 0.12s;
  flex-shrink: 0;
}
.clear-btn:hover { color: var(--color-text-muted); background: var(--color-bg-active); }

/* 搜索结果提示 */
.search-hint {
  padding: 6px 18px;
  font-size: 11px;
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border-bottom: 1px solid var(--color-accent-border);
}

/* 场景标题 */
.sidebar-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 14px 14px 18px; border-bottom: 1px solid var(--color-border);
}
.sidebar-title {
  font-size: 12px; font-weight: 700; color: var(--color-text-muted);
  text-transform: uppercase; letter-spacing: 1px;
}
.icon-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 6px; transition: all 0.15s;
}
.icon-btn:hover { color: var(--color-accent); background: var(--color-bg-hover); }

/* 场景列表 */
.sidebar-nav { flex: 1; overflow-y: auto; padding: 6px 0; }
.sidebar-nav ul { list-style: none; }
.sidebar-nav li {
  display: flex; align-items: center; gap: 10px;
  padding: 9px 14px 9px 18px; cursor: pointer; color: var(--color-text-secondary);
  font-size: 13px; transition: all 0.12s;
  border-left: 2px solid transparent;
}
.sidebar-nav li:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.sidebar-nav li.active {
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  border-left-color: var(--color-accent);
}
.scene-icon {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; flex-shrink: 0; color: var(--color-text-disabled);
}
.sidebar-nav li.active .scene-icon { color: var(--color-accent); }
.scene-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.scene-actions {
  display: flex; gap: 2px; opacity: 0; transition: opacity 0.12s;
}
.sidebar-nav li:hover .scene-actions { opacity: 1; }
.action-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  width: 26px; height: 26px; border-radius: 5px; transition: all 0.12s;
  display: flex; align-items: center; justify-content: center;
}
.action-btn:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.action-btn.danger:hover { color: var(--color-danger); background: rgba(255,77,79,0.1); }

/* 拖拽样式 */
.sidebar-nav li.dragging {
  opacity: 0.4;
}
.sidebar-nav li[draggable="true"] {
  cursor: grab;
}
.sidebar-nav li[draggable="true"]:active {
  cursor: grabbing;
}

/* 空状态 */
.sidebar-empty {
  padding: 32px 16px; text-align: center;
}
.empty-icon {
  width: 40px; height: 40px; border-radius: 50%; border: 2px dashed var(--color-border);
  color: var(--color-text-disabled); font-size: 18px; display: flex; align-items: center;
  justify-content: center; margin: 0 auto 12px;
}
.empty-text { font-size: 13px; color: var(--color-text-disabled); margin-bottom: 4px; }
.empty-hint { font-size: 11px; color: var(--color-text-muted); }

/* 底部设置 */
.sidebar-footer {
  flex-shrink: 0; border-top: 1px solid var(--color-border);
  display: flex; align-items: center; gap: 2px;
  padding: 6px 10px;
}
.settings-btn {
  flex: 1; display: flex; align-items: center; gap: 8px;
  padding: 6px 10px; border: none; background: transparent;
  color: var(--color-text-disabled); font-size: 12px; cursor: pointer; font-family: inherit;
  border-radius: 6px; transition: all 0.12s;
}
.settings-btn:hover { color: var(--color-text-muted); background: var(--color-bg-tertiary); }

/* 工作空间下拉动画 */
.dropdown-enter-active, .dropdown-leave-active { transition: opacity 0.12s, transform 0.12s; }
.dropdown-enter-from, .dropdown-leave-to { opacity: 0; transform: translateY(-4px); }
</style>
