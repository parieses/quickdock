<script setup lang="ts">
// 本地开发站点：域名 → 目录，单一 HTTPS 监听器按域名分发，mkcert 自动签发证书，
// 域名解析写入系统 hosts。后端实现在 internal/sites。
import { ref, onMounted, computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import {
  SitesList, SitesCreate, SitesUpdate, SitesDelete,
  SitesStart, SitesStop, SitesSyncHosts, SitesSetPort, SitesGenConfig, SitesSetModules,
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
  enabled: boolean
  running: boolean
  // 生成配置时勾选的常用模块与反向代理上游端口（随站点持久化，弹窗打开时回填）
  modules?: string[]
  proxyPort?: number
}
interface SitesStatus {
  running: boolean
  port: number
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
const status = ref<SitesStatus>({ running: false, port: 443, certReady: false, certError: '', hostsOk: false, hostsError: '', hostsPath: '', hostsBlock: '', elevated: false })
const caTrusted = ref(false)
const caMessage = ref('')
const busy = ref(false)
const portInput = ref<number>(443)

const form = ref({ name: '', domain: '', dir: '' })
// 编辑中的站点 id（'' = 新增态）
const editingId = ref('')

const showForm = computed(() => editingId.value !== '' || form.value.dir !== '' || form.value.domain !== '')

async function load() {
  try {
    const res = unwrap<{ sites: SiteItem[]; status: SitesStatus }>(await SitesList())
    if (res) {
      sites.value = res.sites ?? []
      status.value = res.status
      portInput.value = res.status.port
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

function resetForm() {
  form.value = { name: '', domain: '', dir: '' }
  editingId.value = ''
}

async function submit() {
  if (!form.value.dir) { toast.error(t('siteNeedDir')); return }
  if (!form.value.domain) { toast.error(t('siteNeedDomain')); return }
  busy.value = true
  try {
    const name = form.value.name || form.value.domain
    if (editingId.value) {
      unwrap(await SitesUpdate(editingId.value, name, form.value.domain, form.value.dir, true))
      toast.success(t('siteUpdated'))
    } else {
      unwrap(await SitesCreate(name, form.value.domain, form.value.dir))
      toast.success(t('siteCreated'))
    }
    resetForm()
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

function edit(s: SiteItem) {
  editingId.value = s.id
  form.value = { name: s.name, domain: s.domain, dir: s.dir }
}

async function toggleEnabled(s: SiteItem) {
  try {
    unwrap(await SitesUpdate(s.id, s.name, s.domain, s.dir, !s.enabled))
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function remove(s: SiteItem) {
  try {
    unwrap(await SitesDelete(s.id))
    if (editingId.value === s.id) resetForm()
    await load()
    toast.success(t('deleted'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function start() {
  busy.value = true
  try {
    unwrap(await SitesStart())
    await load()
    toast.success(t('siteStarted'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

async function stop() {
  try {
    unwrap(await SitesStop())
    await load()
    toast.success(t('siteStoppedToast'))
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

async function applyPort() {
  try {
    unwrap(await SitesSetPort(Number(portInput.value) || 0))
    await load()
    toast.success(t('sitePortSaved'))
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

// 443 是 https 默认端口，URL 里省掉端口号更自然
function siteURL(s: SiteItem): string {
  const p = status.value.port
  return 'https://' + s.domain + (p === 443 ? '' : ':' + p) + '/'
}

interface GenResult {
  backend: string
  snippet: string
  fileName: string
  includeLine: string
  needsPhpFpm: boolean
  confPath: string
  targetDir: string
  writtenPath?: string
}

const gen = ref<{ open: boolean; site: SiteItem | null; backend: string; data: GenResult | null }>(
  { open: false, site: null, backend: 'nginx', data: null },
)

// openGen 打开配置生成弹窗并立即按当前后端生成一次。
async function openGen(s: SiteItem, backend = 'nginx') {
  // 模块选择是站点属性：打开时从站点回填，用户不必每次重勾。
  genMods.value = (s.modules ?? []).slice()
  genProxyPort.value = s.proxyPort && s.proxyPort > 0 ? s.proxyPort : 3000
  gen.value = { open: true, site: s, backend, data: null }
  await refreshGen()
}

async function refreshGen() {
  const s = gen.value.site
  if (!s) return
  try {
    const res = unwrap<GenResult>(await SitesGenConfig(s.id, gen.value.backend, false))
    gen.value.data = res
  } catch (e: any) {
    gen.value.data = null
    toast.error(getErrorMessage(e))
  }
}

async function writeGen() {
  const s = gen.value.site
  if (!s) return
  try {
    const res = unwrap<GenResult>(await SitesGenConfig(s.id, gen.value.backend, true))
    gen.value.data = res
    toast.success(t('siteConfWritten', { path: res?.writtenPath ?? '' }))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function copySnippet() {
  const text = gen.value.data?.snippet
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

    <!-- 服务状态条：为什么打不开，一眼看到是哪一环没就绪 -->
    <section class="detail-block">
      <div class="block-head"><span class="block-title">{{ t('siteService') }}</span></div>
      <div class="status-grid">
        <div class="status-item">
          <span class="status-label">{{ t('siteServiceState') }}</span>
          <span :class="['status-value', status.running ? 'ok' : 'off']">
            {{ status.running ? t('siteRunning') : t('siteStopped') }}
          </span>
        </div>
        <div class="status-item">
          <span class="status-label">{{ t('sitePort') }}</span>
          <span class="status-value">
            <input v-model.number="portInput" type="number" min="1" max="65535" class="env-input port-input" :disabled="status.running" />
            <button class="link-btn" :disabled="status.running" @click="applyPort">{{ t('save') }}</button>
          </span>
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
            <button v-if="!status.hostsOk && status.running" class="link-btn" @click="syncHosts">{{ t('siteHostsSync') }}</button>
            <button v-if="!status.hostsOk" class="link-btn" @click="copyHostsBlock">{{ t('siteHostsCopy') }}</button>
          </span>
        </div>
      </div>
      <p class="block-hint">{{ t('siteHostsPathHint', { path: status.hostsPath }) }}</p>
      <!-- 提权预期管理：UAC 是系统弹的，应用无法先弹一句提示再弹它，所以常驻说明 -->
      <p v-if="!status.hostsOk && !status.elevated" class="block-hint">{{ t('siteHostsAdminHint') }}</p>
      <p v-if="!status.hostsOk && status.elevated" class="block-hint warn">{{ t('siteHostsLockedHint') }}</p>
      <div class="service-ops">
        <button v-if="!status.running" class="op-btn primary" :disabled="busy || sites.length === 0" @click="start">{{ t('siteStart') }}</button>
        <button v-else class="op-btn danger" @click="stop">{{ t('siteStop') }}</button>
      </div>
      <p v-if="caMessage" class="block-hint warn">{{ caMessage }}</p>
    </section>

    <!-- 站点列表 -->
    <section class="detail-block">
      <div class="block-head">
        <span class="block-title">{{ t('siteList') }}</span>
        <button class="link-btn" @click="resetForm(); form.dir = ' '">{{ t('siteAdd') }}</button>
      </div>
      <div v-if="sites.length" class="site-list">
        <div v-for="s in sites" :key="s.id" class="site-row">
          <div class="site-main">
            <a class="site-domain" :href="siteURL(s)" target="_blank" rel="noopener">{{ s.domain }}</a>
            <span class="site-name">{{ s.name }}</span>
            <span class="site-dir" :title="s.dir">{{ s.dir }}</span>
            <span v-if="!s.enabled" class="site-off">{{ t('siteDisabled') }}</span>
          </div>
          <div class="site-ops">
            <button class="op-btn" @click="toggleEnabled(s)">{{ s.enabled ? t('siteDisable') : t('siteEnable') }}</button>
            <button class="op-btn" @click="openGen(s)">{{ t('siteGenConf') }}</button>
            <button class="op-btn" @click="edit(s)">{{ t('edit') }}</button>
            <button class="op-btn danger" @click="remove(s)">{{ t('deleteVersion') }}</button>
          </div>
        </div>
      </div>
      <div v-else class="empty-state">
        <div class="empty-icon">∅</div>
        <div class="empty-text">{{ t('siteNoSite') }}</div>
      </div>
    </section>

    <!-- 新增/编辑表单 -->
    <section v-if="showForm" class="detail-block install-card">
      <div class="block-head">
        <span class="block-title">{{ editingId ? t('siteEdit') : t('siteAdd') }}</span>
        <button class="link-btn" @click="resetForm">{{ t('cancel') }}</button>
      </div>
      <div class="install-grid">
        <input v-model="form.name" class="env-input" :placeholder="t('siteNamePlaceholder')" />
        <input v-model="form.domain" class="env-input" :placeholder="t('siteDomainPlaceholder')" />
        <button class="link-btn" @click="pickDir">{{ t('siteChooseDir') }}</button>
        <input v-model="form.dir" class="env-input" :placeholder="t('siteDirPlaceholder')" readonly />
        <button class="env-install-btn" :disabled="busy" @click="submit">
          {{ editingId ? t('save') : t('siteCreate') }}
        </button>
      </div>
      <p class="block-hint">{{ t('siteDomainHint') }}</p>
    </section>

    <!-- 配置生成弹窗：nginx server 块 / Caddyfile 站点块 -->
    <Teleport to="body">
      <div v-if="gen.open" class="gen-overlay" @mousedown.self="gen.open = false">
        <div class="gen-panel" @mousedown.stop>
          <div class="gen-head">
            <span class="block-title">{{ t('siteGenConf') }} · {{ gen.site?.domain }}</span>
            <button class="link-btn" @click="gen.open = false">{{ t('cancel') }}</button>
          </div>
          <div class="gen-tabs">
            <button :class="['op-btn', { primary: gen.backend === 'nginx' }]" @click="gen.backend = 'nginx'; refreshGen()">nginx</button>
            <button :class="['op-btn', { primary: gen.backend === 'caddy' }]" @click="gen.backend = 'caddy'; refreshGen()">caddy</button>
            <span v-if="gen.data?.needsPhpFpm" class="gen-tag">{{ t('siteGenHasPHP') }}</span>
          </div>
          <p class="block-hint">{{ t('siteGenHint') }}</p>
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
          <pre v-if="gen.data" class="gen-code">{{ gen.data.snippet }}</pre>
          <div v-if="gen.data" class="gen-meta">
            <p class="block-hint">{{ t('siteGenInclude', { line: gen.data.includeLine }) }}</p>
            <p class="block-hint">{{ t('siteGenTarget', { path: gen.data.targetDir }) }}</p>
            <p v-if="gen.data.writtenPath" class="block-hint ok">{{ t('siteGenWritten', { path: gen.data.writtenPath }) }}</p>
          </div>
          <div class="gen-foot">
            <button class="op-btn" @click="copySnippet">{{ t('siteCopy') }}</button>
            <button class="op-btn primary" @click="writeGen">{{ t('siteGenWrite') }}</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.sites-panel { display: flex; flex-direction: column; gap: 16px; padding: 20px 24px; overflow-y: auto; }
.detail-head { display: flex; align-items: center; gap: 12px; }
.detail-avatar {
  width: 40px; height: 40px; border-radius: 10px; flex: 0 0 auto;
  display: flex; align-items: center; justify-content: center;
  font-size: 18px; color: #fff;
}
.detail-titles { display: flex; flex-direction: column; gap: 4px; }
.detail-title-row { display: flex; align-items: baseline; gap: 8px; }
.detail-name { font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.detail-id { font-size: 11px; color: var(--color-text-disabled); }
.detail-badges { display: flex; gap: 6px; }
.badge {
  font-size: 10px; padding: 1px 6px; border-radius: 4px;
  background: var(--color-bg-active); color: var(--color-text-muted);
}
.detail-block {
  border: 1px solid var(--color-border); border-radius: 10px;
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
  padding: 7px 10px; border: 1px solid var(--color-border); border-radius: 6px;
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 12px; font-family: inherit; outline: none; min-width: 0;
}
.env-input:focus { border-color: var(--color-accent); }
.env-input::placeholder { color: var(--color-text-disabled); }
.install-grid {
  display: grid; grid-template-columns: 1fr 1fr; gap: 8px; align-items: center;
}
.install-card { background: var(--color-bg-tertiary); }
.site-list { display: flex; flex-direction: column; }
.site-row {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 8px 0; border-top: 1px solid var(--color-border);
}
.site-main { display: flex; align-items: baseline; gap: 10px; min-width: 0; flex: 1; }
.site-domain { font-size: 13px; font-weight: 600; color: var(--color-accent); text-decoration: none; }
.site-domain:hover { text-decoration: underline; }
.site-name { font-size: 12px; color: var(--color-text-muted); }
.site-dir {
  font-size: 11px; color: var(--color-text-disabled); overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap; max-width: 240px;
}
.site-off { font-size: 11px; color: var(--color-danger); }
.site-ops { display: flex; gap: 6px; flex: 0 0 auto; }
.op-btn {
  padding: 4px 10px; border: 1px solid var(--color-border); border-radius: 5px;
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
.env-install-btn {
  grid-column: 1 / -1; justify-self: start;
  padding: 7px 16px; border: none; border-radius: 6px;
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 12px; font-family: inherit; cursor: pointer;
}
.env-install-btn:disabled { opacity: 0.5; cursor: default; }
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
  width: 720px; max-width: 92vw; max-height: 84vh;
  display: flex; flex-direction: column; gap: 10px; padding: 16px 18px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: 12px; box-shadow: 0 12px 40px var(--color-bg-overlay);
}
.gen-head { display: flex; align-items: center; justify-content: space-between; }
.gen-tabs { display: flex; align-items: center; gap: 6px; }
.gen-tag { font-size: 11px; color: var(--color-accent); }
.gen-code {
  flex: 1; min-height: 200px; overflow: auto; margin: 0;
  padding: 12px; border-radius: 8px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-family: ui-monospace, Consolas, monospace; font-size: 12px; line-height: 1.5;
  white-space: pre;
}
.gen-meta { display: flex; flex-direction: column; gap: 2px; }
.block-hint.ok { color: var(--color-accent); }
.gen-foot { display: flex; justify-content: flex-end; gap: 8px; }
.gen-modules { display: flex; flex-direction: column; gap: 8px; }
.gen-modules-title { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }
.gen-mod-grid {
  display: grid; grid-template-columns: 1fr 1fr; gap: 6px;
}
.gen-mod {
  display: grid; grid-template-columns: auto 1fr; grid-template-rows: auto auto;
  column-gap: 8px; align-items: center;
  padding: 6px 8px; border: 1px solid var(--color-border); border-radius: 6px;
  background: var(--color-bg-tertiary); cursor: pointer;
}
.gen-mod.disabled { opacity: 0.45; cursor: not-allowed; }
.gen-mod input { grid-row: 1 / 3; width: 14px; height: 14px; align-self: center; }
.gen-mod-label { font-size: 12px; color: var(--color-text-primary); }
.gen-mod-desc { font-size: 10px; color: var(--color-text-disabled); line-height: 1.3; }
.gen-proxy { display: flex; align-items: center; gap: 8px; }
.gen-proxy-label { font-size: 11px; color: var(--color-text-muted); }
</style>
