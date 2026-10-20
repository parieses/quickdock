<script lang="ts" setup>
/**
 * 会话标签栏（#13）。
 *
 * 每个标签 = 一个 hostId + 一个 sessionId。注意标签不是「主机」：
 * 同一台主机理论上可以开多个会话（不同目录/不同 sudo 上下文），
 * 但当前 UI 刻意**一个主机只留一个标签**——同一台机器开三个看不出区别的终端，
 * 用户自己都分不清谁是谁。要开多个请用 tmux/screen，那是服务器侧该管的事。
 *
 * 已打开的主机再点一次是「聚焦已有标签」而不是重新连接：
 * 后端连接池会复用同一条 TCP，但 manager.Start 仍会新建一个 channel，
 * 旧的那个 session 就成了没人管的孤儿（占着引用计数直到进程结束）。
 * 前端先查已有标签，从根上避免制造孤儿。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { HardDrive, Plus, X } from '@lucide/vue'
import { sshEnvClass, type SshHost } from '../utils/ssh'
import type { SshTab } from '../utils/sshTabs'

const { t } = useI18n()

const props = defineProps<{
  tabs: SshTab[]
  activeId: string
  /** 当前标签的文件面板是否已打开（用于按钮的激活态） */
  filesOpen: boolean
}>()

const emit = defineEmits<{
  (e: 'select', tabId: string): void
  (e: 'close', tabId: string): void
  (e: 'new'): void
  /** 切换当前标签的文件面板 */
  (e: 'files'): void
}>()

const hostOf = computed(() => {
  const map = new Map<string, SshHost>()
  for (const tab of props.tabs) map.set(tab.id, tab.host)
  return map
})

function shortLabel(tab: SshTab): string {
  return tab.host.name || tab.host.host
}

function titleOf(tab: SshTab): string {
  const h = tab.host
  return `${h.name || h.host} — ${h.user || 'root'}@${h.host}:${h.port || 22}`
}
</script>

<template>
  <div class="tabs" role="tablist">
    <div class="tabs-strip">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        class="tab"
        :class="[sshEnvClass(tab.host.env), { 'is-active': tab.id === activeId }]"
        role="tab"
        :aria-selected="tab.id === activeId"
        :title="titleOf(tab)"
        @click="emit('select', tab.id)"
      >
        <!-- 生产环境标签用实心红点 + 危险色：多标签并排时颜色是唯一能一眼分辨的线索 -->
        <i class="tab-dot" :class="sshEnvClass(tab.host.env)" />
        <span class="tab-name">{{ shortLabel(tab) }}</span>
        <span v-if="tab.busy" class="tab-spinner" />
        <span
          class="tab-x"
          :title="t('sshCloseTab')"
          @click.stop="emit('close', tab.id)"
        >
          <X :size="11" />
        </span>
      </button>

      <button class="tab tab-new" :title="t('sshNewTab')" @click="emit('new')">
        <Plus :size="13" />
      </button>

      <div class="tabs-actions">
        <button
          class="tabs-act"
          :class="{ 'is-on': filesOpen }"
          :disabled="!activeId"
          :title="t('sshFiles')"
          @click="emit('files')"
        >
          <HardDrive :size="13" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tabs {
  flex: 0 0 auto;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}

.tabs-strip {
  display: flex;
  align-items: stretch;
  gap: 1px;
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: thin;
}

.tab {
  position: relative;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  max-width: 190px;
  padding: 0 var(--space-3) 0 var(--space-4);
  height: 30px;
  font-size: 12px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-right: 1px solid var(--color-border);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}

.tab:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.tab.is-active {
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  /* 顶部一条实线标示当前标签，比背景色更明确——
     终端区本身就是深色，背景色的差别在深色主题下几乎看不出来。 */
  box-shadow: inset 0 2px 0 var(--color-accent);
}

/* 生产标签：整条 tab 用危险色描边，多标签并排时最醒目 */
.tab.env-prod.is-active {
  box-shadow: inset 0 2px 0 var(--color-danger);
}

.tab.env-prod {
  background: var(--color-danger-bg);
}

.tab.env-staging.is-active {
  box-shadow: inset 0 2px 0 var(--color-warning);
}

.tab-dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: var(--radius-full);
}

.tab-dot.env-prod {
  background: var(--color-danger);
}

.tab-dot.env-staging {
  background: var(--color-warning);
}

.tab-dot.env-test {
  background: var(--color-text-disabled);
}

.tab-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tab-spinner {
  flex: 0 0 auto;
  width: 9px;
  height: 9px;
  border: 1.5px solid var(--color-border-light);
  border-top-color: var(--color-accent);
  border-radius: var(--radius-full);
  animation: tab-spin 700ms linear infinite;
}

@keyframes tab-spin {
  to {
    transform: rotate(360deg);
  }
}

.tab-x {
  display: flex;
  flex: 0 0 auto;
  padding: 2px;
  border-radius: var(--radius-xs);
  color: var(--color-text-muted);
  opacity: 0;
  transition: opacity var(--transition-fast), background var(--transition-fast);
}

.tab:hover .tab-x,
.tab.is-active .tab-x {
  opacity: 1;
}

.tab-x:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.tab-new {
  padding: 0 var(--space-4);
  color: var(--color-text-muted);
}

.tab-new:hover {
  color: var(--color-text-primary);
}

/* 推到最右：标签条会横向滚动，按钮留在末尾比跟着标签滚走更好找 */
.tabs-actions {
  display: flex;
  align-items: center;
  margin-left: auto;
  padding: 0 var(--space-3);
  border-left: 1px solid var(--color-border);
}

.tabs-act {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-muted);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.tabs-act:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.tabs-act.is-on {
  color: var(--color-accent);
  background: var(--color-accent-bg);
}

.tabs-act:disabled {
  cursor: default;
  opacity: 0.4;
}
</style>
