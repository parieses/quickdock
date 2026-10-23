import { describe, expect, it } from 'vitest'
import {
  buildSshTree,
  collectFolders,
  collectSubtreeIds,
  filterSshTree,
  flattenSshTree,
  resolveDrop,
  treeHasHost,
  type SshTreeNode,
} from './sshTree'
import type { SshHost } from './ssh'

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

function folder(id: string, over: Partial<SshHost> = {}): SshHost {
  return host(id, { isFolder: true, name: id, ...over })
}

/** 把树递归压成 "a/b/c" 便于断言结构（能看出任意深度的嵌套）。 */
function shape(nodes: SshTreeNode[]): string[] {
  return nodes.map((n) => {
    const kids = n.children.length ? `/${shape(n.children).join(',')}` : ''
    return `${n.host.id}${kids}`
  })
}

describe('buildSshTree', () => {
  it('空列表返回空树', () => {
    expect(buildSshTree([])).toEqual([])
  })

  it('文件夹排在主机前面', () => {
    // sort 都是 0，靠 isFolder 分组。文件夹排前面是因为清单顶部
    // 应该先看到分组结构，而不是散落的主机。
    const tree = buildSshTree([host('web1'), folder('prod'), host('web2')])
    expect(tree.map((n) => n.host.id)).toEqual(['prod', 'web1', 'web2'])
  })

  it('按 sort 排序，同 sort 退回名称', () => {
    const tree = buildSshTree([
      host('c', { sort: 2 }),
      host('a', { sort: 1 }),
      host('b', { sort: 1 }),
    ])
    expect(tree.map((n) => n.host.id)).toEqual(['a', 'b', 'c'])
  })

  it('按 parentId 组装层级', () => {
    const tree = buildSshTree([
      folder('prod'),
      host('web1', { parentId: 'prod' }),
      host('web2', { parentId: 'prod' }),
      folder('test'),
      host('db1', { parentId: 'test' }),
      host('local'),
    ])
    expect(shape(tree)).toEqual(['prod/web1,web2', 'test/db1', 'local'])
    expect(tree[0].depth).toBe(0)
    expect(tree[0].children[0].depth).toBe(1)
  })

  it('支持多层嵌套', () => {
    const tree = buildSshTree([
      folder('a'),
      folder('b', { parentId: 'a' }),
      host('h', { parentId: 'b' }),
    ])
    expect(shape(tree)).toEqual(['a/b/h'])
    expect(tree[0].children[0].children[0].depth).toBe(2)
  })

  it('hostCount 递归统计子树里的主机数', () => {
    // 文件夹自身不计，只数主机。清单上显示「(3)」指的是里面有几台可连的机器。
    const tree = buildSshTree([
      folder('prod'),
      host('web1', { parentId: 'prod' }),
      folder('sub', { parentId: 'prod' }),
      host('db1', { parentId: 'sub' }),
      host('web2', { parentId: 'prod' }),
    ])
    expect(tree[0].hostCount).toBe(3)
    expect(tree[0].children[1].hostCount).toBe(1)
    expect(buildSshTree([folder('empty')])[0].hostCount).toBe(0)
  })

  it('父节点不存在时提升到根而不是丢弃', () => {
    // 这是最容易出「用户建了却看不见」的坑：parentId 指向已删除的文件夹，
    // 按 parentId 挂就会凭空消失。
    const tree = buildSshTree([host('orphan', { parentId: 'gone' })])
    expect(shape(tree)).toEqual(['orphan'])
  })

  it('父节点不是文件夹时提升到根', () => {
    // 不能把主机挂到另一台主机下——主机节点没有 children 语义。
    const tree = buildSshTree([host('a'), host('b', { parentId: 'a' })])
    expect(shape(tree)).toEqual(['a', 'b'])
  })

  it('环状数据被切断且不丢节点', () => {
    // A→B→C→A。脏数据或跨库迁移可能造出这种结构，
    // 不处理就是无限递归爆栈 / 白屏。
    const tree = buildSshTree([
      folder('a', { parentId: 'c' }),
      folder('b', { parentId: 'a' }),
      folder('c', { parentId: 'b' }),
    ])
    const flat = flattenSshTree(tree, new Set())
    expect(flat.map((n) => n.host.id).sort()).toEqual(['a', 'b', 'c'])
  })

  it('自环（A 的父是 A）也能处理', () => {
    const tree = buildSshTree([folder('self', { parentId: 'self' })])
    expect(flattenSshTree(tree, new Set()).map((n) => n.host.id)).toEqual(['self'])
  })

  it('超深嵌套提升到根而不是丢弃', () => {
    // 构造 50 层：真实使用最多两三层，这是防脏数据打爆递归的兜底。
    const list: SshHost[] = []
    for (let i = 0; i < 50; i++) {
      list.push(folder(`f${i}`, { parentId: i === 0 ? '' : `f${i - 1}` }))
    }
    const flat = flattenSshTree(buildSshTree(list), new Set())
    // 一个都不能少：数据可能在，只是不能藏在第 41 层。
    expect(flat.length).toBe(50)
    expect(flat.some((n) => n.depth === 0 && n.host.id !== 'f0')).toBe(true)
  })

  it('重复 id 只保留第一个', () => {
    const tree = buildSshTree([host('dup', { name: '第一个' }), host('dup', { name: '第二个' })])
    expect(tree.length).toBe(1)
    expect(tree[0].host.name).toBe('第一个')
  })

  it('空 parentId 的空白串视为根', () => {
    // Go 侧扫出来的空值可能是 "" 也可能是 "   "，都要当根。
    const tree = buildSshTree([host('a', { parentId: '   ' })])
    expect(tree[0].depth).toBe(0)
  })
})

describe('flattenSshTree', () => {
  const tree = () =>
    buildSshTree([
      folder('prod'),
      host('web1', { parentId: 'prod' }),
      host('web2', { parentId: 'prod' }),
      host('local'),
    ])

  it('全部展开时深度优先', () => {
    expect(flattenSshTree(tree(), new Set()).map((n) => n.host.id)).toEqual([
      'prod',
      'web1',
      'web2',
      'local',
    ])
  })

  it('折叠的文件夹不返回其子节点，但自己仍在', () => {
    const flat = flattenSshTree(tree(), new Set(['prod']))
    expect(flat.map((n) => n.host.id)).toEqual(['prod', 'local'])
  })

  it('折叠状态只影响展示，不影响数据', () => {
    const t = tree()
    const before = flattenSshTree(t, new Set()).length
    flattenSshTree(t, new Set(['prod']))
    expect(flattenSshTree(t, new Set()).length).toBe(before)
  })
})

describe('resolveDrop', () => {
  const tree = () =>
    buildSshTree([
      folder('prod'),
      host('web1', { parentId: 'prod' }),
      host('web2', { parentId: 'prod' }),
      folder('test'),
      host('db1', { parentId: 'test' }),
      host('local'),
    ])

  it('拖进文件夹 = 成为其子节点', () => {
    expect(resolveDrop(tree(), 'local', 'prod', 'inside')).toEqual({ parentId: 'prod' })
  })

  it('拖到根节点前后 = 移到根', () => {
    expect(resolveDrop(tree(), 'web1', 'local', 'before')).toEqual({ parentId: '' })
    expect(resolveDrop(tree(), 'web1', 'local', 'after')).toEqual({ parentId: '' })
  })

  it('拖到别的文件夹里的主机前后 = 进入那个父节点', () => {
    expect(resolveDrop(tree(), 'local', 'db1', 'before')).toEqual({ parentId: 'test' })
  })

  it('落在主机上的 inside 退化为插入其前，不静默失败', () => {
    // 主机不是文件夹，不能成为父。给一个明确的落点，
    // 好过返回 null 让拖拽「没反应」。
    expect(resolveDrop(tree(), 'local', 'db1', 'inside')).toEqual({ parentId: 'test' })
  })

  it('拒绝把节点拖进自己的子孙里', () => {
    // 这是最危险的非法操作：会造出环，渲染时无限递归。
    expect(resolveDrop(tree(), 'prod', 'web1', 'inside')).toBeNull()
  })

  it('拒绝自拖', () => {
    expect(resolveDrop(tree(), 'prod', 'prod', 'inside')).toBeNull()
  })

  it('不存在的 id 返回 null', () => {
    expect(resolveDrop(tree(), 'nope', 'prod', 'inside')).toBeNull()
    expect(resolveDrop(tree(), 'local', 'nope', 'inside')).toBeNull()
    expect(resolveDrop(tree(), '', 'prod', 'inside')).toBeNull()
  })
})

describe('treeHasHost', () => {
  it('只有文件夹时为 false', () => {
    // 空态判断要区分「一个主机都没有」和「有几个空分组」——
    // 后者该显示分组，不该显示「还没有主机」。
    expect(treeHasHost(buildSshTree([folder('a'), folder('b')]))).toBe(false)
  })

  it('嵌套里有主机则为 true', () => {
    expect(treeHasHost(buildSshTree([folder('a'), host('h', { parentId: 'a' })]))).toBe(true)
  })
})

describe('collectFolders', () => {
  it('深度优先收集全部文件夹', () => {
    const t = buildSshTree([
      folder('a'),
      folder('b', { parentId: 'a' }),
      host('h', { parentId: 'b' }),
      folder('c'),
    ])
    expect(collectFolders(t).map((n) => n.host.id)).toEqual(['a', 'b', 'c'])
  })
})

describe('collectSubtreeIds', () => {
  it('返回自身及其全部后代', () => {
    const t = buildSshTree([
      folder('a'),
      host('h1', { parentId: 'a' }),
      folder('b', { parentId: 'a' }),
      host('h2', { parentId: 'b' }),
      host('h3', { parentId: 'b' }),
      host('h4'),
    ])
    // 删掉分组 a 必须连 h1/h2/h3 一起收走，否则它们会变成指向已删除主机的孤儿标签
    expect(collectSubtreeIds(t, 'a').sort()).toEqual(['a', 'b', 'h1', 'h2', 'h3'])
  })

  it('叶子节点只返回自己', () => {
    const t = buildSshTree([folder('a'), host('h1', { parentId: 'a' })])
    expect(collectSubtreeIds(t, 'h1')).toEqual(['h1'])
  })

  it('找不到的 id 返回空数组', () => {
    const t = buildSshTree([host('h1')])
    expect(collectSubtreeIds(t, 'nope')).toEqual([])
  })
})

describe('filterSshTree', () => {
  const t = buildSshTree([
    folder('prod', { name: '生产环境' }),
    host('db1', { name: '数据库', host: 'db1.prod.example.com', parentId: 'prod', remark: '主库' }),
    host('web1', { name: 'web-1', host: 'web1.prod.example.com', parentId: 'prod' }),
    folder('dev', { name: '开发' }),
    host('local', { name: '本地', host: '127.0.0.1', parentId: 'dev' }),
    host('bare', { name: '裸机', host: '10.0.0.9' }),
  ])

  it('空关键字原样返回', () => {
    expect(filterSshTree(t, '   ')).toBe(t)
  })

  it('命中主机时保留祖先链，不相关的分支整个消失', () => {
    expect(shape(filterSshTree(t, 'db1'))).toEqual(['prod/db1'])
  })

  it('命中文件夹名时整棵子树保留（按分组名找里面有什么）', () => {
    // 用户搜「生产」的意图是「看看生产组里有啥」，若再按关键字筛子项
    // 会得到「分组在、里都没了」，与意图相反。
    expect(shape(filterSshTree(t, '生产'))).toEqual(['prod/db1,web1'])
  })

  it('文件夹命中但大小写不同也能匹配', () => {
    expect(shape(filterSshTree(t, 'WEB1'))).toEqual(['prod/web1'])
  })

  it('备注也参与匹配（用户不一定记得主机名叫什么）', () => {
    expect(shape(filterSshTree(t, '主库'))).toEqual(['prod/db1'])
  })

  it('保留下来的文件夹重算 hostCount，与眼前能点开的主机数一致', () => {
    const [prod] = filterSshTree(t, 'web1')
    expect(prod.hostCount).toBe(1)
  })

  it('完全不匹配时返回空树', () => {
    expect(filterSshTree(t, 'zzz')).toEqual([])
  })
})
