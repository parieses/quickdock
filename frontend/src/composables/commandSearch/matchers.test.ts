// 命令面板检索的 matcher / 编排行为测试。
//
// 这批用例锁的是「拆分前后必须一致」的东西：分组归属、seen 去重、
// calculator 的提前终止、剪贴板置顶、最佳匹配跨组去重、分组优先级。

import { describe, it, expect } from 'vitest'
import { MATCHERS, matchClipboard, type MatchCtx } from './matchers'
import { dropEmptyGroups, injectBestMatch, sortGroups } from './ranking'
import type { LcIndex, ResultGroup } from './types'

function makeCtx(over: Partial<MatchCtx> = {}): MatchCtx {
  const lc: LcIndex = { items: [], notes: [], apps: [], sys: [] }
  return {
    q: '', qLC: '', seen: new Set<string>(), t: (k: string) => k, locale: 'zh-CN',
    lc,
    backendItems: [], backendNotes: [], backendTodos: [], fileResults: [], clipboardUrlSource: '',
    navItems: [],
    pluginCmdIndex: [], envRuntimes: [], envStatuses: {}, envMgmt: {}, pluginIcons: {},
    frecencyScore: () => 0,
    pinyinMatch: () => false,
    itemIcon: () => null,
    appIcon: () => null,
    calcPluginScore: () => ({ score: 0, matchType: '' }),
    ...over,
  }
}

// 只跑到 matcher 阶段（含剪贴板置顶与分组排序），不做最佳匹配置顶：
// matcher 用例关心的是「归到哪个分组」，而 ≥70 分的结果会被 injectBestMatch 提到
// best 分组并从原分组摘走 —— 掺进来看不出归属。
function runMatchOnly(ctx: MatchCtx): ResultGroup[] {
  const groups: ResultGroup[] = []
  for (const match of MATCHERS) {
    const out = match(ctx)
    if (!out) continue
    if (out.groups.length) groups.push(...out.groups)
    if (out.stop) return groups
  }
  const clip = matchClipboard(ctx)
  if (clip.length) groups.unshift(...clip)
  sortGroups(groups)
  return groups
}

// 完整编排（与 useCommandSearch 的 groupedResults 等价，不含 debounce / 数据拉取）
function runFull(ctx: MatchCtx): ResultGroup[] {
  const groups = runMatchOnly(ctx) as ResultGroup[]
  injectBestMatch(groups, ctx.t)
  dropEmptyGroups(groups)
  sortGroups(groups)
  return groups
}

const groupOf = (groups: ResultGroup[], type: string) => groups.find(g => g.type === type)

describe('matchers', () => {
  it('条目：前缀命中给 75 分，并归到 item 分组', () => {
    const item = { id: 'i1', name: 'Notepad', value: 'C:\\a.txt', type: '文件' } as any
    const ctx = makeCtx({
      q: 'note', qLC: 'note',
      lc: { items: [{ item, nameLC: 'notepad', valueLC: 'c:\\a.txt' }], notes: [], apps: [], sys: [] },
    })
    const g = groupOf(runMatchOnly(ctx), 'item')!
    expect(g.results).toHaveLength(1)
    expect(g.results[0].score).toBe(75)
  })

  it('条目：后端兜底不与本地池重复（共享 seen）', () => {
    const item = { id: 'i1', name: 'Note', value: '', type: '文件' } as any
    const ctx = makeCtx({
      q: 'note', qLC: 'note',
      lc: { items: [{ item, nameLC: 'note', valueLC: '' }], notes: [], apps: [], sys: [] },
      backendItems: [item],
    })
    const g = groupOf(runMatchOnly(ctx), 'item')!
    expect(g.results).toHaveLength(1)
  })

  it('条目：value 含 {query} 时归为 quicklink', () => {
    const item = { id: 'q1', name: '搜索', value: 'https://x.com?q={query}', type: '链接' } as any
    const ctx = makeCtx({
      q: '搜索', qLC: '搜索',
      lc: { items: [{ item, nameLC: '搜索', valueLC: 'https://x.com?q={query}' }], notes: [], apps: [], sys: [] },
    })
    const g = groupOf(runMatchOnly(ctx), 'item')!
    expect(g.results[0].type).toBe('quicklink')
  })

  it('笔记：内容命中得 55 分（名称未命中）', () => {
    const note = { id: 'n1', name: '标题', content: '正文里提到 note', isFolder: false, parentId: '', keyword: '' }
    const ctx = makeCtx({
      q: 'note', qLC: 'note',
      lc: { items: [], notes: [{ note, nameLC: '标题', contentLC: '正文里提到 note' }], apps: [], sys: [] },
    })
    const g = groupOf(runMatchOnly(ctx), 'note')!
    expect(g.results).toHaveLength(1)
    expect(g.results[0].score).toBe(55)
  })

  it('待办：仅有后端来源，title 前缀命中 75 分', () => {
    const ctx = makeCtx({
      q: 'note', qLC: 'note',
      backendTodos: [{ id: 't1', title: 'note 整理', status: 'todo', priority: 'mid', dueDate: '', tags: '[]' }],
    })
    const g = groupOf(runMatchOnly(ctx), 'todo')!
    expect(g.results).toHaveLength(1)
    expect(g.results[0].score).toBe(75)
  })

  it('应用：别名小写命中即可（不依赖拼音）', () => {
    const app = { name: 'Notepad', path: 'C:\\np.exe', category: '其他' }
    const ctx = makeCtx({
      q: '记事本', qLC: '记事本',
      lc: { items: [], notes: [], apps: [{ app, nameLC: 'notepad', aliases: [{ raw: '记事本', lc: '记事本' }] }], sys: [] },
    })
    const g = groupOf(runMatchOnly(ctx), 'app')!
    expect(g.results[0].score).toBe(60)
    // 分类是「其他」时 desc 回退到路径
    expect(g.results[0].desc).toBe('C:\\np.exe')
  })

  it('系统命令：命中 keywords 也给分', () => {
    const cmd = { id: 's1', label: '打开设置', desc: '', keywords: ['settings'], icon: null, action: async () => {} }
    const ctx = makeCtx({
      q: 'settings', qLC: 'settings',
      lc: { items: [], notes: [], apps: [], sys: [{ cmd, labelLC: '打开设置' }] },
    })
    const g = groupOf(runMatchOnly(ctx), 'system')!
    expect(g.results).toHaveLength(1)
  })

  it('插件：calcPluginScore 返回 0 时不入列', () => {
    const idx = { plugin: { id: 'p1', name: 'P' }, cmd: { id: 'c1', title: 'T' } } as any
    const hit = makeCtx({
      q: 'p', qLC: 'p', pluginCmdIndex: [idx],
      calcPluginScore: () => ({ score: 90, matchType: 'name' }),
    })
    expect(groupOf(runMatchOnly(hit), 'plugin')!.results).toHaveLength(1)

    const miss = makeCtx({
      q: 'p', qLC: 'p', pluginCmdIndex: [idx],
      calcPluginScore: () => ({ score: 0, matchType: '' }),
    })
    expect(groupOf(runMatchOnly(miss), 'plugin')).toBeUndefined()
  })

  it('环境：未运行时给「启动」，运行中给「停止」，RabbitMQ 额外给管理后台', () => {
    const rt = {
      id: 'rabbitmq', name: 'RabbitMQ', group: 'messagequeue', hasService: true,
      installed: [{ version: '3.13', active: true, scope: 'global' }],
    } as any
    const running = makeCtx({
      q: 'rabbitmq', qLC: 'rabbitmq', envRuntimes: [rt],
      envStatuses: { 'rabbitmq:3.13': { running: true } as any },
      envMgmt: { '3.13': false },
    })
    const actions = groupOf(runMatchOnly(running), 'env')!.results.map(r => r.envAction)
    expect(actions).toContain('stop')
    expect(actions).toContain('mgmt')

    const stopped = makeCtx({
      q: 'rabbitmq', qLC: 'rabbitmq', envRuntimes: [rt],
      envStatuses: { 'rabbitmq:3.13': { running: false } as any },
    })
    expect(groupOf(runMatchOnly(stopped), 'env')!.results.map(r => r.envAction)).toEqual(['start'])
  })

  it('文件：同一路径只出现一次，最多 20 条', () => {
    const mk = (i: number) => ({
      type: 'file' as const, label: `f${i}`, filePath: `C:\\f${i}.txt`, score: 50,
    })
    const ctx = makeCtx({ q: 'f', qLC: 'f', fileResults: [...Array(25)].map((_, i) => mk(i)) })
    expect(groupOf(runMatchOnly(ctx), 'file')!.results).toHaveLength(20)
  })

  it('计算器：算出结果即终止，不再往下搜条目', () => {
    const item = { id: 'i1', name: '=1+1', value: '', type: '文件' } as any
    const ctx = makeCtx({
      q: '=1+1', qLC: '=1+1',
      lc: { items: [{ item, nameLC: '=1+1', valueLC: '' }], notes: [], apps: [], sys: [] },
    })
    const groups = runMatchOnly(ctx)
    expect(groups).toHaveLength(1)
    expect(groups[0].type).toBe('calculator')
    expect(groups[0].results[0].calcResult).toBe('2')
  })
})

describe('matchNav', () => {
  // 与 useCommandSearch 的筛选口径一致：只有带 component 的内容页进得了候选，
  // ssh / dsh（只有 activate）在这之前就被滤掉了。
  const navItems = [
    { id: 'notes', labelKey: 'navNotes', icon: null },
    { id: 'environments', labelKey: 'navEnvironments', icon: null },
  ]
  const labels: Record<string, string> = { navNotes: '笔记', navEnvironments: '环境管理' }

  it('命中页面名，带出页面 id', () => {
    const ctx = makeCtx({ q: '笔记', qLC: '笔记', navItems, t: (k: string) => labels[k] ?? k })
    const groups = runMatchOnly(ctx)
    const nav = groupOf(groups, 'nav')!
    expect(nav.results.map(r => r.label)).toEqual(['笔记'])
    expect(nav.results[0].navPage).toBe('notes')
  })

  it('英文 id 也能命中（面板里敲 environments 一样找得到）', () => {
    const ctx = makeCtx({ q: 'environ', qLC: 'environ', navItems, t: (k: string) => labels[k] ?? k })
    const groups = runMatchOnly(ctx)
    expect(groupOf(groups, 'nav')!.results.map(r => r.navPage)).toEqual(['environments'])
  })

  it('拼音命中中文页面名', () => {
    const ctx = makeCtx({
      q: 'biji', qLC: 'biji', navItems, t: (k: string) => labels[k] ?? k,
      pinyinMatch: (text: string) => text === '笔记',
    })
    const groups = runMatchOnly(ctx)
    expect(groupOf(groups, 'nav')!.results.map(r => r.navPage)).toEqual(['notes'])
  })

  it('候选为空时不产生分组（ssh / dsh 被编排层滤掉）', () => {
    const ctx = makeCtx({ q: 'ssh', qLC: 'ssh', navItems: [], t: (k: string) => labels[k] ?? k })
    expect(groupOf(runMatchOnly(ctx), 'nav')).toBeUndefined()
  })
})

describe('ranking', () => {
  it('最佳匹配置顶，并从原分组里摘掉同一实体', () => {
    const groups: ResultGroup[] = [
      { type: 'item', label: '', results: [{ type: 'item', label: 'a', item: { id: 'i1' } as any, score: 100 }] },
      { type: 'app', label: '', results: [{ type: 'app', label: 'b', appPath: 'C:\\b.exe', score: 80 }] },
      { type: 'note', label: '', results: [{ type: 'note', label: 'c', note: { id: 'n1' } as any, score: 30 }] },
    ]
    injectBestMatch(groups, (k: string) => k)
    expect(groups[0].type).toBe('best')
    expect(groups[0].results.map(r => r.label)).toEqual(['a', 'b'])
    // 被提走的条目从原分组消失，低分的留下
    const item = groups.find(g => g.type === 'item')!
    expect(item.results).toHaveLength(0)
    expect(groups.find(g => g.type === 'note')!.results).toHaveLength(1)
  })

  it('插件与计算器不进最佳匹配', () => {
    const groups: ResultGroup[] = [
      { type: 'plugin', label: '', results: [{ type: 'plugin', label: 'p', pluginId: 'p1', pluginCommandId: 'c1', score: 99 }] },
      { type: 'calculator', label: '', results: [{ type: 'calculator', label: '1+1', score: 99 }] },
    ]
    injectBestMatch(groups, (k: string) => k)
    expect(groups.map(g => g.type)).toEqual(['plugin', 'calculator'])
  })

  it('分组按固定优先级排序：app 在 item 之前，file 在 plugin 之后（文件恒最后）', () => {
    const groups: ResultGroup[] = [
      { type: 'file', label: '', results: [] },
      { type: 'plugin', label: '', results: [] },
      { type: 'item', label: '', results: [] },
      { type: 'app', label: '', results: [] },
      { type: 'env', label: '', results: [] },
    ]
    sortGroups(groups)
    expect(groups.map(g => g.type)).toEqual(['app', 'item', 'env', 'plugin', 'file'])
  })

  it('被最佳匹配摘空的分组不再渲染（不会有「应用程序 0」的空标题）', () => {
    const groups: ResultGroup[] = [
      { type: 'item', label: '', results: [{ type: 'item', label: 'a', item: { id: 'i1' } as any, score: 100 }] },
      { type: 'app', label: '', results: [{ type: 'app', label: 'b', appPath: 'C:\\b.exe', score: 80 }] },
      { type: 'note', label: '', results: [{ type: 'note', label: 'c', note: { id: 'n1' } as any, score: 30 }] },
    ]
    injectBestMatch(groups, (k: string) => k)
    dropEmptyGroups(groups)
    sortGroups(groups)
    expect(groups.map(g => g.type)).toEqual(['best', 'note'])
    expect(groups[0].results.map(r => r.label)).toEqual(['a', 'b'])
  })
})
