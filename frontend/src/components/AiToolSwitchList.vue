<script setup lang="ts">
/**
 * 内置 AI 可用工具：域级总开关 + 单工具开关 + 等级上限。
 *
 * 这里的开关**只影响内置 AI**：与「环境管理 → MCP」里的工具开关是两份互不读取的策略。
 * 关掉这里的 file_edit，外部 MCP 客户端照常能改文件；反过来也一样。
 * 界面文案必须把这件事说清，否则用户会以为「关了一处、两边都没了」——
 * 那正是这次要修掉的老行为。
 */
import { computed, inject, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertCircle, RefreshCw } from '@lucide/vue'
import QdSwitch from './QdSwitch.vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import type { ToastAPI } from '../types'
import {
  AISetDomainEnabled,
  AISetToolEnabled,
  AISetToolMaxLevel,
  AIToolPolicy,
} from '../../bindings/quickdock/services/ai/aiservice'

interface ToolRow {
  name: string
  domain: string
  level: number
  desc: string
  /** 用户自己关没关它（开关状态只由这一位决定） */
  enabled: boolean
  disabled: boolean
  /** 被等级上限挡住：开关可以是开的，但这个工具仍然调不到 */
  gated: boolean
  agentOnly: boolean
}

interface DomainRow {
  id: string
  label: string
  order: number
  desc: string
  public: boolean
}

interface PolicyData {
  maxLevel: number
  disabled: string[]
  tools: ToolRow[]
  domains: DomainRow[]
}

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const data = ref<PolicyData>({ maxLevel: 1, disabled: [], tools: [], domains: [] })
const loading = ref(false)
const error = ref('')
const busy = ref('')

// 等级上限只给两档可选：高危（2）没有对应的 UI 入口，因为 AI 侧默认就不挂它，
// 真要放开需要用户明确知道自己在做什么——这一类改动不该靠一个下拉框顺手完成。
const levelOptions = computed(() => [
  { value: 0, label: t('aiToolsLevelRead') },
  { value: 1, label: t('aiToolsLevelWrite') },
])

/** 按域分组；域的顺序与标题都来自后端目录，前端不再维护第二份顺序表。 */
const groups = computed(() => {
  const byDomain = new Map<string, ToolRow[]>()
  for (const tool of data.value.tools) {
    const key = tool.domain || 'other'
    const list = byDomain.get(key)
    if (list) list.push(tool)
    else byDomain.set(key, [tool])
  }
  const meta = new Map(data.value.domains.map((d) => [d.id, d]))
  const rest = [...byDomain.keys()].filter((k) => !meta.has(k))
  const ordered = [...data.value.domains.map((d) => d.id), ...rest]
  return ordered
    .filter((id) => byDomain.has(id))
    .map((id) => {
      const tools = byDomain.get(id) || []
      const label = meta.get(id)?.label || ''
      return {
        id,
        // label 是 i18n key（如 mcpCatEnv）；查不到就退回 id，至少不显示空白。
        label: label ? t(label) : id,
        desc: meta.get(id)?.desc || '',
        tools,
        allOn: tools.every((x) => x.enabled),
        someOn: tools.some((x) => x.enabled),
      }
    })
})

function levelOf(level: number): string {
  if (level <= 0) return t('aiToolsTagRead')
  if (level === 1) return t('aiToolsTagWrite')
  return t('aiToolsTagRisk')
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const d = unwrap<PolicyData>(await AIToolPolicy())
    data.value = d ?? { maxLevel: 1, disabled: [], tools: [], domains: [] }
  } catch (e) {
    error.value = getErrorMessage(e)
    logErr('ai-tools', e)
  } finally {
    loading.value = false
  }
}

async function setLevel(level: number) {
  if (busy.value) return
  busy.value = 'level'
  try {
    unwrap(await AISetToolMaxLevel(level))
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('ai-tools', e)
  } finally {
    busy.value = ''
  }
}

async function toggleTool(tool: ToolRow, on: boolean) {
  if (busy.value) return
  busy.value = tool.name
  // 乐观更新：开关要跟手，失败了再回滚（load() 会拉回真实状态）。
  tool.enabled = on
  try {
    unwrap(await AISetToolEnabled(tool.name, on))
    await load()
  } catch (e) {
    tool.enabled = !on
    toast.error(getErrorMessage(e))
    logErr('ai-tools', e)
  } finally {
    busy.value = ''
  }
}

async function toggleDomain(domain: string, on: boolean) {
  if (busy.value) return
  busy.value = domain
  try {
    unwrap(await AISetDomainEnabled(domain, on))
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('ai-tools', e)
  } finally {
    busy.value = ''
  }
}

onMounted(load)
defineExpose({ reload: load })
</script>

<template>
  <!-- 复用设置页既有的 .ai-usage 卡片范式（settings.css 里的全局类），
       而不是自己再造一层边框与标题：同一个「权限」页里并排两块，
       视觉权重不一致会让人以为其中一块是别的东西。 -->
  <div class="ai-usage ai-tools">
    <div class="ai-usage-head">
      <h4 class="ai-usage-title">{{ t('aiToolsTitle') }}</h4>
      <button class="ai-tools-refresh" type="button" :title="t('aiToolsRefresh')" @click="load">
        <RefreshCw :size="14" />
      </button>
    </div>
    <p class="usage-hint">{{ t('aiToolsDesc') }}</p>

    <div class="ai-tools-level">
      <span class="ai-tools-level-label">{{ t('aiToolsLevel') }}</span>
      <select
        class="field-input ai-tools-select"
        :value="data.maxLevel"
        :disabled="!!busy"
        @change="setLevel(Number(($event.target as HTMLSelectElement).value))"
      >
        <option v-for="o in levelOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select>
    </div>

    <p v-if="error" class="ai-tools-error">
      <AlertCircle :size="14" /> {{ error }}
    </p>
    <p v-else-if="loading && !data.tools.length" class="usage-hint">{{ t('aiUsageLoading') }}</p>

    <div v-for="g in groups" :key="g.id" class="ai-tools-group">
      <div class="ai-tools-group-head">
        <div class="ai-tools-group-title">
          <span>{{ g.label }}</span>
          <span v-if="!g.someOn" class="ai-tools-off-tag">{{ t('aiToolsDomainOff') }}</span>
        </div>
        <QdSwitch
          :model-value="g.allOn"
          :disabled="!!busy"
          :label="g.label"
          @update:model-value="(v: boolean) => toggleDomain(g.id, v)"
        />
      </div>
      <p v-if="g.desc" class="ai-tools-group-desc">{{ g.desc }}</p>

      <div v-for="tool in g.tools" :key="tool.name" class="ai-tools-row" :class="{ 'is-gated': tool.gated }">
        <div class="ai-tools-row-main">
          <code class="ai-tools-name">{{ tool.name }}</code>
          <span class="ai-tools-tag" :class="{ 'is-write': tool.level === 1, 'is-risk': tool.level >= 2 }">
            {{ levelOf(tool.level) }}
          </span>
          <span v-if="tool.gated" class="ai-tools-tag is-gated-tag" :title="t('aiToolsGatedHint')">
            {{ t('aiToolsGated') }}
          </span>
          <span v-if="tool.agentOnly" class="ai-tools-tag is-agent">{{ t('aiToolsAgentOnly') }}</span>
          <div class="ai-tools-row-desc">{{ tool.desc }}</div>
        </div>
        <QdSwitch
          :model-value="tool.enabled"
          :disabled="!!busy"
          :label="tool.name"
          @update:model-value="(v: boolean) => toggleTool(tool, v)"
        />
      </div>
    </div>

    <p class="ai-tools-note">{{ t('aiToolsMcpNote') }}</p>
  </div>
</template>

<style scoped>
/* 只覆盖间距：卡片外观来自全局 .ai-usage。它默认带 margin-top（为了接在上一个
   卡片后面），而这块是「权限」页的第一块，顶上是标签栏，不需要再空一截。 */
.ai-tools {
  margin-top: 0;
  margin-bottom: var(--space-5);
}

.ai-tools-refresh {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ai-tools-refresh:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.ai-tools-level {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-top: var(--space-4);
}

.ai-tools-level-label {
  font-size: var(--text-sm);
  color: var(--color-text-primary);
}

.ai-tools-select {
  width: 220px;
}

.ai-tools-error {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin: var(--space-3) 0 0;
  font-size: var(--text-xs);
  color: var(--color-danger);
}

.ai-tools-group {
  margin-top: var(--space-5);
  border-top: 1px solid var(--color-border);
  padding-top: var(--space-4);
}

.ai-tools-group-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
}

.ai-tools-group-title {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-sm);
  color: var(--color-text-primary);
}

.ai-tools-off-tag {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.ai-tools-group-desc {
  margin: var(--space-1) 0 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.ai-tools-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-3) 0;
  border-bottom: 1px solid var(--color-border);
}

.ai-tools-row:last-child {
  border-bottom: none;
}

.ai-tools-row-main {
  min-width: 0;
}

.ai-tools-name {
  font-size: var(--text-sm);
  color: var(--color-text-primary);
}

.ai-tools-tag {
  margin-left: var(--space-2);
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  border: 1px solid var(--color-border);
}

.ai-tools-tag.is-write {
  color: var(--color-warning);
  border-color: var(--color-warning);
}

.ai-tools-tag.is-risk {
  color: var(--color-danger);
  border-color: var(--color-danger);
}

.ai-tools-tag.is-agent {
  color: var(--color-text-muted);
}

.ai-tools-tag.is-gated-tag {
  color: var(--color-text-muted);
  border-style: dashed;
}

/* 被等级挡住的行压暗但不隐藏：用户要能看见「它还在，只是现在调不到」，
   否则抬高等级之后会以为工具是凭空出现的。 */
.ai-tools-row.is-gated .ai-tools-name,
.ai-tools-row.is-gated .ai-tools-row-desc {
  opacity: 0.65;
}

.ai-tools-row-desc {
  margin-top: var(--space-1);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  line-height: 1.5;
}

.ai-tools-note {
  margin: var(--space-4) 0 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  line-height: 1.6;
}
</style>
