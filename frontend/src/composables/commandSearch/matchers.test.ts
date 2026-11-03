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

  it('插件：content 命中按插件去重，每插件只留 1 条（一段 JSON 不铺满列表）', () => {
    const mk = (pid: string, cid: string) => ({ plugin: { id: pid, name: 'P' }, cmd: { id: cid, title: 'T' } }) as any
    const ctx = makeCtx({
      q: '{"a":1}', qLC: '{"a":1}',
      pluginCmdIndex: [mk('p1', 'c1'), mk('p1', 'c2'), mk('p2', 'c1'), mk('p3', 'c1')],
      calcPluginScore: (idx: any) =>
        idx.cmd.id === 'c2' || idx.plugin.id !== 'p2'
          ? { score: 72, matchType: 'content' }
          : { score: 90, matchType: 'keyword' },
    })
    const g = groupOf(runMatchOnly(ctx), 'plugin')!
    // p1 两条 content 命中去重成 1 条；p2(keyword) 与 p3(content) 各留 1 条
    expect(g.results).toHaveLength(3)
    expect(g.results.filter(r => r.pluginId === 'p1')).toHaveLength(1)
  })

  it('插件：inlineInput ≥ 40 字符时不拼进 label（长 SQL 不撑爆列表行）', () => {
    const idx = { plugin: { id: 'p1', name: 'P' }, cmd: { id: 'c1', title: 'T' } } as any
    const long = 'SELECT a, b, c FROM users WHERE name = 1 AND age > 2 ORDER BY id;'
    const ctx = makeCtx({
      q: long, qLC: long.toLowerCase(), pluginCmdIndex: [idx],
      calcPluginScore: () => ({ score: 93, matchType: 'match pattern', inlineInput: long }),
    })
    const g = groupOf(runMatchOnly(ctx), 'plugin')!
    expect(g.results[0].label).toBe('T')
    // 内容仍完整随 inlineInput 带给执行器
    expect(g.results[0].inlineInput).toBe(long)
  })

  it('插件：组内最多 30 条（单字母查询下命中能到 90+，不截断就铺满面板）', () => {
    const idxs = [...Array(50)].map((_, i) => ({
      plugin: { id: 'p' + i, name: 'P' }, cmd: { id: 'c' + i, title: 'T' + i },
    })) as any
    const ctx = makeCtx({
      q: 'git', qLC: 'git', pluginCmdIndex: idxs,
      calcPluginScore: () => ({ score: 90, matchType: 'keyword' }),
    })
    expect(groupOf(runMatchOnly(ctx), 'plugin')!.results).toHaveLength(30)
  })

  it('插件：单字母查询滤掉 <60 的弱命中（关键字包含 40 / 插件名 35 / 命令 id 30）', () => {
    const mk = (id: string) => ({ plugin: { id }, cmd: { id: 'c' } }) as any
    const ctx = makeCtx({
      q: 'a', qLC: 'a',
      pluginCmdIndex: [mk('weak1'), mk('weak2'), mk('strong')],
      calcPluginScore: (idx: any) => ({
        score: idx.plugin.id === 'strong' ? 90 : 40,
        matchType: idx.plugin.id === 'strong' ? 'keyword' : 'kw match',
      }),
    })
    const g = groupOf(runMatchOnly(ctx), 'plugin')!
    expect(g.results.map(r => r.pluginId)).toEqual(['strong'])
  })

  it('插件：内容/正则命中的原文随 hitText 带给前端插件（与 acceptsInput 无关）', () => {
    const idx = { plugin: { id: 'p1', name: 'P' }, cmd: { id: 'c1', title: 'T' } } as any
    const raw = '{"a":1}'
    const ctx = makeCtx({
      q: raw, qLC: raw, pluginCmdIndex: [idx],
      calcPluginScore: () => ({ score: 72, matchType: 'content', hitText: raw }),
    })
    const r = groupOf(runMatchOnly(ctx), 'plugin')!.results[0]
    expect(r.hitText).toBe(raw)
    // 未声明 acceptsInput：不作为后端参数
    expect(r.inlineInput).toBeUndefined()
  })

  it('斜杠发现：只敲 `/` 时列出全部带 prefix 的命令（原本是空列表，没人知道有这些命令）', () => {
    const mk = (pid: string, cid: string, prefix?: string) => ({
      plugin: { id: pid, name: 'P' }, cmd: { id: cid, title: cid, prefix },
    }) as any
    const ctx = makeCtx({
      q: '/', qLC: '/',
      pluginCmdIndex: [mk('p1', 'sql', '/sql'), mk('p2', 'jwt', '/jwt'), mk('p3', 'no-prefix')],
      // 只敲 `/` 时前缀规则不成立，打分全是 0
      calcPluginScore: () => ({ score: 0, matchType: '' }),
    })
    const g = groupOf(runMatchOnly(ctx), 'plugin')!
    expect(g.results.map(r => r.pluginCommandId).sort()).toEqual(['jwt', 'sql'])
    expect(g.results.every(r => r.score === 72 && r.matchType === 'slash')).toBe(true)
  })

  it('斜杠发现：`/s` 只补前缀以其开头的命令，且不重复已命中的那条', () => {
    const mk = (pid: string, cid: string, prefix: string) => ({
      plugin: { id: pid, name: 'P' }, cmd: { id: cid, title: cid, prefix },
    }) as any
    const ctx = makeCtx({
      q: '/s', qLC: '/s',
      pluginCmdIndex: [mk('p1', 'sql', '/sql'), mk('p2', 'jwt', '/jwt')],
      // 伪造「/s」已命中的情况不该发生（前缀不完整），但仍要保证不重复添加
      calcPluginScore: (idx: any) => idx.cmd.id === 'sql'
        ? { score: 95, matchType: 'slash' }
        : { score: 0, matchType: '' },
    })
    const g = groupOf(runMatchOnly(ctx), 'plugin')!
    expect(g.results.map(r => r.pluginCommandId)).toEqual(['sql'])
    expect(g.results[0].score).toBe(95)
  })

  it('插件排序权重写进 rankScore（组内与最佳匹配共用同一个数）', () => {
    const mk = (id: string) => ({ plugin: { id }, cmd: { id: 'c' } }) as any
    const ctx = makeCtx({
      q: 'git', qLC: 'git',
      pluginCmdIndex: [mk('cold'), mk('hot')],
      calcPluginScore: () => ({ score: 90, matchType: 'keyword' }),
      // 两个命令命中分相同，只有频次不同
      frecencyScore: (k: string) => (k.includes('hot') ? 30 : 0),
    })
    const g = groupOf(runMatchOnly(ctx), 'plugin')!
    expect(g.results.map(r => r.pluginId)).toEqual(['hot', 'cold'])
    // 90 + min(30,30)*0.2 = 96；冷门那条就是 90
    expect(g.results[0].rankScore).toBeCloseTo(96, 5)
    expect(g.results[1].rankScore).toBeCloseTo(90, 5)
  })

  it('斜杠查询只认前缀通道：keyword 90 / 正则 88 这类噪声一律不列', () => {
    // 真实数据里的两个例子：disk-analyzer 的 keywords 含 '/'，calcsheet 的正则
    // `^[0-9+*/().%^, -]+$` 也能匹配一个孤零零的 '/' —— 敲斜杠时它们会排在真命令前面。
    const mk = (id: string) => ({ plugin: { id }, cmd: { id: 'c' } }) as any
    const ctx = makeCtx({
      q: '/', qLC: '/',
      pluginCmdIndex: [mk('kw'), mk('regex')],
      calcPluginScore: (idx: any) => idx.plugin.id === 'kw'
        ? { score: 90, matchType: 'keyword' }
        : { score: 88, matchType: 'pattern short' },
    })
    expect(groupOf(runMatchOnly(ctx), 'plugin')).toBeUndefined()
  })

  it('粘 URL：内置「打开链接」(96) 排在插件的正则内容命中(93) 之前', () => {
    const idx = { plugin: { id: 'p1', name: 'P' }, cmd: { id: 'c1', title: '登录测试' } } as any
    const q = 'https://example.com/a?b=1'
    const ctx = makeCtx({
      q, qLC: q, pluginCmdIndex: [idx],
      calcPluginScore: () => ({ score: 93, matchType: 'match pattern', hitText: q }),
    })
    const groups = runFull(ctx)
    expect(groups[0].type).toBe('best')
    expect(groups[0].results.map(r => r.type)).toEqual(['url', 'plugin'])
  })

  it('单位换算：内置换算(94) 排在插件的同类命令(93) 之前', () => {
    const idx = { plugin: { id: 'p1', name: 'P' }, cmd: { id: 'c1', title: '单位换算' } } as any
    const ctx = makeCtx({
      q: '10km to mi', qLC: '10km to mi', pluginCmdIndex: [idx],
      calcPluginScore: () => ({ score: 93, matchType: 'match pattern', hitText: '10km to mi' }),
    })
    const groups = runFull(ctx)
    expect(groups[0].type).toBe('best')
    expect(groups[0].results[0].type).toBe('calculator')
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

  it('换算结果(94) 进最佳匹配并排在插件(93) 之前；剪贴板动作永不进', () => {
    const groups: ResultGroup[] = [
      { type: 'calculator', label: '', results: [{ type: 'calculator', label: '6.2 mi', calcResult: '6.2', score: 94 }] },
      { type: 'plugin', label: '', results: [{ type: 'plugin', label: 'p', pluginId: 'p1', pluginCommandId: 'c1', score: 93, matchType: 'match pattern' }] },
      { type: 'clipboard-action', label: '', results: [{ type: 'clipboard-action', label: 'clip', score: 100 }] },
    ]
    injectBestMatch(groups, (k: string) => k)
    expect(groups[0].type).toBe('best')
    expect(groups[0].results.map(r => r.label)).toEqual(['6.2 mi', 'p'])
    expect(groups.map(g => g.type)).toContain('clipboard-action')
  })

  it('插件资格按命中类型判定：content(72) 进最佳匹配，标题前缀(75) 不进', () => {
    const groups: ResultGroup[] = [
      // 资格按命中类型判：content(72) 进；标题前缀(75) 是顺手扫到的，不进
      { type: 'plugin', label: '', results: [
        { type: 'plugin', label: 'content', pluginId: 'p1', pluginCommandId: 'c1', score: 72, matchType: 'content' },
        { type: 'plugin', label: 'title-prefix', pluginId: 'p2', pluginCommandId: 'c2', score: 75, matchType: 'prefix' },
      ] },
    ]
    injectBestMatch(groups, (k: string) => k)
    expect(groups[0].type).toBe('best')
    expect(groups[0].results.map(r => r.label)).toEqual(['content'])
    expect(groups.find(g => g.type === 'plugin')!.results.map(r => r.label)).toEqual(['title-prefix'])
  })

  it('插件惩罚只影响最佳匹配内部先后，不影响资格', () => {
    const groups: ResultGroup[] = [
      { type: 'plugin', label: '', results: [
        { type: 'plugin', label: 'p', pluginId: 'p1', pluginCommandId: 'c1', score: 72, matchType: 'content' },
      ] },
      { type: 'app', label: '', results: [{ type: 'app', label: 'a', appPath: 'C:\\a.exe', score: 70 }] },
    ]
    injectBestMatch(groups, (k: string) => k)
    // 72-3=69 仍排在 70 之后，但两条都在（门槛不再吃掉 72 分的内容命中）
    expect(groups[0].results.map(r => r.label)).toEqual(['a', 'p'])
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
