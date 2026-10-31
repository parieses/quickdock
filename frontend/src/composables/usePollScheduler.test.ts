import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePollScheduler } from './usePollScheduler'

/**
 * 轮询调度器的回归测试。
 *
 * 守的是四条「请求量与可见性」的约定，都是原先写在页面里、靠人肉 review 保证的：
 *   1. 周期轮询按 interval 执行，stop 之后不再跑；
 *   2. requestNow 在合并窗口内只跑一次（一次操作里连打多个刷新不会放大成多轮）；
 *   3. 正在跑的时候 requestNow 不并发、不丢弃，而是本轮结束后补跑一次；
 *   4. pause 之后不跑，resume 立即补一轮。
 */

function makeRun(delay = 0) {
  let calls = 0
  const run = vi.fn(async () => {
    calls++
    if (delay > 0) await new Promise((r) => setTimeout(r, delay))
  })
  return { run, count: () => calls }
}

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

describe('usePollScheduler', () => {
  it('按间隔轮询，stop 之后停止', async () => {
    const { run, count } = makeRun()
    const s = usePollScheduler(run, { interval: 1000, pauseWhenHidden: false })
    s.start()
    await vi.advanceTimersByTimeAsync(3500)
    expect(count()).toBe(3)
    s.stop()
    await vi.advanceTimersByTimeAsync(5000)
    expect(count()).toBe(3)
  })

  it('合并窗口内的多次 requestNow 只跑一次', async () => {
    const { run, count } = makeRun()
    const s = usePollScheduler(run, { interval: 60_000, minGap: 600, pauseWhenHidden: false })
    s.start()
    // 首轮周期在 60s 后，这里只考察 requestNow
    s.requestNow()
    s.requestNow()
    s.requestNow()
    await vi.advanceTimersByTimeAsync(700)
    expect(count()).toBe(1)
    s.stop()
  })

  it('上一轮未结束时 requestNow 会补跑一轮而不是被丢弃', async () => {
    const { run, count } = makeRun(500)
    const s = usePollScheduler(run, { interval: 60_000, minGap: 0, pauseWhenHidden: false })
    s.start()
    s.requestNow()
    await vi.advanceTimersByTimeAsync(100) // 第一轮已开始、尚未结束
    s.requestNow()
    s.requestNow()
    await vi.advanceTimersByTimeAsync(2000)
    expect(count()).toBe(2)
    s.stop()
  })

  it('pause 后不跑，resume 立即补一轮', async () => {
    const { run, count } = makeRun()
    const s = usePollScheduler(run, { interval: 1000, pauseWhenHidden: false })
    s.start()
    await vi.advanceTimersByTimeAsync(1000)
    expect(count()).toBe(1)

    s.pause()
    await vi.advanceTimersByTimeAsync(5000)
    expect(count()).toBe(1)

    s.resume()
    await vi.advanceTimersByTimeAsync(10)
    expect(count()).toBe(2)
    s.stop()
  })

  it('runNow 等待本轮结果，且不与在跑的轮次并发', async () => {
    let active = 0
    let maxActive = 0
    const run = vi.fn(async () => {
      active++
      maxActive = Math.max(maxActive, active)
      await new Promise((r) => setTimeout(r, 300))
      active--
    })
    const s = usePollScheduler(run, { interval: 60_000, minGap: 600, pauseWhenHidden: false })
    s.start()
    const first = s.runNow()
    const second = s.runNow() // 撞上在跑的一轮：不并发，排在其后
    const both = Promise.all([first, second])
    await vi.advanceTimersByTimeAsync(1200)
    await both
    expect(maxActive).toBe(1)
    expect(run).toHaveBeenCalledTimes(2)
    s.stop()
  })

  it('单轮抛错不会让调度器停摆', async () => {
    let calls = 0
    const run = vi.fn(async () => {
      calls++
      if (calls === 1) throw new Error('boom')
    })
    const s = usePollScheduler(run, { interval: 1000, pauseWhenHidden: false })
    s.start()
    await vi.advanceTimersByTimeAsync(2500)
    expect(calls).toBe(2)
    s.stop()
  })
})