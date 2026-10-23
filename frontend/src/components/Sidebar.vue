<script setup lang="ts">
import { ref, inject, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import CreateDialog from './CreateDialog.vue'
import { getErrorMessage } from '../utils/error'
import { pluginUpdateBadge } from '../composables/usePluginUpdateBadge'
import type { ToastAPI } from '../types'
// 导航项图标已收敛到 pages/registry（纯数据），侧边栏仅保留仍在本文件直接使用的图标
import {
  FolderKanban, ChevronDown, ChevronLeft, ChevronRight, Plus, Pencil, Trash2, Settings,
} from '@lucide/vue'
import { navEntries } from '../pages/registry'

const store = useWorkspaceStore()
const { t } = useI18n()

const emit = defineEmits<{
  (e: 'navigate', page: string): void
  (e: 'open-settings'): void
}>()
const props = defineProps<{
  currentPage: string
  // 点击 dsh 导航后正在启动 DeepSeek Harness（原生窗口打开中），导航项显示加载态
  dshOpening?: boolean
}>()
const toast = inject<ToastAPI>('toast')!

// ---- 侧栏收起（仅显示图标） ----
const collapsed = ref(localStorage.getItem('qd_sidebar_collapsed') === '1')
function toggleCollapse() {
  collapsed.value = !collapsed.value
  try { localStorage.setItem('qd_sidebar_collapsed', collapsed.value ? '1' : '0') } catch { /* ignore */ }
}

// 点击「插件」导航：先清掉「可更新」角标（打开后即视为已查看），再导航
function goPlugins() {
  pluginUpdateBadge.count = 0
  emit('navigate', 'plugins')
}

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

async function handleDeleteWorkspace(wsId: string) {
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
</script>

<template>
  <aside class="sidebar" :class="{ collapsed }">
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
            <button class="ws-item-action danger" :title="t('delete')" :disabled="store.workspaces.indexOf(ws) === 0" :class="{ 'ws-action-disabled': store.workspaces.indexOf(ws) === 0 }" @click="handleDeleteWorkspace(ws.id)">
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

    <!-- 页面导航（由 pages/registry 驱动，新增页面只改注册表） -->
    <div class="sidebar-navigator">
      <div class="navigator-header">{{ t('navigation') }}</div>
      <template v-for="entry in navEntries" :key="entry.id">
        <!-- 普通页 / 工作空间：点击导航，按 currentPage 高亮 -->
        <button
          v-if="entry.kind === 'workspace' || entry.kind === 'page'"
          :class="['nav-item', { active: currentPage === entry.id }]"
          @click="emit('navigate', entry.id)"
        >
          <component :is="entry.icon" :size="14" />
          <span>{{ t(entry.labelKey) }}</span>
        </button>
        <!-- SSH：唤起独立终端窗口，主窗口内容不动，不置 active -->
        <button
          v-else-if="entry.kind === 'window'"
          class="nav-item"
          :title="t('navSshHint')"
          @click="emit('navigate', entry.id)"
        >
          <component :is="entry.icon" :size="14" />
          <span>{{ t(entry.labelKey) }}</span>
        </button>
        <!-- DSH：DeepSeek Harness 原生窗口，启动中显示 loading 且禁用 -->
        <button
          v-else-if="entry.kind === 'dsh'"
          :class="['nav-item', { active: currentPage === 'dsh', opening: dshOpening }]"
          :disabled="dshOpening"
          :title="dshOpening ? t('dshLaunching') : undefined"
          @click="emit('navigate', entry.id)"
        >
          <span v-if="dshOpening" class="nav-spinner"></span>
          <component v-else :is="entry.icon" :size="14" />
          <span>{{ dshOpening ? t('dshLaunching') : t(entry.labelKey) }}</span>
        </button>
        <!-- 插件：点击清角标后导航，带可更新角标 -->
        <button
          v-else-if="entry.kind === 'plugins'"
          :class="['nav-item', { active: currentPage === 'plugins' }]"
          @click="goPlugins"
        >
          <component :is="entry.icon" :size="14" />
          <span>{{ t(entry.labelKey) }}</span>
          <span v-if="pluginUpdateBadge.count > 0" class="nav-badge" :title="t('pluginUpdateBadge', { n: pluginUpdateBadge.count })">{{ pluginUpdateBadge.count > 99 ? '99+' : pluginUpdateBadge.count }}</span>
        </button>
      </template>
    </div>

    <!-- 工作空间新建 / 编辑弹窗（场景相关已迁移到右侧 SceneRail） -->
    <CreateDialog :visible="showCreateWorkspaceDialog" :title="t('addWorkspace')"
      :fields="[{ key: 'name', label: t('workspace'), type: 'text', placeholder: t('workspaceNamePlaceholder') }]"
      @confirm="handleCreateWorkspace" @cancel="showCreateWorkspaceDialog = false" />
    <CreateDialog :visible="showEditWorkspaceDialog" :title="t('edit')"
      :fields="[{ key: 'name', label: t('workspace'), type: 'text', placeholder: t('workspaceNamePlaceholder') }]"
      :editValues="{ name: editingWorkspaceName }"
      @confirm="handleEditWorkspace" @cancel="showEditWorkspaceDialog = false" />

    <!-- 底部固定：设置 + 收起（设置始终沉在最底部，与导航区独立） -->
    <div class="sidebar-footer">
      <button class="settings-btn" @click="emit('open-settings')" :title="t('settings')">
        <Settings :size="16" />
        <span>{{ t('settings') }}</span>
      </button>
      <button class="collapse-btn" @click="toggleCollapse" :title="collapsed ? t('expandSidebar') : t('collapseSidebar')">
        <ChevronLeft v-if="!collapsed" :size="16" />
        <ChevronRight v-else :size="16" />
      </button>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  width: var(--sidebar-width);
  min-width: var(--sidebar-width);
  height: 100%;
  background: var(--color-bg-secondary);
  box-shadow: var(--shadow-border-right);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  z-index: 10;
  transition: width var(--transition-base), min-width var(--transition-base);
}

/* 收起态：仅显示图标 */
.sidebar.collapsed { width: 54px; min-width: 54px; }
.sidebar.collapsed .ws-name,
.sidebar.collapsed .ws-arrow,
.sidebar.collapsed .navigator-header,
.sidebar.collapsed .nav-item span,
.sidebar.collapsed .settings-btn span { display: none; }
.sidebar.collapsed .workspace-selector { justify-content: center; padding: 10px 0; }
.sidebar.collapsed .nav-item { justify-content: center; padding-left: 0; padding-right: 0; }
.sidebar.collapsed .sidebar-footer { justify-content: center; gap: 4px; }
.sidebar.collapsed .settings-btn { flex: 0; padding: 6px; }
.sidebar.collapsed .ws-dropdown { left: 6px; right: auto; width: 200px; }

/* 工作空间切换器 */
.workspace-selector {
  position: relative;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: 10px 14px;
  min-height: 44px;
  cursor: pointer;
  user-select: none;
  box-shadow: var(--shadow-border);
  transition: background var(--transition-fast);
}
.workspace-selector:hover { background: var(--color-bg-hover); }
.ws-icon { color: var(--color-accent); flex-shrink: 0; }

/* 导航菜单（自身可滚动，避免导航过长把底部按钮挤出） */
.sidebar-navigator {
  flex: 1 1 auto;
  min-height: 0;
  padding: 6px 10px 10px;
  box-shadow: var(--shadow-border);
  overflow-y: auto;
}
.navigator-header {
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 1px;
  padding: 2px 4px 6px;
  user-select: none;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 8px;
  position: relative;
  width: 100%;
  padding: 7px 10px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 13px;
  cursor: pointer;
  font-family: inherit;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
  text-align: left;
}
.nav-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.nav-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); font-weight: 500; }
.nav-item.active svg { color: var(--color-accent); }
.nav-badge {
  margin-left: auto; min-width: 16px; height: 16px; padding: 0 4px; border-radius: var(--radius-md);
  background: #e0533d; color: var(--color-accent-text); font-size: 10px; font-weight: 700; line-height: 16px; text-align: center;
}
.sidebar.collapsed .nav-badge { position: absolute; top: 6px; right: 10px; margin: 0; }
.nav-item.opening { cursor: default; opacity: .7; }
.nav-item.opening:hover { background: transparent; color: var(--color-text-secondary); }
.nav-spinner {
  width: 14px; height: 14px; flex-shrink: 0;
  border: 2px solid color-mix(in srgb, var(--color-accent) 30%, transparent);
  border-top-color: var(--color-accent);
  border-radius: 50%;
  animation: nav-spin .8s linear infinite;
}
@keyframes nav-spin { to { transform: rotate(360deg); } }

.ws-name {
  flex: 1;
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ws-arrow {
  color: var(--color-text-disabled);
  flex-shrink: 0;
  transition: transform var(--transition-base);
}
.ws-arrow.open { transform: rotate(180deg); }
.ws-dropdown {
  position: absolute;
  top: 100%;
  left: var(--space-4);
  right: var(--space-4);
  z-index: 100;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
  overflow: hidden;
}
.ws-dropdown-header {
  padding: var(--space-4) var(--space-5);
  font-size: 11px;
  font-weight: 500;
  color: var(--color-text-disabled);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.ws-option {
  width: 100%;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 8px 12px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 13px;
  cursor: pointer;
  font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
  text-align: left;
}
.ws-option:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.ws-option.active { color: var(--color-accent); }

/* 工作空间条目行 */
.ws-item-row { display: flex; align-items: center; }
.ws-item-row .ws-option { flex: 1; min-width: 0; }
.ws-item-action {
  flex-shrink: 0;
  display: none;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  background: transparent;
  color: var(--color-text-disabled);
  cursor: pointer;
  border-radius: var(--radius-sm);
  margin-right: var(--space-2);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ws-item-row:hover .ws-item-action { display: flex; }
.ws-item-action:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.ws-item-action.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.ws-item-action:disabled,
.ws-item-action.ws-action-disabled { display: none; }
.ws-create { border-top: 1px solid var(--color-border); color: var(--color-text-disabled); }
.ws-create:hover { color: var(--color-accent); }
.ws-dropdown-divider { height: 1px; background: var(--color-border); }

/* 底部固定：设置 + 收起 */
.sidebar-footer {
  flex-shrink: 0;
  box-shadow: var(--shadow-border);
  display: flex;
  align-items: center;
  padding: var(--space-3) 10px;
}
.settings-btn {
  flex: 1;
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: 6px 10px;
  border: none;
  background: transparent;
  color: var(--color-text-disabled);
  font-size: 12px;
  cursor: pointer;
  font-family: inherit;
  border-radius: var(--radius-md);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.settings-btn:hover { color: var(--color-text-muted); background: var(--color-bg-tertiary); }
.collapse-btn {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 32px;
  border: none;
  background: transparent;
  color: var(--color-text-disabled);
  cursor: pointer;
  border-radius: var(--radius-md);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.collapse-btn:hover { color: var(--color-text-muted); background: var(--color-bg-tertiary); }

/* 工作空间下拉动画 */
.dropdown-enter-active, .dropdown-leave-active {
  transition: opacity var(--transition-base), transform var(--transition-base);
}
.dropdown-enter-from, .dropdown-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
