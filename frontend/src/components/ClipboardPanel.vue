<script setup lang="ts">
import PageHeader from './PageHeader.vue'
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, onActivated, onDeactivated, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { KEEP_ALIVE } from '../pages/keepAlive'
defineOptions({ name: KEEP_ALIVE.clipboard })
import { ClipboardList, Search, X, RefreshCw, Download, Trash2, Image as ImageIcon, File as FileIcon, Star, Globe, Mail, Braces, Code, Phone, Tag, StickyNote, ChevronLeft, ChevronRight, ChevronDown, ListTodo, FilePlus, Sparkles, FolderSearch, ImageDown, ClipboardPaste, AlignLeft, ListPlus, Layers, Play, SkipForward, CheckSquare, Ban, Loader2, AlertCircle, MoreHorizontal, BookMarked, Copy, Check, Clock, Monitor } from '@lucide/vue'
import ClipboardAiPanel from './ClipboardAiPanel.vue'
import { clipboardCodeView } from '../utils/clipboardCode'
import '../styles/hljs-theme.css'
import {
  ListClipboardEntries,
  ListClipboardEntriesByType,
  ListClipboardEntriesBySmartType,
  SearchClipboardEntries,
  CopyClipboardEntry,
  PasteClipboardEntry,
  PasteClipboardText,
  HideWindow,
  GetClipboardImageBase64,
  HideClipboardWindow,
  TogglePinClipboardEntry,
  ExportClipboard,
  ClearClipboardHistory,
  DeleteClipboardEntry,
  UpdateClipboardNote,
  CountClipboardEntries,
  GetPasteQueue,
  SavePasteQueue,
  GetClipboardCaptureSettings,
  SetClipboardIgnoredApps,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { Events } from '@wailsio/runtime'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import { isAppLocked } from '../utils/appLock'
import { useAIAvailability } from '../composables/useAIAvailability'
import { useSaveAs } from '../composables/useSaveAs'
import { pinyinOf } from '../utils/pinyin'
import { filterByTag, queryPushForTag, tagPushSignature } from '../utils/clipboardFilter'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const sa = useSaveAs()

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
// 首次加载失败时常驻错误态（loading 复位后由模板显示「加载失败 + 重试」），
// 避免 toast 一闪而过、界面永远停在「加载中」。
const loadError = ref('')
const refreshTimer = ref<number | null>(null)
const imageCache = ref<Record<string, string>>({})
const imageLoading = ref<Record<string, boolean>>({})
const listRef = ref<HTMLElement | null>(null)
const selectedIndex = ref(0)
const searchInputRef = ref<HTMLInputElement | null>(null)
const activeTag = ref<string>('all')
// 按来源筛选：sourceApp 存的是复制时的前台窗口标题。列表里给出出现过的来源，
// 选一个就只看该来源的历史；「屏蔽此来源」把它写进已有的入库前忽略列表，
// 以后该窗口的复制不再进历史（隐私场景：密码库/Token 面板）。
const sourceFilter = ref<string>('all')
const knownSources = computed(() => {
  const set = new Set<string>()
  for (const e of entries.value) if (e.sourceApp?.trim()) set.add(e.sourceApp)
  return [...set]
})
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
//
// 分两排管理：primary 是高频类型（一行放得下），secondary 收进「更多」下拉。
// 原先 9 个标签 + 来源下拉 + 4 段时间平铺一行，窄窗口直接折成两排密密的药丸，
// 用户扫过去找不到落点；现在一排只留 7 个，其余按需展开。
const PRIMARY_TAG_IDS = ['all', 'favorite', 'text', 'image', 'file', 'url', 'code']
const SECONDARY_TAG_IDS = ['email', 'json', 'phone']
// 浮窗只有 480px：只留最常用的四个，其余（含文件/链接/代码）全部收进「更多」，
// 否则药丸会折成三排、把列表挤到只剩半屏。主窗口仍然一排 7 个。
const COMPACT_PRIMARY_TAG_IDS = ['all', 'favorite', 'text', 'image']

const allTags = computed(() => [
  { id: 'all', label: t('tagAll'), icon: Tag },
  { id: 'favorite', label: t('tagFavorite'), icon: Star },
  // text / image / file 是 content_type 里的三个整类，单独成标签直接下推到 SQL 过滤
  // （前端不再把整段历史拉回再筛）。
  { id: 'text', label: t('tagText'), icon: AlignLeft },
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

const primaryTags = computed(() => {
  const ids = props.compact ? COMPACT_PRIMARY_TAG_IDS : PRIMARY_TAG_IDS
  return allTags.value.filter(x => ids.includes(x.id))
})
const secondaryTags = computed(() => {
  // 浮窗里「更多」承接所有没进主排的标签，保证任何筛选都点得到。
  const primaryIds = props.compact ? COMPACT_PRIMARY_TAG_IDS : PRIMARY_TAG_IDS
  return props.compact
    ? allTags.value.filter(x => !primaryIds.includes(x.id))
    : allTags.value.filter(x => SECONDARY_TAG_IDS.includes(x.id))
})
/** 当前选中的是「更多」里的某个标签时，把它的名字显示在外层按钮上，避免筛选态被藏起来。 */
const activeSecondaryTag = computed(() => secondaryTags.value.find(x => x.id === activeTag.value) || null)

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
  const tag = allTags.value.find(t => t.id === activeTag.value)
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
  // 按来源筛选（前端字段，无需下推 SQL）
  if (sourceFilter.value !== 'all') {
    list = list.filter(e => e.sourceApp === sourceFilter.value)
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

// 分页条写清「第几条到第几条 / 共多少条」。
// 以前标题一个数、底部一个「共 N 条」，N 又只是当前筛选结果，
// 两个数字对不上时用户无法判断到底是没数据还是被筛掉了。
const pageFrom = computed(() =>
  filteredEntries.value.length === 0 ? 0 : (clipboardPage.value - 1) * CLIPBOARD_PAGE_SIZE + 1
)
const pageTo = computed(() =>
  Math.min(clipboardPage.value * CLIPBOARD_PAGE_SIZE, filteredEntries.value.length)
)

/** 是否处于任何筛选态（标签 / 时间 / 来源 / 搜索）。用于决定要不要报「筛出 N 条」。 */
const filterActive = computed(() =>
  activeTag.value !== 'all' ||
  timeFilter.value !== 'all' ||
  sourceFilter.value !== 'all' ||
  !!searchQuery.value.trim()
)

// 文本条目的代码预览：只对当前页的条目求值（20 条），且同一条目命中缓存，
// 滚动、勾选、30s 轮询刷新都不会重跑高亮。
const codeViews = computed<Record<string, ReturnType<typeof clipboardCodeView>>>(() => {
  const map: Record<string, ReturnType<typeof clipboardCodeView>> = {}
  for (const e of displayEntries.value) {
    if (e.contentType !== 'text') continue
    map[e.id] = clipboardCodeView(e.id, e.textContent || '', e.textType)
  }
  return map
})

// 页码只在筛选条件变化时归 1。
// 绑到 filteredEntries 会让每 30 秒一次的轮询刷新（以及每次复制后的刷新）
// 把正在看的第 3 页弹回第 1 页——数据本身没变，只是重新取了一遍。
watch([activeTag, searchQuery, timeFilter, sourceFilter], () => { clipboardPage.value = 1 })

// 屏蔽来源：把窗口标题写进已有的入库前忽略列表。沿用设置面板的同一份数据，
// 屏蔽后该来源的新复制不再入库；已入库的历史由上面的来源筛选继续可查/可删。
async function blockSource(source: string) {
  const name = source?.trim()
  if (!name) return
  try {
    const s = unwrap<{ ignoredApps?: string[] }>(await GetClipboardCaptureSettings())
    const apps = Array.isArray(s?.ignoredApps) ? [...s!.ignoredApps!] : []
    if (!apps.some(a => a.toLowerCase() === name.toLowerCase())) apps.push(name)
    unwrap(await SetClipboardIgnoredApps(JSON.stringify(apps)))
    // sourceApp 实际是复制时的前台窗口标题，网页标题一变屏蔽就失效；
    // 在成功提示里直接说明匹配口径，避免用户误以为是按应用进程屏蔽。
    toast.success(t('cbSourceBlocked', { name }) + ' · ' + t('clipBlockByTitleNote'))
    if (sourceFilter.value === name) sourceFilter.value = 'all'
  } catch (e) {
    toast.error(getErrorMessage(e)); logErr('clipboard/blockSource', e)
  }
}
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

// ---- 三个下拉（页头「更多」/ 标签「更多」/ 来源）----
// 同一时刻只允许开一个：它们位置相邻，两个浮层同时展开会互相遮挡。
const headerMenuOpen = ref(false)
const tagMenuOpen = ref(false)
const sourceMenuOpen = ref(false)
function closeTopMenus() {
  headerMenuOpen.value = false
  tagMenuOpen.value = false
  sourceMenuOpen.value = false
}
function toggleHeaderMenu() {
  const next = !headerMenuOpen.value
  closeTopMenus()
  headerMenuOpen.value = next
}
function toggleTagMenu() {
  const next = !tagMenuOpen.value
  closeTopMenus()
  tagMenuOpen.value = next
}
function toggleSourceMenu() {
  const next = !sourceMenuOpen.value
  closeTopMenus()
  sourceMenuOpen.value = next
}
/** 选中「更多」里的标签后自动收起下拉，并让外层按钮显示当前筛选名。 */
function pickTag(tagId: string) {
  selectTag(tagId)
  tagMenuOpen.value = false
}
function pickSource(name: string) {
  sourceFilter.value = name
  sourceMenuOpen.value = false
  selectedIndex.value = 0
  clipboardPage.value = 1
}

// 每次窗口打开时清除搜索状态
function clearSearch() {
  searchQuery.value = ''
  activeTag.value = 'all'
  timeFilter.value = 'today'
  selectedIndex.value = 0
  clipboardPage.value = 1
}

// 空态引导：库里有记录但被时间/标签/来源/搜索滤空时，一键还原为全量视图。
function showAllRecords() {
  searchQuery.value = ''
  activeTag.value = 'all'
  sourceFilter.value = 'all'
  timeFilter.value = 'all'
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

// baseEntries 是「无搜索词时按标签下推载入的基础集合」。
// 拼音/首字母搜索无法下推 SQL（库里没有拼音列），必须保留一份本地候选；
// 搜索态时再叠加后端全库 LIKE 的命中，两者按 id 去重，才能保证
// 「旧记录的文本命中」与「最近记录的拼音命中」同时可用。
const baseEntries = ref<ClipboardEntry[]>([])

let entriesLoadGen = 0
async function loadEntries() {
  const gen = ++entriesLoadGen
  try {
    const push = queryPushForTag(activeTag.value)
    // 收藏标签无法下推 SQL（bindings 无 isPinned 参数）：收藏态 limit=0 拉全量，
    // 再由 filterByTag 按 isPinned 过滤。否则只能在前 1000 条里筛，老收藏永远不出现。
    const limit = push.push === 'favorite' ? 0 : (props.compact ? 300 : CLIPBOARD_MAX_LOAD)
    // 按当前标签把类型过滤下推到 SQL：
    //  - image/file → content_type 整类（ListClipboardEntriesByType）
    //  - 智能分类（url/email/json/code/phone）→ text_type 列（ListClipboardEntriesBySmartType，
    //    content_type 固定为 text），同样在库内过滤，既快又覆盖全量历史。
    // 这样即使目标条目排在「最近 1000 条」之外，对应标签也能查到。
    const ct = push.push === 'contentType' ? push.value : ''
    const st = push.push === 'smartType' ? push.value : ''
    const q = searchQuery.value.trim()
    // 基础集合始终载入；有搜索词时额外发一次全库搜索，两次并发，
    // 别让「搜全库」把正常的 30s 刷新拖成串行两趟。
    const baseP = push.push === 'contentType'
      ? ListClipboardEntriesByType(limit, push.value)
      : push.push === 'smartType'
        ? ListClipboardEntriesBySmartType(limit, push.value)
        : ListClipboardEntries(limit)
    const searchP = q ? SearchClipboardEntries(limit, q, ct, st) : Promise.resolve(null)
    const [baseR, searchR] = await Promise.all([baseP, searchP])
    if (gen !== entriesLoadGen) return // 已有更新的刷新，丢弃过期响应
    const base = (unwrap(baseR as any) || []) as ClipboardEntry[]
    baseEntries.value = base
    if (!q) {
      entries.value = base
    } else {
      const hits = (searchR ? (unwrap(searchR as any) || []) : []) as ClipboardEntry[]
      const seen = new Set<string>()
      const merged: ClipboardEntry[] = []
      for (const e of [...hits, ...base]) {
        if (!seen.has(e.id)) { seen.add(e.id); merged.push(e) }
      }
      // 合并后重排：命中项不能一律压在基础集合前面，顺序仍是置顶优先 + 时间倒序。
      merged.sort((a, b) => (b.isPinned - a.isPinned) || (b.createdAt - a.createdAt))
      entries.value = merged
    }
    // 不再预加载所有图片 — 由 IntersectionObserver 懒加载
    loading.value = false
    loadError.value = ''
    // 删除 / 清空 / 自动过期都会走这里刷新，总数一并跟上
    void loadTotal()
  } catch (e) {
    if (gen !== entriesLoadGen) return
    const msg = getErrorMessage(e)
    logErr('ClipboardPanel', msg)
    // 失败也要复位 loading，否则空列表会永远停在「加载中」，错误只闪一下 toast。
    loading.value = false
    loadError.value = msg
    if (toast?.error) {
      toast.error(t('loadFailed') + ': ' + msg)
    }
  }
}

// 失败态的「重试」：显式恢复加载态再查一次（loadEntries 自身不置 loading=true）。
function retryLoad() {
  loading.value = true
  loadError.value = ''
  void loadEntries()
}

// 标签切换后若下推方式变了，必须重新查库（已载入的集合不再是所需的那一类）。
// 用「完整下推签名」（push 类型 + 值）做比较：智能分类之间互切（url↔email）虽然都下推
// smartType，但 text_type 值不同 → SQL 查询不同 → 必须重查；all↔favorite 也不同
// （favorite 拉全量），同样要重查，否则收藏态会沿用 all 的前 1000 条基础集合。
watch(() => {
  return tagPushSignature(queryPushForTag(activeTag.value))
}, (nv, ov) => {
  if (nv !== ov) loadEntries()
})

// 搜索词下推到后端：输入过程防抖（每敲一个字都发一次全库 LIKE 太浪费），
// 停止输入后立即重查。防抖期间保留上一轮命中，不清空列表以免闪一下空白。
let searchDebounce: number | null = null
watch(searchQuery, () => {
  if (searchDebounce !== null) clearTimeout(searchDebounce)
  searchDebounce = window.setTimeout(() => loadEntries(), 250)
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
      // 不再整表 loadEntries：连续复制时 ≤1000 条重载 + 图片集合重建会打断操作。
      // 只把被点这一条的 copyCount +1，其余刷新交给 30s 轮询 / 剪贴板事件。
      const target = entries.value.find(e => e.id === entry.id)
        || baseEntries.value.find(e => e.id === entry.id)
      if (target) target.copyCount = (target.copyCount || 0) + 1
    }
  } catch (e) {
    if (toast?.error) {
      toast.error(t('copyFailed') + ': ' + getErrorMessage(e))
    }
  }
}

// ---- 粘贴为…：三种格式 ----
// 转换放在前端做完，再交给 Go 的 PasteClipboardText 走同一条 Hide+Ctrl+V 链路：
// 条目正文本来就在内存里，JSON / Markdown 这类变换用 JS 最省事，Go 侧只负责
// 「写剪贴板 + 让出焦点 + 模拟粘贴」，不重复实现一遍文本处理。
type PasteFormat = 'plain' | 'markdown' | 'json'

// 单条目右侧低频操作统一收进「更多」菜单；置顶 / 删除常驻。
const moreMenuId = ref('')


// 纯文本：网页复制常夹带富文本残留标签；历史里存的是纯文本，但标签、CRLF、
// 控制字符粘到编辑器里就是一堆源码，先清一遍。
function toPlainText(text: string): string {
  return text
    .replace(/<[^>]*>/g, '')
    .replace(/\r\n?/g, '\n')
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/g, '')
}
// Markdown → 格式化文本：去标题/粗斜体/行内代码标记，链接转成「文字 (URL)」，
// 列表与引用符号保留成可读的纯文本。不引第三方渲染器：这里要的是文本，不是 HTML。
function toMarkdownText(text: string): string {
  return text
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/(^|[^*])\*([^*]+)\*/g, '$1$2')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '$1 ($2)')
    .replace(/^>\s?/gm, '')
    .replace(/\r\n?/g, '\n')
}
function toPrettyJson(text: string): string | null {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return null
  }
}

// 非浮窗入口先把主窗口也藏掉，否则焦点还在 QuickDock 的搜索框里，
// Ctrl+V 会贴到自己身上（handleCopy 在主窗口只复制不模拟粘贴就是这个原因）。
async function ensurePasteTarget() {
  if (!props.compact) unwrap(await HideWindow())
}

async function pasteAs(entry: ClipboardEntry, fmt: PasteFormat) {
  closeMoreMenu()
  if (entry.contentType !== 'text' || !entry.textContent) return
  let out = entry.textContent
  if (fmt === 'plain') out = toPlainText(out)
  else if (fmt === 'markdown') out = toMarkdownText(out)
  else {
    const pretty = toPrettyJson(out)
    if (pretty === null) {
      toast.error(t('cbNotJson')) // 不是 JSON 就按原文贴，别让误点变成什么都没发生
    } else {
      out = pretty
    }
  }
  try {
    await ensurePasteTarget()
    unwrap(await PasteClipboardText(out))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// ---- 队列式连续粘贴 ----
// 队列存后端而不是只放组件里：主窗口导航页与 Ctrl+~ 浮窗是两个 WebView 实例，
// 各持一份 ref 的话，在一边入队到另一边就看不见 —— 而「主窗口挑几条 → 浮窗里
// 逐条贴」正是这个功能最常用的走法。后端是两者唯一共用的地方。
interface PasteItem { id: string; label: string }
const pasteQueue = ref<PasteItem[]>([])
const pasteRunning = ref(false)
const pasteDone = ref(0)
const pasteTotal = ref(0)
let pasteGen = 0

// persistQueue 把当前队列写回后端并广播给另一个窗口。
// 失败只记日志：入队这个动作本身已经在界面上完成了，不该因为一次写库失败
// 就把队列回滚掉，让用户以为自己没点到。
async function persistQueue() {
  try {
    await SavePasteQueue(pasteQueue.value)
  } catch (e) {
    logErr('clipboard', e)
  }
}

// applyRemoteQueue 应用另一端窗口广播来的队列。
// 内容一致直接跳过：自己写入产生的回声若照单全收，会让列表每 300ms 无谓重建一次。
function applyRemoteQueue(items: PasteItem[]) {
  if (!Array.isArray(items)) return
  const next = items
    .map((x) => ({ id: String(x?.id ?? ''), label: String(x?.label ?? '') }))
    .filter((x) => x.id)
  if (JSON.stringify(next) === JSON.stringify(pasteQueue.value)) return
  pasteQueue.value = next
}

async function loadPasteQueue() {
  try {
    const items = unwrap<PasteItem[]>(await GetPasteQueue())
    if (items) applyRemoteQueue(items)
  } catch (e) {
    logErr('clipboard', e)
  }
}

// 两次 Hide+Ctrl+V 之间的间隔：浮窗隐藏后目标窗口要重新拿到焦点，上一条
// Ctrl+V 也要等目标程序处理完；贴得太快会出现后一条覆盖前一条、或粘到错误窗口。
// 用前端 await 逐条调度而不是把整批丢给后端，是为了让「跳过 / 清空」能在
// 运行途中直接改队列即时生效。
const PASTE_QUEUE_GAP_MS = 300
const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

function queuePreview(e: ClipboardEntry): string {
  const s = (e.textContent || '').replace(/\s+/g, ' ').trim()
  return s.length > 30 ? s.slice(0, 30) + '…' : (s || e.contentType)
}
function enqueue(items: ClipboardEntry[]) {
  const known = new Set(pasteQueue.value.map((x) => x.id))
  let added = false
  for (const e of items) {
    if (known.has(e.id)) continue
    known.add(e.id)
    pasteQueue.value.push({ id: e.id, label: queuePreview(e) })
    added = true
  }
  if (added) void persistQueue()
}

// 多选存 id 而不是对象：列表每 30s 会整体刷新，持对象会指向已失效的旧数据。
const multiSelect = ref(false)
const checkedIds = ref<string[]>([])
function toggleMultiSelect() {
  multiSelect.value = !multiSelect.value
  if (!multiSelect.value) checkedIds.value = []
}
function toggleChecked(id: string) {
  const i = checkedIds.value.indexOf(id)
  if (i >= 0) checkedIds.value.splice(i, 1)
  else checkedIds.value.push(id)
}
function enqueueChecked() {
  // 按当前展示顺序入队：多选的语义是「按看到的顺序依次贴」。
  enqueue(displayEntries.value.filter((e) => checkedIds.value.includes(e.id)))
  checkedIds.value = []
  multiSelect.value = false
}

// ---- 批量操作 ----
// 勾选后页头下方浮出批量条（已选 N 项 + 动作），而不是让用户去猜
// 右上角那个「全选」图标能干什么。
const batchBusy = ref(false)

function clearChecked() {
  checkedIds.value = []
  multiSelect.value = false
}

function selectAllPage() {
  checkedIds.value = displayEntries.value.map((e) => e.id)
}

const allPageSelected = computed(() =>
  displayEntries.value.length > 0 &&
  displayEntries.value.every((e) => checkedIds.value.includes(e.id))
)

async function batchDelete() {
  const ids = [...checkedIds.value]
  if (!ids.length || batchBusy.value) return
  const ok = await toast?.confirm?.(t('cbBatchDeleteConfirm', { n: ids.length }))
  if (!ok) return
  batchBusy.value = true
  const failed: string[] = []
  // 逐条删：后端没有批量删除接口，单条删除已是幂等操作，
  // 失败项继续尝试并统一回报，避免「删了一半就中断」。
  for (const id of ids) {
    try {
      unwrap(await DeleteClipboardEntry(id))
      const idx = entries.value.findIndex((e) => e.id === id)
      if (idx >= 0) entries.value.splice(idx, 1)
      const bidx = baseEntries.value.findIndex((e) => e.id === id)
      if (bidx >= 0) baseEntries.value.splice(bidx, 1)
    } catch {
      failed.push(id)
    }
  }
  batchBusy.value = false
  checkedIds.value = []
  if (!failed.length) toast.success(t('cbBatchDeleted', { n: ids.length }))
  else toast.error(t('cbBatchDeletePartial', { n: failed.length }))
  void loadTotal()
}

async function batchPin() {
  const ids = [...checkedIds.value]
  if (!ids.length || batchBusy.value) return
  batchBusy.value = true
  let done = 0
  for (const id of ids) {
    const entry = entries.value.find((e) => e.id === id)
    // 已经是收藏态的不再点一次：TogglePin 是开关，无脑调用会把已收藏的取消掉。
    if (entry?.isPinned === 1) { done++; continue }
    try {
      unwrap<boolean>(await TogglePinClipboardEntry(id))
      if (entry) entry.isPinned = 1
      done++
    } catch {
      // 单条失败不阻断整批
    }
  }
  batchBusy.value = false
  if (done) toast.success(t('cbBatchPinned', { n: done }))
  checkedIds.value = []
}

// ---- 条目展开 / 收起 ----
// 长文本与代码默认只露 3 行，超出部分给「展开全文」。以前整段铺满，
// 一条 500 字的记录就把后面十几条挤出视野，列表失去节奏。
const expandedIds = ref<Set<string>>(new Set())
function isExpanded(id: string): boolean {
  return expandedIds.value.has(id)
}
function toggleExpand(id: string) {
  const next = new Set(expandedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedIds.value = next
}
function isExpandable(entry: ClipboardEntry): boolean {
  if (entry.contentType !== 'text') return false
  const text = entry.textContent || ''
  if (codeViews.value[entry.id]) {
    // 代码块按行数判断：超过 6 行就要能收起
    return text.split('\n').length > 6
  }
  if (text.length > 140) return true
  return (text.match(/\n/g) || []).length >= 3
}

/** 卡片点击：多选态下是勾选，否则沿用「点一条即复制」。 */
function handleCardClick(entry: ClipboardEntry) {
  if (multiSelect.value) {
    toggleChecked(entry.id)
    return
  }
  handleCopy(entry)
}

async function runPasteQueue() {
  if (pasteRunning.value || pasteQueue.value.length === 0) return
  // 代次令牌：清空时 +1，让仍在 sleep 的旧循环醒来后直接退出，
  // 不会和新启动的循环一起消费同一条队列。
  const gen = ++pasteGen
  pasteRunning.value = true
  pasteTotal.value = pasteQueue.value.length
  pasteDone.value = 0
  try {
    await ensurePasteTarget()
    while (gen === pasteGen && pasteQueue.value.length) {
      const item = pasteQueue.value.shift()!
      // 每贴一条就同步一次：另一个窗口的队列条要跟着减少，
      // 否则浮窗上还挂着已经贴过的条目，用户会以为还要再点一次。
      void persistQueue()
      try {
        unwrap(await PasteClipboardEntry(item.id))
      } catch (e) {
        toast.error(getErrorMessage(e))
      }
      pasteDone.value++
      if (gen !== pasteGen || !pasteQueue.value.length) break
      await sleep(PASTE_QUEUE_GAP_MS)
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    pasteRunning.value = false
    pasteTotal.value = 0
  }
}
function skipNextInQueue() {
  // 运行中当前条已经贴出，跳过的是队首（下一条）；未运行则直接丢掉队首。
  if (pasteQueue.value.length) {
    pasteQueue.value.shift()
    void persistQueue()
  }
}
function clearPasteQueue() {
  pasteGen++
  pasteQueue.value = []
  pasteRunning.value = false
  pasteDone.value = 0
  pasteTotal.value = 0
  void persistQueue()
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
    unwrap(await UpdateClipboardNote(noteEditingId.value, NOTE_INPUT.value))
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

function toggleMoreMenu(entry: ClipboardEntry) {
  moreMenuId.value = moreMenuId.value === entry.id ? '' : entry.id
}
function closeMoreMenu() { moreMenuId.value = '' }

// 存为待办/笔记/知识库统一收敛到共享沉淀层 useSaveAs（标题提取、toast、徽标刷新
// 都收在一处），这里只负责「从剪贴板条目取正文」并保留现有更多菜单结构。
function saveAsTodo(entry: ClipboardEntry) {
  closeMoreMenu()
  if (entry.contentType !== 'text' || !entry.textContent) return
  void sa.saveToTodo({ content: entry.textContent })
}

function saveAsNote(entry: ClipboardEntry) {
  closeMoreMenu()
  if (entry.contentType !== 'text' || !entry.textContent) return
  void sa.saveToNote({ content: entry.textContent })
}

function saveAsKB(entry: ClipboardEntry) {
  closeMoreMenu()
  if (entry.contentType !== 'text' || !entry.textContent) return
  void sa.addToKB({ text: entry.textContent, name: entry.textContent.split('\n')[0]?.trim().slice(0, 40) })
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
  const items = listRef.value.querySelectorAll('.cb-card')
  const el = items[selectedIndex.value] as HTMLElement | undefined
  el?.scrollIntoView({ block: 'nearest' })
}

function onPanelKeydown(e: KeyboardEvent) {
  // 锁定期间这条必须让路：Enter 是「复制选中项并关闭窗口」，
  // 用户在密码框里按回车会直接把浮窗关掉（锁层挡得住视觉，挡不住这条 document 监听）。
  if (isAppLocked()) return
  // 已被更内层处理过的按键（v-key-click 的卡片会 preventDefault 后自己 click）
  // 不再重复处理，否则一次 Enter 会复制两遍。
  if (e.defaultPrevented) return
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

  // 焦点落在卡片内的按钮 / 勾选框上时，Enter 属于那个控件自己。
  // 不加这道闸，在「删除」上按回车会把选中项又复制一遍（还顺带删掉一条）。
  const target = e.target as HTMLElement | null
  if (
    (e.key === 'Enter' || e.key === ' ') &&
    target &&
    target !== searchInputRef.value &&
    target.closest('button, a[href], input, textarea, select, [contenteditable="true"]')
  ) {
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
// 不要用 Events.Off('clipboard:updated')：Wails 的 Off 是按事件名整体移除的
// （签名见 @wailsio/runtime 的 events.d.ts，只接受事件名、没有回调参数），
// CommandPalette 也监听同名事件，面板一卸载就会把它的监听一起删掉；
// 反过来命令面板卸载也会删掉面板的监听。被删掉的那一端此后收不到任何刷新，
// 只能等 30s 轮询或重新挂载——用户看到的就是「复制了，进剪贴板却没有最新内容」。
// 项目里 App.vue 的 quickdock:dsh:progress 已有同样一处教训，口径保持一致。
const clipboardEventOffs: Array<() => void> = []

// 条目「更多」菜单：点击面板任意位置即收起（按钮自身用 @click.stop 拦截）。
// 页头 / 标签 / 来源三个下拉共用同一条全局点击关闭链路。
function closeMenus() { closeMoreMenu(); closeTopMenus() }

// 本页既在 KeepAlive 白名单里（主窗口内容页），也被独立剪贴板窗口单独渲染
// （那边没有 KeepAlive）。所以所有全局监听必须跟着激活态挂/摘，且启停要幂等：
// 首次挂载时 onMounted 与 onActivated 都会触发，重复注册会出现两个 30s 轮询、
// 两次事件回调，还会在别的页面响应本页的 keydown / click。
function attachGlobal() {
  if (refreshTimer.value !== null) return
  refreshTimer.value = window.setInterval(loadEntries, 30000)
  clipboardEventOffs.push(
    Events.On('clipboard:updated', loadEntries),
    // 每次唤起再对齐一次：事件监听虽然常驻，但窗口隐藏期间的广播一旦漏掉，
    // 浮窗就会一直挂着过期的队列，直到下次入队才自愈。
    Events.On('clipboard:shown', () => { clearSearch(); void loadPasteQueue() }),
    Events.On('clipboard:before-hide', resetScrollOnHide),
    Events.On('clipboard:queue', (ev: any) => applyRemoteQueue(ev?.data ?? ev)),
  )
  document.addEventListener('keydown', onPanelKeydown)
  document.addEventListener('click', closeMenus)
}
function detachGlobal() {
  document.removeEventListener('click', closeMenus)
  document.removeEventListener('keydown', onPanelKeydown)
  if (refreshTimer.value !== null) {
    clearInterval(refreshTimer.value)
    refreshTimer.value = null
  }
  clipboardEventOffs.forEach((off) => off())
  clipboardEventOffs.length = 0
}

// stale-while-revalidate：切回来先显示上次的列表，再静默补一次。
// loadEntries 里会置 loading=true，所以模板不能再用 v-if="!loading" 把整张列表
// 藏掉（那正是「切过去先空白」的元凶），见下方 .clipboard-list 的条件。
let pageActive = false
let needsRefresh = false
function startPage() {
  if (pageActive) return
  pageActive = true
  attachGlobal()
  if (needsRefresh) {
    needsRefresh = false
    void loadEntries()
    void loadPasteQueue()
  }
}
function stopPage() {
  pageActive = false
  needsRefresh = true
  detachGlobal()
}

onMounted(() => {
  loadEntries()
  // 队列也跨窗口共享：进面板先拉一次，之后靠事件跟随另一端的改动。
  void loadPasteQueue()
  startPage()
})
onActivated(startPage)
onDeactivated(stopPage)
onUnmounted(() => {
  if (searchDebounce !== null) clearTimeout(searchDebounce)
  detachGlobal()
  observerRef.value?.disconnect()
})
</script>

<template>
  <div class="clipboard-panel" :class="{ compact }">
    <!-- 头部：与其它功能页共用 qd-page-header（标题 + 副标题在左，搜索与动作在右）。
         原来这一排是自造的 .clipboard-header（18px 图标 + 14px 标题 + 自由宽搜索 +
         三个 30px 图标），高度、内边距、按钮尺寸都和笔记/待办/监控那套 46px 页头
         对不上 —— 从别的页切过来时顶部会"换一套控件"。浮窗（compact）共用同一结构，
         只是省掉副标题（浮窗本来就窄，标题行已经说明一切）。 -->
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title">
          <ClipboardList :size="16" />
          {{ t('clipboardHistoryTitle') }}
          <!-- 只报历史总数，不再拼「总数 · 已载入」：两个数并列时没人分得清哪个是哪个。
               已载入多少交给悬停提示，日常不需要知道。 -->
          <span v-if="totalCount || entries.length" class="cb-count"
                :title="t('clipboardCountTip', { loaded: entries.length, total: totalCount || entries.length })">
            {{ t('cbTotalBadge', { n: totalCount || entries.length }) }}
          </span>
        </h2>
        <span v-if="!compact" class="qd-page-sub">{{ t('clipboardSubtitle') }}</span>
      </template>
      <div class="qd-page-fill">
        <div class="qd-input-wrap cb-search">
          <Search :size="14" />
          <input
            ref="searchInputRef"
            v-model="searchQuery"
            class="qd-input"
            :placeholder="t('searchClipboard')"
          />
          <button v-if="searchQuery" class="cb-clear" @click="searchQuery = ''">
            <X :size="13" />
          </button>
        </div>
      </div>
      <!-- 页头只留「选择 / 刷新 / 更多」三件：导出与清空是低频且危险的动作，
           摊在页头里既挤又容易误点，收进「更多」后每个动作都带文字标签。 -->
      <div class="qd-page-actions">
        <button
          class="qd-btn qd-btn-sm cb-select-toggle"
          :class="{ 'is-on': multiSelect }"
          :aria-pressed="multiSelect"
          :title="t('cbMultiSelect')"
          @click="toggleMultiSelect"
        >
          <CheckSquare :size="13" /><span class="cb-select-label">{{ t('cbMultiSelect') }}</span>
        </button>
        <button class="qd-icon-btn" @click="loadEntries" :title="t('refresh')" :aria-label="t('refresh')">
          <RefreshCw :size="14" />
        </button>
        <div class="save-wrap">
          <button
            class="qd-icon-btn"
            :class="{ active: headerMenuOpen }"
            :title="t('cbMoreActions')"
            :aria-label="t('cbMoreActions')"
            :aria-expanded="headerMenuOpen"
            @click.stop="toggleHeaderMenu"
          >
            <MoreHorizontal :size="14" />
          </button>
          <div v-if="headerMenuOpen" class="save-menu cb-head-menu" @click.stop>
            <button class="save-menu-item" @click="exportClipboard(); closeTopMenus()">
              <Download :size="13" /><span>{{ t('exportClipboard') }}</span>
            </button>
            <button class="save-menu-item danger" @click="clearClipboard(); closeTopMenus()">
              <Trash2 :size="13" /><span>{{ t('clearClipboard') }}</span>
            </button>
          </div>
        </div>
      </div>
    </PageHeader>

    <!-- 筛选栏：左边只放高频类型，右边是「筛出多少条 + 来源 + 时间」。
         低频智能分类（邮箱 / JSON / 手机号）收进「更多」下拉，整行不再折成两排药丸。 -->
    <div class="cb-filters">
      <div class="cb-filter-types">
        <button
          v-for="tag in primaryTags"
          :key="tag.id"
          :class="['tag-btn', { active: activeTag === tag.id }]"
          @click="selectTag(tag.id)"
        >
          <component :is="tag.icon" :size="12" />
          <span>{{ tag.label }}</span>
        </button>
        <div class="save-wrap">
          <button
            class="tag-btn cb-more-tag"
            :class="{ active: !!activeSecondaryTag }"
            :aria-expanded="tagMenuOpen"
            :title="t('cbMoreTags')"
            @click.stop="toggleTagMenu"
          >
            <component :is="activeSecondaryTag ? activeSecondaryTag.icon : MoreHorizontal" :size="12" />
            <span>{{ activeSecondaryTag ? activeSecondaryTag.label : t('cbMoreTags') }}</span>
            <ChevronDown :size="11" class="cb-caret" />
          </button>
          <div v-if="tagMenuOpen" class="save-menu cb-tag-menu" @click.stop>
            <button
              v-for="tag in secondaryTags"
              :key="tag.id"
              class="save-menu-item"
              :class="{ 'is-active': activeTag === tag.id }"
              @click="pickTag(tag.id)"
            >
              <component :is="tag.icon" :size="13" /><span>{{ tag.label }}</span>
              <Check v-if="activeTag === tag.id" :size="12" class="cb-menu-check" />
            </button>
          </div>
        </div>
      </div>

      <span class="cb-filters-fill" />

      <!-- 筛出多少条：只有真的在筛选时才出现，空白视图下这一个数字没有意义 -->
      <span v-if="filterActive" class="cb-filtered-count">{{ t('cbFilteredCount', { n: filteredEntries.length }) }}</span>

      <!-- 来源筛选：换成带文字的轻量下拉，原生 select 在深色主题里像一块补丁 -->
      <div v-if="knownSources.length" class="save-wrap">
        <button
          class="cb-src-btn"
          :class="{ active: sourceFilter !== 'all' }"
          :title="t('cbSourceFilter')"
          :aria-expanded="sourceMenuOpen"
          @click.stop="toggleSourceMenu"
        >
          <Monitor :size="12" />
          <span class="cb-src-name">{{ sourceFilter === 'all' ? t('cbSourceAll') : sourceFilter }}</span>
          <ChevronDown :size="11" />
        </button>
        <div v-if="sourceMenuOpen" class="save-menu cb-src-menu" @click.stop>
          <button class="save-menu-item" :class="{ 'is-active': sourceFilter === 'all' }" @click="pickSource('all')">
            <span class="cb-src-menu-name">{{ t('cbSourceAll') }}</span>
            <Check v-if="sourceFilter === 'all'" :size="12" class="cb-menu-check" />
          </button>
          <button
            v-for="s in knownSources"
            :key="s"
            class="save-menu-item"
            :class="{ 'is-active': sourceFilter === s }"
            @click="pickSource(s)"
          >
            <span class="cb-src-menu-name" :title="s">{{ s }}</span>
            <Check v-if="sourceFilter === s" :size="12" class="cb-menu-check" />
          </button>
        </div>
      </div>

      <div class="cb-time">
        <button
          v-for="tf in ([{id:'all'},{id:'today'},{id:'week'},{id:'month'}] as const)"
          :key="'time-' + tf.id"
          :class="['time-btn', { active: timeFilter === tf.id }]"
          @click="timeFilter = tf.id; selectedIndex = 0; clipboardPage = 1"
        >
          {{ t('time' + tf.id.charAt(0).toUpperCase() + tf.id.slice(1)) }}
        </button>
      </div>
    </div>

    <!-- 批量操作条：勾了才会出现，位置紧贴筛选栏下方（视线不用回头找页头） -->
    <div v-if="checkedIds.length" class="cb-batch">
      <span class="cb-batch-count">{{ t('cbBatchSelected', { n: checkedIds.length }) }}</span>
      <button class="cb-batch-btn" :disabled="batchBusy" @click="batchPin">
        <Star :size="12" />{{ t('cbBatchPin') }}
      </button>
      <button class="cb-batch-btn" :disabled="batchBusy" @click="enqueueChecked">
        <ListPlus :size="12" />{{ t('cbBatchEnqueue', { n: checkedIds.length }) }}
      </button>
      <button class="cb-batch-btn danger" :disabled="batchBusy" @click="batchDelete">
        <Trash2 :size="12" />{{ t('cbBatchDelete') }}
      </button>
      <span class="cb-batch-fill" />
      <button class="cb-batch-btn" :disabled="allPageSelected" @click="selectAllPage">{{ t('cbSelectAll') }}</button>
      <button class="cb-batch-btn" @click="clearChecked">{{ t('cbBatchCancel') }}</button>
    </div>

    <!-- 粘贴队列：入队后常驻，运行中显示进度与跳过/清空。放在标签栏下方是让它在
         列表之上始终可见——浮窗隐藏后用户重新唤起即可操作队列。 -->
    <div v-if="pasteQueue.length || pasteRunning" class="cb-queue-bar">
      <Layers :size="13" class="cb-queue-icon" />
      <span class="cb-queue-title">{{ t('cbQueueTitle') }}</span>
      <span class="cb-queue-status">
        {{ pasteRunning ? t('cbQueueRunning', { done: pasteDone, total: pasteTotal }) : t('cbQueueCount', { n: pasteQueue.length }) }}
      </span>
      <div class="cb-queue-chips">
        <span v-for="(it, i) in pasteQueue.slice(0, 3)" :key="it.id" class="cb-queue-chip" :title="it.label">{{ i + 1 }}. {{ it.label }}</span>
        <span v-if="pasteQueue.length > 3" class="cb-queue-more">+{{ pasteQueue.length - 3 }}</span>
      </div>
      <div class="cb-queue-actions">
        <button class="qd-icon-btn" :disabled="!pasteQueue.length || pasteRunning" :title="t('cbQueueStart')" @click="runPasteQueue"><Play :size="14" /></button>
        <button class="qd-icon-btn" :disabled="!pasteQueue.length" :title="t('cbQueueSkip')" @click="skipNextInQueue"><SkipForward :size="14" /></button>
        <button class="qd-icon-btn" :disabled="!pasteQueue.length && !pasteRunning" :title="t('clear')" @click="clearPasteQueue"><Trash2 :size="14" /></button>
      </div>
    </div>

    <!-- 列表 -->
    <!-- 只在「首次加载、还没有任何数据」时让位给加载占位；后台刷新期间
         loading 也为 true，但列表必须继续显示上一次的条目（SWR）。 -->
    <div ref="listRef" class="clipboard-list" :class="{ 'is-empty': filteredEntries.length === 0 }" v-if="entries.length > 0 || (!loading && !loadError)">
      <div v-if="filteredEntries.length === 0" class="clipboard-empty">
        <ClipboardList :size="20" class="empty-icon" />
        <!-- 区分「库里真的一条都没有」与「有记录但被当前筛选滤空」，后者给出去掉筛选的入口 -->
        <p v-if="entries.length === 0" class="empty-text">{{ t('noClipboardRecords') }}</p>
        <template v-else>
          <p class="empty-text">{{ searchQuery ? t('noMatchClipboard') : t('clipNoRecordsFiltered') }}</p>
          <button class="qd-btn qd-btn-sm" @click="showAllRecords">{{ t('clipSwitchToAll') }}</button>
        </template>
      </div>
      <!-- 卡片：文本/文件整行通栏，图片进两三列的网格（统一 4:3 裁切），
           不再让宽度不一的图片各自撑出参差的左边界。 -->
      <article
        v-for="(entry, idx) in displayEntries"
        :key="entry.id"
        v-key-click
        :class="['cb-card', {
          'is-image': entry.contentType === 'image',
          'is-code': !!codeViews[entry.id],
          'selected': idx === selectedIndex,
          'checked': checkedIds.includes(entry.id),
          'multi': multiSelect
        }]"
        @click="handleCardClick(entry)"
        @mouseenter="selectedIndex = idx"
      >
        <label v-if="multiSelect" class="cb-check" @click.stop>
          <input type="checkbox" :checked="checkedIds.includes(entry.id)" @change="toggleChecked(entry.id)" />
        </label>

        <!-- 图片条目 — 懒加载 -->
        <template v-if="entry.contentType === 'image'">
          <div
            class="cb-img"
            :data-image-id="entry.id"
            :ref="(el: any) => observeImage(el as HTMLElement, entry.id)"
          >
            <img
              v-if="imageCache[entry.id]"
              :src="imageCache[entry.id]"
              class="cb-img-el"
              alt=""
            />
            <div v-else class="cb-img-ph">
              <ImageIcon :size="22" />
            </div>
          </div>
          <div v-if="entry.imageHash && entry.textContent" class="cb-img-path" :title="entry.textContent">
            {{ entry.textContent }}
          </div>
        </template>

        <!-- 文件条目 -->
        <template v-else-if="entry.contentType === 'file'">
          <div class="cb-file">
            <FileIcon :size="15" class="cb-file-icon" />
            <div class="cb-file-paths">{{ entry.textContent }}</div>
            <button class="cb-file-reveal" :disabled="revealBusyId === entry.id"
                    :title="t('clipboardReveal')" @click.stop="revealEntry(entry)">
              <FolderSearch :size="13" />
            </button>
          </div>
        </template>

        <!-- 文本条目：识别成 JSON / 代码的走高亮块，其余按可展开的段落 -->
        <template v-else>
          <pre
            v-if="codeViews[entry.id]"
            class="cb-code hljs"
            :class="{ 'is-open': isExpanded(entry.id) }"
            v-html="codeViews[entry.id]?.html"
          ></pre>
          <div v-else class="cb-text" :class="{ 'is-open': isExpanded(entry.id) }">
            <template v-for="(seg, si) in highlight(entry.textContent || '', searchQuery)" :key="si">
              <mark v-if="seg.hit" class="hl-hit">{{ seg.text }}</mark>
              <template v-else>{{ seg.text }}</template>
            </template>
          </div>
          <button v-if="isExpandable(entry)" class="cb-expand" @click.stop="toggleExpand(entry.id)">
            <span>{{ isExpanded(entry.id) ? t('cbCollapse') : t('cbExpand') }}</span>
            <ChevronDown :size="11" :class="['cb-expand-caret', { up: isExpanded(entry.id) }]" />
          </button>
        </template>

        <!-- 元数据：图标 + 提亮一档的次要色。原来整行是 disabled 灰，
             在深色底上几乎和背景糊在一起。 -->
        <div class="cb-meta">
          <span class="cb-meta-item">
            <Clock :size="11" />{{ timeAgo(entry.createdAt) }}
          </span>
          <span v-if="codeViews[entry.id]" class="cb-meta-item cb-meta-lang">{{ codeViews[entry.id]?.lang }}</span>
          <span v-if="entry.sourceApp" class="cb-meta-item cb-meta-src">
            <Monitor :size="11" />
            <span class="cb-meta-src-text" :title="entry.sourceApp">{{ entry.sourceApp }}</span>
            <button class="cb-source-block" :title="t('cbSourceBlock', { name: entry.sourceApp })"
                    :aria-label="t('cbSourceBlock', { name: entry.sourceApp })"
                    @click.stop="blockSource(entry.sourceApp)"><Ban :size="10" /></button>
          </span>
          <span v-if="entry.copyCount > 1" class="cb-meta-item cb-meta-count">{{ entry.copyCount }} {{ t('count') }}</span>
        </div>
        <div v-if="entry.note" class="item-note">
          <StickyNote :size="12" class="item-note-icon" />
          <span>{{ entry.note }}</span>
        </div>

        <!-- 收藏标记：操作栏收起时也能看出这条是收藏过的，悬停后让位给操作栏 -->
        <span v-if="entry.isPinned === 1" class="cb-pin-flag" :title="t('pin')"><Star :size="10" /></span>

        <!-- 悬停操作栏：半透明浮层压在卡片右上角，不再挤在内容行尾部 -->
        <div class="cb-actions" @click.stop>
          <button
            class="cb-act"
            :class="{ on: entry.isPinned === 1 }"
            :title="entry.isPinned ? t('unpin') : t('pin')"
            :aria-label="entry.isPinned ? t('unpin') : t('pin')"
            @click="handleTogglePin(entry)"
          >
            <Star :size="13" />
          </button>
          <button class="cb-act" :title="t('cbCopyAction')" :aria-label="t('cbCopyAction')" @click="handleCopy(entry)">
            <Copy :size="13" />
          </button>
          <div class="save-wrap">
            <button
              class="cb-act"
              :class="{ on: moreMenuId === entry.id }"
              :title="[t('clipboardSaveAs'), t('cbPasteAs'), t('cbQueueAdd'), t('clipboardAskAI'), t('clipboardEditNote')].join(' / ')"
              @click.stop="toggleMoreMenu(entry)"
            >
              <MoreHorizontal :size="13" />
            </button>
            <div v-key-click v-if="moreMenuId === entry.id" class="save-menu item-more-menu" @click.stop>
              <button
                v-if="entry.contentType === 'image'"
                class="save-menu-item"
                @click="saveImageAs(entry); closeMoreMenu()"
              ><ImageDown :size="13" /><span>{{ t('clipboardSaveImage') }}</span></button>
              <template v-if="entry.contentType === 'text'">
                <button class="save-menu-item" @click="saveAsTodo(entry); closeMoreMenu()">
                  <ListTodo :size="13" /><span>{{ t('clipboardSaveTodo') }}</span>
                </button>
                <button class="save-menu-item" @click="saveAsNote(entry); closeMoreMenu()">
                  <FilePlus :size="13" /><span>{{ t('clipboardSaveNote') }}</span>
                </button>
                <button class="save-menu-item" @click="saveAsKB(entry); closeMoreMenu()">
                  <BookMarked :size="13" /><span>{{ t('saveAsKB') }}</span>
                </button>
                <button class="save-menu-item" @click="pasteAs(entry, 'plain'); closeMoreMenu()">
                  <AlignLeft :size="13" /><span>{{ t('cbPastePlain') }}</span>
                </button>
                <button class="save-menu-item" @click="pasteAs(entry, 'markdown'); closeMoreMenu()">
                  <Code :size="13" /><span>{{ t('cbPasteMarkdown') }}</span>
                </button>
                <button class="save-menu-item" @click="pasteAs(entry, 'json'); closeMoreMenu()">
                  <Braces :size="13" /><span>{{ t('cbPasteJson') }}</span>
                </button>
                <button class="save-menu-item" @click="openAi(entry); closeMoreMenu()">
                  <Sparkles :size="13" /><span>{{ t('clipboardAskAI') }}</span>
                </button>
              </template>
              <button class="save-menu-item" @click="enqueue([entry]); closeMoreMenu()">
                <ListPlus :size="13" /><span>{{ t('cbQueueAdd') }}</span>
              </button>
              <button class="save-menu-item" @click="openNoteEditor(entry); closeMoreMenu()">
                <StickyNote :size="13" /><span>{{ t('clipboardEditNote') }}</span>
              </button>
            </div>
          </div>
          <button class="cb-act danger" :title="t('delete')" :aria-label="t('delete')" @click="handleDelete(entry)">
            <Trash2 :size="13" />
          </button>
        </div>
      </article>
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
    <!-- 同列表：后台静默刷新时 loading 也会置 true，拿它做条件会让分页条闪一下。 -->
    <div v-if="!compact && filteredEntries.length > CLIPBOARD_PAGE_SIZE" class="clipboard-pagination">
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
      <span class="page-total">{{ t('cbPageRange', { from: pageFrom, to: pageTo, total: filteredEntries.length }) }}</span>
    </div>

    <div v-if="loading && !entries.length" class="clipboard-loading">
      <Loader2 :size="22" class="clipboard-spin" />
      <p>{{ t('loading') }}</p>
    </div>
    <div v-else-if="loadError && !entries.length" class="clipboard-loading clipboard-load-error">
      <AlertCircle :size="24" />
      <p>{{ t('loadFailed') }}：{{ loadError }}</p>
      <button class="qd-btn qd-btn-sm" @click="retryLoad"><RefreshCw :size="13" />{{ t('retry') }}</button>
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

/* 顶部页头不再自造：结构走 styles/app.css 的 .qd-page-header / .qd-page-fill /
   .qd-page-actions，搜索框走 .qd-input-wrap + .qd-input，图标按钮走 .qd-icon-btn
   —— 与笔记 / 待办 / 监控页完全同一套。下面只补本页特有的三处细节。 */

/* 条数徽标：与插件页 .plugin-count 同款（10px、tertiary 底、极小圆角），
   挂在标题后面当提示，不跟标题的字号字重抢戏。 */
.cb-count {
  font-size: var(--text-2xs); font-weight: 400; color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 1px 7px; border-radius: var(--radius-xs);
}

/* 搜索框：页头里不铺满整行，封顶 260px —— 窗口拉宽时搜索框不该吃掉整条页头，
   右侧三个图标按钮也要保持贴着右缘。 */
.cb-search { flex: 0 1 260px; min-width: 140px; }
.cb-search > .qd-input { padding-right: 26px; }

/* 清空按钮：压在输入框右内侧，比 .qd-icon-btn 再小一号 */
.cb-clear {
  position: absolute; right: 4px; top: 50%; transform: translateY(-50%);
  display: flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; padding: 0;
  background: none; border: none; border-radius: var(--radius-xs);
  color: var(--color-text-muted); cursor: pointer;
}
.cb-clear:hover { color: var(--color-text-secondary); background: var(--color-bg-hover); }

/* 浮窗（compact）只有 480px 宽（clipWinWidth），会命中 app.css 里
   max-width:900px 的页头换行规则 —— 浮窗没有第二行的高度，必须复原成单行：
   搜索框让出剩余宽度（页头里只有它是可压缩的），标题降一档字号给动作区腾位置。 */
.clipboard-panel.compact .qd-page-header { flex-wrap: nowrap; padding: 0 var(--space-5); gap: var(--space-4); }
.clipboard-panel.compact .qd-page-title { font-size: var(--text-md); }
.clipboard-panel.compact .cb-search { flex: 1 1 auto; min-width: 110px; }

.icon-btn {
  /* 26px 是自造档，收到按钮四档的 sm(24)：命中区仍达 WCAG 最小 24px。 */
  width: 24px; height: 24px; display: flex; align-items: center; justify-content: center;
  background: transparent; border: none; color: var(--color-text-muted); border-radius: var(--radius-sm);
  cursor: pointer; flex-shrink: 0;
  transition: background-color var(--transition-fast), color var(--transition-fast),
    border-color var(--transition-fast), opacity var(--transition-fast);
}
.icon-btn:hover { background: var(--color-bg-hover); color: var(--color-text-secondary); }

/* ---- 筛选栏 ----
   一排：左「类型」、右「筛出条数 / 来源 / 时间」。宽窗口不换行；
   窄窗口整组换行（不横向滚动）——滚动容器会裁掉下拉浮层，标签「更多」点开就看不见了。 */
.cb-filters {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  row-gap: var(--space-3);
  flex-wrap: wrap;
  padding: var(--space-4) var(--space-6);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.cb-filter-types { display: flex; align-items: center; gap: var(--space-3); flex-shrink: 0; }
.cb-filters-fill { flex: 1; min-width: var(--space-4); }
.cb-filtered-count { font-size: var(--text-xs); color: var(--color-text-muted); white-space: nowrap; flex-shrink: 0; }
.cb-caret { opacity: 0.7; margin-left: 1px; }

/* 时间筛选：同排的轻量分段，选中用蓝字+淡蓝底 */
.time-btn {display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); 
  height: 22px; padding: 0 9px;
  border: 1px solid transparent;
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  font-family: inherit;
  cursor: pointer;
  white-space: nowrap;
  flex-shrink: 0;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.time-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.time-btn.active {
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border, var(--color-accent-bg));
}
.cb-time {
  display: flex; align-items: center; gap: var(--space-1); flex-shrink: 0;
  padding-left: var(--space-3);
  border-left: 1px solid var(--color-border);
}

.tag-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 22px; padding: 0 9px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  font-family: inherit;
  cursor: pointer;
  white-space: nowrap;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast);
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

/* 来源筛选：带文字的轻量下拉，替代原生 select（深色主题下原生下拉像一块补丁） */
.cb-src-btn {
  display: inline-flex; align-items: center; gap: var(--space-2);
  height: 22px; max-width: 200px; padding: 0 8px;
  border: 1px solid var(--color-border); border-radius: var(--radius-full);
  background: transparent; color: var(--color-text-muted);
  font-size: var(--text-xs); font-family: inherit; cursor: pointer; flex-shrink: 0;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.cb-src-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.cb-src-btn.active { color: var(--color-accent); border-color: var(--color-accent-border); background: var(--color-accent-bg); }
.cb-src-name { max-width: 150px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cb-src-menu { max-width: 320px; }
.cb-src-menu-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cb-menu-check { color: var(--color-accent); margin-left: var(--space-3); flex-shrink: 0; }
.save-menu-item.is-active { color: var(--color-accent); }

/* 页头「更多」：导出 / 清空 */
.cb-head-menu { min-width: 150px; }
.cb-head-menu .save-menu-item.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }

/* 页头「选择」按钮：带文字，省得用户猜图标是干啥的 */
.cb-select-toggle { gap: var(--space-2); }
.cb-select-toggle.is-on { color: var(--color-accent); border-color: var(--color-accent-border); background: var(--color-accent-bg); }

/* ---- 批量操作条 ---- */
.cb-batch {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 6px var(--space-6); flex-shrink: 0;
  background: var(--color-accent-bg);
  border-bottom: 1px solid var(--color-accent-border);
  flex-wrap: wrap; row-gap: var(--space-2);
}
.cb-batch-count { font-size: var(--text-xs); font-weight: 600; color: var(--color-accent); white-space: nowrap; }
.cb-batch-fill { flex: 1; }
.cb-batch-btn {
  display: inline-flex; align-items: center; gap: var(--space-2);
  height: 24px; padding: 0 var(--space-4);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
  font-size: var(--text-xs); font-family: inherit; cursor: pointer; white-space: nowrap;
  transition: color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast);
}
.cb-batch-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent); }
.cb-batch-btn.danger:hover:not(:disabled) { color: var(--color-danger); border-color: var(--color-danger); background: var(--color-danger-bg); }
.cb-batch-btn:disabled { opacity: 0.45; cursor: default; }

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

/* ---- 列表 = 网格 ----
   文本 / 文件通栏（grid-column: 1/-1），图片占一列。
   图片卡固定高度 + object-fit: cover：不再出现「宽度不一、左边一片空白」的锯齿边界。 */
.clipboard-list {
  flex: 1; overflow-y: auto; padding: var(--space-4);
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: var(--space-4);
  align-content: start;
}

/* 空态：网格只有一张卡时不该被 align-content:start 顶到最上面 */
.clipboard-list.is-empty { display: flex; align-items: center; justify-content: center; }

.cb-card {
  grid-column: 1 / -1;
  position: relative;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-width: 0;
  padding: var(--space-4) var(--space-5);
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast), box-shadow var(--transition-fast);
}
.cb-card:hover { background: var(--color-bg-hover); }
/* 悬停/聚焦的卡片提到上层：条目「更多」菜单向下展开时会压到下一张卡片，
   不抬 z-index 的话菜单会被后渲染的卡片盖住。 */
.cb-card:hover, .cb-card:focus-within { z-index: 5; }
/* 键盘游标（鼠标划过也算）只给一圈淡蓝边；真正的「已勾选」才用实蓝边 + 淡底，
   两者不混用——否则鼠标扫过整列时每条都像被选中（旧版的老问题）。 */
.cb-card.selected { border-color: var(--color-accent-border); }
.cb-card.checked {
  border-color: var(--color-accent);
  box-shadow: inset 0 0 0 1px var(--color-accent-border);
  background: var(--color-select-bg);
}

/* 多选态给复选框让位：文本卡片的正文不能压在勾选框下面 */
.cb-card.multi { padding-left: 34px; }
.cb-card.multi.is-image { padding-left: var(--space-3); }

/* 图片卡片：网格里的一列，固定 148px 高，cover 居中裁切 */
.cb-card.is-image { grid-column: span 1; padding: var(--space-3); gap: var(--space-3); }
.cb-img {
  position: relative;
  width: 100%; height: 148px;
  border-radius: var(--radius-sm);
  overflow: hidden;
  background: var(--color-bg-tertiary);
}
.cb-img-el { display: block; width: 100%; height: 100%; object-fit: cover; }
.cb-img-ph {
  position: absolute; inset: 0;
  display: flex; align-items: center; justify-content: center;
  color: var(--color-text-disabled);
}
.cb-img-path {
  font-size: var(--text-2xs); color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}

/* 文本卡片：默认 3 行，超过就给「展开全文」 */
.cb-text {
  font-size: var(--text-md); line-height: 1.55;
  color: var(--color-text-primary);
  word-break: break-word;
  white-space: pre-wrap;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.cb-text.is-open { display: block; -webkit-line-clamp: unset; overflow: visible; }

/* 代码 / JSON 预览：hljs 上色 + 固定高度，底部渐隐提示还有内容 */
.cb-code {
  margin: 0;
  padding: var(--space-3) var(--space-4);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 104px;
  overflow: hidden;
  -webkit-mask-image: linear-gradient(180deg, #000 72%, transparent 100%);
  mask-image: linear-gradient(180deg, #000 72%, transparent 100%);
}
.cb-code.is-open {
  max-height: 420px;
  overflow: auto;
  -webkit-mask-image: none;
  mask-image: none;
}

.cb-expand {
  display: inline-flex; align-items: center; gap: var(--space-1);
  align-self: flex-start; padding: 0;
  border: 0; background: none; cursor: pointer;
  font-size: var(--text-xs); font-family: inherit;
  color: var(--color-accent);
}
.cb-expand:hover { text-decoration: underline; }
.cb-expand-caret { transition: transform var(--transition-fast); }
.cb-expand-caret.up { transform: rotate(180deg); }

/* 文件条目 */
.cb-file { display: flex; align-items: flex-start; gap: var(--space-3); }
.cb-file-icon { flex-shrink: 0; margin-top: 2px; color: var(--color-accent); }
.cb-file-paths {
  flex: 1; min-width: 0;
  font-size: var(--text-md); color: var(--color-text-secondary); line-height: 1.5;
  word-break: break-all; white-space: pre-wrap;
}
.cb-file-reveal {
  flex-shrink: 0; display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; padding: 0;
  border: none; border-radius: var(--radius-sm); background: none;
  color: var(--color-text-muted); cursor: pointer; opacity: 0;
  transition: opacity var(--transition-fast), background-color var(--transition-fast), color var(--transition-fast);
}
.cb-card:hover .cb-file-reveal { opacity: 1; }
.cb-file-reveal:hover { color: var(--color-accent); background: var(--color-accent-bg); }
.cb-file-reveal:disabled { opacity: 0.4; cursor: default; }

/* ---- 元数据：图标 + 提亮一档的次要色 ---- */
.cb-meta { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; min-width: 0; }
.cb-meta-item {
  display: inline-flex; align-items: center; gap: var(--space-2);
  font-size: var(--text-xs); color: var(--color-text-muted);
  min-width: 0;
}
.cb-meta-src { max-width: 62%; }
.cb-meta-src-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cb-meta-count { color: var(--color-text-secondary); }
.cb-meta-lang {
  font-family: var(--font-mono); font-size: var(--text-2xs);
  letter-spacing: 0.04em; text-transform: uppercase;
  color: var(--color-accent); opacity: 0.85;
}
/* 屏蔽来源：hover 才出现的极轻量按钮 */
.cb-source-block {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 0; border: 0; background: transparent;
  color: var(--color-text-muted); cursor: pointer; opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast);
}
.cb-card:hover .cb-source-block, .cb-source-block:focus-visible { opacity: 1; }
.cb-source-block:hover { color: var(--color-danger); }

/* ---- 悬停操作栏 ----
   压在卡片右上角、半透明浮层：以前置顶/删除缩在内容行尾部，鼠标划过时
   还要横向找一遍；现在固定一个位置，且带 title 说明。 */
.cb-actions {
  position: absolute; top: 6px; right: 6px; z-index: 2;
  display: flex; align-items: center; gap: 1px;
  padding: 2px;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-md);
  opacity: 0;
  transform: translateY(-2px);
  pointer-events: none;
  transition: opacity var(--transition-fast), transform var(--transition-fast);
}
.cb-card:hover .cb-actions,
.cb-card:focus-within .cb-actions,
.cb-card.selected .cb-actions {
  opacity: 1; transform: none; pointer-events: auto;
}
.cb-act {
  display: inline-flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; padding: 0;
  border: 0; border-radius: var(--radius-xs);
  background: transparent; color: var(--color-text-muted); cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.cb-act:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.cb-act.on { color: var(--color-star); }
.cb-act.danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }

/* 收藏标记：操作栏收起时也能看出这条收藏过；悬停后让位给操作栏 */
.cb-pin-flag {
  position: absolute; top: 8px; right: 9px;
  display: flex; color: var(--color-star); opacity: 0.9;
  pointer-events: none;
}
.cb-card:hover .cb-pin-flag { opacity: 0; }

/* 下拉浮层的定位参照物：页头「更多」/ 标签「更多」/ 来源 / 条目「更多」共用 */
.save-wrap { position: relative; display: inline-flex; }

.save-menu {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: var(--z-dropdown);
  min-width: 132px; padding: var(--space-2);
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}
.save-menu-item {
  display: flex; align-items: center; gap: var(--space-4); width: 100%;
  padding: var(--space-3) var(--space-4); font-size: var(--text-sm); cursor: pointer;
  background: transparent; border: 0; border-radius: var(--radius-sm);
  color: var(--color-text-secondary); text-align: left;
}
.save-menu-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.clipboard-empty {
  grid-column: 1 / -1;
  display: flex; flex-direction: column; align-items: center;
  justify-content: center; height: 100%; color: var(--color-text-muted);
}
.clipboard-empty .empty-icon {
  width: 44px;
  height: 44px;
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: inset 0 1px 0 var(--color-hairline);
  margin-bottom: var(--space-5);
}
.clipboard-empty .empty-text { font-size: var(--text-md); color: var(--color-text-secondary); }
.clipboard-empty .empty-text + .qd-btn { margin-top: var(--space-3); }

.clipboard-loading {
  display: flex; align-items: center; justify-content: center;
  height: 100%; color: var(--color-text-muted); font-size: var(--text-md);
  gap: var(--space-3);
}
.clipboard-load-error { flex-direction: column; gap: var(--space-3); }
.clipboard-spin { animation: clipboard-rotate 0.9s linear infinite; }
@keyframes clipboard-rotate { to { transform: rotate(360deg); } }

/* 剪贴板分页 */
.clipboard-pagination {
  display: flex; align-items: center; justify-content: center;
  gap: var(--space-2); padding: var(--space-4); flex-shrink: 0;
  border-top: 1px solid var(--color-border);
}
.clipboard-pagination .page-btn {
  display: flex; align-items: center; justify-content: center;
  min-width: 24px; height: 24px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-size: var(--text-xs); font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.clipboard-pagination .page-btn:hover:not(:disabled) { background: var(--color-bg-active); color: var(--color-text-primary); }
.clipboard-pagination .page-btn:disabled { opacity: 0.35; cursor: default; }
.clipboard-pagination .page-btn.active { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }
.clipboard-pagination .page-ellipsis { color: var(--color-text-muted); font-size: var(--text-xs); width: 18px; text-align: center; }
.clipboard-pagination .page-total { margin-left: var(--space-3); font-size: var(--text-xs); color: var(--color-text-muted); }

/* 备注编辑弹窗 */
.note-editor-mask {
  position: absolute; inset: 0; z-index: var(--z-modal);
  display: flex; align-items: center; justify-content: center;
  background: var(--color-bg-overlay);
  animation: fadeIn 0.12s ease;
}
@keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }

.note-editor {
  width: 320px; max-width: 90vw;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
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
  font-size: var(--text-md); font-weight: 500; color: var(--color-text-primary);
}
.note-editor-head .icon-btn { width: 24px; height: 24px; }

.note-textarea {
  flex: 1; min-height: 80px; padding: 10px 14px;
  border: none; outline: none; resize: none;
  background: transparent; color: var(--color-text-primary);
  font-size: var(--text-md); font-family: inherit; line-height: 1.6;
}
.note-textarea::placeholder { color: var(--color-text-disabled); }

.note-editor-footer {
  display: flex; align-items: center; justify-content: flex-end;
  gap: var(--space-4); padding: 10px 14px;
  border-top: 1px solid var(--color-border);
}
.cancel-btn, .save-btn {
  display: inline-flex; align-items: center; justify-content: center;
  height: 28px; padding: 0 11px; border-radius: var(--radius-sm);
  font-size: var(--text-sm); font-family: inherit; font-weight: 500;
  cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
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
  display: flex; align-items: center; gap: var(--space-2);
  font-size: var(--text-xs); color: var(--color-text-muted);
  margin-top: var(--space-1); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.item-note-icon {
  flex-shrink: 0; opacity: 0.6;
  color: var(--color-accent);
}
.item-note span {
  overflow: hidden; text-overflow: ellipsis;
}

/* 多选开关高亮 / 按钮禁用态：.qd-icon-btn 来自全局 app.css，
   scoped 下用同一类名加状态即可命中本页渲染出来的按钮。 */
.qd-icon-btn.active {
  color: var(--color-accent);
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
}
.qd-icon-btn:disabled { opacity: 0.4; cursor: default; }

/* 粘贴队列条：贴在标签栏下方，队列为空时整条不渲染。 */
.cb-queue-bar {
  display: flex; align-items: center; gap: var(--space-4);
  padding: var(--space-3) var(--space-6); flex-shrink: 0;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-accent-bg);
}
.cb-queue-icon { flex-shrink: 0; color: var(--color-accent); }
.cb-queue-title { flex-shrink: 0; font-size: var(--text-xs); font-weight: 600; color: var(--color-accent); }
.cb-queue-status { flex-shrink: 0; font-size: var(--text-xs); color: var(--color-text-secondary); }
.cb-queue-chips { flex: 1; min-width: 0; display: flex; align-items: center; gap: var(--space-2); overflow: hidden; }
.cb-queue-chip {
  max-width: 120px; padding: 1px var(--space-3); font-size: var(--text-2xs);
  color: var(--color-text-muted); background: var(--color-bg-tertiary);
  border-radius: var(--radius-xs); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.cb-queue-more { font-size: var(--text-2xs); color: var(--color-text-muted); }
.cb-queue-actions { display: flex; align-items: center; gap: var(--space-1); flex-shrink: 0; }
.cb-queue-actions .qd-icon-btn { width: 24px; height: 24px; }

/* 浮窗只有 480px 宽，队列条必须再收一圈，否则第三条预览会把按钮挤出去。 */
.clipboard-panel.compact .cb-queue-bar { padding: var(--space-3) var(--space-5); gap: var(--space-3); }
.clipboard-panel.compact .cb-queue-title { display: none; }
.clipboard-panel.compact .cb-queue-chip { max-width: 88px; }

/* 多选复选框：压在卡片左上角，外层 @click.stop 拦住条目本身的复制动作。
   给一层实底是为了在图片卡片上也看得清（图片底色不可控）。 */
.cb-check {
  position: absolute; top: 8px; left: 8px; z-index: 3;
  display: flex; align-items: center; justify-content: center;
  width: 20px; height: 20px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  cursor: pointer;
}
.cb-check input { margin: 0; cursor: pointer; }

/* ---- 浮窗（compact）适配 ----
   480px 宽放不下多列网格，也不该把一张截图撑成满屏高：
   单列 + 固定高度的图片条，仍然比原来的自由高度整齐。 */
.clipboard-panel.compact .cb-filters { padding: var(--space-3) var(--space-5); gap: var(--space-3); }
.clipboard-panel.compact .cb-filter-types { gap: var(--space-2); }
.clipboard-panel.compact .cb-select-label { display: none; }
.clipboard-panel.compact .clipboard-list { grid-template-columns: 1fr; padding: var(--space-3); gap: var(--space-3); }
.clipboard-panel.compact .cb-card.is-image { grid-column: 1 / -1; }
.clipboard-panel.compact .cb-img { height: 120px; }
.clipboard-panel.compact .cb-text { -webkit-line-clamp: 2; }
.clipboard-panel.compact .cb-code { max-height: 72px; }
.clipboard-panel.compact .cb-batch { padding: 4px var(--space-5); }
</style>

