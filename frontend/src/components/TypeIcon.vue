<script setup lang="ts">
import { computed } from 'vue'
import {
  Layers, Folder, FolderOpen, Globe, Terminal,
  File, Monitor, Briefcase, Building2, Wrench,
  Palette, Star, Link,
} from '@lucide/vue'
import { ITEM_TYPE } from '../constants/itemTypes'

const props = defineProps<{
  type: string
  size?: number
}>()

const iconSize = computed(() => props.size ?? 18)

// 说明：sceneIconMap / collectionIconMap 的键是「场景分类」「集合分类」的中文名，
// 不是 items.type 规范值（ITEM_TYPES 中无对应项），按各自分类字面量查表、保持原样；
// 只有下面的 itemIconMap 是 items.type 规范值，收敛到 itemTypes 常量。
// 场景类型 → 图标
const sceneIconMap: Record<string, any> = {
  '项目': Briefcase,
  '办公': Building2,
  '工程': Wrench,
  '设计': Palette,
  '通用': Layers,
  '自定义': Star,
}

// 集合类型 → 图标
const collectionIconMap: Record<string, any> = {
  '目录集合': FolderOpen,
  '网页集合': Globe,
  '命令集合': Terminal,
  '文件集合': File,
  '应用集合': Monitor,
}

// 项类型 → 图标
const itemIconMap: Record<string, any> = {
  [ITEM_TYPE.DIR]: Folder,
  [ITEM_TYPE.URL]: Globe,
  [ITEM_TYPE.CMD]: Terminal,
  [ITEM_TYPE.FILE]: File,
  [ITEM_TYPE.APP]: Monitor,
  [ITEM_TYPE.LINK]: Link,
}

const iconComponent = computed<any>(() => {
  return sceneIconMap[props.type]
    || collectionIconMap[props.type]
    || itemIconMap[props.type]
    || Layers
})
</script>

<template>
  <component :is="iconComponent" :size="iconSize" />
</template>
