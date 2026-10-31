import { describe, expect, it } from 'vitest'
import {
  artifactsFromCalls, artifactsOf, countToolRounds, rulesOf, rulesTitle, runIDOf,
  serializeTools, shortPath, toolList, toolTarget,
} from './aiTools'
import type { AIMessage, AIToolCall } from '../types/ai'

function call(p: Partial<AIToolCall>): AIToolCall {
  return { id: 'c1', name: 'file_read', ...p }
}

describe('toolList', () => {
  it('解析 tool_calls JSON 数组', () => {
    const m = { tool_calls: JSON.stringify([{ id: 'a', name: 'file_read' }]) } as AIMessage
    expect(toolList(m)[0].name).toBe('file_read')
  })

  it('坏 JSON / 非数组 / 空串都返回空数组，不抛异常（历史脏数据不该白屏）', () => {
    expect(toolList({ tool_calls: '{oops' } as AIMessage)).toEqual([])
    expect(toolList({ tool_calls: '{"a":1}' } as AIMessage)).toEqual([])
    expect(toolList({ tool_calls: '' } as AIMessage)).toEqual([])
    expect(toolList({} as AIMessage)).toEqual([])
  })
})

describe('runIDOf', () => {
  it('取第一条带 run_id 的调用', () => {
    const m = {
      role: 'assistant',
      tool_calls: JSON.stringify([{ id: 'a', name: 'x' }, { id: 'b', name: 'spawn_agent', run_id: 'r1' }]),
    } as AIMessage
    expect(runIDOf(m)).toBe('r1')
  })

  it('非 assistant 或没有 run_id 时为空串', () => {
    expect(runIDOf({ role: 'user', tool_calls: '[{"id":"a","name":"x","run_id":"r"}]' } as AIMessage)).toBe('')
    expect(runIDOf({ role: 'assistant' } as AIMessage)).toBe('')
  })
})

describe('countToolRounds', () => {
  it('数轮次而不是条数：同一 round 的并发调用算一轮', () => {
    const list = [call({ round: 1 }), call({ round: 1 }), call({ round: 2 })]
    expect(countToolRounds(list)).toBe(2)
  })

  it('忽略 round 缺省（0/undefined）的记录', () => {
    expect(countToolRounds([call({}), call({ round: 1 })])).toBe(1)
  })
})

describe('serializeTools', () => {
  it('丢掉 running，保留落库需要的字段', () => {
    const json = serializeTools([call({ running: true, result: 'ok', round: 1 })])
    const out = JSON.parse(json)[0]
    expect(out.running).toBeUndefined()
    expect(out.result).toBe('ok')
    expect(out.round).toBe(1)
  })

  it('denied / diff / added / run_id 等只在有值时出现', () => {
    const out = JSON.parse(serializeTools([call({ denied: true, diff: '@@', added: 2, removed: 1, run_id: 'r', node_id: 'n' })]))[0]
    expect(out.denied).toBe(true)
    expect(out.diff).toBe('@@')
    expect(out.added).toBe(2)
    expect(out.run_id).toBe('r')
    expect(out.node_id).toBe('n')
    const bare = JSON.parse(serializeTools([call({})]))[0]
    expect('denied' in bare).toBe(false)
    expect('diff' in bare).toBe(false)
  })
})

describe('artifactsFromCalls', () => {
  it('只认成功的 file_write / file_edit，同路径去重', () => {
    const list = [
      call({ name: 'file_write', path: 'C:\\a\\b.txt' }),
      call({ name: 'file_edit', path: 'C:\\a\\b.txt', added: 1 }),
      call({ name: 'file_read', path: 'C:\\a\\c.txt' }),
      call({ name: 'file_write', path: 'C:\\a\\d.txt', denied: true }),
      call({ name: 'file_write', path: 'C:\\a\\e.txt', error: 'boom' }),
      call({ name: 'file_write', path: 'C:\\a\\f.txt', dedup: true }),
    ]
    const arts = artifactsFromCalls(list)
    expect(arts.map((a) => a.name)).toEqual(['b.txt'])
    // 同一路径后写覆盖前写：预览读的是磁盘现状，op 取最后一次动作
    expect(arts[0].op).toBe('edit')
  })

  it('artifactsOf 只对 assistant 生效', () => {
    const tc = '[{"id":"a","name":"file_write","path":"C:\\\\x\\\\y.md"}]'
    expect(artifactsOf({ role: 'user', tool_calls: tc } as AIMessage)).toEqual([])
    expect(artifactsOf({ role: 'assistant', tool_calls: tc } as AIMessage)[0].name).toBe('y.md')
  })
})

describe('shortPath', () => {
  it('保留末尾两段，前面用省略号', () => {
    expect(shortPath('E:\\pp\\quickdock\\services\\ai\\tools\\code.go')).toBe('…\\tools\\code.go')
    expect(shortPath('/a/b/c.md')).toBe('…/b/c.md')
  })

  it('两段以内原样返回', () => {
    expect(shortPath('a/b')).toBe('a/b')
    expect(shortPath('a')).toBe('a')
  })
})

describe('toolTarget', () => {
  it('按优先级挑 path/url/command 等已知键', () => {
    expect(toolTarget(call({ args: JSON.stringify({ path: '/x/y/z.go', command: 'ls' }) }))).toBe('…/y/z.go')
    expect(toolTarget(call({ args: JSON.stringify({ command: 'npm test' }) }))).toBe('npm test')
  })

  it('已知键的数字值也能展示', () => {
    expect(toolTarget(call({ args: '{"id":8080}' }))).toBe('8080')
  })

  it('只认字符串兜底：全是非白名单数字键时不出摘要', () => {
    expect(toolTarget(call({ args: '{"port":8080}' }))).toBe('')
  })

  it('没命中已知键时退回第一个字符串值', () => {
    expect(toolTarget(call({ args: '{"whatever":"hello"}' }))).toBe('hello')
  })

  it('参数不是合法 JSON 时截断原文（仍能看出模型想动谁）', () => {
    const raw = 'x'.repeat(200)
    expect(toolTarget(call({ args: raw })).length).toBe(81) // 80 + 省略号
    expect(toolTarget(call({}))).toBe('')
  })
})

describe('rulesOf / rulesTitle', () => {
  it('rules 非数组时返回空', () => {
    expect(rulesOf({})).toEqual([])
    expect(rulesOf({ rules: 'x' })).toEqual([])
  })

  it('标题带字节数与截断标记', () => {
    const title = rulesTitle([{ name: 'AGENTS.md', bytes: 1024 }, { name: 'big.md', bytes: 9000, truncated: true }])
    expect(title.split('\n')[0]).toBe('AGENTS.md · 1024 B')
    expect(title.split('\n')[1]).toContain('已截断')
  })
})
