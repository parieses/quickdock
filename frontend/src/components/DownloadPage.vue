<script setup lang="ts">
/**
 * 下载器页面：QuickDock 只做「遥控器 + 仪表盘」。
 * 添加任务 → Go 门面转 JSON-RPC → aria2c 下载并直接写盘；
 * 进度走后端 1s 一次的整表快照事件（活跃任务快路径），页面切回来时再拉一次兜底。
 *
 * 覆盖 aria2 的主要能力：HTTP/FTP/SFTP、磁力、种子、Metalink、原生批量暂停、
 * 队列位置、URI 编辑、文件勾选、任务级选项、Peers / 服务器 / 全局统计 / 全局选项。
 */
import { ref, computed, watch, watchEffect, nextTick, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import {
  Download, RefreshCw, Settings2, Search, X, Pause, Play, Trash2, Copy,
  ClipboardPaste, Info, Folder, Clock, RotateCcw, Plus, AlertCircle,
  ChevronDown, ChevronUp, Gauge, Activity, Link, Timer,
  ArrowUp, ArrowDown, Magnet, FileArchive, Users,
  Shield, Zap, ExternalLink, Hash, Cpu, Radio, Check, FolderInput,
} from '@lucide/vue'
import {
  useDownloadTasks, formatBytes, formatSpeed, formatEta, formatTime, formatRatio,
} from '../composables/useDownloadTasks'
import type {
  DlTask, DlDetail, DlOptionsView, DlEngineInfo, DlProbeResult,
  DlImportItem, DlImportPreview, DlImportResult,
} from '../composables/useDownloadTasks'
import { useToast } from '../composables/useToast'
import { copyToClipboard } from '../utils/clipboard'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { ReadText } from '../../bindings/quickdock/services/clipboard/clipboardservice'

const { t } = useI18n()
const toast = useToast()
const {
  status, statusReady, options, tasks, busy, error, speedHistory, uploadHistory, global,
  refreshStatus, refreshTasks, refreshGlobal, enable, disable, add, addBatch, addTorrent, addMetalink, pickFile,
  pause, forcePause, resume, remove, pauseMany, resumeMany, removeMany,
  pauseAll, resumeAll, purgeResults, changePosition, changeUri,
  detailOf, setTaskOption, setFileSelection, globalOptions, setGlobalOption,
  engineInfo, shutdown, reveal, saveOptions, checkDisk, probeUrls, pickDir,
  scanImport, commitImport,
} = useDownloadTasks()

// ===== 新建任务（弹框） =====
type AddMode = 'url' | 'batch' | 'torrent' | 'metalink'
const addModes: AddMode[] = ['url', 'batch', 'torrent', 'metalink']
const addMode = ref<AddMode>('url')
const showAdd = ref(false)
const submitting = ref(false)
const urlInput = ref<HTMLInputElement | null>(null)
const batchInput = ref<HTMLTextAreaElement | null>(null)
const url = ref('')
const dir = ref('')
const batchText = ref('')
const filePath = ref('')
const advanced = ref(false)
const advForm = ref({
  split: 0, conn: 0, cont: true,
  out: '', selectFile: '', headers: '', referer: '', httpUser: '', httpPass: '',
  userAgent: '', proxy: '', checksum: '', downLimit: '', upLimit: '',
  seedRatio: '', seedTime: '', position: 0,
})
const diskSpace = ref<{ path: string; total: number; free: number; used: number } | null>(null)
let diskTimer: ReturnType<typeof setTimeout> | null = null
// 下载前探测：拿到远端真实大小后，才能判断目标盘能不能装下。
const probe = ref<DlProbeResult | null>(null)
const probing = ref(false)
const probeError = ref('')
const enoughSpace = computed(() => {
  if (status.value.remote) return true
  const need = probe.value?.total ?? 0
  if (need <= 0) return true
  const free = diskSpace.value?.free ?? 0
  return free <= 0 || free >= need
})

function clearProbe() { probe.value = null; probeError.value = '' }
// 链接或模式一变，旧的检测结果就不再适用，避免拿 A 链接的大小去判断 B 链接。
watch([url, batchText, addMode], clearProbe)

async function refreshDisk() {
  diskSpace.value = await checkDisk(dir.value.trim() || options.value.downloadDir)
}

function scheduleDiskRefresh() {
  if (diskTimer) clearTimeout(diskTimer)
  diskTimer = setTimeout(() => { void refreshDisk() }, 300)
}
watch(dir, scheduleDiskRefresh)

interface BatchLine { uri: string; dir: string }
interface BatchGroup { dir: string; uris: string[] }

// 批量行支持「链接 | 目录」：给了目录的下载单独存到那里，没给的用统一保存目录。
// Windows 路径不含 |，取第一个分隔符即可；同行重复链接只保留第一次。
function parseBatchLines(): BatchLine[] {
  const seen = new Set<string>()
  const out: BatchLine[] = []
  for (const line of batchText.value.split(/\r?\n/)) {
    const idx = line.indexOf('|')
    const uri = (idx < 0 ? line : line.slice(0, idx)).trim()
    const customDir = idx < 0 ? '' : line.slice(idx + 1).trim()
    if (!uri || !isSupportedUri(uri) || seen.has(uri)) continue
    seen.add(uri)
    out.push({ uri, dir: customDir })
  }
  return out
}

// 按目标目录分组，一次 DlBatchAdd 只处理同目录的一批链接。
function batchGroups(): BatchGroup[] {
  const fallback = dir.value.trim() || options.value.downloadDir
  const map = new Map<string, string[]>()
  for (const line of parseBatchLines()) {
    const d = line.dir || fallback
    map.set(d, [...(map.get(d) || []), line.uri])
  }
  return [...map].map(([d, uris]) => ({ dir: d, uris }))
}

async function chooseDir() {
  const p = await pickDir(t('dlPickDirTitle'))
  if (p) dir.value = p
}

async function confirmDiskSpace(): Promise<boolean> {
  // 远程引擎把文件写在远端，本机剩余空间说明不了问题，直接放行。
  if (status.value.remote) return true
  const mainDir = dir.value.trim() || options.value.downloadDir
  const sizeByUri = new Map<string, number>()
  for (const it of probe.value?.items ?? []) {
    if (it.size > 0) sizeByUri.set(it.url, it.size)
  }
  // 每行可以指定不同目录：按目录分别累加所需大小、分别查空间，
  // 否则会把 A 盘的文件算到 B 盘上，误报或漏报。
  const groups: { dir: string; need: number }[] = addMode.value === 'batch'
    ? batchGroups().map((g) => ({
        dir: g.dir,
        need: g.uris.reduce((sum, u) => sum + (sizeByUri.get(u) ?? 0), 0),
      }))
    : [{ dir: mainDir, need: probe.value?.total ?? 0 }]
  for (const g of groups) {
    const d = await checkDisk(g.dir)
    if (g.dir === mainDir) diskSpace.value = d
    if (!d || d.free <= 0) continue
    if (g.need > 0 && g.need > d.free) {
      return toast.confirm(t('dlProbeNotEnoughConfirm', {
        size: formatBytes(g.need), free: formatBytes(d.free),
      }))
    }
    if (d.free < 1024 ** 3) {
      return toast.confirm(t('dlDiskLowConfirm', { free: formatBytes(d.free) }))
    }
  }
  return true
}

// 探测只覆盖 http / https：磁力、种子、Metalink 的大小要等 aria2 拿到元数据才知道。
function probeUris(): string[] {
  const lines = addMode.value === 'batch'
    ? parseBatchLines().map((x) => x.uri)
    : [url.value.trim()]
  const valid = [...new Set(lines.filter(Boolean).filter(isSupportedUri))]
  return valid.filter((x) => /^https?:\/\//i.test(x))
}

async function doProbe() {
  if (probing.value) return
  const lines = addMode.value === 'batch'
    ? batchText.value.split(/\r?\n/).map((x) => x.split('|')[0].trim()).filter(Boolean)
    : (url.value.trim() ? [url.value.trim()] : [])
  const all = lines.filter(isSupportedUri)
  if (!all.length) { toast.error(t('dlProbeNoUrl')); return }
  const uris = probeUris()
  if (!uris.length) { toast.error(t('dlProbeUnsupported')); return }
  probing.value = true
  clearProbe()
  try {
    const r = await probeUrls(uris)
    if (!r) { probeError.value = t('dlProbeFailed', { error: t('dlProbeEmpty') }); return }
    probe.value = r
    await refreshDisk()
  } catch (e) {
    logErr('download/probe', e)
    toast.error(getErrorMessage(e))
    probeError.value = t('dlProbeFailed', { error: getErrorMessage(e) })
  } finally {
    probing.value = false
  }
}

// 提交前兜底探测：用户没手动点「检测大小」也要拿到真实大小，
// 否则 confirmDiskSpace 只能退回「盘快满了」的模糊提示，500G 的文件照样
// 会被加进只剩 92G 的盘，最后把 aria2 写盘卡死。
// 探测失败 / 协议不支持时静默放行，不阻断添加。
async function ensureProbe() {
  if (status.value.remote || probe.value) return
  const uris = probeUris()
  if (!uris.length) return
  probing.value = true
  try {
    const r = await probeUrls(uris)
    if (r) probe.value = r
  } catch (e) {
    logErr('download/probe', e)
  } finally {
    probing.value = false
  }
}

function modeLabel(m: AddMode): string {
  const map: Record<AddMode, string> = {
    url: 'dlModeUrl', batch: 'dlModeBatch', torrent: 'dlModeTorrent', metalink: 'dlModeMetalink',
  }
  return t(map[m])
}

function openAdd() {
  // 三种浮层互斥：打开新建时先收起右键菜单 / 详情 / 设置，避免叠在一起。
  ctx.value = null
  selectedId.value = ''
  showSettings.value = false
  showImport.value = false
  showAdd.value = true
  void focusAddInput()
  void refreshDisk()
}

function closeAdd() { showAdd.value = false; clearProbe() }

async function focusAddInput() {
  await nextTick()
  if (!showAdd.value) return
  if (addMode.value === 'url') urlInput.value?.focus()
  else if (addMode.value === 'batch') batchInput.value?.focus()
}

watch(addMode, () => { void focusAddInput() })

// ===== 设置弹框 =====
type SettingsTab = 'general' | 'bt' | 'auto' | 'connection' | 'engine'
const showSettings = ref(false)
const settingsTab = ref<SettingsTab>('general')
// 拷贝设置表单不能用 structuredClone：options 是 ref，取 .value 得到的是 Vue 的
// reactive proxy，而结构化克隆算法遇到 Proxy 一律抛 DataCloneError（点「下载设置」
// 直接报错，弹窗打不开）。这里字段全是原始类型，只有 categoryRules / schedule 两个
// 普通对象需要各自新建，浅展开就够了。
function cloneOptions(src: DlOptionsView): DlOptionsView {
  return {
    ...src,
    categoryRules: { ...(src.categoryRules || {}) },
    schedule: { ...(src.schedule || { enabled: false, start: '23:00', end: '07:00', downloadLimit: '', uploadLimit: '' }) },
  }
}
const settingsForm = ref<DlOptionsView>(cloneOptions(options.value))
// 后端用反向布尔 notifyDisabled（老配置零值 = 开启），UI 用正向复选框。
const notifyEnabled = computed({
  get: () => !settingsForm.value.notifyDisabled,
  set: (v: boolean) => { settingsForm.value.notifyDisabled = !v },
})
const categoryText = ref('')
const engine = ref<DlEngineInfo | null>(null)
const globalOpts = ref<Record<string, string>>({})
const globalKey = ref('max-overall-download-limit')
const globalVal = ref('')

// ===== 换电脑续传（导入） =====
// 会话文件给 URL，本机目录给 .aria2 / 数据文件；两者按文件名对上号后
// 用新的 dir 重新 addUri。所有可编辑字段提交前都要回传后端重新校验。
const showImport = ref(false)
const importDir = ref('')
const importSession = ref('')
const importPreview = ref<DlImportPreview | null>(null)
const importItems = ref<DlImportItem[]>([])
const importScanning = ref(false)
const importSubmitting = ref(false)
const importPaused = ref(false)
const importCheckIntegrity = ref(false)
const importResult = ref<DlImportResult | null>(null)
const importError = ref('')

const importCheckedCount = computed(() =>
  importItems.value.filter((x) => x.checked && x.name.trim() && x.uris[0]?.trim()).length)
const importAllChecked = computed(() =>
  importItems.value.length > 0 && importItems.value.every((x) => x.checked))
const importFailedActions = computed(() =>
  (importResult.value?.actions || []).filter((a) => !a.ok))
// 模板里不写可选链 + 非空断言：vue-tsc 对 v-if 的窄化不覆盖插值表达式。
const importRootDir = computed(() => importPreview.value?.rootDir || '')
const importLocalSession = computed(() => importPreview.value?.localSession || '')
const importIsRemote = computed(() => !!importPreview.value?.remote)
const importSummary = computed(() => {
  const p = importPreview.value
  return p ? t('dlImportScanSummary', { controls: p.controls, tasks: p.sessionTasks, count: p.items.length }) : ''
})

function openImport() {
  ctx.value = null
  selectedId.value = ''
  showAdd.value = false
  showSettings.value = false
  showImport.value = true
  importResult.value = null
  importError.value = ''
  if (!importDir.value) importDir.value = options.value.downloadDir || ''
  if (importItems.value.length === 0 && importDir.value.trim()) void runImportScan()
}

function closeImport() { showImport.value = false }

async function runImportScan() {
  if (importScanning.value) return
  if (!importDir.value.trim()) { importError.value = t('dlImportNeedDir'); return }
  importScanning.value = true
  importError.value = ''
  importResult.value = null
  try {
    const p = await scanImport(importDir.value.trim(), importSession.value.trim())
    if (!p) { importError.value = t('dlImportScanEmpty'); return }
    importPreview.value = p
    importDir.value = p.rootDir
    // 浅拷贝每一行：表格里的编辑不能改到上一次扫描的快照。
    // rowId 只在本次扫描结果内分配，作为表格 v-for 的稳定 key。
    let rowSeq = 0
    importItems.value = p.items.map((it) => ({
      ...it, rowId: `imp-${++rowSeq}`, uris: [...(it.uris || [])], options: { ...(it.options || {}) },
    }))
  } catch (e) {
    logErr('download/importScan', e)
    importError.value = getErrorMessage(e)
  } finally {
    importScanning.value = false
  }
}

async function chooseImportDir() {
  const p = await pickDir(t('dlImportPickFolder'))
  if (!p) return
  importDir.value = p
  await runImportScan()
}

async function chooseImportSession() {
  const p = await pickFile(t('dlImportPickSession'))
  if (!p) return
  importSession.value = p
  await runImportScan()
}

function useLocalSession() {
  const p = importPreview.value?.localSession
  if (!p) { importError.value = t('dlImportNeedDir'); return }
  importSession.value = p
  void runImportScan()
}

function clearImportSession() {
  importSession.value = ''
  void runImportScan()
}

// 多条镜像链接只露出第一条可改；编辑首条时保留其余镜像。
function onImportUriInput(i: number, e: Event) {
  const it = importItems.value[i]
  if (!it) return
  const v = (e.target as HTMLInputElement).value.trim()
  it.uris = v ? [v, ...it.uris.slice(1)] : it.uris.slice(1)
}

function toggleImportAll() {
  const next = !importAllChecked.value
  importItems.value.forEach((x) => { x.checked = next })
}

function importStateLabel(s: string): string {
  const map: Record<string, string> = {
    resume: 'dlImportStateResume', partial: 'dlImportStatePartial', fresh: 'dlImportStateFresh',
  }
  return t(map[s] || s)
}

function importReasonText(r: string): string {
  if (!r) return ''
  return r.startsWith('dlImportReason') ? t(r) : r
}

async function submitImport() {
  const items = importItems.value.filter((x) => x.checked && x.name.trim() && x.uris[0]?.trim())
  if (!items.length) { toast.error(t('dlImportNoSelection')); return }
  if (!status.value.running) { toast.error(t('dlEngineNotReady')); return }
  importSubmitting.value = true
  importError.value = ''
  try {
    const r = await commitImport(items, importPaused.value, importCheckIntegrity.value)
    if (!r) { importError.value = t('dlImportScanEmpty'); return }
    importResult.value = r
    if (r.added > 0) {
      toast.success(t('dlImportDone', { added: r.added, skipped: r.skipped }))
      await refreshTasks()
      if (r.skipped === 0) showImport.value = false
      else {
        // 成功的行从表里去掉，用户修好失败项后可以直接再提交一次。
        const okNames = new Set(r.actions.filter((a) => a.ok).map((a) => a.name))
        importItems.value = importItems.value.filter((x) => !okNames.has(x.name))
      }
    } else {
      toast.error(t('dlImportDone', { added: 0, skipped: r.skipped }))
    }
  } catch (e) {
    logErr('download/importCommit', e)
    toast.error(getErrorMessage(e))
    importError.value = getErrorMessage(e)
  } finally {
    importSubmitting.value = false
  }
}

// ===== 详情弹框 =====
type DetailTab = 'overview' | 'files' | 'uris' | 'peers' | 'servers' | 'options' | 'torrent'
const detailTab = ref<DetailTab>('overview')
const detail = ref<DlDetail | null>(null)
const detailLoading = ref(false)
const selectedFiles = ref<Set<number>>(new Set())
const taskOpts = ref({ down: '', up: '', seedRatio: '', peers: '' })
const newUri = ref('')

// ===== 分组 / 搜索 / 排序 / 选择 =====
type FilterKey = 'all' | 'active' | 'waiting' | 'paused' | 'complete' | 'stopped'
const filters: FilterKey[] = ['all', 'active', 'waiting', 'paused', 'complete', 'stopped']
const filter = ref<FilterKey>('all')
const query = ref('')
const sortKey = ref('default')
const sortDesc = ref(true)
const selectedIds = ref<Set<string>>(new Set())
const selectedId = ref('')
const selectAllEl = ref<HTMLInputElement | null>(null)
const ctx = ref<{ gid: string; x: number; y: number } | null>(null)

const counts = computed<Record<FilterKey, number>>(() => {
  const c = { all: tasks.value.length, active: 0, waiting: 0, paused: 0, complete: 0, stopped: 0 }
  for (const x of tasks.value) {
    if (x.status === 'error' || x.status === 'removed') c.stopped++
    else if (x.status === 'active' || x.status === 'waiting' || x.status === 'paused' || x.status === 'complete') c[x.status]++
  }
  return c
})

function filterLabel(k: FilterKey): string {
  const map: Record<FilterKey, string> = {
    all: 'dlFilterAll', active: 'dlStatusActive', waiting: 'dlStatusWaiting',
    paused: 'dlStatusPaused', complete: 'dlStatusComplete', stopped: 'dlFilterStopped',
  }
  return t(map[k])
}

function inFilter(task: DlTask, k: FilterKey): boolean {
  if (k === 'all') return true
  if (k === 'stopped') return task.status === 'error' || task.status === 'removed'
  return task.status === k
}

const visible = computed(() => {
  const q = query.value.trim().toLowerCase()
  const list = tasks.value.filter((x) => {
    if (!inFilter(x, filter.value)) return false
    if (!q) return true
    return [x.name, x.url, x.gid, x.path, x.infoHash].some((v) => (v || '').toLowerCase().includes(q))
  })
  const rank: Record<string, number> = {
    active: 0, waiting: 1, paused: 2, error: 3, complete: 4, removed: 5,
  }
  const key = sortKey.value
  return [...list].sort((a, b) => {
    if (key === 'default') return (rank[a.status] ?? 9) - (rank[b.status] ?? 9) || b.createdAt - a.createdAt
    let cmp = 0
    switch (key) {
      case 'name': cmp = a.name.localeCompare(b.name); break
      case 'progress': cmp = a.progress - b.progress; break
      case 'total': cmp = a.total - b.total; break
      case 'speed': cmp = a.speed - b.speed; break
      case 'createdAt': cmp = a.createdAt - b.createdAt; break
    }
    return sortDesc.value ? -cmp : cmp
  })
})

const stats = computed(() => {
  const list = tasks.value
  const speed = list.reduce((s, x) => s + (x.status === 'active' ? x.speed || 0 : 0), 0)
  const pending = list.filter((x) => x.total > 0 && (x.status === 'active' || x.status === 'waiting' || x.status === 'paused'))
  const done = pending.reduce((s, x) => s + x.completed, 0)
  const total = pending.reduce((s, x) => s + x.total, 0)
  return { speed, done, total, overall: total > 0 ? Math.min(100, (done / total) * 100) : 0 }
})

function sparkPoints(list: number[]): string {
  if (list.length < 2) return ''
  const max = Math.max(...list, 1)
  return list.map((v, i) => `${(i / (list.length - 1)) * 100},${30 - (v / max) * 26}`).join(' ')
}
const sparkDown = computed(() => sparkPoints(speedHistory.value))
const sparkUp = computed(() => sparkPoints(uploadHistory.value))
const sparkArea = computed(() => (sparkDown.value ? `0,30 ${sparkDown.value} 100,30` : ''))

const detailTask = computed(() => tasks.value.find((x) => x.gid === selectedId.value) || null)
const ctxTask = computed(() => tasks.value.find((x) => x.gid === ctx.value?.gid) || null)
const pickedName = computed(() => filePath.value.split(/[\\/]/).pop() || '')

const allChecked = computed(() => visible.value.length > 0 && visible.value.every((x) => selectedIds.value.has(x.gid)))
const someChecked = computed(() => visible.value.some((x) => selectedIds.value.has(x.gid)))
watchEffect(() => {
  if (selectAllEl.value) selectAllEl.value.indeterminate = someChecked.value && !allChecked.value
})

watch(tasks, (list) => {
  const alive = new Set(list.map((x) => x.gid))
  const kept = [...selectedIds.value].filter((g) => alive.has(g))
  if (kept.length !== selectedIds.value.size) selectedIds.value = new Set(kept)
  if (selectedId.value && !alive.has(selectedId.value)) selectedId.value = ''
})

function toggleAll() {
  const next = new Set(selectedIds.value)
  if (allChecked.value) visible.value.forEach((x) => next.delete(x.gid))
  else visible.value.forEach((x) => next.add(x.gid))
  selectedIds.value = next
}

function toggleCheck(gid: string) {
  const next = new Set(selectedIds.value)
  if (next.has(gid)) next.delete(gid)
  else next.add(gid)
  selectedIds.value = next
}

function clearSelection() { selectedIds.value = new Set() }
function closeDetail() { selectedId.value = '' }

// ===== 展示辅助 =====
function statusKey(s: string): string {
  const map: Record<string, string> = {
    active: 'dlStatusActive', waiting: 'dlStatusWaiting', paused: 'dlStatusPaused',
    complete: 'dlStatusComplete', error: 'dlStatusError', removed: 'dlStatusRemoved',
  }
  return t(map[s] || s)
}

function sourceLabel(s: string): string {
  if (s === 'manual') return t('dlSourceManual')
  if (s === 'batch') return t('dlSourceBatch')
  if (s === 'import') return t('dlSourceImport')
  if (s === 'torrent') return t('dlModeTorrent')
  if (s === 'metalink') return t('dlModeMetalink')
  return s || '-'
}

function forceFor(task: DlTask): boolean {
  return task.status !== 'complete' && task.status !== 'error' && task.status !== 'removed'
}

function etaFor(task: DlTask): string {
  if (task.status !== 'active' || !task.total || !task.speed) return formatEta(0, 0)
  return formatEta(Math.max(0, task.total - task.completed), task.speed)
}

const primaryLabel = computed(() => {
  if (!status.value.installed) return t('dlGoInstall')
  if (busy.value) return t('dlEnabling')
  if (status.value.remote) return t('dlConnReconnect')
  return t('dlStartEngine')
})

function goInstall() { Events.Emit('nav:goto', 'environments') }

function limitArg(v: string): string {
  const s = v.trim()
  if (!s || s === '0') return '0'
  return /^\d+$/.test(s) ? `${s}K` : s
}

// ===== 动作 =====
function selectedTasks(): DlTask[] {
  return tasks.value.filter((x) => selectedIds.value.has(x.gid))
}

async function bulkPause() {
  const gids = selectedTasks().filter((x) => x.status === 'active' || x.status === 'waiting').map((x) => x.gid)
  if (!gids.length) { toast.error(t('dlNoAction')); return }
  const ok = await pauseMany(gids)
  toast.success(t('dlBulkPaused', { count: ok }))
  clearSelection()
}

async function bulkResume() {
  const gids = selectedTasks().filter((x) => x.status === 'paused').map((x) => x.gid)
  if (!gids.length) { toast.error(t('dlNoAction')); return }
  const ok = await resumeMany(gids)
  toast.success(t('dlBulkResumed', { count: ok }))
  clearSelection()
}

async function bulkRemove() {
  const items = selectedTasks().map((x) => ({ gid: x.gid, force: forceFor(x) }))
  if (!items.length) { toast.error(t('dlNoAction')); return }
  const ok = await removeMany(items)
  if (ok < items.length) toast.error(t('dlBulkPartial', { ok, failed: items.length - ok }))
  else toast.success(t('dlBulkRemoved', { count: ok }))
  if (ok > 0) clearSelection()
}

async function doPauseAll() {
  const ok = await pauseAll(false)
  if (ok) toast.success(t('dlBulkPaused', { count: counts.value.active + counts.value.waiting }))
}

async function doResumeAll() {
  const ok = await resumeAll()
  if (ok) toast.success(t('dlBulkResumed', { count: counts.value.paused }))
}

async function clearFinished() {
  const n = counts.value.complete + counts.value.stopped
  if (!n) { toast.error(t('dlNoFinished')); return }
  if (!await toast.confirm(t('dlPurgeConfirm', { count: n }))) return
  const ok = await purgeResults()
  if (ok) toast.success(t('dlBulkRemoved', { count: n }))
  clearSelection()
}

async function moveTask(task: DlTask, delta: number) {
  await changePosition(task.gid, delta, 'POS_CUR')
}

async function copyField(text: string) {
  const ok = await copyToClipboard(text || '')
  if (ok) toast.success(t('dlCopied'))
  else toast.error(t('dlCopyFailed'))
}

async function openFolder(task: DlTask) {
  await reveal(task.path || task.dir)
}

async function retryTask(task: DlTask) {
  // 种子 / Metalink 的 URL 是本地文件路径，重试必须重新选文件，不能盲目再 addUri。
  if (task.source === 'torrent' || task.source === 'metalink') {
    toast.error(t('dlRetryFile')); return
  }
  const u = (task.url || '').trim()
  if (!isSupportedUri(u)) { toast.error(t('dlRetryNoUrl')); return }
  await add(u, task.dir || '', '')
}

// ===== 新建任务 =====
function isSupportedUri(u: string): boolean {
  const v = u.trim()
  if (/^magnet:\?/i.test(v)) return v.toLowerCase().includes('xt=urn:bt')
  return /^(https?|ftp|sftp):\/\/[^\s]+$/i.test(v)
}

function buildOpts(): string {
  if (!advanced.value || addMode.value === 'batch') return ''
  const a = advForm.value
  const o: Record<string, string> = {}
  if (a.split > 0) o.split = String(Math.min(64, Math.max(1, a.split)))
  if (a.conn > 0) o['max-connection-per-server'] = String(Math.min(64, Math.max(1, a.conn)))
  o.continue = a.cont ? 'true' : 'false'
  const put = (k: string, v: string) => { const s = v.trim(); if (s) o[k] = s }
  put('out', a.out)
  put('select-file', a.selectFile)
  put('referer', a.referer)
  put('http-user', a.httpUser)
  put('http-passwd', a.httpPass)
  put('user-agent', a.userAgent)
  put('all-proxy', a.proxy)
  put('checksum', a.checksum)
  put('seed-ratio', a.seedRatio)
  put('seed-time', a.seedTime)
  if (a.headers.trim()) {
    // aria2 1.37 实测：header 选项按 \n 拆成多个请求头（本地 HTTP 服务收到过两个自定义头）。
    o.header = a.headers.split(/\r?\n/).map((x) => x.trim()).filter(Boolean).join('\n')
  }
  if (a.downLimit.trim()) o['max-download-limit'] = limitArg(a.downLimit)
  if (a.upLimit.trim()) o['max-upload-limit'] = limitArg(a.upLimit)
  // position 不是 aria2 选项，是 addUri 的第 4 个参数；后端 takePosition 会摘出来。
  if (a.position > 0) o.__position = String(Math.min(65535, a.position))
  return JSON.stringify(o)
}

async function chooseFile() {
  const title = t(addMode.value === 'torrent' ? 'dlPickTorrent' : 'dlPickMetalink')
  const p = await pickFile(title)
  if (p) filePath.value = p
}

async function submit() {
  // 引擎没跑或上一次添加还在等 RPC 时忽略重复点击；失败时保留弹框和内容供修改。
  if (!status.value.running || submitting.value) return
  submitting.value = true
  try {
    await ensureProbe()
    if (!await confirmDiskSpace()) return
    let ok = false
    if (addMode.value === 'torrent' || addMode.value === 'metalink') {
      if (!filePath.value) { toast.error(t('dlNoFilePicked')); return }
      ok = addMode.value === 'torrent'
        ? await addTorrent(filePath.value, dir.value.trim(), buildOpts())
        : await addMetalink(filePath.value, dir.value.trim(), buildOpts())
    } else if (addMode.value === 'batch') {
      const raw = batchText.value.split(/\r?\n/).map((x) => x.trim()).filter(Boolean)
      const lines = parseBatchLines()
      if (!lines.length) { toast.error(t('dlInvalidUrls')); return }
      const skipped = raw.length - lines.length
      if (skipped > 0) toast.success(t('dlSkippedUrls', { count: skipped }))
      // 不同目录分组提交；成功提示与刷新统一做一次，避免多组弹多条 toast。
      ok = true
      for (const g of batchGroups()) {
        if (!await addBatch(g.uris, g.dir, true)) { ok = false; break }
      }
      if (ok) {
        toast.success(t('dlAdded'))
        await refreshTasks()
      }
    } else {
      if (!isSupportedUri(url.value)) { toast.error(t('dlInvalidUrl')); return }
      ok = await add(url.value.trim(), dir.value.trim(), buildOpts())
    }
    if (ok) {
      url.value = ''
      batchText.value = ''
      filePath.value = ''
      advanced.value = false
      clearProbe()
      showAdd.value = false
    }
  } finally {
    submitting.value = false
  }
}

async function pasteLinks() {
  try {
    const text = (unwrap(await ReadText()) as string) || ''
    const trimmed = text.trim()
    if (!trimmed) { toast.error(t('dlClipboardEmpty')); return }
    if (addMode.value === 'batch') batchText.value = batchText.value.trim() ? `${batchText.value.trim()}\n${trimmed}` : trimmed
    else url.value = trimmed.split(/\r?\n/)[0] || ''
  } catch (e) {
    logErr('download/paste', e)
    toast.error(getErrorMessage(e))
  }
}

// ===== 设置 =====
function openSettings() {
  showAdd.value = false
  showImport.value = false
  settingsForm.value = cloneOptions(options.value)
  categoryText.value = Object.entries(settingsForm.value.categoryRules).map(([k, v]) => `${k}=${v}`).join('\n')
  settingsTab.value = 'general'
  showSettings.value = true
  void loadEngine()
}

function resetOptions() {
  settingsForm.value = {
    ...settingsForm.value,
    maxConcurrent: 5, split: 8, downloadLimit: '', uploadLimit: '',
    listenPort: 6881, dhtListenPort: 6881, btTracker: '', btMaxPeers: 55,
    seedRatio: '0.0', seedTime: '0', minSplitSize: '', userAgent: '', proxy: '',
    disableDht: false, disableDht6: false, disablePeerExchange: false,
    disableSaveMetadata: false, disableFollowTorrent: false,
    btForceEncryption: false, btRequireCrypto: false,
    btRemoveUnselected: false, checkIntegrity: false,
    autoOpenFolder: false, autoCategory: false, perTaskFolder: false,
    categoryRules: {},
    schedule: { enabled: false, start: '23:00', end: '07:00', downloadLimit: '', uploadLimit: '' },
    remoteEnabled: false, remoteHost: '', remotePort: 6800, remoteSecret: '',
    remoteSecure: false, remotePath: '/jsonrpc', notifyDisabled: false,
  }
  // 空规则文本 = 恢复内置默认（后端 SaveConfig 的 applyDefaults 会补回默认表）。
  categoryText.value = ''
}

function parseCategoryText(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of categoryText.value.split(/\r?\n/)) {
    const i = line.indexOf('=')
    if (i <= 0) continue
    const k = line.slice(0, i).trim().toLowerCase().replace(/^\./, '')
    const v = line.slice(i + 1).trim()
    if (k && v) out[k] = v
  }
  return out
}

async function submitSettings() {
  // 始终下发规则文本：清空后提交也要让「恢复推荐值」真正生效。
  settingsForm.value.categoryRules = parseCategoryText()
  if (await saveOptions(settingsForm.value)) showSettings.value = false
}

async function loadEngine() {
  engine.value = await engineInfo()
  globalOpts.value = await globalOptions()
}

async function applyGlobalOption() {
  if (!globalKey.value) return
  const ok = await setGlobalOption({ [globalKey.value]: globalVal.value.trim() })
  if (ok) {
    globalVal.value = ''
    await loadEngine()
  }
}

async function doShutdown(force: boolean) {
  if (!await toast.confirm(t('dlEngineShutdownConfirm'))) return
  await shutdown(force)
  showSettings.value = false
}

// ===== 详情 =====
watch(selectedId, async (gid) => {
  ctx.value = null
  if (!gid) { detail.value = null; return }
  detailTab.value = 'overview'
  await loadDetail()
})

async function loadDetail() {
  if (!selectedId.value) return
  detailLoading.value = true
  const d = await detailOf(selectedId.value)
  detail.value = d
  if (d) {
    selectedFiles.value = new Set(d.files.filter((f) => f.selected).map((f) => f.index))
    const o = d.options || {}
    taskOpts.value = {
      down: o['max-download-limit'] || '',
      up: o['max-upload-limit'] || '',
      seedRatio: o['seed-ratio'] || '',
      peers: o['bt-max-peers'] || '',
    }
  }
  detailLoading.value = false
}

function toggleFile(index: number) {
  const next = new Set(selectedFiles.value)
  if (next.has(index)) next.delete(index)
  else next.add(index)
  selectedFiles.value = next
}

function toggleAllFiles(on: boolean) {
  selectedFiles.value = on ? new Set((detail.value?.files || []).map((f) => f.index)) : new Set()
}

async function saveSelection() {
  if (!detailTask.value) return
  const ok = await setFileSelection(detailTask.value.gid, [...selectedFiles.value])
  if (ok) await loadDetail()
}

async function saveTaskOptions() {
  if (!detailTask.value) return
  const o: Record<string, string> = {}
  if (taskOpts.value.down.trim()) o['max-download-limit'] = limitArg(taskOpts.value.down)
  if (taskOpts.value.up.trim()) o['max-upload-limit'] = limitArg(taskOpts.value.up)
  if (taskOpts.value.seedRatio.trim()) o['seed-ratio'] = taskOpts.value.seedRatio.trim()
  if (taskOpts.value.peers.trim()) o['bt-max-peers'] = taskOpts.value.peers.trim()
  if (!Object.keys(o).length) { toast.error(t('dlNoAction')); return }
  const ok = await setTaskOption(detailTask.value.gid, o)
  if (ok) await loadDetail()
}

async function addTaskUri() {
  if (!detailTask.value || !newUri.value.trim()) return
  const ok = await changeUri(detailTask.value.gid, 1, [], [newUri.value.trim()])
  if (ok) { newUri.value = ''; await loadDetail() }
}

async function delTaskUri(uri: string) {
  if (!detailTask.value) return
  const ok = await changeUri(detailTask.value.gid, 1, [uri], [])
  if (ok) await loadDetail()
}

// ===== 右键菜单 =====
function closeCtx() { ctx.value = null }

function openCtx(e: MouseEvent, task: DlTask) {
  const host = (e.currentTarget as HTMLElement | null)?.closest('.dl-page') as HTMLElement | null
  const rect = host?.getBoundingClientRect()
  const x = Math.max(0, e.clientX - (rect?.left ?? 0))
  const y = Math.max(0, e.clientY - (rect?.top ?? 0))
  ctx.value = {
    gid: task.gid,
    x: Math.min(x, Math.max(0, (rect?.width ?? 0) - 190)),
    y: Math.min(y, Math.max(0, (rect?.height ?? 0) - 370)),
  }
}

function ctxShowDetail() {
  if (ctx.value) selectedId.value = ctx.value.gid
  closeCtx()
}
function ctxCopy(usePath: boolean) {
  const task = ctxTask.value
  if (task) void copyField(usePath ? (task.path || task.dir) : task.url)
  closeCtx()
}
function ctxCopyHash() {
  const task = ctxTask.value
  if (task?.infoHash) void copyField(task.infoHash)
  closeCtx()
}
function ctxPause(force: boolean) {
  const task = ctxTask.value
  if (task) void (force ? forcePause(task.gid) : pause(task.gid))
  closeCtx()
}
function ctxResume() {
  const task = ctxTask.value
  if (task) void resume(task.gid)
  closeCtx()
}
function ctxRetry() {
  const task = ctxTask.value
  if (task) void retryTask(task)
  closeCtx()
}
function ctxRemove() {
  const task = ctxTask.value
  if (task) void remove(task.gid, forceFor(task))
  closeCtx()
}
function ctxMove(delta: number) {
  const task = ctxTask.value
  if (task) void moveTask(task, delta)
  closeCtx()
}
function ctxReveal() {
  const task = ctxTask.value
  if (task) void openFolder(task)
  closeCtx()
}

function onKeydown(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  if (e.key === 'Escape') {
    ctx.value = null
    if (showImport.value) showImport.value = false
    else if (showAdd.value) showAdd.value = false
    else if (showSettings.value) showSettings.value = false
    else if (selectedId.value) selectedId.value = ''
  }
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown)
  if (diskTimer) clearTimeout(diskTimer)
})
</script>

<template>
  <div class="qd-page dl-page" @click="closeCtx">
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title"><Download :size="16" />{{ t('dlTitle') }}</h2>
        <span class="qd-page-sub">{{ t('dlSubtitle') }}</span>
      </div>
      <div class="qd-page-fill">
        <span class="dl-engine" :class="status.running ? 'is-on' : 'is-off'">
          <span class="dl-dot"></span>
          <span v-if="status.remote" class="dl-badge" :title="status.host">{{ t('dlRemoteBadge') }}</span>
          <span v-if="!statusReady">{{ t('dlChecking') }}</span>
          <span v-else-if="status.running">{{ t('dlEngineOn', { version: status.version, port: status.port }) }}</span>
          <span v-else-if="status.installed">{{ t('dlInstalledOff') }}</span>
          <span v-else>{{ t('dlNotInstalled') }}</span>
        </span>
      </div>
      <div v-if="statusReady && status.installed" class="qd-page-actions">
        <button
          class="qd-btn qd-btn-primary" :disabled="!status.running"
          :title="status.running ? t('dlAddTitle') : t('dlEngineNotReady')" @click="openAdd"
        ><Plus :size="14" />{{ t('dlAddTitle') }}</button>
        <button
          class="qd-btn qd-btn-ghost" :disabled="!status.running"
          :title="status.running ? t('dlImportTitle') : t('dlEngineNotReady')" @click="openImport"
        ><FolderInput :size="14" />{{ t('dlImportTitle') }}</button>
        <span class="qd-action-sep"></span>
        <div class="qd-action-group">
          <button class="qd-btn qd-btn-ghost" @click="openSettings">
            <Settings2 :size="14" />{{ t('dlOptions') }}
          </button>
          <button class="qd-icon-btn" :title="t('dlRefresh')" @click="refreshTasks">
            <RefreshCw :size="14" />
          </button>
        </div>
        <span class="qd-action-sep"></span>
        <button v-if="status.running" class="qd-btn qd-btn-danger" :disabled="busy" @click="disable">
          {{ status.remote ? t('dlConnDisconnect') : t('dlDisable') }}
        </button>
        <button v-else class="qd-btn qd-btn-primary" :disabled="busy" @click="enable">{{ primaryLabel }}</button>
      </div>
    </div>

    <div class="qd-page-body dl-page-body">
      <div v-if="error && statusReady" class="dl-error">{{ error }}</div>

      <!-- 首次 DlStatus() 还没回来时不能按默认 false 渲染「未安装」：
           下载页不进 KeepAlive，切走即销毁重建，这一帧曾被误报成引擎没装。 -->
      <div v-if="!statusReady" class="qd-card dl-setup">
        <div class="dl-setup-icon is-loading"><RefreshCw :size="20" /></div>
        <h3>{{ error ? t('dlStatusFailed') : t('dlChecking') }}</h3>
        <p>{{ error || t('dlCheckingHint') }}</p>
        <button v-if="error" class="qd-btn qd-btn-primary" @click="refreshStatus">{{ t('dlRefresh') }}</button>
      </div>

      <div v-else-if="!status.installed" class="qd-card dl-setup">
        <div class="dl-setup-icon"><Download :size="20" /></div>
        <h3>{{ t('dlNotInstalled') }}</h3>
        <p>{{ t('dlNotInstalledDesc') }}</p>
        <button class="qd-btn qd-btn-primary" :disabled="busy" @click="goInstall">{{ t('dlGoInstall') }}</button>
        <p class="dl-license">{{ t('dlLicense') }}</p>
      </div>

      <template v-else>
        <div v-if="!status.running" class="qd-card dl-notice">
          <AlertCircle :size="16" class="dl-notice-icon" />
          <div class="dl-notice-text">
            <span>{{ t('dlEngineNotReady') }}</span>
            <p>{{ t('dlNotRunningHint') }}</p>
          </div>
        </div>

        <div class="qd-card dl-stats">
          <div class="dl-stat is-speed">
            <span class="dl-stat-label"><Activity :size="14" />{{ t('dlStatSpeed') }}</span>
            <strong class="dl-stat-value">{{ formatSpeed(stats.speed) }}</strong>
          </div>
          <div class="dl-stat is-up">
            <span class="dl-stat-label"><ArrowUp :size="14" />{{ t('dlStatUpload') }}</span>
            <strong class="dl-stat-value">{{ formatSpeed(global.uploadSpeed) }}</strong>
          </div>
          <div class="dl-stat"><span class="dl-stat-label">{{ t('dlStatusActive') }}</span><strong class="dl-stat-value">{{ counts.active }}</strong></div>
          <div class="dl-stat"><span class="dl-stat-label">{{ t('dlStatusWaiting') }}</span><strong class="dl-stat-value">{{ counts.waiting }}</strong></div>
          <div class="dl-stat"><span class="dl-stat-label">{{ t('dlStatusPaused') }}</span><strong class="dl-stat-value">{{ counts.paused }}</strong></div>
          <div class="dl-stat"><span class="dl-stat-label">{{ t('dlStatusComplete') }}</span><strong class="dl-stat-value">{{ counts.complete }}</strong></div>
          <div class="dl-stat dl-stat-spark">
            <span class="dl-stat-label">{{ t('dlStatSpeedHint') }}</span>
            <svg class="dl-spark-svg" viewBox="0 0 100 30" preserveAspectRatio="none">
              <polygon v-if="sparkArea" :points="sparkArea" class="dl-spark-area" />
              <polyline v-if="sparkDown" :points="sparkDown" class="dl-spark-line" />
              <polyline v-if="sparkUp" :points="sparkUp" class="dl-spark-line is-up" />
            </svg>
          </div>
          <div class="dl-stat dl-stat-overall">
            <span class="dl-stat-label">{{ t('dlStatOverall') }} · {{ t('dlStatTotalBytes', { done: formatBytes(stats.done), total: formatBytes(stats.total) }) }}</span>
            <div class="dl-bar">
              <!-- 进度条宽度是运行时计算值，按 R7 例外就近说明 -->
              <div class="dl-bar-fill" :style="{ width: stats.overall + '%' }"></div>
            </div>
          </div>
        </div>

        <div class="dl-workspace">
          <div class="dl-main">
            <div class="dl-tabs">
              <button v-for="k in filters" :key="k" class="dl-tab" :class="{ 'is-on': filter === k }" @click="filter = k">
                {{ filterLabel(k) }}<span class="dl-tab-count">{{ counts[k] }}</span>
              </button>
            </div>

            <div class="dl-toolbar">
              <label class="dl-check-all">
                <input ref="selectAllEl" type="checkbox" class="dl-check" :checked="allChecked" :disabled="!visible.length" @change="toggleAll" />
                <span>{{ t('dlSelectAll') }}</span>
              </label>
              <div class="qd-input-wrap dl-search">
                <Search :size="14" />
                <input v-model="query" class="qd-input" :placeholder="t('dlSearchPlaceholder')" />
                <button v-if="query" class="qd-icon-btn dl-search-clear" :title="t('dlClearSearch')" @click="query = ''"><X :size="14" /></button>
              </div>
              <select v-model="sortKey" class="qd-input dl-sort" :aria-label="t('dlSortLabel')">
                <option value="default">{{ t('dlSortDefault') }}</option>
                <option value="createdAt">{{ t('dlSortTime') }}</option>
                <option value="name">{{ t('dlSortName') }}</option>
                <option value="progress">{{ t('dlSortProgress') }}</option>
                <option value="total">{{ t('dlSortSize') }}</option>
                <option value="speed">{{ t('dlSortSpeed') }}</option>
              </select>
              <button class="qd-icon-btn" :disabled="sortKey === 'default'" :title="sortDesc ? t('dlSortDesc') : t('dlSortAsc')" @click="sortDesc = !sortDesc">
                <ChevronDown v-if="sortDesc" :size="14" /><ChevronUp v-else :size="14" />
              </button>
              <span class="dl-grow"></span>
              <span class="dl-count">{{ t('dlTaskCount', { count: visible.length }) }}</span>
              <button class="qd-btn qd-btn-sm" @click="doPauseAll"><Pause :size="14" />{{ t('dlPauseAll') }}</button>
              <button class="qd-btn qd-btn-sm" @click="doResumeAll"><Play :size="14" />{{ t('dlResumeAll') }}</button>
              <button class="qd-btn qd-btn-sm" @click="clearFinished"><Trash2 :size="14" />{{ t('dlPurgeResults') }}</button>
              <button class="qd-icon-btn" :title="t('dlRefreshGlobal')" @click="refreshGlobal"><RefreshCw :size="14" /></button>
            </div>

            <div v-if="selectedIds.size" class="dl-bulk">
              <span>{{ t('dlSelected', { count: selectedIds.size }) }}</span>
              <span class="dl-grow"></span>
              <button class="qd-btn qd-btn-sm" @click="bulkPause"><Pause :size="14" />{{ t('dlBulkPause') }}</button>
              <button class="qd-btn qd-btn-sm" @click="bulkResume"><Play :size="14" />{{ t('dlBulkResume') }}</button>
              <button class="qd-btn qd-btn-sm qd-btn-danger" @click="bulkRemove"><Trash2 :size="14" />{{ t('dlBulkRemove') }}</button>
              <button class="qd-icon-btn" :title="t('dlClearSelection')" @click="clearSelection"><X :size="14" /></button>
            </div>

            <div v-if="!visible.length" class="qd-card dl-empty">
              <div class="dl-empty-icon"><Search :size="20" /></div>
              <p>{{ tasks.length ? t('dlEmptyFiltered') : t('dlEmpty') }}</p>
              <span>{{ tasks.length ? t('dlEmptyFilteredHint') : t('dlEmptyHint') }}</span>
              <button v-if="!tasks.length && status.running" class="qd-btn qd-btn-primary qd-btn-sm" @click="openAdd">
                <Plus :size="14" />{{ t('dlAddTitle') }}
              </button>
            </div>

            <div v-else class="dl-list">
              <div
                v-for="task in visible" :key="task.gid" class="qd-card dl-item"
                :class="{ 'is-current': selectedId === task.gid, 'is-checked': selectedIds.has(task.gid) }"
                @click="selectedId = task.gid" @contextmenu.prevent="openCtx($event, task)"
              >
                <div class="dl-item-head">
                  <input type="checkbox" class="dl-check" :checked="selectedIds.has(task.gid)" :aria-label="task.name" @click.stop @change.stop="toggleCheck(task.gid)" />
                  <span class="dl-name" :title="task.url">{{ task.name }}</span>
                  <span v-if="task.isTorrent" class="dl-badge" :title="t('dlBtBadge')"><Magnet :size="12" />BT</span>
                  <span class="dl-tag" :class="'is-' + task.status">{{ statusKey(task.status) }}</span>
                </div>
                <div class="dl-bar">
                  <!-- 进度条宽度是运行时计算值，按 R7 例外就近说明 -->
                  <div class="dl-bar-fill" :style="{ width: Math.min(100, Math.max(0, task.progress)) + '%' }"></div>
                </div>
                <div class="dl-item-meta">
                  <span>{{ formatBytes(task.completed) }} / {{ formatBytes(task.total) }}</span>
                  <span>{{ task.progress.toFixed(1) }}%</span>
                  <span v-if="task.status === 'active'"><Gauge :size="14" />{{ formatSpeed(task.speed) }}</span>
                  <span v-if="task.status === 'active'"><Timer :size="14" />{{ etaFor(task) }}</span>
                  <span v-if="task.isTorrent && task.status === 'active'"><ArrowUp :size="14" />{{ formatSpeed(task.uploadSpeed) }}</span>
                  <span v-if="task.isTorrent && task.connections"><Users :size="14" />{{ task.connections }} / {{ task.seeders }}</span>
                  <span><Clock :size="14" />{{ formatTime(task.createdAt) }}</span>
                  <span v-if="task.error" class="dl-err-text">{{ task.error }}</span>
                  <span class="dl-grow"></span>
                  <button v-if="task.status === 'active' || task.status === 'waiting'" class="qd-btn qd-btn-sm" @click.stop="pause(task.gid)">{{ t('dlPause') }}</button>
                  <button v-if="task.status === 'paused'" class="qd-btn qd-btn-sm" @click.stop="resume(task.gid)">{{ t('dlResume') }}</button>
                  <button class="qd-icon-btn" :title="t('dlMoveUp')" @click.stop="moveTask(task, -1)"><ArrowUp :size="14" /></button>
                  <button class="qd-icon-btn" :title="t('dlMoveDown')" @click.stop="moveTask(task, 1)"><ArrowDown :size="14" /></button>
                  <button class="qd-icon-btn" :title="t('dlOpenFolder')" @click.stop="openFolder(task)"><Folder :size="14" /></button>
                  <button v-if="task.status === 'error'" class="qd-btn qd-btn-sm" @click.stop="retryTask(task)"><RotateCcw :size="14" />{{ t('dlRetry') }}</button>
                  <button class="qd-btn qd-btn-sm qd-btn-danger" @click.stop="remove(task.gid, forceFor(task))">{{ t('dlRemove') }}</button>
                  <button class="qd-icon-btn" :title="t('dlDetail')" @click.stop="selectedId = task.gid"><Info :size="14" /></button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>

    <!-- 右键菜单坐标是运行时算出来的，按 R7 例外就近说明 -->
    <div v-if="ctx" class="dl-ctx" :style="{ left: ctx.x + 'px', top: ctx.y + 'px' }" @click.stop>
      <button class="dl-ctx-item" @click="ctxShowDetail"><Info :size="14" />{{ t('dlCtxDetail') }}</button>
      <button class="dl-ctx-item" @click="ctxCopy(false)"><Link :size="14" />{{ t('dlCtxCopyLink') }}</button>
      <button class="dl-ctx-item" @click="ctxCopy(true)"><Folder :size="14" />{{ t('dlCtxCopyPath') }}</button>
      <button class="dl-ctx-item" @click="ctxReveal"><ExternalLink :size="14" />{{ t('dlOpenFolder') }}</button>
      <button v-if="ctxTask?.infoHash" class="dl-ctx-item" @click="ctxCopyHash"><Hash :size="14" />{{ t('dlCopyInfoHash') }}</button>
      <div class="dl-ctx-sep"></div>
      <button v-if="ctxTask?.status === 'active' || ctxTask?.status === 'waiting'" class="dl-ctx-item" @click="ctxPause(false)"><Pause :size="14" />{{ t('dlPause') }}</button>
      <button v-if="ctxTask?.status === 'active'" class="dl-ctx-item" @click="ctxPause(true)"><Shield :size="14" />{{ t('dlForcePause') }}</button>
      <button v-if="ctxTask?.status === 'paused'" class="dl-ctx-item" @click="ctxResume"><Play :size="14" />{{ t('dlResume') }}</button>
      <button v-if="ctxTask?.status === 'error'" class="dl-ctx-item" @click="ctxRetry"><RotateCcw :size="14" />{{ t('dlRetry') }}</button>
      <button class="dl-ctx-item" @click="ctxMove(-1)"><ArrowUp :size="14" />{{ t('dlMoveUp') }}</button>
      <button class="dl-ctx-item" @click="ctxMove(1)"><ArrowDown :size="14" />{{ t('dlMoveDown') }}</button>
      <div class="dl-ctx-sep"></div>
      <button class="dl-ctx-item is-danger" @click="ctxRemove"><Trash2 :size="14" />{{ t('dlRemove') }}</button>
    </div>

    <!-- 换电脑续传：旧会话给 URL，本机目录给 .aria2 / 数据文件 -->
    <Teleport to="body">
      <Transition name="dl-modal">
        <div v-if="showImport" class="dl-modal-overlay" @mousedown.self="closeImport">
          <div class="dl-modal dl-modal-import" role="dialog" :aria-label="t('dlImportTitle')">
            <header class="dl-modal-head">
              <FolderInput :size="15" class="dl-modal-head-icon" />
              <span class="dl-modal-title">{{ t('dlImportTitle') }}</span>
              <span class="dl-modal-sub">{{ importRootDir }}</span>
              <button class="qd-icon-btn" :title="t('dlClose')" @click="closeImport"><X :size="14" /></button>
            </header>

            <div class="dl-modal-body">
              <section class="dl-modal-section">
                <h4>{{ t('dlImportSectionPrepare') }}</h4>
                <p class="dl-hint">{{ t('dlImportIntro') }}</p>
                <p class="dl-hint">{{ t('dlImportSessionHint', { path: importLocalSession }) }}</p>
                <p v-if="importIsRemote" class="dl-hint is-warn">{{ t('dlImportRemoteHint') }}</p>
              </section>

              <section class="dl-modal-section">
                <h4>{{ t('dlImportSectionSource') }}</h4>
                <label class="dl-field">
                  <span>{{ t('dlImportDir') }}</span>
                  <div class="dl-add-input-row">
                    <input v-model="importDir" class="qd-input" :placeholder="t('dlImportDirPlaceholder')" @keydown.enter="runImportScan" />
                    <button class="qd-btn qd-btn-sm dl-row-btn" :title="status.remote ? t('dlPickDirRemoteHint') : t('dlBrowseDir')" @click="chooseImportDir">
                      <Folder :size="14" />{{ t('dlBrowseDir') }}
                    </button>
                  </div>
                </label>
                <label class="dl-field">
                  <span>{{ t('dlImportSession') }}</span>
                  <div class="dl-add-input-row">
                    <input v-model="importSession" class="qd-input" :placeholder="t('dlImportSessionPlaceholder')" @keydown.enter="runImportScan" />
                    <button class="qd-btn qd-btn-sm dl-row-btn" @click="chooseImportSession">
                      <FileArchive :size="14" />{{ t('dlImportPickSession') }}
                    </button>
                  </div>
                </label>
                <div class="dl-table-tools">
                  <button class="qd-btn qd-btn-primary qd-btn-sm" :disabled="importScanning || !importDir.trim()" @click="runImportScan">
                    <RefreshCw :size="14" />{{ importScanning ? t('dlImportScanning') : t('dlImportScan') }}
                  </button>
                  <button class="qd-btn qd-btn-sm" :disabled="importScanning" @click="useLocalSession">{{ t('dlImportUseLocalSession') }}</button>
                  <button class="qd-btn qd-btn-sm" :disabled="importScanning || !importSession" @click="clearImportSession">{{ t('dlImportClearSession') }}</button>
                  <span class="dl-grow"></span>
                  <span v-if="importSummary" class="dl-hint">{{ importSummary }}</span>
                </div>
                <p v-if="importError" class="dl-hint is-warn">{{ importError }}</p>
              </section>

              <section class="dl-modal-section">
                <h4>{{ t('dlImportSectionTasks') }}</h4>
                <div v-if="!importItems.length" class="dl-table-empty">{{ t('dlImportEmpty') }}</div>
                <div v-else class="dl-import-scroll">
                  <table class="dl-table dl-import-table">
                    <thead>
                      <tr>
                        <th class="is-check">
                          <input type="checkbox" class="dl-check" :checked="importAllChecked" :disabled="!importItems.length" :aria-label="t('dlSelectAll')" @change="toggleImportAll" />
                        </th>
                        <th>{{ t('dlImportColName') }}</th>
                        <th>{{ t('dlImportColUrl') }}</th>
                        <th>{{ t('dlImportColDir') }}</th>
                        <th>{{ t('dlImportColSize') }}</th>
                        <th>{{ t('dlImportColState') }}</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="(it, i) in importItems" :key="it.rowId" :class="{ 'is-off': !it.checked }">
                        <td><input v-model="it.checked" type="checkbox" class="dl-check" :aria-label="it.name" /></td>
                        <td>
                          <input v-model="it.name" class="qd-input dl-import-input" :aria-label="t('dlImportColName')" />
                          <div v-if="importReasonText(it.reason)" class="dl-import-note">{{ importReasonText(it.reason) }}</div>
                        </td>
                        <td>
                          <input
                            class="qd-input dl-import-input" :value="it.uris[0] || ''"
                            :placeholder="t('dlImportUrlPlaceholder')" :aria-label="t('dlImportColUrl')"
                            @input="onImportUriInput(i, $event)"
                          />
                          <div v-if="it.uris.length > 1" class="dl-import-note">{{ t('dlImportMirrors', { count: it.uris.length - 1 }) }}</div>
                        </td>
                        <td><input v-model="it.dir" class="qd-input dl-import-input" :title="it.dir" :aria-label="t('dlImportColDir')" /></td>
                        <td class="is-nowrap">{{ it.size > 0 ? formatBytes(it.size) : '-' }}</td>
                        <td><span class="dl-import-state" :class="'is-' + it.state">{{ importStateLabel(it.state) }}</span></td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <div class="dl-import-options">
                  <label class="dl-check-field"><input v-model="importCheckIntegrity" type="checkbox" class="dl-check" /><span>{{ t('dlImportCheckIntegrity') }}</span></label>
                  <label class="dl-check-field"><input v-model="importPaused" type="checkbox" class="dl-check" /><span>{{ t('dlImportPaused') }}</span></label>
                </div>
                <p class="dl-hint">{{ t('dlImportOptionsHint') }}</p>
                <div v-if="importResult" class="dl-import-result">
                  <p class="dl-hint">{{ t('dlImportResult', { added: importResult.added, skipped: importResult.skipped }) }}</p>
                  <p v-for="(a, j) in importFailedActions" :key="j" class="dl-hint is-warn">{{ a.name }}：{{ a.error }}</p>
                </div>
              </section>
            </div>

            <footer class="dl-modal-foot">
              <span class="dl-grow"></span>
              <button class="qd-btn qd-btn-sm" @click="closeImport">{{ t('cancel') }}</button>
              <button
                class="qd-btn qd-btn-primary qd-btn-sm"
                :disabled="!status.running || importSubmitting || !importCheckedCount"
                @click="submitImport"
              ><FolderInput :size="14" />{{ t('dlImportSubmit', { count: importCheckedCount }) }}</button>
            </footer>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- 新建下载弹框：与详情 / 设置共用同一套遮罩与面层 -->
    <Teleport to="body">
      <Transition name="dl-modal">
        <div v-if="showAdd" class="dl-modal-overlay" @mousedown.self="closeAdd">
          <div class="dl-modal dl-modal-wide" role="dialog" :aria-label="t('dlAddTitle')">
            <header class="dl-modal-head">
              <Plus :size="15" class="dl-modal-head-icon" />
              <span class="dl-modal-title">{{ t('dlAddTitle') }}</span>
              <span class="dl-modal-sub">{{ status.version }}</span>
              <button class="qd-icon-btn" :title="t('dlClose')" @click="closeAdd"><X :size="14" /></button>
            </header>

            <div class="dl-modal-tabs">
              <button
                v-for="m in addModes" :key="m" class="dl-tab" :class="{ 'is-on': addMode === m }"
                @click="addMode = m"
              >{{ modeLabel(m) }}</button>
              <span class="dl-grow"></span>
              <button
                class="qd-btn qd-btn-sm" :class="{ 'is-on': advanced }" :disabled="addMode === 'batch'"
                @click="advanced = !advanced"
              ><Settings2 :size="14" />{{ t('dlAdvanced') }}</button>
            </div>

            <div class="dl-modal-body">
              <div class="dl-modal-section">
                <h4>{{ t('dlAddSectionSource') }}</h4>
                <div v-if="addMode === 'url'" class="dl-add-input-row">
                  <input
                    ref="urlInput" v-model="url" class="qd-input" :placeholder="t('dlUrlPlaceholder')"
                    @keydown.enter="submit"
                  />
                  <button class="qd-icon-btn" :title="t('dlPaste')" @click="pasteLinks"><ClipboardPaste :size="14" /></button>
                  <button class="qd-btn qd-btn-sm dl-row-btn" :disabled="probing" @click="doProbe">
                    <Search :size="14" />{{ probing ? t('dlProbing') : t('dlProbe') }}
                  </button>
                </div>
                <template v-else-if="addMode === 'batch'">
                  <textarea
                    ref="batchInput" v-model="batchText" class="qd-input dl-add-textarea"
                    :placeholder="t('dlBatchPlaceholder')" @keydown.ctrl.enter="submit"
                  ></textarea>
                  <p class="dl-hint">{{ t('dlBatchDirHint') }}</p>
                  <div class="dl-add-tools">
                    <button class="qd-btn qd-btn-sm" :disabled="probing" @click="doProbe">
                      <Search :size="14" />{{ probing ? t('dlProbing') : t('dlProbe') }}
                    </button>
                    <button class="qd-btn qd-btn-sm" @click="pasteLinks">
                      <ClipboardPaste :size="14" />{{ t('dlPaste') }}
                    </button>
                  </div>
                </template>
                <div v-else class="dl-add-file-row">
                  <button class="qd-btn" @click="chooseFile">
                    <FileArchive :size="14" />{{ t(addMode === 'torrent' ? 'dlPickTorrent' : 'dlPickMetalink') }}
                  </button>
                  <span class="dl-picked" :title="filePath">{{ pickedName || t('dlNoFilePicked') }}</span>
                </div>
              </div>

              <div class="dl-modal-section">
                <h4>{{ t('dlAddSectionTarget') }}</h4>
                <label class="dl-field">
                  <span>{{ t('dlAddDir') }}</span>
                  <div class="dl-add-input-row">
                    <input v-model="dir" class="qd-input" :placeholder="t('dlDirPlaceholder')" />
                    <button
                      class="qd-btn qd-btn-sm dl-row-btn"
                      :title="status.remote ? t('dlPickDirRemoteHint') : t('dlBrowseDir')"
                      @click="chooseDir"
                    >
                      <Folder :size="14" />{{ t('dlBrowseDir') }}
                    </button>
                  </div>
                </label>
                <p class="dl-hint">{{ t('dlAddDirHint') }}</p>
                <p v-if="options.perTaskFolder" class="dl-hint">{{ t('dlPerTaskFolderOn') }}</p>
                <p v-else class="dl-hint">{{ t('dlPerTaskFolderOff') }}</p>
                <p v-if="diskSpace" class="dl-hint">
                  {{ t('dlDiskSpace', { free: formatBytes(diskSpace.free), total: formatBytes(diskSpace.total) }) }}
                </p>
                <p v-else-if="status.remote" class="dl-hint">{{ t('dlDiskRemoteHint') }}</p>
                <template v-if="probe">
                  <p class="dl-hint">
                    {{ t('dlProbeResult', {
                      size: probe.total > 0 ? formatBytes(probe.total) : t('dlProbeUnknownSize'),
                      count: probe.items.length,
                    }) }}
                  </p>
                  <p v-if="status.remote" class="dl-hint">{{ t('dlProbeRemote') }}</p>
                  <p v-else-if="probe.total > 0 && diskSpace" class="dl-hint" :class="{ 'is-warn': !enoughSpace }">
                    {{ t(enoughSpace ? 'dlProbeEnough' : 'dlProbeNotEnough', {
                      size: formatBytes(probe.total), free: formatBytes(diskSpace.free),
                    }) }}
                  </p>
                  <p v-if="probe.unknown" class="dl-hint">
                    {{ t('dlProbeUnknown', { count: probe.unknown }) }}
                  </p>
                </template>
                <p v-if="probeError" class="dl-hint is-warn">{{ probeError }}</p>
              </div>

              <div v-if="advanced && addMode !== 'batch'" class="dl-modal-section">
                <h4>{{ t('dlAdvanced') }}</h4>
                <div class="dl-settings-grid">
                  <label class="dl-field"><span>{{ t('dlAdvancedSplit') }}</span><input v-model.number="advForm.split" type="number" min="0" max="64" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvancedConn') }}</span><input v-model.number="advForm.conn" type="number" min="0" max="64" class="qd-input" /></label>
                  <label class="dl-check-field"><input v-model="advForm.cont" type="checkbox" class="dl-check" /><span>{{ t('dlAdvancedContinue') }}</span></label>
                </div>

                <h4 class="dl-sub-title">{{ t('dlAdvSectionOut') }}</h4>
                <div class="dl-settings-grid">
                  <label class="dl-field"><span>{{ t('dlAdvOut') }}</span><input v-model="advForm.out" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvSelectFile') }}</span><input v-model="advForm.selectFile" class="qd-input" placeholder="1,3-5" /></label>
                </div>

                <h4 class="dl-sub-title">{{ t('dlAdvSectionHttp') }}</h4>
                <div class="dl-settings-grid">
                  <label class="dl-field dl-field-wide"><span>{{ t('dlAdvHeaders') }}</span><textarea v-model="advForm.headers" rows="3" class="qd-input dl-adv-headers"></textarea></label>
                  <label class="dl-field"><span>{{ t('dlAdvReferer') }}</span><input v-model="advForm.referer" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvUser') }}</span><input v-model="advForm.httpUser" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvPass') }}</span><input v-model="advForm.httpPass" type="password" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvUserAgent') }}</span><input v-model="advForm.userAgent" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvProxy') }}</span><input v-model="advForm.proxy" class="qd-input" /></label>
                  <label class="dl-field dl-field-wide"><span>{{ t('dlAdvChecksum') }}</span><input v-model="advForm.checksum" class="qd-input" placeholder="sha-256=..." /></label>
                </div>

                <h4 class="dl-sub-title">{{ t('dlAdvSectionLimit') }}</h4>
                <div class="dl-settings-grid">
                  <label class="dl-field"><span>{{ t('dlAdvDownLimit') }}</span><input v-model="advForm.downLimit" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvUpLimit') }}</span><input v-model="advForm.upLimit" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvSeedRatio') }}</span><input v-model="advForm.seedRatio" class="qd-input" placeholder="0.0" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvSeedTime') }}</span><input v-model="advForm.seedTime" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlAdvPosition') }}</span><input v-model.number="advForm.position" type="number" min="0" class="qd-input" /></label>
                </div>
                <p class="dl-hint">{{ t('dlAdvHint') }}</p>
              </div>
              <p v-else-if="addMode === 'batch'" class="dl-hint">{{ t('dlAdvancedBatchHint') }}</p>

              <div>
                <p v-if="addMode === 'url'" class="dl-hint">{{ t('dlMagnetHint') }}</p>
                <p v-else-if="addMode === 'torrent'" class="dl-hint">{{ t('dlTorrentHint') }}</p>
                <p v-else-if="addMode === 'metalink'" class="dl-hint">{{ t('dlMetalinkHint') }}</p>
                <p v-else class="dl-hint">{{ t('dlAddBatchHint') }}</p>
                <p class="dl-license">{{ t('dlLicense') }}</p>
              </div>
            </div>

            <footer class="dl-modal-foot">
              <span class="dl-grow"></span>
              <button class="qd-btn qd-btn-sm" @click="closeAdd">{{ t('cancel') }}</button>
              <button
                class="qd-btn qd-btn-primary qd-btn-sm" :disabled="!status.running || submitting"
                :title="status.running ? t('dlAdd') : t('dlEngineNotReady')" @click="submit"
              ><Plus :size="14" />{{ t('dlAdd') }}</button>
            </footer>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- 任务详情弹框：Teleport 到 body，数据随 1s 快照实时刷新 -->
    <Teleport to="body">
      <Transition name="dl-modal">
        <div v-if="detailTask" class="dl-modal-overlay" @mousedown.self="closeDetail">
          <div class="dl-modal dl-modal-wide" role="dialog" :aria-label="t('dlDetailTitle')">
            <header class="dl-modal-head">
              <Info :size="15" class="dl-modal-head-icon" />
              <span class="dl-modal-title">{{ t('dlDetailTitle') }}</span>
              <span class="dl-modal-sub">{{ detailTask.gid }}</span>
              <button class="qd-icon-btn" :title="t('dlDetailRefresh')" @click="loadDetail"><RefreshCw :size="14" /></button>
              <button class="qd-icon-btn" :title="t('dlDetailClose')" @click="closeDetail"><X :size="14" /></button>
            </header>

            <div class="dl-modal-tabs">
              <button
                v-for="k in (['overview', 'files', 'uris', 'peers', 'servers', 'options', 'torrent'] as DetailTab[])" :key="k"
                class="dl-tab" :class="{ 'is-on': detailTab === k }" @click="detailTab = k"
              >{{ t('dlDetailTab' + k.charAt(0).toUpperCase() + k.slice(1)) }}</button>
            </div>

            <div class="dl-modal-body">
              <div v-if="detailLoading" class="dl-table-empty">{{ t('dlLoadingDetail') }}</div>
              <div v-else-if="!detail" class="dl-table-empty">{{ t('dlNoDetail') }}</div>

              <template v-else-if="detailTab === 'overview'">
                <div class="dl-modal-hero">
                  <div class="dl-modal-name" :title="detailTask.url">{{ detailTask.name }}</div>
                  <span class="dl-tag" :class="'is-' + detailTask.status">{{ statusKey(detailTask.status) }}</span>
                </div>
                <div class="dl-modal-progress">
                  <div class="dl-bar">
                    <!-- 进度条宽度是运行时计算值，按 R7 例外就近说明 -->
                    <div class="dl-bar-fill" :style="{ width: Math.min(100, Math.max(0, detailTask.progress)) + '%' }"></div>
                  </div>
                  <span>{{ detailTask.progress.toFixed(1) }}%</span>
                </div>

                <section class="dl-modal-section">
                  <h4>{{ t('dlDetailSectionInfo') }}</h4>
                  <div class="dl-kv">
                    <div class="dl-kv-row"><span>{{ t('dlFieldStatus') }}</span><b>{{ statusKey(detailTask.status) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldSize') }}</span><b>{{ formatBytes(detailTask.completed) }} / {{ formatBytes(detailTask.total) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldSpeed') }}</span><b>{{ formatSpeed(detailTask.speed) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldEta') }}</span><b>{{ etaFor(detailTask) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldCreated') }}</span><b>{{ formatTime(detailTask.createdAt) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldSource') }}</span><b>{{ sourceLabel(detailTask.source) }}</b></div>
                  </div>
                </section>

                <section v-if="detailTask.isTorrent" class="dl-modal-section">
                  <h4>{{ t('dlDetailSectionBT') }}</h4>
                  <div class="dl-kv">
                    <div class="dl-kv-row"><span>{{ t('dlFieldUploadSpeed') }}</span><b>{{ formatSpeed(detailTask.uploadSpeed) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldUploaded') }}</span><b>{{ formatBytes(detailTask.uploaded) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldShareRatio') }}</span><b>{{ formatRatio(detailTask.shareRatio) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldSeeders') }}</span><b>{{ detailTask.seeders }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldConnections') }}</span><b>{{ detailTask.connections }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldPieces') }}</span><b>{{ detailTask.numPieces }} · {{ formatBytes(detailTask.pieceLength) }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldFileCount') }}</span><b>{{ detailTask.fileCount }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldInfoHash') }}</span><b>{{ detailTask.infoHash || '-' }}</b></div>
                  </div>
                </section>

                <section class="dl-modal-section">
                  <h4>{{ t('dlDetailSectionFile') }}</h4>
                  <div class="dl-kv">
                    <div class="dl-kv-row"><span>{{ t('dlFieldGid') }}</span><b>{{ detailTask.gid }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldDir') }}</span><b>{{ detailTask.dir || '-' }}</b></div>
                    <div class="dl-kv-row"><span>{{ t('dlFieldPath') }}</span><b>{{ detailTask.path || '-' }}</b></div>
                    <div v-if="detailTask.error" class="dl-kv-row"><span>{{ t('dlFieldError') }}</span><b class="dl-err-text">{{ detailTask.error }}</b></div>
                  </div>
                </section>

                <section class="dl-modal-section">
                  <h4>{{ t('dlFieldLink') }}</h4>
                  <div class="dl-link-row">
                    <input class="qd-input" :value="detailTask.url" readonly />
                    <button class="qd-icon-btn" :title="t('dlCopyLink')" @click="copyField(detailTask.url)"><Copy :size="14" /></button>
                  </div>
                </section>
              </template>

              <template v-else-if="detailTab === 'files'">
                <div class="dl-table-tools">
                  <button class="qd-btn qd-btn-sm" @click="toggleAllFiles(true)"><Check :size="14" />{{ t('dlSelectAllFiles') }}</button>
                  <button class="qd-btn qd-btn-sm" @click="toggleAllFiles(false)"><X :size="14" />{{ t('dlDeselectAllFiles') }}</button>
                  <span class="dl-grow"></span>
                  <button class="qd-btn qd-btn-primary qd-btn-sm" @click="saveSelection">{{ t('dlSaveSelection') }}</button>
                </div>
                <div v-if="!detail.files.length" class="dl-table-empty">{{ t('dlNoFiles') }}</div>
                <table v-else class="dl-table">
                  <thead><tr>
                    <th>#</th><th>{{ t('dlFileName') }}</th><th>{{ t('dlFileSize') }}</th>
                    <th>{{ t('dlFileDone') }}</th><th>{{ t('dlFileSelected') }}</th>
                  </tr></thead>
                  <tbody>
                    <tr v-for="f in detail.files" :key="f.index">
                      <td>{{ f.index }}</td>
                      <td class="is-wrap" :title="f.path">{{ f.path }}</td>
                      <td>{{ formatBytes(f.length) }}</td>
                      <td>{{ formatBytes(f.completed) }}</td>
                      <td><input type="checkbox" class="dl-check" :checked="selectedFiles.has(f.index)" @change="toggleFile(f.index)" /></td>
                    </tr>
                  </tbody>
                </table>
              </template>

              <template v-else-if="detailTab === 'uris'">
                <div class="dl-link-row">
                  <input v-model="newUri" class="qd-input" :placeholder="t('dlUriPlaceholder')" @keydown.enter="addTaskUri" />
                  <button class="qd-btn qd-btn-sm" @click="addTaskUri"><Plus :size="14" />{{ t('dlAddUri') }}</button>
                </div>
                <div v-if="!detail.uris.length" class="dl-table-empty">{{ t('dlNoUris') }}</div>
                <table v-else class="dl-table">
                  <thead><tr><th>{{ t('dlFieldLink') }}</th><th>{{ t('dlUriStatus') }}</th><th></th></tr></thead>
                  <tbody>
                    <tr v-for="(u, i) in detail.uris" :key="i">
                      <td class="is-wrap">{{ u.uri }}</td><td>{{ u.status }}</td>
                      <td><button class="qd-icon-btn" :title="t('dlDelUri')" @click="delTaskUri(u.uri)"><X :size="14" /></button></td>
                    </tr>
                  </tbody>
                </table>
              </template>

              <template v-else-if="detailTab === 'peers'">
                <div v-if="!detail.peers.length" class="dl-table-empty">{{ t('dlNoPeers') }}</div>
                <table v-else class="dl-table">
                  <thead><tr>
                    <th>{{ t('dlPeerIp') }}</th><th>{{ t('dlPeerPort') }}</th><th>{{ t('dlPeerClient') }}</th>
                    <th>{{ t('dlPeerDown') }}</th><th>{{ t('dlPeerUp') }}</th><th>{{ t('dlPeerSeeder') }}</th>
                  </tr></thead>
                  <tbody>
                    <tr v-for="(p, i) in detail.peers" :key="i">
                      <td>{{ p.ip }}</td><td>{{ p.port }}</td><td class="is-wrap">{{ p.peerId }}</td>
                      <td>{{ formatSpeed(p.downloadSpeed) }}</td><td>{{ formatSpeed(p.uploadSpeed) }}</td>
                      <td>{{ p.seeder ? t('dlYes') : t('dlNo') }}</td>
                    </tr>
                  </tbody>
                </table>
              </template>

              <template v-else-if="detailTab === 'servers'">
                <div v-if="!detail.servers.length" class="dl-table-empty">{{ t('dlNoServers') }}</div>
                <table v-else class="dl-table">
                  <thead><tr>
                    <th>#</th><th>{{ t('dlServerUri') }}</th><th>{{ t('dlServerStatus') }}</th>
                    <th>{{ t('dlServerSpeed') }}</th>
                  </tr></thead>
                  <tbody>
                    <tr v-for="(s, i) in detail.servers" :key="i">
                      <td>{{ s.index }}</td><td class="is-wrap">{{ s.currentUri || s.uri }}</td>
                      <td>{{ s.status }}<span v-if="s.error" class="dl-err-text"> · {{ s.error }}</span></td>
                      <td>{{ formatSpeed(s.speed) }}</td>
                    </tr>
                  </tbody>
                </table>
              </template>

              <template v-else-if="detailTab === 'options'">
                <div class="dl-settings-grid">
                  <label class="dl-field"><span>{{ t('dlTaskOptionDown') }}</span><input v-model="taskOpts.down" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlTaskOptionUp') }}</span><input v-model="taskOpts.up" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlTaskOptionSeedRatio') }}</span><input v-model="taskOpts.seedRatio" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlTaskOptionPeers') }}</span><input v-model="taskOpts.peers" class="qd-input" /></label>
                </div>
                <p class="dl-hint">{{ t('dlTaskOptionHint') }}</p>
                <div class="dl-card-actions"><button class="qd-btn qd-btn-primary" @click="saveTaskOptions">{{ t('dlSaveTaskOptions') }}</button></div>
                <div class="dl-kv dl-kv-raw">
                  <div v-for="(v, k) in detail.options" :key="k" class="dl-kv-row"><span>{{ k }}</span><b>{{ v || '-' }}</b></div>
                </div>
              </template>

              <template v-else-if="detailTab === 'torrent'">
                <div class="dl-kv">
                  <div class="dl-kv-row"><span>{{ t('dlFieldTorrentName') }}</span><b>{{ detailTask.torrentName || '-' }}</b></div>
                  <div class="dl-kv-row"><span>{{ t('dlFieldTorrentMode') }}</span><b>{{ detail.torrentMode || '-' }}</b></div>
                  <div class="dl-kv-row"><span>{{ t('dlFieldTorrentCreated') }}</span><b>{{ detail.torrentCreationDate ? formatTime(detail.torrentCreationDate * 1000) : '-' }}</b></div>
                  <div class="dl-kv-row"><span>{{ t('dlFieldTorrentComment') }}</span><b>{{ detail.torrentComment || '-' }}</b></div>
                  <div class="dl-kv-row"><span>{{ t('dlFieldInfoHash') }}</span><b>{{ detailTask.infoHash || '-' }}</b></div>
                </div>
                <h4 class="dl-sub-title">{{ t('dlFieldTrackers') }}</h4>
                <div v-if="!detail.announceList.length" class="dl-table-empty">{{ t('dlNoTrackers') }}</div>
                <div v-for="(tier, i) in detail.announceList" :key="i" class="dl-tracker-tier">
                  <div v-for="(tr, j) in tier" :key="j" class="dl-tracker">{{ tr }}</div>
                </div>
              </template>
            </div>

            <footer class="dl-modal-foot">
              <button v-if="detailTask.status === 'active' || detailTask.status === 'waiting'" class="qd-btn qd-btn-sm" @click="pause(detailTask.gid)">{{ t('dlPause') }}</button>
              <button v-if="detailTask.status === 'active'" class="qd-btn qd-btn-sm" @click="forcePause(detailTask.gid)">{{ t('dlForcePause') }}</button>
              <button v-if="detailTask.status === 'paused'" class="qd-btn qd-btn-sm" @click="resume(detailTask.gid)">{{ t('dlResume') }}</button>
              <button v-if="detailTask.status === 'error'" class="qd-btn qd-btn-sm" @click="retryTask(detailTask)"><RotateCcw :size="14" />{{ t('dlRetry') }}</button>
              <button class="qd-btn qd-btn-sm qd-btn-danger" @click="remove(detailTask.gid, forceFor(detailTask))">{{ t('dlRemove') }}</button>
              <span class="dl-grow"></span>
              <button class="qd-btn qd-btn-sm" @click="openFolder(detailTask)"><Folder :size="14" />{{ t('dlOpenFolder') }}</button>
              <button class="qd-btn qd-btn-sm" @click="copyField(detailTask.path || detailTask.dir)">{{ t('dlCopyPath') }}</button>
              <button class="qd-btn qd-btn-sm" @click="copyField(detailTask.gid)">{{ t('dlCopyGid') }}</button>
              <button class="qd-btn qd-btn-sm" @click="closeDetail">{{ t('close') }}</button>
            </footer>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- 设置弹框：常规 / BT / 自动化 / 引擎四组 -->
    <Teleport to="body">
      <Transition name="dl-modal">
        <div v-if="showSettings" class="dl-modal-overlay" @mousedown.self="showSettings = false">
          <div class="dl-modal dl-modal-wide" role="dialog" :aria-label="t('dlSettingsTitle')">
            <header class="dl-modal-head">
              <Settings2 :size="15" class="dl-modal-head-icon" />
              <span class="dl-modal-title">{{ t('dlSettingsTitle') }}</span>
              <span class="dl-modal-sub">{{ status.version }}</span>
              <button class="qd-icon-btn" :title="t('dlClose')" @click="showSettings = false"><X :size="14" /></button>
            </header>

            <div class="dl-modal-tabs">
              <button
                v-for="k in (['general', 'bt', 'auto', 'connection', 'engine'] as SettingsTab[])" :key="k"
                class="dl-tab" :class="{ 'is-on': settingsTab === k }" @click="settingsTab = k"
              >{{ t('dlSettings' + k.charAt(0).toUpperCase() + k.slice(1)) }}</button>
            </div>

            <div class="dl-modal-body">
              <template v-if="settingsTab === 'general'">
                <div class="dl-settings-grid">
                  <label class="dl-field"><span>{{ t('dlDir') }}</span><input v-model="settingsForm.downloadDir" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlSplit') }}</span><input v-model.number="settingsForm.split" type="number" min="1" max="64" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlConcurrency') }}</span><input v-model.number="settingsForm.maxConcurrent" type="number" min="1" max="64" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlLimit') }}</span><input v-model="settingsForm.downloadLimit" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlSetUploadLimit') }}</span><input v-model="settingsForm.uploadLimit" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlSetMinSplit') }}</span><input v-model="settingsForm.minSplitSize" class="qd-input" placeholder="20M" /></label>
                  <label class="dl-field"><span>{{ t('dlSetUserAgent') }}</span><input v-model="settingsForm.userAgent" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlSetProxy') }}</span><input v-model="settingsForm.proxy" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlSetFileAlloc') }}</span>
                    <select v-model="settingsForm.fileAllocation" class="qd-input">
                      <option value="none">{{ t('dlAllocNone') }}</option>
                      <option value="falloc">{{ t('dlAllocFalloc') }}</option>
                      <option value="trunc">{{ t('dlAllocTrunc') }}</option>
                      <option value="prealloc">{{ t('dlAllocPrealloc') }}</option>
                    </select>
                  </label>
                </div>
                <p class="dl-hint">{{ t('dlOptionsHint') }}</p>
                <p class="dl-hint">{{ t('dlSetFileAllocHint') }}</p>
              </template>

              <template v-else-if="settingsTab === 'bt'">
                <div class="dl-settings-grid">
                  <label class="dl-field"><span>{{ t('dlSetListenPort') }}</span><input v-model.number="settingsForm.listenPort" type="number" min="1" max="65500" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlSetDhtPort') }}</span><input v-model.number="settingsForm.dhtListenPort" type="number" min="1" max="65500" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlSetMaxPeers') }}</span><input v-model.number="settingsForm.btMaxPeers" type="number" min="1" max="1024" class="qd-input" /></label>
                  <label class="dl-field"><span>{{ t('dlSetSeedRatio') }}</span><input v-model="settingsForm.seedRatio" class="qd-input" placeholder="0.0" /></label>
                  <label class="dl-field"><span>{{ t('dlSetSeedTime') }}</span><input v-model="settingsForm.seedTime" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field dl-field-wide"><span>{{ t('dlSetTracker') }}</span><input v-model="settingsForm.btTracker" class="qd-input" /></label>
                </div>
                <p class="dl-hint">{{ t('dlSetTrackerHint') }}</p>
                <div class="dl-switch-grid">
                  <label class="dl-check-field"><input v-model="settingsForm.disableDht" type="checkbox" class="dl-check" /><span>{{ t('dlSetDht') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.disableDht6" type="checkbox" class="dl-check" /><span>{{ t('dlSetDht6') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.disablePeerExchange" type="checkbox" class="dl-check" /><span>{{ t('dlSetPex') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.disableSaveMetadata" type="checkbox" class="dl-check" /><span>{{ t('dlSetSaveMetadata') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.disableFollowTorrent" type="checkbox" class="dl-check" /><span>{{ t('dlSetFollowTorrent') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.btForceEncryption" type="checkbox" class="dl-check" /><span>{{ t('dlSetForceEncryption') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.btRequireCrypto" type="checkbox" class="dl-check" /><span>{{ t('dlSetRequireCrypto') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.btRemoveUnselected" type="checkbox" class="dl-check" /><span>{{ t('dlSetRemoveUnselected') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.checkIntegrity" type="checkbox" class="dl-check" /><span>{{ t('dlSetCheckIntegrity') }}</span></label>
                </div>
                <p class="dl-hint">{{ t('dlSettingsRestartHint') }}</p>
              </template>

              <template v-else-if="settingsTab === 'auto'">
                <div class="dl-switch-grid">
                  <label class="dl-check-field"><input v-model="settingsForm.autoOpenFolder" type="checkbox" class="dl-check" /><span>{{ t('dlSetAutoOpen') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.autoCategory" type="checkbox" class="dl-check" /><span>{{ t('dlSetAutoCategory') }}</span></label>
                  <label class="dl-check-field"><input v-model="settingsForm.perTaskFolder" type="checkbox" class="dl-check" /><span>{{ t('dlSetPerTaskFolder') }}</span></label>
                </div>
                <label class="dl-field dl-field-block"><span>{{ t('dlSetCategoryRules') }}</span><textarea v-model="categoryText" class="qd-input" rows="5"></textarea></label>
                <p class="dl-hint">{{ t('dlSetCategoryHint') }}</p>
                <p class="dl-hint">{{ t('dlSetPerTaskFolderHint') }}</p>
                <h4 class="dl-sub-title"><Zap :size="14" />{{ t('dlSetSchedule') }}</h4>
                <div class="dl-switch-grid">
                  <label class="dl-check-field"><input v-model="settingsForm.schedule.enabled" type="checkbox" class="dl-check" /><span>{{ t('dlSetSchedule') }}</span></label>
                  <label class="dl-field"><span>{{ t('dlSetScheduleStart') }}</span><input v-model="settingsForm.schedule.start" class="qd-input" placeholder="23:00" /></label>
                  <label class="dl-field"><span>{{ t('dlSetScheduleEnd') }}</span><input v-model="settingsForm.schedule.end" class="qd-input" placeholder="07:00" /></label>
                  <label class="dl-field"><span>{{ t('dlSetScheduleDown') }}</span><input v-model="settingsForm.schedule.downloadLimit" class="qd-input" placeholder="0" /></label>
                  <label class="dl-field"><span>{{ t('dlSetScheduleUp') }}</span><input v-model="settingsForm.schedule.uploadLimit" class="qd-input" placeholder="0" /></label>
                </div>
                <p class="dl-hint">{{ t('dlSetScheduleHint') }}</p>
              </template>

              <template v-else-if="settingsTab === 'connection'">
                <div class="dl-switch-grid">
                  <label class="dl-check-field"><input v-model="settingsForm.remoteEnabled" type="checkbox" class="dl-check" /><span>{{ t('dlConnRemote') }}</span></label>
                  <label class="dl-check-field"><input v-model="notifyEnabled" type="checkbox" class="dl-check" /><span>{{ t('dlConnNotify') }}</span></label>
                </div>
                <p class="dl-hint">{{ t('dlConnNotifyHint') }}</p>
                <template v-if="settingsForm.remoteEnabled">
                  <div class="dl-settings-grid">
                    <label class="dl-field"><span>{{ t('dlConnHost') }}</span><input v-model="settingsForm.remoteHost" class="qd-input" placeholder="192.168.1.10" /></label>
                    <label class="dl-field"><span>{{ t('dlConnPort') }}</span><input v-model.number="settingsForm.remotePort" type="number" min="1" max="65535" class="qd-input" /></label>
                    <label class="dl-field"><span>{{ t('dlConnSecret') }}</span><input v-model="settingsForm.remoteSecret" type="password" class="qd-input" :placeholder="settingsForm.remoteSecretSet ? t('dlConnSecretSet') : ''" /></label>
                    <label class="dl-field"><span>{{ t('dlConnPath') }}</span><input v-model="settingsForm.remotePath" class="qd-input" placeholder="/jsonrpc" /></label>
                  </div>
                  <div class="dl-switch-grid">
                    <label class="dl-check-field"><input v-model="settingsForm.remoteSecure" type="checkbox" class="dl-check" /><span>{{ t('dlConnSecure') }}</span></label>
                  </div>
                  <p class="dl-hint">{{ t('dlConnHint') }}</p>
                </template>
                <p v-else class="dl-hint">{{ t('dlConnLocal') }}</p>
                <p class="dl-hint">{{ status.wsConnected ? t('dlConnWsOn') : t('dlConnWsOff') }}</p>
              </template>

              <template v-else-if="settingsTab === 'engine'">
                <div class="dl-kv">
                  <div class="dl-kv-row"><span>{{ t('dlEngineVersion') }}</span><b>{{ engine?.version || '-' }}</b></div>
                  <div class="dl-kv-row"><span>{{ t('dlEngineFeatures') }}</span><b>{{ (engine?.features || []).join(', ') || '-' }}</b></div>
                  <div class="dl-kv-row"><span>{{ t('dlEngineSession') }}</span><b>{{ engine?.sessionId || '-' }}</b></div>
                  <div class="dl-kv-row"><span>{{ t('dlEngineMethods') }}</span><b>{{ (engine?.methods || []).length || 0 }}</b></div>
                </div>
                <h4 class="dl-sub-title"><Cpu :size="14" />{{ t('dlEngineOptions') }}</h4>
                <div class="dl-link-row">
                  <input v-model="globalKey" class="qd-input dl-global-key" />
                  <input v-model="globalVal" class="qd-input" :placeholder="t('dlGlobalOptionValue')" />
                  <button class="qd-btn qd-btn-sm" @click="applyGlobalOption">{{ t('dlApplyGlobalOption') }}</button>
                </div>
                <div class="dl-kv dl-kv-raw">
                  <div v-for="(v, k) in globalOpts" :key="k" class="dl-kv-row"><span>{{ k }}</span><b>{{ v || '-' }}</b></div>
                </div>
                <h4 class="dl-sub-title"><Radio :size="14" />{{ t('dlEngineShutdown') }}</h4>
                <p class="dl-hint">{{ t('dlEngineShutdownHint') }}</p>
                <div class="dl-card-actions">
                  <button class="qd-btn qd-btn-sm" @click="doShutdown(false)">{{ t('dlEngineShutdown') }}</button>
                  <button class="qd-btn qd-btn-sm qd-btn-danger" @click="doShutdown(true)">{{ t('dlEngineForceShutdown') }}</button>
                </div>
              </template>
            </div>

            <footer class="dl-modal-foot">
              <button class="qd-btn qd-btn-sm" @click="resetOptions"><RotateCcw :size="14" />{{ t('dlResetDefaults') }}</button>
              <button v-if="settingsTab === 'engine'" class="qd-btn qd-btn-sm" @click="loadEngine"><RefreshCw :size="14" />{{ t('dlRefresh') }}</button>
              <span class="dl-grow"></span>
              <button class="qd-btn qd-btn-sm" @click="showSettings = false">{{ t('cancel') }}</button>
              <button class="qd-btn qd-btn-primary qd-btn-sm" @click="submitSettings">{{ t('dlSave') }}</button>
            </footer>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.dl-page { position: relative; }
.dl-page-body { display: flex; flex-direction: column; gap: var(--space-5); }

.dl-engine {
  display: inline-flex; align-items: center; gap: var(--space-2);
  padding: var(--space-1) var(--space-3); border-radius: var(--radius-full);
  background: var(--color-bg-tertiary); font-size: var(--text-xs); color: var(--color-text-secondary);
}
.dl-dot { width: 8px; height: 8px; border-radius: var(--radius-full); background: var(--color-text-disabled); flex: none; }
.dl-engine.is-on .dl-dot { background: var(--color-success); }
.dl-error {
  padding: var(--space-4) var(--space-5); border-radius: var(--radius-md);
  border: 1px solid var(--color-danger-border); background: var(--color-danger-bg);
  color: var(--color-danger); font-size: var(--text-xs);
}

.dl-setup {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: var(--space-4); min-height: 280px; padding: var(--space-8); text-align: center;
}
.dl-setup-icon {
  display: flex; align-items: center; justify-content: center;
  width: 48px; height: 48px; border-radius: var(--radius-full);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.dl-setup h3 { margin: 0; font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary); }
.dl-setup p { margin: 0; max-width: 560px; font-size: var(--text-sm); line-height: 1.7; color: var(--color-text-secondary); }
.dl-setup .dl-license { font-size: var(--text-xs); color: var(--color-text-disabled); }
.dl-setup-icon.is-loading svg { animation: dl-spin 1.2s linear infinite; }
@keyframes dl-spin { to { transform: rotate(360deg); } }

.dl-notice {
  display: flex; align-items: center; gap: var(--space-3);
  padding: var(--space-4) var(--space-5); font-size: var(--text-sm); color: var(--color-text-secondary);
}
.dl-notice-icon { color: var(--color-warning); flex: none; }
.dl-notice-text { display: flex; flex-direction: column; gap: var(--space-1); }
.dl-notice p { margin: 0; font-size: var(--text-xs); color: var(--color-text-muted); }

.dl-stats { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-5); padding: var(--space-4) var(--space-5); }
.dl-stat { display: flex; flex-direction: column; gap: var(--space-1); min-width: 62px; }
.dl-stat-label { display: inline-flex; align-items: center; gap: var(--space-2); font-size: var(--text-xs); color: var(--color-text-muted); white-space: nowrap; }
.dl-stat-value { font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary); }
.dl-stat.is-speed .dl-stat-value { color: var(--color-accent); }
.dl-stat.is-up .dl-stat-value { color: var(--color-success); }
.dl-stat-spark { flex: 1 1 140px; min-width: 120px; }
.dl-spark-svg { display: block; width: 100%; height: 30px; }
.dl-spark-area { fill: var(--color-accent-bg); stroke: none; }
.dl-spark-line { fill: none; stroke: var(--color-accent); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
.dl-spark-line.is-up { stroke: var(--color-success); stroke-dasharray: 3 2; }
.dl-stat-overall { flex: 1 1 200px; min-width: 180px; }
.dl-stat-overall .dl-bar { margin-top: var(--space-2); }

.dl-settings-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: var(--space-4); }
.dl-field { display: flex; flex-direction: column; gap: var(--space-2); font-size: var(--text-xs); color: var(--color-text-secondary); }
.dl-field .qd-input { width: 100%; }
.dl-field-wide { grid-column: 1 / -1; }
.dl-field-block { margin-top: var(--space-4); }
.dl-field-block .qd-input { min-height: 88px; resize: vertical; }
.dl-card-actions { display: flex; justify-content: flex-end; gap: var(--space-2); margin-top: var(--space-5); }
.dl-hint { margin: var(--space-3) 0 0; font-size: var(--text-xs); line-height: 1.6; color: var(--color-text-disabled); }
.dl-hint.is-warn { color: var(--color-danger); }

/* 新建弹框内的输入区：不占列表宽度，source / target / 高级选项分段 */
.dl-add-input-row { display: flex; align-items: center; gap: var(--space-2); }
.dl-add-input-row .qd-input { flex: 1; min-width: 0; width: auto; }
.dl-add-textarea { min-height: 132px; height: auto; padding-top: var(--space-2); resize: vertical; }
.dl-adv-headers { min-height: 64px; height: auto; padding-top: var(--space-2); resize: vertical; font-family: var(--font-mono); font-size: var(--text-xs); }
.dl-add-tools { display: flex; justify-content: flex-end; gap: var(--space-2); margin-top: var(--space-2); }
.dl-row-btn { flex: none; white-space: nowrap; }
.dl-add-file-row { display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
.dl-picked { flex: 1 1 200px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-xs); color: var(--color-text-muted); }
.dl-check-field { display: flex; align-items: center; gap: var(--space-2); align-self: end; font-size: var(--text-sm); color: var(--color-text-secondary); }
.dl-check { flex: none; margin: 0; }
.dl-switch-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: var(--space-3); margin-top: var(--space-4); }
.dl-license { margin: var(--space-4) 0 0; font-size: var(--text-xs); line-height: 1.6; color: var(--color-text-disabled); }

.dl-workspace { display: flex; align-items: flex-start; gap: var(--space-4); }
.dl-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: var(--space-3); }
.dl-tabs { display: flex; gap: var(--space-2); flex-wrap: wrap; }
.dl-tab {
  display: inline-flex; align-items: center; gap: var(--space-2); height: 22px; padding: 0 var(--space-3);
  border: 1px solid transparent; border-radius: var(--radius-full);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-family: inherit; font-size: var(--text-xs); cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.dl-tab:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.dl-tab.is-on { color: var(--color-accent); background: var(--color-accent-bg); border-color: var(--color-accent-border-strong); }
.dl-tab-count { font-size: var(--text-2xs); color: var(--color-text-muted); }

.dl-toolbar {
  display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap;
  padding: var(--space-3) var(--space-4); border-radius: var(--radius-md);
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
}
.dl-check-all { display: inline-flex; align-items: center; gap: var(--space-2); font-size: var(--text-xs); color: var(--color-text-secondary); }
.dl-search { flex: 1 1 180px; max-width: 280px; }
.dl-search .qd-input { padding-right: 28px; }
.dl-search-clear { position: absolute; right: 2px; }
.dl-sort { width: 140px; font-size: var(--text-xs); }
.dl-grow { flex: 1; }
.dl-count { font-size: var(--text-xs); color: var(--color-text-muted); white-space: nowrap; }

.dl-bulk {
  display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap;
  padding: var(--space-3) var(--space-4); border-radius: var(--radius-md);
  background: var(--color-accent-bg); border: 1px solid var(--color-accent-border-strong);
  font-size: var(--text-xs); color: var(--color-accent);
}

.dl-empty { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--space-3); min-height: 180px; padding: var(--space-8); }
.dl-empty-icon { color: var(--color-text-disabled); }
.dl-empty p { margin: 0; font-size: var(--text-md); color: var(--color-text-secondary); }
.dl-empty span { font-size: var(--text-xs); color: var(--color-text-disabled); }

.dl-list { display: flex; flex-direction: column; gap: var(--space-3); }
.dl-item { display: flex; flex-direction: column; gap: var(--space-3); padding: var(--space-4) var(--space-5); cursor: pointer; }
.dl-item:hover { background: var(--color-bg-hover); }
.dl-item.is-checked { background: var(--color-bg-hover); }
.dl-item.is-current { background: var(--color-select-bg); box-shadow: inset 3px 0 0 var(--color-accent); }
.dl-item-head { display: flex; align-items: center; gap: var(--space-3); }
.dl-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-md); color: var(--color-text-primary); }
.dl-badge {
  flex: none; display: inline-flex; align-items: center; gap: 2px; padding: 1px var(--space-2);
  border-radius: var(--radius-sm); background: var(--color-accent-bg); color: var(--color-accent);
  font-size: var(--text-2xs);
}
.dl-tag { flex: none; padding: 2px var(--space-4); border-radius: var(--radius-full); font-size: var(--text-xs); color: var(--color-text-secondary); background: var(--color-bg-tertiary); }
.dl-tag.is-active { color: var(--color-accent); background: var(--color-accent-border-strong); }
.dl-tag.is-complete { color: var(--color-success); background: var(--color-success-bg); }
.dl-tag.is-error { color: var(--color-danger); background: var(--color-danger-bg); }
.dl-tag.is-paused { color: var(--color-warning); background: var(--color-warning-bg); }

.dl-bar { height: 6px; border-radius: var(--radius-full); background: var(--color-bg-tertiary); overflow: hidden; }
.dl-bar-fill { height: 100%; border-radius: var(--radius-full); background: var(--color-accent); transition: width var(--transition-fast); }
.dl-item-meta { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; font-size: var(--text-xs); color: var(--color-text-muted); }
.dl-item-meta > span { display: inline-flex; align-items: center; gap: var(--space-1); }
.dl-err-text { color: var(--color-danger); }

/* 弹框：面层用 popover-bg（比卡片亮一级），遮罩压住整页 */
.dl-modal-overlay { position: fixed; inset: 0; z-index: 3000; display: flex; align-items: center; justify-content: center; background: var(--color-bg-overlay); }
.dl-modal {
  width: 560px; max-width: calc(100vw - 40px); max-height: calc(100vh - 80px);
  display: flex; flex-direction: column;
  border: 1px solid var(--popover-border-color); border-radius: var(--radius-md);
  background: var(--color-popover-bg); box-shadow: var(--shadow-pop);
}
.dl-modal-wide { width: 720px; }
.dl-modal-import { width: 980px; max-height: calc(100vh - 60px); }
/* 导入表：列宽固定，长 URL / 长路径靠输入框内部横向滚动，不把弹框撑爆 */
.dl-import-scroll { max-height: 320px; overflow: auto; border: 1px solid var(--color-border); border-radius: var(--radius-sm); }
.dl-import-table { table-layout: fixed; min-width: 880px; }
.dl-import-table th.is-check { width: 34px; }
.dl-import-table th:nth-child(2) { width: 190px; }
.dl-import-table th:nth-child(3) { width: 280px; }
.dl-import-table th:nth-child(4) { width: 210px; }
.dl-import-table th:nth-child(5) { width: 82px; }
.dl-import-table th:nth-child(6) { width: 84px; }
.dl-import-table td.is-nowrap { white-space: nowrap; }
.dl-import-table tr.is-off { opacity: 0.55; }
.dl-import-input { width: 100%; min-width: 0; font-size: var(--text-2xs); }
.dl-import-note { margin-top: 2px; font-size: var(--text-2xs); line-height: 1.4; color: var(--color-text-disabled); }
.dl-import-state {
  display: inline-block; padding: 1px var(--space-2); border-radius: var(--radius-full);
  font-size: var(--text-2xs); white-space: nowrap;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.dl-import-state.is-resume { background: var(--color-success-bg); color: var(--color-success); }
.dl-import-state.is-partial { background: var(--color-warning-bg); color: var(--color-warning); }
.dl-import-state.is-fresh { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
.dl-import-options { display: flex; align-items: center; gap: var(--space-5); flex-wrap: wrap; }
.dl-import-options .dl-check-field { align-self: auto; font-size: var(--text-xs); }
.dl-import-result { padding: var(--space-3); border-radius: var(--radius-sm); background: var(--color-bg-secondary); }
.dl-import-result .dl-hint { margin-top: 0; }
.dl-import-result .dl-hint + .dl-hint { margin-top: var(--space-1); }
.dl-modal-head { display: flex; align-items: center; gap: var(--space-2); flex: none; padding: var(--space-3) var(--space-5); border-bottom: 1px solid var(--color-border); }
.dl-modal-head-icon { flex: none; color: var(--color-accent); }
.dl-modal-title { font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.dl-modal-sub { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--font-mono); font-size: var(--text-2xs); color: var(--color-text-muted); }
.dl-modal-tabs { display: flex; gap: var(--space-2); flex-wrap: wrap; flex: none; padding: var(--space-3) var(--space-5) 0; }
.dl-modal-body { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-5); padding: var(--space-5); }
.dl-modal-hero { display: flex; align-items: center; gap: var(--space-3); }
.dl-modal-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-lg); font-weight: 600; color: var(--color-text-primary); }
.dl-modal-progress { display: flex; align-items: center; gap: var(--space-3); }
.dl-modal-progress .dl-bar { flex: 1; }
.dl-modal-progress span { flex: none; width: 48px; text-align: right; font-size: var(--text-xs); color: var(--color-text-muted); }
.dl-modal-section { display: flex; flex-direction: column; gap: var(--space-3); }
.dl-modal-section h4 { margin: 0; padding-bottom: var(--space-2); border-bottom: 1px solid var(--color-border); font-size: var(--text-xs); font-weight: 600; color: var(--color-text-secondary); }
.dl-kv { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-3) var(--space-5); }
.dl-kv-row { display: flex; flex-direction: column; gap: 2px; min-width: 0; font-size: var(--text-xs); }
.dl-kv-row span { color: var(--color-text-muted); }
.dl-kv-row b { font-weight: 500; color: var(--color-text-secondary); word-break: break-all; }
.dl-kv-raw { margin-top: var(--space-4); padding-top: var(--space-4); border-top: 1px solid var(--color-border); }
.dl-link-row { display: flex; align-items: center; gap: var(--space-2); }
.dl-link-row .qd-input { flex: 1; min-width: 0; font-size: var(--text-xs); }
.dl-global-key { flex: 0 1 250px !important; }
.dl-modal-foot {
  display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-2); flex: none;
  padding: var(--space-3) var(--space-5); border-top: 1px solid var(--color-border);
}
.dl-modal-enter-active, .dl-modal-leave-active { transition: opacity var(--transition-fast); }
.dl-modal-enter-from, .dl-modal-leave-to { opacity: 0; }

.dl-table-tools { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; }
.dl-table { width: 100%; border-collapse: collapse; font-size: var(--text-xs); }
.dl-table th { padding: var(--space-2) var(--space-3); text-align: left; font-weight: 500; color: var(--color-text-muted); border-bottom: 1px solid var(--color-border); white-space: nowrap; }
.dl-table td { padding: var(--space-2) var(--space-3); color: var(--color-text-secondary); border-bottom: 1px solid var(--color-border); }
.dl-table td.is-wrap { word-break: break-all; max-width: 320px; }
.dl-table-empty { padding: var(--space-8); text-align: center; font-size: var(--text-xs); color: var(--color-text-muted); }
.dl-sub-title { display: inline-flex; align-items: center; gap: var(--space-2); margin: var(--space-2) 0 0; font-size: var(--text-xs); font-weight: 600; color: var(--color-text-secondary); }
.dl-tracker-tier { display: flex; flex-direction: column; gap: 2px; padding: var(--space-2) var(--space-3); border-radius: var(--radius-sm); background: var(--color-bg-secondary); }
.dl-tracker { font-size: var(--text-2xs); color: var(--color-text-muted); word-break: break-all; }

/* 浮层：必须比卡片亮 */
.dl-ctx {
  position: absolute; z-index: 50; min-width: 190px; padding: var(--space-2);
  display: flex; flex-direction: column; gap: 2px;
  border-radius: var(--radius-md); background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color); box-shadow: var(--shadow-pop);
}
.dl-ctx-item {
  display: flex; align-items: center; gap: var(--space-3); width: 100%; height: 28px;
  padding: 0 var(--space-3); border: none; border-radius: var(--radius-sm);
  background: none; color: var(--color-text-secondary);
  font-family: inherit; font-size: var(--text-sm); text-align: left; cursor: pointer;
}
.dl-ctx-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.dl-ctx-item.is-danger { color: var(--color-danger); }
.dl-ctx-sep { height: 1px; margin: var(--space-1) 0; background: var(--color-border); }

@media (max-width: 720px) {
  .dl-kv { grid-template-columns: minmax(0, 1fr); }
}
</style>
