<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Search, Hash, Command, ArrowRight, Lock, Power, Moon, Trash2, RotateCcw,
  Link, Clipboard, Folder, Globe, Terminal, FileText, AppWindow, CornerDownLeft, ChevronUp, ChevronDown, ChevronLeft, ChevronRight, X,
  MessageCircle, Code2, FolderOpen, Calculator, FileEdit, Server, Container, Palette, Music, Settings, Activity, Image, Camera, Puzzle, ExternalLink,
  Check, Bookmark, PanelLeft, PanelRight, Volume2, VolumeX, Volume1, Wifi, WifiOff, XCircle,
  Copy, FolderSearch, Play, ClipboardPaste, Save
} from '@lucide/vue'
import { ListAllItems, ExecuteSystemCommand, OpenItem, HidePaletteWindow, ListSnippets, PasteSnippet, GetLastCopiedText, ScanInstalledApps, LaunchInstalledApp, ListPlugins, ExecutePluginCommand, SetPendingPluginInit, ShowPluginWindow, GetAllUsage, SaveUrlAsItem, CopyText, GetPluginIcon, DeleteItem, DeleteSnippet, RevealInExplorer, EnablePlugin, GetPathQuickInfo, GetPluginNewFlags, MarkPluginSeen } from '../../bindings/quickdock/services/appservice'
import { Events, Browser } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { CollectionItem, PluginInfo } from '../types'
import type { ToastAPI } from '../types'
import { evaluate, format, convertExpression } from '../utils/calc'
import { commandTitle } from '../utils/localize'
import { getPluginLastResult, savePluginLastResult, pluginCmdKey } from '../utils/pluginLastResult'
import { pinyin } from 'pinyin-pro'
import { useFrecency } from '../composables/useFrecency'
import { usePluginIndex } from '../composables/usePluginIndex'
import { useCommandSearch } from '../composables/useCommandSearch'
import PluginFrame from './PluginFrame.vue'
import type { RecentEntry } from '../composables/useCommandSearch'

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')

// ---- 初始化 composables ----
const { frecencyTick, loadFrecency, recordUsage, frecencyScore } = useFrecency()
const { pluginCmdIndex, buildPluginIndex, calcPluginScore, matchTypeI18nKeys } = usePluginIndex()

// ---- 内联插件状态（统一走 PluginFrame 宿主；内联传参不走全局 pending init，避免跨消费竞态）----
const inlinePluginId = ref<string | null>(null)
const inlinePluginName = ref<string>('')
const inlinePluginInit = ref<{ text: string; command: string }>({ text: '', command: '' })

// PluginFrame 在 iframe 挂载时调用此 getter 获取 init（同窗口直接传参，不经 Go 全局槽位）
function inlinePluginInitGetter() {
  return inlinePluginInit.value
}

// 主动退出插件页（返回搜索 / 关闭），区别于「隐藏窗口再重开」的临时收起
function closeInlinePlugin() {
  inlinePluginId.value = null
  inlinePluginName.value = ''
  inlinePluginInit.value = { text: '', command: '' }
}

// 动作菜单版「分离为独立窗口」：选中列表中的前端插件命令，带最近输入/命令打开独立窗口。
// 与内联插件页头的 detach 同一机制（SetPendingPluginInit + ShowPluginWindow）。
async function detachPluginFor(r: SearchResult) {
  const pid = r.pluginId
  if (!pid) return
  markPluginSeen(pid)
  try {
    const last = r.pluginCommandId ? getPluginLastResult(pid, r.pluginCommandId) : null
    const input = r.inlineInput || last?.input || ''
    if (r.pluginCommandId) {
      try { await SetPendingPluginInit(pid, input, r.pluginCommandId) } catch {}
    }
    await ShowPluginWindow(pid)
    closePalette()
  } catch (e) {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
  }
}

// 分离为独立窗口：把当前插件的 init 交给独立窗口的全局 pending init 槽位后再打开
async function detachPlugin() {
  const id = inlinePluginId.value
  if (!id) return
  markPluginSeen(id)
  try {
    if (inlinePluginInit.value.text || inlinePluginInit.value.command) {
      try { await SetPendingPluginInit(id, inlinePluginInit.value.text, inlinePluginInit.value.command) } catch {}
    }
    await ShowPluginWindow(id)
  } catch (e) {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
    return
  }
  closeInlinePlugin()
}

// ---- 关闭面板 ----
function closePalette() {
  closeInlinePlugin()
  actionMenuOpen.value = false
  query.value = ''
  selectedIndex.value = 0
  selectedSet.value = new Set()
  lastAnchor.value = -1
  inlineQuicklink.value = null
  inlineQuery.value = ''
  clipboardUrlSource.value = ''
  quickInfo.value = null
  try { HidePaletteWindow() } catch (e) { console.error('[CmdPalette] HidePaletteWindow:', e) }
}

// 聚焦搜索输入框；仅在搜索模式（非内联插件 / 内联快速链接）下生效
function focusInput() {
  if (inlinePluginId.value || inlineQuicklink.value) return
  const el = inputRef.value
  if (el) { el.focus(); el.select() }
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
type ResultType = 'item' | 'system' | 'quicklink' | 'quicklink-inline' | 'calculator' | 'snippet' | 'app' | 'plugin' | 'url' | 'clipboard-action' | 'best'
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

// 插件「新安装/更新」角标：首次运行/打开后清除
const newFlags = ref<Set<string>>(new Set())
async function loadPluginNewFlags() {
  try {
    const flags = await GetPluginNewFlags()
    if (Array.isArray(flags)) newFlags.value = new Set(flags as string[])
  } catch {}
}
function markPluginSeen(pid: string) {
  if (!pid || !newFlags.value.has(pid)) return
  newFlags.value.delete(pid)
  MarkPluginSeen(pid).catch(() => {})
}
const loading = ref(false)
const selectedIndex = ref(0)
const listRef = ref<HTMLElement | null>(null)
const selectedSet = ref<Set<number>>(new Set())
const lastAnchor = ref(-1)
const clipboardUrlSource = ref('')
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
  { id: 'window-left', label: t('cmdWindowLeft'), desc: t('cmdWindowLeftDesc'), keywords: ['window', 'left', '左半屏', '左半', '窗口', 'ban ping', 'zuo ban ping', '系统'], icon: PanelLeft,
    action: async () => { await ExecuteSystemCommand('window-left'); closePalette() } },
  { id: 'window-right', label: t('cmdWindowRight'), desc: t('cmdWindowRightDesc'), keywords: ['window', 'right', '右半屏', '右半', '窗口', 'ban ping', 'you ban ping', '系统'], icon: PanelRight,
    action: async () => { await ExecuteSystemCommand('window-right'); closePalette() } },
  { id: 'volume-up', label: t('cmdVolumeUp'), desc: t('cmdVolumeUpDesc'), keywords: ['volume', 'up', '音量', '增大', '声音', 'yin liang', 'sheng yin', '系统'], icon: Volume2,
    action: async () => { await ExecuteSystemCommand('volume-up'); closePalette() } },
  { id: 'volume-down', label: t('cmdVolumeDown'), desc: t('cmdVolumeDownDesc'), keywords: ['volume', 'down', '音量', '减小', '声音', 'yin liang', 'sheng yin', '系统'], icon: Volume1,
    action: async () => { await ExecuteSystemCommand('volume-down'); closePalette() } },
  { id: 'volume-mute', label: t('cmdVolumeMute'), desc: t('cmdVolumeMuteDesc'), keywords: ['volume', 'mute', '音量', '静音', '声音', 'jing yin', '系统'], icon: VolumeX,
    action: async () => { await ExecuteSystemCommand('volume-mute'); closePalette() } },
  { id: 'wifi-toggle', label: t('cmdWifiToggle'), desc: t('cmdWifiToggleDesc'), keywords: ['wifi', '无线', '网络', '开关', 'wang luo', 'kai guan', '系统'], icon: Wifi,
    action: async () => { await ExecuteSystemCommand('wifi-toggle'); closePalette() } },
  { id: 'kill-foreground', label: t('cmdKillForeground'), desc: t('cmdKillForegroundDesc'), keywords: ['kill', 'process', '结束', '进程', '终止', 'jin cheng', 'zhong zhi', '系统'], icon: XCircle,
    action: async () => { await ExecuteSystemCommand('kill-foreground'); closePalette() } },
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
  previewResult, recentCache, RECENT_VISIBLE, recentExpanded, toggleRecentExpanded
} = useCommandSearch({
  items, installedApps, snippets, systemCommands, query, selectedIndex,
  pluginCmdIndex, clipboardUrlSource,
  frecencyScore, frecencyTick, calcPluginScore,
  pinyinMatch, appIcon, getAppAliases, itemIcon, t, pluginIcons,
})

// ---- QuickLook 详情预览（选中即自动展示，无需按键）----
interface PathQuickInfo { exists: boolean; isDir?: boolean; path?: string; sizeText?: string; modified?: string }
const quickInfo = ref<PathQuickInfo | null>(null)
let quickInfoTimer: ReturnType<typeof setTimeout> | null = null

// 拉取文件/目录元数据（本地 stat，防抖 200ms 防切项风暴）
async function loadQuickInfo(item: CollectionItem) {
  if (quickInfoTimer) { clearTimeout(quickInfoTimer); quickInfoTimer = null }
  if (item.type !== '文件' && item.type !== '目录') { quickInfo.value = null; return }
  quickInfoTimer = setTimeout(async () => {
    quickInfoTimer = null
    try {
      const res = unwrap<PathQuickInfo | null>(await GetPathQuickInfo(item.value || ''))
      quickInfo.value = res || null
    } catch { quickInfo.value = null }
  }, 200)
}

// 选中项变化：跟随刷新元数据
function refreshQuickInfoForSelection() {
  const r = displayFlat.value[selectedIndex.value]
  const item = r?.type === 'item' ? r.item : null
  if (item) loadQuickInfo(item)
  else quickInfo.value = null
}
watch(selectedIndex, refreshQuickInfoForSelection)
watch(displayFlat, () => { if (previewResult.value) refreshQuickInfoForSelection() })

// 预览正文：基础行 + 详情行（文件元数据 / 命令工作目录）
const previewLines = computed(() => {
  const base = previewResult.value?.lines || []
  const r = displayFlat.value[selectedIndex.value]
  if (r?.type !== 'item' || !r.item) return base
  const it = r.item
  const extra: string[] = []
  if ((it.type === '文件' || it.type === '目录') && quickInfo.value) {
    const q = quickInfo.value
    if (!q.exists) extra.push('⚠ 路径不存在')
    else {
      if (!q.isDir) extra.push('大小: ' + (q.sizeText || '-'))
      extra.push('修改时间: ' + (q.modified || '-'))
      extra.push(q.isDir ? '类型: 文件夹' : '类型: 文件')
    }
  } else if (it.type === '命令') {
    if (it.workingDirectory) extra.push('工作目录: ' + it.workingDirectory)
    if (it.tool) extra.push('工具: ' + it.tool)
  }
  return [...base, ...extra]
})

// ---- 键盘导航 ----
const GRID_COLUMNS = 3 // 结果区每行卡片数（网格列数）

/**
 * 视觉网格导航：结果区是 3 列 grid（每组带跨行 header），若用线性索引 ± GRID_COLUMNS 跳转，
 * 组与组衔接处会错位——某个短分组可能永远点不中（如"na"搜索时的文本片段/系统命令）。
 * 这里按「组 header 占一行」重建每项的视觉行列坐标，↑↓ 走同列上下相邻，保证所见即所得。
 */
function moveVertical(dir: 1 | -1) {
  const flat = displayFlat.value
  const len = flat.length
  if (len <= 1) return
  const groups = displayGroups.value
  // 计算每个 flat 索引的视觉 (row, col)
  // 行语义：header 占据"当前光标行"，本组 items 从下一行开始；
  // 组内每 3 项换一行；下一组的 header 再从 items 结束行之后开始。
  const pos: { row: number; col: number }[] = []
  let row = 0
  for (const g of groups) {
    const itemsStart = row + 1 // header 占 row 行，items 从 row+1 起
    for (let i = 0; i < g.results.length; i++) {
      pos.push({ row: itemsStart + Math.floor(i / GRID_COLUMNS), col: i % GRID_COLUMNS })
    }
    row = itemsStart + Math.ceil(g.results.length / GRID_COLUMNS)
  }
  // 防御：displayGroups 与 displayFlat 时序不一致导致坐标缺失 → 回退线性导航
  if (pos.length !== len) {
    selectedIndex.value = dir === 1
      ? (selectedIndex.value + 1) % Math.max(len, 1)
      : (selectedIndex.value - 1 + len) % Math.max(len, 1)
    scrollToSelected()
    return
  }
  const cur = pos[selectedIndex.value]
  if (!cur) return
  // 语义：↓/↑ 走到"视觉下一行/上一行"（跳开组 header 行），在该行优先选同列项，
  // 同列不存在（短组没到该列）则选最近列——避免"贴着列越过大段空白跨到很远的分组"。
  let targetRow = -1
  for (let i = 0; i < len; i++) {
    if (dir === 1 && pos[i].row > cur.row && (targetRow === -1 || pos[i].row < targetRow)) targetRow = pos[i].row
    if (dir === -1 && pos[i].row < cur.row && pos[i].row > targetRow) targetRow = pos[i].row
  }
  if (targetRow === -1) return // 顶部/底部边界，停住
  let best = -1
  let minColDist = Infinity
  for (let i = 0; i < len; i++) {
    if (pos[i].row !== targetRow) continue
    const cd = Math.abs(pos[i].col - cur.col)
    if (cd < minColDist) { minColDist = cd; best = i } // 同列(0)优先，其次最近列
  }
  if (best >= 0 && best !== selectedIndex.value) {
    selectedIndex.value = best
    scrollToSelected()
  }
}

function scrollToSelected() {
  nextTick(() => {
    const list = listRef.value
    if (!list) return
    const el = list.querySelector('.result-item.active') as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  })
}

function onKeydown(e: KeyboardEvent) {
  // 二级动作菜单打开时独占键盘，避免与结果列表导航冲突
  if (actionMenuOpen.value) {
    const acts = contextActions.value
    const n = Math.max(acts.length, 1)
    // 数字 1-9 直达对应动作、Home/End 跳首尾——长菜单不用纯方向键遍历
    if (/^[1-9]$/.test(e.key) && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const i = parseInt(e.key, 10) - 1
      if (i < acts.length) { e.preventDefault(); runAction(acts[i]) }
      return
    }
    if (e.key === 'Home') { e.preventDefault(); actionMenuIndex.value = 0; return }
    if (e.key === 'End') { e.preventDefault(); actionMenuIndex.value = n - 1; return }
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); actionMenuIndex.value = (actionMenuIndex.value + 1) % n; break
      case 'ArrowUp': e.preventDefault(); actionMenuIndex.value = (actionMenuIndex.value - 1 + n) % n; break
      case 'Enter':
        e.preventDefault()
        // Ctrl/Cmd+Enter：菜单开关（再次按下关闭菜单）；纯 Enter 运行选中动作
        if (e.ctrlKey || e.metaKey) { closeActionMenu(); break }
        { const a = acts[actionMenuIndex.value]; if (a) runAction(a) }
        break
      case 'Escape': e.preventDefault(); closeActionMenu(); break
      case 'k': case 'K': if (e.ctrlKey || e.metaKey) { e.preventDefault(); closeActionMenu() } break
      default:
        // 菜单期间输入框仍持有焦点：吞掉可打印字符，避免误改搜索词
        if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) e.preventDefault()
    }
    return
  }
  if (inlineQuicklink.value) {
    if (e.key === 'Enter') { e.preventDefault(); commitInlineQuicklink() }
    else if (e.key === 'Escape') { e.preventDefault(); cancelInlineQuicklink() }
    return
  }
  const list = displayFlat.value
  if (list.length === 0 && e.key !== 'Escape') return
  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault(); moveVertical(1); break
    case 'ArrowUp':
      e.preventDefault(); moveVertical(-1); break
    case 'ArrowRight':
      e.preventDefault(); selectedIndex.value = (selectedIndex.value + 1) % Math.max(list.length, 1); scrollToSelected(); break
    case 'ArrowLeft':
      e.preventDefault(); selectedIndex.value = (selectedIndex.value - 1 + list.length) % Math.max(list.length, 1); scrollToSelected(); break
    case 'Enter':
      // Ctrl/Cmd+Enter 打开选中项的「动作菜单」；纯 Enter 执行选中项
      if (e.ctrlKey || e.metaKey) { e.preventDefault(); openActionMenu() }
      else { e.preventDefault(); executeSelected() }
      break
    case 'Escape':
      if (inlinePluginId.value) closeInlinePlugin(); else closePalette()
      break
  }
}

// 插件 iframe 的 Esc：关内联插件页（回到输入框）；若已在非插件态则退化为关面板
function escFromPlugin() {
  if (inlinePluginId.value) closeInlinePlugin()
  else closePalette()
}

// 文档级 Esc 兜底：frameless 窗口激活竞态下输入框可能未获得键盘焦点，
// 仅 @keydown 绑在 <input> 上时 Esc 会丢失，故在 document 上再兜一层（capture 保证不被子组件吞掉）。
function onGlobalEsc(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  e.preventDefault()
  if (actionMenuOpen.value) { closeActionMenu(); return }
  if (inlineQuicklink.value) { cancelInlineQuicklink(); return }
  closePalette()
}

// 方向键/数字直达等导航键是否握在输入框手里（避免与 input @keydown 双处理）
function navFocused(): boolean {
  const el = document.activeElement
  return el === inputRef.value || el === inlineInputRef.value
}

// 文档级导航兜底（capture）：焦点不在输入框时（窗口激活竞态、点过结果/菜单项后）
// 方向键、1-9、Home/End、Enter 依然可靠——与 onGlobalEsc 同思路，根治「方向键时灵时不灵」。
// Escape 专由 onGlobalEsc 处理，这里跳过避免双触发。
function onGlobalKey(e: KeyboardEvent) {
  if (e.key === 'Escape') return
  if (navFocused()) return
  onKeydown(e)
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
    try { await OpenItem(item as any); return true } catch (e) { console.error('[CmdPalette] OpenItem:', e); toast?.error?.(getErrorMessage(e)); return false }
  }
  if (r.type === 'item' && r.item) {
    recordUsage('item:' + r.item.id, 'item', r.label, r.desc)
    try { await OpenItem(r.item as any); return true } catch (e) { console.error('[CmdPalette] OpenItem:', e); toast?.error?.(getErrorMessage(e)); return false }
  }
  if (r.type === 'url' && r.url) {
    try { await Browser.OpenURL(r.url); return true } catch (e) { console.error('[CmdPalette] OpenURL:', e); toast?.error?.(getErrorMessage(e)); return false }
  }
  if (r.type === 'app' && r.appPath) {
    recordUsage('app:' + r.label, 'app', r.label, r.desc)
    try { await LaunchInstalledApp(r.appPath); return true } catch (e) { console.error('[CmdPalette] LaunchInstalledApp:', e); toast?.error?.(getErrorMessage(e)); return false }
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
    markPluginSeen(result.pluginId)
    // 插件未运行时先自动拉起（EnablePlugin = Reload 加载 + DB 置启用）：
    // 面板不静默丢弃这类命令，而是「执行前自动启动」；失败则明示原因停在面板
    if (result.pluginNotRunning) {
      try {
        await EnablePlugin(result.pluginId)
      } catch (e) {
        toast?.error?.(t('pluginStartFailed') + ': ' + getErrorMessage(e))
        return
      }
      await loadPluginIndex()
    }
    // 仅当命令声明 acceptsInput 时，才把 Ctrl+K 文本作为插件参数带入；否则不传（"不设置就不带"）
    let inputText: string | undefined
    if (result.acceptsInput) {
      inputText = result.inlineInput || undefined
      if (!inputText && result.label) {
        const idx = pluginCmdIndex.value.find(c => c.plugin.id === result.pluginId && c.cmd.id === result.pluginCommandId)
        if (idx) {
          const title = commandTitle(idx.cmd, locale)
          if (title && result.label.startsWith(title + ': ')) {
            inputText = result.label.slice(title.length + 2)
          }
        }
      }
    }
    // acceptsInput 命令未带输入：回退「最近一次输入」实现一键重跑——
    // 持久化 store（跨应用重启）优先，其次 usage 表 recentCache（input 字段自 v1 起就有）
    if (result.acceptsInput && !inputText) {
      const last = result.pluginId && result.pluginCommandId
        ? getPluginLastResult(result.pluginId, result.pluginCommandId) : null
      if (last?.input) {
        inputText = last.input
      } else {
        const k = pluginCmdKey(result.pluginId, result.pluginCommandId)
        const re = recentCache.value.find((e) => e.key === k && !!e.input)
        if (re?.input) inputText = re.input
      }
    }
    // 仍无输入：绝不空参执行——空跑 + 必报错 + 空输入写坏历史记录三害齐发。
    // 有前端则打开页面让用户补输入；无前端则提示后停在面板，不动历史。
    if (result.acceptsInput && !inputText) {
      if (result.pluginHasFrontend) {
        inlinePluginName.value = ''
        inlinePluginInit.value = { text: '', command: result.pluginCommandId || '' }
        inlinePluginId.value = result.pluginId
        return
      }
      toast?.error?.(t('pluginNeedInput'))
      return
    }
    recordUsage('plugin:' + result.pluginId + '.' + result.pluginCommandId, 'plugin', result.label, result.desc, inputText || '')
    // 即时刷新「最近使用」：运行完立刻能看到，不必等下次重开面板
    refreshRecentCache().catch(() => {})
    try {
      if (result.pluginHasFrontend) {
        // 前端插件：交由统一 PluginFrame 宿主打开 UI；init 通过同窗口 getter 直接传入（不走全局 pending init 单例）
        inlinePluginName.value = ''
        inlinePluginInit.value = { text: inputText || '', command: result.pluginCommandId || '' }
        inlinePluginId.value = result.pluginId
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
          // 持久化「每命令最近一次结果」：重开面板/重启后 preview 仍可展示、动作菜单可复制、acceptsInput 命令可一键重跑
          savePluginLastResult(result.pluginId, result.pluginCommandId, {
            result: displayText, pluginName: result.desc || result.label,
            pluginId: result.pluginId, pluginCommandId: result.pluginCommandId,
            pluginHasFrontend: result.pluginHasFrontend, input: inputText, acceptsInput: result.acceptsInput,
          })
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
      // 纯本地编码，不依赖任何插件：与 text-encoder 内置的
      // urlEncode 实现完全一致（encodeURIComponent 语义），核心功能零插件耦合
      let encoded = ''
      try { encoded = encodeURIComponent(result.url) } catch { encoded = result.url }
      try { await writeClipboard(encoded) } catch {}
      toast?.success?.(t('pluginResultCopied')); closePalette()
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

// ---- Ctrl+K 二级动作菜单 ----
interface PaletteAction { id: string; label: string; icon: any; danger?: boolean; run: () => void | Promise<void> }

const actionMenuOpen = ref(false)
const actionMenuIndex = ref(0)
const actionTarget = computed<SearchResult | undefined>(() => displayFlat.value[selectedIndex.value])

// 结果的"主值"：文件/应用路径、链接、片段内容等，用于复制 / 资源管理器定位
function targetValue(r: SearchResult): string {
  switch (r.type) {
    case 'item': case 'quicklink': case 'quicklink-inline': return r.item?.value || ''
    case 'app': return r.appPath || ''
    case 'url': case 'clipboard-action': return r.url || ''
    case 'snippet': return r.snippet?.content || ''
    case 'calculator': return r.calcResult || ''
    case 'plugin': return r.label || ''
    default: return ''
  }
}

// 是否为可在资源管理器中定位的本地路径（排除 http/自定义协议与纯命令行）
function isLocalPath(v: string): boolean {
  if (!v) return false
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) return false
  return /^[a-zA-Z]:[\\/]/.test(v) || v.startsWith('\\\\')
}

const contextActions = computed<PaletteAction[]>(() => {
  const r = actionTarget.value
  if (!r) return []
  const acts: PaletteAction[] = []
  const value = targetValue(r)

  // 主操作（等价于直接回车）
  const primaryLabel =
    r.type === 'app' ? t('actLaunch')
      : r.type === 'snippet' ? t('actPasteSnippet')
        : (r.type === 'system' || r.type === 'plugin') ? t('actRun')
          : r.type === 'calculator' ? t('actCopyResult')
            : t('actOpen')
  const primaryIcon = r.type === 'snippet' ? ClipboardPaste : r.type === 'calculator' ? Copy : Play
  acts.push({ id: 'primary', label: primaryLabel, icon: primaryIcon, run: () => executeSelected() })

  if (r.label) acts.push({ id: 'copy-name', label: t('actCopyName'), icon: Copy, run: () => copyAndClose(r.label) })

  if (value && value !== r.label) {
    const label = isLocalPath(value) ? t('actCopyPath') : /^https?:/i.test(value) ? t('actCopyLink') : t('actCopyValue')
    acts.push({ id: 'copy-value', label, icon: Copy, run: () => copyAndClose(value) })
  }

  // 插件专属动作：原生 tab 里的动作菜单太薄——补「分离为独立窗口」与「复制上次结果」
  if (r.type === 'plugin' && r.pluginId) {
    if (r.pluginHasFrontend) {
      acts.push({ id: 'plugin-detach-window', label: t('actPluginDetach'), icon: ExternalLink, run: () => detachPluginFor(r) })
    }
    if (r.pluginCommandId) {
      const last = getPluginLastResult(r.pluginId, r.pluginCommandId)
      if (last?.result) {
        acts.push({ id: 'plugin-copy-last', label: t('actCopyLastResult'), icon: Copy, run: () => copyAndClose(last.result) })
      }
    }
  }

  if (isLocalPath(value)) acts.push({ id: 'reveal', label: t('actReveal'), icon: FolderSearch, run: () => revealAndClose(value) })

  if ((r.type === 'url' || r.type === 'clipboard-action') && r.url) {
    acts.push({ id: 'save-url', label: t('cmdSaveAsItem'), icon: Save, run: () => saveUrlAndClose(r.url!) })
  }

  if ((r.type === 'item' || r.type === 'quicklink' || r.type === 'quicklink-inline') && r.item?.id) {
    const id = r.item.id
    acts.push({ id: 'delete-item', label: t('actDeleteItem'), icon: Trash2, danger: true, run: () => deleteItemAndRefocus(id) })
  }
  if (r.type === 'snippet' && r.snippet?.id) {
    const id = r.snippet.id
    acts.push({ id: 'delete-snippet', label: t('actDeleteSnippet'), icon: Trash2, danger: true, run: () => deleteSnippetAndRefocus(id) })
  }
  return acts
})

function openActionMenu() {
  if (!actionTarget.value || contextActions.value.length === 0) return
  actionMenuIndex.value = 0
  actionMenuOpen.value = true
}

function closeActionMenu(refocus = true) {
  if (!actionMenuOpen.value) return
  actionMenuOpen.value = false
  if (refocus) nextTick(focusInput)
}

async function runAction(a: PaletteAction) {
  actionMenuOpen.value = false
  await a.run()
}

async function copyAndClose(text: string) {
  await writeClipboard(text)
  toast?.success?.(t('copied'))
  closePalette()
}

async function revealAndClose(path: string) {
  try { unwrap(await RevealInExplorer(path)) }
  catch (e) { toast?.error?.(getErrorMessage(e)); nextTick(focusInput); return }
  closePalette()
}

async function saveUrlAndClose(url: string) {
  try {
    const item = unwrap<CollectionItem>(await SaveUrlAsItem(url))
    recordUsage('item:' + (item?.id || ''), 'item', url, url)
    toast?.success?.(t('savedAsItem'))
  } catch (e) { toast?.error?.(getErrorMessage(e)) }
  closePalette()
}

// 删除后不关闭面板：保持在结果列表里继续操作，仅同步本地缓存避免"幽灵条目"
async function deleteItemAndRefocus(id: string) {
  const ok = await toast?.confirm?.(t('confirmDeleteItem'))
  if (!ok) { nextTick(focusInput); return }
  try {
    unwrap(await DeleteItem(id))
    const idx = items.value.findIndex(i => i.id === id)
    if (idx >= 0) items.value.splice(idx, 1)
    recentCache.value = recentCache.value.filter(e => e.key !== 'item:' + id)
    selectedIndex.value = 0
    toast?.success?.(t('actItemDeleted'))
  } catch (e) { toast?.error?.(getErrorMessage(e)) }
  nextTick(focusInput)
}

async function deleteSnippetAndRefocus(id: string) {
  const ok = await toast?.confirm?.(t('confirmDeleteSnippetOne'))
  if (!ok) { nextTick(focusInput); return }
  try {
    unwrap(await DeleteSnippet(id))
    const idx = snippets.value.findIndex(s => s.id === id)
    if (idx >= 0) snippets.value.splice(idx, 1)
    recentCache.value = recentCache.value.filter(e => e.key !== 'snippet:' + id)
    selectedIndex.value = 0
    toast?.success?.(t('actSnippetDeleted'))
  } catch (e) { toast?.error?.(getErrorMessage(e)) }
  nextTick(focusInput)
}

// ---- 加载数据 ----
let itemsLoadGen = 0
let lastPluginIndexLoad = 0
// #6 按需刷新：插件索引 500ms 节流；插件图标（每个插件一次 DB 读取）改为长 TTL 缓存，避免每次 show 全量重拉
const PLUGIN_ICON_TTL = 30_000
let lastPluginIconLoad = 0
const APP_SCAN_TTL = 60_000 // 应用扫描（启动器 start-menu 扫描较慢）长 TTL 缓存
let lastAppScan = 0

async function loadPluginIndex(forceIcons = false) {
  const now = Date.now()
  if (now - lastPluginIndexLoad < 500) return
  lastPluginIndexLoad = now
  try {
    const plugins = unwrap<PluginInfo[]>(await ListPlugins())
    const running = plugins?.filter(p => p.status === 'running') || []
    installedPlugins.value = running
    pluginCmdIndex.value = buildPluginIndex(running)
    // 预加载插件真实图标（data URI），结果列表据此展示，无图标的插件回退到 Puzzle
    if (forceIcons || now - lastPluginIconLoad >= PLUGIN_ICON_TTL) {
      lastPluginIconLoad = now
      const iconPromises = running.map(async (p) => {
        try { const uri = unwrap<string | null>(await GetPluginIcon(p.id)); if (uri) pluginIcons.value[p.id] = uri } catch {}
      })
      await Promise.all(iconPromises)
    }
  } catch (e) { console.error('[CmdPalette] ListPlugins:', getErrorMessage(e)) }
}

// 一次性加载全量池（项目 + 应用 + 片段 + 最近使用），后续匹配完全在前端完成，
// 从而支持拼音与子串搜索（后端 FTS5 前缀匹配无法覆盖这两类）。
async function loadPaletteData() {
  loading.value = true; const gen = ++itemsLoadGen
  await loadFrecency()
  try { const result = unwrap<CollectionItem[]>(await ListAllItems()); if (gen !== itemsLoadGen) return; items.value = result || [] } catch (e) { console.error('[CmdPalette] ListAllItems:', getErrorMessage(e)) }
  // #6 按需刷新：应用扫描（start-menu 较慢）在 TTL 内复用已加载数据，避免每次 show 全量重拉
  try {
    const now = Date.now()
    if (gen === itemsLoadGen && (now - lastAppScan >= APP_SCAN_TTL || installedApps.value.length === 0)) {
      const apps = unwrap<InstalledApp[]>(await ScanInstalledApps())
      if (gen === itemsLoadGen) { installedApps.value = apps || []; lastAppScan = now }
    }
  } catch (e) { console.error('[CmdPalette] ScanInstalledApps:', getErrorMessage(e)) }
  try { const snips = unwrap<CmdSnippet[]>(await ListSnippets()); if (gen !== itemsLoadGen) return; snippets.value = snips || [] } catch (e) { console.error('[CmdPalette] ListSnippets:', getErrorMessage(e)) }
  await refreshRecentCache(gen)
  if (gen === itemsLoadGen) loading.value = false
}

// 刷新「最近使用」缓存：运行命令/打开项目后实时拉取，否则要等下次重开面板才出现
async function refreshRecentCache(gen?: number) {
  try {
    const raw = await GetAllUsage()
    if (gen === undefined || gen === itemsLoadGen) {
      // plugin 记录 label 可能为空（某些热键/自动执行路径），但近期有 plugin 记录本身
      // 就值得留在「最近使用」——组装时用命令标题/插件名兜底，故放宽过滤
      recentCache.value = (unwrap<RecentEntry[]>(raw) || []).filter(e => e.type && (e.label || e.type === 'plugin'))
    }
  } catch (e) { console.error('[CmdPalette] GetAllUsage:', getErrorMessage(e)) }
}

// ---- 生命周期 ----
let lastClipboardUpdate = 0

onMounted(async () => {
  Events.On('clipboard:updated', () => { lastClipboardUpdate = Date.now() })
  // 插件 iframe 内的 Esc 经插件桥转发（qd:plugin-esc）：iframe 抢占焦点后
  // 父级 document 收不到 keydown，这是唯一的 Esc 通路
  window.addEventListener('qd:plugin-esc', escFromPlugin)
  // 文档级 Esc 兜底（capture 阶段），确保输入框未获焦点时也能关闭面板
  document.addEventListener('keydown', onGlobalEsc, true)
  // 文档级导航兜底：焦点不在输入框时方向键/数字/Home/End/Enter 依然可用
  document.addEventListener('keydown', onGlobalKey, true)
  Events.On('palette:shown', () => {
    loadPluginIndex()
    loadPluginNewFlags().catch(() => {})
    // 核心修复：若隐藏前仍停留在内联插件页，重开时保持插件页（「临时收起」而非「主动退出」）。
    // 只有当用户点「返回」/ Esc 触发 closeInlinePlugin 清空 inlinePluginId 后，重开才回到输入框。
    if (inlinePluginId.value) {
      return
    }
    loadPaletteData().catch(e => console.warn('[CmdPalette] loadPaletteData:', e))
    query.value = ''; selectedIndex.value = 0; inlineQuicklink.value = null; inlineQuery.value = ''
    recentExpanded.value = false
    actionMenuOpen.value = false
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
    // 聚焦输入框：单次 rAF 偶发因窗口激活竞态而失效，改为主动多次重试 + 绑定窗口获得焦点事件兜底
    nextTick(() => {
      requestAnimationFrame(focusInput)
      setTimeout(focusInput, 60)
      setTimeout(focusInput, 180)
    })
  })
  // 窗口真正获得 OS 焦点时再补一次聚焦，规避 Show/Focus 异步激活导致的首次聚焦丢失
  window.addEventListener('focus', focusInput)
  await loadPaletteData()
  await loadPluginIndex()
})

// 仅在查询变化时重置选中（避免悬停/频次更新导致选中项跳回顶部）
watch(query, (val) => {
  if (!val.trim()) { inlineQuicklink.value = null; inlineQuery.value = '' }
  if (val.trim() !== clipboardUrlSource.value) clipboardUrlSource.value = ''
  actionMenuOpen.value = false
  selectedIndex.value = 0
  selectedSet.value = new Set()
  lastAnchor.value = -1
})

watch(inlineQuicklink, (v) => { if (v) selectedIndex.value = 0 })

onUnmounted(() => {
  closeInlinePlugin()
  Events.Off('palette:shown')
  Events.Off('clipboard:updated')
  window.removeEventListener('qd:plugin-esc', escFromPlugin)
  document.removeEventListener('keydown', onGlobalEsc, true)
  document.removeEventListener('keydown', onGlobalKey, true)
  window.removeEventListener('focus', focusInput)
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
      <PluginFrame
        :key="inlinePluginId"
        :plugin-id="inlinePluginId"
        :get-init="inlinePluginInitGetter"
        @title="inlinePluginName = $event || inlinePluginName"
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
      <button class="palette-close" @click="closePalette" :title="t('close')">
        <X :size="14" />
      </button>
    </div>

    <div ref="listRef" class="palette-results" v-if="displayGroups.length > 0 && !inlineQuicklink">
      <template v-for="(group, gIdx) in displayGroups" :key="group.type">
        <div class="group-header">
          <span class="group-title">{{ group.label }}</span>
          <button
            v-if="group.totalCount !== undefined && group.totalCount > RECENT_VISIBLE"
            class="group-expand"
            :title="recentExpanded ? t('cmdRecentCollapse') : t('cmdRecentExpand')"
            @click.stop="toggleRecentExpanded()"
            @mousemove.stop
          >
            <ChevronUp v-if="recentExpanded" :size="12" />
            <ChevronDown v-else :size="12" />
          </button>
          <span class="group-count">{{ group.totalCount ?? group.results.length }}</span>
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
            <span v-if="result.type === 'plugin' && result.pluginId && newFlags.has(result.pluginId)" class="result-new-badge">新</span>
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
              <span class="meta-tag">{{ matchTypeI18nKeys[result.matchType] ? t(matchTypeI18nKeys[result.matchType]) : result.matchType }}</span>
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
        <div v-for="(line, i) in previewLines" :key="i" class="preview-line">{{ line }}</div>
      </div>
    </div>

    <div v-if="displayGroups.length === 0 && !inlineQuicklink" class="palette-empty">
      <Search :size="28" class="empty-icon" />
      <p class="empty-title">{{ t('cmdEmptyTitle') }}</p>
      <p class="empty-desc">{{ t('cmdEmptyDesc') }}</p>
    </div>

    <!-- Ctrl+K 二级动作菜单 -->
    <div v-if="actionMenuOpen" class="action-backdrop" @click="closeActionMenu()">
      <div class="action-menu" @click.stop>
        <div class="action-menu-head">
          <span class="action-menu-target">{{ actionTarget?.label }}</span>
        </div>
        <div class="action-menu-list">
          <button
            v-for="(a, i) in contextActions"
            :key="a.id"
            :class="['action-menu-item', { active: i === actionMenuIndex, danger: a.danger }]"
            @click="runAction(a)"
            @mousemove="actionMenuIndex = i"
          >
            <span class="action-menu-index" v-if="i < 9">{{ i + 1 }}</span>
            <component :is="a.icon" :size="14" class="action-menu-icon" />
            <span class="action-menu-label">{{ a.label }}</span>
            <CornerDownLeft v-if="i === actionMenuIndex" :size="11" class="action-menu-enter" />
          </button>
        </div>
      </div>
    </div>

    <div class="palette-footer" v-if="!inlineQuicklink">
      <div class="footer-hint">
        <kbd><ChevronLeft :size="11" /></kbd>
        <kbd><ChevronRight :size="11" /></kbd>
        <kbd><ChevronUp :size="11" /></kbd>
        <kbd><ChevronDown :size="11" /></kbd>
        <span>{{ t('cmdNavigate') }}</span>
      </div>
      <div class="footer-hint">
        <kbd><CornerDownLeft :size="11" /></kbd>
        <span>{{ t('cmdExecute') }}</span>
      </div>
      <div class="footer-hint" v-if="contextActions.length > 0">
        <kbd>Ctrl</kbd><kbd>Enter</kbd>
        <span>{{ t('cmdActions') }}</span>
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
  position: relative;
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

.palette-results {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 4px 8px;
  align-content: start;
  flex: 1;
  overflow-y: auto;
  padding: 6px 8px 10px;
}

.group-header {
  grid-column: 1 / -1;
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
.group-expand {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: none;
  border-radius: var(--radius-full, 999px);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background 150ms ease, color 150ms ease;
}
.group-expand:hover { background: var(--color-bg-tertiary); color: var(--color-text); }
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

/* 「新」角标：内联在结果标签后 */
.result-new-badge {
  flex-shrink: 0;
  font-size: 9px; line-height: 14px; font-weight: 600;
  padding: 0 5px; border-radius: 7px;
  background: var(--color-accent, #4a9eff); color: #fff;
  letter-spacing: 0.5px; align-self: center;
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

.palette-close {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; margin-left: 2px; flex-shrink: 0;
  border: none; background: transparent; border-radius: 5px;
  color: var(--color-text-muted); cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.palette-close:hover { background: var(--color-bg-active); color: var(--color-text-primary); }

.palette-preview {
  flex-shrink: 0; max-height: 170px; overflow-y: auto;
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

/* ---- Ctrl+K 二级动作菜单 ---- */
.action-backdrop {
  position: absolute;
  inset: 0;
  z-index: 40;
  display: flex;
  align-items: flex-end;
  justify-content: flex-end;
  padding: 0 10px 42px;
  background: rgba(0, 0, 0, 0.35);
  backdrop-filter: blur(1.5px);
}

.action-menu {
  width: 268px;
  max-height: 74%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-bg-secondary);
  box-shadow: 0 12px 34px rgba(0, 0, 0, 0.45);
  animation: action-menu-in 0.12s ease-out;
}

@keyframes action-menu-in {
  from { opacity: 0; transform: translateY(6px) scale(0.985); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}

.action-menu-head {
  flex-shrink: 0;
  padding: 8px 12px 6px;
  border-bottom: 1px solid var(--color-border);
}
.action-menu-target {
  display: block;
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--color-text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.action-menu-list {
  flex: 1;
  overflow-y: auto;
  padding: 4px;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.action-menu-list::-webkit-scrollbar { width: 5px; }
.action-menu-list::-webkit-scrollbar-track { background: transparent; }
.action-menu-list::-webkit-scrollbar-thumb { background: var(--color-scrollbar-thumb); border-radius: 3px; }

.action-menu-item {
  display: flex;
  align-items: center;
  gap: 9px;
  width: 100%;
  padding: 7px 9px;
  border: none;
  border-radius: var(--radius-md, 6px);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12.5px;
  font-family: inherit;
  font-weight: 450;
  text-align: left;
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}
.action-menu-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.action-menu-item.active { background: var(--color-accent-bg); color: var(--color-text-primary); }
.action-menu-item.danger { color: var(--color-danger); }
.action-menu-item.danger.active,
.action-menu-item.danger:hover { background: var(--color-danger-bg, rgba(239, 68, 68, 0.12)); color: var(--color-danger); }

.action-menu-icon { flex-shrink: 0; opacity: 0.85; }
.action-menu-index {
  flex-shrink: 0; min-width: 14px; text-align: center;
  font-size: 10px; line-height: 14px; font-weight: 600;
  color: var(--color-text-muted);
  border: 1px solid var(--color-border);
  border-radius: 4px; padding: 0 3px; opacity: 0.8;
}
.action-menu-label { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.action-menu-enter { flex-shrink: 0; opacity: 0.5; }

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
