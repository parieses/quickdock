import { afterEach, describe, expect, it, vi } from 'vitest'

// useToast 是模块级单例，这里 spy 它的 error 方法
vi.mock('../composables/useToast', () => ({
  useToast: () => ({ error: errorSpy }),
}))
const errorSpy = vi.fn()

describe('reportError', () => {
  afterEach(() => errorSpy.mockClear())

  it('默认等价于 toast.error(getErrorMessage(e)) + logErr', async () => {
    const { reportError } = await import('./reportError')
    expect(reportError(new Error('boom'), 'tag')).toBe(true)
    expect(errorSpy).toHaveBeenCalledWith('boom')
  })

  it('silent 不弹 toast', async () => {
    const { reportError } = await import('./reportError')
    expect(reportError(new Error('boom'), 'tag', { silent: true })).toBe(false)
    expect(errorSpy).not.toHaveBeenCalled()
  })

  it('suppressCancel 吞掉用户取消', async () => {
    const { reportError } = await import('./reportError')
    const e = new Error('Dialog.OpenFile failed: cancelled by user')
    expect(reportError(e, 'tag', { suppressCancel: true })).toBe(false)
    expect(errorSpy).not.toHaveBeenCalled()
  })

  it('fallback 在消息为空时生效，message 可完整覆盖', async () => {
    const { reportError } = await import('./reportError')
    reportError('', 'tag', { fallback: '兜底' })
    expect(errorSpy).toHaveBeenCalledWith('兜底')
    errorSpy.mockClear()
    reportError(new Error('x'), 'tag', { message: '自定义' })
    expect(errorSpy).toHaveBeenCalledWith('自定义')
  })
})
