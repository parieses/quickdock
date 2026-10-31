/**
 * 本轮新增的三处修复的行为测试：
 *
 * 1. 历史消息按 id 记忆化 —— 长对话下不再出现「稳态帧全量重解析」
 *    （旧实现用整段文本 LRU，上限 200 段；消息数超过上限后，
 *      每帧遍历历史消息都会把全部内容重新 md.parse 一遍）
 * 2. 消息落地时复用流式期已渲染的块 —— 避免落地瞬间整条重新解析
 * 3. marked 的 breaks:true 产出 <br>，CSS 不得把它隐藏
 *    （否则 AI 回复里的多行要点会被挤成一行）
 */
import { describe, test, expect } from 'vitest'
import { Marked } from 'marked'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { splitMarkdownBlocks } from './markdownBlocks'

const md = new Marked({ breaks: true, gfm: true })

/** 复刻 AIPage 的历史消息块缓存：按消息 id 记忆化 */
class MessageBlockCache {
  private map = new Map<string, { src: string; html: string[] }>()
  parseCount = 0

  render(m: { id: string; content: string }): string[] {
    const hit = this.map.get(m.id)
    if (hit && hit.src === m.content) return hit.html
    this.parseCount++
    const html = splitMarkdownBlocks(m.content ?? '').map(
      (raw) => md.parse(raw, { async: false }) as string
    )
    this.map.set(m.id, { src: m.content ?? '', html })
    return html
  }

  prime(id: string, content: string, html: string[]) {
    this.map.set(id, { src: content, html })
  }

  prune(aliveIds: string[]) {
    const alive = new Set(aliveIds)
    for (const id of Array.from(this.map.keys())) {
      if (!alive.has(id)) this.map.delete(id)
    }
  }

  get size() { return this.map.size }
}

interface Msg { id: string; content: string }

describe('历史消息块缓存（按 id 记忆化）', () => {
  test('长对话下稳态帧不再重新解析（核心回归点）', () => {
    const cache = new MessageBlockCache()
    // 400 条消息 × 3 段：旧 LRU 实现（上限 200）在此规模下每帧要解析 1200 次
    const msgs: Msg[] = Array.from({ length: 400 }, (_, i) => ({
      id: `m${i}`,
      content: Array.from({ length: 3 }, (_, j) => `消息${i}第${j}段`).join('\n\n'),
    }))

    // 首帧：全量解析一次
    msgs.forEach((m) => cache.render(m))
    const firstParse = cache.parseCount
    expect(firstParse).toBe(400)

    // 稳态帧：模板每帧都会遍历历史消息，必须零解析
    for (let frame = 0; frame < 5; frame++) {
      msgs.forEach((m) => cache.render(m))
    }
    expect(cache.parseCount, '稳态帧出现了重复解析').toBe(firstParse)
  })

  test('消息内容变化时缓存失效并重新解析', () => {
    const cache = new MessageBlockCache()
    const m: Msg = { id: 'm1', content: '第一版' }
    const a = cache.render(m)
    expect(a.join('')).toContain('第一版')
    m.content = '第二版'
    const b = cache.render(m)
    expect(b.join('')).toContain('第二版')
    expect(cache.parseCount).toBe(2)
  })

  test('内容不变时返回同一数组引用（不触发 v-for diff 重建）', () => {
    const cache = new MessageBlockCache()
    const m: Msg = { id: 'm1', content: '内容' }
    const a = cache.render(m)
    const b = cache.render(m)
    expect(b).toBe(a)
  })

  test('prune 丢弃其他会话的条目，避免内存无限增长', () => {
    const cache = new MessageBlockCache()
    cache.render({ id: 'a1', content: 'A' })
    cache.render({ id: 'b1', content: 'B' })
    expect(cache.size).toBe(2)
    cache.prune(['a1'])
    expect(cache.size).toBe(1)
    // 被保留的条目仍命中缓存
    const before = cache.parseCount
    cache.render({ id: 'a1', content: 'A' })
    expect(cache.parseCount).toBe(before)
  })
})

describe('消息落地时复用流式块', () => {
  test('prime 后的消息直接命中缓存，零额外解析', () => {
    const cache = new MessageBlockCache()
    const content = '# 标题\n\n正文一段。\n\n正文二段。'
    const streamed = splitMarkdownBlocks(content).map(
      (raw) => md.parse(raw, { async: false }) as string
    )
    cache.prime('ast-1', content, streamed)
    const before = cache.parseCount
    const landed = cache.render({ id: 'ast-1', content })
    expect(cache.parseCount, '落地时发生了重复解析').toBe(before)
    expect(landed).toEqual(streamed)
  })

  test('prime 的内容与实际内容不一致时退回重新解析', () => {
    const cache = new MessageBlockCache()
    cache.prime('ast-1', '旧内容', ['<p>旧内容</p>'])
    const got = cache.render({ id: 'ast-1', content: '新内容' })
    expect(got.join('')).toContain('新内容')
    expect(cache.parseCount).toBe(1)
  })
})

describe('换行渲染（breaks:true 与 <br>）', () => {
  test('marked 在 breaks:true 下把段内单换行渲染为 <br>', () => {
    const html = md.parse('第一行\n第二行', { async: false }) as string
    expect(html).toContain('<br>')
  })

  test('列表项内的软换行同样产出 <br>', () => {
    const html = md.parse('- 项目一：说明\n  补充说明', { async: false }) as string
    expect(html).toContain('<br>')
  })

  test('CSS 不得隐藏 <br>（否则多行要点被挤成一行）', () => {
    // 这里断言的是「组件样式表里不存在隐藏 br 的规则」。
    // 读取源码做静态检查：CSS 规则文本中出现 br 的 display:none 即为回归。
    //
    // 样式跟着模板走：消息区已拆成 AiMessageList.vue，.ai-md 的规则都在那边。
    // 两个文件一起读——将来谁把 markdown 样式挪回去，这条断言也不至于失灵。
    const here = dirname(fileURLToPath(import.meta.url))
    const src = [
      readFileSync(resolve(here, '../components/AIPage.vue'), 'utf8'),
      readFileSync(resolve(here, '../components/AiMessageList.vue'), 'utf8'),
    ].join('\n')
    const rules = src.match(/\.ai-md\s+:deep\(br\)\s*\{[^}]*\}/g) ?? []
    expect(rules.length, '未找到 .ai-md br 规则').toBeGreaterThan(0)
    for (const r of rules) {
      expect(r, `br 被隐藏了：${r}`).not.toMatch(/display\s*:\s*none/)
    }
  })
})
