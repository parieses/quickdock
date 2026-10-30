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
// 读出来后全部按 registry 求交集：已删除的 id 自动丢弃，新增的项自动追加到末尾。
// 这样以后往 registry 增删导航项，不会因为本地残留脏 id 出现幽灵项或「新页面不显示」。

import { computed, ref } from 'vue'
import { navEntries, type NavEntry } from '../pages/registry'

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

// 写入合并：拖拽排序 / 显隐会连续触发 persist()，每次同步 JSON.stringify + setItem 偏贵。
// 用 150ms 防抖把高频写合并成一次；模块级 pending 表保证多条 key 一起落盘。
const pendingWrites = new Map<string, string>()
let writeTimer: ReturnType<typeof setTimeout> | null = null
function scheduleWrite(key: string, value: string) {
  pendingWrites.set(key, value)
  if (writeTimer) return
  writeTimer = setTimeout(() => {
    writeTimer = null
    for (const [k, v] of pendingWrites) {
      try {
        localStorage.setItem(k, v)
      } catch {
        /* 隐私模式下写不了，本次会话内仍生效 */
      }
    }
    pendingWrites.clear()
  }, 150)
}

function writeList(key: string, list: string[]) {
  scheduleWrite(key, JSON.stringify(list))
}

const hiddenIds = ref<string[]>(readList(HIDDEN_KEY))
const orderIds = ref<string[]>(readList(ORDER_KEY))

/**
 * registry 为准的全量有序列表：
 * 先按本地 order 排，再补上 registry 里没被 order 覆盖的新项（保证新功能默认可见）。
 */
const orderedNav = computed<NavEntry[]>(() => {
  const byId = new Map(navEntries.map((e) => [e.id, e]))
  const out: NavEntry[] = []
  for (const id of orderIds.value) {
    const e = byId.get(id)
    if (e) out.push(e)
  }
  const seen = new Set(out.map((e) => e.id))
  for (const e of navEntries) if (!seen.has(e.id)) out.push(e)
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
  writeList(HIDDEN_KEY, hiddenIds.value)
  // order 存全量而非可见项：隐藏项保留自己的位置，恢复时原地回归，不会跳到末尾
  writeList(ORDER_KEY, orderedNav.value.map((e) => e.id))
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

/** 设置页拖拽排序：把 id 插到目标位置（拖拽落点语义，非相邻交换） */
function moveNavTo(id: string, targetIndex: number): boolean {
  const ids = orderedNav.value.map((e) => e.id)
  const from = ids.indexOf(id)
  if (from < 0) return false
  const to = Math.max(0, Math.min(ids.length - 1, targetIndex))
  if (from === to) return false
  ids.splice(to, 0, ...ids.splice(from, 1))
  orderIds.value = ids
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

export function useNavPrefs() {
  return {
    orderedNav,
    visibleNav,
    hiddenNav,
    hideNav,
    showNav,
    showAllNav,
    moveNav,
    moveNavTo,
    canMoveUp,
    canMoveDown,
    resetNav,
  }
}
