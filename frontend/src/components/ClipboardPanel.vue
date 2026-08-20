<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, inject, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ClipboardList, Search, X, RefreshCw, Download, Trash2, Image as ImageIcon, File as FileIcon, Star, Globe, Mail, Braces, Code, Phone, Tag, StickyNote, ChevronLeft, ChevronRight } from '@lucide/vue'
import { ListClipboardEntries, PasteClipboardEntry, GetClipboardImageBase64, HideClipboardWindow, TogglePinClipboardEntry, CreateSnippet, ExportClipboard, ClearClipboardHistory, DeleteClipboardEntry, UpdateClipboardNote } from '../../bindings/quickdock/services/appservice'
import { Events } from '@wailsio/runtime'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
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
interface SmartTag {
  id: string
  label: string
  icon: any
  match: (entry: ClipboardEntry) => boolean
}

// 共享：检测文本基础类型（标签栏筛选与片段自动分类复用同一套正则）
type DetectedType = 'email' | 'url' | 'phone' | 'json' | 'code' | null
function detectType(text: string): DetectedType {
  const trimmed = text.trim()
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) return 'email'
  if (/^https?:\/\/[^\s]+$/.test(trimmed)) return 'url'
  if (/^1[3-9]\d{9}$/.test(trimmed)) return 'phone'
  if ((trimmed.startsWith('{') || trimmed.startsWith('[')) && (trimmed.endsWith('}') || trimmed.endsWith(']'))) return 'json'
  if (trimmed.includes('\n') || /^(function|const|let|var|import|export|class|def|#include|public|private)\b/.test(trimmed)) return 'code'
  return null
}

const tags = computed<SmartTag[]>(() => [
  { id: 'all', label: t('tagAll'), icon: Tag, match: () => true },
  { id: 'favorite', label: t('tagFavorite'), icon: Star, match: (e) => e.isPinned === 1 },
  { id: 'url', label: t('tagUrl'), icon: Globe, match: (e) => detectType(e.textContent || '') === 'url' },
  { id: 'email', label: t('tagEmail'), icon: Mail, match: (e) => detectType(e.textContent || '') === 'email' },
  { id: 'json', label: t('tagJson'), icon: Braces, match: (e) => detectType(e.textContent || '') === 'json' },
  { id: 'code', label: t('tagCode'), icon: Code, match: (e) => detectType(e.textContent || '') === 'code' },
  { id: 'phone', label: t('tagPhone'), icon: Phone, match: (e) => detectType(e.textContent || '') === 'phone' },
])

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
  // 按标签筛选
  const tag = tags.value.find(t => t.id === activeTag.value)
  if (tag && tag.id !== 'all') {
    if (tag.id === 'favorite') {
      list = list.filter(e => e.isPinned === 1)
    } else {
      list = list.filter(e => e.contentType === 'text' && tag.match(e))
    }
  }
  // 按搜索词筛选
  const q = searchQuery.value.toLowerCase().trim()
  if (q) {
    list = list.filter(e => {
      if (e.contentType === 'image' && e.textContent) return e.textContent.toLowerCase().includes(q)
      if (e.contentType === 'file') return e.textContent?.toLowerCase().includes(q)
      return e.textContent?.toLowerCase().includes(q)
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
const clipboardPage = ref(1)

const displayEntries = computed(() => {
  // 紧凑模式（快捷弹窗）不分页，显示全部
  if (props.compact) return filteredEntries.value
  const start = (clipboardPage.value - 1) * CLIPBOARD_PAGE_SIZE
  return filteredEntries.value.slice(start, start + CLIPBOARD_PAGE_SIZE)
})

const totalClipboardPages = computed(() =>
  Math.max(1, Math.ceil(filteredEntries.value.length / CLIPBOARD_PAGE_SIZE))
)

watch(filteredEntries, () => { clipboardPage.value = 1 })

function selectTag(tagId: string) {
  activeTag.value = tagId
  searchQuery.value = ''
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
    const limit = props.compact ? 300 : 0
    const r = unwrap(await ListClipboardEntries(limit)) || []
    if (gen !== entriesLoadGen) return // 已有更新的刷新，丢弃过期响应
    // 不再预加载所有图片 — 由 IntersectionObserver 懒加载
    entries.value = r
     loading.value = false
  } catch (e) {
    if (gen !== entriesLoadGen) return
    const msg = getErrorMessage(e)
    console.error('QuickDock: 加载剪贴板历史失败:', msg)
    if (toast?.error) {
      toast.error(t('loadFailed') + ': ' + msg)
    }
  }
}

async function handleCopy(entry: ClipboardEntry) {
  try {
    // 紧凑模式（独立窗口）粘贴后窗口关闭，先归零滚动避免下次打开卡顿
    if (props.compact) {
      listRef.value?.scrollTo(0, 0)
    }
    await PasteClipboardEntry(entry.id)
    // 主页面模式下刷新列表，让条目因 created_at 更新而移到顶部
    if (!props.compact) {
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
    unwrap(await ClearClipboardHistory())
    await loadEntries()
    if (toast?.success) toast.success(t('clipboardCleared'))
  } catch (e) {
    if (toast?.error) toast.error(getErrorMessage(e))
  }
}

// ---- 自动分类 & 添加到片段 ----
function autoCategorize(text: string): string {
  const type = detectType(text)
  if (type) return type
  if (text.trim().length > 100) return 'template'
  return 'other'
}

const catLabels: Record<string, string> = {
  email: 'snippetCatEmail',
  url: 'snippetCatUrl',
  phone: 'snippetCatPhone',
  json: 'snippetCatJson',
  code: 'snippetCatCode',
  template: 'snippetCatTemplate',
  other: 'snippetCatOther',
}

function guessKeyword(text: string, length = 20): string {
  const line = text.trim().split('\n')[0].replace(/\s+/g, ' ')
  if (line.length <= length) return line
  return line.substring(0, length)
}

async function handleAddToSnippet(entry: ClipboardEntry) {
  if (entry.contentType !== 'text' || !entry.textContent) return
  const content = entry.textContent.trim()
  const catId = autoCategorize(content)
  const category = t(catLabels[catId] || 'snippetCatOther')
  const keyword = guessKeyword(content)
  try {
    const res = await CreateSnippet(keyword, content, category)
    if (res && res.code === 0) {
      // 后端可能返回友好提示（如重复保存），优先使用
      toast?.success?.(res.msg || t('snippetAddedMsg'))
    } else if (res) {
      toast?.error?.(getErrorMessage(res))
    }
  } catch (e) {
    if (toast?.error) {
      toast.error(getErrorMessage(e))
    }
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

  if (e.key === 'Enter' && e.target === searchInputRef.value) {
    e.preventDefault()
    e.stopPropagation()
    handleCopy(list[safeIndex(list.length)])
    return
  }

  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault()
      if (selectedIndex.value < list.length - 1) selectedIndex.value++
      else selectedIndex.value = 0
      scrollToSelected()
      break
    case 'ArrowUp':
      e.preventDefault()
      if (selectedIndex.value > 0) selectedIndex.value--
      else selectedIndex.value = list.length - 1
      scrollToSelected()
      break
    case 'Enter':
      e.preventDefault()
      e.stopPropagation()
      handleCopy(list[safeIndex(list.length)])
      break
    case 'Escape':
      resetScrollOnHide()
      try { HideClipboardWindow() } catch {}
      break
  }
}

onMounted(() => {
  loadEntries()
  refreshTimer.value = window.setInterval(loadEntries, 30000)
  Events.On('clipboard:updated', loadEntries)
  Events.On('clipboard:shown', clearSearch)
  Events.On('clipboard:before-hide', resetScrollOnHide)
  document.addEventListener('keydown', onPanelKeydown)
})

onUnmounted(() => {
  if (refreshTimer.value !== null) {
    clearInterval(refreshTimer.value)
  }
  Events.Off('clipboard:updated')
  Events.Off('clipboard:shown')
  Events.Off('clipboard:before-hide')
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
        <span v-if="entries.length" class="header-count">{{ entries.length }} {{ t('count') }}</span>
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
          v-if="entry.contentType === 'text'"
          class="snippet-btn"
          :title="t('addToSnippet')"
          @click.stop="handleAddToSnippet(entry)"
        >
          <StickyNote :size="13" />
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
          v-if="entry.isPinned === 1"
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
  </div>
</template>

<style scoped>
.clipboard-panel {
  height: 100%; display: flex; flex-direction: column;
  background: var(--color-bg-primary);
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
  background: var(--color-bg-tertiary); padding: 2px 8px; border-radius: 10px;
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
  border: 1px solid var(--color-border); border-radius: 8px;
  background: var(--color-surface); color: var(--color-text-secondary); font-size: 13px;
  outline: none; font-family: inherit;
}
.search-input:focus { border-color: var(--color-accent); }
.search-input::placeholder { color: var(--color-text-disabled); }

.clear-btn {
  position: absolute; right: 6px; top: 50%; transform: translateY(-50%);
  background: none; border: none; color: var(--color-text-muted); cursor: pointer;
  padding: 4px; border-radius: 4px; display: flex;
}
.clear-btn:hover { color: var(--color-text-secondary); background: var(--color-bg-active); }

.icon-btn {
  width: 30px; height: 30px; display: flex; align-items: center; justify-content: center;
  background: transparent; border: none; color: var(--color-text-muted); border-radius: 6px;
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
  border-radius: 12px;
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
  background: var(--color-accent-bg, rgba(74, 158, 255, 0.1));
  border-color: var(--color-accent-border, rgba(74, 158, 255, 0.2));
}

.tag-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
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
  background: var(--color-accent-bg, rgba(74, 158, 255, 0.14));
  color: var(--color-accent, #4a9eff);
  font-weight: 600;
  border-radius: 2px;
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
  padding: 10px 12px; border-radius: 8px;
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
  padding: 2px; border-radius: 4px;
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

.snippet-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: 4px;
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity 0.15s, color 0.15s;
  display: flex; align-items: center; justify-content: center;
}
.clipboard-item:hover .snippet-btn,
.clipboard-item.selected .snippet-btn { opacity: 0.4; }
.snippet-btn:hover { opacity: 0.8 !important; color: var(--color-accent); }

.delete-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: 4px;
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity 0.15s, color 0.15s;
  display: flex; align-items: center; justify-content: center;
}
.clipboard-item:hover .delete-btn,
.clipboard-item.selected .delete-btn { opacity: 0.4; }
.delete-btn:hover { opacity: 0.8 !important; color: var(--color-danger, #e5484d); }

.item-content { flex: 1; min-width: 0; }
.item-text {
  font-size: 13px; color: var(--color-text-primary); line-height: 1.5;
  word-break: break-all; margin-bottom: 4px;
}
.item-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.item-time { font-size: 11px; color: var(--color-text-disabled); }
.item-source { font-size: 11px; color: var(--color-text-muted); background: var(--color-bg-tertiary); padding: 1px 6px; border-radius: 4px; }
.item-count { font-size: 11px; color: var(--color-accent); }

.clipboard-item.is-image { align-items: flex-start; padding: 8px 12px; }
.image-thumb-wrap {
  width: 100%; max-width: 300px; border-radius: 6px; overflow: hidden;
  margin-bottom: 6px; border: 1px solid var(--color-border);
}
.image-thumb {
  display: block; width: 100%; height: auto;
  max-height: 180px; object-fit: contain;
  background: var(--color-bg-primary);
}
.image-placeholder {
  width: 160px; height: 100px; display: flex; align-items: center; justify-content: center;
  background: var(--color-surface); border-radius: 6px; margin-bottom: 6px;
  border: 1px solid var(--color-border); color: var(--color-text-disabled);
}

.file-entry {
  display: flex; align-items: flex-start; gap: 8px;
  margin-bottom: 4px;
}
.file-icon { flex-shrink: 0; margin-top: 2px; color: var(--color-accent); }
.file-paths {
  font-size: 12px; color: var(--color-text-secondary); line-height: 1.5;
  word-break: break-all; white-space: pre-wrap;
}

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
  border: 1px solid var(--color-border); border-radius: 4px;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-size: 11px; font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.clipboard-pagination .page-btn:hover:not(:disabled) { background: var(--color-bg-active); color: var(--color-text-primary); }
.clipboard-pagination .page-btn:disabled { opacity: 0.35; cursor: default; }
.clipboard-pagination .page-btn.active { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
.clipboard-pagination .page-ellipsis { color: var(--color-text-disabled); font-size: 11px; width: 18px; text-align: center; }
.clipboard-pagination .page-total { margin-left: 6px; font-size: 11px; color: var(--color-text-muted); }

/* 备注按钮 */
.note-btn {
  flex-shrink: 0; margin-top: 3px;
  background: none; border: none; cursor: pointer;
  padding: 2px; border-radius: 4px;
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
  background: rgba(0, 0, 0, 0.45);
  animation: fadeIn 0.12s ease;
}
@keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }

.note-editor {
  width: 320px; max-width: 90vw;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 10px;
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
  padding: 5px 14px; border-radius: 6px;
  font-size: 12px; font-family: inherit; font-weight: 500;
  cursor: pointer; transition: background-color 0.12s, color 0.12s, border-color 0.12s;
  border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.cancel-btn:hover { background: var(--color-bg-hover); }
.save-btn {
  background: var(--color-accent); color: #fff; border-color: var(--color-accent);
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
