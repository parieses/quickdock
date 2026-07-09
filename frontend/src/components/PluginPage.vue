<script setup lang="ts">
import { ref, onMounted, onUnmounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Minus, Square, X } from '@lucide/vue'
import { GetPluginFrontendPage, ExecutePluginCommand, HidePluginWindow, MinimizePluginWindow, ToggleMaximizePluginWindow } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import type { ToastAPI } from '../types'

const props = defineProps<{ pluginId: string }>()

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const iframeSrc = ref('')
const loading = ref(true)
const error = ref('')
const pluginName = ref('')
let messageHandler: ((e: MessageEvent) => void) | null = null

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

    const blob = new Blob([html], { type: 'text/html' })
    iframeSrc.value = URL.createObjectURL(blob)
    loading.value = false
  } catch (e: any) {
    error.value = t('pluginLoadFailed') + ': ' + (e?.message || String(e))
    loading.value = false
  }

  messageHandler = async (event: MessageEvent) => {
    if (event.data?.type === 'plugin:execute') {
      const { id, command, input } = event.data
      try {
        const result = await ExecutePluginCommand(props.pluginId, command, input || null)
        const data = unwrap(result)
        if (event.source && 'postMessage' in (event.source as any)) {
          ;(event.source as any).postMessage({ type: 'plugin:result', id, data }, '*')
        }
      } catch (e: any) {
        if (event.source && 'postMessage' in (event.source as any)) {
          ;(event.source as any).postMessage({ type: 'plugin:result', id, error: e?.message || String(e) }, '*')
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

function closeWindow() {
  HidePluginWindow()
}
</script>

<template>
  <div class="plugin-window">
    <!-- 标题栏 -->
    <div class="pw-titlebar">
      <span class="pw-title">{{ pluginName || props.pluginId }}</span>
      <div class="pw-controls">
        <button class="pw-btn pw-btn-min" @click="MinimizePluginWindow()" :title="t('minimize')">
          <Minus :size="13" />
        </button>
        <button class="pw-btn pw-btn-max" @click="ToggleMaximizePluginWindow()" :title="t('maximize')">
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
        sandbox="allow-scripts allow-same-origin"
        frameborder="0"
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

/* 标题栏 */
.pw-titlebar {
  display: flex; align-items: center; justify-content: space-between;
  height: 32px; flex-shrink: 0;
  padding: 0 0 0 16px;
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
  -webkit-app-region: drag;
  user-select: none;
}
.pw-title {
  font-size: 12px; font-weight: 500;
  color: var(--color-text-secondary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.pw-controls {
  display: flex; align-items: center;
  -webkit-app-region: no-drag;
}
.pw-btn {
  display: flex; align-items: center; justify-content: center;
  width: 46px; height: 32px;
  border: none; background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background 0.1s, color 0.1s;
}
.pw-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.pw-btn-close:hover { background: #e81123; color: #fff; }
.pw-btn-max svg { transform: rotate(180deg); }

/* 内容区 */
.pw-body { flex: 1; display: flex; overflow: hidden; }
.pw-status {
  flex: 1; display: flex; align-items: center; justify-content: center;
  color: var(--color-text-disabled); font-size: 13px;
}
.pw-error { color: #E24B4A; padding: 0 20px; text-align: center; }
.pw-iframe { flex: 1; width: 100%; border: none; background: var(--color-bg-primary); }
</style>
