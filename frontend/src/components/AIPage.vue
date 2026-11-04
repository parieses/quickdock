<script setup lang="ts">
import { ref, computed, reactive, onMounted, onUnmounted, onActivated, onDeactivated, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { KEEP_ALIVE } from '../pages/keepAlive'
import {
  Copy, Image as ImageIcon, X, Download, MessageSquare,
  Languages, FileText, GraduationCap, Lightbulb, Check,
} from '@lucide/vue'
import { Marked } from 'marked'
import { unwrap } from '../utils/api'
import AiSidePanel from './AiSidePanel.vue'
import AiConversationSidebar from './AiConversationSidebar.vue'
import AiToolbar from './AiToolbar.vue'
import AiDigestPanel from './AiDigestPanel.vue'
import AiJobsPanel from './AiJobsPanel.vue'
import AiHero from './AiHero.vue'
import AiComposer from './AiComposer.vue'
import AiMessageList from './AiMessageList.vue'
import AiRoundBar from './AiRoundBar.vue'
import AiApprovalBar from './AiApprovalBar.vue'
import { SetAIPageVisible } from '../../bindings/quickdock/services/appservice'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { downloadText, safeFileName } from '../utils/download'
import { useAiMarkdownBlocks } from '../composables/useAiMarkdownBlocks'
import { useAiAttachments } from '../composables/useAiAttachments'
import { useAiMention } from '../composables/useAiMention'
import { useAiRuns } from '../composables/useAiRuns'
import type { PlanCardState } from '../composables/useAiRuns'
import { useAiSlashSkill } from '../composables/useAiSlashSkill'
import { isMathFenceLang, mathExtension, renderMath } from '../utils/mathMarkdown'
import { enhanceTables, sortTableByColumn } from '../utils/tableEnhance'
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
import { logErr } from '../utils/logger'
import { useFloatMenu } from '../composables/useFloatMenu'
import { aiGenerating, aiFinishedUnread } from '../composables/bridge'
import { resolveConvDir } from '../utils/aiConvDir'
import { pickActiveRun } from '../utils/aiRuns'
import type { AIRun } from '../utils/aiRuns'
import { CreateNoteDoc } from '../../bindings/quickdock/services/note/noteservice'
import type { PendingImage, ToastAPI } from '../types'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AIConvHit, AIHooksInfo, AIProfilesResult } from '../types/ai'
import type {
  AIArtifact, AICheckpointView, AIConnector, AIConversation, AIJobRow, AIMessage, AIMsgImage,
  AIRuleFile, AISkill, AISpace, AIToolCall, OutlineItem, AIPrompt, AIMentionItem,
} from '../types/ai'
import { artifactsFromCalls, artifactsOf, countToolRounds, serializeTools, shortPath, toolList } from '../utils/aiTools'

import {
  AIListProfiles,
  AISetActiveProfile,
  AIStreamInfo,
  AIRegenerateTitle,
  AISetConversationStar,
  AISummarizeConversation,
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
  AISearchMessages,
  AISetConversationMode,
  AISetConversationPermission,
  AIGetHooks,
  AISetHooksApproved,
  AIGetGitBranch,
  AIGitBranches,
  AIWorktreeRemove,
  AIGetSkills,
  AIGetPrompts,
  AISuggestMentions,
  AIGetExtServers,
  AISetExtServerEnabled,
  AIJobsList,
} from '../../bindings/quickdock/services/ai/aiservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'

// 这个 name 是 App.vue 里 <KeepAlive :include> 的匹配依据，改它等于让 AI 页失去保活。
// 为什么必须显式声明：页面是经 defineAsyncComponent(import(...)) 注册的，而 KeepAlive 判断
// 异步组件时读的是 `vnode.type.__asyncResolved`（内层 SFC 定义）上的名字 ——
// 在包装器上赋 name 完全无效，不声明就永远匹配不上，切页照样销毁组件。
// name 的唯一来源：pages/keepAlive.ts（与 KeepAlive 白名单共用同一常量）。
defineOptions({ name: KEEP_ALIVE.ai })

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!


const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

const conversations = ref<AIConversation[]>([])
const spaces = ref<AISpace[]>([])
const activeId = ref('')
const messages = ref<AIMessage[]>([])
const input = ref('')
const mode = ref('chat')
// —— 「哪几条在跑」——
//
// 一条任务一条流：切会话不再打断回答（见 selectConv），而且**允许多条任务同时跑**。
// 所以流式状态不能是几个全局 ref，必须按会话各存一份：
//
//   全局单缓冲的那些年，第二条流一开就把第一条的字拼进来（同一个 text、同一个
//   streamTools），所以只能在 send 门口硬拦「另一条对话还在生成」。用户被拦住的
//   场景恰恰最需要它——长任务在跑，他想顺手问另一件事。
//
// now：每条流只写自己这个对象；界面画的是「当前打开的那条」对应的 run（见
// activeRun / liveXxx）。切走的那条继续在后台跑，内容由后端落库，切回来照常读库。
//
// AIRun 的定义已移到 utils/aiRuns.ts：消息区（AiMessageList）直接读它画流式气泡，
// 定义留在这里会让那个组件反向依赖页面。

/** 本页是否在前台（KeepAlive 下切到别的导航页时为 false） */
const pageActive = ref(true)

const runs = ref<AIRun[]>([])
/** 从落地页发起、还没拿到会话 id 的那条：落地页上「当前这条」就是它 */
const draftRunId = ref('')

// —— 任务清单卡片（plan_update 工具，见 services/ai/tools/plan.go） ——
//
// 大任务由 AI 自己拆成步骤、边做边勾（这就是「计划」模式去掉之后的行为）。
// 清单挂在**会话 id**上而不是 run 上：回答结束、run 被回收之后清单还没看完的
// 情况不少，跟着 run 一起消失太急；切走的会话切回来也还在。
const planCards = reactive(new Map<string, PlanCardState>())

/** 当前这一页的清单（新建任务在 conv 事件之前用 rid 当键）。 */
const planHere = computed<PlanCardState | null>(() => {
  const key = activeId.value || draftRunId.value
  return key ? planCards.get(key) ?? null : null
})

function planDoneCount(p: PlanCardState): number {
  return p.items.filter((x) => x.status === 'done').length
}

function runOf(convId: string): AIRun | undefined {
  if (!convId) return undefined
  return runs.value.find((r) => r.convId === convId)
}

/** 当前打开的那条任务对应的 run（没有则为 null）。模板里所有流式状态都从它取。 */
const activeRun = computed<AIRun | null>(
  () => pickActiveRun(runs.value, activeId.value, draftRunId.value) || null,
)
/** 当前这一页是否正在生成（后台跑别的任务时不算） */
const streamingHere = computed(() => !!activeRun.value)
/** 有没有任意一条在跑：侧栏转圈、页面保活标记这类「全局有没有活儿」的地方用它 */
const streaming = computed(() => runs.value.length > 0)

/** 某条会话上一次失败的报错。键是会话 id，落地页/新任务用空串。 */
const convErrors = ref<Record<string, string>>({})
/** 页面级错误（拉档案 / 搜消息 / 拉会话列表失败）：不属于任何一条任务。 */
const pageError = ref('')
function setConvError(convId: string, msg: string) {
  if (msg) convErrors.value[convId] = msg
  else delete convErrors.value[convId]
}
const errorMsg = computed(() => {
  const r = activeRun.value
  if (r?.error) return r.error
  return convErrors.value[activeId.value] || pageError.value || ''
})

// 每条会话的运行状态，只用来给左栏画一个点：
//   running = 还在生成（用户已经切走了，或者正看着）
//   done    = 生成完了，但用户还没回到这条会话
//   error   = 后台那条失败了（用户切回来才看得到报错，所以先给个红点）
// 点进这条会话就删掉 —— 这个点的含义就是「有结果你没看」，看过了它就该消失。
type ConvRunState = 'running' | 'done' | 'error'
const convRunState = ref<Record<string, ConvRunState>>({})
function markConvRunning(id: string) {
  if (id) convRunState.value[id] = 'running'
}
function markConvFinished(id: string, failed = false) {
  if (!id) return
  // 用户正停在它上面（而且确实在这一页上）：内容就在眼前，不需要任何标记。
  // pageActive 也要算：切去别的导航页时 AI 页被 KeepAlive 保活，activeId 没变，
  // 但用户根本看不见那条回答——这时候该留点。
  if (activeId.value === id && pageActive.value) delete convRunState.value[id]
  else convRunState.value[id] = failed ? 'error' : 'done'
}

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

// —— 项目钩子（C3）——
// cwd 变了就要重读：<cwd>/.quickdock/hooks.json 是随仓库走的文件，换项目
// 换机器批准状态都不一样。只在 found 且未批准时出提示条：已批准的钩子不吭声
// ——它的产出已经挂在工具卡片里了，批准状态不是用户每次都要再确认一遍的事。
const aiHooks = ref<AIHooksInfo | null>(null)
let aiHooksSeq = 0
async function loadAIHooks() {
  const dir = convCwd.value.trim()
  if (!dir) { aiHooks.value = null; return }
  const seq = ++aiHooksSeq
  try {
    const r = unwrap(await AIGetHooks(dir))
    // 后发的请求可能先回：列表停在上一个目录的钩子上比没有钩子更糟。
    if (seq === aiHooksSeq) aiHooks.value = r as AIHooksInfo
  } catch { aiHooks.value = null }
}
async function approveHooks(on: boolean) {
  const dir = convCwd.value.trim()
  if (!dir) return
  try {
    unwrap(await AISetHooksApproved(dir, on))
    aiHooks.value = null
  } catch { /* 批准失败不弹错：下一次改文件时钩子还是不生效，用户会来问 */ }
}

// —— 当前目录的 git 分支探测（gitRepo / gitBranch / effectiveDir / loadGitBranch /
//    对应的 watch）—— 定义见下方「新建空间」声明之后。effectiveDir 引用了 newTaskSpace /
//    newTaskDir / NEW_SPACE（892 行才声明），整块必须放在它们之后：否则 setup 阶段 Vue 给
//    watch 取旧值时会同步求值 effectiveDir，命中 TDZ（Cannot access 'newTaskSpace' before
//    initialization），整页 setup 崩溃。
watch(convCwd, loadAIHooks, { immediate: true })

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
// 工具卡片、规则文件、思维链、正文缓冲都挂在 run 上（见 AIRun）：两条任务同时跑时
// 各写各的，切走的那些 token / 卡片一直在自己那份里长，不会串到眼前这条上。

// —— 流式状态只在页面内部用，不再穿过模板 ——
//
// blocks / reasoning / rules 这几个字段每帧都在变，**绝不能进这一页的模板**：
// 一旦模板读了它们，父组件的 render 就跟着流式节奏每帧失效，4200 行模板整份 diff。
// 消息区（AiMessageList）现在自己从 runs 里取（传数组 + 会话 id），读取发生在
// 子组件内部，失效范围只有那棵子树。
//
// 留在页面里的只有「不随 token 变」的那部分：审批条（只在工具事件时变）与
// 产物面板（同理）。
const NO_TOOLS: AIToolCall[] = []
const liveTools = computed(() => activeRun.value?.tools ?? NO_TOOLS)
// 流式期的产物：file_write 返回的当下就挂出来，等 done 再显示会让
// 「文件已经写完了」和「看得见」之间隔一整轮回答。
const liveArtifacts = computed(() => artifactsFromCalls(liveTools.value))

// 审批条要展示的那一条：取第一个还等着答复的调用。
//
// 为什么是「第一条」而不是「全部」：后端是一次一条地等（回完一条才下发下一条），
// 同时堆积多条只是前端事件顺序造成的短暂现象，条数用 count 标出来就够了。
const pendingApproval = computed(() => liveTools.value.find((tc) => tc.approval === 'pending') || null)
const pendingApprovalCount = computed(() => liveTools.value.filter((tc) => tc.approval === 'pending').length)

// —— 产物预览 / 右侧面板 ——
// 面板本体（标签页、CodeMirror、文件读写）已收进 AiSidePanel；页面只留开合状态，
// 因为工具栏按钮的 is-active 与 Ctrl+Shift+B 都要读它。
const sideOpen = ref(false)
const sidePanelRef = ref<InstanceType<typeof AiSidePanel> | null>(null)

// convArtifacts 本任务全部回复的产物合集（跨条消息去重，后写的覆盖先写的 op）。
//
// 必须把流式期的产物也并进来：file_write 一返回，消息还挂在 run 上没落库，
// 侧栏这时读到的会是上一轮的旧列表——「产物明明多了，打开侧栏还是那几条」。
// 两条来源按 path 去重，live 放后面覆盖同名旧条目（op / callId 取最新的）。
const convArtifacts = computed<AIArtifact[]>(() => {
  const map = new Map<string, AIArtifact>()
  for (const m of messages.value) {
    for (const a of artifactsOf(m)) map.set(a.path, a)
  }
  for (const a of liveArtifacts.value) map.set(a.path, a)
  return [...map.values()]
})

function toggleSidePanel() {
  sideOpen.value = !sideOpen.value
  // 首次打开送一个首页标签页，避免一块空白
  if (sideOpen.value) sidePanelRef.value?.ensureTab()
}

// 消息区产物条（含流式产物）点击：打开面板并直接跳到那个文件。
function openArtifactInPanel(a: AIArtifact) {
  if (!sideOpen.value) {
    sideOpen.value = true
    sidePanelRef.value?.ensureTab()
  }
  void sidePanelRef.value?.openArtifact(a)
}

// ---- 工具卡片的展示 ----
//
// 直接把 toolName + 原始参数 JSON 铺在卡片上是最省事的写法，也是最难看的：
// 一行 `file_edit` 后面跟一坨 {"path":..., "old_string":...}，用户得在 JSON 里
// 找自己关心的那一个值。所以卡片读的是「动作 + 目标」两件事：
// 图标与动词说清做了什么，后面那一句说清对谁做的。

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

// jobIdOf 从 run_command 的结果里挑出后台任务 id（没有则空串）。
//
// 结果串是后端 JSON 化后的文本，这里只认带引号的 "jobId"：
// 同步执行的结果里没有这个键，返回空串就等于「不是后台任务」，
// 卡片上因此不会多出一行跟它无关的提示。

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

// 按此计划执行：切回对话模式，把计划作为要求发出去。
//
// 必须先切模式再发送：留在计划模式里发，工具面还是只读的，模型会一边说
// 「我来改」一边什么都调不动。模式要落库（AISetConversationMode），
// 否则切走再回来又被 selectConv 读回 plan。
async function runPlan(m: AIMessage) {
  // 只看这条会话自己：别的任务在跑不影响它（并行是允许的）。
  if (runOf(activeId.value)) {
    toast.error(t('aiSameConvRunning'))
    return
  }
  const convID = activeId.value
  if (convID) {
    try {
      unwrap(await AISetConversationMode(convID, 'chat'))
      const c = conversations.value.find((x) => x.id === convID)
      if (c) c.mode = 'chat'
    } catch (e) {
      toast.error(getErrorMessage(e))
      return
    }
  }
  mode.value = 'chat'
  // 前缀与计划之间空两行：模型对「指令」和「材料」的边界靠空行判断，
  // 挤在一起容易把计划里的第一句当成指令的一部分。
  input.value = `${t('aiPlanRunPrefix')}\n\n${m.content}`
  await send()
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
    renderMermaidIn(msgListRef.value?.contentEl() || null, { skip: skipStreamTail })
    enhanceTables(msgListRef.value?.contentEl() || null, {
      exportLabel: t('aiExportCsv'),
      onExported: (name) => toast.success(t('aiDownloadOk', { name })),
    })
  })
}

// 本地 AI 流式服务信息（端口 + 随机令牌，通过 Header 传递）
const aiStreamPort = ref(0)
const aiStreamToken = ref('')

// —— 滚动跟随（已下沉到 AiMessageList） ——
// 滚动状态与消息容器 DOM 是一件事：贴底判定、ResizeObserver、"回到底部"按钮
// 都跟着容器走。留在父组件里就得靠事件回传互相读对方状态，反而更容易漏。
// 这里只保留三个转发入口，父组件在「发送 / 切会话 / 流式跟帧」时调用。
const msgListRef = ref<InstanceType<typeof AiMessageList> | null>(null)

function scrollToBottom(smooth = false) {
  msgListRef.value?.scrollToBottom(smooth)
}

function followBottom() {
  msgListRef.value?.followBottom()
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
//
// 「计划」不再是一个可选类型：它变成了 AI 自己的行为——任务太大、一次说不完时，
// 它自己把步骤拆出来、按顺序做、做完一项勾一项（规则写在 chat 模式的提示词里），
// 而不是让用户先切进一个「只读、只交清单」的模式。留着那个入口的副作用是：
// 用户以为自己在规划，实际只是拿到一份清单，还得再点一次「开始执行」。
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
// attachments（待发送预览）与选择/粘贴/拖拽的交互都在 useAiAttachments 里，
// 这里只留能力判定所需的输入源：imageInputMode 来自当前激活档案的声明。

// 当前激活档案的 imageInput 能力；空（旧档案）按 both 处理，与后端 normalizeImageInput 一致。
const imageInputMode = computed<string>(() => {
  const p = aiProfiles.value.find((x) => x.id === aiActive.value)
  const v = (p?.imageInput || 'both').toLowerCase()
  if (v === 'url' || v === 'base64') return v
  return 'both'
})
const {
  attachments, allowFileImage, allowUrlImage,
  removeAttachment,
  onInputPaste, onInputDrop,
  urlInputOpen, urlDraft, fileInputEl,
  onPickFiles, openFilePicker, toggleUrlInput, confirmUrlImage,
} = useAiAttachments({ imageInputMode, toast, t })

// —— @ 引用 ——
// 补全弹层、@token 识别、会话/文件候选与方向键交互都在 useAiMention 里，
// 这里只把输入框与工作目录这两份状态注入进去。
const {
  mentions, mentionItems, mentionIdx, mentionOpen, lastMentions,
  pickMention, addMention, removeMention, onMentionKey,
// 传 streamingHere 而不是 streaming：后台在跑别的会话时，这条会话照样能 @ 引用
} = useAiMention({ input, streaming: streamingHere, hasKey, conversations, convCwd })

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
    // 任务清单：- [ ] / - [x]（GitHub 风格）渲染成带样式的勾选框，而非原生 <input>。
    // 原生 <input> 会被 DOMPurify 默认白名单剥掉（表单元素），勾选框就消失了；
    // 改成 <span> 既能被 DOMPurify 放行，又能用 CSS 画出方框与对勾、并对已完成项加删除线。
    // AI 在回复里维护这份清单、每完成一步把 [ ] 改成 [x]，界面随之实时打勾。
    checkbox(token: { checked?: boolean }) {
      const done = !!token.checked
      return `<span class="ai-task-box${done ? ' is-done' : ''}" role="img" aria-label="${done ? '已完成' : '未完成'}"></span>`
    },
    listitem(token: { tokens?: any[]; task?: boolean; checked?: boolean }) {
      const body = this.parser.parse(token.tokens ?? [])
      if (token.task) {
        return `<li class="ai-task-item${token.checked ? ' is-done' : ''}">${body}</li>`
      }
      return `<li>${body}</li>`
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

// markdown 分块渲染内核（缓存/切块/增量）已抽到 composables/useAiMarkdownBlocks。
// 必须放在 md 装配之后：md 是 const，提前引用会命中 TDZ。
const {
  renderBlock,
  clearLiveCache,
  renderMessageBlocks,
  renderLiveBlocks,
  primeMessageBlocks,
  pruneMessageBlocks,
} = useAiMarkdownBlocks({ md })

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
    pageError.value = getErrorMessage(e)
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
const FOLD_TASKS = 10
const FOLD_SPACES = 8
const FOLD_SPACE_TASKS = 8

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
      pageError.value = getErrorMessage(e)
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
    pageError.value = getErrorMessage(e)
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
  scrollToBottom()
  // 历史消息里可能有 ```mermaid（图不是同步渲染的，得在 DOM 挂上后补扫一遍）
  scheduleMermaidScan()
}

async function selectConv(id: string, msgId?: string) {
  // 不再打断正在生成的回答：切换的是「我在看哪条」，不是「停掉哪条」。
  // 停有显式的停止按钮，不拿「切换」当停止的替代品——用户只是想去翻一眼别的
  // 任务，回来时半截回答没了，那是最难受的一种丢内容。
  activeId.value = id
  // 看过就消：绿点的含义是「有结果你没看」，人已经在里面了就不该再挂着。
  // 但正在跑的那条不能删——删了之后再切走，就再也看不出它还在生成了。
  if (runOf(id)) convRunState.value[id] = 'running'
  else delete convRunState.value[id]
  const conv = conversations.value.find((c) => c.id === id)
  // 工作目录跟着任务走：两条对话可以绑不同项目，切回来时要还原成它自己的那个。
  convCwd.value = (conv?.cwd || '').trim()
  // 模式是这个任务创建时选定的属性，切回来要还原——否则「这条是用来翻译的」
  // 在切走再回来之后就丢了。
  mode.value = (conv?.mode || 'chat').trim() || 'chat'
  // 权限跟着任务走：这条对话开过完全访问，切回来要还原成它自己的档位。
  permissionFull.value = (conv?.permission || '').trim() === 'full'
  // 工作区同样跟着任务走：已开的不再显示开关，只显示「基于哪条分支 + 移除」；
  // 没开的把草稿状态清干净，免得上一条任务的勾选黏到这一条上。
  worktreeBranchOpen.value = false
  if ((conv?.worktree_path || '').trim()) {
    worktreeEnabled.value = true
    worktreeBranch.value = (conv?.worktree_branch || '').trim()
  } else {
    worktreeEnabled.value = false
    worktreeBranch.value = ''
  }
  // 已有会话的归属以库里的为准，新建待定值在这里作废。
  pendingSpaceId.value = ''
  // 流式缓冲不用清：每条流各存一份（见 AIRun），切过来看到的就是这条会话
  // 自己的半截回答，别的 run 一个字节都不会漏到这一页。
  // 待发送的 @ 引用必须清：切会话之后 cwd 可能变了，旧路径带过去只会被后端一堆拒。
  resetComposerState()
  pageError.value = ''
  await loadMessages(id)
  // 切回一条「还在跑」的会话时补一次流式渲染。
  //
  // 后台那条 run 的 flush 刻意不画（见 flushStream：只画用户正看着的那条），
  // 所以它的 blocks 停在切走那一刻。不补的话，模型正在跑工具（可能几十秒
  // 没有新 token）时切回来，看到的是缺了后半截的回答，甚至整个气泡空白。
  const back = runOf(id)
  if (back) {
    // 定时 flush 可能还没到点，先把缓冲并进正文再重画。
    if (back.buf) { back.text += back.buf; back.buf = '' }
    if (back.reasonBuf) { back.reasoning += back.reasonBuf; back.reasonBuf = '' }
    renderLiveBlocks(back.text, back.blocks)
    scheduleMermaidScan()
    followBottom()
  }
  // 回滚点跟着会话走：切回来要能看到这条对话改过哪些文件。
  // 与消息分开拉而不是塞进 AIGetMessages：回滚点是「另一张表的东西」，
  // 混进消息响里会让那个接口为一张很少用到的表付出每次查询的代价。
  void loadCheckpoints(id)

  // 从搜索命中点进来的：落到那句话上，而不是落在底部让用户自己翻。
  // 命中可能早于「默认只挂最近 200 条」的窗口，交给 ensureRendered 先补渲染。
  if (msgId) {
    await nextTick()
    // 上面那条「还在跑就补渲染 + followBottom」会把视口拉到底，跳转必须排在它之后。
    const ok = await msgListRef.value?.ensureRendered(msgId)
    if (!ok) {
      // 找不到（消息在这期间被删了 / 换了会话）就停在原处，不提示——
      // 用户看到的是这条对话本身，位置只差一次滚动。
      followBottom()
    }
  }
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

// 侧栏右键菜单要的目录：会话 cwd 优先，空则回退所属空间目录。
// 规则本身放在 utils/aiConvDir.ts（纯函数、有测试）——它必须与后端
// 「cwd 恒等于此刻真正在干活的目录（含隔离工作区）」这一口径保持一致。
function convDir(c: AIConversation): string {
  return resolveConvDir(c, spaces.value)
}

// 侧栏右键：在资源管理器打开某个目录。
// 与顶栏 openCwd 的区别只在于「作用于哪一个」——那个按钮只认当前会话，
// 想看另一条任务（或某个空间）的文件得先切过去，而这个右键菜单不用。
async function openDir(dir: string) {
  if (!dir) {
    toast.error(t('aiConvNoDir'))
    return
  }
  try {
    unwrap(await RevealInExplorer(dir))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 复制路径而不是复制标题：打开资源管理器之后常要 cd 过去，
// 路径能直接粘进终端；标题粘出去没有用处。
async function copyPath(dir: string) {
  if (!dir) {
    toast.error(t('aiConvNoDir'))
    return
  }
  try {
    await navigator.clipboard.writeText(dir)
    toast.success(t('copied'))
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

// —— 当前目录的 git 分支（输入框下方实时显示）——
// 与钩子同理：cwd 变了就要重读。非仓库 / 没装 git 一律 repo=false（后端不报错，
// 前端也就不弹错），只是那一行分支提示不显示而已。
// 整块特意放在 newTaskSpace / newTaskDir / NEW_SPACE 声明之后：effectiveDir 计算属性
// 引用了它们，而 Vue 的 watch 在 setup 阶段会先跑一次 getter 取旧值来建依赖，若放在
// 它们声明之前就会命中 TDZ（Cannot access 'newTaskSpace' before initialization），
// 整页 setup 崩溃。放在这里求值即安全。
const gitRepo = ref(false)
const gitBranch = ref('')
let gitSeq = 0
// 探测 git 分支用的「有效目录」：进了会话就用会话工作目录；还在落地页（没进会话）
// 就看当前选中的空间目录（或新建空间时挑的目录）——这样落地页那个输入框也能显示分支。
async function loadGitBranch() {
  const dir = effectiveDir.value.trim()
  if (!dir) { gitRepo.value = false; gitBranch.value = ''; return }
  const seq = ++gitSeq
  try {
    const data = unwrap(await AIGetGitBranch(dir)) as { repo?: boolean; branch?: string } | null
    if (seq !== gitSeq) return
    gitRepo.value = !!data?.repo
    gitBranch.value = data?.branch || ''
  } catch {
    if (seq === gitSeq) { gitRepo.value = false; gitBranch.value = '' }
  }
}
const effectiveDir = computed(() => {
  const c = convCwd.value.trim()
  if (c) return c
  if (newTaskSpace.value === NEW_SPACE) return newTaskDir.value.trim()
  if (newTaskSpace.value) {
    const sp = spaces.value.find((s) => s.id === newTaskSpace.value)
    return (sp?.cwd || '').trim()
  }
  return ''
})
// 这条 watch 必须在 newTaskSpace 等声明之后建立（本块位置即保证）；首次探测由
// onMounted 里的 loadGitBranch() 负责，这里只响应后续 cwd / 空间变化。
watch(effectiveDir, loadGitBranch)

// ---- 「+ 技能和连接器」----
//
// 两组作用域不同：
//   - 技能是**这一条消息的指令**：选中即在输入框上方挂一枚胶囊（见 useAiSlashSkill），
//     发送时把名字数组带给后端。没有第二套「勾选状态」——胶囊本身就是唯一的选择记录。
//   - 连接器是**全局开关**（装着进程、占着工具名额），点一下即写 mcp.json。
const plusOpen = ref(false)
const skills = ref<AISkill[]>([])
const skillsDir = ref('')
const connectors = ref<AIConnector[]>([])

// ---- 执行权限（输入框右下角）----
//
// 只有两档：false = 默认权限（敏感操作逐个确认），true = 完全访问（不再问）。
// 不同「只对这条消息生效」的临时开关混着用：权限一旦临时化，用户就无法回答
// 「刚那条 rm 到底批准过没有」。它跟着会话走（ai_conversations.permission），
// 切换即落库，切回来看到的就是上次真正生效的那个。
const permissionFull = ref(false)

// 每次切换递增。回滚只认最新一次的结果——连点两次（false→true→false）时，
// 先发的请求失败后按「!full」回滚，会把界面写成与用户最新意图相反的值。
// 这是安全开关：界面写着「完全访问」而库里是默认档（或反之）比切不动更危险。
let permissionSeq = 0

/** 切换权限：有会话就立刻落库（界面标记必须和库一致），落地页则等第一句随请求带上。 */
async function onChangePermission(full: boolean) {
  permissionFull.value = full
  const id = activeId.value
  if (!id) return
  const seq = ++permissionSeq
  try {
    unwrap(await AISetConversationPermission(id, full ? 'full' : 'default'))
  } catch (e) {
    // 已经有更新的一次切换在飞：它的结果才代表用户最后的意图，这次失败不要插手。
    if (seq !== permissionSeq) return
    // 落库失败就退回原状：留在「界面写着完全访问、实际每条都在问」的状态里，
    // 比切不成功更让人困惑。
    permissionFull.value = !full
    toast.error(getErrorMessage(e))
  }
}

// ---- 隔离工作区（git worktree，见 AiWorktreeToggle 与后端 ai_worktree.go）----
//
// 状态分两层，别合并：
//   · worktreeEnabled / worktreeBranch —— 「这条消息要不要开、基于哪条分支」，
//     只在**还没开**时有意义，随发送带给后端。
//   · 会话上的 worktree_path —— 「这条任务已经在工作区里跑了」，由后端落库。
// 一旦第二条成立，后端后续每个轮次都复用同一个工作区，与开关无关
// （关开关不等于拆工作区，拆要走显式移除）。
const worktreeEnabled = ref(false)
const worktreeBranch = ref('')
const worktreeBranches = ref<string[]>([])
const worktreeBranchOpen = ref(false)

// activeConv 在下方（摘要区附近）已有一份定义，这里直接复用，别重复声明。
const worktreeActive = computed(() => !!(activeConv.value?.worktree_path || '').trim())
const worktreePath = computed(() => (activeConv.value?.worktree_path || '').trim())

async function loadWorktreeBranches() {
  const dir = effectiveDir.value.trim()
  if (!dir) { worktreeBranches.value = []; return }
  try {
    const data = unwrap(await AIGitBranches(dir)) as { branches?: string[] } | null
    worktreeBranches.value = data?.branches || []
  } catch (e) {
    // 拉不到分支只是下拉空着，不该打断输入
    logErr('AIPage:loadWorktreeBranches', e)
    worktreeBranches.value = []
  }
}

/** 勾选/取消 Worktree：勾上时顺手把分支预置成当前分支并拉一次列表。 */
function onWorktreeToggle(on: boolean) {
  worktreeEnabled.value = on
  worktreeBranchOpen.value = false
  if (on) {
    if (!worktreeBranch.value) worktreeBranch.value = gitBranch.value
    void loadWorktreeBranches()
  }
}

function toggleWorktreeBranch() {
  worktreeBranchOpen.value = !worktreeBranchOpen.value
  if (worktreeBranchOpen.value) void loadWorktreeBranches()
}

function pickWorktreeBranch(name: string) {
  worktreeBranch.value = name
  worktreeBranchOpen.value = false
}

/**
 * 移除这条任务的工作区。
 *
 * 后端在「有未提交改动」时默认拒绝，前端拿到拒绝后必须再问一次——工作区里可能
 * 是模型刚写了一半的代码，静默 force 删掉等于替用户做了「这些改动不要了」的决定。
 */
async function removeWorktree() {
  const id = activeId.value
  if (!id) return
  try {
    unwrap(await AIWorktreeRemove(id, false))
    toast.success(t('aiWorktreeRemoved'))
  } catch (e) {
    const msg = getErrorMessage(e)
    if (!(await toast.confirm(msg + '\n\n' + t('aiWorktreeForceConfirm')))) return
    try {
      const data = unwrap(await AIWorktreeRemove(id, true)) as { cwd?: string } | null
      if (data?.cwd) convCwd.value = data.cwd
      toast.success(t('aiWorktreeRemoved'))
    } catch (e2) {
      toast.error(getErrorMessage(e2))
      logErr('AIPage:removeWorktree', e2)
      return
    }
  }
  // 工作区没了，会话上的 cwd 与记录都变了：重拉列表让 chips 立刻回到「可再开」状态
  void loadConversations()
  worktreeEnabled.value = false
  worktreeBranch.value = ''
}

// 「/ 选技能」补全：斜杠只是输入方式，选中后变成输入框里的一枚行内胶囊。
const pickedSkills = ref<string[]>([])
const {
  slashItems, slashIdx, slashOpen, slashQuery, onSlashKey, stripSkillTokens,
} = useAiSlashSkill({
  // 同上：按「这一页在不在生成」判断，后台跑别的会话不该锁住这里的补全
  input, streaming: streamingHere, hasKey, skills,
  picked: pickedSkills,
  onPick: (sk) => onPickSkill(sk),
})

// 上一次真正发出去的技能名：失败重试时输入框与胶囊都已经清空，
// 靠它沿用上一次的选择（与 @ 引用同一口径，见 send()）。
const lastSkills = ref<string[]>([])

function applySkills(data: { dir?: string; skills?: AISkill[] } | null) {
  if (!data) return
  skillsDir.value = data.dir || skillsDir.value
  skills.value = data.skills || []
}

async function loadSkills() {
  try {
    applySkills(unwrap(await AIGetSkills()) as { dir?: string; skills?: AISkill[] } | null)
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// ---- + 面板「引用」：和手打 @ 走同一条链路 ----
//
// 这里只负责把「本对话里出现过的文件」找出来；选中后一律落到 mentions（chip），
// 发送时由后端按 path 读内容展开。前端绝不读文件、也绝不把文件名写进正文——
// 那样会绕过 fsguard 与行数预算，用户还看不出自己引用了什么。
const cwdMentionFiles = ref<AIMentionItem[]>([])

const convFiles = computed<AIMentionItem[]>(() => {
  const map = new Map<string, AIMentionItem>()
  const add = (kind: string, name: string, path: string) => {
    if (!path) return
    const key = kind + '|' + path.replace(/\\/g, '/')
    if (!map.has(key)) map.set(key, { kind: kind as AIMentionItem['kind'], name, path })
  }
  // AI 写过 / 改过的文件（含流式期）：刚生成的那个通常就是下一句要接着问的。
  for (const a of convArtifacts.value) add('file', a.name, a.path)
  // 之前 @ 引用过的：发出去后 chip 已清空，靠 lastMentions 把「老材料」留在手边。
  for (const m of lastMentions.value) if (m.kind !== 'conv') add(m.kind, m.name, m.path)
  // 当前已挂上的 chip：面板要能标「已引用」并支持再点一次摘掉。
  for (const m of mentions.value) if (m.kind !== 'conv' && m.path) add(m.kind, m.name, m.path)
  return [...map.values()]
})

const pickedFilePaths = computed(() =>
  mentions.value.filter((m) => m.kind !== 'conv' && m.path).map((m) => m.path!),
)

// 工作目录候选与 @ 补全同源（AISuggestMentions），只是把 prefix 留空一次取回，
// 交给面板本地过滤。不跟着搜索词反复发 RPC：列表在面板打开期间不会变，
// 真变了下次打开会重取。没有 cwd / 目录失效时静默降级，本对话那组照样能用。
async function loadMentionFiles() {
  cwdMentionFiles.value = []
  if (!convCwd.value) return
  try {
    cwdMentionFiles.value = unwrap<AIMentionItem[]>(await AISuggestMentions(convCwd.value, '')) || []
  } catch {
    // 与 useAiMention.loadMentionItems 同口径：这是状态不是错误，不弹提示。
  }
}

// 选中 = 挂一枚 chip；已挂上的再点一次摘掉（与技能胶囊同口径）。
// 用 addMention 而不是 pickMention：后者会删掉输入框末尾的 @token，
// 但从 + 面板点进来时用户压根没打 @，末尾可能是邮箱、路径等正文。
function onPickFile(it: AIMentionItem) {
  const i = mentions.value.findIndex(
    (m) => m.kind === it.kind && (m.path || '') === (it.path || ''),
  )
  if (i >= 0) removeMention(i)
  else addMention(it)
  plusOpen.value = false
  // 遮罩吃掉了这次点击的焦点，选完把光标还回输入框，接着就能打字。
  nextTick(() => (activeId.value ? composerRef.value : heroRef.value)?.focus())
}

// ---- 提示词库 ----
//
// 与技能是两套东西：技能插的是输入框里的胶囊（告诉模型「有这套路」），
// 提示词插的是一段可继续编辑的正文（用户自己的话术）。见 internal/db/prompt.go。
const prompts = ref<AIPrompt[]>([])

// 打开面板时才拉：用户刚在设置里加过一条，挂载时缓存一份就会显示不出来。
async function loadPrompts() {
  try {
    const data = unwrap(await AIGetPrompts()) as { prompts?: AIPrompt[] } | null
    prompts.value = data?.prompts || []
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('AIPage:loadPrompts', e)
  }
}

// 模板变量在插入那一刻展开：日期取今天、时间取此刻、选区取刚才选中的正文。
//
// 选区从富输入框取（takeSelectedText），不能直接读 window.getSelection()：
// 点 + 面板时焦点已经移到按钮上，浏览器把选区清空了，这里读到的永远是空串。
//
// 没有选区时保留 {{selection}} 字面量，而不是换成空串：静默变空等于模板少了一半，
// 用户会以为是提示词本身写漏了；留着占位符至少一眼看得见要补什么。
function expandPromptVars(content: string, selection: string): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  const date = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
  const time = `${p(d.getHours())}:${p(d.getMinutes())}`
  const sel = selection.trim()
  let out = content.replace(/\{\{date\}\}/g, date).replace(/\{\{time\}\}/g, time)
  if (sel) out = out.replace(/\{\{selection\}\}/g, sel)
  return out
}

// 选提示词 = 把正文填进输入框（普通文本，不是胶囊），收起面板并把焦点交回输入框，
// 用户接着补上下文就能发。
function onPickPrompt(p: AIPrompt) {
  plusOpen.value = false
  const target = activeId.value ? composerRef.value : heroRef.value
  if (!target) return
  target.insertPromptText(expandPromptVars(p.content, target.takeSelectedText()))
}

function managePrompts() {
  plusOpen.value = false
  emit('open-settings', 'ai')
}

// 外部 MCP 服务器：+ 菜单只要「名字 / 状态 / 工具数」。command 与 env 属于设置页，
// 摆进对话里的菜单只会变成一屏看不懂的配置。
function mapConnectors(raw: unknown): AIConnector[] {
  const list = Array.isArray(raw) ? raw : []
  return list.map((s) => {
    const o = s as {
      name?: string; status?: string; toolCount?: number; error?: string
      config?: { disabled?: boolean }
    }
    return {
      name: o.name || '',
      status: o.status || '',
      toolCount: o.toolCount || 0,
      disabled: !!o.config?.disabled || o.status === 'disabled',
      error: o.error || '',
    }
  })
}

async function loadConnectors() {
  try {
    connectors.value = mapConnectors(unwrap(await AIGetExtServers()))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 打开时才拉：技能目录与 mcp.json 都可能被用户在资源管理器里改过，
// 挂载时缓存一份，会在「刚拷进一个技能」时显示不出来。
async function togglePlus() {
  plusOpen.value = !plusOpen.value
  if (plusOpen.value) {
    // 两个弹层贴在同一个位置，重叠着谁也看不清：显示层用 `slashOpen && !plusOpen` 抑制，
    // 这里不用再动 slashItems（它已是只读 computed）。
    await Promise.all([loadSkills(), loadConnectors(), loadPrompts(), loadMentionFiles()])
    return
  }
  // 收起后把焦点还给输入框：面板外面罩着一层遮罩，用户点输入框那一下
  // 只会关掉面板、焦点落在 body 上，还得再点第二次才能真正打字。
  nextTick(() => (activeId.value ? composerRef.value : heroRef.value)?.focus())
}

// 第一次打 / 时技能清单可能还没加载过（用户没开过 + 面板）：补拉一次，
// 否则弹层永远是空的。扫描技能目录是磁盘 IO，不放在挂载时做。
watch(slashQuery, (q) => {
  if (q !== null && !skills.value.length) void loadSkills()
})


async function toggleConnector(c: AIConnector) {
  try {
    connectors.value = mapConnectors(unwrap(await AISetExtServerEnabled(c.name, c.disabled)))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 「安装技能」= 打开 skills 目录。安装方式就是「把一个技能目录拷进去」：
// 技能是纯文本说明，用户能先看清里面写了什么再决定装不装；
// 做成 zip 一键装反而让人不知道刚刚往系统里放了什么。
async function installSkill() {
  plusOpen.value = false
  try {
    if (!skillsDir.value) await loadSkills()
    if (!skillsDir.value) return
    unwrap(await RevealInExplorer(skillsDir.value))
    toast.success(t('aiInstallSkillHint'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

function manageConnectors() {
  plusOpen.value = false
  emit('open-settings', 'ai')
}

const heroRef = ref<InstanceType<typeof AiHero> | null>(null)
const composerRef = ref<InstanceType<typeof AiComposer> | null>(null)

// 选中技能 = 在输入框里插一枚行内胶囊（插在光标处）。光标由富输入框自己放回胶囊后面，
// 这里不再额外 focus()：那一下会把光标从胶囊后甩到末尾，正文长时看得见。
function onPickSkill(sk: AISkill) {
  const target = activeId.value ? composerRef.value : heroRef.value
  target?.insertSkill(sk.name)
}

// 输入框里的胶囊变了（插入 / 点 × / 退格整颗删 / 清空）：同步一份给面板标「已选」。
// 真源始终是 DOM 里的胶囊，这里只是投影，不要反过来去改它。
function onSkillsChange(names: string[]) {
  pickedSkills.value = names
}

// 落地页上的示例问题：点一下填进输入框，省得对着空白框想「能问什么」。
// 选项池比实际展示多（「多几个」），每次落地页出现都从池子里随机挑 4 个。
const HERO_TIP_POOL = [
  'aiHeroTip1', 'aiHeroTip2', 'aiHeroTip3', 'aiHeroTip4',
  'aiHeroTip5', 'aiHeroTip6', 'aiHeroTip7', 'aiHeroTip8',
  'aiHeroTip9', 'aiHeroTip10',
]
const HERO_TIPS_SHOWN = 4
const heroTips = ref<string[]>([])
function reshuffleHeroTips() {
  const pool = HERO_TIP_POOL
  const n = Math.min(HERO_TIPS_SHOWN, pool.length)
  const idxs = [...pool.keys()]
  for (let i = idxs.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1))
    ;[idxs[i], idxs[j]] = [idxs[j], idxs[i]]
  }
  heroTips.value = idxs.slice(0, n).map(i => pool[i])
}
// 首屏随机取一批；之后每次落地页重新出现（点「新建任务」回到空态）都再随机换一批。
reshuffleHeroTips()
// 「换一批」按钮：手动重新随机一批示例。
function shuffleHeroTips() { reshuffleHeroTips() }

// 空态 = 还没进入任何任务。此时主区渲染落地页而不是「空消息区 + 底部输入框」。
// 判的是 streamingHere（这一页在不在生成）而不是 streaming：后台还有一条会话在跑时，
// 用户点了「新建任务」就该看到落地页，而不是被那条会话的流式状态锁在消息区。
const isNewTask = computed(() =>
  !activeId.value && messages.value.length === 0 && !streamingHere.value)

// 落地页再次出现（点「新建任务」回到空态）时，open 模式再换一批示例。
watch(isNewTask, (v) => { if (v) reshuffleHeroTips() })

function openNewTask() {
  // 不打断正在生成的回答：新建任务和「切到别的会话」是同一件事——换一个空白视图，
  // 不是把上一轮砍掉。发送时如果那条还在跑，会给一句明确提示（见 send）。
  // 下拉要列当前还存在的空间：从回收站恢复/删掉空间之后，挂载时那份列表就过期了。
  void loadSpaces()
  newTaskMode.value = 'chat'
  newTaskSpace.value = ''
  newTaskDir.value = ''
  beginBlankTask()
  nextTick(() => heroRef.value?.focus())
}

// 点示例问题：填进输入框并聚焦，用户接着改两个字就能发。
// 不直接发送——示例只是起点，直接发出去常常不是用户真想问的那句话。
function fillHeroTip(key: string) {
  input.value = t(key)
  nextTick(() => heroRef.value?.focus())
}

// 回车发送（**调用方已确认是回车**，见侧栏 @keydown.enter 的重命名输入框）。
// 这里只挡输入法组合：中文/日文输入中按回车是「确认候选词」，而 Chromium（WebView2 同源）
// 在这个回车照样派发 keydown（isComposing=true / keyCode 229）。之前用
// `@keydown.enter.exact.prevent="send"`，结果是打拼音时一按回车就把半截中文发出去，
// 候选词反而没了。同理 Shift/Ctrl/Alt/Meta + 回车留给换行与快捷键。
function onEnterSend(e: KeyboardEvent, fn: () => void) {
  if (e.isComposing || e.keyCode === 229) return
  if (e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) return
  e.preventDefault()
  fn()
}

// 正文是否非空：技能已经是独立胶囊、不在正文里，所以这里只看正文（兜底剥残留 token）。
//
// 单独一个 `/` 不算正文：那是「我正在叫技能菜单」，不是我要说的话。
// 以前它会一路发出去，建出一条正文为空的任务，侧栏标题写着「/」，
// 每日记忆里也留下一串「用户：/」——没内容、还占着记忆注入的位置。
const canSendText = computed(() => !/^[\s/／]*$/.test(stripSkillTokens(input.value)))

// onComposerKey 是两个输入框（聊天区 / 落地页新建任务）唯一的 keydown 入口。
//
// 顺序即优先级：输入法组合 → / 弹层 → @ 弹层 → 回车发送。输入法组合必须最先挡，
// 原因见上面的 onEnterSend。
//
// `e.key !== 'Enter'` 必须排在 preventDefault 之前：AiRichInput 把**每个** keydown 都
// 抛上来（先给弹层找宿主），少了这条判定，字母数字这些普通按键会被一并吞掉。
//
// 不写成「两个监听器各算各的」（@keydown + @keydown.enter）：两个都会收到同一个
// 事件，谁先谁后取决于编译顺序，而「弹层先选中、发送后触发」就是一条发出去半截
// @xxx 的提问。一个函数里按优先级判定，顺序就是代码顺序。
function onComposerKey(e: KeyboardEvent, fn: () => void) {
  if (e.isComposing || e.keyCode === 229) return
  if (onSlashKey(e)) return
  if (onMentionKey(e)) return
  if (e.key !== 'Enter') return
  if (e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) return
  e.preventDefault()
  fn()
}

// 落地页（新建任务）输入框的 keydown 入口：直接复用聊天区那一条。
//
// 以前这里自己写了一遍判定，但漏了 `e.key !== 'Enter'`，于是任何普通按键（字母、数字）
// 都会被 preventDefault 掉——中文还能靠输入法组合上屏，纯 ASCII 的数字就直接打不进去，
// 表现成「新建任务输入框打不了数字」。同一条链只留一个入口，少一处判定就少一种漏。
function onHeroKey(e: KeyboardEvent) {
  onComposerKey(e, heroSend)
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
  if (!dir) {
    // 取消选择：把下拉退回「不挂空间」。否则它会停在「新建空间…」上却没有目录，
    // 用户看着是选中态，直到发送才被告知「请先选择目录」。
    if (newTaskSpace.value === NEW_SPACE) newTaskSpace.value = ''
    return
  }
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
  if (!hasKey.value) return
  // 正文为空（只有空白、或只有一个正在叫技能菜单的 `/`）：不该发出一条空消息
  if (!canSendText.value && !attachments.value.length) return
  // 从落地页发起后 hero 会卸载、composer 挂载，面板状态是同一份——
  // 不收起的话，一进对话就顶着一个没被要求的弹层。
  plusOpen.value = false
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
  // 后台可能还有别的任务在跑（用户新建任务前没有停它）：那些流各有各的 run，
  // 这里一个字节都碰不到。只把「当前这条」从草稿流上摘下来——否则上一条还没
  // 拿到会话 id 的新任务会继续替占着落地页，用户看不到刚点开的这张空白页。
  draftRunId.value = ''
  resetComposerState()
  pageError.value = ''
  // 落地页的报错只属于上一次那条草稿，别带到这一条上。
  setConvError('', '')
  // 回到落地页时必须是「干净的默认态」：沿用上一次选过的类型和空间，
  // 下一条消息就会莫名其妙落进另一个项目的目录里（删掉一条任务后尤其容易中招）。
  newTaskMode.value = 'chat'
  newTaskSpace.value = ''
  newTaskDir.value = ''
  // 工作目录也要清：convCwd 是「开着的那条会话」的目录，回到落地页就没人认它了。
  // 不清的代价很直观——落地页明明写着「会给这个任务单独建目录」，左边却挂着上一条
  // 任务那个项目的分支（effectiveDir 退到 convCwd 上了），工作区勾选、@ 引用也在按
  // 上一个项目算。清掉之后，落地页的目录只由「归属空间」决定，没选就是没有。
  convCwd.value = ''
  // 隔离工作区同理：上一条任务的勾选不该黏到这一条上；分支下拉列的是旧目录的分支，
  // 目录一换（或没有目录）就是错的，一并清干净。
  worktreeEnabled.value = false
  worktreeBranch.value = ''
  worktreeBranchOpen.value = false
  // 权限同理：上一条对话开了完全访问，新建任务不该继承——「刚开始是默认权限」
  // 是这条功能的默认承诺，忘了切回来正是它最危险的失效方式。
  permissionFull.value = false
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
  // 在跑的那条也可能就在这个空间里（用户已经切到别处看了）：同样要先掐。
  // 现在可能同时有好几条在跑，所以把所有落在这个空间里的都停掉。
  for (const r of runs.value.slice()) {
    if (conversations.value.some((c) => c.id === r.convId && (c.space_id || '') === s.id)) stop(r)
  }
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

// 清掉输入侧那些「属于上一条未发送草稿」的东西。
//
// 流式缓冲已经不用清了：它们挂在各自的 run 上（见 AIRun），run 结束即销毁。
// 这里只留真正会黏到下一条任务上的：@ 引用 chip 与它们的内容候选项。
function resetComposerState() {
  mentions.value = []
  mentionItems.value = []
}

async function delConv(id: string) {
  // 提示语点明「可恢复」：后端已经把它连消息一起拍进回收站，
  // 说「删除」会让用户以为再也找不回来，从而不敢清理侧栏。
  if (!(await toast.confirm(t('aiDelTaskConfirm')))) return
  // 删的正是当前在跑的会话：先掐流再删。否则后端会先发现「会话已被删除」
  // 而把内容写失败，前端还会抱着一条指向已删会话的流继续读。
  // 判「这条会话有没有在跑」而不是 activeId：那条可能正在后台跑（用户已经切到别处了）。
  // 可能还有另一条也在跑，所以只掐这一条。
  if (runOf(id)) stop(id)
  try {
    unwrap(await AIDeleteConversation(id))
    delete convRunState.value[id]
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
const retryPending = ref(false)

function canRetry(): boolean {
  if (runOf(activeId.value)) return false
  return messages.value.some((m) => m.role === 'user')
}

/**
 * 能不能「重新生成」上一条回答。
 *
 * 判据与 canRetry 不同：这里要求**最后一条是助手回复**。失败时最后一条是用户
 * 消息（回复没落库），那是「重试」的场合；已经有一条回答躺在那里、用户想让它
 * 重说一遍才是「重新生成」。所以两个判据不能合并——合并必有一种情况消失。
 */
const canRegenerate = computed(() => {
  if (!activeId.value || runOf(activeId.value)) return false
  const last = messages.value[messages.value.length - 1]
  return !!last && last.role === 'assistant' && messages.value.some((m) => m.role === 'user')
})

function retryLast() {
  if (!canRetry()) return
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const m = messages.value[i]
    if (m.role !== 'user') continue
    setConvError(activeId.value, '')
    pageError.value = ''
    retryPending.value = true
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
  if (!activeId.value || runOf(activeId.value)) return
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

// ---- 星标 ----
// 不做乐观更新：星标会改变侧栏分组（星标组在前），先翻转再回滚就是闪一下。
const starBusy = ref('')
const starredOpen = ref(true)

async function toggleStar(c: AIConversation) {
  if (starBusy.value) return
  starBusy.value = c.id
  const next = !c.starred
  try {
    unwrap(await AISetConversationStar(c.id, next))
    c.starred = next
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    starBusy.value = ''
  }
}

// 星标组：横切所有空间，所以单独一组放在最上面。
// 刻意不从原位置移走 —— 星标是「快捷入口」，不是「搬到另一个文件夹」，
// 任务仍然应该留在它自己的空间里，否则用户会以为它被挪走了。
const starredConvs = computed(() => {
  const q = convQuery.value.trim().toLowerCase()
  return conversations.value.filter((c) => c.starred && (!q || matchConv(c, q)))
})

// ---- 对话大纲 / 摘要 ----
// 大纲是纯前端的：从消息里挑出用户提问，做成可点的目录。
// 「几十轮之后不知道聊到哪了」不需要模型来回答 —— 用户自己的问题就是最好的目录，
// 而且它零成本、即时、永远和当前消息一致。
const digestOpen = ref(false)
const digestBusy = ref(false)

// ---- 后台任务面板 ----
// 徽标要回答的是「现在还有东西在跑吗」，而面板可能一直没打开过，
// 所以数量单独轮询（20s，本地 sqlite 查询，成本可以忽略）。
// 面板自己按 2.5s 轮询明细，两者不共享定时器：面板关掉后徽标还得继续更新。
const jobsOpen = ref(false)
/**
 * 正在跑的后台任务。面板按 2.5s 拉自己的明细，这里这份只服务两个地方：
 * 工具栏徽标的数字，以及输入框上方那条「后台运行中」横条。
 *
 * 存整个列表而不只是数量：横条要显示「跑的是哪条命令、已经多久」，
 * 只留 count 的话横条只能喊一句「有任务在跑」，用户还是得点开面板才认得出是哪个。
 */
const runningJobList = ref<AIJobRow[]>([])
const runningJobs = computed(() => runningJobList.value.length)
/**
 * 全部最近任务（含已结束）：消息卡片要把 run_command 行从「后台任务 job_xxx」
 * 翻成「运行中 / 已结束 · 退出码 1」，只靠 running 那份不够——任务一结束
 * 整行就从列表里消失，卡片会一直停在最后一次看到的状态。
 */
const jobRows = ref<Map<string, AIJobRow>>(new Map())
/** jobRows 的版本号。Map 原地替换不会让 v-memo 失效，消息列表靠这个数字重渲染 */
const jobSeq = ref(0)
/** 面板要定位的任务 id：从消息卡片的「查看」进来时带上 */
const jobsFocus = ref('')
let jobsBadgeTimer: ReturnType<typeof setTimeout> | undefined
/** 横条上的「已运行 3m12s」靠它每秒重算；没有任务在跑时定时器停掉 */
const nowTick = ref(Date.now())
let nowTickTimer: ReturnType<typeof setInterval> | undefined

async function refreshJobBadge() {
  try {
    const list = unwrap<AIJobRow[]>(await AIJobsList()) || []
    const map = new Map<string, AIJobRow>()
    for (const j of list) map.set(j.id, j)
    jobRows.value = map
    jobSeq.value++
    runningJobList.value = list
      .filter((j) => j.running)
      // 最近起的排最前：横条只放得下一条，用户关心的通常是刚点下去的那个
      .sort((a, b) => (Date.parse(b.startedAt) || 0) - (Date.parse(a.startedAt) || 0))
  } catch {
    // 徽标只是提示，查不到就保持上一次的结果，不打扰用户
  }
}

/** 消息卡片上的「查看」：开面板并定位到那条任务 */
function openJobsAt(id: string) {
  jobsFocus.value = id
  jobsOpen.value = true
  void refreshJobBadge()
}

/**
 * 轮询节奏跟着「有没有任务在跑」走：跑着的时候 3s（横条上的时长与「已经停了」都要跟得上），
 * 全空时 20s。用 setTimeout 自排而不是 setInterval：间隔要在每次查询后重算。
 */
function scheduleJobBadge() {
  if (jobsBadgeTimer) clearTimeout(jobsBadgeTimer)
  const delay = runningJobs.value ? 3000 : 20000
  jobsBadgeTimer = setTimeout(async () => {
    await refreshJobBadge()
    scheduleJobBadge()
  }, delay)
}

watch(runningJobs, (n) => {
  if (n > 0 && !nowTickTimer) {
    nowTick.value = Date.now()
    nowTickTimer = setInterval(() => { nowTick.value = Date.now() }, 1000)
  } else if (!n && nowTickTimer) {
    clearInterval(nowTickTimer)
    nowTickTimer = undefined
  }
})

/** 输入框上方那条横条的内容（没有运行中的任务时为 null） */
const jobBar = computed(() => {
  const first = runningJobList.value[0]
  if (!first) return null
  const started = Date.parse(first.startedAt) || nowTick.value
  return {
    command: first.command,
    more: runningJobList.value.length - 1,
    elapsed: formatDuration(Math.max(0, nowTick.value - started)),
  }
})

function toggleJobs() {
  jobsOpen.value = !jobsOpen.value
  if (jobsOpen.value) void refreshJobBadge()
}

// 两个浮层共用同一个右上角位置：同时打开时后开的会压在先开的上面，
// 用户点关闭关掉的是上面那个，下面那个还在——看起来像「关不掉」。
watch(digestOpen, (v) => {
  if (v) jobsOpen.value = false
})
watch(jobsOpen, (v) => {
  if (v) digestOpen.value = false
})

const outlineItems = computed<OutlineItem[]>(() =>
  messages.value
    .filter((m) => m.role === 'user')
    .map((m) => {
      // 取第一行非空文本并压掉 markdown 记号：目录要能一眼扫完，
      // 不是把原消息再抄一遍。
      const line =
        (m.content || '')
          .split('\n')
          .map((l) => l.trim())
          .find((l) => l) || ''
      return { id: m.id, text: line.replace(/^[#>*\-\d.\s]+/, '').slice(0, 80) }
    })
    .filter((it) => it.text),
)

// 大纲里点一条：跳转本身归 AiMessageList（滚动容器在它手里），
// 跳成功才收起大纲——找不到那条消息（比如已被截断）时面板留在原地才有意义。
function jumpToMsg(id: string) {
  if (msgListRef.value?.jumpToMsg(id)) digestOpen.value = false
}

const activeConv = computed(() => conversations.value.find((c) => c.id === activeId.value) || null)

async function runDigest() {
  if (!activeId.value || digestBusy.value || runOf(activeId.value)) return
  digestBusy.value = true
  try {
    const text = unwrap<string>(await AISummarizeConversation(activeId.value))
    const i = conversations.value.findIndex((c) => c.id === activeId.value)
    // unwrap 的返回类型带 null（后端可能给空 data），而 summary 是非可空字符串
    if (i >= 0 && text) conversations.value[i].summary = text
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    digestBusy.value = false
  }
}

// ---- 导出 Markdown ----
// 导出当前任务的全部消息。工具调用只留「调了哪几个」一行：完整参数是执行细节，
// 复制出去给别人看时属于噪音，需要的话库里和界面上都还在。
function exportConvMarkdown() {
  const c = activeConv.value
  if (!c) return
  const lines: string[] = []
  lines.push(`# ${c.title || t('aiNewChat')}`, '')
  const meta: string[] = []
  if (c.updated_at) meta.push(`${t('aiUpdatedAt')}: ${c.updated_at}`)
  if (c.cwd) meta.push(`${t('aiCwdLabel')}: ${c.cwd}`)
  meta.push(`${t('aiMsgCount')}: ${messages.value.length}`)
  lines.push(`> ${meta.join(' · ')}`, '')
  if (c.summary) lines.push(`**${t('aiDigestTitle')}**`, '', c.summary, '')
  for (const m of messages.value) {
    const who = m.role === 'user' ? t('aiYou') : 'AI'
    const head = [who, m.model, m.created_at].filter(Boolean).join(' · ')
    lines.push(`## ${head}`, '')
    if (m.imgList?.length) lines.push(`*（${t('aiImgCount', { n: m.imgList.length })}）*`, '')
    lines.push((m.content || '').trim() || '_（空）_', '')
    const tools = toolList(m)
    if (tools.length) {
      lines.push(`> ${t('aiToolCallsN', { n: tools.length })}: ${tools.map((x) => x.name).join(', ')}`, '')
    }
  }
  const stamp = (c.updated_at || '').slice(0, 10) || new Date().toISOString().slice(0, 10)
  downloadText(`${safeFileName(c.title || t('aiNewChat'))}-${stamp}.md`, lines.join('\n'), 'text/markdown')
  toast.success(t('aiExportMdOk'))
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
// 用时格式化：按量级分段，避免长任务一直堆「几百秒」不直观。
//   <1s   → 「540ms」（短暂任务，精确）
//   <1min → 「54s」（匹配截图「已处理 54s」）
//   <1h   → 「5m12s」（分:秒，实时每秒跳）
//   否则  → 「1h20m」
function formatDuration(ms: number): string {
  const totalSec = Math.max(0, Math.floor(ms / 1000))
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`
  if (totalSec < 60) return `${totalSec}s`
  const min = Math.floor(totalSec / 60)
  const sec = totalSec % 60
  if (min < 60) return `${min}m${sec.toString().padStart(2, '0')}s`
  const hr = Math.floor(min / 60)
  const mLeft = min % 60
  return `${hr}h${mLeft.toString().padStart(2, '0')}m`
}

// —— 流式状态机 ——
//
// send / 停止 / 运行中插入 / 收尾固化都在 useAiRuns 里（P4 从本文件搬出去，
// 原先占着下面七百多行）。runs 与 activeRun / streaming 留在这一页：模板与
// 输入框的 streaming 判断在 setup 阶段就要读它们，绕出去再传回来反而更绕。
const {
  send,
  stop,
  pendingInjectsHere,
  rushInject,
} = useAiRuns({
  activeId, conversations, messages, runs, draftRunId,
  input, attachments, mentions, lastMentions, pickedSkills, lastSkills, stripSkillTokens,
  hasKey, mode, permissionFull, worktreeEnabled, worktreeActive, worktreeBranch,
  pendingSpaceId, convCwd,
  streamModelOf: () => aiProfiles.value.find((x) => x.id === aiActive.value)?.model || '',
  planCards, statMap, setConvError, markConvRunning, markConvFinished,
  loadConversations, loadCheckpoints, refreshJobBadge,
  renderLiveBlocks, primeMessageBlocks, clearLiveCache,
  scrollToBottom, followBottom, scheduleMermaidScan,
  ensureStreamInfo, aiStreamPort, aiStreamToken,
  toast, t, openSettings: () => emit('open-settings', 'ai'), retryPending, pageActive, plusOpen,
})

// ---- 保活（KeepAlive）后的可见性 ----
// 本页被 App 的 KeepAlive 白名单留住：切到别的导航页时组件只是 deactivate，
// 正在进行的 fetch 流和已生成的文字都还在（以前是销毁 → onUnmounted 里 abort →
// 回答整段消失）。代价是用户在别的页看不到任何迹象，所以把状态外抛给侧栏：
// 生成中 = 转圈，已生成且用户还没回来 = 未读点。
// pageActive 的定义在文件前部：useAiRuns 的 deps 里要用它（见下方调用点）。
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
  // 回来时正看着的那条就算「已经看过了」：它的点是在别的页面期间攒下来的
  if (activeId.value) delete convRunState.value[activeId.value]
  reportAIPageVisible(true)
  // 回来时停在原位会看不到后面生成的内容：流式期间一直是贴底的，直接拉到底
  if (streamingHere.value) nextTick(scrollToBottom)
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
  const container = msgListRef.value?.areaEl() || null
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
  // Ctrl+Shift(B)：开合右侧面板（与常见 AI 工作台同快捷键）
  if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === 'b') {
    e.preventDefault()
    toggleSidePanel()
    return
  }
  if (e.key !== 'Escape') return
  // 输入法正在组词时 Esc 是「取消候选」，不是「停止任务」。
  // 中文用户按 Esc 收候选框的瞬间把跑了几分钟的回答掐掉，代价远大于收益；
  // keyCode 229 是部分 IME（Chrome + 微软拼音）在 keydown 里给的值，
  // isComposing 在部分场景下是 false，两个都得判。
  const composing = e.isComposing || e.keyCode === 229
  // 顺序不能反：先关最上层的东西，都关完了才轮到停任务——
  // 否则「Esc 关预览」会顺手把正在生成的回答停掉。
  if (imgViewer.value) closeImgViewer()
  else if (htmlPreview.value) closeHtmlPreview()
  else if (ctxTarget.value) closeCtxMenu()
  else if (!composing && streamingHere.value) stop()
}

onMounted(async () => {
  // KeepAlive 首次挂载也会走 onActivated，但那条路依赖 App.vue 的包裹结构；
  // 这里显式再置一次，保证「一进来就停在 AI 页」这个事实后端一定知道。
  reportAIPageVisible(true)
  // 预热编辑器 chunk：CM 核心 + 封装是动态 import 的，第一次开文件时才下载会明显
  // 卡一下「执行中」。空闲时先取回来，等真点开产物就只剩挂载耗时。
  window.setTimeout(() => { void import('../utils/aiCodeEditor').catch(() => { /* 预热失败无所谓，点开时会重试 */ }) }, 1200)
  await loadProfiles()
  await Promise.all([loadConversations(), loadSpaces()])
  // 后台任务徽标：进页面先对一次（可能有上一次会话留下的任务还在跑），
  // 之后按「有没有任务在跑」自适应轮询（见 scheduleJobBadge）。
  // 面板打开时它自己按 2.5s 轮询明细，两者互不依赖。
  void refreshJobBadge()
  scheduleJobBadge()
  // 会话/空间目录此时已就绪：首次探测 git 分支（effectiveDir 的 watch 已在其定义处
  // 建立，见「新建空间」声明之后的注释）。这里只跑一次首探，后续变化由那条 watch 接管。
  loadGitBranch()

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
    // 星标 / 摘要同样回写本地：后端是同一次调用改的，若不跟着刷，
    // 侧栏分组与摘要卡片会停在旧值，直到下次整表刷新。
    {
      const idx = conversations.value.findIndex((c) => c.id === data.id)
      if (idx >= 0) {
        if (data.starred !== undefined) conversations.value[idx].starred = !!data.starred
        if (data.summary !== undefined) conversations.value[idx].summary = String(data.summary)
      }
    }
  })
  // 设置在 SettingsModal 中保存配置后刷新下拉列表
  Events.On('ai:profiles-updated', loadProfiles)
  // 点击消息区之外时隐藏划词复制浮层
  document.addEventListener('mousedown', onDocMouseDown, true)
  // 图片查看器的 Esc 关闭（capture 不必要，普通冒泡即可；避免干扰输入框内 Esc 行为的判断）
  document.addEventListener('keydown', onGlobalKeydown)

})

onUnmounted(() => {
  Events.Off('ai:conv')
  Events.Off('ai:profiles-updated')
  document.removeEventListener('mousedown', onDocMouseDown, true)
  document.removeEventListener('keydown', onGlobalKeydown)
  // 这里的 abort 现在只会在「应用退出」时跑到：切到别的导航页是 deactivate，
  // 组件还活着，流照常继续（见下方 onActivated / onDeactivated）。
  for (const r of runs.value) {
    r.ctrl?.abort()
    if (r.flushTimer) clearTimeout(r.flushTimer)
  }
  if (msgSearchTimer) { clearTimeout(msgSearchTimer); msgSearchTimer = undefined }
  if (jobsBadgeTimer) { clearTimeout(jobsBadgeTimer); jobsBadgeTimer = undefined }
  if (nowTickTimer) { clearInterval(nowTickTimer); nowTickTimer = undefined }
  aiGenerating.value = false
  aiFinishedUnread.value = false
  reportAIPageVisible(false)
})
</script>

<template>
  <div class="ai-page">
    <!-- 左侧会话列表 -->
    <AiConversationSidebar
      v-model:conv-query="convQuery"
      v-model:starred-open="starredOpen"
      v-model:msg-hits-open="msgHitsOpen"
      v-model:tasks-open="tasksOpen"
      v-model:tasks-expanded="tasksExpanded"
      v-model:spaces-open="spacesOpen"
      v-model:spaces-expanded="spacesExpanded"
      v-model:rename-text="renameText"
      v-model:space-name-text="spaceNameText"
      :conversations="conversations"
      :spaces="spaces"
      :active-id="activeId"
      :run-state="convRunState"
      :starred-convs="starredConvs"
      :msg-hits="msgHits"
      :msg-hits-loading="msgHitsLoading"
      :task-conversations="taskConversations"
      :shown-task-conversations="shownTaskConversations"
      :shown-spaces="shownSpaces"
      :visible-spaces="visibleSpaces"
      :expanded-space-tasks="expandedSpaceTasks"
      :total-matched="totalMatched"
      :renaming-id="renamingId"
      :renaming-space-id="renamingSpaceId"
      :fold-tasks="FOLD_TASKS"
      :fold-spaces="FOLD_SPACES"
      :fold-space-tasks="FOLD_SPACE_TASKS"
      :open-new-task="openNewTask"
      :select-conv="selectConv"
      :conv-dir="convDir"
      :open-dir="openDir"
      :copy-path="copyPath"
      :mode-icon="modeIcon"
      :toggle-star="toggleStar"
      :del-conv="delConv"
      :del-space="delSpace"
      :new-task-in-space="newTaskInSpace"
      :start-rename="startRename"
      :cancel-rename="cancelRename"
      :commit-rename="commitRename"
      :start-rename-space="startRenameSpace"
      :cancel-rename-space="cancelRenameSpace"
      :commit-rename-space="commitRenameSpace"
      :is-space-expanded="isSpaceExpanded"
      :toggle-space="toggleSpace"
      :space-tasks-shown="spaceTasksShown"
      :toggle-space-tasks="toggleSpaceTasks"
      :on-enter-send="onEnterSend"
      :fmt-tokens="fmtTokens"
    />

    <!-- 右侧聊天区 -->
    <section class="ai-main">
      <!-- 工具栏：只在有任务时出现。落地页不需要它（模型选择在输入框下面，
           目录还没定）。 -->
      <AiToolbar
        v-if="!isNewTask"
        v-model:ai-active="aiActive"
        v-model:digest-open="digestOpen"
        :conv-cwd="convCwd"
        :cwd-label="cwdLabel"
        :ai-profiles="aiProfiles"
        :active-id="activeId"
        :active-conv="activeConv"
        :star-busy="starBusy"
        :side-open="sideOpen"
        :jobs-open="jobsOpen"
        :running-jobs="runningJobs"
        @open-cwd="openCwd"
        @model-change="onModelChange"
        @regen-title="regenTitle"
        @toggle-star="toggleStar"
        @export-md="exportConvMarkdown"
        @toggle-side="toggleSidePanel"
        @toggle-jobs="toggleJobs"
      />

      <!-- 后台任务面板：与大纲面板同一套浮层，挂在 .ai-main 上（相对定位）。 -->
      <AiJobsPanel
        v-if="jobsOpen"
        :open="jobsOpen"
        :focus="jobsFocus"
        @close="jobsOpen = false; jobsFocus = ''"
        @changed="refreshJobBadge"
      />

      <!-- 大纲 / 摘要：绝对定位挂在 .ai-main 上（它已是 position: relative）。
           不做成模态框——它只是「翻一下聊到哪了」，看完点目录里的一条就该跳过去。 -->
      <AiDigestPanel
        v-if="digestOpen && activeId"
        :summary="activeConv?.summary || ''"
        :busy="digestBusy"
        :streaming="streamingHere"
        :outline="outlineItems"
        @close="digestOpen = false"
        @run="runDigest"
        @jump="jumpToMsg"
      />

      <!-- 空态落地页：新建任务不走弹窗，直接在这一页选类型、选空间、提问。
           类型与空间仍是这条任务的属性，首次发送时随请求落库。 -->
      <AiHero
        v-if="isNewTask"
        ref="heroRef"
        v-model:new-task-mode="newTaskMode"
        v-model:url-draft="urlDraft"
        v-model:input="input"
        v-model:new-task-space="newTaskSpace"
        v-model:ai-active="aiActive"
        :modes="modes"
        :url-input-open="urlInputOpen"
        :attachments="attachments"
        :mentions="mentions"
        :mention-open="mentionOpen"
        :mention-items="mentionItems"
        :mention-idx="mentionIdx"
        :has-key="hasKey"
        :spaces="spaces"
        :new-space="NEW_SPACE"
        :ai-profiles="aiProfiles"
        :allow-file-image="allowFileImage"
        :allow-url-image="allowUrlImage"
        :git-repo="gitRepo"
        :git-branch="gitBranch"
        :out-hint="newTaskOutHint"
        :hero-tips="heroTips"
        :plus-open="plusOpen"
        :skills="skills"
        :connectors="connectors"
        :prompts="prompts"
        :picked-skills="pickedSkills"
        :files="convFiles"
        :cwd-files="cwdMentionFiles"
        :picked-files="pickedFilePaths"
        :slash-open="slashOpen && !plusOpen"
        :slash-items="slashItems"
        :slash-idx="slashIdx"
        :can-send="canSendText"
        :permission-full="permissionFull"
        :worktree-active="worktreeActive"
        :worktree-path="worktreePath"
        :worktree-branch="worktreeBranch || gitBranch"
        :worktree-enabled="worktreeEnabled"
        :worktree-branches="worktreeBranches"
        :worktree-branch-open="worktreeBranchOpen"
        @update:worktree-enabled="onWorktreeToggle"
        @toggle-worktree-branch="toggleWorktreeBranch"
        @pick-worktree-branch="pickWorktreeBranch"
        @remove-worktree="removeWorktree"
        @model-change="onModelChange"
        @change-permission="onChangePermission"
        @key="onHeroKey"
        @send="heroSend"
        @paste="onInputPaste"
        @drop="onInputDrop"
        @confirm-url="confirmUrlImage"
        @remove-attachment="removeAttachment"
        @remove-mention="removeMention"
        @pick-mention="pickMention"
        @pick-dir="pickNewTaskDir"
        @open-file-picker="openFilePicker"
        @toggle-url="toggleUrlInput"
        @fill-tip="fillHeroTip"
        @shuffle-tips="shuffleHeroTips"
        @open-settings="emit('open-settings', 'ai')"
        @toggle-plus="togglePlus"
        @pick-skill="onPickSkill"
        @pick-prompt="onPickPrompt"
        @pick-file="onPickFile"
        @manage-prompts="managePrompts"
        @update:skills="onSkillsChange"
        @toggle-connector="toggleConnector"
        @install-skill="installSkill"
        @manage-connectors="manageConnectors"
      />

      <template v-else>
      <!-- 消息区 -->
      <!-- 本轮实时用时状态条：计时自己走（见 AiRoundBar），父组件不再持有
           每 200ms 变一次的值——否则一次跳动就要重渲染整页。 -->
      <AiRoundBar
        v-if="streamingHere && activeRun"
        :key="activeRun.rid"
        :started-at="activeRun.startedAt"
      />
      <AiMessageList
        ref="msgListRef"
        :has-key="hasKey"
        :messages="messages"
        :stat-map="statMap"
        :locale="locale"
        :mode="mode"
        :runs="runs"
        :active-id="activeId"
        :draft-run-id="draftRunId"
        :error-msg="errorMsg"
        :can-retry="canRetry"
        :can-regenerate="canRegenerate"
        :render-block="renderBlock"
        :render-message-blocks="renderMessageBlocks"
        :msg-stat="msgStat"
        :format-stat="formatStat"
        :cp-for-call="cpForCall"
        :job-rows="jobRows"
        :job-seq="jobSeq"
        @open-settings="emit('open-settings', 'ai')"
        @open-artifact="openArtifactInPanel"
        @open-img="openImgViewer"
        @copy="copy"
        @save-note="saveAsNote"
        @restore-checkpoint="restoreCheckpoint"
        @open-jobs="openJobsAt"
        @run-plan="runPlan"
        @retry="retryLast"
        @regenerate="retryLast"
        @msg-click="onMsgClick"
        @msg-mouseup="onMsgMouseUp"
        @msg-contextmenu="onMsgContextMenu"
      />

      <!-- 「回到底部」按钮由 AiMessageList 自己渲染：它跟着消息容器的滚动状态走，
           放在父组件里要靠事件回传，反而多一层状态。 -->

      <!-- 划词复制浮层（Teleport 到 body，floating-ui 负责定位与防溢出） -->
      <Teleport to="body">
        <button
          v-if="selCtx?.visible.value"
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
          v-if="ctxMenu?.visible.value"
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

      <!-- 输入停靠区：审批卡以浮层盖在输入框上。
           不做成流内元素有两个原因：① 会话在这期间是停住的，按钮滚出视野就等于卡死；
           ② 待审时输入框本来就不可用，直接盖上去既省地方，也让「现在只能做这个选择」
           一目了然。z-index 高于 composer 内的 @ 补全 / + 面板，避免被它们压住。 -->
      <div class="ai-dock">
        <AiApprovalBar
          v-if="pendingApproval && streamingHere"
          class="ai-dock-approve"
          :call="pendingApproval"
          :count="pendingApprovalCount"
          @allow="approveTool(pendingApproval, 'allow')"
          @remember="approveTool(pendingApproval, 'allow', true)"
          @deny="approveTool(pendingApproval, 'deny')"
          @open-rules="emit('open-settings', 'ai')"
        />

        <!-- 任务清单：AI 自己把大任务拆成步骤，完成一步勾一步（plan_update 工具）。
             放在输入框正上方与后台任务条同一带：它是「当前这件事的进度」，
             不属于任何一条消息，混进消息流会在下一轮里变成一条看不懂的旧记录。 -->
        <div v-if="planHere" class="ai-plan-card">
          <div class="ai-plan-head">
            <span class="ai-plan-title">{{ t('aiPlanTitle') }}</span>
            <span class="ai-plan-count">{{ t('aiPlanCount', { done: planDoneCount(planHere), total: planHere.items.length }) }}</span>
          </div>
          <ul class="ai-plan-list">
            <li v-for="(it, i) in planHere.items" :key="i" class="ai-plan-item" :class="`is-${it.status}`">
              <span class="ai-plan-box">
                <Check v-if="it.status === 'done'" :size="10" />
                <span v-else-if="it.status === 'doing'" class="ai-plan-doing" />
              </span>
              <span class="ai-plan-text">{{ it.text }}</span>
            </li>
          </ul>
          <div v-if="planHere.note" class="ai-plan-note">{{ planHere.note }}</div>
        </div>

        <!-- 后台任务横条：贴在输入框正上方。
             为什么放这里：后台任务最常见的用法就是「让它在后台跑着，我继续问别的」，
             而它一旦跑起来，除了工具栏那个小终端图标就没别的地方能看见——用户会以为
             服务没起来。这条横条把它放到视线落点上：跑的是哪条命令、跑了多久、一共几个。
             点开就是后台任务面板（日志 / 终止都在那里）。 -->
        <button v-if="jobBar" class="ai-jobs-bar" :title="t('aiJobsBarTip')" @click="toggleJobs">
          <span class="ai-jobs-bar-dot" />
          <span class="ai-jobs-bar-label">{{ t('aiJobsBarRunning') }}</span>
          <code class="ai-jobs-bar-cmd" :title="jobBar.command">{{ jobBar.command }}</code>
          <span class="ai-jobs-bar-meta">
            {{ jobBar.elapsed }}<template v-if="jobBar.more > 0"> · {{ t('aiJobsBarMore', { n: jobBar.more }) }}</template>
          </span>
        </button>

        <!-- 等待插入的消息：后端回执（inject_ack）之前这句话谁都还没看到，
             必须有这个落点——否则用户会以为没发出去，然后连按几次回车。 -->
        <div v-if="pendingInjectsHere.length" class="ai-inject-queue">
          <div v-for="p in pendingInjectsHere" :key="p.id" class="ai-inject-item">
            <span class="ai-inject-dot" />
            <span class="ai-inject-text" :title="p.text">{{ p.text }}</span>
            <span class="ai-inject-hint">{{ t('aiInjectWaiting') }}</span>
            <!-- 不等模型写完手上的这段：一句话打断，让它马上接这句话。 -->
            <button
              class="ai-inject-rush"
              :disabled="p.rushed"
              :title="t('aiInjectSendNowTip')"
              @click="rushInject(p)"
            >{{ p.rushed ? t('aiInjectRushing') : t('aiInjectSendNow') }}</button>
          </div>
        </div>

        <!-- 输入区 -->
        <AiComposer
        ref="composerRef"
        v-model:input="input"
        v-model:url-draft="urlDraft"
        :hooks="aiHooks"
        :attachments="attachments"
        :url-input-open="urlInputOpen"
        :mentions="mentions"
        :mention-open="mentionOpen"
        :mention-items="mentionItems"
        :mention-idx="mentionIdx"
        :has-key="hasKey"
        :streaming="streamingHere"
        :allow-file-image="allowFileImage"
        :allow-url-image="allowUrlImage"
        :git-repo="gitRepo"
        :git-branch="gitBranch"
        :plus-open="plusOpen"
        :skills="skills"
        :connectors="connectors"
        :prompts="prompts"
        :picked-skills="pickedSkills"
        :files="convFiles"
        :cwd-files="cwdMentionFiles"
        :picked-files="pickedFilePaths"
        :slash-open="slashOpen && !plusOpen"
        :slash-items="slashItems"
        :slash-idx="slashIdx"
        :can-send="canSendText"
        :permission-full="permissionFull"
        :worktree-active="worktreeActive"
        :worktree-path="worktreePath"
        :worktree-branch="worktreeBranch || gitBranch"
        :worktree-enabled="worktreeEnabled"
        :worktree-branches="worktreeBranches"
        :worktree-branch-open="worktreeBranchOpen"
        @update:worktree-enabled="onWorktreeToggle"
        @toggle-worktree-branch="toggleWorktreeBranch"
        @pick-worktree-branch="pickWorktreeBranch"
        @remove-worktree="removeWorktree"
        @change-permission="onChangePermission"
        @approve-hooks="approveHooks"
        @remove-attachment="removeAttachment"
        @confirm-url="confirmUrlImage"
        @remove-mention="removeMention"
        @pick-mention="pickMention"
        @composer-key="onComposerKey($event, send)"
        @send="send"
        @stop="stop"
        @paste="onInputPaste"
        @drop="onInputDrop"
        @open-file-picker="openFilePicker"
        @toggle-url="toggleUrlInput"
        @toggle-plus="togglePlus"
        @pick-skill="onPickSkill"
        @pick-prompt="onPickPrompt"
        @pick-file="onPickFile"
        @manage-prompts="managePrompts"
        @update:skills="onSkillsChange"
        @toggle-connector="toggleConnector"
        @install-skill="installSkill"
        @manage-connectors="manageConnectors"
        />
      </div>
      </template>
    </section>

    <AiSidePanel
      ref="sidePanelRef"
      :open="sideOpen"
      :artifacts="convArtifacts"
      @update:open="sideOpen = $event"
    />
    <input ref="fileInputEl" type="file" accept="image/*" multiple hidden @change="onPickFiles" />
    <Teleport to="body">
      <div v-if="imgViewer" class="ai-img-viewer" @click="closeImgViewer">
        <img :src="imgViewer.dataUrl || imgViewer.url || ''" :alt="imgViewer.name" @click.stop />
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
/* 这里只剩「外壳」：整页布局、主区、以及三个 Teleport 到 body 的浮层与图片查看器。
   消息区 / 落地页 / 输入区 / 工具栏 / 大纲面板都已经拆成子组件，样式跟着模板走
   （scoped 只作用于本组件的元素，留在父组件里的规则对子组件的元素一律不生效）。 */
.ai-page {
  flex: 1;
  display: flex;
  min-width: 0;
  height: 100%;
  overflow: hidden;
}

/* 主区：position:relative 是两个浮层（大纲面板）与「回到底部」按钮的定位锚点，
   拆出去的子组件靠 display:contents 让内容继续参与这里的布局。 */
.ai-main { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; position: relative; }

/* 输入停靠区：审批卡相对它定位，才能「盖在输入框上」而不是把输入框往下顶。
   flex-shrink:0 必须有——消息区是 flex:1，输入区一旦可收缩，长对话里它会被压扁。 */
/* display:flex 是为了含住输入框自己的 12px 下边距——普通块流里它会与父级塌陷，
   导致 .ai-dock 的实际底边比输入框底边高，审批卡就贴不准输入框下沿。
   左右 16px 内边距与 .ai-hero 对齐：落地页与对话页的输入框不仅同宽，
   左边缘也要在同一条竖线上，否则切页面时输入框整体平移，比宽度变化更扎眼。 */
.ai-dock {
  position: relative; flex-shrink: 0; display: flex; flex-direction: column;
  padding: 0 var(--space-6);
}

/* 与输入框同宽同底边：输入框是 width: var(--ai-column-w) + margin auto。
   绝对定位元素的 100% 是父级的 padding box，比 content box 多左右各一份 padding，
   所以这里要减掉两次 --space-6（.ai-dock 的左右内边距）。
   原来写 left/right:10% 是专门配 80% 的算法，输入框一换口径就立刻错位。 */
/* 后台任务横条：与输入框同宽同左边（--ai-column-w + margin auto），
   一条浅底的行内按钮，不加边框——它是状态提示，不是一张卡片。
   高度刻意压到与等待插入那条一致，两条同时出现时不会一厚一薄。 */
.ai-jobs-bar {
  display: flex;
  align-items: center;
  gap: 7px;
  width: var(--ai-column-w);
  min-width: 0;
  margin: 0 auto 6px;
  padding: 5px 10px;
  border: none;
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  font-family: inherit;
  font-size: 11.5px;
  color: var(--color-text-secondary);
  text-align: left;
  cursor: pointer;
}

.ai-jobs-bar:hover {
  background: var(--color-bg-hover);
}

/* 呼吸点：静态圆点会被读成「一个图标」，这一条要说的是「此刻还在跑」 */
.ai-jobs-bar-dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-accent);
  animation: ai-jobs-bar-pulse 1.2s ease-in-out infinite;
}

@keyframes ai-jobs-bar-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.25; }
}

.ai-jobs-bar-label {
  flex: 0 0 auto;
  color: var(--color-text-secondary);
}

/* 命令是这一条里唯一要认的东西：等宽、提亮、可截断（完整命令看 title） */
.ai-jobs-bar-cmd {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-primary);
}

.ai-jobs-bar-meta {
  flex: 0 0 auto;
  margin-left: auto;
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

/* 任务清单卡片：与输入框同宽（--ai-column-w + margin auto），否则会比输入框宽一截。
   浅底、无边框——它是一条进度，不是一张内容卡片；抢了回答的视觉重心反而碍事。 */
.ai-plan-card {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: var(--ai-column-w);
  margin: 0 auto 6px;
  padding: 7px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  font-size: 12px;
}

.ai-plan-head {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--color-text-secondary);
}

.ai-plan-title { font-weight: 600; }

/* 计数靠右：扫一眼就知道还剩几步，不用逐行数 */
.ai-plan-count {
  margin-left: auto;
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.ai-plan-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.ai-plan-item {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  min-width: 0;
  color: var(--color-text-secondary);
}

/* 勾选框：14px 见方，未完成是描边、进行中是呼吸点、完成是实心绿。
   三态都能在余光里区分——用户扫过去只想知道「做到哪了」。 */
.ai-plan-box {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  margin-top: 2px;
  border: 1px solid var(--color-border);
  border-radius: 3px;
  color: var(--color-accent-text);
}

.ai-plan-item.is-done .ai-plan-box {
  border-color: var(--color-success);
  background: var(--color-success);
}

.ai-plan-item.is-done .ai-plan-text {
  color: var(--color-text-muted);
  text-decoration: line-through;
}

.ai-plan-item.is-doing .ai-plan-box { border-color: var(--color-accent); }

.ai-plan-doing {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-accent);
  animation: ai-inject-pulse 1.2s ease-in-out infinite;
}

.ai-plan-text { min-width: 0; overflow-wrap: anywhere; }

.ai-plan-note { color: var(--color-text-muted); font-size: 11.5px; }

/* 等待插入的消息：贴在输入框正上方，宽度与输入框同口径（--ai-column-w + margin auto），
   否则它会从窗口左边一直拉到右边，比输入框宽一截，看起来像另一块区域。
   只靠一条浅底与左侧圆点分层，不加边框——它是一条临时状态，不是一张卡片。 */
.ai-inject-queue {
  display: flex;
  flex-direction: column;
  gap: 3px;
  width: var(--ai-column-w);
  margin: 0 auto 6px;
}

.ai-inject-item {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
  padding: 4px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  font-size: 11.5px;
  color: var(--color-text-secondary);
}

/* 呼吸点：说明「还挂着，等着被接走」——静态圆点看起来像已完成 */
.ai-inject-dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-accent);
  animation: ai-inject-pulse 1.2s ease-in-out infinite;
}

@keyframes ai-inject-pulse {
  0%, 100% { opacity: 0.25; }
  50% { opacity: 1; }
}

.ai-inject-text {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
}

.ai-inject-hint { flex: 0 0 auto; color: var(--color-text-secondary); font-size: 11px; }

/* 「立即发送」：一条轻量文字按钮，不抢「等待插入」那句状态提示的注意力。
   它是可选动作（默认会自己接走），所以不做成 primary 按钮。 */
.ai-inject-rush {
  flex: 0 0 auto;
  margin-left: auto;
  padding: 1px 7px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-accent);
  font-size: 11px;
  cursor: pointer;
}

.ai-inject-rush:hover:not(:disabled) { background: var(--color-bg-tertiary); }
.ai-inject-rush:disabled { color: var(--color-text-muted); cursor: default; }

.ai-dock-approve {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 12px;
  margin: 0 auto;
  width: min(var(--ai-column-max), calc(100% - var(--space-6) - var(--space-6)));
  z-index: 60;
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
   跟随应用深色主题会把没写背景的示例页变成黑底黑字）。 */
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

/* 图片查看器（点消息里的缩略图） */
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

</style>

<style>
/* 跨子组件的共享类：必须非 scoped。
   这些 class 同时出现在消息区（回答下方产物条）与 AiSidePanel（面板首页产物列表），
   而 scoped 样式只作用于本组件自己的元素——放在 scoped 块里会有一半命不中。 */
/* 回答产物：横向排的 chip，一行放不下自动换行。
   宽度给满而不是 fit-content——fit-content 会先按 max-content 撑开，
   只有一个短文件名时整块缩成一小截，多文件换行时的收缩点也不好判断 */
.ai-artifacts { margin-top: 8px; width: 100%; max-width: 100%; }
.ai-artifacts-sum {
  display: flex; align-items: center; gap: 4px; width: fit-content;
  padding: 3px 0; font-size: 12px; color: var(--color-text-muted);
  cursor: pointer; user-select: none; list-style: none;
}
.ai-artifacts-sum::-webkit-details-marker { display: none; }
.ai-artifacts-chev { transition: transform 0.15s; }
.ai-artifacts[open] > .ai-artifacts-sum .ai-artifacts-chev { transform: rotate(180deg); }
.ai-artifacts-list { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 4px 0; }
/* 产物 chip 同样无框：透明描边只是为了让 hover 底色和文字保持原有内缩，
   在深色底上透明边框与去掉边框视觉一致，但不必再调一次 padding。 */
.ai-artifact {
  display: inline-flex; align-items: center; gap: 5px; min-width: 0; max-width: 260px;
  padding: 3px 8px; border: 1px solid transparent; border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-primary); font-size: 11px; cursor: pointer;
  text-align: left; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-artifact:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-artifact-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ai-artifact-op { color: var(--color-text-secondary); flex-shrink: 0; }
/* 右侧面板首页的产物合集：面板窄，仍是竖列、op 贴行右，chip 去掉描边融进列表 */
.ai-side-home .ai-artifacts-list { flex-direction: column; align-items: stretch; gap: 2px; }
.ai-side-home .ai-artifact {
  width: 100%; max-width: none; gap: 8px; padding: 5px 8px;
  font-size: 12px; border-color: transparent; background: transparent;
}
.ai-side-home .ai-artifact:hover { background: var(--color-bg-tertiary); }
.ai-side-home .ai-artifact-op { margin-left: auto; padding-left: 12px; }

/* 输入相关控件：落地页 hero（AiHero）与输入区（AiComposer）共用同一套
   缩略图 / 链接输入 / 图标按钮；.ai-go-settings 还被消息区的「没配 key」空态用。
   放非 scoped 块是因为两个子组件各有一份 scoped 作用域，写在哪一边另一半都命不中。 */
/* 技能胶囊：**行内**嵌在文字里（由 AiRichInput 用 DOM API 创建），所以样式必须写在
   非 scoped 块——动态创建的节点没有 data-v 属性，写进组件的 scoped 里一条都不命中。
   胶囊是 contenteditable=false 的原子节点：像个字符一样参与换行，退格整颗删掉。 */
.ai-rich-chip {
  display: inline-flex; align-items: center; gap: 3px;
  max-width: 240px; margin: 0 1px;
  padding: 1px 3px 1px 6px;
  border: 1px solid var(--color-border); border-radius: 6px;
  background: var(--color-bg-active); color: var(--color-text-secondary);
  font-size: 11.5px; line-height: 1.6;
  vertical-align: baseline;
  user-select: none;
  white-space: nowrap;
}

.ai-rich-chip-icon { flex-shrink: 0; color: var(--color-accent); font-size: 10px; }

.ai-rich-chip-name { overflow: hidden; text-overflow: ellipsis; }

.ai-rich-chip-x {
  flex-shrink: 0; padding: 0 2px; border: none; background: transparent;
  color: var(--color-text-muted); font-size: 12px; line-height: 1;
  cursor: pointer; opacity: 0.55; transition: opacity var(--transition-fast), color var(--transition-fast);
}

.ai-rich-chip:hover .ai-rich-chip-x { opacity: 1; }

.ai-rich-chip-x:hover { color: var(--color-danger); }

.ai-go-settings {
  padding: 7px 16px; border: none; border-radius: var(--radius-md);
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 13px; cursor: pointer; font-family: inherit;
}

.ai-tool-btn {
  display: flex; align-items: center; justify-content: center;
  width: 40px; height: 32px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  border-radius: var(--radius-md); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}

.ai-tool-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }

/* 运行中不让加图：图片要过一次闸门 + 落盘，插不进已经开始跑的那一轮。
   灰掉而不是藏起来——藏起来会让人以为功能坏了。 */
.ai-tool-btn:disabled { opacity: 0.4; cursor: default; }
.ai-tool-btn:disabled:hover { color: var(--color-text-muted); border-color: var(--color-border); }

.ai-attach-preview { display: flex; flex-wrap: wrap; gap: 8px; }

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
</style>
