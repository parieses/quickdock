import { describe, expect, it } from 'vitest'
import {
  closeTab,
  findResumable,
  findTab,
  openTab,
  patchTab,
  syncHost,
  type SshTab,
} from './sshTabs'
import type { SshHost, SshSessionInfo } from './ssh'

function host(id: string, over: Partial<SshHost> = {}): SshHost {
  return {
    id,
    name: id,
    host: `${id}.example.com`,
    port: 22,
    user: 'root',
    authType: 'password',
    keyPath: '',
    legacyAlgorithms: false,
    env: 'test',
    parentId: '',
    isFolder: false,
    sort: 0,
    remark: '',
    lastConnectedAt: '',
    createdAt: '',
    updatedAt: '',
    hasPassword: true,
    hasPassphrase: false,
    ...over,
  }
}

function session(id: string, hostId: string, started: number): SshSessionInfo {
  return {
    id,
    hostId,
    state: 'connected',
    cols: 80,
    rows: 24,
    received: 0,
    label: `root@${hostId}`,
    started,
    rttMs: 0,
  }
}

const tabs = (...ids: string[]): SshTab[] =>
  ids.map(id => ({ id, host: host(id), resume: null, sessionId: '', busy: false }))

describe('openTab', () => {
  it('新主机追加到末尾并聚焦', () => {
    const r = openTab(tabs('a'), host('b'))
    expect(r.tabs.map(t => t.id)).toEqual(['a', 'b'])
    expect(r.activeId).toBe('b')
  })

  it('已存在则只聚焦，不新建', () => {
    // 这是「同主机只 1 条 TCP」在 UI 层的关键：重复点主机不能开出第二个标签，
    // 否则会多一个 channel，旧会话变孤儿。
    const orig = tabs('a', 'b')
    const r = openTab(orig, host('a'))
    expect(r.tabs.length).toBe(2)
    expect(r.activeId).toBe('a')
    // 同一引用：没产生新数组，Vue 不会因这次点击重渲整个标签栏
    expect(r.tabs).toBe(orig)
  })

  it('resume 只在新建时生效', () => {
    const s = session('s1', 'b', 1000)
    const r = openTab(tabs('a'), host('b'), s)
    expect(r.tabs[1].resume).toBe(s)
    // 已存在的标签即使传了 resume 也不该被覆盖——那条标签连着别的会话
    const r2 = openTab(r.tabs, host('b'), session('s2', 'b', 2000))
    expect(findTab(r2.tabs, 'b')?.resume).toBe(s)
  })
})

describe('closeTab', () => {
  it('关中间的标签聚焦右边的', () => {
    // 符合用户视线移动方向：标签栏里点中间那个，关掉后往右看。
    const r = closeTab(tabs('a', 'b', 'c'), 'b', 'b')
    expect(r.tabs.map(t => t.id)).toEqual(['a', 'c'])
    expect(r.activeId).toBe('c')
    expect(r.closed?.id).toBe('b')
  })

  it('关最后一个聚焦左边的', () => {
    const r = closeTab(tabs('a', 'b', 'c'), 'c', 'c')
    expect(r.activeId).toBe('b')
  })

  it('关唯一的标签后 activeId 为空', () => {
    // 空值是合法状态：右侧显示欢迎页，提示用户去选主机。
    const r = closeTab(tabs('a'), 'a', 'a')
    expect(r.tabs).toEqual([])
    expect(r.activeId).toBe('')
  })

  it('关非当前标签不动 activeId', () => {
    const r = closeTab(tabs('a', 'b', 'c'), 'a', 'c')
    expect(r.activeId).toBe('c')
    expect(r.tabs.map(t => t.id)).toEqual(['b', 'c'])
  })

  it('关不存在的标签是安全的空操作', () => {
    const orig = tabs('a')
    const r = closeTab(orig, 'nope', 'a')
    expect(r.tabs).toBe(orig)
    expect(r.closed).toBeNull()
  })

  it('返回被关的标签，供调用方去断开它的会话', () => {
    // 不断连的后果：Go 侧那条 session 永远留着，回放缓冲占着内存，
    // 且连接池的引用计数减不下去（最后一个引用不归零，TCP 一直吊着）。
    const withSession: SshTab[] = [
      { id: 'a', host: host('a'), resume: session('s1', 'a', 1), sessionId: '', busy: false },
    ]
    const r = closeTab(withSession, 'a', 'a')
    expect(r.closed?.resume?.id).toBe('s1')
  })
})

describe('patchTab', () => {
  it('只改指定标签', () => {
    const r = patchTab(tabs('a', 'b'), 'b', { busy: true })
    expect(r[0].busy).toBe(false)
    expect(r[1].busy).toBe(true)
  })

  it('不存在的 id 返回等价数组', () => {
    const orig = tabs('a')
    expect(patchTab(orig, 'nope', { busy: true })).toEqual(orig)
  })
})

describe('syncHost', () => {
  it('用最新的主机信息刷新已打开的标签', () => {
    // 用户在表单里改了名字，标签上必须立刻变，否则要关掉重开才生效。
    const orig: SshTab[] = [{ id: 'a', host: host('a', { name: '旧名' }), resume: null, sessionId: '', busy: false }]
    const r = syncHost(orig, [host('a', { name: '新名' })])
    expect(r[0].host.name).toBe('新名')
  })

  it('没有变化时返回同一引用（避免无谓重渲染）', () => {
    const h = host('a')
    const orig: SshTab[] = [{ id: 'a', host: h, resume: null, sessionId: '', busy: false }]
    expect(syncHost(orig, [h])).toBe(orig)
  })

  it('主机被删除时不动标签', () => {
    // 标签里还握着 host 引用，删了也要留着让用户看到会话内容，
    // 彻底移除是 closeTab 的事。
    const orig: SshTab[] = [{ id: 'a', host: host('a'), resume: null, sessionId: '', busy: false }]
    expect(syncHost(orig, [])[0].id).toBe('a')
  })
})

describe('findResumable', () => {
  const retained = (s: SshSessionInfo) => s.state === 'connected' || s.state === 'disconnected'

  it('找到该主机可接回的会话', () => {
    // 不接回就 Connect 会多开一个 channel，旧会话变成孤儿。
    expect(findResumable([session('s1', 'a', 1000)], 'a', retained)?.id).toBe('s1')
  })

  it('每台主机取最早的那条', () => {
    // 用户最先开的那条通常才是想继续用的；后面那些可能是误开的。
    const list = [session('s2', 'a', 2000), session('s1', 'a', 1000)]
    expect(findResumable(list, 'a', retained)?.id).toBe('s1')
  })

  it('closed 状态的会话不接回', () => {
    const closed = { ...session('s2', 'a', 500), state: 'closed' }
    expect(findResumable([closed], 'a', retained)).toBeNull()
  })

  it('connected 与 disconnected 都能接回', () => {
    const disc = { ...session('s1', 'a', 1000), state: 'disconnected' }
    expect(findResumable([disc], 'a', retained)?.id).toBe('s1')
  })

  it('没有对应会话时返回 null', () => {
    expect(findResumable([], 'a', retained)).toBeNull()
    expect(findResumable([session('s1', 'b', 1000)], 'a', retained)).toBeNull()
  })

  it('没有 hostId 的会话被忽略', () => {
    const orphan = { ...session('s1', '', 1000) }
    expect(findResumable([orphan], '', retained)).toBeNull()
  })

  it('多条里混着别的会话时只认本主机的', () => {
    const list = [session('other', 'b', 100), session('mine', 'a', 9999)]
    expect(findResumable(list, 'a', retained)?.id).toBe('mine')
  })
})
