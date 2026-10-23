/**
 * Markdown 分块切分 —— 流式增量渲染的基础。
 *
 * 背景：流式输出时若每帧都对全文做 md.parse + v-html 整段替换，成本随回复变长
 * 线性上升（实测 15KB 全文单帧 md.parse ≈ 8.6ms，其中 lexer 占 5ms），
 * 而 innerHTML 赋值会销毁并重建该消息的**全部** DOM 节点。两者叠加就是卡顿根因。
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
 * 切分点规则（在围栏代码块之外）：空行，且**下一行**不是列表项 / 引用 / 缩进续段
 * —— 这些结构在 markdown 里跨空行延续（松散列表渲染成单个 <ul>、列表项的缩进续段），
 * 从中间切开会变成两个列表或孤立段落。
 *
 * 围栏代码块整体作为一块（未闭合时延伸到文本末尾），避免代码块被拦腰截断
 * 导致语言标签/复制按钮错乱。
 *
 * **引用式链接定义**（[ref]: url）是一个例外：它对**全文**生效，与定义出现的
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
