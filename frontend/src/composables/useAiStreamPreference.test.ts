import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

/**
 * 「AI 回答方式」偏好的回归测试。
 *
 * 守的是两条用户可感知的约定：
 *   1. 默认流式 —— 首次使用、存储不可用、存的值是垃圾，都必须流式；
 *   2. 只有显式关过才是一次性 —— 关掉后立即写回存储，下次启动仍然生效。
 *
 * 为什么用动态 import + resetModules：偏好是模块级单例（两个面板共享同一份），
 * 每个用例都得拿到一个「刚启动」的模块，否则上一个用例的切换会污染下一个。
 */
function stubStorage(initial: string | null): Map<string, string> {
  const store = new Map<string, string>()
  if (initial !== null) store.set('quickdock.ai.stream', initial)
  ;(globalThis as Record<string, unknown>).localStorage = {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => {
      store.set(k, v)
    },
    removeItem: (k: string) => {
      store.delete(k)
    },
  }
  return store
}

async function loadModule() {
  vi.resetModules()
  return await import('./useAiStreamPreference')
}

describe('useAiStreamPreference', () => {
  beforeEach(() => {
    delete (globalThis as Record<string, unknown>).localStorage
  })

  it('没有存储时默认流式', async () => {
    const { useAiStreamPreference } = await loadModule()
    expect(useAiStreamPreference().streamEnabled.value).toBe(true)
  })

  it('存储不可用（隐私模式）时仍默认流式', async () => {
    ;(globalThis as Record<string, unknown>).localStorage = {
      getItem() {
        throw new Error('denied')
      },
      setItem() {
        throw new Error('denied')
      },
    }
    const { useAiStreamPreference } = await loadModule()
    expect(useAiStreamPreference().streamEnabled.value).toBe(true)
  })

  it('存的是垃圾值时按流式处理', async () => {
    stubStorage('garbage')
    const { useAiStreamPreference } = await loadModule()
    expect(useAiStreamPreference().streamEnabled.value).toBe(true)
  })

  it('显式关过才是一次性', async () => {
    stubStorage('0')
    const { useAiStreamPreference } = await loadModule()
    expect(useAiStreamPreference().streamEnabled.value).toBe(false)
  })

  it('切换后写回存储，供下次启动/另一个面板读取', async () => {
    const store = stubStorage(null)
    const { useAiStreamPreference } = await loadModule()
    const { streamEnabled } = useAiStreamPreference()
    streamEnabled.value = false
    // watch 默认 pre flush，回调在微任务里执行
    await nextTick()
    expect(store.get('quickdock.ai.stream')).toBe('0')

    streamEnabled.value = true
    await nextTick()
    expect(store.get('quickdock.ai.stream')).toBe('1')
  })
})
