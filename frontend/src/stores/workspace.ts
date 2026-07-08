// QuickDock - 工作区 Pinia Store
// 管理场景、集合、项、打开工具、搜索的全局状态

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { ListWorkspaces, CreateWorkspace, DeleteWorkspace, UpdateWorkspace, GetWorkspace, ReorderWorkspaces } from '../../bindings/quickdock/services/appservice'
import { ListScenes, CreateScene, UpdateScene, DeleteScene, ReorderScenes } from '../../bindings/quickdock/services/appservice'
import { ListCollections, CreateCollection, UpdateCollection, DeleteCollection, ReorderCollections } from '../../bindings/quickdock/services/appservice'
import { ListItems, CreateItem, UpdateItem, DeleteItem, OpenItem, OpenAllInCollection, ReorderItems } from '../../bindings/quickdock/services/appservice'
import { ListTools, GetValue, SetValue } from '../../bindings/quickdock/services/appservice'
import type { CollectionItem as BindingCollectionItem } from '../../bindings/quickdock/internal/db/models'
import type { Workspace, Scene, Collection, CollectionItem, OpenTool } from '../types'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'

// 资源类型 → 推荐打开工具类型映射
const TYPE_TOOL_MAP: Record<string, string> = {
  '目录': '系统',
  '文件': '系统',
  '网页': '浏览器',
  '命令': '终端',
  '应用': '系统',
  '快速链接': '浏览器',
}

export const useWorkspaceStore = defineStore('workspace', () => {
  const activeWorkspaceId = ref('')
  const activeSceneId = ref('')
  const activeCollectionId = ref('')
  const openedSceneIds = ref<string[]>([]) // 已打开的场景标签页

  const workspaces = ref<Workspace[]>([])
  const scenes = ref<Scene[]>([])
  const collections = ref<Collection[]>([])
  const items = ref<CollectionItem[]>([])
  const tools = ref<OpenTool[]>([])

  const loading = ref(false)
  const error = ref('')

  // ---- 请求世代计数器（防止异步请求竞争） ----
  // 每次 selectWorkspace/selectScene/selectCollection 递增对应计数器，
  // fetch 回调检查世代号是否匹配，不匹配则丢弃过期结果。
  let workspaceGen = 0
  let sceneGen = 0
  let collectionGen = 0

  // ---- 搜索 ----
  const searchQuery = ref('')

  const searchResults = computed(() => {
    const q = searchQuery.value.trim().toLowerCase()
    if (!q) return null
    const matchedScenes: Scene[] = []
    const matchedCollections: Collection[] = []
    const matchedItems: CollectionItem[] = []
    for (const s of scenes.value) {
      if (s.name.toLowerCase().includes(q)) matchedScenes.push(s)
    }
    for (const c of collections.value) {
      if (c.name.toLowerCase().includes(q)) matchedCollections.push(c)
    }
    for (const i of items.value) {
      if (i.name.toLowerCase().includes(q) || (i.value && i.value.toLowerCase().includes(q)))
        matchedItems.push(i)
    }
    return { scenes: matchedScenes, collections: matchedCollections, items: matchedItems }
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
    const preferredToolType = TYPE_TOOL_MAP[itemType] || '系统'
    const candidates = tools.value.filter(t => t.type === preferredToolType)
    if (candidates.length > 0) return candidates[0]
    // 回退：任意工具
    if (tools.value.length > 0) return tools.value.find(t => t.isDefault) || tools.value[0]
    return null
  }

  function getToolsForType(itemType: string): OpenTool[] {
    const preferredType = TYPE_TOOL_MAP[itemType] || '系统'
    const matched = tools.value.filter(t => t.type === preferredType)
    // 同时包含系统默认工具
    const systemTools = tools.value.filter(t => t.type === '系统' && !matched.includes(t))
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
        if (lastScene && scenes.value.find(s => s.id === lastScene)) {
          await selectScene(lastScene)
        }
      }
    } catch (_) {}
  }

  async function fetchTools() {
    try {
      const result = unwrap(await ListTools())
      tools.value = normalizeRows(result) as OpenTool[]
    } catch (e) {
      console.error('工具列表加载失败:', e)
    }
  }

  async function fetchWorkspaces() {
    loading.value = true
    try {
      const result = unwrap(await ListWorkspaces())
      workspaces.value = normalizeRows(result) as Workspace[]
      if (workspaces.value.length > 0 && !activeWorkspaceId.value) {
        await selectWorkspace(workspaces.value[0].id)
      }
    } catch (e) {
      error.value = getErrorMessage(e)
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
    try { unwrap(await SetValue('lastWorkspaceId', id)) } catch (_) {}
  }

  async function fetchScenes(workspaceId: string, gen?: number) {
    try {
      const result = unwrap(await ListScenes(workspaceId))
      if (gen !== undefined && gen !== workspaceGen) return
      scenes.value = normalizeRows(result) as Scene[]
    } catch (e) {
      error.value = getErrorMessage(e)
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
    }
    const gen = sceneGen
    await fetchCollections(id, gen)
    try { unwrap(await SetValue('lastSceneId', id)) } catch (_) {}
  }

  async function closeSceneTab(id: string) {
    const idx = openedSceneIds.value.indexOf(id)
    if (idx < 0) return
    openedSceneIds.value.splice(idx, 1)
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

  // ---- 拖拽排序 ----
  async function reorderScenes(orderedIDs: string[]) {
    // 先同步 sort 字段，使 sortedScenes computed 重算后得到正确顺序
    orderedIDs.forEach((id, idx) => {
      const scene = scenes.value.find(s => s.id === id)
      if (scene) scene.sort = idx * 10
    })
    scenes.value.sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))

    try {
      await ReorderScenes(orderedIDs)
    } catch (e) {
      if (activeWorkspaceId.value) await fetchScenes(activeWorkspaceId.value)
    }
  }

  async function reorderCollections(orderedIDs: string[]) {
    // 先同步 sort 字段
    orderedIDs.forEach((id, idx) => {
      const col = collections.value.find(c => c.id === id)
      if (col) col.sort = idx * 10
    })
    collections.value.sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))

    try {
      await ReorderCollections(orderedIDs)
    } catch (e) {
      if (activeSceneId.value) await fetchCollections(activeSceneId.value)
    }
  }

  async function reorderItems(orderedIDs: string[]) {
    // 先同步 sort 字段
    orderedIDs.forEach((id, idx) => {
      const item = items.value.find(i => i.id === id)
      if (item) item.sort = idx * 10
    })
    items.value.sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))

    try {
      await ReorderItems(orderedIDs)
    } catch (e) {
      if (activeCollectionId.value) await fetchItems(activeCollectionId.value)
    }
  }

  async function fetchCollections(sceneId: string, gen?: number) {
    try {
      const result = unwrap(await ListCollections(sceneId))
      if (gen !== undefined && gen !== sceneGen) return
      collections.value = normalizeRows(result) as Collection[]
    } catch (e) {
      error.value = getErrorMessage(e)
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
    try {
      const result = unwrap(await ListItems(collectionId))
      if (gen !== undefined && gen !== collectionGen) return
      items.value = normalizeRows(result) as CollectionItem[]
    } catch (e) {
      error.value = getErrorMessage(e)
    }
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

  async function addScene(name: string, type: string) {
    const s = unwrap(await CreateScene(activeWorkspaceId.value, name, type))
    const normalized = normalizeRows([s])[0] as Scene
    scenes.value.push(normalized)
    return normalized
  }

  async function addCollection(name: string, type: string, openStrategy = 'single') {
    const c = unwrap(await CreateCollection(activeWorkspaceId.value, activeSceneId.value, name, type, openStrategy))
    const normalized = normalizeRows([c])[0] as Collection
    collections.value.push(normalized)
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
    const idx = scenes.value.findIndex(s => s.id === id)
    if (idx >= 0) Object.assign(scenes.value[idx], updates, { updatedAt: new Date().toISOString() })
  }

  async function updateCollectionAction(id: string, updates: Record<string, any>) {
    unwrap(await UpdateCollection(id, toSnake(updates)))
    const idx = collections.value.findIndex(c => c.id === id)
    if (idx >= 0) Object.assign(collections.value[idx], updates, { updatedAt: new Date().toISOString() })
  }

  async function updateItemAction(id: string, updates: Record<string, any>) {
    unwrap(await UpdateItem(id, toSnake(updates)))
    const idx = items.value.findIndex(i => i.id === id)
    if (idx >= 0) Object.assign(items.value[idx], updates, { updatedAt: new Date().toISOString() })
  }

  // ---- 删除 ----

  async function removeScene(id: string) {
    unwrap(await DeleteScene(id))
    scenes.value = scenes.value.filter(s => s.id !== id)
    if (activeSceneId.value === id) activeSceneId.value = ''
  }

  async function removeCollection(id: string) {
    unwrap(await DeleteCollection(id))
    collections.value = collections.value.filter(c => c.id !== id)
    if (activeCollectionId.value === id) activeCollectionId.value = ''
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

  // ---- 搜索 ----

  function setSearch(query: string) {
    searchQuery.value = query
  }

  function clearSearch() {
    searchQuery.value = ''
  }

  // ---- 辅助函数 ----

  function normalizeRows(rows: any[] | null): any[] {
    return (rows ?? []).map(row => {
      const out: Record<string, any> = {}
      for (const [k, v] of Object.entries(row)) {
        const camel = k.replace(/_([a-z])/g, (_, c) => c.toUpperCase())
        out[camel] = v
      }
      return out
    })
  }

  function toSnake(obj: Record<string, any>): Record<string, any> {
    const out: Record<string, any> = {}
    for (const [k, v] of Object.entries(obj)) {
      const snake = k.replace(/[A-Z]/g, c => '_' + c.toLowerCase())
      out[snake] = v
    }
    return out
  }

  return {
    activeWorkspaceId, activeSceneId, activeCollectionId, openedSceneIds,
    workspaces, scenes, collections, items, tools,
    loading, error,
    searchQuery, hasSearch, searchResults,
    activeWorkspace, activeScene, activeCollection,
    sortedScenes, filteredCollections, filteredItems,
    initialize, fetchTools, fetchWorkspaces, fetchScenes, fetchCollections, fetchItems,
    selectWorkspace, selectScene, selectCollection, closeSceneTab,
    closeTabsToLeft, closeTabsToRight, closeOtherTabs,
    reorderScenes, reorderCollections, reorderItems,
    addWorkspace, addScene, addCollection, addItem,
    removeWorkspace,
    updateWorkspaceAction,
    reorderWorkspaces,
    updateSceneAction, updateCollectionAction, updateItemAction,
    removeScene, removeCollection, removeItem,
    openItem, openAllInCollection,
    setSearch, clearSearch,
    getDefaultToolForType, getToolsForType,
  }
})
