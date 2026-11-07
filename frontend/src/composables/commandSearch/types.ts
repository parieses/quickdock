// 命令面板检索的公共类型与常量。
//
// 单独成文件的原因：matchers / ranking / useCommandSearch 三方都要用这些类型，
// 放在 useCommandSearch 里会形成「matcher 反向 import 编排层」的环。
// useCommandSearch.ts 仍然 re-export 这些类型，外部调用方无需改 import 路径。

import type { CollectionItem } from '../../types'

export type ResultType =
  | 'item' | 'system' | 'quicklink' | 'quicklink-inline' | 'calculator'
  | 'note' | 'todo' | 'app' | 'plugin' | 'url' | 'clipboard-action' | 'best' | 'env' | 'file' | 'nav'
  // 场景 / 工作空间集合：切到 workspace 页并定位到条目；create：新建笔记 / 待办动作
  | 'scene' | 'collection' | 'create'

export interface SearchResult {
  type: ResultType
  label: string
  desc?: string
  icon?: any
  iconBase64?: string
  item?: CollectionItem
  cmd?: any
  calcResult?: string
  note?: CmdNote
  todo?: CmdTodo
  inlineQuery?: string
  frecencyScore?: number
  appPath?: string
  appCategory?: string
  pluginId?: string
  pluginCommandId?: string
  pluginHasFrontend?: boolean
  pluginNotRunning?: boolean
  inlineInput?: string
  /** 正则 / 内容嗅探命中的原文：前端插件靠 plugin:init.text 取用（与 acceptsInput 无关） */
  hitText?: string
  pluginResult?: string
  acceptsInput?: boolean
  score?: number
  /**
   * 组内排序权重（命中分 + 频次的加权结果），由 matcher 在入列时算好。
   *
   * 存在的原因：「最佳匹配」按裸分排、插件组按 `score×0.7 + 频次×0.3` 排，
   * 同一条命令在两处的位次会对不上（组内第一，best 里却排在别人后面）。
   * ranking 优先读这个字段，没有才回退到 score。
   */
  rankScore?: number
  matchType?: string
  url?: string
  clipAction?: string
  // 环境管理运行时服务（Ctrl+K 直控启停/管理后台）
  envRuntimeId?: string
  envVersion?: string
  envAction?: 'start' | 'stop' | 'mgmt'
  // Everything 文件搜索结果（效率工具）
  filePath?: string
  fileDir?: string
  fileName?: string
  // 「快速导航」：软件内部页面（与 pages/registry 的 id 同源）
  navPage?: string
  // 场景 / 集合定位：回车切到 navPage('workspace') 后，主窗口据此停在对应条目上。
  // 面板是独立浮窗，与主窗口 JS 状态不互通，只能把目标 id 随事件带过去。
  navTarget?: string
  scene?: CmdScene
  collection?: CmdCollection
  // 新建动作：note=新建笔记 / todo=新建待办，回车进入内联标题输入态
  createKind?: 'note' | 'todo'
}

// 场景 / 集合的检索项：只留匹配与定位需要的字段。matcher 是纯函数，不该拖进
// 完整的领域对象；定位所需的 workspaceId / sceneId 一并带上（主窗口切页后要用）。
export interface CmdScene {
  id: string
  name: string
  description?: string
  workspaceId?: string
}

export interface CmdCollection {
  id: string
  name: string
  description?: string
  workspaceId?: string
  sceneId?: string
}

export interface CmdNote {
  id: string
  name: string
  content: string
  isFolder: boolean
  parentId: string
  keyword: string
}

// 待办命中项：命令面板只做「检索 + 跳转」，展示用字段够用即可，
// 完整字段由待办页自行按 id 加载（避免每次输入都把备注/重复配置全量拉回来）。
export interface CmdTodo {
  id: string
  title: string
  status: string
  priority: string
  dueDate: string
  tags: string
}

export interface SystemCmd {
  id: string
  label: string
  desc: string
  keywords: string[]
  icon: any
  action: () => Promise<void>
}

export interface InstalledApp {
  name: string
  path: string
  category: string
  iconBase64?: string
}

export interface ResultGroup {
  type: ResultType
  label: string
  results: SearchResult[]
  totalCount?: number // 「最近使用」折叠时的全量条数（徽标展示用）
}

/** 「快速导航」检索项：registry 的内容页里挑出的可检索部分（id / 文案 key / 图标）。
 *  单独定义是为了让 matcher 只依赖数据 —— 直接 import registry 会把它的 SFC 依赖拖进
 *  纯函数模块（单测环境编译不了 SFC）。 */
export interface NavSearchItem {
  id: string
  labelKey: string
  icon: any
}

export interface RecentEntry {
  key: string
  type: string
  label: string
  description: string
  input?: string
  count: number
  lastUsed: number
}

// ---- 预计算索引 ----
//
// 这些条目把「与 query 无关」的工作提前算好：小写化、应用别名展开。
// 原先每个 query 变化都要对全部 items/notes/apps/systemCommands 重新
// toLowerCase()，以及在 apps 上重新跑 getAppAliases() —— 数据量大时
// 是纯白烧的 O(N × 字符串长度)，与本轮 query 无关。
// Vue computed 保证只有底层数据变化时才重建索引。

export interface LcItemEntry { item: CollectionItem; nameLC: string; valueLC: string }
export interface LcNoteEntry { note: CmdNote; nameLC: string; contentLC: string }
// 别名保留原文和小写两份：原文给 pinyinMatch（它内部自己归一化，但保持入参不变最安全），
// 小写给 includes 直接比，省掉每轮 query 的重新 toLowerCase。
export interface LcAlias { raw: string; lc: string }
export interface LcAppEntry { app: InstalledApp; nameLC: string; aliases: LcAlias[] }
export interface LcSysEntry { cmd: SystemCmd; labelLC: string }

// 场景 / 集合的预计算小写索引：与 items/notes 同构，逐键输入时不再重复 toLowerCase。
export interface LcSceneEntry { scene: CmdScene; nameLC: string; descLC: string }
export interface LcCollectionEntry { collection: CmdCollection; nameLC: string; descLC: string }

export interface LcIndex {
  items: LcItemEntry[]
  notes: LcNoteEntry[]
  apps: LcAppEntry[]
  sys: LcSysEntry[]
  // 可选：matchers.test.ts 构造的最小 LcIndex 只给四件套，生产侧
  // （useCommandSearch.lcIndex）始终填齐。matcher 内一律用 `|| []` 兜底，
  // 缺字段就等于「这类条目一条都没有」，不会让整套检索炸掉。
  scenes?: LcSceneEntry[]
  collections?: LcCollectionEntry[]
}

// ---- 环境运行时的中文别名 ----

// 运行时的中文分类别名，用于 Ctrl+K 中文检索（如「数据库」「缓存」「消息队列」）。
export const ENV_GROUP_ALIASES: Record<string, string[]> = {
  language: ['语言', '运行时', 'runtime', '开发'],
  webserver: ['网页', 'web', 'web服务器', '服务器', '反向代理', '网关'],
  cache: ['缓存', 'cache', '内存'],
  database: ['数据库', 'database', 'db'],
  tool: ['工具', 'tool', '实用'],
  messagequeue: ['消息', '队列', 'mq', '消息队列'],
}

// 各运行时的中文/语义别名，补充英文 id/name 之外的检索入口。
export const ENV_RUNTIME_ALIASES: Record<string, string[]> = {
  redis: ['缓存', 'redis', '内存数据库'],
  memcached: ['缓存', 'memcache'],
  nginx: ['网页', '反向代理', 'nginx', 'web服务器'],
  caddy: ['网页', '反向代理', 'caddy'],
  traefik: ['网页', '反向代理', '网关', 'traefik'],
  apache: ['网页', 'apache', 'web服务器'],
  postgresql: ['数据库', 'postgres', 'pg'],
  mysql: ['数据库', 'mysql'],
  mariadb: ['数据库', 'mariadb'],
  mongodb: ['数据库', '文档数据库', 'mongo'],
  rabbitmq: ['消息', '队列', 'mq', '消息队列', 'rabbit'],
  minio: ['对象存储', '存储', 'minio', 's3'],
  mailpit: ['邮件', 'mail', '邮件服务'],
  frpc: ['内网穿透', '穿透', 'frp'],
  ftp: ['ftp', '文件服务'],
}

// 待办结果的副标题：一行里挑最有用的那条信息——先期限（含逾期），再标签。
// 期限比标签更容易决定「现在该处理哪条」，所以排在前面；两者都没有就留空，
// 让标题独占一行（硬凑一个占位词反而把标题挤窄）。
export function todoSubtitle(td: CmdTodo, t: (k: string, n?: any) => string): string {
  if (td.dueDate) {
    // 必须用本地日期而不是 toISOString()：后者是 UTC，UTC+8 的早上八点前
    // 会把"今天到期"判成"已逾期"。
    const d = new Date()
    const today = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    const prefix = td.dueDate < today ? t('todoOverdue') : t('todoDue')
    return `${prefix} ${td.dueDate}`
  }
  try {
    const tags = JSON.parse(td.tags || '[]')
    if (Array.isArray(tags) && tags.length) return tags.map((x: unknown) => '#' + String(x)).join(' ')
  } catch { /* tags 是手写 JSON，坏数据不该让整条结果消失 */ }
  return ''
}
