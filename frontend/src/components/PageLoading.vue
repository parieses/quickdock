<script setup lang="ts">
/**
 * 页面 chunk 首次加载时的占位骨架。
 *
 * registry.ts 里所有内容页都是 defineAsyncComponent(() => import(...))，Vue 在
 * Promise 落地前渲染的是一个注释节点 —— 表现就是「点导航后内容区先空一拍」。
 * 这里给它们统一配 loadingComponent（见 pages/registry.ts 的 lazy()）。
 *
 * 刻意只做「页面轮廓」而不是转圈：转圈和空白一样没有信息量，而页头 + 卡片网格
 * 至少告诉用户「这个页面正在来，它大致长这样」。形状是多数列表/总览页的公约数，
 * 不模仿任何一页的具体布局，免得骨架和真实内容对不上反而像闪了一下。
 */
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>

<template>
  <div class="page-loading" role="status" :aria-label="t('loading')">
    <div class="pl-head">
      <div class="pl-line pl-title"></div>
      <div class="pl-line pl-sub"></div>
    </div>
    <div class="pl-grid">
      <div v-for="n in 4" :key="n" class="pl-card">
        <div class="pl-line pl-card-title"></div>
        <div class="pl-line pl-row"></div>
        <div class="pl-line pl-row pl-mid"></div>
        <div class="pl-line pl-row pl-short"></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* .page-shell 是 display:contents，这里就是 .app-content 的直接 flex 子项，
   必须自己撑满；clipboard-page 那条分支是 flex column，flex:1 同样成立。 */
.page-loading {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: 20px;
  padding: 24px;
  overflow: hidden;
}

.pl-head { display: flex; flex-direction: column; gap: 10px; flex-shrink: 0; }

.pl-grid {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  grid-auto-rows: minmax(120px, auto);
  gap: 14px;
  align-content: start;
}

.pl-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-card-bg);
}

/* 灰条用 bg-tertiary：它比卡片底高一级，深浅主题下都看得见但不刺眼。
   shimmer 叠一层中性灰半透明，避免在深色主题里泛白、浅色主题里发黑。 */
.pl-line {
  position: relative;
  height: 10px;
  border-radius: 5px;
  background: var(--color-bg-tertiary);
  overflow: hidden;
}

.pl-line::after {
  content: '';
  position: absolute;
  inset: 0;
  transform: translateX(-100%);
  background: linear-gradient(90deg, transparent, rgba(127, 127, 127, .16), transparent);
  animation: pl-shimmer 1.3s ease-in-out infinite;
}

.pl-title { width: 150px; height: 18px; }
.pl-sub { width: 240px; height: 10px; }
.pl-card-title { width: 45%; height: 12px; }
.pl-row { width: 100%; }
.pl-mid { width: 78%; }
.pl-short { width: 55%; }

@keyframes pl-shimmer { to { transform: translateX(100%); } }

@media (prefers-reduced-motion: reduce) {
  .pl-line::after { animation: none; }
}
</style>
