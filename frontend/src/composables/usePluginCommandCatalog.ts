/**
 * 插件命令目录：把「装了插件却不知道它能干什么」变成看得见。
 *
 * 数据来源用已有的 PluginCatalog('full')——它在前端 bindings 里一直存在，但此前
 * 零调用；返回的 PluginInfo[] 里带 hotkey / params / matchPattern / prefix。
 * 再按需调 PluginCommandParams(pluginId, cmdId)（同样零调用，原本只服务 MCP
 * plugin_execute）核对参数声明。管理页借此把「怎么触发、要填什么」一起列出来。
 *
 * 为什么单独一层而不直接读卡片上的 p.commands：命令目录是可选增强。任一后端调用
 * 失败时这里一律返回空目录，页面隐藏增强区块，绝不因为「展示」把插件列表拖垮。
 */
import { ref } from 'vue'
import { PluginCatalog, PluginCommandParams } from '../../bindings/quickdock/services/plugin/pluginservice'
import { unwrap } from '../utils/api'
import type { PluginInfo, PluginCommand } from '../types'

/** 与执行日志 plugin_exec_logs.trigger 同义：manual / hotkey / palette。 */
export type CommandTrigger = 'hotkey' | 'palette' | 'manual'

export interface CommandView {
  id: string
  title: string
  hotkey?: string
  trigger: CommandTrigger
  /** prefix / matchPattern 原文，用来说明「这条命令怎么被唤出来」。 */
  triggerHint?: string
  description?: string
  params?: Record<string, unknown>
  acceptsInput?: boolean
}

// 模块级共享：管理页挂载时拉一次，卡片与命令清单弹窗共用同一份，切页不重复拉。
const catalog = ref<Record<string, CommandView[]>>({})
const catalogLoaded = ref(false)
const catalogFailed = ref(false)

/** 从命令声明派生触发方式：不依赖任何后端调用，取不到目录也能在卡片上显示。 */
export function commandTrigger(c: PluginCommand): CommandTrigger {
  if (c.hotkey) return 'hotkey'
  if (c.prefix || c.matchPattern) return 'palette'
  return 'manual'
}

function toView(c: PluginCommand): CommandView {
  return {
    id: c.id,
    title: c.title,
    hotkey: c.hotkey,
    trigger: commandTrigger(c),
    triggerHint: c.prefix || c.matchPattern || '',
    description: c.description,
    params: c.params,
    acceptsInput: c.acceptsInput,
  }
}

/**
 * 拉取命令目录。后端未就绪 / 插件尚未加载 / 绑定缺失都走静默降级：
 * 目录为空 + catalogFailed=true，模板据此隐藏命令清单入口，不抛错、不弹 toast。
 */
export async function loadCommandCatalog(): Promise<void> {
  if (catalogLoaded.value) return
  try {
    const list = unwrap<PluginInfo[]>(await PluginCatalog('full'))
    if (!Array.isArray(list)) {
      catalog.value = {}
      catalogFailed.value = true
      return
    }
    const next: Record<string, CommandView[]> = {}
    for (const p of list) {
      next[p.id] = (p.commands || []).map(toView)
    }
    catalog.value = next
    catalogLoaded.value = true
    catalogFailed.value = false
  } catch {
    catalog.value = {}
    catalogFailed.value = true
  }
}

/**
 * 按需取某条命令的参数声明（把 PluginCommandParams 这个零调用方法接上）。
 * 只在用户展开命令清单时调用，避免加载期对每条命令都发一次 IPC。
 * 失败返回 null，弹窗隐藏参数行即可——参数声明是增强，不该挡住命令本身。
 */
export async function loadCommandParams(pluginId: string, cmdId: string): Promise<Record<string, unknown> | null> {
  try {
    const [params, ok] = await PluginCommandParams(pluginId, cmdId)
    return ok && params ? params : null
  } catch {
    return null
  }
}

export function usePluginCommandCatalog() {
  return { catalog, catalogLoaded, catalogFailed, loadCommandCatalog, loadCommandParams, commandTrigger }
}
