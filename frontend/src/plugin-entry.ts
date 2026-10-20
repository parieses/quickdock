import { createApp } from 'vue'
import { i18n } from './i18n'
import PluginPage from './components/PluginPage.vue'
import { Events } from '@wailsio/runtime'
import { ReportFrontendError } from '../bindings/quickdock/services/diag/diagservice'
import { GetValue, ApplyTheme } from '../bindings/quickdock/services/appservice'
import { unwrap } from './utils/api'
import './style.css'

// 轻量入口：插件独立窗口只挂载 PluginPage（标题栏 + 插件 iframe 宿主），
// 不加载主 SPA（命令面板 / 侧边栏 / 环境页 / 各业务页面等），首次打开大幅提速。
// 复用既有 i18n 实例、全局样式与宿主桥（usePluginHost / PluginFrame 已独立于主 App）。

function reportFrontendError(kind: string, message: string, stack: string) {
  try {
    ReportFrontendError(kind, message, stack || '', location.href)
  } catch {
    /* 忽略：上报本身不能抛错 */
  }
}
window.addEventListener('error', (e: ErrorEvent) => {
  const err = e.error as any
  reportFrontendError('error', e.message || String(err || 'unknown'), err?.stack || '')
})
window.addEventListener('unhandledrejection', (e: PromiseRejectionEvent) => {
  const r: any = e.reason
  reportFrontendError('unhandledrejection', r?.message || String(r), r?.stack || '')
})

// 插件 id 经窗口 URL 的 query 传入：/plugin.html?id=<id>
const params = new URLSearchParams(location.search)
const pluginId = params.get('id') || ''

// ---- 主题对齐（独立插件窗口专属）----
// 主窗口的 App.vue.applyTheme 把 <html data-theme> 写成唯一真相，并
// Events.Emit('quickdock:theme-changed') 广播给所有 WebView 窗口。但独立插件窗口
// 早先并不订阅这个广播，于是出现一个很难复现的问题：
//   usePluginHost 里的 themeObserver 观察的是**本窗口**的 documentElement，
//   而本窗口的 <html> 从来没人写 data-theme（主题实际写在 iframe 里，
//   由宿主 BRIDGE_SCRIPT 设置）→ observer 永远不触发 →
//   用户在主窗口切浅/深色，已经开着的插件独立窗口毫无反应。
// 这里补上两件事：启动时先对齐一次，之后订阅广播持续对齐。
const prefersDark = window.matchMedia('(prefers-color-scheme: dark)')

function applyThemeName(theme: 'dark' | 'light' | 'system') {
  const isDark = theme === 'dark' || (theme === 'system' && prefersDark.matches)
  document.documentElement.setAttribute('data-theme', isDark ? 'dark' : 'light')
  // 让原生窗口（标题栏）也跟着切；失败不影响 Web 侧主题
  try { ApplyTheme(isDark) } catch (_) {}
}

// 启动即对齐：先按系统偏好落一版（避免默认浅色闪一下），再读持久化偏好纠正。
applyThemeName('system')
GetValue('theme')
  .then((r) => {
    const saved = unwrap<string>(r)
    if (saved === 'dark' || saved === 'light' || saved === 'system') applyThemeName(saved)
  })
  .catch(() => {})

// 订阅广播：主窗口切主题时本窗口同步，从而触发 usePluginHost 的 themeObserver，
// 把新的 theme 重新下发给插件 iframe。
Events.On('quickdock:theme-changed', (e: { data?: unknown }) => {
  const t = e?.data as string | undefined
  if (t === 'dark' || t === 'light' || t === 'system') applyThemeName(t)
})
// 系统偏好变化时（theme=system 的情况下）也要跟上
if (prefersDark.addEventListener) {
  prefersDark.addEventListener('change', () => {
    GetValue('theme')
      .then((r) => {
        const saved = unwrap<string>(r)
        if (saved === 'system' || !saved) applyThemeName('system')
      })
      .catch(() => {})
  })
}

const app = createApp(PluginPage, { pluginId })
app.use(i18n)
app.config.errorHandler = (err: any, _vm, info: string) => {
  reportFrontendError('vue:' + info, err?.message || String(err), err?.stack || '')
}

// 渲染进程看门狗（与主窗口一致）：宿主每 5s 经 qd:heartbeat 广播，连续 ~30s 收不到
// 说明 WebView2 渲染/桥接卡死，自动重载自我恢复，避免"时间一长页面空白"。
let lastHeartbeat = Date.now()
const watchdogStart = Date.now()
Events.On('qd:heartbeat', () => {
  lastHeartbeat = Date.now()
})
setInterval(() => {
  if (document.visibilityState !== 'visible') return
  if (Date.now() - watchdogStart < 20000) return
  if (Date.now() - lastHeartbeat > 30000) {
    location.reload()
  }
}, 5000)

app.mount('#app')
