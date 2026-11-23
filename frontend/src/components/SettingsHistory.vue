<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { History, Trash2 } from '@lucide/vue'
import ActivityPage from './ActivityPage.vue'
import RecycleBinPage from './RecycleBinPage.vue'
import QdTabs from './QdTabs.vue'

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
// 深链 'history:recycle' 直接落到回收站（回收站清空/恢复的通知点进来就是这个）。
// 与 AI 设置页同理：本组件挂在上层的 v-if 分支里，重建即读到新值。
const props = defineProps<{ initialTab?: string }>()
const tab = ref<'activity' | 'recycle'>(props.initialTab === 'recycle' ? 'recycle' : 'activity')

const tabs = computed(() => [
  { id: 'activity' as const, label: t('navActivity') },
  { id: 'recycle' as const, label: t('recycleBin') },
])
// 图标是内容不是皮肤：QdTabs 的 tabs 数据只认 {id,label}，图标走 #tab 插槽按 id 取。
const tabIcon = (id: string) => (id === 'activity' ? History : Trash2)

const current = computed(() => (tab.value === 'activity' ? ActivityPage : RecycleBinPage))
</script>

<template>
  <div class="hist-page">
    <!-- 动态组件：两个子页的 embedded 契约一致，分段控件只写一份，切换即重新加载 -->
    <component :is="current" :embedded="true">
      <template #titles>
        <div class="hist-tabs">
          <QdTabs v-model="tab" :tabs="tabs" variant="segmented" :aria-label="t('setHistory')">
            <template #tab="{ tab: item }">
              <component :is="tabIcon(item.id)" :size="13" />
              <span>{{ item.label }}</span>
            </template>
          </QdTabs>
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
  gap: var(--space-3);
}
</style>
