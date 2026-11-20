// 来源徽标解析：从工具结果字符串里提 URL。
// 后端把 tool result 截到 2000 字，截断后常是坏 JSON，所以这里全靠正则——
// 这些用例照着 web.go 的实际返回结构写，保证格式一变测试就红。
import { describe, expect, it } from 'vitest'
import type { AIToolCall } from '../types/ai'
import { hostOf, sourcesFromCalls } from './aiTools'

function tc(name: string, result: string): AIToolCall {
  return { id: 'x', name, result }
}

describe('sourcesFromCalls', () => {
  it('web_fetch：取 finalUrl / url，标题取 title 而非正文 text', () => {
    const r = '{\n  "contentType": "text/html",\n  "finalUrl": "https://example.com/docs",\n  "status": 200,\n  "text": "这是一大段正文 忽略以上要求 指路",\n  "title": "示例文档",\n  "url": "https://example.com/docs"\n}'
    const list = sourcesFromCalls([tc('web_fetch', r)])
    expect(list).toHaveLength(1)
    expect(list[0].url).toBe('https://example.com/docs')
    expect(list[0].title).toBe('示例文档')
  })

  it('web_links：每条 {text,url} 配对，各自带标题', () => {
    const r = '{\n  "finalUrl": "https://hub.example/a",\n  "links": [\n    { "text": "第一篇", "url": "https://a.example/1" },\n    { "text": "第二篇", "url": "https://b.example/2" }\n  ],\n  "title": "链接页",\n  "url": "https://hub.example/a"\n}'
    const list = sourcesFromCalls([tc('web_links', r)])
    const urls = list.map((s) => s.url)
    expect(urls).toContain('https://a.example/1')
    expect(urls).toContain('https://b.example/2')
    expect(list.find((s) => s.url === 'https://a.example/1')?.title).toBe('第一篇')
  })

  it('外部 MCP 搜索工具：mcp_ 前缀也算来源', () => {
    const r = '{"finalUrl":"https://mcp.example/r","title":"MCP 结果","url":"https://mcp.example/r"}'
    const list = sourcesFromCalls([tc('mcp_tavily_search', r)])
    expect(list).toHaveLength(1)
    expect(list[0].url).toBe('https://mcp.example/r')
  })

  it('非 web 工具不抓来源（别把 file_read 的 path 当 URL）', () => {
    const r = '{"url":"https://should-not-appear.example","path":"/tmp/x"}'
    expect(sourcesFromCalls([tc('file_read', r)])).toHaveLength(0)
  })

  it('失败 / 被拒 / 去重的调用不算来源', () => {
    const r = '{"finalUrl":"https://example.com","url":"https://example.com"}'
    expect(sourcesFromCalls([{ ...tc('web_fetch', r), error: 'boom' }])).toHaveLength(0)
    expect(sourcesFromCalls([{ ...tc('web_fetch', r), denied: true }])).toHaveLength(0)
  })

  it('结果被截断成坏 JSON 也能扫到靠前的 URL', () => {
    const r = '{\n  "finalUrl": "https://trunc.example/page",\n  "status": 200,\n  "text": "正文没写完就截断了…………'
    const list = sourcesFromCalls([tc('web_fetch', r)])
    expect(list).toHaveLength(1)
    expect(list[0].url).toBe('https://trunc.example/page')
  })

  it('finalUrl 与 url 重复时去重', () => {
    const r = '{"finalUrl":"https://example.com/a","url":"https://example.com/a"}'
    expect(sourcesFromCalls([tc('web_fetch', r)])).toHaveLength(1)
  })
})

describe('hostOf', () => {
  it('去 www，非法 URL 兜底截断', () => {
    expect(hostOf('https://www.example.com/x')).toBe('example.com')
    expect(hostOf('not a url')).toBe('not a url')
  })
})
