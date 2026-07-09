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
  createdAt: string
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
  createdAt: number
}
