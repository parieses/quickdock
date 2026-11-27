<script setup lang="ts">
import { copyToClipboard } from '../utils/clipboard'
import GitDialogs from './GitDialogs.vue'
/**
 * Git 面板（对位 SourceTree 的布局，而不是「三张卡片」）：
 *
 *   ┌ 工具栏：仓库名 + 路径 ────────────── [Fetch][Pull][刷新] ┐
 *   ├────────────┬──────────────────────────────────────────────┤
 *   │ 仓库        │ 提交 · 分支 · 作者 · 时间   ← 列表表头           │
 *   │ 本地分支     │ ● 单行提交（参照 GitLens：一行 28px）             │
 *   │ 远程分支     ├──────────────────────────────────────────────┤
 *   │ 标签        │ 改动文件（状态/路径/+N −N） │ 逐行 diff        │
 *   ├────────────┴──────────────────────────────────────────────┤
 *   │ 状态栏：分支 ↑1↓2 · 3 处改动 · E:\pp\quickdock             │
 *   └───────────────────────────────────────────────────────────┘
 *
 * 四个「为什么这样做」：
 *   1. 提交列表要有表头。没有表头时作者名与时间挤在摘要后面，用户得靠猜哪一列是什么。
 *   2. 详情是左右分栏而不是上下堆叠：文件清单与 diff 是「索引 → 内容」的关系，
 *      并排才能一边点文件一边看内容；上下堆叠时点一个文件得往下滚才能看到结果。
 *   3. 中部可拖动：看历史时想多看提交，看 diff 时想多看代码，比例只能由用户定。
 *   4. 不画分支图：git 的 ASCII 图形里纯连线的 / \ 行没有 hash，解析时被丢弃，
 *      剩下的几根竖线既表达不了分叉，又在双行行距里碎成一截一截。
 *      分支归属交给行内 refs 标签和左侧分支树，比一排断线更准。
 *
 * 写操作的入口分两类：常驻按钮只放高频安全动作（Fetch / Pull / Push / 提交），
 * 其余（丢弃、忽略、停止跟踪、打标签、回滚、分支增删改）走右键——低频且会改磁盘的操作
 * 挂在行上，既不占地方，也让「对谁操作」这件事没有歧义。
 *
 * 仍然不做的部分（做半套比没有更危险）：
 *   - 交互式 rebase / reset --hard / 行级暂存：需要用户看着完整上下文在终端里做；
 *   - 冲突解决界面：冲突时只如实说明怎么继续（解决后提交）或怎么退出（git merge --abort）。
 */
import { ref, computed, reactive, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import type { ApiResult } from '../../bindings/quickdock/services/models'
import { getErrorMessage } from '../utils/error'
import { removeStored, writeStored } from '../utils/storage'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { useGitClone } from '../composables/useGitClone'
import { navigateTo, pendingTarget } from '../composables/bridge'
import ConfirmDialog from './ConfirmDialog.vue'
import {
  GitListRepos, GitAddRepo, GitRemoveRepo, GitPickRepo,
  GitRepoStatus, GitBranches, GitLog, GitCommitDetail,
  GitCheckout, GitFetch, GitPull, GitPush,
  GitWorkingTree, GitWorkingDiff, GitStage, GitUnstage, GitCommit, GitCommitAmend, GitCommitMessageAI,
  GitDiscardChanges, GitAddIgnore, GitUntrack,
  GitCreateBranch, GitDeleteBranch, GitRenameBranch, GitMerge,
  GitCreateTag, GitRevertCommit, GitCheckoutCommit,
  GitPushBranch, GitPushTag, GitDeleteTag,
  GitCherryPick, GitArchive, GitFileHistory, GitFileBlame, GitShowFile,
  GitSearchLog, GitRestoreFile,
  GitStashList, GitStashSave, GitStashApply, GitStashPop, GitStashDrop, GitStashShow,
  GitSSHStatus, GitSSHGenerate, GitSSHTest, GitRemoteInfo, GitRemoteToSSH,
  GitRemotes, GitRemoteSet, GitRemoteRemove,
  GitCompare, GitSubmodules, GitSubmoduleUpdate, GitSubmoduleSync, GitSubmoduleAdd,
} from '../../bindings/quickdock/services/git/gitservice'
import { EnvGitStatus } from '../../bindings/quickdock/services/env/environmentservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { SelectSavePath } from '../../bindings/quickdock/services/appservice'
import {
  FolderGit2, Plus, RefreshCw, Download, ArrowDownToLine, Upload, GitBranch,
  Users, X, Loader2, Tag, ChevronDown, ChevronRight, FileText, Check, CloudDownload,
  FileDiff, Sparkles, Archive, KeyRound, History, Copy, Trash2, Eye, Pencil, Minus,
  Search, AlertTriangle, Link2, PanelLeft, List, ListTree, Folder, Columns2, Rows3, Boxes, MoreHorizontal,
  ArrowUp, ArrowDown,
} from '@lucide/vue'

import { reportError } from '../utils/reportError'
// 返回值里的结构由后端 JSON 直接给出，Wails 绑定生成器不会为它们单独生成类型
// （与 MonitorPage 的 MonitorLog 同一处理），故在此本地声明，字段与 json tag 一致。
interface RepoRef { path: string; name: string; addedAt: string }
interface FileStat { status: string; path: string; desc: string; add: number; del: number }
interface RepoStatus {
  path: string; branch: string; upstream: string; ahead: number; behind: number
  changes: FileStat[] | null; clean: boolean; noCommits: boolean
  gitMissing: boolean; error: string
}
interface BranchInfo { name: string; current: boolean; remote: boolean; tag?: boolean; ahead: number; behind: number }
interface BranchSet { local: BranchInfo[]; remote: BranchInfo[]; tags: BranchInfo[] }
interface Commit {
  hash: string; parents?: string[]; short: string; author: string; email: string
  ts: number; subject: string; refs: string; graph: string
  /** 是否在当前分支（HEAD）上，后端标好带回；「当前分支」视图靠它筛 */
  onHead?: boolean
}
interface CommitDetail { hash: string; files: FileStat[] | null; diff: string }
interface WorkFile { status: string; path: string; orig?: string; desc: string; add: number; del: number }
interface WorkingTree {
  branch: string; upstream: string; ahead: number; behind: number; noCommits: boolean
  staged: WorkFile[] | null; unstaged: WorkFile[] | null
}
interface StashEntry { ref: string; index: number; branch: string; message: string; auto: boolean; ts: number }
interface BlameLine { line: number; hash: string; short: string; author: string; ts: number; text: string }
interface BlameResult { file: string; lines: BlameLine[] | null; truncated: boolean }
interface ShowFileResult { rev: string; file: string; content: string; binary: boolean; truncated: boolean }
interface SSHKeyInfo {
  name: string; type: string; comment: string; fingerprint: string; path: string; publicKey: string
}
interface SSHStatus {
  dir: string; keys: SSHKeyInfo[] | null; configPath: string
  hosts: string[]; hostKey: Record<string, string>
}
interface RemoteInfo { name: string; url: string; isSsh: boolean }
interface RemoteEntry { name: string; url: string; pushUrl: string; isSsh: boolean }
interface CompareResult {
  base: string; head: string; ahead: number; behind: number
  headOnly: Commit[] | null; baseOnly: Commit[] | null
  files: FileStat[] | null; diff: string
}
interface Submodule { path: string; sha: string; state: string; describe: string }

const { t } = useI18n()
const toast = useToast()

const repos = ref<RepoRef[]>([])
const activePath = ref('')
const status = ref<RepoStatus | null>(null)
const branches = ref<BranchSet>({ local: [], remote: [], tags: [] })
const commits = ref<Commit[]>([])
const selectedHash = ref('')
const detail = ref<CommitDetail | null>(null)
const selectedStash = ref<StashEntry | null>(null)
// 详情区当前展示的来源：工作区（worktreeMode）/ 提交 / 贮藏。
// 单独标志，避免刷新把贮藏当提交清掉，也让右侧模板能识别「非提交详情」。
const detailKind = ref<'none' | 'commit' | 'stash'>('none')
// 提交详情 / 贮藏预览的请求失败态：没有它时 detail=null 会让左侧永远转圈，
// 右侧却说「请选择一个提交」，两处自相矛盾。失败后显示「加载失败 + 重试」。
const detailFailed = ref(false)

const loading = ref(false)
const busy = ref('')      // 正在跑的网络/切换动作（按钮转圈用）
const checkoutName = ref('') // 正在切换的分支名：只让被点的那一行转圈
const error = ref('')
const selectedFile = ref('')  // '' = 全部文件

// ---- 工作区（待提交）----
// worktreeMode：详情区当前展示「工作区改动」还是某次提交。有改动时默认落在这里，
// 因为打开面板最想看的通常是「我改了什么、还没提交」，而不是上次提交的细节。
const worktreeMode = ref(true)
const worktree = ref<WorkingTree | null>(null)
const worktreeDiff = ref('')
const commitMsg = ref('')
const aiWriting = ref(false)
const committing = ref(false)
// amend：把暂存区改动并入上一次提交（信息留空 = 保留原信息）
const amendMode = ref(false)

// 左侧分组折叠状态。本地分支默认展开（用得最多），远程分支与标签默收起：
// 一个仓库动辄几十个远程分支和上百个标签，全展开会把「仓库 / 本地分支」挤到看不见。
const open = reactive({ repos: true, local: true, remote: false, tags: false, stash: true })

// ---- 贮藏 ----
const stashes = ref<StashEntry[]>([])

// ---- 文件历史模式：把提交列表临时切成「这个文件的历史」----
const fileHist = ref<{ path: string; rev: string } | null>(null)
const fileCommits = ref<Commit[]>([])

// ---- 逐行归属（blame）与历史版本内容：都是独立浮层，用完即走 ----
const blameState = ref<{ file: string; rev: string; loading: boolean; data: BlameResult | null; err: string } | null>(null)
const fileView = ref<{ rev: string; file: string; loading: boolean; data: ShowFileResult | null; err: string } | null>(null)

// ---- SSH 密钥 ----
const sshOpen = ref(false)
const sshStatus = ref<SSHStatus | null>(null)
const sshRemote = ref<RemoteInfo | null>(null)
const sshHost = ref('github.com')
const sshComment = ref('')
const sshPass = ref('')
const sshBusy = ref('')
const sshOut = ref('')
const sshTestOk = ref(false)
const sshGenerated = ref<SSHKeyInfo | null>(null)

const activeRepo = computed(() => repos.value.find(r => r.path === activePath.value) || null)
const changeCount = computed(() => status.value?.changes?.length || 0)
const stagedFiles = computed(() => worktree.value?.staged || [])
const unstagedFiles = computed(() => worktree.value?.unstaged || [])
const canCommit = computed(() => {
  if (committing.value) return false
  // amend 时信息可空（保留原信息），只要有暂存内容就能并；只改信息（无暂存）也允许
  if (amendMode.value) return stagedFiles.value.length > 0 || !!commitMsg.value.trim()
  return !!commitMsg.value.trim() && stagedFiles.value.length > 0
})

// ---- 最近打开的仓库 ----
// 记在 localStorage 而不是后端 setting：它只是「这台机器上这个面板上次停在哪」，
// 不参与同步也不参与备份；换个数据目录重建时跟着新库重新记就行。
const LAST_REPO_KEY = 'qd.git.activeRepo'
function recallRepo(): string {
  try { return localStorage.getItem(LAST_REPO_KEY) || '' } catch { return '' }
}
function rememberRepo(p: string) {
  try {
    if (p) localStorage.setItem(LAST_REPO_KEY, p)
    else localStorage.removeItem(LAST_REPO_KEY)
  } catch { /* 隐私模式/配额满：记不住就算了，不影响主流程 */ }
}

// ---- Git 环境 ----
// 仓库清单是空的、或者 git 根本不在机器上，面板里所有数据都会是空的。
// 这时最该做的事是把「去环境管理装 git」摆在眼前，而不是让用户对着空列表猜。
const gitEnv = ref<{ installed: boolean; version: string; path: string } | null>(null)
const envChecking = ref(false)
const gitMissing = computed(() =>
  gitEnv.value ? !gitEnv.value.installed : !!status.value?.gitMissing)

// ---- 提交列表筛选：本地内存过滤；回车则升级为「全历史搜索」（问 git 本身）----
const histQuery = ref('')
const searchResults = ref<Commit[] | null>(null) // 非空 = 处于全历史搜索结果模式
const searching = ref(false)

// ---- 历史范围：当前分支 / 全部 ----
// 后端一次 --all 拿回混排的提交，每条都带 onHead 标记；切换只做内存筛选，
// 不回后端、不让列表闪一下。默认「当前分支」——git 自己的 log 就是这个视角，
// 想看别的分支的活才切「全部」。
const SCOPE_KEY = 'quickdock.git.logScope'
function recallScope(): 'current' | 'all' {
  try { return localStorage.getItem(SCOPE_KEY) === 'all' ? 'all' : 'current' } catch { return 'current' }
}
const logScope = ref<'current' | 'all'>(recallScope())
function setLogScope(v: 'current' | 'all') {
  if (logScope.value === v) return
  logScope.value = v
  writeStored(SCOPE_KEY, v) // 记不住就算了，本次切换仍然生效
}
/** 后端一条都没标（detached HEAD / 空仓库 / 老进程）时不筛，否则列表会空成一片。 */
const headMarked = computed(() => commits.value.some(c => c.onHead))
/** 范围过滤后的基准集合。搜索结果、文件历史本身已是子集，不再二次筛。 */
const scopedCommits = computed(() => {
  const base = baseCommits.value
  if (logScope.value !== 'current' || fileHist.value || searchResults.value) return base
  if (!headMarked.value) return base
  return base.filter(c => c.onHead)
})

// ---- 远端管理 ----
const remoteOpen = ref(false)
const remotes = ref<RemoteEntry[]>([])
const remoteBusy = ref('')
const newRemote = reactive({ name: '', url: '' })

// ---- 左栏收起（看长 diff 时把横向空间让出来） ----
const sideOpen = ref(true)

// ---- 工具栏 ⋯ 菜单：远端 / 子模块 / SSH 三个低频入口 ----
const moreOpen = ref(false)
function closeMore() { moreOpen.value = false }
watch(moreOpen, (v) => {
  if (v) document.addEventListener('click', closeMore)
  else document.removeEventListener('click', closeMore)
})

// ---- 多选：Ctrl 点逐个加减、Shift 点选一段，批量暂存 / 丢弃 ----
// 键必须带组前缀：同一个文件可能同时出现在「已暂存」和「未暂存」两组（部分暂存），
// 只按 path 存会让勾一组连带勾上另一组。
const picked = ref<Set<string>>(new Set())
const pickAnchor = ref('')
function pickKey(f: WorkFile, kind: 'staged' | 'unstaged') {
  return (kind === 'staged' ? 's:' : 'u:') + f.path
}
const pickedStaged = computed(() => stagedFiles.value.filter(f => picked.value.has(pickKey(f, 'staged'))))
const pickedUnstaged = computed(() => unstagedFiles.value.filter(f => picked.value.has(pickKey(f, 'unstaged'))))
const pickedCount = computed(() => pickedStaged.value.length + pickedUnstaged.value.length)

// ---- 文件列表视图：树形（按目录分组，默认）/ 平铺 ----
const treeMode = ref(true)
const closedDirs = ref<Set<string>>(new Set())
// 提交详情里文件列可收起：并排 diff 需要横向空间时把这一列让出来
const filesCollapsed = ref(false)

// ---- diff 视图：统一（默认）/ 左右并排 ----
const diffSide = ref(false)

// ---- 分支对比 ----
const compareState = ref<{
  base: string; head: string; loading: boolean; data: CompareResult | null; err: string
} | null>(null)

// ---- 子模块 ----
const subOpen = ref(false)
const subs = ref<Submodule[]>([])
const subBusy = ref('')
const newSub = reactive({ url: '', path: '' })

/** 提交列表当前展示哪一份：搜索结果 > 文件历史 > 全量历史（三者互斥，搜索优先级最高）。 */
const baseCommits = computed(() => {
  if (searchResults.value) return searchResults.value
  return fileHist.value ? fileCommits.value : commits.value
})

/** 提交框左下角那句话：普通提交说暂存数量，amend 说清「留着原信息还是只改信息」。 */
const commitHint = computed(() => {
  if (amendMode.value) {
    if (!stagedFiles.value.length && commitMsg.value.trim()) return t('git_amend_msg_only')
    if (!commitMsg.value.trim()) return t('git_amend_keep_msg')
  }
  return stagedFiles.value.length ? t('git_commit_hint', { n: stagedFiles.value.length }) : t('git_commit_need_staged')
})

/** 按钮为什么按不了：灰按钮不说话时，用户只能猜是「没选文件」还是「没写信息」。 */
const commitBlockReason = computed(() => {
  if (committing.value) return ''
  if (amendMode.value) {
    return stagedFiles.value.length || commitMsg.value.trim() ? '' : t('git_amend_need_any')
  }
  if (!stagedFiles.value.length) return t('git_commit_need_staged')
  if (!commitMsg.value.trim()) return t('git_commit_need_msg')
  return ''
})

/** 筛选只在内存里做：历史上限 200 条，再问一次 git 不会更准，还会让列表闪。 */
const shownCommits = computed(() => {
  const base = scopedCommits.value
  const q = histQuery.value.trim().toLowerCase()
  if (!q) return base
  return base.filter(c =>
    c.subject.toLowerCase().includes(q) ||
    c.author.toLowerCase().includes(q) ||
    c.hash.toLowerCase().startsWith(q) ||
    (c.refs || '').toLowerCase().includes(q))
})

/* ---- 提交图 ----
 * 不用 git 的 ASCII 图：`--graph` 里纯连线的 "/" "\" 行没有 hash，解析时被丢弃，
 * 渲染出来只剩几根互不相连的竖线。这里改用每次提交的父 hash 自己排 lane：
 * 每条 lane 记一个「下一个期待的提交」，走一遍就得到节点位置与连线。
 *
 * 只在完整历史上画图：筛选、全历史搜索、单文件历史拿到的都是子集，父提交大多不在
 * 列表里，硬画只会是一排悬空的线头，所以这几种状态直接收起图形列。
 */
const showGraph = computed(() => !fileHist.value && !histQuery.value.trim() && !searchResults.value)

/** 「当前分支」视图只连第一父：merge 进来的那条分支的提交都被筛掉了，
   照旧往右开 lane 只会留下一排悬空到列表底部的线头。 */
const firstParentOnly = computed(() => logScope.value === 'current' && !fileHist.value && !searchResults.value)

const LANE_W = 12            // 单条 lane 宽（含线间距）
const ROW_H = 100            // SVG 纵向用 0-100 归一化，实际高度随行高走
const CY = ROW_H / 2
const LANE_COLORS = 6
const laneX = (l: number) => l * LANE_W + LANE_W / 2

interface GraphPath { d: string; color: number }
interface GraphRow {
  lane: number
  color: number
  merge: boolean
  x: number
  paths: GraphPath[]
}

/** 自上而下一遍扫完：每行拿到节点所在 lane、颜色，以及要画的线段。
 *  竖线用 V 一次到底，合并/汇入用三次贝塞尔，圆点最后画上去盖住穿过的线。
 *  配色按 lane 序号固定：lane 空出来再被新分支占用时，颜色不会跟着变——
 *  否则同一列会「上半截紫、下半截蓝」，看着像线断了。 */
const graphData = computed<{ rows: GraphRow[]; width: number }>(() => {
  const lanes: (string | null)[] = []   // lane → 它正在等的提交
  const colorOf = (k: number) => k % LANE_COLORS
  let maxLanes = 0
  let prevActive: number[] = []
  const rows: GraphRow[] = []

  const alloc = () => {
    const free = lanes.indexOf(null)
    if (free >= 0) return free
    lanes.push(null)
    return lanes.length - 1
  }

  for (const c of shownCommits.value) {
    let li = lanes.indexOf(c.hash)
    if (li < 0) li = alloc()
    const color = colorOf(li)
    const x = laneX(li)
    const paths: GraphPath[] = []

    // 同一提交被多条 lane 同时等待（合并后重复出现）：其余 lane 在本行并回节点
    const converge: number[] = []
    for (let k = li + 1; k < lanes.length; k++) {
      if (lanes[k] === c.hash) { converge.push(k); lanes[k] = null }
    }

    // 从上一行下来的线：节点 lane 画到中点，其余贯穿整行
    for (const k of prevActive) {
      if (k === li || converge.includes(k)) continue
      paths.push({ d: `M${laneX(k)} 0V${ROW_H}`, color: colorOf(k) })
    }
    if (prevActive.includes(li)) paths.push({ d: `M${x} 0V${CY}`, color })
    for (const k of converge) {
      const kx = laneX(k)
      paths.push({ d: `M${kx} 0C${kx} ${CY - 8} ${x} ${CY - 10} ${x} ${CY}`, color: colorOf(k) })
    }

    // 节点向下：第一个父沿用本 lane；父已被别的 lane 等着就弯过去；其余父各开一条
    const parents = c.parents || []
    let down = false
    const p0 = parents[0]
    if (p0) {
      const dup = lanes.findIndex((h, k) => h === p0 && k !== li)
      if (dup >= 0) {
        const dx = laneX(dup)
        paths.push({ d: `M${x} ${CY}C${x} ${CY + 10} ${dx} ${CY + 12} ${dx} ${ROW_H}`, color: colorOf(dup) })
        lanes[li] = null
      } else {
        lanes[li] = p0
        down = true
      }
    } else {
      lanes[li] = null
    }
    for (const p of firstParentOnly.value ? [] : parents.slice(1)) {
      let j = lanes.indexOf(p)
      if (j < 0) { j = alloc(); lanes[j] = p }
      const jx = laneX(j)
      paths.push({ d: `M${x} ${CY}C${x} ${CY + 10} ${jx} ${CY + 12} ${jx} ${ROW_H}`, color: colorOf(j) })
    }
    if (down) paths.push({ d: `M${x} ${CY}V${ROW_H}`, color })

    rows.push({ lane: li, color, merge: parents.length > 1, x, paths })

    // 行末收缩尾部空 lane，图宽才不会越滚越大
    while (lanes.length && lanes[lanes.length - 1] === null) lanes.pop()
    maxLanes = Math.max(maxLanes, lanes.length, li + 1, converge.length ? converge[converge.length - 1] + 1 : 0)
    prevActive = lanes.map((h, k) => (h !== null ? k : -1)).filter(k => k >= 0)
  }
  return { rows, width: maxLanes ? maxLanes * LANE_W + 4 : 0 }
})

const graphRows = computed(() => graphData.value.rows)
const graphWidth = computed(() => (showGraph.value ? graphData.value.width : 0))
/** lane 配色走 CSS 变量，深浅主题各一套（见 .git-main 上的 --git-lane-*）。 */
const laneVar = (c: number) => `var(--git-lane-${(c % LANE_COLORS) + 1})`

// ---- 加载 ----

/** 总览卡片带来的深链仓库：仓库清单是异步拉的，先记下，清单回来后由 loadRepos 落定。 */
let deepRepo = ''
watch(() => pendingTarget.value, (p) => {
  if (!p || p.page !== 'git' || !p.target) return
  deepRepo = p.target
  // 清单已经在了（页面重进但仓库列表命中缓存）就不用等 loadRepos
  if (repos.value.some(r => r.path === deepRepo)) {
    activePath.value = deepRepo
    deepRepo = ''
  }
  pendingTarget.value = null // 取走即清空，别让下次正常进来又跳一次
}, { immediate: true })

async function loadRepos() {
  try {
    repos.value = (unwrap(await GitListRepos()) as RepoRef[] | null) || []
    // 深链目标优先于一切：用户是点着某个仓库进来的，不能拿「上次那个」把它顶掉
    if (deepRepo) {
      const hit = repos.value.some(r => r.path === deepRepo)
      const target = deepRepo
      deepRepo = '' // 只生效一次：之后的刷新该回到常规的「记住上次打开的那个」
      if (hit) {
        activePath.value = target
        return
      }
    }
    // 选中项仍然有效就不动它（刷新仓库清单不该把用户踢回第一个）
    if (activePath.value && repos.value.some(r => r.path === activePath.value)) return
    // 否则依次退回：上次打开的那个 → 清单第一个
    const saved = recallRepo()
    if (saved && repos.value.some(r => r.path === saved)) {
      activePath.value = saved
      return
    }
    activePath.value = repos.value[0]?.path || ''
  } catch (e) {
    error.value = getErrorMessage(e)
    logErr('git.listRepos', e)
  }
}

// 选中哪个仓库就记哪个：跨会话（关掉应用再开）也回到上次那个仓库。
watch(activePath, p => rememberRepo(p))

/** 进页面先确认本机有没有 git：没有的话整个面板都会是空的，必须给出安装入口。 */
async function checkGitEnv() {
  envChecking.value = true
  try {
    const s = unwrap<{ installed: boolean; version: string; path: string }>(await EnvGitStatus())
    gitEnv.value = { installed: !!s?.installed, version: s?.version || '', path: s?.path || '' }
  } catch (e) {
    // 探测失败不等于没装：此时不弹横幅（不制造假警报），让仓库自己的报错说话
    logErr('git.envCheck', e)
    gitEnv.value = null
  } finally {
    envChecking.value = false
  }
}

function goEnvInstall() {
  // 落到 git 区块本身，而不是环境管理的默认选中项。
  navigateTo('environments', 'git')
}

// ---- 远端管理 ----

async function loadRemotes() {
  try {
    remotes.value = (unwrap(await GitRemotes(activePath.value)) as RemoteEntry[] | null) || []
  } catch (e) {
    reportError(e, 'git.remotes')
  }
}

async function openRemotes() {
  if (!activePath.value) return
  newRemote.name = remotes.value.length ? '' : 'origin'
  newRemote.url = ''
  remoteOpen.value = true
  await loadRemotes()
}

async function saveRemote(r: RemoteEntry) {
  if (remoteBusy.value) return
  remoteBusy.value = r.name
  try {
    const res = unwrap<RemoteEntry[]>(await GitRemoteSet(activePath.value, r.name, r.url))
    remotes.value = res || []
    toast.success(t('git_remote_saved'))
    await loadRepo(true)
  } catch (e) {
    reportError(e, 'GitPage.saveRemote')
    await loadRemotes() // 改失败时把输入框里的脏值刷回真实值
    logErr('git.remoteSet', e)
  } finally {
    remoteBusy.value = ''
  }
}

async function addRemote() {
  const name = newRemote.name.trim()
  const url = newRemote.url.trim()
  if (!name || !url) { toast.error(t('git_remote_need')); return }
  if (remoteBusy.value) return
  remoteBusy.value = 'new'
  try {
    const res = unwrap<RemoteEntry[]>(await GitRemoteSet(activePath.value, name, url))
    remotes.value = res || []
    newRemote.name = ''
    newRemote.url = ''
    toast.success(t('git_remote_added'))
    await loadRepo(true)
  } catch (e) {
    reportError(e, 'git.remoteAdd')
  } finally {
    remoteBusy.value = ''
  }
}

async function removeRemote(r: RemoteEntry) {
  askConfirm(t('git_remote_remove_q', { name: r.name }), () => {
    void (async () => {
      remoteBusy.value = r.name
      try {
        const res = unwrap<RemoteEntry[]>(await GitRemoteRemove(activePath.value, r.name))
        remotes.value = res || []
        toast.success(t('git_remote_removed'))
        await loadRepo(true)
      } catch (e) {
        reportError(e, 'git.remoteRemove')
      } finally {
        remoteBusy.value = ''
      }
    })()
  }, [t('git_remote_remove_hint')])
}

/** 一个仓库的全部视图数据：status / branches / log / 工作区 / 贮藏一起取，避免半新半旧。 */
async function loadRepo(silent = false) {
  const path = activePath.value
  if (!path) {
    status.value = null; commits.value = []; detail.value = null
    worktree.value = null; worktreeDiff.value = ''; stashes.value = []
    return
  }
  if (!silent) {
    loading.value = true
    // 工具栏的「刷新」走非静默路径：清掉全历史搜索态，别让新拉的仓库配着上一次的搜索结果
    searchResults.value = null
    histQuery.value = ''
  }
  error.value = ''
  try {
    const [st, br, lg, tree, wd, sl] = await Promise.all([
      GitRepoStatus(path),
      GitBranches(path),
      GitLog(path, 200),
      GitWorkingTree(path),
      GitWorkingDiff(path),
      GitStashList(path),
    ])
    // 期间切了仓库：这批数据已经不属于当前页面。没有这道判断时，
    // 慢的那次请求回来会盖掉新仓库的数据——表现就是「切到 A 却看到 B 的改动数」。
    if (path !== activePath.value) return
    status.value = unwrap<RepoStatus>(st) || null
    branches.value = (unwrap<BranchSet>(br) || { local: [], remote: [], tags: [] })
    commits.value = (unwrap<Commit[]>(lg) as Commit[] | null) || []
    worktree.value = unwrap<WorkingTree>(tree) || null
    worktreeDiff.value = (unwrap<{ diff: string }>(wd) || { diff: '' }).diff || ''
    stashes.value = (unwrap<StashEntry[]>(sl) as StashEntry[] | null) || []
    if (status.value?.gitMissing) error.value = status.value.error
    // 刷新后选中项可能已不在列表里（切了分支/被 prune），清掉避免详情张冠李戴
    if (selectedHash.value && !commits.value.some(c => c.hash === selectedHash.value)) {
      selectedHash.value = ''
      detail.value = null
    }
    // 有改动就默认停在工作区（只在整页加载时这么定，静默刷新别抢用户当前的选择）
    if (!silent && changeCount.value > 0) worktreeMode.value = true
    if (fileHist.value) await loadFileCommits()
  } catch (e) {
    if (path !== activePath.value) return
    error.value = getErrorMessage(e)
    logErr('git.loadRepo', e)
  } finally {
    // 旧仓库的请求回来时不能把新仓库的「加载中」关掉（数据有 path 校验，loading 也得有）
    if (path === activePath.value) loading.value = false
  }
}

/** 切仓库前先把上一份视图清干净：异步数据回来之前，界面不能还挂着上个仓库的改动数。 */
function resetRepoView() {
  status.value = null
  branches.value = { local: [], remote: [], tags: [] }
  commits.value = []
  fileCommits.value = []
  stashes.value = []
  worktree.value = null
  worktreeDiff.value = ''
  detail.value = null
  detailFailed.value = false
  selectedHash.value = ''
  selectedStash.value = null
  detailKind.value = 'none'
  selectedFile.value = ''
  fileHist.value = null
  searchResults.value = null
}

async function addRepo() {
  try {
    const picked = unwrap<string>(await GitPickRepo())
    if (!picked) return // 用户取消了目录选择
    // 后端会把这个目录归一化到**仓库根**：选的是子目录时，用它返回的清单找回真正的根，
    // 否则侧栏里那条仓库高亮不上，看起来像「加了却没加进去」。
    const list = (unwrap<RepoRef[]>(await GitAddRepo(picked)) as RepoRef[] | null) || []
    await loadRepos()
    const lower = picked.toLowerCase()
    const target = list.find(r => lower === r.path.toLowerCase() || lower.startsWith(r.path.toLowerCase())) || list[list.length - 1]
    if (target) {
      resetRepoView()
      activePath.value = target.path
    }
    await loadRepo()
    toast.success(t('git_added'))
  } catch (e) {
    reportError(e, 'git.addRepo')
  }
}

// ---- 克隆远端仓库 ----
// 对话框自己的状态与请求在 useGitClone；克隆成功后的收尾要动 activePath 与视图状态，
// 所以由本页注入回调。
const {
  cloneOpen, cloneUrl, cloneParent, cloneErr, cloning,
  cloneName, cloneTarget,
  openClone, closeClone, pickParent, doClone,
} = useGitClone({
  onCloned: async (target) => {
    await loadRepos()
    if (target) {
      resetRepoView()
      activePath.value = target
      await loadRepo()
    }
  },
})

function removeRepo(r: RepoRef) {
  // 「移出列表」不改磁盘，但小叉极易误点，必须先问一次再动手。
  askConfirm(t('gitRemoveRepoConfirm', { name: r.name }), () => {
    void (async () => {
      try {
        unwrap(await GitRemoveRepo(r.path))
        if (activePath.value === r.path) {
          resetRepoView()
          activePath.value = ''
        }
        await loadRepos()
        if (!activePath.value && repos.value.length) {
          activePath.value = repos.value[0].path
          await loadRepo()
        }
      } catch (e) {
        reportError(e, 'git.removeRepo')
      }
    })()
  })
}

async function switchRepo(r: RepoRef) {
  if (activePath.value === r.path) return
  resetRepoView()
  activePath.value = r.path
  await loadRepo()
}

async function selectCommit(c: Commit) {
  worktreeMode.value = false
  selectedHash.value = c.hash
  selectedStash.value = null
  detailKind.value = 'commit'
  selectedFile.value = ''
  detail.value = null
  detailFailed.value = false
  try {
    const d = unwrap<CommitDetail>(await GitCommitDetail(activePath.value, c.hash, true)) || null
    // 连点两条提交时先发的可能后到：只在仍选中这条时才回填，否则右侧会显示上一条的 diff
    if (selectedHash.value !== c.hash) return
    detail.value = d
    // 在「文件历史」里点提交：用户想看的就是这个文件，顺手选上，省一次点击
    const want = fileHist.value?.path
    if (want && detail.value?.files) {
      const hit = detail.value.files.find(f => blockPath(f) === want)
      if (hit) selectedFile.value = blockPath(hit)
    }
  } catch (e) {
    if (selectedHash.value !== c.hash) return
    detailFailed.value = true
    reportError(e, 'git.commitDetail')
  }
}

/** 失败态的「重试」：按当前详情来源重新拉一次，保留用户选中的提交/贮藏。 */
function retryDetail() {
  if (detailKind.value === 'stash' && selectedStash.value) void selectStash(selectedStash.value)
  else if (detailKind.value === 'commit' && selectedHash.value) {
    const hit = baseCommits.value.find(c => c.hash === selectedHash.value)
      || commits.value.find(c => c.hash === selectedHash.value)
    if (hit) void selectCommit(hit)
  }
}

// ---- 工作区：勾选即暂存 / 取消即撤出 ----

function openWorktree() {
  worktreeMode.value = true
  selectedHash.value = ''
  selectedFile.value = ''
  detailFailed.value = false
  // 回工作区就退出「文件历史」：列表继续挂着某文件的历史、而详情在画工作区，两处对不上
  if (fileHist.value) exitFileHistory()
}

function selectWorkFile(f: WorkFile) {
  const p = f.path
  // 左栏常驻：点工作区文件就把右侧拉回工作区 diff，别让上面选中的提交继续占着
  worktreeMode.value = true
  selectedFile.value = selectedFile.value === p ? '' : p
}

/**
 * 点击文件行：普通点 = 看这个文件的 diff；Ctrl/⌘ 点 = 加减多选；Shift 点 = 从锚点连选一段。
 *
 * 多选是给「批量暂存 / 丢弃」用的：一屏里挑三五个文件勾掉再一起操作，比一个个点
 * checkbox 快得多。锚点必须同组才有意义——已暂存与未暂存是两份列表，行号不可比。
 */
function onFileClick(e: MouseEvent, f: WorkFile, kind: 'staged' | 'unstaged') {
  const k = pickKey(f, kind)
  const list = kind === 'staged' ? stagedFiles.value : unstagedFiles.value
  if (e.shiftKey && pickAnchor.value && pickAnchor.value.startsWith(kind === 'staged' ? 's:' : 'u:')) {
    const i = list.findIndex(x => pickKey(x, kind) === pickAnchor.value)
    const j = list.findIndex(x => x.path === f.path)
    if (i >= 0 && j >= 0) {
      const [a, b] = i <= j ? [i, j] : [j, i]
      const next = new Set(picked.value)
      for (let n = a; n <= b; n++) next.add(pickKey(list[n], kind))
      picked.value = next
      selectWorkFile(f)
      return
    }
  }
  if (e.ctrlKey || e.metaKey) {
    const next = new Set(picked.value)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    picked.value = next
    pickAnchor.value = k
    selectWorkFile(f)
    return
  }
  picked.value = new Set()
  pickAnchor.value = k
  selectWorkFile(f)
}

function clearPicked() {
  picked.value = new Set()
  pickAnchor.value = ''
}

function stagePicked() {
  const files = pickedUnstaged.value.map(f => f.path)
  if (!files.length || busy.value) return
  void runGitOp(() => GitStage(activePath.value, files))
}

function unstagePicked() {
  const files = pickedStaged.value.map(f => f.path)
  if (!files.length || busy.value) return
  void runGitOp(() => GitUnstage(activePath.value, files))
}

/** 批量丢弃的确认文案与单项不同：这里是「N 个文件」，且不区分未跟踪/已修改，
 *  统一按最坏情况说明（未跟踪会删掉、已跟踪回退到上次提交）。 */
function confirmDiscardPicked() {
  const files = [...pickedStaged.value, ...pickedUnstaged.value].map(f => f.path)
  if (!files.length) return
  askConfirm(
    t('git_discard_picked_confirm', { n: files.length }),
    () => void runGitOp(() => GitDiscardChanges(activePath.value, files, true)),
    [t('git_ctx_discard_detail_mod')],
  )
}

/** 勾上 = 暂存，取消勾 = 撤销暂存。两个方向都只发一条命令再整体刷新，
 *  不在前端猜结果——git 的索引状态（权限位、重命名、冲突）比界面显示的复杂。 */
async function toggleStage(f: WorkFile, isStaged: boolean) {
  if (!activePath.value || busy.value) return
  busy.value = 'stage'
  try {
    if (isStaged) unwrap(await GitUnstage(activePath.value, [f.path]))
    else unwrap(await GitStage(activePath.value, [f.path]))
    await loadRepo(true)
  } catch (e) {
    reportError(e, 'git.toggleStage')
  } finally {
    busy.value = ''
  }
}

async function stageAll() {
  if (!activePath.value || busy.value) return
  busy.value = 'stage'
  try {
    unwrap(await GitStage(activePath.value, []))
    await loadRepo(true)
  } catch (e) {
    reportError(e, 'git.stageAll')
  } finally {
    busy.value = ''
  }
}

async function unstageAll() {
  if (!activePath.value || busy.value) return
  busy.value = 'stage'
  try {
    unwrap(await GitUnstage(activePath.value, []))
    await loadRepo(true)
  } catch (e) {
    reportError(e, 'git.unstageAll')
  } finally {
    busy.value = ''
  }
}

/** 全部丢弃：把未暂存的整批改动退回上次提交。比逐条勾选再丢弃快，
 *  但也是这一页最危险的一步，所以走同一个确认弹窗，不省这一步。 */
function confirmDiscardAll() {
  const files = unstagedFiles.value.map(f => f.path)
  if (!files.length || busy.value) return
  askConfirm(
    t('git_discard_all_confirm', { n: files.length }),
    () => void runGitOp(() => GitDiscardChanges(activePath.value, files, true)),
    [t('git_ctx_discard_detail_mod')],
  )
}

// ---- 拖拽暂存：未暂存行拖到「已暂存」组头 = add，反向拖 = reset ----
//
// 只在两组之间搬，不接受从资源管理器拖文件进来（那是「新增文件」，语义不同，
// 且 WebView2 拿不到真实路径）。组头做投放区而不是整组：整组范围太大，
// 拖到一半就触发，反而容易误操作。
const dragFrom = ref<'' | 'staged' | 'unstaged'>('')
const dragPath = ref('')
const dropOn = ref<'' | 'staged' | 'unstaged'>('')

function onFileDragStart(e: DragEvent, f: WorkFile, from: 'staged' | 'unstaged') {
  dragFrom.value = from
  dragPath.value = f.path
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    // 必须写一份数据，否则部分环境下 dragstart 直接被浏览器取消
    e.dataTransfer.setData('text/plain', f.path)
  }
}

function onFileDragEnd() {
  dragFrom.value = ''
  dragPath.value = ''
  dropOn.value = ''
}

function onGroupDragOver(e: DragEvent, group: 'staged' | 'unstaged') {
  if (!dragFrom.value || dragFrom.value === group || !activePath.value || busy.value) return
  e.preventDefault() // 不 preventDefault 就不会触发 drop
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  dropOn.value = group
}

/** 组头里还有文字和按钮，鼠标在子元素间移动会不停触发 dragleave：
 *  只有真正离开组头本身才清掉高亮，否则投放区会一闪一闪。 */
function onGroupDragLeave(e: DragEvent) {
  const el = e.currentTarget as HTMLElement | null
  const to = e.relatedTarget as Node | null
  if (el && to && el.contains(to)) return
  dropOn.value = ''
}

function onGroupDrop(e: DragEvent, group: 'staged' | 'unstaged') {
  e.preventDefault()
  const path = dragPath.value
  const from = dragFrom.value
  onFileDragEnd()
  if (!path || !from || from === group || busy.value) return
  if (group === 'staged') void runGitOp(() => GitStage(activePath.value, [path]))
  else void runGitOp(() => GitUnstage(activePath.value, [path]))
}

// ---- 提交 ----

async function aiCommitMessage() {
  if (!activePath.value || aiWriting.value) return
  aiWriting.value = true
  try {
    const r = unwrap<{ message: string; model: string; scope: string }>(await GitCommitMessageAI(activePath.value))
    if (r?.message) {
      commitMsg.value = r.message
      // 说明这次是照「暂存区」还是「全部改动」写的：用户据此判断要不要先补暂存
      toast.success(t('git_ai_done', { scope: r.scope || '' }))
    }
  } catch (e) {
    reportError(e, 'git.aiMessage')
  } finally {
    aiWriting.value = false
  }
}

function doCommit() {
  if (!canCommit.value) return
  // amend 会改写历史：上次提交已经推到远端（有上游且没有领先）时，
  // 之后要 force push 才能同步——这个代价必须让用户先看见
  if (amendMode.value && status.value?.upstream && !status.value?.ahead) {
    askConfirm(t('git_amend_pushed_confirm'), () => void performCommit(), [t('git_amend_pushed_detail')])
    return
  }
  void performCommit()
}

async function performCommit() {
  committing.value = true
  try {
    if (amendMode.value) {
      const r = unwrap<{ hash: string }>(await GitCommitAmend(activePath.value, commitMsg.value))
      commitMsg.value = ''
      amendMode.value = false
      await loadRepo(true)
      toast.success(t('git_amend_done', { h: r?.hash || '' }))
      // amend 完选中新哈希：改动生效这件事要看得见
      const target = commits.value.find(c => c.hash.startsWith(r?.hash || '\x00'))
      if (target) void selectCommit(target)
    } else {
      const r = unwrap<{ hash: string }>(await GitCommit(activePath.value, commitMsg.value, false))
      commitMsg.value = ''
      await loadRepo(true)
      toast.success(t('git_committed', { h: r?.hash || '' }))
      // 提交完自动回到历史并选中新提交：刚做完的事应该看得见结果
      const target = commits.value.find(c => c.hash.startsWith(r?.hash || '\x00'))
      if (target) void selectCommit(target)
    }
  } catch (e) {
    // hook 失败 / 身份未配置 的原因都在 msg 里，原样给用户（后端已透出 stderr）
    reportError(e, 'git.commit')
  } finally {
    committing.value = false
  }
}

async function doPush() {
  if (!activePath.value || busy.value) return
  const startedAt = performance.now()
  busy.value = 'push'
  try {
    unwrap(await GitPush(activePath.value))
    await holdSpin(startedAt)
    toast.success(t('git_pushed'))
    await loadRepo(true)
  } catch (e) {
    await holdSpin(startedAt)
    reportError(e, 'git.push')
  } finally {
    busy.value = ''
  }
}

// ---- 动作 ----

async function doCheckout(b: BranchInfo) {
  if (b.current || b.tag || busy.value) return
  const name = b.remote ? b.name.replace(/^[^/]+\//, '') : b.name
  busy.value = 'checkout'
  checkoutName.value = name
  try {
    unwrap(await GitCheckout(activePath.value, name))
    toast.success(t('git_checked_out', { b: name }))
    selectedHash.value = ''
    selectedFile.value = ''
    detail.value = null
    await loadRepo(true)
  } catch (e) {
    // 有未提交改动时 git 会拒绝并说明原因，原样透出比藏起来有用
    reportError(e, 'git.checkout')
  } finally {
    busy.value = ''
    checkoutName.value = ''
  }
}

/** 哪一行在切分支：本地行用全名，远程行去掉 origin/ 前缀后再比。 */
function isCheckingOut(b: BranchInfo): boolean {
  if (!checkoutName.value) return false
  const name = b.remote ? b.name.replace(/^[^/]+\//, '') : b.name
  return name === checkoutName.value
}

// 网络动作的最短可见时长。本地仓库 fetch 往往 80ms 就回来，转圈一闪而过
// 等于没有反馈——用户会以为「点了没反应」，于是再点一次。
const MIN_SPIN_MS = 450

/** 补足转圈时间：操作先完成，再按需等剩下的毫秒数。 */
function holdSpin(startedAt: number): Promise<void> {
  const left = MIN_SPIN_MS - (performance.now() - startedAt)
  return left > 0 ? new Promise((r) => setTimeout(r, left)) : Promise.resolve()
}

async function doFetch() {
  if (busy.value) return
  const startedAt = performance.now()
  busy.value = 'fetch'
  try {
    unwrap(await GitFetch(activePath.value))
    await holdSpin(startedAt)
    toast.success(t('git_fetched'))
    await loadRepo(true)
  } catch (e) {
    await holdSpin(startedAt)
    reportError(e, 'git.fetch')
  } finally {
    busy.value = ''
  }
}

async function doPull() {
  if (busy.value) return
  const startedAt = performance.now()
  busy.value = 'pull'
  try {
    unwrap(await GitPull(activePath.value))
    await holdSpin(startedAt)
    toast.success(t('git_pulled'))
    selectedHash.value = ''
    selectedFile.value = ''
    detail.value = null
    await loadRepo(true)
  } catch (e) {
    await holdSpin(startedAt)
    reportError(e, 'git.pull')
  } finally {
    busy.value = ''
  }
}

// ---- 右键菜单：文件 / 提交 / 分支 ----
//
// 为什么这些放右键而不是常驻按钮：丢弃、忽略、停止跟踪都是低频且会改磁盘的动作，
// 每行常驻 2~3 个图标会把路径本身挤没；右键既省空间，也符合这类工具的肌肉记忆。
// 危险项（丢弃 / 删除分支 / 回滚 / 检出提交）一律先确认，文案写明「不可恢复」的边界。

type CtxKind = 'staged' | 'unstaged' | 'cfile' | 'commit' | 'branch' | 'tag' | 'stash'

interface CtxState {
  x: number
  y: number
  kind: CtxKind
  file?: WorkFile
  cfile?: FileStat
  commit?: Commit
  branch?: BranchInfo
  tag?: BranchInfo
  stash?: StashEntry
}

interface MenuAction {
  id: string
  label: string
  danger?: boolean
  disabled?: boolean
  /** 分隔线：只占一行高度，不需要 label */
  sep?: boolean
  run: () => void
}

const ctx = ref<CtxState | null>(null)
const ctxMenuEl = ref<HTMLElement | null>(null)

/** 定位菜单并把超出窗口的部分收回来（fixed 定位不会自己滚动到可见区）。 */
function placeCtxAt(x: number, y: number, rest: Omit<CtxState, 'x' | 'y'>) {
  ctx.value = { x, y, ...rest }
  nextTick(() => {
    const c = ctx.value
    const el = ctxMenuEl.value
    if (!c || !el) return
    const r = el.getBoundingClientRect()
    c.x = Math.max(8, Math.min(c.x, window.innerWidth - r.width - 8))
    c.y = Math.max(8, Math.min(c.y, window.innerHeight - r.height - 8))
  })
}
function placeCtx(e: MouseEvent, rest: Omit<CtxState, 'x' | 'y'>) {
  placeCtxAt(e.clientX, e.clientY, rest)
}
/** Shift+F10 / Menu 键没有鼠标坐标：把菜单定位到当前行的左下角。v-menu 会把焦点还给这一行。 */
function ctxFrom(e: MouseEvent | KeyboardEvent, rest: Omit<CtxState, 'x' | 'y'>) {
  const me = e as MouseEvent
  if (typeof me.clientX === 'number' && (me.clientX !== 0 || me.clientY !== 0)) {
    placeCtx(me, rest)
    return
  }
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
  placeCtxAt(r.left + 8, r.bottom, rest)
}

function openFileCtx(e: MouseEvent | KeyboardEvent, f: WorkFile, kind: 'staged' | 'unstaged') {
  selectedFile.value = f.path // 右键即选中：菜单操作的对象必须是「看到的这一行」
  ctxFrom(e, { kind, file: f })
}

function openCommitCtx(e: MouseEvent | KeyboardEvent, c: Commit) {
  // 右键也切到这次提交的详情：接下来不管是打标签还是回滚，都要先看清是哪一次
  if (selectedHash.value !== c.hash) void selectCommit(c)
  ctxFrom(e, { kind: 'commit', commit: c })
}

function openBranchCtx(e: MouseEvent | KeyboardEvent, b: BranchInfo) {
  // 右键不切分支：右键是「准备操作」，顺手切走会让后面的操作落到意料之外的分支上
  ctxFrom(e, { kind: 'branch', branch: b })
}

/** 标签单独一类：它能做的事（推送/删除本地/删除远端）跟分支不一样，共用一个菜单只会互相干扰。 */
function openTagCtx(e: MouseEvent | KeyboardEvent, g: BranchInfo) {
  ctxFrom(e, { kind: 'tag', tag: g })
}

/** 提交详情里的文件行：历史 / blame / 「这次提交里它是什么样」都从这里进。 */
function openCommitFileCtx(e: MouseEvent | KeyboardEvent, f: FileStat) {
  selectedFile.value = blockPath(f)
  ctxFrom(e, { kind: 'cfile', cfile: f })
}

function closeCtx() {
  ctx.value = null
}

// ---- 确认框 / 输入框（右键菜单里的危险操作与命名操作） ----

const confirmState = ref<{ message: string; details?: string[]; run: () => void } | null>(null)

function askConfirm(message: string, run: () => void, details?: string[]) {
  confirmState.value = { message, details, run }
}

function onConfirmYes() {
  // 先关窗再执行：失败提示是 toast，被遮罩压住就等于没提示
  const s = confirmState.value
  confirmState.value = null
  s?.run()
}

const promptState = ref<{
  title: string
  label: string
  text: string
  /** 允许留空（贮藏说明这类「不填也有默认值」的场景） */
  optional?: boolean
  run: (v: string) => void
} | null>(null)

const promptInput = ref<HTMLInputElement | null>(null)

function askText(title: string, label: string, text: string, run: (v: string) => void, opts?: { optional?: boolean }) {
  promptState.value = { title, label, text, optional: opts?.optional, run }
  // 打开即聚焦并全选：重命名场景要直接替换旧名，新建场景直接开打
  nextTick(() => {
    promptInput.value?.focus()
    promptInput.value?.select()
  })
}

function onPromptOk() {
  const s = promptState.value
  if (!s) return
  const v = s.text.trim()
  if (!v && !s.optional) {
    toast.error(t('git_name_required'))
    return
  }
  promptState.value = null
  s.run(v)
}

/** 一次写操作的统一外壳：失败 toast 原因、成功 toast 后端提示语并整仓刷新。
 *  统一刷新而不是局部改状态——索引 / 分支 / HEAD 都可能被这一步改掉，前端猜不准。 */
async function runGitOp(fn: () => Promise<ApiResult | null>) {
  try {
    const r = await fn()
    unwrap(r)
    // 操作后清单会变（文件换组/消失），留着选中集只会让下一次批量落到错的文件上
    clearPicked()
    await loadRepo(true)
    // 后端的 msg 常带补充说明（如「已被跟踪的文件需先停止跟踪」），优先用它
    if (r?.msg) toast.success(r.msg)
    return true
  } catch (e) {
    reportError(e, 'git.op')
    return false
  }
}

// ---- 文件级动作 ----

function confirmDiscard(f: WorkFile, staged: boolean) {
  const untracked = f.status === '??'
  askConfirm(
    t('git_ctx_discard_confirm', { p: f.path }),
    () => void runGitOp(() => GitDiscardChanges(activePath.value, [f.path], staged)),
    [untracked ? t('git_ctx_discard_detail_new') : t('git_ctx_discard_detail_mod')],
  )
}

function ignoreFile(f: WorkFile, mode: 'file' | 'dir' | 'ext') {
  void runGitOp(() => GitAddIgnore(activePath.value, [f.path], mode))
}

function confirmUntrack(f: WorkFile) {
  askConfirm(
    t('git_ctx_untrack_confirm', { p: f.path }),
    () => void runGitOp(() => GitUntrack(activePath.value, [f.path])),
    [t('git_ctx_untrack_detail')],
  )
}

/** 相对路径 → 绝对路径。仓库根用的分隔符由 activePath 决定（Windows 下是反斜杠）。 */
function absPath(rel: string): string {
  const sep = activePath.value.includes('\\') ? '\\' : '/'
  return activePath.value.replace(/[\\/]+$/, '') + sep + rel.replace(/\//g, sep)
}

async function revealFile(f: WorkFile) {
  try {
    unwrap(await RevealInExplorer(absPath(f.path)))
  } catch (e) {
    reportError(e, 'git.reveal')
  }
}

async function copyText(s: string) {
  if (await copyToClipboard(s)) {
    toast.success(t('copied'))
  } else {
    toast.error(t('copyFailed'))
  }
}

function extOf(p: string): string {
  const base = p.split('/').pop() || ''
  const i = base.lastIndexOf('.')
  return i > 0 ? base.slice(i) : ''
}

// ---- 贮藏 ----

function promptStash() {
  // 留空等于用 git 自动生成的 WIP 文案，所以这个输入框允许空提交
  askText(t('git_stash_save'), t('git_stash_msg_label'), '', (msg) => {
    void runGitOp(() => GitStashSave(activePath.value, msg, true))
  }, { optional: true })
}

function stashApply(s: StashEntry) {
  void runGitOp(() => GitStashApply(activePath.value, s.ref))
}

async function stashPop(s: StashEntry) {
  const ok = await runGitOp(() => GitStashPop(activePath.value, s.ref))
  // 弹出会删除记录：当前预览的就是它，清掉避免右侧继续显示已不存在的贮藏
  if (ok && selectedStash.value?.ref === s.ref) {
    selectedStash.value = null
    detailKind.value = 'none'
    detail.value = null
  }
}

function confirmStashDrop(s: StashEntry) {
  askConfirm(
    t('git_stash_drop_confirm', { r: s.ref }),
    async () => {
      const ok = await runGitOp(() => GitStashDrop(activePath.value, s.ref))
      if (ok && selectedStash.value?.ref === s.ref) {
        selectedStash.value = null
        detailKind.value = 'none'
        detail.value = null
      }
    },
    [t('git_stash_drop_detail')],
  )
}

// 点击贮藏条目：右侧预览它的改动（之前点了没反应）。复用提交详情那套渲染——
// 后端 GitStashShow 返回与 CommitDetail 同构的 files + diff，文件列表与 diff 面板都不用重写。
async function selectStash(s: StashEntry) {
  worktreeMode.value = false
  selectedHash.value = ''
  selectedStash.value = s
  detailKind.value = 'stash'
  selectedFile.value = ''
  detail.value = null
  detailFailed.value = false
  try {
    const d = unwrap<CommitDetail>(await GitStashShow(activePath.value, s.ref)) || null
    // 同 selectCommit：以「当前仍选中这条贮藏」为准，迟到的旧响应直接丢弃
    if (selectedStash.value?.ref !== s.ref) return
    detail.value = d
  } catch (e) {
    if (selectedStash.value?.ref !== s.ref) return
    detailFailed.value = true
    reportError(e, 'git.stashShow')
  }
}

function openStashCtx(e: MouseEvent | KeyboardEvent, s: StashEntry) {
  ctxFrom(e, { kind: 'stash', stash: s })
}

// ---- 文件历史 / 逐行归属 / 历史版本 ----

/** 进入「这个文件的历史」：提交列表切成 fileCommits，横幅上标明是谁的历史、随时可退出。 */
async function enterFileHistory(file: string, rev: string) {
  fileHist.value = { path: file, rev }
  fileCommits.value = []
  searchResults.value = null // 文件历史与搜索结果互斥：进历史就收搜索
  selectedFile.value = file
  await loadFileCommits()
}

async function loadFileCommits() {
  const f = fileHist.value
  if (!f || !activePath.value) { fileCommits.value = []; return }
  try {
    fileCommits.value = (unwrap<Commit[]>(await GitFileHistory(activePath.value, f.path, f.rev, 100)) as Commit[] | null) || []
  } catch (e) {
    reportError(e, 'git.fileHistory')
  }
}

function exitFileHistory() {
  fileHist.value = null
  fileCommits.value = []
}

// ---- 全历史搜索：本地筛选只覆盖已载入的 200 条，回车升格为问 git 本身 ----

async function doLogSearch() {
  const q = histQuery.value.trim()
  if (!activePath.value || searching.value) return
  if (q.length < 2) { toast.error(t('gitSearchMinChars')); return }
  searching.value = true
  try {
    searchResults.value = (unwrap<Commit[]>(await GitSearchLog(activePath.value, q, 200)) as Commit[] | null) || []
  } catch (e) {
    reportError(e, 'git.searchLog')
  } finally {
    searching.value = false
  }
}

/** 输入框一动就退出搜索结果模式：显示的必须和框里的词对得上。 */
function onHistInput() {
  if (searchResults.value) searchResults.value = null
}

function clearHistFilter() {
  histQuery.value = ''
  searchResults.value = null
}

// ---- blame 行跳转 / 历史版本恢复 ----

/** blame 里点哈希：跳到那次提交（列表里有就选中，没有也把详情拉出来）。 */
function jumpToCommit(hash: string) {
  if (!hash) return
  blameState.value = null
  const hit = baseCommits.value.find(c => c.hash === hash) || commits.value.find(c => c.hash === hash)
  const c: Commit = hit || { hash, short: hash.slice(0, 8), author: '', email: '', ts: 0, subject: '', refs: '', graph: '' }
  void selectCommit(c)
}

function confirmRestoreVersion() {
  const v = fileView.value
  if (!v?.data) return
  askConfirm(
    t('git_restore_confirm', { r: (v.rev || '').slice(0, 8), f: v.file }),
    () => void restoreFileVersion(),
    [t('git_restore_detail')],
  )
}

async function restoreFileVersion() {
  const v = fileView.value
  if (!v) return
  try {
    unwrap(await GitRestoreFile(activePath.value, v.rev, v.file))
    fileView.value = null
    await loadRepo(true)
    toast.success(t('git_restored', { f: v.file }))
  } catch (e) {
    reportError(e, 'git.restoreFile')
  }
}

async function openBlame(file: string, rev: string) {
  const cur = { file, rev, loading: true, data: null as BlameResult | null, err: '' }
  blameState.value = cur
  try {
    const r = unwrap<BlameResult>(await GitFileBlame(activePath.value, file, rev)) || null
    // 同一时刻只可能有这一个浮层：用户关掉再开新的，旧请求回来不该覆盖
    if (blameState.value === cur) blameState.value = { file, rev, loading: false, data: r, err: '' }
  } catch (e) {
    if (blameState.value === cur) blameState.value = { file, rev, loading: false, data: null, err: getErrorMessage(e) }
    logErr('git.blame', e)
  }
}

async function openFileAtRev(file: string, rev: string) {
  const cur = { rev, file, loading: true, data: null as ShowFileResult | null, err: '' }
  fileView.value = cur
  try {
    const r = unwrap<ShowFileResult>(await GitShowFile(activePath.value, rev, file)) || null
    if (fileView.value === cur) fileView.value = { rev, file, loading: false, data: r, err: '' }
  } catch (e) {
    if (fileView.value === cur) fileView.value = { rev, file, loading: false, data: null, err: getErrorMessage(e) }
    logErr('git.showFile', e)
  }
}

// ---- 摘取 / 存档 ----

function confirmCherryPick(c: Commit) {
  askConfirm(
    t('git_ctx_cherry_confirm', { h: c.short, b: status.value?.branch || '—' }),
    () => void runGitOp(() => GitCherryPick(activePath.value, c.hash)),
    [t('git_ctx_cherry_detail')],
  )
}

/** 导出某个提交的快照 zip：另存为对话框选位置，后端用 git archive 打包（不含 .git）。 */
async function archiveAt(rev: string) {
  const base = activeRepo.value?.name || 'repo'
  const short = rev ? rev.slice(0, 7) : 'HEAD'
  try {
    const dest = unwrap<string>(await SelectSavePath(t('git_archive_title'), `${base}-${short}.zip`))
    if (!dest) return
    const r = await GitArchive(activePath.value, rev, dest)
    unwrap(r)
    toast.success(r?.msg || t('git_archive_done'))
  } catch (e) {
    reportError(e, 'git.archive')
  }
}

// ---- 标签 / 分支的远端动作 ----

function pushTag(g: BranchInfo) {
  void runGitOp(() => GitPushTag(activePath.value, g.name))
}

function confirmDeleteTag(g: BranchInfo, remote: boolean) {
  if (remote) {
    askConfirm(
      t('git_ctx_delete_tag_remote_confirm', { b: g.name }),
      () => void runGitOp(() => GitDeleteTag(activePath.value, g.name, true)),
      [t('git_ctx_delete_tag_remote_detail')],
    )
    return
  }
  askConfirm(
    t('git_ctx_delete_tag_confirm', { b: g.name }),
    () => void runGitOp(() => GitDeleteTag(activePath.value, g.name, false)),
    [t('git_ctx_delete_tag_detail')],
  )
}

function pushBranch(b: BranchInfo) {
  void runGitOp(() => GitPushBranch(activePath.value, b.name))
}

// ---- 分支对比：以「别的分支」为基准，看当前分支多了什么、少了什么 ----

async function openCompare(b: BranchInfo) {
  const head = status.value?.branch || 'HEAD'
  const cur = { base: b.name, head, loading: true, data: null as CompareResult | null, err: '' }
  compareState.value = cur
  try {
    const r = unwrap<CompareResult>(await GitCompare(activePath.value, b.name, head)) || null
    if (compareState.value === cur) compareState.value = { base: b.name, head, loading: false, data: r, err: '' }
  } catch (e) {
    if (compareState.value === cur) {
      compareState.value = { base: b.name, head, loading: false, data: null, err: getErrorMessage(e) }
    }
    logErr('git.compare', e)
  }
}

// ---- 子模块 ----

async function openSubmodules() {
  subOpen.value = true
  await loadSubmodules()
}

async function loadSubmodules() {
  if (!activePath.value) return
  try {
    subs.value = (unwrap<Submodule[]>(await GitSubmodules(activePath.value)) as Submodule[] | null) || []
  } catch (e) {
    reportError(e, 'git.submodules')
  }
}

async function subUpdate(s: Submodule, remote: boolean) {
  if (subBusy.value || !activePath.value) return
  subBusy.value = remote ? 'remote:' + s.path : s.path
  try {
    const r = await GitSubmoduleUpdate(activePath.value, s.path, remote)
    unwrap(r)
    await loadSubmodules()
    await loadRepo(true)
    if (r?.msg) toast.success(r.msg)
  } catch (e) {
    reportError(e, 'git.subUpdate')
  } finally {
    subBusy.value = ''
  }
}

async function subSync() {
  if (subBusy.value || !activePath.value) return
  subBusy.value = 'sync'
  try {
    const r = await GitSubmoduleSync(activePath.value)
    unwrap(r)
    if (r?.msg) toast.success(r.msg)
  } catch (e) {
    reportError(e, 'git.subSync')
  } finally {
    subBusy.value = ''
  }
}

/** 子模块状态首字符 → 文案/颜色。'-' 未初始化、'+' 与父仓库记录的提交不一致。 */
function subStateText(state: string): string {
  if (state === '-') return t('git_sub_state_init')
  if (state === '+') return t('git_sub_state_diff')
  if (state === 'U') return t('git_sub_state_conflict')
  return t('git_sub_state_ok')
}

function subStateCls(state: string): string {
  if (state === '-') return 'init'
  if (state === '+') return 'diff'
  if (state === 'U') return 'conflict'
  return 'ok'
}

async function subAdd() {
  if (subBusy.value || !activePath.value) return
  const url = newSub.url.trim()
  const path = newSub.path.trim()
  if (!url || !path) {
    toast.error(t('git_sub_add_required'))
    return
  }
  subBusy.value = 'add'
  try {
    const r = await GitSubmoduleAdd(activePath.value, url, path)
    unwrap(r)
    newSub.url = ''
    newSub.path = ''
    await loadSubmodules()
    await loadRepo(true)
    if (r?.msg) toast.success(r.msg)
  } catch (e) {
    reportError(e, 'git.subAdd')
  } finally {
    subBusy.value = ''
  }
}

// ---- SSH 密钥 ----

async function openSSH() {
  sshOpen.value = true
  sshGenerated.value = null
  sshOut.value = ''
  sshTestOk.value = false
  sshPass.value = ''
  sshComment.value = ''
  sshBusy.value = 'load'
  try {
    sshStatus.value = unwrap<SSHStatus>(await GitSSHStatus()) || null
    sshRemote.value = activePath.value
      ? (unwrap<RemoteInfo>(await GitRemoteInfo(activePath.value)) || null)
      : null
    // 已经配过的 host 优先作为默认值：用户点进来通常就是想复用/测试那把 key
    const hosts = sshStatus.value?.hosts || []
    if (!hosts.includes(sshHost.value)) {
      const known = ['github.com', 'gitlab.com', 'gitee.com'].find(h => hosts.includes(h))
      if (known) sshHost.value = known
    }
  } catch (e) {
    reportError(e, 'git.sshStatus')
  } finally {
    sshBusy.value = ''
  }
}

function closeSSH() {
  if (sshBusy.value === 'gen') return // 生成中途关掉会留下半把密钥
  sshOpen.value = false
}

async function doSSHGenerate() {
  if (sshBusy.value) return
  sshBusy.value = 'gen'
  sshOut.value = ''
  try {
    const r = unwrap<{ key: SSHKeyInfo; configPath: string; reused: boolean }>(
      await GitSSHGenerate(sshHost.value, sshComment.value, sshPass.value),
    )
    if (r?.key) {
      sshGenerated.value = r.key
      // 顺手把公钥放到剪贴板：下一步就是去网页上粘贴，少一次点击
      if (!(await copyToClipboard(r.key.publicKey))) {
        // 自动复制失败要告诉用户，否则下一步直接去网页粘贴会贴出旧内容。
        toast.error(t('copyFailed'))
      }
    }
    sshStatus.value = unwrap<SSHStatus>(await GitSSHStatus()) || sshStatus.value
    toast.success(t('git_ssh_gen_done'))
  } catch (e) {
    reportError(e, 'git.sshGenerate')
  } finally {
    sshBusy.value = ''
  }
}

async function doSSHTest() {
  if (sshBusy.value) return
  sshBusy.value = 'test'
  sshOut.value = t('git_ssh_testing')
  try {
    const r = unwrap<{ ok: boolean; output: string }>(await GitSSHTest(sshHost.value))
    sshTestOk.value = !!r?.ok
    sshOut.value = r?.output || ''
  } catch (e) {
    sshTestOk.value = false
    sshOut.value = getErrorMessage(e)
    logErr('git.sshTest', e)
  } finally {
    sshBusy.value = ''
  }
}

async function remoteToSSH() {
  if (sshBusy.value) return
  sshBusy.value = 'remote'
  try {
    const r = await GitRemoteToSSH(activePath.value)
    unwrap(r)
    if (r?.msg) toast.success(r.msg)
    sshRemote.value = unwrap<RemoteInfo>(await GitRemoteInfo(activePath.value)) || null
  } catch (e) {
    reportError(e, 'git.remoteToSSH')
  } finally {
    sshBusy.value = ''
  }
}

// ---- 提交级动作 ----

function promptBranchAt(c: Commit) {
  askText(t('git_ctx_branch_here'), t('git_ctx_new_branch_name'), '', (name) => {
    void runGitOp(() => GitCreateBranch(activePath.value, name, c.hash))
  })
}

function promptTagAt(c: Commit) {
  askText(t('git_ctx_tag_here'), t('git_ctx_tag_name'), '', (name) => {
    void runGitOp(() => GitCreateTag(activePath.value, name, c.hash))
  })
}

function confirmRevert(c: Commit) {
  askConfirm(
    t('git_ctx_revert_confirm', { h: c.short }),
    () => void runGitOp(() => GitRevertCommit(activePath.value, c.hash)),
    [t('git_ctx_revert_detail')],
  )
}

function confirmCheckoutCommit(c: Commit) {
  askConfirm(
    t('git_ctx_checkout_commit_confirm', { h: c.short }),
    () => void runGitOp(() => GitCheckoutCommit(activePath.value, c.hash)),
    [t('git_ctx_checkout_detail')],
  )
}

// ---- 分支级动作 ----

function promptBranchFrom(b: BranchInfo) {
  const from = b.name
  askText(t('git_ctx_branch_here'), t('git_ctx_new_branch_name'), '', (name) => {
    void runGitOp(() => GitCreateBranch(activePath.value, name, from))
  })
}

function promptRenameBranch(b: BranchInfo) {
  askText(t('git_ctx_rename_branch'), t('git_ctx_new_branch_name'), b.name, (name) => {
    if (name === b.name) return
    void runGitOp(() => GitRenameBranch(activePath.value, b.name, name))
  })
}

/** 删除分支分两步：-d 被拒说明有未合并的提交，再问一次才用 -D，避免顺手丢代码。 */
function confirmDeleteBranch(b: BranchInfo) {
  askConfirm(t('git_ctx_delete_confirm', { b: b.name }), async () => {
    try {
      unwrap(await GitDeleteBranch(activePath.value, b.name, false))
      await loadRepo(true)
      toast.success(t('git_ctx_branch_deleted', { b: b.name }))
    } catch (e) {
      const msg = getErrorMessage(e)
      if (msg.includes('未合并')) {
        askConfirm(
          t('git_ctx_force_delete_confirm', { b: b.name }),
          () => void runGitOp(() => GitDeleteBranch(activePath.value, b.name, true)),
          [t('git_ctx_force_delete_detail')],
        )
        return
      }
      toast.error(msg)
      logErr('git.deleteBranch', e)
    }
  })
}

function confirmMerge(b: BranchInfo) {
  askConfirm(
    t('git_ctx_merge_confirm', { b: b.name }),
    () => void runGitOp(() => GitMerge(activePath.value, b.name)),
    [t('git_ctx_merge_detail')],
  )
}

/** 菜单项按「当前右键对象」生成：模板里只画列表，不再写一堆 v-if。 */
const ctxActions = computed<MenuAction[]>(() => {
  const c = ctx.value
  if (!c) return []
  const sep = (id: string): MenuAction => ({ id, label: '', sep: true, run: () => {} })

  if (c.kind === 'commit' && c.commit) {
    const cm = c.commit
    return [
      { id: 'co', label: t('git_ctx_checkout_commit'), run: () => confirmCheckoutCommit(cm) },
      { id: 'br', label: t('git_ctx_branch_here'), run: () => promptBranchAt(cm) },
      { id: 'tag', label: t('git_ctx_tag_here'), run: () => promptTagAt(cm) },
      sep('s1'),
      { id: 'pick', label: t('git_ctx_cherry_pick'), run: () => confirmCherryPick(cm) },
      { id: 'zip', label: t('git_ctx_archive'), run: () => void archiveAt(cm.hash) },
      sep('s2'),
      { id: 'revert', label: t('git_ctx_revert'), danger: true, run: () => confirmRevert(cm) },
      sep('s3'),
      { id: 'hash', label: t('git_ctx_copy_hash'), run: () => void copyText(cm.hash) },
      { id: 'subj', label: t('git_ctx_copy_subject'), run: () => void copyText(cm.subject) },
    ]
  }

  // 标签：推送 / 删本地 / 删远端。删除分两条，是因为「删远端」影响所有协作者。
  if (c.kind === 'tag' && c.tag) {
    const g = c.tag
    return [
      { id: 'push', label: t('git_ctx_push_tag'), run: () => pushTag(g) },
      sep('s1'),
      { id: 'del', label: t('git_ctx_delete_tag'), danger: true, run: () => confirmDeleteTag(g, false) },
      { id: 'delr', label: t('git_ctx_delete_tag_remote'), danger: true, run: () => confirmDeleteTag(g, true) },
      sep('s2'),
      { id: 'copy', label: t('git_ctx_copy_name'), run: () => void copyText(g.name) },
    ]
  }

  // 贮藏：弹出（应用并删除）是最常用的一条，放第一个
  if (c.kind === 'stash' && c.stash) {
    const s = c.stash
    return [
      { id: 'pop', label: t('git_stash_pop'), run: () => stashPop(s) },
      { id: 'apply', label: t('git_stash_apply'), run: () => stashApply(s) },
      sep('s1'),
      { id: 'drop', label: t('git_stash_drop'), danger: true, run: () => confirmStashDrop(s) },
      sep('s2'),
      { id: 'copy', label: t('git_stash_copy'), run: () => void copyText(`${s.ref} ${s.message}`) },
    ]
  }

  if (c.kind === 'branch' && c.branch) {
    const b = c.branch
    const acts: MenuAction[] = []
    if (!b.current) acts.push({ id: 'co', label: t('git_ctx_checkout'), run: () => void doCheckout(b) })
    acts.push({ id: 'br', label: t('git_ctx_branch_here'), run: () => promptBranchFrom(b) })
    // 推送只对本地分支有意义：远程分支已经在远端了，标签走标签自己的菜单
    if (!b.remote && !b.tag) {
      acts.push({ id: 'push', label: t('git_ctx_push_branch'), run: () => pushBranch(b) })
    }
    if (!b.remote && !b.tag && !b.current) {
      acts.push({ id: 'ren', label: t('git_ctx_rename_branch'), run: () => promptRenameBranch(b) })
      acts.push({ id: 'del', label: t('git_ctx_delete_branch'), danger: true, run: () => confirmDeleteBranch(b) })
    }
    // 「合并到当前」只对「别的分支」有意义：右键当前分支自己，git 会直接回
    // "Already up to date"，列出来只是让人以为点了会做什么。
    if (!b.tag && !b.current) {
      acts.push(sep('s1'))
      acts.push({ id: 'cmp', label: t('git_ctx_compare'), run: () => void openCompare(b) })
      acts.push({ id: 'merge', label: t('git_ctx_merge'), run: () => confirmMerge(b) })
    }
    acts.push(sep('s2'))
    acts.push({ id: 'copy', label: t('git_ctx_copy_name'), run: () => void copyText(b.name) })
    return acts
  }

  // 提交详情里的文件行
  if (c.kind === 'cfile' && c.cfile) {
    const f = c.cfile
    const p = blockPath(f)
    const rev = selectedHash.value
    return [
      { id: 'hist', label: t('git_ctx_file_history'), run: () => void enterFileHistory(p, rev) },
      { id: 'blame', label: t('git_ctx_blame'), run: () => void openBlame(p, rev) },
      { id: 'view', label: t('git_ctx_view_version'), run: () => void openFileAtRev(p, rev) },
      sep('s1'),
      { id: 'copy', label: t('git_ctx_copy_path'), run: () => void copyText(f.path) },
    ]
  }

  const f = c.file
  if (!f) return []

  // 未跟踪文件在 HEAD 里没有对应路径：历史与 blame 都查不到东西，直接禁掉比点开报错好
  const untracked = f.status === '??'
  const histActs: MenuAction[] = [
    { id: 'hist', label: t('git_ctx_file_history'), disabled: untracked, run: () => void enterFileHistory(f.path, '') },
    { id: 'blame', label: t('git_ctx_blame'), disabled: untracked, run: () => void openBlame(f.path, '') },
  ]

  if (c.kind === 'staged') {
    return [
      { id: 'unstage', label: t('git_unstage'), run: () => void toggleStage(f, true) },
      sep('s1'),
      { id: 'discard', label: t('git_ctx_discard'), danger: true, run: () => confirmDiscard(f, true) },
      { id: 'untrack', label: t('git_ctx_untrack'), run: () => confirmUntrack(f) },
      sep('s2'),
      ...histActs,
      sep('s3'),
      { id: 'reveal', label: t('git_ctx_reveal'), run: () => void revealFile(f) },
      { id: 'copy', label: t('git_ctx_copy_path'), run: () => void copyText(f.path) },
    ]
  }

  const acts: MenuAction[] = [
    { id: 'stage', label: t('git_stage'), run: () => void toggleStage(f, false) },
    sep('s1'),
    { id: 'discard', label: t('git_ctx_discard'), danger: true, run: () => confirmDiscard(f, false) },
  ]
  // 未跟踪文件本来就不在索引里，「停止跟踪」对它没有意义
  if (!untracked) {
    acts.push({ id: 'untrack', label: t('git_ctx_untrack'), run: () => confirmUntrack(f) })
  }
  acts.push(sep('s2'))
  acts.push(...histActs)
  acts.push(sep('s3'))
  acts.push({ id: 'if', label: t('git_ctx_ignore_file'), run: () => ignoreFile(f, 'file') })
  acts.push({ id: 'id', label: t('git_ctx_ignore_dir'), run: () => ignoreFile(f, 'dir') })
  const ext = extOf(f.path)
  acts.push({
    id: 'ie',
    label: ext ? t('git_ctx_ignore_ext', { ext }) : t('git_ctx_ignore_ext_none'),
    disabled: !ext,
    run: () => ignoreFile(f, 'ext'),
  })
  acts.push(sep('s4'))
  acts.push({ id: 'reveal', label: t('git_ctx_reveal'), run: () => void revealFile(f) })
  acts.push({ id: 'copy', label: t('git_ctx_copy_path'), run: () => void copyText(f.path) })
  return acts
})

// ---- 分栏拖动 ----

/** 提交区占比（%）。
 *
 *  46 而不是「一半」：下半区要装文件列表 + diff + 提交面板，提交面板固定占 ~110px，
 *  历史给太多的话文件列表就只剩标题行。46% 在 900 高的窗口里约 350px 历史（12 行），
 *  下半区仍有 300px 给 diff。
 */
const listPct = ref(46)
const mainEl = ref<HTMLElement | null>(null)

function startSplit(e: MouseEvent) {
  e.preventDefault()
  const startY = e.clientY
  const startPct = listPct.value
  const h = mainEl.value?.getBoundingClientRect().height || 600
  const move = (ev: MouseEvent) => {
    const pct = startPct + ((ev.clientY - startY) / h) * 100
    // 22% / 78% 是「两边都还看得见一行」的下限
    listPct.value = Math.min(78, Math.max(22, pct))
  }
  const up = () => {
    window.removeEventListener('mousemove', move)
    window.removeEventListener('mouseup', up)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  }
  window.addEventListener('mousemove', move)
  window.addEventListener('mouseup', up)
  document.body.style.cursor = 'row-resize'
  document.body.style.userSelect = 'none'
}

// ---- 工作区两组的高度分配 ----
/** 默认比例：已暂存 62%。暂存区是"马上要提交的那几条"，看得全才敢按提交；
 *  未暂存那边多是刚改完还没整理的，往下翻一翻很正常。 */
const WT_PCT_DEFAULT = 62
/** 上半区「已暂存」占的百分比；下半区吃剩下的。 */
const wtPct = ref(WT_PCT_DEFAULT)
const wtPanesEl = ref<HTMLElement | null>(null)

/** 拖两组的中间那条：按位移换算成百分比，容器高度在 mousedown 时量一次就够了。 */
function startWtSplit(e: MouseEvent) {
  e.preventDefault()
  const h = wtPanesEl.value?.getBoundingClientRect().height || 0
  if (h <= 0) return
  const startY = e.clientY
  const startPct = wtPct.value
  const move = (ev: MouseEvent) => {
    const pct = startPct + ((ev.clientY - startY) / h) * 100
    // 22% / 78%：两边都还留得住一条组头 + 至少一行文件
    wtPct.value = Math.min(78, Math.max(22, pct))
  }
  const up = () => {
    window.removeEventListener('mousemove', move)
    window.removeEventListener('mouseup', up)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  }
  window.addEventListener('mousemove', move)
  window.addEventListener('mouseup', up)
  document.body.style.cursor = 'row-resize'
  document.body.style.userSelect = 'none'
}

// ---- 列宽拖动（仓库栏 / 工作区栏 / 文件清单）----
// 宽度记在 localStorage：它只是「这台机器上这个面板多宽」，不是仓库数据，没必要进后端。
type ColKey = 'side' | 'wt' | 'files'

const COL_MIN = 140
const COL_MAX = 600
/** 弹性列（提交列表 / diff）的可读下限：再窄连摘要、代码行都放不下。 */
const COL_FLEX_MIN = 320
const COL_CONF: Record<ColKey, { key: string; cssVar: string; cap: number }> = {
  side: { key: 'quickdock.git.sideWidth', cssVar: '--side-w', cap: 480 },
  wt: { key: 'quickdock.git.wtWidth', cssVar: '--wt-w', cap: 520 },
  files: { key: 'quickdock.git.filesWidth', cssVar: '--files-w', cap: 560 },
}

/** 0 表示「没拖过」：此时不出 inline 变量，宽度交给样式表与窄屏媒体查询。 */
function readColW(k: ColKey): number {
  try {
    const n = parseInt(localStorage.getItem(COL_CONF[k].key) || '', 10)
    return n >= COL_MIN && n <= COL_MAX ? n : 0
  } catch { return 0 }   // 隐私模式下 localStorage 读写都会抛，用默认值即可
}

const colW = reactive<Record<ColKey, number>>({
  side: readColW('side'), wt: readColW('wt'), files: readColW('files'),
})
const sideEl = ref<HTMLElement | null>(null)
const wtEl = ref<HTMLElement | null>(null)
const filesEl = ref<HTMLElement | null>(null)
const dragCol = ref<'' | ColKey>('')

const colWidthStyle = computed(() => {
  const s: Record<string, string> = {}
  for (const k of Object.keys(colW) as ColKey[]) {
    if (colW[k]) s[COL_CONF[k].cssVar] = colW[k] + 'px'
  }
  return s
})

function colEl(k: ColKey) {
  return k === 'side' ? sideEl.value : k === 'wt' ? wtEl.value : filesEl.value
}

function startColSplit(e: MouseEvent, which: ColKey) {
  const el = colEl(which)
  if (!el) return
  const conf = COL_CONF[which]
  const startX = e.clientX
  const startW = el.getBoundingClientRect().width
  // 这三栏右边都只有一个弹性列（.git-main / .detail-diff），本列加宽多少，它就窄多少。
  // 所以上限 = 起始宽 + 弹性列还能让出的宽度，比按父容器比例估算准得多——
  // 后者没算中间那几列的固定宽度，窗口不宽时会算出「一拖就把主区挤没」的上限。
  const flex = el.parentElement?.lastElementChild as HTMLElement | null
  const flexW = flex ? flex.getBoundingClientRect().width : 480
  const grow = Math.max(0, flexW - COL_FLEX_MIN)
  const max = Math.max(COL_MIN + 40, Math.min(conf.cap, startW + grow))
  dragCol.value = which
  const move = (ev: MouseEvent) => {
    colW[which] = Math.round(Math.min(max, Math.max(COL_MIN, startW + ev.clientX - startX)))
  }
  const up = () => {
    window.removeEventListener('mousemove', move)
    window.removeEventListener('mouseup', up)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    dragCol.value = ''
    writeStored(conf.key, String(colW[which])) // 存不下就算了，本次拖动仍然生效
  }
  window.addEventListener('mousemove', move)
  window.addEventListener('mouseup', up)
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
}

/** 双击分隔条回到样式表的默认宽度（窄屏另有默认值，所以是「清掉自定义」而不是写死 240）。 */
function resetColSplit(which: ColKey) {
  colW[which] = 0
  removeStored(COL_CONF[which].key)
}

// ---- 展示辅助 ----

function fmtTime(ts: number): string {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  const now = Date.now()
  const diffMin = Math.floor((now - ts * 1000) / 60000)
  if (diffMin < 1) return t('git_just_now')
  if (diffMin < 60) return t('git_min_ago', { n: diffMin })
  const sameDay = d.toDateString() === new Date().toDateString()
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  if (sameDay) return hm
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`
}

/** refs 是 git 给的原始装饰串（HEAD -> main, origin/main, tag: v1.0），切成可单独上色的片段。
 *  "HEAD -> " 前缀对用户没有信息量（它只是说「这个指针指着当前分支」），剥掉。 */
function refList(refs: string): string[] {
  if (!refs) return []
  return refs.split(',')
    .map(s => s.trim().replace(/^HEAD -> /, ''))
    .filter(Boolean)
}

/** 三种 ref 三种语气：本地分支实心（最重，它是「你在这」）、远程淡底、标签琥珀。
 *  全都一个样式的话，扫一眼分不清哪些是当前分支。 */
function refKind(r: string): 'branch' | 'remote' | 'tag' {
  if (r.startsWith('tag:')) return 'tag'
  if (r.includes('/')) return 'remote'
  return 'branch'
}

function refText(r: string): string {
  return r.startsWith('tag:') ? r.slice(4).trim() : r
}

/** 一行最多两个徽章：再多就把摘要挤没了，剩下的折成 +N（完整列表在 title 里）。 */
function refMore(refs: string): number {
  return Math.max(0, refList(refs).length - 2)
}


function statusColor(code: string): string {
  if (code === '??') return 'new'
  if (code.includes('U')) return 'conflict'
  if (code.includes('D')) return 'del'
  if (code.includes('A')) return 'add'
  if (code.includes('R') || code.includes('C')) return 'ren'
  return 'mod'
}

interface DiffLine { cls: string; text: string }
interface DiffBlock { path: string; lines: DiffLine[] }

/** 按 `diff --git` 切块：一块 = 一个文件的改动，点文件列表就按路径筛这一块。
 *  只认最外层前缀，不解析 hunk 内容，避免把代码里的 + 当成增行。 */
function parseDiff(text: string): DiffBlock[] {
  if (!text) return []
  const blocks: DiffBlock[] = []
  let cur: DiffBlock | null = null
  for (const raw of text.split('\n')) {
    if (raw.startsWith('diff --git ')) {
      const m = / b\/(.*)$/.exec(raw)
      cur = { path: m ? m[1] : raw.slice(11), lines: [{ cls: 'meta', text: raw + '\n' }] }
      blocks.push(cur)
      continue
    }
    if (!cur) continue
    let cls = 'ctx'
    if (/^(index |--- |\+\+\+ |new file|deleted file|old mode|new mode|similarity |rename |Binary files)/.test(raw)) cls = 'meta'
    else if (raw.startsWith('@@')) cls = 'hunk'
    else if (raw.startsWith('+')) cls = 'add'
    else if (raw.startsWith('-')) cls = 'del'
    cur.lines.push({ cls, text: raw + '\n' })
  }
  // 末尾多切一行空串（文本以 \n 结尾），去掉免得 diff 底部长出一条空行
  const last = blocks[blocks.length - 1]
  if (last && last.lines.length && last.lines[last.lines.length - 1].text === '\n') last.lines.pop()
  return blocks
}

/** 详情区当前展示的 diff：工作区模式是整份工作区改动，否则是选中提交的 diff。
 *  两者都走同一套 parseDiff，点文件按路径筛块的逻辑因此不用写两遍。 */
const activeDiff = computed(() => worktreeMode.value ? worktreeDiff.value : (detail.value?.diff || ''))
const diffBlocks = computed(() => parseDiff(activeDiff.value))

// 详情区是否已选中「提交或贮藏」：右侧文件列表 / diff 面板据此区分「无内容」与「已选中」。
const detailActive = computed(() => detailKind.value === 'commit' || detailKind.value === 'stash')
// diff 标题处的标识：提交显示前 8 位 hash，贮藏显示 stash@{n}。
const detailHash = computed(() => {
  if (detailKind.value === 'stash' && selectedStash.value) return selectedStash.value.ref
  if (selectedHash.value) return selectedHash.value.slice(0, 8)
  return ''
})

/** 单个文件的 block 路径：name-status 的重命名写「旧 → 新」，diff 里只有新路径。 */
function blockPath(f: FileStat): string {
  const i = f.path.indexOf(' → ')
  return i >= 0 ? f.path.slice(i + 3) : f.path
}

const shownBlocks = computed(() => {
  if (!selectedFile.value) return diffBlocks.value
  const hit = diffBlocks.value.filter(b => b.path === selectedFile.value)
  return hit // 找不到（重命名/二进制的路径对不上）就返回空，让面板显示「无文本差异」而不是整份 diff
})

/** diff 行数上限：一次提交改几千行时逐行建 span 会让面板卡住，截断并说明。 */
const MAX_LINES = 3000
const shownLines = computed(() => {
  const all = shownBlocks.value.flatMap(b => b.lines)
  return all.length > MAX_LINES ? all.slice(0, MAX_LINES) : all
})
const truncated = computed(() => shownBlocks.value.flatMap(b => b.lines).length > MAX_LINES)

function selectFile(f: FileStat) {
  const p = blockPath(f)
  selectedFile.value = selectedFile.value === p ? '' : p
}

// ---- 文件树：把扁平路径压成「目录行 + 文件行」----
//
// 为什么不引第三方树组件：这里要的不是可拖拽的真树，只是「同一目录的文件挨在一起」，
// 自己构造一遍行列表比装一个带虚拟滚动 / 拖放的库小两个数量级。
//
// 压缩单链目录（a/b/c 只有一条子目录且没有文件时合成一行）：后端代码动辄
// services/git/xxx.go，不压缩会为每个文件排出一串只有一个子项的空目录。

interface TreeRow<T> {
  key: string
  name: string
  depth: number
  dir: boolean
  file?: T
}

interface DirNode<T> {
  name: string
  path: string
  dirs: Map<string, DirNode<T>>
  files: T[]
}

function buildTree<T extends { path: string }>(files: T[], prefix: string): TreeRow<T>[] {
  const root: DirNode<T> = { name: '', path: '', dirs: new Map(), files: [] }
  for (const f of files) {
    const parts = f.path.split('/')
    parts.pop()
    let cur = root
    let acc = ''
    for (const p of parts) {
      acc = acc ? acc + '/' + p : p
      let n = cur.dirs.get(p)
      if (!n) {
        n = { name: p, path: acc, dirs: new Map(), files: [] }
        cur.dirs.set(p, n)
      }
      cur = n
    }
    cur.files.push(f)
  }

  const rows: TreeRow<T>[] = []
  const walk = (node: DirNode<T>, depth: number) => {
    const dirs = [...node.dirs.values()].sort((a, b) => a.name.localeCompare(b.name))
    for (const d of dirs) {
      // 单链压缩：d 只有一个子目录且自己没有文件 → 显示成 a/b/c 一行
      let label = d.name
      let tail = d
      while (tail.dirs.size === 1 && tail.files.length === 0) {
        const next = [...tail.dirs.values()][0]
        label += '/' + next.name
        tail = next
      }
      rows.push({ key: prefix + 'd:' + tail.path, name: label, depth, dir: true })
      if (!closedDirs.value.has(prefix + 'd:' + tail.path)) walk(tail, depth + 1)
    }
    const fs = [...node.files].sort((a, b) => a.path.localeCompare(b.path))
    for (const f of fs) {
      rows.push({ key: prefix + 'f:' + f.path, name: f.path.split('/').pop() || f.path, depth, dir: false, file: f })
    }
  }
  walk(root, 0)
  return rows
}

function toggleDir(key: string) {
  const next = new Set(closedDirs.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  closedDirs.value = next
}

/** 目录行上的「+ / −」：把该目录下的整批文件一次 add / reset。
 *  目录可能有几十个文件，逐个勾太慢；这也是「悬停即见」最值的两个位置之一。 */
function dirPathOfRow(key: string) {
  const i = key.indexOf('d:')
  return i >= 0 ? key.slice(i + 2) : ''
}

function stageDirRow(row: TreeRow<WorkFile>) {
  const dir = dirPathOfRow(row.key)
  if (!dir || busy.value) return
  const files = unstagedFiles.value.filter(f => f.path.startsWith(dir + '/')).map(f => f.path)
  if (!files.length) return
  void runGitOp(() => GitStage(activePath.value, files))
}

function unstageDirRow(row: TreeRow<WorkFile>) {
  const dir = dirPathOfRow(row.key)
  if (!dir || busy.value) return
  const files = stagedFiles.value.filter(f => f.path.startsWith(dir + '/')).map(f => f.path)
  if (!files.length) return
  void runGitOp(() => GitUnstage(activePath.value, files))
}

/** 平铺模式的同等形状：模板只认 TreeRow，不用为两种视图写两套 DOM。 */
function flatRows<T>(files: T[], prefix: string, pathOf: (f: T) => string): TreeRow<T>[] {
  return files.map(f => ({
    key: prefix + 'f:' + pathOf(f), name: pathOf(f), depth: 0, dir: false, file: f,
  }))
}

const stagedRows = computed(() =>
  treeMode.value ? buildTree(stagedFiles.value, 's:') : flatRows(stagedFiles.value, 's:', f => f.path))
const unstagedRows = computed(() =>
  treeMode.value ? buildTree(unstagedFiles.value, 'u:') : flatRows(unstagedFiles.value, 'u:', f => f.path))
const commitRows = computed(() => {
  const files = detail.value?.files || []
  return treeMode.value ? buildTree(files, 'c:') : flatRows(files, 'c:', f => blockPath(f))
})

// ---- 并排 diff：把 unified 的 -/+ 行配成左右两栏 ----
//
// 配对规则：连续的 del 行与 add 行按位置一一对应，多出来的一侧补空行；遇到上下文行
// 或新 hunk 就截断当前配对。这不是「真正的行级 diff」（真正的对齐要跑 diff 算法），
// 但 git 输出的 hunk 本来就把相关改动放在一起，按位置配对已经能对上九成场景。

interface SideRow {
  ln?: number
  text?: string
  cls: string
  rn?: number
  rtext?: string
  rcls: string
}

const stripSign = (s: string) => s.replace(/\n$/, '').slice(1)

function sideRows(lines: DiffLine[]): SideRow[] {
  const out: SideRow[] = []
  let ln = 0
  let rn = 0
  let dels: string[] = []
  let adds: string[] = []
  let dStart = 0
  let aStart = 0
  const flush = () => {
    const n = Math.max(dels.length, adds.length)
    for (let i = 0; i < n; i++) {
      const d = dels[i]
      const a = adds[i]
      out.push({
        ln: d !== undefined ? dStart + i : undefined,
        text: d ?? '',
        cls: d !== undefined ? 'del' : 'blank',
        rn: a !== undefined ? aStart + i : undefined,
        rtext: a ?? '',
        rcls: a !== undefined ? 'add' : 'blank',
      })
    }
    dels = []
    adds = []
  }
  for (const l of lines) {
    if (l.cls === 'meta') {
      flush()
      // 文件头已有 side-file 显示路径，diff --git / index / --- / +++ 都不再重复
      continue
    }
    if (l.cls === 'hunk') {
      flush()
      const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(l.text)
      if (m) {
        ln = Number(m[1])
        rn = Number(m[2])
      }
      out.push({ cls: 'hunk', text: l.text.trimEnd(), rcls: 'hunk' })
      continue
    }
    if (l.cls === 'del') {
      if (!dels.length) dStart = ln
      dels.push(stripSign(l.text))
      ln++
      continue
    }
    if (l.cls === 'add') {
      if (!adds.length) aStart = rn
      adds.push(stripSign(l.text))
      rn++
      continue
    }
    flush()
    const text = l.text.replace(/\n$/, '')
    out.push({ ln: ln++, text, cls: 'ctx', rn: rn++, rtext: text, rcls: 'ctx' })
  }
  flush()
  return out
}

/** 并排行数上限：一屏双列比单列更容易把渲染压垮，截断到 unified 的一半左右。 */
const sideBlocks = computed(() => {
  let budget = MAX_LINES
  const out: { path: string; rows: SideRow[] }[] = []
  for (const b of shownBlocks.value) {
    if (budget <= 0) break
    const rows = sideRows(b.lines)
    out.push({ path: b.path, rows: rows.slice(0, budget) })
    budget -= rows.length
  }
  return out
})

function groupToggle(k: keyof typeof open) { open[k] = !open[k] }

// 全局 Esc：按「先弹层、后菜单」的顺序收起，一次只关一层（项目通用约定）。
// 中文输入法用 Esc 取消候选，所以必须先排除输入法合成态。
function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape' || e.isComposing || e.keyCode === 229) return
  if (promptState.value) { promptState.value = null; return }
  if (confirmState.value) { confirmState.value = null; return }
  if (blameState.value) { blameState.value = null; return }
  if (fileView.value) { fileView.value = null; return }
  if (ctx.value) { ctx.value = null; return }
  if (compareState.value) { compareState.value = null; return }
  if (subOpen.value) { subOpen.value = false; return }
  if (remoteOpen.value) { remoteOpen.value = false; return }
  if (sshOpen.value) { closeSSH(); return }
  if (cloneOpen.value) closeClone()
}
onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))

onMounted(async () => {
  if (PREVIEW) { seedPreview(); return }
  // 环境探测与仓库清单并行：两件事互不依赖，串行会让空面板多空 200ms
  await Promise.all([loadRepos(), checkGitEnv()])
  await loadRepo()
})
onUnmounted(() => { document.body.style.cursor = ''; document.body.style.userSelect = ''; document.removeEventListener('click', closeMore) })

// ===== PREVIEW-ONLY（截图预览用：浏览器里没有 Wails 桥，加 ?preview=1 注入假数据以便 chrome headless 截图调样式；仅 DEV 生效，保留供后续视觉迭代）=====
const PREVIEW = import.meta.env.DEV && window.location.search.includes('preview=1')
function seedPreview() {
  const now = Math.floor(Date.now() / 1000)
  repos.value = [
    { path: 'E:\\pp\\quickdock', name: 'quickdock', addedAt: '' },
    { path: 'E:\\pp\\hnwf', name: 'hnwf', addedAt: '' },
  ]
  activePath.value = repos.value[0].path
  status.value = {
    path: repos.value[0].path, branch: 'dev', upstream: 'origin/dev',
    ahead: 2, behind: 0,
    changes: [
      { status: 'M', path: 'services/git/service.go', desc: '已修改', add: 34, del: 12 },
      { status: 'M', path: 'frontend/src/components/GitPage.vue', desc: '已修改', add: 128, del: 41 },
      { status: 'A', path: 'frontend/src/components/DashboardPage.vue', desc: '新增', add: 412, del: 0 },
      { status: '??', path: 'docs/preview-notes.md', desc: '未跟踪', add: 0, del: 0 },
    ] as FileStat[] | null,
    clean: false, noCommits: false, gitMissing: false, error: '',
  }
  branches.value = {
    local: [
      { name: 'dev', current: true, remote: false, ahead: 2, behind: 0 },
      { name: 'main', current: false, remote: false, tag: false, ahead: 0, behind: 0 },
      { name: 'feat/monitor-kinds', current: false, remote: false, tag: false, ahead: 0, behind: 0 },
      { name: 'fix/appstate-key', current: false, remote: false, tag: false, ahead: 0, behind: 0 },
    ],
    remote: [
      { name: 'origin/dev', current: false, remote: true, ahead: 0, behind: 0 },
      { name: 'origin/main', current: false, remote: true, ahead: 0, behind: 0 },
      { name: 'origin/feat/wsl', current: false, remote: true, ahead: 0, behind: 0 },
    ],
    tags: [
      { name: 'v0.9.2', current: false, remote: true, tag: true, ahead: 0, behind: 0 },
      { name: 'v0.9.1', current: false, remote: true, tag: true, ahead: 0, behind: 0 },
      { name: 'v0.9.0', current: false, remote: true, tag: true, ahead: 0, behind: 0 },
    ],
  }
  commits.value = [
    { hash: 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0', short: 'a1b2c3d', author: 'Mr.liang', email: '', ts: now - 1200, subject: 'feat(git): 克隆仓库加入总览 Git 卡', refs: 'HEAD -> dev, origin/dev', graph: '* ' },
    { hash: 'b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1', short: 'b2c3d4e', author: 'Mr.liang', email: '', ts: now - 72000, subject: 'feat(dashboard): 总览刻度带与双列布局', refs: 'origin/dev', graph: '* ' },
    { hash: 'c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1b2', short: 'c3d4e5f', author: 'Mr.liang', email: '', ts: now - 140000, subject: 'fix(db): app_state 白名单放行 git_ 前缀', refs: '', graph: '* ' },
    { hash: 'd4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1b2c3', short: 'd4e5f6a', author: 'liang.dev', email: '', ts: now - 190000, subject: 'feat(monitor): 多类型探活（tcp/process/cmd）', refs: 'tag: v0.9.2', graph: '| * ' },
    { hash: 'e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1b2c3d4', short: 'e5f6a7b', author: 'Mr.liang', email: '', ts: now - 260000, subject: 'chore: 清理 snippet 命名残留', refs: '', graph: '* | ' },
    { hash: 'f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1b2c3d4e5', short: 'f6a7b8c', author: 'liang.dev', email: '', ts: now - 340000, subject: 'feat(env): WSL 发行版列表与默认切换', refs: 'origin/feat/wsl', graph: '* | ' },
    { hash: '0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b', short: '0a1b2c3', author: 'Mr.liang', email: '', ts: now - 480000, subject: 'refactor(db): 列白名单三处同步检查', refs: '', graph: '* | ' },
    { hash: '1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1', short: '1b2c3d4', author: 'Mr.liang', email: '', ts: now - 620000, subject: 'docs: 补充数据目录迁移验收清单', refs: 'tag: v0.9.1', graph: '* | ' },
    { hash: '2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1b2', short: '2c3d4e5', author: 'liang.dev', email: '', ts: now - 760000, subject: 'fix(port): 排除本应用监听端口的误报', refs: '', graph: '| * ' },
    { hash: '3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1b2c3', short: '3d4e5f6', author: 'Mr.liang', email: '', ts: now - 900000, subject: 'init: 项目骨架', refs: 'tag: v0.9.0', graph: '* ' },
  ] as Commit[]
  selectedHash.value = commits.value[0].hash
  detail.value = {
    hash: selectedHash.value,
    files: [
      { status: 'M', path: 'services/git/service.go', desc: '已修改', add: 34, del: 12 },
      { status: 'M', path: 'frontend/src/components/GitPage.vue', desc: '已修改', add: 128, del: 41 },
    ] as FileStat[] | null,
    diff: [
      'diff --git a/services/git/service.go b/services/git/service.go',
      'index 3f2a1bc..8d71e09 100644',
      '--- a/services/git/service.go',
      '+++ b/services/git/service.go',
      '@@ -286,9 +286,17 @@ func (s *GitService) loadRepos() ([]RepoRef, error) {',
      ' func (s *GitService) loadRepos() ([]RepoRef, error) {',
      '-\tres := s.App.GetValue(repoStoreKey)',
      '-\tif res == nil || res.Code != 0 {',
      '+\t// 白名单只守前端入口，内部写入走 SetSetting。',
      '+\tif s.App == nil || s.App.DB == nil {',
      '+\t\treturn []RepoRef{}, nil',
      '+\t}',
      '+\traw, err := s.App.DB.GetSetting(repoStoreKey)',
      ' \t\treturn []RepoRef{}, nil',
      ' \t}',
      ' \tvar repos []RepoRef',
      'diff --git a/frontend/src/components/GitPage.vue b/frontend/src/components/GitPage.vue',
      'index 91c04d2..5ab7712 100644',
      '--- a/frontend/src/components/GitPage.vue',
      '+++ b/frontend/src/components/GitPage.vue',
      '@@ -130,6 +130,14 @@ async function addRepo() {',
      ' }',
      '   try {',
      '     const picked = unwrap<string>(await GitPickRepo())',
      '+    if (!picked) return',
      '+    unwrap(await GitAddRepo(picked))',
      '+    await loadRepos()',
      '     if (!picked) return // 用户取消了目录选择',
    ].join('\n'),
  }
  // 工作区预览：默认就停在这里（与真实行为一致：有改动先看改动）
  worktreeMode.value = true
  worktree.value = {
    branch: 'dev', upstream: 'origin/dev', ahead: 2, behind: 0, noCommits: false,
    staged: [
      { status: 'M', path: 'services/git/service.go', desc: '已修改', add: 34, del: 12 },
      { status: 'A', path: 'services/git/remotes.go', desc: '新增', add: 142, del: 0 },
    ],
    unstaged: [
      { status: 'M', path: 'frontend/src/components/GitPage.vue', desc: '已修改', add: 128, del: 41 },
      { status: '??', path: 'docs/preview-notes.md', desc: '未跟踪', add: 0, del: 0 },
      { status: 'M', path: 'frontend/src/components/DashboardPage.vue', desc: '已修改', add: 62, del: 18 },
      { status: 'M', path: 'frontend/src/i18n/zh-CN.ts', desc: '已修改', add: 24, del: 0 },
      { status: 'M', path: 'frontend/src/i18n/en-US.ts', desc: '已修改', add: 24, del: 0 },
      { status: '??', path: 'docs/git-feature-matrix.md', desc: '未跟踪', add: 0, del: 0 },
      { status: 'D', path: 'services/git/legacy_worktree.go', desc: '已删除', add: 0, del: 88 },
      { status: 'M', path: 'refs/frontend.md', desc: '已修改', add: 6, del: 2 },
    ],
  }
  worktreeDiff.value = detail.value?.diff || ''
  commitMsg.value = 'feat(git): 工作区暂存、提交与 AI 代笔'
  stashes.value = [
    { ref: 'stash@{0}', index: 0, branch: 'dev', message: 'wip: 总览刻度带配色', auto: false, ts: now - 9000 },
    { ref: 'stash@{1}', index: 1, branch: 'main', message: 'fix(db): 列白名单', auto: false, ts: now - 92000 },
  ]
  subs.value = [
    { path: 'vendor/onnxruntime', sha: '4f2a9c1de8b7a6f5e4d3c2b1a0987654321fedcb', state: '-', describe: '' },
    { path: 'libs/ui-kit', sha: 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0', state: ' ', describe: 'v2.3.1' },
    { path: 'third_party/ffmpeg', sha: 'b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0a1', state: '+', describe: 'heads/main' },
  ] as Submodule[]
}
// ===== PREVIEW-ONLY END =====

// 弹窗层共享上下文：状态留在本页，子组件只渲染 + 写回。
// reactive 会 unwrap 其中的 ref，子组件里 ctx.x = v 等价于 x.value = v。
const dialogCtx = reactive({
  activePath,
  addRemote,
  blameState,
  cloneErr,
  cloneOpen,
  cloneParent,
  cloneTarget,
  cloneUrl,
  cloning,
  closeClone,
  closeSSH,
  compareState,
  confirmRestoreVersion,
  copyText,
  doClone,
  doSSHGenerate,
  doSSHTest,
  fileView,
  fmtTime,
  jumpToCommit,
  newRemote,
  newSub,
  onPromptOk,
  pickParent,
  promptInput,
  promptState,
  remoteBusy,
  remoteOpen,
  remoteToSSH,
  remotes,
  removeRemote,
  saveRemote,
  sshBusy,
  sshComment,
  sshGenerated,
  sshHost,
  sshOpen,
  sshOut,
  sshPass,
  sshRemote,
  sshStatus,
  sshTestOk,
  status,
  statusColor,
  subAdd,
  subBusy,
  subOpen,
  subStateCls,
  subStateText,
  subSync,
  subUpdate,
  subs,
})
</script>

<template>
  <div class="git-page">
    <!-- 工具栏 -->
    <header class="git-bar">
      <div class="bar-left">
        <button class="bar-toggle" :title="t('git_toggle_side')" @click="sideOpen = !sideOpen">
          <PanelLeft :size="14" />
        </button>
        <FolderGit2 :size="15" class="bar-icon" />
        <span class="bar-repo">{{ activeRepo?.name || t('git_no_repo') }}</span>
        <span v-if="activeRepo" class="bar-path" :title="activePath">{{ activePath }}</span>
        <span v-if="changeCount" class="bar-dirty">{{ t('git_changes', { n: changeCount }) }}</span>
      </div>
      <!-- 按 .qd-page-header 的动作规则收成两段：一组次要动作 + 1 个主按钮，
           中间一条短竖线。原来 7 个同级 ghost 平铺，看不出哪个是主操作。 -->
      <div class="bar-actions">
        <div class="qd-action-group">
          <!-- 转圈类挂在按钮上、动画作用到内部 svg：这样在 Download 与 Loader2
               之间切换图标时不用各写一份 class。类名必须避开 .spinning：
               全局 settings.css 有「.spinning { animation: spin }」，用它会连按钮一起转。 -->
          <button class="qd-btn qd-btn-ghost" :class="{ 'is-spinning': busy === 'fetch' }"
                  :disabled="!activePath || !!busy" :aria-busy="busy === 'fetch'" @click="doFetch">
            <Loader2 v-if="busy === 'fetch'" :size="13" />
            <Download v-else :size="13" />
            {{ t('git_fetch') }}
          </button>
          <button class="qd-btn qd-btn-ghost" :class="{ 'is-spinning': busy === 'pull' }"
                  :disabled="!activePath || !!busy" :aria-busy="busy === 'pull'" @click="doPull">
            <Loader2 v-if="busy === 'pull'" :size="13" />
            <ArrowDownToLine v-else :size="13" />
            {{ t('git_pull') }}
            <span v-if="status?.behind" class="btn-badge"><ArrowDown :size="9" />{{ status.behind }}</span>
          </button>
          <button class="qd-btn qd-btn-ghost" :class="{ 'is-spinning': loading }" :disabled="!activePath || loading"
                  :title="t('git_history')" @click="loadRepo()">
            <RefreshCw :size="13" />
          </button>
          <!-- 低频入口收进 ⋯：远端 / 子模块 / SSH 原来和 Fetch 平级排成一排，
               图标大小不一、又没有文字，一行里看不出哪些是同一类动作。 -->
          <div class="bar-more">
            <button class="qd-btn qd-btn-ghost" :class="{ on: moreOpen }" :title="t('git_more')"
                    :disabled="!activePath" :aria-expanded="moreOpen" @click.stop="moreOpen = !moreOpen">
              <MoreHorizontal :size="14" />
            </button>
            <div v-if="moreOpen" class="bar-more-menu" @click.stop>
              <button class="bm-item" @click="openRemotes(); moreOpen = false">
                <Link2 :size="13" />{{ t('git_remotes') }}
              </button>
              <button class="bm-item" @click="openSubmodules(); moreOpen = false">
                <Boxes :size="13" />{{ t('git_submodules') }}
              </button>
              <button class="bm-item" @click="openSSH(); moreOpen = false">
                <KeyRound :size="13" />{{ t('git_ssh') }}
              </button>
            </div>
          </div>
        </div>
        <span class="qd-action-sep"></span>
        <!-- 推送是这一页唯一「会改动远端」的主操作，给实心主按钮；
             ahead 徽章仍挂在按钮上，未推送数量一眼可见。 -->
        <button class="qd-btn qd-btn-primary" :class="{ 'is-spinning': busy === 'push' }"
                :disabled="!activePath || !!busy" :aria-busy="busy === 'push'"
                :title="status?.ahead ? t('git_push_n', { n: status.ahead }) : t('git_push')" @click="doPush">
          <Loader2 v-if="busy === 'push'" :size="13" />
          <Upload v-else :size="13" />
          {{ t('git_push') }}
          <span v-if="status?.ahead" class="btn-badge"><ArrowUp :size="9" />{{ status.ahead }}</span>
        </button>
      </div>
    </header>

    <!-- 本机没装 git：面板所有数据都会是空的，把安装入口放在最显眼的位置 -->
    <div v-if="gitMissing" class="git-warn">
      <AlertTriangle :size="14" class="gw-ic" />
      <div class="gw-text">
        <b>{{ t('git_missing_title') }}</b>
        <span>{{ t('git_missing_desc') }}</span>
      </div>
      <button class="qd-btn" @click="goEnvInstall">{{ t('git_go_env') }}</button>
      <button class="qd-btn qd-btn-ghost" :class="{ 'is-spinning': envChecking }"
              :disabled="envChecking" @click="checkGitEnv">
        <RefreshCw :size="12" />{{ t('git_recheck') }}
      </button>
    </div>

    <p v-if="error" class="git-error">{{ error }}</p>

    <!-- 三列宽度都走 CSS 变量：没拖过时变量为空，宽度由样式表给（含窄屏媒体查询），
         拖过一次之后 inline 变量优先，用户的选择才不会被响应式规则悄悄改回去。 -->
    <div class="git-body" :style="colWidthStyle">
      <!-- 左栏：仓库 / 分支 / 远程 / 标签 / 贮藏 五组 -->
      <aside ref="sideEl" :class="['git-side', { collapsed: !sideOpen }]">
        <div class="grp">
          <div class="grp-head">
            <button class="grp-toggle" @click="groupToggle('repos')">
              <component :is="open.repos ? ChevronDown : ChevronRight" :size="12" />
              <span>{{ t('git_repos') }}</span>
              <span class="grp-n">{{ repos.length }}</span>
            </button>
            <button class="grp-act" :title="t('git_clone')" @click="openClone"><CloudDownload :size="12" /></button>
            <button class="grp-act" :title="t('git_add_repo')" @click="addRepo"><Plus :size="12" /></button>
          </div>
          <ul v-show="open.repos" class="grp-list">
            <li v-key-click v-for="r in repos" :key="r.path" :class="['item', { active: r.path === activePath }]"
                :title="r.path" @click="switchRepo(r)">
              <FolderGit2 :size="11" class="item-ic" />
              <span class="item-name">{{ r.name }}</span>
              <button class="item-x" :title="t('git_remove_repo')" @click.stop="removeRepo(r)">
                <X :size="11" />
              </button>
            </li>
            <li v-if="!repos.length" class="grp-empty">{{ t('git_repos_empty') }}</li>
          </ul>
        </div>

        <template v-if="activePath">
          <div class="grp">
            <div class="grp-head">
              <button class="grp-toggle" @click="groupToggle('local')">
                <component :is="open.local ? ChevronDown : ChevronRight" :size="12" />
                <span>{{ t('git_local_branches') }}</span>
                <span class="grp-n">{{ branches.local.length }}</span>
              </button>
            </div>
            <ul v-show="open.local" class="grp-list">
              <li v-key-click v-for="b in branches.local" :key="b.name"
                  :class="['item', { current: b.current, 'is-spinning': isCheckingOut(b) }]"
                  tabindex="0" @click="doCheckout(b)" @contextmenu.prevent="openBranchCtx($event, b)"
                  @keydown.menu.prevent="openBranchCtx($event, b)"
                  @keydown.shift.f10.prevent="openBranchCtx($event, b)">
                <Loader2 v-if="isCheckingOut(b)" :size="11" class="item-ic" />
                <GitBranch v-else :size="11" class="item-ic" />
                <span class="item-name">{{ b.name }}</span>
                <span v-if="b.ahead" class="ab up"><ArrowUp :size="10" />{{ b.ahead }}</span>
                <span v-if="b.behind" class="ab down"><ArrowDown :size="10" />{{ b.behind }}</span>
                <Check v-if="b.current" :size="11" class="item-cur" />
              </li>
            </ul>
          </div>

          <div v-if="branches.remote.length" class="grp">
            <div class="grp-head">
              <button class="grp-toggle" @click="groupToggle('remote')">
                <component :is="open.remote ? ChevronDown : ChevronRight" :size="12" />
                <span>{{ t('git_remote_branches') }}</span>
                <span class="grp-n">{{ branches.remote.length }}</span>
              </button>
            </div>
            <ul v-show="open.remote" class="grp-list">
              <li v-key-click v-for="b in branches.remote" :key="b.name"
                  :class="['item', 'dim', { 'is-spinning': isCheckingOut(b) }]"
                  :title="t('git_checkout_remote')" tabindex="0" @click="doCheckout(b)"
                  @contextmenu.prevent="openBranchCtx($event, b)"
                  @keydown.menu.prevent="openBranchCtx($event, b)"
                  @keydown.shift.f10.prevent="openBranchCtx($event, b)">
                <Loader2 v-if="isCheckingOut(b)" :size="11" class="item-ic" />
                <Users v-else :size="11" class="item-ic" />
                <span class="item-name">{{ b.name.replace(/^[^/]+\//, '') }}</span>
                <span class="item-remote">{{ b.name.split('/')[0] }}</span>
              </li>
            </ul>
          </div>

          <div v-if="branches.tags.length" class="grp">
            <div class="grp-head">
              <button class="grp-toggle" @click="groupToggle('tags')">
                <component :is="open.tags ? ChevronDown : ChevronRight" :size="12" />
                <span>{{ t('git_tags') }}</span>
                <span class="grp-n">{{ branches.tags.length }}</span>
              </button>
            </div>
            <ul v-show="open.tags" class="grp-list">
              <li v-for="g in branches.tags" :key="g.name" class="item dim" :title="t('git_ctx_push_tag')"
                  tabindex="0" @contextmenu.prevent="openTagCtx($event, g)"
                  @keydown.menu.prevent="openTagCtx($event, g)"
                  @keydown.shift.f10.prevent="openTagCtx($event, g)">
                <Tag :size="11" class="item-ic" />
                <span class="item-name">{{ g.name }}</span>
              </li>
            </ul>
          </div>

          <!-- 贮藏：默认展开（它通常就一两条，而且是「我要拿回来的东西」） -->
          <div class="grp">
            <div class="grp-head">
              <button class="grp-toggle" @click="groupToggle('stash')">
                <component :is="open.stash ? ChevronDown : ChevronRight" :size="12" />
                <span>{{ t('git_stash') }}</span>
                <span class="grp-n">{{ stashes.length }}</span>
              </button>
              <button class="grp-act" :title="t('git_stash_save')" @click="promptStash">
                <Plus :size="12" />
              </button>
            </div>
            <ul v-show="open.stash" class="grp-list">
              <li v-key-click v-for="s in stashes" :key="s.ref"
                  :class="['item', 'dim', { active: selectedStash?.ref === s.ref }]"
                  :title="`${s.ref} · ${s.branch} · ${s.message}`" tabindex="0"
                  @click="selectStash(s)" @contextmenu.prevent="openStashCtx($event, s)"
                  @keydown.menu.prevent="openStashCtx($event, s)"
                  @keydown.shift.f10.prevent="openStashCtx($event, s)">
                <Archive :size="11" class="item-ic" />
                <span class="item-name">{{ s.message || t('git_stash_auto') }}</span>
                <span class="item-remote">{{ s.ref.replace(/^stash@\{|\}$/g, '') }}</span>
              </li>
              <li v-if="!stashes.length" class="grp-empty">{{ t('git_stash_empty') }}</li>
            </ul>
          </div>
        </template>
      </aside>

      <!-- 列分隔：左栏 ↔ 工作区。折叠左栏时它随左栏一起消失，不留一条悬空的线。 -->
      <div v-if="sideOpen" class="vsplit" :class="{ dragging: dragCol === 'side' }"
           role="separator" aria-orientation="vertical" :title="t('git_resize_col')"
           @mousedown.prevent="startColSplit($event, 'side')"
           @dblclick="resetColSplit('side')"></div>

      <!-- 工作区栏：常驻左侧独立一列。
           以前工作区（已暂存 / 未暂存 + 提交框）和提交详情共用右下角那块区域，
           点开就像一张抽屉盖在历史上，diff 只剩一个角。现在固定成第三列：
           左 = 仓库 / 分支，中 = 工作区改动，右 = 历史 + diff，互不遮挡。 -->
      <aside v-if="activePath" ref="wtEl" class="git-wt">
        <div class="wt-head">
          <FileDiff :size="13" class="wt-ic" :class="{ clean: !changeCount }" />
          <span class="wt-name">{{ t('git_worktree') }}</span>
          <span v-if="changeCount" class="wt-n">{{ changeCount }}</span>
          <!-- 树 / 平铺是整栏的视图设置，不属于「已暂存」这一组 -->
          <button class="pane-ic" :title="t('git_tree_toggle')" @click="treeMode = !treeMode">
            <List v-if="treeMode" :size="11" />
            <ListTree v-else :size="11" />
          </button>
          <button class="wt-all" :class="{ on: worktreeMode }" :title="t('git_worktree_diff')"
                  @click="openWorktree">{{ t('git_all_changes') }}</button>
        </div>

        <!-- 两组常驻：干净时也照原样摆「已暂存 / 未暂存」两条组头，各自下面一行灰字。
             之前试过把干净态收成一块居中空态，结果是整栏一大片深色里孤零零一个勾，
             比两行组头更空；组头本身也是「拖到这里 add / reset」的投放区，收起来等于
             把一个持续可用的入口藏掉了。 -->
        <div ref="wtPanesEl" class="wt-panes" :style="{ '--wt-top': wtPct + '%' }">
          <!-- 已暂存：上半区，高度由中间那条分隔条决定；内容超了就在自己的框内滚 -->
          <div class="wt-pane wt-pane-top">
          <!-- 组头同时是拖放投放区：把文件从另一组拖过来即可 add / reset。
               整组做投放区范围太大，拖到一半就触发，所以只认组头这一条。 -->
          <div class="pane-head" :class="{ 'drop-on': dropOn === 'staged' }"
               @dragover="onGroupDragOver($event, 'staged')"
               @dragleave="onGroupDragLeave"
               @drop="onGroupDrop($event, 'staged')">
            <!-- 组头只留「状态点 + 名称 + 数量」：原来的 FileText 图标两组一模一样，
                 说了等于没说。实心点 = 已加入暂存区，空心点 = 还没加。 -->
            <span class="grp-dot on"></span>
            <span class="pane-name">{{ t('git_staged') }}</span>
            <span v-if="stagedFiles.length" class="pane-n">{{ stagedFiles.length }}</span>
            <button v-if="stagedFiles.length" class="pane-act" :disabled="!!busy" @click="unstageAll">
              {{ t('git_unstage_all') }}
            </button>
          </div>
          <ul v-if="stagedFiles.length" class="file-rows wt-rows"
              @dragover="onGroupDragOver($event, 'staged')" @dragleave="onGroupDragLeave"
              @drop="onGroupDrop($event, 'staged')">
            <template v-for="row in stagedRows" :key="row.key">
              <li v-key-click v-if="row.dir" class="tree-dir"
                  :style="{ paddingLeft: 10 + row.depth * 12 + 'px' }" @click="toggleDir(row.key)">
                <ChevronDown v-if="!closedDirs.has(row.key)" :size="10" />
                <ChevronRight v-else :size="10" />
                <Folder :size="11" class="td-ic" />
                <span class="td-name">{{ row.name }}</span>
                <button class="file-quick" :title="t('git_unstage')" :disabled="!!busy"
                        @click.stop="unstageDirRow(row)"><Minus :size="11" /></button>
              </li>
              <li v-key-click v-else-if="row.file"
                  :class="['file-row', { active: worktreeMode && selectedFile === row.file!.path, picked: picked.has('s:' + row.file!.path) }]"
                  :style="{ paddingLeft: 10 + row.depth * 12 + 'px' }"
                  :title="row.file!.path" tabindex="0" draggable="true"
                  @dragstart="onFileDragStart($event, row.file!, 'staged')" @dragend="onFileDragEnd"
                  @click="onFileClick($event, row.file!, 'staged')"
                  @contextmenu.prevent="openFileCtx($event, row.file!, 'staged')"
                  @keydown.menu.prevent="openFileCtx($event, row.file!, 'staged')"
                  @keydown.shift.f10.prevent="openFileCtx($event, row.file!, 'staged')">
                <input class="stage-box" type="checkbox" :checked="true" :disabled="busy === 'stage'"
                       :title="t('git_unstage')" @click.stop="toggleStage(row.file!, true)" />
                <span :class="['file-badge', statusColor(row.file!.status)]">{{ row.file!.status || 'M' }}</span>
                <span class="file-path">{{ row.name }}</span>
                <span v-if="row.file!.add || row.file!.del" class="file-num">
                  <b v-if="row.file!.add" class="add">+{{ row.file!.add }}</b>
                  <b v-if="row.file!.del" class="del">−{{ row.file!.del }}</b>
                </span>
                <button class="file-quick" :title="t('git_unstage')" :disabled="busy === 'stage'"
                        @click.stop="toggleStage(row.file!, true)"><Minus :size="11" /></button>
              </li>
            </template>
          </ul>
          <p v-else class="file-state">{{ t('git_nothing_staged') }}</p>
          </div>

          <!-- 两组之间的分隔条：拖动改上半区高度，双击回到默认比例 -->
          <div class="splitter" role="separator" aria-orientation="horizontal"
               :title="t('git_resize_wt')" @mousedown.prevent="startWtSplit"
               @dblclick="wtPct = WT_PCT_DEFAULT"></div>

          <!-- 未暂存：下半区，吃剩下的高度 -->
          <div class="wt-pane wt-pane-bottom">
          <div class="pane-head" :class="{ 'drop-on': dropOn === 'unstaged' }"
               @dragover="onGroupDragOver($event, 'unstaged')"
               @dragleave="onGroupDragLeave"
               @drop="onGroupDrop($event, 'unstaged')">
            <span class="grp-dot"></span>
            <span class="pane-name">{{ t('git_unstaged') }}</span>
            <span v-if="unstagedFiles.length" class="pane-n">{{ unstagedFiles.length }}</span>
            <button v-if="unstagedFiles.length" class="pane-act" :disabled="!!busy" @click="stageAll">
              {{ t('git_stage_all') }}
            </button>
            <button v-if="unstagedFiles.length" class="pane-ic danger" :title="t('git_discard_all')"
                    :disabled="!!busy" @click="confirmDiscardAll"><Trash2 :size="11" /></button>
          </div>
          <ul v-if="unstagedFiles.length" class="file-rows wt-rows"
              @dragover="onGroupDragOver($event, 'unstaged')" @dragleave="onGroupDragLeave"
              @drop="onGroupDrop($event, 'unstaged')">
            <template v-for="row in unstagedRows" :key="row.key">
              <li v-key-click v-if="row.dir" class="tree-dir"
                  :style="{ paddingLeft: 10 + row.depth * 12 + 'px' }" @click="toggleDir(row.key)">
                <ChevronDown v-if="!closedDirs.has(row.key)" :size="10" />
                <ChevronRight v-else :size="10" />
                <Folder :size="11" class="td-ic" />
                <span class="td-name">{{ row.name }}</span>
                <button class="file-quick" :title="t('git_stage')" :disabled="!!busy"
                        @click.stop="stageDirRow(row)"><Plus :size="11" /></button>
              </li>
              <li v-key-click v-else-if="row.file"
                  :class="['file-row', { active: worktreeMode && selectedFile === row.file!.path, picked: picked.has('u:' + row.file!.path) }]"
                  :style="{ paddingLeft: 10 + row.depth * 12 + 'px' }"
                  :title="row.file!.path" tabindex="0" draggable="true"
                  @dragstart="onFileDragStart($event, row.file!, 'unstaged')" @dragend="onFileDragEnd"
                  @click="onFileClick($event, row.file!, 'unstaged')"
                  @contextmenu.prevent="openFileCtx($event, row.file!, 'unstaged')"
                  @keydown.menu.prevent="openFileCtx($event, row.file!, 'unstaged')"
                  @keydown.shift.f10.prevent="openFileCtx($event, row.file!, 'unstaged')">
                <input class="stage-box" type="checkbox" :checked="false" :disabled="busy === 'stage'"
                       :title="t('git_stage')" @click.stop="toggleStage(row.file!, false)" />
                <span :class="['file-badge', statusColor(row.file!.status)]">{{ row.file!.status || 'M' }}</span>
                <span class="file-path">{{ row.name }}</span>
                <span v-if="row.file!.add || row.file!.del" class="file-num">
                  <b v-if="row.file!.add" class="add">+{{ row.file!.add }}</b>
                  <b v-if="row.file!.del" class="del">−{{ row.file!.del }}</b>
                </span>
                <button class="file-quick" :title="t('git_stage')" :disabled="busy === 'stage'"
                        @click.stop="toggleStage(row.file!, false)"><Plus :size="11" /></button>
              </li>
            </template>
          </ul>
          <p v-else class="file-state">{{ t('git_worktree_empty') }}</p>
          </div>
        </div>

        <!-- 多选浮条：Ctrl/⌘ 点选、Shift 连选后出现，贴在文件区底部，滚多远都在手边 -->
        <div v-if="pickedCount" class="pick-bar">
          <span class="pb-n">{{ t('git_picked_n', { n: pickedCount }) }}</span>
          <button v-if="pickedUnstaged.length" class="pb-btn" :disabled="!!busy" @click="stagePicked">
            {{ t('git_stage_picked') }}
          </button>
          <button v-if="pickedStaged.length" class="pb-btn" :disabled="!!busy" @click="unstagePicked">
            {{ t('git_unstage_picked') }}
          </button>
          <button class="pb-btn danger" :disabled="!!busy" @click="confirmDiscardPicked">
            {{ t('git_ctx_discard') }}
          </button>
          <button class="pb-x" :title="t('git_picked_clear')" @click="clearPicked">
            <X :size="12" />
          </button>
        </div>

        <!-- 提交面板：跟着工作区走，固定在左栏底部 -->
        <div v-if="worktree" class="commit-box">
          <div class="cb-head">
            <span class="cb-label">{{ t('git_commit_msg') }}</span>
            <button class="cb-mini" :class="{ 'is-on': amendMode }"
                    :title="t('git_amend_title')" @click="amendMode = !amendMode">
              <Pencil :size="11" />
            </button>
            <button class="cb-mini" :class="{ 'is-spinning': aiWriting }"
                    :disabled="aiWriting || !changeCount" :title="t('git_ai_hint')" @click="aiCommitMessage">
              <Loader2 v-if="aiWriting" :size="11" />
              <Sparkles v-else :size="11" />
            </button>
          </div>
          <textarea v-model="commitMsg" class="cb-input" rows="3"
                    :placeholder="amendMode ? t('git_amend_ph') : t('git_commit_ph')"
                    @keydown.ctrl.enter.prevent="doCommit" @keydown.meta.enter.prevent="doCommit"></textarea>
          <div class="cb-foot">
            <span class="cb-hint">{{ commitHint }}</span>
            <button class="qd-btn qd-btn-primary" :class="{ 'is-spinning': committing }"
                    :disabled="!canCommit" :title="commitBlockReason" @click="doCommit">
              <Loader2 v-if="committing" :size="12" />
              <Check v-else :size="12" />
              {{ amendMode ? t('git_amend_btn') : t('git_commit_btn') }}
            </button>
          </div>
        </div>
      </aside>

      <!-- 列分隔：工作区 ↔ 提交区。没有打开的仓库时工作区栏不存在，这条也一起收掉。 -->
      <div v-if="activePath" class="vsplit" :class="{ dragging: dragCol === 'wt' }"
           role="separator" aria-orientation="vertical" :title="t('git_resize_col')"
           @mousedown.prevent="startColSplit($event, 'wt')"
           @dblclick="resetColSplit('wt')"></div>

      <!-- 右栏：提交表 + 详情。 -->
      <section ref="mainEl" class="git-main">

        <!-- 文件历史横幅：列表内容被换成了「这个文件的提交」，必须有个明确出口 -->
        <div v-if="fileHist" class="fh-bar">
          <History :size="12" class="fh-ic" />
          <span class="fh-name">{{ t('git_file_history') }}</span>
          <span class="fh-path" :title="fileHist.path">{{ fileHist.path }}</span>
          <span class="fh-n">{{ fileCommits.length }}</span>
          <button class="fh-x" :title="t('git_exit_history')" @click="exitFileHistory"><X :size="11" /></button>
        </div>

        <!-- 提交筛选：输入即过滤已载入的 200 条；回车升格为全历史搜索（问 git 本身，覆盖所有分支） -->
        <div v-if="activePath" class="hist-bar">
          <div class="hb-scope">
            <button :class="{ on: logScope === 'current' }" :title="t('git_scope_current_hint')"
                    @click="setLogScope('current')">{{ t('git_scope_current') }}</button>
            <button :class="{ on: logScope === 'all' }" :title="t('git_scope_all_hint')"
                    @click="setLogScope('all')">{{ t('git_scope_all') }}</button>
          </div>
          <Search :size="11" class="hb-ic" />
          <input v-model="histQuery" class="hb-input" spellcheck="false"
                 :placeholder="t('gitFilterPhMin2')" :title="t('git_search_hint')"
                 @keydown.enter.prevent="doLogSearch" @input="onHistInput" />
          <button v-if="histQuery" class="hb-x" :title="t('git_filter_clear')" @click="clearHistFilter">
            <X :size="10" />
          </button>
          <span v-if="searching" class="hb-mode"><Loader2 :size="10" class="spin" />{{ t('loading') }}</span>
          <span v-else-if="searchResults" class="hb-mode">{{ t('git_search_all') }}</span>
          <span class="hb-n">
            {{ shownCommits.length }}<template v-if="histQuery">/{{ scopedCommits.length }}</template>
          </span>
        </div>

        <div class="commit-head" :style="showGraph ? { '--graph-w': graphWidth + 'px' } : null">
          <span v-if="showGraph" class="col-graph"></span>
          <span class="commit-head-main">
            <span>{{ t('git_col_subject') }}</span>
            <span class="ch-time">{{ t('git_col_date') }}</span>
          </span>
        </div>

        <div class="commit-scroll" :style="{ flexBasis: listPct + '%' }">
          <div v-if="loading && !shownCommits.length" class="pane-state">
            <Loader2 :size="15" class="spin" />{{ t('loading') }}
          </div>
          <div v-else-if="!activePath" class="pane-state">
            <FolderGit2 :size="26" />
            <p>{{ t('git_pick_hint') }}</p>
            <div class="state-actions">
              <button class="qd-btn qd-btn-primary" @click="addRepo">
                <Plus :size="13" />{{ t('git_add_repo') }}
              </button>
              <button class="qd-btn" @click="openClone">
                <CloudDownload :size="13" />{{ t('git_clone') }}
              </button>
            </div>
          </div>
          <p v-else-if="status?.noCommits" class="pane-state">{{ t('git_no_commits') }}</p>
          <p v-else-if="fileHist && !shownCommits.length" class="pane-state">{{ t('git_file_history_empty') }}</p>
          <p v-else-if="!shownCommits.length && histQuery" class="pane-state">{{ t('git_filter_empty') }}</p>
          <p v-else-if="!shownCommits.length" class="pane-state">{{ t('git_history_empty') }}</p>

          <ul v-else class="commit-list">
            <li v-key-click v-for="(c, gi) in shownCommits" :key="c.hash"
                :class="['commit-item', { active: c.hash === selectedHash }]" tabindex="0"
                :style="showGraph ? { '--graph-w': graphWidth + 'px' } : null"
                @click="selectCommit(c)" @contextmenu.prevent="openCommitCtx($event, c)"
                @keydown.menu.prevent="openCommitCtx($event, c)"
                @keydown.shift.f10.prevent="openCommitCtx($event, c)">
              <!-- 提交图：SVG 只画线（viewBox 纵向归一化到 0-100，随行高自适应），
                   圆点用绝对定位的 span——preserveAspectRatio=none 会把 SVG 里的圆压成椭圆。 -->
              <span v-if="showGraph" class="col-graph">
                <svg class="commit-graph" :viewBox="`0 0 ${graphWidth} ${ROW_H}`" preserveAspectRatio="none" aria-hidden="true">
                  <path v-for="(p, pi) in (graphRows[gi]?.paths || [])" :key="pi"
                        :d="p.d" :stroke="laneVar(p.color)" stroke-width="1.3"
                        fill="none" stroke-linecap="round" vector-effect="non-scaling-stroke" />
                </svg>
                <span v-if="graphRows[gi]" class="commit-dot" :class="{ merge: graphRows[gi].merge }"
                      :style="{ left: graphRows[gi].x + 'px', color: laneVar(graphRows[gi].color) }"></span>
              </span>
              <!-- 单行：摘要 · 分支标记 · 作者 · 时间。参照 GitLens，行高压到 28px，
                   一屏能多看一倍提交；作者靠右，竖着扫不会因为摘要长短来回跳。 -->
              <span class="commit-core">
                <span class="subject-text" :title="c.subject">{{ c.subject }}</span>
                <span v-for="r in refList(c.refs).slice(0, 2)" :key="r"
                      :class="['ref', `ref-${refKind(r)}`]" :title="refText(r)">{{ refText(r) }}</span>
                <span v-if="refMore(c.refs)" class="ref ref-more" :title="c.refs">+{{ refMore(c.refs) }}</span>
                <span class="author-name">{{ c.author }}</span>
                <span class="commit-time">{{ fmtTime(c.ts) }}</span>
              </span>
            </li>
          </ul>
        </div>

        <!-- 拖动条：上下比例由用户定 -->
        <div class="splitter" :class="{ disabled: detailKind === 'none' && !worktreeMode }"
             @mousedown="(detailKind !== 'none' || worktreeMode) && startSplit($event)"></div>

        <div class="detail-wrap">
          <div class="detail">
            <!-- 工作区模式下不显示这一列：文件清单已经在左边常驻的工作区栏里，
                 右侧整块宽度留给 diff（diff 是这一页要读的东西）。 -->
            <div v-if="!worktreeMode && !filesCollapsed" ref="filesEl" class="detail-files">
              <div class="pane-head">
                <FileText :size="12" />
                <span>{{ t('git_changed_files', { n: detail?.files?.length || 0 }) }}</span>
                <button class="pane-ic" :title="t('git_tree_toggle')" @click="treeMode = !treeMode">
                  <List v-if="treeMode" :size="11" />
                  <ListTree v-else :size="11" />
                </button>
              </div>
              <div v-if="detailKind === 'none'" class="file-state">—</div>
              <div v-else-if="detailFailed" class="file-state detail-failed">
                <span>{{ t('loadFailed') }}</span>
                <button class="qd-btn qd-btn-sm" @click="retryDetail">{{ t('retry') }}</button>
              </div>
              <div v-else-if="!detail" class="file-state"><Loader2 :size="13" class="spin" /></div>
              <ul v-else class="file-rows">
                <template v-for="row in commitRows" :key="row.key">
                  <li v-key-click v-if="row.dir" class="tree-dir"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }" @click="toggleDir(row.key)">
                    <ChevronDown v-if="!closedDirs.has(row.key)" :size="10" />
                    <ChevronRight v-else :size="10" />
                    <Folder :size="11" class="td-ic" />
                    <span class="td-name">{{ row.name }}</span>
                  </li>
                  <li v-key-click v-else-if="row.file"
                      :class="['file-row', { active: selectedFile && selectedFile === blockPath(row.file!) }]"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }"
                      :title="row.file!.desc" tabindex="0" @click="selectFile(row.file!)"
                      @contextmenu.prevent="openCommitFileCtx($event, row.file!)"
                      @keydown.menu.prevent="openCommitFileCtx($event, row.file!)"
                      @keydown.shift.f10.prevent="openCommitFileCtx($event, row.file!)">
                    <span :class="['file-badge', statusColor(row.file!.status)]">{{ row.file!.status || 'M' }}</span>
                    <span class="file-path">{{ row.name }}</span>
                    <span v-if="row.file!.add || row.file!.del" class="file-num">
                      <b v-if="row.file!.add" class="add">+{{ row.file!.add }}</b>
                      <b v-if="row.file!.del" class="del">−{{ row.file!.del }}</b>
                    </span>
                  </li>
                </template>
              </ul>
            </div>

            <!-- 列分隔：文件清单 ↔ diff。折叠文件列时随之隐藏 -->
            <div v-if="!worktreeMode && !filesCollapsed" class="vsplit"
                 :class="{ dragging: dragCol === 'files' }"
                 role="separator" aria-orientation="vertical" :title="t('git_resize_col')"
                 @mousedown.prevent="startColSplit($event, 'files')"
                 @dblclick="resetColSplit('files')"></div>

            <div class="detail-diff">
              <div class="pane-head">
                <!-- 提交详情里文件列可折叠：并排 diff 一开就需要横向空间，
                     收起后 diff 吃满主区宽度。按钮留在 diff 头上，收了也能立刻打开。 -->
                <button v-if="!worktreeMode" class="pane-ic lead" :class="{ on: filesCollapsed }"
                        :title="t('git_files_toggle')" :aria-pressed="filesCollapsed"
                        @click="filesCollapsed = !filesCollapsed">
                  <PanelLeft :size="11" />
                </button>
                <span class="diff-title" :title="selectedFile || t('git_all_changes')">
                  {{ selectedFile || t('git_all_changes') }}
                </span>
                <span v-if="detailHash && !worktreeMode" class="diff-hash">{{ detailHash }}</span>
                <button class="diff-toggle" :title="t('git_diff_side_hint')" @click="diffSide = !diffSide">
                  <Columns2 v-if="!diffSide" :size="11" />
                  <Rows3 v-else :size="11" />
                  {{ diffSide ? t('git_diff_unified') : t('git_diff_side') }}
                </button>
                <button v-if="selectedFile" class="diff-reset" @click="selectedFile = ''">
                  {{ t('git_all_changes') }}
                </button>
              </div>
              <template v-if="shownLines.length">
                <pre v-if="!diffSide" class="diff"><span
                  v-for="(l, i) in shownLines" :key="i" :class="l.cls">{{ l.text }}</span><span
                  v-if="truncated" class="trunc">…</span></pre>
                <!-- 并排：左是旧版本、右是新版本，行号各自一列。上下文行两边同文，
                     删除/新增按位置配对，配不上的补空行 -->
                <div v-else class="diff-side">
                  <div v-for="(b, bi) in sideBlocks" :key="bi" class="side-block">
                    <div v-if="b.path" class="side-file">{{ b.path }}</div>
                    <table class="side-table">
                      <!-- 列宽必须由 colgroup 定死，不能靠单元格上的 width：
                           table-layout: fixed 只认「第一行」的宽，而这里每个 table
                           的首行都是 colspan=4 的 hunk 头，浏览器于是把四列均分成
                           各 25%——两个行号列变成两条巨大的空槽，代码只剩一半宽度。
                           colgroup 优先级高于首行单元格，行号 48px、代码平分余下宽度。 -->
                      <colgroup>
                        <col class="sd-col-ln" /><col /><col class="sd-col-ln" /><col />
                      </colgroup>
                      <tbody>
                        <tr v-for="(r, i) in b.rows" :key="i" :class="['side-row', r.cls]">
                          <template v-if="r.cls === 'meta' || r.cls === 'hunk'">
                            <td colspan="4" :class="['sd-full', r.cls]">{{ r.text }}</td>
                          </template>
                          <template v-else>
                            <td class="sd-ln">{{ r.ln ?? '' }}</td>
                            <td :class="['sd-code', r.cls]">{{ r.text }}</td>
                            <td class="sd-ln">{{ r.rn ?? '' }}</td>
                            <td :class="['sd-code', r.rcls]">{{ r.rtext }}</td>
                          </template>
                        </tr>
                      </tbody>
                    </table>
                  </div>
                  <p v-if="truncated" class="trunc">…</p>
                </div>
              </template>
              <p v-else-if="detailFailed" class="pane-state">{{ t('loadFailed') }}</p>
              <p v-else-if="worktreeMode" class="pane-state">
                {{ changeCount ? t('git_no_diff_worktree') : t('git_clean') }}
              </p>
              <p v-else-if="detailActive && detail" class="pane-state">{{ t('git_no_diff') }}</p>
              <div v-else class="pane-state diff-idle">
                <GitBranch :size="20" />
                <p>{{ activePath ? t('git_select_commit') : '' }}</p>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>

    <!-- 状态栏 -->
    <footer class="git-status">
      <span class="st-branch">
        <GitBranch :size="11" />{{ status?.branch || '—' }}
        <template v-if="status?.ahead"><ArrowUp :size="10" /> {{ status.ahead }}</template>
        <template v-if="status?.behind"><ArrowDown :size="10" /> {{ status.behind }}</template>
      </span>
      <span v-if="status?.upstream" class="st-dim">{{ t('git_upstream') }} {{ status.upstream }}</span>
      <span v-if="changeCount" class="st-dirty">
        <span class="st-dot"></span><b>{{ changeCount }}</b>{{ t('git_changes_suffix') }}
      </span>
      <span v-else class="st-dim">{{ t('git_clean') }}</span>
      <span v-if="busy" class="st-dim">{{ t('loading') }}</span>
      <span class="st-path" :title="activePath">{{ activePath }}</span>
    </footer>

    <GitDialogs :ctx="dialogCtx" />

    <!-- 右键菜单：透明遮罩铺满，点空白/滚轮都收起（fixed 定位不会跟着列表滚） -->
    <Teleport to="body">
      <div v-if="ctx" class="ctx-backdrop" @click="closeCtx" @contextmenu.prevent="closeCtx"
           @wheel.prevent="closeCtx">
        <div v-menu ref="ctxMenuEl" class="ctx-menu" :style="{ left: ctx.x + 'px', top: ctx.y + 'px' }"
             @click.stop @contextmenu.stop>
          <template v-for="a in ctxActions" :key="a.id">
            <div v-if="a.sep" class="ctx-sep"></div>
            <button v-else :class="['ctx-item', { danger: a.danger }]" :disabled="a.disabled"
                    @click="a.run(); closeCtx()">{{ a.label }}</button>
          </template>
        </div>
      </div>
    </Teleport>

    <!-- 危险操作确认：丢弃 / 删除分支 / 回滚 / 检出提交 -->
    <ConfirmDialog :visible="!!confirmState" :message="confirmState?.message || ''"
                   :details="confirmState?.details"
                   @confirm="onConfirmYes" @cancel="confirmState = null" />
  </div>
</template>

<style scoped>
/* 三栏浏览器型页面不套 .qd-page-body 的 16px 内边距：面板贴边、
   靠 1px 分隔线切分，才能把纵向空间全留给提交列表（SourceTree 也是如此）。 */
.git-page {
  height: 100%; display: flex; flex-direction: column; overflow: hidden;
  background: var(--color-bg-primary);
}

/* ---- 工具栏 ---- */
/* 高度 / 左右内边距对齐公共页头 .qd-page-header（46px、16px）：Git 是唯一
   不用页头的一级页（顶部是工具栏而不是标题），原来 40px + 12px 内边距，
   和相邻页面切来切去时上沿明显差一截。 */
.git-bar {
  display: flex; align-items: center; justify-content: space-between; gap: var(--space-5);
  /* 与 .qd-page-header 同高、同内边距、同分隔方式：切页时顶部基线不跳。
     内高光已去掉 —— ui.md §3.1 规定页头靠 1px border-bottom 收口，不叠第三层装饰。 */
  height: 46px; padding: 0 var(--space-6); flex-shrink: 0;
  border-bottom: 1px solid var(--color-border); background: var(--color-bg-secondary);
}
.bar-left { display: flex; align-items: center; gap: var(--space-3); min-width: 0; }
/* 收起左栏：看长 diff 时把 240px 让出来，按钮留在工具栏最左，收了也能立刻展开 */
.bar-toggle {
  display: grid; place-items: center; width: 22px; height: 22px; flex-shrink: 0;
  margin-right: var(--space-1); padding: 0; border: 0; border-radius: var(--radius-xs);
  background: none; color: var(--color-text-muted); cursor: pointer;
}
.bar-toggle:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.bar-icon { color: var(--color-accent); flex-shrink: 0; }
.bar-repo { font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary); flex-shrink: 0; }
.bar-path {
  font-size: var(--text-xs); color: var(--color-text-muted); font-family: var(--font-mono);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0;
}
.bar-dirty { font-size: var(--text-xs); color: var(--color-warning); flex-shrink: 0; }
.bar-actions { display: flex; align-items: center; gap: var(--space-2); flex-shrink: 0; }
.bar-actions .qd-btn { height: 26px; }
.qd-btn-ghost.on { background: var(--color-bg-hover); color: var(--color-text-primary); }

/* ⋯ 菜单：远端 / 子模块 / SSH。浮层贴在按钮右下，和右键菜单同一套材质与行高。 */
.bar-more { position: relative; }
.bar-more-menu {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: 60; min-width: 164px;
  display: flex; flex-direction: column; gap: 1px; padding: var(--space-2);
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md); box-shadow: var(--shadow-lg);
}
.bm-item {
  display: flex; align-items: center; gap: var(--space-3);
  height: 28px; padding: 0 var(--space-4); border: 0; border-radius: var(--radius-sm);
  background: none; color: var(--color-text-secondary); font: inherit; font-size: var(--text-sm);
  text-align: left; white-space: nowrap; cursor: pointer;
}
.bm-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.btn-badge {
  display: inline-flex; align-items: center; justify-content: center; gap: 1px;
  min-width: 15px; height: 15px; padding: 0 3px; border-radius: var(--radius-full);
  background: var(--color-accent); color: var(--color-accent-text);
  font-family: var(--font-mono); font-size: var(--text-2xs); line-height: 1;
}
/* 徽章挂在实心的主按钮上时，蓝底压蓝底会糊成一团：反成白底蓝字 */
.qd-btn-primary .btn-badge { background: var(--color-accent-text); color: var(--color-accent); }

.git-error {
  margin: 0; padding: var(--space-3) var(--space-5); font-size: var(--text-sm); flex-shrink: 0;
  color: var(--color-danger); background: var(--color-danger-bg);
  border-bottom: 1px solid var(--color-border);
}

/* 缺 git 的横幅：不用 .git-error 的红条，因为它不是「出错了」，而是「缺前置条件」——
   红条读起来像可以忽略的报错，这里必须带上安装按钮才算把话说完整。 */
.git-warn {
  display: flex; align-items: center; gap: var(--space-4); flex-shrink: 0;
  padding: var(--space-4) var(--space-5);
  background: var(--color-warning-bg); border-bottom: 1px solid var(--color-border);
}
.gw-ic { color: var(--color-warning); flex-shrink: 0; }
.gw-text { display: flex; flex-direction: column; gap: 1px; min-width: 0; flex: 1; }
.gw-text b { font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary); }
.gw-text span { font-size: var(--text-xs); color: var(--color-text-muted); }
.git-warn .qd-btn { height: 24px; flex-shrink: 0; }

/* ---- 主体 ---- */
.git-body { flex: 1; display: flex; min-height: 0; }

/* ---- 左栏 ---- */
.git-side {
  /* 宽度由 --side-w 决定，拖动时由脚本写 inline 变量；
     没拖过时变量为空，回落到这里的默认值（窄屏另有媒体查询）。 */
  width: var(--side-w, 240px); flex-shrink: 0; overflow-y: auto;
  padding: var(--space-2) 0 var(--space-5);
  background: var(--color-bg-secondary);
}
.git-side.collapsed { display: none; }
.grp + .grp { margin-top: var(--space-3); }
.grp-head {
  display: flex; align-items: center; gap: var(--space-1);
  padding-right: var(--space-2);
}
.grp-toggle {
  flex: 1; min-width: 0; display: flex; align-items: center; gap: 3px;
  height: 24px; padding: 0 var(--space-2);
  background: none; border: 0; cursor: pointer; font: inherit;
  font-size: var(--text-xs); font-weight: 600; letter-spacing: 0.03em;
  /* 折叠箭头也是「这里可以点」的唯二信号之一，别用 disabled 级灰度 */
  color: var(--color-text-secondary);
}
.grp-toggle:hover { color: var(--color-text-secondary); }
.grp-n {
  margin-left: auto; font-family: var(--font-mono); font-size: var(--text-2xs); font-weight: 400;
  letter-spacing: 0; text-transform: none; color: var(--color-text-muted);
}
.grp-act {
  display: grid; place-items: center; width: 20px; height: 20px; flex-shrink: 0;
  background: none; border: 0; border-radius: var(--radius-xs); cursor: pointer;
  color: var(--color-text-muted);
}
.grp-act:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

/* 组内一条细引导线：分支组里「组头 → 条目」的从属关系靠缩进 + 这条线表达，
   没有它时组头和条目左边缘几乎对齐，扫视时容易读成同级。 */
.grp-list { list-style: none; margin: 0; padding: 0 var(--space-2) 0 var(--space-4); position: relative; }
.grp-list::before {
  content: ''; position: absolute; left: 9px; top: 2px; bottom: 2px;
  width: 1px; background: var(--color-border);
}
.grp-empty { padding: var(--space-2) var(--space-2) var(--space-3); font-size: var(--text-xs); color: var(--color-text-muted); }
.item {
  display: flex; align-items: center; gap: 5px;
  height: 24px; padding: 0 var(--space-2); border-radius: var(--radius-xs);
  font-size: var(--text-sm); color: var(--color-text-secondary); cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
/* 当前检出的分支：淡 accent 底 + 图标转 accent + 字重 700。
   只加粗时在 12px 字号下几乎看不出差别，必须有底色才能一眼定位。 */
.item.current { background: var(--color-accent-bg); color: var(--color-text-primary); font-weight: 700; }
.item.current .item-ic { color: var(--color-accent); }
.item.dim .item-name { color: var(--color-text-secondary); }
/* 选中项与全站统一：淡底 + 左缘竖条 + 常规文字色。
   原来整行文字转强调色，一列仓库名里只有它发蓝，看着像链接列表。 */
.item { position: relative; }
.item.active { background: var(--color-select-bg); color: var(--color-text-primary); }
.item.active::before {
  content: '';
  position: absolute; left: 0; top: 50%; transform: translateY(-50%);
  width: 3px; height: 16px; border-radius: var(--radius-full);
  background: var(--color-accent);
}
.item-ic { flex-shrink: 0; color: var(--color-text-muted); }
.item-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.item-cur { flex-shrink: 0; color: var(--color-accent); }
.item-remote {
  flex-shrink: 0; font-size: var(--text-2xs); color: var(--color-text-muted);
}
.item-x {
  display: none; place-items: center; width: 20px; height: 20px; flex-shrink: 0;
  background: none; border: 0; padding: 0; border-radius: var(--radius-xs);
  cursor: pointer; color: var(--color-text-muted);
}
.item:hover .item-x { display: grid; }
.item-x:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.ab { display: inline-flex; align-items: center; gap: 1px; font-family: var(--font-mono); font-size: var(--text-2xs); flex-shrink: 0; }
.ab.up { color: var(--color-warning); }
.ab.down { color: var(--color-accent); }

/* ---- 右栏 ---- */
.git-main {
  flex: 1; min-width: 0; display: flex; flex-direction: column;
  /* 提交图 lane 配色：借语义色 + 三个额外色相凑够 6 条。
     色值统一在 style.css 双主题定义，这里只做别名。 */
  --git-lane-1: var(--color-accent);
  --git-lane-2: var(--color-warning);
  --git-lane-3: var(--color-git-lane-3);
  --git-lane-4: var(--color-success);
  --git-lane-5: var(--color-git-lane-5);
  --git-lane-6: var(--color-git-lane-6);
}

/* 筛选行：紧跟工作区入口条之后。做成「一条细线」而不是输入框方框，
   是因为它属于历史列表的表头区，抢视觉权重会让上面的仓库名失焦。 */
.hist-bar {
  display: flex; align-items: center; gap: 5px; height: 28px; flex-shrink: 0;
  padding: 0 var(--space-5); border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}
.hb-ic { color: var(--color-text-muted); flex-shrink: 0; }
/* 「当前分支 / 全部」分段：历史列表的作用域开关。做成两格小药丸而不是下拉——
   只有两个值，下拉要多点一次还挡住列表。 */
.hb-scope {
  flex-shrink: 0; display: inline-flex; align-items: center; gap: 1px;
  padding: 1px; border-radius: var(--radius-xs); background: var(--color-bg-tertiary);
}
.hb-scope button {
  height: 18px; padding: 0 6px; min-width: 42px; border: 0; border-radius: var(--radius-xs);
  background: none; font: inherit; font-size: var(--text-2xs); text-align: center;
  color: var(--color-text-muted); white-space: nowrap; cursor: pointer;
}
.hb-scope button:hover { color: var(--color-text-primary); }
/* 选中格用页面底色提亮 + 一圈描边：不借 accent 色，免得跟旁边的搜索图标抢眼。 */
.hb-scope button.on {
  background: var(--color-bg-primary); color: var(--color-text-primary);
  box-shadow: 0 0 0 1px var(--color-border);
}
.hb-input {
  flex: 1; min-width: 0; height: 100%; padding: 0;
  background: none; border: 0; outline: none;
  font: inherit; font-size: var(--text-xs); color: var(--color-text-secondary);
}
.hb-input::placeholder { color: var(--color-text-disabled); }
.hb-x {
  display: grid; place-items: center; width: 16px; height: 16px; flex-shrink: 0;
  padding: 0; border: 0; border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-muted); cursor: pointer;
}
.hb-x:hover { color: var(--color-text-primary); }
.hb-n {
  flex-shrink: 0; font-family: var(--font-mono); font-size: var(--text-2xs);
  color: var(--color-text-muted);
}
/* 「全历史」徽标：告诉用户当前列表已经不是本地 200 条的筛选结果 */
.hb-mode {
  flex-shrink: 0; display: inline-flex; align-items: center; gap: 3px;
  font-size: var(--text-2xs); color: var(--color-accent);
}

/* 表头与数据行共用同一套列宽：图形列 + 内容列，时间在内容行内右对齐。
   作者 / hash 已经下沉到第二行，再切列只会把摘要压窄。 */
.commit-head, .commit-item {
  display: grid;
  grid-template-columns: var(--graph-w, 0px) minmax(0, 1fr);
  align-items: center;
  column-gap: var(--space-3);
}
.commit-head {
  height: 26px; padding: 0 var(--space-5); flex-shrink: 0;
  font-size: var(--text-xs); color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-border); background: var(--color-bg-secondary);
}
.commit-head-main { display: flex; align-items: center; }
.ch-time { margin-left: auto; }

.commit-scroll { flex: 0 0 auto; min-height: 0; overflow: auto; }
.commit-list { list-style: none; margin: 0; padding: 0; }
/* 一条提交 = 一行。参照 GitLens：摘要、分支标记、作者、时间挤在同一行，
   行高压到 28px，一屏能多看一倍提交；作者右对齐，扫读时视线不用来回跳。
   行与行之间不画分隔线：28px 的行高下，一条线会在整屏织出一片横纹，
   把提交图那几条竖线切成一段一段。相邻行靠 hover / 选中底色区分。
   表头下沿那条线保留，它是表头与内容的边界。 */
.commit-item {
  min-height: 28px; padding: 0 var(--space-5);
  font-size: var(--text-sm); cursor: pointer;
}
.commit-item:hover { background: var(--color-bg-hover); }
/* 选中态与 .qd-row.active 同一套：--color-select-bg 实底 + 3px accent 左标记。
   grid 布局里加 border-left 会让整行列宽位移，故这里用 inset 画竖条；
   实现因布局而异，但「底色 + 3px 竖条」这组信号与全局范式完全一致。 */
.commit-item.active {
  background: var(--color-select-bg);
  box-shadow: inset 3px 0 0 var(--color-accent);
}
/* 图形列铺满整行高度：SVG 才能把线从行顶连到行底。若受行 padding 限制，
   相邻两行的线之间就会露出缺口，又回到「一截一截」的老样子。 */
.col-graph { position: relative; align-self: stretch; min-height: 28px; }
.commit-graph { position: absolute; inset: 0; width: 100%; height: 100%; }
.commit-dot {
  position: absolute; top: 50%; width: 7px; height: 7px;
  border-radius: 50%; background: currentColor;
  transform: translate(-50%, -50%);
}
/* merge 提交画成空心环，与普通提交区分；内圈用面板底色把穿过的线盖掉 */
.commit-dot.merge {
  width: 9px; height: 9px; background: var(--color-bg-primary);
  border: 1.5px solid currentColor; box-sizing: border-box;
}
.commit-core { display: flex; align-items: center; gap: 6px; min-width: 0; }
/* 摘要先让位（shrink 1），分支标记与时间按内容宽度整颗保留（shrink 0） */
.subject-text {
  min-width: 0; flex: 0 1 auto;
  color: var(--color-text-primary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ref {
  flex: 0 0 auto; max-width: 130px;
  font-size: var(--text-2xs); line-height: 15px; padding: 0 5px; border-radius: var(--radius-full);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}
/* 分支标记用浅底胶囊（GitLens 那套）：一眼能认出「这个提交带着哪个分支」。
   底色只用 8.5% 的 accent-bg，比实底克制，整列连起来也不抢摘要。 */
.ref-branch { background: var(--color-accent-bg); color: var(--color-accent); font-weight: 600; }
.ref-remote { background: var(--color-bg-tertiary); color: var(--color-accent); }
.ref-tag { background: var(--color-warning-bg); color: var(--color-warning); }
.ref-more { background: var(--color-bg-tertiary); color: var(--color-text-muted); }

/* 作者靠右：摘要与分支标记留在左侧，作者与时间固定在右边缘。
   短 hash 不进列表（GitLens 也不放）——选中后详情面板里有完整 hash，
   右键菜单还留了「复制 hash」，行内再挂一颗只会把时间挤离右边缘。 */
.author-name {
  margin-left: auto; flex: 0 0 auto; max-width: 160px;
  font-size: var(--text-xs); color: var(--color-text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.commit-time {
  flex-shrink: 0;
  font-family: var(--font-mono); font-size: var(--text-2xs); color: var(--color-text-muted);
  white-space: nowrap;
}

/* ---- 工作区栏（第三列）：已暂存 / 未暂存 + 提交框 ---- */
/* 常驻而不是浮层：它与右侧 diff 是同一件事的两个视角（左边挑文件、右边看内容），
   做成抽屉就会盖住历史；固定成列以后，两边的选中状态还能互相对照。 */
.git-wt {
  width: var(--wt-w, 320px); flex-shrink: 0; display: flex; flex-direction: column; min-height: 0;
  background: var(--color-bg-secondary);
}
.wt-head {
  display: flex; align-items: center; gap: 5px; height: 30px; flex-shrink: 0;
  padding: 0 var(--space-4); border-bottom: 1px solid var(--color-border);
}
.wt-ic { flex-shrink: 0; color: var(--color-warning); }
/* 没有未提交改动时不该还是警告色：黄图标在「一切正常」的状态下读起来像出了事 */
.wt-ic.clean { color: var(--color-text-muted); }
.wt-name { font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary); }
.wt-n {
  min-width: 16px; height: 16px; padding: 0 var(--space-2); border-radius: var(--radius-full);
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--color-warning-bg); color: var(--color-warning);
  font-family: var(--font-mono); font-size: var(--text-2xs);
}
/* 「全部改动」：只看整份工作区 diff（不清空选中文件时的默认态） */
.wt-all {
  margin-left: 2px; height: 19px; padding: 0 6px; flex-shrink: 0;
  border-radius: var(--radius-xs); border: 1px solid transparent;
  background: none; cursor: pointer; font: inherit; font-size: var(--text-2xs);
  color: var(--color-text-muted);
}
.wt-all:hover { color: var(--color-text-secondary); background: var(--color-bg-hover); }
.wt-all.on { background: var(--color-accent-bg); border-color: var(--color-accent-border); color: var(--color-accent); }

/* ---- 详情 + 提交面板 ---- */
.detail-wrap { flex: 1; min-height: 0; display: flex; flex-direction: column; }
/* 工作区两组：各自一块、各自滚动，中间的分隔条拖动改上半区高度。
 *
 * 走过两版：先是「每组 max-height:44% + 组内滚动」，两组都长的时候都只剩组头；
 * 后来改成「整列一起滚」，看得全了但没法把某组临时放大，看未暂存要滚过一整段已暂存。
 * 现在是固定比例 + 可拖：默认 62% 给已暂存（马上要提交的那几条要看得全），
 * 剩下给未暂存；比例不合适直接拖，双击回默认。
 * 注意 .file-rows 自带的 flex:1 / overflow-y:auto 就够用，这里不要再覆盖成
 * flex:none + overflow:visible，否则两组会撑破自己的框。 */
.wt-panes { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.wt-pane { display: flex; flex-direction: column; min-height: 0; overflow: hidden; }
.wt-pane-top { flex: 0 0 var(--wt-top, 62%); }
.wt-pane-bottom { flex: 1 1 0; }
/* 组头里的空态：缩进对齐到组名的文字起点（16 padding + 7 点 + 5 gap），
   一眼能看出「这句是在说上面那一组」；原来齐着边框，读起来像不属于任何组。 */
.git-wt .file-state { padding: 1px var(--space-4) 10px 28px; font-size: var(--text-xs); color: var(--color-text-muted); line-height: 1.5; }
/* 两区分隔交给中间那条 .splitter 画线，组头自己不再加 border-top（会变成双线） */
/* 组头状态点：实心 = 已在暂存区，空心 = 还在工作区。
   原来两组都用 FileText 图标，一模一样，等于没有信息。 */
.grp-dot {
  flex-shrink: 0; width: 7px; height: 7px; border-radius: 50%;
  border: 1.5px solid var(--color-text-muted); box-sizing: border-box;
}
.grp-dot.on { background: var(--color-accent); border-color: var(--color-accent); }
/* 组名是分区标题，比别处的列表头重一档：muted 在深色底上几乎读不出「这是一组」 */
.git-wt .pane-head { color: var(--color-text-secondary); }
.pane-name { font-weight: 500; }
/* 尺寸与外观由 style.css 的全局 input[type=checkbox] 负责；这里只把工作区的
   勾选框抬到 15px——它在这一栏里是主操作（勾一下 = 一次 git add），14px 在
   22px 行高里偏小，鼠标要瞄一下。 */
.stage-box { margin: 0; flex-shrink: 0; width: 15px; height: 15px; cursor: pointer; }
.pane-n { font-family: var(--font-mono); font-size: var(--text-2xs); color: var(--color-text-muted); }
.pane-act {
  margin-left: auto; padding: 0 var(--space-1); border: 0; background: none;
  font: inherit; font-size: var(--text-2xs); color: var(--color-text-muted); cursor: pointer;
}
.pane-act:hover:not(:disabled) { color: var(--color-accent); }
.pane-act:disabled { opacity: var(--opacity-disabled); cursor: default; }
/* 「全部暂存」后面紧跟的丢弃按钮不该再抢一次 auto 边距，否则两个动作被推到两端 */
.pane-act + .pane-ic { margin-left: 0; }

.commit-box {
  flex-shrink: 0; display: flex; flex-direction: column; gap: var(--space-2);
  padding: var(--space-3) var(--space-4) var(--space-4);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.cb-head { display: flex; align-items: center; gap: var(--space-1); }
.cb-label { font-size: var(--text-xs); color: var(--color-text-muted); }
/* amend / AI 代笔收成输入框右上角的两个小图标：它们都是「对这段文字做点什么」，
   不占一行文字位置，提交框本身就能更大。悬停有 title，不靠自解释图标。 */
.cb-mini {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; padding: 0; border-radius: var(--radius-xs);
  border: 1px solid transparent; background: none; cursor: pointer;
  color: var(--color-text-muted);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.cb-mini:first-of-type { margin-left: auto; }
.cb-mini:hover:not(:disabled) { color: var(--color-accent); background: var(--color-accent-bg); }
.cb-mini:disabled { opacity: var(--opacity-disabled); cursor: default; }
/* amend 是开关，选中态要与悬停态一眼区分 */
.cb-mini.is-on { background: var(--color-accent-bg); border-color: var(--color-accent-border); color: var(--color-accent); }
.cb-input {
  width: 100%; flex: none; height: 74px; min-height: 74px; resize: vertical;
  max-height: 180px;
  padding: var(--space-4); border-radius: var(--radius-sm);
  border: 1px solid var(--color-border); background: var(--color-bg-primary);
  color: var(--color-text-primary); font: inherit; font-size: var(--text-sm); line-height: 1.5;
}
.cb-input:focus { outline: none; border-color: var(--color-border-focus-soft); }
.cb-input::placeholder { color: var(--color-text-disabled); }
.cb-foot { display: flex; align-items: center; gap: var(--space-3); }
/* 提示语常是整句（「暂存区为空，先勾选要提交的文件」），300px 栏里会换行，
   给个可收缩的宽度 + 行高，别把提交按钮挤成竖排。 */
.cb-hint { min-width: 0; font-size: var(--text-xs); line-height: 1.4; color: var(--color-text-muted); }
.cb-foot .qd-btn { margin-left: auto; flex-shrink: 0; height: 26px; }

/* ---- 拖动条 ---- */
/* 横向分隔条：和 .vsplit 同一套——7px 命中区、1px 视觉线，上下各 -3px 收回，
   布局占位仍是 1px。原来是 6px 实心带 + 上下两条边，等于凭空吃掉 8px 高度，
   还在两栏之间画出一道双线。 */
.splitter {
  position: relative; height: 7px; flex: 0 0 7px; margin: -3px 0; z-index: 2;
  cursor: row-resize; background: transparent;
  transition: background-color var(--transition-fast);
}
.splitter::after {
  content: ''; position: absolute; left: 0; right: 0; top: 3px; height: 1px;
  background: var(--color-border);
  transition: background-color var(--transition-fast);
}
.splitter:hover { background: var(--color-accent-bg); }
.splitter:hover::after { background: var(--color-accent-border-strong); }
.splitter.disabled { cursor: default; }
.splitter.disabled:hover { background: transparent; }
.splitter.disabled:hover::after { background: var(--color-border); }

/* 列分隔条：5px 命中区，1px 线画在正中。1px 的命中区基本拖不到，
   但把线本身做粗又会让两栏看着像被一道沟隔开。
   两侧各 -2px 外边距把多出来的 4px 收回去：命中区 5px，占位仍是 1px，
   与改前 border-right 的视觉宽度一致。不收的话三列之间会凭空多出 8px 空隙，
   整页看上去像「线变粗了」。 */
.vsplit {
  position: relative; width: 5px; flex: 0 0 5px; margin: 0 -2px; z-index: 2;
  cursor: col-resize; background: transparent;
  transition: background-color var(--transition-fast);
}
.vsplit::after {
  content: ''; position: absolute; top: 0; bottom: 0; left: 2px; width: 1px;
  background: var(--color-border);
  transition: background-color var(--transition-fast);
}
/* 悬停 / 拖动中透出 accent：拖的时候手在动、视线在另两栏，
   没有这条高亮就分不清自己抓的是哪根线。 */
.vsplit:hover, .vsplit.dragging { background: var(--color-accent-bg); }
.vsplit:hover::after, .vsplit.dragging::after { background: var(--color-accent-border-strong); }

/* ---- 详情：文件 | diff ---- */
.detail { flex: 1; min-height: 0; display: flex; }
.detail-files {
  width: var(--files-w, 248px); flex-shrink: 0; display: flex; flex-direction: column;
  /* 分隔线交给 .vsplit 画：这列在折叠 / 工作区模式下不渲染，线留在这里会变成孤立的一条 */
  background: var(--color-bg-secondary);
}
.detail-diff { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.pane-head {
  display: flex; align-items: center; gap: 5px; height: 26px; flex-shrink: 0;
  padding: 0 var(--space-4); font-size: var(--text-xs); color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-border);
}
/* 拖到组头上方：整条变 accent 底 + 内描边，明确「松手就搬到这一组」 */
.pane-head.drop-on {
  background: var(--color-accent-bg); color: var(--color-accent);
  box-shadow: inset 0 0 0 1px var(--color-accent-border);
}
.file-state { padding: var(--space-5); color: var(--color-text-muted); font-size: var(--text-sm); }
/* 详情加载失败：文案 + 重试并排，和左侧空态的灰字区分开。 */
.file-state.detail-failed { display: flex; align-items: center; gap: var(--space-3); color: var(--color-danger); }
.file-rows { list-style: none; margin: 0; padding: var(--space-1); overflow-y: auto; flex: 1; min-height: 0; }
.file-row {
  position: relative;
  display: flex; align-items: center; gap: var(--space-3);
  min-height: 22px; padding: 0 var(--space-3); border-radius: var(--radius-xs);
  font-size: var(--text-sm); cursor: pointer;
}
.file-row:hover { background: var(--color-bg-hover); }
/* 行尾的「+ / −」：悬停才出现，和勾选框是同一个动作的两个入口。
   勾选框在行首、目标小；整行扫过时手已经在右侧，这里点一下更快。 */
.file-quick {
  display: none; place-items: center; width: 18px; height: 18px; flex-shrink: 0;
  padding: 0; border: 0; border-radius: var(--radius-xs); background: none;
  color: var(--color-text-muted); cursor: pointer;
}
.file-row:hover .file-quick, .file-row:focus-within .file-quick { display: grid; }
/* 目录行同理：一键把整个目录暂存 / 取消 */
.tree-dir .file-quick { margin-left: auto; }
.tree-dir:hover .file-quick, .tree-dir:focus-within .file-quick { display: grid; }
.file-quick:hover { color: var(--color-accent); background: var(--color-accent-bg); }
.file-quick:disabled { opacity: var(--opacity-disabled); cursor: default; }
/* 目录行：只承担分组与折叠，不参与选中（点它折叠/展开） */
.tree-dir {
  display: flex; align-items: center; gap: 3px; min-height: 20px;
  padding: 0 var(--space-3); border-radius: var(--radius-xs);
  font-size: var(--text-xs); color: var(--color-text-muted); cursor: pointer; user-select: none;
}
.tree-dir:hover { background: var(--color-bg-hover); }
.td-ic { color: var(--color-text-muted); }
.td-name { font-family: var(--font-mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-row.active { background: var(--color-select-bg); }
/* 与全局 .qd-row.active 同一画法：select-bg 实底 + 3px accent 竖条；
   flex 行用 ::before 画竖条（不影响行内列宽），图标与文字都不变色。 */
.file-row.active::before {
  content: '';
  position: absolute; left: 0; top: 50%; transform: translateY(-50%);
  width: 3px; height: 16px; border-radius: var(--radius-full);
  background: var(--color-accent);
}
.file-badge {
  width: 14px; flex-shrink: 0; text-align: center;
  font-family: var(--font-mono); font-size: var(--text-2xs); color: var(--color-text-secondary);
}
.file-badge.add { color: var(--color-success); }
.file-badge.del { color: var(--color-danger); }
.file-badge.mod { color: var(--color-warning); }
.file-badge.ren { color: var(--color-accent); }
.file-badge.new { color: var(--color-success); }
.file-badge.conflict { color: var(--color-danger); }
.file-path {
  flex: 1; min-width: 0; font-family: var(--font-mono); font-size: var(--text-xs);
  color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.file-num { display: flex; gap: 3px; flex-shrink: 0; font-family: var(--font-mono); font-size: var(--text-2xs); }
.file-num b { font-weight: 500; }
.file-num .add { color: var(--color-success); }
.file-num .del { color: var(--color-danger); }

.diff-title {
  font-family: var(--font-mono); font-size: var(--text-xs); color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.diff-hash { margin-left: auto; font-family: var(--font-mono); color: var(--color-text-muted); }
.diff-reset {
  margin-left: auto; background: none; border: 0; padding: 0; cursor: pointer;
  font: inherit; font-size: var(--text-xs); color: var(--color-accent);
}
.diff-reset:hover { color: var(--color-accent-hover); }

.diff {
  margin: 0; padding: var(--space-3) 0; overflow: auto; flex: 1; min-height: 0;
  font-family: var(--font-mono); font-size: var(--text-xs); line-height: 1.6;
  color: var(--color-text-secondary); white-space: pre;
}
.diff span { display: block; padding: 0 var(--space-4); }
.diff span.add { color: var(--color-success); background: var(--color-success-bg); }
.diff span.del { color: var(--color-danger); background: var(--color-danger-bg); }
.diff span.hunk { color: var(--color-accent); background: var(--color-bg-tertiary); }
.diff span.meta { color: var(--color-text-muted); }
.diff span.trunc { color: var(--color-text-muted); }

/* ---- 并排 diff ---- */
.diff-toggle {
  display: inline-flex; align-items: center; gap: 3px;
  margin-left: auto; padding: 0 var(--space-2); border: 0; background: none; cursor: pointer;
  font: inherit; font-size: var(--text-2xs); color: var(--color-text-muted);
}
.diff-toggle:hover { color: var(--color-accent); }
.diff-toggle + .diff-reset { margin-left: var(--space-4); }
.diff-side { flex: 1; min-height: 0; overflow: auto; font-family: var(--font-mono); font-size: var(--text-xs); }
.side-block + .side-block { border-top: 1px solid var(--color-border); }
.side-file {
  position: sticky; left: 0; padding: var(--space-2) var(--space-4); font-size: var(--text-xs);
  color: var(--color-text-secondary); background: var(--color-bg-tertiary);
}
.side-table { width: 100%; border-collapse: collapse; table-layout: fixed; }
/* 行号列宽只能写在 col 上：见模板里的注释，首行是 colspan 行，靠 td 宽度定不下来。
   48px 够放 6 位数行号（11px 等宽字体，扣掉左右各 8px 内边距还有 32px）。 */
.side-table .sd-col-ln { width: 48px; }
.side-row { line-height: 1.6; }
.sd-ln {
  padding: 0 var(--space-3); text-align: right; vertical-align: top;
  color: var(--color-text-muted); background: var(--color-bg-secondary);
  border-right: 1px solid var(--color-border); user-select: none;
}
.sd-code { padding: 0 var(--space-4); white-space: pre-wrap; word-break: break-all; vertical-align: top; }
.side-table td.del { background: var(--color-danger-bg); color: var(--color-danger); }
.side-table td.add { background: var(--color-success-bg); color: var(--color-success); }
.side-table td.blank { background: var(--color-bg-secondary); }
/* 左右两栏的分界线：没有它，上下文行两边的同文会看起来像连成一片 */
.side-table td:nth-of-type(2) { border-right: 1px solid var(--color-border); }
.sd-full { padding: 0 var(--space-4); white-space: pre-wrap; }
.sd-full.hunk { color: var(--color-accent); background: var(--color-bg-tertiary); }
.sd-full.meta { color: var(--color-text-muted); }

/* ---- 多选 ---- */
.file-row.picked { background: var(--color-select-bg); }
/* picked + active 同普通 active：竖条由 .file-row.active::before 统一提供，不再另画 */
.pane-ic {
  margin-left: auto; padding: 0 var(--space-1); border: 0; background: none; cursor: pointer;
  color: var(--color-text-muted); display: inline-flex; align-items: center;
}
.pane-ic:hover { color: var(--color-accent); }
/* diff 头最左的「收起文件列」按钮：不属于右侧动作组，要抵消 margin-left:auto */
.pane-ic.lead { margin-left: 0; margin-right: 2px; }
.pane-ic.lead.on { color: var(--color-accent); background: var(--color-accent-bg); }
.pane-ic.danger:hover:not(:disabled) { color: var(--color-danger); background: var(--color-danger-bg); }
.pane-ic:disabled { opacity: var(--opacity-disabled); cursor: default; }
.pane-ic + .pane-act { margin-left: var(--space-3); }
.pick-bar {
  position: sticky; bottom: 8px; z-index: 3; margin: var(--space-2) var(--space-4) 0;
  display: flex; align-items: center; gap: var(--space-3);
  padding: 5px var(--space-4); border-radius: var(--radius-sm);
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  box-shadow: var(--shadow-pop);
}
.pb-n { font-size: var(--text-xs); color: var(--color-text-secondary); }
.pb-btn {
  padding: 0 7px; height: 20px; border-radius: var(--radius-xs); cursor: pointer;
  font: inherit; font-size: var(--text-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border: 1px solid var(--color-border);
}
.pb-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent); }
.pb-btn.danger:hover:not(:disabled) { color: var(--color-danger); border-color: var(--color-danger); }
.pb-btn:disabled { opacity: var(--opacity-disabled); cursor: default; }
.pb-x {
  margin-left: auto; display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border: 0; background: none; cursor: pointer;
  color: var(--color-text-muted); border-radius: var(--radius-xs);
}
.pb-x:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }


.pane-state {
  display: flex; align-items: center; justify-content: center; gap: var(--space-3);
  flex: 1; padding: var(--space-6); font-size: var(--text-sm); color: var(--color-text-muted);
  flex-direction: column;
}
.pane-state p { margin: 0; }
.diff-idle { color: var(--color-text-muted); }

/* ---- 状态栏 ---- */
.git-status {
  display: flex; align-items: center; gap: var(--space-5); height: 24px; flex-shrink: 0;
  padding: 0 var(--space-5); font-size: var(--text-xs);
  border-top: 1px solid var(--color-border); background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
}
.st-branch { display: inline-flex; align-items: center; gap: var(--space-2); font-family: var(--font-mono); color: var(--color-text-secondary); }
.st-dim { color: var(--color-text-muted); font-size: var(--text-xs); }
/* 工作区脏标记：一个发光的黄点 + 加粗数字。整句染黄时数字反而不突出，
   圆点是「这里有未提交的东西」的固定信号，扫一眼状态栏就能发现。 */
.st-dirty {
  display: inline-flex; align-items: center; gap: 4px;
  color: var(--color-warning); font-size: var(--text-xs);
}
.st-dirty b { font-family: var(--font-mono); font-weight: 600; font-size: var(--text-xs); }
.st-dot {
  width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0;
  background: var(--color-warning); box-shadow: 0 0 4px var(--color-warning);
}
.st-path {
  margin-left: auto; font-family: var(--font-mono); color: var(--color-text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.spin { animation: git-spin 900ms linear infinite; }
/* 转圈态挂在容器上，内部图标自动跟着转（换图标不用改 class）；用 :deep 是因为
   图标是子组件根节点，普通后代选择器在部分打包路径下拿不到它的作用域属性。 */
.is-spinning :deep(svg) { animation: git-spin 900ms linear infinite; }
@keyframes git-spin { to { transform: rotate(360deg); } }

/* ---- 右键菜单 & 弹窗 ---- */
.state-actions { display: flex; gap: var(--space-2); }

/* 菜单固定在鼠标位置，透明遮罩负责「点外面关闭」，与项目内其它浮层同一套语言 */
.ctx-backdrop { position: fixed; inset: 0; z-index: var(--z-ctx-backdrop); }
.ctx-menu {
  position: fixed; min-width: 208px; padding: var(--space-2);
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md); box-shadow: var(--shadow-lg);
  display: flex; flex-direction: column; gap: 1px;
}
.ctx-item {
  display: flex; align-items: center; height: 28px; padding: 0 var(--space-5);
  border: none; border-radius: var(--radius-sm); background: none;
  color: var(--color-text-secondary); font-size: var(--text-sm); font-family: inherit;
  text-align: left; cursor: pointer; white-space: nowrap;
}
.ctx-item:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.ctx-item.danger { color: var(--color-danger); }
.ctx-item.danger:hover:not(:disabled) { background: var(--color-danger-bg); color: var(--color-danger); }
.ctx-item:disabled { color: var(--color-text-disabled); cursor: default; }
.ctx-sep { height: 1px; margin: 3px var(--space-3); background: var(--color-border); }

/* ---- 远端管理（弹窗内） ---- */
.rm-add .qd-btn { flex-shrink: 0; height: 26px; }

/* ---- 文件历史横幅 ---- */
.fh-bar {
  display: flex; align-items: center; gap: var(--space-3); height: 28px; flex-shrink: 0;
  padding: 0 var(--space-5); background: var(--color-accent-bg);
  border-bottom: 1px solid var(--color-border); font-size: var(--text-xs); color: var(--color-accent);
}
.fh-ic { flex-shrink: 0; }
.fh-name { font-weight: 500; flex-shrink: 0; }
.fh-path {
  min-width: 0; font-family: var(--font-mono); color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.fh-n { font-family: var(--font-mono); color: var(--color-text-muted); }
.fh-x {
  margin-left: auto; display: grid; place-items: center; width: 18px; height: 18px; flex-shrink: 0;
  border: 0; background: none; border-radius: var(--radius-xs); color: inherit; cursor: pointer;
}
.fh-x:hover { background: var(--color-bg-hover); }

/* ---- 大弹窗（SSH / blame / 历史版本）---- */

/* ---- 子模块 ---- */
.sub-state.diff { color: var(--color-accent); background: var(--color-accent-bg); }

/* ---- 分支对比 ---- */
.cmp-pill.add { color: var(--color-success); background: var(--color-success-bg); }
.cmp-pill.del { color: var(--color-danger); background: var(--color-danger-bg); }

/* SSH 分区 */

/* blame：表头吸顶 + 逐行等宽 */
/* blame 行的哈希可点：跳到那次提交 */

/* 历史版本内容 */

/* 三栏（仓库 / 工作区 / 历史+diff）在窄屏先收两侧，diff 始终是最后让步的那个 */
@media (max-width: 1180px) {
  /* 只改默认值：拖过之后 inline 变量优先，用户的选择不会被响应式规则悄悄改回去 */
  .git-body { --side-w: 190px; --wt-w: 280px; --files-w: 200px; }
  .st-path { display: none; }
}
@media (max-width: 940px) {
  .git-body { --side-w: 168px; --wt-w: 248px; }
  .author-name { max-width: 84px; }
  .commit-time { font-size: var(--text-2xs); }
}
/* 窄窗：bar-left 原来 min-width:0 会被右侧 flex-shrink:0 的按钮组挤没，
   仓库名和路径缩到不可读。让工具栏换行，仓库信息独占一行，按钮组自己再换行。 */
@media (max-width: 820px) {
  .git-bar {
    flex-wrap: wrap; height: auto; min-height: 46px;
    padding-top: 6px; padding-bottom: 6px; row-gap: 6px;
  }
  .bar-left { flex: 1 1 100%; }
  .bar-actions { flex: 1 1 auto; min-width: 0; flex-wrap: wrap; }
}
</style>