<script setup lang="ts">
import { ref, computed, onMounted, inject, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import ConfirmDialog from './ConfirmDialog.vue'
import NoteTreeNode from './NoteTreeNode.vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import {
  ListNotesTree, SearchNotesTree, CreateNoteFolder, CreateNoteDoc, RenameNoteNode,
  UpdateNoteDoc, MoveNoteNode, DeleteNoteNode, CopyText, SetNoteDocFormat,
} from '../../bindings/quickdock/services/appservice'
import {
  Trash2, Search, X, FolderPlus, FileText, Folder,
  Copy as CopyIcon, PanelLeft,
} from '@lucide/vue'
import type { ToastAPI, Snippet } from '../types'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 数据 ----
const nodes = ref<Snippet[]>([])
const loading = ref(true)
const expanded = ref<Set<string>>(new Set())
const searchQuery = ref('')
const activeTag = ref('')
const selectedDocId = ref('')
const editingId = ref('')
const editName = ref('')
const selectedFolderId = ref('')

const docContent = ref('')
const docTagsText = ref('')
let saveTimer: ReturnType<typeof setTimeout> | null = null

interface TreeNode extends Snippet { children: TreeNode[] }

function buildTree(list: Snippet[]): TreeNode[] {
  const map = new Map<string, TreeNode>()
  const roots: TreeNode[] = []
  for (const s of list) map.set(s.id, { ...s, children: [] })
  for (const n of map.values()) {
    if (n.parentId && map.has(n.parentId)) map.get(n.parentId)!.children.push(n)
    else roots.push(n)
  }
  const sortRec = (arr: TreeNode[]) => {
    arr.sort((a, b) => (a.isFolder === b.isFolder ? (a.sort || 0) - (b.sort || 0) : a.isFolder ? -1 : 1))
    arr.forEach(c => sortRec(c.children))
  }
  sortRec(roots)
  return roots
}

const tree = computed<TreeNode[]>(() => buildTree(nodes.value))

// 扁平化（供搜索/标签结果铺平展示）
function flatten(arr: TreeNode[]): TreeNode[] {
  const out: TreeNode[] = []
  for (const n of arr) { out.push(n); out.push(...flatten(n.children)) }
  return out
}
const flatResults = computed<TreeNode[]>(() => flatten(tree.value))

function parseTagsS(json: string): string[] {
  try { const a = JSON.parse(json || '[]'); return Array.isArray(a) ? a.filter((x: unknown) => typeof x === 'string') : [] } catch { return [] }
}

async function load() {
  loading.value = true
  try {
    const r = unwrap<Snippet[]>(await ListNotesTree())
    nodes.value = r || []
    if (selectedDocId.value && !nodes.value.find(n => n.id === selectedDocId.value)) {
      selectedDocId.value = ''; docContent.value = ''; docTagsText.value = ''
    }
  } catch (e) { toast.error(getErrorMessage(e)) } finally { loading.value = false }
}

async function doSearch() {
  const q = searchQuery.value.trim()
  if (!q) { await load(); return }
  try { nodes.value = unwrap<Snippet[]>(await SearchNotesTree(q)) || [] } catch (e) { toast.error(getErrorMessage(e)) }
}
watch(searchQuery, () => doSearch())
function clearSearch() { searchQuery.value = ''; load() }

// ---- 标签 ----
const allTags = computed<string[]>(() => {
  const set = new Set<string>()
  for (const n of nodes.value) for (const tg of parseTagsS(n.tags)) set.add(tg)
  return [...set].sort()
})

// ---- 左侧树 ----
function toggleFolder(id: string) {
  const s = new Set(expanded.value); s.has(id) ? s.delete(id) : s.add(id); expanded.value = s
}
function expandAllOpen() {}
function collapseAll() { expanded.value = new Set() }
function selectNodeIntoFolder(folderId: string) { selectedFolderId.value = folderId || '' }

// 新建（直接在目标文件夹下创建，无弹菜单）
async function newFolderAt(folderId: string) {
  try {
    const r = unwrap<Snippet>(await CreateNoteFolder(folderId, '新文件夹'))
    if (r) { if (folderId) expanded.value.add(folderId); expanded.value.add(r.id) }
    await load()
    // 创建后进入重命名，方便直接命名
    setTimeout(() => { if (r) startRename(r) }, 100)
  } catch (e) { toast.error(getErrorMessage(e)) }
}
async function newDocAt(folderId: string) {
  try {
    const r = unwrap<Snippet>(await CreateNoteDoc(folderId, '未命名笔记', '', 'markdown'))
    if (folderId) expanded.value.add(folderId)
    await load()
    if (r) { const found = nodes.value.find(x => x.id === r.id); if (found) onDoc(found) }
  } catch (e) { toast.error(getErrorMessage(e)) }
}

// 切换笔记渲染格式（markdown | text）
async function setFormat(fmt: string) {
  if (!selectedDocId.value || !selectedDoc.value || selectedDoc.value.format === fmt) return
  try {
    await SetNoteDocFormat(selectedDocId.value, fmt)
    selectedDoc.value.format = fmt
  } catch (e) { toast.error(getErrorMessage(e)) }
}

// 重命名
function startRename(n: Snippet) { editingId.value = n.id; editName.value = n.name || n.keyword || '' }
async function commitRename(id: string) {
  const name = editName.value.trim()
  if (!name) { editingId.value = ''; return }
  try { await RenameNoteNode(id, name); await load() } catch (e) { toast.error(getErrorMessage(e)) }
  editingId.value = ''
}

// 删除
const deletingNode = ref<Snippet | null>(null)
const showDel = ref(false)
function askDelete(n: Snippet) { deletingNode.value = n; showDel.value = true }
async function confirmDeleteNode() {
  if (!deletingNode.value) return
  try {
    await DeleteNoteNode(deletingNode.value.id)
    if (selectedDocId.value === deletingNode.value.id) { selectedDocId.value = ''; docContent.value = ''; docTagsText.value = '' }
    toast.success(t('deleted')); showDel.value = false; await load()
  } catch (e) { toast.error(getErrorMessage(e)) }
}

// 移动（拖拽）
async function handleDrop(dragId: string, targetId: string) {
  if (!dragId) return
  try { await MoveNoteNode(dragId, targetId || ''); await load() } catch (e) { toast.error(getErrorMessage(e)) }
}

// ---- 右侧文档编辑/预览 ----
const selectedDoc = computed<Snippet | null>(() => (selectedDocId.value ? nodes.value.find(n => n.id === selectedDocId.value) || null : null))
const previewHtml = computed(() => (docContent.value ? DOMPurify.sanitize(marked.parse(docContent.value, { async: false }) as string) : ''))

function onDoc(d: Snippet) {
  if (d.isFolder) return
  selectedDocId.value = d.id
  selectedFolderId.value = d.parentId || ''
  docContent.value = d.content || ''
  docTagsText.value = parseTagsS(d.tags).join(', ')
  // 展开祖先
  let cur = d.parentId
  const s = new Set(expanded.value)
  while (cur) {
    s.add(cur)
    const par = nodes.value.find(x => x.id === cur)
    cur = par?.parentId ?? ''
  }
  expanded.value = s
}
function onDocInput() {
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(saveDoc, 800)
}
async function saveDoc() {
  if (!selectedDocId.value) return
  const tags = '["' + docTagsText.value.split(',').map(x => x.trim()).filter(Boolean).join('","') + '"]'
  try { await UpdateNoteDoc(selectedDocId.value, docContent.value, tags); await load() } catch { /* 自动保存静默 */ }
}
async function copyDoc() {
  if (!docContent.value) return
  try { await CopyText(docContent.value); toast.success(t('copied')) } catch (e) { toast.error(getErrorMessage(e)) }
}
async function renameCurrent() {
  if (!selectedDoc.value) return
  try { await RenameNoteNode(selectedDoc.value.id, selectedDoc.value.name); await load() } catch (e) { toast.error(getErrorMessage(e)) }
}

onMounted(() => load())
</script>

<template>
  <div class="notes-page">
    <!-- 顶部 -->
    <div class="notes-header">
      <div class="notes-title">
        <h2 class="notes-h2">{{ t('notesTitle') }}</h2>
        <span class="notes-sub">{{ t('notesSubtitle') }}</span>
      </div>
      <div class="notes-search">
        <Search :size="14" class="ns-icon" />
        <input v-model="searchQuery" class="ns-input" :placeholder="t('notesSearchPh')" />
        <button v-if="searchQuery" class="ns-clear" @click="clearSearch"><X :size="13" /></button>
      </div>
      <div class="notes-tags">
        <button class="nt-chip" :class="{ active: activeTag === '' }" @click="activeTag = ''">{{ t('notesAllTags') }}</button>
        <button v-for="tg in allTags" :key="tg" class="nt-chip" :class="{ active: activeTag === tg }" @click="activeTag = activeTag === tg ? '' : tg">{{ tg }}</button>
      </div>
    </div>

    <div class="notes-body">
      <!-- 左：树 -->
      <div class="notes-tree">
        <div class="tree-toolbar">
          <button class="tt-btn" :title="t('notesNewFolder')" @click="newFolderAt('')"><FolderPlus :size="13" /></button>
          <button class="tt-btn" :title="t('notesNewDoc')" @click="newDocAt('')"><FileText :size="13" /></button>
          <span class="tt-sep"></span>
          <button class="tt-btn" :title="t('notesCollapseAll')" @click="collapseAll"><PanelLeft :size="13" /></button>
        </div>
        <div class="tree-wrap">
          <template v-if="!searchQuery && !activeTag">
            <div v-for="root in tree" :key="root.id">
              <NoteTreeNode
                :node="root" :depth="0"
                :expanded="expanded" :editing-id="editingId" :edit-name="editName"
                :selected-doc="selectedDocId" :selected-folder="selectedFolderId"
                @toggle="toggleFolder" @select="onDoc"
                @rename-start="startRename" @rename-commit="commitRename"
                @del="askDelete" @create-folder="newFolderAt" @create-doc="newDocAt"
                @drop-node="handleDrop"
              />
            </div>
            <div v-if="!tree.length && !loading" class="tree-empty">{{ t('notesEmpty') }}</div>
          </template>
          <!-- 搜索/标签结果：扁平展示 -->
          <template v-else>
            <div v-for="n in flatResults" :key="'res-'+n.id" class="sr-row" @click="n.isFolder ? toggleFolder(n.id) : onDoc(n)">
              <component :is="n.isFolder ? Folder : FileText" :size="13" class="tn-icon" />
              <span class="tn-name">{{ n.name || n.keyword }}</span>
            </div>
          </template>
        </div>
      </div>

      <!-- 右：文档编辑 -->
      <div class="notes-editor">
        <div v-if="!selectedDoc" class="ne-empty"><FileText :size="32" /><p>{{ t('notesSelectHint') }}</p></div>
        <template v-else-if="selectedDoc.isFolder">
          <div class="ne-empty ne-folder"><Folder :size="32" /><p>{{ t('notesIsFolder') }}</p></div>
        </template>
        <template v-else>
          <div class="ne-head">
            <input v-model="selectedDoc.name" class="ne-name" @change="renameCurrent" />
            <div class="ne-format">
              <button :class="{ active: selectedDoc.format !== 'text' }" @click="setFormat('markdown')">{{ t('notesFormatMd') }}</button>
              <button :class="{ active: selectedDoc.format === 'text' }" @click="setFormat('text')">{{ t('notesFormatText') }}</button>
            </div>
            <button class="ne-copy" :title="t('copy')" @click="copyDoc"><CopyIcon :size="13" /></button>
          </div>
          <div class="ne-tags">
            <span class="ne-tags-label">{{ t('notesTag') }}</span>
            <input v-model="docTagsText" class="ne-tags-input" :placeholder="t('notesTagsPh')" @change="saveDoc" />
          </div>
          <!-- markdown：分栏编辑+预览；纯文本：仅单编辑区 -->
          <div v-if="selectedDoc.format !== 'text'" class="ne-split">
            <textarea v-model="docContent" class="ne-input" spellcheck="false"
              :placeholder="t('notesMarkdownPh')" @input="onDocInput"></textarea>
            <div class="ne-preview markdown-body" v-html="previewHtml"></div>
          </div>
          <div v-else class="ne-split">
            <textarea v-model="docContent" class="ne-input ne-textonly" spellcheck="false"
              :placeholder="t('notesTextPh')" @input="onDocInput"></textarea>
          </div>
        </template>
      </div>
    </div>

    <ConfirmDialog :visible="showDel" :message="(deletingNode?.isFolder ? t('notesDelFolder') : t('notesDelDoc')) + '?'"
      @confirm="confirmDeleteNode" @cancel="showDel = false" />
  </div>
</template>

<style scoped>
.notes-page { height: 100%; display: flex; flex-direction: column; background: var(--color-bg-primary); }
.notes-header { display: flex; align-items: center; gap: 10px; padding: 10px 16px; border-bottom: 1px solid var(--color-border); }
.notes-title { display: flex; flex-direction: column; flex-shrink: 0; }
.notes-h2 { margin: 0; font-size: 15px; color: var(--color-text-primary); }
.notes-sub { font-size: 11px; color: var(--color-text-muted); }
.notes-search { position: relative; width: 260px; flex-shrink: 0; display: flex; align-items: center; }
.ns-icon { position: absolute; left: 8px; color: var(--color-text-disabled); }
.ns-input { width: 100%; padding: 6px 28px 6px 28px; border: 1px solid var(--color-border); border-radius: 8px; background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 13px; outline: none; font-family: inherit; }
.ns-input:focus { border-color: var(--color-accent); }
.ns-clear { position: absolute; right: 6px; border: none; background: none; color: var(--color-text-muted); cursor: pointer; }
.notes-tags { flex: 1; min-width: 0; display: flex; gap: 6px; flex-wrap: wrap; align-items: center; margin-left: 8px; }
.nt-chip { padding: 2px 6px; border: none; background: transparent; color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit; flex-shrink: 0; line-height: 1; transition: color var(--transition-fast), font-weight var(--transition-fast); }
.nt-chip:hover { color: var(--color-text-primary); }
.nt-chip.active { color: var(--color-accent); font-weight: 600; }

.notes-body { flex: 1; display: flex; min-height: 0; }
.notes-tree { width: 250px; flex-shrink: 0; border-right: 1px solid var(--color-border); display: flex; flex-direction: column; background: var(--color-bg-secondary); }
.tree-toolbar { display: flex; gap: 2px; padding: 6px 8px; border-bottom: 1px solid var(--color-border); }
.tt-btn { width: 26px; height: 26px; display: flex; align-items: center; justify-content: center; border: none; background: none; color: var(--color-text-muted); cursor: pointer; border-radius: 5px; }
.tt-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.tt-sep { width: 1px; background: var(--color-border); margin: 0 4px; }
.tree-wrap { flex: 1; overflow-y: auto; padding: 4px; }
.tree-empty { color: var(--color-text-disabled); font-size: 12px; padding: 20px; text-align: center; }
.sr-row { display: flex; align-items: center; gap: 6px; padding: 6px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.sr-row:hover { background: var(--color-bg-hover); }

.notes-editor { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.ne-empty { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; color: var(--color-text-disabled); gap: 6px; }
.ne-head { display: flex; align-items: center; gap: 6px; padding: 8px 12px; border-bottom: 1px solid var(--color-border); }
.ne-name { flex: 1; padding: 6px 10px; border: 1px solid var(--color-border); border-radius: 6px; background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 14px; font-weight: 600; outline: none; font-family: inherit; }
.ne-name:focus { border-color: var(--color-accent); }
.ne-format { display: flex; gap: 2px; flex-shrink: 0; }
.ne-format button { padding: 4px 10px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit; }
.ne-format button:first-child { border-radius: 6px 0 0 6px; }
.ne-format button:last-child { border-radius: 0 6px 6px 0; margin-left: -1px; }
.ne-format button.active { color: var(--color-accent); background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.ne-format button:hover:not(.active) { color: var(--color-text-primary); }
.ne-copy { border: none; background: none; color: var(--color-text-muted); cursor: pointer; padding: 6px; border-radius: 5px; }
.ne-copy:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.ne-tags { display: flex; align-items: center; gap: 6px; padding: 6px 12px; border-bottom: 1px solid var(--color-border); }
.ne-tags-label { font-size: 11px; color: var(--color-text-muted); flex-shrink: 0; }
.ne-tags-input { flex: 1; padding: 4px 8px; border: 1px solid var(--color-border); border-radius: 5px; background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 12px; outline: none; font-family: inherit; }
.ne-tags-input:focus { border-color: var(--color-accent); }
.ne-split { flex: 1; display: flex; min-height: 0; }
.ne-input { flex: 1; min-width: 0; resize: none; padding: 12px; border: none; border-right: 1px solid var(--color-border); background: var(--color-bg-secondary); color: var(--color-text-primary); font-size: 13px; font-family: 'Consolas','Monaco',monospace; line-height: 1.6; outline: none; box-sizing: border-box; }
.ne-input.ne-textonly { border-right: none; }
.ne-preview { flex: 1; min-width: 0; overflow: auto; padding: 12px 16px; background: var(--color-bg-primary); }

/* Markdown 渲染（复用 HttpClientPage 样式） */
.markdown-body { color: var(--color-text-primary); font-size: 13px; line-height: 1.7; word-break: break-word; }
.markdown-body :deep(h1), .markdown-body :deep(h2), .markdown-body :deep(h3), .markdown-body :deep(h4) { margin: 12px 0 8px; line-height: 1.3; }
.markdown-body :deep(h1) { font-size: 20px; border-bottom: 1px solid var(--color-border); padding-bottom: 6px; }
.markdown-body :deep(h2) { font-size: 17px; border-bottom: 1px solid var(--color-border); padding-bottom: 4px; }
.markdown-body :deep(h3) { font-size: 15px; }
.markdown-body :deep(p) { margin: 8px 0; }
.markdown-body :deep(a) { color: var(--color-accent); }
.markdown-body :deep(code) { background: var(--color-bg-tertiary); padding: 1px 5px; border-radius: 4px; font-family: 'Consolas','Monaco',monospace; font-size: 13px; }
.markdown-body :deep(pre) { background: var(--color-bg-tertiary); padding: 10px 12px; border-radius: 6px; overflow: auto; }
.markdown-body :deep(pre code) { background: none; padding: 0; }
.markdown-body :deep(blockquote) { border-left: 3px solid var(--color-border-focus); margin: 8px 0; padding: 2px 12px; color: var(--color-text-muted); }
.markdown-body :deep(ul), .markdown-body :deep(ol) { padding-left: 22px; margin: 8px 0; }
.markdown-body :deep(table) { border-collapse: collapse; margin: 8px 0; }
.markdown-body :deep(th), .markdown-body :deep(td) { border: 1px solid var(--color-border); padding: 4px 8px; }
.markdown-body :deep(img) { max-width: 100%; }
</style>
