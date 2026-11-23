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
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { KEEP_ALIVE } from '../pages/keepAlive'
// 显式名字：KeepAlive 的 include 按组件 name 匹配，异步组件解析后读的是内层 SFC
// 的名字，靠文件名推断容易在重构时静默失效（见 pages/keepAlive.ts 的说明）。
defineOptions({ name: KEEP_ALIVE.filesearch })
import {
  Search, File as FileIcon, FolderOpen, ExternalLink, Copy, Loader2, X, BookMarked,
} from '@lucide/vue'
import EverythingGate from './EverythingGate.vue'
import FilePreviewPane from './FilePreviewPane.vue'
import { useToast } from '../composables/useToast'
import { useSaveAs } from '../composables/useSaveAs'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { EnvEverythingSearch } from '../../bindings/quickdock/services/env/environmentservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { CopyText } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { LaunchInstalledApp } from '../../bindings/quickdock/services/appservice'
import type { FileSearchResult } from '../types'

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

const SORTS = [
  { value: 'name', labelKey: 'fileSearchSortName' },
  { value: 'path', labelKey: 'fileSearchSortPath' },
  { value: 'date_modified', labelKey: 'fileSearchSortModified' },
  { value: 'size', labelKey: 'fileSearchSortSize' },
]
const LIMITS = [50, 100, 200]

const selectedRows = computed(() => rows.value.filter(r => !!r.path && selected.value.has(r.path)))
const selectedCount = computed(() => selected.value.size)

async function doSearch() {
  const q = query.value.trim()
  if (!q) { reqId++; rows.value = []; searched.value = false; return }
  const seq = ++reqId
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
  } catch (e) {
    if (seq !== reqId) return
    rows.value = []
    activePath.value = ''
    errorMsg.value = getErrorMessage(e)
    logErr('fileSearch/search', e)
  } finally {
    if (seq === reqId) loading.value = false
  }
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
    toast.error(getErrorMessage(e))
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
      <template #default="{ redetect }">
        <div class="qd-toolbar fs-toolbar">
          <div class="qd-input-wrap fs-query">
            <Search :size="13" />
            <input
              ref="queryInputRef"
              v-model="query"
              class="qd-input"
              :placeholder="t('fileSearchPlaceholder')"
              @keydown.enter.prevent="doSearch"
            />
          </div>
          <select v-model="sortBy" class="qd-input fs-select" :title="t('fileSearchSort')">
            <option v-for="o in SORTS" :key="o.value" :value="o.value">{{ t(o.labelKey) }}</option>
          </select>
          <select v-model.number="limit" class="qd-input fs-select fs-limit" :title="t('fileSearchLimit')">
            <option v-for="n in LIMITS" :key="n" :value="n">{{ n }}</option>
          </select>
          <button class="qd-btn qd-btn-primary qd-btn-sm" :disabled="loading || query.trim().length < 2" @click="doSearch">
            <Loader2 v-if="loading" :size="13" class="spin" />
            <Search v-else :size="13" />
            {{ t('fileSearchSearch') }}
          </button>
          <span v-if="loading" class="fs-count">{{ t('evSearching') }}</span>
          <span v-else-if="searched && !errorMsg" class="fs-count">{{ t('fileSearchResultCount', { n: rows.length }) }}</span>
          <span class="qd-page-fill" />
          <span class="fs-syntax">{{ t('fileSearchSyntaxHint') }}</span>
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
            <div v-else-if="!rows.length" class="qd-empty">
              <div class="qd-empty-icon"><FileIcon :size="22" /></div>
              <p class="qd-empty-title">{{ searched ? t('fileSearchEmpty') : t('fileSearchIdle') }}</p>
              <p class="qd-empty-hint">{{ t('fileSearchEmptyHint') }}</p>
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
            <div v-else class="qd-empty"><p class="qd-empty-hint">{{ t('fileSearchPreviewEmpty') }}</p></div>
          </div>
        </div>
      </template>
    </EverythingGate>
  </div>
</template>

<style scoped>
.fs-page { height: 100%; }
.fs-toolbar { flex-wrap: wrap; }
.fs-query { flex: 1; min-width: 180px; max-width: 420px; }
.fs-select { width: auto; min-width: 92px; }
.fs-limit { min-width: 66px; }
.fs-count { font-size: var(--text-sm); color: var(--color-text-muted); white-space: nowrap; }
.fs-syntax { font-size: var(--text-xs); color: var(--color-text-muted); white-space: nowrap; }

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
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.fs-body { display: flex; flex: 1; min-height: 0; overflow: hidden; }
/* 预览面板的「放大」是个上提状态：隐藏结果列表，让预览吃掉整块宽度 */
.fs-body.is-expanded .fs-list { display: none; }
.spin { animation: fs-spin 1s linear infinite; }
@keyframes fs-spin { to { transform: rotate(360deg); } }
.fs-list {
  flex: 1 1 55%;
  min-width: 0;
  overflow-y: auto;
  padding: var(--space-3) var(--space-4);
  border-right: 1px solid var(--color-border);
}
.fs-preview { flex: 1 1 45%; min-width: 0; overflow: hidden; padding: var(--space-4); }

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
    border-bottom: 1px solid var(--color-border);
  }
  .fs-preview { flex: 1 1 50%; }
}
</style>
