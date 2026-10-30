/**
 * 多标签的状态机（#13）。
 *
 * 抽成纯函数是因为这里的 bug 都很隐蔽且难复现：
 * 关掉中间标签后 activeId 指向已删除的标签、切主机时忘记清 resume、
 * 同一台主机开出多个标签后 id 撞车——这些在真机上要靠特定点击顺序才触发，
 * 但每一条都是「用户看到空白终端」级别的问题。
 *
 * 关键约定：tab.id 与 hostId 解耦。
 * 现在允许同一台主机开多个标签（比如一个跑更新、一个看日志/比对配置），
 * 所以每个标签有自己全局唯一的 id（hostId::序号），hostId 只用来归组与断连时匹配。
 * 「点主机时聚焦已有标签而非新建」由 openOrFocus 负责，避免手滑多点开一堆孤儿会话。
 */
import type { SshHost, SshSessionInfo } from './ssh'

export interface SshTab {
  /** 全局唯一，形如 `${hostId}::${序号}`，不再等于 hostId */
  id: string
  /** 所属主机。一个主机可对应多个标签 */
  hostId: string
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

let tabSeq = 0

/** 生成全局唯一的标签 id。
 *
 * 与 hostId 解耦：同一台主机现在可开多个标签，若 tab.id 仍等于 hostId，
 * 两个标签会撞 id，v-show 与 :key 全乱。前缀带 hostId 仅为调试可读。 */
export function makeTabId(hostId: string): string {
  tabSeq++
  return `${hostId}::${tabSeq}`
}

/**
 * 打开一个全新的标签（永远新建，不复用）。
 *
 * 同一主机的第二个标签靠它生成——每开一个就多一个独立会话（共享底层 TCP，
 * 但各自一个 channel）。要「点主机时聚焦已有标签而不是新建」，请用 openOrFocus。
 */
export function openTab(
  tabs: SshTab[],
  host: SshHost,
  resume: SshSessionInfo | null = null,
): { tabs: SshTab[]; activeId: string } {
  const tab: SshTab = { id: makeTabId(host.id), hostId: host.id, host, resume, sessionId: '', busy: false }
  return { tabs: [...tabs, tab], activeId: tab.id }
}

/**
 * 「点主机」时的语义：该主机已有标签 → 聚焦最近打开的那一个；否则新建。
 *
 * 保留聚焦逻辑是为了避免「手滑多点一下就多开一个标签」，也避免重复点同一台
 * 机器制造孤儿会话。想要第二个独立会话，调用方应直接走 openTab（复制会话）。
 */
export function openOrFocus(
  tabs: SshTab[],
  host: SshHost,
  resume: SshSessionInfo | null = null,
): { tabs: SshTab[]; activeId: string; opened: boolean } {
  let last: SshTab | undefined
  for (const t of tabs) {
    if (t.hostId === host.id) last = t
  }
  if (last) return { tabs, activeId: last.id, opened: false }
  const r = openTab(tabs, host, resume)
  return { tabs: r.tabs, activeId: r.activeId, opened: true }
}

/**
 * 关闭标签，返回新的 tabs 与应当聚焦的标签 id。
 *
 * 聚焦规则（关中间标签最容易错的地方）：
 *   - 关掉的是当前标签 → 优先聚焦右边的（符合用户视线移动方向），
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
    const fresh = byId.get(t.hostId)
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
 * 取最早的一条：ListSessions 已按 started 正序，用户最先开的那条
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
