// useDashPrefs.ts — 总览卡片的「显隐 + 排序」本地偏好
//
// 与 useNavPrefs 同一套取向：卡片清单是代码的真相，localStorage 里只存偏好；
// 读出来后与清单求交集，已删除的卡片 id 自动丢弃、新增卡片自动补进来。
// 这样以后增删卡片，不会因为本地残留脏 id 出现「卡片失踪」。
//
// 单例：显隐与排序是应用级偏好，配置浮层与卡片网格必须看到同一份，
// 不引 pinia —— 两个 key、无异步，一个 ref 对就够。
//
// 「隐藏不留空位」靠模板层实现：卡片块整体 v-if，span 网格里少一个节点，
// 后面的卡片自然补位；偏好本身只负责给出可见 id 与顺序。

import { computed, ref } from 'vue'
import { writeStorageDebounced } from '../utils/storageQueue'

/** 总览卡片 id（模板里每张 section.card 的 data-dash-card 必须与此一一对应） */
export const DASH_CARDS = [
  'next', 'recent', 'shelf', 'todo', 'mail', 'ports', 'resident', 'git', 'monitor', 'ai',
] as const
export type DashCardId = (typeof DASH_CARDS)[number]

const HIDDEN_KEY = 'qd_dash_hidden'
const ORDER_KEY = 'qd_dash_order'

function readList(key: string): string[] {
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return []
    const arr: unknown = JSON.parse(raw)
    return Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return [] // 偏好坏了就当没有，退回代码里的默认顺序
  }
}

const hiddenIds = ref<string[]>(readList(HIDDEN_KEY))
const orderIds = ref<string[]>(readList(ORDER_KEY))

/** 与 DASH_CARDS 求交集后的完整顺序：先按用户排过的 id，再按清单顺序补齐新卡。 */
const orderedCards = computed<DashCardId[]>(() => {
  const known = new Set<string>(DASH_CARDS)
  const placed = new Set<string>()
  const out: DashCardId[] = []

  for (const id of orderIds.value) {
    if (!known.has(id) || placed.has(id)) continue
    out.push(id as DashCardId)
    placed.add(id)
  }
  for (const id of DASH_CARDS) {
    if (placed.has(id)) continue
    out.push(id)
    placed.add(id)
  }
  return out
})

const visibleCards = computed<DashCardId[]>(() =>
  orderedCards.value.filter((id) => !hiddenIds.value.includes(id)))

const hiddenCards = computed<DashCardId[]>(() =>
  orderedCards.value.filter((id) => hiddenIds.value.includes(id)))

function persist() {
  writeStorageDebounced(HIDDEN_KEY, JSON.stringify(hiddenIds.value))
  writeStorageDebounced(ORDER_KEY, JSON.stringify(orderedCards.value))
}

function hideCard(id: DashCardId): boolean {
  if (hiddenIds.value.includes(id)) return true
  // 至少留一张可见卡，否则卡片区空掉看起来像加载失败
  if (visibleCards.value.length <= 1) return false
  hiddenIds.value = [...hiddenIds.value, id]
  persist()
  return true
}

function showCard(id: DashCardId) {
  if (!hiddenIds.value.includes(id)) return
  hiddenIds.value = hiddenIds.value.filter((x) => x !== id)
  persist()
}

function showAllCards() {
  if (!hiddenIds.value.length) return
  hiddenIds.value = []
  persist()
}

/** 上移/下移以完整顺序（含隐藏卡）为基准，恢复的卡才回得到原位 */
function moveCard(id: DashCardId, dir: -1 | 1): boolean {
  const ids = orderedCards.value.slice()
  const i = ids.indexOf(id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= ids.length) return false
  ;[ids[i], ids[j]] = [ids[j], ids[i]]
  orderIds.value = ids
  persist()
  return true
}

function canMoveUp(id: DashCardId) { return orderedCards.value.indexOf(id) > 0 }
function canMoveDown(id: DashCardId) {
  const i = orderedCards.value.indexOf(id)
  return i >= 0 && i < orderedCards.value.length - 1
}

function resetCards() {
  if (!hiddenIds.value.length && !orderIds.value.length) return false
  hiddenIds.value = []
  orderIds.value = []
  persist()
  return true
}

export const dashPrefs = {
  orderedCards, visibleCards, hiddenCards,
  hideCard, showCard, showAllCards, moveCard,
  canMoveUp, canMoveDown, resetCards,
}
