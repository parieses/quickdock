<script setup lang="ts">
import { ref, computed, watch, inject, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Terminal, Download, ExternalLink, Plug, CheckCircle2, AlertCircle } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { DetectNodeEnv, SetupDSH, OpenDSHWindow, DSHInstallPlugin, CheckDSHUpdate, UpdateDSH } from '../../bindings/quickdock/services/appservice'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const props = defineProps<{ visible: boolean }>()

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

const status = ref<NodeEnvStatus | null>(null)
const settingUp = ref(false)
const updating = ref(false)
const checkingUpdate = ref(false)
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

async function refresh() {
  try {
    const res = unwrap<NodeEnvStatus>(await DetectNodeEnv())
    status.value = res
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

const pluginName = ref('')
const installingPlugin = ref(false)
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
onMounted(() => {
  offProgress = Events.On('quickdock:dsh:progress', (payload: any) => {
    const p = (payload?.data ?? payload) as DshProgress
    progress.value = p
    if (p.stage === 'done') {
      settingUp.value = false
      updating.value = false
      progress.value = null
      showMsg(t('dshReady'))
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
    const p = (payload?.data ?? payload) as { ok: boolean }
    installingPlugin.value = false
    if (!p?.ok) showMsg(t('dshPluginError'), true)
  })
})
onUnmounted(() => { offProgress?.(); offLog?.(); offPlugin?.() })

let checkRanOnce = false
watch(() => props.visible, async (v) => {
  if (v) {
    // 重新进入本页时复位插件安装状态，避免上次卡死的事件遗漏导致按钮永久禁用
    installingPlugin.value = false
    await refresh()
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
          <span v-if="status?.nodePath" class="dsh-status-path">{{ status.nodePath }}</span>
        </div>
        <span :class="['dsh-badge', { ok: status?.nodeFound }]">
          <component :is="status?.nodeFound ? CheckCircle2 : AlertCircle" :size="13" />
          {{ status?.nodeFound ? (status.nodeVersion || t('dshInstalled')) : t('dshNotInstalled') }}
        </span>
      </div>
      <div class="dsh-status-row">
        <div class="dsh-status-left">
          <span class="dsh-status-label">npx</span>
          <span v-if="status?.nodePath && status?.npxFound" class="dsh-status-path">{{ t('dshSameAsNode') }}</span>
        </div>
        <span :class="['dsh-badge', { ok: status?.npxFound }]">
          <component :is="status?.npxFound ? CheckCircle2 : AlertCircle" :size="13" />
          {{ status?.npxFound ? (status.npxVersion || t('dshInstalled')) : t('dshNotInstalled') }}
        </span>
      </div>
      <div class="dsh-status-row">
        <div class="dsh-status-left">
          <span class="dsh-status-label">DeepSeek Harness</span>
          <span v-if="status?.dshPath" class="dsh-status-path">{{ status.dshPath }}</span>
          <span v-if="status?.dshHome" class="dsh-status-path dsh-status-home">DSH_HOME: {{ status.dshHome }}</span>
        </div>
        <span :class="['dsh-badge', { ok: status?.dshInstalled }]">
          <component :is="status?.dshInstalled ? CheckCircle2 : AlertCircle" :size="13" />
          {{ status?.dshInstalled ? (status.dshVersion ? 'v' + status.dshVersion : t('dshInstalled')) : t('dshNotInstalled') }}
        </span>
      </div>
    </div>

    <!-- 检测到的问题提示 -->
    <p v-if="status?.message" class="result-hint" style="margin-top:8px">{{ status.message }}</p>

    <!-- 版本与更新（dsh 已安装时显示） -->
    <div v-if="status?.dshInstalled" class="update-row" style="margin-top:12px">
      <div class="dsh-status-left" style="flex:1; min-width:0">
        <span class="dsh-status-label">{{ t('dshLatestVersion') }}: {{ status?.latestDshVersion || '—' }}</span>
        <span v-if="status?.dshUpdateAvailable" class="dsh-update-badge">{{ t('dshUpdateNew') }}</span>
        <span v-else-if="status?.latestDshVersion" class="dsh-update-badge ok">{{ t('dshUpToDate') }}</span>
      </div>
      <div class="action-row" style="margin-top:8px">
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
.dsh-status-home {
  color: var(--color-text-muted);
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
