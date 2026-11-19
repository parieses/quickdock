/**
 * 打印通道：把「外部 HTML（通常是插件生成的、自带完整 CSS 的打印文档）」
 * 在宿主顶层文档里渲染一次，然后调起系统打印。
 *
 * 为什么必须由宿主来做：
 *   WebView2 / Chromium 的 window.print() 只作用于顶层文档。插件跑在
 *   sandbox iframe 里，它自己调 window.print() 打印的是整个 QuickDock 应用
 *   —— 暗色外壳、并且纸面内容根本不在打印上下文里，结果就是「打印出来一张白纸」。
 *   约定：插件把打印用 HTML 交给宿主（qdPrint），宿主在顶层文档渲染后打印。
 *   与插件「导出 HTML」共用同一份 HTML 字符串，打印件与导出件永远一致。
 *
 * 隔离策略（刻意不做 CSS 作用域重写 —— 选择器 / @page / @media 的重写太容易踩坑）：
 *   1. 插件 CSS 整段包一层 @media print 再注入。它只在打印上下文生效，
 *      宿主界面在屏幕上一动不动，也就不用抢在打印前精确清理；
 *   2. 内容装进 #qd-print-host：屏幕态 display:none，打印态 display:block；
 *   3. 打印期间给 body 加 qd-printing 类，把顶层其它节点全部隐藏，只留内容容器；
 *   4. 打印结束后移除容器 / 样式 / 类，宿主界面完好如初。
 *
 * ⚠️ 安全：插件 iframe 是无同源沙箱（sandbox 不含 allow-same-origin）。
 *    把插件的 <script> 搬进宿主顶层文档执行 = 沙箱逃逸，脚本可直接拿到宿主
 *    全部能力（含 Wails 绑定）。这里一律丢弃 <script>，打印也根本不需要脚本。
 */

export interface PrintHtmlOptions {
  /** @page 尺寸，例如 'A4'；省略则不覆盖宿主页尺寸。 */
  page?: string
}

const HOST_ID = 'qd-print-host'
const STYLE_ATTR = 'data-qd-print-style'
const BODY_CLASS = 'qd-printing'
/** afterprint 不派发时（静默打印 / 个别驱动）的兜底清理延时 */
const CLEANUP_FALLBACK_MS = 30000
const PAINT_TIMEOUT_MS = 120

/**
 * 允许进入宿主文档的标签（白名单）。
 *
 * 只收「排版需要」的语义标签：任何可执行/可嵌入内容（script/iframe/object/embed/
 * svg/meta/base/link/style）都在 DROP_TAGS 里直接丢弃；白名单外的普通标签
 * （section/a/font…）做「拆壳保留内容」处理——保留文字，丢掉标签本身。
 */
const ALLOWED_TAGS = new Set([
  'P', 'DIV', 'SPAN', 'BR',
  'TABLE', 'TBODY', 'TR', 'TD', 'TH',
  'UL', 'OL', 'LI',
  'H1', 'H2', 'H3', 'H4', 'H5', 'H6',
  'IMG', 'PRE', 'CODE', 'BLOCKQUOTE', 'HR'
])

/** 连同子树一起丢弃的标签：可执行、可嵌入、可加载外部资源、可覆盖宿主样式。 */
const DROP_TAGS = new Set([
  'SCRIPT', 'STYLE', 'LINK', 'META', 'BASE', 'TITLE',
  'IFRAME', 'FRAME', 'FRAMESET', 'OBJECT', 'EMBED', 'APPLET',
  'SVG', 'MATH', 'FORM', 'INPUT', 'BUTTON', 'SELECT', 'TEXTAREA', 'OPTION',
  'VIDEO', 'AUDIO', 'SOURCE', 'TRACK', 'CANVAS', 'TEMPLATE', 'NOSCRIPT'
])

/** 允许保留的内联样式属性（打印排版够用，且都不能外联加载资源）。 */
const STYLE_PROPS = new Set([
  'color', 'background', 'background-color',
  'font', 'font-family', 'font-size', 'font-style', 'font-weight',
  'text-align', 'text-decoration', 'text-indent', 'text-transform',
  'line-height', 'letter-spacing', 'word-spacing', 'white-space',
  'margin', 'margin-top', 'margin-right', 'margin-bottom', 'margin-left',
  'padding', 'padding-top', 'padding-right', 'padding-bottom', 'padding-left',
  'border', 'border-top', 'border-right', 'border-bottom', 'border-left',
  'border-color', 'border-style', 'border-width', 'border-collapse', 'border-spacing',
  'width', 'height', 'max-width', 'min-width', 'vertical-align',
  'list-style', 'list-style-type', 'list-style-position',
  'page-break-before', 'page-break-after', 'page-break-inside',
  'break-before', 'break-after', 'break-inside'
])

function buildGuardStyle(page?: string): string {
  const size = page && /^[A-Za-z0-9 .-]{1,24}$/.test(page) ? page : ''
  return [
    // 屏幕态：容器不可见（样式/容器清理前的窗口期内，宿主界面不受任何影响）
    `#${HOST_ID}{display:none;}`,
    size ? `@page{size:${size};margin:0;}` : '',
    '@media print{',
    // 暗色主题下 body 的文字是近白色，打印丢背景后等于白纸上看不见字
    'html,body{background:#fff !important;color:#000 !important;}',
    `body.${BODY_CLASS} > *:not(#${HOST_ID}):not(style):not(script):not(link){display:none !important;}`,
    `#${HOST_ID}{display:block !important;}`,
    '}'
  ]
    .filter(Boolean)
    .join('\n')
}

/** 图片地址白名单：只允许 http(s) 与位图 data URL，杜绝 javascript:/file:/blob:。 */
function isSafeImgSrc(raw: string): boolean {
  const s = raw.trim()
  if (/^https?:\/\/[^\s]+$/i.test(s)) return true
  return /^data:image\/(png|jpe?g|gif|webp|bmp|x-icon|vnd\.microsoft\.icon|svg\+xml);base64,[a-z0-9+/=\s]+$/i.test(s)
}

/** 内联样式白名单：逐条声明过滤，去掉 url()/expression()/@import/反斜杠转义。 */
function sanitizeStyle(raw: string): string {
  const out: string[] = []
  for (const decl of raw.split(';')) {
    const i = decl.indexOf(':')
    if (i < 0) continue
    const prop = decl.slice(0, i).trim().toLowerCase()
    const val = decl.slice(i + 1).trim()
    if (!val || !STYLE_PROPS.has(prop)) continue
    const low = val.toLowerCase()
    if (low.includes('url(') || low.includes('expression') || low.includes('javascript:') ||
      low.includes('@import') || low.includes('\\')) continue
    out.push(`${prop}:${val}`)
  }
  return out.join(';')
}

/** 按白名单把 src 的属性抄到干净节点上。除 src/colspan/rowspan/style 外一律丢弃。 */
function copyAllowedAttrs(src: Element, dst: HTMLElement): void {
  const tag = src.tagName
  for (const a of Array.from(src.attributes)) {
    const name = a.name.toLowerCase()
    if (name.startsWith('on')) continue // 内联事件处理器：一个都不留
    if (name === 'src' && tag === 'IMG') {
      if (isSafeImgSrc(a.value)) dst.setAttribute('src', a.value.trim())
      continue
    }
    if ((name === 'colspan' || name === 'rowspan') && (tag === 'TD' || tag === 'TH')) {
      if (/^\d{1,3}$/.test(a.value.trim())) dst.setAttribute(name, a.value.trim())
      continue
    }
    if (name === 'style') {
      const css = sanitizeStyle(a.value)
      if (css) dst.setAttribute('style', css)
      continue
    }
  }
}

/**
 * 递归重建 DOM（而不是直接 importNode）：
 * 白名单外的标签拆壳保留文字，黑名单标签连子树一起丢，属性逐个过白名单。
 * importNode 只过滤「顶层标签名」，`<div><svg onload=...>` 这类嵌套写法照样进宿主。
 */
function sanitizeInto(src: Node, dst: Node): void {
  for (const n of Array.from(src.childNodes)) {
    if (n.nodeType === Node.TEXT_NODE) {
      dst.appendChild(document.createTextNode(n.nodeValue || ''))
      continue
    }
    if (n.nodeType !== Node.ELEMENT_NODE) continue
    const el = n as Element
    const tag = el.tagName.toUpperCase()
    if (DROP_TAGS.has(tag)) continue
    if (!ALLOWED_TAGS.has(tag)) {
      sanitizeInto(el, dst) // 拆壳：保留内容，丢掉标签
      continue
    }
    const clean = document.createElement(tag.toLowerCase())
    copyAllowedAttrs(el, clean)
    sanitizeInto(el, clean)
    dst.appendChild(clean)
  }
}

/** 等两帧（或超时兜底），确保注入的样式与内容已经完成布局 */
function nextPaint(): Promise<void> {
  return new Promise((resolve) => {
    let settled = false
    const done = () => {
      if (settled) return
      settled = true
      resolve()
    }
    if (typeof requestAnimationFrame === 'function') {
      requestAnimationFrame(() => requestAnimationFrame(done))
    }
    setTimeout(done, PAINT_TIMEOUT_MS)
  })
}

/**
 * 在宿主顶层文档渲染 `html` 并调起系统打印。
 * resolve 于打印对话框关闭（或兜底清理）之后。
 */
export async function printHtmlDocument(html: string, opts: PrintHtmlOptions = {}): Promise<void> {
  if (typeof html !== 'string' || !html.trim()) throw new Error('打印内容为空')
  if (typeof window.print !== 'function') throw new Error('当前环境不支持系统打印')

  // 解析成一份惰性文档：DOMParser 产物不会执行脚本，也不会触发资源加载
  const doc = new DOMParser().parseFromString(html, 'text/html')

  const injected: HTMLElement[] = []
  const guard = document.createElement('style')
  guard.setAttribute(STYLE_ATTR, '')
  guard.textContent = buildGuardStyle(opts.page)
  injected.push(guard)

  // 插件 CSS 整段并入 @media print：屏幕上对宿主零影响
  doc.head?.querySelectorAll('style').forEach((src) => {
    const el = document.createElement('style')
    el.setAttribute(STYLE_ATTR, '')
    el.textContent = '@media print{\n' + (src.textContent || '') + '\n}'
    injected.push(el)
  })

  const host = document.createElement('div')
  host.id = HOST_ID
  // 逐节点白名单重建：插件 HTML 是不可信输入，只让它带来「排版」，不带来任何行为。
  sanitizeInto(doc.body ?? doc.documentElement, host)

  document.head.append(...injected)
  document.body.appendChild(host)
  document.body.classList.add(BODY_CLASS)

  try {
    await nextPaint()
  } catch {
    /* 忽略：继续打印 */
  }

  await new Promise<void>((resolve, reject) => {
    let done = false
    let timer = 0
    const finish = () => {
      if (done) return
      done = true
      window.removeEventListener('afterprint', finish)
      if (timer) clearTimeout(timer)
      injected.forEach((el) => el.remove())
      host.remove()
      document.body.classList.remove(BODY_CLASS)
      resolve()
    }
    window.addEventListener('afterprint', finish)
    try {
      window.print()
    } catch (e) {
      finish()
      reject(e)
      return
    }
    // 部分环境（WebView2 静默打印 / 用户取消）不派发 afterprint，
    // 给一个兜底延时，避免容器与样式永久留在宿主文档里。
    timer = window.setTimeout(finish, CLEANUP_FALLBACK_MS)
  })
}
