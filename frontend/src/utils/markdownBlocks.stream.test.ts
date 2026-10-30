/**
 * 流式增量渲染的行为测试。
 *
 * 关键断言：
 * 1. 增量渲染的结果 == 整段渲染的结果（任何时刻都不出现「内容变形」）
 * 2. 已完成块的 HTML 在后续帧中保持同一字符串引用 —— Vue 的 patchElement
 *    对 innerHTML 只在值变化时赋值（见 @vue/runtime-core patchProps：
 *    `next !== prev` 才调 hostPatchProp），引用不变即不产生 DOM 写入。
 * 3. 块分裂（最后一块长大后变成两块）时，已完成块不会错位。
 */
import { describe, test, expect } from 'vitest'
import { Marked } from 'marked'
import { splitMarkdownBlocks } from './markdownBlocks'

const md = new Marked({ breaks: true, gfm: true })

/** 复刻 AIPage 的块缓存 */
class BlockCache {
  private map = new Map<string, string>()
  render(raw: string): string {
    let h = this.map.get(raw)
    if (h === undefined) {
      h = md.parse(raw, { async: false }) as string
      this.map.set(raw, h)
    }
    return h
  }
}

interface LiveBlock { raw: string; html: string }

/** 复刻 AIPage 的 renderLiveBlocks */
function renderLive(blocks: LiveBlock[], src: string, cache: BlockCache): void {
  const raws = splitMarkdownBlocks(src)
  const n = raws.length
  const doneCount = n > 0 ? n - 1 : 0
  let common = 0
  const limit = Math.min(doneCount, blocks.length)
  while (common < limit && blocks[common].raw === raws[common]) {
    common++
  }
  for (let i = common; i < doneCount; i++) {
    const html = cache.render(raws[i])
    if (i < blocks.length) {
      if (blocks[i].html !== html) blocks[i].html = html
      blocks[i].raw = raws[i]
    } else {
      blocks.push({ raw: raws[i], html })
    }
  }
  if (n > 0) {
    const html = cache.render(raws[n - 1])
    if (doneCount < blocks.length) {
      if (blocks[doneCount].html !== html) blocks[doneCount].html = html
      blocks[doneCount].raw = raws[n - 1]
    } else {
      blocks.push({ raw: raws[n - 1], html })
    }
  }
  if (blocks.length > n) blocks.length = n
}

const DOC = `# 标题

第一段，包含 **粗体**。

- 列表一
- 列表二

\`\`\`go
func main() {}
\`\`\`

> 引用

结尾段落。
`

/** 以固定步长模拟流式增量到达（末帧必须是全文，否则测的不是完整流） */
function framesOf(doc: string, step: number): string[] {
  const out: string[] = []
  for (let i = step; i < doc.length; i += step) out.push(doc.slice(0, i))
  out.push(doc)
  return out
}

const norm = (s: string) => s.replace(/\s+/g, ' ').trim()

describe('流式增量渲染', () => {
  test('增量渲染在每一帧都与整段渲染一致', () => {
    const cache = new BlockCache()
    const blocks: LiveBlock[] = []
    for (const src of framesOf(DOC, 7)) {
      renderLive(blocks, src, cache)
      const joined = blocks.map((b) => b.html).join('')
      const whole = md.parse(src, { async: false }) as string
      expect(norm(joined), `在「${JSON.stringify(src.slice(-24))}」处不一致`).toBe(norm(whole))
    }
  })

  test('各种到达步长下都保持一致', () => {
    for (const step of [1, 2, 3, 5, 7, 11, 13, 17, 23, 29, 50]) {
      const cache = new BlockCache()
      const blocks: LiveBlock[] = []
      for (const src of framesOf(DOC, step)) {
        renderLive(blocks, src, cache)
        const joined = blocks.map((b) => b.html).join('')
        const whole = md.parse(src, { async: false }) as string
        expect(norm(joined), `step=${step} 处不一致`).toBe(norm(whole))
      }
    }
  })

  test('已完成块的 HTML 保持同一字符串引用（不触发 DOM 写入）', () => {
    const cache = new BlockCache()
    const blocks: LiveBlock[] = []
    const frames = framesOf(DOC, 7)
    let checked = false
    for (let i = 0; i < frames.length && !checked; i++) {
      renderLive(blocks, frames[i], cache)
      if (blocks.length < 3) continue
      const before = blocks.slice(0, blocks.length - 1).map((b) => b.html)
      for (let j = i + 1; j < Math.min(i + 6, frames.length); j++) {
        renderLive(blocks, frames[j], cache)
      }
      const after = blocks.slice(0, before.length).map((b) => b.html)
      for (let k = 0; k < before.length; k++) {
        expect(after[k], `第 ${k} 块引用发生变化，会导致 DOM 重写`).toBe(before[k])
      }
      checked = true
    }
    expect(checked, '未找到足够多的块用于验证').toBe(true)
  })

  test('最后一块分裂时已完成块不错位', () => {
    const cache = new BlockCache()
    const blocks: LiveBlock[] = []
    renderLive(blocks, '# 标题\n\n第一段。', cache)
    expect(blocks).toHaveLength(2)
    const firstHtml = blocks[0].html
    renderLive(blocks, '# 标题\n\n第一段。\n\n新段落。', cache)
    expect(blocks).toHaveLength(3)
    expect(blocks[0].html).toBe(firstHtml)
    expect(blocks[0].html).toContain('标题')
    expect(blocks[2].html).toContain('新段落')
  })

  test('流式结束后与落地消息的渲染结果一致', () => {
    const cache = new BlockCache()
    const blocks: LiveBlock[] = []
    for (const src of framesOf(DOC, 5)) renderLive(blocks, src, cache)
    const streamed = blocks.map((b) => b.html).join('')
    const landed = splitMarkdownBlocks(DOC)
      .map((b) => md.parse(b, { async: false }) as string)
      .join('')
    expect(norm(streamed)).toBe(norm(landed))
  })
})
