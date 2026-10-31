// 命令面板检索的「分组匹配器」。
//
// 每个 matcher 只回答一个问题：「这个类别的数据里有哪些命中一行 query」。
// 它们不持有状态、不碰 Vue ref（数据与回调都由 MatchCtx 显式传入），
// 因此可以逐条单测，也不会因为某个类别的改动牵动别的类别。
//
// 之前的形态是一个 395 行的 groupedResults computed，里面 12 段循环依次内联，
// 共用一个 seen 去重集。现在把 12 段搬到这里，编排（顺序、去重集、best-match
// 注入、分组排序）留在 useCommandSearch。
//
// 关键约束：**seen 是跨 matcher 共享的**，所以调用顺序有意义 —— 同一个条目
// 先被哪个 matcher 认领，就只出现在那个分组里。MATCHERS 的数组顺序即执行顺序，
// 与原先的内联顺序逐条对齐，不要随意重排。

import { Puzzle, Play, Power, Gauge, ListChecks } from '@lucide/vue'
import { commandTitle, pluginName } from '../../utils/localize'
import { evaluate, format, convertExpression } from '../../utils/calc'
import type { CollectionItem, EnvRuntimeInfo, EnvServiceStatus } from '../../types'
import type { PluginCmdIndex } from '../usePluginIndex'
import { ENV_GROUP_ALIASES, ENV_RUNTIME_ALIASES, todoSubtitle } from './types'
import type { CmdNote, CmdTodo, LcIndex, NavSearchItem, ResultGroup, SearchResult } from './types'

export interface MatchCtx {
  q: string
  qLC: string
  /** 跨 matcher 共享的去重集：同一实体只归属第一个认领它的分组 */
  seen: Set<string>
  t: (key: string, n?: any) => string
  locale: string
  /** 预计算的小写化索引（与 query 无关，见 types.ts） */
  lc: LcIndex
  backendItems: CollectionItem[]
  backendNotes: CmdNote[]
  backendTodos: CmdTodo[]
  fileResults: SearchResult[]
  clipboardUrlSource: string
  /** 「快速导航」候选（registry 里带 component 的内容页，已由编排层筛好） */
  navItems: NavSearchItem[]
  pluginCmdIndex: PluginCmdIndex[]
  envRuntimes: EnvRuntimeInfo[]
  envStatuses: Record<string, EnvServiceStatus>
  envMgmt: Record<string, boolean>
  pluginIcons: Record<string, string>
  frecencyScore: (key: string) => number
  pinyinMatch: (text: string, queryLC: string, cacheKey?: string) => boolean
  itemIcon: (item: CollectionItem) => any
  appIcon: (name: string) => any
  calcPluginScore: (idx: PluginCmdIndex, q: string, qLC: string) => { score: number; matchType: string; inlineInput?: string }
}

export interface MatchOutcome {
  groups: ResultGroup[]
  /** 命中后终止后续 matcher（如 `=1+1` 算出来了就别再去搜条目） */
  stop?: boolean
}

export type Matcher = (ctx: MatchCtx) => MatchOutcome | null

// 0. 单位换算（「10km to mi」这类），与 1 共用 calculator 分组
export function matchConvert(ctx: MatchCtx): MatchOutcome | null {
  const conv = convertExpression(ctx.q)
  if (!conv) return null
  return {
    groups: [{
      type: 'calculator',
      label: ctx.t('cmdGroupCalc'),
      results: [{ type: 'calculator', label: conv.text, desc: ctx.t('calcHint'), calcResult: conv.text }],
    }],
  }
}

// 1. 计算器（`=` 前缀）
export function matchCalculator(ctx: MatchCtx): MatchOutcome | null {
  if (!ctx.q.startsWith('=')) return null
  try {
    const result = evaluate(ctx.q.slice(1))
    const groups: ResultGroup[] = []
    if (result !== undefined && result !== null) {
      groups.push({
        type: 'calculator',
        label: ctx.t('cmdGroupCalc'),
        results: [{
          type: 'calculator',
          label: `${ctx.q} = ${format(result, { precision: 14 })}`,
          desc: ctx.t('calcHint'),
          calcResult: String(result),
        }],
      })
    }
    // 只要 evaluate 没抛异常就终止：`=abc` 算不出东西也不该继续去搜条目
    // （这是原实现的行为，`return groups` 在 if 之外）
    return { groups, stop: true }
  } catch {
    // 表达式非法：当作普通查询继续往下走
    return null
  }
}

// 2. URL 检测
export function matchUrl(ctx: MatchCtx): MatchOutcome | null {
  const { q } = ctx
  let isUrl = false
  let urlStr = q
  if (/^https?:\/\//i.test(q)) {
    isUrl = true
  } else if (/^[a-z0-9][-a-z0-9]*\.[a-z]{2,}(\/|$)/i.test(q) && !/^[\d+\-*/().%^, ]+$/.test(q)) {
    isUrl = true
    urlStr = 'https://' + q
  }
  if (!isUrl) return null
  return {
    groups: [{
      type: 'url',
      label: '🌐 ' + ctx.t('cmdGroupWeb'),
      results: [{ type: 'url', label: ctx.t('cmdOpenUrl'), desc: urlStr, url: urlStr }],
    }],
  }
}

// 2b. 快速导航（软件内部页面）：回车跳到主窗口对应页，并让它成为选中态。
//
// 只收「带 component」的内容页，ssh / dsh 因此天然被排除 —— 它们是唤起独立窗口的
// 入口（registry 里只有 activate），面板里选它们等于「什么都没发生」：主窗口内容区
// 不动，用户也不知道窗口开没开。**排除规则不写死 id**：将来新增窗口入口自动跟着排除。
// 过滤在编排层做（ctx.navItems 已筛好）：matcher 不 import registry —— 后者静态引了
// WorkspacePage.vue，会把整个 SFC 编译链拖进这个纯函数模块（单测环境编译不了 SFC）。
//
// 匹配文本只有两项：本地化后的页面名 + registry 的英文 id（面板里也能敲英文）。
export function matchNav(ctx: MatchCtx): MatchOutcome | null {
  const { qLC, seen } = ctx
  const navResults: SearchResult[] = []

  for (const e of ctx.navItems) {
    const k = 'nav:' + e.id
    if (seen.has(k)) continue
    const label = ctx.t(e.labelKey)
    const labelLC = label.toLowerCase()
    const idLC = e.id.toLowerCase()
    const score =
      labelLC === qLC ? 100
        : labelLC.startsWith(qLC) || idLC.startsWith(qLC) ? 75
          : labelLC.includes(qLC) || idLC.includes(qLC) ? 60
            : ctx.pinyinMatch(label, qLC, k) ? 50 : 0
    if (!score) continue
    seen.add(k)
    navResults.push({
      type: 'nav',
      label,
      desc: ctx.t('cmdNavHint'),
      icon: e.icon,
      navPage: e.id,
      score,
    })
  }

  if (navResults.length === 0) return null
  navResults.sort((a, b) => (b.score || 0) - (a.score || 0))
  return { groups: [{ type: 'nav', label: ctx.t('cmdGroupNav'), results: navResults }] }
}

// 3. 项目条目（含后端 FTS 兜底）
export function matchItems(ctx: MatchCtx): MatchOutcome | null {
  const { qLC, seen } = ctx
  const itemResults: SearchResult[] = []

  for (const e of ctx.lc.items) {
    const item = e.item
    if (seen.has(item.id)) continue
    if (!(e.nameLC.includes(qLC) || e.valueLC.includes(qLC) || ctx.pinyinMatch(item.name, qLC, 'i:' + item.id))) continue
    seen.add(item.id)
    const isQuicklink = !!item.value && item.value.includes('{query}')
    const itemScore =
      e.nameLC === qLC ? 100
        : e.nameLC.startsWith(qLC) ? 75
          : e.nameLC.includes(qLC) ? 60
            : e.valueLC.includes(qLC) ? 55 : 0
    itemResults.push({
      type: isQuicklink ? 'quicklink' : 'item',
      label: item.name,
      desc: item.value || '',
      icon: ctx.itemIcon(item),
      iconBase64: item.icon || undefined,
      item,
      frecencyScore: ctx.frecencyScore('item:' + item.id),
      score: itemScore,
    })
  }

  // 3a. 后端 FTS 兜底：本地池上限之外的条目（见 useCommandSearch 里 backendItems 注释）。
  // 后端已经筛过了，不再做二次匹配。
  for (const item of ctx.backendItems) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    const isQuicklink = !!item.value && item.value.includes('{query}')
    itemResults.push({
      type: isQuicklink ? 'quicklink' : 'item',
      label: item.name,
      desc: item.value || '',
      icon: ctx.itemIcon(item),
      iconBase64: item.icon || undefined,
      item,
      frecencyScore: ctx.frecencyScore('item:' + item.id),
      score: 55,
    })
  }

  // 应用类型条目优先；同权重时按相关度与频次排序
  itemResults.sort((a, b) => {
    const wa = (a.item?.type === '应用' ? 1000 : 0) + (a.score || 0) + Math.min(30, a.frecencyScore || 0)
    const wb = (b.item?.type === '应用' ? 1000 : 0) + (b.score || 0) + Math.min(30, b.frecencyScore || 0)
    return wb - wa
  })
  if (itemResults.length === 0) return null
  return { groups: [{ type: 'item', label: ctx.t('cmdGroupItems'), results: itemResults }] }
}

// 3b. Quicklink 内联（把 query 灌进 {query} 占位符直接执行）
export function matchQuicklinkInline(ctx: MatchCtx): MatchOutcome | null {
  const { qLC, seen } = ctx
  if (qLC.length <= 1 || /^\d+$/.test(qLC)) return null

  const qlResults: SearchResult[] = []
  for (const e of ctx.lc.items) {
    const item = e.item
    if (!item.value || !item.value.includes('{query}')) continue
    // 注意：这里**不**往 seen 里写。条目分组若已认领就跳过；若没命中（query 不匹配
    // 条目名），它仍可以以「把 query 填进模板」的形式出现。
    if (seen.has(item.id)) continue
    const resolved = item.value.replace(/\{query\}/g, qLC)
    qlResults.push({
      type: 'quicklink-inline',
      label: item.name,
      desc: resolved,
      icon: ctx.itemIcon(item),
      iconBase64: item.icon || undefined,
      item,
      inlineQuery: qLC,
    })
  }
  if (qlResults.length === 0) return null
  return { groups: [{ type: 'quicklink-inline', label: ctx.t('cmdGroupQuicklink'), results: qlResults.slice(0, 3) }] }
}

// 4. 笔记（含后端全文兜底）
export function matchNotes(ctx: MatchCtx): MatchOutcome | null {
  const { qLC, seen } = ctx
  const noteResults: SearchResult[] = []

  for (const e of ctx.lc.notes) {
    const n = e.note
    if (!(e.nameLC.includes(qLC) || e.contentLC.includes(qLC) || ctx.pinyinMatch(n.name || n.keyword || '', qLC, 'n:' + n.id))) continue
    if (seen.has('note-' + n.id)) continue
    seen.add('note-' + n.id)
    const score =
      e.nameLC === qLC ? 100
        : e.nameLC.startsWith(qLC) ? 75
          : e.nameLC.includes(qLC) ? 60
            : e.contentLC.includes(qLC) ? 55 : 0
    noteResults.push({
      type: 'note',
      label: n.name || n.keyword,
      desc: (n.content || '').slice(0, 80),
      note: n,
      frecencyScore: ctx.frecencyScore('note:' + n.id),
      score,
    })
  }

  // 4a. 后端全文兜底：内容/标签命中而本地扫描漏掉的笔记（本地只扫名称 + 拼音）。
  for (const n of ctx.backendNotes) {
    if (seen.has('note-' + n.id)) continue
    seen.add('note-' + n.id)
    const nameLC = (n.name || n.keyword || '').toLowerCase()
    noteResults.push({
      type: 'note',
      label: n.name || n.keyword,
      desc: (n.content || '').slice(0, 80),
      note: n,
      frecencyScore: ctx.frecencyScore('note:' + n.id),
      score: nameLC.includes(qLC) ? 60 : 55,
    })
  }

  noteResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
  if (noteResults.length === 0) return null
  return { groups: [{ type: 'note', label: ctx.t('cmdGroupNotes'), results: noteResults }] }
}

// 4b. 待办（唯一来源就是后端检索：本地没有待办池）
export function matchTodos(ctx: MatchCtx): MatchOutcome | null {
  const { qLC, seen } = ctx
  const todoResults: SearchResult[] = []
  for (const td of ctx.backendTodos) {
    if (seen.has('todo-' + td.id)) continue
    seen.add('todo-' + td.id)
    const titleLC = (td.title || '').toLowerCase()
    const score = titleLC === qLC ? 100 : titleLC.startsWith(qLC) ? 75 : titleLC.includes(qLC) ? 60 : 50
    todoResults.push({
      type: 'todo',
      label: td.title,
      icon: ListChecks,
      desc: todoSubtitle(td, ctx.t),
      todo: td,
      score,
    })
  }
  todoResults.sort((a, b) => (b.score || 0) - (a.score || 0))
  if (todoResults.length === 0) return null
  return { groups: [{ type: 'todo', label: ctx.t('cmdGroupTodos'), results: todoResults }] }
}

// 5. 已安装应用
export function matchApps(ctx: MatchCtx): MatchOutcome | null {
  const { qLC } = ctx
  const appResults: SearchResult[] = []

  for (const e of ctx.lc.apps) {
    const app = e.app
    // 别名的拼音必须给 cacheKey：原先这里裸调 pinyinMatch(a, qLC)，等于每次
    // query 变化都把所有应用的别名重新转一遍拼音 —— 别名是静态的，白烧。
    const aliasMatch = e.aliases.some((al, i) => al.lc.includes(qLC) || ctx.pinyinMatch(al.raw, qLC, 'alias:' + app.name + '#' + i))
    if (!(e.nameLC.includes(qLC) || ctx.pinyinMatch(app.name, qLC, 'a:' + app.name) || aliasMatch)) continue
    const appScore =
      e.nameLC === qLC ? 100
        : e.nameLC.startsWith(qLC) ? 75
          : aliasMatch ? 60
            : e.nameLC.includes(qLC) ? 60 : 0
    appResults.push({
      type: 'app',
      label: app.name,
      desc: app.category !== '其他' && app.category !== '系统工具' ? app.category : app.path,
      icon: ctx.appIcon(app.name),
      iconBase64: app.iconBase64,
      appPath: app.path,
      appCategory: app.category,
      frecencyScore: ctx.frecencyScore('app:' + app.name),
      score: appScore,
    })
  }

  appResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
  if (appResults.length === 0) return null
  return { groups: [{ type: 'app', label: ctx.t('cmdGroupApps'), results: appResults.slice(0, 8) }] }
}

// 6. 系统命令
export function matchSystem(ctx: MatchCtx): MatchOutcome | null {
  const { qLC } = ctx
  const sysResults: SearchResult[] = []

  for (const e of ctx.lc.sys) {
    const cmd = e.cmd
    if (!(cmd.keywords.some(k => k.includes(qLC)) || e.labelLC.includes(qLC) || ctx.pinyinMatch(cmd.label, qLC, 'sys:' + cmd.id))) continue
    const sysScore =
      e.labelLC === qLC ? 100
        : e.labelLC.startsWith(qLC) ? 75
          : cmd.keywords.some(k => k === qLC) ? 90
            : cmd.keywords.some(k => k.startsWith(qLC)) ? 65
              : e.labelLC.includes(qLC) ? 60 : 0
    sysResults.push({ type: 'system', label: cmd.label, desc: cmd.desc, icon: cmd.icon, cmd, score: sysScore })
  }

  if (sysResults.length === 0) return null
  return { groups: [{ type: 'system', label: ctx.t('cmdGroupSystem'), results: sysResults }] }
}

// 7. 插件命令
export function matchPlugins(ctx: MatchCtx): MatchOutcome | null {
  const { q, qLC } = ctx
  const pluginResults: SearchResult[] = []

  for (const idx of ctx.pluginCmdIndex) {
    const { score, matchType, inlineInput } = ctx.calcPluginScore(idx, q, qLC)
    if (score <= 0) continue
    pluginResults.push({
      type: 'plugin',
      label: inlineInput ? `${commandTitle(idx.cmd, ctx.locale)}: ${inlineInput}` : commandTitle(idx.cmd, ctx.locale),
      desc: pluginName(idx.plugin, ctx.locale)
        + (idx.cmd.hotkey ? `  ${idx.cmd.hotkey}` : '')
        + (idx.notRunning ? `  · ${ctx.t('pluginNotRunning')}` : ''),
      icon: Puzzle,
      iconBase64: ctx.pluginIcons[idx.plugin.id],
      pluginId: idx.plugin.id,
      pluginCommandId: idx.cmd.id,
      pluginHasFrontend: idx.plugin.hasFrontend,
      pluginNotRunning: idx.notRunning,
      acceptsInput: idx.cmd.acceptsInput,
      inlineInput,
      score,
      matchType,
      frecencyScore: ctx.frecencyScore('plugin:' + idx.plugin.id + '.' + idx.cmd.id),
    })
  }

  pluginResults.sort((a, b) => {
    const sa = (a.score || 0) * 0.7 + Math.min(30, a.frecencyScore || 0) * 0.3
    const sb = (b.score || 0) * 0.7 + Math.min(30, b.frecencyScore || 0) * 0.3
    return sb - sa
  })
  if (pluginResults.length === 0) return null
  return { groups: [{ type: 'plugin', label: ctx.t('cmdGroupPlugins'), results: pluginResults }] }
}

// 7.5 环境管理运行时服务（启动 / 停止 / 启用管理后台 15672）
// 仅在 query 命中时展示：避免面板一开就铺满服务项（空 query 时编排层已提前返回）。
export function matchEnv(ctx: MatchCtx): MatchOutcome | null {
  const { qLC, t } = ctx
  const envKeyOf = (id: string, v: string) => id + ':' + v
  const statuses = ctx.envStatuses
  const envResults: SearchResult[] = []
  const startVerb = /启动|start|运行|开服|拉起/.test(qLC)
  const stopVerb = /停止|stop|停服|关停|关掉/.test(qLC)
  const mgmtVerb = /15672|管理后台|management|mgmt|管理面板/.test(qLC)

  for (const r of ctx.envRuntimes) {
    if (!r.hasService || r.installed.length === 0) continue
    // 运行中的版本（按已装版本逐一比对状态）
    const running = r.installed.find(ins => (ins.scope === 'linked' && r.id !== 'php') ? false : statuses[envKeyOf(r.id, ins.version)]?.running)
    const runningVersion = running?.version
    const startVersion = r.installed.find(i => i.active)?.version || r.installed[0].version
    const nameLC = r.name.toLowerCase()
    const idLC = r.id.toLowerCase()
    const aliasHit = (ENV_RUNTIME_ALIASES[r.id] || []).concat(ENV_GROUP_ALIASES[r.group] || []).some(a => {
      const aLC = a.toLowerCase()
      return aLC.includes(qLC) || ctx.pinyinMatch(a, qLC)
    })
    const nameHit = nameLC.includes(qLC) || idLC.includes(qLC) || ctx.pinyinMatch(r.name, qLC) || aliasHit
    if (!nameHit && !startVerb && !stopVerb && !mgmtVerb) continue

    const baseScore = nameLC === qLC ? 100 : idLC === qLC ? 95
      : nameLC.startsWith(qLC) ? 80 : idLC.startsWith(qLC) ? 75 : nameHit ? 60 : 50

    if (!runningVersion && (nameHit || startVerb)) {
      envResults.push({
        type: 'env',
        label: t('envStart') + ' ' + r.name,
        desc: startVersion + ' · ' + t('envNotRunning'),
        icon: Play,
        envRuntimeId: r.id,
        envVersion: startVersion,
        envAction: 'start',
        score: baseScore,
      })
    }
    if (runningVersion && (nameHit || stopVerb)) {
      envResults.push({
        type: 'env',
        label: t('envStop') + ' ' + r.name,
        desc: runningVersion + ' · ' + t('envRunning'),
        icon: Power,
        envRuntimeId: r.id,
        envVersion: runningVersion,
        envAction: 'stop',
        score: baseScore,
      })
    }
    // RabbitMQ 专属：运行中对节点启用/关闭管理后台插件（rabbitmq_management，端口 15672）
    if (r.id === 'rabbitmq' && runningVersion && (nameHit || mgmtVerb || (idLC === 'rabbitmq' && startVerb))) {
      const mgmtOn = ctx.envMgmt[runningVersion] === true
      envResults.push({
        type: 'env',
        label: mgmtOn ? t('rabbitmqDisableMgmt') : t('envEnableMgmt'),
        desc: runningVersion + ' · http://127.0.0.1:15672/',
        icon: Gauge,
        envRuntimeId: r.id,
        envVersion: runningVersion,
        envAction: 'mgmt',
        score: baseScore + 5,
      })
    }
  }

  if (envResults.length === 0) return null
  return { groups: [{ type: 'env', label: ctx.t('cmdGroupEnv'), results: envResults }] }
}

// 7.6 Everything 文件搜索（效率工具）
// fileResults 由 useCommandSearch 的 130ms 防抖异步填充，命中后编排层随 ref 变化自动重算。
export function matchFiles(ctx: MatchCtx): MatchOutcome | null {
  const { seen } = ctx
  const hits = ctx.fileResults.filter((r) => {
    const k = 'file:' + (r.filePath || r.label)
    if (seen.has(k)) return false
    seen.add(k)
    return true
  })
  if (hits.length === 0) return null
  return { groups: [{ type: 'file', label: ctx.t('cmdGroupFiles'), results: hits.slice(0, 20) }] }
}

// 剪贴板智能路由：剪贴板里是 URL 且用户原样敲了它 → 给三个动作。
// 不进 MATCHERS：它要插到列表最前面（unshift），由编排层单独调用。
export function matchClipboard(ctx: MatchCtx): ResultGroup[] {
  const src = ctx.clipboardUrlSource
  if (!src || ctx.q !== src) return []
  const suggest: SearchResult[] = [
    { type: 'clipboard-action', clipAction: 'open-url', label: ctx.t('cmdOpenUrl'), desc: src, url: src, score: 100 },
    { type: 'clipboard-action', clipAction: 'save-url', label: ctx.t('cmdSaveAsItem'), desc: src, url: src, score: 99 },
    { type: 'clipboard-action', clipAction: 'encode-url', label: ctx.t('cmdEncodeUrl'), desc: ctx.t('cmdEncodeUrlDesc'), url: src, score: 98 },
  ]
  return [{ type: 'clipboard-action', label: ctx.t('cmdGroupClipboard'), results: suggest }]
}

// 执行顺序 = 原 groupedResults 内联顺序。**共享 seen 使其有语义**，勿随意重排。
export const MATCHERS: Matcher[] = [
  matchConvert,
  matchCalculator,
  matchUrl,
  matchNav,
  matchItems,
  matchQuicklinkInline,
  matchNotes,
  matchTodos,
  matchApps,
  matchSystem,
  matchPlugins,
  matchEnv,
  matchFiles,
]
