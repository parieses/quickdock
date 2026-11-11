<script setup lang="ts">
/**
 * 邮件原文查看器。
 *
 * 为什么按需重新 FETCH，而不是把正文塞进卡片快照：正文不落库、不常驻内存是
 * 这个模块的硬约定；快照里有 N 个账号 × 20 封，常驻全文既浪费又扩大暴露面。
 * 点开时按 UID 用 BODY.PEEK 拉一次，用完即弃，也不会把邮件标成已读。
 *
 * HTML 邮件走隔离渲染：后端把保留标签的 text/html 原文送回来，这里用
 * buildMailDocument 清洗后塞进 sandbox="" 的 iframe srcdoc —— 不用 v-html，
 * 无脚本 / 无同源 / 无导航，远程图片默认剥掉、由用户主动勾选才加载。
 * 纯文本与原始源码仍作为兜底 Tab，经 {{ }} 转义显示。
 */
import { ref, computed, toRef, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { X, Copy, Check } from '@lucide/vue'
import { GetMessage } from '../../bindings/quickdock/services/mailbox/mailboxservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { copyToClipboard } from '../utils/clipboard'
import { useFocusTrap } from '../utils/focusTrap'
import { buildMailDocument, mailHtmlToText } from '../utils/mailHtml'

// 返回值经 ApiResult.Data 传回，绑定生成器不会产出具名接口，本地声明与
// DashboardPage 的 MailCode 保持同一种做法。
interface MailDetail {
  id: string; uid: number; account: string; from: string; to: string
  subject: string; date: string; text: string; html: string; raw: string
  truncated: boolean
}

const props = defineProps<{
  visible: boolean
  /** 卡片上的 id（邮箱/UID），同时用于后端鉴权「只能看快照里这封」 */
  mailId: string
  /** 卡片行快照：请求中或失败时至少还能显示主题 / 发件人 / 时间 */
  fallback?: { subject: string; from: string; account: string; received: number } | null
}>()

const emit = defineEmits<{ (e: 'close'): void }>()
const { t } = useI18n()

const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

const detail = ref<MailDetail | null>(null)
const loading = ref(false)
const errMsg = ref('')
const tab = ref<'render' | 'text' | 'raw'>('text')
const allowRemote = ref(false)
const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null

watch(
  () => [props.visible, props.mailId] as const,
  async ([vis, id]) => {
    if (!vis || !id) return
    detail.value = null
    errMsg.value = ''
    copied.value = false
    tab.value = 'text'
    allowRemote.value = false
    loading.value = true
    try {
      const d = unwrap<MailDetail>(await GetMessage(id))
      if (d) {
        detail.value = d
        // 有 HTML 就默认渲染；纯文本邮件直接落在「正文」Tab，不出现空 Tab。
        tab.value = d.html?.trim() ? 'render' : 'text'
      }
    } catch (e) {
      errMsg.value = getErrorMessage(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

// 复制按钮始终给纯文本：渲染视图复制 HTML 标签没意义，源码 Tab 才复制源码。
const shownText = computed(() => {
  const d = detail.value
  if (!d) return ''
  if (tab.value === 'raw') return d.raw
  return d.text || (d.html ? mailHtmlToText(d.html) : '')
})

const renderDoc = computed(() => {
  const d = detail.value
  return d?.html ? buildMailDocument(d.html, allowRemote.value) : ''
})

const subjectText = computed(() => detail.value?.subject || props.fallback?.subject || t('mailDetailTitle'))
const fromText = computed(() => detail.value?.from || props.fallback?.from || '-')
const toText = computed(() => detail.value?.to || '-')
const accountText = computed(() => detail.value?.account || props.fallback?.account || '-')
const dateText = computed(() => {
  if (detail.value?.date) return detail.value.date
  const ts = props.fallback?.received
  return ts ? new Date(ts).toLocaleString() : '-'
})

async function copyAll() {
  if (!shownText.value) return
  if (!(await copyToClipboard(shownText.value))) return
  copied.value = true
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { copied.value = false }, 1600)
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') { emit('close'); return }
  onKeydownTrap(e)
}

onUnmounted(() => { if (copyTimer) clearTimeout(copyTimer) })
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="visible" class="md-overlay" @mousedown.self="emit('close')" @keydown="onKeydown">
        <div ref="panelRef" class="md-panel" @mousedown.stop>
          <header class="md-head">
            <span class="md-title">{{ t('mailDetailTitle') }}</span>
            <button class="md-icon" :title="t('close')" @click="emit('close')"><X :size="15" /></button>
          </header>

          <div class="md-subject" :title="subjectText">{{ subjectText }}</div>
          <dl class="md-meta">
            <div><dt>{{ t('mailDetailFrom') }}</dt><dd :title="fromText">{{ fromText }}</dd></div>
            <div><dt>{{ t('mailDetailTo') }}</dt><dd :title="toText">{{ toText }}</dd></div>
            <div><dt>{{ t('mailDetailDate') }}</dt><dd :title="dateText">{{ dateText }}</dd></div>
            <div><dt>{{ t('mailDetailAccount') }}</dt><dd :title="accountText">{{ accountText }}</dd></div>
          </dl>

          <div class="md-tabs">
            <button v-if="detail?.html" :class="{ on: tab === 'render' }" @click="tab = 'render'">{{ t('mailDetailTabRender') }}</button>
            <button :class="{ on: tab === 'text' }" @click="tab = 'text'">{{ t('mailDetailTabText') }}</button>
            <button :class="{ on: tab === 'raw' }" @click="tab = 'raw'">{{ t('mailDetailTabRaw') }}</button>
            <label v-if="tab === 'render'" class="md-remote">
              <input v-model="allowRemote" type="checkbox" />{{ t('mailDetailRemoteImages') }}
            </label>
            <span class="md-hint">{{ t('mailDetailSafeHint') }}</span>
          </div>
          <p v-if="detail?.truncated" class="md-trunc">{{ t('mailDetailTruncated') }}</p>

          <div class="md-body-wrap">
            <p v-if="loading" class="md-state">{{ t('mailDetailLoading') }}</p>
            <p v-else-if="errMsg" class="md-state err">{{ errMsg }}</p>
            <!-- sandbox 无脚本 / 无同源 / 无导航；srcdoc 已清洗，邮件自带样式仍在 -->
            <iframe v-else-if="tab === 'render' && detail?.html" class="md-frame"
                    :srcdoc="renderDoc" sandbox="" referrerpolicy="no-referrer"
                    :title="t('mailDetailTabRender')"></iframe>
            <!-- 纯插值：Vue 自动转义，源码里的标签 / 脚本一律当文本显示 -->
            <pre v-else class="md-body">{{ shownText || t('mailDetailEmpty') }}</pre>
          </div>

          <footer class="md-footer">
            <button class="md-btn" :disabled="!shownText" @click="copyAll">
              <Check v-if="copied" :size="13" /><Copy v-else :size="13" />
              {{ copied ? t('mailDetailCopied') : t('mailDetailCopy') }}
            </button>
            <button class="md-btn primary" @click="emit('close')">{{ t('close') }}</button>
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.md-overlay {
  position: fixed; inset: 0; z-index: 20000;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
  display: flex; align-items: center; justify-content: center;
}
.md-panel {
  display: flex; flex-direction: column;
  width: min(760px, 92vw); height: min(620px, 78vh);
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  padding: 14px 16px 12px;
}
.md-head { display: flex; align-items: center; gap: 8px; flex: 0 0 auto; }
.md-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.md-icon {
  margin-left: auto; display: inline-flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; padding: 0; border: none; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-muted); cursor: pointer;
}
.md-icon:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.md-subject {
  flex: 0 0 auto; margin: 8px 0 10px; font-size: 14px; font-weight: 500;
  color: var(--color-text-primary); line-height: 1.5;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.md-meta {
  flex: 0 0 auto; display: grid; grid-template-columns: 1fr 1fr;
  gap: 6px 14px; margin: 0 0 12px; padding: 10px 12px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.md-meta div { display: flex; min-width: 0; gap: 8px; font-size: 11px; line-height: 1.5; }
.md-meta dt { flex: 0 0 52px; color: var(--color-text-disabled); }
.md-meta dd {
  min-width: 0; margin: 0; color: var(--color-text-secondary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.md-tabs { flex: 0 0 auto; display: flex; align-items: center; gap: 6px; margin-bottom: 8px; }
.md-tabs button {
  height: 24px; padding: 0 10px; border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); background: transparent;
  color: var(--color-text-muted); font-family: inherit; font-size: 11px; cursor: pointer;
}
.md-tabs button:hover { color: var(--color-text-primary); }
.md-tabs button.on {
  background: var(--color-accent-bg); border-color: var(--color-accent-border);
  color: var(--color-accent);
}
.md-remote {
  display: inline-flex; align-items: center; gap: 4px; margin-left: 6px;
  font-size: 10px; color: var(--color-text-muted); cursor: pointer; white-space: nowrap;
}
/* 勾选框尺寸/勾型由 style.css 的全局自绘规则唯一负责，这里只留布局。 */
.md-remote input { margin: 0; }
.md-hint { margin-left: auto; font-size: 10px; color: var(--color-text-disabled); white-space: nowrap; }
.md-trunc { flex: 0 0 auto; margin: -2px 0 6px; font-size: 10px; color: var(--color-warning); }

.md-body-wrap {
  flex: 1; min-height: 0; overflow: hidden;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-primary);
}
/* 邮件正文按白纸渲染：邮件 CSS 是按白底设计的，跟着深色主题反色会糊成一团。 */
.md-frame {
  display: block; width: 100%; height: 100%; border: 0; background: #fff;
}
.md-body {
  height: 100%; margin: 0; padding: 12px 14px; overflow: auto;
  font-family: ui-monospace, Consolas, monospace; font-size: 12px; line-height: 1.7;
  color: var(--color-text-secondary);
  white-space: pre-wrap; word-break: break-word;
}
.md-state {
  margin: 0; padding: 24px 14px; text-align: center;
  font-size: 12px; color: var(--color-text-disabled);
}
.md-state.err { color: var(--color-danger-text); }

.md-footer { flex: 0 0 auto; display: flex; justify-content: flex-end; gap: 8px; margin-top: 10px; }
.md-btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 5px;
  height: 28px; padding: 0 12px; border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); background: transparent;
  color: var(--color-text-secondary); font-family: inherit; font-size: 12px; cursor: pointer;
}
.md-btn:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.md-btn:disabled { opacity: .4; cursor: default; }
.md-btn.primary { background: var(--color-accent-bg); border-color: var(--color-accent-border); color: var(--color-accent); }

.dialog-enter-active { transition: opacity .2s ease-out, transform .2s ease-out; }
.dialog-leave-active { transition: opacity .15s ease-in; }
.dialog-enter-from, .dialog-leave-to { opacity: 0; }
.dialog-enter-from .md-panel, .dialog-leave-to .md-panel { transform: scale(.97) translateY(-6px); }
</style>
