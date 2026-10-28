// 剪贴板历史「标签筛选」的判定规则。
//
// 抽成独立模块的原因：这段判定曾出过一处真 bug——image/file 标签的 match
// 本身就是 contentType 判断，却和其余标签共用了一层
// `e.contentType === 'text' &&` 前置条件。两个条件互斥 → 点「图片」标签
// 永远筛不出任何条目（用户反馈「图片文件筛选不生效」）。
//
// 根因是「text 前置条件」被当成了全局前提，其实它只对
// url/email/json/code/phone 这几个**智能分类标签**成立——它们靠正则判断
// 文本内容，对图片/文件条目没有意义；而 image/file 是 content_type 里的
// **独立整类**，不是 text 的子类。
//
// 三类标签的判定语义各不相同，故显式区分，避免再被合并成一条分支：
//
//  1. all       —— 不过滤
//  2. favorite  —— isPinned === 1（收藏项可能是任意类型，故不限 contentType）
//  3. image/file—— contentType 精确等于该类型（整类匹配）
//  4. 智能分类   —— 入库时已按文本正则算出 text_type（url/email/json/code/phone），
//                  筛选阶段直接下推到 SQL（WHERE content_type='text' AND text_type=?），
//                  既快又覆盖全量历史；前端仅用存储的 text_type 做一遍校验兜底。

/** 剪贴板条目的最小结构（本模块只依赖这几个字段，便于单测构造）。 */
export interface FilterableEntry {
  id: string
  contentType: string
  textContent: string
  isPinned: number
  createdAt: number
  /** 文本智能分类（url/email/json/code/phone）；非文本条目无此字段。 */
  textType?: string
}

/** 标签 id。 */
export type ClipTagId =
  | 'all'
  | 'favorite'
  | 'image'
  | 'file'
  | 'url'
  | 'email'
  | 'json'
  | 'code'
  | 'phone'

/**
 * loadEntries 把标签筛选下推到 SQL 的方式：
 *  - none       ：全量载入（all / favorite），前端再按 isPinned 等逻辑过滤
 *  - contentType：按 content_type 整类过滤（image / file）
 *  - smartType  ：按 text_type 智能分类过滤（url/email/json/code/phone），
 *                 content_type 固定为 text
 */
export type TagPush =
  | { push: 'none' }
  | { push: 'contentType'; value: string }
  | { push: 'smartType'; value: string }

/** 智能分类标签：靠文本内容正则判断，对非文本条目无意义。 */
const SMART_TEXT_TAGS = ['url', 'email', 'json', 'code', 'phone'] as const

/** 直接下推到 content_type 整类的标签（图片/文件是 content_type 已有的两类）。 */
const SQL_TYPE_TAGS: Record<string, string> = { image: 'image', file: 'file' }

/**
 * 该标签是否是「智能分类标签」（只对文本条目有意义）。
 */
export function isSmartTextTag(tagId: string): boolean {
  return (SMART_TEXT_TAGS as readonly string[]).includes(tagId)
}

/**
 * 当前标签对应的 SQL 下推方式；all/favorite 返回 none。
 *
 * 注意：image/file **不是** text 的子类，不能套 text 前置条件；
 * 智能分类下推的是 text_type（不是 content_type='text'），这样切换 url↔email
 * 也会带着不同的 text_type 重新查库，而不是在已载入的 text 集合里再筛。
 */
export function queryPushForTag(tagId: string): TagPush {
  if (SQL_TYPE_TAGS[tagId]) return { push: 'contentType', value: SQL_TYPE_TAGS[tagId] }
  if (isSmartTextTag(tagId)) return { push: 'smartType', value: tagId }
  return { push: 'none' }
}

/**
 * 下推方式的「完整签名」（push 类型 + 值），用于比较两次标签切换是否真的换了 SQL。
 *
 * TagPush 是联合类型，`none` 分支没有 value 字段，不能直接取 `push.value`；
 * 这里用 `in` 收窄，调用方（ClipboardPanel 的 watch）就不必各自处理一次。
 */
export function tagPushSignature(push: TagPush): string {
  return push.push + ':' + ('value' in push ? push.value : '')
}

/**
 * 文本智能分类（与后端 internal/db/clipboard.go 的 classifyTextType 同构）。
 * 顺序即优先级：email > url > phone > json > code。空串表示普通文本。
 *
 * 后端入库时已算好并存入 text_type，前端筛选优先用存储值；这里作为兜底
 * （老数据未回填 / 内存构造的临时条目）复算一遍，保证前后端判定一致。
 */
export function classifyTextType(text: string): string {
  const t = (text || '').trim()
  if (!t) return ''
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(t)) return 'email'
  if (/^https?:\/\/[^\s]+$/.test(t)) return 'url'
  if (/^1[3-9]\d{9}$/.test(t)) return 'phone'
  if ((t.startsWith('{') || t.startsWith('[')) && (t.endsWith('}') || t.endsWith(']'))) return 'json'
  if (t.includes('\n') || /^(function|const|let|var|import|export|class|def|#include|public|private)\b/.test(t)) return 'code'
  return ''
}

/**
 * 按标签过滤剪贴板条目。
 *
 * @param list 已载入的条目（智能分类标签下已按 text_type 下推 SQL，这里只做兜底校验）
 * @param tagId 当前标签
 */
export function filterByTag<T extends FilterableEntry>(
  list: T[],
  tagId: string,
): T[] {
  if (!tagId || tagId === 'all') return list
  if (tagId === 'favorite') return list.filter(e => e.isPinned === 1)
  if (isSmartTextTag(tagId)) {
    // 后端已按 text_type 下推；此处用存储类型做一遍校验，旧数据无 text_type 时回退正则。
    return list.filter(e => {
      const t = (e.textType && e.textType !== '') ? e.textType : classifyTextType(e.textContent)
      return t === tagId
    })
  }
  // image/file：content_type 精确匹配（整类），**不套 text 前置条件**。
  return list.filter(e => e.contentType === tagId)
}
