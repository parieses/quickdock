<script setup lang="ts">
/**
 * 一级页面统一页头 —— docs/conventions/ui.md §3.1 / §4 的组件化落地。
 *
 * 输出与 styles/app.css 的 .qd-page-header 三段式完全一致，只是把此前散在
 * 十几个页面里逐字复制的外壳 DOM 收敛成一个来源。以后调页头高度、内边距、
 * 加返回按钮/面包屑，只改这里，不再逐页翻找。
 *
 * 为什么 fill / actions 由调用方原样透传、组件不额外包裹：
 * 每页的中间控件（搜索框、标签条、月份导航、引擎状态）和右侧动作（含
 * v-if、title、事件、scoped 样式）全都不同，包一层只会让 scoped 选择器和
 * 条件渲染变得难以推理。组件只保证「外壳 + 标题区」这两块全应用一致。
 *
 * 为什么保留 #titles 插槽而不只给 title prop：
 * 标题位常带图标、计数 badge、问号按钮，ActivityPage/RecycleBinPage 嵌进设置
 * 时还要由外层塞分段控件。slot 内容属于调用方作用域，父页面的 scoped 样式
 * （如 .clipboard-panel.compact .qd-page-title）照样生效。
 *
 * 注意：本组件模板必须直接写 .qd-page-header 这个 div，不能引用自己。
 */
withDefaults(defineProps<{
  /** 纯文本标题；带图标/badge 时用 #titles 插槽 */
  title?: string
  /** 纯文本副标题；带条件渲染时用 #titles 插槽 */
  subtitle?: string
}>(), {
  title: '',
  subtitle: '',
})
</script>

<template>
  <div class="qd-page-header">
    <div class="qd-page-titles">
      <slot name="titles">
        <h2 v-if="title" class="qd-page-title">{{ title }}</h2>
        <span v-if="subtitle" class="qd-page-sub">{{ subtitle }}</span>
      </slot>
    </div>
    <slot />
  </div>
</template>
