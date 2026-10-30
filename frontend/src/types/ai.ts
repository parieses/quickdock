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
