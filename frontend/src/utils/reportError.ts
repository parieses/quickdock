/**
 * catch 块的统一错误上报：toast 提示 + 开发日志 + 取消静默。
 *
 * 默认行为与 `toast.error(getErrorMessage(e)); logErr(tag, e)` 逐字等价。
 * 所有分支（静默、取消、兜底文案）都用 opts 显式开启，避免 wrapper 悄悄替调用方做决定。
 *
 * 落地时有两类迁移点，语义不同：
 * - 原本就同时写了 toast + logErr —— 纯收口，行为零变化；
 * - 原本只有 toast、没记日志 —— 迁移后**新增**一条日志。这是有意补齐：
 *   用户报「点了没反应」时，只有 toast 的错误在日志里毫无痕迹，无从倒查。
 *   tag 按「文件名.函数名」给，保证日志能直接定位到操作。
 *
 * 业务状态修改（loadFailed.value = true、return false、部分成功计数等）仍留在调用点，
 * 这里只负责「怎么把错误告诉用户和开发者」。
 */
import { useToast } from '../composables/useToast'
import { getErrorMessage, isDialogCanceled } from './error'
import { logErr } from './logger'

export interface ReportErrorOptions {
  /** 不弹 toast，只记日志（调用方随后会统一汇总提示） */
  silent?: boolean
  /** 用户在系统对话框点取消时静默返回，不弹「cancelled by user」 */
  suppressCancel?: boolean
  /** getErrorMessage 为空时的兜底文案，如 t('credLoadFailed') */
  fallback?: string
  /** 完整覆盖消息（用于前缀拼接等自定义文案） */
  message?: string
}

/**
 * 上报一次错误。调用点只需给出 tag，其余交给这里。
 * @returns 是否实际弹出了 toast（suppressCancel / silent 时为 false）
 */
export function reportError(e: unknown, tag: string, opts: ReportErrorOptions = {}): boolean {
  if (opts.silent) {
    logErr(tag, e)
    return false
  }
  if (opts.suppressCancel && isDialogCanceled(e)) return false

  const toast = useToast()
  const text = opts.message ?? getErrorMessage(e) ?? ''
  toast.error(text || opts.fallback || '')
  logErr(tag, e)
  return true
}
