/**
 * 运行轨迹（agent_runs / agent_nodes / agent_events）在前端共用的类型与纯函数。
 *
 * 为什么单独抽出来：轨迹有两个渲染入口——消息下方那条 run 树（AgentRunTree.vue）
 * 和左上角的轨迹抽屉（AiTraceDrawer.vue）。两处各自声明一遍接口，字段一旦加错
 * 就是「一处能显示、另一处静默空白」，而这类错没有任何报错可看。
 *
 * 字段与 internal/db/agent_run.go 的 json tag 逐字对齐（snake_case）。
 */

/** run 头：一次任务执行。 */
export interface RunHead {
  id: string
  status: string
  goal: string
  used_tokens: number
  budget_tokens: number
  error: string
}

/** run 树里的一个节点 = 一次派活（顶层是 role=lead 的根）。 */
export interface RunNode {
  id: string
  run_id: string
  parent_id: string
  seq: number
  role: string
  task: string
  status: string
  cwd: string
  model: string
  result: string
  prompt_tokens: number
  completion_tokens: number
  started_at: string
  ended_at: string
}

/** 一条执行事件。payload 是 JSON 字符串（后端按 map 序列化后落库）。 */
export interface RunEvent {
  id: string
  run_id: string
  node_id: string
  seq: number
  kind: string
  payload: string
  created_at: string
}

/** 会话回放里的一条 run（对应 Go 端 services/ai.AISessionRun，WP12）。 */
export interface SessionRun {
  run: RunHead & { conv_id?: string; trigger?: string; created_at?: string; ended_at?: string }
  nodes: RunNode[]
  events: RunEvent[]
  /** 这次 run 派出去了几个子 agent（父节点非空即子）。 */
  children: number
}

/** AISessionTimeline 的返回体。 */
export interface SessionTimeline {
  convId: string
  runs: SessionRun[]
}

/** 一个子 agent 节点在列表里要显示的东西（对应 DSH 轨迹顶部的子任务详情）。 */
export interface ChildTask {
  id: string
  role: string
  task: string
  status: string
  tokens: number
  /** 耗时（毫秒）；节点没记结束时间或时间倒挂时为 0。 */
  elapsedMs: number
  /** 完整节点：顶部列表点开后要看目录、模型、输入/输出 token 与产出。 */
  node: RunNode
}

/**
 * 从一条 run 的节点里挑出子 agent（parent_id 非空），算好 token 与耗时。
 *
 * 顶层 role=lead 的根节点 parent_id 为空，天然被排除；根节点自己的 token
 * 不该算进任何子任务。时间字段是后端序列化的本地时间字符串（无时区后缀），
 * 直接交 Date 解析按浏览器本地时区处理，与写入时同一台机器一致。
 */
export function childTasksOf(run: SessionRun): ChildTask[] {
  return childTasksFromNodes(run.nodes)
}

/** 从任意节点集合里取子 agent；「本次运行」模式没有 SessionRun 包装，直接用这个。 */
export function childTasksFromNodes(nodes: RunNode[]): ChildTask[] {
  return nodes
    .filter((n) => !!n.parent_id)
    .map((n) => {
      const start = Date.parse(n.started_at)
      const end = Date.parse(n.ended_at)
      return {
        id: n.id,
        role: n.role,
        task: n.task,
        status: n.status,
        tokens: n.prompt_tokens + n.completion_tokens,
        elapsedMs: Number.isFinite(start) && Number.isFinite(end) && end >= start ? end - start : 0,
        node: n,
      }
    })
    .sort((a, b) => b.tokens - a.tokens)
}

/**
 * 毫秒 → 随量级自动换单位：873ms / 12.4s / 2m30s / 1h04m。
 *
 * 工具调用动辄跑十几分钟（子 agent 尤甚），直接打「873697ms」没人能一眼
 * 看出是 14 分钟。<1s 保留毫秒，<10s 带一位小数才能看出快慢差别，
 * 再往上只给整数秒——长耗时里那点小数没有信息量。
 */
export function fmtElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0ms'
  if (ms < 1000) return `${Math.round(ms)}ms`
  const sec = ms / 1000
  if (sec < 10) return `${sec.toFixed(1)}s`
  if (sec < 60) return `${Math.round(sec)}s`
  const wholeSec = Math.floor(sec)
  const min = Math.floor(wholeSec / 60)
  const rest = wholeSec % 60
  if (min < 60) return rest ? `${min}m${rest}s` : `${min}m`
  const hr = Math.floor(min / 60)
  const restMin = min % 60
  return restMin ? `${hr}h${String(restMin).padStart(2, '0')}m` : `${hr}h`
}

/** AIAgentRunTree 的返回体。 */
export interface RunTree {
  run: RunHead | null
  nodes: RunNode[]
  events: RunEvent[]
  last_seq: number
}

/**
 * 按节点分组事件，保持 seq 升序。
 *
 * seq 由数据库按 run 分配且全局递增，所以「同一 run 内的事件顺序」天然正确；
 * 这里只做分组，不再依赖数组下标——`afterSeq` 增量拉取会让数组从中间开始。
 */
export function groupEventsByNode(events: RunEvent[]): Map<string, RunEvent[]> {
  const out = new Map<string, RunEvent[]>()
  for (const e of events) {
    const key = e.node_id || ''
    const list = out.get(key)
    if (list) list.push(e)
    else out.set(key, [e])
  }
  for (const list of out.values()) list.sort((a, b) => a.seq - b.seq)
  return out
}

/**
 * 解析事件载荷；坏数据返回 null 而不是抛错。
 *
 * 载荷是后端按 map 序列化来的，理论上总是合法 JSON；但历史数据、截断后的
 * `{"marshal_error":true}` 都在真实库里出现过，渲染层不该为此崩掉整棵树。
 */
export function parseEventPayload(e: RunEvent): Record<string, unknown> | null {
  if (!e.payload) return null
  try {
    const v = JSON.parse(e.payload)
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}

/** 事件里取一个字符串字段（取不到给空串，避免渲染出 "undefined"）。 */
export function strField(p: Record<string, unknown> | null, key: string): string {
  if (!p) return ''
  const v = p[key]
  return typeof v === 'string' ? v : ''
}

/** 事件里取一个数字字段（取不到给 0）。 */
export function numField(p: Record<string, unknown> | null, key: string): number {
  if (!p) return 0
  const v = p[key]
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}

/** 事件里取一个布尔字段。 */
export function boolField(p: Record<string, unknown> | null, key: string): boolean {
  if (!p) return false
  return p[key] === true
}

/**
 * 一条事件的归一化摘要（只取字段，不做文案）。
 *
 * 文案留给组件用 `t()` 拼——两个渲染入口（消息下的 run 树、左侧轨迹抽屉）
 * 空间不同、措辞也不同，但「哪些字段有意义」应当只有一份定义。
 */
export interface EventSummary {
  kind: string
  /** tool_call id：tool 与 tool_result 靠它精确配对 */
  id: string
  /** 工具名；非工具事件回落到 kind */
  name: string
  /** tool 事件的参数摘要（后端已截断到 200 字） */
  args: string
  ok: boolean
  ms: number
  added: number
  removed: number
  message: string
  error: string
  /** 结果/人看的输出/行级 diff/产物路径——步骤卡片展开时看这些 */
  result: string
  output: string
  diff: string
  path: string
  round: number
  rounds: number
  tools: number
  /** 有推理产出的轮数（WP20）：用来区分「模型只在首轮思考」与「思考被合并了」。 */
  reasoningRounds: number
  reasoningChars: number
  /** reasoning_round 事件的思考原文 */
  text: string
}

export function summarizeEvent(e: RunEvent): EventSummary {
  const p = parseEventPayload(e)
  const text = strField(p, 'text')
  return {
    kind: e.kind,
    id: strField(p, 'id'),
    name: strField(p, 'name') || e.kind,
    args: strField(p, 'args'),
    ok: boolField(p, 'ok'),
    ms: numField(p, 'ms'),
    added: numField(p, 'added'),
    removed: numField(p, 'removed'),
    message: strField(p, 'message'),
    error: strField(p, 'error'),
    result: strField(p, 'result'),
    output: strField(p, 'output'),
    diff: strField(p, 'diff'),
    path: strField(p, 'path'),
    round: numField(p, 'round'),
    rounds: numField(p, 'rounds'),
    tools: numField(p, 'tools'),
    reasoningRounds: numField(p, 'reasoningRounds'),
    // reasoning_round 事件带的是 text 原文（不是 reasoningChars）；
    // 这里就地换算成字数，显示层不必把整段思考拉进内存再 length。
    reasoningChars: numField(p, 'reasoningChars') || text.length,
    text,
  }
}

/**
 * 时间线的一步：一个动作单元（工具调用 / 思考 / 结论）。
 *
 * 为什么不直接渲染事件：后端一条工具调用发 tool + tool_result 两条事件，
 * 扁平铺出来用户看到的是「两步」，还会把 args 的 JSON 原文糊在界面上。
 * 这里按 id 把它们合成一步——「做了什么 + 结果如何」在一张卡片上说完。
 */
export interface RunStep {
  kind: 'tool' | 'think' | 'done' | 'note'
  /** 稳定 key：工具用 call id，其余用事件 id */
  key: string
  name: string
  args: string
  ok: boolean
  ms: number
  added: number
  removed: number
  text: string
  error: string
  result: string
  output: string
  diff: string
  path: string
  message: string
  reasoningChars: number
}

/** 哪些事件是「上下文注入」而非动作——不进时间线，避免淹没真正的步骤。 */
const META_KINDS = new Set([
  'conv', 'rules', 'cwd', 'start', 'project_memory',
  'inject_ack', 'approval', 'plan', 'compact', 'tool_output',
])

/**
 * 把节点事件聚合成时间线步骤。
 *
 * tool 与 tool_result 靠 id 配对：结果回填到发起那一步，而不是各占一行。
 * 落单的 tool_result（历史数据只有结果）也照样成一步，不丢信息。
 */
export function eventsToSteps(events: RunEvent[]): { steps: RunStep[]; hidden: number } {
  const steps: RunStep[] = []
  const byId = new Map<string, RunStep>()
  let hidden = 0
  for (const e of events) {
    const s = summarizeEvent(e)
    if (META_KINDS.has(e.kind)) { hidden++; continue }
    if (e.kind === 'tool') {
      const step: RunStep = {
        kind: 'tool', key: s.id || e.id, name: s.name, args: s.args,
        ok: true, ms: 0, added: 0, removed: 0, text: '', error: '',
        result: '', output: '', diff: '', path: '', message: '', reasoningChars: 0,
      }
      steps.push(step)
      if (step.key) byId.set(step.key, step)
      continue
    }
    if (e.kind === 'tool_result') {
      const prev = (s.id && byId.get(s.id)) || undefined
      if (prev) {
        prev.ok = s.ok; prev.ms = s.ms; prev.added = s.added; prev.removed = s.removed
        prev.error = s.error; prev.result = s.result; prev.output = s.output
        prev.diff = s.diff; prev.path = s.path
      } else {
        steps.push({
          kind: 'tool', key: s.id || e.id, name: s.name, args: '',
          ok: s.ok, ms: s.ms, added: s.added, removed: s.removed, text: '',
          error: s.error, result: s.result, output: s.output, diff: s.diff,
          path: s.path, message: '', reasoningChars: 0,
        })
      }
      continue
    }
    if (e.kind === 'reasoning_round') {
      steps.push({
        kind: 'think', key: e.id, name: '', args: '', ok: true, ms: 0,
        added: 0, removed: 0, text: s.text, error: '', result: '', output: '',
        diff: '', path: '', message: '', reasoningChars: s.reasoningChars,
      })
      continue
    }
    // done / notice / error 都作为结论一步
    steps.push({
      kind: e.kind === 'done' ? 'done' : 'note', key: e.id, name: s.name,
      args: '', ok: e.kind !== 'error', ms: 0, added: 0, removed: 0,
      text: '', error: '', result: '', output: '', diff: '', path: '',
      message: s.message, reasoningChars: 0,
    })
  }
  return { steps, hidden }
}
