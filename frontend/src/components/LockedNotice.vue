<script lang="ts" setup>
// 浮窗的锁定提示（只读，不含输入框）。
//
// 为什么浮窗不直接复用 LockScreen：密码输入只保留在主窗口一处。浮窗尺寸各不相同
// （剪贴板 480×540、命令面板、SSH、窗口排版），每个都要适配一套锁层布局；更麻烦的是
// 浮窗底下的面板都挂着 document 级键盘监听，密码框里的按键会漏下去——回车被当成
// 「复制并关闭」、字符被发给 SSH 远端会话。
//
// 正常路径下这个组件根本不会出现：锁定期间浮窗类热键由 tray.go 的 lockBlocksHotkey
// 拦住，锁定动作本身也会收起所有浮窗。它是防漏的兜底——万一将来新增了打开浮窗的
// 入口忘了加锁判断，最坏也只是露出这一屏提示，而不是用户数据。
import { Lock } from '@lucide/vue'
import { useI18n } from 'vue-i18n'
import { logErr } from '../utils/logger'
import { ShowMainWindow } from '../../bindings/quickdock/services/appservice'

const { t } = useI18n()

async function openMain() {
  try {
    await ShowMainWindow()
  } catch (e) {
    logErr('LockedNotice.openMain', e)
  }
}
</script>

<template>
  <div class="notice-root">
    <!-- 浮窗是无边框的，锁层一盖就没地方拖窗口了，顶部留一条极窄拖拽带 -->
    <div class="notice-drag" aria-hidden="true"></div>
    <div class="notice-card">
      <div class="notice-icon"><Lock :size="20" :stroke-width="2" /></div>
      <h1 class="notice-title">{{ t('lockTitle') }}</h1>
      <p class="notice-desc">{{ t('lockNoticeDesc') }}</p>
      <button class="notice-btn" @click="openMain">{{ t('lockNoticeOpen') }}</button>
    </div>
  </div>
</template>

<style scoped>
.notice-root {
  position: fixed;
  inset: 0;
  z-index: 30000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  background: var(--color-bg-primary);
}

.notice-drag {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 24px;
  --wails-draggable: drag;
}

.notice-card {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  max-width: 320px;
  text-align: center;
}

.notice-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 42px;
  height: 42px;
  color: var(--color-accent);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
}

.notice-title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.notice-desc {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.notice-btn {
  margin-top: 4px;
  padding: 0 14px;
  height: 34px;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--color-accent-text);
  background: var(--color-accent);
  border: none;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color 140ms ease;
}

.notice-btn:hover {
  background: var(--color-accent-hover);
}
</style>
