// QuickDock - 全局类型定义

export interface Workspace {
  id: string
  name: string
  storage?: string
  remark?: string
  sort?: number
  createdAt?: string
  updatedAt?: string
}

export interface Scene {
  id: string
  workspaceId: string
  name: string
  type?: string
  description?: string
  icon?: string
  color?: string
  favorite?: number
  unbound?: number
  usageCount?: number
  sort?: number
  createdAt?: string
  updatedAt?: string
}

export interface Collection {
  id: string
  workspaceId: string
  sceneId: string
  name: string
  type?: string
  description?: string
  defaultToolId?: string
  tool?: string
  icon?: string
  color?: string
  openStrategy?: string
  favorite?: number
  recent?: number
  recentAt?: string
  unbound?: number
  pluginId?: string
  usageCount?: number
  sort?: number
  createdAt?: string
  updatedAt?: string
}

export interface CollectionItem {
  id: string
  workspaceId: string
  collectionId: string
  name: string
  type: string
  value: string
  workingDirectory?: string
  toolId?: string
  tool?: string
  args?: string
  icon?: string
  color?: string
  remark?: string
  pluginData?: string
  usageCount?: number
  sort?: number
  createdAt?: string
  updatedAt?: string
}

export interface OpenTool {
  id: string
  name: string
  type: string
  path?: string
  args?: string
  isDefault?: number
}

export interface HotkeyConfig {
  modifiers: number
  vk: number
  label: string
}

// scene.type 实际使用中文值，见 schema.go scenes.type DEFAULT '通用'
// collection.type 实际使用中文值，见 schema.go collections.type DEFAULT '目录集合'
// collectionItem.type 实际使用中文值，见 schema.go items.type DEFAULT '目录'
// TYPE_TOOL_MAP 映射也使用这些中文键
export type SceneType = '通用' | '项目' | '办公' | '开发' | '学习' | '生活' | string
export type CollectionType = '目录集合' | '标签页' | '列表' | string

// Toast 消息注入类型
export interface ToastFunc {
  (text: string): void
}
export interface ToastAPI {
  error: ToastFunc
  success: ToastFunc
  confirm: (message: string) => Promise<boolean>
}

export interface Snapshot {
  id: string
  kind: string
  label: string
  note: string
  payload: string
  size: number
  created_at: string
}

// 文本片段
export interface Snippet {
  id: string
  keyword: string
  content: string
  category: string
  name: string
  parentId: string
  isFolder: boolean
  sort: number
  tags: string
  isNote: boolean
  format: string
  createdAt: string
}

// HTTP 客户端：保存的请求
export interface ApiRequest {
  id: string
  name: string
  projectId: string
  folderId: string
  method: string
  url: string
  headers: string
  body: string
  bodyType: string
  authType: string
  authToken: string
  authUser: string
  authPass: string
  sort: number
  createdAt: string
  updatedAt: string
}

// HTTP 客户端：项目（Postman Collection 式分组）
export interface HttpProject {
  id: string
  name: string
  headers: string // JSON map：项目共享请求头
  sort: number
  createdAt: string
  updatedAt: string
}

// HTTP 客户端：环境（项目下变量集合）
export interface HttpEnvironment {
  id: string
  projectId: string
  name: string
  variables: string // JSON 数组：[{ key, value, enabled }]
  sort: number
  createdAt: string
  updatedAt: string
}

export interface HttpEnvVar {
  key: string
  value: string
  enabled: boolean
}

// HTTP 客户端：目录（项目下多级嵌套，folderId 关联请求）
export interface HttpFolder {
  id: string
  projectId: string
  parentId: string
  name: string
  sort: number
  createdAt: string
  updatedAt: string
}

// HTTP 客户端：目录下的 Markdown 文档（轻量笔记）
export interface HttpDoc {
  id: string
  projectId: string
  folderId: string
  name: string
  content: string
  sort: number
  createdAt: string
  updatedAt: string
}

// HTTP 客户端：树拖拽类型
export type HttpDragItem = { kind: 'request' | 'folder' | 'doc'; id: string }
export type HttpDropTarget =
  | { kind: 'into-folder'; id: string }
  | { kind: 'after-folder'; id: string }
  | { kind: 'before-request'; id: string }
  | { kind: 'project-root'; projectId: string }

// HTTP 客户端：发送响应
export interface ApiResponse {
  status: number
  ok: boolean
  headers: Record<string, string>
  body: string
  durationMs: number
  size: number
  truncated: boolean
}

// 数据库连接
export interface DbConnection {
  id: string
  name: string
  dbType: string // mysql | redis | sqlite
  host: string
  port: number
  username: string
  password: string
  database: string
  filePath: string
  createdAt: string
}

// 数据库查询结果
export interface DbQueryResult {
  success: boolean
  columns: string[]
  rows: string[][]
  nulls: boolean[][] // 与 rows 同维度，标记单元格是否为 NULL
  rowCount: number
  affected: number
  message: string
  error: string
  durationMs: number
  editable?: boolean // 单表 SELECT 且含主键列时可内联编辑
  tableName?: string
  primaryKey?: string
  editReason?: string // 不可编辑时的原因，便于前端诊断
}

// 单行修改提交（主键定位 + 列值/置空）
export interface DbRowUpdateInput {
  tableName: string
  pkColumn: string
  pkValue: string
  sets: Record<string, string>
  nulls: string[]
}

// 库表浏览器树节点
export interface DbTreeNode {
  name: string
  kind: string // folder | table | view | column | key
  detail?: string // 字段类型 / Redis 类型
  children?: DbTreeNode[]
}

// 剪贴板条目
export interface ClipboardEntry {
  id: string
  contentType: string
  textContent: string
  imagePath: string
  imageHash: string
  sourceApp: string
  isPinned: number
  copyCount: number
  note: string
  createdAt: number
}

// 插件系统
export interface PluginCommand {
  id: string
  title: string
  titleI18n?: Record<string, string>  // 多语言标题: {locale: 标题}
  hotkey?: string
  keywords?: string[]        // 搜索别名，用于命令面板快速查找
  aliases?: string[]         // 中文别名，如 ["计算器", "jsq"]，扩展搜索覆盖
  prefix?: string            // Slash 命令前缀，如 "/translate"，输入 /tr 时只匹配该插件
  matchPattern?: string      // 命令面板正则匹配：命中时自动传入输入文本
  acceptsInput?: boolean     // 是否接收命令面板传入的参数（Ctrl+K 文本）
}

export interface PluginInfo {
  id: string
  name: string
  nameI18n?: Record<string, string>
  version: string
  description?: string
  descriptionI18n?: Record<string, string>
  author?: string
  category?: string
  status: string
  hasFrontend: boolean
  usageCount: number
  commands: PluginCommand[]
}

// 插件命令执行日志（5.2）
export interface PluginExecLog {
  id: string
  pluginId: string
  commandId: string
  executedAt: string
  executedTs: number
  success: boolean
  durationMs: number
  result: string
  error: string
  trigger: string // manual | hotkey | palette
}

// ---- 环境管理运行时类型（与后端 services/env/manager.go 的 RuntimeInfo/Install/ServiceStatus 对齐）----
export interface EnvInstall {
  version: string
  scope: string // "portable" 便携目录 | "system" 系统 PATH | "linked" 导入目录
  path: string
  active: boolean // 是否为当前激活（环境变量指向）版本
  inSystemPath: boolean // bin 目录是否真正出现在系统 PATH
  alias: string
  note: string
}

export interface EnvServiceStatus {
  running: boolean
  pid: number
  port: number
  version: string
}

export interface EnvRuntimeInfo {
  id: string
  name: string
  group: string // language / webserver / cache / tool / database
  platforms: string[]
  recommended: string[]
  installed: EnvInstall[]
  sources: { id: string; name: string }[]
  activeSource: string
  hasService: boolean // 是否支持服务启停/状态监听
}
