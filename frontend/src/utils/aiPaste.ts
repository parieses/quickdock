// 已发送消息里的粘贴块解析：AiRichInput 把大段文字序列化成
// `<pasted_text lines="N">\n原文\n</pasted_text>`，展示层（AiPastedContent）
// 按同一标记折回 chip。原文不落库之外的地方，也不在这里改动。

export interface PastedSegment {
  text?: string
  full?: string
  lines?: string
}

/** 把消息正文切成普通文本与粘贴块；没有粘贴块时原样返回一段。 */
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

export function parsePastedSegments(content: string): PastedSegment[] {
  const out: PastedSegment[] = []
  // 前后各吃掉一个换行（serialize 加的包壳）；正文自带的换行不动。
  const re = /<pasted_text(?:\s+lines="([^"]*)")?>\n?([\s\S]*?)\n?<\/pasted_text>/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(content))) {
    if (m.index > last) out.push({ text: content.slice(last, m.index) })
    out.push({ full: m[2], lines: m[1] || '' })
    last = re.lastIndex
  }
  if (last < content.length) out.push({ text: content.slice(last) })
  return out.length ? out : [{ text: content }]
}
