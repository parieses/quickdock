/**
 * 防抖 / 节流的统一实现。
 *
 * 两者返回的都还是「可调用的函数」（不是 `{ run, cancel }` 对象），额外挂一个
 * `cancel()`：这样存量调用点一个字都不用改，需要清理的地方才用得上 cancel。
 * 组件卸载 / 切换 tab 时调用 cancel()，否则末尾那次定时器会在组件销毁后再跑一遍
 * ——轻则白打一次后端请求，重则改到已经失效的组件状态。
 */

/** debounce / throttle 返回的可调用对象：原函数签名 + 清理方法。 */
export type Debounced<T extends (...args: any[]) => any> = T & {
  /** 丢弃尚未触发的尾部调用。没有待触发调用时是无操作。 */
  cancel: () => void
}

/**
 * 防抖函数：延迟执行 fn，在 delay 毫秒内如果没有新调用则执行
 * @param fn 要防抖的函数
 * @param delay 延迟毫秒数，默认 300ms
 * @returns 防抖后的函数（带 cancel）
 */
export function debounce<T extends (...args: any[]) => any>(fn: T, delay = 300): Debounced<T> {
  let timer: ReturnType<typeof setTimeout> | null = null
  const wrapped = ((...args: any[]) => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      // 置空让之后调 cancel() 能准确判断「确实没有待触发调用」，
      // 也让闭包不再持有 args。
      timer = null
      fn(...args)
    }, delay)
  }) as Debounced<T>

  wrapped.cancel = () => {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
  }

  return wrapped
}

/**
 * 节流函数：限制 fn 在 delay 毫秒内最多执行一次
 * @param fn 要节流的函数
 * @param delay 延迟毫秒数，默认 300ms
 * @returns 节流后的函数（带 cancel）
 */
export function throttle<T extends (...args: any[]) => any>(fn: T, delay = 300): Debounced<T> {
  let lastCall = 0
  let timer: ReturnType<typeof setTimeout> | null = null
  const wrapped = ((...args: any[]) => {
    const now = Date.now()
    const remaining = delay - (now - lastCall)
    if (remaining <= 0) {
      if (timer) {
        clearTimeout(timer)
        timer = null
      }
      lastCall = now
      fn(...args)
    } else if (!timer) {
      timer = setTimeout(() => {
        lastCall = Date.now()
        timer = null
        fn(...args)
      }, remaining)
    }
  }) as Debounced<T>

  // 与 debounce 对称：throttle 也有一个「窗口结束补一次」的尾巴，
  // 卸载时不清理同样会在组件销毁后跑。
  wrapped.cancel = () => {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
  }

  return wrapped
}
