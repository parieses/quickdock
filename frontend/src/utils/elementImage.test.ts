import { describe, it, expect } from 'vitest'
import { xmlAttr } from './elementImage'

/**
 * 这组用例守的是一个真实故障：KaTeX 公式转图时 SVG 一直 decode failed。
 * 根因是 `getComputedStyle(el).fontFamily` 里带双引号（`"Segoe UI"`），
 * 原样拼进 `style="…"` 后属性被截断 → 非法 XML。
 */
describe('xmlAttr', () => {
  it('转义双引号（字体族名里的引号是真正的坑）', () => {
    const ff = 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif'
    const out = xmlAttr(ff)
    expect(out).not.toContain('"')
    expect(out).toContain('&quot;Segoe UI&quot;')
  })

  it('转义 & < >（& 必须最先处理，否则会二次转义）', () => {
    expect(xmlAttr('a & b')).toBe('a &amp; b')
    expect(xmlAttr('<b>')).toBe('&lt;b&gt;')
    expect(xmlAttr('&amp;')).toBe('&amp;amp;')
  })

  it('普通颜色/字号原样返回', () => {
    expect(xmlAttr('rgb(15, 17, 22)')).toBe('rgb(15, 17, 22)')
    expect(xmlAttr('13px')).toBe('13px')
  })

  it('null / undefined 不产出字符串 "undefined"', () => {
    expect(xmlAttr(undefined as unknown as string)).toBe('')
    expect(xmlAttr(null as unknown as string)).toBe('')
  })
})
