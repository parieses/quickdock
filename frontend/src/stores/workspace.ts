import { logErr } from '../utils/logger'
// QuickDock - 工作区 Pinia Store
// 管理场景、集合、项、打开工具、搜索的全局状态

import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import { ListWorkspaces, CreateWorkspace, DeleteWorkspace, UpdateWorkspace, GetWorkspace, ReorderWorkspaces } from '../../bindings/quickdock/services/workspace/workspaceservice'
import { ListScenes, CreateScene, UpdateScene, DeleteScene, ReorderScenes } from '../../bindings/quickdock/services/scene/sceneservice'
import { ListCollections, CreateCollection, UpdateCollection, DeleteCollection, ReorderCollections, OpenItem, OpenAllInCollection } from '../../bindings/quickdock/services/collection/collectionservice'
import { ListItems, CreateItem, UpdateItem, DeleteItem, ReorderItems } from '../../bindings/quickdock/services/item/itemservice'
import { ListTools, GetValue, SetValue, SearchAll } from '../../bindings/quickdock/services/appservice'
import type { CollectionItem as BindingCollectionItem } from '../../bindings/quickdock/internal/db/models'
import type { Workspace, Scene, Collection, CollectionItem, OpenTool } from '../types'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import { toSnakeCase, toCamelCase } from '../utils/caseConversion'
import { debounce } from '../utils/debounce'
import { matchesTextOrPinyin } from '../utils/pinyin'

// 资源类型 → 推荐打开工具类型（数组：第一项用于默认工具，全部用于下拉候选）
// 目录额外把「编辑器」纳入候选，以便用 VSCode / Trae / Cursor 等打开目录
const TYPE_TOOL_MAP: Record<string, string[]> = {
  '目录': ['系统', '编辑器'],
  '文件': ['系统'],
  '网页': ['浏览器'],
  '命令': ['终端'],
  '应用': ['系统'],
  '快速链接': ['浏览器'],
}

// 内置哨兵工具：名称为“系统默认”且为默认工具，代表“跟随系统打开”。
// 编辑器中已有空选项（id=''）与之等价，故下拉列表中排除它，避免重复显示两个“系统默认”。
function isSentinelTool(t: OpenTool): boolean {
  return t.name === '系统默认' && t.isDefault === 1
}

export const useWorkspaceStore = defineStore('workspace', () => {
  const activeWorkspaceId = ref('')
  const activeSceneId = ref('')
  const activeCollectionId = ref('')
  const openedSceneIds = ref<string[]>([]) // 已打开的场景标签页

  // ---- 场景标签页持久化 ----
  //
  // 标签页是用户的「工作台」：关掉应用再回来，昨天开着的几个场景应该还在。
  // 之前它只是内存里的 ref，切工作空间/重启即清空，这个功能的价值基本为零。
  // 按工作空间分别存（key 带 wsId），因为不同工作空间的场景 id 互不相干。
  const openedTabsKey = (wsId: string) => 'openedSceneIds:' + wsId

  const saveOpenedTabs = debounce(() => {
    const wsId = activeWorkspaceId.value
    if (!wsId) return
    // 写库失败不该影响交互（标签页已经在前端生效了），只记日志
    SetValue(openedTabsKey(wsId), JSON.stringify(openedSceneIds.value))
      .catch((e) => logErr('workspace:saveTabs', e))
  }, 300)

  async function restoreOpenedTabs(wsId: string) {
    try {
      const raw = await GetValue(openedTabsKey(wsId))
      const ids = JSON.parse(String(raw || '[]'))
      if (!Array.isArray(ids)) return
      // 只保留当前仍存在的场景：场景可能已被删除，留下点不开的空标签更糟
      const alive = new Set(scenes.value.map((sc) => sc.id))
      openedSceneIds.value = ids.filter((x): x is string => typeof x === 'string' && alive.has(x))
    } catch (e) {
      logErr('workspace:restoreTabs', e)
    }
  }

  const workspaces = ref<Workspace[]>([])
  const scenes = ref<Scene[]>([])
  const collections = ref<Collection[]>([])
  const items = ref<CollectionItem[]>([])
  const tools = ref<OpenTool[]>([])

  const loading = ref(false)
  // 场景 / 集合列表加载中：用于区分「确实一个都没有」与「还在加载」。
  // 没有这两个标记时，切换工作空间/场景会先把数组清空再拉取，
  // 空状态引导页会闪一下（先显示"暂无集合 + 添加集合"，再变成真实列表）。
  const scenesLoading = ref(false)
  const collectionsLoading = ref(false)
  const error = ref('')

  // ---- 请求世代计数器（防止异步请求竞争） ----
  // 每次 selectWorkspace/selectScene/selectCollection 递增对应计数器，
  // fetch 回调检查世代号是否匹配，不匹配则丢弃过期结果。
  let workspaceGen = 0
  let sceneGen = 0
  let collectionGen = 0

  // ---- 搜索 ----
  const searchQuery = ref('')
  const searchResults = ref<{ scenes: Scene[]; collections: Collection[]; items: CollectionItem[] } | null>(null)
  const searchLoading = ref(false)

  /**
   * 当前工作空间内的条目搜索。
   *
   * 两条来源缺一不可：
   *   - 内存匹配：覆盖已加载的条目，并且支持拼音（FTS5 只做前缀匹配，不认拼音、不认中文子串）；
   *   - 后端 FTS：覆盖没点开过的集合里的条目——条目是按集合懒加载的，
   *     只扫内存会让人以为"我明明存过这条"。
   * 结果按 workspaceId 过滤，避免把别的工作空间的条目混进来。
   */
  async function searchItemsEverywhere(q: string, wsId: string): Promise<CollectionItem[]> {
    const local = items.value.filter((i) => matchesTextOrPinyin([i.name, i.value], q, 'i:' + i.id))
    if (!wsId) return local
    try {
      const remote = (unwrap(await SearchAll(q)) || []) as CollectionItem[]
      const seen = new Set(local.map((i) => i.id))
      const extra = remote.filter((i) => i.workspaceId === wsId && !seen.has(i.id))
      return [...local, ...extra]
    } catch (e) {
      // 搜索失败只降级为内存结果，不要弹错打断输入
      logErr('workspace:searchFts', e)
      return local
    }
  }

  // 防抖搜索：用户输入时延迟计算，避免频繁重算
  const debouncedSearch = debounce((query: string) => { void runSearch(query) }, 150)

  // 场景匹配不止名字：描述、类型、下挂集合名、以及「命中的条目所在的场景」都算命中。
  //
  // 只按名字匹配时，「我记得存过这条，忘了搁在哪个场景」这条路是断的 —— 场景是唯一的
  // 顶层分类，找不到它就等于找不到东西。hitItems 由调用方传：条目检索是异步的
  // （内存 + 后端 FTS），场景过滤必须等它出结果才能用。
  function matchScenes(q: string, hitItems: CollectionItem[]): Scene[] {
    const sceneOfCollection = new Map(collections.value.map((c) => [c.id, c.sceneId]))
    const scenesOfHitItems = new Set<string>()
    for (const it of hitItems) {
      const sid = sceneOfCollection.get(it.collectionId)
      if (sid) scenesOfHitItems.add(sid)
    }
    return scenes.value.filter((s) => {
      if (matchesTextOrPinyin([s.name, s.description, s.type], q, 's:' + s.id)) return true
      if (scenesOfHitItems.has(s.id)) return true
      return collections.value.some(
        (c) => c.sceneId === s.id && matchesTextOrPinyin([c.name], q, 'c:' + c.id),
      )
    })
  }

  async function runSearch(query: string) {
    const q = query.trim().toLowerCase()
    if (!q) {
      searchResults.value = null
      searchLoading.value = false
      return
    }
    searchLoading.value = true
    try {
      const matchedItems = await searchItemsEverywhere(q, activeWorkspaceId.value)
      const matchedScenes = matchScenes(q, matchedItems)
      const matchedCollections = collections.value.filter((c) => matchesTextOrPinyin([c.name], q, 'c:' + c.id))
      // 期间用户又敲了字：丢弃这次的结果，避免旧结果覆盖新查询
      if (searchQuery.value.trim().toLowerCase() !== q) return
      searchResults.value = { scenes: matchedScenes, collections: matchedCollections, items: matchedItems }
    } finally {
      searchLoading.value = false
    }
  }

  // 监听搜索查询变化，触发防抖搜索
  watch(searchQuery, (newQuery) => {
    if (!newQuery || newQuery.trim() === '') {
      searchResults.value = null
      searchLoading.value = false
    } else {
      debouncedSearch(newQuery)
    }
  })

  const hasSearch = computed(() => searchQuery.value.trim().length > 0)

  // ---- 计算属性 ----

  const activeWorkspace = computed(() =>
    workspaces.value.find(w => w.id === activeWorkspaceId.value) ?? null
  )
  const activeScene = computed(() =>
    scenes.value.find(s => s.id === activeSceneId.value) ?? null
  )
  const activeCollection = computed(() =>
    collections.value.find(c => c.id === activeCollectionId.value) ?? null
  )
  const sortedScenes = computed(() =>
    [...scenes.value].sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
  )
  const filteredCollections = computed(() => {
    if (hasSearch.value && searchResults.value) {
      const ids = new Set(searchResults.value.collections.map(c => c.id))
      return [...collections.value]
        .filter(c => ids.has(c.id))
        .sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
    }
    return collections.value
      .filter(c => c.sceneId === activeSceneId.value || (activeSceneId.value === '' && !c.sceneId))
      .sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
  })
  const filteredItems = computed(() => {
    if (hasSearch.value && searchResults.value) {
      const ids = new Set(searchResults.value.items.map(i => i.id))
      return [...items.value]
        .filter(i => ids.has(i.id))
        .sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
    }
    return items.value
      .filter(i => i.collectionId === activeCollectionId.value)
      .sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
  })

  // ---- 打开工具 ----

  function getDefaultToolForType(itemType: string): OpenTool | null {
    const preferred = TYPE_TOOL_MAP[itemType] || ['系统']
    for (const pt of preferred) {
      const candidates = tools.value.filter(t => t.type === pt)
      if (candidates.length > 0) return candidates[0]
    }
    // 回退：任意工具
    if (tools.value.length > 0) return tools.value.find(t => t.isDefault) || tools.value[0]
    return null
  }

  function getToolsForType(itemType: string): OpenTool[] {
    const preferred = TYPE_TOOL_MAP[itemType] || ['系统']
    const matched = tools.value.filter(t => preferred.includes(t.type) && !isSentinelTool(t))
    // 同时包含系统默认工具（排除内置“系统默认”哨兵，空选项已代表系统默认）
    const systemTools = tools.value.filter(t => t.type === '系统' && !matched.includes(t) && !isSentinelTool(t))
    return [...matched, ...systemTools]
  }

  // ---- 数据加载 ----

  async function initialize() {
    await Promise.all([fetchTools(), fetchWorkspaces()])
    // 恢复上次选中的工作空间和场景
    try {
      const lastWs = unwrap<string>(await GetValue('lastWorkspaceId'))
      const lastScene = unwrap<string>(await GetValue('lastSceneId'))
      if (lastWs && workspaces.value.find(w => w.id === lastWs)) {
        await selectWorkspace(lastWs)
        // selectWorkspace 可能已自动选中唯一场景；此时不必再走一遍
        // （重复 selectScene 会多两次后端查询，且会把刚打开的场景标签重排）
        if (lastScene && lastScene !== activeSceneId.value && scenes.value.find(s => s.id === lastScene)) {
          await selectScene(lastScene)
        }
      }
    } catch (e) { logErr('workspace', e) }
  }

  async function fetchTools() {
    try {
      const result = unwrap(await ListTools())
      tools.value = normalizeRows(result) as OpenTool[]
    } catch (e) {
      error.value = getErrorMessage(e)
      logErr('fetchTools', e)
    }
  }

  async function fetchWorkspaces() {
    loading.value = true
    error.value = ''
    try {
      const result = unwrap(await ListWorkspaces())
      workspaces.value = normalizeRows(result) as Workspace[]
    } catch (e) {
      error.value = getErrorMessage(e)
      logErr('fetchWorkspaces', e)
    } finally {
      loading.value = false
    }
  }

  async function selectWorkspace(id: string) {
    workspaceGen++
    activeWorkspaceId.value = id
    activeSceneId.value = ''
    activeCollectionId.value = ''
    openedSceneIds.value = []
    scenes.value = []
    collections.value = []
    items.value = []
    searchQuery.value = ''
    const gen = workspaceGen
    await fetchScenes(id, gen)
    await restoreOpenedTabs(id)
    try { unwrap(await SetValue('lastWorkspaceId', id)) } catch (e) { logErr('workspace', e) }
    // 只有一个场景时直接选中：否则用户进来看到的是「请先选择一个集合」的空页面，
    // 还得再点一次唯一的那一行才有内容
    await autoSelectSingleScene()
  }

  async function fetchScenes(workspaceId: string, gen?: number) {
    scenesLoading.value = true
    error.value = ''
    try {
      const result = unwrap(await ListScenes(workspaceId))
      if (gen !== undefined && gen !== workspaceGen) return
      scenes.value = normalizeRows(result) as Scene[]
    } catch (e) {
      error.value = getErrorMessage(e)
      logErr('fetchScenes', e)
    } finally {
      // 过期请求不能把新一代请求的 loading 关掉
      if (gen === undefined || gen === workspaceGen) scenesLoading.value = false
    }
  }

  async function selectScene(id: string) {
    sceneGen++
    activeSceneId.value = id
    activeCollectionId.value = ''
    collections.value = []
    items.value = []
    searchQuery.value = ''
    if (!openedSceneIds.value.includes(id)) {
      openedSceneIds.value.push(id)
      saveOpenedTabs()
    }
    const gen = sceneGen
    await fetchCollections(id, gen)
    try { unwrap(await SetValue('lastSceneId', id)) } catch (e) { logErr('workspace', e) }
    // 同理：这个场景下只有一个集合时直接选中它，不再要求用户多点一次
    await autoSelectSingleCollection()
  }

  async function closeSceneTab(id: string) {
    const idx = openedSceneIds.value.indexOf(id)
    if (idx < 0) return
    openedSceneIds.value.splice(idx, 1)
    saveOpenedTabs()
    // 如果关闭的是当前激活场景，切换到相邻场景
    if (activeSceneId.value === id) {
      if (openedSceneIds.value.length > 0) {
        // 优先切换到后一个，没有则前一个
        const nextId = openedSceneIds.value[Math.min(idx, openedSceneIds.value.length - 1)]
        await selectScene(nextId)
      } else {
        activeSceneId.value = ''
        activeCollectionId.value = ''
        collections.value = []
        items.value = []
        searchQuery.value = ''
      }
    }
  }

  // 关闭左侧所有标签页
  async function closeTabsToLeft(id: string) {
    const idx = openedSceneIds.value.indexOf(id)
    if (idx <= 0) return
    const toRemove = openedSceneIds.value.slice(0, idx)
    openedSceneIds.value.splice(0, idx)
    saveOpenedTabs()
    // 如果当前激活的场景被关闭了，切换到被右击的场景
    if (toRemove.includes(activeSceneId.value)) {
      await selectScene(id)
    }
  }

  // 关闭右侧所有标签页
  async function closeTabsToRight(id: string) {
    const idx = openedSceneIds.value.indexOf(id)
    if (idx < 0 || idx >= openedSceneIds.value.length - 1) return
    const toRemove = openedSceneIds.value.slice(idx + 1)
    openedSceneIds.value.splice(idx + 1)
    saveOpenedTabs()
    // 如果当前激活的场景被关闭了，切换到被右击的场景
    if (toRemove.includes(activeSceneId.value)) {
      await selectScene(id)
    }
  }

  // 关闭其他所有标签页（仅保留当前）
  async function closeOtherTabs(id: string) {
    if (openedSceneIds.value.length <= 1) return
    openedSceneIds.value = [id]
    // 如果当前激活的不是被右击的场景，切换过去
    if (activeSceneId.value !== id) {
      await selectScene(id)
    }
  }

  // 重排已打开的场景标签页（SceneTags 拖拽排序落点）。只改顺序并落库，不影响当前激活场景。
  function reorderOpenedTabs(orderedIDs: string[]) {
    openedSceneIds.value = orderedIDs
    saveOpenedTabs()
  }

  // ---- 辅助函数 ----

  // 在数组中按 id 查找并合并更新（自动设置 updatedAt）
  function updateInArray(arr: any[], id: string, updates: Record<string, any>) {
    const idx = arr.findIndex((item: any) => item.id === id)
    if (idx >= 0) Object.assign(arr[idx], updates, { updatedAt: new Date().toISOString() })
  }

  // 更新数组中元素的 sort 字段并按 sort 排序
  function sortByOrder(arr: any[], orderedIDs: string[]): any[] {
    orderedIDs.forEach((id, idx) => {
      const item = arr.find((x: any) => x.id === id)
      if (item) item.sort = idx * 10
    })
    return [...arr].sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
  }

  function normalizeRows(rows: any[] | null): any[] {
    return (rows ?? []).map(row => toCamelCase(row))
  }

  // 使用统一转换函数，确保前后端字段名映射一致
  const toSnake = toSnakeCase

  // ---- 拖拽排序 ----
  async function reorderScenes(orderedIDs: string[]) {
    scenes.value = sortByOrder(scenes.value, orderedIDs)
    try {
      await ReorderScenes(orderedIDs)
    } catch (e) {
      if (activeWorkspaceId.value) await fetchScenes(activeWorkspaceId.value)
    }
  }

  async function reorderCollections(orderedIDs: string[]) {
    collections.value = sortByOrder(collections.value, orderedIDs)
    try {
      await ReorderCollections(orderedIDs)
    } catch (e) {
      if (activeSceneId.value) await fetchCollections(activeSceneId.value)
    }
  }

  async function reorderItems(orderedIDs: string[]) {
    items.value = sortByOrder(items.value, orderedIDs)
    try {
      await ReorderItems(orderedIDs)
    } catch (e) {
      if (activeCollectionId.value) await fetchItems(activeCollectionId.value)
    }
  }

  async function fetchCollections(sceneId: string, gen?: number) {
    collectionsLoading.value = true
    error.value = ''
    try {
      const result = unwrap(await ListCollections(sceneId))
      if (gen !== undefined && gen !== sceneGen) return
      collections.value = normalizeRows(result) as Collection[]
    } catch (e) {
      error.value = getErrorMessage(e)
      logErr('fetchCollections', e)
    } finally {
      if (gen === undefined || gen === sceneGen) collectionsLoading.value = false
    }
  }

  async function selectCollection(id: string) {
    collectionGen++
    activeCollectionId.value = id
    items.value = []
    searchQuery.value = ''
    const gen = collectionGen
    await fetchItems(id, gen)
  }

  async function fetchItems(collectionId: string, gen?: number) {
    error.value = ''
    try {
      const result = unwrap(await ListItems(collectionId))
      if (gen !== undefined && gen !== collectionGen) return
      items.value = normalizeRows(result) as CollectionItem[]
    } catch (e) {
      error.value = getErrorMessage(e)
      logErr('fetchItems', e)
    }
  }

  // ---- 「只有一个」时自动选中 ----
  // 只有一个候选时选中它没有歧义，让界面直接落到有内容的那一屏；
  // 有多个候选则不猜（用户可能想先建第二个），保持未选中。
  async function autoSelectSingleScene() {
    if (activeSceneId.value) return
    if (scenes.value.length !== 1) return
    await selectScene(scenes.value[0].id)
  }

  async function autoSelectSingleCollection() {
    if (activeCollectionId.value) return
    if (collections.value.length !== 1) return
    await selectCollection(collections.value[0].id)
  }

  // ---- 创建 ----

  async function addWorkspace(name: string) {
    const w = unwrap(await CreateWorkspace(name))
    const normalized = normalizeRows([w])[0] as Workspace
    workspaces.value.push(normalized)
    return normalized
  }

  async function removeWorkspace(id: string) {
    unwrap(await DeleteWorkspace(id))
    workspaces.value = workspaces.value.filter(w => w.id !== id)
    if (activeWorkspaceId.value === id) {
      if (workspaces.value.length > 0) {
        await selectWorkspace(workspaces.value[0].id)
      } else {
        activeWorkspaceId.value = ''
        activeSceneId.value = ''
        activeCollectionId.value = ''
        scenes.value = []
        collections.value = []
        items.value = []
      }
    }
  }

  async function updateWorkspaceAction(id: string, name: string) {
    unwrap(await UpdateWorkspace(id, name))
    const w = workspaces.value.find(w => w.id === id)
    if (w) w.name = name
  }

  async function reorderWorkspaces(orderedIDs: string[]) {
    workspaces.value.sort((a, b) => {
      const ai = orderedIDs.indexOf(a.id), bi = orderedIDs.indexOf(b.id)
      if (ai >= 0 && bi >= 0) return ai - bi
      if (ai >= 0) return -1
      if (bi >= 0) return 1
      return 0
    })
    try {
      await ReorderWorkspaces(orderedIDs)
    } catch (e) {
      const result = unwrap(await ListWorkspaces())
      workspaces.value = normalizeRows(result) as Workspace[]
    }
  }

  async function addScene(name: string, type: string, group = '') {
    const s = unwrap(await CreateScene(activeWorkspaceId.value, name, type, group))
    const normalized = normalizeRows([s])[0] as Scene
    scenes.value.push(normalized)
    // 刚建出来的是唯一场景 → 直接选中，省掉"建完还要再点一下"
    await autoSelectSingleScene()
    return normalized
  }

  async function addCollection(name: string, type: string, openStrategy = 'single', group = '') {
    const c = unwrap(await CreateCollection(activeWorkspaceId.value, activeSceneId.value, name, type, openStrategy, group))
    const normalized = normalizeRows([c])[0] as Collection
    collections.value.push(normalized)
    await autoSelectSingleCollection()
    return normalized
  }

  async function addItem(name: string, itemType: string, value: string) {
    const i = unwrap(await CreateItem(activeWorkspaceId.value, activeCollectionId.value, name, itemType, value))
    const normalized = normalizeRows([i])[0] as CollectionItem
    items.value.push(normalized)
    return normalized
  }

  // ---- 更新 ----

  async function updateSceneAction(id: string, updates: Record<string, any>) {
    unwrap(await UpdateScene(id, toSnake(updates)))
    updateInArray(scenes.value, id, updates)
  }

  async function updateCollectionAction(id: string, updates: Record<string, any>) {
    unwrap(await UpdateCollection(id, toSnake(updates)))
    updateInArray(collections.value, id, updates)
  }

  async function updateItemAction(id: string, updates: Record<string, any>) {
    unwrap(await UpdateItem(id, toSnake(updates)))
    updateInArray(items.value, id, updates)
    // 后端可能已自动重新提取图标（应用 exe/lnk 路径变化），
    // 重新拉取当前集合以把数据库中的最新字段（含 icon）同步到列表
    if (activeCollectionId.value) {
      await fetchItems(activeCollectionId.value)
    }
  }

  // ---- 删除 ----

  async function removeScene(id: string) {
    unwrap(await DeleteScene(id))
    scenes.value = scenes.value.filter(s => s.id !== id)
    openedSceneIds.value = openedSceneIds.value.filter(sid => sid !== id)
    // 清理该场景下的 collections 和 items 的缓存
    const removedColIds = collections.value.filter(c => c.sceneId === id).map(c => c.id)
    collections.value = collections.value.filter(c => c.sceneId !== id)
    items.value = items.value.filter(i => !removedColIds.includes(i.collectionId))
    if (activeSceneId.value === id) {
      activeSceneId.value = ''
      activeCollectionId.value = ''
      // 删掉当前场景后只剩一个场景 → 自动落到那个场景，别停在空页面
      await autoSelectSingleScene()
    }
  }

  async function removeCollection(id: string) {
    unwrap(await DeleteCollection(id))
    collections.value = collections.value.filter(c => c.id !== id)
    if (activeCollectionId.value === id) {
      activeCollectionId.value = ''
      // 清空属于此集合的 items，防止残留数据
      items.value = []
      await autoSelectSingleCollection()
    }
  }

  async function removeItem(id: string) {
    unwrap(await DeleteItem(id))
    items.value = items.value.filter(i => i.id !== id)
  }

  // ---- 打开 ----

  async function openItem(item: CollectionItem) {
    unwrap(await OpenItem(item as BindingCollectionItem))
  }

  async function openAllInCollection(collectionId: string) {
    unwrap(await OpenAllInCollection(collectionId))
  }

  // ---- 集合的「打开策略」 ----
  //
  // collections.open_strategy 一直只在写侧存在（新建/编辑表单能选三档），
  // 全仓库没有任何一处读它做分支 —— 选什么都不影响行为。这里把它接到
  // 集合行的「打开」动作上：
  //   single 单次打开 → 只打开使用次数最高的一条（"我就想开常用的那个"）
  //   batch  批量打开 → 打开前 N 条（N 有上限，别一次弹十几个窗口）
  //   all    全部打开 → 打开集合内全部条目（沿用后端批量接口）
  // 只取 usageCount 最高的场景下，未打开过的集合按 sort 顺序退化，结果可预期。
  const OPEN_BATCH_LIMIT = 5

  async function openCollectionByStrategy(collection: Collection): Promise<number> {
    const strategy = collection.openStrategy || 'single'
    if (strategy === 'all') {
      await openAllInCollection(collection.id)
      return -1 // -1 = 全部，调用方据此显示不同文案
    }
    // 不动 items.value：那是「当前选中集合」的列表，这里只是取数据
    const rows = (unwrap<BindingCollectionItem[]>(await ListItems(collection.id)) ?? [])
      .map((r) => toCamelCase(r) as unknown as CollectionItem)
    let picks: CollectionItem[]
    if (strategy === 'batch') {
      picks = rows.slice(0, OPEN_BATCH_LIMIT)
    } else {
      picks = [...rows].sort((a, b) => (b.usageCount ?? 0) - (a.usageCount ?? 0)).slice(0, 1)
    }
    let opened = 0
    for (const it of picks) {
      await openItem(it) // 单条失败只影响它自己，不阻断其余
      opened++
    }
    return opened
  }

  // ---- 搜索 ----

  function setSearch(query: string) {
    searchQuery.value = query
  }

  function clearSearch() {
    searchQuery.value = ''
  }

  return {
    activeWorkspaceId, activeSceneId, activeCollectionId, openedSceneIds,
    workspaces, scenes, collections, items, tools,
    loading, error,
    scenesLoading, collectionsLoading,
    searchQuery, hasSearch, searchResults, searchLoading,
    activeWorkspace, activeScene, activeCollection,
    sortedScenes, filteredCollections, filteredItems,
    initialize, fetchTools, fetchWorkspaces, fetchScenes, fetchCollections, fetchItems,
    selectWorkspace, selectScene, selectCollection, closeSceneTab,
    closeTabsToLeft, closeTabsToRight, closeOtherTabs,
    reorderScenes, reorderCollections, reorderItems, reorderOpenedTabs,
    addWorkspace, addScene, addCollection, addItem,
    removeWorkspace,
    updateWorkspaceAction,
    reorderWorkspaces,
    updateSceneAction, updateCollectionAction, updateItemAction,
    removeScene, removeCollection, removeItem,
    openItem, openAllInCollection, openCollectionByStrategy,
    setSearch, clearSearch,
    getDefaultToolForType, getToolsForType,
  }
})

