<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, toRef, watch, inject, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { X, Palette, Keyboard, Database, Info, ChevronRight, Sun, Moon, Monitor as MonitorIcon, RotateCcw, Bot, Wrench, ExternalLink, ScrollText, History, ListOrdered, Search, Clipboard } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'
import { useTheme } from '../composables/useTheme'
import { unwrap } from '../utils/api'
import { i18n } from '../i18n'
import { pinyinMatch } from '../utils/pinyin'
import { Events, Browser } from '@wailsio/runtime'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const store = useWorkspaceStore()
import HotkeySettings from './HotkeySettings.vue'
import SettingsClipboardRules from './SettingsClipboardRules.vue'
import SettingsHistory from './SettingsHistory.vue'
import SettingsAI from './SettingsAI.vue'
import SettingsSnapshot from './SettingsSnapshot.vue'
import SettingsSync from './SettingsSync.vue'
import SettingsTools from './SettingsTools.vue'
import LogViewer from './LogViewer.vue'
import ActivityPage from './ActivityPage.vue'
import RecycleBinPage from './RecycleBinPage.vue'
import SettingsWinmgr from './SettingsWinmgr.vue'
import SettingsNav from './SettingsNav.vue'

import {
  GetClipboardRetentionDays,
  SetClipboardRetentionDays,
  CleanupClipboardNow,
  SuspendHotkeys,
  ResumeHotkeys,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { GetAutoStart, SetAutoStart, GetDataDir, SetDataDir, ResetDataDir, NormalizeDataDir, GetUiZoom, SetUiZoom, RestartApp as RestartAppPlain } from '../../bindings/quickdock/services/appservice'
import { StorageMigrate } from '../../bindings/quickdock/services/storageservice'
import { GetValue, SetValue } from '../../bindings/quickdock/services/appservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { GetAppVersion, CheckForUpdates, DownloadUpdate, RestartApp, GetUpdateState } from '../../bindings/quickdock/services/update/updateservice'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
// UpdateStatus 改从 ../types 取：接口统一返回 ApiResult 后 bindings 不再生成它的 model
import type { ToastAPI, UpdateStatus } from '../types'
import { useWorkspaceStore } from '../stores/workspace'
import { getErrorMessage } from '../utils/error'
import { markSeen } from '../composables/useNavBadges'
import ConfirmDialog from './ConfirmDialog.vue'

const props = defineProps<{
  visible: boolean
  initialPage?: string
}>()

const emit = defineEmits<{ close: [] }>()

const activePage = ref<string | null>(null)
// 无论从哪个入口打开设置（footer 按钮 / AI 页 / 命令面板），都视为看过设置内的
// 待读信息（崩溃记录角标）—— 只靠侧栏 footer 清水位，会漏掉其它打开路径
watch(() => props.visible, (v) => { if (v) markSeen('settings') })
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)
const hotkeyRef = ref<InstanceType<typeof HotkeySettings> | null>(null)
const searchRef = ref<HTMLInputElement | null>(null)

// ---- 设置项搜索 ----
// 11 个设置页靠左侧菜单逐个翻已经不快。搜索框按「标签 + 描述 + 内部 key + 拼音」
// 过滤菜单项：key 参与匹配是为了让英文输入（如 sync、ai）在中文界面下也能命中。
const settingsQuery = ref('')

// 菜单项按「一件事一项」划分，不按实现模块划分：
//   · 剪贴板 = 历史保留/清理 + 自动规则（原来拆成「剪贴板历史」「剪贴板规则」两项，
//     而前者只有保留天数一个控件，独立成页是浪费一次点击）
//   · 数据与备份 = 存储位置 + 同步 + 快照（三件事都是「数据放哪、怎么留后路」）
//   · 历史 = 最近动态 + 回收站（都是「回头看一眼」型信息，页内用分段切换）
// 日志与关于不属于配置，置底独立成行（见 footerItems）。
const menuItems = computed(() => [
  { key: 'preferences', label: t('preferences'), icon: Palette, desc: t('autoStart') + ' / ' + t('theme') + ' / ' + t('language') + ' / ' + t('uiZoom') },
  // 导航显隐与排序：它是「隐藏了怎么找回来」的唯一兜底入口，放在通用组开头。
  // 为什么必须在设置里：侧栏可以被裁剪，但这个入口不属于导航项，永远可达。
  { key: 'navigation', label: t('navSettings'), icon: ListOrdered, desc: t('navSettingsDesc') },
  { key: 'hotkeys',    label: t('hotkeySettings'), icon: Keyboard, desc: t('setHotkeysDesc') },
  { key: 'clipboard',  label: t('clipboard'),      icon: Clipboard, desc: t('setClipboardDesc') },
  { key: 'backup',     label: t('setBackup'),      icon: Database,  desc: t('setBackupDesc') },
  { key: 'history',    label: t('setHistory'),     icon: History,   desc: t('setHistoryDesc') },
  { key: 'tools',      label: t('openTool'),       icon: Wrench,    desc: t('toolManageDesc') },
  { key: 'winmgr',     label: t('winmgr'),         icon: MonitorIcon, desc: t('winmgrDesc') },
  { key: 'ai',         label: t('navAi'),          icon: Bot,       desc: t('aiSettingsDesc') },
])

// ---- 旧 key 别名 ----
// 合并后旧 key 会失效，但深链散落在各入口（Onboarding/EmptyGuide 传 'snapshot'、
// 备忘单传 'hotkeys'、AI 页传 'ai'），改一次 key 就得全局搜一遍。这里集中兜底：
// 任何历史 key 进来都会被翻译成当前 key，命不中的原样返回。
const PAGE_ALIASES: Record<string, string> = {
  data: 'clipboard', cliprule: 'clipboard',
  storage: 'backup', datasync: 'backup', sync: 'backup', snapshot: 'backup',
  activity: 'history', recycle: 'history',
}
function resolvePage(key: string): string {
  return PAGE_ALIASES[key] ?? key
}

function menuMatches(m: { key: string; label: string; desc: string }, qLC: string): boolean {
  if (m.label.toLowerCase().includes(qLC)) return true
  if (m.desc.toLowerCase().includes(qLC)) return true
  if (m.key.toLowerCase().includes(qLC)) return true
  return pinyinMatch(m.label, qLC) || pinyinMatch(m.desc, qLC)
}

const menuGroups = computed(() => {
  const qLC = settingsQuery.value.trim().toLowerCase()
  const byKey = Object.fromEntries(menuItems.value.map((m) => [m.key, m]))
  const defs: { title: string; keys: string[] }[] = [
    { title: t('menuGroupBasics'), keys: ['preferences', 'navigation', 'hotkeys'] },
    { title: t('menuGroupData'), keys: ['clipboard', 'backup', 'history'] },
    { title: t('menuGroupService'), keys: ['tools', 'winmgr', 'ai'] },
  ]
  return defs
    .map((g) => ({ title: g.title, items: g.keys.map((k) => byKey[k]).filter(Boolean).filter((m) => !qLC || menuMatches(m, qLC)) }))
    .filter((g) => g.items.length > 0)
})

// 置底独立成行：日志与关于都不是「配置项」，是「了解这个应用」的入口，
// 混在服务组里会让那一组看起来像四个互不相干的东西。同样参与搜索。
const footerItems = computed(() => {
  const qLC = settingsQuery.value.trim().toLowerCase()
  const all = [
    { key: 'logs', label: t('navLogs'), icon: ScrollText, desc: t('logsDesc') },
    { key: 'about', label: t('appName'), icon: Info, desc: t('aboutDesc') },
  ]
  if (!qLC) return all
  return all.filter((m) => menuMatches(m, qLC))
})

// 回车直达第一个匹配项：搜索的意图就是「少点一次」
function searchGoFirst() {
  const first = menuGroups.value[0]?.items[0]
  if (first) selectMenu(first.key)
  else if (footerItems.value[0]) selectMenu(footerItems.value[0].key)
}
function onSearchKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') { e.preventDefault(); searchGoFirst() }
  else if (e.key === 'Escape') {
    e.preventDefault()
    if (settingsQuery.value) settingsQuery.value = ''
    else close()
  }
}

// 打开设置且停在根菜单时聚焦搜索框（带 initialPage 的直接进目标页，不抢焦点）
watch(() => props.visible, (v) => {
  if (v && !props.initialPage && !activePage.value) {
    nextTick(() => searchRef.value?.focus())
  }
})

function selectMenu(key: string) {
  activePage.value = resolvePage(key)
  // 选了就走：过滤条件留着，从设置页返回根菜单时反而要找半天为什么少了一半
  settingsQuery.value = ''
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
  // 订阅数据目录迁移进度（复制核心数据 → 清理旧目录 → 环境数据重新下载）。
  // 提前订阅而非等到点「应用」才订阅：后端是异步任务，可能在调用返回前就已推了首帧进度。
  watchMigrateProgress()
})

onUnmounted(() => {
  offUpdateStatus?.()
  offDlProgress?.()
  offMigrate?.()
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

// ---- 存储位置迁移进度 ----
// 迁移是运行时异步执行（后端打包旧数据 → 复制到新目录 → 解压 → 切目录 → 改环境变量 → 清理旧目录），
// 进度经 quickdock:storage:migrate 事件推送，这里订阅后渲染进度条。
const migratePhase = ref('')
const migrateTotalFiles = ref(0)
const migrateDoneFiles = ref(0)
const migrateTotalBytes = ref(0)
const migrateDoneBytes = ref(0)
const migrateCurrent = ref('')
const migrateRunning = ref(false)
const migrateDone = ref(false)
const migrateSkipped = ref<string[]>([])

/** 目标目录回显：用户选 D 盘 → D:\.quickdock（后端规则，前端只展示）。 */
const storageTargetHint = ref('')

/** 迁移进度百分比：按字节算（比文件数更贴近用户感知的大小）。字节未知时回退按文件数。 */
const migratePercent = computed(() => {
  if (migrateTotalBytes.value > 0) {
    return Math.min(100, Math.round((migrateDoneBytes.value / migrateTotalBytes.value) * 100))
  }
  if (migrateTotalFiles.value > 0) {
    return Math.min(100, Math.round((migrateDoneFiles.value / migrateTotalFiles.value) * 100))
  }
  return migratePhase.value === 'done' ? 100 : 0
})

/** 已复制的体积文案。 */
const migrateSizeText = computed(() => {
  const mb = (n: number) => (n / 1024 / 1024).toFixed(1)
  if (migrateTotalBytes.value > 0) return `${mb(migrateDoneBytes.value)} / ${mb(migrateTotalBytes.value)} MB`
  return `${migrateDoneFiles.value} / ${migrateTotalFiles.value}`
})

/** 迁移阶段的用户可读文案。 */
const migratePhaseText = computed(() => {
  switch (migratePhase.value) {
    case 'scanning':
      return t('storageMigrateScanning')
    case 'packing':
      return t('storageMigratePacking')
    case 'copying':
      return t('storageMigrateCopying')
    case 'extracting':
      return t('storageMigrateExtracting')
    case 'cleanup':
      return t('storageMigrateCleanup')
    case 'downloading':
    case 'rebind':
    case 'envvars':
      return t('storageMigrateDownloading')
    case 'done':
      return t('storageMigrateDone')
    default:
      return ''
  }
})

/** 订阅后端迁移进度事件。 */
let offMigrate: (() => void) | null = null
function watchMigrateProgress() {
  if (offMigrate) return
  offMigrate = Events.On('quickdock:storage:migrate', (payload: any) => {
    // Wails v3 发结构体时载荷可能被包一层 data，必须兼容两种形态——
    // 否则 payload.phase 是 undefined，进度条永远停在 0/0（真踩过）。
    const p = (payload?.data ?? payload) as any
    if (!p || typeof p !== 'object') return
    migratePhase.value = p.phase || ''
    migrateTotalFiles.value = p.totalFiles || 0
    migrateDoneFiles.value = p.doneFiles || 0
    migrateTotalBytes.value = p.totalBytes || 0
    migrateDoneBytes.value = p.doneBytes || 0
    migrateCurrent.value = p.current || ''
    if (Array.isArray(p.skipped)) migrateSkipped.value = p.skipped
    if (p.message) storageMsg.value = p.message
    if (p.phase === 'done') {
      migrateRunning.value = false
      migrateDone.value = true
      // 迁移完成 → 刷新显示的当前路径（后端已写配置）
      loadStorage()
    }
    if (p.phase === 'error') {
      migrateRunning.value = false
      storageMsg.value = p.err || t('updateError')
    }
  })
}

async function loadStorage() {
  try {
    const r = unwrap<{ current: string; isCustom: boolean; default: string }>(await GetDataDir())
    if (r) {
      storageCur.value = r.current
      storageDefault.value = r.default
      storageIsCustom.value = r.isCustom
      storageInput.value = r.current
      await updateStorageTargetHint()
    }
  } catch (e: any) {
    logErr('Settings.storage', e)
  }
}

/**
 * 更新「实际将迁移到」的目标目录回显。
 *
 * 规则由后端 platform.NormalizeTargetDir 决定（唯一实现）：
 * 选 D 盘根目录 → D:\.quickdock；选 D:\data → D:\data\.quickdock。
 * 前端只负责展示——自己复制一份规则必然与后端漂移。
 */
async function updateStorageTargetHint() {
  const p = storageInput.value?.trim()
  if (!p) {
    storageTargetHint.value = ''
    return
  }
  try {
    const r = unwrap<string>(await NormalizeDataDir(p))
    // 与当前目录相同时不提示（后端会直接拒绝迁移）。
    storageTargetHint.value = r && r !== storageCur.value ? r : ''
  } catch (e: any) {
    storageTargetHint.value = ''
    logErr('Settings.storage', e)
  }
}

async function pickStorageDir() {
  try {
    const dir = unwrap<string | null>(await PickFolderPath(t('storagePick')))
    if (dir) {
      storageInput.value = dir
      await updateStorageTargetHint()
    }
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function applyStorage() {
  if (!storageInput.value) return
  storageLoading.value = true
  storageMsg.value = ''
  try {
    // 勾选「迁移现有数据」时走迁移接口（带进度条，运行时执行，完成后提示重启）；
    // 未勾选则只是切换路径（新目录从空开始，旧数据留在原地）。
    if (storageMigrate.value) {
      watchMigrateProgress()
      migrateRunning.value = true
      migrateDone.value = false
      migratePhase.value = 'scanning'
      migrateTotalFiles.value = 0
      migrateDoneFiles.value = 0
      migrateTotalBytes.value = 0
      migrateDoneBytes.value = 0
      migrateCurrent.value = ''
      migrateSkipped.value = []
      const res = await StorageMigrate(storageInput.value)
      if (res && res.code === 0) {
        storageMsg.value = t('storageMigrateStarted')
      } else {
        migrateRunning.value = false
        storageMsg.value = res?.msg || t('updateError')
      }
      return
    }
    const res = await SetDataDir(storageInput.value, false)
    if (res && res.code === 0) {
      storageMsg.value = res.msg || t('storageApply')
      await loadStorage()
    } else {
      storageMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    migrateRunning.value = false
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
  if (k === 'backup') loadStorage()
  if (k === 'preferences') loadUiZoom()
})

// 存储位置页的「重启」：走 AppService.RestartApp（真重启，脱离式拉起新进程）。
// 以前这里复用更新页的 restartApp()——那个是「就地替换更新」专用，无待更新时必然失败，
// 且错误只显示在更新页，存储页表现为「点了没反应」，数据目录迁移也因此卡住。
// 现在错误直接落在本页的 storageMsg，用户能看到真实原因。
async function restartAppForStorage() {
  storageLoading.value = true
  storageMsg.value = t('storageRestarting')
  try {
    const res = await RestartAppPlain()
    if (res && res.code === 0) {
      // 成功时进程会在新实例接管后异步退出，不再有后续 UI 更新
      storageMsg.value = t('storageRestarting')
    } else {
      storageMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    storageMsg.value = getErrorMessage(e)
  } finally {
    storageLoading.value = false
  }
}

// ---- 界面缩放 ----
// 与存储位置同源：缩放写在 platform 配置（quickdock.json）而非数据库里，因为窗口的
// Zoom 与尺寸在「创建时」就固定了，而数据库要等 Wails 服务启动才打开——那时窗口早已建好。
// 同理改完不能即时生效，必须重启应用（见下面 restartForUiZoom）。
const uiZoomValue = ref(1)
const uiZoomMsg = ref('')
const uiZoomBusy = ref(false)
// 只暴露三档：缩放会同时改窗口尺寸与内容，任意数值很容易得到内容被裁的畸形窗口。
// 用 computed 包一层，切语言时标签跟着变（t 是响应式的）。
const uiZoomLevels = computed(() => [
  { value: 1, label: t('uiZoomStandard') },
  { value: 1.1, label: t('uiZoomLarge') },
  { value: 1.2, label: t('uiZoomXLarge') },
])

async function loadUiZoom() {
  try {
    const v = unwrap<number>(await GetUiZoom())
    if (typeof v === 'number') uiZoomValue.value = v
  } catch (e) {
    logErr('Settings.uiZoom', e)
  }
}

async function selectUiZoom(v: number) {
  if (uiZoomBusy.value || v === uiZoomValue.value) return
  uiZoomBusy.value = true
  uiZoomMsg.value = ''
  try {
    const res = await SetUiZoom(v)
    if (res && res.code === 0) {
      uiZoomValue.value = v
      uiZoomMsg.value = res.msg || t('uiZoomRestartHint')
    } else {
      uiZoomMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    uiZoomMsg.value = getErrorMessage(e)
  } finally {
    uiZoomBusy.value = false
  }
}

// 缩放改完必须重启才生效：走 AppService.RestartApp（真重启，脱离式拉起新进程），
// 与存储位置页同一条路径——不能用更新页的 restartApp，那个是「就地替换更新」专用，
// 无待更新时必然失败，表现为「点了没反应」。
async function restartForUiZoom() {
  uiZoomBusy.value = true
  uiZoomMsg.value = t('uiZoomRestarting')
  try {
    const res = await RestartAppPlain()
    if (res && res.code === 0) {
      // 成功时进程会在新实例接管后异步退出，不再有后续 UI 更新
      uiZoomMsg.value = t('uiZoomRestarting')
    } else {
      uiZoomMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    uiZoomMsg.value = getErrorMessage(e)
  } finally {
    uiZoomBusy.value = false
  }
}

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
        // 兼容旧入口：'sync' / 'snapshot' / 'data' / 'storage' … 经 PAGE_ALIASES 落到当前页
        activePage.value = resolvePage(props.initialPage)
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

          <!-- 设置项搜索：根菜单常驻；进了具体设置页也留着，回来时不用重输 -->
          <div class="settings-search">
            <div class="settings-search-box">
              <Search :size="14" class="settings-search-icon" />
              <input
                ref="searchRef"
                v-model="settingsQuery"
                type="text"
                :placeholder="t('settingsSearchPh')"
                autocomplete="off"
                @keydown="onSearchKeydown"
              />
            </div>
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

            <p v-if="settingsQuery.trim() && !menuGroups.length && !footerItems.length" class="settings-search-empty">
              {{ t('settingsNoMatch') }}
            </p>

            <!-- 置底：日志 / 关于（都不是配置项，单独一行不参与分组） -->
            <div v-if="footerItems.length" class="about-row-wrap">
              <button
                v-for="item in footerItems"
                :key="item.key"
                :class="['menu-row about-row', { active: activePage === item.key }]"
                @click="selectMenu(item.key)"
              >
                <component :is="item.icon" :size="18" class="menu-row-icon" />
                <div class="menu-row-text">
                  <span class="menu-row-label">{{ item.label }}</span>
                  <span class="menu-row-desc">{{ item.desc }}</span>
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
            <div class="section">
              <h3 class="section-title">{{ t('uiZoom') }}</h3>
              <p class="section-desc">{{ t('uiZoomDesc') }}</p>
              <div class="locale-selector">
                <button
                  v-for="lv in uiZoomLevels"
                  :key="lv.value"
                  :class="['locale-btn', { active: uiZoomValue === lv.value }]"
                  :disabled="uiZoomBusy"
                  @click="selectUiZoom(lv.value)"
                >{{ lv.label }}</button>
              </div>
              <!-- 「立即重启」只在改完出现：缩放是窗口创建时固定的，不重启等于没改 -->
              <div v-if="uiZoomMsg" class="action-row">
                <p class="result-hint">{{ uiZoomMsg }}</p>
                <button class="btn btn-secondary" :disabled="uiZoomBusy" @click="restartForUiZoom">{{ t('uiZoomRestart') }}</button>
              </div>
            </div>
          </div>
          <div v-else-if="activePage === 'hotkeys'" class="content-page content-left">
            <HotkeySettings ref="hotkeyRef" />
          </div>
          <!-- 剪贴板：历史保留/清理 + 自动规则（原「剪贴板历史」只有保留天数一个控件，
               单独占一页是浪费一次点击，合并进规则页由「自动规则」带标题区分） -->
          <div v-else-if="activePage === 'clipboard'" class="content-page content-left">
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
            <SettingsClipboardRules />
          </div>

          <!-- 数据与备份：存储位置 + 同步 + 快照（原「存储位置」「同步与快照」两项合并） -->
          <div v-else-if="activePage === 'backup'" class="content-page content-left">
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
                    @input="updateStorageTargetHint"
                  />
                  <button class="btn btn-secondary" @click="pickStorageDir">{{ t('storagePick') }}</button>
                </div>
              </div>
              <!-- 目标目录回显：用户选 D 盘时显示 D:\.quickdock，避免疑惑「怎么多了个子目录」 -->
              <p class="field-hint" v-if="storageTargetHint">
                {{ t('storageMigrateTarget') }}：{{ storageTargetHint }}
              </p>

              <label class="field toggle-field" style="cursor: pointer; margin-bottom: 16px;">
                <input type="checkbox" v-model="storageMigrate" class="field-checkbox" :disabled="migrateRunning" />
                <span>{{ t('storageMigrate') }}</span>
              </label>

              <!-- 迁移进度条：打包 → 复制 → 解压 → 切目录 → 改环境变量 → 清理旧目录 -->
              <div v-if="migrateRunning || migrateDone" class="storage-migrate-progress">
                <div class="storage-migrate-head">
                  <span class="storage-migrate-phase">{{ migratePhaseText }}</span>
                  <span class="storage-migrate-size">{{ migrateSizeText }}</span>
                </div>
                <div class="storage-migrate-bar">
                  <div class="storage-migrate-fill" :style="{ width: migratePercent + '%' }" />
                </div>
                <p class="field-hint" v-if="migrateCurrent">
                  {{ t('storageMigrateCurrent') }}：{{ migrateCurrent }}
                </p>
              </div>

              <div class="action-row">
                <button class="btn btn-primary" :disabled="storageLoading || migrateRunning" @click="applyStorage">
                  {{ t('storageApply') }}
                </button>
                <button
                  class="btn btn-secondary"
                  v-if="storageIsCustom"
                  :disabled="storageLoading || migrateRunning"
                  @click="resetStorage"
                >
                  {{ t('storageReset') }}
                </button>
                <button
                  class="btn btn-secondary"
                  :disabled="storageLoading || migrateRunning"
                  @click="restartAppForStorage"
                >
                  {{ t('storageRestart') }}
                </button>
              </div>

              <p class="field-hint">{{ t('storageRestartHint') }}</p>
              <p class="result-hint" v-if="storageMsg">{{ storageMsg }}</p>
            </div>
            <SettingsSync :visible="activePage === 'backup'" @close="close" />
            <SettingsSnapshot :visible="activePage === 'backup'" @close="close" />
          </div>
          <!-- 历史：最近动态 + 回收站（页内分段切换，两者都是「回头看一眼」型信息） -->
          <div v-else-if="activePage === 'history'" class="content-page content-flush">
            <SettingsHistory />
          </div>

          <!-- AI 助手 -->
          <div v-else-if="activePage === 'ai'" class="content-page content-left">
            <SettingsAI :visible="activePage === 'ai'" />
          </div>

          <!-- 导航项显隐 / 排序：被隐藏的导航项在这里总能找回来 -->
          <div v-else-if="activePage === 'navigation'" class="content-page content-left">
            <SettingsNav :visible="activePage === 'navigation'" />
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

          <!-- 最近动态（从导航栏迁入：属于「偶尔回头看一眼」的信息，不值得占一级导航） -->

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

