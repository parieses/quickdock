/**
 * 笔记预览里的代码块：右上角「复制」按钮。
 *
 * 为什么用 renderer 覆盖而不是渲染后扫 DOM 插按钮：预览是 `v-html` 整体重设的，
 * 每次编辑都会重建全部节点，事后插入的按钮会随之消失；渲染期直接吐出按钮和容器，
 * 重渲多少次都还在。
 *
 * 为什么按钮是 `<span>` 而不是 `<button>`：笔记预览走 DOMPurify 默认白名单，
 * 表单元素（button/input）会被剥掉，span + data-* 才能活过 sanitize。
 * 点击事件由预览容器做事件委托（见 NoteManagerPage 的 onPreviewClick）。
 */
import type { MarkedExtension, Tokens } from 'marked'

export interface CodeCopyLabels {
  /** 按钮 title / aria-label 文案（跟随当前语言） */
  copy: string
}

function escapeHtml(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&' + 'amp;')
    .replace(/</g, '&' + 'lt;')
    .replace(/>/g, '&' + 'gt;')
    .replace(/"/g, '&' + 'quot;')
    .replace(/'/g, '&' + '#39;')
}

let installed = false

/**
 * 给全局 marked 实例装一次代码块渲染器。
 *
 * 文案用 getter 传进来而不是安装时求值：语言可在应用内切换，
 * 渲染器每次输出都取一次当前文案，切语言后已打开的笔记也跟着变。
 */
export function installCodeCopyOnce(
  md: { use: (ext: MarkedExtension) => unknown },
  labels: () => CodeCopyLabels,
): void {
  if (installed) return
  installed = true
  md.use({
    renderer: {
      code(token: Tokens.Code) {
        const code = String(token.text ?? '').replace(/\n$/, '')
        const lang = String(token.lang ?? '').trim().split(/\s+/)[0] || ''
        const label = escapeHtml(labels().copy)
        return (
          '<div class="md-code" data-md-code>' +
            `<pre><code${lang ? ` class="language-${escapeHtml(lang)}"` : ''}>${escapeHtml(code)}</code></pre>` +
            `<span class="md-code-copy" data-code-copy="1" title="${label}" aria-label="${label}" role="button">` +
              '<svg viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
                '<rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>' +
                '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>' +
              '</svg>' +
            '</span>' +
          '</div>'
        )
      },
    },
  })
}
