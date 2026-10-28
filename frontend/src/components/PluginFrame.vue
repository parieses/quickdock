<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { GetPluginFrontendPage } from '../../bindings/quickdock/services/plugin/pluginservice'
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

// 模块级缓存：同一插件 + 主题 + 语言的前端页 HTML 只让后端构建一次，
// 之后 remount / reload 直接复用，避免每次都打 GetPluginFrontendPage（插件前端构建偏重）。
const pluginFrontendCache = new Map<string, string>()
function frontendCacheKey(id: string, theme: string, loc: string): string {
  return `${id}:${theme}:${loc}`
}

function currentThemeName(): string {
  return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark'
}

async function buildHtml(): Promise<string | null> {
  const theme = currentThemeName()
  const loc = locale.value
  const key = frontendCacheKey(props.pluginId, theme, loc)
  const cached = pluginFrontendCache.get(key)
  if (cached) return cached
  const html = unwrap<string>(await GetPluginFrontendPage(props.pluginId, theme, loc))
  if (html) pluginFrontendCache.set(key, html)
  return html
}

async function loadFrontend() {
  loading.value = true
  error.value = ''
  // 重置前先释放旧 blob URL，避免内存泄漏（旧 iframe 即将被替换）
  if (blobUrl) {
    URL.revokeObjectURL(blobUrl)
    blobUrl = null
  }
  try {
    const html = await buildHtml()
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
}

// 刷新/重置：重新拉取前端页并重建 iframe，使插件页回到初始状态
// （窗口与后端进程保持复用，符合隐藏-复用设计）。
function reload() {
  loadFrontend()
}

onMounted(loadFrontend)

onUnmounted(() => {
  if (blobUrl) URL.revokeObjectURL(blobUrl)
  blobUrl = null
})

defineExpose({ reload })
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
