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
import { createBlockSplitter, splitMarkdownBlocks, type BlockSplitter } from './markdownBlocks'

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

/**
 * 每条流一份切块器，按块数组区分（与 useAiMarkdownBlocks 的 WeakMap 同构）。
 *
 * 必须按流隔离：两条回复的文本可能一模一样（同一个问题跑两遍），共用一个切块器
 * 时，后一条的「已确认前缀」会把前一条顶掉，前一条的块就全乱了。
 */
const splitters = new WeakMap<LiveBlock[], BlockSplitter>()

/** 复刻 AIPage 的 renderLiveBlocks */
function renderLive(blocks: LiveBlock[], src: string, cache: BlockCache): void {
  let sp = splitters.get(blocks)
  if (!sp) {
    sp = createBlockSplitter()
    splitters.set(blocks, sp)
  }
  const raws = sp.feed(src)
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
    // 末尾带上换行：增量切块对「后面还没有内容」的空行不下结论（见
    // createBlockSplitter 的注释），没有这个 \n，第二段还挂在待定区里没定型。
    renderLive(blocks, '# 标题\n\n第一段。\n\n新段落。\n', cache)
    expect(blocks).toHaveLength(3)
    expect(blocks[0].html).toBe(firstHtml)
    expect(blocks[0].html).toContain('标题')
    expect(blocks[2].html).toContain('新段落')
  })

  test('松散列表跨空行不被切开（增量版最容易踩的坑）', () => {
    // `- a` 空行 `- b` 在 markdown 里是同一个列表，从中间切开会渲染成两个 <ul>。
    // 增量切块必须等到「空行后面那个非空行」到了才能判断能不能切。
    const src = '- 一\n\n- 二\n\n- 三\n'
    const sp = createBlockSplitter()
    // 逐字喂入：每一步的块数都不能超过 1（中间三行属于同一个列表）
    for (let i = 1; i <= src.length; i++) {
      const blocks = sp.feed(src.slice(0, i))
      expect(blocks.length, `在「${src.slice(0, i)}」处被切成了 ${blocks.length} 块`).toBe(1)
    }
    expect(sp.feed(src)[0]).toContain('- 二')
  })

  test('围栏代码块内部不被切开', () => {
    const src = '说明\n\n```go\nfunc a() {}\n\nfunc b() {}\n```\n\n结尾\n'
    const sp = createBlockSplitter()
    for (let i = 1; i <= src.length; i++) {
      const blocks = sp.feed(src.slice(0, i))
      // 中途代码块是「未闭合」状态，块里出现一个 ``` 是正常的；不能出现的是
      // 代码块被空行切开（那样话末尾会多出独立的块）。
      expect(blocks.length, `在「${src.slice(0, i)}」处块数异常`).toBeLessThanOrEqual(3)
    }
    const final = sp.feed(src)
    const codeBlock = final.find((b) => b.includes('func a()'))
    expect(codeBlock, '代码块不见了').toBeTruthy()
    // 内部那个空行不能成为切分点
    expect(codeBlock).toContain('func b()')
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
