<script setup lang="ts">
import { ref, computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import TypeIcon from './TypeIcon.vue'
import CreateDialog from './CreateDialog.vue'
import SceneEnvDialog from './SceneEnvDialog.vue'
import { getErrorMessage } from '../utils/error'
import { pluginUpdateBadge } from '../composables/usePluginUpdateBadge'
import type { Scene, ToastAPI } from '../types'
import {
  Search, X, Plus, Pencil, Trash2, Server,
} from '@lucide/vue'

const store = useWorkspaceStore()
const { t, tm } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 搜索时过滤场景列表
const displayScenes = computed(() => {
  if (store.hasSearch && store.searchResults) {
    return store.searchResults.scenes
  }
  return store.sortedScenes
})

// ---- 拖拽排序 ----
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
  { key: 'name', label: t('sceneName'), type: 'text' as const, placeholder: t('sceneNamePlaceholder') },
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
// 场景环境服务绑定弹窗（随场景启停哪些运行时）
const envDialogScene = ref<Scene | null>(null)

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
  <aside class="scene-rail">
    <!-- 搜索栏 -->
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
            <button class="action-btn" @click="envDialogScene = scene" :title="t('envServices')">
              <Server :size="13" />
            </button>
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
    <SceneEnvDialog
      :visible="!!envDialogScene"
      :scene-id="envDialogScene?.id ?? ''"
      :scene-name="envDialogScene?.name ?? ''"
      @close="envDialogScene = null"
    />
  </aside>
</template>

<style scoped>
.scene-rail {
  width: 220px;
  min-width: 220px;
  height: 100%;
  flex-shrink: 0;
  background: var(--color-bg-secondary);
  border-right: 1px solid var(--color-border);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  z-index: 9;
}

/* 搜索栏 */
.sidebar-search {
  flex-shrink: 0;
  padding: var(--space-4) 10px;
  box-shadow: var(--shadow-border);
  -webkit-app-region: drag;
}
.search-wrapper {
  display: flex;
  align-items: center;
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  padding: 0 var(--space-3) 0 10px;
  width: 100%;
  height: 30px;
  gap: var(--space-3);
  transition: border-color var(--transition-base), box-shadow var(--transition-base);
  -webkit-app-region: no-drag;
}
.search-wrapper:focus-within {
  border-color: var(--color-border-focus);
  box-shadow: 0 0 0 2px var(--color-accent-bg);
}
.search-icon { color: var(--color-text-disabled); flex-shrink: 0; }
.search-input {
  flex: 1;
  background: none;
  border: none;
  outline: none;
  color: var(--color-text-primary);
  font-size: 12px;
  font-family: inherit;
  min-width: 0;
}
.search-input::placeholder { color: var(--color-text-disabled); }
.clear-btn {
  background: none;
  border: none;
  color: var(--color-text-disabled);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
  flex-shrink: 0;
}
.clear-btn:hover { color: var(--color-text-muted); background: var(--color-bg-active); }

/* 搜索结果提示 */
.search-hint {
  padding: var(--space-3) 18px;
  font-size: 11px;
  color: var(--color-accent);
  background: var(--color-accent-bg);
  box-shadow: var(--shadow-border);
}

/* 场景标题 */
.sidebar-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px 12px 18px;
}
.sidebar-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 1px;
}
.icon-btn {
  background: none;
  border: none;
  color: var(--color-text-disabled);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: var(--radius-md);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.icon-btn:hover { color: var(--color-accent); background: var(--color-bg-hover); }

/* 场景列表 */
.sidebar-nav {
  flex: 1;
  overflow-y: auto;
  padding: var(--space-2) 0;
}
.sidebar-nav ul { list-style: none; padding: 0; margin: 0; }
.sidebar-nav li {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 7px 14px 7px 18px;
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
  border-left: 2px solid transparent;
}
.sidebar-nav li:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.sidebar-nav li.active {
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  border-left-color: var(--color-accent);
}
.scene-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  flex-shrink: 0;
  color: var(--color-text-muted);
}
.sidebar-nav li.active .scene-icon { color: var(--color-accent); }
.scene-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}
.scene-actions {
  display: flex;
  gap: var(--space-1);
  opacity: 0;
  transition: opacity var(--transition-fast);
}
.sidebar-nav li:hover .scene-actions { opacity: 1; }
.action-btn {
  background: none;
  border: none;
  color: var(--color-text-disabled);
  cursor: pointer;
  width: 24px;
  height: 24px;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
  display: flex;
  align-items: center;
  justify-content: center;
}
.action-btn:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.action-btn.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }

/* 拖拽样式 */
.sidebar-nav li.dragging { opacity: 0.4; }
.sidebar-nav li[draggable="true"] { cursor: grab; }
.sidebar-nav li[draggable="true"]:active { cursor: grabbing; }

/* 空状态 */
.sidebar-empty {
  padding: var(--space-9) var(--space-6);
  text-align: center;
}
.empty-icon {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  border: 2px dashed var(--color-border);
  color: var(--color-text-disabled);
  font-size: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  margin: 0 auto var(--space-5);
}
.empty-text { font-size: 13px; color: var(--color-text-disabled); margin-bottom: var(--space-2); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); }
</style>
