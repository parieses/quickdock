/**
 * 数学公式渲染的边界测试。
 *
 * 这里只测「会不会误伤正文」和「半截公式崩不崩」——真正的排版长什么样靠截图看，
 * 断言 HTML 里有没有 katex 结构就够了。
 */
import { describe, it, expect } from 'vitest'
import { Marked } from 'marked'
import { isMathFenceLang, mathExtension, renderMath } from './mathMarkdown'

function render(src: string): string {
  const md = new Marked({ breaks: true, gfm: true })
  md.use(mathExtension())
  return md.parse(src, { async: false }) as string
}

describe('mathMarkdown', () => {
  it('块级 \\[...\\] 独占一块时按公式渲染', () => {
    const html = render('\\[\n\\sum_{n=1}^{\\infty}\\frac{1}{n^2}\n\\]')
    expect(html).toContain('katex-display')
    // 源码保存在 data-tex（供右键「复制源码」），而不是裸 LaTeX 散落在可见文本里
    expect(html).toContain('data-tex="\\sum')
  })

  it('公式紧跟在正文行后面（中间没有空行）也能渲染', () => {
    // 这是实际踩到的场景：模型把「1. 生成函数法：利用已知恒等式」和公式写在一起，
    // marked 的 paragraph 规则会把整段吞掉，只有 inline tokenizer 能救。
    const html = render('1. 生成函数法：利用已知恒等式\n\\[\n\\sum_{n=1}^\\infty H_n x^n = -\\frac{\\ln(1-x)}{1-x}\n\\]')
    expect(html).toContain('katex')
    // 源码进入 data-tex（供右键「复制源码」），而非裸 \frac 散落可见文本
    expect(html).toContain('data-tex="\\sum')
  })

  it('行内 \\(...\\) 与 $...$ 都能渲染', () => {
    expect(render('其中 \\(H_n\\) 为调和数')).toContain('katex')
    expect(render('其中 $H_n$ 为调和数')).toContain('katex')
    expect(render('$$E=mc^2$$')).toContain('katex-display')
  })

  it('钱和普通美元符号不会被当成公式', () => {
    // 判断依据：闭定界符后紧跟数字 → 价格区间
    const price = render('预算 $5 到 $10 之间')
    expect(price).not.toContain('katex')
    expect(price).toContain('$5')
    // 开定界符后是空白 → 不是公式
    expect(render('价格是 $ 20 左右')).not.toContain('katex')
  })

  it('转义的 \\$ 仍是普通文字', () => {
    const html = render('花费 \\$5')
    expect(html).not.toContain('katex')
    expect(html).toContain('$5')
  })

  it('围栏里的 latex 标注会被识别成公式标注', () => {
    expect(isMathFenceLang('latex')).toBe(true)
    expect(isMathFenceLang('TeX')).toBe(true)
    expect(isMathFenceLang('math')).toBe(true)
    expect(isMathFenceLang('bash')).toBe(false)
    expect(isMathFenceLang('')).toBe(false)
  })

  it('半截公式 / 非法语法降级成原始源码，不抛错', () => {
    // 流式期间每一帧都可能这样，抛一次整条消息就白屏
    expect(() => render('\\[\\frac{1}{')).not.toThrow()
    const raw = renderMath('\\frac{1}{\\notARealCommand', true)
    expect(raw).toContain('md-math-raw')
    expect(raw).not.toContain('katex-html')
    expect(raw).toContain('\\notARealCommand')
  })

  it('代码围栏里的 $ 不受影响（公式只在正文层生效）', () => {
    const html = render('```bash\nawk \'{print $1}\'\n```')
    expect(html).not.toContain('katex')
  })
})
