<script setup lang="ts">
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
import { computed, onMounted, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { KEEP_ALIVE } from '../pages/keepAlive'
import {
  Bookmark, ChevronDown, Copy, CopyCheck, ExternalLink, FilePlus2, Globe, Link2Off, Loader2, Plus,
  RefreshCw, Search, Sparkles, X,
} from '@lucide/vue'
import QdModal from './QdModal.vue'
import QdTabs from './QdTabs.vue'
import { useToast } from '../composables/useToast'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { SelectLocalFile } from '../../bindings/quickdock/services/appservice'
import { OpenItem } from '../../bindings/quickdock/services/collection/collectionservice'
import {
  ApplyMovedURL, BuildCurationInsight, FillIcons, FindDuplicates, ImportBookmarks, ImportBrowserBookmarks,
  LinkCheckStatus, ListBookmarks, ListBrowserSources, ListHealth, MergeDuplicates, PreviewBrowserImport,
  PreviewImport, StartLinkCheck,
} from '../../bindings/quickdock/services/bookmark/bookmarkservice'

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
// 之前把两级拼成「组 / 集合」平铺，几百条书签就是几十个无法收起的标题——书签页最常见的
// 操作恰恰是「先收起不关心的大类，只看剩下的」。所以两级都可折叠，状态记在 localStorage。
type LeafGroup = { key: string; label: string; items: BookmarkRow[] }
type RootGroup = {
  key: string
  label: string
  scene: string
  total: number
  leaves: LeafGroup[]
  // 只有一个集合时不再画第二层：多一层什么也没分组，反而多一次点击
  flat: boolean
  // 场景标签只在「换了浏览器」的那一行出现：十几个组全挂着同一个「Edge」
  // 只是噪声，扫列表时反而看不见真正的分组信息
  showScene: boolean
}

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
        showScene: true,
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

  // 每个分组都显示场景名（浏览器名）：用户导入多个浏览器时，按场景定位比只看文件夹
  // 名字更快；只有一个场景时也保持一致，不会「有时有有时没有」。
  return roots
})

// 折叠状态按 key 记住：一级 key 是「场景|组名」，二级 key 是集合 id，前缀区分避免撞键
const COLLAPSE_KEY = 'quickdock.bookmarks.collapsed'
const collapsed = ref<Record<string, boolean>>(readCollapsed())

function readCollapsed(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(COLLAPSE_KEY)
    const parsed = raw ? JSON.parse(raw) : null
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, boolean>) : {}
  } catch {
    return {}
  }
}
function persistCollapsed() {
  try {
    localStorage.setItem(COLLAPSE_KEY, JSON.stringify(collapsed.value))
  } catch {
    /* 隐私模式下写不进，折叠照旧按内存状态工作 */
  }
}
// 搜索或筛选时强制展开：命中结果藏在折叠组里，等于告诉用户「没搜到」
const forceExpand = computed(() => keyword.value.trim() !== '' || tab.value !== 'all')
function isCollapsed(key: string) {
  return !forceExpand.value && !!collapsed.value[key]
}
function toggleCollapse(key: string) {
  if (collapsed.value[key]) delete collapsed.value[key]
  else collapsed.value[key] = true
  persistCollapsed()
}
const allCollapsed = computed(() => groups.value.length > 0 && groups.value.every((g) => isCollapsed(g.key)))
function toggleAllCollapsed() {
  const next: Record<string, boolean> = {}
  if (!allCollapsed.value) for (const g of groups.value) next[g.key] = true
  collapsed.value = next
  persistCollapsed()
}

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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
  try {
    for (let round = 0; round < 6; round++) {
      const res = unwrap<{ checked: number; filled: number }>(await FillIcons(24))
      if (!res || res.filled === 0) break
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
  }
}

async function copyUrl(r: BookmarkRow) {
  try {
    await navigator.clipboard.writeText(r.value)
    toast.success(t('bmCopied'))
  } catch (e) {
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
    toast.error(getErrorMessage(e))
    logErr('bookmark', e)
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
      <div class="qd-input-wrap bm-search">
        <Search :size="14" />
        <input v-model="keyword" class="qd-input" :placeholder="t('bmSearchPlaceholder')" />
        <button v-if="keyword" class="qd-icon-btn bm-clear" :title="t('bmClear')" @click="keyword = ''">
          <X :size="14" />
        </button>
      </div>
      <QdTabs v-model="tab" :tabs="tabs" variant="chip" />
      <span class="qd-page-fill" />
      <!-- 搜索/筛选时分组是强制展开的，这时「全部收起」按了也不会生效，干脆不显示 -->
      <button
        v-if="!forceExpand && groups.length"
        class="qd-btn qd-btn-ghost qd-btn-sm"
        @click="toggleAllCollapsed"
      >{{ allCollapsed ? t('bmExpandAll') : t('bmCollapseAll') }}</button>
      <span v-if="checking?.running" class="bm-note">
        <Loader2 :size="12" class="spin" />{{ progressText }}
      </span>
      <span v-else-if="iconsBusy" class="bm-note">
        <Loader2 :size="12" class="spin" />{{ t('bmIconsFetching') }}
      </span>
    </div>

    <div class="qd-page-body bm-body">
      <div v-if="!loading && !rows.length" class="qd-empty">
        <div class="qd-empty-icon"><Bookmark :size="20" /></div>
        <p class="qd-empty-title">{{ t('bmEmpty') }}</p>
        <p class="qd-empty-hint">{{ t('bmEmptyHint') }}</p>
        <button class="qd-empty-cta" @click="openImport"><Plus :size="14" />{{ t('bmImport') }}</button>
      </div>

      <template v-else>
        <section v-for="root in groups" :key="root.key" class="qd-card bm-group">
          <button
            class="bm-group-head"
            :class="{ 'is-collapsed': isCollapsed(root.key) }"
            :aria-expanded="!isCollapsed(root.key)"
            @click="toggleCollapse(root.key)"
          >
            <ChevronDown :size="12" class="bm-chev" />
            <span class="bm-group-name" :title="root.label">{{ root.label }}</span>
            <span v-if="root.showScene" class="bm-group-scene">{{ root.scene }}</span>
            <span class="qd-page-fill" />
            <span class="bm-group-n">{{ root.total }}</span>
          </button>

          <template v-if="!isCollapsed(root.key)">
            <div v-for="leaf in root.leaves" :key="leaf.key" class="bm-leaf">
              <button
                v-if="!root.flat"
                class="bm-leaf-head"
                :class="{ 'is-collapsed': isCollapsed(leaf.key) }"
                :aria-expanded="!isCollapsed(leaf.key)"
                @click="toggleCollapse(leaf.key)"
              >
                <ChevronDown :size="12" class="bm-chev" />
                <span class="bm-leaf-name" :title="leaf.label">{{ leaf.label }}</span>
                <span class="qd-page-fill" />
                <span class="bm-group-n">{{ leaf.items.length }}</span>
              </button>

              <template v-if="root.flat || !isCollapsed(leaf.key)">
                <div
                  v-for="r in leaf.items"
                  :key="r.id"
                  class="qd-row bm-row"
                  @dblclick="openBookmark(r)"
                >
                  <span class="qd-row-icon">
                    <img
                      v-if="r.icon" class="bm-fav" :src="r.icon" alt=""
                      referrerpolicy="no-referrer" loading="lazy"
                    />
                    <span v-else class="bm-fav-ph">{{ initial(r) }}</span>
                  </span>
                  <div class="qd-row-main">
                    <span class="qd-row-title" :title="r.name">{{ r.name }}</span>
                    <span class="qd-row-sub" :title="r.value">
                      {{ hostOf(r.value) }}<template v-if="r.remark"> · {{ r.remark }}</template>
                    </span>
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
              </template>
            </div>
          </template>
        </section>
        <p v-if="!filtered.length" class="bm-none">{{ t('bmNoMatch') }}</p>
      </template>
    </div>

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
.bm-toolbar { gap: var(--space-3); }
.bm-search { width: 260px; flex-shrink: 1; min-width: 150px; }
.bm-clear { position: absolute; right: 2px; width: 22px; height: 22px; }
.bm-note {
  display: inline-flex; align-items: center; gap: var(--space-2); margin-left: var(--space-3);
  font-size: var(--text-xs); color: var(--color-text-muted); white-space: nowrap;
}

/* 分组 = 卡片。全站的「一组内容」都是 .qd-card（下载任务、插件、笔记面板、统计块），
   页面主体也统一留 18/20px 内边距；这里把分组做成通栏灰带 + 发丝线，行贴到窗口
   边缘、组头跟条目行同高同色，整页读起来像一张表格，和其余页面完全两个世界。
   一级组头 34px、二级 30px，卡片内部靠缩进和字重分层，不再用底色分块。 */
.bm-body { display: flex; flex-direction: column; gap: var(--space-3); }
/* flex-shrink:0：卡片是纵向 flex 的子项，内容总高一旦超过可视区，默认的
   flex-shrink:1 会把每张卡片按比例压扁，配合卡片上的 overflow:hidden，
   组头连字都被裁掉半行（20 张卡片时每个只剩十几像素高）。空状态同理。 */
.bm-body > * { flex-shrink: 0; }
.bm-group { display: flex; flex-direction: column; overflow: hidden; }
.bm-leaf { display: flex; flex-direction: column; }

.bm-group-head,
.bm-leaf-head {
  display: flex; align-items: center; gap: var(--space-3);
  width: 100%; margin: 0; padding: 0 var(--space-5);
  border: 0;
  box-sizing: border-box;
  background: transparent;
  font-family: inherit; text-align: left; color: inherit;
  cursor: pointer;
  transition: background-color var(--transition-fast);
}
.bm-group-head {
  height: 34px;
  font-size: var(--text-md);
  /* 展开时用一条线收住组头；收起时卡片只有组头高，这条线会和卡片下缘重合 */
  border-bottom: 1px solid var(--color-border);
}
.bm-group-head.is-collapsed { border-bottom: 0; }
.bm-leaf-head {
  height: 30px;
  padding-left: var(--space-8);
  font-size: var(--text-sm);
}
/* 二级之间才画线：第一层二级紧贴组头的 1px 线，再加一条会变成双线 */
.bm-leaf + .bm-leaf .bm-leaf-head { border-top: 1px solid var(--color-border); }
.bm-group-head:hover,
.bm-leaf-head:hover { background: var(--color-bg-hover); }
.bm-chev {
  flex-shrink: 0; color: var(--color-text-muted);
  transition: transform var(--transition-fast);
}
.is-collapsed .bm-chev { transform: rotate(-90deg); }

.bm-group-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-weight: 600; color: var(--color-text-primary);
}
.bm-leaf-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-weight: 500; color: var(--color-text-secondary);
}
/* 场景用低调的前缀文字，不做描边药丸：十几个组各挂一个「Edge」时，
   一圈圈边框会把注意力拉到「这台机器有几个浏览器」这件无关的事上。 */
.bm-group-scene {
  flex-shrink: 0; padding-left: var(--space-3);
  border-left: 1px solid var(--color-border);
  font-size: var(--text-xs); color: var(--color-text-muted);
}
.bm-group-n {
  flex-shrink: 0; color: var(--color-text-muted);
  font-size: var(--text-xs); font-variant-numeric: tabular-nums;
}

.bm-row { cursor: default; }
/* 二级下面的条目要落在二级组头右侧，否则子项看着比父标题更靠左，层级是反的。
   三级缩进：一级文字 26px → 二级文字 70px → 条目标题 76px。 */
.bm-leaf > .qd-row { padding-left: var(--space-9); }
/* 组头紧贴首行会显得挤，给条目区留一点呼吸；行自身的 margin 只负责行间距 */
.bm-leaf > .qd-row:first-of-type { margin-top: var(--space-1); }
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
.bm-badge.is-broken { border-color: var(--color-danger-border); color: var(--color-danger); }
.bm-badge.is-blocked { border-color: var(--color-warning-border); color: var(--color-warning); }
.bm-badge.is-moved,
.bm-badge.is-changed { border-color: var(--color-accent-border); color: var(--color-accent); }
.bm-badge.is-ok { border-color: var(--color-success-border); color: var(--color-success); }

.bm-none { padding: var(--space-6); text-align: center; font-size: var(--text-sm); color: var(--color-text-muted); }
.bm-hint { margin: 0 0 var(--space-3); font-size: var(--text-sm); color: var(--color-text-muted); }

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
.bm-src-count.is-bad { color: var(--color-danger); }

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
.bm-preview-note { margin: 0 0 var(--space-4); font-size: var(--text-xs); color: var(--color-warning); }

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
.bm-advice-row b { font-variant-numeric: tabular-nums; color: var(--color-warning); }

.spin { animation: bm-spin 1s linear infinite; }
@keyframes bm-spin { to { transform: rotate(360deg); } }
</style>
