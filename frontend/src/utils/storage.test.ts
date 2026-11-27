import { afterEach, describe, expect, it, vi } from 'vitest'
import { removeStored, writeStored } from './storage'

describe('storage pref 写入', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('正常写入/删除', () => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      setItem: (k: string, v: string) => store.set(k, v),
      removeItem: (k: string) => store.delete(k),
    })
    writeStored('k', 'v')
    expect(store.get('k')).toBe('v')
    removeStored('k')
    expect(store.has('k')).toBe(false)
  })

  it('setItem/removeItem 抛错时静默吞掉，不影响调用方', () => {
    vi.stubGlobal('localStorage', {
      setItem: () => { throw new DOMException('quota', 'QuotaExceededError') },
      removeItem: () => { throw new Error('disabled') },
    })
    expect(() => writeStored('k', 'v')).not.toThrow()
    expect(() => removeStored('k')).not.toThrow()
  })
})
