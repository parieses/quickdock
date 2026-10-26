<script setup lang="ts">
/**
 * 整页空状态引导（两个层级共用一套排版，样式在 styles/guide.css）。
 *
 * - variant="scene"      工作空间里一个场景都没有 → 占满内容区（场景栏此时不渲染）
 * - variant="collection" 当前场景下一个集合都没有 → 占满「集合 + 项目」两块区域
 *
 * 为什么合成一个组件：这两档引导页的骨架完全一样（图标 / 标题 / 说明 /
 * 主按钮 / 三步 / 导入链接），只有文案和「新建什么」不同。拆成两个组件
 * 就得把排版写两遍，迟早一处改了另一处没改 —— 这正是这次要修的问题。
 *
 * 新建对话框由本组件自己持有：两档空状态下，原本负责新建的那个列表栏
 * 都不渲染，不能指望它把对话框带进来。
 */
import { computed, inject, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import { useSceneFields } from '../composables/useSceneFields'
import { useCollectionFields } from '../composables/useCollectionFields'
import { getErrorMessage } from '../utils/error'
import CreateDialog from './CreateDialog.vue'
import type { ToastAPI } from '../types'

const props = defineProps<{ variant: 'scene' | 'collection' }>()

const { t } = useI18n()
const store = useWorkspaceStore()
const toast = inject<ToastAPI>('toast')!

const sceneFields = useSceneFields()
const collectionFields = useCollectionFields()

const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

const showDialog = ref(false)
const isCreating = ref(false)

const isScene = computed(() => props.variant === 'scene')

const copy = computed(() => isScene.value
  ? {
      title: t('wsGuideTitle'),
      desc: t('wsGuideDesc'),
      cta: t('wsGuideCta'),
      steps: [
        { label: t('wsGuideStep1'), desc: t('wsGuideStep1Desc') },
        { label: t('wsGuideStep2'), desc: t('wsGuideStep2Desc') },
        { label: t('wsGuideStep3'), desc: t('wsGuideStep3Desc') },
      ],
    }
  : {
      title: t('collGuideTitle'),
      desc: t('collGuideDesc'),
      cta: t('collGuideCta'),
      steps: [
        { label: t('collGuideStep1'), desc: t('collGuideStep1Desc') },
        { label: t('collGuideStep2'), desc: t('collGuideStep2Desc') },
        { label: t('collGuideStep3'), desc: t('collGuideStep3Desc') },
      ],
    })

const dialogTitle = computed(() => isScene.value ? t('addScene') : t('addCollection'))
const dialogFields = computed(() => isScene.value ? sceneFields.value : collectionFields.value)

async function handleCreate(values: Record<string, string>) {
  if (isCreating.value) return
  isCreating.value = true
  try {
    if (isScene.value) {
      await store.addScene(values.name, values.type)
    } else {
      await store.addCollection(values.name, values.type, values.openStrategy || 'single')
    }
    showDialog.value = false
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  } finally {
    isCreating.value = false
  }
}
</script>

<template>
  <div class="qd-guide">
    <div class="qd-guide-inner">
      <div class="qd-guide-icon">
        <!-- scene：分层图标（一组工作内容）；collection：文件夹图标 -->
        <svg v-if="isScene" width="26" height="26" viewBox="0 0 24 24" fill="none"
          stroke="var(--color-accent)" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
          <path d="M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z" />
          <path d="M2 12.5l8.58 3.91a2 2 0 0 0 1.66 0L21 12.5" />
          <path d="M2 17.5l8.58 3.91a2 2 0 0 0 1.66 0L21 17.5" />
        </svg>
        <svg v-else width="26" height="26" viewBox="0 0 24 24" fill="none"
          stroke="var(--color-accent)" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
          <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
        </svg>
      </div>

      <h2 class="qd-guide-title">{{ copy.title }}</h2>
      <p class="qd-guide-desc">{{ copy.desc }}</p>

      <button class="qd-guide-cta" :disabled="isCreating" @click="showDialog = true">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor"
          stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round">
          <path d="M12 5v14" /><path d="M5 12h14" />
        </svg>
        <span>{{ copy.cta }}</span>
      </button>

      <div class="qd-guide-steps">
        <div v-for="(step, i) in copy.steps" :key="step.label" class="qd-guide-step">
          <div class="qd-guide-step-head">
            <span class="qd-guide-step-num">{{ i + 1 }}</span>
            <span class="qd-guide-step-label">{{ step.label }}</span>
          </div>
          <p class="qd-guide-step-desc">{{ step.desc }}</p>
        </div>
      </div>

      <div class="qd-guide-footer">
        <button class="qd-guide-link" @click="emit('open-settings', 'snapshot')">
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M3 12a9 9 0 1 0 9-9" /><path d="M3 3v6h6" />
          </svg>
          {{ t('importFromSnapshot') }}
        </button>
      </div>
    </div>

    <CreateDialog
      :visible="showDialog"
      :title="dialogTitle"
      :fields="dialogFields"
      @confirm="handleCreate"
      @cancel="showDialog = false"
    />
  </div>
</template>
