<script setup lang="ts">
/**
 * 密码库页面：条目管理 + 浏览器扩展配对。
 *
 * 布局沿用「左列表 / 右详情」两栏（与知识库页一致）。三点取舍：
 *  1. 密码默认不显示，点眼睛才 Reveal 一次 —— 列表接口本来就不带密码，
 *     这里是第二道闸；顺手把「显示过一次」留在审计日志里。
 *  2. 「保存」不做自动提交：密码是低频、慎重的编辑，误触代价比多点一次高。
 *  3. 复制走 CopyCredentialSecret（后端写剪贴板 + 30 秒后比对清除），
 *     前端自己写剪贴板会绕过回环令牌，密码会被记进剪贴板历史。
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Check, Copy, Eye, EyeOff, Globe, KeyRound, Plus, RefreshCw, Trash2, Wand2,
} from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import type { CredentialSummary } from '../composables/useCredential'
import {
  CopyCredentialSecret, CopyExtensionKey, DeleteCredential, DisableExtension, EnableExtension,
  GeneratePassword, ListCredentials, RevealCredential, SaveCredential, Status,
} from '../../bindings/quickdock/services/credentials/credentialsservice'

const { t } = useI18n()
const toast = useToast()

interface VaultStatusView {
  available: boolean
  count: number
  platformProtected: boolean
  lockBound: boolean
  extensionEnabled: boolean
  extensionPaired: boolean
  extensionPort: number
}

const list = ref<CredentialSummary[]>([])
const status = ref<VaultStatusView | null>(null)
const selectedId = ref('')
const query = ref('')
const loading = ref(false)
const saving = ref(false)

/** 表单。secret 留空 + hasSecret 表示「不改密码」，与后端约定一致。 */
const form = reactive({
  name: '',
  kind: 'login',
  username: '',
  secret: '',
  clearSecret: false,
  host: '',
  port: 0,
  url: '',
  notes: '',
  tags: '',
  favorite: false,
})

const showSecret = ref(false)
const revealedSecret = ref('')

/** 浏览器集成：一次开关，长期有效。extBusy 防连点（开关是幂等接口，但连点会闪）。 */
const extBusy = ref(false)

const selected = computed(() => list.value.find((c) => c.id === selectedId.value) ?? null)
const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return list.value
  return list.value.filter((c) =>
    [c.name, c.username, c.host, c.tags].some((v) => (v || '').toLowerCase().includes(q)))
})

const kindOptions = computed(() => [
  { value: 'login', label: t('credKindLogin') },
  { value: 'ssh', label: t('credKindSsh') },
  { value: 'database', label: t('credKindDatabase') },
  { value: 'webdav', label: t('credKindWebdav') },
  { value: 'git', label: t('credKindGit') },
  { value: 'api_key', label: t('credKindApiKey') },
  { value: 'other', label: t('credKindOther') },
])

function kindLabel(kind: string): string {
  return kindOptions.value.find((o) => o.value === kind)?.label ?? kind
}

function fillForm(item: CredentialSummary | null) {
  showSecret.value = false
  revealedSecret.value = ''
  form.name = item?.name ?? ''
  form.kind = item?.kind || 'login'
  form.username = item?.username ?? ''
  form.secret = ''
  form.clearSecret = false
  form.host = item?.host ?? ''
  form.port = item?.port ?? 0
  form.url = item?.url ?? ''
  form.notes = item?.notes ?? ''
  form.tags = item?.tags ?? ''
  form.favorite = item?.favorite ?? false
}

async function load(keepId = '') {
  loading.value = true
  try {
    const res = await ListCredentials()
    list.value = unwrap<CredentialSummary[]>(res) ?? []
    const target = keepId
      ? list.value.find((c) => c.id === keepId)
      : list.value.find((c) => c.id === selectedId.value)
    if (target) {
      selectedId.value = target.id
      fillForm(target)
    } else if (list.value.length > 0) {
      selectedId.value = list.value[0].id
      fillForm(list.value[0])
    } else {
      selectedId.value = ''
      fillForm(null)
    }
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credLoadFailed'))
    logErr('CredentialsPage.load', e)
  } finally {
    loading.value = false
  }
}

async function loadStatus() {
  try {
    status.value = unwrap<VaultStatusView>(await Status())
  } catch (e) {
    logErr('CredentialsPage.status', e)
  }
}

function selectItem(item: CredentialSummary) {
  selectedId.value = item.id
  fillForm(item)
}

function createNew() {
  selectedId.value = ''
  fillForm(null)
  showSecret.value = false
}

async function save() {
  if (saving.value) return
  if (!form.name.trim() && !form.host.trim() && !form.username.trim()) {
    toast.error(t('credNeedOneField'))
    return
  }
  saving.value = true
  try {
    const res = await SaveCredential({
      id: selectedId.value,
      name: form.name.trim(),
      kind: form.kind,
      username: form.username.trim(),
      secret: form.secret,
      clearSecret: form.clearSecret,
      host: form.host.trim(),
      port: Number(form.port) || 0,
      url: form.url.trim(),
      notes: form.notes,
      tags: form.tags.trim(),
      // 来源是只读溯源信息：编辑时原样带回。清空它会让「SSH 主机 → 密码库」
      // 的关联断掉，下次改主机密码就会多出一条重复条目。
      source: selected.value?.source ?? '',
      sourceId: selected.value?.sourceId ?? '',
      favorite: form.favorite,
    })
    const saved = unwrap<CredentialSummary>(res)
    toast.success(t('credSaved'))
    await load(saved?.id ?? selectedId.value)
    await loadStatus()
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credSaveFailed'))
    logErr('CredentialsPage.save', e)
  } finally {
    saving.value = false
  }
}

async function remove() {
  const item = selected.value
  if (!item) return
  const ok = await toast.confirm(t('credDeleteConfirm', { name: item.name }))
  if (!ok) return
  try {
    await DeleteCredential(item.id)
    toast.success(t('credDeleted'))
    selectedId.value = ''
    await load()
    await loadStatus()
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credDeleteFailed'))
    logErr('CredentialsPage.delete', e)
  }
}

async function toggleReveal() {
  const item = selected.value
  if (!item) return
  if (showSecret.value) {
    showSecret.value = false
    revealedSecret.value = ''
    return
  }
  if (!item.hasSecret) return
  try {
    const data = unwrap<{ id: string; username: string; secret: string }>(
      await RevealCredential(item.id))
    revealedSecret.value = data?.secret ?? ''
    form.secret = data?.secret ?? ''
    showSecret.value = true
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credRevealFailed'))
    logErr('CredentialsPage.reveal', e)
  }
}

async function copySecret() {
  const item = selected.value
  if (!item) return
  try {
    const res = await CopyCredentialSecret(item.id)
    const data = unwrap<{ clearedInSec: number }>(res)
    toast.success(t('credCopied', { sec: data?.clearedInSec ?? 30 }))
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credCopyFailed'))
    logErr('CredentialsPage.copy', e)
  }
}

async function genPassword() {
  try {
    const data = unwrap<{ password: string }>(await GeneratePassword(20, true, true, true))
    if (data?.password) {
      form.secret = data.password
      revealedSecret.value = data.password
      showSecret.value = true
    }
  } catch (e) {
    logErr('CredentialsPage.generate', e)
  }
}

async function toggleExtension() {
  if (extBusy.value) return
  if (status.value?.extensionEnabled) {
    const ok = await toast.confirm(t('credExtDisableConfirm'))
    if (!ok) return
    extBusy.value = true
    try {
      await DisableExtension()
      await loadStatus()
      toast.success(t('credExtDisabled'))
    } catch (e) {
      toast.error(getErrorMessage(e) || t('credExtEnableFailed'))
      logErr('CredentialsPage.extOff', e)
    } finally {
      extBusy.value = false
    }
    return
  }
  extBusy.value = true
  try {
    await EnableExtension()
    await loadStatus()
    toast.success(t('credExtEnabled'))
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credExtEnableFailed'))
    logErr('CredentialsPage.extOn', e)
  } finally {
    extBusy.value = false
  }
}

/** 复制连接密钥：由后端写剪贴板，避免密钥被记进剪贴板历史。 */
async function copyExtKey() {
  try {
    await CopyExtensionKey()
    toast.success(t('credExtCopied'))
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credExtEnableFailed'))
    logErr('CredentialsPage.extCopy', e)
  }
}

onMounted(async () => {
  await Promise.all([load(), loadStatus()])
})
</script>

<template>
  <div class="cred-page">
    <div class="qd-page-header">
      <div class="qd-page-titles">
        <h2 class="qd-page-title">{{ t('navCredentials') }}</h2>
        <span class="qd-page-sub">{{ t('credSubtitle', { n: status?.count ?? 0 }) }}</span>
      </div>
      <div class="qd-page-actions">
        <button
          class="cred-switch"
          :class="{ 'is-on': status?.extensionEnabled }"
          type="button"
          role="switch"
          :aria-checked="!!status?.extensionEnabled"
          :disabled="extBusy"
          @click="toggleExtension"
        >
          <Globe :size="14" />
          {{ t('credExtTitle') }}
          <span class="cred-switch-dot"></span>
        </button>
        <button class="qd-btn" @click="load()"><RefreshCw :size="14" /></button>
        <button class="qd-btn qd-btn-primary" @click="createNew">
          <Plus :size="14" /> {{ t('credNew') }}
        </button>
      </div>
    </div>

    <!-- 开启状态：常驻一条说明条。密钥只在用户点「复制」时由后端写剪贴板，
         页面上不显示明文——它等价于密码库的读取权限，没必要挂在屏幕上。 -->
    <div v-if="status?.extensionEnabled" class="cred-ext">
      <div class="cred-ext-row">
        <span class="cred-ext-label">{{ t('credExtPort') }}</span>
        <code class="cred-ext-code">{{ status.extensionPort }}</code>
        <button class="qd-btn qd-btn-sm" @click="copyExtKey">
          <Copy :size="13" /> {{ t('credExtCopyKey') }}
        </button>
        <span class="cred-ext-badge">{{ t('credExtOn') }}</span>
      </div>
      <p class="cred-ext-hint">{{ t('credExtKeyHint') }}</p>
    </div>

    <div v-if="status && !status.platformProtected" class="cred-warn">
      {{ t('credPlatformWarn') }}
    </div>

    <div class="cred-body">
      <aside class="cred-list">
        <div class="cred-search">
          <input v-model="query" class="qd-input" :placeholder="t('credSearchPlaceholder')" />
        </div>
        <div class="cred-items">
          <button
            v-for="item in filtered"
            :key="item.id"
            class="cred-item"
            :class="{ 'is-active': item.id === selectedId }"
            type="button"
            @click="selectItem(item)"
          >
            <KeyRound :size="13" class="cred-item-icon" />
            <span class="cred-item-main">
              <span class="cred-item-name">{{ item.name }}</span>
              <span class="cred-item-sub">{{ item.username || item.host || kindLabel(item.kind) }}</span>
            </span>
          </button>
          <p v-if="!loading && filtered.length === 0" class="cred-empty">
            {{ query ? t('credNoResult') : t('credEmpty') }}
          </p>
        </div>
      </aside>

      <section class="cred-detail">
        <div class="cred-grid">
          <label class="cred-field">
            <span class="cred-label">{{ t('credName') }}</span>
            <input v-model="form.name" class="qd-input" :placeholder="t('credNamePlaceholder')" />
          </label>
          <label class="cred-field">
            <span class="cred-label">{{ t('credKind') }}</span>
            <select v-model="form.kind" class="qd-input">
              <option v-for="o in kindOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
          </label>
        </div>

        <div class="cred-grid">
          <label class="cred-field">
            <span class="cred-label">{{ t('credUsername') }}</span>
            <input v-model="form.username" class="qd-input" autocomplete="off" spellcheck="false" />
          </label>
          <label class="cred-field">
            <span class="cred-label">{{ t('credPassword') }}</span>
            <div class="cred-pw-row">
              <input
                v-model="form.secret"
                class="qd-input cred-pw"
                :type="showSecret ? 'text' : 'password'"
                autocomplete="off"
                spellcheck="false"
                :placeholder="selected?.hasSecret ? t('credPasswordKeep') : ''"
              />
              <button class="qd-icon-btn" type="button" :title="t('credShow')" @click="toggleReveal">
                <EyeOff v-if="showSecret" :size="15" />
                <Eye v-else :size="15" />
              </button>
              <button class="qd-icon-btn" type="button" :title="t('credGenerate')" @click="genPassword">
                <Wand2 :size="15" />
              </button>
              <button
                class="qd-icon-btn"
                type="button"
                :title="t('credCopy')"
                :disabled="!selected?.hasSecret"
                @click="copySecret"
              >
                <Copy :size="15" />
              </button>
            </div>
          </label>
        </div>

        <div class="cred-grid">
          <label class="cred-field">
            <span class="cred-label">{{ t('credHost') }}</span>
            <input v-model="form.host" class="qd-input" placeholder="example.com" spellcheck="false" />
          </label>
          <label class="cred-field">
            <span class="cred-label">{{ t('credPort') }}</span>
            <input v-model.number="form.port" class="qd-input" type="number" min="0" max="65535" />
          </label>
        </div>

        <label class="cred-field">
          <span class="cred-label">{{ t('credUrl') }}</span>
          <input v-model="form.url" class="qd-input" placeholder="https://" spellcheck="false" />
        </label>

        <label class="cred-field">
          <span class="cred-label">{{ t('credTags') }}</span>
          <input v-model="form.tags" class="qd-input" :placeholder="t('credTagsPlaceholder')" />
        </label>

        <label class="cred-field">
          <span class="cred-label">{{ t('credNotes') }}</span>
          <textarea v-model="form.notes" class="qd-input cred-textarea" rows="3"></textarea>
        </label>

        <label class="cred-check">
          <input v-model="form.favorite" type="checkbox" />
          <span>{{ t('credFavorite') }}</span>
        </label>
        <label v-if="selected?.hasSecret" class="cred-check">
          <input v-model="form.clearSecret" type="checkbox" />
          <span>{{ t('credClearSecret') }}</span>
        </label>

        <div class="cred-actions">
          <button v-if="selected" class="qd-btn qd-btn-danger" @click="remove">
            <Trash2 :size="14" /> {{ t('delete') }}
          </button>
          <span class="cred-spacer"></span>
          <button class="qd-btn qd-btn-primary" :disabled="saving" @click="save">
            <Check :size="14" /> {{ t('save') }}
          </button>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.cred-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

/* 开关：按钮形态的 role="switch"。
   不用 input[type=checkbox] 是为了让「圆点 + 文案」一体，避免和页面里
   真正的表单勾选框（表单区那几个）长得一样却用途不同。 */
.cred-switch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.cred-switch:hover {
  color: var(--color-text-primary);
  border-color: var(--color-text-muted);
}

.cred-switch-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-text-muted);
}

.cred-switch.is-on {
  color: var(--color-accent);
  border-color: var(--color-accent);
  background: var(--color-accent-bg);
}

.cred-switch.is-on .cred-switch-dot {
  background: var(--color-accent);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 18%, transparent);
}

.cred-ext {
  flex-shrink: 0;
  padding: 10px 16px;
  background: var(--color-accent-bg);
  border-bottom: 1px solid var(--color-border);
}

.cred-ext-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.cred-ext-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.cred-ext-code {
  padding: 2px 8px;
  font-family: ui-monospace, Consolas, monospace;
  font-size: var(--text-sm);
  color: var(--color-accent);
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-sm);
}

.cred-ext-badge {
  margin-left: auto;
  font-size: var(--text-xs);
  color: var(--color-accent);
}

.cred-ext-hint {
  margin: 6px 0 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.cred-warn {
  flex-shrink: 0;
  padding: 8px 16px;
  font-size: var(--text-xs);
  color: var(--color-warning);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.cred-body {
  display: flex;
  flex: 1;
  min-height: 0;
}

.cred-list {
  display: flex;
  flex-direction: column;
  width: 260px;
  flex-shrink: 0;
  border-right: 1px solid var(--color-border);
}

.cred-search {
  padding: 10px 10px 6px;
}

.cred-search .qd-input {
  width: 100%;
}

.cred-items {
  flex: 1;
  overflow-y: auto;
  padding: 0 6px 10px;
}

.cred-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 8px;
  background: none;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--color-text-secondary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}

.cred-item:hover { background: var(--color-bg-hover); }

.cred-item.is-active {
  background: var(--color-accent-bg);
  color: var(--color-accent);
}

.cred-item-icon { flex-shrink: 0; opacity: 0.7; }

.cred-item-main {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.cred-item-name {
  font-size: var(--text-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cred-item-sub {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cred-empty {
  margin: 12px 6px;
  font-size: var(--text-xs);
  color: var(--color-text-disabled);
  line-height: 1.6;
}

.cred-detail {
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  padding: 14px 18px 20px;
}

.cred-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}

.cred-field {
  display: block;
  margin-bottom: 10px;
}

.cred-label {
  display: block;
  margin-bottom: 4px;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.cred-field .qd-input {
  width: 100%;
}

.cred-pw-row {
  display: flex;
  align-items: center;
  gap: 4px;
}

.cred-pw { flex: 1; min-width: 0; }

.cred-textarea {
  height: auto;
  padding: 6px 9px;
  resize: vertical;
  line-height: 1.6;
}

.cred-check {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin: 0 14px 8px 0;
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  cursor: pointer;
}

.cred-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--color-border);
}

.cred-spacer { flex: 1; }
</style>
