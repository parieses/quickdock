import type { ApiResult } from '../../bindings/quickdock/services/models'

/**
 * 统一 API 结果解析
 * 所有 Go 接口返回 { code: 0=成功/1=失败, data: any, msg: string }
 * 成功 → 返回 data，失败 → 抛出 Error(msg)
 */
export function unwrap<T = any>(result: ApiResult | null): T | null {
  if (!result) {
    return null
  }
  if (result.code !== 0) {
    throw new Error(result.msg || '请求失败')
  }
  return result.data as T
}

/**
 * 同步版本：用法 unwrapSync(r, defaultValue)
 */
export function unwrapSync<T = any>(result: ApiResult | null, fallback: T): T {
  if (!result || result.code !== 0) {
    return fallback
  }
  return (result.data as T) ?? fallback
}
