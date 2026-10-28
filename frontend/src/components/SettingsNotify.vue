<script setup lang="ts">
/**
 * 设置 → 通知：机器人 Webhook 统一配置。
 *
 * 为什么收进设置：这份配置本来就是全局一份（后端 settings 单键 notify_webhook），
 * 之前却在监控页和定时任务页各挂一个配置弹窗——两个入口改的是同一份数据，
 * 用户会误以为「每个任务/每个监控单独配机器人」。收进设置后，任务/监控页只保留
 * 「发不发、走哪个渠道」的开关，机器人地址统一在这里配。
 */
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { GetWebhookConfig, SetWebhookConfig, TestWebhook } from '../../bindings/quickdock/services/appservice'

const { t } = useI18n()
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

watch(() => props.visible, (v) => { if (v) { msg.value = ''; void load() } }, { immediate: true })

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
    msg.value = (r && r.code !== 0) ? '❌ ' + r.msg : '✅ ' + t('mon_nf_test_ok')
    msgOk.value = !(r && r.code !== 0)
  } catch (e) {
    msg.value = '❌ ' + getErrorMessage(e)
    msgOk.value = false
  } finally {
    testing.value = ''
  }
}
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
          <button class="nt-test" :disabled="testing === kind" @click="test(kind)">
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
  </div>
</template>

<style scoped>
.nt-hint { font-size: 11px; color: var(--color-text-disabled); margin: 0 0 var(--space-3); line-height: 1.6; }
.nt-label { display: block; font-size: 12px; color: var(--color-text-secondary); margin: var(--space-3) 0 4px; }
.nt-row { display: flex; gap: var(--space-2); align-items: center; }
.nt-row .qd-input { flex: 1; min-width: 0; }
.nt-test {
  flex-shrink: 0; height: 34px; padding: 0 14px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border-radius: var(--radius-md); font-size: 12px; cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.nt-test:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-hover); }
.nt-test:disabled { opacity: 0.5; cursor: default; }
.nt-msg { margin-top: var(--space-3); font-size: 12px; color: var(--color-text-secondary); word-break: break-all; line-height: 1.5; }
.nt-msg.ok { color: var(--color-success); }
.nt-actions { display: flex; justify-content: flex-end; margin-top: var(--space-4); }
</style>
