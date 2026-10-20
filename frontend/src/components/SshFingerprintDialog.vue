<script lang="ts" setup>
/**
 * 主机指纹确认弹窗（TOFU）。
 *
 * 两个入口共用本组件，因为「首次信任」和「指纹变了要接受新密钥」在服务端是
 * 同一个动作（KnownHosts.Trust），文案与风险等级却完全不同：
 *   - unknown：第一次连这台机器，核对无误即可信任；
 *   - changed：同类型密钥换了，可能是重装也可能是中间人，必须强告警 + 显式勾选。
 *
 * 弹窗自己调 TrustFingerprint（而不是把指纹抛给父组件再回传）：
 * 服务端只信任「本次握手真实观察到」的指纹，页面拿到的就是那个值，
 * 交给本组件闭环处理，父组件不需要再拼参数、也不会传错。
 */
import { computed, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, Copy, ShieldAlert, ShieldQuestion, X } from '@lucide/vue'
import { TrustFingerprint } from '../../bindings/quickdock/services/sshx/sshservice'
import { CopyText } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useFocusTrap } from '../utils/focusTrap'
import { sshHostKeyFile } from '../utils/ssh'

const { t } = useI18n()

const props = withDefaults(
  defineProps<{
    visible: boolean
    /** unknown = 首次信任；changed = 指纹变更（高危） */
    mode: 'unknown' | 'changed'
    host: string
    port: number
    keyType: string
    /** 本次观察到、等待确认的指纹 */
    fingerprint: string
    /** 变更前的指纹（mode=changed 时展示） */
    oldFingerprint?: string
    /** 该主机此前记录过的密钥类型（非首连时非空） */
    knownTypes?: string[]
  }>(),
  { oldFingerprint: '', knownTypes: () => [] },
)

const emit = defineEmits<{
  (e: 'trusted'): void
  (e: 'cancel'): void
}>()

const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

const busy = ref(false)
const errMsg = ref('')
const copied = ref(false)
/** 高危变更必须显式勾选才能继续——防止用户对着红字一路回车点过去。 */
const acknowledged = ref(false)

const isChanged = computed(() => props.mode === 'changed')
const target = computed(() => `${props.host}:${props.port || 22}`)
const verifyCmd = computed(() => `ssh-keygen -lf /etc/ssh/${sshHostKeyFile(props.keyType)}`)

const canTrust = computed(() => !busy.value && (!isChanged.value || acknowledged.value))

watch(
  () => props.visible,
  (v) => {
    if (!v) return
    // 每次打开都要复位：上一次的勾选/报错不能带到下一次。
    busy.value = false
    errMsg.value = ''
    copied.value = false
    acknowledged.value = false
  },
)

async function copyFingerprint() {
  try {
    await CopyText(props.fingerprint)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    // 复制失败不影响主流程：用户可以手动选中文本
  }
}

async function onTrust() {
  if (!canTrust.value) return
  busy.value = true
  errMsg.value = ''
  try {
    unwrap(await TrustFingerprint(props.host, props.port, props.fingerprint))
    emit('trusted')
  } catch (e) {
    errMsg.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    emit('cancel')
    return
  }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fp">
      <div v-if="visible" class="fp-overlay" @keydown="onKeydown">
        <div ref="panelRef" class="fp-panel" :class="{ 'fp-panel--danger': isChanged }" role="dialog">
          <header class="fp-head">
            <span class="fp-badge">
              <ShieldAlert v-if="isChanged" :size="16" />
              <ShieldQuestion v-else :size="16" />
            </span>
            <span class="fp-title">
              {{ isChanged ? t('sshHostKeyChangedTitle') : t('sshHostKeyTitle') }}
            </span>
            <button class="fp-x" :title="t('cancel')" @click="emit('cancel')">
              <X :size="15" />
            </button>
          </header>

          <div class="fp-body">
            <p class="fp-desc">
              {{ isChanged
                ? t('sshHostKeyChangedDesc', { target })
                : t('sshHostKeyDesc', { target }) }}
            </p>

            <div v-if="isChanged && oldFingerprint" class="fp-row">
              <span class="fp-label">{{ t('sshHostKeyOld') }}</span>
              <code class="fp-fp fp-fp--old">{{ oldFingerprint }}</code>
            </div>

            <div class="fp-row">
              <span class="fp-label">{{ isChanged ? t('sshHostKeyNew') : t('sshHostKeyFingerprint') }}</span>
              <span class="fp-value">
                <code class="fp-fp">{{ fingerprint }}</code>
                <button class="fp-copy" :title="t('copy')" @click="copyFingerprint">
                  <Check v-if="copied" :size="13" />
                  <Copy v-else :size="13" />
                </button>
              </span>
            </div>

            <div v-if="keyType" class="fp-row">
              <span class="fp-label">{{ t('sshHostKeyType') }}</span>
              <code class="fp-key">{{ keyType }}</code>
            </div>

            <p v-if="!isChanged && knownTypes.length > 0" class="fp-note">
              {{ t('sshHostKeyKnownTypes', { types: knownTypes.join(', '), keyType: keyType || '-' }) }}
            </p>

            <div class="fp-verify">
              <span class="fp-verify-label">{{ t('sshHostKeyVerifyHint') }}</span>
              <code class="fp-cmd">{{ verifyCmd }}</code>
              <span class="fp-verify-label">{{ t('sshHostKeyCompareHint') }}</span>
            </div>

            <label v-if="isChanged" class="fp-ack">
              <input v-model="acknowledged" type="checkbox" />
              <span>{{ t('sshHostKeyAck') }}</span>
            </label>

            <p v-if="errMsg" class="fp-err">{{ errMsg }}</p>
          </div>

          <footer class="fp-foot">
            <button class="btn btn-ghost" :disabled="busy" @click="emit('cancel')">
              {{ t('cancel') }}
            </button>
            <button
              class="btn"
              :class="isChanged ? 'btn-danger' : 'btn-primary'"
              :disabled="!canTrust"
              @click="onTrust"
            >
              {{ busy ? t('sshHostKeyTrusting') : t('sshHostKeyTrust') }}
            </button>
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/* 比主机表单弹窗高一层：它是从表单/会话视图里二次弹出的。 */
.fp-overlay {
  position: fixed;
  inset: 0;
  z-index: 1500;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

.fp-panel {
  width: 480px;
  max-width: 92vw;
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden;
  transition: transform 0.16s ease-out;
}

/* 高危态用顶部红条标记，避免整屏变红导致文字对比度下降 */
.fp-panel--danger {
  border-top: 3px solid var(--color-danger);
}

.fp-head {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-6) var(--space-7);
  border-bottom: 1px solid var(--color-border);
}

.fp-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: var(--radius-sm);
  color: var(--color-warning);
  background: var(--color-warning-bg);
}

.fp-panel--danger .fp-badge {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.fp-title {
  flex: 1 1 auto;
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.fp-x {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.fp-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.fp-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-6) var(--space-7);
  overflow-y: auto;
}

.fp-desc {
  margin: 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--color-text-secondary);
}

.fp-row {
  display: flex;
  align-items: baseline;
  gap: var(--space-4);
}

.fp-label {
  flex: 0 0 auto;
  width: 64px;
  font-size: 11px;
  color: var(--color-text-muted);
}

.fp-value {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
}

.fp-fp,
.fp-key,
.fp-cmd {
  font-family: var(--font-mono);
  font-size: 11.5px;
  word-break: break-all;
  user-select: text;
}

.fp-fp {
  flex: 1 1 auto;
  padding: var(--space-3) var(--space-4);
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  line-height: 1.5;
}

.fp-fp--old {
  color: var(--color-text-muted);
  text-decoration: line-through;
  background: transparent;
}

.fp-key {
  color: var(--color-text-secondary);
}

.fp-copy {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-muted);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.fp-copy:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.fp-note {
  margin: 0;
  padding: var(--space-4) var(--space-5);
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-radius: var(--radius-xs);
}

.fp-verify {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-5);
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-xs);
}

.fp-verify-label {
  font-size: 11px;
  color: var(--color-text-muted);
}

.fp-cmd {
  color: var(--color-text-secondary);
}

.fp-ack {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
  padding: var(--space-4) var(--space-5);
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-primary);
  background: var(--color-danger-bg);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.fp-ack input {
  margin-top: 2px;
  accent-color: var(--color-danger);
  cursor: pointer;
}

.fp-err {
  margin: 0;
  padding: var(--space-3) var(--space-4);
  font-size: 12px;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-radius: var(--radius-xs);
}

.fp-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-4);
  padding: var(--space-5) var(--space-7);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.btn {
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
  opacity: 0.5;
}

.btn-ghost {
  color: var(--color-text-muted);
  background: transparent;
  border: 1px solid var(--color-border);
}

.btn-ghost:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.btn-primary {
  color: var(--color-accent-text);
  background: var(--color-accent);
}

.btn-primary:hover:not(:disabled) {
  background: var(--color-accent-hover);
}

.btn-danger {
  color: var(--color-accent-text);
  background: var(--color-danger);
}

.btn-danger:hover:not(:disabled) {
  opacity: 0.88;
}

.fp-enter-active {
  transition: opacity 0.16s ease-out;
}

.fp-leave-active {
  transition: opacity 0.12s ease-in;
}

.fp-enter-from,
.fp-leave-to {
  opacity: 0;
}

.fp-enter-from .fp-panel {
  transform: scale(0.96) translateY(-6px);
}
</style>
