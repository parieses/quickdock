<script setup lang="ts">
defineOptions({ name: 'NoteManagerPage' })
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onBeforeUnmount, inject, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { pendingOpenNoteId } from '../composables/bridge'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { pinyinMatch } from '../utils/pinyin'
import { debounce } from '../utils/debounce'
import ConfirmDialog from './ConfirmDialog.vue'
import NoteTreeNode from './NoteTreeNode.vue'
import NoteAiPanel from './NoteAiPanel.vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { installMathOnce } from '../utils/mathMarkdown'
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
import {
  Trash2, Search, X, FolderPlus, FileText, Folder,
  Copy as CopyIcon, PanelLeft, ChevronDown, Sparkles, FileDown, List,
} from '@lucide/vue'
import type { ToastAPI, Note } from '../types'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 数据 ----
const nodes = ref<Note[]>([])
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
function selectNodeIntoFolder(folderId: string) { selectedFolderId.value = folderId || '' }

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
  try { await RenameNoteNode(id, name); await load() } catch (e) { toast.error(getErrorMessage(e)) }
}

// 删除
const deletingNode = ref<Note | null>(null)
const showDel = ref(false)
function askDelete(n: Note) { deletingNode.value = n; showDel.value = true }
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
// 用 installMathOnce 而不是直接 marked.use —— 本文件每次进入页面都会重跑一遍 setup。
installMathOnce(marked)

const previewHtml = computed(() => (docContent.value ? DOMPurify.sanitize(marked.parse(docContent.value, { async: false }) as string) : ''))

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
    await UpdateNoteDoc(id, content, tagsJSON)
    applyLocalTags(id, tagsJSON)
  } catch (e) {
    logErr('NoteManager:flushPendingSave', e)
  }
}

/**
 * 保存后只在本地同步 tags，不重拉整棵树。
 *
 * 自动保存每 800ms 就可能触发一次，而 ListNotesTree 返回的是**整库**节点：
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

// 预览区与编辑区的滚动联动。
//
// 两栏独立滚动时，左边改到第 40 行、右边还停在第 3 行，对照修改完全没法用。
// 按**比例**同步而不是逐行量化：markdown 渲染后行高不等（标题/代码块/图片），
// 换算成行号必然错位，按滚动百分比对齐至少始终「看到大致同一段」。
let syncing = false
function syncScroll(from: 'input' | 'preview') {
  // 防止 A→B→A 的回环：由对方事件引起的设置不再反向触发。
  if (syncing) return
  const input = docInputRef.value
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
    await UpdateNoteDoc(id, docContent.value, tagsJSON)
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
const docInputRef = ref<HTMLTextAreaElement | null>(null)
const previewRef = ref<HTMLElement | null>(null)

function openAi() {
  if (!selectedDoc.value || selectedDoc.value.isFolder) return
  const el = docInputRef.value
  // 面板是覆盖层，textarea 一旦失焦，selectionStart 会归 0 —— 事后再读就会把
  // 整段回答插到文档开头。所以必须在打开前快照。
  const focused = !!el && document.activeElement === el
  savedCaret.value = focused ? el!.selectionStart : -1
  const selected = focused ? docContent.value.slice(el!.selectionStart, el!.selectionEnd) : ''
  aiText.value = selected.trim() ? selected : docContent.value
  aiOpen.value = true
}

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
async function renameCurrent() {
  if (!selectedDoc.value) return
  try { await RenameNoteNode(selectedDoc.value.id, selectedDoc.value.name); await load() } catch (e) { toast.error(getErrorMessage(e)) }
}

onMounted(() => load())
onBeforeUnmount(() => { void flushPendingSave() })

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
      <div class="notes-tree">
        <div class="tree-toolbar">
          <button class="tt-btn" :title="t('notesNewFolder')" @click="newFolderAt('')"><FolderPlus :size="13" /></button>
          <button class="tt-btn" :title="t('notesNewDoc')" @click="newDocAt('')"><FileText :size="13" /></button>
          <span class="tt-sep"></span>
          <button class="tt-btn" :title="t('notesExpandAll')" @click="expandAllOpen"><ChevronDown :size="13" /></button>
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
                @rename-start="startRename" @rename-commit="commitRename" @rename-input="onRenameInput" @rename-cancel="cancelRename"
                @del="askDelete" @create-folder="newFolderAt" @create-doc="newDocAt"
                @drop-node="handleDrop" @reorder="reorderNode"
              />
            </div>
            <div v-if="!tree.length && !loading" class="tree-empty">{{ t('notesEmpty') }}</div>
          </template>
          <!-- 搜索/标签结果：扁平展示 -->
          <template v-else>
            <div v-for="n in flatResults" :key="'res-'+n.id" class="sr-row" @click="n.isFolder ? toggleFolder(n.id) : onDoc(n)">
              <component :is="n.isFolder ? Folder : FileText" :size="13" class="tn-icon" />
              <span class="tn-name"><template v-for="(p, i) in highlightParts(n.name || n.keyword, searchQuery)" :key="i"><mark v-if="p.hit" class="tn-hl">{{ p.s }}</mark><template v-else>{{ p.s }}</template></template></span>
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
            <button class="ne-copy" :title="t('noteAiTitle')" @click="openAi"><Sparkles :size="13" /></button>
            <button class="ne-copy" :title="t('notesExportMd')" @click="exportDoc"><FileDown :size="13" /></button>
            <button class="ne-copy" :title="t('copy')" @click="copyDoc"><CopyIcon :size="13" /></button>
            <button v-if="tocItems.length" class="ne-copy" :class="{ active: tocOpen }" :title="t('notesToc')" @click="tocOpen = !tocOpen"><List :size="13" /></button>
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
          <!-- markdown：分栏编辑+预览；纯文本：仅单编辑区 -->
          <div v-if="selectedDoc.format !== 'text'" class="ne-split">
            <textarea ref="docInputRef" v-model="docContent" class="ne-input" spellcheck="false"
              :placeholder="t('notesMarkdownPh')" @input="onDocInput" @scroll="syncScroll('input')"></textarea>
            <div ref="previewRef" class="ne-preview markdown-body" v-html="previewHtml"
              @scroll="syncScroll('preview')"></div>
          </div>
          <div v-else class="ne-split">
            <textarea ref="docInputRef" v-model="docContent" class="ne-input ne-textonly" spellcheck="false"
              :placeholder="t('notesTextPh')" @input="onDocInput"></textarea>
          </div>

          <!-- 页内 AI：覆盖在编辑区上（与快捷笔记浮窗同一套面板） -->
          <NoteAiPanel
            v-if="aiOpen"
            :text="aiText"
            allow-write-back
            @close="aiOpen = false"
            @writeback="onAiWriteBack"
          />
        </template>
      </div>
    </div>

    <ConfirmDialog :visible="showDel" :message="deletingNode?.isFolder ? t('notesDelFolder') : t('notesDelDoc')"
      @confirm="confirmDeleteNode" @cancel="showDel = false" />
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
.tree-toolbar { display: flex; gap: 2px; padding: 6px 8px; border-bottom: 1px solid var(--color-border); }
.tt-btn { width: 26px; height: 26px; display: flex; align-items: center; justify-content: center; border: none; background: none; color: var(--color-text-muted); cursor: pointer; border-radius: var(--radius-sm); }
.tt-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.tt-sep { width: 1px; background: var(--color-border); margin: 0 4px; }
.tree-wrap { flex: 1; overflow-y: auto; padding: 4px; }
.tree-empty { color: var(--color-text-disabled); font-size: 12px; padding: 20px; text-align: center; }
.sr-row { display: flex; align-items: center; gap: 6px; padding: 6px; border-radius: var(--radius-sm); cursor: pointer; font-size: 13px; }
.sr-row:hover { background: var(--color-bg-hover); }

.notes-editor { flex: 1; display: flex; flex-direction: column; min-width: 0; position: relative; }
.ne-empty { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; color: var(--color-text-disabled); gap: 6px; }
.ne-head { display: flex; align-items: center; gap: 6px; padding: 8px 12px; border-bottom: 1px solid var(--color-border); }
.ne-name { flex: 1; padding: 6px 10px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 14px; font-weight: 600; outline: none; font-family: inherit; }
.ne-name:focus { border-color: var(--color-accent); }
.ne-format { display: flex; gap: 2px; flex-shrink: 0; }
.ne-format button { padding: 4px 10px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit; }
.ne-format button:first-child { border-radius: var(--radius-sm) 0 0 6px; }
.ne-format button:last-child { border-radius: 0 6px 6px 0; margin-left: -1px; }
.ne-format button.active { color: var(--color-accent); background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.ne-format button:hover:not(.active) { color: var(--color-text-primary); }
.ne-copy { border: none; background: none; color: var(--color-text-muted); cursor: pointer; padding: 6px; border-radius: var(--radius-sm); }
.ne-copy:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.ne-tags { display: flex; align-items: center; gap: 6px; padding: 6px 12px; border-bottom: 1px solid var(--color-border); }
.ne-tags-label { font-size: 11px; color: var(--color-text-muted); flex-shrink: 0; }
.ne-tags-input { flex: 1; padding: 4px 8px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 12px; outline: none; font-family: inherit; }
.ne-tags-input:focus { border-color: var(--color-accent); }
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
.ne-split { flex: 1; display: flex; min-height: 0; }
.ne-input { flex: 1; min-width: 0; resize: none; padding: 12px; border: none; border-right: 1px solid var(--color-border); background: var(--color-bg-secondary); color: var(--color-text-primary); font-size: 13px; font-family: 'Consolas','Monaco',monospace; line-height: 1.6; outline: none; box-sizing: border-box; }
.ne-input.ne-textonly { border-right: none; }
.ne-preview { flex: 1; min-width: 0; overflow: auto; padding: 12px 16px; background: var(--color-bg-primary); }

/* Markdown 渲染（复用统一插件样式） */
.markdown-body { color: var(--color-text-primary); font-size: 13px; line-height: 1.7; word-break: break-word; }
.markdown-body :deep(h1), .markdown-body :deep(h2), .markdown-body :deep(h3), .markdown-body :deep(h4) { margin: 12px 0 8px; line-height: 1.3; }
.markdown-body :deep(h1) { font-size: 20px; border-bottom: 1px solid var(--color-border); padding-bottom: 6px; }
.markdown-body :deep(h2) { font-size: 17px; border-bottom: 1px solid var(--color-border); padding-bottom: 4px; }
.markdown-body :deep(h3) { font-size: 15px; }
.markdown-body :deep(p) { margin: 8px 0; }
.markdown-body :deep(a) { color: var(--color-accent); }
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
