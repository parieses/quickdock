<script setup lang="ts">
/**
 * 环境页左侧分组导航：分组标题（可折叠，运行中有小绿点）+ 分类按钮。
 *
 * 分组的计算（`sidebarGroups`）刻意留在页面里：它同时依赖 GROUP_ORDER、
 * 运行时列表、选中项、运行态缓存、搜索词与四个入口开关，抽出来是个十几个
 * 参数的 composable，比留在页面更难读。这里只负责渲染与把交互 emit 回去。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

/** 侧栏的一项：运行时分两类，special 是 git / everything 这种非服务入口 */
interface SidebarItem {
  kind: 'runtime' | 'special'
  id: string
  name: string
  /** special 项用的内联 svg；runtime 项走 avatarIcon() */
  avatar: string
  color: string
  /** 该运行时已安装版本数；undefined 表示不显示角标 */
  count?: number
  running?: boolean
  active: boolean
}

interface SidebarGroup {
  key: string
  labelKey: string
  items: SidebarItem[]
  collapsed: boolean
  /** 组内有任何一项在跑；折叠时也要能看到 */
  running: boolean
}

const props = defineProps<{
  query: string
  groups: SidebarGroup[]
  selectedId: string
  /** runtime 项的图标（内联 svg 字符串，见页面的 RUNTIME_ICONS） */
  avatarIcon: (id: string) => string
}>()

const emit = defineEmits<{
  (e: 'update:query', v: string): void
  (e: 'toggle-group', key: string): void
  (e: 'select', id: string): void
}>()

const { t } = useI18n()

// 搜索态：清空输入框即回到「按分组浏览」
const searching = computed(() => props.query.trim().length > 0)
</script>

<template>
  <aside class="env-cats">
    <div class="cats-title">{{ t('envCategories') }}</div>
    <div class="cats-search">
      <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg>
      <input :value="props.query" class="cats-search-input" :placeholder="t('envSearchPh')" @input="emit('update:query', ($event.target as HTMLInputElement).value)" />
      <button v-if="props.query" class="cats-search-clear" @click="emit('update:query', '')" :title="t('clear')">×</button>
    </div>
    <div v-if="searching && props.groups.length === 0" class="cats-empty">{{ t('envNoMatch') }}</div>
    <template v-for="grp in props.groups" :key="grp.key">
      <button class="cats-group-label" type="button" @click="emit('toggle-group', grp.key)">
        <span v-if="grp.running" class="cat-dot" :title="t('svcRunning')"></span>
        <span>{{ t(grp.labelKey) }}</span>
        <span class="cats-chevron">{{ grp.collapsed ? '▸' : '▾' }}</span>
      </button>
      <button
        v-for="it in grp.items"
        :key="it.id"
        class="cat-btn"
        :class="{ active: it.active }"
        @click="emit('select', it.id)"
      >
        <span class="cat-avatar" :style="{ background: it.color }" v-html="it.kind === 'runtime' ? props.avatarIcon(it.id) : it.avatar"></span>
        <span class="cat-meta">
          <span class="cat-name">{{ it.name }}</span>
          <span class="cat-id">{{ it.id }}</span>
        </span>
        <span class="cat-trail">
          <span v-if="it.running" class="cat-dot" :title="t('svcRunning')"></span>
          <span v-if="it.count !== undefined" class="cat-count" :class="{ zero: !it.count }">{{ it.count }}</span>
        </span>
      </button>
    </template>
  </aside>
</template>

<style scoped>
.env-cats {
  width: 196px; flex-shrink: 0;
  border-right: 1px solid var(--color-border);
  background: var(--color-bg-primary);
  overflow-y: auto; padding: 12px 10px;
  display: flex; flex-direction: column; gap: 4px;
}

.cats-search {
  display: flex; align-items: center; gap: 5px; margin: 0 8px 8px;
  padding: 0 7px; height: 26px; flex-shrink: 0;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); color: var(--color-text-disabled);
}

.cats-search:focus-within { border-color: var(--color-accent); }

.cats-search-input {
  flex: 1; min-width: 0; border: none; outline: none; background: none;
  color: var(--color-text-primary); font-size: 11px; font-family: inherit;
}

.cats-search-input::placeholder { color: var(--color-text-disabled); }

.cats-search-clear {
  padding: 0; border: none; background: none; color: var(--color-text-disabled);
  font-size: 14px; line-height: 1; cursor: pointer;
}

.cats-search-clear:hover { color: var(--color-text-primary); }

.cats-empty {
  padding: 16px 10px; font-size: 11px; text-align: center;
  color: var(--color-text-disabled);
}

.cats-title {
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px;
  color: var(--color-text-disabled); padding: 2px 8px 8px;
}

.cat-btn {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 10px; border-radius: var(--radius-sm);
  border: 1px solid transparent; background: transparent;
  color: var(--color-text-secondary); cursor: pointer;
  font-family: inherit; font-size: 13px; text-align: left; width: 100%;
  transition: background var(--transition-fast), border-color var(--transition-fast);
}

.cat-btn:hover { background: var(--color-bg-tertiary); color: var(--color-text-primary); }

.cat-avatar {
  width: 28px; height: 28px; border-radius: var(--radius-sm); flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 13px; font-weight: 700; color: var(--color-accent-text);
}

.cat-avatar :deep(svg) { width: 20px; height: 20px; display: block; }

.cat-meta { display: flex; flex-direction: column; min-width: 0; line-height: 1.25; }

.cat-name { font-weight: 600; font-size: 13px; }

.cat-id { font-size: 10px; color: var(--color-text-disabled); }

.cat-trail { margin-left: auto; display: flex; align-items: center; gap: 6px; }

.cat-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--color-success); box-shadow: 0 0 5px var(--color-success-border); flex-shrink: 0; }

.cat-count {
  min-width: 20px; text-align: center; font-size: 11px; padding: 1px 6px; border-radius: var(--radius-md);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}

.cats-sep { height: 1px; background: var(--color-border); margin: 8px 6px; flex-shrink: 0; }

.cats-group-label {
  display: flex; align-items: center; gap: 8px;
  width: 100%; text-align: left; cursor: pointer; font-family: inherit;
  font-size: 13px; font-weight: 700; letter-spacing: 0.3px;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-left: 3px solid var(--color-accent);
  border-radius: var(--radius-sm);
  padding: 9px 11px; margin-top: 9px;
  transition: background var(--transition-fast), border-color var(--transition-fast);
}

.cats-group-label:hover {
  background: var(--color-bg-hover, var(--color-bg-tertiary));
  border-color: var(--color-border-focus);
  border-left-color: var(--color-accent);
}

.cats-chevron { margin-left: auto; font-size: 11px; line-height: 1; color: var(--color-text-disabled); transition: transform 120ms ease; }

.cat-btn.active {
  background: var(--color-accent-bg);
  border-color: var(--color-border-focus);
  color: var(--color-text-primary);
}

.cat-count.zero { color: var(--color-text-disabled); }
</style>
