// 将 CI 生成的 release notes 解析为紧凑要点列表。
// CI 用 `git log --pretty=format="- %s"` 逐行产出，本函数去掉行首项目符号、
// 合并被折行的内容、跳过空行。无要点（纯段落文本）时返回空数组，调用方回退到 <pre> 原样展示。
export function parseReleaseNotes(notes: string | undefined | null): string[] {
  if (!notes) return []
  const items: string[] = []
  let buf = ''
  const flush = () => {
    if (buf) {
      items.push(buf)
      buf = ''
    }
  }
  for (const line of notes.split('\n')) {
    const m = line.match(/^\s*[-*]\s+(.*)$/)
    if (m) {
      flush()
      items.push(m[1].trimEnd())
    } else if (line.trim() === '') {
      flush()
    } else {
      const text = line.trim()
      buf = buf ? buf + ' ' + text : text
    }
  }
  flush()
  return items
}
