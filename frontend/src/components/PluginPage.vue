<script setup lang="ts">
import { ref, onMounted, onUnmounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Minus, Square, X } from '@lucide/vue'
import { GetPluginFrontendPage, ExecutePluginCommand, HidePluginWindow, MinimizePluginWindow, ToggleMaximizePluginWindow, GetAndClearPendingPluginInit } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import type { ToastAPI } from '../types'

const props = defineProps<{ pluginId: string }>()

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!

const iframeSrc = ref('')
const loading = ref(true)
const error = ref('')
const pluginName = ref('')
let messageHandler: ((e: MessageEvent) => void) | null = null
let iframeWindow: Window | null = null

onMounted(async () => {
  try {
    const html = unwrap<string>(await GetPluginFrontendPage(props.pluginId))
    if (!html) {
      error.value = t('pluginNoFrontend')
      loading.value = false
      return
    }
    const match = html.match(/<title>([^<]*)<\/title>/)
    pluginName.value = match ? match[1] : props.pluginId

    const blob = new Blob([html], { type: 'text/html;charset=utf-8' })
    iframeSrc.value = URL.createObjectURL(blob)
    loading.value = false
  } catch (e: any) {
    error.value = t('pluginLoadFailed') + ': ' + (e?.message || String(e))
    loading.value = false
  }

  messageHandler = async (event: MessageEvent) => {
    // 验证消息来源：只接受插件 iframe 的消息
    if (event.source !== iframeWindow) return

    if (event.data?.type === 'plugin:execute') {
      const { id, command, input } = event.data
      try {
        const result = await ExecutePluginCommand(props.pluginId, command, input || null)
        const data = unwrap(result)
        if (event.source && 'postMessage' in (event.source as any)) {
          ;(event.source as any).postMessage(
            { type: 'plugin:result', id, data },
            window.location.origin
          )
        }
      } catch (e: any) {
        if (event.source && 'postMessage' in (event.source as any)) {
          ;(event.source as any).postMessage(
            { type: 'plugin:result', id, error: e?.message || String(e) },
            window.location.origin
          )
        }
      }
    }
  }
  window.addEventListener('message', messageHandler)
})

onUnmounted(() => {
  if (iframeSrc.value) URL.revokeObjectURL(iframeSrc.value)
  if (messageHandler) window.removeEventListener('message', messageHandler)
})

async function onIframeLoad(event: Event) {
  iframeWindow = (event.target as HTMLIFrameElement)?.contentWindow
  // 从 Go 后端检查有没有待传递的初始文本（从命令面板来）
  try {
    const raw = await GetAndClearPendingPluginInit()
    const init = raw?.data || raw
    const text = (init && typeof init === 'object') ? (init.text || '') : (typeof init === 'string' ? init : '')
    const command = (init && typeof init === 'object') ? (init.command || '') : ''
    if (iframeWindow) {
      // 先发 theme 消息让插件 HTML 应用主题
      iframeWindow.postMessage({ type: 'plugin:theme', data: { theme: 'dark', locale: locale.value } }, window.location.origin)
      // 再发 init 消息（携带 text、command 和主题/语言）
      iframeWindow.postMessage({
        type: 'plugin:init',
        data: { text, command, theme: 'dark', locale: locale.value }
      }, window.location.origin)
    }
  } catch {}
}

function closeWindow() {
  HidePluginWindow(props.pluginId)
}
</script>

<template>
  <div class="plugin-window">
    <!-- 标题栏 -->
    <div class="pw-titlebar">
      <span class="pw-title">{{ pluginName || props.pluginId }}</span>
      <div class="pw-controls">
        <button class="pw-btn pw-btn-min" @click="MinimizePluginWindow(props.pluginId)" :title="t('minimize')">
          <Minus :size="13" />
        </button>
        <button class="pw-btn pw-btn-max" @click="ToggleMaximizePluginWindow(props.pluginId)" :title="t('maximize')">
          <Square :size="11" />
        </button>
        <button class="pw-btn pw-btn-close" @click="closeWindow" :title="t('close')">
          <X :size="14" />
        </button>
      </div>
    </div>

    <!-- 内容区 -->
    <div class="pw-body">
      <div v-if="loading" class="pw-status">{{ t('loading') }}</div>
      <div v-else-if="error" class="pw-status pw-error">{{ error }}</div>
      <iframe
        v-else
        :src="iframeSrc"
        class="pw-iframe"
        sandbox="allow-scripts allow-same-origin allow-modals"
        frameborder="0"
        @load="onIframeLoad"
      />
    </div>
  </div>
</template>

<style scoped>
.plugin-window {
  display: flex; flex-direction: column;
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}

/* 标题栏：shadow-border 替代 solid border */
.pw-titlebar {
  display: flex; align-items: center; justify-content: space-between;
  height: 36px; flex-shrink: 0;
  padding: 0 0 0 14px;
  background: var(--color-bg-secondary);
  box-shadow: inset 0 -1px 0 0 var(--color-border);
  -webkit-app-region: drag;
  user-select: none;
}
.pw-title {
  font-size: 12px; font-weight: 500;
  color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  letter-spacing: 0.02em;
}
.pw-controls {
  display: flex; align-items: center;
  -webkit-app-region: no-drag;
}
.pw-btn {
  display: flex; align-items: center; justify-content: center;
  width: 46px; height: 36px;
  border: none; background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background 0.1s, color 0.1s;
}
.pw-btn:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.pw-btn:active {
  background: var(--color-bg-active);
}
.pw-btn-close:hover {
  background: var(--color-danger);
  color: #fff;
}
.pw-btn-max svg {
  transform: rotate(180deg);
}

/* 内容区 */
.pw-body {
  flex: 1; display: flex; overflow: hidden;
}
.pw-status {
  flex: 1; display: flex; align-items: center; justify-content: center;
  color: var(--color-text-disabled); font-size: 13px;
  user-select: none;
}
.pw-error {
  color: var(--color-danger);
  padding: 0 24px; text-align: center;
  line-height: 1.6;
}
.pw-iframe {
  flex: 1; width: 100%; border: none;
  background: var(--color-bg-primary);
}
</style>
