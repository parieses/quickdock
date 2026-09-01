<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Minus, Square, X } from '@lucide/vue'
import { HidePluginWindow, MinimizePluginWindow, ToggleMaximizePluginWindow } from '../../bindings/quickdock/services/appservice'
import PluginFrame from './PluginFrame.vue'

const props = defineProps<{ pluginId: string }>()

const { t } = useI18n()
const pluginName = ref(props.pluginId)

// 独立窗口：init 走全局 pending init（跨窗口传入），前端在此页不主动注入
function closeWindow() {
  HidePluginWindow(props.pluginId)
}
</script>

<template>
  <div class="plugin-window">
    <!-- 标题栏 -->
    <div class="pw-titlebar">
      <span class="pw-title">{{ pluginName }}</span>
      <div class="pw-controls">
        <button class="pw-btn pw-btn-min" @click="MinimizePluginWindow(props.pluginId)" :title="t('minimize')">
          <Minus :size="13" />
        </button>
        <button class="pw-btn pw-btn-max" @click="ToggleMaximizePluginWindow(props.pluginId)" :title="t('maximize')">
          <Square :size="11" />
        </button>
        <button class="pw-btn pw-btn-close" @click="closeWindow" :title="t('close')">
          <X :size="14" />
        </button>
      </div>
    </div>

    <!-- 内容区：统一插件宿主 -->
    <PluginFrame :plugin-id="props.pluginId" use-pending-init @title="pluginName = $event || pluginName" />
  </div>
</template>

<style scoped>
.plugin-window {
  display: flex; flex-direction: column;
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}

/* 标题栏：shadow-border 替代 solid border */
.pw-titlebar {
  display: flex; align-items: center; justify-content: space-between;
  height: 36px; flex-shrink: 0;
  padding: 0 0 0 14px;
  background: var(--color-bg-secondary);
  box-shadow: inset 0 -1px 0 0 var(--color-border);
  /* 拖拽机制：Wails v3 运行时调度器识别 --wails-draggable（全局 body 同款），
     在标题栏区域按下即可移动窗口。
     注意：不要用 -webkit-app-region: drag —— 它在 Wails 中必须
     NonClientRegionSupport:true 才生效（本程序未开启），且会让 Blink 把该
     区域当成原生拖拽手柄、吞掉 mousedown，反而导致整扇窗口无法拖动。 */
  --wails-draggable: drag;
  user-select: none;
}
.pw-title {
  font-size: 12px; font-weight: 500;
  color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  letter-spacing: 0.02em;
}
.pw-controls {
  display: flex; align-items: center;
  /* 标题栏按钮区退出拖拽，点击按钮不会误触发移动 */
  --wails-draggable: no-drag;
}
.pw-btn {
  display: flex; align-items: center; justify-content: center;
  width: 46px; height: 36px;
  border: none; background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background 0.1s, color 0.1s;
}
.pw-btn:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.pw-btn:active {
  background: var(--color-bg-active);
}
.pw-btn-close:hover {
  background: var(--color-danger);
  color: #fff;
}
.pw-btn-max svg {
  transform: rotate(180deg);
}
</style>
