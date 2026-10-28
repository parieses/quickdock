import { describe, it, expect } from 'vitest'
import { isExternalURL, shouldHandOverLink } from './externalLinks'

// 这组断言锁的是「哪些链接会被交给系统浏览器」。
// 判错了两种方向都难受：
//   - 漏判 → 点在当前窗口导航，主界面被顶掉、没法返回（本模块就是为了修这个）；
//   - 误判 → 应用内部的 hash 路由（#/settings 之类）被丢给浏览器，点了没反应。
describe('isExternalURL', () => {
  it('接受 http / https 绝对地址', () => {
    expect(isExternalURL('https://example.com/a?b=1#c')).toBe(true)
    expect(isExternalURL('http://127.0.0.1:8080/')).toBe(true)
  })

  it('协议大小写不敏感，首尾空白容忍', () => {
    expect(isExternalURL('HTTPS://Example.com')).toBe(true)
    expect(isExternalURL('  https://example.com  ')).toBe(true)
  })

  it('拒绝应用内 hash 路由与相对路径', () => {
    expect(isExternalURL('#/settings')).toBe(false)
    expect(isExternalURL('/plugins')).toBe(false)
    expect(isExternalURL('docs/readme.md')).toBe(false)
  })

  it('拒绝非 http(s) 协议', () => {
    expect(isExternalURL('mailto:a@b.com')).toBe(false)
    expect(isExternalURL('javascript:alert(1)')).toBe(false)
    expect(isExternalURL('file:///C:/tmp/a.txt')).toBe(false)
    expect(isExternalURL('wails://wails/')).toBe(false)
  })

  it('空值不抛错', () => {
    expect(isExternalURL('')).toBe(false)
    expect(isExternalURL('   ')).toBe(false)
    expect(isExternalURL(undefined as unknown as string)).toBe(false)
    expect(isExternalURL(null as unknown as string)).toBe(false)
  })
})

describe('shouldHandOverLink', () => {
  it('外站链接交给浏览器', () => {
    expect(shouldHandOverLink('https://github.com/parieses/quickdock', 'http://localhost:5173')).toBe(true)
    expect(shouldHandOverLink('http://localhost:9001/', 'wails://wails')).toBe(true)
  })

  it('同 origin 不拦（开发模式下 vite 自身资源）', () => {
    expect(shouldHandOverLink('http://localhost:5173/@vite/client', 'http://localhost:5173')).toBe(false)
    expect(shouldHandOverLink('https://example.com/x', 'https://example.com')).toBe(false)
  })

  it('hash 路由 / 相对路径一律不拦', () => {
    const origin = 'http://localhost:5173'
    expect(shouldHandOverLink('#/clipboard', origin)).toBe(false)
    expect(shouldHandOverLink('/plugins', origin)).toBe(false)
    expect(shouldHandOverLink('', origin)).toBe(false)
  })
})
