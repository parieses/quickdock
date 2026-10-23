/**
 * 会话树的构建与操作（纯函数，不碰 DOM 与后端）。
 *
 * 为什么要单独抽出来：组树里最容易出错的地方是**孤儿节点**——
 * parentId 指向一个不存在或已被删掉的节点时，如果直接按 parentId 挂，
 * 那台主机就会从树上彻底消失（用户建了却看不见，比直接报错难查一万倍）。
 * 后端 checkSshParentLocked 已经拦了新建/移动，但**删除文件夹**是级联的，
 * 历史脏数据或导入的数据仍可能留下孤儿，所以前端必须自己兜住。
 *
 * 全部纯函数 → 可以直接跑 vitest，不需要起 Vue 或 mock 后端。
 */
import type { SshHost } from './ssh'

/** 树节点。文件夹与主机同表，靠 kind 区分。 */
export interface SshTreeNode {
  host: SshHost
  /** folder | host */
  kind: 'folder' | 'host'
  /** 直接子节点，已按 sort → name 排好 */
  children: SshTreeNode[]
  /** 层级深度，根为 0；仅用于缩进 */
  depth: number
  /** 该节点及其子树里的主机总数（文件夹显示「(3)」） */
  hostCount: number
}

function compareNodes(a: SshHost, b: SshHost): number {
  // 文件夹排前面：清单顶部的分组比散落的主机更值得先看到。
  if (a.isFolder !== b.isFolder) return a.isFolder ? -1 : 1
  const sa = Number.isFinite(a.sort) ? a.sort : 0
  const sb = Number.isFinite(b.sort) ? b.sort : 0
  if (sa !== sb) return sa - sb
  // sort 相同时（老数据默认值都是 0）退回名称排序，
  // 否则顺序取决于 SQLite 的返回顺序，重启一次就变了。
  return (a.name || a.host || '').localeCompare(b.name || b.host || '')
}

/**
 * 把扁平列表组装成树。
 *
 * 关键约束：
 *  1. 父节点不存在（被删/脏数据）→ **提升到根**，不丢弃。
 *  2. 父节点存在但不是文件夹 → 也提升到根（不能把主机挂到另一台主机下）。
 *  3. 数据里存在环（A→B→A，MoveHost 理论上拦得住但脏数据可能）→
 *     断环时把节点提升到根，保证不递归爆栈、也不丢节点。
 *  4. 同一个 id 出现两次 → 保留第一个，丢弃后续（否则树上会出现两个同名项，
 *     点哪个都对不上会话）。
 */
export function buildSshTree(list: SshHost[]): SshTreeNode[] {
  const byId = new Map<string, SshHost>()
  for (const h of list) {
    if (byId.has(h.id)) continue // 约束 4
    byId.set(h.id, h)
  }

  // 只有文件夹能当父节点。
  const isFolder = new Set<string>()
  for (const [id, h] of byId) {
    if (h.isFolder) isFolder.add(id)
  }

  const parentUsable = (h: SshHost): string | null => {
    const pid = (h.parentId || '').trim()
    if (!pid) return ''
    if (!isFolder.has(pid)) return null // 约束 1、2：父不存在或不是文件夹
    return pid
  }

  // 约束 3：找环。用迭代上溯 + 状态染色（0=未访问 1=在当前路径上 2=已确认无环），
  // 避免递归爆栈，也避免「A→B→A」这种互相指认时两边都以为自己没问题。
  const mark = new Map<string, number>()
  const cyclic = new Set<string>()
  for (const start of byId.keys()) {
    if (mark.get(start) === 2) continue
    const path: string[] = []
    let cur: string | null = start
    while (cur) {
      const st = mark.get(cur) ?? 0
      if (st === 1) {
        // 撞上当前路径上的节点 → 从它起往后都是环，整段标记。
        const at = path.indexOf(cur)
        for (const id of path.slice(at)) cyclic.add(id)
        break
      }
      if (st === 2) break
      mark.set(cur, 1)
      path.push(cur)
      const h = byId.get(cur)
      if (!h) break
      const pid = parentUsable(h)
      cur = pid && pid !== '' ? pid : null
    }
    for (const id of path) mark.set(id, 2)
  }

  const childrenOf = new Map<string, SshHost[]>()
  const roots: SshHost[] = []
  for (const h of byId.values()) {
    const pid = cyclic.has(h.id) ? null : parentUsable(h)
    if (pid === null || pid === '') {
      roots.push(h)
      continue
    }
    const arr = childrenOf.get(pid)
    if (arr) arr.push(h)
    else childrenOf.set(pid, [h])
  }

  roots.sort(compareNodes)
  for (const arr of childrenOf.values()) arr.sort(compareNodes)

  const out: SshTreeNode[] = []
  // 深度上限：环检测已切断环，这里防「脏数据造出几千层嵌套」把递归打爆。
  // 超过上限的节点不是丢弃而是**提升成根**——数据可能在，只是不该藏在第 41 层。
  const maxDepth = 40
  const demoted: SshHost[] = []
  const buildUnder = (h: SshHost, depth: number): SshTreeNode => {
    let count = h.isFolder ? 0 : 1
    const children: SshTreeNode[] = []
    const kids = childrenOf.get(h.id) || []
    for (const k of kids) {
      if (depth >= maxDepth) {
        demoted.push(k)
        continue
      }
      const node = buildUnder(k, depth + 1)
      children.push(node)
      count += node.hostCount
    }
    return { host: h, kind: h.isFolder ? 'folder' : 'host', children, depth, hostCount: count }
  }
  for (const r of roots) out.push(buildUnder(r, 0))
  for (const d of demoted) out.push(buildUnder(d, 0))
  out.sort(compareNodesNode)
  return out
}

function compareNodesNode(a: SshTreeNode, b: SshTreeNode): number {
  return compareNodes(a.host, b.host)
}

/** 深度优先展平（配合「全部展开」使用；折叠状态由 UI 自己控制）。 */
export function flattenSshTree(nodes: SshTreeNode[], collapsed: Set<string>): SshTreeNode[] {
  const out: SshTreeNode[] = []
  const walk = (list: SshTreeNode[]) => {
    for (const n of list) {
      out.push(n)
      if (n.kind === 'folder' && !collapsed.has(n.host.id)) walk(n.children)
    }
  }
  walk(nodes)
  return out
}

/**
 * 拖拽落点计算：源节点拖到目标节点的什么位置。
 *
 * 返回 null 表示这个落点非法（比如把父节点拖进自己的儿子里），
 * 调用方据此不发起 MoveHost —— 前端先拦一道，比等数据库报错更即时。
 */
export type DropPosition = 'inside' | 'before' | 'after'

export function resolveDrop(
  tree: SshTreeNode[],
  dragId: string,
  targetId: string,
  position: DropPosition,
): { parentId: string } | null {
  if (!dragId || !targetId || dragId === targetId) return null

  const index = new Map<string, SshTreeNode>()
  const collect = (list: SshTreeNode[]) => {
    for (const n of list) {
      index.set(n.host.id, n)
      collect(n.children)
    }
  }
  collect(tree)

  const drag = index.get(dragId)
  const target = index.get(targetId)
  if (!drag || !target) return null

  // 把 X 拖进它自己的子孙里会成环，必须拒绝。
  const isDescendant = (node: SshTreeNode, ancestorId: string): boolean => {
    if (node.host.id === ancestorId) return true
    return node.children.some((c) => isDescendant(c, ancestorId))
  }
  if (isDescendant(drag, target.host.id)) return null

  if (position === 'inside' && target.kind === 'folder') {
    return { parentId: target.host.id }
  }
  // before/after：插到目标所在的父节点下，顺序由后端 nextSort 追加。
  // 落在主机上的 'inside' 也退化成这条——不能把主机挂到另一台主机下，
  // 与其静默失败，不如明确插到它前面。
  return { parentId: parentOf(tree, target.host.id) }
}

/**
 * 找某节点的父 id（根返回 ''）。
 *
 * 用一次遍历建父映射再查，而不是每个节点都扫一遍树 ——
 * 拖拽排序时 resolveDrop 会被高频调用，O(n²) 在主机数上百时会明显卡手。
 */
function parentOf(tree: SshTreeNode[], id: string): string {
  let found = ''
  const walk = (list: SshTreeNode[], parentId: string) => {
    for (const n of list) {
      if (n.host.id === id) {
        found = parentId
        return true
      }
      if (walk(n.children, n.host.id)) return true
    }
    return false
  }
  walk(tree, '')
  return found
}

/** 树里是否还有任何主机（空态判断用）。 */
export function treeHasHost(nodes: SshTreeNode[]): boolean {
  for (const n of nodes) {
    if (n.kind === 'host') return true
    if (treeHasHost(n.children)) return true
  }
  return false
}

/**
 * 关键字过滤：返回一棵「只剩命中项及其祖先」的新树。
 *
 * 三条刻意的规则：
 *  1. **文件夹自身命中 → 整棵子树保留**。用户搜「生产」是想看那个分组里有什么，
 *     把子项按关键字再筛一遍等于告诉他「分组在，里都没了」，与意图相反。
 *  2. 只有子孙命中的文件夹要保留（否则命中项无处挂），并重算 hostCount，
 *     数字必须和眼前能点开的主机数一致。
 *  3. 匹配 name / host / user / remark / env 五个字段：库里那台机器叫什么、
 *     备注写了什么，用户自己也不一定记得，多匹配几个字段的成本只是几次字符串比较。
 *
 * 不处理大小写以外的模糊匹配（拼音首字母等）——主机名本来就是 ASCII，
 * 真要按拼音找，搜索框里那几个字母和直接输主机名的成本差不多。
 */
export function filterSshTree(nodes: SshTreeNode[], keyword: string): SshTreeNode[] {
  const kw = String(keyword || '').trim().toLowerCase()
  if (!kw) return nodes

  const hit = (h: SshHost): boolean =>
    [h.name, h.host, h.user, h.remark, h.env].some(
      v => typeof v === 'string' && v.toLowerCase().includes(kw),
    )

  const walk = (list: SshTreeNode[]): SshTreeNode[] => {
    const out: SshTreeNode[] = []
    for (const n of list) {
      if (n.kind === 'folder') {
        if (hit(n.host)) {
          out.push(n)
          continue
        }
        const kids = walk(n.children)
        if (kids.length === 0) continue
        out.push({ ...n, children: kids, hostCount: countHosts(kids) })
        continue
      }
      if (hit(n.host)) out.push(n)
    }
    return out
  }
  return walk(nodes)
}

function countHosts(nodes: SshTreeNode[]): number {
  let n = 0
  for (const node of nodes) {
    n += node.kind === 'folder' ? countHosts(node.children) : 1
  }
  return n
}

/**
 * 收集节点及其全部后代的 host id（含自身）。
 *
 * 删文件夹时用：后端是级联删除，而文件夹本身不会有标签，只看当前 id
 * 会一个都匹配不到，结果就是分组从树上消失、里面主机的标签和会话全留在
 * 屏幕上——重连必然失败，且那条会话再无 UI 入口可关。
 */
export function collectSubtreeIds(nodes: SshTreeNode[], id: string): string[] {
  const target = findNode(nodes, id)
  if (!target) return []
  const out: string[] = []
  const walk = (n: SshTreeNode) => {
    out.push(n.host.id)
    for (const c of n.children) walk(c)
  }
  walk(target)
  return out
}

function findNode(nodes: SshTreeNode[], id: string): SshTreeNode | null {
  for (const n of nodes) {
    if (n.host.id === id) return n
    const hit = findNode(n.children, id)
    if (hit) return hit
  }
  return null
}

/** 收集全部文件夹 id（建「新建到某个分组」的下拉用）。 */
export function collectFolders(nodes: SshTreeNode[], out: SshTreeNode[] = []): SshTreeNode[] {
  for (const n of nodes) {
    if (n.kind === 'folder') {
      out.push(n)
      collectFolders(n.children, out)
    }
  }
  return out
}
