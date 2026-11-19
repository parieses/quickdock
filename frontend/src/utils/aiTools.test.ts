import { describe, expect, it } from 'vitest'
import {
  artifactsFromCalls, artifactsOf, countToolRounds, rulesOf, rulesTitle, runIDOf,
  serializeTools, shortPath, spawnArgs, spawnPartial, spawnResultText, spawnTaskBrief, toolList,
  toolOutput, toolTarget,
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

  it('output 只在有值时出现（刷新后卡片仍显示命令输出，而不是原始 JSON）', () => {
    const out = JSON.parse(serializeTools([call({ name: 'run_command', output: 'ok\nline2' })]))[0]
    expect(out.output).toBe('ok\nline2')
    expect('output' in JSON.parse(serializeTools([call({})]))[0]).toBe(false)
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
    // 带上调用 id 与完成标记：侧栏据此判断已打开的文件该不该重读
    expect(arts[0].callId).toBe('c1')
    expect(arts[0].done).toBe(true)
  })

  it('流式期还没跑完的写，标记 done=false（侧栏先列出来、但不读文件）', () => {
    const arts = artifactsFromCalls([call({ id: 'live-1', name: 'file_write', path: 'E:/p/x.md', running: true })])
    expect(arts[0].done).toBe(false)
    expect(arts[0].callId).toBe('live-1')
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

describe('spawn_agent 摘要', () => {
  // 模型写 task 时经常把 Windows 路径原样塞进 JSON 字符串，`\p` 是非法转义，
  // 整段 JSON.parse 必然失败——这正是派活行长期显示一坨参数原文的原因。
  const badJSON = '{"role":"explore","task":"项目：D:\\project\\quickdock-v3 看看支付流程"}'

  it('任务里含非法 JSON 转义时仍能抠出 role/task', () => {
    expect(() => JSON.parse(badJSON)).toThrow()
    const s = spawnArgs(badJSON)
    expect(s?.role).toBe('explore')
    expect(s?.task).toContain('quickdock-v3')
  })

  it('摘要行是「角色 · 任务首行」，不再是参数原文', () => {
    expect(toolTarget(call({ name: 'spawn_agent', args: badJSON }))).toBe(
      'explore · 项目：D:\\project\\quickdock-v3 看看支付流程',
    )
  })

  it('缺 role 时兜底 agent；参数完全解析不出时返回 null', () => {
    expect(spawnArgs('{"task":"只读排查"}')?.role).toBe('agent')
    expect(spawnArgs('{oops')).toBeNull()
    expect(spawnArgs('')).toBeNull()
  })

  it('任务摘要取首个非空行并压掉多余空白', () => {
    expect(spawnTaskBrief('\n\n  第一行   有空格 \n第二行')).toBe('第一行 有空格')
    expect(spawnTaskBrief('a'.repeat(80)).length).toBe(49) // 48 + 省略号
  })

  it('子 agent 结论只取 JSON 里的 result 字段', () => {
    const tc = call({
      name: 'spawn_agent',
      result: '{"run_id":"r1","node_id":"n1","role":"explore","tokens":12,"result":"结论：没有支付相关代码"}',
    })
    expect(spawnResultText(tc)).toBe('结论：没有支付相关代码')
  })

  it('结果为空时返回空串（调用方据此显示「还在跑」提示，而不是空白面板）', () => {
    expect(spawnResultText(call({ name: 'spawn_agent' }))).toBe('')
  })

  // 残本标记：子任务超时 / 被停时结论只跑了一半。结果会被截断成半截 JSON，
  // 所以判定只能扫文本——partial 字段被后端刻意排在 result 之前。
  it('partial=true 的结果要能认出来，且不影响取结论', () => {
    const tc = call({
      name: 'spawn_agent',
      result: '{"run_id":"r1","node_id":"n1","role":"explore","tokens":9,"partial":true,"note":"子任务未跑完","result":"结论：只查到一半"}',
    })
    expect(spawnPartial(tc)).toBe(true)
    expect(spawnResultText(tc)).toBe('结论：只查到一半')
    expect(spawnPartial(call({ name: 'spawn_agent', result: '{"result":"完整结论"}' }))).toBe(false)
    expect(spawnPartial(call({ name: 'spawn_agent' }))).toBe(false)
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

describe('toolOutput', () => {
  it('优先用人看的 output，缺失时退回 result（老数据 / 其它工具）', () => {
    expect(toolOutput(call({ name: 'run_command', output: 'line1\nline2', result: '{"stdout":"ok"}' })))
      .toBe('line1\nline2')
    expect(toolOutput(call({ name: 'file_read', result: '{"path":"a"}' }))).toBe('{"path":"a"}')
    expect(toolOutput(call({}))).toBe('')
  })

  it('命令还在跑时用实时输出：已确认的行 + 正在刷新的那一行', () => {
    // add 追加、tail 替换 —— 后者是进度条用 \r 重写的同一行
    expect(toolOutput(call({ name: 'run_command', live: 'Collecting foo\n', liveTail: ' 45%' })))
      .toBe('Collecting foo\n 45%')
    // 还没输出任何完整行时，tail 就是全部内容（不能因为 live 为空就显示成空卡片）
    expect(toolOutput(call({ name: 'run_command', liveTail: ' 12%' }))).toBe(' 12%')
  })

  it('结果到达后以 output 为准，实时缓冲不再参与', () => {
    expect(toolOutput(call({
      name: 'run_command',
      live: 'old\n',
      liveTail: 'old tail',
      output: '最终输出',
    }))).toBe('最终输出')
  })
})
