<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { History, Trash2 } from '@lucide/vue'
import ActivityPage from './ActivityPage.vue'
import RecycleBinPage from './RecycleBinPage.vue'

// 「历史」= 最近动态 + 回收站，页内分段切换。
//
// 为什么合成一项：两者都是「回头看一眼」的回顾型信息，在菜单里各占一行会让数据组
// 看起来有四五个互不相干的东西。
//
// 为什么把分段控件塞进子页的页头（titles 插槽）而不是自己加一层工具条：两个子页都有
// 40px 页头（刷新 / 清空），再叠一条 40px 的分段条，顶部就白吃 80px，而设置面板本来不高。
// 塞进 titles 位后只有一行：左边切换、右边操作。
//
// 为什么不用 KeepAlive：这两页的数据都会被主界面改动（继续用就产生新动态、删除就进
// 回收站），缓存住会让人看到过期内容，而「回头看」这个动作本身就是要看最新的。
const { t } = useI18n()
const tab = ref<'activity' | 'recycle'>('activity')

const tabs = computed(() => [
  { value: 'activity' as const, label: t('navActivity'), icon: History },
  { value: 'recycle' as const, label: t('recycleBin'), icon: Trash2 },
])

const current = computed(() => (tab.value === 'activity' ? ActivityPage : RecycleBinPage))
</script>

<template>
  <div class="hist-page">
    <!-- 动态组件：两个子页的 embedded 契约一致，分段控件只写一份，切换即重新加载 -->
    <component :is="current" :embedded="true">
      <template #titles>
        <div class="hist-tabs">
          <button
            v-for="o in tabs"
            :key="o.value"
            :class="['qd-btn', 'qd-btn-sm', { 'is-on': tab === o.value }]"
            @click="tab = o.value"
          >
            <component :is="o.icon" :size="13" />
            <span>{{ o.label }}</span>
          </button>
        </div>
      </template>
    </component>
  </div>
</template>

<style scoped>
.hist-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
}
/* 分段控件落在页头标题位：横向紧挨，纵向由 .qd-page-header 的 align-items 居中 */
.hist-tabs {
  display: flex;
  align-items: center;
  gap: 6px;
}
</style>
