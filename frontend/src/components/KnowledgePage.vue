<script setup lang="ts">
import PageHeader from './PageHeader.vue'
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
  BookOpen, Briefcase, FlaskConical, Home, TrendingUp, Puzzle, GraduationCap, Folder,
  Plus, FolderPlus, RefreshCw, Trash2, Loader2, Settings2, X,
  Search, Sparkles, FileText, FileSpreadsheet, File as FileIcon, Send, Square, AlertCircle, Info,
  Layers, Pencil, PanelRight, ChevronDown, ChevronUp, Eraser,
  ListTree, Lightbulb, MessageSquare, Quote, CornerDownLeft,
} from '@lucide/vue'
import QdModal from './QdModal.vue'
import QdTabs from './QdTabs.vue'
import QdSearchBox from './QdSearchBox.vue'
import { useToast } from '../composables/useToast'
import { unwrap } from '../utils/api'
import { formatBytes } from '../utils/bytes'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { SelectLocalFiles, SelectLocalDirectory } from '../../bindings/quickdock/services/appservice'
import {
  ListKBSources, AddKBFiles, AddKBFolder, RemoveKBSource, ScanKB, ReindexKBSource,
  ReindexAll, GetKBStatus, ListKBChunks, SearchKB, AskKB, GetKBSettings, SaveKBSettings, CancelKBIndex,
  ListKBLibraries, CreateKBLibrary, RenameKBLibrary, DeleteKBLibrary, MoveKBSource,
} from '../../bindings/quickdock/services/kb/kbservice'

import { reportError } from '../utils/reportError'
// 与 internal/kb.SupportedExts 对齐（后端是权威，这里只是选择器过滤）。
const KB_PICK_FILTER = [
  '*.md', '*.markdown',
  '*.txt', '*.text', '*.log', '*.csv', '*.json', '*.yaml', '*.yml',
  '*.toml', '*.ini', '*.conf', '*.xml', '*.html', '*.htm',
  '*.css', '*.scss', '*.less',
  '*.go', '*.php', '*.js', '*.mjs', '*.cjs', '*.ts', '*.tsx', '*.jsx',
  '*.py', '*.java', '*.kt', '*.kts', '*.cs', '*.c', '*.h',
  '*.cpp', '*.hpp', '*.cc', '*.rs', '*.rb', '*.swift', '*.dart',
  '*.lua', '*.pl', '*.sh', '*.bash', '*.ps1', '*.bat', '*.cmd',
  '*.sql', '*.vue', '*.svelte', '*.scala', '*.groovy', '*.r',
  '*.docx', '*.xlsx', '*.pdf',
].join(';')

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
  /** 还没建向量的切片数（含部分失败残留）。 */
  vecMissing?: number
  /** 上一次索引结束时仍有切片没向量时，这里是剩余数（终态提示用）。 */
  vecPending?: number
}

// 索引阶段里「还在跑」的那几个。其余（ready / failed / missing / embed-partial）
// 都是终态：以前写的是「非 ready、非 failed 就算 running」，于是向量部分失败时
// embed-partial 被当成运行中，按钮全灰、又没有轮询，只能刷新页面才好。
const RUNNING_STAGES = new Set(['parsing', 'chunking', 'embedding'])

function isRunningStage(stage: string | undefined) {
  return !!stage && RUNNING_STAGES.has(stage)
}

interface AskMessage {
  role: 'user' | 'assistant'
  text: string
  citations?: SearchHit[]
  pending?: boolean
  /** 这句回答对应的问题原文：点引用时用它给右栏片段做高亮。 */
  question?: string
}

const sources = ref<KBSource[]>([])
const status = ref<KBStatus | null>(null)
const loading = ref(false)
// 左栏列表加载失败：与「这个库是空的」分开，否则用户以为文档全丢了会重新导入一遍
const sourcesFailed = ref(false)
const selectedId = ref('')
const chunks = ref<KBChunk[]>([])
const chunksLoading = ref(false)

// 知识库分组。libId 为空串 = 「全部」视图（跨库检索）。
// 默认就是全部：刚装上只有一个库时，先看全量比先教用户建库更有用。
const libraries = ref<KBLibrary[]>([])
const libId = ref('')
// 建库 / 改名的弹窗。id 为空表示新建。icon 存 Lucide 图标名（字符串）。
const libraryModal = reactive({ open: false, id: '', name: '', icon: 'BookOpen' })
const librarySaving = ref(false)
// 资料库图标：后端存图标名，渲染时按名取组件。旧库里存的是 emoji，
// 读取时经 libraryIconName 映射成图标名，保证存量数据不丢图标。
const LIBRARY_ICON_COMPONENTS = {
  BookOpen, Briefcase, FlaskConical, Home, TrendingUp, Puzzle, GraduationCap, Folder,
}
type LibraryIconName = keyof typeof LIBRARY_ICON_COMPONENTS
const LIBRARY_ICONS = Object.keys(LIBRARY_ICON_COMPONENTS) as LibraryIconName[]
const LEGACY_LIBRARY_ICONS: Record<string, LibraryIconName> = {
  '📚': 'BookOpen', '💼': 'Briefcase', '🧪': 'FlaskConical', '🏠': 'Home',
  '📈': 'TrendingUp', '🧩': 'Puzzle', '🎓': 'GraduationCap', '🗂️': 'Folder',
}
function libraryIconName(icon: string | undefined): LibraryIconName {
  if (!icon) return 'BookOpen'
  if (icon in LIBRARY_ICON_COMPONENTS) return icon as LibraryIconName
  return LEGACY_LIBRARY_ICONS[icon] || 'BookOpen'
}
function libraryIconComp(icon: string | undefined) {
  return LIBRARY_ICON_COMPONENTS[libraryIconName(icon)]
}

const tab = ref<'search' | 'ask'>('search')
const kbTabs = computed(() => [
  { id: 'search' as const, label: t('kbTabSearch') },
  { id: 'ask' as const, label: t('kbTabAsk') },
])
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
// 右栏片段要高亮的词：检索时是 query，点引用时是那条回答的问题原文。
const previewHighlight = ref('')
const expandedChunks = ref<string[]>([])
const previewListEl = ref<HTMLElement | null>(null)

const messages = ref<AskMessage[]>([])
const question = ref('')
const asking = ref(false)
const askListEl = ref<HTMLElement | null>(null)

const settingsOpen = ref(false)
const settings = reactive({ embedModel: '', embedBaseURL: '', embedAPIKey: '', topN: 5, vectorOn: true })
const settingsSaving = ref(false)
const settingsLoading = ref(false)

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

// ---- 中栏默认态（选中文档但还没检索时） ----
// 不做 AI 摘要：摘要需要新增后端字段并缓存，库里还没有这份数据。
// 换成三块**真实**信息——文档元信息、章节结构、开头原文——同样能回答
// 「这是什么文档、该从哪问起」，而且不用编造内容。
const chunkHeadings = computed(() => {
  const seen = new Set<string>()
  const out: string[] = []
  for (const c of chunks.value) {
    const h = c.heading?.trim()
    if (!h || seen.has(h)) continue
    seen.add(h)
    out.push(h)
    if (out.length >= 6) break
  }
  return out
})
const openingChunk = computed(() => chunks.value[0] || null)

// 问答范围：选中文档后默认「只问这篇」，也可以在问答框上方切回整库。
// 后端 AskKB 的 sourceId 就是为这个场景留的（vec 层按 SourceID 过滤）。
const askScope = ref<'doc' | 'lib'>('lib')
watch(selectedId, (id, prev) => {
  if (!id) askScope.value = 'lib'
  else if (!prev) askScope.value = 'doc'
})
const scopeSourceId = computed(() => (askScope.value === 'doc' ? selectedId.value : ''))

const suggestedPrompts = computed(() => {
  if (!selectedSource.value) return []
  const out = [t('kbPromptSummary'), t('kbPromptKeywords'), t('kbPromptTodo')]
  const h = chunkHeadings.value[0]
  if (h) out.push(t('kbPromptHeading', { h: h.length > 18 ? h.slice(0, 18) + '…' : h }))
  else {
    const page = chunks.value.find(c => c.page)?.page
    if (page) out.push(t('kbPromptPage', { n: page }))
  }
  return out
})

function relTime(ts?: number) {
  if (!ts) return ''
  const sec = Math.max(0, Math.floor(Date.now() / 1000 - ts))
  if (sec < 60) return t('kbAgoJust')
  if (sec < 3600) return t('kbAgoMin', { n: Math.floor(sec / 60) })
  if (sec < 86400) return t('kbAgoHour', { n: Math.floor(sec / 3600) })
  if (sec < 86400 * 7) return t('kbAgoDay', { n: Math.floor(sec / 86400) })
  return new Date(ts * 1000).toLocaleDateString()
}

function excerpt(text: string) {
  const flat = (text || '').replace(/\s+/g, ' ').trim()
  return flat.length > 220 ? flat.slice(0, 220) + '…' : flat
}

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
    // 旧库里 icon 存的是 emoji，读进来就归一成 Lucide 名，渲染与编辑共用一套值。
    libraries.value = (unwrap<KBLibrary[]>(await ListKBLibraries()) ?? []).map(lib => ({
      ...lib,
      icon: libraryIconName(lib.icon),
    }))
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
  const want = libId.value
  loading.value = true
  try {
    const list = unwrap<KBSource[]>(await ListKBSources(want)) ?? []
    // 期间切了库：旧库的列表不能盖到新库上（标题是新的、左栏还是旧文档）
    if (libId.value !== want) return
    sources.value = list
    sourcesFailed.value = false
  } catch (e) {
    if (libId.value !== want) return
    // 失败时清空并标记，避免把「加载失败」渲染成「这个库是空的」
    sources.value = []
    sourcesFailed.value = true
    reportError(e, 'kb')
  } finally {
    if (libId.value === want) loading.value = false
  }
}

async function loadStatus() {
  try {
    const st = unwrap<KBStatus>(await GetKBStatus())
    if (st) {
      // 后端只在真的在跑时才置 running；这里再用 stage 兜一道，
      // 免得旧版本后端（或事件缺失）把界面永久锁在「索引中」。
      st.running = st.running && isRunningStage(st.stage)
    }
    status.value = st
  } catch (e) {
    // 与 loadSources 一样要喊一声：只 logErr 的话，状态区停在空值上，
    // 看起来和「这个库还什么都没有」完全一样，用户不会想到是接口挂了。
    reportError(e, 'kb')
  }
}

async function loadSettings() {
  try {
    const cfg = unwrap<{ embedModel: string; embedBaseURL?: string; embedAPIKey?: string; topN: number; vectorOn: boolean }>(await GetKBSettings())
    if (cfg) {
      settings.embedModel = cfg.embedModel || ''
      settings.embedBaseURL = cfg.embedBaseURL || ''
      settings.embedAPIKey = cfg.embedAPIKey || ''
      settings.topN = cfg.topN || 5
      settings.vectorOn = cfg.vectorOn !== false
    }
  } catch (e) {
    // 读取失败要让用户知道：否则弹窗里是一份空表单，看起来就像「配置全丢了」。
    reportError(e, 'kb')
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
  // 旧库的检索结果/查询态一并作废，否则中间栏还挂着上一个库的命中，
  // 点这些旧命中时 openHit 在新库 sources 里找不到来源会毫无反应。
  searchSeq++ // 让在途的旧库检索失效，避免结果回落污染新库视图
  clearQuery()
  await loadSources()
}

function openLibraryModal(lib: KBLibrary | null) {
  libraryModal.id = lib?.id ?? ''
  // 用原始 name 而不是显示名：默认库显示的是本地化的「默认知识库」，
  // 一旦把它填进输入框再保存，就等于把中文名写进了数据库。
  libraryModal.name = lib?.name ?? ''
  libraryModal.icon = libraryIconName(lib?.icon)
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
      unwrap(await RenameKBLibrary(libraryModal.id, name, libraryModal.icon))
    } else {
      const lib = unwrap<KBLibrary>(await CreateKBLibrary(name, libraryModal.icon))
      if (lib) libId.value = lib.id
    }
    libraryModal.open = false
    await refreshAll()
  } catch (e) {
    reportError(e, 'kb')
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
    unwrap(await DeleteKBLibrary(lib.id))
    if (libId.value === lib.id) libId.value = ''
    selectedId.value = ''
    chunks.value = []
    await refreshAll()
    toast.success(t('deleted'))
  } catch (e) {
    reportError(e, 'kb')
  }
}

// 把当前文档挪到别的库。挪完刷列表：按库过滤时它就该从当前视图消失。
async function moveSelected(ev: Event) {
  const src = selectedSource.value
  const target = (ev.target as HTMLSelectElement).value
  if (!src || !target || target === src.libraryId) return
  try {
    unwrap(await MoveKBSource(src.id, target))
    await refreshAll()
    // 挪出当前视图后清掉选中：否则左栏已经找不到它，
    // 抽屉里还挂着它的分段，看着像数据错乱。
    if (libId.value && libId.value !== target) {
      selectedId.value = ''
      chunks.value = []
      previewOpen.value = false
    }
  } catch (e) {
    reportError(e, 'kb')
  }
}

// ---- 导入 ----

async function addFiles() {
  try {
    const picked = unwrap<string[]>(await SelectLocalFiles(t('kbPickFileTitle'), t('kbPickFileFilter'), KB_PICK_FILTER))
    if (!picked || !picked.length) return
    const res = unwrap<{ added: number; skipped: number; unsupported: string[]; truncated?: boolean }>(await AddKBFiles(libId.value, picked))
    if (res) {
      if (res.added > 0) toast.success(t('kbAddedCount', { n: res.added }))
      if (res.skipped > 0) toast.success(t('kbSkippedCount', { n: res.skipped }))
      if (res.truncated) toast.error(t('kbTruncatedCount', { n: 500 }))
      if (res.unsupported?.length) toast.error(t('kbUnsupported', { names: res.unsupported.slice(0, 3).join(', ') }))
    }
    await refreshAll()
  } catch (e) {
    reportError(e, 'kb')
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
    reportError(e, 'kb')
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
    reportError(e, 'kb')
  }
}

async function reindexAll() {
  try {
    unwrap(await ReindexAll())
    toast.success(t('kbReindexStarted'))
    await refreshAll()
  } catch (e) {
    reportError(e, 'kb')
  }
}

async function reindexOne(src: KBSource, ev: Event) {
  ev.stopPropagation()
  try {
    unwrap(await ReindexKBSource(src.id))
    toast.success(t('kbReindexStarted'))
    await refreshAll()
  } catch (e) {
    reportError(e, 'kb')
  }
}

async function removeOne(src: KBSource, ev: Event) {
  ev.stopPropagation()
  const ok = await toast.confirm(t('kbRemoveConfirm', { name: src.name }))
  if (!ok) return
  try {
    unwrap(await RemoveKBSource(src.id))
    if (selectedId.value === src.id) {
      selectedId.value = ''
      chunks.value = []
      previewOpen.value = false
    }
    await refreshAll()
    toast.success(t('deleted'))
  } catch (e) {
    reportError(e, 'kb')
  }
}

const indexStopping = ref(false)
async function stopIndex() {
  if (indexStopping.value) return
  indexStopping.value = true
  try {
    unwrap(await CancelKBIndex())
    toast.success(t('kbStopDone'))
  } catch (e) {
    reportError(e, 'kb.stopIndex')
  } finally {
    indexStopping.value = false
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
  previewHighlight.value = ''
  previewOpen.value = true
  await loadChunksFor(src.id)
}

let chunksSeq = 0

async function loadChunksFor(id: string) {
  const seq = ++chunksSeq
  chunksLoading.value = true
  try {
    const list = unwrap<KBChunk[]>(await ListKBChunks(id)) ?? []
    if (seq !== chunksSeq) return // 连点了别的文档，这份结果过期
    chunks.value = list
  } catch (e) {
    if (seq !== chunksSeq) return
    // 失败要清空：否则抽屉里继续显示上一篇的分段，看着像新文档的内容
    chunks.value = []
    reportError(e, 'kb')
  } finally {
    if (seq === chunksSeq) chunksLoading.value = false
  }
}

// 点检索结果：抽屉打开该文档并滚到具体那一段。
// 同一份文档已经在抽屉里就不重复拉 —— 连点五条命中只该有零次请求。
async function openHit(h: SearchHit, hl?: string) {
  const src = sources.value.find(s => s.id === h.sourceId)
  if (!src) {
    // 命中来源不在当前库（多半是切库后的旧结果，或文档已移除）：明确提示，别静默失败。
    toast.error(t('kbHitSourceGone'))
    return
  }
  // 高亮词随入口变化：从检索来就是搜索词，从回答引用来就是问题原文。
  previewHighlight.value = hl ?? query.value.trim()
  activeChunkId.value = h.chunkId
  previewOpen.value = true
  if (selectedId.value !== src.id || !chunks.value.length) {
    selectedId.value = src.id
    await loadChunksFor(src.id)
  }
  await nextTick()
  scrollToActiveChunk()
}

// 回答里的引用：带上那条回答的问题，右栏同时高亮它。
function openCitation(c: SearchHit, m: AskMessage) {
  void openHit(c, m.question ?? '')
}

// 概览里的章节：跳到该标题的第一段。
function openHeading(heading: string) {
  const c = chunks.value.find(x => (x.heading || '').trim() === heading)
  if (!c) return
  activeChunkId.value = c.id
  previewHighlight.value = ''
  previewOpen.value = true
  void nextTick(scrollToActiveChunk)
}

// 点整张片段卡也能展开/收起；先拖选了文字就不切，避免想复制却把卡片折叠了。
function onChunkClick(c: KBChunk) {
  const sel = window.getSelection?.()
  if (sel && sel.toString().trim()) return
  toggleChunk(c.id)
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
// 请求序号：检索要走向量接口（几百毫秒），没有序号的话慢的旧请求会
// 后到覆盖新结果；出错时旧结果也还挂在新查询下面，高亮词都对不上。
let searchSeq = 0

watch(query, () => {
  if (searchTimer) clearTimeout(searchTimer)
  const q = query.value.trim()
  if (q.length < 3) {
    // 关键词短到不检索时，也要作废在途请求：否则它回来会把结果填回空搜索框下面
    searchSeq++
    hits.value = []
    searched.value = false
    searching.value = false
    return
  }
  // 250ms 防抖：每次检索要走向量接口（几百毫秒），逐字触发会把请求堆成雪崩。
  searchTimer = setTimeout(() => { void runSearch() }, 250)
})

async function runSearch() {
  const q = query.value.trim()
  if (q.length < 3) return
  const seq = ++searchSeq
  searching.value = true
  try {
    const res = unwrap<{ hits: SearchHit[]; mode: string; elapsedMs: number; vecFallback: boolean }>(
      await SearchKB(q, settings.topN, libId.value),
    )
    if (seq !== searchSeq) return // 已有更新的查询，丢弃这次的结果
    hits.value = res?.hits ?? []
    searchMode.value = res?.mode ?? 'fts'
    searchElapsed.value = res?.elapsedMs ?? 0
    searchFallback.value = !!res?.vecFallback
    searched.value = true
  } catch (e) {
    if (seq === searchSeq) {
      // 只在仍是最新查询时清结果并提示：旧请求的失败不该干扰新查询的展示。
      hits.value = []
      searched.value = true
      reportError(e, 'KnowledgePage.runSearch')
    }
    logErr('kb', e)
  } finally {
    if (seq === searchSeq) searching.value = false
  }
}

// 清空检索：一并把防抖定时器掐掉，否则刚清空又会被上一次的延迟调用刷回结果。
function clearQuery() {
  if (searchTimer) clearTimeout(searchTimer)
  searchSeq++ // 作废在途检索，避免清空后结果又被填回来
  searching.value = false
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

// 从概览的推荐问题/提问框进来：切到问答页并立即发送。
function askQuestion(text: string) {
  const q = text.trim()
  if (!q || asking.value) return
  question.value = q
  void sendQuestion()
}

async function sendQuestion() {
  const q = question.value.trim()
  if (!q || asking.value) return
  messages.value.push({ role: 'user', text: q })
  const reply: AskMessage = { role: 'assistant', text: '', pending: true, question: q }
  messages.value.push(reply)
  question.value = ''
  asking.value = true
  scrollAskToBottom()
  tab.value = 'ask'

  try {
    const res = unwrap<{ answer: string; citations: SearchHit[] }>(
      await AskKB({ question: q, sourceId: scopeSourceId.value, libraryId: libId.value, streamId: STREAM_ID, topN: settings.topN }),
    )
    reply.pending = false
    reply.text = res?.answer || ''
    reply.citations = res?.citations ?? []
    if (!reply.text) reply.text = t('kbAskNoHit')
    // 「答案 → 原文」闭环：限定在单篇文档提问时，回答一落地就把右栏跳到第一条引用，
    // 用户不用自己去点引用再找段落。整库问答不抢这一下——引用可能来自别的文档。
    const first = reply.citations[0]
    if (first && askScope.value === 'doc' && first.sourceId === selectedId.value) {
      void openHit(first, q)
    }
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
  // 先等加载完成再开窗：以前 void loadSettings() 之后立刻开窗，加载失败时
  // 表单是空的（含 Key），用户点保存就把已配好的模型/Key 一起提交清空了。
  settingsLoading.value = true
  void loadSettings().finally(() => { settingsLoading.value = false })
  settingsOpen.value = true
}

async function saveSettings() {
  settingsSaving.value = true
  try {
    unwrap(await SaveKBSettings({
      embedModel: settings.embedModel.trim(),
      embedBaseURL: settings.embedBaseURL.trim(),
      // 掩码原样回传（= 不改 Key），用户清空 = 回退到激活档案。
      embedAPIKey: settings.embedAPIKey.trim(),
      topN: settings.topN,
      vectorOn: settings.vectorOn,
    }))
    settingsOpen.value = false
    toast.success(t('save'))
    await refreshAll()
  } catch (e) {
    reportError(e, 'kb')
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
    case 'embed-partial': return 'kbStatusEmbedPartial'
    default: return 'kbStatusPending'
  }
}

// 左栏一行的第二行：段数（或失败原因）+ 体积 + 更新时间。
// 以前只有段数，用户想确认「导入的是不是最新那版」时无从判断。
function docMetaLine(s: KBSource) {
  if (s.status === 'failed' && s.err) return s.err
  const parts = [
    s.chunkCount > 0 ? t('kbChunkCount', { n: s.chunkCount }) : t(statusKey(s.status)),
    formatBytes(s.size, ''),
    relTime(s.mtime),
  ].filter(Boolean)
  return parts.join(' · ')
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

// 右栏片段的同一套高亮：用 previewHighlight（搜索词 / 引用问题），
// 按片段 id 缓存，避免索引进度事件把整列长文本重算。
const chunkPartsMap = computed(() => {
  const m = new Map<string, { t: string; hit: boolean }[]>()
  const q = previewHighlight.value
  for (const c of chunks.value) m.set(c.id, highlightParts(c.text, q))
  return m
})

function chunkParts(c: KBChunk) {
  return chunkPartsMap.value.get(c.id) ?? [{ t: c.text, hit: false }]
}

const unsubscribers: Array<() => void> = []

function bindEvents() {
  unsubscribers.push(Events.On('kb:index-progress', (ev: any) => {
    const d = ev?.data ?? ev
    if (d && status.value) {
      status.value.running = isRunningStage(d.stage)
      status.value.stage = d.stage
      status.value.currentName = d.name
      status.value.done = d.done ?? 0
      status.value.total = d.total ?? 0
      if (d.stage === 'embed-partial') {
        status.value.vecPending = d.vecPending ?? 0
      }
    }
    // 终态都刷一次清单：状态点与块数都变了。embed-partial 同样是终态
    // （文档已可检索，只是部分切片没向量），不刷新的话统计一直是旧的。
    if (d && (d.stage === 'ready' || d.stage === 'failed' || d.stage === 'missing' || d.stage === 'embed-partial')) {
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
    <PageHeader>
      <template #titles>
        <h1 class="qd-page-title">
          <BookOpen :size="16" />
          {{ t('kbTitle') }}
        </h1>
      </template>
      <!-- 文档/片段统计跟在标题后面同一行：原来放在 .qd-page-titles 里是第二行，
           且 <p> 带浏览器默认上下 margin，把 46px 公共页头撑得比其它页都高。 -->
      <span class="qd-page-sub qd-page-sub-inline">{{ subtitle }}</span>
      <div class="qd-page-actions">
        <button v-if="status?.running" class="qd-btn qd-btn-danger" :disabled="indexStopping" @click="stopIndex">
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
    </PageHeader>

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

    <!-- 向量部分失败的终态提示：进度条已经消失，但不能让用户以为语义索引齐全了。 -->
    <p v-else-if="status?.stage === 'embed-partial'" class="kb-hint warn kb-partial-hint">
      <AlertCircle :size="12" />
      {{ t('kbIndexPartial', { n: status.vecPending || status.vecMissing || 0 }) }}
    </p>

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
            <component :is="libraryIconComp(lib.icon)" :size="12" class="kb-lib-icon" />
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
          <div v-if="sourcesFailed" class="kb-empty-side">
            <BookOpen :size="22" />
            <p class="kb-empty-title">{{ t('statusError') }}</p>
            <button class="qd-btn qd-btn-sm" @click="loadSources()">{{ t('statusRetry') }}</button>
          </div>
          <div v-else-if="!sources.length && !loading" class="kb-empty-side">
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
              <span class="kb-doc-meta" :class="{ bad: s.status === 'failed' }">{{ docMetaLine(s) }}</span>
            </span>
            <span class="kb-doc-actions">
              <span v-key-click class="kb-doc-reindex" :title="t('kbReindex')" :aria-label="t('kbReindex')" @click="reindexOne(s, $event)">
                <RefreshCw :size="12" />
              </span>
              <span v-key-click class="kb-doc-remove" :title="t('kbRemove')" :aria-label="t('kbRemove')" @click="removeOne(s, $event)">
                <Trash2 :size="12" />
              </span>
            </span>
            <!-- 状态点的 title 说明它是什么颜色；解析/切分/建向量时改成呼吸动画，
                 静态圆点看不出「正在干活」，只能看出「有个橙点」。 -->
            <span
              class="kb-dot"
              :class="['st-' + s.status, { 'is-busy': isRunningStage(s.status) }]"
              :title="t(statusKey(s.status))"
              :aria-label="t(statusKey(s.status))"
            ></span>
          </button>
        </div>
      </aside>

      <section class="kb-main">
        <div class="kb-tabbar">
          <QdTabs v-model="tab" :tabs="kbTabs">
            <template #tab="{ tab: item }">
              <component
                :is="item.id === 'search' ? Search : Sparkles"
                :size="13"
                class="kb-tab-icon"
                :class="{ 'kb-tab-ai': item.id === 'ask' }"
              />
              {{ item.label }}
            </template>
          </QdTabs>
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
              <QdSearchBox
                v-model="query"
                class="kb-search"
                :icon-size="14"
                :loading="searching"
                :clear-title="t('kbSearchClear')"
                :placeholder="t('kbSearchPlaceholder')"
                @clear="clearQuery"
              />
              <p v-if="query.trim().length > 0 && query.trim().length < 3" class="kb-hint">{{ t('kbSearchTooShort') }}</p>
              <p v-if="searchFallback" class="kb-hint warn">{{ t('kbSearchFallback') }}</p>
            </div>
          </div>

          <div class="kb-scroll">
            <div class="kb-pane-inner">
              <!-- 默认态：选中文档后不把中栏空着。
                   三块都是真实数据（元信息 / 章节 / 开头原文），不做假的 AI 摘要。 -->
              <div v-if="!searched" class="kb-overview">
                <template v-if="selectedSource">
                  <div class="kb-ov-head">
                    <component :is="iconFor(selectedSource.ext)" :size="18" class="kb-ov-icon" />
                    <div class="kb-ov-title">
                      <span class="kb-ov-name" :title="selectedSource.path">{{ selectedSource.name }}</span>
                      <span class="kb-ov-meta">
                        <span>{{ t('kbChunkCount', { n: selectedSource.chunkCount }) }}</span>
                        <template v-if="formatBytes(selectedSource.size, '')">
                          <span class="kb-ov-sep">·</span><span>{{ formatBytes(selectedSource.size, '') }}</span>
                        </template>
                        <template v-if="selectedSource.pageCount">
                          <span class="kb-ov-sep">·</span><span>{{ t('kbPageCount', { n: selectedSource.pageCount }) }}</span>
                        </template>
                        <template v-if="relTime(selectedSource.mtime)">
                          <span class="kb-ov-sep">·</span><span>{{ relTime(selectedSource.mtime) }}</span>
                        </template>
                      </span>
                    </div>
                  </div>

                  <div class="kb-ov-ask">
                    <MessageSquare :size="14" class="kb-ov-ask-icon" />
                    <input
                      v-model="question"
                      class="kb-ov-ask-input"
                      :placeholder="t('kbOverviewAskPlaceholder', { name: selectedSource.name })"
                      spellcheck="false"
                      @keydown.enter.prevent="askQuestion(question)"
                    />
                    <button
                      class="kb-ov-ask-send"
                      :disabled="asking || !question.trim()"
                      :title="t('kbAskSend')"
                      @click="askQuestion(question)"
                    >
                      <CornerDownLeft :size="13" />
                    </button>
                  </div>

                  <div v-if="suggestedPrompts.length" class="kb-ov-block">
                    <div class="kb-ov-label"><Lightbulb :size="12" />{{ t('kbOverviewPrompts') }}</div>
                    <div class="kb-ov-prompts">
                      <button v-for="p in suggestedPrompts" :key="p" class="kb-ov-prompt" @click="askQuestion(p)">{{ p }}</button>
                    </div>
                  </div>

                  <div v-if="chunkHeadings.length" class="kb-ov-block">
                    <div class="kb-ov-label"><ListTree :size="12" />{{ t('kbOverviewStructure') }}</div>
                    <div class="kb-ov-outline">
                      <button v-for="h in chunkHeadings" :key="h" class="kb-ov-heading" :title="h" @click="openHeading(h)">
                        {{ h }}
                      </button>
                    </div>
                  </div>

                  <div v-if="openingChunk" class="kb-ov-block">
                    <div class="kb-ov-label"><Quote :size="12" />{{ t('kbOverviewOpening') }}</div>
                    <p class="kb-ov-excerpt">{{ excerpt(openingChunk.text) }}</p>
                  </div>
                </template>

                <div v-else class="kb-empty kb-ov-none">
                  <BookOpen :size="22" />
                  <p class="kb-empty-title">{{ t('kbOverviewPick') }}</p>
                  <p class="kb-empty-desc">{{ t('kbOverviewPickHint') }}</p>
                </div>
              </div>

              <div v-if="searched && !hits.length && !searching" class="kb-empty">
                <Search :size="22" />
                <p class="kb-empty-title">{{ t('kbSearchEmpty') }}</p>
                <p class="kb-empty-desc">{{ t('kbAskNoHit') }}</p>
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
              <!-- 空对话也给出可点的问题，省掉「第一句问什么」的停顿 -->
              <div v-if="suggestedPrompts.length" class="kb-ov-prompts kb-ask-prompts">
                <button v-for="p in suggestedPrompts" :key="p" class="kb-ov-prompt" @click="askQuestion(p)">{{ p }}</button>
              </div>
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
                  @click="openCitation(c, m)"
                >
                  [#{{ ci + 1 }}] {{ c.sourceName }}<template v-if="c.page"> · {{ t('kbPageNo', { n: c.page }) }}</template>
                </button>
              </div>
            </div>
          </div>
          <div class="kb-ask-box">
            <!-- 问答范围：默认「只问当前文档」，也能一键切回整库。
                 不显式标出来的话，用户会以为问的是全部文档（或反过来）。 -->
            <div class="kb-pane-inner kb-scope-row">
              <span class="kb-scope-label">{{ t('kbAskScope') }}</span>
              <button
                class="kb-scope-chip"
                :class="{ active: askScope === 'doc' }"
                :disabled="!selectedSource"
                :title="selectedSource?.name"
                @click="askScope = 'doc'"
              >
                <FileText :size="11" />
                <span class="kb-scope-name">{{ selectedSource?.name || t('kbAskScopeNone') }}</span>
              </button>
              <button class="kb-scope-chip" :class="{ active: askScope === 'lib' }" @click="askScope = 'lib'">
                <Layers :size="11" />
                {{ t('kbAskScopeLib') }}
              </button>
            </div>
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
          <span v-if="chunks.length" class="kb-preview-sep" />
          <button class="qd-icon-btn kb-preview-close" :title="t('kbClosePreview')" @click="previewOpen = false">
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
            @click="onChunkClick(c)"
          >
            <div class="kb-chunk-head">
              <span class="kb-chunk-idx">#{{ c.idx + 1 }}</span>
              <span v-if="c.page" class="kb-chunk-pos">{{ t('kbPageNo', { n: c.page }) }}</span>
              <span v-else-if="c.heading" class="kb-chunk-pos">{{ c.heading }}</span>
              <!-- 向量标记收成一个弱化的小图标：518 字、语义这些是排查信息，
                   不该和正文抢注意力，悬停才给解释。 -->
              <span v-if="c.hasVec" class="kb-chunk-vec" :title="t('kbChunkVecHint')">
                <Sparkles :size="10" />
              </span>
              <span class="kb-chunk-size" :title="t('kbChunkChars', { n: c.charCount })">{{ t('kbChunkChars', { n: c.charCount }) }}</span>
            </div>
            <p class="kb-chunk-text" :class="{ open: isChunkExpanded(c.id) }">
              <template v-for="(part, i) in chunkParts(c)" :key="i">
                <mark v-if="part.hit" class="kb-mark">{{ part.t }}</mark>
                <template v-else>{{ part.t }}</template>
              </template>
            </p>
            <button v-if="c.text.length > 200" class="kb-chunk-more" @click.stop="toggleChunk(c.id)">
              <component :is="isChunkExpanded(c.id) ? ChevronUp : ChevronDown" :size="12" />
              {{ isChunkExpanded(c.id) ? t('kbChunkCollapse') : t('kbChunkExpand') }}
            </button>
          </article>
        </div>
      </aside>
    </div>

    <QdModal
      v-if="settingsOpen"
      :model-value="true"
      :title="t('kbSettingsTitle')"
      width="420px"
      @update:model-value="settingsOpen = $event"
    >
      <div class="kb-modal-body">
        <label class="kb-field">
          <span class="kb-field-label">{{ t('kbSetEmbedModel') }}</span>
          <input v-model="settings.embedModel" class="kb-input" :placeholder="t('kbSetEmbedModelPlaceholder')" spellcheck="false" />
          <span class="kb-field-hint">{{ t('kbSetEmbedModelHint') }}</span>
        </label>
        <label class="kb-field">
          <span class="kb-field-label">{{ t('kbSetEmbedEndpoint') }}</span>
          <input v-model="settings.embedBaseURL" class="kb-input" :placeholder="t('kbSetEmbedEndpointPlaceholder')" spellcheck="false" />
          <input v-model="settings.embedAPIKey" class="kb-input" type="password" autocomplete="off" :placeholder="t('kbSetEmbedKeyPlaceholder')" spellcheck="false" />
          <span class="kb-field-hint">{{ t('kbSetEmbedEndpointHint') }}</span>
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
             → 提示就在本弹窗里填，别把人往别处赶（否则用户会以为档案又丢了）。
             用户自己关掉语义检索时不再警告：那是他的选择，不是配置出错。 -->
        <p v-if="status?.vectorOn && !status?.embedReady" class="kb-hint warn">
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
        <!-- 配置齐全但库里一条向量都没有：这不是「不可用」，是还没建。
             此前这里什么都不说，用户搜完发现只有关键词命中，会以为模型配错了。 -->
        <p v-else-if="status?.vectorOn && stats.vecChunks === 0 && stats.chunks > 0" class="kb-hint">
          <Info :size="12" />
          {{ t('kbVecPending') }}
        </p>
      </div>
      <template #footer>
        <button class="qd-btn" @click="settingsOpen = false">{{ t('cancel') }}</button>
        <button class="qd-btn qd-btn-primary" :disabled="settingsSaving || settingsLoading" @click="saveSettings">{{ t('save') }}</button>
      </template>
    </QdModal>

    <QdModal
      v-if="libraryModal.open"
      :model-value="true"
      :title="libraryModal.id ? t('kbLibraryEdit') : t('kbLibraryNew')"
      width="360px"
      @update:model-value="libraryModal.open = $event"
    >
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
              :title="ic"
              @click="libraryModal.icon = ic"
            ><component :is="LIBRARY_ICON_COMPONENTS[ic]" :size="15" /></button>
          </div>
        </div>
      </div>
      <template #footer>
        <button class="qd-btn" @click="libraryModal.open = false">{{ t('cancel') }}</button>
        <button class="qd-btn qd-btn-primary" :disabled="librarySaving" @click="saveLibrary">{{ t('save') }}</button>
      </template>
    </QdModal>
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
  gap: var(--space-4);
  flex-shrink: 0;
  padding: var(--space-2) var(--space-6);
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
  gap: var(--space-2);
  flex-shrink: 0;
  padding: var(--space-4) var(--space-4) var(--space-3);
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
  gap: var(--space-4);
  flex-shrink: 0;
  padding: var(--space-2) var(--space-3) var(--space-2) var(--space-5);
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
  gap: var(--space-1);
}

.kb-side-list {
  flex: 1;
  overflow-y: auto;
  padding: var(--space-3);
}

/* 左栏文档筛选：文档多了才出现（见 showDocFilter）。
   和检索框同构但更矮，避免左栏顶部堆成一片输入区。 */
.kb-doc-filter {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
  margin: var(--space-3) var(--space-4) 0;
  padding: var(--space-1) var(--space-3);
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
  gap: var(--space-4);
  width: 100%;
  padding: var(--space-3) var(--space-4);
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
  gap: var(--space-1);
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
  color: var(--color-danger-ink);
}

/* 操作按钮常驻占位、靠透明度显隐。
   原来用 display:none 切换，hover 时按钮凭空插入会把文件名挤短一截，行会跳。 */
.kb-doc-actions {
  display: flex;
  align-items: center;
  gap: var(--space-1);
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
  position: relative;
  display: inline-flex;
  padding: var(--space-1);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
}

/* 图标 12px + padding 3px = 18px，透明热区再外扩 3px 到 24×24，视觉不变。 */
.kb-doc-reindex::after,
.kb-doc-remove::after {
  content: '';
  position: absolute;
  inset: -3px;
}

.kb-doc-reindex:hover {
  color: var(--color-accent);
  background: var(--color-bg-hover);
}

.kb-doc-remove:hover {
  color: var(--color-danger-ink);
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

/* 正在索引的文档：点会呼吸。静态蓝点看不出「在跑」，只像「有个状态」。
   动画只改透明度和缩放，不动颜色，和右侧进度条是一套语言。 */
.kb-dot.is-busy {
  animation: kb-dot-pulse 1.4s ease-in-out infinite;
}

@keyframes kb-dot-pulse {
  0%, 100% { opacity: 0.35; transform: scale(0.8); }
  50% { opacity: 1; transform: scale(1.2); }
}

@media (prefers-reduced-motion: reduce) {
  .kb-dot.is-busy { animation: none; }
}

/* ---- 中栏 ---- */
.kb-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.kb-tabbar {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
  min-height: 38px;
  padding: 0 var(--space-4);
  border-bottom: 1px solid var(--color-hairline);
}

/* 分段控制器：外面是凹槽，选中项是浮起来的一块。
   原来只是一圈细边 + 淡蓝底，两个词看起来像并排的文字按钮，分不出当前在哪。 */
.kb-tabbar :deep(.qd-tabs.is-segmented) {
  padding: 2px;
  border-color: transparent;
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
}

.kb-tabbar :deep(.qd-tab) {
  min-height: 26px;
  padding: 0 var(--space-5);
}

.kb-tabbar :deep(.qd-tab[aria-selected='true']) {
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  box-shadow: var(--shadow-sm);
}

.kb-tab-icon { flex-shrink: 0; }
/* 问答是 AI 能力：图标常驻主题色，选中时更亮一点，作为「这里能对话」的提示 */
.kb-tab-ai { color: var(--color-accent); opacity: 0.75; }
.kb-tabbar :deep(.qd-tab[aria-selected='true']) .kb-tab-ai { opacity: 1; }

.kb-tabs-right {
  display: flex;
  align-items: center;
  gap: var(--space-4);
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
  padding: var(--space-4) var(--space-6) var(--space-2);
}

.kb-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--space-4) var(--space-6) var(--space-6);
}

/* ---- 检索 ----
   盒子皮肤交给 QdSearchBox（无边框 / 28px / bg-tertiary），这里只留外层留白。 */
.kb-search {
  flex-shrink: 0;
}

.kb-result-count {
  margin: 0 0 var(--space-3) var(--space-1);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

/* 命中项做成轻列表而不是一叠卡片：十条结果是十个带框盒子时，
   眼睛要在框线之间反复跳，反而找不到内容。留白 + hover 底色更安静。 */
.kb-hit {
  padding: var(--space-4) var(--space-4);
  margin-bottom: var(--space-1);
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
  gap: var(--space-3);
  margin-bottom: var(--space-2);
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
  gap: var(--space-2);
  margin-left: auto;
  flex-shrink: 0;
}

.kb-tag {
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
  font-size: var(--text-2xs);
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
  color: var(--color-success-ink);
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
  padding: 0 var(--space-1);
  border-radius: var(--radius-2xs);
  background: var(--color-search-hit-bg);
  color: var(--color-text-primary);
}

/* ---- 问答 ---- */
.kb-ask-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-5) var(--space-6) var(--space-4);
}

.kb-msg {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
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
  padding: var(--space-4) var(--space-5);
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
  gap: var(--space-2);
}

.kb-cites-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-cite {
  padding: var(--space-1) var(--space-3);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-sm);
  background: var(--color-card-bg);
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-xs);
  cursor: pointer;
  transition: border-color var(--transition-fast), color var(--transition-fast);
}

.kb-cite:hover {
  border-color: var(--color-accent);
  color: var(--color-accent);
}

.kb-ask-box {
  flex-shrink: 0;
  padding: var(--space-4) var(--space-6) var(--space-5);
  border-top: 1px solid var(--color-hairline);
}

/* 问答范围：一行胶囊。默认指向当前文档，点另一个切回整库。 */
.kb-scope-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-bottom: var(--space-3);
}

.kb-scope-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-scope-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  max-width: 240px;
  height: 22px;
  padding: 0 var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-xs);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}

.kb-scope-chip:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }

.kb-scope-chip.active {
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
  color: var(--color-accent);
}

.kb-scope-chip:disabled { color: var(--color-text-disabled); cursor: default; }

.kb-scope-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-ask-inner {
  display: flex;
  align-items: flex-end;
  gap: var(--space-4);
}

.kb-ask-input {
  flex: 1;
  min-width: 0;
  resize: none;
  padding: var(--space-4) var(--space-5);
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
  /* 与中栏的分界：--color-border 在深色下几乎等于底色，亮一档才看得见 */
  border-left: 1px solid var(--color-border-light);
  background: var(--color-bg-secondary);
}

.kb-preview-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
  padding: var(--space-3) var(--space-3) var(--space-3) var(--space-5);
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

/* 「6 段」和关闭按钮原来只隔 6px，点关变点段。中间加一条分隔线并把间距拉开，
   计数也收进浅底胶囊，不再是贴着 X 的一串小字。 */
.kb-preview-count {
  flex-shrink: 0;
  padding: 1px 7px;
  border-radius: var(--radius-full);
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  font-size: var(--text-2xs);
  font-variant-numeric: tabular-nums;
}

.kb-preview-sep {
  flex-shrink: 0;
  width: 1px;
  height: 14px;
  background: var(--color-border-light);
}

.kb-preview-close { margin-left: var(--space-1); }

.kb-preview-tools {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
  padding: var(--space-3) var(--space-5);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-hairline);
}

.kb-chunk-list {
  padding: var(--space-4) var(--space-4) var(--space-6);
}

/* 片段做成独立卡片：底色一档 + 1px 边 + 圆角。以前所有片段铺在同一个面板上，
   只有一条透明边框，六段连读像一张长纸，很难看出「一段到哪结束」。 */
.kb-chunk {
  padding: var(--space-4);
  margin-bottom: var(--space-3);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  background: var(--color-card-bg);
  cursor: pointer;
  scroll-margin: var(--space-4) 0;
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}

.kb-chunk:hover { border-color: var(--color-border-light); }

/* 从检索结果/回答引用跳过来的那一段：左边一条实心竖条 + 淡底。
   只改底色在深色主题下几乎看不出，光靠边框又会被其它卡片的边淹没。 */
.kb-chunk.active {
  border-color: var(--color-accent);
  background: var(--color-accent-bg);
  box-shadow: inset 3px 0 0 var(--color-accent);
}

.kb-chunk-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-2);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-chunk-idx {
  color: var(--color-accent);
  font-variant-numeric: tabular-nums;
}

.kb-chunk-pos {
  padding: 0 var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-bg-active);
  color: var(--color-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 语义标记：弱化成一个淡色小图标，悬停给解释。
   以前是一枚蓝色「语义」标签，和正文同屏时比内容还显眼。 */
.kb-chunk-vec {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  color: var(--color-text-muted);
}

.kb-chunk-vec:hover { color: var(--color-accent); }

.kb-chunk-size {
  margin-left: auto;
  flex-shrink: 0;
  color: var(--color-text-muted);
  font-size: var(--text-2xs);
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

/* 展开全文：幽灵按钮。之前是裸文字 + 默认蓝，看着像正文里的一个链接，
   读者不知道它属于这张卡片。 */
.kb-chunk-more {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 22px;
  margin-top: var(--space-3);
  padding: 0 var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-muted);
  font-family: inherit;
  font-size: var(--text-2xs);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}

.kb-chunk-more:hover {
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  text-decoration: none;
}

/* ---- 中栏默认态（文档概览） ---- */
.kb-overview {
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
  padding-top: var(--space-2);
}

.kb-ov-head {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
}

.kb-ov-icon {
  flex-shrink: 0;
  margin-top: 2px;
  color: var(--color-accent);
}

.kb-ov-title {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  min-width: 0;
}

.kb-ov-name {
  font-size: var(--text-md);
  font-weight: 600;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-ov-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.kb-ov-sep { color: var(--color-text-muted); }

/* 概览里的提问框：和问答页的输入框同构，但更矮，避免和上方检索框叠成两块大输入区 */
.kb-ov-ask {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
}

.kb-ov-ask:focus-within {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-bg);
}

.kb-ov-ask-icon {
  flex-shrink: 0;
  color: var(--color-accent);
}

.kb-ov-ask-input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: var(--text-sm);
}

.kb-ov-ask-input::placeholder { color: var(--color-text-disabled); }

.kb-ov-ask-send {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-sm);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  cursor: pointer;
}

.kb-ov-ask-send:hover:not(:disabled) {
  background: var(--color-accent);
  color: var(--color-accent-text);
}

.kb-ov-ask-send:disabled {
  color: var(--color-text-disabled); border-color: var(--color-border);
  cursor: default;
}

.kb-ov-block {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.kb-ov-label {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-ov-prompts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.kb-ov-prompt {
  padding: 5px 11px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: var(--color-card-bg);
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-xs);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}

.kb-ov-prompt:hover {
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
  color: var(--color-accent);
}

/* 章节列表：像目录一样一列，点一条跳到右栏对应片段 */
.kb-ov-outline {
  display: flex;
  flex-direction: column;
  border-left: 1px solid var(--color-border-light);
  padding-left: var(--space-4);
  gap: var(--space-1);
}

.kb-ov-heading {
  padding: 3px 0;
  border: 0;
  background: none;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-sm);
  text-align: left;
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-ov-heading:hover { color: var(--color-accent); }

.kb-ov-excerpt {
  margin: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
  font-size: var(--text-sm);
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

.kb-ov-none { padding: var(--space-10) var(--space-6); }

/* 问答空态里的推荐问题：和概览共用一套胶囊，只多一层上边距 */
.kb-ask-prompts { margin-top: var(--space-3); max-width: 560px; }

/* ---- 空态与提示 ---- */
.kb-empty,
.kb-empty-side {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-10) var(--space-6);
  color: var(--color-text-muted);
  text-align: center;
}

/* 左栏窄，空态不需要那么大的上下留白，否则一屏只剩"这里什么都没有"。
   28 = space-8 + space-2：阶梯里没有 28 这一档，但写裸值又绕开单一来源，
   用两个 token 相加既不变视觉也不新增档位。 */
.kb-empty-side {
  padding: calc(var(--space-8) + var(--space-2)) var(--space-5);
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
  gap: var(--space-2);
  margin: var(--space-3) 0 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.kb-hint.warn {
  color: var(--color-warning-ink);
}

/* 终态的部分失败提示：跟在页头下方，和进度条占同一位置，不额外撑高页面。 */
.kb-partial-hint {
  padding: 0 var(--space-5);
  margin-bottom: var(--space-2);
}

/* 提示里的直达入口：做成文字链而不是第二个按钮——它是对警告的补充说明，
   给个实心按钮会把「保存」抢成次要操作。 */
.kb-hint-link {
  padding: 0;
  margin-left: var(--space-1);
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

/* ---- 设置 / 建库弹窗（统一走 QdModal，body 只保留内容布局） ---- */
/* 分段抽屉窄屏时要盖在中栏上，锚点仍是页面根容器。 */
.qd-page {
  position: relative;
}

.kb-modal-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}

.kb-field {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
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
  padding: var(--space-3) var(--space-4);
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
  gap: var(--space-4);
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
}

/* ---- 知识库分组 ---- */
/* 容器（单行横滚）的规则在左栏段，这里只管 chip 自身。 */

.kb-lib-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  flex-shrink: 0;
  max-width: 170px;
  padding: var(--space-1) var(--space-4);
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
  flex: none;
  display: inline-flex;
  align-items: center;
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
  color: var(--color-accent-ink);
}

.kb-lib-new {
  color: var(--color-text-muted);
}

.kb-lib-btn {
  position: relative;
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

/* 视觉尺寸保持 20×20，透明热区把可点区域扩到 24×24，避免高 DPI 点空。 */
.kb-lib-btn::after {
  content: '';
  position: absolute;
  inset: -2px;
}

.kb-lib-btn:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.kb-lib-btn.danger:hover {
  background: var(--color-danger-bg);
  color: var(--color-danger-ink);
}

.kb-hit-lib {
  padding: 0 var(--space-2);
  border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  flex-shrink: 0;
}

.kb-select {
  padding: var(--space-1) var(--space-3);
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

.kb-icon-row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
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
  color: var(--color-text-secondary);
  cursor: pointer;
}

.kb-icon-pick:hover {
  background: var(--color-bg-hover);
}

.kb-icon-pick.active {
  border-color: var(--color-accent);
  background: var(--color-accent-bg);
  color: var(--color-accent);
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
    z-index: var(--z-dropdown);
    box-shadow: var(--shadow-pop);
  }
  .kb-tabs-meta {
    display: none;
  }
}
</style>
