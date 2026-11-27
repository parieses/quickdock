<script setup lang="ts">
import { ref, computed, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pen, Trash2, Plus, RefreshCw, ChevronDown, ChevronRight, Copy, Eye, EyeOff } from '@lucide/vue'
import SettingsPrompts from './SettingsPrompts.vue'
import SettingsSkills from './SettingsSkills.vue'
import SettingsMCP from './SettingsMCP.vue'
import AiToolSwitchList from './AiToolSwitchList.vue'
import QdBarChart from './charts/QdBarChart.vue'
import QdTabs from './QdTabs.vue'
import QdSwitch from './QdSwitch.vue'
import QdModal from './QdModal.vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { profileNeedsKey } from '../utils/aiKey'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { fmtCost, fmtTokens } from '../utils/format'
import { logErr } from '../utils/logger'
import { localISO } from '../utils/lunar'
import { useAiCostPreference } from '../composables/useAiCostPreference'

import {
  AIApprovalStats,
  AIFetchModels,
  AIGetMemoryAuto,
  AIListApprovalRules,
  AIListApprovalGrants,
  AIDeleteApprovalGrant,
  AIListProfiles,
  AIRevealProfileKey,
  AISaveProfiles,
  AISetApprovalRule,
  AISetMemoryAuto,
  AITestConnection,
  AIUsageStats,
} from '../../bindings/quickdock/services/ai/aiservice'
import { EnvOllamaModels } from '../../bindings/quickdock/services/env/environmentservice'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type {
  AIApprovalRuleView,
  AIGrantView,
  AIApprovalStatsResult,
  AICostEntry,
  AIProfilesResult,
  AIUsageStatsResult,
} from '../types/ai'
import type { ToastAPI } from '../types'

import { reportError } from '../utils/reportError'
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
const aiTabs = computed<{ id: AiSettingsTab; label: string }[]>(() => [
  { id: 'model', label: t('aiTabModel') },
  { id: 'usage', label: t('aiTabUsage') },
  { id: 'permission', label: t('aiTabPermission') },
  { id: 'mcp', label: t('aiTabMcp') },
  { id: 'skills', label: t('aiTabSkills') },
  { id: 'prompts', label: t('aiTabPrompts') },
])

const aiPresets: Record<string, string> = {
  openai: 'https://api.openai.com/v1',
  deepseek: 'https://api.deepseek.com/v1',
  kimi: 'https://api.moonshot.cn/v1',
  qwen: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
  ollama: 'http://localhost:11434/v1',
  azure: 'https://{resource}.openai.azure.com',
  custom: '',
}
// 厂商名里只有「本地」「自定义」是需要翻译的描述，纯品牌名保留原文。
// 用 computed 而不是常量：切换语言时下拉选项要跟着变。
const aiProviders = computed(() => [
  { value: 'openai', label: 'OpenAI' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'kimi', label: 'Kimi (Moonshot)' },
  { value: 'qwen', label: t('aiProviderQwen') },
  { value: 'ollama', label: t('aiProviderOllama') },
  { value: 'azure', label: 'Azure OpenAI' },
  { value: 'custom', label: t('aiProviderCustom') },
])
const aiProfiles = ref<AIProfile[]>([])
const aiActive = ref('')
/**
 * 一次性问答（SSH / 剪贴板 / 笔记 / 插件）单独使用的档案 id。
 *
 * 空串 = 跟随上面的对话模型。以前这两类共用一个全局档案，想给面板换成
 * 快模型就得连聊天一起换；分开之后「聊天用强模型、面板问答用快模型」才成立。
 */
const aiAskActive = ref('')
const aiMsg = ref('')
const aiMsgError = ref(false)
const aiTesting = ref(false)
const aiEditDraft = ref<AIProfile | null>(null)

// ---- API Key 的查看 / 复制 ----
// 列表与配置接口给的一律是掩码（sk-1***abcd），所以「看一眼」和「复制一份」
// 都不能靠输入框里那串字符——必须向 Go 侧要一次明文。只在用户显式点按钮时取，
// 取到的明文只活在这一个编辑草稿里，关窗即丢（见 closeAIEditor）。
const keyVisible = ref(false)
const keyPlain = ref('')
const keyBusy = ref(false)
let keyMasked = ''

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
/** 这个模型是否已在这条档案的启用列表里。 */
function isModelEnabled(id: string): boolean {
  const d = aiEditDraft.value
  if (!d) return false
  return (d.models || []).some((m) => m.id === id)
}

/**
 * 勾选 / 取消勾选一个模型。
 *
 * 只存 id：窗口与输出上限留空 = 自动（按模型名查能力表）。让用户手填 384K
 * 这类值既没依据，也容易把输入空间挤掉——这是上一轮刚统一的口径。
 */
function toggleModel(id: string, ev: Event) {
  const d = aiEditDraft.value
  if (!d) return
  const on = (ev.target as HTMLInputElement).checked
  const list = [...(d.models || [])]
  if (on) {
    if (!list.some((m) => m.id === id)) list.push({ id } as (typeof list)[number])
    if (!d.modelId) d.modelId = id
  } else {
    const i = list.findIndex((m) => m.id === id)
    if (i >= 0) list.splice(i, 1)
    if (d.modelId === id) d.modelId = list[0]?.id || ''
  }
  d.models = list
}
async function fetchApiModels() {
  const d = aiEditDraft.value
  if (!d || modelOptionsLoading.value) return
  const needKey = profileNeedsKey(d)
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
    // 多模型档案（方案 A）：新档案先空着，勾选「拉取」到的模型后才填。
    modelId: '',
    models: [],
    name: '',
    provider,
    baseURL: aiPresets[provider] || '',
    apiKey: '',
    model: 'gpt-4o-mini',
    temperature: 0.7,
    maxTokens: 0, // 0 = 按模型自动（见 services/ai/ai_model_caps.go）
    systemPrompt: '',
    topP: 0,
    frequencyPenalty: 0,
    presencePenalty: 0,
    thinkingEnabled: true,
    imageInput: 'both',
    // 0 = 自动（按模型名查内置表 + 撞了 400 后按上游报的真实值校正）。
    contextWindow: 0,
    // WP11：空 = 起标题 / 摘要也用主模型。
    cheapModel: '',
    // WP4c 压缩阈值：0 = 默认值（占位符显示的就是默认值）。
    contextSoftCap: 0,
    preflightMargin: 0,
    keepRecentMin: 0,
    keepRecentMax: 0,
  }
}

// 默认值只用于输入框占位提示，真正的默认在后端（`softCapOf` 等）。
// 两处写死会漂移，所以这里只在注释里说明「与后端常量一致」，不参与计算。
const aiSoftCapDefault = 120000
const aiPreflightMarginDefault = 85
const aiKeepRecentMinDefault = 8
const aiKeepRecentMaxDefault = 40

async function loadAIProfiles() {
  try {
    const res = unwrap<AIProfilesResult>(await AIListProfiles())
    if (!res) return
    aiProfiles.value = res.profiles ?? []
    aiActive.value = res.active || (aiProfiles.value[0]?.id ?? '')
    // 后端已把「指向已删档案」的脏值过滤成空串，这里直接跟着走。
    aiAskActive.value = res.askActive || ''
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
    await loadGrants()
  } catch (e) {
    apError.value = getErrorMessage(e)
  } finally {
    apLoading.value = false
  }
}

// 沉淀的放行规则（WP6）：跨会话生效，所以必须有一个看得见、点得动的撤销入口。
// 单独一张表而不是塞进上面那行：上面是「每个工具的默认策略」，
// 这里是「用户点过的具体放行」，两者的粒度与生命周期都不同。
const grants = ref<AIGrantView[]>([])

async function loadGrants() {
  try {
    grants.value = unwrap<AIGrantView[]>(await AIListApprovalGrants()) ?? []
  } catch (e) {
    // 不与主列表共用错误位：它失败了不该把整个审批页判成加载失败。
    logErr('settings:listGrants', e)
  }
}

function grantScopeText(g: AIGrantView): string {
  if (g.scope === 'global') return t('aiGrantScopeGlobal')
  return t('aiGrantScopeWorkspace', { dir: g.workspace })
}

async function revokeGrant(g: AIGrantView) {
  try {
    unwrap(await AIDeleteApprovalGrant(g.tool, g.kind, g.value, g.scope, g.workspace))
    await loadGrants()
  } catch (e) {
    reportError(e, 'settings:revokeGrant')
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

// MCP 分段的失败数（由 SettingsMCP 回传）：挂在分段标题的红点上。
// server 启动只连一次、失败不自动重试，用户往往就是看到这个数才进来重连。
const mcpErrorCount = ref(0)

// 日期轴只写 M/D 才排得下，完整日期由 dailyTooltipTitle 放 tooltip 首行。
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
// 三条口径都是踩过的坑：
//  1) 整窗一律按 token 画。旧判据「窗口里有一天有价就按金额画」会让 Ollama /
//     未收录模型那些 estimatedCost=0 但跑了几万 token 的日子变成 0 柱；
//     费用受单价影响，和 token 混在同一根轴上也没有可比性。金额仍在总量和 tooltip 里。
//  2) 后端 daily 只含有调用记录的日期。按自然日补零，否则「近 7 天」可能只有两根柱、
//     横轴还跳日；推进用本地日历构造 Date，跨月/跨年由 Date 自己处理。
//  3) 「全部」不铺上百天：以最后一次调用日为终点只画最近 30 个自然日，
//     半年没用时仍看得到当时那段的分布，而不是一片全 0 的近 30 天。
const DAILY_CHART_MAX_DAYS = 30
function dayShift(base: Date, delta: number): Date {
  return new Date(base.getFullYear(), base.getMonth(), base.getDate() + delta)
}
function parseDayKey(key: string): Date {
  const [y, m, d] = key.split('-').map(Number)
  return new Date(y, (m || 1) - 1, d || 1)
}
const EMPTY_DAY: AICostEntry = {
  key: '', requests: 0, promptTokens: 0, completionTokens: 0, cachedTokens: 0,
  totalTokens: 0, estimatedCost: 0, savedByCache: 0, priced: false,
}
const dailyChart = computed(() => {
  if (!usage.value) {
    return {
      labels: [] as string[], values: [] as number[], items: [] as AICostEntry[],
      name: '', color: 'accent' as const,
    }
  }
  const all = usage.value.daily ?? []
  const byDay = new Map(all.map((d) => [d.key, d]))
  const today = localISO(new Date())
  const lastKey = all.length ? all[all.length - 1].key : today
  // 后端按 since 过滤，正常不会有未来日期；仍夹一下，免得脏数据把终点推到未来。
  const endKey = usageDays.value > 0 ? today : (lastKey > today ? today : lastKey)
  const span = usageDays.value > 0 ? usageDays.value : DAILY_CHART_MAX_DAYS
  const end = parseDayKey(endKey)
  const items: AICostEntry[] = []
  for (let i = span - 1; i >= 0; i--) {
    const key = localISO(dayShift(end, -i))
    items.push(byDay.get(key) ?? { ...EMPTY_DAY, key })
  }
  return {
    labels: items.map((d) => fmtDay(d.key)),
    values: items.map((d) => d.totalTokens),
    items,
    name: t('chartLegendTokens'),
    color: 'accent' as const,
  }
})
const dailyChartSeries = computed(() => dailyChart.value.labels.length
  ? [{ name: dailyChart.value.name, values: dailyChart.value.values, color: dailyChart.value.color }]
  : [])
function dailyTooltipTitle(index: number): string {
  return dailyChart.value.items[index]?.key ?? ''
}
function dailyTooltipExtra(index: number): string {
  const d = dailyChart.value.items[index]
  if (!d) return ''
  // 主系列行已是「Tokens: 50k」，这里不再重复写一遍单位。
  return `<div>${t('chartTooltipRequests')}: ${d.requests}</div>`
    + `<div>${t('chartTooltipCost')}: ${fmtCost(d.estimatedCost)}</div>`
}

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
    reportError(e, 'SettingsAI.onToggleMemoryAuto')
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
  // 被删的正好是「一次性问答」指定的那个：回落到「跟随对话模型」，
  // 否则选择框里会留一个已经不存在的 id（后端虽然会兜底，但那要等下次保存）。
  if (aiAskActive.value === id) aiAskActive.value = ''
}
function onAIProviderChange() {
  const cur = aiCurrent.value
  if (!cur) return
  const url = aiPresets[cur.provider]
  if (url) cur.baseURL = url
}
async function saveAIProfiles() {
  try {
    // 数字字段先归一成整数再提交。
    //
    // 为什么必须做：输入框是 `v-model.number` + `type="number"`，用户按提示
    // **清空**时拿到的是 ''（Vue 的 looseToNumber 在 NaN 时保留原值），而 Go 侧
    // 字段是 int——JSON 反序列化直接失败，**整个档案**（含 API Key、模型名）
    // 都存不进去，界面只提示「保存失败」，用户根本定位不到是哪个框。
    // 语义与后端一致：<=0 一律当「用默认值」（见 ai_config.go 的 clampInt）。
    const asInt = (v: unknown): number => {
      const n = Number(v)
      if (!Number.isFinite(n) || n <= 0) return 0
      return Math.round(n)
    }
    const profiles = aiProfiles.value.map((p) => ({
      ...p,
      cheapModel: String(p.cheapModel || ''),
      contextSoftCap: asInt(p.contextSoftCap),
      preflightMargin: asInt(p.preflightMargin),
      keepRecentMin: asInt(p.keepRecentMin),
      keepRecentMax: asInt(p.keepRecentMax),
    }))
    unwrap(await AISaveProfiles({ active: aiActive.value, askActive: aiAskActive.value, profiles }))
    Events.Emit('ai:profiles-updated')
    showAIMsg(t('saved'))
    return true
  } catch (e) {
    showAIMsg(t('saveFailed2') + ': ' + getErrorMessage(e), true)
    return false
  }
}
async function testAIConnection() {
  const cur = aiCurrent.value
  if (!cur) return
  // 本地推理服务（Ollama / LM Studio / vLLM…）无需 API Key，只校 Base URL 与 Model。
  const needKey = profileNeedsKey(cur)
  if (!cur.baseURL || !cur.model || (needKey && !cur.apiKey)) {
    showAIMsg(t(needKey ? 'aiFillRequired' : 'aiFillRequiredNoKey'), true); return
  }
  await saveAIProfiles()
  aiTesting.value = true; showAIMsg(t('aiTesting'))
  try {
    const res = unwrap<{ success?: boolean; message?: string }>(await AITestConnection(cur.id))
    if (!res) throw new Error(t('aiNoResponse'))
    showAIMsg(res.message || t('aiUnknown'), res.success !== true)
  } catch (e: any) {
    showAIMsg(t('testFailed') + ': ' + (e?.message || String(e)), true)
  } finally { aiTesting.value = false }
}

/**
 * 拿「要给人看 / 要复制」的那份 Key。
 *
 * 三种情况分开处理，判断顺序不能换：
 *   1. 用户在本次编辑里已经敲过新 Key（不含 ***）：还没落库，输入框里那份就是最新的；
 *   2. 之前已经取过一次明文：直接复用，别为点眼睛 / 点复制各解一次密；
 *   3. 剩下才是「回填的掩码」——找后端要一次。
 * 取不到（解密失败 / 没填）返回空串，由调用方给提示，不在这里静默当成功。
 */
async function ensurePlainKey(): Promise<string> {
  const d = aiEditDraft.value
  if (!d) return ''
  if (d.apiKey && !d.apiKey.includes('***')) return d.apiKey
  if (keyPlain.value) return keyPlain.value
  if (keyBusy.value) return ''
  keyBusy.value = true
  try {
    const res = unwrap<{ apiKey?: string }>(await AIRevealProfileKey(d.id))
    keyPlain.value = res?.apiKey || ''
    return keyPlain.value
  } catch (e) {
    reportError(e, 'SettingsAI.ensurePlainKey', { fallback: t('aiKeyRevealFailed') })
    return ''
  } finally {
    keyBusy.value = false
  }
}

/** 眼睛：显示明文 / 还原掩码。隐藏时一定要把掩码写回去，否则明文字符串会一直留在输入框里。 */
async function toggleKeyVisible() {
  const d = aiEditDraft.value
  if (!d) return
  if (keyVisible.value) {
    keyVisible.value = false
    // 只有「还是刚取出来那份明文」时才还原掩码：用户若在显示状态下改了内容，
    // 那是他要保存的新 Key，藏起来不能把它一起吞掉。
    if (keyMasked && d.apiKey === keyPlain.value) d.apiKey = keyMasked
    return
  }
  const plain = await ensurePlainKey()
  if (!plain) return
  keyVisible.value = true
  d.apiKey = plain
}

/** 复制：不要求先点眼睛，隐藏状态下也能把这个 Key 复制走。 */
async function copyKey() {
  const d = aiEditDraft.value
  if (!d) return
  const text = await ensurePlainKey()
  if (!text) {
    toast.error(t('aiKeyEmpty'))
    return
  }
  if (await copyToClipboard(text)) toast.success(t('aiKeyCopied'))
  else toast.error(t('copyFailed'))
}

function openAIEditor() {
  const cur = aiCurrent.value
  if (!cur) return
  aiEditDraft.value = { ...cur }
  // 记住回填时那份掩码：点「隐藏」要还原成它，而不是把明文留在输入框里。
  keyMasked = cur.apiKey || ''
  keyVisible.value = false
  keyPlain.value = ''
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
function closeAIEditor() {
  aiEditDraft.value = null
  resetModelOptions()
  // 明文凭据不跟着弹窗一起留在内存里：关掉就把这份副本丢掉。
  keyPlain.value = ''
  keyVisible.value = false
}
async function saveAIModal() {
  const draft = aiEditDraft.value
  if (!draft) return
  const idx = aiProfiles.value.findIndex((p) => p.id === draft.id)
  // 先替换本地再落库，失败必须回滚：否则列表里显示的是没存进去的值，
  // 下次保存别的档案会把这份失败内容一起写进库。
  const prev = idx >= 0 ? aiProfiles.value[idx] : null
  if (idx >= 0) aiProfiles.value[idx] = { ...draft }
  aiEditDraft.value = null
  if (!await saveAIProfiles()) {
    if (prev) aiProfiles.value[idx] = prev
    aiEditDraft.value = draft // 失败把表单还回去，别让用户重填
  }
}

// 必须放在所有 ref / 函数声明之后：immediate watch 在 setup 期同步执行，
// 提前声明会先于 memoryAuto 等 const 初始化触发暂时性死区（浮窗问 AI 未配 Key 时自动弹本页必崩）。
watch(() => props.visible, (v) => {
  if (!v) return
  loadAIProfiles()
  loadUsage()
  loadApprovalRules()
  loadMemoryAuto()
  // 外部 MCP server 的加载搬到 SettingsMCP 内部：它的失败数要显示在分段红点上，
  // 所以由自己接 visible 触发，这里不再代劳。
}, { immediate: true })
</script>

<template>
  <div class="section">
    <h3 class="section-title">{{ t('navAi') }}</h3>
    <p class="section-desc">{{ t('aiSettingsDesc') }}</p>

    <div class="ai-tabs">
      <QdTabs v-model="activeTab" :tabs="aiTabs">
        <template #tab="{ tab }">
          <span>{{ tab.label }}</span>
          <span
            v-if="tab.id === 'mcp' && mcpErrorCount"
            class="ai-tab-badge"
            :title="t('aiExtStatusErr')"
          >{{ mcpErrorCount }}</span>
        </template>
      </QdTabs>
    </div>

    <div v-show="activeTab === 'model'" class="ai-tab-panel" role="tabpanel">
    <div class="ai-profiles">
      <div
        v-for="p in aiProfiles"
        :key="p.id"
        :class="['ai-profile-item', { active: p.id === aiActive }]"
      >
        <div v-key-click class="ai-profile-info" @click="selectAIProfile(p.id)">
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

    <!-- 一次性问答的模型：SSH / 剪贴板 / 笔记面板和插件 host.ai.ask 走这条。
         它们的诉求常与聊天不同（要快、要便宜、不要长思考），所以允许单独指定。
         改完立即保存，不必再回去点「保存」。 -->
    <div class="ai-ask-profile">
      <div class="ai-ask-profile-text">
        <span class="ai-ask-profile-label">{{ t('aiAskProfileLabel') }}</span>
        <span class="ai-ask-profile-hint">{{ t('aiAskProfileHint') }}</span>
      </div>
      <select
        v-model="aiAskActive"
        class="ai-ask-profile-select"
        :aria-label="t('aiAskProfileLabel')"
        @change="saveAIProfiles"
      >
        <option value="">{{ t('aiAskProfileFollow') }}</option>
        <option v-for="p in aiProfiles" :key="p.id" :value="p.id">
          {{ p.name || p.model || p.id }}
        </option>
      </select>
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
          :value-formatter="fmtTokens"
          :tooltip-title="dailyTooltipTitle"
          :tooltip-extra="dailyTooltipExtra"
          zero-as-empty
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
                <span v-if="entryCachePct(m) > 0" class="usage-cell-sub">{{ t('aiCacheHit', { pct: entryCachePct(m) }) }}</span>
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
    <!-- AI 可用工具：域级总开关 + 单工具 + 等级上限。放在审批策略上面，因为
         「能不能用」在「用了要不要问」之前——先关掉工具，就不用再给它配审批规则了。 -->
    <AiToolSwitchList />

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
        <span class="approval-ro-arrow">
          <ChevronDown v-if="apShowReadOnly" :size="12" />
          <ChevronRight v-else :size="12" />
        </span>
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

      <!-- 沉淀的放行规则（WP6）：「以后都允许」点过的东西落在这里。
           没有撤销入口的话，一次手滑就是永久放行一类操作——所以这块不能只做记录。 -->
      <div class="approval-grants">
        <h4 class="ai-usage-title">{{ t('aiGrantTitle', { n: grants.length }) }}</h4>
        <p v-if="!grants.length" class="usage-hint">{{ t('aiGrantEmpty') }}</p>
        <table v-else class="usage-table approval-rules">
          <tbody>
            <tr v-for="g in grants" :key="g.tool + '|' + g.kind + '|' + g.value + '|' + g.scope + '|' + g.workspace">
              <td class="model">
                <code>{{ g.tool }}</code>
                <div class="usage-cell-sub">{{ g.note }}</div>
                <div class="usage-cell-sub">{{ grantScopeText(g) }}</div>
              </td>
              <td class="approval-rules-mode">
                <button class="btn btn-secondary" @click="revokeGrant(g)">{{ t('aiGrantRevoke') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="grants.length" class="usage-hint">{{ t('aiGrantHint') }}</p>
      </div>
    </div>
    </div>

    <div v-show="activeTab === 'mcp'" class="ai-tab-panel" role="tabpanel">
      <SettingsMCP v-model:error-count="mcpErrorCount" :visible="visible" />
    </div>

    <!-- AI 配置编辑模态框。v-if 驱动而非 model-value：内容里全是 aiEditDraft.xxx，
         TS 只有在 v-if 分支内才认得它非空。 -->
    <QdModal
      v-if="aiEditDraft"
      :model-value="true"
      :title="t('aiEditProfile')"
      width="520px"
      @update:model-value="closeAIEditor"
    >
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
        <!-- 这一行用 div 而不是 label：里面多了两个按钮，label 会把点击顺带
             转给输入框，点「复制」时焦点乱跳。语义靠 field-label 文案保留。 -->
        <div class="field">
          <span class="field-label">{{ t('aiAPIKey') }}</span>
          <div class="key-input-wrap">
            <input
              v-model="aiEditDraft.apiKey"
              :type="keyVisible ? 'text' : 'password'"
              class="field-input"
              :placeholder="!profileNeedsKey(aiEditDraft) ? t('aiAPIKeyOptional') : 'sk-...'"
            />
            <div class="key-input-actions">
              <button
                type="button"
                class="qd-icon-btn"
                :title="keyVisible ? t('aiKeyHide') : t('aiKeyShow')"
                :aria-label="keyVisible ? t('aiKeyHide') : t('aiKeyShow')"
                :disabled="keyBusy"
                @click="toggleKeyVisible"
              >
                <EyeOff v-if="keyVisible" :size="13" />
                <Eye v-else :size="13" />
              </button>
              <button
                type="button"
                class="qd-icon-btn"
                :title="t('aiKeyCopy')"
                :aria-label="t('aiKeyCopy')"
                :disabled="keyBusy"
                @click="copyKey"
              >
                <Copy :size="13" />
              </button>
            </div>
          </div>
        </div>
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
            ><ChevronDown :size="12" /></button>
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
        <!-- 多模型（方案 A）：同一条接入下可启用多个模型。
             只影响「这条档案能用哪些模型」，当前用哪个仍由上面的输入框决定
             （选中项写入 modelId）；一个都不勾时行为与旧版完全一致。 -->
        <div v-if="modelOptions.length > 1" class="field">
          <span class="field-label">{{ t('aiModelsEnabled') }}</span>
          <div class="model-enable-list">
            <label v-for="m in modelOptions" :key="'en-' + m" class="model-enable-item">
              <input type="checkbox" :checked="isModelEnabled(m)" @change="toggleModel(m, $event)" />
              <span class="model-enable-name">{{ m }}</span>
            </label>
          </div>
          <p class="ai-field-hint">{{ t('aiModelsEnabledHint') }}</p>
        </div>
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
        </div>
        <!-- 最大输出 token 已从界面移除：主流 agent 工具都按模型自动取（见
             services/ai/ai_model_caps.go 的能力表 + 运行期修正），让用户手填一个
             384K 既没依据也容易把输入空间挤掉。字段仍留在档案里（0 = 自动），
             显式配置过的老档案行为不变。 -->
        <p class="ai-field-hint">{{ t('aiMaxTokensAutoHint') }}</p>
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
        <!-- 廉价模型（WP11）：只用在「起标题 / 摘要 / 压缩」这类输入长、输出短、
             失败也不致命的调用上。留空 = 全部用主模型。 -->
        <label class="field">
          <span class="field-label">{{ t('aiCheapModel') }}</span>
          <input v-model="aiEditDraft.cheapModel" class="field-input" :placeholder="t('aiCheapModelPh')" />
        </label>
        <p class="ai-field-hint">{{ t('aiCheapModelHint') }}</p>
        <!-- 压缩阈值（WP4c）：0 = 用默认值，所以留空不会改变任何行为。 -->
        <div class="field-row">
          <label class="field field-half">
            <span class="field-label">{{ t('aiSoftCap') }}</span>
            <input
              v-model.number="aiEditDraft.contextSoftCap"
              type="number"
              min="0"
              max="1000000"
              step="1000"
              class="num-input"
              :placeholder="String(aiSoftCapDefault)"
            />
          </label>
          <label class="field field-half">
            <span class="field-label">{{ t('aiPreflightMargin') }}</span>
            <input
              v-model.number="aiEditDraft.preflightMargin"
              type="number"
              min="0"
              max="95"
              step="1"
              class="num-input"
              :placeholder="String(aiPreflightMarginDefault)"
            />
          </label>
        </div>
        <div class="field-row">
          <label class="field field-half">
            <span class="field-label">{{ t('aiKeepRecentMin') }}</span>
            <input
              v-model.number="aiEditDraft.keepRecentMin"
              type="number"
              min="0"
              max="200"
              step="1"
              class="num-input"
              :placeholder="String(aiKeepRecentMinDefault)"
            />
          </label>
          <label class="field field-half">
            <span class="field-label">{{ t('aiKeepRecentMax') }}</span>
            <input
              v-model.number="aiEditDraft.keepRecentMax"
              type="number"
              min="0"
              max="200"
              step="1"
              class="num-input"
              :placeholder="String(aiKeepRecentMaxDefault)"
            />
          </label>
        </div>
        <p class="ai-field-hint">{{ t('aiThresholdHint') }}</p>
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
            <span class="field-label">{{ t('aiThinkingMode') }}</span>
            <QdSwitch :model-value="aiEditDraft.thinkingEnabled" :label="t('aiThinkingMode')"
                      @update:model-value="(v: boolean) => aiEditDraft && (aiEditDraft.thinkingEnabled = v)" />
          </label>
        </div>
      </div>
      <template #footer>
        <button class="btn btn-secondary" @click="closeAIEditor">{{ t('cancel') }}</button>
        <button class="btn btn-primary" @click="saveAIModal">{{ t('save') }}</button>
      </template>
    </QdModal>

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
/* API Key 行：输入框右侧内嵌「显示 / 复制」两个图标按钮。
   沿用 .model-input-wrap 的思路（相对容器 + 输入框留出右侧内边距），
   区别只是这里有俩按钮，所以内边距给到 62px。 */
.key-input-wrap {
  position: relative;
  display: flex;
  align-items: center;
}
.key-input-wrap .field-input {
  flex: 1;
  padding-right: 62px;
}
.key-input-actions {
  position: absolute;
  right: 4px;
  top: 50%;
  transform: translateY(-50%);
  display: flex;
  align-items: center;
  gap: var(--space-1);
}
/* .qd-icon-btn 默认 26×26，塞进 36px 高的输入框里会顶到边框，这里缩一档 */
.key-input-actions .qd-icon-btn {
  width: 24px;
  height: 24px;
}

.ai-field-hint {
  margin: var(--space-1) 0 0;
  font-size: var(--text-xs);
  line-height: 1.5;
  color: var(--color-text-muted);
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
  font-size: var(--text-xs);
  line-height: 1;
}

/* 单条费用开关：插在用量面板标题行下方，补一点上间距免得贴着标题。
   只写在本 SFC 的 scoped 块里，不改全局 settings.css。 */
.ai-cost-pref { margin-top: var(--space-4); }
/* 多模型勾选列表：模型名可能几十个且很长（open_/trae- 前缀），
   不限高就是截图里那种一片乱麻。限高 + 滚动 + 两列。 */
.model-enable-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: var(--space-1) var(--space-3);
  max-height: 180px;
  overflow-y: auto;
  padding: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-elevated);
}
.model-enable-item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  cursor: pointer;
}
.model-enable-name {
  font-size: var(--text-sm);
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 字符箭头换 Lucide 后补基线对齐 */
.approval-ro-arrow { display: inline-flex; vertical-align: -2px; }
</style>
