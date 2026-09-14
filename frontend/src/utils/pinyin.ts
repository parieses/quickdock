import { pinyin } from 'pinyin-pro'

// 拼音搜索的公共实现。
//
// 此前有两份重复实现：CommandPalette.vue 里带缓存的 pinyinMatch（供命令面板搜
// 快捷项/笔记/应用/系统命令/运行时），以及 usePluginIndex.ts 里为插件命令索引
// 预计算 pinyinTitleFull / pinyinTitleInit 的一批 pinyin() 调用。两处逻辑相同
// 但各自为政，导致其余 6 个搜索框（剪贴板、笔记管理、插件市场、侧栏、环境页、
// 配置编辑器）根本用不上拼音搜索。统一收敛到这里。

export interface PinyinParts {
  /** 每个拼音音节的首字母连成的串，如「环境管理」→ hjgl */
  init: string
  /** 全拼连成的串，如「环境管理」→ huanjingguanli */
  full: string
}

const EMPTY: PinyinParts = { init: '', full: '' }

/**
 * 计算一段文本的拼音部件。非中文字符 pinyin-pro 原样返回，故中英混排同样可用。
 */
export function pinyinParts(text: string): PinyinParts {
  if (!text) return EMPTY
  const arr = pinyin(text, { toneType: 'none', type: 'array' })
  // 空音节（如纯标点）p[0] 是 undefined，用 charAt 兜底避免拼出 "undefined"
  return {
    init: arr.map(p => p.charAt(0)).join('').toLowerCase(),
    full: arr.join('').toLowerCase(),
  }
}

// 缓存：命令面板会随输入逐键对全量条目做匹配，不缓存时每次都重算整表拼音。
// 缓存键由调用方给定（形如 'i:'+id），重建索引时调 clearPinyinCache 清空。
const cache = new Map<string, PinyinParts>()

export function clearPinyinCache(): void {
  cache.clear()
}

/**
 * 取文本的拼音部件。给了 cacheKey 就走缓存（读时没有才计算并写入）。
 */
export function pinyinOf(text: string, cacheKey?: string): PinyinParts {
  if (!text) return EMPTY
  if (!cacheKey) return pinyinParts(text)
  const hit = cache.get(cacheKey)
  if (hit) return hit
  const parts = pinyinParts(text)
  cache.set(cacheKey, parts)
  return parts
}

/**
 * 拼音是否命中查询串。
 * 判定：首字母串前缀命中（输入 hjgl 命中「环境管理」），或全拼包含（输入 huanjing 命中）。
 * 查询串大小写不敏感，内部自行归一。
 */
export function pinyinHit(parts: PinyinParts, query: string): boolean {
  if (!query) return false
  if (!parts.init && !parts.full) return false
  const q = query.toLowerCase()
  return parts.init.startsWith(q) || parts.full.includes(q)
}

/**
 * 文本的拼音是否命中查询串。pinyinHit 的便捷封装。
 */
export function pinyinMatch(text: string, query: string, cacheKey?: string): boolean {
  if (!text || !query) return false
  return pinyinHit(pinyinOf(text, cacheKey), query)
}

/**
 * 通用的「原文 or 拼音」匹配：任意一段原文包含查询串，或任意一段的拼音命中即算命中。
 * 供剪贴板/侧栏/插件市场等「多字段列表过滤」直接使用，大小写不敏感。
 */
export function matchesTextOrPinyin(fields: (string | undefined | null)[], query: string, cacheKey?: string): boolean {
  if (!query) return true
  const q = query.trim().toLowerCase()
  if (!q) return true
  for (const f of fields) {
    if (f && f.toLowerCase().includes(q)) return true
  }
  return fields.some((f, i) => (f ? pinyinMatch(f, q, cacheKey ? cacheKey + '#' + i : undefined) : false))
}
