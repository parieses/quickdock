<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, toRef, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { X, Monitor, Palette, Keyboard, Database, Cloud, Info, ChevronRight, Sun, Moon, Monitor as MonitorIcon, HardDrive, RotateCcw, Bot, Wrench } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'
import { unwrap } from '../utils/api'
import { i18n } from '../i18n'
import { Events } from '@wailsio/runtime'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const store = useWorkspaceStore()
import HotkeySettings from './HotkeySettings.vue'
import SettingsAI from './SettingsAI.vue'
import SettingsSnapshot from './SettingsSnapshot.vue'
import SettingsWebDAV from './SettingsWebDAV.vue'
import SettingsTools from './SettingsTools.vue'
import { GetClipboardRetentionDays, SetClipboardRetentionDays, CleanupClipboardNow } from '../../bindings/quickdock/services/appservice'
import { GetAutoStart, SetAutoStart } from '../../bindings/quickdock/services/appservice'
import { GetValue, SetValue } from '../../bindings/quickdock/services/appservice'
import { SuspendHotkeys, ResumeHotkeys } from '../../bindings/quickdock/services/appservice'
import { GetAppVersion, CheckForUpdates, DownloadUpdate, RestartApp, GetUpdateState } from '../../bindings/quickdock/services/appservice'
import type { UpdateStatus, AIProfile } from '../../bindings/quickdock/services/models'
import type { ToastAPI } from '../types'
import { useWorkspaceStore } from '../stores/workspace'
import { getErrorMessage } from '../utils/error'

const props = defineProps<{
  visible: boolean
  initialPage?: string
}>()

const emit = defineEmits<{ close: [] }>()

const activePage = ref<string | null>(null)
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)
const hotkeyRef = ref<InstanceType<typeof HotkeySettings> | null>(null)

const menuItems = computed(() => [
  { key: 'general',    label: t('general'),         icon: Monitor,  desc: t('autoStart') },
  { key: 'appearance', label: t('appearance'),      icon: Palette,  desc: t('theme') + ' / ' + t('language') },
  { key: 'hotkeys',    label: t('hotkeySettings'),  icon: Keyboard, desc: t('shortcut') },
  { key: 'data',       label: t('clipboardHistory'), icon: Database, desc: t('retentionDays') + ' / ' + t('cleanupNow') },
  { key: 'webdav',     label: 'WebDAV',              icon: Cloud,    desc: t('settings') },
  { key: 'snapshot',   label: t('snapshot'),          icon: HardDrive, desc: t('snapshotDesc') },
  { key: 'tools',      label: t('openTool'),          icon: Wrench,    desc: t('toolManageDesc') },
  { key: 'ai',         label: t('navAi'),             icon: Bot,      desc: t('aiSettingsDesc') },
])

function selectMenu(key: string) {
  activePage.value = key
}

function close() {
  activePage.value = null
  emit('close')
}

function onKeydown(e: KeyboardEvent) {
  // 如果快捷键页正在捕获，Escape 不关闭设置页
  if (e.key === 'Escape' && activePage.value === 'hotkeys' && hotkeyRef.value?.capturing) {
    return
  }
  if (e.key === 'Escape') { close(); return }
  onKeydownTrap(e)
}

// ---- 更新检查 ----
const appVersion = ref('')
const updateStatus = ref<UpdateStatus | null>(null)
const updateChecking = ref(false)
const updateResult = ref('')

onMounted(async () => {
  try {
    const ver = await GetAppVersion()
    appVersion.value = ver
    const state = await GetUpdateState()
    if (state) updateStatus.value = state
  } catch {}
})

async function checkForUpdates() {
  updateChecking.value = true
  updateResult.value = ''
  try {
    const result = await CheckForUpdates()
    if (!result) { updateResult.value = t('updateError'); updateChecking.value = false; return }
    updateStatus.value = result
    if (result.state === 'up-to-date') {
      updateResult.value = t('updateUpToDate')
    } else if (result.state === 'available') {
      updateResult.value = t('updateAvailable') + ' ' + (result.availableVersion || '')
    } else if (result.state === 'error') {
      updateResult.value = (result.error || t('updateError'))
    }
  } catch (e: any) {
    updateResult.value = getErrorMessage(e)
  } finally {
    updateChecking.value = false
  }
}

async function downloadUpdate() {
  updateResult.value = t('updateDownloading')
  try {
    const result = await DownloadUpdate()
    if (!result) { updateResult.value = t('updateError'); return }
    updateStatus.value = result
    if (result.state === 'ready') {
      updateResult.value = t('updateReady')
    } else if (result.state === 'error') {
      updateResult.value = result.error || t('updateError')
    }
  } catch (e: any) {
    updateResult.value = getErrorMessage(e)
  }
}

async function restartApp() {
  try {
    await RestartApp()
  } catch {}
}

// ---- 主题 / 语言 ----
const currentTheme = ref('system')
const themeOptions = computed(() => [
  { value: 'dark',   label: t('dark'), icon: Moon },
  { value: 'light',  label: t('light'), icon: Sun },
  { value: 'system', label: t('system'), icon: MonitorIcon },
])

function applyTheme(theme: string) {
  const prefersDark = window.matchMedia('(prefers-color-scheme: dark)')
  const isDark = theme === 'dark' || (theme === 'system' && prefersDark.matches)
  document.documentElement.setAttribute('data-theme', isDark ? 'dark' : 'light')
}

async function setTheme(theme: string) {
  currentTheme.value = theme
  applyTheme(theme)
  try { await SetValue('theme', theme) } catch (_) {}
}
const currentLocale = ref('zh-CN')

async function setLocale(newLocale: string) {
  currentLocale.value = newLocale
  i18n.global.locale.value = newLocale as 'zh-CN' | 'en-US'
  try {
    await SetValue('locale', newLocale)
  } catch (_) {}
}

// 打开设置页时如有初始页面则导航，不再全局挂起热键
watch(() => props.visible, async (val) => {
  if (val) {
    if (props.initialPage) {
      activePage.value = props.initialPage
    }
  }
}, { immediate: false })

// 只在弹窗可见时监听全局按键，避免隐藏后 keydown 常驻
watch(() => props.visible, (v) => {
  if (v) {
    document.addEventListener('keydown', onGlobalKeydown)
  } else {
    document.removeEventListener('keydown', onGlobalKeydown)
  }
}, { immediate: false })

onMounted(async () => {
  if (props.visible) {
    document.addEventListener('keydown', onGlobalKeydown)
  }
  try {
    const saved = unwrap<string>(await GetValue('locale'))
    if (saved) currentLocale.value = saved
  } catch (_) {}
  try {
    const saved = unwrap<string>(await GetValue('theme'))
    if (saved === 'dark' || saved === 'light' || saved === 'system') {
      currentTheme.value = saved
    }
  } catch (_) {}
  try {
    const days = unwrap<number>(await GetClipboardRetentionDays())
    clipboardRetentionDays.value = days ?? 30
  } catch (_) {}
  try {
    autoStart.value = unwrap<boolean>(await GetAutoStart()) ?? false
  } catch (_) {}
})

// ---- 剪贴板设置 ----
const clipboardRetentionDays = ref(30)
const cleanupResult = ref('')
const autoStartResult = ref('')
const autoStart = ref(false)
const cleanupTimer = ref<ReturnType<typeof setTimeout> | null>(null)

function clearCleanupTimer() {
  if (cleanupTimer.value !== null) {
    clearTimeout(cleanupTimer.value)
    cleanupTimer.value = null
  }
}

function onGlobalKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (activePage.value === 'hotkeys' && hotkeyRef.value?.capturing) {
    return
  }
  // 全局 handler 和模板 @keydown 可能同时触发，跳过已关闭状态
  if (activePage.value === null) return
  close()
}

onUnmounted(() => {
  document.removeEventListener('keydown', onGlobalKeydown)
  clearCleanupTimer()
})

async function saveRetentionDays() {
  try {
    unwrap(await SetClipboardRetentionDays(clipboardRetentionDays.value))
    clearCleanupTimer()
    cleanupResult.value = t('saveSuccess')
    cleanupTimer.value = setTimeout(() => { cleanupResult.value = ''; cleanupTimer.value = null }, 2000)
  } catch (e) {
    cleanupResult.value = t('saveFailed2') + ': ' + getErrorMessage(e)
  }
}

async function cleanNow() {
  try {
    const count = unwrap<number>(await CleanupClipboardNow())
    clearCleanupTimer()
    cleanupResult.value = t('cleanupResult') + ' ' + count + ' ' + t('count')
    cleanupTimer.value = setTimeout(() => { cleanupResult.value = ''; cleanupTimer.value = null }, 3000)
  } catch (e) {
    cleanupResult.value = t('cleanupResult') + ': ' + getErrorMessage(e)
  }
}

async function toggleAutoStart() {
  const newVal = !autoStart.value
  try {
    unwrap(await SetAutoStart(newVal))
    autoStart.value = newVal
  } catch (e) {
    autoStartResult.value = t('saveFailed2') + ': ' + getErrorMessage(e)
  }
}

</script>

<template>
  <Teleport to="body">
    <Transition name="panel-slide">
      <div v-if="visible" class="settings-overlay" @mousedown.self="close" @keydown="onKeydown">
        <div ref="panelRef" class="settings-panel" @mousedown.stop>
        <!-- 左侧菜单 -->
        <div class="settings-sidebar">
          <div class="settings-sidebar-header">
            <span class="sidebar-header-title">{{ t('settings') }}</span>
            <button class="close-btn" @click="close">
              <X :size="18" />
            </button>
          </div>

          <div class="settings-menu">
            <button
              v-for="item in menuItems"
              :key="item.key"
              :class="['menu-row', { active: activePage === item.key }]"
              @click="selectMenu(item.key)"
            >
              <component :is="item.icon" :size="18" class="menu-row-icon" />
              <div class="menu-row-text">
                <span class="menu-row-label">{{ item.label }}</span>
                <span class="menu-row-desc">{{ item.desc }}</span>
              </div>
              <ChevronRight :size="14" class="menu-row-arrow" />
            </button>

            <!-- 分隔线 -->
            <div class="menu-spacer" />

            <!-- 关于 -->
            <button
              :class="['menu-row about-row', { active: activePage === 'about' }]"
              @click="selectMenu('about')"
            >
              <Info :size="18" class="menu-row-icon" />
              <div class="menu-row-text">
                <span class="menu-row-label">{{ t('appName') }}</span>
                <span class="menu-row-desc">{{ t('aboutDesc') }}</span>
              </div>
              <ChevronRight :size="14" class="menu-row-arrow" />
            </button>
          </div>
        </div>

        <!-- 右侧内容 -->
        <div class="settings-content">
          <!-- 关于（含版本信息和更新检查） -->
          <div v-if="activePage === 'about'" class="content-page content-left">
            <div class="section">
              <h3>{{ t('appName') }}</h3>
              <p class="about-version">{{ t('version') }} {{ appVersion || '0.0.0' }}</p>
              <p class="about-desc">{{ t('appDesc') }}</p>
              <p class="about-tech">{{ t('aboutTech') }}</p>
              <p class="about-copy">{{ t('aboutCopyright') }}</p>
            </div>

            <div class="section" style="margin-top:24px">
              <h3 class="section-title">{{ t('update') }}</h3>
              <p class="section-desc">{{ t('updateCheckingAuto') }}</p>

              <div class="action-row" style="margin-top:12px">
                <button class="btn btn-primary" :disabled="updateChecking" @click="checkForUpdates">
                  <RotateCcw :size="14" :class="{ spinning: updateChecking }" />
                  {{ updateChecking ? t('updateChecking') : t('updateCheckNow') }}
                </button>
              </div>

              <p v-if="updateResult" class="result-hint" :class="{ 'result-error': updateStatus?.state === 'error' }">{{ updateResult }}</p>

              <div v-if="updateStatus?.state === 'available'" class="action-row" style="margin-top:12px">
                <button class="btn btn-primary" @click="downloadUpdate">
                  {{ t('updateDownload') }} {{ updateStatus.availableVersion }}
                </button>
                <button class="btn btn-secondary" @click="updateStatus.state = 'idle'">
                  {{ t('updateSkip') }}
                </button>
              </div>

              <div v-if="updateStatus?.state === 'ready'" class="action-row" style="margin-top:12px">
                <button class="btn btn-primary update-restart-btn" @click="restartApp">
                  {{ t('updateRestart') }}
                </button>
              </div>
            </div>
          </div>

          <!-- 外观设置 -->
          <div v-else-if="activePage === 'appearance'" class="content-page content-left">
            <div class="section">
              <h3 class="section-title">{{ t('theme') }}</h3>
              <p class="section-desc">{{ t('themeDesc') }}</p>
              <div class="theme-selector">
                <button
                  v-for="opt in themeOptions"
                  :key="opt.value"
                  :class="['theme-card', { active: currentTheme === opt.value }]"
                  @click="setTheme(opt.value)"
                >
                  <component :is="opt.icon" :size="24" />
                  <span>{{ opt.label }}</span>
                </button>
              </div>
            </div>
            <div class="section" style="margin-top: 32px;">
              <h3 class="section-title">{{ t('language') }}</h3>
              <p class="section-desc">{{ t('languageDesc') }}</p>
              <div class="locale-selector">
                <button
                  :class="['locale-btn', { active: currentLocale === 'zh-CN' }]"
                  @click="setLocale('zh-CN')"
                >简体中文</button>
                <button
                  :class="['locale-btn', { active: currentLocale === 'en-US' }]"
                  @click="setLocale('en-US')"
                >English</button>
              </div>
            </div>
          </div>
          <HotkeySettings ref="hotkeyRef" v-else-if="activePage === 'hotkeys'" />

          <!-- 数据与备份 -->
          <div v-else-if="activePage === 'data'" class="content-page content-left">
            <div class="section">
              <h3 class="section-title">{{ t('clipboardHistory') }}</h3>
              <p class="section-desc">{{ t('clipboardDesc') }}</p>
              <div class="setting-row">
                <label class="setting-label">{{ t('retentionDays') }}</label>
                <div class="setting-control">
                  <input v-model.number="clipboardRetentionDays" type="number" min="1" max="365" class="num-input" />
                  <span class="input-suffix">{{ t('days') }}</span>
                  <button class="btn btn-primary" @click="saveRetentionDays">{{ t('save') }}</button>
                </div>
              </div>
              <div class="action-row">
                <button class="btn btn-secondary" @click="cleanNow">{{ t('cleanupNow') }}</button>
              </div>
              <p v-if="cleanupResult" class="result-hint">{{ cleanupResult }}</p>
            </div>
          </div>

          <!-- WebDAV 同步 -->
          <div v-else-if="activePage === 'webdav'" class="content-page content-left">
            <SettingsWebDAV :visible="activePage === 'webdav'" @close="close" />
          </div>

          <!-- 快照备份 -->
          <div v-else-if="activePage === 'snapshot'" class="content-page content-left">
            <SettingsSnapshot :visible="activePage === 'snapshot'" @close="close" />
          </div>

          <!-- 通用设置 -->
          <div v-else-if="activePage === 'general'" class="content-page content-left">
            <div class="section">
              <h3 class="section-title">{{ t('general') }}</h3>
              <div class="setting-row">
                <label class="setting-label">{{ t('autoStart') }}</label>
                <div class="setting-control">
                  <button
                    :class="['toggle-btn', { active: autoStart }]"
                    @click="toggleAutoStart"
                  >
                    <span class="toggle-knob" />
                  </button>
                  <span class="toggle-label">{{ autoStart ? t('autoStartOn') : t('autoStartOff') }}</span>
                </div>
                <p v-if="autoStartResult" class="result-hint error">{{ autoStartResult }}</p>
              </div>
            </div>
          </div>

          <!-- AI 助手 -->
          <div v-else-if="activePage === 'ai'" class="content-page content-left">
            <SettingsAI :visible="activePage === 'ai'" />
          </div>

          <!-- 打开工具管理 -->
          <div v-else-if="activePage === 'tools'" class="content-page content-left">
            <SettingsTools :visible="activePage === 'tools'" />
          </div>

          <!-- 其他设置页占位（旧路由兼容） -->
          <div v-else-if="activePage && activePage !== 'about' && activePage !== 'hotkeys'" class="content-page">
            <p class="placeholder-title">{{ menuItems.find(m => m.key === activePage)?.label }}</p>
            <p class="placeholder-hint">{{ t('comingSoon') }}</p>
          </div>

          <!-- 空状态 -->
          <div v-else class="content-page content-empty">
            <p class="empty-icon">⚙</p>
            <p class="empty-text">{{ t('selectMenuHint') }}</p>
          </div>
        </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style>
/* === 全屏覆盖层 === */
.settings-overlay {
  position: fixed; inset: 0; z-index: 10000;
  background: var(--color-bg-overlay);
}

/* === 全屏面板 === */
.settings-panel {
  position: fixed; inset: 0; z-index: 10001;
  display: flex;
  background: var(--color-bg-primary);
}

/* === 左侧菜单 === */
.settings-sidebar {
  width: 240px; min-width: 240px;
  background: var(--color-bg-secondary); border-right: 1px solid var(--color-border);
  display: flex; flex-direction: column;
}

.settings-sidebar-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.sidebar-header-title {
  font-size: 16px; font-weight: 600; color: var(--color-text-primary);
}
.close-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  width: 30px; height: 30px; border-radius: 6px;
  display: flex; align-items: center; justify-content: center;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.close-btn:hover { color: var(--color-text-primary); background: var(--color-bg-active); }

.settings-menu {
  flex: 1; overflow-y: auto; padding: 8px 0;
}

.menu-row {
  width: 100%; display: flex; align-items: center; gap: 12px;
  padding: 12px 20px; border: none; background: transparent;
  color: var(--color-text-muted); font-size: 13px; cursor: pointer;
  text-align: left; font-family: inherit;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.menu-row:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.menu-row.active { background: var(--color-bg-hover); color: var(--color-accent); }
.menu-row-icon { flex-shrink: 0; opacity: 0.7; color: inherit; }
.menu-row.active .menu-row-icon { opacity: 1; }
.menu-row-text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.menu-row-label { font-size: 13px; font-weight: 500; }
.menu-row-desc { font-size: 11px; color: var(--color-text-disabled); }
.menu-row.active .menu-row-desc { color: var(--color-accent-muted); }
.menu-row-arrow { color: var(--color-text-disabled); flex-shrink: 0; }
.menu-row.active .menu-row-arrow { color: var(--color-accent); }

.menu-spacer {
  flex: 1; min-height: 20px;
}

.about-row {
  border-top: 1px solid var(--color-border);
}

/* === 右侧内容 === */
.settings-content {
  flex: 1; overflow-y: auto;
}

.content-page {
  height: 100%; display: flex; flex-direction: column;
  align-items: center; justify-content: center;
  padding: 48px 32px;
}

.content-empty {
  color: var(--color-text-disabled);
}
.content-empty .empty-icon { font-size: 48px; margin-bottom: 16px; }
.content-empty .empty-text { font-size: 14px; }

/* 关于（含版本信息） */
.about-version { font-size: 13px; color: var(--color-accent); margin: 0 0 16px; }
.about-desc { font-size: 14px; color: var(--color-text-muted); margin: 0 0 4px; }
.about-tech { font-size: 12px; color: var(--color-text-disabled); margin: 0 0 20px; }
.about-copy { font-size: 11px; color: var(--color-text-disabled); margin: 0; }

/* 更新按钮 */
.update-restart-btn { background: var(--color-accent); color: #fff; font-weight: 500; }
.update-restart-btn:hover { opacity: 0.9; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }

/* 主题/语言选择器 */
.theme-selector { display: flex; gap: 12px; }
.theme-card {
  flex: 1; display: flex; flex-direction: column; align-items: center; gap: 8px;
  padding: 16px 12px; border: 1px solid var(--color-border); border-radius: 10px;
  background: transparent; color: var(--color-text-muted); font-size: 12px; cursor: pointer;
  font-family: inherit; transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.theme-card:hover { border-color: var(--color-accent); color: var(--color-text-primary); }
.theme-card.active { border-color: var(--color-accent); background: var(--color-accent-bg); color: var(--color-accent); }
.locale-selector { display: flex; gap: 8px; }
.locale-btn {
  padding: 8px 20px; border: 1px solid var(--color-border); border-radius: 8px;
  background: transparent; color: var(--color-text-muted); font-size: 13px; cursor: pointer;
  font-family: inherit; transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.locale-btn:hover { border-color: var(--color-accent); color: var(--color-text-primary); }
.locale-btn.active { border-color: var(--color-accent); background: var(--color-accent-bg); color: var(--color-accent); }

/* 数据与备份 */
.content-left { align-items: flex-start; justify-content: flex-start; }
.section { width: 100%; max-width: 480px; }
.section-title { font-size: 16px; font-weight: 600; color: var(--color-text-primary); margin: 0 0 4px; }
.section-desc { font-size: 12px; color: var(--color-text-disabled); margin: 0 0 20px; }
.setting-row { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.setting-label { font-size: 13px; color: var(--color-text-muted); min-width: 80px; }
.setting-control { display: flex; align-items: center; gap: 8px; }
.num-input {
  width: 80px; padding: 6px 10px; border: 1px solid var(--color-border); border-radius: 6px;
  background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 13px; font-family: inherit;
  outline: none;
}
.num-input:focus { border-color: var(--color-accent); }
.input-suffix { font-size: 12px; color: var(--color-text-disabled); }
.btn {
  padding: 6px 14px; border: none; border-radius: 6px;
  font-size: 12px; cursor: pointer; font-family: inherit;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.btn-primary { background: var(--color-accent); color: var(--color-accent-text); }
.btn-primary:hover { background: var(--color-accent-hover); }
.btn-secondary { background: var(--color-bg-active); color: var(--color-text-secondary); }
.btn-secondary:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.action-row { margin-top: 8px; }
.result-hint { font-size: 12px; color: var(--color-accent); margin: 8px 0 0; }

/* 切换开关 */
.toggle-btn {
  width: 40px; height: 22px; border-radius: 11px; border: none;
  background: var(--color-bg-active); cursor: pointer; position: relative;
  transition: background 0.2s; padding: 0;
}
.toggle-btn.active { background: var(--color-accent); }
.toggle-knob {
  position: absolute; top: 2px; left: 2px;
  width: 18px; height: 18px; border-radius: 50%;
  background: var(--color-accent-text); transition: transform 0.2s;
}
.toggle-btn.active .toggle-knob { transform: translateX(18px); }
.toggle-label { font-size: 12px; color: var(--color-text-muted); }

/* 快照备份 */
.snapshot-create-area { margin-bottom: 16px; }
.snapshot-create-form { display: flex; flex-direction: column; gap: 8px; }
.snapshot-input {
  width: 100%; padding: 8px 12px; border: 1px solid var(--color-border); border-radius: 6px;
  background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 13px;
  font-family: inherit; outline: none; box-sizing: border-box;
}
.snapshot-input:focus { border-color: var(--color-accent); }
.snapshot-create-actions { display: flex; gap: 8px; }
.snapshot-empty { text-align: center; padding: 32px 0; }
.snapshot-empty .empty-icon { color: var(--color-text-muted); margin-bottom: 12px; }
.snapshot-empty .empty-text { font-size: 13px; color: var(--color-text-disabled); margin: 0 0 4px; }
.snapshot-empty .empty-hint { font-size: 11px; color: var(--color-text-muted); margin: 0; }

/* WebDAV 表单 */
.webdav-form { display: flex; flex-direction: column; gap: 14px; margin-top: 16px; }
.webdav-actions { display: flex; gap: 10px; flex-wrap: wrap; }
.webdav-actions .btn { flex: 0 0 auto; }
.result-error { color: var(--color-danger) !important; }

.ai-hint { font-size: 12px; color: var(--color-text-disabled); margin: 10px 0 0; line-height: 1.5; }

/* 多档案列表 */
.ai-profiles { display: flex; flex-direction: column; gap: 6px; margin: 4px 0 16px; }
.ai-profile-item {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 10px; border: 1px solid var(--color-border); border-radius: 8px;
  cursor: pointer; background: var(--color-bg-tertiary); transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-profile-item:hover { border-color: var(--color-border-light); }
.ai-profile-item.active { border-color: var(--color-accent); background: var(--color-accent-bg); }
.ai-profile-info { flex: 1; cursor: pointer; min-width: 0; }
.ai-profile-actions { display: flex; gap: 2px; align-items: center; flex-shrink: 0; }
.ai-profile-edit {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center; width: 22px; height: 22px;
  border-radius: 6px; flex-shrink: 0;
}
.ai-profile-edit:hover { color: var(--color-accent); background: var(--color-accent-bg); }
.ai-profile-name { flex: 1; font-size: 13px; color: var(--color-text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ai-profile-model { font-size: 11px; color: var(--color-text-muted); }
.ai-profile-del {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center; width: 22px; height: 22px;
  border-radius: 6px; flex-shrink: 0;
}
.ai-profile-del:hover { color: var(--color-danger); background: rgba(232,76,76,0.1); }
.ai-profile-add {
  display: flex; align-items: center; justify-content: center; gap: 4px;
  padding: 7px; border: 1px dashed var(--color-border); background: transparent;
  color: var(--color-text-secondary); border-radius: 8px; cursor: pointer;
  font-family: inherit; font-size: 12px; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-profile-add:hover { color: var(--color-accent); border-color: var(--color-accent); }

/* 配置编辑模态框 */
.ai-modal-overlay {
  position: fixed; inset: 0; z-index: 20000;
  background: rgba(0,0,0,0.45); display: flex;
  align-items: center; justify-content: center;
}
.ai-modal {
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: 12px; width: 520px; max-width: 90vw; max-height: 85vh;
  display: flex; flex-direction: column; box-shadow: 0 12px 40px rgba(0,0,0,0.25);
}
.ai-modal-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 18px; border-bottom: 1px solid var(--color-border); flex-shrink: 0;
}
.ai-modal-header h3 { font-size: 15px; font-weight: 600; margin: 0; color: var(--color-text-primary); }
.ai-modal-close {
  background: none; border: none; color: var(--color-text-muted);
  font-size: 20px; cursor: pointer; width: 28px; height: 28px;
  display: flex; align-items: center; justify-content: center; border-radius: 6px;
}
.ai-modal-close:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.ai-modal-body { padding: 14px 18px; overflow-y: auto; flex: 1; display: flex; flex-direction: column; gap: 8px; }
.ai-modal-footer {
  display: flex; gap: 8px; justify-content: flex-end;
  padding: 12px 18px; border-top: 1px solid var(--color-border); flex-shrink: 0;
}
.ai-edit-bar { display: flex; gap: 8px; margin: 4px 0; }

/* 小型 toggle 开关（思考模式） */
.toggle-btn-sm {
  position: relative; width: 36px; height: 20px; border-radius: 10px;
  border: none; background: var(--color-bg-active); cursor: pointer;
  transition: background 0.15s; padding: 0; flex-shrink: 0;
}
.toggle-btn-sm.active { background: var(--color-accent); }
.toggle-btn-sm .toggle-knob {
  position: absolute; top: 2px; left: 2px; width: 16px; height: 16px;
  border-radius: 50%; background: var(--color-text-secondary);
  transition: transform 0.15s, background 0.15s;
}
.toggle-btn-sm.active .toggle-knob {
  transform: translateX(16px); background: var(--color-accent-text);
}
.toggle-field { flex-direction: row !important; align-items: center; gap: 8px; }

/* 表单字段（AI 配置与 WebDAV 复用） */
.field { display: flex; flex-direction: column; gap: 6px; }
.field-label { font-size: 12px; color: var(--color-text-muted); font-weight: 500; }
.field-input {
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: 6px;
  padding: 9px 12px; color: var(--color-text-primary); font-size: 13px;
  outline: none; transition: border-color 0.15s;
  font-family: inherit;
}
.field-input:focus { border-color: var(--color-accent); box-shadow: 0 0 0 2px var(--color-accent-border); }
.field-input::placeholder { color: var(--color-text-disabled); }
.field-textarea textarea { resize: vertical; min-height: 60px; font-family: inherit; }
.field-row { display: flex; gap: 12px; }
.field-half { flex: 1; min-width: 0; }
.snapshot-list { display: flex; flex-direction: column; gap: 8px; margin-top: 12px; }
.snapshot-item {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 12px 14px; background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: 8px; transition: border-color 0.12s;
}
.snapshot-item:hover { border-color: var(--color-border-light); }
.snapshot-item-info { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.snapshot-item-label { font-size: 13px; font-weight: 500; color: var(--color-text-primary); }
.snapshot-item-note { font-size: 11px; color: var(--color-text-muted); }
.snapshot-item-meta { font-size: 11px; color: var(--color-text-disabled); }
.snapshot-item-actions { display: flex; gap: 4px; flex-shrink: 0; }
.snapshot-item-actions .action-btn {
  width: 30px; height: 30px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-muted); border-radius: 6px;
  cursor: pointer; transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.snapshot-item-actions .action-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.snapshot-item-actions .restore-btn:hover { color: var(--color-accent); }
.snapshot-item-actions .action-btn.danger:hover { color: var(--color-danger); background: rgba(255,77,79,0.1); }
.snapshot-item-actions .action-btn.enabled-btn { color: var(--color-accent); }

/* 占位 */
.placeholder-title { font-size: 16px; color: var(--color-text-primary); margin: 0 0 8px; }
.placeholder-hint { font-size: 13px; color: var(--color-text-disabled); margin: 0; }



/* 滑入动画 */
.panel-slide-enter-active,
.panel-slide-leave-active {
  transition: opacity 0.2s ease;
}
.panel-slide-enter-active .settings-sidebar,
.panel-slide-leave-active .settings-sidebar {
  transition: transform 0.2s ease;
}
.panel-slide-enter-from,
.panel-slide-leave-to {
  opacity: 0;
}
.panel-slide-enter-from .settings-sidebar,
.panel-slide-leave-to .settings-sidebar {
  transform: translateX(-100%);
}
</style>
