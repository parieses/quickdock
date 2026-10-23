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
import { navigateTo, pendingOpenNoteId } from './composables/bridge';
import Sidebar from './components/Sidebar.vue';
import SceneRail from './components/SceneRail.vue';
import CollectionList from './components/CollectionList.vue';
import ItemList from './components/ItemList.vue';
import ClipboardPanel from './components/ClipboardPanel.vue';
import NotePanel from './components/NotePanel.vue';
import SceneTags from './components/SceneTags.vue';
import SceneEnvBar from './components/SceneEnvBar.vue';
import Toast from './components/Toast.vue';
import ConfirmDialog from './components/ConfirmDialog.vue';
import { SkipUpdate } from '../bindings/quickdock/services/update/updateservice';
// 页面注册表：主窗口内容页与侧边栏导航项的单一数据源（替代 currentPage 手动 switch）
import { findContentPage, type PageContext } from './pages/registry'

// 异步加载的页面级组件（仅在主窗口中用，减少独立窗口的加载体积）
const SettingsModal = defineAsyncComponent(() => import('./components/SettingsModal.vue'))
const OnboardingPage = defineAsyncComponent(() => import('./components/OnboardingPage.vue'))
const CommandPalette = defineAsyncComponent(() => import('./components/CommandPalette.vue'))
// 主窗口内容页（notes/plugins/todo/schedule/monitor/ai/environments/clipboard）的
// 异步组件与导航信息已收敛到 pages/registry，由注册表驱动渲染，替代手工 v-if 切换链。
const PluginPage = defineAsyncComponent(() => import('./components/PluginPage.vue'))
const WindowManager = defineAsyncComponent(() => import('./components/WindowManager.vue'))
const UpdatePrompt = defineAsyncComponent(() => import('./components/UpdatePrompt.vue'))
// SSH 终端窗口专用页：只在该独立窗口内加载，不进主窗口体积
const SshPage = defineAsyncComponent(() => import('./components/SshPage.vue'))

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
function setPage(page: string) {
  if (page === 'dsh') {
    openDSH()
    return
  }
  if (page === 'ssh') {
    // SSH 不是主窗口内的页面：点侧栏只负责把独立终端窗口唤到前台。
    openSsh()
    return
  }
  currentPage.value = page
}

// 注册跨页面导航（笔记/剪贴板等独立视图 → 侧边栏页面），供 bridge 使用
navigateTo.value = (page: string) => {
  if (window.location.hash) window.location.hash = ''
  currentPage.value = page
}

// 打开设置模态框（供 AIPage 等注册页经注册表事件回调复用，替代模板内联箭头）
function openSettings(page?: string) {
  settingsPage.value = page
  showSettings.value = true
}
// 页面注册表渲染上下文：向注册页传递动态 props / 事件处理器
const pageContext: PageContext = { envInitialSection, openSettings }
const currentContentPage = computed(() => findContentPage(currentPage.value))

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
  // 托盘「打开设置」：显示主窗口后由 Go 侧发事件，这里打开设置模态框
  Events.On('app:open-settings', () => {
    settingsPage.value = undefined
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
    navigateTo.value?.('notes')
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
        :dshOpening="dshOpening"
        @navigate="setPage"
        @open-settings="openSettings"
      />
      <!-- 场景栏：位于导航与内容区之间，顺序为 场景 → 集合 → 项目（仅工作空间页且有工作空间时显示） -->
      <SceneRail v-if="currentPage === 'workspace' && store.workspaces.length > 0" />
      <div class="app-content">
        <!-- 工作空间复合页（含首次引导 Onboarding / 场景栏，非单组件，保留显式分支） -->
        <template v-if="currentPage === 'workspace'">
          <!-- 空状态引导页（首次使用，无工作空间） -->
          <OnboardingPage v-if="store.workspaces.length === 0" @open-settings="openSettings" />

          <!-- 常规内容 -->
          <template v-else>
            <div v-if="store.error" class="ws-error-bar">
              <span class="ws-error-text">{{ store.error }}</span>
              <button class="ws-error-close" @click="store.error = ''" :title="t('close')">✕</button>
            </div>
            <SceneTags />
            <SceneEnvBar />
            <div class="app-content-body">
              <CollectionList class="app-collections" />
              <ItemList class="app-items" />
            </div>
          </template>
        </template>

        <!-- 注册页：由 pages/registry 驱动动态渲染，替代手工 v-if 切换链 -->
        <div
          v-else-if="currentContentPage"
          :class="currentContentPage.wrapperClass || 'page-shell'"
        >
          <component
            :is="currentContentPage.component"
            v-bind="currentContentPage.props ? currentContentPage.props(pageContext) : {}"
            v-on="currentContentPage.listeners ? currentContentPage.listeners(pageContext) : {}"
          />
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
.app-content-body {
  flex: 1; display: flex; overflow: hidden;
}
/* 注册页外层：默认 display:contents 使其子组件等价于 .app-content 的直接 flex 子项，
   保持与旧 v-if 链一致的布局；clipboard 等需撑满的页由 wrapperClass 覆盖本规则。 */
.page-shell { display: contents; }
.ws-error-bar {
  display: flex; align-items: center; gap: 8px;
  margin: 8px 12px 0;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  color: var(--color-danger);
  font-size: 12px;
}
.ws-error-text { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-error-close {
  background: none; border: none; cursor: pointer;
  color: inherit; font-size: 12px; padding: 2px 4px; opacity: 0.7;
}
.ws-error-close:hover { opacity: 1; }
.app-collections { flex-shrink: 0; }
.app-items { flex: 1; min-width: 0; }

/* 剪贴板页面 */
.clipboard-page {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
</style>

