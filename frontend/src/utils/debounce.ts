/**
 * 防抖函数：延迟执行 fn，在 delay 毫秒内如果没有新调用则执行
 * @param fn 要防抖的函数
 * @param delay 延迟毫秒数，默认 300ms
 * @returns 防抖后的函数
 */
export function debounce<T extends (...args: any[]) => any>(fn: T, delay = 300): T {
  let timer: ReturnType<typeof setTimeout> | null = null
  return ((...args: any[]) => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      fn(...args)
    }, delay)
  }) as T
}

/**
 * 节流函数：限制 fn 在 delay 毫秒内最多执行一次
 * @param fn 要节流的函数
 * @param delay 延迟毫秒数，默认 300ms
 * @returns 节流后的函数
 */
export function throttle<T extends (...args: any[]) => any>(fn: T, delay = 300): T {
  let lastCall = 0
  let timer: ReturnType<typeof setTimeout> | null = null
  return ((...args: any[]) => {
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
  }) as T
}
