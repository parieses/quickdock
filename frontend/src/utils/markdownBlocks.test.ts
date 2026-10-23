/**
 * markdownBlocks 切分逻辑的等价性测试。
 *
 * 核心断言：分块渲染的结果必须与整段渲染一致。
 * 否则用户在流式过程中会看到内容「变形」（突然多出一个列表、引用变纯文本等）。
 */
import { describe, test, expect } from 'vitest'
import { Marked } from 'marked'
import { splitMarkdownBlocks } from './markdownBlocks'

const md = new Marked({ breaks: true, gfm: true })

// 归一化：忽略空白差异（分块会引入换行，整段不会）
function norm(s: string): string {
  return s.replace(/\s+/g, ' ').trim()
}

/** 分块渲染的结果（拼接各块 HTML） */
function renderByBlocks(src: string): string {
  return splitMarkdownBlocks(src).map((b) => md.parse(b, { async: false }) as string).join('')
}

/** 整段渲染的结果 */
function renderWhole(src: string): string {
  return md.parse(src, { async: false }) as string
}

const CASES: Record<string, string> = {
  heading: '# 标题\n\n段落\n',
  paragraph: '第一段\n\n第二段\n',
  // 松散列表：- a 与 - b 之间有空行，markdown 渲染成单个 <ul>
  looseList: '- a\n\n- b\n\n后文\n',
  orderedList: '1. 一\n\n2. 二\n\n后文\n',
  // 列表项的缩进续段
  listContinuation: '- item\n\n  续段\n\n- item2\n',
  nestedList: '- a\n  - b\n\n后文\n',
  // 引用块
  blockquote: '> 引用一\n\n> 引用二\n\n后文\n',
  // 围栏代码块
  codeFence: '```go\nfunc main() {}\n```\n\n后文\n',
  codeFenceUnclosed: '```go\nfunc main() {\n',
  codeFenceTilde: '~~~\nplain\n~~~\n\n后文\n',
  // 表格
  table: '| a | b |\n| --- | --- |\n| 1 | 2 |\n\n后文\n',
  // setext 标题（=== 下划线）
  setext: '标题\n===\n\n段落\n',
  // 分隔线
  hr: 'a\n\n---\n\nb\n',
  // 引用式链接定义：定义与引用必须同块
  refDef: '# T\n\n看 [ref] 这个\n\n[ref]: https://x.com\n',
  refDefBefore: '看 [ref] 这个\n\n[ref]: https://x.com\n\n后文\n',
  // 图片
  image: '![alt](https://x.com/a.png)\n\n后文\n',
  // 混合大块
  mixed: `# 标题

段落一，含 **粗体** 和 \`行内代码\`。

- 列表项一
- 列表项二

\`\`\`bash
npm run build
\`\`\`

> 引用内容

| 列 A | 列 B |
| --- | --- |
| 1 | 2 |

结尾段落。
`,
}

describe('splitMarkdownBlocks', () => {
  test('分块渲染与整段渲染结果一致', () => {
    for (const [name, src] of Object.entries(CASES)) {
      expect(norm(renderByBlocks(src)), `用例 ${name} 不一致`).toBe(norm(renderWhole(src)))
    }
  })

  test('代码块不会被拦腰截断', () => {
    const src = '```go\nx := 1\n```\n\n后文\n'
    const blocks = splitMarkdownBlocks(src)
    expect(blocks).toHaveLength(2)
    expect(blocks[0]).toContain('```')
  })

  test('未闭合的围栏代码块延伸到文本末尾', () => {
    expect(splitMarkdownBlocks('```go\nx := 1\n')).toHaveLength(1)
  })

  test('松散列表不会被拆成两块', () => {
    const src = '- a\n\n- b\n\n后文\n'
    const blocks = splitMarkdownBlocks(src)
    expect(blocks).toHaveLength(2)
    expect(blocks[0]).toContain('- a')
    expect(blocks[0]).toContain('- b')
  })

  test('引用定义出现时整体不切分（定义对全文生效）', () => {
    const src = '看 [ref] 这个\n\n[ref]: https://x.com\n\n后文\n'
    const blocks = splitMarkdownBlocks(src)
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toContain('[ref]:')
  })

  test('空输入返回空数组', () => {
    expect(splitMarkdownBlocks('')).toEqual([])
  })

  test('连续空行不产生空块', () => {
    const blocks = splitMarkdownBlocks('a\n\n\n\nb\n')
    expect(blocks).toHaveLength(2)
    blocks.forEach((b) => expect(b.trim()).not.toBe(''))
  })

  test('分块是源码的无损重组（去掉空行后内容一致）', () => {
    const src = CASES.mixed
    const joined = splitMarkdownBlocks(src).join('\n')
    const strip = (s: string) => s.replace(/\n{2,}/g, '\n').trim()
    expect(strip(joined)).toBe(strip(src))
  })
})
