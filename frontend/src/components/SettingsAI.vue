<script setup lang="ts">
import { ref, computed, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pen, Trash2, Plus } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { AIListProfiles, AISaveProfiles, AITestConnection } from '../../bindings/quickdock/services/appservice'
import type { AIProfile } from '../../bindings/quickdock/services/models'
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
  if (!cur || !cur.apiKey || !cur.baseURL || !cur.model) {
    showAIMsg('请先填写 API Key、Base URL 和 Model', true); return
  }
  await saveAIProfiles()
  aiTesting.value = true; showAIMsg('测试中…')
  try {
    const res = await AITestConnection(cur.id)
    if (!res) throw new Error('无响应')
    showAIMsg(res.message || '未知', res.success !== true)
  } catch (e: any) {
    showAIMsg('测试失败: ' + (e?.message || String(e)), true)
  } finally { aiTesting.value = false }
}
function openAIEditor() {
  const cur = aiCurrent.value
  if (!cur) return
  aiEditDraft.value = { ...cur }
}
function onAIProviderChangeDraft() {
  const d = aiEditDraft.value
  if (!d) return
  const url = aiPresets[d.provider]
  if (url) d.baseURL = url
}
function closeAIEditor() { aiEditDraft.value = null }
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
              <input v-model="aiEditDraft.apiKey" type="password" class="field-input" placeholder="sk-..." />
            </label>
            <label class="field">
              <span class="field-label">{{ t('aiModel') }}</span>
              <input v-model="aiEditDraft.model" type="text" class="field-input" placeholder="gpt-4o-mini / deepseek-chat" />
            </label>
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
