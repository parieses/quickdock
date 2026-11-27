import { describe, expect, it } from 'vitest'
import { formatBytes } from './bytes'

describe('formatBytes', () => {
  it('B 档取整，不出现小数', () => {
    expect(formatBytes(1)).toBe('1 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1023)).toBe('1023 B')
  })

  it('按 1024 进位，整数不显示 .0', () => {
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(2048)).toBe('2 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5 MB')
    expect(formatBytes(1024 ** 4)).toBe('1 TB')
  })

  it('非整数保留一位小数', () => {
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1.26 * 1024 ** 3)).toBe('1.3 GB')
  })

  it('≥100 时取整，避免长数字', () => {
    expect(formatBytes(200 * 1024 * 1024)).toBe('200 MB')
    expect(formatBytes(512.3 * 1024 * 1024)).toBe('512 MB')
  })

  it('零值语义由调用方决定', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(0, '—')).toBe('—')
    expect(formatBytes(0, '')).toBe('')
    // 负数 / NaN / Infinity 与 0 同档，不产出 "NaN B"
    expect(formatBytes(-5)).toBe('0 B')
    expect(formatBytes(Number.NaN, '—')).toBe('—')
    expect(formatBytes(Number.POSITIVE_INFINITY)).toBe('0 B')
  })
})
