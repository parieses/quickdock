// storageQueue.ts — 合并高频 localStorage 写
//
// 为什么需要它：localStorage 是同步 API，写在热路径上会掉帧；而用到它的两处
// （导航排序的 persist、角标轮询的已读水位）都是「连续多次触发、每次都是完整快照」的写。
//
// 合并规则：同一 key 只落最后一次值；定时器不随新值重置（首次排队后 150ms 就到点）。
// 注意这与 utils/debounce 的语义不同 —— 那个每次调用都会重置计时，连续写会让落盘
// 无限往后推；这里要的是「最多攒 150ms 就写」，不是「等安静下来再写」。
//
// 每次排队必须传**完整快照**，不能传增量：合并只保留最后一个值，增量会被直接覆盖掉。
//
// 状态（pending / timer）刻意是模块级的：合并天然是全局的，两个调用方不需要各自的队列。
// 但它只服务于「写 localStorage」这一件事，所以关在本文件里，而不是散在某个 composable 内部。

const PENDING_MS = 150
const pending = new Map<string, string>()
let timer: ReturnType<typeof setTimeout> | null = null

function flush() {
  timer = null
  // 先取快照再清空：flush 期间若又有 queue() 进来，会重新排一个 timer，不会丢。
  const batch = [...pending]
  pending.clear()
  for (const [key, value] of batch) {
    try {
      localStorage.setItem(key, value)
    } catch {
      /* 隐私模式下写不了，本次会话内仍生效 */
    }
  }
}

/** 合并写入：同一 key 在窗口内多次调用只落最后一次值。value 必须是完整快照。 */
export function writeStorageDebounced(key: string, value: string) {
  pending.set(key, value)
  if (timer) return
  timer = setTimeout(flush, PENDING_MS)
}
