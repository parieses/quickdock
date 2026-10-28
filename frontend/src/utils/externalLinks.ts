/**
 * 外链打开的统一出口：一律交给系统默认浏览器，绝不在 WebView 里导航。
 *
 * 背景（2026-10-28 修）：AI 回答 / markdown 预览里渲染出的 <a href> 是普通链接，
 * 点一下 WebView 就在**当前窗口**导航过去，把整个应用界面替换掉。主界面是单页
 * 应用，没有地址栏也没有后退按钮，用户回不来，只能重启软件。
 *
 * 站点面板、命令面板、设置页早就是这个约定（Browser.OpenURL），只是 markdown
 * 渲染出来的内容一直没人接管：它由 marked 直接产出 HTML、经 DOMPurify 后 v-html
 * 注入，不经过任何组件的 @click，所以在单个组件里加处理必然漏（对话页、AI 面板、
 * 笔记预览是三处独立渲染入口）。这里改成 document 级事件委托，一次安装全站生效。
 *
 * 为什么不做「应用内新窗口 / 模态框」：
 *   - 模态框只能靠 iframe 承载第三方页面，大量站点带 X-Frame-Options /
 *     CSP frame-ancestors，嵌进去是白屏，且没有地址栏与后退；
 *   - 独立 WebView 窗口要再背一套生命周期（关闭隐藏、位置记忆、失焦、多窗口
 *     进程开销），而用户点外链就是想用自己那个有书签、有密码、有多标签的浏览器。
 */
import { Browser } from '@wailsio/runtime'
import { logErr } from './logger'

/**
 * 是否是可交给浏览器的地址：只认**原始写法**就是绝对 http(s) 的链接。
 *
 * 用 getAttribute 而不是 a.href 判断，是因为 a.href 会把相对路径解析成当前 origin
 * 下的绝对地址，那样 hash 路由（#/settings）在开发模式下会被误判成外链。
 */
export function isExternalURL(raw: string): boolean {
  return /^https?:\/\//i.test((raw || '').trim())
}

/**
 * 用系统默认浏览器打开 url。
 * 非 http(s) 协议静默忽略（调用方不必再判一次）；失败抛给调用方决定怎么提示。
 */
export async function openExternal(url: string): Promise<void> {
  const u = (url || '').trim()
  if (!isExternalURL(u)) return
  await Browser.OpenURL(u)
}

/**
 * 判定某个 <a> 的**原始 href** 是否该交给系统浏览器。
 *
 * 抽成纯函数是为了能单测：判定口径一旦回退（例如改成用 a.href 判断，hash 路由
 * 在开发模式下就会被解析成 http://localhost:5173/#/xxx 而误判为外链），
 * 表现是「点一下内部导航就跑出去开浏览器」，而这类 bug 人工很难每次都回归。
 *
 * 站内地址不拦：开发模式下 vite 自身资源、以及生产环境同 origin 的相对路径。
 */
export function shouldHandOverLink(rawHref: string, currentOrigin: string): boolean {
  if (!isExternalURL(rawHref)) return false
  let origin = ''
  try {
    origin = new URL(rawHref.trim()).origin
  } catch {
    return false // 解析不了就别拦，交给浏览器默认行为
  }
  return !(origin && currentOrigin && origin === currentOrigin)
}

let guardInstalled = false

/**
 * 安装全局外链拦截（幂等：同一窗口内 Vue 可能重复挂载/热重载）。
 *
 * 只拦「左键 / Ctrl+点击 / 中键 / 回车」触发的 http(s) 绝对链接；下列情况放行：
 *   - 组件已在自己的 @click 里 preventDefault（说明这下点击已被接管，例如
 *     「服务未启动时不让点端口链接」），否则会重复打开一次；
 *   - <a download>（下载，不是导航）；
 *   - 站内地址（origin 与当前页相同，开发模式下 vite 自身的资源）。
 *
 * onError 用于弹 toast，需由组件传入——本模块在 App.vue 里安装，拿不到 toast 实例。
 */
export function installExternalLinkGuard(onError?: (url: string, e: unknown) => void): void {
  if (guardInstalled) return
  guardInstalled = true

  const handler = (e: Event) => {
    if (e.defaultPrevented) return
    const el = e.target as HTMLElement | null
    const a = el?.closest?.('a[href]') as HTMLAnchorElement | null
    if (!a || a.hasAttribute('download')) return
    const raw = a.getAttribute('href') ?? ''
    if (!shouldHandOverLink(raw, window.location.origin)) return
    e.preventDefault()
    openExternal(a.href).catch((err: unknown) => {
      logErr('externalLinks.guard', err)
      onError?.(a.href, err)
    })
  }

  // click 覆盖左键 / Ctrl+点击 / 键盘回车；auxclick 覆盖中键（默认新标签打开，同样要接管）
  document.addEventListener('click', handler)
  document.addEventListener('auxclick', handler)
}
