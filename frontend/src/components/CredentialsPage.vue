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
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Braces, Check, Cloud, Copy, Database, Download, Eye, EyeOff, GitBranch, Globe, KeyRound,
  LockKeyhole, MoreHorizontal, Plus, RefreshCw, Shield, Terminal, Trash2, Upload, Wand2,
} from '@lucide/vue'
import { unwrap } from '../utils/api'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import type { CredentialSummary } from '../composables/useCredential'
import {
  CopyCredentialSecret, CopyExtensionKey, DeleteCredential, DisableExtension, EnableExtension,
  ExportCredentials, GeneratePassword, ImportCredentials, ListCredentials,
  RevealCredential, SaveCredential, Status,
} from '../../bindings/quickdock/services/credentials/credentialsservice'
import { LockStatus, SelectLocalFile, SelectSavePath } from '../../bindings/quickdock/services/appservice'

import { reportError } from '../utils/reportError'
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

/** 列表是否明文显示账号：默认打码，选择记在 localStorage（不涉及解密边界）。 */
const showUser = ref(localStorage.getItem('quickdock.credentials.showUser') === '1')
/** 页头「更多」菜单（导入 / 导出）。 */
const moreOpen = ref(false)
/** 离开自动锁定的分钟数；0 = 未开启。只做展示，真正计时在后端。 */
const autoLockMin = ref(0)

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

/**
 * 每行图标按凭据类型分：登录页用地球、SSH 用终端、数据库用库……
 * 长列表里清一色的钥匙图标等于没有图标，还白占横向空间。
 * 不抓 favicon：那要发外部请求（泄漏「用户存了哪些站」），也要额外的磁盘缓存。
 */
const KIND_ICONS = {
  login: Globe,
  ssh: Terminal,
  database: Database,
  webdav: Cloud,
  git: GitBranch,
  api_key: Braces,
  other: KeyRound,
} as const

function kindIcon(kind: string) {
  return (KIND_ICONS as Record<string, unknown>)[kind] ?? KeyRound
}

/** 打码规则：邮箱保留首字母与域名，其余保留前两位。 */
function maskUser(value: string): string {
  const s = (value || '').trim()
  if (!s) return ''
  const at = s.indexOf('@')
  if (at > 0) return `${s.slice(0, 1)}•••${s.slice(at)}`
  if (s.length <= 2) return '••'
  return `${s.slice(0, 2)}•••`
}

function toggleShowUser() {
  showUser.value = !showUser.value
  try {
    localStorage.setItem('quickdock.credentials.showUser', showUser.value ? '1' : '0')
  } catch {
    // 隐私模式下 localStorage 可能不可写：本次会话仍然生效，不打扰用户。
  }
}

/**
 * 列表副行：账号（默认打码）@主机:端口。
 *
 * 原来只显示账号，SSH / 数据库 / WebDAV 这类条目一眼看不出是哪台机器 ——
 * 恰恰是地址而不是账号在区分它们。80/443 是默认端口，写出来只是噪音，
 * 22 / 5432 / 8080 这类才是有效信息。两者都没有时退回类型名。
 */
function credSubtitle(item: CredentialSummary): string {
  const user = item.username ? (showUser.value ? item.username : maskUser(item.username)) : ''
  const port = Number(item.port) || 0
  const withPort = port > 0 && port !== 80 && port !== 443
  const addr = item.host ? `${item.host}${withPort ? `:${port}` : ''}` : ''
  if (user && addr) return `${user}@${addr}`
  return user || addr || kindLabel(item.kind)
}

/** 点空白处收起「更多」菜单（菜单自身用了 @click.stop 开合）。 */
function onDocClick(e: MouseEvent) {
  if (!moreOpen.value) return
  const el = e.target as HTMLElement | null
  if (el?.closest('.cred-more-wrap')) return
  moreOpen.value = false
}

async function loadAutoLock() {
  try {
    const st = unwrap<{ autoLockMinutes?: number }>(await LockStatus())
    autoLockMin.value = st?.autoLockMinutes ?? 0
  } catch (e) {
    logErr('CredentialsPage.autoLock', e)
  }
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
    reportError(e, 'CredentialsPage.load', { fallback: t('credLoadFailed') })
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
    reportError(e, 'CredentialsPage.save', { fallback: t('credSaveFailed') })
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
    reportError(e, 'CredentialsPage.delete', { fallback: t('credDeleteFailed') })
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
    reportError(e, 'CredentialsPage.reveal', { fallback: t('credRevealFailed') })
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
    reportError(e, 'CredentialsPage.copy', { fallback: t('credCopyFailed') })
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
      reportError(e, 'CredentialsPage.extOff', { fallback: t('credExtEnableFailed') })
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
    reportError(e, 'CredentialsPage.extOn', { fallback: t('credExtEnableFailed') })
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
    reportError(e, 'CredentialsPage.extCopy', { fallback: t('credExtEnableFailed') })
  }
}

/** 展开/收起面板。切换模式时清空口令，免得导出口令被顺手带进导入框。 */
function openTransfer(mode: 'export' | 'import') {
  moreOpen.value = false
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
    reportError(e, 'CredentialsPage.transfer', { fallback: t('credTransferFailed') })
  } finally {
    transferBusy.value = false
  }
}

onMounted(async () => {
  document.addEventListener('click', onDocClick)
  await Promise.all([load(), loadStatus(), loadAutoLock()])
})

onUnmounted(() => {
  document.removeEventListener('click', onDocClick)
})
</script>

<template>
  <div class="cred-page">
    <PageHeader>
      <template #titles>
        <h2 class="qd-page-title">{{ t('navCredentials') }}</h2>
        <!-- 副标题拆成三段：条数（可扫读）+ 隐私 badge（信任感）+ 自动锁定状态。
             原来一整句「共 16 条，只存本机，不随备份同步」挤在标题旁，既看不出
             哪个是数量，也把最该被记住的隐私承诺埋进了灰字里。 -->
        <span class="cred-meta">
          <span class="cred-count">{{ t('credCount', { n: loadFailed || !status ? '—' : status.count }) }}</span>
          <span class="cred-privacy" :title="t('credPrivacyHint')">
            <Shield :size="11" />{{ t('credPrivacy') }}
          </span>
          <span v-if="autoLockMin" class="cred-autolock" :title="t('credAutoLockHint')">
            <LockKeyhole :size="11" />{{ t('credAutoLockOn', { n: autoLockMin }) }}
          </span>
        </span>
      </template>
      <!-- 页头动作按 ui.md §3.1：一组次要动作（ghost）+ 1 个主按钮。
           导入/导出是低频操作，收进 ⋯ 里；「新建」是这页唯一的主动作，给足尺寸。 -->
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
          <button class="qd-icon-btn" type="button" :title="t('refresh')" @click="load()">
            <RefreshCw :size="15" />
          </button>
          <div class="cred-more-wrap">
            <button
              class="qd-icon-btn"
              type="button"
              :title="t('credMore')"
              :aria-expanded="moreOpen"
              @click.stop="moreOpen = !moreOpen"
            >
              <MoreHorizontal :size="16" />
            </button>
            <div v-if="moreOpen" class="cred-more" role="menu">
              <button
                class="cred-more-item"
                role="menuitem"
                :disabled="transferBusy"
                @click="openTransfer('export')"
              >
                <Download :size="13" />{{ t('credTransferExport') }}
              </button>
              <button
                class="cred-more-item"
                role="menuitem"
                :disabled="transferBusy"
                @click="openTransfer('import')"
              >
                <Upload :size="13" />{{ t('credTransferImport') }}
              </button>
            </div>
          </div>
        </div>
        <span class="qd-action-sep"></span>
        <button class="qd-btn qd-btn-primary cred-new-btn" @click="createNew">
          <Plus :size="16" />{{ t('credNew') }}
        </button>
      </div>
    </PageHeader>

    <!-- 开启状态：常驻一条说明条。密钥只在用户点「复制」时由后端写剪贴板，
         页面上不显示明文——它等价于密码库的读取权限，没必要挂在屏幕上。 -->
    <div v-if="status?.extensionEnabled" class="cred-ext">
      <div class="cred-ext-row">
        <span class="cred-live">
          <i class="cred-live-dot"></i>
          {{ t('credExtListening', { port: status.extensionPort }) }}
        </span>
        <button class="qd-btn qd-btn-sm" @click="copyExtKey">
          <Copy :size="13" /> {{ t('credExtCopyKey') }}
        </button>
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
          <!-- 列表默认给用户名打码：密码库最先暴露在肩窥里的就是「账号叫 admin」。
               想看全文时点这个眼睛，选择记在 localStorage，不影响安全边界。 -->
          <button
            class="qd-icon-btn cred-eye"
            type="button"
            :title="showUser ? t('credHideUser') : t('credShowUser')"
            @click="toggleShowUser"
          >
            <EyeOff v-if="showUser" :size="14" />
            <Eye v-else :size="14" />
          </button>
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
              <!-- 图标按类型区分：长列表里一排同样的钥匙等于没有图标。 -->
              <component :is="kindIcon(item.kind)" :size="14" class="cred-item-icon" />
              <span class="cred-item-main">
                <span class="cred-item-name">{{ item.name || item.host || kindLabel(item.kind) }}</span>
                <span class="cred-item-sub" :title="credSubtitle(item)">{{ credSubtitle(item) }}</span>
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
        <div class="cred-form">
          <div class="cred-row">
            <label class="cred-field">
              <span class="cred-label">{{ t('credName') }}</span>
              <input v-model="form.name" class="qd-input" :placeholder="t('credNamePlaceholder')" />
            </label>
            <label class="cred-field cred-field-kind">
              <span class="cred-label">{{ t('credKind') }}</span>
              <select v-model="form.kind" class="qd-input">
                <option v-for="o in kindOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
              </select>
            </label>
          </div>

          <!-- 主机与端口是同一个地址的两半：并排 + 冒号连接，比上下两个输入框更贴直觉。 -->
          <div class="cred-field">
            <span class="cred-label">{{ t('credHost') }} / {{ t('credPort') }}</span>
            <div class="cred-compound">
              <input
                v-model="form.host"
                class="qd-input cred-compound-host"
                placeholder="example.com"
                spellcheck="false"
              />
              <span class="cred-compound-sep">:</span>
              <input
                v-model.number="form.port"
                class="qd-input cred-compound-port"
                type="number"
                min="0"
                max="65535"
                :title="t('credPort')"
              />
            </div>
          </div>

          <div class="cred-row cred-row-url">
            <label class="cred-field">
              <span class="cred-label">{{ t('credUrl') }}</span>
              <input v-model="form.url" class="qd-input" placeholder="https://" spellcheck="false" />
            </label>
            <label class="cred-field">
              <span class="cred-label">{{ t('credUsername') }}</span>
              <input v-model="form.username" class="qd-input" autocomplete="off" spellcheck="false" />
            </label>
          </div>

          <div class="cred-field">
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
              <button class="cred-tool" type="button" :title="t('credShow')" @click="toggleReveal">
                <EyeOff v-if="(selected ? showSecret : showCreatePwd)" :size="16" />
                <Eye v-else :size="16" />
              </button>
              <button
                class="cred-tool"
                type="button"
                :title="t('credCopy')"
                :disabled="!selected?.hasSecret"
                @click="copySecret"
              >
                <Copy :size="16" />
              </button>
              <button class="qd-btn cred-gen" type="button" @click="genPassword">
                <Wand2 :size="13" />{{ t('credGenerate') }}
              </button>
            </div>
            <p v-if="selected?.hasSecret" class="cred-hint">{{ t('credPasswordKeepTip') }}</p>
          </div>

          <label class="cred-field">
            <span class="cred-label">{{ t('credTags') }}</span>
            <input v-model="form.tags" class="qd-input" :placeholder="t('credTagsPlaceholder')" />
          </label>

          <label class="cred-field">
            <span class="cred-label">{{ t('credNotes') }}</span>
            <textarea v-model="form.notes" class="qd-input cred-textarea" rows="3"></textarea>
          </label>

          <div class="cred-checks">
            <label class="cred-check">
              <input v-model="form.favorite" type="checkbox" />
              <span>{{ t('credFavorite') }}</span>
            </label>
            <label v-if="selected?.hasSecret" class="cred-check">
              <input v-model="form.clearSecret" type="checkbox" />
              <span>{{ t('credClearSecret') }}</span>
            </label>
          </div>
        </div>

        <!-- 操作栏贴底常驻：表单再长也不必滚回去点保存。
             删除降级为红色幽灵按钮，视觉权重让位给保存，也降低盲操误触。 -->
        <div class="cred-actions">
          <button v-if="selected" class="qd-btn cred-del" @click="remove">
            <Trash2 :size="14" /> {{ t('delete') }}
          </button>
          <span class="cred-spacer"></span>
          <button class="qd-btn qd-btn-primary cred-save" :disabled="saving" @click="save">
            <Check :size="15" /> {{ t('save') }}
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

/* ---- 页头：条数 + 隐私 badge + 自动锁定 ---- */
.cred-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-3);
  min-width: 0;
}

.cred-count {
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

.cred-privacy,
.cred-autolock {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 19px;
  padding: 0 var(--space-4);
  font-size: var(--text-2xs);
  line-height: 1;
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  white-space: nowrap;
}

.cred-privacy svg,
.cred-autolock svg {
  flex-shrink: 0;
  opacity: 0.85;
}

.cred-autolock {
  color: var(--color-text-secondary);
}

/* ---- 浏览器集成开关 ---- */
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
  border-color: var(--color-accent-border-strong);
  background: var(--color-accent-bg);
}

.cred-switch.is-on .cred-switch-dot {
  background: var(--color-success);
  box-shadow: 0 0 0 3px var(--color-success-bg);
}

.cred-new-btn {
  height: 30px;
  padding: 0 var(--space-6);
  font-weight: 500;
}

/* ---- 更多菜单 ---- */
.cred-more-wrap {
  position: relative;
}

.cred-more {
  position: absolute;
  right: 0;
  top: calc(100% + 6px);
  z-index: 30;
  min-width: 152px;
  padding: var(--space-2);
  background: var(--color-bg-secondary);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-pop);
}

.cred-more-item {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  width: 100%;
  padding: var(--space-3) var(--space-4);
  font-family: inherit;
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
  text-align: left;
  background: none;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.cred-more-item:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.cred-more-item:disabled {
  color: var(--color-text-disabled);
  cursor: not-allowed;
}

/* ---- 集成状态条 ---- */
.cred-ext {
  flex-shrink: 0;
  padding: var(--space-4) var(--space-6);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.cred-ext-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-4);
}

.cred-live {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

/* 「服务在跑」这件事靠一个会呼吸的绿点表达，比一行「已开启」文字更快被看到。 */
.cred-live-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--color-success);
  box-shadow: 0 0 0 3px var(--color-success-bg);
  animation: cred-breathe 2.6s ease-in-out infinite;
}

@keyframes cred-breathe {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}

.cred-ext-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.cred-ext-hint {
  margin: var(--space-3) 0 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-muted);
}

/* 导出/导入面板：与集成条同构，但用主底色区分它是一次性操作。 */
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

/* ---- 列表 ---- */
.cred-body {
  display: flex;
  flex: 1;
  min-height: 0;
}

.cred-list {
  display: flex;
  flex-direction: column;
  width: 264px;
  flex-shrink: 0;
  border-right: 1px solid var(--color-border);
}

.cred-search {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-5) var(--space-5) var(--space-3);
}

.cred-search .qd-input {
  flex: 1;
  min-width: 0;
  height: 30px;
  background: var(--color-bg-tertiary);
  border-color: var(--color-border-light);
}

.cred-search .qd-input:focus {
  border-color: var(--color-accent);
}

.cred-eye {
  flex-shrink: 0;
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
  padding: 8px var(--space-4);
  background: none;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--color-text-secondary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  /* 未选中行之间用一条极细内阴影分界：比分割线轻，又比纯留白更容易扫读。 */
  box-shadow: inset 0 -1px 0 var(--color-border);
}

.cred-item:hover { background: var(--color-bg-hover); }

/* 选中态全页唯一画法：select-bg 实底 + 3px accent 竖条。 */
.cred-item.is-active {
  background: var(--color-select-bg);
  box-shadow: none;
}

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

.cred-item-icon {
  flex-shrink: 0;
  color: var(--color-text-muted);
}

.cred-item.is-active .cred-item-icon {
  color: var(--color-accent-ink);
}

.cred-item-main {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.cred-item-name {
  font-size: var(--text-sm);
  font-weight: 500;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cred-item-sub {
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 列表空态：图标落一个 44 方底（同各页空状态），文字是这一屏唯一的内容，
   别用 muted 小字——那是提示语的层级，不是标题的层级。 */
.cred-empty > svg {
  width: 44px;
  height: 44px;
  padding: 12px;
  box-sizing: border-box;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: inset 0 1px 0 var(--color-hairline);
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
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
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
  height: 40px;
  border-radius: var(--radius-sm);
  background: linear-gradient(90deg, var(--color-bg-tertiary), var(--color-bg-hover), var(--color-bg-tertiary));
  background-size: 200% 100%;
  animation: cred-shimmer 1.4s ease-in-out infinite;
}
@keyframes cred-shimmer {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

/* ---- 表单 ---- */
.cred-detail {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  padding: var(--space-6) var(--space-7) 0;
}

.cred-form {
  flex: 1;
  min-height: 0;
}

.cred-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 152px;
  gap: var(--space-4);
}

.cred-row-url {
  grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr);
}

.cred-field {
  display: block;
  margin-bottom: var(--space-5);
}

.cred-label {
  display: block;
  margin-bottom: 6px;
  font-size: var(--text-xs);
  font-weight: 500;
  color: var(--color-text-secondary);
}

/* 表单里的输入框要比全局「凹槽」更明确：底色提一档 + 亮一档的描边，
   否则深色下标签和输入区几乎同色，用户得先点一下才知道哪里能填。 */
.cred-form .qd-input {
  width: 100%;
  height: 32px;
  background: var(--color-bg-hover);
  border-color: var(--color-border-light);
}

.cred-form .qd-input:hover:not(:disabled) {
  border-color: var(--color-text-disabled);
}

.cred-form .qd-input:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-border);
}

.cred-form .qd-input:disabled {
  background: var(--color-bg-tertiary);
}

/* 主机 + 端口：一个合成控件，中间冒号，视觉上就是 host:port。
 *
 * 选择器必须带上 .cred-form：本页 .cred-form .qd-input 写了 width:100%，
 * 权重（0,2,0）高于单个类名，会把页面里每个输入框都拉成整行宽 ——
 * 端口的 92px 被顶成整行、可伸缩的主机框被挤到 0 宽（只剩一根光标），
 * 表现为「主机那一栏看不到地址」。三条都放在 .qd-input 规则之后，
 * 并各自显式写回 width，避免再被那条通用规则盖掉。 */
.cred-form .cred-compound {
  display: flex;
  align-items: stretch;
}

.cred-form .cred-compound-host {
  flex: 1 1 0;
  width: auto;
  min-width: 0;
  border-top-right-radius: 0;
  border-bottom-right-radius: 0;
}

.cred-form .cred-compound-sep {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  flex: 0 0 20px;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-top: 1px solid var(--color-border-light);
  border-bottom: 1px solid var(--color-border-light);
}

.cred-form .cred-compound-port {
  flex: 0 0 92px;
  width: 92px;
  border-left: none;
  border-top-left-radius: 0;
  border-bottom-left-radius: 0;
  text-align: center;
  font-variant-numeric: tabular-nums;
}

.cred-pw-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.cred-pw {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
}

.cred-tool {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex-shrink: 0;
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast),
    border-color var(--transition-fast);
}

.cred-tool:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
  border-color: var(--color-text-disabled);
}

.cred-tool:disabled {
  color: var(--color-text-disabled);
  cursor: not-allowed;
}

.cred-gen {
  height: 32px;
  flex-shrink: 0;
  gap: var(--space-2);
  white-space: nowrap;
  padding: 0 var(--space-5);
}

.cred-gen:hover {
  color: var(--color-accent);
  border-color: var(--color-accent-border-strong);
  background: var(--color-accent-bg);
}

.cred-hint {
  margin: 6px 0 0;
  font-size: var(--text-xs);
  line-height: 1.5;
  color: var(--color-text-muted);
}

.cred-textarea {
  height: auto;
  padding: var(--space-3) var(--space-4);
  resize: vertical;
  line-height: 1.6;
  font-family: inherit;
}

.cred-checks {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3) var(--space-6);
  margin-bottom: var(--space-4);
}

.cred-check {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  cursor: pointer;
}

/* 操作栏贴底：长表单不用滚回去找保存；删除降级成红色幽灵按钮。 */
.cred-actions {
  position: sticky;
  bottom: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin: 0 calc(-1 * var(--space-7));
  padding: var(--space-5) var(--space-7);
  background: var(--color-bg-primary);
  border-top: 1px solid var(--color-border);
}

.cred-del {
  background: none;
  border-color: transparent;
  color: var(--color-danger);
}

.cred-del:hover {
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
  color: var(--color-danger);
}

.cred-save {
  height: 32px;
  padding: 0 var(--space-7);
  font-weight: 500;
}

.cred-spacer { flex: 1; }

/* 窄窗：扩展条与导入导出面板原来是单行 flex + 定宽输入，控件被挤出可视区。 */
@media (max-width: 820px) {
  .cred-list { width: 216px; }
  .cred-row,
  .cred-row-url { grid-template-columns: minmax(0, 1fr); gap: 0; }
  .cred-field-kind { max-width: none; }
  .cred-pw-row { flex-wrap: wrap; }
  .cred-pw { flex: 1 1 100%; }
  .cred-transfer-pw { flex: 1 1 100%; width: auto; min-width: 0; }
  .cred-ext-row { gap: var(--space-2); }
}

@media (prefers-reduced-motion: reduce) {
  .cred-live-dot { animation: none; }
  .cred-skeleton { animation: none; }
}
</style>
