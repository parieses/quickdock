<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { GetPluginFrontendPage } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { injectPluginBridge } from '../utils/pluginBridge'
import { usePluginHost } from '../composables/usePluginHost'

const props = defineProps<{
  pluginId: string
  /** 直接传入 init（inline 同窗口场景；避免走全局 pending init 单例） */
  getInit?: () => { text: string; command: string }
  /** 改用全局 pending init（独立窗口 / 分离场景） */
  usePendingInit?: boolean
}>()

const emit = defineEmits<{ (e: 'title', title: string): void }>()

const { t, locale } = useI18n()
const loading = ref(true)
const error = ref('')
const iframeSrc = ref('')
let blobUrl: string | null = null

const { onLoad } = usePluginHost({
  pluginId: () => props.pluginId,
  getInit: props.getInit,
  usePendingInit: props.usePendingInit,
})

function currentThemeName(): string {
  return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark'
}

onMounted(async () => {
  try {
    const html = unwrap<string>(await GetPluginFrontendPage(props.pluginId, currentThemeName(), locale.value))
    if (!html) {
      error.value = t('pluginNoFrontend')
      loading.value = false
      return
    }
    const titleMatch = html.match(/<title>([^<]*)<\/title>/)
    if (titleMatch) emit('title', titleMatch[1])
    const blob = new Blob([injectPluginBridge(html)], { type: 'text/html;charset=utf-8' })
    blobUrl = URL.createObjectURL(blob)
    iframeSrc.value = blobUrl
    loading.value = false
  } catch (e: any) {
    error.value = t('pluginLoadFailed') + ': ' + (e?.message || String(e))
    loading.value = false
  }
})

onUnmounted(() => {
  if (blobUrl) URL.revokeObjectURL(blobUrl)
  blobUrl = null
})
</script>

<template>
  <div class="pf-root">
    <div v-if="loading" class="pf-status">{{ t('loading') }}</div>
    <div v-else-if="error" class="pf-status pf-error">{{ error }}</div>
    <iframe
      v-else
      :src="iframeSrc"
      class="pf-iframe"
      sandbox="allow-scripts allow-modals allow-downloads"
      frameborder="0"
      @load="onLoad"
    />
  </div>
</template>

<style scoped>
.pf-root {
  flex: 1;
  display: flex;
  overflow: hidden;
  min-height: 0;
}
.pf-status {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--color-text-disabled);
  font-size: 13px;
  user-select: none;
}
.pf-error {
  color: var(--color-danger);
  padding: 0 24px;
  text-align: center;
  line-height: 1.6;
}
.pf-iframe {
  flex: 1;
  width: 100%;
  border: none;
  background: var(--color-bg-primary);
}
</style>
