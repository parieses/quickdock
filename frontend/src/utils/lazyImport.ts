// 动态 import 的统一入口（含 dev 期 vite 预构建换 hash 的自愈）。
//
// 为什么单独一个文件：CodeMirror 那一坨动态 import 原来把这段重试逻辑写在
// utils/aiCodeEditor.ts 里，但**加载 aiCodeEditor 这个模块本身**也是动态的
// （ConfigEditor / AI 侧栏 / 笔记面板都这么干）。外层那一次失败进不到内层的
// importWithRetry，于是宿主 div 永久空白 —— 日志里就是
// `Failed to fetch dynamically imported module: .../aiCodeEditor.ts?t=...`。
// 抽出来以后内外两层共用同一套路，谁失败谁自愈。
import { log } from './logger'

const DEP_RELOAD_KEY = 'quickdock.ai.editor.depReloadAt'

/** dev 期 vite 的「依赖换了 hash」类错误；浏览器对 504 的模块请求统一报 fetch 失败 */
export function isStaleDepError(e: unknown): boolean {
  const msg = e instanceof Error ? e.message : String(e ?? '')
  return /Outdated Optimize Dep|Failed to fetch dynamically imported module/i.test(msg)
}

/** 距离上次自动重载是否已经超过节流窗口（60s）。 */
export function shouldReloadForDeps(): boolean {
  try {
    const last = Number(sessionStorage.getItem(DEP_RELOAD_KEY) || 0)
    return Date.now() - last > 60_000
  } catch {
    // 隐私模式/禁用 storage：宁可不重载，也不要每次打开编辑器都刷一次页。
    return false
  }
}

/**
 * 动态 import，失败重试一次。
 *
 * dev 期的 504 是预构建（dep optimizer）重排造成的，`?v=<browserHash>` 写在编译后的
 * 说明符里——同一个说明符重试一百次也还是旧 hash，只有整页重载能换掉它。所以分两条路：
 * hash 过期 → 重载页面（带节流，免得真·离线时无限刷新）；其余错误 → 等 300ms 再试一次。
 */
export async function importWithRetry<T>(load: () => Promise<T>): Promise<T> {
  try {
    return await load()
  } catch (e) {
    log.warn('CodeEditor', 'dynamic import failed', e)
    // 只在 dev 自动重载：生产环境整页刷新会丢掉用户没保存的编辑内容，
    // 宁可降级成纯文本。dev 期的 504 纯粹是预构建换 hash，刷新无损。
    if (import.meta.env.DEV && isStaleDepError(e) && shouldReloadForDeps()) {
      try {
        sessionStorage.setItem(DEP_RELOAD_KEY, String(Date.now()))
      } catch {
        // 写不进去也能重载一次，只是失去节流保护。
      }
      log.warn('CodeEditor', 'dep hash stale, reloading page once')
      location.reload()
      // 重载已在路上，返回一个永不 resolve 的 Promise：调用方不会在页面
      // 消失前抢着渲染一版降级内容（那会闪一下纯文本编辑器）。
      return new Promise<T>(() => {})
    }
    await new Promise((r) => setTimeout(r, 300))
    return load()
  }
}
