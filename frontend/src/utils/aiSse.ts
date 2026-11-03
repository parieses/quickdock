// SSE 帧解析：把「字节流 → 一组事件」这段纯搬运逻辑从 AIPage 的 send() 里抽出来。
//
// 抽它的理由不是复用（目前只有一处流），而是它和「怎么处理事件」是两件事：
// 前者是协议细节（分帧、多行 data、注释行、跨 chunk 的半个 UTF-8 字符），
// 后者是业务（工具卡片、消息固化、收尾）。混在一起的后果是每改一次业务都要
// 重读一遍分帧代码，而分帧代码里最容易写错、又最不容易被发现的恰恰是
// 「一个中文字被切在两个 chunk 里」这种边界。
export interface AiSseEvent {
  /** 事件名（没有 event: 行时按 SSE 规范作 message） */
  event: string
  /** 已 JSON.parse 过的负载 */
  data: any
}

/**
 * 读一个 SSE 响应体，逐帧回调。
 *
 * 约定：后端只发 JSON 负载，所以解析失败的帧直接丢弃（宁可少一个事件，
 * 也不要因为一行脏数据把整轮回答中断）。
 *
 * 中止靠上层传进来的 AbortSignal：fetch 的 body 在 abort 时会 reject，
 * 这里不额外判——抛出即可，send 的 catch 里按 AbortError 处理。
 */
export async function readAiSse(
  body: ReadableStream<Uint8Array>,
  onEvent: (ev: AiSseEvent) => void,
): Promise<void> {
  const reader = body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      // stream: true 让被 chunk 边界切断的多字节字符留在解码器里，
      // 下一块接上再吐——否则中文会出现乱码方块。
      buf += dec.decode(value, { stream: true })
      let sep: number
      while ((sep = buf.indexOf('\n\n')) >= 0) {
        const rawMsg = buf.slice(0, sep)
        buf = buf.slice(sep + 2)

        let event = 'message'
        let dataStr = ''
        for (const line of rawMsg.split('\n')) {
          if (line.startsWith('event: ')) {
            event = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            // 多行 data 直接拼（不加换行）：后端把整个 JSON 放在一行里，
            // 真出现多行时拼起来才是它原本写的那个值。
            dataStr += line.slice(6)
          }
          // 忽略注释行（: 开头）与空行
        }
        if (!dataStr) continue

        let data: any
        try {
          data = JSON.parse(dataStr)
        } catch {
          continue
        }
        onEvent({ event, data })
      }
    }
  } finally {
    // 提前退出（回调抛错 / 上层不要后面的帧）时把读锁还回去；
    // 有未完成的 read 时 releaseLock 会抛，此时连接已经要断了，忽略即可。
    try {
      reader.releaseLock()
    } catch {
      /* 已释放 */
    }
  }
}
