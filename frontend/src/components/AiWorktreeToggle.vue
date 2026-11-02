<script setup lang="ts">
/**
 * 输入区底栏的「Worktree」开关（隔离工作区）。
 *
 * 只在当前目录确实是 git 仓库时渲染——没有版本控制就没有「基于分支开副本」
 * 这回事，把灰掉的开关摆在那里只是噪音。
 *
 * 三种形态：
 *   1. 没开 → 一个可点的开关；打开后出现「基于哪条分支」的下拉（默认当前分支）。
 *   2. 已开（会话上记着 worktree_path）→ 显示基于的分支 + 移除按钮。
 *      「已开」不给再勾选：拆工作区只能走显式移除，否则一个误点就把改动丢了。
 *   3. 生成中 → 全部禁用（这一轮已经按旧口径在跑）。
 *
 * 状态归 AIPage（与权限、技能同一层）：这个组件只管显示与把选择 emit 上去。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { FolderGit2, X } from '@lucide/vue'

const props = defineProps<{
  /** 当前目录在 git 仓库里：false 时整个组件不渲染 */
  gitRepo: boolean
  /** 会话上已有工作区 */
  active: boolean
  /** 工作区目录（active 时用于 tooltip） */
  path: string
  /** 新建时选的分支 / 已有工作时基于的分支 */
  branch: string
  /** 新建时的开关状态 */
  enabled: boolean
  /** 可选分支（打开下拉时由 AIPage 拉一次） */
  branches: string[]
  branchOpen: boolean
  /** 生成中：禁用一切交互 */
  disabled: boolean
}>()

const emit = defineEmits<{
  (e: 'update:enabled', v: boolean): void
  (e: 'toggle-branch'): void
  (e: 'pick-branch', name: string): void
  (e: 'remove'): void
}>()

const { t } = useI18n()

const label = computed(() => {
  if (props.active) return props.branch || t('aiWorktreeCurrent')
  if (props.enabled) return props.branch || t('aiWorktreeCurrent')
  return t('aiWorktree')
})

function onToggle() {
  if (props.disabled) return
  emit('update:enabled', !props.enabled)
}
</script>

<template>
  <div v-if="props.gitRepo" class="ai-wt">
    <!-- 已开：显示基于分支 + 移除；不再给勾选框，避免误点丢改动 -->
    <template v-if="props.active">
      <span class="ai-wt-chip is-active" :title="t('aiWorktreeActiveTip', { path: props.path })">
        <FolderGit2 :size="12" />
        <span class="ai-wt-name">{{ label }}</span>
      </span>
      <button
        class="ai-wt-del"
        :title="t('aiWorktreeRemove')"
        :disabled="props.disabled"
        @click="emit('remove')"
      >
        <X :size="11" />
      </button>
    </template>

    <!-- 没开：开关 + （打开后）分支下拉 -->
    <template v-else>
      <button
        class="ai-wt-chip"
        :class="{ 'is-on': props.enabled }"
        :title="t('aiWorktreeTip')"
        :disabled="props.disabled"
        @click="onToggle"
      >
        <FolderGit2 :size="12" />
        <span class="ai-wt-name">Worktree</span>
      </button>
      <button
        v-if="props.enabled"
        class="ai-wt-branch"
        :title="t('aiWorktreeBranchTip')"
        :disabled="props.disabled"
        @click="emit('toggle-branch')"
      >
        {{ label }} ▾
      </button>
      <!-- 分支下拉：向上弹（输入区贴着窗口底部） -->
      <div v-if="props.enabled && props.branchOpen" class="ai-wt-menu">
        <button class="ai-wt-item" @click="emit('pick-branch', '')">
          {{ t('aiWorktreeCurrent') }}
        </button>
        <button
          v-for="b in props.branches"
          :key="b"
          class="ai-wt-item"
          :class="{ 'is-sel': b === props.branch }"
          @click="emit('pick-branch', b)"
        >
          {{ b }}
        </button>
        <div v-if="!props.branches.length" class="ai-wt-empty">{{ t('aiWorktreeNoBranches') }}</div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.ai-wt {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  position: relative;
}

/* 开关与分支共用一套「底栏小胶囊」样式：高度对齐旁边的分支 chip */
.ai-wt-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 22px;
  padding: 0 7px;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-muted);
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast),
    background-color var(--transition-fast);
}

.ai-wt-chip:hover:not(:disabled) {
  color: var(--color-accent);
  border-color: var(--color-accent);
}

/* 打开 / 已启用：实线边框 + 强调色，和旁边那条「当前分支」区分开 */
.ai-wt-chip.is-on,
.ai-wt-chip.is-active {
  border-style: solid;
  border-color: var(--color-accent);
  color: var(--color-accent);
  background: var(--color-bg-tertiary);
}

.ai-wt-chip:disabled { opacity: 0.5; cursor: not-allowed; }

.ai-wt-name { white-space: nowrap; }

.ai-wt-branch {
  height: 22px;
  padding: 0 7px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-wt-branch:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent); }
.ai-wt-branch:disabled { opacity: 0.5; cursor: not-allowed; }

.ai-wt-del {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: none;
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}

.ai-wt-del:hover:not(:disabled) { color: var(--color-danger); background: var(--color-bg-tertiary); }
.ai-wt-del:disabled { opacity: 0.4; cursor: not-allowed; }

/* 下拉向上弹：输入区贴着窗口底部，往下弹会被切掉 */
.ai-wt-menu {
  position: absolute;
  left: 0;
  bottom: calc(100% + 6px);
  min-width: 160px;
  max-height: 240px;
  overflow: auto;
  padding: 4px;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  z-index: 40;
}

.ai-wt-item {
  display: block;
  width: 100%;
  padding: 5px 8px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px;
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ai-wt-item:hover { background: var(--color-bg-tertiary); color: var(--color-text-primary); }
.ai-wt-item.is-sel { color: var(--color-accent); }

.ai-wt-empty {
  padding: 6px 8px;
  color: var(--color-text-disabled);
  font-size: 11px;
}
</style>
