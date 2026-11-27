// 通用「另存为 / 加入知识库」：把任意页面看到的内容沉淀成笔记、待办或知识库文档。
//
// 为什么做成模块级单例而不是每页各写一个菜单：
// 剪贴板页早就有 saveAsTodo/saveAsNote，AI 页也有一份 saveAsNote——同一件事
// 两套实现，新增「加入知识库」就得改两处，且各处的标题截断/标签/副作用口径
// 迟早漂移。这里把写入链路（标题提取、trim、format、刷新徽标、互链失效）收在
// 一处，宿主页面只负责「在什么时候、以什么坐标弹出」。
//
// 菜单组件 QdSaveAsMenu.vue 是全局唯一实例，读这里的 state；任何页面调 open()
// 即可在鼠标位置弹出。面板独立窗口（命令面板/SSH 窗口）不共享 Pinia，但模块级
// state 在各自 WebView 内独立，互不干扰——这是期望行为。

import { computed, shallowRef, type CSSProperties, type ComputedRef, type Ref } from 'vue'
import { useFloatMenu } from './useFloatMenu'
import {
  AddKBFiles,
  AddKBText,
} from '../../bindings/quickdock/services/kb/kbservice'
import {
  CreateNoteDoc,
} from '../../bindings/quickdock/services/note/noteservice'
import {
  CreateTodo,
} from '../../bindings/quickdock/services/todo/todoservice'
import { navBadges } from './useNavBadges'
import { invalidateLinkTargets } from '../constants/linkEvents'
import { useToast } from './useToast'
import { i18n } from '../i18n'

import { reportError } from '../utils/reportError'
/** 模块级文案：composable 不在 setup 上下文里，用导出的 i18n 实例取 t。 */
const t = (key: string) => i18n.global.t(key)

export type SaveTarget = 'note' | 'todo' | 'kb'
export type SaveSource =
  | 'download' | 'filesearch' | 'clipboard' | 'ssh' | 'ai' | 'monitor' | 'note'

export interface SaveAsPayload {
  /** 待沉淀正文（函数内部会 trim） */
  text: string
  source: SaveSource
  sourceMeta?: {
    /** 磁盘路径：有则加入知识库走文件管线（可溯源），无则用文本管线 */
    paths?: string[]
    convId?: string
    messageId?: string
    sessionId?: string
    monitorId?: string
    /** 默认标题 */
    label?: string
  }
  title?: string
  tags?: string[]
}

// 模块级状态：全局唯一菜单实例读它
const state = shallowRef<SaveAsPayload | null>(null)
const menu = useFloatMenu({ placement: 'right-start', offset: 4 })
const toast = useToast()

/** 首行做标题，避免长文本整段塞进标题栏。空文本回落到给调用方指定的默认标题。 */
function firstLineAsTitle(text: string, max = 40): string {
  const line = (text || '').split('\n').map((s) => s.trim()).find(Boolean) || ''
  return line.length > max ? line.slice(0, max) + '…' : line
}

function feedback(res: { code: number; msg: string } | null, fallbackMsg: string) {
  if (res && res.code === 0) toast.success(res.msg || fallbackMsg)
  else toast.error((res && res.msg) || fallbackMsg)
}

function open(payload: SaveAsPayload, at: { x: number; y: number } | DOMRect) {
  state.value = payload
  if (at instanceof DOMRect) menu.showRect(at)
  else menu.showAt(at.x, at.y)
}

function close() {
  state.value = null
  menu.hide()
}

/** 存为笔记。传入 tags 只用于拼进正文首行标签，CreateNoteDoc 本身无 tags 参数。 */
async function saveToNote(p: {
  content: string
  title?: string
  tags?: string[]
  format?: 'markdown' | 'text'
}): Promise<void> {
  const content = (p.content || '').trim()
  if (!content) return
  const title = p.title || firstLineAsTitle(content, 30) || t('saveAsDefaultNoteTitle')
  const tagsLine = p.tags && p.tags.length
    ? '\n\n' + p.tags.map((t) => '#' + t).join(' ')
    : ''
  try {
    const res = await CreateNoteDoc('', title, content + tagsLine, p.format || 'markdown')
    feedback(res, t('saveAsSavedNote'))
    invalidateLinkTargets()
  } catch (e) {
    reportError(e, 'useSaveAs.saveToNote')
  }
}

/** 存为待办。tags 序列化成 JSON 数组字符串，与 TodoPage 的口径一致。 */
async function saveToTodo(p: {
  content: string
  title?: string
  tags?: string[]
  priority?: string
}): Promise<void> {
  const content = (p.content || '').trim()
  if (!content) return
  const title = p.title || firstLineAsTitle(content) || t('saveAsDefaultTodoTitle')
  const tags = JSON.stringify(p.tags || [])
  try {
    const res = await CreateTodo(
      title,
      p.priority || 'medium',
      '',
      content,
      '', '', '', '',
      tags,
    )
    feedback(res, t('saveAsSavedTodo'))
    navBadges().refreshNow()
  } catch (e) {
    reportError(e, 'useSaveAs.saveToTodo')
  }
}

/**
 * 加入知识库。有磁盘路径走 AddKBFiles（可溯源），纯文本走 AddKBText（写成
 * dataDir/kb-clips 下的 .md 再索引）。libraryID 传空 = 默认库（后端 resolve）。
 */
async function addToKB(p: {
  paths?: string[]
  text?: string
  name?: string
  libraryID?: string
}): Promise<void> {
  try {
    if (p.paths && p.paths.length) {
      const res = await AddKBFiles(p.libraryID || '', p.paths)
      feedback(res, t('saveAsSavedKB'))
    } else {
      const content = (p.text || '').trim()
      if (!content) return
      const res = await AddKBText(p.libraryID || '', p.name || firstLineAsTitle(content, 40), content)
      feedback(res, t('saveAsSavedKB'))
    }
  } catch (e) {
    reportError(e, 'useSaveAs.addToKB')
  }
}

export function useSaveAs() {
  return {
    state,
    visible: menu.visible,
    floatingRef: menu.floatingRef,
    floatingStyles: menu.floatingStyles,
    open,
    close,
    saveToNote,
    saveToTodo,
    addToKB,
    firstLineAsTitle,
  }
}

// 供 QdSaveAsMenu 内部读取的类型化引用（避免重复 import composable 内部状态）
export type SaveAsState = {
  state: Ref<SaveAsPayload | null>
  visible: ComputedRef<boolean>
  floatingRef: Ref<HTMLElement | null>
  floatingStyles: ComputedRef<CSSProperties>
}
