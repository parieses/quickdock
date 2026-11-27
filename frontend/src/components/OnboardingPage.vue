<script setup lang="ts">
import { inject, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Globe2, Play } from '@lucide/vue'
import { useWorkspaceStore } from '../stores/workspace'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const store = useWorkspaceStore()
const toast = inject<ToastAPI>('toast')!
const isCreating = ref(false)

const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

async function handleGetStarted() {
  if (isCreating.value) return
  isCreating.value = true
  try {
    const ws = await store.addWorkspace(t('defaultWorkspaceName'))
    await store.selectWorkspace(ws.id)
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  } finally {
    isCreating.value = false
  }
}

function openSettings() {
  emit('open-settings', 'backup')
}
</script>

<template>
  <div class="onboarding">
    <div class="onboarding-inner">
      <!-- Logo / Icon -->
      <div class="ob-logo">
        <svg width="48" height="48" viewBox="0 0 48 48" fill="none">
          <rect x="6" y="12" width="12" height="28" rx="3" fill="var(--color-accent)" opacity="0.6"/>
          <rect x="18" y="6" width="12" height="34" rx="3" fill="var(--color-accent)" opacity="0.8"/>
          <rect x="30" y="16" width="12" height="24" rx="3" fill="var(--color-accent)"/>
        </svg>
      </div>

      <!-- 标题区 -->
      <h1 class="ob-title">{{ t('welcomeTitle') }}</h1>
      <p class="ob-desc">{{ t('welcomeDesc') }}</p>

      <!-- 开始按钮 -->
      <button class="ob-start-btn qd-btn" :class="{ disabled: isCreating }" :disabled="isCreating" @click="handleGetStarted">
        <Play :size="18" />
        <span>{{ t('getStarted') }}</span>
      </button>

      <!-- 三步引导：本组件只有「创建工作空间 / 打开设置」两个动作，
           所以只把第 1 步接到创建动作；第 2、3 步没有可落地入口，
           去掉数字与连线，保留为纯说明，不伪造可点的假按钮。 -->
      <div class="ob-steps">
        <div
          class="ob-step is-action"
          role="button"
          tabindex="0"
          :aria-disabled="isCreating"
          @click="handleGetStarted"
          @keydown.enter.prevent="handleGetStarted"
          @keydown.space.prevent="handleGetStarted"
        >
          <div class="step-number">1</div>
          <div class="step-body">
            <span class="step-label">{{ t('step1') }}</span>
            <span class="step-desc">{{ t('step1Desc') }}</span>
          </div>
        </div>
        <div class="ob-step">
          <div class="step-body">
            <span class="step-label">{{ t('step2') }}</span>
            <span class="step-desc">{{ t('step2Desc') }}</span>
          </div>
        </div>
        <div class="ob-step">
          <div class="step-body">
            <span class="step-label">{{ t('step3') }}</span>
            <span class="step-desc">{{ t('step3Desc') }}</span>
          </div>
        </div>
      </div>

      <!-- 底部操作 -->
      <div class="ob-footer">
        <button class="ob-link-btn qd-btn" @click="openSettings">
          <Globe2 :size="14" />
          {{ t('importFromSnapshot') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.onboarding {
  flex: 1;
  display: flex;
  align-items: center;
  /* 高度不够时用 margin:auto 居中，而不是 justify-content:center：
     后者在内容比容器高时会把顶部推出可视区上沿，且滚不回去（负溢出），
     矮窗口里就是「logo 少一截、底部按钮干脆看不见」。 */
  justify-content: flex-start;
  height: 100%;
  overflow-y: auto;
  background: var(--color-bg-primary);
}

.onboarding-inner {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  max-width: 420px;
  padding: var(--space-11) var(--space-9);
  margin: auto;
}

.ob-logo {
  margin-bottom: var(--space-8);
  opacity: 0.9;
}

.ob-title {
  font-size: var(--text-2xl);
  font-weight: 600;
  color: var(--color-text-primary);
  margin: 0 0 var(--space-4);
  
}

.ob-desc {
  font-size: var(--text-base);
  color: var(--color-text-muted);
  margin: 0 0 var(--space-9);
  line-height: 1.6;
}

.ob-start-btn {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  padding: var(--space-5) var(--space-9);
  border: none;
  border-radius: var(--radius-md);
  background: var(--color-accent);
  color: var(--color-accent-text);
  font-size: var(--text-lg);
  font-weight: 600;
  font-family: inherit;
  cursor: pointer;
  transition: background-color 0.15s, color 0.15s, border-color 0.15s, opacity 0.15s;
  margin-bottom: var(--space-10);
}
.ob-start-btn:hover {
  background: var(--color-accent-hover);
  transform: translateY(-1px);
  box-shadow: 0 4px 12px var(--color-accent-border-strong);
}
.ob-start-btn:active {
  transform: translateY(0);
}

/* 三步引导 */
.ob-steps {
  display: flex;
  flex-direction: column;
  gap: 0;
  width: 100%;
  margin-bottom: var(--space-9);
}

.ob-step {
  display: flex;
  align-items: center;
  gap: var(--space-6);
  padding: 14px var(--space-6);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  text-align: left;
}
/* 第 1 步是可点击入口：与主按钮同源（创建工作空间），要有明确的可点反馈 */
.ob-step.is-action { cursor: pointer; transition: border-color var(--transition-fast), background-color var(--transition-fast); }
.ob-step.is-action:hover { border-color: var(--color-accent-border); background: var(--color-bg-hover); }
/* 第 2、3 步没有落地动作，去掉数字圆圈后正文对齐第 1 步标题的起始位置 */
.ob-step:not(.is-action) { padding-left: calc(32px + var(--space-6)); }

.step-number {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: var(--color-accent-bg);
  color: var(--color-accent-ink);
  font-size: var(--text-base);
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.step-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  min-width: 0;
}

.step-label {
  font-size: var(--text-md);
  font-weight: 600;
  color: var(--color-text-primary);
}

.step-desc {
  font-size: var(--text-sm);
  color: var(--color-text-muted);
}

/* 底部 */
.ob-footer {
  margin-top: var(--space-4);
}

.ob-link-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-4) 14px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
  font-family: inherit;
  cursor: pointer;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.ob-link-btn:hover {
  color: var(--color-accent-ink);
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
}
</style>
