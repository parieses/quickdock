<script setup lang="ts">
import { computed, ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Folder, FileText, ChevronRight, ChevronDown, Plus, FolderPlus, Pencil, Trash2 } from '@lucide/vue'
import type { Note } from '../types'

interface Props {
  node: any
  depth?: number
  expanded: Set<string>
  editingId: string
  editName: string
  selectedDoc: string
  selectedFolder: string
  selectMode?: boolean
  selected?: boolean
}
const props = withDefaults(defineProps<Props>(), { depth: 0, selectMode: false, selected: false })

const emit = defineEmits<{
  (e: 'toggle', id: string): void
  (e: 'select', node: any): void
  (e: 'rename-start', node: any): void
  (e: 'rename-commit', id: string): void
  (e: 'rename-input', value: string): void
  (e: 'rename-cancel'): void
  (e: 'del', node: any): void
  (e: 'create-folder', folderId: string): void
  (e: 'create-doc', folderId: string): void
  (e: 'drop-node', dragId: string, targetId: string): void
  /** 同级排序：把 dragId 插到 targetId 前面（before=true）或后面 */
  (e: 'reorder', dragId: string, targetId: string, before: boolean): void
  /** 多选：切换某节点选中状态 */
  (e: 'toggle-select', id: string): void
}>()

const { t } = useI18n()
const n = computed<any>(() => props.node)
const isFolder = computed(() => n.value.isFolder)
const isOpen = computed(() => props.expanded.has(n.value.id))
const hasKids = computed(() => n.value.children?.length > 0)

// 进入编辑态时自动聚焦并全选，否则焦点未进入输入框，点走不会触发 blur → 编辑态卡死去不掉
const inputRef = ref<HTMLInputElement | null>(null)
watch(
  () => props.editingId === props.node.id,
  (active) => { if (active) nextTick(() => { inputRef.value?.focus(); inputRef.value?.select() }) },
  { immediate: true },
)

/**
 * 拖拽落点：'' = 中间（放进这个文件夹），'before'/'after' = 上下边缘（同级排序）。
 *
 * 为什么要分区域：同一个拖拽手势既要能「移进文件夹」又要能「插到某一项前后」，
 * 唯一不冲突的切分方式就是按行的上下边缘。边缘区取 30%，中间 40% 留给
 * 「放进文件夹」——文件夹默认是展开的，中间区域误插进去很难复原。
 *
 * dragover 期间拿不到 dataTransfer.getData（浏览器为安全屏蔽），所以这里只算
 * 落点，是否同级由页面在 drop 时判定。
 */
const dropSide = ref<'' | 'before' | 'after'>('')

function sideOf(e: DragEvent): '' | 'before' | 'after' {
  const el = e.currentTarget as HTMLElement | null
  if (!el) return ''
  const r = el.getBoundingClientRect()
  const y = e.clientY - r.top
  if (y < r.height * 0.3) return 'before'
  if (y > r.height * 0.7) return 'after'
  return ''
}

function onDragOver(e: DragEvent) {
  e.preventDefault(); e.stopPropagation()
  dropSide.value = sideOf(e)
}

function onDragLeave() { dropSide.value = '' }

function onDrop(e: DragEvent) {
  e.preventDefault(); e.stopPropagation()
  const did = e.dataTransfer?.getData('text/plain')
  const side = dropSide.value
  dropSide.value = ''
  if (!did) return
  if (!side || did === n.value.id) {
    emit('drop-node', did, n.value.id)
    return
  }
  emit('reorder', did, n.value.id, side === 'before')
}

function onDragStart(e: DragEvent) {
  e.dataTransfer?.setData('text/plain', n.value.id)
}
</script>

<template>
  <div>
    <div
      :draggable="!selectMode"
      :style="'padding-left:' + (12 + depth * 16) + 'px'"
      :class="['tn-row', 'tn-' + (isFolder ? 'folder' : 'doc'), {
        selected: selectedDoc === node.id || selectedFolder === node.id,
        'sel-on': selected,
        'drop-before': dropSide === 'before',
        'drop-after': dropSide === 'after',
      }]"
      @click="selectMode ? emit('toggle-select', node.id) : (isFolder ? emit('toggle', node.id) : emit('select', node))"
      @dragstart="onDragStart"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
      <span v-if="selectMode" class="tn-check" :class="{ on: selected }" @click.stop="emit('toggle-select', node.id)"></span>
      <button v-else-if="isFolder" class="tn-caret" @click.stop="emit('toggle', node.id)">
        <ChevronRight v-if="!isOpen" :size="12" /><ChevronDown v-else :size="12" />
      </button>
      <span v-else class="tn-spacer"></span>
      <component :is="isFolder ? Folder : FileText" :size="13" class="tn-icon" />
      <template v-if="editingId === node.id">
        <input
          ref="inputRef"
          :value="editName"
          class="tn-input"
          @input="emit('rename-input', ($event.target as HTMLInputElement).value)"
          @keyup.enter="emit('rename-commit', node.id)"
          @keyup.esc="emit('rename-cancel')"
          @blur="emit('rename-commit', node.id)"
          @click.stop
        />
      </template>
      <template v-else>
        <span class="tn-name">{{ node.name || node.keyword }}</span>
      </template>
      <span v-if="isFolder && hasKids" class="tn-count">{{ node.children.length }}</span>
      <span v-if="!selectMode" class="tn-actions" @click.stop>
        <button v-if="isFolder" class="tn-act" :title="'+ ' + t('notesNewFolder')" @click="emit('create-folder', node.id)"><FolderPlus :size="12" /></button>
        <button v-if="isFolder" class="tn-act" :title="'+ ' + t('notesNewDoc')" @click="emit('create-doc', node.id)"><Plus :size="12" /></button>
        <button class="tn-act" :title="t('rename')" @click="emit('rename-start', node)"><Pencil :size="12" /></button>
        <button class="tn-act danger" :title="t('delete')" @click="emit('del', node)"><Trash2 :size="12" /></button>
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
        @rename-input="(v: string) => emit('rename-input', v)" @rename-cancel="emit('rename-cancel')"
        @del="emit('del', $event)" @create-folder="emit('create-folder', $event)" @create-doc="emit('create-doc', $event)"
        @drop-node="(d: string, t: string) => emit('drop-node', d, t)"
        @reorder="(d: string, t: string, b: boolean) => emit('reorder', d, t, b)"
      />
    </template>
  </div>
</template>

<style scoped>
/* 树节点行：对齐应用内 HttpFolderTreeNode 风格 */
.tn-row {
  display: flex; align-items: center; gap: 5px;
  padding: 5px 6px; border-radius: var(--radius-sm); cursor: pointer;
  font-size: 12px; min-height: 26px; box-sizing: border-box;
  color: var(--color-text-secondary);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.tn-row:hover { background: var(--color-bg-hover); }
.tn-row.selected { background: var(--color-bg-tertiary); }

/* 拖拽落点提示：上下边缘一条线，比整行高亮更清楚「会插到哪」 */
.tn-row.drop-before { box-shadow: inset 0 2px 0 var(--color-accent); }
.tn-row.drop-after { box-shadow: inset 0 -2px 0 var(--color-accent); }
.tn-row.tn-folder { font-weight: 600; }
.tn-row.tn-doc.selected { color: var(--color-accent); }

.tn-caret { border: none; background: none; color: var(--color-text-disabled); cursor: pointer; width: 14px; height: 14px; display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; }
.tn-spacer { width: 14px; flex-shrink: 0; }
/* 多选 checkbox：自绘，避免引入原生 checkbox 在暗色下的样式漂移 */
.tn-check {
  width: 14px; height: 14px; flex-shrink: 0; border-radius: var(--radius-xs);
  border: 1px solid var(--color-border-focus); background: var(--color-bg-primary);
  cursor: pointer; position: relative; transition: background-color var(--transition-fast), border-color var(--transition-fast);
}
.tn-check.on { background: var(--color-accent); border-color: var(--color-accent); }
.tn-check.on::after {
  content: ''; position: absolute; left: 4px; top: 1px; width: 4px; height: 8px;
  border: solid var(--color-on-accent, #fff); border-width: 0 2px 2px 0; transform: rotate(45deg);
}
.tn-row.sel-on { background: var(--color-accent-bg); color: var(--color-text-primary); }
.tn-row.sel-on:hover { background: var(--color-accent-bg); }
.tn-icon { flex-shrink: 0; color: var(--color-accent); }
.tn-doc .tn-icon { color: var(--color-warning); }
.tn-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tn-count { font-size: 10px; color: var(--color-text-muted); background: var(--color-bg-tertiary); border-radius: var(--radius-md); padding: 0 6px; flex-shrink: 0; }

/* hover 操作按钮：常在 hover 显示半透明，hover 变实 */
.tn-actions { display: inline-flex; align-items: center; gap: 1px; flex-shrink: 0; opacity: 0; transition: opacity var(--transition-fast); }
.tn-row:hover .tn-actions, .tn-row.selected .tn-actions { opacity: 1; }
.tn-act {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border: none; background: transparent;
  color: var(--color-text-muted); cursor: pointer; border-radius: var(--radius-xs);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.tn-act:hover { color: var(--color-text-primary); background: var(--color-bg-active); }
.tn-act.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }

.tn-input { flex: 1; min-width: 0; padding: 2px 6px; border: 1px solid var(--color-accent); border-radius: var(--radius-xs); background: var(--color-bg-primary); color: var(--color-text-primary); font-size: 12px; outline: none; }
.tn-children { margin-left: 12px; }
</style>
