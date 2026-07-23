<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Search, Hash, Command, ArrowRight, Lock, Power, Moon, Trash2, RotateCcw,
  Link, Clipboard, Folder, Globe, Terminal, FileText, AppWindow, CornerDownLeft, ChevronUp, ChevronDown, ChevronLeft, X,
  MessageCircle, Code2, FolderOpen, Calculator, FileEdit, Server, Container, Palette, Music, Settings, Activity, Image, Camera, Puzzle, ExternalLink,
  Check, Bookmark
} from '@lucide/vue'
import { ListAllItems, ExecuteSystemCommand, OpenItem, HidePaletteWindow, ListSnippets, PasteSnippet, GetLastCopiedText, ScanInstalledApps, LaunchInstalledApp, ListPlugins, ExecutePluginCommand, GetPluginFrontendPage, SetPendingPluginInit, GetAndClearPendingPluginInit, ShowPluginWindow, GetRecentUsage, SaveUrlAsItem, CopyText, GetPluginIcon } from '../../bindings/quickdock/services/appservice'
import { Events, Browser } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { CollectionItem, PluginInfo } from '../types'
import type { ToastAPI } from '../types'
import { evaluate, format, convertExpression } from '../utils/calc'
import { pinyin } from 'pinyin-pro'
import { useFrecency } from '../composables/useFrecency'
import { usePluginIndex } from '../composables/usePluginIndex'
import { useInlinePlugin } from '../composables/useInlinePlugin'
import { useCommandSearch } from '../composables/useCommandSearch'
import type { RecentEntry } from '../composables/useCommandSearch'

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')

// ---- 初始化 composables ----
const { frecencyTick, loadFrecency, recordUsage, frecencyScore } = useFrecency()
const { pluginCmdIndex, buildPluginIndex, calcPluginScore, matchTypeLabels } = usePluginIndex()
const {
  inlinePluginId, inlinePluginHtml, inlinePluginLoading, inlinePluginError,
  inlinePluginName, inlinePluginIframe, closeInlinePlugin, detachPlugin, onInlinePluginLoad
} = useInlinePlugin()

// ---- 关闭面板 ----
function closePalette() {
  closeInlinePlugin()
  query.value = ''
  selectedIndex.value = 0
  selectedSet.value = new Set()
  lastAnchor.value = -1
  inlineQuicklink.value = null
  inlineQuery.value = ''
  pluginResultCache.value = null
  clipboardUrlSource.value = ''
  try { HidePaletteWindow() } catch (e) { console.error('[CmdPalette] HidePaletteWindow:', e) }
}

// 写入系统剪贴板（优先 Go 侧 CopyText，规避 WebView2 中 navigator.clipboard 静默失败）
async function writeClipboard(text: string) {
  try { await CopyText(text) } catch (e) { console.error('[CmdPalette] CopyText:', e) }
}

// ---- 应用图标映射 ----
const APP_ICON_MAP: [RegExp, any][] = [
  [/chrome|google/i, Globe],
  [/firefox|edge|brave|opera|safari|浏览器/i, Globe],
  [/微信|wechat|weixin/i, MessageCircle],
  [/qq|tencent/i, MessageCircle],
  [/terminal|cmd|powershell|wsl|命令提示符|windows terminal/i, Terminal],
  [/code|vs Code|visual studio|jetbrains|idea|goland|webstorm|pycharm|sublime|atom/i, Code2],
  [/文件资源管理器|explorer|文件管理/i, FolderOpen],
  [/计算器|calculator/i, Calculator],
  [/notepad|记事本|编辑/i, FileEdit],
  [/vscode|visual studio code/i, Code2],
  [/postman|apifox|curl/i, Server],
  [/docker/i, Container],
  [/figma|sketch|xd|ps|photoshop/i, Palette],
  [/spotify|音乐|网易云|qq音乐/i, Music],
  [/word|excel|powerpoint|office|wps|文档|表格|演示/i, FileText],
  [/设置|settings|control panel|控制面板/i, Settings],
  [/任务管理器|task manager/i, Activity],
  [/画图|paint|mspaint/i, Image],
  [/截图|snip|snipping tool/i, Camera],
]

// ---- 应用中文别名映射 ----
const APP_NAME_ALIASES: [RegExp, string[]][] = [
  [/notepad/i, ['记事本', 'jb']],
  [/calculator/i, ['计算器', 'jsq']],
  [/paint|mspaint/i, ['画图', 'ht']],
  [/snipping/i, ['截图工具', '截图', 'jttj']],
  [/explorer/i, ['文件资源管理器', '资源管理器', 'wjj']],
  [/task manager/i, ['任务管理器', 'rwglq']],
  [/control panel/i, ['控制面板', 'kzmb']],
  [/command prompt/i, ['命令提示符', 'cmd', 'mltsf']],
  [/registry editor|regedit/i, ['注册表编辑器', '注册表', 'zcb']],
  [/windows terminal/i, ['终端', 'zd']],
  [/word/i, ['文档', 'wd']],
  [/excel/i, ['表格', 'bg']],
  [/powerpoint|ppt/i, ['演示文稿', 'ppt']],
  [/visual studio code|vscode/i, ['代码编辑器', 'vscode']],
  [/steam/i, ['游戏平台']],
  [/wechat|weixin/i, ['微信', 'wx']],
  [/qq\b/i, ['腾讯qq']],
  [/discord/i, ['discord聊天']],
  [/spotify/i, ['音乐播放器']],
  [/docker/i, ['容器引擎']],
  [/postman/i, ['接口测试']],
  [/figma/i, ['设计工具']],
  [/photoshop|ps\b/i, ['图像编辑']],
  [/snipaste/i, ['截图工具']],
  [/everything/i, ['文件搜索']],
  [/7-zip|7zip|winrar/i, ['压缩软件']],
  [/vmware|virtualbox/i, ['虚拟机']],
  [/git/i, ['版本控制']],
  [/node/i, ['node运行时']],
  [/python/i, ['python运行时']],
]

// ---- 类型 ----
type ResultType = 'item' | 'system' | 'quicklink' | 'quicklink-inline' | 'calculator' | 'snippet' | 'app' | 'plugin' | 'url' | 'clipboard-action'
interface InstalledApp { name: string; path: string; category: string; iconBase64?: string }
interface SystemCmd { id: string; label: string; desc: string; keywords: string[]; icon: any; action: () => Promise<void> }
interface CmdSnippet { id: string; keyword: string; content: string; category: string; createdAt: string }
interface SearchResult {
  type: ResultType; label: string; desc?: string; icon?: any; iconBase64?: string
  item?: CollectionItem; cmd?: SystemCmd; calcResult?: string; snippet?: CmdSnippet; inlineQuery?: string
  frecencyScore?: number; appPath?: string; appCategory?: string; pluginId?: string; pluginCommandId?: string
  pluginHasFrontend?: boolean; inlineInput?: string; pluginResult?: string; score?: number; matchType?: string; url?: string; clipAction?: string; acceptsInput?: boolean
}

// ---- 状态 ----
const query = ref('')
const inputRef = ref<HTMLInputElement | null>(null)
const items = ref<CollectionItem[]>([])
const installedApps = ref<InstalledApp[]>([])
const installedPlugins = ref<PluginInfo[]>([])
const pluginIcons = ref<Record<string, string>>({}) // pluginId → data URI（真实插件图标，来自 GetPluginIcon）
const loading = ref(false)
const selectedIndex = ref(0)
const listRef = ref<HTMLElement | null>(null)
const selectedSet = ref<Set<number>>(new Set())
const lastAnchor = ref(-1)
const clipboardUrlSource = ref('')
const pluginResultCache = ref<{ result: string; pluginName: string; pluginId?: string; pluginCommandId?: string; pluginHasFrontend?: boolean; input?: string; acceptsInput?: boolean } | null>(null)
const inlineQuicklink = ref<CollectionItem | null>(null)
const inlineQuery = ref('')
const inlineInputRef = ref<HTMLInputElement | null>(null)
const snippets = ref<CmdSnippet[]>([])

// ---- 系统命令 ----
const systemCommands = computed<SystemCmd[]>(() => [
  { id: 'lock', label: t('cmdLock'), desc: t('cmdLockDesc'), keywords: ['lock', '锁屏', '锁定', 'suo ping', 'suo ding', '系统'], icon: Lock,
    action: async () => { await ExecuteSystemCommand('lock'); closePalette() } },
  { id: 'shutdown', label: t('cmdShutdown'), desc: t('cmdShutdownDesc'), keywords: ['shutdown', '关机', 'guan ji', '关闭', '系统'], icon: Power,
    action: async () => { await ExecuteSystemCommand('shutdown'); closePalette() } },
  { id: 'restart', label: t('cmdRestart'), desc: t('cmdRestartDesc'), keywords: ['restart', '重启', 'reboot', 'chong qi', '重新启动', '系统'], icon: RotateCcw,
    action: async () => { await ExecuteSystemCommand('restart'); closePalette() } },
  { id: 'sleep', label: t('cmdsleep'), desc: t('cmdsleepDesc'), keywords: ['sleep', '休眠', '睡眠', 'shui mian', 'xiu mian', '系统'], icon: Moon,
    action: async () => { await ExecuteSystemCommand('sleep'); closePalette() } },
  { id: 'emptytrash', label: t('cmdEmptyTrash'), desc: t('cmdEmptyTrashDesc'), keywords: ['trash', '回收站', '清空', '垃圾', 'hui shou zhan', 'qing kong', '系统'], icon: Trash2,
    action: async () => { await ExecuteSystemCommand('emptytrash'); closePalette() } },
])

// ---- 拼音匹配 ----
function pinyinMatch(text: string, queryLC: string, cacheKey?: string): boolean {
  if (!text || !queryLC) return false
  let initials: string, full: string
  if (cacheKey) {
    const cached = pinyinCache.get(cacheKey)
    if (cached) { initials = cached.init; full = cached.full }
    else {
      const pyArr = pinyin(text, { pattern: 'first', toneType: 'none', type: 'array' })
      initials = pyArr.map(p => p[0]).join('').toLowerCase()
      full = pinyin(text, { toneType: 'none', type: 'array' }).join('').toLowerCase()
    }
  } else {
    const pyArr = pinyin(text, { pattern: 'first', toneType: 'none', type: 'array' })
    initials = pyArr.map(p => p[0]).join('').toLowerCase()
    full = pinyin(text, { toneType: 'none', type: 'array' }).join('').toLowerCase()
  }
  if (initials.startsWith(queryLC)) return true
  if (full.includes(queryLC)) return true
  return false
}

// ---- 拼音缓存 ----
const pinyinCache = new Map<string, { init: string; full: string }>()
function rebuildPinyinCache() {
  pinyinCache.clear()
  for (const item of items.value) {
    const py = pinyin(item.name, { toneType: 'none', type: 'array' })
    pinyinCache.set('i:' + item.id, { init: py.map(p => p[0]).join('').toLowerCase(), full: py.join('').toLowerCase() })
  }
  for (const s of snippets.value) {
    const py = pinyin(s.keyword, { toneType: 'none', type: 'array' })
    pinyinCache.set('s:' + s.id, { init: py.map(p => p[0]).join('').toLowerCase(), full: py.join('').toLowerCase() })
  }
  for (const app of installedApps.value) {
    const py = pinyin(app.name, { toneType: 'none', type: 'array' })
    pinyinCache.set('a:' + app.name, { init: py.map(p => p[0]).join('').toLowerCase(), full: py.join('').toLowerCase() })
  }
  for (const cmd of systemCommands.value) {
    const py = pinyin(cmd.label, { toneType: 'none', type: 'array' })
    pinyinCache.set('sys:' + cmd.id, { init: py.map(p => p[0]).join('').toLowerCase(), full: py.join('').toLowerCase() })
  }
}
watch([items, snippets, installedApps], () => { rebuildPinyinCache() })
watch(systemCommands, () => { rebuildPinyinCache() })

// ---- 项目类型图标 ----
const ITEM_TYPE_ICONS: Record<string, any> = {
  '网页': Globe, '命令': Terminal, '文件': FileText, '应用': AppWindow, '快速链接': Link,
}
function itemIcon(item: CollectionItem): any { return ITEM_TYPE_ICONS[item.type] || Folder }
function appIcon(name: string): any {
  for (const [re, icon] of APP_ICON_MAP) { if (re.test(name)) return icon }
  return AppWindow
}
function getAppAliases(name: string): string[] {
  for (const [re, aliases] of APP_NAME_ALIASES) { if (re.test(name)) return aliases }
  return []
}

// ---- 搜索结果（useCommandSearch）----
const {
  groupedResults, allResults, recentResults, displayGroups, displayFlat,
  previewResult, recentCache
} = useCommandSearch({
  items, installedApps, snippets, systemCommands, query, selectedIndex,
  pluginCmdIndex, pluginResultCache, clipboardUrlSource,
  frecencyScore, frecencyTick, calcPluginScore,
  pinyinMatch, appIcon, getAppAliases, itemIcon, t, pluginIcons,
})

// ---- 键盘导航 ----
function scrollToSelected() {
  nextTick(() => {
    const list = listRef.value
    if (!list) return
    const el = list.querySelector('.result-item.active') as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  })
}

function onKeydown(e: KeyboardEvent) {
  if (inlineQuicklink.value) {
    if (e.key === 'Enter') { e.preventDefault(); commitInlineQuicklink() }
    else if (e.key === 'Escape') { e.preventDefault(); cancelInlineQuicklink() }
    return
  }
  const list = displayFlat.value
  if (list.length === 0 && e.key !== 'Escape') return
  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault(); selectedIndex.value = (selectedIndex.value + 1) % Math.max(list.length, 1); scrollToSelected(); break
    case 'ArrowUp':
      e.preventDefault(); selectedIndex.value = (selectedIndex.value - 1 + list.length) % Math.max(list.length, 1); scrollToSelected(); break
    case 'a': case 'A':
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault(); const s = new Set<number>(); displayFlat.value.forEach((_, i) => s.add(i)); selectedSet.value = s
      }
      break
    case 'Enter': e.preventDefault(); executeSelected(); break
    case 'Escape':
      if (inlinePluginId.value) closeInlinePlugin(); else closePalette()
      break
  }
}

// ---- 执行 ----
function isOpenable(r: SearchResult): boolean {
  return r.type === 'item' || r.type === 'url' || r.type === 'app' || r.type === 'quicklink' || r.type === 'quicklink-inline'
}

async function openResultOnly(r: SearchResult): Promise<boolean> {
  if (r.type === 'quicklink' && r.item) {
    inlineQuicklink.value = r.item; inlineQuery.value = ''; await nextTick(); inlineInputRef.value?.focus(); return true
  }
  if (r.type === 'quicklink-inline' && r.item) {
    const item = { ...r.item }; let value = item.value || ''
    if (r.inlineQuery) value = value.replace(/\{query\}/g, r.inlineQuery)
    item.value = value; recordUsage('item:' + item.id, 'item', item.name, item.value || '')
    try { await OpenItem(item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
    return true
  }
  if (r.type === 'item' && r.item) {
    recordUsage('item:' + r.item.id, 'item', r.label, r.desc)
    try { await OpenItem(r.item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
    return true
  }
  if (r.type === 'url' && r.url) {
    try { await Browser.OpenURL(r.url) } catch (e) { console.error('[CmdPalette] OpenURL:', e) }
    return true
  }
  if (r.type === 'app' && r.appPath) {
    recordUsage('app:' + r.label, 'app', r.label, r.desc)
    try { await LaunchInstalledApp(r.appPath) } catch (e) { console.error('[CmdPalette] LaunchInstalledApp:', e) }
    return true
  }
  return false
}

async function executeBatch() {
  const indices = [...selectedSet.value].sort((a, b) => a - b); let n = 0
  for (const idx of indices) {
    const r = displayFlat.value[idx]
    if (r && isOpenable(r)) { const ok = await openResultOnly(r); if (ok) n++ }
  }
  selectedSet.value = new Set(); lastAnchor.value = -1
  if (n > 0) toast?.success?.(t('openedCount', { n }))
}

async function executeSelected() {
  if (selectedSet.value.size > 0) { await executeBatch(); return }
  const result = displayFlat.value[selectedIndex.value]
  if (!result) return
  if (result.type === 'system' && result.cmd) {
    recordUsage('system:' + result.cmd.id, 'system', result.label, result.desc); await result.cmd.action()
  } else if (result.type === 'quicklink-inline' && result.item) {
    const item = { ...result.item }; let value = item.value || ''
    if (result.inlineQuery) value = value.replace(/\{query\}/g, result.inlineQuery)
    item.value = value; recordUsage('item:' + item.id, 'item', item.name, item.value || '')
    try { await OpenItem(item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }; closePalette()
  } else if (result.type === 'quicklink' && result.item) {
    inlineQuicklink.value = result.item; inlineQuery.value = ''; await nextTick(); inlineInputRef.value?.focus()
  } else if (result.type === 'item' && result.item) {
    recordUsage('item:' + result.item.id, 'item', result.label, result.desc)
    try { await OpenItem(result.item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }; closePalette()
  } else if (result.type === 'url' && result.url) {
    try { await Browser.OpenURL(result.url) } catch (e) { console.error('[CmdPalette] OpenURL:', e) }; closePalette()
  } else if (result.type === 'calculator' && result.calcResult) {
    try { await writeClipboard(result.calcResult) } catch {}; closePalette()
  } else if (result.type === 'snippet' && result.snippet) {
    recordUsage('snippet:' + result.snippet.id, 'snippet', result.label, result.desc)
    try { await PasteSnippet(result.snippet.content) } catch (e) { console.error('[CmdPalette] PasteSnippet:', e) }; closePalette()
  } else if (result.type === 'app' && result.appPath) {
    recordUsage('app:' + result.label, 'app', result.label, result.desc)
    try { await LaunchInstalledApp(result.appPath) } catch (e) { console.error('[CmdPalette] LaunchInstalledApp:', e) }; closePalette()
  } else if (result.type === 'plugin' && result.pluginId && result.pluginCommandId) {
    // 仅当命令声明 acceptsInput 时，才把 Ctrl+K 文本作为插件参数带入；否则不传（"不设置就不带"）
    let inputText: string | undefined
    if (result.acceptsInput) {
      inputText = result.inlineInput || undefined
      if (!inputText && result.label) {
        const idx = pluginCmdIndex.value.find(c => c.plugin.id === result.pluginId && c.cmd.id === result.pluginCommandId)
        if (idx && idx.cmd.title && result.label.startsWith(idx.cmd.title + ': ')) {
          inputText = result.label.slice(idx.cmd.title.length + 2)
        }
      }
    }
    recordUsage('plugin:' + result.pluginId + '.' + result.pluginCommandId, 'plugin', result.label, result.desc, inputText || '')
    try {
      if (result.pluginHasFrontend) {
        // 前端插件：打开 UI，参数经 plugin:init 注入，由插件自身自动执行具体功能
        if (inputText) { try { await SetPendingPluginInit(inputText, result.pluginCommandId || '') } catch {} }
        inlinePluginId.value = result.pluginId; inlinePluginLoading.value = true; inlinePluginError.value = ''
        try {
          const html = unwrap<string>(await GetPluginFrontendPage(result.pluginId))
          if (html) { const tmatch = html.match(/<title>([^<]*)<\/title>/); inlinePluginName.value = tmatch ? tmatch[1] : result.label; inlinePluginHtml.value = html }
          else { inlinePluginError.value = t('pluginNoFrontend') }
        } catch (e: any) { inlinePluginError.value = t('pluginLoadFailed') + ': ' + getErrorMessage(e) }
        inlinePluginLoading.value = false
      } else {
        // 无前端插件（goja/native 无 UI）：后端执行并返回结果
        const raw = await ExecutePluginCommand(result.pluginId, result.pluginCommandId, inputText ? { text: inputText } : null as any)
        const pluginResult = unwrap<string | any>(raw)
        if (pluginResult && pluginResult.error) { toast?.error?.(pluginResult.error) }
        else if (pluginResult) {
          const displayText = typeof pluginResult === 'object'
            ? (pluginResult.translated || pluginResult.text || pluginResult.display || JSON.stringify(pluginResult)) : String(pluginResult)
          const copyText = typeof pluginResult === 'object'
            ? (pluginResult.translated || pluginResult.text || pluginResult.copy || displayText) : displayText
          try { await writeClipboard(copyText) } catch {}
          pluginResultCache.value = { result: displayText.slice(0, 150), pluginName: result.desc || result.label, pluginId: result.pluginId, pluginCommandId: result.pluginCommandId, pluginHasFrontend: false, input: inputText, acceptsInput: result.acceptsInput }
          toast?.success?.(t('pluginResultCopied'))
        }
      }
    } catch (e) { toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e)) }
  } else if (result.type === 'clipboard-action' && result.clipAction) {
    if (result.clipAction === 'open-url' && result.url) {
      try { await Browser.OpenURL(result.url) } catch (e) { console.error('[CmdPalette] OpenURL:', e) }; closePalette()
    } else if (result.clipAction === 'save-url' && result.url) {
      try { const item = unwrap<CollectionItem>(await SaveUrlAsItem(result.url)); recordUsage('item:' + (item?.id || ''), 'item', result.url, result.url); toast?.success?.(t('savedAsItem')) } catch (e) { toast?.error?.(getErrorMessage(e)) }; closePalette()
    } else if (result.clipAction === 'encode-url' && result.url) {
      try { const raw = await ExecutePluginCommand('com.quickdock.text-encoder', 'url-encode', { text: result.url }); const res = unwrap<any>(raw); const text = typeof res === 'object' ? (res.translated || res.text || res.display || JSON.stringify(res)) : String(res); try { await writeClipboard(text) } catch {}; toast?.success?.(t('pluginResultCopied')) } catch (e) { toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e)) }; closePalette()
    }
  }
}

async function commitInlineQuicklink() {
  if (!inlineQuicklink.value) return
  const item = { ...inlineQuicklink.value }; let value = item.value || ''
  if (inlineQuery.value) value = value.replace(/\{query\}/g, inlineQuery.value)
  item.value = value; recordUsage('item:' + item.id, 'item', item.name, item.value || '')
  try { await OpenItem(item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
  inlineQuicklink.value = null; inlineQuery.value = ''; closePalette()
}

function cancelInlineQuicklink() { inlineQuicklink.value = null; inlineQuery.value = ''; nextTick(() => inputRef.value?.focus()) }

function selectResult(groupIdx: number, itemIdx: number) {
  let flatIdx = 0; for (let i = 0; i < groupIdx; i++) flatIdx += displayGroups.value[i].results.length
  selectedIndex.value = flatIdx + itemIdx
}

function onResultClick(gIdx: number, iIdx: number, ev: MouseEvent) {
  const flat = getFlatIndex(gIdx, iIdx)
  if (ev.ctrlKey || ev.metaKey) { const s = new Set(selectedSet.value); if (s.has(flat)) s.delete(flat); else s.add(flat); selectedSet.value = s; lastAnchor.value = flat; selectedIndex.value = flat; return }
  if (ev.shiftKey && lastAnchor.value >= 0) { const [a, b] = [lastAnchor.value, flat].sort((x, y) => x - y); const s = new Set(selectedSet.value); for (let i = a; i <= b; i++) s.add(i); selectedSet.value = s; selectedIndex.value = flat; return }
  selectedSet.value = new Set(); lastAnchor.value = flat; selectedIndex.value = flat; executeSelected()
}

function getFlatIndex(groupIdx: number, itemIdx: number): number {
  let flatIdx = 0; for (let i = 0; i < groupIdx; i++) flatIdx += displayGroups.value[i].results.length
  return flatIdx + itemIdx
}

// ---- 加载数据 ----
let itemsLoadGen = 0
let lastPluginIndexLoad = 0

async function loadPluginIndex() {
  const now = Date.now()
  if (now - lastPluginIndexLoad < 500) return
  lastPluginIndexLoad = now
  try {
    const plugins = unwrap<PluginInfo[]>(await ListPlugins())
    const running = plugins?.filter(p => p.status === 'running') || []
    installedPlugins.value = running
    pluginCmdIndex.value = buildPluginIndex(running)
    // 预加载插件真实图标（data URI），结果列表据此展示，无图标的插件回退到 Puzzle
    const iconPromises = running.map(async (p) => {
      try { const uri = unwrap<string | null>(await GetPluginIcon(p.id)); if (uri) pluginIcons.value[p.id] = uri } catch {}
    })
    await Promise.all(iconPromises)
  } catch (e) { console.error('[CmdPalette] ListPlugins:', getErrorMessage(e)) }
}

// 一次性加载全量池（项目 + 应用 + 片段 + 最近使用），后续匹配完全在前端完成，
// 从而支持拼音与子串搜索（后端 FTS5 前缀匹配无法覆盖这两类）。
async function loadPaletteData() {
  loading.value = true; const gen = ++itemsLoadGen
  await loadFrecency()
  try { const result = unwrap<CollectionItem[]>(await ListAllItems()); if (gen !== itemsLoadGen) return; items.value = result || [] } catch (e) { console.error('[CmdPalette] ListAllItems:', getErrorMessage(e)) }
  try { const apps = unwrap<InstalledApp[]>(await ScanInstalledApps()); if (gen === itemsLoadGen) installedApps.value = apps || [] } catch (e) { console.error('[CmdPalette] ScanInstalledApps:', getErrorMessage(e)) }
  try { const snips = unwrap<CmdSnippet[]>(await ListSnippets()); if (gen !== itemsLoadGen) return; snippets.value = snips || [] } catch (e) { console.error('[CmdPalette] ListSnippets:', getErrorMessage(e)) }
  try { const raw = await GetRecentUsage(20); if (gen === itemsLoadGen) recentCache.value = (unwrap<RecentEntry[]>(raw) || []).filter(e => e.type && e.label) } catch (e) { console.error('[CmdPalette] GetRecentUsage:', getErrorMessage(e)) }
  if (gen === itemsLoadGen) loading.value = false
}

// ---- 生命周期 ----
let lastClipboardUpdate = 0

onMounted(async () => {
  Events.On('clipboard:updated', () => { lastClipboardUpdate = Date.now() })
  Events.On('palette:shown', () => {
    if (inlinePluginId.value) closeInlinePlugin()
    loadPluginIndex()
    loadPaletteData().catch(e => console.warn('[CmdPalette] loadPaletteData:', e))
    query.value = ''; selectedIndex.value = 0; inlineQuicklink.value = null; inlineQuery.value = ''; pluginResultCache.value = null
    if (Date.now() - lastClipboardUpdate < 3000) {
      GetLastCopiedText().then(raw => {
        const copied = unwrap<string>(raw)
        if (copied && copied.trim() && copied.trim().length < 200) {
          const c = copied.trim()
          const isHttp = /^https?:\/\//i.test(c)
          const looksDomain = /^[a-z0-9][-a-z0-9]*\.[a-z]{2,}(\/|$)/i.test(c) && !/^[\d+\-*/().%^, ]+$/.test(c)
          if (isHttp || looksDomain) { const urlStr = isHttp ? c : 'https://' + c; query.value = urlStr; clipboardUrlSource.value = urlStr }
          else { query.value = c }
        }
      }).catch(() => {})
    }
    nextTick(() => { requestAnimationFrame(() => { inputRef.value?.focus(); inputRef.value?.select() }) })
  })
  await loadPaletteData()
  await loadPluginIndex()
})

// 仅在查询变化时重置选中（避免悬停/频次更新导致选中项跳回顶部）
watch(query, (val) => {
  if (!val.trim()) { pluginResultCache.value = null; inlineQuicklink.value = null; inlineQuery.value = '' }
  if (val.trim() !== clipboardUrlSource.value) clipboardUrlSource.value = ''
  selectedIndex.value = 0
  selectedSet.value = new Set()
  lastAnchor.value = -1
})

watch(inlineQuicklink, (v) => { if (v) selectedIndex.value = 0 })

onUnmounted(() => {
  closeInlinePlugin()
  Events.Off('palette:shown')
  Events.Off('clipboard:updated')
})
</script>

<template>
  <!-- Inline 插件模式 -->
  <div v-if="inlinePluginId" class="palette-root palette-plugin-mode" @click.self="closeInlinePlugin">
    <div class="palette-plugin-header">
      <button class="plugin-back-btn" @click="closeInlinePlugin">
        <ChevronLeft :size="16" />
        <span>{{ t('back') }}</span>
      </button>
      <span class="plugin-title">{{ inlinePluginName }}</span>
      <div class="plugin-header-actions">
        <button class="plugin-detach-btn" @click="detachPlugin" :title="t('pluginDetach')">
          <ExternalLink :size="14" />
        </button>
      </div>
    </div>
    <div class="palette-plugin-body">
      <div v-if="inlinePluginLoading" class="palette-plugin-status">{{ t('loading') }}</div>
      <div v-else-if="inlinePluginError" class="palette-plugin-status palette-plugin-error">{{ inlinePluginError }}</div>
      <iframe
        v-else-if="inlinePluginHtml"
        ref="inlinePluginIframe"
        :srcdoc="inlinePluginHtml"
        class="palette-plugin-iframe"
        sandbox="allow-scripts allow-same-origin allow-modals"
        frameborder="0"
        @load="onInlinePluginLoad"
      />
    </div>
  </div>

  <!-- 搜索模式 -->
  <div v-else class="palette-root" @click.self="closePalette">
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

    <div ref="listRef" class="palette-results" v-if="displayGroups.length > 0 && !inlineQuicklink">
      <template v-for="(group, gIdx) in displayGroups" :key="group.type">
        <div class="group-header">
          <span class="group-title">{{ group.label }}</span>
          <span class="group-count">{{ group.results.length }}</span>
        </div>
        <div
          v-for="(result, iIdx) in group.results"
          :key="group.type + '-' + (result.item?.id || result.cmd?.id || result.snippet?.id || result.url || iIdx)"
          :class="['result-item', { active: getFlatIndex(gIdx, iIdx) === selectedIndex, selected: selectedSet.has(getFlatIndex(gIdx, iIdx)) }]"
          @click="onResultClick(gIdx, iIdx, $event)"
          @mousemove="selectResult(gIdx, iIdx)"
        >
          <span v-if="selectedSet.has(getFlatIndex(gIdx, iIdx))" class="result-check"><Check :size="14" /></span>
          <div class="result-icon">
            <img v-if="result.iconBase64" :src="result.iconBase64" class="result-app-icon" alt="" />
            <component v-else :is="result.icon" :size="15" />
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
            <template v-else-if="result.type === 'plugin' && result.matchType">
              <span class="meta-tag">{{ matchTypeLabels[result.matchType!] || result.matchType }}</span>
            </template>
          </div>
        </div>
      </template>
    </div>

    <div class="palette-preview" v-if="previewResult && !inlineQuicklink">
      <div class="preview-head">
        <span class="preview-title">{{ previewResult.title }}</span>
        <span class="preview-subtitle" v-if="previewResult.subtitle">{{ previewResult.subtitle }}</span>
      </div>
      <div class="preview-body">
        <div v-for="(line, i) in previewResult.lines" :key="i" class="preview-line">{{ line }}</div>
      </div>
    </div>

    <div v-if="displayGroups.length === 0 && !inlineQuicklink" class="palette-empty">
      <Search :size="28" class="empty-icon" />
      <p class="empty-title">{{ t('cmdEmptyTitle') }}</p>
      <p class="empty-desc">{{ t('cmdEmptyDesc') }}</p>
    </div>

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

.palette-searchbar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 18px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}

.search-icon { color: var(--color-text-muted); flex-shrink: 0; }

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
.search-input::placeholder { color: var(--color-text-muted); }

.inline-prefix {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--color-accent);
  font-size: 13px;
  font-weight: 500;
  flex-shrink: 0;
}
.inline-name { color: var(--color-accent); }
.inline-sep { color: var(--color-text-muted); }
.inline-input { font-size: 14px; }
.inline-cancel {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; border: none; border-radius: 5px;
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  cursor: pointer; flex-shrink: 0;
  transition: background 0.12s, color 0.12s;
}
.inline-cancel:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.palette-results { flex: 1; overflow-y: auto; padding: 6px 8px 10px; }

.group-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 10.5px;
  font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.09em;
  padding: 14px 12px 6px;
  user-select: none;
}
.group-header:not(:first-of-type) {
  border-top: 1px solid var(--color-border);
  margin-top: 4px;
}
.group-title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.group-count {
  flex-shrink: 0;
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0;
  color: var(--color-text-disabled);
  background: var(--color-bg-tertiary);
  padding: 1px 7px;
  border-radius: var(--radius-full);
}

.result-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 12px;
  border-radius: var(--radius-lg);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}
.result-item:hover { background: var(--color-bg-hover); }
.result-item.active { background: var(--color-accent-bg); }
.result-item.active::before {
  content: '';
  position: absolute;
  left: 3px;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 56%;
  border-radius: 0 2px 2px 0;
  background: var(--color-accent);
}

.result-icon {
  width: 30px; height: 30px; border-radius: 7px;
  background: var(--color-bg-tertiary);
  display: flex; align-items: center; justify-content: center;
  color: var(--color-text-secondary); flex-shrink: 0;
  transition: color var(--transition-fast), background var(--transition-fast);
}
.result-item:hover .result-icon { color: var(--color-text-primary); }
.result-item.active .result-icon { color: var(--color-accent); background: var(--color-accent-bg); }

.result-app-icon { width: 20px; height: 20px; object-fit: contain; border-radius: 4px; }

.result-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }

.result-label {
  font-size: 13.5px; font-weight: 500; color: var(--color-text-primary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; letter-spacing: 0.01em;
}

.result-desc {
  font-size: 11.5px; color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; letter-spacing: 0.01em;
}

.result-meta { color: var(--color-text-muted); flex-shrink: 0; display: flex; align-items: center; gap: 4px; }
.result-item.active .result-meta { color: var(--color-text-secondary); }

.meta-tag {
  font-size: 10px;
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  padding: 2px 7px;
  border-radius: var(--radius-full);
  color: var(--color-text-muted);
  letter-spacing: 0.02em;
  white-space: nowrap;
}
.result-item.active .meta-tag { background: var(--color-bg-hover); border-color: var(--color-accent-border); color: var(--color-text-secondary); }

.palette-empty {
  flex: 1; display: flex; flex-direction: column; align-items: center;
  justify-content: center; gap: 8px; padding: 32px;
}
.empty-icon { color: var(--color-text-disabled); margin-bottom: 4px; }
.empty-title { font-size: 14px; font-weight: 500; color: var(--color-text-secondary); margin: 0; }
.empty-desc { font-size: 12px; color: var(--color-text-muted); margin: 0; text-align: center; max-width: 360px; line-height: 1.5; }

.palette-footer {
  display: flex; align-items: center; gap: 20px;
  padding: 8px 18px; border-top: 1px solid var(--color-border); flex-shrink: 0;
}
.footer-hint { display: flex; align-items: center; gap: 4px; font-size: 11px; color: var(--color-text-muted); }
.footer-hint kbd {
  display: flex; align-items: center; justify-content: center;
  min-width: 18px; height: 18px; padding: 0 4px; border-radius: 4px;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-size: 10px; font-family: var(--font-mono, monospace); font-weight: 500;
}

.result-item.selected { background: var(--color-accent-bg); }
.result-item.selected::before {
  content: '';
  position: absolute;
  left: 3px; top: 50%; transform: translateY(-50%);
  width: 3px; height: 56%; border-radius: 0 2px 2px 0;
  background: var(--color-accent);
}
.result-check {
  display: flex; align-items: center; justify-content: center;
  width: 18px; height: 18px; border-radius: var(--radius-sm); background: var(--color-accent); color: #fff; flex-shrink: 0;
}

.palette-preview {
  flex-shrink: 0; max-height: 96px; overflow-y: auto;
  padding: 8px 16px; border-top: 1px solid var(--color-border); background: var(--color-bg-secondary);
}
.preview-head { display: flex; align-items: baseline; gap: 8px; margin-bottom: 4px; }
.preview-title { font-size: 12.5px; font-weight: 600; color: var(--color-text-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.preview-subtitle { font-size: 10.5px; color: var(--color-text-muted); flex-shrink: 0; }
.preview-body { display: flex; flex-direction: column; gap: 2px; }
.preview-line { font-size: 11.5px; color: var(--color-text-secondary); font-family: var(--font-mono, monospace); white-space: pre-wrap; word-break: break-all; line-height: 1.45; }

.palette-results::-webkit-scrollbar { width: 5px; }
.palette-results::-webkit-scrollbar-track { background: transparent; }
.palette-results::-webkit-scrollbar-thumb { background: var(--color-scrollbar-thumb); border-radius: 3px; }
.palette-results::-webkit-scrollbar-thumb:hover { background: var(--color-scrollbar-hover); }

.palette-plugin-mode { background: var(--color-bg-primary); }

.palette-plugin-header {
  display: flex; align-items: center; gap: 6px; height: 36px; flex-shrink: 0;
  padding: 0 6px; background: var(--color-bg-secondary);
  box-shadow: inset 0 -1px 0 0 var(--color-border);
  -webkit-app-region: drag; user-select: none;
}

.plugin-back-btn {
  display: flex; align-items: center; gap: 2px; height: 28px; padding: 0 6px;
  border: none; border-radius: 6px; background: transparent; color: var(--color-text-muted);
  font-size: 12px; font-weight: 500; font-family: inherit; cursor: pointer;
  -webkit-app-region: no-drag;
  transition: background 0.1s, color 0.1s;
}
.plugin-back-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.plugin-back-btn:active { background: var(--color-bg-active); }
.plugin-back-btn svg { width: 15px; height: 15px; }

.plugin-title {
  flex: 1; font-size: 12px; font-weight: 500; color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  text-align: center; letter-spacing: 0.02em;
}

.plugin-header-actions { display: flex; align-items: center; gap: 2px; flex-shrink: 0; }

.plugin-detach-btn {
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border: none; border-radius: 6px;
  background: transparent; color: var(--color-text-disabled); cursor: pointer;
  -webkit-app-region: no-drag;
  transition: background 0.12s, color 0.12s;
}
.plugin-detach-btn:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.plugin-detach-btn:active { background: var(--color-bg-active); }

.palette-plugin-body { flex: 1; display: flex; overflow: hidden; }

.palette-plugin-status {
  flex: 1; display: flex; align-items: center; justify-content: center;
  font-size: 13px; color: var(--color-text-disabled); user-select: none;
}
.palette-plugin-error { color: var(--color-danger); padding: 0 24px; text-align: center; line-height: 1.6; }

.palette-plugin-iframe { flex: 1; width: 100%; height: 100%; border: none; background: var(--color-bg-primary); }
</style>
