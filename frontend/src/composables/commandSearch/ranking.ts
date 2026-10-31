// 命令面板结果的分组编排：最佳匹配置顶、跨组去重、分组优先级排序。
//
// 这三件事都必须拿到「所有分组的并集」才能做对，所以留在编排侧而不进 matcher：
// 去重要跨组删重复项，排序要同时看到所有 type。

import type { ResultGroup, SearchResult } from './types'

// 同一实体的稳定标识：最佳匹配置顶后，要把这些条目从原分组里摘掉，
// 否则同一条目会同时出现在「最佳匹配」和它自己的分组里。
function dedupKey(r: SearchResult): string {
  if (r.pluginId && r.pluginCommandId) return 'plugin:' + r.pluginId + '.' + r.pluginCommandId
  if (r.item?.id) return 'item:' + r.item.id
  if (r.note?.id) return 'note:' + r.note.id
  if (r.todo?.id) return 'todo:' + r.todo.id
  if (r.cmd?.id) return 'cmd:' + r.cmd.id
  if (r.appPath) return 'app:' + r.appPath
  return r.label
}

/**
 * 把高相关度结果（score ≥ 70）提到最前面的「最佳匹配」分组，并原地把被提走的
 * 条目从各自原分组里删掉。无命中时不动 groups。
 *
 * 刻意排除三类：calculator / clipboard-action 是特殊交互项，plugin 恒排最后
 * （插件不得出现在应用之前）。
 */
export function injectBestMatch(groups: ResultGroup[], t: (k: string, n?: any) => string): void {
  const bestMatch: SearchResult[] = []
  for (const g of groups) {
    if (g.type === 'calculator' || g.type === 'clipboard-action' || g.type === 'plugin') continue
    for (const r of g.results) {
      if ((r.score || 0) >= 70) bestMatch.push(r)
    }
  }
  if (bestMatch.length === 0) return

  bestMatch.sort((a, b) => (b.score || 0) - (a.score || 0))
  const show = bestMatch.slice(0, 6)
  const dedupIds = new Set(show.map(dedupKey))
  // 去重作用于所有分组：app/note/system/url 命中 ≥70 时也会进入最佳匹配，
  // 若只在 plugin/item 分组里过滤，它们仍会同时出现在两组中造成重复。
  for (const g of groups) {
    g.results = g.results.filter(r => !dedupIds.has(dedupKey(r)))
  }
  groups.unshift({ type: 'best', label: t('cmdBestMatch'), results: show })
}

// 分组的固定展示顺序。没列出的 type 落到 99（末尾）。
//
// file 恒排最后：Everything 一次能命中几十条，夹在中间会把下面的插件/系统命令全推走。
// plugin 因此从「最后」退到倒数第二。
const GROUP_PRIORITY: Record<string, number> = {
  best: 0,
  'clipboard-action': 1,
  calculator: 2,
  url: 3,
  // 「快速导航」排在应用之前：Ctrl+K 里敲页面名时，用户要的是软件内部的那一个页，
  // 而不是同名的已安装应用。
  nav: 3.5,
  app: 4,
  item: 5,
  'quicklink-inline': 6,
  todo: 6.5,
  note: 7,
  system: 8,
  env: 8.5,
  plugin: 9,
  file: 9.5,
}

/**
 * 丢掉空分组。
 *
 * 必须放在 injectBestMatch 之后：唯一命中的那一条被提到「最佳匹配」后，
 * 原分组会留下一个空壳，渲染出来就是「应用程序 0」这种看起来像坏了的标题。
 */
export function dropEmptyGroups(groups: ResultGroup[]): void {
  for (let i = groups.length - 1; i >= 0; i--) {
    if (groups[i].results.length === 0) groups.splice(i, 1)
  }
}

/** 按固定优先级原地排序（JS sort 稳定，同优先级保持入列顺序） */
export function sortGroups(groups: ResultGroup[]): void {
  groups.sort((a, b) => (GROUP_PRIORITY[a.type] ?? 99) - (GROUP_PRIORITY[b.type] ?? 99))
}
