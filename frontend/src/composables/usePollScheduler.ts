// usePollScheduler — 统一的轮询调度器
//
// 解决的问题（环境管理页原先是三个独立定时器 + 一堆 `pollStatus()` 直呼）：
//   1. 没有合并：一次操作里连打三四个 `pollStatus()`，每个都跑一整轮
//      （每轮 = 所有运行时 × 所有版本的状态查询），请求被放大数倍；
//   2. 定时器用 setInterval，一轮探测比间隔还长时下一轮照样排进来，请求叠加；
//   3. 窗口最小化/后台时照常轮询，笔记本白烧电；
//   4. 正在跑的时候来个「刷新」请求会被布尔 guard 直接丢弃，
//      用户点了按钮却看到旧状态，只能等下一个 tick。
//
// 这里用 setTimeout 链（跑完再排下一次）+ 串行队列（在跑的请求结束后补跑一次）+
// 合并窗口，把四件事一起收掉。三种触发方式各有用处：
//   start()      周期轮询
//   requestNow() 动作后的「尽快刷新」（可合并，不等待）
//   runNow()     动作后的「等这轮结果」（如启动服务后要立刻拿到 running 状态）
import { getCurrentInstance, onUnmounted, ref } from 'vue'
import type { Ref } from 'vue'

export interface PollSchedulerOptions {
  /** 常规轮询间隔（ms），默认 3000 */
  interval?: number
  /** requestNow 的合并窗口（ms）：窗口内的重复请求只跑一次，默认 600 */
  minGap?: number
  /** 页面不可见时自动暂停，默认 true */
  pauseWhenHidden?: boolean
}

export interface PollScheduler {
  /** 开始周期轮询（幂等） */
  start(): void
  /** 停止并清空所有定时器（幂等） */
  stop(): void
  /** 请求尽快跑一轮（合并窗口内只跑一次，不等待结果） */
  requestNow(): void
  /** 立刻跑一轮并等待结果；已有在跑时会排在它之后补跑 */
  runNow(): Promise<void>
  /** 手动暂停（如 KeepAlive 失活）；不会清掉间隔设置 */
  pause(): void
  /** 手动恢复，并立刻补一轮 */
  resume(): void
  /** 当前间隔，可热改（如按负载伸缩） */
  intervalMs: Ref<number>
  isRunning: Ref<boolean>
}

export function usePollScheduler(
  run: () => Promise<void> | void,
  options: PollSchedulerOptions = {},
): PollScheduler {
  const intervalMs = ref(options.interval ?? 3000)
  const minGap = options.minGap ?? 600
  const pauseWhenHidden = options.pauseWhenHidden ?? true

  const isRunning = ref(false)
  const paused = ref(false)

  let timer: ReturnType<typeof setTimeout> | null = null
  let reqTimer: ReturnType<typeof setTimeout> | null = null
  let started = false
  let lastRunAt = 0
  // inflight 是「正在跑（或即将补跑）的那一串」；needAgain 表示期间又有人要刷新
  let inflight: Promise<void> | null = null
  let needAgain = false

  function clearTimer() {
    if (timer) { clearTimeout(timer); timer = null }
  }
  function clearReqTimer() {
    if (reqTimer) { clearTimeout(reqTimer); reqTimer = null }
  }

  function schedule(delay: number) {
    clearTimer()
    if (!started || paused.value) return
    timer = setTimeout(() => { timer = null; void tick() }, Math.max(0, delay))
  }

  async function runOnce() {
    try {
      await run()
    } catch {
      // 单轮失败不该让调度器停摆；错误由调用方自己记录
    }
    lastRunAt = Date.now()
  }

  // 串行队列：一轮没跑完时进来的请求不并发，而是标记 needAgain 在本轮结束后补跑。
  // 这样「点按钮 → 立刻看到新状态」不会被撞上后台 tick 而丢弃，
  // 也不会两个整轮同时压到后端。
  function drain(): Promise<void> {
    if (inflight) { needAgain = true; return inflight }
    isRunning.value = true
    const p = (async () => {
      await runOnce()
      while (needAgain) { needAgain = false; await runOnce() }
    })()
    inflight = p.finally(() => {
      inflight = null
      isRunning.value = false
    })
    return inflight
  }

  async function tick() {
    if (!started || paused.value) return
    await drain()
    // 用 setTimeout 链而不是 setInterval：一轮比间隔长时不会叠加请求
    if (started && !paused.value) schedule(intervalMs.value)
  }

  function requestNow() {
    if (!started) { void drain(); return }
    if (inflight) { needAgain = true; return }
    if (reqTimer) return
    const wait = Math.max(0, minGap - (Date.now() - lastRunAt))
    reqTimer = setTimeout(() => {
      reqTimer = null
      if (!started || paused.value) return
      void drain().then(() => { if (started && !paused.value) schedule(intervalMs.value) })
    }, wait)
  }

  function runNow(): Promise<void> {
    if (inflight) { needAgain = true; return inflight }
    clearReqTimer()
    return drain()
  }

  function start() {
    if (started) return
    started = true
    if (pauseWhenHidden) {
      paused.value = typeof document !== 'undefined' && document.hidden
      document.addEventListener('visibilitychange', onVisibility)
    }
    schedule(intervalMs.value)
  }

  function stop() {
    started = false
    clearTimer()
    clearReqTimer()
    if (pauseWhenHidden && typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }

  function pause() {
    paused.value = true
    clearTimer()
    clearReqTimer()
  }

  function resume() {
    if (!paused.value) return
    paused.value = false
    schedule(0)
  }

  // 后台标签页不轮询；回到前台立刻补一轮，
  // 否则第一眼看到的是几分钟前的状态（切回来那一下最容易误判服务挂了）
  function onVisibility() {
    if (document.hidden) {
      pause()
    } else if (started) {
      paused.value = false
      requestNow()
    }
  }

  // 组件卸载时兜底清理：页面自己忘了调 stop 也不会留下定时器。
  // getCurrentInstance 判空是为了让单测能在组件外直接调用它。
  if (getCurrentInstance()) onUnmounted(stop)

  return { start, stop, requestNow, runNow, pause, resume, intervalMs, isRunning }
}
