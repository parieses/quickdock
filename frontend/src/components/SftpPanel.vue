<script lang="ts" setup>
/**
 * SFTP 文件面板（#14）。
 *
 * 与终端共处同一个标签：它们共享一条 SSH 连接（服务端 MaxSessions 有限，
 * 各建一条会先把自己挤下线），但互不干涉生命周期——本组件自己拉列表，
 * 关掉也只是关掉，不影响同主机已开着的终端会话。
 *
 * 刻意不做拖拽上传：WebView2 的 dataTransfer.files 拿不到真实路径，
 * 而 SFTP 上传必须在 Go 侧用本地绝对路径打开文件，这条路走不通。
 * 改为走宿主原生文件对话框（SelectLocalFile / SelectSavePath）——
 * 路径由系统选择器给出，真实、绝对，不用用户去资源管理器里复制一遍。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import {
  ArrowUp,
  Download,
  Eye,
  FileArchive,
  FileCode,
  FileCog,
  FileImage,
  FilePlay,
  FileText,
  Folder,
  FolderInput,
  FolderPlus,
  FolderUp,
  HardDrive,
  Home,
  Loader,
  Pencil,
  RefreshCw,
  ScrollText,
  Trash2,
  Upload,
  X,
} from '@lucide/vue'
import SftpFileEditor from './SftpFileEditor.vue'
import type { SshHost } from '../utils/ssh'
import { formatBytes } from '../utils/ssh'
import {
  SftpCancelTransfer,
  SftpChmod,
  SftpDownload,
  SftpList,
  SftpMkdir,
  SftpRemove,
  SftpRename,
  SftpStat,
  SftpUpload,
} from '../../bindings/quickdock/services/sshx/sshservice'
import {
  SelectLocalDirectory,
  SelectLocalFile,
  SelectSavePath,
} from '../../bindings/quickdock/services/appservice'
import { apiErrorCode, unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../composables/useToast'
import { copyToClipboard } from '../utils/clipboard'
import {
  entryKind,
  filterEntries,
  formatEntrySize,
  formatMtime,
  joinPath,
  permOctal,
  splitPath,
  type EntryKind,
  type SftpEntry,
} from '../utils/sshSftp'

const props = defineProps<{
  host: SshHost
  /**
   * 只有当前可见时才该发请求。
   *
   * 父页面为了保留目录状态用的是 v-show，等于每个标签一打开就实例化了一个
   * 面板；若这里不设闸门，开 5 个标签就会在用户根本没点「文件」的情况下
   * 悄悄做 5 次 SSH 握手 + SFTP 子系统协商。不可达的主机还会挂住
   * 30 秒的拨号超时，并把连接池引用顶上去。
   */
  active: boolean
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const { confirm, error: toastError, success: toastSuccess } = useToast()

const pwd = ref('')
const parent = ref('/')
const entries = ref<SftpEntry[]>([])
const truncated = ref(false)

const loading = ref(false)
const errMsg = ref('')
/** 正在操作的行的 path，防重复点击 */
const rowBusy = ref('')
/** 工具按钮忙（上传/下载/新建目录） */
const toolBusy = ref(false)

const kw = ref('')
const selected = ref<SftpEntry | null>(null)

/**
 * 多选集合（path → 选中）。
 *
 * 与 selected 分成两套状态而不是「把 selected 改成数组」：
 *   - selected 是「当前行」，重命名 / 改权限 / 编辑 / 属性都只对它生效，
 *     这些操作天然是单项的，做成多选语义反而要想「改了 3 个文件的权限」怎么提示；
 *   - checked 只服务批量动作（下载 / 删除 / 移动）。
 * 两者可以并存：右键某行会同时把它设为当前行，多选不受影响。
 *
 * 用 Set 而不是数组：勾选/取消是 O(1)，且 rows 每次过滤后无需去重。
 */
const checked = ref<Set<string>>(new Set())

/** 已勾选的条目（按列表顺序，与用户视觉一致）。 */
const checkedEntries = computed(() => entries.value.filter(e => checked.value.has(e.path)))

/** 当前可见行是否全选（受搜索过滤影响：全选 = 选中你看见的这些）。 */
const allChecked = computed(() => rows.value.length > 0 && rows.value.every(e => checked.value.has(e.path)))

/**
 * 正在查看/编辑的文件。非空即打开编辑弹窗。
 *
 * 单独一个状态而不是复用 selected：用户可能一边开着编辑窗，
 * 一边在列表里点别的行看信息，两者不该互相覆盖。
 */
const editorEntry = ref<SftpEntry | null>(null)

/** 路径输入框类操作：null 表示没在开任何输入框 */
type PromptKind = 'mkdir' | 'rename' | 'chmod' | 'move'
const promptKind = ref<PromptKind | null>(null)
const promptValue = ref('')
const promptTitle = ref('')
const promptLabel = ref('')
const promptOk = ref('')

/**
 * 当前传输。同一面板一次只跑一个：进度条只有一条，两个传输同时跑会互相覆盖，
 * 而用户也无法从一条进度里判断到底哪个在动。要并发就开另一个标签。
 */
interface TransferState {
  id: string
  op: 'upload' | 'download'
  name: string
  done: number
  total: number
  /** 已发出取消信号，等后端返回；期间按钮置灰防重复点。 */
  canceling?: boolean
}
const transfer = ref<TransferState | null>(null)
const transferring = computed(() => transfer.value !== null)
/** 进度百分比。total 未知（目录下载等）时给 0，进度条走 indeterminate 样式。 */
const transferPercent = computed(() => {
  const tr = transfer.value
  if (!tr || tr.total <= 0) return 0
  return Math.min(100, Math.floor((tr.done / tr.total) * 100))
})
const transferText = computed(() => {
  const tr = transfer.value
  if (!tr) return ''
  if (tr.total <= 0) return formatBytes(tr.done)
  return `${transferPercent.value}% · ${formatBytes(tr.done)} / ${formatBytes(tr.total)}`
})

const rows = computed(() => filterEntries(entries.value, kw.value))
const crumbs = computed(() => splitPath(pwd.value))
const canUp = computed(() => pwd.value !== parent.value && pwd.value !== '/')

/**
 * 文件类型 → 图标。
 *
 * 之前所有非目录文件都共用同一个 File 图标，等于「图标只区分了目录和文件」——
 * 而一屏里 90% 都是文件，那 1 bit 信息几乎没用。压缩包、图片、代码、配置、
 * 日志各给一个形状，用户不用读后缀就能扫到目标。
 * 目录不走这里（Folder 图标 + accent 色有更强的语义）。
 */
const KIND_ICONS: Record<EntryKind, Component> = {
  archive: FileArchive,
  code: FileCode,
  image: FileImage,
  video: FilePlay,
  log: ScrollText,
  config: FileCog,
  text: FileText,
}

function kindIcon(e: SftpEntry) {
  return KIND_ICONS[entryKind(e)]
}

// ---- 直接输入路径 ----
// 原来只能逐级点面包屑：从 /home/u 去 /etc/nginx 要退两次再点两次，
// 路径深的时候完全是体力活。这里给面包屑一个"编辑"入口，回车即跳转。
const pathEditing = ref(false)
const pathInput = ref('')
const pathInputRef = ref<HTMLInputElement | null>(null)

function startPathEdit() {
  pathInput.value = pwd.value
  pathEditing.value = true
  nextTick(() => {
    pathInputRef.value?.focus()
    pathInputRef.value?.select()
  })
}

/**
 * 把用户敲的路径规范成绝对远端路径。
 *
 * 统一用 /、按当前目录拼相对路径、折叠 . 与 ..、去掉尾部 /（根目录除外）。
 * 折叠 .. 是必须的：用户写 ../logs 很正常，不处理会拼出 /a/../logs 这种
 * 能连但看着别扭、又没法跟列表里的 path 直接比对的路径。
 */
function normalizeRemotePath(raw: string): string {
  let next = String(raw || '').trim().replace(/\\/g, '/')
  if (!next) return ''
  if (!next.startsWith('/')) next = (pwd.value.replace(/\/$/, '') || '') + '/' + next
  const parts: string[] = []
  for (const seg of next.split('/')) {
    if (!seg || seg === '.') continue
    if (seg === '..') parts.pop()
    else parts.push(seg)
  }
  return '/' + parts.join('/')
}

function commitPath() {
  pathEditing.value = false
  const raw = pathInput.value.trim()
  if (!raw) return
  const next = normalizeRemotePath(raw)
  if (!next) return
  if (next !== pwd.value) void load(next)
}

async function load(path = pwd.value) {
  if (!path) path = ''
  loading.value = true
  errMsg.value = ''
  // 留在同一目录的刷新要保住选中行：换个目录才清，
  // 否则用户选中一个文件再点刷新，选中就没了，得重新找一遍。
  const keep = path === pwd.value ? selected.value : null
  // 勾选集同理：换目录整批清掉（那些路径已经不在眼前，留着会让「批量删除」
  // 删到上一个目录的东西）；同目录刷新则只保留仍存在的路径。
  const prevChecked = path === pwd.value ? checked.value : new Set<string>()
  try {
    const res = unwrap<{
      path: string
      parent: string
      entries: SftpEntry[]
      truncated: boolean
    }>(await SftpList(props.host.id, path))
    pwd.value = res?.path || '/'
    parent.value = res?.parent || '/'
    entries.value = res?.entries || []
    truncated.value = !!res?.truncated
    selected.value =
      keep && entries.value.some(e => e.path === keep.path) ? keep : null
    checked.value = new Set([...prevChecked].filter(p => entries.value.some(e => e.path === p)))
  } catch (e) {
    errMsg.value = getErrorMessage(e)
    entries.value = []
    selected.value = null
    checked.value = new Set()
  } finally {
    loading.value = false
  }
}

// 依赖里带上 active：从未激活 → 激活时列一次目录，之后每次切回来也刷新一次
// （远端文件可能已经变了，展示上一次的快照会误导人）。pwd 保留，不重置到根目录。
watch(
  () => [props.host.id, props.active] as const,
  () => {
    if (props.active) void load(pwd.value)
  },
  { immediate: true },
)

function openEntry(e: SftpEntry) {
  if (e.isDir) {
    void load(e.path)
    return
  }
  selected.value = e
  // 文件双击 = 打开查看/编辑。真正的「能不能当文本打开」由弹窗判定
  // （扩展名 + 内容双重判断），这里不预筛——否则二进制文件被点开时
  // 会毫无反应，用户只会以为双击没生效。
  editorEntry.value = e
}

function goUp() {
  if (canUp.value) void load(parent.value)
}

function goRoot() {
  void load('/')
}

function selectRow(e: SftpEntry) {
  selected.value = e
}

/** 双击是「进入/打开」——目录进目录，文件开编辑窗。单击只负责选中。 */
function onRowClick(e: SftpEntry) {
  selectRow(e)
}
function onRowDblClick(e: SftpEntry) {
  openEntry(e)
}

/** 工具栏/行内「打开」按钮：与双击同一个入口，给不知道能双击的人一条明路。 */
function openSelected() {
  if (selected.value) openEntry(selected.value)
}

// ---- 多选 ----

function isChecked(e: SftpEntry): boolean {
  return checked.value.has(e.path)
}

/** 勾选/取消单行。force 用于全选框的子项同步（显式给值，避免来回翻转）。 */
function toggleCheck(e: SftpEntry, force?: boolean) {
  const want = force ?? !checked.value.has(e.path)
  const next = new Set(checked.value)
  if (want) next.add(e.path)
  else next.delete(e.path)
  checked.value = next
}

/** 全选/取消全选**当前可见**行：搜索过滤后就只作用于过滤结果，符合所见即所得。 */
function toggleCheckAll() {
  const on = !allChecked.value
  const next = new Set(checked.value)
  for (const e of rows.value) {
    if (on) next.add(e.path)
    else next.delete(e.path)
  }
  checked.value = next
}

function clearChecked() {
  if (checked.value.size) checked.value = new Set()
}

// ---- 路径型操作 ----

function openPrompt(kind: PromptKind, seed: string) {
  promptKind.value = kind
  promptValue.value = seed
  switch (kind) {
    case 'mkdir':
      promptTitle.value = t('sshFileNewDir')
      promptLabel.value = t('sshFileDirName')
      promptOk.value = t('create')
      break
    case 'rename':
      promptTitle.value = t('sshFileRename')
      promptLabel.value = t('sshFileNewName')
      promptOk.value = t('save')
      break
    case 'chmod':
      promptTitle.value = t('sshFileChmod')
      promptLabel.value = t('sshFileChmodLabel')
      promptOk.value = t('save')
      break
    case 'move':
      promptTitle.value = t('sshFileBatchMove')
      promptLabel.value = t('sshFileMoveLabel')
      promptOk.value = t('sshFileBatchMove')
      break
  }
}

function closePrompt() {
  promptKind.value = null
  promptValue.value = ''
}

/** 权限输入框的校验：3 位（644）或 4 位（4755，带 setuid/sticky）八进制。 */
const chmodValid = computed(() => /^[0-7]{3,4}$/.test(promptValue.value.trim()))

async function submitPrompt() {
  const kind = promptKind.value
  const value = promptValue.value.trim()
  if (!kind || !value) return
  if (kind === 'chmod' && !chmodValid.value) return

  toolBusy.value = true
  try {
    // 「移动到」是批量动作，逻辑与结果汇总都在 submitBatchMove 里，
    // 它自己会刷目录，所以走独立分支提前返回，别再落到下面的单条 switch。
    if (kind === 'move') {
      const target = normalizeRemotePath(value)
      if (!target) return
      closePrompt()
      await submitBatchMove(target)
      return
    }
    switch (kind) {
      case 'mkdir':
        unwrap(await SftpMkdir(props.host.id, joinPath(pwd.value, value) || value))
        break
      case 'rename': {
        if (!selected.value) break
        unwrap(await SftpRename(props.host.id, selected.value.path, value))
        break
      }
      case 'chmod': {
        if (!selected.value) break
        // parseInt 的第二个参数必须是 8：用户填 644，十进制会变成 644 & 0o7777
        // 之外的巨数，服务层会直接拒绝。
        unwrap(await SftpChmod(props.host.id, selected.value.path, parseInt(value, 8)))
        break
      }
    }
    closePrompt()
    await load()
  } catch (e) {
    toastError(getErrorMessage(e))
    // 失败不清输入框：路径敲错是常态，让用户改一处再试比全部重填友好。
    return
  } finally {
    toolBusy.value = false
  }
}

/** 打开改权限输入框，预填当前八进制（没有则留空，别塞一个假值）。 */
function startChmod(entry: SftpEntry) {
  selected.value = entry
  openPrompt('chmod', permOctal(entry.mode))
}

// ---- 传输（上传 / 下载）----

/**
 * 进度事件名必须与 services/sshx/sftp.go 的 EventSftpProgress 一致。
 * 两边都是字符串字面量，编译器帮不上忙，改一处就要同步改另一处。
 */
const EVENT_TRANSFER_PROGRESS = 'quickdock:ssh:sftp-progress'

let offProgress: (() => void) | null = null

/**
 * 生成 transferId。
 *
 * 不需要全局唯一：只要和「本面板并发的那一个」不同即可（面板本身已串行化）。
 * 但两个标签页可能同一毫秒发起传输，所以带一段随机后缀而不是纯时间戳。
 */
function newTransferId(): string {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

/** 单次传输的结局。批量操作靠它区分「失败继续下一个」与「用户中止整批」。 */
type TransferOutcome = 'ok' | 'fail' | 'cancel'

/**
 * 跑一次传输：置状态 → 调后端 → 清状态（无论成败）。
 *
 * 只负责进度条与错误归一，不弹提示也不重列目录——那些交给调用方：
 * 批量下载里逐个 toast 会把屏幕刷满，逐个 load() 更是把同一个目录列 N 遍。
 */
async function runTransferOnce(
  op: 'upload' | 'download',
  name: string,
  task: (transferId: string) => Promise<unknown>,
): Promise<TransferOutcome> {
  const id = newTransferId()
  transfer.value = { id, op, name, done: 0, total: 0 }
  try {
    await task(id)
    return 'ok'
  } catch (e) {
    // 用户自己按的取消不是失败：后端把它编成独立错误码，这里静默吞掉。
    // 若按普通错误弹一条红色提示，等于告诉用户「你刚才那下把东西弄坏了」。
    if (apiErrorCode(e) === 'ssh_transfer_canceled') return 'cancel'
    toastError(getErrorMessage(e))
    return 'fail'
  } finally {
    transfer.value = null
  }
}

/** 单次传输（含提示与刷目录），上传/下载按钮走这里。 */
async function runTransfer(
  op: 'upload' | 'download',
  name: string,
  task: (transferId: string) => Promise<unknown>,
) {
  toolBusy.value = true
  try {
    const out = await runTransferOnce(op, name, task)
    if (out !== 'ok') return
    toastSuccess(
      op === 'upload'
        ? t('sshFileUploadDone', { name })
        : t('sshFileDownloadDone', { name }),
    )
    await load()
  } finally {
    toolBusy.value = false
  }
}

/**
 * 取消当前传输。
 *
 * 只发取消信号，不清 transfer 状态：后端要等当前 32KB 分片写完才会返回，
 * 提前清掉进度条会让用户以为已经停了，实际还在写盘。真正的清理由
 * runTransfer 的 finally 负责。
 */
async function cancelTransfer() {
  const tr = transfer.value
  if (!tr || tr.canceling) return
  tr.canceling = true
  try {
    unwrap(await SftpCancelTransfer(tr.id))
  } catch (e) {
    toastError(getErrorMessage(e))
    tr.canceling = false
  }
}

/**
 * 选本地文件上传。
 *
 * 取消（对话框返回空串）直接返回，不发请求也不报错——取消是正常操作，
 * 报错会让用户以为上传失败。
 */
async function pickAndUpload() {
  if (transferring.value) return
  let local = ''
  try {
    local = unwrap<string>(await SelectLocalFile(t('sshFileUpload'))) || ''
  } catch (e) {
    toastError(getErrorMessage(e))
    return
  }
  if (!local) return
  const name = local.split(/[\\/]/).pop() || local
  await runTransfer('upload', name, id => SftpUpload(props.host.id, local, pwd.value, id))
}

/**
 * 选本地目录上传（递归）。
 *
 * 与上传文件分成两个按钮而不是让后端猜：一个「上传」按钮在用户选了目录时
 * 静默开始传几百个文件，是最容易被误触的行为。分开之后，「传目录」是一次
 * 明确的决定。目录落到当前目录下、以本地目录名命名，已存在则逐文件覆盖。
 */
async function pickAndUploadDir() {
  if (transferring.value) return
  let local = ''
  try {
    local = unwrap<string>(await SelectLocalDirectory(t('sshFileUploadDir'))) || ''
  } catch (e) {
    toastError(getErrorMessage(e))
    return
  }
  if (!local) return
  const name = local.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || local
  await runTransfer('upload', name, id => SftpUpload(props.host.id, local, pwd.value, id))
}

/**
 * 下载选中项。
 *
 * 目录下载也走「另存为」选落点：引擎对已存在的目录会把源名字拼进去，
 * 对不存在的路径则当作完整目标名，两种都接受，所以直接透传用户选的结果。
 */
async function startDownload() {
  const entry = selected.value
  if (!entry) return
  await downloadEntry(entry)
}

/** 指定下载某一项（编辑窗里「二进制请下载」也走这条路，不依赖列表选中态）。 */
async function downloadEntry(entry: SftpEntry) {
  if (transferring.value) return
  let target = ''
  try {
    target = unwrap<string>(await SelectSavePath(t('sshFileDownload'), entry.name)) || ''
  } catch (e) {
    toastError(getErrorMessage(e))
    return
  }
  if (!target) return
  await runTransfer('download', entry.name, id =>
    SftpDownload(props.host.id, entry.path, target, id),
  )
}

onMounted(() => {
  // 回调参数是 Wails 的事件信封 { name, data }，负载在 .data 里。
  offProgress = Events.On(EVENT_TRANSFER_PROGRESS, (ev: any) => {
    const p = ev?.data as { transferId?: string; done?: number; total?: number } | undefined
    const tr = transfer.value
    // 只认自己发起的那一个：别的标签页/面板的进度不能画到这条进度条上。
    if (!tr || !p?.transferId || p.transferId !== tr.id) return
    tr.done = Number(p.done) || 0
    tr.total = Number(p.total) || 0
  })
})

onBeforeUnmount(() => {
  offProgress?.()
  offProgress = null
})

async function removeEntry(e: SftpEntry) {
  const msg = e.isDir
    ? t('sshFileDeleteDirConfirm', { name: e.name })
    : t('sshFileDeleteConfirm', { name: e.name })
  if (!(await confirm(msg))) return

  rowBusy.value = e.path
  try {
    // 目录一律递归：用户在文件面板里点「删除」的意图就是把它弄走，
    // 弹一个「目录非空」再让他钩一次「递归」属于把一次确认拆成两次。
    unwrap(await SftpRemove(props.host.id, e.path, e.isDir))
    toastSuccess(t('sshFileDeleted', { name: e.name }))
    selected.value = null
    await load()
  } catch (e2) {
    toastError(getErrorMessage(e2))
  } finally {
    rowBusy.value = ''
  }
}

function startRename() {
  if (!selected.value) return
  openPrompt('rename', selected.value.name)
}

// ---- 批量操作 ----
//
// 多选的价值全在这三件事上：一次挑一批下载、删掉、挪走。逐条来点 10 个文件
// 就是 30 次点击（选中、点按钮、确认），而真正的意图是「这一批」。

/**
 * 批量结果汇总成一条提示。
 *
 * 逐个 toast 会把屏幕刷满，一条不报又会让用户不知道哪几个没成，
 * 所以合成一条：全成报成功，有失败就报失败并列出前几个名字。
 */
function reportBatch(okKey: string, ok: number, total: number, failed: string[]) {
  if (failed.length) {
    const names = failed.slice(0, 3).join('、') + (failed.length > 3 ? ` …(+${failed.length - 3})` : '')
    toastError(t('sshFileBatchPartial', { ok, total, n: failed.length, names }))
    return
  }
  if (ok > 0) toastSuccess(t(okKey, { n: ok }))
}

/** 批量下载：只选一次本地目录，逐个下到该目录下（沿用远端原文件名）。 */
async function batchDownload() {
  const list = checkedEntries.value
  if (!list.length || transferring.value) return
  let dir = ''
  try {
    dir = unwrap<string>(await SelectLocalDirectory(t('sshFileBatchDownload'))) || ''
  } catch (e) {
    toastError(getErrorMessage(e))
    return
  }
  if (!dir) return

  const failed: string[] = []
  let ok = 0
  let canceled = false
  toolBusy.value = true
  try {
    for (const e of list) {
      // dst 直接传目录：引擎对「已存在的目录」会把源名拼进去，
      // 文件与目录都落在 dir/<名字>，前端不必自己拼本地分隔符。
      const out = await runTransferOnce('download', e.name, id =>
        SftpDownload(props.host.id, e.path, dir, id),
      )
      if (out === 'ok') ok++
      else {
        failed.push(e.name)
        // 用户按了「中断」就把整批停掉：他要的是别再传了，
        // 继续把剩下 9 个传完是最糟的响应。
        if (out === 'cancel') {
          canceled = true
          break
        }
      }
    }
  } finally {
    toolBusy.value = false
  }
  reportBatch('sshFileBatchDownloaded', ok, list.length, failed)
  if (ok) {
    if (!canceled) clearChecked()
    await load()
  }
}

/** 批量删除：一次确认，逐个删（目录递归）。 */
async function batchDelete() {
  const list = checkedEntries.value
  if (!list.length || !!rowBusy.value) return
  const hasDir = list.some(e => e.isDir)
  // 含目录时换一句文案：递归删掉一整棵子树和删几个文件不是一个量级的事。
  const confirmed = await confirm(
    t(hasDir ? 'sshFileBatchDeleteMixed' : 'sshFileBatchDeleteConfirm', { n: list.length }),
  )
  if (!confirmed) return

  const failed: string[] = []
  let ok = 0
  toolBusy.value = true
  try {
    for (const e of list) {
      // rowBusy 逐行给上：删目录可能要几秒，没有行内反馈用户会以为卡住了。
      rowBusy.value = e.path
      try {
        // 目录一律递归，理由同 removeEntry：一次确认就够了，
        // 不要再弹「目录非空」让用户钩第二个框。
        unwrap(await SftpRemove(props.host.id, e.path, e.isDir))
        ok++
      } catch {
        failed.push(e.name)
      }
    }
  } finally {
    rowBusy.value = ''
    toolBusy.value = false
  }
  reportBatch('sshFileBatchDeleted', ok, list.length, failed)
  clearChecked()
  await load()
}

/** 批量移动：输入目标目录，逐个 rename 过去（同主机内是纯 rename，不搬数据）。 */
function startBatchMove() {
  if (!checkedEntries.value.length) return
  openPrompt('move', '')
}

async function submitBatchMove(target: string) {
  const list = checkedEntries.value
  if (!list.length) return
  // 先确认目标目录存在且确实是目录：否则下面的 N 次 rename 会报 N 条同样的错，
  // 用户还得从一堆重复错误里猜「是我路径打错了」。
  try {
    const st = unwrap<SftpEntry>(await SftpStat(props.host.id, target))
    if (!st?.isDir) {
      toastError(t('sshFileMoveNotDir', { path: target }))
      return
    }
  } catch (e) {
    toastError(getErrorMessage(e))
    return
  }

  const failed: string[] = []
  let ok = 0
  for (const e of list) {
    // 目标里已经有同名项时 rename 会被服务端拒绝（不静默覆盖），
    // 失败的进 failed 汇总，成功的照常移动——不做全有或全无的回滚，
    // 那反而会把已经挪走的文件再挪回来，越弄越乱。
    if (e.path === target || target.startsWith(e.path + '/')) {
      failed.push(e.name)
      continue
    }
    try {
      unwrap(await SftpRename(props.host.id, e.path, joinPath(target, e.name)))
      ok++
    } catch {
      failed.push(e.name)
    }
  }
  reportBatch('sshFileBatchMoved', ok, list.length, failed)
  clearChecked()
  await load()
}

// ---- 右键菜单 ----
//
// 行内的图标按钮已经能完成大部分操作，但每个按钮都要先 hover 到那一行才看得见，
// 且「文件到底多大、什么时候改的」在窄列里会被截断。右键菜单把这两件事补齐：
// 一组不用找图标的常用动作，加一个「属性」看完整信息。
interface RowMenu { x: number; y: number; entry: SftpEntry }
const rowMenu = ref<RowMenu | null>(null)

function openRowMenu(e: MouseEvent, entry: SftpEntry) {
  selectRow(entry)
  // 往回收，避免菜单被视口边缘裁掉：宽度按最长一项估，高度按项数算。
  const w = 170
  const h = 30 * (entry.isDir ? 5 : 7) + 12
  rowMenu.value = {
    x: Math.min(e.clientX, window.innerWidth - w),
    y: Math.min(e.clientY, window.innerHeight - h),
    entry,
  }
}
function closeRowMenu() { rowMenu.value = null }

// ---- 属性弹窗 ----
//
// 现取（SftpStat）而不用列表里那条记录：列表可能是几十秒前拉的，
// 这期间文件被别的过程改过很常见——属性弹窗的意义恰恰是「此刻的状态」，
// 拿旧数据就白弹了。
const propsEntry = ref<SftpEntry | null>(null)
const propsLoading = ref(false)
const propsError = ref('')

async function showProps(entry: SftpEntry) {
  rowMenu.value = null
  propsEntry.value = entry
  propsError.value = ''
  propsLoading.value = true
  try {
    const fresh = unwrap<SftpEntry>(await SftpStat(props.host.id, entry.path))
    if (fresh) propsEntry.value = fresh
  } catch (e) {
    // 取不到就退回列表里的那条：宁可展示可能过期的信息，也不要弹个空窗。
    // 错误仍然提示，让用户知道这不是最新值。
    propsError.value = getErrorMessage(e)
  } finally {
    propsLoading.value = false
  }
}
function closeProps() { propsEntry.value = null; propsError.value = '' }

function propsTypeLabel(e: SftpEntry): string {
  if (e.isLink) return t('sshFileTypeLink')
  return e.isDir ? t('sshFileTypeDir') : t('sshFileTypeFile')
}

async function copyPath(e: SftpEntry) {
  rowMenu.value = null
  const ok = await copyToClipboard(e.path)
  if (ok) toastSuccess(t('sshFileCopied'))
  else toastError(t('copyFailed'))
}
</script>

<template>
  <div class="sftp">
    <header class="sftp-head">
      <HardDrive :size="14" class="sftp-head-icon" />
      <span class="sftp-title">{{ t('sshFiles') }}</span>

      <div class="sftp-tools">
        <button class="sftp-tool" :disabled="loading" :title="t('refresh')" @click="load()">
          <RefreshCw :size="13" :class="{ spinning: loading }" />
        </button>
        <button class="sftp-tool" :disabled="!canUp" :title="t('sshFileUp')" @click="goUp">
          <ArrowUp :size="14" />
        </button>
        <button class="sftp-tool" :title="t('sshFileHome')" @click="load('')">
          <Home :size="14" />
        </button>
        <button class="sftp-tool" :title="t('sshFileRoot')" @click="goRoot">
          <Folder :size="14" />
        </button>
        <span class="sftp-sep" />
        <button
          class="sftp-tool"
          :disabled="toolBusy"
          :title="t('sshFileNewDir')"
          @click="openPrompt('mkdir', '')"
        >
          <FolderPlus :size="14" />
        </button>
        <button
          class="sftp-tool"
          :disabled="toolBusy"
          :title="t('sshFileUpload')"
          @click="pickAndUpload"
        >
          <Upload :size="14" />
        </button>
        <button
          class="sftp-tool"
          :disabled="toolBusy"
          :title="t('sshFileUploadDir')"
          @click="pickAndUploadDir"
        >
          <FolderUp :size="14" />
        </button>
        <button
          class="sftp-tool"
          :disabled="!selected || selected.isDir"
          :title="t('sshFileOpen')"
          @click="openSelected"
        >
          <Eye :size="14" />
        </button>
        <button
          class="sftp-tool sftp-tool--danger"
          :disabled="!selected || !!rowBusy"
          :title="t('sshDelete')"
          @click="selected && removeEntry(selected)"
        >
          <Trash2 :size="14" />
        </button>
        <span class="sftp-sep" />
        <button class="sftp-x" :title="t('cancel')" @click="emit('close')">
          <X :size="15" />
        </button>
      </div>
    </header>

    <nav class="sftp-crumbs">
      <template v-if="pathEditing">
        <input
          ref="pathInputRef"
          v-model="pathInput"
          class="sftp-path-input"
          :placeholder="t('sshPathPh')"
          @keyup.enter="commitPath"
          @keyup.esc="pathEditing = false"
          @blur="commitPath"
        />
      </template>
      <template v-else>
        <button
          v-for="c in crumbs"
          :key="c.path"
          class="sftp-crumb"
          :class="{ 'sftp-crumb--now': c.path === pwd }"
          :disabled="loading"
          @click="load(c.path)"
        >
          {{ c.name }}
        </button>
        <button class="sftp-crumb sftp-crumb-edit" :title="t('sshEditPath')" :disabled="loading" @click="startPathEdit">
          ✎
        </button>
      </template>
      <input v-model="kw" class="sftp-kw" :placeholder="t('search')" />
    </nav>

    <!-- 传输进度条：只在传输期间出现，避免常驻占一行。
         total 为 0（目录下载时单片大小未知）走 indeterminate，不假装有百分比。
         填充宽度是运行时计算值，只能内联 style（R7 的例外项）。 -->
    <div v-if="transfer" class="sftp-progress">
      <div class="sftp-progress-text">
        <span class="sftp-progress-name">{{ transfer.name }}</span>
        <span class="sftp-progress-meta">{{ transferText }}</span>
        <button
          class="sftp-progress-cancel"
          :disabled="transfer.canceling"
          :title="t('sshFileTransferCancel')"
          @click="cancelTransfer"
        >
          <X :size="11" />
          <span>{{ transfer.canceling ? t('sshFileTransferCanceling') : t('cancel') }}</span>
        </button>
      </div>
      <div class="sftp-progress-track">
        <div
          class="sftp-progress-fill"
          :class="{ 'is-indeterminate': transfer.total <= 0 }"
          :style="{ width: transfer.total > 0 ? transferPercent + '%' : '100%' }"
        />
      </div>
    </div>

    <div class="sftp-body">
      <!-- 多选工具条：只在勾选了东西之后出现，平时不占一行。
           批量动作放这里而不是顶部工具栏，是因为它们的前提就是「你已经勾了几项」，
           挨着列表显示，勾完立刻能看见能做什么。 -->
      <div v-if="!errMsg && checkedEntries.length" class="sftp-selbar">
        <span class="sftp-selcount">{{ t('sshFileSelCount', { n: checkedEntries.length }) }}</span>
        <button class="sftp-selbtn" :disabled="toolBusy || transferring" @click="batchDownload">
          <Download :size="12" />
          <span>{{ t('sshFileBatchDownload') }}</span>
        </button>
        <button class="sftp-selbtn" :disabled="toolBusy || !!rowBusy" @click="startBatchMove">
          <FolderInput :size="12" />
          <span>{{ t('sshFileBatchMove') }}</span>
        </button>
        <button
          class="sftp-selbtn sftp-selbtn--danger"
          :disabled="toolBusy || !!rowBusy"
          @click="batchDelete"
        >
          <Trash2 :size="12" />
          <span>{{ t('sshFileBatchDelete') }}</span>
        </button>
        <button class="sftp-selclear" :title="t('sshFileSelClear')" @click="clearChecked">
          <X :size="11" />
        </button>
      </div>

      <div v-if="errMsg" class="sftp-err">{{ errMsg }}</div>

      <div v-else-if="loading && !entries.length" class="sftp-state">
        <Loader :size="16" class="spinning" />
        <span>{{ t('sshFileLoading') }}</span>
      </div>

      <div v-else-if="!rows.length" class="sftp-state">
        {{ kw ? t('sshFileNoMatch') : t('sshFileEmpty') }}
      </div>

      <table v-else class="sftp-table">
        <thead>
          <tr>
            <th class="c-check">
              <input
                type="checkbox"
                class="sftp-check"
                :checked="allChecked"
                :title="t('sshFileSelectAll')"
                @change="toggleCheckAll"
              />
            </th>
            <th class="c-name">{{ t('sshName') }}</th>
            <th class="c-size">{{ t('sshFileSize') }}</th>
            <th class="c-perm">{{ t('sshFilePerm') }}</th>
            <th class="c-time">{{ t('sshFileMtime') }}</th>
            <th class="c-ops" />
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="e in rows"
            :key="e.path"
            class="sftp-row"
            :class="{ 'sftp-row--sel': selected?.path === e.path }"
            @click="onRowClick(e)"
            @dblclick="onRowDblClick(e)"
            @contextmenu.prevent="openRowMenu($event, e)"
          >
            <td class="c-check" @click.stop>
              <input
                type="checkbox"
                class="sftp-check"
                :checked="isChecked(e)"
                @change="toggleCheck(e)"
              />
            </td>
            <td class="c-name">
              <Folder v-if="e.isDir" :size="14" class="ic ic--dir" />
              <component :is="kindIcon(e)" v-else :size="14" class="ic" />
              <span class="nm">{{ e.name }}</span>
              <span v-if="e.isLink" class="tag-link">link</span>
            </td>
            <td class="c-size">{{ formatEntrySize(e) }}</td>
            <td class="c-perm" :title="t('sshFileChmod')">
              <button class="perm" @click.stop="startChmod(e)">
                <span class="perm-oct">{{ permOctal(e.mode) }}</span>
                <span class="perm-rwx">{{ e.mode.slice(-9) }}</span>
              </button>
            </td>
            <td class="c-time">{{ formatMtime(e.modTime) }}</td>
            <td class="c-ops">
              <button
                v-if="!e.isDir"
                class="op"
                :title="t('sshFileOpen')"
                @click.stop="openEntry(e)"
              >
                <Eye :size="12" />
              </button>
              <button
                class="op"
                :disabled="!!rowBusy"
                :title="t('sshFileRename')"
                @click.stop="(selectRow(e), startRename())"
              >
                <Pencil :size="12" />
              </button>
              <button
                class="op"
                :disabled="!!rowBusy"
                :title="t('sshFileDownload')"
                @click.stop="(selectRow(e), startDownload())"
              >
                <Download :size="12" />
              </button>
              <button
                class="op op--danger"
                :disabled="rowBusy === e.path"
                :title="t('sshDelete')"
                @click.stop="removeEntry(e)"
              >
                <Trash2 :size="12" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>

      <p v-if="truncated && !kw" class="sftp-note">
        {{ t('sshFileTruncated', { n: entries.length }) }}
      </p>
    </div>

    <!-- 新建目录 / 重命名 / 改权限的输入框。上传下载已改走原生文件对话框，不再需要它。 -->
    <Teleport to="body">
      <Transition name="sfp">
        <div v-if="promptKind" class="sfp-overlay" @mousedown.self="closePrompt">
          <div class="sfp-panel" role="dialog" @keydown.enter.stop="submitPrompt">
            <div class="sfp-title">{{ promptTitle }}</div>
            <label class="sfp-label">{{ promptLabel }}</label>
            <input
              v-model="promptValue"
              class="sfp-input"
              :class="{ 'sfp-input--bad': promptKind === 'chmod' && promptValue && !chmodValid }"
              autofocus
              @keydown.esc.stop="closePrompt"
            />
            <!-- 权限是唯一需要格式校验的输入：用户敲「rwx」或不存在的位都拦在这里，
                 让后端只处理合法八进制，报错集中在网络/权限问题上。 -->
            <div v-if="promptKind === 'chmod' && promptValue && !chmodValid" class="sfp-hint">
              {{ t('sshFileChmodInvalid') }}
            </div>
            <div class="sfp-foot">
              <button class="sfp-btn" @click="closePrompt">{{ t('cancel') }}</button>
              <button
                class="sfp-btn sfp-btn--primary"
                :disabled="toolBusy || (promptKind === 'chmod' && !chmodValid)"
                @click="submitPrompt"
              >
                {{ promptOk }}
              </button>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- 行右键菜单：backdrop 负责「点别处/再右键即关」，菜单本体 stop 掉冒泡 -->
    <Teleport to="body">
      <div
        v-if="rowMenu"
        class="sfp-menu-backdrop"
        @click="closeRowMenu"
        @contextmenu.prevent="closeRowMenu"
      >
        <div
          class="sfp-menu"
          :style="{ left: rowMenu.x + 'px', top: rowMenu.y + 'px' }"
          @click.stop
          @contextmenu.prevent
        >
          <button class="sfp-menu-item" @click="(closeRowMenu(), onRowDblClick(rowMenu.entry))">
            {{ rowMenu.entry.isDir ? t('sshFileEnter') : t('sshFileOpen') }}
          </button>
          <button class="sfp-menu-item" @click="showProps(rowMenu.entry)">
            {{ t('sshFileProps') }}
          </button>
          <button class="sfp-menu-item" @click="copyPath(rowMenu.entry)">
            {{ t('sshFileCopyPath') }}
          </button>
          <div class="sfp-menu-sep" />
          <button
            class="sfp-menu-item"
            :disabled="!!rowBusy"
            @click="(closeRowMenu(), downloadEntry(rowMenu.entry))"
          >
            {{ t('sshFileDownload') }}
          </button>
          <button
            class="sfp-menu-item"
            :disabled="!!rowBusy"
            @click="(selectRow(rowMenu.entry), closeRowMenu(), startRename())"
          >
            {{ t('sshFileRename') }}
          </button>
          <button
            class="sfp-menu-item"
            @click="(closeRowMenu(), startChmod(rowMenu.entry))"
          >
            {{ t('sshFileChmod') }}
          </button>
          <div class="sfp-menu-sep" />
          <button
            class="sfp-menu-item sfp-menu-item--danger"
            :disabled="rowBusy === rowMenu.entry.path"
            @click="(closeRowMenu(), removeEntry(rowMenu.entry))"
          >
            {{ t('sshDelete') }}
          </button>
        </div>
      </div>
    </Teleport>

    <!-- 属性弹窗 -->
    <Teleport to="body">
      <Transition name="sfp">
        <div v-if="propsEntry" class="sfp-overlay" @mousedown.self="closeProps">
          <div class="sfp-panel sfp-panel--props" role="dialog" @keydown.esc.stop="closeProps">
            <div class="sfp-title">{{ t('sshFileProps') }}</div>
            <div class="props-grid">
              <div class="props-row">
                <span class="props-k">{{ t('sshName') }}</span>
                <span class="props-v props-v--wrap">{{ propsEntry.name }}</span>
              </div>
              <div class="props-row">
                <span class="props-k">{{ t('sshFilePropType') }}</span>
                <span class="props-v">{{ propsTypeLabel(propsEntry) }}</span>
              </div>
              <div class="props-row">
                <span class="props-k">{{ t('sshFilePropPath') }}</span>
                <span class="props-v props-v--wrap">{{ propsEntry.path }}</span>
              </div>
              <div class="props-row">
                <span class="props-k">{{ t('sshFilePropSize') }}</span>
                <span class="props-v">
                  {{ formatEntrySize(propsEntry) }}
                  <span v-if="!propsEntry.isDir" class="props-sub">（{{ propsEntry.size }} B）</span>
                </span>
              </div>
              <div class="props-row">
                <span class="props-k">{{ t('sshFilePropPerm') }}</span>
                <span class="props-v">
                  <span class="props-oct">{{ permOctal(propsEntry.mode) }}</span>
                  <span class="props-sub">{{ propsEntry.mode.slice(-9) }}</span>
                </span>
              </div>
              <div class="props-row">
                <span class="props-k">{{ t('sshFilePropMtime') }}</span>
                <span class="props-v">{{ formatMtime(propsEntry.modTime) }}</span>
              </div>
              <div v-if="propsEntry.isLink && propsEntry.linkTo" class="props-row">
                <span class="props-k">{{ t('sshFilePropLink') }}</span>
                <span class="props-v props-v--wrap">{{ propsEntry.linkTo }}</span>
              </div>
            </div>
            <div v-if="propsLoading" class="props-note">{{ t('loading') }}</div>
            <div v-else-if="propsError" class="props-note props-note--warn">
              {{ t('sshFilePropsStale', { err: propsError }) }}
            </div>
            <div class="sfp-foot">
              <button class="sfp-btn" @click="copyPath(propsEntry)">{{ t('sshFileCopyPath') }}</button>
              <button class="sfp-btn sfp-btn--primary" @click="closeProps">{{ t('close') }}</button>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- 双击文件打开的查看/编辑弹窗。自带 Teleport，放在最后不与上面互斥。 -->
    <SftpFileEditor
      v-if="editorEntry"
      :host-id="host.id"
      :entry="editorEntry"
      @close="editorEntry = null"
      @saved="load()"
      @download="downloadEntry"
    />
  </div>
</template>

<style scoped>
.sftp {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--color-bg-primary);
}

.sftp-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--color-border);
}

.sftp-head-icon {
  color: var(--color-accent);
  flex: 0 0 auto;
}

.sftp-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.sftp-tools {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  margin-left: auto;
}

.sftp-tool {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sftp-tool:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.sftp-tool:disabled {
  cursor: default;
  opacity: 0.4;
}

.sftp-tool--danger:hover:not(:disabled) {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.sftp-sep {
  width: 1px;
  height: 16px;
  background: var(--color-border);
}

.sftp-x {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sftp-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.sftp-crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: 0 0 auto;
  min-width: 0;
  padding: var(--space-3) var(--space-5);
  overflow-x: auto;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.sftp-crumb {
  flex: 0 0 auto;
  padding: 2px var(--space-2);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  white-space: nowrap;
}

.sftp-crumb:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}
/* 面包屑末尾的"编辑路径"按钮：字符文案用 ✎，省一个图标 import */
.sftp-crumb-edit {
  flex: 0 0 auto;
  font-size: 12px;
  color: var(--color-text-disabled);
  padding: 2px 5px;
}
.sftp-crumb-edit:hover:not(:disabled) { color: var(--color-accent); }
/* 路径输入框：占满面包屑剩余宽度，与面包屑同字号同高 */
.sftp-path-input {
  flex: 1 1 auto;
  min-width: 0;
  height: 22px;
  padding: 0 var(--space-2);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-accent);
  border-radius: var(--radius-xs);
  outline: none;
}

.sftp-crumb--now {
  color: var(--color-text-primary);
  font-weight: 500;
}

.sftp-kw {
  flex: 0 0 130px;
  margin-left: auto;
  padding: 2px var(--space-3);
  font-size: 11px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
}

.sftp-kw:focus {
  border-color: var(--color-accent);
}

/* 传输进度：细条 + 一行文本，不抢正文的空间（面板本身只有 300px 宽） */
.sftp-progress {
  flex: 0 0 auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-5);
  border-bottom: 1px solid var(--color-border);
}

.sftp-progress-text {
  display: flex;
  align-items: baseline;
  gap: var(--space-3);
  min-width: 0;
  font-size: 11px;
}

.sftp-progress-name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
}

.sftp-progress-meta {
  flex: 0 0 auto;
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--color-text-muted);
}

/* 取消按钮跟进度文案同一行、贴右侧：传输是阻塞式的一整段，
   把「怎么停下来」放在进度旁边，比藏在工具栏里更容易在慌乱中找到。 */
.sftp-progress-cancel {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0 var(--space-2);
  font-size: 10.5px;
  color: var(--color-text-muted);
  background: none;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.sftp-progress-cancel:hover:not(:disabled) {
  color: var(--color-danger);
  border-color: var(--color-danger-border);
}

.sftp-progress-cancel:disabled {
  opacity: 0.55;
  cursor: default;
}

.sftp-progress-track {
  height: 3px;
  overflow: hidden;
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-full);
}

.sftp-progress-fill {
  height: 100%;
  background: var(--color-accent);
  border-radius: var(--radius-full);
  transition: width 0.15s linear;
}

/* 总长未知时不显示百分比，用一条来回走的动画表示「还在传，但不知道还有多久」 */
.sftp-progress-fill.is-indeterminate {
  animation: sftp-indeterminate 1.1s ease-in-out infinite;
  transition: none;
}

@keyframes sftp-indeterminate {
  0% {
    transform: translateX(-100%);
  }
  100% {
    transform: translateX(100%);
  }
}

.sftp-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
}

/* ---- 多选工具条 ---- */

.sftp-selbar {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-4);
  font-size: 11.5px;
  background: var(--color-accent-bg);
  border-bottom: 1px solid var(--color-border);
}

.sftp-selcount {
  flex: 0 0 auto;
  font-weight: 500;
  color: var(--color-accent);
}

.sftp-selbtn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: 3px var(--space-3);
  font-size: 11px;
  color: var(--color-text-secondary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
  white-space: nowrap;
}

.sftp-selbtn:hover:not(:disabled) {
  color: var(--color-text-primary);
  border-color: var(--color-border-focus);
}

.sftp-selbtn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.sftp-selbtn--danger:hover:not(:disabled) {
  color: var(--color-danger);
  border-color: var(--color-danger);
}

.sftp-selclear {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  /* 推到最右：它是「取消选择」，离批量按钮远一点，避免误点成删除。 */
  margin-left: auto;
  width: 20px;
  height: 20px;
  color: var(--color-text-muted);
  background: none;
  border: 0;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sftp-selclear:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.c-check {
  width: 30px;
  padding-right: 0 !important;
  text-align: center;
}

.sftp-check {
  width: 12px;
  height: 12px;
  margin: 0;
  accent-color: var(--color-accent);
  cursor: pointer;
}

.sftp-err {
  margin: var(--space-4) var(--space-5);
  padding: var(--space-3) var(--space-4);
  font-size: 11.5px;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-radius: var(--radius-xs);
}

.sftp-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  padding: var(--space-10) var(--space-4);
  font-size: 12px;
  color: var(--color-text-muted);
}

.sftp-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11.5px;
}

.sftp-table th {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: var(--space-3) var(--space-4);
  font-size: 10.5px;
  font-weight: 500;
  text-align: left;
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.sftp-row {
  cursor: pointer;
}

.sftp-row:hover {
  background: var(--color-bg-hover);
}

.sftp-row--sel {
  background: var(--color-accent-bg);
}

.sftp-table td {
  padding: var(--space-2) var(--space-4);
  color: var(--color-text-secondary);
  border-bottom: 1px solid var(--color-border-light);
}

.c-name {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
}

.ic {
  flex: 0 0 auto;
  color: var(--color-text-disabled);
}

.ic--dir {
  color: var(--color-accent);
}

.nm {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
}

.tag-link {
  flex: 0 0 auto;
  padding: 0 var(--space-2);
  font-size: 9.5px;
  color: var(--color-text-muted);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
}

.c-size,
.c-perm,
.c-time {
  white-space: nowrap;
}

.c-size {
  width: 84px;
  text-align: right;
}

.c-perm {
  /* 同时放八进制与 rwx：644 是 chmod 时要的数字，-rw-r--r-- 是 ls -l 的直觉读法。
     两个都要，任一单独出现都会让另一拨用户去心算。112px 是二者并排的最小值。 */
  width: 112px;
  font-family: var(--font-mono);
  font-size: 10.5px;
}

/* 内层按钮而不是给 td 加 display:flex——td 一旦不是 table-cell，
   行高、列宽、border-bottom 全部脱离表格布局，整行会错位。
   做成按钮是因为权限现在可改：不改的话这一列看起来就是死文本，
   没人会想到点它。 */
.perm {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  width: 100%;
  padding: 1px 3px;
  margin: -1px -3px;
  font: inherit;
  text-align: left;
  color: inherit;
  background: none;
  border: 1px solid transparent;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.perm:hover {
  border-color: var(--color-border);
  background: var(--color-bg-hover);
}

.perm-oct {
  flex: 0 0 auto;
  color: var(--color-text-secondary);
}

.perm-rwx {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--color-text-muted);
}

.perm:hover .perm-rwx {
  color: var(--color-text-secondary);
}

.c-time {
  width: 128px;
}

.c-ops {
  width: 76px;
  white-space: nowrap;
  text-align: right;
}

.op {
  display: inline-flex;
  padding: var(--space-1);
  margin-left: 2px;
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.op:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.op--danger:hover:not(:disabled) {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.op:disabled {
  cursor: default;
  opacity: 0.4;
}

.sftp-note {
  margin: var(--space-4) var(--space-5);
  font-size: 10.5px;
  color: var(--color-text-muted);
}

/* ---- 路径输入弹窗 ---- */
.sfp-overlay {
  position: fixed;
  inset: 0;
  z-index: 1400;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
}

.sfp-panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  width: 420px;
  max-width: 92vw;
  padding: var(--space-6);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
}

.sfp-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.sfp-label {
  font-size: 11px;
  color: var(--color-text-muted);
}

.sfp-input {
  width: 100%;
  box-sizing: border-box;
  padding: var(--space-3) var(--space-4);
  font-size: 12px;
  font-family: var(--font-mono);
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
}

.sfp-input:focus {
  border-color: var(--color-accent);
}

/* 校验失败时边框先变红，提示文字在下面补原因——
   只变红不说话，用户还得自己猜哪里不对。 */
.sfp-input--bad,
.sfp-input--bad:focus {
  border-color: var(--color-danger);
}

.sfp-hint {
  font-size: 10.5px;
  color: var(--color-danger);
}

.sfp-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
}

.sfp-btn {
  padding: var(--space-2) var(--space-5);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sfp-btn:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.sfp-btn--primary {
  color: var(--color-accent-text);
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.sfp-btn--primary:disabled {
  cursor: default;
  opacity: 0.5;
}

/* ---- 行右键菜单 ---- */
/* backdrop 铺满视口接住「点别处关闭」，菜单按坐标定位。
   不用 document 监听：组件卸载时容易漏解绑，而 backdrop 随 v-if 一起消失。 */
.sfp-menu-backdrop {
  position: fixed;
  inset: 0;
  z-index: 3000;
}
.sfp-menu {
  position: fixed;
  min-width: 160px;
  padding: 4px;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 8px 24px var(--color-bg-overlay);
  display: flex;
  flex-direction: column;
}
.sfp-menu-item {
  display: block;
  width: 100%;
  padding: 5px 10px;
  border: none;
  border-radius: var(--radius-sm);
  background: none;
  color: var(--color-text-primary);
  font: inherit;
  font-size: 11.5px;
  text-align: left;
  cursor: pointer;
}
.sfp-menu-item:hover:not(:disabled) { background: var(--color-bg-hover); }
.sfp-menu-item:disabled { color: var(--color-text-disabled); cursor: default; }
.sfp-menu-item--danger { color: var(--color-danger); }
.sfp-menu-item--danger:hover:not(:disabled) { background: var(--color-danger-bg); }
.sfp-menu-sep {
  height: 1px;
  margin: 3px 2px;
  background: var(--color-border);
}

/* ---- 属性弹窗 ---- */
.sfp-panel--props { min-width: 380px; max-width: 560px; }
.props-grid {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 4px 0 2px;
}
.props-row {
  display: grid;
  grid-template-columns: 76px minmax(0, 1fr);
  gap: 8px;
  align-items: baseline;
}
.props-k {
  font-size: 11px;
  color: var(--color-text-muted);
}
.props-v {
  font-size: 12px;
  color: var(--color-text-primary);
  min-width: 0;
}
/* 路径与文件名会比弹窗宽：截断就看不到后半个路径，而那正是属性里最有价值的一项 */
.props-v--wrap {
  font-family: var(--font-mono);
  font-size: 11px;
  word-break: break-all;
}
.props-sub { color: var(--color-text-muted); font-size: 10.5px; margin-left: 4px; }
.props-oct {
  font-family: var(--font-mono);
  color: var(--color-accent);
}
.props-note {
  margin-top: 6px;
  font-size: 10.5px;
  color: var(--color-text-muted);
}
.props-note--warn { color: var(--color-warning); }

.sfp-enter-active,
.sfp-leave-active {
  transition: opacity 0.14s ease;
}

.sfp-enter-from,
.sfp-leave-to {
  opacity: 0;
}

.spinning {
  animation: sftp-spin 800ms linear infinite;
}

@keyframes sftp-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
