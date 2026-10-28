<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { ClipboardList, Search, X, RefreshCw, Download, Trash2, Image as ImageIcon, File as FileIcon, Star, Globe, Mail, Braces, Code, Phone, Tag, StickyNote, ChevronLeft, ChevronRight, ListTodo, FilePlus, Sparkles, FolderSearch, ImageDown } from '@lucide/vue'
import { CreateNoteDoc } from '../../bindings/quickdock/services/note/noteservice'
import { CreateTodo } from '../../bindings/quickdock/services/todo/todoservice'
import ClipboardAiPanel from './ClipboardAiPanel.vue'
import {
  ListClipboardEntries,
  ListClipboardEntriesByType,
  ListClipboardEntriesBySmartType,
  CopyClipboardEntry,
  PasteClipboardEntry,
  GetClipboardImageBase64,
  HideClipboardWindow,
  TogglePinClipboardEntry,
  ExportClipboard,
  ClearClipboardHistory,
  DeleteClipboardEntry,
  UpdateClipboardNote,
  CountClipboardEntries,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { Events } from '@wailsio/runtime'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import { useAIAvailability } from '../composables/useAIAvailability'
import { pinyinOf } from '../utils/pinyin'
import { filterByTag, queryPushForTag, tagPushSignature } from '../utils/clipboardFilter'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const props = withDefaults(defineProps<{
  compact?: boolean
}>(), {
  compact: false,
})

interface ClipboardEntry {
  id: string
  contentType: string
  textContent: string
  imagePath: string
  imageHash: string
  sourceApp: string
  isPinned: number
  copyCount: number
  note: string
  createdAt: number
  textType?: string
}

const entries = ref<ClipboardEntry[]>([])
const searchQuery = ref('')
// 时间过滤：all | today | week | month（默认今天）
const timeFilter = ref<'all' | 'today' | 'week' | 'month'>('today')
const loading = ref(true)
const refreshTimer = ref<number | null>(null)
const imageCache = ref<Record<string, string>>({})
const imageLoading = ref<Record<string, boolean>>({})
const listRef = ref<HTMLElement | null>(null)
const selectedIndex = ref(0)
const searchInputRef = ref<HTMLInputElement | null>(null)
const activeTag = ref<string>('all')
const observerRef = ref<IntersectionObserver | null>(null)
const IMAGE_CACHE_MAX = 60
const NOTE_INPUT = ref<string>('')
const noteEditingId = ref<string | null>(null)

function cacheImage(id: string, dataUri: string) {
  const keys = Object.keys(imageCache.value)
  if (keys.length >= IMAGE_CACHE_MAX && !imageCache.value[id]) {
    delete imageCache.value[keys[0]]
  }
  imageCache.value[id] = dataUri
  imageLoading.value[id] = false
}

// ---- 智能分类标签 ----
// 标签只需 id/label/icon：真正的匹配逻辑全部下沉到 utils/clipboardFilter.ts
// （filterByTag 按 tagId 判定，且智能分类已下推到 SQL 的 text_type 列）。
// 这里不再保留每标签的 match 回调——它和 filterByTag 是两套判定，容易再次分叉。
const tags = computed(() => [
  { id: 'all', label: t('tagAll'), icon: Tag },
  { id: 'favorite', label: t('tagFavorite'), icon: Star },
  // 图片 / 文件是 content_type 已有的两类，单独成标签直接下推到 SQL 过滤
  // （前端不再把整段历史拉回再筛）。
  { id: 'image', label: t('tagImage'), icon: ImageIcon },
  { id: 'file', label: t('tagFile'), icon: FileIcon },
  // 文本智能分类：入库时已按正则算好 text_type，筛选阶段下推到 SQL，
  // 既快又覆盖全量历史（不再受「只载入前 N 条」限制）。
  { id: 'url', label: t('tagUrl'), icon: Globe },
  { id: 'email', label: t('tagEmail'), icon: Mail },
  { id: 'json', label: t('tagJson'), icon: Braces },
  { id: 'code', label: t('tagCode'), icon: Code },
  { id: 'phone', label: t('tagPhone'), icon: Phone },
])

// 需要下推到 SQL 的类型标签与智能分类标签的判定，见 utils/clipboardFilter.ts。
// （image/file 不是 text 的子类 —— 曾经统一套了 text 前置条件，
//   导致点「图片」标签永远筛不出任何条目；智能分类下推的是 text_type 而非 content_type。）

const filteredEntries = computed(() => {
  let list = entries.value
  // 按时间过滤
  const tf = timeFilter.value
  if (tf !== 'all') {
    const now = new Date()
    const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
    const startOfWeek = (() => {
      const d = new Date(startOfToday)
      const day = (d.getDay() + 6) % 7 // 0=周一
      d.setDate(d.getDate() - day)
      return d.getTime()
    })()
    const cutoff = tf === 'today' ? startOfToday : tf === 'week' ? startOfWeek : startOfToday - 29 * 86400000
    list = list.filter(e => e.createdAt >= cutoff)
  }
  // 按标签筛选（filterByTag 内部按 tagId 判定；智能分类已下推到 SQL，这里只兜底校验）
  const tag = tags.value.find(t => t.id === activeTag.value)
  if (tag && tag.id !== 'all') {
    list = filterByTag(list, tag.id)
  }
  // 按搜索词筛选
  const q = searchQuery.value.toLowerCase().trim()
  if (q) {
    list = list.filter(e => {
      const text = e.textContent || ''
      if (text.toLowerCase().includes(q)) return true
      // 拼音命中：这是「纯过滤」列表（不像命令面板会按分数排序把弱命中压下去），
      // 若用全拼 includes 判定，输入 "de" 会命中几乎所有含「的」的记录，噪音太大。
      const parts = pinyinOf(text, 'cb:' + e.id)
      // 1) 首字母前缀：输入 hjgl 命中「环境管理…」开头，最准。
      if (parts.init.startsWith(q)) return true
      // 2) 长文本中间命中：一段日志/代码里的「环境管理」在中间时，只比开头永远搜不到。
      //    但 1~2 个字母在千字文本的首字母串里几乎必然出现（676 种组合 × 上千个位置），
      //    纯噪音，所以只有 3 个字母以上才放开到任意位置；全拼再多收一位。
      if (q.length < 3) return false
      if (parts.init.includes(q)) return true
      return q.length >= 4 && parts.full.includes(q)
    })
  }
  return list
})

// 搜索命中高亮：把 text 中命中 query 的部分切分为片段数组 [{text, hit}]
function highlight(text: string, query: string): { text: string; hit: boolean }[] {
  const q = (query || '').toLowerCase().trim()
  const out: { text: string; hit: boolean }[] = []
  if (!q || !text) return [{ text: text || '', hit: false }]
  const lower = text.toLowerCase()
  let i = 0
  while (i < text.length) {
    const idx = lower.indexOf(q, i)
    if (idx < 0) {
      if (i < text.length) out.push({ text: text.slice(i), hit: false })
      break
    }
    if (idx > i) out.push({ text: text.slice(i, idx), hit: false })
    out.push({ text: text.slice(idx, idx + q.length), hit: true })
    i = idx + q.length
  }
  return out
}

// ---- 剪贴板分页 ----
const CLIPBOARD_PAGE_SIZE = 20
// 全量载入上限：非 compact 模式不再拉整表（limit=0 会触发后端无上限分支，
// 历史量大时整段读内存）。image/file 等类型标签走 SQL 下推（见 queryPushForTag），
// 不受这个上限影响。
const CLIPBOARD_MAX_LOAD = 1000
const clipboardPage = ref(1)

// 历史总条数（不受 CLIPBOARD_MAX_LOAD 影响）。
// 标题以前写 entries.length，那只是"已载入多少"：历史有 3 万条时显示 1000，
// 用户会以为东西被删了。
const totalCount = ref(0)
async function loadTotal() {
  try {
    totalCount.value = unwrap<number>(await CountClipboardEntries()) ?? 0
  } catch {
    totalCount.value = 0 // 计数失败不影响主列表，不弹错
  }
}

const displayEntries = computed(() => {
  // 紧凑模式（快捷弹窗）不分页，显示全部
  if (props.compact) return filteredEntries.value
  const start = (clipboardPage.value - 1) * CLIPBOARD_PAGE_SIZE
  return filteredEntries.value.slice(start, start + CLIPBOARD_PAGE_SIZE)
})

const totalClipboardPages = computed(() =>
  Math.max(1, Math.ceil(filteredEntries.value.length / CLIPBOARD_PAGE_SIZE))
)

// 页码只在**筛选条件变化**时归 1。
// 绑到 filteredEntries 会让每 30 秒一次的轮询刷新（以及每次复制后的刷新）
// 把正在看的第 3 页弹回第 1 页——数据本身没变，只是重新取了一遍。
watch([activeTag, searchQuery, timeFilter], () => { clipboardPage.value = 1 })
// 但筛选结果变少时页码可能越界（比如删到只剩 1 页），这时要收回到最后一页，
// 否则会停在一个空白页上，看起来像"东西没了"。
watch(totalClipboardPages, (n) => {
  if (clipboardPage.value > n) clipboardPage.value = n
})

function selectTag(tagId: string) {
  activeTag.value = tagId
  // 保留搜索词：以前切一下分类就把刚敲的关键词清了，"先在搜索结果里换个分类"
  // 这个最自然的动作反而要重打一遍。要清空搜索用搜索框里的 ×。
  selectedIndex.value = 0
}

// 每次窗口打开时清除搜索状态
function clearSearch() {
  searchQuery.value = ''
  activeTag.value = 'all'
  timeFilter.value = 'today'
  selectedIndex.value = 0
  clipboardPage.value = 1
}

// 关闭时回到顶部（覆盖所有关闭路径）
function resetScrollOnHide() {
  listRef.value?.scrollTo(0, 0)
}

// ---- 懒加载图片 ----
function observeImage(el: HTMLElement | null, entryId: string) {
  if (!el || !observerRef.value) return
  // 如果已缓存，直接显示
  if (imageCache.value[entryId]) return
  observerRef.value.observe(el)
}

function onImageObserved(entries: IntersectionObserverEntry[]) {
  for (const entry of entries) {
    if (entry.isIntersecting) {
      const id = (entry.target as HTMLElement).dataset?.imageId
      if (id && !imageCache.value[id] && !imageLoading.value[id]) {
        imageLoading.value[id] = true
        loadImage(id)
      }
      observerRef.value?.unobserve(entry.target)
    }
  }
}

async function loadImage(id: string) {
  try {
    const b64 = unwrap<string>(await GetClipboardImageBase64(id))
    cacheImage(id, 'data:image/png;base64,' + b64)
  } catch {
    imageLoading.value[id] = false
  }
}

// 只在有图片条目时才创建 observer
const hasImages = computed(() => entries.value.some(e => e.contentType === 'image'))

watch(hasImages, (val) => {
  if (val && !observerRef.value) {
    observerRef.value = new IntersectionObserver(onImageObserved, { rootMargin: '100px' })
  }
}, { immediate: true })

let entriesLoadGen = 0
async function loadEntries() {
  const gen = ++entriesLoadGen
  try {
    const limit = props.compact ? 300 : CLIPBOARD_MAX_LOAD
    // 按当前标签把类型过滤下推到 SQL：
    //  - image/file → content_type 整类（ListClipboardEntriesByType）
    //  - 智能分类（url/email/json/code/phone）→ text_type 列（ListClipboardEntriesBySmartType，
    //    content_type 固定为 text），同样在库内过滤，既快又覆盖全量历史。
    // 这样即使目标条目排在「最近 1000 条」之外，对应标签也能查到。
    const push = queryPushForTag(activeTag.value)
    let r: any
    if (push.push === 'contentType') {
      r = await ListClipboardEntriesByType(limit, push.value)
    } else if (push.push === 'smartType') {
      r = await ListClipboardEntriesBySmartType(limit, push.value)
    } else {
      r = await ListClipboardEntries(limit)
    }
    r = unwrap(r) || []
    if (gen !== entriesLoadGen) return // 已有更新的刷新，丢弃过期响应
    // 不再预加载所有图片 — 由 IntersectionObserver 懒加载
    entries.value = r
    loading.value = false
    // 删除 / 清空 / 自动过期都会走这里刷新，总数一并跟上
    void loadTotal()
  } catch (e) {
    if (gen !== entriesLoadGen) return
    const msg = getErrorMessage(e)
    logErr('ClipboardPanel', msg)
    if (toast?.error) {
      toast.error(t('loadFailed') + ': ' + msg)
    }
  }
}

// 标签切换后若下推方式变了，必须重新查库（已载入的集合不再是所需的那一类）。
// 用「完整下推签名」（push 类型 + 值）做比较：智能分类之间互切（url↔email）虽然都下推
// smartType，但 text_type 值不同 → SQL 查询不同 → 必须重查；而 all↔favorite 都下推 none，
// 集合一致，不重查。
watch(() => {
  return tagPushSignature(queryPushForTag(activeTag.value))
}, (nv, ov) => {
  if (nv !== ov) loadEntries()
})

// ---- 文件 / 图片条目的落盘入口 ----
// 文件条目原来只能"点一下复制路径"，再自己去资源管理器粘贴定位；
// 图片条目只能看缩略图，想留一张就得截图重存。这两个动作本该一步到位。
const revealBusyId = ref('')

async function revealEntry(entry: ClipboardEntry) {
  // file 条目的 textContent 可能是多行（一次复制多个文件），定位第一个
  const first = (entry.textContent || '')
    .split(/\r?\n/)
    .map((x) => x.trim())
    .filter(Boolean)[0]
  if (!first) {
    toast.error(t('clipboardRevealNoPath'))
    return
  }
  revealBusyId.value = entry.id
  try {
    unwrap(await RevealInExplorer(first))
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    revealBusyId.value = ''
  }
}

async function saveImageAs(entry: ClipboardEntry) {
  try {
    let dataUrl = imageCache.value[entry.id]
    if (!dataUrl) {
      const b64 = unwrap<string>(await GetClipboardImageBase64(entry.id))
      dataUrl = 'data:image/png;base64,' + b64
      cacheImage(entry.id, dataUrl)
    }
    // data URL → Blob → 交给浏览器的下载通道（WebView2 会落到「下载」目录）。
    // 不用后端另开保存对话框：一条链条两个进程，且这条路径失败时无从提示。
    const blob = await (await fetch(dataUrl)).blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    // createdAt 是毫秒时间戳（不是字符串）：文件名自己拼，避免非法字符
    const d = new Date(entry.createdAt || Date.now())
    const p2 = (n: number) => String(n).padStart(2, '0')
    a.download = `clipboard-${d.getFullYear()}${p2(d.getMonth() + 1)}${p2(d.getDate())}-${p2(d.getHours())}${p2(d.getMinutes())}${p2(d.getSeconds())}.png`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    setTimeout(() => URL.revokeObjectURL(url), 0)
    toast.success(t('clipboardImageSaved'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

async function handleCopy(entry: ClipboardEntry) {
  try {
    // 两种宿主窗口的点击语义不同，不能都用「复制 + 模拟 Ctrl+V」：
    //   - 浮窗（compact）：点条目 = 选一条内容贴到刚才那个程序里，所以写入后隐藏浮窗再发 Ctrl+V；
    //   - 主窗口的剪贴板页：点条目 = 只放回剪贴板。旧实现同样隐藏窗口并发 Ctrl+V，
    //     于是这次粘贴落到了 QuickDock 自己身上（当前焦点的搜索框/输入框），
    //     用户看到的是「粘贴没反应」或「输入框被塞了东西」。
    if (props.compact) {
      // 先归零滚动避免下次打开卡顿
      listRef.value?.scrollTo(0, 0)
      unwrap(await PasteClipboardEntry(entry.id))
    } else {
      unwrap(await CopyClipboardEntry(entry.id))
      toast.success(t('copied'))
      await loadEntries()
    }
  } catch (e) {
    if (toast?.error) {
      toast.error(t('copyFailed') + ': ' + getErrorMessage(e))
    }
  }
}

async function handleTogglePin(entry: ClipboardEntry) {
  try {
    const nowPinned = unwrap<boolean>(await TogglePinClipboardEntry(entry.id))
    entry.isPinned = nowPinned ? 1 : 0
  } catch (e) {
    if (toast?.error) {
      toast.error(getErrorMessage(e))
    }
  }
}

// ---- 备注功能 ----
function openNoteEditor(entry: ClipboardEntry) {
  noteEditingId.value = entry.id
  NOTE_INPUT.value = entry.note || ''
}

function closeNoteEditor() {
  noteEditingId.value = null
  NOTE_INPUT.value = ''
}

async function saveNote(entry: ClipboardEntry) {
  if (!noteEditingId.value) return
  try {
    await UpdateClipboardNote(noteEditingId.value, NOTE_INPUT.value)
    entry.note = NOTE_INPUT.value
    noteEditingId.value = null
    NOTE_INPUT.value = ''
    toast?.success?.(t('clipboardNoteSaved'))
  } catch (e) {
    if (toast?.error) {
      toast.error(getErrorMessage(e))
    }
  }
}

async function handleDelete(entry: ClipboardEntry) {
  const ok = await toast?.confirm?.(t('confirmDeleteClipboard'))
  if (!ok) return
  try {
    unwrap(await DeleteClipboardEntry(entry.id))
    const idx = entries.value.findIndex(e => e.id === entry.id)
    if (idx >= 0) entries.value.splice(idx, 1)
    if (toast?.success) toast.success(t('clipboardEntryDeleted'))
  } catch (e) {
    if (toast?.error) toast.error(getErrorMessage(e))
  }
}

// ---- 导出 / 清空 ----
async function exportClipboard() {
  try {
    const data = unwrap<any[]>(await ExportClipboard())
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `quickdock-clipboard-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(url)
    if (toast?.success) toast.success(t('clipboardExported'))
  } catch (e) {
    if (toast?.error) toast.error(t('exportFailed') + ': ' + getErrorMessage(e))
  }
}

async function clearClipboard() {
  const ok = await toast?.confirm?.(t('confirmClearClipboard'))
  if (!ok) return
  try {
    // 后端返回 {deleted: n}：之前 unwrap 完直接丢掉，只提示一句「已清空」，
    // 用户看不出到底删了 0 条还是 3000 条（重复点、或已被过期清理时尤其困惑）。
    const res = unwrap<{ deleted: number }>(await ClearClipboardHistory())
    const n = res?.deleted ?? 0
    await loadEntries()
    if (toast?.success) toast.success(t('clipboardClearedN', { n }))
  } catch (e) {
    if (toast?.error) toast.error(getErrorMessage(e))
  }
}

// ---- 问 AI：把选中条目交给 AI 处理（翻译 / 摘要 / 格式化 / 自由提问）----

// 正在问 AI 的条目 id。空串表示没开 AI 面板。
// 用 id 而不是对象：条目列表会随剪贴板监听刷新（每 30s 一次），
// 持对象引用会在刷新后指向已失效的旧数据。
const aiEntryId = ref('')

const aiEntry = computed(() => entries.value.find((e) => e.id === aiEntryId.value) || null)

const ai = useAIAvailability()

async function openAi(entry: ClipboardEntry) {
  // 只有文本条目能问 AI：图片与文件没有可提问的内容。
  if (entry.contentType !== 'text' || !entry.textContent?.trim()) return
  // 入口前置：未配置 AI 档案时，不打开一个空面板让用户对着输入框发呆，
  // 直接跳转到设置页填 Key（跨窗口由 Go 侧负责把主窗口带上来）。
  const ok = await ai.check()
  if (!ok) {
    ai.goToConfig()
    return
  }
  aiEntryId.value = entry.id
}

function closeAi() {
  aiEntryId.value = ''
}

// ---- 一键转存：剪贴板条目 → 待办 / 笔记 ----

const saveMenuId = ref('')

function toggleSaveMenu(entry: ClipboardEntry) {
  saveMenuId.value = saveMenuId.value === entry.id ? '' : entry.id
}

function closeSaveMenu() { saveMenuId.value = '' }

// 用首行非空内容做标题，避免把整段长文本塞进待办/笔记标题
function firstLineAsTitle(text: string, max = 40): string {
  const line = (text || '').split('\n').map(s => s.trim()).find(Boolean) || ''
  return line.length > max ? line.slice(0, max) + '…' : line
}

function feedback(res: any, fallbackMsg: string) {
  if (res && res.code === 0) toast?.success?.(res.msg || fallbackMsg)
  else if (res) toast?.error?.(getErrorMessage(res))
}

async function saveAsTodo(entry: ClipboardEntry) {
  closeSaveMenu()
  if (entry.contentType !== 'text' || !entry.textContent) return
  const content = entry.textContent.trim()
  try {
    // 标题取首行，全文放备注，避免标题栏塞满长文本
    feedback(await CreateTodo(firstLineAsTitle(content), 'medium', '', content, '', '', '', '', ''), t('clipboardSavedTodo'))
  } catch (e) {
    toast?.error?.(getErrorMessage(e))
  }
}

async function saveAsNote(entry: ClipboardEntry) {
  closeSaveMenu()
  if (entry.contentType !== 'text' || !entry.textContent) return
  const content = entry.textContent.trim()
  try {
    feedback(await CreateNoteDoc('', firstLineAsTitle(content, 30), content, 'markdown'), t('clipboardSavedNote'))
  } catch (e) {
    toast?.error?.(getErrorMessage(e))
  }
}

function timeAgo(ts: number): string {
  if (!ts || ts <= 0) return ''
  const now = Date.now()
  const diff = now - ts
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return t('momentsAgo')
  if (mins < 60) return mins + t('minutesAgo')
  const hours = Math.floor(mins / 60)
  if (hours < 24) return hours + t('hoursAgo')
  const days = Math.floor(hours / 24)
  if (days < 7) return days + t('daysAgo')
  return new Date(ts).toLocaleDateString()
}

function textPreview(text: string, max = 200): string {
  if (!text) return ''
  const single = text.replace(/\s+/g, ' ')
  return single.length > max ? single.substring(0, max) + '...' : single
}

// ---- 键盘导航 ----
watch(filteredEntries, () => {
  selectedIndex.value = 0
})

// displayEntries 随分页/筛选变化。若翻到较短的末页，selectedIndex 可能超出新列表长度，
// 这里夹紧到 [0, len-1]，避免后续 list[selectedIndex] 为 undefined 而崩溃。
watch(displayEntries, (list) => {
  if (selectedIndex.value >= list.length) {
    selectedIndex.value = Math.max(0, list.length - 1)
  }
})

// 把 selectedIndex 夹紧到当前列表范围内，返回安全索引
function safeIndex(listLen: number): number {
  if (selectedIndex.value < 0) selectedIndex.value = 0
  if (selectedIndex.value >= listLen) selectedIndex.value = Math.max(0, listLen - 1)
  return selectedIndex.value
}

function scrollToSelected() {
  if (!listRef.value) return
  const items = listRef.value.querySelectorAll('.clipboard-item')
  const el = items[selectedIndex.value] as HTMLElement | undefined
  el?.scrollIntoView({ block: 'nearest' })
}

function onPanelKeydown(e: KeyboardEvent) {
  const list = displayEntries.value
  if (list.length === 0) return

  // AI 面板打开时，列表级快捷键让位给面板：
  // - Enter 应当发问，而不是把选中的剪贴板条目粘贴出去（那会覆盖用户剪贴板）；
  // - Escape 应当关闭 AI 面板回到列表，而不是直接关掉整个浮窗。
  if (aiEntryId.value) {
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      closeAi()
    }
    return
  }

  if (e.key === 'Enter' && e.target === searchInputRef.value) {
    e.preventDefault()
    e.stopPropagation()
    handleCopy(list[safeIndex(list.length)])
    return
  }

  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault()
      if (selectedIndex.value < list.length - 1) {
        selectedIndex.value++
      } else if (!props.compact && clipboardPage.value < totalClipboardPages.value) {
        // 页尾再按一次 → 翻到下一页并从第一项开始。以前是在本页内绕回第一项，
        // 有 50 页时想看到第 2 页只能去点分页按钮。
        clipboardPage.value++
        selectedIndex.value = 0
      } else {
        selectedIndex.value = 0
      }
      scrollToSelected()
      break
    case 'ArrowUp':
      e.preventDefault()
      if (selectedIndex.value > 0) {
        selectedIndex.value--
      } else if (!props.compact && clipboardPage.value > 1) {
        clipboardPage.value--
        // 落到上一页的最后一项（翻页后 displayEntries 才是新页，用 nextTick 取长度）
        nextTick(() => {
          selectedIndex.value = Math.max(0, displayEntries.value.length - 1)
          scrollToSelected()
        })
        break
      } else {
        selectedIndex.value = list.length - 1
      }
      scrollToSelected()
      break
    case 'Enter':
      e.preventDefault()
      e.stopPropagation()
      handleCopy(list[safeIndex(list.length)])
      break
    case 'Escape':
      resetScrollOnHide()
      try { HideClipboardWindow() } catch (e) { logErr('ClipboardPanel', e) }
      break
  }
}

// 事件注销句柄：必须用 On 返回的 off 逐个移除。
//
// **不要用 Events.Off('clipboard:updated')**：Wails 的 Off 是按事件名整体移除的
// （签名见 @wailsio/runtime 的 events.d.ts，只接受事件名、没有回调参数），
// CommandPalette 也监听同名事件，面板一卸载就会把它的监听一起删掉；
// 反过来命令面板卸载也会删掉面板的监听。被删掉的那一端此后收不到任何刷新，
// 只能等 30s 轮询或重新挂载——用户看到的就是「复制了，进剪贴板却没有最新内容」。
// 项目里 App.vue 的 quickdock:dsh:progress 已有同样一处教训，口径保持一致。
const clipboardEventOffs: Array<() => void> = []

onMounted(() => {
  loadEntries()
  refreshTimer.value = window.setInterval(loadEntries, 30000)
  clipboardEventOffs.push(
    Events.On('clipboard:updated', loadEntries),
    Events.On('clipboard:shown', clearSearch),
    Events.On('clipboard:before-hide', resetScrollOnHide),
  )
  document.addEventListener('keydown', onPanelKeydown)
  // 转存菜单：点击面板任意位置即收起（按钮自身用 @click.stop 拦截）
  document.addEventListener('click', closeSaveMenu)
})

onUnmounted(() => {
  document.removeEventListener('click', closeSaveMenu)
  if (refreshTimer.value !== null) {
    clearInterval(refreshTimer.value)
  }
  clipboardEventOffs.forEach((off) => off())
  clipboardEventOffs.length = 0
  document.removeEventListener('keydown', onPanelKeydown)
  observerRef.value?.disconnect()
})
</script>

<template>
  <div class="clipboard-panel">
    <!-- 头部 -->
    <div class="clipboard-header">
      <div class="header-left">
        <ClipboardList :size="18" />
        <span class="header-title">{{ t('clipboardHistoryTitle') }}</span>
        <!-- 已载入 / 总数：单写 entries.length 会被误读成"历史就这么多" -->
        <span v-if="totalCount || entries.length" class="header-count"
              :title="t('clipboardCountTip', { loaded: entries.length, total: totalCount || entries.length })">
          {{ totalCount || entries.length }}{{ entries.length < totalCount ? ' · ' + entries.length : '' }} {{ t('count') }}
        </span>
      </div>
      <div class="header-search">
        <Search :size="14" class="search-icon" />
        <input
          ref="searchInputRef"
          v-model="searchQuery"
          class="search-input"
          :placeholder="t('searchClipboard')"
        />
        <button v-if="searchQuery" class="clear-btn" @click="searchQuery = ''">
          <X :size="14" />
        </button>
      </div>
      <button class="icon-btn" @click="loadEntries" :title="t('refresh')">
        <RefreshCw :size="14" />
      </button>
      <button class="icon-btn" @click="exportClipboard" :title="t('exportClipboard')">
        <Download :size="14" />
      </button>
      <button class="icon-btn" @click="clearClipboard" :title="t('clearClipboard')">
        <Trash2 :size="14" />
      </button>
    </div>

    <!-- 智能分类标签栏 + 时间过滤（合成一行，右侧时间分段） -->
    <div class="clipboard-tags">
      <button
        v-for="tag in tags"
        :key="tag.id"
        :class="['tag-btn', { active: activeTag === tag.id }]"
        @click="selectTag(tag.id)"
      >
        <component :is="tag.icon" :size="12" />
        <span>{{ tag.label }}</span>
      </button>
      <div class="tags-divider"></div>
      <button
        v-for="tf in ([{id:'all'},{id:'today'},{id:'week'},{id:'month'}] as const)"
        :key="'time-' + tf.id"
        :class="['time-btn', { active: timeFilter === tf.id }]"
        @click="timeFilter = tf.id; selectedIndex = 0; clipboardPage = 1"
      >
        {{ t('time' + tf.id.charAt(0).toUpperCase() + tf.id.slice(1)) }}
      </button>
    </div>

    <!-- 列表 -->
    <div ref="listRef" class="clipboard-list" v-if="!loading">
      <div v-if="filteredEntries.length === 0" class="clipboard-empty">
        <ClipboardList :size="36" class="empty-icon" />
        <p class="empty-text">{{ searchQuery ? t('noMatchClipboard') : t('noClipboardRecords') }}</p>
      </div>
      <div
        v-for="(entry, idx) in displayEntries"
        :key="entry.id"
        :class="['clipboard-item', {
          'is-image': entry.contentType === 'image',
          'selected': idx === selectedIndex
        }]"
        @click="handleCopy(entry)"
        @mouseenter="selectedIndex = idx"
      >
        <div class="item-content">
          <!-- 图片条目 — 懒加载 -->
          <template v-if="entry.contentType === 'image'">
            <div class="image-thumb-wrap"
              :data-image-id="entry.id"
              :ref="(el: any) => observeImage(el as HTMLElement, entry.id)"
            >
              <img
                v-if="imageCache[entry.id]"
                :src="imageCache[entry.id]"
                class="image-thumb"
              />
              <div v-else class="image-placeholder">
                <ImageIcon :size="24" />
              </div>
            </div>
            <div v-if="entry.imageHash && entry.textContent" class="file-entry" style="margin-top:6px">
              <FileIcon :size="14" class="file-icon" />
              <div class="file-paths" style="font-size:11px">{{ entry.textContent }}</div>
            </div>
          </template>
          <!-- 文件条目 -->
          <template v-else-if="entry.contentType === 'file'">
            <div class="file-entry">
              <FileIcon :size="16" class="file-icon" />
              <div class="file-paths">{{ entry.textContent }}</div>
              <button class="file-reveal" :disabled="revealBusyId === entry.id"
                      :title="t('clipboardReveal')" @click.stop="revealEntry(entry)">
                <FolderSearch :size="13" />
              </button>
            </div>
          </template>
          <!-- 文本条目 -->
          <template v-else>
            <div class="item-text item-text-hl">
              <template v-for="(seg, si) in highlight(entry.textContent || '', searchQuery)" :key="si">
                <mark v-if="seg.hit" class="hl-hit">{{ seg.text }}</mark>
                <template v-else>{{ seg.text }}</template>
              </template>
            </div>
          </template>
          <div class="item-meta">
            <span class="item-time">{{ timeAgo(entry.createdAt) }}</span>
            <span v-if="entry.sourceApp" class="item-source">{{ entry.sourceApp }}</span>
            <span v-if="entry.copyCount > 1" class="item-count">{{ entry.copyCount }} {{ t('count') }}</span>
          </div>
          <div v-if="entry.note" class="item-note">
            <svg class="item-note-icon" xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>
            <span>{{ entry.note }}</span>
          </div>
        </div>
        <button
          v-if="entry.contentType === 'image'"
          class="note-btn"
          :title="t('clipboardSaveImage')"
          @click.stop="saveImageAs(entry)"
        >
          <ImageDown :size="13" />
        </button>
        <div class="save-wrap">
          <button
            v-if="entry.contentType === 'text'"
            class="note-btn"
            :class="{ active: saveMenuId === entry.id }"
            :title="t('clipboardSaveAs')"
            @click.stop="toggleSaveMenu(entry)"
          >
            <StickyNote :size="13" />
          </button>
          <div v-if="saveMenuId === entry.id" class="save-menu" @click.stop>
            <button class="save-menu-item" @click="saveAsTodo(entry)">
              <ListTodo :size="13" /><span>{{ t('clipboardSaveTodo') }}</span>
            </button>
            <button class="save-menu-item" @click="saveAsNote(entry)">
              <FilePlus :size="13" /><span>{{ t('clipboardSaveNote') }}</span>
            </button>
          </div>
        </div>
        <!--
          问 AI：翻译 / 摘要 / 格式化这条文本。
          与「转存」并排放在同一位置组——两个都是"对这条文本做点什么"，
          分开放会让用户在四个图标里找。
        -->
        <button
          v-if="entry.contentType === 'text'"
          class="ai-btn"
          :title="t('clipboardAskAI')"
          @click.stop="openAi(entry)"
        >
          <Sparkles :size="13" />
        </button>
        <button
          class="pin-btn"
          :class="{ pinned: entry.isPinned === 1 }"
          :title="entry.isPinned ? t('unpin') : t('pin')"
          @click.stop="handleTogglePin(entry)"
        >
          <Star :size="13" />
        </button>
        <button
          class="note-btn"
          :class="{ 'note-editing': noteEditingId === entry.id }"
          :title="t('clipboardEditNote')"
          @click.stop="openNoteEditor(entry)"
        >
          <StickyNote :size="13" />
        </button>
        <button
          class="delete-btn"
          :title="t('delete')"
          @click.stop="handleDelete(entry)"
        >
          <Trash2 :size="13" />
        </button>
      </div>
      <!-- 备注编辑弹窗 -->
      <div v-if="noteEditingId" class="note-editor-mask" @click.self="closeNoteEditor">
        <div class="note-editor">
          <div class="note-editor-head">
            <span>{{ t('clipboardEditNote') }}</span>
            <button class="icon-btn" @click="closeNoteEditor"><X :size="14" /></button>
          </div>
          <textarea
            v-model="NOTE_INPUT"
            class="note-textarea"
            :placeholder="t('clipboardNotePlaceholder')"
            rows="3"
          />
          <div class="note-editor-footer">
            <button class="cancel-btn" @click="closeNoteEditor">{{ t('cancel') }}</button>
            <button class="save-btn" @click="saveNote(entries.find(e => e.id === noteEditingId)!)">{{ t('save') }}</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 剪贴板分页（弹窗模式不显示） -->
    <div v-if="!compact && !loading && filteredEntries.length > CLIPBOARD_PAGE_SIZE" class="clipboard-pagination">
      <button class="page-btn" :disabled="clipboardPage <= 1" @click="clipboardPage--">
        <ChevronLeft :size="14" />
      </button>
      <template v-for="p in totalClipboardPages" :key="p">
        <button
          v-if="p === 1 || p === totalClipboardPages || Math.abs(p - clipboardPage) <= 1"
          :class="['page-btn', { active: p === clipboardPage }]"
          @click="clipboardPage = p"
        >{{ p }}</button>
        <span v-else-if="p === totalClipboardPages - 1 || p === 2" class="page-ellipsis">…</span>
      </template>
      <button class="page-btn" :disabled="clipboardPage >= totalClipboardPages" @click="clipboardPage++">
        <ChevronRight :size="14" />
      </button>
      <span class="page-total">{{ t('paginationTotal', { total: filteredEntries.length }) }}</span>
    </div>

    <div v-if="loading" class="clipboard-loading">
      <p>{{ t('loading') }}</p>
    </div>

    <!--
      AI 面板：覆盖在列表之上。
      放在 .clipboard-panel 内而不是 App.vue 里，因为它是**某个条目**的上下文
      （跟列表里的那一条绑定），不是全局功能。
    -->
    <ClipboardAiPanel
      v-if="aiEntry"
      :text="aiEntry.textContent"
      @close="closeAi"
    />
  </div>
</template>

<style scoped>
.clipboard-panel {
  height: 100%; display: flex; flex-direction: column;
  background: var(--color-bg-primary);
  /* AI 面板是绝对定位覆盖在列表之上的，容器必须是定位参照物。 */
  position: relative;
}

.clipboard-header {
  display: flex; align-items: center; gap: 10px;
  padding: 12px 16px; border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.header-left {
  display: flex; align-items: center; gap: 8px;
  color: var(--color-text-primary); font-size: 14px; font-weight: 500;
  white-space: nowrap;
}

.header-count {
  font-size: 11px; color: var(--color-text-muted); font-weight: 400;
  background: var(--color-bg-tertiary); padding: 2px 8px; border-radius: var(--radius-md);
}

.header-search {
  flex: 1; display: flex; align-items: center;
  position: relative; max-width: 300px;
}

.search-icon {
  position: absolute; left: 10px; color: var(--color-text-disabled);
  pointer-events: none;
}

.search-input {
  width: 100%; padding: 6px 30px 6px 32px;
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  background: var(--color-surface); color: var(--color-text-secondary); font-size: 13px;
  outline: none; font-family: inherit;
}
.search-input:focus { border-color: var(--color-accent); }
.search-input::placeholder { color: var(--color-text-disabled); }

.clear-btn {
  position: absolute; right: 6px; top: 50%; transform: translateY(-50%);
  background: none; border: none; color: var(--color-text-muted); cursor: pointer;
  padding: 4px; border-radius: var(--radius-xs); display: flex;
}
.clear-btn:hover { color: var(--color-text-secondary); background: var(--color-bg-active); }

.icon-btn {
  width: 30px; height: 30px; display: flex; align-items: center; justify-content: center;
  background: transparent; border: none; color: var(--color-text-muted); border-radius: var(--radius-sm);
  cursor: pointer; flex-shrink: 0;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.icon-btn:hover { background: var(--color-bg-hover); color: var(--color-text-secondary); }

/* 智能分类标签栏：横向滚动，允许换行；时间筛选同排展示，右侧分隔 */
.clipboard-tags {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--color-border);
  overflow-x: auto;
  flex-shrink: 0;
}
.tags-divider {
  width: 1px;
  height: 16px;
  margin: 0 4px;
  background: var(--color-border);
  flex-shrink: 0;
}

/* 时间筛选：同排的轻量分段，选中用蓝字+淡蓝底 */
.time-btn {
  padding: 3px 9px;
  border: 1px solid transparent;
  border-radius: var(--radius-lg);
  background: transparent;
  color: var(--color-text-muted);
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  white-space: nowrap;
  flex-shrink: 0;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s;
}
.time-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.time-btn.active {
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border, var(--color-accent-bg));
}

.tag-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: transparent;
  color: var(--color-text-muted);
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  white-space: nowrap;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
  flex-shrink: 0;
}

.tag-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.tag-btn.active {
  color: var(--color-accent);
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
}

/* 搜索命中高亮：明显区分 —— 主题蓝字 + 半粗 + 极淡蓝底，
   一眼能看清命中词，又不刺眼 */
.hl-hit {
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-weight: 600;
  border-radius: var(--radius-xs);
  padding: 0;
  box-decoration-break: clone;
  -webkit-box-decoration-break: clone;
}

/* 文本条目：限制高度 + 溢出省略，避免高亮显示长文本撑爆列表 */
.item-text-hl {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.clipboard-list {
  flex: 1; overflow-y: auto; padding: 8px;
}

.clipboard-item {
  display: flex; align-items: flex-start; gap: 10px;
  padding: 10px 12px; border-radius: var(--radius-md);
  cursor: pointer; transition: background 0.1s;
}
.clipboard-item:hover {
  background: var(--color-accent-bg);
  outline: 1px solid var(--color-accent-border);
  outline-offset: -1px;
}
.clipboard-item.selected {
  background: var(--color-accent-bg);
  outline: 1px solid var(--color-accent-border);
  outline-offset: -1px;
}

.pin-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: var(--radius-xs);
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity 0.15s, color 0.15s;
  display: flex; align-items: center; justify-content: center;
}
.clipboard-item:hover .pin-btn,
.clipboard-item.selected .pin-btn { opacity: 0.4; }
.pin-btn:hover { opacity: 0.8 !important; }
.pin-btn.pinned {
  opacity: 0.9; color: var(--color-star);
}
.pin-btn.pinned:hover { opacity: 1 !important; }

.save-wrap { position: relative; display: inline-flex; }
.save-menu {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: 40;
  min-width: 132px; padding: 4px;
  background: var(--color-surface-elevated);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 8px 24px rgba(0, 0, 0, .28);
}
.save-menu-item {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 6px 8px; font-size: 12px; cursor: pointer;
  background: transparent; border: 0; border-radius: calc(var(--radius-md) - 2px);
  color: var(--color-text-secondary); text-align: left;
}
.save-menu-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.note-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: var(--radius-xs);
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity 0.15s, color 0.15s;
  display: flex; align-items: center; justify-content: center;
}
.clipboard-item:hover .note-btn,
.clipboard-item.selected .note-btn { opacity: 0.4; }
.note-btn:hover { opacity: 0.8 !important; color: var(--color-accent); }

.delete-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: var(--radius-xs);
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity 0.15s, color 0.15s;
  display: flex; align-items: center; justify-content: center;
}
.clipboard-item:hover .delete-btn,
.clipboard-item.selected .delete-btn { opacity: 0.4; }
.delete-btn:hover { opacity: 0.8 !important; color: var(--color-danger); }

/* 问 AI 按钮：与固定/删除同一组（悬停才显形），
   但用 accent 色区分——它是"对内容做处理"，与"管理条目"不是一类动作。 */
.ai-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: var(--radius-xs);
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity 0.15s, color 0.15s;
  display: flex; align-items: center; justify-content: center;
}
.clipboard-item:hover .ai-btn,
.clipboard-item.selected .ai-btn { opacity: 0.4; }
.ai-btn:hover { opacity: 0.9 !important; color: var(--color-accent); }

.item-content { flex: 1; min-width: 0; }
.item-text {
  font-size: 13px; color: var(--color-text-primary); line-height: 1.5;
  word-break: break-all; margin-bottom: 4px;
}
.item-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.item-time { font-size: 11px; color: var(--color-text-disabled); }
.item-source { font-size: 11px; color: var(--color-text-muted); background: var(--color-bg-tertiary); padding: 1px 6px; border-radius: var(--radius-xs); }
.item-count { font-size: 11px; color: var(--color-accent); }

.clipboard-item.is-image { align-items: flex-start; padding: 8px 12px; }
.image-thumb-wrap {
  width: 100%; max-width: 300px; border-radius: var(--radius-sm); overflow: hidden;
  margin-bottom: 6px; border: 1px solid var(--color-border);
}
.image-thumb {
  display: block; width: 100%; height: auto;
  max-height: 180px; object-fit: contain;
  background: var(--color-bg-primary);
}
.image-placeholder {
  width: 160px; height: 100px; display: flex; align-items: center; justify-content: center;
  background: var(--color-surface); border-radius: var(--radius-sm); margin-bottom: 6px;
  border: 1px solid var(--color-border); color: var(--color-text-disabled);
}

.file-entry {
  display: flex; align-items: flex-start; gap: 8px;
  margin-bottom: 4px;
}
.file-icon { flex-shrink: 0; margin-top: 2px; color: var(--color-accent); }
.file-paths {
  flex: 1; min-width: 0;
  font-size: 12px; color: var(--color-text-secondary); line-height: 1.5;
  word-break: break-all; white-space: pre-wrap;
}
/* 在资源管理器定位：贴在路径右侧，hover 条目才显出来，不抢视觉 */
.file-reveal {
  flex-shrink: 0; display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; padding: 0;
  border: none; border-radius: var(--radius-sm); background: none;
  color: var(--color-text-disabled); cursor: pointer; opacity: 0;
  transition: opacity var(--transition-fast), background-color var(--transition-fast), color var(--transition-fast);
}
.clipboard-item:hover .file-reveal { opacity: 1; }
.file-reveal:hover { color: var(--color-accent); background: var(--color-accent-bg); }
.file-reveal:disabled { opacity: 0.4; cursor: default; }

.clipboard-empty {
  display: flex; flex-direction: column; align-items: center;
  justify-content: center; height: 100%; color: var(--color-text-disabled);
}
.clipboard-empty .empty-icon { color: var(--color-text-muted); margin-bottom: 12px; }
.clipboard-empty .empty-text { font-size: 13px; }

.clipboard-loading {
  display: flex; align-items: center; justify-content: center;
  height: 100%; color: var(--color-text-disabled); font-size: 13px;
}

/* 剪贴板分页 */
.clipboard-pagination {
  display: flex; align-items: center; justify-content: center;
  gap: 4px; padding: 8px; flex-shrink: 0;
  border-top: 1px solid var(--color-border);
}
.clipboard-pagination .page-btn {
  display: flex; align-items: center; justify-content: center;
  min-width: 26px; height: 26px;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-size: 11px; font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.clipboard-pagination .page-btn:hover:not(:disabled) { background: var(--color-bg-active); color: var(--color-text-primary); }
.clipboard-pagination .page-btn:disabled { opacity: 0.35; cursor: default; }
.clipboard-pagination .page-btn.active { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }
.clipboard-pagination .page-ellipsis { color: var(--color-text-disabled); font-size: 11px; width: 18px; text-align: center; }
.clipboard-pagination .page-total { margin-left: 6px; font-size: 11px; color: var(--color-text-muted); }

/* 备注按钮 */
.note-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: var(--radius-xs);
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity 0.15s, color 0.15s;
  display: flex; align-items: center; justify-content: center;
}
.clipboard-item:hover .note-btn,
.clipboard-item.selected .note-btn { opacity: 0.4; }
.note-btn:hover { opacity: 0.8 !important; color: var(--color-accent); }
.note-btn.note-editing { opacity: 1 !important; color: var(--color-accent); }

/* 备注编辑弹窗 */
.note-editor-mask {
  position: absolute; inset: 0; z-index: 100;
  display: flex; align-items: center; justify-content: center;
  background: var(--color-bg-overlay);
  animation: fadeIn 0.12s ease;
}
@keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }

.note-editor {
  width: 320px; max-width: 90vw;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  display: flex; flex-direction: column;
  overflow: hidden;
  animation: slideUp 0.15s ease;
}
@keyframes slideUp { from { transform: translateY(6px); opacity: 0; } to { transform: translateY(0); opacity: 1; } }

.note-editor-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 10px 14px; border-bottom: 1px solid var(--color-border);
  font-size: 13px; font-weight: 500; color: var(--color-text-primary);
}
.note-editor-head .icon-btn { width: 24px; height: 24px; }

.note-textarea {
  flex: 1; min-height: 80px; padding: 10px 14px;
  border: none; outline: none; resize: none;
  background: transparent; color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; line-height: 1.6;
}
.note-textarea::placeholder { color: var(--color-text-disabled); }

.note-editor-footer {
  display: flex; align-items: center; justify-content: flex-end;
  gap: 8px; padding: 10px 14px;
  border-top: 1px solid var(--color-border);
}
.cancel-btn, .save-btn {
  padding: 5px 14px; border-radius: var(--radius-sm);
  font-size: 12px; font-family: inherit; font-weight: 500;
  cursor: pointer; transition: background-color 0.12s, color 0.12s, border-color 0.12s;
  border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.cancel-btn:hover { background: var(--color-bg-hover); }
.save-btn {
  background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent);
}
.save-btn:hover { filter: brightness(1.1); }

/* 备注行 */
.item-note {
  display: flex; align-items: center; gap: 4px;
  font-size: 11px; color: var(--color-text-muted);
  margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.item-note-icon {
  flex-shrink: 0; opacity: 0.6;
  color: var(--color-accent);
}
.item-note span {
  overflow: hidden; text-overflow: ellipsis;
}
</style>

