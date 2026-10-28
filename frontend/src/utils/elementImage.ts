/**
 * 把页面里已经渲染好的东西（Mermaid 图 / KaTeX 公式）复制成 PNG 图片。
 *
 * 为什么不用截图：截整个窗口会把侧栏、气泡、光标一起带上；用户要的是「这一段图」
 * 粘进文档或聊天框，不是一张界面截图。
 *
 * 两条转换路线，按元素类型分开：
 *
 * 1. **SVG（Mermaid）走矢量序列化**：克隆 `<svg>` → 序列化 → 以 data URI 塞进 `<img>`
 *    → 画到 canvas。清晰、不受字体影响、60ms 级别。注意 Mermaid 输出带
 *    `style="max-width:…px"`，序列化进图片后没有父容器可参照，必须换掉。
 *
 * 2. **HTML（KaTeX）走 html-to-image**：公式是带 `katex` 类的一堆 span，字形全靠
 *    KaTeX 的 woff2 字体。html-to-image 会把整棵树的**计算样式**内联、再把
 *    @font-face 的字体抓成 base64 塞进 foreignObject，这是纯 DOM 转图唯一能保住
 *    字形的方式（自己写一份等于重写它的样式内联逻辑）。
 *
 * 写剪贴板优先用浏览器 API，失败退回后端（`CopyPNGToClipboard`）：
 * WebView2 里 `navigator.clipboard.write` 与 writeText 是同一个坑，会静默拒绝。
 */
import { CopyPNGToClipboard } from '../../bindings/quickdock/services/appservice'

/** 2 倍图：粘到文档里放大看仍然清楚，体积也可控 */
const PIXEL_RATIO = 2

/**
 * 转义要拼进 XML 属性值的内容。
 *
 * 这个函数存在的唯一原因是一个真实故障：`getComputedStyle(el).fontFamily` 的值是
 * `system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", …` —— 里面的双引号
 * 一旦原样拼进 `style="…"`，属性在 `"Segoe UI"` 处就被截断，整个 SVG 变成非法
 * XML，`new Image()` 报 `decode failed`（不告诉你哪里错）。而单测只拼数字/颜色时
 * 一切都是好的，所以这个坑必须有一个能被断言的地方守着。
 */
export function xmlAttr(v: string): string {
  return String(v ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/**
 * 解析一个「不透明的底色」。
 *
 * 为什么要逐级向上找：`.md-math` 本身没有背景（公式就浮在气泡上），
 * 直接给 canvas 填主题底色会得到一块与周围不一致的色块；而 KaTeX/图
 * 的实际底色来自最近的祖先。找不到（全是 transparent）才落到主题底。
 */
export function resolveBackground(el: Element | null): string {
  let cur: Element | null = el
  while (cur) {
    const bg = getComputedStyle(cur).backgroundColor
    // rgba(0, 0, 0, 0) 是 Chromium 对 transparent 的计算结果
    if (bg && bg !== 'transparent' && !/rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*0\s*\)/.test(bg)) return bg
    cur = cur.parentElement
  }
  const v = getComputedStyle(document.documentElement).getPropertyValue('--color-bg-primary').trim()
  return v || '#ffffff'
}

function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error('image decode failed'))
    img.src = src
  })
}

function canvasToPng(canvas: HTMLCanvasElement): Promise<Blob | null> {
  return new Promise((resolve) => canvas.toBlob(resolve, 'image/png'))
}

/** SVG → PNG。宽高按元素当前显示尺寸（像素）取，保证与屏幕上看到的一致 */
export async function svgToPngBlob(svg: SVGSVGElement, bg: string): Promise<Blob | null> {
  const rect = svg.getBoundingClientRect()
  const w = Math.max(1, Math.round(rect.width))
  const h = Math.max(1, Math.round(rect.height))
  if (w <= 1 || h <= 1) return null

  const clone = svg.cloneNode(true) as SVGSVGElement
  clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg')
  clone.setAttribute('xmlns:xlink', 'http://www.w3.org/1999/xlink')
  // Mermaid 给的是 width="100%" + 内联 max-width。图片里没有父容器，
  // 100% 会退化成默认 300×150，所以宽高必须写成像素，并去掉那个 max-width。
  clone.setAttribute('width', String(w))
  clone.setAttribute('height', String(h))
  clone.removeAttribute('style')

  const xml = new XMLSerializer().serializeToString(clone)
  const img = await loadImage(`data:image/svg+xml;charset=utf-8,${encodeURIComponent(xml)}`)

  const canvas = document.createElement('canvas')
  canvas.width = w * PIXEL_RATIO
  canvas.height = h * PIXEL_RATIO
  const ctx = canvas.getContext('2d')
  if (!ctx) return null
  ctx.scale(PIXEL_RATIO, PIXEL_RATIO)
  ctx.fillStyle = bg
  ctx.fillRect(0, 0, w, h)
  ctx.drawImage(img, 0, 0, w, h)
  return canvasToPng(canvas)
}

/**
 * HTML（公式等）→ PNG。
 *
 * 为什么要自己搭 foreignObject，而不是用 html-to-image 这类库：
 *
 * - 那类库的做法是「克隆整棵树 + 逐节点内联计算样式 + 抓取所有 @font-face」。
 *   实测在 KaTeX 上每次都重新抓 60 个字体文件（woff2/woff/ttf 三套 × 20 个字形族），
 *   第一次 20ms、之后每次 26s —— 慢在网络往返而不是光栅化，用户点一次右键要等半分钟。
 * - 而我们这里的样式来源其实极窄：公式的每个字形都由 `katex.min.css` 的 class 规则
 *   决定，不需要逐节点内联。把这份 CSS 原样带进 foreignObject，class 就能命中；
 *   唯一需要改写的只有 @font-face 的字体 URL（data URI 里没有 base 可解析相对路径）。
 *
 * 于是流程是：取 katex 相关规则 → 字体只取 woff2 转 base64（模块级缓存，只抓一次）
 * → 拼 `<svg><foreignObject>` → 加载成 Image → 画到 canvas。
 *
 * 布局上的坑：行内公式是 `<span>`，它的尺寸来自排版结果，直接塞进 foreignObject
 * 会塌成 0 宽。所以外面套一个**确定像素尺寸**的 flex 盒子，并显式带上
 * 字号/字色/字体 —— 公式的间距全按 em 算，字号一变整张图就不是屏幕上那张了。
 */
export async function htmlToPngBlob(el: HTMLElement, bg: string): Promise<Blob | null> {
  const rect = el.getBoundingClientRect()
  // 尺寸取「内容的实际外接矩形」而不是元素自身：块级公式是 display:block，
  // 自身宽度等于整行（KaTeX 的 .katex-display>.katex 也是 block），按它裁会得到
  // 一张「一大片空白 + 右边一个公式」的长条。Range 量的是内容本身。
  let w = Math.ceil(rect.width)
  let h = Math.ceil(rect.height)
  if (el.classList.contains('md-math-block')) {
    // 块级公式改成 inline-block 量一次「收缩到内容」的宽度再改回来：
    // KaTeX 的 .katex-display > .katex 都是 display:block，Range / 子元素 rect
    // 拿到的仍然是整行宽。shrink-to-fit 是唯一能直接问出内容宽度的办法。
    const prev = el.style.display
    el.style.display = 'inline-block'
    const r1 = el.getBoundingClientRect()
    el.style.display = prev
    if (r1.width > 0) {
      w = Math.ceil(r1.width)
      h = Math.ceil(r1.height)
    }
  }
  if (w <= 0 || h <= 0) return null

  const cs = getComputedStyle(el)
  const pad = 8
  const boxW = w + pad * 2
  const boxH = h + pad * 2
  const { css, fontCss } = await collectPrintableCss()

  // 克隆里可能有 id，重复渲染时会和页面上的元素撞 id；去掉即可（样式选择器用的是 class）
  const clone = el.cloneNode(true) as HTMLElement
  clone.removeAttribute('id')
  // 必须用 XMLSerializer 而不是 outerHTML：SVG 是 XML，HTML 序列化会产出
  // `<br>`、`&nbsp;` 这类 XML 不认识的东西，`new Image()` 直接 decode failed。
  const body = new XMLSerializer().serializeToString(clone)


  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="${boxW}" height="${boxH}" viewBox="0 0 ${boxW} ${boxH}">` +
    `<foreignObject x="0" y="0" width="${boxW}" height="${boxH}">` +
    `<div xmlns="http://www.w3.org/1999/xhtml" style="width:${boxW}px;height:${boxH}px;display:flex;` +
    `align-items:center;justify-content:center;background:${xmlAttr(bg)};color:${xmlAttr(cs.color)};` +
    `font-size:${xmlAttr(cs.fontSize)};font-family:${xmlAttr(cs.fontFamily)};` +
    `font-weight:${xmlAttr(cs.fontWeight)};line-height:${xmlAttr(cs.lineHeight)};">` +
    `<style>${fontCss}${css}</style>` +
    body +
    `</div></foreignObject></svg>`

  // 用 Blob URL 而不是 data URI：字体 base64 之后整串能有几百 KB，
  // data URI 太长时 Image 解码会失败且不给原因。
  // 用 data URI 而不是 Blob URL：这里的内容对 canvas 来说是「同源且自包含」的，
  // 而 blob: 形式的 SVG 图片在 Chromium 里会被判成跨源，画进 canvas 后
  // toBlob 直接报 "Tainted canvases may not be exported"（图都画出来了，导不出来）。
  const url = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
  const img = await loadImage(url).catch(() => {
    throw new Error(`svg decode failed (svg=${svg.length}B, font=${fontCss.length}B, css=${css.length}B)`)
  })

  const canvas = document.createElement('canvas')
  canvas.width = boxW * PIXEL_RATIO
  canvas.height = boxH * PIXEL_RATIO
  const ctx = canvas.getContext('2d')
  if (!ctx) return null
  ctx.scale(PIXEL_RATIO, PIXEL_RATIO)
  ctx.fillStyle = bg
  ctx.fillRect(0, 0, boxW, boxH)
  ctx.drawImage(img, 0, 0, boxW, boxH)
  return canvasToPng(canvas)
}

/** 打印用 CSS：katex 规则（去掉 @font-face）+ 只含 woff2 的字体面（base64） */
let printableCssCache: { css: string; fontCss: string } | null = null

function arrayBufferToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let bin = ''
  const CHUNK = 0x8000
  for (let i = 0; i < bytes.length; i += CHUNK) {
    bin += String.fromCharCode.apply(null, Array.from(bytes.subarray(i, i + CHUNK)))
  }
  return btoa(bin)
}

async function collectPrintableCss(): Promise<{ css: string; fontCss: string }> {
  if (printableCssCache) return printableCssCache

  let css = ''
  const faces: CSSFontFaceRule[] = []
  // 只收「和公式有关」的规则：全量收会把整份应用样式（几十 KB）都塞进 data URI，
  // 而且其中大量选择器在 foreignObject 里反而会干扰排版。
  const relevant = (text: string) => /katex|md-math/.test(text)

  for (const sheet of Array.from(document.styleSheets)) {
    let rules: CSSRuleList | null = null
    try {
      rules = (sheet as CSSStyleSheet).cssRules
    } catch {
      continue // 跨域样式表读不到规则，跳过
    }
    if (!rules) continue
    for (const rule of Array.from(rules)) {
      if (rule instanceof CSSFontFaceRule) {
        if (/katex/i.test(String(rule.style.fontFamily || ''))) faces.push(rule)
        continue
      }
      const text = rule.cssText
      if (relevant(text)) css += text + '\n'
    }
  }

  // 每个字形族只保留 woff2：katex.min.css 给每族准备了三份格式，
  // 全带上会让 data URI 涨到 1.6MB（Image 解码明显变慢）。
  const parts: string[] = []
  await Promise.all(faces.map(async (f) => {
    const src = String(f.style.getPropertyValue('src') || '')
    const m = /url\(["']?([^"')]+\.woff2)["']?\)/.exec(src)
    if (!m) return
    let url = m[1]
    try {
      url = new URL(url, location.href).href
    } catch {
      return
    }
    try {
      const buf = await (await fetch(url)).arrayBuffer()
      const b64 = arrayBufferToBase64(buf)
      const family = f.style.fontFamily
      const weight = f.style.getPropertyValue('font-weight') || 'normal'
      const style = f.style.getPropertyValue('font-style') || 'normal'
      parts.push(
        `@font-face{font-family:${family};font-style:${style};font-weight:${weight};` +
        `src:url(data:font/woff2;base64,${b64}) format("woff2");}`,
      )
    } catch {
      // 单个字体抓不到就跳过：少了某个字形族会退化成系统字体，但图还能出来
    }
  }))

  printableCssCache = { css, fontCss: parts.join('\n') }
  return printableCssCache
}

/** 元素 → PNG（自动按类型选路线） */
export async function elementToPngBlob(el: HTMLElement): Promise<Blob | null> {
  const bg = resolveBackground(el)
  const svg = el.querySelector('svg')
  // 只有「元素本身就是一张图」时才走矢量路线；公式里不会含 svg。
  if (svg instanceof SVGSVGElement && el.classList.contains('md-mermaid')) {
    return svgToPngBlob(svg, bg)
  }
  return htmlToPngBlob(el, bg)
}

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader()
    fr.onload = () => {
      const s = String(fr.result || '')
      const i = s.indexOf(',')
      resolve(i >= 0 ? s.slice(i + 1) : s)
    }
    fr.onerror = () => reject(new Error('read blob failed'))
    fr.readAsDataURL(blob)
  })
}

/** 把 PNG 写进系统剪贴板。返回是否成功（失败不抛错，调用方负责提示） */
export async function copyPngBlob(blob: Blob): Promise<boolean> {
  // 路线一：浏览器剪贴板（快，但 WebView2 里可能静默拒绝）
  try {
    if (typeof ClipboardItem !== 'undefined' && navigator.clipboard?.write) {
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })])
      return true
    }
  } catch {
    // 落到后端
  }
  // 路线二：后端写 CF_DIB（WebView2 下可靠）
  try {
    const b64 = await blobToBase64(blob)
    const res = await CopyPNGToClipboard(b64)
    return res ? res.code === 0 : false
  } catch {
    return false
  }
}

/** 复制一个元素为图片（组合上面三步） */
export async function copyElementAsImage(el: HTMLElement): Promise<boolean> {
  const blob = await elementToPngBlob(el)
  if (!blob) return false
  return copyPngBlob(blob)
}

/** 触发浏览器下载一个 Blob（图/文件落盘用）。临时 a[download] 点击后清理。 */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** Mermaid 图导出：取已渲染的 svg 元素序列化。缺 xmlns 时补上，保证下载的 .svg 能独立打开。 */
export function serializeMermaidSvg(el: HTMLElement): string | null {
  const svg = el.querySelector('.md-mermaid-svg svg') as SVGSVGElement | null
  if (!svg) return null
  const xml = new XMLSerializer().serializeToString(svg)
  if (!/xmlns="http:\/\/www\.w3\.org\/2000\/svg"/.test(xml)) {
    return xml.replace(/^<svg/, '<svg xmlns="http://www.w3.org/2000/svg"')
  }
  return xml
}
