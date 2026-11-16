/**
 * 条目 / 工具 / 分类的中文规范值常量（DB 持久化值）。
 *
 * ⚠️ 中文是持久化规范值，不是「文案」，绝不能改成英文枚举：
 *   - internal/db/schema.go:76   items.type DEFAULT '目录'
 *   - internal/db/item.go:533    execOpen() 按中文 case 分支决定打开行为
 *   - frontend/src/types/index.ts:93-98   scene/collection/item 中文值说明
 *   - usePaletteExecutor.ts 把整个 item 对象原样回传后端 OpenItem
 * 改英文需先做 Go 与 DB 双向兼容（写入归一化 + 读取兼容 + 老库 / 备份 /
 * 场景包 / 墓碑恢复全链路），禁止只改前端 —— 否则所有条目的打开行为、
 * 历史数据与恢复链路全崩。
 *
 * 本模块只做「常量化 + 类型化 + 只读识别」：值一字未改，不 mutate 任何 item 对象。
 * 显示名一律走 i18n 的 itemTypes 字典（key 就是这些中文值），不在此处翻译。
 */

/** items.type 的中文规范值；顺序与后端 case 分支一致，勿重排。 */
export const ITEM_TYPES = ['目录', '网页', '命令', '文件', '应用', '快速链接'] as const
export type ItemType = typeof ITEM_TYPES[number]

/**
 * 具名单值常量，供散落的 `=== '目录'` 判断与映射键引用。
 * 值全部取自 ITEM_TYPES，顺序无关，永远不会与数组脱节。
 */
export const ITEM_TYPE = {
  DIR: ITEM_TYPES[0],   // 目录
  URL: ITEM_TYPES[1],   // 网页
  CMD: ITEM_TYPES[2],   // 命令
  FILE: ITEM_TYPES[3],  // 文件
  APP: ITEM_TYPES[4],   // 应用
  LINK: ITEM_TYPES[5],  // 快速链接
} as const

/** 打开工具类型（tools.type，与后端 EnsureDefaultTools / TYPE_TOOL_MAP 一致）。 */
export const TOOL_TYPES = {
  SYSTEM: '系统',
  EDITOR: '编辑器',
  BROWSER: '浏览器',
  TERMINAL: '终端',
  OFFICE: 'Office',
} as const

/** 内置「系统默认」哨兵工具名（isDefault=1；前端下拉用空选项代表它）。 */
export const SENTINEL_TOOL_NAME = '系统默认'

/** 已安装应用分类（installed app.category）。 */
export const APP_CATEGORY_OTHER = '其他'
export const APP_CATEGORY_SYSTEM = '系统工具'
export const APP_CATEGORY_APPLICATIONS = '应用程序'

// 英文别名 → 中文规范值：仅用于「识别」，绝不写回 item 对象。
const ITEM_TYPE_ALIASES: Record<string, ItemType> = {
  dir: ITEM_TYPE.DIR,
  url: ITEM_TYPE.URL,
  cmd: ITEM_TYPE.CMD,
  file: ITEM_TYPE.FILE,
  app: ITEM_TYPE.APP,
  link: ITEM_TYPE.LINK,
}

/**
 * 只读识别一个 type 值：
 *   - 中文规范值 → 原样返回；
 *   - 兼容未来可能的英文别名（dir/url/cmd/file/app/link，大小写不敏感）→ 归一化到中文规范值；
 *   - 未知 / 空 → 'unknown'。
 * 只做识别，绝不改写传入对象，也不替换入参；调用方需自行决定如何使用返回值。
 */
export function normalizeItemType(raw: string | undefined | null): ItemType | 'unknown' {
  if (raw == null) return 'unknown'
  const canonical = ITEM_TYPES.find(t => t === raw)
  if (canonical) return canonical
  const alias = ITEM_TYPE_ALIASES[raw.trim().toLowerCase()]
  return alias ?? 'unknown'
}
