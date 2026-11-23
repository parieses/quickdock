<script setup lang="ts">
/**
 * AI 输入框共用的圆形图标按钮：落地页（AiHero）与聊天区（AiComposer）同款
 * 32px 正圆 accent。生成中的「插入 / 停止」也复用它，只换色调与图标
 * （图标由 slot 传入），这样尺寸、圆角、禁用态只有一份实现。
 */
withDefaults(defineProps<{
  disabled?: boolean
  title?: string
  /** accent = 发送 / 插入；stop = 中止（红色） */
  variant?: 'accent' | 'stop'
}>(), { disabled: false, variant: 'accent' })

defineEmits<{ (e: 'click'): void }>()
</script>

<template>
  <button
    type="button"
    class="ai-bit-send"
    :class="{ 'is-stop': variant === 'stop' }"
    :disabled="disabled"
    :title="title"
    @click="$emit('click')"
  >
    <slot />
  </button>
</template>

<style scoped>
.ai-bit-send {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex-shrink: 0;
  border: none;
  border-radius: 50%;
  background: var(--color-accent);
  color: var(--color-accent-text);
  cursor: pointer;
  transition: opacity var(--transition-fast), background-color var(--transition-fast);
}
.ai-bit-send:disabled { background: var(--color-bg-active); color: var(--color-text-disabled); cursor: default; }
.ai-bit-send.is-stop { background: var(--color-danger); color: var(--color-danger-text); }
.ai-bit-send.is-stop:hover { background: var(--color-danger-hover); }
</style>
