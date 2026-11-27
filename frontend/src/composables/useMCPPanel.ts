import { computed, inject, reactive, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  MCPStatus,
  MCPTools,
  MCPClientConfig,
  MCPDomains,
  MCPDoctor,
  MCPSetLevel,
  MCPSetToolEnabled,
  MCPStart,
  MCPStop,
  MCPRestart,
  MCPCheck,
  MCPToken,
  MCPSetTokenEnabled,
  MCPRotateToken,
} from '../../bindings/quickdock/services/mcp/mcpservice'
import { AIToolPolicy } from '../../bindings/quickdock/services/ai/aiservice'
import { unwrap } from '../utils/api'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'

import { reportError } from '../utils/reportError'
export interface MCPToolInfo {
  name: string
  description: string
  level: number
  domain: string // 所属域（app/env/note…），面板据此分组
  exposed: boolean // 等级门 + 单工具开关都通过，**外部 MCP 客户端**实际能看到
  disabled: boolean // 被单独关掉：与「没资格」区分，前者可重新开启
  // agentOnly 只给内置 AI 用（改文件、跑命令、抓网页那批）：
  // 外部客户端看不到它，所以 exposed 为 false 是正常的，不是「被挡住」。
  agentOnly?: boolean
  // aiOff 内置 AI 侧是否关掉了这个工具。**只读展示**：面板上改不了，
  // 也不能由这里的开关推导——两边是两份独立策略，见 ai_tool_scope.go。
  aiOff?: boolean
  // reason 它没进外部客户端工具面的原因（超出等级上限 / 已单独关闭 / 不对外）。
  // 来自 MCPDoctor：面板上「关着」的行悬停就能看到是哪一层关的，不用猜。
  reason?: string
  // notReady 运行环境未就绪（如 file_search 需要 Everything 在跑）：开关开着也调不到。
  // notReadyNote 是具体原因，作为徽标的悬停说明。
  notReady?: boolean
  notReadyNote?: string
}

// MCPToolGroup 面板里的一个工具分组。
export interface MCPToolGroup {
  category: string
  label: string
  desc: string
  tools: MCPToolInfo[]
}

// MCPDomainMeta 后端登记的一个能力域。
// 分组顺序、标题、说明、对外可见性全部来自这里，前端不再维护第二份顺序表——
// 以前 MCP_CATEGORY_ORDER + MCP_CATEGORY_I18N 两处硬编码，加一个域必漏一处。
interface MCPDomainMeta {
  id: string
  label: string // i18n key（如 mcpCatEnv），不是文案本身
  order: number
  desc: string
  public: boolean
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
  const toast = inject<{
    error: (m: string) => void
    success: (m: string) => void
    confirm: (m: string) => Promise<boolean>
  }>('toast')!

  const mcpInfo = reactive({ running: false, endpoint: '', port: 0, maxLevel: 1, tools: 0, total: 0, blocked: 0, agent: 0 })
  const mcpTools = ref<MCPToolInfo[]>([])
  const mcpClients = ref<MCPClientSnippet[]>([])
  // mcpDomains 后端登记的能力域：分组顺序、标题 i18n key、说明、对外可见性。
  const mcpDomains = ref<MCPDomainMeta[]>([])
  const mcpLoading = ref(false)
  const mcpBusy = ref(false)
  // mcpLevelHint 等级改动后的提示：客户端要重新握手才看得到新工具集。
  const mcpLevelHint = ref('')
  // mcpCheck 自检结论：端口在监听 ≠ 服务可用，握手 + 列工具才算通。
  const mcpCheck = reactive({ busy: false, ok: false, text: '' })

  // 访问令牌单独一块状态：它是敏感值，不塞进 mcpInfo（状态刷新会把整份对象
  // Object.assign 过去，混在一起很容易被日志/调试输出带出去）。
  const mcpToken = reactive({ value: '', require: false, visible: false, busy: false })
  // 掩码只用于「不显示」的那一刻：复制、客户端接入片段永远用真值。
  const mcpTokenMasked = computed(() => {
    const v = mcpToken.value
    if (!v) return ''
    if (v.length <= 8) return '••••••••'
    return v.slice(0, 4) + '••••••••••••••••••••••••' + v.slice(-4)
  })

  // loadMCP 拉取 MCP 状态/工具/客户端配置。服务启停后需重新调用（版本表的启停按钮走 Env 通用接口，
  // 不经过本面板，故额外在 selectedId 变化与本面板刷新按钮时各拉一次）。
  async function loadMCP() {
    mcpLoading.value = true
    try {
      const [st, tools, cfg, tk, dm, aiPol, doc] = await Promise.all([
        MCPStatus(), MCPTools(), MCPClientConfig(), MCPToken(), MCPDomains(), AIToolPolicy(), MCPDoctor(),
      ])
      const s = unwrap<{ running: boolean; endpoint: string; port: number; maxLevel: number; tools: number; total: number; blocked: number; agent: number }>(st)
      if (s) Object.assign(mcpInfo, s)
      mcpTools.value = unwrap<MCPToolInfo[]>(tools) || []
      mcpDomains.value = unwrap<MCPDomainMeta[]>(dm) || []
      // AI 侧只要一份「被关掉的名字」：AI 的策略对象整份很大（含全量工具表），
      // 这里不把它存下来，免得两份数据混在一个 ref 里被误当成同一份状态。
      // 注意语义：tool.aiOff 只是给用户看的提示，不参与下面任何开关判断。
      const ai = unwrap<{ disabled: string[] }>(aiPol)
      const off = new Set(ai?.disabled || [])
      for (const tool of mcpTools.value) tool.aiOff = off.has(tool.name)
      // 「为什么调不到」：自检给出的是逐工具的一句话原因，挂到行上做悬停提示。
      // 单独拉一次而不是让 MCPTools 返回来：这段判断要用 Resolve 跑一遍装配，
      // 而 MCPTools 是纯清单——把它并进去等于每次刷新都多算一份面。
      const dr = unwrap<{ tools: { name: string; reason: string }[] }>(doc)
      const reason = new Map((dr?.tools || []).map((x) => [x.name, x.reason]))
      for (const tool of mcpTools.value) {
        const why = reason.get(tool.name)
        if (why) tool.reason = why
      }
      const t0 = unwrap<{ token: string; requireToken: boolean }>(tk)
      if (t0) {
        mcpToken.value = t0.token || ''
        mcpToken.require = !!t0.requireToken
      }
      const c = unwrap<{ clients: MCPClientSnippet[] }>(cfg)
      mcpClients.value = (c?.clients || []).map((x) => ({
        id: x.id || '',
        label: x.label || '',
        path: x.path || '',
        config: x.config || '',
      }))
    } catch (e) {
      reportError(e, 'useMCPPanel.loadMCP')
    } finally {
      mcpLoading.value = false
    }
  }

  // ---- 访问令牌 ----

  function toggleMCPTokenVisible() { mcpToken.visible = !mcpToken.visible }

  async function copyMCPToken() { await copyMCP(mcpToken.value) }

  // setMCPTokenRequired 不用 v-model：开关要先落盘成功才改状态，失败得把
  // checkbox 拨回去，否则界面显示「已开启」而后端其实没开。
  async function setMCPTokenRequired(e: Event) {
    const input = e.target as HTMLInputElement
    const next = input.checked
    if (mcpToken.busy || next === mcpToken.require) return
    mcpToken.busy = true
    try {
      const r = unwrap<{ token: string; requireToken: boolean }>(await MCPSetTokenEnabled(next))
      mcpToken.require = !!r?.requireToken
      if (r?.token) mcpToken.value = r.token
      // 开关一变，六份接入片段里的请求头也得跟着变，重拉一次而不是前端自己拼。
      await loadMCP()
      toast.success(t(mcpToken.require ? 'mcpTokenEnabled' : 'mcpTokenDisabled'))
    } catch (err) {
      input.checked = mcpToken.require
      reportError(err, 'useMCPPanel.setMCPTokenRequired')
    } finally {
      mcpToken.busy = false
    }
  }

  // rotateMCPToken 会让旧令牌立即失效，所有已配好的客户端都要重新复制片段，
  // 所以必须先问一次；轮换后收起明文，避免新令牌长时间挂在屏幕上。
  async function rotateMCPToken() {
    if (mcpToken.busy) return
    if (!(await toast.confirm(t('mcpTokenRotateConfirm')))) return
    mcpToken.busy = true
    try {
      const r = unwrap<{ token: string; requireToken: boolean }>(await MCPRotateToken())
      mcpToken.value = r?.token || ''
      mcpToken.require = !!r?.requireToken
      mcpToken.visible = false
      await loadMCP()
      toast.success(t('mcpTokenRotated'))
    } catch (err) {
      reportError(err, 'useMCPPanel.rotateMCPToken')
    } finally {
      mcpToken.busy = false
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
      reportError(e, 'useMCPPanel.startMCP')
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
      reportError(e, 'useMCPPanel.stopMCP')
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
      reportError(e, 'useMCPPanel.restartMCP')
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
      reportError(e, 'useMCPPanel.setMCPLevel')
    } finally {
      mcpBusy.value = false
    }
  }

  // setMCPToolEnabled 逐条开关工具（参数取反：tool.disabled 为 true 即重新开启）。
  // 与等级一样落盘并立即对注册表生效，但已连上的客户端要重新握手才看得到，
  // 故复用同一条「需要重启」提示，而不是再编一句意思一样的话。
  async function setMCPToolEnabled(tool: MCPToolInfo) {
    mcpBusy.value = true
    try {
      const r = unwrap<{ needsRestart: boolean }>(await MCPSetToolEnabled(tool.name, tool.disabled))
      toast.success(t('saved'))
      mcpLevelHint.value = r?.needsRestart ? t('mcpToolNeedsRestart') : ''
      mcpCheck.text = ''
      await loadMCP()
    } catch (e) {
      reportError(e, 'useMCPPanel.setMCPToolEnabled')
    } finally {
      mcpBusy.value = false
    }
  }

  // domainLabel 把后端给的 i18n key 翻成文案。key 缺失时退回域 id：
  // vue-i18n 查不到会原样返回 key，直接显示 "mcpCatFoo" 比显示 "foo" 更像 bug。
  function domainLabel(d: MCPDomainMeta): string {
    if (!d.label) return d.id
    const s = t(d.label)
    return s === d.label ? d.id : s
  }

  // mcpToolGroups 按域分组。顺序、标题、说明全部取自后端目录，前端不再维护第二份
  // 顺序表——以前 MCP_CATEGORY_ORDER / MCP_CATEGORY_I18N 两处硬编码，加一个域必漏一处。
  // 后端没登记的域（理论上不会有）排到末尾并直接用 id 当标题。
  const mcpToolGroups = computed<MCPToolGroup[]>(() => {
    const buckets = new Map<string, MCPToolInfo[]>()
    for (const tool of mcpTools.value) {
      const key = tool.domain || 'other'
      const list = buckets.get(key)
      if (list) list.push(tool)
      else buckets.set(key, [tool])
    }
    const meta = new Map(mcpDomains.value.map((d) => [d.id, d]))
    const ordered = [...mcpDomains.value].sort((a, b) => a.order - b.order).map((d) => d.id)
    const rest = [...buckets.keys()].filter((k) => !meta.has(k))
    const groups: MCPToolGroup[] = []
    for (const category of [...ordered, ...rest]) {
      const tools = buckets.get(category)
      if (!tools?.length) continue
      const d = meta.get(category)
      groups.push({
        category,
        label: d ? domainLabel(d) : category,
        desc: d?.desc || '',
        tools,
      })
    }
    return groups
  })

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
    mcpInfo, mcpTools, mcpToolGroups, mcpClients, mcpLoading, mcpBusy, mcpLevelHint, mcpCheck,
    mcpToken, mcpTokenMasked,
    loadMCP, copyMCP, setMCPLevel, setMCPToolEnabled, startMCP, stopMCP, restartMCP, checkMCP,
    toggleMCPTokenVisible, copyMCPToken, setMCPTokenRequired, rotateMCPToken,
  }
}
