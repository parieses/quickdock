import type { ApiResult } from '../../bindings/quickdock/services/models'

/**
 * 业务 API 错误。
 *
 * Go 侧的失败结果除了 msg 还带 code 与 data，其中 data 里常放结构化信息
 * （如 SSH 的 errorCode、主机指纹、已收到的半截输出）。只抛 message 会把这些
 * 全部丢掉，调用方就只能显示一句「失败」，无法区分「指纹要确认」和「网络不通」。
 */
export class ApiError extends Error {
  readonly code: number
  readonly data: Record<string, any>

  constructor(message: string, code: number, data: Record<string, any> = {}) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.data = data
  }

  /** 业务错误码（data.errorCode），没有则空串。 */
  get errorCode(): string {
    return typeof this.data.errorCode === 'string' ? this.data.errorCode : ''
  }
}

/**
 * 统一 API 结果解析
 * 所有 Go 接口返回 { code: 0=成功/1=失败, data: any, msg: string }
 * 成功 → 返回 data，失败 → 抛出 ApiError（带 code 与结构化 data）
 */
export function unwrap<T = any>(result: ApiResult | null): T | null {
  if (!result) {
    return null
  }
  if (result.code !== 0) {
    const data = (result.data && typeof result.data === 'object' ? result.data : {}) as Record<string, any>
    throw new ApiError(result.msg || 'Request failed', result.code, data)
  }
  return result.data as T
}

/** 取业务错误码；不是 ApiError 时返回空串（网络/运行时错误没有业务码）。 */
export function apiErrorCode(e: unknown): string {
  return e instanceof ApiError ? e.errorCode : ''
}

/** 取失败结果附带的业务数据；不是 ApiError 时返回 null。 */
export function apiErrorData<T = Record<string, any>>(e: unknown): T | null {
  return e instanceof ApiError ? (e.data as T) : null
}
