/**
 * 从 Wails 绑定调用的错误对象中提取用户可读的错误消息。
 *
 * Wails 运行时返回的错误可能有多种格式：
 *   1. Error 实例，e.message 是 JSON 字符串: '{"message":"名称已存在","cause":{},"kind":"RuntimeError"}'
 *   2. Error 实例，e.message 是普通文本: 'Bound method returned an error: 名称已存在'
 *   3. 普通对象: { message: "名称已存在", cause: {}, kind: "RuntimeError" }
 *   4. 普通字符串
 *
 * 本函数统一提取纯净的错误文本。
 */
export function getErrorMessage(e: unknown): string {
  if (e == null) return ''

  // 先尝试取 message 字段（无论是 Error 实例还是普通对象）
  let raw: string
  if (e instanceof Error) {
    raw = e.message
  } else if (typeof e === 'object' && e !== null && 'message' in e) {
    raw = String((e as Record<string, unknown>).message)
  } else {
    raw = String(e)
  }

  return extractErrorMessage(raw)
}

function extractErrorMessage(raw: string): string {
  // 去掉 "Bound method returned an error: " 前缀
  const prefix = 'Bound method returned an error: '
  const preIdx = raw.lastIndexOf(prefix)
  if (preIdx >= 0) {
    raw = raw.substring(preIdx + prefix.length)
  }

  // 如果内容是 JSON，解析并提取 .message
  const trimmed = raw.trim()
  if (trimmed.startsWith('{')) {
    try {
      const parsed = JSON.parse(trimmed)
      if (parsed && typeof parsed.message === 'string') {
        return parsed.message
      }
    } catch {
      // 不是合法 JSON，使用原字符串
    }
  }

  return raw
}
