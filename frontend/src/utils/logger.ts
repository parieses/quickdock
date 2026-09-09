/**
 * 前端日志工具 - 生产环境静默版本
 * 
 * 使用方式：
 * import { log } from '@/utils/logger'
 * log.error('tag', 'message', err)
 * log.warn('tag', 'message')
 * log.debug('tag', 'message')
 */

const IS_DEV = import.meta.env.DEV

function formatTag(tag: string): string {
  return `[QuickDock][${tag}]`
}

export const log = {
  /** 错误日志：开发环境输出，生产环境静默 */
  error: (tag: string, ...args: any[]) => {
    if (!IS_DEV) return
    console.error(formatTag(tag), ...args)
  },

  /** 警告日志：开发环境输出，生产环境静默 */
  warn: (tag: string, ...args: any[]) => {
    if (!IS_DEV) return
    console.warn(formatTag(tag), ...args)
  },

  /** 调试日志：仅开发环境输出 */
  debug: (tag: string, ...args: any[]) => {
    if (!IS_DEV) return
    console.debug(formatTag(tag), ...args)
  },

  /** 信息日志：仅开发环境输出 */
  info: (tag: string, ...args: any[]) => {
    if (!IS_DEV) return
    console.info(formatTag(tag), ...args)
  },

  /** 重要错误：生产环境也输出（用于关键错误） */
  critical: (tag: string, ...args: any[]) => {
    console.error(formatTag(tag), ...args)
  }
}

// 便捷函数，兼容旧代码
export const logErr = (tag: string, ...args: any[]) => log.error(tag, ...args)
export const logWarn = (tag: string, ...args: any[]) => log.warn(tag, ...args)
export const logDebug = (tag: string, ...args: any[]) => log.debug(tag, ...args)
