<script setup lang="ts">
/**
 * 页面 chunk 首次加载时的占位骨架。
 *
 * registry.ts 里所有内容页都是 defineAsyncComponent(() => import(...))，Vue 在
 * Promise 落地前渲染的是一个注释节点 —— 表现就是「点导航后内容区先空一拍」。
 * 这里给它们统一配 loadingComponent（见 pages/registry.ts 的 lazy()）。
 *
 * 版式刻意去贴真实页面的公共框架（styles/app.css 的 .qd-page-header /
 * .qd-page-body / .qd-card）：页头 46px 通栏、内容区左右 20、卡片只靠亮度分层。
 * 旧版是「整页 24px padding + 页头两条占位 + 四张 1px 描边卡片」，跟任何一页
 * 都对不上：真实页头出现时凭空多出一条通栏、整页往下跳，卡片还多一圈亮边。
 * 贴框架之后，骨架到真实内容的切换只剩下「灰条变成文字」这一层变化。
 *
 * 内容形状仍取公约数（通栏概览 + 两列卡 + 列表），不模仿某一页的具体布局，
 * 免得骨架和真实内容对不上反而像闪了一下。
 */
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>

<template>
  <div class="page-loading" role="status" :aria-label="t('loading')">
    <!-- 页头：与 .qd-page-header 同高、同底色、同分隔线 -->
    <div class="pl-header">
      <div class="pl-bar pl-header-title"></div>
      <div class="pl-actions">
        <div class="pl-bar pl-action"></div>
        <div class="pl-bar pl-action pl-action-sm"></div>
      </div>
    </div>

    <div class="pl-body">
      <!-- 通栏概览：多数页第一块是横向状态带 / 今日卡 -->
      <div class="pl-card pl-overview">
        <div class="pl-bar pl-card-title"></div>
        <div class="pl-bar pl-line pl-w-55"></div>
      </div>

      <div class="pl-columns">
        <div v-for="col in 2" :key="col" class="pl-card">
          <div class="pl-bar pl-card-title"></div>
          <div class="pl-bar pl-line"></div>
          <div class="pl-bar pl-line pl-w-70"></div>
          <div class="pl-bar pl-line pl-w-40"></div>
        </div>
      </div>

      <!-- 列表区：列表 / 日志 / 历史类页面的主体 -->
      <div class="pl-card pl-list">
        <div v-for="n in 3" :key="n" class="pl-list-row">
          <div class="pl-bar pl-dot"></div>
          <div class="pl-bar pl-line" :class="n === 2 ? 'pl-row-short' : 'pl-row-long'"></div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* .page-shell 是 display:contents，这里就是 .app-content 的直接 flex 子项，
   必须自己撑满；clipboard-page 那条分支是 flex column，flex:1 同样成立。
   材质档下 style.css 的 .app-content > * > * 会把这层底透明化，与真实页面
   根节点一致，由 .app-content 提供唯一那层窗口底。 */
.page-loading {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: var(--color-bg-primary);
}

.pl-header {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
  min-height: 46px;
  padding: 0 var(--space-6);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.pl-actions { display: flex; gap: var(--space-2); margin-left: auto; }

/* 与 .qd-page-body 的 18/20/24 对齐：上边取 20，2px 差在切换瞬间看不出来，
   但换来全部取值走 token。 */
.pl-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-7) var(--space-7) var(--space-8);
  overflow: hidden;
}

/* 卡片三件套直接复刻 .qd-card：card-bg + 浅色专用描边 + 无阴影。
   旧版自己写 1px solid --color-border，深色下比真实卡片多一圈亮边。 */
.pl-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  flex-shrink: 0;
  padding: var(--space-6);
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-card);
}

.pl-overview { gap: var(--space-5); }

.pl-columns {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-5);
  flex-shrink: 0;
}
.pl-columns .pl-card { min-height: 132px; }

.pl-list { gap: var(--space-2); padding: var(--space-4) var(--space-6); }
.pl-list-row { display: flex; align-items: center; gap: var(--space-4); height: 28px; }
/* 行内按「方块 + 条」的比例给宽，条可收缩，避免百分比宽度 + 固定方块溢出卡片。
   不能复用卡片里的 pl-w-*（那些是相对整卡的 100% 宽度）。 */
.pl-list-row .pl-line { flex-shrink: 1; }
.pl-list-row .pl-row-long { width: 62%; }
.pl-list-row .pl-row-short { width: 40%; }

/* 占位条用「活动态」实色 token：深色提亮、浅色压深，两套主题都成立。
   旧版叠半透明中性灰 shimmer，深色泛白、浅色发黑，和周围 token 不同源。
   所有条同步呼吸，不逐条扫光 —— 骨架常只出现一两百毫秒，扫光扫不完
   一轮，只会看见一堆条在乱动。 */
.pl-bar {
  flex-shrink: 0;
  height: 10px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-active);
  animation: pl-breathe 1.5s ease-in-out infinite;
}

.pl-header-title { width: 96px; height: 14px; }
.pl-action { width: 64px; height: 24px; border-radius: var(--radius-sm); }
.pl-action-sm { width: 24px; }
.pl-card-title { width: 34%; height: 12px; }
.pl-line { width: 100%; height: 9px; }
.pl-dot { width: 14px; height: 14px; }
.pl-w-70 { width: 70%; }
.pl-w-55 { width: 55%; }
.pl-w-40 { width: 40%; }

@keyframes pl-breathe {
  0%, 100% { opacity: .55; }
  50% { opacity: 1; }
}

@media (prefers-reduced-motion: reduce) {
  .pl-bar { animation: none; opacity: .75; }
}

@media (max-width: 760px) {
  .pl-columns { grid-template-columns: 1fr; }
  .pl-columns .pl-card:last-child { display: none; }
}
</style>
