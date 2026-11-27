import { describe, expect, it } from 'vitest'
import { fmtCost, fmtTokens } from './format'

describe('fmtTokens', () => {
  it('零值与负数区间原样返回', () => {
    expect(fmtTokens(0)).toBe('0')
    expect(fmtTokens(999)).toBe('999')
  })

  it('k 档按数量级切精度：1e4 以上不带小数', () => {
    expect(fmtTokens(1500)).toBe('1.5k')
    expect(fmtTokens(12345)).toBe('12k')
    expect(fmtTokens(999999)).toBe('1000k')
  })

  it('M 档按 1e7 切精度', () => {
    expect(fmtTokens(1_500_000)).toBe('1.50M')
    expect(fmtTokens(12_345_678)).toBe('12.3M')
  })
})

describe('fmtCost', () => {
  it('按数量级自适应小数位，避免小额显示成 ¥0.00', () => {
    expect(fmtCost(0)).toBe('¥0')
    expect(fmtCost(2.5)).toBe('¥2.50')
    expect(fmtCost(0.05)).toBe('¥0.050')
    expect(fmtCost(0.001)).toBe('¥0.0010')
  })
})
