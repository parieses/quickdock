<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Search, Hash, Command, ArrowRight, Lock, Power, Moon, Trash2, RotateCcw,
  Link, Clipboard, Folder, Globe, Terminal, FileText, AppWindow, CornerDownLeft, ChevronUp, ChevronDown, X
} from '@lucide/vue'
import { SearchAll, ExecuteSystemCommand, OpenItem, HidePaletteWindow, SearchSnippets, PasteSnippet, GetLastCopiedText } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { CollectionItem } from '../types'
import { pinyin } from 'pinyin-pro'
import { create, all } from 'mathjs'

const math = create(all)
const { t } = useI18n()

// ---- 类型 ----
type ResultType = 'item' | 'system' | 'quicklink' | 'quicklink-inline' | 'calculator' | 'snippet'

interface SearchResult {
  type: ResultType
  label: string
  desc?: string
  icon?: any
  item?: CollectionItem
  cmd?: SystemCmd
  calcResult?: string
  snippet?: CmdSnippet
  inlineQuery?: string
  frecencyScore?: number
}

interface SystemCmd {
  id: string
  label: string
  desc: string
  keywords: string[]
  icon: any
  action: () => Promise<void>
}

interface CmdSnippet { id: string; keyword: string; content: string; category: string; createdAt: string }

// ---- Frecency ----
interface FrecencyEntry { count: number; lastUsed: number }
const FRECENCY_KEY = 'quickdock:frecency'

function loadFrecency(): Record<string, FrecencyEntry> {
  try {
    const raw = localStorage.getItem(FRECENCY_KEY)
    return raw ? JSON.parse(raw) : {}
  } catch { return {} }
}

function saveFrecency(data: Record<string, FrecencyEntry>) {
  try { localStorage.setItem(FRECENCY_KEY, JSON.stringify(data)) } catch {}
}

function recordUsage(key: string) {
  const data = loadFrecency()
  const now = Date.now()
  data[key] = { count: (data[key]?.count || 0) + 1, lastUsed: now }
  saveFrecency(data)
}

function frecencyScore(key: string): number {
  const data = loadFrecency()
  const entry = data[key]
  if (!entry) return 0
  const now = Date.now()
  const recencyDays = (now - entry.lastUsed) / 86400000
  // 越近期使用 + 使用次数越多 → 分数越高
  return entry.count * 10 + Math.max(0, 30 - recencyDays)
}

// ---- 状态 ----
const query = ref('')
const inputRef = ref<HTMLInputElement | null>(null)
const items = ref<CollectionItem[]>([])
const loading = ref(false)
const selectedIndex = ref(0)
const listRef = ref<HTMLElement | null>(null)

// Quicklink 内联输入模式
const inlineQuicklink = ref<CollectionItem | null>(null)
const inlineQuery = ref('')
const inlineInputRef = ref<HTMLInputElement | null>(null)

// ---- 片段 ----
const snippets = ref<CmdSnippet[]>([])

// ---- 系统命令 ----
const systemCommands: SystemCmd[] = [
  { id: 'lock', label: t('cmdLock'), desc: t('cmdLockDesc'), keywords: ['lock', '锁屏', '锁定', 'suo ping', 'suo ding'], icon: Lock,
    action: async () => { await ExecuteSystemCommand('lock'); HidePaletteWindow() } },
  { id: 'shutdown', label: t('cmdShutdown'), desc: t('cmdShutdownDesc'), keywords: ['shutdown', '关机', 'guan ji', '关闭'], icon: Power,
    action: async () => { await ExecuteSystemCommand('shutdown'); HidePaletteWindow() } },
  { id: 'restart', label: t('cmdRestart'), desc: t('cmdRestartDesc'), keywords: ['restart', '重启', 'reboot', 'chong qi', '重新启动'], icon: RotateCcw,
    action: async () => { await ExecuteSystemCommand('restart'); HidePaletteWindow() } },
  { id: 'sleep', label: t('cmdsleep'), desc: t('cmdsleepDesc'), keywords: ['sleep', '休眠', '睡眠', 'shui mian', 'xiu mian'], icon: Moon,
    action: async () => { await ExecuteSystemCommand('sleep'); HidePaletteWindow() } },
  { id: 'emptytrash', label: t('cmdEmptyTrash'), desc: t('cmdEmptyTrashDesc'), keywords: ['trash', '回收站', '清空', '垃圾', 'hui shou zhan', 'qing kong'], icon: Trash2,
    action: async () => { await ExecuteSystemCommand('emptytrash'); HidePaletteWindow() } },
]

// ---- 拼音匹配 ----
function pinyinMatch(text: string, queryLC: string): boolean {
  if (!text || !queryLC) return false
  const py = pinyin(text, { pattern: 'first', toneType: 'none', type: 'array' })
  const initials = py.map(p => p[0]).join('').toLowerCase()
  if (initials.includes(queryLC)) return true
  const full = pinyin(text, { toneType: 'none', type: 'array' }).join('').toLowerCase()
  if (full.includes(queryLC)) return true
  return false
}

// ---- 项目类型图标 ----
function itemIcon(item: CollectionItem): any {
  switch (item.type) {
    case '网页': return Globe
    case '命令': return Terminal
    case '文件': return FileText
    case '应用': return AppWindow
    case '快速链接': return Link
    default: return Folder
  }
}

// ---- 搜索结果（分组） ----
interface ResultGroup {
  type: ResultType
  label: string
  results: SearchResult[]
}

const groupedResults = computed<ResultGroup[]>(() => {
  const q = query.value.trim()
  if (!q) return []

  const qLC = q.toLowerCase()
  const groups: ResultGroup[] = []
  const seen = new Set<string>()

  // 1. 计算器
  if (/^[0-9+\-*/().%^, ]+$/.test(q) || q.startsWith('=')) {
    const expr = q.startsWith('=') ? q.slice(1) : q
    try {
      const result = math.evaluate(expr)
      if (result !== undefined && result !== null) {
        groups.push({
          type: 'calculator',
          label: t('cmdGroupCalc'),
          results: [{
            type: 'calculator',
            label: `${q} = ${math.format(result, { precision: 14 })}`,
            desc: t('calcHint'),
            icon: Hash,
            calcResult: String(result),
          }]
        })
      }
    } catch {}
  }

  // 2. 项目 + Quicklink（按名称匹配）
  const itemResults: SearchResult[] = []
  for (const item of items.value) {
    const nameLC = item.name.toLowerCase()
    const valueLC = (item.value || '').toLowerCase()
    const isQuicklink = item.value && item.value.includes('{query}')
    if (nameLC.includes(qLC) || valueLC.includes(qLC) || pinyinMatch(item.name, qLC)) {
      if (!seen.has(item.id)) {
        seen.add(item.id)
        itemResults.push({
          type: isQuicklink ? 'quicklink' : 'item',
          label: item.name,
          desc: item.value || '',
          icon: itemIcon(item),
          item,
          frecencyScore: frecencyScore('item:' + item.id),
        })
      }
    }
  }
  // Frecency 排序
  itemResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
  if (itemResults.length > 0) {
    groups.push({ type: 'item', label: t('cmdGroupItems'), results: itemResults })
  }

  // 3. Quicklink 内联候选 — 输入文本作为 {query} 值
  // 仅当查询长度 > 1 且不是纯数字时展示，避免泛滥
  if (qLC.length > 1 && !/^\d+$/.test(qLC)) {
    const qlResults: SearchResult[] = []
    for (const item of items.value) {
      if (!item.value || !item.value.includes('{query}')) continue
      if (seen.has(item.id)) continue  // 已在项目分组中展示
      const resolved = item.value.replace(/\{query\}/g, qLC)
      qlResults.push({
        type: 'quicklink-inline',
        label: item.name,
        desc: resolved,
        icon: Link,
        item,
        inlineQuery: qLC,
      })
    }
    // 限制最多 3 个，避免结果爆炸
    if (qlResults.length > 0) {
      groups.push({ type: 'quicklink-inline', label: t('cmdGroupQuicklink'), results: qlResults.slice(0, 3) })
    }
  }

  // 4. 文本片段
  const snippetResults: SearchResult[] = []
  for (const s of snippets.value) {
    const kid = s.keyword.toLowerCase()
    const cid = s.content.toLowerCase()
    if (kid.includes(qLC) || cid.includes(qLC) || pinyinMatch(s.keyword, qLC)) {
      if (!seen.has('snippet-' + s.id)) {
        seen.add('snippet-' + s.id)
        snippetResults.push({
          type: 'snippet',
          label: s.keyword,
          desc: s.content.slice(0, 80),
          icon: Clipboard,
          snippet: s,
          frecencyScore: frecencyScore('snippet:' + s.id),
        })
      }
    }
  }
  snippetResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
  if (snippetResults.length > 0) {
    groups.push({ type: 'snippet', label: t('cmdGroupSnippets'), results: snippetResults })
  }

  // 5. 系统命令
  const sysResults: SearchResult[] = []
  for (const cmd of systemCommands) {
    if (cmd.keywords.some(k => k.includes(qLC)) || cmd.label.toLowerCase().includes(qLC) || pinyinMatch(cmd.label, qLC)) {
      sysResults.push({ type: 'system', label: cmd.label, desc: cmd.desc, icon: cmd.icon, cmd })
    }
  }
  if (sysResults.length > 0) {
    groups.push({ type: 'system', label: t('cmdGroupSystem'), results: sysResults })
  }

  return groups
})

// 扁平化结果列表（用于键盘导航）
const allResults = computed<SearchResult[]>(() => {
  return groupedResults.value.flatMap(g => g.results)
})

// ---- 空状态：最近使用 ----
const recentResults = computed<SearchResult[]>(() => {
  if (query.value.trim()) return []
  const scored: SearchResult[] = []
  for (const item of items.value) {
    const score = frecencyScore('item:' + item.id)
    if (score > 0) {
      scored.push({
        type: item.value?.includes('{query}') ? 'quicklink' : 'item',
        label: item.name,
        desc: item.value || '',
        icon: itemIcon(item),
        item,
        frecencyScore: score,
      })
    }
  }
  for (const s of snippets.value) {
    const score = frecencyScore('snippet:' + s.id)
    if (score > 0) {
      scored.push({
        type: 'snippet',
        label: s.keyword,
        desc: s.content.slice(0, 80),
        icon: Clipboard,
        snippet: s,
        frecencyScore: score,
      })
    }
  }
  scored.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
  return scored.slice(0, 6)
})

// 实际显示的分组（有查询时用搜索结果，无查询时用最近使用）
const displayGroups = computed<ResultGroup[]>(() => {
  if (query.value.trim()) {
    return groupedResults.value
  }
  if (recentResults.value.length > 0) {
    return [{ type: 'item', label: t('cmdRecent'), results: recentResults.value }]
  }
  return []
})

const displayFlat = computed<SearchResult[]>(() => {
  return displayGroups.value.flatMap(g => g.results)
})

// ---- 键盘导航 ----
function scrollToSelected() {
  const list = listRef.value
  if (!list) return
  const el = list.querySelector('.result-item.active') as HTMLElement | undefined
  el?.scrollIntoView({ block: 'nearest' })
}

function onKeydown(e: KeyboardEvent) {
  // 内联 Quicklink 输入模式
  if (inlineQuicklink.value) {
    if (e.key === 'Enter') {
      e.preventDefault()
      commitInlineQuicklink()
    } else if (e.key === 'Escape') {
      e.preventDefault()
      cancelInlineQuicklink()
    }
    return
  }

  const list = displayFlat.value
  if (list.length === 0 && e.key !== 'Escape') return

  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault()
      selectedIndex.value = (selectedIndex.value + 1) % Math.max(list.length, 1)
      scrollToSelected()
      break
    case 'ArrowUp':
      e.preventDefault()
      selectedIndex.value = (selectedIndex.value - 1 + list.length) % Math.max(list.length, 1)
      scrollToSelected()
      break
    case 'Enter':
      e.preventDefault()
      executeSelected()
      break
    case 'Escape':
      HidePaletteWindow()
      break
  }
}

// ---- 执行选中项 ----
async function executeSelected() {
  const result = displayFlat.value[selectedIndex.value]
  if (!result) return

  if (result.type === 'system' && result.cmd) {
    await result.cmd.action()
  } else if (result.type === 'quicklink-inline' && result.item) {
    const item = result.item
    let value = item.value || ''
    if (result.inlineQuery) {
      value = value.replace(/\{query\}/g, result.inlineQuery)
    }
    item.value = value
    recordUsage('item:' + item.id)
    await OpenItem(item as any)
    HidePaletteWindow()
  } else if (result.type === 'quicklink' && result.item) {
    // 进入内联输入模式
    inlineQuicklink.value = result.item
    inlineQuery.value = ''
    await nextTick()
    inlineInputRef.value?.focus()
  } else if (result.type === 'item' && result.item) {
    recordUsage('item:' + result.item.id)
    await OpenItem(result.item as any)
    HidePaletteWindow()
  } else if (result.type === 'calculator' && result.calcResult) {
    try { await navigator.clipboard.writeText(result.calcResult) } catch {}
    HidePaletteWindow()
  } else if (result.type === 'snippet' && result.snippet) {
    recordUsage('snippet:' + result.snippet.id)
    await PasteSnippet(result.snippet.content)
    HidePaletteWindow()
  }
}

async function commitInlineQuicklink() {
  if (!inlineQuicklink.value) return
  const item = inlineQuicklink.value
  let value = item.value || ''
  if (inlineQuery.value) {
    value = value.replace(/\{query\}/g, inlineQuery.value)
  }
  item.value = value
  recordUsage('item:' + item.id)
  await OpenItem(item as any)
  inlineQuicklink.value = null
  inlineQuery.value = ''
  HidePaletteWindow()
}

function cancelInlineQuicklink() {
  inlineQuicklink.value = null
  inlineQuery.value = ''
  nextTick(() => inputRef.value?.focus())
}

// ---- 点击选中 ----
function selectResult(groupIdx: number, itemIdx: number) {
  let flatIdx = 0
  for (let i = 0; i < groupIdx; i++) {
    flatIdx += displayGroups.value[i].results.length
  }
  selectedIndex.value = flatIdx + itemIdx
}

// 全局 flat index
function getFlatIndex(groupIdx: number, itemIdx: number): number {
  let flatIdx = 0
  for (let i = 0; i < groupIdx; i++) {
    flatIdx += displayGroups.value[i].results.length
  }
  return flatIdx + itemIdx
}

// ---- 加载数据 ----
async function loadItems() {
  loading.value = true
  try {
    const result = unwrap<CollectionItem[]>(await SearchAll(''))
    items.value = result || []
  } catch (e) {
    console.error('[CmdPalette] SearchAll:', getErrorMessage(e))
  }
  try {
    const snips = unwrap<CmdSnippet[]>(await SearchSnippets(''))
    snippets.value = snips || []
  } catch (e) {
    console.error('[CmdPalette] SearchSnippets:', getErrorMessage(e))
    try {
      const { ListSnippets } = await import('../../bindings/quickdock/services/appservice')
      const fallback = unwrap<CmdSnippet[]>(await ListSnippets())
      snippets.value = fallback || []
    } catch (e2) {
      console.error('[CmdPalette] ListSnippets fallback:', getErrorMessage(e2))
    }
  } finally {
    loading.value = false
  }
}

// ---- 窗口打开 ----
onMounted(async () => {
  loadItems()
  try {
    const copied = unwrap<string>(await GetLastCopiedText())
    if (copied && copied.trim() && copied.trim().length < 200) {
      query.value = copied.trim()
    }
  } catch {}
  setTimeout(() => {
    inputRef.value?.focus()
    inputRef.value?.select()
  }, 100)
})

// Reset selectedIndex when results change
watch(displayFlat, () => {
  selectedIndex.value = 0
})

// 切换到内联模式时清空选中
watch(inlineQuicklink, (v) => {
  if (v) selectedIndex.value = 0
})
</script>

<template>
  <div class="palette-root" @keydown="onKeydown" @click.self="HidePaletteWindow">
    <!-- 搜索栏 -->
    <div class="palette-searchbar">
      <Search :size="16" class="search-icon" />
      <input
        v-if="!inlineQuicklink"
        ref="inputRef"
        v-model="query"
        class="search-input"
        :placeholder="t('cmdPlaceholder')"
        @keydown="onKeydown"
      />
      <!-- 内联 Quicklink 输入 -->
      <template v-else>
        <span class="inline-prefix">
          <Link :size="14" />
          <span class="inline-name">{{ inlineQuicklink.name }}</span>
          <span class="inline-sep">›</span>
        </span>
        <input
          ref="inlineInputRef"
          v-model="inlineQuery"
          class="search-input inline-input"
          :placeholder="t('enterQueryParam')"
          @keydown="onKeydown"
        />
        <button class="inline-cancel" @click="cancelInlineQuicklink" :title="t('cancel')">
          <X :size="13" />
        </button>
      </template>
    </div>

    <!-- 结果列表 -->
    <div ref="listRef" class="palette-results" v-if="displayGroups.length > 0 && !inlineQuicklink">
      <template v-for="(group, gIdx) in displayGroups" :key="group.type">
        <div class="group-header">{{ group.label }}</div>
        <div
          v-for="(result, iIdx) in group.results"
          :key="group.type + '-' + (result.item?.id || result.cmd?.id || result.snippet?.id || iIdx)"
          :class="['result-item', { active: getFlatIndex(gIdx, iIdx) === selectedIndex }]"
          @click="selectResult(gIdx, iIdx); executeSelected()"
          @mousemove="selectResult(gIdx, iIdx)"
        >
          <div class="result-icon">
            <component :is="result.icon" :size="15" />
          </div>
          <div class="result-body">
            <span class="result-label">{{ result.label }}</span>
            <span class="result-desc" v-if="result.desc">{{ result.desc }}</span>
          </div>
          <div class="result-meta">
            <template v-if="result.type === 'quicklink'">
              <ArrowRight :size="12" />
            </template>
            <template v-else-if="result.type === 'quicklink-inline'">
              <span class="meta-tag">↵ {{ result.inlineQuery }}</span>
            </template>
            <template v-else-if="result.type === 'system'">
              <span class="meta-tag">cmd</span>
            </template>
            <template v-else-if="result.type === 'snippet' && result.snippet?.category">
              <span class="meta-tag">{{ result.snippet.category }}</span>
            </template>
          </div>
        </div>
      </template>
    </div>

    <!-- 空状态：无查询且无最近使用 -->
    <div v-if="displayGroups.length === 0 && !inlineQuicklink" class="palette-empty">
      <Search :size="28" class="empty-icon" />
      <p class="empty-title">{{ t('cmdEmptyTitle') }}</p>
      <p class="empty-desc">{{ t('cmdEmptyDesc') }}</p>
    </div>

    <!-- 底部快捷键提示 -->
    <div class="palette-footer" v-if="!inlineQuicklink">
      <div class="footer-hint">
        <kbd><ChevronUp :size="11" /></kbd>
        <kbd><ChevronDown :size="11" /></kbd>
        <span>{{ t('cmdNavigate') }}</span>
      </div>
      <div class="footer-hint">
        <kbd><CornerDownLeft :size="11" /></kbd>
        <span>{{ t('cmdExecute') }}</span>
      </div>
      <div class="footer-hint">
        <kbd>Esc</kbd>
        <span>{{ t('close') }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.palette-root {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
  overflow: hidden;
}

/* 搜索栏 */
.palette-searchbar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 18px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.search-icon {
  color: var(--color-text-muted);
  flex-shrink: 0;
}

.search-input {
  flex: 1;
  background: none;
  border: none;
  outline: none;
  color: var(--color-text-primary);
  font-size: 15px;
  font-family: inherit;
  font-weight: 450;
  letter-spacing: 0.01em;
  min-width: 0;
}
.search-input::placeholder {
  color: var(--color-text-muted);
}

/* 内联 Quicklink */
.inline-prefix {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--color-accent);
  font-size: 13px;
  font-weight: 500;
  flex-shrink: 0;
}
.inline-name {
  color: var(--color-accent);
}
.inline-sep {
  color: var(--color-text-muted);
}
.inline-input {
  font-size: 14px;
}
.inline-cancel {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 5px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  cursor: pointer;
  flex-shrink: 0;
  transition: background 0.12s, color 0.12s;
}
.inline-cancel:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

/* 结果列表 */
.palette-results {
  flex: 1;
  overflow-y: auto;
  padding: 4px 6px 8px;
}

/* 分组标题 */
.group-header {
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  padding: 10px 12px 4px;
  user-select: none;
}

/* 结果项 */
.result-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.08s;
}

.result-item.active {
  background: var(--color-bg-active);
}

.result-icon {
  width: 28px;
  height: 28px;
  border-radius: 6px;
  background: var(--color-bg-tertiary);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--color-text-muted);
  flex-shrink: 0;
  transition: color 0.12s, background 0.12s;
}
.result-item.active .result-icon {
  color: var(--color-accent);
  background: var(--color-accent-bg);
}

.result-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.result-label {
  font-size: 13.5px;
  font-weight: 500;
  color: var(--color-text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  letter-spacing: 0.01em;
}

.result-desc {
  font-size: 11.5px;
  color: var(--color-text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  letter-spacing: 0.01em;
}

.result-meta {
  color: var(--color-text-muted);
  flex-shrink: 0;
  display: flex;
  align-items: center;
}
.result-item.active .result-meta {
  color: var(--color-text-secondary);
}

.meta-tag {
  font-size: 10px;
  background: var(--color-bg-tertiary);
  padding: 2px 6px;
  border-radius: 4px;
  color: var(--color-text-muted);
  letter-spacing: 0.03em;
}
.result-item.active .meta-tag {
  background: var(--color-bg-hover);
  color: var(--color-text-secondary);
}

/* 空状态 */
.palette-empty {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 32px;
}
.empty-icon {
  color: var(--color-text-disabled);
  margin-bottom: 4px;
}
.empty-title {
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-secondary);
  margin: 0;
}
.empty-desc {
  font-size: 12px;
  color: var(--color-text-muted);
  margin: 0;
  text-align: center;
  max-width: 360px;
  line-height: 1.5;
}

/* 底部快捷键提示 */
.palette-footer {
  display: flex;
  align-items: center;
  gap: 20px;
  padding: 8px 18px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}

.footer-hint {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--color-text-muted);
}

.footer-hint kbd {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 18px;
  padding: 0 4px;
  border-radius: 4px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  font-size: 10px;
  font-family: var(--font-mono, monospace);
  font-weight: 500;
}

/* 滚动条 */
.palette-results::-webkit-scrollbar {
  width: 5px;
}
.palette-results::-webkit-scrollbar-track {
  background: transparent;
}
.palette-results::-webkit-scrollbar-thumb {
  background: var(--color-scrollbar-thumb);
  border-radius: 3px;
}
.palette-results::-webkit-scrollbar-thumb:hover {
  background: var(--color-scrollbar-hover);
}
</style>
