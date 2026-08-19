<script lang="ts" setup>
import { computed, defineAsyncComponent, onMounted, provide, ref, watch } from 'vue';
import { Events } from '@wailsio/runtime';
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from './stores/workspace';
import { useToast } from './composables/useToast';
import { GetValue, SetValue, ApplyTheme, OpenDSHWindow, DetectNodeEnv, SetupDSH } from '../bindings/quickdock/services/appservice';
import { i18n } from './i18n';
import { unwrap } from './utils/api';
import { getErrorMessage } from './utils/error';
import Sidebar from './components/Sidebar.vue';
import CollectionList from './components/CollectionList.vue';
import ItemList from './components/ItemList.vue';
import ClipboardPanel from './components/ClipboardPanel.vue';
import NotePanel from './components/NotePanel.vue';
import SceneTags from './components/SceneTags.vue';
import Toast from './components/Toast.vue';
import ConfirmDialog from './components/ConfirmDialog.vue';

// 异步加载的页面级组件（仅在主窗口中用，减少独立窗口的加载体积）
const SettingsModal = defineAsyncComponent(() => import('./components/SettingsModal.vue'))
const OnboardingPage = defineAsyncComponent(() => import('./components/OnboardingPage.vue'))
const CommandPalette = defineAsyncComponent(() => import('./components/CommandPalette.vue'))
const PluginManagerPage = defineAsyncComponent(() => import('./components/PluginManagerPage.vue'))
const SnippetManagerPage = defineAsyncComponent(() => import('./components/SnippetManagerPage.vue'))
const PluginPage = defineAsyncComponent(() => import('./components/PluginPage.vue'))
const TodoPage = defineAsyncComponent(() => import('./components/TodoPage.vue'))
const SchedulePage = defineAsyncComponent(() => import('./components/SchedulePage.vue'))
const MonitorPage = defineAsyncComponent(() => import('./components/MonitorPage.vue'))
const AIPage = defineAsyncComponent(() => import('./components/AIPage.vue'))
const HttpClientPage = defineAsyncComponent(() => import('./http-client/HttpClientPage.vue'))
const DatabasePage = defineAsyncComponent(() => import('./components/DatabasePage.vue'))

document.title = i18n.global.t('appName');
watch(() => i18n.global.locale.value, () => {
  document.title = i18n.global.t('appName');
});

const store = useWorkspaceStore();
const { t } = useI18n()
const { items, remove, error, success, confirm, confirmItems, resolveConfirm } = useToast();
const showSettings = ref(false);
const settingsPage = ref<string | undefined>(undefined);

// 页面路由
const currentPage = ref('workspace')
function setPage(page: string) {
  if (page === 'dsh') {
    openDSH()
    return
  }
  currentPage.value = page
}

// 从侧边栏点 DeepSeek：先探测环境——缺 node/dsh 自动安装并打开设置页展示进度；已就绪直接开原生窗口
interface EnvProbe { nodeFound?: boolean; dshInstalled?: boolean }
const dshOpening = ref(false)
// 本会话内已确认 node+dsh 就绪后不再重复探测：DetectNodeEnv() 每次会 spawn node/npx 子进程
// 测版本（~1s），dsh 已装好的情况下纯属浪费。后端 Start() 仍会兜底检查入口存在。
let dshEnvReady = false
async function openDSH() {
  if (dshOpening.value) return // 防连点：首启可能耗时数秒，避免开多个窗口
  dshOpening.value = true
  try {
    if (!dshEnvReady) {
      const st = unwrap<EnvProbe | null>(await DetectNodeEnv())
      if (!st?.nodeFound || !st.dshInstalled) {
        // 新电脑：node+dsh 一起自动装；装了 node 没装 dsh：只补 dsh
        settingsPage.value = 'dsh'
        showSettings.value = true
        success(t('dshSettingUp'))
        await new Promise(r => setTimeout(r, 150)) // 等设置页挂载并订阅进度事件
        // 先注册监听再触发安装：避免安装极快完成时错过 done（窗口不自动打开）；done/error 后 off 防泄漏
        Events.Off('quickdock:dsh:progress')
        const off = Events.On('quickdock:dsh:progress', async (payload: any) => {
          const p = (payload?.data ?? payload) as { stage: string; message?: string }
          if (p?.stage === 'done') {
            off()
            dshEnvReady = true
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
      dshEnvReady = true
    }
    success(t('dshLaunching'))
    unwrap(await OpenDSHWindow())
  } catch (e: any) {
    dshEnvReady = false // 环境可能已变化（如 dsh 被卸载），下次点击重新探测
    error(getErrorMessage(e))
    settingsPage.value = 'dsh'
    showSettings.value = true
  } finally {
    dshOpening.value = false
  }
}

provide('toast', { error, success, confirm });

// ---- 窗口类型检测 ----
// 使用 ref 来使 hash 变化可响应
const hashRef = ref(window.location.hash)
window.addEventListener('hashchange', () => {
  hashRef.value = window.location.hash
})

const isClipboardWindow = computed(() => hashRef.value === '#/clipboard')
const isNoteWindow = computed(() => hashRef.value === '#/note')
const isPaletteWindow = computed(() => hashRef.value === '#/command-palette')
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
  try { ApplyTheme(isDark) } catch (_) {}
}

async function setTheme(theme: Theme) {
  currentTheme.value = theme
  applyTheme(theme)
  try { await SetValue('theme', theme) } catch (_) {}
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
  } catch (_) {}
  applyTheme(currentTheme.value)
  // 读取已保存的语言设置
  try {
    const saved = unwrap<string>(await GetValue('locale'))
    if (saved === 'en-US' || saved === 'zh-CN') {
      i18n.global.locale.value = saved
    }
  } catch (_) {}
});

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

  <!-- 主窗口：完整 UI -->
  <div v-else class="app-container">
    <div class="app-body">
      <Sidebar class="app-sidebar"
        :currentPage="currentPage"
        @navigate="setPage"
        @open-settings="(page?: string) => { settingsPage = page; showSettings = true }"
      />
      <div class="app-content">
        <!-- 工作空间页面 -->
        <template v-if="currentPage === 'workspace'">
          <!-- 空状态引导页（首次使用，无工作空间） -->
          <OnboardingPage v-if="store.workspaces.length === 0" @open-settings="(page?: string) => { settingsPage = page; showSettings = true }" />

          <!-- 常规内容 -->
          <template v-else>
            <div v-if="store.error" class="ws-error-bar">
              <span class="ws-error-text">{{ store.error }}</span>
              <button class="ws-error-close" @click="store.error = ''" :title="t('close')">✕</button>
            </div>
            <SceneTags />
            <div class="app-content-body">
              <CollectionList class="app-collections" />
              <ItemList class="app-items" />
            </div>
          </template>
        </template>

        <!-- 文本片段页面 -->
        <SnippetManagerPage v-else-if="currentPage === 'snippets'" />

        <!-- 剪贴板历史页面 -->
        <div v-else-if="currentPage === 'clipboard'" class="clipboard-page">
          <ClipboardPanel />
        </div>

        <!-- 插件页面 -->
        <PluginManagerPage v-else-if="currentPage === 'plugins'" />

        <!-- 待办任务页面 -->
        <TodoPage v-else-if="currentPage === 'todo'" />

        <!-- 定时任务页面 -->
        <SchedulePage v-else-if="currentPage === 'schedule'" />
        <!-- 网站监控页面 -->
        <MonitorPage v-else-if="currentPage === 'monitor'" />

        <!-- HTTP 客户端页面 -->
        <HttpClientPage v-else-if="currentPage === 'httpclient'" />

        <!-- 数据库连接查询页面 -->
        <DatabasePage v-else-if="currentPage === 'database'" />

        <!-- AI 助手页面 -->
        <AIPage v-else-if="currentPage === 'ai'" @open-settings="(page?: string) => { settingsPage = page; showSettings = true }" />
      </div>
    </div>

    <SettingsModal :visible="showSettings" :initialPage="settingsPage" @close="showSettings = false; settingsPage = undefined" />
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
}
.app-content-body {
  flex: 1; display: flex; overflow: hidden;
}
.ws-error-bar {
  display: flex; align-items: center; gap: 8px;
  margin: 8px 12px 0;
  padding: 6px 10px;
  border-radius: 6px;
  background: rgba(244, 67, 54, 0.12);
  border: 1px solid rgba(244, 67, 54, 0.4);
  color: #ef5350;
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
