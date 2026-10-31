<script setup lang="ts">
/**
 * 工作空间复合页：首次引导 / 无场景 / 无集合 / 常规三栏 四档状态。
 *
 * 为什么四档留在一个组件里而不是拆四个：它们共用同一份状态（store 的
 * workspaces / scenes / collections 与两个 loading 标记），拆开就得把
 * 4 个 computed 传进来传出去，而每档之间的切换条件正是那些 computed。
 *
 * 为什么从 App.vue 搬出来：App 的内容区原本是「workspace 专属分支 + 注册页
 * <component :is>」两套写法，前者有 4 层嵌套。搬出来之后 App 只剩一套通用渲染，
 * 工作空间在注册表里跟 notes / monitor 等页完全同权 —— 差别只在本组件内部。
 *
 * 布局注意：外层 .page-shell 是 display:contents（见 App.vue），所以本组件的
 * 根节点会直接成为 .app-content（flex column）的子项，得自己撑满并管好纵向排列。
 */
import { computed, defineAsyncComponent } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
import SceneTags from './SceneTags.vue'
import SceneRail from './SceneRail.vue'
import CollectionList from './CollectionList.vue'
import ItemList from './ItemList.vue'

// 两档整页引导 + 首次引导只在工作空间出现，异步化避免进主 chunk
const OnboardingPage = defineAsyncComponent(() => import('./OnboardingPage.vue'))
const EmptyGuide = defineAsyncComponent(() => import('./EmptyGuide.vue'))

const store = useWorkspaceStore()
const { t } = useI18n()
const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

// 已选到工作空间、场景列表加载完毕、但一个场景都没有。
// 必须带 !scenesLoading，否则切换工作空间时 scenes 先被清空，引导页会闪一下。
// 这一档要占满整个内容区（连场景栏一起收掉），否则会留下一个空荡荡的场景列。
const showWorkspaceGuide = computed(() =>
  store.workspaces.length > 0 &&
  store.scenes.length === 0 &&
  !store.scenesLoading &&
  !store.hasSearch
)

// 「当前场景下一个集合都没有」这一档：引导页占满集合 + 项目两块区域。
// 只有两个空列表并排、各自写一句「点击上方 +」时，用户看到的是两片空白。
// 同样要带 !collectionsLoading，否则切换场景时 collections 先被清空会闪。
const showCollectionsGuide = computed(() =>
  store.workspaces.length > 0 &&
  !!store.activeSceneId &&
  store.collections.length === 0 &&
  !store.collectionsLoading &&
  !store.hasSearch
)
</script>

<template>
  <div class="ws-root">
    <!-- 空状态引导页（首次使用，无工作空间） -->
    <OnboardingPage
      v-if="store.workspaces.length === 0"
      @open-settings="emit('open-settings', $event)"
    />

    <!-- 空状态引导页（已有工作空间，但里面一个场景都没有）：占满整个内容区 -->
    <EmptyGuide
      v-else-if="showWorkspaceGuide"
      variant="scene"
      @open-settings="emit('open-settings', $event)"
    />

    <!-- 常规内容 -->
    <template v-else>
      <div v-if="store.error" class="ws-error-bar">
        <span class="ws-error-text">{{ store.error }}</span>
        <button class="ws-error-close" @click="store.error = ''" :title="t('close')">✕</button>
      </div>
      <SceneTags />
      <div class="ws-body">
        <!-- 三栏起点：场景 → 集合 → 项目，共用同一条顶部基线 -->
        <SceneRail />
        <!-- 当前场景下没有集合：引导页占满「集合 + 项目」两块区域 -->
        <EmptyGuide
          v-if="showCollectionsGuide"
          variant="collection"
          @open-settings="emit('open-settings', $event)"
        />
        <template v-else>
          <CollectionList class="ws-collections" />
          <ItemList class="ws-items" />
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
/* 根节点直接作为 .app-content（flex column）的子项：撑满剩余高度、自己管纵向排列 */
.ws-root {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* 场景 → 集合 → 项目 同一行，共用一条顶部基线 */
.ws-body {
  flex: 1;
  display: flex;
  overflow: hidden;
}

.ws-collections { flex-shrink: 0; }
.ws-items { flex: 1; min-width: 0; }

.ws-error-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 12px 0;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  color: var(--color-danger);
  font-size: 12px;
}
.ws-error-text { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-error-close {
  background: none;
  border: none;
  cursor: pointer;
  color: inherit;
  font-size: 12px;
  padding: 2px 4px;
  opacity: 0.7;
}
.ws-error-close:hover { opacity: 1; }
</style>
