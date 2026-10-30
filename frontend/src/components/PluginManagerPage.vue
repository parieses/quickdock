<script setup lang="ts">
defineOptions({ name: 'PluginManagerPage' })
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Puzzle, Power, PowerOff, Trash2, RefreshCw, Upload, ExternalLink, History, ChevronDown, ChevronRight, CheckCircle2, XCircle, Globe, Square, Loader2, Search, DownloadCloud, X } from '@lucide/vue'

import {
  ListPlugins,
  DisablePlugin,
  EnablePlugin,
  UninstallPlugin,
  InstallPlugin,
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
import { useThemeName } from '../composables/useThemeName'
import type { ToastAPI, PluginInfo, PluginExecLog, PluginCommand } from '../types'

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!

const plugins = ref<PluginInfo[]>([])
const icons = ref<Record<string, string>>({}) // pluginId → data URI（当前主题那一档）
const loading = ref(true)
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
const loadingLogs = ref(false)
const historyOpen = ref(false)
const expandedLog = ref<string | null>(null)

// ---- 权限审计 ----
//
// 只在卡片上列"有声明"的项：Permissions 是 Go 结构体，零值（false / 空对象）也会被
// 序列化出来，不过滤就等于给每个插件挂一排假标签。
const PERM_KEYS = ['network', 'filesystem', 'shell', 'process', 'processKill', 'clipboard'] as const

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
// PluginInfo.commands 一直在下发，此前卡片上完全不提，导致「装了插件却不知道
// 它能干什么」。卡片宽度放不下整张命令表，所以：显示前两条（带热键），
// 完整清单塞进 title 提示 —— 一行一条「热键 · 标题」。
function cmdTitle(c: PluginCommand): string {
  return commandTitle(c, locale)
}

function cmdListTitle(p: PluginInfo): string {
  return (p.commands || [])
    .map((c) => (c.hotkey ? `${c.hotkey} · ${cmdTitle(c)}` : cmdTitle(c)))
    .join('\n')
}

async function loadLogs() {
  loadingLogs.value = true
  try {
    execLogs.value = (unwrap(await ListPluginExecLogs(100)) || []) as PluginExecLog[]
  } catch (e) {
    execLogs.value = []
  } finally {
    loadingLogs.value = false
  }
}
function toggleHistory() {
  historyOpen.value = !historyOpen.value
  if (historyOpen.value && execLogs.value.length === 0) loadLogs()
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
    toast.error(getErrorMessage(e))
  } finally {
    checkingUpdates.value = false
  }
}

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
    await loadIcons()
    // 加载「新」角标集合
    try {
      const flags = await GetPluginNewFlags()
      if (Array.isArray(flags)) newFlags.value = new Set(flags as string[])
    } catch (e) { logErr('PluginManagerPage', e) }
  } catch (e) {
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
    await KillPlugin(p.id)
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
      await DisablePlugin(p.id)
      // 后端语义：禁用 = disabled=true + status=stopped；两个字段都要同步本地，
      // 否则徽标/电源按钮会与真实状态错位（曾出现启用后仍显示「已禁用」）。
      p.disabled = true
      p.status = 'stopped'
    } else {
      await EnablePlugin(p.id)
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

// ---- 安装前权限二次确认 ----
//
// native 插件的危险调用运行期拦不住，唯一能拦的时机就是安装前：装之前把 zip 里
// plugin.json 声明的敏感能力（联网/读写文件/剪贴板/执行命令/杀进程）亮出来，
// 用户看清再点确认。预检失败的包不在此阻断——真正安装时后端还有完整校验兜底。
interface ZipPermPreview { name: string; version: string; permissions: string[] }

const permDialog = ref<{ visible: boolean; details: string[] }>({ visible: false, details: [] })
let permResolve: ((ok: boolean) => void) | null = null

function confirmInstallPermissions(details: string[]): Promise<boolean> {
  permDialog.value = { visible: true, details }
  return new Promise((resolve) => { permResolve = resolve })
}
function onPermConfirm() { permDialog.value.visible = false; permResolve?.(true); permResolve = null }
function onPermCancel() { permDialog.value.visible = false; permResolve?.(false); permResolve = null }

/** 逐包预检并汇总成确认框明细行。返回 null 表示全部预检失败（直接走安装，让后端报错）。 */
async function buildPermDetails<T>(
  items: T[],
  inspectOf: (item: T) => Promise<ZipPermPreview | null>,
  labelOf: (item: T) => string,
): Promise<string[] | null> {
  const lines: string[] = []
  let inspected = 0
  for (const it of items) {
    let prev: ZipPermPreview | null = null
    try { prev = await inspectOf(it) } catch { prev = null }
    if (!prev) continue
    inspected++
    lines.push(`${labelOf(it)} — ${prev.name} v${prev.version}`)
    if (prev.permissions.length) lines.push(...prev.permissions.map((p) => '  · ' + p))
    else lines.push(`  · ${t('pluginPermNone')}`)
  }
  return inspected ? lines : null
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
  // 安装前权限确认：亮出每个包声明的敏感能力，用户确认才继续
  const details = await buildPermDetails(
    paths,
    async (p) => unwrap<ZipPermPreview>(await InspectPluginZip(p)),
    zipName,
  )
  if (details && !(await confirmInstallPermissions(details))) return
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
  // 安装前权限确认（与「从文件安装」同一条规则）
  const details = await buildPermDetails(
    files,
    async (f) => unwrap<ZipPermPreview>(await InspectPluginZipData(f.name, b64.get(f) || '')),
    (f) => f.name,
  )
  if (details && !(await confirmInstallPermissions(details))) return
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

function openPluginPage(p: PluginInfo) {
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
onMounted(() => {
  loadPlugins(); loadLogs()
  statusTimer = window.setInterval(refreshStatuses, 3000)
})
onUnmounted(() => {
  if (statusTimer !== undefined) clearInterval(statusTimer)
  if (windowOpenTimer !== null) clearTimeout(windowOpenTimer)
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
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">
          <Puzzle :size="17" />
          {{ t('pluginManager') }}
          <span v-if="plugins.length" class="plugin-count">{{ plugins.length }}</span>
        </h2>
      </div>
      <div class="qd-page-actions">
        <button :class="['qd-btn', 'qd-btn-ghost', { 'is-on': activeView === 'market' }]" @click="activeView = activeView === 'local' ? 'market' : 'local'" :title="t('pluginMarket')">
          <Globe :size="14" />
          <span>{{ t('pluginMarket') }}</span>
        </button>
        <button class="qd-btn" @click="selectAndInstall" :title="t('pluginInstallFromFile')" :disabled="operating.has('_install_')">
          <Loader2 v-if="operating.has('_install_')" :size="14" class="spin" />
          <Upload v-else :size="14" />
          <span>{{ operating.has('_install_') ? t('pluginInstalling') : t('pluginInstallFromFile') }}</span>
        </button>
        <button class="qd-btn" :disabled="checkingUpdates" @click="checkUpdates" :title="t('pluginCheckUpdates')">
          <Loader2 v-if="checkingUpdates" :size="14" class="spin" />
          <DownloadCloud v-else :size="14" />
          <span>{{ checkingUpdates ? t('checking') : t('pluginCheckUpdates') }}</span>
        </button>
        <button class="qd-icon-btn" @click="reloadPlugins" :title="t('refresh')">
          <RefreshCw :size="14" />
        </button>
      </div>
    </div>

    <div v-if="activeView === 'local'" class="plugin-local">
    <!-- 分类 Tabs -->
    <div class="plugin-categories" v-if="plugins.length > 0">
      <button
        :class="['cat-tab', { active: selectedCategory === '' }]"
        @click="selectedCategory = ''"
      >全部 <span class="cat-count">{{ plugins.length }}</span></button>
      <button
        v-for="cat in categories"
        :key="cat"
        :class="['cat-tab', { active: selectedCategory === cat }]"
        @click="selectedCategory = cat"
      >{{ cat }} <span class="cat-count">{{ plugins.filter(p => p.category === cat).length }}</span></button>
      <div class="plugin-search">
        <Search :size="12" class="plugin-search-icon" />
        <input v-model="pluginQuery" class="plugin-search-input" :placeholder="t('pluginSearchPh')" />
        <button v-if="pluginQuery" class="plugin-search-clear" @click="pluginQuery = ''"><X :size="11" /></button>
      </div>
    </div>

    <!-- 加载中 -->
    <div v-if="loading" class="plugin-loading">
      <p>{{ t('loading') }}</p>
    </div>

    <!-- 空状态 -->
    <div v-else-if="plugins.length === 0" class="plugin-empty" @click="selectAndInstall">
      <Puzzle :size="48" class="empty-icon" />
      <p class="empty-title">{{ t('pluginNoPlugins') }}</p>
      <p class="empty-desc">{{ t('pluginDragToInstall') }}</p>
    </div>

    <!-- 九宫格插件列表 -->
    <div v-else-if="filteredPlugins.length === 0" class="plugin-empty">
      <Puzzle :size="48" class="empty-icon" />
      <p class="empty-title">{{ t('pluginEmptyCategory') }}</p>
    </div>

    <div v-else class="plugin-grid">
      <div
        v-for="p in filteredPlugins"
        :key="p.id"
        :class="['plugin-card', { 'card-running': p.status === 'running' }]"
      >
        <!-- 「新」角标 -->
        <span v-if="newFlags.has(p.id)" class="new-badge">新</span>

        <!-- 悬停操作按钮 -->
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
            :disabled="(p.status !== 'running' && p.status !== 'registered') || p.disabled || operating.has(p.id)"
            @click.stop="openPluginPage(p)"
            :title="(p.status === 'running' || p.status === 'registered') ? t('pluginOpen') : t('pluginNotRunning')"
          >
            <ExternalLink :size="12" />
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

        <!-- 图标区域 -->
        <div class="card-icon-wrap">
          <img
            v-if="icons[p.id]"
            :src="icons[p.id]"
            class="card-icon-img"
            alt=""
          />
          <Puzzle v-else :size="22" class="card-icon-fallback" />
        </div>

        <!-- 名称 -->
        <div class="card-name">{{ pluginName(p, locale) }}</div>

        <!-- 元信息行 -->
        <div class="card-meta">
          <span class="card-version">v{{ p.version }}</span>
          <span v-if="p.usageCount > 0" class="card-usage">
            <span class="usage-icon">▶</span>
            <span class="usage-count">{{ p.usageCount }}</span>
          </span>
        </div>

        <span :class="['status-badge', statusBadgeClass(p)]">{{ statusLabel(p) }}</span>

        <!-- 权限审计：装完之后仍能看到这个插件能碰什么，不必回去翻 plugin.json -->
        <div v-if="permTags(p).length" class="card-perms">
          <span v-for="tag in permTags(p)" :key="tag" class="perm-tag">{{ permLabel(tag) }}</span>
        </div>

        <!--
          命令与热键：PluginInfo.commands 一直在下发，但此前卡片上完全不提。
          后果是「装了插件却不知道它能干什么」——只能去命令面板里瞎搜，
          或者回去翻 plugin.json。热键尤其要露出来：它是唯一「不用先想起插件名」
          的入口。
        -->
        <div v-if="p.commands && p.commands.length" class="card-cmds" :title="cmdListTitle(p)">
          <span v-for="c in p.commands.slice(0, 2)" :key="c.id" class="cmd-chip">
            <kbd v-if="c.hotkey" class="cmd-kbd">{{ c.hotkey }}</kbd>
            <span class="cmd-chip-title">{{ cmdTitle(c) }}</span>
          </span>
          <span v-if="p.commands.length > 2" class="cmd-more">+{{ p.commands.length - 2 }}</span>
        </div>

        <!-- 描述：固定两行（.card-desc 的 line-clamp），超出的悬停看全文 -->
        <p
          v-if="p.description"
          class="card-desc"
          @mouseenter="applyOverflowTitle($event, pluginDesc(p, locale))"
        >{{ pluginDesc(p, locale) }}</p>
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

    <!-- 安装前权限确认 -->
    <ConfirmDialog
      :visible="permDialog.visible"
      :message="t('pluginPermConfirmTitle')"
      :details="permDialog.details"
      @confirm="onPermConfirm"
      @cancel="onPermCancel"
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
        <div v-else-if="execLogs.length === 0" class="history-empty">{{ t('pluginExecHistoryEmpty') }}</div>
        <div v-else class="history-list">
          <div
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

    <PluginMarketPage v-else @installed="onMarketInstalled" />

    <!-- zip 安装遮罩动画：覆盖「选择 zip → 安装完成」全过程 -->
    <div v-if="operating.has('_install_')" class="install-overlay">
      <div class="install-overlay-box">
        <Loader2 :size="28" class="install-overlay-spin" />
        <div class="install-overlay-text">{{ installProgress.total > 1 ? t('pluginInstallBatchProgress', { i: installProgress.done + 1, n: installProgress.total }) : t('pluginInstalling') }}</div>
      </div>
    </div>

    <!-- 拖拽投放提示：只在真正拖着文件时出现，告诉用户「松手即安装」 -->
    <div v-else-if="dragOver" class="drop-hint">
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
  padding: 12px 20px 16px;
}

.plugin-page {
  flex: 1; display: flex; flex-direction: column;
  overflow: hidden;
  min-height: 0; /* flex 子项默认 min-height:auto 会按内容撑高，插件多时整页超出被裁、无法滚动 */
  position: relative; /* 安装遮罩绝对定位的参照 */
  background: var(--color-bg-primary);
}

/* ---- 头部：已统一到 styles/app.css 的 .qd-page-header ---- */
.plugin-count { font-size: 10px; color: var(--color-text-muted); background: var(--color-bg-tertiary); padding: 1px 7px; border-radius: var(--radius-xs); }

/* ---- 加载 / 空状态 ---- */
.plugin-loading, .plugin-empty {
  flex: 1; display: flex; flex-direction: column;
  align-items: center; justify-content: center;
  gap: 10px; color: var(--color-text-disabled);
}
.empty-icon { opacity: 0.3; }
.empty-title { font-size: 13px; color: var(--color-text-muted); margin: 0; }
.empty-desc { font-size: 11px; margin: 0; color: var(--color-text-disabled); }

/* ---- 网格 ---- */
.plugin-grid {
  flex: 1; overflow-y: auto;
  min-height: 0; /* 同上：允许网格收缩到可视高度，超出才滚动 */
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
  gap: 8px;
  align-content: start;
  padding: 2px 0;
}

/* ---- 插件卡片 ---- */
.plugin-card {
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  padding: 10px;
  display: flex; flex-direction: column;
  align-items: center;
  gap: 3px;
  text-align: center;
  transition: border-color 150ms, box-shadow 150ms;
  cursor: default;
  position: relative;
  /* 同市场页 .market-card：grid item 默认 min-width:auto(=min-content)，
     置 0 后卡片才允许收缩到列宽，内容约束（见 .card-desc）才能真正生效 */
  min-width: 0;
}
.plugin-card:hover { border-color: var(--color-accent); box-shadow: 0 0 0 1px var(--color-accent-bg); }
.plugin-card.card-running { border-color: var(--color-success-border); }

/* 「新」角标 */
.new-badge {
  position: absolute; top: 4px; left: 4px;
  font-size: 9px; line-height: 14px; font-weight: 600;
  padding: 0 5px; border-radius: var(--radius-sm);
  background: var(--color-accent); color: var(--color-accent-text);
  z-index: 1; letter-spacing: 0.5px;
}

/* 顶部操作悬停 */
.card-actions-top {
  position: absolute; top: 4px; right: 4px;
  display: flex; gap: 2px; opacity: 0;
  transition: opacity 120ms;
}
.plugin-card:hover .card-actions-top { opacity: 1; }
.action-top-btn {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: var(--radius-xs);
  border: none; background: var(--color-bg-tertiary);
  color: var(--color-text-muted); cursor: pointer;
  font-size: 10px; transition: background-color 100ms, color 100ms, border-color 100ms;
}
.action-top-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.action-top-btn.btn-stop-top:hover { color: var(--color-warning); }
.action-top-btn.btn-kill-top:hover { color: var(--color-danger); }
.action-top-btn.btn-open-top:hover { color: var(--color-accent); }
.action-top-btn.btn-uninstall-top:hover { color: var(--color-danger); }

/* 图标 */
.card-icon-wrap {
  width: 36px; height: 36px;
  display: flex; align-items: center; justify-content: center;
  margin-bottom: 2px;
}
.card-icon-img { width: 36px; height: 36px; object-fit: contain; }
.card-icon-fallback { color: var(--color-text-muted); }

/* 名称 */
.card-name { font-size: 12px; font-weight: 500; color: var(--color-text-primary); line-height: 1.3; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }
.card-meta { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; justify-content: center; }
.card-version { font-size: 9px; color: var(--color-text-muted); }

/* 状态 */
.status-badge { font-size: 9px; padding: 0 6px; border-radius: var(--radius-md); font-weight: 500; line-height: 16px; }
.badge-running { background: var(--color-success-bg); color: var(--color-success); }
.badge-stopped { background: var(--color-border); color: var(--color-text-muted); }
.badge-disabled { background: var(--color-danger-bg); color: var(--color-danger); }
.badge-crashed { background: var(--color-danger-bg); color: var(--color-danger); }
.badge-registered { background: var(--color-accent-bg); color: var(--color-accent); }
.badge-starting { background: var(--color-warning-bg); color: var(--color-warning); }
.badge-unresponsive { background: var(--color-warning-bg); color: var(--color-warning); }

/* 权限标签：与状态徽标同尺寸，但用中性底 —— 权限是"事实"不是"状态"，
   给它们上色会和运行状态抢注意力 */
.card-perms { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; justify-content: center; }
.perm-tag {
  font-size: 9px;
  line-height: 15px;
  padding: 0 5px;
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 命令与热键：卡片上只露前两条，完整清单在 title 里。
   热键用 kbd 的等宽感是刻意的 —— 它是「按键」而不是「标签」。 */
.card-cmds {
  display: flex; align-items: center; gap: 4px; flex-wrap: wrap;
  justify-content: center;
  max-width: 100%;
}
.cmd-chip {
  display: inline-flex; align-items: center; gap: 4px;
  max-width: 100%;
  font-size: 9px;
  color: var(--color-text-secondary);
}
.cmd-kbd {
  padding: 0 4px;
  font-family: var(--font-mono, monospace);
  font-size: 9px;
  line-height: 14px;
  color: var(--color-text-secondary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  white-space: nowrap;
}
.cmd-chip-title {
  max-width: 96px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-text-muted);
}
.cmd-more {
  font-size: 9px;
  line-height: 15px;
  padding: 0 4px;
  border-radius: var(--radius-md);
  color: var(--color-text-disabled);
  background: var(--color-bg-tertiary);
}

/* 使用次数 */
.card-usage { display: inline-flex; align-items: center; gap: 2px; font-size: 9px; color: var(--color-text-muted); }
.usage-icon { font-size: 7px; }
.usage-count { font-family: var(--font-mono, monospace); font-weight: 500; }

/* 描述：固定两行高度（10px × 1.3 × 2），卡片按行对齐；超出部分由
   line-clamp 裁掉，鼠标移入时由 applyOverflowTitle 补上 title 看全文 */
.card-desc {
  font-size: 10px; color: var(--color-text-muted);
  margin: 1px 0 0; line-height: 1.3;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: 26px;
  /* 卡片是 flex column + align-items:center，item 宽度=内容宽而非容器宽：
     不约束 max-width，一个超长无空格词（英文描述/URL）就会把描述撑破卡片 */
  max-width: 100%;
  /* line-clamp 的省略号只加在行尾；单词本身超过一行宽时，先得让它能断行 */
  overflow-wrap: anywhere;
}

/* ---- 分类 Tabs ---- */
/* 搜索框跟分类 tab 同一行靠右：装得多时先搜再筛，比先筛再翻更快 */
.plugin-search {
  display: flex; align-items: center; gap: 5px; margin-left: auto;
  height: 26px; padding: 0 8px; min-width: 150px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.plugin-search:focus-within { border-color: var(--color-accent); }
.plugin-search-icon { color: var(--color-text-disabled); flex-shrink: 0; }
.plugin-search-input {
  flex: 1; min-width: 0; border: none; outline: none; background: none;
  color: var(--color-text-primary); font-size: 11px; font-family: inherit;
}
.plugin-search-input::placeholder { color: var(--color-text-disabled); }
.plugin-search-clear {
  display: flex; padding: 0; border: none; background: none;
  color: var(--color-text-disabled); cursor: pointer;
}
.plugin-search-clear:hover { color: var(--color-text-primary); }

.plugin-categories {
  display: flex; gap: 3px; flex-wrap: wrap;
  margin-bottom: 10px; flex-shrink: 0;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--color-border);
}
.cat-tab {
  display: inline-flex; align-items: center; gap: 3px;
  padding: 4px 10px; border-radius: var(--radius-sm);
  border: 1px solid transparent;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 11px; font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.cat-tab:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.cat-tab.active {
  background: var(--color-accent-bg);
  border-color: var(--color-accent);
  color: var(--color-accent);
}
.cat-count {
  font-size: 10px; opacity: 0.6;
}

/* ---- 执行历史（5.2） ---- */
.exec-history {
  flex-shrink: 0;
  margin-top: 12px;
  border-top: 1px solid var(--color-border);
  padding-top: 8px;
}
.history-head {
  display: flex; align-items: center; gap: 6px;
  width: 100%; padding: 4px 2px;
  background: transparent; border: none; cursor: pointer;
  color: var(--color-text-secondary); font-size: 12px; font-family: inherit;
}
.history-head:hover { color: var(--color-text-primary); }
.history-count {
  font-size: 10px; color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 0 6px; border-radius: var(--radius-md);
}
.history-err-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: var(--color-danger); margin-left: 2px;
}
.history-body { margin-top: 6px; }
.history-loading, .history-empty {
  font-size: 11px; color: var(--color-text-disabled);
  padding: 8px 2px;
}
.history-list {
  max-height: 220px; overflow-y: auto;
  display: flex; flex-direction: column; gap: 2px;
}
.exec-row {
  display: flex; align-items: center; gap: 8px;
  padding: 5px 6px; border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  border: 1px solid transparent;
  cursor: pointer; font-size: 11px;
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
  font-size: 10px; color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 0 6px; border-radius: var(--radius-md); flex-shrink: 0;
}
.row-dur { color: var(--color-text-muted); font-family: var(--font-mono, monospace); flex-shrink: 0; }
.row-time { color: var(--color-text-disabled); font-size: 10px; margin-left: auto; flex-shrink: 0; }
.row-caret { color: var(--color-text-disabled); flex-shrink: 0; }
.exec-detail {
  padding: 6px 8px 8px; margin: -2px 0 2px;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
}
.detail-block { margin-top: 6px; }
.detail-block:first-child { margin-top: 0; }
.detail-label { font-size: 10px; color: var(--color-text-muted); margin-bottom: 3px; }
.detail-label.detail-err { color: var(--color-danger); }
.detail-pre {
  font-family: var(--font-mono, monospace); font-size: 10px;
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary); border-radius: var(--radius-xs);
  padding: 6px 8px; margin: 0; max-height: 140px; overflow-y: auto;
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
  display: flex; flex-direction: column; align-items: center; gap: 12px;
  padding: 24px 34px;
  background: var(--color-bg-secondary, #1e1e1e);
  border: 1px solid var(--color-border, #2a2a2a);
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
  padding: 20px 30px;
  background: var(--color-bg-secondary);
  border: 1px dashed var(--color-accent);
  border-radius: var(--radius-md);
  color: var(--color-accent); font-size: 13px;
  box-shadow: 0 8px 24px var(--color-bg-overlay);
}

/* 空状态可点：文案原本就写着「拖入此区域或点击安装」，之前点击是没反应的 */
.plugin-empty { cursor: pointer; }
.plugin-empty:hover .empty-title { color: var(--color-accent); }

.install-overlay-spin { color: var(--color-accent); animation: qd-spin 0.9s linear infinite; }
.install-overlay-text { font-size: 13px; color: var(--color-text-secondary, #bdbdbd); }
.spin { color: var(--color-accent); animation: qd-spin 0.9s linear infinite; }
@keyframes qd-spin { to { transform: rotate(360deg); } }

</style>
