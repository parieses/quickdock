<script setup lang="ts">
import { computed } from 'vue'
import { Folder, FileText, ChevronRight, ChevronDown, Plus, FolderPlus, Pencil, Trash2 } from '@lucide/vue'
import type { Snippet } from '../types'

interface Props {
  node: any
  depth?: number
  expanded: Set<string>
  editingId: string
  editName: string
  selectedDoc: string
  selectedFolder: string
}
const props = withDefaults(defineProps<Props>(), { depth: 0 })

const emit = defineEmits<{
  (e: 'toggle', id: string): void
  (e: 'select', node: any): void
  (e: 'rename-start', node: any): void
  (e: 'rename-commit', id: string): void
  (e: 'del', node: any): void
  (e: 'create-folder', folderId: string): void
  (e: 'create-doc', folderId: string): void
  (e: 'drop-node', dragId: string, targetId: string): void
}>()

const n = computed<any>(() => props.node)
const isFolder = computed(() => n.value.isFolder)
const isOpen = computed(() => props.expanded.has(n.value.id))
const hasKids = computed(() => n.value.children?.length > 0)

function onDrop(e: DragEvent) {
  e.preventDefault(); e.stopPropagation()
  const did = e.dataTransfer?.getData('text/plain')
  if (did) emit('drop-node', did, n.value.id)
}
function onDragStart(e: DragEvent) {
  e.dataTransfer?.setData('text/plain', n.value.id)
}
</script>

<template>
  <div>
    <div
      draggable
      :style="'padding-left:' + (12 + depth * 16) + 'px'"
      :class="['tn-row', 'tn-' + (isFolder ? 'folder' : 'doc'), { selected: selectedDoc === node.id || selectedFolder === node.id }]"
      @click="isFolder ? emit('toggle', node.id) : emit('select', node)"
      @dragstart="onDragStart"
      @dragover.prevent.stop
      @drop="onDrop"
    >
      <button v-if="isFolder" class="tn-caret" @click.stop="emit('toggle', node.id)">
        <ChevronRight v-if="!isOpen" :size="12" /><ChevronDown v-else :size="12" />
      </button>
      <span v-else class="tn-spacer"></span>
      <component :is="isFolder ? Folder : FileText" :size="13" class="tn-icon" />
      <template v-if="editingId === node.id">
        <input :value="editName" class="tn-input" @keyup.enter="emit('rename-commit', node.id)" @blur="emit('rename-commit', node.id)" @click.stop />
      </template>
      <template v-else>
        <span class="tn-name">{{ node.name || node.keyword }}</span>
      </template>
      <span v-if="isFolder && hasKids" class="tn-count">{{ node.children.length }}</span>
      <span class="tn-actions" @click.stop>
        <button v-if="isFolder" class="tn-act" title="+ 新建子文件夹" @click="emit('create-folder', node.id)"><FolderPlus :size="12" /></button>
        <button v-if="isFolder" class="tn-act" title="+ 新建笔记" @click="emit('create-doc', node.id)"><Plus :size="12" /></button>
        <button class="tn-act" title="Rename" @click="emit('rename-start', node)"><Pencil :size="12" /></button>
        <button class="tn-act danger" title="Delete" @click="emit('del', node)"><Trash2 :size="12" /></button>
      </span>
    </div>
    <template v-if="isFolder && isOpen && hasKids">
      <NoteTreeNode
        v-for="c in node.children" :key="c.id"
        :node="c" :depth="depth + 1"
        :expanded="expanded" :editing-id="editingId" :edit-name="editName"
        :selected-doc="selectedDoc" :selected-folder="selectedFolder"
        @toggle="emit('toggle', $event)" @select="emit('select', $event)"
        @rename-start="emit('rename-start', $event)" @rename-commit="emit('rename-commit', $event)"
        @del="emit('del', $event)" @create-folder="emit('create-folder', $event)" @create-doc="emit('create-doc', $event)"
        @drop-node="(d: string, t: string) => emit('drop-node', d, t)"
      />
    </template>
  </div>
</template>

<style scoped>
/* 树节点行：对齐应用内 HttpFolderTreeNode 风格 */
.tn-row {
  display: flex; align-items: center; gap: 5px;
  padding: 5px 6px; border-radius: 6px; cursor: pointer;
  font-size: 12px; min-height: 26px; box-sizing: border-box;
  color: var(--color-text-secondary);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.tn-row:hover { background: var(--color-bg-hover); }
.tn-row.selected { background: var(--color-bg-tertiary); }
.tn-row.tn-folder { font-weight: 600; }
.tn-row.tn-doc.selected { color: var(--color-accent); }

.tn-caret { border: none; background: none; color: var(--color-text-disabled); cursor: pointer; width: 14px; height: 14px; display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; }
.tn-spacer { width: 14px; flex-shrink: 0; }
.tn-icon { flex-shrink: 0; color: var(--color-accent); }
.tn-doc .tn-icon { color: #d9920a; }
.tn-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tn-count { font-size: 10px; color: var(--color-text-muted); background: var(--color-bg-tertiary); border-radius: 8px; padding: 0 6px; flex-shrink: 0; }

/* hover 操作按钮：常在 hover 显示半透明，hover 变实 */
.tn-actions { display: inline-flex; align-items: center; gap: 1px; flex-shrink: 0; opacity: 0; transition: opacity var(--transition-fast); }
.tn-row:hover .tn-actions, .tn-row.selected .tn-actions { opacity: 1; }
.tn-act {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border: none; background: transparent;
  color: var(--color-text-muted); cursor: pointer; border-radius: 4px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.tn-act:hover { color: var(--color-text-primary); background: var(--color-bg-active); }
.tn-act.danger:hover { color: var(--color-danger); background: rgba(232, 76, 76, 0.12); }

.tn-input { flex: 1; min-width: 0; padding: 2px 6px; border: 1px solid var(--color-accent); border-radius: 4px; background: var(--color-bg-primary); color: var(--color-text-primary); font-size: 12px; outline: none; }
.tn-children { margin-left: 12px; }
</style>
