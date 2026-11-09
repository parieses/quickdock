<script setup lang="ts">
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
  Search, RefreshCw, File as FileIcon, FolderOpen, ExternalLink, Copy, Loader2, X,
} from '@lucide/vue'
import EverythingGate from './EverythingGate.vue'
import FilePreviewPane from './FilePreviewPane.vue'
import { useToast } from '../composables/useToast'
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
async function runOn(paths: string[], kind: 'reveal' | 'open') {
  let ok = 0
  for (const p of paths) {
    try {
      unwrap(await (kind === 'reveal' ? RevealInExplorer(p) : LaunchInstalledApp(p)))
      ok++
    } catch (e) {
      logErr('fileSearch/action', e)
    }
  }
  if (ok) toast.success(t('fileSearchActionDone', { n: ok }))
  else if (paths.length) toast.error(t('fileSearchActionFailed'))
}
function bulk(kind: 'reveal' | 'open') {
  void runOn(selectedRows.value.map(r => r.path!).filter(Boolean), kind)
}
function rowAction(e: MouseEvent, r: FileSearchResult, kind: 'reveal' | 'open') {
  e.stopPropagation()
  if (r.path) void runOn([r.path], kind)
}

// 预览面板自己不处理 Esc（命令面板里那是文档级 capture 监听）。这里只在容器上
// 收掉预览内搜索与放大态，避免 Esc 冒泡到全局把整个页面收起。
function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape' || e.isComposing || e.keyCode === 229) return
  if (previewSearching.value || previewExpanded.value) {
    previewSearching.value = false
    previewExpanded.value = false
    e.stopPropagation()
  }
}
</script>

<template>
  <div class="qd-page fs-page" @keydown="onKeydown">
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('fileSearchTitle') }}</h2>
        <span class="qd-page-sub">{{ t('fileSearchSubtitle') }}</span>
      </div>
      <div class="qd-page-actions">
        <button class="qd-btn qd-btn-sm" :disabled="loading" @click="doSearch">
          <RefreshCw :size="13" /> {{ t('evTestSearch') }}
        </button>
      </div>
    </div>

    <EverythingGate>
      <template #default="{ redetect }">
        <div class="qd-toolbar fs-toolbar">
          <div class="qd-input-wrap fs-query">
            <Search :size="13" />
            <input
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
          <span class="qd-page-fill" />
          <button class="qd-btn qd-btn-sm" @click="selectAll">{{ t('fileSearchSelectAll') }}</button>
          <button class="qd-btn qd-btn-sm" @click="clearSelection"><X :size="12" /> {{ t('fileSearchClearSelection') }}</button>
        </div>

        <p v-if="errorMsg" class="fs-error">{{ t('fileSearchFailed') }}：{{ errorMsg }}
          <button class="qd-btn qd-btn-sm" @click="redetect">{{ t('evRedetect') }}</button>
        </p>

        <div class="fs-body" :class="{ 'is-expanded': previewExpanded }">
          <div class="fs-list">
            <div v-if="loading && !rows.length" class="qd-empty">
              <p class="qd-empty-hint">{{ t('evSearching') }}</p>
            </div>
            <div v-else-if="!rows.length" class="qd-empty">
              <div class="qd-empty-icon"><FileIcon :size="22" /></div>
              <p class="qd-empty-title">{{ searched ? t('fileSearchEmpty') : t('fileSearchIdle') }}</p>
              <p class="qd-empty-hint">{{ t('fileSearchEmptyHint') }}</p>
            </div>
            <template v-else>
              <div
                v-for="(r, i) in rows"
                :key="r.path || i"
                :class="['qd-row', 'fs-row', { active: !!r.path && selected.has(r.path) }]"
                @click="onRowClick($event, i, r.path || '')"
                @dblclick="rowAction($event, r, 'reveal')"
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
.fs-count { font-size: 12px; color: var(--color-text-muted); white-space: nowrap; }
.fs-syntax { font-size: 11px; color: var(--color-text-muted); white-space: nowrap; }

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
.fs-bulk-count { font-size: 12px; font-weight: 600; color: var(--color-accent); }
.fs-error {
  margin: 0;
  padding: var(--space-3) var(--space-6);
  font-size: 12px;
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
.fs-row-main { display: flex; flex-direction: column; flex: 1; min-width: 0; gap: 2px; }
.fs-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fs-dir { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; }
.fs-time { flex-shrink: 0; font-size: 11px; color: var(--color-text-muted); white-space: nowrap; }
.fs-row-actions { display: flex; flex-shrink: 0; gap: var(--space-1); opacity: 0; }
.fs-row:hover .fs-row-actions, .fs-row.active .fs-row-actions { opacity: 1; }
</style>
