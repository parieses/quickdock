import { describe, expect, it } from 'vitest'
import { base64ToBytes, bytesToBase64, textToBytes } from './base64'

describe('bytesToBase64 / base64ToBytes', () => {
  it('往返后字节完全一致', () => {
    const bytes = new Uint8Array([0, 1, 127, 128, 255, 65, 66])
    expect(Array.from(base64ToBytes(bytesToBase64(bytes)))).toEqual(Array.from(bytes))
  })

  it('空数组编码为空串', () => {
    expect(bytesToBase64(new Uint8Array(0))).toBe('')
    expect(base64ToBytes('').length).toBe(0)
  })

  it('不会因为分块边界而损坏数据（64KB 以上）', () => {
    // 超过内部 0x8000 分块阈值，覆盖「跨块拼接」这条路径。
    const bytes = new Uint8Array(200_000)
    for (let i = 0; i < bytes.length; i++) bytes[i] = i % 256
    const round = base64ToBytes(bytesToBase64(bytes))
    expect(round.length).toBe(bytes.length)

    // 逐字节比对而不是 toEqual：vitest 对 20 万元素的数组做深比较要 1.5s，
    // 这里只要一个「第一个不一致的位置」，用循环几十毫秒就够。
    let mismatch = -1
    for (let i = 0; i < bytes.length; i++) {
      if (round[i] !== bytes[i]) {
        mismatch = i
        break
      }
    }
    expect(mismatch).toBe(-1)
  })

  it('保留非 UTF-8 原始字节（终端输出可能是任意字节）', () => {
    const raw = new Uint8Array([0xff, 0xfe, 0x00, 0x1b, 0x5b, 0x33, 0x31, 0x6d])
    expect(Array.from(base64ToBytes(bytesToBase64(raw)))).toEqual(Array.from(raw))
  })
})

describe('textToBytes', () => {
  it('中文按 UTF-8 编码成多字节', () => {
    expect(Array.from(textToBytes('中'))).toEqual([0xe4, 0xb8, 0xad])
  })

  it('控制字符原样保留（Ctrl+C 等按键）', () => {
    expect(Array.from(textToBytes('\u0003'))).toEqual([3])
  })
})
