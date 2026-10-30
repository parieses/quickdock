<script lang="ts" setup>
/**
 * 已信任服务器（指纹）管理弹窗。
 *
 * 解决一个具体的困境：用户一旦信任过某台服务器的指纹，除了删数据库重配
 * 没有任何撤销途径。服务器重装了系统、换了密钥、被中间人替换之后，
 * 旧记录会一直把连接挡住（报 ssh_hostkey_changed），而用户能做的只有
 * 打开数据库文件手删——这是不可接受的。
 *
 * 入口放在 SSH 窗口工具栏而不是设置页：它和「连接」是同一个心智模型下的
 * 事，用户在排查连不上的时候就在这个窗口里。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw, ShieldCheck, Trash2, X } from '@lucide/vue'
import {
  ListAllTrustedKeys,
  RemoveTrustedKeys,
} from '../../bindings/quickdock/services/sshx/sshservice'
import { CopyText } from '../../bindings/quickdock/services/clipboard/clipboardservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../composables/useToast'
import { sshHostKeyFile, type TrustedHost } from '../utils/ssh'
import { useFocusTrap } from '../utils/focusTrap'

const { t } = useI18n()
const { confirm, error: toastError, success: toastSuccess } = useToast()

const visible = defineModel<boolean>('visible', { required: true })

const panelRef = ref<HTMLElement | null>(null)
// defineModel 返回的 WritableComputedRef 就是 Ref<boolean>，可直接喂给 focusTrap。
const { onKeydown: onKeydownTrap } = useFocusTrap(visible, panelRef)

const list = ref<TrustedHost[]>([])
const loading = ref(false)
const loadErr = ref('')
/** 正在删除的 target，用来禁用该行的按钮防重复点击 */
const removingKey = ref('')

const hasAny = computed(() => list.value.length > 0)

/**
 * 按 host:port 分组。
 *
 * 一台服务器可能信任了多种密钥（ed25519 + ecdsa + rsa），
 * 逐条列会让用户看到三行几乎一样的记录，还要分别删——
 * 实际需求是「这台机器我不信了，一次清干净」。
 */
const groups = computed(() => {
  const map = new Map<string, TrustedHost[]>()
  for (const item of list.value) {
    const key = item.display
    const arr = map.get(key)
    if (arr) arr.push(item)
    else map.set(key, [item])
  }
  return [...map.values()].map(items => ({
    display: items[0].display,
    hostId: items[0].hostId,
    hostName: items[0].hostName,
    items,
  }))
})

async function load() {
  loading.value = true
  loadErr.value = ''
  try {
    list.value = unwrap<TrustedHost[]>(await ListAllTrustedKeys()) || []
  } catch (e) {
    loadErr.value = getErrorMessage(e)
    list.value = []
  } finally {
    loading.value = false
  }
}

watch(visible, v => {
  if (v) void load()
})

async function removeGroup(g: { display: string; hostName: string; items: TrustedHost[] }) {
  const name = g.hostName || g.display
  // 措辞要说清后果：清掉后下次连接会重新弹指纹确认（不是「永久不再校验」）。
  const ok = await confirm(t('sshForgetConfirm', { name }))
  if (!ok) return
  removingKey.value = g.display
  try {
    unwrap(await RemoveTrustedKeys(g.items[0].host, g.items[0].port))
    toastSuccess(t('sshForgetDone', { name }))
    await load()
  } catch (e) {
    toastError(getErrorMessage(e))
  } finally {
    removingKey.value = ''
  }
}

/** 给用户一条可照抄的核对命令：上服务器跑它，比对输出与列表里的指纹。 */
function verifyCommand(item: TrustedHost): string {
  return `ssh-keygen -lf /etc/ssh/${sshHostKeyFile(item.keyType)}`
}

async function copy(text: string) {
  // WebView2 里 navigator.clipboard 会静默失败，必须走 Go 侧。
  try {
    await CopyText(text)
    toastSuccess(t('copied'))
  } catch (e) {
    toastError(getErrorMessage(e))
  }
}

/**
 * addedAt 是 Unix 毫秒（Go 侧 `time.Now().UnixMilli()` 写入，与仓库其它表一致），
 * 直接喂 Date 即可。别再乘 1000 —— 那是把毫秒当秒，会算出公元 5 万年。
 * 空值显示 '-'，避免出现「1970 年」误导用户。
 */
function formatDate(ts: number): string {
  if (!ts) return '-'
  // 容错：极小的值只可能是被误存成秒的旧记录（1e11 毫秒 ≈ 公元 5138 年），补齐后再格式化。
  const d = new Date(ts < 1e11 ? ts * 1000 : ts)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleDateString()
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    visible.value = false
    return
  }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="th">
      <div v-if="visible" class="th-overlay" @mousedown.self="visible = false" @keydown="onKeydown">
        <div ref="panelRef" class="th-panel" role="dialog">
          <header class="th-head">
            <span class="th-title">{{ t('sshTrustedTitle') }}</span>
            <div class="th-head-tools">
              <button class="th-tool" :disabled="loading" :title="t('refresh')" @click="load">
                <RefreshCw :size="13" :class="{ spinning: loading }" />
              </button>
              <button class="th-x" :title="t('cancel')" @click="visible = false">
                <X :size="16" />
              </button>
            </div>
          </header>

          <div class="th-body">
            <p class="th-hint">{{ t('sshTrustedHint') }}</p>

            <div v-if="loadErr" class="th-error">{{ loadErr }}</div>

            <div v-if="loading && !hasAny" class="th-hint-block">
              <span class="th-spinner" />
            </div>

            <div v-else-if="!hasAny && !loadErr" class="th-empty">
              <ShieldCheck :size="26" class="th-empty-icon" />
              <p class="th-empty-text">{{ t('sshTrustedEmpty') }}</p>
            </div>

            <div v-for="g in groups" :key="g.display" class="th-group">
              <div class="th-group-head">
                <div class="th-group-meta">
                  <span class="th-group-name">{{ g.hostName || g.display }}</span>
                  <span class="th-group-target">{{ g.display }}</span>
                </div>
                <button
                  class="th-forget"
                  :disabled="removingKey === g.display"
                  @click="removeGroup(g)"
                >
                  <Trash2 :size="12" />
                  <span>{{ t('sshForget') }}</span>
                </button>
              </div>

              <div v-for="item in g.items" :key="item.keyType" class="th-key">
                <div class="th-key-main">
                  <span class="th-key-type">{{ item.keyType }}</span>
                  <code class="th-fp">{{ item.fingerprint }}</code>
                </div>
                <div class="th-key-side">
                  <button
                    class="th-copy"
                    :title="t('sshVerifyCommand')"
                    @click="copy(verifyCommand(item))"
                  >
                    {{ t('sshVerify') }}
                  </button>
                  <span class="th-added">{{ t('sshAddedAt', { date: formatDate(item.addedAt) }) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.th-overlay {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

.th-panel {
  display: flex;
  flex-direction: column;
  width: 560px;
  max-width: 94vw;
  max-height: 76vh;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden;
}

.th-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  flex: 0 0 auto;
  padding: var(--space-6) var(--space-7);
  border-bottom: 1px solid var(--color-border);
}

.th-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.th-head-tools {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.th-tool {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.th-tool:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.th-x {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.th-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.th-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: var(--space-5) var(--space-7) var(--space-6);
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.th-hint {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--color-text-muted);
}

.th-error {
  padding: var(--space-3) var(--space-4);
  font-size: 11.5px;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-radius: var(--radius-xs);
}

.th-hint-block {
  display: flex;
  justify-content: center;
  padding: var(--space-8);
}

.th-spinner {
  width: 16px;
  height: 16px;
  border: 2px solid var(--color-border-light);
  border-top-color: var(--color-accent);
  border-radius: var(--radius-full);
  animation: th-spin 700ms linear infinite;
}

@keyframes th-spin {
  to {
    transform: rotate(360deg);
  }
}

.spinning {
  animation: th-spin 900ms linear infinite;
}

.th-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-8) var(--space-4);
}

.th-empty-icon {
  color: var(--color-text-disabled);
}

.th-empty-text {
  margin: 0;
  font-size: 12px;
  color: var(--color-text-muted);
}

.th-group {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  overflow: hidden;
}

.th-group-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-4) var(--space-5);
  background: var(--color-bg-tertiary);
}

.th-group-meta {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.th-group-name {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--color-text-primary);
}

.th-group-target {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.th-forget {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  padding: var(--space-2) var(--space-4);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-danger);
  background: transparent;
  border: 1px solid var(--color-danger-bg);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.th-forget:hover:not(:disabled) {
  background: var(--color-danger-bg);
}

.th-forget:disabled {
  cursor: default;
  opacity: 0.5;
}

.th-key {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-5);
  border-top: 1px solid var(--color-border);
}

.th-key-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.th-key-type {
  font-size: 11px;
  color: var(--color-text-secondary);
}

.th-fp {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.th-key-side {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: 0 0 auto;
}

.th-copy {
  padding: 2px var(--space-3);
  font-size: 10.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.th-copy:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.th-added {
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.th-enter-active,
.th-leave-active {
  transition: opacity 0.16s ease;
}

.th-enter-from,
.th-leave-to {
  opacity: 0;
}
</style>
