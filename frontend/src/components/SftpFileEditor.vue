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
  SftpFileDigest,
  SftpReadText,
  SftpWriteTextChecked,
} from '../../bindings/quickdock/services/sshx/sshservice'
import { apiErrorCode, unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
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
/**
 * 打开时记录的远端摘要基线，保存时透传给后端比对。空串表示取摘要失败
 * （或刷新失败），降级为不做并发校验——绝不拿过期摘要把后续保存全挡掉。
 */
const baseDigest = ref<string>('')

const editorRef = ref<InstanceType<typeof ConfigEditor> | null>(null)

const dirty = computed(() => !binary.value && draft.value !== original.value)
const canSave = computed(
  () => !binary.value && !truncated.value && dirty.value && !saving.value,
)

/**
 * 单独取一次远端摘要。内容读取与摘要是两次独立的一次性调用，这里抽出来
 * 供 load（并行取基线）与 save（保存后刷新基线）共用。
 *
 * 失败一律降级为空串而不是向上抛：摘要只服务于「保存时的并发校验」，
 * 拿不到它文件照样能看、能存。按 R9 记日志、不空 catch，也不弹 toast
 * 打扰用户（与 SshBatchDialog.loadCommands 的静默降级一致）。
 */
async function fetchDigest(): Promise<string> {
  try {
    const res = unwrap<{ digest: string }>(
      await SftpFileDigest(props.hostId, props.entry.path),
    )
    return res?.digest ?? ''
  } catch (e) {
    logErr('sftp-editor', e)
    return ''
  }
}

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
    // 内容与摘要并行取：两者都是「一次性借连接」的独立往返，串行会让打开
    // 慢一倍；摘要还必须对应这次读到的同一份内容快照，否则等于拿 A 时刻
    // 的摘要去守 B 时刻的内容。fetchDigest 自带降级，Promise.all 只会在
    // 读内容失败时 reject，交给外层 catch 按 R9 呈现 + 记日志。
    const [text, digest] = await Promise.all([
      SftpReadText(props.hostId, props.entry.path, READ_LIMIT)
        .then((r) => unwrap<{ content: string }>(r)?.content ?? ''),
      fetchDigest(),
    ])
    original.value = text
    draft.value = text
    binary.value = looksBinary(text)
    // 远端 size 来自列表接口的 Stat；比上限大就说明读到的不是完整内容。
    truncated.value = props.entry.size > READ_LIMIT
    baseDigest.value = digest
  } catch (e) {
    // 错误沿用本组件既有的面板内 loadErr 呈现，R9 要求补日志且不空 catch。
    loadErr.value = getErrorMessage(e)
    logErr('sftp-editor', e)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!canSave.value) return
  saving.value = true
  try {
    // mode 传 0：不改权限。把 600 的私钥配置改成 644 是没人想要的副作用。
    // baseDigest 必须透传：withSftp 每次借完就还，服务端两次调用之间不共享
    // 任何句柄，没有地方能替编辑器记住「打开时」的摘要。
    unwrap(await SftpWriteTextChecked(
      props.hostId, props.entry.path, draft.value, baseDigest.value, 0,
    ))
    original.value = draft.value
    // 保存成功后必须刷新摘要基线：内容已变，旧摘要会在下一次保存时被判成
    // 「远端已修改」，把用户自己的连续编辑全挡掉。门面方法不返回新摘要，
    // 只能再取一次；取不到就由 fetchDigest 降级为空串，宁可不校验也不留脏基线。
    baseDigest.value = await fetchDigest()
    // 基线要在「保存成功后」刷新，否则编辑器的「已修改」标记会一直亮着。
    editorRef.value?.markSaved()
    toastSuccess(t('sshFileEditorSaved', { name: props.entry.name }))
    emit('saved')
  } catch (e) {
    // 并发变更单独提示：通用文案只会让用户反复点保存，反而把别人的改动
    // 覆盖掉。按项目既有模式用 apiErrorCode 取业务码；该码尚未收进 SSH_ERR，
    // 与 SftpPanel 对 ssh_transfer_canceled 一样用字面量比较。
    if (apiErrorCode(e) === 'ssh_remote_changed') {
      toastError(t('sshFileEditorRemoteChanged'))
      logErr('sftp-editor', e)
      return
    }
    toastError(getErrorMessage(e))
    logErr('sftp-editor', e)
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
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
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
  gap: var(--space-1);
  min-width: 0;
  flex: 1 1 auto;
}

.sfe-name {
  font-size: var(--text-md);
  font-weight: 600;
  color: var(--color-text-primary);
}

/* 路径可能很长，单行截断 + title 兜底，不要让它把弹窗顶宽 */
.sfe-path {
  font-size: var(--text-xs);
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
  font-size: var(--text-sm);
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
  font-size: var(--text-xs);
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
  font-size: var(--text-xs);
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
  height: 28px; padding: 0 11px;
  font-size: var(--text-sm);
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
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
