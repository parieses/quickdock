<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import {
  EnvList,
  EnvInstall,
  EnvAvailableVersions,
  EnvStart,
  EnvStop,
  EnvStatus,
  EnvSetActive,
  EnvUnsetActive,
  EnvSetMeta,
  EnvDeleteVersion,
  EnvImportVersion,
  EnvPHPConfigGet,
  EnvPHPConfigSet,
  HTTPServeList,
  HTTPServeCreate,
  HTTPServeStart,
  HTTPServeStop,
  HTTPServeDelete,
  PickFolderPath,
} from '../../bindings/quickdock/services/appservice'
import SettingsDSH from './SettingsDSH.vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

const { t } = useI18n()
const toast = inject<{ error: (m: string) => void; success: (m: string) => void }>('toast')!

const props = defineProps<{ initialSection?: string }>()

// harness（DeepSeek Harness）是特殊的「集成」分类，不属于 RuntimeAdapter 多版本模型，
// 直接在环境管理内复用 SettingsDSH 组件承载安装/配置/服务/插件/更新。
const HARNESS_KEY = 'harness'
const HTTP_KEY = 'httpserve'
const isHarness = computed(() => selectedId.value === HARNESS_KEY)
const isHttp = computed(() => selectedId.value === HTTP_KEY)

// 侧边栏分组：语言 / Web 服务器 / 缓存 / 工具。工具组追加 harness 与 HTTP 服务两个特殊入口。
const GROUP_ORDER = ['language', 'webserver', 'cache', 'tool']
const GROUP_LABEL: Record<string, string> = {
  language: 'groupLanguage',
  webserver: 'groupWebserver',
  cache: 'groupCache',
  tool: 'groupTool',
}
interface SidebarItem {
  kind: 'runtime' | 'special'
  id: string
  name: string
  avatar: string
  color: string
  count?: number
  running?: boolean
  active: boolean
}
const sidebarGroups = computed(() => {
  const out: { key: string; labelKey: string; items: SidebarItem[] }[] = []
  for (const g of GROUP_ORDER) {
    const items: SidebarItem[] = runtimes.value
      .filter((r) => r.group === g)
      .map((r) => ({
        kind: 'runtime',
        id: r.id,
        name: r.name,
        avatar: avatarText(r.id),
        color: avatarColor(r.id),
        count: r.installed.length,
        running: r.hasService && anyRunning(r),
        active: selectedId.value === r.id,
      }))
    if (g === 'tool') {
      items.push({ kind: 'special', id: HARNESS_KEY, name: t('navDsh'), avatar: 'DS', color: 'var(--color-accent)', active: isHarness.value })
      items.push({ kind: 'special', id: HTTP_KEY, name: t('httpServe'), avatar: '⬡', color: '#4a9eff', active: isHttp.value })
    }
    if (items.length) out.push({ key: g, labelKey: GROUP_LABEL[g], items })
  }
  return out
})

interface SourceInfo {
  id: string
  name: string
}
interface Install {
  version: string
  scope: string // portable | system
  path: string
  active: boolean // 是否为环境变量指向的激活版本
  inSystemPath: boolean // 该版本 bin 目录是否真正在系统 PATH 中（后端读取 PATH 比对得出）
  alias: string // 用户别名
  note: string // 用户备注
}
interface ServiceStatus {
  running: boolean
  pid: number
  port: number
  version: string
}
interface RuntimeInfo {
  id: string
  name: string
  group: string
  platforms: string[]
  recommended: string[]
  installed: Install[]
  sources: SourceInfo[]
  activeSource: string
  hasService: boolean
}

// 每个运行时的品牌色（用于头像）
const RUNTIME_COLORS: Record<string, string> = {
  php: '#777bb3',
  node: '#539e43',
  go: '#00add8',
  redis: '#d82c20',
  nginx: '#009639',
  git: '#f05032',
}
function avatarColor(id: string): string {
  return RUNTIME_COLORS[id] || 'var(--color-accent)'
}
function avatarText(id: string): string {
  return (id[0] || '?').toUpperCase()
}

const runtimes = ref<RuntimeInfo[]>([])
const selectedId = ref<string>('')

const selected = computed<RuntimeInfo | null>(() => {
  return runtimes.value.find((r) => r.id === selectedId.value) || runtimes.value[0] || null
})

// 每个运行时的交互态，按 id 索引
interface UiState {
  version: string
  source: string
  custom: string
  installing: boolean
  stage: string
  message: string
  written: number
  total: number
  available: string[]
  availLoaded: boolean
  fetching: boolean
  svc: Record<string, ServiceStatus>
}
const ui = reactive<Record<string, UiState>>({})

function stateFor(id: string, info: RuntimeInfo): UiState {
  if (!ui[id]) {
    ui[id] = {
      version: info.recommended[0] || '',
      source: info.activeSource || info.sources[0]?.id || '',
      custom: '',
      installing: false,
      stage: '',
      message: '',
      written: 0,
      total: -1,
      available: [],
      availLoaded: false,
      fetching: false,
      svc: {},
    }
  }
  return ui[id]
}

function percent(s: { written: number; total: number }): number {
  if (s.total <= 0) return -1
  return Math.min(100, Math.floor((s.written / s.total) * 100))
}

function svcOn(r: RuntimeInfo, version: string): boolean {
  const st = ui[r.id]?.svc?.[version]
  return !!(st && st.running)
}
function svcPort(r: RuntimeInfo, version: string): number {
  return ui[r.id]?.svc?.[version]?.port || 0
}
function anyRunning(r: RuntimeInfo): boolean {
  return r.installed.some((ins) => svcOn(r, ins.version))
}

// 别名/备注编辑弹窗
interface EditState {
  open: boolean
  runtime: string
  version: string
  alias: string
  note: string
}
const editing = reactive<EditState>({
  open: false,
  runtime: '',
  version: '',
  alias: '',
  note: '',
})
function openAlias(r: RuntimeInfo, ins: Install) {
  editing.open = true
  editing.runtime = r.id
  editing.version = ins.version
  editing.alias = ins.alias || ''
  editing.note = ins.note || ''
}
async function saveAlias() {
  try {
    unwrap(await EnvSetMeta(editing.runtime, editing.version, editing.alias.trim(), editing.note.trim()))
    toast.success(t('saved'))
    editing.open = false
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

// 设置/取消环境变量：未设置的版本注册其 bin 目录到系统 PATH；已设置的注销其 bin 目录。
// 取消走 EnvUnsetActive（直接按版本注销，不依赖 active 元数据），避免元数据漂移导致 PATH 残留旧版本。
async function toggleEnvVar(r: RuntimeInfo, ins: Install) {
  try {
    if (ins.inSystemPath) {
      unwrap(await EnvUnsetActive(r.id, ins.version))
    } else {
      unwrap(await EnvSetActive(r.id, ins.version))
    }
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

// 操作列下拉菜单（设置别名 / 删除 收纳于此）
const openMenu = ref<string | null>(null)
function toggleMenu(v: string) {
  openMenu.value = openMenu.value === v ? null : v
}

// 删除确认弹窗（替换原生 confirm，统一风格）
const confirmDelete = reactive<{ open: boolean; runtime: string; version: string; scope: string }>({
  open: false,
  runtime: '',
  version: '',
  scope: '',
})
function askDelete(r: RuntimeInfo, ins: Install) {
  if (ins.scope === 'system') {
    toast.error(t('systemCannotDelete'))
    return
  }
  confirmDelete.runtime = r.id
  confirmDelete.version = ins.version
  confirmDelete.scope = ins.scope
  confirmDelete.open = true
  openMenu.value = null
}
async function doDelete() {
  try {
    unwrap(await EnvDeleteVersion(confirmDelete.runtime, confirmDelete.version))
    toast.success(t('deleted'))
    confirmDelete.open = false
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

// 导入已有安装：选择外部目录 → 探测版本号并登记，使其在版本列表中可见。
async function importExisting(r: RuntimeInfo) {
  try {
    const dir = unwrap<string | null>(await PickFolderPath(t('pickDirTitle')))
    if (!dir) return
    const version = unwrap<string>(await EnvImportVersion(r.id, dir))
    toast.success(t('imported') + ' ' + r.name + ' ' + version)
    await load()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

// ---- PHP 配置弹窗（php.ini / 禁用函数 / 错误日志 / 扩展）----
interface PHPExt { name: string; file: string; enabled: boolean }
const phpModal = reactive<{
  open: boolean; runtime: string; version: string; tab: string
  raw: string; disableFunctions: string; errorLog: string; errorLogContent: string
  extensions: PHPExt[]
}>({
  open: false, runtime: '', version: '', tab: 'ini',
  raw: '', disableFunctions: '', errorLog: '', errorLogContent: '', extensions: [],
})
async function openPHPConfig(r: RuntimeInfo, ins: Install, tab: string) {
  try {
    const cfg = unwrap<any>(await EnvPHPConfigGet(r.id, ins.version))
    phpModal.runtime = r.id
    phpModal.version = ins.version
    phpModal.tab = tab
    phpModal.raw = cfg.raw || ''
    phpModal.disableFunctions = cfg.disableFunctions || ''
    phpModal.errorLog = cfg.errorLog || ''
    phpModal.errorLogContent = cfg.errorLogContent || ''
    phpModal.extensions = (cfg.extensions || []).map((e: any) => ({ name: e.name, file: e.file, enabled: !!e.enabled }))
    phpModal.open = true
    openMenu.value = null
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}
function toggleExt(i: number) {
  phpModal.extensions[i].enabled = !phpModal.extensions[i].enabled
}
async function savePHPConfig() {
  try {
    const patch: any = {}
    if (phpModal.tab === 'ini') {
      patch.raw = phpModal.raw
    } else {
      patch.disableFunctions = phpModal.disableFunctions
      patch.errorLog = phpModal.errorLog
      patch.extensions = phpModal.extensions.filter((e) => e.enabled).map((e) => e.name)
    }
    unwrap(await EnvPHPConfigSet(phpModal.runtime, phpModal.version, patch))
    toast.success(t('saved'))
    phpModal.open = false
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

// ---- HTTP 服务（目录 → 可访问的静态服务）----
interface HTTPServerItem { id: string; name: string; dir: string; port: number; running: boolean }
const httpServers = ref<HTTPServerItem[]>([])
const httpForm = reactive<{ name: string; dir: string; port: number }>({ name: '', dir: '', port: 8080 })
const httpRunning = reactive<Record<string, boolean>>({})

async function loadHTTPServers() {
  try {
    const list = unwrap<HTTPServerItem[]>(await HTTPServeList())
    if (list) {
      httpServers.value = list
      list.forEach((s) => { httpRunning[s.id] = s.running })
    }
  } catch {
    /* 忽略 */
  }
}
async function pickHTTPDir() {
  try {
    const dir = unwrap<string | null>(await PickFolderPath(t('pickDirTitle')))
    if (dir) httpForm.dir = dir
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}
async function createHTTPServer() {
  if (!httpForm.dir) { toast.error(t('httpNeedDir')); return }
  try {
    unwrap(await HTTPServeCreate(httpForm.name || httpForm.dir, httpForm.dir, httpForm.port))
    toast.success(t('httpCreated'))
    httpForm.name = ''; httpForm.dir = ''; httpForm.port = 8080
    await loadHTTPServers()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}
async function startHTTP(id: string) {
  try { unwrap(await HTTPServeStart(id)); httpRunning[id] = true; toast.success(t('httpStarted')) }
  catch (e: any) { toast.error(getErrorMessage(e)) }
}
async function stopHTTP(id: string) {
  try { unwrap(await HTTPServeStop(id)); httpRunning[id] = false; toast.success(t('httpStopped')) }
  catch (e: any) { toast.error(getErrorMessage(e)) }
}
async function deleteHTTP(id: string) {
  try { unwrap(await HTTPServeDelete(id)); await loadHTTPServers(); toast.success(t('deleted')) }
  catch (e: any) { toast.error(getErrorMessage(e)) }
}
function openHTTP(s: HTTPServerItem) {
  window.open('http://localhost:' + s.port + '/', '_blank')
}

async function load() {
  try {
    const list = unwrap<RuntimeInfo[]>(await EnvList())
    if (list) {
      runtimes.value = list
      list.forEach((r) => stateFor(r.id, r))
      // 侧边栏 harness 入口可要求自动定位到 harness 区块（优先于默认选中第一个运行时）
      if (props.initialSection) selectedId.value = props.initialSection
      if (!selectedId.value) selectedId.value = list[0]?.id || ''
      // 自动拉取当前选中运行时的可下载版本，避免用户必须手动点「获取版本」
      // （harness / http 不是运行时，跳过版本拉取）
      if (selectedId.value && selectedId.value !== HARNESS_KEY && selectedId.value !== HTTP_KEY) loadAvailable(selectedId.value)
    }
  } catch (e: any) {
    toast.error(t('envLoadFailed') + ': ' + getErrorMessage(e))
  }
}

async function loadAvailable(id: string) {
  const s = ui[id]
  if (s.fetching) return
  s.fetching = true
  try {
    const vs = unwrap<string[]>(await EnvAvailableVersions(id))
    if (vs) {
      s.available = vs
      s.availLoaded = true
    }
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  } finally {
    s.fetching = false
  }
}

async function pollStatus() {
  for (const r of runtimes.value) {
    if (!r.hasService) continue
    const s = ui[r.id]
    if (!s) continue
    for (const ins of r.installed) {
      try {
        const st = unwrap<ServiceStatus>(await EnvStatus(r.id, ins.version))
        if (st) s.svc[ins.version] = st
      } catch {
        /* 忽略单次轮询失败 */
      }
    }
  }
}

function onProgress(payload: any) {
  const p = payload?.data ?? payload
  const rt = p?.runtime
  if (!rt || !ui[rt]) return
  const s = ui[rt]
  s.stage = p.stage || ''
  if (p.message) s.message = p.message
  if (typeof p.written === 'number') s.written = p.written
  if (typeof p.total === 'number') s.total = p.total
  if (p.stage === 'done') {
    s.installing = false
    s.written = 0
    s.total = -1
    s.message = ''
    toast.success(rt.toUpperCase() + ' ' + t('envInstalled'))
    load()
  } else if (p.stage === 'error') {
    s.installing = false
    s.written = 0
    s.total = -1
    toast.error(rt.toUpperCase() + ': ' + (p.message || t('envInstallFailed')))
  } else if (p.stage === 'download' || p.stage === 'extract') {
    s.installing = true
  }
}

async function install(r: RuntimeInfo) {
  const s = stateFor(r.id, r)
  if (s.installing) return
  s.installing = true
  s.stage = 'download'
  s.written = 0
  s.total = -1
  s.message = ''
  try {
    const custom = s.source === 'custom' ? s.custom : ''
    unwrap(await EnvInstall(r.id, s.version, s.source, custom))
  } catch (e: any) {
    s.installing = false
    toast.error(getErrorMessage(e))
  }
}

async function startService(r: RuntimeInfo, version: string) {
  try {
    unwrap(await EnvStart(r.id, version))
    toast.success(r.name + ' ' + version + ' ' + t('svcStart'))
    pollStatus()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

async function stopService(r: RuntimeInfo) {
  try {
    unwrap(await EnvStop(r.id))
    toast.success(r.name + ' ' + t('svcStop'))
    pollStatus()
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

let off: (() => void) | null = null
let timer: number | null = null
onMounted(() => {
  load()
  loadHTTPServers()
  off = Events.On('quickdock:env:progress', onProgress)
  timer = window.setInterval(pollStatus, 3000)
  pollStatus()
})
// 切换运行时时自动拉取对应可下载版本列表（harness / http 区块不触发）
watch(selectedId, (id) => {
  if (id && id !== HARNESS_KEY && id !== HTTP_KEY) loadAvailable(id)
})
// 侧边栏 harness 入口要求切换区块时同步定位
watch(() => props.initialSection, (v) => {
  if (v) selectedId.value = v
})
onUnmounted(() => {
  if (off) off()
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div class="env-page">
    <div class="env-header">
      <div>
        <h2 class="env-title">{{ t('navEnvironments') }}</h2>
        <p class="env-sub">{{ t('envSubtitle') }}</p>
      </div>
      <button class="env-refresh" @click="load" :title="t('refresh')">⟳</button>
    </div>

    <div class="env-body">
      <!-- 左侧：分组导航 -->
      <aside class="env-cats">
        <div class="cats-title">{{ t('envCategories') }}</div>
        <template v-for="grp in sidebarGroups" :key="grp.key">
          <div class="cats-group-label">{{ t(grp.labelKey) }}</div>
          <button
            v-for="it in grp.items"
            :key="it.id"
            class="cat-btn"
            :class="{ active: it.active }"
            @click="selectedId = it.id; openMenu = null"
          >
            <span class="cat-avatar" :style="{ background: it.color }">{{ it.avatar }}</span>
            <span class="cat-meta">
              <span class="cat-name">{{ it.name }}</span>
              <span class="cat-id">{{ it.id }}</span>
            </span>
            <span class="cat-trail">
              <span v-if="it.running" class="cat-dot" :title="t('svcRunning')"></span>
              <span v-if="it.count !== undefined" class="cat-count" :class="{ zero: !it.count }">{{ it.count }}</span>
            </span>
          </button>
        </template>
      </aside>

      <!-- 右侧：所选分类详情 -->
      <main class="env-detail">
        <!-- DeepSeek Harness 区块：复用 SettingsDSH 组件 -->
        <SettingsDSH v-if="isHarness" :visible="isHarness" />

        <!-- HTTP 服务区块：目录 → 可访问的静态服务 -->
        <template v-else-if="isHttp">
          <header class="detail-head">
            <span class="detail-avatar" style="background:#4a9eff">⬡</span>
            <div class="detail-titles">
              <div class="detail-title-row">
                <span class="detail-name">{{ t('httpServe') }}</span>
                <span class="detail-id">httpserve</span>
              </div>
              <div class="detail-badges"><span class="badge svc">{{ t('httpServeDesc') }}</span></div>
            </div>
          </header>

          <section class="detail-block">
            <div class="block-head">
              <span class="block-title">{{ t('httpServers') }}</span>
              <span class="block-count">{{ httpServers.length }}</span>
            </div>
            <div v-if="httpServers.length" class="ver-table">
              <div class="ver-row ver-head">
                <span class="col-ver">{{ t('httpSrvName') }}</span>
                <span class="col-path">{{ t('httpDir') }}</span>
                <span class="col-env">{{ t('httpPort') }}</span>
                <span class="col-ops">{{ t('operations') }}</span>
              </div>
              <div v-for="s in httpServers" :key="s.id" class="ver-row">
                <div class="col-ver"><span class="ver-ver">{{ s.name }}</span></div>
                <div class="col-path" :title="s.dir">{{ s.dir }}</div>
                <div class="col-env">
                  <span class="env-badge" :class="httpRunning[s.id] ? 'on' : 'off'">{{ httpRunning[s.id] ? t('httpRunning') : t('httpStopped') }}</span>
                  <span class="svc-port">http://localhost:{{ s.port }}</span>
                </div>
                <div class="col-ops">
                  <button v-if="!httpRunning[s.id]" class="op-btn" @click="startHTTP(s.id)">{{ t('httpStart') }}</button>
                  <button v-else class="op-btn" @click="openHTTP(s)">{{ t('httpOpen') }}</button>
                  <button v-if="httpRunning[s.id]" class="op-btn danger" @click="stopHTTP(s.id)">{{ t('httpStop') }}</button>
                  <button class="op-btn danger" @click="deleteHTTP(s.id)">{{ t('deleteVersion') }}</button>
                </div>
              </div>
            </div>
            <div v-else class="empty-state">
              <div class="empty-icon">∅</div>
              <div class="empty-text">{{ t('httpNoServer') }}</div>
            </div>
          </section>

          <section class="detail-block install-card">
            <div class="block-head"><span class="block-title">{{ t('httpAdd') }}</span></div>
            <div class="install-grid">
              <input v-model="httpForm.name" class="env-input" :placeholder="t('httpNamePlaceholder')" />
              <button class="link-btn" @click="pickHTTPDir">{{ t('httpChooseDir') }}</button>
              <input v-model="httpForm.dir" class="env-input" :placeholder="t('httpDirPlaceholder')" readonly />
              <input v-model.number="httpForm.port" type="number" class="env-input" style="max-width:90px" :placeholder="t('httpPort')" />
              <button class="env-install-btn" @click="createHTTPServer">{{ t('httpCreate') }}</button>
            </div>
          </section>
        </template>

        <template v-else-if="selected">
        <header class="detail-head">
          <span class="detail-avatar" :style="{ background: avatarColor(selected.id) }">{{ avatarText(selected.id) }}</span>
          <div class="detail-titles">
            <div class="detail-title-row">
              <span class="detail-name">{{ selected.name }}</span>
              <span class="detail-id">{{ selected.id }}</span>
            </div>
            <div class="detail-badges">
              <span v-if="selected.hasService" class="badge svc">{{ t('svcManage') }}</span>
              <span v-for="p in selected.platforms" :key="p" class="badge plat">{{ p }}</span>
            </div>
          </div>
        </header>

        <!-- 已下载 / 已安装版本 -->
        <section class="detail-block">
          <div class="block-head">
            <span class="block-title">{{ t('installedVersions') }}</span>
            <span class="block-count">{{ selected.installed.length }}</span>
            <button class="link-btn import-btn" @click="importExisting(selected)">{{ t('importExisting') }}</button>
          </div>

          <div v-if="selected.installed.length" class="ver-table">
            <div class="ver-row ver-head">
              <span class="col-ver">{{ t('version') }}</span>
              <span class="col-path">{{ t('installPath') }}</span>
              <span class="col-note">{{ t('envNote') }}</span>
              <span class="col-env">{{ t('envVar') }}</span>
              <span class="col-svc" v-if="selected.hasService">{{ t('svcStatus') }}</span>
              <span class="col-ops">{{ t('operations') }}</span>
            </div>
            <div
              v-for="ins in selected.installed"
              :key="ins.version"
              class="ver-row"
              :class="[ins.scope, { active: ins.active }]"
            >
              <div class="col-ver">
                <div class="ver-name">
                  <span v-if="ins.alias" class="ver-alias">{{ ins.alias }}</span>
                  <span class="ver-ver">{{ ins.version }}</span>
                </div>
                <span class="ver-scope" :class="ins.scope">
                  {{ ins.scope === 'system' ? t('scopeSystem') : t('scopePortable') }}
                </span>
              </div>
              <div class="col-path" :title="ins.path">{{ ins.path }}</div>
              <div class="col-note" :title="ins.note">{{ ins.note || '—' }}</div>
              <div class="col-env">
                <span v-if="ins.inSystemPath" class="env-badge on" :title="t('envVarSetTitle')">{{ t('envVarSet') }}</span>
                <span v-else class="env-badge off" :title="t('envVarUnsetTitle')">{{ t('envVarUnset') }}</span>
              </div>
              <div class="col-svc" v-if="selected.hasService && ins.scope !== 'linked'">
                <span class="status" :class="{ on: svcOn(selected, ins.version) }">
                  <span class="status-dot"></span>
                  {{ svcOn(selected, ins.version) ? t('svcRunning') : t('svcStopped') }}
                </span>
                <button
                  v-if="!svcOn(selected, ins.version)"
                  class="svc-btn start"
                  @click="startService(selected, ins.version)"
                >{{ t('svcStart') }}</button>
                <button v-else class="svc-btn stop" @click="stopService(selected)">{{ t('svcStop') }}</button>
                <span v-if="svcPort(selected, ins.version)" class="svc-port">{{ t('svcPort') }}: {{ svcPort(selected, ins.version) }}</span>
              </div>
              <div class="col-ops">
                <button class="op-btn menu-trigger" @click.stop="toggleMenu(ins.version)">
                  {{ t('operations') }}<span class="caret">▾</span>
                </button>
                <div v-if="openMenu === ins.version" class="op-menu">
                  <button class="op-item" :class="{ on: ins.inSystemPath }" @click="toggleEnvVar(selected, ins); openMenu = null">
                    {{ ins.inSystemPath ? t('unsetEnvVar') : t('setEnvVar') }}
                  </button>
                  <button class="op-item" @click="openAlias(selected, ins); openMenu = null">{{ t('setAlias') }}</button>
                  <template v-if="selected.id === 'php' && ins.scope !== 'system'">
                    <div class="op-sep"></div>
                    <button class="op-item" @click="openPHPConfig(selected, ins, 'ini'); openMenu = null">{{ t('phpIni') }}</button>
                    <button class="op-item" @click="openPHPConfig(selected, ins, 'disable'); openMenu = null">{{ t('phpDisableFunctions') }}</button>
                    <button class="op-item" @click="openPHPConfig(selected, ins, 'errorlog'); openMenu = null">{{ t('phpErrorLog') }}</button>
                    <button class="op-item" @click="openPHPConfig(selected, ins, 'ext'); openMenu = null">{{ t('phpExtensions') }}</button>
                  </template>
                  <div class="op-sep"></div>
                  <button class="op-item danger" :disabled="ins.scope === 'system'" @click="askDelete(selected, ins)">
                    {{ t('deleteVersion') }}
                  </button>
                </div>
              </div>
            </div>
          </div>

          <div v-else class="empty-state">
            <div class="empty-icon">∅</div>
            <div class="empty-text">{{ t('noInstalledVersion') }}</div>
            <div class="empty-hint">{{ t('installHint') }}</div>
          </div>
        </section>

        <!-- 安装新版本 -->
        <section class="detail-block install-card">
          <div class="block-head">
            <span class="block-title">{{ t('installNewVersion') }}</span>
          </div>
          <div class="install-grid">
            <div class="install-input-wrap">
              <input
                :list="'verlist-' + selected.id"
                v-model="stateFor(selected.id, selected).version"
                class="env-input"
                :placeholder="t('availableVersions')"
                :disabled="stateFor(selected.id, selected).installing"
              />
              <datalist :id="'verlist-' + selected.id">
                <option v-for="v in stateFor(selected.id, selected).available" :key="v" :value="v" />
              </datalist>
            </div>
            <button
              class="link-btn"
              :disabled="stateFor(selected.id, selected).fetching"
              @click="loadAvailable(selected.id)"
            >{{ t('fetchVersions') }}</button>
            <select
              v-model="stateFor(selected.id, selected).source"
              class="env-select"
              :disabled="stateFor(selected.id, selected).installing"
            >
              <option v-for="s in selected.sources" :key="s.id" :value="s.id">{{ s.name }}</option>
            </select>
            <button
              class="env-install-btn"
              :class="{ busy: stateFor(selected.id, selected).installing }"
              :disabled="stateFor(selected.id, selected).installing"
              @click="install(selected)"
            >{{ stateFor(selected.id, selected).installing ? t('installing') : t('install') }}</button>
          </div>

          <input
            v-if="stateFor(selected.id, selected).source === 'custom'"
            v-model="stateFor(selected.id, selected).custom"
            class="env-input custom"
            :placeholder="t('customSourcePlaceholder')"
            :disabled="stateFor(selected.id, selected).installing"
          />

          <div v-if="stateFor(selected.id, selected).installing" class="env-progress">
            <div class="env-progress-bar">
              <div
                class="env-progress-fill"
                :class="{ indeterminate: percent(stateFor(selected.id, selected)) < 0 }"
                :style="{ width: (percent(stateFor(selected.id, selected)) >= 0 ? percent(stateFor(selected.id, selected)) + '%' : '40%') }"
              ></div>
            </div>
            <div class="env-progress-text">
              <span>{{ stateFor(selected.id, selected).stage }}</span>
              <span v-if="percent(stateFor(selected.id, selected)) >= 0">{{ percent(stateFor(selected.id, selected)) }}%</span>
              <span v-else-if="stateFor(selected.id, selected).written > 0">{{ (stateFor(selected.id, selected).written / 1048576).toFixed(1) }} MB</span>
            </div>
          </div>
          <p v-else-if="stateFor(selected.id, selected).message" class="env-msg">{{ stateFor(selected.id, selected).message }}</p>
        </section>
        </template>

        <div v-else class="empty-state">
          <div class="empty-icon">∅</div>
          <div class="empty-text">{{ t('noInstalledVersion') }}</div>
        </div>
      </main>
    </div>

    <!-- 别名 / 备注编辑弹窗 -->
    <div v-if="editing.open" class="modal-overlay" @click.self="editing.open = false">
      <div class="modal">
        <div class="modal-title">{{ t('editAliasNote') }} · {{ editing.version }}</div>
        <label class="modal-label">{{ t('envAlias') }}</label>
        <input v-model="editing.alias" class="env-input" :placeholder="t('aliasPlaceholder')" />
        <label class="modal-label">{{ t('envNote') }}</label>
        <textarea v-model="editing.note" class="env-textarea" :placeholder="t('envNotePlaceholder')" rows="3"></textarea>
        <div class="modal-actions">
          <button class="op-btn" @click="editing.open = false">{{ t('cancel') }}</button>
          <button class="env-install-btn" @click="saveAlias">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <!-- 删除确认弹窗 -->
    <div v-if="confirmDelete.open" class="modal-overlay" @click.self="confirmDelete.open = false">
      <div class="modal">
        <div class="modal-title">{{ t('confirmDeleteTitle') }}</div>
        <p class="modal-text">{{ t('confirmDeleteVersion', { version: confirmDelete.version }) }}</p>
        <div class="modal-actions">
          <button class="op-btn" @click="confirmDelete.open = false">{{ t('cancel') }}</button>
          <button class="env-install-btn danger" @click="doDelete">{{ t('deleteVersion') }}</button>
        </div>
      </div>
    </div>

    <!-- PHP 配置弹窗：php.ini / 禁用函数 / 错误日志 / 扩展 -->
    <div v-if="phpModal.open" class="modal-overlay" @click.self="phpModal.open = false">
      <div class="modal php-modal">
        <div class="modal-title">{{ t('phpConfigTitle') }} · {{ phpModal.version }}</div>
        <div class="php-tabs">
          <button class="php-tab" :class="{ active: phpModal.tab === 'ini' }" @click="phpModal.tab = 'ini'">{{ t('phpIni') }}</button>
          <button class="php-tab" :class="{ active: phpModal.tab === 'disable' }" @click="phpModal.tab = 'disable'">{{ t('phpDisableFunctions') }}</button>
          <button class="php-tab" :class="{ active: phpModal.tab === 'errorlog' }" @click="phpModal.tab = 'errorlog'">{{ t('phpErrorLog') }}</button>
          <button class="php-tab" :class="{ active: phpModal.tab === 'ext' }" @click="phpModal.tab = 'ext'">{{ t('phpExtensions') }}</button>
        </div>
        <div class="php-tab-body">
          <template v-if="phpModal.tab === 'ini'">
            <label class="modal-label">{{ t('phpIni') }}（{{ t('phpIniHint') }}）</label>
            <textarea v-model="phpModal.raw" class="env-textarea php-ini" rows="12"></textarea>
          </template>
          <template v-else-if="phpModal.tab === 'disable'">
            <label class="modal-label">{{ t('phpDisableFunctions') }}</label>
            <textarea v-model="phpModal.disableFunctions" class="env-textarea" rows="4" :placeholder="t('phpDisablePlaceholder')"></textarea>
          </template>
          <template v-else-if="phpModal.tab === 'errorlog'">
            <label class="modal-label">{{ t('phpErrorLog') }}</label>
            <input v-model="phpModal.errorLog" class="env-input" :placeholder="t('phpErrorLogPlaceholder')" />
            <label class="modal-label">{{ t('phpErrorLogContent') }}</label>
            <pre class="php-log">{{ phpModal.errorLogContent || t('phpNoLog') }}</pre>
          </template>
          <template v-else>
            <label class="modal-label">{{ t('phpExtensions') }}</label>
            <div class="ext-list">
              <label v-for="(e, i) in phpModal.extensions" :key="e.name" class="ext-row">
                <input type="checkbox" :checked="e.enabled" @change="toggleExt(i)" />
                <span class="ext-name">{{ e.name }}</span>
                <span class="ext-file">{{ e.file }}</span>
              </label>
              <div v-if="!phpModal.extensions.length" class="empty-text">{{ t('phpNoExt') }}</div>
            </div>
          </template>
        </div>
        <div class="modal-actions">
          <button class="op-btn" @click="phpModal.open = false">{{ t('cancel') }}</button>
          <button class="env-install-btn" @click="savePHPConfig">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <!-- 操作下拉点击外部关闭 -->
    <div v-if="openMenu" class="menu-backdrop" @click="openMenu = null"></div>
  </div>
</template>

<style scoped>
.env-page {
  flex: 1;
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
  background: var(--color-bg-primary);
}
.env-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px 12px;
  box-shadow: var(--shadow-border);
  flex-shrink: 0;
}
.env-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.env-sub { margin: 2px 0 0; font-size: 12px; color: var(--color-text-muted); }
.env-refresh {
  width: 30px; height: 30px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-secondary); cursor: pointer; font-size: 15px;
}
.env-refresh:hover { color: var(--color-accent); border-color: var(--color-border-focus); }

.env-body { flex: 1; min-height: 0; display: flex; }

/* 左侧分类导航 */
.env-cats {
  width: 196px; flex-shrink: 0;
  border-right: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
  overflow-y: auto; padding: 12px 10px;
  display: flex; flex-direction: column; gap: 4px;
}
.cats-title {
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px;
  color: var(--color-text-disabled); padding: 2px 8px 8px;
}
.cat-btn {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 10px; border-radius: var(--radius-sm);
  border: 1px solid transparent; background: transparent;
  color: var(--color-text-secondary); cursor: pointer;
  font-family: inherit; font-size: 13px; text-align: left; width: 100%;
  transition: background var(--transition-fast), border-color var(--transition-fast);
}
.cat-btn:hover { background: var(--color-bg-tertiary); color: var(--color-text-primary); }
.cat-btn.active {
  background: var(--color-accent-bg);
  border-color: var(--color-border-focus);
  color: var(--color-text-primary);
}
.cat-avatar {
  width: 28px; height: 28px; border-radius: 7px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 13px; font-weight: 700; color: #fff;
}
.cat-meta { display: flex; flex-direction: column; min-width: 0; line-height: 1.25; }
.cat-name { font-weight: 600; font-size: 13px; }
.cat-id { font-size: 10px; color: var(--color-text-disabled); }
.cat-trail { margin-left: auto; display: flex; align-items: center; gap: 6px; }
.cat-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--color-success); box-shadow: 0 0 5px rgba(76, 175, 80, 0.6); flex-shrink: 0; }
.cat-count {
  min-width: 20px; text-align: center; font-size: 11px; padding: 1px 6px; border-radius: 10px;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}
.cat-count.zero { color: var(--color-text-disabled); }
.cats-sep { height: 1px; background: var(--color-border); margin: 8px 6px; flex-shrink: 0; }
.harness-avatar { background: var(--color-accent); }

/* 右侧详情 */
.env-detail {
  flex: 1; min-width: 0; overflow-y: auto;
  padding: 18px 22px 28px;
  display: flex; flex-direction: column; gap: 18px;
}
.detail-head { display: flex; align-items: center; gap: 12px; flex-shrink: 0; }
.detail-avatar {
  width: 40px; height: 40px; border-radius: 9px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 18px; font-weight: 700; color: #fff;
}
.detail-titles { display: flex; flex-direction: column; gap: 5px; }
.detail-title-row { display: flex; align-items: center; gap: 8px; }
.detail-name { font-size: 18px; font-weight: 600; color: var(--color-text-primary); }
.detail-id {
  font-size: 11px; color: var(--color-text-disabled);
  background: var(--color-bg-tertiary); padding: 1px 6px; border-radius: 4px;
}
.detail-badges { display: flex; align-items: center; gap: 6px; }
.badge { font-size: 11px; padding: 1px 7px; border-radius: 10px; background: var(--color-bg-tertiary); color: var(--color-text-muted); }
.badge.svc { background: var(--color-accent-bg); color: var(--color-accent); }

.detail-block {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  padding: 14px 16px;
}
.install-card { background: transparent; border-style: dashed; }
.block-head { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.block-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.block-count {
  min-width: 20px; text-align: center; font-size: 11px; padding: 1px 7px; border-radius: 10px;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}

/* 版本表 */
.ver-table { display: flex; flex-direction: column; gap: 6px; }
.ver-row {
  display: grid;
  grid-template-columns: 1.4fr 1.6fr 1.2fr 0.9fr 1.4fr auto;
  align-items: center; gap: 10px;
  padding: 9px 12px;
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-left: 3px solid var(--color-border);
  border-radius: var(--radius-sm);
  transition: border-color var(--transition-fast), background var(--transition-fast);
}
.ver-row.ver-head {
  background: transparent; border: none; border-left: none;
  padding: 0 12px 4px; font-size: 11px; color: var(--color-text-disabled);
  text-transform: uppercase; letter-spacing: 0.4px;
}
.ver-row:not(.ver-head):hover { background: var(--color-bg-secondary); }
.ver-row.portable { border-left-color: var(--color-accent); }
.ver-row.system { border-left-color: var(--color-text-disabled); }
.ver-row.active { border-left-color: var(--color-success); }

.col-ver { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.ver-name { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
.ver-alias { font-size: 13px; font-weight: 600; color: var(--color-accent); white-space: nowrap; }
.ver-ver { font-size: 12px; color: var(--color-text-secondary); font-family: monospace; }
.ver-scope { font-size: 10px; padding: 1px 7px; border-radius: 10px; width: fit-content; }
.ver-scope.portable { background: var(--color-accent-bg); color: var(--color-accent); }
.ver-scope.system { background: rgba(124, 129, 139, 0.18); color: var(--color-text-secondary); }
.col-path, .col-note {
  font-size: 11px; color: var(--color-text-disabled);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-family: monospace;
}
.col-note { font-family: inherit; }
.env-badge { font-size: 11px; padding: 2px 8px; border-radius: 10px; width: fit-content; }
.env-badge.on { background: rgba(76, 175, 80, 0.15); color: var(--color-success); }
.env-badge.off { background: var(--color-bg-primary); color: var(--color-text-disabled); }

.col-svc { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.status { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; color: var(--color-text-muted); }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--color-text-disabled); }
.status.on { color: var(--color-success); }
.status.on .status-dot { background: var(--color-success); box-shadow: 0 0 5px rgba(76, 175, 80, 0.6); }
.svc-btn {
  font-size: 11px; padding: 4px 12px; border-radius: 5px; border: none; cursor: pointer;
  font-family: inherit; color: #fff; transition: opacity var(--transition-fast);
}
.svc-btn.start { background: var(--color-success); }
.svc-btn.stop { background: var(--color-danger); }
.svc-btn:hover { opacity: 0.9; }
.svc-port { font-size: 11px; color: var(--color-text-disabled); }

.col-ops { position: relative; display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.op-btn {
  font-size: 11px; padding: 4px 10px; border-radius: 5px; cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary);
  font-family: inherit; transition: background var(--transition-fast), color var(--transition-fast);
  white-space: nowrap;
}
.op-btn:hover { background: var(--color-bg-primary); color: var(--color-text-primary); }
.op-btn.on { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-border-focus); }
.op-btn.danger:hover { background: rgba(216, 44, 32, 0.12); color: var(--color-danger); border-color: var(--color-danger); }
.op-btn:disabled { opacity: 0.4; cursor: default; }

/* 操作列下拉菜单 */
.menu-trigger { display: inline-flex; align-items: center; gap: 4px; }
.caret { font-size: 9px; opacity: 0.7; }
.op-menu {
  position: absolute; z-index: 30; min-width: 132px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); padding: 4px; margin-top: 4px;
  box-shadow: 0 8px 28px rgba(0, 0, 0, 0.45);
  display: flex; flex-direction: column; gap: 2px;
}
.op-item {
  text-align: left; font-size: 12px; padding: 6px 10px; border-radius: 5px;
  border: none; background: transparent; color: var(--color-text-secondary);
  font-family: inherit; cursor: pointer; white-space: nowrap;
}
.op-item:hover { background: var(--color-bg-tertiary); color: var(--color-text-primary); }
.op-item.on { color: var(--color-accent); }
.op-item.danger:hover { background: rgba(216, 44, 32, 0.12); color: var(--color-danger); }
.op-item:disabled { opacity: 0.4; cursor: default; }
.menu-backdrop { position: fixed; inset: 0; z-index: 20; }
.env-install-btn.danger { background: var(--color-danger); }
.env-install-btn.danger:hover { opacity: 0.9; }
.modal-text { font-size: 12px; color: var(--color-text-muted); margin: 6px 0 0; line-height: 1.5; }

.empty-state { display: flex; flex-direction: column; align-items: center; gap: 4px; padding: 26px 0; }
.empty-icon { font-size: 28px; color: var(--color-text-disabled); }
.empty-text { font-size: 13px; color: var(--color-text-muted); }
.empty-hint { font-size: 11px; color: var(--color-text-disabled); }

/* 安装区 */
.install-grid { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.install-input-wrap { flex: 1; min-width: 160px; }
.env-input, .env-select {
  width: 100%; min-width: 0;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-primary); border-radius: 6px; padding: 7px 9px; font-size: 12px;
  font-family: inherit; outline: none;
}
.env-input.custom { margin-top: 10px; }
.env-textarea {
  width: 100%; min-width: 0; resize: vertical;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  color: var(--color-text-primary); border-radius: 6px; padding: 7px 9px; font-size: 12px;
  font-family: inherit; outline: none; margin-top: 6px;
}
.env-select { width: auto; flex-shrink: 0; }
.env-select:focus, .env-input:focus, .env-textarea:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
.link-btn {
  flex-shrink: 0; font-size: 11px; padding: 7px 11px; border-radius: 6px; cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-accent);
  font-family: inherit; transition: background var(--transition-fast);
}
.link-btn:hover { background: var(--color-accent-bg); }
.link-btn:disabled { opacity: 0.5; cursor: default; }
.env-install-btn {
  flex-shrink: 0; padding: 8px 16px; border-radius: 6px; border: none;
  background: var(--color-accent); color: #fff; font-size: 13px; font-weight: 500;
  cursor: pointer; font-family: inherit; transition: opacity var(--transition-fast);
}
.env-install-btn:hover { opacity: 0.9; }
.env-install-btn.busy { opacity: 0.7; cursor: default; }

.env-progress { display: flex; flex-direction: column; gap: 5px; margin-top: 12px; }
.env-progress-bar { height: 6px; background: var(--color-bg-tertiary); border-radius: 3px; overflow: hidden; }
.env-progress-fill { height: 100%; background: var(--color-accent); border-radius: 3px; transition: width var(--transition-base); }
.env-progress-fill.indeterminate { animation: env-indet 1.1s ease-in-out infinite; }
@keyframes env-indet { 0% { margin-left: -40%; } 100% { margin-left: 100%; } }
.env-progress-text { display: flex; justify-content: space-between; font-size: 11px; color: var(--color-text-muted); }
.env-msg { font-size: 11px; color: var(--color-text-disabled); margin: 10px 0 0; word-break: break-all; }

/* 弹窗 */
.modal-overlay {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5);
  display: flex; align-items: center; justify-content: center; z-index: 50;
}
.modal {
  width: 360px; max-width: 90vw;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: var(--radius-lg); padding: 18px 20px;
  display: flex; flex-direction: column; gap: 6px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.4);
}
.modal-title { font-size: 14px; font-weight: 600; color: var(--color-text-primary); margin-bottom: 6px; }
.modal-label { font-size: 11px; color: var(--color-text-muted); margin-top: 8px; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }

/* 分组侧栏 */
.cats-group-label {
  font-size: 10px; text-transform: uppercase; letter-spacing: 0.6px;
  color: var(--color-text-disabled); padding: 10px 8px 4px;
}
.import-btn { margin-left: auto; font-size: 11px; padding: 3px 9px; }
.op-sep { height: 1px; background: var(--color-border); margin: 3px 0; }

/* PHP 配置弹窗 */
.php-modal { width: 540px; max-width: 94vw; }
.php-tabs { display: flex; gap: 4px; margin: 4px 0 10px; flex-wrap: wrap; }
.php-tab {
  font-size: 12px; padding: 5px 11px; border-radius: 6px; cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary);
  font-family: inherit;
}
.php-tab.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-border-focus); }
.php-tab-body { min-height: 200px; }
.php-ini { font-family: monospace; font-size: 11px; line-height: 1.5; }
.php-log {
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: 6px; padding: 8px 10px; font-size: 11px; max-height: 220px; overflow: auto;
  white-space: pre-wrap; word-break: break-all; color: var(--color-text-muted); margin: 0;
}
.ext-list { display: flex; flex-direction: column; gap: 2px; max-height: 300px; overflow: auto; }
.ext-row {
  display: flex; align-items: center; gap: 8px; font-size: 12px; padding: 5px 8px;
  border-radius: 5px; cursor: pointer; color: var(--color-text-secondary);
}
.ext-row:hover { background: var(--color-bg-tertiary); }
.ext-name { font-weight: 600; }
.ext-file { margin-left: auto; font-size: 10px; color: var(--color-text-disabled); font-family: monospace; }

</style>
