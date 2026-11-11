<script setup lang="ts">
defineOptions({ name: KEEP_ALIVE.notes })
// name 的唯一来源：pages/keepAlive.ts（与 KeepAlive 白名单共用同一常量）
import { KEEP_ALIVE } from '../pages/keepAlive'
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onBeforeUnmount, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { pendingOpenNoteId, pendingOpenTodoId, navigate, navigateTo } from '../composables/bridge'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { pinyinMatch, matchesTextOrPinyin } from '../utils/pinyin'
import { debounce } from '../utils/debounce'
import ConfirmDialog from './ConfirmDialog.vue'
import NoteTreeNode from './NoteTreeNode.vue'
import NoteAiPanel from './NoteAiPanel.vue'
import EntityLinkPicker from './EntityLinkPicker.vue'
import NoteGraphPanel from './NoteGraphPanel.vue'
import NoteOutlinePanel from './NoteOutlinePanel.vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { installMathOnce } from '../utils/mathMarkdown'
import { installWikiOnce } from '../utils/wikiMarkdown'
import { createCodeEditor, type CodeEditorHandle } from '../utils/aiCodeEditor'
import { downloadText, safeFileName } from '../utils/download'
import {
  ListNotesTree,
  SearchNotesTree,
  CreateNoteFolder,
  CreateNoteDoc,
  RenameNoteNode,
  UpdateNoteDoc,
  MoveNoteNode,
  DeleteNoteNode,
  SetNoteDocFormat,
  SetNoteSort,
} from '../../bindings/quickdock/services/note/noteservice'
import { CopyText } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { ResolveWikiTarget, ListLinkTargets } from '../../bindings/quickdock/services/appservice'
import {
  Trash2, Search, X, FolderPlus, FileText, Folder,
  Copy as CopyIcon, PanelLeft, PanelLeftClose, ChevronDown, Sparkles, FileDown, List,
  FileSearch, Square, CheckSquare, Clock, Code2, Columns2, Eye, Link2, Network, PanelRight,
} from '@lucide/vue'
import type { ToastAPI, Note, WikiTarget, LinkTargetGroups, EntityType } from '../types'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 数据 ----
const nodes = ref<Note[]>([])
const loading = ref(true)
const expanded = ref<Set<string>>(new Set())
const searchQuery = ref('')
const activeTag = ref('')
// 单选只认「当前打开的文档」一个 id。旧实现另设 selectedFolderId = 父文件夹 id，
// 树行再用 OR 判定，结果父行和文档行同时高亮、换文档时高亮在文件夹之间跳，
// 看起来就是选中态自己漂走了。祖先路径由 expanded 展开即可，不需要第二个选中态。
const selectedDocId = ref('')
const editingId = ref('')
const editName = ref('')

// ---- 多选 / 批量操作 ----
// 进入多选模式后，树节点改为「点击切换选中」而非「打开」，行尾操作按钮隐藏，
// 拖拽禁用；底部出现批量栏（打标签 / 移动到 / 删除）。退出多选清空选择。
const selectMode = ref(false)
const selectedIds = ref<Set<string>>(new Set())
const selectedCount = computed(() => selectedIds.value.size)
function toggleSelect(id: string) {
  const s = new Set(selectedIds.value)
  if (s.has(id)) s.delete(id); else s.add(id)
  selectedIds.value = s
}
function clearSelection() { selectedIds.value = new Set() }
function toggleSelectMode() {
  selectMode.value = !selectMode.value
  if (!selectMode.value) clearSelection()
}
// 当前可见文档（非搜索时取整棵树、搜索/标签时取扁平结果）
const visibleDocs = computed(() => {
  const pool = (searchQuery.value || activeTag.value) ? flatResults.value : flatten(tree.value)
  return pool.filter(n => !n.isFolder)
})
const allSelected = computed(() =>
  visibleDocs.value.length > 0 && visibleDocs.value.every(n => selectedIds.value.has(n.id)),
)
function toggleSelectAll() {
  if (allSelected.value) clearSelection()
  else selectedIds.value = new Set(visibleDocs.value.map(n => n.id))
}
// 批量移动目标：所有文件夹
const folderOptions = computed(() =>
  flatten(tree.value).filter(n => n.isFolder).map(n => ({ id: n.id, name: n.name || n.keyword || t('notesFolder') })),
)

// ---- 最近打开（localStorage 存最近 5 篇文档 {id, 打开时间戳}）----
const RECENT_KEY = 'quickdock.notes.recent'
type RecentItem = { id: string; t: number }
const recentItems = ref<RecentItem[]>([])
function loadRecent() {
  try {
    const a = JSON.parse(localStorage.getItem(RECENT_KEY) || '[]')
    if (!Array.isArray(a)) return
    recentItems.value = a
      // 兼容旧版纯字符串数组：补 t:0（未知时间）
      .map((x: any) => typeof x === 'string' ? { id: x, t: 0 } : { id: String(x.id), t: Number(x.t) || 0 })
      .filter((x: RecentItem) => x.id)
      .slice(0, 5)
  } catch { /* 损坏就当没有 */ }
}
function pushRecent(id: string) {
  const next = [{ id, t: Date.now() }, ...recentItems.value.filter(x => x.id !== id)].slice(0, 5)
  recentItems.value = next
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(next)) } catch { /* 配额/隐私模式忽略 */ }
}
// 相对时间：让「最近」看起来像真正的 recently-opened，而不是又一个文件列表
function relTime(ts: number): string {
  if (!ts) return ''
  const d = Date.now() - ts
  const m = 60000, h = 3600000, day = 86400000, w = 7 * day
  if (d < m) return t('notesRecentJust')
  if (d < h) return Math.floor(d / m) + t('notesRecentMin')
  if (d < day) return Math.floor(d / h) + t('notesRecentHour')
  if (d < w) return Math.floor(d / day) + t('notesRecentDay')
  return Math.floor(d / w) + t('notesRecentWeek')
}
// 只展示仍然存在且非文件夹的文档；搜索/标签视图下不显示（那会盖住结果列表）。
// 顺带带出父文件夹名 + 相对时间，让「最近」列表和下面的树在信息上明显分层。
const recentDisplay = computed(() => {
  if (searchQuery.value || activeTag.value) return []
  const map = new Map(nodes.value.map(n => [n.id, n]))
  return recentItems.value
    .map(it => {
      const n = map.get(it.id)
      if (!n || n.isFolder) return null
      const par = n.parentId ? map.get(n.parentId) : null
      return {
        ...n,
        parentName: (par?.name || par?.keyword || '') as string,
        rel: relTime(it.t),
      }
    })
    .filter((x): x is Note & { parentName: string; rel: string } => !!x)
})

// ---- 左侧树折叠 / 拖拽调宽 ----
const TREE_W_KEY = 'quickdock.notes.treeWidth'
const TREE_C_KEY = 'quickdock.notes.treeCollapsed'
const treeWidth = ref(250)
const treeCollapsed = ref(false)
function loadTreePrefs() {
  try {
    const w = parseInt(localStorage.getItem(TREE_W_KEY) || '', 10)
    if (w >= 160 && w <= 480) treeWidth.value = w
  } catch { /* ignore */ }
  treeCollapsed.value = localStorage.getItem(TREE_C_KEY) === '1'
}
function persistTreeWidth() { try { localStorage.setItem(TREE_W_KEY, String(treeWidth.value)) } catch { /* ignore */ } }
function persistTreeCollapsed() { try { localStorage.setItem(TREE_C_KEY, treeCollapsed.value ? '1' : '0') } catch { /* ignore */ } }
function toggleTreeCollapsed() { treeCollapsed.value = !treeCollapsed.value; persistTreeCollapsed() }

// 分割线拖拽：按下后跟随鼠标改宽度，按 .notes-body 左缘换算（树贴着 body 左边）。
let resizing = false
let bodyLeft = 0
function startResize(e: MouseEvent) {
  resizing = true
  const tree = (e.currentTarget as HTMLElement).parentElement
  bodyLeft = tree?.getBoundingClientRect().left ?? 0
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
  window.addEventListener('mousemove', onResizeMove)
  window.addEventListener('mouseup', stopResize)
  e.preventDefault()
}
function onResizeMove(e: MouseEvent) {
  if (!resizing) return
  treeWidth.value = Math.min(480, Math.max(160, Math.round(e.clientX - bodyLeft)))
}
function stopResize() {
  if (!resizing) return
  resizing = false
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
  window.removeEventListener('mousemove', onResizeMove)
  window.removeEventListener('mouseup', stopResize)
  persistTreeWidth()
}

// ---- 批量操作 ----
const batchTagText = ref('')
const moveTarget = ref('')
const showBatchDel = ref(false)
function askBatchDelete() { if (selectedCount.value) showBatchDel.value = true }
async function confirmBatchDelete() {
  const ids = [...selectedIds.value]
  if (!ids.length) return
  try {
    for (const id of ids) unwrap(await DeleteNoteNode(id))
    if (ids.includes(selectedDocId.value)) { selectedDocId.value = ''; docContent.value = ''; docTagsText.value = '' }
    toast.success(t('deleted'))
  } catch (e) { toast.error(getErrorMessage(e)) } finally {
    showBatchDel.value = false; clearSelection(); await load()
  }
}
async function batchMove() {
  const target = moveTarget.value
  if (!target) return
  const ids = [...selectedIds.value]
  try {
    for (const id of ids) unwrap(await MoveNoteNode(id, target))
    toast.success(t('notesBatchMoved', { count: ids.length }))
  } catch (e) { toast.error(getErrorMessage(e)) } finally {
    clearSelection(); moveTarget.value = ''; await load()
  }
}
async function batchTag() {
  const tag = batchTagText.value.trim()
  if (!tag) return
  await flushPendingSave() // 先把当前文档落盘，避免覆盖未保存正文
  const ids = [...selectedIds.value]
  try {
    for (const id of ids) {
      const node = nodes.value.find(n => n.id === id)
      if (!node || node.isFolder) continue
      // 当前打开的文档用编辑器实时内容，其余用库里内容，合并标签后回写（保留正文）。
      const content = id === selectedDocId.value ? docContent.value : (node.content || '')
      const cur = parseTagsS(node.tags)
      if (!cur.includes(tag)) cur.push(tag)
      const tagsJSON = JSON.stringify(cur)
      unwrap(await UpdateNoteDoc(id, content, tagsJSON))
      applyLocalTags(id, tagsJSON)
    }
    toast.success(t('notesBatchTagDone', { count: ids.length }))
  } catch (e) { toast.error(getErrorMessage(e)) } finally {
    clearSelection(); batchTagText.value = ''; await load()
  }
}

const docContent = ref('')
const docTagsText = ref('')

/** 快捷笔记（浮窗用）在 notes 表里的固定 keyword，见 internal/db/note_tree.go。 */
const QUICK_NOTE_KEYWORD = '__quicknote__'
let saveTimer: ReturnType<typeof setTimeout> | null = null

interface TreeNode extends Note { children: TreeNode[] }

function buildTree(list: Note[]): TreeNode[] {
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
// 标签筛选结果：真正按 tags 过滤。
// 之前 activeTag 只把左栏从"树"切成"扁平列表"，flatResults 却是 flatten 全量 ——
// 点某个标签后看到的是所有节点铺平，等于这个筛选器完全没生效。
const flatResults = computed<TreeNode[]>(() => {
  const all = flatten(tree.value)
  const tag = activeTag.value
  return tag ? all.filter(n => parseTagsS(n.tags).includes(tag)) : all
})

// 全量笔记树（load 时刷新）。搜索时后端只回子串命中的子集，拼音匹配需要完整树做前端扫描。
const allNodes = ref<Note[]>([])

function parseTagsS(json: string): string[] {
  try { const a = JSON.parse(json || '[]'); return Array.isArray(a) ? a.filter((x: unknown) => typeof x === 'string') : [] } catch { return [] }
}

async function load() {
  loading.value = true
  try {
    const r = unwrap<Note[]>(await ListNotesTree())
    // 快捷笔记与树节点共用一张表：它的 keyword 固定，name 会回退成 keyword，
    // 于是会以一个叫 __quicknote__ 的节点挂在树里。它不是树的一部分，必须排除。
    const list = (r || []).filter(n => n.keyword !== QUICK_NOTE_KEYWORD)
    nodes.value = list
    allNodes.value = list
    if (selectedDocId.value && !nodes.value.find(n => n.id === selectedDocId.value)) {
      selectedDocId.value = ''; docContent.value = ''; docTagsText.value = ''
    }
    // 命令面板深链：打开指定笔记（pendingOpenNoteId 由命令面板写入）
    if (pendingOpenNoteId.value) {
      const n = nodes.value.find(x => x.id === pendingOpenNoteId.value)
      if (n) onDoc(n)
      pendingOpenNoteId.value = ''
    }
  } catch (e) { toast.error(getErrorMessage(e)) } finally { loading.value = false }
}

async function doSearch() {
  const q = searchQuery.value.trim()
  if (!q) { await load(); return }
  try {
    // 后端按 name/content/tags 子串匹配；再叠加前端拼音匹配 name/keyword，
    // 二者取并集（按 id 去重）。拼音逻辑统一走 utils/pinyin，与命令面板/工作空间一致。
    const sub = unwrap<Note[]>(await SearchNotesTree(q)) || []
    const py = allNodes.value.filter(n => pinyinMatch(n.name || n.keyword || '', q))
    const byId = new Map<string, Note>()
    for (const n of sub) byId.set(n.id, n)
    for (const n of py) if (!byId.has(n.id)) byId.set(n.id, n)
    nodes.value = [...byId.values()]
  } catch (e) { toast.error(getErrorMessage(e)) }
}
// 输入防抖：每敲一个字符都会打一次后端 LIKE + 一遍全量拼音扫描，
// 中文输入法逐字上屏时这个代价非常明显。
const runSearch = debounce(() => { void doSearch() }, 250)
watch(searchQuery, () => runSearch())
// 只清状态，重载交给上面的 watch：原先这里再加一次 load()，
// 清空搜索会同时触发 doSearch(空) 与 load()，白白多拉一遍全表。
function clearSearch() { searchQuery.value = '' }

// ---- 标签 ----
// 基于 allNodes（全量树）而不是 nodes：搜索时 nodes 被换成命中子集，
// 用它聚合会让标签栏跟着塌缩成一个标签，用户就没法在搜索结果里换标签了。
// 也不需要额外的「列出全部标签」后端接口 —— ListNotesTree 本来就返回全部 tags。
const allTags = computed<string[]>(() => {
  const set = new Set<string>()
  for (const n of allNodes.value) for (const tg of parseTagsS(n.tags)) set.add(tg)
  return [...set].sort()
})

// ---- 左侧树 ----
function toggleFolder(id: string) {
  const s = new Set(expanded.value); s.has(id) ? s.delete(id) : s.add(id); expanded.value = s
}
// 展开所有含子项的文件夹。之前是个空函数（也没有按钮），工具栏只剩"全部折叠"，
// 大目录里想快速扫一遍只能一层层点。
function expandAllOpen() {
  const next = new Set<string>()
  const walk = (arr: TreeNode[]) => {
    for (const n of arr) {
      if (n.isFolder) { next.add(n.id); walk(n.children) }
    }
  }
  walk(tree.value)
  expanded.value = next
}
function collapseAll() { expanded.value = new Set() }

// 新建（直接在目标文件夹下创建，无弹菜单）
async function newFolderAt(folderId: string) {
  try {
    const r = unwrap<Note>(await CreateNoteFolder(folderId, '新文件夹'))
    if (r) { if (folderId) expanded.value.add(folderId); expanded.value.add(r.id) }
    await load()
    // 创建后进入重命名，方便直接命名（load 完成后再 startRename，确保节点已在树中，自动聚焦可用）
    if (r) startRename(r)
  } catch (e) { toast.error(getErrorMessage(e)) }
}
async function newDocAt(folderId: string) {
  try {
    const r = unwrap<Note>(await CreateNoteDoc(folderId, '未命名笔记', '', 'markdown'))
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
function startRename(n: Note) { editingId.value = n.id; editName.value = n.name || n.keyword || '' }
function onRenameInput(v: string) { editName.value = v }
function cancelRename() { editingId.value = ''; editName.value = '' }
async function commitRename(id: string) {
  // 防重复提交（Enter 与 blur 会先后触发）；只处理当前正在编辑的节点
  if (editingId.value === '' || editingId.value !== id) return
  const name = editName.value.trim()
  editingId.value = ''
  editName.value = ''
  if (!name) return
  try { unwrap(await RenameNoteNode(id, name)); await load() } catch (e) { toast.error(getErrorMessage(e)) }
}

// 删除
const deletingNode = ref<Note | null>(null)
const showDel = ref(false)
function askDelete(n: Note) { deletingNode.value = n; showDel.value = true }
async function confirmDeleteNode() {
  if (!deletingNode.value) return
  try {
    unwrap(await DeleteNoteNode(deletingNode.value.id))
    if (selectedDocId.value === deletingNode.value.id) { selectedDocId.value = ''; docContent.value = ''; docTagsText.value = '' }
    toast.success(t('deleted')); showDel.value = false; await load()
  } catch (e) { toast.error(getErrorMessage(e)) }
}

// 移动（拖拽）
async function handleDrop(dragId: string, targetId: string) {
  if (!dragId) return
  try { unwrap(await MoveNoteNode(dragId, targetId || '')); await load() } catch (e) { toast.error(getErrorMessage(e)) }
}

/** 某个父目录下的同级节点，按**与树完全相同的规则**排序。
 *
 * 口径必须和 buildTree 的 sortRec 一致（文件夹在前 → sort → 名字），
 * 否则「插到它前面」算出来的落点和用户在屏幕上看到的顺序对不上，
 * 拖完会发现位置偏了一格。 */
function siblingsOf(parentId: string): Note[] {
  return nodes.value
    .filter(n => (n.parentId || '') === parentId)
    .sort((a, b) =>
      a.isFolder === b.isFolder
        ? (a.sort || 0) - (b.sort || 0) || (a.name || a.keyword || '').localeCompare(b.name || b.keyword || '')
        : a.isFolder ? -1 : 1,
    )
}

/**
 * 同级拖拽排序。
 *
 * 跨级落点不在这里处理（语义是「移动」，走 handleDrop）；这里只处理同父节点
 * 之间的上下插入，因此不需要 MoveNoteNode，也不会改 parent_id。
 *
 * 排序值统一重排成 10、20、30…：相邻两个节点原本可能只差 1，反复在两格之间
 * 拖几次就会出现「中间插不进去」（需要浮点 sort）的情况。整批重排最省事，
 * 且只对真正变了的项发请求，一次拖拽通常只涉及两三行。
 */
async function reorderNode(dragId: string, targetId: string, before: boolean) {
  if (!dragId || dragId === targetId) return
  const drag = nodes.value.find(n => n.id === dragId)
  const target = nodes.value.find(n => n.id === targetId)
  if (!drag || !target) return
  if ((drag.parentId || '') !== (target.parentId || '')) return
  const siblings = siblingsOf(drag.parentId || '')
  const from = siblings.findIndex(n => n.id === dragId)
  const to = siblings.findIndex(n => n.id === targetId)
  if (from < 0 || to < 0) return
  const next = [...siblings]
  const [moved] = next.splice(from, 1)
  // 先移除再插入，目标索引要按移除位置修正，否则「往上拖一格」会变成原地不动。
  let at = to + (before ? 0 : 1)
  if (from < at) at--
  next.splice(at, 0, moved)
  try {
    for (let i = 0; i < next.length; i++) {
      const want = (i + 1) * 10
      if ((next[i].sort || 0) === want) continue
      await SetNoteSort(next[i].id, want)
      next[i].sort = want
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
    // 本地状态可能已经部分更新，重拉一次回到库里的真实顺序。
    await load()
  }
}

// ---- 右侧文档编辑/预览 ----
const selectedDoc = computed<Note | null>(() => (selectedDocId.value ? nodes.value.find(n => n.id === selectedDocId.value) || null : null))
// 笔记预览也认 LaTeX：AI 回答里「存为笔记」保存的就是带 `\(...\)` / `\[...\]` 的原文，
// 不接这一步，笔记里就会重现「公式当正文」的样子。
// 用 installMathOnce / installWikiOnce 而不是直接 marked.use —— 本文件每次进入页面
// 都会重跑一遍 setup，两个包装各自用模块级标志保证全局单例只装一次。
// 顺序无所谓：两者都是往同一个 marked 实例的 extensions 里合并，互不覆盖。
installMathOnce(marked)
installWikiOnce(marked)

// ---- Markdown 视图模式：源码 / 分屏 / 预览 ----
// 仅对 markdown 生效（纯文本没有预览，永远只有源码一栏）。
// 默认「分屏」= 源码与预览都有，符合「md 默认两者都显示」的预期；
// 偏好存 localStorage，切文档 / 重启都不丢。
const NOTE_VIEW_KEY = 'quickdock.notes.viewMode'
type NoteView = 'split' | 'source' | 'preview'
const noteView = ref<NoteView>('split')
function loadNoteView() {
  try {
    const v = localStorage.getItem(NOTE_VIEW_KEY)
    if (v === 'split' || v === 'source' || v === 'preview') noteView.value = v
  } catch { /* 隐私模式/配额忽略 */ }
}
function setNoteView(v: NoteView) {
  noteView.value = v
  try { localStorage.setItem(NOTE_VIEW_KEY, v) } catch { /* 同上 */ }
}

const previewHtml = computed(() => (docContent.value ? DOMPurify.sanitize(marked.parse(docContent.value, { async: false }) as string) : ''))

/** 三类目标统一的深链映射；复用 bridge 已有载荷，不另造导航机制。 */
function jumpToEntity(type: string, id: string): boolean {
  if (!id) return false
  if (type === 'note') { pendingOpenNoteId.value = id; navigate('notes'); return true }
  if (type === 'todo') { pendingOpenTodoId.value = id; navigate('todo'); return true }
  if (type === 'item') { navigateTo('workspace', 'item:' + id); return true }
  return false
}

/**
 * 预览里的 wiki 链接点击。v-html 生成的 DOM 绑不了 Vue 事件，只能在容器上
 * 做事件委托（与 AIPage.vue 代码块按钮同一套路）。data-wiki 存的是 [[ ]] 内的
 * 完整原文（含 note:/todo:/item: 前缀），正好是 ResolveWikiTarget 的入参。
 */
async function onPreviewClick(e: MouseEvent) {
  const a = (e.target as HTMLElement | null)?.closest?.('a.qd-wiki') as HTMLElement | null
  if (!a) return
  const label = a.dataset.wiki || ''
  if (!label) return
  try {
    const target = unwrap<WikiTarget | null>(await ResolveWikiTarget(label))
    if (!target || !jumpToEntity(target.type, target.id)) {
      toast.error(t('wikiTargetNotFound'))
    }
  } catch (err) {
    toast.error(getErrorMessage(err))
  }
}

// 标签以逗号分隔输入，必须序列化成合法 JSON 数组。
//
// 早先版本手工拼 '["' + ... + '"]'：标签里一旦含引号或反斜杠就产出非法 JSON，
// 后端照样落库，之后 parseTagsS 解析失败又静默返回 []——用户写的标签全丢，
// 而且整个过程没有任何提示。交给 JSON.stringify 处理转义即可。
function buildTagsJSON(text: string): string {
  return JSON.stringify(text.split(',').map(x => x.trim()).filter(Boolean))
}

// 落盘当前文档（切文档/离开前调用）。先同步快照再 await，避免调用后被切换污染把内容存错文档。
//
// 自动保存此处刻意不弹 toast：它在 800ms 防抖里高频触发，失败就弹会持续打断
// 用户写作。但也不该彻底无痕（原写法是空 catch），故记为 error 日志留证。
async function flushPendingSave() {
  if (saveTimer) { clearTimeout(saveTimer); saveTimer = null }
  const id = selectedDocId.value
  if (!id) return
  const content = docContent.value
  try {
    const tagsJSON = buildTagsJSON(docTagsText.value)
    unwrap(await UpdateNoteDoc(id, content, tagsJSON))
    applyLocalTags(id, tagsJSON)
  } catch (e) {
    logErr('NoteManager:flushPendingSave', e)
  }
}

/**
 * 保存后只在本地同步 tags，不重拉整棵树。
 *
 * 自动保存每 800ms 就可能触发一次，而 ListNotesTree 返回的是整库节点：
 * 大库下每次保存都会重排/重渲染整棵树（滚动位置跳、输入卡顿），而实际上
 * 只有 tags 变了——内容不参与树渲染。
 */
function applyLocalTags(id: string, tagsJSON: string) {
  for (const list of [nodes.value, allNodes.value]) {
    for (const n of list) if (n.id === id) n.tags = tagsJSON
  }
}

function onDoc(d: Note) {
  if (d.isFolder) return
  void flushPendingSave() // 切文档前先落盘当前文档，避免防抖窗口内切换丢失内容
  aiOpen.value = false // 切走文档时关掉 AI 覆盖层：它贴在旧文档上，留着会写错地方
  savedCaret.value = -1
  selectedDocId.value = d.id
  docContent.value = d.content || ''
  pushRecent(d.id)
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

/**
 * 字数统计。
 *
 * 中英混排按「中文字数 + 英文单词数」算，这是中文写作工具的统一口径
 * （Word / 石墨 / 飞书都一样）：一个汉字算一个词，一串英文算一个词。
 * 字符数另算，用码点而不是 UTF-16 长度——否则 emoji 和生僻字会被算成 2 个。
 */
const docStats = computed(() => {
  const s = docContent.value || ''
  const chars = [...s].length
  const cjk = (s.match(/[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]/g) || []).length
  const en = (s.match(/[A-Za-z0-9_'-]+/g) || []).length
  const lines = s ? s.split('\n').length : 0
  return { chars, words: cjk + en, lines }
})

/** 导出当前文档为 .md（纯文本导出，导出的是原文而不是渲染结果）。 */
function exportDoc() {
  const doc = selectedDoc.value
  if (!doc) return
  const base = safeFileName(doc.name || doc.keyword || '', 'note')
  downloadText(`${base}.md`, docContent.value, 'text/markdown')
  toast.success(t('notesExported', { name: `${base}.md` }))
}

// 目录：从 markdown 源里抽 #/##/### 三级标题，纯前端、不依赖渲染结果。
// 长文档没有目录就得靠肉眼翻，对照着改结构时尤其难受。
const tocItems = computed(() => {
  if (selectedDoc.value?.format === 'text') return []
  const out: { level: number; text: string }[] = []
  for (const line of (docContent.value || '').split('\n')) {
    const m = /^(#{1,3})\s+(.+?)\s*#*\s*$/.exec(line)
    if (m) out.push({ level: m[1].length, text: m[2].trim() })
  }
  return out
})
const tocOpen = ref(false)

// ---- 关系图谱 / 块级大纲 ----
// 图谱是全局只读汇总视图，按约定挂回笔记页做 tab，不占侧栏导航；
// 大纲是编辑区右侧的第三个 flex 栏，与源码/预览并排，不遮挡内容。
const graphOpen = ref(false)
const outlineOpen = ref(false)
interface OutlineHeading { level: number; text: string; line: number }
// 比 tocItems 多一个 1-based 行号：大纲要能把编辑器滚到该行并折叠该区段，
// 光有标题文本做不到（同名标题会跳错）。
const outlineHeadings = computed<OutlineHeading[]>(() => {
  if (selectedDoc.value?.format === 'text') return []
  const out: OutlineHeading[] = []
  const lines = (docContent.value || '').split('\n')
  for (let i = 0; i < lines.length; i++) {
    const m = /^(#{1,3})\s+(.+?)\s*#*\s*$/.exec(lines[i])
    if (m) out.push({ level: m[1].length, text: m[2].trim(), line: i + 1 })
  }
  return out
})
function onOutlineJump(line: number) {
  docEditor?.revealLine(line)
  const h = outlineHeadings.value.find(x => x.line === line)
  if (h && previewRef.value) gotoHeading(h.text)
}
function onOutlineFold(line: number) { docEditor?.foldAt(line) }
function onOutlineFoldAll() { docEditor?.foldAll() }
function onOutlineUnfoldAll() { docEditor?.unfoldAll() }
function onGraphOpenTarget(type: string, id: string, _label: string) {
  // 悬空目标（后端用 ~hash 伪 id）前端回传空 id，静默忽略，别跳到不存在的条目。
  if (!id) return
  graphOpen.value = false
  if (type === 'note') {
    const n = nodes.value.find(x => x.id === id)
    if (n) void onDoc(n)
  } else if (type === 'todo') {
    pendingOpenTodoId.value = id
    navigate('todo')
  } else {
    navigateTo('workspace', id)
  }
}
function gotoHeading(text: string) {
  const preview = previewRef.value
  if (!preview) return
  // 渲染后的标题文本与源一致，按文本定位到 DOM 节点再滚到它。
  const heads = Array.from(preview.querySelectorAll('h1,h2,h3')) as HTMLElement[]
  const el = heads.find(h => h.textContent?.trim() === text)
  if (el) {
    preview.scrollTo({ top: el.offsetTop - 8, behavior: 'smooth' })
    tocOpen.value = false
  }
}

/** 搜索高亮切片：返回「命中段 / 普通段」交替的数组，模板里用 span 渲染。 */
function highlightParts(text: string, q: string): { s: string; hit: boolean }[] {
  const src = String(text || '')
  const needle = q.trim()
  if (!needle) return [{ s: src, hit: false }]
  // 不区分大小写的子串切分；正则元字符必须转义，否则搜 "a.b" 会被当通配符。
  const re = new RegExp(needle.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi')
  const out: { s: string; hit: boolean }[] = []
  let last = 0
  for (const m of src.matchAll(re)) {
    const at = m.index ?? 0
    if (at > last) out.push({ s: src.slice(last, at), hit: false })
    out.push({ s: m[0], hit: true })
    last = at + m[0].length
  }
  if (last < src.length) out.push({ s: src.slice(last), hit: false })
  return out.length ? out : [{ s: src, hit: false }]
}

/** 高亮切片缓存：只在 flatResults / searchQuery 变化时对每行重算一次。
 *  原来是模板 v-for 里直接调 highlightParts(...)，每次渲染全列表重跑正则。 */
const rowHighlightCache = computed(() => {
  const m = new Map<string, { s: string; hit: boolean }[]>()
  const q = searchQuery.value
  for (const n of flatResults.value) {
    const text = n.name || n.keyword || ''
    if (!m.has(text)) m.set(text, highlightParts(text, q))
  }
  return m
})

function rowHighlight(n: { name?: string; keyword?: string }) {
  const text = n.name || n.keyword || ''
  return rowHighlightCache.value.get(text) ?? highlightParts(text, searchQuery.value)
}

// 预览区与编辑区的滚动联动。
//
// 两栏独立滚动时，左边改到第 40 行、右边还停在第 3 行，对照修改完全没法用。
// 按比例同步而不是逐行量化：markdown 渲染后行高不等（标题/代码块/图片），
// 换算成行号必然错位，按滚动百分比对齐至少始终「看到大致同一段」。
let syncing = false
function syncScroll(from: 'input' | 'preview') {
  // 防止 A→B→A 的回环：由对方事件引起的设置不再反向触发。
  if (syncing) return
  // 输入侧的滚动容器是 CodeMirror 的 .cm-scroller（不再是 textarea）
  const input = docEditor?.scrollEl() ?? null
  const preview = previewRef.value
  if (!input || !preview) return
  const src = from === 'input' ? input : preview
  const dst = from === 'input' ? preview : input
  const srcMax = src.scrollHeight - src.clientHeight
  const dstMax = dst.scrollHeight - dst.clientHeight
  if (srcMax <= 0 || dstMax <= 0) return
  syncing = true
  dst.scrollTop = (src.scrollTop / srcMax) * dstMax
  // 下一帧再解锁：scrollTop 赋值触发的 scroll 事件是异步派发的。
  requestAnimationFrame(() => { syncing = false })
}
async function saveDoc() {
  if (!selectedDocId.value) return
  const id = selectedDocId.value
  try {
    const tagsJSON = buildTagsJSON(docTagsText.value)
    unwrap(await UpdateNoteDoc(id, docContent.value, tagsJSON))
    applyLocalTags(id, tagsJSON)
  } catch (e) {
    logErr('NoteManager:saveDoc', e)
  }
}
async function copyDoc() {
  if (!docContent.value) return
  try { await CopyText(docContent.value); toast.success(t('copied')) } catch (e) { toast.error(getErrorMessage(e)) }
}

// ---- 页内 AI ----
//
// AskAI 后端对任意文本都可用（services/note/ai.go，流式 + 4 个动作），
// 但此前唯一入口是快捷笔记浮窗里的 NoteAiPanel —— 在管理器里写长文时反而没有。
// 这里补一个：选中有内容就问选区，没选就问整篇。
const aiOpen = ref(false)
const aiText = ref('')
/** 打开面板前记下的光标位置；-1 = 当时没有焦点，写回时改为追加到末尾。 */
const savedCaret = ref(-1)
const previewRef = ref<HTMLElement | null>(null)

// —— 正文编辑器（CodeMirror） ——
//
// 原来这里是两个 v-model 的 textarea。换 CM 的收益不只是高亮：撤销历史、
// 行内查找、括号匹配这些都是白送的，markdown 源里的标题/列表/代码块也有了
// 视觉结构——一屏里能看出文章骨架，对写长文比纯文本强得多。
//
// 唯一要显式开的是**软换行**（lineWrapping）：CM 默认不折行，手抄 textarea 的
// 默认行为是折行的，不开的话一段中文会变成一条横向长龙。
const docEditorHost = ref<HTMLElement | null>(null)
// CM 走动态 import，加载失败时给一条可见提示，别让用户对着空白猜是不是样式坏了
const editorError = ref('')
let docEditor: CodeEditorHandle | null = null
let docEditorGen = 0
let offDocScroll: (() => void) | null = null

async function mountDocEditor() {
  editorError.value = ''
  const gen = ++docEditorGen
  // 先拆旧的：切文档/切格式都走这里，旧实例的滚动监听要一起摘掉
  offDocScroll?.()
  offDocScroll = null
  docEditor?.destroy()
  docEditor = null

  const host = docEditorHost.value
  if (!host) return
  const isText = selectedDoc.value?.format === 'text'
  try {
    const h = await createCodeEditor(host, {
      value: docContent.value,
      // 文件名只用于选语言：.md 出 markdown 高亮，纯文本笔记不给语言包
      filename: isText ? 'note.txt' : 'note.md',
      fontSize: '13px',
      lineWrapping: true,
      placeholder: isText ? t('notesTextPh') : t('notesMarkdownPh'),
      // 写作场景 Tab 应该能移出输入框，而不是吃进缩进
      tabIndent: false,
      // 只有 markdown 开标题折叠：大纲侧栏的 foldAt/foldAll 依赖这个扩展。
      // 其它编辑器使用者（NotePanel / 文件编辑器）不传，行为不变。
      fold: !isText,
      onChange: (v) => {
        if (v === docContent.value) return
        docContent.value = v
        onDocInput()
      },
      onSave: () => { void flushPendingSave() },
    })
    // await 期间用户可能已经切到另一篇：这次的实例直接丢掉，别挂错文档
    if (gen !== docEditorGen) { h.destroy(); return }
    docEditor = h
    offDocScroll = h.onScroll(() => syncScroll('input'))
    h.focus()
  } catch (e) {
    logErr('NoteManager:editor', e)
    editorError.value = e instanceof Error ? e.message : String(e)
  }
}

// 切文档 / 切格式 → 重建实例。重建而不是 setValue：否则 Ctrl+Z 会退回上一篇的内容。
watch(
  () => [selectedDocId.value, selectedDoc.value?.format ?? ''] as const,
  async () => {
    await nextTick()
    void mountDocEditor()
  },
  { immediate: true },
)

// 外部改内容（AI 写回、新建文档、格式化）→ 同步进编辑器。
// 自身击键引起的变更会在这里撞上「内容相同直接 return」，不会打架。
watch(docContent, (v) => { docEditor?.setValue(v) })

function openAi() {
  if (!selectedDoc.value || selectedDoc.value.isFolder) return
  // CM 的选区存在 state 里，面板抢走焦点也不会丢——不像 textarea 失焦就归 0
  const sel = docEditor?.getSelection() ?? null
  savedCaret.value = sel ? sel.from : -1
  const selected = sel?.text ?? ''
  aiText.value = selected.trim() ? selected : docContent.value
  aiOpen.value = true
}

// 在笔记内查找：复用编辑器自带的查找栏（Ctrl+F 同款），给一个可见按钮入口。
function findInDoc() { docEditor?.openFind() }

function onAiWriteBack(text: string, mode: 'insert' | 'replace' | 'append') {
  if (mode === 'replace') {
    docContent.value = text
  } else if (mode === 'append') {
    docContent.value = docContent.value.trim()
      ? docContent.value.replace(/\s*$/, '') + '\n\n' + text
      : text
  } else if (savedCaret.value >= 0) {
    const pos = savedCaret.value
    docContent.value = docContent.value.slice(0, pos) + text + docContent.value.slice(pos)
  } else {
    // 打开面板时编辑区没焦点（可能刚点完树里的另一篇）→ 追加，别猜位置
    docContent.value = docContent.value.trim()
      ? docContent.value.replace(/\s*$/, '') + '\n\n' + text
      : text
  }
  aiOpen.value = false
  savedCaret.value = -1
  void saveDoc()
  toast.success(t('noteAiWrittenBack'))
}
// ---- 编辑器工具条：插入 wiki 链接 ----
// 只负责把 `[[type:标题]]` 插到当前光标处；关联表由后端保存正文时自行扫描重建，
// 前端绝不写关联表。候选复用 ListLinkTargets，与 EntityLinkPicker 同一份数据。
const showWikiPicker = ref(false)
const wikiQuery = ref('')
const wikiTargets = ref<LinkTargetGroups>({ notes: [], todos: [], items: [] })
const wikiCandidates = computed(() => {
  const all: { type: EntityType; id: string; label: string }[] = [
    ...(wikiTargets.value.notes || []).map(x => ({ ...x, type: 'note' as EntityType })),
    ...(wikiTargets.value.todos || []).map(x => ({ ...x, type: 'todo' as EntityType })),
    ...(wikiTargets.value.items || []).map(x => ({ ...x, type: 'item' as EntityType })),
  ]
  return all.filter(c => matchesTextOrPinyin([c.label], wikiQuery.value, 'wiki:' + c.type + ':' + c.id))
})
function linkTypeLabel(type: string): string {
  if (type === 'note') return t('linkTypeNote')
  if (type === 'todo') return t('linkTypeTodo')
  if (type === 'item') return t('linkTypeItem')
  return type
}
async function openWikiPicker() {
  showWikiPicker.value = !showWikiPicker.value
  if (!showWikiPicker.value) return
  try {
    wikiTargets.value = unwrap<LinkTargetGroups>(await ListLinkTargets()) || { notes: [], todos: [], items: [] }
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}
function insertWiki(c: { type: EntityType; label: string }) {
  // 带类型前缀，避免同名笔记/待办/条目让 ResolveWikiTarget 歧义。
  const text = '[[' + c.type + ':' + c.label + ']]'
  const sel = docEditor?.getSelection() ?? null
  const from = sel ? sel.from : docContent.value.length
  const to = sel ? sel.to : from
  docContent.value = docContent.value.slice(0, from) + text + docContent.value.slice(to)
  // watch(docContent) 会把新内容同步进 CM；这里只额外落一次盘并收起浮层。
  showWikiPicker.value = false
  wikiQuery.value = ''
  void saveDoc()
  docEditor?.focus()
}

async function renameCurrent() {
  if (!selectedDoc.value) return
  try { unwrap(await RenameNoteNode(selectedDoc.value.id, selectedDoc.value.name)); await load() } catch (e) { toast.error(getErrorMessage(e)) }
}

onMounted(() => { loadTreePrefs(); loadRecent(); loadNoteView(); load() })
onBeforeUnmount(() => {
  void flushPendingSave()
  offDocScroll?.()
  docEditor?.destroy()
  docEditor = null
})

// 命令面板导航到本页后再写入 pendingOpenNoteId 的场景：load 完成后补开
watch(pendingOpenNoteId, (id) => {
  if (id && nodes.value.length) {
    const n = nodes.value.find(x => x.id === id)
    if (n) onDoc(n)
    pendingOpenNoteId.value = ''
  }
})
</script>

<template>
  <div class="notes-page">
    <!-- 顶部 -->
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('notesTitle') }}</h2>
        <span class="qd-page-sub">{{ t('notesSubtitle') }}</span>
      </div>
      <div class="qd-page-fill">
        <div class="qd-input-wrap notes-search">
          <Search :size="14" />
          <input v-model="searchQuery" class="qd-input ns-input" :placeholder="t('notesSearchPh')" />
          <button v-if="searchQuery" class="ns-clear" @click="clearSearch"><X :size="13" /></button>
        </div>
        <div class="notes-tags">
          <button class="nt-chip" :class="{ active: activeTag === '' }" @click="activeTag = ''">{{ t('notesAllTags') }}</button>
          <button v-for="tg in allTags" :key="tg" class="nt-chip" :class="{ active: activeTag === tg }" @click="activeTag = activeTag === tg ? '' : tg">{{ tg }}</button>
        </div>
      </div>
    </div>

    <div class="notes-body">
      <!-- 左：树 -->
      <div class="notes-tree" :style="{ width: treeWidth + 'px' }" v-show="!treeCollapsed">
        <!-- 两组就够了：新建（会改变内容）/ 树与选择（只改变看法）。
             原来每两个图标就插一根分隔线，7 个按钮被切成 4 段，反而比不分组更碎。 -->
        <div class="tree-toolbar">
          <div class="tt-group">
            <button class="tt-btn" :title="t('notesNewFolder')" @click="newFolderAt('')"><FolderPlus :size="13" /></button>
            <button class="tt-btn" :title="t('notesNewDoc')" @click="newDocAt('')"><FileText :size="13" /></button>
          </div>
          <span class="tt-sep"></span>
          <div class="tt-group">
            <button class="tt-btn" :title="t('notesExpandAll')" @click="expandAllOpen"><ChevronDown :size="13" /></button>
            <button class="tt-btn" :title="t('notesCollapseAll')" @click="collapseAll"><PanelLeft :size="13" /></button>
            <button class="tt-btn" :class="{ on: selectMode }" :title="selectMode ? t('notesMultiSelectExit') : t('notesMultiSelect')" @click="toggleSelectMode">
              <component :is="selectMode ? CheckSquare : Square" :size="13" />
            </button>
            <button v-if="selectMode" class="tt-btn" :title="allSelected ? t('selectedCount', { count: selectedCount }) : t('notesSelectAllDocs')" @click="toggleSelectAll">
              <component :is="allSelected ? CheckSquare : Square" :size="13" />
            </button>
            <button class="tt-btn" :title="treeCollapsed ? t('notesExpandTree') : t('notesCollapseTree')" @click="toggleTreeCollapsed">
              <component :is="treeCollapsed ? PanelLeft : PanelLeftClose" :size="13" />
            </button>
          </div>
        </div>

        <!-- 多选模式下的批量操作栏 -->
        <div v-if="selectMode" class="tree-batch">
          <div class="tb-line">
            <span class="tb-count">{{ t('selectedCount', { count: selectedCount }) }}</span>
            <button class="tb-clear" :disabled="!selectedCount" @click="clearSelection">{{ t('clear') }}</button>
          </div>
          <div class="tb-line">
            <input v-model="batchTagText" class="tb-input" :placeholder="t('notesBatchTagPh')" @keyup.enter="batchTag" />
            <button class="tb-btn" :disabled="!batchTagText.trim() || !selectedCount" @click="batchTag">{{ t('notesBatchTagApply') }}</button>
          </div>
          <div class="tb-line">
            <select v-model="moveTarget" class="tb-select">
              <option value="">{{ t('notesMoveTo') }}</option>
              <option v-for="f in folderOptions" :key="f.id" :value="f.id">{{ f.name }}</option>
            </select>
            <button class="tb-btn" :disabled="!moveTarget || !selectedCount" @click="batchMove">{{ t('notesBatchMove') }}</button>
          </div>
          <button class="tb-btn danger" :disabled="!selectedCount" @click="askBatchDelete">{{ t('notesBatchDelete') }}</button>
        </div>

        <div class="tree-wrap">
          <template v-if="!searchQuery && !activeTag">
            <!-- 最近打开：启动时为空树兜底，帮用户找回上次编辑的文档 -->
            <div v-if="recentDisplay.length" class="recent-block">
              <div class="recent-title">
                <Clock :size="11" />
                <span class="rt-label">{{ t('notesRecent') }}</span>
                <span class="rt-count">{{ recentDisplay.length }}</span>
              </div>
              <div
                v-for="n in recentDisplay" :key="'recent-'+n.id"
                class="qd-row recent-row"
                :class="{ active: !selectMode && selectedDocId === n.id, 'sel-on': selectMode && selectedIds.has(n.id) }"
                @click="selectMode ? toggleSelect(n.id) : onDoc(n)"
              >
                <span v-if="selectMode" class="tn-check" :class="{ on: selectedIds.has(n.id) }" @click.stop="toggleSelect(n.id)"></span>
                <FileText :size="13" class="tn-icon" />
                <div class="rn-text">
                  <div class="rn-line1">
                    <span class="rn-name">{{ n.name || n.keyword }}</span>
                    <span v-if="n.rel" class="rn-time">{{ n.rel }}</span>
                  </div>
                  <div v-if="n.parentName" class="rn-path">{{ n.parentName }}</div>
                </div>
              </div>
            </div>
            <div v-for="root in tree" :key="root.id">
              <NoteTreeNode
                :node="root" :depth="0"
                :expanded="expanded" :editing-id="editingId" :edit-name="editName"
                :selected-doc="selectedDocId"
                :select-mode="selectMode" :selected-set="selectedIds"
                @toggle="toggleFolder" @select="onDoc"
                @rename-start="startRename" @rename-commit="commitRename" @rename-input="onRenameInput" @rename-cancel="cancelRename"
                @del="askDelete" @create-folder="newFolderAt" @create-doc="newDocAt"
                @drop-node="handleDrop" @reorder="reorderNode"
                @toggle-select="toggleSelect"
              />
            </div>
            <div v-if="!tree.length && !recentDisplay.length && !loading" class="tree-empty">{{ t('notesEmpty') }}</div>
          </template>
          <!-- 搜索/标签结果：扁平展示 -->
          <template v-else>
            <div v-for="n in flatResults" :key="'res-'+n.id" class="qd-row sr-row" :class="{ active: !selectMode && selectedDocId === n.id, 'sel-on': selectMode && selectedIds.has(n.id) }" @click="selectMode ? toggleSelect(n.id) : (n.isFolder ? toggleFolder(n.id) : onDoc(n))">
              <span v-if="selectMode" class="tn-check" :class="{ on: selectedIds.has(n.id) }" @click.stop="toggleSelect(n.id)"></span>
              <component :is="n.isFolder ? Folder : FileText" :size="13" class="tn-icon" />
              <span class="tn-name"><template v-for="(p, i) in rowHighlight(n)" :key="i"><mark v-if="p.hit" class="tn-hl">{{ p.s }}</mark><template v-else>{{ p.s }}</template></template></span>
            </div>
          </template>
        </div>
      </div>

      <!-- 树收起后的恢复条 -->
      <div v-if="treeCollapsed" class="tree-reopen" :title="t('notesExpandTree')" @click="toggleTreeCollapsed">
        <PanelLeft :size="16" />
      </div>
      <!-- 可拖拽分割线：调树宽 -->
      <div v-if="!treeCollapsed" class="tree-splitter" @mousedown="startResize"></div>

      <!-- 右：文档编辑 -->
      <div class="notes-editor">
        <!-- 视图 tab：文档 / 关系图谱。图谱铺满编辑区但不卸载它，
             切回来时 CodeMirror 的光标与撤销历史都还在。 -->
        <div class="ne-viewtabs">
          <button :class="{ active: !graphOpen }" @click="graphOpen = false">{{ t('notesTitle') }}</button>
          <button :class="{ active: graphOpen }" @click="graphOpen = true"><Network :size="13" />{{ t('graphTab') }}</button>
          <span class="ne-viewtabs-spacer"></span>
          <button v-if="!graphOpen && selectedDoc && outlineHeadings.length" :class="{ active: outlineOpen }" :title="t('outlineTitle')" @click="outlineOpen = !outlineOpen"><PanelRight :size="13" /></button>
        </div>
        <NoteGraphPanel v-if="graphOpen" class="ne-graph-overlay" @close="graphOpen = false" @open-target="onGraphOpenTarget" />
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
            <!-- Markdown 视图三段切换：源码 / 分屏 / 预览（默认分屏）。纯文本格式没有预览，不显示。 -->
            <div v-if="selectedDoc.format !== 'text'" class="ne-view">
              <button :class="{ active: noteView === 'source' }" :title="t('notesViewSource')" @click="setNoteView('source')"><Code2 :size="13" /></button>
              <button :class="{ active: noteView === 'split' }" :title="t('notesViewSplit')" @click="setNoteView('split')"><Columns2 :size="13" /></button>
              <button :class="{ active: noteView === 'preview' }" :title="t('notesViewPreview')" @click="setNoteView('preview')"><Eye :size="13" /></button>
            </div>
            <button class="ne-copy" :title="t('notesFindInDoc')" @click="findInDoc()"><FileSearch :size="13" /></button>
            <button class="ne-copy" :title="t('noteAiTitle')" @click="openAi"><Sparkles :size="13" /></button>
            <button class="ne-copy" :title="t('notesExportMd')" @click="exportDoc"><FileDown :size="13" /></button>
            <button class="ne-copy" :title="t('copy')" @click="copyDoc"><CopyIcon :size="13" /></button>
            <button v-if="tocItems.length" class="ne-copy" :class="{ active: tocOpen }" :title="t('notesToc')" @click="tocOpen = !tocOpen"><List :size="13" /></button>
            <button class="ne-copy" :title="t('wikiInsertLink')" @click="openWikiPicker"><Link2 :size="13" /></button>
            <div v-if="showWikiPicker" class="ne-wiki-pop">
              <div class="ne-wiki-search">
                <Search :size="12" />
                <input v-model="wikiQuery" class="ne-wiki-input" :placeholder="t('search')" spellcheck="false" />
              </div>
              <div class="ne-wiki-list">
                <div v-if="!wikiCandidates.length" class="ne-wiki-empty">{{ t('linkEmpty') }}</div>
                <button v-for="c in wikiCandidates" :key="c.type + ':' + c.id" class="ne-wiki-item" @click="insertWiki(c)">
                  <span class="ne-wiki-type">{{ linkTypeLabel(c.type) }}</span>
                  <span class="ne-wiki-label">{{ c.label }}</span>
                </button>
              </div>
            </div>
          </div>
          <div v-if="tocOpen && tocItems.length" class="ne-toc">
            <button v-for="(it, i) in tocItems" :key="i" class="ne-toc-item" :style="{ paddingLeft: 8 + (it.level - 1) * 14 + 'px' }" @click="gotoHeading(it.text)">
              {{ it.text }}
            </button>
          </div>
          <div class="ne-tags">
            <span class="ne-tags-label">{{ t('notesTag') }}</span>
            <input v-model="docTagsText" class="ne-tags-input" :placeholder="t('notesTagsPh')" @change="saveDoc" />
            <span class="ne-stats" :title="t('notesStatsTitle')">
              {{ t('notesStats', { words: docStats.words, chars: docStats.chars, lines: docStats.lines }) }}
            </span>
          </div>
          <!-- 条目互链：放在标签行下方，与文档正文同属一个视图；新建未保存由组件自己显示禁用态。 -->
          <EntityLinkPicker
            src-type="note"
            :src-id="selectedDocId"
            :src-label="selectedDoc?.name || ''"
            compact
          />
          <!-- markdown：左编辑 + 右预览，两栏滚动同步；纯文本：仅编辑区。
               两栏共用同一个编辑器宿主（格式切换只重建 CM，不动 DOM 结构）。
               视图模式只切「显隐」不卸载：编辑器宿主始终在 DOM 里，避免切换时
               CodeMirror 实例被销毁重建（丢光标/滚动/撤销历史）。隐藏用 v-show +
               父级 class，隐藏规则以 !important 压过 v-show 的内联 display:none。 -->
          <div class="ne-split" :class="'ne-view-' + noteView">
            <div
              ref="docEditorHost"
              class="ne-editor"
              :class="{ 'ne-editor-only': selectedDoc.format === 'text' }"
            ></div>
            <div v-show="selectedDoc.format !== 'text' && noteView !== 'source'" ref="previewRef" class="ne-preview markdown-body" v-html="previewHtml"
              @scroll="syncScroll('preview')" @click="onPreviewClick"></div>
            <NoteOutlinePanel
              v-if="outlineOpen && outlineHeadings.length"
              class="ne-outline-dock"
              :headings="outlineHeadings"
              @jump="onOutlineJump"
              @fold="onOutlineFold"
              @unfold="onOutlineFold"
              @fold-all="onOutlineFoldAll"
              @unfold-all="onOutlineUnfoldAll"
              @close="outlineOpen = false"
            />
          </div>

          <!-- 页内 AI：覆盖在编辑区上（与快捷笔记浮窗同一套面板） -->
          <NoteAiPanel
            v-if="aiOpen"
            :text="aiText"
            allow-write-back
            @close="aiOpen = false"
            @writeback="onAiWriteBack"
          />
          <!-- 编辑器动态 import 失败（dev 下依赖预构建 504 最常见）时，
               原来的 catch 只写日志，界面上就是一整片空白，极易误判成样式问题。
               详情放 title，正文给一句可操作的提示 + 重试入口。 -->
          <div v-if="editorError" class="ne-load-error" :title="editorError">
            <span>{{ t('notesEditorLoadFailed') }}</span>
            <button class="qd-btn qd-btn-ghost" @click="mountDocEditor()">{{ t('retry') }}</button>
          </div>
        </template>
      </div>
    </div>

    <ConfirmDialog :visible="showDel" :message="deletingNode?.isFolder ? t('notesDelFolder') : t('notesDelDoc')"
      @confirm="confirmDeleteNode" @cancel="showDel = false" />
    <ConfirmDialog :visible="showBatchDel" :message="t('confirmDeleteBatch', { count: selectedCount })"
      @confirm="confirmBatchDelete" @cancel="showBatchDel = false" />
  </div>
</template>

<style scoped>
.notes-page { height: 100%; display: flex; flex-direction: column; background: var(--color-bg-primary); }
/* 页头已统一到 styles/app.css 的 .qd-page-header（标题 + 搜索 + 标签三段式） */
.notes-search { width: 240px; flex-shrink: 0; }
.ns-input { padding-left: 28px; padding-right: 26px; }
.ns-clear { position: absolute; right: 6px; border: none; background: none; color: var(--color-text-muted); cursor: pointer; display: flex; align-items: center; }
.notes-tags { flex: 1; min-width: 0; display: flex; gap: 6px; flex-wrap: wrap; align-items: center; margin-left: 8px; }
.nt-chip { padding: 2px 6px; border: none; background: transparent; color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit; flex-shrink: 0; line-height: 1; transition: color var(--transition-fast), font-weight var(--transition-fast); }
.nt-chip:hover { color: var(--color-text-primary); }
.nt-chip.active { color: var(--color-accent); font-weight: 600; }

.notes-body { flex: 1; display: flex; min-height: 0; }
.notes-tree { width: 250px; flex-shrink: 0; border-right: 1px solid var(--color-border); display: flex; flex-direction: column; background: var(--color-bg-primary); }
/* 整组一起换行，单个图标不会散到第二行：树宽是用户可拖的，窄栏下
   原来 7 个图标各自折行，看起来像工具栏被拆坏了。 */
.tree-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; padding: 6px 8px; border-bottom: 1px solid var(--color-border); }
.tt-group { display: flex; align-items: center; gap: 2px; flex-shrink: 0; }
.tt-btn { width: 26px; height: 26px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; border: none; background: none; color: var(--color-text-muted); cursor: pointer; border-radius: var(--radius-sm); }
.tt-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
/* 分组线只占图标高度的一半，不再是拉满整条工具栏的一根长线 */
.tt-sep { width: 1px; height: 14px; flex-shrink: 0; background: var(--color-border); margin: 0 2px; }
.tree-wrap { flex: 1; overflow-y: auto; padding: 4px; }
.tree-empty { color: var(--color-text-disabled); font-size: 12px; padding: 20px; text-align: center; }
.sr-row { display: flex; align-items: center; gap: 6px; padding: 6px; margin-bottom: 0; min-height: 26px; border-radius: var(--radius-sm); cursor: pointer; font-size: 13px; }
.sr-row:hover { background: var(--color-bg-hover); }

.notes-editor { flex: 1; display: flex; flex-direction: column; min-width: 0; position: relative; }
/* 编辑区空态：与其它页的空状态同一套语汇——图标先落一个方底挂牌，
   否则一根细线图标浮在大片深底上，像还没渲染完。 */
.ne-empty {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  color: var(--color-text-muted); gap: 10px;
}
.ne-empty :deep(svg) {
  padding: 12px;
  box-sizing: content-box;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.ne-empty p { margin: 0; font-size: var(--text-md); color: var(--color-text-secondary); }
.ne-head { display: flex; align-items: center; gap: 6px; padding: 6px 12px; border-bottom: 1px solid var(--color-border); position: relative; }
.ne-name { flex: 1; padding: 6px 10px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: var(--text-base); font-weight: 600; outline: none; font-family: inherit; }
.ne-name:focus { border-color: var(--color-border-focus-soft); }
.ne-format { display: flex; gap: 2px; flex-shrink: 0; }
.ne-format button { padding: 4px 10px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit; }
.ne-format button:first-child { border-radius: var(--radius-sm) 0 0 var(--radius-sm); }
.ne-format button:last-child { border-radius: 0 var(--radius-sm) var(--radius-sm) 0; margin-left: -1px; }
.ne-format button.active { color: var(--color-accent); background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.ne-format button:hover:not(.active) { color: var(--color-text-primary); }
/* Markdown 视图三段切换（源码 / 分屏 / 预览）：仅 markdown 格式出现，与格式按钮紧邻 */
.ne-view { display: flex; gap: 2px; flex-shrink: 0; }
.ne-view button { width: 26px; height: 26px; display: flex; align-items: center; justify-content: center; border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-muted); cursor: pointer; font-family: inherit; border-radius: var(--radius-sm); }
.ne-view button:first-child { border-radius: var(--radius-sm) 0 0 var(--radius-sm); }
.ne-view button:last-child { border-radius: 0 var(--radius-sm) var(--radius-sm) 0; margin-left: -1px; }
.ne-view button.active { color: var(--color-accent); background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.ne-view button:hover:not(.active) { color: var(--color-text-primary); }
/* 视图模式：隐藏某一栏，另一栏撑满。预览用 v-show（内联 display:none），
   故这里必须 !important 才能压过内联样式；编辑器无 v-show，正常隐藏即可。 */
.ne-view-source .ne-preview { display: none !important; }
.ne-view-preview .ne-editor { display: none !important; }
.ne-copy { border: none; background: none; color: var(--color-text-muted); cursor: pointer; padding: 6px; border-radius: var(--radius-sm); }
.ne-copy:hover { background: var(--color-bg-hover); color: var(--color-accent); }
/* 插入 wiki 链接的候选浮层：挂在 .ne-head 上，absolute 不占布局高度 */
.ne-wiki-pop { position: absolute; top: 100%; right: 12px; z-index: 20; width: 260px; margin-top: 4px; padding: 6px; border: 1px solid var(--popover-border-color); border-radius: var(--radius-sm); background: var(--color-popover-bg); box-shadow: var(--shadow-sm); }
.ne-wiki-search { display: flex; align-items: center; gap: 6px; color: var(--color-text-muted); }
.ne-wiki-input { flex: 1; min-width: 0; padding: 4px 6px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 12px; outline: none; font-family: inherit; }
.ne-wiki-input:focus { border-color: var(--color-border-focus-soft); }
.ne-wiki-list { max-height: 180px; overflow-y: auto; margin-top: 6px; }
.ne-wiki-empty { padding: 6px; color: var(--color-text-disabled); font-size: 12px; }
.ne-wiki-item { display: flex; align-items: center; gap: 6px; width: 100%; padding: 4px 6px; border: none; border-radius: var(--radius-xs); background: transparent; color: var(--color-text-primary); text-align: left; cursor: pointer; font-family: inherit; font-size: 12px; }
.ne-wiki-item:hover { background: var(--color-bg-hover); }
.ne-wiki-type { flex-shrink: 0; color: var(--color-text-muted); }
.ne-wiki-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ne-tags { display: flex; align-items: center; gap: 6px; padding: 4px 12px; border-bottom: 1px solid var(--color-border); }
.ne-tags-label { font-size: 11px; color: var(--color-text-muted); flex-shrink: 0; }
.ne-tags-input { flex: 1; padding: 4px 8px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 12px; outline: none; font-family: inherit; }
.ne-tags-input:focus { border-color: var(--color-border-focus-soft); }
/* 字数统计：右对齐、弱化显示，写作时能瞄一眼但不抢注意力 */
.ne-stats { flex-shrink: 0; font-size: 11px; color: var(--color-text-muted); font-variant-numeric: tabular-nums; }

/* 目录：浮在编辑区顶部，长文档跳转到标题用。层级用左缩进表达，不画竖线保持轻。 */
.ne-toc {
  max-height: 180px; overflow-y: auto; padding: 6px; border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-secondary); display: flex; flex-direction: column; gap: 1px;
}
.ne-toc-item {
  text-align: left; padding: 4px 8px; border: none; background: transparent; cursor: pointer;
  color: var(--color-text-muted); font-size: 12px; border-radius: var(--radius-sm); white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis;
}
.ne-toc-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
/* 搜索命中高亮：只覆盖文字底色，不用 <mark> 的默认黄底（深色主题下会刺眼） */
.tn-hl { background: var(--color-accent-bg); color: var(--color-accent); border-radius: 2px; padding: 0 1px; }
/* 视图 tab：文档 / 图谱。图谱是覆盖层，tab 必须压在它上面（z-index 更高）。 */
.ne-viewtabs { display: flex; align-items: center; gap: 4px; padding: 2px 8px; flex-shrink: 0; border-bottom: 1px solid var(--color-border); background: var(--color-bg-secondary); position: relative; z-index: 11; }
.ne-viewtabs button { display: flex; align-items: center; gap: 4px; padding: 4px 10px; border: 1px solid transparent; border-radius: var(--radius-sm); background: transparent; color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit; }
.ne-viewtabs button:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.ne-viewtabs button.active { color: var(--color-accent); background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.ne-viewtabs-spacer { flex: 1; }
.ne-graph-overlay { position: absolute; inset: 0; z-index: 10; background: var(--color-bg-primary); }
/* 大纲侧栏：作为 ne-split 的第三个 flex 栏，与源码/预览并排，不遮挡内容。 */
.ne-outline-dock { flex: 0 0 240px; min-width: 0; border-left: 1px solid var(--color-border); }

.ne-split { flex: 1; display: flex; min-height: 0; }
/* 编辑区宿主：CodeMirror 撑满左栏（行号/高亮/undo 都在实例里，见 utils/aiCodeEditor.ts）。
   原来这里是 textarea，padding/border 直接写在控件上；现在要给宿主定边框，内边距落到
   CM 的 .cm-content 上——否则右边框会贴在文字上 */
.ne-editor { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; background: var(--color-bg-secondary); border-right: 1px solid var(--color-border); }
.ne-editor.ne-editor-only { border-right: none; }
.ne-editor :deep(.cm-editor) { flex: 1; min-height: 0; }
.ne-editor :deep(.cm-content) { padding: 10px 14px 24px 4px; }
.ne-preview { flex: 1; min-width: 0; overflow: auto; padding: 12px 16px; background: var(--color-bg-primary); }
.ne-load-error {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); z-index: 12;
  display: flex; align-items: center; gap: 10px; padding: 12px 14px;
  border: 1px solid var(--popover-border-color); border-radius: var(--radius-md);
  background: var(--color-popover-bg); box-shadow: var(--shadow-md);
  color: var(--color-text-primary); font-size: var(--text-md);
}

/* 工具栏按钮高亮态（多选/激活） */
.tt-btn.on { background: var(--color-accent-bg); color: var(--color-accent); }

/* 多选 checkbox（与 NoteTreeNode 内自绘一致，因 scoped 不跨组件，这里再定义一份） */
/* 未勾选时不该是蓝框：14px 的小方块描一圈强调色，整棵树看起来都是"待办高亮"。
   未选中用常规边框，选中才转强调色。 */
.tn-check {
  width: 14px; height: 14px; flex-shrink: 0; border-radius: var(--radius-xs);
  border: 1px solid var(--color-border-light); background: var(--color-bg-primary);
  cursor: pointer; position: relative; transition: background-color var(--transition-fast), border-color var(--transition-fast);
}
.tn-check:hover { border-color: var(--color-text-muted); }
.tn-check.on { background: var(--color-accent); border-color: var(--color-accent); }
.tn-check.on::after {
  content: ''; position: absolute; left: 4px; top: 1px; width: 4px; height: 8px;
  border: solid var(--color-accent-text); border-width: 0 2px 2px 0; transform: rotate(45deg);
}
/* 单选高亮走 panels.css 的全局 .qd-row.active（select-bg 实底 + 3px accent 竖条），
   树行 / 最近打开 / 搜索结果三处共用，不再各写一份必然漂移的样式。
   多选只铺同一层底色，竖条留给「当前打开的文档」，勾选由实心 checkbox 表达。
   注意：NoteTreeNode.vue 里有一条等价的 .tn-row.sel-on，改值要两边同步。 */
.sel-on { background: var(--color-select-bg); color: var(--color-text-primary); }
.sel-on:hover { background: var(--color-select-bg); }
/* 本组件 scoped 的 .recent-row:hover / .sr-row:hover 特异性会压过上面那条，
   补一条更高特异性的，让选中/勾选行在 hover 时不闪回普通 hover 底色。 */
.recent-row.sel-on:hover, .sr-row.sel-on:hover,
.recent-row.active:hover, .sr-row.active:hover { background: var(--color-select-bg); }

/* 最近打开分区：融入式，靠顶部分隔线和树分开，不堆生硬卡片边框 */
.recent-block {
  margin: 4px 0 6px;
  padding: 6px 4px 0;
  border-top: 1px solid var(--color-border);
}
.recent-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--color-text-secondary);
  font-weight: 600;
  letter-spacing: 0.02em;
  padding: 0 4px 4px;
}
.recent-title :deep(svg) { color: var(--color-text-muted); opacity: 0.9; }
.recent-title .rt-label { flex: 1; }
.recent-title .rt-count {
  font-size: 10px;
  color: var(--color-text-disabled);
  font-weight: 500;
  background: var(--color-bg-secondary);
  border-radius: var(--radius-full);
  padding: 1px 6px;
}
.recent-row {
  display: flex;
  align-items: flex-start;
  gap: 7px;
  padding: 5px 6px;
  margin-bottom: 0; /* 抵消全局 .qd-row 的 2px 行距 */
  min-height: 0;    /* 抵消全局 .qd-row 的 34px 行高，保持双行文字自然高度 */
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background-color var(--transition-fast);
}
.recent-row:hover { background: var(--color-bg-hover); }
.recent-row .tn-icon { margin-top: 1px; flex-shrink: 0; color: var(--color-text-muted); }
.recent-row .tn-check { margin-top: 1px; flex-shrink: 0; }
.recent-row .rn-text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.recent-row .rn-line1 { display: flex; align-items: baseline; gap: 6px; }
.recent-row .rn-name { font-size: 13px; color: var(--color-text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0; }
.recent-row .rn-time { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; }
.recent-row .rn-path { font-size: 11px; color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 批量操作栏 */
.tree-batch { border-bottom: 1px solid var(--color-border); padding: 6px 8px; display: flex; flex-direction: column; gap: 6px; background: var(--color-bg-secondary); }
.tb-line { display: flex; gap: 4px; align-items: center; }
.tb-count { flex: 1; font-size: 11px; color: var(--color-text-muted); }
.tb-clear { border: none; background: none; color: var(--color-text-muted); cursor: pointer; font-size: 11px; padding: 2px 6px; border-radius: var(--radius-xs); }
.tb-clear:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.tb-clear:disabled { opacity: 0.4; cursor: default; }
.tb-input, .tb-select { flex: 1; min-width: 0; padding: 4px 6px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 12px; outline: none; font-family: inherit; }
.tb-input:focus, .tb-select:focus { border-color: var(--color-border-focus-soft); }
.tb-btn {display: inline-flex; align-items: center; justify-content: center; gap: 4px;  flex-shrink: 0; height: 28px; padding: 0 11px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-secondary); font-size: 12px; cursor: pointer; border-radius: var(--radius-sm); font-family: inherit; }
.tb-btn:hover:not(:disabled) { color: var(--color-text-primary); border-color: var(--color-border-focus); }
.tb-btn:disabled { opacity: 0.4; cursor: default; }
.tb-btn.danger { color: var(--color-danger); border-color: var(--color-danger-border); }
.tb-btn.danger:hover:not(:disabled) { background: var(--color-danger-bg); }

/* 树收起后的恢复条：窄竖条，hover 提示可展开 */
.tree-reopen {
  width: 14px; flex-shrink: 0; display: flex; align-items: center; justify-content: center;
  background: var(--color-bg-secondary); border-right: 1px solid var(--color-border);
  color: var(--color-text-muted); cursor: pointer;
}
.tree-reopen:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
/* 拖拽分割线 */
.tree-splitter { width: 4px; flex-shrink: 0; cursor: col-resize; background: transparent; transition: background-color var(--transition-fast); }
.tree-splitter:hover { background: var(--color-accent); }

/* Markdown 渲染（复用统一插件样式） */
.markdown-body { color: var(--color-text-primary); font-size: 13px; line-height: 1.7; word-break: break-word; }
.markdown-body :deep(h1), .markdown-body :deep(h2), .markdown-body :deep(h3), .markdown-body :deep(h4) { margin: 12px 0 8px; line-height: 1.3; }
.markdown-body :deep(h1) { font-size: var(--text-2xl); border-bottom: 1px solid var(--color-border); padding-bottom: 6px; }
.markdown-body :deep(h2) { font-size: 18px; border-bottom: 1px solid var(--color-border); padding-bottom: 4px; }
.markdown-body :deep(h3) { font-size: 15px; }
.markdown-body :deep(p) { margin: 8px 0; }
.markdown-body :deep(a) { color: var(--color-accent); }
/* wiki 链接没有 href（避免 DOMPurify 过滤伪协议），补手型与虚下划线让它可点 */
.markdown-body :deep(a.qd-wiki) { cursor: pointer; border-bottom: 1px dashed var(--color-accent); }
.markdown-body :deep(code) { background: var(--color-bg-tertiary); padding: 1px 5px; border-radius: var(--radius-xs); font-family: 'Consolas','Monaco',monospace; font-size: 13px; }
.markdown-body :deep(pre) { background: var(--color-bg-tertiary); padding: 10px 12px; border-radius: var(--radius-sm); overflow: auto; }
.markdown-body :deep(pre code) { background: none; padding: 0; }
.markdown-body :deep(blockquote) { border-left: 3px solid var(--color-border-focus); margin: 8px 0; padding: 2px 12px; color: var(--color-text-muted); }
.markdown-body :deep(ul), .markdown-body :deep(ol) { padding-left: 22px; margin: 8px 0; }
.markdown-body :deep(table) { border-collapse: collapse; margin: 8px 0; }
.markdown-body :deep(th), .markdown-body :deep(td) { border: 1px solid var(--color-border); padding: 4px 8px; }
.markdown-body :deep(img) { max-width: 100%; }
/* 数学公式：与 AI 对话页同一套容器规则（见 utils/mathMarkdown.ts） */
.markdown-body :deep(.md-math-inline) { display: inline; }
.markdown-body :deep(.md-math-block) { display: block; margin: 10px 0; overflow-x: auto; overflow-y: hidden; }
.markdown-body :deep(.md-math-block .katex-display) { margin: 0; }
.markdown-body :deep(.md-math-raw) {
  font-family: 'Consolas','Monaco',monospace; font-size: 12px;
  white-space: pre-wrap; word-break: break-word; color: var(--color-text-secondary);
}
</style>
