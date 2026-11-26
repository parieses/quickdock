<script setup lang="ts">
/**
 * 环境页左侧分组导航：分组标题（可折叠，运行中有小绿点）+ 分类按钮。
 *
 * 分组的计算（`sidebarGroups`）刻意留在页面里：它同时依赖 GROUP_ORDER、
 * 运行时列表、选中项、运行态缓存、搜索词与四个入口开关，抽出来是个十几个
 * 参数的 composable，比留在页面更难读。这里只负责渲染与把交互 emit 回去。
 */
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight } from '@lucide/vue'

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

const rootEl = ref<HTMLElement | null>(null)

/**
 * 把选中项滚进可视区。
 *
 * 侧栏自己是滚动容器（31 个运行时 + 7 个分组，高度远超视口），
 * 深链定位到靠后的区块时（aria2 在倒数第二组），右侧详情换了、左侧却
 * 看不见选中项，用户会以为「没跳对」。这里只调整侧栏的 scrollTop，
 * 不用 scrollIntoView —— 那会连带滚动外层可滚动的祖先。
 */
function revealActive() {
  const scroller = rootEl.value
  const el = scroller?.querySelector<HTMLElement>('.cat-btn.active')
  if (!scroller || !el) return
  const box = scroller.getBoundingClientRect()
  const item = el.getBoundingClientRect()
  const pad = 8
  if (item.top < box.top) {
    scroller.scrollTop -= box.top - item.top + pad
  } else if (item.bottom > box.bottom) {
    scroller.scrollTop += item.bottom - box.bottom + pad
  }
}

watch(() => props.selectedId, () => { void nextTick(revealActive) }, { immediate: true })
</script>

<template>
  <aside class="env-cats" ref="rootEl">
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
        <span class="cats-chevron">
          <ChevronRight v-if="grp.collapsed" :size="12" />
          <ChevronDown v-else :size="12" />
        </span>
      </button>
      <button
        v-for="it in grp.items"
        :key="it.id"
        class="cat-btn"
        :class="{ active: it.active, zero: it.count === 0 }"
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
  overflow-y: auto; padding: var(--space-5) 10px;
  display: flex; flex-direction: column; gap: var(--space-2);
}

/* 搜索框：31 个运行时要靠它定位，原来用 bg-tertiary + 弱边框，在深色底上
   几乎与背景融成一片。抬到 secondary 底色、加高一档、图标提亮，
   聚焦时边框染强调色，才看得出这是个输入框。 */
.cats-search {
  display: flex; align-items: center; gap: 6px; margin: 0 var(--space-3) var(--space-5);
  padding: 0 8px; height: 30px; flex-shrink: 0;
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); color: var(--color-text-muted);
  transition: border-color var(--transition-fast), background var(--transition-fast);
}

.cats-search:hover { border-color: var(--color-border-focus-soft); }

.cats-search:focus-within {
  border-color: var(--color-accent);
  background: var(--color-bg-primary);
  color: var(--color-accent);
}

.cats-search-input {
  flex: 1; min-width: 0; border: none; outline: none; background: none;
  color: var(--color-text-primary); font-size: var(--text-xs); font-family: inherit;
}

.cats-search-input::placeholder { color: var(--color-text-disabled); }

.cats-search-clear {
  padding: 0; border: none; background: none; color: var(--color-text-muted);
  font-size: var(--text-base); line-height: 1; cursor: pointer;
}

.cats-search-clear:hover { color: var(--color-text-primary); }

.cats-empty {
  padding: var(--space-6) 10px; font-size: var(--text-xs); text-align: center;
  color: var(--color-text-muted);
}

.cats-title {
  font-size: var(--text-xs); letter-spacing: 0.5px;
  color: var(--color-text-muted); padding: var(--space-1) var(--space-4) var(--space-4);
}

.cat-btn {
  display: flex; align-items: center; gap: 10px;
  padding: var(--space-4) 10px; border-radius: var(--radius-sm);
  border: 1px solid transparent; background: transparent;
  color: var(--color-text-secondary); cursor: pointer;
  font-family: inherit; font-size: var(--text-md); text-align: left; width: 100%;
  transition: background var(--transition-fast), border-color var(--transition-fast);
}

.cat-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.cat-avatar {
  width: 28px; height: 28px; border-radius: var(--radius-sm); flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: var(--text-md); font-weight: 600; color: var(--color-accent-text);
}

.cat-avatar :deep(svg) { width: 20px; height: 20px; display: block; }

.cat-meta { display: flex; flex-direction: column; min-width: 0; line-height: 1.25; }

.cat-name { font-weight: 600; font-size: var(--text-md); }

.cat-id { font-size: var(--text-2xs); color: var(--color-text-muted); }

/* 未安装（数量 0）：图标降透明度、常字重降低一档，让「装了东西的那几项」
   在长列表里先跳出来。不用 display:none 之类的隐藏——入口还得能点进去装。 */
.cat-btn.zero .cat-avatar { opacity: 0.42; filter: saturate(0.5); }
.cat-btn.zero .cat-name { font-weight: 500; color: var(--color-text-muted); }
.cat-btn.zero .cat-id { opacity: 0.6; }

.cat-trail { margin-left: auto; display: flex; align-items: center; justify-content: flex-end; gap: 6px; }

.cat-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--color-success); box-shadow: 0 0 5px var(--color-success-border); flex-shrink: 0; }

/* 数量胶囊：定宽 + 数字等宽，31 项的角标才能对齐成一列竖线；
   否则 0/1/10 宽度不同，右缘参差。 */
.cat-count {
  min-width: 22px; height: 17px; box-sizing: border-box;
  display: inline-flex; align-items: center; justify-content: center;
  text-align: center; font-size: var(--text-2xs); padding: 0 5px; border-radius: var(--radius-full);
  font-variant-numeric: tabular-nums;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
}

.cat-btn.active .cat-count { background: var(--color-accent-bg); color: var(--color-accent); }

.cats-sep { height: 1px; background: var(--color-border); margin: var(--space-4) var(--space-3); flex-shrink: 0; }

/* 分组标题：12px/700 + secondary 色，靠「字重差」把分组边界立起来。
   间距收到 8px：一屏能看到的分组更多，折叠状态（只有标题）时尤其明显—— 
   原来 16px 上间距 + 6/4 内边距，7 个标题排下来像 7 条互不相干的条目。 */
.cats-group-label {
  display: flex; align-items: center; gap: var(--space-4);
  width: 100%; text-align: left; cursor: pointer; font-family: inherit;
  font-size: var(--text-sm); font-weight: 700; letter-spacing: 0.02em;

  color: var(--color-text-secondary);
  background: none;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  padding: 3px var(--space-4); margin-top: 8px;
  transition: background var(--transition-fast), color var(--transition-fast);
}

.cats-group-label:hover {
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
}

.cats-chevron { margin-left: auto; font-size: var(--text-xs); line-height: 1; color: var(--color-text-muted); transition: transform 120ms ease; }

/* 选中项与侧栏 / 三栏同一套：整行淡底 + 左缘竖条。
   原来用 border-color 变蓝，等于给这一行描了个亮蓝框，比页头的主按钮还抢。 */
.cat-btn { position: relative; }
.cat-btn.active {
  background: var(--color-accent-bg);
  border-color: transparent;
  color: var(--color-text-primary);
}
.cat-btn.active::before {
  content: '';
  position: absolute; left: 0; top: 50%; transform: translateY(-50%);
  width: 2px; height: 18px; border-radius: var(--radius-full);
  background: var(--color-accent);
}
.cat-btn.active .cat-name { font-weight: 600; }

.cat-count.zero { color: var(--color-text-muted); }
</style>
