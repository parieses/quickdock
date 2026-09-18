<script setup lang="ts">
// 本地开发站点：域名 + 目录 → 由 nginx/caddy 以 HTTPS 提供服务。QuickDock 只负责签发证书、
// 写 hosts 解析并生成站点片段，自己不监听任何端口。后端实现在 internal/sites。
import { ref, onMounted, computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Browser } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import {
  SitesList, SitesCreate, SitesUpdate, SitesDelete,
  SitesSyncHosts, SitesGenConfig, SitesSetModules,
} from '../../bindings/quickdock/services/appservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { CopyText } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { EnvCertStatus, EnvCertInstallRoot } from '../../bindings/quickdock/services/env/environmentservice'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface SiteItem {
  id: string
  name: string
  domain: string
  dir: string
  // 对外服务根相对项目目录的子目录（Laravel=public、Yii2=web）；空 = 直接用项目目录
  docRoot?: string
  enabled: boolean
  // 生成配置时勾选的常用模块与反向代理上游端口（随站点持久化，弹窗打开时回填）
  modules?: string[]
  proxyPort?: number
  // 用户改过并保存的配置片段：backend → 内容；有值时弹窗用它回填，而不是生成器的结果
  configs?: Record<string, string>
}
// 服务后端的可用性：nginx / caddy 都可能是「没装」，此时生成必然失败。
// 弹窗打开时据此自动选一个能用的后端，并把装不了的标灰。
// available 同时决定「能不能创建站点」：站点必须由其中之一提供服务。
interface BackendInfo {
  id: string
  available: boolean
  version: string
  reason: string
  // 该后端的服务当前是否在跑：配置片段只是磁盘上的文件，不跑就没人加载它
  running?: boolean
}
// 站点没有自己的运行态 —— 它是 nginx/caddy 里的一份配置片段，服务由那两者提供。
interface SitesStatus {
  certReady: boolean
  certError: string
  hostsOk: boolean
  hostsError: string
  hostsPath: string
  // 应写入 hosts 的区块文本：自动提权被拒时的兜底，供手工粘贴
  hostsBlock: string
  // 宿主自身是否已是管理员：为 true 却写不进去，说明是安全软件锁定而非权限问题
  elevated: boolean
}

const sites = ref<SiteItem[]>([])
const status = ref<SitesStatus>({ certReady: false, certError: '', hostsOk: false, hostsError: '', hostsPath: '', hostsBlock: '', elevated: false })
const backends = ref<BackendInfo[]>([])
const hasPHP = ref(false)
const caTrusted = ref(false)
const caMessage = ref('')
const busy = ref(false)

// 新增 / 编辑弹窗。两种用途共用一个弹窗：字段完全一致，分成两套必然漂移。
const formOpen = ref(false)
const form = ref({ name: '', domain: '', dir: '', docRoot: '' })
// 编辑中的站点 id（'' = 新增态）
const editingId = ref('')

function openCreate() {
  editingId.value = ''
  form.value = { name: '', domain: '', dir: '', docRoot: '' }
  formOpen.value = true
}

function openEdit(s: SiteItem) {
  editingId.value = s.id
  form.value = { name: s.name, domain: s.domain, dir: s.dir, docRoot: s.docRoot ?? '' }
  formOpen.value = true
}

function closeForm() {
  formOpen.value = false
  editingId.value = ''
  form.value = { name: '', domain: '', dir: '', docRoot: '' }
}

async function load() {
  try {
    const res = unwrap<{ sites: SiteItem[]; status: SitesStatus; backends: BackendInfo[]; hasPHP: boolean }>(await SitesList())
    if (res) {
      sites.value = res.sites ?? []
      status.value = res.status
      backends.value = res.backends ?? []
      hasPHP.value = !!res.hasPHP
    }
  } catch (e) {
    logErr('SitesPanel.load', e)
  }
}

// mkcert 根 CA 是否已信任：未信任时浏览器仍会报证书错误，必须显式提示并可一键安装。
async function loadCert() {
  try {
    const st = unwrap<{ exe: boolean; rootTrusted: boolean; message: string }>(await EnvCertStatus())
    caTrusted.value = !!st?.rootTrusted
    caMessage.value = st?.message ?? ''
  } catch (e) {
    logErr('SitesPanel.loadCert', e)
  }
}

async function pickDir() {
  try {
    const dir = unwrap<string | null>(await PickFolderPath(t('pickDirTitle')))
    if (dir) form.value.dir = dir
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function submit() {
  if (!form.value.dir) { toast.error(t('siteNeedDir')); return }
  if (!form.value.domain) { toast.error(t('siteNeedDomain')); return }
  busy.value = true
  try {
    const name = form.value.name || form.value.domain
    if (editingId.value) {
      unwrap(await SitesUpdate(editingId.value, name, form.value.domain, form.value.dir, true, form.value.docRoot))
      toast.success(t('siteUpdated'))
    } else {
      unwrap(await SitesCreate(name, form.value.domain, form.value.dir, form.value.docRoot))
      toast.success(t('siteCreated'))
    }
    closeForm()
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

async function toggleEnabled(s: SiteItem) {
  try {
    unwrap(await SitesUpdate(s.id, s.name, s.domain, s.dir, !s.enabled, s.docRoot ?? ''))
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function remove(s: SiteItem) {
  try {
    unwrap(await SitesDelete(s.id))
    if (editingId.value === s.id) closeForm()
    await load()
    toast.success(t('deleted'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function syncHosts() {
  try {
    unwrap(await SitesSyncHosts())
    await load()
    toast.success(t('siteHostsSynced'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

// 自动提权被拒（组策略禁 UAC / 标准账号 / 点了「否」）时的兜底：
// 把区块文本放进剪贴板，用户自己用管理员权限的编辑器粘贴即可，不至于卡死。
async function copyHostsBlock() {
  try {
    await CopyText(status.value.hostsBlock)
    toast.success(t('siteHostsCopied'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function trustCA() {
  try {
    unwrap(await EnvCertInstallRoot())
    await loadCert()
    toast.success(t('siteCATrusted'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

// 站点由 nginx/caddy 在 443 上以 HTTPS 提供服务，URL 里不带端口。
// 站点面板没有端口设置 —— 服务器软件监听哪个端口是「环境」页的事。
function siteURL(s: SiteItem): string {
  return 'https://' + s.domain + '/'
}

// 用系统默认浏览器打开站点。WebView 里 <a target="_blank"> 的行为不可控
//（可能被拦、也可能在应用内开新窗口），而用户要的就是「在常用浏览器里看」。
function openSite(s: SiteItem) {
  Browser.OpenURL(siteURL(s)).catch((e: any) => {
    logErr('SitesPanel.openSite', e)
    toast.error(getErrorMessage(e))
  })
}

// 服务后端（nginx / caddy）的安装与运行状态。
// 站点本身没有运行态：它是那两者里的一份配置片段，服务由它们提供。
const serverAvailable = computed(() => backends.value.some(b => b.available))
const serverRunning = computed(() => backends.value.some(b => b.available && b.running))
const serverText = computed(() =>
  backends.value.filter(b => b.available).map(b => `${b.id} ${b.version}`).join(' / '),
)

interface GenResult {
  backend: string
  snippet: string
  fileName: string
  includeLine: string
  needsPhpFpm: boolean
  confPath: string
  targetDir: string
  writtenPath?: string
  // 落盘后运行时是否已热重载（false + 无 reloadError = 服务器当时没在跑，下次启动生效）
  reloaded?: boolean
  // 热重载失败原因（最常见：443 上已有别的监听器）
  reloadError?: string
  // 主配置是用户手写的、里面没有引入片段目录 → 片段不会生效，得手工加一行
  manualImport?: boolean
}

const gen = ref<{
  open: boolean
  site: SiteItem | null
  backend: string
  data: GenResult | null
  // 编辑器内容。它才是最终落盘的东西：模板/模块只负责生成一版初稿，用户改完点保存才算数。
  text: string
  // 编辑器内容是否已作为该后端的自定义片段存在站点上（决定要不要显示「自定义」标记）
  custom: boolean
}>({ open: false, site: null, backend: 'nginx', data: null, text: '', custom: false })

function backendInfo(id: string): BackendInfo | undefined {
  return backends.value.find(b => b.id === id)
}
function backendAvailable(id: string): boolean {
  return !!backendInfo(id)?.available
}
// 默认后端：装了的优先（nginx 在先），都没装就仍给 nginx —— 那种情况下会显示明确的提示条，
// 而不是让用户点了「生成配置」只收到一条转瞬即逝的报错。
function defaultBackend(): string {
  if (backendAvailable('nginx')) return 'nginx'
  if (backendAvailable('caddy')) return 'caddy'
  return 'nginx'
}

// openGen 打开配置生成弹窗：自动选一个能用的后端，站点存过自定义内容就直接回填它。
async function openGen(s: SiteItem, backend = '') {
  // 模块选择是站点属性：打开时从站点回填，用户不必每次重勾。
  genMods.value = (s.modules ?? []).slice()
  genProxyPort.value = s.proxyPort && s.proxyPort > 0 ? s.proxyPort : 3000
  genTemplate.value = ''
  const be = backend || defaultBackend()
  const saved = s.configs?.[be] ?? ''
  gen.value = { open: true, site: s, backend: be, data: null, text: saved, custom: !!saved }
  if (saved) {
    // 已有保存过的自定义内容：只补一次元信息（文件名 / 落盘目录 / include 行），
    // 不把编辑器内容换成生成器的结果 —— 那等于当场抹掉用户改过的东西。
    await refreshGen(false)
  } else {
    await refreshGen()
  }
}

// refreshGen 按当前后端重新生成。
// useSnippet=false 时只刷新元信息、保留编辑器里已有的内容（用于「已有自定义内容」与切换后端）。
// 传 custom='' 只代表「要生成器结果」，后端不会因此清掉站点上已保存的自定义片段。
async function refreshGen(useSnippet = true) {
  const s = gen.value.site
  if (!s) return
  try {
    const res = unwrap<GenResult>(await SitesGenConfig(s.id, gen.value.backend, false, ''))
    gen.value.data = res
    if (useSnippet) {
      gen.value.text = res?.snippet ?? ''
      gen.value.custom = false
    }
  } catch (e: any) {
    gen.value.data = null
    gen.value.text = ''
    toast.error(getErrorMessage(e))
  }
}

// switchBackend 切 nginx / caddy：装不了的后端直接不给切（点了也没内容）。
async function switchBackend(id: string) {
  if (!backendAvailable(id) || gen.value.backend === id) return
  gen.value.backend = id
  const saved = gen.value.site?.configs?.[id] ?? ''
  if (saved) {
    gen.value.text = saved
    gen.value.custom = true
    await refreshGen(false)
  } else {
    await refreshGen()
  }
}

// saveGen 保存：内容作为该后端的自定义片段存到站点上（下次打开回填），同时落盘到 quickdock-sites/。
// 这是唯一会写文件的入口 —— 生成/改模板只动编辑器，不动磁盘。
async function saveGen() {
  const s = gen.value.site
  if (!s) return
  if (!gen.value.text.trim()) { toast.error(t('siteGenEmpty')); return }
  busy.value = true
  try {
    const res = unwrap<GenResult>(await SitesGenConfig(s.id, gen.value.backend, true, gen.value.text))
    gen.value.data = res
    gen.value.custom = true
    await load() // 站点列表带回新的 configs，之后重新打开弹窗即可回填
    // 如实反馈到哪一步：落盘 ≠ 生效。重载失败（如 443 已被占）必须说出来，
    // 否则用户看到「已写入」却打不开，只会以为功能是坏的。
    if (res?.reloadError) {
      toast.error(t('siteGenReloadFailed', { msg: res.reloadError }))
    } else if (res?.reloaded) {
      toast.success(t('siteGenApplied'))
    } else {
      toast.success(t('siteConfWritten', { path: res?.writtenPath ?? '' }))
    }
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

// regenerate 丢弃自定义内容，回到生成器的结果。只改编辑器 —— 要写进站点与磁盘仍需点「保存」，
// 免得手滑一下就把存过的自定义配置冲掉。
async function regenerate() {
  genTemplate.value = ''
  await refreshGen()
  if (gen.value.text) toast.success(t('siteGenRegenerated'))
}

async function copySnippet() {
  const text = gen.value.text
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('siteCopied'))
  } catch (e: any) {
    logErr('SitesPanel.copy', e)
    toast.error(getErrorMessage(e))
  }
}

// ---- 生成配置弹窗里的「常用配置模块」勾选 ----
// 模块定义是固定集合（与后端 internal/sites/gen.go 的 moduleOrder 对齐）；
// 透传 i18n key，由 t() 解析文案。后端会做归一化（过滤未知、消解互斥），
// 但前端也要把互斥项禁灰，避免用户勾了又发现不生效。
const moduleDefs: { id: string; labelKey: string; descKey: string }[] = [
  { id: 'spa', labelKey: 'siteModSpa', descKey: 'siteModSpaDesc' },
  { id: 'static', labelKey: 'siteModStatic', descKey: 'siteModStaticDesc' },
  { id: 'gzip', labelKey: 'siteModGzip', descKey: 'siteModGzipDesc' },
  { id: 'cache', labelKey: 'siteModCache', descKey: 'siteModCacheDesc' },
  { id: 'cors', labelKey: 'siteModCors', descKey: 'siteModCorsDesc' },
  { id: 'security', labelKey: 'siteModSecurity', descKey: 'siteModSecurityDesc' },
  { id: 'hide', labelKey: 'siteModHide', descKey: 'siteModHideDesc' },
  { id: 'upload', labelKey: 'siteModUpload', descKey: 'siteModUploadDesc' },
  { id: 'proxy', labelKey: 'siteModProxy', descKey: 'siteModProxyDesc' },
]
// 勾选中的模块与反向代理上游端口（本地态，打开弹窗时从站点回填）
const genMods = ref<string[]>([])
const genProxyPort = ref<number>(3000)

// ---- 配置模板 ----
// 模板 = 「模块组合 + 文档根」的预设，不引入第二套生成逻辑
// （多一套模板渲染 = 两处配置会漂移，用户切来切去还会得到互相矛盾的内容）。
//
// 框架模板的关键不是模块（各框架要的模块差不多），而是 **文档根**：
// Laravel / ThinkPHP / Symfony 的入口在 public，Yii2 在 web。
// root 不跟着走的话 .env、storage、vendor 会直接暴露在 web 根下。
interface SiteTemplate {
  id: string
  labelKey: string
  descKey: string
  modules: string[]
  // 应用模板时一并写入站点的文档根。
  // undefined = 不动它；'' = 重置为项目根（WordPress 这类入口就在根目录的框架）；
  // 其它值 = 项目目录下的子目录。
  docRoot?: string
  needPHP?: boolean
  group: 'php' | 'other'
}
const templates: SiteTemplate[] = [
  // --- PHP 框架（固定入口目录 + 保留 index.php 回退）---
  { id: 'laravel', labelKey: 'siteTplLaravel', descKey: 'siteTplLaravelDesc', group: 'php', docRoot: 'public', needPHP: true, modules: ['gzip', 'hide', 'security', 'upload', 'cache'] },
  { id: 'thinkphp', labelKey: 'siteTplThinkPHP', descKey: 'siteTplThinkPHPDesc', group: 'php', docRoot: 'public', needPHP: true, modules: ['gzip', 'hide', 'security', 'upload'] },
  { id: 'yii2', labelKey: 'siteTplYii', descKey: 'siteTplYiiDesc', group: 'php', docRoot: 'web', needPHP: true, modules: ['gzip', 'hide', 'security'] },
  { id: 'symfony', labelKey: 'siteTplSymfony', descKey: 'siteTplSymfonyDesc', group: 'php', docRoot: 'public', needPHP: true, modules: ['gzip', 'hide', 'security', 'upload'] },
  { id: 'wordpress', labelKey: 'siteTplWordPress', descKey: 'siteTplWordPressDesc', group: 'php', docRoot: '', needPHP: true, modules: ['gzip', 'hide', 'security', 'upload', 'cache'] },
  // --- 静态资源与代理 ---
  { id: 'spa', labelKey: 'siteTplSpa', descKey: 'siteTplSpaDesc', group: 'other', modules: ['spa', 'gzip', 'cache', 'hide', 'security'] },
  { id: 'static', labelKey: 'siteTplStatic', descKey: 'siteTplStaticDesc', group: 'other', modules: ['static', 'gzip', 'cache', 'hide', 'security'] },
  { id: 'node', labelKey: 'siteTplNode', descKey: 'siteTplNodeDesc', group: 'other', modules: ['proxy', 'gzip', 'hide', 'security'] },
  { id: 'upload', labelKey: 'siteTplUpload', descKey: 'siteTplUploadDesc', group: 'other', modules: ['spa', 'gzip', 'upload', 'hide', 'security'] },
]
const genTemplate = ref('')
const phpTemplates = computed(() => templates.filter(x => x.group === 'php'))
const otherTemplates = computed(() => templates.filter(x => x.group === 'other'))

// 当前模板的 PHP 依赖没被满足（选了 PHP 框架但机器上没有 PHP）→ 生成的配置不会有 FastCGI 段。
const templateNeedsPHP = computed(() => {
  const tpl = templates.find(x => x.id === genTemplate.value)
  return !!tpl?.needPHP && !hasPHP.value
})

// 站点上存着该后端的自定义内容：此时编辑器里若显示的是生成结果，要提醒「保存才会覆盖」。
const hasSavedCustom = computed(() => !!gen.value.site?.configs?.[gen.value.backend])

// 生成配置弹窗里展示实际生效的站点根目录：文档根是个容易忘的设置，
// 配置里的 root 指到哪必须一眼看到，否则改了也不知道有没有生效。
const siteRootPath = computed(() => {
  const s = gen.value.site
  if (!s) return ''
  const base = s.dir.replace(/[\\/]+$/, '')
  return s.docRoot ? base + '/' + s.docRoot : base
})

// applyTemplate 应用模板：文档根写回站点（站点属性），模块组也落库（模块是站点属性），再刷新编辑器。
async function applyTemplate(id: string) {
  genTemplate.value = id
  const tpl = templates.find(x => x.id === id)
  if (!tpl) return
  genMods.value = tpl.modules.slice()
  if (genMods.value.includes('proxy') && (!genProxyPort.value || genProxyPort.value <= 0)) {
    genProxyPort.value = 3000
  }
  // 文档根只能通过 SitesUpdate 写（它是站点结构属性，不在「生成配置」的范畴里）。
  // 失败不阻断模块落库——两件事相互独立，没必要因为一个失败就丢掉另一个。
  const s = gen.value.site
  const wantRoot = tpl.docRoot
  if (s && wantRoot !== undefined && (s.docRoot ?? '') !== wantRoot) {
    try {
      const updated = unwrap<SiteItem>(await SitesUpdate(s.id, s.name, s.domain, s.dir, s.enabled, wantRoot))
      if (updated) gen.value.site = { ...s, ...updated }
    } catch (e: any) {
      toast.error(getErrorMessage(e))
    }
  }
  await persistMods() // 内部会 refreshGen()，编辑器换成新生成的内容
  await load() // 拉回最新站点，让列表上的文档根徽标与弹窗里的根目录同步
}

// 互斥禁用：proxy 接管全部请求 → spa/static/cache 不再有机会生效；spa 优先于 static。
function isModuleDisabled(id: string): boolean {
  if (genMods.value.includes('proxy') && (id === 'spa' || id === 'static' || id === 'cache')) return true
  if (id === 'static' && genMods.value.includes('spa')) return true
  return false
}

// 模块改变后立即落库并刷新预览：模块是站点属性，落库后下次打开自动回填。
async function persistMods() {
  const s = gen.value.site
  if (!s) return
  try {
    unwrap(await SitesSetModules(s.id, genMods.value, Number(genProxyPort.value) || 0))
    await refreshGen()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function toggleModule(id: string) {
  if (isModuleDisabled(id)) return
  const arr = genMods.value.slice()
  const i = arr.indexOf(id)
  if (i >= 0) arr.splice(i, 1)
  else arr.push(id)
  genMods.value = arr
  genTemplate.value = '' // 手工改动后就不再是某个模板了
  await persistMods()
}

async function onProxyPortChange() {
  await persistMods()
}

onMounted(() => { load(); loadCert() })
</script>

<template>
  <div class="sites-panel">
    <header class="detail-head">
      <span class="detail-avatar" style="background:#e8a33d">⌂</span>
      <div class="detail-titles">
        <div class="detail-title-row">
          <span class="detail-name">{{ t('sitesTitle') }}</span>
          <span class="detail-id">sites</span>
        </div>
        <div class="detail-badges">
          <span class="badge">{{ t('sitesBadge') }}</span>
        </div>
      </div>
    </header>

    <!-- 服务状态条：为什么打不开，一眼看到是哪一环没就绪。
         站点自己没有运行态 —— 这一栏反映的是 nginx/caddy 的安装与运行情况。 -->
    <section class="detail-block">
      <div class="block-head"><span class="block-title">{{ t('siteService') }}</span></div>
      <div class="status-grid">
        <div class="status-item">
          <span class="status-label">{{ t('siteServer') }}</span>
          <span v-if="serverAvailable" :class="['status-value', serverRunning ? 'ok' : 'off']">
            {{ serverText }} · {{ serverRunning ? t('siteRunning') : t('siteStopped') }}
          </span>
          <span v-else class="status-value off">{{ t('siteServerNone') }}</span>
        </div>
        <div class="status-item">
          <span class="status-label">{{ t('siteCert') }}</span>
          <span :class="['status-value', status.certReady ? 'ok' : 'off']">
            {{ status.certReady ? t('siteCertReady') : (status.certError || t('siteCertPending')) }}
          </span>
        </div>
        <div class="status-item">
          <span class="status-label">{{ t('siteCATrust') }}</span>
          <span :class="['status-value', caTrusted ? 'ok' : 'off']">
            {{ caTrusted ? t('siteCATrustedShort') : t('siteCANotTrusted') }}
            <button v-if="!caTrusted" class="link-btn" @click="trustCA">{{ t('siteCATrustAction') }}</button>
          </span>
        </div>
        <div class="status-item">
          <span class="status-label">{{ t('siteHosts') }}</span>
          <span :class="['status-value', status.hostsOk ? 'ok' : 'off']">
            {{ status.hostsOk ? t('siteHostsOK') : (status.hostsError || t('siteHostsPending')) }}
            <button v-if="!status.hostsOk" class="link-btn" @click="syncHosts">{{ t('siteHostsSync') }}</button>
            <button v-if="!status.hostsOk" class="link-btn" @click="copyHostsBlock">{{ t('siteHostsCopy') }}</button>
          </span>
        </div>
      </div>
      <p class="block-hint">{{ t('siteServiceHint') }}</p>
      <p class="block-hint">{{ t('siteHostsPathHint', { path: status.hostsPath }) }}</p>
      <!-- 提权预期管理：UAC 是系统弹的，应用无法先弹一句提示再弹它，所以常驻说明 -->
      <p v-if="!status.hostsOk && !status.elevated" class="block-hint">{{ t('siteHostsAdminHint') }}</p>
      <p v-if="!status.hostsOk && status.elevated" class="block-hint warn">{{ t('siteHostsLockedHint') }}</p>
      <!-- 有站点却没人提供服务：直接说破，比让用户去猜「配置生效没有」便宜得多 -->
      <p v-if="sites.length && !serverAvailable" class="block-hint warn">{{ t('siteNeedServer') }}</p>
      <p v-else-if="sites.length && !serverRunning" class="block-hint warn">{{ t('siteServerStopped') }}</p>
      <p v-if="caMessage" class="block-hint warn">{{ caMessage }}</p>
    </section>

    <!-- 站点列表 -->
    <section class="detail-block">
      <div class="block-head">
        <span class="block-title">{{ t('siteList') }}</span>
        <button
          class="link-btn"
          :disabled="!serverAvailable"
          :title="serverAvailable ? '' : t('siteNeedServer')"
          @click="openCreate"
        >{{ t('siteAdd') }}</button>
      </div>
      <!-- 一个站点都没建、又没装服务器软件：把要求直接放在被禁用按钮旁边，这是最该看到它的位置 -->
      <p v-if="!sites.length && !serverAvailable" class="block-hint warn">{{ t('siteNeedServer') }}</p>
      <div v-if="sites.length" class="site-list">
        <div v-for="s in sites" :key="s.id" class="site-row">
          <div class="site-main">
            <a class="site-domain" :href="siteURL(s)" @click.prevent="openSite(s)">{{ s.domain }}</a>
            <span class="site-name">{{ s.name }}</span>
            <span v-if="s.docRoot" class="site-doc-root" :title="t('siteDocRootLabel')">{{ s.docRoot }}</span>
            <span class="site-dir" :title="s.dir">{{ s.dir }}</span>
            <span v-if="!s.enabled" class="site-off">{{ t('siteDisabled') }}</span>
          </div>
          <div class="site-ops">
            <button class="op-btn" @click="toggleEnabled(s)">{{ s.enabled ? t('siteDisable') : t('siteEnable') }}</button>
            <button class="op-btn" @click="openGen(s)">{{ t('siteGenConf') }}</button>
            <button class="op-btn" @click="openEdit(s)">{{ t('edit') }}</button>
            <button class="op-btn danger" @click="remove(s)">{{ t('deleteVersion') }}</button>
          </div>
        </div>
      </div>
      <div v-else class="empty-state">
        <div class="empty-icon">∅</div>
        <div class="empty-text">{{ t('siteNoSite') }}</div>
      </div>
    </section>

    <!-- 新增 / 编辑弹窗 -->
    <Teleport to="body">
      <div v-if="formOpen" class="gen-overlay" @mousedown.self="closeForm">
        <div class="gen-panel form-panel" @mousedown.stop>
          <div class="gen-head">
            <span class="block-title">{{ editingId ? t('siteEdit') : t('siteAdd') }}</span>
            <button class="link-btn" @click="closeForm">{{ t('cancel') }}</button>
          </div>
          <div class="gen-body">
            <label class="form-row">
              <span class="form-label">{{ t('siteNameLabel') }}</span>
              <input v-model="form.name" class="env-input" :placeholder="t('siteNamePlaceholder')" />
            </label>
            <label class="form-row">
              <span class="form-label">{{ t('siteDomainLabel') }}</span>
              <input v-model="form.domain" class="env-input" :placeholder="t('siteDomainPlaceholder')" />
            </label>
            <!-- 这行用 div 而非 label：label 里不该放 button（点击会被浏览器再转发一次） -->
            <div class="form-row">
              <span class="form-label">{{ t('siteDirLabel') }}</span>
              <span class="form-inline">
                <input v-model="form.dir" class="env-input" :placeholder="t('siteDirPlaceholder')" readonly />
                <button class="op-btn" @click="pickDir">{{ t('siteChooseDir') }}</button>
              </span>
            </div>
            <label class="form-row">
              <span class="form-label">{{ t('siteDocRootLabel') }}</span>
              <input v-model="form.docRoot" class="env-input" :placeholder="t('siteDocRootPlaceholder')" />
            </label>
            <p class="block-hint">{{ t('siteDocRootHint') }}</p>
            <p class="block-hint">{{ t('siteDomainHint') }}</p>
          </div>
          <div class="gen-foot">
            <button class="op-btn" @click="closeForm">{{ t('cancel') }}</button>
            <button class="op-btn primary" :disabled="busy" @click="submit">
              {{ editingId ? t('save') : t('siteCreate') }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- 配置生成弹窗：nginx server 块 / Caddyfile 站点块 -->
    <Teleport to="body">
      <div v-if="gen.open" class="gen-overlay" @mousedown.self="gen.open = false">
        <div class="gen-panel" @mousedown.stop>
          <div class="gen-head">
            <span class="block-title">{{ t('siteGenConf') }} · {{ gen.site?.domain }}</span>
            <button class="link-btn" @click="gen.open = false">{{ t('cancel') }}</button>
          </div>
          <div class="gen-tabs">
            <button
              v-for="b in backends"
              :key="b.id"
              :class="['op-btn', { primary: gen.backend === b.id }]"
              :disabled="!b.available"
              :title="b.available ? b.id + ' ' + b.version : (b.reason || '')"
              @click="switchBackend(b.id)"
            >
              {{ b.id }}<span v-if="!b.available" class="gen-tag-off">{{ t('siteGenBackendMissing') }}</span>
            </button>
            <span v-if="gen.data?.needsPhpFpm" class="gen-tag">{{ t('siteGenHasPHP') }}</span>
            <!-- 纯静态站点不会生成 PHP 段 —— 顺带说清「它不需要 php-fpm 的 9000」 -->
            <span v-else-if="gen.data" class="gen-tag muted">{{ t('siteGenStaticOnly') }}</span>
            <span v-if="gen.custom" class="gen-tag">{{ t('siteGenCustomTag') }}</span>
          </div>
          <p v-if="!backends.some(b => b.available)" class="block-hint warn">{{ t('siteGenNoBackend') }}</p>
          <p class="block-hint">{{ t('siteGenHint') }}</p>
          <!-- 中段整体可滚动：窗口矮的时候也必须能摸到底部的「保存」，否则等于没有保存入口 -->
          <div class="gen-body">
            <!-- 模板 = 常用模块 + 文档根的一键预设，不引入第二套生成逻辑 -->
            <div class="gen-tpl">
              <span class="gen-tpl-label">{{ t('siteGenTemplate') }}</span>
              <select v-model="genTemplate" class="env-input gen-tpl-select" @change="applyTemplate(genTemplate)">
                <option value="">{{ t('siteGenTemplateNone') }}</option>
                <optgroup :label="t('siteTplGroupPhp')">
                  <option v-for="tpl in phpTemplates" :key="tpl.id" :value="tpl.id">{{ t(tpl.labelKey) }}</option>
                </optgroup>
                <optgroup :label="t('siteTplGroupOther')">
                  <option v-for="tpl in otherTemplates" :key="tpl.id" :value="tpl.id">{{ t(tpl.labelKey) }}</option>
                </optgroup>
              </select>
              <span v-if="genTemplate" class="gen-tpl-desc">
                {{ t(templates.find(x => x.id === genTemplate)?.descKey ?? '') }}
              </span>
            </div>
            <!-- 配置里的 root 到底指向哪：文档根是个容易忘的设置，必须一眼可见 -->
            <p class="block-hint">{{ t('siteGenRoot', { path: siteRootPath }) }}</p>
            <p v-if="templateNeedsPHP" class="block-hint warn">{{ t('siteGenNoPHP') }}</p>
            <!-- 常用配置模块：勾选后实时预览并随站点保存，下次打开自动回填 -->
            <div class="gen-modules">
              <div class="gen-modules-title">{{ t('siteModTitle') }}</div>
              <div class="gen-mod-grid">
                <label
                  v-for="m in moduleDefs"
                  :key="m.id"
                  class="gen-mod"
                  :class="{ disabled: isModuleDisabled(m.id) }"
                >
                  <input
                    type="checkbox"
                    :checked="genMods.includes(m.id)"
                    :disabled="isModuleDisabled(m.id)"
                    @change="toggleModule(m.id)"
                  />
                  <span class="gen-mod-label">{{ t(m.labelKey) }}</span>
                  <span class="gen-mod-desc">{{ t(m.descKey) }}</span>
                </label>
              </div>
              <div v-if="genMods.includes('proxy')" class="gen-proxy">
                <span class="gen-proxy-label">{{ t('siteModProxyPort') }}</span>
                <input
                  v-model.number="genProxyPort"
                  type="number"
                  min="1"
                  max="65535"
                  class="env-input port-input"
                  @change="onProxyPortChange"
                />
              </div>
              <p class="block-hint">{{ t('siteModHint') }}</p>
            </div>
            <p class="block-hint">{{ t('siteGenEditorHint') }}</p>
            <!-- 可编辑：生成器只给初稿，最终落盘的是这里的内容 -->
            <textarea v-model="gen.text" class="gen-code" spellcheck="false" />
            <div v-if="gen.data" class="gen-meta">
              <!-- 落盘 ≠ 生效：先把「到底有没有生效」摆出来，再给路径信息 -->
              <p v-if="gen.data.reloaded" class="block-hint ok">{{ t('siteGenAppliedHint') }}</p>
              <p v-else-if="gen.data.reloadError" class="block-hint warn">{{ t('siteGenReloadFailed', { msg: gen.data.reloadError }) }}</p>
              <p v-if="gen.data.manualImport" class="block-hint warn">{{ t('siteGenManualImport') }}</p>
              <p class="block-hint">{{ t('siteGenInclude', { line: gen.data.includeLine }) }}</p>
              <p class="block-hint">{{ t('siteGenTarget', { path: gen.data.targetDir }) }}</p>
              <p v-if="gen.data.writtenPath" class="block-hint ok">{{ t('siteGenWritten', { path: gen.data.writtenPath }) }}</p>
            </div>
            <p v-if="hasSavedCustom && !gen.custom" class="block-hint">{{ t('siteGenSavedHint') }}</p>
          </div>
          <div class="gen-foot">
            <button class="op-btn" :disabled="!gen.text" @click="copySnippet">{{ t('siteCopy') }}</button>
            <button class="op-btn" @click="regenerate">{{ t('siteGenRegen') }}</button>
            <button class="op-btn primary" :disabled="busy || !gen.text.trim()" @click="saveGen">{{ t('siteGenSave') }}</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
/* 本组件挂在 EnvironmentPage 的 .env-detail 里，外层已统一给了内边距与滚动容器。
   这里再自带一层的话顶部会双重缩进（比其它分类明显低一截），且形成嵌套滚动区。 */
.sites-panel { display: flex; flex-direction: column; gap: 16px; }
.detail-head { display: flex; align-items: center; gap: 12px; }
.detail-avatar {
  width: 40px; height: 40px; border-radius: var(--radius-md); flex: 0 0 auto;
  display: flex; align-items: center; justify-content: center;
  font-size: 18px; color: #fff;
}
.detail-titles { display: flex; flex-direction: column; gap: 4px; }
.detail-title-row { display: flex; align-items: baseline; gap: 8px; }
.detail-name { font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.detail-id { font-size: 11px; color: var(--color-text-disabled); }
.detail-badges { display: flex; gap: 6px; }
.badge {
  font-size: 10px; padding: 1px 6px; border-radius: var(--radius-xs);
  background: var(--color-bg-active); color: var(--color-text-muted);
}
.detail-block {
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  padding: 14px 16px; background: var(--color-surface);
  display: flex; flex-direction: column; gap: 10px;
}
.block-head { display: flex; align-items: center; justify-content: space-between; }
.block-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.block-hint { font-size: 11px; color: var(--color-text-disabled); margin: 0; }
.block-hint.warn { color: var(--color-danger); }
.status-grid { display: flex; flex-direction: column; gap: 6px; }
.status-item { display: flex; align-items: center; gap: 10px; font-size: 12px; }
.status-label { width: 96px; flex: 0 0 auto; color: var(--color-text-muted); }
.status-value { display: flex; align-items: center; gap: 6px; color: var(--color-text-primary); }
.status-value.ok { color: var(--color-accent); }
.status-value.off { color: var(--color-text-disabled); }
.port-input { width: 90px; padding: 3px 8px; font-size: 12px; }
.env-input {
  padding: 7px 10px; border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 12px; font-family: inherit; outline: none; min-width: 0;
}
.env-input:focus { border-color: var(--color-accent); }
.env-input::placeholder { color: var(--color-text-disabled); }
.site-list { display: flex; flex-direction: column; }
.site-row {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 8px 0; border-top: 1px solid var(--color-border);
}
.site-main { display: flex; align-items: baseline; gap: 10px; min-width: 0; flex: 1; }
.site-domain { font-size: 13px; font-weight: 600; color: var(--color-accent); text-decoration: none; }
.site-domain:hover { text-decoration: underline; }
.site-name { font-size: 12px; color: var(--color-text-muted); }
/* 文档根徽标：一眼看出这个站点不是从项目根对外服务的（Laravel→public 之类） */
.site-doc-root {
  flex: 0 0 auto; font-size: 10px; padding: 1px 5px; border-radius: var(--radius-xs);
  background: var(--color-bg-active); color: var(--color-text-muted);
  font-family: ui-monospace, Consolas, monospace;
}
.site-dir {
  font-size: 11px; color: var(--color-text-disabled); overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap; max-width: 240px;
}
.site-off { font-size: 11px; color: var(--color-danger); }
.site-ops { display: flex; gap: 6px; flex: 0 0 auto; }
.op-btn {
  padding: 4px 10px; border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  font-size: 11px; font-family: inherit; cursor: pointer;
}
.op-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.op-btn.danger { color: var(--color-danger); }
.op-btn.primary { background: var(--color-accent); color: var(--color-accent-text); border-color: transparent; }
.op-btn:disabled { opacity: 0.5; cursor: default; }
.link-btn {
  background: none; border: none; padding: 0; font-size: 12px; font-family: inherit;
  color: var(--color-accent); cursor: pointer;
}
.link-btn:hover:not(:disabled) { text-decoration: underline; }
.link-btn:disabled { color: var(--color-text-disabled); cursor: default; }
.empty-state { display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 24px 0; }
.empty-icon { font-size: 20px; color: var(--color-text-disabled); }
.empty-text { font-size: 12px; color: var(--color-text-disabled); }

/* 配置生成弹窗：Teleport 到 body，避免被面板的 overflow 裁掉 */
.gen-overlay {
  position: fixed; inset: 0; z-index: 9000;
  background: var(--color-bg-overlay);
  display: flex; align-items: center; justify-content: center;
}
.gen-panel {
  width: 720px; max-width: 92vw; max-height: 84vh; overflow: hidden;
  display: flex; flex-direction: column; gap: 10px; padding: 16px 18px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: var(--radius-lg); box-shadow: 0 12px 40px var(--color-bg-overlay);
}
.gen-head { display: flex; align-items: center; justify-content: space-between; flex: 0 0 auto; }
.gen-tabs { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; flex: 0 0 auto; }
.gen-tag { font-size: 11px; color: var(--color-accent); }
.gen-tag.muted { color: var(--color-text-disabled); }
.gen-tag-off { margin-left: 5px; font-size: 10px; opacity: 0.75; }
/* 中段独立滚动：头部/底部固定，窗口再矮也点得到「保存」 */
.gen-body {
  flex: 1 1 auto; min-height: 0; overflow-y: auto;
  display: flex; flex-direction: column; gap: 10px;
}
.gen-tpl { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.gen-tpl-label { font-size: 12px; color: var(--color-text-muted); }
.gen-tpl-select { width: 260px; }
.gen-tpl-desc { flex: 1 1 100%; font-size: 11px; color: var(--color-text-disabled); }
.gen-code {
  min-height: 200px; width: 100%; box-sizing: border-box; resize: vertical; margin: 0;
  padding: 12px; border-radius: var(--radius-md); border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-family: ui-monospace, Consolas, monospace; font-size: 12px; line-height: 1.5;
  white-space: pre; outline: none;
}
.gen-code:focus { border-color: var(--color-accent); }
.gen-meta { display: flex; flex-direction: column; gap: 2px; flex: 0 0 auto; }
.block-hint.ok { color: var(--color-accent); }
.gen-foot { display: flex; justify-content: flex-end; gap: 8px; flex: 0 0 auto; }
.gen-modules { display: flex; flex-direction: column; gap: 8px; }
.gen-modules-title { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }
.gen-mod-grid {
  display: grid; grid-template-columns: 1fr 1fr; gap: 6px;
}
.gen-mod {
  display: grid; grid-template-columns: auto 1fr; grid-template-rows: auto auto;
  column-gap: 8px; align-items: center;
  padding: 6px 8px; border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); cursor: pointer;
}
.gen-mod.disabled { opacity: 0.45; cursor: not-allowed; }
.gen-mod input { grid-row: 1 / 3; width: 14px; height: 14px; align-self: center; }
.gen-mod-label { font-size: 12px; color: var(--color-text-primary); }
.gen-mod-desc { font-size: 10px; color: var(--color-text-disabled); line-height: 1.3; }
.gen-proxy { display: flex; align-items: center; gap: 8px; }
.gen-proxy-label { font-size: 11px; color: var(--color-text-muted); }

/* 新增 / 编辑弹窗：字段是竖向表单，比配置弹窗窄 */
.form-panel { width: 520px; }
.form-row { display: grid; grid-template-columns: 84px 1fr; align-items: center; gap: 10px; }
.form-label { font-size: 12px; color: var(--color-text-muted); }
.form-inline { display: flex; align-items: center; gap: 8px; }
.form-inline .env-input { flex: 1; min-width: 0; }
</style>
