<script setup lang="ts">
import { ref, inject, computed, onMounted, onUnmounted, type Component } from 'vue'
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
import type { NavDotKind } from '../pages/registry'

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
  // 正在启动中的导航项 id（App 报告，如 dsh 唤起原生窗口期间传 'dsh'）：
  // 该行换 spinner + 禁用 + 文案变「启动中」。Sidebar 不需要认识「dsh」是什么。
  busyPage?: string
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

// 展开/收起的高度过渡：列表条数不定（1~11 条），CSS 只能用 max-height 猜一个上限，
// 条数一多就被截断、条数少时收尾又慢半拍。所以用 JS 钩子量真实高度。
// 三个钩子都必须清理内联 height：留着它会让后续「取回一项」时列表高度不再自适应。
function onCollapseEnter(el: Element) {
  const e = el as HTMLElement
  const h = e.scrollHeight
  e.style.height = '0px'
  e.style.opacity = '0'
  void e.offsetHeight // 强制回流，否则同一帧内改 height 会被合并，动画不触发
  e.style.height = h + 'px'
  e.style.opacity = '1'
}
function onCollapseAfterEnter(el: Element) {
  const e = el as HTMLElement
  e.style.height = ''
  e.style.opacity = ''
}
function onCollapseLeave(el: Element) {
  const e = el as HTMLElement
  e.style.height = e.scrollHeight + 'px'
  e.style.opacity = '1'
  void e.offsetHeight
  e.style.height = '0px'
  e.style.opacity = '0'
}

// 点击外部关闭：右键菜单 / 已隐藏浮层 / 工作空间菜单共用（避免多个浮层互相打架）
function onDocumentClick() {
  showWorkspaceMenu.value = false
  navMenu.value = null
  showHidden.value = false
}

// 全文件只注册这一次。这里和原先的文件末尾各写过一遍，同一实例会挂两个完全相同的
// 全局 click 监听：onDocumentClick 幂等，所以没暴露成 bug，但每次点击都白跑两遍。
onMounted(() => document.addEventListener('click', onDocumentClick))
onUnmounted(() => document.removeEventListener('click', onDocumentClick))

// ---- 侧栏收起（仅显示图标） ----
const collapsed = ref(localStorage.getItem('qd_sidebar_collapsed') === '1')
function toggleCollapse() {
  collapsed.value = !collapsed.value
  try { localStorage.setItem('qd_sidebar_collapsed', collapsed.value ? '1' : '0') } catch { /* ignore */ }
}

// ---- 导航项视图模型 ----
// 差异只有三点：是否置 active、是否有 loading/禁用态、行尾是否挂「后台仍在跑」的指示点。
// 全部来自 registry 条目的字段声明（dot / busyLabelKey / hintKey / activate），
// 模板只有一段 <button>，这里也不再判断 kind —— 新加一种导航项行为只改数据。
interface NavItemView {
  id: string
  icon: Component
  label: string
  active: boolean
  /** 启动中：图标换 spinner、禁用、文字与 title 变「启动中」 */
  busy: boolean
  disabled: boolean
  title?: string
  /** 行尾「后台活动」指示点：有值才渲染（running = 还在跑 / done = 已跑完） */
  dot?: 'running' | 'done'
  dotTitle?: string
}

// 「后台仍在运行」指示点的取值表，键是 registry 的 NavDotKind（闭集）。
// Record<NavDotKind, …> 的用处：新增一种 dot 时这里缺项会**编译报错**，
// 而不是像 string key 的 map 那样静默不显示（也就是「指示点凭空消失」）。
// 数据来源必须是响应式的（aiGenerating 等），所以表只能在 Sidebar 而不能进 registry。
const DOT_PROVIDERS: Record<NavDotKind, () => { cls: 'running' | 'done'; title: string } | undefined> = {
  // AI 页被 KeepAlive 保活，回答会在别的页面继续生成/完成，
  // 不给提示用户会以为「切走了就没了」。
  // 文案在此处就 t() 掉：模板里 v-if 无法收窄对象属性类型，
  // 拍平成两个标量字段（dot / dotTitle）就省掉了需要窄化的写法。
  ai: () => {
    if (aiGenerating.value) return { cls: 'running', title: t('aiGeneratingHint') }
    if (aiFinishedUnread.value) return { cls: 'done', title: t('aiFinishedHint') }
    return undefined
  },
}

const navItems = computed<NavItemView[]>(() =>
  visibleNav.value.map((e) => {
    const busy = props.busyPage === e.id
    const dot = e.dot ? DOT_PROVIDERS[e.dot]() : undefined
    return {
      id: e.id,
      icon: e.icon,
      label: busy ? t(e.busyLabelKey || e.labelKey) : t(e.labelKey),
      // 唤起独立窗口的入口（有 activate）不切页，所以永不置 active；
      // 它点下去会跳出当前界面，需要一句 title 说明后果（hintKey）。
      active: !e.activate && props.currentPage === e.id,
      busy,
      disabled: busy,
      title: busy ? t(e.busyLabelKey || e.labelKey) : e.hintKey ? t(e.hintKey) : undefined,
      dot: dot?.cls,
      dotTitle: dot?.title,
    }
  }),
)

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

    <!-- 页面导航（由 pages/registry 驱动 + useNavPrefs 裁剪排序，新增页面只改注册表）
         一个模板覆盖全部导航项：active / loading / 指示点的差异由 navItems 按条目字段算好，模板不分 kind。
         插件项的「可更新」角标同样不需要显式清零 —— 进入 plugins 页时 useNavBadges 的
         currentPage watcher 会 markSeen('plugins')，插件更新数本身由 usePluginUpdateBadge 订阅。 -->
    <div class="sidebar-navigator">
      <div class="navigator-header">{{ t('navigation') }}</div>
      <button
        v-for="it in navItems"
        :key="it.id"
        :class="['nav-item', { active: it.active, opening: it.busy }]"
        :disabled="it.disabled"
        :title="it.title"
        @click="emit('navigate', it.id)"
        @contextmenu="openNavMenu(it.id, $event)"
      >
        <span v-if="it.busy" class="nav-spinner"></span>
        <component v-else :is="it.icon" :size="14" />
        <span>{{ it.label }}</span>
        <span v-if="it.dot" class="nav-dot" :class="it.dot" :title="it.dotTitle"></span>
        <NavBadge :id="it.id" />
      </button>

      <!-- 已隐藏项的栖身之所：隐藏不等于删除，这里始终可以取回。
           只有在「当前可见项被隐藏」或「收起态」时才会出现；也可以从设置 → 导航管理。 -->
      <div v-if="hiddenNav.length" class="hidden-group">
        <button class="nav-item hidden-toggle" :title="t('navHiddenWhere')" @click="toggleHidden($event)">
          <EyeOff :size="14" />
          <span>{{ t('navHiddenCount', { n: hiddenNav.length }) }}</span>
          <ChevronDown :size="13" class="hidden-caret" :class="{ open: showHidden }" />
        </button>
        <!-- 展开态：就地列出；收起态：侧栏塞不下，用浮层向右展开 -->
        <Transition
          name="collapse"
          @enter="onCollapseEnter"
          @after-enter="onCollapseAfterEnter"
          @leave="onCollapseLeave"
        >
          <div v-if="showHidden" class="hidden-list" :class="{ 'as-popover': collapsed }" @click.stop>
            <!-- 外层必须是 div：里面还有一个「取回」按钮，<button> 不能嵌套 <button> -->
            <div
              v-for="e in hiddenNav"
              :key="e.id"
              class="nav-item hidden-item"
              :class="{ active: e.id === currentPage }"
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
        </Transition>
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
/* 收起态 54px 宽放不下「通知 + 设置 + 折叠」三个横排按钮（实测超 90px），
   居中后最右的折叠/展开箭头会被侧栏 overflow:hidden 裁掉——用户就「找不到展开按钮」。
   改成竖向堆叠：三项各自居中，展开箭头恒定可见，也不挤占横向。 */
.sidebar.collapsed .sidebar-footer { flex-direction: column; justify-content: center; gap: 8px; padding: 8px 0; }
.sidebar.collapsed .settings-btn { flex: 0; padding: 6px; }
.sidebar.collapsed .ws-dropdown { left: 6px; right: auto; width: 200px; }
/* 收起态同样不能隐藏 span（浮层里的行要靠它显示名字），
   只在不同的 class 上做显隐，不用 :has() —— WebView2 版本不可控。
   但「已隐藏」分组的展开箭头必须保留：只剩一个灰色眼睛图标时毫无「可展开」提示，
   用户会以为那是个普通图标、找不到展开入口。箭头靠 margin-left:auto 顶到行尾。 */
.sidebar.collapsed .hidden-toggle span { display: none; }
.sidebar.collapsed .hidden-item span { display: inline; }
/* 收起态把分组按钮从「禁用灰」提回可点色，配合保留的箭头给出明确的可展开信号 */
.sidebar.collapsed .hidden-toggle { color: var(--color-text-secondary); }
.sidebar.collapsed .hidden-toggle .hidden-caret { color: var(--color-text-secondary); }

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

/* 导航项行尾的「后台活动」指示点（provider 表见 DOT_PROVIDERS，键是 registry 的 NavDotKind）：
   running = 还在跑（呼吸动画），done = 已跑完等你回来看。
   颜色不用红：红色留给监控掉线这类真故障（与通知中心角标同一口径）。 */
.nav-dot {
  flex-shrink: 0; margin-left: auto;
  width: 7px; height: 7px; border-radius: 50%;
  background: var(--color-accent);
}
.nav-dot.running { animation: nav-dot-pulse 1.1s ease-in-out infinite; }
@keyframes nav-dot-pulse { 0%, 100% { opacity: 1; } 50% { opacity: .25; } }
/* NavBadge 也挂在行尾，两者同排时留一点间距 */
.nav-dot + * { margin-left: 4px; }
/* 收起态照样要看见：`.collapsed .nav-item span` 会 display:none（0-3-1），
   这里必须同样的权重以上才能翻盘（nav-badge 是同样的坑，见上） */
.sidebar.collapsed .nav-item .nav-dot {
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
.hidden-list { display: flex; flex-direction: column; gap: 1px; padding-left: 8px; border-left: 1px dashed var(--color-border-secondary); margin: 2px 0 0 10px; }
/* 显隐交给 v-if + Transition（高度由 JS 钩子量，见 onCollapseEnter）；
   as-popover 只负责换定位方式，两者互不干涉。 */
.collapse-enter-active,
.collapse-leave-active {
  overflow: hidden;
  transition: height var(--transition-base), opacity var(--transition-base);
}
.collapse-enter-from, .collapse-leave-to { opacity: 0; }
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
