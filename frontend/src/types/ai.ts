import type { AIProfile } from '../../bindings/quickdock/services/models'

// Wails 不会为嵌套在 ApiResult.data 内的类型生成绑定，故在此手动定义。
// 该文件是前端源码，不会被 `wails3 generate bindings` 覆盖。
// 字段名需与 Go 端 json tag 一致（小写 active / profiles）。
export interface AIProfilesResult {
  active: string
  profiles: AIProfile[]
}
