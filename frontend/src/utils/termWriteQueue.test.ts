import { describe, expect, it } from 'vitest'
import { createWriteQueue } from './termWriteQueue'

/** 手动调度器：把 rAF 变成「显式触发」，测试里才能精确控制帧边界。 */
function manualScheduler() {
  const callbacks: Array<() => void> = []
  return {
    schedule: (cb: () => void) => {
      callbacks.push(cb)
    },
    /** 跑一帧 */
    tick() {
      const cb = callbacks.shift()
      cb?.()
    },
    /** 一直跑到没有待执行任务 */
    runAll() {
      let guard = 0
      while (callbacks.length > 0) {
        callbacks.shift()!()
        if (++guard > 1000) throw new Error('调度器没有收敛（疑似死循环）')
      }
    },
    pending: () => callbacks.length,
  }
}

function concat(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((n, c) => n + c.byteLength, 0)
  const out = new Uint8Array(total)
  let off = 0
  for (const c of chunks) {
    out.set(c, off)
    off += c.byteLength
  }
  return out
}

describe('createWriteQueue', () => {
  it('单块未超预算时一帧写完', () => {
    const s = manualScheduler()
    const written: Uint8Array[] = []
    const q = createWriteQueue((c) => written.push(c), { budget: 16, schedule: s.schedule })

    q.enqueue(new Uint8Array([1, 2, 3]))
    expect(written.length).toBe(0) // 还没到帧边界
    expect(s.pending()).toBe(1)

    s.runAll()
    expect(written.length).toBe(1)
    expect(Array.from(written[0])).toEqual([1, 2, 3])
    expect(q.backlog()).toBe(0)
  })

  it('超过预算时按字节切分，且拼接结果与原始一致', () => {
    const s = manualScheduler()
    const written: Uint8Array[] = []
    const q = createWriteQueue((c) => written.push(c), { budget: 8, schedule: s.schedule })

    const data = new Uint8Array(20)
    for (let i = 0; i < data.length; i++) data[i] = i
    q.enqueue(data)
    s.runAll()

    expect(written.map((c) => c.byteLength)).toEqual([8, 8, 4])
    expect(Array.from(concat(written))).toEqual(Array.from(data))
  })

  it('多块入队按顺序写，且同帧内累计不超过预算', () => {
    const s = manualScheduler()
    const written: Uint8Array[] = []
    const q = createWriteQueue((c) => written.push(c), { budget: 10, schedule: s.schedule })

    q.enqueue(new Uint8Array([1, 2, 3]))
    q.enqueue(new Uint8Array([4, 5, 6]))
    q.enqueue(new Uint8Array([7, 8, 9]))
    expect(s.pending()).toBe(1) // 三次入队只调度一帧

    s.tick()
    expect(Array.from(concat(written))).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9])
    expect(s.pending()).toBe(0)
  })

  it('中文跨帧被劈开也不会丢字节（上层按字节流拼回）', () => {
    const s = manualScheduler()
    const written: Uint8Array[] = []
    const q = createWriteQueue((c) => written.push(c), { budget: 2, schedule: s.schedule })

    // "中" 的 UTF-8 是 3 字节，budget=2 必然把它劈成 2+1
    const bytes = new TextEncoder().encode('中文')
    q.enqueue(bytes)
    s.runAll()

    expect(written[0].byteLength).toBe(2)
    expect(Array.from(concat(written))).toEqual(Array.from(bytes))
    // 单独看第一天帧是半个字符——这正是为什么前端不能逐帧 decode
    expect(() => new TextDecoder('utf-8', { fatal: true }).decode(written[0])).toThrow()
  })

  it('积压超限时丢旧保新', () => {
    const s = manualScheduler()
    const written: Uint8Array[] = []
    const q = createWriteQueue((c) => written.push(c), {
      budget: 4,
      maxBacklog: 10,
      schedule: s.schedule,
    })

    q.enqueue(new Uint8Array([1, 1, 1, 1])) // 4
    q.enqueue(new Uint8Array([2, 2, 2, 2])) // 8
    q.enqueue(new Uint8Array([3, 3, 3, 3])) // 12 > 10 → 清空后只留最新的 4
    expect(q.backlog()).toBe(4)

    s.runAll()
    expect(Array.from(concat(written))).toEqual([3, 3, 3, 3])
  })

  it('dispose 之后不再写入', () => {
    const s = manualScheduler()
    const written: Uint8Array[] = []
    const q = createWriteQueue((c) => written.push(c), { budget: 8, schedule: s.schedule })

    q.enqueue(new Uint8Array([1, 2, 3]))
    q.dispose()
    s.runAll()

    expect(written.length).toBe(0)
    expect(q.backlog()).toBe(0)

    q.enqueue(new Uint8Array([4, 5, 6]))
    s.runAll()
    expect(written.length).toBe(0)
  })

  it('空块不入队也不触发调度', () => {
    const s = manualScheduler()
    const q = createWriteQueue(() => {}, { schedule: s.schedule })
    q.enqueue(new Uint8Array(0))
    expect(s.pending()).toBe(0)
  })
})
