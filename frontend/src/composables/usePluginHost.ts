import { inject, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExecutePluginCommand, CopyText, GetAndClearPendingPluginInit, PickFilePath, ReadPickedFile } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { Dialogs } from '@wailsio/runtime'
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
    if (event.data?.type === 'plugin:esc') {
      // iframe 内 Esc 经桥转发到此：iframe 抢焦点后父级收不到 keydown，
      // 用宿主级 CustomEvent 通知所在上下文（命令面板内联插件 → 关闭插件页）。
      window.dispatchEvent(new CustomEvent('qd:plugin-esc'))
      return
    }
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

    // 插件原生选文件：走宿主 PickFilePath（Wails Dialog），规避 iframe 沙箱
    // 与宿主窗口失焦问题；取消或失败回传 null
    if (event.data?.type === 'plugin:pickfile') {
      const { id, title, filter, pattern } = event.data
      try {
        const path = unwrap<string>(await PickFilePath(title || '', filter || '', pattern || ''))
        event.source?.postMessage({ type: 'plugin:pickfile-result', id, path: path || null }, '*')
      } catch {
        event.source?.postMessage({ type: 'plugin:pickfile-result', id, path: null }, '*')
      }
      return
    }

    // 插件读取选中文件内容（配合 qdPickFile；文本/图片文本自动嗅探）
    if (event.data?.type === 'plugin:readfile') {
      const { id, path } = event.data
      try {
        const payload = unwrap<{ type: string; content: string }>(await ReadPickedFile(path || ''))
        event.source?.postMessage({ type: 'plugin:readfile-result', id, payload }, '*')
      } catch {
        event.source?.postMessage({ type: 'plugin:readfile-result', id, payload: null }, '*')
      }
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

      // 拦截宿主 dialog 命令，直接在 frontend 调用 Wails Dialog API
      if (command === 'host.dialog.open') {
        try {
          const p = input || {}
          const multiple = !!p.multiple
          const filters = (p.filters || []).map((f: any) => ({
            DisplayName: f.name || f.pattern,
            Pattern: f.pattern || '*.*'
          }))
          const result = await Dialogs.OpenFile({
            Title: p.title || '选择文件',
            Filters: filters.length > 0 ? filters : undefined,
            AllowsMultipleSelection: multiple
          })
          if (multiple) {
            const paths = Array.isArray(result) ? result : (result ? [result] : [])
            iframePostMessage({
              type: 'plugin:result',
              id,
              data: { canceled: paths.length === 0, multiple: true, paths }
            })
          } else {
            const paths = typeof result === 'string' ? [result] : (result || [])
            iframePostMessage({
              type: 'plugin:result',
              id,
              data: paths.length > 0 ? { canceled: false, path: paths[0] } : { canceled: true, path: '' }
            })
          }
          return
        } catch (e: any) {
          iframePostMessage({
            type: 'plugin:result',
            id,
            data: { canceled: true, path: '' }
          })
          return
        }
      }

      if (command === 'host.dialog.save') {
        try {
          const p = input || {}
          const filters = (p.filters || []).map((f: any) => ({
            DisplayName: f.name || f.pattern,
            Pattern: f.pattern || '*.*'
          }))
          const result = await Dialogs.SaveFile({
            Title: p.title || '保存文件',
            Filename: p.defaultName || '',
            Filters: filters.length > 0 ? filters : undefined
          })
          iframePostMessage({
            type: 'plugin:result',
            id,
            data: result ? { canceled: false, path: result } : { canceled: true, path: '' }
          })
          return
        } catch (e: any) {
          iframePostMessage({
            type: 'plugin:result',
            id,
            data: { canceled: true, path: '' }
          })
          return
        }
      }

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

  // 切换界面语言时同步重发 locale：语言变更不产生 DOM 属性变化，
  // MutationObserver 感知不到，已打开的插件页（如 pdf-toolkit 的 onChange 重渲染）会停留在旧语言
  const stopLocaleWatch = watch(locale, () => {
    iframePostMessage({ type: 'plugin:theme', data: { theme: currentThemeName(), locale: locale.value } })
  })

  function cleanup() {
    stopLocaleWatch()
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
