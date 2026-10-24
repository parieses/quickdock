<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { navBadges } from '../composables/useNavBadges'

// 角标渲染抽成组件的原因：Sidebar 的导航项有 4 个分支（page / window / dsh / plugins），
// 每支都要挂角标。抽出来比在四个分支里复制同一段 v-if 好维护。
// 注意样式仍写在 Sidebar.vue 的 scoped 里 —— 子组件根节点会带上父级的 scoped 标记，
// 所以 .sidebar.collapsed .nav-badge 这类父级选择器照样命中。
const props = defineProps<{ id: string }>()
const { t } = useI18n()
const { badgeOf } = navBadges()

const badge = computed(() => badgeOf(props.id))
const text = computed(() => {
  const b = badge.value
  if (!b) return ''
  return b.count > 99 ? '99+' : String(b.count)
})
</script>

<template>
  <span
    v-if="badge"
    class="nav-badge"
    :class="`tone-${badge.tone}`"
    :title="t(badge.titleKey, { n: badge.count })"
  >{{ text }}</span>
</template>
