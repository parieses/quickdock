// useDownloadTasks.ts — 下载页的数据与动作。
//
// 前端只跟 Wails 绑定打交道：不知道 aria2 的端口、secret，也不碰 JSON-RPC。
// 进度走后端 1s 一次的 quickdock:dl:tasks 整表快照（活跃任务快路径）；
// 页面切回来时再用 DlList() 兜底拉一次，避免「事件在页面卸载期间丢了」。
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from './useToast'
import { usePollScheduler } from './usePollScheduler'
import {
  DlStatus, DlEnable, DlDisable, DlAdd, DlBatch, DlBatchAdd, DlList,
  DlPause, DlForcePause, DlResume, DlRemove,
  DlPauseAll, DlResumeAll, DlPurgeResults,
  DlAddTorrent, DlAddMetalink, DlOptions, DlSetOptions,
  DlDetail, DlSetTaskOption, DlSetFileSelection,
  DlGlobalStat, DlGlobalOptions, DlSetGlobalOption,
  DlEngineInfo, DlShutdown, DlReveal, DlDiskSpace,
  DlChangePosition, DlChangeUri,
} from '../../bindings/quickdock/services/download/downloadservice'
import { SelectLocalFile } from '../../bindings/quickdock/services/appservice'

// DlBatch 的返回结构：ApiResult.data 是 any，Wails 不会为它生成模型，
// 与 DlDiskSpace 一样由前端手写，字段名与 Go 的 json tag 对齐。
export interface DlBatchAction {
  gid: string
  ok: boolean
  error: string
}

export interface DlBatchResult {
  succeeded: number
  failed: number
  actions: DlBatchAction[]
}

export interface DlTask {
  gid: string
  name: string
  url: string
  status: string
  total: number
  completed: number
  speed: number
  progress: number
  dir: string
  path: string
  source: string
  error: string
  createdAt: number
  uploadSpeed: number
  uploaded: number
  seeders: number
  connections: number
  infoHash: string
  isTorrent: boolean
  torrentName: string
  shareRatio: number
  numPieces: number
  pieceLength: number
  fileCount: number
}

export interface DlEngineStatus {
  installed: boolean
  running: boolean
  port: number
  version: string
  downloadDir: string
  maxConcurrent: number
  split: number
  downloadLimit: string
  uploadLimit: string
  remote: boolean
  host: string
  wsConnected: boolean
}

export interface DlDiskSpace {
  path: string
  total: number
  free: number
  used: number
}

export interface DlSchedule {
  enabled: boolean
  start: string
  end: string
  downloadLimit: string
  uploadLimit: string
}

export interface DlOptionsView {
  downloadDir: string
  maxConcurrent: number
  split: number
  downloadLimit: string
  uploadLimit: string
  listenPort: number
  dhtListenPort: number
  btTracker: string
  btMaxPeers: number
  seedRatio: string
  seedTime: string
  minSplitSize: string
  userAgent: string
  proxy: string
  disableDht: boolean
  disableDht6: boolean
  disablePeerExchange: boolean
  disableSaveMetadata: boolean
  disableFollowTorrent: boolean
  btForceEncryption: boolean
  btRequireCrypto: boolean
  btRemoveUnselected: boolean
  checkIntegrity: boolean
  autoOpenFolder: boolean
  autoCategory: boolean
  categoryRules: Record<string, string>
  schedule: DlSchedule
  remoteEnabled: boolean
  remoteHost: string
  remotePort: number
  remoteSecret: string
  remoteSecretSet: boolean
  remoteSecure: boolean
  remotePath: string
  notifyDisabled: boolean
}

export interface DlGlobalStat {
  downloadSpeed: number
  uploadSpeed: number
  active: number
  waiting: number
  stopped: number
  stoppedTotal: number
}

export interface DlUriView { uri: string; status: string }
export interface DlFileView {
  index: number
  path: string
  length: number
  completed: number
  selected: boolean
  uris: DlUriView[]
}
export interface DlPeerView {
  peerId: string
  ip: string
  port: number
  downloadSpeed: number
  uploadSpeed: number
  seeder: boolean
  amChoking: boolean
  peerChoking: boolean
}
export interface DlServerView {
  index: number
  uri: string
  currentUri: string
  status: string
  speed: number
  error: string
}
export interface DlDetail {
  gid: string
  options: Record<string, string>
  files: DlFileView[]
  uris: DlUriView[]
  peers: DlPeerView[]
  servers: DlServerView[]
  announceList: string[][]
  torrentComment: string
  torrentMode: string
  torrentCreationDate: number
}
export interface DlEngineInfo {
  version: string
  features: string[]
  sessionId: string
  methods: string[]
  options: Record<string, string>
}

export function formatBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatSpeed(n: number): string {
  return `${formatBytes(n)}/s`
}

export function formatEta(bytesLeft: number, speed: number): string {
  if (speed <= 0 || bytesLeft <= 0) return '--:--'
  const sec = Math.ceil(bytesLeft / speed)
  if (sec >= 3600) {
    const h = Math.floor(sec / 3600)
    return `${h}:${String(Math.floor((sec % 3600) / 60)).padStart(2, '0')}:${String(sec % 60).padStart(2, '0')}`
  }
  return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`
}

export function formatTime(ms: number): string {
  if (!ms) return '-'
  return new Date(ms).toLocaleString()
}

export function formatRatio(r: number): string {
  return `${(r || 0).toFixed(2)}`
}

const EMPTY_GLOBAL: DlGlobalStat = {
  downloadSpeed: 0, uploadSpeed: 0, active: 0, waiting: 0, stopped: 0, stoppedTotal: 0,
}

// 下载页刻意不进 KeepAlive 白名单（切走要停 8s 兜底轮询省电），组件会被销毁重建。
// 这几个 ref 提到模块级做 stale-while-revalidate：切回来先显示上次的真实状态，
// 再静默刷新，避免组件刚挂载、DlStatus() 还没回来时先渲染出「引擎未安装」。
// 目前只有 DownloadPage.vue 一个调用方，共享不会串实例；事件订阅与 poll 仍是组件级。
const status = ref<DlEngineStatus>({
  installed: false, running: false, port: 0, version: '',
  downloadDir: '', maxConcurrent: 5, split: 8, downloadLimit: '', uploadLimit: '',
  remote: false, host: '', wsConnected: false,
})
const options = ref<DlOptionsView>({
  downloadDir: '', maxConcurrent: 5, split: 8, downloadLimit: '', uploadLimit: '',
  listenPort: 6881, dhtListenPort: 6881, btTracker: '', btMaxPeers: 55,
  seedRatio: '0.0', seedTime: '0', minSplitSize: '', userAgent: '', proxy: '',
  disableDht: false, disableDht6: false, disablePeerExchange: false,
  disableSaveMetadata: false, disableFollowTorrent: false,
  btForceEncryption: false, btRequireCrypto: false, btRemoveUnselected: false,
  checkIntegrity: false, autoOpenFolder: false, autoCategory: false,
  categoryRules: {}, schedule: { enabled: false, start: '23:00', end: '07:00', downloadLimit: '', uploadLimit: '' },
  remoteEnabled: false, remoteHost: '', remotePort: 6800, remoteSecret: '',
  remoteSecretSet: false, remoteSecure: false, remotePath: '/jsonrpc', notifyDisabled: false,
})
const tasks = ref<DlTask[]>([])
const global = ref<DlGlobalStat>({ ...EMPTY_GLOBAL })
// statusReady：至少成功拿到过一次 DlStatus()。首次结果回来前不显示未安装引导卡片，
// 否则一次普通切页会被误报成「引擎没装」；失败时保持 false，由 error 文案 + 重试承接。
const statusReady = ref(false)

export function useDownloadTasks() {
  const { t } = useI18n()
  const toast = useToast()

  const busy = ref(false)
  const error = ref('')
  const speedHistory = ref<number[]>([])
  const uploadHistory = ref<number[]>([])

  let offTasks: (() => void) | null = null
  let offNotice: (() => void) | null = null
  let offGlobal: (() => void) | null = null
  let offWs: (() => void) | null = null

  async function refreshStatus() {
    try {
      const s = unwrap<DlEngineStatus>(await DlStatus())
      if (s) {
        status.value = { ...s, uploadLimit: s.uploadLimit || '' }
        statusReady.value = true
        error.value = ''
      }
    } catch (e) {
      error.value = getErrorMessage(e)
      logErr('download/status', e)
      // 刷新失败时不能把上次成功的状态抹成「未安装」；statusReady 保持原值，
      // 首次就失败则停在检测态，由页面显示错误文案和重试按钮。
    }
  }

  async function refreshOptions() {
    try {
      const o = unwrap<DlOptionsView>(await DlOptions())
      if (o) options.value = o
    } catch (e) {
      logErr('download/options', e)
    }
  }

  async function refreshTasks() {
    // 引擎没跑时跳过兜底轮询，别每 8s 往日志里塞一条 RPC 失败。
    if (!status.value.running) return
    try {
      const list = unwrap<DlTask[]>(await DlList())
      tasks.value = Array.isArray(list) ? list : []
    } catch (e) {
      logErr('download/list', e)
    }
  }

  async function refreshGlobal() {
    try {
      const g = unwrap<DlGlobalStat>(await DlGlobalStat())
      if (g) global.value = g
    } catch (e) {
      logErr('download/global', e)
    }
  }

  // 远程引擎把文件写在远端，本机磁盘容量没有意义，直接跳过预检。
  async function checkDisk(dir: string): Promise<DlDiskSpace | null> {
    if (status.value.remote) return null
    try {
      return unwrap<DlDiskSpace>(await DlDiskSpace(dir))
    } catch (e) {
      logErr('download/disk', e)
      return null
    }
  }

  // 事件 1s 一次；兜底放到 8s，别和后端 ticker 一起挤 aria2 的单线程 RPC 队列。
  const poll = usePollScheduler(refreshTasks, { interval: 8000 })

  let lastSample = 0
  watch(tasks, (list) => {
    const now = Date.now()
    if (now - lastSample < 700) return
    lastSample = now
    const down = (list || []).reduce((sum, x) => sum + (x.status === 'active' ? x.speed || 0 : 0), 0)
    const up = (list || []).reduce((sum, x) => sum + (x.status === 'active' ? x.uploadSpeed || 0 : 0), 0)
    speedHistory.value = [...speedHistory.value, down].slice(-60)
    uploadHistory.value = [...uploadHistory.value, up].slice(-60)
  })

  async function enable() {
    busy.value = true
    try {
      unwrap(await DlEnable())
      toast.success(t('dlEnabled'))
      await refreshStatus()
      await refreshTasks()
      poll.start()
    } catch (e) {
      toast.error(getErrorMessage(e))
      logErr('download/enable', e)
    } finally {
      busy.value = false
    }
  }

  async function disable() {
    busy.value = true
    try {
      unwrap(await DlDisable())
      toast.success(t('dlDisabled'))
      await refreshStatus()
      poll.stop()
    } catch (e) {
      toast.error(getErrorMessage(e))
      logErr('download/disable', e)
    } finally {
      busy.value = false
    }
  }

  async function add(uri: string, dir = '', optsJSON = ''): Promise<boolean> {
    if (!uri.trim()) return false
    try {
      unwrap(await DlAdd(uri.trim(), dir, optsJSON))
      toast.success(t('dlAdded'))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e))
      logErr('download/add', e)
      return false
    }
  }

  async function addBatch(uris: string[], dir = ''): Promise<boolean> {
    if (!uris.length) return false
    try {
      unwrap(await DlBatchAdd(JSON.stringify(uris), dir))
      toast.success(t('dlAdded'))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e))
      logErr('download/batch', e)
      return false
    }
  }

  // 种子/Metalink 走本地文件对话框：前端只拿到路径，文件内容由 Go 读取并 base64 给 RPC。
  async function pickFile(title: string): Promise<string> {
    try {
      const p = unwrap<string>(await SelectLocalFile(title))
      return typeof p === 'string' ? p : ''
    } catch (e) {
      logErr('download/pickFile', e)
      return ''
    }
  }

  async function addTorrent(path: string, dir = '', optsJSON = ''): Promise<boolean> {
    if (!path) return false
    try {
      unwrap(await DlAddTorrent(path, dir, optsJSON))
      toast.success(t('dlAdded'))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e))
      logErr('download/addTorrent', e)
      return false
    }
  }

  async function addMetalink(path: string, dir = '', optsJSON = ''): Promise<boolean> {
    if (!path) return false
    try {
      unwrap(await DlAddMetalink(path, dir, optsJSON))
      toast.success(t('dlAdded'))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e))
      logErr('download/addMetalink', e)
      return false
    }
  }

  async function pause(gid: string) {
    try {
      unwrap(await DlPause(gid))
      await refreshTasks()
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/pause', e)
    }
  }

  async function forcePause(gid: string) {
    try {
      unwrap(await DlForcePause(gid))
      await refreshTasks()
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/forcePause', e)
    }
  }

  async function resume(gid: string) {
    try {
      unwrap(await DlResume(gid))
      await refreshTasks()
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/resume', e)
    }
  }

  async function remove(gid: string, force = false) {
    try {
      unwrap(await DlRemove(gid, force))
      toast.success(t('dlRemoved'))
      await refreshTasks()
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/remove', e)
    }
  }

  // 批量走后端 system.multicall：pause/resume 一次往返，remove 两次（查状态 + 删除）。
  // 旧写法是逐 gid 串行 await，选中几十项时会明显卡住界面，且单项失败会拖慢后面全部。
  async function batchAction(action: 'pause' | 'resume' | 'remove', gids: string[], force = false): Promise<number> {
    if (gids.length === 0) return 0
    try {
      const r = unwrap<DlBatchResult>(await DlBatch(action, JSON.stringify(gids), force))
      if (!r) return 0
      await refreshTasks()
      if (r.failed > 0) {
        const first = r.actions.find((a) => !a.ok)
        toast.error(t('dlBatchPartial', { failed: String(r.failed), reason: first?.error || '' }))
      }
      return r.succeeded
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/batch', e)
      return 0
    }
  }

  async function pauseMany(gids: string[]): Promise<number> {
    return batchAction('pause', gids)
  }

  async function resumeMany(gids: string[]): Promise<number> {
    return batchAction('resume', gids)
  }

  async function removeMany(items: Array<{ gid: string; force: boolean }>): Promise<number> {
    // force 是批级参数，强制移除与普通移除要分两批，否则语义会被整批统一掉。
    const normal = items.filter((x) => !x.force).map((x) => x.gid)
    const forced = items.filter((x) => x.force).map((x) => x.gid)
    let ok = 0
    if (normal.length) ok += await batchAction('remove', normal, false)
    if (forced.length) ok += await batchAction('remove', forced, true)
    return ok
  }

  // 原生批量：一次 RPC 完成，不再串行 N 次。
  async function pauseAll(force = false): Promise<boolean> {
    try {
      unwrap(await DlPauseAll(force))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/pauseAll', e)
      return false
    }
  }

  async function resumeAll(): Promise<boolean> {
    try {
      unwrap(await DlResumeAll())
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/resumeAll', e)
      return false
    }
  }

  async function purgeResults(): Promise<boolean> {
    try {
      unwrap(await DlPurgeResults())
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/purge', e)
      return false
    }
  }

  async function changePosition(gid: string, pos: number, how: string): Promise<boolean> {
    try {
      unwrap(await DlChangePosition(gid, pos, how))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/position', e)
      return false
    }
  }

  async function changeUri(gid: string, fileIndex: number, del: string[], addUris: string[]): Promise<boolean> {
    try {
      unwrap(await DlChangeUri(gid, fileIndex, JSON.stringify(del), JSON.stringify(addUris)))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/changeUri', e)
      return false
    }
  }

  async function detailOf(gid: string): Promise<DlDetail | null> {
    try {
      const d = unwrap<DlDetail>(await DlDetail(gid))
      return d || null
    } catch (e) {
      logErr('download/detail', e)
      return null
    }
  }

  async function setTaskOption(gid: string, opts: Record<string, string>): Promise<boolean> {
    try {
      unwrap(await DlSetTaskOption(gid, JSON.stringify(opts)))
      toast.success(t('dlSaved'))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/setTaskOption', e)
      return false
    }
  }

  async function setFileSelection(gid: string, indexes: number[]): Promise<boolean> {
    try {
      unwrap(await DlSetFileSelection(gid, JSON.stringify(indexes)))
      toast.success(t('dlSaved'))
      await refreshTasks()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/selectFile', e)
      return false
    }
  }

  async function globalOptions(): Promise<Record<string, string>> {
    try {
      const o = unwrap<Record<string, string>>(await DlGlobalOptions())
      return o || {}
    } catch (e) {
      logErr('download/globalOptions', e)
      return {}
    }
  }

  async function setGlobalOption(opts: Record<string, string>): Promise<boolean> {
    try {
      unwrap(await DlSetGlobalOption(JSON.stringify(opts)))
      toast.success(t('dlSaved'))
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/setGlobalOption', e)
      return false
    }
  }

  async function engineInfo(): Promise<DlEngineInfo | null> {
    try {
      return unwrap<DlEngineInfo>(await DlEngineInfo()) || null
    } catch (e) {
      logErr('download/engineInfo', e)
      return null
    }
  }

  async function shutdown(force: boolean): Promise<boolean> {
    try {
      unwrap(await DlShutdown(force))
      await refreshStatus()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/shutdown', e)
      return false
    }
  }

  async function reveal(target: string) {
    try {
      unwrap(await DlReveal(target))
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/reveal', e)
    }
  }

  async function saveOptions(next: DlOptionsView): Promise<boolean> {
    try {
      unwrap(await DlSetOptions(JSON.stringify(next)))
      toast.success(t('dlSaved'))
      await refreshOptions()
      await refreshStatus()
      return true
    } catch (e) {
      toast.error(getErrorMessage(e)); logErr('download/saveOptions', e)
      return false
    }
  }

  onMounted(async () => {
    // Wails3 的 Events.On 回调收到的是 WailsEvent { name, data }，不是裸数据；
    // 项目里其它页面统一用 `ev?.data ?? ev` 解包，这里必须一致，否则整表快照进不来。
    const off1 = Events.On('quickdock:dl:tasks', (ev: any) => {
      const data = ev?.data ?? ev
      if (Array.isArray(data)) tasks.value = data as DlTask[]
    })
    offTasks = typeof off1 === 'function' ? (off1 as () => void) : null
    const off2 = Events.On('quickdock:dl:notice', (ev: any) => {
      const data = ev?.data ?? ev
      // Go 侧现在推 { ok, name, error }；旧版裸字符串也兼容，免得 dev 热重载期间报错。
      if (data && typeof data === 'object') {
        if (data.ok) toast.success(t('dlNoticeDone', { name: String(data.name || '') }))
        else toast.error(t('dlNoticeFailed', { name: String(data.name || ''), error: String(data.error || '') }))
      } else {
        toast.success(t('dlNoticeDone', { name: String(data || '') }))
      }
    })
    offNotice = typeof off2 === 'function' ? (off2 as () => void) : null
    const off3 = Events.On('quickdock:dl:global', (ev: any) => {
      const data = ev?.data ?? ev
      if (data && typeof data === 'object' && typeof data.downloadSpeed === 'number') {
        global.value = data as DlGlobalStat
      }
    })
    offGlobal = typeof off3 === 'function' ? (off3 as () => void) : null
    const off4 = Events.On('quickdock:dl:ws', (ev: any) => {
      const data = ev?.data ?? ev
      status.value = { ...status.value, wsConnected: data === true }
    })
    offWs = typeof off4 === 'function' ? (off4 as () => void) : null
    await refreshStatus()
    await refreshOptions()
    if (status.value.installed) {
      await refreshTasks()
      await refreshGlobal()
      poll.start()
    }
  })

  onUnmounted(() => {
    offTasks?.(); offTasks = null
    offNotice?.(); offNotice = null
    offGlobal?.(); offGlobal = null
    offWs?.(); offWs = null
    poll.stop()
  })

  return {
    status, statusReady, options, tasks, busy, error, speedHistory, uploadHistory, global,
    refreshStatus, refreshOptions, refreshTasks, refreshGlobal, checkDisk,
    enable, disable, add, addBatch, addTorrent, addMetalink, pickFile,
    pause, forcePause, resume, remove, pauseMany, resumeMany, removeMany,
    pauseAll, resumeAll, purgeResults, changePosition, changeUri,
    detailOf, setTaskOption, setFileSelection,
    globalOptions, setGlobalOption, engineInfo, shutdown, reveal, saveOptions,
  }
}
