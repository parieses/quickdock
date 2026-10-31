<script lang="ts" setup>
import { computed, defineAsyncComponent, nextTick, onMounted, provide, ref, watch } from 'vue';
import { Events } from '@wailsio/runtime';
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from './stores/workspace';
import { useToast } from './composables/useToast';
import { GetValue, SetValue, ApplyTheme } from '../bindings/quickdock/services/appservice'
import {
  OpenDSHWindow,
  DetectNodeEnv,
  SetupDSH,
} from '../bindings/quickdock/services/dsh/dshservice';
import { OpenSshWindow } from '../bindings/quickdock/services/sshx/sshservice'
import { i18n } from './i18n';
import { unwrap } from './utils/api';
import { logErr } from './utils/logger';
import { getErrorMessage } from './utils/error';
import { installExternalLinkGuard } from './utils/externalLinks';
import { navigate, pendingOpenNoteId, pendingOpenTodoId, setNavigator } from './composables/bridge';
import Sidebar from './components/Sidebar.vue';
import NotePanel from './components/NotePanel.vue';
import Toast from './components/Toast.vue';
import ConfirmDialog from './components/ConfirmDialog.vue';
import { SkipUpdate } from '../bindings/quickdock/services/update/updateservice';
// 页面注册表：主窗口内容页与侧边栏导航项的单一数据源（替代 currentPage 手动 switch）
import { findContentPage, findNavEntry, KEEP_ALIVE_PAGES, type PageContext } from './pages/registry'

// 异步加载的页面级组件（仅在主窗口中用，减少独立窗口的加载体积）
const SettingsModal = defineAsyncComponent(() => import('./components/SettingsModal.vue'))
// 主窗口内容页（含工作空间复合页 WorkspacePage）的异步组件与导航信息已收敛到
// pages/registry，由注册表驱动渲染，替代手工 v-if 切换链。
const CommandPalette = defineAsyncComponent(() => import('./components/CommandPalette.vue'))
// 主窗口内容页（notes/plugins/todo/schedule/monitor/ai/environments/clipboard）的
// 异步组件与导航信息已收敛到 pages/registry，由注册表驱动渲染，替代手工 v-if 切换链。
const PluginPage = defineAsyncComponent(() => import('./components/PluginPage.vue'))
const WindowManager = defineAsyncComponent(() => import('./components/WindowManager.vue'))
const UpdatePrompt = defineAsyncComponent(() => import('./components/UpdatePrompt.vue'))
// SSH 终端窗口专用页：只在该独立窗口内加载，不进主窗口体积
const SshPage = defineAsyncComponent(() => import('./components/SshPage.vue'))
// 剪贴板面板：独立剪贴板窗口（#/clipboard）与主窗口内容页共用它。
// 这里必须也是动态 import，不能静态 import —— 静态引用会把它无条件编进主 chunk，
// 于是 ① pages/registry 里的动态 import 拆不出独立 chunk（Vite 报 INEFFECTIVE_DYNAMIC_IMPORT），
// ② 笔记窗口、命令面板窗口、窗口管理浮窗也各自白载一份剪贴板面板代码。
// 两处用的是同一个 import specifier，Rollup 会把它们合并成同一个 chunk，不会重复打包。
const ClipboardPanel = defineAsyncComponent(() => import('./components/ClipboardPanel.vue'))

document.title = i18n.global.t('appName');
watch(() => i18n.global.locale.value, () => {
  document.title = i18n.global.t('appName');
});

const store = useWorkspaceStore();
const { t } = useI18n()
const { items, remove, error, success, confirm, confirmItems, resolveConfirm } = useToast();
const showSettings = ref(false);
const settingsPage = ref<string | undefined>(undefined);
// 后台自动更新提示弹窗状态
const updatePromptVisible = ref(false);
const updatePromptData = ref<{ version: string; currentVersion: string; releaseNotes: string } | null>(null);
const updatePromptHandled = new Set<string>();
// 环境管理定位区块：侧边栏点击 harness 时，让 EnvironmentPage 自动选中 harness 区块
const envInitialSection = ref<string | undefined>(undefined);

// 页面路由
const currentPage = ref('workspace')
// 打开设置模态框（供 AIPage 等注册页经注册表事件回调复用，替代模板内联箭头）
function openSettings(page?: string) {
  settingsPage.value = page
  showSettings.value = true
}
// 页面注册表运行期上下文：内容页拿它算动态 props/events，非内容页入口（ssh/dsh）
// 拿它执行 activate。注册表只依赖这个接口，不依赖 App 内部实现。
// openDSH / openSsh 是函数声明（提升），此处引用安全；真正调用发生在用户点击之后。
const pageContext: PageContext = {
  envInitialSection,
  openSettings,
  openDsh: () => openDSH(),
  openSsh: () => openSsh(),
}
const currentContentPage = computed(() => findContentPage(currentPage.value))

// 导航分发：条目自己声明点击行为 —— 有 activate 就执行它（唤起独立窗口），否则切页。
// 这里刻意不认识 dsh / ssh：之前它们是两条 if 分支，等于把「哪些 id 不是内容页」
// 这份知识同时写在注册表和 App 里，加一个窗口入口就要改两处。
function setPage(page: string) {
  const entry = findNavEntry(page)
  if (entry?.activate) {
    void entry.activate(pageContext)
    return
  }
  currentPage.value = page
}

// 注册跨页面导航器（笔记/剪贴板等独立视图 → 侧边栏页面），供 bridge 使用。
// 走 setNavigator 而非直接赋值：bridge 那边会在导航器缺失时排队，注册时补发，
// 于是「事件早于 App setup 到达」不再是一种需要靠执行顺序兜住的情况。
setNavigator((page: string) => {
  if (window.location.hash) window.location.hash = ''
  currentPage.value = page
})

// 工作空间的四档状态（引导 / 无场景 / 无集合 / 三栏）已随布局一起搬到
// components/WorkspacePage.vue —— 它们与布局强相关，留在 App 只会让内容区
// 恢复成「注册页 + 一支特例分支」两套写法。

// 从侧边栏点 DeepSeek：先探测环境——缺 node/dsh 自动安装并打开设置页展示进度；已就绪直接开原生窗口
interface EnvProbe { nodeFound?: boolean; dshInstalled?: boolean }
const dshOpening = ref(false)
// 本会话内已确认 node+dsh 就绪后不再重复探测：DetectNodeEnv() 每次会 spawn node/npx 子进程
// 测版本（~1s），dsh 已装好的情况下纯属浪费。后端 Start() 仍会兜底检查入口存在。
const dshEnvReady = ref(false)
async function openDSH() {
  if (dshOpening.value) return // 防连点：首启可能耗时数秒，避免开多个窗口
  dshOpening.value = true
  try {
    if (!dshEnvReady.value) {
      const st = unwrap<EnvProbe | null>(await DetectNodeEnv())
      if (!st?.nodeFound || !st.dshInstalled) {
        // 新电脑：node+dsh 一起自动装；装了 node 没装 dsh：只补 dsh
        // harness 安装配置已迁入「环境管理」，这里直接打开环境管理的 harness 区块展示进度
        currentPage.value = 'environments'
        // 先复位再置位，确保重复点击侧栏 harness 时 EnvironmentPage 的 watch 能再次触发定位
        envInitialSection.value = undefined
        await nextTick()
        envInitialSection.value = 'harness'
        success(t('dshSettingUp'))
        await new Promise(r => setTimeout(r, 150)) // 等环境管理 harness 区块挂载并订阅进度事件
        // 注册监听（done/error 后用返回的 off 自行移除，防泄漏）。
        // 注意：不要用 Events.Off('quickdock:dsh:progress') 全局移除——
        // SettingsDSH 页也注册了同事件监听，全局 Off 会一并杀掉它导致 harness 安装进度丢失。
        const off = Events.On('quickdock:dsh:progress', async (payload: any) => {
          const p = (payload?.data ?? payload) as { stage: string; message?: string }
          if (p?.stage === 'done') {
            off()
            dshEnvReady.value = true
            success(t('dshLaunching'))
            try {
              unwrap(await OpenDSHWindow())
            } catch (e: any) {
              error(getErrorMessage(e))
            }
          } else if (p?.stage === 'error') {
            off()
            error(p.message || t('dshError'))
          }
        })
        unwrap(await SetupDSH())
        return
      }
      dshEnvReady.value = true
    }
    success(t('dshLaunching'))
    unwrap(await OpenDSHWindow())
  } catch (e: any) {
    dshEnvReady.value = false // 环境可能已变化（如 dsh 被卸载），下次点击重新探测
    error(getErrorMessage(e))
    // 出错时同样回退到环境管理的 harness 区块，让用户手动重试
    currentPage.value = 'environments'
    envInitialSection.value = undefined
    await nextTick()
    envInitialSection.value = 'harness'
  } finally {
    dshOpening.value = false
  }
}

provide('toast', { error, success, confirm });

// 全局外链拦截：AI 回答 / markdown 预览里的 <a href> 由 v-html 直接注入 DOM，
// 不经过任何组件，只有 document 级委托才拦得住（否则点击会把整个界面导航走、没法返回）。
// 装在 App.vue 是因为主窗口与剪贴板 / 笔记 / SSH 等独立窗口都挂载它，一处覆盖全部。
// 细节与选型理由见 utils/externalLinks.ts。
installExternalLinkGuard((_url, e) => error(getErrorMessage(e)));

// 侧栏「SSH 终端」：唤起独立窗口（窗口由 Go 侧懒创建；已存在就 Show+Focus 复用）。
// 与 DSH 不同，这里不含任何进程启动，打开是瞬时的，所以不做 loading 态。
async function openSsh() {
  try {
    unwrap(await OpenSshWindow())
  } catch (e: any) {
    error(getErrorMessage(e))
  }
}

// ---- 窗口类型检测 ----
// 使用 ref 来使 hash 变化可响应
const hashRef = ref(window.location.hash)
window.addEventListener('hashchange', () => {
  hashRef.value = window.location.hash
})

const isClipboardWindow = computed(() => hashRef.value === '#/clipboard')
const isNoteWindow = computed(() => hashRef.value === '#/note')
const isWinmgrWindow = computed(() => hashRef.value === '#/window-manager')
const isPaletteWindow = computed(() => hashRef.value === '#/command-palette')
// SSH 终端独立窗口（#/ssh）：与其它独立窗口不同，它有边框、可缩放、失焦不隐藏，
// 见 windows.go 的 initSshWindow。
const isSshWindow = computed(() => hashRef.value === '#/ssh')
const isPluginWindow = computed(() => {
  return hashRef.value.startsWith('#/plugin')
})
const pluginWindowId = computed(() => {
  const m = hashRef.value.match(/^#\/plugin\/([^?]+)/)
  return m ? m[1] : null
})
type Theme = 'dark' | 'light' | 'system'
const currentTheme = ref<Theme>('system')
const prefersDark = window.matchMedia('(prefers-color-scheme: dark)')

function applyTheme(theme: Theme) {
  const isDark = theme === 'dark' || (theme === 'system' && prefersDark.matches)
  document.documentElement.setAttribute('data-theme', isDark ? 'dark' : 'light')
  // 同步到原生窗口：主窗口（系统原生标题栏）跟随 App 主题，插件窗口底色跟随主题
  try { ApplyTheme(isDark) } catch (e) { logErr('App', e) }
}

async function setTheme(theme: Theme) {
  currentTheme.value = theme
  applyTheme(theme)
  try { await SetValue('theme', theme) } catch (e) { logErr('App', e) }
  // 广播到所有 WebView 窗口（主窗口 + 各浮窗），让它们各自的 document 同步切换主题
  try { Events.Emit('quickdock:theme-changed', theme) } catch (e) { logErr('App', e) }
}

// 监听系统主题变化
prefersDark.addEventListener('change', () => {
  if (currentTheme.value === 'system') applyTheme('system')
})

onMounted(async () => {
  await store.initialize();
  // 读取已保存的主题
  try {
    const saved = unwrap<string>(await GetValue('theme'))
    if (saved === 'dark' || saved === 'light' || saved === 'system') {
      currentTheme.value = saved as Theme
    }
  } catch (e) { logErr('App', e) }
  applyTheme(currentTheme.value)
  // 读取已保存的语言设置
  try {
    const saved = unwrap<string>(await GetValue('locale'))
    if (saved === 'en-US' || saved === 'zh-CN') {
      i18n.global.locale.value = saved
    }
  } catch (e) { logErr('App', e) }
  // 跨 WebView 广播：主窗口切主题时，所有浮窗同步（每个窗口独立应用）
  Events.On('quickdock:theme-changed', (payload: any) => {
    const t = (typeof payload === 'string' ? payload : payload?.data) as Theme
    if (t === 'dark' || t === 'light' || t === 'system') {
      currentTheme.value = t
      applyTheme(t)
    }
  })
  // 托盘「打开设置」：显示主窗口后由 Go 侧发事件，这里打开设置模态框。
  // 载荷可带目标页签（命令面板的备忘单「去修改」会传 'hotkeys'），Go 侧不带载荷时回落到默认页。
  Events.On('app:open-settings', (ev: any) => {
    const raw = ev?.data ?? ev
    settingsPage.value = typeof raw === 'string' && raw ? raw : undefined
    showSettings.value = true
  })
  // 浮窗（剪贴板 / SSH）里「问 AI」发现没配 Key 时，Go 侧唤起主窗口并发本事件，
  // 这里直接定位到 AI 配置页 —— 用户无需再自己找入口。
  Events.On('app:open-ai-settings', () => {
    settingsPage.value = 'ai'
    showSettings.value = true
  })
  // 后台自动检查到新版本时弹出应用内更新提示（不再依赖用户主动打开设置）
  Events.On('quickdock:update:status', onUpdateStatus)
  // 全局热键注册失败（多被其它程序占用）：Go 侧延迟投放本事件，弹 toast 引导用户改绑
  Events.On('hotkey:register-failed', (payload: any) => {
    const list: string[] = Array.isArray(payload) ? payload : (payload?.data ?? [])
    if (!list.length) return
    error(t('hotkeyRegisterFailed', { keys: list.join('、') }))
  })
  // 命令面板选中笔记：面板浮窗经后端广播笔记 id，主窗口写入 pendingOpenNoteId
  // （NoteManagerPage 加载/监听后打开对应文档）并切到笔记页。
  // 事件广播到所有窗口，独立窗口（hash 非空）必须忽略。
  Events.On('note:open-request', (ev: any) => {
    if (window.location.hash) return
    const raw = ev?.data ?? ev
    const id = typeof raw === 'string' ? raw : ''
    if (!id.trim()) return
    pendingOpenNoteId.value = id
    navigate('notes')
  })
  // 命令面板选中待办：同上，写入 pendingOpenTodoId 并切到待办页
  Events.On('todo:open-request', (ev: any) => {
    if (window.location.hash) return
    const raw = ev?.data ?? ev
    const id = typeof raw === 'string' ? raw : ''
    if (!id.trim()) return
    pendingOpenTodoId.value = id
    navigate('todos')
  })
  // 命令面板「快速导航」：面板浮窗经后端广播页面 id，主窗口直接切页。
  // 走 setPage 而不是 navigate setNavigator 那条：这里就在主窗口内，没有
  // 「导航器尚未注册」的问题，且需要保留 registry 的 activate 分发语义。
  Events.On('nav:goto', (ev: any) => {
    if (window.location.hash) return
    const raw = ev?.data ?? ev
    const page = typeof raw === 'string' ? raw : ''
    if (!page.trim()) return
    setPage(page)
  })
});

// ---- 后台自动更新提示弹窗 ----
// 后台检查（backgroundCheck）发现新版本会发 quickdock:update:status；此前只在设置弹窗（默认关闭）内
// 默默更新文字 + 发系统通知（易错过），用户感知不到。这里改为应用内自动弹出明显提示。
function onUpdateStatus(payload: any) {
  const st = payload?.data ?? payload
  if (!st || st.state !== 'available') return
  const v = st.availableVersion
  if (!v) return
  // 本次会话已处理过该版本，不再弹
  if (updatePromptHandled.has(v)) return
  try {
    const today = new Date().toISOString().slice(0, 10)
    // 当天选过「稍后」则今日不再弹；选过「跳过此版本」则永久不再弹
    if (localStorage.getItem('qd_update_later_' + v) === today) return
    if (localStorage.getItem('qd_update_skip_' + v) === '1') return
  } catch (e) { logErr('App', e) }
  updatePromptData.value = {
    version: v,
    currentVersion: st.currentVersion || '',
    releaseNotes: st.releaseNotes || '',
  }
  updatePromptVisible.value = true
}

function closeUpdatePrompt() {
  updatePromptVisible.value = false
}

function viewUpdate() {
  const v = updatePromptData.value?.version
  if (v) updatePromptHandled.add(v)
  closeUpdatePrompt()
  // 打开「关于」页，更新区（下载/重启）就在那里
  settingsPage.value = 'about'
  showSettings.value = true
}

function laterUpdate() {
  const v = updatePromptData.value?.version
  if (v) {
    updatePromptHandled.add(v)
    try { localStorage.setItem('qd_update_later_' + v, new Date().toISOString().slice(0, 10)) } catch (e) { logErr('App', e) }
  }
  closeUpdatePrompt()
}

async function skipUpdate() {
  const v = updatePromptData.value?.version
  if (v) {
    updatePromptHandled.add(v)
    try {
      localStorage.setItem('qd_update_skip_' + v, '1')
      unwrap(await SkipUpdate(v))
    } catch (e) {
      // 跳过失败也要留痕：否则用户点了「跳过」却反复被同一版本打扰，无从排查
      logErr('App:skipUpdate', e)
    }
  }
  closeUpdatePrompt()
}

provide('theme', { current: currentTheme, set: setTheme })

// 当前待确认的对话框：直接取确认队列头部。
// 用 computed 取代「watch + ref 同步」的写法，避免 resolve 处理器与 watch 之间的竞态
// （旧写法在快速连续 confirm 时可能漏掉某些条目 / 留下永不 resolve 的孤儿 Promise）。
const activeConfirm = computed(() =>
  confirmItems[0]
    ? { id: confirmItems[0].id, message: confirmItems[0].message }
    : null
)
</script>

<template>
  <!-- 独立剪贴板窗口：仅显示剪贴板列表 -->
  <div v-if="isClipboardWindow" class="clipboard-standalone">
    <ClipboardPanel compact />
  </div>

  <!-- 快捷笔记独立窗口：复用剪贴板窗口，导航到 #/note -->
  <div v-else-if="isNoteWindow" class="note-standalone">
    <NotePanel />
  </div>

  <!-- 窗口管理浮层：选模板→分配窗口→一键排版 -->
  <div v-else-if="isWinmgrWindow" class="winmgr-standalone">
    <WindowManager />
  </div>

  <!-- 命令面板独立窗口 -->
  <div v-else-if="isPaletteWindow" class="palette-standalone">
    <CommandPalette />
  </div>

  <!-- 插件独立窗口 -->
  <div v-else-if="isPluginWindow" class="plugin-standalone">
    <PluginPage v-if="pluginWindowId" :key="pluginWindowId" :pluginId="pluginWindowId" />
    <div v-else class="plugin-standalone-empty">
      <p>{{ t('loading') }}</p>
    </div>
  </div>

  <!-- SSH 终端独立窗口（多会话工作台，非浮窗） -->
  <div v-else-if="isSshWindow" class="ssh-standalone">
    <SshPage />
  </div>

  <!-- 主窗口：完整 UI -->
  <div v-else class="app-container">
    <div class="app-body">
      <Sidebar class="app-sidebar"
        :currentPage="currentPage"
        :busyPage="dshOpening ? 'dsh' : ''"
        @navigate="setPage"
        @open-settings="openSettings"
      />
      <!-- 场景栏不再挂在 .app-body 下：它必须和集合/项目在同一行里，
           否则 SceneTags 这条横栏只把集合/项目往下推，
           三栏的面板头不在同一条水平线上 —— 这才是"三栏割裂"的主因。 -->
      <div class="app-content">
        <!-- 内容区只有一套渲染：注册页（工作空间也在这里，复合布局在 WorkspacePage 内部）。
             此前 workspace 是一支独立分支，四层条件嵌套；现在它与其它页完全同权。 -->
        <div
          v-if="currentContentPage"
          :class="currentContentPage.wrapperClass || 'page-shell'"
        >
          <!-- KeepAlive：白名单内的页（AI / Monitor / SSH / Note / Plugin）离开后继续存活，
               切回来不重 fetch、不重建 xterm / 轮询 / 编辑态（见 KEEP_ALIVE_PAGES）。
               其余页按常规卸载重载——它们是无状态列表，重建便宜且能拿到最新数据。 -->
          <Transition name="page" mode="out-in">
            <KeepAlive :include="KEEP_ALIVE_PAGES">
              <component
                :is="currentContentPage.component"
                v-bind="currentContentPage.props ? currentContentPage.props(pageContext) : {}"
                v-on="currentContentPage.listeners ? currentContentPage.listeners(pageContext) : {}"
              />
            </KeepAlive>
          </Transition>
        </div>
      </div>
    </div>

    <SettingsModal :visible="showSettings" :initialPage="settingsPage" @close="showSettings = false; settingsPage = undefined" />
    <UpdatePrompt
      :visible="updatePromptVisible"
      :version="updatePromptData?.version || ''"
      :current-version="updatePromptData?.currentVersion || ''"
      :release-notes="updatePromptData?.releaseNotes || ''"
      @view="viewUpdate"
      @later="laterUpdate"
      @skip="skipUpdate"
    />
  </div>

  <!-- 全局浮层：主窗口 / 独立剪贴板窗口 / 命令面板窗口 共用 -->
  <Toast :messages="items" @remove="remove" />
  <ConfirmDialog
    v-if="activeConfirm"
    :visible="true"
    :message="activeConfirm.message"
    @confirm="resolveConfirm(activeConfirm.id, true)"
    @cancel="resolveConfirm(activeConfirm.id, false)"
  />
</template>

<style>
html, body, #app {
  height: 100%; width: 100%;
  overflow: hidden;
}

body {
  font-family: var(--font-family);
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
  user-select: none;
}

/* 切换导航的过渡：淡出→淡入（out-in）。
   不用「同时过渡」是因为那需要两页绝对定位叠在一起，而各页容器结构不同
   （有的是 .page-shell display:contents、有的要撑满），一叠就错位。
   离场比入场快：切换的手感由「旧页消失得干脆」决定。 */
.page-enter-active { transition: opacity var(--transition-base), transform var(--transition-base); }
.page-leave-active { transition: opacity var(--transition-fast), transform var(--transition-fast); }
.page-enter-from { opacity: 0; transform: translateY(6px); }
.page-leave-to { opacity: 0; transform: translateY(-4px); }

</style>

<style scoped>
.clipboard-standalone {
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}

.palette-standalone {
  height: 100vh; width: 100vw; overflow: hidden;
  background: transparent;
}

.plugin-standalone {
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}

.winmgr-standalone {
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}

/* SSH 窗口：SshPage 自己撑满并管理内部滚动，这里不加 padding，
   否则终端的 FitAddon 会按父元素高度算尺寸（见 SshTerminal.vue 的注释）。 */
.ssh-standalone {
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}

.plugin-standalone-empty {
  height: 100%; display: flex; align-items: center; justify-content: center;
  color: var(--color-text-disabled); font-size: 13px;
}

.app-container {
  display: flex; flex-direction: column;
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}
.app-body { display: flex; flex: 1; overflow: hidden; }
.app-sidebar { flex-shrink: 0; }
.app-content {
  flex: 1; min-width: 0;
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--color-bg-primary);
}
/* 注册页外层：默认 display:contents 使其子组件等价于 .app-content 的直接 flex 子项，
   保持与旧 v-if 链一致的布局；clipboard 等需撑满的页由 wrapperClass 覆盖本规则。 */
.page-shell { display: contents; }
/* 工作空间的三栏布局样式（.ws-root / .ws-body / .ws-error-bar 等）随布局
   移到了 WorkspacePage.vue 的 scoped 样式里：父组件的 scoped 规则够不到子组件内部元素。 */

/* 剪贴板页面 */
.clipboard-page {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
</style>

