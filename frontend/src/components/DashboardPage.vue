<script setup lang="ts">
/**
 * 总览仪表盘：一屏看清「今天要做什么 / 服务有没有挂 / 环境在跑什么 / 端口被谁占」。
 *
 * 为什么需要这一页：首屏是工作空间启动台，它回答的是「我要打开哪个项目」；
 * 而每天开工的第一分钟真正要回答的是另外几个问题——待办有没有到期、昨晚的
 * 服务是不是还活着、哪个端口被人占了。这些信息散在 4 个页面里，逐个翻一遍才
 * 能拼出来，于是有了这块聚合页。
 *
 * 版式：页头沿用全应用的 .qd-page-header（与监控/笔记同高，切页不跳）；
 * 「今日」卡在最上（问候 + 日期农历节气 + 天气），概览带在其下：指标不做成
 * 四张并排卡片，而是一条通栏刻度带——卡片是「并列的模块」，刻度带是「同一条
 * 状态线上的读数」，后者才符合这一页「扫一眼」的用途。问候原本独占刻度带第一列，
 * 只剩一行大字、下面整片空白，所以挪进「今日」卡头部并配一句实时状态副句。
 *
 * 数据全部取自已有接口（不新增后端聚合方法）：各块内容独立请求、独立降级——
 * 某一块失败只让那块区域显示「加载失败」，不该因为端口枚举失败把整页拖成空白。
 * 「今日」卡是例外：农历/节气前端本地算，宜忌走离线规则库 lunar-typescript（民俗
 * 数据，不是天文数据，见 utils/huangli.ts）；天气由 Go 后台每 30 分钟自行刷新并持有
 * 唯一快照（Go 直连 Open-Meteo），前端只在挂载/每分钟读一次快照、配置变更时报给
 * 后端，不再自己起 30 分钟定时器。它独立于 refreshAll 的并发，失败只让天气区降级。
 */
import { ref, reactive, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events, Dialogs } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage, isDialogCanceled } from '../utils/error'
import { logErr } from '../utils/logger'
import { navigate, navigateTo } from '../composables/bridge'
import { useToast } from '../composables/useToast'
import { describeDay, localISO } from '../utils/lunar'
import type { LunarDay } from '../utils/lunar'
import { describeHuangLi } from '../utils/huangli'
import type { HuangLiDay } from '../utils/huangli'
import {
  DEFAULT_WEATHER, readWeatherCache, writeWeatherCache,
  fetchWeather, refreshWeather, setWeatherConfig, searchCities, weatherGroup,
} from '../utils/weather'
import type { WeatherConfig, WeatherData, CityHit } from '../utils/weather'
import { ListTodos, ToggleTodo } from '../../bindings/quickdock/services/todo/todoservice'
import { ListMonitors, ListMonitorStats, GetMonitorLogsSince } from '../../bindings/quickdock/services/monitor/monitorservice'
import { EnvList, EnvEverythingStatus } from '../../bindings/quickdock/services/env/environmentservice'
import { DSHStatus } from '../../bindings/quickdock/services/dsh/dshservice'
import { ListListeningPorts } from '../../bindings/quickdock/services/port/portservice'
import { GetValue, GetMostUsedItems, SetValue } from '../../bindings/quickdock/services/appservice'
import { OpenItem } from '../../bindings/quickdock/services/collection/collectionservice'
import { GitListRepos, GitRepoStatus } from '../../bindings/quickdock/services/git/gitservice'
import {
  GetConfig, SetConfig, TestConnection, GetCodes, Refresh as RefreshMail, Dismiss,
} from '../../bindings/quickdock/services/mailbox/mailboxservice'
import {
  List as ListShelf, AddPaths, Remove as RemoveShelf,
  Open as OpenShelf, Reveal as RevealShelf,
} from '../../bindings/quickdock/services/shelf/shelfservice'
import { AIListProfiles, AIUsageStats } from '../../bindings/quickdock/services/ai/aiservice'
import type { Todo, Monitor, CollectionItem } from '../../bindings/quickdock/internal/db/models'
import type { AIUsageStatsResult, AICostEntry } from '../types/ai'
import {
  RefreshCw, ListTodo, Activity, Server, Plug, ChevronRight, ChevronDown, FolderOpen, Check,
  FolderGit2, CalendarDays, Settings2, MapPin, X,
  Sun, CloudSun, Cloud, CloudFog, CloudRain, CloudSnow, CloudLightning, CloudOff,
  Mail, Layers, Copy, Trash2, Plus, File, Folder, AlertCircle,
  Coins,
} from '@lucide/vue'
import TypeIcon from './TypeIcon.vue'

// 以下两个结构只在返回值里出现，Wails 绑定生成器不会单独生成类型（与 MonitorPage 的
// MonitorLog/MonitorStat 同一处理方式），故本地声明，字段与后端 json tag 一致。
interface RuntimeInfo {
  id: string
  name: string
  group: string
  installed: unknown[] | null
  enabled: boolean
  hasService: boolean
}
interface PortInfo {
  port: number
  protocol: string
  pid: number
  process: string
  path: string
  self: boolean
}
interface GitRepoRef { path: string; name: string; addedAt: string }

/** ListMonitorStats 的行：近 24h 每个监控的在线率汇总（字段与后端 json tag 一致）。 */
interface MonitorStat {
  monitorId: string
  totalChecks: number
  upChecks: number
  uptimeRatio: number // 0..100
  avgLatencyMs: number
  lastDownAt: string
}
/** 热力条只需要时间与结果两个字段，其余不取。 */
interface CheckPoint { checkedTs: number; status: string }

/** 常驻服务卡的一行：不仅 env 运行时，Everything / DSH 也按同一形状并进来。
 *  page + target 决定点进去落在哪：target 是目标页里的区块 id（空 = 只切页不定位）。 */
interface ResidentRow {
  id: string
  name: string
  meta: string
  page: string // 点击后去哪一页
  target: string
}
interface GitRepoRow {
  name: string
  path: string
  branch: string
  ahead: number
  behind: number
  changes: number
  error: string
}

// 跳设置页（日志查看器 / AI 用量面板都在设置里）：由 App 经注册表 listeners 注入，
// 与 WorkspacePage / AIPage 同一套 open-settings 约定，不直接 import 设置弹窗。
const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

const { t, locale } = useI18n()
const toast = useToast()

// ---- 顶部信息条：公历 / 农历 / 节气 / 宜忌 / 天气 ----
// 农历、节气、宜忌都是本地离线计算，天气走 Go 后台快照。四者都不进 refreshAll
// 的 Promise.all：网络慢或断网时，下面六张卡片该多久刷还是多久刷。
const dayInfo = ref<LunarDay>(describeDay())
const weatherCfg = ref<WeatherConfig>({ ...DEFAULT_WEATHER })
const weather = ref<WeatherData | null>(null)
const weatherBusy = ref(false)
const weatherError = ref(false)
const showDaySettings = ref(false)
const cityQuery = ref('')
const cityHits = ref<CityHit[]>([])
const cityBusy = ref(false)
const citySearched = ref(false)

// ---- 今日宜忌：纯本地偏好，不报给 Go（后端只管天气快照） ----
interface DayPrefs { showYiJi: boolean }
const DEFAULT_DAY_PREFS: DayPrefs = { showYiJi: true }
// 默认显示、可在设置里关掉；关掉时连展开态一起收起，别留个半开的详情块。
const dayPrefs = ref<DayPrefs>({ ...DEFAULT_DAY_PREFS })
const showYiJiDetail = ref(false)

// ---- 设置浮层 ----
// .qd-page-body 是 overflow-y:auto，绝对定位浮层会被它裁掉；放在 .dash 里做流式块
// 又会把下面六张卡片整体推下去。所以 Teleport 到 body + position:fixed 锚定齿轮：
// 不占文档流、不受任何祖先裁剪。定位范式与 NotificationCenter 一致。
const DAY_PANEL_W = 340
const MAIL_PANEL_W = 340
const dayGearEl = ref<HTMLElement | null>(null)
const daySettingsEl = ref<HTMLElement | null>(null)
const daySettingsPos = ref<Record<string, string>>({})
// 邮件账号设置与「日期/天气」是两个独立浮层，共用同一套定位算法。
const showMailSettings = ref(false)
const mailGearEl = ref<HTMLElement | null>(null)
const mailSettingsEl = ref<HTMLElement | null>(null)
const mailSettingsPos = ref<Record<string, string>>({})

// 两个浮层只差锚点、宽度和位置 ref，定位逻辑必须只有一份：右边沿对齐锚点、
// 下方空间不足时翻到锚点上方、面板超高时自己滚（不撑破视口）。
function placeFloating(anchor: HTMLElement | null, pos: { value: Record<string, string> }, width: number) {
  if (!anchor) return
  const r = anchor.getBoundingClientRect()
  const gap = 8
  const w = Math.min(width, window.innerWidth - 24)
  const left = Math.max(12, Math.min(r.right - w, window.innerWidth - w - 12))
  const below = window.innerHeight - r.bottom - gap
  if (below < 360 && r.top - gap > below) {
    const bottom = window.innerHeight - r.top + gap
    pos.value = {
      left: `${Math.round(left)}px`,
      bottom: `${Math.round(bottom)}px`,
      maxHeight: `calc(100vh - ${Math.round(bottom)}px - 12px)`,
    }
    return
  }
  const top = r.bottom + gap
  pos.value = {
    left: `${Math.round(left)}px`,
    top: `${Math.round(top)}px`,
    maxHeight: `calc(100vh - ${Math.round(top)}px - 12px)`,
  }
}

function placeDaySettings() { placeFloating(dayGearEl.value, daySettingsPos, DAY_PANEL_W) }
function placeMailSettings() { placeFloating(mailGearEl.value, mailSettingsPos, MAIL_PANEL_W) }

// 置物架的「添加」菜单：文件与文件夹只能分两次选（对话框互斥），但入口可以收成
// 一个按钮 + 两项菜单 —— 卡头上常驻两个链接，会让人以为它们是两个并列功能。
const SHELF_MENU_W = 156
const showShelfMenu = ref(false)
const shelfAddEl = ref<HTMLElement | null>(null)
const shelfMenuEl = ref<HTMLElement | null>(null)
const shelfMenuPos = ref<Record<string, string>>({})

function placeShelfMenu() {
  if (!showShelfMenu.value) return
  placeFloating(shelfAddEl.value, shelfMenuPos, SHELF_MENU_W)
}
function toggleShelfMenu() {
  if (showShelfMenu.value) {
    showShelfMenu.value = false
    return
  }
  placeFloating(shelfAddEl.value, shelfMenuPos, SHELF_MENU_W)
  showShelfMenu.value = true
}
function closeShelfMenu() { showShelfMenu.value = false }

function placeAllFloating() { placeDaySettings(); placeMailSettings(); placeShelfMenu() }

function closeDaySettings() { showDaySettings.value = false }
function closeMailSettings() { showMailSettings.value = false }

// 点击外部关闭：用 mousedown 而不是 click，否则面板里的按钮会在同一次事件里
// 随 DOM 一起消失、click 永远收不到（同 NotificationCenter）。
function onDayDocDown(e: MouseEvent) {
  const node = (e.target as Node | null) || document.body
  if (showDaySettings.value) {
    if (daySettingsEl.value?.contains(node)) return
    if (dayGearEl.value?.contains(node)) return
    closeDaySettings()
  }
  if (showMailSettings.value) {
    if (mailSettingsEl.value?.contains(node)) return
    if (mailGearEl.value?.contains(node)) return
    closeMailSettings()
  }
  // 菜单的锚点按钮要排除：它的 toggle 走 click，若 mousedown 先把它关掉，
  // 随后的 click 又打开一次 —— 表现就是「点按钮关不掉菜单」。
  if (showShelfMenu.value) {
    if (shelfMenuEl.value?.contains(node)) return
    if (shelfAddEl.value?.contains(node)) return
    closeShelfMenu()
  }
}

// 输入法组词时的 Esc 是「取消候选」，不能拿来收起内容。
// 收起顺序固定「先弹层、后任务」：设置浮层优先，其次才是内联的宜忌详情；
// 同一次 Esc 只做一件事，拦住冒泡，别顺手触发全局「收起任务」的快捷键。
function onDayKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (e.isComposing || e.keyCode === 229) return
  if (showMailSettings.value) closeMailSettings()
  else if (showShelfMenu.value) closeShelfMenu()
  else if (showDaySettings.value) closeDaySettings()
  else if (showYiJiDetail.value) showYiJiDetail.value = false
  else return
  e.preventDefault()
  e.stopPropagation()
}

// 页面滚动直接收起：fixed 浮层不会跟着齿轮走，边滚边重定位又会让整页每帧重渲染。
// 面板内部自己的滚动（城市结果列表 / 面板超高）不算，否则一滚列表就把设置关了。
function onDayScroll(e: Event) {
  const target = e.target as Node | null
  if (showDaySettings.value) {
    if (target && daySettingsEl.value?.contains(target)) return
    closeDaySettings()
  }
  if (showMailSettings.value) {
    if (target && mailSettingsEl.value?.contains(target)) return
    closeMailSettings()
  }
  if (showShelfMenu.value) {
    if (target && shelfMenuEl.value?.contains(target)) return
    closeShelfMenu()
  }
}

watch(showDaySettings, (v) => {
  if (!v) return
  placeDaySettings()               // 先算好，避免首帧 :style 为空、浮层闪在 body 左上角
  void nextTick(placeDaySettings)  // 面板插入后布局稳定了再校正一次
})
watch(showMailSettings, (v) => {
  if (!v) return
  placeMailSettings()
  void nextTick(placeMailSettings)
})
watch(showShelfMenu, (v) => {
  if (!v) return
  placeShelfMenu()
  void nextTick(placeShelfMenu)
})

// ---- 邮件验证码卡片 ----
// 后端持有 IMAP 长连接与唯一快照，这里只做「读快照 / 报配置 / 点一下复制」。
// 前端绝不碰外网：WebView2 跟随系统代理时外部请求会全超时，这条坑已经踩过一次。
interface MailCode {
  id: string; uid: number; account: string; from: string
  subject: string; code: string; preview: string; received: number
}
interface MailConfig {
  host: string; port: number; email: string; enabled: boolean; connected: boolean
}
const MAIL_EXPIRE_MS = 10 * 60 * 1000
const mailCodes = ref<MailCode[]>([])
const mailCfg = ref<MailConfig>({ host: '', port: 993, email: '', enabled: false, connected: false })
const mailBusy = ref(false)
const mailError = ref('')
const mailCopied = ref('')
const mailNow = ref(Date.now())
const mailTestBusy = ref(false)
const mailTestMsg = ref('')
const mailTestOk = ref(false)
const mailForm = reactive({ email: '', host: '', port: 993, password: '', enabled: false })

function isMailExpired(received: number) { return mailNow.value - received > MAIL_EXPIRE_MS }
function mailTimeText(received: number) {
  return new Date(received).toLocaleTimeString(locale.value === 'en-US' ? 'en-US' : 'zh-CN', {
    hour: '2-digit', minute: '2-digit',
  })
}

async function loadMailConfig() {
  try {
    const cfg = unwrap<MailConfig>(await GetConfig())
    if (!cfg) return
    mailCfg.value = cfg
    // 授权码刻意不回显：连自己都别把它渲染进 DOM。password 留空 = 保存时不改。
    Object.assign(mailForm, {
      email: cfg.email, host: cfg.host, port: cfg.port || 993,
      enabled: cfg.enabled, password: '',
    })
  } catch (e) { logErr('dashboard.mailConfig', e) }
}

async function loadMailCodes() {
  try {
    // 顺带读一次配置：连接状态点要跟着后台重连结果变，但绝不能在这里重置
    // mailForm（用户可能正开着设置浮层填授权码）。
    const [listRaw, cfgRaw] = await Promise.all([GetCodes(), GetConfig()])
    const list = unwrap<MailCode[]>(listRaw)
    const cfg = unwrap<MailConfig>(cfgRaw)
    mailCodes.value = Array.isArray(list) ? list : []
    if (cfg) mailCfg.value = cfg
  } catch (e) { logErr('dashboard.mailCodes', e) }
}

async function refreshMailCodes() {
  if (mailBusy.value || !mailCfg.value.enabled) return
  mailBusy.value = true
  try {
    unwrap(await RefreshMail())
    mailError.value = ''
    await loadMailCodes()
  } catch (e) {
    mailError.value = getErrorMessage(e)
    logErr('dashboard.mailRefresh', e)
  } finally { mailBusy.value = false }
}

async function copyCode(m: MailCode) {
  if (await copyToClipboard(m.code)) {
    mailCopied.value = m.id
    setTimeout(() => { if (mailCopied.value === m.id) mailCopied.value = '' }, 1600)
  } else {
    toast.error(t('mailCopyFailed'))
  }
}

async function dismissCode(id: string) {
  try {
    unwrap(await Dismiss(id))
    await loadMailCodes()
  } catch (e) { toast.error(getErrorMessage(e)); logErr('dashboard.mailDismiss', e) }
}

function openMailSettings() {
  showDaySettings.value = false
  mailTestMsg.value = ''
  showMailSettings.value = !showMailSettings.value
}

async function saveMailConfig() {
  try {
    unwrap(await SetConfig(
      mailForm.host.trim(), Number(mailForm.port) || 993,
      mailForm.email.trim(), mailForm.password, mailForm.enabled,
    ))
    mailForm.password = ''
    await loadMailConfig()
    closeMailSettings()
    toast.success(t('mailSaved'))
    if (mailCfg.value.enabled) void refreshMailCodes()
  } catch (e) { toast.error(getErrorMessage(e)); logErr('dashboard.mailSave', e) }
}

async function testMailConfig() {
  if (mailTestBusy.value) return
  mailTestBusy.value = true
  mailTestMsg.value = ''
  try {
    unwrap(await TestConnection(
      mailForm.host.trim(), Number(mailForm.port) || 993,
      mailForm.email.trim(), mailForm.password,
    ))
    mailTestOk.value = true
    mailTestMsg.value = t('mailTestOk')
  } catch (e) {
    mailTestOk.value = false
    mailTestMsg.value = getErrorMessage(e)
    logErr('dashboard.mailTest', e)
  } finally { mailTestBusy.value = false }
}

// ---- 文件置物架 ----
// 只记路径，永不搬动用户文件。拖入走 Wails 的 WindowFilesDropped 宿主事件
// （WebView2 的 dataTransfer.files 只有文件名、拿不到绝对路径），后端入库后广播事件。
interface ShelfItem {
  id: string; path: string; name: string; kind: string
  sort: number; createdTs: number; lastOpenedTs: number; exists: boolean
}
const shelfItems = ref<ShelfItem[]>([])
const shelfDragOver = ref(false)
let offShelfChanged: (() => void) | null = null
let offMailChanged: (() => void) | null = null

async function loadShelf() {
  try {
    const list = unwrap<ShelfItem[]>(await ListShelf())
    shelfItems.value = Array.isArray(list) ? list : []
  } catch (e) { logErr('dashboard.shelfList', e) }
}

/**
 * 添加文件或文件夹。
 *
 * Windows 的文件对话框里「选文件」与「选目录」互斥（FOS_PICKFOLDERS 一开就选不了
 * 文件，见 pkg/application/dialogs_windows.go 的 openDialogKindFor），所以只能是
 * 两次对话框 —— 但入口可以合成一个按钮 + 两项菜单，不必让卡头常驻两个链接。
 *
 * CanChooseFiles 故意不传：两者都为 false 时 Wails 会默认回落到只选文件。
 */
async function addShelf(dir: boolean) {
  closeShelfMenu()
  try {
    const result = await Dialogs.OpenFile({
      Title: dir ? t('shelfPickDirTitle') : t('shelfPickTitle'),
      CanChooseDirectories: dir,
      AllowsMultipleSelection: true,
    })
    const paths = Array.isArray(result) ? result : result ? [result] : []
    if (!paths.length) return
    unwrap(await AddPaths(paths))
    await loadShelf()
  } catch (e) {
    // 用户点取消不是失败：Wails 会把它当错误抛出来，这里吞掉。
    if (isDialogCanceled(e)) return
    toast.error(getErrorMessage(e))
    logErr('dashboard.shelfAdd', e)
  }
}

async function openShelfItem(it: ShelfItem) {
  try {
    unwrap(await OpenShelf(it.id))
    void loadShelf()
  } catch (e) { toast.error(getErrorMessage(e)); logErr('dashboard.shelfOpen', e) }
}

async function revealShelfItem(it: ShelfItem) {
  try { unwrap(await RevealShelf(it.id)) }
  catch (e) { toast.error(getErrorMessage(e)); logErr('dashboard.shelfReveal', e) }
}

async function removeShelfItem(it: ShelfItem) {
  try {
    unwrap(await RemoveShelf(it.id))
    await loadShelf()
  } catch (e) { toast.error(getErrorMessage(e)); logErr('dashboard.shelfRemove', e) }
}

function dragHasFiles(e: DragEvent) {
  const types = e.dataTransfer?.types
  return !!types && Array.from(types).includes('Files')
}
function onShelfDrag(e: DragEvent) {
  if (!dragHasFiles(e)) return      // 内部拖拽排序照常走，不抢事件
  e.preventDefault()
  shelfDragOver.value = true
}
function onShelfLeave() { shelfDragOver.value = false }

/** 天气图标按 weatherGroup 的 8 类一一对应，不随主题存色值（颜色全用 token）。 */
const WEATHER_ICONS = [Sun, CloudSun, Cloud, CloudFog, CloudRain, CloudSnow, CloudLightning, CloudOff]
/** app_state 的前端 key 白名单只放行 qd_ 前缀（internal/db/db.go:validateAppStateKey）。
 *  宜忌偏好与天气配置分开存：后者要整个对象报给 Go 后台，多塞字段只会污染那边的类型。 */
const WEATHER_CFG_KEY = 'qd_weather_config'
const DAY_PREFS_KEY = 'qd_day_prefs'

/** i18n 里的名称表用逗号分隔，取第 n 项；越界退回空串而不是 undefined 渲染出来。 */
function nameAt(key: string, index: number): string {
  if (index < 0) return ''
  return String(t(key)).split(',')[index] ?? ''
}

/**
 * 日期展示统一以 dayInfo.date 为真源：computed 里裸写 new Date() 没有响应式依赖，
 * 跨天后 refreshDayIfNeeded 换掉 dayInfo 也不会重算，数字会永远停在打开页面那天。
 */
const baseDate = computed(() => {
  const [y, m, d] = dayInfo.value.date.split('-').map(Number)
  return new Date(y, m - 1, d)
})
const dateLocale = computed(() => locale.value === 'en-US' ? 'en-US' : 'zh-CN')
const solarDay = computed(() => String(baseDate.value.getDate()).padStart(2, '0'))
const solarMonthYear = computed(() =>
  baseDate.value.toLocaleDateString(dateLocale.value, { year: 'numeric', month: 'long' }))
const weekdayText = computed(() =>
  baseDate.value.toLocaleDateString(dateLocale.value, { weekday: 'long' }))

const lunarLine = computed(() => {
  const d = dayInfo.value
  if (!d.month || !d.day) return ''
  return t('daybarLunarLine', {
    ganzhi: d.ganzhiYear,
    leap: d.leap ? t('daybarLunarLeap') : '',
    month: nameAt('lunarMonthNames', d.month - 1),
    day: nameAt('lunarDayNames', d.day - 1),
  })
})
const zodiacText = computed(() => {
  const z = nameAt('lunarZodiacNames', dayInfo.value.zodiac)
  return z ? t('daybarZodiac', { z }) : ''
})
const termLine = computed(() => {
  const d = dayInfo.value
  const today = nameAt('lunarTermNames', d.term)
  if (today) return t('daybarTermToday', { term: today })
  const next = nameAt('lunarTermNames', d.nextTerm)
  if (!next) return ''
  return d.nextTermIn <= 1
    ? t('daybarTermNextTomorrow', { term: next })
    : t('daybarTermNext', { term: next, n: d.nextTermIn })
})
const festivalText = computed(() =>
  dayInfo.value.festivals.map(k => t('festival' + k)).join(' · '))

// ---- 今日宜忌：默认只露一行摘要（宜 4 / 忌 3），全量与神煞要点开才铺开 ----
// computed 挂 dayInfo.date，跨天自动重算；describeHuangLi 内部按日期缓存，
// 同一天里组件反复挂载也不会重复解析规则表。
const YI_BRIEF = 4
const JI_BRIEF = 3
const huangli = computed<HuangLiDay | null>(() => {
  const [y, m, d] = dayInfo.value.date.split('-').map(Number)
  return describeHuangLi(new Date(y, m - 1, d))
})
const yiBrief = computed(() => huangli.value?.yi.slice(0, YI_BRIEF).join(' ') ?? '')
const jiBrief = computed(() => huangli.value?.ji.slice(0, JI_BRIEF).join(' ') ?? '')
const yiRest = computed(() => Math.max(0, (huangli.value?.yi.length ?? 0) - YI_BRIEF))
const jiRest = computed(() => Math.max(0, (huangli.value?.ji.length ?? 0) - JI_BRIEF))

const weatherIcon = computed(() => WEATHER_ICONS[weatherGroup(weather.value?.code ?? -1)] ?? CloudOff)
const weatherKind = computed(() => nameAt('weatherKinds', weatherGroup(weather.value?.code ?? -1)))
const weatherTime = computed(() => weather.value
  ? new Date(weather.value.updatedAt).toLocaleTimeString(
      locale.value === 'en-US' ? 'en-US' : 'zh-CN', { hour: '2-digit', minute: '2-digit' })
  : '')

const todos = ref<Todo[]>([])
const monitors = ref<Monitor[]>([])
const runtimes = ref<RuntimeInfo[]>([])
// 两个不归 env 监督器管的常驻进程：只取「跑没跑 / 端口」，其余字段这里用不上
const fileSearch = ref<{ running: boolean; enabled: boolean } | null>(null)
const dsh = ref<{ running: boolean; port: number } | null>(null)
const ports = ref<PortInfo[]>([])
const items = ref<CollectionItem[]>([])
const gitRepos = ref<GitRepoRow[]>([])
// 24h 可用率：stats 是后端聚合好的汇总，logs 用来画逐时段热力条
const monStats = ref<Record<string, MonitorStat>>({})
const monLogs = ref<Record<string, CheckPoint[]>>({})

const loading = ref(false)
const failed = ref<Record<string, boolean>>({})

const todayStr = computed(() => {
  const d = new Date()
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${mm}-${dd}`
})

// 问候归到最上面那张「今日」卡的头部：它和日期、农历、天气同属「开工第一眼」。
// 公历/星期的唯一展示位仍是 db-date，别再往问候旁边挂小字日期。
const greeting = computed(() => {
  const h = new Date().getHours()
  if (h < 6) return t('dash_greet_night')
  if (h < 12) return t('dash_greet_morning')
  if (h < 18) return t('dash_greet_afternoon')
  return t('dash_greet_evening')
})

// ---- 指标 ----
const openTodos = computed(() => todos.value.filter(x => x.status !== 'done'))
const todayDue = computed(() => openTodos.value.filter(x => x.dueDate === todayStr.value).length)
const overdue = computed(() => openTodos.value.filter(x => x.dueDate && x.dueDate < todayStr.value).length)
const enabledMonitors = computed(() => monitors.value.filter(m => m.enabled))
const downMonitors = computed(() => enabledMonitors.value.filter(m => m.lastStatus === 'down'))
const residentRuntimes = computed(() => runtimes.value.filter(r => r.enabled))
const installedRuntimes = computed(() => runtimes.value.filter(r => (r.installed?.length || 0) > 0))

// 「今日」卡头部的副句：让大字问候后面跟一句有内容的状态，而不是一片空白。
// 全部复用上面算好的指标，不额外发请求；没异常时给一句轻的收尾。
const dayNote = computed(() => {
  if (overdue.value > 0 || downMonitors.value.length > 0) {
    return t('dash_day_note_busy', { overdue: overdue.value, down: downMonitors.value.length })
  }
  if (todayDue.value > 0) return t('dash_day_note_due', { n: todayDue.value })
  return t('dash_day_note_calm')
})
const overviewAlert = computed(() => overdue.value > 0 || downMonitors.value.length > 0)
const dirtyRepos = computed(() => gitRepos.value.filter(r => r.changes > 0 || r.ahead > 0 || !!r.error).length)
const lastRefresh = ref<Date | null>(null)
const lastRefreshText = computed(() => lastRefresh.value
  ? lastRefresh.value.toLocaleTimeString(locale.value === 'en-US' ? 'en-US' : 'zh-CN', {
      hour: '2-digit', minute: '2-digit',
    })
  : '--')

// ---- 24h 可用率图表（纯 CSS 热力条，不引图表库：桌面端无外网，CDN 图表库不可用）----
/** 热力格数：48 格 = 30 分钟/格，与后端 logWindowSec(24h) 对齐；mini 条共用同一份格。 */
const RIBBON_CELLS = 48
const DAY_SEC = 86400

/** 一条监控的 24h 热力格。30 分钟内挂过一次就算 down（宁可红多不漏报）。 */
function bucketsOf(id: string): ('up' | 'down' | 'none')[] {
  const logs = monLogs.value[id]
  const out: ('up' | 'down' | 'none')[] = Array(RIBBON_CELLS).fill('none')
  if (!logs?.length) return out
  const start = Math.floor(Date.now() / 1000) - DAY_SEC
  for (const l of logs) {
    const idx = Math.floor(((l.checkedTs - start) / DAY_SEC) * RIBBON_CELLS)
    if (idx < 0 || idx >= RIBBON_CELLS) continue
    if (l.status === 'down') out[idx] = 'down'
    else if (out[idx] === 'none') out[idx] = 'up'
  }
  return out
}

/** 全局条：启用中的监控一起聚合——一格红 = 这半小时里至少有一个服务挂过。 */
const globalRibbon = computed<( 'up' | 'down' | 'none' )[]>(() => {
  const ids = monitors.value.filter(m => m.enabled).map(m => m.id)
  if (!ids.length) return Array(RIBBON_CELLS).fill('none')
  const per = ids.map(bucketsOf)
  return Array.from({ length: RIBBON_CELLS }, (_, i) =>
    per.some(b => b[i] === 'down') ? 'down' : per.some(b => b[i] === 'up') ? 'up' : 'none')
})

/** 整体可用率 = 启用监控的 up 次数总和 / 检测总次数（后端已按 24h 窗口算好）。 */
const globalUptime = computed(() => {
  const ids = new Set(monitors.value.filter(m => m.enabled).map(m => m.id))
  let total = 0, up = 0
  for (const s of Object.values(monStats.value)) {
    if (!ids.has(s.monitorId)) continue
    total += s.totalChecks
    up += s.upChecks
  }
  return total ? (up / total) * 100 : -1
})

const globalUptimeText = computed(() =>
  globalUptime.value < 0 ? '—' : globalUptime.value.toFixed(1) + '%')

/** 单项可用率文案；没有检测记录显示占位。 */
function upPct(id: string): string {
  const s = monStats.value[id]
  return s && s.totalChecks ? s.uptimeRatio.toFixed(1) + '%' : '—'
}

/** 今日待办的到期分布条：三段各占的数量比例，颜色即语义（红=逾期、琥珀=今天、蓝=以后）。 */
const todoDist = computed(() => {
  const later = Math.max(0, openTodos.value.length - overdue.value - todayDue.value)
  return [
    { k: 'overdue', n: overdue.value, cls: 'danger' },
    { k: 'today', n: todayDue.value, cls: 'warn' },
    { k: 'later', n: later, cls: 'ok' },
  ].filter(x => x.n > 0)
})

/**
 * 常驻服务清单 = 环境的「常驻运行时」+ 两个自带常驻进程（Everything / DSH）。
 *
 * 为什么不只数 EnvList：Everything 与 DSH 不归 env 的监督器管——Everything 的开关是
 * 「文件搜索」总开关 + 自己的进程，DSH 是独立 web 服务（有自己的 running/端口）。
 * 只统计 env 的话，这两样明明在后台吃内存却不出现在这张卡上，排查「谁在跑」时漏人。
 */
const residentServices = computed<ResidentRow[]>(() => {
  const rows: ResidentRow[] = residentRuntimes.value.map(r => ({
    id: 'env:' + r.id,
    name: r.name,
    meta: t('dash_env_versions', { n: r.installed?.length || 0 }),
    page: 'environments',
    // env 的区块 id 就是运行时 id：点哪一行就定位到那个运行时的面板
    target: r.id,
  }))
  // 进程在跑就算常驻：文件搜索总开关关掉时它仍是常驻进程，只是不参与检索
  if (fileSearch.value?.running) {
    rows.push({ id: 'everything', name: 'Everything', meta: t('dash_svc_filesearch'), page: 'environments', target: 'everything' })
  }
  // DSH 也进常驻卡，但落点是环境管理的 harness 区块而不是唤起窗口：
  // 这张卡回答的是「谁在后台占着资源 / 它现在什么状态」，点进去看状态比弹一个终端更贴题。
  if (dsh.value?.running) {
    rows.push({ id: 'dsh', name: 'DSH', meta: dsh.value.port ? `:${dsh.value.port}` : '', page: 'environments', target: 'harness' })
  }
  return rows.sort((a, b) => a.name.localeCompare(b.name))
})

/** 卡片里最多列 6 条：再多就成了一张要滚动的表，而仪表盘只负责「先看见」。 */
const residentList = computed(() => residentServices.value.slice(0, 6))

// ---- 列表 ----
/** 今日待办：逾期的最靠前，其次今天到期，再按创建顺序兜底。 */
const todoList = computed(() =>
  [...openTodos.value]
    .sort((a, b) => {
      const ad = a.dueDate || '9999-12-31'
      const bd = b.dueDate || '9999-12-31'
      return ad === bd ? 0 : ad < bd ? -1 : 1
    })
    .slice(0, 6))

/** 服务健康：红的在最前，其次是「还没检测过」，最后才是正常的。 */
const monitorList = computed(() => {
  const rank = (m: Monitor) => (m.lastStatus === 'down' ? 0 : m.lastStatus ? 2 : 1)
  return [...monitors.value].sort((a, b) => rank(a) - rank(b)).slice(0, 6)
})

const portList = computed(() =>
  [...ports.value].sort((a, b) => a.port - b.port).slice(0, 6))

/** 需要关注的仓库：有改动 / 领先落后 / 出错的排前面，干净的沉底。 */
const gitList = computed(() =>
  [...gitRepos.value]
    .sort((a, b) => {
      const wa = (a.error ? 4 : 0) + (a.changes ? 2 : 0) + (a.ahead + a.behind ? 1 : 0)
      const wb = (b.error ? 4 : 0) + (b.changes ? 2 : 0) + (b.ahead + b.behind ? 1 : 0)
      return wb - wa
    })
    .slice(0, 6))
const gitBusyCount = computed(() =>
  gitRepos.value.reduce((n, r) => n + (r.changes ? 1 : 0), 0))

function dueText(x: Todo): string {
  if (!x.dueDate) return ''
  if (x.dueDate < todayStr.value) return t('dash_overdue')
  if (x.dueDate === todayStr.value) return t('dash_due_today')
  return x.dueDate.slice(5)
}

/** 逾期或今天到期都算「要紧张一下」，列表里标红提醒。 */
function isUrgent(x: Todo): boolean {
  return !!x.dueDate && x.dueDate <= todayStr.value
}

// ---- 加载 ----
async function loadTodos() {
  try {
    todos.value = (unwrap(await ListTodos()) as Todo[] | null) || []
    delete failed.value.todo
  } catch (e) {
    failed.value.todo = true
    logErr('dashboard.todos', e)
  }
}

async function loadMonitors() {
  try {
    monitors.value = (unwrap(await ListMonitors()) as Monitor[] | null) || []
    delete failed.value.monitor
  } catch (e) {
    failed.value.monitor = true
    logErr('dashboard.monitors', e)
    return
  }
  await loadUptime()
}

/**
 * 24h 可用率：汇总一次（stats），再对启用中的监控逐个取检测日志画热力条。
 * 日志按监控独立请求（都是本地 IPC，监控通常个位数，全并发）；单项失败
 * 只让那一行的条是「无数据」，不拖垮整块。
 */
async function loadUptime() {
  try {
    const st = (unwrap(await ListMonitorStats()) as MonitorStat[] | null) || []
    const map: Record<string, MonitorStat> = {}
    for (const s of st) map[s.monitorId] = s
    monStats.value = map
  } catch (e) {
    logErr('dashboard.monStats', e)
    return
  }
  const enabled = monitors.value.filter(m => m.enabled).slice(0, 8)
  const since = Math.floor(Date.now() / 1000) - DAY_SEC
  const pairs = await Promise.all(enabled.map(async m => {
    try {
      const logs = (unwrap(await GetMonitorLogsSince(m.id, since, 1000)) as CheckPoint[] | null) || []
      return [m.id, logs] as const
    } catch {
      return [m.id, [] as CheckPoint[]] as const
    }
  }))
  monLogs.value = Object.fromEntries(pairs)
}

async function loadRuntimes() {
  try {
    runtimes.value = (unwrap(await EnvList()) as RuntimeInfo[] | null) || []
    delete failed.value.env
  } catch (e) {
    failed.value.env = true
    logErr('dashboard.env', e)
  }
}

/**
 * Everything / DSH 的常驻状态。两者都不在 EnvList 里，各走自己的状态接口。
 * 独立 catch：探测失败只让那一行不出现，不该把「常驻服务」卡整张拖成加载失败。
 */
async function loadTools() {
  const [es, ds] = await Promise.allSettled([EnvEverythingStatus(), DSHStatus()])
  if (es.status === 'fulfilled') {
    const v = unwrap<{ running?: boolean; enabled?: boolean }>(es.value)
    fileSearch.value = v ? { running: !!v.running, enabled: !!v.enabled } : null
  } else {
    logErr('dashboard.everything', es.reason)
  }
  if (ds.status === 'fulfilled') {
    const v = unwrap<{ running?: boolean; port?: number }>(ds.value)
    dsh.value = v ? { running: !!v.running, port: v.port || 0 } : null
  } else {
    logErr('dashboard.dsh', ds.reason)
  }
}

async function loadPorts() {
  try {
    ports.value = (unwrap(await ListListeningPorts()) as PortInfo[] | null) || []
    delete failed.value.port
  } catch (e) {
    failed.value.port = true
    logErr('dashboard.port', e)
  }
}

async function loadItems() {
  try {
    items.value = (unwrap(await GetMostUsedItems(6)) as CollectionItem[] | null) || []
    delete failed.value.item
  } catch (e) {
    failed.value.item = true
    logErr('dashboard.items', e)
  }
}

/** Git 仓库状态：先拿清单，再逐个查 status（仓库少、命令都是本地的，可以全并发）。 */
async function loadGit() {
  try {
    const refs = (unwrap(await GitListRepos()) as GitRepoRef[] | null) || []
    const rows = await Promise.all(refs.map(async r => {
      try {
        const st = unwrap<{ branch: string; ahead: number; behind: number; changes: unknown[] | null }>(
          await GitRepoStatus(r.path))
        return {
          name: r.name, path: r.path,
          branch: st?.branch || '', ahead: st?.ahead || 0, behind: st?.behind || 0,
          changes: st?.changes?.length || 0, error: '',
        } as GitRepoRow
      } catch (e) {
        logErr('dashboard.gitStatus', e)
        // 出错也要带 path：这行照样能点开对应仓库，错误本身在 git 页里看得更全
        return { name: r.name, path: r.path, branch: '', ahead: 0, behind: 0, changes: 0, error: t('dash_git_err') }
      }
    }))
    gitRepos.value = rows
    delete failed.value.git
  } catch (e) {
    failed.value.git = true
    logErr('dashboard.git', e)
  }
}

// ===== AI 用量 =====
//
// 读 ai_usage 明细表的按天聚合（一次分组查询喂给总额/按模型/按天/按来源四路，
// 见 services/ai/ai_stats.go）。它要扫全表，所以不进 60 秒轮询的常驻队列，
// 只在挂载与手动刷新时取一次。

const AI_USAGE_DAYS = 7
const aiUsage = ref<AIUsageStatsResult | null>(null)
const aiUsageFailed = ref(false)
// 整张卡跟着「有没有配模型」显隐：没配 AI 的人看到一张「¥0.00 · 0 次」的卡，
// 只会以为它坏了；而它本该回答的问题（我花了多少）对这个人根本不存在。
const aiConfigured = ref(false)

/**
 * 只判 profiles 非空，不看 active 指向是否有效：档案在、active 失联属于
 * 「配置坏了」，那种情况反而更该把卡亮出来——点「全部」进设置就能看到问题。
 */
async function loadAIConfig() {
  try {
    const res = unwrap(await AIListProfiles()) as { profiles?: unknown[] } | null
    aiConfigured.value = (res?.profiles?.length ?? 0) > 0
  } catch (e) {
    aiConfigured.value = false
    logErr('dashboard.aiConfig', e)
  }
}

async function loadAIUsage() {
  try {
    aiUsage.value = unwrap<AIUsageStatsResult>(await AIUsageStats(AI_USAGE_DAYS)) ?? null
    aiUsageFailed.value = false
  } catch (e) {
    aiUsageFailed.value = true
    logErr('dashboard.aiUsage', e)
  }
}

/** 今日那一行（daily 的 key 是 YYYY-MM-DD，与 todayStr 同一口径，可直接查）。 */
const aiToday = computed<AICostEntry | null>(
  () => aiUsage.value?.daily.find(d => d.key === todayStr.value) ?? null,
)
const aiTodayCalls = computed(() => aiToday.value?.requests ?? 0)
const aiTodayTokens = computed(() => aiToday.value?.totalTokens ?? 0)
/** 七日合计：今日可能还没开张，周量才说明「最近的节奏」。 */
const aiWeekCost = computed(() => aiUsage.value?.estimatedCost ?? 0)
const aiWeekCalls = computed(() => aiUsage.value?.requests ?? 0)
const aiBarMax = computed(() => Math.max(1, ...(aiUsage.value?.daily ?? []).map(d => d.totalTokens)))
/**
 * 柱高按 token 归一（不是按钱）：钱受单价影响，视觉上会变成「贵模型那根特别高」。
 *
 * 后端 daily 只含有调用记录的日期，直接铺出来柱数会少于 7 根、横轴还会跳日。
 * 这里按本地日期补齐窗口内每一天，没调用的那天就是一根 0 高的柱——
 * 「哪天闲了」本身也是这张图要回答的。
 */
const aiBars = computed(() => {
  const byDay = new Map((aiUsage.value?.daily ?? []).map(d => [d.key, d.totalTokens]))
  const now = new Date()
  const out: { key: string; pct: number; tokens: number }[] = []
  for (let i = AI_USAGE_DAYS - 1; i >= 0; i--) {
    const key = localISO(new Date(now.getFullYear(), now.getMonth(), now.getDate() - i))
    out.push({ key, pct: Math.round((byDay.get(key) ?? 0) / aiBarMax.value * 100), tokens: byDay.get(key) ?? 0 })
  }
  return out
})
const aiTopModels = computed(() => {
  const total = aiUsage.value?.totalTokens ?? 0
  return (aiUsage.value?.models ?? []).slice(0, 3).map(m => ({
    name: m.key || '—',
    pct: total ? Math.round((m.totalTokens / total) * 100) : 0,
    cost: m.estimatedCost,
  }))
})

/** 金额一律两位小数；不足一分显示 <0.01，避免「¥0.00」被读成没花钱。 */
function moneyText(v: number): string {
  if (!v) return '0.00'
  if (v < 0.01) return '<0.01'
  return v.toFixed(2)
}
/** token 数按 k/M 收敛：满屏六位数没人去数，量级才是要看的。 */
function tokensText(n: number): string {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k'
  return String(n)
}

// ---- 顶部信息条的数据 ----
async function loadDayPrefs() {
  try {
    const raw = unwrap<string>(await GetValue(DAY_PREFS_KEY))
    if (raw) dayPrefs.value = { ...DEFAULT_DAY_PREFS, ...(JSON.parse(raw) as Partial<DayPrefs>) }
  } catch (e) {
    // 读失败退回默认（显示宜忌）；纯本地偏好，静默降级即可
    logErr('dashboard.dayPrefs', e)
  }
}

async function saveDayPrefs() {
  try {
    unwrap(await SetValue(DAY_PREFS_KEY, JSON.stringify(dayPrefs.value)))
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('dashboard.dayPrefsSave', e)
  }
}

async function toggleYiJi() {
  dayPrefs.value = { ...dayPrefs.value, showYiJi: !dayPrefs.value.showYiJi }
  if (!dayPrefs.value.showYiJi) showYiJiDetail.value = false
  await saveDayPrefs()
}

async function loadWeatherCfg() {
  try {
    const raw = unwrap<string>(await GetValue(WEATHER_CFG_KEY))
    if (raw) weatherCfg.value = { ...DEFAULT_WEATHER, ...(JSON.parse(raw) as Partial<WeatherConfig>) }
    // 后台刷新要知道坐标与开关；配置没变时后端不会清快照，切页回来是秒回。
    await setWeatherConfig(weatherCfg.value)
  } catch (e) {
    // 配置读失败退回默认（天气关闭）；与其余卡片一致，静默降级只记日志
    logErr('dashboard.weatherCfg', e)
  }
}

async function saveWeatherCfg() {
  try {
    unwrap(await SetValue(WEATHER_CFG_KEY, JSON.stringify(weatherCfg.value)))
    await setWeatherConfig(weatherCfg.value)
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('dashboard.weatherCfgSave', e)
  }
}

/**
 * 读后端快照（不发外网请求）。force 只给手动刷新 / 改城市后的重试：
 * 那两个场景用户明确要「现在就要新数据」，才值得让 Go 立刻打一次 Open-Meteo。
 * 后台 30 分钟轮询由 services/weather 自己负责，这里不要再加任何定时器。
 */
async function loadWeather(force = false) {
  const cfg = weatherCfg.value
  if (!cfg.enabled || !cfg.city) {
    weather.value = null
    weatherError.value = false
    return
  }
  if (!force) {
    const cached = readWeatherCache(cfg.latitude, cfg.longitude)
    if (cached) weather.value = cached
  }
  if (weatherBusy.value) return
  weatherBusy.value = true
  try {
    const data = force ? await refreshWeather() : await fetchWeather()
    weather.value = data
    weatherError.value = false
    writeWeatherCache(cfg.latitude, cfg.longitude, data)
  } catch (e) {
    // 后台失败不弹 toast；有旧数据时连日志都省了——否则失败冷却那 5 分钟里
    // 每分钟读一次快照会刷出 5 条一模一样的错误。手动刷新失败仍要留痕。
    if (!weather.value) weatherError.value = true
    if (force || !weather.value) logErr('dashboard.weather', e)
  } finally {
    weatherBusy.value = false
  }
}

async function toggleWeather() {
  weatherCfg.value = { ...weatherCfg.value, enabled: !weatherCfg.value.enabled }
  await saveWeatherCfg()
  if (weatherCfg.value.enabled) await loadWeather()
  else {
    weather.value = null
    weatherError.value = false
  }
}

/** 「开启天气」一步到位：打开设置的同时先把开关打开，用户只剩选城市。 */
async function openWeatherSettings(enable: boolean) {
  showDaySettings.value = true
  if (enable && !weatherCfg.value.enabled) await toggleWeather()
}

async function doSearchCity() {
  const q = cityQuery.value.trim()
  if (!q || cityBusy.value) return
  cityBusy.value = true
  citySearched.value = false
  try {
    cityHits.value = await searchCities(q, locale.value === 'en-US' ? 'en' : 'zh')
    citySearched.value = true
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('dashboard.citySearch', e)
  } finally {
    cityBusy.value = false
  }
}

async function pickCity(c: CityHit) {
  weatherCfg.value = {
    enabled: true, city: c.name, latitude: c.latitude, longitude: c.longitude,
  }
  cityHits.value = []
  cityQuery.value = ''
  await saveWeatherCfg()
  await loadWeather(true)
}

/** 跨天重算农历/节气/宜忌：搭 60 秒监控轮询的便车，不另起定时器。 */
function refreshDayIfNeeded() {
  if (dayInfo.value.date !== localISO(new Date())) {
    dayInfo.value = describeDay()
    showYiJiDetail.value = false // 昨天展开的全量宜忌别留在今天
  }
}

async function refreshAll(silent = false) {
  if (!silent) loading.value = true
  // AI 用量要扫 ai_usage 全表聚合，不进 60 秒轮询；但用户点的是「全部刷新」，
  // 手动那一次得把它带上，否则卡片上的钱数会停在进来时的那个值。
  await Promise.all([
    loadTodos(), loadMonitors(), loadRuntimes(), loadTools(), loadPorts(), loadItems(), loadGit(),
    loadAIConfig(), loadAIUsage(),
  ])
  loading.value = false
  lastRefresh.value = new Date()
}

// ---- 交互 ----
/** 切到某个页面。target 非空时要求目标页定位到那一项（仓库 / 端口区块 / 运行时）。 */
function go(page: string, target = '') {
  if (target) navigateTo(page, target)
  else navigate(page)
}

async function toggleTodo(x: Todo) {
  try {
    unwrap(await ToggleTodo(x.id))
    await loadTodos()
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('dashboard.toggleTodo', e)
  }
}

async function openItem(it: CollectionItem) {
  try {
    unwrap(await OpenItem(it))
  } catch (e) {
    toast.error(getErrorMessage(e))
    logErr('dashboard.openItem', e)
  }
}

// ===== PREVIEW-ONLY（截图预览用：浏览器里没有 Wails 桥，加 ?preview=1 注入假数据以便 chrome headless 截图调样式；仅 DEV 生效，保留供后续视觉迭代）=====
const PREVIEW = import.meta.env.DEV && window.location.search.includes('preview=1')
function seedPreview() {
  const d = new Date()
  const day = (n: number) => {
    const x = new Date(d.getTime() + n * 86400000)
    return `${x.getFullYear()}-${String(x.getMonth() + 1).padStart(2, '0')}-${String(x.getDate()).padStart(2, '0')}`
  }
  todos.value = [
    { id: '1', title: '修 composer 在中文输入法下的回车误发送', status: 'open', priority: 'high', dueDate: day(-2) },
    { id: '2', title: '梳理 notes 重命名后遗留的接口字段', status: 'open', priority: 'normal', dueDate: day(-1) },
    { id: '3', title: '给 MCP 补 logs 工具', status: 'open', priority: 'normal', dueDate: day(0) },
    { id: '4', title: '复盘端口扫描插件的内存占用', status: 'open', priority: 'normal', dueDate: day(0) },
    { id: '5', title: '把 Git 面板的 diff 加行号', status: 'open', priority: 'normal', dueDate: day(2) },
    { id: '6', title: '整理数据目录迁移的验收清单', status: 'open', priority: 'normal', dueDate: '' },
  ] as unknown as Todo[]
  monitors.value = [
    { id: '1', name: '博客', kind: 'http', url: 'https://blog.example.dev', target: '', enabled: true, lastStatus: 'down' },
    { id: '2', name: 'MySQL', kind: 'tcp', url: '', target: '127.0.0.1:3306', enabled: true, lastStatus: 'down' },
    { id: '3', name: 'API 网关', kind: 'http', url: 'https://api.example.dev/health', target: '', enabled: true, lastStatus: 'up' },
    { id: '4', name: 'nginx', kind: 'process', url: '', target: 'nginx.exe', enabled: true, lastStatus: 'up' },
    { id: '5', name: 'Redis', kind: 'tcp', url: '', target: '6379', enabled: true, lastStatus: 'up' },
    { id: '6', name: '备份脚本', kind: 'cmd', url: '', target: 'backup.cmd --check', enabled: true, lastStatus: '' },
  ] as unknown as Monitor[]
  runtimes.value = [
    { id: 'nginx', name: 'Nginx', group: 'web', installed: [1, 2], enabled: true, hasService: true },
    { id: 'mysql', name: 'MySQL', group: 'db', installed: [1, 2, 3], enabled: true, hasService: true },
    { id: 'redis', name: 'Redis', group: 'db', installed: [1], enabled: true, hasService: true },
    { id: 'php', name: 'PHP', group: 'lang', installed: [1, 2], enabled: false, hasService: false },
    { id: 'node', name: 'Node.js', group: 'lang', installed: [1, 2, 3, 4], enabled: false, hasService: false },
    { id: 'go', name: 'Go', group: 'lang', installed: [1], enabled: false, hasService: false },
    { id: 'postgresql', name: 'PostgreSQL', group: 'db', installed: [], enabled: false, hasService: true },
    { id: 'mongo', name: 'MongoDB', group: 'db', installed: [1], enabled: false, hasService: true },
    { id: 'traefik', name: 'Traefik', group: 'web', installed: [1], enabled: false, hasService: true },
  ] as unknown as RuntimeInfo[]
  fileSearch.value = { running: true, enabled: true }
  dsh.value = { running: true, port: 8123 }
  // 热力条种子：给每个监控造一份 5 分钟间隔的 24h 检测记录，down 窗口用确定性
  // 区间（不用随机数，保证每次截图布局一致）；id 1/2 各挂两段、其余全绿。
  const nowSec = Math.floor(Date.now() / 1000)
  const badWindows: Record<string, number[][]> = {
    '1': [[nowSec - 3600 * 20, nowSec - 3600 * 19.4], [nowSec - 3600 * 6, nowSec - 3600 * 5.4]],
    '2': [[nowSec - 3600 * 11, nowSec - 3600 * 10.3]],
    '3': [[nowSec - 3600 * 2, nowSec - 3600 * 1.8]],
  }
  const seedLogs = (id: string): CheckPoint[] => {
    const arr: CheckPoint[] = []
    for (let t = nowSec - DAY_SEC; t <= nowSec; t += 300) {
      const bad = (badWindows[id] || []).some(([a, b]) => t >= a && t <= b)
      arr.push({ checkedTs: t, status: bad ? 'down' : 'up' })
    }
    return arr
  }
  // 「备份脚本」(id 6) 是待检测状态，不给它造数据——保持「无检测记录」的真实形态
  const seededIds = ['1', '2', '3', '4', '5']
  monStats.value = Object.fromEntries(seededIds.map(id => {
    const logs = seedLogs(id)
    const up = logs.filter(l => l.status === 'up').length
    return [id, {
      monitorId: id, totalChecks: logs.length, upChecks: up,
      uptimeRatio: (up / logs.length) * 100, avgLatencyMs: 42, lastDownAt: '',
    }]
  }))
  monLogs.value = Object.fromEntries(seededIds.map(id => [id, seedLogs(id)]))
  ports.value = [
    { port: 3306, protocol: 'TCP', pid: 4412, process: 'mysqld.exe', path: '', self: false },
    { port: 6379, protocol: 'TCP', pid: 8104, process: 'redis-server.exe', path: '', self: false },
    { port: 9245, protocol: 'TCP', pid: 22164, process: 'quickdock.exe', path: '', self: true },
    { port: 9230, protocol: 'TCP', pid: 22164, process: 'quickdock.exe', path: '', self: true },
    { port: 5173, protocol: 'TCP', pid: 15092, process: 'node.exe', path: '', self: false },
    { port: 8080, protocol: 'TCP', pid: 7712, process: 'nginx.exe', path: '', self: false },
  ]
  items.value = [
    { id: '1', name: 'quickdock', value: 'E:\\pp\\quickdock', type: 'folder' },
    { id: '2', name: 'hnwf', value: 'E:\\pp\\hnwf', type: 'folder' },
    { id: '3', name: '设计稿', value: 'https://figma.com/x', type: 'url' },
    { id: '4', name: '常用命令', value: 'git status --short', type: 'command' },
    { id: '5', name: 'release.md', value: 'E:\\pp\\quickdock\\docs\\release.md', type: 'file' },
  ] as unknown as CollectionItem[]
  gitRepos.value = [
    { name: 'quickdock', path: 'E:\\pp\\quickdock', branch: 'dev', ahead: 2, behind: 0, changes: 7, error: '' },
    { name: 'hnwf', path: 'E:\\pp\\hnwf', branch: 'main', ahead: 0, behind: 3, changes: 0, error: '' },
    { name: 'env-runtime', path: 'E:\\pp\\env-runtime', branch: 'feat/wsl', ahead: 1, behind: 0, changes: 0, error: '' },
    { name: 'docs', path: 'E:\\pp\\docs', branch: 'main', ahead: 0, behind: 0, changes: 0, error: '' },
  ]
  weatherCfg.value = { enabled: true, city: '北京', latitude: 39.9075, longitude: 116.39723 }
  weather.value = {
    temp: 14, apparent: 12, humidity: 48, wind: 9, code: 2, isDay: true,
    max: 17, min: 6, updatedAt: Date.now(), city: '北京',
  }
}
// ===== PREVIEW-ONLY END =====

// 监控状态是高变动数据，60 秒静默刷一次（静默=不显示 loading，免得用户正在看
// 这一页时整块内容闪一下）。只刷监控：待办变化由用户自己操作触发，
// 而端口与运行时清单要枚举系统进程/扫目录，每分钟跑一遍不值当。
// 同一轮顺带读一次天气快照：纯本地 IPC，后端 30 分钟换新后最多 1 分钟就能看到，
// 但不会因此产生任何外网请求。
let timer: ReturnType<typeof setInterval> | null = null
// onMounted 是 async：HMR 换模块时旧组件已卸载，但它的 await 链可能还在往下跑。
// 卸载即置位，每个 await 之后都判一次，别让旧模块继续写状态、发 IPC，
// 更别在卸载钩子里碰到「上一次热更新已经删掉」的变量。
let disposed = false
onMounted(async () => {
  // 浮层监听要先注册：预览模式下面也要能开设置面板，不能被下面的 return 跳过。
  window.addEventListener('resize', placeAllFloating)
  window.addEventListener('scroll', onDayScroll, true)
  document.addEventListener('mousedown', onDayDocDown, true)
  document.addEventListener('keydown', onDayKeydown)
  // 后端换快照 / 拖入文件后主动推事件，前端不必轮询外网。
  offMailChanged = Events.On('mailbox:changed', () => { void loadMailCodes() })
  offShelfChanged = Events.On('shelf:changed', () => { void loadShelf() })
  lastRefresh.value = new Date()
  if (PREVIEW) { seedPreview(); return }
  void refreshAll()
  timer = setInterval(() => {
    void loadMonitors()
    refreshDayIfNeeded()
    void loadWeather()
    mailNow.value = Date.now()      // 验证码 10 分钟过期态要自己走动
    void loadMailCodes()            // 纯本地 IPC，兼作事件丢失时的兜底
  }, 60000)
  await loadDayPrefs()
  if (disposed) return
  await loadWeatherCfg()
  if (disposed) return
  void loadWeather()
  void loadMailConfig()
  void loadMailCodes()
  void loadShelf()
})
onUnmounted(() => {
  disposed = true
  offMailChanged?.()
  offShelfChanged?.()
  window.removeEventListener('resize', placeAllFloating)
  window.removeEventListener('scroll', onDayScroll, true)
  document.removeEventListener('mousedown', onDayDocDown, true)
  document.removeEventListener('keydown', onDayKeydown)
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div class="qd-page">
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('navDashboard') }}</h2>
        <span class="qd-page-sub">{{ t('dash_subtitle') }}</span>
      </div>
      <div class="qd-page-actions">
        <button class="qd-btn qd-btn-ghost" :disabled="loading" @click="refreshAll()">
          <RefreshCw :size="13" :class="{ spin: loading }" />
          {{ t('dash_refresh') }}
        </button>
      </div>
    </div>

    <div class="qd-page-body">
      <div class="dash">
        <!-- 「今日」卡放在最上：问候 + 公历/农历/节气/天气都是「开工第一眼」的信息。
             问候原本是下面概览带的第一列，只有一行大字、下面整片空白；挪到这里当
             卡片头部，后面跟一句实时状态副句，层次和密度都更合理。
             做成通栏而不是第七张卡片：插进 6 列网格会让某行只剩 2 格、空 4 格。 -->
        <section class="daybar">
          <div class="db-head">
            <span class="db-greeting">{{ greeting }}</span>
            <span class="db-note">{{ dayNote }}</span>
            <!-- 刷新与设置归到头部右侧成一组：不再压在天气文字里，降级态也留着齿轮 -->
            <div class="db-actions">
              <button v-if="weather" class="db-icon-btn" :title="t('dash_refresh')" :disabled="weatherBusy"
                      @click="loadWeather(true)">
                <RefreshCw :size="14" :class="{ spin: weatherBusy }" />
              </button>
              <button ref="dayGearEl" class="db-icon-btn" :title="t('daybarSettingsTitle')"
                      @click="showDaySettings = !showDaySettings">
                <Settings2 :size="14" />
              </button>
            </div>
          </div>

          <div class="db-body">
            <div class="db-date">
              <span class="db-day">{{ solarDay }}</span>
              <div class="db-date-meta">
                <span class="db-ym">{{ solarMonthYear }}</span>
                <span class="db-week">{{ weekdayText }}</span>
              </div>
            </div>

            <div class="db-lunar">
              <div class="db-lunar-main">
                <CalendarDays :size="12" class="db-ic" />
                <span class="db-title">{{ t('daybarTitle') }}</span>
                <span v-if="lunarLine">{{ lunarLine }}</span>
                <span v-if="zodiacText" class="db-zodiac">{{ zodiacText }}</span>
              </div>
              <div class="db-lunar-sub">
                <span v-if="termLine" class="db-term">{{ termLine }}</span>
                <span v-if="festivalText" class="db-festival">{{ festivalText }}</span>
              </div>

              <!-- 宜忌默认只给一行摘要，全量与神煞要点开才铺开，别把信息条撑爆。
                   词条是规则库返回的中文术语，英文界面也保留汉字（见 utils/huangli.ts）。 -->
              <div v-if="dayPrefs.showYiJi" class="db-yiji">
                <template v-if="huangli">
                  <button class="db-yiji-toggle"
                          :title="showYiJiDetail ? t('daybarYiJiCollapse') : t('daybarYiJiExpand')"
                          :aria-expanded="showYiJiDetail"
                          @click="showYiJiDetail = !showYiJiDetail">
                    <span class="yj-tag yi">{{ t('daybarYi') }}</span>
                    <span class="yj-words">{{ yiBrief }}<span v-if="yiRest" class="yj-more">{{ t('daybarYiJiMore', { n: yiRest }) }}</span></span>
                    <span class="yj-tag ji">{{ t('daybarJi') }}</span>
                    <span class="yj-words">{{ jiBrief }}<span v-if="jiRest" class="yj-more">{{ t('daybarYiJiMore', { n: jiRest }) }}</span></span>
                    <ChevronDown :size="12" class="yj-chev" :class="{ open: showYiJiDetail }" />
                  </button>
                  <div v-if="showYiJiDetail" class="db-yiji-detail">
                    <div class="yj-row">
                      <span class="yj-label yi">{{ t('daybarYi') }}</span>
                      <span class="yj-full">{{ huangli.yi.join(' · ') }}</span>
                    </div>
                    <div class="yj-row">
                      <span class="yj-label ji">{{ t('daybarJi') }}</span>
                      <span class="yj-full">{{ huangli.ji.join(' · ') }}</span>
                    </div>
                    <div class="yj-facts">
                      <span><b>{{ t('daybarYiJiGanZhi') }}</b>{{ huangli.ganzhi }} · {{ huangli.naYin }}</span>
                      <span><b>{{ t('daybarYiJiZhiXing') }}</b>{{ huangli.zhiXing }}</span>
                      <span><b>{{ t('daybarYiJiChong') }}</b>{{ huangli.chong }} · {{ t('daybarYiJiSha', { n: huangli.sha }) }}</span>
                      <span><b>{{ t('daybarYiJiJiShen') }}</b>{{ huangli.jiShen.join(' ') }}</span>
                      <span><b>{{ t('daybarYiJiXiongSha') }}</b>{{ huangli.xiongSha.join(' ') }}</span>
                      <span><b>{{ t('daybarYiJiPengZu') }}</b>{{ huangli.pengZuGan }} · {{ huangli.pengZuZhi }}</span>
                    </div>
                    <span class="yj-disclaimer">{{ t('daybarYiJiHint') }}</span>
                  </div>
                </template>
                <span v-else class="yj-empty">{{ t('daybarYiJiEmpty') }}</span>
              </div>
            </div>

            <div class="db-weather">
              <template v-if="!weatherCfg.enabled">
                <span class="db-weather-off">{{ t('daybarWeatherOff') }}</span>
                <button class="db-text-btn" @click="openWeatherSettings(true)">{{ t('daybarWeatherEnable') }}</button>
              </template>
              <template v-else-if="!weatherCfg.city">
                <span class="db-weather-off">{{ t('daybarNoCity') }}</span>
                <button class="db-text-btn" @click="showDaySettings = true">{{ t('daybarCity') }}</button>
              </template>
              <template v-else-if="!weather && weatherBusy">
                <span class="db-weather-off">{{ t('daybarWeatherLoading') }}</span>
              </template>
              <template v-else-if="!weather && weatherError">
                <span class="db-weather-off">{{ t('daybarWeatherFailed') }}</span>
                <button class="db-text-btn" @click="loadWeather(true)">{{ t('daybarWeatherRetry') }}</button>
              </template>
              <template v-else-if="weather">
                <component :is="weatherIcon" :size="22" class="db-w-ic" />
                <div class="db-w-main">
                  <div class="db-w-line">
                    <span class="db-w-temp">{{ Math.round(weather.temp) }}°</span>
                    <span class="db-w-kind">{{ weatherKind }}</span>
                    <span v-if="weather.city" class="db-w-city">{{ weather.city }}</span>
                  </div>
                  <div class="db-w-meta">
                    <span>{{ t('daybarWeatherHighLow', { max: Math.round(weather.max), min: Math.round(weather.min) }) }}</span>
                    <span>{{ t('daybarWeatherHumidity', { n: Math.round(weather.humidity) }) }}</span>
                    <span>{{ t('daybarWeatherWind', { n: Math.round(weather.wind) }) }}</span>
                    <span class="db-w-time">{{ t('daybarWeatherUpdated', { time: weatherTime }) }}</span>
                  </div>
                </div>
              </template>
            </div>

          </div>
        </section>

        <!-- 概览带下移：问候搬走后第一列会整块空掉，所以补一行卡头（状态点 +
             更新时间 + 跳转提示），并把四个读数扩成六个。新增两项取本页已有的
             items / gitRepos，不新增请求、不新增后端方法。 -->
        <section class="band">
          <div class="band-head">
            <span class="band-head-dot" :class="{ alert: overviewAlert }"></span>
            <span class="band-head-title">{{ t('dash_overview_title') }}</span>
            <span class="band-head-hint">{{ t('dash_overview_hint') }}</span>
            <span class="band-head-meta">{{ t('dash_overview_updated', { time: lastRefreshText }) }}</span>
          </div>
          <div class="band-grid">
            <button class="readout" @click="go('todo')">
              <span class="r-label"><ListTodo :size="12" />{{ t('dash_stat_todo') }}</span>
              <span class="r-num">{{ openTodos.length }}</span>
              <span class="r-foot" :class="{ danger: overdue > 0 }">
                <template v-if="overdue">{{ t('dash_stat_overdue', { n: overdue }) }}</template>
                <template v-else>{{ t('dash_stat_due_today', { n: todayDue }) }}</template>
              </span>
            </button>

            <button class="readout" :class="{ alert: downMonitors.length > 0 }" @click="go('monitor')">
              <span class="r-label"><Activity :size="12" />{{ t('dash_stat_monitor') }}</span>
              <span class="r-num">{{ downMonitors.length }}</span>
              <span class="r-foot">{{ t('dash_stat_monitor_foot', { n: enabledMonitors.length }) }}</span>
            </button>

            <button class="readout" @click="go('environments')">
              <span class="r-label"><Server :size="12" />{{ t('dash_stat_env') }}</span>
              <span class="r-num">{{ residentServices.length }}</span>
              <span class="r-foot">{{ t('dash_stat_env_foot', { n: installedRuntimes.length }) }}</span>
            </button>

            <div class="readout static">
              <span class="r-label"><Plug :size="12" />{{ t('dash_stat_port') }}</span>
              <span class="r-num">{{ ports.length }}</span>
              <span class="r-foot">{{ t('dash_stat_port_foot') }}</span>
            </div>

            <button class="readout" @click="go('workspace')">
              <span class="r-label"><FolderOpen :size="12" />{{ t('dash_stat_items') }}</span>
              <span class="r-num">{{ items.length }}</span>
              <span class="r-foot">{{ t('dash_stat_items_foot') }}</span>
            </button>

            <button class="readout" @click="go('git')">
              <span class="r-label"><FolderGit2 :size="12" />{{ t('dash_git') }}</span>
              <span class="r-num">{{ gitRepos.length }}</span>
              <span class="r-foot">{{ t('dash_git_dirty', { n: dirtyRepos }) }}</span>
            </button>
          </div>
        </section>

        <!-- 设置浮层：Teleport 到 body + fixed 锚定齿轮。.qd-page-body 是 overflow-y:auto，
             绝对定位会被内容盒裁掉；留在 .dash 里做流式块又会把下面六张卡片推下去。 -->
        <Teleport to="body">
          <div v-if="showDaySettings" ref="daySettingsEl" class="db-settings" :style="daySettingsPos">
            <div class="db-settings-head">
              <span>{{ t('daybarSettingsTitle') }}</span>
              <button class="db-icon-btn" @click="closeDaySettings"><X :size="13" /></button>
            </div>
            <label class="field toggle-field">
              <span class="field-label">{{ t('daybarShowYiJi') }}</span>
              <button type="button" class="toggle-btn" :class="{ active: dayPrefs.showYiJi }" @click="toggleYiJi">
                <span class="toggle-knob"></span>
              </button>
            </label>
            <span class="db-pref-hint">{{ t('daybarYiJiHint') }}</span>
            <label class="field toggle-field">
              <span class="field-label">{{ t('daybarShowWeather') }}</span>
              <button type="button" class="toggle-btn" :class="{ active: weatherCfg.enabled }" @click="toggleWeather">
                <span class="toggle-knob"></span>
              </button>
            </label>
            <div class="field">
              <span class="field-label">{{ t('daybarCity') }}</span>
              <div class="db-city-row">
                <input v-model="cityQuery" class="field-input" :placeholder="t('daybarCityPlaceholder')"
                       spellcheck="false" @keydown.enter.prevent="doSearchCity" />
                <button class="btn btn-primary" :disabled="cityBusy" @click="doSearchCity">{{ t('daybarSearch') }}</button>
              </div>
            </div>
            <div v-if="cityBusy" class="result-hint">{{ t('daybarSearching') }}</div>
            <div v-else-if="citySearched && !cityHits.length" class="result-hint">{{ t('daybarNoResult') }}</div>
            <div v-if="cityHits.length" class="db-city-hits">
              <button v-for="(c, i) in cityHits" :key="i" class="db-city-hit" @click="pickCity(c)">
                <MapPin :size="11" />
                <span>{{ c.name }}</span>
                <small>{{ [c.region, c.country].filter(Boolean).join(' · ') }}</small>
              </button>
            </div>
            <div class="result-hint">
              {{ weatherCfg.city ? t('daybarCurrentCity', { city: weatherCfg.city }) : t('daybarNoCity') }}
            </div>
            <div class="db-settings-foot">
              <span>{{ t('daybarWeatherHint') }}</span>
              <button class="btn btn-secondary" @click="closeDaySettings">{{ t('daybarClose') }}</button>
            </div>
          </div>
        </Teleport>

        <!-- 邮箱账号浮层：与日期/天气共用 .db-settings 外壳与定位算法。
             授权码输入框永远不回填，且从不进入任何日志或 DOM 常量。 -->
        <Teleport to="body">
          <div v-if="showMailSettings" ref="mailSettingsEl" class="db-settings" :style="mailSettingsPos">
            <div class="db-settings-head">
              <span>{{ t('mailSettingsTitle') }}</span>
              <button class="db-icon-btn" @click="closeMailSettings"><X :size="13" /></button>
            </div>
            <label class="field toggle-field">
              <span class="field-label">{{ t('mailEnable') }}</span>
              <button type="button" class="toggle-btn" :class="{ active: mailForm.enabled }"
                      @click="mailForm.enabled = !mailForm.enabled"><span class="toggle-knob"></span></button>
            </label>
            <div class="field">
              <span class="field-label">{{ t('mailEmail') }}</span>
              <input v-model="mailForm.email" class="field-input" type="email" spellcheck="false"
                     :placeholder="t('mailEmailPlaceholder')" />
            </div>
            <div class="field">
              <span class="field-label">{{ t('mailHost') }}</span>
              <div class="db-city-row">
                <input v-model="mailForm.host" class="field-input" spellcheck="false"
                       :placeholder="t('mailHostPlaceholder')" />
                <input v-model.number="mailForm.port" class="field-input qc-port" type="number"
                       min="1" max="65535" :title="t('mailPort')" />
              </div>
            </div>
            <div class="field">
              <span class="field-label">{{ t('mailPassword') }}</span>
              <input v-model="mailForm.password" class="field-input" type="password"
                     autocomplete="new-password" :placeholder="t('mailPasswordPlaceholder')" />
            </div>
            <span class="db-pref-hint">{{ t('mailPasswordHint') }}</span>
            <div v-if="mailTestMsg" class="result-hint" :class="mailTestOk ? 'qc-ok' : 'qc-error'">{{ mailTestMsg }}</div>
            <div class="db-settings-foot">
              <button class="btn btn-secondary" :disabled="mailTestBusy" @click="testMailConfig()">
                {{ mailTestBusy ? t('mailTesting') : t('mailTest') }}
              </button>
              <button class="btn btn-primary" @click="saveMailConfig()">{{ t('mailSave') }}</button>
            </div>
          </div>
        </Teleport>

        <!-- 卡片网格：6 列坐标系（宽屏三列视觉、中屏两列、窄屏一列）。
             原先固定两列 + 1180 上限，窗口一最大化就变成中间一条窄带、两侧大片留白，
             看着像没加载完。改成 span 拼接后卡片填满宽度，同一行的卡片等高。
             卡片顺序按「一天里会看几次」排：入口（最近使用 / 置物架）→ 取用（验证码）
             → 状态（端口 / 健康 / 常驻 / Git）→ 回顾（AI 用量）。 -->
          <div class="cards">

            <!-- 最近使用排第一：这一页被点得最多的是「把我昨天在弄的东西打开」，
                 磁贴等分铺满半行（与置物架各占一半），比竖列表少一次扫读。 -->
            <section class="card span-6">
              <header class="card-head">
                <FolderOpen :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_recent') }}</span>
                <button class="card-link" @click="go('workspace')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.item" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!items.length" class="empty">{{ t('dash_recent_empty') }}</p>
              <div v-else class="tiles">
                <button v-for="it in items" :key="it.id" class="tile"
                        :title="it.value" @click="openItem(it)">
                  <!-- 真图标优先：应用/网页条目的 icon 是后端提取的 data URL（与 ItemList 同一份），
                       没有 icon 的才回落到类型图标。磁贴里常驻一个文件夹图标，会让三种完全不同的
                       条目看起来是同一类东西。 -->
                  <span class="tile-ic" :class="{ 'has-icon': !!it.icon }">
                    <img v-if="it.icon" :src="it.icon" class="tile-ic-img" alt="" />
                    <TypeIcon v-else :type="it.type" :size="14" />
                  </span>
                  <span class="tile-name">{{ it.name }}</span>
                  <span class="tile-type mono">{{ it.type }}</span>
                </button>
              </div>
            </section>

            <!-- 置物架：手边要用的文件/目录（拖进来即存路径）。与最近使用同为入口型，
                 一个按「用过」排、一个按「我放这儿」排，所以并排占据第一行。 -->
            <section class="card span-3">
              <header class="card-head">
                <Layers :size="13" class="t-ic" />
                <span class="card-title">{{ t('shelfTitle') }}</span>
                <!-- 一个「添加」按钮 + 两项菜单：系统对话框不能同时选文件与选目录，
                     但「先问一句」不必占两个常驻链接的宽度。 -->
                <div class="qc-actions">
                  <button ref="shelfAddEl" class="card-link" @click="toggleShelfMenu()">
                    <Plus :size="11" />{{ t('shelfAdd') }}
                  </button>
                </div>
              </header>
              <!-- 菜单本身 Teleport 到 body：.qd-page-body 是 overflow-y:auto，留在卡里会被裁 -->
              <Teleport to="body">
                <div v-if="showShelfMenu" ref="shelfMenuEl" class="db-menu" :style="shelfMenuPos">
                  <button class="db-menu-item" @click="addShelf(false)">
                    <File :size="12" />{{ t('shelfAddFile') }}
                  </button>
                  <button class="db-menu-item" @click="addShelf(true)">
                    <Folder :size="12" />{{ t('shelfAddDir') }}
                  </button>
                </div>
              </Teleport>
              <!-- id + data-file-drop-target 是 Wails WindowFilesDropped 的投递目标：
                   宿主事件只在拖到这个元素上时把绝对路径送进置物架。 -->
              <div id="qd-shelf-drop" data-file-drop-target class="qc-body qc-drop"
                   :class="{ over: shelfDragOver }"
                   @dragenter="onShelfDrag" @dragover="onShelfDrag"
                   @dragleave="onShelfLeave" @drop="onShelfLeave">
                <p v-if="!shelfItems.length" class="empty qc-empty">{{ t('shelfEmpty') }}</p>
                <ul v-else class="qc-list">
                  <li v-for="it in shelfItems" :key="it.id" class="qc-row" :class="{ expired: !it.exists }">
                    <span class="qc-file-ic"><Folder v-if="it.kind === 'dir'" :size="14" /><File v-else :size="14" /></span>
                    <div class="qc-meta">
                      <span class="qc-name" :title="it.path">{{ it.name }}</span>
                      <span class="qc-sub" :title="it.path">{{ it.exists ? it.path : t('shelfMissing') }}</span>
                    </div>
                    <button class="db-icon-btn" :title="t('shelfOpen')" @click="openShelfItem(it)">
                      <FolderOpen :size="13" />
                    </button>
                    <button class="db-icon-btn" :title="t('shelfReveal')" @click="revealShelfItem(it)">
                      <MapPin :size="13" />
                    </button>
                    <button class="db-icon-btn" :title="t('shelfRemove')" @click="removeShelfItem(it)"><X :size="13" /></button>
                  </li>
                </ul>
              </div>
            </section>

            <!-- 待办：到期分布条用颜色直接回答「紧不紧张」，不必逐行读日期 -->
            <section class="card span-3">
              <header class="card-head">
                <ListTodo :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_today_todo') }}</span>
                <span v-if="todoList.length" class="card-count">{{ todoList.length }}</span>
                <button class="card-link" @click="go('todo')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.todo" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!todoList.length" class="empty">{{ t('dash_todo_empty') }}</p>
              <template v-else>
                <div v-if="todoDist.length" class="todo-dist" :title="t('dash_dist_title')">
                  <span v-for="d in todoDist" :key="d.k"
                        :class="['seg', d.cls]" :style="{ flexGrow: d.n }"
                        :title="d.k === 'overdue' ? t('dash_stat_overdue', { n: d.n }) : d.k === 'today' ? t('dash_stat_due_today', { n: d.n }) : t('dash_dist_later', { n: d.n })"></span>
                </div>
                <ul class="rows">
                  <li v-for="x in todoList" :key="x.id" class="row">
                    <button class="check" :title="t('dash_mark_done')" @click="toggleTodo(x)">
                      <Check :size="10" />
                    </button>
                    <span class="row-title" :title="x.title">{{ x.title }}</span>
                    <span v-if="x.priority === 'high'" class="pri" :title="t('dash_pri_high')"></span>
                    <span v-if="dueText(x)" :class="['due', { warn: isUrgent(x) }]">{{ dueText(x) }}</span>
                  </li>
                </ul>
              </template>
            </section>

            <!-- 验证码：有时效、拿到就复制走，属于「扫一眼就处理」的卡，
                 所以紧跟两个入口卡，排在需要细读的状态卡之前。 -->
            <section class="card span-3">
              <header class="card-head">
                <Mail :size="13" class="t-ic" />
                <span class="card-title">{{ t('mailTitle') }}</span>
                <span v-if="mailCfg.enabled" class="qc-dot" :class="mailCfg.connected ? 'up' : 'down'"></span>
                <div class="qc-actions">
                  <button v-if="mailCfg.enabled" class="db-icon-btn" :title="t('dash_refresh')"
                          :disabled="mailBusy" @click="refreshMailCodes()">
                    <RefreshCw :size="13" :class="{ spin: mailBusy }" />
                  </button>
                  <button ref="mailGearEl" class="db-icon-btn" :title="t('mailSettings')" @click="openMailSettings()">
                    <Settings2 :size="13" />
                  </button>
                </div>
              </header>
              <div class="qc-body">
                <p v-if="!mailCfg.enabled" class="empty qc-empty">
                  {{ t('mailDisabled') }}
                  <button class="qc-link" @click="openMailSettings()">{{ t('mailSetup') }}</button>
                </p>
                <p v-else-if="mailError" class="empty qc-error">
                  {{ mailError }}
                  <button class="qc-link" @click="refreshMailCodes()">{{ t('mailRetry') }}</button>
                </p>
                <p v-else-if="!mailCodes.length" class="empty qc-empty">{{ t('mailEmpty') }}</p>
                <ul v-else class="qc-list">
                  <li v-for="m in mailCodes.slice(0, 3)" :key="m.id" class="qc-row"
                      :class="{ expired: isMailExpired(m.received) }">
                    <span class="qc-code mono">{{ m.code }}</span>
                    <div class="qc-meta">
                      <span class="qc-name" :title="m.subject">{{ m.from || m.account }}</span>
                      <span class="qc-sub">{{ m.subject }} · {{ mailTimeText(m.received) }}</span>
                    </div>
                    <button class="db-icon-btn" :title="t('mailCopy')" @click="copyCode(m)">
                      <Check v-if="mailCopied === m.id" :size="13" /><Copy v-else :size="13" />
                    </button>
                    <button class="db-icon-btn" :title="t('mailDismiss')" @click="dismissCode(m.id)"><X :size="13" /></button>
                  </li>
                </ul>
              </div>
            </section>

            <!-- wide-sm：端口在 2 列布局里独占一行。span-2 的卡共 5 张（奇数），
                 不留这张全宽的话，最后一张会在 2 列里落单、右边空半格。 -->
            <section class="card span-3">
              <header class="card-head">
                <Plug :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_ports') }}</span>
                <button class="card-link" @click="go('environments', 'ports')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.port" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!portList.length" class="empty">{{ t('dash_ports_empty') }}</p>
              <ul v-else class="rows">
                <li v-for="p in portList" :key="`${p.protocol}-${p.port}`" class="row clickable"
                    :title="p.path" @click="go('environments', 'ports')">
                  <span class="port">{{ p.port }}</span>
                  <span class="row-title mono" :title="p.path">{{ p.process || t('dash_unknown_proc') }}</span>
                  <span v-if="p.self" class="due">{{ t('dash_self') }}</span>
                </li>
              </ul>
            </section>

            <!-- 常驻服务：仪表带给了数字，这里给「具体是哪几个」——
                 数字只能说明「有几个在跑」，排查时要知道是哪个在跑。
                 除了 env 的常驻运行时，Everything 与 DSH 这两个自带常驻进程也一并列出 -->
            <section class="card span-3">
              <header class="card-head">
                <Server :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_env_resident') }}</span>
                <span v-if="residentList.length" class="card-count">{{ residentList.length }}</span>
                <button class="card-link" @click="go('environments')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="!residentList.length" class="empty">
                {{ failed.env ? t('dash_load_failed') : t('dash_env_empty') }}
              </p>
              <ul v-else class="rows">
                <li v-for="r in residentList" :key="r.id" class="row clickable" @click="go(r.page, r.target)">
                  <span class="dot up"></span>
                  <span class="row-title">{{ r.name }}</span>
                  <span class="row-target">{{ r.meta }}</span>
                </li>
              </ul>
            </section>

            <!-- Git：有没有忘提交的、落后没拉的。属于「开工前确认」的卡，
                 排在这些状态卡的末尾、报表卡之前。 -->
            <section class="card span-3">
              <header class="card-head">
                <FolderGit2 :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_git') }}</span>
                <span v-if="gitBusyCount" class="card-count bad">{{ t('dash_git_dirty', { n: gitBusyCount }) }}</span>
                <button class="card-link" @click="go('git')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.git" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!gitList.length" class="empty">{{ t('dash_git_empty') }}</p>
              <ul v-else class="rows">
                <li v-for="r in gitList" :key="r.path" class="row clickable" :title="r.error || r.path"
                    @click="go('git', r.path)">
                  <FolderGit2 :size="12" class="row-icon" />
                  <span class="row-title">{{ r.name }}</span>
                  <span v-if="r.changes" class="due warn">{{ t('git_changes', { n: r.changes }) }}</span>
                  <span v-if="r.ahead" class="git-ab">↑{{ r.ahead }}</span>
                  <span v-if="r.behind" class="git-ab down">↓{{ r.behind }}</span>
                  <span v-if="!r.changes && !r.ahead && !r.behind && !r.error" class="git-ok">
                    <Check :size="11" />
                  </span>
                  <span v-if="r.branch" class="row-target">{{ r.branch }}</span>
                </li>
              </ul>
            </section>
            <!-- 健康卡：顶部一条 24h 全局热力条（整段时间里谁挂过一眼可见），
                 每行再给单项的 24h 条与可用率，列表就有了时间维度。
                 与 AI 卡配对占一行；AI 卡不显示（没配模型）时它独占整行，
                 否则右边会空半格。 -->
            <section :class="['card', aiConfigured ? 'span-3' : 'span-6']">
              <header class="card-head">
                <Activity :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_health') }}</span>
                <span v-if="downMonitors.length" class="card-count bad">{{ t('dash_down_n', { n: downMonitors.length }) }}</span>
                <button class="card-link" @click="go('monitor')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="failed.monitor" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!monitorList.length" class="empty">{{ t('dash_health_empty') }}</p>
              <template v-else>
                <div class="ribbon-wrap" :title="t('dash_uptime_24h')">
                  <div class="ribbon-meta">
                    <span class="ribbon-label">{{ t('dash_uptime_24h') }}</span>
                    <span :class="['ribbon-pct', 'mono', { bad: globalUptime >= 0 && globalUptime < 99 }]">{{ globalUptimeText }}</span>
                  </div>
                  <div class="ribbon">
                    <span v-for="(b, i) in globalRibbon" :key="i" :class="['cell', b]"></span>
                  </div>
                </div>
                <ul class="rows">
                  <li v-for="m in monitorList" :key="m.id" class="row clickable" @click="go('monitor')"
                      :title="(m.kind === 'http' ? m.url : m.target) + (upPct(m.id) === '—' ? '' : ' · ' + upPct(m.id))">
                    <span :class="['dot', m.lastStatus || 'unknown']"></span>
                    <span class="row-title" >{{ m.name }}</span>
                    <span v-if="m.lastStatus === 'down'" class="due warn">{{ t('dash_down') }}</span>
                    <span v-else-if="!m.lastStatus" class="due">{{ t('dash_never_checked') }}</span>
                    <span class="ribbon mini">
                      <span v-for="(b, i) in bucketsOf(m.id)" :key="i" :class="['cell', b]"></span>
                    </span>
                    <span class="up-pct mono">{{ upPct(m.id) }}</span>
                  </li>
                </ul>
              </template>
            </section>

            <!-- AI 用量：今日 / 七日两个钱数 + 七日柱 + 模型占比。
                 纯回顾型，放最后一行；没配模型时整张卡不出现——一张恒为 ¥0.00 的卡
                 对没在用 AI 的人是噪音。钱是估算（内置价目表线性折算），口径在设置页。 -->
            <section v-if="aiConfigured" class="card span-3">
              <header class="card-head">
                <Coins :size="13" class="t-ic" />
                <span class="card-title">{{ t('dash_ai_usage') }}</span>
                <button class="card-link" @click="emit('open-settings', 'ai:usage')">
                  {{ t('dash_view_all') }}<ChevronRight :size="11" />
                </button>
              </header>
              <p v-if="aiUsageFailed" class="empty">{{ t('dash_load_failed') }}</p>
              <p v-else-if="!aiWeekCalls" class="empty">{{ t('dash_ai_empty') }}</p>
              <div v-else class="ai-body">
                <div class="ai-side">
                  <div class="ai-stat">
                    <span class="ai-label">{{ t('dash_ai_today') }}</span>
                    <span class="ai-num mono">{{ t('dash_ai_cost', { v: moneyText(aiToday?.estimatedCost ?? 0) }) }}</span>
                    <span class="ai-sub">{{ t('dash_ai_calls', { calls: aiTodayCalls, tokens: tokensText(aiTodayTokens) }) }}</span>
                  </div>
                  <div class="ai-stat">
                    <span class="ai-label">{{ t('dash_ai_window', { n: AI_USAGE_DAYS }) }}</span>
                    <span class="ai-num mono">{{ t('dash_ai_cost', { v: moneyText(aiWeekCost) }) }}</span>
                    <span class="ai-sub">{{ t('dash_ai_week_calls', { n: aiWeekCalls }) }}</span>
                  </div>
                </div>
                <div class="ai-chart">
                  <!-- 柱高是运行时按当日最大值归一的百分数，只能内联（R7 运行时值例外）。
                       当天没有调用时给一条 2px 的底色，而不是 Math.max 保底——
                       保底会让空日子看起来也"用过一点"，那正是这张图不该骗人的地方。 -->
                  <div class="ai-bars" :title="t('dash_ai_bars_title')">
                    <span v-for="b in aiBars" :key="b.key" :class="['ai-bar', { empty: !b.tokens }]"
                          :style="{ height: b.tokens ? Math.max(6, b.pct) + '%' : '2px' }"
                          :title="`${b.key} · ${tokensText(b.tokens)}`"></span>
                  </div>
                  <ul class="ai-models">
                    <li v-for="m in aiTopModels" :key="m.name" class="ai-model">
                      <span class="ai-model-name" :title="m.name">{{ m.name }}</span>
                      <span class="ai-model-pct mono">{{ m.pct }}%</span>
                    </li>
                  </ul>
                </div>
              </div>
            </section>
          </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 内容最宽 1680：再宽的话六行清单会被拉成一整条，眼睛要横跨半屏才读完一行；
   1680 在 1920 的屏幕上只留 120 的边距，不会再出现「中间一条窄带、两边全是空白」。 */
.dash {
  max-width: 1680px; margin: 0 auto;
  display: flex; flex-direction: column; gap: var(--space-5);
}

/* ---- 概览带：卡头 + 六读数共用一根线 ----
 * 问候搬到上面的「今日」卡后，原第一列会整块空掉；改成一条窄卡头 + 六等分网格，
 * 既补上留白，也把读数从四个扩到六个（新增两项取本页已有数据，不发新请求）。 */
.band {
  display: flex; flex-direction: column;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  /* 抬起来的板：否则读数和页面底色几乎同层 */
  box-shadow: var(--shadow-card);
  overflow: hidden;
}
.band-head {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 7px var(--space-6);
  border-bottom: 1px solid var(--color-border);
}
.band-head-dot {
  width: 6px; height: 6px; flex-shrink: 0;
  border-radius: 50%; background: var(--color-success);
}
.band-head-dot.alert { background: var(--color-danger); }
.band-head-title { font-size: 12px; font-weight: 600; color: var(--color-text-secondary); }
.band-head-hint { font-size: 10px; color: var(--color-text-disabled); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.band-head-meta { margin-left: auto; flex-shrink: 0; font-size: 10px; color: var(--color-text-disabled); white-space: nowrap; }
.band-grid { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); }
.band-grid > * + * { border-left: 1px solid var(--color-border); }

/* 读数三行全部左对齐：数字是主角，foot 只是注脚，右对齐的对角线留白反而显得散 */
.readout {
  display: flex; flex-direction: column; gap: 2px; min-width: 0;
  padding: 10px var(--space-5) 11px;
  background: none; border: 0; font: inherit; text-align: left; color: inherit;
  cursor: pointer; transition: background-color var(--transition-fast);
}
.readout:hover { background: var(--color-bg-hover); }
.readout.static { cursor: default; }
.readout.static:hover { background: none; }
.r-label {
  display: flex; align-items: center; gap: 5px;
  font-size: 11px; color: var(--color-text-muted); white-space: nowrap;
}
/* 大号读数：mono 保证四位数字不跳宽，字距收一点让它更像"仪表读数"而不是代码 */
.r-num {
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 24px; font-weight: 600; letter-spacing: -0.02em;
  line-height: 1.2; color: var(--color-text-primary);
}
.r-foot {
  font-size: 11px; color: var(--color-text-disabled);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.r-foot.danger { color: var(--color-danger); }
/* 异常读数只染数字——一排四个数字里，颜色比符号更快被看见 */
.readout.alert .r-num { color: var(--color-danger); }

/* ---- 卡片网格 ----
 * 6 列基底 + 跨列：健康卡 4 格、其余 2 格，重要内容拿大位，不再是六张同构卡片平铺。
 * align-items 保持默认 stretch —— 同一行的卡片等高，底边对齐，比参差的下边缘好读。
 */
.cards {
  display: grid; gap: var(--space-5);
  grid-template-columns: repeat(6, minmax(0, 1fr));
}
/* 只有两档宽度，这样任何断点下的节奏都一样：
   span-3 = 半行（6 列里 3 列 / 2 列里 1 列）、span-6 = 整行。
   三档以上（2/3/4/6）混排时，2 列布局里会出现「半格落单」——奇数张 span-2 必然留空。 */
.span-3 { grid-column: span 3; }
.span-6 { grid-column: 1 / -1; }

.card {
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-card);
  overflow: hidden;
}
.card-head {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 9px 14px;
  border-bottom: 1px solid var(--color-border);
}
.t-ic { color: var(--color-text-muted); flex-shrink: 0; }
.card-title { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }
.card-count {
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 10px; line-height: 15px; padding: 0 6px; border-radius: var(--radius-full);
  color: var(--color-text-muted); background: var(--color-bg-tertiary);
}
.card-count.bad { color: var(--color-danger); background: var(--color-danger-bg); }
.card-link {
  display: inline-flex; align-items: center; gap: 1px; margin-left: auto;
  background: none; border: 0; padding: 0; cursor: pointer;
  font: inherit; font-size: 11px; color: var(--color-text-muted);
  transition: color var(--transition-fast);
}
.card-link:hover { color: var(--color-accent); }

/* 卡片内的空态：不居中反而是对的（列表为空时上方还有热力条/分布条），
   但留白要够，否则一行灰字像被漏掉的说明。 */
.empty { margin: 0; padding: 22px 14px; font-size: var(--text-sm); line-height: 1.6; color: var(--color-text-muted); }

/* ---- 24h 可用率热力条 ----
 * 一格 = 30 分钟：绿=全程在线、红=挂过、暗=没有数据。放在卡头之下、列表之上，
 * 「整体一眼 → 逐项细看」的阅读顺序由它领起。 */
.ribbon-wrap {
  padding: var(--space-3) 14px var(--space-4);
  border-bottom: 1px solid var(--color-border);
}
.ribbon-meta {
  display: flex; align-items: baseline; gap: var(--space-3);
  margin-bottom: 6px;
}
.ribbon-label { font-size: 11px; color: var(--color-text-muted); }
.ribbon-pct {
  margin-left: auto; font-size: 13px; font-weight: 600;
  font-variant-numeric: tabular-nums; color: var(--color-success);
}
.ribbon-pct.bad { color: var(--color-danger); }

.ribbon { display: flex; gap: 2px; height: 16px; }
.cell { flex: 1 1 0; min-width: 0; border-radius: 1.5px; background: var(--color-bg-tertiary); }
/* 整条全绿会炸：up 用 55% 透明度压成「在线但不刺眼」，down 保持饱和 */
.cell.up { background: var(--color-success); opacity: .5; }
.cell.down { background: var(--color-danger); opacity: .85; }

/* 行内 mini 条：宽度固定，与名称/徽章同行；格子数同全局条，密度一致才像一张图 */
.ribbon.mini { width: 132px; flex-shrink: 0; height: 12px; }
.up-pct {
  width: 52px; flex-shrink: 0; text-align: right;
  font-size: 11px; font-variant-numeric: tabular-nums;
  color: var(--color-text-secondary);
}

/* ---- 待办到期分布条：三段各按数量占比，颜色即语义 ---- */
.todo-dist { display: flex; gap: 2px; height: 5px; margin: var(--space-3) 14px 0; }
.seg { min-width: 6px; border-radius: 2px; }
.seg.danger { background: var(--color-danger); }
.seg.warn { background: var(--color-warning); }
.seg.ok { background: var(--color-accent); opacity: .55; }

/* ---- 验证码 / 置物架：两张 span-3 的「取用型」卡片 ----
 * 与上面的读数/清单卡共用 .card/.card-head/.empty/.db-icon-btn，
 * 这里只补它们独有的行内结构，不再造第二套页头或按钮。 */
.qc-actions { display: flex; align-items: center; gap: 2px; margin-left: auto; }
/* .card-link 自带 margin-left:auto（单个链接靠右），放进已经 auto 的容器里会多推一次 */
.qc-actions .card-link { margin-left: 0; }

/* ---- 卡头下拉菜单（置物架的「添加」） ----
 * Teleport 到 body + fixed，与设置浮层同一套定位（placeFloating）。
 * 只有两项且都是短词，所以不做分组、不做勾选，点完即关。 */
.db-menu {
  position: fixed; z-index: 660; min-width: 148px; padding: 4px;
  background: var(--color-surface-elevated);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  display: flex; flex-direction: column; gap: 1px;
}
.db-menu-item {
  display: flex; align-items: center; gap: var(--space-3);
  width: 100%; padding: 6px 8px;
  background: none; border: none; border-radius: var(--radius-sm);
  color: var(--color-text-secondary);
  font-size: 12px; text-align: left; cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}
.db-menu-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.qc-body { padding: 0 var(--space-5) var(--space-3); min-height: 106px; }
.qc-empty {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: var(--space-2); min-height: 100px;
}
.qc-link {
  background: none; border: 0; padding: 0; cursor: pointer;
  font: inherit; color: var(--color-accent);
}
.qc-link:hover { text-decoration: underline; }
.qc-error { color: var(--color-danger); }
.qc-ok { color: var(--color-success); }
.qc-list { list-style: none; margin: 0; padding: 0; }
.qc-row {
  display: flex; align-items: center; gap: var(--space-2);
  min-height: 46px; padding: var(--space-2) 0;
  border-top: 1px solid var(--color-border);
}
.qc-row:first-child { border-top: 0; }
.qc-row.expired { opacity: .5; }
.qc-code {
  flex-shrink: 0; min-width: 78px; font-size: 16px; font-weight: 600;
  letter-spacing: .06em; color: var(--color-accent);
}
.qc-file-ic {
  display: grid; place-items: center; flex-shrink: 0;
  width: 24px; height: 24px; color: var(--color-text-muted);
}
.qc-meta { display: flex; flex-direction: column; gap: 2px; flex: 1; min-width: 0; }
.qc-name {
  font-size: 12px; color: var(--color-text-primary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.qc-sub {
  font-size: 10px; color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.qc-dot { width: 6px; height: 6px; border-radius: var(--radius-full); flex-shrink: 0; }
.qc-dot.up { background: var(--color-success); }
.qc-dot.down { background: var(--color-danger); }
.qc-drop.over, .qc-drop.file-drop-target-active { background: var(--color-accent-bg); }
.qc-port { width: 76px; flex-shrink: 0; }

/* ---- AI 用量 ----
 * 左「今日 / 七日」两个钱数、右柱状条与模型占比：钱是结论，柱与占比是佐证，
 * 所以左边给固定窄栏（数字不会因为窗口宽度变来变去），右边吃掉剩余宽度。
 *
 * 柱宽必须封顶：flex:1 在千像素宽的卡里会让 7 根柱变成 7 个方块，看着像色块不像图。
 * 封顶后多出来的宽度交给 space-between 摊到间隙上。
 * 模型占比用「换行胶囊」而不是等分栅格——模型名长度能差一倍，栅格一定会出现两项贴合。 */
.ai-body {
  display: flex; align-items: stretch; gap: var(--space-6);
  padding: var(--space-4) var(--space-5);
}
.ai-side {
  display: flex; gap: var(--space-6); flex-shrink: 0;
  padding-right: var(--space-5); border-right: 1px solid var(--color-border);
}
.ai-stat { display: flex; flex-direction: column; gap: 1px; min-width: 84px; }
.ai-label { font-size: 10px; color: var(--color-text-muted); }
.ai-num {
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 22px; font-weight: 600; letter-spacing: -0.02em; line-height: 1.25;
  color: var(--color-text-primary);
}
.ai-sub { font-size: 10px; color: var(--color-text-disabled); white-space: nowrap; }
.ai-chart { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: var(--space-3); }
/* 柱状条：容器给固定高度，柱高用百分比才有参照 */
.ai-bars {
  display: flex; align-items: flex-end; justify-content: space-between; gap: 6px;
  height: 52px; padding-bottom: 1px; border-bottom: 1px solid var(--color-border);
}
.ai-bar {
  flex: 1 1 0; max-width: 40px; border-radius: 2px 2px 0 0;
  background: var(--color-accent); opacity: .55;
  transition: opacity var(--transition-fast);
}
.ai-bar:hover { opacity: 1; }
/* 没有调用的那天：一条贴底的浅色短线，与有量的柱子区分开 */
.ai-bar.empty { background: var(--color-bg-tertiary); opacity: 1; }
/* 模型占比：等分整行。
 * 这里不能用固定列数或按内容自宽——前者遇到名字长短不一会挤到一起，
 * 后者（原先的胶囊流）在 1600px 宽的卡里只排到左边半行，右边一片空白。
 * flex:1 1 0 让每项等分、恰好铺满；名字给 min-width:0 + ellipsis 才不会撑破。 */
.ai-models {
  list-style: none; margin: 0; padding: 0;
  display: flex; flex-wrap: wrap; gap: 6px var(--space-3);
}
/* 基宽 130 起、可换行：半行卡里三个模型会折成两行，宽卡里仍是一行均分 */
.ai-model {
  flex: 1 1 130px; min-width: 0;
  display: inline-flex; align-items: center; gap: 6px;
  padding: 3px 9px; border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); font-size: 11px;
}
.ai-model-name {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-text-secondary);
}
.ai-model-pct { flex-shrink: 0; color: var(--color-text-muted); font-variant-numeric: tabular-nums; }

/* ---- 最近使用磁贴 ----
 * 用等分网格而不是「按内容自宽 + 换行」：后者在宽屏下只排到左边半行，
 * 整张卡右半边空着，看起来像没加载完。auto-fit 让这一行里每个磁贴均分，
 * 项目少时也只是每块变宽，不会留出缺口。 */
.tiles {
  display: grid; gap: var(--space-3);
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  padding: var(--space-4) 14px;
}
.tile {
  display: flex; align-items: center; gap: var(--space-3);
  min-width: 0;
  padding: 8px 14px 8px 10px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  font: inherit; cursor: pointer; text-align: left;
  transition: border-color var(--transition-fast), background-color var(--transition-fast);
}
.tile:hover { border-color: var(--color-accent-border-strong); background: var(--color-bg-hover); }
.tile-ic {
  display: grid; place-items: center; flex-shrink: 0;
  width: 26px; height: 26px; border-radius: var(--radius-xs);
  background: var(--color-accent-bg); color: var(--color-accent);
}
/* 真图标多半自己带底色（应用图标是圆角方块、favicon 是站点色），
   再垫一层浅色底会看起来像贴了两层皮，所以只留透明底。 */
.tile-ic.has-icon { background: transparent; }
.tile-ic-img { width: 20px; height: 20px; object-fit: contain; border-radius: 3px; }
/* 名字吃掉中间的剩余宽度，把类型徽标顶到右端——磁贴变宽后两端对齐才不散 */
.tile-name {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: 12px; font-weight: 500; color: var(--color-text-primary);
}
.tile-type {
  flex-shrink: 0; font-size: 10px; color: var(--color-text-muted);
  background: var(--color-bg-secondary); padding: 1px 6px; border-radius: var(--radius-full);
}

/* ---- 行 ---- */
.rows { list-style: none; margin: 0; padding: var(--space-2); display: flex; flex-direction: column; }
.row {
  display: flex; align-items: center; gap: var(--space-4);
  height: 32px; padding: 0 var(--space-4); border-radius: var(--radius-xs);
  font-size: 12px; min-width: 0;
}
/* WebView2 不支持 :has()，所以「这行可点」只能靠显式 class，不能靠子元素推断 */
.row.clickable { cursor: pointer; }
.row:hover { background: var(--color-bg-hover); }
.row-title {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-text-primary);
}
.row-icon { color: var(--color-text-muted); flex-shrink: 0; }
.row-target {
  max-width: 46%; flex-shrink: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono); font-size: 11px; color: var(--color-text-muted);
}
.mono { font-family: var(--font-mono); }

/* 勾选框：常态只是一圈细线，悬停才浮出「完成」的绿色，避免六行全是花花绿绿的圆点 */
.check {
  display: grid; place-items: center; flex-shrink: 0;
  width: 16px; height: 16px; padding: 0;
  background: none; border: 1.5px solid var(--color-border-light); border-radius: 50%;
  color: transparent; cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast);
}
.row:hover .check { color: var(--color-text-muted); border-color: var(--color-text-muted); }
.row .check:hover {
  color: var(--color-success); border-color: var(--color-success); background: var(--color-success-bg);
}

.dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; background: var(--color-text-disabled); }
.dot.up { background: var(--color-success); }
.dot.down { background: var(--color-danger); box-shadow: 0 0 0 3px var(--color-danger-bg); }

/* 优先级用一个小三角点，比再写一个「高」字省地方，也不与到期标撞色 */
.pri { width: 0; height: 0; flex-shrink: 0; border-left: 3px solid transparent; border-right: 3px solid transparent; border-bottom: 5px solid var(--color-danger); }

.due {
  flex-shrink: 0; font-size: 10px; line-height: 1.6; padding: 0 5px;
  border-radius: var(--radius-xs); color: var(--color-text-muted); background: var(--color-bg-tertiary);
}
.due.warn { color: var(--color-danger); background: var(--color-danger-bg); }

.port {
  width: 44px; flex-shrink: 0;
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 12px; color: var(--color-text-secondary);
}
.mono { font-family: var(--font-mono); }

/* Git 行的 ↑↓ 与干净标记 */
.git-ab { font-family: var(--font-mono); font-size: 11px; color: var(--color-warning); flex-shrink: 0; }
.git-ab.down { color: var(--color-accent); }
.git-ok { display: flex; color: var(--color-success); flex-shrink: 0; }

.spin { animation: dash-spin 900ms linear infinite; }
@keyframes dash-spin { to { transform: rotate(360deg); } }

/* 断点只改列数、不改宽度档：6 列时 span-3 是半行；2 列时它也就是一列（auto）；
 * 单列时一律整行。因为只有 3/6 两档，任何宽度下都是「整行 / 半半」的同一节奏。
 *
 * 铁律：**span 永远不要大于当前列数**。`grid-column: span 3` 落在 2 列网格里不报错，
 * 浏览器会补一条隐式列来容纳它，于是同一行的卡宽度不一致（曾表现为
 * 「全屏能铺满、默认窗口下右边空一块」）。加卡片时先想 2 列布局怎么排。 */
@media (max-width: 1360px) {
  .cards { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .span-3 { grid-column: auto; }   /* 半行：2 列里就是一列 */
  .span-6 { grid-column: 1 / -1; }
  .ribbon.mini { width: 96px; }
}

@media (max-width: 1080px) {
  .cards { grid-template-columns: minmax(0, 1fr); }
  .span-3 { grid-column: 1 / -1; }   /* 单列下没有「半行」，一律整行 */
  /* AI 卡在窄屏横排会被压扁：左栏两个钱数是固定宽，柱子只剩几十像素。
     改成上下叠，分隔线从竖线换横线。 */
  .ai-body { flex-direction: column; gap: var(--space-4); }
  .ai-side {
    padding-right: 0; padding-bottom: var(--space-4);
    border-right: 0; border-bottom: 1px solid var(--color-border);
  }
  .band-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  /* 六读数两两成行：奇数在左列，右边框当列间线；第三格起补行间横线 */
  .band-grid > * + * { border-left: 0; }
  .band-grid > .readout:nth-child(odd) { border-right: 1px solid var(--color-border); }
  .band-grid > .readout:nth-child(n+3) { border-top: 1px solid var(--color-border); }
}

/* ---- 今日卡：头部是问候 + 实时状态，主体靠竖线分三段 ---- */
.daybar {
  position: relative;
  display: flex; flex-direction: column;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-card);
  overflow: hidden;
}
.db-head {
  display: flex; align-items: center; gap: var(--space-4);
  min-height: 42px; padding: var(--space-2) var(--space-6);
  border-bottom: 1px solid var(--color-border);
}
.db-greeting {
  font-size: 18px; font-weight: 600; letter-spacing: -0.01em; line-height: 1.3;
  color: var(--color-text-primary); white-space: nowrap; flex-shrink: 0;
}
.db-note {
  min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: 11px; color: var(--color-text-muted);
}
.db-body {
  display: flex; align-items: center; gap: var(--space-6);
  min-height: 68px; padding: var(--space-4) var(--space-6);
}
.db-date { display: flex; align-items: center; gap: var(--space-4); flex-shrink: 0; }
.db-day {
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 34px; font-weight: 600; line-height: 1; letter-spacing: -0.04em;
  color: var(--color-text-primary);
}
.db-date-meta { display: flex; flex-direction: column; gap: 2px; }
.db-ym { font-size: 12px; color: var(--color-text-secondary); white-space: nowrap; }
.db-week { font-size: 11px; color: var(--color-text-muted); white-space: nowrap; }

.db-lunar {
  display: flex; flex-direction: column; gap: 5px; flex: 1 1 auto; min-width: 0;
  padding-left: var(--space-6); border-left: 1px solid var(--color-border);
}
.db-lunar-main {
  display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap;
  font-size: 12px; color: var(--color-text-primary);
}
.db-ic { color: var(--color-text-muted); flex-shrink: 0; }
.db-title { font-weight: 600; }
.db-lunar-sub { display: flex; gap: var(--space-3); flex-wrap: wrap; }
.db-zodiac, .db-term, .db-festival {
  font-size: 11px; line-height: 1.6; padding: 0 6px;
  border-radius: var(--radius-full);
  color: var(--color-text-muted); background: var(--color-bg-tertiary);
}
.db-festival { color: var(--color-accent); background: var(--color-accent-bg); }

/* 宜忌摘要：一行到底、过长省略，只有点开的全量详情才允许长高（且自带高度上限）。
   宜绿忌红走语义 token，深浅两套主题都随变量走；不写死任何色值。 */
.db-yiji { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.db-yiji-toggle {
  display: flex; align-items: center; gap: var(--space-2); flex-wrap: nowrap;
  width: 100%; min-width: 0; padding: 0;
  background: none; border: 0; border-radius: var(--radius-xs);
  font: inherit; font-size: 11px; color: var(--color-text-secondary);
  text-align: left; cursor: pointer;
}
.db-yiji-toggle:hover { color: var(--color-text-primary); }
.db-yiji-toggle:focus-visible { outline: 1px solid var(--color-accent-border-strong); outline-offset: 2px; }
.yj-tag, .yj-label {
  flex-shrink: 0; padding: 0 5px; border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); font-size: 10px; font-weight: 600; line-height: 16px;
}
.yj-tag.yi, .yj-label.yi { color: var(--color-success); }
.yj-tag.ji, .yj-label.ji { color: var(--color-danger); }
.yj-words {
  flex: 0 1 auto; min-width: 0; white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis;
}
.yj-more { margin-left: 3px; color: var(--color-text-disabled); }
.yj-chev {
  flex-shrink: 0; margin-left: auto; color: var(--color-text-disabled);
  transition: transform var(--transition-fast);
}
.yj-chev.open { transform: rotate(180deg); }
.yj-empty { font-size: 11px; color: var(--color-text-disabled); }

.db-yiji-detail {
  display: flex; flex-direction: column; gap: 4px;
  max-height: 172px; overflow-y: auto;
  padding: var(--space-3);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.yj-row { display: flex; align-items: baseline; gap: var(--space-3); font-size: 11px; line-height: 1.7; }
.yj-full { min-width: 0; color: var(--color-text-secondary); }
.yj-facts {
  display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 2px var(--space-4);
  padding-top: 5px; border-top: 1px solid var(--color-border);
  font-size: 10px; line-height: 1.7; color: var(--color-text-muted);
}
.yj-facts > span { min-width: 0; }
.yj-facts b { margin-right: var(--space-2); font-weight: 500; color: var(--color-text-disabled); }
.yj-disclaimer { font-size: 10px; color: var(--color-text-disabled); }
.db-pref-hint { font-size: 10px; line-height: 1.5; color: var(--color-text-disabled); }

.db-weather {
  display: flex; align-items: center; gap: var(--space-4); min-width: 0;
  margin-left: auto; padding-left: var(--space-6);
  border-left: 1px solid var(--color-border);
}
.db-w-ic { color: var(--color-accent); flex-shrink: 0; }
.db-w-main { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.db-w-line { display: flex; align-items: baseline; gap: var(--space-3); }
.db-w-temp {
  font-family: var(--font-mono); font-variant-numeric: tabular-nums;
  font-size: 20px; font-weight: 600; line-height: 1.2; color: var(--color-text-primary);
}
.db-w-kind { font-size: 12px; color: var(--color-text-secondary); white-space: nowrap; }
.db-w-city {
  font-size: 11px; color: var(--color-text-muted); white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis;
}
.db-w-meta {
  display: flex; flex-wrap: wrap; gap: var(--space-3);
  font-size: 10px; color: var(--color-text-muted); white-space: nowrap;
}
/* 更新时间只是详情行的最后一项，比其余读数更淡，用中点跟前面三个读数分开，
   不再单独挂成一列歪在刷新按钮下面 */
.db-w-time { color: var(--color-text-disabled); }
.db-w-time::before { content: '· '; }
.db-weather-off { font-size: 12px; color: var(--color-text-muted); }

/* 操作按钮默认不画框：两个 1px 硬边框小方块压在文字流上太像表单控件，
   而且刷新按钮 hover / focus 后那圈亮蓝边框会常驻。改为幽灵图标按钮，
   hover 才给柔和底色，键盘 focus 用 outline（不占尺寸、不常驻）。 */
.db-actions { display: flex; align-items: center; gap: 2px; flex-shrink: 0; margin-left: auto; }
.db-icon-btn {
  display: grid; place-items: center; flex-shrink: 0;
  width: 28px; height: 28px; padding: 0;
  background: transparent; border: 0; border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast);
}
.db-icon-btn:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-accent); }
.db-icon-btn:focus-visible { outline: 1px solid var(--color-accent-border-strong); outline-offset: 2px; }
.db-icon-btn:disabled { cursor: default; opacity: .45; }
.db-text-btn {
  background: none; border: 0; padding: 0; cursor: pointer;
  font: inherit; font-size: 11px; color: var(--color-accent);
  transition: color var(--transition-fast);
}
.db-text-btn:hover { color: var(--color-accent-hover); }

/* 设置浮层：Teleport 到 body 后 fixed 锚定齿轮，不占文档流，也不被 .qd-page-body
   的滚动盒裁掉；maxHeight 由 placeDaySettings 按剩余视口高度算，超高时面板自己滚。
   表单复用 styles/settings.css 公共类；z-index 略高于 NotificationCenter(600)。 */
.db-settings {
  position: fixed; z-index: 650;
  display: flex; flex-direction: column; gap: var(--space-3);
  width: 340px; padding: var(--space-5);
  overflow-y: auto;
  background: var(--color-surface-elevated);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}
.db-settings-head {
  display: flex; align-items: center; justify-content: space-between;
  font-size: 13px; font-weight: 600; color: var(--color-text-primary);
}
.db-city-row { display: flex; gap: var(--space-3); }
.db-city-row .field-input { flex: 1; min-width: 0; }
.db-city-hits { display: flex; flex-direction: column; gap: 2px; max-height: 168px; overflow-y: auto; }
.db-city-hit {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 6px var(--space-3); text-align: left;
  background: none; border: 0; border-radius: var(--radius-xs);
  font: inherit; font-size: 12px; color: var(--color-text-primary); cursor: pointer;
}
.db-city-hit:hover { background: var(--color-bg-hover); }
.db-city-hit small { margin-left: auto; font-size: 10px; color: var(--color-text-muted); }
.db-settings-foot {
  display: flex; align-items: center; justify-content: space-between; gap: var(--space-3);
  padding-top: var(--space-2); border-top: 1px solid var(--color-border);
}
.db-settings-foot > span { font-size: 10px; color: var(--color-text-disabled); }

@media (max-width: 1080px) {
  .db-body { flex-wrap: wrap; gap: var(--space-4); }
  .db-note { display: none; } /* 头部变窄，先让位给问候和右侧两枚按钮 */
  .db-lunar { border-left: 0; padding-left: 0; flex-basis: 100%; order: 2; }
  .db-weather { order: 3; width: 100%; margin-left: 0; padding-left: 0; border-left: 0; }
  .yj-facts { grid-template-columns: minmax(0, 1fr); } /* 窄屏详情改单列，别把词条挤断 */
  .db-settings { width: min(340px, calc(100vw - 24px)); }
}
</style>
