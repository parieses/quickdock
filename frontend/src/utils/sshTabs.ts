/**
 * 多标签的状态机（#13）。
 *
 * 抽成纯函数是因为这里的 bug 都很隐蔽且难复现：
 * 关掉中间标签后 activeId 指向已删除的标签、切主机时忘记清 resume、
 * 重复点同一台主机开出两个标签——这些在真机上要靠特定点击顺序才触发，
 * 但每一条都是「用户看到空白终端」级别的问题。
 *
 * 关键约定：tab.id 就是 hostId（当前一个主机一个标签）。
 * 用 hostId 当标识让「同主机去重」变成一行判断，也天然避免了
 * 同一台机器开出两个看不出区别的标签。
 */
import type { SshHost, SshSessionInfo } from './ssh'

export interface SshTab {
  /** 与 hostId 相同 */
  id: string
  host: SshHost
  /** 要接回的既有会话（关窗重开 / 该主机已有残留会话时用） */
  resume: SshSessionInfo | null
  /**
   * 本次由前端发起连接拿到的 sessionId。
   *
   * 与 resume 分开存：resume 是「接手别人（上一个窗口）开的会话」，
   * 这个字段是「我刚建的」。关标签时两个都可能有用，得都留着。
   */
  sessionId: string
  /** 正在连接（用于标签上的转圈） */
  busy: boolean
}

export function findTab(tabs: SshTab[], tabId: string): SshTab | undefined {
  return tabs.find(t => t.id === tabId)
}

/** 标签当前持有的会话 id：优先用自己建的，其次用接回的。 */
export function tabSessionId(tab: SshTab): string {
  return tab.sessionId || tab.resume?.id || ''
}

/**
 * 打开或聚焦某台主机的标签。
 *
 * 已存在 → 只返回 activeId，不新建（避免同主机多标签）。
 * 不存在 → 追加到末尾。
 */
export function openTab(
  tabs: SshTab[],
  host: SshHost,
  resume: SshSessionInfo | null = null,
): { tabs: SshTab[]; activeId: string } {
  if (findTab(tabs, host.id)) {
    return { tabs, activeId: host.id }
  }
  const tab: SshTab = { id: host.id, host, resume, sessionId: '', busy: false }
  return { tabs: [...tabs, tab], activeId: host.id }
}

/**
 * 关闭标签，返回新的 tabs 与应当聚焦的标签 id。
 *
 * 聚焦规则（关中间标签最容易错的地方）：
 *   - 关掉的是当前标签 → 优先聚焦**右边**的（符合用户视线移动方向），
 *     右边没有了才退到左边。
 *   - 关掉的不是当前标签 → 当前标签保持不动。
 *
 * 不做「关掉就断开」的判断：断连由 SshPage 在拿到返回值后按 sessionId 去调
 * Disconnect，这里只管标签数组，职责单一才不会漏掉一半。
 */
export function closeTab(tabs: SshTab[], tabId: string, activeId: string): {
  tabs: SshTab[]
  activeId: string
  closed: SshTab | null
} {
  const idx = tabs.findIndex(t => t.id === tabId)
  if (idx === -1) return { tabs, activeId, closed: null }
  const closed = tabs[idx]
  const rest = tabs.filter(t => t.id !== tabId)

  if (tabId !== activeId) {
    return { tabs: rest, activeId, closed }
  }
  // 当前标签被关：优先右边，其次左边，都没有则空。
  const next = rest[idx] ?? rest[idx - 1] ?? null
  return { tabs: rest, activeId: next ? next.id : '', closed }
}

/** 合并某个标签的字段变化（连接成功回填 sessionId、busy 翻转等）。 */
export function patchTab(tabs: SshTab[], tabId: string, patch: Partial<SshTab>): SshTab[] {
  return tabs.map(t => (t.id === tabId ? { ...t, ...patch } : t))
}

/**
 * 同步主机信息：用户在表单里改了名字/环境，已打开的标签要跟着更新，
 * 否则标签上还是旧名字，直到用户关掉重开才变。
 */
export function syncHost(tabs: SshTab[], hosts: SshHost[]): SshTab[] {
  const byId = new Map(hosts.map(h => [h.id, h]))
  let changed = false
  const next = tabs.map(t => {
    const fresh = byId.get(t.id)
    if (!fresh || fresh === t.host) return t
    changed = true
    return { ...t, host: fresh }
  })
  return changed ? next : tabs
}

/**
 * 从 Go 侧的会话列表里，给某台主机找一条可接回的会话。
 *
 * 用途：关窗重开后主机可能已经挂着会话（用户之前开着）；或标签被异常清理掉
 * 但会话还在。若不接回就 Connect，会多开一个 channel 并让旧会话变成孤儿。
 *
 * 取**最早**的一条：ListSessions 已按 started 正序，用户最先开的那条
 * 通常才是他还想继续用的（后面那些可能是误开的）。
 *
 * 单独抽出来而不是遍历整个列表：调用点在「点主机」这种高频路径上，
 * 而且语义上「找一条」比「批量挂载」更准确 —— 后者会误伤已绑定的标签。
 */
export function findResumable(
  sessions: SshSessionInfo[],
  hostId: string,
  isRetained: (s: SshSessionInfo) => boolean,
): SshSessionInfo | null {
  // 空 hostId 必须直接拒：会话列表里可能残留 hostId 为空的条目
  // （认不出主人的孤儿），拿它当「可接回」会把会话接到错误的标签上。
  if (!hostId) return null
  let best: SshSessionInfo | null = null
  for (const s of sessions) {
    if (s.hostId !== hostId || !isRetained(s)) continue
    if (!best || s.started < best.started) best = s
  }
  return best
}
