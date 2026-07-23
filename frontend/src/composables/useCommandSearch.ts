import { ref, computed, type Ref, type ComputedRef } from 'vue'
import type { CollectionItem } from '../types'
import type { PluginCmdIndex } from './usePluginIndex'
import { evaluate, format, convertExpression } from '../utils/calc'
import { Puzzle } from '@lucide/vue'

// ---- Types ----
type ResultType = 'item' | 'system' | 'quicklink' | 'quicklink-inline' | 'calculator' | 'snippet' | 'app' | 'plugin' | 'url' | 'clipboard-action'

export interface SearchResult {
  type: ResultType
  label: string
  desc?: string
  icon?: any
  iconBase64?: string
  item?: CollectionItem
  cmd?: any
  calcResult?: string
  snippet?: CmdSnippet
  inlineQuery?: string
  frecencyScore?: number
  appPath?: string
  appCategory?: string
  pluginId?: string
  pluginCommandId?: string
  pluginHasFrontend?: boolean
  inlineInput?: string
  pluginResult?: string
  acceptsInput?: boolean
  score?: number
  matchType?: string
  url?: string
  clipAction?: string
}

interface CmdSnippet { id: string; keyword: string; content: string; category: string; createdAt: string }
interface SystemCmd { id: string; label: string; desc: string; keywords: string[]; icon: any; action: () => Promise<void> }
interface InstalledApp { name: string; path: string; category: string; iconBase64?: string }

interface ResultGroup {
  type: ResultType
  label: string
  results: SearchResult[]
}

export interface RecentEntry {
  key: string; type: string; label: string; description: string; input?: string; count: number; lastUsed: number
}

// ---- Dependencies type ----
export interface SearchDeps {
  items: Ref<CollectionItem[]>
  installedApps: Ref<InstalledApp[]>
  snippets: Ref<CmdSnippet[]>
  systemCommands: ComputedRef<SystemCmd[]>
  query: Ref<string>
  selectedIndex: Ref<number>
  pluginCmdIndex: Ref<PluginCmdIndex[]>
  pluginResultCache: Ref<{ result: string; pluginName: string; pluginId?: string; pluginCommandId?: string; pluginHasFrontend?: boolean; input?: string; acceptsInput?: boolean } | null>
  clipboardUrlSource: Ref<string>
  frecencyScore: (key: string) => number
  frecencyTick: Ref<number>
  calcPluginScore: (idx: PluginCmdIndex, q: string, qLC: string) => { score: number; matchType: string; inlineInput?: string }
  pinyinMatch: (text: string, queryLC: string, cacheKey?: string) => boolean
  appIcon: (name: string) => any
  getAppAliases: (name: string) => string[]
  itemIcon: (item: CollectionItem) => any
  t: (key: string) => string
  pluginIcons: Ref<Record<string, string>>
}

export function useCommandSearch(deps: SearchDeps) {
  const { items, installedApps, snippets, systemCommands, query, selectedIndex,
          pluginCmdIndex, pluginResultCache, clipboardUrlSource,
          frecencyScore, frecencyTick, calcPluginScore,
          pinyinMatch, appIcon, getAppAliases, itemIcon, t, pluginIcons } = deps

  const recentCache = ref<RecentEntry[]>([])

  const groupedResults = computed<ResultGroup[]>(() => {
    void frecencyTick.value
    const q = query.value.trim()
    if (!q) return []

    const qLC = q.toLowerCase()
    const groups: ResultGroup[] = []
    const seen = new Set<string>()

    // 0. 单位换算
    {
      const conv = convertExpression(q)
      if (conv) {
        groups.push({
          type: 'calculator',
          label: t('cmdGroupCalc'),
          results: [{ type: 'calculator' as ResultType, label: conv.text, desc: t('calcHint'), calcResult: conv.text }]
        })
      }
    }

    // 1. 计算器
    if (q.startsWith('=')) {
      try {
        const expr = q.slice(1)
        const result = evaluate(expr)
        if (result !== undefined && result !== null) {
          groups.push({
            type: 'calculator',
            label: t('cmdGroupCalc'),
            results: [{
              type: 'calculator' as ResultType,
              label: `${q} = ${format(result, { precision: 14 })}`,
              desc: t('calcHint'),
              calcResult: String(result),
            }]
          })
        }
        return groups
      } catch {
        // fall through
      }
    }

    // 2. URL 检测
    let isUrl = false
    let urlStr = q
    if (/^https?:\/\//i.test(q)) {
      isUrl = true
    } else if (/^[a-z0-9][-a-z0-9]*\.[a-z]{2,}(\/|$)/i.test(q) && !/^[\d+\-*/().%^, ]+$/.test(q)) {
      isUrl = true
      urlStr = 'https://' + q
    }
    if (isUrl) {
      groups.push({
        type: 'url',
        label: '🌐 ' + t('cmdGroupWeb'),
        results: [{ type: 'url' as ResultType, label: t('cmdOpenUrl'), desc: urlStr, url: urlStr }]
      })
    }

    // 3. 项目 + Quicklink
    const itemResults: SearchResult[] = []
    for (const item of items.value) {
      if (seen.has(item.id)) continue
      const nameLC = item.name.toLowerCase()
      const valueLC = (item.value || '').toLowerCase()
      if (!(nameLC.includes(qLC) || valueLC.includes(qLC) || pinyinMatch(item.name, qLC, 'i:' + item.id))) continue
      seen.add(item.id)
      const isQuicklink = item.value && item.value.includes('{query}')
      const itemScore = nameLC === qLC ? 100 : nameLC.startsWith(qLC) ? 75 : nameLC.includes(qLC) ? 60 : valueLC.includes(qLC) ? 55 : 0
      itemResults.push({
        type: isQuicklink ? 'quicklink' : 'item',
        label: item.name,
        desc: item.value || '',
        icon: itemIcon(item),
        iconBase64: item.icon || undefined,
        item,
        frecencyScore: frecencyScore('item:' + item.id),
        score: itemScore,
      })
    }
    itemResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
    if (itemResults.length > 0) {
      groups.push({ type: 'item', label: t('cmdGroupItems'), results: itemResults })
    }

    // 3b. Quicklink 内联
    if (qLC.length > 1 && !/^\d+$/.test(qLC)) {
      const qlResults: SearchResult[] = []
      for (const item of items.value) {
        if (!item.value || !item.value.includes('{query}')) continue
        if (seen.has(item.id)) continue
        const resolved = item.value.replace(/\{query\}/g, qLC)
        qlResults.push({
          type: 'quicklink-inline',
          label: item.name,
          desc: resolved,
          icon: itemIcon(item),
          iconBase64: item.icon || undefined,
          item,
          inlineQuery: qLC,
        } as SearchResult)
      }
      if (qlResults.length > 0) {
        groups.push({ type: 'quicklink-inline', label: t('cmdGroupQuicklink'), results: qlResults.slice(0, 3) })
      }
    }

    // 4. 文本片段
    const snippetResults: SearchResult[] = []
    for (const s of snippets.value) {
      const kid = s.keyword.toLowerCase()
      const cid = s.content.toLowerCase()
      if (kid.includes(qLC) || cid.includes(qLC) || pinyinMatch(s.keyword, qLC, 's:' + s.id)) {
        if (!seen.has('snippet-' + s.id)) {
          seen.add('snippet-' + s.id)
          const snippetScore = kid === qLC ? 100 : kid.startsWith(qLC) ? 75 : kid.includes(qLC) ? 60 : cid.includes(qLC) ? 55 : 0
          snippetResults.push({
            type: 'snippet',
            label: s.keyword,
            desc: s.content.slice(0, 80),
            snippet: s,
            frecencyScore: frecencyScore('snippet:' + s.id),
            score: snippetScore,
          })
        }
      }
    }
    snippetResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
    if (snippetResults.length > 0) {
      groups.push({ type: 'snippet', label: t('cmdGroupSnippets'), results: snippetResults })
    }

    // 5. 已安装应用
    const appResults: SearchResult[] = []
    for (const app of installedApps.value) {
      const nameLC = app.name.toLowerCase()
      const aliases = getAppAliases(app.name)
      const aliasMatch = aliases.some(a => a.toLowerCase().includes(qLC) || pinyinMatch(a, qLC))
      if (nameLC.includes(qLC) || pinyinMatch(app.name, qLC, 'a:' + app.name) || aliasMatch) {
        const appScore = nameLC === qLC ? 100 : nameLC.startsWith(qLC) ? 75 : aliasMatch ? 60 : nameLC.includes(qLC) ? 60 : 0
        appResults.push({
          type: 'app',
          label: app.name,
          desc: app.category !== '其他' && app.category !== '系统工具' ? app.category : app.path,
          icon: appIcon(app.name),
          iconBase64: app.iconBase64,
          appPath: app.path,
          appCategory: app.category,
          frecencyScore: frecencyScore('app:' + app.name),
          score: appScore,
        })
      }
    }
    appResults.sort((a, b) => (b.frecencyScore || 0) - (a.frecencyScore || 0))
    if (appResults.length > 0) {
      groups.push({ type: 'app', label: t('cmdGroupApps'), results: appResults.slice(0, 8) })
    }

    // 6. 系统命令
    const sysResults: SearchResult[] = []
    for (const cmd of systemCommands.value) {
      const labelLC = cmd.label.toLowerCase()
      if (cmd.keywords.some(k => k.includes(qLC)) || labelLC.includes(qLC) || pinyinMatch(cmd.label, qLC, 'sys:' + cmd.id)) {
        const sysScore = labelLC === qLC ? 100 : labelLC.startsWith(qLC) ? 75 : cmd.keywords.some(k => k === qLC) ? 90 : cmd.keywords.some(k => k.startsWith(qLC)) ? 65 : labelLC.includes(qLC) ? 60 : 0
        sysResults.push({ type: 'system', label: cmd.label, desc: cmd.desc, icon: cmd.icon, cmd, score: sysScore })
      }
    }
    if (sysResults.length > 0) {
      groups.push({ type: 'system', label: t('cmdGroupSystem'), results: sysResults })
    }

    // 7. 插件命令
    const pluginResults: SearchResult[] = []
    for (const idx of pluginCmdIndex.value) {
      const { score, matchType, inlineInput } = calcPluginScore(idx, q, qLC)
      if (score <= 0) continue
      pluginResults.push({
        type: 'plugin',
        label: inlineInput ? `${idx.cmd.title}: ${inlineInput}` : idx.cmd.title,
        desc: idx.plugin.name + (idx.cmd.hotkey ? `  ${idx.cmd.hotkey}` : ''),
        icon: Puzzle,
        iconBase64: pluginIcons.value[idx.plugin.id],
        pluginId: idx.plugin.id,
        pluginCommandId: idx.cmd.id,
        pluginHasFrontend: idx.plugin.hasFrontend,
        acceptsInput: idx.cmd.acceptsInput,
        inlineInput,
        score,
        matchType,
        frecencyScore: frecencyScore('plugin:' + idx.plugin.id + '.' + idx.cmd.id),
      })
    }
    if (pluginResultCache.value) {
      const pc = pluginResultCache.value
      pluginResults.unshift({
        type: 'plugin',
        label: pc.result,
        desc: '\u2190 ' + pc.pluginName,
        icon: Puzzle,
        iconBase64: pc.pluginId ? pluginIcons.value[pc.pluginId] : undefined,
        score: 99,
        frecencyScore: 9999,
        pluginId: pc.pluginId,
        pluginCommandId: pc.pluginCommandId,
        pluginHasFrontend: pc.pluginHasFrontend,
        acceptsInput: pc.acceptsInput,
        inlineInput: pc.input || undefined,
        matchType: pc.input ? 'match pattern' : undefined,
      })
    }
    pluginResults.sort((a, b) => {
      const sa = (a.score || 0) * 0.7 + Math.min(30, a.frecencyScore || 0) * 0.3
      const sb = (b.score || 0) * 0.7 + Math.min(30, b.frecencyScore || 0) * 0.3
      return sb - sa
    })
    if (pluginResults.length > 0) {
      groups.push({ type: 'plugin', label: t('cmdGroupPlugins'), results: pluginResults })
    }

    // 剪贴板智能路由
    if (clipboardUrlSource.value && q === clipboardUrlSource.value) {
      const urlStr = clipboardUrlSource.value
      const suggest: SearchResult[] = [
        { type: 'clipboard-action', clipAction: 'open-url', label: t('cmdOpenUrl'), desc: urlStr, url: urlStr, score: 100 },
        { type: 'clipboard-action', clipAction: 'save-url', label: t('cmdSaveAsItem'), desc: urlStr, url: urlStr, score: 99 },
        { type: 'clipboard-action', clipAction: 'encode-url', label: t('cmdEncodeUrl'), desc: t('cmdEncodeUrlDesc'), url: urlStr, score: 98 },
      ]
      groups.unshift({ type: 'clipboard-action', label: t('cmdGroupClipboard'), results: suggest })
    }

    // 最佳匹配注入
    const bestMatch: SearchResult[] = []
    for (const g of groups) {
      if (g.type === 'calculator' || g.type === 'clipboard-action') continue
      for (const r of g.results) {
        if ((r.score || 0) >= 70) bestMatch.push(r)
      }
    }
    if (bestMatch.length >= 1) {
      bestMatch.sort((a, b) => (b.score || 0) - (a.score || 0))
      const show = bestMatch.slice(0, 6)
      function dedupKey(r: SearchResult): string {
        if (r.pluginId && r.pluginCommandId) return 'plugin:' + r.pluginId + '.' + r.pluginCommandId
        if (r.item?.id) return 'item:' + r.item.id
        if ((r as any).snippet?.id) return 'snippet:' + (r as any).snippet.id
        if ((r as any).cmd?.id) return 'cmd:' + (r as any).cmd.id
        if (r.appPath) return 'app:' + r.appPath
        return r.label
      }
      const dedupIds = new Set(show.map(dedupKey))
      // 去重作用于所有分组：app/snippet/system/url 命中 ≥70 时也会进入最佳匹配，
      // 若只在 plugin/item 分组里过滤，它们仍会同时出现在两组中造成重复。
      for (const g of groups) {
        g.results = g.results.filter(r => !dedupIds.has(dedupKey(r)))
      }
      groups.unshift({ type: 'item', label: t('cmdBestMatch'), results: show })
    }

    return groups
  })

  const allResults = computed<SearchResult[]>(() => {
    return groupedResults.value.flatMap(g => g.results)
  })

  const recentResults = computed<SearchResult[]>(() => {
    if (query.value.trim()) return []
    const raw = recentCache.value
    if (!raw || raw.length === 0) return []

    const itemByKey: Record<string, CollectionItem> = {}
    for (const it of items.value) itemByKey['item:' + it.id] = it
    const snippetByKey: Record<string, CmdSnippet> = {}
    for (const s of snippets.value) snippetByKey['snippet:' + s.id] = s
    const cmdByKey: Record<string, SystemCmd> = {}
    for (const c of systemCommands.value) cmdByKey['system:' + c.id] = c

    const results: SearchResult[] = []
    for (const entry of raw) {
      if (entry.type === 'item' || entry.type === 'quicklink') {
        const item = itemByKey[entry.key]
        if (!item) continue
        results.push({
          type: item.value?.includes('{query}') ? 'quicklink' : 'item',
          label: entry.label,
          desc: entry.description,
          icon: itemIcon(item),
          iconBase64: item.icon || undefined,
          item,
          frecencyScore: entry.count,
        })
      } else if (entry.type === 'snippet') {
        const snippet = snippetByKey[entry.key]
        if (!snippet) continue
        results.push({
          type: 'snippet',
          label: entry.label,
          desc: entry.description,
          snippet,
          frecencyScore: entry.count,
        })
      } else if (entry.type === 'plugin') {
        const keyWithoutPrefix = entry.key.replace(/^plugin:/, '')
        let idx: PluginCmdIndex | undefined
        let matchedPluginId = ''
        let matchedCmdId = ''
        for (const cand of pluginCmdIndex.value) {
          const prefix = cand.plugin.id + '.'
          if (keyWithoutPrefix.startsWith(prefix)) {
            const cid = keyWithoutPrefix.slice(prefix.length)
            if (cid === cand.cmd.id) { idx = cand; matchedPluginId = cand.plugin.id; matchedCmdId = cid; break }
          }
        }
        if (!idx) continue
        // 仅当命令声明 acceptsInput 时，才把参数带回插件；否则不传（符合"不设置就不带"的意图）
        let recentInput: string | undefined
        let recentMatchType: string | undefined
        if (idx.cmd.acceptsInput) {
          recentInput = entry.input || undefined
          recentMatchType = entry.input ? 'match pattern' : undefined
          if (!recentInput) {
            const prefix = idx.cmd.title + ': '
            if (entry.label.startsWith(prefix)) {
              recentInput = entry.label.slice(prefix.length)
              recentMatchType = 'match pattern'
            }
          }
        }
        results.push({
          type: 'plugin',
          label: entry.label,
          desc: entry.description,
          icon: Puzzle,
          iconBase64: pluginIcons.value[matchedPluginId],
          pluginId: matchedPluginId,
          pluginCommandId: matchedCmdId,
          pluginHasFrontend: idx.plugin.hasFrontend,
          acceptsInput: idx.cmd.acceptsInput,
          inlineInput: recentInput,
          matchType: recentMatchType,
          frecencyScore: entry.count,
        })
      } else if (entry.type === 'system') {
        const cmd = cmdByKey[entry.key.replace(/^system:/, '')]
        if (!cmd) continue
        results.push({
          type: 'system',
          label: entry.label,
          desc: entry.description,
          icon: cmd.icon,
          cmd,
          frecencyScore: entry.count,
        })
      } else if (entry.type === 'app') {
        const installed = installedApps.value.find(a => a.name === entry.label)
        results.push({
          type: 'app',
          label: entry.label,
          desc: entry.description,
          icon: appIcon(entry.label),
          iconBase64: installed?.iconBase64 || '',
          appPath: installed?.path || entry.description,
          appCategory: installed?.category,
          frecencyScore: entry.count,
        })
      }
    }
    return results.slice(0, 8)
  })

  const displayGroups = computed<ResultGroup[]>(() => {
    if (query.value.trim()) return groupedResults.value
    if (recentResults.value.length > 0) {
      return [{ type: 'item', label: t('cmdRecent'), results: recentResults.value }]
    }
    return []
  })

  const displayFlat = computed<SearchResult[]>(() => {
    return displayGroups.value.flatMap(g => g.results)
  })

  const previewResult = computed<{ title: string; subtitle?: string; lines: string[]; kind: string } | null>(() => {
    const r = displayFlat.value[selectedIndex.value]
    if (!r) return null
    if (r.type === 'item' && r.item) {
      const lines = [r.item.value || '']
      if (r.item.type === '目录' || r.item.type === '文件') {
        const v = r.item.value || ''
        const idx = Math.max(v.lastIndexOf('/'), v.lastIndexOf('\\'))
        if (idx > 0) lines.push(t('previewParent') + ': ' + v.slice(0, idx))
      }
      return { title: r.label, subtitle: r.item.type, lines, kind: 'item' }
    }
    if (r.type === 'snippet' && (r as any).snippet) {
      return { title: r.label, subtitle: (r as any).snippet.category, lines: [(r as any).snippet.content], kind: 'snippet' }
    }
    if (r.type === 'plugin') {
      const cached = pluginResultCache.value
      return {
        title: r.label,
        subtitle: cached ? t('previewLast') : (r.desc || ''),
        lines: cached ? [cached.result] : [r.desc || ''],
        kind: 'plugin',
      }
    }
    if (r.type === 'url' && r.url) return { title: r.label, subtitle: t('previewUrl'), lines: [r.url], kind: 'url' }
    if (r.type === 'app' && r.appPath) return { title: r.label, subtitle: t('previewPath'), lines: [r.appPath], kind: 'app' }
    if (r.type === 'clipboard-action' && r.url) return { title: r.label, subtitle: t('previewUrl'), lines: [r.url], kind: 'clipboard-action' }
    return null
  })

  return { groupedResults, allResults, recentResults, displayGroups, displayFlat, previewResult, recentCache }
}
