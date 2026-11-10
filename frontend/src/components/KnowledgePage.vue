<script setup lang="ts">
// KnowledgePage.vue — 本地文档知识库
//
// 形态参考 ima / Cherry Studio 的知识库：左边是文档清单（带索引状态），
// 中间是「检索 / 问答」，右边是可收起的**分段抽屉**。
//
// 四个刻意的取舍：
// 1) 分段预览是正经功能而不是调试工具。切片切错了（比如表格被拆成孤行、
//    PDF 抽成乱码），检索再怎么调都是白搭，用户必须能亲眼看一眼。
// 2) 分段不能做成「和检索并列的第三个 tab」。那样点一下命中去核对原文，
//    检索结果就被顶掉了，回来还得重搜 —— 所以它必须是并排的抽屉。
// 3) 检索结果标出「关键词命中 / 语义命中」。这是排查「为什么这条排第一」
//    的唯一手段 —— 没有它，用户只能怀疑整个功能是坏的。
// 4) 文档列表只表示意状态不搬文件。移除只是删索引，磁盘上的原文件不动，
//    所以确认框要写清楚这一点，否则用户不敢点。
import { computed, nextTick, onScopeDispose, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import {
  BookOpen, Plus, FolderPlus, RefreshCw, Trash2, Loader2, Settings2, X,
  Search, Sparkles, FileText, FileSpreadsheet, File as FileIcon, Send, Square, AlertCircle,
  Layers, Pencil, PanelRight, ChevronDown, ChevronUp, Eraser,
} from '@lucide/vue'
import { useToast } from '../composables/useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { SelectLocalFiles, SelectLocalDirectory } from '../../bindings/quickdock/services/appservice'
import {
  ListKBSources, AddKBFiles, AddKBFolder, RemoveKBSource, ScanKB, ReindexKBSource,
  ReindexAll, GetKBStatus, ListKBChunks, SearchKB, AskKB, GetKBSettings, SaveKBSettings, CancelKBIndex,
  ListKBLibraries, CreateKBLibrary, RenameKBLibrary, DeleteKBLibrary, MoveKBSource,
} from '../../bindings/quickdock/services/kb/kbservice'

const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

const { t } = useI18n()
const toast = useToast()

interface KBSource {
  id: string
  name: string
  libraryId: string
  path: string
  ext: string
  size: number
  mtime: number
  hash: string
  status: string
  chunkCount: number
  pageCount: number
  err: string
}

// KBLibrary 知识库分组。name 为空 = 默认库，显示名按当前语言给。
interface KBLibrary {
  id: string
  name: string
  icon: string
  sort: number
  sourceCount: number
  chunkCount: number
}

interface KBChunk {
  id: string
  sourceId: string
  idx: number
  heading: string
  text: string
  charCount: number
  page: number
  hasVec: boolean
}

interface SearchHit {
  chunkId: string
  sourceId: string
  sourceName: string
  libraryId: string
  ext: string
  heading: string
  text: string
  page: number
  score: number
  fromFts: boolean
  fromVec: boolean
}

interface KBStats {
  sources: number
  ready: number
  failed: number
  missing: number
  stale: number
  chunks: number
  vecChunks: number
  totalChars: number
}

interface KBStatus {
  running: boolean
  stage: string
  currentId: string
  currentName: string
  done: number
  total: number
  queue: number
  stats: KBStats
  vectorOn: boolean
  embedModel: string
  /** 有无可用的 AI 档案（后端判）。与 embedReady 分开：模型名没填也算「档案就绪」。 */
  profileReady: boolean
  embedReady: boolean
}

interface AskMessage {
  role: 'user' | 'assistant'
  text: string
  citations?: SearchHit[]
  pending?: boolean
}

const sources = ref<KBSource[]>([])
const status = ref<KBStatus | null>(null)
const loading = ref(false)
const selectedId = ref('')
const chunks = ref<KBChunk[]>([])
const chunksLoading = ref(false)

// 知识库分组。libId 为空串 = 「全部」视图（跨库检索）。
// 默认就是全部：刚装上只有一个库时，先看全量比先教用户建库更有用。
const libraries = ref<KBLibrary[]>([])
const libId = ref('')
// 建库 / 改名的弹窗。id 为空表示新建。
const libraryModal = reactive({ open: false, id: '', name: '', icon: '📚' })
const librarySaving = ref(false)
const LIBRARY_ICONS = ['📚', '💼', '🧪', '🏠', '📈', '🧩', '🎓', '🗂️']

const tab = ref<'search' | 'ask'>('search')
const query = ref('')
const hits = ref<SearchHit[]>([])
const searched = ref(false)
const searching = ref(false)
const searchMode = ref('')
const searchElapsed = ref(0)
const searchFallback = ref(false)

// 分段抽屉。它是独立的一栏而不是一个 tab：核对「检索结果在原文里是什么样」
// 是最高频的操作，一切换就会把结果列表顶掉，那是布局问题不是功能问题。
const previewOpen = ref(false)
const activeChunkId = ref('')
const expandedChunks = ref<string[]>([])
const previewListEl = ref<HTMLElement | null>(null)

const messages = ref<AskMessage[]>([])
const question = ref('')
const asking = ref(false)
const askListEl = ref<HTMLElement | null>(null)

const settingsOpen = ref(false)
const settings = reactive({ embedModel: '', topN: 5, vectorOn: true })
const settingsSaving = ref(false)

// 左栏文档筛选。文档上百之后「翻列表找文件」比搜索还慢，
// 但只有三五个文档时多一个输入框纯属占地方，所以按数量决定显不显示。
const docFilter = ref('')
const filteredSources = computed(() => {
  const q = docFilter.value.trim().toLowerCase()
  if (!q) return sources.value
  return sources.value.filter(s => s.name.toLowerCase().includes(q))
})
const showDocFilter = computed(() => sources.value.length > 8)

const selectedSource = computed(() => sources.value.find(s => s.id === selectedId.value) || null)
const currentLibrary = computed(() => libraries.value.find(l => l.id === libId.value) || null)
const stats = computed<KBStats>(() => status.value?.stats ?? {
  sources: 0, ready: 0, failed: 0, missing: 0, stale: 0, chunks: 0, vecChunks: 0, totalChars: 0,
})

const subtitle = computed(() => {
  const lib = currentLibrary.value
  if (lib) return t('kbSubtitleCount', { docs: lib.sourceCount, chunks: lib.chunkCount })
  const s = stats.value
  if (!s.sources) return t('kbSubtitle')
  return t('kbSubtitleCount', { docs: s.sources, chunks: s.chunks })
})

const progressPercent = computed(() => {
  const s = status.value
  if (!s || !s.running || !s.total) return 0
  return Math.min(100, Math.round((s.done / s.total) * 100))
})

// ---- 数据加载 ----

async function loadLibraries() {
  try {
    libraries.value = unwrap<KBLibrary[]>(await ListKBLibraries()) ?? []
    // 选中的库被删掉时退回「全部」：否则列表空着，
    // 用户看到的是「文档全没了」，而其实只是站在一个已经不存在的库上。
    if (libId.value && !libraries.value.some(l => l.id === libId.value)) {
      libId.value = ''
    }
  } catch (e) {
    logErr('kb', e)
  }
}

async function loadSources() {
  loading.value = true
  try {
    sources.value = unwrap<KBSource[]>(await ListKBSources(libId.value)) ?? []
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  } finally {
    loading.value = false
  }
}

async function loadStatus() {
  try {
    status.value = unwrap<KBStatus>(await GetKBStatus())
  } catch (e) {
    logErr('kb', e)
  }
}

async function loadSettings() {
  try {
    const cfg = unwrap<{ embedModel: string; topN: number; vectorOn: boolean }>(await GetKBSettings())
    if (cfg) {
      settings.embedModel = cfg.embedModel || ''
      settings.topN = cfg.topN || 5
      settings.vectorOn = cfg.vectorOn !== false
    }
  } catch (e) {
    logErr('kb', e)
  }
}

// ---- 分组 ----

// libLabel 分组的显示名。默认库在库里存的是空名字，这里按语言补上，
// 这样中英文界面不会出现一份写死的中文库名。
function libLabel(lib: KBLibrary) {
  return lib.name?.trim() || t('kbLibraryDefault')
}

function libraryName(id: string) {
  const lib = libraries.value.find(l => l.id === id)
  return lib ? libLabel(lib) : ''
}

async function selectLibrary(id: string) {
  if (libId.value === id) return
  libId.value = id
  selectedId.value = ''
  chunks.value = []
  // 换了库，原来那份文档多半不在新库里，抽屉里的分段就成了「无主」内容。
  previewOpen.value = false
  await loadSources()
}

function openLibraryModal(lib: KBLibrary | null) {
  libraryModal.id = lib?.id ?? ''
  // 用原始 name 而不是显示名：默认库显示的是本地化的「默认知识库」，
  // 一旦把它填进输入框再保存，就等于把中文名写进了数据库。
  libraryModal.name = lib?.name ?? ''
  libraryModal.icon = lib?.icon || '📚'
  libraryModal.open = true
}

async function saveLibrary() {
  if (librarySaving.value) return
  const name = libraryModal.name.trim()
  if (!libraryModal.id && !name) {
    toast.error(t('kbLibraryName'))
    return
  }
  librarySaving.value = true
  try {
    if (libraryModal.id) {
      await RenameKBLibrary(libraryModal.id, name, libraryModal.icon)
    } else {
      const lib = unwrap<KBLibrary>(await CreateKBLibrary(name, libraryModal.icon))
      if (lib) libId.value = lib.id
    }
    libraryModal.open = false
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  } finally {
    librarySaving.value = false
  }
}

async function removeLibrary(lib: KBLibrary) {
  if (libraries.value.length <= 1) {
    toast.error(t('kbLibraryLast'))
    return
  }
  const ok = await toast.confirm(t('kbLibraryDeleteConfirm', { name: libLabel(lib), n: lib.sourceCount }))
  if (!ok) return
  try {
    await DeleteKBLibrary(lib.id)
    if (libId.value === lib.id) libId.value = ''
    selectedId.value = ''
    chunks.value = []
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

// 把当前文档挪到别的库。挪完刷列表：按库过滤时它就该从当前视图消失。
async function moveSelected(ev: Event) {
  const src = selectedSource.value
  const target = (ev.target as HTMLSelectElement).value
  if (!src || !target || target === src.libraryId) return
  try {
    await MoveKBSource(src.id, target)
    await refreshAll()
    // 挪出当前视图后清掉选中：否则左栏已经找不到它，
    // 抽屉里还挂着它的分段，看着像数据错乱。
    if (libId.value && libId.value !== target) {
      selectedId.value = ''
      chunks.value = []
      previewOpen.value = false
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

// ---- 导入 ----

async function addFiles() {
  try {
    const picked = unwrap<string[]>(await SelectLocalFiles(t('kbPickFileTitle'), t('kbPickFileFilter'), '*.md;*.markdown;*.txt;*.csv;*.json;*.log;*.docx;*.xlsx;*.pdf'))
    if (!picked || !picked.length) return
    const res = unwrap<{ added: number; skipped: number; unsupported: string[] }>(await AddKBFiles(libId.value, picked))
    if (res) {
      if (res.added > 0) toast.success(t('kbAddedCount', { n: res.added }))
      if (res.unsupported?.length) toast.error(t('kbUnsupported', { names: res.unsupported.slice(0, 3).join(', ') }))
    }
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

async function addFolder() {
  try {
    const dir = unwrap<string>(await SelectLocalDirectory(t('kbPickFolderTitle')))
    if (!dir) return
    const res = unwrap<{ added: number }>(await AddKBFolder(libId.value, dir, true))
    if (res && res.added > 0) toast.success(t('kbAddedCount', { n: res.added }))
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

async function rescan() {
  try {
    const res = unwrap<{ queued: number; missing: number }>(await ScanKB())
    if (res) {
      if (res.queued > 0) toast.success(t('kbQueuedCount', { n: res.queued }))
      else toast.success(t('kbScanNoChange'))
    }
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

async function reindexAll() {
  try {
    await ReindexAll()
    toast.success(t('kbReindexStarted'))
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

async function reindexOne(src: KBSource, ev: Event) {
  ev.stopPropagation()
  try {
    await ReindexKBSource(src.id)
    toast.success(t('kbReindexStarted'))
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

async function removeOne(src: KBSource, ev: Event) {
  ev.stopPropagation()
  const ok = await toast.confirm(t('kbRemoveConfirm', { name: src.name }))
  if (!ok) return
  try {
    await RemoveKBSource(src.id)
    if (selectedId.value === src.id) {
      selectedId.value = ''
      chunks.value = []
      previewOpen.value = false
    }
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  }
}

async function stopIndex() {
  try {
    await CancelKBIndex()
  } catch (e) {
    logErr('kb', e)
  }
}

// ---- 文档选择与分段 ----

async function selectDoc(src: KBSource) {
  // 再点一次已选中的文档 = 收起/展开抽屉。重复点不提「又拉一遍分段」的请求。
  if (selectedId.value === src.id) {
    if (!previewOpen.value) previewOpen.value = true
    else previewOpen.value = false
    return
  }
  selectedId.value = src.id
  activeChunkId.value = ''
  previewOpen.value = true
  await loadChunksFor(src.id)
}

async function loadChunksFor(id: string) {
  chunksLoading.value = true
  try {
    chunks.value = unwrap<KBChunk[]>(await ListKBChunks(id)) ?? []
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  } finally {
    chunksLoading.value = false
  }
}

// 点检索结果：抽屉打开该文档并滚到具体那一段。
// 同一份文档已经在抽屉里就不重复拉 —— 连点五条命中只该有零次请求。
async function openHit(h: SearchHit) {
  const src = sources.value.find(s => s.id === h.sourceId)
  if (!src) return
  activeChunkId.value = h.chunkId
  previewOpen.value = true
  if (selectedId.value !== src.id || !chunks.value.length) {
    selectedId.value = src.id
    await loadChunksFor(src.id)
  }
  await nextTick()
  scrollToActiveChunk()
}

function scrollToActiveChunk() {
  const el = previewListEl.value?.querySelector(`[data-chunk="${activeChunkId.value}"]`)
  // behavior: smooth 在长列表里能让人看清"跳到哪了"，不用瞬间闪现。
  if (el) el.scrollIntoView({ block: 'center', behavior: 'smooth' })
}

function togglePreview() {
  previewOpen.value = !previewOpen.value
  if (previewOpen.value && selectedSource.value && !chunks.value.length) {
    void loadChunksFor(selectedSource.value.id)
  }
}

function toggleChunk(id: string) {
  const i = expandedChunks.value.indexOf(id)
  if (i >= 0) expandedChunks.value.splice(i, 1)
  else expandedChunks.value.push(id)
}

function isChunkExpanded(id: string) {
  return expandedChunks.value.includes(id)
}

// ---- 检索 ----

let searchTimer: ReturnType<typeof setTimeout> | null = null

watch(query, () => {
  if (searchTimer) clearTimeout(searchTimer)
  const q = query.value.trim()
  if (q.length < 3) {
    hits.value = []
    searched.value = false
    return
  }
  // 250ms 防抖：每次检索要走向量接口（几百毫秒），逐字触发会把请求堆成雪崩。
  searchTimer = setTimeout(() => { void runSearch() }, 250)
})

async function runSearch() {
  const q = query.value.trim()
  if (q.length < 3) return
  searching.value = true
  try {
    const res = unwrap<{ hits: SearchHit[]; mode: string; elapsedMs: number; vecFallback: boolean }>(
      await SearchKB(q, settings.topN, libId.value),
    )
    hits.value = res?.hits ?? []
    searchMode.value = res?.mode ?? 'fts'
    searchElapsed.value = res?.elapsedMs ?? 0
    searchFallback.value = !!res?.vecFallback
    searched.value = true
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  } finally {
    searching.value = false
  }
}

// 清空检索：一并把防抖定时器掐掉，否则刚清空又会被上一次的延迟调用刷回结果。
function clearQuery() {
  if (searchTimer) clearTimeout(searchTimer)
  query.value = ''
  hits.value = []
  searched.value = false
}

// 高亮：按空格把查询拆成词，只标 2 字以上的词。
// 用 split 的捕获组做切分，奇数下标就是命中片段 —— 不用 v-html，
// 文档内容里可能有尖括号，直接注入 HTML 会被当成标签。
function highlightParts(text: string, q: string): { t: string; hit: boolean }[] {
  const terms = q.split(/\s+/).map(w => w.trim()).filter(w => w.length >= 2)
  if (!terms.length) return [{ t: text, hit: false }]
  const escaped = terms.map(w => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')
  const parts = text.split(new RegExp(`(${escaped})`, 'gi'))
  return parts.map((part, i) => ({ t: part, hit: i % 2 === 1 }))
}

// ---- 问答 ----

const STREAM_ID = 'kb-ask'

function scrollAskToBottom() {
  nextTick(() => {
    const el = askListEl.value
    if (el) el.scrollTop = el.scrollHeight
  })
}

async function sendQuestion() {
  const q = question.value.trim()
  if (!q || asking.value) return
  messages.value.push({ role: 'user', text: q })
  const reply: AskMessage = { role: 'assistant', text: '', pending: true }
  messages.value.push(reply)
  question.value = ''
  asking.value = true
  scrollAskToBottom()
  tab.value = 'ask'

  try {
    const res = unwrap<{ answer: string; citations: SearchHit[] }>(
      await AskKB({ question: q, sourceId: '', libraryId: libId.value, streamId: STREAM_ID, topN: settings.topN }),
    )
    reply.pending = false
    reply.text = res?.answer || ''
    reply.citations = res?.citations ?? []
    if (!reply.text) reply.text = t('kbAskNoHit')
  } catch (e) {
    reply.pending = false
    reply.text = getErrorMessage(e)
    logErr('kb', e)
  } finally {
    asking.value = false
    scrollAskToBottom()
  }
}

// ---- 设置 ----

function openSettings() {
  void loadSettings()
  settingsOpen.value = true
}

async function saveSettings() {
  settingsSaving.value = true
  try {
    await SaveKBSettings({
      embedModel: settings.embedModel.trim(),
      topN: settings.topN,
      vectorOn: settings.vectorOn,
    })
    settingsOpen.value = false
    toast.success(t('save'))
    await refreshAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('kb', e)
  } finally {
    settingsSaving.value = false
  }
}

// ---- 通用 ----

async function refreshAll() {
  // 先读分组再读文档：loadLibraries 可能把 libId 重置成「全部」，
  // 反过来就会用上一个已经失效的库 id 去查文档，得到空列表。
  await loadLibraries()
  await Promise.all([loadSources(), loadStatus()])
}

function iconFor(ext: string) {
  if (ext === '.xlsx') return FileSpreadsheet
  if (ext === '.pdf') return FileIcon
  return FileText
}

function statusKey(s: string) {
  switch (s) {
    case 'ready': return 'kbStatusReady'
    case 'parsing': return 'kbStatusParsing'
    case 'chunking': return 'kbStatusChunking'
    case 'embedding': return 'kbStatusEmbedding'
    case 'failed': return 'kbStatusFailed'
    case 'missing': return 'kbStatusMissing'
    case 'stale': return 'kbStatusStale'
    default: return 'kbStatusPending'
  }
}

function docMeta(s: KBSource) {
  if (s.status === 'failed' && s.err) return s.err
  if (s.chunkCount > 0) return t('kbChunkCount', { n: s.chunkCount })
  return t(statusKey(s.status))
}

// 高亮切片缓存：computed 只在 hits / query 变化时重算一次。
// 原来是模板 v-for 里直接调 hitParts(h)，内部每次 new RegExp + split 全文——
// 任何无关状态变化（索引进度事件、面板开关）都会把所有命中片段重算一遍。
const hitPartsMap = computed(() => {
  const m = new Map<SearchHit, { t: string; hit: boolean }[]>()
  const q = query.value.trim()
  for (const h of hits.value) m.set(h, highlightParts(h.text, q))
  return m
})

function hitParts(h: SearchHit) {
  return hitPartsMap.value.get(h) ?? highlightParts(h.text, query.value.trim())
}

const unsubscribers: Array<() => void> = []

function bindEvents() {
  unsubscribers.push(Events.On('kb:index-progress', (ev: any) => {
    const d = ev?.data ?? ev
    if (d && status.value) {
      status.value.running = d.stage !== 'ready' && d.stage !== 'failed'
      status.value.stage = d.stage
      status.value.currentName = d.name
      status.value.done = d.done ?? 0
      status.value.total = d.total ?? 0
    }
    // 完成或失败时刷一次清单：状态点与块数都变了。
    if (d && (d.stage === 'ready' || d.stage === 'failed' || d.stage === 'missing')) {
      void refreshAll()
    }
  }))

  unsubscribers.push(Events.On('ai:ask-delta', (ev: any) => {
    const d = ev?.data ?? ev
    if (!d || d.id !== STREAM_ID) return
    const last = messages.value[messages.value.length - 1]
    if (last && last.role === 'assistant') {
      last.text += d.delta || ''
      scrollAskToBottom()
    }
  }))
}

refreshAll()
loadSettings()
bindEvents()

onScopeDispose(() => {
  if (searchTimer) clearTimeout(searchTimer)
  unsubscribers.forEach(fn => { try { fn() } catch { /* 取消订阅失败无影响 */ } })
})
</script>

<template>
  <div class="qd-page">
    <header class="qd-page-header">
      <div class="qd-page-titles">
        <h1 class="qd-page-title">
          <BookOpen :size="16" />
          {{ t('kbTitle') }}
        </h1>
        <p class="qd-page-sub">{{ subtitle }}</p>
      </div>
      <div class="qd-page-actions">
        <button v-if="status?.running" class="qd-btn qd-btn-danger" @click="stopIndex">
          <Square :size="12" />
          {{ t('kbStop') }}
        </button>
        <button class="qd-btn qd-btn-primary" :disabled="status?.running" @click="addFiles">
          <Plus :size="14" />
          {{ t('kbAddFiles') }}
        </button>
        <button class="qd-btn" :disabled="status?.running" @click="addFolder">
          <FolderPlus :size="14" />
          {{ t('kbAddFolder') }}
        </button>
        <button class="qd-icon-btn" :disabled="status?.running" :title="t('kbRescan')" @click="rescan">
          <RefreshCw :size="15" />
        </button>
        <button class="qd-icon-btn" :disabled="status?.running || !sources.length" :title="t('kbReindexAll')" @click="reindexAll">
          <Sparkles :size="15" />
        </button>
        <button class="qd-icon-btn" :title="t('kbSettingsTitle')" @click="openSettings">
          <Settings2 :size="15" />
        </button>
      </div>
    </header>

    <div v-if="status?.running" class="kb-progress">
      <Loader2 :size="12" class="kb-spin" />
      <span class="kb-progress-name">{{ t('kbIndexing', { name: status.currentName || status.stage }) }}</span>
      <div class="kb-progress-track">
        <div
          class="kb-progress-bar"
          :class="{ indeterminate: !status.total }"
          :style="status.total ? { width: progressPercent + '%' } : undefined"
        ></div>
      </div>
      <span v-if="status.total" class="kb-progress-count">{{ status.done }}/{{ status.total }}</span>
    </div>

    <div class="qd-page-body kb-body">
      <aside class="kb-side">
        <!-- 库切换：单行横向滚动。左栏只有 288px，换行会把文档列表挤掉半屏，
             而库一般五六个，滚动比换行更省纵向空间。 -->
        <div class="kb-libs">
          <button class="kb-lib-chip" :class="{ active: libId === '' }" @click="selectLibrary('')">
            <Layers :size="12" />
            <span class="kb-lib-name">{{ t('kbLibraryAll') }}</span>
          </button>
          <button
            v-for="lib in libraries"
            :key="lib.id"
            class="kb-lib-chip"
            :class="{ active: libId === lib.id }"
            @click="selectLibrary(lib.id)"
          >
            <span class="kb-lib-icon">{{ lib.icon || '📚' }}</span>
            <span class="kb-lib-name">{{ libLabel(lib) }}</span>
            <span class="kb-lib-count">{{ lib.sourceCount }}</span>
          </button>
          <button class="kb-lib-chip kb-lib-new" :title="t('kbLibraryNew')" @click="openLibraryModal(null)">
            <Plus :size="12" />
          </button>
        </div>
        <div class="kb-side-head">
          <span>{{ t('kbDocCount', { n: sources.length }) }}</span>
          <span class="kb-side-vec">{{ t('kbVecCount', { n: stats.vecChunks }) }}</span>
          <!-- 库的编辑/删除只在「站在某个具体库上」时出现，避免让人以为能删掉「全部」。 -->
          <span v-if="currentLibrary" class="kb-lib-acts">
            <button class="kb-lib-btn" :title="t('kbLibraryEdit')" @click="openLibraryModal(currentLibrary)">
              <Pencil :size="12" />
            </button>
            <button class="kb-lib-btn danger" :title="t('kbLibraryDelete')" @click="removeLibrary(currentLibrary)">
              <Trash2 :size="12" />
            </button>
          </span>
        </div>
        <div v-if="showDocFilter" class="kb-doc-filter">
          <Search :size="12" class="kb-doc-filter-icon" />
          <input
            v-model="docFilter"
            class="kb-doc-filter-input"
            :placeholder="t('kbDocFilter')"
            spellcheck="false"
          />
          <button v-if="docFilter" class="kb-search-clear" :title="t('kbSearchClear')" @click="docFilter = ''">
            <X :size="12" />
          </button>
        </div>
        <div class="kb-side-list">
          <div v-if="!sources.length && !loading" class="kb-empty-side">
            <BookOpen :size="22" />
            <p class="kb-empty-title">{{ libId ? t('kbLibraryEmpty') : t('kbEmptyTitle') }}</p>
            <p class="kb-empty-desc">{{ t('kbEmptyDesc') }}</p>
          </div>
          <div v-else-if="!filteredSources.length" class="kb-empty-side">
            <Search :size="20" />
            <p class="kb-empty-title">{{ t('kbDocFilterEmpty') }}</p>
          </div>
          <button
            v-for="s in filteredSources"
            :key="s.id"
            class="kb-doc"
            :class="{ active: selectedId === s.id }"
            @click="selectDoc(s)"
          >
            <component :is="iconFor(s.ext)" :size="15" class="kb-doc-icon" />
            <span class="kb-doc-main">
              <span class="kb-doc-name">{{ s.name }}</span>
              <span class="kb-doc-meta" :class="{ bad: s.status === 'failed' }">{{ docMeta(s) }}</span>
            </span>
            <span class="kb-doc-actions">
              <span class="kb-doc-reindex" :title="t('kbReindex')" @click="reindexOne(s, $event)">
                <RefreshCw :size="12" />
              </span>
              <span class="kb-doc-remove" :title="t('kbRemove')" @click="removeOne(s, $event)">
                <Trash2 :size="12" />
              </span>
            </span>
            <span class="kb-dot" :class="'st-' + s.status"></span>
          </button>
        </div>
      </aside>

      <section class="kb-main">
        <div class="kb-tabs">
          <button class="kb-tab" :class="{ active: tab === 'search' }" @click="tab = 'search'">
            <Search :size="13" />
            {{ t('kbTabSearch') }}
          </button>
          <button class="kb-tab" :class="{ active: tab === 'ask' }" @click="tab = 'ask'">
            <Sparkles :size="13" />
            {{ t('kbTabAsk') }}
          </button>
          <div class="kb-tabs-right">
            <span v-if="tab === 'search' && searched" class="kb-tabs-meta">
              {{ searchMode === 'hybrid' ? t('kbSearchModeHybrid') : t('kbSearchModeFts') }} · {{ searchElapsed }}ms
            </span>
            <!-- 分段预览的开关。它是一个并排的抽屉而不是第三个 tab：
                 核对「命中在原文里长什么样」时检索结果必须留在原位。 -->
            <button
              class="qd-icon-btn kb-preview-toggle"
              :class="{ 'is-on': previewOpen }"
              :title="t('kbTabChunks')"
              :disabled="!selectedSource"
              @click="togglePreview"
            >
              <PanelRight :size="15" />
            </button>
          </div>
        </div>

        <!-- 检索 -->
        <div v-show="tab === 'search'" class="kb-pane">
          <div class="kb-pane-top">
            <div class="kb-pane-inner">
              <div class="kb-search">
                <Search :size="14" class="kb-search-icon" />
                <input
                  v-model="query"
                  class="kb-search-input"
                  :placeholder="t('kbSearchPlaceholder')"
                  spellcheck="false"
                />
                <Loader2 v-if="searching" :size="14" class="kb-spin" />
                <button v-else-if="query" class="kb-search-clear" :title="t('kbSearchClear')" @click="clearQuery">
                  <X :size="13" />
                </button>
              </div>
              <p v-if="query.trim().length > 0 && query.trim().length < 3" class="kb-hint">{{ t('kbSearchTooShort') }}</p>
              <p v-if="searchFallback" class="kb-hint warn">{{ t('kbSearchFallback') }}</p>
            </div>
          </div>

          <div class="kb-scroll">
            <div class="kb-pane-inner">
              <div v-if="searched && !hits.length && !searching" class="kb-empty">
                <Search :size="22" />
                <p class="kb-empty-title">{{ t('kbSearchEmpty') }}</p>
              </div>
              <p v-if="hits.length" class="kb-result-count">{{ t('kbHitCount', { n: hits.length }) }}</p>
              <article v-for="h in hits" :key="h.chunkId" class="kb-hit" @click="openHit(h)">
                <div class="kb-hit-head">
                  <component :is="iconFor(h.ext)" :size="12" class="kb-hit-icon" />
                  <span class="kb-hit-src">{{ h.sourceName }}</span>
                  <span v-if="!libId" class="kb-hit-lib">{{ libraryName(h.libraryId) }}</span>
                  <span v-if="h.page" class="kb-hit-pos">{{ t('kbPageNo', { n: h.page }) }}</span>
                  <span v-else-if="h.heading" class="kb-hit-pos">{{ h.heading }}</span>
                  <span class="kb-hit-tags">
                    <span v-if="h.fromFts && h.fromVec" class="kb-tag both">{{ t('kbHitBoth') }}</span>
                    <span v-else-if="h.fromFts" class="kb-tag fts">{{ t('kbHitFts') }}</span>
                    <span v-else-if="h.fromVec" class="kb-tag vec">{{ t('kbHitVec') }}</span>
                  </span>
                </div>
                <p class="kb-hit-text">
                  <template v-for="(part, i) in hitParts(h)" :key="i">
                    <mark v-if="part.hit" class="kb-mark">{{ part.t }}</mark>
                    <span v-else>{{ part.t }}</span>
                  </template>
                </p>
              </article>
            </div>
          </div>
        </div>

        <!-- 问答 -->
        <div v-show="tab === 'ask'" class="kb-pane">
          <div ref="askListEl" class="kb-scroll kb-ask-list kb-pane-inner">
            <div v-if="!messages.length" class="kb-empty">
              <Sparkles :size="22" />
              <p class="kb-empty-title">{{ t('kbAskEmptyTitle') }}</p>
              <p class="kb-empty-desc">{{ t('kbAskEmptyDesc') }}</p>
            </div>
            <div v-for="(m, i) in messages" :key="i" class="kb-msg" :class="m.role">
              <div class="kb-msg-body">
                <template v-if="m.pending && !m.text">
                  <span class="kb-typing">{{ t('kbAskThinking') }}</span>
                </template>
                <template v-else>{{ m.text }}</template>
              </div>
              <div v-if="m.citations?.length" class="kb-cites">
                <span class="kb-cites-label">{{ t('kbCitation') }}</span>
                <button
                  v-for="(c, ci) in m.citations"
                  :key="c.chunkId"
                  class="kb-cite"
                  @click="openHit(c)"
                >
                  [#{{ ci + 1 }}] {{ c.sourceName }}<template v-if="c.page"> · {{ t('kbPageNo', { n: c.page }) }}</template>
                </button>
              </div>
            </div>
          </div>
          <div class="kb-ask-box">
            <div class="kb-pane-inner kb-ask-inner">
              <textarea
                v-model="question"
                class="kb-ask-input"
                rows="2"
                :placeholder="t('kbAskPlaceholder')"
                @keydown.enter.exact.prevent="sendQuestion"
              ></textarea>
              <button class="qd-btn qd-btn-primary kb-ask-send" :disabled="asking || !question.trim()" @click="sendQuestion">
                <Loader2 v-if="asking" :size="14" class="kb-spin" />
                <Send v-else :size="14" />
              </button>
            </div>
          </div>
        </div>
      </section>

      <!-- 右栏：分段抽屉。与检索/问答并排，核对原文时中间的结果列表不动。 -->
      <aside v-if="previewOpen" class="kb-preview">
        <div class="kb-preview-head">
          <component :is="iconFor(selectedSource?.ext || '')" :size="14" class="kb-preview-icon" />
          <span class="kb-preview-name" :title="selectedSource?.path">{{ selectedSource?.name || t('kbTabChunks') }}</span>
          <span v-if="chunks.length" class="kb-preview-count">{{ t('kbChunkCount', { n: chunks.length }) }}</span>
          <button class="qd-icon-btn" :title="t('cancel')" @click="previewOpen = false">
            <X :size="14" />
          </button>
        </div>
        <div v-if="selectedSource && libraries.length > 1" class="kb-preview-tools">
          <span class="kb-move-label">{{ t('kbLibraryMoveTo') }}</span>
          <select class="kb-select" :value="selectedSource.libraryId" @change="moveSelected">
            <option v-for="lib in libraries" :key="lib.id" :value="lib.id">{{ libLabel(lib) }}</option>
          </select>
        </div>
        <div ref="previewListEl" class="kb-scroll kb-chunk-list">
          <div v-if="chunksLoading" class="kb-empty"><Loader2 :size="20" class="kb-spin" /></div>
          <div v-else-if="!selectedSource" class="kb-empty">
            <FileText :size="22" />
            <p class="kb-empty-title">{{ t('kbChunksPickDoc') }}</p>
          </div>
          <div v-else-if="!chunks.length" class="kb-empty">
            <FileText :size="22" />
            <p class="kb-empty-title">{{ t('kbChunksEmpty') }}</p>
          </div>
          <article
            v-for="c in chunks"
            :key="c.id"
            class="kb-chunk"
            :class="{ active: activeChunkId === c.id }"
            :data-chunk="c.id"
          >
            <div class="kb-chunk-head">
              <span class="kb-chunk-idx">#{{ c.idx + 1 }}</span>
              <span v-if="c.page" class="kb-chunk-pos">{{ t('kbPageNo', { n: c.page }) }}</span>
              <span v-else-if="c.heading" class="kb-chunk-pos">{{ c.heading }}</span>
              <span class="kb-chunk-size">{{ c.charCount }}</span>
              <span v-if="c.hasVec" class="kb-tag vec">{{ t('kbHitVec') }}</span>
            </div>
            <p class="kb-chunk-text" :class="{ open: isChunkExpanded(c.id) }">{{ c.text }}</p>
            <button v-if="c.text.length > 200" class="kb-chunk-more" @click="toggleChunk(c.id)">
              <component :is="isChunkExpanded(c.id) ? ChevronUp : ChevronDown" :size="12" />
              {{ isChunkExpanded(c.id) ? t('kbChunkCollapse') : t('kbChunkExpand') }}
            </button>
          </article>
        </div>
      </aside>
    </div>

    <div v-if="settingsOpen" class="kb-overlay" @click.self="settingsOpen = false">
      <div class="kb-modal">
        <div class="kb-modal-head">
          <h2 class="kb-modal-title">{{ t('kbSettingsTitle') }}</h2>
          <button class="kb-modal-close" @click="settingsOpen = false"><X :size="14" /></button>
        </div>
        <div class="kb-modal-body">
          <label class="kb-field">
            <span class="kb-field-label">{{ t('kbSetEmbedModel') }}</span>
            <input v-model="settings.embedModel" class="kb-input" :placeholder="t('kbSetEmbedModelPlaceholder')" spellcheck="false" />
            <span class="kb-field-hint">{{ t('kbSetEmbedModelHint') }}</span>
          </label>
          <label class="kb-field">
            <span class="kb-field-label">{{ t('kbSetTopN') }}</span>
            <input v-model.number="settings.topN" type="number" min="1" max="20" class="kb-input kb-input-sm" />
          </label>
          <label class="kb-check">
            <input v-model="settings.vectorOn" type="checkbox" />
            <span>{{ t('kbSetVector') }}</span>
          </label>
          <!-- 两种未就绪的原因要分开说：都没配 → 送去 AI 设置；档案有、模型名空
               → 提示就在本弹窗里填，别把人往别处赶（否则用户会以为档案又丢了）。 -->
          <p v-if="!status?.embedReady" class="kb-hint warn">
            <AlertCircle :size="12" />
            {{ status?.profileReady ? t('kbVecNoModel') : t('kbVecNotReady') }}
            <button
              v-if="!status?.profileReady"
              class="kb-hint-link"
              @click="emit('open-settings', 'ai:model')"
            >
              {{ t('kbGoAiSettings') }}
            </button>
          </p>
        </div>
        <div class="kb-modal-foot">
          <button class="qd-btn" @click="settingsOpen = false">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" :disabled="settingsSaving" @click="saveSettings">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <div v-if="libraryModal.open" class="kb-overlay" @click.self="libraryModal.open = false">
      <div class="kb-modal kb-modal-narrow">
        <div class="kb-modal-head">
          <h2 class="kb-modal-title">{{ libraryModal.id ? t('kbLibraryEdit') : t('kbLibraryNew') }}</h2>
          <button class="kb-modal-close" @click="libraryModal.open = false"><X :size="14" /></button>
        </div>
        <div class="kb-modal-body">
          <label class="kb-field">
            <span class="kb-field-label">{{ t('kbLibraryName') }}</span>
            <input
              v-model="libraryModal.name"
              class="kb-input"
              :placeholder="t('kbLibraryNamePlaceholder')"
              spellcheck="false"
              @keydown.enter.prevent="saveLibrary"
            />
          </label>
          <div class="kb-field">
            <span class="kb-field-label">{{ t('kbLibraryIcon') }}</span>
            <div class="kb-icon-row">
              <button
                v-for="ic in LIBRARY_ICONS"
                :key="ic"
                class="kb-icon-pick"
                :class="{ active: libraryModal.icon === ic }"
                @click="libraryModal.icon = ic"
              >{{ ic }}</button>
            </div>
          </div>
        </div>
        <div class="kb-modal-foot">
          <button class="qd-btn" @click="libraryModal.open = false">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" :disabled="librarySaving" @click="saveLibrary">{{ t('save') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 三段水面：左栏文档清单，中间检索/问答，右侧分段抽屉。
   抽屉是 flex 的第三段而不是浮层，这样中栏会自动收窄、不会遮住结果列表。 */
.kb-body {
  position: relative;
  display: flex;
  padding: 0;
  overflow: hidden;
}

/* 索引进度条：贴在页头下方，不占内容区高度，也不弹窗打断操作。
   进度条本体必须有确定的宽度来源 —— 没有 width 的进度条只会显示一条空槽。 */
.kb-progress {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
  padding: 5px 16px;
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border-bottom: 1px solid var(--color-border);
}

.kb-progress-name {
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-progress-track {
  flex: 1;
  height: 3px;
  border-radius: var(--radius-full);
  background: var(--color-bg-active);
  overflow: hidden;
}

.kb-progress-bar {
  height: 100%;
  background: var(--color-accent);
  transition: width 0.2s ease;
}

.kb-progress-bar.indeterminate {
  width: 30%;
  animation: kb-slide 1.2s ease-in-out infinite;
}

@keyframes kb-slide {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(340%); }
}

.kb-progress-count {
  font-variant-numeric: tabular-nums;
  color: var(--color-text-muted);
}

/* ---- 左栏 ---- */
.kb-side {
  display: flex;
  flex-direction: column;
  width: 272px;
  flex-shrink: 0;
  border-right: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}

/* 库切换：单行横向滚动。左栏只有 272px，换行会把文档列表挤掉半屏；
   而库一般五六个，横滚比换行省得多。滚动条不显示，靠边缘渐隐暗示还有内容。 */
.kb-libs {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  padding: 8px 10px 7px;
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: none;
  border-bottom: 1px solid var(--color-hairline);
}

.kb-libs::-webkit-scrollbar {
  display: none;
}

/* 计数行：文档数在左，「语义 N」与库操作贴右。
   库的编辑/删除从原来的独立一行收进这里，省下一整行高度。 */
.kb-side-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
  padding: 5px 6px 5px 12px;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-hairline);
}

.kb-side-vec {
  margin-left: auto;
  color: var(--color-accent);
}

.kb-lib-acts {
  display: inline-flex;
  align-items: center;
  gap: 1px;
}

.kb-side-list {
  flex: 1;
  overflow-y: auto;
  padding: 6px;
}

/* 左栏文档筛选：文档多了才出现（见 showDocFilter）。
   和检索框同构但更矮，避免左栏顶部堆成一片输入区。 */
.kb-doc-filter {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  margin: 6px 8px 0;
  padding: 3px 7px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-primary);
}

.kb-doc-filter:focus-within {
  border-color: var(--color-border-focus);
}

.kb-doc-filter-icon {
  flex-shrink: 0;
  color: var(--color-text-muted);
}

.kb-doc-filter-input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: var(--text-xs);
}

.kb-doc {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 8px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-primary);
  text-align: left;
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}

.kb-doc:hover {
  background: var(--color-bg-hover);
}

/* 选中态与 .qd-row.active 同一套：实底 + 左缘 3px 竖条。
   不再让文字和图标一起转蓝 —— 颜色只表达「当前项」，不兼任装饰；
   蓝底蓝边蓝字三层叠上去，整行会像被高亮笔涂过。 */
.kb-doc.active {
  background: var(--color-select-bg);
  border-color: transparent;
}
.kb-doc.active::before {
  content: '';
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 16px;
  border-radius: var(--radius-full);
  background: var(--color-accent);
}

.kb-doc-icon {
  flex-shrink: 0;
  color: var(--color-text-muted);
}

.kb-doc-main {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  flex: 1;
}

.kb-doc-name {
  font-size: var(--text-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-doc-meta {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-doc-meta.bad {
  color: var(--color-danger);
}

/* 操作按钮常驻占位、靠透明度显隐。
   原来用 display:none 切换，hover 时按钮凭空插入会把文件名挤短一截，行会跳。 */
.kb-doc-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
  opacity: 0;
  transition: opacity var(--transition-fast);
}

.kb-doc:hover .kb-doc-actions,
.kb-doc.active .kb-doc-actions {
  opacity: 1;
}

.kb-doc-reindex,
.kb-doc-remove {
  display: inline-flex;
  padding: 3px;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
}

.kb-doc-reindex:hover {
  color: var(--color-accent);
  background: var(--color-bg-hover);
}

.kb-doc-remove:hover {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

/* 状态点：只传达「能不能用」，所以颜色收敛成四档。 */
.kb-dot {
  width: 6px;
  height: 6px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--color-text-disabled);
}

.kb-dot.st-ready { background: var(--color-success); }
.kb-dot.st-failed { background: var(--color-danger); }
.kb-dot.st-missing { background: var(--color-warning); }
.kb-dot.st-stale { background: var(--color-warning); }
.kb-dot.st-embedding,
.kb-dot.st-parsing,
.kb-dot.st-chunking { background: var(--color-accent); }

/* ---- 中栏 ---- */
.kb-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.kb-tabs {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  min-height: 38px;
  padding: 0 10px;
  border-bottom: 1px solid var(--color-hairline);
}

.kb-tab {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 10px;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-sm);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.kb-tab:hover:not(:disabled) {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.kb-tab.active {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
  font-weight: 500;
}

.kb-tab:disabled {
  opacity: 0.45;
  cursor: default;
}

.kb-tabs-right {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-left: auto;
  min-width: 0;
}

.kb-tabs-meta {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 抽屉开关的"已打开"态。优先级要压过 .qd-icon-btn:hover。 */
.kb-preview-toggle.is-on {
  color: var(--color-accent);
  background: var(--color-accent-bg);
}

.kb-pane {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* 内容列最宽 820px 并居中：桌面窗口常见 1400px 以上，
   让一行文字横跨整屏会把读一屏变成"追一行"。 */
.kb-pane-inner {
  width: 100%;
  max-width: 820px;
  margin: 0 auto;
}

.kb-pane-top {
  flex-shrink: 0;
  padding: 10px 16px 4px;
}

.kb-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 8px 16px 18px;
}

/* ---- 检索 ---- */
.kb-search {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 7px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  transition: border-color var(--transition-fast);
}

.kb-search:focus-within {
  border-color: var(--color-border-focus);
}

.kb-search-icon {
  color: var(--color-text-muted);
  flex-shrink: 0;
}

.kb-search-input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: var(--text-sm);
}

.kb-search-clear {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  padding: 2px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.kb-search-clear:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.kb-result-count {
  margin: 0 0 6px 2px;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

/* 命中项做成轻列表而不是一叠卡片：十条结果是十个带框盒子时，
   眼睛要在框线之间反复跳，反而找不到内容。留白 + hover 底色更安静。 */
.kb-hit {
  padding: 9px 10px;
  margin-bottom: 2px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}

.kb-hit:hover {
  background: var(--color-bg-secondary);
  border-color: var(--color-border);
}

.kb-hit:active {
  background: var(--color-bg-active);
}

.kb-hit-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 5px;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-hit-icon {
  flex-shrink: 0;
}

.kb-hit-src {
  color: var(--color-text-secondary);
  font-weight: 500;
  max-width: 40%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-hit-pos {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-hit-tags {
  display: inline-flex;
  gap: 4px;
  margin-left: auto;
  flex-shrink: 0;
}

.kb-tag {
  padding: 0 5px;
  border-radius: var(--radius-sm);
  font-size: 10px;
  line-height: 16px;
  white-space: nowrap;
}

.kb-tag.fts {
  color: var(--color-text-secondary);
  background: var(--color-bg-active);
}

.kb-tag.vec {
  color: var(--color-accent);
  background: var(--color-accent-bg);
}

.kb-tag.both {
  color: var(--color-success);
  background: var(--color-success-bg);
}

.kb-hit-text {
  margin: 0;
  font-size: var(--text-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
  display: -webkit-box;
  -webkit-line-clamp: 4;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.kb-mark {
  padding: 0 1px;
  border-radius: 2px;
  background: var(--color-search-hit-bg);
  color: var(--color-text-primary);
}

/* ---- 问答 ---- */
.kb-ask-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 14px 16px 10px;
}

.kb-msg {
  display: flex;
  flex-direction: column;
  gap: 5px;
  max-width: 88%;
}

.kb-msg.user {
  align-self: flex-end;
  align-items: flex-end;
}

.kb-msg.assistant {
  align-self: flex-start;
}

.kb-msg-body {
  padding: 9px 12px;
  border-radius: var(--radius-md);
  font-size: var(--text-sm);
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
}

.kb-msg.user .kb-msg-body {
  background: var(--color-accent);
  color: var(--color-accent-text);
  border-bottom-right-radius: var(--radius-xs);
}

.kb-msg.assistant .kb-msg-body {
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-bottom-left-radius: var(--radius-xs);
  color: var(--color-text-primary);
}

.kb-typing {
  color: var(--color-text-muted);
}

.kb-cites {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
}

.kb-cites-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-cite {
  padding: 2px 7px;
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-sm);
  background: var(--color-card-bg);
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 11px;
  cursor: pointer;
  transition: border-color var(--transition-fast), color var(--transition-fast);
}

.kb-cite:hover {
  border-color: var(--color-accent);
  color: var(--color-accent);
}

.kb-ask-box {
  flex-shrink: 0;
  padding: 8px 16px 14px;
  border-top: 1px solid var(--color-hairline);
}

.kb-ask-inner {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}

.kb-ask-input {
  flex: 1;
  min-width: 0;
  resize: none;
  padding: 8px 11px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: var(--text-sm);
  line-height: 1.5;
  outline: none;
  transition: border-color var(--transition-fast);
}

.kb-ask-input:focus {
  border-color: var(--color-border-focus);
}

.kb-ask-send {
  flex-shrink: 0;
  width: 34px;
  height: 34px;
  padding: 0;
}

/* ---- 右栏：分段抽屉 ---- */
/* 宽度用 clamp：窄窗口给 300px 保底可读，宽屏最多 420px（再宽会挤压检索区）。 */
.kb-preview {
  display: flex;
  flex-direction: column;
  width: clamp(300px, 32%, 420px);
  flex-shrink: 0;
  border-left: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}

.kb-preview-head {
  display: flex;
  align-items: center;
  gap: 7px;
  flex-shrink: 0;
  padding: 6px 6px 6px 12px;
  border-bottom: 1px solid var(--color-hairline);
}

.kb-preview-icon {
  flex-shrink: 0;
  color: var(--color-text-muted);
}

.kb-preview-name {
  flex: 1;
  min-width: 0;
  font-size: var(--text-sm);
  font-weight: 500;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-preview-count {
  flex-shrink: 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.kb-preview-tools {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  padding: 6px 12px;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-hairline);
}

.kb-chunk-list {
  padding: 8px 10px 16px;
}

.kb-chunk {
  padding: 8px 10px;
  margin-bottom: 6px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  scroll-margin: 10px 0;
}

/* 从检索结果跳过来的那一段。用边框 + 底色一起标，只改底色在深色主题下几乎看不出。 */
.kb-chunk.active {
  border-color: var(--color-accent);
  background: var(--color-accent-bg);
}

.kb-chunk-head {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 4px;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-chunk-idx {
  color: var(--color-accent);
  font-variant-numeric: tabular-nums;
}

.kb-chunk-pos {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-chunk-size {
  margin-left: auto;
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}

/* 默认折 6 行：抽屉里一屏能扫到三四个块，判断切分质量靠的是"块头块尾"。
   需要读全的时候点下面那行展开，而不是让一整块把列表推走。 */
.kb-chunk-text {
  margin: 0;
  font-size: var(--text-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
  display: -webkit-box;
  -webkit-line-clamp: 6;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.kb-chunk-text.open {
  display: block;
  overflow: visible;
  -webkit-line-clamp: unset;
}

.kb-chunk-more {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  margin-top: 5px;
  padding: 0;
  border: none;
  background: none;
  color: var(--color-accent);
  font-family: inherit;
  font-size: var(--text-xs);
  cursor: pointer;
}

.kb-chunk-more:hover {
  color: var(--color-accent-hover);
  text-decoration: underline;
}

/* ---- 空态与提示 ---- */
.kb-empty,
.kb-empty-side {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 40px 18px;
  color: var(--color-text-disabled);
  text-align: center;
}

/* 左栏窄，空态不需要那么大的上下留白，否则一屏只剩"这里什么都没有"。 */
.kb-empty-side {
  padding: 28px 14px;
}

.kb-empty-title {
  margin: 0;
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
}

.kb-empty-desc {
  margin: 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-muted);
  max-width: 240px;
}

/* 提示行在 .kb-pane-inner 里，左右缩进由外层 padding 提供，不再自带 margin。 */
.kb-hint {
  display: flex;
  align-items: center;
  gap: 5px;
  margin: 6px 0 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-hint.warn {
  color: var(--color-warning);
}

/* 提示里的直达入口：做成文字链而不是第二个按钮——它是对警告的补充说明，
   给个实心按钮会把「保存」抢成次要操作。 */
.kb-hint-link {
  padding: 0;
  margin-left: 2px;
  border: 0;
  background: none;
  font-size: inherit;
  font-family: inherit;
  color: var(--color-accent);
  text-decoration: underline;
  cursor: pointer;
}

.kb-hint-link:hover {
  color: var(--color-accent-hover);
}

.kb-spin {
  animation: kb-rotate 0.9s linear infinite;
  flex-shrink: 0;
}

@keyframes kb-rotate {
  to { transform: rotate(360deg); }
}

/* ---- 设置浮层 ---- */
.kb-overlay {
  position: absolute;
  inset: 0;
  z-index: 40;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
}

.kb-modal {
  width: 420px;
  max-width: calc(100% - 48px);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface-elevated);
  box-shadow: var(--shadow-lg, 0 12px 32px rgba(0, 0, 0, 0.4));
}

.kb-modal-head {
  display: flex;
  align-items: center;
  padding: 12px 14px 8px;
}

/* 设置浮层用 absolute 定位，锚点必须是页面根容器（项目禁用 position:fixed）。 */
.qd-page {
  position: relative;
}

.kb-modal-title {
  margin: 0;
  font-size: var(--text-md);
  font-weight: 500;
  color: var(--color-text-primary);
}

.kb-modal-close {
  margin-left: auto;
  display: inline-flex;
  padding: 3px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.kb-modal-close:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.kb-modal-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 4px 14px 12px;
}

.kb-modal-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 0 14px 14px;
}

.kb-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.kb-field-label {
  font-size: var(--text-sm);
  color: var(--color-text-primary);
}

.kb-field-hint {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-input {
  padding: 6px 9px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: var(--text-sm);
  outline: none;
}

.kb-input:focus {
  border-color: var(--color-border-focus);
}

.kb-input-sm {
  max-width: 90px;
}

.kb-check {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
}

/* ---- 知识库分组 ---- */
/* 容器（单行横滚）的规则在左栏段，这里只管 chip 自身。 */

.kb-lib-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
  max-width: 170px;
  padding: 3px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-xs);
  white-space: nowrap;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast),
    border-color var(--transition-fast);
}

.kb-lib-chip:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.kb-lib-chip.active {
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border);
  color: var(--color-accent);
}

.kb-lib-icon {
  font-size: 11px;
  line-height: 1;
}

.kb-lib-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-lib-count {
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.kb-lib-chip.active .kb-lib-count {
  color: var(--color-accent);
}

.kb-lib-new {
  color: var(--color-text-muted);
}

.kb-lib-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.kb-lib-btn:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.kb-lib-btn.danger:hover {
  background: var(--color-danger-bg);
  color: var(--color-danger);
}

.kb-hit-lib {
  padding: 0 4px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  flex-shrink: 0;
}

.kb-select {
  padding: 2px 6px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: var(--text-xs);
  outline: none;
  cursor: pointer;
}

.kb-select:focus {
  border-color: var(--color-border-focus);
}

.kb-modal-narrow {
  width: 360px;
}

.kb-icon-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.kb-icon-pick {
  width: 28px;
  height: 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: transparent;
  font-size: var(--text-base);
  line-height: 1;
  cursor: pointer;
}

.kb-icon-pick:hover {
  background: var(--color-bg-hover);
}

.kb-icon-pick.active {
  border-color: var(--color-accent);
  background: var(--color-accent-bg);
}

/* ---- 窄窗口 ---- */
@media (max-width: 1080px) {
  .kb-side {
    width: 236px;
  }
  .kb-preview {
    width: 300px;
  }
}

/* 再窄就不并排了：抽屉改成盖在中栏上的浮层。
   硬挤成三段的结果是检索区只剩一条缝，三个区域同时不可用。 */
@media (max-width: 900px) {
  .kb-preview {
    position: absolute;
    top: 0;
    right: 0;
    bottom: 0;
    width: min(360px, 84%);
    z-index: 30;
    box-shadow: var(--shadow-pop);
  }
  .kb-tabs-meta {
    display: none;
  }
}
</style>
