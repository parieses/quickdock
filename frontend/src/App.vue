<script lang="ts" setup>
import { computed, defineAsyncComponent, onMounted, provide, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from './stores/workspace';
import { useToast } from './composables/useToast';
import { GetValue, SetValue } from '../bindings/quickdock/services/appservice';
import { i18n } from './i18n';
import { unwrap } from './utils/api';
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
const SystemStatusPage = defineAsyncComponent(() => import('./components/SystemStatusPage.vue'))
const TodoPage = defineAsyncComponent(() => import('./components/TodoPage.vue'))
const SchedulePage = defineAsyncComponent(() => import('./components/SchedulePage.vue'))
const MonitorPage = defineAsyncComponent(() => import('./components/MonitorPage.vue'))
const AIPage = defineAsyncComponent(() => import('./components/AIPage.vue'))

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
  currentPage.value = page
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

        <!-- 系统状态页面 -->
        <SystemStatusPage v-else-if="currentPage === 'systemstatus'" />

        <!-- 待办任务页面 -->
        <TodoPage v-else-if="currentPage === 'todo'" />

        <!-- 定时任务页面 -->
        <SchedulePage v-else-if="currentPage === 'schedule'" />
        <!-- 网站监控页面 -->
        <MonitorPage v-else-if="currentPage === 'monitor'" />

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
