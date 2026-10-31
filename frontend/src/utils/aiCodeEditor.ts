// CodeMirror 6 封装：右侧面板的文件编辑器。
//
// 为什么整体走动态 import：CM 核心 + 本文件只有在用户第一次点「编辑」时才需要，
// 静态引会把 300KB 拖进主包；语言包同理——打开 .go 才加载 lang-go，
// 没用到的语言包永远不下载。
//
// 主题跟随 <html data-theme>：色板复用 hljs-theme.css 的 Atom One 亮暗两套，
// token 用 class 上色（HighlightStyle 的 class 规格），颜色写在 cm-theme.css。

import type { Extension } from '@codemirror/state'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'
import { i18n } from '../i18n'

/** 查找栏文案。DOM 是命令式创建的，拿不到 useI18n() 的组件上下文，直接问全局实例 */
const tf = (k: string): string => (i18n.global.t as (key: string) => string)(k)

export interface CodeEditorHandle {
  focus(): void
  destroy(): void
  /** 外部内容整体替换（父组件的 ref 被重新赋值）时同步文档，不触发 onChange，也不进 undo 历史 */
  setValue(v: string): void
  getValue(): string
  /**
   * 设置搜索词并重算命中数。空串 = 清空高亮。
   * 查找/替换栏由调用方自己写 UI（配置文件编辑器就是这么用的），
   * 这里只提供「找、跳、换」的原子操作，免得每个调用方各写一套游标逻辑。
   */
  setSearch(q: string, caseSensitive: boolean): number
  /** 跳到第 idx 个命中（0 基，允许负数回绕），返回实际下标；没有命中返回 -1 */
  gotoMatch(idx: number): number
  /** 把第 idx 个命中替换成 text；返回替换后的命中总数 */
  replaceMatch(idx: number, text: string): number
  /** 全部替换，返回替换次数 */
  replaceAllMatches(text: string): number
  /**
   * 当前选区（偏移量 + 文本）。用于「选中了就问选中的那段」，
   * 以及写回时把内容插到光标处。面板/弹层打开后编辑器失焦也不影响——
   * CM 的选区存在 state 里，不像 textarea 那样失焦就归 0。
   */
  getSelection(): { from: number; to: number; text: string }
  /** 滚动容器（CM 的 .cm-scroller）。用于与预览栏做滚动同步 */
  scrollEl(): HTMLElement | null
  /** 订阅滚动；返回取消订阅函数（实例销毁后调用也无害） */
  onScroll(cb: () => void): () => void
  /** 主动打开查找栏（等同于在编辑器里按 Ctrl+F） */
  openFind(): void
}

export interface CodeEditorOpts {
  value: string
  /**
   * 用于选语言的文件名。空串 = 按配置文件处理（properties/ini 着色），
   * 这是给「环境管理里编辑配置」这种没有扩展名可依的场景留的兜底。
   */
  filename: string
  /**
   * 只读模式（截断文件：只读到前 512KB，保存会丢掉没读到的后半截）。
   * 只读时仍然挂 CM —— 行号/高亮/搜索照用，只是不给改。
   */
  readOnly?: boolean
  onChange: (v: string) => void
  /** Ctrl+S 触发；调用方自行决定保存后的提示 */
  onSave: () => void
  /** Ctrl+F 触发：有回调就拦下 CM 自带搜索面板，让调用方开自己的查找栏 */
  onSearch?: () => void
  /** 光标移动回调，用于状态栏「行:列」 */
  onSelectionChange?: (line: number, col: number) => void
  /** 字号，默认 12px（配置文件编辑器习惯略大一点） */
  fontSize?: string
  /** 字体族，默认等宽。笔记这类正文编辑传 --font-family */
  fontFamily?: string
  /**
   * 软换行。代码默认不折行（横向滚动保持缩进结构可读），
   * 笔记这种自然语言段落必须开——否则一段话要横向拉着看。
   */
  lineWrapping?: boolean
  /** 行号栏，默认显示。随手记一类的短文本不需要 */
  lineNumbers?: boolean
  /** 空文档时的占位文案 */
  placeholder?: string
  /**
   * Tab 键插入缩进（默认 true，代码编辑器该如此）。
   * 正文写作传 false：用户的 Tab 是想离开输入框，被吃成缩进会很难受。
   */
  tabIndent?: boolean
}

// 语言按扩展名懒加载。default 返回 null = 纯文本编辑（行号/搜索/undo 照有）。
async function languageFor(filename: string): Promise<Extension | null> {
  const name = (filename || '').trim().toLowerCase()
  // 没有文件名（配置文件编辑器：nginx / my.cnf / php.ini 一堆）按 ini 着色兜底，
  // 那类「键 值 / 键 = 值」的文本至少能让注释与字符串有区分。
  if (!name) {
    const [{ StreamLanguage }, m] = await Promise.all([
      import('@codemirror/language'),
      import('@codemirror/legacy-modes/mode/properties'),
    ])
    return StreamLanguage.define(m.properties)
  }
  const base = name.slice(Math.max(name.lastIndexOf('/'), name.lastIndexOf('\\')) + 1)
  const ext = base.slice(base.lastIndexOf('.') + 1)
  switch (ext) {
    case 'js': case 'mjs': case 'cjs':
      return (await import('@codemirror/lang-javascript')).javascript()
    case 'jsx':
      return (await import('@codemirror/lang-javascript')).javascript({ jsx: true })
    case 'ts': case 'mts':
      return (await import('@codemirror/lang-javascript')).javascript({ typescript: true })
    case 'tsx':
      return (await import('@codemirror/lang-javascript')).javascript({ typescript: true, jsx: true })
    case 'vue':
      return (await import('@codemirror/lang-vue')).vue()
    case 'html': case 'htm': case 'xhtml':
      return (await import('@codemirror/lang-html')).html()
    case 'css': case 'scss': case 'less':
      return (await import('@codemirror/lang-css')).css()
    case 'json': case 'jsonc':
      return (await import('@codemirror/lang-json')).json()
    case 'md': case 'markdown': case 'mdx':
      return (await import('@codemirror/lang-markdown')).markdown()
    case 'py':
      return (await import('@codemirror/lang-python')).python()
    case 'go':
      return (await import('@codemirror/lang-go')).go()
    case 'rs':
      return (await import('@codemirror/lang-rust')).rust()
    case 'java':
      return (await import('@codemirror/lang-java')).java()
    case 'c': case 'h': case 'cpp': case 'cc': case 'hpp': case 'hh':
      return (await import('@codemirror/lang-cpp')).cpp()
    case 'php':
      return (await import('@codemirror/lang-php')).php()
    case 'sql':
      return (await import('@codemirror/lang-sql')).sql()
    case 'yml': case 'yaml':
      return (await import('@codemirror/lang-yaml')).yaml()
    case 'sh': case 'bash': case 'zsh': {
      const [{ StreamLanguage }, m] = await Promise.all([
        import('@codemirror/language'),
        import('@codemirror/legacy-modes/mode/shell'),
      ])
      return StreamLanguage.define(m.shell)
    }
    case 'toml': {
      const [{ StreamLanguage }, m] = await Promise.all([
        import('@codemirror/language'),
        import('@codemirror/legacy-modes/mode/toml'),
      ])
      return StreamLanguage.define(m.toml)
    }
    // .conf / .cnf：nginx、apache、redis、mysql 全是这一族。nginx 的规则
    //（# 注释、directive 参数、{} 块）比 ini 更接近它们的共同形状。
    case 'conf': case 'cnf': case 'nginx': {
      const [{ StreamLanguage }, m] = await Promise.all([
        import('@codemirror/language'),
        import('@codemirror/legacy-modes/mode/nginx'),
      ])
      return StreamLanguage.define(m.nginx)
    }
    // .ini / .properties / .env / .cfg：严格的键值对，用 properties 比 nginx 准
    case 'ini': case 'properties': case 'env': case 'cfg': {
      const [{ StreamLanguage }, m] = await Promise.all([
        import('@codemirror/language'),
        import('@codemirror/legacy-modes/mode/properties'),
      ])
      return StreamLanguage.define(m.properties)
    }
    default:
      return null
  }
}

// token → class，颜色在 cm-theme.css 里按 data-theme 给（亮暗各一套 Atom One 色板）。
// 用 class 而不是内联样式：暗色/亮色的切换只动 CSS，不重建编辑器。
const cmTokens = HighlightStyle.define([
  { tag: [t.comment, t.quote], class: 'cm-tok-comment' },
  { tag: [t.keyword, t.modifier, t.operatorKeyword, t.controlKeyword, t.moduleKeyword, t.definitionKeyword], class: 'cm-tok-keyword' },
  { tag: [t.string, t.special(t.string), t.regexp, t.character], class: 'cm-tok-string' },
  { tag: [t.number, t.bool, t.null, t.atom], class: 'cm-tok-number' },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.macroName], class: 'cm-tok-func' },
  { tag: [t.typeName, t.className, t.namespace], class: 'cm-tok-type' },
  { tag: [t.propertyName, t.attributeName], class: 'cm-tok-prop' },
  { tag: [t.variableName, t.labelName], class: 'cm-tok-var' },
  { tag: [t.tagName], class: 'cm-tok-tag' },
  { tag: [t.meta, t.processingInstruction, t.annotation], class: 'cm-tok-meta' },
  { tag: [t.heading, t.strong], class: 'cm-tok-func' },
  { tag: [t.emphasis], class: 'cm-tok-comment' },
  { tag: [t.link, t.url], class: 'cm-tok-string' },
  { tag: t.invalid, class: 'cm-tok-invalid' },
])

export async function createCodeEditor(host: HTMLElement, opts: CodeEditorOpts): Promise<CodeEditorHandle> {
  const [{ basicSetup, EditorView }, { keymap, placeholder: cmPlaceholder }, { indentWithTab }, { EditorState, Transaction }, searchMod, lang] =
    await Promise.all([
      import('codemirror'),
      import('@codemirror/view'),
      import('@codemirror/commands'),
      import('@codemirror/state'),
      import('@codemirror/search'),
      languageFor(opts.filename),
    ])
  await import('../styles/cm-theme.css')

  // 搜索状态：命中列表 + 跳转 + 替换的原子操作，内置查找栏与调用方共用。
  let searchQ = ''
  let searchCS = false
  let matches: { from: number; to: number }[] = []
  // 查找栏的交互状态。声明在 view 之前：updateListener 里要用到它们，
  // 而 updateListener 是 view 构造时就绑上去的。
  let caseSensitive = false
  let curIdx = 0
  let syncTimer = 0
  // findBar 在 view 之后才创建（要先有 view 才能绑事件），updateListener 里用这个
  // 标志短路，避免构造期万一触发一次事务时踩到 TDZ。
  let barReady = false
  function collectMatches(): { from: number; to: number }[] {
    const out: { from: number; to: number }[] = []
    if (!searchQ) return out
    const doc = view.state.doc
    const cur = new searchMod.SearchCursor(
      doc, searchQ, 0, doc.length,
      searchCS ? undefined : (s: string) => s.toLowerCase(),
    )
    for (let step = cur.next(); !step.done; step = cur.next()) {
      out.push({ from: step.value.from, to: step.value.to })
      if (out.length >= 5000) break
    }
    return out
  }

  const view = new EditorView({
    doc: opts.value,
    extensions: [
      // 自定义键位放最前：basicSetup 自带的 searchKeymap 会抢 Mod-f 去开它自己的面板，
      // 这个面板和项目的界面风格不搭——默认交给内置查找栏，调用方想接管就传 onSearch。
      keymap.of([
        { key: 'Mod-s', run: () => { opts.onSave(); return true } },
        { key: 'Mod-f', run: () => { if (opts.onSearch) opts.onSearch(); else openFind(); return true } },
        { key: 'Mod-g', run: () => (findBar.hidden ? false : (stepMatch(1), true)) },
        { key: 'Mod-Shift-g', run: () => (findBar.hidden ? false : (stepMatch(-1), true)) },
        { key: 'Escape', run: () => (findBar.hidden ? false : (closeFind(), true)) },
        // Tab 插入缩进只在代码场景要；正文写作里 Tab 应该能离开输入框
        ...(opts.tabIndent === false ? [] : [indentWithTab]),
      ]),
      basicSetup,
      // 这个扩展的默认面板被上面的 Mod-f 拦掉了，但它同时提供命中高亮
      //（setSearchQuery effect 的消费者）——留着，只是永远不露出它自己的 UI
      searchMod.search({ top: true }),
      opts.lineWrapping ? EditorView.lineWrapping : [],
      opts.placeholder ? cmPlaceholder(opts.placeholder) : [],
      EditorView.theme({
        '&': { height: '100%', fontSize: opts.fontSize || '12px', backgroundColor: 'transparent' },
        '.cm-scroller': { fontFamily: opts.fontFamily || 'var(--font-mono)', lineHeight: '1.6' },
        '.cm-content': { caretColor: 'var(--color-text-primary)', paddingBottom: '24px' },
        '.cm-gutters': {
          backgroundColor: 'var(--color-bg-tertiary)',
          color: 'var(--color-text-muted)',
          border: 'none',
          borderRight: '1px solid var(--color-border)',
          // 不要行号就别留一条空 gutter（basicSetup 里 lineNumbers 是写死的，
          // 拆掉它得自己重装整套扩展，代价远大于一行 display:none）
          ...(opts.lineNumbers === false ? { display: 'none' } : {}),
        },
        '.cm-activeLine': { backgroundColor: 'var(--color-bg-tertiary)' },
        '.cm-activeLineGutter': { backgroundColor: 'var(--color-bg-tertiary)' },
        '.cm-selectionBackground': { backgroundColor: 'var(--color-accent-muted) !important' },
        '&.cm-focused .cm-selectionBackground': { backgroundColor: 'var(--color-accent-muted) !important' },
        '.cm-cursor': { borderLeftColor: 'var(--color-text-primary)' },
        // 搜索命中：与项目其它地方的黄底一致（cm-theme.css 里没有这块）
        '.cm-searchMatch': { backgroundColor: 'var(--color-search-hit-bg)', outline: 'none' },
        '.cm-searchMatch.cm-searchMatch-selected': {
          backgroundColor: 'var(--color-search-hit-bg-active)',
          outline: '1px solid var(--color-warning)',
        },
      }),
      syntaxHighlighting(cmTokens),
      lang ? [lang] : [],
      // 只读：编辑与光标交互一起关掉，但保留高亮/行号/滚动/搜索
      opts.readOnly ? [EditorState.readOnly.of(true), EditorView.editable.of(false)] : [],
      EditorView.updateListener.of((u) => {
        if (u.docChanged) {
          opts.onChange(view.state.doc.toString())
          // 栏开着时用户还能接着改文档，命中数得跟上（防抖：连打不停就别每键跑全文）
          if (barReady && !findBar.hidden) {
            window.clearTimeout(syncTimer)
            syncTimer = window.setTimeout(syncBarCount, 160)
          }
        }
        if (opts.onSelectionChange && (u.selectionSet || u.docChanged)) {
          const head = view.state.selection.main.head
          const line = view.state.doc.lineAt(head)
          opts.onSelectionChange(line.number, head - line.from + 1)
        }
      }),
    ],
    parent: host,
  })

  // ---- 命中的跳转与替换 ----
  //
  // 刻意不在这里 view.focus()：查找栏打开时焦点应该留在输入框里，
  // 每输入一个字符就把焦点抢回编辑器，第二个字符就打不进去了。
  function setSearch(q: string, cs: boolean): number {
    searchQ = q
    searchCS = cs
    matches = collectMatches()
    // 命中底色交给 CM：setSearchQuery 会让它把全文匹配标成 .cm-searchMatch
    view.dispatch({
      effects: searchMod.setSearchQuery.of(
        new searchMod.SearchQuery({ search: q, caseSensitive: cs, literal: true }),
      ),
    })
    return matches.length
  }

  function gotoMatch(idx: number): number {
    const n = matches.length
    if (!n) return -1
    const i = ((idx % n) + n) % n
    const m = matches[i]
    view.dispatch({
      selection: { anchor: m.from, head: m.to },
      effects: EditorView.scrollIntoView(m.from, { y: 'center' }),
    })
    return i
  }

  function replaceMatch(idx: number, text: string): number {
    const m = matches[idx]
    if (!m) return matches.length
    view.dispatch({ changes: { from: m.from, to: m.to, insert: text } })
    matches = collectMatches()
    return matches.length
  }

  function replaceAllMatches(text: string): number {
    if (!matches.length) return 0
    const n = matches.length
    // 命中按文档升序且互不重叠，正好是 CM changes 数组要求的形状
    view.dispatch({ changes: matches.map((m) => ({ from: m.from, to: m.to, insert: text })) })
    matches = collectMatches()
    return n
  }

  // ---- 内置查找 / 替换栏 ----
  //
  // 不用 CM 自带的 search 面板：那个面板是 CM 自己的一套 UI，配色、圆角、按钮尺寸
  // 跟项目主题两张皮，中文下字还会被挤。这里用项目 token 画一条，所有调用方默认
  // 就有（Ctrl+F 直达），想接自己的实现就传 onSearch 接管。
  //
  // 定位用 absolute 覆盖在编辑器右上角，不插进文档流：宿主高度由各调用方的 flex
  // 决定，插一条会占高度的栏等于把笔记分栏、右侧面板、配置弹窗三处布局都改一遍。
  host.classList.add('qd-cmhost')
  const findBar = document.createElement('div')
  findBar.className = 'qd-cmfind'
  findBar.hidden = true
  findBar.innerHTML = [
    '<div class="qd-cmfind-row">',
    '<input class="qd-cmfind-input" data-find spellcheck="false" />',
    '<span class="qd-cmfind-count" data-count></span>',
    '<button type="button" class="qd-cmfind-btn" data-act="prev" tabindex="-1">↑</button>',
    '<button type="button" class="qd-cmfind-btn" data-act="next" tabindex="-1">↓</button>',
    '<button type="button" class="qd-cmfind-btn" data-act="case" tabindex="-1">Aa</button>',
    '<button type="button" class="qd-cmfind-btn" data-act="repl" tabindex="-1">⇄</button>',
    '<button type="button" class="qd-cmfind-btn" data-act="close" tabindex="-1">×</button>',
    '</div>',
    '<div class="qd-cmfind-row" data-repl-row hidden>',
    '<input class="qd-cmfind-input" data-repl-input spellcheck="false" />',
    '<button type="button" class="qd-cmfind-btn" data-act="replace" tabindex="-1"></button>',
    '<button type="button" class="qd-cmfind-btn" data-act="replace-all" tabindex="-1"></button>',
    '</div>',
  ].join('')
  host.appendChild(findBar)
  barReady = true

  const findInput = findBar.querySelector<HTMLInputElement>('[data-find]')!
  const replRow = findBar.querySelector<HTMLElement>('[data-repl-row]')!
  const replInput = findBar.querySelector<HTMLInputElement>('[data-repl-input]')!
  const countEl = findBar.querySelector<HTMLElement>('[data-count]')!
  const caseBtn = findBar.querySelector<HTMLButtonElement>('[data-act="case"]')!
  const replBtn = findBar.querySelector<HTMLButtonElement>('[data-act="repl"]')!

  // 按 data-act 取按钮。这里踩过一次：早先写成 [data-replace] / [data-replace-all]，
  // 而实际属性是 data-act="replace"，querySelector 返回 null —— `!.` 断言在运行时
  // 就是 null.textContent = … 抛 TypeError，而它排在 findBar.hidden=false 之前，
  // 于是 Ctrl+F 的表现是「什么都不发生」（栏一次都没露出来）。
  function findBtn(act: string): HTMLElement | null {
    return findBar.querySelector<HTMLElement>(`[data-act="${act}"]`)
  }

  function refreshFindLabels() {
    findInput.placeholder = tf('ceFind')
    replInput.placeholder = tf('ceReplaceWith')
    caseBtn.title = tf('ceCase')
    caseBtn.classList.toggle('on', caseSensitive)
    replBtn.title = tf('ceReplace')
    replBtn.classList.toggle('on', !replRow.hidden)
    const repl = findBtn('replace')
    const replAll = findBtn('replace-all')
    if (repl) repl.textContent = tf('ceReplace')
    if (replAll) replAll.textContent = tf('ceReplaceAll')
    const prev = findBtn('prev')
    const next = findBtn('next')
    const close = findBtn('close')
    if (prev) prev.title = tf('cePrev')
    if (next) next.title = tf('ceNext')
    if (close) close.title = tf('close')
  }

  function refreshCount() {
    const n = matches.length
    countEl.textContent = n ? `${curIdx + 1} / ${n}` : (findInput.value ? tf('ceNoMatch') : '')
  }

  function syncBarCount() {
    matches = collectMatches()
    if (curIdx >= matches.length) curIdx = 0
    refreshCount()
  }

  function applySearch() {
    const n = setSearch(findInput.value, caseSensitive)
    curIdx = n > 0 ? Math.max(gotoMatch(0), 0) : 0
    refreshCount()
  }

  function stepMatch(delta: number) {
    if (!matches.length) return
    curIdx = Math.max(gotoMatch(curIdx + delta), 0)
    refreshCount()
  }

  function openFind() {
    // 先让栏可见，再刷文案：文案里任何一处取不到节点都不该让「打开查找栏」这件事失败
    //（曾经就是这样：一次 null.textContent 让 Ctrl+F 完全没反应，还看不出原因）。
    findBar.hidden = false
    try {
      refreshFindLabels()
    } catch (e) {
      console.error('[aiCodeEditor] find bar labels', e)
    }
    // 有单行选中就带进来（多行选区是「找一段」，不是找一个词）
    const sel = view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to)
    if (sel && !sel.includes('\n') && sel.length <= 120) findInput.value = sel
    findInput.focus()
    findInput.select()
    if (findInput.value) applySearch()
    else { matches = []; setSearch('', false); refreshCount() }
  }

  function closeFind() {
    findBar.hidden = true
    replRow.hidden = true
    matches = []
    setSearch('', false)
    refreshCount()
    view.focus()
  }

  function doReplace() {
    if (!matches.length) return
    const left = replaceMatch(curIdx, replInput.value)
    // 替换后命中少一处：停在原地就等于跳到下一个
    curIdx = left > 0 ? Math.max(gotoMatch(Math.min(curIdx, left - 1)), 0) : 0
    refreshCount()
  }

  function doReplaceAll() {
    if (!matches.length) return
    replaceAllMatches(replInput.value)
    // 文本变了，CM 的查询高亮要重新发一次，否则命中底色停在旧位置
    const n = setSearch(findInput.value, caseSensitive)
    curIdx = n > 0 ? Math.max(gotoMatch(0), 0) : 0
    refreshCount()
  }

  findInput.addEventListener('input', applySearch)
  findInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); stepMatch(e.shiftKey ? -1 : 1) }
    else if (e.key === 'Escape') { e.preventDefault(); closeFind() }
  })
  replInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); doReplace() }
    else if (e.key === 'Escape') { e.preventDefault(); replRow.hidden = true; findInput.focus() }
  })
  findBar.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement).closest<HTMLButtonElement>('[data-act]')
    if (!btn) return
    switch (btn.dataset.act) {
      case 'prev': stepMatch(-1); break
      case 'next': stepMatch(1); break
      case 'case':
        caseSensitive = !caseSensitive
        btn.classList.toggle('on', caseSensitive)
        applySearch()
        break
      case 'repl':
        replRow.hidden = !replRow.hidden
        btn.classList.toggle('on', !replRow.hidden)
        if (!replRow.hidden) replInput.focus()
        break
      case 'replace': doReplace(); break
      case 'replace-all': doReplaceAll(); break
      case 'close': closeFind(); break
    }
  })

  // 宿主级兜底：CM 的 keymap 只在 contentDOM 自己收到 keydown 时才触发（焦点在行号槽、
  // 滚动条、或 editable=false 的只读编辑器上时压根收不到）。这里在宿主上捕一层，
  // 拦下后 stopPropagation 掐掉 CM 的重复处理 —— 只有一个入口，不会开两次。
  function onHostKeydown(e: KeyboardEvent) {
    if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey) return
    if ((e.key || '').toLowerCase() !== 'f') return
    e.preventDefault()
    e.stopPropagation()
    if (opts.onSearch) opts.onSearch()
    else openFind()
  }
  host.addEventListener('keydown', onHostKeydown, true)

  return {
    focus: () => view.focus(),
    destroy: () => {
      window.clearTimeout(syncTimer)
      host.removeEventListener('keydown', onHostKeydown, true)
      findBar.remove()
      view.destroy()
    },
    openFind,
    setValue: (v: string) => {
      const cur = view.state.doc
      // 先比长度：这里每次击键都会被 v-model 的 watch 调一次（内容和刚 emit 上去的
      // 一模一样），对几百 KB 的配置做 toString 全量比较纯属浪费。
      if (v.length === cur.length && v === cur.toString()) return
      // 不进 undo 历史：这是「父组件换了内容」，不是用户的一次编辑，
      // 否则 Ctrl+Z 会退回到上一个文件的内容。
      view.dispatch({
        changes: { from: 0, to: cur.length, insert: v },
        annotations: Transaction.addToHistory.of(false),
      })
    },
    getValue: () => view.state.doc.toString(),
    getSelection: () => {
      const r = view.state.selection.main
      return { from: r.from, to: r.to, text: view.state.sliceDoc(r.from, r.to) }
    },
    scrollEl: () => view.scrollDOM,
    onScroll: (cb) => {
      const el = view.scrollDOM
      el.addEventListener('scroll', cb, { passive: true })
      return () => el.removeEventListener('scroll', cb)
    },
    setSearch,
    gotoMatch,
    replaceMatch,
    replaceAllMatches,
  }
}
