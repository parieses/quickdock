/**
 * 终端写入队列：把高频数据帧按每帧预算分片写进 xterm。
 *
 * 为什么需要它：后端按 16ms / 单帧 ≤64KB 合批推来，满速输出时若同步写，
 * 主线程会被灌满，进而触发 main.ts 的心跳看门狗把整个页面 reload——
 * 终端内容直接从视觉上消失。分片后每帧最多写 budget 字节，其余排到下一帧。
 *
 * 抽成独立模块（而不是写在组件里）是为了可单测：这里有两处容易写错的地方——
 *  subarray 拆分时不能越界、积压超限时必须丢旧数据而不是丢弃新数据。
 */

export interface WriteQueueOptions {
  /** 每帧最多写入的字节数，默认 64KB（与后端单帧上限一致）。 */
  budget?: number
  /** 积压上限，默认 4MB。超过后丢弃旧数据，只保留最新。 */
  maxBacklog?: number
  /** 调度器，默认 requestAnimationFrame。测试里可注入同步执行器。 */
  schedule?: (cb: () => void) => void
}

export interface WriteQueue {
  /** 入队一段字节。不拷贝数据，调用方不要再改这块内存。 */
  enqueue(bytes: Uint8Array): void
  /** 当前积压字节数（诊断用）。 */
  backlog(): number
  /** 停止调度并清空队列（组件卸载时调用）。 */
  dispose(): void
}

export function createWriteQueue(
  write: (chunk: Uint8Array) => void,
  opts: WriteQueueOptions = {},
): WriteQueue {
  const budget = opts.budget ?? 64 << 10
  const maxBacklog = opts.maxBacklog ?? 4 << 20
  const schedule = opts.schedule ?? ((cb: () => void) => requestAnimationFrame(cb))

  const queue: Uint8Array[] = []
  let bytes = 0
  let scheduled = false
  let disposed = false

  function drain() {
    scheduled = false
    if (disposed) return

    let left = budget
    while (queue.length > 0 && left > 0) {
      const head = queue[0]
      if (head.byteLength <= left) {
        queue.shift()
        bytes -= head.byteLength
        write(head)
        left -= head.byteLength
        continue
      }

      // 拆帧只在字节边界做。xterm 内部是流式解析器，能跨 write 调用接续
      // 被劈开的 UTF-8 字符和 ANSI 序列；反过来「每帧先 decode 成字符串」
      // 才会把中文变成一堆替换符。
      write(head.subarray(0, left))
      queue[0] = head.subarray(left)
      bytes -= left
      left = 0
    }

    if (queue.length > 0) scheduleNext()
  }

  function scheduleNext() {
    if (scheduled || disposed) return
    scheduled = true
    schedule(drain)
  }

  return {
    enqueue(chunk: Uint8Array) {
      if (disposed || chunk.byteLength === 0) return
      if (bytes + chunk.byteLength > maxBacklog) {
        // 消费端严重落后（或窗口被隐藏、rAF 不跑）。终端显示的是「当前画面」，
        // 旧字节已无显示价值，丢旧保新比卡死好——这与后端合批器的背压策略一致。
        queue.length = 0
        bytes = 0
      }
      queue.push(chunk)
      bytes += chunk.byteLength
      scheduleNext()
    },
    backlog: () => bytes,
    dispose() {
      disposed = true
      queue.length = 0
      bytes = 0
    },
  }
}
