<script lang="ts">
// 模块级缓存：组件卸载/重挂载时预填上次的检测结果，避免「空 → 加载」的闪烁。
// 仅缓存只读检测结果，瞬态标志（installing/settingUp/updating）由组件实例自身复位。
let cachedDshStatus: any = null
let cachedDshSvc: any = null
</script>

<script setup lang="ts">
import { ref, computed, watch, inject, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Terminal, Download, ExternalLink, Plug, CheckCircle2, AlertCircle } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { DetectNodeEnv, SetupDSH, OpenDSHWindow, DSHInstallPlugin, CheckDSHUpdate, UpdateDSH, DSHStatus, DSHStart, DSHStop, DSHSetAutoStart, DSHUpdateAllPlugins, DSHRollbackPlugins, DSHCheckPluginUpdates, RevealInExplorer } from '../../bindings/quickdock/services/appservice'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ (e: 'goto', id: string): void }>()

interface NodeEnvStatus {
  nodeFound: boolean
  npxFound: boolean
  nodeSupport?: boolean
  nodeVersion: string
  npxVersion: string
  nodePath: string
  dshInstalled: boolean
  dshPath: string
  dshHome: string
  dshVersion?: string
  latestDshVersion?: string
  dshUpdateAvailable?: boolean
  installing: boolean
  message?: string
}

interface DshProgress {
  stage: string
  written: number
  total: number
  message: string
}

interface DSHStatusInfo {
  running: boolean
  autoStart: boolean
  port: number
  url: string
}

interface PluginUpdateInfo {
  name: string
  current: string
  latest: string
}

const status = ref<NodeEnvStatus | null>(cachedDshStatus)
const settingUp = ref(false)
const updating = ref(false)
const checkingUpdate = ref(false)
const dshSvc = ref<DSHStatusInfo | null>(cachedDshSvc)
const svcBusy = ref(false) // 启动/停止操作进行中
const progress = ref<DshProgress | null>(null)
const logs = ref<{ level: string; message: string }[]>([])
const logBox = ref<HTMLElement | null>(null)
const msg = ref('')
const msgError = ref(false)
let msgTimer: ReturnType<typeof setTimeout> | null = null

interface DshLog {
  level: string
  message: string
}

const dshReady = computed(() => !!status.value?.dshInstalled && !!status.value?.nodeFound && status.value?.nodeSupport !== false)

const pluginBtnTitle = computed(() => {
  if (!dshReady.value) return t('dshNeedInstall')
  if (installingPlugin.value) return t('dshPluginInstalling')
  return ''
})

const progressPercent = computed(() => {
  if (!progress.value || progress.value.total <= 0) return 0
  return Math.min(100, Math.round((progress.value.written / progress.value.total) * 100))
})

const stageText = computed(() => {
  const s = progress.value?.stage
  if (s === 'download-node') return t('dshDownloadingNode')
  if (s === 'extract-node') return t('dshExtractingNode')
  if (s === 'install-dsh') return t('dshInstalling')
  if (s === 'update-dsh') return t('dshUpdating')
  if (s === 'done') return t('dshReady')
  if (s === 'error') return t('dshError')
  return ''
})

function showMsg(text: string, isError = false) {
  msg.value = text
  msgError.value = isError
  if (msgTimer !== null) clearTimeout(msgTimer)
  msgTimer = setTimeout(() => { msg.value = ''; msgError.value = false }, 5000)
}

// 在文件资源管理器中打开路径（目录直接打开，文件高亮选中）。
async function reveal(path: string) {
  if (!path) return
  try {
    unwrap(await RevealInExplorer(path))
  } catch (e: any) {
    showMsg(getErrorMessage(e), true)
  }
}

async function refresh() {
  try {
    const res = unwrap<NodeEnvStatus>(await DetectNodeEnv())
    status.value = res
    cachedDshStatus = res
  } catch (e) {
    showMsg(getErrorMessage(e), true)
  }
}

async function checkUpdate() {
  if (checkingUpdate.value || !status.value?.dshInstalled || updating.value || settingUp.value) return
  checkingUpdate.value = true
  try {
    const res = unwrap<NodeEnvStatus>(await CheckDSHUpdate())
    if (res && status.value) status.value = { ...status.value, ...res }
  } catch (e) {
    // 检测失败（离线/超时）静默，不打断用户
  } finally {
    checkingUpdate.value = false
  }
}

async function updateDSH() {
  if (updating.value || settingUp.value) return
  updating.value = true
  progress.value = null
  logs.value = []
  try {
    unwrap(await UpdateDSH())
  } catch (e) {
    updating.value = false
    showMsg(getErrorMessage(e), true)
  }
}

async function setup() {
  if (settingUp.value || updating.value) return
  settingUp.value = true
  progress.value = null
  logs.value = []
  try {
    unwrap(await SetupDSH())
  } catch (e) {
    settingUp.value = false
    showMsg(getErrorMessage(e), true)
  }
}

async function openDSH() {
  try {
    const res = unwrap<{ url: string }>(await OpenDSHWindow())
    if (!res) return
  } catch (e: any) {
    showMsg(getErrorMessage(e), true)
  }
}

// —— dsh web 服务运行状态 / 开关 / 自动启动 ——

async function loadDSHStatus() {
  try {
    const res = unwrap<DSHStatusInfo | null>(await DSHStatus())
    if (res) { dshSvc.value = res; cachedDshSvc = res }
  } catch (e) {
    // 查询失败静默（如后端未就绪），不影响其余 UI
  }
}

async function toggleService(start: boolean) {
  if (svcBusy.value) return
  svcBusy.value = true
  try {
    if (start) {
      unwrap(await DSHStart())
    } else {
      unwrap(await DSHStop())
      // 即时反馈：先置为已停止，避免后端状态刷新（1.2s 后）前仍显示"运行中"
      if (dshSvc.value) dshSvc.value.running = false
    }
    showMsg(start ? t('dshServiceRunning') : t('dshServiceStopped'))
    // dsh 进程启动/停止是异步的，延迟刷新一次状态
    setTimeout(loadDSHStatus, start ? 2500 : 1200)
  } catch (e) {
    showMsg(getErrorMessage(e), true)
    loadDSHStatus()
  } finally {
    svcBusy.value = false
  }
}

async function setAutoStart(enabled: boolean) {
  try {
    unwrap(await DSHSetAutoStart(enabled))
    if (dshSvc.value) dshSvc.value.autoStart = enabled
  } catch (e: any) {
    showMsg(getErrorMessage(e), true)
    if (dshSvc.value) dshSvc.value.autoStart = !enabled // 回滚 UI
  }
}

const pluginName = ref('')
const installingPlugin = ref(false)
const updatingPlugins = ref(false) // 一键更新全部插件进行中
const checkingPluginUpdates = ref(false)
const pluginUpdates = ref<PluginUpdateInfo[]>([]) // 预检：有可用更新的 registry 插件
const pluginBackup = ref('') // 最近一次更新的备份路径（非空时回滚按钮可用）
async function installPlugin() {
  if (installingPlugin.value) return
  // 兼容粘贴完整命令（`dsh plugin --profile web add <name>`）：提取末尾插件名；
  // 输入为空时默认装 dshmarket
  let name = pluginName.value.trim()
  const addIdx = name.toLowerCase().lastIndexOf('add ')
  if (addIdx >= 0) name = name.slice(addIdx + 4).trim()
  name = name.split(/\s+/)[0] || ''
  if (!name) name = 'dshmarket'
  installingPlugin.value = true
  logs.value = []
  try {
    unwrap(await DSHInstallPlugin(name))
  } catch (e: any) {
    installingPlugin.value = false
    showMsg(getErrorMessage(e), true)
  }
}

// 预检：查 registry 插件（git 依赖不纳入）是否有可用更新
async function checkPluginUpdates() {
  if (checkingPluginUpdates.value || !dshReady.value) return
  checkingPluginUpdates.value = true
  try {
    const list = unwrap<PluginUpdateInfo[]>(await DSHCheckPluginUpdates())
    pluginUpdates.value = list || []
    if (pluginUpdates.value.length) {
      showMsg(t('dshPluginUpdatesAvailable', { n: pluginUpdates.value.length }))
    } else {
      showMsg(t('dshNoPluginUpdates'))
    }
  } catch (e: any) {
    showMsg(getErrorMessage(e), true)
  } finally {
    checkingPluginUpdates.value = false
  }
}

// 一键更新全部插件（保守：按 semver 范围升级，固定版不动；升级前自动备份）
async function updateAllPlugins() {
  if (updatingPlugins.value) return
  updatingPlugins.value = true
  pluginUpdates.value = []
  logs.value = []
  try {
    unwrap(await DSHUpdateAllPlugins())
  } catch (e: any) {
    updatingPlugins.value = false
    showMsg(getErrorMessage(e), true)
  }
}

// 一键回滚到最近一次更新前的备份
async function rollbackPlugins() {
  if (!pluginBackup.value || updatingPlugins.value) return
  updatingPlugins.value = true
  logs.value = []
  try {
    unwrap(await DSHRollbackPlugins())
  } catch (e: any) {
    updatingPlugins.value = false
    showMsg(getErrorMessage(e), true)
  }
}

async function copyLogs() {
  if (!logs.value.length) return
  const text = logs.value.map((l) => (l.level === 'error' ? '✗ ' : '› ') + l.message).join('\n')
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('copied'))
  } catch (e) {
    showMsg(getErrorMessage(e), true)
  }
}

let offProgress: (() => void) | null = null
let offLog: (() => void) | null = null
let offPlugin: (() => void) | null = null
let offRollback: (() => void) | null = null
onMounted(() => {
  offProgress = Events.On('quickdock:dsh:progress', (payload: any) => {
    const p = (payload?.data ?? payload) as DshProgress
    progress.value = p
    if (p.stage === 'done') {
      settingUp.value = false
      updating.value = false
      progress.value = null
      showMsg(t('dshReady'))
      // 更新完成后服务可能被自动重启（新版本生效），延迟刷新运行状态
      setTimeout(loadDSHStatus, 1200)
      refresh().then(() => { if (dshReady.value) checkUpdate() })
    } else if (p.stage === 'error') {
      settingUp.value = false
      updating.value = false
      showMsg(p.message || t('dshError'), true)
    }
  })
  offLog = Events.On('quickdock:dsh:log', (payload: any) => {
    const l = (payload?.data ?? payload) as DshLog
    logs.value.push(l)
    // 防止长时间运行的日志面板内存/DOM 无限增长：只保留最近 500 行
    if (logs.value.length > 500) logs.value.splice(0, logs.value.length - 500)
    nextTick(() => {
      if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
    })
  })
  offPlugin = Events.On('quickdock:dsh:plugin', (payload: any) => {
    const p = (payload?.data ?? payload) as { ok: boolean; backup?: string; kind?: string }
    if (p?.kind === 'update') {
      // 一键更新全部插件完成
      updatingPlugins.value = false
      if (p.ok && p.backup) pluginBackup.value = p.backup
      if (!p.ok) showMsg(t('dshPluginError'), true)
    } else {
      // 安装单个插件完成
      installingPlugin.value = false
      if (!p?.ok) showMsg(t('dshPluginError'), true)
    }
  })
  offRollback = Events.On('quickdock:dsh:plugin-rollback', (payload: any) => {
    const p = (payload?.data ?? payload) as { ok: boolean }
    updatingPlugins.value = false
    if (p?.ok) {
      pluginBackup.value = ''
      pluginUpdates.value = []
      showMsg(t('dshRollbackDone'))
    } else {
      showMsg(t('dshRollbackFailed'), true)
    }
  })
})
onUnmounted(() => { offProgress?.(); offLog?.(); offPlugin?.(); offRollback?.() })

let checkRanOnce = false
watch(() => props.visible, async (v) => {
  if (v) {
    // 重新进入本页时复位插件安装状态，避免上次卡死的事件遗漏导致按钮永久禁用
    installingPlugin.value = false
    updatingPlugins.value = false
    await refresh()
    loadDSHStatus()
    if (!checkRanOnce && dshReady.value) {
      checkRanOnce = true
      checkUpdate() // 进入页面静默自动检测一次新版本
    }
  }
}, { immediate: true })
</script>

<template>
  <div class="section">
    <h3 class="section-title"><Terminal :size="16" style="vertical-align:-3px;margin-right:6px" />{{ t('navDsh') }}</h3>
    <p class="section-desc">{{ t('dshDesc') }}</p>

    <!-- 状态卡（含检测到的安装位置） -->
    <div class="dsh-status">
      <div class="dsh-status-row">
        <div class="dsh-status-left">
          <span class="dsh-status-label">Node.js</span>
          <span v-if="status?.nodePath" class="dsh-status-path clickable" :title="'在资源管理器中打开: ' + status.nodePath" @click="reveal(status.nodePath)">{{ status.nodePath }}</span>
        </div>
        <span :class="['dsh-badge', { ok: status?.nodeFound }]">
          <component :is="status?.nodeFound ? CheckCircle2 : AlertCircle" :size="13" />
          {{ status?.nodeFound ? t('dshInstalled') : t('dshNotInstalled') }}
        </span>
      </div>
      <div class="dsh-status-row">
        <div class="dsh-status-left">
          <span class="dsh-status-label">npx</span>
          <span v-if="status?.nodePath && status?.npxFound" class="dsh-status-path">{{ t('dshSameAsNode') }}</span>
        </div>
        <span :class="['dsh-badge', { ok: status?.npxFound }]">
          <component :is="status?.npxFound ? CheckCircle2 : AlertCircle" :size="13" />
          {{ status?.npxFound ? t('dshInstalled') : t('dshNotInstalled') }}
        </span>
      </div>
      <div class="dsh-status-row">
        <div class="dsh-status-left">
          <span class="dsh-status-label">DeepSeek Harness</span>
          <span v-if="status?.dshPath" class="dsh-status-path clickable" :title="'在资源管理器中打开: ' + status.dshPath" @click="reveal(status.dshPath)">{{ status.dshPath }}</span>
          <span v-if="status?.dshHome" class="dsh-status-path dsh-status-home clickable" :title="'在资源管理器中打开: ' + status.dshHome" @click="reveal(status.dshHome)">DSH_HOME: {{ status.dshHome }}</span>
        </div>
        <span :class="['dsh-badge', { ok: status?.dshInstalled }]">
          <component :is="status?.dshInstalled ? CheckCircle2 : AlertCircle" :size="13" />
          {{ status?.dshInstalled ? (status.dshVersion ? 'v' + status.dshVersion : t('dshInstalled')) : t('dshNotInstalled') }}
        </span>
      </div>
    </div>

    <!-- Node 未安装：引导至 Node.js 分组安装 -->
    <p v-if="!status?.nodeFound" class="dsh-node-hint">
      {{ t('dshNodeInstallHint') }}
      <a href="#" class="dsh-goto-link" @click.prevent="emit('goto', 'node')">{{ t('dshGotoNode') }}</a>
    </p>

    <!-- 检测到的问题提示 -->
    <p v-if="status?.message" class="result-hint" style="margin-top:8px">{{ status.message }}</p>

    <!-- dsh web 服务运行状态 / 开关 / 自动启动（dsh 已安装时显示） -->
    <div v-if="status?.dshInstalled" class="dsh-status" style="margin-top:12px">
      <div class="dsh-status-row">
        <div class="dsh-status-left">
          <span class="dsh-status-label">{{ t('dshLatestVersion') }}: {{ status?.latestDshVersion || '—' }}</span>
          <span
            :class="['dsh-update-badge', { ok: dshSvc?.running }]"
            style="font-weight:600"
          >
            <span class="dsh-dot" :class="{ on: dshSvc?.running }" />
            {{ dshSvc && dshSvc.running ? t('dshServiceRunning') : t('dshServiceStopped') }}
          </span>
          <span v-if="dshSvc?.running && dshSvc.url" class="dsh-status-path">{{ dshSvc.url }}</span>
        </div>
        <div class="action-row" style="flex-shrink:0">
          <button
            v-if="dshSvc && dshSvc.running"
            class="btn btn-secondary"
            :disabled="svcBusy || settingUp || updating"
            @click="toggleService(false)"
          >
            {{ svcBusy ? t('dshServiceStopping') : t('dshServiceStop') }}
          </button>
          <button
            v-else
            class="btn btn-secondary"
            :disabled="svcBusy || settingUp || updating || !dshReady"
            @click="toggleService(true)"
          >
            {{ svcBusy ? t('dshServiceStarting') : t('dshServiceStart') }}
          </button>
        </div>
      </div>
      <label class="dsh-auto-start" :title="t('dshAutoStartHint')">
        <input
          type="checkbox"
          :checked="!!dshSvc?.autoStart"
          @change="(e: any) => setAutoStart(!!e.target.checked)"
        />
        <span>{{ t('dshAutoStart') }}</span>
      </label>
    </div>

    <!-- 版本更新（dsh 已安装时显示） -->
    <div v-if="status?.dshInstalled" class="update-row" style="margin-top:12px">
      <div class="action-row">
        <button class="btn btn-secondary" :disabled="checkingUpdate || updating || settingUp" @click="checkUpdate">
          {{ checkingUpdate ? t('dshCheckingUpdate') : t('dshCheckUpdate') }}
        </button>
        <button v-if="status?.dshUpdateAvailable" class="btn btn-primary" :disabled="updating || settingUp" @click="updateDSH">
          <Download :size="14" />
          {{ updating ? t('dshUpdating') : t('dshUpdateNow') }}
        </button>
      </div>
    </div>

    <!-- 一键安装 / 打开 -->
    <div class="action-row" style="margin-top:16px">
      <button class="btn btn-primary" :disabled="settingUp || dshReady" @click="setup">
        <Download :size="14" />
        {{ settingUp ? (stageText || t('dshSettingUp')) : (dshReady ? t('dshInstalled') : t('dshInstall')) }}
      </button>
      <button class="btn btn-secondary" :disabled="!dshReady" @click="openDSH">
        <ExternalLink :size="14" />
        {{ t('dshOpen') }}
      </button>
    </div>

    <!-- 安装插件：输入插件名 -->
    <div class="plugin-row" style="margin-top:12px">
      <input
        v-model="pluginName"
        class="plugin-input"
        :placeholder="t('dshPluginPlaceholder')"
        :disabled="!dshReady || installingPlugin"
        @keyup.enter="installPlugin"
      />
      <button class="btn btn-secondary" :disabled="!dshReady || installingPlugin" @click="installPlugin" :title="pluginBtnTitle">
        <Plug :size="14" />
        {{ installingPlugin ? t('dshPluginInstalling') : t('dshInstallPlugin') }}
      </button>
    </div>

    <!-- 插件批量管理：检查更新 / 一键更新全部 / 回滚 -->
    <div class="plugin-batch" style="margin-top:12px" v-if="status?.dshInstalled">
      <div class="action-row">
        <button class="btn btn-secondary" :disabled="!dshReady || checkingPluginUpdates || updatingPlugins" @click="checkPluginUpdates">
          {{ checkingPluginUpdates ? t('dshCheckingPluginUpdates') : t('dshCheckPluginUpdates') }}
        </button>
        <button class="btn btn-primary" :disabled="!dshReady || updatingPlugins" @click="updateAllPlugins">
          <Download :size="14" />
          {{ updatingPlugins ? t('dshUpdatingPlugins') : t('dshUpdateAllPlugins') }}
        </button>
        <button class="btn btn-secondary" :disabled="!dshReady || updatingPlugins || !pluginBackup" @click="rollbackPlugins" :title="pluginBackup || ''">
          {{ t('dshRollback') }}
        </button>
      </div>
      <p class="dsh-update-hint">{{ t('dshUpdateAllHint') }}</p>
      <ul v-if="pluginUpdates.length" class="plugin-update-list">
        <li v-for="u in pluginUpdates" :key="u.name">
          <span class="pu-name">{{ u.name }}</span>
          <span class="pu-ver">{{ u.current }} → <b>{{ u.latest }}</b></span>
        </li>
      </ul>
    </div>

    <!-- 进度 -->
    <div v-if="(settingUp || updating) && progress?.stage === 'download-node'" class="dsh-progress">
      <div class="dsh-progress-bar"><div class="dsh-progress-fill" :style="{ width: progressPercent + '%' }" /></div>
      <span class="dsh-progress-text">{{ stageText }} {{ progressPercent }}%</span>
    </div>
    <p v-else-if="(settingUp || updating) && stageText" class="dsh-progress-text">{{ stageText }}</p>

    <!-- 实时日志 -->
    <div v-if="logs.length" class="dsh-log-wrap">
      <div class="dsh-log-head">
        <span>{{ t('dshLogTitle') }}</span>
        <button class="dsh-log-copy" @click="copyLogs">{{ t('copy') }}</button>
      </div>
      <div ref="logBox" class="dsh-log">
        <div v-for="(l, i) in logs" :key="i" :class="['dsh-log-line', l.level]">{{ l.message }}</div>
      </div>
    </div>

    <p v-if="msg" :class="['result-hint', { error: msgError }]">{{ msg }}</p>

    <p class="ai-hint">{{ t('dshHint') }}</p>
  </div>
</template>

<style scoped>
.dsh-status {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 4px;
}
.dsh-status-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-tertiary);
}
.dsh-status-left {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.dsh-status-label {
  font-size: 13px;
  color: var(--color-text-primary);
}
.dsh-status-path {
  font-size: 11px;
  color: var(--color-text-disabled);
  word-break: break-all;
  line-height: 1.4;
}
.dsh-status-path.clickable {
  cursor: pointer;
  color: var(--color-text-secondary);
}
.dsh-status-path.clickable:hover {
  color: var(--color-accent);
  text-decoration: underline;
}
.dsh-status-home {
  color: var(--color-text-muted);
}
.dsh-node-hint {
  margin-top: 10px;
  font-size: 12px;
  color: #ffb020;
  line-height: 1.5;
}
.dsh-goto-link {
  color: var(--color-accent);
  text-decoration: none;
  margin-left: 4px;
  cursor: pointer;
}
.dsh-goto-link:hover {
  text-decoration: underline;
}
.dsh-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--color-text-disabled);
}
.dsh-badge.ok {
  color: var(--color-accent);
}
.dsh-update-badge {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 8px;
  border-radius: 10px;
  font-size: 11px;
  background: rgba(255, 176, 32, 0.16);
  color: #ffb020;
}
.dsh-update-badge.ok {
  background: rgba(76, 175, 80, 0.16);
  color: #4caf50;
}
.dsh-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  margin-right: 5px;
  vertical-align: 1px;
  background: #8b919c;
}
.dsh-dot.on {
  background: #4caf50;
  box-shadow: 0 0 6px rgba(76, 175, 80, 0.8);
}
.dsh-auto-start {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--color-text-muted);
  cursor: pointer;
  user-select: none;
}
.dsh-auto-start input {
  accent-color: var(--color-accent);
  cursor: pointer;
}
.plugin-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.plugin-input {
  flex: 1;
  min-width: 0;
  padding: 7px 10px;
  border: 1px solid var(--color-border);
  border-radius: 6px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 12px;
  outline: none;
}
.plugin-input:focus {
  border-color: var(--color-accent);
}
.plugin-input:disabled {
  opacity: 0.5;
}
.plugin-batch {
  padding-top: 10px;
  border-top: 1px dashed var(--color-border);
}
.dsh-update-hint {
  margin: 8px 0 0;
  font-size: 11px;
  color: var(--color-text-muted);
  line-height: 1.5;
}
.plugin-update-list {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.plugin-update-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 5px 8px;
  border-radius: 6px;
  background: var(--color-bg-tertiary);
  font-size: 12px;
}
.pu-name {
  color: var(--color-text-primary);
  word-break: break-all;
}
.pu-ver {
  color: var(--color-text-muted);
  white-space: nowrap;
}
.pu-ver b {
  color: var(--color-accent);
}
.dsh-progress {
  margin-top: 12px;
}
.dsh-progress-bar {
  height: 6px;
  border-radius: 3px;
  background: var(--color-bg-active);
  overflow: hidden;
}
.dsh-progress-fill {
  height: 100%;
  background: var(--color-accent);
  transition: width 0.2s ease;
}
.dsh-progress-text {
  display: block;
  margin-top: 6px;
  font-size: 12px;
  color: var(--color-text-muted);
}
.dsh-log-wrap {
  margin-top: 12px;
}
.dsh-log-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
  font-size: 12px;
  color: var(--color-text-muted);
}
.dsh-log-copy {
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  color: var(--color-text-primary);
  font-size: 11px;
  padding: 2px 10px;
  cursor: pointer;
}
.dsh-log-copy:hover {
  border-color: var(--color-accent);
}
.dsh-log {
  max-height: 200px;
  overflow-y: auto;
  padding: 10px 12px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: #0d0f12;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 11px;
  line-height: 1.6;
  user-select: text;
  -webkit-user-select: text;
  cursor: text;
}
.dsh-log-line {
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--color-text-muted);
}
.dsh-log-line::before {
  content: '› ';
  color: var(--color-text-disabled);
}
.dsh-log-line.error {
  color: #ff6b6b;
}
.dsh-log-line.error::before {
  content: '✗ ';
  color: #ff6b6b;
}
</style>
