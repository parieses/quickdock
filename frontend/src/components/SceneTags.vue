<script setup lang="ts">
import { computed, inject, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { useWorkspaceStore } from '../stores/workspace'
import { useFloatMenu } from '../composables/useFloatMenu'
import { useDragSort } from '../composables/useDragSort'
import { pickExportScene } from '../utils/sceneIO'
import { isDialogCanceled } from '../utils/error'
import type { Scene, ToastAPI } from '../types'

import { reportError } from '../utils/reportError'
const store = useWorkspaceStore()
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 场景声明式导入/导出 ----
// 导出成 quickdock-scene.json（只含声明，不含 id 与本机统计），可放进项目仓库随代码走。
async function menuExport() {
  const id = menuSceneId.value
  const sc = store.scenes.find(s => s.id === id)
  ctxMenu.hide()
  try {
    const r = await pickExportScene(id, sc?.name || 'scene', t('sceneExportTitle'), t('sceneFileFilter'))
    if (r) toast.success(t('sceneExportDone', { collections: r.collections, items: r.items }))
  } catch (e) {
    // 保存对话框点取消时 Wails 会抛错，那不是失败。
    if (isDialogCanceled(e)) return
    reportError(e, 'SceneTags.menuExport')
  }
}

// 导入入口已移除（产品决定不做导入，只保留导出）。

// 已打开的场景标签页列表
const openedTabs = computed(() => {
  return store.openedSceneIds
    .map(id => store.scenes.find(s => s.id === id))
    .filter((s): s is Scene => s !== undefined)
})

// ---- 标签页拖拽排序 ----
// 复用 useDragSort 管道：落点按 openedSceneIds 顺序算，松手后写回并落库（reorderOpenedTabs）。
const {
  dragId: dragTabId,
  dragOverIndex,
  onDragStart: onDragStartTab,
  onDragOver: onDragOverTab,
  onDrop: onDropTab,
  onDragEnd: onDragEndTab,
} = useDragSort({
  ids: () => store.openedSceneIds,
  onReorder: (ids) => { store.reorderOpenedTabs(ids) },
})

// 右键菜单（floating-ui：跟随鼠标 + 防溢出翻转）
const ctxMenu = useFloatMenu({ offset: 0 })
const menuSceneId = ref('')

// 点击标签切换场景
function selectTab(sceneId: string) {
  if (sceneId !== store.activeSceneId) {
    store.selectScene(sceneId)
  }
}

// 关闭标签
function closeTab(sceneId: string, event: Event) {
  event.stopPropagation()
  store.closeSceneTab(sceneId)
}

// 右键菜单
function onContextMenu(sceneId: string, event: MouseEvent) {
  event.preventDefault()
  menuSceneId.value = sceneId
  ctxMenu.showAt(event.clientX, event.clientY)
}

function menuClose() {
  store.closeSceneTab(menuSceneId.value)
  ctxMenu.hide()
}

function menuCloseLeft() {
  store.closeTabsToLeft(menuSceneId.value)
  ctxMenu.hide()
}

function menuCloseRight() {
  store.closeTabsToRight(menuSceneId.value)
  ctxMenu.hide()
}

function menuCloseOthers() {
  store.closeOtherTabs(menuSceneId.value)
  ctxMenu.hide()
}

// 菜单项是否可用
const menuSceneIdx = computed(() => {
  return store.openedSceneIds.indexOf(menuSceneId.value)
})
const canCloseLeft = computed(() => menuSceneIdx.value > 0)
const canCloseRight = computed(() => menuSceneIdx.value >= 0 && menuSceneIdx.value < store.openedSceneIds.length - 1)
const canCloseOthers = computed(() => store.openedSceneIds.length > 1)

// 点击任意位置关闭菜单
function onClickAway() {
  ctxMenu.hide()
}
</script>

<template>
  <div v-if="openedTabs.length > 0" class="tag-tabs">
    <div class="tabs-container">
      <!-- 外层改成 div 容器 + 两个同级 button：span 嵌在 button 里是无效的嵌套交互，
           键盘也摸不到关闭按钮。标签与关闭各自是一个可 Tab 到的控件。 -->
      <div v-key-click
        v-for="(tab, i) in openedTabs"
        :key="tab.id"
        :class="['tab', { active: tab.id === store.activeSceneId, dragging: dragTabId === tab.id, 'drag-over': dragOverIndex === i }]"
        :title="tab.name"
        draggable="true"
        @click="selectTab(tab.id)"
        @contextmenu="onContextMenu(tab.id, $event)"
        @dragstart="onDragStartTab(tab.id, $event, i)"
        @dragover="onDragOverTab(i, $event)"
        @drop="onDropTab(tab.id, $event)"
        @dragend="onDragEndTab"
      >
        <!-- 标签本身也是 button，便于键盘 Tab 聚焦；click 冒泡到容器统一处理切换，
             关闭按钮在 closeTab 里 stopPropagation，不会误触发切换。 -->
        <button class="tab-label">{{ tab.name }}</button>
        <button
          v-key-click
          class="tab-close"
          :aria-label="t('close')"
          :title="t('close')"
          @click="closeTab(tab.id, $event)"
        ><X :size="13" /></button>
      </div>
    </div>

    <!-- 右键菜单 -->
    <Teleport to="body">
      <div v-if="ctxMenu.visible.value" class="context-overlay" @click="onClickAway" @contextmenu.prevent="onClickAway">
        <div
          :ref="el => (ctxMenu.floatingRef.value = el as HTMLElement | null)"
          class="context-menu"
          :style="ctxMenu.floatingStyles.value"
        >
          <button class="menu-item" @click="menuClose">{{ t('closeTab') }}</button>
          <button class="menu-item" :disabled="!canCloseLeft" @click="menuCloseLeft">{{ t('closeLeft') }}</button>
          <button class="menu-item" :disabled="!canCloseRight" @click="menuCloseRight">{{ t('closeRight') }}</button>
          <button class="menu-item" :disabled="!canCloseOthers" @click="menuCloseOthers">{{ t('closeOthers') }}</button>
          <button class="menu-item" @click="menuExport">{{ t('sceneExport') }}…</button>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
/* 标签条并入一级页头（.qd-page-header 46px）：不再是页头下面另起的一整行，
   而是紧跟「工作空间」标题、占满中间空档的横向 chip 组。页头自带下缘线，
   所以不再需要原 tab-line；标签也不再做成「上圆角 + 底部连内容」的页签，
   那种形态脱离分隔线后会像一块贴在页头上的补丁。 */
.tag-tabs {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  background: transparent;
}

.tabs-container {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  max-width: 100%;
  /* 标签多到放不下时横向滚动，别像以前那样直接裁掉看不见的标签。
     隐藏滚动条：46px 的页头里再塞一条滚动条会把标签挤窄。 */
  overflow-x: auto;
  scrollbar-width: none;
  --wails-draggable: no-drag;
}
.tabs-container::-webkit-scrollbar { display: none; }

.tab {
  position: relative;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  height: 28px;
  padding: 0 10px;
  min-width: 80px;
  max-width: 180px;
  background: var(--color-bg-tertiary);
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-size: var(--text-sm);
  cursor: pointer;
  white-space: nowrap;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
  --wails-draggable: no-drag;
}

.tab:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.tab:hover .tab-close,
.tab:focus-within .tab-close {
  opacity: 0.6;
}
/* 键盘 Tab 到关闭按钮时也得看得见，不能只靠行 hover */
.tab-close:focus-visible { opacity: 1 !important; }

/* 选中态复用 QdTabs chip 的口径（accent 底 + accent 字 + 描边），不再用
   bg-primary 实心块：那是「选中标签与下方内容连成一片」的旧做法，标签搬进
   页头后下方没有内容可连，实色块压在 bg-secondary 上反而像挖了个洞。 */
.tab.active {
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-weight: 500;
  border-color: var(--color-accent-border-strong);
  z-index: 1;
}

.tab.dragging {
  opacity: 0.4;
}

.tab.drag-over {
  box-shadow: inset 2px 0 0 var(--color-accent);
}

.tab-label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.3;
  padding: 0;
  border: none;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.tab-close {
  position: relative;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: none;
  font-size: var(--text-2xs);
  line-height: 1;
  color: var(--color-text-muted);
  cursor: pointer;
  opacity: 0;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
/* 命中区补到 24px：伪元素外扩 4px，不动布局尺寸、不改标签行高 */
.tab-close::before {
  content: '';
  position: absolute;
  inset: -4px;
  border-radius: var(--radius-xs);
}

.tab-close:hover {
  background: var(--color-bg-active);
  color: var(--color-text-secondary);
  opacity: 1 !important;
}

.tab.active .tab-close {
  opacity: 0.3;
}
</style>
