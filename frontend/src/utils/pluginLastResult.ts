// 插件命令"最近一次执行"的持久化存储。
//
// 背景：命令面板的 pluginResultCache 只活在单次面板会话里（清输入/重开即丢），
// 且以截断 150 字符的结果文本冒充列表条目，用户分不清按钮与结果。
// 本模块把每 (pluginId, commandId) 的最近结果与输入落盘（localStorage 兜底，
// 内存态兜 localStorage 不可用），支持：
//   - 重开面板/重启应用后 preview 面板仍能展示上次结果
//   - 一键重跑：acceptsInput 命令直接沿用上次输入
//   - 动作菜单「复制上次结果」「分离为独立窗口（带上次输入）」

export interface PluginLastResult {
  result: string      // 执行结果正文（截断 2000 字符，防撑爆 localStorage）
  pluginName: string
  pluginId: string
  pluginCommandId: string
  pluginHasFrontend?: boolean
  input?: string
  acceptsInput?: boolean
  ts: number          // Unix ms
}

const STORAGE_KEY = 'qd.plugin-last-result.v1'
const MAX_RESULT_LEN = 2000
const MAX_ENTRIES = 60

let memory: Record<string, PluginLastResult> = load()

function load(): Record<string, PluginLastResult> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const j = JSON.parse(raw)
      if (j && typeof j === 'object') return j
    }
  } catch { /* localStorage 不可用（隐私模式等），退回纯内存 */ }
  return {}
}

function persist() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(memory))
  } catch { /* 空间满/被禁用时忽略，本轮会话内存态仍可用 */ }
}

export function pluginCmdKey(pluginId: string, commandId: string): string {
  return pluginId + '.' + commandId
}

export function getPluginLastResult(pluginId: string, commandId: string): PluginLastResult | null {
  return memory[pluginCmdKey(pluginId, commandId)] || null
}

export function savePluginLastResult(pluginId: string, commandId: string, data: Omit<PluginLastResult, 'ts'>) {
  const entry: PluginLastResult = { ...data, ts: Date.now() }
  if (entry.result && entry.result.length > MAX_RESULT_LEN) {
    entry.result = entry.result.slice(0, MAX_RESULT_LEN) + '…'
  }
  memory[pluginCmdKey(pluginId, commandId)] = entry
  const keys = Object.keys(memory)
  if (keys.length > MAX_ENTRIES) {
    keys
      .sort((a, b) => memory[a].ts - memory[b].ts)
      .slice(0, keys.length - MAX_ENTRIES)
      .forEach((k) => delete memory[k])
  }
  persist()
}