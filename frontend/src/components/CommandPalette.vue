<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Search, Hash, Command, ArrowRight, Lock, Power, Moon, Trash2, RotateCcw,
  Link, Clipboard, Folder, Globe, Terminal, FileText, AppWindow, CornerDownLeft, ChevronUp, ChevronDown, ChevronLeft, X,
  MessageCircle, Code2, FolderOpen, Calculator, FileEdit, Server, Container, Palette, Music, Settings, Activity, Image, Camera, Puzzle, ExternalLink
} from '@lucide/vue'
import { SearchAll, ExecuteSystemCommand, OpenItem, HidePaletteWindow, SearchSnippets, PasteSnippet, GetLastCopiedText, GetMostUsedItems, ScanInstalledApps, LaunchInstalledApp, ListPlugins, ExecutePluginCommand, GetPluginFrontendPage, SetPendingPluginInit, GetAndClearPendingPluginInit, ShowPluginWindow, RecordUsage, RecordUsageEx, GetRecentUsage, GetAllUsage } from '../../bindings/quickdock/services/appservice'
import { Events, Browser } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { CollectionItem, PluginInfo, PluginCommand } from '../types'
import type { ToastAPI } from '../types'
import { evaluate, format } from '../utils/calc'
import { pinyin } from 'pinyin-pro'
const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')

// ---- 关闭面板（清空搜索框和结果缓存）----
function closePalette() {
  closeInlinePlugin()
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
type ResultType = 'item' | 'system' | 'quicklink' | 'quicklink-inline' | 'calculator' | 'snippet' | 'app' | 'plugin' | 'url'

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
  score?: number                 // 匹配质量分数 0-100（用于跨组排序）
  matchType?: string             // 匹配类型标签（"精确" "正则" "拼音" "模糊" 等）
  url?: string                   // URL 直接打开
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

// ---- Frecency（后端 SQLite 存储，跨窗口共享）----
interface FrecencyEntry { count: number; lastUsed: number }

// 内存缓存 — 启动时从后端 GetAllUsage 一次性加载
let frecencyCache: Record<string, FrecencyEntry> = {}
const frecencyTick = ref(0) // 递增以触发 computed 重新计算

async function loadFrecency(): Promise<void> {
  if (frecencyTick.value > 0) return // 已经加载过
  try {
    const raw = await GetAllUsage()
    const entries = unwrap<{ key: string; count: number; lastUsed: number }[]>(raw)
    frecencyCache = {}
    if (entries) {
      for (const e of entries) {
        frecencyCache[e.key] = { count: e.count, lastUsed: e.lastUsed }
      }
    }
  } catch {
    frecencyCache = {}
  }
  frecencyTick.value++ // 触发所有依赖 frecency 的 computed 重算
}

function recordUsage(key: string, type_?: string, label?: string, desc?: string) {
  // 立即更新本地缓存（乐观更新）
  const now = Date.now()
  frecencyCache[key] = { count: (frecencyCache[key]?.count || 0) + 1, lastUsed: now }
  // 异步写后端（跨窗口共享），同时存储展示信息
  if (type_) {
    try { RecordUsageEx(key, type_, label || '', desc || '').catch(e => console.warn('[CmdPalette] RecordUsageEx:', e)) } catch {}
  } else {
    try { RecordUsage(key).catch(e => console.warn('[CmdPalette] RecordUsage:', e)) } catch {}
  }
}

function frecencyScore(key: string): number {
  if (frecencyTick.value === 0) return 0
  const entry = frecencyCache[key]
  if (!entry) return 0
  const now = Date.now()
  const recencyDays = (now - entry.lastUsed) / 86400000
  return entry.count * 10 + Math.max(0, 30 - recencyDays)
}

// ---- 插件命令预编译索引（缓存 regex + 拼音，避免每次击键重复计算）----
interface PluginCmdIndex {
  plugin: PluginInfo
  cmd: PluginCommand
  regex: RegExp | null        // 预编译 matchPattern
  regexValid: boolean         // 正则是否有效
  pinyinTitleFull: string     // 预计算标题全拼
  pinyinTitleInit: string     // 预计算标题首字母
  pinyinKwFull: string[]      // 预计算各关键字全拼
  pinyinKwInit: string[]      // 预计算各关键字首字母
  pinyinAliasFull: string[]   // 预计算各别名全拼
  pinyinAliasInit: string[]   // 预计算各别名首字母
}
let pluginCmdIndex = ref<PluginCmdIndex[]>([])

function buildPluginIndex(plugins: PluginInfo[]): PluginCmdIndex[] {
  const idx: PluginCmdIndex[] = []
  const seen = new Set<string>()
  for (const plugin of plugins) {
    if (plugin.status !== 'running') continue
    for (const cmd of plugin.commands) {
      const key = plugin.id + '|' + cmd.id
      if (seen.has(key)) continue
      seen.add(key)
      let regex: RegExp | null = null
      let regexValid = true
      if (cmd.matchPattern) {
        try {
          regex = new RegExp(cmd.matchPattern)
        } catch (e) {
          console.warn(`[CmdPalette] Invalid matchPattern in plugin "${plugin.id}" cmd "${cmd.id}":`, (e as Error).message)
          regexValid = false
        }
      }
      // 预计算拼音
      const titlePy = pinyin(cmd.title, { toneType: 'none', type: 'array' })
      const pinyinTitleFull = titlePy.join('').toLowerCase()
      const pinyinTitleInit = titlePy.map(p => p[0]).join('').toLowerCase()
      const pinyinKwFull: string[] = []
      const pinyinKwInit: string[] = []
      for (const kw of (cmd.keywords || [])) {
        const kwPy = pinyin(kw, { toneType: 'none', type: 'array' })
        pinyinKwFull.push(kwPy.join('').toLowerCase())
        pinyinKwInit.push(kwPy.map(p => p[0]).join('').toLowerCase())
      }
      // 预计算别名拼音
      const pinyinAliasFull: string[] = []
      const pinyinAliasInit: string[] = []
      for (const alias of (cmd.aliases || [])) {
        const aPy = pinyin(alias, { toneType: 'none', type: 'array' })
        pinyinAliasFull.push(aPy.join('').toLowerCase())
        pinyinAliasInit.push(aPy.map(p => p[0]).join('').toLowerCase())
      }
      idx.push({ plugin, cmd, regex, regexValid, pinyinTitleFull, pinyinTitleInit, pinyinKwFull, pinyinKwInit, pinyinAliasFull, pinyinAliasInit })
    }
  }
  return idx
}

// ---- Levenshtein 编辑距离（用于模糊匹配）----
function levenshtein(a: string, b: string): number {
  const alen = a.length, blen = b.length
  if (alen === 0) return blen
  if (blen === 0) return alen
  // 只比较前 min(alen, 20) 个字符，避免大矩阵
  const limit = Math.min(Math.max(alen, blen), 20)
  const s1 = a.slice(0, limit), s2 = b.slice(0, limit)
  const m = s1.length, n = s2.length
  const dp: number[] = Array(n + 1).fill(0).map((_, i) => i)
  for (let i = 1; i <= m; i++) {
    let prev = i
    for (let j = 1; j <= n; j++) {
      const tmp = dp[j]
      dp[j] = s1[i - 1] === s2[j - 1] ? prev : 1 + Math.min(prev, dp[j], dp[j - 1])
      prev = tmp
    }
  }
  return dp[n]
}

// ---- 插件命令加权评分 ----
function calcPluginScore(
  idx: PluginCmdIndex, q: string, qLC: string
): { score: number; matchType: string; inlineInput?: string } {
  let bestScore = 0
  let bestType = ''
  let inlineInput: string | undefined

  if (!q) return { score: 0, matchType: '' }

  // 1) Slash 命令前缀精确匹配 (最高优先级)
  if (idx.cmd.prefix && qLC.startsWith(idx.cmd.prefix.toLowerCase())) {
    bestScore = 95; bestType = 'slash'
    inlineInput = q.slice(idx.cmd.prefix.length).trim() || undefined
  }

  // 2) matchPattern 命中
  if (idx.regex && idx.regexValid) {
    try {
      if (idx.regex.test(q)) {
        if (idx.cmd.prefix && qLC.startsWith(idx.cmd.prefix.toLowerCase())) {
          // slash + matchPattern 一起用时不重复加分
        } else if (bestScore < 85) {
          bestScore = 85; bestType = 'match pattern'; inlineInput = q
        }
      }
    } catch {}
  }

  const titleLC = idx.cmd.title.toLowerCase()

  // 3) 标题精确匹配
  if (titleLC === qLC && bestScore < 100) { bestScore = 100; bestType = 'exact' }

  // 4) 关键字精确匹配
  if ((idx.cmd.keywords || []).some(k => k.toLowerCase() === qLC) && bestScore < 90) { bestScore = 90; bestType = 'keyword' }

  // 5) 别名精确匹配
  if ((idx.cmd.aliases || []).some(a => a.toLowerCase() === qLC) && bestScore < 85) { bestScore = 85; bestType = 'alias' }

  // 6) 标题前缀匹配
  if (titleLC.startsWith(qLC) && bestScore < 75) { bestScore = 75; bestType = 'prefix' }

  // 7) 关键字前缀 (输入以 "关键字 " 开头，自动传参)
  if ((idx.cmd.keywords || []).some(k => qLC.startsWith(k.toLowerCase() + ' ')) && bestScore < 55) { bestScore = 55; bestType = 'kw inline' }

  // 8) 标题包含
  if (titleLC.includes(qLC) && bestScore < 60) { bestScore = 60; bestType = 'contains' }

  // 9) 关键字包含
  if ((idx.cmd.keywords || []).slice(0, 20).some(k => k.toLowerCase().includes(qLC)) && bestScore < 40) { bestScore = 40; bestType = 'kw match' }

  // 10) 别名包含
  if ((idx.cmd.aliases || []).some(a => a.toLowerCase().includes(qLC)) && bestScore < 50) { bestScore = 50; bestType = 'alias' }

  // 11) 插件名包含
  if (idx.plugin.name.toLowerCase().includes(qLC) && bestScore < 35) { bestScore = 35; bestType = 'plugin' }

  // 12) 插件描述包含
  if ((idx.plugin.description || '').toLowerCase().includes(qLC) && bestScore < 35) { bestScore = 35; bestType = 'desc' }

  // 13) ID 包含
  if (idx.cmd.id.toLowerCase().includes(qLC) && bestScore < 30) { bestScore = 30; bestType = 'id' }

  // 14) 拼音匹配标题 (用缓存)
  if ((idx.pinyinTitleFull.includes(qLC) || idx.pinyinTitleInit.includes(qLC)) && bestScore < 20) { bestScore = 20; bestType = 'pinyin' }

  // 15) 拼音匹配关键字
  if (bestScore < 15) {
    for (let i = 0; i < idx.pinyinKwFull.length; i++) {
      if (idx.pinyinKwFull[i].includes(qLC) || idx.pinyinKwInit[i].includes(qLC)) {
        bestScore = Math.max(bestScore, 15); bestType = 'kw pinyin'; break
      }
    }
  }

  // 16) 别名拼音（用缓存）
  if (bestScore < 12) {
    if (idx.pinyinAliasFull.some(p => p.includes(qLC)) || idx.pinyinAliasInit.some(p => p.includes(qLC))) {
      bestScore = Math.max(bestScore, 12); bestType = 'alias py'
    }
  }

  // 17) 模糊匹配 (编辑距离，仅当没有其他匹配且查询 ≥ 3 字符)
  if (bestScore === 0 && qLC.length >= 3) {
    // 中文查询：每个汉字权重高，放宽阈值到 3
    const hasChinese = /[\u4e00-\u9fff]/.test(qLC)
    const threshold = hasChinese ? 3 : 2
    const qLen = qLC.length
    if (levenshtein(titleLC.slice(0, Math.max(titleLC.length, qLen)), qLC) <= threshold) { bestScore = 10; bestType = 'fuzzy' }
    else if ((idx.cmd.keywords || []).some(k => levenshtein(k.toLowerCase().slice(0, Math.max(k.length, qLen)), qLC) <= threshold)) { bestScore = 8; bestType = 'fuzzy' }
  }

  return { score: bestScore, matchType: bestType, inlineInput }
}

// 匹配类型 → 中文标签
const matchTypeLabels: Record<string, string> = {
  'exact':       '精确',
  'keyword':     '关键字',
  'alias':       '别名',
  'prefix':      '前缀',
  'contains':    '包含',
  'fuzzy':       '模糊',
  'match pattern': '正则',
  'kw inline':   '内联',
  'kw match':    '关键字',
  'kw pinyin':   '拼音',
  'alias py':    '拼音',
  'pinyin':      '拼音',
  'plugin':      '插件',
  'desc':        '描述',
  'id':          'ID',
  'slash':       '命令',
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

// Inline 插件模式：在同一个面板内展示插件内容（uTools 风格）
const inlinePluginId = ref<string | null>(null)
const inlinePluginHtml = ref('')
const inlinePluginLoading = ref(false)
const inlinePluginError = ref('')
const inlinePluginName = ref('')
const inlinePluginIframe = ref<HTMLIFrameElement | null>(null)
let inlinePluginMsgHandler: ((e: MessageEvent) => void) | null = null

function closeInlinePlugin() {
  inlinePluginId.value = null
  inlinePluginHtml.value = ''
  inlinePluginLoading.value = false
  inlinePluginError.value = ''
  inlinePluginName.value = ''
  // 清理消息监听
  if (inlinePluginMsgHandler) {
    window.removeEventListener('message', inlinePluginMsgHandler)
    inlinePluginMsgHandler = null
  }
  // 聚焦输入框
  nextTick(() => inputRef.value?.focus())
}

// 分离为独立窗口：弹出到任务栏可见的独立窗口，仅能通过 X 关闭
async function detachPlugin() {
  if (!inlinePluginId.value) return
  const id = inlinePluginId.value
  try {
    await ShowPluginWindow(id)
  } catch (e) {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
    return
  }
  // 分离后回到搜索模式
  closeInlinePlugin()
}

// 内联插件 iframe 加载完成时：传递初始文本
async function onInlinePluginLoad() {
  const iframe = inlinePluginIframe.value
  if (!iframe?.contentWindow) return
  inlinePluginMsgHandler = (event: MessageEvent) => {
    if (event.source !== iframe.contentWindow) return
    if (event.data?.type === 'plugin:execute') {
      const { id, command, input } = event.data
      if (!inlinePluginId.value) return
      ExecutePluginCommand(inlinePluginId.value, command, input || null).then(raw => {
        const result = unwrap(raw)
        if (event.source) {
          ;(event.source as any).postMessage(
            { type: 'plugin:result', id, data: result },
            window.location.origin
          )
        }
      }).catch(e => {
        if (event.source) {
          ;(event.source as any).postMessage(
            { type: 'plugin:result', id, error: e?.message || String(e) },
            window.location.origin
          )
        }
      })
    }
  }
  window.addEventListener('message', inlinePluginMsgHandler)

  // 传递初始文本 + 主题 + 语言
  try {
    const raw = await GetAndClearPendingPluginInit()
    const text = raw?.data || raw
    if (iframe.contentWindow) {
      // 先发 theme 消息让插件 HTML 应用主题
      iframe.contentWindow.postMessage({ type: 'plugin:theme', data: { theme: 'dark', locale: locale.value } }, window.location.origin)
      // 再发 init 消息（携带 text 和主题/语言）
      iframe.contentWindow.postMessage({
        type: 'plugin:init',
        data: { text, theme: 'dark', locale: locale.value }
      }, window.location.origin)
    }
  } catch {}
}

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
  void frecencyTick.value // 依赖 frecencyTick，加载完毕后重算
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

  // 2. URL 检测：检测到网页链接时添加直接打开选项
  var isUrl = false
  var urlStr = q
  if (/^https?:\/\//i.test(q)) {
    isUrl = true
  } else if (/^[a-z0-9][-a-z0-9]*\.[a-z]{2,}(\/|$)/i.test(q) && !/^[\d+\-*/().%^, ]+$/.test(q)) {
    isUrl = true
    urlStr = 'https://' + q
  }
  if (isUrl) {
    groups.push({
      type: 'url',
      label: '🌐 ' + t('cmdGroupWeb'),
      results: [{
        type: 'url',
        label: t('cmdOpenUrl'),
        desc: urlStr,
        icon: Globe,
        url: urlStr,
      }]
    })
  }

  // 3. 项目 + Quicklink — items.value 已由后端 FTS5 筛选
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

  // 7. 插件命令 — 加权评分 + 预编译索引 + 模糊匹配
  const pluginResults: SearchResult[] = []
  for (const idx of pluginCmdIndex.value) {
    const { score, matchType, inlineInput } = calcPluginScore(idx, q, qLC)
    if (score <= 0) continue

    pluginResults.push({
      type: 'plugin',
      label: inlineInput ? `${idx.cmd.title}: ${inlineInput}` : idx.cmd.title,
      desc: idx.plugin.name + (idx.cmd.hotkey ? `  ${idx.cmd.hotkey}` : ''),
      icon: Puzzle,
      pluginId: idx.plugin.id,
      pluginCommandId: idx.cmd.id,
      pluginHasFrontend: idx.plugin.hasFrontend,
      inlineInput,
      score,
      matchType,
      frecencyScore: frecencyScore('plugin:' + idx.plugin.id + '.' + idx.cmd.id),
    })
  }
  // 加上之前执行的结果缓存（始终显示在插件分组顶部）
  if (pluginResultCache.value) {
    pluginResults.unshift({
      type: 'plugin',
      label: pluginResultCache.value.result,
      desc: '\u2190 ' + pluginResultCache.value.pluginName,
      icon: Puzzle,
      score: 99,
      frecencyScore: 9999, // 置顶
    })
  }
  // 按统一分数排序
  pluginResults.sort((a, b) => {
    const sa = (a.score || 0) * 0.7 + Math.min(30, a.frecencyScore || 0) * 0.3
    const sb = (b.score || 0) * 0.7 + Math.min(30, b.frecencyScore || 0) * 0.3
    return sb - sa
  })
  if (pluginResults.length > 0) {
    groups.push({ type: 'plugin', label: t('cmdGroupPlugins'), results: pluginResults })
  }

  // ---- 跨组最佳匹配注入 (#8, #14) ----
  // 从所有分组中收集 score >= 70 的结果，注入顶部 "最佳匹配" 分组
  const bestMatch: SearchResult[] = []
  for (const g of groups) {
    if (g.type === 'calculator') continue // 计算器单结果不抢
    for (const r of g.results) {
      if ((r.score || 0) >= 70) bestMatch.push(r)
    }
  }
  if (bestMatch.length >= 1) {
    bestMatch.sort((a, b) => (b.score || 0) - (a.score || 0))
    // 最多展示 6 个
    const show = bestMatch.slice(0, 6)
    // 生成带类型前缀的去重键，避免 pluginId 与 item.id 冲突
    function dedupKey(r: SearchResult): string {
      if (r.pluginId && r.pluginCommandId) return 'plugin:' + r.pluginId + '.' + r.pluginCommandId
      if (r.item?.id) return 'item:' + r.item.id
      if (r.snippet?.id) return 'snippet:' + r.snippet.id
      if (r.cmd?.id) return 'cmd:' + r.cmd.id
      if (r.appPath) return 'app:' + r.appPath
      return r.label
    }
    const dedupIds = new Set(show.map(dedupKey))
    for (const g of groups) {
      if (g.type === 'plugin' || g.type === 'item') {
        g.results = g.results.filter(r => !dedupIds.has(dedupKey(r)))
      }
    }
    groups.unshift({ type: 'item', label: t('cmdBestMatch'), results: show })
  }

  return groups
})

// 扁平化结果列表（用于键盘导航）
const allResults = computed<SearchResult[]>(() => {
  return groupedResults.value.flatMap(g => g.results)
})

// ---- 空状态：最近使用 ----
// 后端直接返回最近使用的记录（含 type/label/desc/count），前端仅做对象引用解析
interface RecentEntry {
  key: string; type: string; label: string; description: string; count: number; lastUsed: number
}

const recentResults = computed<SearchResult[]>(() => {
  if (query.value.trim()) return []
  // 从后端获取最近使用的记录
  const raw = recentCache.value
  if (!raw || raw.length === 0) return []

  // 构建查询表
  const itemByKey: Record<string, CollectionItem> = {}
  for (const it of items.value) itemByKey['item:' + it.id] = it
  const snippetByKey: Record<string, CmdSnippet> = {}
  for (const s of snippets.value) snippetByKey['snippet:' + s.id] = s
  const cmdByKey: Record<string, SystemCmd> = {}
  for (const c of systemCommands.value) cmdByKey['system:' + c.id] = c

  const results: SearchResult[] = []
  // 已经有 icon 映射的函数可用
  for (const entry of raw) {
    if (entry.type === 'item' || entry.type === 'quicklink') {
      const item = itemByKey[entry.key]
      if (!item) continue
      results.push({
        type: item.value?.includes('{query}') ? 'quicklink' : 'item',
        label: entry.label,
        desc: entry.description,
        icon: itemIcon(item),
        item,
        frecencyScore: entry.count,
      })
    } else if (entry.type === 'snippet') {
      const snippet = snippetByKey[entry.key]
      if (!snippet) continue
      results.push({
        type: 'snippet',
        label: entry.label,
        desc: entry.description,
        icon: Clipboard,
        snippet,
        frecencyScore: entry.count,
      })
    } else if (entry.type === 'plugin') {
      const keyWithoutPrefix = entry.key.replace(/^plugin:/, '')
      // pluginCmdIndex 里每个插件的完整 id（如 com.quickdock.calcsheet）已知，遍历匹配
      let idx: PluginCmdIndex | undefined
      let matchedPluginId = ''
      let matchedCmdId = ''
      for (const cand of pluginCmdIndex.value) {
        const prefix = cand.plugin.id + '.'
        if (keyWithoutPrefix.startsWith(prefix)) {
          const cid = keyWithoutPrefix.slice(prefix.length)
          if (cid === cand.cmd.id) {
            idx = cand; matchedPluginId = cand.plugin.id; matchedCmdId = cid; break
          }
        }
      }
      if (!idx) continue
      results.push({
        type: 'plugin',
        label: entry.label,
        desc: entry.description,
        icon: Puzzle,
        pluginId: matchedPluginId,
        pluginCommandId: matchedCmdId,
        pluginHasFrontend: idx.plugin.hasFrontend,
        frecencyScore: entry.count,
      })
    } else if (entry.type === 'system') {
      const cmd = cmdByKey[entry.key.replace(/^system:/, '')]
      if (!cmd) continue
      results.push({
        type: 'system',
        label: entry.label,
        desc: entry.description,
        icon: cmd.icon,
        cmd,
        frecencyScore: entry.count,
      })
    } else if (entry.type === 'app') {
      results.push({
        type: 'app',
        label: entry.label,
        desc: entry.description,
        icon: AppWindow,
        appPath: entry.key.replace(/^app:/, ''),
        frecencyScore: entry.count,
      })
    }
  }
  return results.slice(0, 8)
})

// 最近使用缓存（由 loadMostUsedItems 填充）
const recentCache = ref<RecentEntry[]>([])

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
      if (inlinePluginId.value) {
        // 在插件内联模式中：按 Esc 回到搜索模式
        closeInlinePlugin()
      } else {
        closePalette()
      }
      break
  }
}

// ---- 执行选中项 ----
async function executeSelected() {
  const result = displayFlat.value[selectedIndex.value]
  if (!result) return

  if (result.type === 'system' && result.cmd) {
    recordUsage('system:' + result.cmd.id, 'system', result.label, result.desc)
    await result.cmd.action()
  } else if (result.type === 'quicklink-inline' && result.item) {
    const item = { ...result.item }
    let value = item.value || ''
    if (result.inlineQuery) {
      value = value.replace(/\{query\}/g, result.inlineQuery)
    }
    item.value = value
    recordUsage('item:' + item.id, 'item', item.name, item.value || '')
    try { await OpenItem(item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
    closePalette()
  } else if (result.type === 'quicklink' && result.item) {
    // 进入内联输入模式
    inlineQuicklink.value = result.item
    inlineQuery.value = ''
    await nextTick()
    inlineInputRef.value?.focus()
  } else if (result.type === 'item' && result.item) {
    recordUsage('item:' + result.item.id, 'item', result.label, result.desc)
    try { await OpenItem(result.item as any) } catch (e) { console.error('[CmdPalette] OpenItem:', e) }
    closePalette()
  } else if (result.type === 'url' && result.url) {
    try { await Browser.OpenURL(result.url) } catch (e) { console.error('[CmdPalette] OpenURL:', e) }
    closePalette()
  } else if (result.type === 'calculator' && result.calcResult) {
    try { await navigator.clipboard.writeText(result.calcResult) } catch {}
    closePalette()
  } else if (result.type === 'snippet' && result.snippet) {
    recordUsage('snippet:' + result.snippet.id, 'snippet', result.label, result.desc)
    try { await PasteSnippet(result.snippet.content) } catch (e) { console.error('[CmdPalette] PasteSnippet:', e) }
    closePalette()
  } else if (result.type === 'app' && result.appPath) {
    recordUsage('app:' + result.label, 'app', result.label, result.desc)
    try { await LaunchInstalledApp(result.appPath) } catch (e) { console.error('[CmdPalette] LaunchInstalledApp:', e) }
    closePalette()
    } else if (result.type === 'plugin' && result.pluginId && result.pluginCommandId) {
    recordUsage('plugin:' + result.pluginId + '.' + result.pluginCommandId, 'plugin', result.label, result.desc)
    try {
      // 仅当 matchPattern/slash 前缀命中时才传输入文本
      // 纯关键字/别名/包含等匹配时不传
      const matchType = result.matchType
      const inputText = (matchType === 'match pattern' || matchType === 'slash') ? result.inlineInput : undefined
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
        // 如果插件有前端（将被内联展示），不弹 Toast 打扰用户
        if (!result.pluginHasFrontend) {
          toast?.success?.(t('pluginResultCopied'))
        }
      }

      // 如果有前端页面且有输入文本，为 iframe 准备初始值
      if ((result.pluginHasFrontend || result.pluginId) && inputText) {
        try { await SetPendingPluginInit(inputText) } catch {}
      }

      // 如果有前端页面，在面板内联展示（uTools 风格）
      if (result.pluginHasFrontend || result.pluginId) {
        inlinePluginId.value = result.pluginId
        inlinePluginLoading.value = true
        inlinePluginError.value = ''
        try {
          const html = unwrap<string>(await GetPluginFrontendPage(result.pluginId))
          if (html) {
            // 从 HTML 中提取 title
            const titleMatch = html.match(/<title>([^<]*)<\/title>/)
            inlinePluginName.value = titleMatch ? titleMatch[1] : result.label
            inlinePluginHtml.value = html
          } else {
            inlinePluginError.value = t('pluginNoFrontend')
          }
        } catch (e: any) {
          inlinePluginError.value = t('pluginLoadFailed') + ': ' + getErrorMessage(e)
        }
        inlinePluginLoading.value = false
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
  recordUsage('item:' + item.id, 'item', item.name, item.value || '')
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
  // 预加载 frecency 数据（后端存储，跨窗口共享）
  await loadFrecency()
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
    if (gen === itemsLoadGen) {
      installedPlugins.value = plugins?.filter(p => p.status === 'running') || []
      pluginCmdIndex.value = buildPluginIndex(installedPlugins.value) // 构建预编译索引
    }
  } catch (e) {
    console.error('[CmdPalette] ListPlugins:', getErrorMessage(e))
  } finally {
    // 加载最近使用记录（后端按 last_used DESC 排序）
    try {
      const raw = await GetRecentUsage(20)
      if (gen === itemsLoadGen) {
        recentCache.value = (unwrap<RecentEntry[]>(raw) || []).filter(e => e.type && e.label)
      }
    } catch (e) {
      console.error('[CmdPalette] GetRecentUsage:', getErrorMessage(e))
    }
    if (gen === itemsLoadGen) loading.value = false
  }
}

// 用户输入搜索词时 → 后端 FTS5 搜索 + 刷新插件列表
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
  }
  // 刷新插件列表（面板打开期间插件可能启动/崩溃/禁用）(#1)
  try {
    const plugins = unwrap<PluginInfo[]>(await ListPlugins())
    if (gen === itemsLoadGen) {
      installedPlugins.value = plugins?.filter(p => p.status === 'running') || []
      pluginCmdIndex.value = buildPluginIndex(installedPlugins.value) // 重建索引
    }
  } catch (e) {
    console.error('[CmdPalette] ListPlugins(refresh):', getErrorMessage(e))
  } finally {
    if (gen === itemsLoadGen) loading.value = false
  }
}

// ---- 窗口打开 ----
let lastClipboardUpdate = 0

onMounted(async () => {
  // 监听剪贴板更新事件，记录时间戳
  Events.On('clipboard:updated', () => {
    lastClipboardUpdate = Date.now()
  })

  await loadMostUsedItems()

  // 每次窗口打开时：如果在 inline 插件模式则保持，否则重置搜索状态
  Events.On('palette:shown', () => {
    if (inlinePluginId.value) {
      // Inline 插件模式：保持当前插件显示，只聚焦
      setTimeout(() => inputRef.value?.focus(), 50)
      return
    }

    query.value = ''
    selectedIndex.value = 0
    inlineQuicklink.value = null
    inlineQuery.value = ''
    pluginResultCache.value = null

    // 仅当 3 秒内有新复制的内容才自动填充
    if (Date.now() - lastClipboardUpdate < 3000) {
      GetLastCopiedText().then(raw => {
        const copied = unwrap<string>(raw)
        if (copied && copied.trim() && copied.trim().length < 200) {
          query.value = copied.trim()
          searchItems(query.value.trim())
        }
      }).catch(() => {})
    }

    setTimeout(() => {
      inputRef.value?.focus()
      inputRef.value?.select()
    }, 50)
  })
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
  Events.Off('palette:shown')
  Events.Off('clipboard:updated')
})
</script>

<template>
  <!-- Inline 插件模式：面板内展示插件内容（uTools 风格） -->
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
          :key="group.type + '-' + (result.item?.id || result.cmd?.id || result.snippet?.id || result.url || iIdx)"
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
            <template v-else-if="result.type === 'plugin' && result.matchType">
              <span class="meta-tag">{{ matchTypeLabels[result.matchType!] || result.matchType }}</span>
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

/* ===== Inline 插件模式（uTools 风格） ===== */

.palette-plugin-mode {
  background: var(--color-bg-primary);
}

/* 标题栏：精准的层级感，shadow-border 替代 solid border */
.palette-plugin-header {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 36px;
  flex-shrink: 0;
  padding: 0 6px;
  background: var(--color-bg-secondary);
  box-shadow: inset 0 -1px 0 0 var(--color-border);
  -webkit-app-region: drag;
  user-select: none;
}

/* 返回按钮：紧凑、清晰 */
.plugin-back-btn {
  display: flex;
  align-items: center;
  gap: 2px;
  height: 28px;
  padding: 0 6px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--color-text-muted);
  font-size: 12px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  -webkit-app-region: no-drag;
  transition: background 0.1s, color 0.1s;
}
.plugin-back-btn:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.plugin-back-btn:active {
  background: var(--color-bg-active);
}
.plugin-back-btn svg {
  width: 15px;
  height: 15px;
}

/* 插件名称：居中，次级强调 */
.plugin-title {
  flex: 1;
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  text-align: center;
  letter-spacing: 0.02em;
}

/* 右上角操作区 */
.plugin-header-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
}

/* 分离按钮：安静存在，悬停显现 */
.plugin-detach-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--color-text-disabled);
  cursor: pointer;
  -webkit-app-region: no-drag;
  transition: background 0.12s, color 0.12s;
}
.plugin-detach-btn:hover {
  background: var(--color-bg-hover);
  color: var(--color-accent);
}
.plugin-detach-btn:active {
  background: var(--color-bg-active);
}

/* 内容区：占满剩余空间 */
.palette-plugin-body {
  flex: 1;
  display: flex;
  overflow: hidden;
}

/* 加载/错误状态 */
.palette-plugin-status {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  color: var(--color-text-disabled);
  user-select: none;
}
.palette-plugin-error {
  color: var(--color-danger);
  padding: 0 24px;
  text-align: center;
  line-height: 1.6;
}

/* iframe：干净无边框 */
.palette-plugin-iframe {
  flex: 1;
  width: 100%;
  height: 100%;
  border: none;
  background: var(--color-bg-primary);
}
</style>
