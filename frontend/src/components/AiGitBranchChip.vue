<script setup lang="ts">
/** 当前目录的 git 分支 chip：落地页与聊天区输入条共用同一套盒模型。 */
import { GitBranch } from '@lucide/vue'

defineProps<{
  branch: string
  tip?: string
}>()
</script>

<template>
  <span class="ai-bit-branch" :title="tip">
    <GitBranch :size="12" />
    <span class="ai-bit-branch-name">{{ branch }}</span>
  </span>
</template>

<style scoped>
.ai-bit-branch {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 22px;
  max-width: 100%;
  padding: 0 var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
  font-size: var(--text-xs);
  font-family: var(--font-mono);
  /* 分支名可能是 feature/xxx-yyy 长串：允许收缩，超长由 name 的 ellipsis 收尾 */
  flex-shrink: 1;
  min-width: 0;
}
.ai-bit-branch-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
