<script setup lang="ts">
import PageHeader from './PageHeader.vue'
// FileSearchPage.vue — Everything 文件检索独立页
//
// 命令面板（Ctrl+K）早就能搜文件，但那是「一边打字一边看 20 条下拉」：
// 只能回车定位文件夹，没法对照预览、没法多选、没法一次复制十条路径。
// 这里补的就是展示层 —— 检索、四态探测、启停与安装全部复用既有实现，
// 后端不加方法、也不重生成 bindings。
//
// 两个刻意的边界：
// 1) 不做过滤表单（目录/大小/后缀）。Everything 的 query 本身就是完整语法，
//    再造一层过滤只会和 es.exe 的语义打架；提供语法提示 + 排序 + 条数即可。
// 2) 不做「移入回收站」。批量删除用户文件不可逆，检索页不该成为删除入口。
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { KEEP_ALIVE } from '../pages/keepAlive'
// 显式名字：KeepAlive 的 include 按组件 name 匹配，异步组件解析后读的是内层 SFC
// 的名字，靠文件名推断容易在重构时静默失效（见 pages/keepAlive.ts 的说明）。
defineOptions({ name: KEEP_ALIVE.filesearch })
import {
  Search, File as FileIcon, FolderOpen, ExternalLink, Copy, X, BookMarked,
  SlidersHorizontal, HelpCircle, History, Zap, Code, FileArchive, CalendarClock,
  HardDrive, RotateCcw,
} from '@lucide/vue'
import EverythingGate from './EverythingGate.vue'
import FilePreviewPane from './FilePreviewPane.vue'
import { useToast } from '../composables/useToast'
import { useSaveAs } from '../composables/useSaveAs'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { removeStored, writeStored } from '../utils/storage'
import { logErr } from '../utils/logger'
import { EnvEverythingSearch } from '../../bindings/quickdock/services/env/environmentservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { CopyText } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { LaunchInstalledApp } from '../../bindings/quickdock/services/appservice'
import type { FileSearchResult } from '../types'

import { reportError } from '../utils/reportError'
const { t } = useI18n()
const toast = useToast()
const sa = useSaveAs()

const query = ref('')
const sortBy = ref('name')
const limit = ref(100)
const rows = ref<FileSearchResult[]>([])
const loading = ref(false)
const errorMsg = ref('')
const searched = ref(false)
const selected = ref<Set<string>>(new Set())
const activePath = ref('')
const previewSearching = ref(false)
const previewExpanded = ref(false)
// 结果列表的键盘焦点索引（与鼠标选中解耦：↑↓ 只移动焦点，Enter 才定位/打开）。
const activeIndex = ref(0)
const listRef = ref<HTMLElement | null>(null)
const queryInputRef = ref<HTMLInputElement | null>(null)

let reqId = 0
let debounceTimer: ReturnType<typeof setTimeout> | null = null
let lastAnchor = 0
// 快捷词条/预设点击要求「点了就搜」，但 query 的 watch 默认还有 350ms 防抖。
// 用这个标记让紧接着触发的那次 watch 走 0 延迟，避免再挂一个定时器导致重复请求。
let flushNext = false

const SORTS = [
  { value: 'name', labelKey: 'fileSearchSortName' },
  { value: 'path', labelKey: 'fileSearchSortPath' },
  { value: 'date_modified', labelKey: 'fileSearchSortModified' },
  { value: 'size', labelKey: 'fileSearchSortSize' },
]
const LIMITS = [50, 100, 200]

// ---- 搜索框内的「语法速查」卡：悬停或点击 ? 打开 ----
const syntaxHover = ref(false)
const syntaxPinned = ref(false)
const syntaxOpen = computed(() => syntaxHover.value || syntaxPinned.value)

// ---- 高级筛选（排序 / 条数）：默认收起，只留一个大输入框 ----
const showAdvanced = ref(false)
const filtersDirty = computed(() => sortBy.value !== 'name' || limit.value !== 100)
function resetFilters() {
  sortBy.value = 'name'
  limit.value = 100
}

// ---- 最近搜索：纯本地，只存最近 6 条，不做服务端同步 ----
const RECENT_KEY = 'quickdock.filesearch.recent'
const RECENT_MAX = 6
const recentSearches = ref<string[]>([])

function loadRecent() {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    const arr = raw ? JSON.parse(raw) : []
    if (Array.isArray(arr)) {
      recentSearches.value = arr.filter((x): x is string => typeof x === 'string' && !!x.trim()).slice(0, RECENT_MAX)
    }
  } catch { /* 本地存储被禁用/内容损坏时静默降级为空 */ }
}
function pushRecent(q: string) {
  const s = q.trim()
  if (s.length < 2) return
  recentSearches.value = [s, ...recentSearches.value.filter(x => x !== s)].slice(0, RECENT_MAX)
  writeStored(RECENT_KEY, JSON.stringify(recentSearches.value))
}
function clearRecent() {
  recentSearches.value = []
  removeStored(RECENT_KEY)
}

// 快捷检索预设：全部是 Everything 原生语法，点一下就填进搜索框并立即检索。
// 取「最近修改 / 代码 / 大文件 / 压缩包」而不是「下载/桌面/文档」——后者的绝对路径
// 要由后端提供（用户目录可能被 OneDrive 重定向），这一轮不加后端方法。
const PRESETS = [
  { q: 'dm:today', labelKey: 'fsQuickToday', icon: CalendarClock, sort: 'date_modified' },
  { q: 'ext:go;ts;tsx;js;vue;py;php;rs', labelKey: 'fsQuickCode', icon: Code },
  { q: 'size:>100mb', labelKey: 'fsQuickLarge', icon: HardDrive, sort: 'size' },
  { q: 'ext:zip;7z;rar;tar;gz', labelKey: 'fsQuickArchive', icon: FileArchive },
] as const

// 上一次检索耗时（毫秒）：状态栏展示，给「引擎在工作」一个可验证的凭据。
const lastMs = ref<number | null>(null)

const selectedRows = computed(() => rows.value.filter(r => !!r.path && selected.value.has(r.path)))
const selectedCount = computed(() => selected.value.size)

onMounted(loadRecent)

async function doSearch() {
  const q = query.value.trim()
  if (!q) { reqId++; rows.value = []; searched.value = false; lastMs.value = null; return }
  const seq = ++reqId
  const t0 = performance.now()
  loading.value = true
  errorMsg.value = ''
  searched.value = true
  try {
    // 后端 3 参接口已够用：query 原样透传 es.exe（含 Everything 语法）、limit 限量、sortBy 排序。
    const res = unwrap<FileSearchResult[]>(await EnvEverythingSearch(q, limit.value, sortBy.value)) ?? []
    if (seq !== reqId) return // 输入已被更新的请求取代
    rows.value = res
    selected.value = new Set()
    activePath.value = res.length ? (res[0].path ?? '') : ''
    pushRecent(q)
  } catch (e) {
    if (seq !== reqId) return
    rows.value = []
    activePath.value = ''
    errorMsg.value = getErrorMessage(e)
    logErr('fileSearch/search', e)
  } finally {
    if (seq === reqId) {
      loading.value = false
      lastMs.value = Math.round(performance.now() - t0)
    }
  }
}

// 回车立刻搜：把待触发的防抖取消掉，否则同一条 query 会在 350ms 后再查一遍。
// 仍然守住「至少 2 个字符」：单字符全盘检索在 Everything 上很贵，状态栏会提示补一个字。
function searchNow() {
  if (query.value.trim().length < 2) return
  if (debounceTimer) clearTimeout(debounceTimer)
  void doSearch()
}

// 快捷词条 / 预设：写入 query 后让紧接着的那次 watch 走 0 延迟（见 flushNext 的说明）。
function applyQuickQuery(q: string) {
  if (!q.trim()) return
  flushNext = true
  query.value = q.trim()
}
function applyPreset(p: (typeof PRESETS)[number]) {
  if ('sort' in p && p.sort) sortBy.value = p.sort
  applyQuickQuery(p.q)
}

// 边打边搜（es.exe 是外部进程，350ms 防抖 + 至少 2 字，比命令面板的 130ms 更保守）。
// 排序/条数变化立即重查；清空时推进 reqId 让在途响应失效，否则旧结果会盖回空列表。
watch([query, sortBy, limit], () => {
  if (debounceTimer) clearTimeout(debounceTimer)
  if (query.value.trim().length < 2) {
    reqId++
    rows.value = []
    selected.value = new Set()
    activePath.value = ''
    searched.value = false
    loading.value = false
    lastMs.value = null
    flushNext = false
    return
  }
  if (flushNext) {
    flushNext = false
    void doSearch()
    return
  }
  debounceTimer = setTimeout(doSearch, 350)
})
onScopeDispose(() => { if (debounceTimer) clearTimeout(debounceTimer) })

// 新结果到位后焦点回到首行，避免残留旧索引指向完全不同的一条。
watch(rows, () => { activeIndex.value = 0 })

function onRowClick(e: MouseEvent, i: number, path: string) {
  activePath.value = path
  const next = new Set(selected.value)
  if (e.shiftKey && rows.value.length) {
    const [a, b] = [Math.min(lastAnchor, i), Math.max(lastAnchor, i)]
    next.clear()
    for (let k = a; k <= b; k++) {
      const p = rows.value[k]?.path
      if (p) next.add(p)
    }
  } else if (e.ctrlKey || e.metaKey) {
    if (next.has(path)) next.delete(path)
    else next.add(path)
    lastAnchor = i
  } else {
    next.clear()
    next.add(path)
    lastAnchor = i
  }
  selected.value = next
}

function selectAll() {
  selected.value = new Set(rows.value.map(r => r.path).filter((p): p is string => !!p))
}
function clearSelection() { selected.value = new Set() }

function fmtTime(v?: number) {
  if (!v) return '—'
  return new Date(v * 1000).toLocaleString()
}

async function copyText(text: string) {
  try {
    unwrap(await CopyText(text))
    toast.success(t('fileSearchCopied'))
  } catch (e) {
    reportError(e, 'FileSearchPage.copyText')
  }
}
async function copyField(field: 'path' | 'name') {
  const text = selectedRows.value
    .map(r => (field === 'path' ? r.path : r.name) ?? '')
    .filter(Boolean)
    .join('\n')
  if (text) await copyText(text)
}
async function runOn(items: FileSearchResult[], kind: 'reveal' | 'open') {
  let ok = 0
  const failed: string[] = []
  for (const r of items) {
    const p = r.path
    if (!p) continue
    try {
      unwrap(await (kind === 'reveal' ? RevealInExplorer(p) : LaunchInstalledApp(p)))
      ok++
    } catch (e) {
      failed.push(r.name || p)
      logErr('fileSearch/action', e)
    }
  }
  if (failed.length) {
    // 部分失败不能只报成功数：列出失败项名称，用户才知道该重试哪几条。
    toast.error(t('fileSearchActionPartial', { ok, fail: failed.length }) + '：' + failed.join('、'))
  } else if (ok) {
    toast.success(t('fileSearchActionDone', { n: ok }))
  } else if (items.length) {
    toast.error(t('fileSearchActionFailed'))
  }
}
function bulk(kind: 'reveal' | 'open') {
  void runOn(selectedRows.value, kind)
}
function rowAction(e: MouseEvent, r: FileSearchResult, kind: 'reveal' | 'open') {
  e.stopPropagation()
  void runOn([r], kind)
}

// 加入知识库：命中文件走磁盘路径管线（可溯源），用鼠标坐标弹出共享菜单。
function addRowToKB(e: MouseEvent, r: FileSearchResult) {
  e.stopPropagation()
  if (!r.path) return
  sa.open(
    { text: r.name || r.path, source: 'filesearch', sourceMeta: { paths: [r.path], label: r.name } },
    { x: e.clientX, y: e.clientY },
  )
}
function addSelectedToKB(e: MouseEvent) {
  const items = selectedRows.value
  if (!items.length) return
  const paths = items.map(r => r.path!).filter(Boolean)
  sa.open(
    {
      text: items.map(r => r.name || r.path).filter(Boolean).join('\n'),
      source: 'filesearch',
      sourceMeta: { paths, label: t('saveAsKB') },
    },
    { x: e.clientX, y: e.clientY },
  )
}

// 容器级键盘导航：预览面板自己不处理 Esc（命令面板里那是文档级 capture 监听）。
// 这里收掉预览内搜索/放大态，并给结果行补 ↑↓ / Enter / Esc，与剪贴板页一致。
function scrollToActive() {
  const items = listRef.value?.querySelectorAll('.fs-row')
  ;(items?.[activeIndex.value] as HTMLElement | undefined)?.scrollIntoView({ block: 'nearest' })
}

function onKeydown(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  // Esc：先收预览内的搜索/放大；否则清空选中与键盘焦点，并阻止冒泡到全局收起页面。
  if (e.key === 'Escape') {
    if (previewSearching.value || previewExpanded.value) {
      previewSearching.value = false
      previewExpanded.value = false
      e.stopPropagation()
      return
    }
    // 语法速查卡优先关：它是从搜索框旁弹出的一层，Esc 的直觉是「收起这个浮层」。
    if (syntaxOpen.value) {
      syntaxPinned.value = false
      syntaxHover.value = false
      e.stopPropagation()
      return
    }
    if (selected.value.size || activeIndex.value) {
      selected.value = new Set()
      activeIndex.value = 0
      e.preventDefault()
      e.stopPropagation()
    }
    return
  }
  const target = e.target as HTMLElement | null
  const tag = target?.tagName
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    // 搜索后焦点通常还在搜索框，↑↓ 必须能在那里接管；但排序/条数下拉与其它输入框不抢。
    if (tag === 'SELECT' || tag === 'TEXTAREA' || tag === 'BUTTON' || tag === 'A') return
    if (tag === 'INPUT' && target !== queryInputRef.value) return
  } else if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA' || tag === 'BUTTON' || tag === 'A') {
    // 搜索框回车是「搜索」，按钮回车是它自身动作，都不能再触发行打开。
    return
  }
  if (!rows.value.length) return
  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault()
      activeIndex.value = activeIndex.value < rows.value.length - 1 ? activeIndex.value + 1 : 0
      scrollToActive()
      break
    case 'ArrowUp':
      e.preventDefault()
      activeIndex.value = activeIndex.value > 0 ? activeIndex.value - 1 : rows.value.length - 1
      scrollToActive()
      break
    case 'Enter': {
      e.preventDefault()
      const r = rows.value[activeIndex.value]
      if (!r?.path) return
      // 与鼠标单击/双击一致：设为当前预览、单选该行，再定位到所在文件夹。
      activePath.value = r.path
      selected.value = new Set([r.path])
      lastAnchor = activeIndex.value
      void runOn([r], 'reveal')
      break
    }
  }
}
</script>

<template>
  <div class="qd-page fs-page" @keydown="onKeydown">
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title">{{ t('fileSearchTitle') }}</h2>
        <span class="qd-page-sub">{{ t('fileSearchSubtitle') }}</span>
      </template>
    </PageHeader>

    <EverythingGate>
      <template #default="{ redetect, status }">
        <div class="qd-toolbar fs-toolbar">
          <!-- 一个输入框承担全部日常操作：输入即搜、回车立即、右侧内嵌语法速查。
               排序/条数是低频设置，收进「高级」展开，不再和搜索框抢同一排。 -->
          <div class="fs-query">
            <div class="qd-input-wrap fs-input-wrap">
              <Search :size="14" />
              <input
                ref="queryInputRef"
                v-model="query"
                class="qd-input fs-input"
                :placeholder="t('fileSearchPlaceholder')"
                spellcheck="false"
                @keydown.enter.prevent="searchNow"
              />
              <button
                v-if="query"
                class="fs-inline-btn fs-clear"
                :title="t('clear')"
                :aria-label="t('clear')"
                @click="query = ''"
              >
                <X :size="12" />
              </button>
              <!-- 只悬停问号这一小块才展开：整条搜索框悬停弹卡会遮挡结果，
                   也太容易误触发。浮层是 zone 的子节点，鼠标移进去不会关。 -->
              <span class="fs-help-zone" @mouseenter="syntaxHover = true" @mouseleave="syntaxHover = false">
                <button
                  class="fs-inline-btn fs-help"
                  :class="{ active: syntaxPinned }"
                  :aria-expanded="syntaxOpen"
                  :title="t('fsSyntaxTitle')"
                  :aria-label="t('fsSyntaxTitle')"
                  @click="syntaxPinned = !syntaxPinned"
                >
                  <HelpCircle :size="13" />
                </button>

                <!-- 语法速查：悬停 / 点击都开，Esc 或移开收起 -->
                <div v-if="syntaxOpen" class="fs-syn-pop" role="dialog" :aria-label="t('fsSyntaxTitle')">
                  <div class="fs-syn-title">{{ t('fsSyntaxTitle') }}</div>
                  <dl class="fs-syn-list">
                    <div><dt>ext:go;ts</dt><dd>{{ t('fsSyntaxExt') }}</dd></div>
                    <div><dt>path:"D:\code"</dt><dd>{{ t('fsSyntaxPath') }}</dd></div>
                    <div><dt>size:&gt;10mb</dt><dd>{{ t('fsSyntaxSize') }}</dd></div>
                    <div><dt>dm:today</dt><dd>{{ t('fsSyntaxDate') }}</dd></div>
                  </dl>
                  <p class="fs-syn-note">{{ t('fsSyntaxBool') }}</p>
                  <p class="fs-syn-note">{{ t('fsSyntaxDebounce') }}</p>
                </div>
              </span>
            </div>
          </div>

          <button
            class="qd-btn qd-btn-sm fs-adv-btn"
            :class="{ active: showAdvanced || filtersDirty }"
            :aria-expanded="showAdvanced"
            @click="showAdvanced = !showAdvanced"
          >
            <SlidersHorizontal :size="13" />
            {{ t('fsAdvanced') }}
            <span v-if="filtersDirty" class="fs-adv-dot" :title="t('fsFiltersCustom')" />
          </button>

          <span class="qd-page-fill" />
          <span v-if="loading" class="fs-count">{{ t('evSearching') }}</span>
        </div>

        <!-- 高级筛选：默认收起，展开后只影响排序与返回条数，不改变 Everything 语法本身 -->
        <div v-if="showAdvanced" class="fs-adv">
          <label class="fs-adv-item">
            <span class="fs-adv-label">{{ t('fileSearchSort') }}</span>
            <select v-model="sortBy" class="qd-input fs-select">
              <option v-for="o in SORTS" :key="o.value" :value="o.value">{{ t(o.labelKey) }}</option>
            </select>
          </label>
          <div class="fs-adv-item">
            <span class="fs-adv-label">{{ t('fileSearchLimit') }}</span>
            <div class="fs-seg" role="group" :aria-label="t('fileSearchLimit')">
              <button
                v-for="n in LIMITS"
                :key="n"
                class="fs-seg-btn"
                :class="{ active: limit === n }"
                :aria-pressed="limit === n"
                @click="limit = n"
              >{{ n }}</button>
            </div>
          </div>
          <span class="qd-page-fill" />
          <button class="qd-btn qd-btn-sm" :disabled="!filtersDirty" @click="resetFilters">
            <RotateCcw :size="12" /> {{ t('fsResetFilters') }}
          </button>
        </div>

        <div v-if="selectedCount" class="fs-bulk">
          <span class="fs-bulk-count">{{ t('fileSearchSelected', { n: selectedCount }) }}</span>
          <button class="qd-btn qd-btn-sm" @click="copyField('path')"><Copy :size="12" /> {{ t('fileSearchBulkCopyPath') }}</button>
          <button class="qd-btn qd-btn-sm" @click="copyField('name')"><Copy :size="12" /> {{ t('fileSearchBulkCopyName') }}</button>
          <button class="qd-btn qd-btn-sm" @click="bulk('reveal')"><FolderOpen :size="12" /> {{ t('fileSearchBulkReveal') }}</button>
          <button class="qd-btn qd-btn-sm" @click="bulk('open')"><ExternalLink :size="12" /> {{ t('fileSearchBulkOpen') }}</button>
          <button class="qd-btn qd-btn-sm" @click="addSelectedToKB"><BookMarked :size="12" /> {{ t('saveAsKB') }}</button>
          <span class="qd-page-fill" />
          <button class="qd-btn qd-btn-sm" @click="selectAll">{{ t('fileSearchSelectAll') }}</button>
          <button class="qd-btn qd-btn-sm" @click="clearSelection"><X :size="12" /> {{ t('fileSearchClearSelection') }}</button>
        </div>

        <p v-if="errorMsg" class="fs-error">{{ t('fileSearchFailed') }}：{{ errorMsg }}
          <button class="qd-btn qd-btn-sm" @click="redetect">{{ t('evRedetect') }}</button>
        </p>

        <div class="fs-body" :class="{ 'is-expanded': previewExpanded }">
          <div ref="listRef" class="fs-list">
            <div v-if="loading && !rows.length" class="qd-empty">
              <p class="qd-empty-hint">{{ t('evSearching') }}</p>
            </div>
            <!-- 未开始检索：只讲「怎么开始」，把索引排查留到真的搜不到时再说 -->
            <div v-else-if="!searched" class="fs-idle">
              <div class="fs-idle-head">
                <div class="qd-empty-icon"><Zap :size="20" /></div>
                <p class="qd-empty-title">{{ t('fileSearchIdle') }}</p>
                <p class="qd-empty-hint">{{ t('fsIdleHint') }}</p>
              </div>

              <div v-if="recentSearches.length" class="fs-quick">
                <div class="fs-quick-head">
                  <History :size="12" />
                  <span>{{ t('fsRecent') }}</span>
                  <button class="fs-quick-clear" @click="clearRecent">{{ t('fsRecentClear') }}</button>
                </div>
                <div class="fs-chips">
                  <button
                    v-for="s in recentSearches"
                    :key="s"
                    class="fs-chip"
                    :title="s"
                    @click="applyQuickQuery(s)"
                  >{{ s }}</button>
                </div>
              </div>

              <div class="fs-quick">
                <div class="fs-quick-head">
                  <Zap :size="12" />
                  <span>{{ t('fsQuickTitle') }}</span>
                </div>
                <div class="fs-presets">
                  <button
                    v-for="p in PRESETS"
                    :key="p.q"
                    class="fs-preset"
                    @click="applyPreset(p)"
                  >
                    <component :is="p.icon" :size="15" class="fs-preset-icon" />
                    <span class="fs-preset-main">
                      <span class="fs-preset-name">{{ t(p.labelKey) }}</span>
                      <span class="fs-preset-q">{{ p.q }}</span>
                    </span>
                  </button>
                </div>
              </div>
            </div>
            <!-- 搜过了但没有结果：先给「怎么改词」，最后一行才提索引。
                 检索报错时让位给上方红色错误条，避免同时出现「失败」和「没命中」两套解释。 -->
            <div v-else-if="!rows.length && !errorMsg" class="fs-idle">
              <div class="fs-idle-head">
                <div class="qd-empty-icon"><FileIcon :size="20" /></div>
                <p class="qd-empty-title">{{ t('fileSearchEmpty') }}</p>
                <p class="qd-empty-hint">{{ t('fileSearchEmptyHint') }}</p>
              </div>
              <p class="fs-index-tip">
                {{ t('fsIndexTip') }}
                <button class="fs-link" @click="redetect">{{ t('evRedetect') }}</button>
              </p>
            </div>
            <template v-else>
              <div v-key-click
                v-for="(r, i) in rows"
                :key="r.path || i"
                :class="['qd-row', 'fs-row', {
                  active: !!r.path && selected.has(r.path),
                  'kbd-active': i === activeIndex
                }]"
                @click="onRowClick($event, i, r.path || '')"
                @dblclick="rowAction($event, r, 'reveal')"
                @contextmenu.prevent="addRowToKB($event, r)"
                @mouseenter="activeIndex = i"
              >
                <span class="qd-row-icon"><FileIcon :size="15" /></span>
                <span class="fs-row-main">
                  <span class="qd-row-title fs-name" :title="r.path">{{ r.name }}</span>
                  <span class="qd-row-sub fs-dir" :title="r.path">{{ r.dir }}</span>
                </span>
                <span class="fs-time">{{ fmtTime(r.modified) }}</span>
                <span class="fs-row-actions">
                  <button class="qd-icon-btn" :title="t('actCopyPath')" @click="r.path && copyText(r.path)"><Copy :size="12" /></button>
                  <button class="qd-icon-btn" :title="t('actReveal')" @click="rowAction($event, r, 'reveal')"><FolderOpen :size="12" /></button>
                  <button class="qd-icon-btn" :title="t('fileSearchOpenDefault')" @click="rowAction($event, r, 'open')"><ExternalLink :size="12" /></button>
                  <button class="qd-icon-btn" :title="t('saveAsKB')" @click="addRowToKB($event, r)"><BookMarked :size="12" /></button>
                </span>
              </div>
            </template>
          </div>

          <div class="fs-preview">
            <FilePreviewPane
              v-if="activePath"
              :path="activePath"
              :searching="previewSearching"
              :expanded="previewExpanded"
              @update:searching="previewSearching = $event"
              @update:expanded="previewExpanded = $event"
            />
            <!-- 预览区空态：用一块半透明的「代码块 + 图片」示意图说明这里能干什么，
                 比一句「选中左侧结果可预览」更像产品界面，也更少阅读负担。 -->
            <div v-else class="fs-prev-empty">
              <div class="fs-mock" aria-hidden="true">
                <div class="fs-mock-head">
                  <span class="fs-mock-dot" /><span class="fs-mock-dot" /><span class="fs-mock-dot" />
                  <span class="fs-mock-file">main.go</span>
                </div>
                <div class="fs-mock-body">
                  <div class="fs-mock-line" style="width: 78%"><span class="fs-mock-kw" /></div>
                  <div class="fs-mock-line" style="width: 56%"><span class="fs-mock-str" /></div>
                  <div class="fs-mock-line" style="width: 88%" />
                  <div class="fs-mock-line" style="width: 64%" />
                  <div class="fs-mock-line" style="width: 42%" />
                </div>
                <div class="fs-mock-foot">
                  <span class="fs-mock-thumb" />
                  <span class="fs-mock-thumb fs-mock-thumb--alt" />
                  <span class="fs-mock-thumb" />
                </div>
              </div>
              <p class="fs-prev-text">{{ t('fileSearchPreviewEmpty') }}</p>
              <div class="fs-prev-tags">
                <span>{{ t('fsPreviewCode') }}</span>
                <span>{{ t('fsPreviewImage') }}</span>
                <span>{{ t('fsPreviewText') }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- 状态栏：只报可验证的事实（引擎在跑 / 版本 / 上次往返耗时），不做装饰性指标 -->
        <div class="fs-statusbar">
          <span class="fs-status-dot" :class="{ ok: status?.state === 'running' && status?.enabled !== false }" />
          <span class="fs-status-main">
            {{ status?.state === 'running' && status?.enabled !== false ? t('fsStatusConnected') : t('fsStatusNotReady') }}
          </span>
          <span v-if="status?.version" class="fs-status-dim">{{ status.version }}</span>
          <span class="fs-status-sep" />
          <span class="fs-status-dim">
            {{ lastMs !== null ? t('fsStatusLast', { ms: lastMs }) : t('fsStatusIdle') }}
          </span>
          <span class="qd-page-fill" />
          <span v-if="searched && !loading && !errorMsg" class="fs-status-dim">
            {{ t('fileSearchResultCount', { n: rows.length }) }}
          </span>
          <span v-if="query.trim().length === 1" class="fs-status-dim">{{ t('fsMinChars') }}</span>
        </div>
      </template>
    </EverythingGate>
  </div>
</template>

<style scoped>
.fs-page { height: 100%; min-height: 0; }
.fs-toolbar { flex-wrap: wrap; gap: var(--space-4); }

/* ---- 搜索框：整页唯一的主操作，给它一档更高的重量 ---- */
.fs-query { position: relative; flex: 1 1 320px; max-width: 640px; min-width: 200px; }
.fs-input-wrap .qd-input.fs-input {
  height: 34px;
  padding-right: 62px;
  border-color: var(--color-border-light);
  background: var(--color-bg-tertiary);
  font-size: var(--text-md);
}
.fs-input-wrap .qd-input.fs-input:hover:not(:disabled) { border-color: var(--color-border-focus-soft); }
/* 聚焦：主题蓝描边 + 一圈极淡光晕。以前只换 border-color-soft，深色下几乎看不出来。 */
.fs-input-wrap .qd-input.fs-input:focus {
  border-color: var(--color-accent);
  background: var(--color-bg-primary);
  box-shadow: 0 0 0 3px var(--color-accent-bg);
}
.fs-inline-btn {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 0;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.fs-inline-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.fs-clear { right: 31px; }
.fs-help { right: 7px; }
.fs-help.active { color: var(--color-accent); background: var(--color-accent-bg); }

/* 语法速查卡：挂在搜索框下方，不再在工具条右侧挂一行没人读的提示 */
.fs-syn-pop {
  position: absolute;
  top: calc(100% + 8px);
  left: 0;
  z-index: var(--z-dropdown);
  width: min(380px, 100%);
  padding: var(--space-4) var(--space-5);
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}
.fs-syn-title {
  margin-bottom: var(--space-3);
  font-size: var(--text-sm);
  font-weight: 600;
  color: var(--color-text-primary);
}
.fs-syn-list { display: flex; flex-direction: column; gap: 6px; margin: 0; }
.fs-syn-list > div { display: flex; align-items: baseline; gap: var(--space-3); }
.fs-syn-list dt {
  flex-shrink: 0;
  width: 98px;
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-family: var(--font-mono);
  font-size: 11px;
}
.fs-syn-list dd {
  margin: 0;
  font-size: var(--text-xs);
  line-height: 1.5;
  color: var(--color-text-muted);
}
.fs-syn-note {
  margin: var(--space-3) 0 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-muted);
}

/* ---- 高级筛选 ---- */
.fs-adv-btn { position: relative; }
.fs-adv-btn.active {
  color: var(--color-accent);
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
}
.fs-adv-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--color-accent);
}
.fs-adv {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-5);
  flex-shrink: 0;
  padding: var(--space-3) var(--space-6);
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.fs-adv-item { display: flex; align-items: center; gap: var(--space-3); }
.fs-adv-label { font-size: var(--text-xs); color: var(--color-text-muted); }
.fs-select { width: auto; min-width: 92px; }
.fs-seg {
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}
.fs-seg-btn {
  height: 22px;
  padding: 0 9px;
  border: 0;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  font-family: inherit;
  font-size: var(--text-xs);
  font-variant-numeric: tabular-nums;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.fs-seg-btn:hover { color: var(--color-text-primary); }
.fs-seg-btn.active {
  background: var(--color-bg-primary);
  color: var(--color-accent);
  box-shadow: var(--shadow-sm);
}

.fs-count { font-size: var(--text-sm); color: var(--color-text-muted); white-space: nowrap; }

.fs-bulk {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-3);
  flex-shrink: 0;
  padding: var(--space-3) var(--space-6);
  border-bottom: 1px solid var(--color-border);
  background: var(--color-accent-bg);
}
.fs-bulk-count { font-size: var(--text-sm); font-weight: 600; color: var(--color-accent); }
.fs-error {
  margin: 0;
  padding: var(--space-3) var(--space-6);
  font-size: var(--text-sm);
  color: var(--color-danger-ink);
  background: var(--color-danger-bg);
}

.fs-body { display: flex; flex: 1; min-height: 0; overflow: hidden; }
/* 预览面板的「放大」是个上提状态：隐藏结果列表，让预览吃掉整块宽度 */
.fs-body.is-expanded .fs-list { display: none; }
.fs-list {
  flex: 1 1 55%;
  min-width: 0;
  overflow-y: auto;
  padding: var(--space-3) var(--space-4);
  /* 两栏分界：--color-border 在深色下几乎等于底色，改用亮一档的 border-light */
  border-right: 1px solid var(--color-border-light);
}
.fs-preview {
  flex: 1 1 45%;
  min-width: 0;
  overflow: hidden;
  padding: var(--space-4);
  /* 右侧预览区比列表区弱亮一档，即使不画线也能看出这是两块功能区 */
  background: var(--color-bg-secondary);
}

/* ---- 左侧空态 ---- */
.fs-idle {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-7);
  padding: 44px var(--space-6) 32px;
}
.fs-idle-head { display: flex; flex-direction: column; align-items: center; text-align: center; }
.fs-quick { width: 100%; max-width: 520px; }
.fs-quick-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-bottom: var(--space-3);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}
.fs-quick-clear {
  margin-left: auto;
  padding: 0;
  border: 0;
  background: none;
  color: var(--color-text-muted);
  font-family: inherit;
  font-size: var(--text-xs);
  cursor: pointer;
}
.fs-quick-clear:hover { color: var(--color-text-primary); }
.fs-chips { display: flex; flex-wrap: wrap; gap: var(--space-2); }
.fs-chip {
  max-width: 100%;
  padding: 3px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-xs);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.fs-chip:hover {
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
  color: var(--color-accent);
}
.fs-presets {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-3);
}
.fs-preset {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 9px var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-card-bg);
  color: var(--color-text-secondary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}
.fs-preset:hover { background: var(--color-bg-hover); border-color: var(--color-accent-border); }
.fs-preset-icon { flex-shrink: 0; color: var(--color-text-muted); }
.fs-preset:hover .fs-preset-icon { color: var(--color-accent); }
.fs-preset-main { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
.fs-preset-name { font-size: var(--text-sm); color: var(--color-text-primary); }
.fs-preset-q {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fs-index-tip {
  margin: 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}
.fs-link {
  padding: 0;
  border: 0;
  background: none;
  color: var(--color-accent);
  font-family: inherit;
  font-size: inherit;
  text-decoration: underline;
  text-underline-offset: 2px;
  cursor: pointer;
}

/* ---- 右侧空态：示意图 + 能力说明 ---- */
.fs-prev-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-4);
  height: 100%;
  text-align: center;
}
.fs-mock {
  width: min(320px, 86%);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-card-bg);
  box-shadow: var(--shadow-card);
  opacity: 0.85;
  overflow: hidden;
}
.fs-mock-head {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}
.fs-mock-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--color-border-light); }
.fs-mock-file {
  margin-left: 6px;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-muted);
}
.fs-mock-body { display: flex; flex-direction: column; gap: 6px; padding: 12px; }
.fs-mock-line {
  height: 6px;
  border-radius: var(--radius-2xs);
  background: var(--color-border-light);
  opacity: 0.5;
}
.fs-mock-kw,
.fs-mock-str { display: block; height: 100%; border-radius: var(--radius-2xs); }
.fs-mock-kw { width: 30%; background: var(--color-accent); opacity: 0.55; }
.fs-mock-str { width: 45%; background: var(--color-cat-ai); opacity: 0.5; }
.fs-mock-foot { display: flex; gap: 6px; padding: 0 12px 12px; }
.fs-mock-thumb {
  flex: 1;
  height: 34px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}
.fs-mock-thumb--alt { background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.fs-prev-text { margin: 0; font-size: var(--text-sm); color: var(--color-text-muted); }
.fs-prev-tags { display: flex; flex-wrap: wrap; justify-content: center; gap: var(--space-2); }
.fs-prev-tags > span {
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  font-size: 11px;
  color: var(--color-text-muted);
}

/* ---- 底部状态栏 ---- */
.fs-statusbar {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
  height: 30px;
  padding: 0 var(--space-6);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}
.fs-status-dot {
  width: 6px;
  height: 6px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--color-warning);
}
.fs-status-dot.ok { background: var(--color-success); box-shadow: 0 0 0 3px var(--color-success-bg); }
.fs-status-main { color: var(--color-text-secondary); }
.fs-status-dim { color: var(--color-text-muted); white-space: nowrap; }
.fs-status-sep { width: 1px; height: 10px; background: var(--color-border-light); }

.fs-row { gap: var(--space-4); }
/* 与全局选中态同一套底色/竖条体系：Ctrl 多选（active）保留 select-bg 但不画竖条，
   键盘焦点（kbd-active）多一条 3px 竖条，↑↓ 移动时两者始终可辨。 */
.fs-row.active::before { display: none; }
.fs-row.kbd-active { background: var(--color-select-bg); }
.fs-row.kbd-active::before {
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
.fs-row-main { display: flex; flex-direction: column; flex: 1; min-width: 0; gap: var(--space-1); }
.fs-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fs-dir { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-xs); }
.fs-time { flex-shrink: 0; font-size: var(--text-xs); color: var(--color-text-muted); white-space: nowrap; }
.fs-row-actions { display: flex; flex-shrink: 0; gap: var(--space-1); opacity: 0; }
.fs-row:hover .fs-row-actions, .fs-row.active .fs-row-actions { opacity: 1; }

/* 窄窗（约 900px 以下）双栏会把列表和预览都挤得太窄，改为上下堆叠：
   列表占上半、预览移到下方，各自保留独立滚动，不再横向抢宽度。 */
@media (max-width: 900px) {
  .fs-body { flex-direction: column; }
  .fs-list {
    flex: 1 1 50%;
    border-right: none;
    border-bottom: 1px solid var(--color-border-light);
  }
  .fs-preview { flex: 1 1 50%; }
  .fs-presets { grid-template-columns: 1fr; }
  .fs-query { max-width: none; }
}
</style>
