<script setup lang="ts">
/**
 * SSH 交互式问答弹窗（密码 + OTP 等多轮 keyboard-interactive）。
 *
 * 服务端可能连着下发好几轮问题，每一问要不要回显由 echos[i] 决定：
 * OTP / 密码这类必须走 password，否则明文答案会留在屏幕上。
 * 组件只负责收集并原样交回，不判断答案对错——那是服务端的事。
 */
import { nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldAlert } from '@lucide/vue'

const { t } = useI18n()

/** 与 Go 侧 SshChallengeEvent 的 json 字段一一对应，事件负载在 Wails 信封的 data 里。 */
interface SshChallengeEvent {
  id: string
  hostId: string
  name: string
  instruction: string
  questions: string[]
  echos: boolean[]
}

const props = defineProps<{
  event: SshChallengeEvent
}>()

const emit = defineEmits<{
  submit: [answers: string[], canceled: boolean]
  cancel: []
}>()

const answers = ref<string[]>([])
const boxRef = ref<HTMLElement | null>(null)

// 每个问题一格答案；换一批问题就整体重置，避免上一轮的 OTP 残留到这一轮。
watch(
  () => props.event,
  e => {
    answers.value = e.questions.map(() => '')
  },
  { immediate: true },
)

onMounted(() => {
  // 弹窗一出现焦点就落在第一个输入框：用户手已经在键盘上，省掉一次点击。
  void nextTick(() => boxRef.value?.querySelector<HTMLInputElement>('input')?.focus())
})

function onSubmit() {
  emit('submit', answers.value.slice(), false)
}

function onCancel() {
  emit('cancel')
}
</script>

<template>
  <div
    class="challenge-mask"
    @click.self="onCancel"
    @keydown.esc="onCancel"
  >
    <div ref="boxRef" class="challenge-box" role="dialog" aria-modal="true">
      <header class="challenge-head">
        <ShieldAlert :size="16" class="challenge-icon" />
        <h3 class="challenge-title">{{ t('sshChallengeTitle') }}</h3>
      </header>

      <p v-if="event.name" class="challenge-name">{{ event.name }}</p>
      <p v-if="event.instruction" class="challenge-instruction">{{ event.instruction }}</p>

      <p class="challenge-desc">{{ t('sshChallengeInstruction') }}</p>

      <div class="challenge-questions">
        <label v-for="(q, i) in event.questions" :key="i" class="challenge-field">
          <span class="challenge-label">{{ q }}</span>
          <input
            v-model="answers[i]"
            class="challenge-input"
            :type="event.echos[i] === false ? 'password' : 'text'"
            autocomplete="off"
            @keydown.enter="onSubmit"
          />
        </label>
      </div>

      <p class="challenge-hint">{{ t('sshChallengeHint') }}</p>

      <footer class="challenge-actions">
        <button class="challenge-btn" @click="onCancel">{{ t('sshChallengeCancel') }}</button>
        <button class="challenge-btn challenge-btn--primary" @click="onSubmit">
          {{ t('sshChallengeSubmit') }}
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.challenge-mask {
  position: absolute;
  inset: 0;
  z-index: 40;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-6);
  background: var(--color-bg-overlay);
}

.challenge-box {
  width: 420px;
  max-width: 100%;
  max-height: 100%;
  display: flex;
  flex-direction: column;
  overflow-y: auto;
  padding: var(--space-6);
  background: var(--color-popover-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-toast);
}

.challenge-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}

.challenge-icon {
  color: var(--color-accent);
  flex: 0 0 auto;
}

.challenge-title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.challenge-name {
  margin: 0 0 var(--space-2);
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-primary);
  word-break: break-word;
}

.challenge-instruction {
  margin: 0 0 var(--space-3);
  font-size: 12px;
  line-height: 1.7;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}

.challenge-desc {
  margin: 0 0 var(--space-4);
  font-size: 12px;
  line-height: 1.7;
  color: var(--color-text-secondary);
}

.challenge-questions {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.challenge-field {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.challenge-label {
  font-size: 11px;
  color: var(--color-text-muted);
  word-break: break-word;
}

.challenge-input {
  height: 30px;
  padding: 0 var(--space-3);
  font-size: 12px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  outline: none;
}

.challenge-input:focus {
  border-color: var(--color-border-focus);
}

.challenge-hint {
  margin: var(--space-4) 0 0;
  font-size: 11px;
  line-height: 1.6;
  color: var(--color-text-muted);
}

.challenge-actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
  margin-top: var(--space-6);
}

.challenge-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 28px;
  padding: 0 11px;
  font-size: 12px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.challenge-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.challenge-btn--primary {
  color: var(--color-accent-text);
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.challenge-btn--primary:hover {
  color: var(--color-accent-text);
  background: var(--color-accent-hover);
}
</style>
