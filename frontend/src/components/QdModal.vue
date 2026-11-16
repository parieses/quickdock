<script setup lang="ts">
import { useI18n } from 'vue-i18n'

/**
 * 全应用唯一的模态容器。
 *
 * 存量页面还有 5+ 套私有遮罩（.modal-overlay / .modal-mask / .dl-modal /
 * .qd-modal / .php-modal …），圆角、内边距、遮罩色、关闭按钮各不相同。
 * 新写的弹窗一律用本组件，不要再复制一套；存量弹窗的视觉由
 * styles/app.css 的「弹窗基线」逐项对齐，不必一次性迁移模板。
 *
 * 无障碍：v-dialog 指令（utils/a11y.ts）负责 role/aria-modal、打开聚焦首个
 * 输入控件、Tab 循环与关闭恢复焦点；这里只补 Esc 与点遮罩关闭。
 */
const props = withDefaults(defineProps<{
  modelValue: boolean
  title?: string
  closeOnMask?: boolean
  width?: string
}>(), {
  closeOnMask: true,
  width: 'var(--qd-modal-width, 460px)',
})

const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const { t } = useI18n()

function close() {
  emit('update:modelValue', false)
}
function onMask() {
  if (props.closeOnMask) close()
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="modelValue"
      v-dialog
      class="qd-modal-mask"
      @click.self="onMask"
      @keydown.esc="close"
    >
      <!-- width 是调用方按内容密度传的运行时值，属 R7 允许的例外（就近说明）。 -->
      <div class="qd-modal-card" :style="{ width }">
        <header v-if="title || $slots.header" class="qd-modal-head">
          <slot name="header">
            <h3 class="qd-modal-title">{{ title }}</h3>
          </slot>
          <button
            type="button"
            class="qd-modal-close"
            :aria-label="t('close')"
            :title="t('close')"
            @click="close"
          >✕</button>
        </header>
        <div class="qd-modal-body"><slot /></div>
        <footer v-if="$slots.footer" class="qd-modal-foot"><slot name="footer" /></footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.qd-modal-mask {
  position: fixed;
  inset: 0;
  z-index: 3000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-6);
  background: var(--color-bg-overlay);
}
.qd-modal-card {
  max-width: 94vw;
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-pop);
  overflow: hidden;
}
.qd-modal-head {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-5) var(--space-6);
  border-bottom: 1px solid var(--color-border);
}
.qd-modal-title {
  flex: 1;
  min-width: 0;
  margin: 0;
  font-size: var(--text-base);
  font-weight: 600;
  color: var(--color-text-primary);
}
.qd-modal-close {
  flex: none;
  width: 24px;
  height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
  cursor: pointer;
}
.qd-modal-close:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.qd-modal-body { flex: 1; min-height: 0; overflow: auto; padding: var(--space-6); }
.qd-modal-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
  padding: var(--space-4) var(--space-6);
  border-top: 1px solid var(--color-border);
}
</style>
