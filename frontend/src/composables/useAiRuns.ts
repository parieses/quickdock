/**
 * AI 对话的「流式状态机」：一条流从发起到落地，全在这里。
 *
 * 为什么从 AIPage 搬出来（P4）：这堆东西原先占着页面七百多行，而它和「页面」
 * 其实只有几个接口——会话 id 从哪来、消息往哪 push、画完要不要滚到底。混在
 * 四千行的组件里，改一处收尾逻辑要在模板与状态之间来回翻。
 *
 * 边界刻意划在「状态 vs 视图动作」上：
 *   · 这一层拥有 runs 的生命周期 / 注入队列 / 会话 id 补全 / 收尾固化；
 *   · 页面仍然拥有「用户看到的东西」——消息列表、会话列表、滚动、toast。
 *     所以 deps 里传的是 ref 与回调，不是把页面整套状态搬过来。
 *
 * 两条不能动的规矩（写在这里是怕下次重构顺手改掉）：
 *   1. **流式事件只写回发起它的那条 run**（run 自己带 convId / rid），
 *      绝不按 activeId 写——用户切走之后事件还在来，按 activeId 写就是
 *      把 A 的字拼到 B 上。
 *   2. **收尾的每个分支都要判 here**（用户是否还在看这条）：切走时只留状态点，
 *      绝不碰眼前这条会话的消息列表。
 */
import { computed, ref } from 'vue'
import type { ComputedRef, Ref } from 'vue'
import { readAiSse } from '../utils/aiSse'
import { pickActiveRun } from '../utils/aiRuns'
import type { AIRun } from '../utils/aiRuns'
import { getErrorMessage } from '../utils/error'
import { countToolRounds, serializeTools } from '../utils/aiTools'
import { aiFinishedUnread } from './bridge'
import type { MentionSnapshot } from './useAiMention'
import type { LiveBlock } from './useAiMarkdownBlocks'
import type { AIConversation, AIMentionItem, AIMessage, AIMsgImage, AIStreamStat } from '../types/ai'
import type { PendingImage, ToastAPI } from '../types'

/** 任务清单卡片（plan_update 工具，见 services/ai/tools/plan.go） */
export interface PlanCardState {
  items: { text: string; status: string }[]
  note: string
}

/** 一条还在排队、没被后端接走的插入消息 */
export interface PendingInject {
  id: string
  /** 发起它的那一轮：收尾时按它把没赶上的话退回输入框 */
  rid: string
  text: string
  /** 已经点过「立即发送」：按钮转成等待态，避免连点打断好几次 */
  rushed?: boolean
}

export interface AiRunsDeps {
  /** 当前打开的会话（决定「用户在看哪条」） */
  activeId: Ref<string>
  /** 会话列表：后台那条失败时要拿标题说清「是哪条」 */
  conversations: Ref<AIConversation[]>
  /** 消息列表：user 气泡与固化后的 assistant 气泡都往这里 push */
  messages: Ref<AIMessage[]>
  /** 所有在跑的流（页面自己也读它算 activeRun / 侧栏转圈） */
  runs: Ref<AIRun[]>
  /** 落地页那张草稿对应的 run id（会话建出来之前它就是「当前这条」） */
  draftRunId: Ref<string>

  // —— 输入区 ——
  input: Ref<string>
  attachments: Ref<PendingImage[]>
  mentions: Ref<AIMentionItem[]>
  lastMentions: Ref<MentionSnapshot[]>
  pickedSkills: Ref<string[]>
  lastSkills: Ref<string[]>
  stripSkillTokens: (text: string) => string

  // —— 这一轮的执行口径（每次都显式带，不做「沿用上次」） ——
  hasKey: ComputedRef<boolean>
  mode: Ref<string>
  permissionFull: Ref<boolean>
  worktreeEnabled: Ref<boolean>
  worktreeActive: ComputedRef<boolean>
  worktreeBranch: Ref<string>
  pendingSpaceId: Ref<string>
  convCwd: Ref<string>
  /** 发送那一刻的模型名（用户可能在流式期间切档案，事后读会拿到新值） */
  streamModelOf: () => string
  /** 同上，但取的是档案 id：随请求发给后端，决定这一轮真正用哪个模型 */
  profileIdOf: () => string

  // —— 会话状态标记 ——
  planCards: Map<string, PlanCardState>
  statMap: Map<string, AIStreamStat>
  setConvError: (convId: string, msg: string) => void
  markConvRunning: (convId: string) => void
  markConvFinished: (convId: string, failed?: boolean) => void
  loadConversations: () => void
  loadCheckpoints: (convId: string) => void
  refreshJobBadge: () => void

  // —— 渲染 / 滚动 ——
  renderLiveBlocks: (src: string, blocks?: LiveBlock[]) => void
  primeMessageBlocks: (id: string, content: string, html: string[]) => void
  clearLiveCache: () => void
  scrollToBottom: (smooth?: boolean) => void
  followBottom: () => void
  scheduleMermaidScan: () => void

  // —— 本地流式服务（端口 + 随机令牌） ——
  ensureStreamInfo: () => Promise<void>
  aiStreamPort: Ref<number>
  aiStreamToken: Ref<string>

  // —— 杂项 ——
  toast: ToastAPI
  t: (key: string, named?: Record<string, unknown>) => string
  openSettings: () => void
  /** 重试标记：send 读后清，retryLast 置 true（跳过再插一个 user 气泡） */
  retryPending: Ref<boolean>
  /** 本页是否在前台（不在前台时跑完留未读点） */
  pageActive: Ref<boolean>
  /** 「+」面板开着时按回车发送要先收起来 */
  plusOpen: Ref<boolean>
}

export function useAiRuns(deps: AiRunsDeps) {
  // 把 deps 摊平成本地名：下面这些函数是从页面整段搬过来的，保持原样才好对照。
  // 只用一层解构而不是到处写 deps.xxx——搬移期间最容易出错的是漏改某处引用，
  // 摊平后仍然只有一个名字可写错，而它会在类型检查里立刻暴露。
  const {
    activeId, conversations, messages, runs, draftRunId,
    input, attachments, mentions, lastMentions, pickedSkills, lastSkills, stripSkillTokens,
    hasKey, mode, permissionFull, worktreeEnabled, worktreeActive, worktreeBranch,
    pendingSpaceId, convCwd, streamModelOf, profileIdOf,
    planCards, statMap, setConvError, markConvRunning, markConvFinished,
    loadConversations, loadCheckpoints, refreshJobBadge,
    renderLiveBlocks, primeMessageBlocks, clearLiveCache,
    scrollToBottom, followBottom, scheduleMermaidScan,
    ensureStreamInfo, aiStreamPort, aiStreamToken,
    toast, t, openSettings, retryPending, pageActive, plusOpen,
  } = deps

  let runSeq = 0

  function runOf(convId: string): AIRun | undefined {
    if (!convId) return undefined
    return runs.value.find((r) => r.convId === convId)
  }

  /**
   * 内部用的「当前这条」：flushStream 靠它判断「这一帧该不该画」。
   *
   * 页面自己也有一份同规则的 computed（它还要喂模板与输入框的 streaming 判断）。
   * 规则实现只有 utils/aiRuns 里那一份，两边不会各算各的。
   */
  const activeRun = computed<AIRun | null>(
    () => pickActiveRun(runs.value, activeId.value, draftRunId.value) || null,
  )

  // —— 流式文本缓冲 ——
  // 关键性能点：token 到达时不直接写响应式状态。改为先追加到普通字符串缓冲，
  // 再按 ~16ms 的节奏批量提交。原因是每个 token 都触发一次响应式更新 → 整个
  // AIPage 组件（含全部历史消息的 v-html diff）重渲染，上游吐字越快掉帧越明显。
  // 批量提交把渲染次数从「每 token 一次」压到「每帧一次」。
  //
  // 缓冲挂在 run 对象上（见 AIRun.buf / flushTimer）：多条任务同时跑时各攒各的。
  const STREAM_FLUSH_MS = 16

  // 提交这条流的缓冲区。done/停止时必须立即 flush，否则尾部文字会丢。
  //
  // 只画「用户正看着的那条」：分块渲染 / 图扫描 / 贴底都是给眼前这一页用的。
  // 后台那条只攒文本（内容由后端落库，切回去从库里读），对着它渲染只会白烧 CPU，
  // 还会把当前这页滚到底——用户正翻历史时最烦这个。
  function flushStream(run: AIRun) {
    if (run.flushTimer) { clearTimeout(run.flushTimer); run.flushTimer = null }
    if (run.buf) {
      run.text += run.buf
      run.buf = ''
    }
    if (run.reasonBuf) {
      run.reasoning += run.reasonBuf
      run.reasonBuf = ''
    }
    if (activeRun.value !== run) return
    renderLiveBlocks(run.text, run.blocks)
    scheduleMermaidScan()
    followBottom()
  }

  // 刷帧间隔随回答长度放宽。单帧成本 ≈ splitMarkdownBlocks(全文) + renderBlock(最后一块)，
  // 固定 16ms 节奏下长回答会把主线程吃到接近饱和——这时点开产物要等好几秒才出来，
  // 因为交互的回调排在成堆的渲染任务后面。放宽到 60/120ms 肉眼仍是连续输出，
  // 主线程却留出了响应点击的余量。
  function streamFlushDelay(run: AIRun): number {
    const n = run.text.length + run.buf.length
    if (n > 24 * 1024) return 120
    if (n > 8 * 1024) return 60
    return STREAM_FLUSH_MS
  }

  function scheduleStreamFlush(run: AIRun) {
    if (run.flushTimer) return
    run.flushTimer = setTimeout(() => {
      run.flushTimer = null
      flushStream(run)
    }, streamFlushDelay(run))
  }

  // 本轮实时用时：从「发送」到「本轮结束」（done / 中断 / 失败）之间，
  // 消息区顶部会跳动显示计时，让用户直观看到这一轮花了多久——
  // 而不是只在回合结束后的消息脚注里看到一行静态「耗时」。
  //
  // 这里只记起点；走表由 AiRoundBar 自己做（见该组件）：计时数字每 200ms 变一次，
  // 让它在本页面上做 computed 的话，每次跳动都会把整个 AI 页面重渲染一遍，
  // 而工具调用期间正文根本不动，那几秒全是在为这一个数字干活。
  function startRoundTimer(run: AIRun) {
    run.startedAt = performance.now()
    run.elapsedMs = 0
  }

  function stopRoundTimer(run: AIRun) {
    // 结束时刻的快照：消息脚注用它，不需要精确到毫秒以下。
    if (run.startedAt > 0) run.elapsedMs = performance.now() - run.startedAt
  }

  // ---- 运行中插入（steering，后端见 services/ai/ai_inject.go） ----
  //
  // 一条会话同一时刻只跑一轮（对一条会话只有一串历史，两轮同时写就是互相踩）。
  // 所以「跑着的时候又打了一句」现在不再是拦下来让用户等，而是把这句插进正在跑的
  // 那一轮：后端在下一个节点（调模型之前）把它接进上下文，模型接着就看见了。
  //
  // 前端为什么还要自己记一份 pending：后端回执（inject_ack）之前，这句话在界面上
  // 谁都还没看到。没有这个落点，用户会以为没发出去，然后连按几次回车。
  const pendingInjects = ref<PendingInject[]>([])
  /** 已经落成正式气泡的回执 id。
   *
   *  后端已按 id 去重（services/ai/ai_inject.go 的 push），这里是第二道闸：
   *  同一句万一被回执两次，界面上不该出现两条一模一样的话。
   *  只留最近一小段，别让它无限涨。 */
  const ackedInjectIds = new Set<string>()
  /** 当前这一轮还没被接走的那些（只画属于眼前这条任务的） */
  const pendingInjectsHere = computed(() => {
    const r = activeRun.value
    if (!r) return [] as PendingInject[]
    return pendingInjects.value.filter((p) => p.rid === r.rid)
  })

  function dropPendingInject(id: string) {
    pendingInjects.value = pendingInjects.value.filter((p) => p.id !== id)
  }

  // 没赶上这一轮：把字退回输入框，而不是让它凭空消失。
  // 追加而不是覆盖——用户可能已经在写下一句了，覆盖等于把刚打的字也吃了。
  function restoreInjectText(text: string) {
    input.value = input.value ? `${text}\n${input.value}` : text
  }

  // 落地页发起的第一个任务，会话 id 要等 conv 事件回来（几百毫秒）才有，
  // 而插入必须带会话 id（后端按它找正在跑的那一轮）。等不到就放弃，
  // 让用户自己再按一次——总比把话插到别的会话里强。
  function waitConvId(run: AIRun, timeoutMs = 5000): Promise<boolean> {
    return new Promise((resolve) => {
      const started = performance.now()
      const tick = () => {
        if (run.convId) return resolve(true)
        if (!runs.value.some((r) => r.rid === run.rid)) return resolve(false)
        if (performance.now() - started > timeoutMs) return resolve(false)
        setTimeout(tick, 100)
      }
      tick()
    })
  }

  /** 把一句话插进正在跑的那一轮。 */
  async function injectMessage(run: AIRun) {
    if (!hasKey.value) {
      toast.error(t('aiNeedKey'))
      openSettings()
      return
    }
    // 与正常发送同一套解析口径：技能胶囊只是输入框里的标记，不算正文。
    const raw = stripSkillTokens(input.value)
    const text = /^[\s/／]*$/.test(raw) ? '' : raw
    if (!text) return
    // 图片与技能要跟着「一条新消息」做上下文组装（过闸门、落盘、拼技能提示），
    // 塞不进已经跑起来的那一轮。按钮在流式期已经禁用，这里只是兜底（键盘路径）。
    if (attachments.value.length || pickedSkills.value.length) {
      toast.error(t('aiInjectTextOnly'))
      return
    }
    if (!run.convId && !(await waitConvId(run))) {
      toast.error(t('aiInjectNoConv'))
      return
    }

    const id = `inj-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
    pendingInjects.value.push({ id, rid: run.rid, text })
    // 立刻清空输入框：字已经交出去了，留在框里会让下一次回车重复发送。
    // 万一没赶上（下面 catch / 收尾退回），会原样塞回来。
    input.value = ''
    attachments.value = []
    mentions.value = []
    try {
      await ensureStreamInfo()
      const resp = await fetch(`http://127.0.0.1:${aiStreamPort.value}/ai/inject`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Stream-Token': aiStreamToken.value },
        body: JSON.stringify({ convId: run.convId, id, message: text }),
      })
      // 409 = 这一轮刚好结束了：没赶上，退回输入框。
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    } catch {
      dropPendingInject(id)
      restoreInjectText(text)
      toast.error(t('aiInjectMissed'))
    }
  }

  /** 在「等待插入」那条上点「立即发送」：打断正在生成的回答，让它马上接这句话。
   *
   * 不打断的默认行为是「等模型把手上这段写完再接入」。长回答要吐十几秒，
   * 这期间横条一直挂着不动，用户会以为消息没送出去——所以给一个能催它的按钮。
   *
   * 打断是后端做的（immediate 字段）。这边只负责：按钮转等待态、失败时把字退回输入框。
   * 成功不需要做什么——后端接走时会发 inject_ack，那条横条自己就消失了。
   */
  async function rushInject(p: PendingInject) {
    if (p.rushed) return
    // 会话 id 从发起它的那一轮取：排队项只记 rid，用户此刻可能已经切到别的任务了。
    const run = runs.value.find((r) => r.rid === p.rid)
    const convId = run?.convId
    if (!convId) return
    p.rushed = true
    // 只有「确实打断了」才保持「正在打断…」：那时这句话马上会被接走，
    // 回执（inject_ack）会把它从横条上移除，按钮不用再变。
    let keeping = false
    try {
      await ensureStreamInfo()
      const resp = await fetch(`http://127.0.0.1:${aiStreamPort.value}/ai/inject`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Stream-Token': aiStreamToken.value },
        body: JSON.stringify({ convId, id: p.id, message: p.text, immediate: true }),
      })
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
      const body = await resp.json().catch(() => null)
      if (body && body.interrupted === true) {
        keeping = true
      } else if (body && body.interrupted === false) {
        // 没打断成功 = 这一刻正在跑工具（模型没在生成），这句话还在队里，
        // 那一步做完就会接走。按钮退回可点状态并说清原因，别一直挂着「正在打断…」。
        toast.success(t('aiInjectRushBusy'))
      }
    } catch {
      // 409 = 这一轮刚好结束了：这句话没进上下文，退回输入框（与 injectMessage 同一口径）。
      dropPendingInject(p.id)
      restoreInjectText(p.text)
      toast.error(t('aiInjectMissed'))
    } finally {
      // 除「确实打断了」以外一律复位。放在 finally 是因为：接口回空体、204、
      // 非 JSON 时原来没有任何分支复位，按钮会永久卡在「正在打断…」且无法再发
      // ——那是这条排队消息唯一的失败出口。
      if (!keeping) p.rushed = false
    }
  }

  // 把「流式跑到一半的这一段」定格成一条正式消息。//
  // 插入会在这里把一轮切成两段：用户这句话之后模型说的是对它的回答，和前面那段
  // 不是同一条回复。不切的话，用户会看到自己的话排在「已经写了一半的回答」上面，
  // 而实际上他看到的是自己插话前后各有一截回答。
  // 后端在同一个位置做同一件事（agent_run.go 的 saveSegment），两边的段落边界
  // 必须对齐，否则刷新前后同一条对话会读成不同的顺序。
  function freezeSegment(run: AIRun) {
    flushStream(run)
    const content = run.text
    if (!content && !run.reasoning && !run.tools.length) return
    const id = `ast-${Date.now()}-s${++segmentSeq}`
    const msg: Record<string, any> = {
      id, conv_id: run.convId, role: 'assistant', content,
      created_at: new Date().toISOString(),
    }
    if (run.model) msg.model = run.model
    if (run.reasoning) msg.reasoning_content = run.reasoning
    if (run.tools.length) {
      msg.tool_calls = serializeTools(run.tools)
      msg.tool_rounds = countToolRounds(run.tools)
    }
    messages.value.push(msg as AIMessage)
    // 复用流式期已经渲染好的块，避免定格那一刻整段重新解析
    primeMessageBlocks(id, content, run.blocks.map((b) => b.html))
    // 缓冲清空：后面的内容属于下一段，落在新的气泡里
    run.text = ''
    run.buf = ''
    run.reasoning = ''
    run.reasonBuf = ''
    run.tools = []
    run.blocks = []
    clearLiveCache()
  }

  let segmentSeq = 0

  /** 重新生成 / 编辑重发的附加参数（见 services/ai/ai_stream.go 的截断处理）。
   *  普通发送不传，行为与原来完全一致。 */
  interface SendOpts {
    action?: 'regen' | 'edit'
    targetId?: string
    /** 请求没能开始（含截断被拒）：调用方把乐观改过的消息列表回滚。
     *  SSE 中途的 event: error 不调它——那时历史已经截断，模型只是这一轮没答好。 */
    onFailed?: () => void
  }

  async function send(opts?: SendOpts) {
    // 先读后清：任何提前 return（没配 Key / 空消息 / 这条还在跑）都不能把标志
    // 留给下一次手动发送，否则那条用户发言将不再出现在气泡里。
    const reuseUserBubble = retryPending.value
    retryPending.value = false
    const convId = activeId.value
    // 只挡「这条任务自己」：别的任务在跑不该拦这里——并行就是为了能同时干两件事。
    // 拦在同一条会话上是因为后端对一条会话只有一串历史，两轮同时往里写就是互相踩。
    //
    // 新任务多判一个「这条草稿是不是已经在创建中」：会话 id 要等 conv 事件回来才有，
    // 这中间几百毫秒里连按两次回车，会多出一条并行的同题任务。
    const busy = convId ? runOf(convId) : runs.value.find((r) => r.rid === draftRunId.value)
    if (busy) {
      // 这条会话还在跑：这一句插进正在跑的那一轮，而不是拦下来让用户等
      // （见 injectMessage 与后端的 ai_inject.go）。
      await injectMessage(busy)
      return
    }
    // 面板开着时按回车也能发（焦点还在 textarea 上），发出去就该收起来。
    plusOpen.value = false
    // 新一轮开始：上一轮的清单属于上一次提问，留着会让人以为这次也要做那些事。
    const planKey = convId || draftRunId.value
    if (planKey) planCards.delete(planKey)
    // 技能是「这一条消息」的指令，不在正文里：直接把胶囊里的名字传给后端。
    // 先取快照再清空——下面 input 一清，胶囊也跟着清，那时再读就没了。
    // 重试要沿用上一次的技能：失败重发时胶囊已经空了，拼个空数组等于
    // 把用户特意选的技能默默丢掉。
    const skillNames = pickedSkills.value.slice()
    if (skillNames.length) lastSkills.value = skillNames
    const skillPayload = skillNames.length ? skillNames : (reuseUserBubble ? lastSkills.value : [])
    const raw = stripSkillTokens(input.value)
    // 单独一个 `/` 只是要调技能菜单（没配技能时弹层为空，回车就会落到这里）：
    // 当成空正文挡住，别建出一条标题叫「/」、正文为空的任务。
    const text = /^[\s/／]*$/.test(raw) ? '' : raw
    if (!text && attachments.value.length === 0) return
    if (!hasKey.value) {
      toast.error(t('aiNeedKey'))
      openSettings()
      return
    }
    // 发送时的模型名：流式结束前用户可能切档案（onModelChange 会回滚），
    // 事后读 activeProfile 拿到的是新值，所以此刻先定下来。
    const streamModel = streamModelOf()
    // 档案 id 同理要在这一刻定下来：流式期间用户可能给输入框换了模型，
    // 而这一轮已经按旧档案在跑，事后读会记错。
    const profileId = profileIdOf()
    // 这一轮自己的那份流式状态。放在 runs 里而不是几个全局 ref：
    // 用户切走之后事件还在来，必须写回**发起它的那条**；两条任务同时跑时更不能共用。
    runs.value.push({
      rid: `run-${++runSeq}`,
      convId,
      text: '',
      buf: '',
      reasoning: '',
      reasonBuf: '',
      tools: [],
      rules: [],
      blocks: [],
      error: '',
      startedAt: performance.now(),
      elapsedMs: 0,
      flushTimer: null,
      tickTimer: null,
      ctrl: null,
      done: false,
      model: streamModel,
    })
    // 必须取回响应式代理再改：push 进去的是原始对象，直接改原始对象不会触发界面更新。
    const run = runs.value[runs.value.length - 1] as AIRun
    // 落地页发起的那条：在 conv 事件拿到会话 id 之前，它就是这个页面上的「当前这条」。
    if (!convId) draftRunId.value = run.rid
    markConvRunning(convId)
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
    // 技能胶囊随这一条消息一起用掉：输入框清空时里面的胶囊已经被富输入框一并清掉了
    // （它会 emit update:skills []），这里同步一份，避免面板在你还没看到新内容时还标「已选」。
    pickedSkills.value = []
    // 上一次失败留下的报错：发了新的就该让它消失（只清这条会话的）。
    setConvError(convId, '')
    // 新的一轮 = 新的一批块：块缓存留着只会白占内存（原文一样时复用是安全的，
    // 但这里清掉不影响正确性，也顺手把上一条长回答的条目丢掉）。
    clearLiveCache()
    startRoundTimer(run)
    // 这一轮是否以失败收尾：只用来决定「切走的那条会话该留绿点还是红点」。
    let runFailed = false
    try {
      await ensureStreamInfo()
      const ctrl = new AbortController()
      run.ctrl = ctrl
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
          // 技能：本次输入框里选的那些（可能为空数组 = 这条不用技能）。
          // 总是显式带上，后端就不必再猜「沿用上次」还是「这次没选」。
          skills: skillPayload,
          // 权限：每次都显式带。「缺省 = 沿用上次」在这里不成立——权限是每一条消息
          // 的执行口径，少带一次就可能按上一轮的完全访问静默执行。
          permission: permissionFull.value ? 'full' : 'default',
          // 模型档案：每次都带输入框里选中的那个。用户在发送前刚切过模型时，
          // 库里还是上一轮的值，后端必须以这次请求为准。
          profileId,
          // 隔离工作区：已开工作区的会话传什么都无所谓（后端按会话记录续用），
          // 没开的才看这两个字段。分支空 = 用当前分支。
          worktree: worktreeEnabled.value || worktreeActive.value,
          worktreeBranch: worktreeBranch.value || undefined,
          // 重新生成 / 编辑重发：后端在写响应头之前按 targetId 截断历史，
          // 再让 saveUserTurn 的幂等与 buildAIMessages 按截断后的库跑这一轮。
          action: opts?.action,
          targetId: opts?.targetId,
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
      // 分帧细节（\n\n 分隔、跨 chunk 的半个汉字、多行 data）在 utils/aiSse.ts，
      // 这里只写「收到某个事件该做什么」。
      await readAiSse(resp.body, ({ event, data }) => {
        switch (event) {
          case 'plan': {
            // AI 更新了任务清单（plan_update 工具）：整份覆盖，不按序号增量合并。
            // 它可能重排过顺序、加过条目，按序号改就会把完成状态标到别的行上，
            // 而错位的勾选没人看得出来。
            const raw = Array.isArray(data.items) ? data.items : []
            const items = raw
              .map((it: any) => ({ text: String(it?.text || ''), status: String(it?.status || 'todo') }))
              .filter((it: { text: string }) => it.text)
            if (!items.length) break
            const key = run.convId || run.rid
            planCards.set(key, { items, note: String(data.note || '') })
            break
          }
          case 'token':
            // 写入这条流自己的普通缓冲，按帧批量提交到响应式状态
            if (data.text) { run.buf += data.text; scheduleStreamFlush(run) }
            break
          case 'reasoning':
            if (data.text) { run.reasonBuf += data.text; scheduleStreamFlush(run) }
            break
          case 'tool':
            // 清单更新不画工具卡片：它已经有自己那张清单卡片了，
            // 再挂一条「调用 plan_update」只会让人以为 AI 在刷工具。
            if (String(data.name || '') === 'plan_update') break
            // 工具开始执行：先把卡片挂上去（「调用中」），不等结果。
            // 一个工具可能跑几秒（扫端口、读大文件），这几秒界面必须看起来是活的，
            // 否则用户会以为卡死并去点停止——那反而中断了本来正常的调用。
            run.tools.push({
              id: String(data.id || ''),
              name: String(data.name || ''),
              args: String(data.args || ''),
              round: Number(data.round) || 0,
              running: true,
            })
            break
          case 'tool_output': {
            // 命令执行期间的实时输出（pip install / npm install / go build）。
            // 以前这些命令跑几十秒，卡片上只会从「正在执行」直接跳到一大段结果，
            // 中间看不出它是在下载依赖还是卡死了——用户往往因此去点停止。
            //
            // add 只能往后追加；tail 是「当前正在刷新的那一行」，每帧整体替换：
            // pip 的进度条用 \r 重写同一行，当成追加会刷出几十行一样的百分比。
            const tp = run.tools.find((x) => x.id === String(data.id || ''))
            if (tp) {
              const add = String(data.add || '')
              if (add) tp.live = (tp.live || '') + add
              tp.liveTail = String(data.tail || '')
            }
            break
          }
          case 'approval': {
            // 写类 / 高危工具在宿主侧停下来等确认（见 ai_approval.go）。
            // 后端保证「先发 tool 再发 approval」，所以这里一定能找到卡片。
            const at = run.tools.find((x) => x.id === String(data.callId || ''))
            if (at) {
              at.approval = 'pending'
              at.riskNote = String(data.riskNote || '')
              at.rememberable = !!data.rememberable
              at.grantScope = String(data.grantScope || '')
              // 会话由后端下发：用户可能在这张卡等着的时候切走再切回，
              // 那时 activeId 已经不是发起审批的那个会话了。
              at.approvalConvId = String(data.convId || '')
              // 后台那条会话在等确认：审批条只画在当前会话上（见模板），切走的人
              // 就完全看不到它在等——而它会一直等下去。这里必须喊一声。
              const aConv = String(data.convId || '')
              if (aConv && aConv !== activeId.value) {
                const name = conversations.value.find((c) => c.id === aConv)?.title || t('aiNewChat')
                toast.success(t('aiApprovalOtherConv', { name }))
              }
            }
            break
          }
          case 'tool_result': {
            // 结果回填到同一张卡片。按 id 找而不是按「最后一条」：
            // 一轮里可能并发调多个工具，回来的顺序不一定与发出顺序一致。
            const tc = run.tools.find((x) => x.id === String(data.id || ''))
            if (tc) {
              tc.running = false
              tc.ms = Number(data.ms) || 0
              tc.error = String(data.error || '')
              tc.result = String(data.result || '')
              // 人看的输出（命令的 stdout/stderr）：卡片正文用它，result 只是兜底。
              tc.output = String(data.output || '')
              // 实时缓冲留在那儿也没用：output 是完整版本，live 只是它跑到一半时的样子。
              tc.live = ''
              tc.liveTail = ''
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
                void loadCheckpoints(run.convId)
              }
            }
            break
          }
          case 'conv':
            if (data.id) {
              // 发出第一条消息后立刻点开别的任务的人，不能被这条流拽回来：
              // 只有「还停在落地页等它」或「已经在这条会话上」才切过去。
              const adopt = draftRunId.value === run.rid || activeId.value === data.id
              run.convId = data.id
              markConvRunning(data.id)
              if (adopt) {
                if (draftRunId.value === run.rid) draftRunId.value = ''
                activeId.value = data.id
              }
            }
            // 会话是首次发送时才建的：它这时才拿到 space_id，侧栏要重取
            // 才知道该把它排到哪个空间下（否则要等下次刷新才出现）。
            if (data.spaceId) loadConversations()
            // 归属已随这次请求落库，待定值作废（下一条消息走的是 convId，不带 spaceId）。
            pendingSpaceId.value = ''
            // 首次发送时才建的会话，侧栏那条是本地占位：把库和后端实际采用的
            // 档案 id 补回去。少了 profile_id，切走再切回来就会回落到全局
            // 「最后选择」——如果用户之后在别处换过模型，这条任务的显示就错了。
            if (data.title !== undefined || data.profileId) {
              const i = conversations.value.findIndex((c) => c.id === data.id)
              if (i >= 0) {
                if (data.title !== undefined) conversations.value[i].title = data.title
                if (data.profileId) conversations.value[i].profile_id = String(data.profileId)
              } else loadConversations()
            }
            break
          case 'rules':
            // 本轮生效的项目规则文件（AGENTS.md / .quickdock/rules*）。
            // 只存内存、不落库：规则是「这次提问读了哪些文件」的执行时事实，不是对话内容。
            run.rules = Array.isArray(data.files)
              ? (data.files as any[]).map((f) => ({ name: String(f?.name || ''), bytes: Number(f?.bytes) || 0, truncated: !!f?.truncated }))
              : []
            break
          case 'notice':
            // 后端主动告知的过程事件（上下文超窗已压缩、引用读取失败等）。
            // 必须让用户看见：多条问题一起消失时，不说一声就像「AI 忽然忘事」。
            // level 决定颜色：引用失败是「没做成」，不该以绿条出现。
            //
            // 现在可能有好几条任务同时跑，后台那条的提示只有一句话会让人一头雾水
            // ——前面挂上它的任务名，才知道是谁在说。
            if (data.message) {
              const away = run.convId && run.convId !== activeId.value
              const who = away ? (conversations.value.find((c) => c.id === run.convId)?.title || '').trim() : ''
              const text = who ? `${who} · ${String(data.message)}` : String(data.message)
              if (data.level === 'error') toast.error(text)
              else toast.success(text)
            }
            break
          case 'cwd':
            // 后端本轮才定下工作目录（普通任务的专属目录是首次发送时才建的，
            // 空间任务的目录也可能这时才回退到空间的）。不回传的话工具栏那条
            // 指示要等切走再切回才出现，而用户此刻正想知道文件会写到哪。
            if (data.cwd) {
              // 写回这一轮**发起时**的那条会话，不是当前打开的那条：
              // 用户可能已经切到别的任务看历史，把 A 的目录写到 B 上就是数据错位。
              const ci = conversations.value.findIndex((c) => c.id === run.convId)
              if (ci >= 0) conversations.value[ci].cwd = String(data.cwd)
              // 工具栏只在这一轮就是用户正在看的那条时才跟着变：切走之后把 A 的
              // 目录显示在 B 的工具栏上，比不显示更糟。
              if (run.convId === activeId.value || draftRunId.value === run.rid) {
                convCwd.value = String(data.cwd)
              }
            }
            break
          case 'inject_ack': {
            // 后端把这句话接进这一轮了（可能在几十秒之后，等模型手上那一步做完）。
            // 到这里它才成为「真的发出去过的一句话」——之前只是排队。
            const iid = String(data.id || '')
            const itext = String(data.text || '')
            dropPendingInject(iid)
            if (!itext) break
            // 同一句只认第一次回执（见 ackedInjectIds）。
            if (iid) {
              if (ackedInjectIds.has(iid)) break
              ackedInjectIds.add(iid)
              if (ackedInjectIds.size > 64) {
                const oldest = ackedInjectIds.values().next().value
                if (oldest) ackedInjectIds.delete(oldest)
              }
            }
            // 用户可能已经把会话切走了：这一轮的内容后端已经落库，回来时 loadMessages
            // 会拿到；这里只把排队项清掉，绝不能往眼前这条会话的消息列表里塞。
            const here = !run.convId || run.convId === activeId.value || draftRunId.value === run.rid
            if (!here) break
            // 先把已经吐出来的半段定格（见 freezeSegment），再接上这句话：
            // 顺序反过来的话，用户的话会排在自己已经看到的回答上面。
            freezeSegment(run)
            messages.value.push({
              id: 'usr-' + Date.now() + '-' + Math.random().toString(36).slice(2, 7),
              conv_id: run.convId, role: 'user', content: itext,
              created_at: new Date().toISOString(),
            } as AIMessage)
            scrollToBottom()
            break
          }
          case 'done':
            if (data.convId) {
              run.done = true
              // 这一轮里可能刚起了后台任务（模型往往「起完就收尾」）。徽标是 20s 轮询的，
              // 不等下一次：否则用户刚看完「已在后台启动」，抬头却看不到那个数字。
              void refreshJobBadge()
              // 用户已经切到别的会话：这一轮的内容后端已经落库，回来时 loadMessages
              // 会拿到。**绝不能**往 messages 里塞 —— 那是另一条会话的列表。
              // 但「结束」这件事本身还是要处理：给那条会话留个绿点。
              const here = data.convId === activeId.value
              if (here) {
                // 立即 flush，把缓冲里的尾部文字全部落到响应式状态
                flushStream(run)
                const content = run.text
                const reasoning = run.reasoning
                // 有工具记录也必须固化消息：模型可能「调了 3 个工具但没写答案」，
                // 这时 content 是空的，但用户需要看到它做了什么、钱花在哪。
                if (content || reasoning || run.tools.length) {
                  const msg: Record<string, any> = { id: `ast-${Date.now()}`, conv_id: data.convId, role: 'assistant', content: content || '', created_at: new Date().toISOString() }
                  // 模型名由后端回传（它就是落库的那份），拿不到才退回发送时的档案
                  const usedModel = String(data.model || run.model || '').trim()
                  if (usedModel) msg.model = usedModel
                  if (reasoning) msg.reasoning_content = reasoning
                  // 工具记录落到消息上，刷新前就不用重新拉一次会话。
                  // 序列化时丢掉 running：库里存的是「发生过什么」，不是「界面此刻什么样」。
                  if (run.tools.length) {
                    msg.tool_calls = serializeTools(run.tools)
                    msg.tool_rounds = Number(data.toolRounds) || countToolRounds(run.tools)
                  }
                  // 规则文件跟着这条回复走，而不是挂在页面顶上：用户事后想问的是
                  // 「这条回答当时读了什么约定」，刷新后消失也无所谓（下次提问会重读）。
                  if (run.rules.length) msg.rules = run.rules.slice()
                  messages.value.push(msg as AIMessage)
                  // 复用流式期已渲染好的块 HTML，避免落地瞬间整条重新解析
                  primeMessageBlocks(msg.id as string, content, run.blocks.map((b) => b.html))
                  // 记录本条回复的耗时/token，用于在消息下方展示
                  const stat: AIStreamStat = {
                    elapsedMs: Math.round(performance.now() - run.startedAt),
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
              }
              // 无论用户是否还看着这条会话，这一轮都结束了。半截气泡 / 工具卡片
              // 由 finally 里把 run 摘掉收走；后台那条的内容已经在库里，
              // 留在内存里只会白占地方。
              markConvFinished(data.convId)
              loadConversations()
            }
            break
          case 'error': {
            // 报错只画给「用户正在看的那条会话」：切走之后把 A 的报错横幅挂到 B 上，
            // 用户会以为是自己刚打开的那条出了问题。后台那条改成红点（见 finally），
            // 报错本身留在它自己的会话上，切回去能看见。
            const errText = data.message || data.error || '未知错误'
            run.error = String(errText)
            if (run.convId && run.convId !== activeId.value) {
              runFailed = true
              break
            }
            flushStream(run)
            break
          }
        }
      })
    } catch (e: any) {
      // 中断（AbortError）不算失败：那是用户自己点的停止。
      if (e && e.name !== 'AbortError') {
        const msg = getErrorMessage(e)
        run.error = msg
        // 报错横幅同理只给当前会话；后台那条留红点。
        if (run.convId && run.convId !== activeId.value) runFailed = true
        // 请求没起来（400 = 服务端拒绝了截断）：把前端乐观收起的历史拉回来。
        opts?.onFailed?.()
      }
    } finally {
      // 这一轮属于谁、用户还在不在看它。整个收尾都按这两个值判断：
      // 切走之后事件还在来，收尾绝不能碰用户正在看的另一条会话。
      const runID = run.convId
      const here = !runID || runID === activeId.value || draftRunId.value === run.rid
      // 读完了却既没收到 done、也没有 error、也不是用户点的停止 —— 这是断流
      // （后端崩了、SSE 被中间层截断、网络断）。不判这一条的话它会走「用户点了停止」
      // 的分支：用户只看到一条半截回答，没有任何提示，也无从知道该不该重发。
      const broken = !run.done && !run.error && !run.stoppedByUser
      if (broken) {
        run.error = t('aiStreamInterrupted')
        if (here) toast.error(run.error)
      }
      // 中断（用户点停止）时：把已生成的内容固化成一条正式消息，避免内容凭空消失。
      // 之前 stop() 只是 abort，已收到的文字既没进 messages 也没清空，
      // 结果是「AI 一直在回复」但内容卡在半截、也无法复制。
      // 必须先 flush：否则还留在缓冲里（run.buf）的尾部文字会随 clear 一起丢掉。
      //
      // done 已经固化过就不再补（run.done）：否则一条正常结束的回复会变成
      // 「正式回答 + 半截回答」两条。
      if (here) flushStream(run)
      else if (run.flushTimer) { clearTimeout(run.flushTimer); run.flushTimer = null }
      // 中断时也可能已经有工具卡片：模型调了工具、结果还没回来用户就点了停止。
      // 这些记录同样要固化——「调过但没结果」本身就是这次中断发生过的事实。
      const hadTools = run.tools.length > 0
      if (!run.done && (run.text || run.reasoning || hadTools)) {
        // 只往「用户还停留着的那个会话」里补气泡。切走之后后端仍会把半截内容落库
        // （带 stopped 标记），但绝不能塞进另一个会话的消息列表。
        if (here && runID) {
          const aid = `ast-${Date.now()}-stop`
          const msg: Record<string, any> = { id: aid, conv_id: runID, role: 'assistant', content: run.text || '', created_at: new Date().toISOString(), stopped: true }
          // 断流也留半截内容，但标记要分开：卡片上写「已中断」还是「连接中断」，
          // 决定用户回来时是「哦我当时停了」还是「哦当时断了，得重发」。
          if (broken) msg.interrupted = true
          // 中断时后端还没发 done，模型名用发送时记下的那份
          if (run.model) msg.model = run.model
          if (run.reasoning) msg.reasoning_content = run.reasoning
          if (hadTools) {
            msg.tool_calls = serializeTools(run.tools)
            msg.tool_rounds = countToolRounds(run.tools)
          }
          messages.value.push(msg as AIMessage)
          // 同样复用流式期已渲染的块
          primeMessageBlocks(aid, run.text || '', run.blocks.map((b) => b.html))
          const stat: AIStreamStat = {
            elapsedMs: Math.round(performance.now() - run.startedAt),
            firstTokenMs: 0,
            promptTokens: 0,
            completionTokens: 0,
            cachedTokens: 0,
            toolRounds: hadTools ? countToolRounds(run.tools) : 0,
          }
          statMap.set(aid, stat)
        }
      }
      // 这一轮结束了还有没被接走的插入消息：它没进上下文（后端那侧不会再有
      // 下一个节点来接它），得把字退回输入框，不能让用户以为已经发出去了。
      // 只退给「还看着这条任务」的人：切走之后退进去，下一句就会发到别的任务上。
      const stranded = pendingInjects.value.filter((p) => p.rid === run.rid)
      if (stranded.length) {
        pendingInjects.value = pendingInjects.value.filter((p) => p.rid !== run.rid)
        if (here) {
          for (const p of stranded) restoreInjectText(p.text)
          toast.error(t('aiInjectMissed'))
        } else {
          // 用户已经切到别的任务上了：不能把字塞进眼前这条的输入框（下一句就发
          // 到别的任务去了），但也不能一声不响地吞掉——说清楚是哪条没赶上。
          const name = conversations.value.find((c) => c.id === runID)?.title || t('aiNewChat')
          toast.error(t('aiInjectMissedAway', { name }))
        }
      }
      // 报错留在它自己的会话上：侧栏红点之外，切回来还能读到原因。
      if (run.error) setConvError(runID, run.error)
      // 这条流到此为止：从 runs 里摘掉。界面上的半截气泡、工具卡片、计时条
      // 都跟着 activeRun 一起消失（内容已经固化进 messages，或已由后端落库）。
      runs.value = runs.value.filter((r) => r.rid !== run.rid)
      if (draftRunId.value === run.rid) draftRunId.value = ''
      stopRoundTimer(run)
      // 这一轮结束：正看着这条会话的不需要任何标记，切走了的留一个绿点（失败是红点）
      // 等他回来 —— 否则用户永远不知道「刚才那条跑完了没有」。
      //
      // run.error 也要算进失败：错误发生在当前会话时只画横幅、不置 runFailed，
      // 若用户在那之后、收尾之前切走，就会留下一个绿点，而点进去是报错。
      markConvFinished(runID, runFailed || !!run.error)
      // 流式结束后尾块不再是「进行中」：补一次扫描，把最后一张图（可能就在结尾）画出来
      if (here) scheduleMermaidScan()
      // 在别的页面期间回答跑完了：给侧栏一个未读点，否则用户不知道已经能看
      if (!pageActive.value) aiFinishedUnread.value = true
    }
  }

  /**
   * 告诉后端「这一轮是用户主动停的」。
   *
   * 为什么不能只 abort：点停止最直接的做法是 abort 掉 fetch，而从服务端看，
   * abort 与刷新页面、关闭窗口、网络断完全一样——都是连接没了。后端因此只能
   * 把两者都记成「用户中断」，于是刷一下页面，通知中心就多一条「AI 回答已停止」。
   *
   * 失败不重试也不提示：这是一条尽力而为的旁路信号，最坏情况是通知措辞不准，
   * 不该让它影响停止本身。
   */
  async function tellStop(convId: string) {
    if (!convId) return
    await ensureStreamInfo()
    await fetch(`http://127.0.0.1:${aiStreamPort.value}/ai/stop`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Stream-Token': aiStreamToken.value },
      body: JSON.stringify({ convId }),
    })
  }

  /**
   * 停止某条流。默认停「当前打开的那条」；删会话/删空间时要停指定的那条。
   *
   * abort 之后 finally 会照常收尾（把已生成的半截内容固化下来），所以这里只负责掐流。
   */
  function stop(target?: AIRun | string) {
    const run = typeof target === 'string' ? runOf(target) : (target || activeRun.value)
    if (!run) return
    // 先打标再 abort：finally 要据此区分「用户停的」和「流断了」（见 stoppedByUser）。
    run.stoppedByUser = true
    // 顺序不能反：连接一断后端就开始收尾，那时再发信号已经没人收了。
    // 但有上限——本地 HTTP 正常几毫秒，出问题也不能让停止按钮卡在那里。
    void Promise.race([
      tellStop(run.convId).catch(() => { /* 旁路信号，失败就算了 */ }),
      new Promise((resolve) => setTimeout(resolve, 200)),
    ]).finally(() => run.ctrl?.abort())
  }

  return {
    send,
    stop,
    /** 把一句话插进正在跑的那一轮（发第二句时用） */
    injectMessage,
    /** 「等待插入」横条上的立即发送 */
    rushInject,
    pendingInjects,
    pendingInjectsHere,
  }
}
