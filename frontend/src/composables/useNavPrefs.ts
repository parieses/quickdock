// useNavPrefs.ts — 侧边栏导航项的「显隐 + 排序」本地偏好
//
// 存在的理由：一级导航会随功能增加越来越长，历史教训是加到一定程度就挤掉右侧场景区。
// 于是给用户裁剪能力 —— 但裁剪必须可逆，否则「隐藏」就等于「功能失踪」。
//
// 三条恢复路径中的前两条由本模块提供数据：
//   1. 侧栏底部的「已隐藏 (n)」折叠组（hiddenNav）就地恢复；
//   2. 设置 → 导航（SettingsNav）的全量开关列表，它本身不属于导航项，因此永远可达。
//   3. Ctrl+K 仍可搜到被隐藏的页面名并打开 —— 隐藏只裁剪侧栏，不删能力。
//
// 数据自净（关键）：order / hidden 只是偏好，真相始终在 pages/registry.ts。
// 读出来后全部按 registry 求交集：已删除的 id 自动丢弃，新增的项自动补进来。
// 这样以后往 registry 增删导航项，不会因为本地残留脏 id 出现幽灵项或「新页面不显示」。
//
// 共享方式是**显式单例**：显隐/排序是应用级偏好，不是某个组件的局部状态 —— Sidebar 与
// SettingsNav 必须看到同一份。所以 store 在本模块创建一次，useNavPrefs() 只负责把它交出去。
// 不引 pinia：这里只有两个 key、无异步、无复杂派生缓存，一个 ref 对就够，多一层框架反而是负担。
//
// 新项的落位（锚点插入）：order 里只会有「用户排过的项」，未出现过的 id 一律视为 registry
// 新增，插到它在 registry 里的**前一个兄弟之后**，而不是甩到末尾。否则用户只要拖过一次，
// registry 里声明的位置就永久失效，新功能永远长在侧栏最下面。

import { computed, ref } from 'vue'
import { navEntries, type NavEntry } from '../pages/registry'
import { writeStorageDebounced } from '../utils/storageQueue'

const HIDDEN_KEY = 'qd_nav_hidden'
const ORDER_KEY = 'qd_nav_order'

function readList(key: string): string[] {
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return []
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return [] // 偏好坏了就当没有，退化成 registry 原序，不影响使用
  }
}

// 写入合并（150ms、同 key 只留最后一份）交给 utils/storageQueue：
// 拖拽排序会连续触发 persist()，每次同步 JSON.stringify + setItem 偏贵。
const hiddenIds = ref<string[]>(readList(HIDDEN_KEY))
const orderIds = ref<string[]>(readList(ORDER_KEY))

/**
 * registry 为准的全量有序列表：
 * 1) 先按本地 order 排（registry 已删的 id 自然丢弃，顺带去掉本地脏重复）；
 * 2) 再把「order 里没有的项」按 registry 的语义位置锚点插入。
 */
const orderedNav = computed<NavEntry[]>(() => {
  const byId = new Map(navEntries.map((e) => [e.id, e]))
  const placed = new Set<string>()
  const out: NavEntry[] = []

  for (const id of orderIds.value) {
    const e = byId.get(id)
    if (!e || placed.has(id)) continue
    out.push(e)
    placed.add(id)
  }

  // 未出现在 order 里的 = 用户没见过的（registry 新增）。
  // 按 registry 顺序遍历：连续新增的几项能互相成为前驱，相对顺序也保持住。
  for (let i = 0; i < navEntries.length; i++) {
    const e = navEntries[i]
    if (placed.has(e.id)) continue
    let at = 0 // 没有前驱（新项排在 registry 最前）就插到列表头
    for (let j = i - 1; j >= 0; j--) {
      const p = out.findIndex((x) => x.id === navEntries[j].id)
      if (p >= 0) {
        at = p + 1
        break
      }
    }
    out.splice(at, 0, e)
    placed.add(e.id)
  }

  return out
})

/** 侧栏可见项（默认渲染这个） */
const visibleNav = computed<NavEntry[]>(() =>
  orderedNav.value.filter((e) => !hiddenIds.value.includes(e.id)),
)

/** 侧栏被隐藏项（显示在底部折叠组里，随时可恢复） */
const hiddenNav = computed<NavEntry[]>(() =>
  orderedNav.value.filter((e) => hiddenIds.value.includes(e.id)),
)

function persist() {
  writeStorageDebounced(HIDDEN_KEY, JSON.stringify(hiddenIds.value))
  // order 存全量而非可见项：隐藏项保留自己的位置，恢复时原地回归，不会跳到末尾。
  // 注意这一步把「锚点算出来的位置」也写成了显式顺序 —— 用户真的动过排序之后，
  // registry 再调整既有项的位置就不再影响他了（用户的手工排列优先，这是有意的）。
  writeStorageDebounced(ORDER_KEY, JSON.stringify(orderedNav.value.map((e) => e.id)))
}

/** 隐藏某项。至少留一个可见项，否则 nav 区空掉看起来像坏了 */
function hideNav(id: string): boolean {
  if (hiddenIds.value.includes(id)) return true
  if (visibleNav.value.length <= 1) return false
  hiddenIds.value = [...hiddenIds.value, id]
  persist()
  return true
}

function showNav(id: string) {
  if (!hiddenIds.value.includes(id)) return
  hiddenIds.value = hiddenIds.value.filter((x) => x !== id)
  persist()
}

function showAllNav() {
  if (hiddenIds.value.length === 0) return
  hiddenIds.value = []
  persist()
}

/** 把 id 移动到目标下标处的相邻位置：dir = -1 上移，1 下移 */
function moveNav(id: string, dir: -1 | 1): boolean {
  const ids = orderedNav.value.map((e) => e.id)
  const i = ids.indexOf(id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= ids.length) return false
  ;[ids[i], ids[j]] = [ids[j], ids[i]]
  orderIds.value = ids
  persist()
  return true
}

/**
 * 拖拽排序：直接采用调用方算好的完整顺序。
 * 调用方是 useDragSort，它产出的一定是「同一批 id 的排列」；这里仍校验一遍，
 * 因为 orderIds 是持久化数据，写进一份缺项/多项的列表会让侧栏丢项（极难查）。
 */
function reorderNav(ids: string[]): boolean {
  const current = orderedNav.value.map((e) => e.id)
  if (ids.length !== current.length) return false
  const known = new Set(current)
  if (ids.some((id) => !known.has(id))) return false
  if (ids.every((id, i) => id === current[i])) return false
  orderIds.value = [...ids]
  persist()
  return true
}

// 上下移动都以「全量有序列表」的下标为准：顺序包含隐藏项，
// 恢复项才会回到它原来的位置，而不是被当成新项追加到末尾。
function canMoveUp(id: string): boolean {
  return orderedNav.value.findIndex((e) => e.id === id) > 0
}
function canMoveDown(id: string): boolean {
  const i = orderedNav.value.findIndex((e) => e.id === id)
  return i >= 0 && i < orderedNav.value.length - 1
}

/** 恢复出厂：清空显隐与排序偏好，完全回到 registry 顺序 */
function resetNav() {
  const changed = hiddenIds.value.length > 0 || orderIds.value.length > 0
  hiddenIds.value = []
  orderIds.value = []
  persist()
  return changed
}

/**
 * 唯一的 store 实例。写在这一层是为了让「共享」成为声明而不是巧合：
 * 组件取到的是同一个对象，无论调用多少次、从哪个组件调用。
 */
const navPrefs = {
  orderedNav,
  visibleNav,
  hiddenNav,
  hideNav,
  showNav,
  showAllNav,
  moveNav,
  reorderNav,
  canMoveUp,
  canMoveDown,
  resetNav,
}

export function useNavPrefs() {
  return navPrefs
}
