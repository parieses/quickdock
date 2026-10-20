<script lang="ts" setup>
/**
 * 会话标签栏（#13）。
 *
 * 每个标签 = 一个全局唯一 id + 一个 hostId + 一个 sessionId。标签不再等于「主机」：
 * 同一台主机现在允许开多个标签（不同目录/不同 sudo 上下文/一个跑更新一个看日志），
 * 用右键「复制会话」或双击标签即可再开一个，后端连接池会复用同一条 TCP、各开一个 channel。
 *
 * 点左树主机时仍是「聚焦该主机已有标签」而不是重新连接：避免手滑多点开一堆孤儿会话。
 * 真正要第二个独立会话，走复制入口（openTab，resume 为 null）。
 */
import { computed, ref } from 'vue'
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
  /** 复制该标签的会话到新标签（同主机第二个独立会话） */
  (e: 'duplicate', tabId: string): void
}>()

const hostOf = computed(() => {
  const map = new Map<string, SshHost>()
  for (const tab of props.tabs) map.set(tab.id, tab.host)
  return map
})

/** 每台主机开了几个标签：>1 时需要给标签加序号区分。 */
const hostCounts = computed(() => {
  const m = new Map<string, number>()
  for (const t of props.tabs) m.set(t.hostId, (m.get(t.hostId) || 0) + 1)
  return m
})

/** 同一主机下每个标签的序号（1 起），用于标签上的角标。 */
const ordinals = computed(() => {
  const seen = new Map<string, number>()
  const out = new Map<string, number>()
  for (const t of props.tabs) {
    const n = (seen.get(t.hostId) || 0) + 1
    seen.set(t.hostId, n)
    out.set(t.id, n)
  }
  return out
})

function shortLabel(tab: SshTab): string {
  return tab.host.name || tab.host.host
}

function titleOf(tab: SshTab): string {
  const h = tab.host
  const ord =
    (hostCounts.value.get(h.id) || 0) > 1 ? ` ·${ordinals.value.get(tab.id)}` : ''
  return `${h.name || h.host} — ${h.user || 'root'}@${h.host}:${h.port || 22}${ord}`
}

// ---- 右键菜单：复制会话 ----
const menu = ref<{ id: string; x: number; y: number } | null>(null)
function openMenu(tabId: string, ev: MouseEvent) {
  menu.value = { id: tabId, x: ev.clientX, y: ev.clientY }
}
function closeMenu() {
  menu.value = null
}
function duplicate(tabId: string) {
  emit('duplicate', tabId)
  closeMenu()
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
        @dblclick="emit('duplicate', tab.id)"
        @contextmenu.prevent="openMenu(tab.id, $event)"
      >
        <!-- 生产环境标签用实心红点 + 危险色：多标签并排时颜色是唯一能一眼分辨的线索 -->
        <i class="tab-dot" :class="sshEnvClass(tab.host.env)" />
        <span class="tab-name">{{ shortLabel(tab) }}</span>
        <sup v-if="(hostCounts.get(tab.hostId) || 0) > 1" class="tab-ord">{{ ordinals.get(tab.id) }}</sup>
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

    <!-- 右键菜单：复制会话到新标签 -->
    <div v-if="menu" class="tab-menu-backdrop" @click="closeMenu" @contextmenu.prevent="closeMenu">
      <div class="tab-menu" :style="{ left: menu.x + 'px', top: menu.y + 'px' }" @click.stop>
        <button class="tab-menu-item" @click="duplicate(menu.id)">
          {{ t('sshDuplicateTab') }}
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

/* 同一主机的多个标签：右上角小序号，便于区分「哪个是跑更新的、哪个是看日志的」 */
.tab-ord {
  margin-left: 2px;
  font-size: 9px;
  font-weight: 500;
  color: var(--color-text-muted);
  font-family: var(--font-sans);
}

.tab-menu-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1000;
}

.tab-menu {
  position: fixed;
  z-index: 1001;
  min-width: 168px;
  padding: var(--space-1);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-pop);
}

.tab-menu-item {
  display: block;
  width: 100%;
  padding: var(--space-2) var(--space-3);
  font-size: 12px;
  font-family: inherit;
  text-align: left;
  color: var(--color-text-primary);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.tab-menu-item:hover {
  background: var(--color-bg-hover);
}
</style>
