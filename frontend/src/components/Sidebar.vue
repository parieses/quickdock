<script setup lang="ts">
import { ref, inject, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import CreateDialog from './CreateDialog.vue'
import NavBadge from './NavBadge.vue'
import NotificationCenter from './NotificationCenter.vue'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'
import { Events } from '@wailsio/runtime'
// 导航项图标已收敛到 pages/registry（纯数据），侧边栏仅保留仍在本文件直接使用的图标
import {
  FolderKanban, ChevronDown, ChevronLeft, ChevronRight, Plus, Pencil, Trash2, Settings,
  EyeOff, ArrowUp, ArrowDown, RotateCcw,
} from '@lucide/vue'
// navEntries 不再直接使用：可见性/顺序交给 useNavPrefs，它内部仍以 registry 为唯一真相

import { useNavPrefs } from '../composables/useNavPrefs'
import { useNavBadges } from '../composables/useNavBadges'
import { aiGenerating, aiFinishedUnread } from '../composables/bridge'

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

// 导航显隐 + 排序的本地偏好（唯一数据源，设置页与侧栏共用同一份）
const { orderedNav, visibleNav, hiddenNav, hideNav, showNav, moveNav, canMoveUp, canMoveDown } = useNavPrefs()

// 通用角标：注册 polling + 跟随 currentPage 自动打已读。
// 为什么这里需要用 object spread：useNavPrefs 已经顶着 orderedNav 同名变量了。
const navBadge = useNavBadges(() => props.currentPage)
const settingsBadge = navBadge.settingsBadge

// 崩溃记录藏在「设置 → 日志」里，打开设置即视为看过；
// 否则用户进了日志页，脚标上的红点还得等下一轮轮询才消失。
function openSettings() {
  navBadge.markSeen('settings')
  emit('open-settings')
}

// 通知点进去要落在具体的设置页（比如崩溃记录在「日志」页）。
// 与命令面板走同一条事件通道，避免再给 Sidebar 加一个 emit 通道。
function openSettingsPage(page: string) {
  navBadge.markSeen('settings')
  try { Events.Emit('app:open-settings', page) } catch { /* 主窗口未就绪时忽略 */ }
}

// ---- 导航项右键菜单 ----
// 用 fixed 定位跟随鼠标：菜单要浮在侧栏之上且能被全局 document click 关闭，
// 放进侧栏内会被 overflow:hidden 裁掉。
const navMenu = ref<{ id: string; x: number; y: number } | null>(null)
function openNavMenu(id: string, e: MouseEvent) {
  e.preventDefault()
  e.stopPropagation()
  navMenu.value = { id, x: e.clientX, y: e.clientY }
}
function closeNavMenu() { navMenu.value = null }

function menuHide(id: string) {
  // 先取名字再隐藏：隐藏后它就从 visibleNav 里消失了
  const label = t(visibleNav.value.find((e) => e.id === id)?.labelKey ?? '')
  if (!hideNav(id)) {
    toast.error(t('navKeepOne'))
    return
  }
  closeNavMenu()
  toast.success(t('navHiddenToast', { name: label }) + ' · ' + t('navHiddenWhere'))
}
// 模板里 v-if="navMenu" 无法收窄类型，统一走这些 computed 取值
const menuId = computed(() => navMenu.value?.id ?? '')
const menuLabel = computed(() => t(orderedNav.value.find((e) => e.id === menuId.value)?.labelKey ?? ''))
const menuIsHidden = computed(() => hiddenNav.value.some((e) => e.id === menuId.value))

function menuMove(id: string, dir: -1 | 1) {
  moveNav(id, dir)
  closeNavMenu()
}

function menuRestore(id: string) {
  showNav(id)
  closeNavMenu()
}

// ---- 底部「已隐藏」折叠组 ----
const showHidden = ref(false)
function toggleHidden(e: MouseEvent) {
  e.stopPropagation()
  showHidden.value = !showHidden.value
}

// 点击外部关闭：右键菜单 / 已隐藏浮层 / 工作空间菜单共用（避免多个浮层互相打架）
function onDocumentClick() {
  showWorkspaceMenu.value = false
  navMenu.value = null
  showHidden.value = false
}

onMounted(() => document.addEventListener('click', onDocumentClick))
onUnmounted(() => document.removeEventListener('click', onDocumentClick))

// ---- 侧栏收起（仅显示图标） ----
const collapsed = ref(localStorage.getItem('qd_sidebar_collapsed') === '1')
function toggleCollapse() {
  collapsed.value = !collapsed.value
  try { localStorage.setItem('qd_sidebar_collapsed', collapsed.value ? '1' : '0') } catch { /* ignore */ }
}

// 点击「插件」导航：进入即视为已读，useNavBadges 会在 currentPage 变为
// 'plugins' 时自动 markSeen('plugins') 把「可更新」角标清零。插件更新数本身
// 来自 usePluginUpdateBadge（在 useNavBadges 内部订阅），这里无需再手写清零。
function goPlugins() {
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

    <!-- 页面导航（由 pages/registry 驱动 + useNavPrefs 裁剪排序，新增页面只改注册表） -->
    <div class="sidebar-navigator">
      <div class="navigator-header">{{ t('navigation') }}</div>
      <template v-for="entry in visibleNav" :key="entry.id">
        <!-- 普通页 / 工作空间：点击导航，按 currentPage 高亮 -->
        <button
          v-if="entry.kind === 'workspace' || entry.kind === 'page'"
          :class="['nav-item', { active: currentPage === entry.id }]"
          @click="emit('navigate', entry.id)"
          @contextmenu="openNavMenu(entry.id, $event)"
        >
          <component :is="entry.icon" :size="14" />
          <span>{{ t(entry.labelKey) }}</span>
          <!-- AI 生成指示：AI 页被 KeepAlive 保活，回答会在别的页面继续生成完成，
               不给提示用户会以为"切走了就没了" -->
          <span
            v-if="entry.id === 'ai' && aiGenerating"
            class="nav-ai-dot running"
            :title="t('aiGeneratingHint')"
          ></span>
          <span
            v-else-if="entry.id === 'ai' && aiFinishedUnread"
            class="nav-ai-dot done"
            :title="t('aiFinishedHint')"
          ></span>
          <NavBadge :id="entry.id" />
        </button>
        <!-- SSH：唤起独立终端窗口，主窗口内容不动，不置 active -->
        <button
          v-else-if="entry.kind === 'window'"
          class="nav-item"
          :title="t('navSshHint')"
          @click="emit('navigate', entry.id)"
          @contextmenu="openNavMenu(entry.id, $event)"
        >
          <component :is="entry.icon" :size="14" />
          <span>{{ t(entry.labelKey) }}</span>
          <NavBadge :id="entry.id" />
        </button>
        <!-- DSH：DeepSeek Harness 原生窗口，启动中显示 loading 且禁用 -->
        <button
          v-else-if="entry.kind === 'dsh'"
          :class="['nav-item', { active: currentPage === 'dsh', opening: dshOpening }]"
          :disabled="dshOpening"
          :title="dshOpening ? t('dshLaunching') : undefined"
          @click="emit('navigate', entry.id)"
          @contextmenu="openNavMenu(entry.id, $event)"
        >
          <span v-if="dshOpening" class="nav-spinner"></span>
          <component v-else :is="entry.icon" :size="14" />
          <span>{{ dshOpening ? t('dshLaunching') : t(entry.labelKey) }}</span>
          <NavBadge :id="entry.id" />
        </button>
        <!-- 插件：点击清角标后导航。角标数据源本身在 useNavBadges 里统一管了，
             这里保留 goPlugins 的语义：进入即视为已读（markSeen 由 currentPage 变更触发） -->
        <button
          v-else-if="entry.kind === 'plugins'"
          :class="['nav-item', { active: currentPage === 'plugins' }]"
          @click="goPlugins"
          @contextmenu="openNavMenu(entry.id, $event)"
        >
          <component :is="entry.icon" :size="14" />
          <span>{{ t(entry.labelKey) }}</span>
          <NavBadge :id="entry.id" />
        </button>
      </template>

      <!-- 已隐藏项的栖身之所：隐藏不等于删除，这里始终可以取回。
           只有在「当前可见项被隐藏」或「收起态」时才会出现；也可以从设置 → 导航管理。 -->
      <div v-if="hiddenNav.length" class="hidden-group">
        <button class="nav-item hidden-toggle" :title="t('navHiddenWhere')" @click="toggleHidden($event)">
          <EyeOff :size="14" />
          <span>{{ t('navHiddenCount', { n: hiddenNav.length }) }}</span>
          <ChevronDown :size="13" class="hidden-caret" :class="{ open: showHidden }" />
        </button>
        <!-- 展开态：就地列出；收起态：侧栏塞不下，用浮层向右展开 -->
        <div :class="['hidden-list', { open: showHidden, 'as-popover': collapsed }]" @click.stop>
          <!-- 外层必须是 div：里面还有一个「取回」按钮，<button> 不能嵌套 <button> -->
          <div
            v-for="e in hiddenNav"
            :key="e.id"
            class="nav-item hidden-item"
            role="button"
            tabindex="0"
            :title="t(e.labelKey)"
            @click="emit('navigate', e.id)"
            @keydown.enter.prevent="emit('navigate', e.id)"
            @keydown.space.prevent="emit('navigate', e.id)"
            @contextmenu="openNavMenu(e.id, $event)"
          >
            <component :is="e.icon" :size="14" />
            <span>{{ t(e.labelKey) }}</span>
            <button class="hidden-restore" :title="t('navRestore')" @click.stop="showNav(e.id)">
              <RotateCcw :size="12" />
            </button>
          </div>
        </div>
      </div>
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
      <!-- 通知中心：与设置同级沉底。它进不了导航区——导航是「去某个地方」，
           通知是「刚才发生了什么」，混在一起会让导航项的顺序失去意义。 -->
      <NotificationCenter @navigate="(p) => emit('navigate', p)" @open-settings="openSettingsPage" />
      <button class="settings-btn" @click="openSettings" :title="t('settings')">
        <Settings :size="16" />
        <span>{{ t('settings') }}</span>
        <span
          v-if="settingsBadge"
          class="nav-badge footer-badge"
          :class="`tone-${settingsBadge.tone}`"
          :title="t(settingsBadge.titleKey, { n: settingsBadge.count })"
        >{{ settingsBadge.count > 99 ? '99+' : settingsBadge.count }}</span>
      </button>
      <button class="collapse-btn" @click="toggleCollapse" :title="collapsed ? t('expandSidebar') : t('collapseSidebar')">
        <ChevronLeft v-if="!collapsed" :size="16" />
        <ChevronRight v-else :size="16" />
      </button>
    </div>

    <!-- 导航项右键菜单：Teleport 到 body，避免被侧栏 overflow:hidden 裁切 -->
    <Teleport to="body">
      <div
        v-if="navMenu"
        class="nav-menu"
        :style="{ top: navMenu.y + 'px', left: navMenu.x + 'px' }"
        @click.stop
      >
        <div class="nav-menu-title">{{ menuLabel }}</div>
        <button :disabled="!canMoveUp(menuId)" @click="menuMove(menuId, -1)">
          <ArrowUp :size="13" /><span>{{ t('navMoveUp') }}</span>
        </button>
        <button :disabled="!canMoveDown(menuId)" @click="menuMove(menuId, 1)">
          <ArrowDown :size="13" /><span>{{ t('navMoveDown') }}</span>
        </button>
        <div class="nav-menu-divider" />
        <button v-if="menuIsHidden" @click="menuRestore(menuId)">
          <RotateCcw :size="13" /><span>{{ t('navRestore') }}</span>
        </button>
        <button v-else class="danger" @click="menuHide(menuId)">
          <EyeOff :size="13" /><span>{{ t('navHide') }}</span>
        </button>
      </div>
    </Teleport>
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
/* 收起态同样不能隐藏 span（浮层里的行要靠它显示名字），
   只在不同的 class 上做显隐，不用 :has() —— WebView2 版本不可控 */
.sidebar.collapsed .hidden-toggle span,
.sidebar.collapsed .hidden-caret { display: none; }
.sidebar.collapsed .hidden-item span { display: inline; }

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
  min-height: 32px;
  padding: 0 10px;
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
/* 选中态与工作空间三栏的 .qd-row.active 同一套语言：强调色淡底 + 图标转强调色。
   原来用 bg-tertiary + 整行文字变蓝，字一多就像超链接。 */
.nav-item.active { background: var(--color-accent-bg); color: var(--color-text-primary); font-weight: 500; }
.nav-item.active svg { color: var(--color-accent); }
.nav-badge {
  margin-left: auto; min-width: 16px; height: 16px; padding: 0 4px; border-radius: var(--radius-md);
  background: var(--color-danger); color: var(--color-accent-text); font-size: 10px; font-weight: 700; line-height: 16px; text-align: center;
}
/* 角标色调：红=故障/崩溃，橙=该处理的事，蓝=有好东西可用（统一走语义 token） */
.nav-badge.tone-danger { background: var(--color-danger); }
.nav-badge.tone-warning { background: var(--color-warning); }
.nav-badge.tone-info { background: var(--color-accent); }
/* footer 上的设置角标：按钮没右侧内边距，且不能让小红点把「设置」文字挤没 */
.footer-badge { margin-left: 4px; flex-shrink: 0; }
.sidebar.collapsed .nav-item .nav-badge,
.sidebar.collapsed .settings-btn .footer-badge {
  /* 收起态「只留图标」的规则是 .nav-item span（0-3-1），这里必须至少 0-4-0 才能翻盘；
     之前收起态角标从不显示，根因正是权重输给了那条 */
  display: inline-block;
  position: absolute;
  top: 4px;
  right: 8px;
  margin: 0;
  min-width: 14px;
  height: 14px;
  padding: 0 3px;
  line-height: 14px;
  font-size: 9px;
}
.sidebar.collapsed .settings-btn .footer-badge { top: 0; right: 2px; }
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

/* AI 生成指示点：running = 还在写（呼吸动画），done = 已写完等你回来看。
   颜色不用红：红色留给监控掉线这类真故障（与通知中心角标同一口径）。 */
.nav-ai-dot {
  flex-shrink: 0; margin-left: auto;
  width: 7px; height: 7px; border-radius: 50%;
  background: var(--color-accent);
}
.nav-ai-dot.running { animation: nav-ai-pulse 1.1s ease-in-out infinite; }
@keyframes nav-ai-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .25; } }
/* NavBadge 也挂在行尾，两者同排时留一点间距 */
.nav-ai-dot + * { margin-left: 4px; }
/* 收起态照样要看见：`.collapsed .nav-item span` 会 display:none（0-3-1），
   这里必须同样的权重以上才能翻盘（nav-badge 是同样的坑，见上） */
.sidebar.collapsed .nav-item .nav-ai-dot {
  display: inline-block;
  position: absolute;
  top: 5px;
  right: 7px;
  margin: 0;
}

/* 已隐藏项折叠组：虚线框 + 就地恢复列表 */
.hidden-group { margin-top: 6px; }
.hidden-toggle { color: var(--color-text-disabled); }
.hidden-caret { margin-left: auto; color: var(--color-text-disabled); transition: transform var(--transition-base); }
.hidden-caret.open { transform: rotate(180deg); }
.hidden-list { display: none; flex-direction: column; gap: 1px; padding-left: 8px; border-left: 1px dashed var(--color-border-secondary); margin: 2px 0 0 10px; }
/* 展开与否只由 .open 决定；as-popover 只负责换定位方式，两者互不干涉 */
.hidden-list.open { display: flex; }
.hidden-list.as-popover {
  position: fixed;
  left: 54px;
  bottom: 72px;
  width: 180px;
  max-height: 240px;
  overflow-y: auto;
  z-index: 60;
  margin: 0;
  padding: 6px;
  border-left: none;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
}
/* 已隐藏项被 hover 时才显示「恢复」按钮：默认不占用横向空间 */
/* 已隐藏项：外层是 div（内含「取回」按钮，不能嵌套 button），
   所以显式补上按钮本来自带的行为 */
.hidden-item {
  position: relative;
  color: var(--color-text-disabled);
  font-style: normal;
  user-select: none;
}
.hidden-item:hover { color: var(--color-text-secondary); }
.hidden-restore {
  display: none;
  margin-left: auto;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border: none;
  background: transparent;
  color: var(--color-accent);
  cursor: pointer;
  border-radius: var(--radius-sm);
  flex-shrink: 0;
}
.hidden-item:hover .hidden-restore { display: flex; }
.hidden-restore:hover { background: var(--color-bg-active); }

/* 导航右键菜单 */
.nav-menu {
  position: fixed;
  z-index: 200;
  min-width: 150px;
  padding: 4px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
}
.nav-menu-title {
  padding: 4px 8px 6px;
  font-size: 11px;
  color: var(--color-text-disabled);
  border-bottom: 1px solid var(--color-border);
  margin-bottom: 4px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.nav-menu button {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 8px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  border-radius: var(--radius-sm);
  text-align: left;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.nav-menu button:hover:not(:disabled) { background: var(--color-bg-active); color: var(--color-text-primary); }
.nav-menu button:disabled { opacity: 0.4; cursor: default; }
.nav-menu button.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.nav-menu-divider { height: 1px; background: var(--color-border); margin: 4px 2px; }

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
  position: relative; /* footer 角标 absolute 的定位基准，缺了它会相对窗口跑飞 */
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
