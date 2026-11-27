<script lang="ts" setup>
/**
 * 会话标签栏（#13）。
 *
 * 每个标签 = 一个全局唯一 id + 一个 hostId + 一个 sessionId。标签不再等于「主机」：
 * 同一台主机现在允许开多个标签（不同目录/不同 sudo 上下文/一个跑更新一个看日志），
 * 用右键「复制会话」或双击标签即可再开一个，后端连接池会复用同一条 TCP、各开一个 channel。
 *
 * 点左树主机时仍是「聚焦该主机已有标签」而不是重新连接：避免手滑多点开一堆孤儿会话。
 * 真正要第二个独立会话，走复制入口（openTab，resume 为 null）。
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { HardDrive, Plus, X, ZoomIn, ZoomOut } from '@lucide/vue'
import { sshEnvClass, type SshHost } from '../utils/ssh'
import type { SshTab } from '../utils/sshTabs'
import { sshFontSize, setSshFontSize, SSH_FONT_MIN, SSH_FONT_MAX } from '../composables/useSshFont'

const { t } = useI18n()

const props = defineProps<{
  tabs: SshTab[]
  activeId: string
  /** 当前标签的文件面板是否已打开（用于按钮的激活态） */
  filesOpen: boolean
}>()

// emits 统一用元组式（docs/conventions/frontend.md §3）；新增 reorder 时一并把
// 历史遗留的回调式签名收拢过来，避免两种风格混在同一个 defineEmits 里。
const emit = defineEmits<{
  select: [tabId: string]
  close: [tabId: string]
  new: []
  /** 切换当前标签的文件面板 */
  files: []
  /** 复制该标签的会话到新标签（同主机第二个独立会话） */
  duplicate: [tabId: string]
  /** 标签拖拽重排：只交下标，由父组件 reorderSshTabs 后回传新数组 */
  reorder: [from: number, to: number]
}>()

const hostOf = computed(() => {
  const map = new Map<string, SshHost>()
  for (const tab of props.tabs) map.set(tab.id, tab.host)
  return map
})

/** 每台主机开了几个标签：>1 时需要给标签加序号区分。 */
const hostCounts = computed(() => {
  const m = new Map<string, number>()
  for (const t of props.tabs) m.set(t.hostId, (m.get(t.hostId) || 0) + 1)
  return m
})

/** 同一主机下每个标签的序号（1 起），用于标签上的角标。 */
const ordinals = computed(() => {
  const seen = new Map<string, number>()
  const out = new Map<string, number>()
  for (const t of props.tabs) {
    const n = (seen.get(t.hostId) || 0) + 1
    seen.set(t.hostId, n)
    out.set(t.id, n)
  }
  return out
})

function shortLabel(tab: SshTab): string {
  return tab.host.name || tab.host.host
}

function titleOf(tab: SshTab): string {
  const h = tab.host
  const ord =
    (hostCounts.value.get(h.id) || 0) > 1 ? ` ·${ordinals.value.get(tab.id)}` : ''
  return `${h.name || h.host} — ${h.user || 'root'}@${h.host}:${h.port || 22}${ord}`
}

// ---- 右键菜单：复制会话 ----
const menu = ref<{ id: string; x: number; y: number } | null>(null)
function openMenu(tabId: string, ev: MouseEvent) {
  menu.value = { id: tabId, x: ev.clientX, y: ev.clientY }
}
function closeMenu() {
  menu.value = null
}
function duplicate(tabId: string) {
  emit('duplicate', tabId)
  closeMenu()
}

// ---- 标签拖拽重排（原生 HTML5 DnD）----
//
// 只把「从哪个下标拖到哪个下标」emit 出去，绝不改 props.tabs：
// 数组归父组件所有，重排要由父组件调 reorderSshTabs 后回传新数组。
// 拖拽反馈靠下面的 CSS class，颜色/描边一律取 style.css 的 token。
const dragFrom = ref<number | null>(null)
const dragOver = ref<number | null>(null)

function onDragStart(index: number, ev: DragEvent) {
  dragFrom.value = index
  dragOver.value = index
  if (ev.dataTransfer) {
    ev.dataTransfer.effectAllowed = 'move'
    // Firefox 必须 setData 才会真正进入拖拽；值仅用于调试，不参与落点判断。
    ev.dataTransfer.setData('text/plain', props.tabs[index]?.id ?? '')
  }
}

function onDragOver(index: number, ev: DragEvent) {
  if (dragFrom.value === null) return
  ev.preventDefault() // 不 preventDefault，drop 不会触发
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'move'
  dragOver.value = index
}

function onDrop(index: number, ev: DragEvent) {
  ev.preventDefault()
  const from = dragFrom.value
  clearDrag()
  if (from === null || from === index) return
  emit('reorder', from, index)
}

function onDragEnd() {
  clearDrag()
}

function clearDrag() {
  dragFrom.value = null
  dragOver.value = null
}
</script>

<template>
  <div class="tabs" role="tablist">
    <div class="tabs-strip">
      <button
        v-for="(tab, i) in tabs"
        :key="tab.id"
        class="tab"
        :class="[
          sshEnvClass(tab.host.env),
          {
            'is-active': tab.id === activeId,
            'is-dragging': dragFrom === i,
            'is-drop-target': dragOver === i && dragFrom !== null && dragFrom !== i,
          },
        ]"
        draggable="true"
        role="tab"
        :aria-selected="tab.id === activeId"
        :title="titleOf(tab)"
        @click="emit('select', tab.id)"
        @dblclick="emit('duplicate', tab.id)"
        @contextmenu.prevent="openMenu(tab.id, $event)"
        @dragstart="onDragStart(i, $event)"
        @dragover="onDragOver(i, $event)"
        @drop="onDrop(i, $event)"
        @dragend="onDragEnd"
      >
        <!-- 生产环境标签用实心红点 + 危险色：多标签并排时颜色是唯一能一眼分辨的线索 -->
        <i class="tab-dot" :class="sshEnvClass(tab.host.env)" />
        <span class="tab-name">{{ shortLabel(tab) }}</span>
        <sup v-if="(hostCounts.get(tab.hostId) || 0) > 1" class="tab-ord">{{ ordinals.get(tab.id) }}</sup>
        <span v-if="tab.busy" class="tab-spinner" />
        <span
          class="tab-x"
          :title="t('sshCloseTab')"
          @click.stop="emit('close', tab.id)"
        >
          <X :size="11" />
        </span>
      </button>

      <button class="tab tab-new" :title="t('sshNewTab')" @click="emit('new')">
        <Plus :size="13" />
      </button>

      <div class="tabs-actions">
        <!-- 终端字号：原来只能靠终端右键菜单里的放大/缩小，入口太深且改完不知道
             存到哪儿去了。这里给一对常驻按钮 + 当前值，一处调整所有标签同步生效。 -->
        <div class="tabs-font" :title="t('sshFontSize')">
          <button
            class="tabs-act"
            :disabled="sshFontSize <= SSH_FONT_MIN"
            :title="t('sshCtxFontSmaller')"
            @click="setSshFontSize(sshFontSize - 1)"
          >
            <ZoomOut :size="13" />
          </button>
          <span class="tabs-font-val">{{ sshFontSize }}</span>
          <button
            class="tabs-act"
            :disabled="sshFontSize >= SSH_FONT_MAX"
            :title="t('sshCtxFontLarger')"
            @click="setSshFontSize(sshFontSize + 1)"
          >
            <ZoomIn :size="13" />
          </button>
        </div>
        <button
          class="tabs-act"
          :class="{ 'is-on': filesOpen }"
          :disabled="!activeId"
          :title="t('sshFiles')"
          @click="emit('files')"
        >
          <HardDrive :size="13" />
        </button>
      </div>
    </div>

    <!-- 右键菜单：复制会话到新标签 -->
    <div v-if="menu" class="tab-menu-backdrop" @click="closeMenu" @contextmenu.prevent="closeMenu">
      <div v-key-click class="tab-menu" :style="{ left: menu.x + 'px', top: menu.y + 'px' }" @click.stop>
        <button class="tab-menu-item" @click="duplicate(menu.id)">
          {{ t('sshDuplicateTab') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tabs {
  flex: 0 0 auto;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}

.tabs-strip {
  display: flex;
  align-items: stretch;
  gap: 1px;
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: thin;
}

.tab {
  position: relative;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  max-width: 190px;
  padding: 0 var(--space-3) 0 var(--space-4);
  height: 30px;
  font-size: var(--text-sm);
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-right: 1px solid var(--color-border);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}

.tab:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.tab.is-active {
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  /* 顶部一条实线标示当前标签，比背景色更明确——
     终端区本身就是深色，背景色的差别在深色主题下几乎看不出来。 */
  box-shadow: inset 0 2px 0 var(--color-accent);
}

/* 生产标签：整条 tab 用危险色描边，多标签并排时最醒目 */
.tab.env-prod.is-active {
  box-shadow: inset 0 2px 0 var(--color-danger);
}

.tab.env-prod {
  background: var(--color-danger-bg);
}

.tab.env-staging.is-active {
  box-shadow: inset 0 2px 0 var(--color-warning);
}

/* 拖拽反馈：源标签半透明表示「已拿起」，落点标签用底部强调线 + 抬起的底色
   表示「会插到这里」。底色/描边/阴影颜色全取 style.css 的 token，无硬编码色值。
   选择器带 [draggable] 把特异性提到与 .tab.env-prod.is-active 同级，
   放在其后即可盖住生产标签的 box-shadow，避免落点提示被吞掉。 */
.tab.is-dragging {
  opacity: 0.4;
  cursor: grabbing;
}

.tab[draggable].is-drop-target {
  background: var(--color-bg-active);
  box-shadow: inset 0 -2px 0 var(--color-accent);
}

.tab-dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: var(--radius-full);
}

.tab-dot.env-prod {
  background: var(--color-danger);
}

.tab-dot.env-staging {
  background: var(--color-warning);
}

.tab-dot.env-test {
  background: var(--color-text-disabled);
}

.tab-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tab-spinner {
  flex: 0 0 auto;
  width: 9px;
  height: 9px;
  border: 1.5px solid var(--color-border-light);
  border-top-color: var(--color-accent);
  border-radius: var(--radius-full);
  animation: tab-spin 700ms linear infinite;
}

@keyframes tab-spin {
  to {
    transform: rotate(360deg);
  }
}

.tab-x {
  display: flex;
  flex: 0 0 auto;
  padding: var(--space-1);
  border-radius: var(--radius-xs);
  color: var(--color-text-muted);
  opacity: 0;
  transition: opacity var(--transition-fast), background var(--transition-fast);
}

.tab:hover .tab-x,
.tab.is-active .tab-x {
  opacity: 1;
}

.tab-x:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.tab-new {
  padding: 0 var(--space-4);
  color: var(--color-text-muted);
}

.tab-new:hover {
  color: var(--color-text-primary);
}

/* 推到最右：标签条会横向滚动，按钮留在末尾比跟着标签滚走更好找 */
.tabs-actions {
  display: flex;
  align-items: center;
  margin-left: auto;
  padding: 0 var(--space-3);
  border-left: 1px solid var(--color-border);
}

.tabs-act {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-muted);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.tabs-act:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.tabs-act.is-on {
  color: var(--color-accent-ink);
  background: var(--color-accent-bg);
}

.tabs-act:disabled {
  cursor: default;
  color: var(--color-text-disabled);
}

/* 字号控制：三个元素紧贴成一组，与右侧文件面板按钮之间留一条分隔线，
   避免「1 3 放大」被看成三个互不相关的按钮 */
.tabs-font {
  display: flex;
  align-items: center;
  gap: 1px;
  margin-right: var(--space-2);
  padding-right: var(--space-2);
  border-right: 1px solid var(--color-border);
}

.tabs-font-val {
  min-width: 16px;
  text-align: center;
  font-size: var(--text-xs);
  font-variant-numeric: tabular-nums;
  color: var(--color-text-muted);
  user-select: none;
}

/* 同一主机的多个标签：右上角小序号，便于区分「哪个是跑更新的、哪个是看日志的」 */
.tab-ord {
  margin-left: var(--space-1);
  font-size: var(--text-2xs);
  font-weight: 500;
  color: var(--color-text-muted);
  font-family: var(--font-family);
}

.tab-menu-backdrop {
  position: fixed;
  inset: 0;
  z-index: var(--z-ctx-backdrop);
}

.tab-menu {
  position: fixed;
  z-index: calc(var(--z-ctx-backdrop) + 1);
  min-width: 168px;
  padding: var(--space-1);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-lg);
}

.tab-menu-item {
  display: block;
  width: 100%;
  padding: var(--space-2) var(--space-3);
  font-size: var(--text-sm);
  font-family: inherit;
  text-align: left;
  color: var(--color-text-primary);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.tab-menu-item:hover {
  background: var(--color-bg-hover);
}
</style>
