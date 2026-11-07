/**
 * Markdown 里的 `[[wiki 链接]]` 渲染。
 *
 * 为什么必须做成 marked 的 inline 扩展并显式给 `start`：
 * marked 的 inline text 规则会一口气吃到下一个特殊字符（`/^[^\n]+/` 系列），
 * 正文里只要 `[[` 前面有普通文字，text tokenizer 就会把整段吞掉，wiki
 * tokenizer 永远拿不到以 `[[` 开头的切片。`start` 返回的切入位置会被 marked
 * 用来把 text 截断，从而把 `[[` 留到下一轮循环。mathMarkdown.ts 的
 * `inlineMathStart` 是同一套做法，这里照搬。
 *
 * 为什么输出「无 href 的 <a>」：DOMPurify 会过滤 `javascript:` 之类的伪协议，
 * 而无 href 的 <a> + class + data-* 属于默认白名单内的安全属性组合，
 * 既能被 sanitize 保留，又能承载点击事件（预览区用事件委托接）。
 */
import type { MarkedExtension, Tokens } from 'marked'

function escapeHtml(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&' + 'amp;')
    .replace(/</g, '&' + 'lt;')
    .replace(/>/g, '&' + 'gt;')
    .replace(/"/g, '&' + 'quot;')
    .replace(/'/g, '&' + '#39;')
}

/** 每次调用返回新对象，避免多个 marked 实例共享内部状态（与 mathExtension 一致）。 */
export function wikiExtension(): MarkedExtension {
  return {
    extensions: [
      {
        name: 'qdWikiLink',
        level: 'inline',
        // marked 传入的是 e.slice(1)（切入位置只找第 1 个字符之后），
        // 返回 -1 时 marked 只认 >=0 的数、直接忽略，不会截断文本。
        start(src: string) {
          return src.indexOf('[[')
        },
        tokenizer(src: string) {
          // 未闭合 / 内部还有方括号时返回 undefined，文本交回 text 规则，绝不吞字。
          const m = /^\[\[([^\[\]\n]+)\]\]/.exec(src)
          if (!m) return undefined
          return { type: 'qdWikiLink', raw: m[0], text: m[1] }
        },
        renderer(token: Tokens.Generic) {
          const raw = String(token.text ?? '')
          // 显示文本去掉 note:/todo:/item: 前缀；data-wiki 一定存完整原文
          // （含前缀），与 ResolveWikiTarget 的入参保持同一口径。
          const shown = raw.replace(/^(?:note|todo|item):/, '')
          return `<a class="qd-wiki" data-wiki="${escapeHtml(raw)}">${escapeHtml(shown)}</a>`
        },
      },
    ],
  }
}

let globalWikiInstalled = false

/**
 * 给全局 marked 实例装一次 wiki 扩展。
 *
 * SFC 的 `<script setup>` 每次创建组件都会整体重跑，直接 `marked.use(...)` 会
 * 每进一次笔记页就往全局 extensions 里再堆一份 tokenizer。用模块级标志兜住，
 * 与 installMathOnce 各自独立计数，互不干扰。
 */
export function installWikiOnce(md: { use: (ext: MarkedExtension) => unknown }): void {
  if (globalWikiInstalled) return
  globalWikiInstalled = true
  md.use(wikiExtension())
}
