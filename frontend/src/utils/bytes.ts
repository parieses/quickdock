// 字节数 → 人类可读。全站唯一口径。
//
// 规则（1024 进位，B/KB/MB/GB/TB/PB）：
// - B 档取整；其余档保留一位小数，且整数不显示 ".0"（"5 MB" 而不是 "5.0 MB"）
// - 数值 ≥ 100 时取整（"512 MB" 不写 "512.3 MB"，避免长数字挤版面）
// - 0 / 负数 / NaN 由调用方决定语义：默认 "0 B"，表示「未知」时传 '—'，
//   需要「不展示」时传 ''。
const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function formatBytes(n: number, zero = '0 B'): string {
  if (!Number.isFinite(n) || n <= 0) return zero
  let v = n
  let i = 0
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  if (i === 0) return `${Math.round(v)} B`
  const text = v >= 100 ? String(Math.round(v)) : v.toFixed(1).replace(/\.0$/, '')
  return `${text} ${UNITS[i]}`
}
