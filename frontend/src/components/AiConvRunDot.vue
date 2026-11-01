<script setup lang="ts">
// 会话行上的运行状态点。
//
// 三个状态各有各的含义，别合并：
//   running = 这一轮还在生成（用户可能已经切走了，回答仍在继续）
//   done    = 生成完了，但用户还没回来打开这条对话 → 绿点，看过了就消
//   error   = 后台那条失败了 → 红点（报错细节要切进去才看得到）
//
// 形状与导航栏的「后台活动」点一致（见 Sidebar.vue 的 .nav-dot），
// 因为说的是同一件事：有东西在跑 / 有东西你还没看。
import { useI18n } from 'vue-i18n'

defineProps<{ state?: 'running' | 'done' | 'error' }>()

const { t } = useI18n()
</script>

<template>
  <span
    v-if="state"
    class="ai-conv-run"
    :class="state"
    :title="t(state === 'running' ? 'aiConvRunning' : state === 'done' ? 'aiConvDone' : 'aiConvFailed')"
  />
</template>

<style scoped>
.ai-conv-run {
  flex-shrink: 0;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-accent);
}
/* 跑着的时候呼吸：静态点会被当成「一个装饰」，这一条是「此刻在动」 */
.ai-conv-run.running { animation: ai-conv-run-pulse 1.1s ease-in-out infinite; }
@keyframes ai-conv-run-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.25; } }
.ai-conv-run.done { background: var(--color-success); }
.ai-conv-run.error { background: var(--color-danger); }
</style>
