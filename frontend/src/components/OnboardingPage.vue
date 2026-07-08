<script setup lang="ts">
import { inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const store = useWorkspaceStore()
const toast = inject<ToastAPI>('toast')!

const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

async function handleGetStarted() {
  try {
    const ws = await store.addWorkspace(t('defaultWorkspaceName'))
    await store.selectWorkspace(ws.id)
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}

function openSettings() {
  emit('open-settings', 'snapshot')
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
      <button class="ob-start-btn" @click="handleGetStarted">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
          <polygon points="5 3 19 12 5 21 5 3"/>
        </svg>
        <span>{{ t('getStarted') }}</span>
      </button>

      <!-- 三步引导 -->
      <div class="ob-steps">
        <div class="ob-step">
          <div class="step-number">1</div>
          <div class="step-body">
            <span class="step-label">{{ t('step1') }}</span>
            <span class="step-desc">{{ t('step1Desc') }}</span>
          </div>
        </div>
        <div class="ob-step-connector" />
        <div class="ob-step">
          <div class="step-number">2</div>
          <div class="step-body">
            <span class="step-label">{{ t('step2') }}</span>
            <span class="step-desc">{{ t('step2Desc') }}</span>
          </div>
        </div>
        <div class="ob-step-connector" />
        <div class="ob-step">
          <div class="step-number">3</div>
          <div class="step-body">
            <span class="step-label">{{ t('step3') }}</span>
            <span class="step-desc">{{ t('step3Desc') }}</span>
          </div>
        </div>
      </div>

      <!-- 底部操作 -->
      <div class="ob-footer">
        <button class="ob-link-btn" @click="openSettings">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M21 12a9 9 0 1 1-9-9"/><path d="M3 12h18"/><path d="M12 3a9 9 0 0 1 9 9"/>
          </svg>
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
  justify-content: center;
  height: 100%;
  overflow: hidden;
  background: var(--color-bg-primary);
}

.onboarding-inner {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  max-width: 420px;
  padding: 48px 32px;
}

.ob-logo {
  margin-bottom: 24px;
  opacity: 0.9;
}

.ob-title {
  font-size: 24px;
  font-weight: 700;
  color: var(--color-text-primary);
  margin: 0 0 8px;
  letter-spacing: -0.3px;
}

.ob-desc {
  font-size: 14px;
  color: var(--color-text-muted);
  margin: 0 0 32px;
  line-height: 1.6;
}

.ob-start-btn {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  padding: 12px 32px;
  border: none;
  border-radius: 10px;
  background: var(--color-accent);
  color: #fff;
  font-size: 15px;
  font-weight: 600;
  font-family: inherit;
  cursor: pointer;
  transition: all 0.15s;
  margin-bottom: 40px;
}
.ob-start-btn:hover {
  background: var(--color-accent-hover);
  transform: translateY(-1px);
  box-shadow: 0 4px 12px rgba(74, 158, 255, 0.3);
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
  margin-bottom: 32px;
}

.ob-step {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 14px 16px;
  border-radius: 10px;
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  text-align: left;
}

.step-number {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: var(--color-accent-bg);
  color: var(--color-accent);
  font-size: 14px;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.step-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.step-label {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.step-desc {
  font-size: 12px;
  color: var(--color-text-muted);
}

.ob-step-connector {
  width: 2px;
  height: 16px;
  background: var(--color-border);
  margin-left: 23px;
}

/* 底部 */
.ob-footer {
  margin-top: 8px;
}

.ob-link-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-muted);
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
  transition: all 0.12s;
}
.ob-link-btn:hover {
  color: var(--color-accent);
  border-color: var(--color-accent-border);
  background: var(--color-accent-bg);
}
</style>
