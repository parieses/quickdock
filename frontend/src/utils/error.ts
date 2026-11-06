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

/**
 * 判断错误是不是「用户在系统对话框里点了取消」。
 *
 * Wails 把取消当成错误抛出来（`Dialog.OpenFile failed: error getting selection:
 * cancelled by user`），不去区分的话，用户每按一次取消都会吃到一个红色 toast ——
 * 而这本来是他主动做的正常操作。取消不算失败，调用方应当静默返回。
 *
 * 只认这两个词：Windows 用 "cancelled"，macOS 用 "canceled"，两边都覆盖。
 */
export function isDialogCanceled(e: unknown): boolean {
  const msg = getErrorMessage(e) || String(e ?? '')
  return /cancell?ed by (the )?user/i.test(msg)
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
