<script setup lang="ts">
import { logErr } from '../utils/logger'
import { ref, watch, computed, toRef, inject } from 'vue';
import { useI18n } from 'vue-i18n';
import { useWorkspaceStore } from '../stores/workspace'
import { useFocusTrap } from '../utils/focusTrap'
import { unwrap } from '../utils/api'
import { EnvList, EnvSetActive, EnvProjectVersions } from '../../bindings/quickdock/services/env/environmentservice'
// 「应用」类型的目标值复用应用选择器：手输自定义路径 / 浏览文件 / 系统扫描的已装应用
import AppTargetPicker from './AppTargetPicker.vue'
import type { CollectionItem, OpenTool, EnvRuntimeInfo, ToastAPI } from '../types';

const { t, tm } = useI18n()

const props = defineProps<{
  item?: CollectionItem | null;
  visible: boolean;
}>();

const store = useWorkspaceStore()
const toast = inject<ToastAPI>('toast')!

const emit = defineEmits<{
  (e: 'save', item: Partial<CollectionItem>): void;
  (e: 'cancel'): void;
}>();

const name = ref('');
const type = ref('目录');
const value = ref('');
const remark = ref('');
const toolId = ref('');
const workingDirectory = ref('');
// args / color / icon 三列在后端早就有（UpdateItem 白名单已放行，items 表也有），
// 但编辑器从来不提交它们 —— 命令没法带参数、条目没法标记颜色/自定义图标。
const args = ref('');
const color = ref('');
const icon = ref('');
// 跨集合移动：collection_id 本来就在更新白名单里，前端一直没有入口
const collectionId = ref('');
const nameError = ref(false);
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

// 可移动的目标集合 = 当前场景下的集合（跨场景移动会改变归属场景，语义上不属于"整理"，
// 留给拖拽；这里只做同场景内的重新归类）
const collectionOptions = computed(() => store.filteredCollections)

const iconInput = ref<HTMLInputElement | null>(null)

// 自定义图标存 data URL：items.icon 本来就是这个格式（应用/网页图标由后端提取成
// data URL 写入），复用同一列就不用再加文件管理。超过 ~256KB 的图会明显拖慢列表渲染，
// 所以挡住。
const ICON_MAX_BYTES = 256 * 1024

function pickIcon() {
  iconInput.value?.click()
}

function onIconChange(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // 允许重复选择同一个文件
  if (!file) return
  if (!file.type.startsWith('image/')) {
    toast.error(t('itemIconNotImage'))
    return
  }
  if (file.size > ICON_MAX_BYTES) {
    toast.error(t('itemIconTooLarge'))
    return
  }
  const reader = new FileReader()
  reader.onload = () => { icon.value = String(reader.result || '') }
  reader.onerror = () => toast.error(t('itemIconReadFailed'))
  reader.readAsDataURL(file)
}

const availableTools = computed<OpenTool[]>(() => store.getToolsForType(type.value))

const toolOptions = computed(() => {
  const list = availableTools.value
  if (list.length === 0) {
    return [{ id: '', name: t('systemDefault') }]
  }
  return [{ id: '', name: t('systemDefault') }, ...list.map(t => ({ id: t.id, name: `${t.name}（${t.type}）` }))]
})

const itemTypeOptions = computed(() => {
  const types = ['目录', '网页', '命令', '文件', '应用', '快速链接']
  const labels = tm('itemTypes') as Record<string, string>
  return types.map(v => ({ label: labels[v] || v, value: v }))
})

// ---- 项目级版本切换 ----
// 探测到的一条版本要求 + 它在本机已装版本里的落点。
interface ProjectVersionRow {
  runtime: string
  require: string
  source: string
  matched: string  // 匹配到的已装版本；'' = 该要求无对应已装版本
  active: string
  installed: string[]
}

const projectRows = ref<ProjectVersionRow[]>([])
const runtimes = ref<EnvRuntimeInfo[]>([])
// binding 是「本项目绑定」的运行时版本，最终序列化进 item.env。
// 与「全局切换」（写系统 PATH）是两回事：绑定只影响打开这个条目时的子进程。
const binding = ref<Record<string, string>>({})
const loadingProject = ref(false)
const addRuntime = ref('')
const addVersion = ref('')

// 项目目录：目录型条目取 value，其余取工作目录——两者都没有就没什么可探测的。
const projectDir = computed(() => (type.value === '目录' ? value.value : workingDirectory.value || '').trim())

const runtimesWithInstalls = computed(() => runtimes.value.filter(r => r.installed.length > 0))

const addVersionOptions = computed(() => {
  const rt = runtimes.value.find(r => r.id === addRuntime.value)
  return rt ? rt.installed.map(i => i.version) : []
})

const runtimeLabel = (id: string) => runtimes.value.find(r => r.id === id)?.name || id
const installedOf = (id: string) => runtimes.value.find(r => r.id === id)?.installed.map(i => i.version) ?? []
const activeOf = (id: string) => runtimes.value.find(r => r.id === id)?.installed.find(i => i.active)?.version || ''

// displayRows = 探测到的要求 + 用户手动绑定但没被探测到的运行时，让两类都能在同一处解绑。
const displayRows = computed<ProjectVersionRow[]>(() => {
  const detected = new Set(projectRows.value.map(r => r.runtime))
  const extra: ProjectVersionRow[] = Object.entries(binding.value)
    .filter(([rt]) => !detected.has(rt))
    .map(([rt, v]) => ({ runtime: rt, require: '', source: '', matched: v, active: activeOf(rt), installed: installedOf(rt) }))
  return [...projectRows.value, ...extra]
})

// 某一行「建议采用」的版本：已绑定优先，其次探测匹配到的，再次当前激活，最后任一已装版本。
function suggestVersion(row: ProjectVersionRow): string {
  return binding.value[row.runtime] || row.matched || row.active || row.installed[0] || ''
}

function parseBinding(raw?: string): Record<string, string> {
  if (!raw) return {}
  try {
    const arr = JSON.parse(raw) as { runtime: string; version: string }[]
    const out: Record<string, string> = {}
    for (const e of arr) if (e && e.runtime) out[e.runtime] = e.version || ''
    return out
  } catch {
    return {} // 历史脏数据不应该让编辑器打不开
  }
}

async function loadProjectEnv() {
  binding.value = parseBinding(props.item?.env)
  projectRows.value = []
  loadingProject.value = false
  if (!projectDir.value) return
  loadingProject.value = true
  try {
    const [list, rows] = await Promise.all([
      EnvList().then(r => unwrap<EnvRuntimeInfo[]>(r) ?? []),
      EnvProjectVersions(projectDir.value).then(r => unwrap<ProjectVersionRow[]>(r) ?? []),
    ])
    runtimes.value = list
    projectRows.value = rows
  } catch (e) {
    logErr('ItemEditor.projectEnv', e)
  } finally {
    loadingProject.value = false
  }
}

// 全局切换：写系统 PATH（HKCU），影响所有程序。与「仅本项目」不同，需用户显式点击。
async function switchGlobal(row: ProjectVersionRow) {
  const target = suggestVersion(row)
  if (!target || target === row.active) return
  try {
    const res = await EnvSetActive(row.runtime, target)
    if (res && res.code !== 0) {
      toast.error(res.msg || t('projectEnvSwitchFailed'))
      return
    }
    row.active = target
  } catch (e) {
    logErr('ItemEditor.switchGlobal', e)
    toast.error(t('projectEnvSwitchFailed'))
  }
}

// 仅本项目：切换绑定。开启用建议版本，关闭则解绑。
function toggleBind(row: ProjectVersionRow) {
  const next = { ...binding.value }
  if (next[row.runtime]) {
    delete next[row.runtime]
  } else {
    const v = suggestVersion(row)
    if (!v) return
    next[row.runtime] = v
  }
  binding.value = next
}

function setBindVersion(row: ProjectVersionRow, version: string) {
  binding.value = { ...binding.value, [row.runtime]: version }
}

function addBinding() {
  if (!addRuntime.value || !addVersion.value) return
  binding.value = { ...binding.value, [addRuntime.value]: addVersion.value }
  addRuntime.value = ''
  addVersion.value = ''
}

// envJSON 把绑定序列化成 items.env 的存储格式（与 scenes.env 同形）；无绑定存空串。
function envJSON(): string {
  const entries = Object.entries(binding.value)
    .filter(([runtime, version]) => runtime && version)
    .map(([runtime, version]) => ({ runtime, version }))
  return entries.length ? JSON.stringify(entries) : ''
}

watch(() => [props.visible, props.item], ([v]) => {
  if (v) {
    name.value = props.item?.name ?? '';
    type.value = props.item?.type ?? '目录';
    value.value = props.item?.value ?? '';
    remark.value = props.item?.remark ?? '';
    toolId.value = props.item?.toolId ?? '';
    workingDirectory.value = props.item?.workingDirectory ?? '';
    args.value = props.item?.args ?? '';
    color.value = props.item?.color ?? '';
    icon.value = props.item?.icon ?? '';
    // 新建时目标集合就是当前集合；编辑时是它原本所属的集合
    collectionId.value = props.item?.collectionId ?? store.activeCollectionId;
    nameError.value = false;
    if (!props.item && !toolId.value) {
      const suggested = store.getDefaultToolForType(type.value)
      if (suggested) toolId.value = suggested.id
    }
    loadProjectEnv()
  }
});

watch(type, (newType) => {
  if (newType === '快速链接' && !value.value.includes('{query}')) {
    // 自动补成「协议 + 域名 + {query}」的示例，但不能无脑拼前缀：
    // 用户填的常常已经是完整地址（https://a.com/search?q=），再拼一次就变成
    // https://https://a.com/search?q={query}，点了直接打不开。
    const v = value.value.trim()
    if (!v) {
      value.value = 'https://{query}'
    } else if (/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) {
      value.value = v + '{query}' // 已带协议：只补占位符
    } else {
      value.value = 'https://' + v + '{query}' // 裸域名：补协议
    }
  }
  // 切换类型后，校验当前 toolId 是否仍属于新类型可选工具；不属于则回落默认
  const valid = store.getToolsForType(newType).some(t => t.id === toolId.value)
  if (!valid) {
    const suggested = store.getDefaultToolForType(newType)
    toolId.value = suggested ? suggested.id : ''
  }
})

function handleSave() {
  const trimmed = name.value.trim()
  if (!trimmed) {
    nameError.value = true
    return
  }
  nameError.value = false
  const payload: Partial<CollectionItem> & Record<string, any> = {
    name: trimmed,
    type: type.value,
    value: value.value,
    remark: remark.value,
    toolId: toolId.value,
    workingDirectory: workingDirectory.value,
    env: envJSON(),
    args: args.value.trim(),
    color: color.value,
    icon: icon.value,
  }
  // 只有编辑态才可能真的「移动」：新建时集合由 activeCollectionId 决定
  if (props.item && collectionId.value && collectionId.value !== props.item.collectionId) {
    payload.collectionId = collectionId.value
  }
  emit('save', payload);
}

function handleCancel() {
  emit('cancel');
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') { handleCancel(); return }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="visible" class="editor-overlay" @mousedown.self="handleCancel" @keydown="onKeydown">
        <div ref="panelRef" class="editor-panel" @mousedown.stop>
          <div class="editor-header">
            <span class="editor-title">{{ item ? t('itemEditorTitle') : t('itemEditorNewTitle') }}</span>
            <button class="editor-close" @click="handleCancel">
              <span class="close-icon">&times;</span>
            </button>
          </div>
          <div class="editor-body">
            <label class="field">
              <span class="field-label">{{ t('itemName') }}</span>
              <input v-model="name" type="text" :placeholder="t('itemName')" class="field-input" :class="{ 'input-error': nameError }" @keydown.enter="handleSave" />
              <p v-if="nameError" class="field-error">{{ t('emptyName') }}</p>
            </label>
            <label class="field">
              <span class="field-label">{{ t('itemType') }}</span>
              <select v-model="type" class="field-input">
                <option v-for="opt in itemTypeOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
              </select>
            </label>
            <label class="field">
              <span class="field-label">{{ t('itemValue') }}</span>
              <AppTargetPicker v-if="type === '应用'" v-model="value" :placeholder="t('itemValue')" />
              <input v-else v-model="value" type="text" :placeholder="type === '网页' ? 'https://...' : type === '快速链接' ? t('quicklinkUrlPlaceholder') : t('itemValue')" class="field-input" />
              <p class="field-hint">{{ type === '快速链接' ? t('quicklinkDesc') : t('itemValueHint') }}</p>
            </label>
            <label class="field">
              <span class="field-label">{{ t('workingDir') }}</span>
              <input v-model="workingDirectory" type="text" :placeholder="t('workingDir')" class="field-input" />
            </label>
            <label class="field">
              <span class="field-label">{{ t('openTool') }}</span>
              <select v-model="toolId" class="field-input">
                <option v-for="opt in toolOptions" :key="opt.id" :value="opt.id">{{ opt.name }}</option>
              </select>
            </label>
            <label class="field">
              <span class="field-label">{{ t('itemArgs') }}</span>
              <input v-model="args" type="text" class="field-input" :placeholder="t('itemArgsPlaceholder')" />
              <p class="field-hint">{{ t('itemArgsHint') }}</p>
            </label>
            <label v-if="item && collectionOptions.length > 1" class="field">
              <span class="field-label">{{ t('itemCollection') }}</span>
              <select v-model="collectionId" class="field-input">
                <option v-for="c in collectionOptions" :key="c.id" :value="c.id">{{ c.name }}</option>
              </select>
              <p class="field-hint">{{ t('itemMoveHint') }}</p>
            </label>
            <div class="field">
              <span class="field-label">{{ t('itemAppearance') }}</span>
              <div class="appearance-row">
                <input v-model="color" type="color" class="color-input" :title="t('itemColor')" />
                <button v-if="color" type="button" class="link-btn" @click="color = ''">{{ t('clear') }}</button>
                <span class="appearance-gap"></span>
                <img v-if="icon" :src="icon" class="icon-preview" alt="" />
                <button type="button" class="link-btn" @click="pickIcon">
                  {{ icon ? t('itemIconChange') : t('itemIconPick') }}
                </button>
                <button v-if="icon" type="button" class="link-btn" @click="icon = ''">{{ t('clear') }}</button>
                <input ref="iconInput" type="file" accept="image/*" class="icon-file" @change="onIconChange" />
              </div>
            </div>
            <div v-if="projectDir" class="field project-env">
              <div class="project-env-head">
                <span class="field-label">{{ t('projectEnvTitle') }}</span>
                <button type="button" class="link-btn" :disabled="loadingProject" @click="loadProjectEnv">
                  {{ loadingProject ? t('projectEnvScanning') : t('projectEnvRescan') }}
                </button>
              </div>
              <p class="field-hint">{{ t('projectEnvHint') }}</p>
              <p v-if="projectRows.length === 0 && Object.keys(binding).length === 0" class="field-hint">
                {{ t('projectEnvNone') }}
              </p>
              <div v-for="row in displayRows" :key="row.runtime" class="env-row">
                <div class="env-row-main">
                  <span class="env-rt">{{ runtimeLabel(row.runtime) }}</span>
                  <span v-if="row.require" class="env-req">{{ t('projectEnvRequire', { v: row.require, src: row.source }) }}</span>
                  <span v-if="row.matched" class="env-tag ok">{{ t('projectEnvMatched', { v: row.matched }) }}</span>
                  <span v-else-if="row.require" class="env-tag bad">{{ t('projectEnvNoMatch') }}</span>
                  <span class="env-active">
                    {{ row.active ? t('projectEnvActive', { v: row.active }) : t('projectEnvUnset') }}
                  </span>
                </div>
                <div class="env-row-actions">
                  <button
                    type="button"
                    class="link-btn"
                    :disabled="!suggestVersion(row) || suggestVersion(row) === row.active"
                    @click="switchGlobal(row)"
                  >{{ t('projectEnvSwitchGlobal') }}</button>
                  <button
                    type="button"
                    class="link-btn"
                    :class="{ active: !!binding[row.runtime] }"
                    :disabled="!suggestVersion(row)"
                    @click="toggleBind(row)"
                  >{{ binding[row.runtime] ? t('projectEnvBound') : t('projectEnvBindHere') }}</button>
                  <select
                    v-if="binding[row.runtime]"
                    class="env-select"
                    :value="binding[row.runtime]"
                    @change="setBindVersion(row, ($event.target as HTMLSelectElement).value)"
                  >
                    <option v-for="v in (row.installed.length ? row.installed : [binding[row.runtime]])" :key="v" :value="v">{{ v }}</option>
                  </select>
                </div>
              </div>
              <div v-if="runtimesWithInstalls.length" class="env-add">
                <select v-model="addRuntime" class="field-input env-add-select">
                  <option value="">{{ t('projectEnvAdd') }}</option>
                  <option v-for="rt in runtimesWithInstalls" :key="rt.id" :value="rt.id">{{ rt.name }}</option>
                </select>
                <select v-if="addRuntime" v-model="addVersion" class="field-input env-add-select">
                  <option value="">{{ t('projectEnvPickVersion') }}</option>
                  <option v-for="v in addVersionOptions" :key="v" :value="v">{{ v }}</option>
                </select>
                <button v-if="addRuntime && addVersion" type="button" class="link-btn" @click="addBinding">{{ t('projectEnvAddConfirm') }}</button>
              </div>
            </div>
            <label class="field">
              <span class="field-label">{{ t('remark') }}</span>
              <textarea v-model="remark" class="field-textarea" :placeholder="t('remarkPlaceholder')"></textarea>
            </label>
          </div>
          <div class="editor-footer">
            <button class="btn btn-cancel" @click="handleCancel">{{ t('cancel') }}</button>
            <button class="btn btn-primary" @click="handleSave">{{ item ? t('save') : t('create') }}</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.editor-overlay {
  position: fixed; inset: 0; z-index: 9000;
  background: var(--color-bg-overlay);
  display: flex; align-items: center; justify-content: center;
}
.editor-panel {
  width: 480px; max-height: 80vh;
  background: var(--color-surface); border: 1px solid var(--color-border); border-radius: var(--radius-lg);
  display: flex; flex-direction: column; overflow: hidden;
  box-shadow: 0 12px 40px var(--color-bg-overlay);
}
.editor-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 16px 20px; border-bottom: 1px solid var(--color-border);
}
.editor-title { font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.editor-close {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  width: 28px; height: 28px; border-radius: var(--radius-sm);
  display: flex; align-items: center; justify-content: center; font-size: 18px;
}
.editor-close:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.editor-body {
  flex: 1; overflow-y: auto; padding: 20px;
  display: flex; flex-direction: column; gap: 16px;
}
.field { display: flex; flex-direction: column; gap: 6px; }
.field-label { font-size: 12px; font-weight: 600; color: var(--color-text-muted); }
.field-input {
  padding: 8px 12px; border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; outline: none; transition: border-color 0.12s;
}
.field-input:focus { border-color: var(--color-accent); }
.field-input.input-error { border-color: var(--color-danger); }
.field-input::placeholder { color: var(--color-text-disabled); }
.field-textarea {
  resize: vertical; min-height: 60px;
  padding: 8px 12px; border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; outline: none;
}
.field-textarea:focus { border-color: var(--color-accent); }
.field-error { font-size: 11px; color: var(--color-danger); margin-top: 2px; }
.field-hint { font-size: 11px; color: var(--color-text-disabled); margin-top: 2px; }

/* 外观行：颜色 + 自定义图标（都是可选，默认不占视觉重量） */
.appearance-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.color-input {
  width: 32px; height: 24px; padding: 0; cursor: pointer;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary);
}
.appearance-gap { flex: 1; }
.icon-preview {
  width: 20px; height: 20px; object-fit: contain;
  border-radius: var(--radius-xs);
}
/* 文件选择的原生按钮不显示，靠上面的「选择图标」按钮触发 */
.icon-file { display: none; }

/* 项目环境：探测到的版本要求 + 全局切换/仅本项目绑定 */
.project-env {
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  padding: 10px 12px; background: var(--color-bg-tertiary); gap: 8px;
}
.project-env-head { display: flex; align-items: center; justify-content: space-between; }
.env-row { display: flex; flex-direction: column; gap: 4px; padding: 6px 0; border-top: 1px solid var(--color-border); }
.env-row-main { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; font-size: 12px; }
.env-rt { font-weight: 600; color: var(--color-text-primary); }
.env-req { color: var(--color-text-muted); }
.env-tag { padding: 1px 6px; border-radius: var(--radius-xs); font-size: 11px; }
.env-tag.ok { background: var(--color-bg-hover); color: var(--color-text-primary); }
.env-tag.bad { background: var(--color-danger); color: var(--color-accent-text); }
.env-active { color: var(--color-text-disabled); font-size: 11px; }
.env-row-actions { display: flex; align-items: center; gap: 8px; }
.env-add { display: flex; align-items: center; gap: 6px; padding-top: 6px; border-top: 1px solid var(--color-border); }
.env-add-select { flex: 1; padding: 4px 8px; font-size: 12px; }
.env-select { padding: 3px 6px; font-size: 11px; border: 1px solid var(--color-border); border-radius: var(--radius-xs); background: var(--color-bg-tertiary); color: var(--color-text-primary); }
.link-btn {
  background: none; border: none; padding: 0; font-size: 12px; font-family: inherit;
  color: var(--color-accent); cursor: pointer;
}
.link-btn:hover:not(:disabled) { text-decoration: underline; }
.link-btn:disabled { color: var(--color-text-disabled); cursor: default; }
.link-btn.active { color: var(--color-text-primary); font-weight: 600; }
.editor-footer {
  display: flex; justify-content: flex-end; gap: 8px;
  padding: 14px 20px; border-top: 1px solid var(--color-border);
}
.btn {
  padding: 8px 18px; border: none; border-radius: var(--radius-sm); font-size: 13px;
  cursor: pointer; font-family: inherit; transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.btn-cancel { background: var(--color-bg-active); color: var(--color-text-muted); }
.btn-cancel:hover { background: var(--color-bg-active); color: var(--color-text-primary); }

.dialog-enter-active, .dialog-leave-active { transition: opacity 0.15s; }
.dialog-enter-from, .dialog-leave-to { opacity: 0; }
</style>
