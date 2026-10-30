<script lang="ts" setup>
/**
 * 单主机「一次性执行一条命令」。
 *
 * 为什么需要它：`SshService.Exec` 一直在后端躺着（非交互、跑完即断、带超时），
 * 但前端只有「开终端会话」一条路。为了看一眼 `uptime` 就开一个 xterm、
 * 建一条长连接、退出时再断一次，代价明显不成比例——尤其是主机多的时候。
 *
 * 与批量执行的分工：批量是「同一条命令发到 N 台」，这里是「一台机器跑一条」。
 * 两者调用的是不同的后端入口（Exec 一次性连接 vs BatchRun 并发 + 事件推送），
 * 但风险判定必须共用同一份规则：这里同样走 SshBatchPlan 预演，
 * 而不是前端自己匹配关键词——规则表复制到前端必然漂移，
 * 届时界面说「低风险」而实际命中了 high。
 *
 * 安全底线与批量执行一致：
 *  1. 风险说明排在执行按钮之前（放在下面等于让用户先点了才看到）；
 *  2. 生产机或高风险命令要点两次，且命令一改就立刻收回确认态；
 *  3. 拿不到风险标注时按最保守处理——不预演就不给执行。
 *
 * 与批量不同的一点：这里没有「停止」。Exec 是一次阻塞调用，后端没有
 * 提供取消通道，画一个停止按钮只会让用户以为按了就真的停了。超时由
 * 后端兜底，界面如实显示「执行中」。
 */
import { computed, onBeforeUnmount, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle, Check, Copy, LoaderCircle, Play, ShieldAlert, SquareTerminal, X } from '@lucide/vue'
import { Exec, SshBatchPlan } from '../../bindings/quickdock/services/sshx/sshservice'
import { apiErrorData, unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { copyToClipboard } from '../utils/clipboard'
import { useFocusTrap } from '../utils/focusTrap'
import {
  sshEnvClass,
  sshEnvKey,
  sshTargetFull,
  type SshBatchPlan as BatchPlan,
  type SshExecResult,
  type SshHost,
} from '../utils/ssh'

const props = defineProps<{
  visible: boolean
  /** 要执行命令的主机；null 时弹窗内容为空（正常不会发生） */
  host: SshHost | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()

const panelRef = ref<HTMLElement | null>(null)
const cmdRef = ref<HTMLTextAreaElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

/** 执行结果。字段名与后端 ExecResult 的 json tag 完全一致（见 utils/ssh）。 */
const command = ref('')
const timeoutSec = ref(30)
const running = ref(false)
const plan = ref<BatchPlan | null>(null)
const planErr = ref('')
/** 已生效的风险预演对应的命令；用来判断当前输入是否还没被预演过 */
const plannedCmd = ref('')
const out = ref<SshExecResult | null>(null)
const execErr = ref('')
const copied = ref(false)
/** 二次确认已武装 */
const armed = ref(false)

/**
 * 请求序号：关窗重开后，上一次还在途中的 Exec 返回时不该再往界面里写。
 * 组件常驻（只切 visible），没有 Unmount 可以依赖。
 */
let runSeq = 0
let planTimer: ReturnType<typeof setTimeout> | null = null
let armTimer: ReturnType<typeof setTimeout> | null = null
let copyTimer: ReturnType<typeof setTimeout> | null = null

const hostId = computed(() => props.host?.id || '')

const riskLevel = computed(() => plan.value?.riskLevel || '')
const needsConfirm = computed(() =>
  !!plan.value && (plan.value.prodCount > 0 || plan.value.riskLevel === 'high'),
)

/**
 * 风险预演是否覆盖了当前命令。
 *
 * 只有「预演过这条命令」才允许执行：否则用户改完命令直接点执行，
 * 走的是上一条命令的风险判定，等于绕过了安全提示。
 */
const planFresh = computed(() => plannedCmd.value === command.value.trim())

const canRun = computed(() =>
  !running.value
  && !!hostId.value
  && command.value.trim().length > 0
  && planFresh.value
  && !!(plan.value && (plan.value.hosts?.length ?? 0) > 0),
)

const runHint = computed(() => {
  if (running.value) return ''
  if (!command.value.trim()) return t('sshBatchNoCommand')
  if (!planFresh.value) return t('sshQuickExecPlanning')
  if (!plan.value || (plan.value.hosts?.length ?? 0) === 0) return planErr.value || t('sshQuickExecNoHost')
  return ''
})

/** 合并 stdout/stderr：先错误后正常会看不出因果，保持「正常输出在前、错误在后」的终端直觉。 */
const outputText = computed(() => {
  const o = out.value
  if (!o) return ''
  const parts: string[] = []
  if (o.stdout) parts.push(o.stdout.trimEnd())
  if (o.stderr) parts.push(o.stderr.trimEnd())
  return parts.join('\n').trim() || t('sshBatchEmptyOutput')
})

function disarm() {
  armed.value = false
  if (armTimer) {
    clearTimeout(armTimer)
    armTimer = null
  }
}

/**
 * 命令变化 → 重新预演。
 *
 * 250ms 去抖：预演本身只读本地 DB 并跑一遍规则表，但没必要每个字符都来一次。
 * 更要紧的是立刻收回二次确认——确认态必须绑在具体命令内容上。
 */
function schedulePlan() {
  disarm()
  if (planTimer) clearTimeout(planTimer)
  const cmd = command.value.trim()
  if (!cmd) {
    plan.value = null
    planErr.value = ''
    plannedCmd.value = ''
    return
  }
  planTimer = setTimeout(() => { void loadPlan() }, 250)
}

async function loadPlan() {
  const id = hostId.value
  const cmd = command.value.trim()
  if (!id || !cmd) {
    plan.value = null
    plannedCmd.value = ''
    return
  }
  try {
    // 单主机也用批量预演接口：规则与「哪些主机不能执行」的判定只有一份实现。
    plan.value = unwrap<BatchPlan>(await SshBatchPlan({ hostIds: [id], command: cmd })) || null
    plannedCmd.value = cmd
    planErr.value = ''
  } catch (e) {
    // 拿不到预演结果就不放行——见 canRun / runHint
    plan.value = null
    plannedCmd.value = ''
    planErr.value = getErrorMessage(e)
  }
}

async function onRunClick() {
  if (!canRun.value) return
  if (needsConfirm.value && !armed.value) {
    armed.value = true
    if (armTimer) clearTimeout(armTimer)
    // 5 秒自动撤回：不信这个计时器，用户改完别的回来随手一点就绕过了确认。
    armTimer = setTimeout(disarm, 5000)
    return
  }
  disarm()
  await run()
}

async function run() {
  const id = hostId.value
  const cmd = command.value.trim()
  if (!id || !cmd) return
  const seq = ++runSeq
  running.value = true
  out.value = null
  execErr.value = ''
  copied.value = false
  try {
    const res = unwrap<SshExecResult>(await Exec(id, cmd, Number(timeoutSec.value) || 0))
    if (seq !== runSeq) return
    out.value = res
  } catch (e) {
    if (seq !== runSeq) return
    // 失败也把已收到的半截输出带回来：后端 failSshWith 把 out 放进了 data.result，
    // 丢掉它就只剩一句「执行失败」，看不出卡在哪一步。
    const partial = apiErrorData<{ result?: SshExecResult }>(e)?.result
    if (partial) out.value = partial
    execErr.value = getErrorMessage(e)
  } finally {
    if (seq === runSeq) running.value = false
  }
}

async function copyOutput() {
  const ok = await copyToClipboard(outputText.value)
  if (!ok) return
  copied.value = true
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { copied.value = false }, 1500)
}

/** 结果里的耗时：毫秒级差异是「这台机器快不快」的唯一线索，别取整成秒。 */
function fmtDuration(ms: number): string {
  if (!ms) return ''
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(1)} s`
}

function requestClose() {
  // 执行中关窗：命令已经在机器上跑，后端也没有取消通道，所以不弹确认
  // （弹了也停不下来，只会让用户以为停下来）。只作废本次结果。
  if (running.value) runSeq++
  running.value = false
  disarm()
  emit('close')
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    requestClose()
    return
  }
  onKeydownTrap(e)
}

/** 命令框里 Ctrl/Cmd+Enter 直接执行，省一次挪鼠标。 */
function onCmdKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
    e.preventDefault()
    void onRunClick()
  }
}

watch(
  () => props.visible,
  (v) => {
    if (!v) return
    // 每次打开都是一次全新的操作：留着上次的命令和输出会让人以为那是刚跑的结果。
    command.value = ''
    timeoutSec.value = 30
    running.value = false
    plan.value = null
    planErr.value = ''
    plannedCmd.value = ''
    out.value = null
    execErr.value = ''
    copied.value = false
    runSeq++
    disarm()
    // 焦点落在命令框而不是开关按钮上（焦点陷阱默认聚焦第一个可聚焦元素＝关闭键，
    // 对「进来就是为了敲一条命令」的弹窗来说那是一次多余的 Tab）。
    // 用 rAF 而不是 nextTick：陷阱也在 rAF 里聚焦，注册在前所以先跑，这样能盖住它。
    requestAnimationFrame(() => cmdRef.value?.focus())
  },
)

onBeforeUnmount(() => {
  if (planTimer) clearTimeout(planTimer)
  if (armTimer) clearTimeout(armTimer)
  if (copyTimer) clearTimeout(copyTimer)
})
</script>

<template>
  <Teleport to="body">
    <Transition name="qex">
      <div v-if="visible" class="qex-overlay" @mousedown.self="requestClose" @keydown="onKeydown">
        <div ref="panelRef" class="qex-panel" role="dialog" aria-modal="true">
          <header class="qex-head">
            <SquareTerminal :size="15" class="qex-head-icon" />
            <div class="qex-head-text">
              <span class="qex-title">{{ t('sshQuickExec') }}</span>
              <span class="qex-sub">{{ t('sshQuickExecHint') }}</span>
            </div>
            <button class="qex-x" :title="t('close')" @click="requestClose">
              <X :size="15" />
            </button>
          </header>

          <div class="qex-body">
            <!-- 目标主机：执行前必须一眼看清是哪台，尤其是同名不同环境那种 -->
            <div v-if="host" class="qex-target">
              <span class="qex-target-name">{{ host.name || host.host }}</span>
              <span class="qex-env" :class="sshEnvClass(host.env)">{{ t(sshEnvKey(host.env)) }}</span>
              <span class="qex-target-addr">{{ sshTargetFull(host) }}</span>
            </div>

            <label class="qex-label" for="qex-cmd">{{ t('sshBatchCommand') }}</label>
            <textarea
              id="qex-cmd"
              ref="cmdRef"
              v-model="command"
              class="qex-textarea"
              rows="4"
              spellcheck="false"
              :disabled="running"
              :placeholder="t('sshBatchCommandPlaceholder')"
              @input="schedulePlan"
              @keydown="onCmdKeydown"
            />

            <div class="qex-opts">
              <label class="qex-opt">
                <span>{{ t('sshBatchTimeout') }}</span>
                <input v-model.number="timeoutSec" type="number" min="1" max="600" :disabled="running" />
              </label>
              <span class="qex-kbd">{{ t('sshQuickExecShortcut') }}</span>
            </div>

            <!-- 风险提示在按钮之前：放在下面等于让用户先点了才看到 -->
            <div v-if="plan?.prodCount" class="qex-warn">
              <ShieldAlert :size="13" />
              <span>{{ t('sshQuickExecProdWarn') }}</span>
            </div>
            <div v-if="riskLevel" class="qex-risk" :data-level="riskLevel">
              <AlertTriangle :size="13" />
              <div class="qex-risk-text">
                <strong>{{ t('sshBatchHighRisk') }}</strong>
                <span>{{ plan?.riskNote }}</span>
              </div>
            </div>
            <div v-if="planErr" class="qex-warn">
              <AlertTriangle :size="13" />
              <span>{{ planErr }}</span>
            </div>

            <div class="qex-actions">
              <span v-if="runHint" class="qex-run-hint">{{ runHint }}</span>
              <button
                class="qex-run"
                :class="{ armed, danger: needsConfirm }"
                :disabled="!canRun"
                @click="onRunClick"
              >
                <LoaderCircle v-if="running" :size="12" class="qex-spin" />
                <Play v-else :size="12" />
                <span>{{ running ? t('sshQuickExecRunning') : (armed ? t('sshBatchRunConfirm') : t('sshBatchRun')) }}</span>
              </button>
            </div>

            <!-- 结果区 -->
            <div v-if="out || execErr" class="qex-result">
              <div class="qex-result-head">
                <span class="qex-result-title">{{ t('sshQuickExecOutput') }}</span>
                <span class="qex-result-meta">
                  <template v-if="out">
                    <span
                      class="qex-code"
                      :data-ok="out.exitCode === 0 ? '1' : '0'"
                    >{{ t('sshBatchExitCode', { code: out.exitCode }) }}</span>
                    <em>{{ fmtDuration(out.durationMs) }}</em>
                  </template>
                  <span v-if="out?.truncated" class="qex-trunc">{{ t('sshQuickExecTruncated') }}</span>
                </span>
                <button class="qex-mini" :title="t('sshQuickExecCopy')" @click="copyOutput">
                  <Check v-if="copied" :size="11" />
                  <Copy v-else :size="11" />
                </button>
              </div>
              <div v-if="execErr" class="qex-err">{{ execErr }}</div>
              <pre v-if="out" class="qex-out">{{ outputText }}</pre>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.qex-overlay {
  position: fixed;
  inset: 0;
  z-index: 3100;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

.qex-panel {
  display: flex;
  flex-direction: column;
  width: 560px;
  max-width: calc(100vw - 40px);
  max-height: 84vh;
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 16px 48px var(--color-bg-overlay);
  overflow: hidden;
}

.qex-head {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: 12px 14px;
  border-bottom: 1px solid var(--color-border);
}
.qex-head-icon { color: var(--color-accent); margin-top: 1px; }
.qex-head-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.qex-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.qex-sub { font-size: 10.5px; color: var(--color-text-muted); }
.qex-x {
  margin-left: auto;
  display: flex; align-items: center;
  padding: 3px; border: none; border-radius: var(--radius-sm);
  background: none; color: var(--color-text-muted); cursor: pointer;
}
.qex-x:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.qex-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: 12px 14px 14px;
}

.qex-target {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  padding: var(--space-2) var(--space-3);
  font-size: 11.5px;
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-xs);
}
.qex-target-name {
  font-weight: 600;
  color: var(--color-text-primary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.qex-target-addr {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--color-text-muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.qex-env {
  flex: 0 0 auto;
  padding: 0 5px;
  font-size: 9.5px;
  line-height: 1.6;
  border-radius: var(--radius-full);
  border: 1px solid transparent;
}
.qex-env.env-prod {
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}
.qex-env.env-staging {
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-color: var(--color-warning-border);
}
.qex-env.env-test {
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}

.qex-label { font-size: 11.5px; font-weight: 600; color: var(--color-text-primary); }

.qex-textarea {
  width: 100%;
  padding: var(--space-3);
  font-family: var(--font-mono, monospace);
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
  resize: vertical;
}
.qex-textarea:focus { border-color: var(--color-accent); }

.qex-opts { display: flex; align-items: flex-end; gap: var(--space-4); }
.qex-opt {
  display: flex; flex-direction: column; gap: 3px;
  font-size: 10.5px; color: var(--color-text-muted);
}
.qex-opt input {
  width: 72px;
  padding: 3px 6px;
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
}
.qex-opt input:focus { border-color: var(--color-accent); }
.qex-kbd { margin-left: auto; font-size: 10px; color: var(--color-text-disabled); }

.qex-warn,
.qex-risk {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-3);
  font-size: 11px;
  line-height: 1.6;
  border-radius: var(--radius-xs);
  border: 1px solid transparent;
}
.qex-warn {
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-color: var(--color-warning-border);
}
.qex-risk {
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}
.qex-risk[data-level='high'] {
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}
.qex-risk-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.qex-risk-text strong { font-weight: 600; }

.qex-actions { display: flex; align-items: center; justify-content: flex-end; gap: var(--space-3); }
.qex-run-hint { margin-right: auto; font-size: 10.5px; color: var(--color-text-muted); }
.qex-run {
  display: inline-flex; align-items: center; gap: var(--space-2);
  padding: 5px 14px;
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-accent-text);
  background: var(--color-accent);
  border: 1px solid var(--color-accent);
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: filter var(--transition-fast);
}
.qex-run:hover:not(:disabled) { filter: brightness(1.06); }
.qex-run.danger {
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}
.qex-run.armed {
  font-weight: 600;
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger);
  box-shadow: 0 0 0 1px var(--color-danger);
}
.qex-run:disabled { cursor: default; opacity: 0.45; }

.qex-spin { animation: qex-spin 900ms linear infinite; }
@keyframes qex-spin { to { transform: rotate(360deg); } }

.qex-result {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-3);
  border-top: 1px solid var(--color-border);
}
.qex-result-head { display: flex; align-items: center; gap: var(--space-2); }
.qex-result-title { font-size: 11.5px; font-weight: 600; color: var(--color-text-primary); }
.qex-result-meta {
  display: inline-flex; align-items: center; gap: var(--space-2);
  margin-left: auto;
  font-size: 10px;
  color: var(--color-text-muted);
}
.qex-result-meta em { font-style: normal; opacity: 0.75; }
.qex-code { font-family: var(--font-mono); }
.qex-code[data-ok='1'] { color: var(--color-success); }
.qex-code[data-ok='0'] { color: var(--color-warning); }
.qex-trunc { color: var(--color-warning); }
.qex-mini {
  display: flex; align-items: center;
  padding: 2px 5px;
  color: var(--color-text-muted);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}
.qex-mini:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.qex-err {
  padding: var(--space-2) var(--space-3);
  font-size: 11px;
  line-height: 1.55;
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-xs);
}

.qex-out {
  margin: 0;
  padding: var(--space-3);
  max-height: 260px;
  overflow: auto;
  font-family: var(--font-mono, monospace);
  font-size: 10.5px;
  line-height: 1.55;
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  white-space: pre-wrap;
  word-break: break-all;
}

.qex-enter-active, .qex-leave-active { transition: opacity 120ms ease; }
.qex-enter-from, .qex-leave-to { opacity: 0; }
</style>
