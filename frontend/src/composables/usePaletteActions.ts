// 命令面板的「二级动作菜单」（Ctrl+K 再按一次调出）。
//
// 职责：给定当前选中的一条结果，算出一组可执行动作（打开 / 复制 / 定位 / 分离窗口 /
// 勾掉待办 / 删除条目），并管理菜单的开关与游标。
//
// 从 CommandPalette.vue 抽出的原因：这段与 usePaletteExecutor 同属「结果 → 动作」，
// 但执行器管的是「回车执行主操作」，这里管的是「主操作之外的旁路动作」。
// 两者都需要面板状态，靠 deps 显式注入，避免互相持有。

import { computed, nextTick, ref } from 'vue'
import type { ComputedRef, Ref } from 'vue'
import {
  Power, Gauge, Play, FileText, ListChecks, FolderSearch, Copy, ExternalLink, Check, Save, Trash2,
} from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { getPluginLastResult } from '../utils/pluginLastResult'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { DeleteItem } from '../../bindings/quickdock/services/item/itemservice'
import { SaveUrlAsItem } from '../../bindings/quickdock/services/appservice'
import { SetTodoStatus } from '../../bindings/quickdock/services/todo/todoservice'
import type { CollectionItem, ToastAPI } from '../types'
import type { RecentEntry, SearchResult } from './useCommandSearch'

export interface PaletteAction {
  id: string
  label: string
  icon: any
  danger?: boolean
  run: () => void | Promise<void>
}

export interface PaletteActionsDeps {
  /** 当前选中的结果（面板侧算好，本模块不碰列表状态） */
  actionTarget: ComputedRef<SearchResult | undefined>
  items: Ref<CollectionItem[]>
  recentCache: Ref<RecentEntry[]>
  selectedIndex: Ref<number>
  t: (k: string) => string
  toast?: ToastAPI
  /** 回车主操作（由 usePaletteExecutor 提供，避免这里重复实现路由） */
  executeSelected: () => Promise<void>
  closePalette: () => Promise<void> | void
  writeClipboard: (text: string) => Promise<boolean>
  detachPluginFor: (r: SearchResult) => Promise<void> | void
  recordUsage: (key: string, type: string, label: string, description: string) => void
  focusInput: () => void
}

// 结果的"主值"：文件/应用路径、链接、笔记内容等，用于复制 / 资源管理器定位
export function targetValue(r: SearchResult): string {
  switch (r.type) {
    case 'item': case 'quicklink': case 'quicklink-inline': return r.item?.value || ''
    case 'app': return r.appPath || ''
    case 'url': case 'clipboard-action': return r.url || ''
    case 'note': return r.note?.content || ''
    case 'todo': return r.todo?.title || ''
    case 'calculator': return r.calcResult || ''
    case 'plugin': return r.label || ''
    case 'file': return r.filePath || ''
    default: return ''
  }
}

// 是否为可在资源管理器中定位的本地路径（排除 http/自定义协议与纯命令行）
export function isLocalPath(v: string): boolean {
  if (!v) return false
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) return false
  return /^[a-zA-Z]:[\\/]/.test(v) || v.startsWith('\\\\')
}

export function usePaletteActions(deps: PaletteActionsDeps) {
  const { t, toast, items, recentCache, selectedIndex } = deps

  const actionMenuOpen = ref(false)
  const actionMenuIndex = ref(0)

  async function copyAndClose(text: string) {
    // 失败时 writeClipboard 已经弹过错误提示，这里不能再报「已复制」。
    if (await deps.writeClipboard(text)) toast?.success?.(t('copied'))
    void deps.closePalette()
  }

  async function revealAndClose(path: string) {
    try { unwrap(await RevealInExplorer(path)) }
    catch (e) { toast?.error?.(getErrorMessage(e)); nextTick(deps.focusInput); return }
    void deps.closePalette()
  }

  async function saveUrlAndClose(url: string) {
    try {
      const item = unwrap<CollectionItem>(await SaveUrlAsItem(url))
      deps.recordUsage('item:' + (item?.id || ''), 'item', url, url)
      toast?.success?.(t('savedAsItem'))
    } catch (e) { toast?.error?.(getErrorMessage(e)) }
    void deps.closePalette()
  }

  // 删除后不关闭面板：保持在结果列表里继续操作，仅同步本地缓存避免"幽灵条目"
  async function deleteItemAndRefocus(id: string) {
    const ok = await toast?.confirm?.(t('confirmDeleteItem'))
    if (!ok) { nextTick(deps.focusInput); return }
    try {
      unwrap(await DeleteItem(id))
      const idx = items.value.findIndex(i => i.id === id)
      if (idx >= 0) items.value.splice(idx, 1)
      recentCache.value = recentCache.value.filter(e => e.key !== 'item:' + id)
      selectedIndex.value = 0
      toast?.success?.(t('actItemDeleted'))
    } catch (e) { toast?.error?.(getErrorMessage(e)) }
    nextTick(deps.focusInput)
  }

  const contextActions = computed<PaletteAction[]>(() => {
    const r = deps.actionTarget.value
    if (!r) return []
    const acts: PaletteAction[] = []
    const value = targetValue(r)

    // 主操作（等价于直接回车）
    // 文件结果（Everything 检索）的主操作是「定位所在文件夹」而非打开文件本身
    const primaryLabel =
      r.type === 'env' ? (r.envAction === 'stop' ? t('envStop') : r.envAction === 'mgmt' ? t('envEnableMgmt') : t('envStart'))
        : r.type === 'app' ? t('actLaunch')
          : r.type === 'note' ? t('actOpenNote')
            : r.type === 'todo' ? t('actOpenTodo')
              // 快速导航：主操作是切页，不是"打开"（它不是一个可打开的对象）
              : r.type === 'nav' ? t('cmdNavHint')
                : r.type === 'file' ? t('actReveal')
                : (r.type === 'system' || r.type === 'plugin') ? t('actRun')
                  : r.type === 'calculator' ? t('actCopyResult')
                    : t('actOpen')
    const primaryIcon = r.type === 'env' ? (r.envAction === 'stop' ? Power : r.envAction === 'mgmt' ? Gauge : Play)
      : r.type === 'note' ? FileText : r.type === 'todo' ? ListChecks : r.type === 'file' ? FolderSearch : r.type === 'calculator' ? Copy : Play
    acts.push({ id: 'primary', label: primaryLabel, icon: primaryIcon, run: () => deps.executeSelected() })

    if (r.label) acts.push({ id: 'copy-name', label: t('actCopyName'), icon: Copy, run: () => copyAndClose(r.label) })

    if (value && value !== r.label) {
      const label = isLocalPath(value) ? t('actCopyPath') : /^https?:/i.test(value) ? t('actCopyLink') : t('actCopyValue')
      acts.push({ id: 'copy-value', label, icon: Copy, run: () => copyAndClose(value) })
    }

    // 插件专属动作：原生 tab 里的动作菜单太薄——补「分离为独立窗口」与「复制上次结果」
    if (r.type === 'plugin' && r.pluginId) {
      if (r.pluginHasFrontend) {
        acts.push({ id: 'plugin-detach-window', label: t('actPluginDetach'), icon: ExternalLink, run: () => deps.detachPluginFor(r) })
      }
      if (r.pluginCommandId) {
        const last = getPluginLastResult(r.pluginId, r.pluginCommandId)
        if (last?.result) {
          acts.push({ id: 'plugin-copy-last', label: t('actCopyLastResult'), icon: Copy, run: () => copyAndClose(last.result) })
        }
      }
    }

    // 待办专属：不用在面板里来回切到待办页就能勾掉一条（搜索的目的往往就是"找出来处理掉"）
    if (r.type === 'todo' && r.todo?.id && r.todo.status !== 'done') {
      const id = r.todo.id
      acts.push({
        id: 'complete-todo', label: t('actCompleteTodo'), icon: Check,
        run: async () => {
          try { unwrap(await SetTodoStatus(id, 'done')); toast?.success?.(t('actCompleteTodo')) }
          catch (e) { toast?.error?.(getErrorMessage(e)); nextTick(deps.focusInput); return }
          void deps.closePalette()
        },
      })
    }

    // 文件结果的主操作已是「定位所在文件夹」，不重复追加 reveal 项
    if (isLocalPath(value) && r.type !== 'file') acts.push({ id: 'reveal', label: t('actReveal'), icon: FolderSearch, run: () => revealAndClose(value) })

    if ((r.type === 'url' || r.type === 'clipboard-action') && r.url) {
      acts.push({ id: 'save-url', label: t('cmdSaveAsItem'), icon: Save, run: () => saveUrlAndClose(r.url!) })
    }

    if ((r.type === 'item' || r.type === 'quicklink' || r.type === 'quicklink-inline') && r.item?.id) {
      const id = r.item.id
      acts.push({ id: 'delete-item', label: t('actDeleteItem'), icon: Trash2, danger: true, run: () => deleteItemAndRefocus(id) })
    }
    return acts
  })

  function openActionMenu() {
    if (!deps.actionTarget.value || contextActions.value.length === 0) return
    actionMenuIndex.value = 0
    actionMenuOpen.value = true
  }

  function closeActionMenu(refocus = true) {
    if (!actionMenuOpen.value) return
    actionMenuOpen.value = false
    if (refocus) nextTick(deps.focusInput)
  }

  async function runAction(a: PaletteAction) {
    actionMenuOpen.value = false
    await a.run()
  }

  return { actionMenuOpen, actionMenuIndex, contextActions, openActionMenu, closeActionMenu, runAction }
}
