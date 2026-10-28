import type { AIProfile } from '../../bindings/quickdock/services/ai/models'

// Wails 不会为嵌套在 ApiResult.data 内的类型生成绑定，故在此手动定义。
// 该文件是前端源码，不会被 `wails3 generate bindings` 覆盖。
// 字段名需与 Go 端 json tag 一致（小写 active / profiles）。
export interface AIProfilesResult {
  active: string
  profiles: AIProfile[]
}

// AI 用量统计（对应 Go 端 services/ai.AIUsageStats）。
// 金额单位是人民币元，且是**估算**：按内置价目表线性折算，不是账单。
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
