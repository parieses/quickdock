<script setup lang="ts">
import { ref, computed, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pen, Trash2, Plus, RefreshCw, ChevronDown } from '@lucide/vue'
import SettingsPrompts from './SettingsPrompts.vue'
import SettingsSkills from './SettingsSkills.vue'
import QdBarChart from './charts/QdBarChart.vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useAiCostPreference } from '../composables/useAiCostPreference'

import {
  AIApprovalStats,
  AIFetchModels,
  AIGetMemoryAuto,
  AIListApprovalRules,
  AIListProfiles,
  AISaveProfiles,
  AISetApprovalRule,
  AISetMemoryAuto,
  AITestConnection,
  AIUsageStats,
  AIGetExtServers,
  AISaveExtServers,
  AISetExtServerEnabled,
  AIReloadExtServers,
  AIListExtTools,
} from '../../bindings/quickdock/services/ai/aiservice'
import { MCPSetToolEnabled } from '../../bindings/quickdock/services/mcp/mcpservice'
import { EnvOllamaModels } from '../../bindings/quickdock/services/env/environmentservice'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type {
  AIApprovalRuleView,
  AIApprovalStatsResult,
  AICostEntry,
  AIExtServerState,
  AIProfilesResult,
  AIUsageStatsResult,
} from '../types/ai'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
// 单条费用显示开关：与消息列表共享同一份偏好（见 useAiCostPreference），
// 关掉只收起气泡尾部那一行，设置页的用量总量照常显示。
// 落盘由 composable 里的 watch 负责，这里只改那一份单例 ref。
const { showMsgCost } = useAiCostPreference()
function onToggleShowMsgCost(on: boolean) { showMsgCost.value = on }

const emit = defineEmits<{ close: [] }>()
const props = defineProps<{ visible: boolean; initialTab?: string }>()

// ── 页内分段 ────────────────────────────────────────────────────────
// 原来六块内容全堆在一个滚动区里，找「外部 MCP 重连」要滚过整张用量表格。
// 分段按钮沿用「历史」页的 qd-btn / is-on 范式（SettingsHistory.vue），不另造样式。
// 面板用 v-show 而不是 v-if：所有数据 ref 都在本组件，SettingsPrompts 的编辑草稿
// 还在子组件里，切走再切回来不该被销毁重建，也不该每次重发一遍加载请求。
type AiSettingsTab = 'model' | 'usage' | 'permission' | 'mcp' | 'skills' | 'prompts'
const AI_TABS: AiSettingsTab[] = ['model', 'usage', 'permission', 'mcp', 'skills', 'prompts']

/**
 * 初始分段：从总览的「AI 用量」卡、「+ 菜单 → 提示词/MCP」这类深链进来时，
 * 应当直接落在对应面板上，而不是每次都从「模型配置」翻起。
 *
 * 与 activePage 一样依赖上层是 v-if 挂载（SettingsModal 的 activePage === 'ai' 分支）：
 * 组件重建即读到新值。若哪天改成 v-show，这段要补 watch。
 * 非法值静默回落 model —— 深链是「尽量帮忙」，不该因为传错一个字符串就白屏。
 */
const initial = props.initialTab as AiSettingsTab | undefined
const activeTab = ref<AiSettingsTab>(initial && AI_TABS.includes(initial) ? initial : 'model')
const aiTabs = computed<{ value: AiSettingsTab; label: string }[]>(() => [
  { value: 'model', label: t('aiTabModel') },
  { value: 'usage', label: t('aiTabUsage') },
  { value: 'permission', label: t('aiTabPermission') },
  { value: 'mcp', label: t('aiTabMcp') },
  { value: 'skills', label: t('aiTabSkills') },
  { value: 'prompts', label: t('aiTabPrompts') },
])

// 外部 MCP 的两段 placeholder 是「JSON 片段 / 命令行参数」的语法示例，不走 i18n：
//   1) 内容全是 JSON 与包名，各语言相同，翻译没有意义；
//   2) '{' '}' 在 vue-i18n 里是插值定界符、'@' 是 linked-message 前缀，放进 messages
//      会被当语法解析 —— 开发环境只打一条警告，生产构建直接抛 SyntaxError，
//      整个页面渲染失败（表现为「新建任务页白屏 / 输入框消失」）。
const EXT_IMPORT_PH = '{\n  "filesystem": {\n    "command": "npx",\n    "args": ["-y", "@modelcontextprotocol/server-filesystem", "D:/proj"]\n  },\n  "remote": { "url": "http://127.0.0.1:9000/mcp",\n    "headers": { "Authorization": "Bearer sk-xxx" } }\n}'
const EXT_ARGS_PH = '-y\n@modelcontextprotocol/server-filesystem\nD:/proj'
const EXT_HEADERS_PH = 'X-API-Key: sk-xxx\nX-Tenant: acme'

const aiPresets: Record<string, string> = {
  openai: 'https://api.openai.com/v1',
  deepseek: 'https://api.deepseek.com/v1',
  kimi: 'https://api.moonshot.cn/v1',
  qwen: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
  ollama: 'http://localhost:11434/v1',
  azure: 'https://{resource}.openai.azure.com',
  custom: '',
}
const aiProviders = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'kimi', label: 'Kimi (Moonshot)' },
  { value: 'qwen', label: '通义千问 (Qwen)' },
  { value: 'ollama', label: 'Ollama (本地)' },
  { value: 'azure', label: 'Azure OpenAI' },
  { value: 'custom', label: '自定义 / 其他' },
]
const aiProfiles = ref<AIProfile[]>([])
const aiActive = ref('')
const aiMsg = ref('')
const aiMsgError = ref(false)
const aiTesting = ref(false)
const aiEditDraft = ref<AIProfile | null>(null)
let aiMsgTimer: ReturnType<typeof setTimeout> | null = null

// modelOptions 模型候选列表，来源有两类：
//   - Ollama：编辑档案时自动拉本机已下载模型（EnvOllamaModels）；
//   - 其它兼容接口：用户点「拉取模型列表」后从 /models 拉取（AIFetchModels）。
// 两路共用同一份候选与下拉，避免维护两套 UI、两种交互。
const modelOptions = ref<string[]>([])
const modelOptionsLoading = ref(false)
const modelOptionsOpen = ref(false)
const modelOptionsError = ref('')

// filteredModelOptions 下拉内容：已输入则按子串过滤（大小写不敏感），未输入则全列。
// 这样「点箭头看全部、边打边缩」两种用法都成立。
const filteredModelOptions = computed(() => {
  const kw = (aiEditDraft.value?.model || '').trim().toLowerCase()
  if (!kw || modelOptions.value.includes(aiEditDraft.value?.model || '')) {
    return modelOptions.value
  }
  return modelOptions.value.filter((m) => m.toLowerCase().includes(kw))
})

function resetModelOptions() {
  modelOptions.value = []
  modelOptionsError.value = ''
  modelOptionsOpen.value = false
}

function onModelFocus() {
  if (modelOptions.value.length) modelOptionsOpen.value = true
}

function pickModel(name: string) {
  if (aiEditDraft.value) aiEditDraft.value.model = name
  modelOptionsOpen.value = false
}

async function loadOllamaLocalModels() {
  if (modelOptionsLoading.value) return
  modelOptionsLoading.value = true
  modelOptionsError.value = ''
  try {
    const ms = unwrap<{ name: string }[]>(await EnvOllamaModels()) || []
    modelOptions.value = ms.map((m) => m.name).filter(Boolean)
  } catch {
    modelOptions.value = []
  } finally {
    modelOptionsLoading.value = false
  }
}

// fetchApiModels 从兼容接口拉取模型列表（OpenAI 兼容 GET /models）。
// 直接把「编辑中的草稿」传给后端：用户刚填好 BaseURL/Key、尚未保存时也能拉取；
// 若 Key 是回填的掩码，后端会回退到库里该档案解密后的 Key（见 AIFetchModels）。
async function fetchApiModels() {
  const d = aiEditDraft.value
  if (!d || modelOptionsLoading.value) return
  const needKey = d.provider !== 'ollama'
  if (!d.baseURL || (needKey && !d.apiKey)) {
    modelOptionsError.value = t('aiFetchNeedBaseKey')
    return
  }
  modelOptionsLoading.value = true
  modelOptionsError.value = ''
  try {
    const res = unwrap<{ models?: string[] }>(
      await AIFetchModels({ id: d.id, provider: d.provider, baseURL: d.baseURL, apiKey: d.apiKey }),
    )
    const list = res?.models ?? []
    modelOptions.value = list
    modelOptionsOpen.value = list.length > 0
    if (!list.length) modelOptionsError.value = t('aiNoModelsFound')
  } catch (e: any) {
    modelOptionsError.value = t('aiFetchModelsFailed') + ': ' + (e?.message || String(e))
  } finally {
    modelOptionsLoading.value = false
  }
}

const aiCurrent = computed<AIProfile | null>(() =>
  aiProfiles.value.find((p) => p.id === aiActive.value) || null,
)

function showAIMsg(text: string, isError = false) {
  aiMsg.value = text
  aiMsgError.value = isError
  if (aiMsgTimer !== null) clearTimeout(aiMsgTimer)
  aiMsgTimer = setTimeout(() => { aiMsg.value = ''; aiMsgError.value = false }, 4000)
}

function newAIProfile(): AIProfile {
  const provider = 'openai'
  return {
    id: crypto.randomUUID ? crypto.randomUUID() : String(Date.now() + Math.random()),
    name: '',
    provider,
    baseURL: aiPresets[provider] || '',
    apiKey: '',
    model: 'gpt-4o-mini',
    temperature: 0.7,
    maxTokens: 8192,
    systemPrompt: '',
    topP: 0,
    frequencyPenalty: 0,
    presencePenalty: 0,
    thinkingEnabled: true,
    imageInput: 'both',
    // 0 = 自动（按模型名查内置表 + 撞了 400 后按上游报的真实值校正）。
    contextWindow: 0,
  }
}

async function loadAIProfiles() {
  try {
    const res = unwrap<AIProfilesResult>(await AIListProfiles())
    if (!res) return
    aiProfiles.value = res.profiles ?? []
    aiActive.value = res.active || (aiProfiles.value[0]?.id ?? '')
    if (aiProfiles.value.length === 0) {
      const p = newAIProfile()
      aiProfiles.value = [p]
      aiActive.value = p.id
    }
  } catch (e) {
    showAIMsg(t('loadFailed') + ': ' + getErrorMessage(e), true)
  }
}

// ── 用量统计与花费估算 ───────────────────────────────────────────────
// 默认「近 30 天」：AI 花费通常按月评估，7 天样本太小，全部时间又拉不出趋势。
const usageDays = ref(30)
const usageLoading = ref(false)
const usageError = ref('')
const usage = ref<AIUsageStatsResult | null>(null)
// 审批统计与用量共用同一个时间窗口：两个数字要能互相解释（花在哪些工具上、
// 其中多少被拦下来了），分成两套筛选就会得出对不上的结论。
const approval = ref<AIApprovalStatsResult | null>(null)

const usageRanges = computed(() => [
  { days: 7, label: t('aiUsageRange7') },
  { days: 30, label: t('aiUsageRange30') },
  { days: 0, label: t('aiUsageRangeAll') },
])

async function loadUsage(days: number = usageDays.value) {
  usageDays.value = days
  usageLoading.value = true
  usageError.value = ''
  try {
    // 两条查询并发：它们是独立的统计，串起来只会让面板慢一倍。
    const [u, ap] = await Promise.all([AIUsageStats(days), AIApprovalStats(days)])
    usage.value = unwrap<AIUsageStatsResult>(u) ?? null
    approval.value = unwrap<AIApprovalStatsResult>(ap) ?? null
  } catch (e) {
    usageError.value = getErrorMessage(e)
    logErr('settingsAI.loadUsage', e)
  } finally {
    usageLoading.value = false
  }
}

// 审批总数：为 0 时整块不显示——没开过审批的库不该多出一行空统计。
const approvalTotal = computed(() => {
  const a = approval.value
  return a ? a.allow + a.deny + a.auto : 0
})

// 只列被拒过的工具，最多 3 个：这一段是给人看「哪里卡住了」，不是完整报表。
const topDenied = computed(() =>
  (approval.value?.byTool ?? []).filter((s) => s.deny > 0).slice(0, 3))

// ── 工具审批策略 ─────────────────────────────────────────────────────
// 默认策略由工具等级推导（写类询问、高危只给单次）。这里只开放「显式覆盖」，
// 而且必须逐项点：一个「全部改成 auto」的按钮按下去，这道闸门就只剩装饰作用了。
const apRules = ref<AIApprovalRuleView[]>([])
const apLoading = ref(false)
const apError = ref('')
// 正在保存的那一行：只锁这一行的下拉，改别的工具不必等它。
const apSaving = ref('')

// 分两段显示：可改的（写类 + 高危）在上，只读的在下且默认折叠。
//
// 只读工具也有二十来个，全铺在最上面会把真正想改的那十几个挤到看不见；
// 但完全不列又让人怀疑「git 相关的是不是漏了」——这个项目里 git 只有
// git_status / git_diff 两个只读工具，没有 git_commit / git_push。
const apRulesEditable = computed(() => apRules.value.filter((r) => r.editable))
const apRulesReadOnly = computed(() => apRules.value.filter((r) => !r.editable))
const apShowReadOnly = ref(false)

async function loadApprovalRules() {
  apLoading.value = true
  apError.value = ''
  try {
    apRules.value = unwrap<AIApprovalRuleView[]>(await AIListApprovalRules()) ?? []
  } catch (e) {
    apError.value = getErrorMessage(e)
  } finally {
    apLoading.value = false
  }
}

// mode 传空串表示恢复默认（后端把这条覆盖删掉）。
async function saveApprovalRule(rule: AIApprovalRuleView, mode: string) {
  apSaving.value = rule.tool
  apError.value = ''
  try {
    unwrap(await AISetApprovalRule(rule.tool, mode))
    // 重新拉一次而不是本地改：生效值可能有后端规整（比如读类直接回绝），
    // 本地改出来的「看起来成功了」比多一次请求贵得多。
    await loadApprovalRules()
  } catch (e) {
    const msg = getErrorMessage(e)
    // 失败同样要重拉：只回改本地 ref 拨不动已经被用户改过的 <select> DOM，
    // 界面会停在一个假的「改成功了」上。
    await loadApprovalRules().catch(() => {})
    apError.value = msg
  } finally {
    apSaving.value = ''
  }
}

// ── 外部 MCP 服务器（阶段 D）────────────────────────────────────────
// 状态由后端维护（连接、注册工具都在那边），这里负责展示与增删改。
//
// 原来是「一整份 JSON 文本框」：字段确实和 Claude Desktop / Cursor 的 mcp.json
// 一致，但每加一个服务器都要手写括号、引号、逗号，写错一个字符整份都保存不了，
// 而且报错只说「JSON 解析失败」。现在改成表单：名字、类型、命令、参数、环境变量、
// 认证令牌各一格；JSON 留两条通道——「编辑配置 JSON」改整份原文，
// 「粘贴导入」把别人给的片段合并进来。两条都认 headers，不再丢认证信息。
const extServers = ref<AIExtServerState[]>([])
// 失败数挂在 MCP 分段上：外部 server 启动时只连一次、失败不会自动重试
// （services/ai/ext_mcp.go loadExtOnStart），用户往往就是看到红字才进来重连。
const extErrorCount = computed(() => extServers.value.filter(s => s.status === 'error').length)
const extLoading = ref(false)
const extSaving = ref(false)
const extError = ref('')

// 表单：origName 非空表示在改既有项（改名 = 覆盖旧名的同时删掉旧键）
interface ExtForm {
  origName: string
  name: string
  kind: 'stdio' | 'http'
  command: string
  argsText: string
  envText: string
  url: string
  token: string
  headersText: string
}
const extForm = ref<ExtForm | null>(null)
// 令牌默认是掩码，编辑时也不把明文摊在屏幕上；想核对内容自己点「显示」。
const extTokenVisible = ref(false)
const extImportOpen = ref(false)
const extImportDraft = ref('')
// 「编辑配置 JSON」：一份当前 mcp.json 的原文草稿，保存即整份替换。
const extEditOpen = ref(false)
const extEditDraft = ref('')

// ---- 工具详情展开行 ----
// AIGetExtServers 只给工具名；描述与「是否被单独关掉」在 mcp.Catalog 里，
// 由 AIListExtTools 按 server 取。展开状态按 server 名记，服务器停用 / 重连 /
// 改名后一律收起，否则会展示上一轮的陈旧列表。
interface ExtToolInfo {
  name: string
  desc: string
  disabled: boolean
}
const extExpanded = ref<string | null>(null)
const extTools = ref<ExtToolInfo[]>([])
const extToolsLoading = ref(false)
const extToolsBusy = ref('')
// 快速连点两个 server 时，前一个请求回来不能盖掉后一个的结果。
let extToolsSeq = 0

function resetExtTools() {
  extToolsSeq++
  extExpanded.value = null
  extTools.value = []
  extToolsBusy.value = ''
  extToolsLoading.value = false
}

async function toggleExtTools(s: AIExtServerState) {
  if (extExpanded.value === s.name) { resetExtTools(); return }
  resetExtTools()
  const seq = extToolsSeq
  extExpanded.value = s.name
  extToolsLoading.value = true
  try {
    const list = unwrap<ExtToolInfo[]>(await AIListExtTools(s.name)) ?? []
    if (seq === extToolsSeq) extTools.value = list
  } catch (e) {
    logErr('[SettingsAI] AIListExtTools:', e)
    toast.error(getErrorMessage(e))
    // 这里不能再调 resetExtTools：它会推进 extToolsSeq，finally 就判不出
    // 「还是本次请求」而漏掉关 loading，展开行会一直转圈。
    if (seq === extToolsSeq) {
      extExpanded.value = null
      extTools.value = []
    }
  } finally {
    if (seq === extToolsSeq) extToolsLoading.value = false
  }
}

async function toggleExtTool(tool: ExtToolInfo) {
  if (extToolsBusy.value) return
  extToolsBusy.value = tool.name
  try {
    // 参数取反：tool.disabled 为 true 表示当前被关掉、这次要重新开启。
    // 与「环境管理 → MCP 服务」面板走的是同一个注册表开关，立即生效。
    unwrap(await MCPSetToolEnabled(tool.name, tool.disabled))
    tool.disabled = !tool.disabled
  } catch (e) {
    logErr('[SettingsAI] MCPSetToolEnabled:', e)
    toast.error(getErrorMessage(e))
  } finally {
    extToolsBusy.value = ''
  }
}

// 与后端 mcpclient.ServerConfig 同构的提交项
interface ExtInput {
  name: string
  command: string
  args: string[]
  env: Record<string, string>
  url: string
  headers: Record<string, string>
  disabled: boolean
}

async function loadExtServers() {
  extLoading.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AIGetExtServers()) ?? []
    resetExtTools()
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extLoading.value = false
  }
}

function kindOf(s: { config?: { url?: string } } | null | undefined): 'stdio' | 'http' {
  return (s?.config?.url || '').trim() ? 'http' : 'stdio'
}

/** 参数逐行存/取：用空格分隔的话，带空格或引号的参数会被拆散 */
function argsToText(args?: string[]): string {
  return (args || []).join('\n')
}
function textToArgs(s: string): string[] {
  return s.split('\n').map((x) => x.trim()).filter(Boolean)
}

/** 环境变量逐行 KEY=VALUE；只切第一个等号，值里带 = 不会被截断 */
function envToText(env?: Record<string, string>): string {
  return Object.entries(env || {}).map(([k, v]) => `${k}=${v}`).join('\n')
}
function textToEnv(s: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of s.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const i = line.indexOf('=')
    if (i <= 0) continue
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

// 请求头逐行「名称: 值」，也认「名称=值」。分隔符取更靠前的那个：
// `X-Url=http://host` 若一律按冒号切，值会被截成 `http`。
function headersToText(h?: Record<string, string>): string {
  return Object.entries(h || {}).map(([k, v]) => `${k}: ${v}`).join('\n')
}
function textToHeaders(s: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of s.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const ci = line.indexOf(':')
    const ei = line.indexOf('=')
    let i = -1
    if (ci > 0 && (ei < 0 || ci < ei)) i = ci
    else if (ei > 0) i = ei
    if (i <= 0) continue
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

/** 编辑回显：Bearer 令牌抽进专用输入框，其余请求头留在逐行文本里。
 *  Basic / 自定义 Authorization 不抽，否则提交时会被套成 Bearer Basic xxx。 */
function splitAuth(headers?: Record<string, string>): { token: string; text: string } {
  const h = { ...(headers || {}) }
  const key = Object.keys(h).find((k) => k.toLowerCase() === 'authorization')
  let token = ''
  if (key && /^Bearer\s+/i.test(h[key])) {
    token = h[key].replace(/^Bearer\s+/i, '').trim()
    delete h[key]
  }
  return { token, text: headersToText(h) }
}

/** 表单 -> headers：令牌写进 Authorization，与自定义头合并（同名时令牌优先）。 */
function extFormHeaders(f: ExtForm): Record<string, string> {
  const h = textToHeaders(f.headersText)
  const token = f.token.trim()
  if (token) h.Authorization = /^Bearer\s+/i.test(token) ? token : `Bearer ${token}`
  return h
}

/** 状态 -> 提交项：覆盖式保存时其余 server 要原样带上 */
function stateToInput(s: AIExtServerState): ExtInput {
  const c = s.config || {}
  return {
    name: s.name,
    command: c.command || '',
    args: c.args || [],
    env: c.env || {},
    url: c.url || '',
    headers: c.headers || {},
    disabled: !!c.disabled,
  }
}

/** 列表里那一行「它在跑什么」：URL 或 命令+参数 */
function extTarget(s: AIExtServerState): string {
  const c = s.config || {}
  if ((c.url || '').trim()) return c.url as string
  return [c.command || '', ...(c.args || [])].filter(Boolean).join(' ')
}

function openAddExt() {
  extForm.value = {
    origName: '', name: '', kind: 'stdio', command: '', argsText: '', envText: '',
    url: '', token: '', headersText: '',
  }
  extTokenVisible.value = false
  extImportOpen.value = false
  extEditOpen.value = false
  extError.value = ''
}

function openEditExt(s: AIExtServerState) {
  const c = s.config || {}
  const auth = splitAuth(c.headers)
  extForm.value = {
    origName: s.name,
    name: s.name,
    kind: kindOf(s),
    command: c.command || '',
    argsText: argsToText(c.args),
    envText: envToText(c.env),
    url: c.url || '',
    token: auth.token,
    headersText: auth.text,
  }
  extTokenVisible.value = false
  extImportOpen.value = false
  extEditOpen.value = false
  extError.value = ''
}

/** 覆盖式提交：后端把这份清单当「用户编辑后的全量」，所以删除也是提交少了某项的清单 */
async function commitExt(items: ExtInput[]): Promise<boolean> {
  extSaving.value = true
  extError.value = ''
  try {
    const res = unwrap<{ states: AIExtServerState[] }>(await AISaveExtServers(items))
    extServers.value = res?.states ?? []
    resetExtTools()
    extForm.value = null
    extImportOpen.value = false
    extImportDraft.value = ''
    extEditOpen.value = false
    extEditDraft.value = ''
    extTokenVisible.value = false
    return true
  } catch (e) {
    extError.value = getErrorMessage(e)
    return false
  } finally {
    extSaving.value = false
  }
}

// 名称规则与后端 validServerName 一致，前端先拦一次：不然要等一次往返才看到错
const EXT_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$/

async function submitExtForm() {
  const f = extForm.value
  if (!f) return
  const name = f.name.trim()
  if (!EXT_NAME_RE.test(name)) {
    extError.value = t('aiExtNameBad')
    return
  }
  const isHttp = f.kind === 'http'
  const url = f.url.trim()
  const command = f.command.trim()
  if (isHttp && !url) {
    extError.value = t('aiExtNeedUrl')
    return
  }
  if (!isHttp && !command) {
    extError.value = t('aiExtNeedCommand')
    return
  }
  // 同名但不同原名视为「改名」：先把旧名剔掉再追加。其余项保持原状（含 disabled）。
  const rest = extServers.value.filter((s) => s.name !== f.origName && s.name !== name).map(stateToInput)
  const prev = extServers.value.find((s) => s.name === f.origName)
  const item: ExtInput = {
    name,
    command: isHttp ? '' : command,
    args: isHttp ? [] : textToArgs(f.argsText),
    env: isHttp ? {} : textToEnv(f.envText),
    url: isHttp ? url : '',
    headers: isHttp ? extFormHeaders(f) : {},
    disabled: prev ? !!prev.config?.disabled : false,
  }
  await commitExt([...rest, item])
}

async function removeExt(s: AIExtServerState) {
  if (!(await toast.confirm(t('aiExtRemoveConfirm', { name: s.name })))) return
  await commitExt(extServers.value.filter((x) => x.name !== s.name).map(stateToInput))
}

/** 开关直接走 mcp.json 的 disabled 字段：关掉即断开，stdio 进程会被回收 */
async function toggleExt(s: AIExtServerState) {
  const enable = s.status === 'disabled'
  extSaving.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AISetExtServerEnabled(s.name, enable)) ?? []
    resetExtTools()
    // 整份 JSON 草稿还开着时同步一下 disabled，否则保存会把开关状态盖回去
    if (extEditOpen.value) extEditDraft.value = buildManifestJson()
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extSaving.value = false
  }
}

// 粘贴导入要认的形态比想象中多，各家文档给的不一样：
//   {"mcpServers": {"x": {...}}}        Claude Desktop / Cursor
//   {"servers": {"x": {...}}}           VS Code
//   {"x": {"command": ...}}             裸映射（这一栏的占位示例就是它）
//   {"command": "npx", "args": [...]}   单条配置，很多 README 直接贴这一段
//   [{"name": "x", "command": ...}]     带名字的数组
// 早先只把非 mcpServers 的一律当「名字 → 配置」映射，于是单条配置会被读成
// 一堆叫 command / args 的服务器（command 为空）→ 提交时后端整份打回，
// 表现就是「点了导入没反应」。所以先认结构，认不出就直说，不猜。

interface ParsedExt {
  name: string
  cfg: Record<string, any>
}

/** 名字落到后端的 validServerName 上：非法字符换成 -，去掉开头非字母数字，截 32 */
function sanitizeExtName(raw: string, fallback: string): string {
  let n = (raw || '').trim().replace(/^[^A-Za-z0-9]+/, '').replace(/[^A-Za-z0-9_-]+/g, '-')
  if (!n) n = fallback
  return n.slice(0, 32)
}

/** 从命令行猜个名字：`npx -y @scope/pkg@1.2` → `pkg`（通用词则取 scope）。猜不出返回空串 */
function nameFromCommand(cmd: string): string {
  const toks = cmd.trim().split(/\s+/).filter((x) => x && !x.startsWith('-'))
  if (!toks.length) return ''
  // npm 包名最像这个 server 的身份，优先用它
  const pkg = toks.find((x) => x.startsWith('@'))
  if (pkg) {
    // 先摘掉开头的 scope 前缀 @ 再去版本号：否则 /@[^@]*$/ 会把整串吃掉
    const seg = pkg.slice(1).replace(/@[^@]*$/, '').split('/').filter(Boolean) // [scope, name]
    const last = seg[seg.length - 1] || ''
    // @playwright/mcp → mcp 太泛，退一级用包主名
    if (seg.length > 1 && /^(mcp|server|cli|sdk|index|main)$/i.test(last)) return seg[0]
    return last
  }
  // 否则退到第一个像包/文件的 token（跳过目录参数与裸路径）
  const pick = toks.find((x) => /\.[a-z0-9]+$/i.test(x) || /^[a-z0-9][\w.-]*$/i.test(x)) || ''
  if (!pick) return ''
  const base = pick.split(/[\\/]/).pop() || pick
  return base.replace(/\.[a-z0-9]{1,4}$/i, '')
}

/** 拆一行命令行，认双引号（Windows 路径常带空格） */
function splitCommandLine(s: string): string[] {
  const out: string[] = []
  let cur = ''
  let quote = false
  for (const ch of s) {
    if (ch === '"') { quote = !quote; continue }
    if (!quote && /\s/.test(ch)) { if (cur) { out.push(cur); cur = '' } continue }
    cur += ch
  }
  if (cur) out.push(cur)
  return out
}

/**
 * 宽松解析：文档站给的片段常带 `//` 注释和尾逗号（jsonc），严格 JSON.parse 会直接失败。
 * 按字符扫一遍去掉注释和尾逗号，字符串里的内容原样保留。
 */
function parseLooseJson(text: string): any {
  try {
    return JSON.parse(text)
  } catch {
    /* 走 jsonc 兜底 */
  }
  let out = ''
  let q = false
  let esc = false
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (q) {
      out += ch
      if (esc) esc = false
      else if (ch === '\\') esc = true
      else if (ch === '"') q = false
      continue
    }
    if (ch === '"') { q = true; out += ch; continue }
    if (ch === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++
      out += '\n'
      continue
    }
    if (ch === '/' && text[i + 1] === '*') {
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i++
      i++
      continue
    }
    out += ch
  }
  return JSON.parse(out.replace(/,\s*([}\]])/g, '$1'))
}

/** 一条外部配置 → 提交项（不含名字）。headers 原样保留：远程 server 的
 *  Authorization / X-API-Key 都在这里，丢了就等于配好的令牌没生效。*/
function cfgToInput(cfg: Record<string, any>): {
  command: string
  args: string[]
  env: Record<string, string>
  url: string
  headers: Record<string, string>
} {
  const c = cfg || {}
  let command = String(c.command ?? c.cmd ?? '').trim()
  let args: string[] = []
  if (Array.isArray(c.args)) args = c.args.map((x) => String(x))
  else if (typeof c.args === 'string') args = splitCommandLine(c.args)
  // 「command」里塞了整条命令（"npx -y pkg"）时拆开：后端把它当可执行文件名
  if (command && !args.length && /\s/.test(command)) {
    const parts = splitCommandLine(command)
    if (parts.length > 1) { command = parts[0]; args = parts.slice(1) }
  }

  let env: Record<string, string> = {}
  if (Array.isArray(c.env)) {
    // 少数配置写成 ["KEY=VALUE"] 数组
    for (const line of c.env) {
      const s = String(line)
      const i = s.indexOf('=')
      if (i > 0) env[s.slice(0, i).trim()] = s.slice(i + 1).trim()
    }
  } else if (c.env && typeof c.env === 'object') {
    env = Object.fromEntries(Object.entries(c.env).map(([k, v]) => [k, String(v)]))
  }

  const url = String(c.url ?? c.serverUrl ?? c.server_url ?? c.endpoint ?? c.baseUrl ?? '').trim()
  // headers 既有对象写法，也有少数文档给的 ["KEY: VALUE"] 数组写法
  let headers: Record<string, string> = {}
  if (Array.isArray(c.headers)) headers = textToHeaders(c.headers.map(String).join('\n'))
  else if (c.headers && typeof c.headers === 'object') {
    headers = Object.fromEntries(Object.entries(c.headers).map(([k, v]) => [k, String(v)]))
  }
  // 有 url 就是 HTTP：把 command/args/env 清空，否则后端会判「只能填一个」
  return url ? { command: '', args: [], env: {}, url, headers } : { command, args, env, url: '', headers }
}

/** 从任意粘贴内容里认出一批「名字 + 配置」；认不出返回说明性的错误 key */
function extractExtCandidates(doc: any): ParsedExt[] | 'unrecognized' {
  if (Array.isArray(doc)) {
    return doc
      .filter((x) => x && typeof x === 'object' && !Array.isArray(x))
      .map((x: any, i: number) => ({
        name: sanitizeExtName(String(x.name ?? x.id ?? x.server ?? x.serverName ?? ''), `mcp${i + 1}`),
        cfg: x,
      }))
  }
  if (!doc || typeof doc !== 'object') return 'unrecognized'

  const wrapKey = ['mcpServers', 'mcp_servers', 'servers', 'server'].find(
    (k) => doc[k] && typeof doc[k] === 'object' && !Array.isArray(doc[k]),
  )
  if (wrapKey) {
    return Object.entries(doc[wrapKey] as Record<string, any>)
      .filter(([, v]) => v && typeof v === 'object' && !Array.isArray(v))
      .map(([k, v]) => ({ name: sanitizeExtName(k, 'mcp'), cfg: v as Record<string, any> }))
  }
  // 单条配置：自己就是一份命令或地址
  if (doc.command || doc.cmd || doc.url || doc.serverUrl || doc.endpoint) {
    const cmdLine = [String(doc.command ?? doc.cmd ?? ''), ...(Array.isArray(doc.args) ? doc.args.map(String) : [])].join(' ')
    const guess = String(doc.name ?? '') || nameFromCommand(cmdLine)
    return [{ name: sanitizeExtName(guess, 'mcp'), cfg: doc }]
  }
  // 剩下的只有当「名字 → 配置」的裸映射才认
  const entries = Object.entries(doc as Record<string, any>).filter(
    ([, v]) => v && typeof v === 'object' && !Array.isArray(v),
  )
  if (!entries.length) return 'unrecognized'
  return entries.map(([k, v]) => ({ name: sanitizeExtName(k, 'mcp'), cfg: v as Record<string, any> }))
}

async function importExtJson() {
  let doc: any
  try {
    doc = parseLooseJson(extImportDraft.value)
  } catch {
    extError.value = t('aiExtBadJson')
    return
  }
  const parsed = extractExtCandidates(doc)
  if (parsed === 'unrecognized') {
    extError.value = t('aiExtUnrecognized')
    return
  }
  if (!parsed.length) {
    extError.value = t('aiExtEmptyJson')
    return
  }

  const items = extServers.value.map(stateToInput)
  const taken = new Set<string>()
  const built: ExtInput[] = []
  const skipped: string[] = []
  const withHeaders: string[] = []
  for (const e of parsed) {
    const c = cfgToInput(e.cfg)
    if (!c.command && !c.url) {
      skipped.push(e.name)
      continue
    }
    if (Object.keys(c.headers).length) withHeaders.push(e.name)
    // 本次导入内部 sanitize 撞名（"@a/b" 与 "a-b"）时加序号，别互相覆盖
    let name = e.name
    let k = 2
    while (taken.has(name)) name = `${e.name.slice(0, 28)}-${k++}`
    taken.add(name)
    const prev = items.find((x) => x.name === name)
    built.push({
      name,
      command: c.command,
      args: c.args,
      env: c.env,
      url: c.url,
      headers: c.headers,
      // 覆盖时沿用原开关状态：导进来不该把用户停用的服务器又拉起来
      disabled: prev ? prev.disabled : !!e.cfg.disabled,
    })
  }
  if (!built.length) {
    extError.value = t('aiExtNoneValid', { names: skipped.join('、') })
    return
  }

  for (const it of built) {
    const i = items.findIndex((x) => x.name === it.name)
    if (i >= 0) items[i] = it
    else items.push(it)
  }
  // 后端整份校验不通过时不会落盘：红字在区块里，另外补一个 toast，
  // 免得导入面板收起后（或页面滚动到别处）用户以为又「没反应」
  if (!(await commitExt(items))) {
    if (extError.value) toast.error(extError.value)
    return
  }

  // 成功要说话：面板收起后只有列表变化，用户分不清「导入了」还是「没反应」
  let msg = t('aiExtImported', { n: built.length })
  const extra: string[] = []
  if (skipped.length) extra.push(t('aiExtImportSkipped', { n: skipped.length, names: skipped.join('、') }))
  if (withHeaders.length) extra.push(t('aiExtImportHeaders', { names: withHeaders.join('、') }))
  toast.success(extra.length ? `${msg}（${extra.join('；')}）` : msg)
}

/** 当前 mcp.json 的完整原文：含 headers 与令牌，给「编辑配置 JSON」直接用。
 *  只写非空字段，避免把一堆 null / 空数组带进用户要读的配置里。 */
function buildManifestJson(): string {
  const servers: Record<string, Record<string, any>> = {}
  for (const s of extServers.value) {
    const c = s.config || {}
    const item: Record<string, any> = {}
    if (c.command) item.command = c.command
    if (c.args?.length) item.args = c.args
    if (c.env && Object.keys(c.env).length) item.env = c.env
    if (c.url) item.url = c.url
    if (c.headers && Object.keys(c.headers).length) item.headers = c.headers
    if (c.disabled) item.disabled = true
    servers[s.name] = item
  }
  return JSON.stringify({ mcpServers: servers }, null, 2)
}

function openEditJson() {
  extEditOpen.value = !extEditOpen.value
  if (!extEditOpen.value) return
  extEditDraft.value = buildManifestJson()
  extImportOpen.value = false
  extError.value = ''
}

/** 与「粘贴导入」的合并语义不同：这里是整份替换，删掉一个名字就是删除它。 */
async function saveExtManifestJson() {
  let doc: any
  try {
    doc = parseLooseJson(extEditDraft.value)
  } catch {
    extError.value = t('aiExtBadJson')
    return
  }
  let parsed: ParsedExt[] | 'unrecognized' = extractExtCandidates(doc)
  // 空对象 {} 也当成「清空全部」的一种写法，否则结构识别会把它判成没认出来
  if (parsed === 'unrecognized' && doc && typeof doc === 'object' && !Array.isArray(doc) && !Object.keys(doc).length) {
    parsed = []
  }
  if (parsed === 'unrecognized') {
    extError.value = t('aiExtUnrecognized')
    return
  }
  const taken = new Set<string>()
  const items: ExtInput[] = []
  const skipped: string[] = []
  for (const e of parsed) {
    const c = cfgToInput(e.cfg)
    if (!c.command && !c.url) {
      skipped.push(e.name)
      continue
    }
    let name = e.name
    let k = 2
    while (taken.has(name)) name = `${e.name.slice(0, 28)}-${k++}`
    taken.add(name)
    items.push({
      name,
      command: c.command,
      args: c.args,
      env: c.env,
      url: c.url,
      headers: c.headers,
      disabled: !!e.cfg.disabled,
    })
  }
  // 一个合法项都没有时等于清空全部外部 server，先问一句再落盘
  if (!items.length && extServers.value.length && !(await toast.confirm(t('aiExtJsonClearConfirm')))) return
  if (!(await commitExt(items))) {
    if (extError.value) toast.error(extError.value)
    return
  }
  let msg = t('aiExtJsonSaved')
  if (skipped.length) msg += `（${t('aiExtImportSkipped', { n: skipped.length, names: skipped.join('、') })}）`
  toast.success(msg)
}

async function reloadExt() {
  extLoading.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AIReloadExtServers()) ?? []
    resetExtTools()
    if (extEditOpen.value) extEditDraft.value = buildManifestJson()
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extLoading.value = false
  }
}

// token 数量级跨得很大（几百 ~ 上千万），统一折算成 k/M，否则表格列宽会被撑爆。
function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 1 : 2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + 'k'
  return String(n)
}

// 单次问答常在 0.001 元量级，固定两位小数会全变成「¥0.00」，故按数量级自适应。
function fmtCost(n: number): string {
  if (!n) return '¥0'
  if (n >= 1) return '¥' + n.toFixed(2)
  if (n >= 0.01) return '¥' + n.toFixed(3)
  return '¥' + n.toFixed(4)
}

// 日期轴只写 M/D 才排得下，完整日期放 tooltip。
function fmtDay(day: string): string {
  const p = day.split('-')
  return p.length === 3 ? `${Number(p[1])}/${Number(p[2])}` : day
}

// 缓存命中率：分母是「输入 token」。长会话里输入的大头是每轮重复的上下文，
// 命中率 90%+ 是常态，所以这个数字比「省了多少钱」更能解释账单。
const cachePct = computed(() => {
  const u = usage.value
  if (!u || u.promptTokens <= 0 || u.cachedTokens <= 0) return 0
  return Math.round((u.cachedTokens / u.promptTokens) * 1000) / 10
})

// 来源标签：kind 是后端常量（chat/ask/title/summary/legacy），这里只做展示映射。
// 未知值原样显示 —— 后端以后新增来源时不会因为漏配文案而整条消失。
function sourceLabel(kind: string): string {
  const key = `aiUsageSource_${kind}`
  const label = t(key)
  return label === key ? kind : label
}

function entryCachePct(e: { promptTokens: number; cachedTokens: number }): number {
  if (e.promptTokens <= 0 || e.cachedTokens <= 0) return 0
  return Math.round((e.cachedTokens / e.promptTokens) * 1000) / 10
}

// 每日柱状：7/30/全部范围由 loadUsage(days) / usageDays 决定，图表组件自己不拉数据。
// 多则只画最近 30 根：全部时间窗口可能上百天，逐根铺开会糊成一片。
// 计价为 0 的窗口（例如全用免费模型/未收录模型）退回按 token 画，否则柱子全是空的。
const dailyChart = computed(() => {
  const list = (usage.value?.daily ?? []).slice(-30)
  if (!list.length) {
    return {
      labels: [] as string[], values: [] as number[], items: [] as AICostEntry[],
      name: '', color: 'accent' as 'accent' | 'warning',
    }
  }
  const useCost = list.some((d) => d.estimatedCost > 0)
  return {
    labels: list.map((d) => fmtDay(d.key)),
    values: list.map((d) => (useCost ? d.estimatedCost : d.totalTokens)),
    items: list,
    name: t(useCost ? 'chartLegendCost' : 'chartLegendTokens'),
    color: (useCost ? 'warning' : 'accent') as 'accent' | 'warning',
  }
})
const dailyChartSeries = computed(() => dailyChart.value.labels.length
  ? [{ name: dailyChart.value.name, values: dailyChart.value.values, color: dailyChart.value.color }]
  : [])
function dailyTooltipExtra(index: number): string {
  const d = dailyChart.value.items[index]
  if (!d) return ''
  return `<div>${t('chartTooltipRequests')}: ${d.requests}</div>`
    + `<div>${t('chartTooltipTokens')}: ${fmtTokens(d.totalTokens)}</div>`
    + `<div>${t('chartTooltipCost')}: ${fmtCost(d.estimatedCost)}</div>`
}

watch(() => props.visible, (v) => {
  if (!v) return
  loadAIProfiles()
  loadUsage()
  loadApprovalRules()
  loadExtServers()
  loadMemoryAuto()
}, { immediate: true })

// ---- 记忆自动整理 ----
//
// 开关状态只在后端（ai_memory_auto），前端不缓存到 localStorage：
// 那样两台机器/两处入口会出现「这里开着、那里关着」，而它决定的是花不花钱。
const memoryAuto = ref(true)
const memoryAutoSaving = ref(false)

async function loadMemoryAuto() {
  try {
    memoryAuto.value = !!unwrap<boolean>(await AIGetMemoryAuto())
  } catch (e) {
    // 读不到就按默认（开）显示：这是默认行为，不该因为一次读取失败变成「关」
    logErr('ai-memory-auto', e)
  }
}

async function onToggleMemoryAuto(on: boolean) {
  memoryAutoSaving.value = true
  try {
    memoryAuto.value = !!unwrap<boolean>(await AISetMemoryAuto(on))
    toast.success(t(memoryAuto.value ? 'aiMemoryAutoOn' : 'aiMemoryAutoOff'))
  } catch (e) {
    // 回滚到改之前的勾选状态，别让界面显示一个没存进去的值
    memoryAuto.value = !on
    toast.error(getErrorMessage(e))
  } finally {
    memoryAutoSaving.value = false
  }
}

function selectAIProfile(id: string) { aiActive.value = id }
function editAIProfile(id: string) { aiActive.value = id; openAIEditor() }
function addAIProfile() {
  const p = newAIProfile(); aiProfiles.value.push(p); aiActive.value = p.id; openAIEditor()
}
function delAIProfile(id: string) {
  aiProfiles.value = aiProfiles.value.filter((p) => p.id !== id)
  if (aiActive.value === id) aiActive.value = aiProfiles.value[0]?.id ?? ''
}
function onAIProviderChange() {
  const cur = aiCurrent.value
  if (!cur) return
  const url = aiPresets[cur.provider]
  if (url) cur.baseURL = url
}
async function saveAIProfiles() {
  try {
    unwrap(await AISaveProfiles({ active: aiActive.value, profiles: aiProfiles.value }))
    Events.Emit('ai:profiles-updated')
    showAIMsg(t('saved'))
  } catch (e) {
    showAIMsg(t('saveFailed2') + ': ' + getErrorMessage(e), true)
  }
}
async function testAIConnection() {
  const cur = aiCurrent.value
  if (!cur) return
  // Ollama 本地服务无需 API Key，只校 Base URL 与 Model。
  const needKey = cur.provider !== 'ollama'
  if (!cur.baseURL || !cur.model || (needKey && !cur.apiKey)) {
    showAIMsg(t(needKey ? 'aiFillRequired' : 'aiFillRequiredNoKey'), true); return
  }
  await saveAIProfiles()
  aiTesting.value = true; showAIMsg('测试中…')
  try {
    const res = unwrap<{ success?: boolean; message?: string }>(await AITestConnection(cur.id))
    if (!res) throw new Error('无响应')
    showAIMsg(res.message || '未知', res.success !== true)
  } catch (e: any) {
    showAIMsg(t('testFailed') + ': ' + (e?.message || String(e)), true)
  } finally { aiTesting.value = false }
}
function openAIEditor() {
  const cur = aiCurrent.value
  if (!cur) return
  aiEditDraft.value = { ...cur }
  resetModelOptions()
  if (cur.provider === 'ollama') loadOllamaLocalModels()
}
function onAIProviderChangeDraft() {
  const d = aiEditDraft.value
  if (!d) return
  const url = aiPresets[d.provider]
  if (url) d.baseURL = url
  // 切换服务商后旧的候选列表已不适用，清掉重新来。
  resetModelOptions()
  if (d.provider === 'ollama') loadOllamaLocalModels()
}
function closeAIEditor() { aiEditDraft.value = null; resetModelOptions() }
function saveAIModal() {
  const draft = aiEditDraft.value
  if (!draft) return
  const idx = aiProfiles.value.findIndex((p) => p.id === draft.id)
  if (idx >= 0) aiProfiles.value[idx] = { ...draft }
  aiEditDraft.value = null
  saveAIProfiles()
}
</script>

<template>
  <div class="section">
    <h3 class="section-title">{{ t('navAi') }}</h3>
    <p class="section-desc">{{ t('aiSettingsDesc') }}</p>

    <div class="ai-tabs" role="tablist">
      <button
        v-for="o in aiTabs"
        :key="o.value"
        type="button"
        role="tab"
        :aria-selected="activeTab === o.value"
        :class="['qd-btn', 'qd-btn-sm', { 'is-on': activeTab === o.value }]"
        @click="activeTab = o.value"
      >
        <span>{{ o.label }}</span>
        <span
          v-if="o.value === 'mcp' && extErrorCount"
          class="ai-tab-badge"
          :title="t('aiExtStatusErr')"
        >{{ extErrorCount }}</span>
      </button>
    </div>

    <div v-show="activeTab === 'model'" class="ai-tab-panel" role="tabpanel">
    <div class="ai-profiles">
      <div
        v-for="p in aiProfiles"
        :key="p.id"
        :class="['ai-profile-item', { active: p.id === aiActive }]"
      >
        <div class="ai-profile-info" @click="selectAIProfile(p.id)">
          <span class="ai-profile-name">{{ p.name || t('aiProfileDefault') }}</span>
          <span class="ai-profile-model">{{ p.model || '—' }}</span>
        </div>
        <div class="ai-profile-actions">
          <button class="ai-profile-edit" :title="t('aiEditProfile')" @click.stop="editAIProfile(p.id)">
            <Pen :size="13" />
          </button>
          <button
            v-if="aiProfiles.length > 1"
            class="ai-profile-del"
            :title="t('delete')"
            @click.stop="delAIProfile(p.id)"
          >
            <Trash2 :size="13" />
          </button>
        </div>
      </div>
      <button class="ai-profile-add" @click="addAIProfile">
        <Plus :size="13" /> {{ t('aiAddProfile') }}
      </button>
    </div>

    <p v-if="aiMsg" :class="['result-hint', { error: aiMsgError }]">{{ aiMsg }}</p>

    <div v-if="!aiEditDraft" class="ai-edit-bar">
      <button v-if="aiCurrent" class="btn btn-primary" @click="openAIEditor">
        <Pen :size="13" /> {{ t('aiEditProfile') }}
      </button>
      <button class="btn btn-secondary" @click="testAIConnection" :disabled="aiTesting">
        {{ aiTesting ? t('aiTesting') : t('aiTestConnection') }}
      </button>
      <button class="btn btn-primary" @click="saveAIProfiles">{{ t('save') }}</button>
    </div>

    <!-- 记忆：每轮结束后 AI 自己把结论/偏好摘进长期记忆（没有绑定空间的对话共用一份，
         绑定空间的写在项目里的 .quickDock）。开关放这里是因为它会多花一次小调用，
         钱得让人看得见、也随时能关。 -->
    <div class="ai-usage">
      <div class="ai-usage-head">
        <h4 class="ai-usage-title">{{ t('aiMemoryTitle') }}</h4>
      </div>
      <label class="ai-memory-row">
        <input
          type="checkbox"
          class="ai-memory-check"
          :checked="memoryAuto"
          :disabled="memoryAutoSaving"
          @change="onToggleMemoryAuto(($event.target as HTMLInputElement).checked)"
        />
        <span class="ai-memory-label">{{ t('aiMemoryAutoLabel') }}</span>
      </label>
      <p class="usage-hint">{{ t('aiMemoryHint') }}</p>
    </div>
    </div>

    <div v-show="activeTab === 'usage'" class="ai-tab-panel" role="tabpanel">
    <!-- 用量统计：花费是按内置价目表估算的，界面里必须说清楚，别让人当账单 -->
    <div class="ai-usage">
      <div class="ai-usage-head">
        <h4 class="ai-usage-title">{{ t('aiUsageTitle') }}</h4>
        <div class="ai-usage-ranges">
          <button
            v-for="r in usageRanges"
            :key="r.days"
            type="button"
            class="usage-range"
            :class="{ on: usageDays === r.days }"
            @click="loadUsage(r.days)"
          >{{ r.label }}</button>
        </div>
      </div>

      <!-- 单条费用显示：与消息列表共享偏好（useAiCostPreference），拨一下两边同时生效。
           放在用量面板而不是模型面板——它管的是「钱看不看得见」，与下面的总量是同一件事。
           复用「记忆自动整理」那套勾选行样式，不动全局 settings.css。 -->
      <label class="ai-memory-row ai-cost-pref">
        <input
          type="checkbox"
          class="ai-memory-check"
          :checked="showMsgCost"
          @change="onToggleShowMsgCost(($event.target as HTMLInputElement).checked)"
        />
        <span class="ai-memory-label">{{ t('aiShowMsgCost') }}</span>
      </label>
      <p class="usage-hint">{{ t('aiShowMsgCostHint') }}</p>

      <p v-if="usageError" class="result-hint error">{{ usageError }}</p>
      <p v-else-if="usageLoading && !usage" class="usage-hint">{{ t('aiUsageLoading') }}</p>
      <template v-else-if="usage">
        <div class="usage-tiles">
          <div class="usage-tile">
            <span class="usage-tile-label">{{ t('aiUsageCost') }}</span>
            <span class="usage-tile-value">{{ fmtCost(usage.estimatedCost) }}</span>
          </div>
          <div class="usage-tile">
            <span class="usage-tile-label">{{ t('aiUsageRequests') }}</span>
            <span class="usage-tile-value">{{ usage.requests }}</span>
          </div>
          <div class="usage-tile">
            <span class="usage-tile-label">Tokens</span>
            <span class="usage-tile-value">{{ fmtTokens(usage.totalTokens) }}</span>
          </div>
          <div class="usage-tile">
            <span class="usage-tile-label">{{ t('aiUsageConversations') }}</span>
            <span class="usage-tile-value">{{ usage.conversations }}</span>
          </div>
        </div>

        <!-- 缓存命中：长会话的输入绝大部分是重复上下文，命中率直接决定账单 -->
        <p v-if="cachePct > 0" class="usage-cache">
          {{ usage.savedByCache > 0
            ? t('aiUsageCacheSaved', { pct: cachePct, saved: fmtCost(usage.savedByCache) })
            : t('aiUsageCache', { pct: cachePct }) }}
        </p>

        <!-- 审批：闸门「设得太严」和「根本没生效」在界面上长得一模一样，
             只有这组数能区分。auto（超时/停止/断开）单列——它不代表用户拒绝，
             混进「拒绝」会让人误以为某个工具特别招人烦。 -->
        <div v-if="approvalTotal > 0" class="usage-sources">
          <span class="usage-approval-head">{{ t('aiApprovalStatTitle') }}</span>
          <span class="usage-source">{{ t('aiApprovalStatAllow', { n: approval?.allow ?? 0 }) }}</span>
          <span v-if="approval?.deny" class="usage-source">{{ t('aiApprovalStatDeny', { n: approval.deny }) }}</span>
          <span v-if="approval?.auto" class="usage-source">{{ t('aiApprovalStatAuto', { n: approval.auto }) }}</span>
          <!-- 只列被拒过的工具：批准多的恰恰说明闸门没问题，列出来只会把
               真正卡住的地方挤出视野。 -->
          <span v-for="s in topDenied" :key="s.tool" class="usage-source">
            <code>{{ s.tool }}</code> <b>{{ s.deny }}</b>
          </span>
        </div>

        <!-- 来源：SSH 面板 / 剪贴板 / 笔记 / 插件的一次性调用也计费，分开列才看得出钱花在哪 -->
        <div v-if="usage.sources.length > 1" class="usage-sources">
          <span v-for="src in usage.sources" :key="src.key || 'unknown'" class="usage-source">
            {{ sourceLabel(src.key) }} <b>{{ src.requests }}</b>
          </span>
        </div>

        <!-- 未收录模型的调用单独披露：否则「¥3.2」会被当成全部用量的价格 -->
        <p v-if="usage.unpricedRequests > 0" class="usage-note">
          {{ t('aiUsageUnpriced', { n: usage.unpricedRequests }) }}
        </p>

        <QdBarChart
          v-if="dailyChart.labels.length"
          class="usage-chart"
          :labels="dailyChart.labels"
          :series="dailyChartSeries"
          :height="120"
          :loading="usageLoading"
          :axis-name-y="dailyChart.name"
          :tooltip-extra="dailyTooltipExtra"
        />

        <table v-if="usage.models.length" class="usage-table">
          <thead>
            <tr>
              <th>{{ t('aiUsageModel') }}</th>
              <th class="num">{{ t('aiUsageRequests') }}</th>
              <th class="num">Tokens</th>
              <th class="num">{{ t('aiUsageCost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in usage.models" :key="m.key || 'unknown'">
              <td class="model">{{ m.key || t('aiUsageUnknownModel') }}</td>
              <td class="num">{{ m.requests }}</td>
              <td class="num">
                {{ fmtTokens(m.totalTokens) }}
                <span v-if="entryCachePct(m) > 0" class="usage-cell-sub">缓存 {{ entryCachePct(m) }}%</span>
              </td>
              <td class="num" :class="{ unknown: !m.priced }">
                {{ m.priced ? fmtCost(m.estimatedCost) : t('aiUsageNoPrice') }}
              </td>
            </tr>
          </tbody>
        </table>

        <p class="usage-hint">{{ t('aiUsageHint') }}</p>
      </template>
      <p v-else class="usage-hint">{{ t('aiUsageEmpty') }}</p>
    </div>
    </div>

    <div v-show="activeTab === 'permission'" class="ai-tab-panel" role="tabpanel">
    <!-- 工具审批策略：先列会改东西的工具，只读的在下面折叠起来（只展示、
         恒放行）。全过滤掉会让「git 相关的一个都没有」看起来像漏了。 -->
    <div class="ai-usage">
      <div class="ai-usage-head">
        <h4 class="ai-usage-title">{{ t('aiApprovalRulesTitle') }}</h4>
      </div>
      <p class="usage-hint">{{ t('aiApprovalRulesDesc') }}</p>

      <p v-if="apError" class="result-hint error">{{ apError }}</p>
      <p v-else-if="apLoading && !apRules.length" class="usage-hint">{{ t('aiUsageLoading') }}</p>
      <template v-else-if="apRulesEditable.length">
      <table class="usage-table approval-rules">
        <thead>
          <tr>
            <th>{{ t('aiApprovalRuleTool') }}</th>
            <th class="approval-rules-mode">{{ t('aiApprovalRulePolicy') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in apRulesEditable" :key="r.tool" :class="{ 'is-custom': r.custom }">
            <td class="model">
              <code>{{ r.tool }}</code>
              <span v-if="r.level >= 2" class="approval-risk-tag">{{ t('aiApprovalRuleRisk') }}</span>
              <span v-if="r.agentOnly" class="approval-risk-tag is-plat">{{ t('aiApprovalRuleAgentOnly') }}</span>
              <div class="usage-cell-sub">{{ r.desc }}</div>
            </td>
            <td class="approval-rules-mode">
              <select
                class="field-input approval-select"
                :value="r.mode"
                :disabled="apSaving === r.tool"
                @change="saveApprovalRule(r, ($event.target as HTMLSelectElement).value)"
              >
                <option value="auto">{{ t('aiApprovalModeAuto') }}</option>
                <option value="ask">{{ t('aiApprovalModeAsk') }}</option>
                <option value="deny">{{ t('aiApprovalModeDeny') }}</option>
              </select>
              <button
                v-if="r.custom"
                class="approval-reset"
                :title="t('aiApprovalRuleReset')"
                @click="saveApprovalRule(r, '')"
              >{{ t('aiApprovalRuleReset') }}</button>
            </td>
          </tr>
        </tbody>
      </table>

      <!-- 只读工具：恒放行，只展示。 -->
      <button class="approval-ro-toggle" @click="apShowReadOnly = !apShowReadOnly">
        <span class="approval-ro-arrow">{{ apShowReadOnly ? '▾' : '▸' }}</span>
        {{ t('aiApprovalRuleReadOnly', { n: apRulesReadOnly.length }) }}
      </button>
      <table v-if="apShowReadOnly" class="usage-table approval-rules is-readonly">
        <tbody>
          <tr v-for="r in apRulesReadOnly" :key="r.tool">
            <td class="model">
              <code>{{ r.tool }}</code>
              <span v-if="r.agentOnly" class="approval-risk-tag is-plat">{{ t('aiApprovalRuleAgentOnly') }}</span>
              <div class="usage-cell-sub">{{ r.desc }}</div>
            </td>
            <td class="approval-rules-mode">
              <span class="approval-ro-mode">{{ t('aiApprovalModeAuto') }}</span>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-if="apShowReadOnly" class="usage-hint">{{ t('aiApprovalRuleReadOnlyHint') }}</p>
      </template>
    </div>
    </div>

    <div v-show="activeTab === 'mcp'" class="ai-tab-panel" role="tabpanel">
    <!-- 外部 MCP 服务器（阶段 D）：把别的 server 的工具接进内置 AI。
         表单负责逐项增删改（最常做的事），JSON 两条通道并列放在本区块顶部：
         「编辑配置 JSON」改当前整份 mcp.json，「粘贴导入」把别人的片段合并进来。 -->
    <div class="ai-usage">
      <div class="ai-usage-head">
        <h4 class="ai-usage-title">{{ t('aiExtTitle') }}</h4>
        <!-- 整份 JSON 入口放在标题行：之前只有列表下方一个折叠的「粘贴导入」，
             想直接改 mcp.json 的人根本找不到地方输入。 -->
        <div class="ai-ext-head-ops">
          <button v-if="!extForm" class="ai-ext-add" @click="openEditJson">{{ t('aiExtJsonEdit') }}</button>
          <button v-if="!extForm" class="ai-ext-add" @click="extImportOpen = !extImportOpen; extEditOpen = false">
            {{ t('aiExtImport') }}
          </button>
          <button v-if="!extForm" class="ai-ext-add" @click="openAddExt">
            <Plus :size="12" /> {{ t('aiExtAdd') }}
          </button>
        </div>
      </div>
      <p class="usage-hint">{{ t('aiExtDesc') }}</p>

      <p v-if="extError" class="result-hint error">{{ extError }}</p>

      <!-- 整份配置 JSON：当前 mcp.json 原文，可直接写 headers / 令牌，保存即全量替换 -->
      <div v-if="!extForm && extEditOpen" class="ai-ext-import">
        <textarea
          v-model="extEditDraft"
          class="field-input ai-ext-json"
          spellcheck="false"
        ></textarea>
        <div class="ai-ext-actions">
          <button class="ai-hook-ok" :disabled="extSaving || !extEditDraft.trim()" @click="saveExtManifestJson">
            {{ t('aiExtJsonSaveDo') }}
          </button>
          <button class="ai-hook-no" @click="extEditOpen = false">{{ t('cancel') }}</button>
        </div>
        <span class="ai-ext-tip">{{ t('aiExtJsonHint') }}</span>
      </div>

      <!-- 粘贴导入：别人给的 mcp.json 片段整段贴进来（同名覆盖，其余保留） -->
      <div v-if="!extForm && extImportOpen" class="ai-ext-import">
        <textarea
          v-model="extImportDraft"
          class="field-input ai-ext-json"
          :placeholder="EXT_IMPORT_PH"
          spellcheck="false"
        ></textarea>
        <div class="ai-ext-actions">
          <button class="ai-hook-ok" :disabled="extSaving || !extImportDraft.trim()" @click="importExtJson">
            {{ t('aiExtImportDo') }}
          </button>
          <button class="ai-hook-no" @click="extImportOpen = false">{{ t('cancel') }}</button>
        </div>
        <span class="ai-ext-tip">{{ t('aiExtImportHint') }}</span>
      </div>

      <!-- 新增 / 编辑表单 -->
      <div v-if="extForm" class="ai-ext-form">
        <div class="ai-ext-grid">
          <label class="field">
            <span class="field-label">{{ t('aiExtName') }}</span>
            <input v-model="extForm.name" class="field-input" :placeholder="t('aiExtNamePh')" spellcheck="false" />
          </label>
          <div class="field">
            <span class="field-label">{{ t('aiExtKind') }}</span>
            <div class="ai-ext-seg">
              <button
                type="button"
                :class="{ active: extForm.kind === 'stdio' }"
                @click="extForm.kind = 'stdio'"
              >{{ t('aiExtKindStdio') }}</button>
              <button
                type="button"
                :class="{ active: extForm.kind === 'http' }"
                @click="extForm.kind = 'http'"
              >{{ t('aiExtKindHttp') }}</button>
            </div>
          </div>
        </div>

        <template v-if="extForm.kind === 'stdio'">
          <label class="field">
            <span class="field-label">{{ t('aiExtCommand') }}</span>
            <input v-model="extForm.command" class="field-input" :placeholder="t('aiExtCommandPh')" spellcheck="false" />
          </label>
          <label class="field">
            <span class="field-label">{{ t('aiExtArgs') }}</span>
            <textarea
              v-model="extForm.argsText"
              class="field-input ai-ext-ta"
              rows="3"
              :placeholder="EXT_ARGS_PH"
              spellcheck="false"
            ></textarea>
            <span class="ai-ext-tip">{{ t('aiExtArgsHint') }}</span>
          </label>
          <label class="field">
            <span class="field-label">{{ t('aiExtEnv') }}</span>
            <textarea
              v-model="extForm.envText"
              class="field-input ai-ext-ta"
              rows="2"
              :placeholder="t('aiExtEnvPh')"
              spellcheck="false"
            ></textarea>
            <span class="ai-ext-tip">{{ t('aiExtEnvHint') }}</span>
          </label>
        </template>
        <template v-else>
          <label class="field">
            <span class="field-label">{{ t('aiExtUrl') }}</span>
            <input v-model="extForm.url" class="field-input" :placeholder="t('aiExtUrlPh')" spellcheck="false" />
          </label>
          <div class="field">
            <span class="field-label">{{ t('aiExtToken') }}</span>
            <div class="ai-ext-secret">
              <input
                v-model="extForm.token"
                :type="extTokenVisible ? 'text' : 'password'"
                class="field-input"
                :placeholder="t('aiExtTokenPh')"
                spellcheck="false"
                autocomplete="off"
              />
              <button type="button" class="ai-hook-no" @click="extTokenVisible = !extTokenVisible">
                {{ extTokenVisible ? t('mcpTokenHide') : t('mcpTokenShow') }}
              </button>
            </div>
            <span class="ai-ext-tip">{{ t('aiExtTokenHint') }}</span>
          </div>
          <label class="field">
            <span class="field-label">{{ t('aiExtHeaders') }}</span>
            <textarea
              v-model="extForm.headersText"
              class="field-input ai-ext-ta"
              rows="2"
              :placeholder="EXT_HEADERS_PH"
              spellcheck="false"
            ></textarea>
            <span class="ai-ext-tip">{{ t('aiExtHeadersHint') }}</span>
          </label>
        </template>

        <div class="ai-ext-actions">
          <button class="ai-hook-ok" :disabled="extSaving" @click="submitExtForm">{{ t('aiExtSaveReload') }}</button>
          <button class="ai-hook-no" :disabled="extSaving" @click="extForm = null">{{ t('cancel') }}</button>
        </div>
      </div>

      <!-- 已接入列表 -->
      <template v-else>
        <p v-if="extLoading && !extServers.length" class="usage-hint">{{ t('aiUsageLoading') }}</p>
        <p v-else-if="!extServers.length" class="usage-hint">{{ t('aiExtEmpty') }}</p>
        <div v-else class="ai-ext-list">
          <div v-for="s in extServers" :key="s.name" class="ai-ext-item">
            <div class="ai-ext-item-main">
              <div class="ai-ext-item-head">
                <code class="ai-ext-name">{{ s.name }}</code>
                <span class="ai-ext-kind-tag">{{ kindOf(s) === 'http' ? 'HTTP' : 'stdio' }}</span>
                <span :class="['ext-status', s.status]">
                  {{ s.status === 'ok' ? t('aiExtStatusOk', { n: s.toolCount }) : s.status === 'disabled' ? t('aiExtStatusOff') : t('aiExtStatusErr') }}
                </span>
              </div>
              <div class="ai-ext-cmd" :title="extTarget(s)">{{ extTarget(s) }}</div>
              <div v-if="s.error" class="usage-cell-sub ai-ext-err">{{ s.error }}</div>
              <div v-else-if="s.tools.length" class="usage-cell-sub">{{ s.tools.join(' · ') }}</div>

              <!-- 工具详情：名字清单上面已有，这里补描述与逐条开关 -->
              <div v-if="extExpanded === s.name" class="ai-ext-tools">
                <p v-if="extToolsLoading" class="usage-cell-sub">{{ t('aiExtToolsLoading') }}</p>
                <p v-else-if="!extTools.length" class="usage-cell-sub">{{ t('aiExtToolsEmpty') }}</p>
                <div v-else class="ai-ext-tools-list">
                  <div v-for="tool in extTools" :key="tool.name" class="ai-ext-tool">
                    <div class="ai-ext-tool-main">
                      <code class="ai-ext-tool-name">{{ tool.name }}</code>
                      <span v-if="tool.disabled" class="ai-ext-tool-off">{{ t('aiExtToolDisabled') }}</span>
                      <span class="ai-ext-tool-desc">{{ tool.desc }}</span>
                    </div>
                    <button
                      class="toggle-btn-sm"
                      :class="{ active: !tool.disabled }"
                      :disabled="extToolsBusy === tool.name"
                      :title="tool.disabled ? t('aiExtToolEnable') : t('aiExtToolDisable')"
                      @click="toggleExtTool(tool)"
                    >
                      <span class="toggle-knob"></span>
                    </button>
                  </div>
                </div>
              </div>
            </div>
            <div class="ai-ext-item-ops">
              <button
                class="ai-hook-no ai-ext-expand"
                :class="{ 'is-open': extExpanded === s.name }"
                :disabled="extSaving || s.status === 'disabled'"
                :title="extExpanded === s.name ? t('aiExtToolsHide') : t('aiExtToolsShow')"
                @click="toggleExtTools(s)"
              >
                <ChevronDown :size="12" />
              </button>
              <button
                class="toggle-btn-sm"
                :class="{ active: s.status !== 'disabled' }"
                :disabled="extSaving"
                :title="s.status === 'disabled' ? t('aiExtEnable') : t('aiExtDisable')"
                @click="toggleExt(s)"
              >
                <span class="toggle-knob"></span>
              </button>
              <button class="ai-hook-no" :disabled="extSaving" @click="openEditExt(s)">{{ t('aiExtEdit') }}</button>
              <button class="ai-hook-no is-danger" :disabled="extSaving" @click="removeExt(s)">{{ t('delete') }}</button>
            </div>
          </div>
        </div>

        <div class="ai-ext-actions">
          <button class="ai-hook-no" :disabled="extLoading" @click="reloadExt">
            <RefreshCw :size="12" /> {{ t('aiExtReload') }}
          </button>
        </div>
      </template>
    </div>
    </div>

    <!-- AI 配置编辑模态框 -->
    <Teleport to="body">
      <div v-if="aiEditDraft" class="ai-modal-overlay" @mousedown.self="closeAIEditor">
        <div class="ai-modal">
          <div class="ai-modal-header">
            <h3>{{ t('aiEditProfile') }}</h3>
            <button class="ai-modal-close" @click="closeAIEditor">&times;</button>
          </div>
          <div class="ai-modal-body">
            <label class="field">
              <span class="field-label">{{ t('aiProfileName') }}</span>
              <input v-model="aiEditDraft.name" type="text" class="field-input" :placeholder="t('aiProfileNamePh')" />
            </label>
            <label class="field">
              <span class="field-label">{{ t('aiProvider') }}</span>
              <select v-model="aiEditDraft.provider" class="field-input" @change="onAIProviderChangeDraft">
                <option v-for="p in aiProviders" :key="p.value" :value="p.value">{{ p.label }}</option>
              </select>
            </label>
            <label class="field">
              <span class="field-label">{{ t('aiBaseURL') }}</span>
              <input v-model="aiEditDraft.baseURL" type="text" class="field-input" placeholder="https://api.openai.com/v1" />
            </label>
            <label class="field">
              <span class="field-label">{{ t('aiAPIKey') }}</span>
              <input
                v-model="aiEditDraft.apiKey"
                type="password"
                class="field-input"
                :placeholder="aiEditDraft.provider === 'ollama' ? t('aiAPIKeyOptional') : 'sk-...'"
              />
            </label>
            <label class="field">
              <span class="field-label">{{ t('aiModel') }}</span>
              <!-- 输入框 + 显式下拉。原生 datalist 必须聚焦后输入才过滤，
                   WebView2 上体验不可靠，故自己做一个「点箭头即出全部」的列表。 -->
              <div class="model-input-wrap">
                <input
                  v-model="aiEditDraft.model"
                  type="text"
                  class="field-input"
                  :placeholder="aiEditDraft.provider === 'ollama' ? t('aiModelPlaceholderOllama') : 'gpt-4o-mini / deepseek-chat'"
                  @focus="onModelFocus"
                />
                <button
                  v-if="modelOptions.length"
                  type="button"
                  class="model-caret"
                  :title="t('aiFetchModels')"
                  @click.prevent="modelOptionsOpen = !modelOptionsOpen"
                >▾</button>
                <div v-if="modelOptionsOpen && modelOptions.length" class="model-dropdown">
                  <div
                    v-for="m in filteredModelOptions"
                    :key="m"
                    class="model-option"
                    :class="{ on: aiEditDraft.model === m }"
                    @mousedown.prevent="pickModel(m)"
                  >{{ m }}</div>
                  <div v-if="!filteredModelOptions.length" class="model-option-empty">
                    {{ t('aiNoModelsFound') }}
                  </div>
                </div>
              </div>
              <!-- 非 Ollama：从兼容接口拉取模型列表。Ollama 已在编辑时自动拉本机模型，无需此按钮。 -->
              <div v-if="aiEditDraft.provider !== 'ollama'" class="model-fetch-row">
                <button
                  type="button"
                  class="model-fetch-btn"
                  :disabled="modelOptionsLoading"
                  @click.prevent="fetchApiModels"
                >
                  <RefreshCw :size="12" :class="{ spinning: modelOptionsLoading }" />
                  {{ modelOptionsLoading ? t('aiFetchingModels') : t('aiFetchModels') }}
                </button>
                <span v-if="modelOptionsError" class="model-fetch-msg error">{{ modelOptionsError }}</span>
                <span v-else-if="modelOptions.length" class="model-fetch-msg">
                  {{ t('aiModelsFetched', { n: modelOptions.length }) }}
                </span>
              </div>
            </label>
            <!-- Ollama：chip 快捷选择（与下拉等价，鼠标不用移来移去） -->
            <div v-if="aiEditDraft.provider === 'ollama'" class="ollama-model-picks">
              <template v-if="modelOptions.length">
                <span class="picks-label">{{ t('aiOllamaLocalModels') }}</span>
                <button
                  v-for="m in modelOptions"
                  :key="m"
                  type="button"
                  class="pick-chip"
                  :class="{ on: aiEditDraft.model === m }"
                  @click="aiEditDraft.model = m"
                >{{ m }}</button>
              </template>
              <span v-else-if="modelOptionsLoading" class="picks-hint">{{ t('aiOllamaLoading') }}</span>
              <span v-else class="picks-hint">{{ t('aiOllamaNoModels') }}</span>
            </div>
            <div class="field-row">
              <label class="field field-half">
                <span class="field-label">{{ t('aiTemperature') }}</span>
                <input v-model.number="aiEditDraft.temperature" type="number" min="0" max="2" step="0.1" class="num-input" />
              </label>
              <label class="field field-half">
                <span class="field-label">{{ t('aiMaxTokens') }}</span>
                <input v-model.number="aiEditDraft.maxTokens" type="number" min="0" max="131072" step="1" class="num-input" placeholder="0" />
              </label>
            </div>
            <label class="field field-textarea">
              <span class="field-label">{{ t('aiSystemPrompt') }}</span>
              <textarea v-model="aiEditDraft.systemPrompt" class="field-input" rows="3" :placeholder="t('aiSystemPromptPh')"></textarea>
            </label>
            <label class="field">
              <span class="field-label">{{ t('aiImageInput') }}</span>
              <select v-model="aiEditDraft.imageInput" class="field-input">
                <option value="both">{{ t('aiImageInputBoth') }}</option>
                <option value="url">{{ t('aiImageInputURL') }}</option>
                <option value="base64">{{ t('aiImageInputBase64') }}</option>
              </select>
            </label>
            <p class="ai-field-hint">{{ t('aiImageInputHint') }}</p>
            <label class="field">
              <span class="field-label">{{ t('aiContextWindow') }}</span>
              <input
                v-model.number="aiEditDraft.contextWindow"
                type="number"
                min="0"
                max="2000000"
                step="1024"
                class="num-input"
                placeholder="0"
              />
            </label>
            <p class="ai-field-hint">{{ t('aiContextWindowHint') }}</p>
            <div class="field-row">
              <label class="field field-half">
                <span class="field-label">top_p</span>
                <input v-model.number="aiEditDraft.topP" type="number" min="0" max="1" step="0.05" class="num-input" placeholder="0" />
              </label>
              <label class="field field-half">
                <span class="field-label">frequency_penalty</span>
                <input v-model.number="aiEditDraft.frequencyPenalty" type="number" min="-2" max="2" step="0.1" class="num-input" placeholder="0" />
              </label>
            </div>
            <div class="field-row">
              <label class="field field-half">
                <span class="field-label">presence_penalty</span>
                <input v-model.number="aiEditDraft.presencePenalty" type="number" min="-2" max="2" step="0.1" class="num-input" placeholder="0" />
              </label>
              <label class="field field-half toggle-field">
                <span class="field-label">思考模式 (thinking)</span>
                <button :class="['toggle-btn-sm', { active: aiEditDraft.thinkingEnabled }]" @click="aiEditDraft.thinkingEnabled = !aiEditDraft.thinkingEnabled">
                  <span class="toggle-knob" />
                </button>
              </label>
            </div>
          </div>
          <div class="ai-modal-footer">
            <button class="btn btn-secondary" @click="closeAIEditor">{{ t('cancel') }}</button>
            <button class="btn btn-primary" @click="saveAIModal">{{ t('save') }}</button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- 技能：skills/ 目录的 SKILL.md 操作手册，与提示词库（ai_prompts 表）不是一套。
         visible 同 SettingsPrompts，只在切到本分段时触发加载；切走再切回要重拉，
         因为技能目录可能刚被用户在资源管理器里改过。 -->
    <div v-show="activeTab === 'skills'" class="ai-tab-panel" role="tabpanel">
      <SettingsSkills :visible="visible && activeTab === 'skills'" />
    </div>

    <!-- 提示词库：与技能（skills/ 目录）不是一套，见 SettingsPrompts 文件头。
         visible 只有切到本分段才为 true：它靠这个 prop 的 false→true 触发加载，
         常驻为 true 会在打开设置时白白拉一次、切走时也不会停。 -->
    <div v-show="activeTab === 'prompts'" class="ai-tab-panel" role="tabpanel">
      <SettingsPrompts :visible="visible && activeTab === 'prompts'" />
    </div>
  </div>
</template>

<style scoped>
.ai-field-hint {
  margin: 2px 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-muted);
}

/* 外部 MCP 工具详情：展开区挂在 ai-ext-item-main 里，随行宽收缩 */
.ai-ext-expand {
  display: inline-flex;
  align-items: center;
  padding: 4px;
}
.ai-ext-expand svg {
  transition: transform 0.15s ease;
}
.ai-ext-expand.is-open svg {
  transform: rotate(180deg);
}
.ai-ext-tools {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
}
.ai-ext-tools-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 220px;
  overflow-y: auto;
}
.ai-ext-tool {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 6px 8px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}
.ai-ext-tool-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.ai-ext-tool-name {
  font-size: 11px;
  color: var(--color-text-primary);
  word-break: break-all;
}
.ai-ext-tool-off {
  font-size: 10px;
  color: var(--color-warning);
}
.ai-ext-tool-desc {
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-muted);
  word-break: break-word;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

/* 页内分段：按钮走公共类 qd-btn/qd-btn-sm/is-on，这里只排布一行 */
.ai-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  margin-bottom: var(--space-6);
  padding-bottom: var(--space-5);
  border-bottom: 1px solid var(--color-border);
}
.ai-tab-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 16px;
  height: 16px;
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
  background: var(--color-danger-bg);
  color: var(--color-danger);
  font-size: 11px;
  line-height: 1;
}

/* 单条费用开关：插在用量面板标题行下方，补一点上间距免得贴着标题。
   只写在本 SFC 的 scoped 块里，不改全局 settings.css。 */
.ai-cost-pref { margin-top: var(--space-4); }
</style>
