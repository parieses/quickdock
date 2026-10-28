<script setup lang="ts">
import { computed, inject, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { useFloatMenu } from '../composables/useFloatMenu'
import { pickExportScene } from '../utils/sceneIO'
import { getErrorMessage } from '../utils/error'
import type { Scene, ToastAPI } from '../types'

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
    toast.error(getErrorMessage(e))
  }
}

// 导入入口已移除（产品决定不做导入，只保留导出）。

// 已打开的场景标签页列表
const openedTabs = computed(() => {
  return store.openedSceneIds
    .map(id => store.scenes.find(s => s.id === id))
    .filter((s): s is Scene => s !== undefined)
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
      <button
        v-for="tab in openedTabs"
        :key="tab.id"
        :class="['tab', { active: tab.id === store.activeSceneId }]"
        :title="tab.name"
        @click="selectTab(tab.id)"
        @contextmenu="onContextMenu(tab.id, $event)"
      >
        <span class="tab-label">{{ tab.name }}</span>
        <span class="tab-close" @click="closeTab(tab.id, $event)">✕</span>
      </button>
    </div>
    <div class="tab-line" />

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
.tag-tabs {
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  background: transparent;
}

.tabs-container {
  display: flex;
  align-items: flex-end;
  gap: 2px;
  /* 高度对齐一级页头（.qd-page-header 44px）：工作空间没有传统页头，
     这条标签栏就是它的顶栏；不等高的话从工作空间切到其它页会上下跳。
     减去 1px 是因为下面还有 .tab-line 那条分隔线。 */
  min-height: 43px;
  padding: 6px 10px 0;
  overflow: hidden;
  -webkit-app-region: no-drag;
}

.tab {
  position: relative;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 7px 10px;
  min-width: 80px;
  max-width: 180px;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-bottom: none;
  border-radius: var(--radius-md) var(--radius-md) 0 0;
  color: var(--color-text-muted);
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
  -webkit-app-region: no-drag;
}

.tab:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-secondary);
}

.tab:hover .tab-close {
  opacity: 0.6;
}

.tab.active {
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  font-weight: 500;
  border-color: var(--color-border);
  padding-bottom: 7px;
  z-index: 1;
}

.tab.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 1px;
  right: 1px;
  height: 2px;
  background: linear-gradient(90deg, var(--color-accent), var(--color-accent-light));
  border-radius: 0 0 1px 1px;
}

.tab-label {
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.3;
}

.tab-close {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: var(--radius-xs);
  font-size: 10px;
  line-height: 1;
  color: var(--color-text-muted);
  opacity: 0;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}

.tab-close:hover {
  background: var(--color-bg-active);
  color: var(--color-text-secondary);
  opacity: 1 !important;
}

.tab.active .tab-close {
  opacity: 0.3;
}

.tab-line {
  height: 1px;
  background: var(--color-border);
  flex-shrink: 0;
}
</style>

<!-- 右键菜单样式（非 scoped：Teleport 到 body 后 scoped 不生效） -->
<style>
.context-overlay {
  position: fixed;
  inset: 0;
  z-index: 9999;
  background: transparent;
}

.context-menu {
  position: fixed;
  display: flex;
  flex-direction: column;
  min-width: 120px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  padding: 4px;
  box-shadow: 0 8px 32px var(--color-bg-overlay);
  backdrop-filter: blur(12px);
}

.context-menu .menu-item {
  display: flex;
  align-items: center;
  padding: 7px 12px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px;
  border-radius: var(--radius-xs);
  cursor: pointer;
  white-space: nowrap;
  transition: background 0.1s;
}

.context-menu .menu-item:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.context-menu .menu-item:disabled {
  color: var(--color-text-disabled);
  cursor: default;
}

.context-menu .menu-item:disabled:hover {
  background: transparent;
  color: var(--color-text-disabled);
}
</style>
