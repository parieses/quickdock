<script setup lang="ts">
import { copyToClipboard } from '../utils/clipboard'
import PageHeader from './PageHeader.vue'
// BookmarkPage.vue — 浏览器书签导入与维护页
//
// 书签不是独立实体：导入后就是普通「网页」类型项目（落在「书签」工作空间里），
// 因此这里不重复实现打开/搜索——打开走 collectionservice 的 OpenItem，
// 与资源树点开同一条路径，usage_count 也照常累加。
//
// 页面只承担「导入 + 维护」两件事：
//   导入  直接读本机浏览器的书签（无需导出），或选一份 HTML 文件；先预览再写库
//   维护  死链检查 / 重复合并 / 策展洞察 / 补全网站图标
// 没有阅读状态（未读→在读→已读）：那是稍后读工具的语义，这里的条目是「要用的东西」，
// 加了没人会维护。
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { KEEP_ALIVE } from '../pages/keepAlive'
import {
  ArrowLeft, ArrowUpDown, Bookmark, Check, CheckSquare, Copy, CopyCheck, ExternalLink, FilePlus2,
  Folder, Globe, Globe2, GripVertical, Link2Off, Loader2, Pencil, Plus, RefreshCw, Sparkles,
  Trash2,
} from '@lucide/vue'
import QdModal from './QdModal.vue'
import QdSearchBox from './QdSearchBox.vue'
import QdTabs from './QdTabs.vue'
import { useToast } from '../composables/useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { SelectLocalFile } from '../../bindings/quickdock/services/appservice'
import {
  DeleteCollection, OpenAllInCollection, OpenItem, ReorderCollections, UpdateCollection,
} from '../../bindings/quickdock/services/collection/collectionservice'
import {
  ApplyMovedURL, BuildCurationInsight, FillIcons, FindDuplicates, ImportBookmarks, ImportBrowserBookmarks,
  LinkCheckStatus, ListBookmarks, ListBrowserSources, ListHealth, MergeDuplicates, PreviewBrowserImport,
  PreviewImport, StartLinkCheck,
} from '../../bindings/quickdock/services/bookmark/bookmarkservice'

import { reportError } from '../utils/reportError'
defineOptions({ name: KEEP_ALIVE.bookmarks })

const { t } = useI18n()
const toast = useToast()

type BookmarkRow = {
  id: string
  name: string
  value: string
  remark: string
  icon: string
  collection_id: string
  collection_name: string
  group_name: string
  scene_name: string
}

type HealthRow = {
  itemId: string
  status: string
  code: number
  finalUrl: string
  errMsg: string
  changed: boolean
  checkedAt: string
}

type CheckStatus = {
  running: boolean
  total: number
  done: number
  ok: number
  broken: number
  moved: number
  blocked: number
  failed: number
  timedOut: number
}

type DupGroup = {
  kind: 'url' | 'title'
  key: string
  items: { id: string; name: string; url: string; collectionName: string; sceneName: string }[]
}

type SourceInfo = { key: string; browser: string; profile: string; path: string; count: number }

type Preview = {
  path: string
  source: string
  sceneName: string
  total: number
  valid: number
  invalid: number
  existing: number
  fresh: number
  folders: number
  sample: { title: string; url: string; folder: string }[]
}

type TabKey = 'all' | 'broken' | 'blocked' | 'moved' | 'changed'

const rows = ref<BookmarkRow[]>([])
const health = ref<Record<string, HealthRow>>({})
const keyword = ref('')
const tab = ref<TabKey>('all')
const loading = ref(false)
const iconsBusy = ref(false)
const iconsFilled = ref(0)

// 导入
const importOpen = ref(false)
const sources = ref<SourceInfo[]>([])
const sourcesLoading = ref(false)
const preview = ref<Preview | null>(null)
const previewKey = ref('') // 浏览器来源的 Key；HTML 文件导入为空
const previewSourceName = ref('')
const importing = ref(false)
const sceneName = ref('')

// 死链检查
const checking = ref<CheckStatus | null>(null)
let pollTimer: number | null = null

// 查重
const dupGroups = ref<DupGroup[]>([])
const dupExtra = ref(0)
const keepChoice = ref<Record<number, string>>({})
// 「查过了但没有重复」需要一个单独的态：dupGroups 为空时弹窗不能靠它判断，
// 否则用户点一次查重、什么也没发生，还以为按钮坏了。
const dupChecked = ref(false)

// 洞察
const insight = ref<{ stats: Record<string, number>; advice: { kind: string; count: number }[] } | null>(null)

const statusOf = (id: string) => health.value[id]?.status ?? ''

const tabs = computed(() => [
  { id: 'all' as const, label: t('bmTabAll'), badge: rows.value.length },
  { id: 'broken' as const, label: t('bmTabBroken'), badge: countBy('broken') },
  { id: 'blocked' as const, label: t('bmTabBlocked'), badge: countBy('blocked') },
  { id: 'moved' as const, label: t('bmTabMoved'), badge: countBy('moved') },
  { id: 'changed' as const, label: t('bmTabChanged'), badge: changedCount() },
])

function countBy(s: string) {
  return rows.value.filter((r) => statusOf(r.id) === s).length
}
function changedCount() {
  return rows.value.filter((r) => health.value[r.id]?.changed).length
}

const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return rows.value.filter((r) => {
    if (kw && !r.name.toLowerCase().includes(kw) && !r.value.toLowerCase().includes(kw)) return false
    switch (tab.value) {
      case 'all':
        return true
      case 'changed':
        return !!health.value[r.id]?.changed
      default:
        return statusOf(r.id) === tab.value
    }
  })
})

// 分组是两级的：一级 = 书签文件夹的上级路径（collection.group_name），二级 = 集合本身。
// 网格视图拿二级（集合）当卡片，一级只作标题前缀——几百条书签平铺成几十个无法收起的
// 标题时，一屏能看的文件夹数量反而变少，横向又被拉成空白长条。
type LeafGroup = { key: string; label: string; items: BookmarkRow[] }
type RootGroup = { key: string; label: string; scene: string; total: number; leaves: LeafGroup[]; flat: boolean }

const groups = computed<RootGroup[]>(() => {
  const roots: RootGroup[] = []
  const rootIndex = new Map<string, number>()
  const leaves = new Map<string, LeafGroup>()

  for (const r of filtered.value) {
    const groupName = (r.group_name || '').trim()
    const colName = r.collection_name || t('bmUncategorized')
    // 无父路径时一级就是集合自己，用集合 id 当一级 key，避免不同场景的同名集合撞在一起
    const rootKey = groupName
      ? `${r.scene_name}|${groupName}`
      : `${r.scene_name}|#${r.collection_id || 'none'}`
    let ri = rootIndex.get(rootKey)
    if (ri === undefined) {
      ri = roots.length
      rootIndex.set(rootKey, ri)
      roots.push({
        key: rootKey,
        label: groupName || colName,
        scene: r.scene_name || '',
        total: 0,
        leaves: [],
        flat: !groupName,
      })
    }
    const root = roots[ri]
    // 集合 id 全局唯一，可直接当二级 key
    const leafKey = r.collection_id || 'none'
    let leaf = leaves.get(leafKey)
    if (!leaf) {
      leaf = { key: leafKey, label: colName, items: [] }
      leaves.set(leafKey, leaf)
      root.leaves.push(leaf)
    }
    leaf.items.push(r)
    root.total++
  }

  for (const root of roots) {
    if (root.leaves.length === 1) {
      root.flat = true
      // 拍平时一级标题吃掉二级名字，否则「技术 / 前端」只剩「技术」，反而看不出是哪个文件夹
      const only = root.leaves[0].label
      if (root.label !== only) root.label = `${root.label} / ${only}`
    }
  }

  return roots
})

// ── 卡片 ─────────────────────────────────────────────
// 两个视图共用一张卡片：文件夹视图按集合聚合，域名视图按主机名聚合。
// 卡片只算「数量 + 健康分布 + 前几个图标」，条目本身留在 items 里给详情页渲染。
type Card = {
  key: string
  rootKey: string
  name: string
  label: string
  sub: string
  scene: string
  collectionId: string
  items: BookmarkRow[]
  total: number
  broken: number
  blocked: number
  moved: number
  changed: number
  issues: number
  preview: BookmarkRow[]
  names: string[]
}

function buildCard(base: Omit<Card, 'total' | 'broken' | 'blocked' | 'moved' | 'changed' | 'issues' | 'preview' | 'names'>): Card {
  let broken = 0
  let blocked = 0
  let moved = 0
  let changed = 0
  let issues = 0
  for (const r of base.items) {
    const h = health.value[r.id]
    if (!h) continue
    const isBroken = h.status === 'broken'
    const isBlocked = h.status === 'blocked'
    const isMoved = h.status === 'moved'
    if (isBroken) broken++
    if (isBlocked) blocked++
    if (isMoved) moved++
    if (h.changed) changed++
    if (isBroken || isBlocked || isMoved || h.changed) issues++
  }
  return {
    ...base,
    total: base.items.length,
    broken, blocked, moved, changed, issues,
    // 预览最多 5 个图标：卡片宽度只够这么多，再挤就成了一排看不清的小点
    preview: base.items.slice(0, 5),
    names: base.items.slice(0, 3).map((r) => r.name),
  }
}

// 只有一个场景时不重复播报浏览器名：十几个卡片各挂一个「Chrome」，
// 扫列表时反而看不见真正的文件夹信息。
const multiScene = computed(() => new Set(rows.value.map((r) => r.scene_name || '')).size > 1)

const folderCards = computed<Card[]>(() => {
  const out: Card[] = []
  for (const root of groups.value) {
    for (const leaf of root.leaves) {
      out.push(buildCard({
        key: `f|${root.scene}|${leaf.key}`,
        rootKey: root.key,
        name: leaf.label,
        label: root.flat ? root.label : `${root.label} / ${leaf.label}`,
        sub: multiScene.value ? root.scene : '',
        scene: root.scene,
        collectionId: leaf.key === 'none' ? '' : leaf.key,
        items: leaf.items,
      }))
    }
  }
  return out
})

const domainCards = computed<Card[]>(() => {
  const byHost = new Map<string, BookmarkRow[]>()
  for (const r of filtered.value) {
    const host = hostOf(r.value)
    const arr = byHost.get(host)
    if (arr) arr.push(r)
    else byHost.set(host, [r])
  }
  const out: Card[] = []
  for (const [host, items] of byHost) {
    out.push(buildCard({
      key: `d|${host}`,
      rootKey: 'domain',
      name: host,
      label: host,
      sub: t('bmDomainFolders', { n: new Set(items.map((r) => r.collection_name || '')).size }),
      scene: '',
      collectionId: '',
      items,
    }))
  }
  return out
})

type ViewMode = 'folder' | 'domain'
type SortMode = 'default' | 'issues' | 'count'

const viewMode = ref<ViewMode>('folder')
const sortMode = ref<SortMode>('default')
const sortOpen = ref(false)
const selectMode = ref(false)

const cards = computed<Card[]>(() => {
  const list = [...(viewMode.value === 'folder' ? folderCards.value : domainCards.value)]
  if (sortMode.value === 'issues') list.sort((a, b) => b.issues - a.issues || b.total - a.total)
  else if (sortMode.value === 'count') list.sort((a, b) => b.total - a.total)
  // 域名视图默认按书签数排：几百个站点里，头部那几个才是常去的
  else if (viewMode.value === 'domain') list.sort((a, b) => b.total - a.total)
  return list
})

// 筛选态：有关键词或选了健康标签时，卡片上的数量是「命中数」而不是文件夹总数
const filterActive = computed(() => keyword.value.trim() !== '' || tab.value !== 'all')

// 详情：点卡片进入，面包屑返回。key 用集合 id，重新加载后依然指得回同一个文件夹。
const openKey = ref('')
const openCard = computed(() => cards.value.find((c) => c.key === openKey.value) || null)
const openItems = computed(() => openCard.value?.items ?? [])
watch(cards, () => {
  // 文件夹被删掉、或切到域名视图后旧 key 不存在了：退回网格，别停在空白详情页
  if (openKey.value && !openCard.value) openKey.value = ''
})

function openFolder(c: Card) {
  if (selectMode.value && c.collectionId) {
    togglePick(c.collectionId)
    return
  }
  openKey.value = c.key
}

const viewTabs = computed(() => [
  { id: 'folder' as const, label: t('bmViewFolder') },
  { id: 'domain' as const, label: t('bmViewDomain') },
])

const sortOptions = computed(() => [
  { id: 'default' as const, label: t('bmSortDefault') },
  { id: 'issues' as const, label: t('bmSortIssues') },
  { id: 'count' as const, label: t('bmSortCount') },
])
const sortLabel = computed(() => sortOptions.value.find((o) => o.id === sortMode.value)?.label ?? '')

// ── 勾选与批量 ────────────────────────────────────────
const picked = ref<string[]>([])
// 统计用「全部文件夹」而不是当前可见卡片：勾完再敲关键词筛选时，
// 已选 N / 共 M 条 不该因为筛掉了几个就缩水，否则批量删除的代价会被低估。
const pickedCards = computed(() => folderCards.value.filter((c) => c.collectionId && picked.value.includes(c.collectionId)))
const pickedTotal = computed(() => pickedCards.value.reduce((n, c) => n + c.total, 0))
const allPicked = computed(() => {
  const ids = cards.value.filter((c) => c.collectionId).map((c) => c.collectionId)
  return ids.length > 0 && ids.every((id) => picked.value.includes(id))
})
const isPicked = (c: Card) => !!c.collectionId && picked.value.includes(c.collectionId)

// 换视图时退出批量与详情：勾选的是集合 id，域名视图里没有对应卡片，
// 留着会让「已选 3 个」悬空。
watch(viewMode, () => {
  openKey.value = ''
  selectMode.value = false
  picked.value = []
})

function toggleSelectMode() {
  selectMode.value = !selectMode.value
  if (!selectMode.value) picked.value = []
  // 批量只在网格里有意义，进详情就退出
  if (selectMode.value) openKey.value = ''
}
function togglePick(id: string) {
  picked.value = picked.value.includes(id) ? picked.value.filter((x) => x !== id) : [...picked.value, id]
}
function togglePickAll() {
  picked.value = allPicked.value ? [] : cards.value.filter((c) => c.collectionId).map((c) => c.collectionId)
}

const batchOpen = ref(false)
const batchBusy = ref(false)
function askBatchDelete() {
  if (picked.value.length) batchOpen.value = true
}
async function confirmBatchDelete() {
  const ids = [...picked.value]
  if (!ids.length) return
  batchBusy.value = true
  let done = 0
  for (const id of ids) {
    try {
      unwrap(await DeleteCollection(id))
      done++
    } catch (e) {
      logErr('bookmark.batchDelete', e)
    }
  }
  batchBusy.value = false
  batchOpen.value = false
  picked.value = []
  selectMode.value = false
  await load(true)
  if (done === ids.length) toast.success(t('bmBatchDeleted', { n: done }))
  else toast.error(t('bmBatchPartial', { done, total: ids.length }))
}

// ── 重命名 / 删除单个文件夹 ────────────────────────────
const renameOpen = ref(false)
const renameValue = ref('')
const renameTarget = ref<Card | null>(null)
const renameBusy = ref(false)
function askRename(c: Card) {
  if (!c.collectionId) return
  renameTarget.value = c
  renameValue.value = c.name
  renameOpen.value = true
}
async function confirmRename() {
  const c = renameTarget.value
  const name = renameValue.value.trim()
  if (!c?.collectionId || !name || renameBusy.value) return
  renameBusy.value = true
  try {
    unwrap(await UpdateCollection(c.collectionId, { name }))
    renameOpen.value = false
    await load(true)
    toast.success(t('bmRenamed'))
  } catch (e) {
    reportError(e, 'bookmark.rename')
  } finally {
    renameBusy.value = false
  }
}

const deleteTarget = ref<Card | null>(null)
const deleteBusy = ref(false)
function askDelete(c: Card) {
  if (!c.collectionId) return
  deleteTarget.value = c
}
async function confirmDelete() {
  const c = deleteTarget.value
  if (!c?.collectionId || deleteBusy.value) return
  deleteBusy.value = true
  try {
    unwrap(await DeleteCollection(c.collectionId))
    if (openKey.value === c.key) openKey.value = ''
    deleteTarget.value = null
    await load(true)
    toast.success(t('bmFolderDeleted'))
  } catch (e) {
    reportError(e, 'bookmark.delete')
  } finally {
    deleteBusy.value = false
  }
}

async function openAll() {
  const c = openCard.value
  if (!c?.collectionId) return
  try {
    unwrap(await OpenAllInCollection(c.collectionId, ''))
  } catch (e) {
    toast.error(t('openAllFailed') + ': ' + getErrorMessage(e))
    logErr('bookmark', e)
  }
}

// ── 拖拽排序（同组内） ─────────────────────────────────
// 后端 ReorderCollections 只按传入顺序重写 sort；列表 SQL 是
// `ORDER BY s.name, c.group_name, c.sort, c.name`，跨组拖会被 group_name 弹回原位，
// 所以只在同一 rootKey 内允许放下，语义与结果一致。
const dragKey = ref('')
const dragOverKey = ref('')
function canDrag(c: Card) {
  return viewMode.value === 'folder' && sortMode.value === 'default' && !selectMode.value
    && !filterActive.value && !!c.collectionId
}
function onDragStart(e: DragEvent, c: Card) {
  if (!canDrag(c)) {
    e.preventDefault()
    return
  }
  dragKey.value = c.key
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', c.key)
  }
}
function onDragOver(e: DragEvent, c: Card) {
  const from = cards.value.find((x) => x.key === dragKey.value)
  if (!from || from.key === c.key || from.rootKey !== c.rootKey) {
    dragOverKey.value = ''
    return
  }
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  dragOverKey.value = c.key
}
async function onDrop(c: Card) {
  const from = cards.value.find((x) => x.key === dragKey.value)
  dragKey.value = ''
  dragOverKey.value = ''
  if (!from || from.key === c.key || from.rootKey !== c.rootKey) return
  const ids = cards.value.filter((x) => x.rootKey === c.rootKey && x.collectionId).map((x) => x.collectionId)
  const fromIdx = ids.indexOf(from.collectionId)
  const toIdx = ids.indexOf(c.collectionId)
  if (fromIdx < 0 || toIdx < 0) return
  ids.splice(toIdx, 0, ids.splice(fromIdx, 1)[0])
  try {
    unwrap(await ReorderCollections(ids))
    await load(true)
    toast.success(t('bmReordered'))
  } catch (e) {
    reportError(e, 'bookmark.reorder')
  }
}
function onDragEnd() {
  dragKey.value = ''
  dragOverKey.value = ''
}

// 死链检查的进度：总量来自后端，百分比是实测值，不做「假装在动」的动画。
const checkInfo = computed(() => {
  const c = checking.value
  if (!c?.running) return null
  const pct = c.total ? Math.min(100, Math.round((c.done / c.total) * 100)) : 0
  return { done: c.done, total: c.total, pct }
})
const progressText = computed(() => {
  const c = checking.value
  if (!c) return ''
  return `${c.done} / ${c.total}`
})

const previewSourceLabel = computed(() => {
  const p = preview.value
  if (!p) return ''
  if (previewSourceName.value) return previewSourceName.value
  const parts = p.path.split(/[\\/]/)
  return parts[parts.length - 1] || p.path
})

// 网址只显示主机名：完整 URL 在列表里永远被截断，而「这条属于哪个站」才是扫列表时要看的。
function hostOf(url: string) {
  try {
    return new URL(url).host.replace(/^www\./, '')
  } catch {
    return url
  }
}
function initial(r: BookmarkRow) {
  return (hostOf(r.value).charAt(0) || '?').toUpperCase()
}
// 行副标题：文件夹视图看「这个条目属于哪个站」，域名视图反过来看「它在哪个文件夹」，
// 否则在 github.com 的详情里每行都重复一遍 github.com。
function rowSub(r: BookmarkRow) {
  const base = viewMode.value === 'domain'
    ? (r.collection_name || t('bmUncategorized'))
    : hostOf(r.value)
  return r.remark ? `${base} · ${r.remark}` : base
}
function badgeText(r: BookmarkRow) {
  const h = health.value[r.id]
  if (!h) return ''
  return h.code ? `${h.code} ${t(`bmStatus_${h.status}`)}` : t(`bmStatus_${h.status}`)
}

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const list = unwrap<BookmarkRow[]>(await ListBookmarks('')) ?? []
    rows.value = list
    const hs = unwrap<HealthRow[]>(await ListHealth('')) ?? []
    const map: Record<string, HealthRow> = {}
    for (const h of hs) map[h.itemId] = h
    health.value = map
  } catch (e) {
    reportError(e, 'bookmark')
  } finally {
    loading.value = false
  }
}

// ── 网站图标 ──────────────────────────────────────────
// 导入走的是批量插入，没经过「保存条目时抓 favicon」那条路，所以 icon 全为空。
// 这里打开页面就按批补，补一批刷一次列表：用户先看到列表，图标随后一批批出现。
let iconsRunning = false
async function fillIcons() {
  if (iconsRunning) return
  iconsRunning = true
  iconsBusy.value = true
  iconsFilled.value = 0
  try {
    for (let round = 0; round < 6; round++) {
      const res = unwrap<{ checked: number; filled: number }>(await FillIcons(24))
      if (!res || res.filled === 0) break
      iconsFilled.value += res.filled
      await load(true)
    }
  } catch (e) {
    logErr('bookmark.icons', e)
  } finally {
    iconsRunning = false
    iconsBusy.value = false
  }
}

// ── 行操作 ───────────────────────────────────────────
async function openBookmark(r: BookmarkRow) {
  try {
    unwrap(await OpenItem(r.id))
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

async function copyUrl(r: BookmarkRow) {
  if (await copyToClipboard(r.value)) {
    toast.success(t('bmCopied'))
  } else {
    toast.error(t('copyFailed'))
  }
}

// ── 导入 ─────────────────────────────────────────────
async function openImport() {
  importOpen.value = true
  preview.value = null
  previewKey.value = ''
  previewSourceName.value = ''
  sceneName.value = ''
  await loadSources()
}

async function loadSources() {
  sourcesLoading.value = true
  try {
    sources.value = unwrap<SourceInfo[]>(await ListBrowserSources()) ?? []
  } catch (e) {
    reportError(e, 'bookmark')
  } finally {
    sourcesLoading.value = false
  }
}

function applyPreview(p: Preview, key: string, sourceName: string) {
  preview.value = p
  previewKey.value = key
  previewSourceName.value = sourceName
  sceneName.value = p.sceneName
}

async function previewSource(s: SourceInfo) {
  try {
    const p = unwrap<Preview>(await PreviewBrowserImport(s.key))
    if (!p) return
    applyPreview(p, s.key, s.profile ? `${s.browser} · ${s.profile}` : s.browser)
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

async function pickFile() {
  try {
    const path = unwrap<string>(await SelectLocalFile(t('bmImportPickTitle'))) ?? ''
    if (!path) return // 取消是正常操作
    const p = unwrap<Preview>(await PreviewImport(path))
    if (!p) return
    applyPreview(p, '', '')
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

async function confirmImport() {
  if (!preview.value) return
  importing.value = true
  try {
    type ImportRes = { created: number; skipped: number; duplicated: number; invalid: number }
    const res = previewKey.value
      ? unwrap<ImportRes>(await ImportBrowserBookmarks(previewKey.value, sceneName.value))
      : unwrap<ImportRes>(await ImportBookmarks(preview.value.path, sceneName.value))
    toast.success(t('bmImportDone', { created: res?.created ?? 0, skipped: res?.skipped ?? 0 }))
    preview.value = null
    importOpen.value = false
    await load()
    void fillIcons()
  } catch (e) {
    reportError(e, 'bookmark')
  } finally {
    importing.value = false
  }
}

// ── 死链检查 ──────────────────────────────────────────
async function startCheck() {
  try {
    const st = unwrap<CheckStatus>(await StartLinkCheck([], false))
    checking.value = st ?? null
    poll()
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

function poll() {
  stopPoll()
  pollTimer = window.setInterval(async () => {
    try {
      const st = unwrap<CheckStatus>(await LinkCheckStatus())
      if (!st) return
      checking.value = st
      if (!st.running) {
        stopPoll()
        await load(true)
        toast.success(t('bmCheckDone'))
      }
    } catch (e) {
      stopPoll()
      logErr('bookmark', e)
    }
  }, 1000)
}

function stopPoll() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

onScopeDispose(stopPoll)

async function applyMoved(h: HealthRow) {
  try {
    unwrap(await ApplyMovedURL(h.itemId, h.finalUrl))
    await load(true)
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

// ── 查重 ─────────────────────────────────────────────
async function openDuplicates() {
  try {
    const res = unwrap<{ groups: DupGroup[]; extraCount: number }>(await FindDuplicates())
    dupGroups.value = res?.groups ?? []
    dupExtra.value = res?.extraCount ?? 0
    dupChecked.value = dupGroups.value.length === 0
    const choice: Record<number, string> = {}
    dupGroups.value.forEach((g, i) => { choice[i] = g.items[0]?.id ?? '' })
    keepChoice.value = choice
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

async function mergeGroup(i: number) {
  const g = dupGroups.value[i]
  const keep = keepChoice.value[i]
  if (!g || !keep) return
  const drops = g.items.filter((it) => it.id !== keep).map((it) => it.id)
  if (!drops.length) return
  try {
    unwrap(await MergeDuplicates(keep, drops))
    dupGroups.value.splice(i, 1)
    dupExtra.value = Math.max(0, dupExtra.value - drops.length)
    await load(true)
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

// ── 洞察 ─────────────────────────────────────────────
async function openInsight() {
  try {
    const res = unwrap<{ stats: Record<string, number>; advice: { kind: string; count: number }[] }>(
      await BuildCurationInsight(),
    )
    insight.value = res ?? null
  } catch (e) {
    reportError(e, 'bookmark')
  }
}

const adviceLabel = (kind: string) => t(`bmAdvice_${kind}`)

onMounted(() => {
  void load()
  void fillIcons()
})
</script>

<template>
  <div class="qd-page">
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title"><Bookmark :size="16" />{{ t('navBookmarks') }}</h2>
        <span class="qd-page-sub">{{ t('bmSubtitle', { n: rows.length }) }}</span>
      </template>
      <div class="qd-page-actions">
        <div class="qd-action-group">
          <button
            class="qd-icon-btn"
            :title="checking?.running ? progressText : t('bmCheck')"
            :disabled="!!checking?.running || !rows.length"
            @click="startCheck"
          >
            <Loader2 v-if="checking?.running" :size="14" class="spin" />
            <Link2Off v-else :size="14" />
          </button>
          <button class="qd-icon-btn" :title="t('bmDedupe')" :disabled="!rows.length" @click="openDuplicates">
            <CopyCheck :size="14" />
          </button>
          <button class="qd-icon-btn" :title="t('bmInsight')" :disabled="!rows.length" @click="openInsight">
            <Sparkles :size="14" />
          </button>
          <button class="qd-icon-btn" :title="t('bmRefresh')" @click="load()">
            <RefreshCw :size="14" :class="{ spin: loading }" />
          </button>
        </div>
        <span class="qd-action-sep"></span>
        <button class="qd-btn qd-btn-primary" @click="openImport">
          <Plus :size="14" />{{ t('bmImport') }}
        </button>
      </div>
    </PageHeader>

    <div class="qd-toolbar bm-toolbar">
      <QdSearchBox
        v-model="keyword"
        class="bm-search"
        :icon-size="14"
        :clear-title="t('bmClear')"
        :placeholder="t('bmSearchPlaceholder')"
      />

      <!-- 视图：文件夹 / 域名。域名视图把散落在各文件夹里的同一站点收成一堆 -->
      <QdTabs v-model="viewMode" :tabs="viewTabs" class="bm-view" :aria-label="t('bmViewLabel')">
        <template #tab="{ tab: item }">
          <component :is="item.id === 'folder' ? Folder : Globe2" :size="12" />
          <span>{{ item.label }}</span>
        </template>
      </QdTabs>

      <!-- 健康筛选：三个问题标签各带一个色点，扫一眼就知道哪里有脏数据 -->
      <QdTabs v-model="tab" :tabs="tabs" variant="chip">
        <template #tab="{ tab: item }">
          <span v-if="item.id !== 'all'" class="bm-tab-dot" :class="'is-' + item.id"></span>
          <span>{{ item.label }}</span>
          <span class="bm-tab-n">{{ item.badge }}</span>
        </template>
      </QdTabs>

      <span class="qd-page-fill" />

      <button
        v-if="viewMode === 'folder'"
        class="qd-icon-btn"
        :class="{ active: selectMode }"
        :aria-pressed="selectMode"
        :title="t('bmSelect')"
        @click="toggleSelectMode"
      >
        <CheckSquare :size="14" />
      </button>

      <!-- 排序：替代原来的「全部展开 / 全部收起」——网格里没有折叠，六百条一次铺开也不会卡 -->
      <div class="bm-sort">
        <button class="qd-btn qd-btn-ghost qd-btn-sm" :aria-expanded="sortOpen" @click="sortOpen = !sortOpen">
          <ArrowUpDown :size="12" /><span class="bm-sort-label">{{ sortLabel }}</span>
        </button>
        <template v-if="sortOpen">
          <div class="bm-menu-backdrop" @click="sortOpen = false"></div>
          <div class="bm-menu" role="menu">
            <button
              v-for="o in sortOptions"
              :key="o.id"
              class="bm-menu-item"
              role="menuitem"
              :class="{ active: sortMode === o.id }"
              @click="sortMode = o.id; sortOpen = false"
            >
              <span class="bm-menu-tick"><Check v-if="sortMode === o.id" :size="12" /></span>
              {{ o.label }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- 后台任务条：检查死链 / 补图标原来是右上角一行小字，很容易错过 -->
    <div v-if="checkInfo || iconsBusy" class="bm-banner">
      <Loader2 :size="13" class="spin" />
      <template v-if="checkInfo">
        <span class="bm-banner-text">{{ t('bmCheckRunning', { done: checkInfo.done, total: checkInfo.total }) }}</span>
        <span class="bm-banner-bar"><span class="bm-banner-fill" :style="{ width: checkInfo.pct + '%' }"></span></span>
        <span class="bm-banner-num">{{ checkInfo.pct }}%</span>
      </template>
      <template v-else>
        <span class="bm-banner-text">{{ t('bmIconsFetching') }}</span>
        <span class="bm-banner-bar"><span class="bm-banner-fill is-indeterminate"></span></span>
        <span v-if="iconsFilled" class="bm-banner-num">{{ iconsFilled }}</span>
      </template>
    </div>

    <!-- 批量条：勾选文件夹后从工具栏下方浮出 -->
    <div v-if="selectMode" class="bm-batch">
      <span class="bm-batch-n">{{ t('bmBatchPicked', { n: picked.length }) }}</span>
      <span class="bm-batch-sub">{{ t('bmBatchBooks', { n: pickedTotal }) }}</span>
      <span class="qd-page-fill" />
      <button class="qd-btn qd-btn-sm" @click="togglePickAll">{{ allPicked ? t('bmUnpickAll') : t('bmPickAll') }}</button>
      <button class="qd-btn qd-btn-sm qd-btn-danger" :disabled="!picked.length" @click="askBatchDelete">{{ t('delete') }}</button>
      <button class="qd-btn qd-btn-sm" @click="toggleSelectMode">{{ t('cancel') }}</button>
    </div>

    <div class="qd-page-body bm-body">
      <div v-if="!loading && !rows.length" class="qd-empty">
        <div class="qd-empty-icon"><Bookmark :size="20" /></div>
        <p class="qd-empty-title">{{ t('bmEmpty') }}</p>
        <p class="qd-empty-hint">{{ t('bmEmptyHint') }}</p>
        <button class="qd-empty-cta" @click="openImport"><Plus :size="14" />{{ t('bmImport') }}</button>
      </div>

      <!-- 详情：一个文件夹（或一个站点）里的全部书签 -->
      <template v-else-if="openCard">
        <div class="bm-detail-head">
          <button class="qd-btn qd-btn-ghost qd-btn-sm" @click="openKey = ''">
            <ArrowLeft :size="13" />{{ viewMode === 'folder' ? t('bmAllFolders') : t('bmAllDomains') }}
          </button>
          <span class="bm-detail-sep"></span>
          <span class="bm-detail-title" :title="openCard.label">
            <component :is="viewMode === 'folder' ? Folder : Globe2" :size="13" />
            {{ openCard.label }}
          </span>
          <span class="bm-detail-n">{{ t('bmCountN', { n: openCard.total }) }}</span>
          <!-- 详情头里带文字：孤零零一个红「3」离开卡片上下文就说不清是什么 -->
          <span v-if="openCard.broken" class="bm-badge is-broken">{{ t('bmTabBroken') }} {{ openCard.broken }}</span>
          <span v-if="openCard.blocked" class="bm-badge is-blocked">{{ t('bmTabBlocked') }} {{ openCard.blocked }}</span>
          <span v-if="openCard.moved" class="bm-badge is-moved">{{ t('bmTabMoved') }} {{ openCard.moved }}</span>
          <span v-if="openCard.changed" class="bm-badge is-changed">{{ t('bmTabChanged') }} {{ openCard.changed }}</span>
          <span class="qd-page-fill" />
          <template v-if="openCard.collectionId">
            <button class="qd-btn qd-btn-sm" @click="openAll"><ExternalLink :size="12" />{{ t('openAll') }}</button>
            <button class="qd-icon-btn" :title="t('bmRename')" @click="askRename(openCard)"><Pencil :size="14" /></button>
            <button class="qd-icon-btn" :title="t('delete')" @click="askDelete(openCard)"><Trash2 :size="14" /></button>
          </template>
        </div>

        <section class="qd-card bm-detail">
          <div v-for="r in openItems" :key="r.id" class="qd-row bm-row" @dblclick="openBookmark(r)">
            <span class="qd-row-icon">
              <img
                v-if="r.icon" class="bm-fav" :src="r.icon" alt=""
                referrerpolicy="no-referrer" loading="lazy"
              />
              <span v-else class="bm-fav-ph">{{ initial(r) }}</span>
            </span>
            <div class="qd-row-main">
              <span class="qd-row-title" :title="r.name">{{ r.name }}</span>
              <span class="qd-row-sub" :title="r.value">{{ rowSub(r) }}</span>
            </div>
            <span
              v-if="statusOf(r.id) && statusOf(r.id) !== 'ok'"
              class="bm-badge"
              :class="`is-${statusOf(r.id)}`"
              :title="health[r.id]?.errMsg || health[r.id]?.checkedAt"
            >{{ badgeText(r) }}</span>
            <span v-if="health[r.id]?.changed" class="bm-badge is-changed">{{ t('bmStatus_changed') }}</span>
            <div class="qd-row-actions">
              <button
                v-if="statusOf(r.id) === 'moved' && health[r.id]?.finalUrl"
                class="qd-row-btn"
                :title="t('bmApplyMoved')"
                @click.stop="applyMoved(health[r.id])"
              ><RefreshCw :size="14" /></button>
              <button class="qd-row-btn" :title="t('bmCopyUrl')" @click.stop="copyUrl(r)">
                <Copy :size="14" />
              </button>
              <button class="qd-row-btn" :title="t('bmOpen')" @click.stop="openBookmark(r)">
                <ExternalLink :size="14" />
              </button>
            </div>
          </div>
          <p v-if="!openItems.length" class="bm-none">{{ t('bmNoMatch') }}</p>
        </section>
      </template>

      <!-- 网格：一个文件夹一张卡，卡片里带前几个站点图标，不进文件夹也能看出装了什么 -->
      <template v-else>
        <div class="bm-grid">
          <article
            v-for="c in cards"
            :key="c.key"
            v-key-click
            class="bm-card"
            :class="{
              'is-select': selectMode,
              'is-domain': viewMode === 'domain',
              'is-picked': isPicked(c),
              'is-drop': dragOverKey === c.key,
              'is-dragging': dragKey === c.key
            }"
            :draggable="canDrag(c)"
            @click="openFolder(c)"
            @dragstart="onDragStart($event, c)"
            @dragover="onDragOver($event, c)"
            @drop.prevent="onDrop(c)"
            @dragend="onDragEnd"
          >
            <label v-if="selectMode && c.collectionId" class="bm-card-check" @click.stop>
              <input type="checkbox" :checked="isPicked(c)" @change="togglePick(c.collectionId)" />
            </label>
            <span v-else-if="canDrag(c)" class="bm-grip" :title="t('bmDragHint')" aria-hidden="true">
              <GripVertical :size="12" />
            </span>

            <div class="bm-card-top">
              <component :is="viewMode === 'folder' ? Folder : Globe2" :size="14" class="bm-card-icon" />
              <span class="bm-card-name" :title="c.label">{{ c.label }}</span>
              <span class="bm-card-n">{{ c.total }}</span>
            </div>

            <div v-if="c.sub" class="bm-card-sub" :title="c.sub">{{ c.sub }}</div>

            <div class="bm-fav-strip">
              <template v-for="r in c.preview" :key="r.id">
                <img
                  v-if="r.icon" class="bm-card-fav" :src="r.icon" alt=""
                  referrerpolicy="no-referrer" loading="lazy"
                />
                <span v-else class="bm-card-fav-ph">{{ initial(r) }}</span>
              </template>
              <span v-if="c.total > c.preview.length" class="bm-fav-more">+{{ c.total - c.preview.length }}</span>
            </div>

            <div class="bm-card-names">{{ c.names.join(' · ') }}</div>

            <div v-if="c.issues" class="bm-card-flags">
              <span v-if="c.broken" class="bm-flag is-broken" :title="t('bmTabBroken')">{{ c.broken }}</span>
              <span v-if="c.blocked" class="bm-flag is-blocked" :title="t('bmTabBlocked')">{{ c.blocked }}</span>
              <span v-if="c.moved" class="bm-flag is-moved" :title="t('bmTabMoved')">{{ c.moved }}</span>
              <span v-if="c.changed" class="bm-flag is-changed" :title="t('bmTabChanged')">{{ c.changed }}</span>
            </div>

            <div v-if="viewMode === 'folder' && !selectMode" class="bm-card-actions" @click.stop>
              <button class="qd-row-btn" :title="t('bmRename')" @click="askRename(c)"><Pencil :size="12" /></button>
              <button class="qd-row-btn" :title="t('delete')" @click="askDelete(c)"><Trash2 :size="12" /></button>
            </div>
          </article>
        </div>
        <p v-if="!cards.length" class="bm-none">{{ loading ? t('loading') : t('bmNoMatch') }}</p>
      </template>
    </div>

    <!-- 重命名文件夹 -->
    <QdModal
      v-if="renameOpen"
      :model-value="true" :title="t('bmRenameTitle')" width="420px"
      @update:model-value="renameOpen = false"
    >
      <label class="bm-field">
        <span class="bm-field-label">{{ t('bmRenameLabel') }}</span>
        <input v-model="renameValue" class="qd-input" @keydown.enter.prevent="confirmRename" />
      </label>
      <template #footer>
        <button class="qd-btn" @click="renameOpen = false">{{ t('cancel') }}</button>
        <button
          class="qd-btn qd-btn-primary"
          :disabled="renameBusy || !renameValue.trim()"
          @click="confirmRename"
        >{{ t('save') }}</button>
      </template>
    </QdModal>

    <!-- 删除文件夹（会连带删掉里面的书签，必须说清条数） -->
    <QdModal
      v-if="deleteTarget"
      :model-value="true" :title="t('bmDeleteTitle')" width="440px"
      @update:model-value="deleteTarget = null"
    >
      <p class="bm-hint">{{ t('bmDeleteBody', { name: deleteTarget.name, n: deleteTarget.total }) }}</p>
      <p class="bm-hint bm-hint-warn">{{ t('bmDeleteWarn') }}</p>
      <template #footer>
        <button class="qd-btn" @click="deleteTarget = null">{{ t('cancel') }}</button>
        <button class="qd-btn qd-btn-danger" :disabled="deleteBusy" @click="confirmDelete">
          <Loader2 v-if="deleteBusy" :size="13" class="spin" />{{ t('delete') }}
        </button>
      </template>
    </QdModal>

    <!-- 批量删除文件夹 -->
    <QdModal
      v-if="batchOpen"
      :model-value="true" :title="t('bmDeleteTitle')" width="440px"
      @update:model-value="batchOpen = false"
    >
      <p class="bm-hint">{{ t('bmBatchDeleteBody', { folders: picked.length, n: pickedTotal }) }}</p>
      <p class="bm-hint bm-hint-warn">{{ t('bmDeleteWarn') }}</p>
      <template #footer>
        <button class="qd-btn" @click="batchOpen = false">{{ t('cancel') }}</button>
        <button class="qd-btn qd-btn-danger" :disabled="batchBusy" @click="confirmBatchDelete">
          <Loader2 v-if="batchBusy" :size="13" class="spin" />{{ t('delete') }}
        </button>
      </template>
    </QdModal>

    <!-- 导入：先选来源（本机浏览器 / HTML 文件），再确认预览 -->
    <QdModal
      v-if="importOpen"
      :model-value="true"
      :title="t('bmImportTitle')"
      width="620px"
      @update:model-value="importOpen = false"
    >
      <template v-if="!preview">
        <div class="bm-src-head">
          <div class="bm-src-titles">
            <span class="bm-src-title">{{ t('bmSrcTitle') }}</span>
            <span class="bm-src-hint">{{ t('bmSrcHint') }}</span>
          </div>
          <button class="qd-btn qd-btn-ghost qd-btn-sm" :disabled="sourcesLoading" @click="loadSources">
            <Loader2 v-if="sourcesLoading" :size="12" class="spin" />
            <RefreshCw v-else :size="12" />
            {{ t('bmRefresh') }}
          </button>
        </div>

        <div v-if="sources.length" class="bm-src-list">
          <div
            v-for="s in sources"
            :key="s.key"
            class="qd-row bm-src-row"
            @click="previewSource(s)"
          >
            <span class="qd-row-icon"><Globe :size="16" /></span>
            <div class="qd-row-main">
              <span class="qd-row-title">
                {{ s.browser }}<span class="bm-src-profile">{{ s.profile }}</span>
              </span>
              <span class="qd-row-sub" :title="s.path">{{ s.path }}</span>
            </div>
            <span class="bm-src-count" :class="{ 'is-bad': s.count < 0 }">
              {{ s.count < 0 ? t('bmSrcReadFail') : t('bmCountN', { n: s.count }) }}
            </span>
            <button class="qd-btn qd-btn-sm" @click.stop="previewSource(s)">{{ t('bmSrcImport') }}</button>
          </div>
        </div>
        <p v-else-if="!sourcesLoading" class="bm-src-empty">
          {{ t('bmSrcEmpty') }}<br />
          <span class="bm-src-hint">{{ t('bmSrcEmptyHint') }}</span>
        </p>

        <div class="bm-or"><span>{{ t('bmOr') }}</span></div>

        <div class="bm-file">
          <div class="bm-file-main">
            <p class="bm-file-title">{{ t('bmImportFileTitle') }}</p>
            <p class="bm-file-hint">{{ t('bmImportFileHint') }}</p>
          </div>
          <button class="qd-btn" @click="pickFile">
            <FilePlus2 :size="14" />{{ t('bmImportFilePick') }}
          </button>
        </div>
      </template>

      <template v-else>
        <div class="bm-preview-head">
          <span class="bm-preview-src" :title="preview.path">{{ previewSourceLabel }}</span>
          <span class="qd-page-fill" />
          <button class="qd-btn qd-btn-ghost qd-btn-sm" @click="preview = null">{{ t('bmBack') }}</button>
        </div>

        <div class="bm-stat-grid">
          <div class="bm-stat">
            <span class="bm-stat-label">{{ t('bmStatRead') }}</span>
            <strong class="bm-stat-value">{{ preview.total }}</strong>
          </div>
          <div class="bm-stat">
            <span class="bm-stat-label">{{ t('bmStatValid') }}</span>
            <strong class="bm-stat-value">{{ preview.fresh }}</strong>
          </div>
          <div class="bm-stat">
            <span class="bm-stat-label">{{ t('bmStatFolders') }}</span>
            <strong class="bm-stat-value">{{ preview.folders }}</strong>
          </div>
        </div>
        <!-- 第二个浏览器导入时大多数网址已经在库里，不提前说明，用户只会看到「导完没反应」 -->
        <p v-if="preview.existing" class="bm-preview-note">{{ t('bmPreviewExisting', { n: preview.existing }) }}</p>
        <p v-if="preview.invalid" class="bm-preview-note">{{ t('bmImportInvalid', { n: preview.invalid }) }}</p>

        <label class="bm-field">
          <span class="bm-field-label">{{ t('bmImportScene') }}</span>
          <input v-model="sceneName" class="qd-input" :placeholder="t('bmImportScenePlaceholder')" />
        </label>

        <div class="bm-sample">
          <span class="bm-field-label">{{ t('bmImportSample') }}</span>
          <div v-for="(s, i) in preview.sample" :key="i" class="bm-sample-row">
            <span class="bm-sample-title" :title="s.title">{{ s.title }}</span>
            <span class="bm-sample-folder">{{ s.folder || t('bmUncategorized') }}</span>
          </div>
        </div>
      </template>

      <template #footer>
        <template v-if="preview">
          <button class="qd-btn" @click="importOpen = false">{{ t('bmClose') }}</button>
          <button class="qd-btn qd-btn-primary" :disabled="importing || !preview.fresh" @click="confirmImport">
            <Loader2 v-if="importing" :size="14" class="spin" />
            {{ t('bmImportConfirm', { n: preview.fresh }) }}
          </button>
        </template>
        <button v-else class="qd-btn" @click="importOpen = false">{{ t('bmClose') }}</button>
      </template>
    </QdModal>

    <!-- 重复合并 -->
    <QdModal
      v-if="dupGroups.length"
      :model-value="true" :title="t('bmDupTitle')" width="720px"
      @update:model-value="dupGroups = []"
    >
      <p class="bm-hint">{{ t('bmDupHint', { n: dupExtra }) }}</p>
      <div class="bm-dup">
        <div v-for="(g, i) in dupGroups" :key="g.key" class="bm-dup-group">
          <div class="bm-dup-head">
            <span class="bm-dup-kind">{{ g.kind === 'url' ? t('bmDupKindUrl') : t('bmDupKindTitle') }}</span>
            <span class="bm-dup-key" :title="g.key">{{ g.key }}</span>
            <span class="qd-page-fill" />
            <button class="qd-btn qd-btn-sm" @click="mergeGroup(i)">{{ t('bmDupMerge') }}</button>
          </div>
          <label v-for="it in g.items" :key="it.id" class="bm-dup-item">
            <input v-model="keepChoice[i]" type="radio" :value="it.id" />
            <span class="bm-dup-name">{{ it.name }}</span>
            <span class="bm-dup-url" :title="it.url">{{ it.url }}</span>
            <span class="bm-dup-coll">{{ it.collectionName }}</span>
          </label>
        </div>
      </div>
      <template #footer>
        <button class="qd-btn" @click="dupGroups = []">{{ t('bmClose') }}</button>
      </template>
    </QdModal>

    <QdModal
      v-if="dupGroups.length === 0 && dupChecked"
      :model-value="true" :title="t('bmDupTitle')" width="420px"
      @update:model-value="dupChecked = false"
    >
      <p class="bm-hint">{{ t('bmDupNone') }}</p>
      <template #footer>
        <button class="qd-btn" @click="dupChecked = false">{{ t('bmClose') }}</button>
      </template>
    </QdModal>

    <!-- 洞察 -->
    <QdModal
      v-if="insight"
      :model-value="true" :title="t('bmInsightTitle')" width="520px"
      @update:model-value="insight = null"
    >
      <div class="bm-stat-grid bm-stat-grid-4">
        <div class="bm-stat">
          <span class="bm-stat-label">{{ t('bmInsightTotalLabel') }}</span>
          <strong class="bm-stat-value">{{ insight.stats.total ?? 0 }}</strong>
        </div>
        <div class="bm-stat">
          <span class="bm-stat-label">{{ t('bmInsightNeverLabel') }}</span>
          <strong class="bm-stat-value">{{ insight.stats.neverOpened ?? 0 }}</strong>
        </div>
        <div class="bm-stat">
          <span class="bm-stat-label">{{ t('bmInsightStaleLabel') }}</span>
          <strong class="bm-stat-value">{{ insight.stats.stale ?? 0 }}</strong>
        </div>
        <div class="bm-stat">
          <span class="bm-stat-label">{{ t('bmInsightDupLabel') }}</span>
          <strong class="bm-stat-value">{{ insight.stats.duplicates ?? 0 }}</strong>
        </div>
      </div>
      <div class="bm-advice">
        <p v-for="a in insight.advice" :key="a.kind" class="bm-advice-row">
          <span>{{ adviceLabel(a.kind) }}</span>
          <b>{{ a.count }}</b>
        </p>
      </div>
      <p v-if="!insight.advice.length" class="bm-hint">{{ t('bmInsightClean') }}</p>
      <template #footer>
        <button class="qd-btn" @click="insight = null">{{ t('bmClose') }}</button>
      </template>
    </QdModal>
  </div>
</template>

<style scoped>
/* 只写本页独有的布局偏移与元素；页头、按钮、输入框、行、空状态一律用全局类。 */
.bm-toolbar { gap: var(--space-3); flex-wrap: wrap; }
.bm-search { width: 240px; flex-shrink: 1; min-width: 150px; }
.bm-view { flex-shrink: 0; }

/* 健康筛选的色点：死链红、无法访问橙、地址已变蓝、内容已变蓝。
   标签文字是灰的，靠这点色把「这里有脏数据」先递到眼睛里。 */
.bm-tab-dot {
  flex-shrink: 0; width: 6px; height: 6px; border-radius: var(--radius-full);
  background: var(--color-text-muted);
}
.bm-tab-dot.is-broken { background: var(--color-danger); }
.bm-tab-dot.is-blocked { background: var(--color-warning); }
.bm-tab-dot.is-moved,
.bm-tab-dot.is-changed { background: var(--color-accent); }
.bm-tab-n {
  font-size: var(--text-2xs); color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

/* ---- 排序菜单 ---- */
.bm-sort { position: relative; flex-shrink: 0; }
.bm-sort-label { max-width: 84px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 铺满整屏的透明背板：点空白处收起，比 document 级监听更好清理 */
.bm-menu-backdrop { position: fixed; inset: 0; z-index: var(--z-dropdown); }
.bm-menu {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: calc(var(--z-dropdown) + 1);
  min-width: 132px; padding: var(--space-2);
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}
.bm-menu-item {
  display: flex; align-items: center; gap: var(--space-2); width: 100%;
  padding: var(--space-2) var(--space-3);
  background: transparent; border: 0; border-radius: var(--radius-sm);
  color: var(--color-text-secondary); font-family: inherit;
  font-size: var(--text-sm); text-align: left; cursor: pointer;
}
.bm-menu-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.bm-menu-item.active { color: var(--color-accent-ink); }
.bm-menu-tick { display: inline-flex; width: 14px; flex-shrink: 0; }

/* ---- 后台任务条 ---- */
.bm-banner {
  display: flex; align-items: center; gap: var(--space-3);
  flex-shrink: 0; margin: 0 var(--space-6); padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-sm);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-size: var(--text-xs);
}
.bm-banner-text { flex-shrink: 0; white-space: nowrap; }
.bm-banner-bar {
  flex: 1; min-width: 60px; height: 3px; overflow: hidden;
  border-radius: var(--radius-full); background: var(--color-accent-muted);
}
.bm-banner-fill {
  display: block; height: 100%; width: 0;
  border-radius: inherit; background: var(--color-accent);
  transition: width var(--transition-slow);
}
/* 补图标拿不到总量，用一条来回跑的光带表示「还在动」，不假造百分比 */
.bm-banner-fill.is-indeterminate {
  width: 32%;
  animation: bm-indeterminate 1.4s ease-in-out infinite;
}
@keyframes bm-indeterminate {
  0% { margin-left: -32%; }
  100% { margin-left: 100%; }
}
.bm-banner-num { flex-shrink: 0; font-variant-numeric: tabular-nums; }

/* ---- 批量条 ---- */
.bm-batch {
  display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-3);
  flex-shrink: 0; margin: 0 var(--space-6); padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-sm);
  background: var(--color-select-bg);
}
.bm-batch-n { font-size: var(--text-sm); font-weight: 600; color: var(--color-accent); }
.bm-batch-sub { font-size: var(--text-xs); color: var(--color-text-muted); }

/* ---- 网格 ---- */
/* minmax(272px)：1280 宽是 4 列，1024 宽是 3 列，窄窗自动落回 2 列。
   卡片之间只隔 10px，一屏能看到十几个文件夹，比原来一条一条往下排密度高得多。 */
.bm-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(272px, 1fr));
  gap: var(--space-4);
  align-content: start;
}
.bm-card {
  position: relative;
  display: flex; flex-direction: column; gap: var(--space-2);
  min-height: 108px;
  /* 左侧多留 8px 给拖拽把手 / 复选框，hover 时才浮出，不占常态版面 */
  padding: var(--space-4) var(--space-5) var(--space-4) var(--space-7);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  background: var(--color-card-bg);
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast), box-shadow var(--transition-fast);
}
.bm-card:hover {
  background: var(--color-bg-hover);
  border-color: var(--color-border-light);
  box-shadow: var(--shadow-sm);
}
.bm-card.is-picked { border-color: var(--color-accent); box-shadow: inset 0 0 0 1px var(--color-accent-border); }
/* 拖拽落点：用一条竖直色条表示「插到这里」，比整卡高亮更接近列表排序的直觉 */
.bm-card.is-drop::before {
  content: '';
  position: absolute; left: -6px; top: 6px; bottom: 6px; width: 2px;
  border-radius: var(--radius-full); background: var(--color-accent);
}
.bm-card.is-dragging { opacity: 0.5; }
.bm-card.is-select { cursor: default; padding-left: var(--space-9); }

/* 拖拽把手 / 复选框占用同一个左上角位置 */
.bm-grip {
  position: absolute; left: 4px; top: 50%; transform: translateY(-50%);
  display: flex; align-items: center; justify-content: center;
  width: 12px; height: 20px;
  color: var(--color-text-muted); cursor: grab; opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast);
}
.bm-card:hover .bm-grip { opacity: 1; }
.bm-grip:hover { color: var(--color-text-secondary); }
.bm-card-check {
  position: absolute; left: 8px; top: var(--space-4);
  display: flex; align-items: center; cursor: pointer;
}

.bm-card-top { display: flex; align-items: center; gap: var(--space-2); min-width: 0; }
.bm-card-icon { flex-shrink: 0; color: var(--color-text-muted); }
.bm-card-name {
  flex: 1; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary);
}
/* 数量用主文字色而不是 muted：它是扫网格时最先要找的信息，
   和右边的 ⋯ 操作错开一层，hover 时让位给操作按钮。 */
.bm-card-n {
  flex-shrink: 0; font-size: var(--text-sm); font-weight: 600;
  color: var(--color-text-primary); font-variant-numeric: tabular-nums;
  transition: opacity var(--transition-fast);
}
.bm-card:not(.is-domain):hover .bm-card-n { opacity: 0; }

.bm-card-sub {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-xs); color: var(--color-text-muted);
}

.bm-fav-strip { display: flex; align-items: center; gap: var(--space-1); }
.bm-card-fav { width: 14px; height: 14px; object-fit: contain; border-radius: 2px; }
.bm-card-fav-ph {
  display: flex; align-items: center; justify-content: center;
  width: 14px; height: 14px; border-radius: 2px;
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  font-size: var(--text-2xs); font-weight: 600; text-transform: uppercase;
}
.bm-fav-more { font-size: var(--text-2xs); color: var(--color-text-muted); }

.bm-card-names {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-xs); color: var(--color-text-secondary);
}

/* 问题计数：贴底排，四类各一色；margin-top:auto 让没有问题的卡片和对齐 */
.bm-card-flags { display: flex; align-items: center; gap: var(--space-2); margin-top: auto; }
.bm-flag {
  display: inline-flex; align-items: center; gap: 2px;
  padding: 0 5px; height: 15px;
  border-radius: var(--radius-full);
  font-size: var(--text-2xs); font-variant-numeric: tabular-nums;
  border: 1px solid transparent;
}
.bm-flag.is-broken { color: var(--color-danger-ink); border-color: var(--color-danger-border); background: var(--color-danger-bg); }
.bm-flag.is-blocked { color: var(--color-warning-ink); border-color: var(--color-warning-border); background: var(--color-warning-bg); }
.bm-flag.is-moved,
.bm-flag.is-changed { color: var(--color-accent); border-color: var(--color-accent-border); background: var(--color-accent-bg); }

.bm-card-actions {
  position: absolute; top: var(--space-3); right: var(--space-3);
  display: flex; gap: var(--space-1); opacity: 0;
  transition: opacity var(--transition-fast);
}
.bm-card:hover .bm-card-actions,
.bm-card:focus-within .bm-card-actions { opacity: 1; }

/* ---- 详情 ---- */
.bm-detail-head {
  display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap;
  flex-shrink: 0;
}
.bm-detail-sep { width: 1px; height: 14px; background: var(--color-border); }
.bm-detail-title {
  display: inline-flex; align-items: center; gap: var(--space-2);
  max-width: 40vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary);
}
.bm-detail-n { font-size: var(--text-xs); color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
.bm-detail { flex-shrink: 0; padding: var(--space-2) var(--space-2) var(--space-3); }

.bm-body { display: flex; flex-direction: column; gap: var(--space-3); }
/* flex-shrink:0：卡片是纵向 flex 的子项，内容总高一旦超过可视区，默认的
   flex-shrink:1 会把每张卡片按比例压扁，配合 overflow:hidden 连字都被裁掉半行。 */
.bm-body > * { flex-shrink: 0; }

.bm-row { cursor: default; }
/* 行尾操作常驻占位、hover 才浮出：一屏几十个图标按钮会让视线没有落点。
   不用 display:none，避免 hover 时行宽跳动。 */
.bm-row .qd-row-actions { opacity: 0; transition: opacity var(--transition-fast); }
.bm-row:hover .qd-row-actions,
.bm-row:focus-within .qd-row-actions { opacity: 1; }

.bm-fav { width: 16px; height: 16px; object-fit: contain; }
.bm-fav-ph {
  display: flex; align-items: center; justify-content: center;
  width: 18px; height: 18px; border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  font-size: var(--text-2xs); font-weight: 600; text-transform: uppercase;
}

.bm-badge {
  flex-shrink: 0; padding: 1px var(--space-2);
  border: 1px solid var(--color-border); border-radius: var(--radius-full);
  font-size: var(--text-2xs); color: var(--color-text-muted); white-space: nowrap;
}
/* 徽标只留描边 + 文字色，不铺底：一屏里死链、被挡、地址已变的条目混排时，
   三块实心色块会把整行的重量从标题抢到右侧边缘。 */
.bm-badge.is-broken { border-color: var(--color-danger-border); color: var(--color-danger-ink); }
.bm-badge.is-blocked { border-color: var(--color-warning-border); color: var(--color-warning-ink); }
.bm-badge.is-moved,
.bm-badge.is-changed { border-color: var(--color-accent-border); color: var(--color-accent); }
.bm-badge.is-ok { border-color: var(--color-success-border); color: var(--color-success-ink); }

.bm-none { padding: var(--space-6); text-align: center; font-size: var(--text-sm); color: var(--color-text-muted); }
.bm-hint { margin: 0 0 var(--space-3); font-size: var(--text-sm); color: var(--color-text-muted); }
.bm-hint-warn { color: var(--color-warning-ink); }

/* ---- 导入弹窗 ---- */
.bm-src-head { display: flex; align-items: flex-start; gap: var(--space-4); margin-bottom: var(--space-2); }
.bm-src-titles { display: flex; flex-direction: column; gap: var(--space-1); min-width: 0; flex: 1; }
.bm-src-title { font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.bm-src-hint { font-size: var(--text-xs); color: var(--color-text-muted); }
.bm-src-list { display: flex; flex-direction: column; }
.bm-src-row { padding-left: var(--space-3); padding-right: var(--space-3); }
.bm-src-profile {
  margin-left: var(--space-3); padding: 0 var(--space-2);
  border: 1px solid var(--color-border); border-radius: var(--radius-full);
  font-size: var(--text-2xs); color: var(--color-text-muted);
}
.bm-src-empty { margin: var(--space-3) 0; font-size: var(--text-sm); color: var(--color-text-muted); }
/* 来源右侧的条数：点进去之前就能确认「这台机器上到底读到了多少条」 */
.bm-src-count {
  flex-shrink: 0; font-size: var(--text-xs); color: var(--color-text-muted);
  font-variant-numeric: tabular-nums; white-space: nowrap;
}
.bm-src-count.is-bad { color: var(--color-danger-ink); }

.bm-or {
  display: flex; align-items: center; gap: var(--space-4);
  margin: var(--space-6) 0 var(--space-5);
  font-size: var(--text-xs); color: var(--color-text-muted);
}
.bm-or::before, .bm-or::after { content: ''; flex: 1; height: 1px; background: var(--color-border); }

.bm-file { display: flex; align-items: center; gap: var(--space-4); }
.bm-file-main { flex: 1; min-width: 0; }
.bm-file-title { margin: 0 0 var(--space-1); font-size: var(--text-md); color: var(--color-text-primary); }
.bm-file-hint { margin: 0; font-size: var(--text-xs); line-height: 1.5; color: var(--color-text-muted); }

.bm-preview-head { display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-4); }
.bm-preview-src {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-sm); color: var(--color-text-secondary);
}
.bm-preview-note { margin: 0 0 var(--space-4); font-size: var(--text-xs); color: var(--color-warning-ink); }

.bm-stat-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--space-3); margin-bottom: var(--space-4); }
.bm-stat-grid-4 { grid-template-columns: repeat(4, 1fr); }
.bm-stat { display: flex; flex-direction: column; gap: var(--space-1); padding: var(--space-3) var(--space-4); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); }
.bm-stat-label { font-size: var(--text-xs); color: var(--color-text-muted); }
.bm-stat-value { font-size: var(--text-xl); font-weight: 600; line-height: 1.2; color: var(--color-text-primary); font-variant-numeric: tabular-nums; }

.bm-field { display: flex; flex-direction: column; gap: var(--space-1); margin-bottom: var(--space-4); }
.bm-field-label { font-size: var(--text-xs); color: var(--color-text-muted); }

.bm-sample { display: flex; flex-direction: column; gap: var(--space-1); padding-top: var(--space-3); border-top: 1px solid var(--color-border); }
.bm-sample-row { display: flex; align-items: baseline; gap: var(--space-3); font-size: var(--text-xs); }
.bm-sample-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-text-secondary); }
.bm-sample-folder { flex-shrink: 0; max-width: 45%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-text-muted); }

/* ---- 查重 ---- */
.bm-dup { display: flex; flex-direction: column; gap: var(--space-3); max-height: 52vh; overflow: auto; }
.bm-dup-group { padding: var(--space-3); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); }
.bm-dup-head { display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-1); }
.bm-dup-kind { flex-shrink: 0; font-size: var(--text-xs); color: var(--color-accent); }
.bm-dup-key { max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-xs); color: var(--color-text-muted); }
.bm-dup-item { display: flex; align-items: center; gap: var(--space-3); padding: 3px 0; font-size: var(--text-sm); cursor: pointer; }
.bm-dup-name { color: var(--color-text-primary); }
.bm-dup-url { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-xs); color: var(--color-text-muted); }
.bm-dup-coll { margin-left: auto; font-size: var(--text-xs); color: var(--color-text-secondary); }

/* ---- 洞察 ---- */
.bm-advice { display: flex; flex-direction: column; gap: var(--space-2); }
.bm-advice-row {
  display: flex; align-items: center; gap: var(--space-2); margin: 0;
  font-size: var(--text-sm); color: var(--color-text-secondary);
}
.bm-advice-row b { font-variant-numeric: tabular-nums; color: var(--color-warning-ink); }

.spin { animation: bm-spin 1s linear infinite; }
@keyframes bm-spin { to { transform: rotate(360deg); } }
</style>
