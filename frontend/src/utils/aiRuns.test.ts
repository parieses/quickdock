import { describe, expect, it } from 'vitest'
import { pickActiveRun } from './aiRuns'

const run = (rid: string, convId: string) => ({ rid, convId })

describe('pickActiveRun', () => {
  it('有会话 id 时只认这个会话自己的流', () => {
    const runs = [run('r1', 'a'), run('r2', 'b')]
    expect(pickActiveRun(runs, 'b', '')?.rid).toBe('r2')
    // 这一页对应的会话没在跑：不能退化成「随便挑一条」
    expect(pickActiveRun(runs, 'c', '')).toBeUndefined()
  })

  it('落地页只认这张草稿自己的流，不被另一条新任务顶掉', () => {
    // 连开两条新任务：第一条已经拿到 id 在后台跑，第二条还在等 conv 事件。
    const runs = [run('r1', 'a'), run('r2', '')]
    expect(pickActiveRun(runs, '', 'r2')?.rid).toBe('r2')
  })

  it('点开新建任务后不认上一条草稿：落地页必须是空的', () => {
    const runs = [run('r1', '')]
    expect(pickActiveRun(runs, '', '')).toBeUndefined()
  })
})
