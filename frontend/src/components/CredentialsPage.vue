<script setup lang="ts">
import PageHeader from './PageHeader.vue'
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
  Check, Copy, Download, Eye, EyeOff, Globe, KeyRound, Plus, RefreshCw, Trash2, Upload, Wand2,
} from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import type { CredentialSummary } from '../composables/useCredential'
import {
  CopyCredentialSecret, CopyExtensionKey, DeleteCredential, DisableExtension, EnableExtension,
  ExportCredentials, GeneratePassword, ImportCredentials, ListCredentials,
  RevealCredential, SaveCredential, Status,
} from '../../bindings/quickdock/services/credentials/credentialsservice'
import { SelectLocalFile, SelectSavePath } from '../../bindings/quickdock/services/appservice'

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
// 列表请求的持久失败态：catch 里只 toast 会让界面退回「共 0 条 + 还没有保存任何密码」，
// 把真实错误伪装成空库。失败后计数显示「—」，列表区给「加载失败 + 重试」。
const loadFailed = ref(false)
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
// 新建态没有选中条目，不能走 Reveal；眼睛只切本地密码框的明文。
const showCreatePwd = ref(false)
const revealedSecret = ref('')

/** 浏览器集成：一次开关，长期有效。extBusy 防连点（开关是幂等接口，但连点会闪）。 */
const extBusy = ref(false)

/** 导出/导入面板。口令只在内存里待到这次调用结束，不进 ref 之外的任何地方。 */
const transferMode = ref<'export' | 'import' | ''>('')
const transferPassword = ref('')
const transferBusy = ref(false)
const transferResult = ref('')

interface TransferReport {
  total: number
  created: number
  updated: number
  skipped: number
  failed: number
  errors?: string[]
}

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
  showCreatePwd.value = false
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
  loadFailed.value = false
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
    loadFailed.value = true
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
  showCreatePwd.value = false
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
      // 点过「显示密码」后 form.secret 里是明文；此时若又勾了「清除密码」，
      // 两边同时传会让后端先清空、再被非空 secret 覆盖回来。以勾选项为准。
      secret: form.clearSecret ? '' : form.secret,
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
    unwrap(await DeleteCredential(item.id))
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
  // 新建态：没有可 Reveal 的条目，眼睛只切本地密码框明文。
  if (!item) { showCreatePwd.value = !showCreatePwd.value; return }
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
      showCreatePwd.value = true
    }
  } catch (e) {
    toast.error(t('credGenerateFailed'))
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
      unwrap(await DisableExtension())
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
    unwrap(await EnableExtension())
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
    unwrap(await CopyExtensionKey())
    toast.success(t('credExtCopied'))
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credExtEnableFailed'))
    logErr('CredentialsPage.extCopy', e)
  }
}

/** 展开/收起面板。切换模式时清空口令，免得导出口令被顺手带进导入框。 */
function openTransfer(mode: 'export' | 'import') {
  transferMode.value = transferMode.value === mode ? '' : mode
  transferPassword.value = ''
  transferResult.value = ''
}

function closeTransfer() {
  transferMode.value = ''
  transferPassword.value = ''
  transferResult.value = ''
}

/**
 * 导出 / 导入。
 *
 * 文件对话框走 AppService 的宿主能力（SelectSavePath / SelectLocalFile），
 * 拿到绝对路径后交给后端读写：WebView2 里 <input type="file"> 只有伪造路径
 * 和内存副本，拿不到真实文件（见 services/dialog.go）。
 */
async function runTransfer() {
  if (transferBusy.value || !transferMode.value) return
  const mode = transferMode.value
  if (mode === 'export' && transferPassword.value.length < 8) {
    toast.error(t('credTransferPwShort'))
    return
  }
  // 导入会覆盖同站点同账号的已有条目，先问一次，别让用户事后才发现旧密码没了。
  if (mode === 'import') {
    const ok = await toast.confirm(t('credTransferImportConfirm'))
    if (!ok) return
  }
  transferBusy.value = true
  transferResult.value = ''
  try {
    if (mode === 'export') {
      const day = new Date().toISOString().slice(0, 10)
      const dest = (unwrap<string>(
        await SelectSavePath(t('credTransferSaveTitle'), `quickdock-vault-${day}.qdvault`)) ?? '').trim()
      if (!dest) return // 用户在「另存为」点了取消
      const data = unwrap<{ count: number; bytes: number }>(
        await ExportCredentials(dest, transferPassword.value))
      transferResult.value = t('credTransferExported', { n: data?.count ?? 0, size: data?.bytes ?? 0 })
      toast.success(t('credTransferExportedShort'))
      await loadStatus()
    } else {
      const src = (unwrap<string>(await SelectLocalFile(t('credTransferOpenTitle'))) ?? '').trim()
      if (!src) return
      const rep = unwrap<TransferReport>(await ImportCredentials(src, transferPassword.value))
      if (!rep) throw new Error(t('credTransferFailed'))
      transferResult.value = t('credTransferImported', {
        total: rep.total, created: rep.created, updated: rep.updated,
        skipped: rep.skipped, failed: rep.failed,
      })
      if (rep.errors?.length) transferResult.value += ` ${rep.errors.join('; ')}`
      toast.success(t('credTransferImportedShort'))
      await load()
      await loadStatus()
    }
    transferPassword.value = ''
  } catch (e) {
    toast.error(getErrorMessage(e) || t('credTransferFailed'))
    logErr('CredentialsPage.transfer', e)
  } finally {
    transferBusy.value = false
  }
}

onMounted(async () => {
  await Promise.all([load(), loadStatus()])
})
</script>

<template>
  <div class="cred-page">
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title">{{ t('navCredentials') }}</h2>
        <span class="qd-page-sub">{{ t('credSubtitle', { n: loadFailed || !status ? '—' : status.count }) }}</span>
      </template>
      <!-- 页头动作按 ui.md §3.1：一组次要动作（ghost）+ 1 个主按钮，
           中间一条短竖线。原来 5 个同级按钮平铺，「新建」和「刷新」一样重。 -->
      <div class="qd-page-actions">
        <div class="qd-action-group">
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
          <button class="qd-btn qd-btn-ghost" @click="load()"><RefreshCw :size="14" /></button>
          <button class="qd-btn qd-btn-ghost" :disabled="transferBusy" @click="openTransfer('export')">
            <Download :size="14" /> {{ t('credTransferExport') }}
          </button>
          <button class="qd-btn qd-btn-ghost" :disabled="transferBusy" @click="openTransfer('import')">
            <Upload :size="14" /> {{ t('credTransferImport') }}
          </button>
        </div>
        <span class="qd-action-sep"></span>
        <button class="qd-btn qd-btn-primary" @click="createNew">
          <Plus :size="14" /> {{ t('credNew') }}
        </button>
      </div>
    </PageHeader>

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

    <!-- 导出/导入：内联面板而不是第二个弹窗，输入与按钮沿用本页既有类。 -->
    <div v-if="transferMode" class="cred-transfer">
      <p class="cred-transfer-desc">
        {{ transferMode === 'export' ? t('credTransferExportDesc') : t('credTransferImportDesc') }}
      </p>
      <div class="cred-ext-row">
        <span class="cred-ext-label">{{ t('credTransferPassword') }}</span>
        <input
          v-model="transferPassword"
          type="password"
          class="qd-input cred-transfer-pw"
          autocomplete="new-password"
          spellcheck="false"
          :placeholder="t('credTransferPasswordHint')"
          @keydown.enter.stop="runTransfer"
        />
        <button class="qd-btn qd-btn-primary qd-btn-sm" :disabled="transferBusy" @click="runTransfer">
          {{ transferMode === 'export' ? t('credTransferRunExport') : t('credTransferRunImport') }}
        </button>
        <button class="qd-btn qd-btn-sm" @click="closeTransfer">{{ t('cancel') }}</button>
      </div>
      <p v-if="transferResult" class="cred-ext-hint cred-transfer-result">{{ transferResult }}</p>
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
          <div v-if="loadFailed" class="cred-load-failed">
            <span>{{ t('credLoadFailed') }}</span>
            <button class="qd-btn qd-btn-sm" @click="load()">{{ t('retry') }}</button>
          </div>
          <div v-else-if="loading && !list.length" class="cred-skeleton-wrap">
            <div v-for="n in 4" :key="n" class="cred-skeleton"></div>
          </div>
          <template v-else>
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
            <div v-if="!loading && filtered.length === 0" class="cred-empty">
              <KeyRound :size="22" />
              <p class="cred-empty-title">{{ query ? t('credNoResult') : t('credEmpty') }}</p>
              <button v-if="!query" class="qd-btn qd-btn-sm" @click="createNew">
                <Plus :size="13" /> {{ t('credNew') }}
              </button>
            </div>
          </template>
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
                :type="(selected ? showSecret : showCreatePwd) ? 'text' : 'password'"
                autocomplete="off"
                spellcheck="false"
                :placeholder="selected?.hasSecret ? t('credPasswordKeep') : ''"
              />
              <button class="qd-icon-btn" type="button" :title="t('credShow')" @click="toggleReveal">
                <EyeOff v-if="(selected ? showSecret : showCreatePwd)" :size="15" />
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
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
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
  box-shadow: 0 0 0 3px var(--color-accent-border);
}

.cred-ext {
  flex-shrink: 0;
  padding: var(--space-5) var(--space-6);
  background: var(--color-accent-bg);
  border-bottom: 1px solid var(--color-border);
}

.cred-ext-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-4);
}

.cred-ext-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.cred-ext-code {
  padding: var(--space-1) var(--space-4);
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
  margin: var(--space-3) 0 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

/* 导出/导入面板：与浏览器集成条同构，但用中性底色区分它是一次性操作。 */
.cred-transfer {
  flex-shrink: 0;
  padding: var(--space-5) var(--space-6);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.cred-transfer-desc {
  margin: 0 0 var(--space-4);
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.cred-transfer-pw {
  width: 220px;
  max-width: 100%;
  flex: 0 1 220px;
  min-width: 120px;
}

.cred-transfer-result {
  color: var(--color-accent);
  word-break: break-all;
}

.cred-warn {
  flex-shrink: 0;
  padding: var(--space-4) var(--space-6);
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
  padding: var(--space-5) var(--space-5) var(--space-3);
}

.cred-search .qd-input {
  width: 100%;
}

.cred-items {
  flex: 1;
  overflow-y: auto;
  padding: 0 var(--space-3) var(--space-4);
}

.cred-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: var(--space-4);
  width: 100%;
  padding: var(--space-4);
  background: none;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--color-text-secondary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}

.cred-item:hover { background: var(--color-bg-hover); }

/* 选中态全页唯一画法：select-bg 实底 + 3px accent 竖条，文字/图标不变色。 */
.cred-item.is-active { background: var(--color-select-bg); }

/* 竖条与全局 .qd-row.active 同一画法：3px、居中 16px 高。 */
.cred-item.is-active::before {
  content: '';
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 16px;
  border-radius: var(--radius-full);
  background: var(--color-accent);
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
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-3);
  margin: var(--space-5) var(--space-3);
  color: var(--color-text-muted);
  text-align: center;
}

.cred-empty-title {
  margin: 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  line-height: 1.6;
}

/* 加载失败：常驻错误态替代「共 0 条」，重试按钮就地 recover。 */
.cred-load-failed {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin: var(--space-5) var(--space-3);
  font-size: var(--text-xs);
  color: var(--color-danger);
}

/* 首次加载的空窗期：空态被 !loading 挡住，给几条骨架免得整块空白。 */
.cred-skeleton-wrap {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-3);
}
.cred-skeleton {
  height: 38px;
  border-radius: var(--radius-sm);
  background: linear-gradient(90deg, var(--color-bg-tertiary), var(--color-bg-hover), var(--color-bg-tertiary));
  background-size: 200% 100%;
  animation: cred-shimmer 1.4s ease-in-out infinite;
}
@keyframes cred-shimmer {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

.cred-detail {
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  padding: var(--space-6) var(--space-7) var(--space-7);
}

.cred-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-4);
}

.cred-field {
  display: block;
  margin-bottom: var(--space-4);
}

.cred-label {
  display: block;
  margin-bottom: var(--space-2);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.cred-field .qd-input {
  width: 100%;
}

.cred-pw-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.cred-pw { flex: 1; min-width: 0; }

.cred-textarea {
  height: auto;
  padding: var(--space-3) var(--space-4);
  resize: vertical;
  line-height: 1.6;
}

.cred-check {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  margin: 0 var(--space-6) var(--space-4) 0;
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  cursor: pointer;
}

.cred-actions {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-top: var(--space-4);
  padding-top: var(--space-5);
  border-top: 1px solid var(--color-border);
}

.cred-spacer { flex: 1; }

/* 窄窗：扩展条与导入导出面板原来都是单行 flex + 定宽输入，控件被挤出可视区。
   允许整行换行，口令框独占一行自适应宽度。 */
@media (max-width: 760px) {
  .cred-ext-row { gap: var(--space-2); }
  .cred-transfer-pw { flex: 1 1 100%; width: auto; min-width: 0; }
  .cred-ext-badge { margin-left: 0; }
}
</style>
