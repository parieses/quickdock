<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { toCamelCase } from '../utils/caseConversion'
import { ListScenes } from '../../bindings/quickdock/services/scene/sceneservice'
import { ListCollections } from '../../bindings/quickdock/services/collection/collectionservice'
import { ListItems } from '../../bindings/quickdock/services/item/itemservice'
import { useWorkspaceStore } from '../stores/workspace'
import type { CollectionItem, Collection, Scene } from '../types'
import TypeIcon from './TypeIcon.vue'
import { LoaderCircle, X, Layers, ChevronRight } from '@lucide/vue'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ select: [item: CollectionItem]; cancel: [] }>()

const { t } = useI18n()
const store = useWorkspaceStore()

// 三级层级：场景 → 集合 → 项目。选择器自带加载逻辑，不依赖/不污染工作区全局 store 的
// 单一激活态（store 的 scenes/collections/items 是「当前激活场景/集合」懒加载的，
// 直接复用既会破坏用户导航态、数据又不齐）。
const scenes = ref<Scene[]>([])
const collections = ref<Collection[]>([])
const items = ref<CollectionItem[]>([])
const activeScene = ref<Scene | null>(null)
const activeCollection = ref<Collection | null>(null)
const level = ref<'scene' | 'collection' | 'item'>('scene')
const loading = ref(false)
const err = ref('')

function normalize(rows: any[] | null): any[] {
  return (rows ?? []).map((r) => toCamelCase(r))
}

async function loadScenes() {
  const wsId = store.activeWorkspaceId
  if (!wsId) { err.value = t('sched_picker_no_scene'); return }
  loading.value = true; err.value = ''
  try {
    scenes.value = normalize(unwrap<any[]>(await ListScenes(wsId))) as Scene[]
  } catch (e) { err.value = getErrorMessage(e) }
  finally { loading.value = false }
}

watch(() => props.visible, (v) => {
  if (v) { reset(); void loadScenes() }
})

function reset() {
  scenes.value = []; collections.value = []; items.value = []
  activeScene.value = null; activeCollection.value = null
  level.value = 'scene'; err.value = ''
}

async function pickScene(s: Scene) {
  activeScene.value = s
  level.value = 'collection'
  loading.value = true; err.value = ''
  try {
    collections.value = normalize(unwrap<any[]>(await ListCollections(s.id))) as Collection[]
  } catch (e) { err.value = getErrorMessage(e) }
  finally { loading.value = false }
}

async function pickCollection(c: Collection) {
  activeCollection.value = c
  level.value = 'item'
  loading.value = true; err.value = ''
  try {
    items.value = normalize(unwrap<any[]>(await ListItems(c.id))) as CollectionItem[]
  } catch (e) { err.value = getErrorMessage(e) }
  finally { loading.value = false }
}

function chooseItem(it: CollectionItem) { emit('select', it) }

function back() {
  if (level.value === 'item') { level.value = 'collection'; items.value = [] }
  else if (level.value === 'collection') { level.value = 'scene'; collections.value = [] }
}

function close() { emit('cancel') }
</script>

<template>
  <div v-if="visible" class="modal-mask" @click.self="close">
    <div class="modal">
      <h3>
        {{ t('sched_picker_title') }}
        <button class="modal-x" @click="close" :title="t('cancel')"><X :size="16" /></button>
      </h3>

      <!-- 面包屑：点任一层跳回（数据已缓存，不重新拉取） -->
      <div class="crumb">
        <button class="crumb-item" :class="{ on: level === 'scene' }" @click="level = 'scene'">{{ t('sched_picker_scene') }}</button>
        <template v-if="activeScene">
          <ChevronRight :size="13" class="crumb-sep" />
          <button class="crumb-item" :class="{ on: level === 'collection' }" @click="level = 'collection'">{{ activeScene.name }}</button>
        </template>
        <template v-if="activeCollection">
          <ChevronRight :size="13" class="crumb-sep" />
          <button class="crumb-item" :class="{ on: level === 'item' }" @click="level = 'item'">{{ activeCollection.name }}</button>
        </template>
      </div>

      <div class="picker-body">
        <div v-if="loading" class="picker-loading">
          <LoaderCircle :size="18" class="spin" /> {{ t('sched_picker_loading') }}
        </div>
        <div v-else-if="err" class="picker-err">{{ err }}</div>

        <!-- 场景层 -->
        <ul v-else-if="level === 'scene'" class="picker-list">
          <li v-if="!scenes.length" class="picker-empty">{{ t('sched_picker_no_scene') }}</li>
          <li v-for="s in scenes" :key="s.id" class="qd-row" @click="pickScene(s)">
            <span class="qd-row-icon"><Layers :size="17" /></span>
            <span class="qd-row-main">
              <span class="qd-row-title">{{ s.name }}</span>
              <span class="qd-row-sub">{{ s.type }}</span>
            </span>
            <span class="qd-row-actions"><ChevronRight :size="14" /></span>
          </li>
        </ul>

        <!-- 集合层 -->
        <ul v-else-if="level === 'collection'" class="picker-list">
          <li v-if="!collections.length" class="picker-empty">{{ t('sched_picker_no_collection') }}</li>
          <li v-for="c in collections" :key="c.id" class="qd-row" @click="pickCollection(c)">
            <span class="qd-row-icon"><TypeIcon :type="c.type || '目录集合'" :size="17" /></span>
            <span class="qd-row-main">
              <span class="qd-row-title">{{ c.name }}</span>
              <span class="qd-row-sub">{{ c.type }}</span>
            </span>
            <span class="qd-row-actions"><ChevronRight :size="14" /></span>
          </li>
        </ul>

        <!-- 项目层 -->
        <ul v-else class="picker-list">
          <li v-if="!items.length" class="picker-empty">{{ t('sched_picker_no_item') }}</li>
          <li v-for="it in items" :key="it.id" class="qd-row" @click="chooseItem(it)">
            <span class="qd-row-icon">
              <img v-if="it.icon" :src="it.icon" class="item-icon-img" alt="" />
              <TypeIcon v-else :type="it.type" :size="17" />
            </span>
            <span class="qd-row-main">
              <span class="qd-row-title">{{ it.name }}</span>
              <span class="qd-row-sub">{{ it.value }}</span>
            </span>
          </li>
        </ul>
      </div>

      <div class="modal-actions">
        <button class="qd-btn" @click="back" :disabled="level === 'scene'">{{ t('back') }}</button>
        <button class="qd-btn qd-btn-primary" @click="close">{{ t('done') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.modal-mask {
  position: fixed; inset: 0; background: var(--color-bg-overlay);
  display: flex; align-items: center; justify-content: center; z-index: 110;
}
.modal {
  width: 460px; max-width: 92vw; max-height: 86vh;
  display: flex; flex-direction: column;
  background: var(--color-surface); border-radius: var(--radius-lg);
  padding: var(--space-5); box-shadow: var(--shadow-lg);
}
h3 {
  margin: 0; display: flex; align-items: center; justify-content: space-between;
  font-size: 14px; font-weight: 600; color: var(--color-text-primary);
}
.modal-x {
  border: none; background: none; color: var(--color-text-disabled);
  cursor: pointer; padding: 2px; border-radius: var(--radius-sm); line-height: 0;
}
.modal-x:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.crumb { display: flex; align-items: center; gap: 4px; margin: var(--space-3) 0; flex-wrap: wrap; }
.crumb-item {
  border: none; background: none; padding: 3px 6px; border-radius: var(--radius-sm);
  font-size: 12px; color: var(--color-text-secondary); cursor: pointer; font-family: inherit;
}
.crumb-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.crumb-item.on { color: var(--color-accent); background: var(--color-accent-bg); }
.crumb-sep { color: var(--color-text-disabled); flex-shrink: 0; }

.picker-body {
  flex: 1; overflow-y: auto; min-height: 140px; max-height: 52vh;
  border: 1px solid var(--color-border); border-radius: var(--radius-md); margin-top: 2px;
}
.picker-list { list-style: none; margin: 0; padding: var(--space-1); display: flex; flex-direction: column; gap: 2px; }
/* .qd-row 及其子元素（.qd-row-icon/.qd-row-main/.qd-row-title/.qd-row-sub/.qd-row-actions）
   是 panels.css 的公共类，这里只补 picker 特有的交互态 */
.picker-list .qd-row { cursor: pointer; }
.item-icon-img { width: 20px; height: 20px; object-fit: contain; border-radius: var(--radius-xs); }
.picker-empty, .picker-err, .picker-loading {
  padding: var(--space-7) var(--space-2); text-align: center;
  font-size: 12px; color: var(--color-text-disabled);
}
.picker-err { color: var(--color-danger); }
.picker-loading .spin {
  animation: picker-spin 1s linear infinite; vertical-align: -3px; margin-right: 6px;
}
@keyframes picker-spin { to { transform: rotate(360deg); } }

.modal-actions { display: flex; justify-content: space-between; gap: var(--space-3); margin-top: var(--space-5); }
</style>
