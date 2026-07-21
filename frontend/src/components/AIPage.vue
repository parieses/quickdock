<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { Bot, Plus, Trash2, Copy, Square, RefreshCw, Eraser } from '@lucide/vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'
import type { AIProfile } from '../../bindings/quickdock/services/models'
import type { AIProfilesResult } from '../types/ai'
import {
  AIListProfiles, AISetActiveProfile, AIStreamInfo,
  AIClearMessages, AIRegenerateTitle,
  AIListConversations, AIDeleteConversation, AIGetMessages,
} from '../../bindings/quickdock/services/appservice'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface AIConversation { id: string; title: string; summary: string; prompt_tokens: number; completion_tokens: number; created_at: string; updated_at: string }
interface AIMessage { id: string; conv_id: string; role: string; content: string; created_at: string; reasoning_content?: string }

const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

const conversations = ref<AIConversation[]>([])
const activeId = ref('')
const messages = ref<AIMessage[]>([])
const input = ref('')
const mode = ref('chat')
const streaming = ref(false)
const streamingText = ref('')
const renderedStreamHtml = ref('')
let renderTimer: ReturnType<typeof setTimeout> | null = null
watch(streamingText, (val) => {
  if (renderTimer) return
  renderTimer = setTimeout(() => {
    renderTimer = null
    renderedStreamHtml.value = renderMarkdown(val)
  }, 60)
})
const reasoningText = ref('')  // 思考过程（reasoning_content），折叠显示
const errorMsg = ref('')

// 本地 AI 流式服务信息（端口 + 随机令牌，通过 Header 传递）
const aiStreamPort = ref(0)
const aiStreamToken = ref('')
let streamCtrl: AbortController | null = null

// 消息区 DOM 引用，有新内容时自动滚到底部
const msgArea = ref<HTMLElement | null>(null)
watch(streamingText, scrollToBottom)

// 多档案 / 模型切换
const aiProfiles = ref<AIProfile[]>([])
const aiActive = ref('')
const hasKey = computed(() => {
  const p = aiProfiles.value.find((x) => x.id === aiActive.value)
  return !!(p && p.apiKey)
})

const modes = [
  { key: 'chat', label: 'aiModeChat' },
  { key: 'explain', label: 'aiModeExplain' },
  { key: 'translate', label: 'aiModeTranslate' },
  { key: 'summarize', label: 'aiModeSummarize' },
]

marked.setOptions({ breaks: true, gfm: true })

function renderMarkdown(src: string): string {
  const html = marked.parse(src ?? '', { async: false }) as string
  return DOMPurify.sanitize(html)
}

async function loadProfiles() {
  try {
    const res = unwrap<AIProfilesResult>(await AIListProfiles())
    if (!res) return
    aiProfiles.value = res.profiles ?? []
    aiActive.value = res.active || (aiProfiles.value[0]?.id ?? '')
  } catch {
    aiProfiles.value = []
  }
}

async function loadConversations() {
  try {
    const list = unwrap<AIConversation[]>(await AIListConversations())
    conversations.value = list ?? []
  } catch { /* ignore */ }
}

async function loadMessages(convID: string) {
  try {
    const msgs = unwrap<AIMessage[]>(await AIGetMessages(convID))
    messages.value = msgs ?? []
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
  scrollToBottom()
}

function scrollToBottom() {
  nextTick(() => { if (msgArea.value) msgArea.value.scrollTop = msgArea.value.scrollHeight })
}

async function selectConv(id: string) {
  // 如果有正在流的对话，先打断
  if (streaming.value) stop()
  activeId.value = id
  streamingText.value = ''
  reasoningText.value = ''
  errorMsg.value = ''
  await loadMessages(id)
}

function newConv() {
  if (streaming.value) stop()
  activeId.value = ''
  messages.value = []
  streamingText.value = ''
  reasoningText.value = ''
  errorMsg.value = ''
}

async function delConv(id: string) {
  if (!window.confirm(t('confirmDelete'))) return
  try {
    unwrap(await AIDeleteConversation(id))
    if (activeId.value === id) newConv()
    await loadConversations()
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

function onModelChange() {
  if (aiActive.value) AISetActiveProfile(aiActive.value)
}

async function regenTitle() {
  if (!activeId.value || streaming.value) return
  try {
    const title = unwrap<string>(await AIRegenerateTitle(activeId.value))
    if (title) {
      const i = conversations.value.findIndex((c) => c.id === activeId.value)
      if (i >= 0) conversations.value[i].title = title
      else loadConversations()
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

async function clearContext() {
  if (!activeId.value) return
  if (!window.confirm(t('aiClearConfirm'))) return
  try {
    unwrap(await AIClearMessages(activeId.value))
    messages.value = []
    streamingText.value = ''
    errorMsg.value = ''
    toast.success(t('aiCleared'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 确保已拿到本地流式服务端口+令牌
async function ensureStreamInfo() {
  if (aiStreamPort.value) return
  const info = unwrap<{ port: number; token: string }>(await AIStreamInfo())
  if (!info) throw new Error('流式服务不可用')
  aiStreamPort.value = info.port
  aiStreamToken.value = info.token
}

async function send() {
  if (streaming.value) return
  const text = input.value.trim()
  if (!text) return
  if (!hasKey.value) {
    toast.error(t('aiNeedKey'))
    emit('open-settings', 'ai')
    return
  }
  const convId = activeId.value
  // 立即把用户消息显示出来，随后在消息区内等待 AI 流式回复
  messages.value.push({ id: '', conv_id: convId, role: 'user', content: text, created_at: new Date().toISOString() })
  scrollToBottom()
  input.value = ''
  streaming.value = true
  streamingText.value = ''
  reasoningText.value = ''
  errorMsg.value = ''
  try {
    await ensureStreamInfo()
    const ctrl = new AbortController()
    streamCtrl = ctrl
    const resp = await fetch(`http://127.0.0.1:${aiStreamPort.value}/ai/stream`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Stream-Token': aiStreamToken.value,
      },
      body: JSON.stringify({ convId, mode: mode.value, message: text }),
      signal: ctrl.signal,
    })
    if (!resp.body) throw new Error('无响应流')
    const reader = resp.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    // SSE 解析：消息以 \n\n 分隔，每条消息含 event: 和 data: 行
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      let sep
      while ((sep = buf.indexOf('\n\n')) >= 0) {
        const rawMsg = buf.slice(0, sep)
        buf = buf.slice(sep + 2)

        let event = 'message'
        let dataStr = ''
        for (const line of rawMsg.split('\n')) {
          if (line.startsWith('event: ')) {
            event = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            dataStr += line.slice(6)
          }
          // 忽略注释行（以 : 开头）和空行
        }
        if (!dataStr) continue

        let data: any
        try { data = JSON.parse(dataStr) } catch { continue }

        switch (event) {
          case 'token':
            if (data.text) streamingText.value += data.text
            break
          case 'reasoning':
            if (data.text) reasoningText.value += data.text
            break
          case 'conv':
            if (data.id) activeId.value = data.id
            if (data.title !== undefined) {
              const i = conversations.value.findIndex((c) => c.id === data.id)
              if (i >= 0) conversations.value[i].title = data.title
              else loadConversations()
            }
            break
          case 'done':
            if (data.convId) {
              if (data.convId !== activeId.value) break // 对话已被切换，忽略过期事件
              const content = streamingText.value
              const reasoning = reasoningText.value
              if (content || reasoning) {
                const msg: Record<string, any> = { id: `ast-${Date.now()}`, conv_id: data.convId, role: 'assistant', content: content || '', created_at: new Date().toISOString() }
                if (reasoning) msg.reasoning_content = reasoning
                messages.value.push(msg as AIMessage)
                scrollToBottom()
              }
              streamingText.value = ''
              reasoningText.value = ''
              loadConversations()
            }
            break
          case 'error':
            errorMsg.value = data.message || data.error || '未知错误'
            break
        }
      }
    }
  } catch (e: any) {
    if (e && e.name !== 'AbortError') errorMsg.value = getErrorMessage(e)
  } finally {
    streaming.value = false
    streamCtrl = null
  }
}

function stop() {
  if (streamCtrl) streamCtrl.abort()
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('copied'))
  } catch { /* ignore */ }
}

onMounted(async () => {
  await loadProfiles()
  await loadConversations()

  // 仅保留标题重生成的单条事件；AI 问答流式已改为本地 HTTP 流式（fetch 读取）
  Events.On('ai:conv', (ev: any) => {
    const data = ev?.data
    if (!data) return
    if (data.id && data.id !== activeId.value) activeId.value = data.id
    if (data.title !== undefined) {
      const idx = conversations.value.findIndex((c) => c.id === data.id)
      if (idx >= 0) conversations.value[idx].title = data.title
      else loadConversations()
    }
  })
})

onUnmounted(() => {
  Events.Off('ai:conv')
  if (streamCtrl) streamCtrl.abort()
})
</script>

<template>
  <div class="ai-page">
    <!-- 左侧会话列表 -->
    <aside class="ai-sidebar">
      <button class="ai-new-btn" @click="newConv">
        <Plus :size="14" /> {{ t('aiNewChat') }}
      </button>
      <div class="ai-conv-list">
        <div
          v-for="c in conversations"
          :key="c.id"
          :class="['ai-conv-item', { active: c.id === activeId }]"
          @click="selectConv(c.id)"
        >
          <span class="ai-conv-title">{{ c.title || t('aiNewChat') }}</span>
          <span v-if="c.prompt_tokens > 0 || c.completion_tokens > 0" class="ai-conv-tokens" :title="`输入 ${c.prompt_tokens} / 输出 ${c.completion_tokens} token`">
            {{ c.prompt_tokens }}/{{ c.completion_tokens }}
          </span>
          <button class="ai-conv-del" :title="t('delete')" @click.stop="delConv(c.id)">
            <Trash2 :size="13" />
          </button>
        </div>
        <div v-if="conversations.length === 0" class="ai-conv-empty">{{ t('aiNoConversations') }}</div>
      </div>
    </aside>

    <!-- 右侧聊天区 -->
    <section class="ai-main">
      <!-- 模式切换 + 模型选择 + 操作 -->
      <div class="ai-toolbar">
        <div class="ai-modes">
          <button
            v-for="m in modes"
            :key="m.key"
            :class="['ai-mode-tab', { active: mode === m.key }]"
            @click="mode = m.key"
          >{{ t(m.label) }}</button>
        </div>
        <div class="ai-toolbar-right">
          <select v-model="aiActive" class="ai-model-select" :title="t('aiSwitchModel')" @change="onModelChange">
            <option v-for="p in aiProfiles" :key="p.id" :value="p.id">{{ p.name || p.model }}</option>
          </select>
          <button v-if="activeId" class="ai-icon-btn" :title="t('aiRegenTitle')" @click="regenTitle">
            <RefreshCw :size="13" />
          </button>
          <button v-if="activeId" class="ai-icon-btn" :title="t('aiClearContext')" @click="clearContext">
            <Eraser :size="13" />
          </button>
          <button v-if="streaming" class="ai-stop" @click="stop">
            <Square :size="13" /> {{ t('aiStop') }}
          </button>
        </div>
      </div>

      <!-- 消息区 -->
      <div ref="msgArea" class="ai-messages">
        <div v-if="!hasKey" class="ai-need-key">
          <Bot :size="40" />
          <p>{{ t('aiNeedKey') }}</p>
          <button class="ai-go-settings" @click="emit('open-settings', 'ai')">{{ t('aiGoSettings') }}</button>
        </div>

        <template v-else>
          <div
            v-for="m in messages"
            :key="m.id"
            :class="['ai-msg', m.role]"
          >
            <div class="ai-msg-role">{{ m.role === 'user' ? t('aiYou') : 'AI' }}</div>
            <details v-if="m.reasoning_content" class="ai-reasoning">
              <summary>{{ t('aiThought') }} · {{ m.reasoning_content.length }}字</summary>
              <div class="ai-reasoning-content">{{ m.reasoning_content }}</div>
            </details>
            <div v-if="m.role === 'assistant'" class="ai-msg-content ai-md" v-html="renderMarkdown(m.content)"></div>
            <div v-else class="ai-msg-content">{{ m.content }}</div>
            <button v-if="m.role === 'assistant' && m.id" class="ai-copy" @click="copy(m.content)">
              <Copy :size="12" /> {{ t('copy') }}
            </button>
          </div>

          <div v-if="reasoningText" class="ai-reasoning">
            <details open>
              <summary>{{ t('aiThinking') }}</summary>
              <div class="ai-reasoning-content">{{ reasoningText }}</div>
            </details>
          </div>
          <div v-if="streamingText" class="ai-msg assistant">
            <div class="ai-msg-role">AI</div>
            <div class="ai-msg-content ai-md" v-html="renderedStreamHtml"></div>
            <span class="ai-cursor">▋</span>
          </div>

          <div v-if="errorMsg" class="ai-error">{{ errorMsg }}</div>

          <div v-if="messages.length === 0 && !streamingText && !errorMsg" class="ai-empty">
            <Bot :size="40" />
            <p>{{ t('aiEmptyHint') }}</p>
          </div>
        </template>
      </div>

      <!-- 输入区 -->
      <div class="ai-input-area">
        <textarea
          v-model="input"
          class="ai-input"
          :placeholder="t('aiPlaceholder')"
          :disabled="!hasKey || streaming"
          @keydown.enter.exact.prevent="send"
        />
        <button class="ai-send" :disabled="!hasKey || streaming || !input.trim()" @click="send">
          {{ streaming ? t('aiThinking') : t('send') }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ai-page {
  flex: 1;
  display: flex;
  min-width: 0;
  height: 100%;
  overflow: hidden;
}

/* 会话列表 */
.ai-sidebar {
  width: 220px;
  min-width: 220px;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-secondary);
  border-right: 1px solid var(--color-border);
  overflow: hidden;
}
.ai-new-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  margin: 12px;
  padding: 8px;
  border: 1px dashed var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  border-radius: 8px;
  cursor: pointer;
  font-family: inherit;
  font-size: 13px;
  transition: all var(--transition-fast);
}
.ai-new-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-conv-list { flex: 1; overflow-y: auto; padding: 0 8px 12px; }
.ai-conv-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: 13px;
  transition: background var(--transition-fast);
}
.ai-conv-item:hover { background: var(--color-bg-hover); }
.ai-conv-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.ai-conv-title {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ai-conv-del {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: 6px;
}
.ai-conv-item:hover .ai-conv-del { display: flex; }
.ai-conv-del:hover { color: var(--color-danger); background: rgba(232,76,76,0.1); }
.ai-conv-tokens {
  font-size: 10px; color: var(--color-text-disabled); white-space: nowrap;
  margin-right: 4px; font-variant-numeric: tabular-nums;
}
.ai-conv-empty { padding: 24px 12px; text-align: center; color: var(--color-text-disabled); font-size: 12px; }

/* 主区 */
.ai-main { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; }
.ai-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  gap: 10px;
}
.ai-modes { display: flex; gap: 6px; flex-wrap: wrap; }
.ai-mode-tab {
  padding: 5px 12px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text-muted);
  border-radius: 16px;
  font-size: 12px;
  cursor: pointer;
  font-family: inherit;
  transition: all var(--transition-fast);
}
.ai-mode-tab:hover { color: var(--color-text-primary); }
.ai-mode-tab.active { border-color: var(--color-accent); color: var(--color-accent); background: var(--color-accent-bg); }
.ai-toolbar-right { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.ai-model-select {
  max-width: 160px;
  padding: 5px 8px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 12px;
  font-family: inherit;
  outline: none;
}
.ai-model-select:focus { border-color: var(--color-accent); }
.ai-icon-btn {
  display: flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border: 1px solid var(--color-border);
  background: transparent; color: var(--color-text-muted);
  border-radius: 8px; cursor: pointer; font-family: inherit;
  transition: all var(--transition-fast);
}
.ai-icon-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-stop {
  display: flex; align-items: center; gap: 4px;
  padding: 5px 12px; border: none; border-radius: 6px;
  background: var(--color-bg-active); color: var(--color-text-secondary);
  font-size: 12px; cursor: pointer; font-family: inherit;
}
.ai-stop:hover { color: var(--color-danger); }

/* 思考过程折叠块 */
.ai-reasoning {
  font-size: 12px; margin: 2px 0; max-width: 80%;
}
.ai-reasoning details { background: var(--color-bg-active); border-radius: 6px; padding: 4px 8px; }
.ai-reasoning summary {
  cursor: pointer; color: var(--color-text-muted); font-size: 11px;
  user-select: none; outline: none;
}
.ai-reasoning summary:hover { color: var(--color-text-primary); }
.ai-reasoning-content {
  margin-top: 4px; padding-top: 4px; border-top: 1px solid var(--color-border);
  color: var(--color-text-secondary); font-size: 12px; line-height: 1.5;
  white-space: pre-wrap; word-break: break-word;
}

/* 消息 */
.ai-messages { flex: 1; overflow-y: auto; padding: 16px 20px; display: flex; flex-direction: column; gap: 10px; }
.ai-msg { display: flex; flex-direction: column; gap: 2px; max-width: 80%; }
.ai-msg.user { align-self: flex-end; align-items: flex-end; }
.ai-msg.assistant { align-self: flex-start; }
.ai-msg-role { font-size: 11px; color: var(--color-text-disabled); margin-bottom: 1px; }
.ai-msg-content {
  padding: 8px 12px;
  border-radius: 10px;
  font-size: 13px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
}
.ai-msg.user .ai-msg-content { background: var(--color-accent); color: var(--color-accent-text); }
.ai-copy {
  align-self: flex-start;
  display: flex; align-items: center; gap: 3px;
  background: none; border: none; color: var(--color-text-disabled);
  font-size: 11px; cursor: pointer; padding: 2px 4px; border-radius: 4px;
}
.ai-copy:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.ai-cursor { animation: ai-blink 1s steps(1) infinite; color: var(--color-accent); }
@keyframes ai-blink { 50% { opacity: 0; } }
.ai-error {
  align-self: flex-start;
  padding: 10px 14px; border-radius: 12px;
  background: rgba(232,76,76,0.12); color: var(--color-danger);
  font-size: 13px; max-width: 80%;
}
.ai-need-key, .ai-empty {
  margin: auto;
  display: flex; flex-direction: column; align-items: center; gap: 12px;
  color: var(--color-text-disabled); text-align: center;
}
.ai-need-key p, .ai-empty p { font-size: 13px; margin: 0; }
.ai-go-settings {
  padding: 7px 16px; border: none; border-radius: 8px;
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 13px; cursor: pointer; font-family: inherit;
}

/* Markdown 内容——紧凑排版 */
.ai-md :deep(h1), .ai-md :deep(h2), .ai-md :deep(h3), .ai-md :deep(h4) {
  font-size: 14px; margin: 6px 0 3px; font-weight: 600; line-height: 1.4;
}
.ai-md :deep(p) { margin: 3px 0; }
.ai-md :deep(ul), .ai-md :deep(ol) { padding-left: 18px; margin: 3px 0; }
.ai-md :deep(li) { margin: 1px 0; line-height: 1.45; }
.ai-md :deep(li > p) { margin: 0; }
.ai-md :deep(br) { display: none; }
.ai-md :deep(a) { color: var(--color-accent); }
.ai-md :deep(blockquote) {
  border-left: 3px solid var(--color-border); padding-left: 10px;
  color: var(--color-text-muted); margin: 6px 0;
}
.ai-md :deep(pre) {
  background: rgba(0,0,0,0.28); border-radius: 6px; padding: 10px 12px;
  overflow-x: auto; margin: 6px 0;
}
.ai-md :deep(code) {
  font-family: var(--font-mono, monospace); font-size: 12px;
  background: rgba(0,0,0,0.28); padding: 1px 4px; border-radius: 4px;
}
.ai-md :deep(pre code) { background: none; padding: 0; }
.ai-md :deep(table) { border-collapse: collapse; margin: 6px 0; }
.ai-md :deep(th), .ai-md :deep(td) { border: 1px solid var(--color-border); padding: 4px 8px; }

/* 输入 */
.ai-input-area {
  display: flex;
  gap: 10px;
  padding: 12px 16px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}
.ai-input {
  flex: 1;
  resize: none;
  height: 56px;
  padding: 10px 12px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 13px;
  font-family: inherit;
  outline: none;
  line-height: 1.5;
}
.ai-input:focus { border-color: var(--color-accent); }
.ai-input:disabled { opacity: 0.6; cursor: not-allowed; }
.ai-send {
  align-self: flex-end;
  padding: 0 20px;
  height: 56px;
  border: none;
  border-radius: 10px;
  background: var(--color-accent);
  color: var(--color-accent-text);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  font-family: inherit;
  white-space: nowrap;
}
.ai-send:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
