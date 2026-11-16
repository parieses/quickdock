<script lang="ts" setup>
/**
 * SSH 主机新建/编辑表单。
 *
 * 设计取舍：
 *   - 保存与测试连接都在本组件内完成，父组件只接收 emitted('saved') 后刷新列表。
 *     表单字段多，抛给父组件维护会多一层状态同步，还容易漏掉 sort/parentId 这类
 *     「不该改但必须原样带回去」的字段。
 *   - 「测试连接」测的是表单当前的值，不要求先保存：新建主机填错密码/端口时，
 *     不该被迫先存一台连不上的机器再回头改（后端 TestHostInput 接受未落库的表单值）。
 *     但也不会偷偷保存——测试通过与否都不改动已存配置，用户点「取消」时一切照旧。
 */
import { computed, reactive, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight, Eye, EyeOff, KeyRound, Loader2, X } from '@lucide/vue'
import { ListHosts, SaveHost, TestHostInput } from '../../bindings/quickdock/services/sshx/sshservice'
// 入参类型用 bindings 重新生成后的那份（含 mcpAllowed/tags/... 九个新字段），
// 而不是 utils/ssh.ts 里手写的旧 SshHostInput —— 后者缺列，SaveHost/TestHostInput
// 现在都要求新字段，toInput() 直接返回新类型才对得上。
import type { SshHostInput } from '../../bindings/quickdock/services/sshx/models'
import { MatchCredentials, RevealCredential } from '../../bindings/quickdock/services/credentials/credentialsservice'
import { apiErrorCode, apiErrorData, unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useFocusTrap } from '../utils/focusTrap'
import { useToast } from '../composables/useToast'
import {
  SSH_ERR,
  sshErrorText,
  type SshHost,
  type SshHostKeyInfo,
  type TestHostResult,
} from '../utils/ssh'
import type { SshHostDisplay } from '../utils/sshTree'
import SshFingerprintDialog from './SshFingerprintDialog.vue'
import QdTabs from './QdTabs.vue'

const { t } = useI18n()
// 只取 success：失败文案可能很长（认证方式不匹配、指纹异常等），
// toast 是 white-space: nowrap 的单行容器，长句会撑出屏幕，
// 所以失败仍走行内结果条 —— 现在它已钉在按钮栏上方，不会再被滚动吞掉。
const { success: toastSuccess } = useToast()

/**
 * 从密码库填充这台主机的密码。
 *
 * 只填第一条匹配项（按主机名 + 端口 + 用户名打分，见 internal/credentials/match.go）。
 * 不做「弹一个选择列表」是因为这里已经有输入框：多条时用 toast 告知条数，
 * 想看全部就去密码库页 —— 在编辑对话框里再叠一层选择器反而更绕。
 */
async function fillFromVault() {
  if (!form.host.trim()) {
    toastSuccess(t('credNeedHostFirst'))
    return
  }
  try {
    const res = await MatchCredentials('ssh', form.host.trim(), Number(form.port) || 22, form.user.trim())
    const items = unwrap<Array<{ id: string; name: string; hasSecret: boolean }>>(res) ?? []
    const withSecret = items.filter((it) => it.hasSecret)
    if (!withSecret.length) {
      toastSuccess(t('credNoMatchForHost'))
      return
    }
    const data = unwrap<{ username: string; secret: string }>(await RevealCredential(withSecret[0].id))
    if (data?.secret) {
      form.password = data.secret
      showPassword.value = true
      toastSuccess(withSecret.length > 1
        ? t('credFilledOneOfMany', { n: withSecret.length })
        : t('credFilledFromVault'))
    }
  } catch (e) {
    // 密码库不可用（未设密钥 / 平台不支持）不该阻断手动填密码，
    // 所以这里只记日志，界面上不弹错误。
    logErr('SshHostDialog.fillFromVault', e)
  }
}

const props = defineProps<{
  visible: boolean
  /** 传入表示编辑，为空表示新建 */
  host?: SshHostDisplay | null
  /**
   * 新建时的所属分组（#13）。
   *
   * 从树上某个文件夹的「+」进来时带上；为空表示建在根。
   * 编辑已有主机时忽略——分组由拖拽调整，不在表单里改。
   */
  parentId?: string
}>()

const emit = defineEmits<{
  (e: 'saved'): void
  (e: 'cancel'): void
}>()

interface FormState {
  name: string
  host: string
  port: string
  user: string
  authType: 'password' | 'key'
  password: string
  clearPassword: boolean
  keyPath: string
  keyPassphrase: string
  legacyAlgorithms: boolean
  env: string
  remark: string
  // ---- 高级（与 bindings/models.ts SshHostInput 新增字段一一对应） ----
  mcpAllowed: boolean
  tags: string
  color: string
  icon: string
  postLoginCommand: string
  autoReplySudo: boolean
  proxyCommand: string
  proxyCommandEnabled: boolean
  keepaliveInterval: number
}

const form = reactive<FormState>(blankForm())

// 环境与认证两组分段的标签；环境色在 #tab 插槽里以圆点表达（见 .env-dot）。
const envTabs = computed(() => [
  { id: 'prod' as const, label: t('sshEnvProd') },
  { id: 'staging' as const, label: t('sshEnvStaging') },
  { id: 'test' as const, label: t('sshEnvTest') },
])
const authTabs = computed(() => [
  { id: 'password' as const, label: t('sshAuthPassword') },
  { id: 'key' as const, label: t('sshAuthKey') },
])
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

const saving = ref(false)
const testing = ref(false)
const formErr = ref('')
const testErr = ref('')
const testOk = ref('')
const showPassword = ref(false)
const showPassphrase = ref(false)
/** 「高级」折叠区开关：默认收起，别让一屏表单把常用字段挤到要滚动。 */
const advancedOpen = ref(false)

/**
 * 颜色下拉：value 存的是设计 token 的**变量名**（R4：色值只能来自 style.css），
 * 不是色值。'' = 跟随环境色。选项与树里 ssh-color-* 预定义 class 一一对应。
 */
const colorOptions = [
  { value: '', key: 'sshHostColorFollow' },
  { value: '--color-accent', key: 'sshHostColorAccent' },
  { value: '--color-danger', key: 'sshHostColorDanger' },
  { value: '--color-warning', key: 'sshHostColorWarning' },
  { value: '--color-success', key: 'sshHostColorSuccess' },
]

/** 保活：0 = 默认 45s，-1 = 关闭，其余合法区间 5..600；越界钳到边界。 */
const KEEPALIVE_MIN = 5
const KEEPALIVE_MAX = 600
function clampKeepalive(v: number | string): number {
  const raw = Number(v)
  if (!Number.isFinite(raw)) return 0
  const n = Math.round(raw)
  if (n === 0 || n === -1) return n
  if (n < 0) return -1 // 其它负数统一按「关闭」
  if (n < KEEPALIVE_MIN) return KEEPALIVE_MIN
  if (n > KEEPALIVE_MAX) return KEEPALIVE_MAX
  return n
}
function onKeepaliveBlur() {
  form.keepaliveInterval = clampKeepalive(form.keepaliveInterval)
}

// 生产机自动送密码风险太大：切到 prod 时立刻清掉勾选，保存时 toInput 再兜一道，
// 避免「先勾上、再切环境」把残留值写进库。理由同 SSH 表单里其它保守默认值。
watch(() => form.env, (env) => {
  if (env === 'prod') form.autoReplySudo = false
})

// ---- 跳板机（空串 = 直连） ----
const jumpHostId = ref('')
const allHosts = ref<SshHost[]>([])

// ---- 指纹确认（测试连接时可能触发） ----
const fpVisible = ref(false)
const fpMode = ref<'unknown' | 'changed'>('unknown')
const fpInfo = ref<SshHostKeyInfo>({})

const isEdit = computed(() => !!props.host?.id)
const hasPassword = computed(() => !!props.host?.hasPassword)
const hasPassphrase = computed(() => !!props.host?.hasPassphrase)

// ---- 跳板机候选项 ----
/** 沿 jumpHostId 链向上最多走 8 步，脏数据里的环也不会把 computed 卡死。 */
const JUMP_CHAIN_LIMIT = 8

interface JumpOption {
  id: string
  label: string
}

/**
 * 候选跳板机：第一项固定「不使用跳板」，其后只列非文件夹、非自己、
 * 且跳板链不会绕回当前编辑机的主机（A→B→A 会让连接无限中转）。
 */
const jumpOptions = computed<JumpOption[]>(() => {
  const editingId = props.host?.id ?? ''
  const byId = new Map(allHosts.value.map((h) => [h.id, h]))
  const options: JumpOption[] = [{ id: '', label: t('sshHostJumpNone') }]
  for (const h of allHosts.value) {
    if (h.isFolder || h.id === editingId) continue
    if (editingId && jumpChainHits(h, editingId, byId)) continue
    options.push({ id: h.id, label: h.name || `${h.host}:${h.port}` })
  }
  return options
})

/** 沿候选主机的 jumpHostId 链向上走，命中当前编辑机 id 即说明会成环。 */
function jumpChainHits(start: SshHost, editingId: string, byId: Map<string, SshHost>): boolean {
  let cur = start
  const seen = new Set<string>()
  for (let step = 0; step < JUMP_CHAIN_LIMIT; step++) {
    const nextId = cur.jumpHostId
    if (!nextId || seen.has(nextId)) return false
    if (nextId === editingId) return true
    seen.add(nextId)
    const next = byId.get(nextId)
    if (!next) return false
    cur = next
  }
  return false
}

function blankForm(): FormState {
  return {
    name: '',
    host: '',
    port: '22',
    user: 'root',
    authType: 'password',
    password: '',
    clearPassword: false,
    keyPath: '',
    keyPassphrase: '',
    legacyAlgorithms: false,
    env: 'test',
    remark: '',
    mcpAllowed: false,
    tags: '',
    color: '',
    icon: '',
    postLoginCommand: '',
    autoReplySudo: false,
    proxyCommand: '',
    proxyCommandEnabled: false,
    keepaliveInterval: 0,
  }
}

function fillFromHost(h: SshHostDisplay | null | undefined) {
  const next = blankForm()
  if (h) {
    next.name = h.name ?? ''
    next.host = h.host ?? ''
    next.port = String(h.port || 22)
    next.user = h.user || 'root'
    next.authType = h.authType === 'key' ? 'key' : 'password'
    next.keyPath = h.keyPath ?? ''
    next.legacyAlgorithms = !!h.legacyAlgorithms
    next.env = h.env || 'test'
    next.remark = h.remark ?? ''
    // 高级字段按同一套 fillFromHost 逻辑补齐（与上面基础字段保持一致），
    // 不另起一份 watch / onMounted 赋值链。全部走 ?? / !!，老数据缺列时回落到默认值。
    next.mcpAllowed = !!h.mcpAllowed
    next.tags = h.tags ?? ''
    next.color = h.color ?? ''
    next.icon = h.icon ?? ''
    next.postLoginCommand = h.postLoginCommand ?? ''
    next.autoReplySudo = !!h.autoReplySudo
    next.proxyCommand = h.proxyCommand ?? ''
    next.proxyCommandEnabled = !!h.proxyCommandEnabled
    next.keepaliveInterval = typeof h.keepaliveInterval === 'number' ? h.keepaliveInterval : 0
  }
  Object.assign(form, next)
  jumpHostId.value = h?.jumpHostId ?? ''
  saving.value = false
  testing.value = false
  formErr.value = ''
  testErr.value = ''
  testOk.value = ''
  showPassword.value = false
  showPassphrase.value = false
  advancedOpen.value = false
}

/** 拉一次主机列表供跳板下拉用；失败就静默只留「不使用跳板」。 */
async function loadHosts() {
  try {
    allHosts.value = unwrap<SshHost[]>(await ListHosts()) || []
  } catch {
    allHosts.value = []
  }
}

// 只在「打开」这个边沿重置：父组件重渲染会带来新的 props.host 引用，
// 若把它放进依赖，用户正在输入的密码会被清空。
watch(
  () => props.visible,
  (v) => {
    if (v) {
      fillFromHost(props.host)
      void loadHosts()
    }
  },
  { immediate: true },
)

/**
 * 挡住「测试连接」的原因，空串表示可以测。
 *
 * 只作为禁用按钮的 title 提示，不挂到结果区——刚打开一个空白表单就红一句
 * 「请填写主机地址」太吵，用户还没开始填。
 */
const testBlocker = computed(() => {
  const bad = validate()
  if (bad) return bad
  // 编辑态密码留空是「沿用库里已存的」；新建或勾了「清除密码」时留空才是真没凭据。
  if (form.authType === 'password' && !form.password && (!props.host || form.clearPassword)) {
    return t('sshPasswordRequired')
  }
  return ''
})

const canTest = computed(() => !testBlocker.value && !saving.value && !testing.value)

function validate(): string {
  if (!form.host.trim()) return t('sshHostRequired')
  const p = Number(form.port)
  if (!Number.isInteger(p) || p < 1 || p > 65535) return t('sshPortInvalid')
  if (form.authType === 'key' && !form.keyPath.trim()) return t('sshKeyPathRequired')
  return ''
}

function toInput(): SshHostInput {
  const h = props.host
  return {
    id: h?.id ?? '',
    name: form.name.trim(),
    host: form.host.trim(),
    port: Number(form.port) || 22,
    jumpHostId: jumpHostId.value,
    user: form.user.trim() || 'root',
    authType: form.authType,
    password: form.password,
    clearPassword: form.clearPassword,
    keyPath: form.keyPath.trim(),
    keyPassphrase: form.keyPassphrase,
    legacyAlgorithms: form.legacyAlgorithms,
    env: form.env,
    // ---- 高级 ----
    mcpAllowed: form.mcpAllowed,
    tags: form.tags.trim(),
    color: form.color,
    icon: form.icon.trim(),
    postLoginCommand: form.postLoginCommand,
    // 生产机强制 false：UI 已禁用勾选，这里再兜一道，防止切环境前的残留值入库。
    autoReplySudo: form.env === 'prod' ? false : form.autoReplySudo,
    // 只填命令绝不自动启用：proxyCommandEnabled 完全由用户单独勾选（见下方高级区）。
    proxyCommand: form.proxyCommand,
    proxyCommandEnabled: form.proxyCommandEnabled,
    keepaliveInterval: clampKeepalive(form.keepaliveInterval),
    // parentId / sort / isFolder 本表单不编辑，但必须原样带回：
    // 漏掉 sort 会让这台主机在列表里的排序被重置成 0，看起来像「自己跳到了最前面」。
    // 新建时 parentId 取自 props（从哪个文件夹点「+」进来的），
    // sort 传 0 让后端分配到末尾。
    parentId: props.host ? (props.host.parentId ?? '') : (props.parentId ?? ''),
    isFolder: props.host?.isFolder ?? false,
    sort: props.host?.sort ?? 0,
    remark: form.remark.trim(),
  }
}

async function save() {
  if (saving.value) return
  const bad = validate()
  if (bad) {
    formErr.value = bad
    return
  }
  saving.value = true
  formErr.value = ''
  try {
    unwrap(await SaveHost(toInput()))
    emit('saved')
  } catch (e) {
    formErr.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

function openFingerprint(mode: 'unknown' | 'changed', data: SshHostKeyInfo) {
  fpMode.value = mode
  fpInfo.value = {
    host: data.host || form.host.trim(),
    port: data.port || Number(form.port) || 22,
    keyType: data.keyType || '',
    fingerprint: data.newFingerprint || data.fingerprint || '',
    oldFingerprint: data.oldFingerprint || '',
    knownTypes: data.knownTypes || [],
  }
  fpVisible.value = true
}

async function testConn() {
  if (!canTest.value) return
  testing.value = true
  testErr.value = ''
  testOk.value = ''
  try {
    // 传表单值而不是 id：新建的主机库里还没有记录，后端也能拿同一套逻辑去连。
    const r = unwrap<TestHostResult>(await TestHostInput(toInput()))
    if (!r) return
    if (r.needsTrust) {
      // 连通了，但服务器身份还没被信任：这不是失败，弹窗让用户核对指纹。
      // host/port 取表单值：新建时 props.host 为空，用库里的会把待信任的
      // 指纹记到一台不存在（或另一台）的机器名下。
      openFingerprint('unknown', {
        host: form.host.trim(),
        port: Number(form.port) || 22,
        keyType: r.hostKeyAlgorithm,
        fingerprint: r.fingerprint,
        knownTypes: r.knownTypes,
      })
      return
    }
    testOk.value = t('sshTestOk', { version: r.serverVersion || '-', ms: r.latencyMs })
    // 结果行原本在滚动区底部，表单一长就看不见，用户会以为按钮没生效。
    // 成功是「一次性的确认」，弹到视口顶部最省事；行内那份留着，方便回看
    // 服务器版本和延迟（3 秒后 toast 就没了，这些信息用户还想再瞄一眼）。
    toastSuccess(testOk.value)
  } catch (e) {
    const code = apiErrorCode(e)
    if (code === SSH_ERR.HOSTKEY_CHANGED) {
      openFingerprint('changed', apiErrorData<SshHostKeyInfo>(e) || {})
      return
    }
    if (code === SSH_ERR.HOSTKEY_UNKNOWN) {
      openFingerprint('unknown', apiErrorData<SshHostKeyInfo>(e) || {})
      return
    }
    testErr.value = sshErrorText(e, code, t)
  } finally {
    testing.value = false
  }
}

/** 信任指纹后自动重测一次，用户不用再点一遍。 */
function onTrusted() {
  fpVisible.value = false
  void testConn()
}

function onKeydown(e: KeyboardEvent) {
  // 指纹弹窗开着时，事件属于它，别再让表单吞掉 Esc
  if (fpVisible.value) return
  if (e.key === 'Escape') {
    emit('cancel')
    return
  }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="hd">
      <div v-if="visible" class="hd-overlay" @mousedown.self="emit('cancel')" @keydown="onKeydown">
        <div ref="panelRef" class="hd-panel" role="dialog" @mousedown.stop>
          <header class="hd-head">
            <span class="hd-title">{{ isEdit ? t('sshEditHost') : t('sshNewHost') }}</span>
            <button class="hd-x" :title="t('cancel')" @click="emit('cancel')">
              <X :size="16" />
            </button>
          </header>

          <div class="hd-body">
            <!-- 基本信息 -->
            <div class="hd-grid hd-grid--2">
              <label class="fd">
                <span class="fd-label">{{ t('sshName') }}</span>
                <input v-model="form.name" class="fd-input" :placeholder="t('sshNamePlaceholder')" />
              </label>
              <div class="fd">
                <span class="fd-label">{{ t('sshEnvLabel') }}</span>
                <QdTabs
                  :model-value="form.env"
                  :tabs="envTabs"
                  @update:model-value="(v: string) => (form.env = v as 'prod' | 'staging' | 'test')"
                >
                  <template #tab="{ tab }">
                    <span class="env-dot" :class="tab.id" />
                    {{ tab.label }}
                  </template>
                </QdTabs>
              </div>
            </div>

            <div class="hd-grid hd-grid--addr">
              <label class="fd">
                <span class="fd-label">{{ t('sshHostAddr') }}</span>
                <input
                  v-model="form.host"
                  class="fd-input"
                  spellcheck="false"
                  :placeholder="t('sshHostAddrPlaceholder')"
                />
              </label>
              <label class="fd">
                <span class="fd-label">{{ t('sshPort') }}</span>
                <input v-model="form.port" class="fd-input" inputmode="numeric" placeholder="22" />
              </label>
              <label class="fd">
                <span class="fd-label">{{ t('sshUser') }}</span>
                <input v-model="form.user" class="fd-input" spellcheck="false" placeholder="root" />
              </label>
            </div>

            <!-- 跳板机 -->
            <label class="fd">
              <span class="fd-label">{{ t('sshHostJump') }}</span>
              <select v-model="jumpHostId" class="fd-input">
                <option v-for="opt in jumpOptions" :key="opt.id" :value="opt.id">{{ opt.label }}</option>
              </select>
              <em class="fd-hint">{{ t('sshHostJumpHint') }}</em>
            </label>

            <!-- 认证 -->
            <div class="hd-section">
              <QdTabs
                class="seg--wide"
                :model-value="form.authType"
                :tabs="authTabs"
                @update:model-value="(v: string) => (form.authType = v as 'password' | 'key')"
              />

              <template v-if="form.authType === 'password'">
                <label class="fd">
                  <span class="fd-label">{{ t('sshPassword') }}</span>
                  <span class="fd-secret">
                    <input
                      v-model="form.password"
                      class="fd-input"
                      :type="showPassword ? 'text' : 'password'"
                      autocomplete="new-password"
                      :placeholder="hasPassword ? t('sshSecretKeep') : t('sshPasswordPlaceholder')"
                    />
                    <button type="button" class="fd-eye" @click="showPassword = !showPassword">
                      <EyeOff v-if="showPassword" :size="14" />
                      <Eye v-else :size="14" />
                    </button>
                    <button
                      type="button"
                      class="fd-eye fd-vault"
                      :title="t('credFillFromVault')"
                      @click="fillFromVault"
                    >
                      <KeyRound :size="14" />
                    </button>
                  </span>
                </label>
                <label v-if="hasPassword" class="fd-check">
                  <input v-model="form.clearPassword" type="checkbox" />
                  <span>{{ t('sshPasswordClear') }}</span>
                </label>
              </template>

              <template v-else>
                <label class="fd">
                  <span class="fd-label">{{ t('sshKeyPath') }}</span>
                  <input
                    v-model="form.keyPath"
                    class="fd-input"
                    spellcheck="false"
                    :placeholder="t('sshKeyPathPlaceholder')"
                  />
                </label>
                <label class="fd">
                  <span class="fd-label">{{ t('sshKeyPassphrase') }}</span>
                  <span class="fd-secret">
                    <input
                      v-model="form.keyPassphrase"
                      class="fd-input"
                      :type="showPassphrase ? 'text' : 'password'"
                      autocomplete="new-password"
                      :placeholder="hasPassphrase ? t('sshSecretKeep') : t('sshKeyPassphrasePlaceholder')"
                    />
                    <button type="button" class="fd-eye" @click="showPassphrase = !showPassphrase">
                      <EyeOff v-if="showPassphrase" :size="14" />
                      <Eye v-else :size="14" />
                    </button>
                  </span>
                </label>
              </template>

              <label class="fd-check">
                <input v-model="form.legacyAlgorithms" type="checkbox" />
                <span>
                  {{ t('sshLegacy') }}
                  <em class="fd-hint">{{ t('sshLegacyHint') }}</em>
                </span>
              </label>
            </div>

            <label class="fd">
              <span class="fd-label">{{ t('sshRemark') }}</span>
              <textarea v-model="form.remark" class="fd-input fd-area" rows="2" :placeholder="t('sshRemarkPlaceholder')" />
            </label>

            <!-- 高级：折叠区一律复用 settings.css 的公共类（.section / .field* / .btn），
                 不新造一套表单样式。 -->
            <div class="section hd-adv">
              <button type="button" class="btn btn-outline hd-adv-toggle" @click="advancedOpen = !advancedOpen">
                <ChevronDown v-if="advancedOpen" :size="13" />
                <ChevronRight v-else :size="13" />
                <span>{{ t('sshHostAdvanced') }}</span>
              </button>

              <div v-show="advancedOpen" class="hd-adv-body">
                <!-- 1. 允许 MCP Agent 使用：默认关，必须带明确警示 -->
                <div class="field">
                  <label class="hd-adv-check field-checkbox">
                    <input v-model="form.mcpAllowed" type="checkbox" />
                    <span>{{ t('sshHostMcpAllowed') }}</span>
                  </label>
                  <p class="field-hint hd-adv-warn">{{ t('sshHostMcpAllowedWarn') }}</p>
                </div>

                <!-- 2. 标签 / 颜色 / 图标：颜色只选设计 token 名，不是色值 -->
                <div class="field-row">
                  <label class="field field-half">
                    <span class="field-label">{{ t('sshHostTags') }}</span>
                    <input v-model="form.tags" class="field-input" :placeholder="t('sshHostTagsPlaceholder')" />
                    <p class="field-hint">{{ t('sshHostTagsHint') }}</p>
                  </label>
                  <label class="field field-half">
                    <span class="field-label">{{ t('sshHostColor') }}</span>
                    <select v-model="form.color" class="field-input">
                      <option v-for="o in colorOptions" :key="o.value" :value="o.value">{{ t(o.key) }}</option>
                    </select>
                  </label>
                </div>
                <div class="field-row">
                  <label class="field field-half">
                    <span class="field-label">{{ t('sshHostIcon') }}</span>
                    <input v-model="form.icon" class="field-input" :placeholder="t('sshHostIconPlaceholder')" />
                  </label>
                  <label class="field field-half">
                    <span class="field-label">{{ t('sshHostKeepalive') }}</span>
                    <input
                      v-model.number="form.keepaliveInterval"
                      class="field-input"
                      type="number"
                      @blur="onKeepaliveBlur"
                    />
                    <p class="field-hint">{{ t('sshHostKeepaliveHint') }}</p>
                  </label>
                </div>

                <!-- 3. 登录后自动执行 -->
                <label class="field">
                  <span class="field-label">{{ t('sshHostPostLogin') }}</span>
                  <input v-model="form.postLoginCommand" class="field-input" :placeholder="t('sshHostPostLoginPlaceholder')" />
                  <p class="field-hint">{{ t('sshHostPostLoginHint') }}</p>
                </label>

                <!-- 4. sudo 自动应答：生产机自动送密码风险太大，prod 时禁用并提示。
                     理由见脚本里 watch(() => form.env) 的注释。 -->
                <div class="field">
                  <label class="hd-adv-check field-checkbox">
                    <input v-model="form.autoReplySudo" type="checkbox" :disabled="form.env === 'prod'" />
                    <span>{{ t('sshHostSudo') }}</span>
                  </label>
                  <p v-if="form.env === 'prod'" class="field-hint hd-adv-warn">{{ t('sshHostSudoProdHint') }}</p>
                </div>

                <!-- 5. ProxyCommand：命令与「启用」是两个独立控件，
                     只填命令绝不自动勾选 proxyCommandEnabled。 -->
                <div class="field">
                  <span class="field-label">{{ t('sshHostProxy') }}</span>
                  <textarea v-model="form.proxyCommand" class="field-input" rows="2" :placeholder="t('sshHostProxyPlaceholder')" />
                  <label class="hd-adv-check field-checkbox">
                    <input v-model="form.proxyCommandEnabled" type="checkbox" />
                    <span>{{ t('sshHostProxyEnabled') }}</span>
                  </label>
                  <p class="field-hint hd-adv-warn">{{ t('sshHostProxyWarn') }}</p>
                  <p class="field-hint">{{ t('sshHostProxyPlaceholders') }}</p>
                </div>
              </div>
            </div>
          </div>

          <!-- 结果区：故意放在滚动区外，钉在按钮栏正上方。
               表单字段多时 .hd-body 会滚动，结果行跟着滚就会被推到看不见的底部，
               用户点完「测试连接」只看到按钮恢复原样，以为没生效。 -->
          <div v-if="formErr || testErr || testOk" class="hd-status">
            <p v-if="formErr" class="hd-msg hd-msg--err">{{ formErr }}</p>
            <p v-else-if="testErr" class="hd-msg hd-msg--err">{{ testErr }}</p>
            <p v-else-if="testOk" class="hd-msg hd-msg--ok">{{ testOk }}</p>
          </div>

          <footer class="hd-foot">
            <button class="btn btn-ghost" :disabled="saving" @click="emit('cancel')">{{ t('cancel') }}</button>
            <button class="btn btn-outline" :disabled="!canTest" :title="testBlocker" @click="testConn">
              <Loader2 v-if="testing" :size="13" class="spin" />
              <span>{{ testing ? t('sshTesting') : t('sshTest') }}</span>
            </button>
            <button class="btn btn-primary" :disabled="saving" @click="save">
              {{ saving ? t('sshSaving') : t('save') }}
            </button>
          </footer>
        </div>
      </div>
    </Transition>

    <SshFingerprintDialog
      :visible="fpVisible"
      :mode="fpMode"
      :host="fpInfo.host || ''"
      :port="fpInfo.port || 22"
      :key-type="fpInfo.keyType || ''"
      :fingerprint="fpInfo.fingerprint || ''"
      :old-fingerprint="fpInfo.oldFingerprint || ''"
      :known-types="fpInfo.knownTypes || []"
      @trusted="onTrusted"
      @cancel="fpVisible = false"
    />
  </Teleport>
</template>

<style scoped>
.hd-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

.hd-panel {
  width: 520px;
  max-width: 94vw;
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden;
}

.hd-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-6) var(--space-7);
  border-bottom: 1px solid var(--color-border);
}

.hd-title {
  font-size: var(--text-base);
  font-weight: 600;
  color: var(--color-text-primary);
}

.hd-x {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.hd-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.hd-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-6) var(--space-7);
  overflow-y: auto;
}

.hd-grid {
  display: grid;
  gap: var(--space-5);
}

.hd-grid--2 {
  grid-template-columns: 1fr 200px;
}

/* 主机名占大头，端口窄、用户名适中 */
.hd-grid--addr {
  grid-template-columns: 1fr 84px 130px;
}

.fd {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  min-width: 0;
}

.fd-label {
  font-size: var(--text-xs);
  font-weight: 500;
  color: var(--color-text-muted);
}

.fd-input {
  width: 100%;
  box-sizing: border-box;
  padding: var(--space-3) var(--space-5);
  font-size: var(--text-md);
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  outline: none;
  transition: border-color var(--transition-fast);
}

.fd-input:focus {
  border-color: var(--color-border-focus-soft);
}

.fd-input::placeholder {
  color: var(--color-text-disabled);
}

/* 原生 select 的下拉面板在深色主题下要显式声明配色，否则是白底黑字 */
select.fd-input {
  color-scheme: dark;
}

.fd-area {
  resize: vertical;
  min-height: 48px;
  line-height: 1.5;
}

.fd-secret {
  position: relative;
  display: block;
}

.fd-secret .fd-input {
  /* 右侧要同时容纳「显示密码」与「从密码库填充」两个图标按钮 */
  padding-right: calc(var(--space-9) + 22px);
}

/* 第二个图标按钮：往左让开一个位，避免与眼睛按钮重叠 */
.fd-vault {
  right: calc(var(--space-3) + 22px);
}

.fd-eye {
  position: absolute;
  top: 50%;
  right: var(--space-3);
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-muted);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  transform: translateY(-50%);
  cursor: pointer;
}

.fd-eye:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.fd-check {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
  font-size: var(--text-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
  cursor: pointer;
}

.fd-check input {
  margin-top: var(--space-1);
  cursor: pointer;
}

.fd-hint {
  display: block;
  margin-top: var(--space-1);
  font-size: var(--text-xs);
  font-style: normal;
  color: var(--color-text-muted);
}

.hd-section {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  padding: var(--space-5) var(--space-6);
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-sm);
}

/* 「高级」折叠区：只补新元素（折叠头 / 勾选行 / 颜色警告）的排布，
   字段本身全部复用 settings.css 的 .field* 公共类，不新造表单体系。 */
.hd-adv {
  padding: var(--space-4) var(--space-5);
  background: var(--color-card-bg);
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-sm);
}

.hd-adv-toggle {
  width: 100%;
  justify-content: flex-start;
}

.hd-adv-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
  margin-top: var(--space-5);
}

/* 全局 .field-row 是 align-items:center（给单行控件用），这里两列高度不等，
   顶对齐才不会把矮的那列吊在中间。 */
.hd-adv .field-row {
  align-items: flex-start;
}

/* 原生 select 在深色主题下要声明 color-scheme，否则下拉面板白底黑字。 */
.hd-adv select.field-input {
  color-scheme: dark;
}

.hd-adv textarea.field-input {
  min-height: 56px;
  resize: vertical;
}

.hd-adv-check {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
}

.hd-adv-warn {
  color: var(--color-warning);
}

/* 分段选择器已统一到 QdTabs；这里只保留它作为 flex 子项的对齐方式。 */
.seg--wide {
  align-self: flex-start;
}

/* 环境色仍是安全语义（生产/预发/测试），但不再整块染色——
   统一约定是「状态用小图标 + 常规文字色」，整块底色会把一行字变成色块。 */
.env-dot {
  flex: none;
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.env-dot.prod { background: var(--color-danger); }
.env-dot.staging { background: var(--color-warning); }
.env-dot.test { background: var(--color-success); }

.hd-status {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-7);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.hd-msg {
  margin: 0;
  padding: var(--space-3) var(--space-5);
  font-size: var(--text-sm);
  line-height: 1.5;
  border-radius: var(--radius-xs);
  word-break: break-all;
}

.hd-msg--err {
  color: var(--color-danger);
  background: var(--color-danger-bg);
}

.hd-msg--ok {
  color: var(--color-success);
  background: var(--color-success-bg);
}

.hd-msg--hint {
  color: var(--color-text-muted);
  background: var(--color-bg-secondary);
}

.hd-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-4);
  padding: var(--space-5) var(--space-7);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
  height: 32px; padding: 0 var(--space-6);
  font-size: var(--text-md);
  font-weight: 500;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-active);
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background var(--transition-fast), opacity var(--transition-fast);
}

.btn:disabled {
  cursor: default;
  opacity: 0.45;
}

.btn-outline {
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
}

.btn-outline:hover:not(:disabled) {
  color: var(--color-text-primary);
  border-color: var(--color-border-light);
}

.spin {
  animation: hd-spin 900ms linear infinite;
}

@keyframes hd-spin {
  to {
    transform: rotate(360deg);
  }
}

.hd-enter-active {
  transition: opacity 0.16s ease-out;
}

.hd-leave-active {
  transition: opacity 0.12s ease-in;
}

.hd-enter-from,
.hd-leave-to {
  opacity: 0;
}
</style>
