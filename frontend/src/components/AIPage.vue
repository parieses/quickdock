<script setup lang="ts">
import { ref, computed, reactive, onMounted, onUnmounted, onActivated, onDeactivated, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { KEEP_ALIVE } from '../pages/keepAlive'
import {
  Copy, Image as ImageIcon, X, Download, MessageSquare,
  Languages, FileText, GraduationCap, Lightbulb, ListChecks,
} from '@lucide/vue'
import { Marked } from 'marked'
import { unwrap } from '../utils/api'
import AiSidePanel from './AiSidePanel.vue'
import AiConversationSidebar from './AiConversationSidebar.vue'
import AiToolbar from './AiToolbar.vue'
import AiDigestPanel from './AiDigestPanel.vue'
import AiHero from './AiHero.vue'
import AiComposer from './AiComposer.vue'
import AiMessageList from './AiMessageList.vue'
import AiApprovalBar from './AiApprovalBar.vue'
import { SetAIPageVisible } from '../../bindings/quickdock/services/appservice'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { downloadText, safeFileName } from '../utils/download'
import { useAiMarkdownBlocks } from '../composables/useAiMarkdownBlocks'
import { useAiAttachments } from '../composables/useAiAttachments'
import { useAiMention } from '../composables/useAiMention'
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
import { useFloatMenu } from '../composables/useFloatMenu'
import { aiGenerating, aiFinishedUnread } from '../composables/bridge'
import { CreateNoteDoc } from '../../bindings/quickdock/services/note/noteservice'
import type { PendingImage, ToastAPI } from '../types'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AIConvHit, AIHooksInfo, AIProfilesResult } from '../types/ai'
import type {
  AIArtifact, AICheckpointView, AIConversation, AIMessage, AIMsgImage,
  AIRuleFile, AISpace, AIToolCall, OutlineItem,
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
  AIGetHooks,
  AISetHooksApproved,
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
watch(convCwd, loadAIHooks, { immediate: true })

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





// streamArtifacts 流式期的产物：file_write 返回的当下就挂出来，
// 等 done 再显示会让「文件已经写完了」和「看得见」之间隔一整轮回答。
const streamArtifacts = computed(() => artifactsFromCalls(streamTools.value))

// 审批条要展示的那一条：取第一个还等着答复的调用。
//
// 为什么是「第一条」而不是「全部」：后端是一次一条地等（回完一条才下发下一条），
// 同时堆积多条只是前端事件顺序造成的短暂现象，条数用 count 标出来就够了。
const pendingApproval = computed(() => streamTools.value.find((tc) => tc.approval === 'pending') || null)
const pendingApprovalCount = computed(() => streamTools.value.filter((tc) => tc.approval === 'pending').length)

// —— 产物预览 / 右侧面板 ——
// 面板本体（标签页、CodeMirror、文件读写）已收进 AiSidePanel；页面只留开合状态，
// 因为工具栏按钮的 is-active 与 Ctrl+Shift+B 都要读它。
const sideOpen = ref(false)
const sidePanelRef = ref<InstanceType<typeof AiSidePanel> | null>(null)

// convArtifacts 本任务全部回复的产物合集（跨条消息去重，后写的覆盖先写的 op）
const convArtifacts = computed<AIArtifact[]>(() => {
  const map = new Map<string, AIArtifact>()
  for (const m of messages.value) {
    for (const a of artifactsOf(m)) map.set(a.path, a)
  }
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
  if (streaming.value) return
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

// 刷帧间隔随回答长度放宽。单帧成本 ≈ splitMarkdownBlocks(全文) + renderBlock(最后一块)，
// 固定 16ms 节奏下长回答会把主线程吃到接近饱和——这时点开产物要等好几秒才出来，
// 因为交互的回调排在成堆的渲染任务后面。放宽到 60/120ms 肉眼仍是连续输出，
// 主线程却留出了响应点击的余量。
function streamFlushDelay(): number {
  const n = streamingText.value.length + streamBuf.length
  if (n > 24 * 1024) return 120
  if (n > 8 * 1024) return 60
  return STREAM_FLUSH_MS
}

function scheduleStreamFlush() {
  if (streamTimer) return
  streamTimer = setTimeout(() => {
    streamTimer = null
    flushStream()
  }, streamFlushDelay())
}

const reasoningText = ref('')  // 思考过程（reasoning_content），折叠显示

// 本地 AI 流式服务信息（端口 + 随机令牌，通过 Header 传递）
const aiStreamPort = ref(0)
const aiStreamToken = ref('')
let streamCtrl: AbortController | null = null

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
const modes = [
  { key: 'chat', label: 'aiModeChat', desc: 'aiModeChatDesc', icon: MessageSquare },
  // 计划模式排第二：它和对话是「先想后做 / 边想边做」的一对，
  // 放在翻译、总结后面会让人以为它是个特殊用途
  { key: 'plan', label: 'aiModePlan', desc: 'aiModePlanDesc', icon: ListChecks },
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
  pickMention, removeMention, onMentionKey,
} = useAiMention({ input, streaming, hasKey, conversations, convCwd })

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
} = useAiMarkdownBlocks({ md, streamBlocks })

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
const heroRef = ref<InstanceType<typeof AiHero> | null>(null)

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
  nextTick(() => heroRef.value?.focus())
}

// 点示例问题：填进输入框并聚焦，用户接着改两个字就能发。
// 不直接发送——示例只是起点，直接发出去常常不是用户真想问的那句话。
function fillHeroTip(key: string) {
  input.value = t(key)
  nextTick(() => heroRef.value?.focus())
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
  // 流式块缓存按回复隔离（细节见 useAiMarkdownBlocks.clearLiveCache）
  clearLiveCache()
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
  if (!activeId.value || digestBusy.value || streaming.value) return
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
  if (mentionSnapshot.length) lastMentions.value = mentionSnapshot
  const mentionPayload = mentionSnapshot.length ? mentionSnapshot : (reuseUserBubble ? lastMentions.value : [])
  // 立即把用户消息显示出来，随后在消息区内等待 AI 流式回复
  const msg: AIMessage = {
    id: 'usr-' + Date.now() + '-' + Math.random().toString(36).slice(2, 7), conv_id: convId, role: 'user', content: text,
    created_at: new Date().toISOString(), imgList,
  }
  // 重试：那条 user 消息本来就在列表里，别再插一个重复气泡。
  // （后端 saveUserTurn 对「同内容 + 同图片」幂等，DB 里也不会多出一行）
  if (!reuseUserBubble) messages.value.push(msg)
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
              // 产物路径：本条回复的「产物」条在流式期就能挂出来，
              // 不必等 done 固化成历史消息。
              tc.path = String(data.path || '')
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
  if (imgViewer.value) closeImgViewer()
  else if (htmlPreview.value) closeHtmlPreview()
  else if (ctxTarget.value) closeCtxMenu()
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
  if (streamCtrl) streamCtrl.abort()
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  if (msgSearchTimer) { clearTimeout(msgSearchTimer); msgSearchTimer = undefined }
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
        :streaming="streaming"
        @open-cwd="openCwd"
        @model-change="onModelChange"
        @regen-title="regenTitle"
        @toggle-star="toggleStar"
        @export-md="exportConvMarkdown"
        @toggle-side="toggleSidePanel"
        @stop="stop"
      />

      <!-- 大纲 / 摘要：绝对定位挂在 .ai-main 上（它已是 position: relative）。
           不做成模态框——它只是「翻一下聊到哪了」，看完点目录里的一条就该跳过去。 -->
      <AiDigestPanel
        v-if="digestOpen && activeId"
        :summary="activeConv?.summary || ''"
        :busy="digestBusy"
        :streaming="streaming"
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
        :has-key="hasKey"
        :spaces="spaces"
        :new-space="NEW_SPACE"
        :new-task-dir="newTaskDir"
        :new-task-dir-label="newTaskDir ? dirTail(newTaskDir) : ''"
        :ai-profiles="aiProfiles"
        :allow-file-image="allowFileImage"
        :allow-url-image="allowUrlImage"
        :out-hint="newTaskOutHint"
        :hero-tips="heroTips"
        @model-change="onModelChange"
        @enter="onEnterSend($event, heroSend)"
        @send="heroSend"
        @paste="onInputPaste"
        @drop="onInputDrop"
        @confirm-url="confirmUrlImage"
        @remove-attachment="removeAttachment"
        @pick-dir="pickNewTaskDir"
        @open-file-picker="openFilePicker"
        @toggle-url="toggleUrlInput"
        @fill-tip="fillHeroTip"
        @open-settings="emit('open-settings', 'ai')"
      />

      <template v-else>
      <!-- 消息区 -->
      <AiMessageList
        ref="msgListRef"
        :has-key="hasKey"
        :messages="messages"
        :stat-map="statMap"
        :locale="locale"
        :mode="mode"
        :streaming="streaming"
        :reasoning-text="reasoningText"
        :stream-tools="streamTools"
        :streaming-text="streamingText"
        :stream-blocks="streamBlocks"
        :run-rules="runRules"
        :stream-artifacts="streamArtifacts"
        :error-msg="errorMsg"
        :can-retry="canRetry"
        :render-block="renderBlock"
        :render-message-blocks="renderMessageBlocks"
        :msg-stat="msgStat"
        :format-stat="formatStat"
        :cp-for-call="cpForCall"
        @open-settings="emit('open-settings', 'ai')"
        @open-artifact="openArtifactInPanel"
        @open-img="openImgViewer"
        @copy="copy"
        @save-note="saveAsNote"
        @restore-checkpoint="restoreCheckpoint"
        @run-plan="runPlan"
        @retry="retryLast"
        @msg-click="onMsgClick"
        @msg-mouseup="onMsgMouseUp"
        @msg-contextmenu="onMsgContextMenu"
      />

      <!-- 「回到底部」按钮由 AiMessageList 自己渲染：它跟着消息容器的滚动状态走，
           放在父组件里要靠事件回传，反而多一层状态。 -->

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

      <!-- 工具审批条：钉在输入区上方。
           不放消息流里是因为会话在这期间是停住的，按钮滚出视野就等于卡死。 -->
      <AiApprovalBar
        v-if="pendingApproval"
        :call="pendingApproval"
        :count="pendingApprovalCount"
        @allow="approveTool(pendingApproval, 'allow')"
        @remember="approveTool(pendingApproval, 'allow', true)"
        @deny="approveTool(pendingApproval, 'deny')"
      />

      <!-- 输入区 -->
      <AiComposer
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
        :streaming="streaming"
        :allow-file-image="allowFileImage"
        :allow-url-image="allowUrlImage"
        @approve-hooks="approveHooks"
        @remove-attachment="removeAttachment"
        @confirm-url="confirmUrlImage"
        @remove-mention="removeMention"
        @pick-mention="pickMention"
        @composer-key="onComposerKey($event, send)"
        @send="send"
        @paste="onInputPaste"
        @drop="onInputDrop"
        @open-file-picker="openFilePicker"
        @toggle-url="toggleUrlInput"
      />
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
  padding: 3px 0; font-size: 11px; color: var(--color-text-secondary);
  cursor: pointer; user-select: none; list-style: none;
}
.ai-artifacts-sum::-webkit-details-marker { display: none; }
.ai-artifacts-chev { transition: transform 0.15s; }
.ai-artifacts[open] > .ai-artifacts-sum .ai-artifacts-chev { transform: rotate(180deg); }
.ai-artifacts-list { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 4px 0; }
.ai-artifact {
  display: inline-flex; align-items: center; gap: 5px; min-width: 0; max-width: 260px;
  padding: 3px 9px; border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  color: var(--color-text-primary); font-size: 11px; cursor: pointer;
  text-align: left; font-family: inherit;
}
.ai-artifact:hover { border-color: var(--color-accent); }
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
.ai-go-settings {
  padding: 7px 16px; border: none; border-radius: var(--radius-md);
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 13px; cursor: pointer; font-family: inherit;
}

.ai-tool-btn {
  display: flex; align-items: center; justify-content: center;
  width: 40px; height: var(--ai-input-h); border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  border-radius: var(--radius-md); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}

.ai-tool-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }

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
