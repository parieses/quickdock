import { inject, reactive, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  MCPStatus,
  MCPTools,
  MCPClientConfig,
  MCPSetLevel,
  MCPStart,
  MCPStop,
  MCPRestart,
  MCPCheck,
} from '../../bindings/quickdock/services/mcp/mcpservice'
import { unwrap } from '../utils/api'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'

export interface MCPToolInfo {
  name: string
  description: string
  level: number
  exposed: boolean // 当前工具权限等级下是否对客户端开放
}

// MCPClientSnippet 一种客户端的接入片段：config 可直接粘贴/执行，path 是它该写到哪。
export interface MCPClientSnippet {
  id: string
  label: string
  path: string
  config: string
}

// useMCPPanel 内置 MCP 服务的面板状态：监听地址、工具权限、客户端配置与已开放工具。
// 从 EnvironmentPage 抽出（页面 script 过长），模板引用保持同名，故原样解构即可。
export function useMCPPanel(selectedId: Ref<string>) {
  const { t } = useI18n()
  const toast = inject<{ error: (m: string) => void; success: (m: string) => void }>('toast')!

  const mcpInfo = reactive({ running: false, endpoint: '', port: 0, maxLevel: 1, tools: 0, total: 0, blocked: 0 })
  const mcpTools = ref<MCPToolInfo[]>([])
  const mcpClients = ref<MCPClientSnippet[]>([])
  const mcpLoading = ref(false)
  const mcpBusy = ref(false)
  // mcpLevelHint 等级改动后的提示：客户端要重新握手才看得到新工具集。
  const mcpLevelHint = ref('')
  // mcpCheck 自检结论：端口在监听 ≠ 服务可用，握手 + 列工具才算通。
  const mcpCheck = reactive({ busy: false, ok: false, text: '' })

  // loadMCP 拉取 MCP 状态/工具/客户端配置。服务启停后需重新调用（版本表的启停按钮走 Env 通用接口，
  // 不经过本面板，故额外在 selectedId 变化与本面板刷新按钮时各拉一次）。
  async function loadMCP() {
    mcpLoading.value = true
    try {
      const [st, tools, cfg] = await Promise.all([MCPStatus(), MCPTools(), MCPClientConfig()])
      const s = unwrap<{ running: boolean; endpoint: string; port: number; maxLevel: number; tools: number; total: number; blocked: number }>(st)
      if (s) Object.assign(mcpInfo, s)
      mcpTools.value = unwrap<MCPToolInfo[]>(tools) || []
      const c = unwrap<{ clients: MCPClientSnippet[] }>(cfg)
      mcpClients.value = (c?.clients || []).map((x) => ({
        id: x.id || '',
        label: x.label || '',
        path: x.path || '',
        config: x.config || '',
      }))
    } catch (e) {
      toast.error(getErrorMessage(e))
    } finally {
      mcpLoading.value = false
    }
  }

  async function copyMCP(text: string) {
    if (!text) return
    // 统一入口（后端 Win32 写入），避免 WebView2 里 navigator.clipboard 被静默拒绝。
    if (await copyToClipboard(text)) {
      toast.success(t('copied'))
    } else {
      toast.error(t('copyFailed'))
    }
  }

  // startMCP / stopMCP / restartMCP 走 MCPService 专用入口，而不是环境管理的通用
  // EnvStart/EnvStop：通用入口要拼 runtime id 与版本号，「builtin」这个版本号只有
  // MCP 自己知道，漏到页面里就会出现第二处「内置版本叫什么」的答案。
  async function startMCP() {
    mcpBusy.value = true
    try {
      unwrap(await MCPStart())
      mcpLevelHint.value = ''
      mcpCheck.text = ''
      await loadMCP()
      toast.success(t('mcpStarted'))
    } catch (e) {
      toast.error(getErrorMessage(e))
    } finally {
      mcpBusy.value = false
    }
  }

  async function stopMCP() {
    mcpBusy.value = true
    try {
      unwrap(await MCPStop())
      mcpCheck.text = ''
      await loadMCP()
      toast.success(t('mcpStopped'))
    } catch (e) {
      toast.error(getErrorMessage(e))
    } finally {
      mcpBusy.value = false
    }
  }

  async function restartMCP() {
    mcpBusy.value = true
    try {
      unwrap(await MCPRestart())
      mcpLevelHint.value = ''
      mcpCheck.text = ''
      await loadMCP()
      toast.success(t('mcpRestarted'))
    } catch (e) {
      toast.error(getErrorMessage(e))
    } finally {
      mcpBusy.value = false
    }
  }

  // setMCPLevel 切换工具权限等级（0=只读，1=只读+低危写），后端立即对注册表生效，
  // 但已连上的客户端要重新握手（或重启服务）才看得到新的工具集，故给出提示。
  async function setMCPLevel() {
    mcpBusy.value = true
    try {
      const r = unwrap<{ maxLevel: number; needsRestart: boolean }>(await MCPSetLevel(mcpInfo.maxLevel))
      toast.success(t('saved'))
      mcpLevelHint.value = r?.needsRestart ? t('mcpLevelNeedsRestart') : ''
      mcpCheck.text = ''
      await loadMCP()
    } catch (e) {
      toast.error(getErrorMessage(e))
    } finally {
      mcpBusy.value = false
    }
  }

  // checkMCP 让后端像真实客户端一样走一次 initialize + tools/list。
  // 放后端做而不是前端 fetch：WebView 的源不是 127.0.0.1，跨域请求会被浏览器按
  // CORS 拦下，且服务端还会按 Origin 头拒绝——前端永远只能看到「请求失败」，
  // 拿不到「服务到底通不通」。
  async function checkMCP() {
    mcpCheck.busy = true
    mcpCheck.text = ''
    try {
      const d = unwrap<{ ok: boolean; tools: number; latencyMs: number; protocolVersion: string; serverVersion: string }>(
        await MCPCheck(),
      )
      mcpCheck.ok = !!d?.ok
      mcpCheck.text = d?.ok
        ? t('mcpCheckOk', { tools: d.tools, ms: d.latencyMs, proto: d.protocolVersion || '—', ver: d.serverVersion || '—' })
        : t('mcpCheckFailed')
    } catch (e) {
      mcpCheck.ok = false
      mcpCheck.text = getErrorMessage(e)
    } finally {
      mcpCheck.busy = false
    }
  }

  watch(selectedId, (id) => { if (id === 'mcp') loadMCP() })

  return {
    mcpInfo, mcpTools, mcpClients, mcpLoading, mcpBusy, mcpLevelHint, mcpCheck,
    loadMCP, copyMCP, setMCPLevel, startMCP, stopMCP, restartMCP, checkMCP,
  }
}
