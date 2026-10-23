import { CopyText } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { unwrap } from './api'
import { logErr } from './logger'

/**
 * 写系统剪贴板 —— **全应用唯一入口**。
 *
 * 主路径是 Go 侧 `CopyText` → `platform.SetClipboardText`（Win32 CF_UNICODETEXT），
 * 它不依赖 WebView2 的安全上下文、文档焦点和用户激活，因此不会被 Chromium 静默拒绝。
 *
 * 为什么不再直接用 `navigator.clipboard.writeText`：浏览器侧写剪贴板要求
 * 「文档已聚焦 + 处于用户激活期」，只要其中一条不满足就抛 NotAllowedError
 * （或直接被静默拦截），宿主是嵌入式 WebView2 + 多浮窗时这条前提经常不成立
 * ——表现为「有时能复制、有时点了没反应」，且错误信息因版本而异，极难复现。
 * `execCommand('copy')` 在 WebView2 同样不可靠，还会在没写进去时返回 true。
 *
 * navigator.clipboard 只作为兜底：纯浏览器调试 / 单测环境没有 Wails 桥时才会走到。
 *
 * @returns 是否真的写入成功。调用方据此决定提示，不要再「先报成功再说」。
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  const value = text ?? ''
  try {
    unwrap(await CopyText(value))
    return true
  } catch (e) {
    // 后端已重试过（WithClipboard 内部 6 次、约 150ms 的打开重试），
    // 到这里基本是剪贴板被别的程序长时间占用，转兜底路径再试一次。
    logErr('clipboard.copy', e)
  }
  try {
    if (navigator.clipboard?.writeText && window.isSecureContext && document.hasFocus()) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch (e) {
    logErr('clipboard.copy.fallback', e)
  }
  return false
}
