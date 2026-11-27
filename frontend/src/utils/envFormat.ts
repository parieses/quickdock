// 环境页的格式化与比较工具：进度百分比、速度、剩余时间、版本号。
// 字节数统一走 utils/bytes.ts（formatBytes），这里不再保留第二套口径。
//
// 全是无状态的纯函数（不读页面 ui 表、不引 i18n），抽出来才能单测，
// 也让页面只留状态机与编排。

import { formatBytes } from './bytes'

// percent 下载进度百分比；total <= 0（未知大小）返回 -1，调用方据此显示不确定进度条。
export function percent(s: { written: number; total: number }): number {
  if (s.total <= 0) return -1
  return Math.min(100, Math.floor((s.written / s.total) * 100))
}

export function formatSpeed(b: number): string {
  if (!isFinite(b) || b <= 0) return ''
  return formatBytes(b) + '/s'
}

export function formatEta(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return ''
  if (sec >= 3600) return (sec / 3600).toFixed(1) + ' h'
  if (sec >= 60) return Math.ceil(sec / 60) + ' min'
  return Math.ceil(sec) + ' s'
}

// cmpVer 比较版本号。版本号形如 "v22.22.2" / "1.25.0" / "8.3.20"，
// 去掉前缀 v 后按数值逐段比较，只比前三段（够用，且避免第四段脏数据干扰排序）。
export function cmpVer(a: string, b: string): number {
  const pa = String(a || '').trim().replace(/^v/i, '').split('.').map((n) => parseInt(n, 10) || 0)
  const pb = String(b || '').trim().replace(/^v/i, '').split('.').map((n) => parseInt(n, 10) || 0)
  for (let i = 0; i < 3; i++) {
    const x = pa[i] || 0
    const y = pb[i] || 0
    if (x !== y) return x < y ? -1 : 1
  }
  return 0
}
