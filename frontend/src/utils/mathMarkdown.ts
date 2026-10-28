/**
 * Markdown 里的数学公式渲染（KaTeX）。
 *
 * 为什么需要：AI 回答里的公式几乎都是 LaTeX 源码（`\(...\)` / `\[...\]` / `$$...$$`），
 * marked 不认识这些定界符，于是整段 `\sum_{n=1}^{\infty}\frac{...}` 原样当正文输出
 * ——用户看到的就是「样式全乱」。
 *
 * 三条硬约束（决定了下面的实现方式）：
 *
 * 1. **流式期间不能抛错**。回答是逐字到达的，`\frac{1}{` 这种半截公式一定会出现。
 *    KaTeX 的 `throwOnError: false` 会把整条染红并输出一堆 error span，比不渲染更糟；
 *    这里用 `throwOnError: true` + try/catch，失败就原样输出源码，下一帧补齐后自然变成公式。
 *
 * 2. **必须在 marked 的 inline 层也支持**，不能只做 block 层。模型经常把
 *    「1. 生成函数法：利用已知恒等式」和紧随其后的 `\[ ... \]` 写在一起（中间没有空行），
 *    marked 的 paragraph 规则会把整段（含公式）当成一个段落吞掉——block 级 tokenizer
 *    根本没机会看到它。inline tokenizer 拿到的正是段落文本，能正确切开。
 *
 * 3. **`$` 不能乱认**。正文里 `$5 到 $10`、`awk '{print $1}'`（后者在围栏里，走不到这里）
 *    都很常见。行内 `$...$` 采用「开定界符后非空白、闭定界符前非空白、闭定界符后不接数字」
 *    的判定，这是 KaTeX 生态里最通用的一套启发式。
 *
 * 输出统一是 `<span class="md-math ...">`（display 模式靠 CSS 变成块级），
 * 这样即使 inline tokenizer 把它塞进 `<p>` 里也不会产生非法嵌套。
 */
import katex from 'katex'
import type { MarkedExtension, Tokens } from 'marked'
import 'katex/dist/katex.min.css'

/** 围栏代码块的语言标注命中这些时，按公式渲染而不是代码卡片 */
const MATH_FENCE_LANGS = new Set(['latex', 'tex', 'math', 'katex'])

/** 单个公式的长度上限：超过基本可以断定是「`$` 认错了」把半篇文章当公式 */
const MAX_MATH_LEN = 4000

export function isMathFenceLang(lang: string): boolean {
  return MATH_FENCE_LANGS.has(String(lang || '').trim().toLowerCase())
}

function escapeHtml(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

/**
 * 把一段 TeX 渲染成 HTML。失败（公式不完整 / 用了 KaTeX 不支持的宏）时**原样返回源码**，
 * 绝不抛错——流式渲染每帧都会调用它，抛一次整个消息就白屏。
 */
export function renderMath(tex: string, display: boolean): string {
  const body = String(tex ?? '').trim()
  const cls = display ? 'md-math md-math-block' : 'md-math md-math-inline'
  if (!body) return ''
  if (body.length > MAX_MATH_LEN) {
    return `<span class="${cls} md-math-raw">${escapeHtml(display ? `\\[${body}\\]` : body)}</span>`
  }
  try {
    const html = katex.renderToString(body, {
      displayMode: display,
      throwOnError: true,
      // strict:false —— 允许中文标点、`\\` 换行等「不规范但模型爱写」的写法；
      // 开着只会让更多公式掉进 catch 分支变成源码。
      strict: false,
      // 只输出 HTML，不要 MathML：少一半体积，也避开 DOMPurify 对 MathML 标签的取舍。
      output: 'html',
      trust: false,
    })
    return `<span class="${cls}" data-tex="${escapeHtml(body)}">${html}</span>`
  } catch {
    return `<span class="${cls} md-math-raw" data-tex="${escapeHtml(body)}">${escapeHtml(display ? `\\[${body}\\]` : body)}</span>`
  }
}

/** 行内 `$...$`：从 src[0] 开始扫，返回闭合位置；不符合行内公式特征则返回 -1。 */
function scanInlineDollar(src: string): number {
  if (src[0] !== '$' || src[1] === '$') return -1
  const first = src[1]
  // `$ 5` / `$ ` 这种是钱不是公式
  if (first === undefined || /\s/.test(first)) return -1
  for (let i = 1; i < src.length && i <= MAX_MATH_LEN; i++) {
    const ch = src[i]
    if (ch === '\\') {
      i++ // 跳过转义字符（`\$` 是公式内部的美元符号，不是定界符）
      continue
    }
    // 行内公式不跨行：跨行的 `$` 几乎都是「$5 ... 下一行又有 $」这种正文
    if (ch === '\n') return -1
    if (ch !== '$') continue
    if (/\s/.test(src[i - 1])) return -1
    // `$5 到 $10`：闭定界符后面紧跟数字 → 当成价格
    if (/\d/.test(src[i + 1] ?? '')) return -1
    return i
  }
  return -1
}

/**
 * 行内层必须提供 `start`。
 *
 * marked 的 text 规则是 `/^[^\n]+/`（一口气吃到行尾），没有 `start` 的话
 * 一旦前面是普通文字，`$` / `\(` 永远轮不到 tokenizer —— 行内公式在句子里就渲染不出来。
 * `start` 返回的位置会被 marked 用来把 text 截断，从而给公式留出切入点。
 */
function inlineMathStart(src: string): number | undefined {
  let min = -1
  const consider = (i: number) => {
    if (i >= 0 && (min < 0 || i < min)) min = i
  }
  consider(src.indexOf('$'))
  consider(src.indexOf('\\('))
  consider(src.indexOf('\\['))
  consider(src.indexOf('\\begin{'))
  return min >= 0 ? min : undefined
}

/** 块级同理：段落后紧跟 `$$` / `\[` 时，paragraph 会整段吞掉，靠 start 提前截断 */
function blockMathStart(src: string): number | undefined {
  const m = /^ {0,3}(?:\$\$|\\\[|\\begin\{)/m.exec(src)
  return m ? m.index : undefined
}

/** 生成一组 marked 扩展（每次调用返回新对象，避免多个 marked 实例共享内部状态） */
export function mathExtension(): MarkedExtension {  return {
    extensions: [
      // —— 块级：公式独占一块时优先走这里，输出不带 <p> 包裹，间距更干净 ——
      {
        name: 'mathBlockDollar',
        level: 'block',
        // 只要有一个 block 扩展提供 start，整组 block 扩展就都能被 paragraph 之前看到
        start: blockMathStart,
        tokenizer(src: string) {
          const m = /^ {0,3}\$\$([\s\S]+?)\$\$[ \t]*(?:\n+|$)/.exec(src)
          if (!m) return undefined
          return { type: 'mathBlockDollar', raw: m[0], text: m[1] }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), true)
        },
      },
      {
        name: 'mathBlockBracket',
        level: 'block',
        tokenizer(src: string) {
          const m = /^ {0,3}\\\[([\s\S]+?)\\\][ \t]*(?:\n+|$)/.exec(src)
          if (!m) return undefined
          return { type: 'mathBlockBracket', raw: m[0], text: m[1] }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), true)
        },
      },
      {
        name: 'mathBlockEnv',
        level: 'block',
        tokenizer(src: string) {
          // 裸 `\begin{align}...\end{align}`：模型经常不带定界符直接吐出来
          const m = /^ {0,3}(\\begin\{([a-zA-Z*]+)\}[\s\S]*?\\end\{\2\})[ \t]*(?:\n+|$)/.exec(src)
          if (!m) return undefined
          return { type: 'mathBlockEnv', raw: m[0], text: m[1] }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), true)
        },
      },

      // —— 行内：兜住「公式和文字在同一段」的情况（含 paragraph 吞掉整段的情形）——
      {
        name: 'mathInlineBracket',
        level: 'inline',
        // 同上：一个 start 覆盖本组所有行内公式的切入点
        start: inlineMathStart,
        tokenizer(src: string) {
          const m = /^\\\[([\s\S]{1,4000}?)\\\]/.exec(src)
          if (!m) return undefined
          // `\[...\]` 是 display 语义，即使出现在段落里也按块级排
          return { type: 'mathInlineBracket', raw: m[0], text: m[1], display: true }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), token.display !== false)
        },
      },
      {
        name: 'mathInlineParen',
        level: 'inline',
        tokenizer(src: string) {
          const m = /^\\\(([\s\S]{1,4000}?)\\\)/.exec(src)
          if (!m) return undefined
          return { type: 'mathInlineParen', raw: m[0], text: m[1], display: false }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), false)
        },
      },
      {
        name: 'mathInlineDollarDollar',
        level: 'inline',
        tokenizer(src: string) {
          const m = /^\$\$([\s\S]{1,4000}?)\$\$/.exec(src)
          if (!m) return undefined
          return { type: 'mathInlineDollarDollar', raw: m[0], text: m[1], display: true }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), true)
        },
      },
      {
        name: 'mathInlineDollar',
        level: 'inline',
        tokenizer(src: string) {
          const end = scanInlineDollar(src)
          if (end < 0) return undefined
          return { type: 'mathInlineDollar', raw: src.slice(0, end + 1), text: src.slice(1, end), display: false }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), false)
        },
      },
      {
        name: 'mathInlineEnv',
        level: 'inline',
        tokenizer(src: string) {
          const m = /^(\\begin\{([a-zA-Z*]+)\}[\s\S]{0,4000}?\\end\{\2\})/.exec(src)
          if (!m) return undefined
          return { type: 'mathInlineEnv', raw: m[0], text: m[1], display: true }
        },
        renderer(token: Tokens.Generic) {
          return renderMath(String(token.text ?? ''), true)
        },
      },
    ],
  }
}

let globalMathInstalled = false

/**
 * 给**全局** marked 实例装一次公式扩展。
 *
 * 为什么需要这个包装：SFC 的 `<script setup>` 每创建一次组件就整体重跑一遍，
 * 直接在里面 `marked.use(...)` 会在每次进入笔记页时往全局 extensions 里再堆一份
 * tokenizer（越用越慢，且只有重启才清）。这里用模块级标志兜住。
 */
export function installMathOnce(md: { use: (ext: MarkedExtension) => unknown }): void {
  if (globalMathInstalled) return
  globalMathInstalled = true
  md.use(mathExtension())
}

