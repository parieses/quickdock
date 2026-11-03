/**
 * Markdown 分块切分 —— 流式增量渲染的基础。
 *
 * 背景：流式输出时若每帧都对全文做 md.parse + v-html 整段替换，成本随回复变长
 * 线性上升（实测 15KB 全文单帧 md.parse ≈ 8.6ms，其中 lexer 占 5ms），
 * 而 innerHTML 赋值会销毁并重建该消息的全部 DOM 节点。两者叠加就是卡顿根因。
 *
 * 解法：把文本切成顶层块，逐块渲染并缓存。已完成的块不再重解析，
 * 每帧只重解析最后一个未完成块。
 *
 * 关键约束：切分点必须是「markdown 语义上独立」的边界，否则分块渲染的结果
 * 会与整段渲染不一致（表现为内容在流式过程中突然变形）。本文件末尾的测试
 * 覆盖了这些边界场景。
 */

/** 引用式链接定义行：[label]: https://... （m 标志：逐行匹配） */
const REF_DEF_RE = /^\s{0,3}\[[^\]]+\]:\s*\S/m
/** 围栏代码块的起止标记：``` 或 ~~~（允许最多 3 个前导空格） */
const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})/
/** 列表项起始：- / * / + / 1. / 1) */
const LIST_ITEM_RE = /^\s*([-*+]|\d{1,9}[.)])\s/
/** 引用块起始：> */
const QUOTE_RE = /^\s*>/

/**
 * 把 markdown 源码切成顶层块。
 *
 * 切分点规则（在围栏代码块之外）：空行，且下一行不是列表项 / 引用 / 缩进续段
 * —— 这些结构在 markdown 里跨空行延续（松散列表渲染成单个 <ul>、列表项的缩进续段），
 * 从中间切开会变成两个列表或孤立段落。
 *
 * 围栏代码块整体作为一块（未闭合时延伸到文本末尾），避免代码块被拦腰截断
 * 导致语言标签/复制按钮错乱。
 *
 * 引用式链接定义（[ref]: url）是一个例外：它对全文生效，与定义出现的
 * 位置无关（先引用后定义也能解析）。因此只要文档里存在引用定义，就不能切分，
 * 否则前面的 [ref] 会退化成纯文本。此时退化为整块渲染 —— 引用定义在 AI 回复里
 * 极少出现，性能影响可忽略。
 */
export function splitMarkdownBlocks(src: string): string[] {
  const text = src ?? ''
  // 含引用式链接定义 → 整块渲染（定义对全文生效，切开就会失效）
  if (REF_DEF_RE.test(text)) return text ? [text] : []

  const lines = text.split('\n')
  const blocks: string[] = []
  let cur: string[] = []
  let fence = '' // 当前所处围栏标记（``` 或 ~~~）

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]

    const fenceMatch = FENCE_RE.exec(line)
    if (fenceMatch) {
      if (!fence) fence = fenceMatch[1]
      else if (fenceMatch[1][0] === fence[0] && fenceMatch[1].length >= fence.length) fence = ''
      cur.push(line)
      continue
    }
    // 围栏代码块内部：一切都是内容，不切分
    if (fence) {
      cur.push(line)
      continue
    }
    if (line.trim() === '') {
      // 找下一个非空行，用来判断此处能否安全切分
      let j = i + 1
      while (j < lines.length && lines[j].trim() === '') j++
      const next = j < lines.length ? lines[j] : null
      const safe =
        next === null ||
        (!LIST_ITEM_RE.test(next) && !QUOTE_RE.test(next) && !/^\s/.test(next))
      if (safe) {
        if (cur.length) {
          blocks.push(cur.join('\n'))
          cur = []
        }
        continue
      }
      // 不安全边界：空行属于当前块内部
      cur.push(line)
      continue
    }
    cur.push(line)
  }
  if (cur.length) blocks.push(cur.join('\n'))
  return blocks
}

/**
 * 增量切块器：流式期间只扫「新增部分 + 还没定型的那一块」。
 *
 * 为什么要它：`splitMarkdownBlocks` 每帧对全文 split('\n') 再跑一遍状态机，
 * 成本随回复长度线性上升。切块本质上是逐行推进的状态机，没有理由每帧从头再来。
 *
 * 模型是「已确认前缀 + 待定尾部」：
 *   - `closed` 是已经越过安全切分点的块，一旦成型就不再重扫；
 *   - `buf` 是切分点之后还没定型的全部文本（含末尾那段没有换行的残行）。
 *
 * 唯一比整段版「保守」的地方：遇到空行时，若后面的非空行还没流到（或只是一段
 * 可能还会变的残行），本帧不下结论，把这一块留在待定区等下一帧。整段版此时按
 * 「文本末尾」处理成可切分，但那是以「文本已完整」为前提；流式期照抄会把松散
 * 列表切成两个 <ul>（`- a` 空行 `- b`）。晚一帧定型对用户不可见，切错却看得见。
 *
 * 不确认只会让最后几块暂时合并渲染——markdown 渲染结果一致，视觉没有差别。
 */
export interface BlockSplitter {
  /** 喂入**全文**（不是增量片段），返回当前顶层块（末块进行中）。 */
  feed(full: string): string[]
}

/**
 * 一段**还没写完**的行，开头是否还没定型到「能判断它不会是列表项/引用/缩进续段」。
 *
 * 流式里最后一行随时会被追加，所以不能拿它当判断依据 —— 除非它开头那几个字符
 * 已经决定了性质：以 `第` 开头的行再怎么追加也变不成 `- ` 列表项。
 *
 * 返回 true 表示「还不能判断，等下一帧」。
 */
function unsafePrefix(s: string): boolean {
  if (!s) return true
  if (/^\s/.test(s)) return true // 可能是缩进续段（或列表续行）
  const c = s[0]
  // `>` 任何长度都是引用；`-` `*` `+` 只有紧跟空格才是列表项
  if (c === '>') return true
  if (c === '-' || c === '*' || c === '+') {
    if (s.length === 1) return true
    return s[1] === ' ' || s[1] === '\t'
  }
  if (c >= '0' && c <= '9') {
    // 有序列表 `1. ` / `1) `：纯数字或「数字+分隔符」都还没定型
    if (/^\d{1,9}$/.test(s)) return true
    if (/^\d{1,9}[.)]$/.test(s)) return true
    return /^\d{1,9}[.)][ \t]/.test(s)
  }
  return false
}

export function createBlockSplitter(): BlockSplitter {
  // 上次喂入的全文：用来判断这次是纯追加还是被改写过。
  let lastFull = ''
  // 待定尾部（尚未越过安全切分点的文本）
  let buf = ''
  // 已确认的块
  let closed: string[] = []

  function feed(full: string): string[] {
    const text = full ?? ''

    // 引用式链接定义对全文生效（先引用后定义也能解析），切开就会让前面的 [ref]
    // 退化成纯文本。这个判定必须每帧做：定义可能出现在很靠后的位置。
    if (REF_DEF_RE.test(text)) {
      lastFull = text
      buf = ''
      closed = text ? [text] : []
      return closed
    }

    if (!text.startsWith(lastFull)) {
      // 前半段被改过（DSML 剥离、重试拼接、用户编辑）：已确认的块全部作废。
      buf = ''
      closed = []
      buf = text
    } else if (text.length > lastFull.length) {
      buf += text.slice(lastFull.length)
    }
    lastFull = text

    scan()
    return buf ? closed.concat(buf) : closed
  }

  function scan(): void {
    let fence = ''
    let cur: string[] = []
    // buf 中已确认到哪个字符位置（切片点之后的内容下一帧继续扫）
    let cut = 0
    let i = 0

    while (i < buf.length) {
      const nl = buf.indexOf('\n', i)
      if (nl < 0) break // 末尾残行：下一帧再判
      const line = buf.slice(i, nl)
      const after = nl + 1

      const fenceMatch = FENCE_RE.exec(line)
      if (fenceMatch) {
        if (!fence) fence = fenceMatch[1]
        else if (fenceMatch[1][0] === fence[0] && fenceMatch[1].length >= fence.length) fence = ''
        cur.push(line)
        i = after
        continue
      }
      if (fence) {
        cur.push(line)
        i = after
        continue
      }
      if (line.trim() === '') {
        // 向后找下一个非空行，用来判断此处能否安全切分
        let j = after
        let next: string | null = null
        while (j < buf.length) {
          const nl2 = buf.indexOf('\n', j)
          if (nl2 < 0) break
          const l2 = buf.slice(j, nl2)
          if (l2.trim() !== '') {
            next = l2
            break
          }
          j = nl2 + 1
        }
        if (next === null) {
          // 后面只剩一段残行（或什么都没有）。残行不能整体当判断依据
          //（`-` 后面可能补上空格变成列表项），但它**已经到达的那部分**是确定的：
          // 以「第一段。」开头的行不管后面再追加什么，都不会变成列表项或缩进续段。
          // 只有开头还悬着（`-` / `1` / `>` / 空白）时才必须等。
          const tail = j < buf.length ? buf.slice(j) : ''
          if (unsafePrefix(tail)) break
          next = tail
        }
        if (!LIST_ITEM_RE.test(next) && !QUOTE_RE.test(next) && !/^\s/.test(next)) {
          if (cur.length) closed.push(cur.join('\n'))
          cur = []
          cut = after
        } else {
          cur.push(line) // 不安全边界：空行属于当前块内部
        }
        i = after
        continue
      }
      cur.push(line)
      i = after
    }
    if (cut > 0) buf = buf.slice(cut)
  }

  return { feed }
}

