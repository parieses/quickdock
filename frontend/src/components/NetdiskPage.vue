<script setup lang="ts">
import PageHeader from './PageHeader.vue'
/**
 * 网盘页：入口是「本机 OpenList 网关」的 WebDAV。
 *
 * 为什么不自研网盘协议、也不把 OAuth 塞进前端：百度/夸克的凭据、风控、接口变动
 * 全由网关社区跟进，QuickDock 只当标准 WebDAV 客户端 —— 一份代码通吃所有网盘，
 * 加新网盘只要在网关后台加一个驱动。
 *
 * 三处交互取舍：
 *  1. 「配账号」只能去网关后台（百度扫码 / 夸克 Cookie 都在那儿），所以页面顶部
 *     常驻「管理后台」按钮，而不是把登录流程抄一遍；
 *  2. 未安装 / 未运行时不留一张死白的列表，直接给「安装 / 启动」按钮 —— 这是
 *     用户在这个页面唯一能自救的动作；
 *  3. 下载走系统保存对话框 + 后端写盘，进度经事件推送。前端自己拼路径会绕过
 *     用户对目录的选择权，也拿不到真实写盘进度。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ArrowUp,
  ArrowUpDown,
  Cloud,
  Download,
  FileText,
  Folder,
  FolderPlus,
  Pencil,
  Power,
  RefreshCw,
  Search,
  Send,
  Settings2,
  Trash2,
  Upload,
  X,
} from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { openExternal } from '../utils/externalLinks'
import { formatBytes } from '../utils/envFormat'
import ConfirmDialog from './ConfirmDialog.vue'
import CreateDialog from './CreateDialog.vue'
import {
  SelectLocalDirectory,
  SelectLocalFiles,
  SelectSavePath,
} from '../../bindings/quickdock/services/appservice'
import { EnvInstall, EnvRestart, EnvStart } from '../../bindings/quickdock/services/env/environmentservice'
import {
  NetdiskDelete,
  NetdiskDownload,
  NetdiskDownloadBatch,
  NetdiskListDir,
  NetdiskMkdir,
  NetdiskRename,
  NetdiskSearch,
  NetdiskSendToDownloader,
  NetdiskStatus,
  NetdiskUpload,
} from '../../bindings/quickdock/services/netdisk/netdiskservice'

const { t } = useI18n()
const toast = useToast()

interface GatewayInfo {
  installed: boolean
  running: boolean
  port: number
  adminUrl: string
  davUrl: string
  username: string
  version: string
  /** 预览地址前缀（http://127.0.0.1:PORT/<token>/f?p=），空串表示代理没起来。 */
  previewBase: string
  /** 网关已运行但一个存储都没有（或根目录列不出来）：首屏进引导页，别弹 HTTP 500。 */
  needSetup: boolean
}

interface Entry {
  name: string
  path: string
  size: number
  isDir: boolean
  modified: string
  /** 预览形态，后端按扩展名判定：video/audio/image/pdf/text/code/markdown/binary/… */
  kind: string
}

interface JobRow {
  /** 进度行主键：方向 + 远端路径，同名文件也不会互相覆盖进度。 */
  key: string
  name: string
  percent: number
  done: number
  total: number
  phase: string
  err: string
  /** up = 上传，down = 下载；行首箭头据此区分。 */
  dir: 'up' | 'down'
  index: number
  count: number
}

const gw = ref<GatewayInfo | null>(null)
/** 网关已跑起来但没添加存储：单独一档引导态，避免列目录撞 500。 */
const needSetup = ref(false)
const entries = ref<Entry[]>([])
/** 当前目录（相对 WebDAV 根，"/" 表示根）。 */
const dir = ref('/')
const loading = ref(false)
const busy = ref('') // install | start | ''
const installText = ref('')
const jobs = ref<JobRow[]>([])

type SortKey = 'name' | 'size' | 'time'
const keyword = ref('')
const sortKey = ref<SortKey>('name')
const sortAsc = ref(true)
/** 多选：存远端路径（同一批操作都按路径走）。 */
const selected = ref<string[]>([])
/** 搜索模式：列表来自递归搜索结果，而不是当前目录。 */
const searchMode = ref(false)
const searching = ref(false)
/** 新建目录 / 重命名 / 删除 三个弹框的开关与目标。 */
const showMkdir = ref(false)
const renameTarget = ref<Entry | null>(null)
const deleteTargets = ref<Entry[]>([])

/** 正在预览的文件；null 表示浮层关闭。 */
const preview = ref<Entry | null>(null)
const previewText = ref('')
const previewLoading = ref(false)

/** 面包屑：把 "/a/b" 拆成 [{name:'根',path:'/'},{name:'a',path:'/a'},…] */
const crumbs = computed(() => {
  const out = [{ name: t('netdiskRoot'), path: '/' }]
  const segs = dir.value.split('/').filter(Boolean)
  let acc = ''
  for (const s of segs) {
    acc += '/' + s
    out.push({ name: s, path: acc })
  }
  return out
})

const ready = computed(() => !!gw.value?.installed && !!gw.value?.running)

/**
 * 列表展示数据：当前目录模式下按关键词过滤，然后排序。
 * 目录永远排最前——文件管理器里养成的预期，搜索时同样适用。
 */
const displayEntries = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  const list =
    searchMode.value || !kw ? entries.value.slice() : entries.value.filter((e) => e.name.toLowerCase().includes(kw))
  const sign = sortAsc.value ? 1 : -1
  return list.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    let diff = 0
    if (sortKey.value === 'size') diff = a.size - b.size
    else if (sortKey.value === 'time') diff = timeValue(a.modified) - timeValue(b.modified)
    else diff = a.name.localeCompare(b.name, 'zh-CN')
    return diff * sign
  })
})

/** 全选只作用于「当前可见项」：搜索/翻页时不该把看不见的也选上。 */
const allSelected = computed(
  () => displayEntries.value.length > 0 && displayEntries.value.every((e) => selected.value.includes(e.path)),
)

async function refreshStatus() {
  try {
    gw.value = unwrap<GatewayInfo>(await NetdiskStatus())
    // 后端在 Status 里探测过根目录：没存储时直接进引导页，不再发那次注定
    // 返回 500 的列目录请求；用户在后台加完存储点「刷新」就会重新探测。
    needSetup.value = !!gw.value?.needSetup
    if (needSetup.value) {
      entries.value = []
      return
    }
    if (ready.value) await loadDir(dir.value)
  } catch (e) {
    logErr('NetdiskPage.refreshStatus', e)
    toast.error(getErrorMessage(e))
  }
}

async function loadDir(path: string) {
  loading.value = true
  try {
    entries.value = unwrap<Entry[]>(await NetdiskListDir(path)) ?? []
    dir.value = path
    // 换目录等于换上下文：搜索词、搜索结果、多选都得清空，否则会出现
    // 「在这个目录勾选、去那个目录删除」这种灾难性误操作。
    searchMode.value = false
    keyword.value = ''
    selected.value = []
  } catch (e) {
    logErr('NetdiskPage.loadDir', e)
    toast.error(getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

function openDir(e: Entry) {
  if (e.isDir) void loadDir(e.path)
}

function goUp() {
  if (dir.value === '/') return
  const parent = dir.value.replace(/\/[^/]+$/, '') || '/'
  void loadDir(parent)
}

/** 安装网关：走环境管理同一条安装链路，进度由 env 事件推过来。 */
async function installGateway() {
  if (busy.value) return
  busy.value = 'install'
  installText.value = t('netdiskInstalling')
  try {
    unwrap(await EnvInstall('openlist', '', '', ''))
  } catch (e) {
    busy.value = ''
    logErr('NetdiskPage.install', e)
    toast.error(getErrorMessage(e))
  }
}

async function startGateway() {
  if (busy.value) return
  busy.value = 'start'
  try {
    // 先刷新一次状态：安装完成的瞬间 gw 还是旧值（未装/无版本号），
    // 而 EnvStart 需要真实的已装版本号，空串会被后端 validateVersion 挡回。
    if (!gw.value?.version) await refreshStatus()
    const ver = gw.value?.version || ''
    if (!ver) throw new Error(t('netdiskNotInstalled'))
    unwrap(await EnvStart('openlist', ver))
    // 网关起来到端口可连有几百毫秒延迟，稍等再拉状态。
    await new Promise((r) => setTimeout(r, 1200))
    await refreshStatus()
  } catch (e) {
    logErr('NetdiskPage.start', e)
    toast.error(getErrorMessage(e))
  } finally {
    busy.value = ''
  }
}

/** 重启网关：OpenList 的 sqlite 死锁、账号改动都只能靠重启进程生效。 */
async function restartGateway() {
  if (busy.value) return
  busy.value = 'restart'
  try {
    const ver = gw.value?.version || ''
    if (!ver) throw new Error(t('netdiskNotInstalled'))
    unwrap(await EnvRestart('openlist', ver))
    // 停 + 起比单纯 start 慢：后端会等旧进程真正退出（避免 admin set 撞库锁）。
    await new Promise((r) => setTimeout(r, 1500))
    await refreshStatus()
  } catch (e) {
    logErr('NetdiskPage.restart', e)
    toast.error(getErrorMessage(e))
  } finally {
    busy.value = ''
  }
}

async function downloadFile(e: Entry) {
  try {
    const dest = unwrap<string>(await SelectSavePath(t('netdiskSaveTitle'), e.name)) || ''
    if (!dest) return
    unwrap(await NetdiskDownload(e.path, dest))
    jobs.value = [
      {
        key: 'down:' + e.path,
        name: e.name,
        dir: 'down',
        index: 0,
        count: 0,
        percent: 0,
        done: 0,
        total: -1,
        phase: 'downloading',
        err: '',
      },
      ...jobs.value,
    ]
  } catch (err) {
    logErr('NetdiskPage.download', err)
    toast.error(getErrorMessage(err))
  }
}

// ---- 选择 / 排序 / 搜索 ----

function toggleSort(key: SortKey) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value
    return
  }
  sortKey.value = key
  // 名称习惯正序，大小/时间习惯「大的、新的在前」。
  sortAsc.value = key === 'name'
}

function isSelected(p: string): boolean {
  return selected.value.includes(p)
}

function toggleSel(p: string) {
  const i = selected.value.indexOf(p)
  if (i >= 0) selected.value.splice(i, 1)
  else selected.value.push(p)
}

function toggleAll() {
  selected.value = allSelected.value ? [] : displayEntries.value.map((e) => e.path)
}

/** 选中项对应的条目：删除提示要区分文件夹，纯路径不够。 */
const selectedEntries = computed(() => displayEntries.value.filter((e) => selected.value.includes(e.path)))
const selectedFiles = computed(() => selectedEntries.value.filter((e) => !e.isDir))

/**
 * 递归搜索结果。
 * 只在用户按按钮时触发：边打字边搜会把网关打满（每次输入都要走一遍 BFS）。
 */
async function runSearch() {
  const kw = keyword.value.trim()
  if (!kw || searching.value) return
  searching.value = true
  try {
    const found = unwrap<Entry[]>(await NetdiskSearch(dir.value, kw)) ?? []
    entries.value = found
    searchMode.value = true
    selected.value = []
    if (!found.length) toast.success(t('netdiskSearchEmpty', { kw }))
  } catch (e) {
    logErr('NetdiskPage.search', e)
    toast.error(getErrorMessage(e))
  } finally {
    searching.value = false
  }
}

function exitSearch() {
  keyword.value = ''
  searchMode.value = false
  void loadDir(dir.value)
}

// ---- 文件管理 ----

function openMkdir() {
  showMkdir.value = true
}

async function submitMkdir(values: Record<string, string>) {
  const name = (values.name || '').trim()
  if (!name) return
  try {
    unwrap(await NetdiskMkdir(dir.value, name))
    showMkdir.value = false
    toast.success(t('netdiskMkdirDone', { name }))
    await refreshAfterChange()
  } catch (e) {
    logErr('NetdiskPage.mkdir', e)
    toast.error(getErrorMessage(e))
  }
}

function openRename(e: Entry) {
  renameTarget.value = e
}

async function submitRename(values: Record<string, string>) {
  const target = renameTarget.value
  const name = (values.name || '').trim()
  renameTarget.value = null
  if (!target || !name || name === target.name) return
  try {
    unwrap(await NetdiskRename(target.path, name))
    toast.success(t('netdiskRenameDone', { name }))
    await refreshAfterChange()
  } catch (e) {
    logErr('NetdiskPage.rename', e)
    toast.error(getErrorMessage(e))
  }
}

function openDelete(entriesToList: Entry[]) {
  if (!entriesToList.length) return
  deleteTargets.value = entriesToList
}

/** 确认框里最多列 10 个名字，剩下的折成一行计数——列表再长也不能把弹框撑破。 */
const deleteDetails = computed(() => {
  const list = deleteTargets.value.slice(0, 10).map((d) => d.name + (d.isDir ? ' /' : ''))
  if (deleteTargets.value.length > 10) {
    list.push(t('netdiskMoreItems', { count: deleteTargets.value.length - 10 }))
  }
  return list
})

async function confirmDelete() {
  const targets = deleteTargets.value
  if (!targets.length) return
  deleteTargets.value = []
  try {
    const res = unwrap<{ deleted: number; failed: string[] }>(
      await NetdiskDelete(JSON.stringify(targets.map((t) => t.path))),
    )
    if (res?.failed?.length) {
      toast.error(t('netdiskDeletePartial', { deleted: res.deleted, failed: res.failed.length }))
    } else {
      toast.success(t('netdiskDeleted', { count: targets.length }))
    }
    selected.value = []
    await refreshAfterChange()
  } catch (e) {
    logErr('NetdiskPage.delete', e)
    toast.error(getErrorMessage(e))
  }
}

// ---- 上传 / 批量下载 / 交给下载器 ----

/** 未完成的上传数：归零时刷一次目录（逐个刷会让列表反复闪）。 */
const uploadPending = ref(0)

async function uploadFiles() {
  try {
    const paths = unwrap<string[]>(await SelectLocalFiles(t('netdiskUploadTitle'), '所有文件', '*.*')) ?? []
    if (!paths.length) return
    unwrap(await NetdiskUpload(JSON.stringify(paths), dir.value))
    uploadPending.value += paths.length
    toast.success(t('netdiskUploadStarted', { count: paths.length }))
  } catch (e) {
    logErr('NetdiskPage.upload', e)
    toast.error(getErrorMessage(e))
  }
}

async function downloadSelected() {
  const files = selectedFiles.value
  if (!files.length) {
    toast.error(t('netdiskNoFilesSelected'))
    return
  }
  try {
    const destDir = unwrap<string>(await SelectLocalDirectory(t('netdiskPickDir'))) || ''
    if (!destDir) return
    unwrap(await NetdiskDownloadBatch(JSON.stringify(files.map((f) => f.path)), destDir))
    toast.success(t('netdiskBatchStarted', { count: files.length }))
  } catch (e) {
    logErr('NetdiskPage.downloadBatch', e)
    toast.error(getErrorMessage(e))
  }
}

/** 交给 aria2：走本地代理地址，进度在「下载器」页看，不在这里重复画一份。 */
async function sendToDownloader(items: Entry[]) {
  const files = items.filter((i) => !i.isDir)
  if (!files.length) {
    toast.error(t('netdiskNoFilesSelected'))
    return
  }
  try {
    const res = unwrap<{ added: number; failed: string[] }>(
      await NetdiskSendToDownloader(JSON.stringify(files.map((f) => f.path))),
    )
    if (res?.failed?.length) {
      toast.error(t('netdiskSendPartial', { added: res.added, failed: res.failed.length }))
      return
    }
    toast.success(t('netdiskSentToDownloader', { count: res?.added ?? files.length }))
  } catch (e) {
    logErr('NetdiskPage.sendToDownloader', e)
    toast.error(getErrorMessage(e))
  }
}

// ---- 预览 ----

/**
 * base64url 编码远端路径。
 * 与后端 base64.RawURLEncoding 严格对应（+ → -、/ → _、去掉 =），否则中文路径解不回来。
 */
function b64url(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let bin = ''
  bytes.forEach((b) => {
    bin += String.fromCharCode(b)
  })
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** 拼预览地址。text=true 时后端只回开头 512KB（文本预览用）。 */
function previewUrl(e: Entry, text = false): string {
  const base = gw.value?.previewBase
  if (!base) return ''
  return base + b64url(e.path) + (text ? '&text=1' : '')
}

/** 文本类预览才需要主动取内容；媒体类交给 <video>/<img> 自己流式拉。 */
function isTextKind(kind: string): boolean {
  return kind === 'text' || kind === 'code' || kind === 'markdown'
}

async function openFile(e: Entry) {
  if (!gw.value?.previewBase) {
    toast.error(t('netdiskPreviewUnavailable'))
    return
  }
  // 同一个文件重复触发（双击会先来两次 click）时不再重复拉文本。
  if (preview.value?.path === e.path && !isTextKind(e.kind)) return
  preview.value = e
  previewText.value = ''
  if (!isTextKind(e.kind)) return
  previewLoading.value = true
  try {
    const res = await fetch(previewUrl(e, true))
    if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`)
    previewText.value = await res.text()
  } catch (err) {
    logErr('NetdiskPage.preview', err)
    previewText.value = t('netdiskPreviewFailed') + '：' + getErrorMessage(err)
  } finally {
    previewLoading.value = false
  }
}

function closePreview() {
  preview.value = null
  previewText.value = ''
  previewLoading.value = false
}

/** 行双击：目录进目录，文件开预览。 */
function onRowActivate(e: Entry) {
  if (e.isDir) void loadDir(e.path)
  else void openFile(e)
}

function onKeydown(ev: KeyboardEvent) {
  if (ev.key === 'Escape' && preview.value) {
    closePreview()
    return
  }
  // Delete 删除选中项，但不抢输入框的按键（用户可能正在搜索框里删字符）。
  const tag = (ev.target as HTMLElement | null)?.tagName
  if (ev.key === 'Delete' && !preview.value && selected.value.length && tag !== 'INPUT' && tag !== 'TEXTAREA') {
    openDelete(selectedEntries.value)
  }
}

async function openAdmin() {
  const url = gw.value?.adminUrl
  if (!url) return
  try {
    await openExternal(url)
  } catch (e) {
    logErr('NetdiskPage.openAdmin', e)
    toast.error(getErrorMessage(e))
  }
}

function onDownloadProgress(payload: any) {
  const p = payload?.data ?? payload
  if (!p?.path) return
  upsertJob('down', p, 0, 0)
}

function onUploadProgress(payload: any) {
  const p = payload?.data ?? payload
  if (!p?.path) return
  upsertJob('up', p, p.index || 0, p.count || 0)
  if ((p.phase === 'done' || p.phase === 'error') && uploadPending.value > 0) {
    uploadPending.value--
    // 全部传完才刷一次：每传一个刷一次会让列表在批量上传时反复闪。
    if (uploadPending.value === 0) void refreshAfterChange()
  }
}

/**
 * upsert 一行进度。
 * 主键用「方向 + 远端路径」而不是文件名：不同目录下的同名文件、或者
 * 正在上传/下载同一个名字时，按名字匹配会把进度写到别人的行上。
 */
function upsertJob(dir: 'up' | 'down', p: any, index: number, count: number) {
  const key = dir + ':' + p.path
  const phase = p.phase || 'downloading'
  const row = jobs.value.find((j) => j.key === key)
  if (row) {
    row.done = p.done
    row.total = p.total
    row.percent = p.percent
    row.phase = phase
    row.err = p.err || ''
  } else {
    jobs.value.unshift({
      key,
      name: p.name,
      dir,
      index,
      count,
      done: p.done,
      total: p.total,
      percent: p.percent,
      phase,
      err: p.err || '',
    })
  }
  if (phase === 'error') toast.error(p.err || t('netdiskFailed'))
  // 只留最近 20 行：页面常开时进度行不该无限堆积。
  if (jobs.value.length > 20) jobs.value.length = 20
}

/** 写操作完成后的刷新：在搜索模式里就重搜一次，别把用户甩回目录。 */
async function refreshAfterChange() {
  if (searchMode.value) await runSearch()
  else await loadDir(dir.value)
}

function onEnvProgress(payload: any) {
  const p = payload?.data ?? payload
  if (!p || p.runtime !== 'openlist') return
  if (p.stage === 'download' && p.total > 0) {
    installText.value = t('netdiskInstalling') + ' ' + Math.floor((p.written * 100) / p.total) + '%'
    return
  }
  if (p.stage === 'log' && p.message) {
    installText.value = p.message
    return
  }
  if (p.stage === 'done') {
    installText.value = t('netdiskInstallDone')
    busy.value = ''
    // 装完自动起网关，省掉一次「装好了，然后呢」。
    void startGateway()
    return
  }
  if (p.stage === 'error') {
    busy.value = ''
    installText.value = ''
    toast.error(p.message || t('netdiskFailed'))
  }
}

let offDownload: (() => void) | null = null
let offUpload: (() => void) | null = null
let offEnv: (() => void) | null = null

onMounted(async () => {
  offDownload = Events.On('quickdock:netdisk:download', onDownloadProgress)
  offUpload = Events.On('quickdock:netdisk:upload', onUploadProgress)
  offEnv = Events.On('quickdock:env:progress', onEnvProgress)
  window.addEventListener('keydown', onKeydown)
  await refreshStatus()
})

onUnmounted(() => {
  offDownload?.()
  offUpload?.()
  offEnv?.()
  window.removeEventListener('keydown', onKeydown)
})

/** 进度条宽度：Content-Length 未知（total<=0）时给一段固定宽度表示「在跑但不知道还剩多少」。 */
function jobWidth(j: JobRow): string {
  if (j.phase === 'done') return '100%'
  if (j.total > 0) return Math.min(100, Math.max(0, j.percent)) + '%'
  return '35%'
}

/** 表头排序指示：当前列高亮并显示方向，其它列淡显。 */
function sortMark(key: SortKey): string {
  if (sortKey.value !== key) return 'is-idle'
  return sortAsc.value ? 'is-asc' : 'is-desc'
}

/** 时间排序用：解析失败的当 0，不至于让整列乱序。 */
function timeValue(s: string): number {
  const v = new Date(s).getTime()
  return Number.isNaN(v) ? 0 : v
}

const mkdirFields = computed(() => [
  {
    key: 'name',
    label: t('netdiskFolderName'),
    type: 'text' as const,
    placeholder: t('netdiskFolderPlaceholder'),
  },
])

const renameFields = computed(() => [
  {
    key: 'name',
    label: t('netdiskNewName'),
    type: 'text' as const,
    placeholder: t('netdiskFolderPlaceholder'),
  },
])

/** 修改时间只留「YYYY-MM-DD HH:mm」，WebDAV 给的是 RFC1123。 */
function shortTime(s: string): string {
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
</script>

<template>
  <div class="netdisk-page">
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title"><Cloud :size="16" />{{ t('netdiskTitle') }}</h2>
      </template>
      <!-- 副标题与标题同一行；状态走中间 .qd-page-fill，操作按钮走 .qd-page-actions 靠右，
           和其它一级页的「标题 | 中间控件 | 右侧动作」同一套排布。 -->
      <span class="qd-page-sub qd-page-sub-inline">{{ t('netdiskSubtitle') }}</span>
      <div class="qd-page-fill">
        <span v-if="gw" class="nd-chip" :class="gw.running ? 'is-on' : 'is-off'">
          {{ gw.running ? t('netdiskGatewayOn', { version: gw.version, port: gw.port }) : t('netdiskGatewayOff') }}
        </span>
      </div>
      <div class="qd-page-actions">
        <!-- 页头按钮走功能页的 .qd-btn，不再用设置页那套 .btn.btn-secondary -->
        <button class="qd-btn" :disabled="!gw" @click="openAdmin">
          <Settings2 :size="14" />{{ t('netdiskOpenAdmin') }}
        </button>
        <button v-if="gw?.installed" class="qd-btn" :disabled="!!busy" @click="restartGateway">
          <Power :size="14" />{{ busy === 'restart' ? t('netdiskRestarting') : t('netdiskRestartGateway') }}
        </button>
        <button class="qd-btn" :disabled="loading" @click="refreshStatus">
          <RefreshCw :size="14" />{{ t('netdiskRefresh') }}
        </button>
      </div>
    </PageHeader>

    <!-- 未安装：给一键安装 -->
    <section v-if="gw && !gw.installed" class="nd-empty">
      <Cloud :size="28" class="nd-empty-icon" />
      <p class="nd-empty-title">{{ t('netdiskNotInstalled') }}</p>
      <p class="nd-empty-desc">{{ t('netdiskNotInstalledHint') }}</p>
      <button class="btn btn-primary" :disabled="busy === 'install'" @click="installGateway">
        <Download :size="14" />{{ busy === 'install' ? installText || t('netdiskInstalling') : t('netdiskInstall') }}
      </button>
    </section>

    <!-- 已安装未运行：给启动 -->
    <section v-else-if="gw && !gw.running" class="nd-empty">
      <Cloud :size="28" class="nd-empty-icon" />
      <p class="nd-empty-title">{{ t('netdiskStopped') }}</p>
      <p class="nd-empty-desc">{{ t('netdiskStoppedHint') }}</p>
      <button class="btn btn-primary" :disabled="busy === 'start'" @click="startGateway">
        {{ busy === 'start' ? t('netdiskStarting') : t('netdiskStart') }}
      </button>
    </section>

    <!-- 已运行但一个存储都没加（或根目录列不出来）：给「去后台添加 + 刷新」，
         首屏不再撞那条没有下一步动作的 HTTP 500。 -->
    <section v-else-if="needSetup" class="nd-empty">
      <Cloud :size="28" class="nd-empty-icon" />
      <p class="nd-empty-title">{{ t('netdiskNoStorageTitle') }}</p>
      <p class="nd-empty-desc">{{ t('netdiskNoStorageDesc') }}</p>
      <div class="nd-empty-acts">
        <button class="qd-btn qd-btn-primary" @click="openAdmin">
          <Settings2 :size="14" />{{ t('netdiskOpenAdmin') }}
        </button>
        <button class="qd-btn" @click="refreshStatus">
          <RefreshCw :size="14" />{{ t('netdiskRefresh') }}
        </button>
      </div>
    </section>

    <template v-else-if="ready">
      <div class="nd-bar">
        <button class="btn btn-secondary nd-up" :disabled="dir === '/' || loading" @click="goUp">
          <ArrowUp :size="14" />{{ t('netdiskUp') }}
        </button>
        <nav class="nd-crumbs">
          <template v-for="(c, i) in crumbs" :key="c.path">
            <button v-if="i < crumbs.length - 1" class="nd-crumb" @click="loadDir(c.path)">{{ c.name }}</button>
            <span v-else class="nd-crumb is-current">{{ c.name }}</span>
            <span v-if="i < crumbs.length - 1" class="nd-sep">/</span>
          </template>
        </nav>
        <div class="nd-tools">
          <div class="nd-search">
            <Search :size="13" class="nd-search-icon" />
            <input
              v-model="keyword"
              class="nd-search-input"
              :placeholder="t('netdiskSearchPlaceholder')"
              @keyup.enter="runSearch"
            />
            <!-- 输入后出现：当前目录过滤是实时的，深搜要按这个 -->
            <button
              v-if="keyword.trim()"
              class="nd-search-deep"
              :title="t('netdiskSearchDeep')"
              :disabled="searching"
              @click="runSearch"
            >
              {{ searching ? '…' : t('netdiskSearchDeepShort') }}
            </button>
          </div>
          <button class="btn btn-secondary nd-tool" :title="t('netdiskNewFolder')" @click="openMkdir">
            <FolderPlus :size="14" /><span class="nd-tool-text">{{ t('netdiskNewFolder') }}</span>
          </button>
          <button class="btn btn-secondary nd-tool" :title="t('netdiskUpload')" @click="uploadFiles">
            <Upload :size="14" /><span class="nd-tool-text">{{ t('netdiskUpload') }}</span>
          </button>
          <button class="btn btn-secondary nd-tool" :title="t('netdiskRefresh')" :disabled="loading" @click="refreshAfterChange">
            <RefreshCw :size="14" />
          </button>
        </div>
      </div>

      <!-- 搜索模式提示：结果来自递归搜索，点「返回」回到目录 -->
      <div v-if="searchMode" class="nd-search-banner">
        <Search :size="13" />
        <span>{{ t('netdiskSearchResult', { kw: keyword, count: entries.length }) }}</span>
        <button class="nd-crumb" @click="exitSearch">{{ t('netdiskSearchBack') }}</button>
      </div>

      <!-- 批量操作条：只有选中东西时才出现，避免常驻一排灰按钮 -->
      <div v-if="selected.length" class="nd-bulk">
        <span class="nd-bulk-count">{{ t('netdiskSelectedCount', { count: selected.length }) }}</span>
        <button class="btn btn-secondary nd-tool" @click="downloadSelected">
          <Download :size="14" />{{ t('netdiskDownloadSelected') }}
        </button>
        <button class="btn btn-secondary nd-tool" @click="sendToDownloader(selectedEntries)">
          <Send :size="14" />{{ t('netdiskToDownloader') }}
        </button>
        <button class="btn btn-secondary nd-tool is-danger" @click="openDelete(selectedEntries)">
          <Trash2 :size="14" />{{ t('netdiskDelete') }}
        </button>
        <button class="nd-crumb nd-bulk-clear" @click="selected = []">{{ t('netdiskClearSelection') }}</button>
      </div>

      <div class="nd-list">
        <div class="nd-row nd-row-head">
          <span class="nd-col-check">
            <input type="checkbox" :checked="allSelected" :title="t('netdiskSelectAll')" @change="toggleAll" />
          </span>
          <button class="nd-col-name nd-sort" @click="toggleSort('name')">
            {{ t('netdiskName') }}<ArrowUp :size="11" class="nd-sort-mark" :class="sortMark('name')" />
          </button>
          <button class="nd-col-size nd-sort" @click="toggleSort('size')">
            {{ t('netdiskSize') }}<ArrowUp :size="11" class="nd-sort-mark" :class="sortMark('size')" />
          </button>
          <button class="nd-col-time nd-sort" @click="toggleSort('time')">
            {{ t('netdiskModified') }}<ArrowUp :size="11" class="nd-sort-mark" :class="sortMark('time')" />
          </button>
          <span class="nd-col-act">{{ t('netdiskActions') }}</span>
        </div>
        <div v-if="loading" class="nd-tip">{{ t('netdiskLoading') }}</div>
        <div v-else-if="!displayEntries.length" class="nd-tip">
          {{ keyword.trim() && !searchMode ? t('netdiskSearchEmpty', { kw: keyword }) : t('netdiskEmptyDir') }}
        </div>
        <template v-else>
          <div
            v-for="e in displayEntries"
            :key="e.path"
            class="nd-row"
            :class="{ 'is-dir': e.isDir, 'is-selected': isSelected(e.path) }"
            @dblclick="onRowActivate(e)"
          >
            <span class="nd-col-check" @click.stop>
              <input type="checkbox" :checked="isSelected(e.path)" @change="toggleSel(e.path)" />
            </span>
            <span class="nd-col-name" :title="e.isDir ? e.name : e.name + ' · ' + t('netdiskPreviewHint')">
              <Folder v-if="e.isDir" :size="14" class="nd-icon-dir" />
              <FileText v-else :size="14" class="nd-icon-file" />
              <button v-if="e.isDir" class="nd-name-btn" @click="openDir(e)">{{ e.name }}</button>
              <button v-else class="nd-name-btn" @click="openFile(e)">{{ e.name }}</button>
            </span>
            <span class="nd-col-size">{{ e.isDir ? '—' : formatBytes(e.size) }}</span>
            <span class="nd-col-time">{{ shortTime(e.modified) }}</span>
            <span class="nd-col-act">
              <template v-if="!e.isDir">
                <button class="nd-act-btn" :title="t('netdiskDownload')" @click="downloadFile(e)">
                  <Download :size="14" />
                </button>
                <button class="nd-act-btn" :title="t('netdiskToDownloader')" @click="sendToDownloader([e])">
                  <Send :size="14" />
                </button>
              </template>
              <button class="nd-act-btn" :title="t('netdiskRename')" @click="openRename(e)">
                <Pencil :size="14" />
              </button>
              <button class="nd-act-btn is-danger" :title="t('netdiskDelete')" @click="openDelete([e])">
                <Trash2 :size="14" />
              </button>
            </span>
          </div>
        </template>
      </div>

      <section v-if="jobs.length" class="nd-jobs">
        <p class="nd-jobs-title">{{ t('netdiskTransfers') }}</p>
        <div v-for="j in jobs" :key="j.key" class="nd-job">
          <span class="nd-job-dir" :class="'is-' + j.dir" :title="j.dir === 'up' ? t('netdiskUpload') : t('netdiskDownload')">
            <Upload v-if="j.dir === 'up'" :size="12" />
            <Download v-else :size="12" />
          </span>
          <span class="nd-job-name" :title="j.name">
            {{ j.name }}<em v-if="j.count > 1" class="nd-job-idx">{{ j.index }}/{{ j.count }}</em>
          </span>
          <div class="nd-job-bar"><div class="nd-job-fill" :class="'is-' + j.phase" :style="{ width: jobWidth(j) }"></div></div>
          <span class="nd-job-state">
            {{ j.phase === 'done' ? t('netdiskDone') : j.phase === 'error' ? t('netdiskFailed') : j.percent + '%' }}
          </span>
        </div>
      </section>
    </template>

    <div v-else class="nd-tip">{{ t('netdiskLoading') }}</div>

    <!-- 预览浮层：媒体类交给浏览器流式拉（后端代理透传 Range，可拖进度条） -->
    <div v-if="preview" class="nd-pv-mask" @click.self="closePreview">
      <div class="nd-pv">
        <header class="nd-pv-head">
          <span class="nd-pv-title" :title="preview.name">{{ preview.name }}</span>
          <span class="nd-pv-size">{{ formatBytes(preview.size) }}</span>
          <button class="btn btn-secondary nd-dl" @click="downloadFile(preview)">
            <Download :size="13" />{{ t('netdiskDownload') }}
          </button>
          <button class="nd-pv-close" :title="t('netdiskClose')" @click="closePreview">
            <X :size="16" />
          </button>
        </header>
        <div class="nd-pv-body">
          <video
            v-if="preview.kind === 'video'"
            class="nd-pv-media"
            :src="previewUrl(preview)"
            controls
            autoplay
          ></video>
          <audio
            v-else-if="preview.kind === 'audio'"
            class="nd-pv-audio"
            :src="previewUrl(preview)"
            controls
            autoplay
          ></audio>
          <img
            v-else-if="preview.kind === 'image'"
            class="nd-pv-image"
            :src="previewUrl(preview)"
            :alt="preview.name"
          />
          <iframe v-else-if="preview.kind === 'pdf'" class="nd-pv-frame" :src="previewUrl(preview)"></iframe>
          <pre v-else-if="isTextKind(preview.kind)" class="nd-pv-text">{{ previewLoading ? t('netdiskLoading') : previewText }}</pre>
          <div v-else class="nd-pv-tip">{{ t('netdiskPreviewUnsupported') }}</div>
        </div>
      </div>
    </div>

    <!-- 新建文件夹 / 重命名：同一个 CreateDialog，只有一个名字字段 -->
    <CreateDialog
      :visible="showMkdir"
      :title="t('netdiskNewFolder')"
      :fields="mkdirFields"
      @confirm="submitMkdir"
      @cancel="showMkdir = false"
    />
    <CreateDialog
      :visible="!!renameTarget"
      :title="t('netdiskRename')"
      :fields="renameFields"
      :editValues="renameTarget ? { name: renameTarget.name } : undefined"
      @confirm="submitRename"
      @cancel="renameTarget = null"
    />

    <!-- 删除确认：把要删的名字列出来。批量删是最容易误操作的一步 -->
    <ConfirmDialog
      :visible="deleteTargets.length > 0"
      :message="t('netdiskDeleteConfirm', { count: deleteTargets.length })"
      :details="deleteDetails"
      @confirm="confirmDelete"
      @cancel="deleteTargets = []"
    />
  </div>
</template>

<style scoped>
.netdisk-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  padding: var(--space-6);
  gap: var(--space-5);
  color: var(--color-text-primary);
}

/* 页头通栏：抵消容器的上/左/右 16px padding，让公共 .qd-page-header 贴顶并撑满整行。
   只动页头、不给内容块统一补 margin —— .nd-empty 靠 margin:auto 居中，.nd-pv-mask 是
   position:fixed + inset:0 的遮罩，给它们加左右 margin 会一个偏左、一个露出没盖住的边。 */
.netdisk-page > :deep(.qd-page-header) {
  margin: calc(0px - var(--space-6)) calc(0px - var(--space-6)) 0;
}

.nd-chip {
  padding: var(--space-1) var(--space-4);
  border-radius: var(--radius-full);
  font-size: 12px;
  border: 1px solid var(--color-border);
}
.nd-chip.is-on {
  color: var(--color-success);
  background: var(--color-success-bg);
  border-color: transparent;
}
.nd-chip.is-off {
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-color: transparent;
}

.nd-empty {
  margin: auto;
  max-width: 460px;
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  gap: var(--space-4);
  padding: var(--space-9);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
}

/* 引导态里并排的两个动作：主操作 + 刷新，窄窗口允许换行 */
.nd-empty-acts {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  flex-wrap: wrap;
}
.nd-empty-icon {
  color: var(--color-text-muted);
}
.nd-empty-title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}
.nd-empty-desc {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: var(--color-text-secondary);
}

.nd-bar {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  min-width: 0;
}
.nd-up {
  flex: none;
}
.nd-crumbs {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  overflow: hidden;
}
.nd-crumb {
  border: none;
  background: none;
  padding: 0;
  font-size: 13px;
  color: var(--color-text-secondary);
  cursor: pointer;
}
.nd-crumb:hover {
  color: var(--color-accent);
}
.nd-crumb.is-current {
  color: var(--color-text-primary);
  cursor: default;
}
.nd-sep {
  color: var(--color-text-disabled);
}
/* ---- 工具栏：搜索 + 新建 + 上传 + 刷新 ---- */
.nd-tools {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: none;
}
.nd-search {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 0 var(--space-3);
  height: 28px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
}
.nd-search:focus-within {
  border-color: var(--color-accent);
}
.nd-search-icon {
  color: var(--color-text-muted);
  flex: none;
}
.nd-search-input {
  width: 148px;
  border: none;
  outline: none;
  background: none;
  color: var(--color-text-primary);
  font-size: 12.5px;
  font-family: inherit;
}
.nd-search-deep {
  flex: none;
  border: none;
  background: none;
  padding-left: var(--space-2);
  border-left: 1px solid var(--color-border);
  color: var(--color-accent);
  font-size: 12px;
  cursor: pointer;
}
.nd-search-deep:disabled {
  color: var(--color-text-muted);
  cursor: default;
}
.nd-tool {
  padding: var(--space-1) var(--space-4);
  font-size: 12px;
  gap: var(--space-2);
}
.nd-tool.is-danger {
  color: var(--color-danger);
}

/* ---- 搜索提示 / 批量操作条 ---- */
.nd-search-banner,
.nd-bulk {
  flex: none;
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  font-size: 12.5px;
  color: var(--color-text-secondary);
}
.nd-bulk-count {
  font-weight: 600;
  color: var(--color-text-primary);
}
.nd-bulk {
  flex-wrap: wrap;
}
.nd-bulk-clear {
  margin-left: auto;
}

/* 窄窗口下按钮只留图标：搜索框和面包屑比按钮文字更值得占位置 */
@media (max-width: 1100px) {
  .nd-tool-text {
    display: none;
  }
  .nd-search-input {
    width: 110px;
  }
}

.nd-list {
  flex: 1;
  min-height: 0;
  overflow: auto;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
}
.nd-row {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr) 96px 148px 148px;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--color-hairline);
  font-size: 13px;
}
.nd-row.is-selected {
  background: var(--color-bg-hover);
}
.nd-col-check {
  display: flex;
  align-items: center;
  justify-content: center;
}
.nd-col-check input {
  cursor: pointer;
  accent-color: var(--color-accent);
}
.nd-row:last-child {
  border-bottom: none;
}
.nd-row-head {
  color: var(--color-text-muted);
  font-size: 12px;
  background: var(--color-bg-tertiary);
  position: sticky;
  top: 0;
}
.nd-row.is-dir:hover {
  background: var(--color-bg-hover);
}
.nd-col-name {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
}
.nd-name-btn,
.nd-name-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.nd-name-btn {
  border: none;
  background: none;
  padding: 0;
  font: inherit;
  color: var(--color-text-primary);
  text-align: left;
  cursor: pointer;
}
.nd-name-btn:hover {
  color: var(--color-accent);
}
.nd-icon-dir {
  color: var(--color-accent);
  flex: none;
}
.nd-icon-file {
  color: var(--color-text-muted);
  flex: none;
}
.nd-col-size,
.nd-col-time {
  color: var(--color-text-secondary);
  font-size: 12px;
}
.nd-col-act {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: var(--space-1);
}
.nd-sort {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  border: none;
  background: none;
  padding: 0;
  font: inherit;
  color: inherit;
  cursor: pointer;
  text-align: left;
}
.nd-sort:hover {
  color: var(--color-text-primary);
}
.nd-sort-mark {
  transition: opacity 0.15s, transform 0.15s;
}
.nd-sort-mark.is-idle {
  opacity: 0.25;
}
.nd-sort-mark.is-asc {
  opacity: 1;
  color: var(--color-accent);
}
.nd-sort-mark.is-desc {
  opacity: 1;
  color: var(--color-accent);
  transform: rotate(180deg);
}
/* 行内操作走图标按钮：四个动作塞文字会挤成两行，标题气泡足够解释 */
.nd-act-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: none;
  border-radius: var(--radius-sm);
  background: none;
  color: var(--color-text-secondary);
  cursor: pointer;
}
.nd-act-btn:hover {
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
}
.nd-act-btn.is-danger:hover {
  color: var(--color-danger);
}
.nd-dl {
  padding: var(--space-1) var(--space-4);
  font-size: 12px;
}
.nd-tip {
  padding: var(--space-9);
  text-align: center;
  color: var(--color-text-muted);
  font-size: 13px;
}

.nd-jobs {
  flex: none;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  padding: var(--space-4) var(--space-5);
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  max-height: 160px;
  overflow: auto;
}
.nd-jobs-title {
  margin: 0;
  font-size: 12px;
  color: var(--color-text-muted);
}
.nd-job {
  display: grid;
  grid-template-columns: 14px minmax(0, 220px) minmax(0, 1fr) 60px;
  align-items: center;
  gap: var(--space-4);
  font-size: 12px;
}
.nd-job-dir {
  display: flex;
  align-items: center;
}
.nd-job-dir.is-up {
  color: var(--color-accent);
}
.nd-job-dir.is-down {
  color: var(--color-success);
}
.nd-job-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.nd-job-idx {
  margin-left: var(--space-2);
  font-style: normal;
  color: var(--color-text-muted);
}
.nd-job-bar {
  height: 4px;
  border-radius: var(--radius-full);
  background: var(--color-bg-tertiary);
  overflow: hidden;
}
.nd-job-fill {
  height: 100%;
  background: var(--color-accent);
  transition: width 0.2s linear;
}
.nd-job-fill.is-done {
  background: var(--color-success);
}
.nd-job-fill.is-error {
  background: var(--color-danger);
}
.nd-job-state {
  text-align: right;
  color: var(--color-text-secondary);
}

/* ---- 预览浮层 ---- */
.nd-pv-mask {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-7);
  background: rgba(0, 0, 0, 0.62);
}
.nd-pv {
  display: flex;
  flex-direction: column;
  width: min(1100px, 100%);
  height: min(760px, 100%);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  overflow: hidden;
  box-shadow: 0 18px 48px rgba(0, 0, 0, 0.45);
}
.nd-pv-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--color-hairline);
}
.nd-pv-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 600;
}
.nd-pv-size {
  flex: none;
  font-size: 12px;
  color: var(--color-text-muted);
}
.nd-pv-head .nd-dl {
  margin-left: auto;
}
.nd-pv-close {
  flex: none;
  display: flex;
  align-items: center;
  border: none;
  background: none;
  padding: 0;
  color: var(--color-text-muted);
  cursor: pointer;
}
.nd-pv-close:hover {
  color: var(--color-text-primary);
}
.nd-pv-body {
  flex: 1;
  min-height: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 视频/图片用纯黑底：比面板底色更接近播放器的观感，也不会露出页面纹理 */
  background: #000;
}
.nd-pv-media,
.nd-pv-image {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}
.nd-pv-audio {
  width: min(560px, 90%);
}
.nd-pv-frame {
  width: 100%;
  height: 100%;
  border: none;
  background: #fff;
}
.nd-pv-text {
  margin: 0;
  width: 100%;
  height: 100%;
  overflow: auto;
  padding: var(--space-5);
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Consolas, monospace);
  font-size: 12.5px;
  line-height: 1.65;
  color: #cfd6e4;
  white-space: pre-wrap;
  word-break: break-word;
}
.nd-pv-tip {
  padding: var(--space-9);
  text-align: center;
  font-size: 13px;
  color: var(--color-text-muted);
}
</style>
