<script lang="ts" setup>
import { downloadBlob } from '../utils/download'
defineOptions({ name: KEEP_ALIVE.ssh })
// name 的唯一来源：pages/keepAlive.ts（与 KeepAlive 白名单共用同一常量）
import { KEEP_ALIVE } from '../pages/keepAlive'
/**
 * SSH 独立窗口的主页面：左侧会话树 + 顶部标签栏 + 右侧终端（#13）。
 *
 * 窗口是懒创建 + 复用的（关闭只是 Hide），Vue 只在首次加载时 mount 一次，
 * 所以「窗口重新唤到前台」要靠 quickdock:ssh:shown 事件刷新清单，
 * 否则用户改了主机名再打开窗口看到的还是旧数据。
 *
 * 三层分工：
 *   - utils/sshTree.ts   纯函数：扁平列表 → 树、拖拽落点计算
 *   - utils/sshTabs.ts   纯函数：标签数组的开/关/去重
 *   - 本组件             只管编排与副作用（调后端、弹确认）
 *
 * 这样切标签、拖拽排序这些高频交互都能用 vitest 直接覆盖，
 * 不用起 Vue、不用 mock Wails 绑定。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { Download, FolderPlus, ListChecks, MapPin, Network, Plus, Radio, RefreshCw, Send, Server, ShieldCheck, SquareTerminal, Zap } from '@lucide/vue'
import {
  DeleteHost,
  Disconnect,
  ListHosts,
  ListSessions,
  MoveHost,
  SaveHost,
  SshStopForwardsByTab,
  Write,
} from '../../bindings/quickdock/services/sshx/sshservice'
import { unwrap } from '../utils/api'
import { logErr } from '../utils/logger'
import { getErrorMessage } from '../utils/error'
import { bytesToBase64, textToBytes } from '../utils/base64'
import { useToast } from '../composables/useToast'
import {
  isSessionRetained,
  type SshHost,
  type SshSessionInfo,
} from '../utils/ssh'
import { buildSshTree, collectSubtreeIds } from '../utils/sshTree'
import {
  closeTab,
  findResumable,
  findTab,
  openOrFocus,
  openTab,
  patchTab,
  reorderSshTabs,
  syncHost,
  tabSessionId,
  type SshTab,
} from '../utils/sshTabs'
import SshHostTree from './SshHostTree.vue'
import QdSearchBox from './QdSearchBox.vue'
import SshHostDialog from './SshHostDialog.vue'
import SshHostDetailDialog from './SshHostDetailDialog.vue'
import SshSessionView from './SshSessionView.vue'
import SshTabBar from './SshTabBar.vue'
import SshTrustedHostsDialog from './SshTrustedHostsDialog.vue'
import SshFolderDialog from './SshFolderDialog.vue'
import SftpPanel from './SftpPanel.vue'
import SshBatchDialog from './SshBatchDialog.vue'
import SshQuickExecDialog from './SshQuickExecDialog.vue'
import SshForwardPanel from './SshForwardPanel.vue'

const { t } = useI18n()
const { confirm, error: toastError, success: toastSuccess } = useToast()

const hosts = ref<SshHost[]>([])
const loading = ref(true)
const loadErr = ref('')
const sessions = ref<SshSessionInfo[]>([])

const tabs = ref<SshTab[]>([])
const activeId = ref('')
const collapsed = ref<Set<string>>(new Set())

/**
 * 主机搜索关键字。
 *
 * 放在父级而不是树里：树是无状态渲染（props 进、事件出），
 * 让树自己持有关键字会让「按名字过滤」和「折叠状态」两套可变状态纠缠在一起。
 * 过滤本身是纯函数（utils/sshTree.filterSshTree），这里只负责持有输入。
 */
const hostKeyword = ref('')

const dialogVisible = ref(false)
const editingHost = ref<SshHost | null>(null)
/** 新建时的父节点：为空表示建在根。 */
const creatingParentId = ref('')
/** 已信任服务器（指纹管理）弹窗 */
const trustedVisible = ref(false)
/** 新建分组弹窗及其父节点 */
const folderVisible = ref(false)
const folderParentId = ref('')
/** SFTP 文件面板：按标签 id 记录谁开着，关某个标签时要一起收掉 */
const sftpTabs = ref<Set<string>>(new Set())

/** 批量执行弹窗；preselect 是打开时要预勾选的主机（从分组菜单进来时带整棵子树） */
const batchVisible = ref(false)
const batchPreselect = ref<string[]>([])
/** 端口转发抽屉显隐：由左工具条按钮切换，面板自身负责列表刷新/轮询 */
const forwardVisible = ref(false)
/** 端口转发面板「所属标签」下拉：从当前标签派生 {id,label}。
 *  tabs 与 host.name 都是响应式，标签开关/重命名会自动同步给面板——否则
 *  面板拿不到标签上下文，规则里的 ownerTabId（形如 hostId::序号）只会显示原始 id。 */
const forwardTabOptions = computed<Array<{ id: string; label: string }>>(() =>
  tabs.value.map((tab) => ({ id: tab.id, label: tab.host.name || tab.host.host })),
)

// ---- OSC 7 终端目录跟随（默认关闭） ----
const FOLLOW_CWD_KEY = 'qd_ssh_follow_cwd'
const followCwd = ref(false)
/**
 * 下发给「当前激活且已打开文件面板」那一个标签的目标路径；null = 不下发。
 *
 * 带自增 seq 而不是裸字符串：远端 PROMPT_COMMAND 每次提示符都上报同一个 $PWD，
 * 用户手动把面板切到别的目录后，再敲命令时路径没变 → 赋相同值不触发 watch，
 * 「跟随终端目录」就只生效一次。
 */
const followSftpPath = ref<{ path: string; seq: number } | null>(null)
let followSeq = 0

function loadFollowCwd() {
  try {
    followCwd.value = localStorage.getItem(FOLLOW_CWD_KEY) === 'true'
  } catch (e) {
    // 隐私模式 / 配额满时 localStorage 会抛：回落默认关，不让一个偏好读失败挡住整页。
    logErr('SshPage.loadFollowCwd', e)
    followCwd.value = false
  }
}

function toggleFollowCwd() {
  followCwd.value = !followCwd.value
  // 关掉开关时立刻撤掉待下发的路径，否则下次打开会把上次那条旧目录灌进面板。
  if (!followCwd.value) followSftpPath.value = null
  try {
    localStorage.setItem(FOLLOW_CWD_KEY, String(followCwd.value))
  } catch (e) {
    logErr('SshPage.toggleFollowCwd', e)
  }
}

// 切换标签也要清空：留在 ref 里的是上一个标签的目录，直接传给新标签的面板
// 会把 B 主机的文件列表指到 A 主机上报的路径上。
watch(activeId, () => {
  followSftpPath.value = null
})

// ---- 远端 shell 集成（登录后注入 OSC 7 钩子，默认开） ----
//
// 这是「跟随终端目录」能生效的前提：OSC 7 不是终端的能力，而是 shell 每次画
// 提示符时自己打印的序列。bash 默认不打，zsh 要装 oh-my-zsh 之类才有，所以在
// 裸 Debian/Ubuntu 上不注入就永远收不到事件——功能看着像坏的。
//
// 注入的是连接时写进会话的一行命令（改 PROMPT_COMMAND / 加 precmd 钩子），
// 不改远端任何文件、重连即消失；代价是这行命令会被 tty 回显在会话顶部。
// 所以给一个关掉的开关：关掉后会话里零噪声，但跟随目录只能靠远端自己配好。
const SHELL_INTEG_KEY = 'qd_ssh_shell_integ'
const shellIntegration = ref(true)

function loadShellIntegration() {
  try {
    // 只认显式写过的 'false'：没存过（新装）默认开，否则「功能默认不工作」。
    shellIntegration.value = localStorage.getItem(SHELL_INTEG_KEY) !== 'false'
  } catch (e) {
    logErr('SshPage.loadShellIntegration', e)
    shellIntegration.value = true
  }
}

function toggleShellIntegration() {
  shellIntegration.value = !shellIntegration.value
  try {
    localStorage.setItem(SHELL_INTEG_KEY, String(shellIntegration.value))
  } catch (e) {
    logErr('SshPage.toggleShellIntegration', e)
  }
}

/**
 * OSC 7 事件 → 只驱动「当前激活且已打开文件面板」的那一个标签。
 *
 * 每个标签都有一份 SshTerminal，全局 DOM 事件是所有实例共用的；事件里带
 * sessionId 就是为了在这里反查标签，避免后台主机的一条 cd 把用户正在看的
 * 文件面板切走。路径已由终端侧做过安全校验（绝对路径、无 ..、无控制字符），
 * 但那仍是远端可伪造的输入，所以开关默认关闭，且只影响可视化目录导航，
 * 不会触发任何上传/下载/删除。
 */
function onSshCwd(ev: Event) {
  if (!followCwd.value) return
  const detail = (ev as CustomEvent<{ path?: string; sessionId?: string }>).detail
  const path = detail?.path || ''
  const sid = detail?.sessionId || ''
  if (!path || !sid) return
  const active = activeId.value
  if (!active || !sftpTabs.value.has(active)) return
  const tab = findTab(tabs.value, active)
  if (!tab || tabSessionId(tab) !== sid) return
  followSftpPath.value = { path, seq: ++followSeq }
}

let offShown: (() => void) | null = null
let offConnect: (() => void) | null = null
// 命令面板可能在窗口刚创建、主机清单还没回来时就发来连接请求：
// 先记下 id，load() 完成后再补偿一次，否则这次点击就静默丢了。
let pendingConnectId = ''

const tree = computed(() => buildSshTree(hosts.value))

const hostCount = computed(() => {
  let n = 0
  for (const h of hosts.value) if (!h.isFolder) n++
  return n
})

/** 有会话在跑（或留着回放）的主机 id，用来在树上打绿点。 */
const busyHostIds = computed(() => {
  const set = new Set<string>()
  for (const s of sessions.value) {
    if (s.hostId) set.add(s.hostId)
  }
  return set
})

/** 多标签下「选中」是多个（每台已打开的主机都算），左树要能同时高亮。 */
const activeHostIds = computed(() => new Set(tabs.value.map(t => t.hostId)))

const activeTab = computed(() => tabs.value.find(t => t.id === activeId.value) || null)

/** 文件面板：给当前活动标签里的那台主机开/关 SFTP 面板。 */
function toggleSftp(tabId: string) {
  const next = new Set(sftpTabs.value)
  if (next.has(tabId)) next.delete(tabId)
  else next.add(tabId)
  sftpTabs.value = next
}
/** 打开批量执行。传空数组 = 从工具栏进来，让用户自己勾。 */
function openBatch(hostIds: string[]) {
  batchPreselect.value = hostIds
  batchVisible.value = true
}

const folderNames = computed(() =>
  hosts.value.filter(h => h.isFolder).map(h => h.name),
)

async function loadSessions() {
  try {
    sessions.value = unwrap<SshSessionInfo[]>(await ListSessions()) || []
  } catch {
    // 会话列表拿不到不该挡住主机清单——那才是这个页面的主功能。
    // 恢复不了只是退回「重新连一次」，不至于让整个页面不可用。
    sessions.value = []
  }
}

/**
 * 从会话列表里给标签找一条可接回的会话。
 *
 * 场景：用户之前开过这台机器的标签又关掉了标签（但 Go 侧会话因异常没清干净），
 * 或者在别的窗口开着这台机器。此时直接 Connect 会多开一个 channel。
 */
function findResumableFor(hostId: string): SshSessionInfo | null {
  return findResumable(sessions.value, hostId, isSessionRetained)
}

async function load() {
  loading.value = true
  loadErr.value = ''
  try {
    hosts.value = unwrap<SshHost[]>(await ListHosts()) || []
    // 已打开的标签要跟着刷新主机名/环境色，否则用户改完配置回到标签
    // 看到的还是打开那一刻的样子。
    tabs.value = syncHost(tabs.value, hosts.value)
  } catch (e: unknown) {
    loadErr.value = getErrorMessage(e)
    hosts.value = []
  } finally {
    loading.value = false
  }
}

/** 导出主机清单为 JSON。目录项只是分组节点、没有任何连接信息，导进去只会得到
 *  一堆空壳，所以只导出真正的主机；SshHost 不含明文密码，可安全落盘。 */
function exportHosts() {
  const stamp = (d: Date) => {
    const p = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
  }
  try {
    const json = JSON.stringify(hosts.value.filter((h) => !h.isFolder), null, 2)
    const blob = new Blob([json], { type: 'application/json;charset=utf-8' })
    downloadBlob(`quickdock-ssh-hosts-${stamp(new Date())}.json`, blob)
    toastSuccess(t('sshExportHostsDone'))
  } catch (e) {
    toastError(getErrorMessage(e))
    logErr('ssh.exportHosts', e)
  }
}

// ---- 标签操作 ----

/** 点主机：该主机已有标签就聚焦最近打开的那个，否则新建。 */
async function selectHost(h: SshHost) {
  if (h.isFolder) return
  // 从别的标签切过来时，当前标签可能正跑着命令。但不弹确认：
  // 标签栏的意义就是随时切换，切换不销毁会话（SshSessionView 靠 v-show 保留），
  // 所以这里没有任何破坏性后果，弹框只会让多标签变得难用。
  const r = openOrFocus(tabs.value, h, findResumableFor(h.id))
  tabs.value = r.tabs
  activeId.value = r.activeId
}

/** 复制当前会话到新标签：同一台主机再开一个独立会话（共享 TCP，各自一个 channel）。
 *  用于「一个标签跑更新命令、另一个看日志/比对配置」这类场景。 */
function duplicateTab(tabId: string) {
  const tab = findTab(tabs.value, tabId)
  if (!tab) return
  // resume 传 null：强制新开一个 channel，而非接回这条已有会话。
  const r = openTab(tabs.value, tab.host, null)
  tabs.value = r.tabs
  activeId.value = r.activeId
}

async function closeTabById(tabId: string) {
  // 确认必须在动 tabs 之前。
  //
  // 一旦从 tabs 里摘掉这一项，v-for 会立刻卸载对应的 SshSessionView，
  // xterm 随之 dispose——用户刚看的滚动缓冲就没了。此时再点「取消」，
  // 重建出来的是一个全新组件实例：它会重新 doConnect() 开出第二条
  // 会话，而原来那条在 Go 侧既没人引用、也没有 UI 入口能关掉（孤儿会话），
  // 连接池的引用计数也永远归不了零。所以先问，问完再拆。
  const target = findTab(tabs.value, tabId)
  if (!target) return

  const sid = tabSessionId(target)
  if (sid) {
    const ok = await confirm(t('sshCloseTabConfirm', { name: target.host.name || target.host.host }))
    if (!ok) return
  }

  const r = closeTab(tabs.value, tabId, activeId.value)
  tabs.value = r.tabs
  activeId.value = r.activeId

  // 标签没了，它的文件面板也就没了宿主主机——不摘掉的话 Set 项会一直累积。
  if (sftpTabs.value.has(tabId)) {
    const next = new Set(sftpTabs.value)
    next.delete(tabId)
    sftpTabs.value = next
  }

  // 显式绑到这个标签的转发规则要随标签一起停。全局规则（ownerTabId 为空）
  // 不受影响——那是有意做成跨标签常驻的，不能因为关了一个终端就掐掉。
  // 失败只记日志：端口没停成会在转发面板里显示为仍在运行，用户可以手动停，
  // 不该因为一个清理动作把「关标签」整个卡住。
  try {
    unwrap(await SshStopForwardsByTab(tabId))
  } catch (e: unknown) {
    logErr('SshPage.closeTabById.stopForwards', e)
  }

  // 必须断开：不断的话 Go 侧那条 session 永远留着，回放缓冲占内存，
  // 连接池的引用计数也减不下去（最后一个引用不归零，TCP 会一直吊着）。
  if (!sid) return
  try {
    unwrap(await Disconnect(sid))
  } catch {
    // 会话可能已经因为断网消失，这里失败不影响用户继续操作
  }
  await loadSessions()
}

/** 标签栏的「+」：回到主机选择。开着标签时不该凭空连一台机器。 */
function onNewTab() {
  // 不清空已有标签——「新建」是加一个，不是换一批。
  // 用户想找另一台机器，点左树就行。
  if (tabs.value.length === 0) return
  activeId.value = ''
}

function markTabBusy(tabId: string, busy: boolean) {
  tabs.value = patchTab(tabs.value, tabId, { busy })
}

/**
 * 记录子组件拿到的 sessionId。
 *
 * 必须存：关标签时父级要断会话，而 sessionId 只存在于子组件的作用域里。
 * 组件断开时会 emit 空串（onDisconnectClick），清掉即可——
 * 留着旧的 id 会让「关标签」去断一条已经不存在的会话。
 */
function onTabSession(tabId: string, sessionId: string) {
  tabs.value = patchTab(tabs.value, tabId, { sessionId })
}

// ---- 广播输入 ----
//
// 安全取舍（刻意如此，别「顺手优化」掉）：
//  · 不做全局/页面级快捷键：Enter 只绑在广播输入条自己的 @keydown.enter 上，
//    焦点在终端里敲回车绝不会把当前这一行打到所有机器；
//  · 不做记住开关：broadcastOpen 是普通 ref，不写 localStorage，页面重开即关，
//    杜绝「上次开着广播、这次没注意就发到一堆机器」；
//  · 每次都 confirm：哪怕刚发过、哪怕只有 2 个标签也重新问一次，把 N 与命令原文
//    摆到用户眼前；含 prod 标签时再在确认文案前拼一段点名生产机数量的醒目告警。
// 根因就一句：广播是「一次输入打到多台机器」，误操作没有撤销键，代价远高于
// 单标签输入，所以宁可多点一下、多按一次回车。
const broadcastOpen = ref(false)
const broadcastText = ref('')
/** 防连发：confirm 与逐条 Write 都是 await，期间第二次回车不能重入。 */
const broadcastSending = ref(false)

/**
 * 「已连接标签」= 本页已知 sessionId 非空、且不在连接中的标签。
 * tab.sessionId 由 SshSessionView 的 @session 在 Connect 成功 / 接回时回填，
 * 与关标签时用来 Disconnect 的是同一个 id，也是 Write 唯一认的入参。
 * 远端意外断线时父级不会立刻收到清空事件，那条 Write 会报 session_not_found，
 * 由下面逐条 try/catch 兜住，不影响其余标签。
 */
const broadcastTargets = computed(() =>
  tabs.value.filter(tb => !tb.busy && !!tabSessionId(tb)),
)

async function sendBroadcast() {
  if (broadcastSending.value) return
  const cmd = broadcastText.value.trim()
  const targets = broadcastTargets.value
  if (!cmd || !targets.length) {
    toastError(t('sshBroadcastEmpty'))
    return
  }
  const prod = targets.filter(tb => tb.host.env === 'prod').length
  const base = t('sshBroadcastConfirm', { n: targets.length, prod, cmd })
  // 含 prod 标签时，把醒目告警（sshBroadcastHint）拼到确认文案前，并把 prod 数量
  // 一并交给字典：useToast.confirm 只收一个字符串、ConfirmDialog 也没有 danger
  // 变体，所以「更醒目」只能靠更长、且点名生产机数量的告警文案来表达（R8 全走 t()）。
  const message = prod > 0 ? `${t('sshBroadcastHint')} ${base}` : base
  broadcastSending.value = true
  try {
    if (!(await confirm(message))) return
    // 编码照抄 SshTerminal.sendInput：UTF-8 字节 → base64；命令末尾补 \r 才会执行
    // （与 SshSessionView.sendToTerm 的 execute 分支一致）。
    const b64 = bytesToBase64(textToBytes(cmd + '\r'))
    for (const tb of targets) {
      const sid = tabSessionId(tb)
      if (!sid) continue
      try {
        unwrap(await Write(sid, b64))
      } catch (e: unknown) {
        // 逐条上报而不是整批中止：一条会话写失败不该让其它机器收不到命令，
        // 用户也需要知道是哪台没执行。R9：unwrap + try/catch + toast + logErr。
        toastError(getErrorMessage(e))
        logErr('SshPage.sendBroadcast', e)
      }
    }
    // 无论个别成败都清空：广播命令已发出且不可撤销，留着旧命令被二次回车
    // 会重复执行，务必清掉。
    broadcastText.value = ''
  } finally {
    broadcastSending.value = false
  }
}

// ---- 主机 CRUD ----

function openNew(parentId = '') {
  editingHost.value = null
  creatingParentId.value = parentId
  dialogVisible.value = true
}

// ---- 快速连接（host:port 直连入口） ----
//
// 【为什么「不落库直连」这条路走不通】
// 原本的设想是：解析 → 造一个 SshHost → openTab(tabs, host, null) 打开标签 →
// 让 SshSessionView 走 requestCredential 补录密码，全程不写库。把整条链路读完
// 后确认在当前架构下不成立，卡点有两层，且都在 SshSessionView.vue：
//   1) doConnect() 调的是 Connect(props.host.id, cols, rows)（:238），后端
//      services/sshx/session.go:115 第一步就 s.loadHost(hostID) 按 id 查库。
//      这是唯一一条交互连接入口，入参只有 hostID，没有「传连接参数」的重载
//      （TestHostInput 是另一条只测不连的路径）。未落库主机的 id 必然是空串或
//      伪造值，Connect 会直接报 ssh_host_not_found，连认证阶段都到不了。
//   2) 即便能走到认证，补录密码的 askPassword()（:294）拿到密码后调的是
//      SaveHost({ id: props.host.id, ... })（:313）——它本就是把主机整行写回库，
//      requestCredential 的 allowSave:false 注释也写明「不落库就没法重连」；
//      而且 needPassword 只在 NO_CREDENTIAL / AUTH_FAILED 分支置位，HOST_NOT_FOUND
//      根本不会触发 askPassword。
// 所以「不落库 + requestCredential 补录」在 Connect 这一关就断了，硬做只会留下
// 一个点了报「主机不存在」的死按钮（任务明确禁止）。按兜底要求改走可工作的路径：
//   解析 → 造 SshHost（id 空）→ 预填 SshHostDialog（按新建处理）→ 用户补密码点保存
//   → 主机上左树 → 点它连接。
// 每一步都有真实后端支撑，不是空壳；解析与构造仍复用 SshHost 形状，不落库由
// 「用户亲自点保存」这一步显式完成。

const quickInput = ref('')

interface QuickTarget { user: string; host: string; port: number }

/** 解析 user@host:port / host:port / host / [::1]:2222；非法返回 null。 */
function parseQuickConnect(raw: string): QuickTarget | null {
  const s = raw.trim()
  if (!s) return null

  // '@' 是 user 与 host 的唯一分隔（地址/IPv6 里不会出现 @）。取最后一个 @，
  // 顺带兼容「登录名里带 @」这种少见但合法的情况。
  const at = s.lastIndexOf('@')
  let user = 'root'
  let hostPort = s
  if (at >= 0) {
    const u = s.slice(0, at).trim()
    if (!u) return null
    user = u
    hostPort = s.slice(at + 1).trim()
  }
  if (!hostPort) return null

  let host = ''
  let port = 22
  if (hostPort.startsWith('[')) {
    // IPv6 字面量：[::1]:2222 / [2001:db8::1]，方括号里的原样留作 host。
    const m = /^\[([^\]]+)\](?::(\d+))?$/.exec(hostPort)
    if (!m) return null
    host = m[1]
    if (m[2] !== undefined) port = Number(m[2])
  } else {
    const colons = (hostPort.match(/:/g) || []).length
    if (colons >= 2) {
      // 裸 IPv6（如 ::1）无法区分端口，整段当主机、端口取默认 22。
      host = hostPort
    } else if (colons === 1) {
      const i = hostPort.lastIndexOf(':')
      host = hostPort.slice(0, i)
      port = Number(hostPort.slice(i + 1))
    } else {
      host = hostPort
    }
  }

  if (!host || !Number.isInteger(port) || port < 1 || port > 65535) return null
  return { user, host, port }
}

/** 把解析结果拼成一个「未落库」的 SshHost（id 留空，9 个新列给默认值）。 */
function makeQuickHost(p: QuickTarget): SshHost {
  return {
    id: '',
    name: p.host,
    host: p.host,
    port: p.port,
    user: p.user,
    authType: 'password',
    keyPath: '',
    legacyAlgorithms: false,
    env: 'test',
    jumpHostId: '',
    parentId: '',
    isFolder: false,
    sort: 0,
    remark: '',
    lastConnectedAt: '',
    createdAt: '',
    updatedAt: '',
    hasPassword: false,
    hasPassphrase: false,
    mcpAllowed: false,
    tags: '',
    color: '',
    icon: '',
    postLoginCommand: '',
    autoReplySudo: false,
    proxyCommand: '',
    proxyCommandEnabled: false,
    keepaliveInterval: 0,
  }
}

function onQuickConnect() {
  const p = parseQuickConnect(quickInput.value)
  if (!p) {
    toastError(t('sshQuickConnectInvalid'))
    return
  }
  // 不落库直连走不通（见上方长注释），改由「预填的新建主机表单」承接：
  // id 为空使 SshHostDialog 按新建处理，字段已带好，用户只需补密码并点保存。
  editingHost.value = makeQuickHost(p)
  creatingParentId.value = ''
  dialogVisible.value = true
  quickInput.value = ''
}

/** 新建文件夹：不走主机表单（那里全是地址/凭据字段，对文件夹无意义）。 */
function createFolder(parentId: string) {
  folderParentId.value = parentId
  folderVisible.value = true
}

async function submitFolder(name: string) {
  folderVisible.value = false
  try {
    unwrap(
      await SaveHost({
        id: '',
        name,
        host: '',
        port: 22,
        user: 'root',
        authType: 'password',
        password: '',
        clearPassword: false,
        keyPath: '',
        keyPassphrase: '',
        legacyAlgorithms: false,
        env: '',
        jumpHostId: '',
        mcpAllowed: false,
        tags: '',
        color: '',
        icon: '',
        postLoginCommand: '',
        autoReplySudo: false,
        proxyCommand: '',
        proxyCommandEnabled: false,
        keepaliveInterval: 0,
        parentId: folderParentId.value,
        isFolder: true,
        sort: 0,
        remark: '',
      }),
    )
    await load()
  } catch (e) {
    toastError(getErrorMessage(e))
  }
}

function openEdit(h: SshHost) {
  editingHost.value = h
  creatingParentId.value = ''
  dialogVisible.value = true
}

// ---- 单主机详情 ----
const detailVisible = ref(false)
const detailHost = ref<SshHost | null>(null)
const detailFolderName = ref('')

function openDetail(h: SshHost) {
  detailHost.value = h
  // 分组名在树里才有（hosts 是扁平列表，只有 parentId），这里顺手查出来：
  // 详情里写「未分组」还是分组名，是用户判断这台机器归属的唯一依据。
  detailFolderName.value = h.parentId ? (hosts.value.find((x) => x.id === h.parentId)?.name || '') : ''
  detailVisible.value = true
}

/** 详情里点「编辑」：关详情 → 开编辑表单，避免两个弹窗叠在一起。 */
function editFromDetail(h: SshHost) {
  detailVisible.value = false
  openEdit(h)
}

// ---- 一次性执行命令 ----
//
// 从详情弹窗进来：不关详情。用户刚在详情里看过凭据状态、环境与地址，
// 跑完命令往往还要回去核对，关掉再打开是纯损失。执行弹窗的 z-index 更高，
// 直接叠在上面，Esc 先收掉它。
const execVisible = ref(false)
const execHost = ref<SshHost | null>(null)

function openExec(h: SshHost) {
  execHost.value = h
  execVisible.value = true
}

async function onSaved() {
  dialogVisible.value = false
  await load()
}

async function removeHost(h: SshHost) {
  const extra = h.isFolder ? t('sshDeleteFolderExtra') : ''
  const ok = await confirm(t('sshDeleteConfirm', { name: h.name || h.host }) + extra)
  if (!ok) return
  try {
    unwrap(await DeleteHost(h.id))
    // 被删的主机若有打开的标签，一并关掉并断开——否则标签会停在一块死画面上。
    //
    // 删文件夹必须展开子树：后端是级联删除的，而文件夹本身不会有标签，
    // 只看 tab.hostId === h.id 会一个都匹配不到。结果就是分组从树上消失了，
    // 但里面主机的标签和会话全留着——重连必然报 ssh_host_not_found，
    // 而且那条 Go 侧会话从此没有任何 UI 入口能关掉。
    const doomed = new Set(h.isFolder ? collectSubtreeIds(tree.value, h.id) : [h.id])
    const affected = tabs.value.filter(tab => doomed.has(tab.hostId))
    for (const tab of affected) {
      const sid = tabSessionId(tab)
      if (sid) {
        try {
          unwrap(await Disconnect(sid))
        } catch {
          // 已经断了，忽略
        }
      }
    }
    if (affected.length) {
      // doomed 装的是 hostId，而 tab.id 是 `${hostId}::${seq}`（见 utils/sshTabs
      // 的 makeTabId），两者不同命名空间——用 doomed.has(tab.id) 永远匹配不上，
      // 标签一个都关不掉。上面 affected 的判定用的是 tab.hostId，这里必须一致。
      tabs.value = tabs.value.filter(tab => !doomed.has(tab.hostId))
      if (!tabs.value.some(tab => tab.id === activeId.value)) {
        activeId.value = tabs.value.length ? tabs.value[tabs.value.length - 1].id : ''
      }
      // 顺带收掉这些标签的文件面板，否则 Set 里会留下永不清理的项。
      // sftpTabs 存的是 tabId 而非 hostId，故这里要遍历 affected 取 tab.id。
      const next = new Set(sftpTabs.value)
      for (const tab of affected) next.delete(tab.id)
      sftpTabs.value = next
    }
    await load()
    await loadSessions()
  } catch (e) {
    toastError(getErrorMessage(e))
  }
}

async function moveHost(payload: { id: string; parentId: string }) {
  try {
    unwrap(await MoveHost(payload.id, payload.parentId))
    await load()
  } catch (e) {
    // 非法落点（拖进自己的子孙）后端会拒绝，前端已经先拦了一道，
    // 能走到这里说明是并发或脏数据。刷新回真实结构，别让树停在错误状态。
    toastError(getErrorMessage(e))
    await load()
  }
}

function toggleFolder(id: string) {
  const next = new Set(collapsed.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  collapsed.value = next
}

// 会话列表变化时树的绿点标记自动更新（busyHostIds 是 computed，模板直接依赖它，
// 不需要额外的 watch —— 加一个空 watch 只会让人误以为这里有副作用逻辑）。

/** 首屏一次最多接回几条会话（见 restoreTabs 里的取舍说明）。 */
const RESTORE_MAX = 8

/**
 * 首屏恢复：把上一个窗口里还留着的会话按原顺序接回标签。
 *
 * 为什么不再「只恢复最近一条」：用户关窗时开着 5 个终端，重开只剩 1 个，
 * 剩下 4 个要自己一台台点回来——「记住上次开了哪些」是终端工具的基本预期，
 * 少恢复反而是丢状态。
 *
 * 但仍要有上限：崩溃重连或长期积累可能留下几十条残留会话，一次全接回会让
 * 首屏卡很久（每条都要抓一次回放），标签栏也会被撑成一行省略号。
 * 取最近 RESTORE_MAX 条，按 started 升序排，最新的那个标签落在最右侧并激活——
 * 和用户自己一个个点开的观感一致。
 */
function restoreTabs() {
  const kept = sessions.value
    .filter(isSessionRetained)
    .sort((a, b) => a.started - b.started)
    .slice(-RESTORE_MAX)
  let next = tabs.value
  let lastId = ''
  for (const s of kept) {
    // 认不出主人（主机被删了）就不恢复：硬接回去用户会看到一个
    // 不认识的终端画面，比重新连一次更难理解。
    const h = hosts.value.find(x => x.id === s.hostId && !x.isFolder)
    if (!h) continue
    const r = openTab(next, h, s)
    next = r.tabs
    lastId = r.activeId
  }
  if (!lastId) return
  tabs.value = next
  activeId.value = lastId
}

onMounted(async () => {
  // 订阅要放在第一个 await 之前。
  //
  // 挂载期间若发生卸载（窗口 hash 切换、组件被替换），onBeforeUnmount 会先跑，
  // 那时 offShown 还是 null；等 await 返回后再注册，就再没有人对它解绑了。
  loadFollowCwd()
  loadShellIntegration()
  // OSC 7 目录跟随：订阅 SshTerminal 发出的自定义 DOM 事件（另一组实现）。
  window.addEventListener('quickdock:ssh:cwd', onSshCwd)
  // 命令面板选中某台主机：后端已把窗口 Show 出来，这里只负责连上。
  // 走 selectHost 而不是另写一条连接路径——点树上的主机与从面板进来必须同一条逻辑，
  // 否则「已有标签就聚焦」这类行为只会在其中一处生效。
  offConnect = Events.On('quickdock:ssh:connect', (ev: any) => {
    const raw = ev?.data ?? ev
    const id = typeof raw === 'string' ? raw : (raw?.hostId || '')
    if (!id) return
    const h = hosts.value.find(x => x.id === id)
    if (h) void selectHost(h)
    else pendingConnectId = id
  })
  offShown = Events.On('quickdock:ssh:shown', () => {
    if (loading.value) return
    void (async () => {
      await load()
      // 会话列表也要刷：窗口隐藏期间可能有会话断开了，只刷主机清单的话
      // 绿点还挂在树上，用户点进去会拿到一条已死的会话去「接回」。
      await loadSessions()
    })()
  })

  await load()
  await loadSessions()
  restoreTabs()
  if (pendingConnectId) {
    const id = pendingConnectId
    pendingConnectId = ''
    const h = hosts.value.find(x => x.id === id)
    if (h) void selectHost(h)
  }
})

onBeforeUnmount(() => {
  offShown?.()
  offConnect?.()
  offShown = null
  window.removeEventListener('quickdock:ssh:cwd', onSshCwd)
})

/** 供调试：当前标签与连接状态。 */
defineExpose({
  tabCount: () => tabs.value.length,
})
</script>

<template>
  <div class="ssh-page">
    <aside class="ssh-side">
      <header class="ssh-side-head">
        <div class="ssh-side-title">
          <span>{{ t('sshHostsTitle') }}</span>
          <em v-if="hostCount" class="ssh-count">{{ hostCount }}</em>
        </div>
        <div class="ssh-side-tools">
          <button class="ssh-tool" :title="t('sshNewFolder')" @click="createFolder('')">
            <FolderPlus :size="14" />
          </button>
          <button class="ssh-tool" :title="t('sshNewHost')" @click="openNew('')">
            <Plus :size="14" />
          </button>
          <button class="ssh-tool" :disabled="loading" :title="t('refresh')" @click="load">
            <RefreshCw :size="13" :class="{ spinning: loading }" />
          </button>
          <button class="ssh-tool" :title="t('sshTrustedTitle')" @click="trustedVisible = true">
            <ShieldCheck :size="14" />
          </button>
          <button
            class="ssh-tool"
            :disabled="!hostCount"
            :title="t('sshExportHosts')"
            @click="exportHosts"
          >
            <Download :size="14" />
          </button>
          <button
            class="ssh-tool"
            :disabled="!hostCount"
            :title="t('sshBatchTitle')"
            @click="openBatch([])"
          >
            <ListChecks :size="14" />
          </button>
          <!-- @mousedown.stop：抽屉用全局 mousedown 判「点外部关闭」，这里挡住，
               否则点按钮切换时会被判成外部点击，刚打开又被自己关掉。 -->
          <button
            class="ssh-tool"
            :title="t('sshForwardTitle')"
            @mousedown.stop
            @click="forwardVisible = !forwardVisible"
          >
            <Network :size="14" />
          </button>
        </div>
      </header>

      <div v-if="loadErr" class="ssh-error">{{ loadErr }}</div>

      <!-- 搜索框只在有主机时出现：一台都没有时它的唯一作用是让人困惑 -->
      <QdSearchBox
        v-if="hostCount > 0"
        v-model="hostKeyword"
        class="ssh-side-search"
        :icon-size="12"
        :placeholder="t('sshSearchHosts')"
      />

      <!-- 快速连接：始终可见（0 台主机时也能用）。不落库直连走不通，见脚本注释；
           这里解析后把结果预填进新建主机表单，由用户点保存。 -->
      <QdSearchBox
        v-model="quickInput"
        class="ssh-side-search ssh-quick"
        :icon-size="12"
        :show-icon="false"
        :clearable="false"
        :placeholder="t('sshQuickConnectPlaceholder')"
        @submit="onQuickConnect"
      >
        <template #trailing>
          <button class="ssh-side-search-clear" :title="t('sshQuickConnect')" @click="onQuickConnect">
            <Zap :size="12" />
          </button>
        </template>
      </QdSearchBox>

      <SshHostTree
        :tree="tree"
        :loading="loading"
        :load-err="loadErr"
        :busy-host-ids="busyHostIds"
        :active-host-ids="activeHostIds"
        :collapsed="collapsed"
        :keyword="hostKeyword"
        @select="selectHost"
        @toggle="toggleFolder"
        @edit="openEdit"
        @detail="openDetail"
        @delete="removeHost"
        @create="openNew"
        @create-folder="createFolder"
        @move="moveHost"
        @refresh="load"
        @batch="(folderId: string) => openBatch(collectSubtreeIds(tree, folderId))"
      />
    </aside>

    <main class="ssh-main">
      <SshTabBar
        v-if="tabs.length"
        :tabs="tabs"
        :active-id="activeId"
        :files-open="!!activeId && sftpTabs.has(activeId)"
        @select="(id: string) => (activeId = id)"
        @close="closeTabById"
        @new="onNewTab"
        @files="toggleSftp(activeId)"
        @duplicate="duplicateTab"
        @reorder="(from: number, to: number) => (tabs = reorderSshTabs(tabs, from, to))"
      />

      <!-- 标签栏工具条：目录跟随（页面级开关）+ 广播（多标签才出现）。 -->
      <div class="ssh-toolbar">
        <button
          class="ssh-tool"
          :class="{ 'is-on': followCwd }"
          :title="t('sshFollowCwdHint')"
          @click="toggleFollowCwd"
        >
          <MapPin :size="14" />
          <span>{{ t('sshFollowCwd') }}</span>
        </button>
        <button
          class="ssh-tool"
          :class="{ 'is-on': shellIntegration }"
          :title="t('sshShellIntegHint')"
          @click="toggleShellIntegration"
        >
          <SquareTerminal :size="14" />
          <span>{{ t('sshShellInteg') }}</span>
        </button>
        <span class="ssh-toolbar-spacer" />
        <button
          v-if="tabs.length >= 2"
          class="ssh-tool"
          :class="{ 'is-on': broadcastOpen }"
          :title="t('sshBroadcast')"
          @click="broadcastOpen = !broadcastOpen"
        >
          <Radio :size="14" />
          <span>{{ t('sshBroadcast') }}</span>
        </button>
      </div>

      <!-- 广播输入条：只在广播打开且确有 >=2 个标签时出现；Enter 只绑在这个输入框上。 -->
      <div v-if="broadcastOpen && tabs.length >= 2" class="ssh-broadcast-bar">
        <span class="text-muted ssh-broadcast-hint">{{ t('sshBroadcastHint') }}</span>
        <input
          v-model="broadcastText"
          class="ssh-broadcast-input"
          spellcheck="false"
          @keydown.enter="sendBroadcast"
        />
        <button
          class="ssh-tool"
          :disabled="!broadcastText.trim() || !broadcastTargets.length || broadcastSending"
          :title="t('sshBroadcast')"
          @click="sendBroadcast"
        >
          <Send :size="13" />
        </button>
      </div>

      <div class="ssh-stage">
        <!--
          用 v-show 而不是 v-if：切标签不能卸载 SshSessionView，
          卸载会触发它的 onBeforeUnmount → Disconnect，等于「一切标签就断线」。
        -->
        <SshSessionView
          v-for="tab in tabs"
          v-show="tab.id === activeId"
          :key="tab.id"
          :host="tab.host"
          :resume="tab.resume"
          :focused="tab.id === activeId"
          :shell-integration="shellIntegration"
          @busy="(v: boolean) => markTabBusy(tab.id, v)"
          @session="(sid: string) => onTabSession(tab.id, sid)"
        />

        <!--
          每个标签一份实例（而不是共用一个）。共用会让「A 开着文件面板 → 切到 B
          → 切回 A」时 A 的目录、滚动位置、选中行全丢——host prop 一变内部
          就重新拉列表了。代价只是多几个组件实例，它们都是 v-show 隐藏的，
          不占可见开销。
        -->
        <template v-for="tab in tabs" :key="`sftp-${tab.id}`">
          <SftpPanel
            v-show="tab.id === activeId && sftpTabs.has(tab.id)"
            class="ssh-sftp"
            :host="tab.host"
            :active="tab.id === activeId && sftpTabs.has(tab.id)"
            :follow-path="tab.id === activeId && sftpTabs.has(tab.id) ? followSftpPath : null"
            @close="toggleSftp(tab.id)"
          />
        </template>

        <div v-if="!activeTab" class="ssh-welcome">
          <Server :size="34" class="ssh-welcome-icon" />
          <p class="ssh-welcome-text">{{ t('sshPickHost') }}</p>
          <p class="ssh-welcome-sub">{{ t('sshHostsDesc') }}</p>
        </div>
      </div>
    </main>

    <SshHostDialog
      :visible="dialogVisible"
      :host="editingHost"
      :parent-id="creatingParentId"
      @saved="onSaved"
      @cancel="dialogVisible = false"
    />

    <SshHostDetailDialog
      :visible="detailVisible"
      :host-id="detailHost?.id || ''"
      :fallback="detailHost"
      :folder-name="detailFolderName"
      @edit="editFromDetail"
      @exec="openExec"
      @close="detailVisible = false"
    />

    <SshQuickExecDialog
      :visible="execVisible"
      :host="execHost"
      @close="execVisible = false"
    />

    <SshTrustedHostsDialog v-model:visible="trustedVisible" />

    <SshBatchDialog
      v-model:visible="batchVisible"
      :hosts="hosts"
      :preselect="batchPreselect"
    />

    <SshFolderDialog
      :visible="folderVisible"
      :existing="folderNames"
      @submit="submitFolder"
      @cancel="folderVisible = false"
    />

    <!-- 端口转发抽屉：绝对定位浮层，不占侧栏、不顶掉会话舞台。
         主机清单直接复用本页的 hosts（ListHosts 结果，随 load() 刷新）。 -->
    <SshForwardPanel
      :open="forwardVisible"
      :hosts="hosts"
      :tabs="forwardTabOptions"
      @close="forwardVisible = false"
    />
  </div>
</template>

<style scoped>
.ssh-page {
  display: flex;
  height: 100vh;
  min-height: 0;
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
}

/* ---- 左侧主机树 ---- */
.ssh-side {
  display: flex;
  flex-direction: column;
  flex: 0 0 268px;
  min-height: 0;
  border-right: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}

.ssh-side-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  flex: 0 0 auto;
  padding: var(--space-5) var(--space-5) var(--space-4) var(--space-6);
}

.ssh-side-title {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
  font-size: var(--text-md);
  font-weight: 600;
  color: var(--color-text-primary);
}

.ssh-count {
  padding: 0 var(--space-2);
  font-size: var(--text-xs);
  font-style: normal;
  font-weight: 500;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-full);
}

.ssh-side-tools {
  display: flex;
  /* 7 个图标按钮在英文标题下会超过侧栏一行宽度换行兜底，避免工具区溢出被裁 */
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: var(--space-2);
}

.ssh-tool {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast);
}

.ssh-tool:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.ssh-tool:disabled {
  cursor: default;
  opacity: var(--opacity-disabled);
}

.spinning {
  animation: ssh-spin 900ms linear infinite;
}

@keyframes ssh-spin {
  to {
    transform: rotate(360deg);
  }
}

.ssh-error {
  margin: 0 var(--space-5) var(--space-4);
  padding: var(--space-3) var(--space-4);
  font-size: var(--text-sm);
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-radius: var(--radius-xs);
}

/* ---- 左侧：主机搜索 ---- */
/* 搜索框皮肤由 QdSearchBox 的 .qd-search-box 统一提供，这里只留外侧留白。
   （.sm 变体已废弃：全项目搜索框统一 28px。） */
.ssh-side-search {
  flex: 0 0 auto;
  margin: 0 var(--space-4) var(--space-3);
}

/* 快速连接行右侧的「连接」按钮；结构上仍在 QdSearchBox 里，所以这里只给按钮本身。 */
.ssh-side-search-clear {
  display: flex;
  flex: 0 0 auto;
  padding: 0;
  color: var(--color-text-muted);
  background: transparent;
  border: none;
  cursor: pointer;
}

.ssh-side-search-clear:hover {
  color: var(--color-text-primary);
}

/* ---- 右侧：标签栏 + 终端 ---- */
.ssh-main {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.ssh-stage {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
}

/* v-show 隐藏的标签要彻底移出布局流，否则 display:none 之外的
   定位方式仍会占位；这里统一绝对定位叠放，切标签即切 z 序。 */
.ssh-stage > :deep(.sess) {
  flex: 1 1 auto;
  min-width: 0;
}

/* 文件面板与终端横向分栏：MobaXterm 的经典布局是「左边终端、右边文件」，
   用户传完文件还要在终端里敲解压命令，两者必须同屏可见——
   做成标签切换会逼用户来回翻。 */
.ssh-sftp {
  flex: 0 0 40%;
  min-width: 300px;
  max-width: 620px;
  border-left: 1px solid var(--color-border);
}

.ssh-welcome {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  padding: var(--space-8);
  color: var(--color-text-muted);
  text-align: center;
}

.ssh-welcome-icon {
  color: var(--color-text-muted);
}

.ssh-welcome-text {
  margin: 0;
  font-size: var(--text-md);
  color: var(--color-text-secondary);
}

.ssh-welcome-sub {
  margin: 0;
  max-width: 320px;
  font-size: var(--text-xs);
  line-height: 1.6;
}

/* ---- 标签栏工具条 ----
   按钮复用左树现成的 .ssh-tool 图标按钮，hint 复用全局 .text-muted（R6）；
   颜色/间距/圆角一律取 style.css 已有 token（R4/R5），无硬编码色值。 */
.ssh-toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}

.ssh-toolbar-spacer {
  flex: 1 1 auto;
}

/* 工具条里的 .ssh-tool 多了文字标签，只补间距与字号，其余外观仍完全来自公共类 */
.ssh-toolbar .ssh-tool {
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  font-size: var(--text-xs);
  font-family: inherit;
}

.ssh-tool.is-on {
  color: var(--color-accent);
  background: var(--color-accent-bg);
}

/* ---- 广播输入条 ---- */
.ssh-broadcast-bar {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}

.ssh-broadcast-hint {
  flex: 0 0 auto;
  font-size: var(--text-xs);
}

.ssh-broadcast-input {
  flex: 1 1 auto;
  min-width: 0;
  padding: var(--space-2) var(--space-3);
  font-size: var(--text-sm);
  font-family: var(--font-mono);
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
}

.ssh-broadcast-input:focus {
  border-color: var(--color-border-focus-soft);
}

/* 快速连接行整体复用 .ssh-side-search（含 :focus-within），
   只把「连接」按钮染成强调色，与上方搜索框的「清空」按钮区分开。 */
.ssh-quick .ssh-side-search-clear {
  color: var(--color-accent);
}

.ssh-quick .ssh-side-search-clear:hover {
  color: var(--color-accent-hover);
}

/* 窗口最小宽度 640：窄屏时压缩侧栏，把宽度让给终端（一行命令别被折成两行） */
@media (max-width: 760px) {
  .ssh-side {
    flex-basis: 212px;
  }
}
</style>
