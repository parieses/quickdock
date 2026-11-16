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
  Download,
  Folder,
  Info,
  Loader2,
  Pencil,
  Plus,
  Search,
  Server,
  Trash2,
  X,
} from '@lucide/vue'
import { SelectLocalFile } from '../../bindings/quickdock/services/appservice'
import { ImportSshConfig } from '../../bindings/quickdock/services/sshx/sshservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { isAppLocked } from '../utils/appLock'
import { sshEnvClass, sshEnvKey, sshTargetFull, type SshHost } from '../utils/ssh'
import {
  flattenSshTree,
  filterSshTree,
  resolveDrop,
  type DropPosition,
  type SshHostDisplay,
  type SshTreeNode,
} from '../utils/sshTree'

const { t } = useI18n()
const toast = useToast()

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
  (e: 'detail', host: SshHost): void
  (e: 'delete', host: SshHost): void
  (e: 'create', parentId: string): void
  (e: 'createFolder', parentId: string): void
  (e: 'move', payload: { id: string; parentId: string }): void
  (e: 'batch', folderId: string): void
  /** 主机清单有变化，通知父级重新 ListHosts（沿用本组件「事件出、父级刷新」的既有机制）。 */
  (e: 'refresh'): void
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
  // resolveDrop 返回 null = 非法落点（拖进自己的子孙）。此时什么都不做，
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
  if (isAppLocked()) return
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

// ---- 主机展示辅助：tags / color / icon ----
//
// 项目里没有「图标名 → lucide 组件」的通用映射/组件，且 icon 字段是自由文本输入，
// 不引新依赖，直接取名字首字母渲染成小徽标（见模板 .tree-hicon）。

/** 逗号分隔的标签串 → 去空、去首尾空格的 chip 文本。 */
function hostTags(h: SshHostDisplay): string[] {
  return (h.tags || '').split(',').map((s) => s.trim()).filter(Boolean)
}

/**
 * color 存的是设计 token 的**变量名**（R4：色值只能来自 style.css）。
 * 这里只把变量名转成预定义 class 名，真正的 var(--color-xxx) 写在下方
 * <style scoped> 的 .ssh-color-* 规则里（R7 禁止内联 style，不能用 :style 拼 var()）。
 * 未登记的 token 一律忽略，避免拼出未定义变量。
 */
const COLOR_CLASS: Record<string, string> = {
  '--color-accent': 'ssh-color-accent',
  '--color-danger': 'ssh-color-danger',
  '--color-warning': 'ssh-color-warning',
  '--color-success': 'ssh-color-success',
}
function hostColorClass(h: SshHostDisplay): string {
  return COLOR_CLASS[h.color || ''] || ''
}

/** 没有通用图标映射：icon 名取首字母（Array.from 兼容 emoji / 代理对）。 */
function hostIconInitial(h: SshHostDisplay): string {
  const s = (h.icon || '').trim()
  return s ? (Array.from(s)[0] || '').toUpperCase() : ''
}

// ---- 从 SSH config 单向导入 ----
interface ImportSummary {
  imported: number
  total: number
  skipped: string[]
}

const importing = ref(false)
const importResult = ref<ImportSummary | null>(null)

/**
 * 选文件 → 导入 → 回显 → 刷新。
 * - 路径走宿主门面 SelectLocalFile（Wails 下 <input type=file> 拿不到绝对路径）。
 * - ImportSshConfig 单向只读导入，绝不写回用户的 ~/.ssh/config。
 * - 成功后 emit('refresh')，复用本组件「事件出、父级刷新」的既有机制通知父级 load()。
 */
async function importSshConfig() {
  if (importing.value) return
  try {
    // 取消选择时后端返回空串且 Code=0（见 SelectLocalFile 注释），静默返回即可。
    const path = (unwrap<string>(await SelectLocalFile(t('sshImportPickTitle'))) ?? '').trim()
    if (!path) return
    importing.value = true
    importResult.value = null
    const data = unwrap<ImportSummary>(await ImportSshConfig(path))
    importResult.value = {
      imported: data?.imported ?? 0,
      total: data?.total ?? 0,
      skipped: Array.isArray(data?.skipped) ? data.skipped : [],
    }
    emit('refresh')
  } catch (e) {
    logErr('SshHostTree.importSshConfig', e)
    toast.error(getErrorMessage(e))
  } finally {
    importing.value = false
  }
}
</script>

<template>
  <div class="tree">
    <!-- 导入工具条：放在树自身的工具条里，不塞进 SshHostDialog 所在的父级页头。
         整条 sticky，滚动清单时按钮和导入结果都留在顶部。 -->
    <div class="tree-import-bar">
      <div class="tree-toolbar">
        <button class="qd-btn qd-btn-sm qd-btn-ghost tree-import-btn" :disabled="importing" @click="importSshConfig">
          <Loader2 v-if="importing" :size="12" class="tree-spin" />
          <Download v-else :size="12" />
          <span>{{ t('sshImportConfig') }}</span>
        </button>
      </div>

      <!-- 导入结果：imported/total 汇总 + 逐条 skipped。
           skipped 是后端给的可读原因串，属数据不是 UI 文案，直接渲染。 -->
      <div v-if="importResult" class="tree-import-result">
        <div class="tree-import-head">
          <p class="tree-import-summary">
            {{ t('sshImportSummary', { imported: importResult.imported, total: importResult.total }) }}
          </p>
          <button class="tree-import-clear" :title="t('close')" @click="importResult = null">
            <X :size="11" />
          </button>
        </div>
        <template v-if="importResult.skipped.length">
          <p class="tree-import-skipped-title">
            {{ t('sshImportSkipped', { n: importResult.skipped.length }) }}
          </p>
          <ul class="tree-import-skipped">
            <li v-for="(s, i) in importResult.skipped" :key="i">{{ s }}</li>
          </ul>
        </template>
      </div>
    </div>

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

          <!-- 主机：环境色条 + 图标 + 名字/标签 + user@host + 环境徽标 -->
          <template v-else>
            <!-- 左色条：env 色是默认；仅当 color 非空时由 .ssh-color-* 覆盖它。
                 优先级见 <style scoped> 里 .ssh-color-* 那段的注释。 -->
            <span class="tree-rail" :class="[sshEnvClass(n.host.env), hostColorClass(n.host)]" />
            <span class="tree-main">
              <span class="tree-name">
                <!-- 项目没有「图标名 → 组件」的通用映射，icon 存的是文本名：
                     不引新依赖，取首字母渲染成小徽标。 -->
                <span
                  v-if="hostIconInitial(n.host)"
                  class="tree-hicon"
                  :title="n.host.icon || ''"
                >{{ hostIconInitial(n.host) }}</span>
                <i v-if="busyHostIds.has(n.host.id)" class="tree-live" :title="t('sshRestored')" />
                <span class="tree-name-text">{{ n.host.name || n.host.host }}</span>
                <!-- 标签拆成小 chip，复用现有 token 样式，不新造体系 -->
                <span v-for="tag in hostTags(n.host)" :key="tag" class="tree-tag">{{ tag }}</span>
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
            <!-- 详情只在主机行上：文件夹没有「连接参数」可看 -->
            <button
              v-if="n.kind !== 'folder'"
              class="tree-act"
              :title="t('sshHostDetail')"
              @click.stop="emit('detail', n.host)"
            >
              <Info :size="11" />
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
      <div v-key-click
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

/* 导入工具条 + 结果：整条 sticky 在滚动区顶部，按钮不会随清单滚走。 */
.tree-import-bar {
  position: sticky;
  top: 0;
  z-index: 1;
  flex: 0 0 auto;
  /* 上方不再留白：搜索框自带 --space-3 下外边距，按钮改成轻量 ghost 后
     再叠一层 padding 会把搜索框和操作行拉成两条互不相关的带。 */
  padding: 0 0 var(--space-2);
  background: var(--color-bg-primary);
}

.tree-toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

/* 从 SSH config 导入是一次性的低频操作，别做成撑满左栏的实心横幅：
   整宽 + 居中 + 描边会在搜索框和主机列表之间形成一条比列表行还重的隔离带。
   改用 qd-btn-sm + qd-btn-ghost（24px、无底无框、hover 才浮色），靠左对齐，
   尺寸与页头那排图标工具保持同一档。align-self 只约束自身，容器仍是 flex-start。 */
.tree-import-btn {
  align-self: flex-start;
}

/* 复用本文件已有的 tree-spin 关键帧，不给按钮再起一套动画 */
.tree-spin {
  animation: tree-spin 900ms linear infinite;
}

.tree-import-result {
  margin-top: var(--space-2);
  padding: var(--space-3) var(--space-4);
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.tree-import-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}

.tree-import-summary {
  margin: 0;
  color: var(--color-text-primary);
}

.tree-import-clear {
  display: flex;
  padding: var(--space-1);
  color: var(--color-text-muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.tree-import-clear:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.tree-import-skipped-title {
  margin: var(--space-3) 0 var(--space-1);
  color: var(--color-warning);
}

.tree-import-skipped {
  margin: 0;
  padding-left: var(--space-6);
  max-height: 120px;
  overflow-y: auto;
  color: var(--color-text-muted);
}

.tree-import-skipped li {
  line-height: 1.6;
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
  font-size: var(--text-sm);
  text-align: center;
}

.tree-empty-cta {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-5);
  font-size: var(--text-sm);
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
  border-radius: var(--radius-xs);
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

/*
 * 自定义颜色（color 字段存的 token 名）覆盖环境色 —— 优先级：
 *   1. 默认走上面的 .tree-rail.env-*（prod 红 / staging 黄 / test 灰），完全保留原有 env 着色；
 *   2. 仅当 color 非空且是已登记 token 时，下面这组 .ssh-color-* 才覆盖左色条背景。
 * 环境徽标 .tree-env 仍按 env 显示：那是「环境」语义，不该被自定义色盖掉。
 * 与 .tree-rail.env-* 同特异性，靠写在后面（源码顺序）取胜；R7 禁止内联 style，
 * 所以色值只以 var(--color-xxx) 出现在这些预定义 class 里，脚本只负责拼 class 名。
 */
.tree-rail.ssh-color-accent {
  background: var(--color-accent);
}

.tree-rail.ssh-color-danger {
  background: var(--color-danger);
}

.tree-rail.ssh-color-warning {
  background: var(--color-warning);
}

.tree-rail.ssh-color-success {
  background: var(--color-success);
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
  font-size: var(--text-md);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 名字单独包一层，才能在 flex 里被标签 chip 挤窄时自己出省略号 */
.tree-name-text {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* icon：没有通用图标组件/映射，取名字首字母的小徽标（全部复用已有 token） */
.tree-hicon {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  font-size: var(--text-2xs);
  font-weight: 600;
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-xs);
}

/* tags：小 chip，沿用与 .tree-count / .tree-env 同一批 token，不新造体系 */
.tree-tag {
  flex: 0 0 auto;
  padding: 0 var(--space-2);
  font-size: var(--text-2xs);
  line-height: 1.6;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
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
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tree-count {
  flex: 0 0 auto;
  padding: 0 var(--space-2);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-full);
}

.tree-env {
  flex: 0 0 auto;
  padding: 0 var(--space-2);
  font-size: var(--text-2xs);
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
  gap: var(--space-1);
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
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-sm);
  box-shadow: 0 8px 28px var(--color-bg-overlay);
}

.tree-menu-item {
  display: block;
  width: 100%;
  padding: var(--space-3) var(--space-4);
  font-size: var(--text-sm);
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
