<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Search, Hash, Command, ArrowRight, Lock, Power, Moon, Trash2, RotateCcw,
  Link, Clipboard, Folder, Globe, Terminal, FileText, AppWindow, CornerDownLeft, ChevronUp, ChevronDown, X,
  MessageCircle, Code2, FolderOpen, Calculator, FileEdit, Server, Container, Palette, Music, Settings, Activity, Image, Camera, Puzzle
} from '@lucide/vue'
import { SearchAll, ExecuteSystemCommand, OpenItem, HidePaletteWindow, SearchSnippets, PasteSnippet, GetLastCopiedText, GetMostUsedItems, ScanInstalledApps, LaunchInstalledApp, ListPlugins, ExecutePluginCommand, ShowPluginWindow, SetPendingPluginInit } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { CollectionItem, PluginInfo } from '../types'
import type { ToastAPI } from '../types'
import { evaluate, format } from '../utils/calc'
import { pinyin } from 'pinyin-pro'
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')

// ---- 关闭面板（清空搜索框和结果缓存）----
function closePalette() {
  query.value = ''
  selectedIndex.value = 0
  inlineQuicklink.value = null
  inlineQuery.value = ''
  pluginResultCache.value = null
  try { HidePaletteWindow() } catch (e) { console.error('[CmdPalette] HidePaletteWindow:', e) }
}

// ---- 应用图标映射（根据名称匹配常见应用）----
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

// ---- 应用中文别名映射（用于搜索匹配）----
// 键 = 英文名关键词（小写），值 = 中文名别名词组
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
type ResultType = 'item' | 'system' | 'quicklink' | 'quicklink-inline' | 'calculator' | 'snippet' | 'app' | 'plugin'

interface InstalledApp {
  name: string
  path: string
  category: string
  iconBase64?: string
}

interface SearchResult {
  type: ResultType
  label: string
  desc?: string
  icon?: any
  iconBase64?: string
  item?: CollectionItem
  cmd?: SystemCmd
  calcResult?: string
  snippet?: CmdSnippet
  inlineQuery?: string
  frecencyScore?: number
  appPath?: string
  appCategory?: string
  pluginId?: string
  pluginCommandId?: string
  pluginHasFrontend?: boolean
  inlineInput?: string           // 从查询中提取的内联输入文本
  pluginResult?: string          // 插件执行结果文本（执行后缓存）
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

// 内存缓存 — 启动时从 localStorage 加载一次，避免每次搜索都 JSON.parse
let frecencyCache: Record<string, FrecencyEntry> = {}
let frecencyLoaded = false

function loadFrecency(): Record<string, FrecencyEntry> {
  if (frecencyLoaded) return frecencyCache
  try {
    const raw = localStorage.getItem(FRECENCY_KEY)
    frecencyCache = raw ? JSON.parse(raw) : {}
  } catch {
    frecencyCache = {}
  }
  frecencyLoaded = true
  return frecencyCache
}

function saveFrecency(data: Record<string, FrecencyEntry>) {
  frecencyCache = data
  try { localStorage.setItem(FRECENCY_KEY, JSON.stringify(data)) } catch {}
}

function recordUsage(key: string) {
  const data = { ...loadFrecency() }
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
  return entry.count * 10 + Math.max(0, 30 - recencyDays)
}

// ---- 状态 ----
const query = ref('')
const inputRef = ref<HTMLInputElement | null>(null)
const items = ref<CollectionItem[]>([])
const installedApps = ref<InstalledApp[]>([])
const installedPlugins = ref<PluginInfo[]>([])
const loading = ref(false)
const selectedIndex = ref(0)
const listRef = ref<HTMLElement | null>(null)

// 插件执行结果缓存（留在结果列表中，直到关闭面板或输入新内容）
const pluginResultCache = ref<{ result: string; pluginName: string } | null>(null)

// Quicklink 内联输入模式
const inlineQuicklink = ref<CollectionItem | null>(null)
const inlineQuery = ref('')
const inlineInputRef = ref<HTMLInputElement | null>(null)

// ---- 片段 ----
const snippets = ref<CmdSnippet[]>([])

// ---- 系统命令（computed 以响应语言切换）----
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
// type 值来自后端 DB（schema.go: items.type DEFAULT '目录'），是稳定的中文枚举
// 不要与 i18n 翻译混淆
const ITEM_TYPE_ICONS: Record<string, any> = {
  '网页': Globe,
  '命令': Terminal,
  '文件': FileText,
  '应用': AppWindow,
  '快速链接': Link,
}
function itemIcon(item: CollectionItem): any {
  return ITEM_TYPE_ICONS[item.type] || Folder
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

  // 1. 计算器（仅 = 前缀触发，避免拦截插件输入）
  if (q.startsWith('=')) {
    const expr = q.slice(1)
    try {
      const result = evaluate(expr)
      if (result !== undefined && result !== null) {
        groups.push({
          type: 'calculator',
          label: t('cmdGroupCalc'),
          results: [{
            type: 'calculator',
            label: `${q} = ${format(result, { precision: 14 })}`,
            desc: t('calcHint'),
            icon: Hash,
            calcResult: String(result),
          }]
        })
      }
    } catch {}
  }

  // 2. 项目 + Quicklink — items.value 已由后端 FTS5 筛选
  // 前端补充拼音匹配 + 文本匹配（用户可能输入中文或拼音）
  const itemResults: SearchResult[] = []
  for (const item of items.value) {
    if (seen.has(item.id)) continue
    const nameLC = item.name.toLowerCase()
    const valueLC = (item.value || '').toLowerCase()
    // FTS5 已确保文本匹配，但拼音可能编码不一致需要前端补充
    // 同时允许中文直输匹配（用户输入中文名直接筛选）
    if (!(nameLC.includes(qLC) || valueLC.includes(qLC) || pinyinMatch(item.name, qLC))) continue
    seen.add(item.id)
    const isQuicklink = item.value && item.value.includes('{query}')
    itemResults.push({
      type: isQuicklink ? 'quicklink' : 'item',
      label: item.name,
      desc: item.value || '',
      icon: itemIcon(item),
      item,
      frecencyScore: frecencyScore('item:' + item.id),
    })
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

  // 5. 已安装的应用
  function appIcon(name: string): any {
    for (const [re, icon] of APP_ICON_MAP) {
      if (re.test(name)) return icon
    }
    return AppWindow
  }

  // 获取应用的中文别名
  function getAppAliases(name: string): string[] {
    for (const [re, aliases] of APP_NAME_ALIASES) {
      if (re.test(name)) return aliases
    }
    return []
  }

  const appResults: SearchResult[] = []
  for (const app of installedApps.value) {
    const nameLC = app.name.toLowerCase()
    // 匹配：英文名包含 / 拼音匹配 / 中文别名匹配
    const aliases = getAppAliases(app.name)
    const aliasMatch = aliases.some(a =>
      a.toLowerCase().includes(qLC) || pinyinMatch(a, qLC)
    )
    if (nameLC.includes(qLC) || pinyinMatch(app.name, qLC) || aliasMatch) {
      appResults.push({
        type: 'app',
        label: app.name,
        desc: app.category !== '其他' && app.category !== '系统工具' ? app.category : app.path,
        icon: appIcon(app.name),
        iconBase64: app.iconBase64,
        appPath: app.path,
        appCategory: app.category,
        frecencyScore: frecencyScore('app:' + app.name),
      })
    }
  }
  appResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
  if (appResults.length > 0) {
    groups.push({ type: 'app', label: t('cmdGroupApps'), results: appResults.slice(0, 8) })
  }

  // 6. 系统命令
  const sysResults: SearchResult[] = []
  for (const cmd of systemCommands.value) {
    if (cmd.keywords.some(k => k.includes(qLC)) || cmd.label.toLowerCase().includes(qLC) || pinyinMatch(cmd.label, qLC)) {
      sysResults.push({ type: 'system', label: cmd.label, desc: cmd.desc, icon: cmd.icon, cmd })
    }
  }
  if (sysResults.length > 0) {
    groups.push({ type: 'system', label: t('cmdGroupSystem'), results: sysResults })
  }

  // 7. 插件命令（支持内联输入：查询文本中自动提取输入参数）
  const pluginResults: SearchResult[] = []
  for (const plugin of installedPlugins.value) {
    if (plugin.status !== 'running') continue
    for (const cmd of plugin.commands) {
      // 检查命令标题或关键字是否匹配
      const titleLC = cmd.title.toLowerCase()
      const idLC = cmd.id.toLowerCase()
      const kwLC = (cmd.keywords || []).map(k => k.toLowerCase())
      const isCalcExpr = /^[0-9+\-*/().%^, ]+$/.test(qLC)
      // 匹配条件：标题/ID/关键字包含查询；或查询以关键字+空格开头（用于内联输入）；或纯数学表达式；或拼音匹配
      const kwMatch = kwLC.some(k => k.includes(qLC) || qLC.startsWith(k + ' '))
      const matchesPlugin = titleLC.includes(qLC) || idLC.includes(qLC) || kwMatch || pinyinMatch(cmd.title, qLC) || isCalcExpr
      if (!matchesPlugin) continue

      // 尝试从查询中提取内联输入
      // 如果查询词比命令标题长，多余的部分作为输入参数
      let inlineInput: string | undefined
      // 先用 keywords 匹配前缀（如 "计算 3500*0.8" → "计算" 是 keyword，提取 "3500*0.8"）
      let matchedPrefix = ''
      for (const kw of [cmd.title, ...(cmd.keywords || [])]) {
        const kwLower = kw.toLowerCase()
        if (kwLower.length < 2) continue
        if (qLC.startsWith(kwLower + ' ')) {
          matchedPrefix = kw
          break
        }
      }
      if (matchedPrefix && qLC.length > matchedPrefix.length) {
        // 去掉匹配的前缀，剩余部分作为内联输入
        inlineInput = query.value.slice(matchedPrefix.length).trim()
        if (inlineInput.startsWith(':') || inlineInput.startsWith('：')) {
          inlineInput = inlineInput.slice(1).trim()
        }
      }

      pluginResults.push({
        type: 'plugin',
        label: inlineInput ? `${cmd.title}: ${inlineInput}` : cmd.title,
        desc: plugin.name,
        icon: Puzzle,
        pluginId: plugin.id,
        pluginCommandId: cmd.id,
        pluginHasFrontend: plugin.hasFrontend,
        inlineInput,
        frecencyScore: frecencyScore('plugin:' + plugin.id + '.' + cmd.id),
      })
    }
  }
  // 加上之前执行的结果缓存（始终显示在插件分组顶部）
  if (pluginResultCache.value) {
    pluginResults.unshift({
      type: 'plugin',
      label: pluginResultCache.value.result,
      desc: '← ' + pluginResultCache.value.pluginName,
      icon: Puzzle,
      frecencyScore: 9999, // 置顶
    })
  }
  pluginResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
  if (pluginResults.length > 0) {
    groups.push({ type: 'plugin', label: t('cmdGroupPlugins'), results: pluginResults })
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
  nextTick(() => {
    const list = listRef.value
    if (!list) return
    const el = list.querySelector('.result-item.active') as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  })
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
      closePalette()
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
    const item = { ...result.item }
    let value = item.value || ''
    if (result.inlineQuery) {
      value = value.replace(/\{query\}/g, result.inlineQuery)
    }
    item.value = value
    recordUsage('item:' + item.id)
    try { await OpenItem(item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
    closePalette()
  } else if (result.type === 'quicklink' && result.item) {
    // 进入内联输入模式
    inlineQuicklink.value = result.item
    inlineQuery.value = ''
    await nextTick()
    inlineInputRef.value?.focus()
  } else if (result.type === 'item' && result.item) {
    recordUsage('item:' + result.item.id)
    try { await OpenItem(result.item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
    closePalette()
  } else if (result.type === 'calculator' && result.calcResult) {
    try { await navigator.clipboard.writeText(result.calcResult) } catch {}
    closePalette()
  } else if (result.type === 'snippet' && result.snippet) {
    recordUsage('snippet:' + result.snippet.id)
    try { await PasteSnippet(result.snippet.content) } catch (e) { console.error('[CmdPalette] PasteSnippet:', e) }
    closePalette()
  } else if (result.type === 'app' && result.appPath) {
    recordUsage('app:' + result.label)
    try { await LaunchInstalledApp(result.appPath) } catch (e) { console.error('[CmdPalette] LaunchInstalledApp:', e) }
    closePalette()
    } else if (result.type === 'plugin' && result.pluginId && result.pluginCommandId) {
    recordUsage('plugin:' + result.pluginId + '.' + result.pluginCommandId)
    try {
      // 统一输入：优先用内联输入的文本，否则用搜索框的查询文本
      const inputText = result.inlineInput || query.value.trim()
      const input: any = inputText ? { text: inputText } : null
      const raw = await ExecutePluginCommand(result.pluginId, result.pluginCommandId, input)
      const pluginResult = unwrap<string | any>(raw)

      if (pluginResult) {
        // 统一结果提取
        const displayText = typeof pluginResult === 'object'
          ? (pluginResult.translated || pluginResult.text || pluginResult.display || JSON.stringify(pluginResult))
          : String(pluginResult)
        const copyText = typeof pluginResult === 'object'
          ? (pluginResult.translated || pluginResult.text || pluginResult.copy || displayText)
          : displayText

        // 复制到剪贴板
        try { await navigator.clipboard.writeText(copyText) } catch {}
        // 缓存到结果列表顶部
        pluginResultCache.value = { result: displayText.slice(0, 150), pluginName: result.desc || result.label }
        // Toast 提示
        toast?.success?.(t('pluginResultCopied'))
      }

      // 如果有前端页面且用户输入了文本，把输入传给插件窗口（跨窗口传递）
      if ((result.pluginHasFrontend || result.pluginId) && inputText) {
        try { await SetPendingPluginInit(inputText) } catch {}
      }

      // 如果有前端页面，在新窗口打开
      if (result.pluginHasFrontend || result.pluginId) {
        try { await ShowPluginWindow(result.pluginId) } catch {}
      }
    } catch (e) {
      toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
    }
    // 不清空 query，保留面板供查看结果
    // 用户按 Esc 关闭
  }
}

async function commitInlineQuicklink() {
  if (!inlineQuicklink.value) return
  const item = { ...inlineQuicklink.value }
  let value = item.value || ''
  if (inlineQuery.value) {
    value = value.replace(/\{query\}/g, inlineQuery.value)
  }
  item.value = value
  recordUsage('item:' + item.id)
  try { await OpenItem(item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
  inlineQuicklink.value = null
  inlineQuery.value = ''
  closePalette()
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
// 命令面板打开时不再一次性加载全部项目，而是：
// 1. 打开时加载 Top-N 最常用项目（用于最近使用面板）
// 2. 用户输入搜索词时，后端 FTS5 精确匹配
let itemsLoadGen = 0

async function loadMostUsedItems() {
  loading.value = true
  const gen = ++itemsLoadGen
  try {
    const result = unwrap<CollectionItem[]>(await GetMostUsedItems(30))
    if (gen !== itemsLoadGen) return
    items.value = result || []
  } catch (e) {
    console.error('[CmdPalette] GetMostUsedItems:', getErrorMessage(e))
  }
  // 并行加载已安装应用（后端有缓存，只扫一次）
  try {
    const apps = unwrap<InstalledApp[]>(await ScanInstalledApps())
    if (gen === itemsLoadGen) installedApps.value = apps || []
  } catch (e) {
    console.error('[CmdPalette] ScanInstalledApps:', getErrorMessage(e))
  }
  try {
    const snips = unwrap<CmdSnippet[]>(await SearchSnippets(''))
    if (gen !== itemsLoadGen) return
    snippets.value = snips || []
  } catch (e) {
    console.error('[CmdPalette] SearchSnippets:', getErrorMessage(e))
  }
  // 加载运行中的插件命令
  try {
    const plugins = unwrap<PluginInfo[]>(await ListPlugins())
    if (gen === itemsLoadGen) installedPlugins.value = plugins?.filter(p => p.status === 'running') || []
  } catch (e) {
    console.error('[CmdPalette] ListPlugins:', getErrorMessage(e))
  } finally {
    if (gen === itemsLoadGen) loading.value = false
  }
}

// 用户输入搜索词时 → 后端 FTS5 搜索
async function searchItems(query: string) {
  if (!query.trim()) {
    // 无查询时加载 Top 常用
    await loadMostUsedItems()
    return
  }
  loading.value = true
  const gen = ++itemsLoadGen
  try {
    const result = unwrap<CollectionItem[]>(await SearchAll(query))
    if (gen !== itemsLoadGen) return
    items.value = result || []
  } catch (e) {
    console.error('[CmdPalette] SearchAll:', getErrorMessage(e))
    if (gen === itemsLoadGen) loading.value = false
  }
  // 片段搜索（后端已有 LIKE 过滤）
  try {
    const snips = unwrap<CmdSnippet[]>(await SearchSnippets(query))
    if (gen !== itemsLoadGen) return
    snippets.value = snips || []
  } catch (e) {
    console.error('[CmdPalette] SearchSnippets:', getErrorMessage(e))
    if (gen === itemsLoadGen) loading.value = false
  } finally {
    if (gen === itemsLoadGen) loading.value = false
  }
}

// ---- 窗口打开 ----
onMounted(async () => {
  // 每次打开面板时清空搜索框
  query.value = ''
  selectedIndex.value = 0
  inlineQuicklink.value = null
  inlineQuery.value = ''
  await loadMostUsedItems()
  try {
    const copied = unwrap<string>(await GetLastCopiedText())
    if (copied && copied.trim() && copied.trim().length < 200) {
      query.value = copied.trim()
      // 如果有预填文本，直接用后端搜索
      searchItems(query.value.trim())
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

// 用户输入时触发后端搜索（100ms 防抖）
let searchTimer: ReturnType<typeof setTimeout> | null = null
watch(query, (val) => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    searchItems(val.trim())
  }, 100)
})

// 切换到内联模式时清空选中
watch(inlineQuicklink, (v) => {
  if (v) selectedIndex.value = 0
})

// 组件卸载时清理定时器
onUnmounted(() => {
  if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
  <div class="palette-root" @click.self="closePalette">
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

.result-app-icon {
  width: 18px;
  height: 18px;
  object-fit: contain;
  border-radius: 2px;
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
