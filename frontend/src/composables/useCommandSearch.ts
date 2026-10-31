import { ref, computed, watch, onScopeDispose } from 'vue'
import type { Ref, ComputedRef } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CollectionItem, EnvRuntimeInfo, EnvServiceStatus, FileSearchResult } from '../types'
import type { PluginCmdIndex } from './usePluginIndex'
import { commandTitle, pluginName } from '../utils/localize'
import { getPluginLastResult } from '../utils/pluginLastResult'
import { Puzzle, File } from '@lucide/vue'
import { EnvEverythingSearch } from '../../bindings/quickdock/services/env/environmentservice'
import { SearchPalette } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
// 导航项唯一真相源：新增页面只改 registry，侧边栏 / 内容区 / 面板「快速导航」三处同时生效
import { navEntries } from '../pages/registry'
import { MATCHERS, matchClipboard, type MatchCtx } from './commandSearch/matchers'
import { dropEmptyGroups, injectBestMatch, sortGroups } from './commandSearch/ranking'
import type {
  CmdNote, CmdTodo, InstalledApp, LcIndex, NavSearchItem, RecentEntry, ResultGroup, SearchResult, SystemCmd,
} from './commandSearch/types'

// 类型转发：调用方一直从本文件 import 这些类型，拆到 commandSearch/types 后
// 在这里继续 re-export，避免改动所有引用点。
export type {
  CmdNote, CmdTodo, InstalledApp, NavSearchItem, RecentEntry, ResultGroup, ResultType, SearchResult, SystemCmd,
} from './commandSearch/types'

// SearchPalette 的合并载荷。后端 `Services.PaletteSearchResult` 字段名固定
// （items/notes/todos），但 bindings 只给到 ApiResult —— notes/todos 的元素
// 结构按 CmdNote/CmdTodo 断言（后端用的就是同一批行）。
interface PaletteSearchPayload {
  items?: CollectionItem[]
  notes?: CmdNote[]
  todos?: CmdTodo[]
}

// 空载荷：后端对空查询返回三个空切片，调用前也要有一份兜底，
// 别让 null 一路传到模板的 `.length` 上。
function emptyPalettePayload(): PaletteSearchPayload {
  return { items: [], notes: [], todos: [] }
}

// ---- Dependencies type ----
export interface SearchDeps {
  items: Ref<CollectionItem[]>
  installedApps: Ref<InstalledApp[]>
  notes: Ref<CmdNote[]>
  systemCommands: ComputedRef<SystemCmd[]>
  query: Ref<string>
  selectedIndex: Ref<number>
  pluginCmdIndex: Ref<PluginCmdIndex[]>
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
  envRuntimes: Ref<EnvRuntimeInfo[]>
  envStatuses: Ref<Record<string, EnvServiceStatus>>
  envMgmt: Ref<Record<string, boolean>> // RabbitMQ 各版本管理后台启用状态（version → enabled）
}

export function useCommandSearch(deps: SearchDeps) {
  const { locale } = useI18n()
  const { items, installedApps, notes, systemCommands, query, selectedIndex,
          pluginCmdIndex, clipboardUrlSource,
          frecencyScore, frecencyTick, calcPluginScore,
          pinyinMatch, appIcon, getAppAliases, itemIcon, t, pluginIcons, envRuntimes, envStatuses, envMgmt } = deps

  const recentCache = ref<RecentEntry[]>([])

  // 预计算索引：小写化与应用别名展开都与 query 无关，只随底层数据变化。
  //
  // 这是本轮唯一的真实性能改动。原先每个 query 变化（防抖后每 80ms 一次）都要对
  // 全部 items / notes / apps / systemCommands 重跑 toLowerCase()，items 上限 5000 条，
  // 纯属白烧；apps 那条还要再跑一遍 getAppAliases()（30 条正则逐条匹配）。
  // 现在这些只在数据变动时算一次。
  //
  // 顺带修掉一个隐性浪费：matchApps 里别名分支原来调 pinyinMatch(a, qLC) 没给
  // cacheKey，等于每轮都把别名重新转拼音 —— 见 matchers.ts 的注释。
  const lcIndex = computed<LcIndex>(() => ({
    items: items.value.map(it => ({
      item: it,
      nameLC: (it.name || '').toLowerCase(),
      valueLC: (it.value || '').toLowerCase(),
    })),
    notes: notes.value.map(n => ({
      note: n,
      nameLC: (n.name || n.keyword || '').toLowerCase(),
      contentLC: (n.content || '').toLowerCase(),
    })),
    apps: installedApps.value.map(a => ({
      app: a,
      nameLC: a.name.toLowerCase(),
      aliases: getAppAliases(a.name).map(raw => ({ raw, lc: raw.toLowerCase() })),
    })),
    sys: systemCommands.value.map(c => ({ cmd: c, labelLC: c.label.toLowerCase() })),
  }))

  // 搜索防抖：逐键触发全量扫描（含 pinyinMatch/Levenshtein）在数据量大时卡顿，
  // 80ms 内停顿才真正重算，键盘快速打字时结果滞后几乎不可感知
  const debouncedQuery = ref(query.value)
  let debounceTimer: ReturnType<typeof setTimeout> | null = null

  // Everything 文件搜索：Ctrl+K 输入经 130ms 防抖后才打后端 es.exe（外部进程，比本地扫描重），
  // 命中的文件路径 merge 进独立的 file 分组（排在资源结果之后）。后端在「未启用 / es 缺失 / 未运行」
  // 时静默返回空，绝不会阻塞 QuickDock 自身的检索。
  const fileResults = ref<SearchResult[]>([])
  let fileDebounce: ReturnType<typeof setTimeout> | null = null
  let fileReqId = 0

  // 合并 query 的两个监听：本地全量扫描防抖（轻，80ms）与 Everything 文件搜索防抖（外部进程，130ms）
  // 共用同一个 watcher，避免对同一个 query 注册两个独立 watch、重复求值。
  const QUERY_DEBOUNCE_MS = 80
  const FILE_SEARCH_DEBOUNCE_MS = 130
  watch(query, (v) => {
    if (debounceTimer) clearTimeout(debounceTimer)
    debounceTimer = setTimeout(() => { debouncedQuery.value = v }, QUERY_DEBOUNCE_MS)
    const q = v.trim()
    if (fileDebounce) clearTimeout(fileDebounce)
    if (!q) { fileResults.value = []; return }
    fileDebounce = setTimeout(() => {
      const reqId = ++fileReqId
      ;(async () => {
        try {
          const res = unwrap<FileSearchResult[]>(await EnvEverythingSearch(q, 30, 'name'))
          if (reqId !== fileReqId) return // 已被更新的输入取代
          if (res && res.length) {
            fileResults.value = res.map((f) => ({
              type: 'file' as const,
              label: f.name ?? '',
              desc: f.dir ?? '',
              icon: File,
              filePath: f.path ?? '',
              fileDir: f.dir ?? '',
              fileName: f.name ?? '',
              score: 50,
            }))
          } else {
            fileResults.value = []
          }
        } catch {
          fileResults.value = []
        }
      })()
    }, FILE_SEARCH_DEBOUNCE_MS)
  }, { immediate: true })
  onScopeDispose(() => {
    if (debounceTimer) clearTimeout(debounceTimer)
    if (fileDebounce) clearTimeout(fileDebounce)
    if (backendTimer) clearTimeout(backendTimer)
  })

  // 后端全文检索兜底（q 长度 ≥2 才启用，140ms 防抖 + 请求代次守卫）：
  // 一次 SearchPalette 拿三类结果，省掉面板每敲一个字打 3 次 IPC、各查一遍库：
  // - items（FTS5）：兜住 ListAllItems 的 5000 上限之外的条目——超限条目在
  //   本地全量池里根本不存在，前端扫得再快也找不到它们。
  // - notes（LIKE 全文）：内容/标签匹配交给 SQLite，前端只保留名称 + 拼音的
  //   快速通道。笔记量大时逐篇 contains 全文是面板可感知的卡顿源，而标签
  //   匹配前端本来就做不到。
  // - todos（LIKE）：待办没有本地池，这一路是唯一来源。
  // 后端三段各自独立（单段失败留空），整条链路失败也静默：
  // 本地匹配仍是权威路径，后端只是补充。
  const backendItems = ref<CollectionItem[]>([])
  const backendNotes = ref<CmdNote[]>([])
  // 待办没有本地常驻池：items/notes 在 App.vue 有一份列表喂进来，待办没有，
  // 所以这一个区 100% 依赖后端检索（否则 Ctrl+K 永远搜不到待办）。
  const backendTodos = ref<CmdTodo[]>([])
  let backendReqId = 0
  let backendTimer: ReturnType<typeof setTimeout> | null = null

  // 后端全文检索结果缓存（按 query）：TTL 30s + 容量上限，本质是个小型 LRU。
  // 命令面板连续输入同一词（删字重打）不再重复打一次 IPC；
  // 后端只是本地匹配的补充，TTL 自然失效不影响权威路径。
  //
  // 必须带容量上限：原先只判断 TTL、从不删除，Map 会随「这次会话里敲过的每个前缀」一路长下去
  // —— TTL 只在读的时候比较，过期条目永远留在内存里。40 条足够覆盖来回删字的场景，
  // 超了按最久未用淘汰。
  interface BackendHit { ts: number; items: CollectionItem[]; notes: CmdNote[]; todos: CmdTodo[] }
  const backendSearchCache = new Map<string, BackendHit>()
  const BACKEND_SEARCH_TTL = 30_000
  const BACKEND_SEARCH_MAX = 40

  function cacheGetBackend(q: string): BackendHit | null {
    const hit = backendSearchCache.get(q)
    if (!hit) return null
    if (Date.now() - hit.ts >= BACKEND_SEARCH_TTL) {
      backendSearchCache.delete(q)
      return null
    }
    // 命中后挪到队尾：Map 保持插入序，淘汰时才是真的「最久未用」
    backendSearchCache.delete(q)
    backendSearchCache.set(q, hit)
    return hit
  }

  function cacheSetBackend(q: string, hit: BackendHit) {
    backendSearchCache.delete(q)
    backendSearchCache.set(q, hit)
    while (backendSearchCache.size > BACKEND_SEARCH_MAX) {
      const oldest = backendSearchCache.keys().next().value
      if (oldest === undefined) break
      backendSearchCache.delete(oldest)
    }
  }

  watch(query, (v) => {
    if (backendTimer) clearTimeout(backendTimer)
    const reqId = ++backendReqId
    const q = v.trim()
    if (q.length < 2) {
      backendItems.value = []
      backendNotes.value = []
      backendTodos.value = []
      return
    }
    // 命中缓存直接填，省一次 IPC 往返
    const cached = cacheGetBackend(q)
    if (cached) {
      backendItems.value = cached.items
      backendNotes.value = cached.notes
      backendTodos.value = cached.todos
      return
    }
    backendTimer = setTimeout(() => {
      if (reqId !== backendReqId) return // 已被更新的输入取代
      void (async () => {
        try {
          // 一次 IPC 拿三类结果（后端 SearchPalette 内部各自独立执行，
          // 单段 SQL 失败只让那一类为空，不拖垮整块）。
          const res = await SearchPalette(q)
          if (reqId !== backendReqId) return
          const payload = unwrap<PaletteSearchPayload>(res) || emptyPalettePayload()
          const items_ = payload.items || []
          const notes_ = (payload.notes || []) as CmdNote[]
          const todos_ = (payload.todos || []) as CmdTodo[]
          cacheSetBackend(q, { ts: Date.now(), items: items_, notes: notes_, todos: todos_ })
          backendItems.value = items_
          backendNotes.value = notes_
          backendTodos.value = todos_
        } catch {
          if (reqId !== backendReqId) return
          backendItems.value = []
          backendNotes.value = []
          backendTodos.value = []
        }
      })()
    }, 140)
  }, { immediate: true })

  // 结果分组的编排层：准备上下文 → 按顺序跑 matcher → 插剪贴板动作 → 最佳匹配置顶 → 排序。
  //
  // 12 个类别的匹配逻辑都在 commandSearch/matchers.ts 里，这里只负责顺序与收尾。
  // 顺序有语义（matcher 共享 seen 去重集），改 MATCHERS 的数组顺序等于改归属规则。
  // 「快速导航」候选：registry 里带 component 的内容页。ssh / dsh 只有 activate
  // （唤起独立窗口），会被这一条自动滤掉 —— 面板里选它们等于什么都没发生。
  // 图标与文案 key 是静态的，不需要 computed；本地化在 matcher 里做（跟着语言变）。
  const navSearchItems: NavSearchItem[] = navEntries
    .filter((e) => !!e.component)
    .map((e) => ({ id: e.id, labelKey: e.labelKey, icon: e.icon }))

  const groupedResults = computed<ResultGroup[]>(() => {
    void frecencyTick.value
    const q = debouncedQuery.value.trim()
    if (!q) return []

    const ctx: MatchCtx = {
      q,
      qLC: q.toLowerCase(),
      seen: new Set<string>(),
      t,
      // locale 取值同时建立响应式依赖：切语言后结果文案跟着重建
      locale: locale.value,
      lc: lcIndex.value,
      backendItems: backendItems.value,
      backendNotes: backendNotes.value,
      backendTodos: backendTodos.value,
      fileResults: fileResults.value,
      clipboardUrlSource: clipboardUrlSource.value,
      navItems: navSearchItems,
      pluginCmdIndex: pluginCmdIndex.value,
      envRuntimes: envRuntimes.value,
      envStatuses: envStatuses.value,
      envMgmt: envMgmt.value,
      pluginIcons: pluginIcons.value,
      frecencyScore,
      pinyinMatch,
      itemIcon,
      appIcon,
      calcPluginScore,
    }

    const groups: ResultGroup[] = []
    for (const match of MATCHERS) {
      const out = match(ctx)
      if (!out) continue
      if (out.groups.length) groups.push(...out.groups)
      // `=1+1` 算出来了就到此为止：不再往下搜条目/应用/插件（原实现的 return 语义）
      if (out.stop) return groups
    }

    // 剪贴板动作要插在最前面，且不受「最佳匹配」去重影响（它被 injectBestMatch 显式排除）
    const clip = matchClipboard(ctx)
    if (clip.length) groups.unshift(...clip)

    injectBestMatch(groups, t)
    dropEmptyGroups(groups)
    sortGroups(groups)
    return groups
  })

  const allResults = computed<SearchResult[]>(() => {
    return groupedResults.value.flatMap(g => g.results)
  })

  const recentResults = computed<SearchResult[]>(() => {
    if (debouncedQuery.value.trim()) return []
    const raw = recentCache.value
    if (!raw || raw.length === 0) return []

    const itemByKey: Record<string, CollectionItem> = {}
    for (const it of items.value) itemByKey['item:' + it.id] = it
    const noteByKey: Record<string, CmdNote> = {}
    for (const n of notes.value) noteByKey['note:' + n.id] = n
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
      } else if (entry.type === 'note') {
        const note = noteByKey[entry.key]
        if (!note) continue
        results.push({
          type: 'note',
          label: entry.label,
          desc: entry.description,
          note,
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
            const prefix = commandTitle(idx.cmd, locale) + ': '
            if (entry.label.startsWith(prefix)) {
              recentInput = entry.label.slice(prefix.length)
              recentMatchType = 'match pattern'
            }
          }
        }
        results.push({
          type: 'plugin',
          // label/desc 空兜底：某些路径写入的 plugin 记录 label 为空，
          // 用命令标题 / 插件名补上，保证「最近使用」可读可执行
          label: entry.label || commandTitle(idx.cmd, locale),
          desc: entry.description || pluginName(idx.plugin, locale),
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
    return results
  })

  // 「最近使用」默认只显示前 15 条，其余通过展开/收起按钮切换查看
  const RECENT_VISIBLE = 15
  const recentExpanded = ref(false)
  function toggleRecentExpanded() { recentExpanded.value = !recentExpanded.value }

  const displayGroups = computed<ResultGroup[]>(() => {
    if (debouncedQuery.value.trim()) return groupedResults.value
    const all = recentResults.value
    if (all.length > 0) {
      const shown = recentExpanded.value ? all : all.slice(0, RECENT_VISIBLE)
      return [{ type: 'item', label: t('cmdRecent'), results: shown, totalCount: all.length }]
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
    if (r.type === 'note' && r.note) {
      return { title: r.label, subtitle: r.note.isFolder ? '📁' : '', lines: [r.note.content], kind: 'note' }
    }
    if (r.type === 'todo' && r.todo) {
      return { title: r.label, subtitle: t('cmdGroupTodos'), lines: [r.desc || ''], kind: 'todo' }
    }
    if (r.type === 'plugin') {
      const last = r.pluginId && r.pluginCommandId ? getPluginLastResult(r.pluginId, r.pluginCommandId) : null
      return {
        title: r.label,
        subtitle: last ? t('previewLast') : (r.desc || ''),
        // 结果正文进右侧 preview 面板，列表只留命令本体
        lines: last ? [last.result] : [r.desc || ''],
        kind: 'plugin',
      }
    }
    if (r.type === 'url' && r.url) return { title: r.label, subtitle: t('previewUrl'), lines: [r.url], kind: 'url' }
    if (r.type === 'app' && r.appPath) return { title: r.label, subtitle: t('previewPath'), lines: [r.appPath], kind: 'app' }
    if (r.type === 'file' && r.filePath) return { title: r.fileName || r.label, subtitle: t('cmdGroupFiles'), lines: [r.filePath], kind: 'file' }
    if (r.type === 'clipboard-action' && r.url) return { title: r.label, subtitle: t('previewUrl'), lines: [r.url], kind: 'clipboard-action' }
    return null
  })

  return { groupedResults, allResults, recentResults, displayGroups, displayFlat, previewResult, recentCache,
           RECENT_VISIBLE, recentExpanded, toggleRecentExpanded }
}
