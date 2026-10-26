<script setup lang="ts">
import { ref, computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import TypeIcon from './TypeIcon.vue'
import CreateDialog from './CreateDialog.vue'
import { getErrorMessage } from '../utils/error'
import { pluginUpdateBadge } from '../composables/usePluginUpdateBadge'
import { useSceneFields } from '../composables/useSceneFields'
import type { Scene, ToastAPI } from '../types'
import {
  Search, X, Plus, Pencil, Trash2,
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

// 新建场景的唯一入口（头部 + 与空状态按钮都走这里）
function openCreate() {
  showSceneDialog.value = true
}
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

    <!-- 场景列表 -->
    <nav class="qd-panel-body">
      <ul v-if="displayScenes.length">
        <li
          v-for="scene in displayScenes"
          :key="scene.id"
          class="qd-row"
          :class="{ active: scene.id === store.activeSceneId, dragging: dragSceneId === scene.id }"
          :draggable="!store.hasSearch"
          @click="handleSceneClick(scene.id)"
          @dragstart="onDragStartScene($event, scene.id)"
          @dragend="onDragEndScene"
          @dragover="onDragOverScene"
          @drop="onDropScene($event, scene.id)"
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
      :editValues="editingScene ? { name: editingScene.name, type: editingScene.type ?? '通用' } : undefined"
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

</style>
