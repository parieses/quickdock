import { inject, reactive, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  MCPStatus,
  MCPTools,
  MCPClientConfig,
  MCPSetLevel,
} from '../../bindings/quickdock/services/mcp/mcpservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

export interface MCPToolInfo {
  name: string
  description: string
  level: number
}

// useMCPPanel 内置 MCP 服务的面板状态：监听地址、已开放工具与客户端配置。
// 从 EnvironmentPage 抽出（页面 script 过长），模板引用保持同名，故原样解构即可。
export function useMCPPanel(selectedId: Ref<string>) {
  const { t } = useI18n()
  const toast = inject<{ error: (m: string) => void; success: (m: string) => void }>('toast')!

  const mcpInfo = reactive({ running: false, endpoint: '', port: 0, maxLevel: 1, toolCount: 0 })
  const mcpTools = ref<MCPToolInfo[]>([])
  const mcpCfg = reactive({ url: '', json: '', cli: '' })
  const mcpLoading = ref(false)

  // loadMCP 拉取 MCP 状态/工具/客户端配置。服务启停后需重新调用（版本表的启停按钮走 Env 通用接口，
  // 不经过本面板，故额外在 selectedId 变化与本面板刷新按钮时各拉一次）。
  async function loadMCP() {
    mcpLoading.value = true
    try {
      const [st, tools, cfg] = await Promise.all([MCPStatus(), MCPTools(), MCPClientConfig()])
      const s = unwrap<{ running: boolean; endpoint: string; port: number; maxLevel: number; tools: number }>(st)
      if (s) Object.assign(mcpInfo, s)
      mcpTools.value = unwrap<MCPToolInfo[]>(tools) || []
      const c = unwrap<{ url: string; json: string; cli: string; running: boolean }>(cfg)
      if (c) Object.assign(mcpCfg, { url: c.url || '', json: c.json || '', cli: c.cli || '' })
    } catch (e) {
      toast.error(getErrorMessage(e))
    } finally {
      mcpLoading.value = false
    }
  }

  async function copyMCP(text: string) {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      toast.success(t('copied'))
    } catch (e) {
      toast.error(getErrorMessage(e))
    }
  }

  // setMCPLevel 切换工具权限等级（0=只读，1=只读+低危写），后端立即对注册表生效，
  // 已在运行的服务需重启后 tools/list 才变化（工具集在服务启动时按等级注册）。
  async function setMCPLevel() {
    try {
      unwrap(await MCPSetLevel(mcpInfo.maxLevel))
      toast.success(t('saved'))
      await loadMCP()
    } catch (e) {
      toast.error(getErrorMessage(e))
    }
  }

  watch(selectedId, (id) => { if (id === 'mcp') loadMCP() })

  return { mcpInfo, mcpTools, mcpCfg, mcpLoading, loadMCP, copyMCP, setMCPLevel }
}
