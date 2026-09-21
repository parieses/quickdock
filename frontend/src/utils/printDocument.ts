/**
 * 打印通道：把「外部 HTML（通常是插件生成的、自带完整 CSS 的打印文档）」
 * 在宿主顶层文档里渲染一次，然后调起系统打印。
 *
 * 为什么必须由宿主来做：
 *   WebView2 / Chromium 的 window.print() 只作用于**顶层文档**。插件跑在
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
 * ⚠️ 安全：插件 iframe 是**无同源沙箱**（sandbox 不含 allow-same-origin）。
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

  // 解析成一份**惰性**文档：DOMParser 产物不会执行脚本，也不会触发资源加载
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
  doc.body?.childNodes.forEach((n) => {
    if (n.nodeType === Node.ELEMENT_NODE) {
      const tag = (n as Element).tagName
      // <script> 绝不能进顶层文档（沙箱逃逸）；link/style 由上面的流程单独处理
      if (tag === 'SCRIPT' || tag === 'LINK' || tag === 'STYLE') return
    }
    host.appendChild(document.importNode(n, true))
  })

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
