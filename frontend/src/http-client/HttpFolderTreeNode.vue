<script setup lang="ts">
import { computed, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight, Folder, Plus, FolderPlus, Pencil, Trash2, FileText } from '@lucide/vue'
import { useFloatMenu } from '../composables/useFloatMenu'
import type { ApiRequest, HttpFolder, HttpDoc, HttpDragItem, HttpDropTarget } from '../types'

const props = defineProps<{
  folder: HttpFolder
  folders: HttpFolder[]
  requests: ApiRequest[]
  docs: HttpDoc[]
  expanded: Set<string>
  currentId: string
  currentDocId: string
  onToggle: (id: string) => void
  draggingItem: HttpDragItem | null
  dropTarget: HttpDropTarget | null
  onDragStart: (item: HttpDragItem) => void
  onDragEnd: () => void
  onDragOver: (target: HttpDropTarget) => void
  onDrop: (target: HttpDropTarget) => void
}>()

const emit = defineEmits<{
  (e: 'add-request', folderId: string): void
  (e: 'add-folder', folderId: string): void
  (e: 'add-doc', folderId: string): void
  (e: 'rename-folder', folder: HttpFolder): void
  (e: 'delete-folder', folder: HttpFolder): void
  (e: 'select-request', req: ApiRequest): void
  (e: 'delete-request', req: ApiRequest): void
  (e: 'select-doc', doc: HttpDoc): void
  (e: 'delete-doc', doc: HttpDoc): void
}>()

const { t } = useI18n()

const isOpen = computed(() => props.expanded.has(props.folder.id))
const childFolders = computed(() => props.folders.filter(f => (f.parentId || '') === props.folder.id))
const childRequests = computed(() => props.requests.filter(r => (r.folderId || '') === props.folder.id))
const childDocs = computed(() => props.docs.filter(d => (d.folderId || '') === props.folder.id))

// 拖拽高亮
const isDragging = computed(() => props.draggingItem?.kind === 'folder' && props.draggingItem.id === props.folder.id)
const isDropInside = computed(() => props.dropTarget?.kind === 'into-folder' && props.dropTarget.id === props.folder.id)
const isDropAfter = computed(() => props.dropTarget?.kind === 'after-folder' && props.dropTarget.id === props.folder.id)

// 拖拽：目录行上 70% 区域=放入目录，底部 30%=作为同级插入其后
function folderTarget(e: DragEvent): HttpDropTarget {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
  const after = e.clientY - r.top > r.height * 0.7
  return after ? { kind: 'after-folder', id: props.folder.id } : { kind: 'into-folder', id: props.folder.id }
}
function isSelfOrDescendant(folderId: string, ancestorId: string): boolean {
  if (!folderId) return false
  const map = new Map(props.folders.map(f => [f.id, f]))
  let cur: HttpFolder | undefined = map.get(folderId)
  const seen = new Set<string>()
  while (cur) {
    if (cur.id === ancestorId) return true
    if (seen.has(cur.id)) break
    seen.add(cur.id)
    cur = cur.parentId ? map.get(cur.parentId) : undefined
  }
  return false
}
function canDrop(target: HttpDropTarget): boolean {
  const item = props.draggingItem
  if (!item || item.kind !== 'folder') return true
  if ((target.kind === 'into-folder' || target.kind === 'after-folder') && isSelfOrDescendant(item.id, target.id)) return false
  return true
}
// 文档项作为投放区：拖到某文档上 = 移到该文档所在目录（无目录则移到项目根）
function docDropTarget(d: HttpDoc): HttpDropTarget {
  if (d.folderId) return { kind: 'into-folder', id: d.folderId }
  return { kind: 'project-root', projectId: d.projectId || '' }
}
const isDocDragging = (id: string) => props.draggingItem?.kind === 'doc' && props.draggingItem.id === id
function startDrag(e: DragEvent, item: HttpDragItem) {
  e.dataTransfer?.setData('application/json', JSON.stringify(item))
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
  props.onDragStart(item)
}
function folderDragOver(e: DragEvent) {
  const target = folderTarget(e)
  if (!canDrop(target)) return
  e.preventDefault()
  props.onDragOver(target)
}
function folderDrop(e: DragEvent) {
  const target = folderTarget(e)
  if (!canDrop(target)) return
  e.preventDefault()
  props.onDrop(target)
}

// 右键上下文菜单（目录 / 请求 / 文档 的删除等）
type MenuKind = 'folder' | 'request' | 'doc'
const menu = reactive({ kind: '' as MenuKind | '', payload: null as ApiRequest | HttpDoc | HttpFolder | null })
const ctxMenu = useFloatMenu()
function openMenu(e: MouseEvent, kind: MenuKind, payload: ApiRequest | HttpDoc | HttpFolder) {
  e.preventDefault()
  menu.kind = kind
  menu.payload = payload
  ctxMenu.showAt(e.clientX, e.clientY)
}
function closeMenu() { ctxMenu.hide() }
function run(action: string) {
  ctxMenu.hide()
  if (menu.kind === 'folder') {
    const f = menu.payload as HttpFolder
    if (action === 'add-folder') emit('add-folder', f.id)
    else if (action === 'add-request') emit('add-request', f.id)
    else if (action === 'add-doc') emit('add-doc', f.id)
    else if (action === 'rename') emit('rename-folder', f)
    else if (action === 'delete') emit('delete-folder', f)
  } else if (menu.kind === 'request') {
    if (action === 'delete') emit('delete-request', menu.payload as ApiRequest)
  } else if (menu.kind === 'doc') {
    if (action === 'delete') emit('delete-doc', menu.payload as HttpDoc)
  }
}
</script>

<template>
  <div class="folder-node">
    <div
      class="folder-head"
      :class="{ 'drop-inside': isDropInside, 'drop-after': isDropAfter, dragging: isDragging }"
      draggable="true"
      @click="onToggle(folder.id)"
      @contextmenu.prevent="openMenu($event, 'folder', folder)"
      @dragstart="startDrag($event, { kind: 'folder', id: folder.id })"
      @dragend="onDragEnd"
      @dragover="folderDragOver"
      @drop="folderDrop"
    >
      <component :is="isOpen ? ChevronDown : ChevronRight" :size="13" class="folder-caret" />
      <Folder :size="13" class="folder-icon" />
      <span class="folder-name" :title="folder.name">{{ folder.name }}</span>
    </div>
    <div v-if="isOpen" class="folder-children">
      <HttpFolderTreeNode
        v-for="f in childFolders"
        :key="f.id"
        :folder="f"
        :folders="folders"
        :requests="requests"
        :docs="docs"
        :expanded="expanded"
        :current-id="currentId"
        :current-doc-id="currentDocId"
        :on-toggle="onToggle"
        :dragging-item="draggingItem"
        :drop-target="dropTarget"
        :on-drag-start="onDragStart"
        :on-drag-end="onDragEnd"
        :on-drag-over="onDragOver"
        :on-drop="onDrop"
        @add-request="id => emit('add-request', id)"
        @add-folder="id => emit('add-folder', id)"
        @add-doc="id => emit('add-doc', id)"
        @rename-folder="f0 => emit('rename-folder', f0)"
        @delete-folder="f0 => emit('delete-folder', f0)"
        @select-request="r => emit('select-request', r)"
        @delete-request="r => emit('delete-request', r)"
        @select-doc="d => emit('select-doc', d)"
        @delete-doc="d => emit('delete-doc', d)"
      />
      <div
        v-for="d in childDocs"
        :key="d.id"
        :class="['doc-item', { active: d.id === currentDocId, dragging: isDocDragging(d.id) }]"
        draggable="true"
        @click="emit('select-doc', d)"
        @contextmenu.prevent="openMenu($event, 'doc', d)"
        @dragstart="startDrag($event, { kind: 'doc', id: d.id })"
        @dragend="onDragEnd"
        @dragover.prevent="onDragOver(docDropTarget(d))"
        @drop.prevent="onDrop(docDropTarget(d))"
      >
        <FileText :size="12" class="doc-icon" />
        <span class="doc-name" :title="d.name">{{ d.name }}</span>
      </div>
      <div
        v-for="r in childRequests"
        :key="r.id"
        :class="['req-item', { active: r.id === currentId, 'drop-before': dropTarget?.kind === 'before-request' && dropTarget.id === r.id, dragging: draggingItem?.kind === 'request' && draggingItem?.id === r.id }]"
        draggable="true"
        @click="emit('select-request', r)"
        @dragstart="startDrag($event, { kind: 'request', id: r.id })"
        @dragend="onDragEnd"
        @dragover.prevent="onDragOver({ kind: 'before-request', id: r.id })"
        @drop.prevent="onDrop({ kind: 'before-request', id: r.id })"
        @contextmenu.prevent="openMenu($event, 'request', r)"
      >
        <span :class="['req-method', 'method-' + (r.method || 'GET').toLowerCase()]">{{ r.method || 'GET' }}</span>
        <span class="req-name" :title="r.name || r.url">{{ r.name || r.url }}</span>
      </div>
      <div v-if="!childFolders.length && !childDocs.length && !childRequests.length" class="folder-empty">{{ t('httpFolderEmpty') }}</div>
    </div>

    <!-- 右键上下文菜单 -->
    <div v-if="ctxMenu.visible.value" class="ctx-menu-mask" @click="closeMenu" @contextmenu.prevent="closeMenu">
      <div class="ctx-menu" :ref="el => (ctxMenu.floatingRef.value = el as HTMLElement | null)" :style="ctxMenu.floatingStyles.value" @click.stop>
        <template v-if="menu.kind === 'folder'">
          <button class="ctx-item" @click="run('add-folder')"><FolderPlus :size="13" /> {{ t('httpNewSubFolder') }}</button>
          <button class="ctx-item" @click="run('add-request')"><Plus :size="13" /> {{ t('httpNewRequest') }}</button>
          <button class="ctx-item" @click="run('add-doc')"><FileText :size="13" /> {{ t('httpNewDoc') }}</button>
          <div class="ctx-sep"></div>
          <button class="ctx-item" @click="run('rename')"><Pencil :size="13" /> {{ t('httpRenameFolder') }}</button>
          <button class="ctx-item danger" @click="run('delete')"><Trash2 :size="13" /> {{ t('httpDeleteFolder') }}</button>
        </template>
        <template v-else-if="menu.kind === 'request'">
          <button class="ctx-item danger" @click="run('delete')"><Trash2 :size="13" /> {{ t('httpDeleteRequest') }}</button>
        </template>
        <template v-else-if="menu.kind === 'doc'">
          <button class="ctx-item danger" @click="run('delete')"><Trash2 :size="13" /> {{ t('httpDeleteDoc') }}</button>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.folder-node { margin-bottom: 2px; }
.folder-head {
  display: flex; align-items: center; gap: 5px; padding: 5px 6px; border-radius: 6px;
  cursor: pointer; color: var(--color-text-secondary); font-size: 12px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.folder-head:hover { background: var(--color-bg-hover); }
.folder-head.dragging { opacity: 0.4; }
.folder-head.drop-inside { background: var(--color-accent-bg); box-shadow: inset 0 0 0 1px var(--color-accent); }
.folder-head.drop-after { box-shadow: inset 0 -2px 0 0 var(--color-accent); }
.folder-caret { color: var(--color-text-disabled); flex-shrink: 0; }
.folder-icon { color: var(--color-accent); flex-shrink: 0; }
.folder-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.folder-children { padding: 2px 0 4px 14px; }
.folder-empty { padding: 6px 8px; color: var(--color-text-disabled); font-size: 11px; font-style: italic; }

/* 目录内文档项 */
.doc-item {
  display: flex; align-items: center; gap: 6px;
  padding: 6px 8px; border-radius: 6px; cursor: pointer;
  color: var(--color-text-secondary); font-size: 12px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.doc-item:hover { background: var(--color-bg-hover); }
.doc-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.doc-item.dragging { opacity: 0.4; }
.doc-icon { color: #d9920a; flex-shrink: 0; }
.doc-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 目录内请求项（与父级一致） */
.req-item {
  display: flex; align-items: center; gap: 6px;
  padding: 6px 8px; border-radius: 6px; cursor: pointer;
  color: var(--color-text-secondary); font-size: 12px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.req-item:hover { background: var(--color-bg-hover); }
.req-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.req-item.dragging { opacity: 0.4; }
.req-item.drop-before { box-shadow: inset 0 2px 0 0 var(--color-accent); }
.req-method { font-size: 10px; font-weight: 700; flex-shrink: 0; padding: 1px 4px; border-radius: 3px; color: #fff; }
.req-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 方法配色 */
.method-get { background: #2e9e5b; }
.method-post { background: #3a8ae0; }
.method-put { background: #d9920a; }
.method-delete { background: #e8584c; }
.method-patch { background: #9b6ddb; }
.method-head { background: #6b7785; }
.method-options { background: #5a8f9e; }

/* 右键上下文菜单 */
.ctx-menu-mask { position: fixed; inset: 0; z-index: 300; }
.ctx-menu {
  position: fixed; min-width: 160px; padding: 4px; border-radius: 8px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  box-shadow: var(--shadow-lg); z-index: 301;
}
.ctx-item {
  display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px;
  border: none; background: none; cursor: pointer; border-radius: 5px;
  color: var(--color-text-primary); font-size: 12px; font-family: inherit; text-align: left;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ctx-item:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.ctx-item.danger { color: var(--color-danger); }
.ctx-item.danger:hover { background: rgba(232, 76, 76, 0.1); color: var(--color-danger); }
.ctx-sep { height: 1px; background: var(--color-border); margin: 4px 2px; }
</style>
