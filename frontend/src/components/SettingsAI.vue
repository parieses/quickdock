<script setup lang="ts">
import { ref, computed, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pen, Trash2, Plus, RefreshCw } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

import {
  AIApprovalStats,
  AIFetchModels,
  AIListApprovalRules,
  AIListProfiles,
  AISaveProfiles,
  AISetApprovalRule,
  AITestConnection,
  AIUsageStats,
  AIGetExtServers,
  AISaveExtServers,
  AIReloadExtServers,
} from '../../bindings/quickdock/services/ai/aiservice'
import { EnvOllamaModels } from '../../bindings/quickdock/services/env/environmentservice'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type {
  AIApprovalRuleView,
  AIApprovalStatsResult,
  AIExtServerState,
  AIProfilesResult,
  AIUsageStatsResult,
} from '../types/ai'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const emit = defineEmits<{ close: [] }>()
const props = defineProps<{ visible: boolean }>()

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
// 状态由后端维护（连接、注册工具都在那边），这里只负责展示与提交整份 JSON。
const extServers = ref<AIExtServerState[]>([])
const extLoading = ref(false)
const extSaving = ref(false)
const extError = ref('')
const extEditing = ref(false)
const extDraft = ref('')

async function loadExtServers() {
  extLoading.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AIGetExtServers()) ?? []
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extLoading.value = false
  }
}

// 从当前状态反推可编辑的 JSON：error 的 server 也要保住它的配置——
// 用户改的往往正是那个连不上的。
function startEditExt() {
  const doc: Record<string, unknown> = {}
  for (const s of extServers.value) doc[s.name] = s.config
  extDraft.value = JSON.stringify(doc, null, 2)
  extEditing.value = true
}

async function saveExt() {
  let doc: Record<string, unknown>
  try {
    doc = JSON.parse(extDraft.value)
  } catch {
    extError.value = t('aiExtBadJson')
    return
  }
  extSaving.value = true
  extError.value = ''
  try {
    const items = Object.entries(doc).map(([name, v]) => {
      const c = (v ?? {}) as Record<string, any>
      return {
        name,
        command: String(c.command ?? ''),
        args: Array.isArray(c.args) ? c.args.map(String) : [],
        env: (c.env && typeof c.env === 'object') ? Object.fromEntries(Object.entries(c.env).map(([k, e]) => [k, String(e)])) : {},
        url: String(c.url ?? ''),
        disabled: !!c.disabled,
      }
    })
    const res = unwrap<{ states: AIExtServerState[] }>(await AISaveExtServers(items))
    extServers.value = res?.states ?? []
    extEditing.value = false
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extSaving.value = false
  }
}

async function reloadExt() {
  extLoading.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AIReloadExtServers()) ?? []
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

// 每日柱状：多则只画最近 30 天（全部时间窗口可能上百天）。
// 计价为 0 的窗口（例如全用免费模型/未收录模型）退回按 token 画，否则柱子全是空的。
const dailyBars = computed(() => {
  const all = usage.value?.daily ?? []
  const list = all.slice(-30)
  if (!list.length) return []
  const useCost = list.some((d) => d.estimatedCost > 0)
  const max = Math.max(1, ...list.map((d) => (useCost ? d.estimatedCost : d.totalTokens)))
  return list.map((d) => {
    const v = useCost ? d.estimatedCost : d.totalTokens
    return {
      key: d.key,
      label: fmtDay(d.key),
      // 最低 2px：小于 1% 的日子也该留下一条痕迹，不然看不出「那天用过」
      height: v > 0 ? Math.max(2, Math.round((v / max) * 46)) : 0,
      title: t('aiUsageDayTip', {
        day: d.key,
        requests: d.requests,
        tokens: fmtTokens(d.totalTokens),
        cost: fmtCost(d.estimatedCost),
      }),
    }
  })
})

watch(() => props.visible, (v) => {
  if (!v) return
  loadAIProfiles()
  loadUsage()
  loadApprovalRules()
  loadExtServers()
}, { immediate: true })

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

        <div v-if="dailyBars.length" class="usage-chart">
          <div v-for="b in dailyBars" :key="b.key" class="usage-bar" :title="b.title">
            <div class="usage-bar-fill" :style="{ height: b.height + 'px' }" />
          </div>
        </div>
        <div v-if="dailyBars.length" class="usage-axis">
          <span>{{ dailyBars[0].label }}</span>
          <span>{{ dailyBars[dailyBars.length - 1].label }}</span>
        </div>

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

    <!-- 外部 MCP 服务器（阶段 D）：把别的 server 的工具接进内置 AI。
         配置直接用 JSON 文本框：字段与 Claude Desktop / Cursor 的 mcp.json 一致，
         现成配置粘贴过来就能用——表单化每加一个字段都要改 UI，JSON 一次到位。 -->
    <div class="ai-usage">
      <div class="ai-usage-head">
        <h4 class="ai-usage-title">{{ t('aiExtTitle') }}</h4>
        <button class="approval-ro-toggle" @click="extEditing ? (extEditing = false) : startEditExt()">
          {{ extEditing ? t('cancel') : t('aiExtEdit') }}
        </button>
      </div>
      <p class="usage-hint">{{ t('aiExtDesc') }}</p>

      <p v-if="extError" class="result-hint error">{{ extError }}</p>

      <template v-if="extEditing">
        <textarea
          v-model="extDraft"
          class="field-input ai-ext-json"
          :placeholder="t('aiExtPh')"
          spellcheck="false"
        ></textarea>
        <div class="ai-ext-actions">
          <button class="ai-hook-ok" :disabled="extSaving" @click="saveExt">{{ t('aiExtSaveReload') }}</button>
        </div>
      </template>
      <template v-else>
        <p v-if="extLoading && !extServers.length" class="usage-hint">{{ t('aiUsageLoading') }}</p>
        <p v-else-if="!extServers.length" class="usage-hint">{{ t('aiExtEmpty') }}</p>
        <table v-else class="usage-table approval-rules">
          <tbody>
            <tr v-for="s in extServers" :key="s.name">
              <td class="model">
                <code>{{ s.name }}</code>
                <span :class="['ext-status', s.status]">
                  {{ s.status === 'ok' ? t('aiExtStatusOk', { n: s.toolCount }) : s.status === 'disabled' ? t('aiExtStatusOff') : t('aiExtStatusErr') }}
                </span>
                <div v-if="s.error" class="usage-cell-sub">{{ s.error }}</div>
                <div v-else-if="s.tools.length" class="usage-cell-sub">{{ s.tools.join(' · ') }}</div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-if="extServers.some((s) => s.status === 'error')" class="ai-ext-actions">
          <button class="ai-hook-no" :disabled="extLoading" @click="reloadExt">{{ t('aiExtReload') }}</button>
        </div>
      </template>
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
  </div>
</template>

<style scoped>
.ai-field-hint {
  margin: 2px 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-muted);
}
</style>
