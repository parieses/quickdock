import { describe, expect, it } from 'vitest'
import { resolveConvDir } from './aiConvDir'

const space = (id: string, cwd: string) => ({ id, cwd })

describe('resolveConvDir', () => {
  it('有会话目录就用会话目录，不看空间', () => {
    const spaces = [space('s1', 'E:\\proj')]
    expect(resolveConvDir({ cwd: 'E:\\work\\任务A', space_id: 's1' }, spaces)).toBe('E:\\work\\任务A')
  })

  it('会话目录为空时回退到所属空间的目录', () => {
    // 老数据：只记了 space_id，cwd 还没落库
    const spaces = [space('s1', 'E:\\proj')]
    expect(resolveConvDir({ cwd: '', space_id: 's1' }, spaces)).toBe('E:\\proj')
  })

  it('纯空白的 cwd 当没写，仍然回退空间', () => {
    const spaces = [space('s1', 'E:\\proj')]
    expect(resolveConvDir({ cwd: '   ', space_id: 's1' }, spaces)).toBe('E:\\proj')
  })

  it('目录两端空白被裁掉：拼出来带空格的路径系统会当成另一个目录', () => {
    expect(resolveConvDir({ cwd: ' E:\\proj ' }, [])).toBe('E:\\proj')
    expect(resolveConvDir({ cwd: '', space_id: 's1' }, [space('s1', ' E:\\proj ')])).toBe('E:\\proj')
  })

  it('空间已被删 / 还是普通任务：返回空串，交给调用方提示而不是打开一个空路径', () => {
    expect(resolveConvDir({ cwd: '', space_id: 'gone' }, [space('s1', 'E:\\proj')])).toBe('')
    expect(resolveConvDir({ cwd: '', space_id: '' }, [])).toBe('')
    expect(resolveConvDir(null, [])).toBe('')
  })

  it('会话目录优先于空间目录，即使空间换了目录', () => {
    // 换项目是改空间，但已经单独绑过目录的会话不该被一起带过去
    expect(resolveConvDir({ cwd: 'E:\\old', space_id: 's1' }, [space('s1', 'E:\\new')])).toBe('E:\\old')
  })
})
