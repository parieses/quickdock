<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Minus, Square, X, RotateCw, Power } from '@lucide/vue'
import { System } from '@wailsio/runtime'

import {
  HidePluginWindow,
  MinimizePluginWindow,
  ToggleMaximizePluginWindow,
  ForceClosePluginWindow,
} from '../../bindings/quickdock/services/plugin/pluginservice'
import PluginFrame from './PluginFrame.vue'

// 缩放手柄：无边框窗口（Frameless:true）被 Wails 裁掉原生边框后，鼠标拖原生边框无法缩放。
// 但 Wails v3 内置了"原生缩放接管"：标题栏拖动即走此机制（wails:drag → 后端 PostMessage(WM_NCLBUTTONDOWN)
// 把缩放交给操作系统，零逐帧 IPC、丝滑）。这里复用同一通道：手柄只负责在边缘捕获 mousedown，
// 命中后发 System.invoke("wails:resize:<edge>")，后端同样地交给 OS 完成整段缩放，故无卡顿。
// 注意 edge 必须是带 -resize 后缀的 token（与 Wails edgeMap 键一致：e-resize/w-resize/s-resize/
// se-resize/sw-resize），否则后端 edgeMap 查不到。
function beginResize(edge: string) {
  System.invoke('wails:resize:' + edge)
}

const props = defineProps<{ pluginId: string }>()

const { t } = useI18n()
const pluginName = ref(props.pluginId)

// 仅在"独立插件窗口"（URL 为 /plugin.html）渲染缩放手柄：主 SPA 经 #/plugin/<id>
// 路由也会挂载本组件，但那时 System.invoke("wails:resize:") 指向主窗口（本身 Frameless:false
// 原生可缩放，手柄多余且会盖住主内容），故须在独立窗口上下文才显示手柄。
const isStandaloneWindow = typeof window !== 'undefined' && window.location.pathname.includes('plugin.html')

const frameRef = ref<InstanceType<typeof PluginFrame> | null>(null)

// 独立窗口：init 走全局 pending init（跨窗口传入），前端在此页不主动注入。
// 关闭 = 隐藏复用（HidePluginWindow）：关窗只隐藏视图、WebView2 renderer 保留，
// 10 分钟内重开走复用路径秒开；与后端「关窗不杀」一致（后端进程仍常驻 running）。
// 真正销毁只发生在「回收超时 / 卸载」等程序性 Close，用户点 X 不再销毁窗口。
function closeWindow() {
  HidePluginWindow(props.pluginId)
}

// 刷新/重置：重建 iframe 让插件页回到初始状态（窗口与进程保持复用）
function refreshWindow() {
  frameRef.value?.reload()
}

// 强制关闭：窗口立即销毁（体感即时），杀进程由后端 goroutine 后台进行。
// 不能前端先 await KillPlugin 再关窗——后端杀进程（taskkill /T + Wait + PID 写盘）
// 耗时可达秒级，窗口会卡住才消失；也不能前端先关窗再杀——JS 上下文随窗口销毁、
// 调用发不出去。故整体下沉后端 ForceClosePluginWindow：同步 Close（快）+ go Kill（慢）。
function forceClose() {
  ForceClosePluginWindow(props.pluginId)
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
        <button class="pw-btn pw-btn-refresh" @click="refreshWindow" :title="t('refresh')">
          <RotateCw :size="13" />
        </button>
        <button class="pw-btn pw-btn-kill" @click="forceClose" :title="t('pluginForceClose')">
          <Power :size="13" />
        </button>
        <button class="pw-btn pw-btn-close" @click="closeWindow" :title="t('close')">
          <X :size="14" />
        </button>
      </div>
    </div>

    <!-- 内容区：统一插件宿主 -->
    <PluginFrame ref="frameRef" :plugin-id="props.pluginId" use-pending-init @title="pluginName = $event || pluginName" />

    <!-- 缩放手柄：覆盖在窗口边缘（z-index 高于 iframe，能捕获边缘 mousedown）；
         mousedown 即把缩放交给操作系统原生处理（wails:resize，丝滑无卡顿）。仅独立窗口渲染。 -->
    <template v-if="isStandaloneWindow">
      <div class="pw-rs pw-rs-e" @mousedown="beginResize('e-resize')"></div>
      <div class="pw-rs pw-rs-w" @mousedown="beginResize('w-resize')"></div>
      <div class="pw-rs pw-rs-s" @mousedown="beginResize('s-resize')"></div>
      <div class="pw-rs pw-rs-se" @mousedown="beginResize('se-resize')"></div>
      <div class="pw-rs pw-rs-sw" @mousedown="beginResize('sw-resize')"></div>
    </template>
  </div>
</template>

<style scoped>
.plugin-window {
  position: relative;
  display: flex; flex-direction: column;
  height: 100vh; width: 100vw; overflow: hidden;
  background: var(--color-bg-primary);
}

/* 缩放手柄：覆盖在窗口边缘（z-index 高于 iframe，能捕获边缘 mousedown）。
   完全透明、无任何视觉提示（不显示蓝色线条），缩放由 OS 原生接管；
   仅保留 resize 光标，让鼠标移到边缘时呈现对应方向的箭头反馈。 */
.pw-rs {
  position: absolute;
  z-index: 5;
  user-select: none;
}
/* 右边缘 */
.pw-rs-e { top: 0; right: 0; width: 5px; height: 100%; cursor: ew-resize; }
/* 左边缘 */
.pw-rs-w { top: 0; left: 0; width: 5px; height: 100%; cursor: w-resize; }
/* 下边缘 */
.pw-rs-s { left: 0; bottom: 0; width: 100%; height: 5px; cursor: ns-resize; }
/* 右下角 */
.pw-rs-se { right: 0; bottom: 0; width: 14px; height: 14px; cursor: nwse-resize; }
/* 左下角 */
.pw-rs-sw { left: 0; bottom: 0; width: 14px; height: 14px; cursor: nesw-resize; }

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
/* 强制关闭：与 X 同款危险色 hover，但图标不同（电源）作视觉区分 */
.pw-btn-kill:hover {
  background: var(--color-danger);
  color: #fff;
}
.pw-btn-max svg {
  transform: rotate(180deg);
}
.pw-btn-refresh:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
</style>
