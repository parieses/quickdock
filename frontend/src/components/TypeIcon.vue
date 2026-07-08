<script setup lang="ts">
import { computed } from 'vue'
import {
  Layers, Folder, FolderOpen, Globe, Terminal,
  File, Monitor, Briefcase, Building2, Wrench,
  Palette, Star,
} from '@lucide/vue'

const props = defineProps<{
  type: string
  size?: number
}>()

const iconSize = computed(() => props.size ?? 18)

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
  '目录': Folder,
  '网页': Globe,
  '命令': Terminal,
  '文件': File,
  '应用': Monitor,
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
