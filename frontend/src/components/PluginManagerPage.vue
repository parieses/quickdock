<script setup lang="ts">
import PageHeader from './PageHeader.vue'
import QdModal from './QdModal.vue'
import QdSearchBox from './QdSearchBox.vue'
defineOptions({ name: KEEP_ALIVE.plugins })
// name 的唯一来源：pages/keepAlive.ts（与 KeepAlive 白名单共用同一常量）
import { KEEP_ALIVE } from '../pages/keepAlive'
import { logErr, logWarn } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, onActivated, onDeactivated, watch, inject } from 'vue'
import type { Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { Puzzle, Power, PowerOff, Trash2, RefreshCw, RotateCcw, Upload, ExternalLink, History, ChevronDown, ChevronRight, CheckCircle2, XCircle, Globe, Square, Loader2, Search, DownloadCloud, Play, Terminal } from '@lucide/vue'

import {
  ListPlugins,
  DisablePlugin,
  EnablePlugin,
  UninstallPlugin,
  InstallPlugin,
  InstallPluginFromURL,
  InstallPluginFromBytes,
  InspectPluginZip,
  InspectPluginZipData,
  GetPluginIcon,
  ShowPluginWindow,
  ListPluginExecLogs,
  KillPlugin,
  GetPluginNewFlags,
  MarkPluginSeen,
  GetPluginMarket,
} from '../../bindings/quickdock/services/plugin/pluginservice'
import { setPluginUpdateBadge } from '../composables/usePluginUpdateBadge'
// 多选文件对话框：宿主级能力（services/dialog.go），不放在插件域里
import { SelectLocalFiles } from '../../bindings/quickdock/services/appservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import ConfirmDialog from './ConfirmDialog.vue'
import PluginMarketPage from './PluginMarketPage.vue'
import { pluginName, pluginDesc, commandTitle } from '../utils/localize'
import { applyOverflowTitle } from '../utils/overflowTitle'
import { pickDownload } from '../utils/platform'
import { useThemeName } from '../composables/useThemeName'
import { usePluginCommandCatalog, type CommandTrigger } from '../composables/usePluginCommandCatalog'
import { usePluginPermissions } from '../composables/usePluginPermissions'
import { usePluginMarketSources, type MarketSource } from '../composables/usePluginMarketSources'
import type { ToastAPI, PluginInfo, PluginExecLog, PluginCommand } from '../types'

import { reportError } from '../utils/reportError'
const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 命令目录 / 权限确认 / 市场源三块共享状态：权限确认框只有一个实例，
// 市场页作为子组件也调 confirmPermissions，两处安装入口共用它。
const { catalog, loadCommandCatalog, loadCommandParams, commandTrigger } = usePluginCommandCatalog()
const { permDialog, confirmPermissions, resolvePermissionConfirm, permissionsToLines } = usePluginPermissions()
const {
  marketSources, marketOffline, loadMarketSources, saveMarketSources,
  isValidSourceUrl, newSourceId,
} = usePluginMarketSources()

const plugins = ref<PluginInfo[]>([])
const icons = ref<Record<string, string>>({}) // pluginId → data URI（当前主题那一档）
const loading = ref(true)
// 读取失败与「真的一个插件都没装」必须分开：以前失败只弹一次 toast，
// 页面随后渲染「还没有插件，拖入此区域安装」，用户会以为插件全丢了。
const loadFailed = ref(false)
const operating = ref<Set<string>>(new Set())

// 插件「新安装/更新」角标：首次打开后清除
const newFlags = ref<Set<string>>(new Set())

// 视图切换：本地管理 / 在线市场（PluginMarketPage 子组件）
const activeView = ref<'local' | 'market'>('local')

// 市场安装成功后刷新本地插件列表
async function onMarketInstalled() {
  await reloadPlugins()
}

// ---- 执行历史（5.2） ----
const execLogs = ref<PluginExecLog[]>([])
// 加载失败与「真的没有记录」必须分开：否则 catch 里清空数组会把失败
// 渲染成「暂无执行记录」，用户以为历史丢了，也没有重试入口。
const execLogsFailed = ref(false)
const loadingLogs = ref(false)
const historyOpen = ref(false)
const expandedLog = ref<string | null>(null)

// ---- 权限审计 ----
//
// 只在卡片上列"有声明"的项：Permissions 是 Go 结构体，零值（false / 空对象）也会被
// 序列化出来，不过滤就等于给每个插件挂一排假标签。
const PERM_KEYS = ['network', 'filesystem', 'shell', 'process', 'processKill', 'clipboard', 'mcp'] as const

function permTags(p: PluginInfo): string[] {
  const out: string[] = []
  for (const c of p.capabilities || []) if (c) out.push(c)
  const perm = p.permissions || {}
  for (const [k, v] of Object.entries(perm)) {
    // selfAudit / skipReason 是审计开关，不是能力
    if (k === 'selfAudit' || k === 'skipReason') continue
    if (!v) continue
    if (!out.includes(k)) out.push(k)
  }
  return [...new Set(out)]
}

function permLabel(key: string): string {
  const known = PERM_KEYS as readonly string[]
  if (!known.includes(key)) return key // 插件自定义 capability，原样显示
  return t('pluginPerm' + key.charAt(0).toUpperCase() + key.slice(1))
}

// ---- 命令与热键 ----
//
// PluginInfo.commands 一直在下发，但此前「记得名字才用得上」：卡片只露两条标题，
// 触发方式与参数都看不见。现在把 trigger（怎么触发）、hotkey、params 一起列出来：
// 卡片上每条带触发方式标签，点开命令清单弹窗看完整表。
// 增强数据来自 PluginCatalog / PluginCommandParams（此前零调用）；取不到时只隐藏
// 对应增强（description / params 行），弹窗回退到 p.commands，绝不因展示报错。
function cmdTitle(c: PluginCommand): string {
  return commandTitle(c, locale)
}

function triggerLabelFor(c: PluginCommand): string {
  return t('trigger_' + commandTrigger(c))
}

function cmdListTitle(p: PluginInfo): string {
  return (p.commands || [])
    .map((c) => `[${triggerLabelFor(c)}] ${c.hotkey ? c.hotkey + ' · ' : ''}${cmdTitle(c)}`)
    .join('\n')
}

// 命令清单弹窗：卡片宽度放不下整张表，点开看完整清单。
interface CommandRow {
  id: string
  title: string
  hotkey?: string
  trigger: CommandTrigger
  triggerHint: string
  description: string
  paramsText: string
}
const cmdDialog = ref<{ visible: boolean; pluginId: string; pluginName: string; rows: CommandRow[] }>({
  visible: false, pluginId: '', pluginName: '', rows: [],
})

async function openCmdList(p: PluginInfo) {
  // 命令目录是可选增强：失败时 extraRows 为空，弹窗照样用 ListPlugins 的
  // p.commands 打开，只是少了 description / params 这些补充字段。
  const extraRows = catalog.value[p.id] || []
  const rows: CommandRow[] = (p.commands || []).map((c) => {
    const extra = extraRows.find((v) => v.id === c.id)
    return {
      id: c.id,
      title: cmdTitle(c),
      hotkey: c.hotkey,
      trigger: commandTrigger(c),
      triggerHint: extra?.triggerHint || c.prefix || c.matchPattern || '',
      description: extra?.description || c.description || '',
      paramsText: extra?.params ? JSON.stringify(extra.params) : '',
    }
  })
  cmdDialog.value = { visible: true, pluginId: p.id, pluginName: pluginName(p, locale), rows }
  // PluginCommandParams 按需核对参数声明：并发、失败静默，拿不到就隐藏参数行。
  await Promise.all(rows.map(async (row) => {
    const params = await loadCommandParams(p.id, row.id)
    if (!params) return
    const d = cmdDialog.value
    if (!d.visible || d.pluginId !== p.id) return // 弹窗已关闭或切到了别的插件
    const target = d.rows.find((r) => r.id === row.id)
    if (target && !target.paramsText) target.paramsText = JSON.stringify(params)
  }))
}

// ---- 卡片唯一入口（CTA） ----
//
// 每张卡片右下角只给一个按钮，按当前状态决定它是什么：未启用 → 启用（主色，
// 「装了但没跑起来」是唯一需要用户动手的状态）；能开界面 → 打开；只有命令 →
// 命令清单；其余不给按钮，也不硬凑一个「禁用」当主操作。
// 判断刻意不调 canOpenPlugin：那个还带 !operating，操作进行中会让按钮文字
// 在「打开 / 命令」之间跳一下；这里只看稳定状态，忙碌由 Loader2 表达。
type CardCtaKind = 'open' | 'activate' | 'commands'
interface CardCta { kind: CardCtaKind; label: string; icon: Component }

const ctaMap = computed<Record<string, CardCta>>(() => {
  const out: Record<string, CardCta> = {}
  for (const p of plugins.value) {
    if (!isEnabled(p)) {
      out[p.id] = { kind: 'activate', label: t('pluginEnable'), icon: Power }
    } else if (p.hasFrontend && (p.status === 'running' || p.status === 'registered')) {
      out[p.id] = { kind: 'open', label: t('pluginOpen'), icon: ExternalLink }
    } else if (p.commands && p.commands.length) {
      out[p.id] = { kind: 'commands', label: t('pluginCtaCommands'), icon: Terminal }
    }
  }
  return out
})

function onCardCta(p: PluginInfo) {
  const cta = ctaMap.value[p.id]
  if (!cta || operating.value.has(p.id)) return
  if (cta.kind === 'activate') void togglePlugin(p)
  else if (cta.kind === 'open') openPluginPage(p)
  else void openCmdList(p)
}

// 底部那行灰字放不下时整行省略，完整内容进 title
function cardTagsTitle(p: PluginInfo): string {
  const parts: string[] = [`v${p.version}`]
  if (p.category) parts.push(p.category)
  const perms = permTags(p).map(permLabel)
  if (perms.length) parts.push(perms.join(' / '))
  return parts.join(' · ')
}

async function loadLogs() {
  loadingLogs.value = true
  execLogsFailed.value = false
  try {
    execLogs.value = (unwrap(await ListPluginExecLogs(100)) || []) as PluginExecLog[]
  } catch (e) {
    execLogs.value = []
    execLogsFailed.value = true
  } finally {
    loadingLogs.value = false
  }
}
function toggleHistory() {
  historyOpen.value = !historyOpen.value
  // 失败态留给显式「重试」按钮处理，避免每次展开/收起都静默重拉一次。
  if (historyOpen.value && execLogs.value.length === 0 && !execLogsFailed.value) loadLogs()
}
function toggleLogDetail(id: string) {
  expandedLog.value = expandedLog.value === id ? null : id
}
function triggerLabel(trigger: string): string {
  return t('trigger_' + trigger) || t('trigger_manual')
}

// ---- 分类筛选 ----
const selectedCategory = ref('')
const categories = computed(() => {
  const cats = new Set<string>()
  for (const p of plugins.value) {
    if (p.category) cats.add(p.category)
  }
  return Array.from(cats).sort()
})
// 名称 / 描述 / 分类 / id 一起搜：装多了以后只按分类 tab 筛，"找那个 OCR 的"
// 还是得逐张卡片看标题。
const pluginQuery = ref('')

const filteredPlugins = computed(() => {
  let list = sortedPlugins.value
  if (selectedCategory.value) list = list.filter(p => p.category === selectedCategory.value)
  const q = pluginQuery.value.trim().toLowerCase()
  if (!q) return list
  return list.filter((p) =>
    pluginName(p, locale).toLowerCase().includes(q) ||
    pluginDesc(p, locale).toLowerCase().includes(q) ||
    (p.category || '').toLowerCase().includes(q) ||
    (p.id || '').toLowerCase().includes(q),
  )
})

// ---- 检查更新 ----
// 更新检查原本只能在市场页触发：用户装完插件停在本地页，想知道有没有新版
// 得先切过去。这里直接复用市场索引接口，只统计「已装且有新版」的数量。
const checkingUpdates = ref(false)
async function checkUpdates() {
  checkingUpdates.value = true
  try {
    const idx = unwrap<{ plugins?: Array<{ installed?: boolean; has_update?: boolean }> }>(await GetPluginMarket())
    const n = (idx?.plugins || []).filter((p) => p.installed && p.has_update).length
    setPluginUpdateBadge(n)
    if (n > 0) toast.success(t('pluginUpdatesFound', { n }))
    else toast.success(t('pluginNoUpdates'))
  } catch (e) {
    reportError(e, 'PluginManagerPage.checkUpdates')
  } finally {
    checkingUpdates.value = false
  }
}

// ---- 市场源管理 / 离线缓存标注 ----
//
// 源配置落在 app_state（qd_ 前缀，用已有的 GetValue/SetValue 绑定），
// 后端 GetPluginMarket 读同一个 key。这里只编辑草稿，保存成功后由
// usePluginMarketSources 发出刷新信号，市场页 watch 到就重拉索引。
const sourcesDialog = ref(false)
const sourcesDraft = ref<MarketSource[]>([])
const savingSources = ref(false)

async function openSourcesDialog() {
  // onMounted 里的读取是异步的，用户一进市场页就点「市场源」时可能还没读完；
  // 这里再等一次，避免打开一个空的编辑态把已存配置覆盖掉。
  await loadMarketSources()
  sourcesDraft.value = marketSources.value.map((s) => ({ ...s }))
  sourcesDialog.value = true
}
function closeSourcesDialog() { sourcesDialog.value = false }
function addSource() {
  sourcesDraft.value.push({ id: newSourceId(), name: '', url: '', enabled: true })
}
function removeSource(i: number) { sourcesDraft.value.splice(i, 1) }
async function saveSources() {
  // 只校验自定义源；内置官方源不可编辑、不可删除，只允许停用。
  const bad = sourcesDraft.value.find((s) => !s.builtin && (!s.name.trim() || !isValidSourceUrl(s.url)))
  if (bad) { toast?.error?.(t('pluginMarketSourceInvalid')); return }
  savingSources.value = true
  try {
    const ok = await saveMarketSources(sourcesDraft.value.map((s) => ({ ...s, url: s.url.trim() })))
    if (!ok) { toast?.error?.(t('pluginMarketSourceInvalid')); return }
    sourcesDialog.value = false
    toast?.success?.(t('pluginMarketSourceSaved'))
  } finally {
    savingSources.value = false
  }
}

// 缓存时间是 Unix 秒；没有时间戳时给个占位符，不编造「最新」。
const offlineTime = computed(() => {
  const ts = marketOffline.value.cachedAt
  return ts ? new Date(ts * 1000).toLocaleString() : '—'
})

// 卸载确认
const showUninstallConfirm = ref(false)
const uninstallingId = ref('')
const uninstallingName = ref('')
// 「保留数据」勾选：卸载时只删本体，私有数据/使用记录/执行日志留着
const uninstallKeepData = ref(false)

// ---- 加载 ----
// 图标分深浅两档，按当前主题取用；主题切换时需重新拉取（见下方 watch）
const themeName = useThemeName()

async function loadIcons() {
  const theme = themeName.value
  await Promise.all(
    plugins.value
      .filter(p => p.hasFrontend)
      .map(async (p) => {
        try {
          const dataUri = unwrap<string | null>(await GetPluginIcon(p.id, theme))
          if (dataUri) icons.value[p.id] = dataUri
        } catch (e) { logErr('PluginManagerPage', e) }
      })
  )
}

// silent：刷新不闪屏。卸载/安装/停用失败后的重载都走这条——列表已有内容时
// 把九宫格换成「加载中」再换回，整页会明显闪一下（用户报「卸载插件页面会闪」）。
// 只有首次加载（列表还是空的）才值得显示全屏 loading。
async function loadPlugins(opts?: { silent?: boolean }) {
  if (!opts?.silent) loading.value = true
  try {
    plugins.value = (unwrap(await ListPlugins()) || []) as PluginInfo[]
    loadFailed.value = false
    await loadIcons()
    // 加载「新」角标集合
    try {
      const flags = await GetPluginNewFlags()
      if (Array.isArray(flags)) newFlags.value = new Set(flags as string[])
    } catch (e) { logErr('PluginManagerPage', e) }
  } catch (e) {
    loadFailed.value = true
    toast?.error?.(t('pluginLoadFailed') + ': ' + getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

// 常规刷新（卸载/安装/失败重试）：保留现有列表，静默替换数据
function reloadPlugins() {
  return loadPlugins({ silent: true })
}

watch(themeName, () => { void loadIcons() })

// ---- 强制终止进程（进程锁目录导致更新/卸载失败时用）----
async function killPlugin(p: PluginInfo) {
  if (operating.value.has(p.id)) return
  operating.value.add(p.id)
  try {
    unwrap(await KillPlugin(p.id))
    // 杀进程 ≠ 禁用：后端把状态置回 registered（懒加载就绪），插件仍启用，
    // 命令面板可见、下次打开/执行命令自动拉起——无需手动再启用。
    p.status = 'registered'
    toast?.success?.(t('pluginKilled'))
  } catch (e) {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
  } finally {
    operating.value.delete(p.id)
  }
}

// 插件的「已启用」语义 = 运行中（running）或 已登记待启动（registered）。
// 懒加载后多数插件处于 registered（已启用、进程待首次使用才拉起），电源按钮必须按
//「已启用」处理才能正确禁用；否则会出现「显示启用、点了只是把它启动起来、且禁用不掉」的错位。
function isEnabled(p: PluginInfo): boolean {
  return !p.disabled && (p.status === 'running' || p.status === 'registered')
}

// ---- 启用/禁用 ----
async function togglePlugin(p: PluginInfo) {
  if (operating.value.has(p.id)) return
  operating.value.add(p.id)
  try {
    if (isEnabled(p)) {
      unwrap(await DisablePlugin(p.id))
      // 后端语义：禁用 = disabled=true + status=stopped；两个字段都要同步本地，
      // 否则徽标/电源按钮会与真实状态错位（曾出现启用后仍显示「已禁用」）。
      p.disabled = true
      p.status = 'stopped'
    } else {
      unwrap(await EnablePlugin(p.id))
      // EnablePlugin → ReloadPlugin 返回前已完成加载握手，状态即 running
      p.disabled = false
      p.status = 'running'
    }
  } catch (e) {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
    await reloadPlugins()
  } finally {
    operating.value.delete(p.id)
  }
}

// ---- 卸载 ----
function onUninstall(p: PluginInfo) {
  uninstallingId.value = p.id
  uninstallingName.value = pluginName(p, locale)
  showUninstallConfirm.value = true
}

async function handleUninstall() {
  const id = uninstallingId.value
  showUninstallConfirm.value = false
  if (operating.value.has(id)) return
  operating.value.add(id)
  try {
    await unwrap(await UninstallPlugin(id, uninstallKeepData.value)) // code!=0 抛错，防止卸载失败误报成功
    toast?.success?.(t(uninstallKeepData.value ? 'pluginUninstallKept' : 'deleted'))
    await reloadPlugins()
  } catch (e) {
    toast?.error?.(t('pluginUninstallFailed') + ': ' + getErrorMessage(e))
  } finally {
    operating.value.delete(id)
  }
}

// ---- 重新安装：文件损坏时只覆盖程序文件，私有数据不丢 ----
//
// 与「卸载再装」的区别：这里只走覆盖安装（InstallFromZip），从不调 UninstallPlugin。
// 插件私有库在 <dataDir>/data/<id>/data.db，plugin_data / usage_frecency / exec_logs
// 在主库里，都不在 pluginsDir/<id> 内；旧目录被重命名为 .bak 而非删除，
// InsertPluginFull 是 UPSERT，连 enabled / installed_at 都保留。
// 市场里的插件按 id 取当前平台的包重下（同版本覆盖放行，后端只拒绝降级）；
// 当初用本地 zip 装、市场索引里没有的，退回让用户选本地 zip，两条路都不经过卸载。
async function onReinstall(p: PluginInfo) {
  if (operating.value.has(p.id)) return
  const lines = [pluginName(p, locale), ...permissionsToLines(p.permissions)]
  lines.push(t('pluginReinstallKeepsData'))
  if (!(await confirmPermissions(lines, 'pluginReinstallConfirmTitle'))) return
  operating.value.add(p.id)
  try {
    let url = ''
    try {
      const idx = unwrap<{ plugins?: Array<{ id: string; downloads?: Record<string, string> }> }>(await GetPluginMarket())
      const item = (idx?.plugins || []).find((x) => x.id === p.id)
      url = item ? pickDownload(item.downloads) : ''
    } catch (e) {
      // 市场不可达 / 无离线缓存不该阻断本地修复：降级到让用户选本地 zip
      logWarn('PluginManagerPage', 'market lookup failed, fall back to local zip:', e)
    }
    if (url) {
      await unwrap(await InstallPluginFromURL(url))
    } else {
      const paths = (unwrap(await SelectLocalFiles(t('pluginReinstallPickZip'), '插件包', '*.zip')) || []) as string[]
      if (!paths.length) return // 用户取消，不算失败
      await unwrap(await InstallPlugin(paths[0]))
    }
    toast?.success?.(t('pluginReinstallSuccess'))
    await reloadPlugins()
  } catch (e) {
    toast?.error?.(t('pluginReinstallFailed') + ': ' + getErrorMessage(e))
  } finally {
    operating.value.delete(p.id)
  }
}

// ---- 安装前权限二次确认 ----
//
// native 插件的危险调用运行期拦不住，唯一能拦的时机就是安装前：把 zip 里
// plugin.json 声明的敏感能力（联网 / 文件 / 剪贴板 / Shell / 进程 / mcp）亮出来，
// 用户看清再点确认。后端 installer.go 对 selfAudit=skip 只记日志，所以这里
// 绝不「预检失败 = 默认放行」——那等于替用户同意了一份根本没看到的声明。
interface ZipPermPreview { name: string; version: string; permissions: string[] }

/** 逐包预检并汇总成确认框明细行；allFailed 时确认框改用「无法核对」的标题。 */
async function buildPermDetails<T>(
  items: T[],
  inspectOf: (item: T) => Promise<ZipPermPreview | null>,
  labelOf: (item: T) => string,
): Promise<{ lines: string[]; allFailed: boolean }> {
  const lines: string[] = []
  let ok = 0
  for (const it of items) {
    let prev: ZipPermPreview | null = null
    try { prev = await inspectOf(it) } catch { prev = null }
    if (!prev) {
      // 预检不出来也把包名摆出来，绝不跳过这一行——跳过就是静默放行。
      lines.push(labelOf(it))
      continue
    }
    ok++
    lines.push(`${labelOf(it)} — ${prev.name} v${prev.version}`)
    if (prev.permissions.length) lines.push(...prev.permissions.map((p) => '  · ' + p))
    else lines.push(`  · ${t('pluginPermNone')}`)
  }
  // 声明是插件自己写的，宿主无法担保真实性；这句话必须跟着清单一起出现，
  // 免得用户把「列出来了」误读成「宿主审核通过了」。
  lines.push(t('pluginPermDisclaimer'))
  return { lines, allFailed: ok === 0 }
}

/** 统一的确认入口：无论预检成败都必须弹框、必须用户点确认，不确认即取消。 */
async function confirmInstallPermissions<T>(
  items: T[],
  inspectOf: (item: T) => Promise<ZipPermPreview | null>,
  labelOf: (item: T) => string,
): Promise<boolean> {
  const { lines, allFailed } = await buildPermDetails(items, inspectOf, labelOf)
  return confirmPermissions(lines, allFailed ? 'pluginPermInspectFailed' : 'pluginPermConfirmTitle')
}

// ---- 安装（多选 / 多拖：一次装多个 zip）----
//
// 文件对话框走宿主 SelectLocalFiles（AllowsMultipleSelection），拖拽也能一次进来
// 多个 .zip。逐个装、失败不中断，最后统一刷新一次列表 —— 每装一个刷一次会让
// 九宫格连续闪 N 次；失败的文件名单独带进提示，否则用户只知道「装了但少了一个」，
// 也不知道少的是哪个。

// 批量进度：只用于遮罩文案「第 i / 共 n 个」；装单个时沿用原来的「安装中…」
const installProgress = ref({ done: 0, total: 0 })

async function installBatch<T>(
  items: T[],
  run: (item: T) => Promise<unknown>,
  labelOf: (item: T) => string,
) {
  installProgress.value = { done: 0, total: items.length }
  operating.value.add('_install_')
  const failed: { name: string; err: string }[] = []
  try {
    for (let i = 0; i < items.length; i++) {
      installProgress.value = { done: i, total: items.length }
      try {
        await run(items[i])
      } catch (e) {
        // 单个失败不中断整批：一个坏包不该让后面几个也装不上
        failed.push({ name: labelOf(items[i]), err: getErrorMessage(e) })
      }
    }
  } finally {
    operating.value.delete('_install_')
    installProgress.value = { done: 0, total: 0 }
  }
  if (failed.length === 0) {
    toast?.success?.(
      items.length > 1 ? t('pluginInstallBatchDone', { n: items.length }) : t('pluginInstallSuccess'),
    )
  } else {
    const ok = items.length - failed.length
    const first = failed[0]
    const more = failed.length > 1 ? t('pluginInstallBatchMore', { n: failed.length - 1 }) : ''
    toast?.error?.(t('pluginInstallBatchPartial', { ok, fail: failed.length }) + '：' + first.name + ' - ' + first.err + more)
  }
  await reloadPlugins()
}

// 提示里只显示文件名，不甩一长串绝对路径
function zipName(p: string): string {
  return p.split(/[\\/]/).pop() || p
}

async function selectAndInstall() {
  if (operating.value.has('_install_')) return
  // 先选文件：此时不显示遮罩，避免选文件期间「安装中」抢窗
  let paths: string[] = []
  try {
    paths = (unwrap(await SelectLocalFiles(t('pluginInstallPickTitle'), '插件包', '*.zip')) || []) as string[]
  } catch (e) {
    // 选文件这一步必须出声。它发生在 installBatch 之外，不接住就是个未处理的
    // promise rejection —— 表现是「点了按钮完全没反应」，连到底是取消还是出错都分不清
    // （后端缺方法、对话框被拒都会走到这里）。
    toast?.error?.(t('pluginInstallFailed') + ': ' + getErrorMessage(e))
    return
  }
  if (!paths.length) return // 用户取消
  // 安装前权限确认：亮出每个包声明的敏感能力，用户确认才继续；
  // 预检不出来也照样弹框（installer.go 只记日志，不能在这里默默放行）。
  if (!(await confirmInstallPermissions(
    paths,
    async (p) => unwrap<ZipPermPreview>(await InspectPluginZip(p)),
    zipName,
  ))) return
  await installBatch(paths, async (p) => unwrap(await InstallPlugin(p)), zipName)
}

// ---- 拖拽安装 ----
//
// WebView 里从资源管理器拖进来的 File 拿不到磁盘路径（浏览器安全模型只给内容），
// 所以拖拽走「读成字节 → 后端写临时文件 → 同一条 InstallFromZip 链路」，
// 与「从文件安装」共用后端校验（zip slip / zip bomb 防护 + 失败回滚），
// 只是取包的途径不同。

// 走 IPC 的字节要过 base64，体积膨胀约 1/3；超过这个量级就该改用「从文件安装」
// （后端只收路径，不经过前端内存）。
const maxDropBytes = 50 * 1024 * 1024

const dragOver = ref(false)
// 嵌套元素会连续触发 dragenter/dragleave，用计数抵消，否则高亮会疯狂闪烁
let dragDepth = 0

function dragHasFiles(e: DragEvent): boolean {
  const types = e.dataTransfer?.types
  return !!types && Array.from(types).includes('Files')
}

function onDragEnter(e: DragEvent) {
  // 拖的是文本（比如集合卡片重排）时不接管，否则会打断页面内既有的拖拽交互
  if (!dragHasFiles(e)) return
  dragDepth++
  dragOver.value = true
}

function onDragOver(e: DragEvent) {
  if (!dragHasFiles(e)) return
  // 不 preventDefault 的话 drop 不会触发，且 WebView 可能自己导航到该文件
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
}

function onDragLeave(e: DragEvent) {
  if (!dragHasFiles(e)) return
  dragDepth = Math.max(0, dragDepth - 1)
  if (dragDepth === 0) dragOver.value = false
}

async function onDrop(e: DragEvent) {
  if (!dragHasFiles(e)) return
  e.preventDefault()
  dragDepth = 0
  dragOver.value = false
  // 一次拖进来多个 zip 时全部装上（非 zip 静默忽略，只在没有 zip 时才提示）
  const files = Array.from(e.dataTransfer?.files || []).filter((f) => f.name.toLowerCase().endsWith('.zip'))
  if (!files.length) {
    toast?.error?.(t('pluginDropZipOnly'))
    return
  }
  if (operating.value.has('_install_')) return
  // 体积检查在开装之前做：装到一半才发现超限，用户分不清哪几个进去了
  if (files.some((f) => f.size > maxDropBytes)) {
    toast?.error?.(t('pluginDropTooLarge'))
    return
  }
  // base64 只算一遍：预检和安装各要一份，重复读 50MB 文件太浪费
  const b64 = new Map<File, string>()
  for (const f of files) b64.set(f, await fileToBase64(f))
  // 安装前权限确认（与「从文件安装」同一条规则：不确认就取消，不默默放行）
  if (!(await confirmInstallPermissions(
    files,
    async (f) => unwrap<ZipPermPreview>(await InspectPluginZipData(f.name, b64.get(f) || '')),
    (f) => f.name,
  ))) return
  await installBatch(files, async (f) => unwrap(await InstallPluginFromBytes(f.name, b64.get(f) || '')), (f) => f.name)
}

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => {
      // dataURL 形如 data:<mime>;base64,xxxx —— 绑定层要的是裸 base64
      const s = String(r.result || '')
      resolve(s.slice(s.indexOf(',') + 1))
    }
    r.onerror = () => reject(new Error('读取文件失败'))
    r.readAsDataURL(file)
  })
}

// ---- Emits ----
const emit = defineEmits<{ (e: 'navigate-plugin', pluginId: string): void }>()

// 打开插件的唯一判据：有界面 + 未禁用 + 进程就绪/运行中 + 没有别的操作正在跑。
// 悬停按钮与双击卡片共用同一个函数，两个入口才不会各自漂移出不同的可用条件。
function canOpenPlugin(p: PluginInfo): boolean {
  return !!p.hasFrontend && !p.disabled
    && (p.status === 'running' || p.status === 'registered')
    && !operating.value.has(p.id)
}

function openPluginPage(p: PluginInfo) {
  if (!canOpenPlugin(p)) return
  // 在新窗口中打开插件前端页面
  ShowPluginWindow(p.id).then(() => {
    // 首次打开 → 清除「新」角标
    if (newFlags.value.has(p.id)) {
      newFlags.value.delete(p.id)
      MarkPluginSeen(p.id).catch(() => {})
    }
    // 打开窗口路径会 EnsureLoaded 拉起后端（registered→running），
    // 稍等握手完成后立即对齐徽标，不等 3s 轮询
    windowOpenTimer = setTimeout(refreshStatuses, 1200)
  }).catch(e => {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
  })
}

// 双击卡片打开插件窗口（等价于悬停按钮里的「打开」）。
// 按钮区的 click 各有语义（开关/终止/卸载），但 dblclick 仍会冒泡到卡片：
// 不拦的话「连点两下停用」会顺带弹出一个插件窗口。
function onCardDblClick(e: MouseEvent, p: PluginInfo) {
  if ((e.target as HTMLElement | null)?.closest('.card-actions-top')) return
  if (!canOpenPlugin(p)) return
  openPluginPage(p)
}

// ---- 状态辅助 ----
const statusOrder: Record<string, number> = { running: 0, crashed: 1, stopped: 2, starting: 3 }

// 解析 RFC3339 时间串为毫秒时间戳；缺失/非法返回 0（排到最后）。
function parseTs(s?: string): number {
  if (!s) return 0
  const n = Date.parse(s)
  return isNaN(n) ? 0 : n
}

const sortedPlugins = computed(() => {
  return [...plugins.value].sort((a, b) => {
    // 先按 useCount 倒序（常用优先）
    if ((b.usageCount || 0) !== (a.usageCount || 0)) {
      return (b.usageCount || 0) - (a.usageCount || 0)
    }
    // 频率相同时，最近安装/更新的排前面（新装或刚升级的一眼可见）
    const tsDiff = parseTs(b.updatedAt) - parseTs(a.updatedAt)
    if (tsDiff !== 0) return tsDiff
    // 再按状态排序
    const statusDiff = (statusOrder[a.status] ?? 99) - (statusOrder[b.status] ?? 99)
    if (statusDiff !== 0) return statusDiff
    // 最后按名称排序，保证顺序稳定
    return (a.name || '').localeCompare(b.name || '')
  })
})
function statusBadgeClass(p: PluginInfo): string {
  if (p.disabled) return 'badge-disabled'
  switch (p.status) {
    case 'running': return 'badge-running'
    case 'starting': return 'badge-starting'
    case 'registered': return 'badge-registered'
    case 'unresponsive': return 'badge-unresponsive'
    case 'stopped': return 'badge-stopped'
    case 'crashed': return 'badge-crashed'
    default: return 'badge-registered'
  }
}

function statusLabel(p: PluginInfo): string {
  if (p.disabled) return t('pluginStatusDisabled')
  switch (p.status) {
    case 'running': return t('pluginStatusRunning')
    case 'starting': return t('pluginStatusStarting')
    case 'registered': return t('pluginStatusRegistered')
    case 'unresponsive': return t('pluginStatusUnresponsive')
    case 'stopped': return t('pluginStatusStopped')
    case 'crashed': return t('pluginStatusCrashed')
    default: return t('pluginStatusRegistered')
  }
}

// 轻量状态刷新：只从后端合并 status/disabled/usageCount，不重拉图标与角标。
// 覆盖「打开窗口后 EnsureLoaded 拉起（registered→running）」「watchPlugin 崩溃重启」
// 「别的窗口跑了一条插件命令（计数 +1）」等后台变化——管理页本地数组不会自己变，
// 必须轮询对齐。
async function refreshStatuses() {
  if (activeView.value !== 'local') return
  try {
    const fresh = (unwrap(await ListPlugins()) || []) as PluginInfo[]
    const byId = new Map(fresh.map(q => [q.id, q]))
    for (const p of plugins.value) {
      const f = byId.get(p.id)
      if (f) {
        p.status = f.status
        p.disabled = f.disabled
        p.usageCount = f.usageCount
      }
    }
  } catch (e) { logErr('PluginManagerPage', e) }
}

let statusTimer: number | undefined
let windowOpenTimer: ReturnType<typeof setTimeout> | null = null
// 本页在 KeepAlive 白名单里：切走只触发 onDeactivated，不卸载，
// onUnmounted 里的清理永远不会执行 → 3s 状态轮询会在后台一直跑。
// 所以定时器启停必须挂到激活/失活钩子上，并且做成幂等（首次挂载
// onMounted 与 onActivated 都会触发，别起两个 interval）。
let pageActive = false
let needsRefresh = false

function startPolling() {
  if (pageActive) return
  pageActive = true
  if (needsRefresh) {
    needsRefresh = false
    void refreshStatuses() // 切回来先补一次，免得第一眼是过期状态
  }
  if (statusTimer === undefined) statusTimer = window.setInterval(refreshStatuses, 3000)
}

function stopPolling() {
  pageActive = false
  needsRefresh = true
  if (statusTimer !== undefined) { clearInterval(statusTimer); statusTimer = undefined }
}

onMounted(() => {
  loadPlugins(); loadLogs()
  void loadCommandCatalog()   // 命令目录取不到会静默降级，不阻塞页面
  void loadMarketSources()    // 市场源编辑态；拉取索引仍由市场页自己发起
  startPolling()
})
onActivated(startPolling)
onDeactivated(stopPolling)
onUnmounted(() => {
  stopPolling()
  if (windowOpenTimer !== null) { clearTimeout(windowOpenTimer); windowOpenTimer = null }
})
</script>

<template>
  <div
    class="plugin-page"
    :class="{ 'page-drag-over': dragOver }"
    @dragenter="onDragEnter"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
  >
    <!-- 头部 -->
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title">
          <Puzzle :size="17" />
          {{ t('pluginManager') }}
          <span v-if="plugins.length" class="plugin-count">{{ plugins.length }}</span>
        </h2>
      </template>
      <!-- 页头动作分两级：左边一组是「顺手工具」（市场 / 检查更新 / 刷新），
           右边实心蓝只留给这一页的主操作——从文件安装。四个按钮同款描边时，
           用户看不出先点哪个。 -->
      <div class="qd-page-actions">
        <div class="qd-action-group">
          <button :class="['qd-btn', 'qd-btn-ghost', { 'is-on': activeView === 'market' }]" @click="activeView = activeView === 'local' ? 'market' : 'local'" :title="t('pluginMarket')">
            <Globe :size="14" />
            <span>{{ t('pluginMarket') }}</span>
          </button>
          <button v-if="activeView === 'market'" class="qd-btn qd-btn-ghost" @click="openSourcesDialog">
            <span>{{ t('pluginMarketSources') }}</span>
          </button>
          <button class="qd-btn qd-btn-ghost" :disabled="checkingUpdates" @click="checkUpdates" :title="t('pluginCheckUpdates')">
            <Loader2 v-if="checkingUpdates" :size="14" class="spin" />
            <DownloadCloud v-else :size="14" />
            <span>{{ checkingUpdates ? t('checking') : t('pluginCheckUpdates') }}</span>
          </button>
          <button class="qd-icon-btn" @click="reloadPlugins" :title="t('refresh')">
            <RefreshCw :size="14" />
          </button>
        </div>
        <span class="qd-action-sep"></span>
        <button class="qd-btn qd-btn-primary" @click="selectAndInstall" :title="t('pluginInstallFromFile')" :disabled="operating.has('_install_')">
          <Loader2 v-if="operating.has('_install_')" :size="14" class="spin" />
          <Upload v-else :size="14" />
          <span>{{ operating.has('_install_') ? t('pluginInstalling') : t('pluginInstallFromFile') }}</span>
        </button>
      </div>
    </PageHeader>

    <div v-if="activeView === 'local'" class="plugin-local">
    <!-- 分类 Tabs：做成一条分段控制器（灰轨道 + 选中实块），十几个分类平铺成
         一排小字时，选中项只靠一点淡蓝几乎找不到锚点。搜索框留在同一行最右，
         视线不用在「筛」和「搜」之间来回跑。 -->
    <div class="plugin-categories" v-if="plugins.length > 0">
      <div class="cat-track">
        <button
          :class="['cat-tab', { active: selectedCategory === '' }]"
          @click="selectedCategory = ''"
        >{{ t('pluginMarketAll') }} <span class="cat-count">{{ plugins.length }}</span></button>
        <button
          v-for="cat in categories"
          :key="cat"
          :class="['cat-tab', { active: selectedCategory === cat }]"
          @click="selectedCategory = cat"
        >{{ cat }} <span class="cat-count">{{ plugins.filter(p => p.category === cat).length }}</span></button>
      </div>
      <QdSearchBox v-model="pluginQuery" class="plugin-search" :icon-size="12" :placeholder="t('pluginSearchPh')" />
    </div>

    <!-- 加载中 -->
    <div v-if="loading" class="plugin-loading">
      <p>{{ t('loading') }}</p>
    </div>

    <!-- 加载失败：与空态分开，给出重试入口 -->
    <div v-else-if="loadFailed" class="plugin-empty">
      <Puzzle :size="20" class="empty-icon" />
      <p class="empty-title">{{ t('statusError') }}</p>
      <button class="qd-btn" @click="loadPlugins()">{{ t('statusRetry') }}</button>
    </div>

    <!-- 空状态 -->
    <div v-key-click v-else-if="plugins.length === 0" class="plugin-empty" @click="selectAndInstall">
      <Puzzle :size="20" class="empty-icon" />
      <p class="empty-title">{{ t('pluginNoPlugins') }}</p>
      <p class="empty-desc">{{ t('pluginDragToInstall') }}</p>
    </div>

    <!-- 九宫格插件列表：空态要分清「分类里没有」和「搜了没匹配」。
         搜索态沿用市场页的 pluginMarketNoResult，别再说「该分类下暂无插件」——
         没选分类时这句话是误导；图标也同步换成搜索图标。 -->
    <div v-else-if="filteredPlugins.length === 0" class="plugin-empty">
      <component :is="pluginQuery.trim() ? Search : Puzzle" :size="20" class="empty-icon" />
      <p class="empty-title">{{ pluginQuery.trim() ? t('pluginMarketNoResult') : t('pluginEmptyCategory') }}</p>
    </div>

    <div v-else class="plugin-grid">
      <div
        v-for="p in filteredPlugins"
        :key="p.id"
        :class="['plugin-card', { 'card-running': p.status === 'running', 'card-openable': canOpenPlugin(p) }]"
        :title="canOpenPlugin(p) ? t('pluginDblclickOpen') : undefined"
        @dblclick="onCardDblClick($event, p)"
      >
        <!-- 悬停快捷操作：和状态徽标共用右上角（卡片只有两百来像素宽，各占一边
             会互相挤），鼠标进来时徽标淡出、这组图标淡入。 -->
        <div class="card-actions-top">
          <button
            :class="['action-top-btn', isEnabled(p) ? 'btn-stop-top' : 'btn-start-top']"
            :disabled="operating.has(p.id)"
            @click.stop="togglePlugin(p)"
            :title="isEnabled(p) ? t('pluginDisable') : t('pluginEnable')"
          >
            <component :is="isEnabled(p) ? PowerOff : Power" :size="12" />
          </button>
          <button
            v-if="p.status === 'running' || p.status === 'crashed'"
            class="action-top-btn btn-kill-top"
            :disabled="operating.has(p.id)"
            @click.stop="killPlugin(p)"
            :title="t('pluginKillTitle')"
          >
            <Square :size="11" />
          </button>
          <button
            v-if="p.hasFrontend"
            class="action-top-btn btn-open-top"
            :disabled="!canOpenPlugin(p)"
            @click.stop="openPluginPage(p)"
            :title="(p.status === 'running' || p.status === 'registered') ? t('pluginOpen') : t('pluginNotRunning')"
          >
            <ExternalLink :size="12" />
          </button>
          <button
            class="action-top-btn btn-reinstall-top"
            :disabled="operating.has(p.id)"
            @click.stop="onReinstall(p)"
            :title="t('pluginReinstallHint')"
          >
            <Loader2 v-if="operating.has(p.id)" :size="11" class="spin" />
            <RotateCcw v-else :size="11" />
          </button>
          <button
            class="action-top-btn btn-uninstall-top"
            :disabled="operating.has(p.id)"
            @click.stop="onUninstall(p)"
            :title="t('pluginUninstall')"
          >
            <Trash2 :size="11" />
          </button>
        </div>

        <!-- 第一层：图标 + 名称 + 右上角状态。原来「名称一行、版本用量状态又一行、
             权限标签一行、命令一行、描述两行」五层平铺，眼睛没有落点；现在固定成
             头（谁）→ 描述（干什么）→ 脚（版本/能力 + 唯一入口）三段。 -->
        <div class="card-head">
          <div class="card-icon-wrap">
            <span v-if="newFlags.has(p.id)" class="new-badge" :title="t('pluginNewBadge')">{{ t('pluginNewBadge') }}</span>
            <img
              v-if="icons[p.id]"
              :src="icons[p.id]"
              class="card-icon-img"
              alt=""
            />
            <Puzzle v-else :size="22" class="card-icon-fallback" />
          </div>
          <div
            class="card-name"
            @mouseenter="applyOverflowTitle($event, pluginName(p, locale))"
          >{{ pluginName(p, locale) }}</div>
          <span
            :class="['status-badge', statusBadgeClass(p)]"
            :title="statusLabel(p)"
          ><i class="badge-dot"></i>{{ statusLabel(p) }}</span>
        </div>

        <!-- 第二层：描述固定两行（无描述也占满两行高度），同一行卡片底部才齐 -->
        <p
          class="card-desc"
          @mouseenter="applyOverflowTitle($event, pluginDesc(p, locale))"
        >{{ pluginDesc(p, locale) }}</p>

        <!--
          命令与热键：卡片上露前两条，点开看完整清单（含参数声明）。
          取不到增强数据时 v-if 直接隐藏这一块，不抛错也不留空壳。
        -->
        <div v-key-click
          v-if="p.commands && p.commands.length"
          class="card-cmds"
          :title="cmdListTitle(p)"
          @click.stop="openCmdList(p)"
        >
          <!-- 卡片上只留「热键 + 命令标题」：触发方式（扫描/手动）是第三个文字块，
               在窄卡片里会把标题挤成单个字。触发方式并入 chip 的 title 提示。 -->
          <span
            v-for="c in p.commands.slice(0, 2)"
            :key="c.id"
            class="cmd-chip"
            :title="`[${triggerLabelFor(c)}] ${cmdTitle(c)}`"
          >
            <kbd v-if="c.hotkey" class="cmd-kbd">{{ c.hotkey }}</kbd>
            <span
              class="cmd-chip-title"
              @mouseenter="applyOverflowTitle($event, cmdTitle(c))"
            >{{ cmdTitle(c) }}</span>
          </span>
          <span v-if="p.commands.length > 2" class="cmd-more">+{{ p.commands.length - 2 }}</span>
        </div>

        <!-- 第三层（贴底）：版本 / 用量 / 分类 / 权限声明做成一行灰字纯文本，
             不再是一堆灰底小胶囊——那些胶囊把卡片切得很碎，谁也没读进去。
             整行超长时省略，完整内容在 title 里。 -->
        <div class="card-foot">
          <div class="card-tags" :title="cardTagsTitle(p)">
            <span class="card-version">v{{ p.version }}</span>
            <span v-if="p.usageCount > 0" class="card-usage">
              <Play :size="9" class="usage-icon" />
              <span class="usage-count">{{ p.usageCount }}</span>
            </span>
            <span v-if="p.category" class="tag-sep">·</span>
            <span v-if="p.category" class="tag-text">{{ p.category }}</span>
            <template v-if="permTags(p).length">
              <span class="tag-sep">·</span>
              <span class="tag-text tag-perms">{{ permTags(p).map(permLabel).join(' / ') }}</span>
            </template>
          </div>
          <!-- 唯一明确入口：可打开 → 打开；未启用 → 启用；只有命令 → 命令。
               以前卡片上没有任何按钮，用户只能猜「点哪里」。 -->
          <button
            v-if="ctaMap[p.id]"
            :class="['card-cta', { 'is-primary': ctaMap[p.id].kind === 'activate' }]"
            :disabled="operating.has(p.id)"
            :title="ctaMap[p.id].label"
            @click.stop="onCardCta(p)"
            @dblclick.stop
          >
            <Loader2 v-if="operating.has(p.id)" :size="11" class="spin" />
            <component v-else :is="ctaMap[p.id].icon" :size="11" />
            <span>{{ ctaMap[p.id].label }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- 卸载确认：可选「保留数据」，重装/迁移时不至于把插件积累一并抹掉 -->
    <ConfirmDialog
      :visible="showUninstallConfirm"
      :message="t('pluginUninstallConfirm', { name: uninstallingName })"
      :checkbox-label="t('pluginKeepData')"
      @update:checkbox="uninstallKeepData = $event"
      @confirm="handleUninstall"
      @cancel="showUninstallConfirm = false"
    />

    <!-- 执行历史（5.2） -->
    <div class="exec-history">
      <button class="history-head" @click="toggleHistory">
        <component :is="historyOpen ? ChevronDown : ChevronRight" :size="14" />
        <History :size="14" />
        <span>{{ t('pluginExecHistory') }}</span>
        <span v-if="execLogs.length" class="history-count">{{ execLogs.length }}</span>
        <span v-if="execLogs.some(l => !l.success)" class="history-err-dot" :title="t('pluginExecHistoryHasError')"></span>
      </button>

      <div v-if="historyOpen" class="history-body">
        <div v-if="loadingLogs" class="history-loading">{{ t('loading') }}</div>
        <!-- 失败态优先于空态：加载失败时绝不能落到「暂无执行记录」，否则用户会
             以为记录真的丢了。重试直接调 loadLogs（原加载函数）。 -->
        <div v-else-if="execLogsFailed" class="history-failed">
          <span>{{ t('pluginExecHistoryLoadFailed') }}</span>
          <button class="history-retry" @click="loadLogs()">{{ t('retry') }}</button>
        </div>
        <div v-else-if="execLogs.length === 0" class="history-empty">{{ t('pluginExecHistoryEmpty') }}</div>
        <div v-else class="history-list">
          <div v-key-click
            v-for="log in execLogs"
            :key="log.id"
            :class="['exec-row', { 'exec-fail': !log.success, 'exec-expanded': expandedLog === log.id }]"
            @click="toggleLogDetail(log.id)"
          >
            <component :is="log.success ? CheckCircle2 : XCircle" :size="13" :class="log.success ? 'row-ok' : 'row-err'" />
            <span class="row-plugin">{{ log.pluginId }}</span>
            <span class="row-cmd">{{ log.commandId }}</span>
            <span class="row-trigger" :title="triggerLabel(log.trigger)">{{ triggerLabel(log.trigger) }}</span>
            <span class="row-dur">{{ log.durationMs }}ms</span>
            <span class="row-time">{{ log.executedAt }}</span>
            <component :is="expandedLog === log.id ? ChevronDown : ChevronRight" :size="12" class="row-caret" />
          </div>
          <div v-for="log in execLogs" :key="'d-' + log.id">
            <div v-if="expandedLog === log.id" class="exec-detail">
              <div v-if="log.result" class="detail-block">
                <div class="detail-label">{{ t('pluginExecResult') }}</div>
                <pre class="detail-pre">{{ log.result }}</pre>
              </div>
              <div v-if="log.error" class="detail-block">
                <div class="detail-label detail-err">{{ t('error') }}</div>
                <pre class="detail-pre detail-pre-err">{{ log.error }}</pre>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
    </div>

    <template v-if="activeView === 'market'">
      <!-- 网络失败降级时明确标注「离线缓存」，不把旧数据冒充最新 -->
      <div v-if="marketOffline.offline" class="market-offline-banner">
        <span class="offline-tag">{{ t('pluginMarketOffline') }}</span>
        <span>{{ t('pluginMarketOfflineTip', { time: offlineTime }) }}</span>
      </div>
      <PluginMarketPage @installed="onMarketInstalled" />
    </template>

    <!--
      安装前权限确认：必须挂在根层而不是 local 视图里——市场视图下 local 块被
      v-if 卸载，装在里面的话市场安装/更新调用 confirmPermissions 会没有对话框。
      本地 zip 与市场安装共用这一个实例（usePluginPermissions）。
    -->
    <ConfirmDialog
      :visible="permDialog.visible"
      :message="t(permDialog.titleKey)"
      :details="permDialog.details"
      @confirm="resolvePermissionConfirm(true)"
      @cancel="resolvePermissionConfirm(false)"
    />

    <!-- 命令清单：卡片上放不下整张表，点开看完整 trigger / 热键 / 参数 -->
    <QdModal
      :model-value="cmdDialog.visible"
      :title="`${t('pluginCmdListTitle')} · ${cmdDialog.pluginName}`"
      width="520px"
      @update:model-value="(v: boolean) => (cmdDialog.visible = v)"
    >
      <div class="cmd-dialog-body">
          <div v-for="r in cmdDialog.rows" :key="r.id" class="cmd-row">
            <div class="cmd-row-top">
              <kbd v-if="r.hotkey" class="cmd-kbd">{{ r.hotkey }}</kbd>
              <span class="cmd-trigger">{{ t('trigger_' + r.trigger) }}</span>
              <span class="cmd-row-title">{{ r.title }}</span>
            </div>
            <div v-if="r.triggerHint" class="cmd-row-hint">{{ r.triggerHint }}</div>
            <div v-if="r.description" class="cmd-row-desc">{{ r.description }}</div>
            <div v-if="r.paramsText" class="cmd-row-params">
              <span class="cmd-row-label">{{ t('pluginCmdParams') }}</span>
              <code>{{ r.paramsText }}</code>
            </div>
          </div>
      </div>
    </QdModal>

    <!-- 市场源：增删 / 启停。保存后由刷新信号让市场页按新配置重拉索引 -->
    <QdModal
      :model-value="sourcesDialog"
      :title="t('pluginMarketSources')"
      width="460px"
      @update:model-value="(v: boolean) => v || closeSourcesDialog()"
    >
      <div class="sources-list">
          <div v-for="(s, i) in sourcesDraft" :key="s.id" class="source-row">
            <input v-model="s.enabled" type="checkbox" class="source-enabled" :disabled="savingSources" :title="t('pluginEnable')" />
            <div class="source-fields">
              <input v-if="!s.builtin" v-model="s.name" class="source-input" :placeholder="t('name')" :disabled="savingSources" />
              <span v-else class="source-builtin-name">{{ t('pluginMarketSourceDefault') }}</span>
              <input v-if="!s.builtin" v-model="s.url" class="source-input" :placeholder="t('pluginMarketSourceUrl')" :disabled="savingSources" />
            </div>
            <button v-if="!s.builtin" class="source-remove" @click="removeSource(i)" :title="t('delete')" :disabled="savingSources">
              <Trash2 :size="12" />
            </button>
          </div>
        </div>
      <template #footer>
        <button class="qd-btn qd-btn-ghost" @click="addSource" :disabled="savingSources"><span>{{ t('add') }}</span></button>
        <button class="qd-btn" @click="closeSourcesDialog" :disabled="savingSources"><span>{{ t('cancel') }}</span></button>
        <button class="qd-btn" @click="saveSources" :disabled="savingSources"><span>{{ t('save') }}</span></button>
      </template>
    </QdModal>

    <!-- zip 安装遮罩动画：覆盖「选择 zip → 安装完成」全过程 -->
    <div v-if="operating.has('_install_')" class="install-overlay">
      <div class="install-overlay-box">
        <Loader2 :size="28" class="install-overlay-spin" />
        <div class="install-overlay-text">{{ installProgress.total > 1 ? t('pluginInstallBatchProgress', { i: installProgress.done + 1, n: installProgress.total }) : t('pluginInstalling') }}</div>
      </div>
    </div>

    <!-- 拖拽投放提示：只在真正拖着文件时出现，告诉用户「松手即安装」。
         用独立 v-if 而不是 else-if：中间插入了若干弹窗，v-else-if 必须紧跟
         v-if 才会生效，装遮罩仍优先于拖放提示（同原来的 if/else 语义）。 -->
    <div v-if="dragOver && !operating.has('_install_')" class="drop-hint">
      <div class="drop-hint-box">
        <Upload :size="24" />
        <span>{{ t('pluginDropHint') }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 中间包装层：v-if="activeView==='local'" 的块级 div 会打断 flex 链，
   使 .plugin-grid 的 flex:1/overflow 失效（grid 撑满内容高度后被 overflow:hidden 裁掉）。
   这里把它也纳入 flex column，grid 才能获得受限高度并内部滚动。 */
/* 页头通栏后，内边距落到内容容器上（与监控页同一套做法） */
.plugin-local {
  flex: 1; min-height: 0;
  display: flex; flex-direction: column;
  overflow: hidden;
  padding: var(--space-5) var(--space-7) var(--space-6);
}

.plugin-page {
  flex: 1; display: flex; flex-direction: column;
  overflow: hidden;
  min-height: 0; /* flex 子项默认 min-height:auto 会按内容撑高，插件多时整页超出被裁、无法滚动 */
  position: relative; /* 安装遮罩绝对定位的参照 */
  background: var(--color-bg-primary);
}

/* ---- 头部：已统一到 styles/app.css 的 .qd-page-header ---- */
.plugin-count { font-size: var(--text-2xs); color: var(--color-text-muted); background: var(--color-bg-tertiary); padding: 1px 7px; border-radius: var(--radius-xs); }

/* ---- 加载 / 空状态 ---- */
.plugin-loading, .plugin-empty {
  flex: 1; display: flex; flex-direction: column;
  align-items: center; justify-content: center;
  gap: 10px; color: var(--color-text-muted);
}
/* 空状态图标：一根细线图标直接浮在深底上，容易被读成「没加载出来」。
   给一层方底 + 细边，它才是一个完整的标识。
   盒子固定 44（= .qd-empty-icon），图标一律 20：早先靠 padding + content-box
   "适配任意尺寸"，结果 28/30/36/48 各自撑成 48~68，同屏不同页的图标底托大小不一。 */
.empty-icon {
  width: 44px;
  height: 44px;
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: inset 0 1px 0 var(--color-hairline);
  margin-bottom: var(--space-5);
}
.empty-title { font-size: var(--text-md); font-weight: 600; color: var(--color-text-secondary); margin: 0; }
.empty-desc { font-size: var(--text-sm); margin: var(--space-3) 0 0; color: var(--color-text-muted); }

/* ---- 网格 ---- */
/* 最小列宽 210px：卡片改成「大图标 + 两行描述 + 底部一行元信息 + 右下角入口」后，
   158px 的宽度放不下这些内容（状态徽标会把名称挤成三四个字，底部一行也会全变省略号）。
   210px 在 1440 宽下约 6 列，仍比列表紧凑。 */
.plugin-grid {
  flex: 1; overflow-y: auto;
  min-height: 0; /* 同上：允许网格收缩到可视高度，超出才滚动 */
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: var(--space-4);
  align-content: start;
  padding: var(--space-1) 0;
}

/* ---- 插件卡片 ---- */
/* 三段式纵向布局：头（图标 + 名称 + 状态）／描述（固定两行）／脚（元信息 + 入口）。
   卡片按 grid 行等高拉伸，脚用 margin-top:auto 贴底，同一行卡片底边自然齐。 */
.plugin-card {
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  padding: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  text-align: left;
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast), transform var(--transition-fast);
  cursor: default;
  position: relative;
  /* 同市场页 .market-card：grid item 默认 min-width:auto(=min-content)，
     置 0 后卡片才允许收缩到列宽，内容约束（见 .card-desc）才能真正生效 */
  min-width: 0;
  /* 深色下卡片底色只比页面亮一点，静止时「贴着桌面」；hover 抬起 2px + 一圈
     更亮的边 + 一层柔和投影，才像一个能点的东西。 */
}
.plugin-card:hover {
  border-color: var(--color-border-light);
  box-shadow: var(--shadow-md);
  transform: translateY(-2px);
}
/* 运行中的绿边放在 hover 之后：悬停时边框跟着抬起变中性色，避免两种强调色打架 */
.plugin-card.card-running { border-color: var(--color-success-border); }
.plugin-card.card-running:hover { border-color: var(--color-success-border); }
/* 能双击打开的卡片给出可点光标；禁用 / 无界面 / 未就绪的保持默认光标 */
.plugin-card.card-openable { cursor: pointer; }
@media (prefers-reduced-motion: reduce) {
  /* 只保留颜色/阴影的过渡（状态反馈），位移仍由全局兜底关掉。
     必须 !important：style.css 的全局 reduced-motion 兜底也带 !important。 */
  .plugin-card { transition: border-color var(--transition-fast), box-shadow var(--transition-fast) !important; }
  .plugin-card:hover { transform: none; }
}

/* 「新」角标：贴在图标右上角，不占头部宽度 */
.new-badge {
  position: absolute; top: -5px; right: -5px;
  font-size: var(--text-2xs); line-height: 13px; font-weight: 700;
  padding: 0 4px; border-radius: var(--radius-sm);
  background: var(--color-accent); color: var(--color-accent-text);
  letter-spacing: 0.3px;
  box-shadow: 0 0 0 2px var(--color-card-bg);
}

/* 顶部操作悬停：和状态徽标同一个角，徽标淡出、图标组淡入 */
.card-actions-top {
  position: absolute; top: 6px; right: 6px;
  display: flex; gap: 2px; opacity: 0;
  padding: 2px; border-radius: var(--radius-sm);
  background: var(--color-card-bg);
  transition: opacity 120ms;
  z-index: 1;
}
/* 键盘用户 Tab 进按钮组时也要显形：只靠 :hover 的话 opacity:0 会把
   按钮连同全局 :focus-visible 焦点环一起藏掉，根本看不出落在哪个危险操作上。
   :focus-within 让焦点进入任意按钮时整组显形，焦点环（全局 button:focus-visible）
   随之可见；鼠标场景仍走 :hover。 */
.plugin-card:hover .card-actions-top,
.plugin-card:focus-within .card-actions-top { opacity: 1; }
/* 同一角落让位：hover / 键盘焦点进来时状态徽标淡出（仍占位，名称不会重排） */
.plugin-card:hover .status-badge,
.plugin-card:focus-within .status-badge { opacity: 0; }
/* 图标组比状态徽标宽（5 个 20px 按钮 ≈ 112px，徽标只有 50 左右），会压到名称
   尾部那一段。hover 时给名称补一段右内边距让它提前省略——比让按钮盖在字上干净，
   而且省略号本来就只影响被盖住的那几个字。 */
.plugin-card:hover .card-name,
.plugin-card:focus-within .card-name { padding-right: 48px; }
.action-top-btn {
  display: flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border-radius: var(--radius-xs);
  border: none; background: var(--color-bg-tertiary);
  color: var(--color-text-muted); cursor: pointer;
  font-size: var(--text-2xs); transition: background-color 100ms, color 100ms, border-color 100ms;
}
.action-top-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.action-top-btn.btn-stop-top:hover { color: var(--color-warning); }
.action-top-btn.btn-kill-top:hover { color: var(--color-danger); }
.action-top-btn.btn-open-top:hover { color: var(--color-accent); }
.action-top-btn.btn-reinstall-top:hover { color: var(--color-accent); }
.action-top-btn.btn-uninstall-top:hover { color: var(--color-danger); }

/* ---- 第一层：头 ---- */
.card-head { display: flex; align-items: center; gap: var(--space-3); min-width: 0; }

/* 图标：插件自带 PNG（各自有色），所以底板只做统一容器——同一个尺寸、
   同一层弱底、同一圈细边，一排卡片的图标才像一组。 */
.card-icon-wrap {
  position: relative;
  width: 38px; height: 38px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
}
.card-icon-img { width: 26px; height: 26px; object-fit: contain; }
.card-icon-fallback { color: var(--color-text-muted); }

/* 名称：独占头部剩余宽度，单行省略，悬停由 applyOverflowTitle 补全名 */
.card-name {
  flex: 1; min-width: 0;
  font-size: var(--text-md); font-weight: 600;
  color: var(--color-text-primary); line-height: 1.35;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}

/* 版本号：脚部那行灰字的一员 */
.card-version { font-size: var(--text-2xs); color: var(--color-text-muted); }

/* 状态：带头部右上，前置一个同色小圆点——纯文字色块在深底上偏弱，
   一个点就能让「绿=跑着 / 灰=停着」在余光里也能分辨。 */
.status-badge {
  flex-shrink: 0; display: inline-flex; align-items: center; gap: 4px;
  font-size: var(--text-2xs); padding: 0 var(--space-3);
  border-radius: var(--radius-md); font-weight: 500;
  line-height: 16px; white-space: nowrap;
  transition: opacity 120ms;
}
.badge-dot { width: 5px; height: 5px; border-radius: 50%; background: currentColor; flex-shrink: 0; }
/* 只有「真的在跑」的插件呼吸：静止插件闪动只会变成整页噪音 */
.badge-running .badge-dot,
.badge-starting .badge-dot { animation: badge-pulse 2.2s ease-in-out infinite; }
@keyframes badge-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
@media (prefers-reduced-motion: reduce) {
  .badge-running .badge-dot, .badge-starting .badge-dot { animation: none; }
}
.badge-running { background: var(--color-success-bg); color: var(--color-success); }
/* 未激活 / 已停止一律退回中性灰：它们是"没开"，不该和运行中抢视线 */
.badge-stopped { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
/* disabled 是中性「被关掉」，不该和 crashed 同为红色；红色只留给真正的崩溃 */
.badge-disabled { background: var(--color-bg-tertiary); color: var(--color-text-disabled); }
.badge-crashed { background: var(--color-danger-bg); color: var(--color-danger); }
.badge-registered { background: var(--color-accent-bg); color: var(--color-accent); }
.badge-starting { background: var(--color-warning-bg); color: var(--color-warning); }
.badge-unresponsive { background: var(--color-warning-bg); color: var(--color-warning); }

/* 命令与热键：卡片上只露前两条，完整清单在 title 里。
   热键用 kbd 的等宽感是刻意的 —— 它是「按键」而不是「标签」。 */
.card-cmds {
  display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap;
  max-width: 100%;
}
.cmd-chip {
  display: inline-flex; align-items: center; gap: var(--space-2);
  min-width: 0; max-width: 100%;
  font-size: var(--text-2xs);
  color: var(--color-text-secondary);
}
.cmd-kbd {
  padding: 0 var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  line-height: 14px;
  color: var(--color-text-secondary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  white-space: nowrap;
  /* 组合键（Ctrl+Alt+Shift+F12）比命令标题还长时，让它先让位并省略，
     而不是把后面的标题压成一个字 */
  flex: 0 1 auto;
  min-width: 0;
  max-width: 60%;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 标题吃掉 chip 的剩余宽度再省略，而不是固定 96px：
   热键（Ctrl+Alt+S）已经占掉半张卡时，固定宽度会把标题挤成单个字。
   宽度自适应后至少还能保留「扫描…」这种可读前缀。 */
.cmd-chip-title {
  flex: 1 1 auto;
  min-width: 0;
  max-width: 100%;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-text-muted);
}
.cmd-more {
  font-size: var(--text-2xs);
  line-height: 15px;
  padding: 0 var(--space-2);
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
}

/* 使用次数 */
.card-usage { display: inline-flex; align-items: center; gap: var(--space-1); font-size: var(--text-2xs); color: var(--color-text-muted); }
.usage-icon { font-size: var(--text-2xs); }
.usage-count { font-family: var(--font-mono); font-weight: 500; }

/* 描述：固定两行高度（14px × 1.45 × 2），无描述也占位，同一行卡片底部才齐。
   颜色用 secondary 而不是 muted：它是卡片里唯一的「说明」，读不清就等于没有。 */
.card-desc {
  margin: 0;
  font-size: var(--text-sm); color: var(--color-text-secondary);
  line-height: 1.45;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: calc(2 * 1.45em);
  /* 卡片是 flex item 时默认 min-width:auto(=min-content)：
     不约束 max-width，一个超长无空格词（英文描述/URL）就会把描述撑破列宽 */
  max-width: 100%;
  /* line-clamp 的省略号只加在行尾；单词本身超过一行宽时，先得让它能断行 */
  overflow-wrap: anywhere;
}

/* ---- 第三层：脚（元信息 + 唯一入口） ---- */
/* margin-top:auto 把它顶到卡片底部：同排卡片被 grid 拉成等高后，
   没有描述的卡片不会在下方留出一段空白再跟一个悬空按钮。 */
.card-foot {
  margin-top: auto;
  padding-top: var(--space-2);
  display: flex; align-items: center; gap: var(--space-3);
  min-width: 0;
}

/* 一行灰字：v1.0.8 · 分类 · 权限 / 权限。原来这些是四个灰底胶囊，在窄卡上
   把一行切成碎块；纯文本 + 点号既省空间，也让「版本」这一层退到后面。 */
.card-tags {
  flex: 1; min-width: 0;
  display: flex; align-items: center; gap: 5px;
  font-size: var(--text-2xs); color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden;
}
.card-tags > * { flex-shrink: 0; }
/* 版本号必须完整（它是最短也最常被找的一项）；分类 / 权限可让位并省略 */
.tag-perms { flex: 0 1 auto; overflow: hidden; text-overflow: ellipsis; }
.tag-sep { color: var(--color-text-muted); }
.tag-text { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--color-text-muted); }

/* 唯一入口：小胶囊按钮。未启用用主色（唯一需要用户动手的状态），
   其余是中性描边——一排卡片上不该有很多个蓝按钮。 */
.card-cta {
  display: inline-flex; align-items: center; gap: 4px;
  flex-shrink: 0; height: 22px; padding: 0 var(--space-3);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--text-2xs); font-family: inherit; font-weight: 500;
  cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast), color var(--transition-fast);
}
.card-cta:hover { background: var(--color-bg-tertiary); color: var(--color-text-primary); border-color: var(--color-accent); }
.card-cta.is-primary {
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border-strong);
  color: var(--color-accent);
}
.card-cta.is-primary:hover { background: var(--color-accent); border-color: transparent; color: var(--color-accent-text); }
.card-cta:disabled { opacity: var(--opacity-disabled-soft); cursor: default; }

/* ---- 分类 Tabs ---- */
/* 搜索框跟分类 tab 同一行靠右：装得多时先搜再筛，比先筛再翻更快 */
/* 搜索框跟分类同一行靠右：装得多时先搜再筛，比先筛再翻更快。
   皮肤由 QdSearchBox 统一提供，这里只定宽 + 靠右。 */
.plugin-search {
  margin-left: auto;
  width: 200px;
}

/* ---- 分类 Tabs（分段控制器） ---- */
/* 搜索框跟分类同一行靠右：装得多时先搜再筛，比先筛再翻更快。
   分类本身收进 .cat-track 一条灰轨道里——十几个分类平铺成一排小字时，
   选中项只靠一点淡蓝几乎找不到锚点，有轨道才像"一组单选"。 */
.plugin-categories {
  display: flex; align-items: center; gap: var(--space-4);
  margin-bottom: 10px; flex-shrink: 0;
  padding-bottom: var(--space-4);
  border-bottom: 1px solid var(--color-border);
}
.cat-track {
  flex: 1; min-width: 0;
  display: flex; align-items: center; flex-wrap: wrap; gap: 4px;
  padding: 3px;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.cat-tab {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 4px 10px; border-radius: var(--radius-sm);
  border: 1px solid transparent;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--text-xs); font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.cat-tab:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.cat-tab.active {
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border-strong);
  color: var(--color-accent);
  font-weight: 600;
}
.cat-count {
  font-size: var(--text-2xs); opacity: 0.6;
}

/* ---- 执行历史（5.2） ---- */
.exec-history {
  flex-shrink: 0;
  margin-top: var(--space-5);
  border-top: 1px solid var(--color-border);
  padding-top: var(--space-4);
}
.history-head {
  display: flex; align-items: center; gap: var(--space-3);
  width: 100%; padding: var(--space-2) var(--space-1);
  background: transparent; border: none; cursor: pointer;
  color: var(--color-text-secondary); font-size: var(--text-sm); font-family: inherit;
}
.history-head:hover { color: var(--color-text-primary); }
.history-count {
  font-size: var(--text-2xs); color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 0 var(--space-3); border-radius: var(--radius-md);
}
.history-err-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: var(--color-danger); margin-left: var(--space-1);
}
.history-body { margin-top: var(--space-3); }
.history-loading, .history-empty, .history-failed {
  font-size: var(--text-xs); color: var(--color-text-muted);
  padding: var(--space-4) var(--space-1);
}
.history-failed { display: flex; align-items: center; gap: var(--space-3); }
.history-retry {
  padding: 1px var(--space-3);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-accent);
  font-size: var(--text-xs); font-family: inherit; cursor: pointer;
}
.history-retry:hover { border-color: var(--color-accent); }
.history-list {
  max-height: 220px; overflow-y: auto;
  display: flex; flex-direction: column; gap: var(--space-1);
}
.exec-row {
  display: flex; align-items: center; gap: var(--space-4);
  padding: 5px var(--space-3); border-radius: var(--radius-sm);
  background: var(--color-card-bg);
  border: 1px solid transparent;
  cursor: pointer; font-size: var(--text-xs);
}
.exec-row:hover { background: var(--color-bg-hover); }
.exec-row.exec-fail { background: var(--color-danger-bg); }
.exec-row.exec-fail:hover { background: var(--color-danger-bg); }
.exec-row.exec-expanded { border-color: var(--color-border); }
.row-ok { color: var(--color-success); flex-shrink: 0; }
.row-err { color: var(--color-danger); flex-shrink: 0; }
.row-plugin { color: var(--color-text-primary); font-weight: 500; max-width: 130px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-cmd { color: var(--color-text-secondary); max-width: 110px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-trigger {
  font-size: var(--text-2xs); color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 0 var(--space-3); border-radius: var(--radius-md); flex-shrink: 0;
}
.row-dur { color: var(--color-text-muted); font-family: var(--font-mono); flex-shrink: 0; }
.row-time { color: var(--color-text-muted); font-size: var(--text-2xs); margin-left: auto; flex-shrink: 0; }
.row-caret { color: var(--color-text-muted); flex-shrink: 0; }
.exec-detail {
  padding: var(--space-3) var(--space-4) var(--space-4); margin: -2px 0 var(--space-1);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
}
.detail-block { margin-top: var(--space-3); }
.detail-block:first-child { margin-top: 0; }
.detail-label { font-size: var(--text-2xs); color: var(--color-text-muted); margin-bottom: 3px; }
.detail-label.detail-err { color: var(--color-danger); }
.detail-pre {
  font-family: var(--font-mono); font-size: var(--text-2xs);
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary); border-radius: var(--radius-xs);
  padding: var(--space-3) var(--space-4); margin: 0; max-height: 140px; overflow-y: auto;
  white-space: pre-wrap; word-break: break-all;
}
.detail-pre-err { color: var(--color-danger); }

/* 视图切换已改用 .qd-btn + .qd-btn.is-on（见 styles/app.css） */

/* ---- zip 安装遮罩动画 ---- */
.install-overlay {
  position: absolute; inset: 0; z-index: 60;
  display: flex; align-items: center; justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(1.5px);
}
.install-overlay-box {
  display: flex; flex-direction: column; align-items: center; gap: var(--space-5);
  padding: var(--space-8) 34px;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: 0 10px 32px var(--color-bg-overlay);
}
/* ---- 拖拽投放提示 ---- */
/* 虚线边框画在页面上而不是弹一层遮罩：遮罩会盖住插件列表，
   用户想确认「装到哪儿去了」时反而看不见；描边只提示，不挡内容。 */
.plugin-page.page-drag-over { outline: 2px dashed var(--color-accent); outline-offset: -4px; border-radius: var(--radius-md); }
.drop-hint {
  position: absolute; inset: 0; z-index: 55;
  display: flex; align-items: center; justify-content: center;
  pointer-events: none; /* 不能吃掉 drop 事件：提示层只负责显示 */
  /* 不用 color-mix()：它是 Chrome 111+ 才有的函数，宿主是用户机器上的
     WebView2，版本下限不受我们控制，失效时会静默变透明。 */
  background: var(--color-accent-bg);
}
.drop-hint-box {
  display: flex; flex-direction: column; align-items: center; gap: 10px;
  padding: var(--space-7) 30px;
  background: var(--color-card-bg);
  border: 1px dashed var(--color-accent);
  border-radius: var(--radius-md);
  color: var(--color-accent); font-size: var(--text-md);
  box-shadow: 0 8px 24px var(--color-bg-overlay);
}

/* 空状态可点：文案原本就写着「拖入此区域或点击安装」，之前点击是没反应的 */
.plugin-empty { cursor: pointer; }
.plugin-empty:hover .empty-title { color: var(--color-accent); }

.install-overlay-spin { color: var(--color-accent); animation: qd-spin 0.9s linear infinite; }
.install-overlay-text { font-size: var(--text-md); color: var(--color-text-secondary); }
.spin { color: var(--color-accent); animation: qd-spin 0.9s linear infinite; }
@keyframes qd-spin { to { transform: rotate(360deg); } }

/* ---- 离线缓存横幅：网络失败降级时必须显式标注，不能静默用旧数据 ---- */
.market-offline-banner {
  flex-shrink: 0;
  display: flex; align-items: center; gap: var(--space-4);
  margin: 0 var(--space-7) var(--space-4);
  padding: 7px var(--space-5);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-sm);
  background: var(--color-warning-bg);
  color: var(--color-warning);
  font-size: var(--text-xs);
}
.offline-tag {
  flex-shrink: 0; font-weight: 600;
  padding: 1px 7px; border-radius: var(--radius-xs);
  background: var(--color-warning-bg);
  border: 1px solid var(--color-warning-border);
}

/* ---- 本组件内的弹窗基类（不写全局 styles，避免影响其它页面） ---- */

/* ---- 命令清单 ---- */
.card-cmds { cursor: pointer; } /* 点开完整命令表 */
.cmd-dialog-body { overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-3); }
.cmd-row {
  padding: var(--space-4) 10px;
  border: 1px solid var(--card-border-color); border-radius: var(--radius-sm);
  background: var(--color-card-bg);
  display: flex; flex-direction: column; gap: var(--space-2);
}
.cmd-row-top { display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
.cmd-row-title { font-size: var(--text-sm); color: var(--color-text-primary); font-weight: 500; }
.cmd-trigger {
  font-size: var(--text-2xs); line-height: 15px; padding: 0 5px;
  border-radius: var(--radius-md);
  background: var(--color-accent-bg); color: var(--color-accent);
  white-space: nowrap;
}
.cmd-row-hint, .cmd-row-desc { font-size: var(--text-xs); color: var(--color-text-muted); overflow-wrap: anywhere; }
.cmd-row-hint { font-family: var(--font-mono); }
.cmd-row-params { display: flex; flex-direction: column; gap: 3px; }
.cmd-row-label { font-size: var(--text-2xs); color: var(--color-text-muted); }
.cmd-row-params code {
  font-family: var(--font-mono); font-size: var(--text-2xs);
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary); border-radius: var(--radius-xs);
  padding: 5px 7px; word-break: break-all; white-space: pre-wrap;
}

/* ---- 市场源编辑 ---- */
.sources-list { overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-3); }
.source-row {
  display: flex; align-items: flex-start; gap: var(--space-4);
  padding: var(--space-4); border: 1px solid var(--card-border-color); border-radius: var(--radius-sm);
  background: var(--color-card-bg);
}
.source-enabled { margin-top: var(--space-3); cursor: pointer; }
.source-fields { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 5px; }
.source-input {
  width: 100%; box-sizing: border-box;
  height: 26px; padding: 0 var(--space-4);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-primary); color: var(--color-text-primary);
  font-size: var(--text-xs); font-family: inherit; outline: none;
}
.source-input:focus { border-color: var(--color-border-focus-soft); }
.source-input::placeholder { color: var(--color-text-disabled); }
.source-builtin-name { font-size: var(--text-sm); font-weight: 500; color: var(--color-text-secondary); line-height: 26px; }
.source-remove {
  display: flex; align-items: center; justify-content: center;
  width: 26px; height: 26px; flex-shrink: 0;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-muted); cursor: pointer;
}
.source-remove:hover { color: var(--color-danger); border-color: var(--color-danger); }

</style>
