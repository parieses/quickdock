<script setup lang="ts">
/**
 * ScriptPage —— 脚本任务面板（青龙式签到/抓取脚本的统一入口）
 *
 * 与「定时任务」的分工：本页管**脚本本身**（代码、文件、变量、运行历史），
 * 定时只负责「什么时候跑」。两边靠 scheduled_tasks.action='script' 对接，
 * target 存脚本 id，所以改脚本不用改任务。
 *
 * 编辑器复用 ConfigEditor（CodeMirror 6）：传 filename 让它按键扩展名选语言，
 * 与笔记/环境配置共用同一套高亮与快捷键，不再养第二个编辑器。
 *
 * 日志两条路：
 *   - 正在跑：订阅 quickdock:script:log（后端 100ms 合批）实时追加；
 *   - 跑完了：点历史里那一条，走 GetScriptRunLog 读日志文件尾部。
 * 两条路都渲染在同一个面板里，用户不需要知道区别。
 */
import PageHeader from './PageHeader.vue'
import QdModal from './QdModal.vue'
import QdSwitch from './QdSwitch.vue'
import QdSearchBox from './QdSearchBox.vue'
import ConfigEditor from './ConfigEditor.vue'
import { KEEP_ALIVE } from '../pages/keepAlive'
defineOptions({ name: KEEP_ALIVE.script })
import { pendingCreateScript, pendingOpenScriptId } from '../composables/bridge'
import { computed, onMounted, onBeforeUnmount, onActivated, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import {
  Plus, Play, Square, Save, FolderOpen, Trash2, RefreshCw, Search, Pin, PinOff,
  FileCode2, FilePlus2, X, LoaderCircle, PackagePlus, History, Wand2, Copy,
  Webhook, Download, Import, Eye, EyeOff, Check, AlertTriangle, File as FileIcon,
  Clock, BookOpen, ChevronDown, ChevronRight,
} from '@lucide/vue'
import {
  ListScripts, GetScript, CreateScript, CreateScriptWithFiles, UpdateScript, DeleteScript, SetScriptPin,
  OpenScriptDir, ReadScriptFile, WriteScriptFile, CreateScriptFile, DeleteScriptFile,
  RunScript, CancelScriptRun, ListScriptRuns, GetScriptRunLog, GetScriptStats,
  InstallDeps, ListScriptEnvs, SaveScriptEnv, RevealScriptEnv, DeleteScriptEnv,
  ExportScript, ImportScript, DeleteScriptRuns, HealScriptRun, ApplyScriptPatch,
  StartTrigger, StopTrigger, TriggerStatus, RotateTriggerToken, TestWatchPath,
  CopyScript, TrustScript, GetScriptBindings, SearchScriptLogs,
} from '../../bindings/quickdock/services/script/scriptservice'
import { SelectLocalFile, SelectSavePath } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { copyToClipboard } from '../utils/clipboard'
// 首次使用教程的地址与「设置 → 关于」「工作空间空状态」共用一份（utils/siteLinks）
import { openExternal } from '../utils/externalLinks'
import { SITE_GUIDE_URL } from '../utils/siteLinks'

const { t } = useI18n()
const { success: toastOK, error: toastErr, confirm: toastConfirm } = useToast()

// ---------- 类型（与 internal/db/script.go 的 JSON 标签一一对应）----------
interface ScriptRec {
  id: string; name: string; description: string; lang: string; entry: string
  interpreter: string; defaultArgs: string; timeoutSec: number; concurrency: number
  autoInstall: boolean; pinned: boolean; mcpAllowed: boolean; tags: string
  runCount: number; failCount: number; lastRun: string; lastStatus: string
  lastDurationMs: number; sort: number; watchPath: string
  createdAt: string; updatedAt: string
  // 列表接口附加：定时绑定与信任状态（见 Go 侧 ScriptListItem）
  trusted?: boolean; scheduleCount?: number; nextRun?: string
}
interface ScriptFile { path: string; size: number; isDir: boolean; modified: string; entry: boolean }
interface ScriptEnvRow {
  id: string; scriptId: string; key: string; value: string
  secret: boolean; remark: string; sort: number
}
interface ScriptRunRow {
  id: string; scriptId: string; scriptName: string; trigger: string; status: string
  exitCode: number; durationMs: number; logPath: string; summary: string
  aiPatch: string; startedAt: string; finishedAt: string
}
interface ScriptStats { total: number; ok: number; fail: number; avgMs: number; last7ok: number; last7total: number }
interface DepsInfo {
  python: boolean; node: boolean; go?: boolean; php?: boolean
  fileName: string; installed?: boolean; stale?: boolean
}
interface ScriptBinding {
  id: string; name: string; scheduleKind: string; expr: string
  enabled: boolean; nextRun: string; lastRun: string; lastStatus: string
}
interface LogHit { runId: string; line: number; text: string; time: string }
interface TriggerInfo { enabled: boolean; running: boolean; port: number; token: string; url: string }
/** 与 Go 侧 script.ScriptInput 同构（Wails 生成的是 $models.ScriptInput，不从这里导出）。 */
interface ScriptForm {
  id: string; name: string; description: string; lang: string; entry: string
  interpreter: string; defaultArgs: string; timeoutSec: number; concurrency: number
  autoInstall: boolean; pinned: boolean; mcpAllowed: boolean; tags: string
  sort: number; watchPath: string
}
interface PatchInfo { diagnosis: string; fix: string; path: string; content: string; model: string }

const LANGS = ['python', 'javascript', 'shell', 'go', 'php', 'custom'] as const

/** 语言显示名。下拉里直接铺 "javascript" 太生硬，也容易和「文件后缀」混淆。 */
const LANG_TEXT: Record<string, string> = {
  python: 'Python',
  javascript: 'Node.js',
  shell: 'Shell',
  go: 'Go',
  php: 'PHP',
}
function langText(l: string) {
  if (l === 'custom') return t('scr_langCustom')
  return LANG_TEXT[l] || l
}

// ---------- 列表 ----------
const scripts = ref<ScriptRec[]>([])
const loading = ref(false)
const keyword = ref('')
/** 语言筛选（空 = 全部）。脚本一多，找「那个 python 的」比搜名字快。 */
const langFilter = ref('')
/** 只看最近一次失败的：每天巡检时只想看「哪些挂了」。 */
const onlyFailed = ref(false)
const currentId = ref('')
const current = computed(() => scripts.value.find(s => s.id === currentId.value) || null)

/** 列表至少成功拉过一次。没有它，首帧 scripts 还是空数组时会把欢迎页闪出来。 */
const listReady = ref(false)
/** 一条脚本都没有：左右两栏合并成全屏引导页，而不是左边喊「还没有」、右边喊「去左边选」 */
const isEmpty = computed(() => listReady.value && scripts.value.length === 0)
const hasFilter = computed(() => !!keyword.value.trim() || !!langFilter.value || onlyFailed.value)
function clearFilters() {
  keyword.value = ''
  langFilter.value = ''
  onlyFailed.value = false
}

const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  let list = scripts.value.slice()
  if (langFilter.value) list = list.filter(s => s.lang === langFilter.value)
  if (onlyFailed.value) {
    list = list.filter(s => s.lastStatus && s.lastStatus !== 'ok' && s.lastStatus !== 'running')
  }
  if (kw) {
    list = list.filter(s =>
      s.name.toLowerCase().includes(kw) || s.tags.toLowerCase().includes(kw) ||
      s.description.toLowerCase().includes(kw))
  }
  // 置顶在前，其余按最近一次运行时间倒序：脚本列表的价值几乎全在「最近跑过的」。
  return list.sort((a, b) => {
    if (a.pinned !== b.pinned) return a.pinned ? -1 : 1
    return (b.lastRun || b.createdAt).localeCompare(a.lastRun || a.createdAt)
  })
})

async function loadScripts(keep = true) {
  loading.value = true
  try {
    scripts.value = unwrap<ScriptRec[]>(await ListScripts()) || []
    if (keep && currentId.value && !scripts.value.some(s => s.id === currentId.value)) {
      currentId.value = ''
    }
  } catch (e) {
    logErr('script.list', e)
    toastErr(getErrorMessage(e))
  } finally {
    // 失败也算「拉过一次」：否则首屏会停在「左边空、右边让去左边选」的悖论里
    listReady.value = true
    loading.value = false
  }
}

// ---------- 选中脚本：文件 / 编辑器 / 变量 / 历史 ----------
const files = ref<ScriptFile[]>([])
const deps = ref<DepsInfo>({ python: false, node: false, go: false, php: false, fileName: '' })
const activeFile = ref('')
const fileText = ref('')
const fileLang = ref('python')
const fileDirty = ref(false)
const fileLoading = ref(false)
/** 打开文件失败的原因（原位显示，而不是只弹一次 toast 留下空白编辑区） */
const fileReadErr = ref('')
/** 切脚本时正在拉文件列表：这一拍 activeFile 还是空的，不能让界面闪「没有文件」 */
const detailLoading = ref(false)
/** 编辑器的「已保存基线」：保存成功后由这里刷新，ConfigEditor 内部的脏标记跟着它走。 */
const fileEditorKey = ref(0)
const editorRef = ref<InstanceType<typeof ConfigEditor> | null>(null)

async function selectScript(id: string) {
  if (id === currentId.value) return
  if (fileDirty.value && !(await toastConfirm(t('scr_discardConfirm')))) return
  currentId.value = id
  tab.value = 'code'
  fileDirty.value = false
  fileReadErr.value = ''
  activeFile.value = ''
  detailLoading.value = true
  try {
    await Promise.all([reloadFiles(), loadEnvs(), loadRuns(), loadStats(), loadBindings()])
  } finally {
    detailLoading.value = false
  }
}

async function reloadFiles(preferPath = '') {
  if (!currentId.value) return
  try {
    const data = unwrap<{ script: ScriptRec; files: ScriptFile[]; deps: DepsInfo; trusted: boolean; running: number }>(
      await GetScript(currentId.value))
    if (data) {
      files.value = (data.files || []).filter(f => !f.isDir)
      deps.value = data.deps || { python: false, node: false, go: false, php: false, fileName: '' }
      // 后端默认不返回 trusted 时按可信处理：老库/老绑定下不该突然拦人。
      trusted.value = data.trusted !== false
      // 页面重新打开时脚本可能已经在跑（定时或从别处触发）：后端给的是实例
      // 计数，用它把列表上的小圆点补上，不必等下一次事件。
      if ((data.running ?? 0) > 0) markRunning(currentId.value, true)
      // 目标是入口文件、或上次打开的那个还在：优先恢复上次的，避免每次切回都跳到入口。
      const want = preferPath || activeFile.value
      const hit = files.value.find(f => f.path === want) || files.value.find(f => f.entry) || files.value[0]
      if (hit && hit.path !== activeFile.value) await openFile(hit.path)
      else if (hit && !fileText.value) await openFile(hit.path)
    }
  } catch (e) {
    logErr('script.files', e)
    toastErr(getErrorMessage(e))
  }
}

/**
 * 只刷新文件树与依赖状态，绝不动编辑器内容。
 *
 * 用在哪：装依赖跑完（deps 的 stale 标记要消掉）、外部改了文件（AI 建脚本/写文件、
 * 用户用外部编辑器保存）。刷新整份 detail（reloadFiles）会在某些分支里重新 openFile，
 * 把用户正在敲的内容换掉——这正是「AI 改了下别处，我这边草稿没了」的成因。
 */
async function refreshFilesAndDeps() {
  if (!currentId.value) return
  try {
    const data = unwrap<{ script: ScriptRec; files: ScriptFile[]; deps: DepsInfo; trusted: boolean }>(
      await GetScript(currentId.value))
    if (!data) return
    const next = (data.files || []).filter(f => !f.isDir)
    files.value = next
    deps.value = data.deps || { python: false, node: false, go: false, php: false, fileName: '' }
    trusted.value = data.trusted !== false
    // 当前打开的文件被删掉了（AI 重构、用户删文件）：换到入口，别让编辑器
    // 停在一个已经不存在的路径上——下次保存会把它原地重建出来。
    if (activeFile.value && !next.some(f => f.path === activeFile.value)) {
      const hit = next.find(f => f.entry) || next[0]
      if (hit) await openFile(hit.path)
    }
  } catch (e) {
    logErr('script.refresh', e)
  }
}

async function openFile(path: string) {
  if (!currentId.value) return
  if (fileDirty.value && path !== activeFile.value && !(await toastConfirm(t('scr_discardConfirm')))) return
  fileLoading.value = true
  fileReadErr.value = ''
  try {
    const data = unwrap<{ content: string; lang: string }>(
      await ReadScriptFile(currentId.value, path))
    activeFile.value = path
    fileText.value = data?.content ?? ''
    fileLang.value = data?.lang || 'text'
    fileDirty.value = false
    // 换文件必须重建编辑器：CM 实例的 value 由内部管理，改 props 不会重置撤销栈，
    // 上一文件的撤销记录会串到新文件里。
    fileEditorKey.value++
  } catch (e) {
    // 读失败不能只是弹个 toast 就把编辑区留白：用户会以为「编辑器坏了/不能写」。
    // 记下来在编辑区原位显示原因 + 重试，文件树仍然可用。
    logErr('script.openFile', e)
    fileReadErr.value = getErrorMessage(e)
    toastErr(fileReadErr.value)
  } finally {
    fileLoading.value = false
  }
}

/** 读文件失败后重试当前文件（错误态里的按钮） */
function retryOpenFile() {
  if (activeFile.value) void openFile(activeFile.value)
  else void reloadFiles()
}

/**
 * 目录里一个文件都没有（入口被删/外部清空）时补建入口文件并直接打开。
 * 没有这一步，用户点进脚本只看到一块空编辑区，敲进去的字也存不下去
 * （saveFile 在没有 activeFile 时是空转的）。
 */
async function createEntryFile() {
  if (!currentId.value) return
  const entry = current.value?.entry || 'main.py'
  try {
    unwrap(await CreateScriptFile(currentId.value, entry, ''))
    await reloadFiles(entry)
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

function onFileInput(v: string) {
  fileText.value = v
  fileDirty.value = true
}

async function saveFile(): Promise<boolean> {
  if (!currentId.value || !activeFile.value || !fileDirty.value) return true
  try {
    unwrap(await WriteScriptFile(currentId.value, activeFile.value, fileText.value))
    fileDirty.value = false
    editorRef.value?.markSaved()
    toastOK(t('scr_saved'))
    return true
  } catch (e) {
    toastErr(getErrorMessage(e))
    return false
  }
}

// 当前文件对应的编辑器文件名提示（CM 靠扩展名选语言高亮）。
const editorFilename = computed(() => activeFile.value || current.value?.entry || 'main.py')

// ---------- 运行 ----------
const runningRunId = ref('')
const logLines = ref<{ stream: string; text: string }[]>([])
const logPanelOpen = ref(true)
/** 外部导入的脚本要确认一次才能手动运行（后端也会拦，这里是前置提示）。 */
const trusted = ref(true)
/** 本次运行的临时参数（JSON 数组字符串，空 = 用脚本设置里的默认参数）。 */
const runArgs = ref('')
/** 日志过滤：关键字 + 只看 stderr。大输出下这两个比「滚动找」快得多。 */
const logFilter = ref('')
const logErrOnly = ref(false)
/** 自动跟随到底部。用户往上翻日志时应停住，否则新行一来就被拽回底部。 */
const logFollow = ref(true)
/**
 * 正在跑的脚本 id 集合（含定时任务触发的）。
 * 列表上的状态点取自 last_status，那是「上一次的结果」——正在跑的时候它还是
 * 上一次的绿点，会让人以为已经跑完了。
 */
const runningIds = ref<Set<string>>(new Set())
function markRunning(id: string, on: boolean) {
  if (!id) return
  const next = new Set(runningIds.value)
  if (on) next.add(id)
  else next.delete(id)
  runningIds.value = next
}
/** 正在看的历史日志（切历史条目时把日志面板切到静态内容）。 */
const viewingRunId = ref('')

const running = computed(() => !!runningRunId.value)

async function runScript() {
  if (!currentId.value) return
  // 外部导入的脚本第一次手动运行前确认一次：跑脚本等于以当前用户身份执行
  // 任意代码，zip 里完全可能是一行删库命令。
  if (!trusted.value && !(await trustCurrent())) return
  // 改了代码直接点运行，跑的是磁盘上的旧版本——这是「明明改了却没生效」最
  // 常见的来源。先落盘，保存失败就不跑（跑了也只会得到上一次的行为）。
  if (fileDirty.value && !(await saveFile())) return
  try {
    logLines.value = []
    viewingRunId.value = ''
    logPanelOpen.value = true
    const res = unwrap<{ runId: string }>(await RunScript(currentId.value, runArgs.value.trim()))
    if (res?.runId) {
      runningRunId.value = res.runId
      markRunning(currentId.value, true)
    }
  } catch (e) {
    // 最常见的失败是「已在运行中」：原样把后端文案给用户，比「运行失败」有用。
    toastErr(getErrorMessage(e))
  }
}

/** 确认信任当前脚本。返回是否已确认（用户取消或失败返回 false）。 */
async function trustCurrent(): Promise<boolean> {
  if (!currentId.value) return false
  if (!(await toastConfirm(t('scr_trustConfirm', { name: current.value?.name || '' })))) return false
  try {
    unwrap(await TrustScript(currentId.value))
    trusted.value = true
    return true
  } catch (e) {
    toastErr(getErrorMessage(e))
    return false
  }
}

async function stopRun() {
  if (!runningRunId.value) return
  try {
    unwrap(await CancelScriptRun(runningRunId.value))
    toastOK(t('scr_stopping'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function installDeps() {
  if (!currentId.value) return
  try {
    logLines.value = []
    logPanelOpen.value = true
    const res = unwrap<{ runId: string }>(await InstallDeps(currentId.value))
    if (res?.runId) runningRunId.value = res.runId
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

/** 外部改动（AI 写文件 / 外部编辑器）落到当前打开的文件时，静默同步内容。
 *
 *  只在「本页没有未保存改动」时同步：脏写时用户手里那份才是他要的，把磁盘版
 *  灌回去等于把他刚敲的字扔掉。本页自己保存也会广播这个事件，所以还要比一次
 *  内容——一样就什么都不做，免得白白重置光标与撤销栈。 */
async function syncActiveFile(path: string) {
  if (!currentId.value || !path || path !== activeFile.value || fileDirty.value) return
  try {
    const data = unwrap<{ content: string; lang: string }>(
      await ReadScriptFile(currentId.value, path))
    const next = data?.content ?? ''
    if (next === fileText.value) return
    fileText.value = next
    if (data?.lang) fileLang.value = data.lang
  } catch (e) {
    // 读不到（文件刚被删/重命名）就交给 refreshFilesAndDeps 处理，这里不弹错。
    logErr('script.syncFile', e)
  }
}

// ---------- 事件订阅 ----------
let offLog: (() => void) | null = null
let offRun: (() => void) | null = null
let offChanged: (() => void) | null = null

function subscribe() {
  offLog = Events.On('quickdock:script:log', (ev: any) => {
    const d = ev?.data
    if (!d || d.runId !== runningRunId.value) return
    const rows = Array.isArray(d.lines) ? d.lines : []
    if (!rows.length) return
    logLines.value.push(...rows)
    // 只留最近 5000 行：一个死循环脚本能在一分钟内产出十万行，
    // 全留在 DOM 里会把页面拖到连滚动都卡。
    if (logLines.value.length > 5000) logLines.value.splice(0, logLines.value.length - 5000)
    scrollLogToEnd()
  })
  offRun = Events.On('quickdock:script:run', (ev: any) => {
    const d = ev?.data
    if (!d) return
    if (d.status === 'running') {
      if (d.scriptId === currentId.value) runningRunId.value = d.runId
      markRunning(d.scriptId, true)
      return
    }
    // 收尾：清掉运行态、刷新列表里的状态与历史
    markRunning(d.scriptId, false)
    if (d.runId === runningRunId.value) runningRunId.value = ''
    if (d.scriptId === currentId.value) {
      loadRuns()
      loadStats()
      // 依赖安装这一轮跑完后 deps 就不再是 stale 了：不刷这里，黄色的
      // 「依赖有变化，建议重新安装」会一直挂着，用户以为没装上。
      void refreshFilesAndDeps()
    }
    loadScripts()
  })
  offChanged = Events.On('quickdock:script:changed', (ev: any) => {
    const reason = ev?.data?.reason
    const path = String(ev?.data?.path || '')
    // 列表元数据（运行次数、置顶、定时绑定等）总是要刷。
    if (ev?.data) loadScripts()
    // 文件级变化要同时刷新树与依赖状态：AI 建脚本/写文件、导入、用外部编辑器
    // 改过之后，树里没有新文件、依赖标记也不对，用户只能靠切页重开才发现。
    if (reason === 'save' || reason === 'create-file' || reason === 'delete-file') {
      void refreshFilesAndDeps()
      // AI 直接改这个脚本的代码是最常见的场景：不把新内容同步过来，用户看到的
      // 是旧代码，一保存又把 AI 的改动覆盖回去。
      if (reason === 'save' || reason === 'create-file') void syncActiveFile(path)
    }
  })
}

const logBox = ref<HTMLElement | null>(null)
function scrollLogToEnd() {
  if (!logFollow.value) return
  requestAnimationFrame(() => {
    const el = logBox.value
    if (el) el.scrollTop = el.scrollHeight
  })
}
/** 用户手动往上滚 → 暂停跟随；滚回底部 → 自动恢复。 */
function onLogScroll() {
  const el = logBox.value
  if (!el) return
  logFollow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 24
}
/** 日志面板里显示的行（关键字 + 仅错误过滤）。 */
const filteredLogs = computed(() => {
  const kw = logFilter.value.trim().toLowerCase()
  let rows = logLines.value
  if (logErrOnly.value) rows = rows.filter(l => l.stream === 'stderr')
  if (kw) rows = rows.filter(l => l.text.toLowerCase().includes(kw))
  return rows
})

// ---------- 变量 ----------
const envs = ref<ScriptEnvRow[]>([])
const envDraft = ref<{ key: string; value: string; secret: boolean; remark: string }>({
  key: '', value: '', secret: false, remark: '',
})
const revealed = ref<Record<string, string>>({})
/**
 * 变量作用域：本脚本 / 全局。
 *
 * 后端 ListScriptEnvs 一次返回「全局 + 本脚本」（运行时也是先铺全局再被脚本
 * 覆盖），所以这里只是一次筛选；但界面上必须分开，否则用户会以为那个
 * Cookie 只属于当前脚本，改完发现所有脚本都变了。
 */
const envScope = ref<'script' | 'global'>('script')
const scriptEnvs = computed(() => envs.value.filter(e => e.scriptId === currentId.value))
const globalEnvs = computed(() => envs.value.filter(e => !e.scriptId))
const scopeEnvs = computed(() => envScope.value === 'global' ? globalEnvs.value : scriptEnvs.value)

async function loadEnvs() {
  if (!currentId.value) { envs.value = []; return }
  try {
    envs.value = unwrap<ScriptEnvRow[]>(await ListScriptEnvs(currentId.value)) || []
    revealed.value = {}
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function saveEnv() {
  if (!currentId.value) return
  const key = envDraft.value.key.trim()
  if (!key) { toastErr(t('scr_envKeyRequired')); return }
  try {
    unwrap(await SaveScriptEnv({
      id: '', scriptId: envScope.value === 'global' ? '' : currentId.value, key,
      value: envDraft.value.value, secret: envDraft.value.secret,
      remark: envDraft.value.remark, sort: scopeEnvs.value.length,
    }))
    envDraft.value = { key: '', value: '', secret: false, remark: '' }
    await loadEnvs()
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function toggleReveal(row: ScriptEnvRow) {
  if (revealed.value[row.id] !== undefined) {
    const copy = { ...revealed.value }
    delete copy[row.id]
    revealed.value = copy
    return
  }
  try {
    const data = unwrap<{ value: string }>(await RevealScriptEnv(row.id))
    revealed.value = { ...revealed.value, [row.id]: data?.value ?? '' }
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function removeEnv(row: ScriptEnvRow) {
  try {
    unwrap(await DeleteScriptEnv(row.id))
    await loadEnvs()
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

// ---------- 运行历史 ----------
const runs = ref<ScriptRunRow[]>([])
const runsLoading = ref(false)
const runLogText = ref('')
const runLogMissing = ref(false)
const viewingRun = ref<ScriptRunRow | null>(null)
const patch = ref<PatchInfo | null>(null)
const healing = ref(false)

async function loadRuns() {
  if (!currentId.value) { runs.value = []; return }
  runsLoading.value = true
  try {
    runs.value = unwrap<ScriptRunRow[]>(await ListScriptRuns(currentId.value, 50)) || []
  } catch (e) {
    logErr('script.runs', e)
  } finally {
    runsLoading.value = false
  }
}

async function viewRun(run: ScriptRunRow) {
  viewingRun.value = run
  patch.value = null
  try {
    const data = unwrap<{ content: string; missing?: boolean }>(await GetScriptRunLog(run.id, 0))
    runLogText.value = data?.content || ''
    runLogMissing.value = !!data?.missing
    // 已经分析过的补丁直接显示，不必再点一次。
    if (run.aiPatch) {
      try { patch.value = JSON.parse(run.aiPatch) } catch { patch.value = null }
    }
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function heal(run: ScriptRunRow) {
  if (healing.value) return
  healing.value = true
  try {
    const data = unwrap<PatchInfo & { running?: boolean }>(await HealScriptRun(run.id))
    if (data && !data.running && data.content !== undefined) {
      patch.value = data
      if (!data.content) toastErr(t('scr_healNoPatch'))
    } else if (data?.running) {
      toastOK(t('scr_healRunning'))
    }
  } catch (e) {
    toastErr(getErrorMessage(e))
  } finally {
    healing.value = false
  }
}

async function applyPatch() {
  if (!viewingRun.value) return
  if (!(await toastConfirm(t('scr_healApplyConfirm')))) return
  try {
    unwrap(await ApplyScriptPatch(viewingRun.value.id, true))
    toastOK(t('scr_healApplied'))
    viewingRun.value = null
    await reloadFiles()
    await loadRuns()
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function clearRuns() {
  if (!currentId.value) return
  if (!(await toastConfirm(t('scr_clearRunsConfirm')))) return
  try {
    unwrap(await DeleteScriptRuns(currentId.value))
    await loadRuns()
    await loadStats()
    toastOK(t('scr_clearedRuns'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

// ---------- 统计 ----------
const stats = ref<ScriptStats | null>(null)
async function loadStats() {
  if (!currentId.value) { stats.value = null; return }
  try {
    stats.value = unwrap<ScriptStats>(await GetScriptStats(currentId.value))
  } catch { stats.value = null }
}
/** 最近若干次成功运行的耗时（时间正序），给统计行里的迷你曲线用。 */
const durations = computed(() => {
  const list = runs.value.slice(0, 20)
    .filter(r => r.status === 'ok' && r.durationMs > 0)
    .map(r => r.durationMs)
  return list.reverse()
})
/** 把耗时映射成 0~20 高度里的折线点（最小-最大归一化）。 */
const sparkPoints = computed(() => {
  const d = durations.value
  if (d.length < 2) return ''
  const max = Math.max(...d)
  const min = Math.min(...d)
  const span = max - min || 1
  return d.map((v, i) => `${(i * 6 + 1).toFixed(1)},${(19 - ((v - min) / span) * 16).toFixed(1)}`).join(' ')
})

const successRate = computed(() => {
  const s = stats.value
  if (!s || s.total === 0) return '—'
  return Math.round((s.ok / s.total) * 100) + '%'
})

// ---------- 设置表单 ----------
const form = ref<ScriptForm | null>(null)
function syncForm() {
  const s = current.value
  if (!s) { form.value = null; return }
  form.value = {
    id: s.id, name: s.name, description: s.description, lang: s.lang, entry: s.entry,
    interpreter: s.interpreter, defaultArgs: s.defaultArgs, timeoutSec: s.timeoutSec,
    concurrency: s.concurrency || 1,
    autoInstall: s.autoInstall, pinned: s.pinned, mcpAllowed: s.mcpAllowed,
    tags: s.tags, sort: s.sort, watchPath: s.watchPath,
  }
}
watch(current, syncForm, { immediate: true })

/** 入口填了但目录里没有这个文件：运行只会得到「找不到文件」，在设置里就提醒。
 *  files 为空时判断不了（可能还在加载），此时不提示，免得闪一下红字。 */
const entryMissing = computed(() => {
  const e = (form.value?.entry || '').trim()
  return !!e && files.value.length > 0 && !files.value.some(f => f.path === e)
})

async function saveSettings() {
  if (!form.value) return
  // 保存前记下原入口：只有入口真的换了才值得动编辑器（切到新入口），
  // 只改个名字/超时也把编辑区切走，等于每次保存都打断正在写的人。
  const before = current.value?.entry || ''
  try {
    unwrap(await UpdateScript(form.value))
    await loadScripts()
    if ((form.value.entry || '').trim() !== before) await reloadFiles(form.value.entry)
    else await refreshFilesAndDeps()
    toastOK(t('scr_saved'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function testWatchPath() {
  if (!form.value?.watchPath) { toastErr(t('scr_watchEmpty')); return }
  try {
    const data = unwrap<{ ok: boolean; isDir: boolean }>(await TestWatchPath(form.value.watchPath))
    toastOK(data?.isDir ? t('scr_watchDirOk') : t('scr_watchFileOk'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

// ---------- 新建 / 删除 / 导入导出 ----------
const createOpen = ref(false)
const createDraft = ref({ name: '', lang: 'python' as string, entry: '', tags: '' })

function openCreate() {
  createDraft.value = { name: '', lang: 'python', entry: '', tags: '' }
  createOpen.value = true
}

async function doCreate() {
  if (!createDraft.value.name.trim()) { toastErr(t('scr_nameRequired')); return }
  try {
    const sc = unwrap<ScriptRec>(await CreateScript({
      id: '', name: createDraft.value.name.trim(), description: '', lang: createDraft.value.lang,
      entry: createDraft.value.entry.trim(), interpreter: '', defaultArgs: '[]',
      timeoutSec: 0, concurrency: 1, autoInstall: createDraft.value.lang !== 'custom', pinned: false,
      mcpAllowed: false, tags: createDraft.value.tags, sort: scripts.value.length, watchPath: '',
    }))
    createOpen.value = false
    await loadScripts()
    if (sc?.id) {
      currentId.value = ''
      await selectScript(sc.id)
    }
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

// ---------- 模板 ----------
//
// 空状态直接给可点击的模板卡：新建弹窗要填名字、语言、入口，对第一次用的人
// 是最难的一步；模板把这些都填好，落地就在编辑器里，只剩「改两行再运行」。
// 走 CreateScriptWithFiles（AI 建脚本同一条后端链路）：一次落库 + 写文件，
// 不会留下「有记录没文件」的半成品。
interface ScriptTemplate {
  key: string
  nameKey: string
  descKey: string
  lang: string
  entry: string
  tags: string
  files: Record<string, string>
}

const TEMPLATES: ScriptTemplate[] = [
  {
    key: 'crawler',
    nameKey: 'scrTplCrawler',
    descKey: 'scrTplCrawlerDesc',
    lang: 'python',
    entry: 'main.py',
    tags: '',
    files: {
      'main.py': `# -*- coding: utf-8 -*-
"""抓取网页标题，并把结果追加到 QD_DATA_DIR/crawl.log。

依赖：requests（见同目录 requirements.txt）。首次运行前先点「安装依赖」。
只改 URL 就能用；要抓正文时在 fetch 之后接 BeautifulSoup 即可。
"""
import os
from datetime import datetime

import requests

URL = "https://example.com"


def fetch_title(url: str) -> str:
    resp = requests.get(url, timeout=15, headers={"User-Agent": "QuickDock/1.0"})
    resp.raise_for_status()
    resp.encoding = resp.apparent_encoding
    if "<title>" not in resp.text:
        return "(no title)"
    return " ".join(resp.text.split("<title>")[1].split("</title>")[0].split())


def main():
    title = fetch_title(URL)
    print(f"{datetime.now():%H:%M:%S} {URL} -> {title}")

    out = os.path.join(os.environ.get("QD_DATA_DIR", "."), "crawl.log")
    with open(out, "a", encoding="utf-8") as f:
        f.write(f"{datetime.now().isoformat(timespec='seconds')}\\t{URL}\\t{title}\\n")


if __name__ == "__main__":
    main()
`,
      'requirements.txt': 'requests>=2.31\n',
    },
  },
  {
    key: 'clean',
    nameKey: 'scrTplClean',
    descKey: 'scrTplCleanDesc',
    lang: 'shell',
    entry: 'clean_logs.sh',
    tags: '',
    files: {
      'clean_logs.sh': `#!/usr/bin/env bash
# 清理过期日志：默认只打印计划（DRY_RUN=1），确认无误后改成 DRY_RUN=0 再跑。
# 目录 / 天数 / 匹配规则都可用环境变量覆盖：LOG_DIR DAYS PATTERN DRY_RUN
set -euo pipefail

LOG_DIR="\${LOG_DIR:-.}"
DAYS="\${DAYS:-7}"
PATTERN="\${PATTERN:-*.log}"
DRY_RUN="\${DRY_RUN:-1}"

echo "扫描 \${LOG_DIR} 下超过 \${DAYS} 天的 \${PATTERN}（DRY_RUN=\${DRY_RUN}）"
files=\$(find "\$LOG_DIR" -type f -name "\$PATTERN" -mtime +"\$DAYS" 2>/dev/null || true)
if [ -z "\$files" ]; then
  echo "没有需要清理的文件"
  exit 0
fi

while IFS= read -r f; do
  if [ "\$DRY_RUN" = "1" ]; then
    echo "[预演] 将删除 \$f"
  else
    rm -f -- "\$f" && echo "已删除 \$f"
  fi
done <<< "\$files"
`,
    },
  },
  {
    key: 'api',
    nameKey: 'scrTplApi',
    descKey: 'scrTplApiDesc',
    lang: 'javascript',
    entry: 'main.js',
    tags: '',
    files: {
      'main.js': `// 接口巡检：状态码非 2xx 或请求失败时以退出码 1 结束，
// 这样「运行历史」会直接标红，绑定的定时任务也能据此告警。
const URL = process.env.CHECK_URL || 'https://example.com'
const TIMEOUT_MS = Number(process.env.TIMEOUT_MS || 10000)

async function main() {
  const started = Date.now()
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS)
  try {
    const resp = await fetch(URL, { signal: ctrl.signal, redirect: 'follow' })
    console.log(\`\${resp.status} \${resp.statusText} \${Date.now() - started}ms \${URL}\`)
    if (!resp.ok) process.exitCode = 1
  } finally {
    clearTimeout(timer)
  }
}

main().catch((e) => {
  console.error('request failed:', e.message)
  process.exitCode = 1
})
`,
    },
  },
  {
    key: 'backup',
    nameKey: 'scrTplBackup',
    descKey: 'scrTplBackupDesc',
    lang: 'python',
    entry: 'main.py',
    tags: '',
    files: {
      'main.py': `# -*- coding: utf-8 -*-
"""把目录打包成 zip 存到 QD_DATA_DIR/backup，按份数自动轮转。

用环境变量覆盖：BACKUP_SRC（要备份的目录）BACKUP_KEEP（保留份数）。
"""
import os
import shutil
from datetime import datetime

SRC = os.environ.get("BACKUP_SRC", ".")
KEEP = int(os.environ.get("BACKUP_KEEP", "7"))


def main():
    dest_dir = os.path.join(os.environ.get("QD_DATA_DIR", "."), "backup")
    os.makedirs(dest_dir, exist_ok=True)

    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    base = os.path.join(dest_dir, f"{os.path.basename(os.path.abspath(SRC))}-{stamp}")
    archive = shutil.make_archive(base, "zip", SRC)
    print("backup ->", archive)

    olds = sorted(
        (os.path.join(dest_dir, f) for f in os.listdir(dest_dir) if f.endswith(".zip")),
        key=os.path.getmtime,
    )
    for f in olds[:-KEEP] if len(olds) > KEEP else []:
        os.remove(f)
        print("rotated out", os.path.basename(f))


if __name__ == "__main__":
    main()
`,
    },
  },
]

const creatingTpl = ref('')

async function createFromTemplate(tpl: ScriptTemplate) {
  if (creatingTpl.value) return
  creatingTpl.value = tpl.key
  try {
    // 注意返回的是 { script, files, deps } 包一层，不是裸 ScriptRec
    const data = unwrap<{ script: ScriptRec }>(await CreateScriptWithFiles({
      id: '', name: t(tpl.nameKey), description: t(tpl.descKey), lang: tpl.lang,
      entry: tpl.entry, interpreter: '', defaultArgs: '[]', timeoutSec: 0, concurrency: 1,
      autoInstall: true, pinned: false, mcpAllowed: false, tags: tpl.tags,
      sort: scripts.value.length, watchPath: '',
    }, tpl.files))
    const id = data?.script?.id || ''
    await loadScripts()
    if (id) {
      currentId.value = ''
      await selectScript(id)
    }
  } catch (e) {
    toastErr(getErrorMessage(e))
  } finally {
    creatingTpl.value = ''
  }
}

/** 首次使用教程：用系统浏览器打开，不用裸 <a>（WebView 会把整个应用导航走）。 */
function openGuide() {
  openExternal(SITE_GUIDE_URL).catch((e: unknown) => {
    logErr('script.guide', e)
    toastErr(getErrorMessage(e))
  })
}

async function removeScript() {
  if (!current.value) return
  const bound = bindings.value.length
  const msg = bound
    ? t('scr_deleteBoundConfirm', { name: current.value.name, n: bound })
    : t('scr_deleteConfirm', { name: current.value.name })
  if (!(await toastConfirm(msg))) return
  try {
    unwrap(await DeleteScript(current.value.id))
    currentId.value = ''
    await loadScripts()
    toastOK(t('scr_deleted'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

/** 复制当前脚本（含目录内容）：改现成脚本比从模板重写快得多。 */
async function duplicateScript() {
  if (!current.value) return
  try {
    const sc = unwrap<ScriptRec>(await CopyScript(current.value.id))
    await loadScripts()
    if (sc?.id) {
      currentId.value = ''
      await selectScript(sc.id)
    }
    toastOK(t('scr_duplicated'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function togglePin(s: ScriptRec) {
  try {
    unwrap(await SetScriptPin(s.id, !s.pinned))
    await loadScripts()
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function exportCurrent() {
  if (!current.value) return
  try {
    const dest = unwrap<string>(await SelectSavePath(t('scr_exportTitle'), current.value.name + '.zip'))
    if (!dest) return
    unwrap(await ExportScript(current.value.id, dest))
    toastOK(t('scr_exported'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function importScript() {
  try {
    const path = unwrap<string>(await SelectLocalFile(t('scr_importTitle')))
    if (!path) return
    const data = unwrap<{ script: ScriptRec; needsTrust: boolean }>(await ImportScript(path))
    const sc = data?.script
    await loadScripts()
    if (sc?.id) {
      currentId.value = ''
      await selectScript(sc.id)
    }
    // 导入的脚本默认不可信：直接告诉用户「要确认一次才能跑」，
    // 否则他会点运行、被拦一次、再回来问为什么。
    toastOK(data?.needsTrust ? t('scr_importedNeedsTrust') : t('scr_imported'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

// 新建文件 / 删除文件
const newFileOpen = ref(false)
const newFileName = ref('')

function addFile() {
  if (!currentId.value) return
  newFileName.value = ''
  newFileOpen.value = true
}

async function commitNewFile() {
  const name = newFileName.value.trim()
  if (!currentId.value || !name) { newFileOpen.value = false; return }
  try {
    unwrap(await CreateScriptFile(currentId.value, name, ''))
    newFileOpen.value = false
    await reloadFiles(name)
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

async function removeFile(f: ScriptFile) {
  if (!currentId.value) return
  if (!(await toastConfirm(t('scr_deleteFileConfirm', { path: f.path })))) return
  try {
    unwrap(await DeleteScriptFile(currentId.value, f.path))
    if (activeFile.value === f.path) {
      activeFile.value = ''
      fileText.value = ''
    }
    await reloadFiles()
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

// ---------- HTTP 触发 ----------
const triggerOpen = ref(false)
const trigger = ref<TriggerInfo>({ enabled: false, running: false, port: 0, token: '', url: '' })

async function openTrigger() {
  triggerOpen.value = true
  await refreshTrigger()
}
async function refreshTrigger() {
  try {
    trigger.value = unwrap<TriggerInfo>(await TriggerStatus()) || trigger.value
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}
async function startTrigger() {
  try {
    const data = unwrap<{ port: number; token: string; url: string }>(await StartTrigger())
    await refreshTrigger()
    toastOK(t('scr_triggerStarted', { port: data?.port ?? 0 }))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}
async function stopTrigger() {
  try {
    unwrap(await StopTrigger())
    await refreshTrigger()
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}
async function rotateToken() {
  try {
    unwrap(await RotateTriggerToken())
    await refreshTrigger()
    toastOK(t('scr_tokenRotated'))
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}
async function copyToken() {
  await copyToClipboard(trigger.value.token)
  toastOK(t('scr_copied'))
}
function triggerUrlFor(id: string) {
  return `http://127.0.0.1:${trigger.value.port || '端口'}/run/${id}?token=${trigger.value.token ? '…' : ''}`
}

// ---------- 定时绑定 ----------
const bindings = ref<ScriptBinding[]>([])
async function loadBindings() {
  if (!currentId.value) { bindings.value = []; return }
  try {
    bindings.value = unwrap<ScriptBinding[]>(await GetScriptBindings(currentId.value)) || []
  } catch {
    bindings.value = []
  }
}

// ---------- 跨 run 日志搜索 ----------
const logSearchKeyword = ref('')
const logSearchBusy = ref(false)
const logHits = ref<LogHit[]>([])
const logSearchFiles = ref(0)

async function searchLogs() {
  const kw = logSearchKeyword.value.trim()
  if (!currentId.value || !kw) { logHits.value = []; logSearchFiles.value = 0; return }
  logSearchBusy.value = true
  try {
    const data = unwrap<{ hits: LogHit[]; scanned: number }>(
      await SearchScriptLogs(currentId.value, kw, 100))
    logHits.value = data?.hits || []
    logSearchFiles.value = data?.scanned || 0
  } catch (e) {
    toastErr(getErrorMessage(e))
  } finally {
    logSearchBusy.value = false
  }
}

/** 点命中项：切到那次运行的历史日志。历史已被裁剪时直接按 runId 读文件。 */
async function openHit(h: LogHit) {
  const run = runs.value.find(r => r.id === h.runId)
  if (run) { await viewRun(run); return }
  // 命中项可能在已经加载的历史之外（列表只拉最近 N 条）：直接按 runId 读，
  // 后端会把 run 元数据一起返回，状态/触发来源不会丢。
  try {
    const data = unwrap<{ content: string; missing?: boolean; run?: ScriptRunRow }>(
      await GetScriptRunLog(h.runId, 0))
    if (data?.run) viewingRun.value = data.run
    patch.value = null
    runLogText.value = data?.content || ''
    runLogMissing.value = !!data?.missing
  } catch (e) {
    toastErr(getErrorMessage(e))
  }
}

// ---------- 展示工具 ----------
const tab = ref<'code' | 'env' | 'runs' | 'settings'>('code')

function fmtTime(s: string) {
  if (!s) return '—'
  return s.replace('T', ' ').replace(/(\+08:00|Z)$/, '').slice(0, 19)
}
function fmtDur(ms: number) {
  if (!ms || ms < 0) return '—'
  if (ms < 1000) return ms + ' ms'
  if (ms < 60000) return (ms / 1000).toFixed(1) + ' s'
  const m = Math.floor(ms / 60000)
  const s = Math.round((ms % 60000) / 1000)
  return m + 'm ' + s + 's'
}
function statusColor(st: string) {
  if (st === 'ok') return 'var(--color-success)'
  if (st === 'running') return 'var(--color-accent)'
  if (st === 'canceled') return 'var(--color-text-muted)'
  if (st === 'timeout') return 'var(--color-warning)'
  if (!st) return 'var(--color-text-muted)'
  return 'var(--color-danger)'
}
function triggerLabel(tr: string) {
  return t('scr_trigger_' + (tr || 'manual'))
}
async function copyLog() {
  const text = viewingRun.value ? runLogText.value
    : filteredLogs.value.map(l => l.text).join('\n')
  await copyToClipboard(text)
  toastOK(t('scr_copied'))
}

// ---------- 快捷键 ----------
// Ctrl+S 保存、Ctrl+Enter 运行。编辑器自己也派发 save，两处都指向同一个
// saveFile（内部有 fileDirty 判断），重复触发无副作用。
function onKeydown(e: KeyboardEvent) {
  if (!currentId.value) return
  if (!(e.ctrlKey || e.metaKey)) return
  if (e.key === 's' || e.key === 'S') {
    e.preventDefault()
    void saveFile()
  } else if (e.key === 'Enter') {
    e.preventDefault()
    if (!running.value) void runScript()
  }
}

// ---------- 生命周期 ----------
/** 消费总览卡片的深链：开新建弹窗 / 选中某条脚本。列表已加载完才调用。 */
function consumeDeepLink() {
  if (pendingCreateScript.value) {
    pendingCreateScript.value = false
    openCreate()
  }
  const id = pendingOpenScriptId.value
  if (!id) return
  pendingOpenScriptId.value = ''
  // 列表里没有这个 id（刚被删/换了机器）就静默忽略，别把用户丢进空白的详情。
  if (scripts.value.some(s => s.id === id)) void selectScript(id)
}

onMounted(async () => {
  window.addEventListener('keydown', onKeydown)
  subscribe()
  await loadScripts()
  consumeDeepLink()
})
// 保活页切回来时列表可能是旧的（别的页面刚建了脚本），先刷新再消费深链。
onActivated(async () => {
  await loadScripts()
  consumeDeepLink()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  offLog?.(); offRun?.(); offChanged?.()
})
</script>

<template>
  <div class="scr-page">
    <PageHeader>
      <template #titles>
        <!-- 计数跟着标题走：放在 #titles 里会掉到第二行，变成一个孤零零的数字 -->
        <h2 class="qd-page-title">{{ t('scr_title') }}<span class="scr-count">({{ scripts.length }})</span></h2>
      </template>
      <!-- 空状态时整条工具栏都没有意义（没有可搜/可筛/可刷新的数据），
           只留标题 + 全屏引导页，避免出现两颗「新建」按钮 -->
      <div v-if="scripts.length" class="scr-head-actions">
        <QdSearchBox v-model="keyword" class="scr-search" :icon-size="14" :placeholder="t('scr_search')" />
        <!-- 语言筛选与「只看失败」统一成胶囊，宽度和高度跟搜索框齐平 -->
        <label class="scr-pill" :class="{ on: !!langFilter }" :title="t('scr_allLangs')">
          <span>{{ langFilter ? langText(langFilter) : t('scr_allLangs') }}</span>
          <ChevronDown :size="12" />
          <select v-model="langFilter" class="scr-pill-select" :aria-label="t('scr_allLangs')">
            <option value="">{{ t('scr_allLangs') }}</option>
            <option v-for="l in LANGS" :key="l" :value="l">{{ langText(l) }}</option>
          </select>
        </label>
        <button class="scr-pill" :class="{ on: onlyFailed }" :aria-pressed="onlyFailed"
          :title="t('scr_onlyFailed')" @click="onlyFailed = !onlyFailed">
          <AlertTriangle :size="12" />
          <span>{{ t('scr_onlyFailed') }}</span>
        </button>
        <span class="scr-sep" aria-hidden="true"></span>
        <button class="scr-btn" :title="t('scr_trigger')" @click="openTrigger">
          <Webhook :size="15" />
        </button>
        <!-- 导入 = 从磁盘拿进来。改用「进入方框的箭头」，不再是容易读成导出的向上箭头 -->
        <button class="scr-btn" :title="t('scr_import')" @click="importScript">
          <Import :size="15" />
        </button>
        <button class="scr-btn" :title="t('scr_refresh')" @click="loadScripts()">
          <RefreshCw :size="15" :class="{ spin: loading }" />
        </button>
        <span class="scr-sep" aria-hidden="true"></span>
        <button class="scr-btn primary" @click="openCreate">
          <Plus :size="15" /> {{ t('scr_new') }}
        </button>
      </div>
    </PageHeader>

    <div class="scr-body">
      <!-- 首帧还没拉到列表：先转圈，别让「左边空 / 右边去左边选」的错误状态闪一下 -->
      <div v-if="!listReady" class="scr-boot"><LoaderCircle :size="18" class="spin" /></div>

      <!-- 一条脚本都没有：不摆两个互相指路的空框，直接合并成全屏引导页 -->
      <div v-else-if="isEmpty" class="qd-guide scr-guide">
        <div class="qd-guide-inner">
          <div class="qd-guide-icon scr-guide-icon">
            <!-- 齿轮 + 代码符号：本页的语义就是「把代码跑起来」 -->
            <svg class="scr-guide-art" width="30" height="30" viewBox="0 0 24 24" fill="none"
              stroke="var(--color-accent)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"
              aria-hidden="true">
              <g class="scr-guide-gear">
                <circle cx="12" cy="12" r="7.2" />
                <path d="M12 2.8v2.1M12 19.1v2.1M2.8 12h2.1M19.1 12h2.1M5.5 5.5l1.5 1.5M17 17l1.5 1.5M18.5 5.5 17 7M7 17l-1.5 1.5" />
              </g>
              <path d="M10.3 9.6 8.4 12l1.9 2.4" />
              <path d="M13.7 9.6 15.6 12l-1.9 2.4" />
            </svg>
          </div>

          <h2 class="qd-guide-title">{{ t('scrGuideTitle') }}</h2>
          <p class="qd-guide-desc">{{ t('scrGuideDesc') }}</p>

          <button class="qd-guide-cta" @click="openCreate">
            <Plus :size="15" />
            <span>{{ t('scrGuideCta') }}</span>
          </button>

          <p class="scr-guide-tpl-title">{{ t('scrGuideTpl') }}</p>
          <div class="scr-tpl-grid">
            <button
              v-for="tpl in TEMPLATES" :key="tpl.key" class="scr-tpl"
              :disabled="!!creatingTpl" @click="createFromTemplate(tpl)"
            >
              <span class="scr-tpl-head">
                <span class="scr-tpl-lang">{{ langText(tpl.lang) }}</span>
                <LoaderCircle v-if="creatingTpl === tpl.key" :size="12" class="spin" />
                <ChevronRight v-else :size="12" class="scr-tpl-arrow" />
              </span>
              <span class="scr-tpl-name">{{ t(tpl.nameKey) }}</span>
              <span class="scr-tpl-desc">{{ t(tpl.descKey) }}</span>
            </button>
          </div>

          <p class="scr-guide-steps">{{ t('scrGuideSteps') }}</p>

          <div class="qd-guide-footer">
            <button class="qd-guide-link" :title="SITE_GUIDE_URL" @click="openGuide">
              <BookOpen :size="13" />
              {{ t('scrGuideDoc') }}
            </button>
            <button class="qd-guide-link" @click="importScript">
              <Import :size="13" />
              {{ t('scrGuideImport') }}
            </button>
          </div>
        </div>
      </div>

      <template v-else>
      <!-- 左：脚本列表 -->
      <aside class="scr-list">
        <div v-if="!filtered.length" class="scr-list-empty">
          <FileCode2 :size="22" />
          <p>{{ t('scr_noMatch') }}</p>
          <button v-if="hasFilter" class="scr-btn sm" @click="clearFilters">{{ t('scrClearFilters') }}</button>
        </div>
        <button
          v-for="s in filtered" :key="s.id" class="scr-item"
          :class="{ active: s.id === currentId }" @click="selectScript(s.id)"
        >
          <span class="scr-item-dot" :style="{ background: statusColor(s.lastStatus) }" />
          <span class="scr-item-main">
            <span class="scr-item-name">
              <Pin v-if="s.pinned" :size="11" class="scr-pin" />
              {{ s.name }}
              <span v-if="runningIds.has(s.id)" class="scr-running-dot" />
            </span>
            <span class="scr-item-meta">
              <span class="scr-lang">{{ langText(s.lang) }}</span>
              <span v-if="s.nextRun" class="scr-next" :title="t('scr_bindingNext')">
                <Clock :size="10" />{{ fmtTime(s.nextRun) }}
              </span>
              <span v-else-if="s.lastRun">{{ fmtTime(s.lastRun) }}</span>
              <span v-else>{{ t('scr_neverRun') }}</span>
            </span>
          </span>
          <span class="scr-item-act" @click.stop>
            <button :title="s.pinned ? t('scr_unpin') : t('scr_pin')" @click="togglePin(s)">
              <PinOff v-if="s.pinned" :size="12" /><Pin v-else :size="12" />
            </button>
          </span>
        </button>
      </aside>

      <!-- 右：详情 -->
      <section class="scr-main">
        <div v-if="!current" class="scr-placeholder">
          <FileCode2 :size="40" />
          <p>{{ t('scr_pickHint') }}</p>
          <p class="scr-placeholder-sub">{{ filtered.length ? t('scr_pickHintSub') : t('scrNoMatchHint') }}</p>
        </div>

        <template v-else>
          <div class="scr-toolbar">
            <div class="scr-title-wrap">
              <h3>{{ current.name }}</h3>
              <span class="scr-sub">{{ current.entry }} · {{ langText(current.lang) }}</span>
            </div>
            <div class="scr-run-actions">
              <span v-if="current.lastStatus" class="scr-status" :style="{ color: statusColor(current.lastStatus) }">
                {{ t('scr_status_' + current.lastStatus) }}
                <template v-if="current.lastDurationMs"> · {{ fmtDur(current.lastDurationMs) }}</template>
              </span>
              <input
                v-model="runArgs" class="scr-args mono" :placeholder="t('scr_runArgsPh')"
                :title="t('scr_runArgsHint')"
              />
              <button v-if="!running" class="scr-btn primary" @click="runScript">
                <Play :size="14" /> {{ t('scr_run') }}
              </button>
              <button v-else class="scr-btn danger" @click="stopRun">
                <Square :size="13" /> {{ t('scr_stop') }}
              </button>
              <button
                v-if="activeFile" class="scr-btn" :class="{ primary: fileDirty }"
                :disabled="!fileDirty" :title="t('scr_saveHint')" @click="saveFile"
              >
                <Save :size="14" /> {{ t('scr_save') }}<span v-if="fileDirty" class="scr-dirty-dot" />
              </button>
              <button v-if="deps.fileName" class="scr-btn" :title="t('scr_installDeps')" @click="installDeps">
                <PackagePlus :size="14" />
              </button>
              <button class="scr-btn" :title="t('scr_openDir')" @click="OpenScriptDir(current.id)">
                <FolderOpen :size="14" />
              </button>
              <button class="scr-btn" :title="t('scr_duplicate')" @click="duplicateScript">
                <Copy :size="14" />
              </button>
              <button class="scr-btn" :title="t('scr_export')" @click="exportCurrent">
                <Download :size="14" />
              </button>
              <button class="scr-btn danger" :title="t('scr_delete')" @click="removeScript">
                <Trash2 :size="14" />
              </button>
            </div>
          </div>

          <div v-if="!trusted" class="scr-banner warn">
            <AlertTriangle :size="14" />
            <span>{{ t('scr_untrusted') }}</span>
            <button class="scr-btn sm" @click="trustCurrent">{{ t('scr_trust') }}</button>
          </div>
          <div v-else-if="deps.fileName && deps.stale" class="scr-banner">
            <PackagePlus :size="14" />
            <span>{{ t('scr_depsStaleHint', { file: deps.fileName }) }}</span>
            <button class="scr-btn sm" @click="installDeps">{{ t('scr_installDeps') }}</button>
          </div>

          <div class="scr-tabs">
            <button :class="{ active: tab === 'code' }" @click="tab = 'code'">
              {{ t('scr_tabCode') }}<span v-if="fileDirty" class="scr-dirty-dot" />
            </button>
            <button :class="{ active: tab === 'env' }" @click="tab = 'env'">
              {{ t('scr_tabEnv') }}<span v-if="scriptEnvs.length" class="scr-badge">{{ scriptEnvs.length }}</span>
            </button>
            <button :class="{ active: tab === 'runs' }" @click="tab = 'runs'">{{ t('scr_tabRuns') }}</button>
            <button :class="{ active: tab === 'settings' }" @click="tab = 'settings'">{{ t('scr_tabSettings') }}</button>
            <div class="scr-tabs-gap" />
            <button class="scr-btn ghost sm" @click="logPanelOpen = !logPanelOpen">
              {{ logPanelOpen ? t('scr_hideLog') : t('scr_showLog') }}
            </button>
          </div>

          <!-- 代码 -->
          <div v-show="tab === 'code'" class="scr-code">
            <div class="scr-files">
              <div class="scr-files-head">
                <span>{{ t('scr_files') }}</span>
                <button :title="t('scr_newFile')" @click="addFile"><FilePlus2 :size="13" /></button>
              </div>
              <div v-if="newFileOpen" class="scr-file-new">
                <input v-model="newFileName" :placeholder="t('scr_newFilePrompt')" class="mono"
                  @keyup.enter="commitNewFile" @keyup.esc="newFileOpen = false" />
              </div>
              <button
                v-for="f in files" :key="f.path" class="scr-file"
                :class="{ active: f.path === activeFile }" @click="openFile(f.path)"
              >
                <FileIcon :size="12" />
                <span class="scr-file-name">{{ f.path }}</span>
                <span v-if="f.path === activeFile && fileDirty" class="scr-dirty-dot" />
                <span class="scr-file-del" @click.stop="removeFile(f)"><X :size="11" /></span>
              </button>
              <p v-if="!files.length" class="scr-files-empty">{{ t('scr_noFiles') }}</p>
            </div>
            <div class="scr-editor">
              <div v-if="fileLoading || detailLoading" class="scr-editor-loading">
                <LoaderCircle :size="18" class="spin" />
              </div>
              <div v-else-if="fileReadErr" class="scr-editor-err">
                <AlertTriangle :size="18" />
                <p>{{ t('scr_fileReadFailed') }}</p>
                <p class="scr-editor-err-msg">{{ fileReadErr }}</p>
                <button class="scr-btn sm" @click="retryOpenFile">{{ t('scr_retry') }}</button>
              </div>
              <div v-else-if="!activeFile" class="scr-editor-err">
                <FileCode2 :size="18" />
                <p>{{ t('scr_noFiles') }}</p>
                <button class="scr-btn primary sm" @click="createEntryFile">
                  <FilePlus2 :size="13" /> {{ t('scr_createEntry') }}
                </button>
              </div>
              <ConfigEditor
                v-else :key="fileEditorKey" ref="editorRef"
                :model-value="fileText" :filename="editorFilename" @update:model-value="onFileInput" @save="saveFile"
              />
            </div>
          </div>

          <!-- 变量 -->
          <div v-show="tab === 'env'" class="scr-panel">
            <div class="scr-logsearch">
              <button class="scr-btn sm" :class="{ primary: envScope === 'script' }" @click="envScope = 'script'">
                {{ t('scr_envScopeScript') }}
              </button>
              <button class="scr-btn sm" :class="{ primary: envScope === 'global' }" @click="envScope = 'global'">
                {{ t('scr_envScopeGlobal') }}
              </button>
              <div class="scr-tabs-gap" />
            </div>
            <p class="scr-hint">{{ envScope === 'global' ? t('scr_envGlobalHint') : t('scr_envHint') }}</p>
            <table class="scr-table">
              <thead><tr>
                <th>{{ t('scr_envKey') }}</th><th>{{ t('scr_envValue') }}</th>
                <th>{{ t('scr_envRemark') }}</th><th class="w-90" />
              </tr></thead>
              <tbody>
                <tr v-for="e in scopeEnvs" :key="e.id">
                  <td class="mono">{{ e.key }}</td>
                  <td class="mono">
                    <span class="scr-env-val">
                      {{ e.secret && revealed[e.id] === undefined ? e.value : (revealed[e.id] ?? e.value) }}
                    </span>
                  </td>
                  <td class="muted">{{ e.remark }}</td>
                  <td class="scr-row-acts">
                    <button v-if="e.secret" :title="t('scr_reveal')" @click="toggleReveal(e)">
                      <EyeOff v-if="revealed[e.id] !== undefined" :size="13" /><Eye v-else :size="13" />
                    </button>
                    <button :title="t('scr_delete')" @click="removeEnv(e)"><Trash2 :size="13" /></button>
                  </td>
                </tr>
                <tr v-if="!scopeEnvs.length">
                  <td colspan="4" class="muted center">
                    {{ envScope === 'global' ? t('scr_envGlobalEmpty') : t('scr_envEmpty') }}
                  </td>
                </tr>
                <tr class="scr-env-new">
                  <td><input v-model="envDraft.key" placeholder="COOKIE" class="mono" /></td>
                  <td><input v-model="envDraft.value" :placeholder="t('scr_envValuePh')" class="mono" /></td>
                  <td><input v-model="envDraft.remark" :placeholder="t('scr_envRemarkPh')" /></td>
                  <td class="scr-row-acts">
                    <label class="scr-secret" :title="t('scr_envSecret')">
                      <input v-model="envDraft.secret" type="checkbox" />{{ t('scr_envSecretShort') }}
                    </label>
                    <button :title="t('scr_add')" @click="saveEnv"><Check :size="14" /></button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 历史 -->
          <div v-show="tab === 'runs'" class="scr-panel">
            <div class="scr-stats">
              <span>{{ t('scr_statTotal') }} <b>{{ stats?.total ?? 0 }}</b></span>
              <span>{{ t('scr_statRate') }} <b>{{ successRate }}</b></span>
              <span>{{ t('scr_statAvg') }} <b>{{ stats ? fmtDur(stats.avgMs) : '—' }}</b></span>
              <span>{{ t('scr_stat7d') }} <b>{{ stats?.last7ok ?? 0 }}/{{ stats?.last7total ?? 0 }}</b></span>
              <svg
                v-if="durations.length > 1" class="scr-spark" :title="t('scr_sparkHint')"
                :viewBox="`0 0 ${(durations.length - 1) * 6 + 2} 20`" preserveAspectRatio="none"
              >
                <polyline :points="sparkPoints" fill="none" stroke="var(--color-accent)" stroke-width="1.2" />
              </svg>
              <div class="scr-tabs-gap" />
              <button class="scr-btn ghost sm" @click="clearRuns"><Trash2 :size="12" /> {{ t('scr_clearRuns') }}</button>
            </div>
            <div class="scr-logsearch">
              <QdSearchBox
                v-model="logSearchKeyword"
                class="scr-search grow"
                :placeholder="t('scr_logSearchPh')"
                @submit="searchLogs"
              />
              <button class="scr-btn sm" :disabled="logSearchBusy" @click="searchLogs">
                <LoaderCircle v-if="logSearchBusy" :size="12" class="spin" />
                <Search v-else :size="12" />
                {{ t('scr_logSearch') }}
              </button>
            </div>
            <p v-if="logSearchFiles" class="scr-field-hint">
              {{ t('scr_logSearchResult', { n: logHits.length, files: logSearchFiles }) }}
            </p>
            <ul v-if="logHits.length" class="scr-hits">
              <li v-for="(h, i) in logHits" :key="i" @click="openHit(h)">
                <span class="mono muted">{{ fmtTime(h.time) }}</span>
                <span class="mono scr-hit-text">{{ h.text }}</span>
              </li>
            </ul>
            <table class="scr-table">
              <thead><tr>
                <th>{{ t('scr_runTime') }}</th><th>{{ t('scr_runTrigger') }}</th>
                <th>{{ t('scr_runStatus') }}</th><th>{{ t('scr_runDuration') }}</th><th />
              </tr></thead>
              <tbody>
                <tr v-for="r in runs" :key="r.id">
                  <td class="mono">{{ fmtTime(r.startedAt) }}</td>
                  <td>{{ triggerLabel(r.trigger) }}</td>
                  <td :style="{ color: statusColor(r.status) }">{{ t('scr_status_' + r.status) }}</td>
                  <td>{{ fmtDur(r.durationMs) }}</td>
                  <td class="scr-row-acts">
                    <button :title="t('scr_viewLog')" @click="viewRun(r)"><History :size="13" /></button>
                    <button v-if="r.status !== 'ok' && r.status !== 'running'" :title="t('scr_heal')" @click="heal(r)">
                      <Wand2 :size="13" />
                    </button>
                  </td>
                </tr>
                <tr v-if="!runs.length && !runsLoading"><td colspan="5" class="muted center">{{ t('scr_noRuns') }}</td></tr>
              </tbody>
            </table>
          </div>

          <!-- 设置 -->
          <div v-show="tab === 'settings'" class="scr-panel scr-form">
            <template v-if="form">
            <div class="scr-field"><label>{{ t('scr_name') }}</label>
              <input v-model="form.name" /></div>
            <div class="scr-field"><label>{{ t('scr_desc') }}</label>
              <input v-model="form.description" :placeholder="t('scr_descPh')" /></div>
            <div class="scr-row2">
              <div class="scr-field"><label>{{ t('scr_lang') }}</label>
                <select v-model="form.lang">
                  <option v-for="l in LANGS" :key="l" :value="l">{{ langText(l) }}</option>
                </select></div>
              <div class="scr-field"><label>{{ t('scr_entry') }}</label>
                <input v-model="form.entry" class="mono" />
                <span v-if="entryMissing" class="scr-field-hint warn">
                  {{ t('scr_entryMissing', { file: form.entry }) }}
                </span></div>
            </div>
            <div class="scr-row2">
              <div class="scr-field"><label>{{ t('scr_interpreter') }}</label>
                <input v-model="form.interpreter" class="mono" :placeholder="t('scr_interpreterPh')" />
                <span v-if="form.lang === 'go'" class="scr-field-hint">{{ t('scr_goHint') }}</span>
                <span v-else-if="form.lang === 'php'" class="scr-field-hint">{{ t('scr_phpHint') }}</span></div>
              <div class="scr-field"><label>{{ t('scr_args') }}</label>
                <input v-model="form.defaultArgs" class="mono" placeholder='["--days","7"]' /></div>
            </div>
            <div class="scr-row2">
              <div class="scr-field"><label>{{ t('scr_timeout') }}</label>
                <input v-model.number="form.timeoutSec" type="number" min="0" />
                <span class="scr-field-hint">{{ t('scr_timeoutHint') }}</span></div>
              <div class="scr-field"><label>{{ t('scr_concurrency') }}</label>
                <input v-model.number="form.concurrency" type="number" min="1" max="8" />
                <span class="scr-field-hint">{{ t('scr_concurrencyHint') }}</span></div>
            </div>
            <div class="scr-field"><label>{{ t('scr_tags') }}</label>
              <input v-model="form.tags" :placeholder="t('scr_tagsPh')" /></div>
            <div class="scr-field"><label>{{ t('scr_watchPath') }}</label>
              <div class="scr-inline">
                <input v-model="form.watchPath" class="mono" :placeholder="t('scr_watchPathPh')" />
                <button class="scr-btn sm" @click="testWatchPath">{{ t('scr_test') }}</button>
              </div>
              <span class="scr-field-hint">{{ t('scr_watchPathHint') }}</span></div>
            <div class="scr-switches">
              <label><QdSwitch v-model="form.autoInstall" /> {{ t('scr_autoInstall') }}</label>
              <label><QdSwitch v-model="form.mcpAllowed" /> {{ t('scr_mcpAllowed') }}</label>
              <label><QdSwitch v-model="form.pinned" /> {{ t('scr_pin') }}</label>
            </div>
            <div class="scr-field">
              <label>{{ t('scr_bindings') }}</label>
              <p v-if="!bindings.length" class="scr-field-hint">{{ t('scr_bindingsNone') }}</p>
              <ul v-else class="scr-bindings">
                <li v-for="b in bindings" :key="b.id">
                  <span class="scr-binding-name">{{ b.name }}</span>
                  <code class="mono">{{ b.expr }}</code>
                  <span class="muted">{{ b.enabled ? t('scr_bindingOn') : t('scr_bindingOff') }}</span>
                  <span v-if="b.nextRun" class="muted">{{ t('scr_bindingNext') }} {{ fmtTime(b.nextRun) }}</span>
                </li>
              </ul>
            </div>
            <p class="scr-danger-hint"><AlertTriangle :size="13" /> {{ t('scr_securityHint') }}</p>
            <div class="scr-form-actions">
              <button class="scr-btn primary" @click="saveSettings"><Save :size="14" /> {{ t('scr_save') }}</button>
            </div>
            </template>
          </div>
        </template>
      </section>
      </template>
    </div>

    <!-- 实时 / 历史日志 -->
    <div v-if="current && logPanelOpen" class="scr-log">
      <div class="scr-log-head">
        <span class="scr-log-title">
          {{ viewingRun ? t('scr_logHistory') : t('scr_logLive') }}
          <span v-if="running" class="scr-running-dot" />
        </span>
        <div class="scr-tabs-gap" />
        <QdSearchBox v-model="logFilter" class="scr-search sm" :icon-size="12" :placeholder="t('scr_logFilterPh')" />
        <button class="scr-btn ghost sm" :class="{ active: logErrOnly }" @click="logErrOnly = !logErrOnly">
          {{ t('scr_logErrOnly') }}
        </button>
        <button class="scr-btn ghost sm" :class="{ active: logFollow }" @click="logFollow = !logFollow">
          {{ t('scr_logFollow') }}
        </button>
        <button v-if="viewingRun" class="scr-btn ghost sm" @click="viewingRun = null; runLogText = ''">
          {{ t('scr_backLive') }}
        </button>
        <button class="scr-btn ghost sm" @click="copyLog"><Copy :size="12" /> {{ t('scr_copy') }}</button>
        <button class="scr-btn ghost sm" @click="viewingRun ? (runLogText = '') : (logLines = [])">
          {{ t('scr_clear') }}
        </button>
      </div>
      <div ref="logBox" class="scr-log-body" @scroll.passive="onLogScroll">
        <template v-if="viewingRun">
          <p v-if="runLogMissing" class="muted">{{ t('scr_logMissing') }}</p>
          <pre class="scr-log-pre">{{ runLogText || t('scr_logEmpty') }}</pre>
        </template>
        <template v-else>
          <p v-if="!logLines.length" class="muted">{{ t('scr_logIdle') }}</p>
          <p v-else-if="!filteredLogs.length" class="muted">{{ t('scr_logNoMatch') }}</p>
          <pre v-else class="scr-log-pre"><span
            v-for="(l, i) in filteredLogs" :key="i"
            :class="{ 'scr-line-err': l.stream === 'stderr' }"
          >{{ l.text }}
</span></pre>
        </template>
      </div>
    </div>

    <!-- 新建脚本 -->
    <QdModal v-if="createOpen" :model-value="true" :title="t('scr_new')" @update:model-value="createOpen = false">
      <div class="scr-form">
        <div class="scr-field"><label>{{ t('scr_name') }}</label>
          <input v-model="createDraft.name" :placeholder="t('scr_namePh')" @keyup.enter="doCreate" /></div>
        <div class="scr-row2">
          <div class="scr-field"><label>{{ t('scr_lang') }}</label>
            <select v-model="createDraft.lang">
              <option v-for="l in LANGS" :key="l" :value="l">{{ langText(l) }}</option>
            </select></div>
          <div class="scr-field"><label>{{ t('scr_entry') }}</label>
            <input v-model="createDraft.entry" class="mono" :placeholder="t('scr_entryPh')" /></div>
        </div>
        <div class="scr-field"><label>{{ t('scr_tags') }}</label>
          <input v-model="createDraft.tags" :placeholder="t('scr_tagsPh')" /></div>
      </div>
      <template #footer>
        <button class="scr-btn" @click="createOpen = false">{{ t('scr_cancel') }}</button>
        <button class="scr-btn primary" @click="doCreate">{{ t('scr_create') }}</button>
      </template>
    </QdModal>

    <!-- 运行日志详情 + AI 建议 -->
    <QdModal
      v-if="viewingRun" :model-value="true" width="min(920px, 92vw)"
      :title="t('scr_runDetail', { time: fmtTime(viewingRun.startedAt), trigger: triggerLabel(viewingRun.trigger) })"
      @update:model-value="viewingRun = null; runLogText = ''"
    >
      <div class="scr-detail">
        <div class="scr-detail-meta">
          <span :style="{ color: statusColor(viewingRun.status) }">{{ t('scr_status_' + viewingRun.status) }}</span>
          <span>{{ t('scr_exitCode') }}: {{ viewingRun.exitCode }}</span>
          <span>{{ fmtDur(viewingRun.durationMs) }}</span>
          <div class="scr-tabs-gap" />
          <button v-if="!patch" class="scr-btn sm" :disabled="healing" @click="heal(viewingRun)">
            <LoaderCircle v-if="healing" :size="12" class="spin" /><Wand2 v-else :size="12" />
            {{ t('scr_heal') }}
          </button>
        </div>
        <div v-if="patch" class="scr-patch">
          <div class="scr-patch-head"><Wand2 :size="14" /> {{ t('scr_healTitle') }}</div>
          <p><b>{{ t('scr_healDiagnosis') }}</b>{{ patch.diagnosis }}</p>
          <p v-if="patch.fix"><b>{{ t('scr_healFix') }}</b>{{ patch.fix }}</p>
          <p v-if="patch.model" class="muted">{{ t('scr_healModel') }}: {{ patch.model }}</p>
          <div v-if="patch.content" class="scr-form-actions">
            <button class="scr-btn primary" @click="applyPatch">
              <Check :size="13" /> {{ t('scr_healApply') }}
            </button>
          </div>
        </div>
        <pre class="scr-log-pre tall">{{ runLogText || t('scr_logEmpty') }}</pre>
      </div>
    </QdModal>

    <!-- HTTP 触发 -->
    <QdModal v-if="triggerOpen" :model-value="true" width="min(560px, 92vw)" :title="t('scr_trigger')" @update:model-value="triggerOpen = false">
      <div class="scr-form">
        <p class="scr-hint">{{ t('scr_triggerHint') }}</p>
        <div class="scr-detail-meta">
          <span :style="{ color: trigger.running ? 'var(--color-success)' : 'var(--color-text-muted)' }">
            {{ trigger.running ? t('scr_triggerOn') : t('scr_triggerOff') }}
          </span>
          <span v-if="trigger.port">:{{ trigger.port }}</span>
        </div>
        <div v-if="trigger.token" class="scr-field">
          <label>{{ t('scr_token') }}</label>
          <div class="scr-inline">
            <input :value="trigger.token" readonly class="mono" />
            <button class="scr-btn sm" @click="copyToken">{{ t('scr_copy') }}</button>
            <button class="scr-btn sm" @click="rotateToken">{{ t('scr_rotate') }}</button>
          </div>
          <span class="scr-field-hint">{{ t('scr_tokenHint') }}</span>
        </div>
        <div v-if="current && trigger.token" class="scr-field">
          <label>{{ t('scr_triggerUrl') }}</label>
          <input :value="triggerUrlFor(current.id)" readonly class="mono" />
          <span class="scr-field-hint">{{ t('scr_triggerUrlHint') }}</span>
        </div>
      </div>
      <template #footer>
        <button v-if="!trigger.running" class="scr-btn primary" @click="startTrigger">{{ t('scr_triggerStart') }}</button>
        <button v-else class="scr-btn danger" @click="stopTrigger">{{ t('scr_triggerStop') }}</button>
      </template>
    </QdModal>
  </div>
</template>

<style scoped>
.scr-page { display: flex; flex-direction: column; height: 100%; overflow: hidden; background: var(--color-bg-primary); }
.scr-count { font-size: var(--text-sm); font-weight: 500; color: var(--color-text-muted); }
.scr-head-actions { display: flex; align-items: center; gap: var(--space-3); }
/* 动作组之间的细竖线：导入/刷新/新建挤在一起时，靠它分组而不是靠距离 */
.scr-sep { width: 1px; height: 16px; flex-shrink: 0; background: var(--color-border-light); }

/* ---------- 空状态引导页（骨架复用 styles/guide.css 的 .qd-guide） ---------- */
.scr-guide-icon { width: 56px; height: 56px; }
/* 齿轮慢转，代码符号不动：整块转起来会像加载中，只转外圈才是「机器在待命」 */
.scr-guide-gear { transform-origin: 12px 12px; animation: scr-gear 22s linear infinite; }
@keyframes scr-gear {
  to { transform: rotate(360deg); }
}
@media (prefers-reduced-motion: reduce) {
  .scr-guide-gear { animation: none; }
}
.scr-guide-tpl-title { margin: 0 0 10px; font-size: var(--text-xs); color: var(--color-text-muted); }
.scr-guide-steps { margin: 0 0 14px; font-size: var(--text-xs); line-height: 1.6; color: var(--color-text-muted); }
/* 200px 是这条容器的分界：640 宽的引导页放 2 列（3 列会因为余量不足掉成 3+1） */
.scr-tpl-grid {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-3); width: 100%; margin-bottom: 18px;
}
.scr-tpl {
  display: flex; flex-direction: column; gap: 3px;
  padding: 9px 11px; text-align: left; font-family: inherit; cursor: pointer;
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  transition: border-color var(--transition-fast), background-color var(--transition-fast);
}
.scr-tpl:hover:not(:disabled) { border-color: var(--color-accent-border); background: var(--color-bg-hover); }
.scr-tpl:disabled { color: var(--color-text-disabled); background: var(--color-bg-tertiary); border-color: var(--color-border); cursor: default; }
.scr-tpl-head { display: flex; align-items: center; justify-content: space-between; }
.scr-tpl-lang {
  padding: 0 6px; line-height: 16px; border-radius: var(--radius-full);
  background: var(--color-accent-bg); color: var(--color-accent); font-size: var(--text-2xs);
}
.scr-tpl-arrow { color: var(--color-text-muted); transition: color var(--transition-fast), transform var(--transition-fast); }
.scr-tpl:hover:not(:disabled) .scr-tpl-arrow { color: var(--color-accent); transform: translateX(2px); }
.scr-tpl-name { font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary); }
.scr-tpl-desc { font-size: var(--text-xs); line-height: 1.5; color: var(--color-text-muted); }
/* 搜索框皮肤由 QdSearchBox 统一提供（无边框 / 28px / bg-tertiary）。
   原来宽度是靠内部 input 的 width 撑开的，改组件后 input 是 flex:1，
   所以宽度必须落在盒子上，否则会在 flex 行里塌成 0。 */
.scr-search { width: 150px; }
.scr-btn { display: inline-flex; align-items: center; gap: 5px; height: 30px; padding: 0 var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-secondary); font-size: var(--text-sm); font-family: inherit; cursor: pointer; }
.scr-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.scr-btn.primary { background: var(--color-accent-bg); border-color: var(--color-accent-border); color: var(--color-accent); }
.scr-btn.danger { color: var(--color-danger-ink); }
.scr-btn.sm { height: 26px; font-size: var(--text-xs); padding: 0 var(--space-2); }
.scr-btn.ghost { background: transparent; border-color: transparent; }
.scr-btn:disabled { color: var(--color-text-disabled); background: var(--color-bg-tertiary); border-color: var(--color-border); cursor: default; }
.scr-btn.ghost.active { background: var(--color-accent-bg); color: var(--color-accent); }
/* 筛选胶囊：语言下拉与「只看失败」共用一套外观，替代原来的裸 select + 复选框 */
.scr-pill {
  position: relative; display: inline-flex; align-items: center; gap: 5px;
  height: 30px; padding: 0 var(--space-4);
  border: 1px solid var(--color-border); border-radius: var(--radius-full);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-size: var(--text-xs); font-family: inherit; white-space: nowrap; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.scr-pill:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }
.scr-pill.on { background: var(--color-accent-bg); border-color: var(--color-accent-border); color: var(--color-accent); }
/* 原生 select 铺满整个胶囊（透明），点击即弹系统下拉 —— 保留键盘与无障碍行为 */
.scr-pill-select {
  position: absolute; inset: 0; width: 100%; height: 100%;
  border: 0; padding: 0; margin: 0; opacity: 0; cursor: pointer; appearance: none;
}
.scr-args { width: 132px; height: 30px; background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); color: var(--color-text-primary); font-size: var(--text-xs); padding: 0 var(--space-2); }
.scr-args::placeholder { color: var(--color-text-muted); }
.scr-search.sm { width: 110px; }
.scr-search.grow { flex: 1 1 auto; width: auto; }
.scr-banner { display: flex; align-items: center; gap: var(--space-2); margin: 0 var(--space-4) var(--space-2); padding: var(--space-2) var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-secondary); font-size: var(--text-xs); color: var(--color-text-secondary); }
.scr-banner.warn { border-color: var(--color-warning); color: var(--color-warning-ink); }
.scr-banner span { flex: 1 1 auto; }
.scr-bindings { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
.scr-bindings li { display: flex; align-items: center; gap: var(--space-3); font-size: var(--text-xs); padding: 4px var(--space-2); background: var(--color-bg-tertiary); border-radius: var(--radius-sm); }
.scr-binding-name { color: var(--color-text-primary); }
.scr-logsearch { display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2); }
.scr-hits { list-style: none; margin: 0 0 var(--space-3); padding: 0; max-height: 180px; overflow: auto; border: 1px solid var(--color-border); border-radius: var(--radius-sm); }
.scr-hits li { display: flex; gap: var(--space-3); padding: 3px var(--space-2); font-size: var(--text-xs); cursor: pointer; border-bottom: 1px solid var(--color-border); }
.scr-hits li:last-child { border-bottom: none; }
.scr-hits li:hover { background: var(--color-bg-hover); }
.scr-hit-text { white-space: pre-wrap; word-break: break-all; color: var(--color-text-secondary); }
.scr-next { display: inline-flex; align-items: center; gap: 2px; color: var(--color-accent); }
.scr-spark { width: 86px; height: 18px; flex-shrink: 0; opacity: .85; }

.scr-body { display: flex; flex: 1 1 auto; min-height: 0; }
.scr-boot { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--color-text-muted); }
/* 列表栏给一档更亮的底色 + 深一档的分割线：深色下只靠 1px 边框分不开两块区域 */
.scr-list {
  width: 250px; flex-shrink: 0; border-right: 1px solid var(--color-border-light);
  background: var(--color-bg-secondary);
  overflow-y: auto; padding: var(--space-2); display: flex; flex-direction: column; gap: 2px;
}
.scr-list-empty { display: flex; flex-direction: column; align-items: center; gap: var(--space-3); padding: var(--space-8) var(--space-4); color: var(--color-text-secondary); font-size: var(--text-sm); text-align: center; }
.scr-list-empty p { margin: 0; }
.scr-item { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-3); border: 1px solid transparent; border-radius: var(--radius-sm); background: transparent; text-align: left; cursor: pointer; color: var(--color-text-primary); font-family: inherit; }
.scr-item:hover { background: var(--color-bg-hover); }
.scr-item.active { background: var(--color-accent-bg); border-color: var(--color-accent-border); }
.scr-item-dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }
.scr-item-main { display: flex; flex-direction: column; min-width: 0; flex: 1 1 auto; gap: 2px; }
.scr-item-name { display: flex; align-items: center; gap: 4px; font-size: var(--text-sm); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.scr-pin { color: var(--color-accent); }
.scr-item-meta { display: flex; gap: var(--space-2); font-size: var(--text-xs); color: var(--color-text-muted); }
.scr-lang { padding: 0 4px; border-radius: 3px; background: var(--color-bg-tertiary); }
.scr-item-act { opacity: 0; }
.scr-item:hover .scr-item-act { opacity: 1; }
.scr-item-act button { background: transparent; border: none; color: var(--color-text-muted); cursor: pointer; padding: 2px; }
.scr-item-act button:hover { color: var(--color-accent); }

.scr-main { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; }
.scr-placeholder { flex: 1 1 auto; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--space-3); color: var(--color-text-secondary); }
.scr-placeholder > svg { color: var(--color-text-muted); }
.scr-placeholder p { margin: 0; }
.scr-placeholder-sub { font-size: var(--text-sm); color: var(--color-text-muted); }
.scr-toolbar { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-3) var(--space-5); border-bottom: 1px solid var(--color-border); }
.scr-title-wrap { display: flex; flex-direction: column; min-width: 0; }
.scr-title-wrap h3 { margin: 0; font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.scr-sub { font-size: var(--text-xs); color: var(--color-text-muted); font-family: var(--font-mono); }
.scr-run-actions { display: flex; align-items: center; gap: var(--space-2); margin-left: auto; }
.scr-status { font-size: var(--text-xs); }

.scr-tabs { display: flex; align-items: center; gap: var(--space-1); padding: var(--space-2) var(--space-5) 0; border-bottom: 1px solid var(--color-border); }
.scr-tabs > button:not(.scr-btn) { padding: var(--space-2) var(--space-3); background: transparent; border: none; border-bottom: 2px solid transparent; color: var(--color-text-muted); font-size: var(--text-sm); font-family: inherit; cursor: pointer; display: inline-flex; align-items: center; gap: 5px; }
.scr-tabs > button:not(.scr-btn):hover { color: var(--color-text-primary); }
.scr-tabs > button.active { color: var(--color-accent-ink); border-bottom-color: var(--color-accent); }
.scr-badge { font-size: var(--text-2xs); background: var(--color-bg-tertiary); border-radius: var(--radius-md); padding: 0 5px; }
.scr-tabs-gap { flex: 1 1 auto; }

.scr-code { flex: 1 1 auto; display: flex; min-height: 0; }
.scr-files { width: 180px; flex-shrink: 0; border-right: 1px solid var(--color-border); overflow-y: auto; padding: var(--space-2); }
.scr-files-head { display: flex; align-items: center; justify-content: space-between; font-size: var(--text-xs); color: var(--color-text-muted); padding: 0 var(--space-2) var(--space-2); }
.scr-files-head button { background: transparent; border: none; color: var(--color-text-muted); cursor: pointer; }
.scr-files-head button:hover { color: var(--color-accent); }
.scr-file { display: flex; align-items: center; gap: 5px; width: 100%; padding: 4px var(--space-2); background: transparent; border: none; border-radius: var(--radius-sm); color: var(--color-text-secondary); font-size: var(--text-xs); font-family: var(--font-mono); cursor: pointer; text-align: left; }
.scr-file:hover { background: var(--color-bg-hover); }
.scr-file.active { background: var(--color-accent-bg); color: var(--color-accent); }
.scr-file-name { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.scr-file-del { opacity: 0; }
.scr-file:hover .scr-file-del { opacity: 1; }
.scr-file-new { padding: 2px var(--space-2) var(--space-2); }
.scr-file-new input { width: 100%; background: var(--color-bg-tertiary); border: 1px solid var(--color-accent-border); border-radius: var(--radius-sm); padding: 2px 5px; color: var(--color-text-primary); font-size: var(--text-xs); font-family: var(--font-mono); }
.scr-files-empty { font-size: var(--text-xs); color: var(--color-text-muted); padding: var(--space-2); }
.scr-editor { flex: 1 1 auto; min-width: 0; min-height: 0; display: flex; }
.scr-editor-loading { flex: 1 1 auto; display: flex; align-items: center; justify-content: center; color: var(--color-text-muted); }
.scr-editor-err {
  flex: 1 1 auto; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: var(--space-2); padding: var(--space-6); color: var(--color-text-muted); text-align: center;
}
.scr-editor-err p { margin: 0; font-size: var(--text-sm); }
.scr-editor-err-msg {
  max-width: 70%; font-size: var(--text-xs) !important; font-family: var(--font-mono);
  color: var(--color-text-muted); word-break: break-all;
}
/* 未保存指示点：在文件树、代码页签、保存按钮上共用一个语义 */
.scr-dirty-dot {
  width: 6px; height: 6px; border-radius: 50%; background: var(--color-warning);
  flex: none; display: inline-block; margin-left: 4px;
}

.scr-panel { flex: 1 1 auto; overflow: auto; padding: var(--space-4) var(--space-5); }
.scr-hint { font-size: var(--text-xs); color: var(--color-text-muted); margin: 0 0 var(--space-3); line-height: 1.6; }
.scr-table { width: 100%; border-collapse: collapse; font-size: var(--text-sm); }
.scr-table th { text-align: left; font-weight: 500; color: var(--color-text-muted); font-size: var(--text-xs); padding: var(--space-2); border-bottom: 1px solid var(--color-border); }
.scr-table td { padding: var(--space-2); border-bottom: 1px solid var(--color-border); color: var(--color-text-secondary); }
.scr-table input { width: 100%; background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); padding: 3px 6px; color: var(--color-text-primary); font-size: var(--text-xs); font-family: inherit; }
.scr-row-acts { display: flex; align-items: center; gap: 4px; justify-content: flex-end; }
.scr-row-acts button { background: transparent; border: none; color: var(--color-text-muted); cursor: pointer; padding: 2px; }
.scr-row-acts button:hover { color: var(--color-accent); }
.scr-env-val { word-break: break-all; }
.scr-secret { display: inline-flex; align-items: center; gap: 3px; font-size: var(--text-xs); color: var(--color-text-muted); white-space: nowrap; }
.mono { font-family: var(--font-mono); }
.muted { color: var(--color-text-muted); }
.center { text-align: center; }
.w-90 { width: 90px; }

.scr-stats { display: flex; align-items: center; gap: var(--space-5); font-size: var(--text-xs); color: var(--color-text-muted); margin-bottom: var(--space-3); }
.scr-stats b { color: var(--color-text-primary); font-weight: 600; }

.scr-form { display: flex; flex-direction: column; gap: var(--space-3); }
.scr-field { display: flex; flex-direction: column; gap: 4px; flex: 1 1 0; min-width: 0; }
.scr-field label { font-size: var(--text-xs); color: var(--color-text-muted); }
.scr-field input, .scr-field select { background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); padding: 6px var(--space-3); color: var(--color-text-primary); font-size: var(--text-sm); font-family: inherit; }
.scr-field input:focus, .scr-field select:focus { outline: none; border-color: var(--color-accent-border); }
.scr-field-hint { font-size: 11px; color: var(--color-text-muted); }
.scr-field-hint.warn { color: var(--color-warning-ink); }
.scr-row2 { display: flex; gap: var(--space-3); }
.scr-inline { display: flex; align-items: center; gap: var(--space-2); }
.scr-inline input { flex: 1 1 auto; }
.scr-switches { display: flex; gap: var(--space-6); font-size: var(--text-sm); color: var(--color-text-secondary); }
.scr-switches label { display: inline-flex; align-items: center; gap: var(--space-2); cursor: pointer; }
.scr-danger-hint { display: flex; gap: 5px; align-items: flex-start; font-size: var(--text-xs); color: var(--color-warning-ink); margin: 0; line-height: 1.5; }
.scr-form-actions { display: flex; justify-content: flex-end; gap: var(--space-2); }

.scr-log { flex-shrink: 0; height: 220px; display: flex; flex-direction: column; border-top: 1px solid var(--color-border); background: var(--color-bg-secondary); }
.scr-log-head { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-5); border-bottom: 1px solid var(--color-border); }
.scr-log-title { font-size: var(--text-xs); color: var(--color-text-muted); display: inline-flex; align-items: center; gap: 5px; }
.scr-running-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--color-accent); animation: scr-pulse 1.2s infinite; }
@keyframes scr-pulse { 0%, 100% { opacity: 1 } 50% { opacity: .3 } }
.scr-log-body { flex: 1 1 auto; overflow: auto; padding: var(--space-2) var(--space-5); }
.scr-log-pre { margin: 0; font-family: var(--font-mono); font-size: 11.5px; line-height: 1.55; color: var(--color-text-secondary); white-space: pre-wrap; word-break: break-all; }
.scr-log-pre.tall { flex: 1 1 auto; overflow: auto; max-height: 52vh; }
.scr-line-err { color: var(--color-danger-ink); }

.scr-detail { display: flex; flex-direction: column; gap: var(--space-3); }
.scr-detail-meta { display: flex; align-items: center; gap: var(--space-4); font-size: var(--text-xs); color: var(--color-text-muted); }
.scr-patch { background: var(--color-popover-bg); border: 1px solid var(--color-border); border-radius: var(--radius-sm); padding: var(--space-3); font-size: var(--text-sm); color: var(--color-text-secondary); }
.scr-patch p { margin: 0 0 var(--space-2); line-height: 1.6; }
.scr-patch p b { color: var(--color-text-primary); font-weight: 500; margin-right: 5px; }
.scr-patch-head { display: flex; align-items: center; gap: 5px; color: var(--color-accent); font-size: var(--text-xs); margin-bottom: var(--space-2); }

.spin { animation: scr-spin .9s linear infinite; }
@keyframes scr-spin { to { transform: rotate(360deg) } }
</style>
