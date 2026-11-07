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
  // 场景 / 集合用「类型 + 条目 id」，新建动作用 kind：否则同名场景只能留一条，
  // 且 best match 会把两条新建动作按 label 之外的键算成重复。
  if (r.navTarget) return r.type + ':' + r.navTarget
  if (r.createKind) return 'create:' + r.createKind
  return r.label
}

/**
 * 把「用户明确指了它」的高相关结果提到最前面的「最佳匹配」分组，并原地把被提走的
 * 条目从各自原分组里删掉。无命中时不动 groups。
 *
 * 资格判定不能只看裸分数（2026-11-03）：
 *   - 72 分的内容嗅探命中减去插件惩罚 3 就掉到 70 以下，等于把「粘 JSON 命中插件」
 *     这条通道整个关掉；
 *   - 反过来，75 分的「标题前缀」是顺手扫到的命中，敲单字母时能把最佳匹配塞满
 *     一堆不相干的插件。
 * 所以插件（带 matchType 的结果）按**命中类型**取资格，非插件结果仍按分数。
 *
 * 刻意排除 clipboard-action：它是同一件事的旁路动作（打开/存为/编码），进最佳匹配
 * 反而把「打开链接」重复三遍。
 *
 * calculator 不排除：`=1+1` 那条走 stop 提前返回，压根到不了这里；能到这里的只有
 * 单位换算（94 分）—— 用户敲「10km to mi」要的就是这个现算出来的结果，它必须排在
 * 插件的同类命令之前。
 */
const BEST_MATCH_PENALTY: Record<string, number> = { plugin: 3 }

// 有资格进「最佳匹配」的命中类型：都是「用户明确指了这条」的口径。
// 没列的（prefix 标题前缀 / contains 标题包含 / pinyin / fuzzy / desc …）留在各自分组里。
const BEST_MATCH_TYPES = new Set([
  'exact', 'keyword', 'alias', 'match pattern', 'pattern short', 'slash', 'content',
])

// 插件排序轻惩罚只影响最佳匹配**内部**的先后（保住「应用/条目排在插件之前」的体感），
// 不参与资格判定 —— 否则 72 分的 content 命中永远进不来。
//
// 排序基准优先取 matcher 算好的 rankScore（含频次加权），没有再回退裸分：
// 否则「组内按加权排、best 按裸分排」会让同一条命令在两处的位次不一致。
function bestMatchWeight(r: SearchResult): number {
  return (r.rankScore ?? (r.score || 0)) - (BEST_MATCH_PENALTY[r.type] ?? 0)
}

function bestMatchEligible(r: SearchResult): boolean {
  if ((r.score || 0) < 70) return false
  if (r.matchType) return BEST_MATCH_TYPES.has(r.matchType)
  return true
}

export function injectBestMatch(groups: ResultGroup[], t: (k: string, n?: any) => string): void {
  const bestMatch: SearchResult[] = []
  for (const g of groups) {
    if (g.type === 'clipboard-action') continue
    for (const r of g.results) {
      if (bestMatchEligible(r)) bestMatch.push(r)
    }
  }
  if (bestMatch.length === 0) return

  bestMatch.sort((a, b) => bestMatchWeight(b) - bestMatchWeight(a))
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
  // 场景 / 集合紧跟项目条目：同属工作空间，敲名字时该挨在一起
  scene: 5.2,
  collection: 5.4,
  'quicklink-inline': 6,
  todo: 6.5,
  note: 7,
  system: 8,
  env: 8.5,
  // 新建动作是明确的动作，排在实体结果之后、插件之前
  create: 8.7,
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
