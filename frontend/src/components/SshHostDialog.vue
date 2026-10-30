<script lang="ts" setup>
/**
 * SSH 主机新建/编辑表单。
 *
 * 设计取舍：
 *   - 保存与测试连接都在本组件内完成，父组件只接收 emitted('saved') 后刷新列表。
 *     表单字段多，抛给父组件维护会多一层状态同步，还容易漏掉 sort/parentId 这类
 *     「不该改但必须原样带回去」的字段。
 *   - 「测试连接」测的是表单当前的值，不要求先保存：新建主机填错密码/端口时，
 *     不该被迫先存一台连不上的机器再回头改（后端 TestHostInput 接受未落库的表单值）。
 *     但也不会偷偷保存——测试通过与否都不改动已存配置，用户点「取消」时一切照旧。
 */
import { computed, reactive, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, EyeOff, Loader2, X } from '@lucide/vue'
import { SaveHost, TestHostInput } from '../../bindings/quickdock/services/sshx/sshservice'
import { apiErrorCode, apiErrorData, unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useFocusTrap } from '../utils/focusTrap'
import { useToast } from '../composables/useToast'
import {
  SSH_ERR,
  sshErrorText,
  type SshHost,
  type SshHostInput,
  type SshHostKeyInfo,
  type TestHostResult,
} from '../utils/ssh'
import SshFingerprintDialog from './SshFingerprintDialog.vue'

const { t } = useI18n()
// 只取 success：失败文案可能很长（认证方式不匹配、指纹异常等），
// toast 是 white-space: nowrap 的单行容器，长句会撑出屏幕，
// 所以失败仍走行内结果条 —— 现在它已钉在按钮栏上方，不会再被滚动吞掉。
const { success: toastSuccess } = useToast()

const props = defineProps<{
  visible: boolean
  /** 传入表示编辑，为空表示新建 */
  host?: SshHost | null
  /**
   * 新建时的所属分组（#13）。
   *
   * 从树上某个文件夹的「+」进来时带上；为空表示建在根。
   * 编辑已有主机时忽略——分组由拖拽调整，不在表单里改。
   */
  parentId?: string
}>()

const emit = defineEmits<{
  (e: 'saved'): void
  (e: 'cancel'): void
}>()

interface FormState {
  name: string
  host: string
  port: string
  user: string
  authType: 'password' | 'key'
  password: string
  clearPassword: boolean
  keyPath: string
  keyPassphrase: string
  legacyAlgorithms: boolean
  env: string
  remark: string
}

const form = reactive<FormState>(blankForm())
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

const saving = ref(false)
const testing = ref(false)
const formErr = ref('')
const testErr = ref('')
const testOk = ref('')
const showPassword = ref(false)
const showPassphrase = ref(false)

// ---- 指纹确认（测试连接时可能触发） ----
const fpVisible = ref(false)
const fpMode = ref<'unknown' | 'changed'>('unknown')
const fpInfo = ref<SshHostKeyInfo>({})

const isEdit = computed(() => !!props.host?.id)
const hasPassword = computed(() => !!props.host?.hasPassword)
const hasPassphrase = computed(() => !!props.host?.hasPassphrase)

function blankForm(): FormState {
  return {
    name: '',
    host: '',
    port: '22',
    user: 'root',
    authType: 'password',
    password: '',
    clearPassword: false,
    keyPath: '',
    keyPassphrase: '',
    legacyAlgorithms: false,
    env: 'test',
    remark: '',
  }
}

function fillFromHost(h: SshHost | null | undefined) {
  const next = blankForm()
  if (h) {
    next.name = h.name ?? ''
    next.host = h.host ?? ''
    next.port = String(h.port || 22)
    next.user = h.user || 'root'
    next.authType = h.authType === 'key' ? 'key' : 'password'
    next.keyPath = h.keyPath ?? ''
    next.legacyAlgorithms = !!h.legacyAlgorithms
    next.env = h.env || 'test'
    next.remark = h.remark ?? ''
  }
  Object.assign(form, next)
  saving.value = false
  testing.value = false
  formErr.value = ''
  testErr.value = ''
  testOk.value = ''
  showPassword.value = false
  showPassphrase.value = false
}

// 只在「打开」这个边沿重置：父组件重渲染会带来新的 props.host 引用，
// 若把它放进依赖，用户正在输入的密码会被清空。
watch(
  () => props.visible,
  (v) => {
    if (v) fillFromHost(props.host)
  },
  { immediate: true },
)

/**
 * 挡住「测试连接」的原因，空串表示可以测。
 *
 * 只作为禁用按钮的 title 提示，不挂到结果区——刚打开一个空白表单就红一句
 * 「请填写主机地址」太吵，用户还没开始填。
 */
const testBlocker = computed(() => {
  const bad = validate()
  if (bad) return bad
  // 编辑态密码留空是「沿用库里已存的」；新建或勾了「清除密码」时留空才是真没凭据。
  if (form.authType === 'password' && !form.password && (!props.host || form.clearPassword)) {
    return t('sshPasswordRequired')
  }
  return ''
})

const canTest = computed(() => !testBlocker.value && !saving.value && !testing.value)

function validate(): string {
  if (!form.host.trim()) return t('sshHostRequired')
  const p = Number(form.port)
  if (!Number.isInteger(p) || p < 1 || p > 65535) return t('sshPortInvalid')
  if (form.authType === 'key' && !form.keyPath.trim()) return t('sshKeyPathRequired')
  return ''
}

function toInput(): SshHostInput {
  const h = props.host
  return {
    id: h?.id ?? '',
    name: form.name.trim(),
    host: form.host.trim(),
    port: Number(form.port) || 22,
    user: form.user.trim() || 'root',
    authType: form.authType,
    password: form.password,
    clearPassword: form.clearPassword,
    keyPath: form.keyPath.trim(),
    keyPassphrase: form.keyPassphrase,
    legacyAlgorithms: form.legacyAlgorithms,
    env: form.env,
    // parentId / sort / isFolder 本表单不编辑，但必须原样带回：
    // 漏掉 sort 会让这台主机在列表里的排序被重置成 0，看起来像「自己跳到了最前面」。
    // 新建时 parentId 取自 props（从哪个文件夹点「+」进来的），
    // sort 传 0 让后端分配到末尾。
    parentId: props.host ? (props.host.parentId ?? '') : (props.parentId ?? ''),
    isFolder: props.host?.isFolder ?? false,
    sort: props.host?.sort ?? 0,
    remark: form.remark.trim(),
  }
}

async function save() {
  if (saving.value) return
  const bad = validate()
  if (bad) {
    formErr.value = bad
    return
  }
  saving.value = true
  formErr.value = ''
  try {
    unwrap(await SaveHost(toInput()))
    emit('saved')
  } catch (e) {
    formErr.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

function openFingerprint(mode: 'unknown' | 'changed', data: SshHostKeyInfo) {
  fpMode.value = mode
  fpInfo.value = {
    host: data.host || form.host.trim(),
    port: data.port || Number(form.port) || 22,
    keyType: data.keyType || '',
    fingerprint: data.newFingerprint || data.fingerprint || '',
    oldFingerprint: data.oldFingerprint || '',
    knownTypes: data.knownTypes || [],
  }
  fpVisible.value = true
}

async function testConn() {
  if (!canTest.value) return
  testing.value = true
  testErr.value = ''
  testOk.value = ''
  try {
    // 传表单值而不是 id：新建的主机库里还没有记录，后端也能拿同一套逻辑去连。
    const r = unwrap<TestHostResult>(await TestHostInput(toInput()))
    if (!r) return
    if (r.needsTrust) {
      // 连通了，但服务器身份还没被信任：这不是失败，弹窗让用户核对指纹。
      // host/port 取表单值：新建时 props.host 为空，用库里的会把待信任的
      // 指纹记到一台不存在（或另一台）的机器名下。
      openFingerprint('unknown', {
        host: form.host.trim(),
        port: Number(form.port) || 22,
        keyType: r.hostKeyAlgorithm,
        fingerprint: r.fingerprint,
        knownTypes: r.knownTypes,
      })
      return
    }
    testOk.value = t('sshTestOk', { version: r.serverVersion || '-', ms: r.latencyMs })
    // 结果行原本在滚动区底部，表单一长就看不见，用户会以为按钮没生效。
    // 成功是「一次性的确认」，弹到视口顶部最省事；行内那份留着，方便回看
    // 服务器版本和延迟（3 秒后 toast 就没了，这些信息用户还想再瞄一眼）。
    toastSuccess(testOk.value)
  } catch (e) {
    const code = apiErrorCode(e)
    if (code === SSH_ERR.HOSTKEY_CHANGED) {
      openFingerprint('changed', apiErrorData<SshHostKeyInfo>(e) || {})
      return
    }
    if (code === SSH_ERR.HOSTKEY_UNKNOWN) {
      openFingerprint('unknown', apiErrorData<SshHostKeyInfo>(e) || {})
      return
    }
    testErr.value = sshErrorText(e, code, t)
  } finally {
    testing.value = false
  }
}

/** 信任指纹后自动重测一次，用户不用再点一遍。 */
function onTrusted() {
  fpVisible.value = false
  void testConn()
}

function onKeydown(e: KeyboardEvent) {
  // 指纹弹窗开着时，事件属于它，别再让表单吞掉 Esc
  if (fpVisible.value) return
  if (e.key === 'Escape') {
    emit('cancel')
    return
  }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="hd">
      <div v-if="visible" class="hd-overlay" @mousedown.self="emit('cancel')" @keydown="onKeydown">
        <div ref="panelRef" class="hd-panel" role="dialog" @mousedown.stop>
          <header class="hd-head">
            <span class="hd-title">{{ isEdit ? t('sshEditHost') : t('sshNewHost') }}</span>
            <button class="hd-x" :title="t('cancel')" @click="emit('cancel')">
              <X :size="16" />
            </button>
          </header>

          <div class="hd-body">
            <!-- 基本信息 -->
            <div class="hd-grid hd-grid--2">
              <label class="fd">
                <span class="fd-label">{{ t('sshName') }}</span>
                <input v-model="form.name" class="fd-input" :placeholder="t('sshNamePlaceholder')" />
              </label>
              <div class="fd">
                <span class="fd-label">{{ t('sshEnvLabel') }}</span>
                <div class="seg">
                  <button
                    v-for="e in ['prod', 'staging', 'test']"
                    :key="e"
                    type="button"
                    class="seg-btn"
                    :class="[`seg-${e}`, { on: form.env === e }]"
                    @click="form.env = e"
                  >
                    {{ t(e === 'prod' ? 'sshEnvProd' : e === 'staging' ? 'sshEnvStaging' : 'sshEnvTest') }}
                  </button>
                </div>
              </div>
            </div>

            <div class="hd-grid hd-grid--addr">
              <label class="fd">
                <span class="fd-label">{{ t('sshHostAddr') }}</span>
                <input
                  v-model="form.host"
                  class="fd-input"
                  spellcheck="false"
                  :placeholder="t('sshHostAddrPlaceholder')"
                />
              </label>
              <label class="fd">
                <span class="fd-label">{{ t('sshPort') }}</span>
                <input v-model="form.port" class="fd-input" inputmode="numeric" placeholder="22" />
              </label>
              <label class="fd">
                <span class="fd-label">{{ t('sshUser') }}</span>
                <input v-model="form.user" class="fd-input" spellcheck="false" placeholder="root" />
              </label>
            </div>

            <!-- 认证 -->
            <div class="hd-section">
              <div class="seg seg--wide">
                <button
                  type="button"
                  class="seg-btn"
                  :class="{ on: form.authType === 'password' }"
                  @click="form.authType = 'password'"
                >
                  {{ t('sshAuthPassword') }}
                </button>
                <button
                  type="button"
                  class="seg-btn"
                  :class="{ on: form.authType === 'key' }"
                  @click="form.authType = 'key'"
                >
                  {{ t('sshAuthKey') }}
                </button>
              </div>

              <template v-if="form.authType === 'password'">
                <label class="fd">
                  <span class="fd-label">{{ t('sshPassword') }}</span>
                  <span class="fd-secret">
                    <input
                      v-model="form.password"
                      class="fd-input"
                      :type="showPassword ? 'text' : 'password'"
                      autocomplete="new-password"
                      :placeholder="hasPassword ? t('sshSecretKeep') : t('sshPasswordPlaceholder')"
                    />
                    <button type="button" class="fd-eye" @click="showPassword = !showPassword">
                      <EyeOff v-if="showPassword" :size="14" />
                      <Eye v-else :size="14" />
                    </button>
                  </span>
                </label>
                <label v-if="hasPassword" class="fd-check">
                  <input v-model="form.clearPassword" type="checkbox" />
                  <span>{{ t('sshPasswordClear') }}</span>
                </label>
              </template>

              <template v-else>
                <label class="fd">
                  <span class="fd-label">{{ t('sshKeyPath') }}</span>
                  <input
                    v-model="form.keyPath"
                    class="fd-input"
                    spellcheck="false"
                    :placeholder="t('sshKeyPathPlaceholder')"
                  />
                </label>
                <label class="fd">
                  <span class="fd-label">{{ t('sshKeyPassphrase') }}</span>
                  <span class="fd-secret">
                    <input
                      v-model="form.keyPassphrase"
                      class="fd-input"
                      :type="showPassphrase ? 'text' : 'password'"
                      autocomplete="new-password"
                      :placeholder="hasPassphrase ? t('sshSecretKeep') : t('sshKeyPassphrasePlaceholder')"
                    />
                    <button type="button" class="fd-eye" @click="showPassphrase = !showPassphrase">
                      <EyeOff v-if="showPassphrase" :size="14" />
                      <Eye v-else :size="14" />
                    </button>
                  </span>
                </label>
              </template>

              <label class="fd-check">
                <input v-model="form.legacyAlgorithms" type="checkbox" />
                <span>
                  {{ t('sshLegacy') }}
                  <em class="fd-hint">{{ t('sshLegacyHint') }}</em>
                </span>
              </label>
            </div>

            <label class="fd">
              <span class="fd-label">{{ t('sshRemark') }}</span>
              <textarea v-model="form.remark" class="fd-input fd-area" rows="2" :placeholder="t('sshRemarkPlaceholder')" />
            </label>
          </div>

          <!-- 结果区：故意放在滚动区外，钉在按钮栏正上方。
               表单字段多时 .hd-body 会滚动，结果行跟着滚就会被推到看不见的底部，
               用户点完「测试连接」只看到按钮恢复原样，以为没生效。 -->
          <div v-if="formErr || testErr || testOk" class="hd-status">
            <p v-if="formErr" class="hd-msg hd-msg--err">{{ formErr }}</p>
            <p v-else-if="testErr" class="hd-msg hd-msg--err">{{ testErr }}</p>
            <p v-else-if="testOk" class="hd-msg hd-msg--ok">{{ testOk }}</p>
          </div>

          <footer class="hd-foot">
            <button class="btn btn-ghost" :disabled="saving" @click="emit('cancel')">{{ t('cancel') }}</button>
            <button class="btn btn-outline" :disabled="!canTest" :title="testBlocker" @click="testConn">
              <Loader2 v-if="testing" :size="13" class="spin" />
              <span>{{ testing ? t('sshTesting') : t('sshTest') }}</span>
            </button>
            <button class="btn btn-primary" :disabled="saving" @click="save">
              {{ saving ? t('sshSaving') : t('save') }}
            </button>
          </footer>
        </div>
      </div>
    </Transition>

    <SshFingerprintDialog
      :visible="fpVisible"
      :mode="fpMode"
      :host="fpInfo.host || ''"
      :port="fpInfo.port || 22"
      :key-type="fpInfo.keyType || ''"
      :fingerprint="fpInfo.fingerprint || ''"
      :old-fingerprint="fpInfo.oldFingerprint || ''"
      :known-types="fpInfo.knownTypes || []"
      @trusted="onTrusted"
      @cancel="fpVisible = false"
    />
  </Teleport>
</template>

<style scoped>
.hd-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

.hd-panel {
  width: 520px;
  max-width: 94vw;
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden;
}

.hd-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-6) var(--space-7);
  border-bottom: 1px solid var(--color-border);
}

.hd-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.hd-x {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.hd-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.hd-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-6) var(--space-7);
  overflow-y: auto;
}

.hd-grid {
  display: grid;
  gap: var(--space-5);
}

.hd-grid--2 {
  grid-template-columns: 1fr 200px;
}

/* 主机名占大头，端口窄、用户名适中 */
.hd-grid--addr {
  grid-template-columns: 1fr 84px 130px;
}

.fd {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-width: 0;
}

.fd-label {
  font-size: 11px;
  font-weight: 500;
  color: var(--color-text-muted);
}

.fd-input {
  width: 100%;
  box-sizing: border-box;
  padding: var(--space-3) var(--space-5);
  font-size: 13px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  outline: none;
  transition: border-color var(--transition-fast);
}

.fd-input:focus {
  border-color: var(--color-accent);
}

.fd-input::placeholder {
  color: var(--color-text-disabled);
}

.fd-area {
  resize: vertical;
  min-height: 48px;
  line-height: 1.5;
}

.fd-secret {
  position: relative;
  display: block;
}

.fd-secret .fd-input {
  padding-right: var(--space-9);
}

.fd-eye {
  position: absolute;
  top: 50%;
  right: var(--space-3);
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-muted);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  transform: translateY(-50%);
  cursor: pointer;
}

.fd-eye:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.fd-check {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-secondary);
  cursor: pointer;
}

.fd-check input {
  margin-top: 2px;
  accent-color: var(--color-accent);
  cursor: pointer;
}

.fd-hint {
  display: block;
  margin-top: var(--space-1);
  font-size: 11px;
  font-style: normal;
  color: var(--color-text-muted);
}

.hd-section {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-5) var(--space-6);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

/* 分段选择器：比 select 少一次点击，也避免下拉框在深色下样式跳变 */
.seg {
  display: flex;
  gap: 2px;
  padding: 2px;
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.seg--wide {
  align-self: flex-start;
}

.seg-btn {
  flex: 1 1 auto;
  padding: var(--space-2) var(--space-5);
  font-size: 12px;
  font-family: inherit;
  color: var(--color-text-muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast);
}

.seg-btn:hover {
  color: var(--color-text-primary);
}

.seg-btn.on {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

/* 选中态带环境色：一眼看出正在给生产机配连接 */
.seg-prod.on {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.seg-staging.on {
  color: var(--color-warning);
  background: var(--color-warning-bg);
}

.seg-test.on {
  color: var(--color-text-primary);
}

.hd-status {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-7);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.hd-msg {
  margin: 0;
  padding: var(--space-3) var(--space-5);
  font-size: 12px;
  line-height: 1.5;
  border-radius: var(--radius-xs);
  word-break: break-all;
}

.hd-msg--err {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.hd-msg--ok {
  color: var(--color-success);
  background: var(--color-success-bg);
}

.hd-msg--hint {
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
}

.hd-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-4);
  padding: var(--space-5) var(--space-7);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-6);
  font-size: 13px;
  font-weight: 500;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-active);
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background var(--transition-fast), opacity var(--transition-fast);
}

.btn:disabled {
  cursor: default;
  opacity: 0.45;
}

.btn-outline {
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
}

.btn-outline:hover:not(:disabled) {
  color: var(--color-text-primary);
  border-color: var(--color-border-light);
}

.spin {
  animation: hd-spin 900ms linear infinite;
}

@keyframes hd-spin {
  to {
    transform: rotate(360deg);
  }
}

.hd-enter-active {
  transition: opacity 0.16s ease-out;
}

.hd-leave-active {
  transition: opacity 0.12s ease-in;
}

.hd-enter-from,
.hd-leave-to {
  opacity: 0;
}
</style>
