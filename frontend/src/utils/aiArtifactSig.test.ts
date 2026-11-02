import { describe, expect, it } from 'vitest'
import { artifactSigOf, changedArtifactPaths } from './aiArtifactSig'
import type { AIArtifact } from '../types/ai'

function art(p: Partial<AIArtifact>): AIArtifact {
  return { path: 'E:\\p\\a.md', name: 'a.md', op: 'write', callId: 'c1', done: true, ...p }
}

describe('artifactSigOf', () => {
  it('一条产物一个「路径#调用id」，顺序照原样', () => {
    expect(artifactSigOf([art({}), art({ path: 'E:\\p\\b.md', callId: 'c2' })])).toBe(
      'E:\\p\\a.md#c1\nE:\\p\\b.md#c2',
    )
  })

  it('没跑完的调用不进签名：磁盘上可能只有半截内容', () => {
    expect(artifactSigOf([art({ done: false })])).toBe('')
    // 历史产物没有 done 字段也算跑完，不能因此被漏掉
    expect(artifactSigOf([art({ done: undefined })])).toBe('E:\\p\\a.md#c1')
  })

  it('空列表是空串', () => {
    expect(artifactSigOf([])).toBe('')
  })
})

describe('changedArtifactPaths', () => {
  it('新出现的产物算变化', () => {
    const prev = artifactSigOf([art({})])
    const next = artifactSigOf([art({}), art({ path: 'E:\\p\\b.md', callId: 'c9' })])
    expect(changedArtifactPaths(prev, next)).toEqual(['E:\\p\\b.md'])
  })

  it('同一个文件被改第二次也认得出（路径集合没变，调用 id 变了）', () => {
    const prev = artifactSigOf([art({ callId: 'c1' })])
    const next = artifactSigOf([art({ callId: 'c2', op: 'edit' })])
    expect(changedArtifactPaths(prev, next)).toEqual(['E:\\p\\a.md'])
  })

  it('签名没变就不该重读：流式期间每条实时输出都会重算一次产物', () => {
    const a = artifactSigOf([art({})])
    const b = artifactSigOf([art({})])
    expect(changedArtifactPaths(a, b)).toEqual([])
  })

  it('路径里带 # 也能正确切分（按最后一个 # 拆）', () => {
    const prev = artifactSigOf([art({ path: 'E:\\p\\#x\\a.md', callId: 'c1' })])
    const next = artifactSigOf([art({ path: 'E:\\p\\#x\\a.md', callId: 'c2' })])
    expect(changedArtifactPaths(prev, next)).toEqual(['E:\\p\\#x\\a.md'])
  })

  it('上一版为空（首次拿到产物）时全部算变化', () => {
    expect(changedArtifactPaths('', artifactSigOf([art({})]))).toEqual(['E:\\p\\a.md'])
  })
})
