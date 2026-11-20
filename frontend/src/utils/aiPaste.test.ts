// 已发送消息的粘贴块解析：保证 chip 化不改原文、不吞掉用户自己打的字。
import { describe, expect, it } from 'vitest'
import { compactPastedBlocks, parsePastedSegments } from './aiPaste'

describe('parsePastedSegments', () => {
  it('没有粘贴块时原样返回', () => {
    expect(parsePastedSegments('一句普通问题\n第二行')).toEqual([{ text: '一句普通问题\n第二行' }])
  })

  it('拆出前后正文与粘贴块，还原包壳换行', () => {
    const content = '帮我看下\n<pasted_text lines="3">\nline1\nline2\nline3\n</pasted_text>\n谢谢'
    const segs = parsePastedSegments(content)
    expect(segs.map((s) => (s.full === undefined ? 'text' : 'paste'))).toEqual(['text', 'paste', 'text'])
    expect(segs[0].text).toBe('帮我看下\n')
    expect(segs[1]).toEqual({ full: 'line1\nline2\nline3', lines: '3' })
    expect(segs[2].text).toBe('\n谢谢')
  })

  it('多个粘贴块各自独立，lines 缺省不报错', () => {
    const segs = parsePastedSegments('<pasted_text>\naaa\n</pasted_text><pasted_text lines="2">\nb\nc\n</pasted_text>')
    expect(segs).toEqual([
      { full: 'aaa', lines: '' },
      { full: 'b\nc', lines: '2' },
    ])
  })
})

describe('compactPastedBlocks', () => {
  it('轨迹摘要里把整段粘贴压成标记，保留用户自己打的字', () => {
    const raw = '请分析\n<pasted_text lines="398">\n// Package kb\n// 很长很长\n</pasted_text>\n谢谢'
    expect(compactPastedBlocks(raw, (n) => `[粘贴 · ${n} 行]`)).toBe('请分析\n[粘贴 · 398 行]\n谢谢')
  })
})
