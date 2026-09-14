import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { debounce, throttle } from './debounce'

describe('debounce', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('延迟内不执行，超时后执行一次', () => {
    const fn = vi.fn()
    const d = debounce(fn, 100)
    d()
    expect(fn).not.toHaveBeenCalled()
    vi.advanceTimersByTime(100)
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('连续调用只执行最后一次（携带最后一次的参数）', () => {
    const fn = vi.fn()
    const d = debounce(fn, 100)
    d('a')
    d('b')
    d('c')
    vi.advanceTimersByTime(100)
    expect(fn).toHaveBeenCalledTimes(1)
    expect(fn).toHaveBeenCalledWith('c')
  })

  it('默认 delay 为 300ms', () => {
    const fn = vi.fn()
    debounce(fn)()
    vi.advanceTimersByTime(299)
    expect(fn).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(fn).toHaveBeenCalledTimes(1)
  })
})

describe('throttle', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('首次调用立即执行', () => {
    const fn = vi.fn()
    const t = throttle(fn, 100)
    t()
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('窗口内的重复调用被合并，窗口结束时补一次尾部执行', () => {
    const fn = vi.fn()
    const t = throttle(fn, 100)
    t('a')
    vi.advanceTimersByTime(30)
    t('b')
    vi.advanceTimersByTime(30)
    t('c')
    // 窗口内只跑了首次，尾部那次还没到点
    expect(fn).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(70)
    expect(fn).toHaveBeenCalledTimes(2)
    // 尾部执行用的是「窗口内第一次被推迟的调用」的参数：throttle 只在无 timer 时
    // 登记一次尾部回调，窗口内更晚的调用（'c'）会被丢弃，这是本实现的语义。
    expect(fn).toHaveBeenLastCalledWith('b')
  })

  it('超过窗口后可再次立即执行', () => {
    const fn = vi.fn()
    const t = throttle(fn, 100)
    t()
    vi.advanceTimersByTime(150)
    t()
    expect(fn).toHaveBeenCalledTimes(2)
  })
})
