<script setup lang="ts">
/**
 * 本地建站向导（四步）：目录 → 证书 → 服务 → 完成。
 *
 * 重要事实：HTTPServeStart 是纯 HTTP 静态服务（Go 侧 net.Listen + http.FileServer），
 * 它不读证书。证书步骤只是复用 mkcert 的能力签一批本地可信证书，供别的用途
 * （本地 HTTPS 站点 / nginx 反代等）使用——走完向导得到的是 http://localhost:<port>/，
 * 不是 https。文案里必须把这件事说清楚（siteWizardHttpNote），不能让人误以为
 * 签了证书就得到了 HTTPS。
 *
 * 状态全部在本组件内部（由父级 v-if 控制挂载），只把 created 抛回去让宿主刷新列表。
 */
import { computed, inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink, FolderOpen, Loader2 } from '@lucide/vue'
import QdModal from './QdModal.vue'
import { CreateDirectory } from '../../bindings/quickdock/services/system/systemservice'
import {
  HTTPServeCreate,
  HTTPServeList,
  HTTPServeStart,
} from '../../bindings/quickdock/services/appservice'
import { PickFolderPath } from '../../bindings/quickdock/services/plugin/pluginservice'
import { useCertPanel } from '../composables/useCertPanel'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { openExternal } from '../utils/externalLinks'

import { reportError } from '../utils/reportError'
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'created'): void
}>()
const { t } = useI18n()
const toast = inject<{ error: (m: string) => void; success: (m: string) => void }>('toast')!

interface WizardServer { id: string; name: string; dir: string; port: number; running: boolean }

const step = ref(1)
const busy = ref(false)
const dir = ref('')
const name = ref('')
const port = ref(0)
const serverId = ref('')
const serverPort = ref(0)
const started = ref(false)
const err = ref('')

const steps = computed(() => [
  t('siteWizardStepDir'),
  t('siteWizardStepCert'),
  t('siteWizardStepService'),
  t('siteWizardStepDone'),
])

// 证书区块直接复用 useCertPanel 的状态与三个后端动作（status / installRoot / issue）。
// 它原本按 selectedId === 'mkcert' 决定是否自动加载，这里进入证书步骤时显式调
// loadCertStatus()，不依赖那个门槛；输出目录同步成向导里填的站点目录。
const certSelected = ref('mkcert')
const cert = useCertPanel(certSelected)
watch(dir, (v) => { cert.certModal.outDir = v.trim() }, { immediate: true })

async function pickDir() {
  try {
    const picked = unwrap<string | null>(await PickFolderPath(t('pickDirTitle')))
    if (picked) dir.value = picked
  } catch (e) {
    reportError(e, 'siteWizard.pickDir')
  }
}

// 步骤 1 → 2：用户允许填尚不存在的路径，这里先建目录再往下走。
async function nextFromDir() {
  const d = dir.value.trim()
  if (!d) { toast.error(t('siteWizardNeedDir')); return }
  if (busy.value) return
  busy.value = true
  err.value = ''
  try {
    unwrap(await CreateDirectory(d))
    cert.certModal.outDir = d
    step.value = 2
    await cert.loadCertStatus()
  } catch (e) {
    logErr('siteWizard.createDir', e)
    err.value = getErrorMessage(e)
    toast.error(t('siteWizardCreateDirFailed') + ': ' + getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

function nextFromCert() {
  step.value = 3
  err.value = ''
}

// 步骤 3 → 4：创建服务。port=0 由后端自动分配，返回值与列表都核对一次拿真实端口。
async function nextFromService() {
  if (busy.value) return
  const d = dir.value.trim()
  if (!d) { toast.error(t('siteWizardNeedDir')); step.value = 1; return }
  busy.value = true
  err.value = ''
  try {
    const srv = unwrap<WizardServer | null>(
      await HTTPServeCreate(name.value.trim() || d, d, Number(port.value) || 0),
    )
    if (!srv?.id) throw new Error(t('siteWizardCreateFailed'))
    serverId.value = srv.id
    serverPort.value = srv.port || 0
    if (!serverPort.value) await refreshPort()
    step.value = 4
    emit('created')
  } catch (e) {
    logErr('siteWizard.createService', e)
    err.value = getErrorMessage(e)
    reportError(e, 'SiteWizardDialog.nextFromService')
  } finally {
    busy.value = false
  }
}

async function refreshPort() {
  try {
    const list = unwrap<WizardServer[]>(await HTTPServeList())
    const found = list?.find((s) => s.id === serverId.value)
    if (found?.port) serverPort.value = found.port
  } catch (e) {
    logErr('siteWizard.refreshPort', e)
  }
}

// 步骤 4：启动。未启动时地址先展示、打开按钮禁用；失败可原地重试。
async function startServer() {
  if (busy.value || !serverId.value) return
  busy.value = true
  err.value = ''
  try {
    unwrap(await HTTPServeStart(serverId.value))
    started.value = true
    await refreshPort()
    toast.success(t('httpStarted'))
  } catch (e) {
    logErr('siteWizard.startService', e)
    err.value = getErrorMessage(e)
    reportError(e, 'SiteWizardDialog.startServer')
  } finally {
    busy.value = false
  }
}

async function openSite() {
  if (!serverPort.value) return
  try {
    await openExternal('http://localhost:' + serverPort.value + '/')
  } catch (e) {
    reportError(e, 'siteWizard.openSite')
  }
}
</script>

<template>
  <QdModal
    :model-value="true"
    :title="t('siteWizardTitle')"
    width="560px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <!-- 步骤指示：当前步高亮，已走过的步淡化，并显示 第 n / 4 步 -->
    <div class="wiz-steps">
      <span
        v-for="(s, i) in steps"
        :key="s"
        class="wiz-step"
        :class="{ on: step === i + 1, past: step > i + 1 }"
      >{{ i + 1 }}. {{ s }}</span>
    </div>
    <div class="wiz-progress"><div class="wiz-progress-fill" :style="{ width: (step / 4 * 100) + '%' }" /></div>
    <div class="wiz-step-label">{{ t('siteWizardStepProgress', { n: step, label: steps[step - 1] }) }}</div>

    <!-- 步骤 1：目录（允许尚不存在的路径，下一步自动创建） -->
    <template v-if="step === 1">
      <p class="wiz-hint">{{ t('siteWizardDirTip') }}</p>
      <label class="wiz-label">{{ t('httpDir') }}</label>
      <div class="wiz-row">
        <input v-model="dir" class="qd-input wiz-grow" :placeholder="t('siteWizardDirPh')" />
        <button class="qd-btn" :disabled="busy" @click="pickDir">
          <FolderOpen :size="13" />{{ t('pickDirTitle') }}
        </button>
      </div>
    </template>

    <!-- 步骤 2：证书（复用 useCertPanel 的状态与签发动作） -->
    <template v-else-if="step === 2">
      <p class="wiz-hint">{{ t('siteWizardHttpNote') }}</p>
      <div v-if="!cert.certModal.status?.exe" class="wiz-msg error">
        {{ cert.certModal.status?.message || t('certExeMissing') }}
      </div>
      <template v-else>
        <div class="wiz-row wiz-root-line">
          <span>{{ t('certRootStatus') }}</span>
          <span :class="cert.certModal.status?.rootTrusted ? 'wiz-badge ok' : 'wiz-badge warn'">
            {{ cert.certModal.status?.rootTrusted ? t('certRootTrusted') : t('certRootUntrusted') }}
          </span>
          <button
            v-if="!cert.certModal.status?.rootTrusted"
            class="qd-btn qd-btn-sm"
            :disabled="cert.certModal.busy"
            @click="cert.certInstallRoot()"
          >{{ cert.certModal.busy ? t('certTrusting') : t('certTrustBtn') }}</button>
        </div>
        <label class="wiz-label">{{ t('certHosts') }}</label>
        <input v-model="cert.certModal.hosts" class="qd-input" :placeholder="t('certHostsPh')" />
        <label class="wiz-label">{{ t('certName') }}</label>
        <input v-model="cert.certModal.name" class="qd-input" placeholder="localhost" />
        <div class="wiz-label">{{ t('certOutDir') }}：{{ cert.certModal.outDir || dir }}</div>
        <button class="qd-btn" :disabled="cert.certModal.busy" @click="cert.certIssue()">
          <Loader2 v-if="cert.certModal.busy" :size="13" class="is-spinning" />
          {{ cert.certModal.busy ? t('certIssuing') : t('certIssueBtn') }}
        </button>
        <textarea
          v-if="cert.certModal.result"
          readonly
          class="qd-input wiz-result"
          rows="3"
          :value="cert.certModal.result"
        />
      </template>
      <p v-if="cert.certModal.error" class="wiz-msg error">{{ cert.certModal.error }}</p>
      <p class="wiz-hint">{{ t('siteWizardCertOptional') }}</p>
    </template>

    <!-- 步骤 3：服务名 + 端口（0 = 自动分配） -->
    <template v-else-if="step === 3">
      <p class="wiz-hint">{{ t('siteWizardHttpNote') }}</p>
      <label class="wiz-label">{{ t('httpSrvName') }}</label>
      <input v-model="name" class="qd-input" :placeholder="t('httpNamePlaceholder')" />
      <label class="wiz-label">{{ t('httpPort') }}</label>
      <input v-model.number="port" type="number" min="0" max="65535" class="qd-input" :placeholder="t('siteWizardPortAuto')" />
      <p class="wiz-hint">{{ t('siteWizardDir') }}：{{ dir }}</p>
    </template>

    <!-- 步骤 4：完成（启动纯 HTTP 静态服务并给出访问地址） -->
    <template v-else>
      <div class="wiz-done-title">{{ t('siteWizardDone') }}</div>
      <p class="wiz-hint">{{ t('siteWizardHttpNote') }}</p>
      <div class="wiz-row wiz-address">
        <span class="wiz-label">{{ t('siteWizardAddress') }}</span>
        <code class="wiz-url">http://localhost:{{ serverPort || (port || 0) }}/</code>
      </div>
      <button class="qd-btn qd-btn-primary" :disabled="busy || started || !serverId" @click="startServer">
        <Loader2 v-if="busy" :size="13" class="is-spinning" />
        {{ started ? t('siteWizardStarted') : t('siteWizardStart') }}
      </button>
      <button class="qd-btn" :disabled="!started || !serverPort" @click="openSite">
        <ExternalLink :size="13" />{{ t('open') }}
      </button>
    </template>

    <p v-if="err" class="wiz-msg error">{{ err }}</p>

    <template #footer>
      <button class="qd-btn" @click="emit('update:modelValue', false)">{{ t('close') }}</button>
      <button v-if="step > 1 && step < 4" class="qd-btn" :disabled="busy" @click="step--">
        {{ t('back') }}
      </button>
      <button v-if="step === 1" class="qd-btn qd-btn-primary" :disabled="busy" @click="nextFromDir">
        <Loader2 v-if="busy" :size="13" class="is-spinning" />{{ t('siteWizardNext') }}
      </button>
      <button v-else-if="step === 2" class="qd-btn qd-btn-primary" @click="nextFromCert">
        {{ t('siteWizardCertNext') }}
      </button>
      <button v-else-if="step === 3" class="qd-btn qd-btn-primary" :disabled="busy" @click="nextFromService">
        <Loader2 v-if="busy" :size="13" class="is-spinning" />{{ t('siteWizardNext') }}
      </button>
    </template>
  </QdModal>
</template>

<style scoped>
.wiz-steps { display: flex; flex-wrap: wrap; gap: var(--space-4); margin-bottom: var(--space-3); }
.wiz-step { font-size: var(--text-xs); color: var(--color-text-muted); }
.wiz-step.on { color: var(--color-accent-ink); font-weight: 600; }
.wiz-step.past { color: var(--color-text-secondary); }
.wiz-progress { height: 4px; border-radius: var(--radius-2xs); background: var(--color-bg-active); overflow: hidden; }
.wiz-progress-fill { height: 100%; background: var(--color-accent); transition: width var(--transition-base); }
.wiz-step-label { margin-top: var(--space-2); font-size: var(--text-xs); color: var(--color-text-secondary); }
.wiz-hint { margin: var(--space-4) 0; font-size: var(--text-sm); line-height: 1.6; color: var(--color-text-secondary); }
.wiz-label { display: block; margin: var(--space-4) 0 var(--space-2); font-size: var(--text-sm); color: var(--color-text-secondary); }
.wiz-row { display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
.wiz-grow { flex: 1; min-width: 0; }
.wiz-root-line { padding: var(--space-3) 0; }
.wiz-badge { padding: 0 var(--space-2); border-radius: var(--radius-xs); font-size: var(--text-xs); }
.wiz-badge.ok { color: var(--color-success); background: var(--color-success-bg); }
.wiz-badge.warn { color: var(--color-warning); background: var(--color-warning-bg); }
.wiz-result { margin-top: var(--space-3); font-family: var(--font-mono); font-size: var(--text-xs); resize: vertical; }
.wiz-msg { margin: var(--space-3) 0; padding: var(--space-3) var(--space-4); border-radius: var(--radius-sm); font-size: var(--text-sm); line-height: 1.5; }
.wiz-msg.error { color: var(--color-danger); background: var(--color-danger-bg); border: 1px solid var(--color-danger-border); }
.wiz-done-title { font-size: var(--text-base); font-weight: 600; color: var(--color-text-primary); }
.wiz-address { margin-bottom: var(--space-4); }
.wiz-url { padding: var(--space-2) var(--space-3); border-radius: var(--radius-xs); background: var(--color-bg-active); color: var(--color-text-primary); font-family: var(--font-mono); font-size: var(--text-sm); word-break: break-all; }
</style>
