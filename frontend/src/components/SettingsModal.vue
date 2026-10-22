<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, toRef, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { X, Palette, Keyboard, Database, Cloud, Info, ChevronRight, Sun, Moon, Monitor as MonitorIcon, HardDrive, RotateCcw, Bot, Wrench, ExternalLink, ScrollText } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'
import { useTheme } from '../composables/useTheme'
import { unwrap } from '../utils/api'
import { i18n } from '../i18n'
import { Events, Browser } from '@wailsio/runtime'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const store = useWorkspaceStore()
import HotkeySettings from './HotkeySettings.vue'
import SettingsAI from './SettingsAI.vue'
import SettingsSnapshot from './SettingsSnapshot.vue'
import SettingsSync from './SettingsSync.vue'
import SettingsTools from './SettingsTools.vue'
import LogViewer from './LogViewer.vue'
import SettingsWinmgr from './SettingsWinmgr.vue'

import {
  GetClipboardRetentionDays,
  SetClipboardRetentionDays,
  CleanupClipboardNow,
  SuspendHotkeys,
  ResumeHotkeys,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { GetAutoStart, SetAutoStart, GetDataDir, SetDataDir, ResetDataDir } from '../../bindings/quickdock/services/appservice'
import { GetValue, SetValue } from '../../bindings/quickdock/services/appservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { GetAppVersion, CheckForUpdates, DownloadUpdate, RestartApp, GetUpdateState } from '../../bindings/quickdock/services/update/updateservice'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
// UpdateStatus 改从 ../types 取：接口统一返回 ApiResult 后 bindings 不再生成它的 model
import type { ToastAPI, UpdateStatus } from '../types'
import { useWorkspaceStore } from '../stores/workspace'
import { getErrorMessage } from '../utils/error'
import ConfirmDialog from './ConfirmDialog.vue'

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
  { key: 'preferences', label: t('preferences'), icon: Palette, desc: t('autoStart') + ' / ' + t('theme') + ' / ' + t('language') },
  { key: 'hotkeys',    label: t('hotkeySettings'),  icon: Keyboard, desc: t('shortcut') },
  { key: 'data',       label: t('clipboardHistory'), icon: Database, desc: t('retentionDays') + ' / ' + t('cleanupNow') },
  { key: 'storage',   label: t('storageLocation'),   icon: HardDrive, desc: t('storageDesc') },
  { key: 'datasync',  label: t('syncAndSnapshot'),   icon: Cloud,    desc: t('syncAndSnapshotDesc') },
  { key: 'tools',      label: t('openTool'),          icon: Wrench,    desc: t('toolManageDesc') },
  { key: 'winmgr',     label: t('winmgr'),             icon: MonitorIcon, desc: t('winmgrDesc') },
  { key: 'ai',         label: t('navAi'),             icon: Bot,      desc: t('aiSettingsDesc') },
  { key: 'logs',       label: t('navLogs'),           icon: ScrollText, desc: t('logsDesc') },
])

// 左侧菜单分组：通用 / 数据与备份 / 服务与工具，扁平展示但用分组标题区分层级。
const menuGroups = computed(() => {
  const byKey = Object.fromEntries(menuItems.value.map((m) => [m.key, m]))
  const defs: { title: string; keys: string[] }[] = [
    { title: t('menuGroupBasics'), keys: ['preferences', 'hotkeys'] },
    { title: t('menuGroupData'), keys: ['data', 'storage', 'datasync'] },
    { title: t('menuGroupService'), keys: ['tools', 'winmgr', 'ai', 'logs'] },
  ]
  return defs
    .map((g) => ({ title: g.title, items: g.keys.map((k) => byKey[k]).filter(Boolean) }))
    .filter((g) => g.items.length > 0)
})

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
// 更新下载实时进度：Wails updater 内部发 wails:updater:download-progress（~10次/秒），
// payload 为 {written, total, rate}；驱动「正在下载 X%」文案与进度条。
const updateDlActive = ref(false)
const updateProgress = ref(0)

let offUpdateStatus: (() => void) | null = null
let offDlProgress: (() => void) | null = null

onMounted(async () => {
  try {
    // 后端已按 R11 统一返回 ApiResult，必须 unwrap 才能拿到业务数据。
    appVersion.value = unwrap<string>(await GetAppVersion()) ?? ''
    const state = unwrap<UpdateStatus>(await GetUpdateState())
    if (state) updateStatus.value = state
  } catch (e) {
    // 取不到版本/状态不影响设置页其它功能，但不该静默——否则用户以为更新功能坏了
    logErr('SettingsModal:loadUpdateState', e)
  }
  // 订阅后台自动检查结果：与手动"检测更新"复用同一套渲染，保证流程一致。
  offUpdateStatus = Events.On('quickdock:update:status', (payload: any) => {
    applyUpdateStatus((payload?.data ?? payload) as UpdateStatus)
  })
  // 订阅更新包下载进度（Wails updater 内部事件，后端并发/镜像下载的进度会汇入其中）
  offDlProgress = Events.On('wails:updater:download-progress', (payload: any) => {
    const d = (payload?.data ?? payload) as { written?: number; total?: number } | undefined
    if (!d || !d.total || typeof d.written !== 'number') return
    updateDlActive.value = true
    updateProgress.value = Math.min(100, Math.max(1, Math.round((d.written / d.total) * 100)))
    updateResult.value = t('updateDownloadingPct', { pct: updateProgress.value })
  })
})

onUnmounted(() => {
  offUpdateStatus?.()
  offDlProgress?.()
})

// 手动检测与后台自动检查共用同一渲染逻辑，保证两条路径展示完全一致。
function applyUpdateStatus(result: UpdateStatus | null) {
  if (!result) { updateResult.value = t('updateError'); return }
  updateStatus.value = result
  if (result.state === 'up-to-date') {
    updateResult.value = t('updateUpToDate')
  } else if (result.state === 'available') {
    updateResult.value = t('updateAvailable') + ' ' + (result.availableVersion || '')
  } else if (result.state === 'ready') {
    updateResult.value = t('updateReady')
  } else if (result.state === 'error') {
    updateResult.value = (result.error || t('updateError'))
  }
}

async function checkForUpdates() {
  updateChecking.value = true
  updateResult.value = ''
  try {
    const result = unwrap<UpdateStatus>(await CheckForUpdates())
    applyUpdateStatus(result)
  } catch (e: any) {
    updateResult.value = getErrorMessage(e)
  } finally {
    updateChecking.value = false
  }
}

function openUrl(url: string) {
  Browser.OpenURL(url).catch((e: any) => {
    logErr('Settings', e)
  })
}

async function downloadUpdate() {
  updateDlActive.value = true
  updateProgress.value = 0
  updateResult.value = t('updateDownloading')
  try {
    const result = unwrap<UpdateStatus>(await DownloadUpdate())
    // 先复位下载态：原先 !result 的早退写在这行之前，一旦走到早退，
    // updateDlActive 就一直停在 true，进度条卡在界面上下不来。
    updateDlActive.value = false
    if (!result) { updateResult.value = t('updateError'); return }
    updateStatus.value = result
    if (result.state === 'ready') {
      updateResult.value = t('updateReady')
    } else if (result.state === 'error') {
      updateResult.value = result.error || t('updateError')
    }
  } catch (e: any) {
    updateDlActive.value = false
    updateResult.value = getErrorMessage(e)
  }
}

// 更新需要管理员权限（NSIS 安装器 REQUEST_EXECUTION_LEVEL=admin 会触发 UAC）。
// 首次点击"立即重启"先弹一次提示，确认后记忆(localStorage)，避免被 UAC 弹窗突兀打断。
const UPDATE_ADMIN_ACK = 'quickdock_update_admin_ack'
const showUpdateAdminConfirm = ref(false)

async function restartApp() {
  if (!localStorage.getItem(UPDATE_ADMIN_ACK)) {
    showUpdateAdminConfirm.value = true
    return
  }
  doRestart()
}

function onUpdateAdminConfirm() {
  localStorage.setItem(UPDATE_ADMIN_ACK, '1')
  showUpdateAdminConfirm.value = false
  doRestart()
}

async function doRestart() {
  updateResult.value = t('updateRestarting')
  try {
    unwrap(await RestartApp())
    // 成功时进程会在助手接管后异步退出，这里不会再有后续 UI 更新
  } catch (e: any) {
    // 以前这里是 catch (e) { logErr('SettingsModal', e) }，任何失败都被静默吞掉，用户只能看到"点了没反应"
    updateResult.value = getErrorMessage(e)
  }
}

// ---- 存储位置（自定义数据目录）----
const storageCur = ref('')
const storageDefault = ref('')
const storageIsCustom = ref(false)
const storageInput = ref('')
const storageMigrate = ref(true)
const storageMsg = ref('')
const storageLoading = ref(false)

async function loadStorage() {
  try {
    const r = unwrap<{ current: string; isCustom: boolean; default: string }>(await GetDataDir())
    if (r) {
      storageCur.value = r.current
      storageDefault.value = r.default
      storageIsCustom.value = r.isCustom
      storageInput.value = r.current
    }
  } catch (e: any) {
    logErr('Settings.storage', e)
  }
}

async function pickStorageDir() {
  try {
    const dir = unwrap<string | null>(await PickFolderPath(t('storagePick')))
    if (dir) storageInput.value = dir
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function applyStorage() {
  if (!storageInput.value) return
  storageLoading.value = true
  storageMsg.value = ''
  try {
    const res = await SetDataDir(storageInput.value, storageMigrate.value)
    if (res && res.code === 0) {
      storageMsg.value = res.msg || t('storageApply')
      await loadStorage()
    } else {
      storageMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    storageMsg.value = getErrorMessage(e)
  } finally {
    storageLoading.value = false
  }
}

async function resetStorage() {
  storageLoading.value = true
  storageMsg.value = ''
  try {
    const res = await ResetDataDir()
    if (res && res.code === 0) {
      storageMsg.value = res.msg || t('storageReset')
      await loadStorage()
    } else {
      storageMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    storageMsg.value = getErrorMessage(e)
  } finally {
    storageLoading.value = false
  }
}

// 切换到存储页时刷新状态（弹窗常驻，onMounted 只在首次执行）
watch(activePage, (k) => {
  if (k === 'storage') loadStorage()
})

// ---- 主题 / 语言 ----
// 主题状态由 App 统一管理（App.vue provide('theme')）：此处不再各存一份，否则用户切主题后
// 主题：统一消费 App.vue 通过 provide('theme') 注入的权威入口（同步 currentTheme + applyTheme + 持久化 + 跨窗口广播）
const { current: currentTheme, set: setTheme } = useTheme()
const themeOptions = computed(() => [
  { value: 'dark',   label: t('dark'), icon: Moon },
  { value: 'light',  label: t('light'), icon: Sun },
  { value: 'system', label: t('system'), icon: MonitorIcon },
])

const currentLocale = ref('zh-CN')

async function setLocale(newLocale: string) {
  currentLocale.value = newLocale
  i18n.global.locale.value = newLocale as 'zh-CN' | 'en-US'
  try {
    await SetValue('locale', newLocale)
  } catch (e) { logErr('SettingsModal', e) }
}

// 打开设置页时如有初始页面则导航，不再全局挂起热键
watch(() => props.visible, async (val) => {
    if (val) {
      if (props.initialPage) {
        // 兼容旧入口：'sync' / 'snapshot' 已合并为 'datasync'
        activePage.value = props.initialPage === 'sync' || props.initialPage === 'snapshot'
          ? 'datasync'
          : props.initialPage
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
  } catch (e) { logErr('SettingsModal', e) }
  try {
    const saved = unwrap<string>(await GetValue('theme'))
    if (saved === 'dark' || saved === 'light' || saved === 'system') {
      currentTheme.value = saved
    }
  } catch (e) { logErr('SettingsModal', e) }
  try {
    const days = unwrap<number>(await GetClipboardRetentionDays())
    clipboardRetentionDays.value = days ?? 30
  } catch (e) { logErr('SettingsModal', e) }
  try {
    autoStart.value = unwrap<boolean>(await GetAutoStart()) ?? false
  } catch (e) { logErr('SettingsModal', e) }
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
            <template v-for="group in menuGroups" :key="group.title">
              <div class="menu-group-title">{{ group.title }}</div>
              <button
                v-for="item in group.items"
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
            </template>

            <!-- 关于（含更新与日志，置底独立） -->
            <div class="about-row-wrap">
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
        </div>

        <!-- 右侧内容 -->
        <div class="settings-content">
          <!-- 关于（版本信息 / 更新 / 诊断） -->
          <div v-if="activePage === 'about'" class="content-page content-left">
            <!-- 产品信息 Hero -->
            <div class="about-hero">
              <div class="about-hero-main">
                <h3 class="about-name">{{ t('appName') }}</h3>
                <span class="about-badge">{{ t('version') }} {{ appVersion || '0.0.0' }}</span>
              </div>
              <p class="about-desc">{{ t('appDesc') }}</p>
              <p class="about-tech">{{ t('aboutTech') }}</p>
              <div class="about-links">
                <a class="about-link" href="#" @click.prevent="openUrl('https://parieses.github.io/quickdock/')">
                  <ExternalLink :size="12" />
                  <span class="about-link-tag">{{ t('aboutSiteMain') }}</span>
                  parieses.github.io/quickdock
                </a>
                <a class="about-link" href="#" @click.prevent="openUrl('https://parieses.github.io/quickdock-plugins/')">
                  <ExternalLink :size="12" />
                  <span class="about-link-tag">{{ t('aboutSitePlugins') }}</span>
                  parieses.github.io/quickdock-plugins
                </a>
                <a class="about-link" href="#" @click.prevent="openUrl('https://github.com/parieses/quickdock')">
                  <ExternalLink :size="12" />
                  <span class="about-link-tag">{{ t('aboutRepoMain') }}</span>
                  github.com/parieses/quickdock
                </a>
                <a class="about-link" href="#" @click.prevent="openUrl('https://github.com/parieses/quickdock-plugins')">
                  <ExternalLink :size="12" />
                  <span class="about-link-tag">{{ t('aboutRepoPlugins') }}</span>
                  github.com/parieses/quickdock-plugins
                </a>
              </div>
              <p class="about-copy">{{ t('aboutCopyright') }}</p>
            </div>

            <!-- 更新 -->
            <div class="section">
              <h3 class="section-title">{{ t('update') }}</h3>
              <p class="section-desc">{{ t('updateCheckingAuto') }}</p>

              <div class="action-row" style="margin-top:12px">
                <button class="btn btn-primary" :disabled="updateChecking" @click="checkForUpdates">
                  <RotateCcw :size="14" :class="{ spinning: updateChecking }" />
                  {{ updateChecking ? t('updateChecking') : t('updateCheckNow') }}
                </button>
              </div>

              <p v-if="updateResult" class="result-hint" :class="{ 'result-error': updateStatus?.state === 'error' }">{{ updateResult }}</p>

              <div v-if="updateDlActive" class="update-dl-bar">
                <div class="update-dl-fill" :style="{ width: updateProgress + '%' }"></div>
              </div>

              <div v-if="updateStatus?.state === 'available'" class="action-row" style="margin-top:12px">
                <button class="btn btn-primary" @click="downloadUpdate" :disabled="updateDlActive">
                  {{ updateDlActive
                    ? t('updateDownloadingPct', { pct: updateProgress })
                    : t('updateDownload') + ' ' + updateStatus.availableVersion }}
                </button>
                <button class="btn btn-secondary" :disabled="updateDlActive" @click="updateStatus.state = 'idle'">
                  {{ t('updateSkip') }}
                </button>
              </div>

              <div v-if="updateStatus?.state === 'available' && updateStatus.releaseNotes" class="update-notes">
                <div class="update-notes-title">{{ t('updateNotes') }}</div>
                <pre class="update-notes-body">{{ updateStatus.releaseNotes }}</pre>
              </div>

              <div v-if="updateStatus?.state === 'ready'" class="action-row" style="margin-top:12px">
                <button class="btn btn-primary update-restart-btn" @click="restartApp">
                  {{ t('updateRestart') }}
                </button>
              </div>
            </div>
          </div>

          <!-- 通用设置（开机自启 + 外观主题/语言） -->
          <div v-else-if="activePage === 'preferences'" class="content-page content-left">
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
            <div class="section">
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
          <div v-else-if="activePage === 'hotkeys'" class="content-page content-left">
            <HotkeySettings ref="hotkeyRef" />
          </div>

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

          <!-- 同步与快照（合并为一个菜单） -->
          <div v-else-if="activePage === 'datasync'" class="content-page content-left">
            <SettingsSync :visible="activePage === 'datasync'" @close="close" />
            <SettingsSnapshot :visible="activePage === 'datasync'" @close="close" />
          </div>

          <!-- 存储位置（自定义数据目录） -->
          <div v-else-if="activePage === 'storage'" class="content-page content-left">
            <div class="section">
              <h3 class="section-title">{{ t('storageLocation') }}</h3>
              <p class="section-desc">{{ t('storageDesc') }}</p>

              <div class="setting-row">
                <label class="setting-label">{{ t('storageCurrent') }}</label>
                <div class="setting-control storage-current-path">{{ storageCur }}</div>
              </div>
              <p class="field-hint" v-if="storageIsCustom">{{ t('storageCustomNote') }}</p>
              <p class="field-hint" v-else>{{ t('storageDefaultNote') }}</p>

              <div class="setting-row">
                <label class="setting-label">{{ t('storagePath') }}</label>
                <div class="setting-control">
                  <input
                    v-model="storageInput"
                    class="field-input storage-path-input"
                    :placeholder="t('storageDefault')"
                  />
                  <button class="btn btn-secondary" @click="pickStorageDir">{{ t('storagePick') }}</button>
                </div>
              </div>

              <label class="field toggle-field" style="cursor: pointer; margin-bottom: 16px;">
                <input type="checkbox" v-model="storageMigrate" class="field-checkbox" />
                <span>{{ t('storageMigrate') }}</span>
              </label>

              <div class="action-row">
                <button class="btn btn-primary" :disabled="storageLoading" @click="applyStorage">
                  {{ t('storageApply') }}
                </button>
                <button
                  class="btn btn-secondary"
                  v-if="storageIsCustom"
                  :disabled="storageLoading"
                  @click="resetStorage"
                >
                  {{ t('storageReset') }}
                </button>
                <button class="btn btn-secondary" @click="restartApp">{{ t('storageRestart') }}</button>
              </div>

              <p class="field-hint">{{ t('storageRestartHint') }}</p>
              <p class="result-hint" v-if="storageMsg">{{ storageMsg }}</p>
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

          <div v-else-if="activePage === 'winmgr'" class="content-page content-left">
            <SettingsWinmgr />
          </div>

          <!-- 日志查看器（从导航栏迁入的专属设置项） -->
          <div v-else-if="activePage === 'logs'" class="content-page content-logs">
            <LogViewer />
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

  <ConfirmDialog
    :visible="showUpdateAdminConfirm"
    :message="t('updateAdminRequired')"
    @confirm="onUpdateAdminConfirm"
    @cancel="showUpdateAdminConfirm = false"
  />
</template>


