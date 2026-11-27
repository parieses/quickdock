<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, computed, onMounted, onUnmounted, toRef, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { X, Palette, Keyboard, Database, Info, Sun, Moon, Monitor as MonitorIcon, RotateCcw, Bot, Wrench, ExternalLink, ScrollText, History, ListOrdered, Clipboard, Bell, ShieldCheck, Settings } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'
import { useTheme } from '../composables/useTheme'
import { unwrap } from '../utils/api'
import { i18n } from '../i18n'
import { pinyinMatch } from '../utils/pinyin'
import { Events, Browser } from '@wailsio/runtime'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
import HotkeySettings from './HotkeySettings.vue'
import SettingsClipboardRules from './SettingsClipboardRules.vue'
import SettingsHistory from './SettingsHistory.vue'
import QdTabs from './QdTabs.vue'
import QdSwitch from './QdSwitch.vue'
import QdSearchBox from './QdSearchBox.vue'
import QdModal from './QdModal.vue'
import SettingsAI from './SettingsAI.vue'
import SettingsSnapshot from './SettingsSnapshot.vue'
import SettingsSync from './SettingsSync.vue'
import SettingsTools from './SettingsTools.vue'
import LogViewer from './LogViewer.vue'
import ActivityPage from './ActivityPage.vue'
import RecycleBinPage from './RecycleBinPage.vue'
import SettingsWinmgr from './SettingsWinmgr.vue'
import SettingsNotify from './SettingsNotify.vue'
import SettingsNav from './SettingsNav.vue'
import SettingsSecurity from './SettingsSecurity.vue'

import {
  GetClipboardRetentionDays,
  SetClipboardRetentionDays,
  CleanupClipboardNow,
  SuspendHotkeys,
  ResumeHotkeys,
} from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { GetSearchExclusions, SaveSearchExclusions, GetSearchShowHidden, SetSearchShowHidden } from '../../bindings/quickdock/services/appservice'
import { GetAutoStart, SetAutoStart, GetDataDir, SetDataDir, ResetDataDir, NormalizeDataDir, GetUiZoom, SetUiZoom, GetWindowBackdrop, SetWindowBackdrop, RestartApp as RestartAppPlain, SelectLocalFile, SelectSavePath } from '../../bindings/quickdock/services/appservice'
import { StorageMigrate, DataExport, DataExportSelected, DataImport, DataImportInspect } from '../../bindings/quickdock/services/storageservice'
import { navEntries } from '../pages/registry'
import { GetValue, SetValue } from '../../bindings/quickdock/services/appservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { GetAppVersion, CheckForUpdates, DownloadUpdate, RestartApp, GetUpdateState } from '../../bindings/quickdock/services/update/updateservice'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
// UpdateStatus 改从 ../types 取：接口统一返回 ApiResult 后 bindings 不再生成它的 model
import type { ToastAPI, UpdateStatus } from '../types'
import { getErrorMessage } from '../utils/error'
import { navBadges } from '../composables/useNavBadges'
import ConfirmDialog from './ConfirmDialog.vue'
import { parseReleaseNotes } from '../utils/releaseNotes'
// 官方站点地址唯一来源：关于页与工作空间引导共用一份（见 utils/siteLinks.ts）
import { SITE_GUIDE_URL, SITE_GUIDE_TEXT } from '../utils/siteLinks'

import { reportError } from '../utils/reportError'
import { writeStored } from '../utils/storage'
const props = defineProps<{
  visible: boolean
  initialPage?: string
}>()

const emit = defineEmits<{ close: [] }>()

const { markSeen, settingsBadge } = navBadges()

const activePage = ref<string | null>(null)
// 二级 tab：'ai:usage' 里的 usage。只有 AI 与历史两页内部有分段，其余传空。
// 与 activePage 分开存是因为它要**原样透传**给子组件——父组件不该知道 AI 页有哪些 tab，
// 合法性由子组件自己校验（脏值就当没传）。
const activeTab = ref('')

// 「存储与同步」页内三段。与 AI 页的分段不同，这一页的三块内容都在本组件里，
// 所以段值单独存一份：activeTab 只用来接深链（'backup:sync'），平时由按钮直接改这里。
// 深链值不合法（含空）一律落回第一段——深链是「尽量帮忙」，不该因为写错就停在空白页。
type BackupTab = 'storage' | 'sync' | 'snapshot'
const backupTab = ref<BackupTab>('storage')
const backupTabs = computed<{ id: BackupTab; label: string }[]>(() => [
  { id: 'storage', label: t('storageLocation') },
  { id: 'sync', label: t('sync') },
  { id: 'snapshot', label: t('snapshot') },
])
watch(activeTab, (v) => {
  backupTab.value = v === 'sync' || v === 'snapshot' ? v : 'storage'
})
// 崩溃记录角标的已读时机 = 真正进入「设置 → 日志」页，不是打开设置弹窗。
// 原先「打开即清」+「设置菜单没有任何标识」= 用户看到侧栏红点，点开后一瞬间消失，
// 根本不知道它指什么。现在红点一直保留到进日志页；进入时先快照数量（必须在
// markSeen 之前取），供日志页顶部按钮与「崩溃记录」分组的「新增 N」使用。
// 无论从哪个入口打开（footer / 通知中心 / 命令面板 / 深链），activePage 都会收敛到这里。
const entryCrashCount = ref(0)
watch(activePage, (p) => {
  if (p !== 'logs') return
  entryCrashCount.value = settingsBadge.value?.count ?? 0
  markSeen('settings')
})
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)
const hotkeyRef = ref<InstanceType<typeof HotkeySettings> | null>(null)

// ---- 窗口材质 ----
// 能力探测决定「有没有这个设置项」：Win10 / 非 Windows 下 supported 恒为 false，
// 菜单描述与 preferences 页都不出现任何相关内容，界面与加这个功能之前一致。
// 声明放在 menuItems 之前：desc 里要读 backdropSupported，避免 computed 里
// 引用一个尚未初始化的 ref。
const backdropSupported = ref(false)
const backdropValue = ref('off')
const backdropMsg = ref('')
const backdropBusy = ref(false)

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
  // 窗口材质只在能力探测通过时才拼进描述：Win10 上菜单文案也不多出一个
  // 用户点了却找不到的词（设置项搜索会拿 desc 参与匹配）。
  { key: 'preferences', label: t('preferences'), icon: Palette, desc: [t('autoStart'), t('theme'), t('language'), t('uiZoom'), ...(backdropSupported.value ? [t('windowBackdrop')] : [])].join(' / ') },
  // 导航显隐与排序：它是「隐藏了怎么找回来」的唯一兜底入口，放在通用组开头。
  // 为什么必须在设置里：侧栏可以被裁剪，但这个入口不属于导航项，永远可达。
  { key: 'navigation', label: t('navSettings'), icon: ListOrdered, desc: t('navSettingsDesc') },
  { key: 'hotkeys',    label: t('hotkeySettings'), icon: Keyboard, desc: t('setHotkeysDesc') },
  { key: 'security',   label: t('securityTitle'),  icon: ShieldCheck, desc: t('setSecurityDesc') },
  { key: 'clipboard',  label: t('clipboard'),      icon: Clipboard, desc: t('setClipboardDesc') },
  { key: 'backup',     label: t('setBackup'),      icon: Database,  desc: t('setBackupDesc') },
  { key: 'history',    label: t('setHistory'),     icon: History,   desc: t('setHistoryDesc') },
  { key: 'tools',      label: t('openTool'),       icon: Wrench,    desc: t('toolManageDesc') },
  { key: 'winmgr',     label: t('winmgr'),         icon: MonitorIcon, desc: t('winmgrDesc') },
  { key: 'notify',     label: t('mon_nf_title'),   icon: Bell,      desc: t('notifyUnifiedDesc') },
  { key: 'ai',         label: t('navAi'),          icon: Bot,       desc: t('aiSettingsDesc') },
])

// ---- 旧 key 别名 ----
// 合并后旧 key 会失效，但历史深链散落在各入口（旧版引导页传 'snapshot'、
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

/**
 * 深链定位：'ai:usage' → 页 ai、页内分段 usage。
 *
 * 分隔符用 ':' 而不是 '/':通知中心的 refType 本来就是「settings:xxx」这种冒号式，
 * 复用同一个符号，后端以后推 settings:ai:usage 前端不用改。
 * 二级段原样交给子组件，合法性由子组件判断（父组件不该知道 AI 页有哪几个 tab）。
 */
function applyInitialPage(raw?: string) {
  if (!raw) return
  const idx = raw.indexOf(':')
  activePage.value = resolvePage(idx < 0 ? raw : raw.slice(0, idx))
  activeTab.value = idx < 0 ? '' : raw.slice(idx + 1)
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
    { title: t('menuGroupBasics'), keys: ['preferences', 'navigation', 'hotkeys', 'security'] },
    { title: t('menuGroupData'), keys: ['clipboard', 'backup', 'history'] },
    { title: t('menuGroupService'), keys: ['tools', 'winmgr', 'notify', 'ai'] },
  ]
  return defs
    .map((g) => ({ title: g.title, items: g.keys.map((k) => byKey[k]).filter(Boolean).filter((m) => !qLC || menuMatches(m, qLC)) }))
    .filter((g) => g.items.length > 0)
})

// 置底独立成行：日志与关于都不是「配置项」，是「了解这个应用」的入口，
// 混在服务组里会让那一组看起来像四个互不相干的东西。同样参与搜索。
const footerItems = computed(() => {
  const qLC = settingsQuery.value.trim().toLowerCase()
  const badge = settingsBadge.value
  // .menu-row-desc 在公共样式里是 display:none，光改 desc 或 title 用户看不到，
  // 所以「红点指什么」必须额外用一个可见标签 + 同色数字角标说明。
  const crashHint = badge ? t('badgeNewCrash', { n: badge.count }) : ''
  const all = [
    { key: 'logs', label: t('navLogs'), icon: ScrollText,
      desc: crashHint || t('logsDesc'), alert: crashHint,
      badge: badge?.count ?? 0, badgeTone: badge?.tone ?? 'danger' },
    { key: 'about', label: t('appName'), icon: Info, desc: t('aboutDesc'),
      alert: '', badge: 0, badgeTone: 'info' },
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

// ---- 记住上次停留的页 ----
// 以前每次打开都落「通用」并停在空状态；现在关掉后记住停在哪一页，下次直接回去。
// 设置关掉时 activePage 会被置空，而用户再次打开十有八九还是想改上次那一项；
// 每次都回到「通用」等于让他重走一遍。只记页面键，不记页内分段（二级 tab 由
// 各子页自己恢复，父组件不该知道 AI 页有几个 tab）。
// 深链（通知中心 / 命令面板传 settings:ai:usage）只负责这一次跳转，不写记忆——
// 「被通知叫过去看了一眼」不该变成下次打开设置的默认落点。
const LAST_PAGE_KEY = 'qd_settings_last_page'

function readLastPage(): string | null {
  try {
    const v = localStorage.getItem(LAST_PAGE_KEY)
    if (!v) return null
    // 页面可能是旧版本写的、也可能已被合并掉：不校验就会落进一个空白页。
    // 用当前菜单实测而不是再维护一份白名单，加删页面时不用同步两处。
    const known = menuItems.value.some((m) => m.key === v) || footerItems.value.some((m) => m.key === v)
    return known ? v : null
  } catch (e) {
    // 隐私模式 / 存储被禁用时读不到：当作没记过，不影响打开设置
    logErr('SettingsModal:readLastPage', e)
    return null
  }
}

function rememberPage(key: string) {
  try {
    localStorage.setItem(LAST_PAGE_KEY, key)
  } catch (e) {
    logErr('SettingsModal:rememberPage', e)
  }
}

function selectMenu(key: string) {
  activePage.value = resolvePage(key)
  // 手动点菜单 = 「进这一页的默认视图」；不清的话会沿用上次深链留下的页内分段
  activeTab.value = ''
  // 选了就走：过滤条件留着，从设置页返回根菜单时反而要找半天为什么少了一半
  settingsQuery.value = ''
  if (activePage.value) rememberPage(activePage.value)
}

function close() {
  activePage.value = null
  activeTab.value = ''
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

// 将 CI 生成的 release notes 解析为紧凑要点列表（无要点时为空，模板回退到 <pre>）。
const releaseNoteItems = computed(() => parseReleaseNotes(updateStatus.value?.releaseNotes))
const updateChecking = ref(false)
const updateResult = ref('')
// 更新下载实时进度：Wails updater 内部发 wails:updater:download-progress（~10次/秒），
// payload 为 {written, total, rate}；驱动「正在下载 X%」文案与进度条。
const updateDlActive = ref(false)
const updateProgress = ref(0)

// ---- 跳过版本（app_state 持久化）----
// 为什么落盘：这里的「跳过此版本」原先只把 updateStatus.state 置 idle，是纯内存动作，
// 刷新/重启后必然再次弹出。用现成的 GetValue/SetValue（与 SettingsSnapshot 的
// qd_snapshot_config 同一套 app_state 配置），不新造存储、不加 RPC。
// 结构是 availableVersion 数组：App.vue 的自动弹窗按版本分别记 localStorage key，
// 天然是「按版本」语义；同一字段也保证两边比对的是同一个版本号字符串。
const SKIPPED_VERSIONS_KEY = 'qd_update_skipped_versions'
const skippedVersions = ref<string[]>([])
function isVersionSkipped(v: string | undefined): boolean {
  return !!v && skippedVersions.value.includes(v)
}

async function loadSkippedVersions() {
  try {
    const raw = unwrap<string>(await GetValue(SKIPPED_VERSIONS_KEY))
    const arr = JSON.parse(raw || '[]')
    skippedVersions.value = Array.isArray(arr)
      ? arr.filter((x): x is string => typeof x === 'string' && !!x)
      : []
  } catch (e) {
    logErr('SettingsModal:loadSkippedVersions', e)
    skippedVersions.value = []
  }
  // App.vue 的 onUpdateStatus 是同步读 localStorage 的 qd_update_skip_<v>（App.vue:346），
  // 不读 app_state。把 app_state 里的版本镜像成同款 key，自动弹窗才会尊重跳过；
  // 用户清了浏览器缓存后，只要再打开设置页就能回灌恢复。
  for (const v of skippedVersions.value) {
    try { localStorage.setItem('qd_update_skip_' + v, '1') } catch (e) { logErr('SettingsModal:mirrorSkip', e) }
  }
}

let offUpdateStatus: (() => void) | null = null
let offDlProgress: (() => void) | null = null

onMounted(async () => {
  // 先于事件订阅装载跳过列表，否则后台检查先推一帧 available 就会漏判
  await loadSkippedVersions()
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
  // 被跳过的版本走自动推送时不再提示；用户主动点「检查更新」仍要能看到（含下载入口），
  // 所以过滤只放在事件回调里，checkForUpdates 直接调 applyUpdateStatus 不受影响。
  offUpdateStatus = Events.On('quickdock:update:status', (payload: any) => {
    const st = (payload?.data ?? payload) as UpdateStatus
    if (st?.state === 'available' && isVersionSkipped(st.availableVersion)) {
      updateResult.value = t('updateSkipped', { version: st.availableVersion })
      return
    }
    applyUpdateStatus(st)
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

// 「跳过此版本」接真逻辑：只写前端两份存储，任一失败都留痕并提示，避免「点了没反应」。
// ① app_state（权威持久化）② localStorage 的 qd_update_skip_<v>（对齐 App.vue:346
// 自动弹窗的判断 key）。刻意不调后端 SkipUpdate：Wails v3 的 Updater.SkipVersion
// 会让之后每次 Check() 都把该版本当「已是最新」，连用户主动点「检查更新」都看不到，
// 与「跳过的版本不再提示、但手动检查能看到」相悖。抑制提示全部交给前端两处判断。
async function skipThisUpdate() {
  const v = updateStatus.value?.availableVersion
  if (!v) return
  if (!skippedVersions.value.includes(v)) {
    skippedVersions.value = [...skippedVersions.value, v]
  }
  const failures: string[] = []
  try {
    unwrap(await SetValue(SKIPPED_VERSIONS_KEY, JSON.stringify(skippedVersions.value)))
  } catch (e) { logErr('SettingsModal:saveSkipped', e); failures.push(getErrorMessage(e)) }
  try {
    localStorage.setItem('qd_update_skip_' + v, '1')
  } catch (e) { logErr('SettingsModal:mirrorSkip', e) }
  // 不管写盘结果如何都先收起下载区：用户已表达意图，界面不该继续挂着被跳过的版本
  const cur = updateStatus.value
  if (cur) cur.state = 'idle'
  updateResult.value = t('updateSkipped', { version: v })
  if (failures.length) toast.error(failures[0])
}

// 恢复路径：清 app_state 与 localStorage 镜像，再拉一次更新状态，让被跳过的可用版本
// 重新出现下载入口。后端从未被 SkipVersion 标记过，GetUpdateState 能直接返回 available。
async function clearSkippedUpdates() {
  const cleared = [...skippedVersions.value]
  try {
    unwrap(await SetValue(SKIPPED_VERSIONS_KEY, '[]'))
  } catch (e) {; reportError(e, 'SettingsModal:clearSkipped'); return }
  skippedVersions.value = []
  for (const v of cleared) {
    try { localStorage.removeItem('qd_update_skip_' + v) } catch (e) { logErr('SettingsModal:unmirrorSkip', e) }
  }
  try {
    const state = unwrap<UpdateStatus>(await GetUpdateState())
    if (state) applyUpdateStatus(state)
  } catch (e) { logErr('SettingsModal:reloadUpdateState', e) }
  toast.success(t('updateSkipCleared'))
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
  writeStored(UPDATE_ADMIN_ACK, '1')
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

/** 已复制的体积文案。
    这里刻意不走 formatBytes：文案是「x / y MB」，单位由 i18n 固定写死，
    自动进档会出现「1.5 GB / 2 MB」这种单位错位。 */
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
    case 'export_db':
      return t('dataExportDb')
    case 'export_vault':
      return t('dataExportVault')
    case 'export_packing':
      return t('dataExportPacking')
    case 'import_extracting':
      return t('dataImportExtracting')
    case 'import_db':
      return t('dataImportDb')
    case 'import_files':
      return t('dataImportFiles')
    case 'import_vault':
      return t('dataImportVault')
    case 'import_env':
      return t('dataImportEnv')
    case 'done':
    case 'export_done':
    case 'import_done':
      return t('dataTransferDone')
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
    if (p.phase === 'done' || p.phase === 'export_done' || p.phase === 'import_done') {
      migrateRunning.value = false
      migrateDone.value = true
      if (p.phase === 'export_done' || p.phase === 'import_done') {
        dataTransferBusy.value = false
        dataTransferDone.value = true
      }
      // 导入完成会切数据目录 → 刷新显示的当前路径（后端已写配置）
      if (p.phase === 'import_done') loadStorage()
    }
    if (p.phase === 'error') {
      migrateRunning.value = false
      dataTransferBusy.value = false
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
    reportError(e, 'SettingsModal.pickStorageDir')
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

// ---- 整机数据迁移包（A 机导出 zip → B 机导入）----
//
// 导入是**合并**不是替换：本机已有的记录和文件都留着，只把包里多出来的并进来
// （同名主键/同名文件保留本地那份）。所以它不切数据目录、也不用重启。
const dataTransferBusy = ref(false)
const dataTransferDone = ref(false)
const dataImportInfo = ref<{
  sourceDir: string
  createdAt: string
  dbBytes: number
  hasVault: boolean
  scope?: string
  navIds?: string[]
} | null>(null)
// 密码库口令。导出时给一个口令重新封装（本机 DPAPI 的密文搬不走），
// 导入时用同一个口令解开。空 = 不动密码库。
const dataVaultPassword = ref('')
const importZipPath = ref('')

// ---- 按导航菜单选择打包范围 ----
// 勾选的 id 直接交给后端做「导航 → 表 / 数据目录」映射，前端只负责文案与勾选状态。
// 默认全选；勾「整机全量」时仍走原来的 DataExport，旧行为一点不变。
const exportScopeOpen = ref(false)
const exportZipPath = ref('')
const exportWholeMachine = ref(true)
const selectedNavs = ref<string[]>(navEntries.map((e) => e.id))

/** 导出：选保存位置 → 弹窗选择要打包的导航功能 → 后端异步打包。 */
async function exportData() {
  if (dataTransferBusy.value) return
  storageMsg.value = ''
  try {
    const day = new Date().toISOString().slice(0, 10)
    const dest = (unwrap<string>(await SelectSavePath(t('dataExportSaveTitle'), `quickdock-backup-${day}.zip`)) ?? '').trim()
    if (!dest) return
    exportZipPath.value = dest
    exportScopeOpen.value = true
  } catch (e: any) {
    reportError(e, 'Settings.exportData')
  }
}

function toggleNav(id: string) {
  const cur = selectedNavs.value
  selectedNavs.value = cur.includes(id) ? cur.filter((v) => v !== id) : [...cur, id]
}

/** 确认打包范围并启动导出；进度仍走 quickdock:storage:migrate。 */
async function confirmExportScope() {
  if (dataTransferBusy.value || !exportZipPath.value) return
  if (!exportWholeMachine.value && selectedNavs.value.length === 0) {
    toast.error(t('dataExportNeedNav'))
    return
  }
  exportScopeOpen.value = false
  dataTransferBusy.value = true
  try {
    resetMigrateUi()
    const pass = dataVaultPassword.value.trim()
    if (exportWholeMachine.value) {
      unwrap(await DataExport(exportZipPath.value, pass))
    } else {
      unwrap(await DataExportSelected(exportZipPath.value, pass, selectedNavs.value))
    }
    toast.success(t('dataExportStarted'))
  } catch (e: any) {
    dataTransferBusy.value = false
    reportError(e, 'Settings.exportData')
  }
}

function cancelExportScope() {
  exportScopeOpen.value = false
  exportZipPath.value = ''
}

const allNavsSelected = computed(() => selectedNavs.value.length === navEntries.length)
function toggleAllNavs() {
  selectedNavs.value = allNavsSelected.value ? [] : navEntries.map((e) => e.id)
}

/** 选 zip → 先校验是不是整机包，是则记住信息，供「开始导入」使用。 */
async function pickImportZip() {
  storageMsg.value = ''
  dataImportInfo.value = null
  importZipPath.value = ''
  try {
    const src = (unwrap<string>(await SelectLocalFile(t('dataImportPickTitle')))) ?? ''
    const path = typeof src === 'string' ? src.trim() : ''
    if (!path) return
    const info = unwrap<any>(await DataImportInspect(path))
    if (info) {
      dataImportInfo.value = {
        sourceDir: info.sourceDir,
        createdAt: info.createdAt,
        dbBytes: info.dbBytes,
        hasVault: !!info.hasVault,
        scope: info.scope || 'full',
        navIds: info.navIds || [],
      }
      importZipPath.value = path
      storageMsg.value = t('dataImportReady', { dir: info.sourceDir })
    }
  } catch (e: any) {
    reportError(e, 'Settings.pickImportZip')
  }
}

/** 开始导入：合并进当前数据目录（不换目录、不重启），密码库用同一口令还原。 */
async function importData() {
  if (dataTransferBusy.value || !importZipPath.value) return
  if (dataImportInfo.value?.hasVault && !dataVaultPassword.value.trim()) {
    toast.error(t('dataImportNeedPass'))
    return
  }
  const ok = await toast.confirm(t('dataImportConfirm'))
  if (!ok) return
  dataTransferBusy.value = true
  storageMsg.value = ''
  try {
    resetMigrateUi()
    unwrap(await DataImport(importZipPath.value, dataVaultPassword.value.trim()))
    toast.success(t('dataImportStarted'))
  } catch (e: any) {
    dataTransferBusy.value = false
    reportError(e, 'Settings.importData')
  }
}

/** 每轮导出/导入前清掉上一轮的进度态。 */
function resetMigrateUi() {
  migratePhase.value = ''
  migrateTotalFiles.value = 0
  migrateDoneFiles.value = 0
  migrateTotalBytes.value = 0
  migrateDoneBytes.value = 0
  migrateCurrent.value = ''
  migrateDone.value = false
  dataTransferDone.value = false
}

// 切换到存储页时刷新状态（弹窗常驻，onMounted 只在首次执行）
watch(activePage, (k) => {
  if (k === 'backup') loadStorage()
  // 弹窗常驻，onMounted 只跑一次；每次进 preferences 都重读一次能力/档位，
  // 避免重启回来后还显示上一次的选中态。
  if (k === 'preferences') { loadUiZoom(); loadWindowBackdrop() }
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
// 档位必须与 internal/platform/uizoom.go 的 uiZoomLevels 一一对应，后端只认这些值。
// 百分比标签不需要 i18n：100% / 150% 各语言写法一致。
const uiZoomLevels = computed(() =>
  [1, 1.1, 1.15, 1.2, 1.25, 1.5].map((v) => ({ value: v, label: Math.round(v * 100) + '%' })),
)

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

// ---- 文字大小 ----
// 只缩放字号（--font-scale），不动间距/图标/窗口，也不需要重启；与上面的界面缩放正交。
// 档位与界面缩放保持一致，用户在一排刻度里看到的两组数字含义相同。
const FONT_SCALE_KEY = 'qd.fontScale'
const fontScaleLevels = [1, 1.1, 1.15, 1.2, 1.25, 1.5]
const fontScale = ref(1)
function selectFontScale(v: number) {
  if (v === fontScale.value) return
  fontScale.value = v
  document.documentElement.style.setProperty('--font-scale', String(v))
  writeStored(FONT_SCALE_KEY, String(v))
}
try {
  const saved = Number(localStorage.getItem(FONT_SCALE_KEY))
  if (fontScaleLevels.includes(saved)) fontScale.value = saved
} catch { /* 存储被禁：保持 100% */ }

// ---- 窗口材质（Mica / Acrylic）----
// 与界面缩放同源：材质在窗口创建时固定，配置写在 platform（quickdock.json），
// 改完必须重启。差别是它多一道能力探测 —— 不支持的系统连设置项都不渲染，
// 后端 SetWindowBackdrop 也会直接拒绝，不留一个「能选但没效果」的假开关。
const backdropLevels = computed(() => [
  { value: 'off', label: t('windowBackdropOff') },
  { value: 'mica', label: t('windowBackdropMica') },
  { value: 'acrylic', label: t('windowBackdropAcrylic') },
  { value: 'tabbed', label: t('windowBackdropTabbed') },
])

async function loadWindowBackdrop() {
  try {
    const st = unwrap<{ supported?: boolean; current?: string }>(await GetWindowBackdrop())
    // 探测失败或字段缺失时保持 false：宁可不显示，也不能给一个点了没反应的开关。
    backdropSupported.value = st?.supported === true
    if (typeof st?.current === 'string') backdropValue.value = st.current
  } catch (e) {
    logErr('Settings.windowBackdrop', e)
  }
}

async function selectWindowBackdrop(v: string) {
  if (backdropBusy.value || v === backdropValue.value) return
  backdropBusy.value = true
  backdropMsg.value = ''
  try {
    const res = await SetWindowBackdrop(v)
    if (res && res.code === 0) {
      backdropValue.value = v
      backdropMsg.value = res.msg || t('windowBackdropRestartHint')
    } else {
      backdropMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    backdropMsg.value = getErrorMessage(e)
  } finally {
    backdropBusy.value = false
  }
}

// 与缩放 / 存储位置同一条真重启路径（不用更新页那个就地替换专用的 restartApp）。
async function restartForWindowBackdrop() {
  backdropBusy.value = true
  backdropMsg.value = t('windowBackdropRestarting')
  try {
    const res = await RestartAppPlain()
    if (res && res.code === 0) {
      // 成功时进程会在新实例接管后异步退出，不再有后续 UI 更新
      backdropMsg.value = t('windowBackdropRestarting')
    } else {
      backdropMsg.value = res?.msg || t('updateError')
    }
  } catch (e: any) {
    backdropMsg.value = getErrorMessage(e)
  } finally {
    backdropBusy.value = false
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
const themeTabs = computed(() => themeOptions.value.map((o) => ({ id: o.value, label: o.label })))
function themeIcon(value: string) {
  return themeOptions.value.find((o) => o.value === value)?.icon ?? Moon
}

const currentLocale = ref('zh-CN')

async function setLocale(newLocale: string) {
  const oldLocale = currentLocale.value
  currentLocale.value = newLocale
  i18n.global.locale.value = newLocale as 'zh-CN' | 'en-US'
  try {
    // code!=0 必须抛错：否则语言当下切了、重启又变回去，用户还不知道失败了。
    unwrap(await SetValue('locale', newLocale))
  } catch (e) {
    // 持久化失败就回滚到旧语言，界面与下次启动的落库状态保持一致，别留下「假切换」。
    currentLocale.value = oldLocale
    i18n.global.locale.value = oldLocale as 'zh-CN' | 'en-US'
    reportError(e, 'SettingsModal.setLocale', { fallback: t('settingLocaleFailed') })
  }
}

// 三处分段切换统一走 QdTabs：语言名是语言自名（两种语言下写法相同、无需翻译的专有名词），
// 缩放值后端用 number，这里映射成字符串 id 交给 QdTabs，回来再转回 number。
const localeTabs = [
  { id: 'zh-CN', label: '简体中文' },
  { id: 'en-US', label: 'English' },
]
const uiZoomTabs = computed(() => uiZoomLevels.value.map((lv) => ({
  id: String(lv.value), label: lv.label, disabled: uiZoomBusy.value,
})))
const backdropTabs = computed(() => backdropLevels.value.map((lv) => ({
  id: lv.value, label: lv.label, disabled: backdropBusy.value,
})))
function onUiZoomTab(v: string) { selectUiZoom(Number(v)) }

// 打开设置页时如有初始页面则导航，不再全局挂起热键
watch(() => props.visible, async (val) => {
    if (val) {
      if (props.initialPage) {
        // 兼容旧入口：'sync' / 'snapshot' / 'data' / 'storage' … 经 PAGE_ALIASES 落到当前页
        applyInitialPage(props.initialPage)
      } else {
        // 没有深链：回到上次停留的页；首次使用（没记录 / 记录已失效）落第一页。
        // 落第一页而不是留空，是因为右侧一大片空白加一句「选择左侧菜单项」，
        // 等于让用户先做一次无用选择。第一页取自菜单顺序，不写死 key。
        selectMenu(readLastPage() || menuItems.value[0]?.key || 'preferences')
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
  try {
    const ex = unwrap<{ workspaces?: boolean; scenes?: boolean; items?: boolean }>(await GetSearchExclusions())
    if (ex) searchExclusions.value = { workspaces: !!ex.workspaces, scenes: !!ex.scenes, items: !!ex.items }
  } catch (e) { logErr('SettingsModal', e) }
  try {
    showHidden.value = !!unwrap<boolean>(await GetSearchShowHidden())
  } catch (e) { logErr('SettingsModal', e) }
  // 能力探测要在根菜单渲染前拿到结果，否则「窗口材质」不会拼进菜单描述。
  loadWindowBackdrop()
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

// ---- Ctrl+K 搜索排除 ----
// 三类实体各一个总开关；单条排除在各自的编辑表单里（场景/集合/项目）。
// 只影响命令面板（Ctrl+K）的检索，不影响资源树与 MCP/AI 工具的全量读取。
const searchExclusions = ref({ workspaces: false, scenes: false, items: false })
const searchPrefsBusy = ref(false)

async function toggleSearchExclusion(kind: 'workspaces' | 'scenes' | 'items') {
  if (searchPrefsBusy.value) return
  const next = { ...searchExclusions.value, [kind]: !searchExclusions.value[kind] }
  searchPrefsBusy.value = true
  try {
    const saved = unwrap<{ workspaces?: boolean; scenes?: boolean; items?: boolean }>(await SaveSearchExclusions(next))
    if (saved) searchExclusions.value = { workspaces: !!saved.workspaces, scenes: !!saved.scenes, items: !!saved.items }
  } catch (e) {
    toast.error(t('saveFailed2') + ': ' + getErrorMessage(e))
  } finally {
    searchPrefsBusy.value = false
  }
}

// 显示隐藏项：被排除的项默认从 Ctrl+K / AI / MCP 结果里消失，打开后重新可见，
// 用于点进去把勾选取消（否则等于把数据锁进黑洞）。资源树始终显示全量，不受影响。
const showHidden = ref(false)

async function toggleShowHidden() {
  try {
    await SetSearchShowHidden(!showHidden.value)
    showHidden.value = !showHidden.value
  } catch (e) {
    toast.error(t('saveFailed2') + ': ' + getErrorMessage(e))
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
            <button class="close-btn qd-icon-btn" :title="t('settingsCloseEsc')" @click="close">
              <X :size="18" />
            </button>
          </div>

          <!-- 设置项搜索：根菜单常驻；进了具体设置页也留着，回来时不用重输 -->
          <div class="settings-search">
            <QdSearchBox
              v-model="settingsQuery"
              class="settings-search-box"
              :icon-size="14"
              :placeholder="t('settingsSearchPh')"
              @keydown="onSearchKeydown"
            />
          </div>

          <div class="settings-menu">
            <template v-for="group in menuGroups" :key="group.title">
              <div class="menu-group-title">{{ group.title }}</div>
              <button
                v-for="item in group.items"
                :key="item.key"
                :class="['menu-row', { active: activePage === item.key }]"
                :title="item.desc"
                @click="selectMenu(item.key)"
              >
                <component :is="item.icon" :size="18" class="menu-row-icon" />
                <div class="menu-row-text">
                  <span class="menu-row-label">{{ item.label }}</span>
                  <span class="menu-row-desc">{{ item.desc }}</span>
                </div>
                <!-- 不加右箭头：点左侧只在右边换内容，没有第二级，箭头会被读成「还要再点一次」。
                     当前位置由淡底 + 左缘竖条表示（见 settings.css 的 .menu-row.active）。 -->
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
                :title="item.desc"
                @click="selectMenu(item.key)"
              >
                <component :is="item.icon" :size="18" class="menu-row-icon" />
                <div class="menu-row-text">
                  <span class="menu-row-label">{{ item.label }}</span>
                  <span class="menu-row-desc">{{ item.desc }}</span>
                  <span v-if="item.alert" class="menu-row-alert">{{ item.alert }}</span>
                </div>
                <span
                  v-if="item.badge"
                  class="menu-badge"
                  :class="`tone-${item.badgeTone}`"
                  :title="t('badgeNewCrash', { n: item.badge })"
                >{{ item.badge > 99 ? '99+' : item.badge }}</span>
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
                <!-- 使用说明放第一位：新用户装完之后第一个该点的就是它，
                     首页 / 仓库属于「了解项目」的次要入口。地址来自 utils/siteLinks，
                     与工作空间空状态引导里的入口共用一份，避免两处指向不同的文档。 -->
                <a class="about-link" href="#" @click.prevent="openUrl(SITE_GUIDE_URL)">
                  <ExternalLink :size="12" />
                  <span class="about-link-tag">{{ t('aboutSiteGuide') }}</span>
                  {{ SITE_GUIDE_TEXT }}
                </a>
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

              <div class="action-row qd-mt-3" >
                <button class="btn btn-primary" :disabled="updateChecking" @click="checkForUpdates">
                  <RotateCcw :size="14" :class="{ spinning: updateChecking }" />
                  {{ updateChecking ? t('updateChecking') : t('updateCheckNow') }}
                </button>
              </div>

              <p v-if="updateResult" class="result-hint" :class="{ 'result-error': updateStatus?.state === 'error' }">{{ updateResult }}</p>

              <div v-if="updateDlActive" class="update-dl-bar">
                <div class="update-dl-fill" :style="{ width: updateProgress + '%' }"></div>
              </div>

              <div v-if="updateStatus?.state === 'available'" class="action-row qd-mt-3" >
                <button class="btn btn-primary" @click="downloadUpdate" :disabled="updateDlActive">
                  {{ updateDlActive
                    ? t('updateDownloadingPct', { pct: updateProgress })
                    : t('updateDownload') + ' ' + updateStatus.availableVersion }}
                </button>
                <button class="btn btn-secondary" :disabled="updateDlActive" @click="skipThisUpdate">
                  {{ t('updateSkip') }}
                </button>
              </div>

              <!-- 恢复路径：只在有跳过记录时出现，避免常态占位 -->
              <div v-if="skippedVersions.length" class="skip-row">
                <span class="skip-versions">{{ t('updateSkippedLabel') }} {{ skippedVersions.join(', ') }}</span>
                <button class="btn btn-secondary" @click="clearSkippedUpdates">{{ t('updateSkipClear') }}</button>
              </div>

              <div v-if="updateStatus?.state === 'available' && updateStatus.releaseNotes" class="update-notes">
                <div class="update-notes-title">{{ t('updateNotes') }}</div>
                <ul v-if="releaseNoteItems.length" class="update-notes-list">
                  <li v-for="(item, i) in releaseNoteItems" :key="i">{{ item }}</li>
                </ul>
                <pre v-else class="update-notes-body">{{ updateStatus.releaseNotes }}</pre>
              </div>

              <div v-if="updateStatus?.state === 'ready'" class="action-row qd-mt-3" >
                <button class="btn btn-primary update-restart-btn" @click="restartApp">
                  {{ t('updateRestart') }}
                </button>
              </div>
            </div>
          </div>

          <!-- 通用设置（开机自启 + 外观主题/语言） -->
          <div v-else-if="activePage === 'preferences'" class="content-page content-left preferences-page">
            <div class="section">
              <h3 class="section-title">{{ t('general') }}</h3>
              <div class="setting-row">
                <label class="setting-label">{{ t('autoStart') }}</label>
                <div class="setting-control">
                  <QdSwitch :model-value="autoStart" :label="t('autoStart')" @update:model-value="toggleAutoStart" />
                  <span class="toggle-label">{{ autoStart ? t('autoStartOn') : t('autoStartOff') }}</span>
                </div>
                <p v-if="autoStartResult" class="result-hint error">{{ autoStartResult }}</p>
              </div>
            </div>
            <div class="section">
              <h3 class="section-title">{{ t('theme') }}</h3>
              <p class="section-desc">{{ t('themeDesc') }}</p>
              <QdTabs
                :model-value="currentTheme"
                :tabs="themeTabs"
                variant="segmented"
                :aria-label="t('theme')"
                @update:model-value="setTheme"
              >
                <template #tab="{ tab }">
                  <component :is="themeIcon(tab.id)" :size="14" />
                  <span>{{ tab.label }}</span>
                </template>
              </QdTabs>
            </div>
            <div class="section">
              <h3 class="section-title">{{ t('language') }}</h3>
              <p class="section-desc">{{ t('languageDesc') }}</p>
              <QdTabs
                :model-value="currentLocale"
                :tabs="localeTabs"
                variant="segmented"
                :aria-label="t('language')"
                @update:model-value="setLocale"
              />
            </div>
            <!-- 两种缩放放同一段里上下对照：两排刻度长得一样，拆成两个 section
                 各带一段长说明时很难看出作用范围不同。说明压到每行下方的小字。 -->
            <div class="section">
              <h3 class="section-title">{{ t('displayScale') }}</h3>
              <div class="scale-row">
                <label class="setting-label">{{ t('uiZoom') }}</label>
                <QdTabs
                  :model-value="String(uiZoomValue)"
                  :tabs="uiZoomTabs"
                  variant="segmented"
                  class="scale-tabs"
                  :aria-label="t('uiZoom')"
                  @update:model-value="onUiZoomTab"
                />
              </div>
              <p class="scale-hint">{{ t('uiZoomDesc') }}</p>
              <div class="scale-row">
                <label class="setting-label">{{ t('textSize') }}</label>
                <QdTabs
                  :model-value="String(fontScale)"
                  :tabs="fontScaleLevels.map((v) => ({ id: String(v), label: Math.round(v * 100) + '%' }))"
                  variant="segmented"
                  class="scale-tabs"
                  :aria-label="t('textSize')"
                  @update:model-value="selectFontScale(Number($event))"
                />
              </div>
              <p class="scale-hint">{{ t('textSizeDesc') }}</p>
              <!-- 「立即重启」只在改完出现：缩放是窗口创建时固定的，不重启等于没改 -->
              <div v-if="uiZoomMsg" class="action-row">
                <p class="result-hint">{{ uiZoomMsg }}</p>
                <button class="btn btn-secondary" :disabled="uiZoomBusy" @click="restartForUiZoom">{{ t('uiZoomRestart') }}</button>
              </div>
            </div>
            <!-- 窗口材质：能力探测通过（Win11 22621+）才渲染这一整块。
                 Win10 / 非 Windows 下 v-if 为 false，设置页与旧版逐项一致。 -->
            <div v-if="backdropSupported" class="section">
              <h3 class="section-title">{{ t('windowBackdrop') }}</h3>
              <p class="section-desc">{{ t('windowBackdropDesc') }}</p>
              <QdTabs
                :model-value="backdropValue"
                :tabs="backdropTabs"
                variant="segmented"
                :aria-label="t('windowBackdrop')"
                @update:model-value="selectWindowBackdrop"
              />
              <p class="field-hint">{{ t('windowBackdropHint') }}</p>
              <!-- 材质在窗口创建时固定，改完必须重启（复用与缩放相同的真重启路径） -->
              <div v-if="backdropMsg" class="action-row">
                <p class="result-hint">{{ backdropMsg }}</p>
                <button class="btn btn-secondary" :disabled="backdropBusy" @click="restartForWindowBackdrop">{{ t('windowBackdropRestart') }}</button>
              </div>
            </div>
            <!-- Ctrl+K 搜索排除：三类实体各一个总开关，关掉后整类不再出现在
                 命令面板结果里。单条排除在各自的编辑表单中（场景/集合/项目）。
                 放在页尾：它是低频的高级设置，不挡在主题/缩放这些常用项前面。 -->
            <div class="section">
              <h3 class="section-title">{{ t('searchScopeTitle') }}</h3>
              <p class="section-desc">{{ t('searchScopeDesc') }}</p>
              <div class="setting-row">
                <label class="setting-label">{{ t('searchScopeWorkspaces') }}</label>
                <div class="setting-control">
                  <QdSwitch :model-value="searchExclusions.workspaces" :label="t('searchScopeWorkspaces')"
                            @update:model-value="toggleSearchExclusion('workspaces')" />
                  <span class="toggle-label">{{ searchExclusions.workspaces ? t('searchScopeExcluded') : t('searchScopeIncluded') }}</span>
                </div>
              </div>
              <div class="setting-row">
                <label class="setting-label">{{ t('searchScopeScenes') }}</label>
                <div class="setting-control">
                  <QdSwitch :model-value="searchExclusions.scenes" :label="t('searchScopeScenes')"
                            @update:model-value="toggleSearchExclusion('scenes')" />
                  <span class="toggle-label">{{ searchExclusions.scenes ? t('searchScopeExcluded') : t('searchScopeIncluded') }}</span>
                </div>
              </div>
              <div class="setting-row">
                <label class="setting-label">{{ t('searchScopeItems') }}</label>
                <div class="setting-control">
                  <QdSwitch :model-value="searchExclusions.items" :label="t('searchScopeItems')"
                            @update:model-value="toggleSearchExclusion('items')" />
                  <span class="toggle-label">{{ searchExclusions.items ? t('searchScopeExcluded') : t('searchScopeIncluded') }}</span>
                </div>
              </div>
              <p class="section-desc">{{ t('showHiddenHint') }}</p>
              <div class="setting-row">
                <label class="setting-label">{{ t('showHidden') }}</label>
                <div class="setting-control">
                  <QdSwitch :model-value="showHidden" :label="t('showHidden')"
                            @update:model-value="toggleShowHidden" />
                  <span class="toggle-label">{{ showHidden ? t('showHiddenOn') : t('showHiddenOff') }}</span>
                </div>
              </div>
            </div>
          </div>
          <div v-else-if="activePage === 'hotkeys'" class="content-page content-left">
            <HotkeySettings ref="hotkeyRef" />
          </div>
          <!-- 安全：启动密码。独立成页而不是塞进「通用」——它改的是入口凭证，
               和主题/语言不是一类东西，且页内要放恢复码这种强提示块。 -->
          <div v-else-if="activePage === 'security'" class="content-page content-left">
            <SettingsSecurity :visible="activePage === 'security'" />
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

          <!-- 数据与备份：存储位置 / 同步 / 快照。
               以前是「三段竖着堆」，同步那块要滚过整张存储表单才看得见；
               三件事互相独立（放哪 / 怎么备份到远端 / 定时留底），分段比滚动短。 -->
          <div v-else-if="activePage === 'backup'" class="content-page content-left">
            <div class="settings-tabs">
              <QdTabs v-model="backupTab" :tabs="backupTabs" />
            </div>
            <!-- 存储段的首行说明保留、标题去掉：tab 已经写了「存储位置」，再说一遍是重复。 -->
            <div v-show="backupTab === 'storage'" class="section">
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

              <label class="field toggle-field qd-pointer qd-mb-4" >
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

            <!-- 整机数据迁移：导出当前数据成 zip / 从 zip 导入（换机迁移）。
                 与上面的目录迁移共用进度条（同一事件通道），故进度条放在两者都能看到的位置。 -->
            <div v-show="backupTab === 'storage'" class="section">
              <h3 class="section-title">{{ t('dataTransferTitle') }}</h3>
              <p class="section-desc">{{ t('dataTransferDesc') }}</p>

              <!-- 导出/导入进度：与目录迁移共用事件与百分比，用 dataTransferBusy 区分，
                   避免和上面目录迁移的进度条同时显示两份。 -->
              <div v-if="dataTransferBusy || dataTransferDone" class="storage-migrate-progress">
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

              <!-- 密码库口令：导出时用它重新封装（本机 DPAPI 的密文搬不走），
                   导入时用同一口令解开。留空 = 不动密码库，包就不带它。 -->
              <div class="setting-row">
                <label class="setting-label">{{ t('dataVaultPass') }}</label>
                <div class="setting-control">
                  <input
                    v-model="dataVaultPassword"
                    type="password"
                    class="field-input storage-path-input"
                    autocomplete="new-password"
                    :placeholder="t('dataVaultPassHint')"
                    :disabled="dataTransferBusy"
                  />
                </div>
              </div>

              <div class="action-row">
                <button class="btn btn-secondary" :disabled="dataTransferBusy || migrateRunning" @click="exportData">
                  {{ t('dataExportBtn') }}
                </button>
                <button class="btn btn-secondary" :disabled="dataTransferBusy || migrateRunning" @click="pickImportZip">
                  {{ t('dataImportPick') }}
                </button>
                <button
                  class="btn btn-primary"
                  :disabled="dataTransferBusy || migrateRunning || !importZipPath"
                  @click="importData"
                >
                  {{ t('dataImportBtn') }}
                </button>
              </div>
              <p class="field-hint" v-if="dataImportInfo">
                {{ t('dataImportSource', { dir: dataImportInfo.sourceDir }) }}
                <span v-if="dataImportInfo.scope === 'selected'"> · {{ t('dataImportScope', { n: dataImportInfo.navIds?.length || 0 }) }}</span>
                <span v-if="dataImportInfo.hasVault"> · {{ t('dataImportHasVault') }}</span>
              </p>
              <p class="field-hint">{{ t('dataImportTip') }}</p>
            </div>
            <!-- v-show 而不是 v-if：两者内部都有表单草稿（WebDAV 地址、快照名），
                 切走再切回不该丢；但加载只看 visible，没切到这一段就不发请求。 -->
            <SettingsSync v-show="backupTab === 'sync'"
                          :visible="activePage === 'backup' && backupTab === 'sync'" @close="close" />
            <SettingsSnapshot v-show="backupTab === 'snapshot'"
                              :visible="activePage === 'backup' && backupTab === 'snapshot'" @close="close" />
          </div>
          <!-- 历史：最近动态 + 回收站（页内分段切换，两者都是「回头看一眼」型信息） -->
          <div v-else-if="activePage === 'history'" class="content-page content-flush">
            <SettingsHistory :initial-tab="activeTab" />
          </div>

          <!-- AI 助手 -->
          <div v-else-if="activePage === 'ai'" class="content-page content-left">
            <SettingsAI :visible="activePage === 'ai'" :initial-tab="activeTab" />
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

          <!-- 通知：机器人 Webhook 全局配置（监控/定时任务共用一份，页内只控制渠道开关） -->
          <div v-else-if="activePage === 'notify'" class="content-page content-left">
            <SettingsNotify :visible="activePage === 'notify'" />
          </div>

          <!-- 日志查看器（从导航栏迁入的专属设置项） -->
          <div v-else-if="activePage === 'logs'" class="content-page content-logs">
            <LogViewer :new-crash-count="entryCrashCount" @crash-cleared="entryCrashCount = 0" />
          </div>

          <!-- 空状态：无 activePage（或落到未知路由）时回落到统一的「选菜单」提示，
               不再显示 comingSoon 那种没有下一步的空话占位 -->
          <div v-else class="content-page content-empty">
            <p class="empty-icon"><Settings :size="20" :stroke-width="1.5" /></p>
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

  <!-- 导出前选打包范围：整机全量，或只勾导航菜单对应的功能数据 -->
  <QdModal
    v-if="exportScopeOpen"
    :model-value="true"
    :title="t('dataExportScopeTitle')"
    width="560px"
    @update:model-value="cancelExportScope"
  >
    <p class="field-hint">{{ t('dataExportScopeHint') }}</p>
    <label class="export-scope-row export-scope-whole">
      <input v-model="exportWholeMachine" type="checkbox" />
      <span>{{ t('dataExportWhole') }}</span>
    </label>
    <div v-if="!exportWholeMachine" class="export-scope-list">
      <div class="export-scope-head">
        <span class="field-hint">{{ selectedNavs.length }} / {{ navEntries.length }}</span>
        <button class="btn btn-secondary btn-sm" @click="toggleAllNavs">
          {{ allNavsSelected ? t('dataExportNavNone') : t('dataExportNavAll') }}
        </button>
      </div>
      <label v-for="e in navEntries" :key="e.id" class="export-scope-row">
        <input type="checkbox" :checked="selectedNavs.includes(e.id)" @change="toggleNav(e.id)" />
        <span>{{ t(e.labelKey) }}</span>
      </label>
    </div>
    <div class="action-row">
      <button class="btn btn-secondary" @click="cancelExportScope">{{ t('cancel') }}</button>
      <button class="btn btn-primary" @click="confirmExportScope">{{ t('dataExportBtn') }}</button>
    </div>
  </QdModal>
</template>

<style scoped>
/* 已跳过版本行：标签+版本号在左、恢复按钮在右，避免与下载按钮挤成一团。
   样式放本组件 scoped 块，不动公共 styles/*.css（WebView2 不支持 :has() 等新选择器）。 */
.skip-row {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  flex-wrap: wrap;
  margin-top: var(--space-5);
}
.skip-versions {
  font-size: var(--text-sm);
  color: var(--color-text-muted);
  overflow-wrap: anywhere;
}

/* 侧栏设置按钮上的红色角标 = 新增崩溃记录数。原先只有 hover title 能看出来源，
   点开设置那一瞬间角标又被清掉，用户完全不知道它指的是哪一项。这里在「日志」
   菜单项上补可见说明；.menu-row-desc 是 display:none，所以必须用独立标签。 */
.menu-row-alert {
  align-self: flex-start;
  max-width: 100%;
  margin-top: var(--space-1);
  padding: 1px var(--space-3);
  border-radius: var(--radius-full);
  background: var(--color-danger-bg);
  color: var(--color-danger-ink);
  font-size: var(--text-2xs);
  line-height: 1.5;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.menu-badge {
  flex-shrink: 0;
  min-width: 18px;
  height: 18px;
  padding: 0 var(--space-2);
  border-radius: var(--radius-full);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: var(--text-2xs);
  font-weight: 600;
  color: var(--color-danger-text);
  font-variant-numeric: tabular-nums;
}
.menu-badge.tone-danger { background: var(--color-danger); }
.menu-badge.tone-warning { background: var(--color-warning); }
.menu-badge.tone-info { background: var(--color-accent); }

/* 导出范围选择：两列网格排导航项，21 项一屏能扫完，不必滚很多屏。 */
.export-scope-whole {
  padding: var(--space-3) var(--space-4);
  margin-bottom: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}
.export-scope-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: var(--space-2);
}
.export-scope-list {
  display: grid; grid-template-columns: 1fr 1fr; gap: 2px var(--space-4);
  max-height: 340px; overflow-y: auto;
  padding: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.export-scope-row {
  display: flex; align-items: center; gap: var(--space-2);
  min-height: 26px; padding: 0 var(--space-2);
  font-size: var(--text-md); color: var(--color-text-secondary);
  border-radius: var(--radius-sm); cursor: pointer;
}
.export-scope-row:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.export-scope-row input { accent-color: var(--color-accent); }
/* 通用页专属微调：section 间距/说明层级已提升为全局收口（见 settings.css）；
   这里只留本页的 field-hint / action-row 贴合。 */
.preferences-page :deep(.field-hint) { margin-top: var(--space-3); }
.preferences-page :deep(.action-row) { margin-top: var(--space-4); }

/* 显示缩放：两种刻度上下对照，标签列与其他设置行对齐（104px）。
   说明压在每行下方的小字，避免两个 section 各占一整段长说明。 */
.scale-row { display: flex; align-items: center; gap: var(--space-5); }
/* 104px 标签列 + 12px(--space-5) 间距 = 116px，说明文字对齐到刻度列 */
.scale-row + .scale-hint { margin: 4px 0 0 116px; }
.scale-row + .scale-hint + .scale-row { margin-top: var(--space-5); }
.scale-hint {
  font-size: var(--text-xs); line-height: 1.5; color: var(--color-text-muted);
}
/* 六档百分比收紧到截图那种一排紧凑刻度的密度 */
.scale-tabs :deep(.qd-tab) { padding: 0 var(--space-3); }
</style>

