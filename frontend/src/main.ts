import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { i18n } from './i18n'
import { Events } from '@wailsio/runtime'
import { ReportFrontendError } from '../bindings/quickdock/services/diag/diagservice'
import './style.css'
// 应用外壳与通用控件（页头 / 按钮 / 输入框 / 卡片）：全应用唯一实现，
// 各业务页不许再自造 xxx-header 或第 N 套按钮尺寸
import './styles/app.css'
// 设置模块公共样式（D5：从 SettingsModal.vue 抽离，供 SettingsAI/SettingsSnapshot 等无自身 style 的子组件依赖）
import './styles/settings.css'
// 工作空间三栏（场景/集合/项目）公共外观：一份类名三处共用，避免再各写一套 scoped 样式
import './styles/panels.css'
// 整页空状态引导（无场景 / 无集合两档共用）
import './styles/guide.css'

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

// dev 期「依赖预构建换哈希」的自愈：Vite 每次（重新）启动都会重跑 optimizeDeps 并换一个
// browserHash，而已经加载完的模块图里还留着 `/node_modules/.vite/deps/xxx.js?v=旧哈希`
// 的地址。之后第一次打开的重型页面（文件搜索 / AI / SSH）一拉懒加载 chunk 就被
// dev server 以 504（Outdated Optimize Dep）拒掉，页面半空白，只有手动刷新能救。
// 这里一次性自动刷新：只跑 DEV、且 15 秒内只刷一次，避免「刷完还是坏」变成死循环。
// 生产没有 dep 优化器，所以整块包在 DEV 判断里。
const STALE_DEP_RELOAD_KEY = 'qd.staleDepReloadAt'
const STALE_DEP_RE = /Outdated Optimize Dep|Failed to fetch dynamically imported module|Importing a module script failed/i
function reloadOnStaleDep(msg: string): boolean {
  if (!STALE_DEP_RE.test(msg)) return false
  const now = Date.now()
  let last = 0
  try { last = Number(sessionStorage.getItem(STALE_DEP_RELOAD_KEY) || 0) } catch { /* 隐私模式：当作没刷过 */ }
  if (now - last < 15000) return true // 刚刷过又坏：不再刷，交给调用方上报
  try { sessionStorage.setItem(STALE_DEP_RELOAD_KEY, String(now)) } catch { /* 记不住就退化成每次都刷，可接受 */ }
  location.reload()
  return true
}
if (import.meta.env.DEV) {
  // Vite 官方的预加载失败钩子：不 preventDefault 的话它还会再抛一次 unhandledrejection
  window.addEventListener('vite:preloadError', (e: any) => {
    e?.preventDefault?.()
    reloadOnStaleDep(String(e?.payload?.message || e?.payload || 'Failed to fetch dynamically imported module'))
  })
}

window.addEventListener('error', (e: ErrorEvent) => {
  const err = e.error as any
  const msg = e.message || String(err || 'unknown')
  // 懒加载 chunk 拉不到时，浏览器也会抛一个 error 事件（上面那条 504 就是从这里来的）
  if (import.meta.env.DEV && reloadOnStaleDep(msg)) return
  reportFrontendError('error', msg, err?.stack || '')
})

window.addEventListener('unhandledrejection', (e: PromiseRejectionEvent) => {
  const r: any = e.reason
  const msg = r?.message || String(r)
  // 先试自愈（会触发刷新），刷新请求发出后这次上报就不必了：页面马上就没了
  if (import.meta.env.DEV && reloadOnStaleDep(msg)) return
  reportFrontendError('unhandledrejection', msg, r?.stack || '')
})

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(i18n)

// Vue 组件渲染 / 生命周期抛错（window.onerror 不一定捕获得到）
//
// 把出错组件名并入 label：Vue 打包后的堆栈只到 vue.runtime，不含组件名，
// 否则崩溃记录只能写「vue:render function」，没法定位是哪个组件炸的。
// 从实例取 dev 阶段 SFC 编译器塞的 __name（生产构建可能被剥掉，退化为 options.name）。
function vueComponentName(vm: any): string {
  const t = vm?.$?.type
  const name = (t && (t.__name || t.name)) || vm?.$options?.name || ''
  return typeof name === 'string' && name ? name : ''
}

app.config.errorHandler = (err: any, vm: any, info: string) => {
  const msg = err?.message || String(err)
  // defineAsyncComponent 的加载失败被 Vue 自己接住了（不会冒到 unhandledrejection），
  // 于是「切到某个懒加载页面时 chunk 504」只会走到这里 —— 同一个自愈入口一并处理
  if (import.meta.env.DEV && reloadOnStaleDep(msg)) return
  const name = vueComponentName(vm)
  const label = name ? `vue:${info} (${name})` : 'vue:' + info
  reportFrontendError(label, msg, err?.stack || '')
}

// 渲染进程看门狗：宿主每 5s 经 qd:heartbeat 广播一次；若可见窗口连续 ~30s 收不到心跳，
// 说明 WebView2 渲染/桥接已卡死（日志表现为 "Eval failed: not in correct state"），
// 自动重载页面自我恢复，避免"时间一长页面空白"需手动重启。
// 隐藏/最小化窗口不触发，避免误重载；启动留 20s 宽限等首个心跳到达。
let lastHeartbeat = Date.now()
const watchdogStart = Date.now()
let sshWatchdogWarned = false
Events.On('qd:heartbeat', () => {
  lastHeartbeat = Date.now()
  sshWatchdogWarned = false
})
setInterval(() => {
  if (document.visibilityState !== 'visible') return
  if (Date.now() - watchdogStart < 20000) return
  if (Date.now() - lastHeartbeat > 30000) {
    // SSH 终端窗口豁免自动重载：location.reload() 会清空 xterm 的屏幕与滚动历史，
    // 正在 tail 的日志、跑到一半的命令当场消失，而 Go 侧的会话其实还活着——
    // 「画面没了但连接还在」比卡住更难向用户解释。
    // 代价是失去自愈能力，所以这里至少把事件落进崩溃日志，便于事后定位；
    // 等 #12 的回放接进 UI（重开窗口可还原画面）后再考虑恢复重载。
    if (window.location.hash === '#/ssh') {
      if (!sshWatchdogWarned) {
        sshWatchdogWarned = true
        reportFrontendError('watchdog', 'SSH 窗口 30s 未收到心跳（已跳过自动重载）', '')
      }
      return
    }
    location.reload()
  }
}, 5000)

app.mount('#app')
