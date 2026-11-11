/**
 * 邮件 HTML 的隔离渲染。
 *
 * 邮件正文是不可信输入：远程图片是发件人追踪器，脚本 / 事件属性 / 外链在
 * WebView2 里就是注入面。所以「直接渲染」不等于 v-html，而是三层收口：
 *  1. DOMParser 解析，删掉危险节点，清洗 on* / href / src / CSS 里的 url()；
 *  2. 组装独立 srcdoc，带 CSP + iframe sandbox=""（无脚本、无同源、无导航）；
 *  3. 默认剥掉所有远程 src；用户主动勾选「加载远程图片」才放行 http(s)/data:image。
 * 邮件排版高度依赖 <style> 和 inline style，这两类只清 url()/@import，不整段删，
 * 否则表格版式会全塌，看起来跟「纯文本」没区别。
 */

/** 整棵删除：脚本、外部资源、表单、媒体容器、样式节点（内容另行收集）。 */
const DROP_TAGS = new Set([
  'script', 'noscript', 'iframe', 'frame', 'frameset', 'object', 'embed', 'applet',
  'link', 'meta', 'base', 'style', 'form', 'input', 'button', 'textarea', 'select',
  'option', 'svg', 'math', 'template', 'video', 'audio', 'source', 'track', 'canvas',
  'portal', 'marquee',
])

/** 事件属性之外的危险属性：能触发资源加载或二次导航。 */
const DROP_ATTRS = new Set([
  'srcdoc', 'formaction', 'ping', 'http-equiv', 'dynsrc', 'lowsrc', 'is',
  'xlink:href', 'target', 'rel',
])

/** 会发起子资源请求的属性。 */
const URL_ATTRS = new Set(['src', 'srcset', 'background', 'poster', 'data', 'href'])

/** 只放行普通位图内嵌图；SVG 可能自带脚本，cid: 在本项目里没有附件上下文。 */
function safeImageURL(value: string): boolean {
  const v = value.trim().replace(/[\u0000-\u001f]/g, '')
  return /^(?:https?:\/\/|data:image\/(?:png|jpe?g|gif|webp|bmp);base64,)/i.test(v)
}

/** 去掉 CSS 里的外链与旧 IE 表达式，保留其余声明以维持邮件版式。 */
function cleanCss(css: string): string {
  return css
    .replace(/@import[^;]*;?/gi, '')
    .replace(/expression\s*\([^)]*\)/gi, '')
    .replace(/-moz-binding\s*:[^;]*;?/gi, '')
    .replace(/url\s*\([^)]*\)/gi, 'none')
}

const BASE_CSS = `
html, body { margin: 0; padding: 0; }
body {
  padding: 16px 18px; background: #fff; color: #1f2328;
  font: 14px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", Arial, "Microsoft YaHei", sans-serif;
  word-break: break-word;
}
a { color: #0969da; text-decoration: underline; }
img { max-width: 100%; height: auto; }
table { max-width: 100%; }
pre { white-space: pre-wrap; word-break: break-word; }
`

/**
 * 生成塞进 iframe srcdoc 的完整文档。返回字符串不含任何可执行内容：
 * 即使清洗漏了什么，sandbox="" 禁脚本、CSP 禁除图片外的子资源，也能兜住。
 */
export function buildMailDocument(rawHtml: string, allowRemote: boolean): string {
  const doc = new DOMParser().parseFromString(rawHtml, 'text/html')

  // 邮件的 <style> 常见于 head，也可能在 body；先收内容再删节点，避免重复注入。
  // CSS 里若夹了字面量 </style>，直接内联会提前闭合标签；脚本虽被 sandbox
  // 与 CSP 双重挡住，也没必要留这个注入口。
  const mailCss = Array.from(doc.querySelectorAll('style'))
    .map(s => cleanCss(s.textContent || ''))
    .join('\n')
    .replace(/<\/style/gi, '<\\/style')

  for (const el of Array.from(doc.querySelectorAll('*'))) {
    if (DROP_TAGS.has(el.tagName.toLowerCase())) {
      el.remove()
      continue
    }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase()
      if (name.startsWith('on') || DROP_ATTRS.has(name)) {
        el.removeAttribute(attr.name)
        continue
      }
      if (name === 'style') {
        const css = cleanCss(attr.value)
        if (css.trim()) el.setAttribute('style', css)
        else el.removeAttribute('style')
        continue
      }
      if (name === 'href') {
        // 链接不可点，但把地址留在 title 里，鼠标悬停还能看清发件人想让你去哪。
        if (el.tagName === 'A' && /^https?:\/\//i.test(attr.value.trim())) {
          el.setAttribute('title', attr.value.trim())
        }
        el.removeAttribute(attr.name)
        continue
      }
      if (URL_ATTRS.has(name)) {
        if (name === 'src' && allowRemote && safeImageURL(attr.value)) {
          el.setAttribute('src', attr.value.trim())
        } else {
          el.removeAttribute(attr.name)
        }
      }
    }
    // 被剥掉 src 的图片保留宽高占位，别让表格布局塌掉；visibility 不显示破图图标。
    if (el.tagName === 'IMG' && !el.getAttribute('src')) {
      el.setAttribute('style', `${el.getAttribute('style') || ''};visibility:hidden`)
    }
  }

  // 邮件常把 bgcolor / font / class 写在 <body> 上；属性已在上面清洗过，
  // 只透传清洗后的值，连同已收集的 <style> 才能还原原版式。
  const bodyAttrs = doc.body
    ? Array.from(doc.body.attributes)
        .filter(a => !/^xmlns/i.test(a.name))
        .map(a => ` ${a.name}="${a.value.replace(/&/g, '&amp;').replace(/"/g, '&quot;')}"`)
        .join('')
    : ''
  const body = doc.body ? doc.body.innerHTML : ''
  const csp = [
    "default-src 'none'",
    allowRemote ? "img-src data: http: https:" : "img-src data:",
    "style-src 'unsafe-inline'",
    "font-src data:",
    "base-uri 'none'",
    "form-action 'none'",
  ].join('; ')

  return `<!doctype html><html><head><meta charset="utf-8">` +
    `<meta name="referrer" content="no-referrer">` +
    `<meta http-equiv="Content-Security-Policy" content="${csp}">` +
    `<style>${BASE_CSS}</style><style>${mailCss}</style>` +
    `</head><body${bodyAttrs}>${body}</body></html>`
}

/** 渲染视图下「复制全文」用纯文本，别把标签也复制走。 */
export function mailHtmlToText(rawHtml: string): string {
  const doc = new DOMParser().parseFromString(rawHtml, 'text/html')
  doc.querySelectorAll('script, style, noscript').forEach(n => n.remove())
  return (doc.body?.textContent || '').replace(/[ \t]+/g, ' ').trim()
}
