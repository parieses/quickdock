<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search, Hash, Command, ArrowRight, Lock, Power, Moon, Trash2, RotateCcw, Monitor, Clipboard } from '@lucide/vue'
import { SearchAll, ExecuteSystemCommand, OpenItem, HidePaletteWindow, SearchSnippets, PasteSnippet } from '../../bindings/quickdock/services/appservice'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { CollectionItem } from '../types'
import { pinyin } from 'pinyin-pro'
import { create, all } from 'mathjs'

const math = create(all)

const { t } = useI18n()

// ---- 状态 ----
const query = ref('')
const inputRef = ref<HTMLInputElement | null>(null)
const items = ref<CollectionItem[]>([])
const loading = ref(false)
const selectedIndex = ref(0)
const showQueryInput = ref(false)
const queryPlaceholder = ref('')
const queryValue = ref('')
const pendingQuicklink = ref<CollectionItem | null>(null)

// ---- 片段 ----
interface CmdSnippet { id: string; keyword: string; content: string; category: string; createdAt: string }
const snippets = ref<CmdSnippet[]>([])

// ---- 系统命令定义 ----
interface SystemCmd {
  id: string
  label: string
  desc: string
  keywords: string[]
  icon: any
  action: () => Promise<void>
}

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
  // 拼音首字母匹配
  const py = pinyin(text, { pattern: 'first', toneType: 'none', type: 'array' })
  const initials = py.map(p => p[0]).join('').toLowerCase()
  if (initials.includes(queryLC)) return true
  // 全拼匹配
  const full = pinyin(text, { toneType: 'none', type: 'array' }).join('').toLowerCase()
  if (full.includes(queryLC)) return true
  return false
}

// ---- 搜索结果 ----
interface SearchResult {
  type: 'item' | 'system' | 'quicklink' | 'calculator' | 'snippet'
  label: string
  desc?: string
  icon?: any
  item?: CollectionItem
  cmd?: SystemCmd
  calcResult?: string
  snippet?: CmdSnippet
}

const allResults = computed(() => {
  const q = query.value.trim()
  if (!q) return []

  const qLC = q.toLowerCase()
  const results: SearchResult[] = []
  const seen = new Set<string>()

  // 1. 计算器 — 检测数学表达式
  if (/^[0-9+\-*/().%^, ]+$/.test(q) || q.startsWith('=')) {
    const expr = q.startsWith('=') ? q.slice(1) : q
    try {
      const result = math.evaluate(expr)
      if (result !== undefined && result !== null) {
        results.push({
          type: 'calculator',
          label: `${q} = ${math.format(result, { precision: 14 })}`,
          desc: t('calcHint'),
          calcResult: String(result)
        })
      }
    } catch {}
  }

  // 2. 系统命令 — 按关键词匹配
  for (const cmd of systemCommands) {
    if (
      cmd.keywords.some(k => k.includes(qLC)) ||
      cmd.label.toLowerCase().includes(qLC) ||
      pinyinMatch(cmd.label, qLC)
    ) {
      if (!seen.has(cmd.id)) {
        seen.add(cmd.id)
        results.push({ type: 'system', label: cmd.label, desc: cmd.desc, icon: cmd.icon, cmd })
      }
    }
  }

  // 3. 项目 — 按名称搜索
  for (const item of items.value) {
    const nameLC = item.name.toLowerCase()
    const valueLC = (item.value || '').toLowerCase()
    if (nameLC.includes(qLC) || valueLC.includes(qLC) || pinyinMatch(item.name, qLC)) {
      if (!seen.has(item.id)) {
        seen.add(item.id)
        const isQuicklink = item.value && item.value.includes('{query}')
        results.push({
          type: isQuicklink ? 'quicklink' : 'item',
          label: item.name,
          desc: item.value || '',
          item
        })
      }
    }
  }

  // 4. 文本片段 — 按关键词和内容搜索
  for (const s of snippets.value) {
    const kid = s.keyword.toLowerCase()
    const cid = s.content.toLowerCase()
    if (kid.includes(qLC) || cid.includes(qLC) || pinyinMatch(s.keyword, qLC)) {
      if (!seen.has('snippet-' + s.id)) {
        seen.add('snippet-' + s.id)
        results.push({
          type: 'snippet',
          label: s.keyword,
          desc: s.content.slice(0, 60),
          snippet: s
        })
      }
    }
  }

  // 按优先级排序：计算器 > 片段 > 项目/Quicklink > 系统命令
  results.sort((a, b) => {
    const order: Record<string, number> = { calculator: 0, snippet: 1, item: 2, quicklink: 2, system: 3 }
    return (order[a.type] || 9) - (order[b.type] || 9)
  })

  return results
})

// ---- 键盘导航 ----
function scrollToSelected() {
  const list = listRef.value
  if (!list) return
  const el = list.querySelector('.pr-item.active') as HTMLElement | undefined
  el?.scrollIntoView({ block: 'nearest' })
}

const listRef = ref<HTMLElement | null>(null)

function onKeydown(e: KeyboardEvent) {
  // 如果正在输入 query 值，不处理导航
  if (showQueryInput.value) {
    if (e.key === 'Enter') {
      e.preventDefault()
      commitQuicklink()
    } else if (e.key === 'Escape') {
      cancelQuicklink()
    }
    return
  }

  const list = allResults.value
  if (list.length === 0) return

  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault()
      selectedIndex.value = (selectedIndex.value + 1) % list.length
      scrollToSelected()
      break
    case 'ArrowUp':
      e.preventDefault()
      selectedIndex.value = (selectedIndex.value - 1 + list.length) % list.length
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
  const result = allResults.value[selectedIndex.value]
  if (!result) return

  if (result.type === 'system' && result.cmd) {
    await result.cmd.action()
  } else if (result.type === 'quicklink' && result.item) {
    // 需要用户输入 query 参数
    pendingQuicklink.value = result.item
    queryPlaceholder.value = t('enterQueryParam')
    queryValue.value = ''
    showQueryInput.value = true
  } else if (result.type === 'item' && result.item) {
    await OpenItem(result.item as any)
    HidePaletteWindow()
  } else if (result.type === 'calculator' && result.calcResult) {
    // 复制计算结果到剪贴板
    try {
      await navigator.clipboard.writeText(result.calcResult)
    } catch {}
    HidePaletteWindow()
  } else if (result.type === 'snippet' && result.snippet) {
    await PasteSnippet(result.snippet.content)
    HidePaletteWindow()
  }
}

async function commitQuicklink() {
  if (!pendingQuicklink.value) return
  const item = pendingQuicklink.value
  let value = item.value || ''
  if (queryValue.value) {
    value = value.replace(/\{query\}/g, queryValue.value)
  }
  item.value = value
  await OpenItem(item as any)
  showQueryInput.value = false
  pendingQuicklink.value = null
  HidePaletteWindow()
}

function cancelQuicklink() {
  showQueryInput.value = false
  pendingQuicklink.value = null
}

// ---- 点击选中 ----
function selectResult(idx: number) {
  selectedIndex.value = idx
}

// ---- 加载数据 ----
async function loadItems() {
  loading.value = true
  try {
    const result = unwrap<CollectionItem[]>(await SearchAll(''))
    items.value = result || []
  } catch (e) {
    console.error('[CmdPalette] SearchAll 失败:', getErrorMessage(e))
  }
  try {
    const snips = unwrap<CmdSnippet[]>(await SearchSnippets(''))
    snippets.value = snips || []
    console.log('[CmdPalette] 加载片段:', snippets.value.length, '条')
  } catch (e) {
    console.error('[CmdPalette] SearchSnippets 失败:', getErrorMessage(e))
    // 回退：尝试用 ListSnippets（修复 validTables 后可用）
    try {
      const { ListSnippets } = await import('../../bindings/quickdock/services/appservice')
      const fallback = unwrap<CmdSnippet[]>(await ListSnippets())
      snippets.value = fallback || []
      console.log('[CmdPalette] 回退 ListSnippets 加载片段:', snippets.value.length, '条')
    } catch (e2) {
      console.error('[CmdPalette] ListSnippets 回退也失败:', getErrorMessage(e2))
    }
  } finally {
    loading.value = false
  }
}

// ---- 窗口切换时重新加载 + 自动聚焦 ----
onMounted(() => {
  loadItems()
  setTimeout(() => inputRef.value?.focus(), 100)
})

// Reset selectedIndex when results change
watch(allResults, () => {
  selectedIndex.value = 0
})

// ---- 节流搜索（仅前端过滤，数据已全量加载） ----
</script>

<template>
  <div class="palette-overlay" @keydown="onKeydown">
    <div class="palette-window">
      <!-- 搜索输入 -->
      <div class="palette-input-wrap">
        <Search :size="16" class="palette-search-icon" />
        <input
          ref="inputRef"
          v-model="query"
          class="palette-input"
          :placeholder="t('cmdPlaceholder')"
          @keydown="onKeydown"
        />
        <kbd v-if="!query" class="palette-hint">Ctrl+K</kbd>
      </div>

      <!-- 结果列表 -->
      <div ref="listRef" class="palette-results" v-if="allResults.length > 0 && !showQueryInput">
        <div
          v-for="(result, idx) in allResults"
          :key="result.type + '-' + (result.item?.id || result.cmd?.id || idx)"
          :class="['pr-item', { active: idx === selectedIndex }]"
          @click="selectResult(idx); executeSelected()"
          @mouseenter="selectResult(idx)"
        >
          <div class="pr-icon">
            <template v-if="result.type === 'system'">
              <component :is="result.icon" :size="16" />
            </template>
            <template v-else-if="result.type === 'calculator'">
              <Hash :size="16" />
            </template>
            <template v-else-if="result.type === 'snippet'">
              <Clipboard :size="16" />
            </template>
            <template v-else>
              <Command :size="16" />
            </template>
          </div>
          <div class="pr-body">
            <span class="pr-label">{{ result.label }}</span>
            <span class="pr-desc" v-if="result.desc">{{ result.desc }}</span>
          </div>
          <div class="pr-meta">
            <template v-if="result.type === 'quicklink'">
              <ArrowRight :size="12" />
            </template>
            <template v-else-if="result.type === 'system'">
              <Monitor :size="10" />
            </template>
            <span v-else-if="result.type === 'snippet' && result.snippet?.category" class="pr-snippet-tag">{{ result.snippet.category }}</span>
          </div>
        </div>
      </div>

      <!-- Quicklink 输入弹窗 -->
      <div v-if="showQueryInput" class="palette-query-input">
        <div class="palette-query-header">
          <Command :size="14" />
          <span>{{ pendingQuicklink?.name }}</span>
        </div>
        <input
          v-model="queryValue"
          class="palette-query-field"
          :placeholder="queryPlaceholder"
          autofocus
          @keydown="onKeydown"
        />
        <div class="palette-query-actions">
          <span class="palette-query-hint">{{ t('enterQueryHint') }}</span>
          <button class="palette-query-btn" @click="commitQuicklink">{{ t('confirm') }}</button>
        </div>
      </div>

      <!-- 空状态 -->
      <div v-if="query && allResults.length === 0 && !showQueryInput" class="palette-empty">
        <p>{{ t('cmdNoResults') }}</p>
      </div>

      <!-- 初次提示 -->
      <div v-if="!query && !showQueryInput" class="palette-tips">
        <p class="palette-tip-line">{{ t('cmdTipSearch') }}</p>
        <p class="palette-tip-line">{{ t('cmdTipCalc') }}</p>
        <p class="palette-tip-line">{{ t('cmdTipSystem') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.palette-overlay {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 24px;
  background: transparent;
}

.palette-window {
  width: 100%;
  max-width: 580px;
  background: var(--color-bg-primary, #1e1e1e);
  border: 1px solid var(--color-border, #333);
  border-radius: 12px;
  box-shadow: 0 8px 32px rgba(0,0,0,0.4);
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.palette-input-wrap {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--color-border, #333);
}

.palette-search-icon {
  color: var(--color-text-muted, #888);
  flex-shrink: 0;
}

.palette-input {
  flex: 1;
  background: none;
  border: none;
  outline: none;
  color: var(--color-text-primary, #d4d4d4);
  font-size: 15px;
  font-family: inherit;
}

.palette-input::placeholder {
  color: var(--color-text-disabled, #555);
}

.palette-hint {
  background: var(--color-bg-hover, #2a2a2a);
  color: var(--color-text-muted, #777);
  font-size: 10px;
  padding: 2px 6px;
  border-radius: 4px;
  font-family: inherit;
  flex-shrink: 0;
}

/* 结果列表 */
.palette-results {
  max-height: 360px;
  overflow-y: auto;
  padding: 6px;
}

.pr-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.08s;
}

.pr-item.active {
  background: var(--color-accent-bg, rgba(74,158,255,0.1));
  outline: 1px solid var(--color-accent-border, rgba(74,158,255,0.15));
  outline-offset: -1px;
}

.pr-icon {
  width: 32px;
  height: 32px;
  border-radius: 6px;
  background: var(--color-bg-tertiary, #252525);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--color-accent, #4a9eff);
  flex-shrink: 0;
}

.pr-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.pr-label {
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary, #d4d4d4);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.pr-desc {
  font-size: 11px;
  color: var(--color-text-disabled, #555);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.pr-meta {
  color: var(--color-text-disabled, #555);
  flex-shrink: 0;
}

.pr-snippet-tag {
  font-size: 10px;
  background: var(--color-bg-tertiary, #252525);
  padding: 1px 6px;
  border-radius: 4px;
  color: var(--color-accent, #4a9eff);
}

/* Quicklink 输入 */
.palette-query-input {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.palette-query-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary, #d4d4d4);
}

.palette-query-field {
  padding: 10px 12px;
  border: 1px solid var(--color-accent-border, rgba(74,158,255,0.3));
  border-radius: 8px;
  background: var(--color-bg-secondary, #1f1f1f);
  color: var(--color-text-primary, #d4d4d4);
  font-size: 14px;
  font-family: inherit;
  outline: none;
}

.palette-query-field:focus {
  border-color: var(--color-accent, #4a9eff);
}

.palette-query-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.palette-query-hint {
  font-size: 11px;
  color: var(--color-text-disabled, #555);
}

.palette-query-btn {
  padding: 6px 16px;
  border: none;
  border-radius: 6px;
  background: var(--color-accent, #4a9eff);
  color: #fff;
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
}

/* 空状态 */
.palette-empty {
  padding: 32px;
  text-align: center;
  color: var(--color-text-disabled, #555);
  font-size: 13px;
}

/* 提示 */
.palette-tips {
  padding: 20px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.palette-tip-line {
  font-size: 12px;
  color: var(--color-text-disabled, #555);
  margin: 0;
}
</style>
