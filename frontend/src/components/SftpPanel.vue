<script lang="ts" setup>
/**
 * SFTP 文件面板（#14）。
 *
 * 与终端共处同一个标签：它们共享一条 SSH 连接（服务端 MaxSessions 有限，
 * 各建一条会先把自己挤下线），但互不干涉生命周期——本组件自己拉列表，
 * 关掉也只是关掉，不影响同主机已开着的终端会话。
 *
 * 刻意不做拖拽上传：WebView2 的 dataTransfer.files 拿不到真实路径，
 * 而 SFTP 上传必须在 Go 侧用本地绝对路径打开文件，这条路走不通。
 * 用「填路径」的输入框反而是当下唯一可靠的形式。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ArrowUp,
  Download,
  File,
  Folder,
  FolderPlus,
  HardDrive,
  Home,
  Loader,
  Pencil,
  RefreshCw,
  Trash2,
  Upload,
  X,
} from '@lucide/vue'
import type { SshHost } from '../utils/ssh'
import {
  SftpDownload,
  SftpList,
  SftpMkdir,
  SftpRemove,
  SftpRename,
  SftpUpload,
} from '../../bindings/quickdock/services/sshx/sshservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../composables/useToast'
import {
  filterEntries,
  formatEntrySize,
  formatMtime,
  joinPath,
  splitPath,
  type SftpEntry,
} from '../utils/sshSftp'

const props = defineProps<{
  host: SshHost
  /**
   * 只有当前可见时才该发请求。
   *
   * 父页面为了保留目录状态用的是 v-show，等于每个标签一打开就实例化了一个
   * 面板；若这里不设闸门，开 5 个标签就会在用户根本没点「文件」的情况下
   * 悄悄做 5 次 SSH 握手 + SFTP 子系统协商。不可达的主机还会挂住
   * 30 秒的拨号超时，并把连接池引用顶上去。
   */
  active: boolean
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const { confirm, error: toastError, success: toastSuccess } = useToast()

const pwd = ref('')
const parent = ref('/')
const entries = ref<SftpEntry[]>([])
const truncated = ref(false)

const loading = ref(false)
const errMsg = ref('')
/** 正在操作的行的 path，防重复点击 */
const rowBusy = ref('')
/** 工具按钮忙（上传/下载/新建目录） */
const toolBusy = ref(false)

const kw = ref('')
const selected = ref<SftpEntry | null>(null)

/** 路径输入框类操作：null 表示没在开任何输入框 */
type PromptKind = 'mkdir' | 'rename' | 'download' | 'upload'
const promptKind = ref<PromptKind | null>(null)
const promptValue = ref('')
const promptTitle = ref('')
const promptLabel = ref('')
const promptOk = ref('')

const rows = computed(() => filterEntries(entries.value, kw.value))
const crumbs = computed(() => splitPath(pwd.value))
const canUp = computed(() => pwd.value !== parent.value && pwd.value !== '/')

async function load(path = pwd.value) {
  if (!path) path = ''
  loading.value = true
  errMsg.value = ''
  // 留在同一目录的刷新要保住选中行：换个目录才清，
  // 否则用户选中一个文件再点刷新，选中就没了，得重新找一遍。
  const keep = path === pwd.value ? selected.value : null
  try {
    const res = unwrap<{
      path: string
      parent: string
      entries: SftpEntry[]
      truncated: boolean
    }>(await SftpList(props.host.id, path))
    pwd.value = res?.path || '/'
    parent.value = res?.parent || '/'
    entries.value = res?.entries || []
    truncated.value = !!res?.truncated
    selected.value =
      keep && entries.value.some(e => e.path === keep.path) ? keep : null
  } catch (e) {
    errMsg.value = getErrorMessage(e)
    entries.value = []
    selected.value = null
  } finally {
    loading.value = false
  }
}

// 依赖里带上 active：从未激活 → 激活时列一次目录，之后每次切回来也刷新一次
// （远端文件可能已经变了，展示上一次的快照会误导人）。pwd 保留，不重置到根目录。
watch(
  () => [props.host.id, props.active] as const,
  () => {
    if (props.active) void load(pwd.value)
  },
  { immediate: true },
)

function openEntry(e: SftpEntry) {
  if (!e.isDir) {
    selected.value = e
    return
  }
  void load(e.path)
}

function goUp() {
  if (canUp.value) void load(parent.value)
}

function goRoot() {
  void load('/')
}

function selectRow(e: SftpEntry) {
  selected.value = e
}

/** 双击是「进入」，单击是「选中」——两者都绑在行上，靠 dblclick 覆盖 click 的默认。 */
function onRowClick(e: SftpEntry) {
  selectRow(e)
}
function onRowDblClick(e: SftpEntry) {
  openEntry(e)
}

// ---- 路径型操作 ----

function openPrompt(kind: PromptKind, seed: string) {
  promptKind.value = kind
  promptValue.value = seed
  switch (kind) {
    case 'mkdir':
      promptTitle.value = t('sshFileNewDir')
      promptLabel.value = t('sshFileDirName')
      promptOk.value = t('create')
      break
    case 'rename':
      promptTitle.value = t('sshFileRename')
      promptLabel.value = t('sshFileNewName')
      promptOk.value = t('save')
      break
    case 'download':
      promptTitle.value = t('sshFileDownload')
      promptLabel.value = t('sshFileDownloadTo')
      promptOk.value = t('sshFileDownload')
      break
    case 'upload':
      promptTitle.value = t('sshFileUpload')
      promptLabel.value = t('sshFileUploadFrom')
      promptOk.value = t('sshFileUpload')
      break
  }
}

function closePrompt() {
  promptKind.value = null
  promptValue.value = ''
}

async function submitPrompt() {
  const kind = promptKind.value
  const value = promptValue.value.trim()
  if (!kind || !value) return

  toolBusy.value = true
  try {
    switch (kind) {
      case 'mkdir':
        unwrap(await SftpMkdir(props.host.id, joinPath(pwd.value, value) || value))
        break
      case 'rename': {
        if (!selected.value) break
        unwrap(await SftpRename(props.host.id, selected.value.path, value))
        break
      }
      case 'download': {
        if (!selected.value) break
        unwrap(await SftpDownload(props.host.id, selected.value.path, value))
        toastSuccess(t('sshFileDownloadDone', { name: selected.value.name }))
        break
      }
      case 'upload':
        unwrap(await SftpUpload(props.host.id, value, pwd.value))
        break
    }
    closePrompt()
    await load()
  } catch (e) {
    toastError(getErrorMessage(e))
    // 失败不清输入框：路径敲错是常态，让用户改一处再试比全部重填友好。
    return
  } finally {
    toolBusy.value = false
  }
}

async function removeEntry(e: SftpEntry) {
  const msg = e.isDir
    ? t('sshFileDeleteDirConfirm', { name: e.name })
    : t('sshFileDeleteConfirm', { name: e.name })
  if (!(await confirm(msg))) return

  rowBusy.value = e.path
  try {
    // 目录一律递归：用户在文件面板里点「删除」的意图就是把它弄走，
    // 弹一个「目录非空」再让他钩一次「递归」属于把一次确认拆成两次。
    unwrap(await SftpRemove(props.host.id, e.path, e.isDir))
    toastSuccess(t('sshFileDeleted', { name: e.name }))
    selected.value = null
    await load()
  } catch (e2) {
    toastError(getErrorMessage(e2))
  } finally {
    rowBusy.value = ''
  }
}

function startRename() {
  if (!selected.value) return
  openPrompt('rename', selected.value.name)
}

function startDownload() {
  if (!selected.value) return
  openPrompt('download', selected.value.name)
}
</script>

<template>
  <div class="sftp">
    <header class="sftp-head">
      <HardDrive :size="14" class="sftp-head-icon" />
      <span class="sftp-title">{{ t('sshFiles') }}</span>

      <div class="sftp-tools">
        <button class="sftp-tool" :disabled="loading" :title="t('refresh')" @click="load()">
          <RefreshCw :size="13" :class="{ spinning: loading }" />
        </button>
        <button class="sftp-tool" :disabled="!canUp" :title="t('sshFileUp')" @click="goUp">
          <ArrowUp :size="14" />
        </button>
        <button class="sftp-tool" :title="t('sshFileHome')" @click="load('')">
          <Home :size="14" />
        </button>
        <button class="sftp-tool" :title="t('sshFileRoot')" @click="goRoot">
          <Folder :size="14" />
        </button>
        <span class="sftp-sep" />
        <button
          class="sftp-tool"
          :disabled="toolBusy"
          :title="t('sshFileNewDir')"
          @click="openPrompt('mkdir', '')"
        >
          <FolderPlus :size="14" />
        </button>
        <button
          class="sftp-tool"
          :disabled="toolBusy"
          :title="t('sshFileUpload')"
          @click="openPrompt('upload', '')"
        >
          <Upload :size="14" />
        </button>
        <button
          class="sftp-tool sftp-tool--danger"
          :disabled="!selected || !!rowBusy"
          :title="t('sshDelete')"
          @click="selected && removeEntry(selected)"
        >
          <Trash2 :size="14" />
        </button>
        <span class="sftp-sep" />
        <button class="sftp-x" :title="t('cancel')" @click="emit('close')">
          <X :size="15" />
        </button>
      </div>
    </header>

    <nav class="sftp-crumbs">
      <button
        v-for="c in crumbs"
        :key="c.path"
        class="sftp-crumb"
        :class="{ 'sftp-crumb--now': c.path === pwd }"
        :disabled="loading"
        @click="load(c.path)"
      >
        {{ c.name }}
      </button>
      <input v-model="kw" class="sftp-kw" :placeholder="t('search')" />
    </nav>

    <div class="sftp-body">
      <div v-if="errMsg" class="sftp-err">{{ errMsg }}</div>

      <div v-else-if="loading && !entries.length" class="sftp-state">
        <Loader :size="16" class="spinning" />
        <span>{{ t('sshFileLoading') }}</span>
      </div>

      <div v-else-if="!rows.length" class="sftp-state">
        {{ kw ? t('sshFileNoMatch') : t('sshFileEmpty') }}
      </div>

      <table v-else class="sftp-table">
        <thead>
          <tr>
            <th class="c-name">{{ t('sshName') }}</th>
            <th class="c-size">{{ t('sshFileSize') }}</th>
            <th class="c-perm">{{ t('sshFilePerm') }}</th>
            <th class="c-time">{{ t('sshFileMtime') }}</th>
            <th class="c-ops" />
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="e in rows"
            :key="e.path"
            class="sftp-row"
            :class="{ 'sftp-row--sel': selected?.path === e.path }"
            @click="onRowClick(e)"
            @dblclick="onRowDblClick(e)"
          >
            <td class="c-name">
              <Folder v-if="e.isDir" :size="14" class="ic ic--dir" />
              <File v-else :size="14" class="ic" />
              <span class="nm">{{ e.name }}</span>
              <span v-if="e.isLink" class="tag-link">link</span>
            </td>
            <td class="c-size">{{ formatEntrySize(e) }}</td>
            <td class="c-perm">{{ e.mode }}</td>
            <td class="c-time">{{ formatMtime(e.modTime) }}</td>
            <td class="c-ops">
              <button
                class="op"
                :disabled="!!rowBusy"
                :title="t('sshFileRename')"
                @click.stop="(selectRow(e), startRename())"
              >
                <Pencil :size="12" />
              </button>
              <button
                v-if="!e.isDir"
                class="op"
                :disabled="!!rowBusy"
                :title="t('sshFileDownload')"
                @click.stop="(selectRow(e), startDownload())"
              >
                <Download :size="12" />
              </button>
              <button
                class="op op--danger"
                :disabled="rowBusy === e.path"
                :title="t('sshDelete')"
                @click.stop="removeEntry(e)"
              >
                <Trash2 :size="12" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>

      <p v-if="truncated && !kw" class="sftp-note">
        {{ t('sshFileTruncated', { n: entries.length }) }}
      </p>
    </div>

    <!-- 路径型操作的统一输入框：上传/下载要本地绝对路径，没有原生文件对话框可用 -->
    <Teleport to="body">
      <Transition name="sfp">
        <div v-if="promptKind" class="sfp-overlay" @mousedown.self="closePrompt">
          <div class="sfp-panel" role="dialog" @keydown.enter.stop="submitPrompt">
            <div class="sfp-title">{{ promptTitle }}</div>
            <label class="sfp-label">{{ promptLabel }}</label>
            <input
              v-model="promptValue"
              class="sfp-input"
              autofocus
              @keydown.esc.stop="closePrompt"
            />
            <div class="sfp-foot">
              <button class="sfp-btn" @click="closePrompt">{{ t('cancel') }}</button>
              <button class="sfp-btn sfp-btn--primary" :disabled="toolBusy" @click="submitPrompt">
                {{ promptOk }}
              </button>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.sftp {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--color-bg-primary);
}

.sftp-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--color-border);
}

.sftp-head-icon {
  color: var(--color-accent);
  flex: 0 0 auto;
}

.sftp-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.sftp-tools {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  margin-left: auto;
}

.sftp-tool {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sftp-tool:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.sftp-tool:disabled {
  cursor: default;
  opacity: 0.4;
}

.sftp-tool--danger:hover:not(:disabled) {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.sftp-sep {
  width: 1px;
  height: 16px;
  background: var(--color-border);
}

.sftp-x {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sftp-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.sftp-crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: 0 0 auto;
  min-width: 0;
  padding: var(--space-3) var(--space-5);
  overflow-x: auto;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.sftp-crumb {
  flex: 0 0 auto;
  padding: 2px var(--space-2);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  white-space: nowrap;
}

.sftp-crumb:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.sftp-crumb--now {
  color: var(--color-text-primary);
  font-weight: 500;
}

.sftp-kw {
  flex: 0 0 130px;
  margin-left: auto;
  padding: 2px var(--space-3);
  font-size: 11px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
}

.sftp-kw:focus {
  border-color: var(--color-accent);
}

.sftp-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
}

.sftp-err {
  margin: var(--space-4) var(--space-5);
  padding: var(--space-3) var(--space-4);
  font-size: 11.5px;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-radius: var(--radius-xs);
}

.sftp-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  padding: var(--space-10) var(--space-4);
  font-size: 12px;
  color: var(--color-text-muted);
}

.sftp-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11.5px;
}

.sftp-table th {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: var(--space-3) var(--space-4);
  font-size: 10.5px;
  font-weight: 500;
  text-align: left;
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.sftp-row {
  cursor: pointer;
}

.sftp-row:hover {
  background: var(--color-bg-hover);
}

.sftp-row--sel {
  background: var(--color-accent-bg);
}

.sftp-table td {
  padding: var(--space-2) var(--space-4);
  color: var(--color-text-secondary);
  border-bottom: 1px solid var(--color-border-light);
}

.c-name {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
}

.ic {
  flex: 0 0 auto;
  color: var(--color-text-disabled);
}

.ic--dir {
  color: var(--color-accent);
}

.nm {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
}

.tag-link {
  flex: 0 0 auto;
  padding: 0 var(--space-2);
  font-size: 9.5px;
  color: var(--color-text-muted);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
}

.c-size,
.c-perm,
.c-time {
  white-space: nowrap;
}

.c-size {
  width: 84px;
  text-align: right;
}

.c-perm {
  width: 92px;
  font-family: var(--font-mono);
  font-size: 10.5px;
}

.c-time {
  width: 128px;
}

.c-ops {
  width: 76px;
  white-space: nowrap;
  text-align: right;
}

.op {
  display: inline-flex;
  padding: var(--space-1);
  margin-left: 2px;
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.op:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.op--danger:hover:not(:disabled) {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.op:disabled {
  cursor: default;
  opacity: 0.4;
}

.sftp-note {
  margin: var(--space-4) var(--space-5);
  font-size: 10.5px;
  color: var(--color-text-muted);
}

/* ---- 路径输入弹窗 ---- */
.sfp-overlay {
  position: fixed;
  inset: 0;
  z-index: 1400;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
}

.sfp-panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  width: 420px;
  max-width: 92vw;
  padding: var(--space-6);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
}

.sfp-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.sfp-label {
  font-size: 11px;
  color: var(--color-text-muted);
}

.sfp-input {
  width: 100%;
  box-sizing: border-box;
  padding: var(--space-3) var(--space-4);
  font-size: 12px;
  font-family: var(--font-mono);
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
}

.sfp-input:focus {
  border-color: var(--color-accent);
}

.sfp-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
}

.sfp-btn {
  padding: var(--space-2) var(--space-5);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sfp-btn:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.sfp-btn--primary {
  color: var(--color-accent-text);
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.sfp-btn--primary:disabled {
  cursor: default;
  opacity: 0.5;
}

.sfp-enter-active,
.sfp-leave-active {
  transition: opacity 0.14s ease;
}

.sfp-enter-from,
.sfp-leave-to {
  opacity: 0;
}

.spinning {
  animation: sftp-spin 800ms linear infinite;
}

@keyframes sftp-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
