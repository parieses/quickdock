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
import QdModal from './QdModal.vue'
import { LoaderCircle, Layers, ChevronRight } from '@lucide/vue'

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
  pickSeq++
  scenes.value = []; collections.value = []; items.value = []
  activeScene.value = null; activeCollection.value = null
  level.value = 'scene'; err.value = ''
}

// 逐级下钻共用一个 loading，请求必须带序号：连点 A 场景再点 B 场景时，
// A 的响应后到会把 B 的集合列表覆盖掉（标题是 B、列表是 A）。
let pickSeq = 0

async function pickScene(s: Scene) {
  activeScene.value = s
  level.value = 'collection'
  const seq = ++pickSeq
  loading.value = true; err.value = ''
  try {
    const list = normalize(unwrap<any[]>(await ListCollections(s.id))) as Collection[]
    if (seq !== pickSeq || activeScene.value?.id !== s.id) return
    collections.value = list
  } catch (e) {
    if (seq !== pickSeq) return
    err.value = getErrorMessage(e)
  }
  finally { if (seq === pickSeq) loading.value = false }
}

async function pickCollection(c: Collection) {
  activeCollection.value = c
  level.value = 'item'
  const seq = ++pickSeq
  loading.value = true; err.value = ''
  try {
    const list = normalize(unwrap<any[]>(await ListItems(c.id))) as CollectionItem[]
    if (seq !== pickSeq || activeCollection.value?.id !== c.id) return
    items.value = list
  } catch (e) {
    if (seq !== pickSeq) return
    err.value = getErrorMessage(e)
  }
  finally { if (seq === pickSeq) loading.value = false }
}

function chooseItem(it: CollectionItem) { emit('select', it) }

function back() {
  pickSeq++ // 返回上层时作废在途请求，别让它在回到上层后又把内容塞回来
  loading.value = false
  if (level.value === 'item') { level.value = 'collection'; items.value = [] }
  else if (level.value === 'collection') { level.value = 'scene'; collections.value = [] }
}

function close() { emit('cancel') }
</script>

<template>
  <QdModal
    :model-value="visible"
    :title="t('sched_picker_title')"
    width="460px"
    @update:model-value="close"
  >
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
        <li v-key-click v-for="s in scenes" :key="s.id" class="qd-row" @click="pickScene(s)">
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
        <li v-key-click v-for="c in collections" :key="c.id" class="qd-row" @click="pickCollection(c)">
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
        <li v-key-click v-for="it in items" :key="it.id" class="qd-row" @click="chooseItem(it)">
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

    <template #footer>
      <div class="modal-actions">
        <button class="qd-btn" @click="back" :disabled="level === 'scene'">{{ t('back') }}</button>
        <button class="qd-btn qd-btn-primary" @click="close">{{ t('done') }}</button>
      </div>
    </template>
  </QdModal>
</template>

<style scoped>
.crumb { display: flex; align-items: center; gap: var(--space-2); margin: 0 0 var(--space-5); flex-wrap: wrap; }
.crumb-item {
  border: none; background: none; padding: 3px var(--space-3); border-radius: var(--radius-sm);
  font-size: var(--text-sm); color: var(--color-text-secondary); cursor: pointer; font-family: inherit;
}
.crumb-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.crumb-item.on { color: var(--color-accent); background: var(--color-accent-bg); }
.crumb-sep { color: var(--color-text-muted); flex-shrink: 0; }

.picker-body {
  flex: 1; overflow-y: auto; min-height: 140px; max-height: 52vh;
  border: 1px solid var(--color-border); border-radius: var(--radius-md); margin-top: var(--space-1);
}
.picker-list { list-style: none; margin: 0; padding: var(--space-1); display: flex; flex-direction: column; gap: var(--space-1); }
/* .qd-row 及其子元素（.qd-row-icon/.qd-row-main/.qd-row-title/.qd-row-sub/.qd-row-actions）
   是 panels.css 的公共类，这里只补 picker 特有的交互态 */
.picker-list .qd-row { cursor: pointer; }
.item-icon-img { width: 20px; height: 20px; object-fit: contain; border-radius: var(--radius-xs); }
.picker-empty, .picker-err, .picker-loading {
  padding: var(--space-7) var(--space-2); text-align: center;
  font-size: var(--text-sm); color: var(--color-text-muted);
}
.picker-err { color: var(--color-danger-ink); }
.picker-loading .spin {
  animation: picker-spin 1s linear infinite; vertical-align: -3px; margin-right: var(--space-3);
}
@keyframes picker-spin { to { transform: rotate(360deg); } }

/* 与 QdModal 的 footer 分工：「上一步」靠左、「完成」靠右，所以撑满整行再两端分开 */
.modal-actions { flex: 1; display: flex; justify-content: space-between; gap: var(--space-3); }
</style>
