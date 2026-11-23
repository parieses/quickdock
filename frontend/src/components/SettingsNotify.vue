<script setup lang="ts">
/**
 * 设置 → 通知：机器人 Webhook 统一配置。
 *
 * 为什么收进设置：这份配置本来就是全局一份（后端 settings 单键 notify_webhook），
 * 之前却在监控页和定时任务页各挂一个配置弹窗——两个入口改的是同一份数据，
 * 用户会误以为「每个任务/每个监控单独配机器人」。收进设置后，任务/监控页只保留
 * 「发不发、走哪个渠道」的开关，机器人地址统一在这里配。
 */
import { ref, watch, inject, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'
import {
  GetWebhookConfig, SetWebhookConfig, TestWebhook,
  GetExtraNotifyChannels, SetExtraNotifyChannels, SendWebhookMessage,
} from '../../bindings/quickdock/services/appservice'

const { t } = useI18n()
// 扩展渠道的保存/加载/测试反馈统一走应用级 toast（App.vue provide），与设置页其它面板一致；
// 原有 6 家渠道的内联 msg 反馈保持不动，两套不混用。
const toast = inject<ToastAPI>('toast')!
const props = defineProps<{ visible: boolean }>()

const dingtalk = ref('')
const wecom = ref('')
const feishu = ref('')
const serverchan = ref('')
const pushplus = ref('')
const telegram = ref('')
const loading = ref(false)
const saving = ref(false)
const testing = ref('') // 正在测试的平台 kind（''=空闲）
const msg = ref('')
const msgOk = ref(false)

type WebhookKind = 'dingtalk' | 'wecom' | 'feishu' | 'serverchan' | 'pushplus' | 'telegram'
const kinds = ['dingtalk', 'wecom', 'feishu', 'serverchan', 'pushplus', 'telegram'] as const

const fields: Record<WebhookKind, { ref: typeof dingtalk }> = {
  dingtalk: { ref: dingtalk },
  wecom: { ref: wecom },
  feishu: { ref: feishu },
  serverchan: { ref: serverchan },
  pushplus: { ref: pushplus },
  telegram: { ref: telegram },
}

async function load() {
  loading.value = true
  try {
    const cfg = unwrap<{ dingtalk: string; wecom: string; feishu: string; serverchan: string; pushplus: string; telegram: string }>(await GetWebhookConfig())
    dingtalk.value = cfg?.dingtalk || ''
    wecom.value = cfg?.wecom || ''
    feishu.value = cfg?.feishu || ''
    serverchan.value = cfg?.serverchan || ''
    pushplus.value = cfg?.pushplus || ''
    telegram.value = cfg?.telegram || ''
  } catch {
    // 读取失败则展示空表单，保存时后端会再校验
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  msg.value = ''
  try {
    await unwrap(await SetWebhookConfig(
      dingtalk.value.trim(), wecom.value.trim(), feishu.value.trim(),
      serverchan.value.trim(), pushplus.value.trim(), telegram.value.trim(),
    ))
    msg.value = t('saved')
    msgOk.value = true
  } catch (e) {
    msg.value = getErrorMessage(e)
    msgOk.value = false
  } finally {
    saving.value = false
  }
}

async function test(kind: WebhookKind) {
  const url = fields[kind].ref.value.trim()
  if (!url) { msg.value = t('mon_nf_empty'); msgOk.value = false; return }
  testing.value = kind
  msg.value = ''
  try {
    const r = await TestWebhook(kind, url)
    msg.value = (r && r.code !== 0) ? r.msg : t('mon_nf_test_ok')
    msgOk.value = !(r && r.code !== 0)
  } catch (e) {
    msg.value = getErrorMessage(e)
    msgOk.value = false
  } finally {
    testing.value = ''
  }
}

// ── 扩展渠道 ────────────────────────────────────────────────────────
// 现有 6 家是「单键 webhook 配置」，扩展渠道是一份可增删的清单：
// 配置分开存（SetExtraNotifyChannels 收整份 JSON），发送仍走同一个 TestWebhook /
// 后端 webhook 通道。URL 属于敏感信息，输入框默认掩码、可逐条显示，且不进任何快照。
interface ExtraNotifyChannel {
  id: string
  kind: string
  name: string
  url: string
  enabled: boolean
}
const EXTRA_KINDS = ['bark', 'discord', 'slack', 'ntfy', 'gotify', 'custom'] as const
const EXTRA_MAX = 20
// 品牌名（Bark / Discord / Slack / Ntfy / Gotify）是专有名词，各语言写法一致，
// 由枚举值首字大写直接显示；只有 'custom' 需要按当前语言翻译成「自定义」。
// 下拉标签是运行时动态值，不是写死在模板里的业务字面量。
function extraKindLabel(k: string): string {
  if (k === 'custom') return t('notifyExtraKindCustom')
  return k ? k.charAt(0).toUpperCase() + k.slice(1) : k
}

const extraChannels = ref<ExtraNotifyChannel[]>([])
const extraLoading = ref(false)
const extraSaving = ref(false)
const extraTesting = ref('') // 正在测试的那条 id（''=空闲）
// 显示/隐藏按 id 记在独立 Set 里：它只是 UI 态，不该混进提交给后端的 JSON。
const shownUrls = ref<Set<string>>(new Set())

function isUrlVisible(id: string): boolean { return shownUrls.value.has(id) }
function toggleUrlVisible(id: string) {
  const next = new Set(shownUrls.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  shownUrls.value = next
}

function newExtraId(): string {
  return crypto.randomUUID ? crypto.randomUUID() : String(Date.now() + Math.random())
}

async function loadExtra() {
  extraLoading.value = true
  try {
    extraChannels.value = unwrap<ExtraNotifyChannel[]>(await GetExtraNotifyChannels()) ?? []
  } catch (e) {
    // 读不到就显示空清单，不把整页设置拖成错误态；给一条 toast 便于排查。
    extraChannels.value = []
    toast.error(t('notifyExtraLoadFailed') + ': ' + getErrorMessage(e))
  } finally {
    extraLoading.value = false
  }
}

function addExtra() {
  if (extraChannels.value.length >= EXTRA_MAX) {
    toast.error(t('notifyExtraMax'))
    return
  }
  extraChannels.value.push({ id: newExtraId(), kind: '', name: '', url: '', enabled: true })
}

function removeExtra(id: string) {
  extraChannels.value = extraChannels.value.filter((c) => c.id !== id)
  const next = new Set(shownUrls.value)
  next.delete(id)
  shownUrls.value = next
}

// 保存前逐条校验：kind 必选、URL 必须 http(s) 开头、总数不超过 20。
// 前端先拦一次，省掉一次明知会失败的往返；后端仍会再校验一遍。
function validateExtra(): string {
  if (extraChannels.value.length > EXTRA_MAX) return t('notifyExtraMax')
  for (const c of extraChannels.value) {
    if (!c.kind.trim()) return t('notifyExtraNeedKind')
    if (!/^https?:\/\//i.test(c.url.trim())) return t('notifyExtraBadUrl')
  }
  return ''
}

async function saveExtra() {
  const err = validateExtra()
  if (err) { toast.error(err); return }
  extraSaving.value = true
  try {
    const payload = extraChannels.value.map((c) => ({
      id: c.id,
      kind: c.kind.trim(),
      name: c.name.trim(),
      url: c.url.trim(),
      enabled: !!c.enabled,
    }))
    await unwrap(await SetExtraNotifyChannels(JSON.stringify(payload)))
    toast.success(t('saved'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    extraSaving.value = false
  }
}

async function testExtra(ch: ExtraNotifyChannel) {
  const url = ch.url.trim()
  if (!ch.kind.trim()) { toast.error(t('notifyExtraNeedKind')); return }
  if (!/^https?:\/\//i.test(url)) { toast.error(t('notifyExtraBadUrl')); return }
  extraTesting.value = ch.id
  try {
    // 扩展渠道的「测试」与现有 6 家共用 TestWebhook（后端已扩展支持这 6 种 kind）。
    unwrap(await TestWebhook(ch.kind, url))
    toast.success(t('mon_nf_test_ok'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    extraTesting.value = ''
  }
}

// ── 手动发消息 ────────────────────────────────────────────────────────
// 与定时任务/监控的 best-effort 异步发送不同：这里同步回收每个渠道的成败，
// 便于用户确认「真的发出去了」。渠道选项完全复用上面两节已加载的配置，不引入新接口。
interface SendChannelOption { value: string; label: string }
interface SendResultRow { channel: string; ok: boolean; error?: string }
interface SendResult { sent: number; ok: number; results: SendResultRow[] }

const sendChannel = ref('')
const sendTitle = ref('')
const sendBody = ref('')
const sending = ref(false) // 发送是同步串行的（多渠道 + 10s 超时会卡），禁用态防重复点击
const sendResult = ref<SendResult | null>(null)

const sendChannels = computed<SendChannelOption[]>(() => {
  const out: SendChannelOption[] = [{ value: '', label: t('notifySendChannelAll') }]
  // 内置 6 家：只列出已填了地址、即实际可用的渠道（值用 kind）。
  for (const kind of kinds) {
    if (fields[kind].ref.value.trim()) out.push({ value: kind, label: t('mon_nf_' + kind) })
  }
  // 扩展渠道：只列出启用且填了 URL 的；值用 id，避免按 kind 向同类型全部渠道扇出。
  for (const ch of extraChannels.value) {
    if (ch.enabled && ch.url.trim()) {
      out.push({ value: ch.id, label: ch.name.trim() || extraKindLabel(ch.kind) })
    }
  }
  return out
})

async function send() {
  const title = sendTitle.value.trim()
  // 后端会 trim 后判空，这里先拦一次给即时反馈（复用现成的通用空值提示）。
  if (!title) { toast.error(t('notifySendTitlePh') + t('cannotBeEmpty')); return }
  sending.value = true
  sendResult.value = null
  try {
    sendResult.value = unwrap<SendResult>(
      await SendWebhookMessage(title, sendBody.value, sendChannel.value)
    ) ?? { sent: 0, ok: 0, results: [] }
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    sending.value = false
  }
}

// 必须放在所有 ref / 函数声明之后：immediate watch 在 setup 期同步执行，
// 若提前到这里会先于 extraLoading 等 const 的初始化触发暂时性死区（打开通知设置直接崩）。
watch(() => props.visible, (v) => {
  if (v) { msg.value = ''; void load(); void loadExtra() }
}, { immediate: true })
</script>

<template>
  <div class="nt-panel">
    <div class="section">
      <h3 class="section-title">{{ t('mon_nf_title') }}</h3>
      <p class="section-desc">{{ t('notifyUnifiedDesc') }}</p>
      <p class="nt-hint">{{ t('mon_nf_hint') }}</p>

      <template v-for="kind in kinds" :key="kind">
        <label class="nt-label">{{ t('mon_nf_' + kind) }}</label>
        <div class="nt-row">
          <input v-model="fields[kind].ref.value" class="qd-input" :placeholder="t('mon_nf_' + kind + '_ph')" />
          <button class="btn btn-secondary btn-sm" :disabled="testing === kind" @click="test(kind)">
            {{ testing === kind ? '…' : t('mon_nf_test') }}
          </button>
        </div>
      </template>

      <div v-if="msg" :class="['nt-msg', { ok: msgOk }]">{{ msg }}</div>

      <div class="nt-actions">
        <button class="qd-btn qd-btn-primary" :disabled="saving || loading" @click="save">
          {{ saving ? t('saving') : t('save') }}
        </button>
      </div>
    </div>

    <!-- 扩展渠道：现有 6 家靠 kinds v-for 自动渲染，此处新增一份可增删的清单。
         两者共用同一套 webhook 发送后端，但配置分开存、分开保存。 -->
    <div class="section">
      <h3 class="section-title">{{ t('notifyExtraTitle') }}</h3>
      <p class="section-desc">{{ t('notifyExtraDesc') }}</p>
      <!-- 敏感信息提示：URL 输入框默认掩码，且这份配置从不写进任何导出快照。 -->
      <p class="nt-hint">{{ t('notifyExtraPrivacy') }}</p>

      <p v-if="!extraChannels.length" class="ntx-empty">{{ t('notifyExtraEmpty') }}</p>
      <div v-for="ch in extraChannels" :key="ch.id" class="ntx-row">
        <input v-model="ch.name" class="qd-input ntx-name" :placeholder="t('notifyExtraNamePh')" spellcheck="false" />
        <select v-model="ch.kind" class="qd-input ntx-kind">
          <option value="">{{ t('notifyExtraKindPh') }}</option>
          <option v-for="k in EXTRA_KINDS" :key="k" :value="k">{{ extraKindLabel(k) }}</option>
        </select>
        <div class="ntx-url">
          <input
            v-model="ch.url"
            :type="isUrlVisible(ch.id) ? 'text' : 'password'"
            class="qd-input"
            :placeholder="t('notifyExtraUrlPh')"
            autocomplete="off"
            spellcheck="false"
          />
          <button type="button" class="qd-btn qd-btn-ghost qd-btn-sm" @click="toggleUrlVisible(ch.id)">
            {{ isUrlVisible(ch.id) ? t('mcpTokenHide') : t('mcpTokenShow') }}
          </button>
        </div>
        <label class="ntx-enable">
          <input v-model="ch.enabled" type="checkbox" />
          <span>{{ t('notifyExtraEnable') }}</span>
        </label>
        <button class="btn btn-sm" :disabled="extraTesting === ch.id" @click="testExtra(ch)">
          {{ extraTesting === ch.id ? '…' : t('mon_nf_test') }}
        </button>
        <button class="btn btn-danger btn-sm" @click="removeExtra(ch.id)">{{ t('delete') }}</button>
      </div>

      <div class="ntx-actions">
        <button class="qd-btn" :disabled="extraChannels.length >= EXTRA_MAX" @click="addExtra">
          {{ t('notifyExtraAdd') }}
        </button>
        <button class="qd-btn qd-btn-primary" :disabled="extraSaving || extraLoading" @click="saveExtra">
          {{ extraSaving ? t('saving') : t('save') }}
        </button>
      </div>
    </div>

    <!-- 手动发消息：渠道选项完全来自上面两节已加载的配置，发送结果逐条展示成败。 -->
    <div class="section">
      <h3 class="section-title">{{ t('notifySendTitle') }}</h3>
      <p class="section-desc">{{ t('notifySendDesc') }}</p>

      <label class="nt-label">{{ t('notifySendChannel') }}</label>
      <select v-model="sendChannel" class="qd-input nt-send-channel">
        <option v-for="opt in sendChannels" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
      </select>
      <p v-if="sendChannels.length === 1" class="nt-hint">{{ t('notifySendEmpty') }}</p>

      <label class="nt-label">{{ t('notifySendTitlePh') }}</label>
      <input v-model="sendTitle" class="qd-input" :placeholder="t('notifySendTitlePh')" />

      <label class="nt-label">{{ t('notifySendBodyPh') }}</label>
      <textarea v-model="sendBody" class="qd-input nt-send-body" :placeholder="t('notifySendBodyPh')" rows="3"></textarea>

      <div class="nt-actions">
        <button class="qd-btn qd-btn-primary" :disabled="sending" @click="send">
          {{ sending ? t('notifySending') : t('notifySendButton') }}
        </button>
      </div>

      <div v-if="sendResult" class="nt-send-result">
        <div class="nt-send-summary">{{ t('notifySendSummary', { sent: sendResult.sent, ok: sendResult.ok }) }}</div>
        <div v-for="(r, i) in sendResult.results" :key="i" class="nt-send-row">
          <span class="nt-send-row-channel">{{ r.channel }}</span>
          <span :class="['nt-send-row-flag', r.ok ? 'ok' : 'fail']">{{ r.ok ? t('notifySendRowOk') : t('notifySendRowFail') }}</span>
          <span v-if="r.error" class="nt-send-row-error">{{ r.error }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.nt-hint { font-size: var(--text-sm); color: var(--color-text-muted); margin: 0 0 var(--space-3); line-height: 1.6; }
.nt-label { display: block; font-size: var(--text-sm); color: var(--color-text-secondary); margin: var(--space-3) 0 var(--space-2); }
.nt-row { display: flex; gap: var(--space-2); align-items: center; }
.nt-row .qd-input { flex: 1; min-width: 0; }
.nt-msg { margin-top: var(--space-3); font-size: var(--text-sm); color: var(--color-text-secondary); word-break: break-all; line-height: 1.5; }
.nt-msg.ok { color: var(--color-success); }
.nt-actions { display: flex; justify-content: flex-end; margin-top: var(--space-4); }
/* 扩展渠道行：字段较多，用 flex-wrap 让窄面板自动换行，不挤成横向滚动条 */
.ntx-empty { font-size: var(--text-sm); color: var(--color-text-muted); margin: var(--space-3) 0; }
.ntx-row {
  display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center;
  padding: var(--space-3) 0; border-bottom: 1px solid var(--color-border);
}
.ntx-name { flex: 1 1 120px; min-width: 100px; }
.ntx-kind { flex: 0 1 130px; min-width: 110px; }
.ntx-url { flex: 2 1 220px; min-width: 200px; display: flex; gap: var(--space-2); align-items: center; }
.ntx-url .qd-input { flex: 1; min-width: 0; }
.ntx-enable { display: inline-flex; align-items: center; gap: var(--space-2); flex-shrink: 0; font-size: var(--text-sm); color: var(--color-text-secondary); }
.ntx-actions { display: flex; justify-content: flex-end; gap: var(--space-2); margin-top: var(--space-4); }
/* 手动发消息 */
.nt-send-channel { max-width: 280px; }
.nt-send-body { min-height: 72px; resize: vertical; }
.nt-send-result { margin-top: var(--space-4); padding: var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); }
.nt-send-summary { font-size: var(--text-sm); color: var(--color-text-secondary); margin-bottom: var(--space-2); }
.nt-send-row { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; font-size: var(--text-sm); padding: var(--space-1) 0; }
.nt-send-row-channel { color: var(--color-text-primary); }
.nt-send-row-flag.ok { color: var(--color-success); }
.nt-send-row-flag.fail { color: var(--color-danger); }
.nt-send-row-error { color: var(--color-text-muted); font-size: var(--text-xs); word-break: break-all; }
</style>
