// 命令面板的数据加载与缓存。
//
// 一次性拉全量池（项目 + 应用 + 笔记 + 最近使用）和插件索引，之后匹配完全在前端做
// ——这样才支持拼音与子串搜索（后端 FTS5 前缀匹配覆盖不到这两类）。
//
// 从 CommandPalette.vue 抽出的原因：这块有自己的一组缓存与节流状态
// （itemsLoadGen 代次、插件索引 500ms 节流、插件图标 30s TTL + 主题分档），
// 和 UI 无关，混在组件里只会让「谁在什么时候重拉」难以追踪。

import { watch } from 'vue'
import type { Ref } from 'vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useThemeName } from './useThemeName'
import { ListAllItems, ScanInstalledApps, ResetInstalledAppsCache } from '../../bindings/quickdock/services/appservice'
import { ListPlugins, GetPluginIcon } from '../../bindings/quickdock/services/plugin/pluginservice'
import { ListNotesTree } from '../../bindings/quickdock/services/note/noteservice'
import { GetAllUsage } from '../../bindings/quickdock/services/frecency/frecencyservice'
import type { PluginInfo } from '../types'
import type { CollectionItem, ToastAPI } from '../types'
import type { PluginCmdIndex } from './usePluginIndex'
import type { CmdNote, InstalledApp, RecentEntry } from './useCommandSearch'

export interface PaletteDataDeps {
  items: Ref<CollectionItem[]>
  installedApps: Ref<InstalledApp[]>
  notes: Ref<CmdNote[]>
  installedPlugins: Ref<PluginInfo[]>
  pluginCmdIndex: Ref<PluginCmdIndex[]>
  pluginIcons: Ref<Record<string, string>>
  recentCache: Ref<RecentEntry[]>
  loading: Ref<boolean>
  t: (k: string) => string
  toast?: ToastAPI
  buildPluginIndex: (plugins: PluginInfo[]) => PluginCmdIndex[]
  loadFrecency: () => Promise<void>
}

export function usePaletteData(deps: PaletteDataDeps) {
  const { items, installedApps, notes, installedPlugins, pluginCmdIndex,
          pluginIcons, recentCache, loading, t, toast } = deps

  // 加载代次：面板连续唤起时，旧一轮的若干 await 不能覆盖新一轮的结果
  let itemsLoadGen = 0
  let lastPluginIndexLoad = 0
  // #6 按需刷新：插件索引 500ms 节流；插件图标（每个插件一次 DB 读取）改为长 TTL 缓存，
  // 避免每次 show 全量重拉
  const PLUGIN_ICON_TTL = 30_000
  let lastPluginIconLoad = 0

  const themeName = useThemeName()

  async function loadPluginIcons(force = false) {
    const now = Date.now()
    if (!force && now - lastPluginIconLoad < PLUGIN_ICON_TTL) return
    lastPluginIconLoad = now
    // 图标分深浅两档，必须按当前主题取；主题切换时由下方 watch 强制重拉
    const theme = themeName.value
    await Promise.all(installedPlugins.value.map(async (p) => {
      try { const uri = unwrap<string | null>(await GetPluginIcon(p.id, theme)); if (uri) pluginIcons.value[p.id] = uri } catch (e) { logErr('CommandPalette', e) }
    }))
  }

  async function loadPluginIndex(forceIcons = false) {
    const now = Date.now()
    if (now - lastPluginIndexLoad < 500) return
    lastPluginIndexLoad = now
    try {
      const plugins = unwrap<PluginInfo[]>(await ListPlugins())
      // 懒加载后已启用插件多数处于 registered（进程/VM 待首次使用才起），必须一并建进索引：
      // 否则命令面板搜不到它们，也就无从触发拉起（EnsureLoaded）——死锁。
      // 排除用户显式禁用（disabled）与已停止（stopped）的插件：状态真相源收敛后由 p.disabled
      // 直接判定禁用态，不再依赖 status 间接推断（禁用插件不进命令面板入口）。
      const enabled = plugins?.filter(p => !p.disabled && p.status !== 'stopped') || []
      installedPlugins.value = enabled
      pluginCmdIndex.value = deps.buildPluginIndex(enabled)
      // 预加载插件真实图标（data URI），结果列表据此展示，无图标的插件回退到 Puzzle
      await loadPluginIcons(forceIcons)
    } catch (e) { console.error('[CmdPalette] ListPlugins:', getErrorMessage(e)) }
  }

  // 主题切换：图标分档必须立刻换，旧的 TTL 缓存作废
  watch(themeName, () => {
    lastPluginIconLoad = 0
    pluginIcons.value = {}
    void loadPluginIcons(true)
  })

  // 重建应用索引：清掉后端的内存缓存后立刻重扫一次。
  //
  // 为什么需要手动入口：缓存的失效依据是「开始菜单/卸载表这些来源目录的 mtime 变化」，
  // 覆盖不到绿色版、便携版、装到自定义目录的软件——它们的安装不碰任何一个被监控的目录，
  // 于是缓存永远命中，用户就一直搜不到。手动重建是这条自动链路兜不住时的出路。
  //
  // 重扫期间不关面板：扫描是磁盘活儿，耗时几百毫秒到数秒，关掉面板用户看不到
  // 「正在重建」，只会以为按了没反应；不关也能在完成后立刻用新索引搜。
  async function rebuildAppIndex() {
    try {
      unwrap(await ResetInstalledAppsCache())
      installedApps.value = unwrap<InstalledApp[]>(await ScanInstalledApps()) || []
      toast?.success?.(t('appIndexRebuilt'))
    } catch (e) {
      console.error('[CmdPalette] RebuildAppIndex:', getErrorMessage(e))
      toast?.error?.(t('appIndexRebuildFailed') + ': ' + getErrorMessage(e))
    }
  }

  async function loadPaletteData() {
    loading.value = true
    const gen = ++itemsLoadGen
    await deps.loadFrecency()
    try {
      const result = unwrap<CollectionItem[]>(await ListAllItems())
      if (gen !== itemsLoadGen) return
      items.value = result || []
    } catch (e) { console.error('[CmdPalette] ListAllItems:', getErrorMessage(e)) }
    // 每次 Ctrl+K 唤起都主动刷新一次已安装应用：后端 GetCachedApps 采用「来源目录 mtime
    // 惰性失效」，未装/卸软件时直接命中内存缓存（零磁盘扫描），仅当开始菜单确实变化时才重扫。
    // 因此这里可以无条件请求，确保新装软件无需重启 QuickDock 即可被搜索到。
    try {
      if (gen === itemsLoadGen) {
        const apps = unwrap<InstalledApp[]>(await ScanInstalledApps()) || []
        if (gen === itemsLoadGen) installedApps.value = apps
      }
    } catch (e) { console.error('[CmdPalette] ScanInstalledApps:', getErrorMessage(e)) }
    try {
      const notesData = unwrap<CmdNote[]>(await ListNotesTree())
      if (gen !== itemsLoadGen) return
      notes.value = notesData || []
    } catch (e) { console.error('[CmdPalette] ListNotesTree:', getErrorMessage(e)) }
    await refreshRecentCache(gen)
    if (gen === itemsLoadGen) loading.value = false
  }

  // 刷新「最近使用」缓存：运行命令/打开项目后实时拉取，否则要等下次重开面板才出现
  async function refreshRecentCache(gen?: number) {
    try {
      const raw = await GetAllUsage()
      if (gen === undefined || gen === itemsLoadGen) {
        // plugin 记录 label 可能为空（某些热键/自动执行路径），但近期有 plugin 记录本身
        // 就值得留在「最近使用」——组装时用命令标题/插件名兜底，故放宽过滤。
        // file（Everything 文件结果）不写也不读历史：即时检索不该沉淀进「最近使用」，
        // 这里顺手滤掉旧版本可能已写入的 file 记录。
        recentCache.value = (unwrap<RecentEntry[]>(raw) || []).filter(e => e.type && e.type !== 'file' && (e.label || e.type === 'plugin'))
      }
    } catch (e) { console.error('[CmdPalette] GetAllUsage:', getErrorMessage(e)) }
  }

  return { loadPluginIcons, loadPluginIndex, rebuildAppIndex, loadPaletteData, refreshRecentCache }
}
