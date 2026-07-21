<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { GetWebhookConfig, SetWebhookConfig, TestWebhook } from '../../bindings/quickdock/services/appservice'
import { Bell } from '@lucide/vue'

const props = defineProps<{ label: string }>()
const { t } = useI18n()

const show = ref(false)
const dingtalk = ref('')
const wecom = ref('')
const feishu = ref('')
const saving = ref(false)
const testing = ref('') // 正在测试的平台 kind（''=空闲）
const msg = ref('')

async function open() {
  msg.value = ''
  try {
    const cfg = unwrap<{ dingtalk: string; wecom: string; feishu: string }>(await GetWebhookConfig())
    dingtalk.value = cfg?.dingtalk || ''
    wecom.value = cfg?.wecom || ''
    feishu.value = cfg?.feishu || ''
  } catch {
    // 读取失败则打开空表单
  }
  show.value = true
}

async function save() {
  saving.value = true
  msg.value = ''
  try {
    await unwrap(await SetWebhookConfig(dingtalk.value.trim(), wecom.value.trim(), feishu.value.trim()))
    show.value = false
  } catch (e) {
    msg.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function test(kind: 'dingtalk' | 'wecom' | 'feishu') {
  const url = (kind === 'dingtalk' ? dingtalk.value : kind === 'wecom' ? wecom.value : feishu.value).trim()
  if (!url) { msg.value = t('mon_nf_empty'); return }
  testing.value = kind
  msg.value = ''
  try {
    const r = await TestWebhook(kind, url)
    msg.value = (r && r.code !== 0) ? '❌ ' + r.msg : '✅ ' + t('mon_nf_test_ok')
  } catch (e) {
    msg.value = '❌ ' + getErrorMessage(e)
  } finally {
    testing.value = ''
  }
}
</script>

<template>
  <button class="nf-trigger" @click="open"><Bell :size="15" /> {{ label }}</button>

  <div v-if="show" class="modal-mask" @click.self="show = false">
    <div class="modal">
      <h3>{{ t('mon_nf_title') }}</h3>
      <p class="nf-hint">{{ t('mon_nf_hint') }}</p>

      <label>{{ t('mon_nf_dingtalk') }}</label>
      <div class="nf-row">
        <input v-model="dingtalk" class="modal-input" placeholder="https://oapi.dingtalk.com/robot/send?access_token=..." />
        <button class="nf-test" :disabled="testing === 'dingtalk'" @click="test('dingtalk')">
          {{ testing === 'dingtalk' ? '…' : t('mon_nf_test') }}
        </button>
      </div>

      <label>{{ t('mon_nf_wecom') }}</label>
      <div class="nf-row">
        <input v-model="wecom" class="modal-input" placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..." />
        <button class="nf-test" :disabled="testing === 'wecom'" @click="test('wecom')">
          {{ testing === 'wecom' ? '…' : t('mon_nf_test') }}
        </button>
      </div>

      <label>{{ t('mon_nf_feishu') }}</label>
      <div class="nf-row">
        <input v-model="feishu" class="modal-input" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..." />
        <button class="nf-test" :disabled="testing === 'feishu'" @click="test('feishu')">
          {{ testing === 'feishu' ? '…' : t('mon_nf_test') }}
        </button>
      </div>

      <div v-if="msg" class="nf-msg">{{ msg }}</div>

      <div class="modal-actions">
        <button class="btn-ghost" @click="show = false">{{ t('cancel') }}</button>
        <button class="btn-primary" :disabled="saving" @click="save">{{ t('save') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.nf-trigger {
  display: inline-flex; align-items: center; gap: 5px; padding: 7px 12px;
  background: transparent; color: var(--color-text-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  font-size: 13px; cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.nf-trigger:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.modal-mask {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5); display: flex;
  align-items: center; justify-content: center; z-index: 100;
}
.modal {
  width: 440px; max-height: 90vh; overflow-y: auto; background: var(--color-surface);
  border-radius: var(--radius-lg); padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.modal h3 { margin: 0 0 var(--space-3); font-size: 15px; color: var(--color-text-primary); }
.modal label { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--color-text-muted); margin: var(--space-3) 0 var(--space-1); }
.modal-input {
  width: 100%; box-sizing: border-box; height: 34px; padding: 0 10px; background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  color: var(--color-text-primary); font-size: 13px; font-family: inherit; outline: none;
}
.modal-input:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }

.nf-hint { font-size: 11px; color: var(--color-text-disabled); margin: 0 0 var(--space-2); line-height: 1.6; }
.nf-row { display: flex; gap: var(--space-2); align-items: center; }
.nf-row .modal-input { flex: 1; min-width: 0; }
.nf-test {
  flex-shrink: 0; height: 34px; padding: 0 14px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border-radius: var(--radius-md); font-size: 12px; cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.nf-test:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-hover); }
.nf-test:disabled { opacity: 0.5; cursor: default; }
.nf-msg { margin-top: var(--space-3); font-size: 12px; color: var(--color-text-secondary); word-break: break-all; line-height: 1.5; }

.modal-actions { display: flex; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-5); }
.btn-ghost { padding: 6px 14px; border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-ghost:hover { background: var(--color-bg-hover); }
.btn-primary { padding: 6px 14px; border: none; background: var(--color-accent); color: #fff; border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-primary:hover:not(:disabled) { background: var(--color-accent-hover); }
.btn-primary:disabled { opacity: 0.5; cursor: default; }
</style>
