<script lang="ts" setup>
/**
 * SSH 批量执行弹窗：选一批主机，把同一条命令并发发下去，逐台看结果。
 *
 * 为什么是一个弹窗而不是新页面：它是一次「操作」而不是一个「场所」。
 * 用户从主机树进来、跑完就走，结果只在这次操作内有意义（后端也不落库，
 * 只有一条 activity 记录）。做成页面就要处理「上次那批结果还算不算数」。
 *
 * 三条安全约束在这里落地（与 SSH 一键执行同一套底线）：
 *  1. **风险说明排在执行按钮之前**——把「这条命令会删库」放在按钮下面，
 *     等于让用户先点了再看到，那时命令已经发出去了。
 *  2. **生产环境 / 高风险要按两次**——第一次只把按钮换成「再点一次确认」，
 *     不发任何字节。批量执行影响的是 N 台机器，误触的成本是线性的。
 *  3. **风险等级由后端判定**（SshBatchPlan），前端不自己匹配关键词。
 *     规则表只有一份（services/sshx/ai.go 的 sshDangerRule），复制到前端
 *     必然漂移，届时界面说「低风险」而实际命中 high。
 *
 * 逐台结果是事件推来的（quickdock:ssh:batch-host），不是等接口返回：
 * 20 台主机最坏要跑几分钟，同步等就只能在界面上转圈。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import {
  AlertTriangle, Check, ChevronDown, ChevronRight, Folder, Play, Server, ShieldAlert, Square, X,
} from '@lucide/vue'
import {
  SshBatchCancel,
  SshBatchPlan,
  SshBatchRun,
} from '../../bindings/quickdock/services/sshx/sshservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../composables/useToast'
import { useFocusTrap } from '../utils/focusTrap'
import {
  batchRowStatus,
  sshEnvClass,
  sshEnvKey,
  sshTargetFull,
  type SshBatchHostResult,
  // 别名：绑定里导出的函数也叫 SshBatchPlan（SshBatchPlan(...)），
  // 同名类型会撞车。类型在 utils/ssh.ts，函数在 bindings，两者必须区分开。
  type SshBatchPlan as BatchPlan,
  type SshBatchStart,
  type SshBatchStatus,
  type SshBatchSummary,
  type SshHost,
} from '../utils/ssh'
import { buildSshTree, flattenSshTree } from '../utils/sshTree'

const props = defineProps<{
  hosts: SshHost[]
  /** 打开时要预先勾选的主机 id（从分组菜单进来时带上整个子树） */
  preselect?: string[]
}>()

const visible = defineModel<boolean>('visible', { required: true })

const { t } = useI18n()
const { confirm, error: toastError, success: toastSuccess } = useToast()

const EVENT_BATCH_HOST = 'quickdock:ssh:batch-host'
const EVENT_BATCH_DONE = 'quickdock:ssh:batch-done'

const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(visible, panelRef)

const selected = ref<Set<string>>(new Set())
const command = ref('')
const timeoutSec = ref(30)
const concurrency = ref(6)

const plan = ref<BatchPlan | null>(null)
const planErr = ref('')
const running = ref(false)
/** 二次确认已武装：按钮文案变成「再点一次确认」，几秒内没点就自动撤回 */
const armed = ref(false)
const batchId = ref('')
const results = ref<Record<string, SshBatchHostResult>>({})
/** 结果行的顺序（开跑那一刻确定的快照，不随勾选变化） */
const order = ref<string[]>([])
const summary = ref<SshBatchSummary | null>(null)
const expanded = ref<Set<string>>(new Set())

let offHost: (() => void) | null = null
let offDone: (() => void) | null = null
let planTimer: ReturnType<typeof setTimeout> | null = null
let armTimer: ReturnType<typeof setTimeout> | null = null

/** 勾选面板：复用主树的建树/展平逻辑，缩进与分组顺序和左树完全一致。 */
const allNodes = computed(() => flattenSshTree(buildSshTree(props.hosts), new Set()))

const hostTotal = computed(() => props.hosts.filter(h => !h.isFolder).length)

/** 某节点（文件夹含整棵子树）里的主机 id。 */
function hostIdsUnder(nodeId: string, isFolder: boolean): string[] {
  if (!isFolder) return [nodeId]
  const out: string[] = []
  const walk = (id: string) => {
    for (const h of props.hosts) {
      if (h.parentId !== id) continue
      if (h.isFolder) walk(h.id)
      else out.push(h.id)
    }
  }
  walk(nodeId)
  return out
}

function isChecked(hostId: string, isFolder: boolean): boolean {
  const ids = hostIdsUnder(hostId, isFolder)
  return ids.length > 0 && ids.every(id => selected.value.has(id))
}

function toggle(hostId: string, isFolder: boolean) {
  if (running.value) return
  const ids = hostIdsUnder(hostId, isFolder)
  const next = new Set(selected.value)
  const all = ids.every(id => next.has(id))
  for (const id of ids) {
    if (all) next.delete(id)
    else next.add(id)
  }
  selected.value = next
  schedulePlan()
}

function selectAll() {
  if (running.value) return
  selected.value = new Set(props.hosts.filter(h => !h.isFolder).map(h => h.id))
  schedulePlan()
}

function clearAll() {
  if (running.value) return
  disarm()
  selected.value = new Set()
  plan.value = null
}

/**
 * 预演请求去抖 250ms。
 *
 * 勾选一批主机时会连发十几个 toggle，逐个请求既浪费也会让风险提示闪烁。
 * 但**不能省掉这次请求**：风险等级与生产机数量都必须由后端算。
 */
function schedulePlan() {
  // 目标或命令一变，之前的「已确认」立刻作废。
  //
  // 不做这一步会留下一个真实的安全缺口：用户先确认了 `systemctl status nginx`，
  // 回来把命令改成 `restart`，然后再点一次——按钮还是「确认」状态，
  // 于是一次点击就把没被确认过的命令发了出去。二次确认必须绑到**具体内容**上。
  disarm()
  if (planTimer) clearTimeout(planTimer)
  if (selected.value.size === 0) {
    plan.value = null
    planErr.value = ''
    return
  }
  planTimer = setTimeout(() => { void loadPlan() }, 250)
}

async function loadPlan() {
  if (selected.value.size === 0) {
    plan.value = null
    return
  }
  try {
    plan.value = unwrap<BatchPlan>(
      await SshBatchPlan({ hostIds: [...selected.value], command: command.value }),
    ) || null
    planErr.value = ''
  } catch (e) {
    // 预演失败不挡住执行按钮之外的任何东西——但要说清楚为什么没有风险提示。
    // 拿不到风险标注时必须按最保守处理：见 canRun 里的 plan 判空。
    planErr.value = getErrorMessage(e)
    plan.value = null
  }
}

const canRun = computed(() =>
  !running.value
  && selected.value.size > 0
  && command.value.trim().length > 0
  && !!(plan.value && (plan.value.hosts?.length ?? 0) > 0),
)

/**
 * 是否需要二次确认。
 *
 * 判定条件是「生产机」或「高风险命令」——普通测试机上跑 uptime 不值得
 * 让用户点两次，次数多了用户会养成无脑连点的习惯，那时二次确认就失效了。
 * 反过来，只要沾上生产机或命中 high 规则，就必须点两次。
 */
const needsConfirm = computed(() =>
  !!plan.value && (plan.value.prodCount > 0 || plan.value.riskLevel === 'high'),
)

const riskLevel = computed(() => plan.value?.riskLevel || '')
const runHint = computed(() => {
  if (running.value) return ''
  if (!selected.value.size) return t('sshBatchNoHost')
  if (!command.value.trim()) return t('sshBatchNoCommand')
  return ''
})

function disarm() {
  armed.value = false
  if (armTimer) {
    clearTimeout(armTimer)
    armTimer = null
  }
}

async function onRunClick() {
  if (!canRun.value) return
  if (needsConfirm.value && !armed.value) {
    armed.value = true
    if (armTimer) clearTimeout(armTimer)
    // 5 秒后自动撤回：留着不撤的「再点一次确认」会在用户改完命令、
    // 过一会儿回来随手一点时直接放行，等于二次确认被时间稀释没了。
    armTimer = setTimeout(disarm, 5000)
    return
  }
  disarm()
  await startRun()
}

async function startRun() {
  // 行顺序取后端解析出的 hosts（可能已剔除刚被删掉的主机），
  // 而不是前端勾选集合——那是打开弹窗那一刻的快照。
  order.value = (plan.value?.hosts || []).map(h => h.hostId)
  results.value = {}
  expanded.value = new Set()
  summary.value = null
  // id 由前端生成并**先**赋值：第一台可能在后端返回之前就跑完了，
  // 事件到达时前端必须已经有一个可比的 id，否则那条结果会被自己过滤掉。
  batchId.value = newBatchId()
  running.value = true
  try {
    const start = unwrap<SshBatchStart>(await SshBatchRun({
      batchId: batchId.value,
      hostIds: [...selected.value],
      command: command.value,
      timeoutSec: Number(timeoutSec.value) || 0,
      concurrency: Number(concurrency.value) || 0,
    }))
    // 后端兜底生成 id（正常不会发生）时以它为准，否则一条事件都收不到。
    if (start?.batchId && start.batchId !== batchId.value) batchId.value = start.batchId
  } catch (e) {
    running.value = false
    batchId.value = ''
    toastError(getErrorMessage(e))
  }
}

function newBatchId(): string {
  const c = globalThis.crypto
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  return `batch-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

async function stop() {
  if (!running.value || !batchId.value) return
  try {
    unwrap(await SshBatchCancel(batchId.value))
  } catch (e) {
    toastError(getErrorMessage(e))
  }
  // 不在这里置 running=false：还在途中的主机要继续把结果报回来，
  // 等后端推 batch-done 才算真正结束（那时才知道有多少台没跑）。
}

/**
 * 关闭。
 *
 * 执行中关闭必须先问：关掉之后结果就没地方看了，而命令**已经在机器上跑**，
 * 静默关窗会让用户以为它停了。确认后取消，再关。
 */
async function requestClose() {
  if (running.value) {
    const ok = await confirm(t('sshBatchCloseConfirm'))
    if (!ok) return
    try {
      unwrap(await SshBatchCancel(batchId.value))
    } catch {
      // 取消失败不阻止关闭：用户已经决定走了，卡在这里更糟。
    }
    // 立刻解绑这个批次：后续到达的事件不该再往一个已经在关闭的窗口里写。
    batchId.value = ''
    running.value = false
  }
  disarm()
  visible.value = false
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    void requestClose()
    return
  }
  onKeydownTrap(e)
}

function rowOf(hostId: string): SshBatchHostResult | null {
  return results.value[hostId] || null
}

function statusOf(hostId: string): SshBatchStatus {
  const res = rowOf(hostId)
  if (res) return batchRowStatus(res, running.value)
  if (running.value) return 'running'
  // 已经结束但这一台没有任何结果：只可能是被取消（后端未执行的那部分）。
  return summary.value?.aborted ? 'canceled' : 'pending'
}

const STATUS_KEY: Record<SshBatchStatus, string> = {
  pending: 'sshBatchStatusPending',
  running: 'sshBatchStatusRunning',
  ok: 'sshBatchStatusOk',
  failed: 'sshBatchStatusFailed',
  errored: 'sshBatchStatusErrored',
  canceled: 'sshBatchStatusCanceled',
}

function rowsOf(): { hostId: string; name: string; addr: string; env: string }[] {
  // 结果表用 order（开跑快照），主机的展示名从 hosts 里补：
  // 后端结果里带了 name/addr/env，但还没回报的行只能靠本地数据。
  const byId = new Map(props.hosts.map(h => [h.id, h]))
  return order.value.map(id => {
    const res = results.value[id]
    const h = byId.get(id)
    return {
      hostId: id,
      name: res?.name || h?.name || h?.host || id,
      addr: res?.addr || (h ? sshTargetFull(h) : ''),
      env: res?.env || h?.env || '',
    }
  })
}

function outputOf(hostId: string): string {
  const res = results.value[hostId]
  if (!res) return ''
  const parts: string[] = []
  if (res.stdout) parts.push(res.stdout)
  if (res.stderr) parts.push(res.stderr)
  return parts.join('\n').trim() || t('sshBatchEmptyOutput')
}

function toggleExpand(hostId: string) {
  const next = new Set(expanded.value)
  if (next.has(hostId)) next.delete(hostId)
  else next.add(hostId)
  expanded.value = next
}

/**
 * 每台的耗时。
 *
 * 不复用 utils/ssh 的 formatDuration：那个是终端会话存活时长，按秒取整，
 * 300ms 会显示成 "0s"——而批量执行里「哪台快哪台慢」正是要看的，
 * 毫秒级差异必须保留。
 */
function batchDuration(ms: number): string {
  if (!ms) return ''
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(1)} s`
}

/** 首次打开（或有新的预选）时重置状态。 */
function reset() {
  disarm()
  selected.value = new Set((props.preselect || []).filter(id => {
    const h = props.hosts.find(x => x.id === id)
    return !!h && !h.isFolder
  }))
  command.value = ''
  timeoutSec.value = 30
  concurrency.value = 6
  plan.value = null
  planErr.value = ''
  running.value = false
  batchId.value = ''
  results.value = {}
  order.value = []
  summary.value = null
  expanded.value = new Set()
  if (selected.value.size) void loadPlan()
}

watch(visible, v => {
  if (v) reset()
})

onMounted(() => {
  offHost = Events.On(EVENT_BATCH_HOST, (ev: unknown) => {
    const res = (ev as { data?: SshBatchHostResult })?.data
    // 只认自己这一批：同一时刻可能有另一批在跑（另一个窗口/另一个标签）。
    if (!res || !res.hostId || res.batchId !== batchId.value) return
    // 整体替换而不是原地改属性：results 是 ref 里的普通对象，
    // 原地赋值在深层属性上要靠 Vue 的响应式代理兜底，替换最稳。
    results.value = { ...results.value, [res.hostId]: res }
  })
  offDone = Events.On(EVENT_BATCH_DONE, (ev: unknown) => {
    const sum = (ev as { data?: SshBatchSummary })?.data
    if (!sum || sum.batchId !== batchId.value) return
    summary.value = sum
    running.value = false
    disarm()
    const msg = t(sum.aborted ? 'sshBatchDoneAborted' : 'sshBatchDone', {
      ok: sum.succeeded,
      fail: sum.failed,
      err: sum.errored + sum.canceled,
    })
    if (sum.aborted || sum.errored > 0) toastError(msg)
    else toastSuccess(msg)
  })
})

onBeforeUnmount(() => {
  offHost?.()
  offHost = null
  offDone?.()
  offDone = null
  if (planTimer) clearTimeout(planTimer)
  if (armTimer) clearTimeout(armTimer)
})
</script>

<template>
  <Teleport to="body">
    <Transition name="batch">
      <div v-if="visible" class="batch-overlay" @mousedown.self="requestClose" @keydown="onKeydown">
        <div ref="panelRef" class="batch-panel" role="dialog" aria-modal="true">
          <header class="batch-head">
            <div class="batch-head-text">
              <span class="batch-title">{{ t('sshBatchTitle') }}</span>
              <span class="batch-sub">{{ t('sshBatchSubtitle') }}</span>
            </div>
            <button class="batch-x" :title="running ? t('sshBatchStop') : t('close')" @click="requestClose">
              <X :size="16" />
            </button>
          </header>

          <div class="batch-body">
            <!-- 左：目标主机勾选 -->
            <aside class="batch-pick">
              <div class="batch-pick-head">
                <span class="batch-pick-title">{{ t('sshBatchHosts') }}</span>
                <em class="batch-pick-count">{{ t('sshBatchSelected', { n: selected.size }) }}</em>
              </div>
              <div class="batch-pick-tools">
                <button class="batch-mini" :disabled="running || !hostTotal" @click="selectAll">
                  {{ t('sshBatchSelectAll') }}
                </button>
                <button class="batch-mini" :disabled="running || !selected.size" @click="clearAll">
                  {{ t('sshBatchClear') }}
                </button>
              </div>
              <div class="batch-pick-list">
                <button
                  v-for="n in allNodes"
                  :key="n.host.id"
                  class="batch-pick-row"
                  :class="{ folder: n.kind === 'folder', on: isChecked(n.host.id, n.kind === 'folder') }"
                  :style="{ paddingLeft: 8 + n.depth * 14 + 'px' }"
                  :disabled="running"
                  @click="toggle(n.host.id, n.kind === 'folder')"
                >
                  <span class="batch-box" :class="{ on: isChecked(n.host.id, n.kind === 'folder') }">
                    <Check v-if="isChecked(n.host.id, n.kind === 'folder')" :size="10" />
                  </span>
                  <Folder v-if="n.kind === 'folder'" :size="12" class="batch-pick-icon" />
                  <Server v-else :size="12" class="batch-pick-icon" />
                  <span class="batch-pick-name">{{ n.host.name || n.host.host }}</span>
                  <span
                    v-if="n.kind === 'host'"
                    class="batch-env"
                    :class="sshEnvClass(n.host.env)"
                  >{{ t(sshEnvKey(n.host.env)) }}</span>
                </button>
                <p v-if="!hostTotal" class="batch-pick-empty">{{ t('sshPickHost') }}</p>
              </div>
            </aside>

            <!-- 右：命令 + 风险 + 操作 -->
            <section class="batch-cmd">
              <label class="batch-label" for="batch-cmd-input">{{ t('sshBatchCommand') }}</label>
              <textarea
                id="batch-cmd-input"
                v-model="command"
                class="batch-textarea"
                rows="5"
                spellcheck="false"
                :disabled="running"
                :placeholder="t('sshBatchCommandPlaceholder')"
                @input="schedulePlan"
              />

              <div class="batch-opts">
                <label class="batch-opt">
                  <span>{{ t('sshBatchTimeout') }}</span>
                  <input v-model.number="timeoutSec" type="number" min="1" max="600" :disabled="running" />
                </label>
                <label class="batch-opt" :title="t('sshBatchConcurrencyHint')">
                  <span>{{ t('sshBatchConcurrency') }}</span>
                  <input v-model.number="concurrency" type="number" min="1" max="16" :disabled="running" />
                </label>
              </div>

              <!--
                风险与生产机提示必须在按钮**之前**。
                放在按钮下方等于让用户先点了才看到——那时命令已经发出去了。
              -->
              <div v-if="plan?.prodCount" class="batch-warn">
                <ShieldAlert :size="13" />
                <span>{{ t('sshBatchProdWarn', { n: plan.prodCount }) }}</span>
              </div>
              <div v-if="riskLevel" class="batch-risk" :data-level="riskLevel">
                <AlertTriangle :size="13" />
                <div class="batch-risk-text">
                  <strong>{{ t('sshBatchHighRisk') }}</strong>
                  <span>{{ plan?.riskNote }}</span>
                </div>
              </div>
              <div v-if="planErr" class="batch-warn">
                <AlertTriangle :size="13" />
                <span>{{ planErr }}</span>
              </div>

              <div v-if="plan?.skipped?.length" class="batch-skip">
                <div class="batch-skip-head">
                  <AlertTriangle :size="12" />
                  <span>{{ t('sshBatchSkipped', { n: plan.skipped.length }) }}</span>
                </div>
                <p class="batch-skip-hint">{{ t('sshBatchSkippedHint') }}</p>
                <ul class="batch-skip-list">
                  <li v-for="s in plan.skipped" :key="s.hostId">
                    <span class="batch-skip-name">{{ s.name || s.hostId }}</span>
                    <span class="batch-skip-why">{{ s.detail }}</span>
                  </li>
                </ul>
              </div>

              <div class="batch-actions">
                <span v-if="runHint" class="batch-run-hint">{{ runHint }}</span>
                <button
                  v-if="running"
                  class="batch-btn batch-stop"
                  @click="stop"
                >
                  <Square :size="12" />
                  <span>{{ t('sshBatchStop') }}</span>
                </button>
                <button
                  v-else
                  class="batch-btn batch-run"
                  :class="{ armed, danger: needsConfirm }"
                  :disabled="!canRun"
                  @click="onRunClick"
                >
                  <Play :size="12" />
                  <span>{{ armed ? t('sshBatchRunConfirm') : t('sshBatchRun') }}</span>
                </button>
              </div>
            </section>
          </div>

          <!-- 结果区：开跑后出现，逐台落行 -->
          <div v-if="order.length" class="batch-results">
            <div class="batch-results-head">
              <span class="batch-results-title">{{ t('sshBatchResults') }}</span>
              <span v-if="summary" class="batch-results-sum">
                {{ t(summary.aborted ? 'sshBatchDoneAborted' : 'sshBatchDone', {
                  ok: summary.succeeded,
                  fail: summary.failed,
                  err: summary.errored + summary.canceled,
                }) }}
                <em>{{ batchDuration(summary.durationMs) }}</em>
              </span>
            </div>
            <div class="batch-result-list">
              <div v-for="row in rowsOf()" :key="row.hostId" class="batch-result">
                <button class="batch-result-row" @click="toggleExpand(row.hostId)">
                  <ChevronDown v-if="expanded.has(row.hostId)" :size="12" />
                  <ChevronRight v-else :size="12" />
                  <span class="batch-result-name">{{ row.name }}</span>
                  <span class="batch-result-addr">{{ row.addr }}</span>
                  <span class="batch-env" :class="sshEnvClass(row.env)">{{ t(sshEnvKey(row.env)) }}</span>
                  <span class="batch-result-meta">
                    <template v-if="rowOf(row.hostId)?.errorCode">
                      <span class="batch-code">{{ rowOf(row.hostId)?.error || t(STATUS_KEY[statusOf(row.hostId)]) }}</span>
                    </template>
                    <template v-else-if="rowOf(row.hostId) && rowOf(row.hostId)!.exitCode !== 0">
                      {{ t('sshBatchExitCode', { code: rowOf(row.hostId)!.exitCode }) }}
                    </template>
                    <em>{{ batchDuration(rowOf(row.hostId)?.durationMs || 0) }}</em>
                  </span>
                  <span class="batch-badge" :data-status="statusOf(row.hostId)">
                    {{ t(STATUS_KEY[statusOf(row.hostId)]) }}
                  </span>
                </button>
                <pre v-if="expanded.has(row.hostId)" class="batch-result-out">{{ outputOf(row.hostId) }}</pre>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.batch-overlay {
  position: fixed;
  inset: 0;
  z-index: 1300;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

.batch-panel {
  display: flex;
  flex-direction: column;
  width: 780px;
  max-width: 94vw;
  max-height: 86vh;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 16px 56px var(--color-bg-overlay);
  overflow: hidden;
}

.batch-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-4);
  flex: 0 0 auto;
  padding: var(--space-5) var(--space-6);
  border-bottom: 1px solid var(--color-border);
}

.batch-head-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.batch-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.batch-sub {
  font-size: 11px;
  color: var(--color-text-muted);
}

.batch-x {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.batch-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.batch-body {
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
}

/* ---- 左侧勾选 ---- */
.batch-pick {
  display: flex;
  flex-direction: column;
  flex: 0 0 264px;
  min-height: 0;
  border-right: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}

.batch-pick-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-4) var(--space-2);
}

.batch-pick-title {
  font-size: 11.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.batch-pick-count {
  font-size: 10.5px;
  font-style: normal;
  color: var(--color-text-muted);
}

.batch-pick-tools {
  display: flex;
  gap: var(--space-2);
  padding: 0 var(--space-4) var(--space-3);
}

.batch-mini {
  padding: 2px 7px;
  font-size: 10.5px;
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.batch-mini:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.batch-mini:disabled {
  cursor: default;
  opacity: 0.5;
}

.batch-pick-list {
  flex: 1 1 auto;
  min-height: 120px;
  overflow-y: auto;
  padding-bottom: var(--space-3);
}

.batch-pick-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  padding: 5px 8px 5px 8px;
  font-size: 11.5px;
  font-family: inherit;
  text-align: left;
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  cursor: pointer;
}

.batch-pick-row:hover:not(:disabled) {
  background: var(--color-bg-hover);
}

.batch-pick-row.on {
  color: var(--color-text-primary);
}

.batch-pick-row.folder {
  font-weight: 600;
  color: var(--color-text-primary);
}

.batch-pick-row:disabled {
  cursor: default;
  opacity: 0.6;
}

/* 自绘勾选框：原生 checkbox 在暗色主题下与整体风格完全脱节，
   且无法表达「分组半选」这种状态（这里用整组全选/全不选规避了半选）。 */
.batch-box {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  width: 13px;
  height: 13px;
  color: var(--color-accent-text);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: 3px;
}

.batch-box.on {
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.batch-pick-icon {
  flex: 0 0 auto;
  color: var(--color-text-muted);
}

.batch-pick-name {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.batch-pick-empty {
  margin: 0;
  padding: var(--space-4);
  font-size: 11px;
  color: var(--color-text-muted);
}

/* ---- 环境徽标（与主树同一套色）---- */
.batch-env {
  flex: 0 0 auto;
  padding: 0 5px;
  font-size: 9.5px;
  line-height: 1.6;
  border-radius: var(--radius-full);
  border: 1px solid transparent;
}

.batch-env.env-prod {
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}

.batch-env.env-staging {
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-color: var(--color-warning-border);
}

.batch-env.env-test {
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}

/* ---- 右侧命令区 ---- */
.batch-cmd {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  padding: var(--space-4) var(--space-6) var(--space-5);
  overflow-y: auto;
}

.batch-label {
  font-size: 11.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.batch-textarea {
  width: 100%;
  padding: var(--space-3);
  font-family: var(--font-mono, monospace);
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
  resize: vertical;
}

.batch-textarea:focus {
  border-color: var(--color-accent);
}

.batch-opts {
  display: flex;
  gap: var(--space-4);
}

.batch-opt {
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.batch-opt input {
  width: 76px;
  padding: 3px 6px;
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
}

.batch-opt input:focus {
  border-color: var(--color-accent);
}

/* ---- 提示块 ---- */
.batch-warn,
.batch-risk,
.batch-skip {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-3);
  font-size: 11px;
  line-height: 1.6;
  border-radius: var(--radius-xs);
  border: 1px solid transparent;
}

.batch-warn {
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-color: var(--color-warning-border);
}

.batch-risk {
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}

.batch-risk[data-level='high'] {
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}

.batch-risk-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.batch-risk-text strong {
  font-weight: 600;
}

.batch-skip {
  flex-direction: column;
  gap: var(--space-2);
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border-color: var(--color-border);
}

.batch-skip-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-weight: 600;
  color: var(--color-warning);
}

.batch-skip-hint {
  margin: 0;
  color: var(--color-text-muted);
}

.batch-skip-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.batch-skip-list li {
  display: flex;
  gap: var(--space-2);
}

.batch-skip-name {
  flex: 0 0 auto;
  color: var(--color-text-primary);
}

.batch-skip-why {
  min-width: 0;
  color: var(--color-text-muted);
}

/* ---- 操作按钮 ---- */
.batch-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-3);
  margin-top: auto;
  padding-top: var(--space-2);
}

.batch-run-hint {
  margin-right: auto;
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.batch-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: 5px 14px;
  font-size: 11.5px;
  font-family: inherit;
  border-radius: var(--radius-xs);
  border: 1px solid transparent;
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}

.batch-run {
  color: var(--color-accent-text);
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.batch-run:hover:not(:disabled) {
  filter: brightness(1.06);
}

.batch-run.danger {
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}

.batch-run.armed {
  font-weight: 600;
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger);
  box-shadow: 0 0 0 1px var(--color-danger);
}

.batch-run:disabled {
  cursor: default;
  opacity: 0.45;
}

.batch-stop {
  color: var(--color-danger-text);
  background: transparent;
  border-color: var(--color-danger-border);
}

.batch-stop:hover {
  background: var(--color-danger-bg);
}

/* ---- 结果区 ---- */
.batch-results {
  display: flex;
  flex-direction: column;
  flex: 0 0 auto;
  max-height: 42%;
  min-height: 0;
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}

.batch-results-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-6);
}

.batch-results-title {
  font-size: 11.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.batch-results-sum {
  display: inline-flex;
  gap: var(--space-2);
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.batch-result-list {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding-bottom: var(--space-3);
}

.batch-result-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  padding: 4px var(--space-6);
  font-size: 11px;
  font-family: inherit;
  text-align: left;
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  cursor: pointer;
}

.batch-result-row:hover {
  background: var(--color-bg-hover);
}

.batch-result-name {
  flex: 0 0 auto;
  max-width: 160px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  color: var(--color-text-primary);
}

.batch-result-addr {
  flex: 0 0 auto;
  font-size: 10px;
  color: var(--color-text-muted);
}

.batch-result-meta {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 1 1 auto;
  min-width: 0;
  justify-content: flex-end;
  font-size: 10px;
  color: var(--color-text-muted);
}

.batch-result-meta em {
  font-style: normal;
  opacity: 0.75;
}

.batch-code {
  max-width: 260px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.batch-badge {
  flex: 0 0 auto;
  min-width: 44px;
  padding: 1px 6px;
  font-size: 9.5px;
  text-align: center;
  border-radius: var(--radius-full);
  border: 1px solid transparent;
}

.batch-badge[data-status='pending'] {
  color: var(--color-text-disabled);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}

.batch-badge[data-status='running'] {
  color: var(--color-accent);
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border);
}

.batch-badge[data-status='ok'] {
  color: var(--color-success);
  background: var(--color-success-bg);
  border-color: var(--color-success-border);
}

.batch-badge[data-status='failed'] {
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border-color: var(--color-warning-border);
}

.batch-badge[data-status='errored'] {
  color: var(--color-danger-text);
  background: var(--color-danger-bg);
  border-color: var(--color-danger-border);
}

.batch-badge[data-status='canceled'] {
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
}

.batch-result-out {
  margin: 0 var(--space-6) var(--space-2);
  padding: var(--space-3);
  max-height: 180px;
  overflow: auto;
  font-family: var(--font-mono, monospace);
  font-size: 10.5px;
  line-height: 1.55;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  white-space: pre-wrap;
  word-break: break-all;
}

/* 过渡 */
.batch-enter-active,
.batch-leave-active {
  transition: opacity 120ms ease;
}

.batch-enter-from,
.batch-leave-to {
  opacity: 0;
}
</style>
