<script setup lang="ts">
// 文件预览内容体（命令面板 QuickLook 预览用）。
//
// 只负责「把已探明的产物画出来」，不负责标题行与容器高度 —— 那两样由所在的
// 浮窗决定，将来独立预览窗复用本组件时不必再改。
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search, ArrowLeft, Maximize2, Minimize2, ChevronRight } from '@lucide/vue'
import hljs from 'highlight.js/lib/common'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { PreviewPath } from '../../bindings/quickdock/services/item/itemservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { installMathOnce } from '../utils/mathMarkdown'
import '../styles/hljs-theme.css'

// searching 由父组件持有：Esc 是文档级 capture 监听，子组件收不到（也拦不住），
// 所以「先退出预览内搜索、再关面板」这个收起顺序只能由父组件的 Esc 分支判断。
//
// expanded 同理上提：放大要隐藏结果列表，而列表是父组件的 DOM，高度 class 也挂在父组件。
const props = defineProps<{ path: string; searching: boolean; expanded: boolean }>()
const emit = defineEmits<{
  (e: 'update:searching', v: boolean): void
  (e: 'update:expanded', v: boolean): void
}>()

const { t } = useI18n()
const toast = useToast()

// 与 Go 侧 fsview.Result 字段一一对应。改结构时两边一起改。
interface PreviewEntry {
  name: string
  size: number
  isDir: boolean
}
interface PreviewResult {
  path: string
  name: string
  ext: string
  kind: 'missing' | 'dir' | 'image' | 'video' | 'audio' | 'pdf' | 'markdown' | 'code' | 'text' | 'archive' | 'binary'
  size: number
  sizeText: string
  modified: string
  text: string
  lineCount: number
  truncated: boolean
  encoding: string
  language: string
  imageWidth: number
  imageHeight: number
  mediaUrl: string
  entries: PreviewEntry[]
  entryTotal: number
  note: string
}

const res = ref<PreviewResult | null>(null)
const loading = ref(false)
const failed = ref(false)
// 媒体加载失败（图片解码不了、视频编码不支持、PDF 损坏）。
// 与 failed 区分：failed 是 IPC 本身出错（值得 toast），这个是「文件就在那儿但读不出来」，
// 用户只是从列表路过，弹 toast 会刷屏，就地显示一句更合适。
const mediaFailed = ref(false)

// 当前浏览的路径。目录预览可以点进子目录，此时 viewPath 会偏离 props.path；
// 一旦用户在结果列表里换了选中项，props.path 变化就把它拉回来。
const viewPath = ref(props.path)
// 目录下钻的历史栈，用于逐级返回（只往前返回，不做前进）。
const pathStack = ref<string[]>([])

// 防抖：面板里方向键连着划过十几个文件时，每个都发一次 IPC 是纯浪费。
// 间隔与父组件的 quickInfo 保持一致，免得两路请求错位闪烁。
const LOAD_DEBOUNCE_MS = 180
let timer: ReturnType<typeof setTimeout> | null = null
// 请求序号：慢请求回来时用户可能已经切到别的文件，用序号丢弃过期结果。
let seq = 0

async function load(p: string) {
  if (!p) {
    res.value = null
    failed.value = false
    return
  }
  const my = ++seq
  loading.value = true
  failed.value = false
  mediaFailed.value = false
  try {
    const data = unwrap<PreviewResult | null>(await PreviewPath(p))
    if (my !== seq) return
    res.value = data || null
  } catch (e) {
    if (my !== seq) return
    failed.value = true
    res.value = null
    toast.error(getErrorMessage(e))
    logErr('fsview/preview', e)
  } finally {
    if (my === seq) loading.value = false
  }
}

// 选中项变了：回到该路径本身，清掉下钻栈与预览内搜索。
// 首次挂载（old 为 undefined）不 emit，此时父组件的状态本来就是初始值。
watch(
  () => props.path,
  (p, old) => {
    pathStack.value = []
    viewPath.value = p
    // 立刻清掉上一个文件的加载失败标记：实际加载要等防抖，不清的话
    // 这 180ms 里新文件会顶着上一张的「无法加载」提示。
    mediaFailed.value = false
    if (old !== undefined) emit('update:searching', false)
  },
  { immediate: true },
)

// 真正加载看 viewPath：目录下钻也走同一条防抖链路。
watch(
  viewPath,
  (p) => {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
    if (!p) {
      res.value = null
      loading.value = false
      failed.value = false
      return
    }
    timer = setTimeout(() => {
      timer = null
      void load(p)
    }, LOAD_DEBOUNCE_MS)
  },
  { immediate: true },
)

// ---- 文本 / 代码 ----
// 同步高亮大文件会卡住首帧（与 AIPage 的只读视图同一个坑），超过阈值直接退纯文本。
const HLJS_MAX_BYTES = 64 * 1024
const HLJS_MAX_LINES = 800

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) =>
    c === '&' ? '&amp;' : c === '<' ? '&lt;' : c === '>' ? '&gt;' : c === '"' ? '&quot;' : '&#39;',
  )
}

const highlighted = computed(() => {
  const r = res.value
  if (!r || (r.kind !== 'code' && r.kind !== 'text')) return ''
  const text = r.text || ''
  if (!text) return ''
  const lang = (r.language || '').toLowerCase()
  const tooBig = text.length > HLJS_MAX_BYTES || r.lineCount > HLJS_MAX_LINES
  if (tooBig || !lang || !hljs.getLanguage(lang)) return escapeHtml(text)
  return hljs.highlight(text, { language: lang }).value
})

// ---- Markdown ----
const md = new Marked()
installMathOnce(md)

const markdownHtml = computed(() => {
  const r = res.value
  if (!r || r.kind !== 'markdown') return ''
  const html = md.parse(r.text || '') as string
  return DOMPurify.sanitize(html)
})

// ---- 图片缩放 ----
// 预览窗很窄，长截图缩到看不清，所以要能放大。缩放比例是本地的运行时值，
// 只能内联到 transform（项目规范里唯一允许的内联样式场景）。
const zoom = ref(1)
const ZOOM_MIN = 0.2
const ZOOM_MAX = 5

function onWheel(e: WheelEvent) {
  // 没按 Ctrl 时把滚动让给外层容器，避免抢走列表滚动
  if (!e.ctrlKey) return
  e.preventDefault()
  const next = zoom.value * (e.deltaY < 0 ? 1.12 : 1 / 1.12)
  zoom.value = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, next))
}

function resetZoom() {
  zoom.value = 1
}

watch(
  () => props.path,
  () => {
    zoom.value = 1
  },
)

const imageStyle = computed(() => ({ transform: `scale(${zoom.value})` }))
// 模板里刻意不写 Math.round(...)：Vue 模板的全局白名单虽然放行 Math，
// 但这类表达式 vue-tsc 不校验，拼错要等运行时才暴露。
const zoomText = computed(() => `${Math.round(zoom.value * 100)}%`)

// 目录项 / 压缩包条目的体积展示。后端只给字节数，这里按 1024 进位格式化。
function formatBytes(n: number): string {
  if (!n || n < 0) return ''
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

// ---- 目录下钻 ----
// 保留原路径的分隔符风格：Windows 路径混进正斜杠虽然能 stat，但显示出来很难看。
function joinPath(base: string, name: string): string {
  const sep = base.includes('\\') ? '\\' : '/'
  return base.endsWith(sep) ? base + name : base + sep + name
}

function enterDir(e: PreviewEntry) {
  if (!e.isDir || !res.value) return
  pathStack.value.push(res.value.path)
  viewPath.value = joinPath(res.value.path, e.name)
  closeSearch()
}

function goBack() {
  const prev = pathStack.value.pop()
  if (prev) viewPath.value = prev
}

const isNavigated = computed(() => pathStack.value.length > 0)

// ---- 预览内搜索 ----
const searchQuery = ref('')
const searchInputRef = ref<HTMLInputElement | null>(null)

// 只对能按行切分的纯文本生效：Markdown 走的是渲染后的 HTML，按行号定位没有意义。
const canSearch = computed(() => {
  const r = res.value
  return !!r && (r.kind === 'code' || r.kind === 'text') && (r.text || '').length > 0
})

// 命中上限。面板里滚 500 行已经到极限，再多只是拖慢渲染、也没人会真看完。
const SEARCH_HIT_MAX = 500

// 命中按「行号 + 原文」返回，不做区间高亮：正文已经是 highlight.js 的
// 一堆 span，在 HTML 上插入 mark 会切开标签，得不偿失。过滤成命中行反而更好扫。
const searchHits = computed<{ n: number; text: string }[] | null>(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!props.searching || !q || !res.value) return null
  const lines = (res.value.text || '').split('\n')
  const out: { n: number; text: string }[] = []
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].toLowerCase().includes(q)) {
      out.push({ n: i + 1, text: lines[i] })
      if (out.length >= SEARCH_HIT_MAX) break
    }
  }
  return out
})

const hitsText = computed(() => (searchHits.value || []).map((h) => `${h.n}: ${h.text}`).join('\n'))

function openSearch() {
  if (!canSearch.value) return
  emit('update:searching', true)
  // emit 只更新父组件的 ref，输入框要等重新渲染后才存在，必须等一拍再聚焦。
  nextTick(() => searchInputRef.value?.focus())
}

function closeSearch() {
  searchQuery.value = ''
  emit('update:searching', false)
}

// 同一个按钮负责开关：搜索框开着的时候再点一次就该收起来，
// 否则用户只能靠 Esc —— 而 Esc 在很多人心里是「关整个面板」。
function toggleSearch() {
  if (props.searching) closeSearch()
  else openSearch()
}

// 放大：把面板里 262px 的预览区顶到整块剩余高度，并让父组件藏起结果列表。
// 这是「独立预览浮窗」的廉价替代 —— 不值得为了看清一张 PDF 再开一个窗口。
const canExpand = computed(() => {
  const r = res.value
  return !!r && r.kind !== 'missing'
})

function toggleExpand() {
  emit('update:expanded', !props.expanded)
}

// 父组件把 searching 置回 false（Esc 收起 / 换了选中项）时清空关键词，
// 免得下次点开搜索框还带着上一份文件里的词。
watch(
  () => props.searching,
  (v) => {
    if (!v) searchQuery.value = ''
  },
)

// ---- 展示用派生 ----
const metaLine = computed(() => {
  const r = res.value
  if (!r) return ''
  const parts: string[] = []
  // 目录与压缩包的「大小」是本目录项字节数，容易误读，干脆不显示
  if (r.kind !== 'dir' && r.sizeText) parts.push(r.sizeText)
  if (r.imageWidth > 0 && r.imageHeight > 0) parts.push(`${r.imageWidth}×${r.imageHeight}`)
  if (r.lineCount > 0) parts.push(`${r.lineCount} ${t('previewLineUnit')}`)
  if (r.kind === 'archive' && r.entryTotal > 0) parts.push(`${r.entryTotal} ${t('previewEntryUnit')}`)
  if (r.kind === 'dir' && r.entryTotal > 0) parts.push(`${r.entryTotal} ${t('previewEntryUnit')}`)
  return parts.join(' · ')
})

const showTruncated = computed(() => !!res.value?.truncated && (res.value.kind === 'code' || res.value.kind === 'text' || res.value.kind === 'markdown'))
const archiveUnsupported = computed(() => res.value?.note === 'archive_unsupported')
</script>

<template>
  <div class="fp-root" :class="{ 'is-loading': loading }" @wheel="onWheel">
    <div v-if="loading && !res" class="fp-hint">{{ t('previewLoading') }}</div>

    <div v-else-if="failed" class="fp-hint">{{ t('previewFailed') }}</div>

    <div v-else-if="!res" class="fp-hint">{{ t('previewLoading') }}</div>

    <div v-else-if="res.kind === 'missing'" class="fp-hint">{{ t('previewPathMissing') }}</div>

    <template v-else>
      <div class="fp-meta">
        <button v-if="isNavigated" class="fp-tool" @click="goBack">
          <ArrowLeft :size="11" />
          {{ t('previewBack') }}
        </button>
        <span v-if="isNavigated" class="fp-crumb">{{ res.path }}</span>
        <span v-if="metaLine">{{ metaLine }}</span>
        <span v-if="res.modified" class="fp-meta-sep">{{ res.modified }}</span>
        <span v-if="res.encoding && res.kind !== 'binary'" class="fp-meta-sep">{{ res.encoding }}</span>
        <span v-if="zoom !== 1" class="fp-meta-sep fp-zoom">{{ zoomText }}</span>
        <div class="fp-tools">
          <button
            v-if="canSearch"
            class="fp-tool"
            :class="{ 'is-on': searching }"
            :title="t('previewSearch')"
            @click="toggleSearch"
          >
            <Search :size="11" />
          </button>
          <button
            v-if="canExpand"
            class="fp-tool"
            :class="{ 'is-on': expanded }"
            :title="expanded ? t('previewCollapse') : t('previewExpand')"
            @click="toggleExpand"
          >
            <Minimize2 v-if="expanded" :size="11" />
            <Maximize2 v-else :size="11" />
          </button>
        </div>
      </div>

      <div v-if="searching" class="fp-searchrow">
        <input
          ref="searchInputRef"
          v-model="searchQuery"
          class="fp-search-input"
          :placeholder="t('previewSearchPlaceholder')"
          @keydown.enter.stop
        />
        <span class="fp-search-count">
          {{ searchQuery.trim() ? (searchHits ? searchHits.length : 0) : '' }}
        </span>
      </div>

      <!-- 图片 -->
      <div v-if="res.kind === 'image'" class="fp-image-wrap">
        <img
          v-if="res.mediaUrl && !mediaFailed"
          :src="res.mediaUrl"
          :alt="res.name"
          :style="imageStyle"
          class="fp-image"
          @error="mediaFailed = true"
          @dblclick="resetZoom"
        />
        <div v-else class="fp-hint">{{ mediaFailed ? t('previewMediaFailed') : t('previewMediaUnavailable') }}</div>
      </div>

      <!-- 视频 -->
      <div v-else-if="res.kind === 'video'" class="fp-media-wrap">
        <video
          v-if="res.mediaUrl && !mediaFailed"
          :src="res.mediaUrl"
          class="fp-video"
          controls
          preload="metadata"
          @error="mediaFailed = true"
        />
        <div v-else class="fp-hint">{{ mediaFailed ? t('previewMediaFailed') : t('previewMediaUnavailable') }}</div>
      </div>

      <!-- 音频：宿主给所有 WebView2 传了 --mute-audio（见 main.go 的 memoryOptimizedArgs），
           播放器能走进度但没有声音。不解释一句的话，用户只会觉得「点了没反应」。 -->
      <div v-else-if="res.kind === 'audio'" class="fp-media-wrap fp-audio-wrap">
        <audio
          v-if="res.mediaUrl && !mediaFailed"
          :src="res.mediaUrl"
          class="fp-audio"
          controls
          preload="metadata"
          @error="mediaFailed = true"
        />
        <div v-else class="fp-hint">{{ mediaFailed ? t('previewMediaFailed') : t('previewMediaUnavailable') }}</div>
        <div class="fp-note">{{ t('previewAudioMuted') }}</div>
      </div>

      <!-- PDF -->
      <div v-else-if="res.kind === 'pdf'" class="fp-media-wrap">
        <iframe v-if="res.mediaUrl" :src="res.mediaUrl" class="fp-pdf" :title="res.name" />
        <div v-else class="fp-hint">{{ t('previewMediaUnavailable') }}</div>
      </div>

      <!-- Markdown -->
      <div v-else-if="res.kind === 'markdown'" class="fp-scroll">
        <div class="fp-markdown" v-html="markdownHtml" />
        <div v-if="showTruncated" class="fp-note">{{ t('previewTruncated') }}</div>
      </div>

      <!-- 代码 / 文本 -->
      <div v-else-if="res.kind === 'code' || res.kind === 'text'" class="fp-scroll">
        <template v-if="searching && searchQuery.trim()">
          <div v-if="!searchHits || searchHits.length === 0" class="fp-hint">{{ t('previewSearchNoMatch') }}</div>
          <pre v-else class="fp-code"><code>{{ hitsText }}</code></pre>
          <!-- 搜索只在已读取的那一段里做。不说明的话，用户会以为「文件里没有这个词」。 -->
          <div v-if="showTruncated" class="fp-note">{{ t('previewSearchTruncated') }}</div>
        </template>
        <template v-else>
          <pre class="fp-code"><code v-html="highlighted" /></pre>
          <div v-if="showTruncated" class="fp-note">{{ t('previewTruncated') }}</div>
        </template>
      </div>

      <!-- 目录：目录项可点进下一层，文件项不接（打开由面板的回车负责，别在这里
           再造一个入口，两处行为不一致反而更难预期） -->
      <div v-else-if="res.kind === 'dir'" class="fp-scroll">
        <!-- 权限不足 / 目录已被删 与「真的空」要分开说：
             后者用户不用管，前者用户得知道是打不开而不是里面没东西。 -->
        <div v-if="res.note === 'read_dir_failed'" class="fp-hint">{{ t('previewFailed') }}</div>
        <div v-else-if="res.entries.length === 0" class="fp-hint">{{ t('previewDirEmpty') }}</div>
        <div
          v-for="(e, i) in res.entries"
          :key="i"
          class="fp-entry"
          :class="{ 'is-clickable': e.isDir }"
          @click="enterDir(e)"
        >
          <span class="fp-entry-name">{{ e.name }}</span>
          <!-- 文件夹末尾加一个箭头：光靠 hover 变亮没人知道能点进去。
               文件不加，打开文件归回车管，多一个箭头只会让人以为点了会开。 -->
          <ChevronRight v-if="e.isDir" :size="11" class="fp-entry-go" />
          <span v-else-if="e.size > 0" class="fp-entry-size">{{ formatBytes(e.size) }}</span>
        </div>
        <div v-if="res.entryTotal > res.entries.length" class="fp-note">
          {{ t('previewMoreEntries', { n: res.entryTotal - res.entries.length }) }}
        </div>
      </div>

      <!-- 压缩包 -->
      <div v-else-if="res.kind === 'archive'" class="fp-scroll">
        <div v-if="archiveUnsupported" class="fp-hint">{{ t('previewArchiveUnsupported') }}</div>
        <div v-else-if="res.entries.length === 0" class="fp-hint">{{ t('previewFailed') }}</div>
        <div v-for="(e, i) in res.entries" :key="i" class="fp-entry">
          <span class="fp-entry-name" :class="{ 'is-dir': e.isDir }">{{ e.name }}</span>
          <span v-if="!e.isDir" class="fp-entry-size">{{ formatBytes(e.size) }}</span>
        </div>
        <div v-if="res.entryTotal > res.entries.length" class="fp-note">
          {{ t('previewMoreEntries', { n: res.entryTotal - res.entries.length }) }}
        </div>
      </div>

      <!-- 二进制 / 未知格式 -->
      <div v-else class="fp-hint">{{ t('previewBinaryHint') }}</div>
    </template>
  </div>
</template>

<style scoped>
.fp-root {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  height: 100%;
  min-height: 0;
  overflow: hidden;
  transition: opacity var(--transition-fast);
}

/* 换文件时不清空内容，只做淡出：清空会让方向键快划时一直白闪，
   保留旧内容又会让「标题已换、正文未换」显得像卡住，淡出是两者之间的提示。 */
.fp-root.is-loading {
  opacity: 0.55;
}

.fp-hint {
  padding: var(--space-4) 0;
  font-size: 12px;
  color: var(--color-text-muted);
}

.fp-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-4);
  font-size: 11px;
  color: var(--color-text-muted);
  flex-shrink: 0;
}

.fp-meta-sep {
  position: relative;
}

/* 元数据行里的小工具按钮（返回 / 搜索）：跟着文字走，不做成明显的按钮块，
   否则在一个只有 262px 高的预览条里会比内容还抢眼。 */
.fp-tool {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  padding: 0 var(--space-2);
  border: none;
  background: transparent;
  color: var(--color-text-muted);
  font-size: 11px;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.fp-tool:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.fp-tool.is-on {
  background: var(--color-bg-active);
  color: var(--color-border-focus);
}

/* 工具按钮组推到右端：放在同一个容器里统一 margin-left:auto，
   否则每个按钮各自 auto 会把元数据行撑出等分空白。 */
.fp-tools {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
}

/* 下钻后的当前路径：截断显示，完整路径留给 hover 的原生提示不划算，
   用户点返回就回去了。 */
.fp-crumb {
  max-width: 45%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-secondary);
}

.fp-searchrow {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
}

.fp-search-input {
  flex: 1;
  min-width: 0;
  padding: var(--space-1) var(--space-3);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-xs);
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  font-size: 11.5px;
}

.fp-search-input:focus {
  outline: none;
  border-color: var(--color-border-focus);
}

.fp-search-count {
  min-width: 26px;
  text-align: right;
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.fp-zoom {
  color: var(--color-border-focus);
}

.fp-image-wrap {
  flex: 1;
  min-height: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  background: var(--color-bg-primary);
  border-radius: var(--radius-sm);
}

.fp-image {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
  transition: transform var(--transition-fast);
}

.fp-media-wrap {
  flex: 1;
  min-height: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  background: var(--color-bg-primary);
  border-radius: var(--radius-sm);
}

.fp-video {
  max-width: 100%;
  max-height: 100%;
}

.fp-audio-wrap {
  flex: 0 0 auto;
  padding: var(--space-4);
}

.fp-audio {
  width: 100%;
}

.fp-pdf {
  width: 100%;
  height: 100%;
  border: none;
  background: var(--color-bg-primary);
}

.fp-scroll {
  flex: 1;
  min-height: 0;
  overflow: auto;
  border-radius: var(--radius-sm);
}

.fp-code {
  margin: 0;
  font-family: var(--font-mono, monospace);
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--color-text-secondary);
  white-space: pre;
  tab-size: 4;
}

.fp-markdown {
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-secondary);
  word-break: break-word;
}

.fp-markdown :deep(h1),
.fp-markdown :deep(h2),
.fp-markdown :deep(h3) {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
  margin: var(--space-4) 0 var(--space-2);
}

.fp-markdown :deep(p) {
  margin: var(--space-2) 0;
}

.fp-markdown :deep(pre) {
  background: var(--color-bg-primary);
  padding: var(--space-4);
  border-radius: var(--radius-sm);
  overflow-x: auto;
}

.fp-markdown :deep(code) {
  font-family: var(--font-mono, monospace);
  font-size: 11.5px;
}

.fp-markdown :deep(a) {
  color: var(--color-border-focus);
}

.fp-entry {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-1) 0;
  font-size: 11.5px;
  color: var(--color-text-secondary);
}

.fp-entry.is-clickable {
  cursor: pointer;
  border-radius: var(--radius-xs);
}

.fp-entry.is-clickable:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.fp-entry-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono, monospace);
}

.fp-entry-name.is-dir {
  color: var(--color-text-primary);
}

.fp-entry-size {
  flex-shrink: 0;
  font-size: 10.5px;
  color: var(--color-text-muted);
}

/* 目录末端的进入箭头：颜色跟条目一致、只有 hover 时才明显，
   免得一堆箭头把目录列表弄得像个菜单。 */
.fp-entry-go {
  flex-shrink: 0;
  color: var(--color-text-muted);
  opacity: 0.55;
  transition: opacity var(--transition-fast);
}
.fp-entry.is-clickable:hover .fp-entry-go {
  opacity: 1;
  color: var(--color-border-focus);
}

.fp-note {
  margin-top: var(--space-2);
  font-size: 11px;
  color: var(--color-text-muted);
}
</style>
