// 已发送消息里的粘贴块解析：AiRichInput 把大段文字序列化成
// `<pasted_text lines="N">\n原文\n</pasted_text>`，展示层（AiPastedContent）
// 按同一标记折回 chip。原文不落库之外的地方，也不在这里改动。

// 粘贴的正文里若出现字面 `</pasted_text>`（比如粘一段 HTML），序列化端会先转义
// 成 `<\/pasted_text>`；否则下面的非贪婪正则会把它当成块结束，展示层提前截断、
// chip 全乱。解析时再还原，保证预览里看到的是用户粘贴的原文。
const PASTED_CLOSE_RE = /<\/pasted_text>/gi
const ESCAPED_PASTED_CLOSE_RE = /<\\\/pasted_text>/gi

/** 序列化 pasted_text 块正文时调用：转义其中字面出现的结束标签。 */
export function escapePastedClose(text: string): string {
  return text.replace(PASTED_CLOSE_RE, '<\\/pasted_text>')
}

function unescapePastedClose(text: string): string {
  return text.replace(ESCAPED_PASTED_CLOSE_RE, '</pasted_text>')
}

export interface PastedSegment {
  text?: string
  full?: string
  lines?: string
}

/** 运行轨迹等单行摘要里用：把粘贴块压成一枚文字标记，不铺全文。
 *  label 由调用方按当前语言给（如「粘贴文本 · 398 行」）。 */
export function compactPastedBlocks(
  content: string,
  label: (lines: string) => string = (n) => `[Pasted text · ${n} lines]`,
): string {
  const re = /<pasted_text(?:\s+lines="([^"]*)")?>\n?([\s\S]*?)\n?<\/pasted_text>/g
  return content.replace(re, (_m, lines: string, full: string) => {
    const n = lines || String(full ? full.split('\n').length : 0)
    return label(n)
  })
}

/** 把消息正文切成普通文本与粘贴块；没有粘贴块时原样返回一段。 */
export function parsePastedSegments(content: string): PastedSegment[] {
  const out: PastedSegment[] = []
  // 前后各吃掉一个换行（serialize 加的包壳）；正文自带的换行不动。
  const re = /<pasted_text(?:\s+lines="([^"]*)")?>\n?([\s\S]*?)\n?<\/pasted_text>/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(content))) {
    if (m.index > last) out.push({ text: content.slice(last, m.index) })
    // 还原序列化时转义的字面结束标签，chip 预览看到的才是原文。
    out.push({ full: unescapePastedClose(m[2]), lines: m[1] || '' })
    last = re.lastIndex
  }
  if (last < content.length) out.push({ text: content.slice(last) })
  return out.length ? out : [{ text: content }]
}
