import { createApp } from 'vue'
import { i18n } from './i18n'
import PluginPage from './components/PluginPage.vue'
import { Events } from '@wailsio/runtime'
import { ReportFrontendError } from '../bindings/quickdock/services/diag/diagservice'
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
