<script setup lang="ts">
import { ref, reactive, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Folder, Plus, FolderPlus, ChevronDown, ChevronRight, Settings as SettingsIcon } from '@lucide/vue'
import type { ApiRequest, HttpProject, HttpEnvironment, HttpFolder, HttpDoc, HttpDragItem, HttpDropTarget } from '../types'
import HttpFolderTreeNode from './HttpFolderTreeNode.vue'

const props = defineProps<{
  projects: HttpProject[]
  folders: HttpFolder[]
  requests: ApiRequest[]
  docs: HttpDoc[]
  environments: HttpEnvironment[]
  expandedProjects: Set<string>
  expandedFolders: Set<string>
  currentId: string | null
  currentDocId: string
  draggingItem: HttpDragItem | null
  dropTarget: HttpDropTarget | null
  activeEnvs: Record<string, string>
  uncatExpanded: boolean
}>()

const emit = defineEmits<{
  (e: 'toggle-project', id: string): void
  (e: 'toggle-folder', id: string): void
  (e: 'toggle-uncategorized'): void
  (e: 'select-request', req: ApiRequest): void
  (e: 'select-doc', doc: HttpDoc): void
  (e: 'new-project'): void
  (e: 'new-request', projectId: string, folderId?: string): void
  (e: 'new-folder', projectId: string, parentId?: string): void
  (e: 'new-doc', projectId: string): void
  (e: 'rename-folder', folder: HttpFolder): void
  (e: 'delete-folder', folder: HttpFolder): void
  (e: 'delete-request', req: ApiRequest): void
  (e: 'delete-doc', doc: HttpDoc): void
  (e: 'set-active-env', projectId: string, envId: string): void
  (e: 'drag-start', item: HttpDragItem): void
  (e: 'drag-end'): void
  (e: 'drag-over', target: HttpDropTarget): void
  (e: 'drop', target: HttpDropTarget): void
  (e: 'open-proj-menu', event: MouseEvent, projectId: string): void
}>()

const { t } = useI18n()

// 目录树访问器
function topFolders(pid: string) {
  return props.folders.filter(f => (f.projectId || '') === pid && !(f.parentId || ''))
}
function topRequests(pid: string) {
  return props.requests.filter(r => (r.projectId || '') === pid && !(r.folderId || ''))
}
const uncategorized = computed(() => props.requests.filter(r => !r.projectId))
const envsByProject = computed(() => (pid: string) => props.environments.filter(e => (e.projectId || '') === pid))
function projectName(pid: string) { return props.projects.find(p => p.id === pid)?.name || '' }

function setActiveEnv(pid: string, eid: string) {
  emit('set-active-env', pid, eid)
}
</script>

<template>
  <div class="req-list">
    <div class="req-list-head">
      <span>{{ t('httpClient') }}</span>
      <button class="icon-btn" :title="t('httpNewProject')" @click="$emit('new-project')">
        <FolderPlus :size="15" />
      </button>
    </div>
    <div class="req-items">
      <!-- 项目 -->
      <div v-for="p in projects" :key="p.id" class="proj-node">
        <div
          class="proj-head"
          :class="{ 'drop-root': dropTarget?.kind === 'project-root' && dropTarget.projectId === p.id }"
          @click="$emit('toggle-project', p.id)"
          @contextmenu.prevent="$emit('open-proj-menu', $event, p.id)"
          @dragover.prevent="$emit('drag-over', { kind: 'project-root', projectId: p.id })"
          @drop.prevent="$emit('drop', { kind: 'project-root', projectId: p.id })"
        >
          <component :is="expandedProjects.has(p.id) ? ChevronDown : ChevronRight" :size="13" class="proj-caret" />
          <Folder :size="13" class="proj-icon" />
          <span class="proj-name" :title="p.name">{{ p.name }}</span>
        </div>
        <div v-if="expandedProjects.has(p.id)" class="proj-children">
          <!-- 请求列表 -->
          <div
            v-for="r in topRequests(p.id)"
            :key="r.id"
            :class="['req-item', { active: r.id === currentId, 'drop-before': dropTarget?.kind === 'before-request' && dropTarget.id === r.id, dragging: draggingItem?.kind === 'request' && draggingItem?.id === r.id }]"
            draggable="true"
            @click="$emit('select-request', r)"
            @dragstart="$emit('drag-start', { kind: 'request', id: r.id })"
            @dragend="$emit('drag-end')"
            @dragover.prevent="$emit('drag-over', { kind: 'before-request', id: r.id })"
            @drop.prevent="$emit('drop', { kind: 'before-request', id: r.id })"
          >
            <span :class="['req-method', 'method-' + (r.method || 'GET').toLowerCase()]">{{ r.method || 'GET' }}</span>
            <span class="req-name" :title="r.name || r.url">{{ r.name || r.url }}</span>
          </div>
          <!-- 目录树 -->
          <HttpFolderTreeNode
            v-for="f in topFolders(p.id)"
            :key="f.id"
            :folder="f"
            :folders="folders"
            :requests="requests"
            :docs="docs"
            :expanded="expandedFolders"
            :current-id="currentId || ''"
            :current-doc-id="currentDocId"
            :on-toggle="(id: string) => $emit('toggle-folder', id)"
            :dragging-item="draggingItem"
            :drop-target="dropTarget"
            :on-drag-start="(item: HttpDragItem) => $emit('drag-start', item)"
            :on-drag-end="() => $emit('drag-end')"
            :on-drag-over="(target: HttpDropTarget) => $emit('drag-over', target)"
            :on-drop="(target: HttpDropTarget) => $emit('drop', target)"
            @add-request="id => $emit('new-request', p.id, id)"
            @add-folder="id => $emit('new-folder', p.id, id)"
            @add-doc="(id: string) => $emit('new-doc', p.id)"
            @rename-folder="$emit('rename-folder', $event)"
            @delete-folder="$emit('delete-folder', $event)"
            @select-request="r => $emit('select-request', r)"
            @delete-request="$emit('delete-request', $event)"
            @select-doc="d => $emit('select-doc', d)"
            @delete-doc="$emit('delete-doc', $event)"
          />
          <!-- 环境选择 -->
          <div class="env-row">
            <span class="env-label">{{ t('httpEnv') }}</span>
            <select class="env-select" :value="activeEnvs[p.id] || ''" @change="setActiveEnv(p.id, ($event.target as HTMLSelectElement).value)">
              <option value="">{{ t('httpNoEnv') }}</option>
              <option v-for="e in envsByProject(p.id)" :key="e.id" :value="e.id">{{ e.name }}</option>
            </select>
          </div>
        </div>
      </div>

      <!-- 未分类 -->
      <div class="proj-node">
        <div
          class="proj-head"
          :class="{ 'drop-root': dropTarget?.kind === 'project-root' && dropTarget.projectId === '' }"
          @click="$emit('toggle-uncategorized')"
          @contextmenu.prevent="$emit('open-proj-menu', $event, '')"
          @dragover.prevent="$emit('drag-over', { kind: 'project-root', projectId: '' })"
          @drop.prevent="$emit('drop', { kind: 'project-root', projectId: '' })"
        >
          <component :is="uncategorized ? ChevronDown : ChevronRight" :size="13" class="proj-caret" />
          <span class="proj-name uncat">{{ t('httpUncategorized') }}</span>
        </div>
        <div v-if="uncategorized" class="proj-children">
          <div
            v-for="r in uncategorized"
            :key="r.id"
            :class="['req-item', { active: r.id === currentId, 'drop-before': dropTarget?.kind === 'before-request' && dropTarget.id === r.id, dragging: draggingItem?.kind === 'request' && draggingItem?.id === r.id }]"
            draggable="true"
            @click="$emit('select-request', r)"
            @dragstart="$emit('drag-start', { kind: 'request', id: r.id })"
            @dragend="$emit('drag-end')"
            @dragover.prevent="$emit('drag-over', { kind: 'before-request', id: r.id })"
            @drop.prevent="$emit('drop', { kind: 'before-request', id: r.id })"
          >
            <span :class="['req-method', 'method-' + (r.method || 'GET').toLowerCase()]">{{ r.method || 'GET' }}</span>
            <span class="req-name" :title="r.name || r.url">{{ r.name || r.url }}</span>
          </div>
          <div v-if="!uncategorized.length" class="proj-empty">{{ t('httpNoRequest') }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.req-list { width: 250px; flex-shrink: 0; display: flex; flex-direction: column; background: var(--color-bg-secondary); border-right: 1px solid var(--color-border); overflow: hidden; }
.req-list-head {
  display: flex; align-items: center; justify-content: space-between; padding: 10px 12px;
  font-size: 13px; font-weight: 600; color: var(--color-text-muted); text-transform: uppercase;
  letter-spacing: 0.5px; border-bottom: 1px solid var(--color-border);
}
.icon-btn {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.icon-btn:hover { color: var(--color-accent); background: var(--color-bg-hover); }
.req-items { flex: 1; overflow-y: auto; padding: 6px; }

/* 项目树 */
.proj-node { margin-bottom: 4px; }
.proj-head {
  display: flex; align-items: center; gap: 5px; padding: 5px 6px; border-radius: 6px;
  cursor: pointer; color: var(--color-text-secondary); font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.proj-head:hover { background: var(--color-bg-hover); }
.proj-caret { color: var(--color-text-disabled); flex-shrink: 0; }
.proj-icon { color: var(--color-accent); flex-shrink: 0; }
.proj-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.proj-name.uncat { font-weight: 500; color: var(--color-text-muted); }
.proj-head.drop-root { background: var(--color-accent-bg); box-shadow: inset 0 0 0 1px var(--color-accent); }
.proj-children { padding: 2px 0 4px 14px; }
.proj-empty { padding: 8px; text-align: center; color: var(--color-text-disabled); font-size: 12px; }

/* 环境选择行 */
.env-row { display: flex; align-items: center; gap: 5px; padding: 4px 6px 2px; }
.env-label { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; }
.env-select {
  flex: 1; min-width: 0; padding: 4px 6px; border-radius: 5px; font-size: 12px; font-family: inherit;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-primary); outline: none;
}
.env-select:focus { border-color: var(--color-border-focus); }

/* 请求项 */
.req-item {
  display: flex; align-items: center; gap: 6px;
  padding: 6px 8px; border-radius: 6px; cursor: pointer;
  color: var(--color-text-secondary); font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.req-item:hover { background: var(--color-bg-hover); }
.req-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.req-method { font-size: 10px; font-weight: 700; flex-shrink: 0; padding: 1px 4px; border-radius: 3px; color: #fff; }
.req-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 方法配色 */
.method-get { background: #2e9e5b; }
.method-post { background: #3a8ae0; }
.method-put { background: #d9920a; }
.method-delete { background: #e8584c; }
.method-patch { background: #9b6ddb; }
.method-head { background: #6b7785; }
.method-options { background: #5a8f9e; }
</style>
