// 「这条插件命令该带什么文本、走哪条通道」的判定表。
//
// 这段逻辑原先散在 executeSelected 的 137 行里，改一行就得通读整个执行器；
// 抽成纯函数后，最要紧的三条约束可以直接锁死：
//   1) 敲 keyword（`json` 回车）**不能**静默复用上次输入 —— 用户没要求重跑；
//   2) 斜杠前缀（`/sql`）与最近使用列表才允许回退上次输入；
//   3) 前端插件的内容通道与 acceptsInput 解耦（json-toolbox 这类「仅界面」入口能收文本）。

import { describe, it, expect } from 'vitest'
import { resolvePluginInput } from './palettePluginInput'

describe('resolvePluginInput', () => {
  it('声明 acceptsInput + 有内联输入：原文交给后端执行', () => {
    const p = resolvePluginInput({
      acceptsInput: true, matchType: 'match pattern', inlineInput: '{"a":1}',
    })
    expect(p.action).toBe('execute')
    expect(p.text).toBe('{"a":1}')
  })

  it('声明 acceptsInput + 有前端：走内联插件页并带上文本', () => {
    const p = resolvePluginInput({
      acceptsInput: true, pluginHasFrontend: true, matchType: 'match pattern', inlineInput: '8080',
    })
    expect(p.action).toBe('open-inline')
    expect(p.text).toBe('8080')
  })

  it('从 label 里反解出的输入优先于「上次输入」', () => {
    const p = resolvePluginInput({
      acceptsInput: true, matchType: 'keyword', labelInput: '80', lastInput: '3000',
    })
    expect(p.text).toBe('80')
  })

  it('敲 keyword 命中：不回退上次输入（否则会带着几个月前的参数静默执行）', () => {
    const p = resolvePluginInput({
      acceptsInput: true, matchType: 'keyword', lastInput: '3000',
    })
    expect(p.text).toBeUndefined()
    expect(p.action).toBe('need-input')
  })

  it('斜杠前缀命中：允许回退上次输入（显式调用，参数留空＝沿用上次）', () => {
    const p = resolvePluginInput({
      acceptsInput: true, matchType: 'slash', lastInput: '3000',
    })
    expect(p.action).toBe('execute')
    expect(p.text).toBe('3000')
  })

  it('最近使用列表：允许回退上次输入（点历史记录本来就是重跑）', () => {
    const p = resolvePluginInput({
      acceptsInput: true, isRecentMode: true, lastInput: '3000',
    })
    expect(p.text).toBe('3000')
  })

  it('前端插件的「仅界面」入口：没声明 acceptsInput 也能收到命中原文', () => {
    const p = resolvePluginInput({
      pluginHasFrontend: true, hitText: '{"a":1}', matchType: 'content',
    })
    expect(p.action).toBe('open-inline')
    expect(p.text).toBe('{"a":1}')
  })

  it('无前端插件不吃 hitText：凭空多一个参数会改变后端命令语义', () => {
    const p = resolvePluginInput({ hitText: '{"a":1}', matchType: 'content' })
    expect(p.action).toBe('execute')
    expect(p.text).toBeUndefined()
  })

  it('acceptsInput + 无输入 + 有前端：开界面让用户补参数', () => {
    const p = resolvePluginInput({ acceptsInput: true, pluginHasFrontend: true, matchType: 'keyword' })
    expect(p.action).toBe('open-inline')
    expect(p.text).toBeUndefined()
  })

  it('acceptsInput + 无输入 + 无前端：提示需要参数，不空跑', () => {
    const p = resolvePluginInput({ acceptsInput: true, matchType: 'keyword' })
    expect(p.action).toBe('need-input')
  })

  it('hitText 只在没有显式输入时兜底（不覆盖用户真正给的参数）', () => {
    const p = resolvePluginInput({
      acceptsInput: true, pluginHasFrontend: true, inlineInput: '80', hitText: 'other',
    })
    expect(p.text).toBe('80')
  })

  it('完全无 payload 的普通命令：空参执行', () => {
    const p = resolvePluginInput({})
    expect(p.action).toBe('execute')
    expect(p.text).toBeUndefined()
  })
})
