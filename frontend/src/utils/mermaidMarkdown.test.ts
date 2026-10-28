import { describe, it, expect } from 'vitest'
import { isMermaidFenceLang, mermaidPlaceholder, setMermaidSourceMode, type MermaidLabels } from './mermaidMarkdown'

// utils 层没有 i18n 上下文，文案是调用方传进来的；测试里给一份同形状的假文案即可。
const L: MermaidLabels = {
  error: 'E',
  source: 'View source',
  diagram: 'View diagram',
  fold: 'Collapse',
  expand: 'Expand',
}

describe('isMermaidFenceLang', () => {
  it('认出 mermaid 与 mmd（大小写、首尾空格都算）', () => {
    expect(isMermaidFenceLang('mermaid')).toBe(true)
    expect(isMermaidFenceLang('Mermaid')).toBe(true)
    expect(isMermaidFenceLang(' mmd ')).toBe(true)
  })

  it('其它语言标注不算图', () => {
    expect(isMermaidFenceLang('graph')).toBe(false)
    expect(isMermaidFenceLang('mermaid2')).toBe(false)
    expect(isMermaidFenceLang('')).toBe(false)
    expect(isMermaidFenceLang(undefined as unknown as string)).toBe(false)
  })
})

describe('mermaidPlaceholder', () => {
  it('源码进 <pre><code>，且尖括号被转义（图的 <br> 不能当 HTML 解析）', () => {
    const html = mermaidPlaceholder('graph TD\nA[开始] --> B{头数<br>脚数}', L)
    expect(html).toContain('<pre class="md-mermaid-src"><code>')
    expect(html).toContain('&lt;br&gt;')
    expect(html).not.toContain('<br>')
  })

  it('带 data-mermaid 标记（渲染扫描的入口）与失败文案', () => {
    const html = mermaidPlaceholder('graph TD\nA-->B', { ...L, error: '图渲染失败' })
    expect(html).toContain('data-mermaid="1"')
    expect(html).toContain('data-mermaid-err="图渲染失败"')
  })

  it('文案里的引号也转义，不会截断属性', () => {
    const html = mermaidPlaceholder('graph TD\nA-->B', { ...L, error: '他说"失败"了' })
    expect(html).toContain('data-mermaid-err="他说&quot;失败&quot;了"')
  })

  it('去掉结尾换行（围栏原文总带一个，留着会在 pre 里多一行空行）', () => {
    const html = mermaidPlaceholder('graph TD\nA-->B\n', L)
    expect(html).toContain('A--&gt;B</code>')
  })

  it('工具栏、源码、图形区三者同时在（draw 只替换图形区，另两个必须留着）', () => {
    const html = mermaidPlaceholder('graph TD\nA-->B', L)
    // 工具栏两个按钮
    expect(html).toContain('data-mermaid-act="src"')
    expect(html).toContain('data-mermaid-act="fold"')
    // 源码 <pre> 常驻：看图时靠 CSS 藏起来，切回源码视图不重建 DOM
    expect(html).toContain('class="md-mermaid-src"')
    // 空图形区：draw() 成功后往这里塞 SVG，不会碰到工具栏
    expect(html).toContain('<div class="md-mermaid-svg"></div>')
  })

  it('按钮文案传的是「点下去会做什么」，不是当前状态', () => {
    const html = mermaidPlaceholder('graph TD\nA-->B', L)
    // 默认是看源码状态（默认显示代码）→ 源码按钮的文案是「查看图」
    expect(html).toContain('title="View diagram"')
    expect(html).toContain('title="Collapse"')
    // 「查看源码」「展开」是切换之后才用得上的文案，初始 HTML 里不该出现
    expect(html).not.toContain('title="View source"')
    expect(html).not.toContain('title="Expand"')
  })

  it('占位块默认带 data-mermaid-src（即默认显示源码而非图形）', () => {
    const html = mermaidPlaceholder('graph TD\nA-->B', L)
    expect(html).toContain('data-mermaid-src="1"')
  })
})

describe('setMermaidSourceMode', () => {
  it('进入源码视图时按钮文案切到「查看图」，回到图形视图时切回「查看源码」', () => {
    // 不依赖真实 DOM：setMermaidSourceMode 只用 setAttribute / removeAttribute / querySelector
    const btnAttrs: Record<string, string> = {}
    const btn = { setAttribute: (k: string, v: string) => { btnAttrs[k] = v } }
    const wrapAttrs: Record<string, string> = { 'data-mermaid-src': '1' }
    const wrap = {
      setAttribute: (k: string, v: string) => { wrapAttrs[k] = v },
      removeAttribute: (k: string) => { delete wrapAttrs[k] },
      querySelector: () => btn,
    } as unknown as HTMLElement
    // 占位块默认已是源码视图
    expect(wrapAttrs['data-mermaid-src']).toBe('1')
    // 切到图形视图
    setMermaidSourceMode(wrap, false, L)
    expect(wrapAttrs['data-mermaid-src']).toBeUndefined()
    expect(btnAttrs['title']).toBe('View source')
    // 再切回源码视图
    setMermaidSourceMode(wrap, true, L)
    expect(wrapAttrs['data-mermaid-src']).toBe('1')
    expect(btnAttrs['title']).toBe('View diagram')
  })
})
