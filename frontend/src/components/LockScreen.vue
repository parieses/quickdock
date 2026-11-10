<script lang="ts" setup>
// 启动锁层。
//
// 主窗口与全部浮窗共用同一个 SPA（index.html + hash 路由），所以这一个组件
// 挂在 App.vue 模板最外层就等于覆盖了 6 个窗口——这也是本方案不逐个窗口
// 改热键回调的原因：锁定时热键照常打开窗口，窗口里显示的就是它。
//
// 设计上刻意做成「覆盖层」而不是「替换内容」：底层组件（笔记编辑器、xterm、
// AI 会话）保持挂载，解锁后原样回来，不会丢滚动位置、不会重建终端连接。
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { KeyRound, Loader2, Lock, ShieldCheck } from '@lucide/vue'
import { useI18n } from 'vue-i18n'
import { LockStatus, VerifyPassword, RecoverStartupPassword } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { setAppLocked } from '../utils/appLock'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'

const emit = defineEmits<{ (e: 'unlocked'): void }>()
const { t } = useI18n()

const password = ref('')
const recoveryCode = ref('')
const recoveryMode = ref(false)
const busy = ref(false)
const errMsg = ref('')
// 密码错时抖一下输入行：后端校验要 60~100ms（bcrypt 本身就是防爆破的，
// 不能为了手感去掉），光靠一行红字容易让人以为没点上。
const shaking = ref(false)
// retryLeft 由后端计时，前端只负责每秒减一：倒计时放前端等于给人留了绕过退避的入口。
const retryLeft = ref(0)

const rootEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLInputElement | null>(null)
let countdown: number | undefined
let shakeTimer: number | undefined

function focusInput() {
  nextTick(() => {
    const el = inputEl.value
    if (!el) return
    el.focus()
    // 光 focus 不够：密码框恢复焦点时把光标放到末尾，符合重输预期
    const end = el.value.length
    try { el.setSelectionRange(end, end) } catch (e) { logErr('LockScreen', e) }
  })
}

function triggerShake() {
  shaking.value = false
  nextTick(() => {
    shaking.value = true
    if (shakeTimer !== undefined) clearTimeout(shakeTimer)
    shakeTimer = window.setTimeout(() => { shaking.value = false }, 400)
  })
}

async function refreshRetry() {
  try {
    const st = unwrap<{ retryAfterSec?: number }>(await LockStatus())
    retryLeft.value = Math.max(0, Number(st?.retryAfterSec) || 0)
  } catch (e) {
    logErr('LockScreen.refreshRetry', e)
  }
  if (retryLeft.value > 0) startCountdown()
}

function startCountdown() {
  if (countdown !== undefined) return
  countdown = window.setInterval(() => {
    retryLeft.value = Math.max(0, retryLeft.value - 1)
    if (retryLeft.value === 0) {
      stopCountdown()
      focusInput()
    }
  }, 1000)
}

function stopCountdown() {
  if (countdown !== undefined) {
    clearInterval(countdown)
    countdown = undefined
  }
}

async function submit() {
  if (busy.value || retryLeft.value > 0) return
  const pwd = password.value
  if (!recoveryMode.value && !pwd) {
    errMsg.value = t('lockPasswordRequired')
    triggerShake()
    focusInput()
    return
  }
  if (recoveryMode.value && !recoveryCode.value.trim()) {
    errMsg.value = t('lockRecoveryRequired')
    triggerShake()
    focusInput()
    return
  }
  // busy 必须在第一个 await 之前置位：点击到下一帧之间没有任何变化，
  // 用户就会重复点、重复回车，反而更慢。
  busy.value = true
  errMsg.value = ''
  try {
    if (recoveryMode.value) {
      unwrap(await RecoverStartupPassword(recoveryCode.value))
    } else {
      unwrap(await VerifyPassword(pwd))
    }
    emit('unlocked')
  } catch (e) {
    errMsg.value = getErrorMessage(e) || t('lockFailed')
    password.value = ''
    triggerShake()
    // 失败后回读退避剩余：连续错到阈值时后端会开始拒答，前端要跟着禁用输入
    await refreshRetry()
    focusInput()
  } finally {
    busy.value = false
  }
}

function toggleRecovery() {
  recoveryMode.value = !recoveryMode.value
  errMsg.value = ''
  focusInput()
}

// 键盘兜底：底层组件（xterm / 编辑器 / 命令面板）各自监听了 keydown，
// 只靠「覆盖层 + 抢焦点」在焦点被别处抢走时挡不住。这里在 document 捕获阶段
// 把「不在锁层内」的按键吃掉并抢回焦点——注意放行 Escape，浮窗要靠它关闭。
function onGlobalKeyDown(e: KeyboardEvent) {
  if (e.key === 'Escape') return
  const target = e.target as Node | null
  if (target && rootEl.value?.contains(target)) return
  e.preventDefault()
  e.stopPropagation()
  focusInput()
}

// 锁层**内部**的按键也要截断冒泡。
//
// 上面那条只管「目标不在锁层内」，而最伤的是目标在锁层内这一支：document 上还挂着
// 一堆冒泡型全局快捷键（剪贴板面板 Enter = 复制选中项并关闭窗口、AI 页方向键、
// 看板快捷键……），它们只判断「是不是自己的目标」，锁层输入框里的回车会被它们
// 当成自己的——实测就是「输完密码一按回车，窗口直接关闭」。
// 捕获阶段的监听在根元素之前执行，这里拦不到，那些由 utils/appLock 的短路负责。
//
// Escape 例外：浮窗是 Frameless 的，Esc 是锁定时唯一的键盘逃生通道（关掉窗口）。
function onRootKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') return
  e.stopPropagation()
}

onMounted(() => {
  document.addEventListener('keydown', onGlobalKeyDown, true)
  // 让 document / window 上的捕获型监听知道现在锁定中，各自首行短路。
  // 与锁层可见性严格同步：挂载置位、卸载清位。
  setAppLocked(true)
  // 进入锁层时先清掉底层残留焦点（xterm 的光标、编辑器选区），否则首屏
  // 看起来「锁层上面还有个闪烁的光标」
  const active = document.activeElement as HTMLElement | null
  if (active && active !== document.body) active.blur()
  refreshRetry()
  focusInput()
})

onUnmounted(() => {
  document.removeEventListener('keydown', onGlobalKeyDown, true)
  setAppLocked(false)
  stopCountdown()
  if (shakeTimer !== undefined) clearTimeout(shakeTimer)
})
</script>

<template>
  <Teleport to="body">
    <div ref="rootEl" class="lock-root" @keydown="onRootKeydown">
      <!-- 浮窗是无边框窗口（Frameless），锁层一盖就没有可拖动的地方了，
           所以顶部留一条极窄的拖拽带，锁定时窗口仍能挪位置。 -->
      <div class="lock-drag" aria-hidden="true"></div>

      <div class="lock-card">
        <div class="lock-brand"><span class="lock-brand-dot"></span>QuickDock</div>

        <div class="lock-icon"><Lock :size="21" :stroke-width="2" /></div>
        <h1 class="lock-title">{{ t('lockTitle') }}</h1>
        <p class="lock-desc">{{ t('lockDesc') }}</p>

        <div class="lock-field" :class="{ 'is-shake': shaking }">
          <input
            v-if="!recoveryMode"
            ref="inputEl"
            v-model="password"
            class="lock-input"
            type="password"
            autocomplete="current-password"
            spellcheck="false"
            :placeholder="t('lockPasswordPlaceholder')"
            :disabled="busy || retryLeft > 0"
            @keydown.enter.prevent="submit"
          />
          <input
            v-else
            ref="inputEl"
            v-model="recoveryCode"
            class="lock-input lock-input-code"
            type="text"
            autocomplete="off"
            spellcheck="false"
            :placeholder="t('lockRecoveryPlaceholder')"
            :disabled="busy || retryLeft > 0"
            @keydown.enter.prevent="submit"
          />
          <button
            class="lock-go"
            :class="{ 'is-busy': busy }"
            :disabled="busy || retryLeft > 0"
            @click="submit"
          >
            <Loader2 v-if="busy" :size="16" class="lock-spin" />
            <template v-else-if="retryLeft > 0">{{ retryLeft }}s</template>
            <template v-else>{{ t('lockUnlock') }}</template>
          </button>
        </div>

        <p v-if="retryLeft > 0" class="lock-hint is-warn">{{ t('lockRetryAfter', { n: retryLeft }) }}</p>
        <p v-else-if="recoveryMode" class="lock-hint">
          <ShieldCheck :size="12" />{{ t('lockRecoveryHint') }}
        </p>
        <p v-else class="lock-hint">{{ t('lockEnterHint') }}</p>

        <p v-if="errMsg" class="lock-error">{{ errMsg }}</p>

        <div class="lock-foot">
          <button class="lock-link" @click="toggleRecovery">
            <KeyRound :size="13" />
            {{ recoveryMode ? t('lockBackToPassword') : t('lockForgot') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* 实色全屏遮罩：半透明会让人从缝里读到笔记标题、终端输出，那就等于没锁。
   两团极淡的强调色光斑只是为了让纯色底不显得像「页面加载失败的白/黑屏」。 */
.lock-root {
  position: fixed;
  inset: 0;
  /* 必须压过设置页(10000/10001)、Toast(10001) 与确认框(20000)：
     锁定时这些浮层若还在上面，等于给了一条读取数据的缝。 */
  z-index: 30000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-6);
  box-sizing: border-box;
  background-color: var(--color-bg-primary);
  background-image:
    radial-gradient(560px 300px at 50% -8%, var(--color-accent-bg), transparent 72%),
    radial-gradient(360px 240px at 108% 108%, var(--color-accent-bg), transparent 74%);
  user-select: none;
  outline: none;
}

.lock-drag {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 24px;
  --wails-draggable: drag;
}

.lock-card {
  position: relative;
  width: 100%;
  max-width: 340px;
  padding: var(--space-8) var(--space-7) var(--space-5);
  box-sizing: border-box;
  text-align: center;
  background: var(--color-popover-bg);
  border: 1px solid var(--color-border);
  border-radius: 16px;
  box-shadow: var(--shadow-pop);
  animation: lock-card-in 200ms cubic-bezier(0.22, 0.9, 0.3, 1);
}

/* 顶部 1px 高光：暗色下卡片边缘容易糊在背景里，加一道极淡的高光就有轮廓了 */
.lock-card::before {
  content: '';
  position: absolute;
  inset: 0 0 auto 0;
  height: 1px;
  border-radius: 16px 16px 0 0;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.09), transparent);
  pointer-events: none;
}

.lock-brand {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  margin-bottom: var(--space-6);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  color: var(--color-text-disabled);
}

.lock-brand-dot {
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background: var(--color-accent);
  box-shadow: 0 0 8px var(--color-accent-muted);
}

.lock-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 46px;
  height: 46px;
  margin: 0 auto var(--space-5);
  border-radius: 13px;
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.06);
}

.lock-title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--color-text-primary);
}

.lock-desc {
  margin: var(--space-3) 0 var(--space-6);
  font-size: 12px;
  line-height: 1.65;
  color: var(--color-text-muted);
}

.lock-field {
  display: flex;
  gap: var(--space-3);
}

.lock-field.is-shake {
  animation: lock-shake 380ms cubic-bezier(0.36, 0.07, 0.19, 0.97);
}

.lock-input {
  flex: 1;
  min-width: 0;
  height: 40px;
  padding: 0 var(--space-5);
  font-size: 13px;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-sizing: border-box;
  outline: none;
  transition: border-color 140ms ease, box-shadow 140ms ease;
}

.lock-input:focus {
  border-color: var(--color-border-focus);
  box-shadow: 0 0 0 3px var(--color-accent-bg);
}

.lock-input:disabled {
  color: var(--color-text-disabled);
  cursor: not-allowed;
}

.lock-input::placeholder {
  color: var(--color-text-disabled);
}

/* 恢复码比密码长，且带连字符：用等宽字体让 16 位一眼能对齐核对 */
.lock-input-code {
  font-family: var(--font-mono, monospace);
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.lock-go {
  display: flex;
  align-items: center;
  justify-content: center;
  /* 最小宽度固定：文案在「解锁 / 12s / spinner」之间切换时按钮不能跟着一起跳 */
  min-width: 62px;
  height: 40px;
  padding: 0 var(--space-4);
  flex-shrink: 0;
  font-size: 13px;
  font-weight: 500;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  color: var(--color-accent-text);
  background: var(--color-accent);
  border: none;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color 140ms ease, transform 120ms ease, opacity 140ms ease;
}

.lock-go:hover:not(:disabled) {
  background: var(--color-accent-hover);
}

.lock-go:active:not(:disabled) {
  transform: scale(0.96);
}

.lock-go:disabled {
  background: var(--color-bg-active);
  color: var(--color-text-disabled);
  cursor: not-allowed;
}

.lock-go.is-busy {
  background: var(--color-accent);
  color: var(--color-accent-text);
}

.lock-spin {
  animation: lock-spin 800ms linear infinite;
}

.lock-hint {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  margin: var(--space-4) 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-disabled);
}

.lock-hint.is-warn {
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.lock-error {
  margin: var(--space-4) 0 0;
  padding: var(--space-3) var(--space-4);
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-sm);
}

.lock-foot {
  display: flex;
  justify-content: center;
  margin-top: var(--space-5);
  padding-top: var(--space-4);
  border-top: 1px solid var(--color-border);
}

.lock-link {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2);
  font-size: 12px;
  color: var(--color-text-muted);
  background: none;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: color 140ms ease, background-color 140ms ease;
}

.lock-link:hover {
  color: var(--color-accent);
  background: var(--color-accent-bg);
}

/* 浮窗比主窗口小得多（剪贴板 480×540），常规尺寸下卡片会顶满甚至溢出：
   收窄留白、去掉品牌行与说明，保证密码框和按钮永远在一屏内。 */
@media (max-width: 620px), (max-height: 520px) {
  .lock-card {
    max-width: 300px;
    padding: var(--space-6) var(--space-5) var(--space-4);
    border-radius: var(--radius-lg);
  }

  .lock-brand {
    display: none;
  }

  .lock-icon {
    width: 40px;
    height: 40px;
    margin-bottom: var(--space-4);
    border-radius: var(--radius-lg);
  }

  .lock-title {
    font-size: 16px;
  }

  .lock-desc {
    display: none;
  }

  .lock-foot {
    margin-top: var(--space-4);
    padding-top: var(--space-3);
  }
}

@keyframes lock-card-in {
  from {
    opacity: 0;
    transform: translateY(6px) scale(0.985);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

@keyframes lock-shake {
  0%, 100% { transform: translateX(0); }
  20% { transform: translateX(-6px); }
  40% { transform: translateX(5px); }
  60% { transform: translateX(-3px); }
  80% { transform: translateX(2px); }
}

@keyframes lock-spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .lock-card,
  .lock-field.is-shake,
  .lock-spin {
    animation: none;
  }
}
</style>
