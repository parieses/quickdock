<script setup lang="ts">
/**
 * 全局密码索取弹层。
 *
 * 挂在 App.vue，全应用唯一实例。任何地方调 requestCredential() 都会走到这里。
 *
 * 三条必须守住的交互约束（都是本项目踩过的坑）：
 *  1. 所有 keydown 一律 stop —— SSH / 命令面板 / 剪贴板这些窗口在 document 的
 *     **捕获阶段**挂了键盘监听，不拦就会出现「输完密码按回车，窗口直接关了」
 *     （见 utils/appLock.ts 头注释记录的事故）。
 *  2. Esc = 取消，Enter 只在输入框内提交。弹层外面点一下也取消，
 *     但点面板内部不能冒泡出去（@mousedown.stop）。
 *  3. 取消必须 resolve(null)，绝不能 resolve 空密码——调用方拿到空密码会
 *     拿它去连接/推送，表现为「密码错误」而不是「用户取消」。
 */
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, EyeOff, KeyRound, Wand2 } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useCredentialQueue, type CredentialSummary } from '../composables/useCredential'
import { useFocusTrap } from '../utils/focusTrap'
import { GeneratePassword, MatchCredentials, RevealCredential, SaveCredential } from '../../bindings/quickdock/services/credentials/credentialsservice'

const { t } = useI18n()
const { items, resolve } = useCredentialQueue()

/** 队首请求：一次只显示一个，提交后自动显示下一个。 */
const current = computed(() => items[0] ?? null)

const form = reactive({ username: '', secret: '', save: true })
const picks = ref<CredentialSummary[]>([])
const pickedId = ref('')
const showSecret = ref(false)
const submitting = ref(false)
const errText = ref('')

const panelRef = ref<HTMLElement | null>(null)
const usernameRef = ref<HTMLInputElement | null>(null)
const secretRef = ref<HTMLInputElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(computed(() => !!current.value), panelRef)

/** 推荐列表只在「允许从库里选」时加载。 */
const allowPick = computed(() => current.value?.req.allowPick !== false)
const allowSave = computed(() => current.value?.req.allowSave !== false)

function resetForm() {
  const req = current.value?.req
  form.username = req?.username ?? ''
  form.secret = ''
  form.save = true
  picks.value = []
  pickedId.value = ''
  showSecret.value = false
  errText.value = ''
  submitting.value = false
}

/** 有推荐条目时聚焦列表（选比打字快），否则直接聚焦用户名/密码框。 */
async function focusFirst() {
  await nextTick()
  if (picks.value.length > 0) {
    panelRef.value?.querySelector<HTMLElement>('.cred-pick')?.focus()
    return
  }
  if (!form.username) usernameRef.value?.focus()
  else secretRef.value?.focus()
}

async function loadSuggestions() {
  const req = current.value?.req
  if (!req || req.allowPick === false) return
  try {
    const res = await MatchCredentials(req.kind, req.host ?? '', req.port ?? 0, req.username ?? '')
    picks.value = unwrap<CredentialSummary[]>(res) ?? []
    // 只预选不预填：解密是有成本的，且用户可能只是想手输一条新的。
    pickedId.value = picks.value[0]?.id ?? ''
  } catch (e) {
    // 推荐失败不该挡住手输：静默降级成「只能手动填」。
    logErr('CredentialPrompt.suggest', e)
    picks.value = []
    pickedId.value = ''
  }
}

// 换一条请求就整套重置：上一条的密码绝不能留在输入框里带到下一个场景。
watch(() => current.value?.id, (id) => {
  if (id == null) return
  resetForm()
  void loadSuggestions().then(focusFirst)
}, { immediate: true })

async function pickOne(item: CredentialSummary) {
  pickedId.value = item.id
  errText.value = ''
  if (!item.hasSecret) {
    form.username = item.username
    return
  }
  try {
    const res = await RevealCredential(item.id)
    const data = unwrap<{ id: string; username: string; secret: string }>(res)
    if (!data) return
    form.username = data.username
    form.secret = data.secret
    await nextTick()
    secretRef.value?.focus()
  } catch (e) {
    errText.value = getErrorMessage(e) || t('credPromptRevealFailed')
    logErr('CredentialPrompt.pick', e)
  }
}

async function genPassword() {
  try {
    const res = await GeneratePassword(20, true, true, true)
    const data = unwrap<{ password: string }>(res)
    if (data?.password) {
      form.secret = data.password
      showSecret.value = true
    }
  } catch (e) {
    logErr('CredentialPrompt.generate', e)
  }
}

const canSubmit = computed(() => form.secret.trim().length > 0 && !submitting.value)

/** 提交：勾了保存且不是「选了已有条目」时，先入库再返回。 */
async function submit() {
  const req = current.value?.req
  const id = current.value?.id
  if (!req || id == null || !canSubmit.value) return

  submitting.value = true
  errText.value = ''
  try {
    let credentialId = pickedId.value || undefined
    // 选了已有条目但改了密码 → 视为更新那条；
    // 手输新值且勾了保存 → 新建一条。
    const shouldSave = allowSave.value && form.save
    if (credentialId && shouldSave) {
      await SaveCredential({
        id: credentialId,
        name: picks.value.find((p) => p.id === credentialId)?.name ?? '',
        kind: req.kind,
        username: form.username.trim(),
        secret: form.secret,
        clearSecret: false,
        host: req.host ?? '',
        port: req.port ?? 0,
        url: '',
        notes: '',
        tags: '',
        source: '',
        sourceId: '',
        favorite: picks.value.find((p) => p.id === credentialId)?.favorite ?? false,
      })
    } else if (!credentialId && shouldSave && req.kind !== 'other') {
      const res = await SaveCredential({
        id: '',
        name: '',
        kind: req.kind,
        username: form.username.trim(),
        secret: form.secret,
        clearSecret: false,
        host: req.host ?? '',
        port: req.port ?? 0,
        url: '',
        notes: '',
        tags: '',
        source: '',
        sourceId: '',
        favorite: false,
      })
      const saved = unwrap<CredentialSummary>(res)
      credentialId = saved?.id
    }

    resolve(id, {
      credentialId,
      username: form.username.trim(),
      secret: form.secret,
      save: shouldSave,
    })
  } catch (e) {
    // 保存失败不阻断「这次用它」：密码已经拿到了，存不进去是次要问题，
    // 报错让用户知道「下次还得再输」即可。
    errText.value = getErrorMessage(e) || t('credPromptSaveFailed')
    logErr('CredentialPrompt.submit', e)
    submitting.value = false
  }
}

function cancel() {
  const id = current.value?.id
  if (id == null) return
  resolve(id, null)
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.stopPropagation()
    cancel()
    return
  }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="cred">
      <div
        v-if="current"
        class="cred-overlay"
        @mousedown.self="cancel"
        @keydown.stop="onKeydown"
      >
        <div ref="panelRef" class="cred-panel" role="dialog" aria-modal="true" @mousedown.stop>
          <header class="cred-head">
            <div class="cred-icon"><KeyRound :size="18" :stroke-width="2" /></div>
            <div class="cred-head-text">
              <h2 class="cred-title">{{ current.req.title }}</h2>
              <p v-if="current.req.hint" class="cred-hint">{{ current.req.hint }}</p>
            </div>
          </header>

          <div v-if="allowPick && picks.length" class="cred-picks">
            <button
              v-for="p in picks"
              :key="p.id"
              class="cred-pick"
              :class="{ 'is-active': p.id === pickedId }"
              type="button"
              @click="pickOne(p)"
            >
              <span class="cred-pick-name">{{ p.name }}</span>
              <span v-if="p.username" class="cred-pick-user">{{ p.username }}</span>
            </button>
          </div>
          <p v-else-if="allowPick" class="cred-empty">{{ t('credPromptNoMatch') }}</p>

          <label class="cred-field">
            <span class="cred-label">{{ t('credPromptUsername') }}</span>
            <input
              ref="usernameRef"
              v-model="form.username"
              class="qd-input"
              type="text"
              autocomplete="off"
              spellcheck="false"
              @keydown.enter.prevent="secretRef?.focus()"
            />
          </label>

          <label class="cred-field">
            <span class="cred-label">{{ t('credPromptPassword') }}</span>
            <div class="cred-pw-row">
              <input
                ref="secretRef"
                v-model="form.secret"
                class="qd-input cred-pw"
                :type="showSecret ? 'text' : 'password'"
                autocomplete="off"
                spellcheck="false"
                @keydown.enter.prevent="submit"
              />
              <button
                class="qd-icon-btn"
                type="button"
                :title="showSecret ? t('credPromptHide') : t('credPromptShow')"
                @click="showSecret = !showSecret"
              >
                <EyeOff v-if="showSecret" :size="15" />
                <Eye v-else :size="15" />
              </button>
              <button class="qd-icon-btn" type="button" :title="t('credPromptGenerate')" @click="genPassword">
                <Wand2 :size="15" />
              </button>
            </div>
          </label>

          <label v-if="allowSave" class="cred-save">
            <input v-model="form.save" type="checkbox" />
            <span>{{ t('credPromptSave') }}</span>
          </label>

          <p v-if="errText" class="cred-err">{{ errText }}</p>

          <footer class="cred-foot">
            <button class="qd-btn" type="button" @click="cancel">{{ t('cancel') }}</button>
            <button class="qd-btn qd-btn-primary" type="button" :disabled="!canSubmit" @click="submit">
              {{ t('credPromptUse') }}
            </button>
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.cred-overlay {
  position: fixed;
  inset: 0;
  z-index: 21000;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

/* 浮层必须用 popover-bg：三级面层是 bg-primary → card-bg → popover-bg，
   用 bg-secondary 会比底下的卡片更暗，看起来像挖了个洞。 */
.cred-panel {
  width: 420px;
  max-width: 92vw;
  padding: 18px 20px 16px;
  background: var(--color-popover-bg);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  box-shadow: 0 16px 56px var(--color-bg-overlay);
}

.cred-head {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  margin-bottom: 14px;
}

.cred-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border-radius: var(--radius-sm);
}

.cred-head-text { min-width: 0; }

.cred-title {
  margin: 0;
  font-size: var(--text-md);
  font-weight: 600;
  color: var(--color-text-primary);
}

.cred-hint {
  margin: 3px 0 0;
  font-size: var(--text-xs);
  line-height: 1.5;
  color: var(--color-text-muted);
}

.cred-picks {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 168px;
  overflow-y: auto;
  margin-bottom: 10px;
  padding: 3px;
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.cred-pick {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 6px 8px;
  background: none;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-sm);
  text-align: left;
  cursor: pointer;
}

.cred-pick:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.cred-pick.is-active {
  background: var(--color-accent-bg);
  color: var(--color-accent);
}

.cred-pick-name {
  flex-shrink: 0;
  max-width: 55%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cred-pick-user {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.cred-empty {
  margin: 0 0 10px;
  font-size: var(--text-xs);
  color: var(--color-text-disabled);
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

.cred-pw-row {
  display: flex;
  align-items: center;
  gap: 4px;
}

.cred-pw {
  flex: 1;
  min-width: 0;
}

.cred-save {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 12px;
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
  cursor: pointer;
}

.cred-err {
  margin: 0 0 10px;
  font-size: var(--text-xs);
  color: var(--color-danger);
}

.cred-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.cred-enter-active,
.cred-leave-active {
  transition: opacity var(--transition-fast);
}

.cred-enter-from,
.cred-leave-to {
  opacity: 0;
}

.cred-enter-from .cred-panel {
  transform: translateY(-6px) scale(0.98);
}
</style>
