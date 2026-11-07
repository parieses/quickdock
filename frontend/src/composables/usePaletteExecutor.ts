// usePaletteExecutor.ts — 命令面板「选中项 = 执行什么」的路由
//
// 从 CommandPalette.vue 里搬出来的：原来 executeSelected 一个函数 137 行、14 个分支，
// 混在搜索/键盘/插件/预览/动作菜单之间，任何改动都要在 1700 行里翻。
// 搬出来的边界很明确——**只管「拿到一条结果，把它变成动作」**，不碰列表状态、不碰键盘。
//
// 依赖全部显式注入（PaletteExecutorDeps），因为这里需要的都是面板自己拥有的状态：
// 扁平结果、选中集、最近使用缓存、内联插件入口。这些不搬走，只把「怎么用」搬走。
//
// 两个用词容易混，注释里统一：
//   quicklink        —— 资源里带 {query} 的条目，回车先进内联输入态再打开
//   inlinePlugin     —— 前端插件的内联页（同一浮窗内切换），不是独立窗口

import { inject, nextTick, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Browser, Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { commandTitle } from '../utils/localize'
import { getPluginLastResult, savePluginLastResult, pluginCmdKey } from '../utils/pluginLastResult'
import { resolvePluginInput } from './palettePluginInput'
import {
  LaunchInstalledApp,
  NavigateMainWindow,
  OpenNoteFromPalette,
  OpenTodoFromPalette,
  SaveUrlAsItem,
} from '../../bindings/quickdock/services/appservice'
import { OpenItem } from '../../bindings/quickdock/services/collection/collectionservice'
import { RevealInExplorer } from '../../bindings/quickdock/services/system/systemservice'
import { ExecutePluginCommand, EnablePlugin } from '../../bindings/quickdock/services/plugin/pluginservice'
import type { CollectionItem, ToastAPI } from '../types'
import type { SearchResult, RecentEntry } from './useCommandSearch'
import type { PluginCmdIndex } from './usePluginIndex'

export interface PaletteExecutorDeps {
  /** 当前展示的扁平结果（分组拍平后），选中下标指向它 */
  displayFlat: Ref<SearchResult[]>
  selectedIndex: Ref<number>
  /**
   * 把 80ms 防抖里挂着的输入立刻落成结果（见 useCommandSearch.flushSearch）。
   * 执行前必须调一次：否则「打完字立刻回车」执行的是上一轮列表的同一下标。
   */
  flushSearch: () => void
  /** 多选批量执行用的下标集合 */
  selectedSet: Ref<Set<number>>
  /** 多选锚点（执行完重置） */
  lastAnchor: Ref<number>
  pluginCmdIndex: Ref<PluginCmdIndex[]>
  recentCache: Ref<RecentEntry[]>
  recordUsage: (key: string, type?: string, label?: string, desc?: string, input?: string) => void
  /** 重新拉取「最近使用」，插件执行完立刻反映 */
  refreshRecent: () => Promise<void>
  closePalette: () => void | Promise<void>
  markPluginSeen: (pluginId: string) => void
  loadPluginIndex: () => void | Promise<void>
  executeEnvAction: (r: SearchResult) => Promise<void>
  writeClipboard: (text: string) => Promise<boolean>
  /** 进入 quicklink 内联输入态（由面板实现：置 inlineQuicklink、清 query、聚焦输入框） */
  beginQuicklink: (item: CollectionItem) => void | Promise<void>
  /** 打开内联插件页（由面板实现：置 inlinePluginId/Name/Init） */
  openInlinePlugin: (pluginId: string, commandId: string, text: string) => void
  /** 进入「新建笔记 / 待办」内联标题输入态（由面板实现，提交才落库） */
  beginCreate: (kind: 'note' | 'todo') => void
  /**
   * 面板是否处于「最近使用」列表（输入框为空）。
   * 这条决定了「没带输入时能不能回退上次输入」—— 点历史记录就是要重跑，
   * 而敲 keyword 命中不是（见 palettePluginInput 的注释）。
   */
  isRecentMode: () => boolean
}

export function usePaletteExecutor(deps: PaletteExecutorDeps) {
  const { t, locale } = useI18n()
  const toast = inject<ToastAPI>('toast')

  /** 上一次执行该命令时的输入：持久化 store 优先（跨应用重启），其次 usage 表 */
  function lastInputOf(pluginId?: string, commandId?: string): string | undefined {
    if (!pluginId || !commandId) return undefined
    const last = getPluginLastResult(pluginId, commandId)
    if (last?.input) return last.input
    const re = deps.recentCache.value.find(e => e.key === pluginCmdKey(pluginId, commandId) && !!e.input)
    return re?.input
  }

  function isOpenable(r: SearchResult): boolean {
    return r.type === 'item' || r.type === 'url' || r.type === 'app'
      || r.type === 'quicklink' || r.type === 'quicklink-inline' || r.type === 'file'
  }

  // Everything 命中的文件：回车不打开文件本身，而是定位到所在文件夹
  // （Windows = explorer /select，文件夹弹出且文件处于选中态）——搜文件的目的
  // 通常是接着操作它的目录，直接打开文件反而常跳出重型应用。
  // 文件结果不写使用历史：Everything 检索是即时查询，记进去只会把「最近使用」冲淡。
  async function openFileItem(r: SearchResult): Promise<boolean> {
    if (!r.filePath) return false
    try {
      unwrap(await RevealInExplorer(r.filePath))
      return true
    } catch (e) {
      console.error('[CmdPalette] RevealInExplorer:', e)
      toast?.error?.(getErrorMessage(e))
      return false
    }
  }

  /** 只打开（不关面板）：多选批量走这条，失败不中断其余项 */
  async function openResultOnly(r: SearchResult): Promise<boolean> {
    if (r.type === 'quicklink' && r.item) {
      await deps.beginQuicklink(r.item)
      return true
    }
    if (r.type === 'quicklink-inline' && r.item) {
      const item = { ...r.item }
      let value = item.value || ''
      if (r.inlineQuery) value = value.replace(/\{query\}/g, r.inlineQuery)
      item.value = value
      deps.recordUsage('item:' + item.id, 'item', item.name, item.value || '')
      try {
        await OpenItem(item as any)
        return true
      } catch (e) {
        console.error('[CmdPalette] OpenItem:', e)
        toast?.error?.(getErrorMessage(e))
        return false
      }
    }
    if (r.type === 'item' && r.item) {
      deps.recordUsage('item:' + r.item.id, 'item', r.label, r.desc)
      try {
        await OpenItem(r.item as any)
        return true
      } catch (e) {
        console.error('[CmdPalette] OpenItem:', e)
        toast?.error?.(getErrorMessage(e))
        return false
      }
    }
    if (r.type === 'url' && r.url) {
      try {
        await Browser.OpenURL(r.url)
        return true
      } catch (e) {
        console.error('[CmdPalette] OpenURL:', e)
        toast?.error?.(getErrorMessage(e))
        return false
      }
    }
    if (r.type === 'app' && r.appPath) {
      deps.recordUsage('app:' + r.label, 'app', r.label, r.desc)
      try {
        await LaunchInstalledApp(r.appPath)
        return true
      } catch (e) {
        console.error('[CmdPalette] LaunchInstalledApp:', e)
        toast?.error?.(getErrorMessage(e))
        return false
      }
    }
    if (r.type === 'file' && r.filePath) {
      return await openFileItem(r)
    }
    return false
  }

  async function executeBatch() {
    const indices = [...deps.selectedSet.value].sort((a, b) => a - b)
    let n = 0
    for (const idx of indices) {
      const r = deps.displayFlat.value[idx]
      if (r && isOpenable(r)) {
        const ok = await openResultOnly(r)
        if (ok) n++
      }
    }
    deps.selectedSet.value = new Set()
    deps.lastAnchor.value = -1
    if (n > 0) toast?.success?.(t('openedCount', { n }))
  }

  async function executeSelected() {
    // 先把防抖里挂着的输入落成结果，并等一帧让模板跟上：
    // 命中 `Enter` 的那一刻，用户看到的是新列表，而 displayFlat 可能还是上一轮的。
    deps.flushSearch()
    await nextTick()
    if (deps.selectedSet.value.size > 0) {
      await executeBatch()
      return
    }
    const result = deps.displayFlat.value[deps.selectedIndex.value]
    if (!result) return
    if (result.type === 'system' && result.cmd) {
      deps.recordUsage('system:' + result.cmd.id, 'system', result.label, result.desc)
      await result.cmd.action()
    } else if (result.type === 'quicklink-inline' && result.item) {
      const item = { ...result.item }
      let value = item.value || ''
      if (result.inlineQuery) value = value.replace(/\{query\}/g, result.inlineQuery)
      item.value = value
      deps.recordUsage('item:' + item.id, 'item', item.name, item.value || '')
      try {
        await OpenItem(item as any)
      } catch (e) {
        console.error('[CmdPalette] OpenItem:', e)
      }
      void deps.closePalette()
    } else if (result.type === 'quicklink' && result.item) {
      await deps.beginQuicklink(result.item)
    } else if (result.type === 'item' && result.item) {
      deps.recordUsage('item:' + result.item.id, 'item', result.label, result.desc)
      try {
        await OpenItem(result.item as any)
      } catch (e) {
        console.error('[CmdPalette] OpenItem:', e)
      }
      void deps.closePalette()
    } else if (result.type === 'url' && result.url) {
      try {
        await Browser.OpenURL(result.url)
      } catch (e) {
        console.error('[CmdPalette] OpenURL:', e)
      }
      void deps.closePalette()
    } else if (result.type === 'calculator' && result.calcResult) {
      await deps.writeClipboard(result.calcResult)
      void deps.closePalette()
    } else if (result.type === 'note' && result.note) {
      deps.recordUsage('note:' + result.note.id, 'note', result.label, result.desc)
      // 面板是独立浮窗，与主窗口 JS 状态不互通；交后端隐藏本窗 + 唤起主窗口
      // 并广播笔记 id（App.vue 监听后写入 pendingOpenNoteId 并切到笔记页）。
      // 面板内直接 navigateTo 会把完整主界面渲染进小浮窗，导致样式错乱。
      try {
        await OpenNoteFromPalette(result.note.id)
      } catch (e) {
        console.error('[CmdPalette] OpenNoteFromPalette:', e)
      }
      void deps.closePalette()
    } else if (result.type === 'todo' && result.todo) {
      deps.recordUsage('todo:' + result.todo.id, 'todo', result.label, result.desc)
      // 与笔记同一条链路：后端隐藏面板 + 唤起主窗口 + 广播待办 id（见 OpenNoteFromPalette 注释）
      try {
        await OpenTodoFromPalette(result.todo.id)
      } catch (e) {
        console.error('[CmdPalette] OpenTodoFromPalette:', e)
      }
      void deps.closePalette()
    } else if (result.type === 'app' && result.appPath) {
      deps.recordUsage('app:' + result.label, 'app', result.label, result.desc)
      try {
        await LaunchInstalledApp(result.appPath)
      } catch (e) {
        console.error('[CmdPalette] LaunchInstalledApp:', e)
      }
      void deps.closePalette()
    } else if (result.type === 'file' && result.filePath) {
      const ok = await openFileItem(result)
      if (ok) void deps.closePalette()
    } else if (result.type === 'env') {
      await deps.executeEnvAction(result)
    } else if (result.type === 'nav' && result.navPage) {
      // 与笔记/待办同一条链路：面板是独立浮窗，直接 navigateTo 会把整个主界面
      // 渲染进小窗。交后端隐藏面板 + 唤起主窗口 + 广播页面 id（App.vue 监听后 setPage）。
      try {
        await NavigateMainWindow(result.navPage)
      } catch (e) {
        console.error('[CmdPalette] NavigateMainWindow:', e)
      }
      void deps.closePalette()
    } else if ((result.type === 'scene' || result.type === 'collection') && result.navTarget) {
      // 场景 / 集合：切到 workspace 页并停在该条目上。复用两段现成链路，不新增后端方法：
      //  - NavigateMainWindow('workspace')：隐藏面板 + 唤起/聚焦主窗口 + 切页。页面 id
      //    已在 services/palette.go 的 navigablePages 白名单里，后端无需同步。
      //  - 定位目标另发前端事件：面板是独立浮窗，JS 状态与主窗口不互通，不能自己写
      //    bridge.pendingTarget；由主窗口 App.vue 监听后用 navigateTo(page, target) 落位。
      // 先发定位、再交后端切页：定位只是往主窗口写一个 pendingTarget ref，与「切页」
      // 无严格先后（主窗口 watch(pendingTarget, { immediate: true }) 前后拿到都能定位），
      // 却能赶在 HidePaletteWindow 把浮窗隐藏之前发出 —— 隐藏后 WebView 可能被节流，
      // 那时再 Emit 有丢事件的风险。
      const page = result.navPage || 'workspace'
      try {
        Events.Emit('palette:locate', {
          page,
          target: result.type + ':' + result.navTarget,
          kind: result.type,
          id: result.navTarget,
          workspaceId: result.scene?.workspaceId || result.collection?.workspaceId || '',
          sceneId: result.collection?.sceneId || '',
        })
      } catch (e) {
        console.error('[CmdPalette] palette:locate:', e)
      }
      try {
        await NavigateMainWindow(page)
      } catch (e) {
        console.error('[CmdPalette] NavigateMainWindow:', e)
      }
      void deps.closePalette()
    } else if (result.type === 'create' && result.createKind) {
      // 新建动作不关面板：进内联标题输入态，提交后才落库并跳到新条目
      deps.beginCreate(result.createKind)
    } else if (result.type === 'plugin' && result.pluginId && result.pluginCommandId) {
      deps.markPluginSeen(result.pluginId)
      // 插件未运行时先自动拉起（EnablePlugin = Reload 加载 + DB 置启用）：
      // 面板不静默丢弃这类命令，而是「执行前自动启动」；失败则明示原因停在面板
      if (result.pluginNotRunning) {
        try {
          await EnablePlugin(result.pluginId)
        } catch (e) {
          toast?.error?.(t('pluginStartFailed') + ': ' + getErrorMessage(e))
          return
        }
        await deps.loadPluginIndex()
      }
      // 「带什么文本、走哪条通道」全部交给纯函数判定（可单测，见 palettePluginInput.test.ts）：
      //   - acceptsInput 命令：inlineInput / label 内联输入 → 必要时才回退上次输入；
      //   - 前端插件的内容通道：没声明 acceptsInput 也能从 plugin:init.text 取到原文；
      //   - 后端执行路径仍严格要求 acceptsInput —— 多塞参数会改变命令语义。
      const title = (() => {
        const idx = deps.pluginCmdIndex.value.find(
          (c) => c.plugin.id === result.pluginId && c.cmd.id === result.pluginCommandId,
        )
        return idx ? commandTitle(idx.cmd, locale) : ''
      })()
      const labelInput = result.label && title && result.label.startsWith(title + ': ')
        ? result.label.slice(title.length + 2)
        : undefined
      const plan = resolvePluginInput({
        acceptsInput: result.acceptsInput,
        pluginHasFrontend: result.pluginHasFrontend,
        matchType: result.matchType,
        inlineInput: result.inlineInput,
        labelInput,
        hitText: result.hitText,
        lastInput: lastInputOf(result.pluginId, result.pluginCommandId),
        isRecentMode: deps.isRecentMode(),
      })
      const inputText = plan.text
      if (plan.action === 'need-input') {
        toast?.error?.(t('pluginNeedInput'))
        return
      }
      // acceptsInput 但没拿到参数、只能先开界面让用户补：这算「打开了一个界面」，
      // 不算一次带参执行 —— 不写使用历史（原实现同样跳过，免得空输入把历史冲淡）。
      const opensForInput = plan.action === 'open-inline' && !!result.acceptsInput && !inputText
      // 只在前端分支记录使用次数：无前端分支紧接着调 ExecutePluginCommand，
      // 后端那边已经写过一次 RecordUsageEx（services/plugin/plugin_exec.go），
      // 两边都写等于 count +2（图标/排序用的频次直接翻倍）。
      if (plan.action === 'open-inline' && !opensForInput) {
        deps.recordUsage(
          'plugin:' + result.pluginId + '.' + result.pluginCommandId,
          'plugin', result.label, result.desc, inputText || '',
        )
      }
      // 即时刷新「最近使用」：运行完立刻能看到，不必等下次重开面板
      void deps.refreshRecent().catch(() => {})
      try {
        if (plan.action === 'open-inline') {
          // 前端插件：交由统一 PluginFrame 宿主打开 UI；init 通过同窗口 getter 直接传入
          // （不走全局 pending init 单例）
          deps.openInlinePlugin(result.pluginId, result.pluginCommandId || '', inputText || '')
        } else {
          // 无前端插件（goja/native 无 UI）：后端执行并返回结果
          const raw = await ExecutePluginCommand(
            result.pluginId, result.pluginCommandId,
            inputText ? { text: inputText } : null as any,
          )
          const pluginResult = unwrap<string | any>(raw)
          if (pluginResult && pluginResult.error) {
            toast?.error?.(pluginResult.error)
          } else if (pluginResult) {
            const displayText = typeof pluginResult === 'object'
              ? (pluginResult.translated || pluginResult.text || pluginResult.display || JSON.stringify(pluginResult))
              : String(pluginResult)
            const copyText = typeof pluginResult === 'object'
              ? (pluginResult.translated || pluginResult.text || pluginResult.copy || displayText)
              : displayText
            await deps.writeClipboard(copyText)
            // 持久化「每命令最近一次结果」：重开面板/重启后 preview 仍可展示、
            // 动作菜单可复制、acceptsInput 命令可一键重跑
            savePluginLastResult(result.pluginId, result.pluginCommandId, {
              result: displayText, pluginName: result.desc || result.label,
              pluginId: result.pluginId, pluginCommandId: result.pluginCommandId,
              pluginHasFrontend: result.pluginHasFrontend, input: inputText,
              acceptsInput: result.acceptsInput,
            })
            toast?.success?.(t('pluginResultCopied'))
          }
        }
      } catch (e) {
        toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
      }
    } else if (result.type === 'clipboard-action' && result.clipAction) {
      if (result.clipAction === 'open-url' && result.url) {
        try {
          await Browser.OpenURL(result.url)
        } catch (e) {
          console.error('[CmdPalette] OpenURL:', e)
        }
        void deps.closePalette()
      } else if (result.clipAction === 'save-url' && result.url) {
        try {
          const item = unwrap<CollectionItem>(await SaveUrlAsItem(result.url))
          deps.recordUsage('item:' + (item?.id || ''), 'item', result.url, result.url)
          toast?.success?.(t('savedAsItem'))
        } catch (e) {
          toast?.error?.(getErrorMessage(e))
        }
        void deps.closePalette()
      } else if (result.clipAction === 'encode-url' && result.url) {
        // 纯本地编码，不依赖任何插件：与 text-encoder 内置的
        // urlEncode 实现完全一致（encodeURIComponent 语义），核心功能零插件耦合
        let encoded = ''
        try {
          encoded = encodeURIComponent(result.url)
        } catch {
          encoded = result.url
        }
        if (await deps.writeClipboard(encoded)) toast?.success?.(t('pluginResultCopied'))
        void deps.closePalette()
      }
    }
  }

  return { executeSelected, executeBatch, openResultOnly, openFileItem, isOpenable }
}
