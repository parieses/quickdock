import { ref, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { inject } from 'vue'
import {
  ExecutePluginCommand, GetPluginFrontendPage, SetPendingPluginInit, GetAndClearPendingPluginInit, ShowPluginWindow
} from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'

export function useInlinePlugin() {
  const { t, locale } = useI18n()
  const toast = inject<ToastAPI>('toast')

  const inlinePluginId = ref<string | null>(null)
  const inlinePluginHtml = ref('')
  const inlinePluginLoading = ref(false)
  const inlinePluginError = ref('')
  const inlinePluginName = ref('')
  const inlinePluginIframe = ref<HTMLIFrameElement | null>(null)
  let inlinePluginMsgHandler: ((e: MessageEvent) => void) | null = null

  function closeInlinePlugin() {
    inlinePluginId.value = null
    inlinePluginHtml.value = ''
    inlinePluginLoading.value = false
    inlinePluginError.value = ''
    inlinePluginName.value = ''
    if (inlinePluginMsgHandler) {
      window.removeEventListener('message', inlinePluginMsgHandler)
      inlinePluginMsgHandler = null
    }
  }

  async function detachPlugin() {
    const id = inlinePluginId.value
    if (!id) return
    try {
      await ShowPluginWindow(id)
    } catch (e) {
      toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
      return
    }
    closeInlinePlugin()
  }

  async function onInlinePluginLoad() {
    const iframe = inlinePluginIframe.value
    if (!iframe?.contentWindow) return
    inlinePluginMsgHandler = (event: MessageEvent) => {
      if (event.source !== iframe.contentWindow) return
      if (event.data?.type === 'plugin:execute') {
        const { id, command, input } = event.data
        const pid = inlinePluginId.value
        if (!pid) return
        ExecutePluginCommand(pid, command, input || null).then(raw => {
          const result = unwrap(raw)
          if (event.source) {
            ;(event.source as any).postMessage(
              { type: 'plugin:result', id, data: result },
              '*'
            )
          }
        }).catch(e => {
          if (event.source) {
            ;(event.source as any).postMessage(
              { type: 'plugin:result', id, error: e?.message || String(e) },
              '*'
            )
          }
        })
      }
    }
    window.addEventListener('message', inlinePluginMsgHandler)
    try {
      const raw = await GetAndClearPendingPluginInit()
      const init = raw?.data || raw
      const text = (init && typeof init === 'object') ? (init.text || '') : (typeof init === 'string' ? init : '')
      const command = (init && typeof init === 'object') ? (init.command || '') : ''
      if (iframe.contentWindow) {
        iframe.contentWindow.postMessage({ type: 'plugin:theme', data: { theme: 'dark', locale: locale.value } }, '*')
        iframe.contentWindow.postMessage({
          type: 'plugin:init',
          data: { text, command, theme: 'dark', locale: locale.value }
        }, '*')
      }
    } catch {}
  }

  return {
    inlinePluginId, inlinePluginHtml, inlinePluginLoading, inlinePluginError,
    inlinePluginName, inlinePluginIframe, closeInlinePlugin, detachPlugin, onInlinePluginLoad
  }
}
