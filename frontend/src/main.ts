import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { i18n } from './i18n'
import { ReportFrontendError } from '../bindings/quickdock/services/diag/diagservice'
import './style.css'

// 前端异常落盘：白屏 / 交互报错时，用户可在「设置 > 关于」的崩溃记录里看到现场，
// 不必开 DevTools（打包版根本没有 DevTools）。
// 上报经宿主绑定写入 <数据目录>/logs/crash/js-*.log；失败静默——上报本身不能再抛错。
function reportFrontendError(kind: string, message: string, stack: string) {
  try {
    ReportFrontendError(kind, message, stack || '', location.href)
  } catch {
    /* 忽略：不能因上报失败影响页面 */
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

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(i18n)

// Vue 组件渲染 / 生命周期抛错（window.onerror 不一定捕获得到）
app.config.errorHandler = (err: any, _vm, info: string) => {
  reportFrontendError('vue:' + info, err?.message || String(err), err?.stack || '')
}

app.mount('#app')
