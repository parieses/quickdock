import { inject, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExecutePluginCommand, CopyText, GetAndClearPendingPluginInit } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import type { ToastAPI } from '../types'

export interface PluginHostOptions {
  /** 取当前插件 id（inline 模式会随 inlinePluginId 变化；window 模式固定） */
  pluginId: () => string | null
  /** 直接提供 init（inline 同窗口场景，避免走全局单例 pending init） */
  getInit?: () => { text: string; command: string }
  /** 走全局 pending init（跨窗口/分离场景） */
  usePendingInit?: boolean
}

/**
 * 统一的插件 iframe 宿主桥接。
 *
 * 同时承载「内联（命令面板内）」与「独立插件窗口」两种形态，收敛原先散落在
 * useInlinePlugin.ts 与 PluginPage.vue 中的重复逻辑：
 *  - postMessage 桥接（confirm / alert / copy / execute）
 *  - nonce 防跨源伪造
 *  - init（含 pending init 或直接传入）+ theme 下发
 *  - 主题 MutationObserver 动态跟随（移除写死的 dark）
 */
export function usePluginHost(opts: PluginHostOptions) {
  const { t, locale } = useI18n()
  const toast = inject<ToastAPI>('toast')

  let messageHandler: ((e: MessageEvent) => void) | null = null
  let iframeWindow: Window | null = null
  let themeObserver: MutationObserver | null = null
  // 随机 nonce 防止跨源消息伪造（blob URL / srcdoc 下无法指定 targetOrigin）
  const pluginNonce = Math.random().toString(36).slice(2, 12)

  function currentThemeName(): string {
    return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark'
  }

  function iframePostMessage(data: any) {
    if (!iframeWindow) return
    iframeWindow.postMessage({ ...data, nonce: pluginNonce }, '*')
  }

  /** iframe onload：先发 theme，再发 init（含 nonce 消息） */
  async function onLoad(event: Event) {
    iframeWindow = (event.target as HTMLIFrameElement)?.contentWindow
    if (!iframeWindow) return
    const theme = currentThemeName()
    iframePostMessage({ type: 'plugin:theme', data: { theme, locale: locale.value } })
    let text = ''
    let command = ''
    if (opts.getInit) {
      const init = opts.getInit()
      text = init.text || ''
      command = init.command || ''
    } else if (opts.usePendingInit) {
      // 带插件 id 归属取用：匹配才消费，避免跨插件错配
      try {
        const pid = opts.pluginId()
        if (pid) {
          const raw = await GetAndClearPendingPluginInit(pid)
          text = (raw && raw[0]) || ''
          command = (raw && raw[1]) || ''
        }
      } catch {}
    }
    iframePostMessage({ type: 'plugin:init', data: { text, command, theme, locale: locale.value } })
  }

  messageHandler = async (event: MessageEvent) => {
    // 只处理当前 iframe 的消息
    if (event.source !== iframeWindow) return
    const pluginId = opts.pluginId()
    if (!pluginId) return

    // 插件对话框桥接（仅校验来源，不依赖 nonce）
    if (event.data?.type === 'plugin:confirm') {
      const { id, message } = event.data
      try {
        const ok = await toast?.confirm?.(message || '')
        event.source?.postMessage({ type: 'plugin:confirm-result', id, ok }, '*')
      } catch {
        event.source?.postMessage({ type: 'plugin:confirm-result', id, ok: false }, '*')
      }
      return
    }
    if (event.data?.type === 'plugin:alert') {
      const { id, message } = event.data
      toast?.success?.(message || '')
      event.source?.postMessage({ type: 'plugin:alert-result', id }, '*')
      return
    }

    // 插件复制：走宿主 CopyText，规避 WebView2 中 navigator.clipboard 静默失败
    if (event.data?.type === 'plugin:copy') {
      const { id, text } = event.data
      try {
        await CopyText(text || '')
        event.source?.postMessage({ type: 'plugin:copy-result', id, ok: true }, '*')
      } catch {
        event.source?.postMessage({ type: 'plugin:copy-result', id, ok: false }, '*')
      }
      return
    }

    if (event.data?.type === 'plugin:execute') {
      const { id, command, input } = event.data
      try {
        const raw = await ExecutePluginCommand(pluginId, command, input || null)
        const result = unwrap(raw)
        if (event.source && 'postMessage' in (event.source as any)) {
          iframePostMessage({ type: 'plugin:result', id, data: result })
        }
      } catch (e: any) {
        if (event.source && 'postMessage' in (event.source as any)) {
          iframePostMessage({ type: 'plugin:result', id, error: e?.message || String(e) })
        }
      }
    }
  }
  window.addEventListener('message', messageHandler)

  // 宿主主题切换时，实时重发 theme 给插件 iframe（浅色/深色跟随）
  themeObserver = new MutationObserver(() => {
    iframePostMessage({ type: 'plugin:theme', data: { theme: currentThemeName(), locale: locale.value } })
  })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })

  function cleanup() {
    if (messageHandler) {
      window.removeEventListener('message', messageHandler)
      messageHandler = null
    }
    if (themeObserver) {
      themeObserver.disconnect()
      themeObserver = null
    }
    iframeWindow = null
  }
  onUnmounted(cleanup)

  return { onLoad, currentThemeName, cleanup }
}
