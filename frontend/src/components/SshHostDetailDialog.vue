<script lang="ts" setup>
/**
 * 单主机详情。
 *
 * 为什么单独一个弹窗而不是把它塞进编辑表单：编辑表单是「要改什么」的界面，
 * 混进只读字段会让「哪些能改」变得模糊；而详情是「这台机器现在是什么状态」，
 * 包含好几个压根不可编辑的项（最近连接、创建时间、凭据是否已保存）。
 *
 * 数据现取（GetHost）而不是用树上那条：列表可能是上次加载时的快照，
 * 别处改过之后要在这里看出「当前值」，否则详情就成了另一个过期的副本。
 * 取不到时退回传入的快照并提示——宁可展示旧值，也不要弹个空窗。
 */
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pencil, Copy, Check, X, Server } from '@lucide/vue'
import { GetHost } from '../../bindings/quickdock/services/sshx/sshservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { copyToClipboard } from '../utils/clipboard'
import { sshEnvClass, sshEnvKey, sshTargetFull, type SshHost } from '../utils/ssh'

const { t } = useI18n()

const props = defineProps<{
  visible: boolean
  /** 要查看的主机 ID */
  hostId: string
  /** 列表里那条快照：请求失败或尚未返回时先展示它 */
  fallback?: SshHost | null
  /** 所属分组名（树上的显示名）；空串表示未分组 */
  folderName?: string
}>()

const emit = defineEmits<{
  (e: 'edit', host: SshHost): void
  (e: 'close'): void
}>()

const host = ref<SshHost | null>(null)
const loading = ref(false)
const errMsg = ref('')
const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null

watch(
  () => [props.visible, props.hostId] as const,
  async ([vis, id]) => {
    if (!vis || !id) return
    loading.value = true
    errMsg.value = ''
    host.value = props.fallback ?? null
    try {
      const fresh = unwrap<SshHost>(await GetHost(id))
      if (fresh) host.value = fresh
    } catch (e) {
      errMsg.value = getErrorMessage(e)
      // host 仍为 fallback：见文件头注释
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

function authLabel(h: SshHost): string {
  return h.authType === 'key' ? t('sshAuthKey') : t('sshAuthPassword')
}

/** 凭据是否已在本地保存。只给「有/没有」，不问也不显示明文。 */
function credLabel(h: SshHost): string {
  const saved = h.authType === 'key' ? h.hasPassphrase : h.hasPassword
  return saved ? t('sshHostCredSaved') : t('sshHostCredNone')
}

function fmtTime(s?: string): string {
  if (!s) return t('sshHostNever')
  // 后端时间串带时区偏移，Date 解析后再按本地格式输出；解析不了就原样显示，
  // 好过显示 Invalid Date 让人以为数据坏了。
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

async function copyTarget() {
  const h = host.value
  if (!h) return
  const ok = await copyToClipboard(sshTargetFull(h))
  if (!ok) return
  copied.value = true
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { copied.value = false }, 1500)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="hdd">
      <div v-if="visible" class="hdd-overlay" @mousedown.self="emit('close')">
        <div class="hdd-panel" role="dialog">
          <header class="hdd-head">
            <Server :size="15" class="hdd-head-icon" />
            <span class="hdd-title">{{ t('sshHostDetail') }}</span>
            <button class="hdd-x" :title="t('cancel')" @click="emit('close')">
              <X :size="15" />
            </button>
          </header>

          <div v-if="host" class="hdd-body">
            <div class="hdd-hero">
              <span class="hdd-hero-name">{{ host.name || host.host }}</span>
              <span class="hdd-env" :class="sshEnvClass(host.env)">{{ t(sshEnvKey(host.env)) }}</span>
            </div>

            <div class="hdd-grid">
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshHostAddr') }}</span>
                <span class="hdd-v hdd-v--mono">{{ host.host }}:{{ host.port }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshUser') }}</span>
                <span class="hdd-v hdd-v--mono">{{ host.user }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshEnvLabel') }}</span>
                <span class="hdd-v">{{ t(sshEnvKey(host.env)) }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshHostDetailAuth') }}</span>
                <span class="hdd-v">
                  {{ authLabel(host) }}
                  <span class="hdd-sub">· {{ credLabel(host) }}</span>
                </span>
              </div>
              <div v-if="host.authType === 'key'" class="hdd-row">
                <span class="hdd-k">{{ t('sshKeyPath') }}</span>
                <span class="hdd-v hdd-v--wrap">{{ host.keyPath || '—' }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshLegacyShort') }}</span>
                <span class="hdd-v">{{ host.legacyAlgorithms ? t('sshOn') : t('sshOff') }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshHostGroup') }}</span>
                <span class="hdd-v">{{ folderName || t('sshHostGroupRoot') }}</span>
              </div>
              <div v-if="host.remark" class="hdd-row">
                <span class="hdd-k">{{ t('sshRemark') }}</span>
                <span class="hdd-v hdd-v--wrap">{{ host.remark }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshHostLastConnected') }}</span>
                <span class="hdd-v">{{ fmtTime(host.lastConnectedAt) }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshHostUpdatedAt') }}</span>
                <span class="hdd-v">{{ fmtTime(host.updatedAt || host.createdAt) }}</span>
              </div>
              <div class="hdd-row">
                <span class="hdd-k">{{ t('sshHostId') }}</span>
                <span class="hdd-v hdd-v--mono hdd-v--wrap">{{ host.id }}</span>
              </div>
            </div>

            <div v-if="loading" class="hdd-note">{{ t('loading') }}</div>
            <div v-else-if="errMsg" class="hdd-note hdd-note--warn">
              {{ t('sshHostDetailStale', { err: errMsg }) }}
            </div>
          </div>

          <div v-else class="hdd-body hdd-body--empty">
            {{ loading ? t('loading') : t('sshHostDetailEmpty') }}
          </div>

          <footer class="hdd-foot">
            <button class="hdd-btn" @click="copyTarget">
              <Check v-if="copied" :size="13" />
              <Copy v-else :size="13" />
              {{ copied ? t('logCopied') : t('sshHostCopyTarget') }}
            </button>
            <span class="hdd-foot-spacer" />
            <button class="hdd-btn" @click="emit('close')">{{ t('close') }}</button>
            <button
              v-if="host"
              class="hdd-btn hdd-btn--primary"
              @click="emit('edit', host)"
            >
              <Pencil :size="13" />
              {{ t('sshEdit') }}
            </button>
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.hdd-overlay {
  position: fixed;
  inset: 0;
  z-index: 3000;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
}

.hdd-panel {
  width: 440px;
  max-width: calc(100vw - 40px);
  max-height: calc(100vh - 80px);
  display: flex;
  flex-direction: column;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 16px 48px var(--color-bg-overlay);
}

.hdd-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.hdd-head-icon { color: var(--color-accent); }
.hdd-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.hdd-x {
  margin-left: auto;
  display: flex; align-items: center;
  padding: 3px; border: none; border-radius: var(--radius-sm);
  background: none; color: var(--color-text-muted); cursor: pointer;
}
.hdd-x:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.hdd-body {
  flex: 1; min-height: 0;
  overflow-y: auto;
  padding: 12px 14px;
}
.hdd-body--empty {
  display: flex; align-items: center; justify-content: center;
  min-height: 120px; font-size: 12px; color: var(--color-text-muted);
}

.hdd-hero {
  display: flex; align-items: center; gap: 8px;
  margin-bottom: 10px;
}
.hdd-hero-name {
  font-size: 14px; font-weight: 600; color: var(--color-text-primary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.hdd-env {
  flex-shrink: 0;
  font-size: 10px; line-height: 16px; padding: 0 6px;
  border-radius: var(--radius-sm);
}

.hdd-grid { display: flex; flex-direction: column; gap: 6px; }
.hdd-row {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: 8px;
  align-items: baseline;
}
.hdd-k { font-size: 11px; color: var(--color-text-muted); }
.hdd-v { font-size: 12px; color: var(--color-text-primary); min-width: 0; }
.hdd-v--mono { font-family: var(--font-mono); font-size: 11px; }
/* 路径与 ID 会比面板宽，截断之后剩下的那点信息恰恰没用（看不出是哪台机器） */
.hdd-v--wrap { word-break: break-all; }
.hdd-sub { color: var(--color-text-muted); font-size: 10.5px; margin-left: 3px; }

.hdd-note { margin-top: 8px; font-size: 10.5px; color: var(--color-text-muted); }
.hdd-note--warn { color: var(--color-warning); }

.hdd-foot {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 14px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}
.hdd-foot-spacer { flex: 1; }
.hdd-btn {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 5px 11px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
  font: inherit; font-size: 11.5px; cursor: pointer;
}
.hdd-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.hdd-btn--primary {
  border-color: var(--color-accent);
  background: var(--color-accent-bg);
  color: var(--color-accent);
}

.hdd-enter-active, .hdd-leave-active { transition: opacity 0.14s ease; }
.hdd-enter-from, .hdd-leave-to { opacity: 0; }
</style>
