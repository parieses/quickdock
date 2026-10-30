/**
 * Markdown 里的 Mermaid 图渲染。
 *
 * 为什么不能用「同步 marked renderer 直接出 HTML」：Mermaid 只有异步 API
 * （`mermaid.render()` 返回 Promise），而整条渲染链路（marked → DOMPurify → v-html）
 * 是同步的。因此这里采用两段式：
 *
 *   1. 同步阶段（marked 的 code renderer）只吐一个占位块，里面是源码 `<pre>`；
 *   2. 渲染阶段（DOM 已经挂上之后）扫描占位块，异步渲染成 SVG 替换进去。
 *
 * 三条硬约束：
 *
 * 1. 流式期间每帧都会失败。回答逐字到达，`graph TD\nA[开` 这种半截定义必然解析失败。
 *    失败时保留源码（占位块本来就是源码），只在前面加一行提示，绝不抛错、绝不显示红框错误图
 *    （`suppressErrorRendering` 关掉 Mermaid 自己往 body 里塞错误图的默认行为）。
 *
 * 2. 同一容器不能重复渲染。流式期间会反复扫描；用 `data-mermaid-done` 标记已完成，
 *    用 `data-mermaid-failed`（源码哈希）记住「这段源码已经失败过」，避免每帧重试。
 *    源码变化时 v-html 会重建这些节点，标记自然消失 → 自动重试，无需额外失效逻辑。
 *
 * 3. 主题跟随应用。SVG 里的颜色是渲染时定死的，CSS 变量进不去。所以每次渲染前把
 *    当前主题的 CSS 变量值读出来喂给 Mermaid 的 `themeVariables`（base 主题），并在
 *    `data-theme` 变化时把已渲染的图按保存的源码重画一遍。
 *
 * 首次加载 Mermaid 走动态 import：它体积大（含多种图的解析器），只有回答里真的出现了
 * ```mermaid 才值得付这笔下载。vite.config.ts 的 optimizeDeps.include 必须列出 mermaid，
 * 否则运行时发现新依赖会触发二次预构建（本项目的已知坑，见该文件里的说明）。
 */

/** 围栏语言标注命中这些时按图渲染（`mmd` 是 Mermaid 圈的常用缩写） */
const MERMAID_FENCE_LANGS = new Set(['mermaid', 'mmd'])

/** 待渲染容器的选择器：没标 done 的都算没画出来 */
const PENDING_SELECTOR = '.md-mermaid:not([data-mermaid-done])'

export function isMermaidFenceLang(lang: string): boolean {
  return MERMAID_FENCE_LANGS.has(String(lang || '').trim().toLowerCase())
}

function escapeHtml(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

/** djb2。只需要「同源码 → 同值」，不需要抗碰撞 */
function hash(s: string): string {
  let h = 5381
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0
  return (h >>> 0).toString(36)
}

/** 源码块的统一形态：占位时是内容，绘图失败时是降级产物，平时藏在图形区后面 */
function sourceHtml(code: string): string {
  return `<pre class="md-mermaid-src"><code>${escapeHtml(code)}</code></pre>`
}

/**
 * 工具栏按钮的文案。
 *
 * utils 层拿不到 i18n 的 `t()`（这里是纯函数，不该依赖组件上下文），所以文案必须由调用方传。
 * `source` / `diagram` / `fold` / `expand` 描述的都是点下去会发生什么，与代码块的
 * 「复制代码」保持同一种语义 —— 按钮说动作，不说当前状态。
 */
export interface MermaidLabels {
  /** 渲染失败的提示 */
  error: string
  /** 当前在看图 → 按钮「查看源码」 */
  source: string
  /** 当前在看源码 → 按钮「查看图」 */
  diagram: string
  fold: string
  expand: string
}

/** 图标都放两个，靠容器属性切换显示（JS 只改属性，不改 DOM 结构） */
const ICONS = {
  code:
    `<svg class="md-mermaid-i-code" viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` +
    `<polyline points="16 18 22 12 16 6"></polyline><polyline points="8 6 2 12 8 18"></polyline></svg>`,
  eye:
    `<svg class="md-mermaid-i-eye" viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` +
    `<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7z"></path><circle cx="12" cy="12" r="3"></circle></svg>`,
  up:
    `<svg class="md-mermaid-i-up" viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` +
    `<polyline points="18 15 12 9 6 15"></polyline></svg>`,
  down:
    `<svg class="md-mermaid-i-down" viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` +
    `<polyline points="6 9 12 15 18 9"></polyline></svg>`,
}

/**
 * 生成占位块（工具栏 + 源码 + 空的图形区）。
 *
 * 为什么一开始就把工具栏和源码都写进 DOM：
 *
 * - 源码不能丢。图渲染成功后 DOM 里只剩 SVG，「看源码」「切换主题重画」都得再找回来。
 *   留着 `<pre>` 用 CSS 隐藏，切换是零成本的，也省掉一份 `data-mermaid-code` 拷贝。
 * - 工具栏不能被 `innerHTML` 冲掉。所以 `draw()` 只替换 `.md-mermaid-svg` 的内容，
 *   折叠 / 看源码这些状态都挂在容器属性上，主题切换重画也不会丢。
 */
export function mermaidPlaceholder(code: string, labels: MermaidLabels): string {
  const txt = String(code ?? '').replace(/\n$/, '')
  const add = (v: string) => escapeHtml(v)
  return (
    `<div class="md-mermaid" data-mermaid="1" data-mermaid-src="1" data-mermaid-err="${add(labels.error)}">` +
      `<div class="md-mermaid-bar">` +
        `<span class="md-mermaid-lang">mermaid</span>` +
        `<span class="md-mermaid-acts">` +
          `<button type="button" class="md-mermaid-act" data-mermaid-act="src" title="${add(labels.diagram)}" aria-label="${add(labels.diagram)}">` +
            ICONS.code + ICONS.eye +
          `</button>` +
          `<button type="button" class="md-mermaid-act" data-mermaid-act="fold" title="${add(labels.fold)}" aria-label="${add(labels.fold)}">` +
            ICONS.up + ICONS.down +
          `</button>` +
        `</span>` +
      `</div>` +
      `<div class="md-mermaid-body">` +
        sourceHtml(txt) +
        `<div class="md-mermaid-svg"></div>` +
      `</div>` +
    `</div>`
  )
}

/**
 * 把一块容器切到「看源码 / 看图」。
 *
 * 状态就存在容器的 `data-mermaid-src` 属性上 —— 不改 DOM 结构，所以不必重渲染，
 * v-html 重建之前一直有效；也便于 CSS 用一条选择器接管显示。
 */
export function setMermaidSourceMode(el: HTMLElement, on: boolean, labels?: MermaidLabels): void {
  if (on) el.setAttribute('data-mermaid-src', '1')
  else el.removeAttribute('data-mermaid-src')
  const btn = el.querySelector<HTMLElement>('[data-mermaid-act="src"]')
  if (!btn || !labels) return
  // on=true 进入源码视图 → 按钮下一步动作是「查看图」（labels.diagram）；
  // on=false 回到图形视图 → 按钮下一步动作是「查看源码」（labels.source）。
  const label = on ? labels.diagram : labels.source
  btn.setAttribute('title', label)
  btn.setAttribute('aria-label', label)
}

/**
 * 折叠 / 展开图形区。
 *
 * 一个刻意的联动：折叠状态下点「看源码」会先展开（见 handleMermaidToolbarClick），
 * 否则点了整块没有任何变化，用户会以为按钮坏了。
 */
export function setMermaidFolded(el: HTMLElement, folded: boolean, labels?: MermaidLabels): void {
  if (folded) el.setAttribute('data-mermaid-folded', '1')
  else el.removeAttribute('data-mermaid-folded')
  const btn = el.querySelector<HTMLElement>('[data-mermaid-act="fold"]')
  if (!btn || !labels) return
  const label = folded ? labels.expand : labels.fold
  btn.setAttribute('title', label)
  btn.setAttribute('aria-label', label)
}

/**
 * 工具栏点击（事件委托的落点）。返回 true 表示这次点击已经被处理，调用方不用再判断。
 *
 * 之所以放在 utils 而不是留在组件里：这段判断有两个容易写错的细节（点「看源码」前
 * 要先展开、icon 与文案要跟着状态翻），组件太大进不了单测，抽出来至少能在
 * 预览页里跑真实点击路径。
 */
export function handleMermaidToolbarClick(target: HTMLElement | null, labels: MermaidLabels): boolean {
  const btn = target?.closest?.('[data-mermaid-act]') as HTMLElement | null
  if (!btn) return false
  const wrap = btn.closest('.md-mermaid') as HTMLElement | null
  if (!wrap) return false
  if (btn.dataset.mermaidAct === 'src') {
    const showingSrc = wrap.hasAttribute('data-mermaid-src')
    // 先展开再切源码：折叠着的时候点这个按钮，两件事都得发生，否则没有任何可见变化
    if (!showingSrc) setMermaidFolded(wrap, false, labels)
    setMermaidSourceMode(wrap, !showingSrc, labels)
  } else {
    setMermaidFolded(wrap, !wrap.hasAttribute('data-mermaid-folded'), labels)
  }
  return true
}

interface MermaidApi {
  initialize: (config: Record<string, unknown>) => void
  render: (id: string, text: string) => Promise<{ svg: string; bindFunctions?: (el: Element) => void }>
}

let loading: Promise<MermaidApi> | null = null

function loadMermaid(): Promise<MermaidApi> {
  if (!loading) {
    loading = import('mermaid').then((mod) => ((mod as { default?: MermaidApi }).default ?? mod) as MermaidApi)
  }
  return loading
}

function cssVar(name: string, fallback: string): string {
  if (typeof document === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

/** 当前主题的「指纹」：主题属性 + 实际取到的底色。任一变化都说明要重配并重画 */
function themeKey(): string {
  if (typeof document === 'undefined') return 'ssr'
  return `${document.documentElement.getAttribute('data-theme') || ''}|${cssVar('--color-bg-primary', '')}`
}

let configuredFor = ''

function buildConfig(): Record<string, unknown> {
  return {
    startOnLoad: false,
    theme: 'base',
    // strict：禁用 click 交互与 label 内的 HTML 标签。
    // 这里的 SVG 是直接 innerHTML 注入的（不经过 DOMPurify），必须让 Mermaid 自己兜住。
    securityLevel: 'strict',
    // 解析失败时不要往 body 里塞一个红色错误图（流式期间每帧都会走到失败分支）
    suppressErrorRendering: true,
    fontFamily: cssVar('--font-family', 'system-ui, -apple-system, sans-serif'),
    themeVariables: {
      background: cssVar('--color-bg-primary', '#ffffff'),
      primaryColor: cssVar('--color-bg-secondary', '#f5f5f5'),
      primaryTextColor: cssVar('--color-text-primary', '#1a1a1a'),
      primaryBorderColor: cssVar('--color-border', '#d8d8d8'),
      secondaryColor: cssVar('--color-bg-hover', '#eeeeee'),
      tertiaryColor: cssVar('--color-bg-tertiary', '#ffffff'),
      lineColor: cssVar('--color-text-secondary', '#6b6b6b'),
      textColor: cssVar('--color-text-primary', '#1a1a1a'),
      fontSize: '13px',
    },
  }
}

async function applyConfig(api: MermaidApi): Promise<void> {
  const key = themeKey()
  if (configuredFor === key) return
  api.initialize(buildConfig())
  configuredFor = key
}

// 串行渲染：Mermaid 内部有全局状态（当前图类型、id 计数），并发 render 会互相串。
let queue: Promise<void> = Promise.resolve()

function enqueue(task: () => Promise<void>): void {
  queue = queue.then(task).catch(() => undefined)
}

let seq = 0

/** 失败时 Mermaid 可能在 body 上留下临时容器，清掉以免污染布局 */
function cleanupTemp(id: string): void {
  for (const sel of [`#${id}`, `#d${id}`]) {
    try {
      document.querySelector(sel)?.remove()
    } catch {
      // 选择器非法只可能出现在 id 被改写时，忽略即可
    }
  }
}

function readSource(el: HTMLElement): string {
  const fromDom = el.querySelector('.md-mermaid-src code')?.textContent
  if (fromDom) return fromDom
  return el.dataset.mermaidCode || ''
}

function showError(el: HTMLElement): void {
  if (el.querySelector('.md-mermaid-err')) return
  const box = document.createElement('div')
  box.className = 'md-mermaid-err'
  box.textContent = el.dataset.mermaidErr || 'Mermaid'
  // 压在图形区（此时是空的）上方，别跑到工具栏上面去
  const body = el.querySelector('.md-mermaid-body')
  if (body) body.prepend(box)
  else el.prepend(box)
}

async function draw(el: HTMLElement, code: string): Promise<void> {
  if (!el.isConnected) return
  const id = `qd-mmd-${++seq}`
  try {
    const api = await loadMermaid()
    await applyConfig(api)
    if (!el.isConnected) return
    const { svg, bindFunctions } = await api.render(id, code)
    if (!el.isConnected) return
    // 只替换图形区，不动工具栏与源码 <pre>：折叠 / 看源码的状态挂在容器属性上，
    // 整块 innerHTML 会把它们连同按钮一起抹掉（旧实现就是这么丢状态的）。
    const box = el.querySelector<HTMLElement>('.md-mermaid-svg')
    if (box) {
      box.innerHTML = svg
      try {
        bindFunctions?.(box)
      } catch {
        // 交互绑定失败不影响图本身
      }
    } else {
      // 兜底：没有图形区（理论上不会发生）时才整块替换
      el.innerHTML = svg
    }
    el.setAttribute('data-mermaid-done', '1')
    delete el.dataset.mermaidFailed
  } catch {
    cleanupTemp(id)
    if (el.isConnected) {
      el.dataset.mermaidFailed = hash(code)
      showError(el)
    }
  } finally {
    el.removeAttribute('data-mermaid-queued')
  }
}

export interface MermaidRenderOptions {
  /** 返回 true 的容器本轮跳过（流式尾块：源码还没写完，每帧都会失败） */
  skip?: (el: HTMLElement) => boolean
}

/**
 * 扫描并渲染 root 下所有还没画出来的占位块。
 *
 * 可以随便重复调用：已完成 / 已失败（同源码）/ 被 skip 的都会被跳过。
 * 真正的开销都在 `draw()` 里，且只发生一次。
 */
export function renderMermaidIn(root: ParentNode | null, options: MermaidRenderOptions = {}): void {
  if (!root || typeof document === 'undefined') return
  const nodes = (root as ParentNode).querySelectorAll?.(PENDING_SELECTOR)
  if (!nodes || nodes.length === 0) return
  watchTheme()
  for (const node of Array.from(nodes)) {
    const el = node as HTMLElement
    const code = readSource(el)
    if (!code.trim()) {
      el.setAttribute('data-mermaid-done', 'empty')
      continue
    }
    if (el.dataset.mermaidFailed === hash(code)) continue
    if (options.skip?.(el)) continue
    // 已在队列里等渲染（异步还没返回）：同一帧内被多次扫到也不重复入队。
    // 少了这一步，流式期间每帧的扫描会为同一张图排 6 次渲染，白烧几百毫秒。
    if (el.hasAttribute('data-mermaid-queued')) continue
    // 存一份源码：图形区被清空（主题切换）时要靠它重画。
    // 源码 <pre> 也一直在 DOM 里，这里只是不依赖它的兜底。
    el.dataset.mermaidCode = code
    el.setAttribute('data-mermaid-queued', '1')
    enqueue(() => draw(el, code))
  }
}

/** 主题变化后把已画出来的图按保存的源码重画（SVG 颜色是渲染时固定的） */
function rerenderAll(): void {
  if (typeof document === 'undefined') return
  const done = document.querySelectorAll<HTMLElement>('.md-mermaid[data-mermaid-done="1"]')
  for (const el of Array.from(done)) {
    // 只清图形区：源码 <pre> 与工具栏留着，折叠 / 看源码的状态不受主题切换影响
    const box = el.querySelector<HTMLElement>('.md-mermaid-svg')
    if (box) box.innerHTML = ''
    else el.innerHTML = sourceHtml(el.dataset.mermaidCode || '')
    el.removeAttribute('data-mermaid-done')
    delete el.dataset.mermaidFailed
  }
  renderMermaidIn(document)
}

let themeWatcher: MutationObserver | null = null

function watchTheme(): void {
  if (themeWatcher || typeof MutationObserver === 'undefined') return
  themeWatcher = new MutationObserver(() => {
    if (themeKey() === configuredFor) return
    rerenderAll()
  })
  themeWatcher.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
}
