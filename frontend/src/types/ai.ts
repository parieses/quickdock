import type { AIProfile } from '../../bindings/quickdock/services/ai/models'

// Wails 不会为嵌套在 ApiResult.data 内的类型生成绑定，故在此手动定义。
// 该文件是前端源码，不会被 `wails3 generate bindings` 覆盖。
// 字段名需与 Go 端 json tag 一致（小写 active / profiles）。
export interface AIProfilesResult {
  active: string
  profiles: AIProfile[]
}

// AI 用量统计（对应 Go 端 services/ai.AIUsageStats）。
// 金额单位是人民币元，且是估算：按内置价目表线性折算，不是账单。
export interface AICostEntry {
  key: string
  requests: number
  promptTokens: number
  completionTokens: number
  cachedTokens: number
  totalTokens: number
  estimatedCost: number
  savedByCache: number
  priced: boolean
}

export interface AIUsageStatsResult {
  days: number
  conversations: number
  requests: number
  promptTokens: number
  completionTokens: number
  cachedTokens: number
  totalTokens: number
  estimatedCost: number
  savedByCache: number
  currency: string
  unpricedRequests: number
  models: AICostEntry[]
  daily: AICostEntry[]
  sources: AICostEntry[]
}

// 审批统计（对应 Go 端 services/ai.AIApprovalStats）。
// deny 与 auto 分开：前者是用户明确点了拒绝，后者是超时/停止/断开——
// 「不想让它干」和「压根没看到卡」是两种问题，混在一起会误判闸门太严。
export interface AIApprovalToolStat {
  tool: string
  allow: number
  deny: number
  auto: number
}

export interface AIApprovalStatsResult {
  allow: number
  deny: number
  auto: number
  byTool: AIApprovalToolStat[]
}

// 工具审批策略的一行（对应 Go 端 services/ai.aiApprovalRuleView）。
// custom 为 false 表示这一行显示的是按等级推导的默认值，不是用户设的。
export interface AIApprovalRuleView {
  tool: string
  level: number
  desc: string
  mode: 'auto' | 'ask' | 'deny'
  custom: boolean
  agentOnly: boolean
  // editable=false 表示读类工具：策略恒 auto，只展示不可改。
  editable: boolean
}

// 消息正文搜索的一条命中（对应 Go 端 db.AIMessageHit）。
export interface AIMessageHit {
  msg_id: string
  conv_id: string
  conv_title: string
  role: string
  snippet: string
  created_at: string
}

// 按对话聚合后的命中（对应 Go 端 services/ai.aiConvHit）。
// count 是这条对话里命中了几条消息，latest 是其中最新那一条。
export interface AIConvHit {
  conv_id: string
  conv_title: string
  count: number
  latest: AIMessageHit
}

// 一个目录的 AI 钩子情况（对应 Go 端 services/ai.aiHooksView）。
// found=false 表示这个目录没有 hooks.json；error 有值即配置写坏了，视为未生效。
export interface AIHooksInfo {
  found: boolean
  path: string
  afterEdit: string[]
  beforeCommand: string[]
  approved: boolean
  error: string
}

// 一个外部 MCP server 的接入状态（对应 Go 端 services/ai.extServerState）。
// config 就是 mcp.json 里那个 server 的对象，编辑时原样带回去。
export interface AIExtServerState {
  name: string
  config: {
    command?: string
    args?: string[]
    env?: Record<string, string>
    url?: string
    disabled?: boolean
  }
  status: 'ok' | 'error' | 'disabled'
  toolCount: number
  error?: string
  tools: string[]
}

// ===== 对话实体（AIPage 与 utils/aiTools.ts 共用） =====
// 这些原本定义在 AIPage.vue 里；工具卡片解析搬到 utils/aiTools.ts 后
// 两处都要用，放这里避免组件反向导出类型。

// 任务 = 一条对话。界面上叫「任务」而不是「会话」：它对应的是「我交给 AI 的一件事」，
// 而且创建时要先选类型（翻译 / 总结 / 解释 …），本来就不是闲聊。
export interface AIConversation {
  id: string; title: string; summary: string
  prompt_tokens: number; completion_tokens: number
  created_at: string; updated_at: string
  cwd?: string
  // 所属空间（空 = 普通任务，侧栏列在「任务」组里）
  space_id?: string
  // 创建时选定的类型。后端对老数据回落 chat，这里同样兜底。
  mode?: string
  // 星标：侧栏置顶一组收藏。与 space_id 正交 —— 空间里的任务也能星标。
  starred?: boolean
}

// 空间 = 一个目录 + 挂在这个目录下的若干任务。
export interface AISpace { id: string; name: string; cwd: string; created_at: string; updated_at: string }

export interface AIMsgImage { kind: 'file' | 'url'; url?: string; path?: string; name?: string; dataUrl?: string }

export interface AIMessage {
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
export interface AIToolCall {
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
  // 触及的文件路径（file_write / file_edit 有值，后端从结果里摘出来随事件下发）。
  // 回答产物条与文件预览都靠它：从结果 JSON 里抠会在结果被截断时整块失败。
  path?: string
  // 这条调用所属的编排 run / 节点（只有 spawn_agent 才有）。
  // 有了它才能在回复下面挂出 run 树，不必去解析可能被截断的结果文本。
  run_id?: string; node_id?: string
}

// 一条编辑快照（回滚点）。Wails 只在 bound 参数里生成类型，返回值里的结构
// （ApiResult.data）是 any，所以这里按后端 AICheckpointView 的 JSON 手写一份。
export interface AICheckpointView {
  id: string; seq: number; tool: string; tool_call_id: string
  created_at: string
  files: { path: string; existed: boolean; mode: number; backup: string; bytes: number }[]
}

// 一个生效的规则文件（后端 ai_rules.go 的 aiRuleFile）。
export interface AIRuleFile { name: string; bytes: number; truncated?: boolean }

// @ 补全列表的一项（后端 AIMentionItem，或本地拼的会话项）。
// 会话项没有 path，只有 convId。
export interface AIMentionItem { kind: 'file' | 'dir' | 'conv'; name: string; path?: string; convId?: string }

// 回答产物：一条回复里 AI 写过 / 改过的文件，聚合成条挂在回答下方，点击预览。
export interface AIArtifact { path: string; name: string; op: 'write' | 'edit' }

// 大纲里的一条：从「用户提过的每一句」里取首行，点它跳到该条消息。
export interface OutlineItem { id: string; text: string }

// 一条回复的用量统计（流式结束后由后端 AIStreamStat 事件逐条补齐）。
export interface AIStreamStat {
  elapsedMs: number
  firstTokenMs: number
  promptTokens: number
  completionTokens: number
  cachedTokens: number
  cost?: number
  toolRounds?: number
}
