<script setup lang="ts">
import { copyToClipboard } from '../utils/clipboard'
/**
 * 设置 → 安全：启动密码。
 *
 * 交互刻意做成「页内展开」而不是弹窗：这里只有三个动作（设置 / 修改 / 移除），
 * 弹窗要多一套遮罩与焦点管理，收益为零。恢复码只在设置或修改成功后出现一次，
 * 是这一页唯一的强提示——用户没抄走就再也看不到。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, Copy, Lock, ShieldCheck } from '@lucide/vue'
import {
  LockStatus, LockNow, SetAutoLock,
  SetStartupPassword, ChangeStartupPassword, RemoveStartupPassword,
} from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'

const props = defineProps<{ visible: boolean }>()
const { t } = useI18n()

const hasPassword = ref(false)
/** 空闲自动锁定分钟数；0 = 关闭。计时在后端，这里只负责展示与设置。 */
const autoLockMin = ref(0)
const autoLockBusy = ref(false)
// 读取状态失败必须与「确实没设密码」区分开：否则已有密码的机器会显示「未启用」，
// 用户点「设置密码」等于把原密码覆盖掉（后端 set 是直接覆盖，不校验旧密码）。
const loadFailed = ref(false)
const loading = ref(false)
const busy = ref(false)
// mode 为空表示没有展开任何表单
const mode = ref<'' | 'set' | 'change' | 'remove'>('')
const oldPwd = ref('')
const newPwd = ref('')
const confirmPwd = ref('')
const recoveryCode = ref('')
const msg = ref('')
const msgOk = ref(false)

const submitLabel = computed(() => {
  if (mode.value === 'remove') return t('securityRemoveConfirm')
  if (mode.value === 'change') return t('securityChangeConfirm')
  return t('securitySetConfirm')
})

async function load() {
  loading.value = true
  try {
    const st = unwrap<{ hasPassword?: boolean; autoLockMinutes?: number }>(await LockStatus())
    hasPassword.value = !!st?.hasPassword
    autoLockMin.value = st?.autoLockMinutes ?? 0
    loadFailed.value = false
  } catch (e) {
    logErr('SettingsSecurity.load', e)
    loadFailed.value = true
  } finally {
    loading.value = false
  }
}

watch(() => props.visible, (v) => { if (v) load() }, { immediate: true })

function resetForm() {
  oldPwd.value = ''
  newPwd.value = ''
  confirmPwd.value = ''
  msg.value = ''
}

function openMode(next: 'set' | 'change' | 'remove') {
  resetForm()
  mode.value = next
}

function cancel() {
  mode.value = ''
  resetForm()
}

function setMsg(text: string, ok: boolean) {
  msg.value = text
  msgOk.value = ok
}

async function submit() {
  if (busy.value) return
  msg.value = ''

  if (mode.value !== 'remove') {
    if (newPwd.value.length < 6) {
      setMsg(t('securityTooShort'), false)
      return
    }
    if (newPwd.value !== confirmPwd.value) {
      setMsg(t('securityMismatch'), false)
      return
    }
  }

  busy.value = true
  try {
    if (mode.value === 'set') {
      const res = unwrap<{ recoveryCode?: string }>(await SetStartupPassword(newPwd.value))
      recoveryCode.value = res?.recoveryCode || ''
      hasPassword.value = true
    } else if (mode.value === 'change') {
      const res = unwrap<{ recoveryCode?: string }>(await ChangeStartupPassword(oldPwd.value, newPwd.value))
      recoveryCode.value = res?.recoveryCode || ''
      hasPassword.value = true
    } else if (mode.value === 'remove') {
      unwrap(await RemoveStartupPassword(oldPwd.value))
      hasPassword.value = false
      setMsg(t('securityRemoved'), true)
    }
    mode.value = ''
    oldPwd.value = ''
    newPwd.value = ''
    confirmPwd.value = ''
  } catch (e) {
    setMsg(getErrorMessage(e), false)
  } finally {
    busy.value = false
  }
}

async function lockNow() {
  if (busy.value) return
  busy.value = true
  try {
    unwrap(await LockNow())
  } catch (e) {
    setMsg(getErrorMessage(e), false)
  } finally {
    busy.value = false
  }
}

/** 改自动锁定时长。后端立即生效，前端用返回的状态校准，避免显示与实际不一致。 */
async function changeAutoLock(e: Event) {
  if (autoLockBusy.value) return
  const next = Number((e.target as HTMLSelectElement).value) || 0
  const prev = autoLockMin.value
  autoLockBusy.value = true
  try {
    const st = unwrap<{ autoLockMinutes?: number }>(await SetAutoLock(next))
    autoLockMin.value = st?.autoLockMinutes ?? next
    setMsg(t('securityAutoLockSaved'), true)
  } catch (err) {
    autoLockMin.value = prev
    setMsg(getErrorMessage(err), false)
  } finally {
    autoLockBusy.value = false
  }
}

async function copyRecovery() {
  if (await copyToClipboard(recoveryCode.value)) {
    setMsg(t('securityCopied'), true)
  } else {
    // 剪贴板写入失败时让用户手动选中：不静默失败
    setMsg(t('securityCopyFailed'), false)
  }
}

function dismissRecovery() {
  recoveryCode.value = ''
  setMsg(t('securityRecoverySaved'), true)
}
</script>

<template>
  <div class="section">
    <h3 class="section-title">{{ t('securityTitle') }}</h3>
    <p class="section-desc">{{ t('securityDesc') }}</p>

    <!-- 恢复码：一次性展示，必须显眼到不会被忽略 -->
    <div v-if="recoveryCode" class="sec-recovery">
      <p class="sec-recovery-title"><ShieldCheck :size="15" />{{ t('securityRecoveryTitle') }}</p>
      <code class="sec-recovery-code">{{ recoveryCode }}</code>
      <p class="sec-recovery-hint">{{ t('securityRecoveryHint') }}</p>
      <div class="setting-control sec-recovery-actions">
        <button class="btn btn-secondary" @click="copyRecovery"><Copy :size="13" />{{ t('securityCopy') }}</button>
        <button class="btn btn-primary" @click="dismissRecovery"><Check :size="13" />{{ t('securitySaved') }}</button>
      </div>
    </div>

    <div class="setting-row">
      <span class="setting-label">{{ t('securityPassword') }}</span>
      <div class="setting-control">
        <template v-if="loadFailed">
          <span class="sec-state">{{ t('statusError') }}</span>
          <button class="btn btn-secondary" @click="load">{{ t('statusRetry') }}</button>
        </template>
        <span v-else class="sec-state" :class="{ 'is-on': hasPassword }">
          {{ hasPassword ? t('securityEnabled') : t('securityDisabled') }}
        </span>
      </div>
    </div>

    <div v-if="!loading && !loadFailed" class="setting-row">
      <span class="setting-label">{{ t('securityActions') }}</span>
      <div class="setting-control">
        <template v-if="!hasPassword">
          <button class="btn btn-primary" @click="openMode('set')">{{ t('securitySetBtn') }}</button>
        </template>
        <template v-else>
          <button class="btn btn-secondary" @click="openMode('change')">{{ t('securityChangeBtn') }}</button>
          <button class="btn btn-secondary" @click="openMode('remove')">{{ t('securityRemoveBtn') }}</button>
          <button class="btn btn-primary" :disabled="busy" @click="lockNow">
            <Lock :size="13" />{{ t('securityLockNow') }}
          </button>
        </template>
      </div>
    </div>

    <!-- 离开自动锁定：密码库最需要的一道闸。计时在后端（系统空闲时长），
         关闭窗口/切到后台都不会暂停。 -->
    <div v-if="!loading && !loadFailed" class="setting-row">
      <span class="setting-label">{{ t('securityAutoLock') }}</span>
      <div class="setting-control">
        <select
          class="field-input sec-autolock"
          :value="autoLockMin"
          :disabled="autoLockBusy"
          @change="changeAutoLock"
        >
          <option :value="0">{{ t('securityAutoLockOff') }}</option>
          <option v-for="m in [5, 10, 15, 30, 60]" :key="m" :value="m">
            {{ t('securityAutoLockMin', { n: m }) }}
          </option>
        </select>
        <span class="sec-hint">{{ t('securityAutoLockDesc') }}</span>
      </div>
    </div>

    <!-- 页内展开的表单：不弹窗，少一层焦点管理 -->
    <div v-if="mode" class="sec-form">
      <label v-if="mode !== 'set'" class="field">
        <span class="field-label">{{ mode === 'remove' ? t('securityCurrentLabel') : t('securityOldLabel') }}</span>
        <input v-model="oldPwd" class="field-input" type="password" autocomplete="off" :placeholder="t('securityOldPlaceholder')" />
      </label>
      <template v-if="mode !== 'remove'">
        <label class="field">
          <span class="field-label">{{ t('securityNewLabel') }}</span>
          <input v-model="newPwd" class="field-input" type="password" autocomplete="new-password" :placeholder="t('securityNewPlaceholder')" />
        </label>
        <label class="field">
          <span class="field-label">{{ t('securityConfirmLabel') }}</span>
          <input v-model="confirmPwd" class="field-input" type="password" autocomplete="new-password" :placeholder="t('securityConfirmPlaceholder')" @keydown.enter.prevent="submit" />
        </label>
      </template>
      <div class="action-row sec-form-actions">
        <button class="btn btn-primary" :disabled="busy" @click="submit">{{ submitLabel }}</button>
        <button class="btn btn-secondary" @click="cancel">{{ t('cancel') }}</button>
      </div>
      <p v-if="msg" class="sec-msg" :class="{ 'is-ok': msgOk }">{{ msg }}</p>
    </div>

    <p v-else-if="msg" class="sec-msg" :class="{ 'is-ok': msgOk }">{{ msg }}</p>

    <p class="sec-note">{{ t('securityNote') }}</p>
  </div>
</template>

<style scoped>
.sec-state {
  font-size: var(--text-sm);
  color: var(--color-text-muted);
}
.sec-state.is-on {
  color: var(--color-accent-ink);
}

.sec-recovery {
  margin-bottom: var(--space-7);
  padding: var(--space-6);
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-md);
}
.sec-recovery-title {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin: 0 0 var(--space-4);
  font-size: var(--text-md);
  font-weight: 600;
  color: var(--color-accent);
}
.sec-recovery-code {
  display: block;
  padding: var(--space-4) var(--space-5);
  font-family: var(--font-mono);
  font-size: var(--text-lg);
  letter-spacing: 0.1em;
  text-align: center;
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border-radius: var(--radius-sm);
  user-select: all;
}
.sec-recovery-hint {
  margin: var(--space-3) 0 var(--space-4);
  font-size: var(--text-sm);
  line-height: 1.6;
  color: var(--color-text-muted);
}
.sec-recovery-actions {
  justify-content: flex-end;
}

.sec-form {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  max-width: 360px;
  margin: var(--space-5) 0 var(--space-6);
  padding: var(--space-6);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}
.sec-form-actions {
  display: flex;
  gap: var(--space-3);
  margin-top: var(--space-2);
}

.sec-autolock {
  width: 120px;
  flex-shrink: 0;
}

.sec-hint {
  min-width: 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-muted);
}

.sec-msg {
  margin: var(--space-4) 0 0;
  font-size: var(--text-sm);
  color: var(--color-danger-ink);
}
.sec-msg.is-ok {
  color: var(--color-accent);
}

.sec-note {
  margin: var(--space-7) 0 0;
  padding-top: var(--space-5);
  border-top: 1px solid var(--color-border);
  font-size: var(--text-sm);
  line-height: 1.7;
  color: var(--color-text-muted);
}
</style>
