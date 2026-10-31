import { describe, expect, it } from 'vitest'
import { cmpVer, formatBytes, formatEta, formatSpeed, percent } from './envFormat'

describe('percent', () => {
  it('按比例向下取整，并封顶 100', () => {
    expect(percent({ written: 1, total: 2 })).toBe(50)
    expect(percent({ written: 999, total: 1000 })).toBe(99)
    // 分母偏小时可能算出 >100，必须封顶，否则进度条溢出
    expect(percent({ written: 120, total: 100 })).toBe(100)
  })

  it('total<=0（未知大小）返回 -1', () => {
    expect(percent({ written: 10, total: 0 })).toBe(-1)
    expect(percent({ written: 10, total: -1 })).toBe(-1)
  })
})

describe('formatSpeed', () => {
  it('按量级选单位', () => {
    expect(formatSpeed(2 * 1048576)).toBe('2.0 MB/s')
    expect(formatSpeed(2048)).toBe('2 KB/s')
    expect(formatSpeed(512)).toBe('512 B/s')
  })

  it('非正数与 NaN 返回空串（不显示 0 B/s 这种噪音）', () => {
    expect(formatSpeed(0)).toBe('')
    expect(formatSpeed(-1)).toBe('')
    expect(formatSpeed(Number.NaN)).toBe('')
  })
})

describe('formatEta', () => {
  it('小时/分钟/秒三档，分钟与秒向上取整', () => {
    expect(formatEta(7200)).toBe('2.0 h')
    expect(formatEta(61)).toBe('2 min')
    expect(formatEta(1.2)).toBe('2 s')
  })

  it('非正数返回空串', () => {
    expect(formatEta(0)).toBe('')
    expect(formatEta(Number.POSITIVE_INFINITY)).toBe('')
  })
})

describe('formatBytes', () => {
  it('>1GB 两位小数、>100MB 取整、更小一位小数', () => {
    expect(formatBytes(2 * 1073741824)).toBe('2.00 GB')
    expect(formatBytes(200 * 1048576)).toBe('200 MB')
    expect(formatBytes(12.34 * 1048576)).toBe('12.3 MB')
  })

  it('0 与空值给破折号而不是 0.0 MB', () => {
    expect(formatBytes(0)).toBe('—')
  })
})

describe('cmpVer', () => {
  it('去掉 v 前缀按数值逐段比较', () => {
    expect(cmpVer('v22.22.2', '22.22.1')).toBe(1)
    expect(cmpVer('1.9.0', '1.10.0')).toBe(-1)
    expect(cmpVer('8.3.20', 'v8.3.20')).toBe(0)
  })

  it('数值比较而非字典序（"10" > "9"）', () => {
    expect(cmpVer('1.10', '1.9')).toBe(1)
  })

  it('缺段按 0 处理', () => {
    expect(cmpVer('1.2', '1.2.0')).toBe(0)
    expect(cmpVer('1.2', '1.2.1')).toBe(-1)
  })
})
