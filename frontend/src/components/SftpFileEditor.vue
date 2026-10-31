<script lang="ts" setup>
/**
 * 远端文本文件查看 / 编辑弹窗（双击文件打开）。
 *
 * 定位：文件面板里「点开看一眼、顺手改一个字」的轻量用途，不是 IDE。
 * 编辑区直接复用 ConfigEditor（行号槽 + 语法着色 + Ctrl+F 查找替换），
 * 与「环境 → 编辑配置文件」是同一个组件——两条路径的「看一个文本文件」
 * 本来就没有区别，各自实现一套只会让快捷键和配色漂移。
 *
 * 三条安全边界（都不是可选项）：
 *   1. 二进制按文本打开并保存 = 静默写坏远端文件，故内容判定与扩展名判定双保险。
 *   2. 只读了前 READ_LIMIT 字节时禁止保存——保存会用这段前缀覆盖整个文件，
 *      是在删数据，不是在编辑。
 *   3. 关窗前有未保存改动要确认，避免误触丢掉修改。
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle, Download, Loader, Save, X } from '@lucide/vue'
import ConfigEditor from './ConfigEditor.vue'
import {
  SftpReadText,
  SftpWriteText,
} from '../../bindings/quickdock/services/sshx/sshservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../composables/useToast'
import { formatBytes } from '../utils/ssh'
import { isPreviewable, looksBinary, type SftpEntry } from '../utils/sshSftp'

const props = defineProps<{
  hostId: string
  entry: SftpEntry
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved'): void
  (e: 'download', entry: SftpEntry): void
}>()

const { t } = useI18n()
const { confirm, error: toastError, success: toastSuccess } = useToast()

/**
 * 一次最多读 512KB。选这个数是因为：nginx / sshd 这类配置文件全在这里面，
 * 而日志那种几百 MB 的文件本来也不该塞进编辑器。
 * 后端 SftpReadText 的默认上限是 1MB，这里显式传参，避免两端默认值漂移。
 */
const READ_LIMIT = 512 * 1024

const loading = ref(true)
const saving = ref(false)
const loadErr = ref('')
/** 原始内容，用于算 dirty 与「未保存」判断。 */
const original = ref('')
const draft = ref('')
/** 内容判定为二进制：既不能编辑也不能保存。 */
const binary = ref(false)
/** 远端文件比 READ_LIMIT 大：只读。 */
const truncated = ref(false)

const editorRef = ref<InstanceType<typeof ConfigEditor> | null>(null)

const dirty = computed(() => !binary.value && draft.value !== original.value)
const canSave = computed(
  () => !binary.value && !truncated.value && dirty.value && !saving.value,
)

async function load() {
  loading.value = true
  loadErr.value = ''
  // 扩展名一眼就是二进制的，连读都不读：读一个 512KB 的 PNG 过来只为了
  // 判定它是二进制，等于白付一次 SFTP 往返 + 传输。
  if (!isPreviewable(props.entry)) {
    binary.value = true
    loading.value = false
    return
  }
  try {
    const res = unwrap<{ content: string }>(
      await SftpReadText(props.hostId, props.entry.path, READ_LIMIT),
    )
    const text = res?.content ?? ''
    original.value = text
    draft.value = text
    binary.value = looksBinary(text)
    // 远端 size 来自列表接口的 Stat；比上限大就说明读到的不是完整内容。
    truncated.value = props.entry.size > READ_LIMIT
  } catch (e) {
    loadErr.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!canSave.value) return
  saving.value = true
  try {
    // mode 传 0：不改权限。把 600 的私钥配置改成 644 是没人想要的副作用。
    unwrap(await SftpWriteText(props.hostId, props.entry.path, draft.value, 0))
    original.value = draft.value
    // 基线要在「保存成功后」刷新，否则编辑器的「已修改」标记会一直亮着。
    editorRef.value?.markSaved()
    toastSuccess(t('sshFileEditorSaved', { name: props.entry.name }))
    emit('saved')
  } catch (e) {
    toastError(getErrorMessage(e))
  } finally {
    saving.value = false
  }
}

async function requestClose() {
  if (dirty.value) {
    if (!(await confirm(t('sshFileEditorUnsaved', { name: props.entry.name })))) return
  }
  emit('close')
}

/**
 * 只管 Esc：Ctrl+S 由 ConfigEditor 自己拦（它已经 preventDefault 并 emit save），
 * 这里再拦一次会让一次按键触发两次保存。
 */
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault()
    void requestClose()
  }
}

onMounted(load)
</script>

<template>
  <Teleport to="body">
    <div class="sfe-overlay" @mousedown.self="requestClose">
      <div class="sfe-panel" role="dialog" @keydown="onKeydown">
        <header class="sfe-head">
          <div class="sfe-head-main">
            <span class="sfe-name">{{ entry.name }}</span>
            <span class="sfe-path" :title="entry.path">{{ entry.path }}</span>
          </div>
          <button class="sfe-icon-btn" :title="t('close')" @click="requestClose">
            <X :size="15" />
          </button>
        </header>

        <div v-if="loading" class="sfe-state">
          <Loader :size="16" class="spinning" />
          <span>{{ t('sshFileLoading') }}</span>
        </div>

        <div v-else-if="loadErr" class="sfe-state sfe-state--err">{{ loadErr }}</div>

        <div v-else-if="binary" class="sfe-state">
          <AlertTriangle :size="18" class="sfe-warn-icon" />
          <p class="sfe-state-text">{{ t('sshFileEditorBinary') }}</p>
          <button class="sfe-btn" @click="emit('download', entry)">
            <Download :size="13" />
            <span>{{ t('sshFileDownload') }}</span>
          </button>
        </div>

        <template v-else>
          <p v-if="truncated" class="sfe-banner">
            <AlertTriangle :size="13" />
            <span>{{ t('sshFileEditorTruncated', { size: formatBytes(entry.size), limit: formatBytes(READ_LIMIT) }) }}</span>
          </p>
          <ConfigEditor
            ref="editorRef"
            v-model="draft"
            :readonly="truncated"
            :filename="entry.name"
            @save="save"
          />
        </template>

        <footer class="sfe-foot">
          <span v-if="dirty" class="sfe-dirty">{{ t('sshFileEditorDirty') }}</span>
          <button class="sfe-btn" @click="requestClose">{{ t('close') }}</button>
          <button
            class="sfe-btn sfe-btn--primary"
            :disabled="!canSave"
            :title="canSave ? t('sshFileEditorSaveHint') : ''"
            @click="save"
          >
            <Save :size="13" />
            <span>{{ saving ? t('sshFileSaving') : t('save') }}</span>
          </button>
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.sfe-overlay {
  position: fixed;
  inset: 0;
  z-index: 1400;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
}

.sfe-panel {
  display: flex;
  flex-direction: column;
  width: min(920px, 92vw);
  height: min(720px, 86vh);
  min-height: 0;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden;
}

.sfe-head {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--color-border);
}

.sfe-head-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1 1 auto;
}

.sfe-name {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

/* 路径可能很长，单行截断 + title 兜底，不要让它把弹窗顶宽 */
.sfe-path {
  font-size: 10.5px;
  font-family: var(--font-mono);
  color: var(--color-text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.sfe-icon-btn {
  display: flex;
  flex: 0 0 auto;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sfe-icon-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.sfe-state {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-4);
  padding: var(--space-6);
  font-size: 12px;
  color: var(--color-text-secondary);
}

.sfe-state--err {
  color: var(--color-danger);
}

.sfe-state-text {
  margin: 0;
  text-align: center;
  max-width: 42ch;
  line-height: 1.6;
}

.sfe-warn-icon {
  color: var(--color-warning);
}

.sfe-banner {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  margin: 0;
  padding: var(--space-3) var(--space-5);
  font-size: 11px;
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-bottom: 1px solid var(--color-border);
}

/*
 * ConfigEditor 自带 margin: 12px 18px 0 与边框，那是为「页面里的一块编辑区」
 * 定的；放进弹窗后弹窗自己已经有边框和内边距，不改会多出一圈错位的空隙。
 */
.sfe-panel :deep(.ce) {
  flex: 1 1 auto;
  min-height: 0;
  margin: 0;
  border: none;
  border-radius: 0;
}

.sfe-foot {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: var(--space-3) var(--space-5);
  border-top: 1px solid var(--color-border);
}

.sfe-dirty {
  margin-left: auto;
  font-size: 10.5px;
  color: var(--color-warning);
}

.sfe-foot .sfe-btn:first-of-type {
  margin-left: auto;
}

/* 有「未保存」标记时它已经占了 auto margin，按钮不再重复推开 */
.sfe-dirty + .sfe-btn {
  margin-left: 0;
}

.sfe-btn {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-5);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.sfe-btn:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.sfe-btn--primary {
  color: var(--color-accent-text);
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.sfe-btn--primary:disabled {
  cursor: default;
  opacity: 0.45;
}

.spinning {
  animation: sfe-spin 800ms linear infinite;
}

@keyframes sfe-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
