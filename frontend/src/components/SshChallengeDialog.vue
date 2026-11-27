<script setup lang="ts">
/**
 * SSH 交互式问答弹窗（密码 + OTP 等多轮 keyboard-interactive）。
 *
 * 服务端可能连着下发好几轮问题，每一问要不要回显由 echos[i] 决定：
 * OTP / 密码这类必须走 password，否则明文答案会留在屏幕上。
 * 组件只负责收集并原样交回，不判断答案对错——那是服务端的事。
 */
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldAlert } from '@lucide/vue'
import QdModal from './QdModal.vue'

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

// 每个问题一格答案；换一批问题就整体重置，避免上一轮的 OTP 残留到这一轮。
watch(
  () => props.event,
  e => {
    answers.value = e.questions.map(() => '')
  },
  { immediate: true },
)

function onSubmit() {
  emit('submit', answers.value.slice(), false)
}

function onCancel() {
  emit('cancel')
}
</script>

<template>
  <!-- inline：只盖住终端区域。终端页是 keep-alive 的，做成全局模态会在切走后飘到别的页面。
       初始焦点由 QdModal 的 v-dialog 落在第一个输入框，与原来手写的 focus 等价。 -->
  <QdModal
    :model-value="true"
    inline
    width="420px"
    @update:model-value="onCancel"
  >
    <template #header>
      <ShieldAlert :size="16" class="challenge-icon" />
      <h3 class="challenge-title">{{ t('sshChallengeTitle') }}</h3>
    </template>

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

    <template #footer>
      <button class="qd-btn" @click="onCancel">{{ t('sshChallengeCancel') }}</button>
      <button class="qd-btn qd-btn-primary" @click="onSubmit">
        {{ t('sshChallengeSubmit') }}
      </button>
    </template>
  </QdModal>
</template>

<style scoped>
/* 外壳（遮罩 / 卡片 / 头部 / 底部按钮排布）由 QdModal 提供，这里只留内容样式。 */
.challenge-icon {
  color: var(--color-accent);
  flex: 0 0 auto;
}

.challenge-title {
  flex: 1;
  min-width: 0;
  margin: 0;
  font-size: var(--text-base);
  font-weight: 600;
  color: var(--color-text-primary);
}

.challenge-name {
  margin: 0 0 var(--space-2);
  font-size: var(--text-sm);
  font-weight: 500;
  color: var(--color-text-primary);
  word-break: break-word;
}

.challenge-instruction {
  margin: 0 0 var(--space-3);
  font-size: var(--text-sm);
  line-height: 1.7;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
}

.challenge-desc {
  margin: 0 0 var(--space-4);
  font-size: var(--text-sm);
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
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  word-break: break-word;
}

.challenge-input {
  height: 30px;
  padding: 0 var(--space-3);
  font-size: var(--text-sm);
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
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-muted);
}
</style>
