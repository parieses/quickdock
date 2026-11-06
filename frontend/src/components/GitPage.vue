<script setup lang="ts">
/**
 * Git 面板（对位 SourceTree 的布局，而不是「三张卡片」）：
 *
 *   ┌ 工具栏：仓库名 + 路径 ────────────── [Fetch][Pull][刷新] ┐
 *   ├────────────┬──────────────────────────────────────────────┤
 *   │ 仓库        │ 摘要 │ 分支/标签 │ 作者 │ 时间   ← 列表表头   │
 *   │ 本地分支     │ ● 提交行 ……                                  │
 *   │ 远程分支     ├──────────────────────────────────────────────┤
 *   │ 标签        │ 改动文件（状态/路径/+N −N） │ 逐行 diff        │
 *   ├────────────┴──────────────────────────────────────────────┤
 *   │ 状态栏：分支 ↑1↓2 · 3 处改动 · E:\pp\quickdock             │
 *   └───────────────────────────────────────────────────────────┘
 *
 * 三个「为什么这样做」：
 *   1. 提交列表要有表头。没有表头时作者名与时间挤在摘要后面，用户得靠猜哪一列是什么。
 *   2. 详情是左右分栏而不是上下堆叠：文件清单与 diff 是「索引 → 内容」的关系，
 *      并排才能一边点文件一边看内容；上下堆叠时点一个文件得往下滚才能看到结果。
 *   3. 中部可拖动：看历史时想多看提交，看 diff 时想多看代码，比例只能由用户定。
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
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { navigate, pendingTarget } from '../composables/bridge'
import ConfirmDialog from './ConfirmDialog.vue'
import {
  GitListRepos, GitAddRepo, GitRemoveRepo, GitPickRepo,
  GitRepoStatus, GitBranches, GitLog, GitCommitDetail,
  GitCheckout, GitFetch, GitPull, GitPush,
  GitPickCloneParent, GitClone,
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
  FileDiff, Sparkles, Archive, KeyRound, History, Copy, Trash2, Eye,
  Search, AlertTriangle, Link2, PanelLeft, List, ListTree, Folder, Columns2, Rows3, Boxes,
} from '@lucide/vue'

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
  hash: string; short: string; author: string; email: string
  ts: number; subject: string; refs: string; graph: string
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

// ---- 远端管理 ----
const remoteOpen = ref(false)
const remotes = ref<RemoteEntry[]>([])
const remoteBusy = ref('')
const newRemote = reactive({ name: '', url: '' })

// ---- 左栏收起（看长 diff 时把横向空间让出来） ----
const sideOpen = ref(true)

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

/** 筛选只在内存里做：历史上限 200 条，再问一次 git 不会更准，还会让列表闪。 */
const shownCommits = computed(() => {
  const q = histQuery.value.trim().toLowerCase()
  if (!q) return baseCommits.value
  return baseCommits.value.filter(c =>
    c.subject.toLowerCase().includes(q) ||
    c.author.toLowerCase().includes(q) ||
    c.hash.toLowerCase().startsWith(q) ||
    (c.refs || '').toLowerCase().includes(q))
})

/** graph 列宽按最长前缀算：每行各算各的会让摘要文字左边缘参差（见模板注释）。 */
const graphCh = computed(() => {
  let n = 2
  for (const c of shownCommits.value) n = Math.max(n, c.graph.length)
  return Math.min(n + 1, 14) // 封顶：分支多到线占半屏时，摘要才是要读的东西
})

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
  navigate('environments')
}

// ---- 远端管理 ----

async function loadRemotes() {
  try {
    remotes.value = (unwrap(await GitRemotes(activePath.value)) as RemoteEntry[] | null) || []
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('git.remotes', e)
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
    toast.error(getErrorMessage(e))
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
    toast.error(getErrorMessage(e))
    logErr('git.remoteAdd', e)
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
        toast.error(getErrorMessage(e))
        logErr('git.remoteRemove', e)
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
  if (!silent) loading.value = true
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
    error.value = getErrorMessage(e)
    logErr('git.loadRepo', e)
  } finally {
    loading.value = false
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
    toast.error(getErrorMessage(e))
    logErr('git.addRepo', e)
  }
}

// ---- 克隆远端仓库 ----
const cloneOpen = ref(false)
const cloneUrl = ref('')
const cloneParent = ref('')
const cloneErr = ref('')
const cloning = ref(false)

/** 与后端 repoNameFromURL 同一规则：取路径末段、剥 .git，用于克隆前预览目标路径。 */
const cloneName = computed(() => {
  let u = cloneUrl.value.trim().replace(/\/+$/, '')
  const i = Math.max(u.lastIndexOf('/'), u.lastIndexOf(':'))
  if (i >= 0) u = u.slice(i + 1)
  return u.replace(/\.git$/, '')
})
const cloneTarget = computed(() =>
  (cloneParent.value && cloneName.value)
    ? `${cloneParent.value.replace(/[\\/]+$/, '')}\\${cloneName.value}`
    : '')

function openClone() {
  cloneUrl.value = ''
  cloneParent.value = ''
  cloneErr.value = ''
  cloneOpen.value = true
}

function closeClone() {
  if (cloning.value) return // 克隆进行中不允许误关：关了也不知道后台跑完没有
  cloneOpen.value = false
}

async function pickParent() {
  try {
    const picked = unwrap<string>(await GitPickCloneParent())
    if (picked) cloneParent.value = picked
  } catch (e) {
    logErr('git.pickCloneParent', e)
  }
}

async function doClone() {
  const url = cloneUrl.value.trim()
  if (!url) { cloneErr.value = t('git_clone_need_url'); return }
  if (!cloneParent.value) { cloneErr.value = t('git_clone_need_parent'); return }
  cloning.value = true
  cloneErr.value = ''
  try {
    // data 是后端归一化好的目标路径（Windows 分隔符由后端 filepath.Join 处理）
    const target = unwrap<string>(await GitClone(url, cloneParent.value)) || ''
    toast.success(t('git_cloned'))
    cloneOpen.value = false
    await loadRepos()
    if (target) {
      resetRepoView()
      activePath.value = target
      await loadRepo()
    }
  } catch (e) {
    // clone 的失败原因（地址不对 / 没权限 / 目录已存在）在 stderr 里，后端已透出
    cloneErr.value = getErrorMessage(e)
    logErr('git.clone', e)
  } finally {
    cloning.value = false
  }
}

async function removeRepo(r: RepoRef) {
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
    toast.error(getErrorMessage(e))
    logErr('git.removeRepo', e)
  }
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
  try {
    detail.value = unwrap<CommitDetail>(await GitCommitDetail(activePath.value, c.hash, true)) || null
    // 在「文件历史」里点提交：用户想看的就是这个文件，顺手选上，省一次点击
    const want = fileHist.value?.path
    if (want && detail.value?.files) {
      const hit = detail.value.files.find(f => blockPath(f) === want)
      if (hit) selectedFile.value = blockPath(hit)
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('git.commitDetail', e)
  }
}

// ---- 工作区：勾选即暂存 / 取消即撤出 ----

function openWorktree() {
  worktreeMode.value = true
  selectedHash.value = ''
  selectedFile.value = ''
  // 回工作区就退出「文件历史」：列表继续挂着某文件的历史、而详情在画工作区，两处对不上
  if (fileHist.value) exitFileHistory()
}

function selectWorkFile(f: WorkFile) {
  const p = f.path
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
    toast.error(getErrorMessage(e))
    logErr('git.toggleStage', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.stageAll', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.unstageAll', e)
  } finally {
    busy.value = ''
  }
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
    toast.error(getErrorMessage(e))
    logErr('git.aiMessage', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.commit', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.push', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.checkout', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.fetch', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.pull', e)
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
function placeCtx(e: MouseEvent, rest: Omit<CtxState, 'x' | 'y'>) {
  ctx.value = { x: e.clientX, y: e.clientY, ...rest }
  nextTick(() => {
    const c = ctx.value
    const el = ctxMenuEl.value
    if (!c || !el) return
    const r = el.getBoundingClientRect()
    c.x = Math.max(8, Math.min(c.x, window.innerWidth - r.width - 8))
    c.y = Math.max(8, Math.min(c.y, window.innerHeight - r.height - 8))
  })
}

function openFileCtx(e: MouseEvent, f: WorkFile, kind: 'staged' | 'unstaged') {
  selectedFile.value = f.path // 右键即选中：菜单操作的对象必须是「看到的这一行」
  placeCtx(e, { kind, file: f })
}

function openCommitCtx(e: MouseEvent, c: Commit) {
  // 右键也切到这次提交的详情：接下来不管是打标签还是回滚，都要先看清是哪一次
  if (selectedHash.value !== c.hash) void selectCommit(c)
  placeCtx(e, { kind: 'commit', commit: c })
}

function openBranchCtx(e: MouseEvent, b: BranchInfo) {
  // 右键不切分支：右键是「准备操作」，顺手切走会让后面的操作落到意料之外的分支上
  placeCtx(e, { kind: 'branch', branch: b })
}

/** 标签单独一类：它能做的事（推送/删除本地/删除远端）跟分支不一样，共用一个菜单只会互相干扰。 */
function openTagCtx(e: MouseEvent, g: BranchInfo) {
  placeCtx(e, { kind: 'tag', tag: g })
}

/** 提交详情里的文件行：历史 / blame / 「这次提交里它是什么样」都从这里进。 */
function openCommitFileCtx(e: MouseEvent, f: FileStat) {
  selectedFile.value = blockPath(f)
  placeCtx(e, { kind: 'cfile', cfile: f })
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
    toast.error(getErrorMessage(e))
    logErr('git.op', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.reveal', e)
  }
}

async function copyText(s: string) {
  try {
    await navigator.clipboard.writeText(s)
    toast.success(t('copied'))
  } catch (e) {
    toast.error(getErrorMessage(e))
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
  try {
    detail.value = unwrap<CommitDetail>(await GitStashShow(activePath.value, s.ref)) || null
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('git.stashShow', e)
  }
}

function openStashCtx(e: MouseEvent, s: StashEntry) {
  placeCtx(e, { kind: 'stash', stash: s })
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
    toast.error(getErrorMessage(e))
    logErr('git.fileHistory', e)
  }
}

function exitFileHistory() {
  fileHist.value = null
  fileCommits.value = []
}

// ---- 全历史搜索：本地筛选只覆盖已载入的 200 条，回车升格为问 git 本身 ----

async function doLogSearch() {
  const q = histQuery.value.trim()
  if (!activePath.value || q.length < 2 || searching.value) return
  searching.value = true
  try {
    searchResults.value = (unwrap<Commit[]>(await GitSearchLog(activePath.value, q, 200)) as Commit[] | null) || []
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('git.searchLog', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.restoreFile', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.archive', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.submodules', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.subUpdate', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.subSync', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.subAdd', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.sshStatus', e)
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
      try { await navigator.clipboard.writeText(r.key.publicKey) } catch { /* 剪贴板不可用时用户还能手点复制 */ }
    }
    sshStatus.value = unwrap<SSHStatus>(await GitSSHStatus()) || sshStatus.value
    toast.success(t('git_ssh_gen_done'))
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('git.sshGenerate', e)
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
    toast.error(getErrorMessage(e))
    logErr('git.remoteToSSH', e)
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

/** 作者头像：没有邮箱哈希可用，取名字首字母 + 按名字定一个三色循环的底色，
 *  让作者列可扫（同一个人同色），也不再是一整列灰字。 */
function avatarCls(name: string): string {
  let n = 0
  for (let i = 0; i < name.length; i++) n = (n + name.charCodeAt(i)) % 997
  return `av${n % 3}`
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
onUnmounted(() => { document.body.style.cursor = ''; document.body.style.userSelect = '' })

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
      '   }',
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
      <div class="bar-actions">
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
          <span v-if="status?.behind" class="btn-badge">{{ status.behind }}</span>
        </button>
        <button class="qd-btn qd-btn-ghost" :class="{ 'is-spinning': busy === 'push' }"
                :disabled="!activePath || !!busy" :aria-busy="busy === 'push'"
                :title="status?.ahead ? t('git_push_n', { n: status.ahead }) : t('git_push')" @click="doPush">
          <Loader2 v-if="busy === 'push'" :size="13" />
          <Upload v-else :size="13" />
          {{ t('git_push') }}
          <span v-if="status?.ahead" class="btn-badge">{{ status.ahead }}</span>
        </button>
        <button class="qd-btn qd-btn-ghost" :class="{ 'is-spinning': loading }" :disabled="!activePath || loading"
                :title="t('git_history')" @click="loadRepo()">
          <RefreshCw :size="13" />
        </button>
        <!-- 远端管理：多远端（origin + upstream）与地址改错，都只能在这里改 -->
        <button class="qd-btn qd-btn-ghost" :title="t('git_remotes')" :disabled="!activePath"
                @click="openRemotes">
          <Link2 :size="13" />
        </button>
        <!-- 子模块：数量少但「clone 后目录是空的」最容易让人以为仓库坏了 -->
        <button class="qd-btn qd-btn-ghost" :title="t('git_submodules')" :disabled="!activePath"
                @click="openSubmodules">
          <Boxes :size="13" />
        </button>
        <!-- SSH 密钥：生成/写 config/测连通都在这一个入口里，用完即走 -->
        <button class="qd-btn qd-btn-ghost" :title="t('git_ssh')" @click="openSSH">
          <KeyRound :size="13" />
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

    <div class="git-body">
      <!-- 左栏：仓库 / 分支 / 远程 / 标签 / 贮藏 五组 -->
      <aside :class="['git-side', { collapsed: !sideOpen }]">
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
            <li v-for="r in repos" :key="r.path" :class="['item', { active: r.path === activePath }]"
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
              <li v-for="b in branches.local" :key="b.name"
                  :class="['item', { current: b.current, 'is-spinning': isCheckingOut(b) }]"
                  @click="doCheckout(b)" @contextmenu.prevent="openBranchCtx($event, b)">
                <Loader2 v-if="isCheckingOut(b)" :size="11" class="item-ic" />
                <GitBranch v-else :size="11" class="item-ic" />
                <span class="item-name">{{ b.name }}</span>
                <span v-if="b.ahead" class="ab up">↑{{ b.ahead }}</span>
                <span v-if="b.behind" class="ab down">↓{{ b.behind }}</span>
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
              <li v-for="b in branches.remote" :key="b.name"
                  :class="['item', 'dim', { 'is-spinning': isCheckingOut(b) }]"
                  :title="t('git_checkout_remote')" @click="doCheckout(b)"
                  @contextmenu.prevent="openBranchCtx($event, b)">
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
                  @contextmenu.prevent="openTagCtx($event, g)">
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
              <li v-for="s in stashes" :key="s.ref"
                  :class="['item', 'dim', { active: selectedStash?.ref === s.ref }]"
                  :title="`${s.ref} · ${s.branch} · ${s.message}`"
                  @click="selectStash(s)"
                  @contextmenu.prevent="openStashCtx($event, s)">
                <Archive :size="11" class="item-ic" />
                <span class="item-name">{{ s.message || t('git_stash_auto') }}</span>
                <span class="item-remote">{{ s.ref.replace(/^stash@\{|\}$/g, '') }}</span>
              </li>
              <li v-if="!stashes.length" class="grp-empty">{{ t('git_stash_empty') }}</li>
            </ul>
          </div>
        </template>
      </aside>

      <!-- 右栏：提交表 + 详情。graph 列宽算在整栏上，表头与数据行才能吃同一个值。 -->
      <section ref="mainEl" class="git-main" :style="{ '--graph-ch': graphCh + 'ch' }">
        <!-- 工作区入口横在最上面：未提交的改动是「现在要处理的事」，
             排在历史列表里当一行会让人以为它也是一次提交。 -->
        <button v-if="activePath" :class="['wt-bar', { active: worktreeMode, dirty: changeCount > 0 }]"
                @click="openWorktree">
          <FileDiff :size="13" class="wt-ic" />
          <span class="wt-name">{{ t('git_worktree') }}</span>
          <span v-if="changeCount" class="wt-n">{{ changeCount }}</span>
          <span class="wt-sub">
            <template v-if="stagedFiles.length">{{ t('git_staged_n', { n: stagedFiles.length }) }}</template>
            <template v-else-if="unstagedFiles.length">{{ t('git_unstaged_n', { n: unstagedFiles.length }) }}</template>
            <template v-else>{{ t('git_clean') }}</template>
          </span>
          <ChevronRight :size="12" class="wt-arrow" />
        </button>

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
          <Search :size="11" class="hb-ic" />
          <input v-model="histQuery" class="hb-input" spellcheck="false"
                 :placeholder="t('git_filter_ph')" :title="t('git_search_hint')"
                 @keydown.enter.prevent="doLogSearch" @input="onHistInput" />
          <button v-if="histQuery" class="hb-x" :title="t('git_filter_clear')" @click="clearHistFilter">
            <X :size="10" />
          </button>
          <span v-if="searching" class="hb-mode"><Loader2 :size="10" class="spin" />{{ t('loading') }}</span>
          <span v-else-if="searchResults" class="hb-mode">{{ t('git_search_all') }}</span>
          <span class="hb-n">
            {{ shownCommits.length }}<template v-if="histQuery">/{{ baseCommits.length }}</template>
          </span>
        </div>

        <div class="commit-head">
          <span class="col-graph"></span>
          <span class="col-subject">{{ t('git_col_subject') }}</span>
          <span class="col-author">{{ t('git_col_author') }}</span>
          <span class="col-date">{{ t('git_col_date') }}</span>
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
            <li v-for="c in shownCommits" :key="c.hash"
                :class="['commit-item', { active: c.hash === selectedHash }]"
                @click="selectCommit(c)" @contextmenu.prevent="openCommitCtx($event, c)">
              <!-- graph 用等宽字体原样渲染：SourceTree 的分支线就是这套字符，不解析反而更准 -->
              <span class="col-graph commit-graph">{{ c.graph }}</span>
              <!-- 分支/标签徽章跟着摘要走：refs 只有被分支或标签指向的提交才有，
                   独立成一列时八成行是空白，摘要反被压窄；贴在这里宽度跟内容走。 -->
              <span class="col-subject commit-subject">
                <span class="subject-text" :title="c.subject">{{ c.subject }}</span>
                <span v-for="r in refList(c.refs).slice(0, 2)" :key="r"
                      :class="['ref', `ref-${refKind(r)}`]" :title="refText(r)">{{ refText(r) }}</span>
                <span v-if="refMore(c.refs)" class="ref ref-more" :title="c.refs">+{{ refMore(c.refs) }}</span>
              </span>
              <span class="col-author commit-author">
                <span :class="['avatar', avatarCls(c.author)]">{{ (c.author || '?').slice(0, 1).toUpperCase() }}</span>
                <span class="author-name">{{ c.author }}</span>
              </span>
              <span class="col-date commit-time">{{ fmtTime(c.ts) }}</span>
            </li>
          </ul>
        </div>

        <!-- 拖动条：上下比例由用户定 -->
        <div class="splitter" :class="{ disabled: detailKind === 'none' && !worktreeMode }"
             @mousedown="(detailKind !== 'none' || worktreeMode) && startSplit($event)"></div>

        <div class="detail-wrap">
          <div class="detail">
            <!-- 左：工作区是「已暂存 / 未暂存」两组（勾选框就是 git add），提交详情是一组 -->
            <div v-if="worktreeMode" class="detail-files wt-files">
              <div class="pane-head sticky">
                <FileText :size="12" />
                <span>{{ t('git_staged') }}</span>
                <span class="pane-n">{{ stagedFiles.length }}</span>
                <button class="pane-ic" :title="t('git_tree_toggle')" @click="treeMode = !treeMode">
                  <List v-if="treeMode" :size="11" />
                  <ListTree v-else :size="11" />
                </button>
                <button v-if="stagedFiles.length" class="pane-act" :disabled="!!busy" @click="unstageAll">
                  {{ t('git_unstage_all') }}
                </button>
              </div>
              <ul v-if="stagedFiles.length" class="file-rows wt-rows">
                <template v-for="row in stagedRows" :key="row.key">
                  <li v-if="row.dir" class="tree-dir"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }" @click="toggleDir(row.key)">
                    <ChevronDown v-if="!closedDirs.has(row.key)" :size="10" />
                    <ChevronRight v-else :size="10" />
                    <Folder :size="11" class="td-ic" />
                    <span class="td-name">{{ row.name }}</span>
                  </li>
                  <li v-else-if="row.file"
                      :class="['file-row', { active: selectedFile === row.file!.path, picked: picked.has('s:' + row.file!.path) }]"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }"
                      :title="row.file!.path" @click="onFileClick($event, row.file!, 'staged')"
                      @contextmenu.prevent="openFileCtx($event, row.file!, 'staged')">
                    <input class="stage-box" type="checkbox" :checked="true" :disabled="busy === 'stage'"
                           :title="t('git_unstage')" @click.stop="toggleStage(row.file!, true)" />
                    <span :class="['file-badge', statusColor(row.file!.status)]">{{ row.file!.status || 'M' }}</span>
                    <span class="file-path">{{ row.name }}</span>
                    <span v-if="row.file!.add || row.file!.del" class="file-num">
                      <b v-if="row.file!.add" class="add">+{{ row.file!.add }}</b>
                      <b v-if="row.file!.del" class="del">−{{ row.file!.del }}</b>
                    </span>
                  </li>
                </template>
              </ul>
              <p v-else class="file-state">{{ t('git_nothing_staged') }}</p>

              <div class="pane-head sticky">
                <FileText :size="12" />
                <span>{{ t('git_unstaged') }}</span>
                <span class="pane-n">{{ unstagedFiles.length }}</span>
                <button v-if="unstagedFiles.length" class="pane-act" :disabled="!!busy" @click="stageAll">
                  {{ t('git_stage_all') }}
                </button>
              </div>
              <ul v-if="unstagedFiles.length" class="file-rows wt-rows">
                <template v-for="row in unstagedRows" :key="row.key">
                  <li v-if="row.dir" class="tree-dir"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }" @click="toggleDir(row.key)">
                    <ChevronDown v-if="!closedDirs.has(row.key)" :size="10" />
                    <ChevronRight v-else :size="10" />
                    <Folder :size="11" class="td-ic" />
                    <span class="td-name">{{ row.name }}</span>
                  </li>
                  <li v-else-if="row.file"
                      :class="['file-row', { active: selectedFile === row.file!.path, picked: picked.has('u:' + row.file!.path) }]"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }"
                      :title="row.file!.path" @click="onFileClick($event, row.file!, 'unstaged')"
                      @contextmenu.prevent="openFileCtx($event, row.file!, 'unstaged')">
                    <input class="stage-box" type="checkbox" :checked="false" :disabled="busy === 'stage'"
                           :title="t('git_stage')" @click.stop="toggleStage(row.file!, false)" />
                    <span :class="['file-badge', statusColor(row.file!.status)]">{{ row.file!.status || 'M' }}</span>
                    <span class="file-path">{{ row.name }}</span>
                    <span v-if="row.file!.add || row.file!.del" class="file-num">
                      <b v-if="row.file!.add" class="add">+{{ row.file!.add }}</b>
                      <b v-if="row.file!.del" class="del">−{{ row.file!.del }}</b>
                    </span>
                  </li>
                </template>
              </ul>
              <p v-else class="file-state">{{ t('git_worktree_empty') }}</p>

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
            </div>

            <div v-else class="detail-files">
              <div class="pane-head">
                <FileText :size="12" />
                <span>{{ t('git_changed_files', { n: detail?.files?.length || 0 }) }}</span>
                <button class="pane-ic" :title="t('git_tree_toggle')" @click="treeMode = !treeMode">
                  <List v-if="treeMode" :size="11" />
                  <ListTree v-else :size="11" />
                </button>
              </div>
              <div v-if="detailKind === 'none'" class="file-state">—</div>
              <div v-else-if="!detail" class="file-state"><Loader2 :size="13" class="spin" /></div>
              <ul v-else class="file-rows">
                <template v-for="row in commitRows" :key="row.key">
                  <li v-if="row.dir" class="tree-dir"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }" @click="toggleDir(row.key)">
                    <ChevronDown v-if="!closedDirs.has(row.key)" :size="10" />
                    <ChevronRight v-else :size="10" />
                    <Folder :size="11" class="td-ic" />
                    <span class="td-name">{{ row.name }}</span>
                  </li>
                  <li v-else-if="row.file"
                      :class="['file-row', { active: selectedFile && selectedFile === blockPath(row.file!) }]"
                      :style="{ paddingLeft: 12 + row.depth * 12 + 'px' }"
                      :title="row.file!.desc" @click="selectFile(row.file!)"
                      @contextmenu.prevent="openCommitFileCtx($event, row.file!)">
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

            <div class="detail-diff">
              <div class="pane-head">
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
              <p v-else-if="worktreeMode" class="pane-state">
                {{ changeCount ? t('git_no_diff') : t('git_clean') }}
              </p>
              <p v-else-if="detailActive && detail" class="pane-state">{{ t('git_no_diff') }}</p>
              <div v-else class="pane-state diff-idle">
                <GitBranch :size="20" />
                <p>{{ activePath ? t('git_select_commit') : '' }}</p>
              </div>
            </div>
          </div>

          <!-- 提交面板：只在工作区模式出现，写信息 → 提交，AI 可以代笔 -->
          <div v-if="worktreeMode && activePath" class="commit-box">
          <div class="cb-head">
            <span class="cb-label">{{ t('git_commit_msg') }}</span>
            <button class="cb-ai" :class="{ 'is-on': amendMode }"
                    :title="t('git_amend_title')" @click="amendMode = !amendMode">
              {{ t('git_amend_toggle') }}
            </button>
            <button class="cb-ai" :class="{ 'is-spinning': aiWriting }"
                    :disabled="aiWriting || !changeCount" :title="t('git_ai_hint')" @click="aiCommitMessage">
              <Loader2 v-if="aiWriting" :size="11" />
              <Sparkles v-else :size="11" />
              {{ aiWriting ? t('git_ai_writing') : t('git_ai_write') }}
            </button>
          </div>
          <textarea v-model="commitMsg" class="cb-input" rows="2"
                    :placeholder="amendMode ? t('git_amend_ph') : t('git_commit_ph')"
                    @keydown.ctrl.enter.prevent="doCommit" @keydown.meta.enter.prevent="doCommit"></textarea>
            <div class="cb-foot">
              <span :class="['cb-hint', { warn: !stagedFiles.length && !(amendMode && commitMsg.trim()) }]">
                {{ commitHint }}
              </span>
              <button class="qd-btn qd-btn-primary" :class="{ 'is-spinning': committing }"
                      :disabled="!canCommit" @click="doCommit">
                <Loader2 v-if="committing" :size="12" />
                <Check v-else :size="12" />
                {{ amendMode ? t('git_amend_btn') : t('git_commit_btn') }}
              </button>
            </div>
          </div>
        </div>
      </section>
    </div>

    <!-- 状态栏 -->
    <footer class="git-status">
      <span class="st-branch">
        <GitBranch :size="11" />{{ status?.branch || '—' }}
        <template v-if="status?.ahead"> ↑{{ status.ahead }}</template>
        <template v-if="status?.behind"> ↓{{ status.behind }}</template>
      </span>
      <span v-if="status?.upstream" class="st-dim">{{ t('git_upstream') }} {{ status.upstream }}</span>
      <span :class="['st-dim', { 'st-warn': changeCount > 0 }]">
        {{ changeCount ? t('git_changes', { n: changeCount }) : t('git_clean') }}
      </span>
      <span v-if="busy" class="st-dim">{{ t('loading') }}</span>
      <span class="st-path" :title="activePath">{{ activePath }}</span>
    </footer>

    <!-- 远端管理：地址写错 / 有 origin + upstream 两条，都要能在这里改 -->
    <div v-if="remoteOpen" class="modal-mask" @click.self="remoteOpen = false">
      <div class="modal modal-lg">
        <header class="modal-head">
          <span>{{ t('git_remotes') }}</span>
          <button class="modal-x" @click="remoteOpen = false"><X :size="13" /></button>
        </header>
        <div class="modal-body">
          <p class="modal-hint">{{ t('git_remotes_hint') }}</p>
          <ul v-if="remotes.length" class="rm-list">
            <li v-for="r in remotes" :key="r.name" class="rm-row">
              <span class="rm-name" :title="r.pushUrl || r.url">{{ r.name }}</span>
              <span :class="['rm-tag', r.isSsh ? 'ssh' : 'https']">{{ r.isSsh ? 'SSH' : 'HTTPS' }}</span>
              <input v-model="r.url" class="modal-input rm-input" spellcheck="false"
                     :disabled="remoteBusy === r.name" @change="saveRemote(r)"
                     @keydown.enter.prevent="saveRemote(r)" />
              <button class="rm-x" :disabled="!!remoteBusy" :title="t('git_remote_remove')"
                      @click="removeRemote(r)">
                <Loader2 v-if="remoteBusy === r.name" :size="11" class="spin" />
                <Trash2 v-else :size="11" />
              </button>
            </li>
          </ul>
          <p v-else class="modal-hint">{{ t('git_remotes_none') }}</p>

          <div class="rm-add">
            <input v-model="newRemote.name" class="modal-input rm-name-in" spellcheck="false"
                   :placeholder="t('git_remote_name_ph')" />
            <input v-model="newRemote.url" class="modal-input" spellcheck="false"
                   :placeholder="t('git_remote_url_ph')" @keydown.enter.prevent="addRemote" />
            <button class="qd-btn qd-btn-primary" :disabled="!!remoteBusy" @click="addRemote">
              {{ t('git_remote_add') }}
            </button>
          </div>
        </div>
        <footer class="modal-foot">
          <button class="qd-btn" @click="remoteOpen = false">{{ t('close') }}</button>
        </footer>
      </div>
    </div>

    <!-- 子模块：clone 后目录为空、改了 .gitmodules 地址却没生效，这两件事都在这修 -->
    <div v-if="subOpen" class="modal-mask" @click.self="subOpen = false">
      <div class="modal modal-lg">
        <header class="modal-head">
          <span>{{ t('git_submodules') }}</span>
          <button class="modal-x" @click="subOpen = false"><X :size="13" /></button>
        </header>
        <div class="modal-body">
          <p class="modal-hint">{{ t('git_sub_hint') }}</p>
          <ul v-if="subs.length" class="sub-list">
            <li v-for="s in subs" :key="s.path" class="sub-row">
              <span :class="['sub-state', subStateCls(s.state)]">{{ subStateText(s.state) }}</span>
              <span class="sub-path" :title="s.path">{{ s.path }}</span>
              <span class="sub-sha mono">{{ s.sha.slice(0, 7) }}</span>
              <span v-if="s.describe" class="sub-desc" :title="s.describe">{{ s.describe }}</span>
              <button class="pb-btn" :disabled="!!subBusy" @click="subUpdate(s, false)">
                <Loader2 v-if="subBusy === s.path" :size="11" class="is-spinning" />
                <template v-else>{{ t('git_sub_update') }}</template>
              </button>
              <button class="pb-btn" :disabled="!!subBusy" :title="t('git_sub_remote_hint')"
                      @click="subUpdate(s, true)">
                <Loader2 v-if="subBusy === 'remote:' + s.path" :size="11" class="is-spinning" />
                <template v-else>{{ t('git_sub_remote') }}</template>
              </button>
            </li>
          </ul>
          <p v-else class="modal-hint">{{ t('git_sub_none') }}</p>

          <div class="rm-add">
            <input v-model="newSub.url" class="modal-input" spellcheck="false"
                   :placeholder="t('git_sub_url_ph')" />
            <input v-model="newSub.path" class="modal-input rm-name-in" spellcheck="false"
                   :placeholder="t('git_sub_path_ph')" @keydown.enter.prevent="subAdd" />
            <button class="qd-btn" :disabled="!!subBusy" @click="subAdd">{{ t('git_sub_add') }}</button>
          </div>
        </div>
        <footer class="modal-foot">
          <button class="qd-btn" :disabled="!!subBusy" @click="subSync">{{ t('git_sub_sync') }}</button>
          <button class="qd-btn qd-btn-primary" @click="subOpen = false">{{ t('close') }}</button>
        </footer>
      </div>
    </div>

    <!-- 分支对比：以别的分支为基准，当前分支多了什么、少了什么 -->
    <div v-if="compareState" class="modal-mask" @click.self="compareState = null">
      <div class="modal modal-xl">
        <header class="modal-head">
          <span>{{ t('git_compare_title', { base: compareState.base, head: compareState.head }) }}</span>
          <button class="modal-x" @click="compareState = null"><X :size="13" /></button>
        </header>
        <div class="modal-body cmp-body">
          <p v-if="compareState.loading" class="modal-hint">
            <Loader2 :size="12" class="is-spinning" />{{ t('loading') }}
          </p>
          <p v-else-if="compareState.err" class="modal-err">{{ compareState.err }}</p>
          <template v-else-if="compareState.data">
            <div class="cmp-sum">
              <span class="cmp-pill add">↑ {{ t('git_compare_ahead', { n: compareState.data.ahead }) }}</span>
              <span class="cmp-pill del">↓ {{ t('git_compare_behind', { n: compareState.data.behind }) }}</span>
              <span class="cmp-files-n">{{ t('git_compare_files', { n: compareState.data.files?.length || 0 }) }}</span>
            </div>
            <div class="cmp-cols">
              <div class="cmp-col">
                <div class="cmp-head">{{ t('git_compare_only_head', { b: compareState.head }) }}</div>
                <ul class="cmp-list">
                  <li v-for="c in compareState.data.headOnly || []" :key="c.hash" :title="c.subject">
                    <span class="cmp-h mono">{{ c.short }}</span>
                    <span class="cmp-s">{{ c.subject }}</span>
                  </li>
                  <li v-if="!compareState.data.headOnly?.length" class="cmp-empty">{{ t('git_compare_none') }}</li>
                </ul>
              </div>
              <div class="cmp-col">
                <div class="cmp-head">{{ t('git_compare_only_base', { b: compareState.base }) }}</div>
                <ul class="cmp-list">
                  <li v-for="c in compareState.data.baseOnly || []" :key="c.hash" :title="c.subject">
                    <span class="cmp-h mono">{{ c.short }}</span>
                    <span class="cmp-s">{{ c.subject }}</span>
                  </li>
                  <li v-if="!compareState.data.baseOnly?.length" class="cmp-empty">{{ t('git_compare_none') }}</li>
                </ul>
              </div>
            </div>
            <ul v-if="compareState.data.files?.length" class="cmp-file-list">
              <li v-for="f in compareState.data.files || []" :key="f.path">
                <span :class="['file-badge', statusColor(f.status)]">{{ f.status || 'M' }}</span>
                <span class="file-path">{{ f.path }}</span>
                <span class="file-num">
                  <b v-if="f.add" class="add">+{{ f.add }}</b>
                  <b v-if="f.del" class="del">−{{ f.del }}</b>
                </span>
              </li>
            </ul>
            <pre v-if="compareState.data.diff" class="cmp-diff">{{ compareState.data.diff }}</pre>
          </template>
        </div>
        <footer class="modal-foot">
          <button class="qd-btn" @click="compareState = null">{{ t('close') }}</button>
        </footer>
      </div>
    </div>

    <!-- 克隆弹窗 -->
    <div v-if="cloneOpen" class="modal-mask" @click.self="closeClone">
      <div class="modal">
        <header class="modal-head">
          <span>{{ t('git_clone') }}</span>
          <button class="modal-x" :disabled="cloning" @click="closeClone"><X :size="13" /></button>
        </header>
        <div class="modal-body">
          <label class="modal-label">{{ t('git_clone_url') }}</label>
          <input v-model="cloneUrl" class="modal-input" spellcheck="false"
                 :placeholder="t('git_clone_url_ph')" :disabled="cloning" />
          <label class="modal-label">{{ t('git_clone_parent') }}</label>
          <div class="modal-row">
            <span class="modal-path" :title="cloneParent">{{ cloneParent || t('git_clone_none') }}</span>
            <button class="qd-btn qd-btn-sm" :disabled="cloning" @click="pickParent">
              {{ t('git_clone_pick') }}
            </button>
          </div>
          <p v-if="cloneTarget" class="modal-hint">{{ t('git_clone_target') }} <span class="mono">{{ cloneTarget }}</span></p>
          <p v-if="cloning" class="modal-hint cloning">
            <Loader2 :size="12" class="spin" />{{ t('git_cloning') }}
          </p>
          <p v-if="cloneErr" class="modal-err">{{ cloneErr }}</p>
        </div>
        <footer class="modal-foot">
          <button class="qd-btn" :disabled="cloning" @click="closeClone">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" :disabled="cloning" @click="doClone">
            {{ t('git_clone_go') }}
          </button>
        </footer>
      </div>
    </div>

    <!-- 右键菜单：透明遮罩铺满，点空白/滚轮都收起（fixed 定位不会跟着列表滚） -->
    <Teleport to="body">
      <div v-if="ctx" class="ctx-backdrop" @click="closeCtx" @contextmenu.prevent="closeCtx"
           @wheel.prevent="closeCtx">
        <div ref="ctxMenuEl" class="ctx-menu" :style="{ left: ctx.x + 'px', top: ctx.y + 'px' }"
             @click.stop @contextmenu.stop>
          <template v-for="a in ctxActions" :key="a.id">
            <div v-if="a.sep" class="ctx-sep"></div>
            <button v-else :class="['ctx-item', { danger: a.danger }]" :disabled="a.disabled"
                    @click="a.run(); closeCtx()">{{ a.label }}</button>
          </template>
        </div>
      </div>
    </Teleport>

    <!-- 命名输入：新建分支 / 打标签 / 重命名分支 -->
    <div v-if="promptState" class="modal-mask" @click.self="promptState = null">
      <div class="modal modal-sm">
        <header class="modal-head">
          <span>{{ promptState.title }}</span>
          <button class="modal-x" @click="promptState = null"><X :size="13" /></button>
        </header>
        <div class="modal-body">
          <label class="modal-label">{{ promptState.label }}</label>
          <input ref="promptInput" v-model="promptState.text" class="modal-input" spellcheck="false"
                 @keydown.enter.prevent="onPromptOk" />
        </div>
        <footer class="modal-foot">
          <button class="qd-btn" @click="promptState = null">{{ t('cancel') }}</button>
          <button class="qd-btn qd-btn-primary" @click="onPromptOk">{{ t('confirm') }}</button>
        </footer>
      </div>
    </div>

    <!-- SSH 密钥：已有密钥 / 生成并写 config / 测试 / 仓库切 SSH -->
    <div v-if="sshOpen" class="modal-mask" @click.self="closeSSH">
      <div class="modal modal-lg">
        <header class="modal-head">
          <span>{{ t('git_ssh') }}</span>
          <button class="modal-x" :disabled="sshBusy === 'gen'" @click="closeSSH"><X :size="13" /></button>
        </header>
        <div class="modal-body ssh-body">
          <section class="ssh-sec">
            <div class="ssh-sec-head">
              <span>{{ t('git_ssh_keys') }}</span>
              <span class="ssh-dir" :title="sshStatus?.configPath">{{ sshStatus?.dir }}</span>
            </div>
            <ul v-if="sshStatus?.keys?.length" class="ssh-keys">
              <li v-for="k in sshStatus.keys" :key="k.path" class="ssh-key">
                <span class="sk-name">{{ k.name }}</span>
                <span class="sk-type">{{ k.type }}</span>
                <span class="sk-fp" :title="k.fingerprint">{{ k.fingerprint || '—' }}</span>
                <button class="qd-btn qd-btn-sm" @click="copyText(k.publicKey)">{{ t('git_ssh_copy_pub') }}</button>
              </li>
            </ul>
            <p v-else class="modal-hint">{{ t('git_ssh_none') }}</p>
          </section>

          <section class="ssh-sec">
            <div class="ssh-sec-head"><span>{{ t('git_ssh_gen') }}</span></div>
            <div class="ssh-form">
              <label class="modal-label">{{ t('git_ssh_host') }}</label>
              <select v-model="sshHost" class="modal-input">
                <option value="github.com">github.com</option>
                <option value="gitlab.com">gitlab.com</option>
                <option value="gitee.com">gitee.com</option>
              </select>
              <label class="modal-label">{{ t('git_ssh_comment') }}</label>
              <input v-model="sshComment" class="modal-input" spellcheck="false"
                     :placeholder="`quickdock@${sshHost}`" />
              <label class="modal-label">{{ t('git_ssh_pass') }}</label>
              <input v-model="sshPass" type="password" class="modal-input" spellcheck="false"
                     :placeholder="t('git_ssh_pass_ph')" />
            </div>
            <p class="modal-hint">{{ t('git_ssh_hint', { host: sshHost }) }}</p>
            <div class="ssh-actions">
              <button class="qd-btn qd-btn-primary" :class="{ 'is-spinning': sshBusy === 'gen' }"
                      :disabled="!!sshBusy" @click="doSSHGenerate">
                <Loader2 v-if="sshBusy === 'gen'" :size="12" />
                <KeyRound v-else :size="12" />
                {{ sshBusy === 'gen' ? t('git_ssh_working') : t('git_ssh_gen') }}
              </button>
              <button class="qd-btn" :class="{ 'is-spinning': sshBusy === 'test' }"
                      :disabled="!!sshBusy" @click="doSSHTest">
                <Loader2 v-if="sshBusy === 'test'" :size="12" />
                <History v-else :size="12" />
                {{ t('git_ssh_test') }}
              </button>
            </div>
            <pre v-if="sshOut" :class="['ssh-out', { ok: sshTestOk }]">{{ sshOut }}</pre>
            <div v-if="sshGenerated" class="ssh-pub">
              <label class="modal-label">{{ t('git_ssh_pub') }}</label>
              <pre class="ssh-pub-text">{{ sshGenerated.publicKey }}</pre>
            </div>
          </section>

          <section v-if="activePath" class="ssh-sec">
            <div class="ssh-sec-head"><span>{{ t('git_ssh_remote') }}</span></div>
            <p class="modal-hint mono ssh-remote">{{ sshRemote?.url || t('git_ssh_no_remote') }}</p>
            <div class="ssh-actions">
              <button class="qd-btn" :disabled="!!sshBusy || !sshRemote?.url || sshRemote?.isSsh"
                      @click="remoteToSSH">
                {{ sshRemote?.isSsh ? t('git_ssh_already') : t('git_ssh_to_ssh') }}
              </button>
            </div>
          </section>
        </div>
        <footer class="modal-foot">
          <button class="qd-btn" :disabled="sshBusy === 'gen'" @click="closeSSH">{{ t('close') }}</button>
        </footer>
      </div>
    </div>

    <!-- 逐行归属：一行一句话「谁、什么时候、哪次提交写的」 -->
    <div v-if="blameState" class="modal-mask" @click.self="blameState = null">
      <div class="modal modal-lg">
        <header class="modal-head">
          <span>{{ t('git_ctx_blame') }}</span>
          <span class="bm-file" :title="blameState.file">{{ blameState.file }}</span>
          <button class="modal-x" @click="blameState = null"><X :size="13" /></button>
        </header>
        <div class="modal-body blame-body">
          <p v-if="blameState.loading" class="modal-hint row-hint">
            <Loader2 :size="12" class="spin" />{{ t('loading') }}
          </p>
          <p v-else-if="blameState.err" class="modal-err">{{ blameState.err }}</p>
          <template v-else-if="blameState.data">
            <div class="blame-head">
              <span class="bl-c">{{ t('git_col_branch') }}</span>
              <span class="bl-a">{{ t('git_col_author') }}</span>
              <span class="bl-t">{{ t('git_col_date') }}</span>
              <span class="bl-x">{{ t('git_blame_code') }}</span>
            </div>
            <div class="blame-list">
              <div v-for="l in (blameState.data.lines || [])" :key="l.line" class="blame-row">
                <span class="bl-c mono bl-link" :title="t('git_blame_goto')" @click="jumpToCommit(l.hash)">{{ l.short }}</span>
                <span class="bl-a">{{ l.author }}</span>
                <span class="bl-t mono">{{ fmtTime(l.ts) }}</span>
                <span class="bl-x mono">{{ l.text }}</span>
              </div>
            </div>
            <p v-if="blameState.data.truncated" class="modal-hint">{{ t('git_blame_truncated') }}</p>
          </template>
        </div>
      </div>
    </div>

    <!-- 历史版本：某次提交里这个文件长什么样；确认后可以整体恢复到这个版本 -->
    <div v-if="fileView" class="modal-mask" @click.self="fileView = null">
      <div class="modal modal-lg">
        <header class="modal-head">
          <span>{{ t('git_ctx_view_version') }}</span>
          <span class="bm-file mono">{{ fileView.file }} @ {{ (fileView.rev || '').slice(0, 8) }}</span>
          <button class="modal-x" @click="fileView = null"><X :size="13" /></button>
        </header>
        <div class="modal-body">
          <p v-if="fileView.loading" class="modal-hint row-hint">
            <Loader2 :size="12" class="spin" />{{ t('loading') }}
          </p>
          <p v-else-if="fileView.err" class="modal-err">{{ fileView.err }}</p>
          <template v-else-if="fileView.data">
            <p v-if="fileView.data.binary" class="modal-hint">{{ t('git_binary_file') }}</p>
            <pre v-else class="file-view">{{ fileView.data.content }}</pre>
            <p v-if="fileView.data.truncated" class="modal-hint">{{ t('git_truncated') }}</p>
          </template>
        </div>
        <footer class="modal-foot">
          <button v-if="fileView.data && !fileView.loading && !fileView.err"
                  class="qd-btn qd-btn-primary" @click="confirmRestoreVersion">
            {{ t('git_restore_version') }}
          </button>
          <button class="qd-btn" @click="fileView = null">{{ t('close') }}</button>
        </footer>
      </div>
    </div>

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
  height: 46px; padding: 0 var(--space-6); flex-shrink: 0;
  border-bottom: 1px solid var(--color-border); background: var(--color-bg-secondary);
  box-shadow: 0 1px 0 var(--color-hairline) inset;
}
.bar-left { display: flex; align-items: center; gap: 6px; min-width: 0; }
/* 收起左栏：看长 diff 时把 240px 让出来，按钮留在工具栏最左，收了也能立刻展开 */
.bar-toggle {
  display: grid; place-items: center; width: 22px; height: 22px; flex-shrink: 0;
  margin-right: 2px; padding: 0; border: 0; border-radius: var(--radius-xs);
  background: none; color: var(--color-text-muted); cursor: pointer;
}
.bar-toggle:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.bar-icon { color: var(--color-accent); flex-shrink: 0; }
.bar-repo { font-size: 12px; font-weight: 600; color: var(--color-text-primary); flex-shrink: 0; }
.bar-path {
  font-size: 11px; color: var(--color-text-muted); font-family: var(--font-mono);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0;
}
.bar-dirty { font-size: 11px; color: var(--color-warning); flex-shrink: 0; }
.bar-actions { display: flex; align-items: center; gap: var(--space-2); flex-shrink: 0; }
.bar-actions .qd-btn { height: 26px; }
.btn-badge {
  display: inline-flex; align-items: center; justify-content: center;
  min-width: 15px; height: 15px; padding: 0 3px; border-radius: var(--radius-full);
  background: var(--color-accent); color: var(--color-accent-text);
  font-family: var(--font-mono); font-size: 9px;
}

.git-error {
  margin: 0; padding: 6px var(--space-5); font-size: 12px; flex-shrink: 0;
  color: var(--color-danger); background: var(--color-danger-bg);
  border-bottom: 1px solid var(--color-border);
}

/* 缺 git 的横幅：不用 .git-error 的红条，因为它不是「出错了」，而是「缺前置条件」——
   红条读起来像可以忽略的报错，这里必须带上安装按钮才算把话说完整。 */
.git-warn {
  display: flex; align-items: center; gap: var(--space-4); flex-shrink: 0;
  padding: 9px var(--space-5);
  background: var(--color-warning-bg); border-bottom: 1px solid var(--color-border);
}
.gw-ic { color: var(--color-warning); flex-shrink: 0; }
.gw-text { display: flex; flex-direction: column; gap: 1px; min-width: 0; flex: 1; }
.gw-text b { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }
.gw-text span { font-size: 11px; color: var(--color-text-muted); }
.git-warn .qd-btn { height: 24px; flex-shrink: 0; }

/* ---- 主体 ---- */
.git-body { flex: 1; display: flex; min-height: 0; }

/* ---- 左栏 ---- */
.git-side {
  width: 240px; flex-shrink: 0; overflow-y: auto;
  padding: var(--space-2) 0 var(--space-5);
  border-right: 1px solid var(--color-border); background: var(--color-bg-secondary);
}
.git-side.collapsed { display: none; }
.grp + .grp { margin-top: 6px; }
.grp-head {
  display: flex; align-items: center; gap: 2px;
  padding-right: var(--space-2);
}
.grp-toggle {
  flex: 1; min-width: 0; display: flex; align-items: center; gap: 3px;
  height: 24px; padding: 0 var(--space-2);
  background: none; border: 0; cursor: pointer; font: inherit;
  font-size: 11px; font-weight: 600; letter-spacing: 0.03em;
  color: var(--color-text-muted); text-transform: uppercase;
}
.grp-toggle:hover { color: var(--color-text-secondary); }
.grp-n {
  margin-left: auto; font-family: var(--font-mono); font-size: 10px; font-weight: 400;
  letter-spacing: 0; text-transform: none; color: var(--color-text-disabled);
}
.grp-act {
  display: grid; place-items: center; width: 20px; height: 20px; flex-shrink: 0;
  background: none; border: 0; border-radius: var(--radius-xs); cursor: pointer;
  color: var(--color-text-muted);
}
.grp-act:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.grp-list { list-style: none; margin: 0; padding: 0 var(--space-2); }
.grp-empty { padding: 4px var(--space-2) 6px; font-size: 11px; color: var(--color-text-disabled); }
.item {
  display: flex; align-items: center; gap: 5px;
  height: 24px; padding: 0 var(--space-2); border-radius: var(--radius-xs);
  font-size: 12px; color: var(--color-text-secondary); cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.item.current { color: var(--color-text-primary); font-weight: 600; }
.item.dim .item-name { color: var(--color-text-muted); }
/* 选中项与全站统一：淡底 + 左缘竖条 + 常规文字色。
   原来整行文字转强调色，一列仓库名里只有它发蓝，看着像链接列表。 */
.item { position: relative; }
.item.active { background: var(--color-accent-bg); color: var(--color-text-primary); }
.item.active::before {
  content: '';
  position: absolute; left: 0; top: 50%; transform: translateY(-50%);
  width: 2px; height: 14px; border-radius: var(--radius-full);
  background: var(--color-accent);
}
.item.active .item-ic { color: var(--color-accent); }
.item-ic { flex-shrink: 0; color: var(--color-text-muted); }
.item-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.item-cur { flex-shrink: 0; color: var(--color-accent); }
.item-remote {
  flex-shrink: 0; font-size: 10px; color: var(--color-text-disabled);
}
.item-x {
  display: none; place-items: center; width: 16px; height: 16px; flex-shrink: 0;
  background: none; border: 0; padding: 0; border-radius: var(--radius-xs);
  cursor: pointer; color: var(--color-text-muted);
}
.item:hover .item-x { display: grid; }
.item-x:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.ab { font-family: var(--font-mono); font-size: 10px; flex-shrink: 0; }
.ab.up { color: var(--color-warning); }
.ab.down { color: var(--color-accent); }

/* ---- 右栏 ---- */
.git-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }

/* 筛选行：紧跟工作区入口条之后。做成「一条细线」而不是输入框方框，
   是因为它属于历史列表的表头区，抢视觉权重会让上面的仓库名失焦。 */
.hist-bar {
  display: flex; align-items: center; gap: 5px; height: 27px; flex-shrink: 0;
  padding: 0 var(--space-5); border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}
.hb-ic { color: var(--color-text-disabled); flex-shrink: 0; }
.hb-input {
  flex: 1; min-width: 0; height: 100%; padding: 0;
  background: none; border: 0; outline: none;
  font: inherit; font-size: 11px; color: var(--color-text-secondary);
}
.hb-input::placeholder { color: var(--color-text-disabled); }
.hb-x {
  display: grid; place-items: center; width: 16px; height: 16px; flex-shrink: 0;
  padding: 0; border: 0; border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-muted); cursor: pointer;
}
.hb-x:hover { color: var(--color-text-primary); }
.hb-n {
  flex-shrink: 0; font-family: var(--font-mono); font-size: 10px;
  color: var(--color-text-disabled);
}
/* 「全历史」徽标：告诉用户当前列表已经不是本地 200 条的筛选结果 */
.hb-mode {
  flex-shrink: 0; display: inline-flex; align-items: center; gap: 3px;
  font-size: 10px; color: var(--color-accent);
}

/* 表头与数据行共用同一套列宽，否则两行的摘要列会错开半格。
   没有独立的「分支」列：徽章挂在摘要单元格内（见模板注释）。 */
.commit-head, .commit-item {
  display: grid;
  grid-template-columns: var(--graph-ch, 3ch) minmax(0, 1fr) 128px 74px;
  align-items: center; gap: var(--space-4);
}
.commit-head {
  height: 28px; padding: 0 var(--space-5); flex-shrink: 0;
  font-size: 11px; color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-border); background: var(--color-bg-secondary);
}
.col-date { text-align: right; }
.col-graph { font-family: var(--font-mono); }

.commit-scroll { flex: 0 0 auto; min-height: 0; overflow: auto; }
.commit-list { list-style: none; margin: 0; padding: 0; }
.commit-item {
  height: 28px; padding: 0 var(--space-5);
  font-size: 12px; cursor: pointer;
}
.commit-item:hover { background: var(--color-bg-hover); }
/* 选中用内阴影画左标记：grid 布局里加 border-left 会让整行列宽位移 2px */
.commit-item.active {
  background: var(--color-accent-bg);
  box-shadow: inset 2px 0 0 var(--color-accent);
}
/* graph 前缀必须等宽 + 保留空格，否则线会歪 */
.commit-graph {
  font-size: 12px; line-height: 1; color: var(--color-accent);
  white-space: pre; overflow: hidden;
}
/* 摘要单元格是 flex：文字先让位（shrink 1），徽章按内容宽度整颗保留（shrink 0） */
.commit-subject {
  display: flex; align-items: center; gap: 6px;
  min-width: 0; overflow: hidden;
  color: var(--color-text-primary);
}
.subject-text {
  min-width: 0; flex: 0 1 auto;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.commit-item.active .commit-subject { color: var(--color-text-primary); }
.ref {
  flex: 0 0 auto; max-width: 140px;
  font-size: 10px; line-height: 15px; padding: 0 6px; border-radius: var(--radius-full);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}
.ref-branch { background: var(--color-accent); color: var(--color-accent-text); font-weight: 500; }
.ref-remote { background: var(--color-accent-bg); color: var(--color-accent); }
.ref-tag { background: var(--color-warning-bg); color: var(--color-warning); }
.ref-more { background: var(--color-bg-tertiary); color: var(--color-text-muted); }

/* 作者：首字母头像 + 名字，同一个人永远同色，作者列从「一排灰字」变成可扫的列 */
.commit-author { display: flex; align-items: center; gap: 6px; min-width: 0; }
.avatar {
  width: 16px; height: 16px; border-radius: 50%; flex-shrink: 0;
  display: grid; place-items: center;
  font-size: 9px; font-weight: 600; line-height: 1;
}
.avatar.av0 { background: var(--color-accent-bg); color: var(--color-accent); }
.avatar.av1 { background: var(--color-success-bg); color: var(--color-success); }
.avatar.av2 { background: var(--color-warning-bg); color: var(--color-warning); }
.author-name {
  min-width: 0; font-size: 11px; color: var(--color-text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.commit-time {
  font-family: var(--font-mono); font-size: 11px; color: var(--color-text-disabled);
  text-align: right; white-space: nowrap;
}

/* ---- 工作区入口条（在提交表上方，独立于历史列表） ---- */
.wt-bar {
  display: flex; align-items: center; gap: var(--space-3);
  height: 30px; flex-shrink: 0; padding: 0 var(--space-5);
  background: var(--color-bg-secondary); border: 0;
  border-bottom: 1px solid var(--color-border);
  font: inherit; font-size: 12px; color: var(--color-text-secondary);
  text-align: left; cursor: pointer;
  box-shadow: inset 2px 0 0 transparent;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.wt-bar:hover { background: var(--color-bg-hover); }
.wt-bar.active {
  background: var(--color-accent-bg); color: var(--color-text-primary);
  box-shadow: inset 2px 0 0 var(--color-accent);
}
.wt-ic { flex-shrink: 0; color: var(--color-text-muted); }
.wt-bar.dirty .wt-ic { color: var(--color-warning); }
.wt-name { font-weight: 500; }
.wt-n {
  min-width: 16px; height: 16px; padding: 0 4px; border-radius: var(--radius-full);
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--color-warning-bg); color: var(--color-warning);
  font-family: var(--font-mono); font-size: 10px;
}
.wt-sub { margin-left: auto; font-size: 11px; color: var(--color-text-muted); }
.wt-arrow { opacity: .45; flex-shrink: 0; }

/* ---- 详情 + 提交面板 ---- */
.detail-wrap { flex: 1; min-height: 0; display: flex; flex-direction: column; }
/* 工作区两组：整列一起滚。
 *
 * 原先这里是「每组各自 max-height:44% + 组内滚动」，两组会互相挤压：暂存与未暂存
 * 都有文件时，两条 list 都被压到只剩组头，必须先拖动分隔条才能看到文件——
 * 「未暂存看不到文件列表，要拉一下才行」就是这么来的。
 * 改成整列滚动后两组按内容展开，滚一下就能到未暂存那组。 */
.wt-files { overflow-y: auto; }
.wt-files .wt-rows { flex: 0 0 auto; max-height: none; overflow: visible; }
.wt-files .file-rows { flex: none; overflow: visible; }
.wt-files .file-state { padding: 3px var(--space-4) 7px; font-size: 11px; color: var(--color-text-disabled); }
/* 组头吸顶：滚到未暂存那组时「已暂存」的标题已经滚出去，
   但组名是这两组的坐标系，不该跟着滚没。 */
.pane-head.sticky { position: sticky; top: 0; z-index: 1; background: var(--color-bg-secondary); }
.wt-rows + .pane-head { border-top: 1px solid var(--color-border); }
.stage-box {
  width: 12px; height: 12px; margin: 0; flex-shrink: 0;
  accent-color: var(--color-accent); cursor: pointer;
}
.pane-n { font-family: var(--font-mono); font-size: 10px; color: var(--color-text-muted); }
.pane-act {
  margin-left: auto; padding: 0 2px; border: 0; background: none;
  font: inherit; font-size: 10px; color: var(--color-text-muted); cursor: pointer;
}
.pane-act:hover:not(:disabled) { color: var(--color-accent); }
.pane-act:disabled { opacity: .5; cursor: default; }

.commit-box {
  flex-shrink: 0; display: flex; flex-direction: column; gap: var(--space-2);
  padding: var(--space-3) var(--space-4) var(--space-4);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.cb-head { display: flex; align-items: center; gap: var(--space-3); }
.cb-label { font-size: 11px; color: var(--color-text-muted); }
.cb-ai {
  margin-left: auto; display: inline-flex; align-items: center; gap: 4px;
  height: 20px; padding: 0 7px; border-radius: var(--radius-xs);
  border: 1px solid var(--color-border-light); background: var(--color-bg-primary);
  color: var(--color-accent); font: inherit; font-size: 11px; cursor: pointer;
  transition: background-color var(--transition-fast), border-color var(--transition-fast);
}
.cb-ai:hover:not(:disabled) { background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.cb-ai:disabled { opacity: .5; cursor: default; }
/* amend 开关是常驻按钮，选中态要与 hover 态一眼区分开 */
.cb-ai.is-on { background: var(--color-accent-bg); border-color: var(--color-accent-border); color: var(--color-accent); }
.cb-input {
  width: 100%; resize: vertical; min-height: 46px; max-height: 160px;
  padding: 7px 9px; border-radius: var(--radius-sm);
  border: 1px solid var(--color-border); background: var(--color-bg-primary);
  color: var(--color-text-primary); font: inherit; font-size: 12px; line-height: 1.5;
}
.cb-input:focus { outline: none; border-color: var(--color-border-focus-soft); }
.cb-input::placeholder { color: var(--color-text-disabled); }
.cb-foot { display: flex; align-items: center; gap: var(--space-3); }
.cb-hint { font-size: 11px; color: var(--color-text-muted); }
.cb-hint.warn { color: var(--color-warning); }
.cb-foot .qd-btn { margin-left: auto; height: 26px; }

/* ---- 拖动条 ---- */
.splitter {
  height: 6px; flex-shrink: 0; cursor: row-resize;
  background: var(--color-bg-secondary);
  border-top: 1px solid var(--color-border); border-bottom: 1px solid var(--color-border);
  transition: background-color var(--transition-fast);
}
.splitter:hover { background: var(--color-accent-bg); }
.splitter.disabled { cursor: default; }
.splitter.disabled:hover { background: var(--color-bg-secondary); }

/* ---- 详情：文件 | diff ---- */
.detail { flex: 1; min-height: 0; display: flex; }
.detail-files {
  width: 248px; flex-shrink: 0; display: flex; flex-direction: column;
  border-right: 1px solid var(--color-border); background: var(--color-bg-secondary);
}
.detail-diff { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.pane-head {
  display: flex; align-items: center; gap: 5px; height: 26px; flex-shrink: 0;
  padding: 0 var(--space-4); font-size: 11px; color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-border);
}
.file-state { padding: var(--space-5); color: var(--color-text-muted); font-size: 12px; }
.file-rows { list-style: none; margin: 0; padding: 2px; overflow-y: auto; flex: 1; min-height: 0; }
.file-row {
  display: flex; align-items: center; gap: var(--space-3);
  min-height: 22px; padding: 0 var(--space-3); border-radius: var(--radius-xs);
  font-size: 12px; cursor: pointer;
}
.file-row:hover { background: var(--color-bg-hover); }
/* 目录行：只承担分组与折叠，不参与选中（点它折叠/展开） */
.tree-dir {
  display: flex; align-items: center; gap: 3px; min-height: 20px;
  padding: 0 var(--space-3); border-radius: var(--radius-xs);
  font-size: 11px; color: var(--color-text-muted); cursor: pointer; user-select: none;
}
.tree-dir:hover { background: var(--color-bg-hover); }
.td-ic { color: var(--color-text-disabled); }
.td-name { font-family: var(--font-mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-row.active { background: var(--color-accent-bg); }
.file-badge {
  width: 14px; flex-shrink: 0; text-align: center;
  font-family: var(--font-mono); font-size: 10px; color: var(--color-text-secondary);
}
.file-badge.add { color: var(--color-success); }
.file-badge.del { color: var(--color-danger); }
.file-badge.mod { color: var(--color-warning); }
.file-badge.ren { color: var(--color-accent); }
.file-badge.new { color: var(--color-success); }
.file-badge.conflict { color: var(--color-danger); }
.file-path {
  flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.file-num { display: flex; gap: 3px; flex-shrink: 0; font-family: var(--font-mono); font-size: 10px; }
.file-num b { font-weight: 500; }
.file-num .add { color: var(--color-success); }
.file-num .del { color: var(--color-danger); }

.diff-title {
  font-family: var(--font-mono); font-size: 11px; color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.diff-hash { margin-left: auto; font-family: var(--font-mono); color: var(--color-text-disabled); }
.diff-reset {
  margin-left: auto; background: none; border: 0; padding: 0; cursor: pointer;
  font: inherit; font-size: 11px; color: var(--color-accent);
}
.diff-reset:hover { color: var(--color-accent-hover); }

.diff {
  margin: 0; padding: var(--space-3) 0; overflow: auto; flex: 1; min-height: 0;
  font-family: var(--font-mono); font-size: 11px; line-height: 1.6;
  color: var(--color-text-secondary); white-space: pre;
}
.diff span { display: block; padding: 0 var(--space-4); }
.diff span.add { color: var(--color-success); background: var(--color-success-bg); }
.diff span.del { color: var(--color-danger); background: var(--color-danger-bg); }
.diff span.hunk { color: var(--color-accent); background: var(--color-bg-tertiary); }
.diff span.meta { color: var(--color-text-disabled); }
.diff span.trunc { color: var(--color-text-muted); }

/* ---- 并排 diff ---- */
.diff-toggle {
  display: inline-flex; align-items: center; gap: 3px;
  margin-left: auto; padding: 0 4px; border: 0; background: none; cursor: pointer;
  font: inherit; font-size: 10px; color: var(--color-text-muted);
}
.diff-toggle:hover { color: var(--color-accent); }
.diff-toggle + .diff-reset { margin-left: 8px; }
.diff-side { flex: 1; min-height: 0; overflow: auto; font-family: var(--font-mono); font-size: 11px; }
.side-block + .side-block { border-top: 1px solid var(--color-border); }
.side-file {
  position: sticky; left: 0; padding: 4px var(--space-4); font-size: 11px;
  color: var(--color-text-secondary); background: var(--color-bg-tertiary);
}
.side-table { width: 100%; border-collapse: collapse; table-layout: fixed; }
.side-row { line-height: 1.6; }
.sd-ln {
  width: 44px; padding: 0 6px; text-align: right; vertical-align: top;
  color: var(--color-text-disabled); background: var(--color-bg-secondary);
  border-right: 1px solid var(--color-border); user-select: none;
}
.sd-code { padding: 0 8px; white-space: pre-wrap; word-break: break-all; vertical-align: top; }
.side-table td.del { background: var(--color-danger-bg); color: var(--color-danger); }
.side-table td.add { background: var(--color-success-bg); color: var(--color-success); }
.side-table td.blank { background: var(--color-bg-secondary); }
/* 左右两栏的分界线：没有它，上下文行两边的同文会看起来像连成一片 */
.side-table td:nth-of-type(2) { border-right: 1px solid var(--color-border); }
.sd-full { padding: 0 var(--space-4); white-space: pre-wrap; }
.sd-full.hunk { color: var(--color-accent); background: var(--color-bg-tertiary); }
.sd-full.meta { color: var(--color-text-disabled); }

/* ---- 多选 ---- */
.file-row.picked { background: var(--color-accent-bg); }
.file-row.picked.active { box-shadow: inset 2px 0 0 var(--color-accent); }
.pane-ic {
  margin-left: auto; padding: 0 2px; border: 0; background: none; cursor: pointer;
  color: var(--color-text-muted); display: inline-flex; align-items: center;
}
.pane-ic:hover { color: var(--color-accent); }
.pane-ic + .pane-act { margin-left: 6px; }
.pick-bar {
  position: sticky; bottom: 8px; z-index: 3; margin: 4px 8px 0;
  display: flex; align-items: center; gap: 6px;
  padding: 5px 8px; border-radius: var(--radius-sm);
  background: var(--color-bg-elevated); border: 1px solid var(--color-border);
  box-shadow: var(--shadow-pop);
}
.pb-n { font-size: 11px; color: var(--color-text-secondary); }
.pb-btn {
  padding: 0 7px; height: 20px; border-radius: var(--radius-xs); cursor: pointer;
  font: inherit; font-size: 11px;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border: 1px solid var(--color-border);
}
.pb-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent); }
.pb-btn.danger:hover:not(:disabled) { color: var(--color-danger); border-color: var(--color-danger); }
.pb-btn:disabled { opacity: .5; cursor: default; }
.pb-x {
  margin-left: auto; display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border: 0; background: none; cursor: pointer;
  color: var(--color-text-muted); border-radius: var(--radius-xs);
}
.pb-x:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }


.pane-state {
  display: flex; align-items: center; justify-content: center; gap: var(--space-3);
  flex: 1; padding: var(--space-6); font-size: 12px; color: var(--color-text-muted);
  flex-direction: column;
}
.pane-state p { margin: 0; }
.diff-idle { color: var(--color-text-disabled); }

/* ---- 状态栏 ---- */
.git-status {
  display: flex; align-items: center; gap: var(--space-5); height: 24px; flex-shrink: 0;
  padding: 0 var(--space-5); font-size: 11px;
  border-top: 1px solid var(--color-border); background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
}
.st-branch { display: inline-flex; align-items: center; gap: 4px; font-family: var(--font-mono); color: var(--color-text-secondary); }
.st-dim { color: var(--color-text-muted); font-size: 11px; }
.st-warn { color: var(--color-warning); }
.st-path {
  margin-left: auto; font-family: var(--font-mono); color: var(--color-text-disabled);
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
.ctx-backdrop { position: fixed; inset: 0; z-index: 900; }
.ctx-menu {
  position: fixed; min-width: 208px; padding: 4px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: var(--radius-md); box-shadow: var(--shadow-lg);
  display: flex; flex-direction: column; gap: 1px;
}
.ctx-item {
  display: flex; align-items: center; height: 27px; padding: 0 10px;
  border: none; border-radius: var(--radius-sm); background: none;
  color: var(--color-text-secondary); font-size: 12px; font-family: inherit;
  text-align: left; cursor: pointer; white-space: nowrap;
}
.ctx-item:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.ctx-item.danger { color: var(--color-danger); }
.ctx-item.danger:hover:not(:disabled) { background: var(--color-danger-bg); color: var(--color-danger); }
.ctx-item:disabled { color: var(--color-text-disabled); cursor: default; }
.ctx-sep { height: 1px; margin: 3px 6px; background: var(--color-border); }

.modal-sm { width: 380px; }

.modal-mask {
  position: fixed; inset: 0; z-index: 60;
  display: grid; place-items: center;
  background: var(--color-bg-overlay);
}
.modal {
  width: 460px; max-width: calc(100vw - 48px);
  background: var(--color-surface-elevated);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}
.modal-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 10px var(--space-5); font-size: 13px; font-weight: 600;
  color: var(--color-text-primary); border-bottom: 1px solid var(--color-border);
}
.modal-x {
  display: grid; place-items: center; width: 20px; height: 20px;
  background: none; border: 0; border-radius: var(--radius-xs); cursor: pointer;
  color: var(--color-text-muted);
}
.modal-x:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.modal-body { padding: var(--space-5); display: flex; flex-direction: column; gap: 6px; }
.modal-label { font-size: 11px; color: var(--color-text-muted); }
.modal-input {
  height: 30px; padding: 0 9px; font-size: 12px; font-family: var(--font-mono);
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); color: var(--color-text-primary); outline: none;
}
.modal-input:focus { border-color: var(--color-border-focus-soft); }
.modal-row { display: flex; align-items: center; gap: var(--space-2); }
.modal-path {
  flex: 1; min-width: 0; height: 28px; line-height: 28px; padding: 0 9px;
  font-size: 12px; font-family: var(--font-mono);
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.modal-hint { margin: 0; font-size: 11px; color: var(--color-text-muted); overflow-wrap: anywhere; }
.modal-hint.cloning { display: flex; align-items: center; gap: 5px; color: var(--color-accent); }
.modal-hint .mono { font-family: var(--font-mono); }
.modal-err { margin: 0; font-size: 11px; color: var(--color-danger); overflow-wrap: anywhere; }
.modal-foot {
  display: flex; justify-content: flex-end; gap: var(--space-2);
  padding: 10px var(--space-5); border-top: 1px solid var(--color-border);
}

/* ---- 远端管理（弹窗内） ---- */
.rm-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.rm-row { display: flex; align-items: center; gap: var(--space-3); }
.rm-name {
  width: 62px; flex-shrink: 0; font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.rm-tag {
  flex-shrink: 0; font-size: 9px; line-height: 15px; padding: 0 5px;
  border-radius: var(--radius-xs); font-family: var(--font-mono);
}
.rm-tag.https { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
.rm-tag.ssh { background: var(--color-success-bg); color: var(--color-success); }
.rm-input { flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 11px; }
.rm-x {
  display: grid; place-items: center; width: 22px; height: 22px; flex-shrink: 0;
  padding: 0; border: 0; border-radius: var(--radius-xs);
  background: none; color: var(--color-text-muted); cursor: pointer;
}
.rm-x:hover:not(:disabled) { color: var(--color-danger); background: var(--color-danger-bg); }
.rm-x:disabled { opacity: .5; cursor: default; }
.rm-add {
  display: flex; align-items: center; gap: var(--space-3);
  margin-top: var(--space-3); padding-top: var(--space-4);
  border-top: 1px dashed var(--color-border);
}
.rm-name-in { width: 62px; flex: 0 0 62px; }
.rm-add .qd-btn { flex-shrink: 0; height: 26px; }

/* ---- 文件历史横幅 ---- */
.fh-bar {
  display: flex; align-items: center; gap: var(--space-3); height: 28px; flex-shrink: 0;
  padding: 0 var(--space-5); background: var(--color-accent-bg);
  border-bottom: 1px solid var(--color-border); font-size: 11px; color: var(--color-accent);
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
.modal-lg { width: 640px; max-height: 82vh; display: flex; flex-direction: column; }
.modal-lg .modal-body { overflow-y: auto; }
.modal-xl { width: 860px; max-height: 84vh; display: flex; flex-direction: column; }
.modal-xl .modal-body { overflow-y: auto; }

/* ---- 子模块 ---- */
.sub-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
.sub-row { display: flex; align-items: center; gap: var(--space-3); }
.sub-state {
  flex-shrink: 0; width: 62px; font-size: 10px; text-align: center; line-height: 16px;
  border-radius: var(--radius-xs); background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
}
.sub-state.ok { color: var(--color-success); background: var(--color-success-bg); }
.sub-state.init { color: var(--color-warning); background: var(--color-warning-bg); }
.sub-state.diff { color: var(--color-accent); background: var(--color-accent-bg); }
.sub-state.conflict { color: var(--color-danger); background: var(--color-danger-bg); }
.sub-path {
  flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 11px; color: var(--color-text-primary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.sub-sha { flex-shrink: 0; font-size: 10px; color: var(--color-text-disabled); }
.sub-desc {
  flex-shrink: 0; max-width: 110px; font-size: 10px; color: var(--color-text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

/* ---- 分支对比 ---- */
.cmp-body { display: flex; flex-direction: column; gap: var(--space-4); }
.cmp-sum { display: flex; align-items: center; gap: var(--space-3); }
.cmp-pill {
  font-size: 11px; padding: 1px 7px; border-radius: 999px;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.cmp-pill.add { color: var(--color-success); background: var(--color-success-bg); }
.cmp-pill.del { color: var(--color-danger); background: var(--color-danger-bg); }
.cmp-files-n { font-size: 11px; color: var(--color-text-muted); }
.cmp-cols { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: var(--space-4); }
.cmp-col { min-width: 0; border: 1px solid var(--color-border); border-radius: var(--radius-sm); }
.cmp-head {
  padding: 4px var(--space-3); font-size: 11px; color: var(--color-text-muted);
  background: var(--color-bg-secondary); border-bottom: 1px solid var(--color-border);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.cmp-list { list-style: none; margin: 0; padding: 2px; max-height: 180px; overflow-y: auto; }
.cmp-list li {
  display: flex; align-items: baseline; gap: var(--space-3);
  padding: 2px var(--space-2); font-size: 11px; border-radius: var(--radius-xs);
}
.cmp-list li:hover { background: var(--color-bg-hover); }
.cmp-h { flex-shrink: 0; font-size: 10px; color: var(--color-text-disabled); }
.cmp-s { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cmp-empty { color: var(--color-text-disabled); }
.cmp-file-list { list-style: none; margin: 0; padding: 0; max-height: 160px; overflow-y: auto; }
.cmp-file-list li { display: flex; align-items: center; gap: var(--space-3); padding: 2px var(--space-2); font-size: 11px; }
.cmp-diff {
  margin: 0; padding: var(--space-3); max-height: 260px; overflow: auto;
  font-family: var(--font-mono); font-size: 11px; line-height: 1.5;
  color: var(--color-text-secondary); background: var(--color-bg-secondary);
  border-radius: var(--radius-sm); white-space: pre;
}
.bm-file {
  flex: 1; min-width: 0; margin-left: var(--space-3); font-size: 11px; font-weight: 400;
  color: var(--color-text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.row-hint { display: flex; align-items: center; gap: 5px; }
.mono { font-family: var(--font-mono); }

/* SSH 分区 */
.ssh-body { gap: var(--space-4); }
.ssh-sec {
  display: flex; flex-direction: column; gap: 6px;
  padding-bottom: var(--space-4); border-bottom: 1px solid var(--color-border);
}
.ssh-sec:last-child { border-bottom: 0; padding-bottom: 0; }
.ssh-sec-head {
  display: flex; align-items: baseline; gap: var(--space-3);
  font-size: 12px; font-weight: 600; color: var(--color-text-primary);
}
.ssh-dir {
  margin-left: auto; font-family: var(--font-mono); font-size: 10px; font-weight: 400;
  color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ssh-keys { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; }
.ssh-key {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 3px 6px; border-radius: var(--radius-xs); font-size: 11px;
}
.ssh-key:hover { background: var(--color-bg-hover); }
.sk-name { flex-shrink: 0; font-family: var(--font-mono); color: var(--color-text-primary); }
.sk-type { flex-shrink: 0; font-family: var(--font-mono); font-size: 10px; color: var(--color-text-muted); }
.sk-fp {
  flex: 1; min-width: 0; font-family: var(--font-mono); font-size: 10px;
  color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ssh-form { display: grid; grid-template-columns: 104px minmax(0, 1fr); align-items: center; gap: 6px var(--space-3); }
.ssh-form .modal-input { height: 28px; }
.ssh-actions { display: flex; gap: var(--space-2); }
.ssh-out {
  margin: 0; padding: 7px 9px; max-height: 130px; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-muted); white-space: pre-wrap; overflow-wrap: anywhere;
}
.ssh-out.ok { color: var(--color-success); }
.ssh-pub-text {
  margin: 0; padding: 7px 9px; max-height: 92px; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-xs); font-family: var(--font-mono); font-size: 10px; line-height: 1.5;
  color: var(--color-text-secondary); white-space: pre-wrap; overflow-wrap: anywhere;
}
.ssh-remote { font-size: 11px; }

/* blame：表头吸顶 + 逐行等宽 */
.blame-body { gap: 0; padding: 0; }
.blame-head, .blame-row {
  display: grid; grid-template-columns: 68px 100px 78px minmax(0, 1fr);
  gap: var(--space-3); align-items: center; padding: 0 var(--space-4);
}
.blame-head {
  position: sticky; top: 0; height: 24px; flex-shrink: 0;
  font-size: 10px; letter-spacing: .03em; text-transform: uppercase; color: var(--color-text-muted);
  background: var(--color-bg-secondary); border-bottom: 1px solid var(--color-border);
}
.blame-list { overflow: auto; max-height: 58vh; }
.blame-row { height: 20px; font-size: 11px; }
.blame-row:hover { background: var(--color-bg-hover); }
.bl-c { color: var(--color-accent); }
/* blame 行的哈希可点：跳到那次提交 */
.bl-link { cursor: pointer; text-decoration: underline dotted; text-underline-offset: 2px; }
.bl-link:hover { text-decoration-style: solid; }
.bl-a { color: var(--color-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bl-t { color: var(--color-text-disabled); }
.bl-x { color: var(--color-text-secondary); white-space: pre; overflow: hidden; text-overflow: ellipsis; }

/* 历史版本内容 */
.file-view {
  margin: 0; padding: var(--space-3) var(--space-4); max-height: 62vh; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono); font-size: 11px; line-height: 1.6;
  color: var(--color-text-secondary); white-space: pre;
}

@media (max-width: 1080px) {
  .git-side { width: 186px; }
  .commit-head, .commit-item { grid-template-columns: var(--graph-ch, 3ch) minmax(0, 1fr) 0 68px; }
  .col-author { display: none; }
  .detail-files { width: 200px; }
  .st-path { display: none; }
}
</style>
