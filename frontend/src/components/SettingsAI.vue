<script setup lang="ts">
import { ref, computed, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pen, Trash2, Plus, RefreshCw } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

import {
  AIFetchModels,
  AIListProfiles,
  AISaveProfiles,
  AITestConnection,
} from '../../bindings/quickdock/services/ai/aiservice'
import { EnvOllamaModels } from '../../bindings/quickdock/services/env/environmentservice'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AIProfilesResult } from '../types/ai'
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

watch(() => props.visible, (v) => { if (v) loadAIProfiles() }, { immediate: true })

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
