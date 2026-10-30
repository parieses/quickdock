<script setup lang="ts">
import { ref, computed, reactive, onMounted, onUnmounted, onActivated, onDeactivated, inject, watch, nextTick, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import {
  Bot, Plus, Trash2, Copy, Square, RefreshCw, StickyNote, ArrowDown, ArrowUp, Image as ImageIcon,
  Link2, X, Download, Pencil, Search, Wrench, FolderOpen, Folder, ChevronRight, ChevronDown,
  MessageSquare, Languages, FileText, GraduationCap, Lightbulb, Globe, Link as LinkIcon,
  FolderTree, FileSearch, GitBranch, PenLine, Terminal, Play, Bug, Clock, FilePlus,
  ListChecks, Bell, LayoutList, Network, Undo2,
} from '@lucide/vue'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { unwrap } from '../utils/api'
import AgentRunTree from './AgentRunTree.vue'
import AiDiffView from './AiDiffView.vue'
import { SetAIPageVisible } from '../../bindings/quickdock/services/appservice'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { splitMarkdownBlocks as splitBlocks } from '../utils/markdownBlocks'
import { stripDsmlMarkup } from '../utils/dsmlMarkup'
import { isMathFenceLang, mathExtension, renderMath } from '../utils/mathMarkdown'
import {
  handleMermaidToolbarClick,
  isMermaidFenceLang,
  mermaidPlaceholder,
  renderMermaidIn,
  setMermaidFolded,
  setMermaidSourceMode,
  type MermaidLabels,
} from '../utils/mermaidMarkdown'
import { copyElementAsImage, downloadBlob, serializeMermaidSvg, svgToPngBlob, resolveBackground } from '../utils/elementImage'
// 代码块语法高亮：common 子集覆盖 AI 常见语言（js/ts/python/go/bash/json/sql/html...），
// 比全量包小很多。主题配色见 styles/hljs-theme.css（跟随 <html data-theme>）。
import hljs from 'highlight.js/lib/common'
import '../styles/hljs-theme.css'
import { useFloatMenu } from '../composables/useFloatMenu'
import { aiGenerating, aiFinishedUnread } from '../composables/bridge'
import { CreateNoteDoc } from '../../bindings/quickdock/services/note/noteservice'
import type { ToastAPI } from '../types'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AIConvHit, AIProfilesResult } from '../types/ai'

import {
  AIListProfiles,
  AISetActiveProfile,
  AIStreamInfo,
  AIRegenerateTitle,
  AIRenameConversation,
  AIListConversations,
  AIDeleteConversation,
  AIGetMessages,
  AILoadImage,
  AIListSpaces,
  AICreateSpace,
  AIRenameSpace,
  AIDeleteSpace,
  AIApproveToolCall,
  AIListCheckpoints,
  AIRestoreCheckpoint,
  AISuggestMentions,
  AISearchMessages,
} from '../../bindings/quickdock/services/ai/aiservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'

// 这个 name 是 App.vue 里 <KeepAlive :include> 的匹配依据，改它等于让 AI 页失去保活。
// 为什么必须显式声明：页面是经 defineAsyncComponent(import(...)) 注册的，而 KeepAlive 判断
// 异步组件时读的是 `vnode.type.__asyncResolved`（内层 SFC 定义）上的名字 ——
// 在包装器上赋 name 完全无效，不声明就永远匹配不上，切页照样销毁组件。
defineOptions({ name: 'AIPageKeepAlive' })

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 任务 = 一条对话。界面上叫「任务」而不是「会话」：它对应的是「我交给 AI 的一件事」，
// 而且创建时要先选类型（翻译 / 总结 / 解释 …），本来就不是闲聊。
interface AIConversation {
  id: string; title: string; summary: string
  prompt_tokens: number; completion_tokens: number
  created_at: string; updated_at: string
  cwd?: string
  // 所属空间（空 = 普通任务，侧栏列在「任务」组里）
  space_id?: string
  // 创建时选定的类型。后端对老数据回落 chat，这里同样兜底。
  mode?: string
}
// 空间 = 一个目录 + 挂在这个目录下的若干任务。
interface AISpace { id: string; name: string; cwd: string; created_at: string; updated_at: string }
interface AIMessage {
  id: string; conv_id: string; role: string; content: string; created_at: string
  reasoning_content?: string; images?: string; imgList?: AIMsgImage[]
  // 该条回复自己那一次调用的用量（assistant 才有值，老消息为 0/undefined）
  model?: string; prompt_tokens?: number; completion_tokens?: number; cached_tokens?: number
  // 这条回复的估算费用（元）。后端按当前价目表现算，未收录的模型没有这个字段。
  cost?: number
  // 这条回复被用户中途停止（内容是半截的）。后端同样落库，刷新后标记仍在。
  stopped?: boolean
  // 这条回复期间的工具调用记录（JSON 数组字符串，后端落库原文）。
  // 空串/undefined = 没调过工具。不解析成对象是因为它只在渲染那一处用到，
  // 提前解析会让每条消息都多一份副本，而绝大多数消息用不上。
  tool_calls?: string
  // 用了几轮工具调用（0/undefined = 纯文本回答）
  tool_rounds?: number
}

// 一条工具调用（前端展示用）。running 只在流式期间为 true：
// 历史记录从库里读回来时一定是已完成的，没有中间态。
interface AIToolCall {
  id: string; name: string; args?: string
  result?: string; error?: string; ms?: number
  round?: number; running?: boolean
  // dedup 与本次提问里之前某次调用完全相同，宿主跳过了执行。
  dedup?: boolean
  // denied 被用户拒绝（审批闸门），没有执行。与 dedup 分开：两个都是「没跑」，
  // 但原因不同——一个是「同样的事刚做过」，一个是「你拒绝了」，卡片上要说不同的话。
  denied?: boolean
  // 审批（见 ai_approval.go）：写类与高危工具在宿主侧停下来等用户点按钮。
  // pending 只在流式期间存在；allowed / denied 是点完之后的终态，
  // expired 表示卡片还在但后端已经不等了（会话结束 / 用户点了停止）。
  approval?: 'pending' | 'allowed' | 'denied' | 'expired'
  riskNote?: string
  rememberable?: boolean
  // 「记住」的范围：命令类工具按命令族记住，这里放族名（如 npm），
  // 按钮上要写明「允许 npm 命令」，否则用户以为自己只批准了这一条。
  grantScope?: string
  // 审批归属的会话：后端下发而不是读 activeId —— 用户在等待期间可能切走，
  // 切回来再点时 activeId 已经不是原来那个会话了。
  approvalConvId?: string
  // 改动行数（file_edit 有值）：卡片摘要行右侧显示 +N / -M。
  added?: number; removed?: number
  // 行级 diff（file_edit / file_write）：数字说明「改了多少」，它说明「改成了什么」。
  // 只有数字时用户只知道动了两行，那正是「不敢让它改代码」的原因。
  diff?: string
  // 这条调用所属的编排 run / 节点（只有 spawn_agent 才有）。
  // 有了它才能在回复下面挂出 run 树，不必去解析可能被截断的结果文本。
  run_id?: string; node_id?: string
}

// 一条编辑快照（回滚点）。Wails 只在 bound 参数里生成类型，返回值里的结构
// （ApiResult.data）是 any，所以这里按后端 AICheckpointView 的 JSON 手写一份。
interface AICheckpointView {
  id: string; seq: number; tool: string; tool_call_id: string
  created_at: string
  files: { path: string; existed: boolean; mode: number; backup: string; bytes: number }[]
}

// 一个生效的规则文件（后端 ai_rules.go 的 aiRuleFile）。
interface AIRuleFile { name: string; bytes: number; truncated?: boolean }

// @ 补全列表的一项（后端 AIMentionItem，或本地拼的会话项）。
// 会话项没有 path，只有 convId。
interface AIMentionItem { kind: 'file' | 'dir' | 'conv'; name: string; path?: string; convId?: string }

// 一条图片引用：url 类直发链接；file 类本地图（dataUrl 为内存态，仅当前会话/已懒加载后存在，
// path 为落盘后相对数据目录的路径，重载会话时由 AILoadImage 读回 dataUrl）。
interface AIMsgImage { kind: 'file' | 'url'; url?: string; path?: string; name?: string; dataUrl?: string }

// 待发送附件（发送前预览，尚未落库/组包）。
interface PendingImage { kind: 'file' | 'url'; url?: string; dataUrl?: string; name: string }

const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

const conversations = ref<AIConversation[]>([])
const spaces = ref<AISpace[]>([])
const activeId = ref('')
const messages = ref<AIMessage[]>([])
const input = ref('')
const mode = ref('chat')
const streaming = ref(false)
const errorMsg = ref('')

// 新建任务的待定归属：选了目录就有 spaceId，没选就是普通任务。
//
// 只在「任务还没发出第一条消息」这段时间内存在 —— 会话是首次发送时由后端创建的，
// 在那之前没有 id 可写，只能先记在前端，发送时随请求带过去。
const pendingSpaceId = ref('')
// 侧栏里展开了的空间 id（默认收起：空间一多，全展开等于没有分组）。
const expandedSpaces = ref<string[]>([])

// 本次对话绑定的工作目录（绝对路径，空 = 没绑定）。
//
// 存后端而不是只放前端：切页/重启后 AI 还得知道该在哪个目录里干活。
// 新会话在发送前还没有 id，所以这里允许「先选、发送时随请求带过去」——
// 后端建会话时会把它写进 ai_conversations.cwd。
const convCwd = ref('')
// 目录名只显示最后一段（完整路径放 title），否则 toolbar 会被长路径撑开。
// 没绑目录时是空串，工具栏那一处据此整块不显示。
const cwdLabel = computed(() => {
  const p = convCwd.value.trim()
  if (!p) return ''
  const parts = p.replace(/\\/g, '/').split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1] : p
})

// —— 流式文本缓冲 ——
// 关键性能点：token 到达时不直接写响应式 ref。改为先追加到普通字符串缓冲，
// 再按 ~16ms 的节奏批量提交到 streamingText。原因是每个 token 都触发一次
// 响应式更新 → 整个 AIPage 组件（含全部历史消息的 v-html diff）重渲染，
// 上游吐字越快掉帧越明显。批量提交把渲染次数从「每 token 一次」压到「每帧一次」。
const streamingText = ref('')
let streamBuf = ''
let streamReasonBuf = ''
let streamTimer: ReturnType<typeof setTimeout> | null = null
const STREAM_FLUSH_MS = 16

// —— Markdown 分块增量渲染 ——
// 性能根因：流式期间若每帧都对全文做 md.parse + v-html 整段替换，成本随回复
// 变长线性上升（实测 15KB 全文单帧 md.parse 约 10.5ms，其中 lexer 占 5ms），
// 而 v-html 的 innerHTML 赋值会销毁并重建该消息的全部 DOM 节点。
// 两者叠加，就是「AI 一直在回复但界面发卡」的根因。
// 改为按顶层块增量渲染：
//   · 文本以「围栏代码块外的空行」为界切块，块内结构不跨空行拆散
//   · 每块的 HTML 单独缓存；已完成块复用同一字符串引用 → Vue 判定值未变，不写 DOM
//   · 每帧只重新解析最后一个未完成块（实测 10.5ms/帧 → 0.75ms/帧，约 14×）
// 流式态与完成态使用完全相同的 DOM 结构（都是 .ai-md-block 列表），
// 因此排版逐像素一致，消息落地时不会出现视觉跳变。
interface LiveBlock { raw: string; html: string }
const streamBlocks = ref<LiveBlock[]>([])

// —— 流式期间的工具调用卡片 ——
// 与 streamingText 分开存放：工具卡片是离散事件（开始/结束各一次），
// 不是逐字增长的文本，混进同一个缓冲会打乱增量渲染的「只有最后一块在变」前提。
const streamTools = ref<AIToolCall[]>([])

// 本轮生效的项目规则文件（后端 rules 事件）。
//
// 为什么要显示：规则文件（AGENTS.md 等）是「改了就该生效」的东西，而生效与否
// 用户完全看不见——写错位置、超过 8KB 被截断、cwd 不是自己想的那一层，
// 三种情况表现一模一样（AI 没照约定做）。给一行提示，这些都能自查。
const runRules = ref<AIRuleFile[]>([])

// toolList 把消息里的 tool_calls 解析成数组。
//
// 用 try/catch 兜住：这是历史数据，可能来自旧版本、也可能被手工改过库；
// 解析失败就不显示卡片，而不是让整条消息渲染崩掉（一个坏 JSON 不该白屏）。
function toolList(m: AIMessage): AIToolCall[] {
  if (!m.tool_calls) return []
  try {
    const arr = JSON.parse(m.tool_calls)
    return Array.isArray(arr) ? (arr as AIToolCall[]) : []
  } catch {
    return []
  }
}

// rulesOf 取这条回复生效过的规则文件。只有本次会话里收到的回复有（见 runRules），
// 刷新后重新从库里读的消息没有这个字段——规则是执行时事实，不落库。
function rulesOf(m: Record<string, any>): AIRuleFile[] {
  const v = m?.rules
  return Array.isArray(v) ? (v as AIRuleFile[]) : []
}

// rulesTitle 悬停时列出文件名与字节数：截断过的一定要标出来，
// 否则「我写了 30KB 的规则，AI 只记住一半」这件事永远查不出来。
function rulesTitle(files: AIRuleFile[]): string {
  return files.map((f) => `${f.name} · ${f.bytes} B${f.truncated ? ' · 已截断' : ''}`).join('\n')
}

// runIDOf 取这条回复所属的编排 run（同一条回复里派了多个子任务时它们同属一个 run，
// 所以取第一条带 run_id 的调用即可）。
// 为空的回复（绝大多数）不渲染 run 树面板——不能对每句普通回答都挂一个空面板。
function runIDOf(m: AIMessage): string {
  if (m.role !== 'assistant') return ''
  for (const tc of toolList(m)) {
    if (tc.run_id) return tc.run_id
  }
  return ''
}

// serializeTools 把流式期收集的记录转成落库/展示同构的 JSON。
// 丢掉 running：库里存的是「发生过什么」，不是「界面此刻什么样」。
function serializeTools(list: AIToolCall[]): string {
  return JSON.stringify(list.map((t) => ({
    id: t.id, name: t.name, args: t.args || '',
    result: t.result || '', error: t.error || '',
    ms: t.ms || 0, round: t.round || 0,
    ...(t.dedup ? { dedup: true } : {}),
    // 拒绝是结果不是瞬时态：刷新后卡片还要显示「已拒绝」，所以必须落库。
    // approval 状态本身不落——pending 在这条消息进库时早已有结论。
    ...(t.denied ? { denied: true } : {}),
    ...(t.added ? { added: t.added } : {}),
    ...(t.removed ? { removed: t.removed } : {}),
    // diff 要落库：刷新后卡片还能展开看到「这一步改了什么」，
    // 而这正是用户回看历史时最需要的信息。
    ...(t.diff ? { diff: t.diff } : {}),
    // run/node 必须一起落库：刷新后 run 树面板靠它才知道该拉哪棵树。
    ...(t.run_id ? { run_id: t.run_id, node_id: t.node_id || '' } : {}),
  })))
}

// countToolRounds 数轮次而不是数条数：一轮可以并发调多个工具，
// 卡片上写「5 个工具」和「3 轮」是两件事，用户问的「调了几轮」是后者。
function countToolRounds(list: AIToolCall[]): number {
  return new Set(list.filter((t) => t.round).map((t) => t.round)).size
}

// ---- 工具卡片的展示 ----
//
// 直接把 toolName + 原始参数 JSON 铺在卡片上是最省事的写法，也是最难看的：
// 一行 `file_edit` 后面跟一坨 {"path":..., "old_string":...}，用户得在 JSON 里
// 找自己关心的那一个值。所以卡片读的是「动作 + 目标」两件事：
// 图标与动词说清做了什么，后面那一句说清对谁做的。
const TOOL_ICON: Record<string, Component> = {
  file_edit: PenLine, file_write: FilePlus, file_read: FileText, file_search: FileSearch, file_grep: FileSearch,
  dir_list: FolderTree, git_status: GitBranch, git_diff: GitBranch,
  web_fetch: Globe, web_links: LinkIcon, web_head: Globe,
  run_command: Terminal, check_workdir: FolderTree,
  // 派活：树状分叉的图示比扳手更能说明「这里分出去了几路子任务」。
  spawn_agent: Network,
  plugin_execute: Play, plugin_open: LayoutList, plugin_list: LayoutList,
  env_list: ListChecks, env_start: Play, env_stop: Square, env_status: Bug,
  port_list: LayoutList, log_list: FileText, log_read: FileText,
  monitor_list: Clock, monitor_check_now: Clock, monitor_logs: Clock,
  schedule_list: Clock, schedule_runs: Clock, schedule_run_now: Play,
  todo_list: ListChecks, todo_add: Plus, todo_done: ListChecks,
  note_search: FileText, note_create: FileText,
  clipboard_list: LayoutList, clipboard_search: Search,
  robot_notify: Bell, app_info: Bot, diag_run: Bug,
}
// 动词按工具名前缀兜底：新工具没登记时至少能看出它是读、改、跑还是查。
function toolIcon(name: string): Component {
  if (TOOL_ICON[name]) return TOOL_ICON[name]
  if (name.startsWith('file_')) return FileText
  if (name.startsWith('git_')) return GitBranch
  if (name.startsWith('web_')) return Globe
  if (name.startsWith('env_')) return ListChecks
  if (name.startsWith('monitor_') || name.startsWith('schedule_')) return Clock
  if (name.startsWith('plugin_')) return Play
  if (name.startsWith('todo_')) return ListChecks
  if (name.startsWith('note_')) return FileText
  if (name.startsWith('clipboard_')) return LayoutList
  if (name.startsWith('log_')) return FileText
  return Wrench
}

const TOOL_VERB: Record<string, string> = {
  file_edit: 'aiToolVerbEdit', file_write: 'aiToolVerbWrite', file_read: 'aiToolVerbRead', file_search: 'aiToolVerbSearch',
  file_grep: 'aiToolVerbSearch', dir_list: 'aiToolVerbList',
  git_status: 'aiToolVerbGitStatus', git_diff: 'aiToolVerbGitDiff',
  web_fetch: 'aiToolVerbFetch', web_links: 'aiToolVerbLinks', web_head: 'aiToolVerbHead',
  run_command: 'aiToolVerbRun', check_workdir: 'aiToolVerbProbe',
  spawn_agent: 'aiToolVerbSpawn',
  plugin_execute: 'aiToolVerbPlugin', plugin_open: 'aiToolVerbOpen', plugin_list: 'aiToolVerbList',
  env_list: 'aiToolVerbQuery', env_start: 'aiToolVerbStart', env_stop: 'aiToolVerbStop',
  env_status: 'aiToolVerbQuery', port_list: 'aiToolVerbQuery',
  log_list: 'aiToolVerbQuery', log_read: 'aiToolVerbRead',
  monitor_list: 'aiToolVerbQuery', monitor_check_now: 'aiToolVerbCheck', monitor_logs: 'aiToolVerbQuery',
  schedule_list: 'aiToolVerbQuery', schedule_runs: 'aiToolVerbQuery', schedule_run_now: 'aiToolVerbRun',
  todo_list: 'aiToolVerbQuery', todo_add: 'aiToolVerbAdd', todo_done: 'aiToolVerbUpdate',
  note_search: 'aiToolVerbSearch', note_create: 'aiToolVerbAdd',
  clipboard_list: 'aiToolVerbQuery', clipboard_search: 'aiToolVerbSearch',
  robot_notify: 'aiToolVerbNotify', app_info: 'aiToolVerbQuery', diag_run: 'aiToolVerbCheck',
}
function toolVerb(name: string): string {
  const k = TOOL_VERB[name]
  return k ? t(k) : t('aiToolVerbCall')
}

// 目标：从参数里挑一个「这条卡片在说谁」的值。
//
// 只挑 path / url / command / pattern / query / name / id 这几个键 ——
// 全参数铺开的话，一条 file_edit 的摘要会有三行，而用户真正想确认的只是
// 「它动的是不是我那个文件」。
const TOOL_TARGET_KEYS = ['path', 'url', 'command', 'pattern', 'query', 'keyword', 'name', 'id', 'plugin', 'runtime', 'dir']
function toolTarget(tc: AIToolCall): string {
  const raw = (tc.args || '').trim()
  if (!raw) return ''
  let obj: Record<string, any> | null = null
  try { obj = JSON.parse(raw) } catch { obj = null }
  if (!obj) {
    // 参数解析失败时至少给原文的前一段：模型写坏了 JSON 也要能看出它想动谁。
    return raw.length > 80 ? raw.slice(0, 80) + '…' : raw
  }
  for (const k of TOOL_TARGET_KEYS) {
    const v = obj[k]
    if (typeof v === 'string' && v.trim()) return shortPath(v.trim())
    if (typeof v === 'number') return String(v)
  }
  // 没命中已知键就退回第一个字符串值，比整段 JSON 可读得多。
  for (const [, v] of Object.entries(obj)) {
    if (typeof v === 'string' && v.trim()) return shortPath(v.trim())
  }
  return ''
}

// 路径只留末尾两段：绝对路径在卡片上是噪音，用户要确认的是「哪个文件」，
// 而 `E:\pp\quickdock\services\ai\tools\code.go` 里有用的只有后两截。
function shortPath(p: string): string {
  const sep = p.includes('\\') ? '\\' : '/'
  const parts = p.split(/[\\/]/).filter(Boolean)
  if (parts.length <= 2) return p
  return '…' + sep + parts.slice(-2).join(sep)
}

// 编辑回滚点（A3）：这条会话改过哪些文件、能退到哪一步。
const checkpoints = ref<AICheckpointView[]>([])

async function loadCheckpoints(convID: string) {
  if (!convID) { checkpoints.value = []; return }
  try {
    const list = unwrap<AICheckpointView[]>(await AIListCheckpoints(convID))
    // 只在还停在同一个会话时写入：请求在途时用户可能已经切走，
    // 把 A 会话的回滚点画到 B 会话的卡片上，是那种「怎么都对不上」的 bug。
    if (activeId.value === convID) checkpoints.value = list ?? []
  } catch {
    // 拉不到回滚点不该弹错：它是附加能力（比如老库还没迁移完），
    // 拿它打扰正常对话不值得，卡片上少一个按钮即可。
    checkpoints.value = []
  }
}

// cpForCall 这条工具调用有没有对应的回滚点。
//
// 按 tool_call_id 匹配而不是「取最近一个」：一轮里可能并发改两个文件，
// 按时间取会让按钮挂到错的那张卡片上——而那个按钮的后果是直接改磁盘。
function cpForCall(tc: AIToolCall): AICheckpointView | undefined {
  if (!tc.id) return undefined
  return checkpoints.value.find((c) => c.tool_call_id === tc.id)
}

// restoreCheckpoint 把文件退回到某一步之前。
//
// 必须二次确认，且把「哪些文件会变」写进确认文案：这是整个 AI 页里唯一
// 一个不经过模型、直接覆盖磁盘现有内容的按钮。
async function restoreCheckpoint(cp: AICheckpointView) {
  const names = cp.files.map((f) => shortPath(f.path)).join('、')
  // 回滚是「退回到这一步之前」，会连带撤销它之后的步骤。这个数字必须摆在确认框里：
  // 用户点的是「第 3 个点」，但如果后面还有 5 步，实际被撤销的是 6 步。
  const steps = checkpoints.value.filter((c) => c.seq >= cp.seq).length || 1
  if (!(await toast.confirm(t('aiCheckpointConfirm', { n: steps, files: names })))) return
  try {
    const res = unwrap<{ restored: number; note?: string }>(await AIRestoreCheckpoint(activeId.value, cp.seq))
    toast.success(res?.note || t('aiCheckpointDone', { n: res?.restored ?? 0 }))
    // 文件内容变了：卡片上的「+N/-M」与工具结果已经与磁盘不符，
    // 重拉消息让界面回到与事实一致的状态（否则用户会对着旧 diff 判断）。
    if (activeId.value) {
      await loadMessages(activeId.value)
      void loadCheckpoints(activeId.value)
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 结果要不要用等宽块：命令输出与网页正文带换行，按纯文本渲染会把缩进吃掉。
function toolResultLang(tc: AIToolCall): string {
  if (tc.name === 'run_command') return 'bash'
  if (tc.name.startsWith('git_')) return 'diff'
  if (tc.name === 'file_edit') return 'diff'
  return ''
}

// approveTool 对一条待确认的工具调用作出决定（见后端 ai_approval.go）。
//
// 点完立刻把本地状态改成终态、不等后端回执：审批期间宿主整条流是阻塞的，
// 中间没有任何进度可显示；按钮不变灰的话用户会连点，而第二次在后端只会被
// 丢掉（缓冲区已满），表现出来就是「点了没反应」。
// 出错要把状态退回去——否则这一次点完就再也没有机会同意了。
async function approveTool(tc: AIToolCall, decision: 'allow' | 'deny', remember = false) {
  if (tc.approval !== 'pending') return
  tc.approval = decision === 'allow' ? 'allowed' : 'denied'
  // 拒绝要留痕：随后的 tool_result 事件会把它写进卡片与库里，刷新后还能看到
  // 「已拒绝」而不是一条普通的红色报错。
  if (decision === 'deny') tc.denied = true
  try {
    await AIApproveToolCall(tc.approvalConvId || activeId.value, tc.id, decision, remember)
  } catch (e) {
    tc.approval = 'pending'
    tc.denied = false
    toast.error(getErrorMessage(e))
  }
}

// 历史消息的块 HTML：按消息 id 记忆化。
//
// 为什么不用「整段文本 → 块数组」的全局 LRU：
// 之前 mdCache 上限 200 段，缓存淘汰采用「删最旧一条」。当一个会话的消息数
// 超过上限后，模板每帧遍历历史消息时，最早的那批条目正好被挤在淘汰线上，
// 于是每帧都要把全部历史消息重新 md.parse 一遍。
// 实测：400 段 × 3 块的会话，稳态帧 md.parse 次数 = 1200（= 消息数 × 块数），
// 也就是「越长的对话越卡」，而且是持续卡，不是只在首帧卡。
//
// AI 消息落库后内容不再变化，按消息 id 记忆化即可：
// 无容量上限（随会话消息数自然增长）、不会抖动、也不会误伤其他会话。
const msgBlockCache = new Map<string, { src: string; html: string[] }>()

// 流式块的 HTML 缓存（与历史块分开存放）。
// 分开的原因：两者共用一个 LRU 时，长对话的历史块会把流式块挤出去，
// 导致流式期间「已完成的块」每帧都被重新解析 —— 恰好是最需要流畅的时候掉帧。
// 流式块的规模只与单次回复的段落数有关，不会无限增长，故不设上限。
const liveBlockCache = new Map<string, string>()

// 以「围栏代码块之外的空行」为界切块。
// 切分点必须是 markdown 语义上独立的边界，否则分块渲染与整段渲染结果不一致
// （表现为流式过程中内容突然变形）。具体规则与边界场景测试见 utils/markdownBlocks.ts。
function splitMarkdownBlocks(src: string): string[] {
  return splitBlocks(src)
}

// 解析单个块（带缓存）。命中缓存时返回同一字符串引用，
// Vue 的 patchElement 对 innerHTML 只在值变化时赋值 → 不会产生 DOM 写入。
//
// 注意：只有已完成的块才写入缓存。流式中最后一个未完成块每帧原文都变，
// 若也缓存会产生大量一次性条目。未完成块每帧重解析本身开销就很小
// （实测末块仅占全文 0.2%）。
function renderBlock(raw: string, cacheable = true): string {
  const cached = liveBlockCache.get(raw)
  if (cached !== undefined) return cached
  const html = DOMPurify.sanitize(md.parse(raw, { async: false }) as string)
  if (cacheable) liveBlockCache.set(raw, html)
  return html
}

// 历史消息的块 HTML 数组：按消息 id 记忆化。
// 消息内容不可变，故可安全缓存；src 用于兜底校验（内容若被改动则重新解析）。
function renderMessageBlocks(m: AIMessage): string[] {
  const key = m.id
  const hit = msgBlockCache.get(key)
  if (hit && hit.src === m.content) return hit.html
  // 渲染前剥掉「被写成正文的工具调用」标记：修复前落库的历史消息里带着这些
  // 标记，直接渲染就是满屏 <｜｜DSML｜｜>（见 utils/dsmlMarkup.ts）。
  const raws = splitMarkdownBlocks(stripDsmlMarkup(m.content ?? ''))
  const html = raws.map((raw) => DOMPurify.sanitize(md.parse(raw, { async: false }) as string))
  msgBlockCache.set(key, { src: m.content ?? '', html })
  return html
}

// 流式增量渲染：只重新解析最后一个未完成块，其余块直接复用缓存。
//
// 对齐方式：只有除最后一块外的块才按原文做公共前缀匹配。
// 原因是最后一块是「正在写」的块，它的原文每帧都在变，若也要求原文完全相等，
// 就永远匹配不上 → 每次都从第 0 块重建，导致已渲染内容被顶替（实测复现）。
// 最后一块无条件重渲染（它的体量小，开销低），前面的块原文一旦稳定就不会再变。
function renderLiveBlocks(src: string) {
  // 与历史消息同一个口径：先剥掉工具调用标记，再切块渲染。
  // 后端已经在流式层拦过一次（services/ai/ai_dsml.go），这里是幂等的兜底。
  const raws = splitMarkdownBlocks(stripDsmlMarkup(src))
  const arr = streamBlocks.value
  const n = raws.length
  // 已完成块的数量：raws 的前 n-1 块（最后一块是进行中的）
  const doneCount = n > 0 ? n - 1 : 0
  // 计算与上一帧的公共前缀（只比较已完成块）
  let common = 0
  const limit = Math.min(doneCount, arr.length)
  while (common < limit && arr[common].raw === raws[common]) {
    common++
  }
  // 公共前缀的 HTML 必然一致（同一原文 → 同一缓存结果），无需重写
  for (let i = common; i < doneCount; i++) {
    const html = renderBlock(raws[i])
    if (i < arr.length) {
      if (arr[i].html !== html) arr[i].html = html
      arr[i].raw = raws[i]
    } else {
      arr.push({ raw: raws[i], html })
    }
  }
  // 最后一块（进行中）：无条件重渲染，且不写入缓存（原文每帧都变）
  if (n > 0) {
    const html = renderBlock(raws[n - 1], false)
    if (doneCount < arr.length) {
      if (arr[doneCount].html !== html) arr[doneCount].html = html
      arr[doneCount].raw = raws[n - 1]
    } else {
      arr.push({ raw: raws[n - 1], html })
    }
  }
  if (arr.length > n) arr.length = n
}

// 流式结束、消息落地时，把已渲染好的块直接交给历史消息缓存。
// 否则落地瞬间要把整条回复重新 md.parse 一遍（实测 15KB ≈ 10.5ms），
// 恰好发生在用户刚看到完整回复的时刻 —— 表现为「回复刚出来就卡一下」。
function primeMessageBlocks(id: string, content: string, html: string[]) {
  msgBlockCache.set(id, { src: content, html })
}

// 只保留当前会话仍在显示的消息缓存，丢弃其他会话的条目。
function pruneMessageBlocks(list: AIMessage[]) {
  if (msgBlockCache.size === 0) return
  const alive = new Set(list.map((m) => m.id))
  for (const id of Array.from(msgBlockCache.keys())) {
    if (!alive.has(id)) msgBlockCache.delete(id)
  }
}

// —— Mermaid 图渲染调度 ——
// 图不是同步渲染出来的：marked 只吐源码占位块，真正画图要等 DOM 挂上后走异步 API。
// 这个调度点用 nextTick 合并同一批更新（流式每帧都会触发，语义上只需要「这帧之后扫一遍」）。
let mermaidScanPending = false

function skipStreamTail(el: HTMLElement): boolean {
  // 流式尾块的围栏还没闭合，此时渲染必然失败；等它变成普通块（下一帧或回答结束）再画
  const block = el.closest('.ai-md-block') as HTMLElement | null
  if (!block || !block.closest('.ai-msg.streaming')) return false
  return block === block.parentElement?.lastElementChild
}

function scheduleMermaidScan() {
  if (mermaidScanPending) return
  mermaidScanPending = true
  nextTick(() => {
    mermaidScanPending = false
    renderMermaidIn(msgContent.value, { skip: skipStreamTail })
    enhanceTables(msgContent.value)
  })
}

// —— 表格增强：横向滚动 / 表头点击排序 / 导出 CSV ——
// 这些作用在 v-html 渲染出的真实 DOM 上（Vue 不托管），故走原生 API。
// 已处理的表打 data-tbl-enhanced，避免流式每帧重扫时重复注入。
function enhanceTables(root: HTMLElement | null) {
  if (!root) return
  const tables = root.querySelectorAll('table:not([data-tbl-enhanced])')
  for (const tbl of Array.from(tables) as HTMLTableElement[]) {
    tbl.setAttribute('data-tbl-enhanced', '1')
    const parent = tbl.parentElement
    // 横向滚动容器：表格可能比气泡宽，不包一层表格自己不会滚
    let wrap = parent?.classList.contains('md-table-wrap')
      ? (parent as HTMLElement)
      : null
    if (!wrap && parent) {
      wrap = document.createElement('div')
      wrap.className = 'md-table-wrap'
      parent.insertBefore(wrap, tbl)
      wrap.appendChild(tbl)
    }
    // 表头排序：给每个 thead th 打列索引 + 可点标记（marked 表格第一行即 thead>th）
    tbl.querySelectorAll('thead th').forEach((th, i) => {
      th.setAttribute('data-col', String(i))
      th.classList.add('md-th-sortable')
    })
    // 导出 CSV 按钮条（插在滚动容器上方）
    if (wrap && !wrap.previousElementSibling?.classList.contains('md-table-bar')) {
      const bar = document.createElement('div')
      bar.className = 'md-table-bar'
      const btn = document.createElement('button')
      btn.type = 'button'
      btn.className = 'md-table-export'
      btn.textContent = t('aiExportCsv')
      btn.addEventListener('click', () => exportTableCsv(tbl))
      bar.appendChild(btn)
      wrap.parentElement?.insertBefore(bar, wrap)
    }
  }
}

function csvCell(s: string): string {
  const v = (s || '').replace(/\r?\n/g, ' ').trim()
  return /[",\r\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v
}

function exportTableCsv(tbl: HTMLTableElement) {
  const lines = Array.from(tbl.querySelectorAll('tr')).map((r) =>
    Array.from(r.children)
      .map((c) => csvCell(c.textContent ?? ''))
      .join(','),
  )
  const csv = '﻿' + lines.join('\r\n') // 前导 BOM：避免 Excel 把中文 CSV 读成乱码
  downloadBlob(new Blob([csv], { type: 'text/csv;charset=utf-8' }), 'table.csv')
  toast.success(t('aiDownloadOk', { name: 'table.csv' }))
}

function sortTableByColumn(th: HTMLElement) {
  const tbl = th.closest('table') as HTMLTableElement | null
  if (!tbl) return
  const col = Number(th.dataset.col ?? '0')
  const tbody = (tbl.querySelector('tbody') as HTMLTableSectionElement | null) || tbl
  const rows = Array.from(tbody.querySelectorAll('tr'))
  if (!rows.length) return
  const prevCol = tbl.dataset.sortCol
  const prevDir = tbl.dataset.sortDir
  const dir = prevCol === String(col) && prevDir === 'asc' ? 'desc' : 'asc'
  const numeric = rows.every((r) => {
    const v = (r.children[col]?.textContent ?? '').trim()
    return v !== '' && !Number.isNaN(Number(v))
  })
  rows.sort((a, b) => {
    const av = (a.children[col]?.textContent ?? '').trim()
    const bv = (b.children[col]?.textContent ?? '').trim()
    const cmp = numeric ? Number(av) - Number(bv) : av.localeCompare(bv, undefined, { numeric: true })
    return dir === 'asc' ? cmp : -cmp
  })
  rows.forEach((r) => tbody.appendChild(r))
  tbl.dataset.sortCol = String(col)
  tbl.dataset.sortDir = dir
  // 指示符：只有当前列显示 ▲/▼，其余清掉
  tbl.querySelectorAll('.md-th-sortable').forEach((el) => {
    if (el !== th) el.removeAttribute('data-sort-dir')
  })
  th.setAttribute('data-sort-dir', dir)
}

// 提交缓冲区到响应式状态。done/停止时必须立即 flush，否则尾部文字会丢。
function flushStream() {
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  if (streamBuf) {
    streamingText.value += streamBuf
    streamBuf = ''
  }
  if (streamReasonBuf) {
    reasoningText.value += streamReasonBuf
    streamReasonBuf = ''
  }
  renderLiveBlocks(streamingText.value)
  scheduleMermaidScan()
  followBottom()
}

function scheduleStreamFlush() {
  if (streamTimer) return
  streamTimer = setTimeout(() => {
    streamTimer = null
    flushStream()
  }, STREAM_FLUSH_MS)
}

const reasoningText = ref('')  // 思考过程（reasoning_content），折叠显示

// 本地 AI 流式服务信息（端口 + 随机令牌，通过 Header 传递）
const aiStreamPort = ref(0)
const aiStreamToken = ref('')
let streamCtrl: AbortController | null = null

// —— 滚动跟随 ——
// 根因：原先用 watch(streamingText) 驱动滚动，但真正改变 DOM 高度的是
// markdown 渲染结果，两者时间点错位，scrollHeight 读到的是旧值。
// 现在改为：ResizeObserver 观察消息容器内容高度变化 → 变化即滚到底。
// 同时记录用户是否主动上滑（stickToBottom），上滑后不再强制抢滚动，改为显示「回到底部」按钮。
const msgArea = ref<HTMLElement | null>(null)
const msgContent = ref<HTMLElement | null>(null)
const stickToBottom = ref(true)
const showJumpBtn = ref(false)
let resizeObs: ResizeObserver | null = null
const BOTTOM_THRESHOLD = 48  // 距底部小于该像素即视为「贴底」

function onMsgScroll() {
  const el = msgArea.value
  if (!el) return
  const dist = el.scrollHeight - el.scrollTop - el.clientHeight
  stickToBottom.value = dist <= BOTTOM_THRESHOLD
  showJumpBtn.value = !stickToBottom.value
}

function scrollToBottom(smooth = false) {
  const el = msgArea.value
  if (!el) return
  nextTick(() => {
    const e = msgArea.value
    if (!e) return
    if (smooth) {
      e.scrollTo({ top: e.scrollHeight, behavior: 'smooth' })
    } else {
      e.scrollTop = e.scrollHeight
    }
  })
}

// 流式过程中跟随底部：内容追加后把视口拉到底。
// 用户主动上滑查看历史时（stickToBottom=false）不抢滚动，只显示「回到底部」按钮。
//
// 必须走 nextTick：调用时 Vue 还没把新的 markdown 渲染进 DOM，
// 此时读 scrollHeight 拿到的是上一帧的旧高度，滚不到位。
function followBottom() {
  if (!stickToBottom.value) return
  nextTick(() => {
    // 下一帧用户可能已主动上滑，这时不该再抢滚动
    if (!stickToBottom.value) return
    const el = msgArea.value
    if (!el) return
    // 直接写 scrollTop 会触发 onMsgScroll，把贴底状态重新判定为 true —— 正是期望行为
    el.scrollTop = el.scrollHeight
  })
}

function jumpToBottom() {
  stickToBottom.value = true
  showJumpBtn.value = false
  scrollToBottom(true)
}

// 多档案 / 模型切换
const aiProfiles = ref<AIProfile[]>([])
const aiActive = ref('')
const hasKey = computed(() => {
  const p = aiProfiles.value.find((x) => x.id === aiActive.value)
  return !!(p && p.apiKey)
})
// 任务的五种类型。icon/desc 跟着类型走，落地页那一排 pill 与侧栏的任务图标都读这一份——
// 两处各写一遍的话，加一个类型就要在两个地方各补一次，漏一处就是「类型选不了」。
const modes = [
  { key: 'chat', label: 'aiModeChat', desc: 'aiModeChatDesc', icon: MessageSquare },
  { key: 'explain', label: 'aiModeExplain', desc: 'aiModeExplainDesc', icon: Lightbulb },
  { key: 'translate', label: 'aiModeTranslate', desc: 'aiModeTranslateDesc', icon: Languages },
  { key: 'summarize', label: 'aiModeSummarize', desc: 'aiModeSummarizeDesc', icon: FileText },
  { key: 'tutorial', label: 'aiModeTutorial', desc: 'aiModeTutorialDesc', icon: GraduationCap },
]

// 侧栏任务行前面的小图标：不标出来，「这条任务是翻译还是闲聊」只能点进去看内容。
function modeIcon(m?: string) {
  const hit = modes.find((x) => x.key === (m || 'chat'))
  return hit ? hit.icon : MessageSquare
}

// —— 图片附件 ——
// attachments 为待发送预览；发送后清空。file 类存 dataUrl（base64），直接随请求发给模型；
// url 类存公网链接。imageInputMode 来自当前激活档案的声明（both/url/base64），
// 决定允许哪种附件——Agnes 这类只收 URL 的模型要禁用本地图选择（见 ai_image.go 闸门）。
const attachments = ref<PendingImage[]>([])

// 当前激活档案的 imageInput 能力；空（旧档案）按 both 处理，与后端 normalizeImageInput 一致。
const imageInputMode = computed<string>(() => {
  const p = aiProfiles.value.find((x) => x.id === aiActive.value)
  const v = (p?.imageInput || 'both').toLowerCase()
  if (v === 'url' || v === 'base64') return v
  return 'both'
})
// 是否允许添加本地图片 / 公网链接（两者在 both 下都允许）。
const allowFileImage = computed(() => imageInputMode.value === 'both' || imageInputMode.value === 'base64')
const allowUrlImage = computed(() => imageInputMode.value === 'both' || imageInputMode.value === 'url')

// 本地图片大小上限（与后端 10 MiB 对齐，前端先拦一道，避免把巨大 base64 塞进请求体）。
const maxImageBytes = 10 * 1024 * 1024

// 把 File 转 data URL（前端预览 + 随请求发送）。仅读取为 base64，格式校验交给后端
// （magic bytes 校验，比前端扩展名更可靠），前端不做硬性拒绝以免误伤。
function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(r.result as string)
    r.onerror = () => reject(new Error('读取失败'))
    r.readAsDataURL(file)
  })
}

async function addFiles(files: FileList | File[]) {
  if (!allowFileImage.value) { toast.error(t('aiImgOnlyUrl')); return }
  for (const file of Array.from(files)) {
    if (!file.type.startsWith('image/')) { toast.error(t('aiImgOnlyLocal')); continue }
    if (file.size > maxImageBytes) { toast.error(t('aiImageTooLarge')); continue }
    const dataUrl = await fileToDataURL(file)
    attachments.value.push({ kind: 'file', dataUrl, name: file.name || 'image' })
  }
  if (attachments.value.length) toast.success(t('aiImageAdded', { n: attachments.value.length }))
}

function addUrlImage(url: string) {
  const u = url.trim()
  if (!u) return
  if (!allowUrlImage.value) { toast.error(t('aiImgOnlyLocal')); return }
  if (!/^https?:\/\//i.test(u)) { toast.error(t('aiImageUrlInvalid')); return }
  attachments.value.push({ kind: 'url', url: u, name: u })
}

function removeAttachment(i: number) {
  attachments.value.splice(i, 1)
}

// —— @ 引用（见后端 ai_mentions.go）——
//
// 输入框里打 @ 弹出补全，选中后变成一个 chip；发送时只把「引用了谁」带给后端，
// 由后端读内容并展开进上下文。
//
// 为什么不在前端读文件内容：读路径必须过 fsguard 白名单，而白名单在 Go 侧；
// 前端读等于绕过它。也不把内容塞进 message 文本——那样用户自己的气泡里会
// 出现几千行文件内容，而且会跟着落库。
const mentions = ref<AIMentionItem[]>([])
const mentionItems = ref<AIMentionItem[]>([])
const mentionIdx = ref(0)
let mentionTimer: number | null = null
// 上一次发送用过的引用：重试（reuseUserBubble）时输入框已清空，靠它把引用带回去。
let lastMentions: { kind: string; path: string; convId: string; name: string }[] = []

// 只识别输入框末尾的 @token。
//
// 光标在中间时「@」可能是已发出的正文（比如邮箱、@2x 图片名），按末尾判定
// 能避开绝大多数误触发；代价是中间插入 @ 不弹窗，用户把光标挪到末尾即可。
const mentionQuery = computed<string | null>(() => {
  if (streaming.value || !hasKey.value) return null
  const m = /(^|\s)@([^\s@]*)$/.exec(input.value)
  return m ? m[2] : null
})

const mentionOpen = computed(() => mentionQuery.value !== null && mentionItems.value.length > 0)

async function loadMentionItems(q: string) {
  const out: AIMentionItem[] = []
  // 会话从本地列表里筛：这些数据页面已经有了，为它再走一次 RPC 只是多一次往返。
  const ql = q.toLowerCase()
  for (const c of conversations.value) {
    const title = (c.title || '').trim()
    if (!title || (ql && !title.toLowerCase().includes(ql))) continue
    out.push({ kind: 'conv', name: title, convId: c.id })
    if (out.length >= 5) break
  }
  try {
    const list = unwrap<AIMentionItem[]>(await AISuggestMentions(convCwd.value, q))
    for (const it of list || []) out.push({ kind: it.kind, name: it.name, path: it.path })
  } catch {
    // 没有工作目录 / 目录已失效：只给会话项。这不是错误，不该弹提示打断输入。
  }
  mentionItems.value = out
  mentionIdx.value = 0
}

watch(mentionQuery, (q) => {
  if (mentionTimer) { clearTimeout(mentionTimer); mentionTimer = null }
  if (q === null) {
    mentionItems.value = []
    mentionIdx.value = 0
    return
  }
  // 120ms 防抖：扫目录是同步 IO，每敲一个字都扫一遍会在机械硬盘上明显卡顿。
  mentionTimer = window.setTimeout(() => { void loadMentionItems(q) }, 120)
})

function sameMention(a: AIMentionItem, b: AIMentionItem): boolean {
  return a.kind === b.kind && (a.path || '') === (b.path || '') && (a.convId || '') === (b.convId || '')
}

function pickMention(it: AIMentionItem) {
  // 把输入框末尾那个 @token 删掉：chip 才是引用的载体，正文里留着 @xxx
  // 会在发给模型的文本里多出一段既不像路径也不像名字的东西。
  input.value = input.value.replace(/@[^\s@]*$/, '')
  if (!mentions.value.some((m) => sameMention(m, it))) mentions.value.push(it)
  mentionItems.value = []
  mentionIdx.value = 0
}

function removeMention(i: number) {
  mentions.value.splice(i, 1)
}

// onMentionKey 处理补全弹层里的方向键与回车。
// 返回 true 表示这次按键已被弹层消费。
function onMentionKey(e: KeyboardEvent): boolean {
  if (!mentionOpen.value) return false
  const n = mentionItems.value.length
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    mentionIdx.value = (mentionIdx.value + 1) % n
    return true
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    mentionIdx.value = (mentionIdx.value - 1 + n) % n
    return true
  }
  if (e.key === 'Enter' || e.key === 'Tab') {
    e.preventDefault()
    pickMention(mentionItems.value[mentionIdx.value])
    return true
  }
  if (e.key === 'Escape') {
    // 只收起弹层、保留已打的 @xxx：Esc 是「我不想选」，不是「我要清空刚打的字」。
    e.preventDefault()
    mentionItems.value = []
    return true
  }
  return false
}


// 剪贴板粘贴图片：在输入框按 Ctrl+V 时，若剪贴板含图片文件则作为本地图附件。
function onInputPaste(e: ClipboardEvent) {
  const items = e.clipboardData?.items
  if (!items) return
  const imgs: File[] = []
  for (const it of Array.from(items)) {
    if (it.kind === 'file' && it.type.startsWith('image/')) {
      const f = it.getAsFile()
      if (f) imgs.push(f)
    }
  }
  if (imgs.length) {
    e.preventDefault()
    addFiles(imgs)
  }
}

// 拖拽图片到输入区：drop 时取图片文件作为本地图附件。
function onInputDrop(e: DragEvent) {
  const files = e.dataTransfer?.files
  if (files && files.length) {
    e.preventDefault()
    const imgs = Array.from(files).filter((f) => f.type.startsWith('image/'))
    if (imgs.length) addFiles(imgs)
  }
}

// 历史消息图片懒加载：把后端存盘（file 类）的路径读回 dataUrl，回填到消息的 imgList。
// url 类直发链接，无需加载。返回供模板渲染用的图片数组（file 类未加载完毕时 dataUrl 为空）。
async function resolveMessageImages(msgs: AIMessage[]) {
  // 先解析引用结构，再把所有待加载的 file 类图一次性并发拉回。
  // 一张一张 await 时，N 张图的总耗时是 N 次往返之和——打开一个贴过图的
  // 长会话会明显停在「加载中」。并发后只等最慢的那一张。
  const pending: { img: AIMsgImage; path: string }[] = []
  for (const m of msgs) {
    if (!m.images) { m.imgList = []; continue }
    let arr: AIMsgImage[] = []
    try {
      const raw = JSON.parse(m.images)
      if (Array.isArray(raw)) {
        arr = raw.map((x: any) => ({ kind: x.kind, url: x.url, path: x.path, name: x.name }))
      }
    } catch { arr = [] }
    for (const img of arr) {
      if (img.kind === 'file' && img.path) pending.push({ img, path: img.path })
    }
    m.imgList = arr
  }
  // 同一张图可能在多条消息里被引用（同一张贴图问了两次），去重少读一次盘。
  const byPath = new Map<string, AIMsgImage[]>()
  for (const p of pending) {
    const list = byPath.get(p.path)
    if (list) list.push(p.img)
    else byPath.set(p.path, [p.img])
  }
  await Promise.all(Array.from(byPath.entries()).map(async ([path, imgs]) => {
    try {
      const d = unwrap<string>(await AILoadImage(path))
      if (d) for (const img of imgs) img.dataUrl = d
    } catch { /* 读不到图不阻断对话，缩略图留空 */ }
  }))
}

// 图片链接输入框（点击「图片链接」后展开的小输入框），避免每次都弹 prompt。
const urlInputOpen = ref(false)
const urlDraft = ref('')
const fileInputEl = ref<HTMLInputElement | null>(null)

function onPickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  if (input.files && input.files.length) addFiles(input.files)
  input.value = '' // 允许重复选择同一文件
}

function openFilePicker() {
  fileInputEl.value?.click()
}

function toggleUrlInput() {
  urlInputOpen.value = !urlInputOpen.value
  if (urlInputOpen.value) urlDraft.value = ''
}

function confirmUrlImage() {
  addUrlImage(urlDraft.value)
  urlDraft.value = ''
  urlInputOpen.value = false
}

// —— Markdown 渲染 ——
// 用独立 Marked 实例（而非全局 marked），避免污染 NoteManagerPage 的全局配置，
// 同时便于扩展 code renderer：给代码块加语言标签 + 复制按钮。
const md = new Marked({ breaks: true, gfm: true })

// 流程图工具栏的文案。utils 层是纯函数、没有 i18n 上下文，所以由这里翻好再传下去；
// 做成函数而不是常量，切语言后新渲染的图能立刻用上新文案。
function mermaidLabels(): MermaidLabels {
  return {
    error: t('aiMermaidError'),
    source: t('aiMermaidSource'),
    diagram: t('aiMermaidDiagram'),
    fold: t('aiMermaidFold'),
    expand: t('aiMermaidExpand'),
  }
}

// 数学公式：AI 回答里的公式一律是 LaTeX 源码，没有这一步就会被当成正文原样吐出来。
// 规则与边界见 utils/mathMarkdown.ts（含流式半截公式的降级处理）。
md.use(mathExtension())

// DOMPurify 默认白名单已包含 button / svg / pre / code，且默认放行 data-* 属性
// （ALLOW_DATA_ATTR=true）与 aria-*（ALLOW_ARIA_ATTR=true），
// 因此复制按钮可以安全地写在 v-html 里，用事件委托处理点击。
// 注意：viewBox 在白名单里是全小写 'viewbox'，DOMPurify 会把属性名统一转小写后比对，
// 故 SVG 必须用小写 viewbox 才能通过。
md.use({
  renderer: {
    code(token: { text: string; lang?: string }) {
      const lang = (token.lang || '').split(/\s+/)[0] || ''
      // ```latex / ```math 这类围栏装的是公式而不是代码：渲染成公式，
      // 也不再给「复制代码」按钮——复制一段 LaTeX 源码回聊天框没有意义。
      if (isMathFenceLang(lang)) return renderMath(token.text, true)
      // ```mermaid 装的是图定义：先吐源码占位块，等 DOM 挂上后再异步渲染成 SVG。
      // 为什么不能在这里直接渲染：Mermaid 只有异步 API，而这条链路是同步的（详见 utils/mermaidMarkdown.ts）。
      if (isMermaidFenceLang(lang)) return mermaidPlaceholder(token.text, mermaidLabels())
      const codeText = token.text.replace(/\n$/, '')
      const label = lang || 'text'
      // 语法高亮：已知语言走精确高亮，未知语言交给 highlightAuto（按内容猜）。
      // hljs 返回的 value 已是「转义后 + <span class="hljs-*" >」的 HTML，无需再 escapeHtml。
      let body: string
      try {
        const langLower = lang.toLowerCase()
        body = lang && hljs.getLanguage(langLower)
          ? hljs.highlight(codeText, { language: langLower }).value
          : hljs.highlightAuto(codeText).value
      } catch {
        body = escapeHtml(codeText)
      }
      // html 代码块给一个「预览」按钮：模型给的示例页只有真的跑起来才知道对不对，
      // 存成文件再双击打开太绕。实际执行放在沙箱 iframe 里，见 openHtmlPreview。
      const runnable = /^(html|htm)$/i.test(lang)
      const runBtn = runnable
        ? `<button type="button" class="md-code-run" data-md-run="1" title="${escapeHtml(t('aiPreviewHtml'))}" aria-label="${escapeHtml(t('aiPreviewHtml'))}">` +
            `<svg viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="6 3 20 12 6 21 6 3"></polygon></svg>` +
          `</button>`
        : ''
      return `<div class="md-code" data-md-code>` +
        `<div class="md-code-bar">` +
          `<span class="md-code-lang">${escapeHtml(label)}</span>` +
          `<span class="md-code-actions">` +
            runBtn +
            `<button type="button" class="md-code-copy" data-md-copy="1" title="${escapeHtml(t('copyCode'))}" aria-label="${escapeHtml(t('copyCode'))}">` +
              `<svg viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>` +
            `</button>` +
          `</span>` +
        `</div>` +
        `<pre><code${lang ? ` class="language-${escapeHtml(lang)}"` : ''}>${body}</code></pre>` +
      `</div>`
    },
  },
})

function escapeHtml(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

async function loadProfiles() {
  try {
    const res = unwrap<AIProfilesResult>(await AIListProfiles())
    if (!res) return
    aiProfiles.value = res.profiles ?? []
    aiActive.value = res.active || (aiProfiles.value[0]?.id ?? '')
    // 以服务端返回的「当前生效档案」为准，作为切模型失败时的还原基准
    lastActiveProfile = aiActive.value
  } catch (e) {
    aiProfiles.value = []
    errorMsg.value = getErrorMessage(e)
  }
}

// ---- 会话搜索 / 分批渲染 ----
// 列表一次全量拉、全量渲染：上百个会话时侧栏本身就成了负担，而真正要找的
// 往往是最近几条。搜 + 分批显示，两个毛病一起解决。
const convQuery = ref('')
// 折叠阈值：超过就只显示前 N 条，其余由「展开 / 收起」控制。
//
// 取「一屏放得下」的量级——侧栏是导航不是列表页，几十条任务全铺开会把当前任务挤出
// 可视区。原来是「显示更多」只增不减，展开一次就收不回去了，条目一多照样难用。
const FOLD_TASKS = 12
const FOLD_SPACES = 8
const FOLD_SPACE_TASKS = 10

// 整组是否展开（「任务 (n)」「空间 (n)」标题可点）。组里的东西一多，
// 先收起一整组往往比在里面翻找更快。
const tasksOpen = ref(true)
const spacesOpen = ref(true)
// 组内是否越过阈值全展开
const tasksExpanded = ref(false)
const spacesExpanded = ref(false)
// 空间内任务全展开的空间 id：与「组是否展开」分开记——一个空间也可能挂几十条任务，
// 展开空间不等于要把它下面全部任务都铺出来。
const expandedSpaceTasks = ref<string[]>([])

// 搜索同时覆盖任务与空间：空间是任务的容器，只搜任务会让「我记得放在某个空间里了」
// 这种最常见的查找方式失效。搜到空间时把它的任务一并算作命中。
function matchConv(c: AIConversation, q: string): boolean {
  return (c.title || '').toLowerCase().includes(q)
}

// 任务组：只列 不属于任何空间 的任务。空间下的任务归在空间那一组里，
// 否则同一个任务在侧栏出现两次，用户分不清点哪个才是同一个。
const taskConversations = computed(() => {
  const q = convQuery.value.trim().toLowerCase()
  return conversations.value.filter((c) => !(c.space_id || '').trim() && (!q || matchConv(c, q)))
})
const shownTaskConversations = computed(() =>
  tasksExpanded.value ? taskConversations.value : taskConversations.value.slice(0, FOLD_TASKS))

// 空间组：命中的空间（自身名字匹配，或它下面有任务匹配）+ 每个空间下的任务。
//
// 空间自己不匹配但下属任务匹配时也要显示这个空间：只把任务挑出来平铺，
// 用户就看不到「这条任务是哪个项目的」——而那正是空间要回答的问题。
const shownSpaces = computed(() => {
  const q = convQuery.value.trim().toLowerCase()
  const out: { space: AISpace; tasks: AIConversation[] }[] = []
  for (const s of spaces.value) {
    const tasks = conversations.value.filter((c) => (c.space_id || '') === s.id)
    const selfHit = !q || (s.name || '').toLowerCase().includes(q)
    const hits = q ? tasks.filter((c) => matchConv(c, q)) : tasks
    if (!q) {
      out.push({ space: s, tasks })
      continue
    }
    if (selfHit) out.push({ space: s, tasks })
    else if (hits.length) out.push({ space: s, tasks: hits })
  }
  return out
})

// 实际渲染的空间：在搜索命中的全集之上再叠一层折叠（命中本身也可能很多）
const visibleSpaces = computed(() =>
  spacesExpanded.value ? shownSpaces.value : shownSpaces.value.slice(0, FOLD_SPACES))

// 空间内的任务同样按阈值折叠
function spaceTasksShown(g: { space: AISpace; tasks: AIConversation[] }): AIConversation[] {
  return expandedSpaceTasks.value.includes(g.space.id) ? g.tasks : g.tasks.slice(0, FOLD_SPACE_TASKS)
}

function toggleSpaceTasks(id: string) {
  expandedSpaceTasks.value = expandedSpaceTasks.value.includes(id)
    ? expandedSpaceTasks.value.filter((x) => x !== id)
    : [...expandedSpaceTasks.value, id]
}

// 总数给「任务 (n) / 空间 (n)」用：搜索时显示命中数而不是总数，
// 否则「搜到 2 条」的标题上却写着「任务 (57)」。
const totalMatched = computed(() => taskConversations.value.length + shownSpaces.value.reduce((n, s) => n + s.tasks.length, 0))

// 换搜索词回到折叠态：搜索结果通常比全量少，沿用上一次的展开状态会显得莫名其妙。
watch(convQuery, () => {
  tasksExpanded.value = false
  spacesExpanded.value = false
})

// ---- 正文搜索 ----
// 标题匹配是内存里的一次 includes（会话列表本来就在手），正文匹配必须查库：
// 几十万条消息全读进前端不可行，所以这一段走 bound 方法。
const msgHits = ref<AIConvHit[]>([])
const msgHitsOpen = ref(true)
const msgHitsLoading = ref(false)
let msgSearchTimer: ReturnType<typeof setTimeout> | undefined
// 后发的请求可能先回（库在忙时尤其明显），用代数把过期结果丢掉——
// 否则列表会停在上一两个关键词的命中上，看起来像「搜错了」。
let msgSearchGen = 0

async function runMsgSearch(kw: string) {
  const gen = ++msgSearchGen
  msgHitsLoading.value = true
  try {
    const list = unwrap<AIConvHit[]>(await AISearchMessages(kw, 20)) ?? []
    if (gen === msgSearchGen) msgHits.value = list
  } catch (e) {
    if (gen === msgSearchGen) {
      msgHits.value = []
      errorMsg.value = getErrorMessage(e)
    }
  } finally {
    if (gen === msgSearchGen) msgHitsLoading.value = false
  }
}

// 300ms 去抖：逐字符发请求会在长会话库上把读锁打满，而打一个词的间隔通常就在这个量级。
watch(convQuery, (v) => {
  const kw = v.trim()
  if (msgSearchTimer) clearTimeout(msgSearchTimer)
  msgHitsOpen.value = true
  // 一个字不查库（后端同样判），但必须清掉上一轮的命中：
  // 删到只剩一个字时，列表还挂着上一个关键词的结果会被当成「它在搜这个字」。
  if (kw.length < 2) {
    msgSearchGen++
    msgHits.value = []
    msgHitsLoading.value = false
    return
  }
  msgSearchTimer = setTimeout(() => { void runMsgSearch(kw) }, 300)
})

async function loadSpaces() {
  try {
    spaces.value = (unwrap<AISpace[]>(await AIListSpaces()) ?? []) as AISpace[]
  } catch (e) {
    // 空间表是新增的：老库升级失败时不该把整个侧栏搞坏，静默按「没有空间」处理。
    spaces.value = []
  }
}

async function loadConversations() {
  try {
    const list = unwrap<AIConversation[]>(await AIListConversations())
    conversations.value = list ?? []
  } catch (e) {
    errorMsg.value = getErrorMessage(e)
  }
}

let messagesGen = 0
async function loadMessages(convID: string) {
  const gen = ++messagesGen
  try {
    const msgs = unwrap<AIMessage[]>(await AIGetMessages(convID))
    if (gen !== messagesGen) return // 已有更新的会话选择，丢弃过期响应
    // 解析历史消息图片：file 类从盘上读回 dataUrl（异步），url 类直发链接。
    // 在赋值 messages.value 之前完成，避免先渲染无图再补图导致的重渲染与闪烁。
    const list = msgs ?? []
    await resolveMessageImages(list)
    if (gen !== messagesGen) return // 等待图片加载期间用户已切换会话，丢弃过期响应
    messages.value = list
  } catch (e) {
    if (gen !== messagesGen) return
    toast.error(getErrorMessage(e))
    return
  }
  // 只保留当前会话的消息缓存：按 id 记忆化没有容量上限，
  // 若不清理，反复切换长会话会让缓存无限增长（这是内存问题，不是性能问题）。
  pruneMessageBlocks(messages.value)
  // 切换会话后强制贴底（用户主动切会话，期望看最新一条）
  stickToBottom.value = true
  showJumpBtn.value = false
  scrollToBottom()
  // 历史消息里可能有 ```mermaid（图不是同步渲染的，得在 DOM 挂上后补扫一遍）
  scheduleMermaidScan()
}

async function selectConv(id: string) {
  // 如果有正在流的对话，先打断
  if (streaming.value) stop()
  activeId.value = id
  const conv = conversations.value.find((c) => c.id === id)
  // 工作目录跟着任务走：两条对话可以绑不同项目，切回来时要还原成它自己的那个。
  convCwd.value = (conv?.cwd || '').trim()
  // 模式是这个任务创建时选定的属性，切回来要还原——否则「这条是用来翻译的」
  // 在切走再回来之后就丢了。
  mode.value = (conv?.mode || 'chat').trim() || 'chat'
  // 已有会话的归属以库里的为准，新建待定值在这里作废。
  pendingSpaceId.value = ''
  resetStreamState()
  errorMsg.value = ''
  await loadMessages(id)
  // 回滚点跟着会话走：切回来要能看到这条对话改过哪些文件。
  // 与消息分开拉而不是塞进 AIGetMessages：回滚点是「另一张表的东西」，
  // 混进消息响里会让那个接口为一张很少用到的表付出每次查询的代价。
  void loadCheckpoints(id)
}

// 打开当前任务的工作目录：AI 生成的文件都在里面。
// 只开不换——换目录的入口在新建任务时选空间。
async function openCwd() {
  if (!convCwd.value) return
  try {
    unwrap(await RevealInExplorer(convCwd.value))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// ---- 新建任务（落地页，不是弹窗）----
//
// 原来点「新建任务」弹一个框让你先定类型再定目录，定完才出现输入框，等于把
// 「提一个问题」拆成三步。现在改成主区直接变成落地页：上面选类型（一排 pill）、
// 输入框、下面一行选归属空间/模型——边选边打字，回车就走。
//
// 类型和空间仍然是这条任务的属性（首次发送时随请求带过去并落库），
// 只是把它们从「创建前的必答项」降级成「发送前顺手改的两个开关」：
// 不改就是默认（对话 + 普通任务），想改就在输入框下面改。
const newTaskMode = ref('chat')
// 归属空间：'' = 普通任务；已有空间的 id = 直接用它；NEW_SPACE = 新建（这时才要选目录）。
//
// 下拉优先而不是「一律选目录」：空间按目录唯一，重复选同一个目录后端也只是返回
// 同一个空间，让用户每次都开一次文件对话框去撞这个幂等，等于白跑一趟。
const NEW_SPACE = '__new__'
const newTaskSpace = ref('')
const newTaskDir = ref('')
const heroInputEl = ref<HTMLTextAreaElement | null>(null)

// 落地页上的示例问题：点一下填进输入框，省得对着空白框想「能问什么」。
const heroTips = ['aiHeroTip1', 'aiHeroTip2', 'aiHeroTip3', 'aiHeroTip4']

// 空态 = 还没进入任何任务。此时主区渲染落地页而不是「空消息区 + 底部输入框」。
// streaming 也要算进去：新任务发出去到 conv 事件回来之间 activeId 还空着，
// 不排除的话落地页会在第一轮请求途中闪回来。
const isNewTask = computed(() =>
  !activeId.value && messages.value.length === 0 && !streaming.value && !streamingText.value)

function openNewTask() {
  if (streaming.value) stop()
  // 下拉要列当前还存在的空间：从回收站恢复/删掉空间之后，挂载时那份列表就过期了。
  void loadSpaces()
  newTaskMode.value = 'chat'
  newTaskSpace.value = ''
  newTaskDir.value = ''
  beginBlankTask()
  nextTick(() => heroInputEl.value?.focus())
}

// 点示例问题：填进输入框并聚焦，用户接着改两个字就能发。
// 不直接发送——示例只是起点，直接发出去常常不是用户真想问的那句话。
function fillHeroTip(key: string) {
  input.value = t(key)
  nextTick(() => heroInputEl.value?.focus())
}

// 回车发送的唯一入口。
//
// 必须挡输入法组合：中文/日文输入中按回车是「确认候选词」，而 Chromium（WebView2 同源）
// 在这个回车照样派发 keydown（isComposing=true / keyCode 229）。之前用
// `@keydown.enter.exact.prevent="send"`，结果是打拼音时一按回车就把半截中文发出去，
// 候选词反而没了。同理 Shift/Ctrl/Alt/Meta + 回车留给换行与快捷键。
function onEnterSend(e: KeyboardEvent, fn: () => void) {
  if (e.isComposing || e.keyCode === 229) return
  if (e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) return
  e.preventDefault()
  fn()
}

// onComposerKey 是主输入框的唯一 keydown 入口。
//
// 不写成「两个监听器各算各的」（@keydown + @keydown.enter）：两个都会收到同一个
// 事件，谁先谁后取决于编译顺序，而「弹层先选中、发送后触发」就是一条发出去半截
// @xxx 的提问。一个函数里按优先级判定，顺序就是代码顺序。
function onComposerKey(e: KeyboardEvent, fn: () => void) {
  if (e.isComposing || e.keyCode === 229) return
  if (onMentionKey(e)) return
  if (e.key !== 'Enter') return
  if (e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) return
  e.preventDefault()
  fn()
}

// 目录最后一段：落地页那行选择器放不下完整路径，全路径放 title。
function dirTail(p: string): string {
  const parts = p.replace(/\\/g, '/').split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1] : p
}

// 产物去向：AI 生成的文件会写到哪。
//
// · 空间任务 —— 直接落在空间目录里（AI 能顺手改项目已有的文件，这也是空间的意义）；
// · 普通任务 —— 后端在首次发送时给它建一个专属目录（数据目录/ai_tasks/<任务 id 短码>），
//   没有目录的话写操作会被白名单直接拒绝，模型只能干瞪眼。
// 建任务时就说清楚，比等用户发现「AI 不肯写文件」再回头找原因强。
const newTaskOutHint = computed(() => {
  if (newTaskSpace.value === NEW_SPACE) {
    return newTaskDir.value ? t('aiNewTaskOutDir', { dir: newTaskDir.value }) : t('aiNewTaskOutDirPending')
  }
  if (newTaskSpace.value) {
    const sp = spaces.value.find((s) => s.id === newTaskSpace.value)
    return sp?.cwd ? t('aiNewTaskOutDir', { dir: sp.cwd }) : t('aiNewTaskOutDirPending')
  }
  return t('aiNewTaskOutPlain')
})

// 目录比对用：Windows 上同一个目录会有盘符大小写、正斜杠、尾部斜杠的写法差异，
// 直接字符串相等会把「已存在的空间」判成不存在，于是又建一个同目录的。
function normDir(p: string): string {
  return p.replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase()
}

async function pickNewTaskDir() {
  // unwrap 在 code≠0 时是抛异常，不是返回带 ok 的结果：不包 try 的话，
  // 原生对话框出错会变成一条未处理的 Promise 拒绝，界面上什么都不会发生。
  let dir: string | null = null
  try {
    dir = unwrap<string | null>(await PickFolderPath(t('aiPickFolderTitle')))
  } catch (e) {
    toast.error(getErrorMessage(e))
    return
  }
  if (!dir) return
  // 选到的目录已经是某个空间：直接切到那个空间。后端同名幂等也能兜住，
  // 但那样用户看到的是「我选了目录，下拉却跳到别处」，不如当场说清楚。
  const hit = spaces.value.find((s) => normDir(s.cwd || '') === normDir(dir))
  if (hit) {
    newTaskSpace.value = hit.id
    newTaskDir.value = ''
    toast.success(t('aiSpaceAlreadyExists', { name: hit.name }))
    return
  }
  newTaskDir.value = dir
}

// 解析这条新任务该落在哪：归属三选一（已有空间 / 新建空间（选目录，后端按目录幂等）/
// 普通任务）。打包成一处，是因为「点创建」和「直接回车发送」两条路都要它。
async function resolveNewTaskTarget(): Promise<{ spaceId: string; dir: string }> {
  if (newTaskSpace.value === NEW_SPACE) {
    const dir = newTaskDir.value.trim()
    // 说了要新建空间却没选目录：不静默降级成普通任务，那样用户以为建了空间，
    // 结果任务落在普通组里。
    if (!dir) throw new Error(t('aiNewTaskDirRequired'))
    const sp = unwrap<AISpace>(await AICreateSpace('', dir))
    await loadSpaces()
    return { spaceId: (sp?.id || '').trim(), dir: (sp?.cwd || dir).trim() }
  }
  if (newTaskSpace.value) {
    // 已有空间：目录以库里为准，不拿前端缓存的路径去设工作目录。
    const sp = spaces.value.find((s) => s.id === newTaskSpace.value)
    // 空间在别处被删了（回收站、另一个窗口）：不能静默退回普通任务 ——
    // 用户以为任务挂在这个项目下，产物却去了 ai_tasks 里，事后只会觉得「文件找不到」。
    if (!sp) throw new Error(t('aiSpaceGone'))
    return { spaceId: sp.id, dir: (sp.cwd || '').trim() }
  }
  return { spaceId: '', dir: '' }
}

// 任务落进某个空间时把它在侧栏展开，好让用户看到新任务排到哪了；
// 整组若被收起、或这个空间落在折叠线之外，也一并打开——否则「建完了侧栏那没有」
// 看起来像没建成。
function revealSpace(spaceId: string) {
  if (!spaceId) return
  spacesOpen.value = true
  toggleSpace(spaceId, true)
  if (spaces.value.findIndex((s) => s.id === spaceId) >= FOLD_SPACES) spacesExpanded.value = true
}

// 落地页里直接发送：先把归属与模式定下来（会话本身仍由后端在收到第一条消息时创建），
// 再走统一的 send()。失败就停在落地页，草稿还在。
async function heroSend() {
  if (!hasKey.value || streaming.value) return
  if (!input.value.trim() && !attachments.value.length) return
  try {
    const target = await resolveNewTaskTarget()
    pendingSpaceId.value = target.spaceId
    convCwd.value = target.dir
    mode.value = newTaskMode.value
    revealSpace(target.spaceId)
  } catch (e) {
    toast.error(getErrorMessage(e))
    return
  }
  await send()
}

// 清出一条空任务：会话 id 置空后，下一条消息发出去时才由后端真正创建。
//
// 不在「新建」这一刻就建会话，是因为那样每点一次就多一条空对话——
// 用户往往是点了又改主意，空会话会越攒越多。
function beginBlankTask() {
  activeId.value = ''
  messages.value = []
  resetStreamState()
  errorMsg.value = ''
  // 回到落地页时必须是「干净的默认态」：沿用上一次选过的类型和空间，
  // 下一条消息就会莫名其妙落进另一个项目的目录里（删掉一条任务后尤其容易中招）。
  newTaskMode.value = 'chat'
  newTaskSpace.value = ''
  newTaskDir.value = ''
}

// ---- 空间 ----

function isSpaceExpanded(id: string): boolean {
  return expandedSpaces.value.includes(id)
}

function toggleSpace(id: string, force?: boolean) {
  const on = force !== undefined ? force : !isSpaceExpanded(id)
  if (on) {
    if (!isSpaceExpanded(id)) expandedSpaces.value = [...expandedSpaces.value, id]
    return
  }
  expandedSpaces.value = expandedSpaces.value.filter((x) => x !== id)
}

// 删空间比删任务危险得多：一次带走它下面全部任务。确认语里必须带数量。
async function delSpace(s: AISpace) {
  const n = conversations.value.filter((c) => (c.space_id || '') === s.id).length
  const msg = n > 0 ? t('aiDelSpaceConfirmWithTasks', { name: s.name, n }) : t('aiDelSpaceConfirm', { name: s.name })
  if (!(await toast.confirm(msg))) return
  // 空间里有正在流的任务：先掐流，否则后端会往一个已被删掉的会话里写内容。
  // 注意判的是「当前任务是否属于这个空间」而不是「当前任务是否正在流」：
  // 停在一个空闲任务上删空间时，后端已经把任务硬删了，界面却还留着它的消息，
  // 下一次发送只会得到一句「会话不存在或已被删除」——用户完全不知道自己在看一条幽灵任务。
  const activeInSpace = !!activeId.value &&
    conversations.value.some((c) => c.id === activeId.value && (c.space_id || '') === s.id)
  if (streaming.value && activeInSpace) stop()
  try {
    unwrap(await AIDeleteSpace(s.id))
    if (activeInSpace) beginBlankTask()
    await Promise.all([loadConversations(), loadSpaces()])
    toast.success(t('aiMovedToRecycle'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 空间改名：与任务一样走行内编辑（双击名字或点铅笔）。
//
// 原来用的是 window.prompt —— 原生对话框在 WebView2 里是系统样式、不跟随主题，
// 中文输入法组合期间还容易丢字（同一个理由，SSH 分组命名早就改成了自建弹窗）。
// 一个短名字犯不上开弹窗，行内改完按回车就走。
const renamingSpaceId = ref('')
const spaceNameText = ref('')
const spaceNameInput = ref<HTMLInputElement | null>(null)

function startRenameSpace(s: AISpace) {
  renamingSpaceId.value = s.id
  spaceNameText.value = s.name || ''
  // 等 input 渲染出来再聚焦全选：不全选就得先手动删掉旧名字。
  nextTick(() => focusRenameInput(spaceNameInput.value))
}

function cancelRenameSpace() {
  renamingSpaceId.value = ''
  spaceNameText.value = ''
}

// Enter 与 blur 都会走到这里（Enter 后输入框随即失焦），所以进函数先清 id：
// 第二次调用读到空 id 直接返回，避免同一份名字提交两遍。
async function commitRenameSpace(s: AISpace) {
  const id = renamingSpaceId.value
  if (!id) return
  const name = spaceNameText.value.trim()
  renamingSpaceId.value = ''
  if (!name || name === (s.name || '')) return
  try {
    unwrap<string>(await AIRenameSpace(id, name))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
  // 无论成败都重取：失败时界面若停在用户改过的样子，会与库里的名字不一致。
  await loadSpaces()
}

// 在某个空间里新建任务：省得每次都回落地页从「归属空间」下拉里翻。
function newTaskInSpace(s: AISpace) {
  openNewTask()
  newTaskSpace.value = s.id
  revealSpace(s.id)
}

// 重置所有流式状态（切会话/新会话时调用）
function resetStreamState() {
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  streamBuf = ''
  streamReasonBuf = ''
  streamingText.value = ''
  streamBlocks.value = []
  streamTools.value = []
  runRules.value = []
  reasoningText.value = ''
  // 待发送的 @ 引用跟着一起清：切会话之后 cwd 可能变了，
  // 留在输入框上方的 chip 会拿着旧路径去请求，结果只是后端一堆「引用被拒」。
  mentions.value = []
  mentionItems.value = []
  // 流式块缓存按回复隔离：一条回复的块原文可能与另一条完全相同
  // （如「好的」这类短回复），跨回复复用是安全的，但没必要长期持有。
  liveBlockCache.clear()
}

async function delConv(id: string) {
  // 提示语点明「可恢复」：后端已经把它连消息一起拍进回收站，
  // 说「删除」会让用户以为再也找不回来，从而不敢清理侧栏。
  if (!(await toast.confirm(t('aiDelTaskConfirm')))) return
  // 删的正是当前在跑的会话：先掐流再删。否则后端会先发现「会话已被删除」
  // 而把内容写失败，前端还会抱着一条指向已删会话的流继续读。
  if (streaming.value && activeId.value === id) stop()
  try {
    unwrap(await AIDeleteConversation(id))
    if (activeId.value === id) beginBlankTask()
    await loadConversations()
    toast.success(t('aiMovedToRecycle'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 上一个真正生效的模型：切模型失败时把下拉框拉回去。
// 不还原的话，界面显示的是新模型、后端实际请求还是旧模型——
// 这种「看起来切了其实没切」的不一致是最难排查的一类问题。
let lastActiveProfile = ''

async function onModelChange() {
  const want = aiActive.value
  if (!want) return
  try {
    unwrap(await AISetActiveProfile(want))
    lastActiveProfile = want
  } catch (e: any) {
    toast.error(getErrorMessage(e))
    aiActive.value = lastActiveProfile
  }
}

// ---- 失败重试 ----
// 后端在调模型之前就把用户消息落库了（所以失败后历史不会丢），
// 但没有重试入口时用户只能手动重打一遍问题——图片还得重新贴。
// retryPending 让 send() 跳过再插一个 user 气泡：那条消息本来就在列表里，
// 后端 saveUserTurn 对「同内容 + 同图片」也是幂等的，重发不会产生重复行。
let retryPending = false

function canRetry(): boolean {
  if (streaming.value) return false
  return messages.value.some((m) => m.role === 'user')
}

function retryLast() {
  if (!canRetry()) return
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const m = messages.value[i]
    if (m.role !== 'user') continue
    errorMsg.value = ''
    retryPending = true
    input.value = m.content
    attachments.value = (m.imgList || []).map((img) => ({
      kind: img.kind,
      url: img.url,
      dataUrl: img.dataUrl,
      name: img.name || 'image',
    }))
    send()
    return
  }
}

async function regenTitle() {
  if (!activeId.value || streaming.value) return
  try {
    const title = unwrap<string>(await AIRegenerateTitle(activeId.value))
    if (title) {
      const i = conversations.value.findIndex((c) => c.id === activeId.value)
      if (i >= 0) conversations.value[i].title = title
      else loadConversations()
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// ---- 会话改名 ----
//
// 自动生成的标题常常只是「大概意思」，用户想换成自己能记住的说法；而重生成
// 要再花一次模型调用（输入是整段对话），改两个字不该有这种代价。
// 底层 UpdateAIConversationMeta 一直都在（摘要压缩也在用它），此前只是没有对外入口。
//
// 做成行内编辑而不是弹窗：为一个短标题开一层模态框，输入成本比标题本身还高。
const renamingId = ref('')
const renameText = ref('')
const renameInput = ref<HTMLInputElement | null>(null)

function startRename(c: AIConversation) {
  renamingId.value = c.id
  renameText.value = c.title || ''
  // 等 input 渲染出来再聚焦并全选：不全选的话，用户得先手动删掉旧标题才能打新的。
  nextTick(() => focusRenameInput(renameInput.value))
}

// 聚焦并全选行内改名的输入框。
//
// 必须容错数组：v-for 里的字符串 ref 会被 Vue 收集成数组（哪怕同一时刻只渲染了一个），
// 直接 `ref.value?.focus()` 拿到的是 [input]，`.focus` 不存在 → nextTick 里抛 TypeError，
// 表现是「点了改名，输入框出来了但没有焦点，打字没反应」。
function focusRenameInput(r: unknown) {
  const el = (Array.isArray(r) ? r[0] : r) as HTMLInputElement | null
  el?.focus()
  el?.select()
}

function cancelRename() {
  renamingId.value = ''
  renameText.value = ''
}

/**
 * 提交改名。
 *
 * Enter 与 blur 都会走到这里（Enter 后输入框随即失焦），所以进函数先清 renamingId：
 * 第二次调用读到空 id 直接返回，避免同一份标题被提交两遍。
 */
async function commitRename(c: AIConversation) {
  const id = renamingId.value
  if (!id) return
  const title = renameText.value.trim()
  renamingId.value = ''
  // 没改或删空都当作放弃：后端会拒绝空标题，为「改到一半又不想改」弹个错误太吵。
  if (!title || title === (c.title || '')) return
  try {
    const saved = unwrap<string>(await AIRenameConversation(id, title))
    const i = conversations.value.findIndex((x) => x.id === id)
    if (i >= 0 && saved) conversations.value[i].title = saved
  } catch (e) {
    toast.error(getErrorMessage(e))
    // 失败时拉回真实值：界面上的标题若停在用户改过的样子，会与库里不一致。
    await loadConversations()
  }
}

// 确保已拿到本地流式服务端口+令牌
async function ensureStreamInfo() {
  if (aiStreamPort.value) return
  const info = unwrap<{ port: number; token: string }>(await AIStreamInfo())
  if (!info) throw new Error('流式服务不可用')
  aiStreamPort.value = info.port
  aiStreamToken.value = info.token
}

// 单条回复的统计信息（耗时 / 首字延迟 / token 用量）
interface AIStreamStat { elapsedMs: number; firstTokenMs: number; promptTokens: number; completionTokens: number; cachedTokens: number; cost?: number; toolRounds?: number }
// messageId -> 统计。挂在消息对象上随消息一起渲染。
const statMap = reactive(new Map<string, AIStreamStat>())

// 把统计渲染成一行小字。只展示有意义的项：
// 全耗时 + 首字延迟（用户能感知的等待）+ token 用量。
// 首字延迟为 0 说明是中断的回复（没等到首字），此时不展示该项。
function formatStat(stat: AIStreamStat): string {
  const parts: string[] = []
  // 耗时只在「本次会话刚生成」时才有（从库里读回来的消息没有），0 即隐藏
  if (stat.elapsedMs > 0) parts.push(t('aiStatElapsed', { ms: formatDuration(stat.elapsedMs) }))
  if (stat.firstTokenMs > 0) {
    parts.push(t('aiStatFirstToken', { ms: formatDuration(stat.firstTokenMs) }))
  }
  if (stat.promptTokens > 0 || stat.completionTokens > 0) {
    const pct = stat.promptTokens > 0 ? Math.round((stat.cachedTokens / stat.promptTokens) * 1000) / 10 : 0
    parts.push(pct > 0
      ? t('aiStatTokensCached', { in: fmtTokens(stat.promptTokens), out: fmtTokens(stat.completionTokens), pct })
      : t('aiStatTokens', { in: fmtTokens(stat.promptTokens), out: fmtTokens(stat.completionTokens) }))
  }
  // 工具轮次排在 token 之后、费用之前：它与 token 是同一件事的两个侧面
  // （同一笔钱，查了五轮还是空聊五轮），放在一起才读得通。
  if (stat.toolRounds && stat.toolRounds > 0) {
    parts.push(t('aiStatToolRounds', { n: stat.toolRounds }))
  }
  // 费用由后端算（价目表只有 ai_pricing.go 一份）；未收录的模型没有 cost，
  // 这里就不显示——与设置页「未知模型不计价」保持同一口径。
  if (stat.cost && stat.cost > 0) parts.push(t('aiStatCost', { c: fmtCost(stat.cost) }))
  return parts.join(' · ')
}

// token 数折算成 k/M：真实对话的输入常是几万到几十万，原样打印会把这一行拖得很长
function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 1 : 2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + 'k'
  return String(n)
}

// 费用格式化：与设置页用量面板同一口径（小额多给两位小数，否则 ¥0.00 会把
// 「这条几乎不要钱」和「这条免费」显示成一个样子）。
function fmtCost(n: number): string {
  if (!n) return '¥0'
  if (n >= 1) return '¥' + n.toFixed(2)
  if (n >= 0.01) return '¥' + n.toFixed(3)
  return '¥' + n.toFixed(4)
}

// 单条消息要显示的统计：优先用本次会话内存里的（含耗时/首字），
// 否则回落到消息自带的用量 —— 这样刷新页面或换会话回来，token 与缓存命中仍在。
function msgStat(m: AIMessage): AIStreamStat | null {
  const live = statMap.get(m.id)
  if (live) return live
  // 有工具轮次也要显示这一行：即使上游没回 usage（token 全 0），
  // 「这条回答调了 3 轮工具」仍然是用户想知道的。
  if (!m.prompt_tokens && !m.completion_tokens && !m.tool_rounds) return null
  return {
    elapsedMs: 0, firstTokenMs: 0,
    promptTokens: m.prompt_tokens || 0,
    completionTokens: m.completion_tokens || 0,
    cachedTokens: m.cached_tokens || 0,
    cost: m.cost || 0,
    // 刷新后从库里的 tool_rounds 接管，与流式结束时那一眼显示的口径一致
    toolRounds: m.tool_rounds || 0,
  }
}

// 毫秒 → 人类可读：<1s 用 ms，否则一位小数的秒
function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

async function send() {
  // 先读后清：任何提前 return（没配 Key / 空消息 / 正在流式）都不能把标志
  // 留给下一次手动发送，否则那条用户发言将不再出现在气泡里。
  const reuseUserBubble = retryPending
  retryPending = false
  if (streaming.value) return
  const text = input.value.trim()
  if (!text && attachments.value.length === 0) return
  if (!hasKey.value) {
    toast.error(t('aiNeedKey'))
    emit('open-settings', 'ai')
    return
  }
  const convId = activeId.value
  // 发送时的模型名：流式结束前用户可能切档案（onModelChange 会回滚），
  // 事后读 activeProfile 拿到的是新值，所以此刻先定下来。
  const streamModel = aiProfiles.value.find((x) => x.id === aiActive.value)?.model || ''
  // 本轮流式归属的会话：新建会话时由 conv 事件回填。中断/失败时都要按它判断
  // 「用户是否还停留在同一会话」，不能读 activeId —— 用户中途可能已经切走。
  let streamConvId = convId
  // 把待发送附件组包为发送给模型的图片数组（file 带 dataUrl，url 带链接），
  // 同时生成渲染用的 imgList（file 直用 dataUrl，无需二次加载）。
  const imgs: PendingImage[] = attachments.value
  const imgList: AIMsgImage[] = imgs.map((a) => a.kind === 'url'
    ? { kind: 'url', url: a.url, name: a.name }
    : { kind: 'file', dataUrl: a.dataUrl, name: a.name })
  const imagesPayload = imgs.map((a) => a.kind === 'url'
    ? { kind: 'url', url: a.url, name: a.name }
    : { kind: 'file', dataUrl: a.dataUrl, name: a.name })
  // @ 引用只送「引用了谁」：内容由后端读（过 fsguard）并展开进上下文。
  // 先取快照再清空——下面几行会把 mentions 置空，那时再读就没了。
  // 重试要沿用上一次的引用：失败重发时输入框已经空了，重新拼一个空数组
  // 等于把用户 @ 过的文件默默丢掉（而他是为了这份材料才重试的）。
  const mentionSnapshot = mentions.value.map((m) => ({
    kind: m.kind, path: m.path || '', convId: m.convId || '', name: m.name,
  }))
  if (mentionSnapshot.length) lastMentions = mentionSnapshot
  const mentionPayload = mentionSnapshot.length ? mentionSnapshot : (reuseUserBubble ? lastMentions : [])
  // 立即把用户消息显示出来，随后在消息区内等待 AI 流式回复
  const msg: AIMessage = {
    id: 'usr-' + Date.now() + '-' + Math.random().toString(36).slice(2, 7), conv_id: convId, role: 'user', content: text,
    created_at: new Date().toISOString(), imgList,
  }
  // 重试：那条 user 消息本来就在列表里，别再插一个重复气泡。
  // （后端 saveUserTurn 对「同内容 + 同图片」幂等，DB 里也不会多出一行）
  if (!reuseUserBubble) messages.value.push(msg)
  stickToBottom.value = true
  showJumpBtn.value = false
  scrollToBottom()
  input.value = ''
  attachments.value = []
  mentions.value = []
  streaming.value = true
  resetStreamState()
  errorMsg.value = ''
  const startedAt = performance.now()
  try {
    await ensureStreamInfo()
    const ctrl = new AbortController()
    streamCtrl = ctrl
    const resp = await fetch(`http://127.0.0.1:${aiStreamPort.value}/ai/stream`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Stream-Token': aiStreamToken.value,
      },
      body: JSON.stringify({
        convId,
        mode: mode.value,
        // 归属只在新建时生效：后端忽略已有会话传来的 spaceId，
        // 免得「随便发一句话」就把任务搬了家。
        spaceId: convId ? undefined : (pendingSpaceId.value || undefined),
        message: text,
        // 新会话还没有 id，工作目录只能随这一次请求带过去（后端建会话时写库）。
        cwd: convCwd.value || undefined,
        images: imagesPayload,
        mentions: mentionPayload,
      }),
      signal: ctrl.signal,
    })
    if (!resp.ok) {
      // 服务端在进入流式之前就会拒掉一批请求（413 请求体过大 / 403 令牌失效 / 400 JSON 坏了）。
      // 这些响应是纯文本、没有 SSE 帧：不在这里拦，下面按 SSE 解析只会读出「什么都没发生」，
      // 用户看到的是转了一圈然后静默回到原样。把状态码和正文原样抛出来。
      let detail = ''
      try { detail = (await resp.text()).trim() } catch { detail = '' }
      throw new Error(`HTTP ${resp.status}${detail ? '：' + detail : ''}`)
    }
    if (!resp.body) throw new Error('无响应流')
    const reader = resp.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    // SSE 解析：消息以 \n\n 分隔，每条消息含 event: 和 data: 行
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      let sep
      while ((sep = buf.indexOf('\n\n')) >= 0) {
        const rawMsg = buf.slice(0, sep)
        buf = buf.slice(sep + 2)

        let event = 'message'
        let dataStr = ''
        for (const line of rawMsg.split('\n')) {
          if (line.startsWith('event: ')) {
            event = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            dataStr += line.slice(6)
          }
          // 忽略注释行（以 : 开头）和空行
        }
        if (!dataStr) continue

        let data: any
        try { data = JSON.parse(dataStr) } catch { continue }

        switch (event) {
          case 'token':
            // 写入普通缓冲，按帧批量提交到响应式状态
            if (data.text) { streamBuf += data.text; scheduleStreamFlush() }
            break
          case 'reasoning':
            if (data.text) { streamReasonBuf += data.text; scheduleStreamFlush() }
            break
          case 'tool':
            // 工具开始执行：先把卡片挂上去（「调用中」），不等结果。
            // 一个工具可能跑几秒（扫端口、读大文件），这几秒界面必须看起来是活的，
            // 否则用户会以为卡死并去点停止——那反而中断了本来正常的调用。
            streamTools.value.push({
              id: String(data.id || ''),
              name: String(data.name || ''),
              args: String(data.args || ''),
              round: Number(data.round) || 0,
              running: true,
            })
            break
          case 'approval': {
            // 写类 / 高危工具在宿主侧停下来等确认（见 ai_approval.go）。
            // 后端保证「先发 tool 再发 approval」，所以这里一定能找到卡片。
            const at = streamTools.value.find((x) => x.id === String(data.callId || ''))
            if (at) {
              at.approval = 'pending'
              at.riskNote = String(data.riskNote || '')
              at.rememberable = !!data.rememberable
              at.grantScope = String(data.grantScope || '')
              // 会话由后端下发：用户可能在这张卡等着的时候切走再切回，
              // 那时 activeId 已经不是发起审批的那个会话了。
              at.approvalConvId = String(data.convId || '')
            }
            break
          }
          case 'tool_result': {
            // 结果回填到同一张卡片。按 id 找而不是按「最后一条」：
            // 一轮里可能并发调多个工具，回来的顺序不一定与发出顺序一致。
            const tc = streamTools.value.find((x) => x.id === String(data.id || ''))
            if (tc) {
              tc.running = false
              tc.ms = Number(data.ms) || 0
              tc.error = String(data.error || '')
              tc.result = String(data.result || '')
              // dedup：与本次提问里之前某次调用完全相同，宿主没真执行。
              // 不标出来的话，两张一模一样的卡片看起来就是「工具跑了两遍」。
              tc.dedup = !!data.dedup
              tc.added = Number(data.added) || 0
              tc.removed = Number(data.removed) || 0
              tc.diff = String(data.diff || '')
              // 编排类工具带回 run/node：流式期就能挂出 run 树，不用等整轮回填。
              tc.run_id = String(data.runId || '')
              tc.node_id = String(data.nodeId || '')
              // 写类工具刚产生了一个回滚点：立刻拉一次，让这张卡片上出现
              // 「回滚到此处」。等到 done 再拉的话，「改错了想马上退回去」
              // 这个最需要它的时刻恰好没有按钮。
              if ((tc.name === 'file_edit' || tc.name === 'file_write') && !data.error) {
                void loadCheckpoints(activeId.value)
              }
            }
            break
          }
          case 'conv':
            if (data.id) { activeId.value = data.id; streamConvId = data.id }
            // 会话是首次发送时才建的：它这时才拿到 space_id，侧栏要重取
            // 才知道该把它排到哪个空间下（否则要等下次刷新才出现）。
            if (data.spaceId) loadConversations()
            // 归属已随这次请求落库，待定值作废（下一条消息走的是 convId，不带 spaceId）。
            pendingSpaceId.value = ''
            if (data.title !== undefined) {
              const i = conversations.value.findIndex((c) => c.id === data.id)
              if (i >= 0) conversations.value[i].title = data.title
              else loadConversations()
            }
            break
          case 'rules':
            // 本轮生效的项目规则文件（AGENTS.md / .quickdock/rules*）。
            // 只存内存、不落库：规则是「这次提问读了哪些文件」的执行时事实，不是对话内容。
            runRules.value = Array.isArray(data.files)
              ? (data.files as any[]).map((f) => ({ name: String(f?.name || ''), bytes: Number(f?.bytes) || 0, truncated: !!f?.truncated }))
              : []
            break
          case 'notice':
            // 后端主动告知的过程事件（上下文超窗已压缩、引用读取失败等）。
            // 必须让用户看见：多条问题一起消失时，不说一声就像「AI 忽然忘事」。
            // level 决定颜色：引用失败是「没做成」，不该以绿条出现。
            if (data.message) {
              if (data.level === 'error') toast.error(String(data.message))
              else toast.success(String(data.message))
            }
            break
          case 'cwd':
            // 后端本轮才定下工作目录（普通任务的专属目录是首次发送时才建的，
            // 空间任务的目录也可能这时才回退到空间的）。不回传的话工具栏那条
            // 指示要等切走再切回才出现，而用户此刻正想知道文件会写到哪。
            if (data.cwd) {
              convCwd.value = String(data.cwd)
              const ci = conversations.value.findIndex((c) => c.id === activeId.value)
              if (ci >= 0) conversations.value[ci].cwd = convCwd.value
            }
            break
          case 'done':
            if (data.convId) {
              if (data.convId !== activeId.value) break // 对话已被切换，忽略过期事件
              // 立即 flush，把缓冲里的尾部文字全部落到响应式状态
              flushStream()
              const content = streamingText.value
              const reasoning = reasoningText.value
              // 有工具记录也必须固化消息：模型可能「调了 3 个工具但没写答案」，
              // 这时 content 是空的，但用户需要看到它做了什么、钱花在哪。
              if (content || reasoning || streamTools.value.length) {
                const msg: Record<string, any> = { id: `ast-${Date.now()}`, conv_id: data.convId, role: 'assistant', content: content || '', created_at: new Date().toISOString() }
                // 模型名由后端回传（它就是落库的那份），拿不到才退回发送时的档案
                const usedModel = String(data.model || streamModel || '').trim()
                if (usedModel) msg.model = usedModel
                if (reasoning) msg.reasoning_content = reasoning
                // 工具记录落到消息上，刷新前就不用重新拉一次会话。
                // 序列化时丢掉 running：库里存的是「发生过什么」，不是「界面此刻什么样」。
                if (streamTools.value.length) {
                  msg.tool_calls = serializeTools(streamTools.value)
                  msg.tool_rounds = Number(data.toolRounds) || countToolRounds(streamTools.value)
                }
                // 规则文件跟着这条回复走，而不是挂在页面顶上：用户事后想问的是
                // 「这条回答当时读了什么约定」，刷新后消失也无所谓（下次提问会重读）。
                if (runRules.value.length) msg.rules = runRules.value.slice()
                messages.value.push(msg as AIMessage)
                // 复用流式期已渲染好的块 HTML，避免落地瞬间整条重新解析
                primeMessageBlocks(msg.id as string, content, streamBlocks.value.map((b) => b.html))
                // 记录本条回复的耗时/token，用于在消息下方展示
                const stat: AIStreamStat = {
                  elapsedMs: Math.round(performance.now() - startedAt),
                  firstTokenMs: Number(data.firstTokenMs) || 0,
                  promptTokens: Number(data.promptTokens) || 0,
                  completionTokens: Number(data.completionTokens) || 0,
                  cachedTokens: Number(data.cachedTokens) || 0,
                  // 后端算好带回来；未收录模型没有这个字段 → 0 即隐藏
                  cost: Number(data.cost) || 0,
                  // 工具轮次：token 里已经含了这些轮次的消耗，
                  // 但「同一个价钱、是模型自己想多了还是真查了五轮」是两回事。
                  toolRounds: Number(data.toolRounds) || 0,
                }
                statMap.set(msg.id as string, stat)
                // 同步到消息对象：刷新后由 DB 里的同名字段接管，展示逻辑不用分两套
                Object.assign(msg, {
                  prompt_tokens: stat.promptTokens,
                  completion_tokens: stat.completionTokens,
                  cached_tokens: stat.cachedTokens,
                })
                scrollToBottom()
              }
              streamingText.value = ''
              streamBlocks.value = []
              // 清空工具卡片：done 分支已经把记录落到消息上了，
              // 留着会让 finally 再补一条重复气泡（那里靠它判断是否被中断）。
              streamTools.value = []
              reasoningText.value = ''
              loadConversations()
            }
            break
          case 'error':
            flushStream()
            errorMsg.value = data.message || data.error || '未知错误'
            break
        }
      }
    }
  } catch (e: any) {
    if (e && e.name !== 'AbortError') errorMsg.value = getErrorMessage(e)
  } finally {
    // 中断（用户点停止）时：把已生成的内容固化成一条正式消息，避免内容凭空消失。
    // 之前 stop() 只是 abort，已收到的文字既没进 messages 也没清空，
    // 结果是「AI 一直在回复」但内容卡在半截、也无法复制。
    // 必须先 flush：否则还留在缓冲里（streamBuf）的尾部文字会随 clear 一起丢掉。
    flushStream()
    // 中断时也可能已经有工具卡片：模型调了工具、结果还没回来用户就点了停止。
    // 这些记录同样要固化——「调过但没结果」本身就是这次中断发生过的事实。
    const hadTools = streamTools.value.length > 0
    if (streamingText.value || reasoningText.value || hadTools) {
      const content = streamingText.value
      const reasoning = reasoningText.value
      // 只往「用户还停留着的那个会话」里补气泡。切走之后后端仍会把半截内容落库
      // （带 stopped 标记），但绝不能塞进另一个会话的消息列表——以前这里用的是
      // activeId.value，selectConv 先 stop 再改 activeId，半截内容就串到新会话里了。
      if (content || reasoning || hadTools) {
        if (streamConvId && activeId.value === streamConvId) {
          const aid = `ast-${Date.now()}-stop`
          const msg: Record<string, any> = { id: aid, conv_id: streamConvId, role: 'assistant', content: content || '', created_at: new Date().toISOString(), stopped: true }
          // 中断时后端还没发 done，模型名用发送时记下的那份
          if (streamModel) msg.model = streamModel
          if (reasoning) msg.reasoning_content = reasoning
          if (hadTools) {
            msg.tool_calls = serializeTools(streamTools.value)
            msg.tool_rounds = countToolRounds(streamTools.value)
          }
          messages.value.push(msg as AIMessage)
          // 同样复用流式期已渲染的块
          primeMessageBlocks(aid, content, streamBlocks.value.map((b) => b.html))
          const stat: AIStreamStat = {
            elapsedMs: Math.round(performance.now() - startedAt),
            firstTokenMs: 0,
            promptTokens: 0,
            completionTokens: 0,
            cachedTokens: 0,
            toolRounds: hadTools ? countToolRounds(streamTools.value) : 0,
          }
          statMap.set(aid, stat)
        }
      }
    }
    streamingText.value = ''
    streamBlocks.value = []
    streamTools.value = []
    reasoningText.value = ''
    streamBuf = ''
    streamReasonBuf = ''
    streaming.value = false
    streamCtrl = null
    // 流式结束后尾块不再是「进行中」：补一次扫描，把最后一张图（可能就在结尾）画出来
    scheduleMermaidScan()
    // 在别的页面期间回答跑完了：给侧栏一个未读点，否则用户不知道已经能看
    if (!pageActive.value) aiFinishedUnread.value = true
  }
}

function stop() {
  if (streamCtrl) streamCtrl.abort()
}

// ---- 保活（KeepAlive）后的可见性 ----
// 本页被 App 的 KeepAlive 白名单留住：切到别的导航页时组件只是 deactivate，
// 正在进行的 fetch 流和已生成的文字都还在（以前是销毁 → onUnmounted 里 abort →
// 回答整段消失）。代价是用户在别的页看不到任何迹象，所以把状态外抛给侧栏：
// 生成中 = 转圈，已生成且用户还没回来 = 未读点。
const pageActive = ref(true)
watch(streaming, (v) => { aiGenerating.value = v })

// 告诉后端「AI 页是不是在前台」。
//
// 为什么需要：完成通知的判定不能只看窗口焦点 —— 用户在 AI 页盯着回答生成时，
// 可能去点了一下别的应用（窗口失焦），按焦点判断就会往通知中心塞一条
// 「AI 回答已完成」，而他刚刚就看着这句话写完。后端逻辑见 services/ai/ai_stream.go。
//
// 失败无妨（后端是旧二进制时没有这个方法）：最坏退化成按窗口焦点判断的老行为。
function reportAIPageVisible(visible: boolean) {
  try {
    const p = SetAIPageVisible(visible)
    if (p && typeof p.catch === 'function') p.catch(() => undefined)
  } catch {
    // 绑定不存在（应用未重启）时忽略
  }
}

onActivated(() => {
  pageActive.value = true
  aiFinishedUnread.value = false
  reportAIPageVisible(true)
  // 回来时停在原位会看不到后面生成的内容：流式期间一直是贴底的，直接拉到底
  if (streaming.value) nextTick(scrollToBottom)
})
onDeactivated(() => {
  pageActive.value = false
  reportAIPageVisible(false)
})

async function copy(text: string) {
  // 统一入口见 utils/clipboard：navigator.clipboard 在 WebView2 里要么被静默拒绝，
  // 要么（execCommand 兜底）返回 true 但其实没写进去——两种都会让用户看到「已复制」的假象。
  const ok = await copyToClipboard(text ?? '')
  if (ok) {
    toast.success(t('copied'))
  } else {
    toast.error(t('copyFailed'))
  }
}

// —— 代码块按钮（复制 / html 预览）——
// 按钮在 v-html 里（DOMPurify 放行 button），无法直接绑 Vue 事件，
// 故在消息区做事件委托。
function onMsgClick(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  if (!target) return

  // 流程图工具栏：看源码 / 折叠。状态挂在容器属性上（见 utils/mermaidMarkdown.ts），
  // 这里只做事件委托，真正的翻转逻辑与按钮文案都在 utils 里。
  if (handleMermaidToolbarClick(target, mermaidLabels())) return
  // 表格表头点击排序（v-html 真实 DOM，原生事件委托）
  const th = target.closest?.('.md-th-sortable') as HTMLElement | null
  if (th) { sortTableByColumn(th); return }

  // html 代码块的「预览」：取源码塞进沙箱 iframe（见模板里的 ai-html-preview）
  const runBtn = target.closest?.('[data-md-run]') as HTMLElement | null
  if (runBtn) {
    const code = runBtn.closest('.md-code')?.querySelector('code')?.textContent ?? ''
    if (code.trim()) {
      // 没有 <!doctype> / <html> 的片段也当成整页渲染：浏览器本来就会补全
      htmlPreview.value = code
    }
    return
  }

  const btn = target.closest?.('[data-md-copy]') as HTMLElement | null
  if (!btn) return
  const wrap = btn.closest?.('.md-code') as HTMLElement | null
  const code = wrap?.querySelector('code')?.textContent ?? ''
  if (!code) return
  copy(code)
  // 按钮短暂显示「已复制」反馈
  btn.classList.add('copied')
  btn.setAttribute('aria-label', t('copied'))
  window.setTimeout(() => {
    btn.classList.remove('copied')
    btn.setAttribute('aria-label', t('copyCode'))
  }, 1200)
}

// —— HTML 预览（沙箱 iframe，全屏遮罩）——
// sandbox 里绝对不能带 allow-same-origin：那样 iframe 与宿主同源，
// 里面的脚本就能拿到 QuickDock 的 window 与 Wails 运行时。允许 scripts /
// modals（示例页常用 alert）/ forms 就够了，opaque origin 下它连相对路径都取不到。
const htmlPreview = ref('')

function closeHtmlPreview() {
  htmlPreview.value = ''
}

// —— 右键：把流程图 / 公式复制成图片 ——
// 原生右键菜单只能「复制」成文本（复制到的是 LaTeX 源码或 SVG 标签），
// 而这类内容的去向通常是文档或聊天框——那里需要一张图。
interface CtxTarget {
  el: HTMLElement
  kind: 'mermaid' | 'math' | 'code'
}
const ctxTarget = ref<CtxTarget | null>(null)
const ctxMenu = useFloatMenu({ placement: 'right-start', offset: 4 })

function onMsgContextMenu(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  if (!target) return
  // 只认已经画出来的图和公式：未渲染的 mermaid 只是一段源码，走原生菜单更合适
  const mmd = target.closest('.md-mermaid[data-mermaid-done="1"]') as HTMLElement | null
  const math = mmd ? null : (target.closest('.md-math') as HTMLElement | null)
  // 普通代码块（.md-code）：右键也能「复制为图片」，与图/公式体验统一
  const code = mmd || math ? null : (target.closest('.md-code') as HTMLElement | null)
  const el = mmd || math || code
  if (!el) return
  // 折叠状态的图尺寸是 0（内容 display:none），转图会得到空图 —— 先展开再弹菜单。
  // 右键本身就意味着「我要对这张图做点什么」，展开是符合预期的，也不留后遗症。
  if (mmd?.hasAttribute('data-mermaid-folded')) setMermaidFolded(mmd, false, mermaidLabels())
  // 源码视图下 SVG 也是 display:none，「复制为图片」会截到代码块而不是图。
  // 与折叠态同样处理：右键即代表要对图操作，自动切到图形视图。
  if (mmd?.hasAttribute('data-mermaid-src')) setMermaidSourceMode(mmd, false, mermaidLabels())
  e.preventDefault()
  ctxTarget.value = { el, kind: mmd ? 'mermaid' : math ? 'math' : 'code' }
  ctxMenu.showAt(e.clientX, e.clientY)
}

function closeCtxMenu() {
  ctxMenu.hide()
  ctxTarget.value = null
}

async function ctxCopyImage() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  try {
    const ok = await copyElementAsImage(t0.el)
    if (ok) toast.success(t('aiCopyImageOk'))
    else toast.error(t('aiCopyImageFail'))
  } catch {
    toast.error(t('aiCopyImageFail'))
  }
}

function ctxCopySource() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  let code = ''
  if (t0.kind === 'math') {
    // 公式源码存在渲染外层 span 的 data-tex（见 utils/mathMarkdown.ts renderMath）
    code = t0.el.dataset.tex || ''
  } else {
    // 渲染成功后 DOM 里只剩 SVG，源码在 data-mermaid-code 上（见 utils/mermaidMarkdown.ts）
    code = t0.el.dataset.mermaidCode || t0.el.querySelector('.md-mermaid-src code')?.textContent || ''
  }
  if (code) copy(code)
}

async function ctxDownloadSvg() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  const xml = serializeMermaidSvg(t0.el)
  if (!xml) { toast.error(t('aiDownloadFail')); return }
  downloadBlob(new Blob([xml], { type: 'image/svg+xml;charset=utf-8' }), 'diagram.svg')
  toast.success(t('aiDownloadOk', { name: 'diagram.svg' }))
}

async function ctxDownloadPng() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  const svg = t0.el.querySelector('.md-mermaid-svg svg') as SVGSVGElement | null
  if (!svg) { toast.error(t('aiDownloadFail')); return }
  const blob = await svgToPngBlob(svg, resolveBackground(t0.el))
  if (blob) {
    downloadBlob(blob, 'diagram.png')
    toast.success(t('aiDownloadOk', { name: 'diagram.png' }))
  } else {
    toast.error(t('aiDownloadFail'))
  }
}

// —— 划词（选择文本）复制 ——
const selPop = reactive({ text: '' })
// floating-ui：跟随选区矩形，选区上方显示，放不下自动翻到下方，靠边自动内收
const selCtx = useFloatMenu({ placement: 'top', offset: 8 })

function hideSelPop() {
  selCtx.hide()
  selPop.text = ''
}

function checkSelection() {
  const sel = window.getSelection()
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) { hideSelPop(); return }
  const text = sel.toString().trim()
  if (!text) { hideSelPop(); return }
  const range = sel.getRangeAt(0)
  const container = msgArea.value
  if (!container || !container.contains(range.commonAncestorContainer)) { hideSelPop(); return }
  selPop.text = text
  selCtx.showRect(range.getBoundingClientRect())
}

function onMsgMouseUp() {
  // 延迟到选择完成后再判定
  setTimeout(checkSelection, 0)
}

async function copySelection() {
  const text = selPop.text
  hideSelPop()
  const sel = window.getSelection()
  if (sel) sel.removeAllRanges()
  await copy(text)
}

// 笔记 ↔ AI：AI 回复转存为笔记树里的一篇文档（与剪贴板「存为笔记」一致）。
// 之前写的是「快捷笔记」（独立窗口那一份），主界面看不到，所以表现为「转存没生效」。
function noteTitleOf(text: string, max = 30): string {
  const line = (text || '').split('\n').map(s => s.trim()).find(Boolean) || ''
  return line.length > max ? line.slice(0, max) + '…' : (line || t('aiSaveToNote'))
}

async function saveAsNote(content: string) {
  if (!content || !content.trim()) return
  try {
    unwrap(await CreateNoteDoc('', noteTitleOf(content), content.trim(), 'markdown'))
    toast.success(t('aiSavedToNote'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

function onDocMouseDown(e: MouseEvent) {
  // 右键菜单自己吞掉 mousedown（@mousedown.stop），落到这里的点击都在菜单之外
  if (ctxTarget.value) closeCtxMenu()
  const pop = document.querySelector('.ai-sel-copy')
  if (pop && pop.contains(e.target as Node)) return
  hideSelPop()
}

// 图片全屏查看器：缩略图点击放大。放组件级状态而非每条消息各自持有，
// 同一时刻最多预览一张图；点遮罩 / Esc 关闭，点图不关（防误触）。
const imgViewer = ref<AIMsgImage | null>(null)
function openImgViewer(img: AIMsgImage) {
  imgViewer.value = img
}
function closeImgViewer() {
  imgViewer.value = null
}
function onGlobalKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (imgViewer.value) closeImgViewer()
  else if (htmlPreview.value) closeHtmlPreview()
  else if (ctxTarget.value) closeCtxMenu()
}

onMounted(async () => {
  // KeepAlive 首次挂载也会走 onActivated，但那条路依赖 App.vue 的包裹结构；
  // 这里显式再置一次，保证「一进来就停在 AI 页」这个事实后端一定知道。
  reportAIPageVisible(true)
  await loadProfiles()
  await Promise.all([loadConversations(), loadSpaces()])

  // 仅保留标题同步：这条事件只由「手动改名 / 重生成标题」触发（新会话的创建走 SSE 的
  // conv 事件）。以前这里还有一句 `activeId.value = data.id`，于是重命名侧栏里
  // 任意一条任务都会把当前任务切过去，而消息并没有跟着重载 —— 高亮换了一条、
  // 内容还是上一条，接着发消息就发进了另一条任务里。
  Events.On('ai:conv', (ev: any) => {
    const data = ev?.data
    if (!data) return
    if (data.title !== undefined) {
      const idx = conversations.value.findIndex((c) => c.id === data.id)
      if (idx >= 0) conversations.value[idx].title = data.title
      else loadConversations()
    }
  })
  // 设置在 SettingsModal 中保存配置后刷新下拉列表
  Events.On('ai:profiles-updated', loadProfiles)
  // 点击消息区之外时隐藏划词复制浮层
  document.addEventListener('mousedown', onDocMouseDown, true)
  // 图片查看器的 Esc 关闭（capture 不必要，普通冒泡即可；避免干扰输入框内 Esc 行为的判断）
  document.addEventListener('keydown', onGlobalKeydown)

  // ResizeObserver 观察消息内容容器：内容变高（流式输出、图片加载、代码块撑开）
  // 时把视口拉到底部。这是「AI 一直在回复但视口不跟随」的正确修法——
  // 滚动应由 DOM 实际高度变化驱动，而不是由某个响应式变量间接推断。
  //
  // 绑定必须跟着容器走：首屏是落地页（`isNewTask`），此时 msgContent 还是 null，
  // 早先只在 onMounted 里 observe 一次 → 这条「内容变高就贴底」从来没生效过
  // （切会话、新建任务后容器才出现，而那之后没人再 observe）。
  if (typeof ResizeObserver !== 'undefined') {
    resizeObs = new ResizeObserver(() => {
      if (!stickToBottom.value) return
      const el = msgArea.value
      if (!el) return
      el.scrollTop = el.scrollHeight
    })
  }
})

// 消息容器会随「落地页 ↔ 聊天区」切换出现/消失：观察目标得跟着换。
// 只在挂载时 observe 一次的话，首屏是落地页（容器还不存在）→ 观察永远没绑上。
watch(msgContent, (el, prev) => {
  if (!resizeObs) return
  if (prev) resizeObs.unobserve(prev)
  if (el) resizeObs.observe(el)
})

onUnmounted(() => {
  Events.Off('ai:conv')
  Events.Off('ai:profiles-updated')
  document.removeEventListener('mousedown', onDocMouseDown, true)
  document.removeEventListener('keydown', onGlobalKeydown)
  // 这里的 abort 现在只会在「应用退出」时跑到：切到别的导航页是 deactivate，
  // 组件还活着，流照常继续（见下方 onActivated / onDeactivated）。
  if (streamCtrl) streamCtrl.abort()
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  if (msgSearchTimer) { clearTimeout(msgSearchTimer); msgSearchTimer = undefined }
  if (resizeObs) { resizeObs.disconnect(); resizeObs = null }
  aiGenerating.value = false
  aiFinishedUnread.value = false
  reportAIPageVisible(false)
})
</script>

<template>
  <div class="ai-page">
    <!-- 左侧会话列表 -->
    <aside class="ai-sidebar">
      <button class="ai-new-btn" @click="openNewTask">
        <Plus :size="14" /> {{ t('aiNewTask') }}
      </button>
      <div v-if="conversations.length > 5" class="ai-conv-search">
        <Search :size="12" class="ai-conv-search-icon" />
        <input v-model="convQuery" class="ai-conv-search-input" :placeholder="t('aiSearchConvPh')" />
        <button v-if="convQuery" class="ai-conv-search-clear" @click="convQuery = ''"><X :size="11" /></button>
      </div>
      <div class="ai-conv-list">
        <!-- 消息命中：搜索框同时查正文。只匹配标题的话，「上次那句报错在哪条任务里」
             这种最常用的找法永远搜不到——而标题往往只写「修复配置」。
             放在任务组之上：搜索时用户要的几乎总是它。 -->
        <template v-if="msgHits.length || msgHitsLoading">
          <div class="ai-group-head ai-group-head-btn" @click="msgHitsOpen = !msgHitsOpen">
            <component :is="msgHitsOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
            <span>{{ t('aiGroupMsgHits') }} ({{ msgHits.length }})</span>
          </div>
          <template v-if="msgHitsOpen">
            <div
              v-for="h in msgHits"
              :key="h.conv_id"
              :class="['ai-conv-item', 'is-msg-hit', { active: h.conv_id === activeId }]"
              @click="selectConv(h.conv_id)"
            >
              <Search :size="11" class="ai-conv-mode" />
              <div class="ai-msg-hit-body">
                <div class="ai-msg-hit-head">
                  <span class="ai-conv-title">{{ h.conv_title || t('aiNewChat') }}</span>
                  <span v-if="h.count > 1" class="ai-msg-hit-count">+{{ h.count - 1 }}</span>
                </div>
                <div class="ai-msg-hit-snippet">{{ h.latest.snippet }}</div>
              </div>
            </div>
            <div v-if="msgHitsLoading && !msgHits.length" class="ai-space-empty">{{ t('aiMsgHitSearching') }}</div>
          </template>
        </template>

        <!-- 任务组：不属于任何空间的任务。组标题可点 = 整组折叠 -->
        <div class="ai-group-head ai-group-head-btn" @click="tasksOpen = !tasksOpen">
          <component :is="tasksOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
          <span>{{ t('aiGroupTasks') }} ({{ taskConversations.length }})</span>
        </div>
        <template v-if="tasksOpen">
        <template v-for="c in shownTaskConversations" :key="c.id">
          <div
            :class="['ai-conv-item', { active: c.id === activeId }]"
            @click="selectConv(c.id)"
          >
            <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
            <input
              v-if="renamingId === c.id"
              ref="renameInput"
              v-model="renameText"
              class="ai-conv-rename"
              spellcheck="false"
              maxlength="60"
              @click.stop
              @keydown.enter="onEnterSend($event, () => commitRename(c))"
              @keydown.esc.prevent="cancelRename()"
              @blur="commitRename(c)"
            />
            <!-- 双击标题也能改名：改名是低频操作，给它常驻按钮不如给个不占位的手势 -->
            <span
              v-else
              class="ai-conv-title"
              :title="t('aiRenameHint')"
              @dblclick.stop="startRename(c)"
            >{{ c.title || t('aiNewChat') }}</span>
            <!-- 任务累计用量：只统计「问答」，标题生成 / 摘要压缩不计入（见 ai_usage.go），
                 所以这里的数字应该等于该任务各条回复下方统计之和。
                 折算成 k/M 显示，精确值放 title。 -->
            <span v-if="renamingId !== c.id && (c.prompt_tokens > 0 || c.completion_tokens > 0)" class="ai-conv-tokens" :title="t('aiConvTokenTip', { in: c.prompt_tokens, out: c.completion_tokens })">
              {{ fmtTokens(c.prompt_tokens + c.completion_tokens) }}
            </span>
            <button
              v-if="renamingId !== c.id"
              class="ai-conv-ren"
              :title="t('aiRename')"
              @click.stop="startRename(c)"
            >
              <Pencil :size="12" />
            </button>
            <button v-if="renamingId !== c.id" class="ai-conv-del" :title="t('delete')" @click.stop="delConv(c.id)">
              <Trash2 :size="13" />
            </button>
          </div>
        </template>
        <!-- 超过阈值才出现：展开后同一个按钮变「收起」，来回都走这一处 -->
        <button
          v-if="taskConversations.length > FOLD_TASKS"
          class="ai-conv-more"
          @click="tasksExpanded = !tasksExpanded"
        >{{ tasksExpanded ? t('aiFoldList') : t('aiShowMoreConv', { n: taskConversations.length - FOLD_TASKS }) }}</button>
        </template>

        <!-- 空间组：绑定目录的任务容器 -->
        <div class="ai-group-head ai-group-head-space ai-group-head-btn" @click="spacesOpen = !spacesOpen">
          <component :is="spacesOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
          <span>{{ t('aiGroupSpaces') }} ({{ shownSpaces.length }})</span>
        </div>
        <template v-if="spacesOpen">
        <div v-for="g in visibleSpaces" :key="g.space.id" class="ai-space">
          <div class="ai-space-row" @click="toggleSpace(g.space.id)">
            <component
              :is="isSpaceExpanded(g.space.id) ? ChevronDown : ChevronRight"
              :size="12"
              class="ai-space-caret"
            />
            <Folder :size="12" class="ai-space-icon" />
            <!-- 与任务标题同一套行内改名：为一个短名字弹系统对话框（window.prompt）
                 不跟主题、字体也不一致，而且输入法组合中容易丢字。 -->
            <input
              v-if="renamingSpaceId === g.space.id"
              ref="spaceNameInput"
              v-model="spaceNameText"
              class="ai-conv-rename"
              spellcheck="false"
              maxlength="40"
              @click.stop
              @keydown.enter="onEnterSend($event, () => commitRenameSpace(g.space))"
              @keydown.esc.prevent="cancelRenameSpace()"
              @blur="commitRenameSpace(g.space)"
            />
            <span v-else class="ai-space-name" :title="g.space.cwd" @dblclick.stop="startRenameSpace(g.space)">{{ g.space.name }}</span>
            <span class="ai-space-count">{{ g.tasks.length }}</span>
            <!-- 在这个空间里新建任务：否则每次都要去落地页的「归属空间」下拉里翻一遍 -->
            <button
              v-if="renamingSpaceId !== g.space.id"
              class="ai-conv-add"
              :title="t('aiNewTaskInSpace')"
              @click.stop="newTaskInSpace(g.space)"
            >
              <Plus :size="12" />
            </button>
            <button
              v-if="renamingSpaceId !== g.space.id"
              class="ai-conv-ren"
              :title="t('aiRename')"
              @click.stop="startRenameSpace(g.space)"
            >
              <Pencil :size="11" />
            </button>
            <button v-if="renamingSpaceId !== g.space.id" class="ai-conv-del" :title="t('delete')" @click.stop="delSpace(g.space)">
              <Trash2 :size="12" />
            </button>
          </div>
          <template v-if="isSpaceExpanded(g.space.id)">
            <div
              v-for="c in spaceTasksShown(g)"
              :key="c.id"
              :class="['ai-conv-item', 'is-in-space', { active: c.id === activeId }]"
              @click="selectConv(c.id)"
            >
              <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
              <span class="ai-conv-title" @dblclick.stop="startRename(c)">{{ c.title || t('aiNewChat') }}</span>
              <button class="ai-conv-del" :title="t('delete')" @click.stop="delConv(c.id)">
                <Trash2 :size="12" />
              </button>
            </div>
            <button
              v-if="g.tasks.length > FOLD_SPACE_TASKS"
              class="ai-conv-more is-in-space"
              @click="toggleSpaceTasks(g.space.id)"
            >{{ expandedSpaceTasks.includes(g.space.id) ? t('aiFoldList') : t('aiShowMoreConv', { n: g.tasks.length - FOLD_SPACE_TASKS }) }}</button>
            <div v-if="g.tasks.length === 0" class="ai-space-empty">{{ t('aiSpaceNoTasks') }}</div>
          </template>
        </div>
        <button
          v-if="shownSpaces.length > FOLD_SPACES"
          class="ai-conv-more"
          @click="spacesExpanded = !spacesExpanded"
        >{{ spacesExpanded ? t('aiFoldList') : t('aiShowMoreSpaces', { n: shownSpaces.length - FOLD_SPACES }) }}</button>
        </template>

        <div v-if="conversations.length === 0" class="ai-conv-empty">{{ t('aiNoConversations') }}</div>
        <!-- 正文命中也算「搜到了东西」，不算空：否则上面列着三条消息命中，
             下面紧跟着一句「没有匹配的会话」。 -->
        <div v-else-if="totalMatched === 0 && !msgHits.length && !msgHitsLoading" class="ai-conv-empty">{{ t('aiNoMatchConv') }}</div>
      </div>
    </aside>

    <!-- 右侧聊天区 -->
    <section class="ai-main">
      <!-- 工具栏：只在有任务时出现。落地页不需要它（模型选择在输入框下面，
           目录还没定）。 -->
      <div v-if="!isNewTask" class="ai-toolbar">
        <!-- 目录指示：绑了目录才允许 AI 改文件、跑命令（见后端 writeRoots），
             点一下在资源管理器里打开——产物的目录在数据目录下（普通任务）或项目里（空间任务），
             不给入口的话用户只能从 title 里抄路径自己去翻。
             它只负责「打开」当前已绑定的目录，不是「选目录」（绑定入口在新建任务时）。 -->
        <button v-if="convCwd" class="ai-cwd-tag" :title="t('aiCwdTip', { dir: convCwd })" @click="openCwd">
          <FolderOpen :size="12" />
          <span class="ai-cwd-name">{{ cwdLabel }}</span>
        </button>
        <div class="ai-toolbar-right">
          <select v-model="aiActive" class="ai-model-select" :title="t('aiSwitchModel')" @change="onModelChange">
            <option v-for="p in aiProfiles" :key="p.id" :value="p.id">{{ p.name || p.model }}</option>
          </select>
          <button v-if="activeId" class="ai-icon-btn" :title="t('aiRegenTitle')" @click="regenTitle">
            <RefreshCw :size="13" />
          </button>
          <button v-if="streaming" class="ai-stop" @click="stop">
            <Square :size="13" /> {{ t('aiStop') }}
          </button>
        </div>
      </div>

      <!-- 空态落地页：新建任务不走弹窗，直接在这一页选类型、选空间、提问。
           类型与空间仍是这条任务的属性，首次发送时随请求落库。 -->
      <div v-if="isNewTask" class="ai-hero">
        <div class="ai-hero-inner">
          <h1 class="ai-hero-title">{{ t('aiHeroTitle') }}</h1>

          <!-- 一排类型 pill：默认对话，点一下换成本次任务的类型 -->
          <div class="ai-hero-modes">
            <button
              v-for="m in modes"
              :key="m.key"
              :class="['ai-hero-mode', { active: newTaskMode === m.key }]"
              :title="t(m.desc)"
              @click="newTaskMode = m.key"
            >
              <component :is="m.icon" :size="13" />
              {{ t(m.label) }}
            </button>
          </div>

          <div class="ai-hero-box" @dragover.prevent @drop="onInputDrop">
            <!-- 图片链接输入（与聊天区同一套状态，落地页也保留这个入口） -->
            <div v-if="urlInputOpen" class="ai-url-row">
              <input
                v-model="urlDraft"
                type="text"
                class="ai-url-input"
                :placeholder="t('aiAddImageUrlPh')"
                @keydown.enter.prevent="confirmUrlImage"
              />
              <button class="ai-url-ok" @click="confirmUrlImage">{{ t('add') }}</button>
            </div>
            <div v-if="attachments.length" class="ai-attach-preview">
              <div v-for="(a, i) in attachments" :key="i" class="ai-attach-thumb">
                <img v-if="a.kind === 'file' && a.dataUrl" :src="a.dataUrl" class="ai-attach-img" />
                <img v-else-if="a.kind === 'url'" :src="a.url" class="ai-attach-img" />
                <button class="ai-attach-del" :title="t('delete')" @click="removeAttachment(i)">×</button>
              </div>
            </div>
            <textarea
              ref="heroInputEl"
              v-model="input"
              class="ai-hero-input"
              rows="3"
              :placeholder="t('aiHeroPlaceholder')"
              :disabled="!hasKey"
              @keydown.enter="onEnterSend($event, heroSend)"
              @paste="onInputPaste"
            ></textarea>
            <div class="ai-hero-bar">
              <!-- 归属空间：不选就是普通任务（后端会自动给它建一个专属目录）。
                   已有空间直接选；「新建空间…」才需要选目录——空间按目录唯一。 -->
              <select v-model="newTaskSpace" class="ai-hero-select" :title="t('aiNewTaskSpace')">
                <option value="">{{ t('aiNewTaskSpaceNone') }}</option>
                <option v-for="s in spaces" :key="s.id" :value="s.id" :title="s.cwd">{{ s.name }}</option>
                <option :value="NEW_SPACE">{{ t('aiNewTaskSpaceNew') }}</option>
              </select>
              <button
                v-if="newTaskSpace === NEW_SPACE"
                class="ai-hero-select"
                :title="newTaskDir || t('aiPickFolderTitle')"
                @click="pickNewTaskDir"
              >
                <FolderOpen :size="12" />
                <span class="ai-hero-dir">{{ newTaskDir ? dirTail(newTaskDir) : t('aiNewTaskDirNone') }}</span>
              </button>
              <select v-model="aiActive" class="ai-hero-select" :title="t('aiSwitchModel')" @change="onModelChange">
                <option v-for="p in aiProfiles" :key="p.id" :value="p.id">{{ p.name || p.model }}</option>
              </select>
              <div class="ai-hero-bar-right">
                <button v-if="allowFileImage" class="ai-tool-btn" :title="t('aiAttachImage')" @click="openFilePicker">
                  <ImageIcon :size="16" />
                </button>
                <button v-if="allowUrlImage" class="ai-tool-btn" :title="t('aiAddImageUrl')" @click="toggleUrlInput">
                  <Link2 :size="16" />
                </button>
                <button
                  class="ai-hero-send"
                  :disabled="!hasKey || (!input.trim() && !attachments.length)"
                  :title="t('send')"
                  @click="heroSend"
                >
                  <ArrowUp :size="16" />
                </button>
              </div>
            </div>
          </div>

          <!-- 产物去向：既是说明也是「空间是什么」的解释——比一个空下拉好懂 -->
          <p class="ai-hero-out">{{ newTaskOutHint }}</p>

          <p v-if="!hasKey" class="ai-hero-needkey">
            {{ t('aiNeedKey') }}
            <button class="ai-go-settings" @click="emit('open-settings', 'ai')">{{ t('aiGoSettings') }}</button>
          </p>

          <div class="ai-hero-tips">
            <button v-for="k in heroTips" :key="k" class="ai-hero-tip" @click="fillHeroTip(k)">
              {{ t(k) }}
              <ArrowUp :size="11" class="ai-hero-tip-icon" />
            </button>
          </div>
        </div>
      </div>

      <template v-else>
      <!-- 消息区 -->
      <div ref="msgArea" class="ai-messages" @scroll="onMsgScroll" @mouseup="onMsgMouseUp" @keyup="onMsgMouseUp" @click="onMsgClick" @contextmenu="onMsgContextMenu">
        <div ref="msgContent" class="ai-msg-content-wrap">
          <div v-if="!hasKey" class="ai-need-key">
            <Bot :size="40" />
            <p>{{ t('aiNeedKey') }}</p>
            <button class="ai-go-settings" @click="emit('open-settings', 'ai')">{{ t('aiGoSettings') }}</button>
          </div>

          <template v-else>
            <!-- v-memo：历史消息在流式期间内容不变，命中缓存即可整棵子树跳过重建。
                 依赖数组必须覆盖子树内所有响应式来源，缺一个就会出现「数据变了但界面不变」：
                   · m.content / m.reasoning_content —— 正文与思考过程
                   · statMap.get(m.id)               —— 该条回复的耗时/token 统计
                   · locale                         —— 按钮 title 等文案随语言切换刷新
                 编译器生成的代码含 `_cached.key === m.id` 校验，
                 切换会话（消息 id 变化）不会误命中旧缓存。 -->
            <div
              v-for="m in messages"
              :key="m.id"
              v-memo="[m.content, m.reasoning_content, statMap.get(m.id), locale, (m.imgList?.length ?? 0), m.tool_calls, m.tool_rounds]"
              :class="['ai-msg', m.role]"
            >
              <div class="ai-msg-role">
                {{ m.role === 'user' ? t('aiYou') : 'AI' }}
                <!-- 多档案（不同模型/档案）混着聊时，不写模型名就分不清这句是谁答的 -->
                <span v-if="m.role === 'assistant' && m.model" class="ai-msg-model">{{ m.model }}</span>
              </div>
              <!-- 图片缩略图：url 类直发链接，file 类用已加载的 dataUrl；历史 file 图由 loadMessages 懒加载填回 -->
              <div v-if="m.imgList && m.imgList.length" class="ai-msg-imgs">
                <div v-for="(img, bi) in m.imgList" :key="bi" class="ai-msg-img">
                  <img v-if="img.url" :src="img.url" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="openImgViewer(img)" />
                  <img v-else-if="img.dataUrl" :src="img.dataUrl" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="openImgViewer(img)" />
                  <div v-else class="ai-msg-img-el ai-img-broken">{{ t('aiImageBroken') }}</div>
                </div>
              </div>
              <details v-if="m.reasoning_content" class="ai-reasoning">
                <summary>{{ t('aiThought') }} · {{ t('aiThoughtChars', { n: m.reasoning_content.length }) }}</summary>
                <div class="ai-reasoning-content">{{ m.reasoning_content }}</div>
              </details>
              <!-- 工具调用卡片：放在正文之前，因为调用发生在作答之前。
                   默认折叠——一段回答调了五个工具时，全展开会把正文挤出屏幕；
                   摘要行自己就说明了「调了几个、几轮」。 -->
              <details v-if="m.role === 'assistant' && toolList(m).length" class="ai-tools">
                <summary class="ai-tools-sum">
                  <Wrench :size="12" />
                  <span>{{ t('aiToolSummary', { n: toolList(m).length, rounds: m.tool_rounds || countToolRounds(toolList(m)) }) }}</span>
                </summary>
                <div class="ai-tools-body">
                  <!-- 每条调用自带折叠：一次回答调了五六个工具时，用户只想展开
                       其中一条看细节，而不是要么全看要么全不看。 -->
                  <details v-for="tc in toolList(m)" :key="tc.id" class="ai-tool" :class="{ 'is-err': !!tc.error }">
                    <summary class="ai-tool-line">
                      <component :is="toolIcon(tc.name)" :size="12" class="ai-tool-icon" />
                      <span class="ai-tool-verb">{{ toolVerb(tc.name) }}</span>
                      <span v-if="toolTarget(tc)" class="ai-tool-target" :title="toolTarget(tc)">{{ toolTarget(tc) }}</span>
                      <!-- 增删行数放摘要行：改文件这一件事上，「改了几行」比「成功了」
                           更值得先看到。 -->
                      <span v-if="tc.added || tc.removed" class="ai-tool-diff">
                        <em v-if="tc.added" class="ai-diff-add">+{{ tc.added }}</em>
                        <em v-if="tc.removed" class="ai-diff-del">-{{ tc.removed }}</em>
                      </span>
                      <span v-if="tc.denied" class="ai-tool-ms is-denied">{{ t('aiToolDenied') }}</span>
                      <span v-else-if="tc.dedup" class="ai-tool-ms">{{ t('aiToolDedup') }}</span>
                      <span v-else-if="tc.ms" class="ai-tool-ms">{{ tc.ms }}ms</span>
                      <!-- 回滚到这一步之前：只有真的建过快照的写类调用才有这个按钮。 -->
                      <button v-if="cpForCall(tc)" class="ai-tool-undo" :title="t('aiCheckpointUndo')" @click.stop.prevent="restoreCheckpoint(cpForCall(tc)!)">
                        <Undo2 :size="11" />
                      </button>
                    </summary>
                    <!-- 行级 diff：结果串只说「改成功了」，这里说「改成了什么」。 -->
                    <AiDiffView v-if="tc.diff" :diff="tc.diff" />
                    <div v-if="tc.error" class="ai-tool-err">{{ tc.error }}</div>
                    <!-- 有 diff 时不再重放一句「已编辑 xxx（替换 1 处）」：
                         路径在摘要行上已经有了，改动在 diff 里已经有了。 -->
                    <pre v-else-if="tc.result && !tc.diff" class="ai-tool-res" :class="{ 'is-code': !!toolResultLang(tc) }">{{ tc.result }}</pre>
                  </details>
                </div>
              </details>
              <!-- 编排 run 树：这条回复派出去的子 agent。只有带 run_id 的回复才渲染，
                   否则每一句普通回答下面都会多一个空面板。展开时才拉数据。 -->
              <AgentRunTree v-if="runIDOf(m)" :run-id="runIDOf(m)" />
              <!-- 本次生效的规则文件：一行小字，悬停看文件名与字节数。
                   没有这一行的话，「规则改了不生效」在界面上没有任何线索——
                   写错位置、被 8KB 截断、cwd 不是想的那一层，表现完全一样。 -->
              <div v-if="rulesOf(m).length" class="ai-rules" :title="rulesTitle(rulesOf(m))">
                <FileText :size="11" />
                <span>{{ t('aiRulesActive', { n: rulesOf(m).length }) }}</span>
                <span class="ai-rules-name">{{ rulesOf(m).map((r) => r.name).join('、') }}</span>
              </div>
              <div v-if="m.role === 'assistant'" class="ai-msg-content ai-md">
                <div v-for="(b, bi) in renderMessageBlocks(m)" :key="bi" class="ai-md-block" v-html="b"></div>
              </div>
              <div v-else class="ai-msg-content ai-plain">{{ m.content }}</div>
              <!-- 操作按钮同一行：.ai-msg 是纵向 flex，不包一层的话每个按钮都会各占一行 -->
              <div class="ai-msg-actions">
                <!-- 中断标记：内容照样保留（后端也把半截内容连同 stopped 一起落库），
                     但要让人一眼看出「这是我按了停止」，而不是模型只答了这么多 -->
                <span v-if="m.stopped" class="ai-msg-stopped" :title="t('aiStoppedHint')">{{ t('aiStopped') }}</span>
                <button v-if="m.content" class="ai-copy" @click="copy(m.content)" :title="t('copy')">
                  <Copy :size="12" /> {{ t('copy') }}
                </button>
                <button v-if="m.role === 'assistant' && m.content" class="ai-copy" @click="saveAsNote(m.content)" :title="t('aiSaveToNote')">
                  <StickyNote :size="12" /> {{ t('aiSaveToNote') }}
                </button>
                <!-- 单条回复统计：耗时 / 首字延迟 / token 用量 -->
                <span v-if="msgStat(m)" class="ai-msg-stat">
                  {{ formatStat(msgStat(m)!) }}
                </span>
              </div>
            </div>

            <div v-if="reasoningText" class="ai-reasoning">
              <details open>
                <summary>{{ t('aiThinking') }}</summary>
                <div class="ai-reasoning-content">{{ reasoningText }}</div>
              </details>
            </div>
            <!-- 流式区：模型先调工具时还没有正文，这里的 v-if 必须带上 streamTools，
                 否则「正在查环境」的那几秒界面上什么都没有，看起来像卡死。 -->
            <div v-if="streamingText || streamTools.length" class="ai-msg assistant streaming">
              <div class="ai-msg-role">AI</div>
              <!-- 与历史卡片同款提示：流式期就能看到「这次带上了哪几个规则文件」，
                   不然要等回答完才知道，而那时已经照错的约定做完了。 -->
              <div v-if="runRules.length" class="ai-rules" :title="rulesTitle(runRules)">
                <FileText :size="11" />
                <span>{{ t('aiRulesActive', { n: runRules.length }) }}</span>
                <span class="ai-rules-name">{{ runRules.map((r) => r.name).join('、') }}</span>
              </div>
              <!-- 流式期默认展开：用户需要看到「正在用哪个工具」，这是等待期间唯一的进度信息 -->
              <details v-if="streamTools.length" class="ai-tools" open>
                <summary class="ai-tools-sum">
                  <Wrench :size="12" />
                  <span>{{ t('aiToolSummary', { n: streamTools.length, rounds: countToolRounds(streamTools) }) }}</span>
                </summary>
                <div class="ai-tools-body">
                  <!-- 正在跑的那条自动展开：等待期间它是唯一的进度信息。 -->
                  <details v-for="tc in streamTools" :key="tc.id" class="ai-tool" :class="{ 'is-err': !!tc.error, 'is-run': tc.running, 'is-ask': tc.approval === 'pending' }" :open="!!tc.running || tc.approval === 'pending'">
                    <summary class="ai-tool-line">
                      <component :is="toolIcon(tc.name)" :size="12" class="ai-tool-icon" />
                      <span class="ai-tool-verb">{{ toolVerb(tc.name) }}</span>
                      <span v-if="toolTarget(tc)" class="ai-tool-target" :title="toolTarget(tc)">{{ toolTarget(tc) }}</span>
                      <span v-if="tc.added || tc.removed" class="ai-tool-diff">
                        <em v-if="tc.added" class="ai-diff-add">+{{ tc.added }}</em>
                        <em v-if="tc.removed" class="ai-diff-del">-{{ tc.removed }}</em>
                      </span>
                      <span v-if="tc.approval === 'pending'" class="ai-tool-ms is-ask">{{ t('aiApprovalWaiting') }}</span>
                      <span v-else-if="tc.approval === 'denied'" class="ai-tool-ms is-denied">{{ t('aiToolDenied') }}</span>
                      <span v-else-if="tc.running" class="ai-tool-ms">{{ t('aiToolRunning') }}</span>
                      <span v-else-if="tc.dedup" class="ai-tool-ms">{{ t('aiToolDedup') }}</span>
                      <span v-else-if="tc.ms" class="ai-tool-ms">{{ tc.ms }}ms</span>
                      <!-- 流式期也能回滚：改错了最好在那一步刚结束时退回去，而不是等整轮跑完。 -->
                      <button v-if="cpForCall(tc)" class="ai-tool-undo" :title="t('aiCheckpointUndo')" @click.stop.prevent="restoreCheckpoint(cpForCall(tc)!)">
                        <Undo2 :size="11" />
                      </button>
                    </summary>
                    <!-- 审批卡：写类与高危工具在这里停下来等人点（见后端 ai_approval.go）。
                         风险说明必须排在按钮之前——先让人看见「为什么问你」，再决定点哪个；
                         反过来等于让人盲点。高危类不给「本次会话都允许」，
                         那是后端 rememberable 决定的，前端只管不渲染。 -->
                    <div v-if="tc.approval === 'pending'" class="ai-approval">
                      <div v-if="tc.riskNote" class="ai-approval-risk">{{ tc.riskNote }}</div>
                      <div class="ai-approval-btns">
                        <button class="ai-approval-btn is-allow" @click.stop="approveTool(tc, 'allow')">{{ t('aiApprovalAllow') }}</button>
                        <button v-if="tc.rememberable" class="ai-approval-btn is-allow-all" @click.stop="approveTool(tc, 'allow', true)">{{ tc.grantScope ? t('aiApprovalRememberScope', { scope: tc.grantScope }) : t('aiApprovalRemember') }}</button>
                        <button class="ai-approval-btn is-deny" @click.stop="approveTool(tc, 'deny')">{{ t('aiApprovalDeny') }}</button>
                      </div>
                    </div>
                    <div v-else-if="tc.approval === 'allowed'" class="ai-approval-done">{{ t('aiApprovalAllowed') }}</div>
                    <AiDiffView v-if="tc.diff" :diff="tc.diff" />
                    <div v-if="tc.error" class="ai-tool-err">{{ tc.error }}</div>
                    <pre v-else-if="tc.result && !tc.diff" class="ai-tool-res" :class="{ 'is-code': !!toolResultLang(tc) }">{{ tc.result }}</pre>
                  </details>
                </div>
              </details>
              <div v-if="streamingText" class="ai-msg-content ai-md">
                <!-- key 用「块原文 + 序号」：原文可能重复（两段相同的话），
                     仅用原文会触发 Vue 的重复 key 警告。仅末尾块会变，
                     前面的块 key 稳定 → 不会被重建。 -->
                <div v-for="(b, bi) in streamBlocks" :key="bi + ':' + b.raw.length + ':' + b.raw.slice(0, 24)" class="ai-md-block" v-html="b.html"></div>
              </div>
              <span v-if="streamingText" class="ai-cursor">▋</span>
            </div>

            <div v-if="errorMsg" class="ai-error">
              <span class="ai-error-text">{{ errorMsg }}</span>
              <!-- 失败时用户消息已落库，重试就是把它再发一次（图片一起带回去） -->
              <button v-if="canRetry()" class="ai-retry" @click="retryLast">{{ t('aiRetry') }}</button>
            </div>

            <div v-if="messages.length === 0 && !streamingText && !errorMsg" class="ai-empty">
              <Bot :size="40" />
              <p>{{ t('aiEmptyHint') }}</p>
            </div>
          </template>
        </div>
      </div>

      <!-- 「回到底部」按钮：用户主动上滑查看历史时出现 -->
      <button v-if="showJumpBtn" class="ai-jump-bottom" @click="jumpToBottom" :title="t('aiBackToBottom')">
        <ArrowDown :size="14" />
      </button>

      <!-- 划词复制浮层（Teleport 到 body，floating-ui 负责定位与防溢出） -->
      <Teleport to="body">
        <button
          v-if="selCtx.visible.value"
          class="ai-sel-copy"
          :ref="el => (selCtx.floatingRef.value = el as HTMLElement | null)"
          :style="selCtx.floatingStyles.value"
          @click="copySelection"
          @mousedown.stop
        >
          <Copy :size="12" /> {{ t('copySelection') }}
        </button>
      </Teleport>

      <!-- 流程图 / 公式的右键菜单：原生菜单只能复制成文本，这里要的是「一张图」 -->
      <Teleport to="body">
        <div
          v-if="ctxMenu.visible.value"
          class="ai-ctx"
          :ref="el => (ctxMenu.floatingRef.value = el as HTMLElement | null)"
          :style="ctxMenu.floatingStyles.value"
          @mousedown.stop
          @contextmenu.prevent
        >
          <button class="ai-ctx-item" @click="ctxCopyImage">
            <ImageIcon :size="13" /> {{ t('aiCopyImage') }}
          </button>
          <button v-if="ctxTarget?.kind === 'mermaid' || ctxTarget?.kind === 'math'" class="ai-ctx-item" @click="ctxCopySource">
            <Copy :size="13" /> {{ t('aiCopySource') }}
          </button>
          <template v-if="ctxTarget?.kind === 'mermaid'">
            <button class="ai-ctx-item" @click="ctxDownloadPng">
              <Download :size="13" /> {{ t('aiDownloadPng') }}
            </button>
            <button class="ai-ctx-item" @click="ctxDownloadSvg">
              <Download :size="13" /> {{ t('aiDownloadSvg') }}
            </button>
          </template>
        </div>
      </Teleport>

      <!-- html 代码块预览：sandbox 无 allow-same-origin，脚本跑在自己的不透明源里 -->
      <Teleport to="body">
        <div v-if="htmlPreview" class="ai-html-preview" @click.self="closeHtmlPreview">
          <div class="ai-html-preview-box">
            <div class="ai-html-preview-bar">
              <span class="ai-html-preview-title">{{ t('aiPreviewTitle') }}</span>
              <button class="qd-btn qd-btn-sm" @click="copy(htmlPreview)">
                <Copy :size="12" /> {{ t('copyCode') }}
              </button>
              <button class="qd-icon-btn" :title="t('close')" @click="closeHtmlPreview">
                <X :size="14" />
              </button>
            </div>
            <iframe
              class="ai-html-preview-frame"
              :srcdoc="htmlPreview"
              sandbox="allow-scripts allow-modals allow-forms allow-popups"
              referrerpolicy="no-referrer"
              title="preview"
            ></iframe>
          </div>
        </div>
      </Teleport>

      <!-- 输入区 -->
      <div class="ai-input-area" @dragover.prevent @drop="onInputDrop">
        <!-- 待发送图片预览 -->
        <div v-if="attachments.length" class="ai-attach-preview">
          <div v-for="(a, i) in attachments" :key="i" class="ai-attach-thumb">
            <img v-if="a.kind === 'file' && a.dataUrl" :src="a.dataUrl" class="ai-attach-img" />
            <img v-else-if="a.kind === 'url'" :src="a.url" class="ai-attach-img" />
            <button class="ai-attach-del" :title="t('delete')" @click="removeAttachment(i)">×</button>
          </div>
        </div>
        <!-- 图片链接输入框（点击「图片链接」后展开） -->
        <div v-if="urlInputOpen" class="ai-url-row">
          <input
            v-model="urlDraft"
            type="text"
            class="ai-url-input"
            :placeholder="t('aiAddImageUrlPh')"
            @keydown.enter.prevent="confirmUrlImage"
          />
          <button class="ai-url-ok" @click="confirmUrlImage">{{ t('add') }}</button>
        </div>
        <!-- @ 引用（B2）：已选 chip 与补全弹层。
             弹层用 absolute 挂在输入区上方——输入区贴着窗口底部，往下弹会被切掉。
             @ 只在主输入框生效：落地页那个 hero 输入框是同一份 v-model，但弹出的
             弹层是相对输入区定位的，在 hero 里打 @ 只会留下纯文本（后端 mentions 为空
             时不会展开任何东西），不会出现「弹层在屏幕另一头」这种怪状。 -->
        <div v-if="mentions.length" class="ai-mention-chips">
          <span
            v-for="(m, i) in mentions"
            :key="m.kind + (m.path || m.convId || String(i))"
            class="ai-mention-chip"
            :title="m.path || m.name"
          >
            <component :is="m.kind === 'conv' ? MessageSquare : m.kind === 'dir' ? Folder : FileText" :size="11" />
            <span class="ai-mention-chip-name">{{ m.kind === 'conv' ? m.name : (m.path || m.name) }}</span>
            <button class="ai-mention-chip-del" :title="t('delete')" @click="removeMention(i)">×</button>
          </span>
        </div>
        <div v-if="mentionOpen" class="ai-mention-pop">
          <div
            v-for="(it, i) in mentionItems"
            :key="it.kind + (it.path || it.convId || String(i))"
            class="ai-mention-item"
            :class="{ 'is-active': i === mentionIdx }"
            @mousedown.prevent="pickMention(it)"
          >
            <component :is="it.kind === 'conv' ? MessageSquare : it.kind === 'dir' ? Folder : FileText" :size="12" />
            <span class="ai-mention-name">{{ it.name }}</span>
            <span v-if="it.path" class="ai-mention-path">{{ it.path }}</span>
            <span v-else-if="it.kind === 'conv'" class="ai-mention-path">{{ t('aiMentionConv') }}</span>
          </div>
        </div>
        <div class="ai-input-row">
          <textarea
            v-model="input"
            class="ai-input"
            :placeholder="t('aiPlaceholder')"
            :disabled="!hasKey || streaming"
            @keydown="onComposerKey($event, send)"
            @paste="onInputPaste"
          />
          <div class="ai-input-tools">
            <button v-if="allowFileImage" class="ai-tool-btn" :title="t('aiAttachImage')" @click="openFilePicker">
              <ImageIcon :size="16" />
            </button>
            <button v-if="allowUrlImage" class="ai-tool-btn" :title="t('aiAddImageUrl')" @click="toggleUrlInput">
              <Link2 :size="16" />
            </button>
            <button class="ai-send" :disabled="!hasKey || streaming || (!input.trim() && !attachments.length)" @click="send">
              {{ streaming ? t('aiThinking') : t('send') }}
            </button>
          </div>
        </div>
      </div>
      </template>
    </section>
    <!-- 隐藏的文件选择器：放在「落地页 / 聊天区」两个分支之外。
         两处都有「添加图片」按钮，而它原先只写在聊天区里 —— 落地页那个按钮点了
         什么也不会发生（fileInputEl 为 null），用户只能靠拖拽或粘贴。 -->
    <input ref="fileInputEl" type="file" accept="image/*" multiple hidden @change="onPickFiles" />
    <!-- 图片全屏查看器：点遮罩关闭，点图不关（防误触），Esc 同样关闭 -->
    <Teleport to="body">
      <div v-if="imgViewer" class="ai-img-viewer" @click="closeImgViewer">
        <img :src="imgViewer.dataUrl || imgViewer.url || ''" :alt="imgViewer.name" @click.stop />
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.ai-page {
  flex: 1;
  display: flex;
  min-width: 0;
  height: 100%;
  overflow: hidden;
}

/* 会话列表 */
.ai-sidebar {
  width: 220px;
  min-width: 220px;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
  border-right: 1px solid var(--color-border);
  overflow: hidden;
}
.ai-new-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  margin: 12px;
  padding: 8px;
  border: 1px dashed var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  border-radius: var(--radius-md);
  cursor: pointer;
  font-family: inherit;
  font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-new-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-conv-list { flex: 1; overflow-y: auto; padding: 0 8px 12px; }

/* 分组标题：任务 / 空间。两组用同一套样式，视觉上是对等的两个区域，
   而不是「任务」为主、「空间」为附属——空间才是带目录的那个。 */
.ai-group-head {
  padding: 12px 10px 6px;
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-disabled);
  letter-spacing: 0.4px;
}
.ai-group-head-space { margin-top: 4px; border-top: 1px solid var(--color-border); padding-top: 12px; }
/* 组标题可点（收起整组）：条目一多时，先收起一整组比在里面翻找更快。
   箭头与空间行同一套语义——展开是向下箭头。 */
.ai-group-head-btn {
  display: flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
  user-select: none;
  transition: color var(--transition-fast);
}
.ai-group-head-btn:hover { color: var(--color-text-secondary); }
.ai-group-caret { flex-shrink: 0; opacity: 0.75; }

/* 类型图标：不标出来，侧栏一长串标题里看不出哪条是翻译、哪条是闲聊 */
.ai-conv-mode { flex-shrink: 0; opacity: 0.7; }
.ai-conv-item.active .ai-conv-mode { opacity: 1; }

.ai-space { margin-bottom: 2px; }
.ai-space-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 7px 10px 7px 4px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: 13px;
  transition: background var(--transition-fast);
}
.ai-space-row:hover { background: var(--color-bg-hover); }
/* 空间行复用了任务行的改名/删除按钮，但「hover 才显示」那条规则是按 .ai-conv-item 写的
   —— 空间行不是 .ai-conv-item，于是这两个按钮一直是 display:none：
   用户根本删不掉、也改不了空间名。这里补上同一条显示规则。 */
.ai-space-row .ai-conv-ren,
.ai-space-row .ai-conv-del,
.ai-space-row .ai-conv-add { flex-shrink: 0; }
.ai-space-row:hover .ai-conv-ren,
.ai-space-row:hover .ai-conv-del,
.ai-space-row:hover .ai-conv-add { display: flex; }
.ai-space-caret { flex-shrink: 0; opacity: 0.6; }
.ai-space-icon { flex-shrink: 0; color: var(--color-accent); opacity: 0.85; }
.ai-space-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 任务数常驻可见（不是 hover 才出现）：空间收起时它是唯一能说明
   「这里面有多少东西」的信息，藏起来等于逼用户一个个展开。 */
.ai-space-count {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--color-text-disabled);
  background: var(--color-bg-tertiary);
  border-radius: 8px;
  padding: 0 6px;
}
.ai-conv-item.is-in-space { margin-left: 18px; padding: 6px 10px; font-size: 12.5px; }
.ai-space-empty { padding: 4px 10px 8px 28px; font-size: 11px; color: var(--color-text-disabled); }

/* 新建任务的落地页（空态）。
   参考主流 AI 产品的首屏：标题 + 类型 pill + 一个大输入框，输入框下面一行放
   归属空间 / 模型 / 发送。它替代了原来的「新建任务」弹窗——弹窗要求先答完两道题
   才出现输入框，而用户往往只是想先打一句话。 */
.ai-hero {
  flex: 1;
  overflow-y: auto;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 16px 40px;
}
.ai-hero-inner {
  width: 100%;
  max-width: 660px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}
.ai-hero-title {
  margin: 0 0 2px;
  font-size: 26px;
  font-weight: 700;
  letter-spacing: -0.2px;
  color: var(--color-text-primary);
}
.ai-hero-modes { display: flex; flex-wrap: wrap; justify-content: center; gap: 6px; }
.ai-hero-mode {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 5px 12px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px; font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast);
}
.ai-hero-mode:hover { color: var(--color-text-primary); border-color: var(--color-text-muted); }
.ai-hero-mode.active { color: var(--color-accent); border-color: var(--color-accent); background: var(--color-accent-bg); }

/* 输入框主体：一整块卡片，textarea 自身无边框，工具行贴在卡片底部 */
.ai-hero-box {
  width: 100%;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-bg-tertiary);
  padding: 10px 12px 8px;
  transition: border-color var(--transition-fast);
}
.ai-hero-box:focus-within { border-color: var(--color-accent); }
.ai-hero-input {
  width: 100%;
  min-height: 64px;
  max-height: 240px;
  resize: none;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: 14px;
  line-height: 1.6;
}
.ai-hero-input::placeholder { color: var(--color-text-disabled); }
.ai-hero-input:disabled { opacity: 0.6; cursor: not-allowed; }
.ai-hero-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: 6px;
}
.ai-hero-bar-right { display: flex; align-items: center; gap: 6px; margin-left: auto; }
/* 归属空间与模型下拉同一套外观：这两个是「本次任务落在哪」的 chip，不是表单字段 */
.ai-hero-select {
  display: inline-flex; align-items: center; gap: 5px;
  max-width: 180px;
  padding: 4px 8px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-muted);
  font-size: 12px; font-family: inherit;
  cursor: pointer;
  text-overflow: ellipsis;
}
.ai-hero-select:hover { color: var(--color-text-primary); border-color: var(--color-border); }
.ai-hero-select:focus-visible { outline: none; border-color: var(--color-accent); }
/* select 不参与 flex 排版（原生下拉对 display:inline-flex 支持不一），
   这里退回 inline-block 并给它自己的底色——透明底在深色主题下会露出系统白。 */
select.ai-hero-select {
  display: inline-block;
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
  color: var(--color-text-primary);
}
.ai-hero-dir { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ai-hero-send {
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px;
  border: none; border-radius: 50%;
  background: var(--color-accent); color: #fff;
  cursor: pointer;
  transition: opacity var(--transition-fast), background-color var(--transition-fast);
}
.ai-hero-send:disabled { background: var(--color-bg-active); color: var(--color-text-disabled); cursor: default; }
/* 产物去向：说清「AI 生成的文件会放到哪」，顺带解释了空间是什么 */
.ai-hero-out {
  margin: 0;
  font-size: 11px;
  color: var(--color-text-disabled);
  text-align: center;
  line-height: 1.5;
  word-break: break-all;
}
.ai-hero-needkey {
  margin: 0;
  display: flex; align-items: center; gap: 8px;
  font-size: 12px; color: var(--color-warning);
}
.ai-hero-tips { width: 100%; display: flex; flex-direction: column; gap: 8px; margin-top: 4px; }
.ai-hero-tip {
  display: inline-flex; align-items: center; gap: 6px;
  align-self: flex-start;
  max-width: 100%;
  padding: 7px 12px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12.5px; font-family: inherit; text-align: left;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}
.ai-hero-tip:hover { color: var(--color-text-primary); border-color: var(--color-accent); }
/* 示例问题后面那个小箭头：转 45 度当作 ↗（lucide 的 ArrowUp 最接近） */
.ai-hero-tip-icon { transform: rotate(45deg); opacity: 0.55; flex-shrink: 0; }

.ai-conv-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: 13px;
  transition: background var(--transition-fast);
}
.ai-conv-item:hover { background: var(--color-bg-hover); }
.ai-conv-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.ai-conv-title {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 消息命中的行比普通任务行高：标题下面还要放一行片段。
   片段限两行——侧栏是导航不是阅读器，一条工具输出能占几十行。 */
.ai-conv-item.is-msg-hit { align-items: flex-start; padding: 7px 10px; }
.ai-conv-item.is-msg-hit .ai-conv-mode { margin-top: 3px; }
.ai-msg-hit-body { flex: 1; min-width: 0; }
.ai-msg-hit-head { display: flex; align-items: center; gap: 4px; }
/* 「还有 N 条」用数字而不是一条分隔线：同一段话被追问、被复述都会重复命中，
   这个数说明「这条对话里提过好几次」，是选哪条进去的关键。 */
.ai-msg-hit-count {
  flex: none; font-size: 10px; color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
}
.ai-msg-hit-snippet {
  margin-top: 2px; font-size: 11px; line-height: 1.45; color: var(--color-text-disabled);
  overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
}
.ai-conv-item.active .ai-msg-hit-snippet { color: var(--color-text-secondary); }
.ai-conv-del {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: var(--radius-sm);
}
.ai-conv-item:hover .ai-conv-del { display: flex; }
.ai-conv-del:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.ai-conv-ren,
.ai-conv-add {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: var(--radius-sm);
}
.ai-conv-item:hover .ai-conv-ren,
.ai-conv-item:hover .ai-conv-add { display: flex; }
.ai-conv-ren:hover,
.ai-conv-add:hover { color: var(--color-text-primary); background: var(--color-bg-tertiary); }
.ai-conv-add:hover { color: var(--color-accent); }
/* 行内编辑：撑满标题那一格，且必须能盖住行本身的点击（改选中了会话就白改了） */
.ai-conv-rename {
  flex: 1;
  min-width: 0;
  padding: 0 4px;
  font: inherit;
  font-size: 13px;
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-accent);
  border-radius: var(--radius-sm);
  outline: none;
}
.ai-conv-tokens {
  font-size: 10px; color: var(--color-text-disabled); white-space: nowrap;
  margin-right: 4px; font-variant-numeric: tabular-nums;
}
.ai-conv-empty { padding: 24px 12px; text-align: center; color: var(--color-text-disabled); font-size: 12px; }

/* 主区 */
.ai-main { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; position: relative; }
.ai-toolbar {
  display: flex;
  align-items: center;
  /* 右侧操作组靠右；左侧若有目录指示，由它的 margin-right:auto 顶到左边。
     原来是 space-between + 左右两块都常驻，现在左侧那块只在绑了目录时才存在，
     用 space-between 会在没有它时把右侧整组推到最左。 */
  justify-content: flex-end;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  gap: 10px;
}
.ai-toolbar-right { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.ai-model-select {
  max-width: 160px;
  padding: 5px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 12px;
  font-family: inherit;
  outline: none;
}
.ai-model-select:focus { border-color: var(--color-accent); }
/* 工作目录：显示当前绑到哪个目录，点一下在资源管理器里打开。
   它只「打开」已绑定的目录，不是「选目录」——绑定入口已收进「新建任务 → 选空间」。
   保留它是因为它同时回答两件事：AI 能不能改文件，以及生成的文件去哪了。
   宽度上限必须有：项目路径动辄五六层，不夹住会把模型下拉挤到换行。 */
.ai-cwd-tag {
  display: inline-flex; align-items: center; gap: 5px;
  margin-right: auto;
  max-width: 40%; min-width: 0;
  padding: 4px 6px;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-disabled);
  font-size: 11.5px; font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}
.ai-cwd-tag:hover { color: var(--color-text-primary); border-color: var(--color-border); }
.ai-cwd-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ai-icon-btn {
  display: flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border: 1px solid var(--color-border);
  background: transparent; color: var(--color-text-muted);
  border-radius: var(--radius-md); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-icon-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-stop {
  display: flex; align-items: center; gap: 4px;
  padding: 5px 12px; border: none; border-radius: var(--radius-sm);
  background: var(--color-bg-active); color: var(--color-text-secondary);
  font-size: 12px; cursor: pointer; font-family: inherit;
}
.ai-stop:hover { color: var(--color-danger); }

/* 思考过程折叠块 */
.ai-reasoning {
  font-size: 12px; margin: 2px 0; max-width: 80%;
}
.ai-reasoning details { background: var(--color-bg-active); border-radius: var(--radius-sm); padding: 4px 8px; }
.ai-reasoning summary {
  cursor: pointer; color: var(--color-text-muted); font-size: 11px;
  user-select: none; outline: none;
}
.ai-reasoning summary:hover { color: var(--color-text-primary); }
.ai-reasoning-content {
  margin-top: 4px; padding-top: 4px; border-top: 1px solid var(--color-border);
  color: var(--color-text-secondary); font-size: 12px; line-height: 1.5;
  white-space: pre-wrap; word-break: break-word;
}

/* 工具调用卡片。
   折叠时只剩一行摘要（调了几个、几轮），展开才看参数与结果——
   一段回答连调五个工具时，全展开会把正文挤出屏幕外。 */
.ai-tools { margin: 2px 0; max-width: 80%; }
.ai-tools > summary {
  display: flex; align-items: center; gap: 5px;
  cursor: pointer; user-select: none; outline: none;
  color: var(--color-text-muted); font-size: 11px;
  background: var(--color-bg-active); border-radius: var(--radius-sm);
  padding: 4px 8px; list-style: none;
}
/* 去掉 Chrome 默认的三角形：上面已经用图标表示「可展开」 */
.ai-tools > summary::-webkit-details-marker { display: none; }
.ai-tools > summary:hover { color: var(--color-text-primary); }
.ai-tools[open] > summary { border-radius: var(--radius-sm) var(--radius-sm) 0 0; }
.ai-tools-body {
  border: 1px solid var(--color-border); border-top: none;
  border-radius: 0 0 var(--radius-sm) var(--radius-sm);
  padding: 4px; display: flex; flex-direction: column; gap: 4px;
}
.ai-tool { background: var(--color-bg-active); border-radius: 4px; padding: 2px 6px; }
/* 失败用左侧红条而不是整块变红：结果区本来就长，整块染色太吵 */
.ai-tool.is-err { border-left: 2px solid var(--color-danger); }
.ai-tool.is-run { opacity: 0.85; }
/* 每条调用自己可折叠：summary 就是那一行，不带默认三角形（图标已表示「可展开」） */
.ai-tool > summary {
  display: flex; align-items: center; gap: 6px;
  cursor: pointer; user-select: none; outline: none; list-style: none;
  padding: 3px 0;
}
.ai-tool > summary::-webkit-details-marker { display: none; }
.ai-tool > summary:hover .ai-tool-target { color: var(--color-text-primary); }
.ai-tool-icon { flex-shrink: 0; color: var(--color-accent); opacity: 0.8; }
.ai-tool.is-err .ai-tool-icon { color: var(--color-danger); opacity: 1; }
.ai-tool-verb {
  font-size: 11.5px; color: var(--color-text-primary); flex-shrink: 0;
}
.ai-tool-target {
  font-size: 11px; color: var(--color-text-secondary); font-family: var(--font-mono);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 320px;
}
/* 增删行数：改文件这一件事上，行数比「成功」更值得先看到 */
.ai-tool-diff { display: flex; gap: 4px; flex-shrink: 0; font-family: var(--font-mono); font-size: 11px; }
.ai-diff-add { color: var(--color-success, #3fb950); font-style: normal; }
.ai-diff-del { color: var(--color-danger); font-style: normal; }
.ai-tool-ms { font-size: 11px; color: var(--color-text-muted); margin-left: auto; flex-shrink: 0; }
/* 等待审批：琥珀色左侧条，与「失败红」「执行中灰」区分开——它既不是错误也不是
   正常执行，是「卡在这儿等人」。 */
.ai-tool.is-ask { border-left: 2px solid var(--color-warning); }
.ai-tool-ms.is-ask { color: var(--color-warning); }
.ai-tool-ms.is-denied { color: var(--color-text-muted); }
/* 审批卡：风险说明在上、按钮在下。说明必须排在按钮前——先让人看见「为什么问你」
   再决定点哪个；反过来等于让人盲点。 */
.ai-approval { margin: 4px 0 2px; display: flex; flex-direction: column; gap: 6px; }
.ai-approval-risk {
  font-size: 11px; color: var(--color-text-secondary); line-height: 1.5;
  background: var(--color-bg-primary); border-radius: 4px; padding: 5px 7px;
  border-left: 2px solid var(--color-warning);
}
.ai-approval-btns { display: flex; gap: 6px; flex-wrap: wrap; }
.ai-approval-btn {
  font-size: 11px; padding: 3px 10px; border-radius: 4px; cursor: pointer;
  border: 1px solid var(--color-border); background: var(--color-bg-primary);
  color: var(--color-text-primary);
}
.ai-approval-btn:hover { background: var(--color-bg-hover); }
/* 允许是主操作，给强调色；拒绝刻意不做成实心红——把「拒绝」做成醒目的红按钮，
   人会下意识躲开它，而这恰恰是用户最该敢按的那一个。 */
.ai-approval-btn.is-allow {
  background: var(--color-accent); border-color: var(--color-accent);
  color: var(--color-accent-text);
}
.ai-approval-btn.is-deny { color: var(--color-danger); border-color: var(--color-danger); }
.ai-approval-done { font-size: 11px; color: var(--color-text-muted); margin-top: 3px; }
/* 回滚按钮：放在摘要行最右侧，平时只显示图标——它是个「事后」操作，
   不该比「改了几行」更抢眼。 */
.ai-tool-undo {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 2px 4px; margin-left: 2px; flex-shrink: 0;
  border: none; background: transparent; color: var(--color-text-muted);
  cursor: pointer; border-radius: 3px;
}
.ai-tool-undo:hover { color: var(--color-accent); background: var(--color-bg-hover); }
/* 生效的规则文件：一行小字，和工具卡片同一量级——它是「解释这条回答为什么这样写」
   的背景信息，不该比正文更显眼。文件名超长时省略，全文在悬停提示里。 */
.ai-rules {
  display: flex; align-items: center; gap: 4px;
  margin: 2px 0 4px; font-size: 11px; color: var(--color-text-muted);
}
.ai-rules-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}
.ai-tool-err {
  font-size: 11px; color: var(--color-danger); margin-top: 3px;
  word-break: break-word;
}
/* 结果区限高：file_grep 这类工具一屏放不下，内部滚动比把页面撑长好 */
.ai-tool-res {
  margin: 4px 0 2px; font-size: 11px; color: var(--color-text-secondary);
  font-family: var(--font-mono); line-height: 1.45;
  max-height: 180px; overflow: auto;
  white-space: pre-wrap; word-break: break-word;
}
/* 命令输出 / diff：缩进与对齐是有意义的，按等宽块原样保留 */
.ai-tool-res.is-code {
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: 4px;
  padding: 6px 8px;
}

/* 消息区：滚动容器 + 内容层（内容层被 ResizeObserver 观察） */
.ai-messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  overflow-anchor: none; /* 关掉浏览器自动锚定，避免与我们的贴底逻辑打架 */
}
/* 内容层的高度必须跟随内容增长，ResizeObserver 才能感知到「AI 又吐了一段」。
   原来这里写的是 flex:1 + min-height:0 —— flex-basis:0% 让盒高被 flex 固定成
   容器高度，内容溢出并不会改变 border-box 尺寸，观察器因此永远不触发，
   这就是「AI 一直在回复但视口不往下走」的直接原因。
   改为 flex:0 0 auto（高度由内容决定）+ min-height:100%（内容少时仍撑满，
   空状态才能垂直居中）。 */
.ai-msg-content-wrap {
  display: flex;
  flex-direction: column;
  gap: 14px;
  flex: 0 0 auto;
  min-height: 100%;
}
.ai-msg { display: flex; flex-direction: column; gap: 4px; max-width: 88%; }
.ai-msg.user { align-self: flex-end; align-items: flex-end; }
.ai-msg.assistant { align-self: flex-start; }
.ai-msg-role { font-size: 11px; color: var(--color-text-disabled); margin-bottom: 1px; display: flex; align-items: center; gap: 6px; }
/* 模型名淡到几乎只作注脚，但比"猜这句是谁答的"强 */
.ai-msg-model {
  font-size: 10px; color: var(--color-text-disabled);
  background: var(--color-bg-tertiary); padding: 0 5px;
  border-radius: var(--radius-xs); opacity: 0.85;
}

/* 会话搜索：与新建按钮同宽，同一视觉层级 */
.ai-conv-search {
  display: flex; align-items: center; gap: 5px;
  margin: 6px 8px 2px; padding: 0 7px; height: 26px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); flex-shrink: 0;
}
.ai-conv-search:focus-within { border-color: var(--color-accent); }
.ai-conv-search-icon { color: var(--color-text-disabled); flex-shrink: 0; }
.ai-conv-search-input {
  flex: 1; min-width: 0; border: none; outline: none; background: none;
  color: var(--color-text-primary); font-size: 11px; font-family: inherit;
}
.ai-conv-search-input::placeholder { color: var(--color-text-disabled); }
.ai-conv-search-clear {
  display: flex; padding: 0; border: none; background: none;
  color: var(--color-text-disabled); cursor: pointer;
}
.ai-conv-search-clear:hover { color: var(--color-text-primary); }
/* 加载更多：整行按钮，不要做成小链接——点中的概率太低 */
.ai-conv-more {
  margin: 2px 8px 8px; padding: 5px 0; flex-shrink: 0;
  border: 1px dashed var(--color-border); border-radius: var(--radius-sm);
  background: none; color: var(--color-text-muted); font-size: 11px;
  font-family: inherit; cursor: pointer;
}
.ai-conv-more:hover { color: var(--color-text-primary); border-color: var(--color-accent); }
/* 空间内的「更多」跟着任务缩进，否则会和空间名对齐，看着像另一个空间 */
.ai-conv-more.is-in-space { margin-left: 26px; margin-right: 0; }

/* AI 气泡去掉底色：原来整块套 --color-bg-tertiary 灰底，
   里面再套深色代码块、外面再套引用块，三层嵌套导致视觉很乱。
   现在 AI 回复走「纯净排版」，只有代码块/引用块自身带底色，靠留白分层。 */
.ai-msg-content {
  padding: 0;
  font-size: 13px;
  line-height: 1.65;
  word-break: break-word;
  color: var(--color-text-primary);
  user-select: text;
  -webkit-user-select: text;
  cursor: text;
  min-width: 0;
}
/* 用户消息仍保留气泡，但改为描边风格，与 AI 的无底色形成层级区分 */
.ai-msg.user .ai-msg-content {
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  color: var(--color-text-primary);
  padding: 8px 12px;
  border-radius: var(--radius-md);
  white-space: pre-wrap;
}
.ai-msg-actions {
  display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap;
}
.ai-msg.user .ai-msg-actions { justify-content: flex-end; }
.ai-copy {
  display: flex; align-items: center; gap: 3px;
  background: none; border: none; color: var(--color-text-disabled);
  font-size: 11px; cursor: pointer; padding: 2px 4px; border-radius: var(--radius-xs);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-copy:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.ai-msg.user .ai-copy { color: var(--color-text-muted); }
.ai-msg.user .ai-copy:hover { color: var(--color-accent); background: var(--color-accent-bg); }
/* 中断标记：贴在操作行最左侧，用警示色区别于普通小字（stat 行） */
.ai-msg-stopped {
  font-size: 10px; padding: 1px 6px; border-radius: var(--radius-xs);
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border: 1px solid var(--color-warning-border);
  cursor: default;
}
/* 单条回复统计：淡色小字，鼠标悬停可看完整信息 */
.ai-msg-stat {
  font-size: 10px;
  color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
  /* 允许换行：这行可能同时有「耗时 / 首字 / 输入输出 / 缓存命中」四段，
     窄窗口下 nowrap 会把后面的内容顶出可视区（数字被裁比换行更难接受） */
  white-space: normal;
  line-height: 1.6;
  cursor: default;
}

/* 划词复制浮层（position/left/top 由 floating-ui 注入） */
.ai-sel-copy {
  z-index: 50;
  display: flex; align-items: center; gap: 4px;
  padding: 5px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 12px; cursor: pointer; font-family: inherit;
  box-shadow: 0 4px 16px var(--color-bg-overlay);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-sel-copy:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-cursor { animation: ai-blink 1s steps(1) infinite; color: var(--color-accent); }
@keyframes ai-blink { 50% { opacity: 0; } }

/* 图 / 公式的右键菜单（position 由 floating-ui 注入） */
.ai-ctx {
  z-index: 700;
  display: flex; flex-direction: column;
  min-width: 132px; padding: 4px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  box-shadow: var(--shadow-md);
}
.ai-ctx-item {
  display: flex; align-items: center; gap: 7px;
  height: 28px; padding: 0 9px;
  border: none; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-primary);
  font-family: inherit; font-size: 12px; text-align: left; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-ctx-item:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-ctx-item svg { flex: 0 0 auto; }

/* html 代码块预览：铺满窗口的遮罩 + 白色画布（示例页大多假设白底，
   跟着应用主题变暗会让「我的网页」看起来坏了） */
.ai-html-preview {
  position: fixed; inset: 0; z-index: 800;
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
  background: var(--color-bg-overlay);
}
.ai-html-preview-box {
  display: flex; flex-direction: column;
  width: 100%; height: 100%;
  max-width: 1080px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-bg-primary);
  box-shadow: var(--shadow-md);
  overflow: hidden;
}
.ai-html-preview-bar {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 8px 6px 14px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.ai-html-preview-title {
  margin-right: auto;
  font-size: 11px; letter-spacing: 0.04em; text-transform: uppercase;
  color: var(--color-text-muted);
}
.ai-html-preview-frame {
  flex: 1; width: 100%; min-height: 0;
  border: none; background: #fff;
}
.ai-error {
  align-self: flex-start;
  padding: 10px 14px; border-radius: var(--radius-lg);
  background: var(--color-danger-bg); color: var(--color-danger);
  font-size: 13px; max-width: 80%;
  /* 错误文案 + 重试按钮同一行；长错误换行，按钮不跟着被挤压 */
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}
.ai-error-text { flex: 1 1 auto; min-width: 0; word-break: break-word; }
.ai-retry {
  flex: 0 0 auto;
  padding: 3px 12px; border-radius: var(--radius-sm);
  border: 1px solid var(--color-danger-border);
  background: transparent; color: var(--color-danger);
  font-size: 12px; font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast);
}
.ai-retry:hover { background: var(--color-danger-bg); }
.ai-need-key, .ai-empty {
  margin: auto;
  display: flex; flex-direction: column; align-items: center; gap: 12px;
  color: var(--color-text-disabled); text-align: center;
}
.ai-need-key p, .ai-empty p { font-size: 13px; margin: 0; }
.ai-go-settings {
  padding: 7px 16px; border: none; border-radius: var(--radius-md);
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 13px; cursor: pointer; font-family: inherit;
}

/* 「回到底部」浮动按钮 */
.ai-jump-bottom {
  position: absolute;
  bottom: 96px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 20;
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px;
  border-radius: var(--radius-full);
  border: 1px solid var(--color-border);
  background: var(--color-surface-elevated);
  color: var(--color-text-secondary);
  cursor: pointer;
  box-shadow: var(--shadow-md);
  transition: background-color var(--transition-fast), color var(--transition-fast),
    border-color var(--transition-fast), opacity var(--transition-fast);
}
.ai-jump-bottom:hover { color: var(--color-accent); border-color: var(--color-accent); }

/* Markdown 内容——紧凑排版 */
/* 分块渲染容器：display:contents 让块内元素不生成盒子、直接参与父级布局，
   因此「分块渲染」与「整段渲染」的排版结果完全一致（无额外间距/缩进差异）。 */
.ai-md-block { display: contents; }
.ai-md :deep(h1), .ai-md :deep(h2), .ai-md :deep(h3), .ai-md :deep(h4) {
  font-size: 14px; margin: 10px 0 4px; font-weight: 600; line-height: 1.4;
}
.ai-md :deep(h1:first-child), .ai-md :deep(h2:first-child), .ai-md :deep(h3:first-child) { margin-top: 0; }
.ai-md :deep(p) { margin: 5px 0; }
.ai-md :deep(ul), .ai-md :deep(ol) { padding-left: 20px; margin: 5px 0; }
.ai-md :deep(li) { margin: 2px 0; line-height: 1.6; }
.ai-md :deep(li > p) { margin: 0; }
/* 换行：marked 开了 breaks:true，段内单换行会渲染成 <br>。
   这里必须让 <br> 生效，否则 AI 回复里的多行要点会被挤成一行
   （实测 marked 输出 <p>第一行<br>第二行</p>，而 display:none 会把 <br> 抹掉）。 */
.ai-md :deep(br) { display: inline; }
.ai-md :deep(a) { color: var(--color-accent); text-decoration: none; }
.ai-md :deep(a:hover) { text-decoration: underline; }
/* 引用块：左侧竖线 + 淡底，不再嵌套成整块灰盒 */
.ai-md :deep(blockquote) {
  border-left: 3px solid var(--color-accent-muted);
  padding: 6px 12px;
  margin: 8px 0;
  background: var(--color-bg-secondary);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  color: var(--color-text-secondary);
}
.ai-md :deep(blockquote p) { margin: 0; }

/* 代码块：独立卡片 + 顶部工具条（语言标签 + 复制按钮） */
.ai-md :deep(.md-code) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  overflow: hidden;
  background: var(--color-code-bg);
}
.ai-md :deep(.md-code-bar) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px 4px 10px;
  background: var(--color-code-bar-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: 11px;
  color: var(--color-text-muted);
}
.ai-md :deep(.md-code-lang) {
  text-transform: lowercase;
  letter-spacing: 0.02em;
}
.ai-md :deep(.md-code-copy) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-md :deep(.md-code-actions) { display: flex; align-items: center; gap: 2px; }
/* html 代码块的「预览」按钮与复制按钮同构，只是悬停时给成主色 ——
   它是这一行里唯一会「做事」的按钮，值得比复制更显眼一点 */
.ai-md :deep(.md-code-run) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-md :deep(.md-code-run:hover) { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-md :deep(.md-code-run svg) { width: 13px; height: 13px; }
.ai-md :deep(.md-code-copy:hover) { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-md :deep(.md-code-copy.copied) { color: var(--color-success); }
.ai-md :deep(.md-code-copy svg) { width: 13px; height: 13px; }
.ai-md :deep(pre) {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  background: transparent;
  font-size: 12px;
  line-height: 1.6;
}
.ai-md :deep(code) {
  font-family: var(--font-mono, monospace); font-size: 12px;
  background: var(--color-code-inline-bg); padding: 1px 5px; border-radius: var(--radius-xs);
}
.ai-md :deep(pre code) { background: none; padding: 0; font-size: 12px; }
.ai-md :deep(table) { border-collapse: collapse; }
.ai-md :deep(th), .ai-md :deep(td) { border: 1px solid var(--color-border); padding: 5px 9px; }
.ai-md :deep(th) { background: var(--color-bg-secondary); font-weight: 600; }
/* 表格增强：横向滚动容器 + 导出条 + 可排序列（见 enhanceTables） */
.ai-md :deep(.md-table-wrap) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  overflow-x: auto;
}
.ai-md :deep(.md-table-bar) {
  display: flex; justify-content: flex-end;
  padding: 3px 6px;
  border: 1px solid var(--color-border);
  border-bottom: none;
  border-radius: var(--radius-md) var(--radius-md) 0 0;
  background: var(--color-code-bar-bg);
}
.ai-md :deep(.md-table-export) {
  display: inline-flex; align-items: center; gap: 3px;
  font-size: 11px; padding: 2px 8px;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-primary); color: var(--color-text-muted); cursor: pointer;
}
.ai-md :deep(.md-table-export:hover) { color: var(--color-accent); border-color: var(--color-accent); }
.ai-md :deep(.md-th-sortable) { cursor: pointer; user-select: none; white-space: nowrap; }
.ai-md :deep(.md-th-sortable:hover) { color: var(--color-accent); }
.ai-md :deep(.md-th-sortable[data-sort-dir="asc"]::after) { content: " ▲"; font-size: 9px; }
.ai-md :deep(.md-th-sortable[data-sort-dir="desc"]::after) { content: " ▼"; font-size: 9px; }
.ai-md :deep(hr) { border: none; border-top: 1px solid var(--color-border); margin: 10px 0; }
.ai-md :deep(img) { max-width: 100%; border-radius: var(--radius-sm); }

/* 数学公式。katex.min.css 负责字形，这里只管容器：块级公式横向可滚
   （长公式会顶破消息气泡），行内公式不要影响行高节奏。 */
.ai-md :deep(.md-math-inline) { display: inline; }
.ai-md :deep(.md-math-inline .katex) { font-size: 1.05em; }
.ai-md :deep(.md-math-block) {
  display: block;
  margin: 8px 0;
  padding: 2px 0;
  overflow-x: auto;
  overflow-y: hidden;
}
.ai-md :deep(.md-math-block .katex-display) { margin: 0; }
/* 公式还没写完 / 写法不支持时的降级形态：等宽小字，别跟正文混在一起看不出是源码 */
.ai-md :deep(.md-math-raw) {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--color-text-secondary);
}

/* Mermaid 图。结构是「工具栏 + 内容区」两层：
   - 工具栏常驻（画出来之后也是），长图占满一屏时折叠 / 看源码仍然点得到；
   - 内容区按容器属性切三种形态：图形（data-mermaid-done）/ 源码（data-mermaid-src）/
     折叠（data-mermaid-folded）。源码 <pre> 一直留在 DOM 里，只是被藏起来。 */
.ai-md :deep(.md-mermaid) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-code-bg);
  /* 圆角要裁得住里面的工具栏；横向滚动交给内容区，见 .md-mermaid-body */
  overflow: hidden;
}
.ai-md :deep(.md-mermaid-bar) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px 4px 10px;
  background: var(--color-code-bar-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: 11px;
  color: var(--color-text-muted);
  /* 工具栏只是外壳，别让它参与划词复制 */
  user-select: none;
}
/* 折叠后只剩这一条：分隔线留着会像半张空卡片 */
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-bar) {
  border-bottom-color: transparent;
}
.ai-md :deep(.md-mermaid-lang) {
  text-transform: lowercase;
  letter-spacing: 0.02em;
}
.ai-md :deep(.md-mermaid-acts) { display: flex; align-items: center; gap: 2px; }
.ai-md :deep(.md-mermaid-act) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-md :deep(.md-mermaid-act:hover) { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-md :deep(.md-mermaid-act svg) { width: 13px; height: 13px; display: block; }

/* 按钮里的两个图标靠容器属性切换，JS 只改属性、不重排 DOM ——
   看图时显示 `</>`（点了去看源码），看源码时显示眼睛（点了回来看图）。
   ⚠️ 这里的隐藏必须写成 `.md-mermaid-act .md-mermaid-i-*`（三个 class）：
   上面那条 `.md-mermaid-act svg { display: block }` 是「两 class + 一标签」，
   比单纯 `.md-mermaid-i-eye`（两 class）更具体，写短了会四个图标一起显示。 */
.ai-md :deep(.md-mermaid-act .md-mermaid-i-eye) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-act .md-mermaid-i-code) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-act .md-mermaid-i-eye) { display: block; }
/* 折叠态：展开时是向上箭头（点了收起），折叠后换成向下箭头（点了展开） */
.ai-md :deep(.md-mermaid-act .md-mermaid-i-down) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-act .md-mermaid-i-up) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-act .md-mermaid-i-down) { display: block; }

.ai-md :deep(.md-mermaid-body) { overflow-x: auto; }
/* 默认（占位块已带 data-mermaid-src）显示源码、藏起图形；点了「查看图」移除该属性就反过来。
   刻意不用 `:not([data-mermaid-src])` 压成一条：拆成两条后写的覆盖前面的，
   意图更直白，也不必依赖 :not 的参数写法（预览脚本抽 :deep 时会在 `:not(` 的括号上截断）。 */
.ai-md :deep(.md-mermaid-svg) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-done="1"] .md-mermaid-svg) {
  display: block;
  padding: 10px;
  background: var(--color-bg-primary);
}
.ai-md :deep(.md-mermaid[data-mermaid-done="1"] .md-mermaid-src) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-svg) { display: none; }
/* 源码视图限高：13 个节点的图定义能有一屏长，别让它把整条回答推走 */
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-src) {
  display: block;
  max-height: 320px;
  overflow: auto;
}
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-body) { display: none; }

/* Mermaid 输出带内联 width/max-width，这里只兜住「别溢出、别被拉变形」 */
.ai-md :deep(.md-mermaid svg) {
  display: block;
  margin: 0 auto;
  max-width: 100%;
  height: auto;
}
/* 失败提示压在源码上方：流式半截定义每帧都会失败，没有这行用户会以为是卡片坏了 */
.ai-md :deep(.md-mermaid-err) {
  padding: 4px 10px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-code-bar-bg);
  font-size: 11px;
  color: var(--color-text-muted);
}
/* 流式尾块（正在写的那一块）不显示工具栏。
   原因：最后一块每帧都会整块重新渲染（见 renderLiveBlocks），点上去的状态下一帧
   就被冲掉，表现成「点了没反应」；而且此时图还没画出来，两个按钮也没有可操作的对象。
   前面已完成的块不受影响，照常可折叠。 */
.ai-msg.streaming :deep(.ai-md-block:last-child .md-mermaid-bar) { display: none; }

/* 输入 */
.ai-input-area {
  /* 输入行控件的唯一高度来源：输入框 / 图标按钮 / 发送按钮全部引用它，
     避免各处硬编码不同数值出现高度参差 */
  --ai-input-h: 56px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
  /* @ 补全弹层的定位锚点：它要贴着输入区上沿向上展开，
     而不是跟着光标跑（textarea 里算光标坐标要镜像一个 div，得不偿失）。 */
  position: relative;
}
.ai-input-row { display: flex; gap: 10px; align-items: flex-end; }
.ai-input-tools { display: flex; align-items: flex-end; gap: 6px; flex-shrink: 0; }
.ai-tool-btn {
  display: flex; align-items: center; justify-content: center;
  width: 40px; height: var(--ai-input-h); border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  border-radius: var(--radius-md); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.ai-tool-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
/* 待发送图片预览缩略图 */
.ai-attach-preview { display: flex; flex-wrap: wrap; gap: 8px; }

/* —— @ 引用（chip 与补全弹层）—— */
.ai-mention-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.ai-mention-chip {
  display: inline-flex; align-items: center; gap: 3px; max-width: 260px;
  padding: 2px 4px 2px 6px; font-size: 11px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
}
.ai-mention-chip-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}
.ai-mention-chip-del {
  border: none; background: transparent; color: var(--color-text-muted);
  cursor: pointer; font-size: 13px; line-height: 1; padding: 0 2px;
}
.ai-mention-chip-del:hover { color: var(--color-danger); }
.ai-mention-pop {
  position: absolute; left: 16px; right: 16px; bottom: calc(100% - 6px);
  max-height: 240px; overflow: auto; z-index: 30; padding: 4px;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.18);
}
.ai-mention-item {
  display: flex; align-items: center; gap: 6px; padding: 4px 6px;
  font-size: 12px; color: var(--color-text-secondary); cursor: pointer;
  border-radius: var(--radius-sm);
}
.ai-mention-item.is-active { background: var(--color-bg-hover); color: var(--color-text-primary); }
.ai-mention-name { flex-shrink: 0; }
.ai-mention-path {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-muted); text-align: right;
}
.ai-attach-thumb {
  position: relative; width: 56px; height: 56px; border-radius: var(--radius-sm);
  overflow: hidden; border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
}
.ai-attach-img { width: 100%; height: 100%; object-fit: cover; display: block; }
.ai-attach-del {
  position: absolute; top: 1px; right: 1px; width: 16px; height: 16px;
  display: flex; align-items: center; justify-content: center; line-height: 1;
  border: none; border-radius: 50%; background: rgba(0,0,0,0.55); color: #fff;
  font-size: 12px; cursor: pointer; padding: 0;
}
.ai-attach-del:hover { background: var(--color-danger); }
.ai-url-row { display: flex; gap: 6px; }
.ai-url-input {
  flex: 1; height: 32px; padding: 0 10px; border: 1px solid var(--color-border);
  border-radius: var(--radius-md); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 12px; font-family: inherit; outline: none;
}
.ai-url-input:focus { border-color: var(--color-accent); }
.ai-url-ok {
  padding: 0 14px; height: 32px; border: none; border-radius: var(--radius-md);
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 12px; cursor: pointer; font-family: inherit;
}
/* 消息内图片缩略图（用户气泡 / 历史消息） */
.ai-msg-imgs { display: flex; flex-wrap: wrap; gap: 8px; }
.ai-msg.user .ai-msg-imgs { justify-content: flex-end; }
/* 缩略图尺寸：120px 在实际气泡里被 max-width 二次压缩后几乎看不清内容，
   放大到 180px/55% 并提高 max-height，保证缩略阶段就能辨认图片主体 */
.ai-msg-img { width: 180px; max-width: 55%; border-radius: var(--radius-sm); overflow: hidden; border: 1px solid var(--color-border); }
.ai-msg-img-el { width: 100%; display: block; max-height: 220px; object-fit: cover; background: var(--color-bg-tertiary); }
.ai-msg-img-el.is-clickable { cursor: zoom-in; }
.ai-img-broken { height: 80px; display: flex; align-items: center; justify-content: center; font-size: 20px; color: var(--color-text-disabled); }

/* 图片全屏查看器（Teleport 到 body，scoped 属性仍在元素上，样式生效） */
.ai-img-viewer {
  position: fixed;
  inset: 0;
  z-index: 1000;
  background: var(--color-bg-overlay-deep);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: zoom-out;
  padding: var(--space-4);
}
.ai-img-viewer img {
  max-width: 90vw;
  max-height: 90vh;
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  cursor: default;
}
.ai-input {
  flex: 1;
  resize: none;
  height: var(--ai-input-h);
  padding: 10px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 13px;
  font-family: inherit;
  outline: none;
  line-height: 1.5;
}
.ai-input:focus { border-color: var(--color-accent); }
.ai-input:disabled { opacity: 0.6; cursor: not-allowed; }
.ai-send {
  align-self: flex-end;
  padding: 0 20px;
  height: var(--ai-input-h);
  border: none;
  border-radius: var(--radius-md);
  background: var(--color-accent);
  color: var(--color-accent-text);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  font-family: inherit;
  white-space: nowrap;
}
.ai-send:disabled { opacity: 0.5; cursor: not-allowed; }

</style>
