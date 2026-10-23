<script lang="ts" setup>
/**
 * 左侧会话树：文件夹分组 + 主机，支持折叠与拖拽排序（#13）。
 *
 * 为什么拖拽用「上/中/下三带」而不是简单的 sortable：
 * 树形结构的拖拽必须能表达「放进这个文件夹」和「排在这个兄弟前面」两种语义。
 * 只做其中一种，用户就没法把一台机器拖进分组、也没法调整组内顺序。
 *
 * 三带的判定按鼠标在行内的纵向位置（0-33% 上、33-67% 中、67-100% 下），
 * 中带只在目标是文件夹时才是「放进去」，落在主机上则退化为「插到它前面」。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ChevronDown,
  ChevronRight,
  Folder,
  Pencil,
  Plus,
  Search,
  Server,
  Trash2,
} from '@lucide/vue'
import { sshEnvClass, sshEnvKey, sshTargetFull, type SshHost } from '../utils/ssh'
import { flattenSshTree, filterSshTree, resolveDrop, type DropPosition, type SshTreeNode } from '../utils/sshTree'

const { t } = useI18n()

const props = defineProps<{
  tree: SshTreeNode[]
  loading: boolean
  loadErr: string
  /** 当前有会话的主机 id 集合 */
  busyHostIds: Set<string>
  /** 当前选中的主机 id（多标签下可能有多个） */
  activeHostIds: Set<string>
  /** 折叠状态由父级持有，切主机时不希望折叠被重置 */
  collapsed: Set<string>
  /** 搜索关键字（空串＝不过滤）。过滤保留祖先链，命中文件夹时整棵子树保留 */
  keyword?: string
}>()

const emit = defineEmits<{
  (e: 'select', host: SshHost): void
  (e: 'toggle', folderId: string): void
  (e: 'edit', host: SshHost): void
  (e: 'delete', host: SshHost): void
  (e: 'create', parentId: string): void
  (e: 'createFolder', parentId: string): void
  (e: 'move', payload: { id: string; parentId: string }): void
  (e: 'batch', folderId: string): void
}>()

const dragId = ref('')
/** 当前悬停的落点：{ targetId, position } */
const dropHint = ref<{ targetId: string; position: DropPosition } | null>(null)
/** 拖到节点之间的缝隙（before/after）时的插入线位置 */
const gapBefore = ref('')
const gapAfter = ref('')

// 展平后的行：折叠的分组不展开子节点，但分组自身仍是一行。
// 空态只在该行数为 0（即树上连一个分组都没有）时才显示——
// 否则「只有一个空分组」会被误判成「没有任何主机」而用占位图把分组整个藏起来，
// 用户建了分组却看不见，更没法往里加连接。
//
// 搜索态下强制全展开：命中项若藏在折叠的分组里，用户会得到「有结果但看不见」，
// 那种「明明有却找不到」比搜不到更让人怀疑功能坏了。
const searching = computed(() => !!props.keyword?.trim())
const visibleTree = computed(() =>
  searching.value ? filterSshTree(props.tree, props.keyword || '') : props.tree,
)
const rows = computed(() =>
  flattenSshTree(visibleTree.value, searching.value ? new Set<string>() : props.collapsed),
)

/** 拖拽悬停：按纵向位置算出落点，并顺手维护插入线的显示位置。 */
function onDragOver(n: SshTreeNode, ev: DragEvent) {
  if (!dragId.value || dragId.value === n.host.id) {
    dropHint.value = null
    return
  }
  ev.preventDefault()
  const rect = (ev.currentTarget as HTMLElement).getBoundingClientRect()
  const ratio = (ev.clientY - rect.top) / Math.max(rect.height, 1)

  let position: DropPosition
  if (ratio < 0.33) position = 'before'
  else if (ratio > 0.67) position = 'after'
  else position = n.kind === 'folder' ? 'inside' : 'before'

  dropHint.value = { targetId: n.host.id, position }
  gapBefore.value = position === 'before' ? n.host.id : ''
  gapAfter.value = position === 'after' ? n.host.id : ''
  if (position === 'inside') {
    gapBefore.value = ''
    gapAfter.value = ''
  }
}

function onDragLeave() {
  dropHint.value = null
  gapBefore.value = ''
  gapAfter.value = ''
}

function onDrop() {
  const hint = dropHint.value
  const id = dragId.value
  resetDrag()
  if (!hint || !id) return
  const res = resolveDrop(props.tree, id, hint.targetId, hint.position)
  // resolveDrop 返回 null = 非法落点（拖进自己的子孙）。此时**什么都不做**，
  // 静默拒绝比弹一个错误框好——用户拖错了，取消掉就行，不该被训斥。
  if (!res) return
  emit('move', { id, parentId: res.parentId })
}

function onDragStart(n: SshTreeNode, ev: DragEvent) {
  dragId.value = n.host.id
  ev.dataTransfer?.setData('text/plain', n.host.id)
  if (ev.dataTransfer) ev.dataTransfer.effectAllowed = 'move'
}

function resetDrag() {
  dragId.value = ''
  dropHint.value = null
  gapBefore.value = ''
  gapAfter.value = ''
}

// ---- 文件夹「+」弹出菜单 ----
//
// 以前文件夹的 + 只做一件事（要么建主机、要么建子分组），用户点错就误建。
// 这里把 + 做成菜单：明确给出「在此分组内新建主机」和「新建子分组」两个选项，
// 消除「点 + 结果建了个分组」的困惑。
const folderMenu = ref<{ x: number; y: number; folderId: string } | null>(null)

function openFolderMenu(e: MouseEvent, folderId: string) {
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
  const x = Math.min(rect.right, window.innerWidth - 180)
  const y = Math.min(rect.bottom + 4, window.innerHeight - 120)
  folderMenu.value = { x, y, folderId }
}

function closeFolderMenu() {
  folderMenu.value = null
}

function folderMenuAction(kind: 'host' | 'subfolder' | 'batch') {
  const m = folderMenu.value
  closeFolderMenu()
  if (!m) return
  if (kind === 'host') emit('create', m.folderId)
  else if (kind === 'subfolder') emit('createFolder', m.folderId)
  // 批量执行的默认目标就是「这个分组里的全部主机」，与用户在这个位置上
  // 点右键的心智一致；弹窗里还能再改勾选，所以按子树预选不会造成误操作。
  else emit('batch', m.folderId)
}

function onMenuDocDown(e: MouseEvent) {
  if (!folderMenu.value) return
  const el = e.target as HTMLElement | null
  if (!el || !el.closest('.tree-folder-menu')) closeFolderMenu()
}

function onMenuDocKey(e: KeyboardEvent) {
  if (folderMenu.value && e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    closeFolderMenu()
  }
}

onMounted(() => {
  document.addEventListener('mousedown', onMenuDocDown)
  document.addEventListener('keydown', onMenuDocKey, true)
})

onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onMenuDocDown)
  document.removeEventListener('keydown', onMenuDocKey, true)
})

/** 落点在行内的类名，供模板高亮。 */
function hintClass(n: SshTreeNode): string[] {
  const h = dropHint.value
  if (!h || h.targetId !== n.host.id) return []
  if (h.position === 'inside') return ['drop-inside']
  if (h.position === 'before') return ['drop-before']
  return ['drop-after']
}

function isDropTarget(n: SshTreeNode): boolean {
  return dropHint.value?.targetId === n.host.id
}

function onRowClick(n: SshTreeNode) {
  if (n.kind === 'folder') {
    emit('toggle', n.host.id)
    return
  }
  emit('select', n.host)
}
</script>

<template>
  <div class="tree">
    <div v-if="loading && rows.length === 0" class="tree-hint">
      <span class="tree-spinner" />
    </div>

    <div v-else-if="rows.length === 0" class="tree-hint">
      <!-- 搜索无结果时不给「新建第一台主机」的 CTA：用户手里明明有主机，
           这里再劝他新建只会更困惑，他要的是「换个关键字」。 -->
      <template v-if="searching">
        <Search :size="26" class="tree-hint-icon" />
        <p class="tree-hint-text">{{ t('sshHostNoMatch') }}</p>
      </template>
      <template v-else>
        <Server :size="26" class="tree-hint-icon" />
        <p class="tree-hint-text">{{ loadErr ? t('loadFailed') : t('sshHostsEmpty') }}</p>
        <button class="tree-empty-cta" @click="emit('create', '')">
          <Plus :size="12" />
          <span>{{ t('sshAddFirst') }}</span>
        </button>
      </template>
    </div>

    <template v-else>
      <!--
        gap（插入线）与行必须在**同一个** v-for 里。
        拆成两个 v-for 时前一个的循环变量对后一个兄弟节点不可见，
        vue-tsc 会报 "Property 'n' does not exist"，而且报错位置离真正原因很远。
      -->
      <template v-for="n in rows" :key="n.host.id">
        <div
          class="tree-gap"
          :class="{ 'is-before': gapBefore === n.host.id, 'is-after': gapAfter === n.host.id }"
        />

        <div
          class="tree-row"
          :class="[
            n.kind === 'folder' ? 'is-folder' : sshEnvClass(n.host.env),
            {
              'is-active': n.kind === 'host' && activeHostIds.has(n.host.id),
              'is-dragging': dragId === n.host.id,
            },
            hintClass(n),
          ]"
          :style="{ paddingLeft: `${10 + n.depth * 13}px` }"
          :title="n.kind === 'folder' ? n.host.name : sshTargetFull(n.host)"
          :draggable="true"
          tabindex="0"
          @click="onRowClick(n)"
          @keydown.enter.prevent="onRowClick(n)"
          @dragstart="onDragStart(n, $event)"
          @dragover="onDragOver(n, $event)"
          @dragleave="onDragLeave"
          @drop.prevent="onDrop"
          @dragend="resetDrag"
        >
          <!-- 文件夹：折叠箭头 + 名字 + 主机数 -->
          <template v-if="n.kind === 'folder'">
            <ChevronDown v-if="!collapsed.has(n.host.id)" :size="12" class="tree-caret" />
            <ChevronRight v-else :size="12" class="tree-caret" />
            <Folder :size="13" class="tree-icon" />
            <span class="tree-name">{{ n.host.name }}</span>
            <span v-if="n.hostCount" class="tree-count">{{ n.hostCount }}</span>
          </template>

          <!-- 主机：环境色条 + 名字 + user@host -->
          <template v-else>
            <span class="tree-rail" :class="sshEnvClass(n.host.env)" />
            <span class="tree-main">
              <span class="tree-name">
                <i v-if="busyHostIds.has(n.host.id)" class="tree-live" :title="t('sshRestored')" />
                {{ n.host.name || n.host.host }}
              </span>
              <span class="tree-target">{{ sshTargetFull(n.host) }}</span>
            </span>
            <span class="tree-env" :class="sshEnvClass(n.host.env)">{{ t(sshEnvKey(n.host.env)) }}</span>
          </template>

          <span class="tree-actions">
            <button
              v-if="n.kind === 'folder'"
              class="tree-act"
              :title="t('sshNewInFolder')"
              @click.stop="openFolderMenu($event, n.host.id)"
            >
              <Plus :size="11" />
            </button>
            <button
              v-else
              class="tree-act"
              :title="t('sshNewSiblingHost')"
              @click.stop="emit('create', n.host.parentId ?? '')"
            >
              <Plus :size="11" />
            </button>
            <button class="tree-act" :title="t('sshEdit')" @click.stop="emit('edit', n.host)">
              <Pencil :size="11" />
            </button>
            <button
              class="tree-act tree-act--danger"
              :title="t('sshDelete')"
              @click.stop="emit('delete', n.host)"
            >
              <Trash2 :size="11" />
            </button>
          </span>
        </div>
      </template>

      <!-- 末节点的下侧插入线：循环里只有「上侧」，最后一行下面还得补一条 -->
      <div class="tree-gap" :class="{ 'is-after': gapAfter === rows[rows.length - 1]?.host.id }" />
    </template>

    <!-- 文件夹「+」菜单：用 Teleport 脱离 .ssh-side 的 overflow 裁剪 -->
    <Teleport to="body">
      <div v-if="folderMenu" class="tree-folder-backdrop" @click="closeFolderMenu" @contextmenu.prevent="closeFolderMenu" />
      <div
        v-if="folderMenu"
        class="tree-folder-menu"
        :style="{ left: folderMenu.x + 'px', top: folderMenu.y + 'px' }"
        @click.stop
        @contextmenu.prevent
      >
        <button class="tree-menu-item" @click="folderMenuAction('host')">
          {{ t('sshNewInFolder') }}
        </button>
        <button class="tree-menu-item" @click="folderMenuAction('subfolder')">
          {{ t('sshNewSubFolder') }}
        </button>
        <div class="tree-menu-sep" />
        <button class="tree-menu-item" @click="folderMenuAction('batch')">
          {{ t('sshBatchFolderEntry') }}
        </button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.tree {
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow-y: auto;
  padding: 0 var(--space-3) var(--space-5);
}

.tree-hint {
  flex: 1 1 auto;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-4);
  padding: var(--space-6) var(--space-4);
  color: var(--color-text-muted);
}

.tree-hint-icon {
  color: var(--color-text-disabled);
}

.tree-hint-text {
  margin: 0;
  font-size: 11.5px;
  text-align: center;
}

.tree-empty-cta {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-5);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.tree-empty-cta:hover {
  background: var(--color-bg-hover);
}

.tree-spinner {
  width: 15px;
  height: 15px;
  border: 2px solid var(--color-border-light);
  border-top-color: var(--color-accent);
  border-radius: var(--radius-full);
  animation: tree-spin 700ms linear infinite;
}

@keyframes tree-spin {
  to {
    transform: rotate(360deg);
  }
}

/* 插入线占位：高度固定，拖拽时才显形，避免布局跳动 */
.tree-gap {
  height: 0;
  flex: 0 0 auto;
  position: relative;
}

.tree-gap.is-before::before,
.tree-gap.is-after::before {
  content: '';
  position: absolute;
  left: var(--space-3);
  right: var(--space-3);
  height: 2px;
  border-radius: 1px;
  background: var(--color-accent);
}

.tree-gap.is-before::before {
  bottom: -1px;
}

.tree-gap.is-after::before {
  top: -1px;
}

.tree-row {
  position: relative;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
  padding-right: var(--space-4);
  min-height: 34px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background var(--transition-fast);
}

.tree-row:hover {
  background: var(--color-bg-hover);
}

.tree-row:focus-visible {
  outline: 1px solid var(--color-accent);
  outline-offset: -1px;
}

.tree-row.is-active {
  background: var(--color-bg-tertiary);
}

.tree-row.is-folder {
  cursor: pointer;
}

.tree-row.is-dragging {
  opacity: 0.4;
}

/* 拖拽落点三态：上/中/下。用边框而不是背景色，
   背景色会和 hover / active 撞色，分不清是选中还是正在拖。 */
.tree-row.drop-before {
  box-shadow: inset 0 2px 0 var(--color-accent);
}

.tree-row.drop-after {
  box-shadow: inset 0 -2px 0 var(--color-accent);
}

.tree-row.drop-inside {
  outline: 1px dashed var(--color-accent);
  outline-offset: -2px;
  background: var(--color-accent-bg);
}

.tree-caret {
  flex: 0 0 auto;
  color: var(--color-text-muted);
}

.tree-icon {
  flex: 0 0 auto;
  color: var(--color-text-secondary);
}

.tree-rail {
  flex: 0 0 auto;
  width: 3px;
  height: 22px;
  border-radius: var(--radius-full);
}

.tree-rail.env-prod {
  background: var(--color-danger);
}

.tree-rail.env-staging {
  background: var(--color-warning);
}

.tree-rail.env-test {
  background: var(--color-border-light);
}

.tree-main {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  flex: 1 1 auto;
}

.tree-name {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: 12.5px;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tree-row.is-folder .tree-name {
  color: var(--color-text-primary);
}

.tree-live {
  flex: 0 0 auto;
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background: var(--color-success);
}

.tree-target {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tree-count {
  flex: 0 0 auto;
  padding: 0 var(--space-2);
  font-size: 10.5px;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-full);
}

.tree-env {
  flex: 0 0 auto;
  padding: 0 var(--space-2);
  font-size: 10px;
  border-radius: var(--radius-xs);
  border: 1px solid transparent;
}

.tree-env.env-prod {
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-bg);
}

.tree-env.env-staging {
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-color: var(--color-warning-bg);
}

.tree-env.env-test {
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}

.tree-actions {
  position: absolute;
  right: var(--space-3);
  top: 50%;
  display: flex;
  gap: 2px;
  opacity: 0;
  transform: translateY(-50%);
  transition: opacity var(--transition-fast);
}

.tree-row:hover .tree-actions {
  opacity: 1;
}

.tree-row:hover .tree-env {
  opacity: 0;
}

.tree-act {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.tree-act:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.tree-act--danger:hover {
  color: var(--color-danger);
  border-color: var(--color-danger-bg);
  background: var(--color-danger-bg);
}

/* 文件夹「+」菜单 */
.tree-folder-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1900;
}

.tree-folder-menu {
  position: fixed;
  z-index: 2000;
  min-width: 168px;
  padding: var(--space-1);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: 0 8px 28px var(--color-bg-overlay);
}

.tree-menu-item {
  display: block;
  width: 100%;
  padding: var(--space-3) var(--space-4);
  font-size: 12px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  text-align: left;
}

.tree-menu-item:hover {
  background: var(--color-bg-active);
}

/* 分隔线：把「新建类」与「操作类」菜单项分开。批量执行会动到整个分组，
   和上面两个「加一个节点」不是同一类动作，视觉上不该连成一片。 */
.tree-menu-sep {
  height: 1px;
  margin: var(--space-1) var(--space-2);
  background: var(--color-border);
}
</style>
